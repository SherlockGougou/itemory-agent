package api

import (
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/SherlockGougou/itemory-agent/internal/config"
	"github.com/SherlockGougou/itemory-agent/internal/store"
	"github.com/SherlockGougou/itemory-agent/internal/volumes"
)

// logTailLines is how many recent log lines the public dashboard shows.
const logTailLines = 40

// handleDashboard serves the read-only aggregate behind the web dashboard.
//
// Everything here is intentionally public (LAN-trust): it must never include a
// device token, its hash, or the pairing code.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot(false)
	settings := snap.settings
	stats := snap.stats
	thumbFiles, thumbBytes := snap.thumbFiles, snap.thumbBytes
	found := snap.volumes
	armed, expires := snap.armed, snap.expires
	scheme, host := requestAuthority(r)

	libraries := libraryDTOs(settings)

	byKind := stats.ByKind
	if byKind == nil {
		byKind = map[string]int{}
	}

	pairing := map[string]any{
		"armed":         armed,
		"pairedDevices": s.d.Tokens.Count(),
		"windowSeconds": int(ClaimTTL.Seconds()),
		"codeLength":    6,
	}
	if armed {
		pairing["expiresAt"] = expires.UTC()
		pairing["expiresInSeconds"] = int(time.Until(expires).Seconds())
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"service": map[string]any{
			"name":          "itemory-agent",
			"version":       s.d.Version,
			"apiVersion":    s.d.APIVersion,
			"serverId":      s.d.Tokens.ServerID(),
			"uptimeSeconds": int(time.Since(s.d.StartedAt).Seconds()),
			"healthy":       true,
			"generatedAt":   time.Now().UTC(),
		},
		"pairing":   pairing,
		"libraries": libraries,
		"index": map[string]any{
			"entries":     stats.Entries,
			"removed":     stats.Removed,
			"byKind":      byKind,
			"lastScanAt":  s.d.Index.LastScanAt(),
			"lastScanGen": stats.LastScanGen,
		},
		"scan": s.d.Scanner.Status(),
		"cache": map[string]any{
			"thumbFiles":      thumbFiles,
			"thumbBytes":      thumbBytes,
			"thumbLimitBytes": settings.ThumbCacheLimitBytes,
		},
		"tools": map[string]any{
			"vipsthumbnail": toolAvailable("vipsthumbnail"),
			"ffmpeg":        toolAvailable("ffmpeg"),
		},
		"volumes":       found,
		"suggestedUser": volumes.SuggestedUser(found),
		"settings": map[string]any{
			"preset":               settings.Preset,
			"thumbSize":            settings.ThumbSize,
			"thumbCacheLimitBytes": settings.ThumbCacheLimitBytes,
			"scanSchedule":         settings.ScanSchedule,
			"logLevel":             settings.LogLevel,
			"transcode":            settings.Transcode,
			"motionPhoto":          settings.MotionPhoto,
			"rawPreview":           settings.RawPreview,
		},
		"hosts": map[string]any{
			"scheme":     scheme,
			"host":       host,
			"port":       requestPort(r),
			"loopback":   hostIsLoopback(r),
			"candidates": hostCandidates(r),
		},
		"logs": s.dashboardLogs(),
	})
}

// snapshot 汇总一次聚合请求需要的全部取数。公开的 dashboard 与需要管理员会话的
// admin overview 共用它，避免同一份聚合逻辑写两遍之后互相漂移。
type snapshot struct {
	settings   config.Settings
	stats      store.Stats
	thumbFiles int
	thumbBytes int64
	volumes    []volumes.Volume
	armed      bool
	expires    time.Time
}

func (s *Server) snapshot(refreshVolumes bool) snapshot {
	settings := s.d.Settings.Get()
	stats, _ := s.d.Index.Stats()
	thumbFiles, thumbBytes := s.d.Thumbs.Stats()
	armed, expires := s.d.Tokens.ClaimState()
	return snapshot{
		settings:   settings,
		stats:      stats,
		thumbFiles: thumbFiles,
		thumbBytes: thumbBytes,
		volumes:    s.discoverVolumes(settings.Libraries, refreshVolumes),
		armed:      armed,
		expires:    expires,
	}
}

// libraryDTOs 逐个 os.Stat 判断可读性。这一步刻意**不进**缓存：
// 「库还在不在」是管理员最需要看到实时值的字段，而 os.Stat 只是一次系统调用。
func libraryDTOs(settings config.Settings) []map[string]any {
	out := make([]map[string]any, 0, len(settings.Libraries))
	for _, library := range settings.Libraries {
		_, err := os.Stat(library.Path)
		out = append(out, map[string]any{
			"id":     library.ID,
			"name":   library.Name,
			"path":   library.Path,
			"exists": err == nil,
		})
	}
	return out
}

// volumesCache 缓存挂载探测结果。
//
// volumes.Discover 会对 /volumes /volume1 /vol1 /vol2 /mnt /srv /share /media /data
// 逐个 ReadDir，并对每个子目录再 ReadDir 一次来找媒体目录——是全套聚合里最重的一步，
// 而它描述的是「挂载点有没有、可不可读」这种分钟级变化的事实。诊断页用 ?refresh=1 强制重算。
type volumesCache struct {
	mu    sync.Mutex
	at    time.Time
	items []volumes.Volume
}

// volumesTTL 取得比缩略图统计更长：挂载点变化比缩略图增减慢得多。
const volumesTTL = 15 * time.Second

func (s *Server) discoverVolumes(libraries []config.Library, refresh bool) []volumes.Volume {
	s.volCache.mu.Lock()
	if !refresh && !s.volCache.at.IsZero() && time.Since(s.volCache.at) < volumesTTL {
		items := s.volCache.items
		s.volCache.mu.Unlock()
		return items
	}
	s.volCache.mu.Unlock()

	found := volumes.Discover(libraries)

	s.volCache.mu.Lock()
	s.volCache.items, s.volCache.at = found, time.Now()
	s.volCache.mu.Unlock()
	return found
}

// dashboardLogs returns the recent log tail with pairing codes masked, since the
// dashboard (and therefore its log card) is readable by anyone on the LAN.
func (s *Server) dashboardLogs() []string {
	if s.d.LogRing == nil {
		return []string{}
	}
	lines := s.d.LogRing.Tail(logTailLines)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, redactSecret(line))
	}
	return out
}

// redactSecret masks `<key>=<value>` pairs for sensitive keys.
func redactSecret(line string) string {
	for _, key := range []string{"code", "token", "secret", "password"} {
		needle := key + "="
		search := 0
		for {
			index := strings.Index(line[search:], needle)
			if index < 0 {
				break
			}
			index += search
			start := index + len(needle)
			end := start
			for end < len(line) && line[end] != ' ' && line[end] != '"' {
				end++
			}
			if end == start {
				search = start
				continue
			}
			line = line[:start] + "***" + line[end:]
			search = start + len("***")
		}
	}
	return line
}
