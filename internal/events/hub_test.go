package events

import "testing"

// 新订阅者只拿到最新的扫描状态，其余事件按发生顺序回放。
func TestSubscribeReplaysLatestScanStateInOrder(t *testing.T) {
	hub := NewHub()
	hub.Broadcast("settings/changed", 1)
	hub.Broadcast("scan/started", 2)
	hub.Broadcast("scan/progress", 3)
	hub.Broadcast("scan/done", 4)
	ch := hub.Subscribe()
	defer hub.Unsubscribe(ch)
	var kinds []string
	for len(ch) > 0 {
		kinds = append(kinds, (<-ch).Kind)
	}
	if len(kinds) != 2 || kinds[0] != "settings/changed" || kinds[1] != "scan/done" {
		t.Fatalf("replay = %v, want [settings/changed scan/done]", kinds)
	}
}
