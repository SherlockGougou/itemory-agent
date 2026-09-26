// Package logging provides a JSON logger plus an in-memory ring buffer that the
// app can read back through /api/v1/logs.
package logging

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// Logger is a thin wrapper so the rest of the code depends on one type.
type Logger struct {
	*slog.Logger
	// level 是可变的：设置页把 logLevel 从 info 调到 debug 后应立即生效，
	// 而不是等到下次重启。slog.LevelVar 是并发安全的，随设置热切换。
	level *slog.LevelVar
}

// SetLevel applies a new minimum level at runtime. 未知取值按 info 处理，
// 与 config.Validate 对 logLevel 的归一化保持一致。
func (l *Logger) SetLevel(level string) {
	if l == nil || l.level == nil {
		return
	}
	l.level.Set(parseLevel(level))
}

// Ring keeps the last N log lines in memory.
type Ring struct {
	mu    sync.Mutex
	lines []string
	max   int
	rest  string
}

// NewRing creates a ring buffer holding up to max lines.
func NewRing(max int) *Ring {
	return &Ring{max: max}
}

// Write implements io.Writer; JSON logs are line-delimited.
func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	text := r.rest + string(p)
	parts := strings.Split(text, "\n")
	r.rest = parts[len(parts)-1]
	for _, line := range parts[:len(parts)-1] {
		if line == "" {
			continue
		}
		r.lines = append(r.lines, line)
		if len(r.lines) > r.max {
			r.lines = r.lines[len(r.lines)-r.max:]
		}
	}
	return len(p), nil
}

// Tail returns the most recent n lines (oldest first).
func (r *Ring) Tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || n > len(r.lines) {
		n = len(r.lines)
	}
	out := make([]string, n)
	copy(out, r.lines[len(r.lines)-n:])
	return out
}

// TailFiltered returns the most recent n lines matching a level and a query.
//
// minLevel 为空表示不限级别；query 为空表示不限关键字（大小写不敏感的子串匹配）。
// 解析不出级别的行（非 slog JSON）一律保留——静默吞掉日志比重显示几行更危险。
func (r *Ring) TailFiltered(minLevel, query string, n int) []string {
	r.mu.Lock()
	lines := make([]string, len(r.lines))
	copy(lines, r.lines)
	r.mu.Unlock()

	threshold := levelRank(minLevel)
	needle := strings.ToLower(strings.TrimSpace(query))
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if needle != "" && !strings.Contains(strings.ToLower(line), needle) {
			continue
		}
		if threshold >= 0 {
			if rank := levelRank(jsonLevel(line)); rank >= 0 && rank < threshold {
				continue
			}
		}
		out = append(out, line)
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// jsonLevel reads slog's "level" field; empty when the line is not JSON.
func jsonLevel(line string) string {
	var payload struct {
		Level string `json:"level"`
	}
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return ""
	}
	return payload.Level
}

// levelRank orders the levels config.Validate accepts. 空串与无法识别的取值都返回 -1。
func levelRank(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return 0
	case "info":
		return 1
	case "warn", "warning":
		return 2
	case "error":
		return 3
	default:
		return -1
	}
}

// New builds a JSON logger writing to stdout and (optionally) the ring buffer.
func New(level string, ring *Ring) *Logger {
	lv := &slog.LevelVar{}
	lv.Set(parseLevel(level))
	var w io.Writer = os.Stdout
	if ring != nil {
		w = io.MultiWriter(os.Stdout, ring)
	}
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lv})
	return &Logger{Logger: slog.New(handler), level: lv}
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
