package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/SherlockGougou/itemory-agent/internal/config"
)

// requestWithCookie 发一个带管理员会话 cookie 的请求。
// 现有的 getBody / postJSON 只支持 Bearer，而这一批要验证的恰恰是
// 「cookie 会话能不能操作控制面」。
func requestWithCookie(t *testing.T, method, url string, cookie *http.Cookie, payload any) (int, []byte) {
	t.Helper()
	var body *bytes.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	} else {
		body = bytes.NewReader(nil)
	}
	request, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
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

// controlPlaneCases 是这一批从「只认 App 令牌」放开给管理员会话的接口。
// 它们的共同点是：登录了管理员的控制台必须能用，而匿名访问必须被拒。
//
// /api/v1/events 不在这里：它是 SSE，读完响应体会永远阻塞，单独测。
var controlPlaneCases = []struct {
	method string
	path   string
	admin  int
}{
	{http.MethodGet, "/api/v1/settings", http.StatusOK},
	{http.MethodGet, "/api/v1/scan/status", http.StatusOK},
	// cancel 必须排在 scan 之前：一轮扫描是异步起的，先取消能保证此刻确实空闲，
	// 否则这个用例会随调度竞态而偶发失败。
	{http.MethodPost, "/api/v1/scan/cancel", http.StatusConflict},
	{http.MethodPost, "/api/v1/scan", http.StatusAccepted},
	{http.MethodGet, "/api/v1/logs", http.StatusOK},
	{http.MethodGet, "/api/v1/diagnostics", http.StatusOK},
	{http.MethodDelete, "/api/v1/cache/thumbs", http.StatusOK},
}

func TestControlPlaneRejectsAnonymous(t *testing.T) {
	agent := newPairingTestServer(t)
	for _, tc := range controlPlaneCases {
		status, _ := requestWithCookie(t, tc.method, agent.server.URL+tc.path, nil, nil)
		if status != http.StatusUnauthorized {
			t.Fatalf("%s %s without credentials: want 401, got %d", tc.method, tc.path, status)
		}
	}
}

func TestControlPlaneAcceptsAdminSession(t *testing.T) {
	agent := newPairingTestServer(t)
	for _, tc := range controlPlaneCases {
		var payload any
		if tc.method == http.MethodPost || tc.method == http.MethodPut {
			payload = map[string]any{}
		}
		status, body := requestWithCookie(t, tc.method, agent.server.URL+tc.path, agent.adminCookie, payload)
		if status == http.StatusUnauthorized {
			t.Fatalf("%s %s with admin session: still 401 (body %s)", tc.method, tc.path, body)
		}
		if status != tc.admin {
			t.Fatalf("%s %s with admin session: want %d, got %d (body %s)", tc.method, tc.path, tc.admin, status, body)
		}
	}
}

// TestEventsStreamAcceptsAdminSession 只验握手，不读 body：
// SSE 在写完 hello 事件后不会结束，按常规方式读响应体会挂住测试。
func TestEventsStreamAcceptsAdminSession(t *testing.T) {
	agent := newPairingTestServer(t)

	anonymous, err := http.Get(agent.server.URL + "/api/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	anonymous.Body.Close()
	if anonymous.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous events: want 401, got %d", anonymous.StatusCode)
	}

	request, _ := http.NewRequest(http.MethodGet, agent.server.URL+"/api/v1/events", nil)
	request.AddCookie(agent.adminCookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("admin events: want 200, got %d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "text/event-stream" {
		t.Fatalf("admin events content-type %q", contentType)
	}
}

// TestAppTokenStillReachesControlPlane 守住回退：放开管理员会话不能影响
// 已有的 App 令牌通道，否则线上客户端会一起失效。
func TestAppTokenStillReachesControlPlane(t *testing.T) {
	agent := newPairingTestServer(t)
	window := agent.openPairingWindow(t)

	status, body := requestWithCookie(t, http.MethodPost, agent.server.URL+"/api/v1/pair", nil, map[string]any{
		"token":      window.code,
		"deviceName": "回归测试设备",
		"platform":   "ios",
	})
	if status != http.StatusOK {
		t.Fatalf("pair status %d: %s", status, body)
	}
	var paired struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &paired); err != nil {
		t.Fatal(err)
	}
	bearer := "Bearer " + paired.Token

	// 先确认 App 令牌在公开接口上可用（原本就如此）。
	if code := get(t, agent.server.URL+"/api/v1/stats", bearer); code != http.StatusOK {
		t.Fatalf("app token on /stats: want 200, got %d", code)
	}
	// 再确认它在放开后的控制面接口上同样可用。
	if code := get(t, agent.server.URL+"/api/v1/scan/status", bearer); code != http.StatusOK {
		t.Fatalf("app token on /scan/status: want 200, got %d", code)
	}
	// 无效令牌仍然被拒。
	if code := get(t, agent.server.URL+"/api/v1/scan/status", "Bearer deadbeef"); code != http.StatusUnauthorized {
		t.Fatalf("bogus token: want 401, got %d", code)
	}
}

// TestPairRecordsPlatform 覆盖 F3：设备平台由客户端在配对时上报，
// 认不出来的取值会被丢弃而不是原样存下来（这个字段会被控制台直接显示）。
func TestPairRecordsPlatform(t *testing.T) {
	agent := newPairingTestServer(t)

	window := agent.openPairingWindow(t)
	if status, body := requestWithCookie(t, http.MethodPost, agent.server.URL+"/api/v1/pair", nil, map[string]any{
		"token": window.code, "deviceName": "iPad", "platform": "iPadOS",
	}); status != http.StatusOK {
		t.Fatalf("pair status %d: %s", status, body)
	}

	// 老客户端不传 platform 也必须配对成功。
	// 配对码有 1 秒限流（claimLimiter），所以要等过窗口才能再开一次。
	time.Sleep(1100 * time.Millisecond)
	window = agent.openPairingWindow(t)
	if status, body := requestWithCookie(t, http.MethodPost, agent.server.URL+"/api/v1/pair", nil, map[string]any{
		"token": window.code, "deviceName": "旧版客户端",
	}); status != http.StatusOK {
		t.Fatalf("pair without platform status %d: %s", status, body)
	}

	_, body := requestWithCookie(t, http.MethodGet, agent.server.URL+"/api/v1/tokens", agent.adminCookie, nil)
	var payload struct {
		Tokens []struct {
			Name     string `json:"name"`
			Platform string `json:"platform"`
			Hash     string `json:"hash"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Tokens) != 2 {
		t.Fatalf("want 2 paired devices, got %d", len(payload.Tokens))
	}
	platforms := map[string]string{}
	for _, token := range payload.Tokens {
		platforms[token.Name] = token.Platform
		if token.Hash != "" {
			t.Fatalf("token hash must not be exposed: %q", token.Hash)
		}
	}
	if platforms["iPad"] != "ipados" {
		t.Fatalf("platform not normalized: %q", platforms["iPad"])
	}
	if platforms["旧版客户端"] != "" {
		t.Fatalf("missing platform should stay empty, got %q", platforms["旧版客户端"])
	}
}

// TestOverviewRequiresAdminSession 守住分层：管理面聚合只能由管理员会话读。
func TestOverviewRequiresAdminSession(t *testing.T) {
	agent := newPairingTestServer(t)

	if status, _ := getBody(t, agent.server.URL+"/api/v1/admin/overview", ""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous overview: want 401, got %d", status)
	}

	status, body := requestWithCookie(t, http.MethodGet, agent.server.URL+"/api/v1/admin/overview", agent.adminCookie, nil)
	if status != http.StatusOK {
		t.Fatalf("admin overview status %d: %s", status, body)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"devices", "health", "schedule", "libraries", "settings"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("overview is missing %q", key)
		}
	}
	health, _ := payload["health"].(map[string]any)
	if health == nil {
		t.Fatal("health block is not an object")
	}
	checks, _ := health["checks"].([]any)
	if len(checks) == 0 {
		t.Fatal("health.checks is empty")
	}
}

// TestPublicDashboardDoesNotLeakAdminFields 守住 R3：
// 匿名可读的 dashboard 不得混入设备、健康判定等管理面字段。
func TestPublicDashboardDoesNotLeakAdminFields(t *testing.T) {
	agent := newPairingTestServer(t)

	status, body := getBody(t, agent.server.URL+"/api/v1/dashboard", "")
	if status != http.StatusOK {
		t.Fatalf("dashboard status %d", status)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"devices", "tokens", "health", "schedule"} {
		if _, leaked := payload[key]; leaked {
			t.Fatalf("public dashboard leaked %q", key)
		}
	}
	// 反向确认它确实还在提供原有字段，避免「因为整个 handler 挂了所以没泄漏」。
	for _, key := range []string{"service", "libraries", "index", "cache", "tools", "pairing"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("public dashboard lost %q", key)
		}
	}
}

// TestPatchSettingsKeepsUnmanagedFields 是 F12 的核心回归：
// Update() 是整份替换，而网页端只白名单地改几个字段——
// 一旦这里失守，用户的媒体库列表会被一次「改日志级别」清空。
func TestPatchSettingsKeepsUnmanagedFields(t *testing.T) {
	agent := newPairingTestServer(t)

	seeded := config.Defaults()
	seeded.Libraries = []config.Library{{ID: "lib-photos", Name: "家庭照片", Path: t.TempDir()}}
	seeded.ExcludePatterns = []string{"@eaDir", ".Trash"}
	seeded.Concurrency = 3
	if err := agent.settings.Update(seeded); err != nil {
		t.Fatal(err)
	}

	status, body := requestWithCookie(t, http.MethodPatch, agent.server.URL+"/api/v1/settings", agent.adminCookie, map[string]any{
		"logLevel": "debug",
	})
	if status != http.StatusOK {
		t.Fatalf("patch status %d: %s", status, body)
	}

	var got config.Settings
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.LogLevel != "debug" {
		t.Fatalf("logLevel not applied: %q", got.LogLevel)
	}
	if len(got.Libraries) != 1 || got.Libraries[0].ID != "lib-photos" {
		t.Fatalf("libraries were clobbered: %+v", got.Libraries)
	}
	if len(got.ExcludePatterns) != 2 {
		t.Fatalf("excludePatterns were clobbered: %+v", got.ExcludePatterns)
	}
	if got.Concurrency != 3 {
		t.Fatalf("concurrency was clobbered: %d", got.Concurrency)
	}
}

// TestPatchSettingsRejectsUnknownLevel 确认非法取值不会静默写进去。
func TestPatchSettingsRejectsUnknownLevel(t *testing.T) {
	agent := newPairingTestServer(t)

	status, _ := requestWithCookie(t, http.MethodPatch, agent.server.URL+"/api/v1/settings", agent.adminCookie, map[string]any{
		"logLevel": "trace",
	})
	// Validate() 会把非法级别归一化成 info，而不是报错——确认归一化生效即可。
	if status != http.StatusOK {
		t.Fatalf("patch status %d", status)
	}
	if agent.settings.Get().LogLevel != "info" {
		t.Fatalf("invalid level should normalize to info, got %q", agent.settings.Get().LogLevel)
	}
}

// TestScanCancelOnIdleScanner 覆盖 F2 的边界：空闲时取消是幂等的 no-op。
func TestScanCancelOnIdleScanner(t *testing.T) {
	agent := newPairingTestServer(t)

	status, body := requestWithCookie(t, http.MethodPost, agent.server.URL+"/api/v1/scan/cancel", agent.adminCookie, map[string]any{})
	if status != http.StatusConflict {
		t.Fatalf("cancel on idle scanner: want 409, got %d (%s)", status, body)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["error"] != "not_running" {
		t.Fatalf("unexpected error code %v", payload["error"])
	}
}

// TestLogsLevelFilter 覆盖 F4：级别与关键字过滤，以及非法级别的处理。
func TestLogsLevelFilter(t *testing.T) {
	agent := newPairingTestServer(t)
	log := agent.ring

	// 直接往 ring 里塞 slog 风格的行，避免依赖真实日志的输出时机。
	for _, line := range []string{
		`{"time":"2026-09-21T10:00:00Z","level":"DEBUG","msg":"http request","path":"/api/v1/dashboard"}`,
		`{"time":"2026-09-21T10:00:01Z","level":"INFO","msg":"scan started","mode":"incremental"}`,
		`{"time":"2026-09-21T10:00:02Z","level":"WARN","msg":"library unreadable","path":"/vol2/Archive"}`,
		`{"time":"2026-09-21T10:00:03Z","level":"ERROR","msg":"scan failed","error":"permission denied"}`,
	} {
		_, _ = log.Write([]byte(line + "\n"))
	}

	read := func(query string) []string {
		t.Helper()
		status, body := requestWithCookie(t, http.MethodGet, agent.server.URL+"/api/v1/logs"+query, agent.adminCookie, nil)
		if status != http.StatusOK {
			t.Fatalf("logs%s status %d", query, status)
		}
		var payload struct {
			Lines []string `json:"lines"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		return payload.Lines
	}

	if got := read(""); len(got) != 4 {
		t.Fatalf("no filter: want 4 lines, got %d", len(got))
	}
	if got := read("?level=warn"); len(got) != 2 {
		t.Fatalf("level=warn: want 2 lines, got %d (%v)", len(got), got)
	}
	if got := read("?level=error"); len(got) != 1 {
		t.Fatalf("level=error: want 1 line, got %d", len(got))
	}
	if got := read("?q=unreadable"); len(got) != 1 {
		t.Fatalf("q=unreadable: want 1 line, got %d", len(got))
	}
	if got := read("?level=warn&q=vol2"); len(got) != 1 {
		t.Fatalf("level+q combined: want 1 line, got %d", len(got))
	}
	if got := read("?tail=2"); len(got) != 2 {
		t.Fatalf("tail=2: want 2 lines, got %d", len(got))
	}

	// 非法级别要报错，而不是当成「不过滤」静默返回全部。
	if status, _ := requestWithCookie(t, http.MethodGet, agent.server.URL+"/api/v1/logs?level=trace", agent.adminCookie, nil); status != http.StatusBadRequest {
		t.Fatalf("level=trace: want 400, got %d", status)
	}
}
