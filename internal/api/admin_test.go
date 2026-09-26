package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testAdminUsername = "test-admin"
	testAdminPassword = "test-admin-password-0123456789"
)

func configureTestAdmin(t *testing.T, server *Server) *http.Cookie {
	t.Helper()
	if err := server.admins.setup(testAdminUsername, testAdminPassword); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if err := server.setAdminSession(recorder); err != nil {
		t.Fatal(err)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one administrator session cookie, got %d", len(cookies))
	}
	return cookies[0]
}

func TestAdminStoreSetupAndPasswordVerification(t *testing.T) {
	dir := t.TempDir()
	store, err := loadAdminStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if store.configured() {
		t.Fatal("new administrator store must be unconfigured")
	}
	password := "correct-password-0123456789"
	if err := store.setup("owner", password); err != nil {
		t.Fatal(err)
	}
	if !store.verify("owner", password) || store.verify("owner", "wrong-password") {
		t.Fatal("administrator password verification failed")
	}
	if err := store.setup("another", password); err != errAdminAlreadyConfigured {
		t.Fatalf("second setup error = %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "admin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || string(raw) == password {
		t.Fatal("administrator password was stored in plaintext")
	}
	reloaded, err := loadAdminStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.verify("owner", password) {
		t.Fatal("reloaded administrator password did not verify")
	}
}

// 同一来源连续输错密码后必须退避：即使随后输对也先拿到 429，直到锁定期结束。
func TestAdminLoginLocksOutAfterRepeatedFailures(t *testing.T) {
	agent := newPairingTestServer(t)
	login := func(password string) int {
		body := `{"username":"` + testAdminUsername + `","password":"` + password + `"}`
		response, err := http.Post(agent.server.URL+"/api/v1/admin/login", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if status := login(testAdminPassword); status != http.StatusOK {
		t.Fatalf("correct password status = %d", status)
	}
	for attempt := 1; attempt <= loginFailureThreshold; attempt++ {
		if status := login("wrong-password"); status != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", attempt, status)
		}
	}
	if status := login(testAdminPassword); status != http.StatusTooManyRequests {
		t.Fatalf("login during lockout status = %d, want 429", status)
	}
}

func TestLoginGuardBackoffDoublesAndCaps(t *testing.T) {
	guard := newLoginGuard()
	now := time.Now()
	fail := func() {
		for i := 0; i < loginFailureThreshold; i++ {
			guard.recordFailure("peer", now)
		}
	}
	fail()
	if wait := guard.retryAfter("peer", now); wait != loginBaseLockout {
		t.Fatalf("first lockout = %v", wait)
	}
	fail()
	if wait := guard.retryAfter("peer", now); wait != 2*loginBaseLockout {
		t.Fatalf("second lockout = %v", wait)
	}
	for i := 0; i < 10; i++ {
		fail()
	}
	if wait := guard.retryAfter("peer", now); wait != loginMaxLockout {
		t.Fatalf("capped lockout = %v", wait)
	}
	guard.recordSuccess("peer")
	if wait := guard.retryAfter("peer", now); wait != 0 {
		t.Fatalf("success must clear lockout, got %v", wait)
	}
}
