// Package volumes reports which mounted volumes the agent can read, including
// owner uid/gid so the app can suggest the exact compose `user:` value.
package volumes

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/SherlockGougou/itemory-agent/internal/config"
	"github.com/SherlockGougou/itemory-agent/internal/media"
)

// Volume is one candidate root visible inside the container.
type Volume struct {
	ContainerPath string   `json:"containerPath"`
	Readable      bool     `json:"readable"`
	OwnerUID      int      `json:"ownerUid"`
	OwnerGID      int      `json:"ownerGid"`
	MediaFolders  []string `json:"mediaFolders,omitempty"`
	// UnreadableFolders 是这个卷下一层里因权限读不了的目录。整卷挂载时很常见：
	// 卷根任何人可读，照片共享目录却只对某个 NAS 账号开放。
	UnreadableFolders []Folder `json:"unreadableFolders,omitempty"`
	TopLevelItems     int      `json:"topLevelItems"`
	Error             string   `json:"error,omitempty"`
}

// Folder is a directory the agent can see but not read, with its owner.
type Folder struct {
	Path     string `json:"path"`
	OwnerUID int    `json:"ownerUid"`
	OwnerGID int    `json:"ownerGid"`
}

// maxListedFolders 限制每个卷上报的媒体目录与不可读目录数量。
const maxListedFolders = 10

var commonRoots = []string{
	"/volumes", "/volume1", "/vol1", "/vol2", "/mnt", "/srv", "/share", "/media", "/data",
}

// Discover inspects configured libraries plus common mount roots.
//
// dataDir 是服务自己的数据目录（容器里是 /data）：里面是索引与缩略图缓存，
// 不能作为候选媒体目录出现，否则缩略图会被当成照片再索引一遍。
// 空的常见挂载点（镜像自带的 /media、/mnt、/srv）同样不上报，它们不是用户挂载的内容。
func Discover(libraries []config.Library, dataDir string) []Volume {
	candidates := map[string]struct{}{}
	for _, lib := range libraries {
		clean := filepath.Clean(lib.Path)
		candidates[clean] = struct{}{}
		// Walk up to two levels so the app can suggest broader roots.
		parent := filepath.Dir(clean)
		for i := 0; i < 2 && parent != "/" && parent != "."; i++ {
			candidates[parent] = struct{}{}
			parent = filepath.Dir(parent)
		}
	}
	for _, root := range commonRoots {
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) == 0 {
			continue
		}
		candidates[root] = struct{}{}
		for _, ent := range entries {
			if ent.IsDir() {
				candidates[filepath.Join(root, ent.Name())] = struct{}{}
			}
		}
	}

	var out []Volume
	for path := range candidates {
		if withinDir(path, dataDir) {
			continue
		}
		out = append(out, inspect(path))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ContainerPath < out[j].ContainerPath })
	return out
}

// withinDir 判断 path 是否就是 dir 或位于其下。dir 为空时恒为 false。
func withinDir(path, dir string) bool {
	if dir == "" {
		return false
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	return path == absolute || strings.HasPrefix(path, absolute+string(os.PathSeparator))
}

func inspect(path string) Volume {
	vol := Volume{ContainerPath: path, OwnerUID: -1, OwnerGID: -1}
	info, err := os.Stat(path)
	if err != nil {
		vol.Error = err.Error()
		return vol
	}
	vol.OwnerUID, vol.OwnerGID = ownerOf(info)
	entries, err := os.ReadDir(path)
	if err != nil {
		vol.Error = err.Error()
		return vol
	}
	vol.Readable = true
	vol.TopLevelItems = len(entries)
	var mediaFolders []string
	for _, ent := range entries {
		if !ent.IsDir() || strings.HasPrefix(ent.Name(), ".") {
			continue
		}
		child := filepath.Join(path, ent.Name())
		count, err := countMedia(child, 50)
		switch {
		case os.IsPermission(err):
			if len(vol.UnreadableFolders) < maxListedFolders {
				folder := Folder{Path: child, OwnerUID: -1, OwnerGID: -1}
				if childInfo, statErr := os.Stat(child); statErr == nil {
					folder.OwnerUID, folder.OwnerGID = ownerOf(childInfo)
				}
				vol.UnreadableFolders = append(vol.UnreadableFolders, folder)
			}
		case count > 0:
			mediaFolders = append(mediaFolders, child)
		}
		if len(mediaFolders) >= maxListedFolders {
			break
		}
	}
	vol.MediaFolders = mediaFolders
	return vol
}

// countMedia counts media files directly inside a folder (bounded by limit).
func countMedia(dir string, limit int) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		if _, ok := media.Classify(ent.Name()); ok {
			count++
			if count >= limit {
				break
			}
		}
	}
	return count, nil
}

// SuggestedUser returns the "uid:gid" to put into the compose `user:` line.
//
// 只在确实有目录读不了时给出建议，取值是那个目录的所有者——所有者一定读得了自己的目录。
// 全部可读时返回空串：此时没有需要修改的地方，返回当前可读目录的所有者只会误导
// （它往往就是容器当前的运行用户，或者镜像里属于 root 的空目录）。
// 所有者是 root 的目录不给建议：让容器以 root 运行不是可接受的修复方式，
// 这种目录应当在 NAS 上给运行账号授予读取权限。
func SuggestedUser(vols []Volume) string {
	for _, v := range vols {
		if !v.Readable && v.OwnerUID > 0 {
			return fmt.Sprintf("%d:%d", v.OwnerUID, v.OwnerGID)
		}
	}
	for _, v := range vols {
		for _, folder := range v.UnreadableFolders {
			if folder.OwnerUID > 0 {
				return fmt.Sprintf("%d:%d", folder.OwnerUID, folder.OwnerGID)
			}
		}
	}
	return ""
}

func ownerOf(info os.FileInfo) (int, int) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(stat.Uid), int(stat.Gid)
	}
	return -1, -1
}
