// Package benchmark measures per-file probe and thumbnail cost so the app can
// calibrate presets on the real NAS.
package benchmark

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/SherlockGougou/itemory-agent/internal/config"
	"github.com/SherlockGougou/itemory-agent/internal/logging"
	"github.com/SherlockGougou/itemory-agent/internal/media"
	"github.com/SherlockGougou/itemory-agent/internal/store"
	"github.com/SherlockGougou/itemory-agent/internal/thumbs"
)

// Report is the benchmark outcome.
type Report struct {
	Dir               string             `json:"dir"`
	Sampled           int                `json:"sampled"`
	ProbeMSAvg        float64            `json:"probeMsAvg"`
	ThumbMSAvg        float64            `json:"thumbMsAvg"`
	ThumbFailures     int                `json:"thumbFailures"`
	ProbeBytesAvg     int64              `json:"probeBytesAvg"`
	ByExtension       map[string]int     `json:"byExtension"`
	ETAProbe10kMin    float64            `json:"etaProbe10kMinutes"`
	ETAProbe100kMin   float64            `json:"etaProbe100kMinutes"`
	ETAThumb10kMin    float64            `json:"etaThumb10kMinutes"`
	ETAThumb100kMin   float64            `json:"etaThumb100kMinutes"`
	ThumbnailsEnabled bool               `json:"thumbnailsEnabled"`
	Notes             []string           `json:"notes"`
	Generator         map[string]bool    `json:"generatorTools"`
	Timings           map[string]float64 `json:"-"`
}

type settingsProvider struct{ settings config.Settings }

func (s settingsProvider) Get() config.Settings { return s.settings }

// Run samples up to `sample` media files below dir and measures the hot paths.
func Run(dir string, sample int) (Report, error) {
	if sample <= 0 {
		sample = 200
	}
	report := Report{
		Dir:         dir,
		ByExtension: map[string]int{},
		Generator: map[string]bool{
			"vipsthumbnail": toolAvailable("vipsthumbnail"),
			"ffmpeg":        toolAvailable("ffmpeg"),
		},
	}

	var files []string
	visited := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		visited++
		if visited > 50000 {
			return filepath.SkipAll
		}
		if _, ok := media.Classify(d.Name()); ok {
			files = append(files, path)
			if len(files) >= sample {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return report, err
	}
	if len(files) == 0 {
		return report, fmt.Errorf("no media files found under %s", dir)
	}
	sort.Strings(files)

	settings := config.Defaults()
	tmpDir, err := os.MkdirTemp("", "itemory-bench-*")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(tmpDir)
	log := logging.New("warn", nil)
	thumbMgr := thumbs.New(filepath.Join(tmpDir, "thumbs"), settingsProvider{settings: settings}, log)

	var probeTotal, thumbTotal time.Duration
	var probeBytes int64
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			continue
		}
		started := time.Now()
		result, err := media.ProbeFile(file, info.Size(), info.ModTime(), media.Options{})
		probeElapsed := time.Since(started)
		if err != nil {
			continue
		}
		probeTotal += probeElapsed
		if media.IsRaw(file) {
			probeBytes += 4 << 20
		} else {
			probeBytes += 256 << 10
		}
		report.ByExtension[media.Extension(file)]++
		report.Sampled++

		entry := store.Entry{
			Path: result.Path, ID: result.ID, Name: filepath.Base(result.Path), Kind: result.Kind,
			Size: result.Size, Modified: result.Modified.Unix(),
		}
		if result.RawPreview != nil {
			entry.RawPreviewOffset = result.RawPreview.Offset
			entry.RawPreviewLength = result.RawPreview.Length
		}
		thumbStarted := time.Now()
		if _, err := thumbMgr.Ensure(entry, settings.ThumbSize); err != nil {
			report.ThumbFailures++
		} else {
			thumbTotal += time.Since(thumbStarted)
			report.ThumbnailsEnabled = true
		}
	}

	if report.Sampled > 0 {
		report.ProbeMSAvg = float64(probeTotal.Microseconds()) / 1000 / float64(report.Sampled)
		report.ProbeBytesAvg = probeBytes / int64(report.Sampled)
		successful := report.Sampled - report.ThumbFailures
		if successful > 0 {
			report.ThumbMSAvg = float64(thumbTotal.Microseconds()) / 1000 / float64(successful)
		}
		report.ETAProbe10kMin = report.ProbeMSAvg * 10000 / 1000 / 60
		report.ETAProbe100kMin = report.ProbeMSAvg * 100000 / 1000 / 60
		report.ETAThumb10kMin = report.ThumbMSAvg * 10000 / 1000 / 60
		report.ETAThumb100kMin = report.ThumbMSAvg * 100000 / 1000 / 60
	}

	if !report.Generator["vipsthumbnail"] {
		report.Notes = append(report.Notes, "no external thumbnail generator found; pure-Go fallback is slower (install libvips in production)")
	}
	if report.ThumbFailures > 0 {
		report.Notes = append(report.Notes, fmt.Sprintf("%d thumbnails failed (unsupported formats are expected for RAW without previews)", report.ThumbFailures))
	}
	return report, nil
}

// JSON renders the report as indented JSON.
func (r Report) JSON() string {
	data, _ := json.MarshalIndent(r, "", "  ")
	return string(data)
}

// Text renders the report for humans.
func (r Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Itemory NAS Agent benchmark\n")
	fmt.Fprintf(&b, "  directory : %s\n", r.Dir)
	fmt.Fprintf(&b, "  sampled   : %d media files %v\n", r.Sampled, r.ByExtension)
	fmt.Fprintf(&b, "  probe     : %.2f ms/file (≈%d KB read/file)\n", r.ProbeMSAvg, r.ProbeBytesAvg/1024)
	if r.ThumbMSAvg > 0 {
		fmt.Fprintf(&b, "  thumbnail : %.2f ms/file (512px)\n", r.ThumbMSAvg)
	}
	if r.ThumbFailures > 0 {
		fmt.Fprintf(&b, "  thumb fail: %d\n", r.ThumbFailures)
	}
	fmt.Fprintf(&b, "  ETA probe : 10k ≈ %.1f min · 100k ≈ %.1f min\n", r.ETAProbe10kMin, r.ETAProbe100kMin)
	fmt.Fprintf(&b, "  ETA thumb : 10k ≈ %.1f min · 100k ≈ %.1f min\n", r.ETAThumb10kMin, r.ETAThumb100kMin)
	fmt.Fprintf(&b, "  tools     : %v\n", r.Generator)
	for _, note := range r.Notes {
		fmt.Fprintf(&b, "  note      : %s\n", note)
	}
	return b.String()
}

func toolAvailable(name string) bool {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}
