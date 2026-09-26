package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// testAgent is a running agent plus the handles a test needs to poke at it.
type testAgent struct {
	server      *httptest.Server
	tokens      *TokenStore
	settings    *config.Manager
	ring        *logging.Ring
	adminCookie *http.Cookie
}

func newPairingTestServer(t *testing.T) *testAgent {
	t.Helper()
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
	ring := logging.NewRing(50)
	log := logging.New("error", ring)
	hub := events.NewHub()
	thumbMgr := thumbs.New(filepath.Join(dataDir, "thumbs"), settings, log)
	scanner := scan.New(index, settings, hub, log)
	tokens, err := LoadTokens(dataDir)
	if err != nil {
		t.Fatal(err)
	}

	server := NewServer(Deps{
		Version: "test", APIVersion: 1, DataDir: dataDir, Index: index, Settings: settings,
		Thumbs: thumbMgr, Scanner: scanner, Hub: hub, Tokens: tokens, Logger: log,
		LogRing: ring, StartedAt: time.Now(),
	})
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	return &testAgent{server: httpServer, tokens: tokens, settings: settings, ring: ring, adminCookie: configureTestAdmin(t, server)}
}

// claimWindow is one armed pairing window as reported by the dashboard API.
type claimWindow struct {
	code             string
	expiresInSeconds int
	hostCandidates   []string
}

func (a *testAgent) openPairingWindow(t *testing.T) claimWindow {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, a.server.URL+"/api/v1/claim", nil)
	request.AddCookie(a.adminCookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("claim arm status %d", response.StatusCode)
	}
	var payload struct {
		Code             string   `json:"code"`
		ExpiresInSeconds int      `json:"expiresInSeconds"`
		HostCandidates   []string `json:"hostCandidates"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Code) != 6 {
		t.Fatalf("unexpected pairing code %q", payload.Code)
	}
	return claimWindow{
		code:             payload.Code,
		expiresInSeconds: payload.ExpiresInSeconds,
		hostCandidates:   payload.HostCandidates,
	}
}

// serverPort reports the port the test server listens on.
func (a *testAgent) serverPort(t *testing.T) string {
	t.Helper()
	parsed, err := url.Parse(a.server.URL)
	if err != nil || parsed.Port() == "" {
		t.Fatalf("cannot determine test server port from %s", a.server.URL)
	}
	return parsed.Port()
}

func (a *testAgent) claimInfo(t *testing.T) (int, map[string]any) {
	t.Helper()
	code, data := getBody(t, a.server.URL+"/api/v1/claim", "")
	var payload map[string]any
	if code == http.StatusOK {
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatal(err)
		}
	}
	return code, payload
}

func (a *testAgent) qrSVG(t *testing.T, headers map[string]string) (int, string, []byte) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, a.server.URL+"/api/v1/claim/qr.svg", nil)
	request.AddCookie(a.adminCookie)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(response.Body)
	return response.StatusCode, response.Header.Get("Content-Type"), buf.Bytes()
}

func (a *testAgent) qrSVGWithQuery(t *testing.T, query string) (int, string, []byte) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, a.server.URL+"/api/v1/claim/qr.svg?"+query, nil)
	request.AddCookie(a.adminCookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(response.Body)
	return response.StatusCode, response.Header.Get("Content-Type"), buf.Bytes()
}

// TestPairingWindowAndQRCode covers the dashboard pairing flow: the window is
// closed until the dashboard arms it, and the QR payload follows the address
// the page was opened with.
func TestPairingWindowAndQRCode(t *testing.T) {
	agent := newPairingTestServer(t)

	unauthorizedArm, err := http.NewRequest(http.MethodPost, agent.server.URL+"/api/v1/claim", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(unauthorizedArm)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous claim arm must be rejected, got %d", response.StatusCode)
	}
	unauthorizedQR, err := http.NewRequest(http.MethodGet, agent.server.URL+"/api/v1/claim/qr.svg", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.DefaultClient.Do(unauthorizedQR)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous QR access must be rejected, got %d", response.StatusCode)
	}

	if code, payload := agent.claimInfo(t); code != http.StatusOK || payload["claimArmed"] != false {
		t.Fatalf("expected a closed window, got %d %+v", code, payload)
	}
	if code, _, _ := agent.qrSVG(t, nil); code != http.StatusConflict {
		t.Fatalf("expected 409 while disarmed, got %d", code)
	}

	window := agent.openPairingWindow(t)
	code := window.code
	if window.expiresInSeconds != int(ClaimTTL.Seconds()) {
		t.Fatalf("unexpected window length %d", window.expiresInSeconds)
	}

	// The public claim endpoint must report the window without leaking the code.
	status, payload := agent.claimInfo(t)
	if status != http.StatusOK || payload["claimArmed"] != true {
		t.Fatalf("expected an armed window, got %d %+v", status, payload)
	}
	if _, present := payload["code"]; present {
		t.Fatalf("claim info leaked the pairing code: %+v", payload)
	}
	if payload["pairedDevices"] != float64(0) {
		t.Fatalf("unexpected paired devices %+v", payload["pairedDevices"])
	}

	// The QR encodes the address the browser used, preferring proxy headers.
	status, contentType, body := agent.qrSVG(t, map[string]string{
		"X-Forwarded-Host":  "192.168.1.10:9999",
		"X-Forwarded-Proto": "https",
	})
	if status != http.StatusOK || !strings.HasPrefix(contentType, "image/svg+xml") {
		t.Fatalf("qr status %d content-type %q", status, contentType)
	}
	expectedPayload := pairPayload("https://192.168.1.10:9999/", code, agent.tokens.ServerID(), 1)
	if !strings.Contains(expectedPayload, "itemory://pair?") {
		t.Fatalf("payload scheme drifted: %s", expectedPayload)
	}
	expectedSVG, err := qrSVG(expectedPayload)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != expectedSVG {
		t.Fatalf("qr payload mismatch:\nwant %s\n got %s", expectedSVG[:64], string(body)[:64])
	}
}

// TestPairingWindowAddressOverride covers the dashboard's LAN address picker:
// a page opened over localhost can still hand the phone a reachable address.
func TestPairingWindowAddressOverride(t *testing.T) {
	agent := newPairingTestServer(t)
	window := agent.openPairingWindow(t)
	code := window.code
	candidates := window.hostCandidates

	// The test server is reached over 127.0.0.1, so the host itself is useless
	// for a phone and must not be offered as a candidate.
	for _, candidate := range candidates {
		if strings.HasPrefix(candidate, "127.0.0.1:") {
			t.Fatalf("loopback address offered as a QR candidate: %v", candidates)
		}
	}

	status, _, body := agent.qrSVG(t, nil)
	if status != http.StatusOK {
		t.Fatalf("qr status %d", status)
	}
	loopbackSVG, err := qrSVG(pairPayload("http://"+strings.TrimPrefix(agent.server.URL, "http://")+"/", code, agent.tokens.ServerID(), 1))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != loopbackSVG {
		t.Fatal("default QR should encode the request host")
	}

	request, _ := http.NewRequest(http.MethodGet, agent.server.URL+"/api/v1/claim/qr.svg?host=192.168.1.10&port=8787", nil)
	request.AddCookie(agent.adminCookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(response.Body)
	overridden, err := qrSVG(pairPayload("http://192.168.1.10:8787/", code, agent.tokens.ServerID(), 1))
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != overridden {
		t.Fatal("host override was not applied to the QR payload")
	}
}

// TestPairingWindowCandidatesPreferRequestHost covers the address picker rule:
// when the page was opened from another device, that address is the only one
// worth offering (container-internal IPs are unreachable from the phone).
func TestPairingWindowCandidatesPreferRequestHost(t *testing.T) {
	agent := newPairingTestServer(t)

	request, _ := http.NewRequest(http.MethodPost, agent.server.URL+"/api/v1/claim", nil)
	request.AddCookie(agent.adminCookie)
	request.Header.Set("X-Forwarded-Host", "192.168.1.10:8787")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload struct {
		HostCandidates []string `json:"hostCandidates"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.HostCandidates) != 1 || payload.HostCandidates[0] != "192.168.1.10:8787" {
		t.Fatalf("expected the request host to be the only candidate, got %v", payload.HostCandidates)
	}

	// A bare-IP override keeps the port used on this request, a hostname does not.
	agent.tokens.SetClaim("135790", ClaimTTL)
	_, _, body := agent.qrSVGWithQuery(t, "host=192.168.50.7")
	expected, err := qrSVG(pairPayload("http://192.168.50.7:"+agent.serverPort(t)+"/", "135790", agent.tokens.ServerID(), 1))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != expected {
		t.Fatal("bare IP override did not inherit the request port")
	}

	_, _, body = agent.qrSVGWithQuery(t, "host=nas.example.com&scheme=https")
	expected, err = qrSVG(pairPayload("https://nas.example.com/", "135790", agent.tokens.ServerID(), 1))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != expected {
		t.Fatal("hostname override should be used verbatim")
	}
}

// TestPairingWindowRotationDisarmAndRateLimit covers rotation, closing and the
// per-caller throttle on the administrator pairing endpoints.
func TestPairingWindowRotationDisarmAndRateLimit(t *testing.T) {
	agent := newPairingTestServer(t)
	first := agent.openPairingWindow(t).code

	// Re-arming within the throttle interval is rejected...
	request, _ := http.NewRequest(http.MethodPost, agent.server.URL+"/api/v1/claim", nil)
	request.AddCookie(agent.adminCookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for a rapid re-arm, got %d", response.StatusCode)
	}

	// ...once the interval elapsed, rotating mints a fresh code...
	time.Sleep(1100 * time.Millisecond)
	second := agent.openPairingWindow(t).code
	if second == first {
		t.Fatal("rotating should mint a new pairing code")
	}

	// ...and closing the window uses its own bucket, so it works immediately.
	request, _ = http.NewRequest(http.MethodDelete, agent.server.URL+"/api/v1/claim", nil)
	request.AddCookie(agent.adminCookie)
	closeResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	closeResponse.Body.Close()
	if closeResponse.StatusCode != http.StatusOK {
		t.Fatalf("close status %d", closeResponse.StatusCode)
	}
	if code, payload := agent.claimInfo(t); code != http.StatusOK || payload["claimArmed"] != false {
		t.Fatalf("window stayed open: %+v", payload)
	}
	if code, _, _ := agent.qrSVG(t, nil); code != http.StatusConflict {
		t.Fatalf("expected 409 after closing, got %d", code)
	}
}

// TestPairingWindowSingleUseAndExpiry covers the one-shot and TTL semantics
// promised to the user.
func TestPairingWindowSingleUseAndExpiry(t *testing.T) {
	agent := newPairingTestServer(t)
	code := agent.openPairingWindow(t).code

	bearer := pairDevice(t, agent.server.URL, code)
	if bearer == "" {
		t.Fatal("pairing did not return a bearer token")
	}
	if status, payload := agent.claimInfo(t); status != http.StatusOK || payload["claimArmed"] != false {
		t.Fatalf("window survived a successful pair: %+v", payload)
	}
	if status, _, _ := agent.qrSVG(t, nil); status != http.StatusConflict {
		t.Fatalf("expected 409 after pairing, got %d", status)
	}
	if status := get(t, agent.server.URL+"/api/v1/diagnostics", bearer); status != http.StatusOK {
		t.Fatalf("paired token rejected: %d", status)
	}
	// A normal App bearer token may read media but cannot manage pairing or
	// enumerate/revoke other devices.
	adminAttempt, err := http.NewRequest(http.MethodPost, agent.server.URL+"/api/v1/claim", nil)
	if err != nil {
		t.Fatal(err)
	}
	adminAttempt.Header.Set("Authorization", bearer)
	response, err := http.DefaultClient.Do(adminAttempt)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("App bearer token must not arm pairing, got %d", response.StatusCode)
	}
	tokensRequest, err := http.NewRequest(http.MethodGet, agent.server.URL+"/api/v1/tokens", nil)
	if err != nil {
		t.Fatal(err)
	}
	tokensRequest.Header.Set("Authorization", bearer)
	response, err = http.DefaultClient.Do(tokensRequest)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("App bearer token must not list paired devices, got %d", response.StatusCode)
	}

	// The consumed code cannot be redeemed twice.
	body, _ := json.Marshal(map[string]string{"token": code, "deviceName": "second"})
	response, err = http.Post(agent.server.URL+"/api/v1/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusGone {
		t.Fatalf("expected 410 for a reused code, got %d", response.StatusCode)
	}

	// An expired window stops working too.
	agent.tokens.SetClaim("424242", 30*time.Millisecond)
	time.Sleep(80 * time.Millisecond)
	if status, _, _ := agent.qrSVG(t, nil); status != http.StatusConflict {
		t.Fatalf("expected 409 after expiry, got %d", status)
	}
	body, _ = json.Marshal(map[string]string{"token": "424242", "deviceName": "late"})
	response, err = http.Post(agent.server.URL+"/api/v1/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusGone {
		t.Fatalf("expected 410 for an expired code, got %d", response.StatusCode)
	}
}

// TestDashboardAggregateAndRedaction covers the read-only dashboard payload: it
// carries the status the page shows and never the pairing code or a token.
func TestDashboardAggregateAndRedaction(t *testing.T) {
	agent := newPairingTestServer(t)
	code := agent.openPairingWindow(t).code
	if _, err := agent.ring.Write([]byte("level=INFO msg=\"pairing code generated\" code=" + code + "\n")); err != nil {
		t.Fatal(err)
	}

	status, body := getBody(t, agent.server.URL+"/api/v1/dashboard", "")
	if status != http.StatusOK {
		t.Fatalf("dashboard status %d", status)
	}
	text := string(body)
	if strings.Contains(text, code) {
		t.Fatal("dashboard payload leaked the pairing code")
	}
	if !strings.Contains(text, "code=***") {
		t.Fatal("dashboard log tail did not mask the pairing code")
	}

	var payload struct {
		Service  map[string]any `json:"service"`
		Pairing  map[string]any `json:"pairing"`
		Index    map[string]any `json:"index"`
		Scan     map[string]any `json:"scan"`
		Cache    map[string]any `json:"cache"`
		Tools    map[string]any `json:"tools"`
		Hosts    map[string]any `json:"hosts"`
		Settings map[string]any `json:"settings"`
		Logs     []string       `json:"logs"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Service["version"] != "test" || payload.Service["apiVersion"] != float64(1) {
		t.Fatalf("unexpected service block: %+v", payload.Service)
	}
	if payload.Pairing["armed"] != true {
		t.Fatalf("unexpected pairing block: %+v", payload.Pairing)
	}
	for name, block := range map[string]map[string]any{
		"index": payload.Index, "scan": payload.Scan, "cache": payload.Cache,
		"tools": payload.Tools, "hosts": payload.Hosts, "settings": payload.Settings,
	} {
		if len(block) == 0 {
			t.Fatalf("dashboard block %s is empty", name)
		}
	}
	if len(payload.Logs) == 0 {
		t.Fatal("dashboard log tail is empty")
	}
}

// TestDashboardPageAndAssets covers the embedded dashboard itself.
func TestDashboardPageAndAssets(t *testing.T) {
	agent := newPairingTestServer(t)
	// 控制台是多路由的静态导出：/ 与 /dashboard 指向首页，各功能页各自一个文件。
	// 这里同时覆盖「路由能解析」与「解析不到时要 404」两种行为。
	for _, path := range []string{"/", "/dashboard", "/pair", "/libraries", "/scan", "/cache", "/settings", "/logs", "/diagnostics"} {
		status, body := getBody(t, agent.server.URL+path, "")
		if status != http.StatusOK {
			t.Fatalf("%s status %d", path, status)
		}
		if !strings.Contains(string(body), "<html") {
			t.Fatalf("%s did not serve an HTML page", path)
		}
	}
	// 带尾斜杠的写法也要能命中同一个页面。
	if status, _ := getBody(t, agent.server.URL+"/pair/", ""); status != http.StatusOK {
		t.Fatalf("/pair/ status %d", status)
	}
	// 静态资源：favicon 由内嵌 FS 直出，首页要引用 _next 产物。
	if status := get(t, agent.server.URL+"/favicon.ico", ""); status != http.StatusOK {
		t.Fatalf("/favicon.ico status %d", status)
	}
	if _, body := getBody(t, agent.server.URL+"/", ""); !strings.Contains(string(body), "/_next/") {
		t.Fatalf("home page does not reference embedded _next assets")
	}
	// 未注册的页面必须 404，而不是回落成首页——否则打错的链接会静默显示概览。
	if status, _ := getBody(t, agent.server.URL+"/no-such-page", ""); status != http.StatusNotFound {
		t.Fatalf("unknown page should 404, got %d", status)
	}
	// /api/ 前缀下的未知路径要回 404，不能吐 HTML：兜底路由挂在 "GET /" 上，
	// 很容易顺手把 API 拼写错误也变成一份网页。
	for _, path := range []string{"/api/v1/no-such-endpoint", "/api/v1"} {
		if status, _ := getBody(t, agent.server.URL+path, ""); status != http.StatusNotFound {
			t.Fatalf("%s should 404, got %d", path, status)
		}
	}
}

// TestDashboardRSCPayloads covers Next.js App Router client navigation payloads.
//
// 静态导出下客户端导航会请求 <route>.txt；这些必须以 text/plain 返回 flight 数据，
// 否则路由退回整页刷新，侧边栏切换就会闪白屏。
func TestDashboardRSCPayloads(t *testing.T) {
	agent := newPairingTestServer(t)
	// 无尾斜杠路由 → <route>.txt；首页 → /index.txt；有尾斜杠 → <route>/index.txt。
	for _, path := range []string{
		"/index.txt",
		"/pair.txt",
		"/libraries.txt",
		"/scan.txt",
		"/cache.txt",
		"/settings.txt",
		"/logs.txt",
		"/diagnostics.txt",
		"/pair/index.txt",
		"/settings/index.txt",
	} {
		req, err := http.NewRequest(http.MethodGet, agent.server.URL+path+"?_rsc=abc123", nil)
		if err != nil {
			t.Fatalf("new request %s: %v", path, err)
		}
		req.Header.Set("RSC", "1")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status %d", path, response.StatusCode)
		}
		contentType := response.Header.Get("Content-Type")
		if !strings.HasPrefix(contentType, "text/plain") && contentType != "text/x-component" {
			t.Fatalf("%s content-type %q, want text/plain or text/x-component", path, contentType)
		}
		// flight payload 是带行号前缀的 React Server Components 文本，不是 HTML。
		if strings.Contains(string(body), "<html") {
			t.Fatalf("%s returned HTML instead of an RSC payload", path)
		}
		if len(body) == 0 {
			t.Fatalf("%s returned an empty RSC payload", path)
		}
	}
	// 不存在的 RSC payload 仍然是 404，不能回落成 index。
	if status, _ := getBody(t, agent.server.URL+"/no-such-page.txt", ""); status != http.StatusNotFound {
		t.Fatalf("unknown RSC payload should 404, got %d", status)
	}
}
