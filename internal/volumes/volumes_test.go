package volumes

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/SherlockGougou/itemory-agent/internal/config"
)

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func find(vols []Volume, path string) (Volume, bool) {
	for _, v := range vols {
		if v.ContainerPath == path {
			return v, true
		}
	}
	return Volume{}, false
}

// under 只保留 root 之下的卷：Discover 还会探测本机的常见挂载点，
// 那些目录的权限因机器而异，不属于用例要断言的内容。
func under(vols []Volume, root string) []Volume {
	var out []Volume
	for _, v := range vols {
		if withinDir(v.ContainerPath, root) {
			out = append(out, v)
		}
	}
	return out
}

func TestDiscoverSkipsTheDataDirectory(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(root, "mount", "photos")
	writeFile(t, filepath.Join(library, "trip", "a.jpg"))
	// 数据目录与媒体库同在一个上级目录下，缩略图缓存里全是 .jpg。
	dataDir := filepath.Join(root, "mount", "data")
	writeFile(t, filepath.Join(dataDir, "thumbs", "m1-512.jpg"))

	vols := Discover([]config.Library{{Path: library}}, dataDir)

	volume, ok := find(vols, library)
	if !ok || !volume.Readable {
		t.Fatalf("library volume missing or unreadable: %+v", vols)
	}
	if len(volume.MediaFolders) != 1 || volume.MediaFolders[0] != filepath.Join(library, "trip") {
		t.Fatalf("media folders = %v", volume.MediaFolders)
	}
	for _, v := range vols {
		if withinDir(v.ContainerPath, dataDir) {
			t.Fatalf("data directory reported as a candidate: %s", v.ContainerPath)
		}
	}
	if parent, ok := find(vols, filepath.Join(root, "mount")); !ok || !parent.Readable {
		t.Fatalf("parent volume missing: %+v", vols)
	}
}

func TestSuggestedUserComesFromUnreadableFolders(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory; permission cases need a regular user")
	}
	root := t.TempDir()
	library := filepath.Join(root, "volume1")
	writeFile(t, filepath.Join(library, "public", "a.jpg"))
	locked := filepath.Join(library, "photo")
	writeFile(t, filepath.Join(locked, "b.jpg"))
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	vols := under(Discover([]config.Library{{Path: library}}, ""), root)
	volume, ok := find(vols, library)
	if !ok {
		t.Fatalf("library volume missing: %+v", vols)
	}
	if len(volume.UnreadableFolders) != 1 || volume.UnreadableFolders[0].Path != locked {
		t.Fatalf("unreadable folders = %+v", volume.UnreadableFolders)
	}
	want := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	if got := SuggestedUser(vols); got != want {
		t.Fatalf("suggested user = %q, want the locked folder's owner %q", got, want)
	}

	// 库根本身读不了：建议取库根的所有者。
	if err := os.Chmod(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(library, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(library, 0o755) })
	vols = under(Discover([]config.Library{{Path: library}}, ""), root)
	if volume, _ := find(vols, library); volume.Readable {
		t.Fatalf("library root should be unreadable: %+v", volume)
	}
	if got := SuggestedUser(vols); got != want {
		t.Fatalf("suggested user = %q, want %q", got, want)
	}
}

func TestSuggestedUserIsEmptyWhenEverythingIsReadable(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(root, "photos")
	writeFile(t, filepath.Join(library, "trip", "a.jpg"))
	if got := SuggestedUser(under(Discover([]config.Library{{Path: library}}, ""), root)); got != "" {
		t.Fatalf("suggested user = %q, want empty", got)
	}
}
