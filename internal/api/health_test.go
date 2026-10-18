package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/SherlockGougou/itemory-agent/internal/config"
	"github.com/SherlockGougou/itemory-agent/internal/scan"
)

func TestHealthHidesPrivateScanDetailsFromAnonymousRequests(t *testing.T) {
	server, _, _, _, httpServer, library, bearer := newDeleteTestServer(t, config.DeleteModeOff)
	writeTestJPEG(t, filepath.Join(library, "private-family-photo.jpg"))
	if err := server.d.Scanner.Scan("full"); err != nil {
		t.Fatal(err)
	}
	progress := server.d.Scanner.Status()
	if progress.CurrentPath == "" || progress.Message == "" {
		t.Fatal("scan must produce private path and a detailed message for this regression")
	}
	adminCookie := configureTestAdmin(t, server)
	for _, tc := range []struct {
		name    string
		bearer  string
		cookie  *http.Cookie
		private bool
	}{
		{name: "anonymous"},
		{name: "invalid token", bearer: "Bearer invalid"},
		{name: "invalid session", cookie: &http.Cookie{Name: adminCookie.Name, Value: "invalid"}},
		{name: "paired device", bearer: bearer, private: true},
		{name: "administrator", cookie: adminCookie, private: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, httpServer.URL+"/api/v1/health", nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.bearer != "" {
				request.Header.Set("Authorization", tc.bearer)
			}
			if tc.cookie != nil {
				request.AddCookie(tc.cookie)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("unexpected health response: %d %v", response.StatusCode, response.Header)
			}
			var body bytes.Buffer
			if _, err := body.ReadFrom(response.Body); err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Scan scan.Progress `json:"scan"`
			}
			if err := json.Unmarshal(body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Scan.Complete != progress.Complete || payload.Scan.MediaIndexed != progress.MediaIndexed {
				t.Fatal("health lost scan completion or counters")
			}
			if tc.private {
				if payload.Scan.CurrentPath != progress.CurrentPath || payload.Scan.Message != progress.Message {
					t.Fatal("authenticated health lost scan details")
				}
			} else if bytes.Contains(body.Bytes(), []byte(library)) || payload.Scan.CurrentPath != "" || payload.Scan.Message != "" {
				t.Fatalf("anonymous health leaked private scan details: %s", body.String())
			}
		})
	}
	if server.d.Scanner.Status().CurrentPath != progress.CurrentPath {
		t.Fatal("anonymous health mutated the scanner's private state")
	}
}

// TestLastScanCheckUsesPersistedState 守住一条曾经误报的判定。
//
// 原始实现只看进程内存里的 Scanner.Progress：容器一重启 Progress 归零，
// 于是控制台每次都报「最近一次扫描：从未运行」，顶栏显示「1 项待处理」——
// 而索引明明在磁盘上。改成以持久化的 index.LastScanAt 为准。
func TestLastScanCheckUsesPersistedState(t *testing.T) {
	persisted := time.Now().Add(-2 * time.Hour).Unix()

	cases := []struct {
		name     string
		at       int64
		progress scan.Progress
		wantOK   bool
		wantCode string
	}{
		{
			// 回归点：进程刚重启、内存进度为空，但磁盘上已有完成的扫描。
			name:     "重启后内存进度为空但有持久化记录",
			at:       persisted,
			progress: scan.Progress{},
			wantOK:   true,
		},
		{
			name:     "从未扫描过",
			at:       0,
			progress: scan.Progress{},
			wantOK:   false,
			wantCode: "never",
		},
		{
			name:     "正在扫描不算异常",
			at:       persisted,
			progress: scan.Progress{Running: true, StartedAt: time.Now()},
			wantOK:   true,
			wantCode: "running",
		},
		{
			name:     "本轮被中止",
			at:       persisted,
			progress: scan.Progress{Cancelled: true, StartedAt: time.Now(), Message: "scan cancelled"},
			wantOK:   false,
			wantCode: "cancelled",
		},
		{
			name:     "本轮有错误",
			at:       persisted,
			progress: scan.Progress{Errors: 3, StartedAt: time.Now(), Message: "scan finished"},
			wantOK:   false,
			wantCode: "errors",
		},
		{
			name:     "本轮正常收尾",
			at:       persisted,
			progress: scan.Progress{StartedAt: time.Now(), Message: "scan finished"},
			wantOK:   true,
		},
		{
			// 旧一轮留下的 Message 不该影响新进程：StartedAt 为零说明本进程没跑过。
			name:     "旧消息不属于本进程",
			at:       persisted,
			progress: scan.Progress{Message: "scan incomplete; existing entries retained"},
			wantOK:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := lastScanCheck(tc.at, tc.progress)
			if got.ID != "lastScan" {
				t.Fatalf("ID = %q", got.ID)
			}
			if got.OK != tc.wantOK {
				t.Fatalf("OK = %v，期望 %v（reason=%q）", got.OK, tc.wantOK, got.Reason)
			}
			if got.Reason != tc.wantCode {
				t.Fatalf("Reason = %q，期望 %q", got.Reason, tc.wantCode)
			}
		})
	}
}
