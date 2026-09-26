package thumbs

import (
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/SherlockGougou/itemory-agent/internal/config"
	"github.com/SherlockGougou/itemory-agent/internal/logging"
	"github.com/SherlockGougou/itemory-agent/internal/media"
	"github.com/SherlockGougou/itemory-agent/internal/store"
)

type dummySettings struct {
	thumbSize int
	limit     int64
}

func (d dummySettings) Get() config.Settings {
	return config.Settings{
		ThumbSize:            d.thumbSize,
		ThumbCacheLimitBytes: d.limit,
	}
}

func writeTestImage(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 120, A: 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
}

func TestManagerConcurrentEnsure(t *testing.T) {
	dir := t.TempDir()
	log := logging.New("error", logging.NewRing(10))
	m := New(dir, dummySettings{thumbSize: 256, limit: 10 * 1024 * 1024}, log)

	imgPath := filepath.Join(dir, "source.jpg")
	writeTestImage(t, imgPath, 800, 600)

	entry := store.Entry{
		ID:     "test-photo-001",
		Kind:   media.KindImage,
		Path:   imgPath,
		Width:  800,
		Height: 600,
	}

	const concurrency = 12
	var wg sync.WaitGroup
	wg.Add(concurrency)
	results := make([]string, concurrency)
	errors := make([]error, concurrency)

	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()
			path, err := m.Ensure(entry, 256)
			results[idx] = path
			errors[idx] = err
		}(i)
	}
	wg.Wait()

	for i := 0; i < concurrency; i++ {
		if errors[i] != nil {
			t.Fatalf("goroutine %d failed: %v", i, errors[i])
		}
		if results[i] != results[0] {
			t.Fatalf("goroutine %d got different path: %s vs %s", i, results[i], results[0])
		}
	}

	fi, err := os.Stat(results[0])
	if err != nil || fi.Size() == 0 {
		t.Fatalf("expected thumbnail file to exist and be non-empty: %v", err)
	}

	// 再次调用命中缓存
	cachedPath, err := m.Ensure(entry, 256)
	if err != nil || cachedPath != results[0] {
		t.Fatalf("cache hit failed: path=%s, err=%v", cachedPath, err)
	}
}

func TestManagerHEICAndFallback(t *testing.T) {
	dir := t.TempDir()
	log := logging.New("error", logging.NewRing(10))
	m := New(dir, dummySettings{thumbSize: 512, limit: 10 * 1024 * 1024}, log)

	// 针对已知不存在的畸形路径应优雅报错，而不是 panic 或死锁
	entry := store.Entry{
		ID:   "nonexistent-heic",
		Kind: media.KindImage,
		Path: filepath.Join(dir, "not_found.heic"),
	}

	_, err := m.Ensure(entry, 512)
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}

	// 缓存路径命名校验
	expectedCache := filepath.Join(dir, fmt.Sprintf("%s-%d.jpg", entry.ID, 512))
	if m.CachePath(entry.ID, 512) != expectedCache {
		t.Fatalf("unexpected cache path: %s != %s", m.CachePath(entry.ID, 512), expectedCache)
	}
}
