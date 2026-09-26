package api

import (
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

// 媒体库被移除后、下一轮扫描前，索引里还留着它的条目：原图接口不能再凭 id 把文件读出去。
func TestOriginalsOutsideConfiguredLibrariesAreNotServed(t *testing.T) {
	library := t.TempDir()
	photo := filepath.Join(library, "a.jpg")
	if err := os.WriteFile(photo, []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
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
		{Path: photo, ID: "i1", Name: "a.jpg", Kind: "image", Capture: 1_709_251_200, DayKey: "2024-03-01", LastSeen: gen},
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
	current := settings.Get()
	current.Libraries = []config.Library{{ID: "lib", Name: "lib", Path: library}}
	if err := settings.Update(current); err != nil {
		t.Fatal(err)
	}
	log := logging.New("error", logging.NewRing(10))
	hub := events.NewHub()
	tokens, err := LoadTokens(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	tokens.SetClaim("135790", 15*time.Minute)
	server := NewServer(Deps{
		Version: "test", APIVersion: 1, DataDir: dataDir, Index: index, Settings: settings,
		Thumbs: thumbs.New(filepath.Join(dataDir, "thumbs"), settings, log), Scanner: scan.New(index, settings, hub, log),
		Hub: hub, Tokens: tokens, Logger: log, LogRing: logging.NewRing(10), StartedAt: time.Now(),
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	bearer := pairDevice(t, httpServer.URL, "135790")

	if code := get(t, httpServer.URL+"/api/v1/originals/i1", bearer); code != http.StatusOK {
		t.Fatalf("original inside library status = %d", code)
	}
	current.Libraries = []config.Library{}
	if err := settings.Update(current); err != nil {
		t.Fatal(err)
	}
	if code := get(t, httpServer.URL+"/api/v1/originals/i1", bearer); code != http.StatusNotFound {
		t.Fatalf("original after library removal status = %d, want 404", code)
	}
}

func TestScopeChangedDetectsLibraryAndExcludeEdits(t *testing.T) {
	base := config.Settings{
		Libraries:       []config.Library{{ID: "a", Path: "/photos"}},
		ExcludePatterns: []string{"@eaDir"},
	}
	same := base
	same.ThumbSize = 512
	if scopeChanged(base, same) {
		t.Fatal("thumbnail size must not trigger a rescan")
	}
	moved := base
	moved.Libraries = []config.Library{{ID: "a", Path: "/photos2"}}
	if !scopeChanged(base, moved) {
		t.Fatal("library path change must trigger a rescan")
	}
	excluded := base
	excluded.ExcludePatterns = []string{"@eaDir", "private"}
	if !scopeChanged(base, excluded) {
		t.Fatal("new exclude pattern must trigger a rescan")
	}
}
