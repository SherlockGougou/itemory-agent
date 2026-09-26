package store

import (
	"testing"
	"time"
)

// 带时区偏移的拍摄时间按拍摄地墙上时钟归日，与服务所在时区无关。
func TestDayKeyUsesWallClockForOffsetCaptures(t *testing.T) {
	tokyo := time.FixedZone("+09:00", 9*60*60)
	capture := time.Date(2024, time.May, 1, 0, 30, 0, 0, tokyo)
	if got := DayKey(capture); got != "2024-05-01" {
		t.Fatalf("DayKey(%v) = %s, want 2024-05-01", capture, got)
	}
	newYork := time.FixedZone("-05:00", -5*60*60)
	late := time.Date(2023, time.December, 31, 23, 50, 0, 0, newYork)
	if got := DayKey(late); got != "2023-12-31" {
		t.Fatalf("DayKey(%v) = %s, want 2023-12-31", late, got)
	}
}

// UTC 时间（如 QuickTime、带 Z 的 XMP）仍按服务时区换算，行为与此前一致。
func TestDayKeyConvertsUTCToServiceZone(t *testing.T) {
	utc := time.Date(2024, time.May, 1, 20, 0, 0, 0, time.UTC)
	if got, want := DayKey(utc), utc.Local().Format("2006-01-02"); got != want {
		t.Fatalf("DayKey(%v) = %s, want %s", utc, got, want)
	}
}
