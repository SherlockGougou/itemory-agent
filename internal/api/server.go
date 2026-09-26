// Package api exposes the HTTP surface consumed by the Itemory app.
package api

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/SherlockGougou/itemory-agent/internal/config"
	"github.com/SherlockGougou/itemory-agent/internal/events"
	"github.com/SherlockGougou/itemory-agent/internal/logging"
	"github.com/SherlockGougou/itemory-agent/internal/scan"
	"github.com/SherlockGougou/itemory-agent/internal/store"
	"github.com/SherlockGougou/itemory-agent/internal/thumbs"
)

// Deps carries everything the HTTP layer needs.
type Deps struct {
	Version    string
	APIVersion int
	DataDir    string
	Index      *store.Store
	Settings   *config.Manager
	Thumbs     *thumbs.Manager
	Scanner    *scan.Scanner
	Hub        *events.Hub
	Tokens     *TokenStore
	Logger     *logging.Logger
	LogRing    *logging.Ring
	StartedAt  time.Time
}

// Server wires the routes.
type Server struct {
	d            Deps
	admins       *adminStore
	sessionsMu   sync.Mutex
	sessions     map[string]adminSession
	claimLimiter *rateLimiter
	loginGuard   *loginGuard
	volCache     volumesCache
}

// NewServer builds a server.
func NewServer(deps Deps) *Server {
	return &Server{
		d:            deps,
		admins:       mustLoadAdminStore(deps.DataDir),
		sessions:     map[string]adminSession{},
		claimLimiter: newRateLimiter(time.Second),
		loginGuard:   newLoginGuard(),
	}
}

func mustLoadAdminStore(dir string) *adminStore {
	store, err := loadAdminStore(dir)
	if err != nil {
		panic("load administrator credentials: " + err.Error())
	}
	return store
}

// Handler returns the fully wired HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public status endpoints and administrator-protected pairing endpoints.
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/admin/status", s.handleAdminStatus)
	mux.HandleFunc("POST /api/v1/admin/setup", s.handleAdminSetup)
	mux.HandleFunc("POST /api/v1/admin/login", s.handleAdminLogin)
	mux.Handle("POST /api/v1/admin/logout", s.adminSession(s.handleAdminLogout))
	// 管理员专享的聚合取数。匿名可读的 /dashboard 是给未登录页面与 App 用的最小集，
	// 设备、完整日志、诊断这些管理面字段只从这里出去。
	mux.Handle("GET /api/v1/admin/overview", s.adminAuth(s.handleOverview))
	mux.HandleFunc("GET /api/v1/claim", s.handleClaimInfo)
	mux.Handle("POST /api/v1/claim", s.adminAuth(s.handleClaimArm))
	mux.Handle("POST /api/v1/claim/arm", s.adminAuth(s.handleClaimArm))
	mux.Handle("DELETE /api/v1/claim", s.adminAuth(s.handleClaimDisarm))
	mux.Handle("POST /api/v1/claim/disarm", s.adminAuth(s.handleClaimDisarm))
	mux.Handle("GET /api/v1/claim/qr.svg", s.adminAuth(s.handleClaimQR))
	mux.HandleFunc("POST /api/v1/pair", s.handlePair)
	mux.HandleFunc("GET /api/v1/dashboard", s.handleDashboard)
	mux.HandleFunc("GET /dashboard", s.handleDashboardPage)
	mux.Handle("GET /_next/", http.FileServerFS(webAssets()))
	mux.Handle("GET /favicon.ico", http.FileServerFS(webAssets()))
	// Next.js App Router 会从 src/app/icon*.png / apple-icon*.png 导出这些根级品牌图标。
	mux.Handle("GET /icon.png", http.FileServerFS(webAssets()))
	mux.Handle("GET /icon-dark.png", http.FileServerFS(webAssets()))
	mux.Handle("GET /apple-icon.png", http.FileServerFS(webAssets()))
	mux.Handle("GET /apple-icon-dark.png", http.FileServerFS(webAssets()))
	// 兜底：控制台本身是多路由的静态导出，由 handleDashboardPage 逐个解析页面文件。
	mux.HandleFunc("GET /", s.handleDashboardPage)

	// App 令牌或管理员会话皆可。
	//
	// 这一档是控制台能不能真正「控制」的关键：以下接口原先只认 App 的 Bearer Token，
	// 于是登录了管理员的网页端读不到扫描状态、改不了设置、看不到完整日志。
	// 注意 s.auth 自身语义不变——App 侧的鉴权行为必须零变化。
	mux.Handle("GET /api/v1/settings", s.authOrAdmin(s.handleGetSettings))
	mux.Handle("PUT /api/v1/settings", s.authOrAdmin(s.handlePutSettings))
	mux.Handle("PATCH /api/v1/settings", s.authOrAdmin(s.handlePatchSettings))
	mux.Handle("POST /api/v1/scan", s.authOrAdmin(s.handleScan))
	mux.Handle("POST /api/v1/scan/cancel", s.authOrAdmin(s.handleScanCancel))
	mux.Handle("GET /api/v1/scan/status", s.authOrAdmin(s.handleScanStatus))
	mux.Handle("GET /api/v1/logs", s.authOrAdmin(s.handleLogs))
	mux.Handle("GET /api/v1/diagnostics", s.authOrAdmin(s.handleDiagnostics))
	mux.Handle("GET /api/v1/events", s.authOrAdmin(s.handleEvents))
	mux.Handle("DELETE /api/v1/cache/thumbs", s.authOrAdmin(s.handleClearThumbCache))

	// 仅 App 令牌：这些是给客户端读媒体内容用的，网页端不需要。
	mux.Handle("GET /api/v1/libraries", s.auth(s.handleLibraries))
	mux.Handle("GET /api/v1/folders", s.auth(s.handleFolders))
	mux.Handle("GET /api/v1/host/volumes", s.auth(s.handleVolumes))
	mux.Handle("GET /api/v1/stats", s.auth(s.handleStats))
	mux.Handle("GET /api/v1/days", s.auth(s.handleDays))
	mux.Handle("GET /api/v1/months", s.auth(s.handleMonths))
	mux.Handle("GET /api/v1/stream", s.auth(s.handleStream))
	mux.Handle("GET /api/v1/days/items", s.auth(s.handleDaysItems))
	mux.Handle("GET /api/v1/days/{day}/items", s.auth(s.handleDayItems))
	mux.Handle("GET /api/v1/items/{id}", s.auth(s.handleItem))
	mux.Handle("GET /api/v1/videos", s.auth(s.handleVideos))
	mux.Handle("GET /api/v1/locations", s.auth(s.handleLocations))
	mux.Handle("GET /api/v1/thumbs/{id}", s.auth(s.handleThumb))
	mux.Handle("GET /api/v1/originals/{id}", s.auth(s.handleOriginal))
	mux.Handle("GET /api/v1/raw/{id}/preview", s.auth(s.handleRawPreview))
	mux.Handle("GET /api/v1/motion/{id}", s.auth(s.handleMotion))
	mux.Handle("GET /api/v1/tokens", s.adminAuth(s.handleTokens))
	mux.Handle("DELETE /api/v1/tokens/{id}", s.adminAuth(s.handleRevokeToken))

	return s.withRequestLog(mux)
}

// authOrAdmin 放行「有效的管理员会话」或「有效的 App 令牌」。
//
// 两档权限的差别在于凭据来源：管理员会话是 HttpOnly + SameSite=Strict 的 cookie，
// App 令牌是 Authorization 头。放行写接口给 cookie 会引入 CSRF 面，这里依赖的是
// SameSite=Strict —— 不要为了兼容而放宽这个属性。
func (s *Server) authOrAdmin(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.hasAdminSession(r) {
			next(w, r)
			return
		}
		if token, ok := bearerToken(r); ok && s.d.Tokens.Verify(token) {
			next(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeError(w, http.StatusUnauthorized, "unauthorized", "administrator login or paired device required")
	})
}

// bearerToken 取出 Authorization 头里的 Bearer 令牌。
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if token == "" {
		return "", false
	}
	return token, true
}

// adminAuth protects operations that change or reveal pairing administration
// state. App bearer tokens intentionally cannot pass this middleware.
func (s *Server) adminAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.hasAdminSession(r) {
			next(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeError(w, http.StatusUnauthorized, "admin_unauthorized", "administrator login required")
		return
	})
}

func (s *Server) adminSession(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hasAdminSession(r) {
			writeError(w, http.StatusUnauthorized, "admin_unauthorized", "administrator login required")
			return
		}
		next(w, r)
	})
}

func (s *Server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 只认 Authorization 头：令牌不进 URL——URL 会留在反代日志、浏览器历史与
		// Referer 里，而客户端与控制台都不需要这条通道。
		token, ok := bearerToken(r)
		if !ok || !s.d.Tokens.Verify(token) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "pair this device first")
			return
		}
		next(w, r)
	})
}

func (s *Server) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		if s.d.Logger != nil {
			s.d.Logger.Debug("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", recorder.status,
				"ms", time.Since(started).Milliseconds())
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush lets SSE handlers keep streaming through the recorder.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":        "ok",
		"version":       s.d.Version,
		"apiVersion":    s.d.APIVersion,
		"serverId":      s.d.Tokens.ServerID(),
		"uptimeSeconds": int(time.Since(s.d.StartedAt).Seconds()),
		"scan":          s.d.Scanner.Status(),
		"pairedDevices": s.d.Tokens.Count(),
		"capabilities":  capabilities(),
	})
}

func (s *Server) handleClaimInfo(w http.ResponseWriter, r *http.Request) {
	armed, expires := s.d.Tokens.ClaimState()
	payload := map[string]any{
		"serverId":      s.d.Tokens.ServerID(),
		"version":       s.d.Version,
		"apiVersion":    s.d.APIVersion,
		"claimArmed":    armed,
		"pairedDevices": s.d.Tokens.Count(),
		"hint":          "open the dashboard in a browser and press the pair button to show a QR code",
	}
	if armed {
		payload["expiresAt"] = expires.UTC()
		payload["expiresInSeconds"] = int(time.Until(expires).Seconds())
	}
	writeJSON(w, http.StatusOK, payload)
}

// handleClaimArm opens (or rotates) the pairing window from the dashboard.
func (s *Server) handleClaimArm(w http.ResponseWriter, r *http.Request) {
	if !s.claimLimiter.allow("arm:" + clientKey(r)) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "wait a moment before requesting another pairing code")
		return
	}
	code := RandomCode(6)
	s.d.Tokens.SetClaim(code, ClaimTTL)
	s.d.Logger.Info("pairing window opened", "expiresInSeconds", int(ClaimTTL.Seconds()))
	writeJSON(w, http.StatusOK, map[string]any{
		"code":             code,
		"expiresInSeconds": int(ClaimTTL.Seconds()),
		"serverId":         s.d.Tokens.ServerID(),
		"version":          s.d.Version,
		"apiVersion":       s.d.APIVersion,
		"hostCandidates":   hostCandidates(r),
	})
}

// handleClaimDisarm closes the pairing window (idempotent).
func (s *Server) handleClaimDisarm(w http.ResponseWriter, r *http.Request) {
	if !s.claimLimiter.allow("disarm:" + clientKey(r)) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "wait a moment before changing the pairing window")
		return
	}
	s.d.Tokens.ClearClaim()
	s.d.Logger.Info("pairing window closed")
	writeJSON(w, http.StatusOK, map[string]any{"closed": true})
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token      string `json:"token"`
		DeviceName string `json:"deviceName"`
		// Platform 可选：老版本 App 不传，配对流程不能因此失败。
		Platform string `json:"platform"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	token, secret, err := s.d.Tokens.Redeem(req.Token, req.DeviceName, req.Platform)
	if err != nil {
		status := http.StatusForbidden
		switch err {
		case ErrClaimExpired, ErrClaimMissing:
			status = http.StatusGone
		case ErrClaimLocked:
			status = http.StatusTooManyRequests
		}
		writeError(w, status, "pair_failed", err.Error())
		return
	}
	s.d.Logger.Info("device paired", "device", token.Name, "tokenId", token.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"token":        secret,
		"tokenId":      token.ID,
		"apiVersion":   s.d.APIVersion,
		"serverId":     s.d.Tokens.ServerID(),
		"version":      s.d.Version,
		"capabilities": capabilities(),
	})
}

// capabilities 返回服务端当前支持的能力位。App 按能力位决定是否使用聚合读接口，
// 旧版本客户端忽略未知能力；旧版本服务端缺能力时客户端回落到按自然日抽样。
func capabilities() []string {
	return []string{
		"days", "folders", "thumbs", "originals", "range",
		"events", "rawPreview", "motionPhoto", "diagnostics", "settings",
		"stats", "months", "stream", "locations", "batchDays", "videoPagination",
		// exif：条目随带镜头与曝光参数（机型 / 光圈 / 快门 / ISO / 焦距 / 海拔 / 镜头型号）。
		// 客户端据此判断远端媒体能否展示这些字段；缺少该能力位的旧服务端不返回这些键。
		"exif",
	}
}

// handleDashboardPage serves the control console.
//
// 控制台是多路由的静态导出：每个路由一个 HTML 文件，全部嵌进二进制。这里把 URL
// 路径映射到内嵌 FS 里的页面文件，两种导出形态（<route>.html 与 <route>/index.html）都认。
//
// Next.js App Router 在 output: 'export' 下的客户端导航会额外请求 RSC payload
// （<route>.txt 或 <route>/index.txt）。这些文件若回 404 / HTML，路由会退回整页
// 刷新，侧边栏切换就会闪一下白屏再重绘。因此 .txt 必须按 text/plain 原样返回。
func (s *Server) handleDashboardPage(w http.ResponseWriter, r *http.Request) {
	// 这条路由挂在 "GET /" 上，会接住所有没被更具体模式命中的路径——包括拼错的
	// /api/… 。那些必须回 JSON 404，而不是塞一份 HTML 出去。
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/_next/") {
		writeError(w, http.StatusNotFound, "not_found", "unknown path")
		return
	}
	for _, name := range pageCandidates(r.URL.Path) {
		body, err := fs.ReadFile(webAssets(), name)
		if err != nil {
			continue
		}
		if strings.HasSuffix(name, ".txt") {
			// Next.js 静态导出模式接受 text/x-component 或 text/plain 前缀。
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(body)
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "unknown page")
}

// pageCandidates 把 URL 路径映射成内嵌 FS 里可能存在的页面文件名，按优先级排列。
func pageCandidates(urlPath string) []string {
	trimmed := strings.Trim(path.Clean("/"+strings.TrimSpace(urlPath)), "/")
	if strings.HasSuffix(trimmed, ".txt") {
		return rscCandidates(trimmed)
	}
	if trimmed == "" || trimmed == "dashboard" {
		return []string{"index.html"}
	}
	return []string{trimmed + ".html", path.Join(trimmed, "index.html")}
}

// rscCandidates 映射 Next.js 静态导出的 RSC payload 路径。
//
// 导出产物是扁平的 <route>.txt；客户端按路由形态请求 <route>.txt（无尾斜杠）或
// <route>/index.txt（有尾斜杠），首页则是 /index.txt。两种请求形态都要落到同一份
// 内嵌文件上。
func rscCandidates(trimmed string) []string {
	base := strings.TrimSuffix(trimmed, ".txt")
	base = strings.TrimSuffix(base, "/index")
	base = strings.Trim(base, "/")
	if base == "" || base == "index" || base == "dashboard" {
		return []string{"index.txt"}
	}
	return []string{base + ".txt", path.Join(base, "index.txt")}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error":   code,
		"message": message,
	})
}
