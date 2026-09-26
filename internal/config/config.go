// Package config manages the agent's settings file (hot-configurable from the app).
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SchemaVersion is bumped when the settings file layout changes.
const SchemaVersion = 1

// Library is one user-selected photo/video root (inside a mounted volume).
type Library struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// Settings is the full configuration surface; every field is hot-applicable.
type Settings struct {
	SchemaVersion           int       `json:"schemaVersion"`
	Preset                  string    `json:"preset"`
	Libraries               []Library `json:"libraries"`
	ExcludePatterns         []string  `json:"excludePatterns"`
	Concurrency             int       `json:"concurrency"`
	ThumbSize               int       `json:"thumbSize"`
	ThumbCacheLimitBytes    int64     `json:"thumbCacheLimitBytes"`
	OriginalCacheLimitBytes int64     `json:"originalCacheLimitBytes"`
	NightlyThumbBudget      int       `json:"nightlyThumbBudget"`
	Transcode               string    `json:"transcode"`
	MotionPhoto             bool      `json:"motionPhoto"`
	RawPreview              bool      `json:"rawPreview"`
	ScanSchedule            string    `json:"scanSchedule"`
	LogLevel                string    `json:"logLevel"`
	Telemetry               bool      `json:"telemetry"`
}

// Defaults returns the "balanced" preset.
func Defaults() Settings {
	s := Settings{
		SchemaVersion:   SchemaVersion,
		Preset:          "balanced",
		ExcludePatterns: []string{"@eaDir", "#recycle", "#snapshot", ".Trash", ".Trash-1000", ".thumbnails", "Thumbs.db", "$RECYCLE.BIN", "lost+found", ".itemory"},
		Transcode:       "off",
		MotionPhoto:     true,
		RawPreview:      true,
		ScanSchedule:    "03:00",
		LogLevel:        "info",
		Telemetry:       false,
		Libraries:       []Library{},
	}
	ApplyPreset(&s, "balanced")
	return s
}

// ApplyPreset overwrites resource-related fields with a named profile.
func ApplyPreset(s *Settings, preset string) {
	switch strings.ToLower(preset) {
	case "light", "low":
		s.Preset = "light"
		s.Concurrency = 1
		s.ThumbSize = 256
		s.ThumbCacheLimitBytes = 1 << 30
		s.OriginalCacheLimitBytes = 512 << 20
		s.NightlyThumbBudget = 2000
	case "performance", "perf":
		s.Preset = "performance"
		s.Concurrency = 4
		s.ThumbSize = 512
		s.ThumbCacheLimitBytes = 10 << 30
		s.OriginalCacheLimitBytes = 2 << 30
		s.NightlyThumbBudget = 20000
	default:
		s.Preset = "balanced"
		s.Concurrency = 2
		s.ThumbSize = 512
		s.ThumbCacheLimitBytes = 5 << 30
		s.OriginalCacheLimitBytes = 1 << 30
		s.NightlyThumbBudget = 5000
	}
}

// Validate normalizes and clamps a settings value; it returns an error only for
// unrecoverable problems (e.g. duplicate library ids).
func Validate(s Settings) (Settings, error) {
	if s.SchemaVersion == 0 {
		s.SchemaVersion = SchemaVersion
	}
	if s.Preset == "" {
		s.Preset = "balanced"
	}
	if s.Concurrency < 1 {
		s.Concurrency = 1
	}
	if s.Concurrency > 8 {
		s.Concurrency = 8
	}
	if s.ThumbSize < 128 {
		s.ThumbSize = 128
	}
	if s.ThumbSize > 2048 {
		s.ThumbSize = 2048
	}
	if s.ThumbCacheLimitBytes < 64<<20 {
		s.ThumbCacheLimitBytes = 64 << 20
	}
	if s.OriginalCacheLimitBytes < 0 {
		s.OriginalCacheLimitBytes = 0
	}
	if s.NightlyThumbBudget < 0 {
		s.NightlyThumbBudget = 0
	}
	switch s.Transcode {
	case "off", "onDemand", "all":
	default:
		s.Transcode = "off"
	}
	switch s.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		s.LogLevel = "info"
	}
	if s.ScanSchedule != "" {
		if _, err := parseHHMM(s.ScanSchedule); err != nil {
			s.ScanSchedule = ""
		}
	}
	seen := map[string]bool{}
	seenPaths := map[string]bool{}
	cleaned := make([]Library, 0, len(s.Libraries))
	for _, lib := range s.Libraries {
		lib.Path = strings.TrimSpace(lib.Path)
		if lib.Path == "" {
			continue
		}
		lib.Path = filepath.Clean(lib.Path)
		// The same folder configured twice is one library, not an error.
		if seenPaths[lib.Path] {
			continue
		}
		seenPaths[lib.Path] = true
		if lib.ID == "" {
			lib.ID = "lib-" + shortHash(lib.Path)
		}
		if seen[lib.ID] {
			return s, fmt.Errorf("duplicate library id %q", lib.ID)
		}
		seen[lib.ID] = true
		if lib.Name == "" {
			lib.Name = filepath.Base(lib.Path)
		}
		cleaned = append(cleaned, lib)
	}
	s.Libraries = cleaned
	if s.ExcludePatterns == nil {
		s.ExcludePatterns = []string{}
	}
	return s, nil
}

// Manager owns the settings file and notifies subscribers on change.
type Manager struct {
	mu   sync.RWMutex
	path string
	s    Settings
	subs []func(Settings)
}

// Load reads settings.json (creating it with defaults on first run).
// ITEMORY_PRESET applies only when the file does not exist yet.
func Load(dir string) (*Manager, error) {
	path := filepath.Join(dir, "settings.json")
	m := &Manager{path: path}

	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		s := Defaults()
		if preset := os.Getenv("ITEMORY_PRESET"); preset != "" {
			ApplyPreset(&s, preset)
		}
		if err := m.persist(s); err != nil {
			return nil, err
		}
		m.s = s
		return m, nil
	}

	var s Settings
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("settings.json is not valid JSON: %w", err)
	}
	s, err = Validate(s)
	if err != nil {
		return nil, err
	}
	m.s = s
	return m, nil
}

// Get returns a copy of the current settings.
//
// 两个切片用 make + append 而不是 append([]T(nil), ...)：后者在源切片为空时
// 返回 nil，JSON 会序列化成 null 而不是 []。控制台把这份设置直接当 JSON 读，
// null 与 [] 的差别会一路传到前端（`settings.libraries.length` 直接抛错）。
func (m *Manager) Get() Settings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := m.s
	out.Libraries = append(make([]Library, 0, len(m.s.Libraries)), m.s.Libraries...)
	out.ExcludePatterns = append(make([]string, 0, len(m.s.ExcludePatterns)), m.s.ExcludePatterns...)
	return out
}

// Update validates, persists and publishes new settings.
func (m *Manager) Update(next Settings) error {
	next, err := Validate(next)
	if err != nil {
		return err
	}
	if err := m.persist(next); err != nil {
		return err
	}
	m.mu.Lock()
	m.s = next
	subs := make([]func(Settings), len(m.subs))
	copy(subs, m.subs)
	m.mu.Unlock()
	for _, fn := range subs {
		fn(next)
	}
	return nil
}

// Subscribe registers a callback invoked after every successful update.
func (m *Manager) Subscribe(fn func(Settings)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subs = append(m.subs, fn)
}

func (m *Manager) persist(s Settings) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

func parseHHMM(v string) (int, error) {
	var h, min int
	if _, err := fmt.Sscanf(v, "%d:%d", &h, &min); err != nil {
		return 0, err
	}
	if h < 0 || h > 23 || min < 0 || min > 59 {
		return 0, fmt.Errorf("invalid time %q", v)
	}
	return h*60 + min, nil
}

// NextScanAt returns the next occurrence of the daily schedule strictly after now.
//
// 调度器每天只触发一次（见 cmd/itemory-agent 的 runScheduler），所以「今天这一刻已经
// 过去」就等于「下一次在明天」。控制台据此显示「下次计划扫描」，
// 不需要去读调度器的内部状态——那个状态是 goroutine 局部的。
func NextScanAt(schedule string, now time.Time) (time.Time, bool) {
	minutes, err := parseHHMM(schedule)
	if err != nil {
		return time.Time{}, false
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), minutes/60, minutes%60, 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next, true
}

func shortHash(v string) string {
	var h uint32 = 2166136261
	for i := 0; i < len(v); i++ {
		h ^= uint32(v[i])
		h *= 16777619
	}
	const digits = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, 6)
	for i := range out {
		out[i] = digits[h%uint32(len(digits))]
		h /= uint32(len(digits))
	}
	return string(out)
}
