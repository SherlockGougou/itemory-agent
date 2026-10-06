package scan

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SherlockGougou/itemory-agent/internal/config"
	"github.com/SherlockGougou/itemory-agent/internal/events"
	"github.com/SherlockGougou/itemory-agent/internal/logging"
	"github.com/SherlockGougou/itemory-agent/internal/media"
	"github.com/SherlockGougou/itemory-agent/internal/store"
)

func TestTryBeginPublishesRunningBeforeWalk(t *testing.T) {
	index, err := store.Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	settings, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scanner := New(index, settings, events.NewHub(), logging.New("error", nil))

	ctx, cancel, err := scanner.TryBegin("full")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	progress := scanner.Status()
	if !progress.Running || progress.Mode != "full" || progress.StartedAt.IsZero() {
		t.Fatalf("TryBegin must publish running progress immediately: %+v", progress)
	}
	// 没配置媒体库时 Run 会 failScan，状态必须带上原因，不能静默回到 idle。
	if err := scanner.Run(ctx, cancel, "full"); err == nil {
		t.Fatal("expected failure when no libraries configured")
	}
	progress = scanner.Status()
	if progress.Running || progress.Message != "no libraries configured" {
		t.Fatalf("failed scan must record reason: %+v", progress)
	}
}

func TestScanIndexesPairsAndIsIncremental(t *testing.T) {
	library := t.TempDir()
	writeJPEG(t, filepath.Join(library, "IMG_20240102_030405.jpg"), 200, 120)

	// Apple Live Photo pair: the still image carries the content identifier in its
	// head, the movie carries the same identifier in its tail.
	uuid := "3671E07C-896E-441B-B72E-6CFDB2B3B31C"
	still := filepath.Join(library, "IMG_1000.HEIC")
	if err := os.WriteFile(still, append([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'c'}, []byte("xxxx"+uuid+"xxxx")...), 0o644); err != nil {
		t.Fatal(err)
	}
	movie := filepath.Join(library, "IMG_1000.MOV")
	var movieBytes bytes.Buffer
	movieBytes.Write(bytes.Repeat([]byte{0}, 1024))
	movieBytes.WriteString("com.apple.quicktime.content.identifier")
	movieBytes.WriteString(uuid)
	if err := os.WriteFile(movie, movieBytes.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	// 备份工具遇到同名冲突写出的副本：与片段字节相同，但同目录里没有同名静帧，
	// 配对阶段识别不出来，只能靠内容指纹收敛回动态片段。
	movieCopy := filepath.Join(library, "IMG_1000_1.MOV")
	if err := os.WriteFile(movieCopy, movieBytes.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	// Standalone video must stay a video.
	writeJPEG(t, filepath.Join(library, "HOLIDAY.jpg"), 100, 100)
	standalone := filepath.Join(library, "CLIP.MP4")
	if err := os.WriteFile(standalone, []byte("\x00\x00\x00\x18ftypmp42"), 0o644); err != nil {
		t.Fatal(err)
	}

	index, err := store.Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()

	settings, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	current := settings.Get()
	current.Libraries = []config.Library{{ID: "lib", Name: "lib", Path: library}}
	current.ExcludePatterns = []string{"@eaDir"}
	if err := settings.Update(current); err != nil {
		t.Fatal(err)
	}

	scanner := New(index, settings, events.NewHub(), logging.New("error", nil))
	if err := scanner.Scan("incremental"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	progress := scanner.Status()
	if progress.Running || progress.FilesSeen == 0 {
		t.Fatalf("unexpected progress: %+v", progress)
	}

	days, err := index.Days("", "")
	if err != nil {
		t.Fatal(err)
	}
	foundDay := false
	for _, day := range days {
		if day.DayKey == "2024-01-02" {
			foundDay = true
		}
	}
	if !foundDay {
		t.Fatalf("filename date not grouped: %+v", days)
	}

	stillEntry, ok, err := index.GetByPath(still)
	if err != nil || !ok {
		t.Fatalf("still not indexed: %v %v", ok, err)
	}
	if stillEntry.Kind != "motion" || stillEntry.MotionPath != movie || stillEntry.MotionConfidence != "identifier" {
		t.Fatalf("live photo not paired: %+v", stillEntry)
	}
	clipEntry, ok, err := index.GetByPath(movie)
	if err != nil || !ok || clipEntry.Kind != "motion-clip" {
		t.Fatalf("motion clip not marked: %+v %v %v", clipEntry, ok, err)
	}
	copyEntry, ok, err := index.GetByPath(movieCopy)
	if err != nil || !ok || copyEntry.Kind != "motion-clip" {
		t.Fatalf("motion clip copy not absorbed: %+v %v %v", copyEntry, ok, err)
	}
	videos, err := index.Videos()
	if err != nil {
		t.Fatal(err)
	}
	if len(videos) != 1 || videos[0].Path != standalone {
		t.Fatalf("unexpected video list: %+v", videos)
	}
	for _, video := range videos {
		if video.Path == movie || video.Path == movieCopy {
			t.Fatalf("motion clip leaked into the video list: %+v", video)
		}
	}

	// Second run: unchanged files are reused, not re-probed.
	if err := scanner.Scan("incremental"); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if reused := scanner.Status().Reused; reused == 0 {
		t.Fatalf("expected reused files on incremental scan: %+v", scanner.Status())
	}

	// Deleting a file marks it removed.
	if err := os.Remove(still); err != nil {
		t.Fatal(err)
	}
	if err := scanner.Scan("incremental"); err != nil {
		t.Fatalf("third scan: %v", err)
	}
	stats, err := index.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Removed == 0 {
		t.Fatalf("removed entries not marked: %+v", stats)
	}
}

// 增量扫描里「静帧没变、片段后到」也必须配上对：未变化的文件只走 Touch、不进 upserts，
// 只看本批会让这类动态照片永远以「照片 + 一条独立视频」两条可见条目存在。
func TestIncrementalScanPairsLateMotionClip(t *testing.T) {
	library := t.TempDir()
	uuid := "8F1D1C6E-3B4A-4C9E-9E0B-2A6F5D4C3B2A"

	stillA := filepath.Join(library, "IMG_2000.HEIC")
	if err := os.WriteFile(stillA, append([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'c'}, []byte("xxxx"+uuid+"xxxx")...), 0o644); err != nil {
		t.Fatal(err)
	}
	// 另一对用大小写不一致的文件名：相机与 NAS 上 .HEIC / .MOV 的大小写并不统一。
	stillB := filepath.Join(library, "IMG_2001.HEIC")
	if err := os.WriteFile(stillB, append([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'c'}, []byte("yyyy")...), 0o644); err != nil {
		t.Fatal(err)
	}

	index, err := store.Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()

	settings, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	current := settings.Get()
	current.Libraries = []config.Library{{ID: "lib", Name: "lib", Path: library}}
	current.MotionPhoto = true
	if err := settings.Update(current); err != nil {
		t.Fatal(err)
	}
	scanner := New(index, settings, events.NewHub(), logging.New("error", nil))

	// 第一次扫描：只有静帧。
	if err := scanner.Scan("incremental"); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	entryA, ok, err := index.GetByPath(stillA)
	if err != nil || !ok || entryA.Kind != "image" {
		t.Fatalf("still should start as a plain image: %+v %v %v", entryA, ok, err)
	}

	// 片段随后才写进共享目录（备份工具分两次写入 / 只补传 .MOV 的常见情形）。
	movieA := filepath.Join(library, "IMG_2000.MOV")
	var movieBytes bytes.Buffer
	movieBytes.Write(bytes.Repeat([]byte{0}, 1024))
	movieBytes.WriteString("com.apple.quicktime.content.identifier")
	movieBytes.WriteString(uuid)
	if err := os.WriteFile(movieA, movieBytes.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	movieB := filepath.Join(library, "img_2001.mov")
	if err := os.WriteFile(movieB, bytes.Repeat([]byte{1}, 2048), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := scanner.Scan("incremental"); err != nil {
		t.Fatalf("second scan: %v", err)
	}

	pairedA, ok, err := index.GetByPath(stillA)
	if err != nil || !ok || pairedA.Kind != "motion" || pairedA.MotionPath != movieA {
		t.Fatalf("late motion clip not paired with the still: %+v %v %v", pairedA, ok, err)
	}
	clipA, ok, err := index.GetByPath(movieA)
	if err != nil || !ok || clipA.Kind != "motion-clip" {
		t.Fatalf("late motion clip not marked as a clip: %+v %v %v", clipA, ok, err)
	}
	// 大小写不一致的同名片段同样要配上。
	pairedB, ok, err := index.GetByPath(stillB)
	if err != nil || !ok || pairedB.Kind != "motion" || pairedB.MotionPath != movieB {
		t.Fatalf("case-insensitive pairing failed: %+v %v %v", pairedB, ok, err)
	}
	clipB, ok, err := index.GetByPath(movieB)
	if err != nil || !ok || clipB.Kind != "motion-clip" {
		t.Fatalf("case-insensitive clip not marked: %+v %v %v", clipB, ok, err)
	}
	videos, err := index.Videos()
	if err != nil {
		t.Fatal(err)
	}
	for _, video := range videos {
		if video.Path == movieA || video.Path == movieB {
			t.Fatalf("motion clip leaked into the video list: %+v", video)
		}
	}
}

func TestIncompleteScanRetainsEntries(t *testing.T) {
	library := t.TempDir()
	protected := filepath.Join(library, "Protected")
	if err := os.Mkdir(protected, 0o755); err != nil {
		t.Fatal(err)
	}
	photo := filepath.Join(protected, "IMG_20240102_030405.jpg")
	writeJPEG(t, photo, 200, 120)

	index, err := store.Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()

	settings, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	current := settings.Get()
	current.Libraries = []config.Library{{ID: "lib", Name: "lib", Path: library}}
	if err := settings.Update(current); err != nil {
		t.Fatal(err)
	}
	scanner := New(index, settings, events.NewHub(), logging.New("error", nil))
	if err := scanner.Scan("incremental"); err != nil {
		t.Fatalf("initial scan: %v", err)
	}

	if err := os.Chmod(protected, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(protected, 0o755)
	if _, err := os.ReadDir(protected); err == nil {
		t.Skip("当前测试环境仍可读取无权限目录")
	}

	if err := scanner.Scan("incremental"); err != nil {
		t.Fatalf("incomplete scan should finish with a visible status: %v", err)
	}
	progress := scanner.Status()
	if progress.Message != "scan incomplete; existing entries retained" {
		t.Fatalf("unexpected incomplete status: %+v", progress)
	}
	stats, err := index.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Removed != 0 {
		t.Fatalf("incomplete scan removed existing entries: %+v", stats)
	}
}

// 日期未知的条目不得落到 1970-01-01：Capture=0 且 DayKey 为空，
// 这样 /days 与 /days/{day}/items 都不会把它聚合进任何自然日。
func TestEntryFromResultMarksUnknownDate(t *testing.T) {
	unknown := entryFromResult(&media.Result{
		Path:          "/lib/DSC_0001.JPG",
		ID:            "m1",
		Kind:          media.KindImage,
		Size:          10,
		Modified:      time.Time{},
		CaptureSource: "unknown",
	}, 1)
	if unknown.Capture != 0 || unknown.DayKey != "" {
		t.Fatalf("unknown date must stay out of day grouping: %+v", unknown)
	}
	if unknown.Modified != 0 {
		t.Fatalf("negative modified time must be clamped to 0: %+v", unknown)
	}

	known := entryFromResult(&media.Result{
		Path:          "/lib/IMG_20240102_030405.jpg",
		ID:            "m2",
		Kind:          media.KindImage,
		Size:          10,
		Capture:       time.Date(2024, 1, 2, 3, 4, 5, 0, time.Local),
		CaptureSource: "filename",
	}, 1)
	if known.Capture <= 0 || known.DayKey != "2024-01-02" {
		t.Fatalf("valid capture must be grouped by day: %+v", known)
	}
}

func writeJPEG(t *testing.T, path string, width, height int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 64, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 探测版本 4 只改了视频：升级后的增量扫描重读旧版本写入的视频行，照片行原样复用。
// 同时验证扫描正常结束后会触发收尾回调。
func TestIncrementalScanReprobesOnlyOutdatedVideos(t *testing.T) {
	library := t.TempDir()
	photo := filepath.Join(library, "IMG_20240102_030405.jpg")
	writeJPEG(t, photo, 200, 120)
	video := filepath.Join(library, "IMG_2000.MOV")
	if err := os.WriteFile(video, []byte("not-a-real-movie-but-classified-by-extension"), 0o644); err != nil {
		t.Fatal(err)
	}

	index, err := store.Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	settings, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	current := settings.Get()
	current.Libraries = []config.Library{{ID: "lib", Name: "lib", Path: library}}
	if err := settings.Update(current); err != nil {
		t.Fatal(err)
	}
	scanner := New(index, settings, events.NewHub(), logging.New("error", nil))
	finished := make(chan struct{}, 4)
	scanner.OnFinished(func() { finished <- struct{}{} })

	if err := scanner.Scan("incremental"); err != nil {
		t.Fatalf("initial scan: %v", err)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("OnFinished was not called after a completed scan")
	}

	// 把两行都改写成上一版探测逻辑的产物。
	existing, err := index.Existing()
	if err != nil {
		t.Fatal(err)
	}
	var downgraded []store.Entry
	for _, entry := range existing {
		entry.ProbeVersion = 3
		downgraded = append(downgraded, entry)
	}
	if len(downgraded) != 2 {
		t.Fatalf("indexed entries = %d, want 2", len(downgraded))
	}
	if err := index.UpsertMany(downgraded); err != nil {
		t.Fatal(err)
	}

	if err := scanner.Scan("incremental"); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	progress := scanner.Status()
	if progress.Reused != 1 || progress.MediaIndexed != 1 {
		t.Fatalf("reused=%d indexed=%d, want the photo reused and the video re-probed", progress.Reused, progress.MediaIndexed)
	}
	after, ok, err := index.GetByPath(video)
	if err != nil || !ok {
		t.Fatalf("video row missing: ok=%v err=%v", ok, err)
	}
	if after.ProbeVersion != media.ProbeVersion {
		t.Fatalf("video probe version = %d, want %d", after.ProbeVersion, media.ProbeVersion)
	}
}
