package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestGetKeepsEmptySlicesNonNull 守住一个很容易漏掉的序列化细节。
//
// Get() 以前用 append([]T(nil), src...)，源切片为空时返回 nil，JSON 里就成了
// null 而不是 []。控制台把这份设置当 JSON 直接读，null 会让
// `settings.libraries.length` 直接抛错——所以这里断言的是**线上协议的形状**，
// 不只是 Go 侧的语义。
func TestGetKeepsEmptySlicesNonNull(t *testing.T) {
	manager, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	got := manager.Get()
	if got.Libraries == nil {
		t.Fatal("Get() 把空的 Libraries 退化成了 nil")
	}
	if got.ExcludePatterns == nil {
		t.Fatal("Get() 把空的 ExcludePatterns 退化成了 nil")
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	if !strings.Contains(encoded, `"libraries":[]`) {
		t.Fatalf("序列化结果里 libraries 不是空数组：%s", encoded)
	}
	if !strings.Contains(encoded, `"excludePatterns":[`) {
		t.Fatalf("序列化结果里 excludePatterns 不是数组：%s", encoded)
	}
}

// TestNextScanAt 覆盖控制台「下次计划扫描」的推导：每天只跑一次，
// 所以今天这一刻已经过去就等于下一次在明天。
func TestNextScanAt(t *testing.T) {
	noon := time.Date(2026, 9, 21, 12, 0, 0, 0, time.Local)

	next, ok := NextScanAt("03:00", noon)
	if !ok {
		t.Fatal("03:00 应当是合法计划")
	}
	if next.Day() != 22 || next.Hour() != 3 || next.Minute() != 0 {
		t.Fatalf("12:00 之后的 03:00 应为次日 03:00，得到 %v", next)
	}

	early := time.Date(2026, 9, 21, 1, 0, 0, 0, time.Local)
	next, ok = NextScanAt("03:00", early)
	if !ok || next.Day() != 21 || next.Hour() != 3 {
		t.Fatalf("01:00 之后的 03:00 应为当天 03:00，得到 %v", next)
	}

	if _, ok := NextScanAt("", noon); ok {
		t.Fatal("空计划（仅手动）不应给出下次时间")
	}
	if _, ok := NextScanAt("25:00", noon); ok {
		t.Fatal("非法计划不应给出下次时间")
	}
}
