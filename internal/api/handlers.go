package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SherlockGougou/itemory-agent/internal/config"
	"github.com/SherlockGougou/itemory-agent/internal/media"
	"github.com/SherlockGougou/itemory-agent/internal/store"
	"github.com/SherlockGougou/itemory-agent/internal/volumes"
)

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.d.Settings.Get())
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var next config.Settings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&next); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid settings JSON")
		return
	}
	// 预设只在真正切换时套用：App 每次都会整份回传当前设置，
	// 若每次都套用预设，用户在预设之上做的单项微调会被反复覆盖。
	current := s.d.Settings.Get()
	if next.Preset != "" && next.Preset != current.Preset {
		config.ApplyPreset(&next, next.Preset)
	}
	if err := s.d.Settings.Update(next); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_settings", err.Error())
		return
	}
	s.d.Logger.Info("settings updated", "libraries", len(next.Libraries), "thumbSize", next.ThumbSize)
	s.d.Hub.Broadcast("settings/changed", next)
	// 媒体库或排除规则变了：App 设置页承诺「保存后立即生效」，这里马上跑一轮增量扫描，
	// 新库的照片不必等到夜间计划扫描，移除的库与新排除的目录也会随之退出时间线。
	if scopeChanged(current, s.d.Settings.Get()) {
		if !s.startScan("incremental") {
			s.d.Logger.Info("scan already running; library change applies on the next scan")
		}
	}
	// 调低缓存上限后立即回收，不必等到下一次生成缩略图才生效。
	go func() {
		if err := s.d.Thumbs.Evict(); err != nil {
			s.d.Logger.Warn("thumb eviction failed", "error", err)
		}
	}()
	writeJSON(w, http.StatusOK, s.d.Settings.Get())
}

// patchableSettings 是控制台允许改写的字段白名单。指针类型用来区分
// 「字段没传」与「传了零值」——false 与 0 都是有意义的目标值。
//
// 为什么不让网页端直接 PUT 整份设置：Update() 是**整份替换**，而网页端拿不到
// libraries / excludePatterns（那些只由 App 维护），回传时就会把它们抹成空。
// 这是数据丢失级的风险，所以网页端只碰这几个字段，其余一律保持原值。
type patchableSettings struct {
	Preset               *string `json:"preset"`
	ThumbSize            *int    `json:"thumbSize"`
	ThumbCacheLimitBytes *int64  `json:"thumbCacheLimitBytes"`
	ScanSchedule         *string `json:"scanSchedule"`
	LogLevel             *string `json:"logLevel"`
	Transcode            *string `json:"transcode"`
	MotionPhoto          *bool   `json:"motionPhoto"`
	RawPreview           *bool   `json:"rawPreview"`
}

// handlePatchSettings 局部更新设置。
//
// 语义：以当前设置为基线，只覆盖请求里出现过的白名单字段。
// 预设的套用规则与 PUT 一致——只在 preset 真正变化时 ApplyPreset，
// 因此用户在预设之上做的单项微调不会被反复覆盖。
func (s *Server) handlePatchSettings(w http.ResponseWriter, r *http.Request) {
	var patch patchableSettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid settings JSON")
		return
	}

	next := s.d.Settings.Get() // 从当前值出发：没提到的字段原样保留
	previousPreset := next.Preset

	if patch.Preset != nil {
		next.Preset = *patch.Preset
	}
	if patch.ThumbSize != nil {
		next.ThumbSize = *patch.ThumbSize
	}
	if patch.ThumbCacheLimitBytes != nil {
		next.ThumbCacheLimitBytes = *patch.ThumbCacheLimitBytes
	}
	if patch.ScanSchedule != nil {
		next.ScanSchedule = *patch.ScanSchedule
	}
	if patch.LogLevel != nil {
		next.LogLevel = *patch.LogLevel
	}
	if patch.Transcode != nil {
		next.Transcode = *patch.Transcode
	}
	if patch.MotionPhoto != nil {
		next.MotionPhoto = *patch.MotionPhoto
	}
	if patch.RawPreview != nil {
		next.RawPreview = *patch.RawPreview
	}

	if next.Preset != "" && next.Preset != previousPreset {
		config.ApplyPreset(&next, next.Preset)
	}

	if err := s.d.Settings.Update(next); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_settings", err.Error())
		return
	}
	s.d.Logger.Info("settings patched", "preset", next.Preset, "thumbSize", next.ThumbSize)
	s.d.Hub.Broadcast("settings/changed", s.d.Settings.Get())
	// 调低缓存上限后立即回收，不必等到下一次生成缩略图才生效。
	go func() {
		if err := s.d.Thumbs.Evict(); err != nil {
			s.d.Logger.Warn("thumb eviction failed", "error", err)
		}
	}()
	writeJSON(w, http.StatusOK, s.d.Settings.Get())
}

// handleClearThumbCache drops every cached thumbnail; the index and user data are
// untouched and thumbnails are regenerated on demand.
func (s *Server) handleClearThumbCache(w http.ResponseWriter, r *http.Request) {
	if err := s.d.Thumbs.Clear(); err != nil {
		writeError(w, http.StatusInternalServerError, "clear_failed", err.Error())
		return
	}
	s.d.Logger.Info("thumb cache cleared")
	s.d.Hub.Broadcast("cache/cleared", map[string]any{"scope": "thumbs"})
	files, bytes := s.d.Thumbs.Stats()
	writeJSON(w, http.StatusOK, map[string]any{"cleared": true, "thumbFiles": files, "thumbBytes": bytes})
}

func (s *Server) handleLibraries(w http.ResponseWriter, r *http.Request) {
	settings := s.d.Settings.Get()
	type libraryDTO struct {
		config.Library
		Exists bool `json:"exists"`
	}
	out := make([]libraryDTO, 0, len(settings.Libraries))
	for _, lib := range settings.Libraries {
		_, err := os.Stat(lib.Path)
		out = append(out, libraryDTO{Library: lib, Exists: err == nil})
	}
	writeJSON(w, http.StatusOK, map[string]any{"libraries": out})
}

func (s *Server) handleFolders(w http.ResponseWriter, r *http.Request) {
	settings := s.d.Settings.Get()
	requested := strings.TrimSpace(r.URL.Query().Get("path"))

	type folderDTO struct {
		Name       string `json:"name"`
		Path       string `json:"path"`
		MediaCount int    `json:"mediaCount"`
		IsLibrary  bool   `json:"isLibrary,omitempty"`
	}

	if requested == "" {
		out := make([]folderDTO, 0, len(settings.Libraries))
		for _, lib := range settings.Libraries {
			out = append(out, folderDTO{
				Name:       lib.Name,
				Path:       lib.Path,
				MediaCount: s.countMedia(lib.Path),
				IsLibrary:  true,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"path": "", "folders": out})
		return
	}

	path := filepath.Clean(requested)
	if !withinLibraries(path, settings.Libraries) {
		writeError(w, http.StatusForbidden, "outside_library", "path is outside the configured libraries")
		return
	}
	// 再过一次「解析符号链接之后」的边界：库内可能有指向库外的链接，
	// filepath.Clean 只做字符串归一，会把目录列举带出库范围。
	if !withinResolvedLibraries(path, settings.Libraries) {
		writeError(w, http.StatusForbidden, "outside_library", "path resolves outside the configured libraries")
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "unreadable", err.Error())
		return
	}
	out := make([]folderDTO, 0, len(entries))
	for _, ent := range entries {
		if !ent.IsDir() || strings.HasPrefix(ent.Name(), ".") {
			continue
		}
		child := filepath.Join(path, ent.Name())
		out = append(out, folderDTO{Name: ent.Name(), Path: child, MediaCount: s.countMedia(child)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{
		"path":       path,
		"mediaCount": s.countMedia(path),
		"folders":    out,
	})
}

func (s *Server) handleVolumes(w http.ResponseWriter, r *http.Request) {
	settings := s.d.Settings.Get()
	found := volumes.Discover(settings.Libraries, s.d.DataDir)
	writeJSON(w, http.StatusOK, map[string]any{
		"volumes":       found,
		"suggestedUser": volumes.SuggestedUser(found),
	})
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req)
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode != "full" {
		mode = "incremental"
	}
	if !s.startScan(mode) {
		writeError(w, http.StatusConflict, "already_running", "a scan is already running")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"started":  true,
		"mode":     mode,
		"progress": s.d.Scanner.Status(),
	})
}

// startScan 同步占坑并公布 running，再进 goroutine：否则客户端 POST 后立刻 reload
// 会看到 idle，以为「全量扫描」没点上。已有扫描在跑时返回 false。
func (s *Server) startScan(mode string) bool {
	ctx, cancel, err := s.d.Scanner.TryBegin(mode)
	if err != nil {
		return false
	}
	go func() {
		if err := s.d.Scanner.Run(ctx, cancel, mode); err != nil {
			s.d.Logger.Warn("scan failed", "error", err)
			return
		}
		// 扫描结束后预热整库聚合：否则扫描后的第一次「照片流 / 时光档案」请求
		// 要等一次全表分组（7.4 万条在 NAS 上是百毫秒到秒级）。
		if _, err := s.d.Index.Summary(); err != nil {
			s.d.Logger.Warn("summary prewarm failed", "error", err)
		}
	}()
	return true
}

// scopeChanged 判断两份设置的扫描范围（媒体库与排除规则）是否不同。
func scopeChanged(before, after config.Settings) bool {
	if len(before.Libraries) != len(after.Libraries) || len(before.ExcludePatterns) != len(after.ExcludePatterns) {
		return true
	}
	for i := range before.Libraries {
		if filepath.Clean(before.Libraries[i].Path) != filepath.Clean(after.Libraries[i].Path) {
			return true
		}
	}
	for i := range before.ExcludePatterns {
		if before.ExcludePatterns[i] != after.ExcludePatterns[i] {
			return true
		}
	}
	return false
}

// handleScanCancel 中止正在跑的那一轮扫描。
//
// 取消不是回滚：已入库的条目保留，**未遍历到的旧条目也不会被判定为已删除**
// （Scanner 取消时把这一轮标成 incomplete，跳过移除判定）。下一次扫描自然收敛。
// 空闲时调用返回 409 而不是错误——幂等的 no-op 更符合「点了一下但没在跑」的直觉。
func (s *Server) handleScanCancel(w http.ResponseWriter, r *http.Request) {
	if !s.d.Scanner.Cancel() {
		writeError(w, http.StatusConflict, "not_running", "no scan is currently running")
		return
	}
	s.d.Logger.Info("scan cancellation requested")
	writeJSON(w, http.StatusAccepted, map[string]any{"cancelling": true})
}

func (s *Server) handleScanStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.d.Scanner.Status())
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "no_stream", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	channel := s.d.Hub.Subscribe()
	defer s.d.Hub.Unsubscribe(channel)

	fmt.Fprintf(w, "event: hello\ndata: {\"version\":%q,\"apiVersion\":%d}\n\n", s.d.Version, s.d.APIVersion)
	flusher.Flush()

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-channel:
			if !ok {
				return
			}
			payload, _ := json.Marshal(event.Data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Kind, payload)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) handleDays(w http.ResponseWriter, r *http.Request) {
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	days, err := s.d.Index.Days(from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days})
}

// handleStats backs the app's memory dashboard: totals, span and per-year buckets
// 全部来自索引，App 端不再需要为了「跨越几年 / 有多少照片」去枚举整库。
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	summary, err := s.d.Index.Summary()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stats": summary})
}

// handleMonths returns per-month aggregates inside one calendar year.
func (s *Server) handleMonths(w http.ResponseWriter, r *http.Request) {
	year, ok := parseQueryYear(r.URL.Query().Get("year"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_year", "year must be a four-digit calendar year")
		return
	}
	months, err := s.d.Index.Months(year)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"year": year, "months": months})
}

// handleStream 在「有内容的自然日」池里随机抽样，并一次带回这些日子的媒体。
//
// 这是记忆流的主读接口：客户端不再在日历日上随机试探、再逐日发请求，
// 空日探测与「抽到空相册」都被消除；exclude 用来避开当前正在展示的日子。
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	count := defaultStreamDays
	if raw := strings.TrimSpace(query.Get("count")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			writeError(w, http.StatusBadRequest, "bad_count", "count must be a positive integer")
			return
		}
		if parsed > maxStreamDays {
			parsed = maxStreamDays
		}
		count = parsed
	}

	year := 0
	if raw := strings.TrimSpace(query.Get("year")); raw != "" {
		parsed, ok := parseQueryYear(raw)
		if !ok {
			writeError(w, http.StatusBadRequest, "bad_year", "year must be a four-digit calendar year")
			return
		}
		year = parsed
	}

	exclude, err := parseExcludeDays(query.Get("exclude"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_exclude", err.Error())
		return
	}

	days, err := s.d.Index.SampleDays(year, exclude, count)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	keys := make([]string, 0, len(days))
	for _, day := range days {
		keys = append(keys, day.DayKey)
	}
	grouped, err := s.d.Index.ItemsByDays(keys)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(days))
	for _, day := range days {
		entries := grouped[day.DayKey]
		items := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			items = append(items, s.itemDTO(entry))
		}
		out = append(out, map[string]any{
			"dateKey":  day.DayKey,
			"count":    day.Count,
			"hasVideo": day.HasVideo,
			"coverId":  day.CoverID,
			"items":    items,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": out})
}

func (s *Server) handleDayItems(w http.ResponseWriter, r *http.Request) {
	day := r.PathValue("day")
	if _, err := time.Parse("2006-01-02", day); err != nil {
		writeError(w, http.StatusBadRequest, "bad_day", "day must be YYYY-MM-DD")
		return
	}
	entries, err := s.d.Index.ItemsByDay(day)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		items = append(items, s.itemDTO(entry))
	}
	writeJSON(w, http.StatusOK, map[string]any{"dateKey": day, "items": items})
}

// handleDaysItems returns several non-empty days in one SQL query. The app uses
// this for the photo stream so network latency does not multiply by candidate days.
func (s *Server) handleDaysItems(w http.ResponseWriter, r *http.Request) {
	days, err := parseRequestedDays(r.URL.Query().Get("days"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_days", err.Error())
		return
	}
	grouped, err := s.d.Index.ItemsByDays(days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(days))
	for _, day := range days {
		entries := grouped[day]
		items := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			items = append(items, s.itemDTO(entry))
		}
		if len(items) > 0 {
			out = append(out, map[string]any{"dateKey": day, "items": items})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": out})
}

// handleVideos backs the app's "动态放映" tab for remote sources.
func (s *Server) handleVideos(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if query.Get("limit") == "" && query.Get("cursor") == "" {
		entries, err := s.d.Index.Videos()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
			return
		}
		items := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			items = append(items, s.itemDTO(entry))
		}
		writeJSON(w, http.StatusOK, map[string]any{"videos": items})
		return
	}
	limit := 15
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			writeError(w, http.StatusBadRequest, "bad_limit", "limit must be a positive integer")
			return
		}
		limit = parsed
		if limit > 60 {
			limit = 60
		}
	}
	offset := 0
	if raw := strings.TrimSpace(query.Get("cursor")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "bad_cursor", "cursor must be a non-negative integer")
			return
		}
		offset = parsed
	}
	entries, hasNext, err := s.d.Index.VideosPage(offset, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		items = append(items, s.itemDTO(entry))
	}
	var nextCursor *string
	if hasNext {
		next := strconv.Itoa(offset + len(entries))
		nextCursor = &next
	}
	writeJSON(w, http.StatusOK, map[string]any{"videos": items, "nextCursor": nextCursor})
}

// handleLocations 一次返回全库带坐标的条目，供 App 铺满回忆地图。
//
// 逐条请求的代价在 74k 库上是不可接受的（每枚标记一次往返），因此这里一条 SQL 出全量；
// 客户端拿到后按「地点格 | 当天零点」自行归并。旧版本服务端没有这个接口，
// App 按能力位 `locations` 回落为「只铺本地相册」——行为与没有这个功能时一致。
func (s *Server) handleLocations(w http.ResponseWriter, r *http.Request) {
	locations, err := s.d.Index.Locations()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	if locations == nil {
		locations = []store.LocatedEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"locations": locations})
}

// handleItem resolves one entry by its stable content-fingerprint id.
// App 侧收藏 / 手记只保存 id（不含路径与日期），保存后重新打开必须能反查到原条目。
func (s *Server) handleItem(w http.ResponseWriter, r *http.Request) {
	entry, ok, err := s.d.Index.GetByID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no indexed entry with this id")
		return
	}
	// 片段不是用户可见条目：收藏 / 手记若指向被收敛的同内容副本（原先是 video），
	// 这里回落到它所属的动态照片，条目仍能打开。
	if entry.Kind == media.KindMotionClip {
		if still, found, err := s.d.Index.GetLivePhotoForClip(entry.ID); err == nil && found {
			entry = still
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": s.itemDTO(entry)})
}

func (s *Server) itemDTO(entry store.Entry) map[string]any {
	size := s.d.Settings.Get().ThumbSize
	dto := map[string]any{
		"id":            entry.ID,
		"kind":          entry.Kind,
		"name":          entry.Name,
		"path":          entry.Path,
		"size":          entry.Size,
		"capture":       entry.Capture,
		"captureSource": entry.CaptureSource,
		"width":         entry.Width,
		"height":        entry.Height,
		"hasMotion":     entry.HasMotion(),
		"thumbUrl":      fmt.Sprintf("/api/v1/thumbs/%s?size=%d", entry.ID, size),
		"originalUrl":   fmt.Sprintf("/api/v1/originals/%s", entry.ID),
	}
	if entry.HasMotion() {
		dto["motionVendor"] = entry.MotionVendor
		dto["motionConfidence"] = entry.MotionConfidence
		dto["motionUrl"] = fmt.Sprintf("/api/v1/motion/%s", entry.ID)
	}
	// 时长只对视频有意义；照片沿用 0，App 端据此决定是否显示时长角标。
	if entry.Kind == media.KindVideo {
		dto["duration"] = entry.Duration
	}
	if entry.Kind == media.KindRaw {
		dto["rawPreviewUrl"] = fmt.Sprintf("/api/v1/raw/%s/preview", entry.ID)
		dto["rawPreviewWidth"] = entry.RawPreviewWidth
		dto["rawPreviewHeight"] = entry.RawPreviewHeight
	}
	// 坐标随条目下发：地图的「此处当日」预览要按同一条 placeKey 过滤，
	// 而远端条目没有 PHAsset 可读定位，只能由服务端给。没有定位时整对字段省略，
	// 避免客户端把 0/0 当成「几内亚湾」这个真实地点。
	if entry.Latitude != 0 || entry.Longitude != 0 {
		dto["latitude"] = entry.Latitude
		dto["longitude"] = entry.Longitude
	}
	// 镜头与曝光参数（media.ProbeVersion 3 起）。沿用坐标那套「零值即省略」的策略：
	// 零值代表这张照片确实没有这项元数据（截图、社交保存图、扫描件普遍如此），
	// 不下发该键，客户端据此隐藏整格，而不是显示「f/0」「ISO 0」这种假参数。
	if entry.Make != "" {
		dto["make"] = entry.Make
	}
	if entry.Model != "" {
		dto["model"] = entry.Model
	}
	if entry.LensModel != "" {
		dto["lens"] = entry.LensModel
	}
	if entry.Aperture > 0 {
		dto["aperture"] = entry.Aperture
	}
	if entry.Exposure > 0 {
		dto["exposure"] = entry.Exposure
	}
	if entry.ISO > 0 {
		dto["iso"] = entry.ISO
	}
	if entry.FocalLength > 0 {
		dto["focalLength"] = entry.FocalLength
	}
	// 海拔可以为负（海平面以下），因此判据是「不等于 0」而不是「大于 0」。
	if entry.Altitude != 0 {
		dto["altitude"] = entry.Altitude
	}
	return dto
}

func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	entry, ok := s.lookupEntry(w, r.PathValue("id"))
	if !ok {
		return
	}
	size := s.d.Settings.Get().ThumbSize
	if raw := r.URL.Query().Get("size"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 64 && parsed <= 2048 {
			size = parsed
		}
	}
	path, err := s.d.Thumbs.Ensure(entry, size)
	if err != nil {
		writeError(w, http.StatusNotImplemented, "thumb_failed", err.Error())
		return
	}
	w.Header().Set("ETag", fmt.Sprintf("%q", entry.ID+strconv.Itoa(size)))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeFile(w, r, path)
}

func (s *Server) handleOriginal(w http.ResponseWriter, r *http.Request) {
	entry, ok := s.lookupEntry(w, r.PathValue("id"))
	if !ok {
		return
	}
	file, err := os.Open(entry.Path)
	if err != nil {
		writeError(w, http.StatusNotFound, "missing_file", err.Error())
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stat_failed", err.Error())
		return
	}
	http.ServeContent(w, r, entry.Name, info.ModTime(), file) // supports Range
}

func (s *Server) handleRawPreview(w http.ResponseWriter, r *http.Request) {
	entry, ok := s.lookupEntry(w, r.PathValue("id"))
	if !ok {
		return
	}
	if entry.RawPreviewLength <= 0 {
		writeError(w, http.StatusNotFound, "no_preview", "no embedded preview was indexed for this RAW file")
		return
	}
	data, err := media.ReadRange(entry.Path, entry.RawPreviewOffset, entry.RawPreviewLength)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(w, r, entry.Name+".jpg", time.Unix(entry.Modified, 0), bytes.NewReader(data))
}

func (s *Server) handleMotion(w http.ResponseWriter, r *http.Request) {
	entry, ok := s.lookupEntry(w, r.PathValue("id"))
	if !ok {
		return
	}
	switch {
	case entry.MotionPath != "":
		file, err := os.Open(entry.MotionPath)
		if err != nil {
			writeError(w, http.StatusNotFound, "missing_motion", err.Error())
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "stat_failed", err.Error())
			return
		}
		if ext := media.Extension(entry.MotionPath); ext == "mov" {
			w.Header().Set("Content-Type", "video/quicktime")
		} else {
			w.Header().Set("Content-Type", "video/mp4")
		}
		http.ServeContent(w, r, filepath.Base(entry.MotionPath), info.ModTime(), file)
	case entry.MotionOffset > 0 && entry.MotionLength > 0:
		file, err := os.Open(entry.Path)
		if err != nil {
			writeError(w, http.StatusNotFound, "missing_file", err.Error())
			return
		}
		defer file.Close()
		w.Header().Set("Content-Type", "video/mp4")
		section := io.NewSectionReader(file, entry.MotionOffset, entry.MotionLength)
		http.ServeContent(w, r, entry.Name+".mp4", time.Unix(entry.Modified, 0), section)
	default:
		writeError(w, http.StatusNotFound, "no_motion", "this item has no motion segment")
	}
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	settings := s.d.Settings.Get()
	stats, _ := s.d.Index.Stats()
	thumbFiles, thumbBytes := s.d.Thumbs.Stats()
	found := volumes.Discover(settings.Libraries, s.d.DataDir)
	writeJSON(w, http.StatusOK, map[string]any{
		"version":       s.d.Version,
		"apiVersion":    s.d.APIVersion,
		"serverId":      s.d.Tokens.ServerID(),
		"uptimeSeconds": int(time.Since(s.d.StartedAt).Seconds()),
		"index": map[string]any{
			"entries":     stats.Entries,
			"removed":     stats.Removed,
			"byKind":      stats.ByKind,
			"lastScanGen": stats.LastScanGen,
			"lastScanAt":  s.d.Index.LastScanAt(),
		},
		"cache": map[string]any{
			"thumbFiles":      thumbFiles,
			"thumbBytes":      thumbBytes,
			"thumbLimitBytes": settings.ThumbCacheLimitBytes,
		},
		"volumes":       found,
		"suggestedUser": volumes.SuggestedUser(found),
		"tools": map[string]any{
			"vipsthumbnail": toolAvailable("vipsthumbnail"),
			"ffmpeg":        toolAvailable("ffmpeg"),
		},
		"settings": settings,
	})
}

// logLevels 是控制台可以请求的级别白名单。空串表示「不限级别」。
var logLevels = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}

// handleLogs returns the recent log tail.
//
// 新增 level 与 q 两个可选参数供控制台过滤；两个都不传时行为与之前完全一致
// （tail 非法值仍按旧逻辑忽略，不改语义，避免影响已发布的 App）。
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	tail := 200
	if raw := query.Get("tail"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 2000 {
			tail = parsed
		}
	}

	level := strings.ToLower(strings.TrimSpace(query.Get("level")))
	if level != "" && !logLevels[level] {
		writeError(w, http.StatusBadRequest, "bad_level", "level must be debug, info, warn or error")
		return
	}

	lines := []string{}
	if s.d.LogRing != nil {
		lines = s.d.LogRing.TailFiltered(level, query.Get("q"), tail)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"lines": lines,
		"level": level,
		"tail":  tail,
	})
}

// handleTokens lists paired devices. Hash 刻意不下发：它是密钥摘要，对外展示
// 无助于识别设备，只会扩大泄漏面。「名称 + 平台 + 最后活动」已足够定位。
func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request) {
	tokens := s.d.Tokens.List()
	out := make([]map[string]any, 0, len(tokens))
	for _, token := range tokens {
		out = append(out, map[string]any{
			"id":        token.ID,
			"name":      token.Name,
			"platform":  token.Platform,
			"createdAt": token.CreatedAt,
			"lastSeen":  token.LastSeen,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.d.Tokens.Revoke(id); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	s.d.Logger.Info("token revoked", "tokenId", id)
	writeJSON(w, http.StatusOK, map[string]any{"revoked": id})
}

func (s *Server) lookupEntry(w http.ResponseWriter, id string) (store.Entry, bool) {
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_id", "missing id")
		return store.Entry{}, false
	}
	entry, ok, err := s.d.Index.GetByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return store.Entry{}, false
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "unknown media id")
		return store.Entry{}, false
	}
	// 索引里的条目在下一轮扫描前可能已不属于任何媒体库（库被移除）：
	// 这时不能再凭 id 读出原图、缩略图或动态片段。
	if !withinLibraries(entry.Path, s.d.Settings.Get().Libraries) {
		writeError(w, http.StatusNotFound, "not_in_library", "media is outside the configured libraries")
		return store.Entry{}, false
	}
	return entry, true
}

const (
	// 记忆流单次抽样的默认与上限天数：响应体量约等于天数 × 当日条目数，
	// 20 天在局域网里仍是可接受的单次往返。
	defaultStreamDays = 8
	maxStreamDays     = 20
	// exclude 只用来避开「当前正在展示 + 最近看过」的日子，正常在两位数；
	// 上限仅防止 URL 失控，超出直接报错而不是静默截断。
	maxStreamExclude = 500
)

// parseQueryYear validates a four-digit calendar year. 四位输入对应的年份必然 ≥ 1000，
// 更小的值只可能是 "0202" 这类无效输入，直接拒绝而不是当成 202 年去查。
func parseQueryYear(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) != 4 {
		return 0, false
	}
	parsed, err := time.Parse("2006", raw)
	if err != nil || parsed.Year() < 1000 {
		return 0, false
	}
	return parsed.Year(), true
}

// parseExcludeDays parses a comma-separated list of YYYY-MM-DD day keys.
func parseExcludeDays(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxStreamExclude {
		return nil, fmt.Errorf("exclude accepts at most %d day keys", maxStreamExclude)
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		key := strings.TrimSpace(part)
		if key == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", key); err != nil {
			return nil, fmt.Errorf("exclude keys must use the YYYY-MM-DD form")
		}
		out = append(out, key)
	}
	return out, nil
}

func parseRequestedDays(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	if len(parts) == 0 || len(parts) > 40 {
		return nil, fmt.Errorf("days must contain between 1 and 40 dates")
	}
	seen := make(map[string]struct{}, len(parts))
	result := make([]string, 0, len(parts))
	for _, rawDay := range parts {
		day := strings.TrimSpace(rawDay)
		if _, err := time.Parse("2006-01-02", day); err != nil {
			return nil, fmt.Errorf("days must contain YYYY-MM-DD values")
		}
		if _, exists := seen[day]; exists {
			continue
		}
		seen[day] = struct{}{}
		result = append(result, day)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("days must contain at least one date")
	}
	return result, nil
}

func countMediaDirect(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		if _, ok := media.Classify(ent.Name()); ok {
			count++
			if count >= 20000 {
				break
			}
		}
	}
	return count
}

// countMedia 优先用索引计数：文档推荐把存储卷根只读挂载，此时子目录可达数百到数千，
// 逐个做文件系统枚举会让文件夹选择页随目录数线性变慢。
// 索引里没有记录（尚未扫描、或确实为空）时回落文件系统，保证首次使用也有数字。
func (s *Server) countMedia(path string) int {
	if s.d.Index != nil {
		if n, err := s.d.Index.CountMediaInDir(path); err == nil && n > 0 {
			return n
		}
	}
	return countMediaDirect(path)
}

func withinLibraries(path string, libraries []config.Library) bool {
	for _, lib := range libraries {
		root := filepath.Clean(lib.Path)
		if path == root {
			return true
		}
		if strings.HasPrefix(path, root+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// withinResolvedLibraries 解析符号链接后再判一次边界：库根自身也可能是符号链接，
// 因此两边都解析，任一组合命中即视为库内；路径无法解析时放行，交给 os.ReadDir 报错。
func withinResolvedLibraries(path string, libraries []config.Library) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return true
	}
	for _, lib := range libraries {
		roots := []string{filepath.Clean(lib.Path)}
		if link, err := filepath.EvalSymlinks(lib.Path); err == nil {
			roots = append(roots, filepath.Clean(link))
		}
		for _, root := range roots {
			if resolved == root || strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
				return true
			}
		}
	}
	return false
}

func toolAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
