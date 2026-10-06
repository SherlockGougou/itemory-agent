// Package scan walks configured libraries, probes media metadata and keeps the
// index in sync (incremental: unchanged files are only touched, never re-read).
package scan

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SherlockGougou/itemory-agent/internal/config"
	"github.com/SherlockGougou/itemory-agent/internal/events"
	"github.com/SherlockGougou/itemory-agent/internal/logging"
	"github.com/SherlockGougou/itemory-agent/internal/media"
	"github.com/SherlockGougou/itemory-agent/internal/store"
)

// Progress is the live scan state exposed through /api/v1/scan/status.
type Progress struct {
	Running      bool      `json:"running"`
	Mode         string    `json:"mode"`
	Libraries    int       `json:"libraries"`
	FoldersDone  int       `json:"foldersDone"`
	FilesSeen    int       `json:"filesSeen"`
	MediaIndexed int       `json:"mediaIndexed"`
	Reused       int       `json:"reused"`
	Errors       int       `json:"errors"`
	StartedAt    time.Time `json:"startedAt"`
	FinishedAt   time.Time `json:"finishedAt,omitempty"`
	CurrentPath  string    `json:"currentPath,omitempty"`
	Message      string    `json:"message,omitempty"`
	// Cancelled 表示这一轮扫描是被管理员中止的，而不是自然跑完或失败。
	// 控制台据此把状态显示成「已中止」，而不是「已完成」。
	Cancelled bool `json:"cancelled,omitempty"`
}

// Scanner performs full and incremental scans.
type Scanner struct {
	store    *store.Store
	settings *config.Manager
	hub      *events.Hub
	log      *logging.Logger

	mu       sync.Mutex // guards running + cancel
	running  bool
	cancel   context.CancelFunc
	progMu   sync.RWMutex
	progress Progress

	onFinished func()
}

// OnFinished 注册一轮扫描正常结束后的回调（被中止或没能跑起来的扫描不触发）。
// 手动、计划与首次启动三种扫描都经过 Run，因此收尾工作只需要挂在这一处。必须在第一次扫描前调用。
func (s *Scanner) OnFinished(fn func()) {
	s.onFinished = fn
}

// New builds a scanner.
func New(st *store.Store, settings *config.Manager, hub *events.Hub, log *logging.Logger) *Scanner {
	return &Scanner{store: st, settings: settings, hub: hub, log: log}
}

// Status returns a copy of the current progress.
func (s *Scanner) Status() Progress {
	s.progMu.RLock()
	defer s.progMu.RUnlock()
	return s.progress
}

// Cancel asks the running scan to stop. 返回 false 表示当时没有扫描在跑——
// 对空闲状态调用是幂等的 no-op，调用方据此回 409 而不是 500。
func (s *Scanner) Cancel() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.cancel == nil {
		return false
	}
	s.cancel()
	return true
}

// TryBegin 占下本轮扫描槽位，并立刻把 Running 写进进度再返回。
//
// 控制台 POST /scan 后会马上 reload 一次。如果要等到目录遍历前才写 Running，
// 大库上的 Existing() 会让界面长时间显示空闲，用户以为点击没生效。
func (s *Scanner) TryBegin(mode string) (context.Context, context.CancelFunc, error) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil, nil, errors.New("scan already running")
	}
	// 一轮扫描一个可取消上下文：控制台的「中止扫描」通过 Cancel() 触发，
	// worker 在取任务前检查 ctx，已发出的目录作业做完当前一批就收手。
	ctx, cancel := context.WithCancel(context.Background())
	s.running = true
	s.cancel = cancel
	s.mu.Unlock()

	settings := s.settings.Get()
	s.setProgress(Progress{
		Running:   true,
		Mode:      mode,
		Libraries: len(settings.Libraries),
		StartedAt: time.Now(),
	})
	s.hub.Broadcast("scan/started", s.Status())
	return ctx, cancel, nil
}

// Scan walks every configured library. mode is "incremental" or "full"
// (mode is recorded in progress; full re-probes every file).
func (s *Scanner) Scan(mode string) error {
	ctx, cancel, err := s.TryBegin(mode)
	if err != nil {
		return err
	}
	return s.Run(ctx, cancel, mode)
}

// Run 在 TryBegin 占坑之后执行真正的目录遍历与入库。
func (s *Scanner) Run(ctx context.Context, cancel context.CancelFunc, mode string) error {
	defer func() {
		cancel()
		s.mu.Lock()
		s.running = false
		s.cancel = nil
		s.mu.Unlock()
	}()

	settings := s.settings.Get()
	if len(settings.Libraries) == 0 {
		return s.failScan("no libraries configured")
	}
	gen, err := s.store.BeginScan()
	if err != nil {
		return s.failScan(err.Error())
	}
	existing, err := s.store.Existing()
	if err != nil {
		return s.failScan(err.Error())
	}
	// 目录 + 小写文件名 → 路径：配对时需要大小写无关地找回「同目录同名」的另一侧
	// （相机与 NAS 上 .HEIC / .JPG / .MOV 的大小写并不统一）。
	nameIndex := lowercaseNameIndex(existing)

	queue := &dirQueue{}
	var visitedMu sync.Mutex
	visited := map[string]struct{}{}
	var pending int64
	var scanIncomplete atomic.Bool

	enqueue := func(path string, depth int) {
		if depth > 32 || ctx.Err() != nil {
			return
		}
		visitedMu.Lock()
		if _, ok := visited[path]; ok {
			visitedMu.Unlock()
			return
		}
		visited[path] = struct{}{}
		visitedMu.Unlock()
		atomic.AddInt64(&pending, 1)
		queue.push(dirJob{path: path, depth: depth})
	}

	for _, lib := range settings.Libraries {
		enqueue(filepath.Clean(lib.Path), 0)
	}

	workers := settings.Concurrency
	if workers < 1 {
		workers = 1
	}

	var wg sync.WaitGroup
	// worker 之间共享，必须用原子量（并发读写 time.Time 是数据竞争）
	var lastBroadcast atomic.Int64
	lastBroadcast.Store(time.Now().UnixNano())
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				job, ok := queue.pop()
				if !ok {
					// 被取消时不再空转等待队列排空，直接退出。
					if ctx.Err() != nil || atomic.LoadInt64(&pending) == 0 {
						return
					}
					time.Sleep(5 * time.Millisecond)
					continue
				}
				s.processDir(job, settings, existing, nameIndex, gen, enqueue, mode == "full", &scanIncomplete)
				atomic.AddInt64(&pending, -1)
				if time.Since(time.Unix(0, lastBroadcast.Load())) > 750*time.Millisecond {
					lastBroadcast.Store(time.Now().UnixNano())
					s.hub.Broadcast("scan/progress", s.Status())
				}
			}
		}()
	}
	wg.Wait()

	// 被取消的这一轮不做移除判定：没遍历到的条目依然有效，交给下一次扫描收敛。
	cancelled := ctx.Err() != nil
	if cancelled {
		scanIncomplete.Store(true)
	}

	var removed int64
	var absorbed int64
	switch {
	case cancelled:
		s.log.Info("scan cancelled; existing entries retained", "mode", mode)
	case scanIncomplete.Load():
		// 任一目录无法读取或写入不完整时，旧条目不能被当成已删除。
		// 下一次完整成功的扫描再执行全库移除判定。
		s.log.Warn("scan incomplete; existing entries retained", "errors", s.Status().Errors)
	default:
		removed, err = s.store.FinishScan(gen)
		if err != nil {
			s.log.Warn("finish scan failed", "error", err)
			scanIncomplete.Store(true)
		}
		// 动态片段的同内容副本（备份工具写出的 xxx_1.MOV）只能在这里收敛：配对按目录与
		// 文件名进行，副本没有同名静帧，扫描期拿不到配对关系，只能靠内容指纹归并。
		if absorbed, err = s.store.AbsorbMotionClipCopies(); err != nil {
			s.log.Warn("absorb motion clip copies failed", "error", err)
		}
	}

	progress := s.Status()
	progress.Running = false
	progress.Cancelled = cancelled
	progress.FinishedAt = time.Now()
	switch {
	case cancelled:
		progress.Message = "scan cancelled"
	case scanIncomplete.Load():
		progress.Message = "scan incomplete; existing entries retained"
	default:
		progress.Message = "scan finished"
	}
	s.setProgress(progress)

	if cancelled {
		s.hub.Broadcast("scan/cancelled", progress)
		return nil
	}
	s.log.Info("scan finished",
		"mode", mode,
		"folders", progress.FoldersDone,
		"files", progress.FilesSeen,
		"indexed", progress.MediaIndexed,
		"reused", progress.Reused,
		"removed", removed,
		"absorbedMotionClips", absorbed,
		"errors", progress.Errors,
		"elapsed_s", progress.FinishedAt.Sub(progress.StartedAt).Seconds())
	s.hub.Broadcast("scan/done", progress)
	s.hub.Broadcast("library/changed", map[string]any{"removed": removed})
	if s.onFinished != nil {
		// 不在这里等回调：Run 返回前扫描槽位还占着，回调若耗时，紧接着的扫描请求会被拒绝。
		go s.onFinished()
	}
	return nil
}

type dirJob struct {
	path  string
	depth int
}

// failScan 把「扫描没能跑起来」的原因写进进度对象并广播。
// 否则 App 只会看到 POST /scan 返回 started:true，而状态页永远停在 idle（原因只进日志）。
func (s *Scanner) failScan(reason string) error {
	// 覆盖成一份干净的状态：只留失败原因，避免把上一次扫描的计数留在状态页上。
	s.setProgress(Progress{Running: false, Message: reason, FinishedAt: time.Now()})
	s.hub.Broadcast("scan/failed", map[string]any{"reason": reason})
	return errors.New(reason)
}

type dirQueue struct {
	mu    sync.Mutex
	items []dirJob
}

func (q *dirQueue) push(j dirJob) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, j)
}

func (q *dirQueue) pop() (dirJob, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return dirJob{}, false
	}
	j := q.items[0]
	q.items = q.items[1:]
	return j, true
}

type fileCandidate struct {
	path     string
	name     string
	size     int64
	modified time.Time
}

func (s *Scanner) processDir(job dirJob, settings config.Settings, existing map[string]store.Entry,
	nameIndex map[string]string, gen int64, enqueue func(string, int), force bool, incomplete *atomic.Bool) {

	dirEntries, err := os.ReadDir(job.path)
	if err != nil {
		incomplete.Store(true)
		s.addError()
		s.log.Debug("read dir failed", "path", job.path, "error", err)
		return
	}

	files := make([]fileCandidate, 0, len(dirEntries))
	for _, ent := range dirEntries {
		name := ent.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if excluded(name, settings.ExcludePatterns) {
			continue
		}
		if ent.IsDir() {
			if ent.Type()&fs.ModeSymlink != 0 {
				continue
			}
			enqueue(filepath.Join(job.path, name), job.depth+1)
			continue
		}
		if !ent.Type().IsRegular() {
			continue
		}
		if _, ok := media.Classify(name); !ok {
			continue
		}
		info, err := ent.Info()
		if err != nil {
			incomplete.Store(true)
			s.addError()
			continue
		}
		files = append(files, fileCandidate{
			path:     filepath.Join(job.path, name),
			name:     name,
			size:     info.Size(),
			modified: info.ModTime(),
		})
	}

	var upserts []store.Entry
	var touches []string

	for _, f := range files {
		s.addFileSeen()
		if !force {
			// 复用条件里带探测版本：文件没变、但这一行是旧版探测逻辑写的（例如还没有 GPS 列），
			// 同样重新探测一次。这样「新增一项元数据」由升级后的第一次增量扫描自动补齐，
			// 不必要求用户在 App 里手工点一次「重建索引」。
			if prev, ok := existing[f.path]; ok &&
				prev.Size == f.size && prev.Modified == f.modified.Unix() &&
				prev.ProbeVersion >= media.MinProbeVersion(prev.Kind) &&
				!prev.Removed && prev.ID != "" {
				touches = append(touches, f.path)
				s.addReused()
				continue
			}
		}
		result, err := media.ProbeFile(f.path, f.size, f.modified, media.Options{})
		if err != nil {
			incomplete.Store(true)
			s.addError()
			s.log.Debug("probe failed", "path", f.path, "error", err)
			continue
		}
		upserts = append(upserts, entryFromResult(result, gen))
		s.addIndexed()
	}

	if settings.MotionPhoto {
		// 先按本批配对（两侧都在本批时的原有行为）
		pairMotion(&upserts)
		// 再补「另一侧本次未变化」的配对：增量扫描里未变化的文件只走 Touch、不在 upserts 里，
		// 只看本批会让「静帧没变、片段新到」这类动态照片永远配不上对（表现为照片 + 一条
		// 独立视频混进动态放映），而且不会自愈——除非有人手工跑一次全量重扫。
		if extra := crossBatchPairings(upserts, existing, nameIndex); len(extra) > 0 {
			upserts = append(upserts, extra...)
		}
	}

	if err := s.store.UpsertMany(upserts); err != nil {
		incomplete.Store(true)
		s.addError()
		s.log.Warn("upsert failed", "dir", job.path, "error", err)
	}
	if err := s.store.Touch(touches, gen); err != nil {
		incomplete.Store(true)
		s.log.Warn("touch failed", "dir", job.path, "error", err)
	}

	s.progMu.Lock()
	s.progress.FoldersDone++
	s.progress.CurrentPath = job.path
	s.progMu.Unlock()
}

// pairMotion links still images with their motion clips (Apple Live Photos) in
// the same directory. Android embedded motion was already detected while probing.
func pairMotion(entries *[]store.Entry) {
	list := *entries
	stills := map[string]int{}
	clips := map[string]int{}
	for i := range list {
		base, ok := media.MotionCandidateBase(list[i].Name)
		if !ok {
			continue
		}
		switch list[i].Kind {
		case media.KindImage, media.KindRaw:
			if _, dup := stills[base]; !dup {
				stills[base] = i
			}
		case media.KindVideo:
			ext := media.Extension(list[i].Name)
			if ext == "mov" || ext == "mp4" {
				if _, dup := clips[base]; !dup {
					clips[base] = i
				}
			}
		}
	}
	for base, stillIdx := range stills {
		clipIdx, ok := clips[base]
		if !ok {
			continue
		}
		still := &list[stillIdx]
		clip := &list[clipIdx]
		identifier, confidence := media.SuggestApplePair(still.Path, clip.Path)
		still.Kind = media.KindMotion
		still.MotionPath = clip.Path
		still.MotionIdentifier = identifier
		still.MotionVendor = "apple"
		still.MotionConfidence = confidence
		clip.Kind = media.KindMotionClip
	}
}

// lowercaseNameIndex 建立「目录|小写文件名 → 路径」索引。配对时用它大小写无关地找回
// 同目录同名的另一侧；索引里的已移除条目不参与配对。
func lowercaseNameIndex(existing map[string]store.Entry) map[string]string {
	out := make(map[string]string, len(existing))
	for path, entry := range existing {
		if entry.Removed || entry.Name == "" {
			continue
		}
		out[store.ParentPath(path)+"|"+strings.ToLower(entry.Name)] = path
	}
	return out
}

// motionCounterpartKeys 返回同目录、同 basename 的另一侧候选索引键（先静帧后片段）。
func motionCounterpartKeys(entry store.Entry) []string {
	base, ok := media.MotionCandidateBase(entry.Name)
	if !ok {
		return nil
	}
	dir := store.ParentPath(entry.Path)
	keys := func(exts ...string) []string {
		out := make([]string, 0, len(exts))
		for _, ext := range exts {
			out = append(out, dir+"|"+base+"."+ext)
		}
		return out
	}
	switch entry.Kind {
	case media.KindVideo:
		if ext := media.Extension(entry.Name); ext != "mov" && ext != "mp4" {
			return nil
		}
		return keys("heic", "heif", "jpg", "jpeg", "png")
	case media.KindImage, media.KindRaw:
		return keys("mov", "mp4")
	default:
		return nil
	}
}

// crossBatchPairings 为「配对的一侧本次未变化」的动态照片补写配对结果。
//
// 另一侧若也在本批 upserts 里，说明 pairMotion 已经处理过，这里直接跳过，
// 避免同一行被写两次（后写的旧行会用陈旧元数据覆盖刚探测出来的结果）。
func crossBatchPairings(upserts []store.Entry, existing map[string]store.Entry,
	names map[string]string) []store.Entry {
	if len(upserts) == 0 || len(names) == 0 {
		return nil
	}
	inBatch := make(map[string]struct{}, len(upserts))
	for _, entry := range upserts {
		inBatch[entry.Path] = struct{}{}
	}
	var extra []store.Entry
	patched := make(map[string]struct{})
	for i := range upserts {
		entry := upserts[i]
		// 已经是片段、或已经配对成功的条目不再介入
		if entry.Kind == media.KindMotionClip || (entry.Kind == media.KindMotion && entry.MotionPath != "") {
			continue
		}
		for _, key := range motionCounterpartKeys(entry) {
			path, ok := names[key]
			if !ok {
				continue
			}
			if _, sameBatch := inBatch[path]; sameBatch {
				break
			}
			if _, done := patched[path]; done {
				break
			}
			counterpart, ok := existing[path]
			if !ok || counterpart.Removed {
				break
			}
			// 复用同一套配对规则：把两侧凑成一小段交给 pairMotion。
			// 不刷新 counterpart 的 last_seen——它是否还在磁盘上由本次目录枚举与 Touch 判定。
			local := []store.Entry{entry, counterpart}
			pairMotion(&local)
			upserts[i] = local[0]
			if pairingChanged(counterpart, local[1]) {
				extra = append(extra, local[1])
				patched[path] = struct{}{}
			}
			break
		}
	}
	return extra
}

// pairingChanged 判断配对结果是否改写了条目（决定是否需要补写这一行）。
func pairingChanged(before, after store.Entry) bool {
	return before.Kind != after.Kind ||
		before.MotionPath != after.MotionPath ||
		before.MotionOffset != after.MotionOffset ||
		before.MotionLength != after.MotionLength ||
		before.MotionIdentifier != after.MotionIdentifier ||
		before.MotionVendor != after.MotionVendor ||
		before.MotionConfidence != after.MotionConfidence
}

func entryFromResult(res *media.Result, gen int64) store.Entry {
	// 拍摄时间无效时不上报 1970-01-01：Capture=0 且 DayKey 为空，
	// 这类条目不会进入按自然日聚合的时间线（days/items 都要求 day_key <> ''）。
	capture := res.Capture.Unix()
	if capture < 0 {
		capture = 0
	}
	dayKey := ""
	if capture > 0 {
		dayKey = store.DayKey(res.Capture)
	}
	modified := res.Modified.Unix()
	if modified < 0 {
		modified = 0
	}
	entry := store.Entry{
		Path:          res.Path,
		ID:            res.ID,
		Parent:        store.ParentPath(res.Path),
		Name:          filepath.Base(res.Path),
		Kind:          res.Kind,
		Size:          res.Size,
		Modified:      modified,
		Capture:       capture,
		CaptureSource: res.CaptureSource,
		DayKey:        dayKey,
		Width:         res.Width,
		Height:        res.Height,
		Latitude:      res.Latitude,
		Longitude:     res.Longitude,
		Make:          res.Make,
		Model:         res.Model,
		LensModel:     res.LensModel,
		Aperture:      res.Aperture,
		Exposure:      res.Exposure,
		ISO:           res.ISO,
		FocalLength:   res.FocalLength,
		Altitude:      res.Altitude,
		ProbeVersion:  media.ProbeVersion,
		LastSeen:      gen,
	}
	if res.Motion != nil {
		entry.MotionOffset = res.Motion.Offset
		entry.MotionLength = res.Motion.Length
		entry.MotionIdentifier = res.Motion.Identifier
		entry.MotionVendor = res.Motion.Vendor
		entry.MotionConfidence = res.Motion.Confidence
		entry.MotionPath = res.Motion.ExternalPath
	}
	entry.Duration = res.Duration
	if res.RawPreview != nil {
		entry.RawPreviewOffset = res.RawPreview.Offset
		entry.RawPreviewLength = res.RawPreview.Length
		entry.RawPreviewWidth = res.RawPreview.Width
		entry.RawPreviewHeight = res.RawPreview.Height
	}
	return entry
}

func excluded(name string, patterns []string) bool {
	for _, p := range patterns {
		if p == "" {
			continue
		}
		if strings.Contains(name, p) {
			return true
		}
	}
	return false
}

func (s *Scanner) setProgress(p Progress) {
	s.progMu.Lock()
	s.progress = p
	s.progMu.Unlock()
}

func (s *Scanner) addFileSeen() {
	s.progMu.Lock()
	s.progress.FilesSeen++
	s.progMu.Unlock()
}

func (s *Scanner) addIndexed() {
	s.progMu.Lock()
	s.progress.MediaIndexed++
	s.progMu.Unlock()
}

func (s *Scanner) addReused() {
	s.progMu.Lock()
	s.progress.Reused++
	s.progMu.Unlock()
}

func (s *Scanner) addError() {
	s.progMu.Lock()
	s.progress.Errors++
	s.progMu.Unlock()
}
