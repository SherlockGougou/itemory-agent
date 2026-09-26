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
	TopLevelItems int      `json:"topLevelItems"`
	Error         string   `json:"error,omitempty"`
}

var commonRoots = []string{
	"/volumes", "/volume1", "/vol1", "/vol2", "/mnt", "/srv", "/share", "/media", "/data",
}

// Discover inspects configured libraries plus common mount roots.
func Discover(libraries []config.Library) []Volume {
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
		if _, err := os.Stat(root); err == nil {
			candidates[root] = struct{}{}
			if entries, err := os.ReadDir(root); err == nil {
				for _, ent := range entries {
					if ent.IsDir() {
						candidates[filepath.Join(root, ent.Name())] = struct{}{}
					}
				}
			}
		}
	}

	var out []Volume
	for path := range candidates {
		out = append(out, inspect(path))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ContainerPath < out[j].ContainerPath })
	return out
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
		if countMedia(child, 50) > 0 {
			mediaFolders = append(mediaFolders, child)
		}
		if len(mediaFolders) >= 10 {
			break
		}
	}
	vol.MediaFolders = mediaFolders
	return vol
}

// countMedia counts media files directly inside a folder (bounded by limit).
func countMedia(dir string, limit int) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
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
	return count
}

// SuggestedUser returns the "uid:gid" string the app should suggest in compose.
func SuggestedUser(vols []Volume) string {
	for _, v := range vols {
		if v.Readable && v.OwnerUID >= 0 {
			return fmt.Sprintf("%d:%d", v.OwnerUID, v.OwnerGID)
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
