package api

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SherlockGougou/itemory-agent/internal/config"
	"github.com/SherlockGougou/itemory-agent/internal/events"
	"github.com/SherlockGougou/itemory-agent/internal/logging"
	"github.com/SherlockGougou/itemory-agent/internal/scan"
	"github.com/SherlockGougou/itemory-agent/internal/store"
	"github.com/SherlockGougou/itemory-agent/internal/thumbs"
)

func TestPairingAuthAndMediaFlow(t *testing.T) {
	library := t.TempDir()
	writeTestJPEG(t, filepath.Join(library, "IMG_20240102_030405.jpg"))
	// 动态照片对 + 备份工具写出的同内容副本：副本必须被收敛进片段，不出现在时间线与视频清单里。
	uuid := "3671E07C-896E-441B-B72E-6CFDB2B3B31C"
	stillPath := filepath.Join(library, "IMG_20240102_030400.HEIC")
	if err := os.WriteFile(stillPath,
		append([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'c'}, []byte("xxxx"+uuid+"xxxx")...), 0o644); err != nil {
		t.Fatal(err)
	}
	var movie bytes.Buffer
	movie.Write(bytes.Repeat([]byte{0}, 1024))
	movie.WriteString("com.apple.quicktime.content.identifier")
	movie.WriteString(uuid)
	moviePath := filepath.Join(library, "IMG_20240102_030400.MOV")
	if err := os.WriteFile(moviePath, movie.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	movieCopyPath := filepath.Join(library, "IMG_20240102_030400_1.MOV")
	if err := os.WriteFile(movieCopyPath, movie.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	index, err := store.Open(filepath.Join(dataDir, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()

	settings, err := config.Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	log := logging.New("error", logging.NewRing(100))
	hub := events.NewHub()
	thumbMgr := thumbs.New(filepath.Join(dataDir, "thumbs"), settings, log)
	scanner := scan.New(index, settings, hub, log)
	tokens, err := LoadTokens(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	tokens.SetClaim("123456", 15*time.Minute)

	server := NewServer(Deps{
		Version: "test", APIVersion: 1, DataDir: dataDir, Index: index, Settings: settings,
		Thumbs: thumbMgr, Scanner: scanner, Hub: hub, Tokens: tokens, Logger: log,
		LogRing: logging.NewRing(10), StartedAt: time.Now(),
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	adminCookie := configureTestAdmin(t, server)

	// Health is public.
	if code := get(t, httpServer.URL+"/api/v1/health", ""); code != http.StatusOK {
		t.Fatalf("health status %d", code)
	}
	// Everything else needs a token.
	if code := get(t, httpServer.URL+"/api/v1/diagnostics", ""); code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", code)
	}
	// Wrong pairing code is rejected.
	body, _ := json.Marshal(map[string]string{"token": "000000", "deviceName": "test"})
	resp, err := http.Post(httpServer.URL+"/api/v1/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for wrong code, got %d", resp.StatusCode)
	}
	// Correct pairing code returns a bearer token.
	body, _ = json.Marshal(map[string]string{"token": "123456", "deviceName": "unit-test"})
	resp, err = http.Post(httpServer.URL+"/api/v1/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var pairResponse struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pairResponse); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || pairResponse.Token == "" {
		t.Fatalf("pair failed: %d %+v", resp.StatusCode, pairResponse)
	}
	bearer := "Bearer " + pairResponse.Token

	// Configure a library through the settings API (the app does this).
	putJSON(t, httpServer.URL+"/api/v1/settings", bearer, map[string]any{
		"schemaVersion":           1,
		"preset":                  "balanced",
		"libraries":               []map[string]string{{"id": "lib", "name": "lib", "path": library}},
		"excludePatterns":         []string{"@eaDir"},
		"concurrency":             1,
		"thumbSize":               256,
		"thumbCacheLimitBytes":    1 << 30,
		"originalCacheLimitBytes": 1 << 30,
		"nightlyThumbBudget":      100,
		"transcode":               "off",
		"motionPhoto":             true,
		"rawPreview":              true,
		"scanSchedule":            "",
		"logLevel":                "error",
	})

	// Scan and wait for completion.
	postJSON(t, httpServer.URL+"/api/v1/scan", bearer, map[string]string{"mode": "incremental"})
	started := false
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		code, data := getBody(t, httpServer.URL+"/api/v1/scan/status", bearer)
		if code == http.StatusOK {
			var progress struct {
				Running   bool `json:"running"`
				FilesSeen int  `json:"filesSeen"`
			}
			_ = json.Unmarshal(data, &progress)
			if progress.Running || progress.FilesSeen > 0 {
				started = true
			}
			if started && !progress.Running {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !started {
		t.Fatal("scan never started")
	}

	// Days endpoint returns the capture day; items expose media URLs.
	var daysResponse struct {
		Days []store.DayStat `json:"days"`
	}
	var data []byte
	var code int
	for time.Now().Before(deadline) {
		code, data = getBody(t, httpServer.URL+"/api/v1/days", bearer)
		if code != http.StatusOK {
			t.Fatalf("days status %d", code)
		}
		if err := json.Unmarshal(data, &daysResponse); err != nil {
			t.Fatal(err)
		}
		if len(daysResponse.Days) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(daysResponse.Days) != 1 || daysResponse.Days[0].DayKey != "2024-01-02" {
		t.Fatalf("unexpected days: %+v", daysResponse.Days)
	}
	mediaID := daysResponse.Days[0].CoverID

	code, data = getBody(t, httpServer.URL+"/api/v1/days/2024-01-02/items", bearer)
	if code != http.StatusOK {
		t.Fatalf("items status %d", code)
	}
	var itemsResponse struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(data, &itemsResponse); err != nil {
		t.Fatal(err)
	}
	// 动态照片的静帧在，同内容副本不在：一天里只应看到照片与动态照片两条。
	if len(itemsResponse.Items) != 2 || daysResponse.Days[0].Count != 2 {
		t.Fatalf("unexpected items: %+v", itemsResponse.Items)
	}
	hasCover := false
	for _, item := range itemsResponse.Items {
		if item["id"] == mediaID {
			hasCover = true
		}
		if item["kind"] == "motion-clip" || item["kind"] == "video" {
			t.Fatalf("motion clip copy leaked into day items: %+v", item)
		}
	}
	if !hasCover {
		t.Fatalf("day items must contain the cover (live photo still): %+v", itemsResponse.Items)
	}
	// 动态放映清单里不能有动态片段的同内容副本。
	code, data = getBody(t, httpServer.URL+"/api/v1/videos", bearer)
	if code != http.StatusOK {
		t.Fatalf("videos status %d", code)
	}
	var videosResponse struct {
		Videos []map[string]any `json:"videos"`
	}
	if err := json.Unmarshal(data, &videosResponse); err != nil {
		t.Fatal(err)
	}
	if len(videosResponse.Videos) != 0 {
		t.Fatalf("motion clip copy leaked into the video list: %+v", videosResponse.Videos)
	}
	// 批量自然日接口应一次返回已有日期，并忽略没有内容的日期。
	code, data = getBody(t, httpServer.URL+"/api/v1/days/items?days=2024-01-02,2024-01-03", bearer)
	if code != http.StatusOK {
		t.Fatalf("batch day items status %d", code)
	}
	var batchDaysResponse struct {
		Days []struct {
			DateKey string           `json:"dateKey"`
			Items   []map[string]any `json:"items"`
		} `json:"days"`
	}
	if err := json.Unmarshal(data, &batchDaysResponse); err != nil {
		t.Fatal(err)
	}
	if len(batchDaysResponse.Days) != 1 || batchDaysResponse.Days[0].DateKey != "2024-01-02" || len(batchDaysResponse.Days[0].Items) != 2 {
		t.Fatalf("unexpected batch day items: %+v", batchDaysResponse.Days)
	}
	// 分页视频接口保持稳定的 capture/path 顺序，并在有下一页时返回游标。
	code, data = getBody(t, httpServer.URL+"/api/v1/videos?limit=1", bearer)
	if code != http.StatusOK {
		t.Fatalf("paginated videos status %d", code)
	}
	var pagedVideosResponse struct {
		Videos     []map[string]any `json:"videos"`
		NextCursor *string          `json:"nextCursor"`
	}
	if err := json.Unmarshal(data, &pagedVideosResponse); err != nil {
		t.Fatal(err)
	}
	if len(pagedVideosResponse.Videos) != 0 || pagedVideosResponse.NextCursor != nil {
		t.Fatalf("unexpected paginated videos: %+v", pagedVideosResponse)
	}

	// Thumbnail, original and diagnostics.
	// 单条元数据端点：收藏 / 手记按 id 反查（未知 id 必须 404，不能 500）
	code, data = getBody(t, httpServer.URL+"/api/v1/items/"+mediaID, bearer)
	if code != http.StatusOK {
		t.Fatalf("item status %d", code)
	}
	var singleItem struct {
		Item map[string]any `json:"item"`
	}
	if err := json.Unmarshal(data, &singleItem); err != nil {
		t.Fatal(err)
	}
	if singleItem.Item["id"] != mediaID {
		t.Fatalf("unexpected item: %+v", singleItem.Item)
	}
	// 被收敛副本的内容指纹（收藏 / 手记里的 id）必须回落到它所属的动态照片。
	copyEntry, found, err := index.GetByPath(movieCopyPath)
	if err != nil || !found || copyEntry.Kind != "motion-clip" {
		t.Fatalf("motion clip copy not absorbed: %+v %v %v", copyEntry, found, err)
	}
	stillEntry, found, err := index.GetByPath(stillPath)
	if err != nil || !found || stillEntry.Kind != "motion" {
		t.Fatalf("live photo still not paired: %+v %v %v", stillEntry, found, err)
	}
	code, data = getBody(t, httpServer.URL+"/api/v1/items/"+copyEntry.ID, bearer)
	if code != http.StatusOK {
		t.Fatalf("item status for absorbed copy %d", code)
	}
	if err := json.Unmarshal(data, &singleItem); err != nil {
		t.Fatal(err)
	}
	if singleItem.Item["id"] != stillEntry.ID || singleItem.Item["kind"] != "motion" {
		t.Fatalf("absorbed copy must resolve to its live photo: %+v", singleItem.Item)
	}
	if code := get(t, httpServer.URL+"/api/v1/items/missing-id", bearer); code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown id, got %d", code)
	}
	if code := get(t, httpServer.URL+"/api/v1/thumbs/"+mediaID+"?size=256", bearer); code != http.StatusOK {
		t.Fatalf("thumb status %d", code)
	}
	if code := get(t, httpServer.URL+"/api/v1/originals/"+mediaID, bearer); code != http.StatusOK {
		t.Fatalf("original status %d", code)
	}
	if code := get(t, httpServer.URL+"/api/v1/diagnostics", bearer); code != http.StatusOK {
		t.Fatalf("diagnostics status %d", code)
	}

	// Revoking the device invalidates the bearer token.
	request, _ := http.NewRequest(http.MethodDelete, httpServer.URL+"/api/v1/tokens/"+tokenID(t, httpServer.URL, bearer, adminCookie), nil)
	request.AddCookie(adminCookie)
	revokeResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	revokeResponse.Body.Close()
	if revokeResponse.StatusCode != http.StatusOK {
		t.Fatalf("revoke status %d", revokeResponse.StatusCode)
	}
	if code := get(t, httpServer.URL+"/api/v1/diagnostics", bearer); code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after revoke, got %d", code)
	}
}

func tokenID(t *testing.T, baseURL, bearer string, adminCookie *http.Cookie) string {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/tokens", nil)
	request.AddCookie(adminCookie)
	httpResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer httpResponse.Body.Close()
	var body bytes.Buffer
	_, _ = body.ReadFrom(httpResponse.Body)
	code, data := httpResponse.StatusCode, body.Bytes()
	if code != http.StatusOK {
		t.Fatalf("tokens status %d", code)
	}
	var payload struct {
		Tokens []struct {
			ID string `json:"id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || len(payload.Tokens) == 0 {
		t.Fatalf("tokens decode: %v %+v", err, payload)
	}
	return payload.Tokens[0].ID
}

// TestSettingsPresetSwitchAndCacheClear covers the settings wiring: switching
// the preset on PUT applies the server-side profile, and deleting the thumbnail
// cache clears files without touching settings (index/user data).
func TestSettingsPresetSwitchAndCacheClear(t *testing.T) {
	library := t.TempDir()
	dataDir := t.TempDir()
	index, err := store.Open(filepath.Join(dataDir, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()

	settings, err := config.Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	log := logging.New("error", logging.NewRing(50))
	hub := events.NewHub()
	thumbDir := filepath.Join(dataDir, "thumbs")
	thumbMgr := thumbs.New(thumbDir, settings, log)
	scanner := scan.New(index, settings, hub, log)
	tokens, err := LoadTokens(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	tokens.SetClaim("654321", 15*time.Minute)

	server := NewServer(Deps{
		Version: "test", APIVersion: 1, DataDir: dataDir, Index: index, Settings: settings,
		Thumbs: thumbMgr, Scanner: scanner, Hub: hub, Tokens: tokens, Logger: log,
		LogRing: logging.NewRing(10), StartedAt: time.Now(),
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	bearer := pairDevice(t, httpServer.URL, "654321")

	// A preset switch must overwrite the resource profile even though the app
	// sends its own (stale) values for the individual fields.
	putJSON(t, httpServer.URL+"/api/v1/settings", bearer, map[string]any{
		"schemaVersion":           1,
		"preset":                  "light",
		"libraries":               []map[string]string{{"id": "lib", "name": "lib", "path": library}},
		"excludePatterns":         []string{"@eaDir"},
		"concurrency":             8,
		"thumbSize":               2048,
		"thumbCacheLimitBytes":    10 << 30,
		"originalCacheLimitBytes": 2 << 30,
		"nightlyThumbBudget":      20000,
		"transcode":               "off",
		"motionPhoto":             true,
		"rawPreview":              true,
		"scanSchedule":            "",
		"logLevel":                "error",
	})
	code, data := getBody(t, httpServer.URL+"/api/v1/settings", bearer)
	if code != http.StatusOK {
		t.Fatalf("settings status %d", code)
	}
	var applied config.Settings
	if err := json.Unmarshal(data, &applied); err != nil {
		t.Fatal(err)
	}
	if applied.Preset != "light" || applied.Concurrency != 1 || applied.ThumbSize != 256 {
		t.Fatalf("light preset not applied: %+v", applied)
	}
	if applied.ThumbCacheLimitBytes != 1<<30 || applied.NightlyThumbBudget != 2000 {
		t.Fatalf("light preset limits not applied: %+v", applied)
	}

	cached := filepath.Join(thumbDir, "sample-512.jpg")
	if err := os.WriteFile(cached, bytes.Repeat([]byte{7}, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodDelete, httpServer.URL+"/api/v1/cache/thumbs", nil)
	request.Header.Set("Authorization", bearer)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("cache clear status %d", response.StatusCode)
	}
	if _, err := os.Stat(cached); !os.IsNotExist(err) {
		t.Fatalf("thumbnail cache file survived clear: %v", err)
	}

	// Clearing the cache must not touch the configured libraries (index/user data).
	code, data = getBody(t, httpServer.URL+"/api/v1/settings", bearer)
	if code != http.StatusOK {
		t.Fatalf("settings after clear status %d", code)
	}
	var afterClear config.Settings
	if err := json.Unmarshal(data, &afterClear); err != nil {
		t.Fatal(err)
	}
	if len(afterClear.Libraries) != 1 || afterClear.Libraries[0].Path != library {
		t.Fatalf("libraries changed after cache clear: %+v", afterClear.Libraries)
	}
}

// TestAggregateEndpoints covers /api/v1/stats, /api/v1/months and /api/v1/stream:
// 聚合口径与 days/items 一致（动态片段副本不计入），年份与月份只包含有内容的桶，
// stream 支持年份限定与 exclude，非法入参一律 400 而不是静默降级。
func TestAggregateEndpoints(t *testing.T) {
	library := t.TempDir()
	dataDir := t.TempDir()
	index, err := store.Open(filepath.Join(dataDir, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()

	gen, err := index.BeginScan()
	if err != nil {
		t.Fatal(err)
	}
	if err := index.UpsertMany([]store.Entry{
		{Path: filepath.Join(library, "a.jpg"), ID: "i1", Kind: "image", Capture: 1_709_251_200, DayKey: "2024-03-01", LastSeen: gen},
		{Path: filepath.Join(library, "b.jpg"), ID: "i2", Kind: "image", Capture: 1_709_254_800, DayKey: "2024-03-01", LastSeen: gen},
		{Path: filepath.Join(library, "c.mp4"), ID: "v1", Kind: "video", Capture: 1_709_337_600, DayKey: "2024-03-02", LastSeen: gen},
		{Path: filepath.Join(library, "d.heic"), ID: "m1", Kind: "motion", Capture: 1_709_341_200, DayKey: "2024-03-02", LastSeen: gen,
			MotionPath: filepath.Join(library, "d.mov")},
		{Path: filepath.Join(library, "d.mov"), ID: "c1", Kind: "motion-clip", Capture: 1_709_341_200, DayKey: "2024-03-02", LastSeen: gen},
		{Path: filepath.Join(library, "old.jpg"), ID: "i3", Kind: "image", Capture: 1_679_011_200, DayKey: "2023-03-15", LastSeen: gen},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := index.FinishScan(gen); err != nil {
		t.Fatal(err)
	}

	settings, err := config.Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	log := logging.New("error", logging.NewRing(50))
	hub := events.NewHub()
	tokens, err := LoadTokens(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	tokens.SetClaim("246810", 15*time.Minute)
	server := NewServer(Deps{
		Version: "test", APIVersion: 1, DataDir: dataDir, Index: index, Settings: settings,
		Thumbs: thumbs.New(filepath.Join(dataDir, "thumbs"), settings, log), Scanner: scan.New(index, settings, hub, log),
		Hub: hub, Tokens: tokens, Logger: log, LogRing: logging.NewRing(10), StartedAt: time.Now(),
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	// 新能力必须出现在 health 的能力位里，客户端据此决定是否走聚合读接口。
	code, data := getBody(t, httpServer.URL+"/api/v1/health", "")
	if code != http.StatusOK {
		t.Fatalf("health status %d", code)
	}
	var health struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(data, &health); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"stats", "months", "stream"} {
		if !contains(health.Capabilities, want) {
			t.Fatalf("capability %q missing: %v", want, health.Capabilities)
		}
	}

	bearer := pairDevice(t, httpServer.URL, "246810")

	code, data = getBody(t, httpServer.URL+"/api/v1/stats", bearer)
	if code != http.StatusOK {
		t.Fatalf("stats status %d", code)
	}
	var stats struct {
		Stats store.Summary `json:"stats"`
	}
	if err := json.Unmarshal(data, &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Stats.Items != 5 || stats.Stats.ContentDays != 3 {
		t.Fatalf("unexpected stats totals: %+v", stats.Stats)
	}
	if stats.Stats.ByKind["image"] != 3 || stats.Stats.ByKind["video"] != 1 || stats.Stats.ByKind["motion"] != 1 {
		t.Fatalf("unexpected stats byKind: %+v", stats.Stats.ByKind)
	}
	if len(stats.Stats.Years) != 2 || stats.Stats.Years[0].Year != 2024 || stats.Stats.Years[0].CoverID != "m1" {
		t.Fatalf("unexpected stats years: %+v", stats.Stats.Years)
	}

	code, data = getBody(t, httpServer.URL+"/api/v1/months?year=2024", bearer)
	if code != http.StatusOK {
		t.Fatalf("months status %d", code)
	}
	var months struct {
		Year   int               `json:"year"`
		Months []store.MonthStat `json:"months"`
	}
	if err := json.Unmarshal(data, &months); err != nil {
		t.Fatal(err)
	}
	if months.Year != 2024 || len(months.Months) != 1 || months.Months[0].Month != 3 {
		t.Fatalf("unexpected months: %+v", months)
	}
	if months.Months[0].Items != 4 || months.Months[0].Days != 2 || !months.Months[0].HasVideo {
		t.Fatalf("unexpected march bucket: %+v", months.Months[0])
	}
	if code := get(t, httpServer.URL+"/api/v1/months?year=abcd", bearer); code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a malformed year, got %d", code)
	}
	if code := get(t, httpServer.URL+"/api/v1/months", bearer); code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a missing year, got %d", code)
	}

	// days 聚合是「某天有多少内容」的唯一口径，stream 必须与之逐日一致。
	code, data = getBody(t, httpServer.URL+"/api/v1/days", bearer)
	if code != http.StatusOK {
		t.Fatalf("days status %d", code)
	}
	var daysResponse struct {
		Days []store.DayStat `json:"days"`
	}
	if err := json.Unmarshal(data, &daysResponse); err != nil {
		t.Fatal(err)
	}
	covers := map[string]store.DayStat{}
	for _, day := range daysResponse.Days {
		covers[day.DayKey] = day
	}

	code, data = getBody(t, httpServer.URL+"/api/v1/stream?year=2024&count=10", bearer)
	if code != http.StatusOK {
		t.Fatalf("stream status %d", code)
	}
	var stream struct {
		Days []struct {
			DateKey  string           `json:"dateKey"`
			Count    int              `json:"count"`
			HasVideo bool             `json:"hasVideo"`
			CoverID  string           `json:"coverId"`
			Items    []map[string]any `json:"items"`
		} `json:"days"`
	}
	if err := json.Unmarshal(data, &stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.Days) != 2 {
		t.Fatalf("stream must return every content day of the year: %+v", stream.Days)
	}
	for _, day := range stream.Days {
		aggregate, ok := covers[day.DateKey]
		if !ok {
			t.Fatalf("stream returned a day outside the index: %+v", day)
		}
		if day.Count != aggregate.Count || day.CoverID != aggregate.CoverID || day.HasVideo != aggregate.HasVideo {
			t.Fatalf("stream day disagrees with /days: %+v vs %+v", day, aggregate)
		}
		if len(day.Items) != day.Count {
			t.Fatalf("stream day must carry its media: %+v", day)
		}
		for _, item := range day.Items {
			if item["kind"] == "motion-clip" {
				t.Fatalf("clip leaked into the stream: %+v", item)
			}
			if item["thumbUrl"] == nil || item["originalUrl"] == nil {
				t.Fatalf("stream items must expose media urls: %+v", item)
			}
		}
	}
	if stream.Days[0].DateKey != "2024-03-02" || stream.Days[1].DateKey != "2024-03-01" {
		t.Fatalf("stream days must be newest first: %+v", stream.Days)
	}

	code, data = getBody(t, httpServer.URL+"/api/v1/stream?exclude=2024-03-01,2024-03-02", bearer)
	if code != http.StatusOK {
		t.Fatalf("stream exclude status %d", code)
	}
	if err := json.Unmarshal(data, &stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.Days) != 1 || stream.Days[0].DateKey != "2023-03-15" {
		t.Fatalf("exclude must drop the shown days: %+v", stream.Days)
	}

	for _, query := range []string{"count=0", "count=abc", "exclude=2024-3-1", "year=0202"} {
		if code := get(t, httpServer.URL+"/api/v1/stream?"+query, bearer); code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %q, got %d", query, code)
		}
	}
}

// 位置索引接口是回忆地图铺远端标记的唯一入口：一次拿全量，口径与时间线一致。
func TestLocationsEndpointAndCapability(t *testing.T) {
	library := t.TempDir()
	dataDir := t.TempDir()
	index, err := store.Open(filepath.Join(dataDir, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()

	gen, err := index.BeginScan()
	if err != nil {
		t.Fatal(err)
	}
	if err := index.UpsertMany([]store.Entry{
		{Path: filepath.Join(library, "a.jpg"), ID: "loc1", Kind: "image", Capture: 1_709_251_200, DayKey: "2024-03-01",
			Latitude: 31.23894, Longitude: 121.47317, LastSeen: gen},
		{Path: filepath.Join(library, "b.mov"), ID: "loc2", Kind: "video", Capture: 1_709_337_600, DayKey: "2024-03-02",
			Latitude: -33.8688, Longitude: 151.2093, LastSeen: gen},
		{Path: filepath.Join(library, "c.jpg"), ID: "noloc", Kind: "image", Capture: 1_709_254_800, DayKey: "2024-03-01", LastSeen: gen},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := index.FinishScan(gen); err != nil {
		t.Fatal(err)
	}

	settings, err := config.Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	log := logging.New("error", logging.NewRing(50))
	hub := events.NewHub()
	tokens, err := LoadTokens(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	tokens.SetClaim("778899", 15*time.Minute)
	server := NewServer(Deps{
		Version: "test", APIVersion: 1, DataDir: dataDir, Index: index, Settings: settings,
		Thumbs: thumbs.New(filepath.Join(dataDir, "thumbs"), settings, log), Scanner: scan.New(index, settings, hub, log),
		Hub: hub, Tokens: tokens, Logger: log, LogRing: logging.NewRing(10), StartedAt: time.Now(),
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	// 能力位：旧版本服务端没有它，App 据此回落「只铺本地相册」，不会对 404 反复请求。
	code, data := getBody(t, httpServer.URL+"/api/v1/health", "")
	if code != http.StatusOK {
		t.Fatalf("health status %d", code)
	}
	var health struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(data, &health); err != nil {
		t.Fatal(err)
	}
	if !contains(health.Capabilities, "locations") {
		t.Fatalf("capability \"locations\" missing: %v", health.Capabilities)
	}

	// 鉴权与其它读接口一致。
	if code := get(t, httpServer.URL+"/api/v1/locations", ""); code != http.StatusUnauthorized {
		t.Fatalf("locations without a token must be 401, got %d", code)
	}

	bearer := pairDevice(t, httpServer.URL, "778899")
	code, data = getBody(t, httpServer.URL+"/api/v1/locations", bearer)
	if code != http.StatusOK {
		t.Fatalf("locations status %d", code)
	}
	var payload struct {
		Locations []store.LocatedEntry `json:"locations"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Locations) != 2 {
		t.Fatalf("expected the two located entries, got %+v", payload.Locations)
	}
	if payload.Locations[0].ID != "loc2" || payload.Locations[0].Kind != "video" {
		t.Fatalf("newest first and kind must travel: %+v", payload.Locations[0])
	}
	if payload.Locations[1].Latitude != 31.23894 || payload.Locations[1].Longitude != 121.47317 {
		t.Fatalf("unexpected coordinates: %+v", payload.Locations[1])
	}
	for _, location := range payload.Locations {
		if location.ID == "noloc" {
			t.Fatalf("an entry without coordinates leaked into the location index: %+v", location)
		}
	}

	// 单条元数据也要带坐标：地图的「此处当日」预览按同一条 placeKey 过滤，
	// 远端条目没有 PHAsset 可读定位，只能由服务端给。
	code, data = getBody(t, httpServer.URL+"/api/v1/items/loc1", bearer)
	if code != http.StatusOK {
		t.Fatalf("item status %d", code)
	}
	var item struct {
		Item map[string]any `json:"item"`
	}
	if err := json.Unmarshal(data, &item); err != nil {
		t.Fatal(err)
	}
	if item.Item["latitude"] != 31.23894 || item.Item["longitude"] != 121.47317 {
		t.Fatalf("item must expose its coordinate: %+v", item.Item)
	}
	// 没有定位的条目省略整对字段，客户端才不会把 0/0 当成真实地点。
	code, data = getBody(t, httpServer.URL+"/api/v1/items/noloc", bearer)
	if code != http.StatusOK {
		t.Fatalf("item status %d", code)
	}
	// 反序列化进 map 是合并而不是替换：复用同一个变量前必须清空，
	// 否则上一轮解出的 latitude 会留在这次的结果里（本用例第一次就是这么假红过的）。
	item.Item = nil
	if err := json.Unmarshal(data, &item); err != nil {
		t.Fatal(err)
	}
	if _, present := item.Item["latitude"]; present {
		t.Fatalf("a located-less item must not carry a coordinate: %+v", item.Item)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func pairDevice(t *testing.T, baseURL, code string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"token": code, "deviceName": "unit-test"})
	response, err := http.Post(baseURL+"/api/v1/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || payload.Token == "" {
		t.Fatalf("pair failed: %d", response.StatusCode)
	}
	return "Bearer " + payload.Token
}

func get(t *testing.T, url, bearer string) int {
	t.Helper()
	code, _ := getBody(t, url, bearer)
	return code
}

func getBody(t *testing.T, url, bearer string) (int, []byte) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, url, nil)
	if bearer != "" {
		request.Header.Set("Authorization", bearer)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(response.Body)
	return response.StatusCode, buf.Bytes()
}

func postJSON(t *testing.T, url, bearer string, payload any) {
	t.Helper()
	body, _ := json.Marshal(payload)
	request, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	request.Header.Set("Authorization", bearer)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}

func putJSON(t *testing.T, url, bearer string, payload any) {
	t.Helper()
	body, _ := json.Marshal(payload)
	request, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	request.Header.Set("Authorization", bearer)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(response.Body)
		t.Fatalf("put %s failed: %d %s", url, response.StatusCode, buf.String())
	}
}

func writeTestJPEG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 128, A: 255})
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
