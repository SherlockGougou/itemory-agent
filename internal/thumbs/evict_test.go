package thumbs

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SherlockGougou/itemory-agent/internal/config"
	"github.com/SherlockGougou/itemory-agent/internal/logging"
)

type fixedSettings struct{ s config.Settings }

func (f fixedSettings) Get() config.Settings { return f.s }

// TestEvictKeepsCacheUnderLimitAtTenThousandFiles is the 10k-scale eviction
// measurement: 10,000 files (~40 MB) against an 8 MB cap must converge below
// the limit and keep the newest entries.
func TestEvictKeepsCacheUnderLimitAtTenThousandFiles(t *testing.T) {
	dir := t.TempDir()
	settings := config.Defaults()
	settings.ThumbCacheLimitBytes = 8 << 20
	manager := New(dir, fixedSettings{settings}, logging.New("error", logging.NewRing(10)))

	const files = 10_000
	payload := make([]byte, 4<<10)
	base := time.Now().Add(-time.Duration(files) * time.Second)
	for i := 0; i < files; i++ {
		path := filepath.Join(dir, fmt.Sprintf("thumb-%05d-512.jpg", i))
		if err := os.WriteFile(path, payload, 0o644); err != nil {
			t.Fatal(err)
		}
		modTime := base.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}

	started := time.Now()
	if err := manager.Evict(); err != nil {
		t.Fatal(err)
	}
	t.Logf("evicted 10k cache files in %s", time.Since(started))

	_, total := manager.Stats()
	if total > settings.ThumbCacheLimitBytes {
		t.Fatalf("cache still %d bytes above limit %d", total, settings.ThumbCacheLimitBytes)
	}
	newest := filepath.Join(dir, fmt.Sprintf("thumb-%05d-512.jpg", files-1))
	if _, err := os.Stat(newest); err != nil {
		t.Fatalf("newest cache entry must survive LRU eviction: %v", err)
	}
}
