// Package thumbs generates and caches grid thumbnails.
//
// Generator priority: vipsthumbnail (libvips, container) → sips (macOS dev) →
// pure-Go fallback for JPEG/PNG. RAW files use the embedded preview extracted by
// the probe, so no demosaicing happens on the hot path.
package thumbs

import (
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SherlockGougou/itemory-agent/internal/config"
	"github.com/SherlockGougou/itemory-agent/internal/logging"
	"github.com/SherlockGougou/itemory-agent/internal/media"
	"github.com/SherlockGougou/itemory-agent/internal/store"
)

// SettingsProvider decouples the thumbnail manager from the settings manager.
type SettingsProvider interface {
	Get() config.Settings
}

// flightCall 代表正在进行中的单次缩略图生成任务。
type flightCall struct {
	wg  sync.WaitGroup
	val string
	err error
}

// Manager owns the thumbnail cache directory.
type Manager struct {
	dir          string
	settings     SettingsProvider
	log          *logging.Logger
	flightMu     sync.Mutex
	inFlight     map[string]*flightCall
	sem          chan struct{}
	evictCounter atomic.Int64
	evictMu      sync.Mutex
	evicting     bool

	// 统计结果缓存。控制台以秒级频率拉 dashboard，而统计要全量 WalkDir，
	// 在几万张缩略图上就是实打实的 IO；写入/清理/淘汰会立即让它失效。
	statsMu   sync.Mutex
	statsAt   time.Time
	statsN    int
	statsSize int64
}

// generateTimeout 是单次缩略图生成的硬上限。
const generateTimeout = 60 * time.Second

// statsTTL 是缓存统计的最长陈旧时间。取值权衡：够短以致目录被手动改动后能自愈，
// 够长以致一秒一次的 dashboard 轮询不会真的去遍历目录。
const statsTTL = 5 * time.Second

// maxThumbWorkers 是并发外部生成进程数的上限。
//
// 以前是 GOMAXPROCS*2 且不封顶：24 核的机器上就是 48 个并发的 vipsthumbnail/ffmpeg。
// 实测（2026-09-21，24 核 fnOS）：
//   - 单独一次 2GB 视频抽帧就要 512m 以上——512m 失败、768m 才成功；
//   - 48 路并发 + 512m 上限时，视频抽帧 8 个全被 OOM 杀掉。
//
// 这种失败是**静默**的：日志里只有进程被杀，界面表现是「有些缩略图就是不生成」。
//
// 封顶到 8 后，批量生成 24 张 25MB 图的耗时从 0.3–0.4 秒变成 0.6 秒（约 2 倍，
// 不是 6 倍——这类任务是 I/O 与解码受限，不是纯 CPU），换来内存占用与核心数解耦。
// 这个值没有做成设置项：加一个字段要动 config / 预设 / 校验 / PATCH 白名单 / i18n，
// 而它的正确取值基本不随用户变。
const maxThumbWorkers = 8

// thumbWorkers 计算并发上限：小机器沿用 2×核数的老行为（至少 4），大机器封顶。
func thumbWorkers() int {
	workers := runtime.GOMAXPROCS(0) * 2
	if workers > maxThumbWorkers {
		workers = maxThumbWorkers
	}
	if workers < 4 {
		workers = 4
	}
	return workers
}

// New creates the cache directory if needed.
func New(dir string, settings SettingsProvider, log *logging.Logger) *Manager {
	_ = os.MkdirAll(dir, 0o755)
	workers := thumbWorkers()
	return &Manager{
		dir:      dir,
		settings: settings,
		log:      log,
		inFlight: make(map[string]*flightCall),
		sem:      make(chan struct{}, workers),
	}
}

// Dir returns the cache root (used by diagnostics).
func (m *Manager) Dir() string { return m.dir }

// CachePath is the deterministic cache location for one entry + size.
func (m *Manager) CachePath(id string, size int) string {
	return filepath.Join(m.dir, fmt.Sprintf("%s-%d.jpg", id, size))
}

// Ensure generates (or returns) the cached thumbnail for an entry.
// 针对并发请求做了单飞去重（singleflight）与有界并发控制（sem），彻底消除全局锁串行化。
func (m *Manager) Ensure(entry store.Entry, size int) (string, error) {
	settings := m.settings.Get()
	if size <= 0 {
		size = settings.ThumbSize
	}
	if size <= 0 {
		size = 512
	}
	out := m.CachePath(entry.ID, size)
	if fi, err := os.Stat(out); err == nil && fi.Size() > 0 {
		return out, nil
	}

	key := fmt.Sprintf("%s-%d", entry.ID, size)

	m.flightMu.Lock()
	if call, ok := m.inFlight[key]; ok {
		m.flightMu.Unlock()
		call.wg.Wait()
		return call.val, call.err
	}
	call := &flightCall{}
	call.wg.Add(1)
	m.inFlight[key] = call
	m.flightMu.Unlock()

	defer func() {
		m.flightMu.Lock()
		delete(m.inFlight, key)
		m.flightMu.Unlock()
	}()

	// 限制并发外部进程数，避免多核系统因瞬间爆发过多进程导致 OOM 或 CPU 颠簸
	m.sem <- struct{}{}
	defer func() { <-m.sem }()

	// 取得信号量后二次检查缓存
	if fi, err := os.Stat(out); err == nil && fi.Size() > 0 {
		call.val = out
		call.wg.Done()
		return out, nil
	}

	tmpDir := filepath.Join(m.dir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		call.err = err
		call.wg.Done()
		return "", err
	}
	tmpOut, err := os.CreateTemp(tmpDir, "thumb-*.jpg")
	if err != nil {
		call.err = err
		call.wg.Done()
		return "", err
	}
	tmpPath := tmpOut.Name()
	_ = tmpOut.Close()
	defer os.Remove(tmpPath)

	inputPath := entry.Path
	cleanupInput := func() {}
	if entry.Kind == media.KindRaw && entry.RawPreviewLength > 0 {
		if preview, err := media.ReadRange(entry.Path, entry.RawPreviewOffset, entry.RawPreviewLength); err == nil && len(preview) > 0 {
			previewFile, err := os.CreateTemp(tmpDir, "preview-*.jpg")
			if err == nil {
				_, _ = previewFile.Write(preview)
				_ = previewFile.Close()
				inputPath = previewFile.Name()
				cleanupInput = func() { _ = os.Remove(previewFile.Name()) }
			}
		}
	}
	defer cleanupInput()

	var genErr error
	if entry.Kind == media.KindVideo || entry.Kind == media.KindMotionClip {
		genErr = generateWithFFmpeg(inputPath, tmpPath, size)
	} else {
		genErr = m.generateImage(inputPath, tmpPath, size)
	}
	if genErr != nil {
		call.err = genErr
		call.wg.Done()
		return "", genErr
	}
	if err := os.Rename(tmpPath, out); err != nil {
		call.err = err
		call.wg.Done()
		return "", err
	}
	m.scheduleEvict()
	call.val = out
	call.wg.Done()
	return out, nil
}

// scheduleEvict 让淘汰在后台执行，并且仅在累计生成一定数量后才尝试触发，杜绝每张图都跑全目录 WalkDir
func (m *Manager) scheduleEvict() {
	if m.evictCounter.Add(1)%100 != 0 {
		return
	}
	m.evictMu.Lock()
	if m.evicting {
		m.evictMu.Unlock()
		return
	}
	m.evicting = true
	m.evictMu.Unlock()
	go func() {
		defer func() {
			m.evictMu.Lock()
			m.evicting = false
			m.evictMu.Unlock()
		}()
		if err := m.Evict(); err != nil {
			m.log.Warn("thumb eviction failed", "error", err)
		}
	}()
}

func (m *Manager) generateImage(in, out string, size int) error {
	if err := requireOutput(generateWithVips(in, out, size), out); err == nil {
		return nil
	} else if !isMissingBinary(err) {
		m.log.Debug("vipsthumbnail failed", "error", err)
	}
	if err := requireOutput(generateWithSips(in, out, size), out); err == nil {
		return nil
	} else if !isMissingBinary(err) {
		m.log.Debug("sips failed", "error", err)
	}
	if err := requireOutput(generateWithFFmpegImage(in, out, size), out); err == nil {
		return nil
	} else if !isMissingBinary(err) {
		m.log.Debug("ffmpeg image failed", "error", err)
	}
	return generatePureGo(in, out, size)
}

// requireOutput 把「退出码为 0 但没有写出内容」视为失败：sips 遇到不存在或无法识别的输入时
// 只打印警告并返回 0，输出仍是 Ensure 预先创建的空临时文件，不能当成缩略图缓存。
func requireOutput(genErr error, out string) error {
	if genErr != nil {
		return genErr
	}
	fi, err := os.Stat(out)
	if err != nil {
		return err
	}
	if fi.Size() == 0 {
		return fmt.Errorf("generator produced empty output")
	}
	return nil
}

func generateWithVips(in, out string, size int) error {
	binary, err := exec.LookPath("vipsthumbnail")
	if err != nil {
		return err
	}
	return runGenerator(binary, in, "--output", out, "-s", strconv.Itoa(size))
}

func generateWithSips(in, out string, size int) error {
	binary, err := exec.LookPath("sips")
	if err != nil {
		return err
	}
	// 显式传入 -s format jpeg，保证即使输入为 HEIC/HEIF/TIFF，输出也必然转为标准 JPEG。
	// 否则 sips 会保留原格式编码并误用 .jpg 扩展名，导致非 Safari 浏览器全部解码失败。
	return runGenerator(binary, "-s", "format", "jpeg", "-Z", strconv.Itoa(size), in, "--out", out)
}

// generateWithFFmpegImage 使用 ffmpeg 转码并缩放静态图像（特别支持含 Tile Grid 的 HEIC/HEIF）。
func generateWithFFmpegImage(in, out string, size int) error {
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		return err
	}
	filter := fmt.Sprintf("[0:v]scale='min(%d,iw)':'min(%d,ih)':force_original_aspect_ratio=decrease[out]", size, size)
	return runGenerator(binary,
		"-y",
		"-v", "error",
		"-i", in,
		"-filter_complex", filter,
		"-map", "[out]",
		"-frames:v", "1",
		"-q:v", "3",
		out,
	)
}

func generateWithFFmpeg(in, out string, size int) error {
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		return fmt.Errorf("ffmpeg not available")
	}
	scale := fmt.Sprintf("scale='min(%d,iw)':-2", size)
	return runGenerator(binary, "-y", "-ss", "0.5", "-i", in, "-frames:v", "1", "-vf", scale, "-q:v", "3", out)
}

// runGenerator 跑一次外部生成命令，带硬超时（超时即杀掉进程）。
func runGenerator(binary string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), generateTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%s timed out after %s", filepath.Base(binary), generateTimeout)
	}
	if err != nil {
		return fmt.Errorf("%s: %v: %s", filepath.Base(binary), err, string(output))
	}
	return nil
}

// generatePureGo is the dependency-free fallback (JPEG/PNG/GIF only).
func generatePureGo(in, out string, size int) error {
	file, err := os.Open(in)
	if err != nil {
		return err
	}
	defer file.Close()
	img, _, err := image.Decode(file)
	if err != nil {
		return fmt.Errorf("decode %s: %w", filepath.Base(in), err)
	}
	scaled := downscale(img, size)
	dst, err := os.Create(out)
	if err != nil {
		return err
	}
	defer dst.Close()
	return jpeg.Encode(dst, scaled, &jpeg.Options{Quality: 82})
}

func downscale(src image.Image, maxDim int) image.Image {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return src
	}
	if w <= maxDim && h <= maxDim {
		return src
	}
	scale := float64(maxDim) / float64(max(w, h))
	newW, newH := int(float64(w)*scale), int(float64(h)*scale)
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	for y := 0; y < newH; y++ {
		sy0 := bounds.Min.Y + y*h/newH
		sy1 := bounds.Min.Y + (y+1)*h/newH
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for x := 0; x < newW; x++ {
			sx0 := bounds.Min.X + x*w/newW
			sx1 := bounds.Min.X + (x+1)*w/newW
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var r, g, b, a, n uint64
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					cr, cg, cb, ca := src.At(sx, sy).RGBA()
					r += uint64(cr >> 8)
					g += uint64(cg >> 8)
					b += uint64(cb >> 8)
					a += uint64(ca >> 8)
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			dst.Set(x, y, color.RGBA{
				R: uint8(r / n), G: uint8(g / n), B: uint8(b / n), A: uint8(a / n),
			})
		}
	}
	return dst
}

// Stats returns cache file count and total bytes.
//
// 结果带 statsTTL 缓存：dashboard 会被控制台以秒级频率拉取，而这里是一次全量
// WalkDir，在几万张缩略图上就是实打实的 IO。
//
// 故意**不**在每次写入缩略图时失效缓存：浏览相册时写入是连续的，逐个失效等于
// 没有缓存。取而代之的是让读数最多滞后 statsTTL —— 对一根「水位」进度条而言
// 完全可以接受。真正需要即时反映的用户操作（清空、改上限触发淘汰）会显式失效。
func (m *Manager) Stats() (int, int64) {
	m.statsMu.Lock()
	if !m.statsAt.IsZero() && time.Since(m.statsAt) < statsTTL {
		count, total := m.statsN, m.statsSize
		m.statsMu.Unlock()
		return count, total
	}
	m.statsMu.Unlock()

	count, total := m.walkStats()

	m.statsMu.Lock()
	m.statsN, m.statsSize, m.statsAt = count, total, time.Now()
	m.statsMu.Unlock()
	return count, total
}

// invalidateStats 让下一次 Stats 重新遍历目录。
func (m *Manager) invalidateStats() {
	m.statsMu.Lock()
	m.statsAt = time.Time{}
	m.statsMu.Unlock()
}

func (m *Manager) walkStats() (int, int64) {
	var count int
	var total int64
	_ = filepath.WalkDir(m.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "tmp" {
				return filepath.SkipDir
			}
			return nil
		}
		if info, err := d.Info(); err == nil {
			count++
			total += info.Size()
		}
		return nil
	})
	return count, total
}

// Evict enforces the configured cache limit with LRU-by-mtime.
func (m *Manager) Evict() error {
	// 刻意不取 m.mu：Evict 由每次生成后的后台任务触发，若与生成互斥，
	// 一次慢生成就会把「保存设置后立即回收」也一起阻塞住。
	// 被淘汰的永远是最旧的文件，因此与刚写入的新缩略图相撞的概率极低，且下次请求会重建。
	limit := m.settings.Get().ThumbCacheLimitBytes
	if limit <= 0 {
		return nil
	}
	type cacheFile struct {
		path    string
		size    int64
		modTime time.Time
	}
	var files []cacheFile
	var total int64
	_ = filepath.WalkDir(m.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "tmp" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		files = append(files, cacheFile{path: path, size: info.Size(), modTime: info.ModTime()})
		total += info.Size()
		return nil
	})
	if total <= limit {
		return nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })
	target := limit * 9 / 10
	removedAny := false
	for _, f := range files {
		if total <= target {
			break
		}
		if err := os.Remove(f.path); err == nil {
			total -= f.size
			removedAny = true
		}
	}
	// 淘汰通常由「调低缓存上限」触发，用户就在等读数掉下来，所以立刻失效。
	// 后台按 1/100 频率自动触发的那些，多失效一次也只是多走一趟目录。
	if removedAny {
		m.invalidateStats()
	}
	return nil
}

// Clear removes every cached thumbnail.
func (m *Manager) Clear() error {
	m.flightMu.Lock()
	defer m.flightMu.Unlock()
	// 清空是用户显式动作，读数必须立刻归零，不能等 TTL 到期。
	defer m.invalidateStats()
	if err := os.RemoveAll(m.dir); err != nil {
		return err
	}
	return os.MkdirAll(m.dir, 0o755)
}

func isMissingBinary(err error) bool {
	return err != nil && (os.IsNotExist(err) || containsAny(err.Error(), "executable file not found", "no such file"))
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if n != "" && len(s) >= len(n) {
			for i := 0; i+len(n) <= len(s); i++ {
				if s[i:i+len(n)] == n {
					return true
				}
			}
		}
	}
	return false
}
