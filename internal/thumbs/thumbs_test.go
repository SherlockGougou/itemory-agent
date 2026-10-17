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
	thumbSize   int
	limit       int64
	budget      int
	concurrency int
}

func (d dummySettings) Get() config.Settings {
	return config.Settings{
		ThumbSize:            d.thumbSize,
		ThumbCacheLimitBytes: d.limit,
		NightlyThumbBudget:   d.budget,
		Concurrency:          d.concurrency,
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

// 单条失效是删除接口的清缓存路径：只清目标 id 的所有尺寸，别的条目与
// 名字相近的 id（前缀关系）都不能被误删，缓存目录之外的路径也不能由 id 指定。
func TestManagerInvalidateRemovesOnlyOneEntry(t *testing.T) {
	dir := t.TempDir()
	log := logging.New("error", logging.NewRing(10))
	m := New(dir, dummySettings{thumbSize: 256, limit: 10 * 1024 * 1024}, log)

	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("cached"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 目标条目的多个尺寸 + 无关条目 + 前缀相近的 id + 回收站里的非缩略图文件。
	write(m.CachePath("mabc", 256)[len(dir)+1:])
	write(m.CachePath("mabc", 512)[len(dir)+1:])
	write(m.CachePath("mother", 256)[len(dir)+1:])
	write("mabc-1-256.jpg")
	write("mabc-256.png")
	write("mabc-abc.jpg")

	if err := os.MkdirAll(filepath.Join(dir, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := m.Invalidate("mabc"); err != nil {
		t.Fatalf("invalidate: %v", err)
	}

	for _, gone := range []string{"mabc-256.jpg", "mabc-512.jpg"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s should be removed: %v", gone, err)
		}
	}
	for _, kept := range []string{"mother-256.jpg", "mabc-1-256.jpg", "mabc-256.png", "mabc-abc.jpg"} {
		if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
			t.Fatalf("%s must survive invalidation: %v", kept, err)
		}
	}

	// 已经没有缓存文件时是成功而不是错误：删除接口不因缓存状态失败。
	if err := m.Invalidate("mabc"); err != nil {
		t.Fatalf("second invalidate: %v", err)
	}
	if err := m.Invalidate("never-cached"); err != nil {
		t.Fatalf("invalidate missing entry: %v", err)
	}

	// 空 id 与带路径分隔符的 id 一律拒绝，不能由调用方指定缓存目录之外的路径。
	for _, bad := range []string{"", ".", "..", "../escape", "a/b", `a\b`} {
		if err := m.Invalidate(bad); err == nil {
			t.Fatalf("invalidate(%q) must be rejected", bad)
		}
	}
}

// 失效之后重新 Ensure 必须重新生成真实缩略图，而不是把空目录当成命中。
func TestManagerInvalidateForcesRegeneration(t *testing.T) {
	dir := t.TempDir()
	log := logging.New("error", logging.NewRing(10))
	m := New(dir, dummySettings{thumbSize: 128, limit: 10 * 1024 * 1024}, log)

	source := filepath.Join(t.TempDir(), "source.jpg")
	writeTestImage(t, source, 400, 300)
	entry := store.Entry{ID: "regen", Kind: media.KindImage, Path: source, Width: 400, Height: 300}

	first, err := m.Ensure(entry, 128)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := m.Invalidate(entry.ID); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("cache file survived invalidate: %v", err)
	}
	second, err := m.Ensure(entry, 128)
	if err != nil {
		t.Fatalf("re-ensure: %v", err)
	}
	if fi, err := os.Stat(second); err != nil || fi.Size() == 0 {
		t.Fatalf("regenerated thumbnail is missing or empty: %v", err)
	}
}
