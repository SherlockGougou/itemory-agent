package api

import (
	"net/http"
	"time"

	"github.com/SherlockGougou/itemory-agent/internal/config"
	"github.com/SherlockGougou/itemory-agent/internal/scan"
	"github.com/SherlockGougou/itemory-agent/internal/volumes"
)

// handleOverview 是控制台管理员视图背后的聚合接口。
//
// 与公开的 handleDashboard 刻意分开：dashboard 是所有 LAN 访问者都能读的最小集，
// 而这里带设备列表、完整媒体库路径与诊断性字段。把管理面塞进匿名接口，等于把
// 整个局域网都当成管理员。两者共用 snapshot()，所以取数逻辑只有一份。
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	// 诊断页的「重新检测」用 refresh=1 绕过挂载探测缓存。
	refresh := r.URL.Query().Get("refresh") == "1"
	snap := s.snapshot(refresh)
	settings := snap.settings
	scheme, host := requestAuthority(r)

	tokens := s.d.Tokens.List()
	devices := make([]map[string]any, 0, len(tokens))
	for _, token := range tokens {
		devices = append(devices, map[string]any{
			"id":        token.ID,
			"name":      token.Name,
			"platform":  token.Platform,
			"createdAt": token.CreatedAt,
			"lastSeen":  token.LastSeen,
		})
	}

	pairing := map[string]any{
		"armed":         snap.armed,
		"pairedDevices": len(tokens),
		"windowSeconds": int(ClaimTTL.Seconds()),
		"codeLength":    6,
	}
	if snap.armed {
		pairing["expiresAt"] = snap.expires.UTC()
		pairing["expiresInSeconds"] = int(time.Until(snap.expires).Seconds())
	}

	tools := map[string]bool{
		"vipsthumbnail": toolAvailable("vipsthumbnail"),
		"ffmpeg":        toolAvailable("ffmpeg"),
	}

	byKind := snap.stats.ByKind
	if byKind == nil {
		byKind = map[string]int{}
	}

	progress := s.d.Scanner.Status()

	writeJSON(w, http.StatusOK, map[string]any{
		"service": map[string]any{
			"name":          "itemory-agent",
			"version":       s.d.Version,
			"apiVersion":    s.d.APIVersion,
			"serverId":      s.d.Tokens.ServerID(),
			"uptimeSeconds": int(time.Since(s.d.StartedAt).Seconds()),
			"generatedAt":   time.Now().UTC(),
		},
		"health":    s.evaluateHealth(snap, tools, progress, len(tokens)),
		"pairing":   pairing,
		"devices":   devices,
		"libraries": libraryDTOs(settings),
		"index": map[string]any{
			"entries":     snap.stats.Entries,
			"removed":     snap.stats.Removed,
			"byKind":      byKind,
			"lastScanAt":  s.d.Index.LastScanAt(),
			"lastScanGen": snap.stats.LastScanGen,
		},
		"scan":     progress,
		"schedule": scanScheduleDTO(settings.ScanSchedule),
		"cache": map[string]any{
			"thumbFiles":      snap.thumbFiles,
			"thumbBytes":      snap.thumbBytes,
			"thumbLimitBytes": settings.ThumbCacheLimitBytes,
		},
		"tools":         tools,
		"volumes":       snap.volumes,
		"suggestedUser": volumes.SuggestedUser(snap.volumes),
		"hosts": map[string]any{
			"scheme":     scheme,
			"host":       host,
			"port":       requestPort(r),
			"loopback":   hostIsLoopback(r),
			"candidates": hostCandidates(r),
		},
		"settings": settings,
		"logs":     s.dashboardLogs(),
	})
}

// scanScheduleDTO 把「每日 HH:MM」翻译成控制台要显示的两件事：原文 + 下一次触发时间。
// 下一次由 config.NextScanAt 从计划字符串推导，不去读调度器的内部状态——那是
// goroutine 局部的，外部拿不到。
func scanScheduleDTO(schedule string) map[string]any {
	out := map[string]any{"daily": schedule}
	if next, ok := config.NextScanAt(schedule, time.Now()); ok {
		out["nextRunAt"] = next
		out["nextRunInSeconds"] = int(time.Until(next).Seconds())
	}
	return out
}

// healthCheck 是一项健康判定。
//
// Count / Text 只装**纯数据**，不拼中文：控制台按 ID 取本地化标签、自己组装句子，
// 否则这句判定就写死了语言。Reason 是机器可读的短码（unreadable / unset / never …）。
type healthCheck struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Count  int    `json:"count,omitempty"`
	Text   string `json:"text,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// evaluateHealth 全部由已有字段推导，不需要新增后端能力——
// 控制台的「健康环」因此不是一个新接口，只是同一份 snapshot 的另一种视图。
func (s *Server) evaluateHealth(snap snapshot, tools map[string]bool, progress scan.Progress, paired int) map[string]any {
	libraries := libraryDTOs(snap.settings)
	unreadable := 0
	for _, lib := range libraries {
		if ok, _ := lib["exists"].(bool); !ok {
			unreadable++
		}
	}
	toolsAvailable := 0
	for _, ok := range tools {
		if ok {
			toolsAvailable++
		}
	}
	readableVolumes := 0
	for _, v := range snap.volumes {
		if v.Readable {
			readableVolumes++
		}
	}
	overLimit := snap.settings.ThumbCacheLimitBytes > 0 && snap.thumbBytes > snap.settings.ThumbCacheLimitBytes
	lastScan := lastScanCheck(s.d.Index.LastScanAt(), progress)

	checks := []healthCheck{
		{ID: "service", OK: true, Text: s.d.Version},
		{
			ID:     "libraries",
			OK:     len(libraries) > 0 && unreadable == 0,
			Count:  len(libraries),
			Reason: libraryReason(len(libraries), unreadable),
		},
		{
			ID:     "volumes",
			OK:     readableVolumes > 0,
			Count:  readableVolumes,
			Reason: reasonIf(readableVolumes == 0, "none"),
		},
		{
			ID:     "index",
			OK:     snap.stats.Entries > 0,
			Count:  snap.stats.Entries,
			Reason: reasonIf(snap.stats.Entries == 0, "empty"),
		},
		{
			ID:     "cache",
			OK:     !overLimit,
			Count:  snap.thumbFiles,
			Reason: reasonIf(overLimit, "over"),
		},
		{
			ID:     "tools",
			OK:     toolsAvailable > 0,
			Count:  toolsAvailable,
			Reason: reasonIf(toolsAvailable == 0, "none"),
		},
		{
			ID:     "schedule",
			OK:     snap.settings.ScanSchedule != "",
			Text:   snap.settings.ScanSchedule,
			Reason: reasonIf(snap.settings.ScanSchedule == "", "unset"),
		},
		{
			ID:     "pairing",
			OK:     paired > 0,
			Count:  paired,
			Reason: reasonIf(paired == 0, "none"),
		},
		lastScan,
	}

	passed := 0
	for _, c := range checks {
		if c.OK {
			passed++
		}
	}
	return map[string]any{
		"checks": checks,
		"passed": passed,
		"total":  len(checks),
	}
}

// lastScanCheck 判定「这台服务是否已经成功扫描过」。
//
// 依据是**持久化**的 index.LastScanAt，而不是本进程内存里的 Scanner.Progress：
// 容器一重启 Progress 就归零，若据此判定，控制台每重启一次就会报
// 「最近一次扫描：从未运行」——可索引明明好好地在磁盘上。这条假告警正是
// 右上角那个「1 项待处理」的来源（2026-09-21 实测）。
//
// 只有在「本进程确实跑过一轮、且以中止或报错收尾」时才降级——那才意味着
// 索引可能不完整、值得用户处理。
func lastScanCheck(lastScanAt int64, progress scan.Progress) healthCheck {
	switch {
	case progress.Running:
		// 正在扫不是问题，是正常状态。
		return healthCheck{ID: "lastScan", OK: true, Reason: "running"}
	case lastScanAt <= 0:
		return healthCheck{ID: "lastScan", OK: false, Reason: "never"}
	case progress.Cancelled:
		return healthCheck{ID: "lastScan", OK: false, Reason: "cancelled"}
	case progress.Errors > 0 && !progress.StartedAt.IsZero():
		return healthCheck{ID: "lastScan", OK: false, Reason: "errors"}
	case progress.Message != "" && progress.Message != "scan finished" && !progress.StartedAt.IsZero():
		return healthCheck{ID: "lastScan", OK: false, Reason: "incomplete"}
	default:
		return healthCheck{ID: "lastScan", OK: true}
	}
}

func libraryReason(configured, unreadable int) string {
	switch {
	case configured == 0:
		return "unset"
	case unreadable > 0:
		return "unreadable"
	default:
		return ""
	}
}

func reasonIf(cond bool, reason string) string {
	if cond {
		return reason
	}
	return ""
}
