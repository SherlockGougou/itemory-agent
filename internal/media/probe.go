// Package media extracts the metadata Itemory needs from photos, videos and RAW
// files using only the file head/tail (no full reads, no demosaicing).
package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Entry kinds.
const (
	KindImage      = "image"
	KindRaw        = "raw"
	KindVideo      = "video"
	KindMotion     = "motion"
	KindMotionClip = "motion-clip"
)

var imageExts = map[string]bool{
	"jpg": true, "jpeg": true, "png": true, "heic": true, "heif": true,
	"webp": true, "tif": true, "tiff": true, "gif": true, "bmp": true, "avif": true,
}

var rawExts = map[string]bool{
	"nef": true, "nrw": true, "cr2": true, "cr3": true, "arw": true, "srf": true, "sr2": true,
	"raf": true, "orf": true, "rw2": true, "pef": true, "srw": true, "rwl": true, "dng": true,
	"x3f": true, "iiq": true, "3fr": true, "mef": true, "mos": true, "erf": true, "rwz": true,
}

var videoExts = map[string]bool{
	"mp4": true, "mov": true, "m4v": true, "avi": true, "mkv": true, "webm": true,
	"mpg": true, "mpeg": true, "3gp": true, "wmv": true, "flv": true, "mts": true, "m2ts": true,
}

// Extension reports the lowercase extension without the dot.
func Extension(name string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
}

// Classify maps a filename to a media kind.
func Classify(name string) (string, bool) {
	ext := Extension(name)
	switch {
	case imageExts[ext]:
		return KindImage, true
	case rawExts[ext]:
		return KindRaw, true
	case videoExts[ext]:
		return KindVideo, true
	default:
		return "", false
	}
}

// IsRaw / IsVideo / IsImage convenience helpers.
func IsRaw(name string) bool   { return rawExts[Extension(name)] }
func IsVideo(name string) bool { return videoExts[Extension(name)] }
func IsImage(name string) bool { return imageExts[Extension(name)] }

// Options tunes how much of each file is read while probing.
type Options struct {
	ImageHead int64 // default 256 KB
	RawHead   int64 // default 4 MB
	RawTail   int64 // default 4 MB (fallback scan)
}

func (o Options) imageHead() int64 {
	if o.ImageHead > 0 {
		return o.ImageHead
	}
	return 256 << 10
}

func (o Options) rawHead() int64 {
	if o.RawHead > 0 {
		return o.RawHead
	}
	return 4 << 20
}

func (o Options) rawTail() int64 {
	if o.RawTail > 0 {
		return o.RawTail
	}
	return 4 << 20
}

// Motion describes an embedded or paired motion segment.
type Motion struct {
	Offset       int64  `json:"offset,omitempty"`
	Length       int64  `json:"length,omitempty"`
	Identifier   string `json:"identifier,omitempty"`
	Vendor       string `json:"vendor,omitempty"`
	Confidence   string `json:"confidence,omitempty"`
	ExternalPath string `json:"externalPath,omitempty"`
}

// RawPreview points at an embedded JPEG preview inside a RAW file.
type RawPreview struct {
	Offset int64 `json:"offset"`
	Length int64 `json:"length"`
	Width  int   `json:"width"`
	Height int   `json:"height"`
}

// Result is the probe outcome for one media file.
type Result struct {
	Path          string
	Kind          string
	Size          int64
	Modified      time.Time
	ID            string
	Capture       time.Time
	CaptureSource string
	Width         int
	Height        int
	Duration      float64
	// 拍摄位置（WGS84 十进制度），0/0 表示没有定位（照片走 EXIF GPS IFD，
	// 视频走 QuickTime 的 ISO6709 atom）。地图侧的可见性谓词就是 `latitude <> 0`。
	Latitude  float64
	Longitude float64
	// 镜头与曝光参数。这里用零值当缺失哨兵，与 Latitude/Longitude 同一约定：
	// 索引层取零值即代表「没有这项元数据」。解析层已用 HasXxx 显式判过存在性，
	// 因此「确实是 0」与「没有」只在海拔恰好为 0 米这一种情形下无法区分——
	// 该精度损失可接受，换来的是索引层不必为每项元数据再存一列存在性标记。
	Make        string
	Model       string
	LensModel   string
	Aperture    float64 // 光圈 F 值
	Exposure    float64 // 快门时间（秒）
	ISO         int
	FocalLength float64 // 等效焦距（mm）
	Altitude    float64 // 海拔（米，海平面以下为负）
	Motion      *Motion
	RawPreview  *RawPreview
}

// ProbeVersion 是探测逻辑的版本号，写进索引的 probe_version 列。
//
// 增量扫描的复用判据包含它：文件没变但探测版本落后时同样重新探测一次。
// 这让「新增一项元数据」不必要求用户手工跑全量重扫——升级容器后的第一次增量扫描
// 就会顺带把旧条目补齐（代价是每文件一次 64–256 KB 的头读取，7 万条约分钟级）。
//
// 1：拍摄时间 / 宽高 / Make / Model / 时长（0.3.x）
// 2：EXIF GPS IFD + 视频 QuickTime ISO6709 位置
// 3：EXIF 光圈 / 快门 / ISO / 焦距 / 海拔 / 镜头型号，并首次真正消费 Make / Model
// 4：视频的画面尺寸（tkhd）与容器创建时间（mvhd）
const ProbeVersion = 4

// MinProbeVersion 返回某类条目可以直接复用的最低探测版本。
//
// 版本 4 只改了视频的探测结果，照片与 RAW 仍按版本 3 复用，
// 升级后的第一次增量扫描因此只重读视频，不必把整库照片的文件头再读一遍。
func MinProbeVersion(kind string) int {
	if kind == KindVideo {
		return ProbeVersion
	}
	return 3
}

// ProbeFile inspects one media file. It never reads more than the configured
// head/tail windows, so it is safe to run over 100k+ libraries.
func ProbeFile(path string, size int64, modified time.Time, opts Options) (*Result, error) {
	kind, ok := Classify(path)
	if !ok {
		return nil, ErrNotMedia
	}
	headSize := opts.imageHead()
	if kind == KindRaw {
		headSize = opts.rawHead()
	}
	head, err := ReadPrefix(path, headSize)
	if err != nil {
		return nil, err
	}
	if len(head) == 0 {
		return nil, fmt.Errorf("empty file %s", path)
	}

	res := &Result{
		Path:     path,
		Kind:     kind,
		Size:     size,
		Modified: modified,
	}
	res.ID = contentID(head, size)

	exifInfo, hasEXIF := parseEXIFFromHead(head)
	var containerTime time.Time
	if kind == KindVideo {
		containerTime, _ = VideoCreationTime(path)
	}
	if capture, source, ok := detectCaptureTime(exifInfo, hasEXIF, head, path, modified, containerTime); ok {
		res.Capture, res.CaptureSource = capture, source
	} else {
		// 没有任何可信时间：保持零值并标记 unknown，由索引层写成「日期未知」而不是 1970-01-01
		res.Capture, res.CaptureSource = time.Time{}, "unknown"
	}
	if hasEXIF && exifInfo.HasGPS {
		res.Latitude, res.Longitude = exifInfo.Latitude, exifInfo.Longitude
	}
	// 镜头与曝光参数。Make/Model 在解析层早就读出来了，但此前没有任何一处消费它们，
	// 属于「解析了却到不了索引」的死字段；这次一并落到 Result，随 ProbeVersion 3 补齐。
	if hasEXIF {
		res.Make, res.Model, res.LensModel = exifInfo.Make, exifInfo.Model, exifInfo.LensModel
		if exifInfo.HasAperture {
			res.Aperture = exifInfo.Aperture
		}
		if exifInfo.HasExposure {
			res.Exposure = exifInfo.Exposure
		}
		if exifInfo.HasISO {
			res.ISO = exifInfo.ISO
		}
		if exifInfo.HasFocalLength {
			res.FocalLength = exifInfo.FocalLength
		}
		if exifInfo.HasAltitude {
			res.Altitude = exifInfo.Altitude
		}
	}

	if cfg, _, err := image.DecodeConfig(bytes.NewReader(head)); err == nil {
		res.Width, res.Height = cfg.Width, cfg.Height
	}
	// HEIC/RAW and truncated heads often fail DecodeConfig: fall back to EXIF.
	if (res.Width == 0 || res.Height == 0) && hasEXIF && exifInfo.Width > 0 && exifInfo.Height > 0 {
		res.Width, res.Height = exifInfo.Width, exifInfo.Height
	}

	if kind == KindImage {
		if motion := detectMotion(head, size); motion != nil {
			res.Motion = motion
			res.Kind = KindMotion
		}
	}

	if kind == KindRaw {
		res.RawPreview = findBestEmbeddedJPEG(head, 0)
		if res.RawPreview == nil {
			if tail, err := ReadSuffix(path, opts.rawTail()); err == nil && len(tail) > 0 {
				base := size - int64(len(tail))
				res.RawPreview = findBestEmbeddedJPEG(tail, base)
			}
		}
		if res.RawPreview != nil && res.Width == 0 {
			res.Width, res.Height = res.RawPreview.Width, res.RawPreview.Height
		}
	}

	// 视频时长、画面尺寸与拍摄位置：都按 box 跳读，不把 mdat 读进内存。
	if kind == KindVideo {
		res.Duration = VideoDuration(path)
		if res.Width == 0 || res.Height == 0 {
			res.Width, res.Height = VideoTrackSize(path)
		}
		// 视频没有 EXIF，位置写在 QuickTime 的 udta/©xyz（ISO6709 串）里。
		if res.Latitude == 0 && res.Longitude == 0 {
			if coordinate := VideoLocation(path); coordinate != nil {
				res.Latitude, res.Longitude = coordinate.Latitude, coordinate.Longitude
			}
		}
	}

	return res, nil
}

// contentID is a rename-stable, cheap content fingerprint: sha256(head64k + size).
func contentID(head []byte, size int64) string {
	window := head
	if len(window) > 64<<10 {
		window = window[:64<<10]
	}
	h := sha256.New()
	h.Write(window)
	fmt.Fprintf(h, "|%d", size)
	return "m" + hex.EncodeToString(h.Sum(nil))[:24]
}

// detectCaptureTime 按可信度从高到低取拍摄时间。
//
// 文件名排在容器时间之前：VID_20230506_150809.mp4 这类文件名是拍摄地的本地时间，
// 而部分 Android 机型把本地时间当作 UTC 写进 mvhd，会差出几个小时并落到相邻的一天。
func detectCaptureTime(exif EXIFInfo, hasEXIF bool, head []byte, path string, modified, container time.Time) (time.Time, string, bool) {
	if hasEXIF && exif.HasCapture && plausibleCapture(exif.Capture) {
		return exif.Capture, "exif", true
	}
	if t, ok := detectTextDate(head); ok && plausibleCapture(t) {
		return t, "exif-text", true
	}
	if t, ok := ParseFilenameDate(filepath.Base(path)); ok && plausibleCapture(t) {
		return t, "filename", true
	}
	if plausibleCapture(container) {
		return container, "container", true
	}
	if plausibleCapture(modified) {
		return modified, "mtime", true
	}
	return time.Time{}, "", false
}

// minPlausibleCapture 是拍摄时间的可信下界（1980-01-01）。
// 相机时钟未设置时常见 1970-01-01 或更早的占位时间，若直接采信会把这些照片
// 归到"1970 年"的假日期里，在 App 时间线上形成无法解释的远古记忆。
const minPlausibleCapture = 315532800

func plausibleCapture(t time.Time) bool {
	return !t.IsZero() && t.Unix() >= minPlausibleCapture
}

var exifTextDateRe = regexp.MustCompile(`(19|20)\d{2}:[01]\d:[0-3]\d [0-2]\d:[0-5]\d:[0-5]\d`)

// detectTextDate is a last-resort scan for an EXIF-looking timestamp in the head
// (covers containers whose TIFF block we cannot parse structurally).
func detectTextDate(head []byte) (time.Time, bool) {
	match := exifTextDateRe.Find(head)
	if match == nil {
		return time.Time{}, false
	}
	value := string(match)
	for _, layout := range []string{"2006:01:02 15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// --- embedded JPEG previews (RAW) ---

// findBestEmbeddedJPEG scans a buffer for complete JPEG streams and returns the
// largest one (offset is relative to the start of the file).
func findBestEmbeddedJPEG(buf []byte, base int64) *RawPreview {
	var best *RawPreview
	pos := 0
	for i := 0; i < 64; i++ {
		start := bytes.Index(buf[pos:], []byte{0xFF, 0xD8, 0xFF})
		if start < 0 {
			break
		}
		start += pos
		end := bytes.Index(buf[start:], []byte{0xFF, 0xD9})
		if end < 0 {
			break
		}
		end += start + 2
		candidate := buf[start:end]
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(candidate)); err == nil {
			preview := &RawPreview{
				Offset: base + int64(start),
				Length: int64(end - start),
				Width:  cfg.Width,
				Height: cfg.Height,
			}
			if best == nil || preview.Width*preview.Height > best.Width*best.Height {
				best = preview
			}
		}
		pos = end
	}
	return best
}

// --- motion photos ---

var microVideoRe = regexp.MustCompile(`GCamera:MicroVideoOffset="(\d+)"`)
var motionPhotoV3Re = regexp.MustCompile(`(?s)Item:Semantic="MotionPhoto".{0,400}?Item:Length="(\d+)"|Item:Length="(\d+)".{0,400}?Item:Semantic="MotionPhoto"`)

func detectMotion(head []byte, size int64) *Motion {
	if m := microVideoRe.FindSubmatch(head); m != nil {
		if n, err := strconv.ParseInt(string(m[1]), 10, 64); err == nil && validSegment(n, size) {
			return &Motion{
				Offset:     size - n,
				Length:     n,
				Vendor:     "android",
				Confidence: "xmp-microvideo",
			}
		}
	}
	if match := motionPhotoV3Re.FindSubmatch(head); match != nil {
		raw := match[1]
		if len(raw) == 0 {
			raw = match[2]
		}
		if n, err := strconv.ParseInt(string(raw), 10, 64); err == nil && validSegment(n, size) {
			return &Motion{
				Offset:     size - n,
				Length:     n,
				Vendor:     "android",
				Confidence: "xmp-motionphoto-v3",
			}
		}
	}
	return nil
}

func validSegment(length, size int64) bool {
	return length > 4096 && length < size
}
