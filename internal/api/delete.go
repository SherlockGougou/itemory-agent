package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SherlockGougou/itemory-agent/internal/config"
	"github.com/SherlockGougou/itemory-agent/internal/media"
	"github.com/SherlockGougou/itemory-agent/internal/store"
)

// trashDirName 是移动删除在库根下使用的回收站目录名。
//
// 以 "." 开头是刻意的：扫描器跳过所有点开头的目录（scanner.go 的
// strings.HasPrefix(name, ".")），回收站里的文件因此不会被重新索引。默认的
// excludePatterns 里还有 ".itemory" 这一条，Contains 匹配对 ".itemory-trash"
// 同样成立，构成第二重保护。
const trashDirName = ".itemory-trash"

// 删除接口的错误码。App 按这些码决定文案与是否允许重试。
const (
	errCodeDeleteDisabled = "delete_disabled"
	errCodeReadOnlyMount  = "read_only_mount"
	errCodeOutsideLibrary = "outside_library"
	errCodeNotFound       = "not_found"
	errCodeDeleteFailed   = "delete_failed"
)

// deleteDisabledMessage 指向配置文档，方便用户直接找到开关。
const deleteDisabledMessage = "deletion is disabled; set deleteMode to trash or permanent in the agent settings " +
	"(see docs/configuration.md) before retrying"

// readOnlyMountMessage 同时点明两层前提：挂载可写 + 删除模式已开启。
const readOnlyMountMessage = "the library directory is not writable; mount the photo directory read-write " +
	"(remove :ro from the compose volume) and see docs/configuration.md for the deleteMode setting"

// handleDeleteItem 删除一条媒体。
//
// 顺序是硬性的「先校验、后动手」：设置开关 → 条目解析 → 属库校验（字符串与解析
// 符号链接两遍）→ 可写性探测 → 文件操作。任何一步失败都不会碰用户文件。
//
// 文件操作成功之后才清缩略图、标记索引、广播事件：磁盘状态是唯一事实，索引与缓存
// 的收敛必须跟在它后面。反过来（先标记 removed）会让条目在任何接口里都查不到，
// 文件却还在盘上，用户既看不到也删不掉。
func (s *Server) handleDeleteItem(w http.ResponseWriter, r *http.Request) {
	settings := s.d.Settings.Get()
	if !deletionEnabled(settings.DeleteMode) {
		writeError(w, http.StatusForbidden, errCodeDeleteDisabled, deleteDisabledMessage)
		return
	}

	entry, ok, err := s.d.Index.GetByID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, errCodeNotFound, "no indexed entry with this id")
		return
	}
	// 片段不是用户可见条目：与 GET /items/{id} 一致地回落到它所属的动态照片，
	// 让「动态照片删一半」不会以 409 的形式甩给客户端。
	if entry.Kind == media.KindMotionClip {
		if still, found, err := s.d.Index.GetLivePhotoForClip(entry.ID); err == nil && found {
			entry = still
		}
	}

	// targets[0] 是索引里的这一条；动态照片的片段文件作为第二个目标一起处理，
	// 否则静帧删掉后片段会变成盘上一份谁也看不到的孤立文件。
	targets := []string{filepath.Clean(entry.Path)}
	if entry.MotionPath != "" {
		targets = append(targets, filepath.Clean(entry.MotionPath))
	}

	// 每个目标都解析出它所属的库根：动态照片的静帧与片段可能位于不同的库，
	// 回收站必须落在各自库根下，不能统一按静帧的库根来算。
	//
	// 校验全部通过之后才开始动文件：两道路径校验与文件夹浏览接口同一口径，
	// 先按字符串归一判一次，再解析符号链接判一次。库内指向库外的链接不会被第一道
	// 拦下，删除前必须被第二道拦下。
	existing := make([]deleteTarget, 0, len(targets))
	for _, target := range targets {
		if !withinLibraries(target, settings.Libraries) {
			writeError(w, http.StatusForbidden, errCodeOutsideLibrary, "media is outside the configured libraries")
			return
		}
		if !withinResolvedLibraries(target, settings.Libraries) {
			writeError(w, http.StatusForbidden, errCodeOutsideLibrary,
				"media resolves outside the configured libraries")
			return
		}
		root, ok := libraryRootFor(target, settings.Libraries)
		if !ok {
			writeError(w, http.StatusForbidden, errCodeOutsideLibrary, "media is outside the configured libraries")
			return
		}
		if _, statErr := os.Lstat(target); statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				// 文件已不在：索引里还留着这一行。跳过它，由后面的索引收敛对齐，
				// 重复删除同一张照片因此是幂等的。
				continue
			}
			writeError(w, http.StatusInternalServerError, errCodeDeleteFailed,
				fmt.Sprintf("inspect %s: %v", target, statErr))
			return
		}
		existing = append(existing, deleteTarget{path: target, root: root})
	}
	if len(existing) == 0 {
		// 索引命中但文件全都不在了：只收敛索引，按成功返回。
		s.finishDelete(entry, targets, nil)
		writeJSON(w, http.StatusOK, map[string]any{
			"deleted": true,
			"mode":    settings.DeleteMode,
			"id":      entry.ID,
		})
		return
	}

	// 只读挂载探测必须在删除之前：容器把照片目录按 :ro 挂进来时，权限位看上去仍是
	// 0755，只有真正写入才会暴露 EROFS。这一阶段不创建任何目录。
	//
	// 两种模式都要探测「源文件所在目录」，因为 rename 与 remove 都要先对源目录取得写权限：
	// 只探测回收站会让「库根可写、文件所在子目录只读」的形态漏过去，直到 rename 才失败，
	// 于是用户看到的是 500 而不是可诊断的只读原因。trash 模式额外探测回收站目录（它可能
	// 还不存在，向上找最近的已存在祖先）。
	for _, target := range existing {
		dirs := []string{filepath.Dir(target.path)}
		if settings.DeleteMode == config.DeleteModeTrash {
			dirs = append(dirs, nearestExistingDir(trashDirFor(target.root, target.path)))
		}
		for _, dir := range dirs {
			if err := probeWritable(dir); err != nil {
				s.d.Logger.Warn("delete rejected: target is not writable", "path", target.path, "dir", dir, "error", err)
				writeError(w, http.StatusForbidden, errCodeReadOnlyMount, readOnlyMountMessage)
				return
			}
		}
	}

	// 校验全部通过，开始动文件。
	var trashed []string
	var warnings []string
	for index, target := range existing {
		var moved string
		var err error
		if settings.DeleteMode == config.DeleteModeTrash {
			moved, err = moveToTrash(target.root, target.path)
		} else {
			err = os.Remove(target.path)
		}
		if err != nil {
			// 主文件失败就是整体失败；片段失败只记录，因为静帧已经删掉了，
			// 回滚不了，把它报成失败会让客户端重试一个必然失败的操作。
			if index == 0 {
				s.d.Logger.Warn("delete failed", "path", target.path, "error", err)
				writeError(w, http.StatusInternalServerError, errCodeDeleteFailed,
					fmt.Sprintf("%s %s: %v", deleteVerb(settings.DeleteMode), target.path, err))
				return
			}
			warnings = append(warnings, fmt.Sprintf("%s %s: %v", deleteVerb(settings.DeleteMode), target.path, err))
			continue
		}
		if moved != "" {
			trashed = append(trashed, moved)
		}
	}

	deleted := make([]string, 0, len(existing))
	for _, target := range existing {
		deleted = append(deleted, target.path)
	}
	warnings = s.finishDelete(entry, deleted, warnings)
	payload := map[string]any{
		"deleted": true,
		"mode":    settings.DeleteMode,
		"id":      entry.ID,
	}
	if len(trashed) > 0 {
		payload["trashPath"] = trashed[0]
		payload["trashPaths"] = trashed
	}
	if len(warnings) > 0 {
		payload["warnings"] = warnings
	}
	writeJSON(w, http.StatusOK, payload)
}

// deleteTarget 是一个待删除的文件与它所属的库根。
type deleteTarget struct {
	path string
	root string
}

// deletionEnabled 表示设置里的删除模式确实允许删除。
func deletionEnabled(mode string) bool {
	return mode == config.DeleteModeTrash || mode == config.DeleteModePermanent
}

func deleteVerb(mode string) string {
	if mode == config.DeleteModeTrash {
		return "move"
	}
	return "remove"
}

// finishDelete 收敛文件删除之后的三个副作用：清缩略图、标记索引、广播事件。
//
// 三者都不改变「文件已经删掉」这个事实，因此失败只记日志并放进 warnings，
// 不把已经成功的删除改写成错误响应——那会让客户端重试一个必然失败的操作。
func (s *Server) finishDelete(entry store.Entry, deleted []string, warnings []string) []string {
	if err := s.d.Thumbs.Invalidate(entry.ID); err != nil {
		s.d.Logger.Warn("thumbnail invalidate failed after delete", "id", entry.ID, "error", err)
		warnings = append(warnings, "thumbnail cleanup: "+err.Error())
	}
	// 索引里可能有多行指向同一份已删文件（静帧与它的动态片段路径不同），逐条收敛。
	var removed int64
	for _, path := range deleted {
		rows, err := s.d.Index.MarkRemoved(path)
		if err != nil {
			s.d.Logger.Warn("index update failed after delete", "path", path, "error", err)
			warnings = append(warnings, "index update: "+err.Error())
			continue
		}
		removed += rows
	}
	s.d.Logger.Info("media deleted", "id", entry.ID, "path", entry.Path, "rowsRemoved", removed)
	if s.d.Hub != nil {
		// 沿用扫描结束时的事件形状（scanner.go 的 library/changed{removed}），
		// 客户端与控制台都按同一条事件刷新时间线与统计。
		s.d.Hub.Broadcast("library/changed", map[string]any{"removed": removed})
	}
	return warnings
}

// libraryRootFor 返回包含 path 的配置库根。命中多个时取最长的那个，
// 保证嵌套库（例如 /photos 与 /photos/2024）下回收站落在最近的一层。
func libraryRootFor(path string, libraries []config.Library) (string, bool) {
	best := ""
	for _, lib := range libraries {
		root := filepath.Clean(lib.Path)
		if !sameOrChild(path, root) {
			continue
		}
		if len(root) > len(best) {
			best = root
		}
	}
	return best, best != ""
}

// sameOrChild 判断 path 是否等于 root 或位于 root 之内（字符串层面）。
func sameOrChild(path, root string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(os.PathSeparator))
}

// relativeToRoot 返回 target 相对库根的路径，用于在回收站里保持原有目录结构。
func relativeToRoot(root, target string) string {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return filepath.Base(target)
	}
	return rel
}

// trashPrefix 是回收站文件名的前缀：UTC 时间戳，避免同一目录下的同名文件
// 在回收站里互相覆盖，同时让回收站按时间排序即可看到删除顺序。
func trashPrefix(now time.Time) string {
	return now.UTC().Format("20060102T150405.000000000")
}

// moveToTrash 把 target 移动到它所属库根的回收站里，返回移动后的实际路径。
//
// 保持目录结构；目标已存在时追加序号，绝不覆盖回收站里已有的文件
// （那里的东西是用户的数据，静默覆盖等于二次删除）。
func moveToTrash(root, target string) (string, error) {
	dir := trashDirFor(root, target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create trash directory %s: %w", dir, err)
	}
	rel := relativeToRoot(root, target)
	prefix := trashPrefix(time.Now())
	dest := filepath.Join(dir, prefix+"_"+filepath.Base(rel))
	for i := 1; ; i++ {
		_, err := os.Lstat(dest)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("inspect trash target %s: %w", dest, err)
		}
		if i > 9999 {
			return "", fmt.Errorf("no free file name in %s", dir)
		}
		dest = filepath.Join(dir, fmt.Sprintf("%s_%d_%s", prefix, i, filepath.Base(rel)))
	}
	// 同一文件系统内 rename 是原子的：不会出现「源已删、目标写了一半」的中间状态。
	// 跨设备时 rename 返回 EXDEV，这里刻意不退回「复制再删」——那会在中断时留下
	// 半份文件，对不可再生的照片，宁可失败也不做非原子搬运。
	if err := os.Rename(target, dest); err != nil {
		return "", fmt.Errorf("move %s to trash: %w", target, err)
	}
	return dest, nil
}

// trashDirFor 返回 target 在回收站里对应的目录，保持原目录结构。
func trashDirFor(root, target string) string {
	dir := filepath.Join(root, trashDirName)
	if parent := filepath.Dir(relativeToRoot(root, target)); parent != "." {
		dir = filepath.Join(dir, parent)
	}
	return dir
}

// nearestExistingDir 向上找到第一个已存在的目录。回收站路径在删除前可能还不存在，
// 只读挂载探测必须落在一个已经存在的目录上，否则 MkdirAll 的失败会掩盖真正原因。
func nearestExistingDir(dir string) string {
	current := dir
	for {
		if info, err := os.Stat(current); err == nil && info.IsDir() {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return current
		}
		current = parent
	}
}

// probeWritable 探测目录能否真正写入：在其中创建并删除一个临时文件。
//
// 不只看权限位：容器里 :ro 挂载的目录权限位可能仍是 0755，只有实际写入才会暴露
// EROFS。这一阶段不创建目录，只落一个探测文件并立即删掉。
func probeWritable(dir string) error {
	probe, err := os.CreateTemp(dir, ".itemory-write-probe-*")
	if err != nil {
		return err
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}
