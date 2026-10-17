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
	"strings"
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

// newDeleteTestServer 搭一个删除接口用的最小服务：一个真实库目录、一个真实索引、
// 真实缩略图缓存与事件 hub，不 mock 任何生产逻辑。
func newDeleteTestServer(t *testing.T, deleteMode string) (*Server, *store.Store, *thumbs.Manager, *events.Hub, *httptest.Server, string, string) {
	t.Helper()
	library := t.TempDir()
	dataDir := t.TempDir()
	index, err := store.Open(filepath.Join(dataDir, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { index.Close() })

	settings, err := config.Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	current := settings.Get()
	current.Libraries = []config.Library{{ID: "lib", Name: "lib", Path: library}}
	current.DeleteMode = deleteMode
	current.LogLevel = "error"
	if err := settings.Update(current); err != nil {
		t.Fatal(err)
	}

	log := logging.New("error", logging.NewRing(50))
	hub := events.NewHub()
	thumbMgr := thumbs.New(filepath.Join(dataDir, "thumbs"), settings, log)
	scanner := scan.New(index, settings, hub, log)
	tokens, err := LoadTokens(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	tokens.SetClaim("135790", 15*time.Minute)
	server := NewServer(Deps{
		Version: "test", APIVersion: 1, DataDir: dataDir, Index: index, Settings: settings,
		Thumbs: thumbMgr, Scanner: scanner, Hub: hub, Tokens: tokens, Logger: log,
		LogRing: logging.NewRing(10), StartedAt: time.Now(),
	})
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	return server, index, thumbMgr, hub, httpServer, library, pairDevice(t, httpServer.URL, "135790")
}

// indexTestEntry 把一个真实存在的文件登记进索引，返回它的内容 id。
func indexTestEntry(t *testing.T, index *store.Store, path, id, kind, dayKey string, extra store.Entry) {
	t.Helper()
	gen, err := index.BeginScan()
	if err != nil {
		t.Fatal(err)
	}
	entry := extra
	entry.Path = path
	entry.ID = id
	entry.Kind = kind
	entry.Name = filepath.Base(path)
	entry.DayKey = dayKey
	entry.Capture = 1_709_251_200
	entry.LastSeen = gen
	if err := index.UpsertMany([]store.Entry{entry}); err != nil {
		t.Fatal(err)
	}
	if _, err := index.FinishScan(gen); err != nil {
		t.Fatal(err)
	}
}

// deleteRequest 发一次删除请求并返回状态码与错误码。
func deleteRequest(t *testing.T, url, bearer string) (int, string) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodDelete, url, nil)
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
	var payload struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(buf.Bytes(), &payload)
	return response.StatusCode, payload.Error
}

// 删除成功：文件离开原位置、索引里立刻消失、缩略图缓存被清、事件按扫描的形状广播。
func TestDeleteItemPermanentSuccess(t *testing.T) {
	_, index, thumbMgr, hub, httpServer, library, bearer := newDeleteTestServer(t, config.DeleteModePermanent)
	eventsCh := hub.Subscribe()
	defer hub.Unsubscribe(eventsCh)

	photo := filepath.Join(library, "IMG_20240102_030405.jpg")
	writeTestJPEG(t, photo)
	indexTestEntry(t, index, photo, "permanent1", "image", "2024-01-02", store.Entry{})

	// 先让缩略图真的生成到缓存里，才能验证删除会把缓存清干净。
	if _, err := thumbMgr.Ensure(store.Entry{ID: "permanent1", Kind: "image", Path: photo}, 256); err != nil {
		t.Fatalf("prepare thumbnail: %v", err)
	}
	cacheFile := thumbMgr.CachePath("permanent1", 256)
	if _, err := os.Stat(cacheFile); err != nil {
		t.Fatalf("thumbnail not cached before delete: %v", err)
	}

	code, errCode := deleteRequest(t, httpServer.URL+"/api/v1/items/permanent1", bearer)
	if code != http.StatusOK || errCode != "" {
		t.Fatalf("delete status %d (%s)", code, errCode)
	}
	if _, err := os.Stat(photo); !os.IsNotExist(err) {
		t.Fatalf("file still on disk after permanent delete: %v", err)
	}
	if _, err := os.Stat(cacheFile); !os.IsNotExist(err) {
		t.Fatalf("thumbnail cache survived delete: %v", err)
	}
	if _, ok, _ := index.GetByID("permanent1"); ok {
		t.Fatal("entry still visible after delete")
	}
	// 原图接口不能还能读出已删除的条目。
	if code := get(t, httpServer.URL+"/api/v1/originals/permanent1", bearer); code != http.StatusNotFound {
		t.Fatalf("original must be gone, got %d", code)
	}

	// 事件形状与 scanner.go 的 library/changed{removed} 一致，客户端按同一条事件刷新。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case event := <-eventsCh:
			if event.Kind != "library/changed" {
				continue
			}
			payload, ok := event.Data.(map[string]any)
			if !ok {
				t.Fatalf("unexpected event payload: %+v", event.Data)
			}
			if _, present := payload["removed"]; !present {
				t.Fatalf("library/changed must carry removed count: %+v", payload)
			}
			return
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Fatal("no library/changed event after delete")
}

// 移动删除：文件进 <库根>/.itemory-trash 并保留目录结构，回收站里的文件不会被重新索引。
func TestDeleteItemMoveToTrashPreservesStructure(t *testing.T) {
	_, index, _, _, httpServer, library, bearer := newDeleteTestServer(t, config.DeleteModeTrash)
	nested := filepath.Join(library, "2024", "01")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	photo := filepath.Join(nested, "IMG_0001.jpg")
	writeTestJPEG(t, photo)
	indexTestEntry(t, index, photo, "trash1", "image", "2024-01-02", store.Entry{})

	code, errCode := deleteRequest(t, httpServer.URL+"/api/v1/items/trash1", bearer)
	if code != http.StatusOK || errCode != "" {
		t.Fatalf("trash status %d (%s)", code, errCode)
	}
	if _, err := os.Stat(photo); !os.IsNotExist(err) {
		t.Fatalf("file still at its original path: %v", err)
	}
	trashRoot := filepath.Join(library, trashDirName, "2024", "01")
	entries, err := os.ReadDir(trashRoot)
	if err != nil {
		t.Fatalf("trash directory missing: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected the file inside the trash directory, got %d entries", len(entries))
	}
	name := entries[0].Name()
	if !strings.HasSuffix(name, "_IMG_0001.jpg") {
		t.Fatalf("trashed file must keep its name with a timestamp prefix: %s", name)
	}
	if _, ok, _ := index.GetByID("trash1"); ok {
		t.Fatal("entry still visible after trashing")
	}
	// 同名文件再次删除不能覆盖回收站里已有的那一份。
	writeTestJPEG(t, photo)
	indexTestEntry(t, index, photo, "trash2", "image", "2024-01-02", store.Entry{})
	if code, errCode := deleteRequest(t, httpServer.URL+"/api/v1/items/trash2", bearer); code != http.StatusOK {
		t.Fatalf("second trash status %d (%s)", code, errCode)
	}
	entries, err = os.ReadDir(trashRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("trash must not overwrite an earlier file: %d entries", len(entries))
	}
}

// 删除模式为 off：接口 403，能力位不下发，而且绝不碰文件。
func TestDeleteItemDisabledBySettings(t *testing.T) {
	server, index, _, _, httpServer, library, bearer := newDeleteTestServer(t, config.DeleteModeOff)
	photo := filepath.Join(library, "keep.jpg")
	writeTestJPEG(t, photo)
	indexTestEntry(t, index, photo, "off1", "image", "2024-01-02", store.Entry{})

	// 能力位必须与接口实际行为一致：关掉之后客户端不该显示删除入口。
	_, data := getBody(t, httpServer.URL+"/api/v1/health", "")
	var health struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(data, &health); err != nil {
		t.Fatal(err)
	}
	if contains(health.Capabilities, "delete") {
		t.Fatalf("delete capability must not be advertised when deleteMode=off: %v", health.Capabilities)
	}

	code, errCode := deleteRequest(t, httpServer.URL+"/api/v1/items/off1", bearer)
	if code != http.StatusForbidden || errCode != errCodeDeleteDisabled {
		t.Fatalf("expected 403 %s, got %d %s", errCodeDeleteDisabled, code, errCode)
	}
	if _, err := os.Stat(photo); err != nil {
		t.Fatalf("file must survive a rejected delete: %v", err)
	}
	if _, ok, _ := index.GetByID("off1"); !ok {
		t.Fatal("index must be untouched when delete is disabled")
	}

	// 打开删除后同名接口立即可用，且能力位随之出现。
	current := server.d.Settings.Get()
	current.DeleteMode = config.DeleteModePermanent
	if err := server.d.Settings.Update(current); err != nil {
		t.Fatal(err)
	}
	_, data = getBody(t, httpServer.URL+"/api/v1/health", "")
	health.Capabilities = nil
	if err := json.Unmarshal(data, &health); err != nil {
		t.Fatal(err)
	}
	if !contains(health.Capabilities, "delete") {
		t.Fatalf("delete capability missing after enabling: %v", health.Capabilities)
	}
	if code, errCode := deleteRequest(t, httpServer.URL+"/api/v1/items/off1", bearer); code != http.StatusOK {
		t.Fatalf("delete after enabling failed: %d %s", code, errCode)
	}
}

// 未知 id 必须 404，且错误码与读接口一致。
func TestDeleteItemUnknownIDReturnsNotFound(t *testing.T) {
	_, _, _, _, httpServer, _, bearer := newDeleteTestServer(t, config.DeleteModePermanent)
	code, errCode := deleteRequest(t, httpServer.URL+"/api/v1/items/missing-id", bearer)
	if code != http.StatusNotFound || errCode != errCodeNotFound {
		t.Fatalf("expected 404 %s, got %d %s", errCodeNotFound, code, errCode)
	}
}

// 索引里残留的条目若路径已经不在任何媒体库内（库被移除 / 规则改过），删除必须拒绝：
// 这是路径越界的拒绝分支，判据来自已经存在的 withinLibraries，不另写一套。
func TestDeleteItemOutsideLibraryIsRejected(t *testing.T) {
	server, index, _, _, httpServer, library, bearer := newDeleteTestServer(t, config.DeleteModePermanent)

	// 条目在库外，但仍被索引记着：取 settings 的当前库列表之后把库换掉，
	// 模拟「媒体库配置改了，索引里还留着旧路径」。
	outside := t.TempDir()
	photo := filepath.Join(outside, "outside.jpg")
	writeTestJPEG(t, photo)
	indexTestEntry(t, index, photo, "outside1", "image", "2024-01-02", store.Entry{})

	// 顺手确认这条路径确实不在当前库内，用例本身不依赖假设。
	current := server.d.Settings.Get()
	if withinLibraries(filepath.Clean(photo), current.Libraries) {
		t.Fatalf("test fixture error: %s is inside the library", photo)
	}

	code, errCode := deleteRequest(t, httpServer.URL+"/api/v1/items/outside1", bearer)
	if code != http.StatusForbidden || errCode != errCodeOutsideLibrary {
		t.Fatalf("expected 403 %s, got %d %s", errCodeOutsideLibrary, code, errCode)
	}
	if _, err := os.Stat(photo); err != nil {
		t.Fatalf("file outside the library must not be touched: %v", err)
	}
	_ = library
}

// 符号链接指向库外：字符串校验能过，解析校验必须拦下，且链接与目标都不能被删。
func TestDeleteItemSymlinkEscapeIsRejected(t *testing.T) {
	_, index, _, _, httpServer, library, bearer := newDeleteTestServer(t, config.DeleteModePermanent)
	outside := t.TempDir()
	target := filepath.Join(outside, "target.jpg")
	writeTestJPEG(t, target)
	link := filepath.Join(library, "link.jpg")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	indexTestEntry(t, index, link, "link1", "image", "2024-01-02", store.Entry{})

	code, errCode := deleteRequest(t, httpServer.URL+"/api/v1/items/link1", bearer)
	if code != http.StatusForbidden || errCode != errCodeOutsideLibrary {
		t.Fatalf("expected 403 %s for a symlink escape, got %d %s", errCodeOutsideLibrary, code, errCode)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("symlink target outside the library must not be deleted: %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("the symlink itself must be left alone: %v", err)
	}
}

// 只读挂载：目录不可写时必须在动文件之前拒绝，返回 403 read_only_mount。
func TestDeleteItemReadOnlyMountIsRejected(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("以 root 运行时权限位不起作用，无法构造只读目录")
	}
	_, index, _, _, httpServer, library, bearer := newDeleteTestServer(t, config.DeleteModePermanent)
	locked := filepath.Join(library, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	photo := filepath.Join(locked, "read-only.jpg")
	writeTestJPEG(t, photo)
	// 文件准备好之后再收紧目录权限：构造「目录不可写、文件本身可读」的只读挂载形态。
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	indexTestEntry(t, index, photo, "ro1", "image", "2024-01-02", store.Entry{})

	code, errCode := deleteRequest(t, httpServer.URL+"/api/v1/items/ro1", bearer)
	if code != http.StatusForbidden || errCode != errCodeReadOnlyMount {
		t.Fatalf("expected 403 %s, got %d %s", errCodeReadOnlyMount, code, errCode)
	}
	if _, err := os.Stat(photo); err != nil {
		t.Fatalf("file in a read-only directory must survive: %v", err)
	}
	if _, ok, _ := index.GetByID("ro1"); !ok {
		t.Fatal("index must be untouched when the mount is read-only")
	}

	// trash 模式要写回收站目录，只读库根同样要拒绝，并且不能留下半建的目录。
	serverRootReadOnly(t, httpServer, bearer, index, library)
}

// serverRootReadOnly 把库根改成只读后重复一次 trash 删除，确认回收站不会被建出来。
func serverRootReadOnly(t *testing.T, httpServer *httptest.Server, bearer string, index *store.Store, library string) {
	t.Helper()
	photo := filepath.Join(library, "trash-needs-write.jpg")
	writeTestJPEG(t, photo)
	indexTestEntry(t, index, photo, "ro2", "image", "2024-01-02", store.Entry{})
	// 通过设置切到 trash 模式。
	if code := patchDeleteMode(t, httpServer.URL, bearer, config.DeleteModeTrash); code != http.StatusOK {
		t.Fatalf("patch deleteMode: %d", code)
	}
	if err := os.Chmod(library, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(library, 0o755) })

	code, errCode := deleteRequest(t, httpServer.URL+"/api/v1/items/ro2", bearer)
	if code != http.StatusForbidden || errCode != errCodeReadOnlyMount {
		t.Fatalf("trash into a read-only library: expected 403 %s, got %d %s", errCodeReadOnlyMount, code, errCode)
	}
	if _, err := os.Stat(filepath.Join(library, trashDirName)); !os.IsNotExist(err) {
		t.Fatalf("trash directory must not be created when the library is read-only: %v", err)
	}
	if _, err := os.Stat(photo); err != nil {
		t.Fatalf("file must survive a rejected trash move: %v", err)
	}
}

// patchDeleteMode 走控制台用的 PATCH 白名单修改删除模式。
func patchDeleteMode(t *testing.T, baseURL, bearer, mode string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"deleteMode": mode})
	request, _ := http.NewRequest(http.MethodPatch, baseURL+"/api/v1/settings", bytes.NewReader(body))
	request.Header.Set("Authorization", bearer)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	return response.StatusCode
}

// 动态照片：删静帧必须同时删掉片段，否则盘上会留下一份谁也看不到的孤立文件。
func TestDeleteItemRemovesPairedMotionClip(t *testing.T) {
	_, index, _, _, httpServer, library, bearer := newDeleteTestServer(t, config.DeleteModePermanent)
	still := filepath.Join(library, "IMG_0002.HEIC")
	clip := filepath.Join(library, "IMG_0002.MOV")
	if err := os.WriteFile(still, []byte("still"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clip, []byte("clip"), 0o644); err != nil {
		t.Fatal(err)
	}
	gen, err := index.BeginScan()
	if err != nil {
		t.Fatal(err)
	}
	if err := index.UpsertMany([]store.Entry{
		{Path: still, ID: "motion1", Kind: "motion", Name: "IMG_0002.HEIC", Capture: 1_709_251_200,
			DayKey: "2024-01-02", MotionPath: clip, LastSeen: gen},
		{Path: clip, ID: "clip1", Kind: "motion-clip", Name: "IMG_0002.MOV", Capture: 1_709_251_200,
			DayKey: "2024-01-02", LastSeen: gen},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := index.FinishScan(gen); err != nil {
		t.Fatal(err)
	}

	if code, errCode := deleteRequest(t, httpServer.URL+"/api/v1/items/motion1", bearer); code != http.StatusOK {
		t.Fatalf("delete live photo: %d %s", code, errCode)
	}
	for _, gone := range []string{still, clip} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s must be removed together with the live photo: %v", gone, err)
		}
	}
	if _, ok, _ := index.GetByID("clip1"); ok {
		t.Fatal("the motion clip row must be hidden too")
	}
}

// 未鉴权的删除请求不能到达 handler：与其它 /api/v1 读接口同一套 Bearer 语义。
func TestDeleteItemRequiresDeviceToken(t *testing.T) {
	_, index, _, _, httpServer, library, bearer := newDeleteTestServer(t, config.DeleteModePermanent)
	photo := filepath.Join(library, "auth.jpg")
	writeTestJPEG(t, photo)
	indexTestEntry(t, index, photo, "auth1", "image", "2024-01-02", store.Entry{})
	if code, _ := deleteRequest(t, httpServer.URL+"/api/v1/items/auth1", ""); code != http.StatusUnauthorized {
		t.Fatalf("delete without a token must be 401, got %d", code)
	}
	if _, err := os.Stat(photo); err != nil {
		t.Fatalf("unauthenticated delete must not touch the file: %v", err)
	}
	_ = bearer
}

// 老版本 App 整份 PUT 设置时不认识 deleteMode：服务端必须沿用当前值，
// 不能让一次「保存设置」把用户在控制台开启的删除模式静默关掉。
func TestPutSettingsPreservesDeleteModeWhenOmitted(t *testing.T) {
	server, _, _, _, httpServer, library, bearer := newDeleteTestServer(t, config.DeleteModeTrash)

	// 模拟老客户端：整份回传，payload 里没有 deleteMode 键。
	putJSON(t, httpServer.URL+"/api/v1/settings", bearer, map[string]any{
		"schemaVersion":           1,
		"preset":                  "balanced",
		"libraries":               []map[string]string{{"id": "lib", "name": "lib", "path": library}},
		"excludePatterns":         []string{"@eaDir"},
		"concurrency":             2,
		"thumbSize":               512,
		"thumbCacheLimitBytes":    1 << 30,
		"originalCacheLimitBytes": 1 << 30,
		"nightlyThumbBudget":      100,
		"transcode":               "off",
		"motionPhoto":             true,
		"rawPreview":              true,
		"scanSchedule":            "",
		"logLevel":                "error",
	})
	if got := server.d.Settings.Get().DeleteMode; got != config.DeleteModeTrash {
		t.Fatalf("deleteMode must survive a legacy full PUT: %q", got)
	}

	// 显式传 deleteMode 时仍然按传入值生效（包括关掉删除）。
	putJSON(t, httpServer.URL+"/api/v1/settings", bearer, map[string]any{
		"schemaVersion":           1,
		"preset":                  "balanced",
		"libraries":               []map[string]string{{"id": "lib", "name": "lib", "path": library}},
		"excludePatterns":         []string{"@eaDir"},
		"concurrency":             2,
		"thumbSize":               512,
		"thumbCacheLimitBytes":    1 << 30,
		"originalCacheLimitBytes": 1 << 30,
		"nightlyThumbBudget":      100,
		"transcode":               "off",
		"motionPhoto":             true,
		"rawPreview":              true,
		"scanSchedule":            "",
		"logLevel":                "error",
		"deleteMode":              config.DeleteModeOff,
	})
	if got := server.d.Settings.Get().DeleteMode; got != config.DeleteModeOff {
		t.Fatalf("explicit deleteMode must be applied: %q", got)
	}
}
