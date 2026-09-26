package thumbs

import (
	"runtime"
	"testing"
)

// TestThumbWorkersBounded 守住并发上限。
//
// 曾经是 GOMAXPROCS*2 且不封顶，24 核机器上就是 48 个并发的 vipsthumbnail/ffmpeg；
// 实测单个 2GB 视频抽帧就要 512m 以上（512m 失败、768m 成功），叠加几十个并发任务后
// 在受限的 cgroup 里会被静默杀死——界面表现是「有些缩略图就是不生成」。
//
// 这个用例不断言某个固定数字（那会让测试随运行机器漂移），而是断言不变量：
// 不低于下限、不超过上限、且与公式一致。
func TestThumbWorkersBounded(t *testing.T) {
	cores := runtime.GOMAXPROCS(0)

	want := cores * 2
	if want > maxThumbWorkers {
		want = maxThumbWorkers
	}
	if want < 4 {
		want = 4
	}

	got := thumbWorkers()
	if got != want {
		t.Fatalf("thumbWorkers() = %d，期望 %d（GOMAXPROCS=%d，上限 %d）", got, want, cores, maxThumbWorkers)
	}
	if got < 4 {
		t.Fatalf("并发数 %d 低于下限 4", got)
	}
	if got > maxThumbWorkers {
		t.Fatalf("并发数 %d 超过上限 %d", got, maxThumbWorkers)
	}

	// 无论多少核心，都不该突破上限——这是这次修复要守的东西。
	if maxThumbWorkers < 1 {
		t.Fatal("上限必须为正")
	}
	t.Logf("GOMAXPROCS=%d → 并发数 %d（上限 %d）", cores, got, maxThumbWorkers)
}
