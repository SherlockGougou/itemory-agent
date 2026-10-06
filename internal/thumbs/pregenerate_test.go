package thumbs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/SherlockGougou/itemory-agent/internal/logging"
	"github.com/SherlockGougou/itemory-agent/internal/media"
	"github.com/SherlockGougou/itemory-agent/internal/store"
)

// pregenFixture 造 count 张 JPEG，返回它们的 id 列表与按 id 查条目的函数。
func pregenFixture(t *testing.T, count int) ([]string, func(string) (store.Entry, bool)) {
	t.Helper()
	dir := t.TempDir()
	entries := map[string]store.Entry{}
	ids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		path := filepath.Join(dir, fmt.Sprintf("photo-%d.jpg", i))
		writeTestImage(t, path, 320, 240)
		id := fmt.Sprintf("id-%d", i)
		entries[id] = store.Entry{ID: id, Path: path, Kind: media.KindImage}
		ids = append(ids, id)
	}
	return ids, func(id string) (store.Entry, bool) {
		entry, ok := entries[id]
		return entry, ok
	}
}

func cachedCount(t *testing.T, m *Manager, ids []string, size int) int {
	t.Helper()
	count := 0
	for _, id := range ids {
		if info, err := os.Stat(m.CachePath(id, size)); err == nil && info.Size() > 0 {
			count++
		}
	}
	return count
}

func TestPregenerateStopsAtBudgetAndResumesNextRound(t *testing.T) {
	ids, lookup := pregenFixture(t, 5)
	settings := dummySettings{thumbSize: 128, limit: 1 << 30, budget: 3, concurrency: 2}
	m := New(t.TempDir(), settings, logging.New("error", nil))

	first := m.pregenerate(context.Background(), ids, lookup)
	if first.Generated != 3 || first.Failed != 0 || first.Stopped != "budget" {
		t.Fatalf("first round = %+v", first)
	}
	// 按给定顺序生成：先处理的是列表前面的条目。
	if got := cachedCount(t, m, ids[:3], 128); got != 3 {
		t.Fatalf("first three cached = %d", got)
	}

	second := m.pregenerate(context.Background(), ids, lookup)
	if second.Generated != 2 || second.Stopped != "done" {
		t.Fatalf("second round = %+v", second)
	}
	third := m.pregenerate(context.Background(), ids, lookup)
	if third.Generated != 0 || third.Stopped != "done" {
		t.Fatalf("third round = %+v", third)
	}
	if got := cachedCount(t, m, ids, 128); got != 5 {
		t.Fatalf("cached = %d, want 5", got)
	}
}

func TestPregenerateDoesNotRetryFailedEntries(t *testing.T) {
	ids, lookup := pregenFixture(t, 1)
	broken := store.Entry{ID: "broken", Path: filepath.Join(t.TempDir(), "missing.jpg"), Kind: media.KindImage}
	withBroken := func(id string) (store.Entry, bool) {
		if id == broken.ID {
			return broken, true
		}
		return lookup(id)
	}
	all := append([]string{broken.ID}, ids...)
	settings := dummySettings{thumbSize: 128, limit: 1 << 30, budget: 10, concurrency: 1}
	m := New(t.TempDir(), settings, logging.New("error", nil))

	first := m.pregenerate(context.Background(), all, withBroken)
	if first.Generated != 1 || first.Failed != 1 {
		t.Fatalf("first round = %+v", first)
	}
	second := m.pregenerate(context.Background(), all, withBroken)
	if second.Generated != 0 || second.Failed != 0 {
		t.Fatalf("second round retried a known failure: %+v", second)
	}
}

func TestPregenerateRespectsDisabledBudgetCacheCeilingAndCancel(t *testing.T) {
	ids, lookup := pregenFixture(t, 3)

	disabled := New(t.TempDir(), dummySettings{thumbSize: 128, limit: 1 << 30, budget: 0, concurrency: 2}, logging.New("error", nil))
	if got := disabled.pregenerate(context.Background(), ids, lookup); got.Stopped != "disabled" || got.Generated != 0 {
		t.Fatalf("disabled = %+v", got)
	}

	// 上限小到第一张缩略图就越过 90% 水位：随后停下，不把剩下的条目都生成出来。
	// 水位在派发时检查，已经派发给 worker 的那一张仍会完成，因此最多多出「并发数」张。
	small := New(t.TempDir(), dummySettings{thumbSize: 128, limit: 1024, budget: 10, concurrency: 1}, logging.New("error", nil))
	if got := small.pregenerate(context.Background(), ids, lookup); got.Stopped != "cache" || got.Generated < 1 || got.Generated > 2 {
		t.Fatalf("cache ceiling = %+v", got)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	stopped := New(t.TempDir(), dummySettings{thumbSize: 128, limit: 1 << 30, budget: 10, concurrency: 2}, logging.New("error", nil))
	if got := stopped.pregenerate(cancelled, ids, lookup); got.Stopped != "cancelled" || got.Generated != 0 {
		t.Fatalf("cancelled = %+v", got)
	}
}

func TestClearStopsRunningPregeneration(t *testing.T) {
	ids, lookup := pregenFixture(t, 4)
	m := New(t.TempDir(), dummySettings{thumbSize: 128, limit: 1 << 30, budget: 10, concurrency: 1}, logging.New("error", nil))
	m.StartPregenerate(ids, lookup)
	if err := m.Clear(); err != nil {
		t.Fatal(err)
	}
	// Clear 返回时预生成已经退出，之后缓存目录保持为空。
	if got := cachedCount(t, m, ids, 128); got != 0 {
		t.Fatalf("cached after clear = %d", got)
	}
	m.StopPregenerate()
}
