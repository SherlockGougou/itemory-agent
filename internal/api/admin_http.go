package api

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// loginGuard 限制管理员登录的暴力尝试与计算开销。
//
// 每次校验要跑一轮 PBKDF2（数十万次迭代），不设限时既能被用来逐个猜密码，
// 也能靠并发请求把 NAS 的 CPU 打满。两道闸：
//   - 同一来源连续失败 loginFailureThreshold 次后退避，时长逐次翻倍，封顶 loginMaxLockout；
//   - 同时进行的校验不超过 loginMaxConcurrent 个，超出直接 429，不排队。
//
// 来源取 TCP 对端地址而不是 X-Forwarded-For：后者可由请求方随意伪造，拿它计数等于没有限制。
type loginGuard struct {
	mu       sync.Mutex
	failures map[string]loginFailure
	slots    chan struct{}
}

type loginFailure struct {
	count       int
	lockedUntil time.Time
	lockout     time.Duration
}

const (
	loginFailureThreshold = 5
	loginBaseLockout      = time.Minute
	loginMaxLockout       = 15 * time.Minute
	loginMaxConcurrent    = 2
)

func newLoginGuard() *loginGuard {
	return &loginGuard{failures: map[string]loginFailure{}, slots: make(chan struct{}, loginMaxConcurrent)}
}

func loginSourceKey(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// retryAfter 返回该来源还需等待的时长；0 表示可以尝试。
func (g *loginGuard) retryAfter(key string, now time.Time) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if state, ok := g.failures[key]; ok && now.Before(state.lockedUntil) {
		return state.lockedUntil.Sub(now)
	}
	return 0
}

func (g *loginGuard) acquire() bool {
	select {
	case g.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (g *loginGuard) release() { <-g.slots }

func (g *loginGuard) recordFailure(key string, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.failures[key]
	state.count++
	if state.count >= loginFailureThreshold {
		if state.lockout == 0 {
			state.lockout = loginBaseLockout
		} else {
			state.lockout *= 2
			if state.lockout > loginMaxLockout {
				state.lockout = loginMaxLockout
			}
		}
		state.lockedUntil = now.Add(state.lockout)
		state.count = 0
	}
	g.failures[key] = state
	// 表不能无限增长：清掉已过锁定期且没有累计失败的来源。
	if len(g.failures) > 1024 {
		for k, v := range g.failures {
			if v.count == 0 && now.After(v.lockedUntil) {
				delete(g.failures, k)
			}
		}
	}
}

func (g *loginGuard) recordSuccess(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.failures, key)
}

func (s *Server) handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"configured":    s.admins.configured(),
		"authenticated": s.hasAdminSession(r),
		"username": func() string {
			if s.hasAdminSession(r) {
				return s.admins.usernameValue()
			}
			return ""
		}(),
	})
}

func (s *Server) handleAdminSetup(w http.ResponseWriter, r *http.Request) {
	if s.admins.configured() {
		writeError(w, http.StatusConflict, "admin_already_configured", "administrator is already configured")
		return
	}
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if err := s.admins.setup(request.Username, request.Password); err != nil {
		writeError(w, http.StatusBadRequest, "admin_setup_failed", err.Error())
		return
	}
	if err := s.setAdminSession(w); err != nil {
		writeError(w, http.StatusInternalServerError, "session_failed", "could not create administrator session")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"configured":    true,
		"authenticated": true,
		"username":      s.admins.usernameValue(),
	})
}

func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if !s.admins.configured() {
		writeError(w, http.StatusPreconditionRequired, "admin_setup_required", "configure an administrator first")
		return
	}
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	key := loginSourceKey(r)
	if wait := s.loginGuard.retryAfter(key, time.Now()); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "admin_login_locked", "too many failed attempts, try again later")
		return
	}
	if !s.loginGuard.acquire() {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "admin_login_busy", "too many concurrent login attempts")
		return
	}
	ok := s.admins.verify(request.Username, request.Password)
	s.loginGuard.release()
	if !ok {
		s.loginGuard.recordFailure(key, time.Now())
		writeError(w, http.StatusUnauthorized, "admin_login_failed", "invalid administrator credentials")
		return
	}
	s.loginGuard.recordSuccess(key)
	if err := s.setAdminSession(w); err != nil {
		writeError(w, http.StatusInternalServerError, "session_failed", "could not create administrator session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"username":      s.admins.usernameValue(),
	})
}

func (s *Server) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	s.clearAdminSession(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
}
