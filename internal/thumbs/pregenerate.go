package thumbs

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/SherlockGougou/itemory-agent/internal/store"
)

// PregenStats 是一轮后台预生成的结果。
type PregenStats struct {
	Generated int
	Failed    int
	// Stopped 说明这一轮为什么结束：done（全部条目都已有缩略图）、budget（达到单轮上限）、
	// cache（缓存接近上限）、cancelled（被新一轮、清空缓存或进程退出打断）、disabled（上限为 0）。
	Stopped string
}

// pregenState 保存后台预生成的运行状态。
type pregenState struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	// failed 记录本进程内生成失败的条目（无内嵌预览的 RAW、损坏的文件）。
	// 不记下来的话，这些文件每一轮都会排在前面重试一遍，白白占用单轮上限。
	failed map[string]struct{}
}

// StartPregenerate 在后台为还没有缓存的条目生成缩略图，立即返回。
//
// ids 是用户可见条目的内容 id，按希望的生成顺序排列；lookup 按 id 取回索引条目。
// 已有一轮在跑时先让它停下：调用方只在扫描结束后调用，新的 id 列表才是最新的索引。
func (m *Manager) StartPregenerate(ids []string, lookup func(id string) (store.Entry, bool)) {
	m.StopPregenerate()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.pregen.mu.Lock()
	m.pregen.cancel, m.pregen.done = cancel, done
	m.pregen.mu.Unlock()

	go func() {
		defer close(done)
		defer cancel()
		started := time.Now()
		stats := m.pregenerate(ctx, ids, lookup)
		if stats.Stopped == "disabled" {
			return
		}
		m.log.Info("thumbnail pre-generation finished",
			"generated", stats.Generated,
			"failed", stats.Failed,
			"stopped", stats.Stopped,
			"elapsed_s", time.Since(started).Seconds())
	}()
}

// StopPregenerate 打断正在进行的预生成并等它退出（没有在跑时是 no-op）。
func (m *Manager) StopPregenerate() {
	m.pregen.mu.Lock()
	cancel, done := m.pregen.cancel, m.pregen.done
	m.pregen.cancel, m.pregen.done = nil, nil
	m.pregen.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// pregenerate 同步执行一轮预生成。
//
// 三条边界都来自设置：单轮最多尝试 NightlyThumbBudget 个条目；并发不超过扫描并发数，
// 且最多占用一半的生成槽位，App 此时发来的缩略图请求不会排在整批预生成后面；
// 缓存达到上限的 90% 就停——那是淘汰的目标水位，越过它继续生成只会让新旧缩略图互相挤出去。
func (m *Manager) pregenerate(ctx context.Context, ids []string, lookup func(id string) (store.Entry, bool)) PregenStats {
	settings := m.settings.Get()
	budget := settings.NightlyThumbBudget
	if budget <= 0 {
		return PregenStats{Stopped: "disabled"}
	}
	size := settings.ThumbSize
	if size <= 0 {
		size = 512
	}
	workers := settings.Concurrency
	if half := cap(m.sem) / 2; workers > half {
		workers = half
	}
	if workers < 1 {
		workers = 1
	}
	ceiling := settings.ThumbCacheLimitBytes * 9 / 10
	_, used := m.walkStats()

	var (
		mu    sync.Mutex
		stats PregenStats
		wg    sync.WaitGroup
	)
	jobs := make(chan store.Entry)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for entry := range jobs {
				path, err := m.Ensure(entry, size)
				mu.Lock()
				if err != nil {
					stats.Failed++
					m.pregen.mu.Lock()
					if m.pregen.failed == nil {
						m.pregen.failed = map[string]struct{}{}
					}
					m.pregen.failed[entry.ID] = struct{}{}
					m.pregen.mu.Unlock()
					m.log.Debug("thumbnail pre-generation failed", "path", entry.Path, "error", err)
				} else {
					stats.Generated++
					if info, statErr := os.Stat(path); statErr == nil {
						used += info.Size()
					}
				}
				mu.Unlock()
			}
		}()
	}

	stopped := "done"
	attempted := 0
dispatch:
	for _, id := range ids {
		if ctx.Err() != nil {
			stopped = "cancelled"
			break
		}
		if attempted >= budget {
			stopped = "budget"
			break
		}
		mu.Lock()
		full := settings.ThumbCacheLimitBytes > 0 && used >= ceiling
		mu.Unlock()
		if full {
			stopped = "cache"
			break
		}
		if info, err := os.Stat(m.CachePath(id, size)); err == nil && info.Size() > 0 {
			continue
		}
		m.pregen.mu.Lock()
		_, failedBefore := m.pregen.failed[id]
		m.pregen.mu.Unlock()
		if failedBefore {
			continue
		}
		entry, ok := lookup(id)
		if !ok {
			continue
		}
		select {
		case jobs <- entry:
			attempted++
		case <-ctx.Done():
			stopped = "cancelled"
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()

	stats.Stopped = stopped
	return stats
}
