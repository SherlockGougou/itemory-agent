package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsAndPresets(t *testing.T) {
	def := Defaults()
	if def.Preset != "balanced" || def.Concurrency != 2 || def.ThumbSize != 512 {
		t.Fatalf("unexpected defaults: %+v", def)
	}
	if def.ThumbCacheLimitBytes != 5<<30 {
		t.Fatalf("expected 5GiB thumb cache, got %d", def.ThumbCacheLimitBytes)
	}

	light := Defaults()
	ApplyPreset(&light, "light")
	if light.Concurrency != 1 || light.ThumbSize != 256 || light.ThumbCacheLimitBytes != 1<<30 {
		t.Fatalf("unexpected light preset: %+v", light)
	}

	perf := Defaults()
	ApplyPreset(&perf, "performance")
	if perf.Concurrency != 4 || perf.ThumbCacheLimitBytes != 10<<30 {
		t.Fatalf("unexpected performance preset: %+v", perf)
	}
}

func TestValidateClampsAndNormalizes(t *testing.T) {
	in := Defaults()
	in.Concurrency = 99
	in.ThumbSize = 4
	in.ThumbCacheLimitBytes = 1
	in.Transcode = "bogus"
	in.ScanSchedule = "25:99"
	in.Libraries = []Library{{Path: "/vol1/photo"}, {Path: "/vol1/photo/../photo"}}

	out, err := Validate(in)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if out.Concurrency != 8 || out.ThumbSize != 128 || out.ThumbCacheLimitBytes != 64<<20 {
		t.Fatalf("clamps not applied: %+v", out)
	}
	if out.Transcode != "off" || out.ScanSchedule != "" {
		t.Fatalf("normalization failed: %+v", out)
	}
	if len(out.Libraries) != 1 || out.Libraries[0].Path != "/vol1/photo" {
		t.Fatalf("libraries not cleaned: %+v", out.Libraries)
	}
	if out.Libraries[0].ID == "" || out.Libraries[0].Name != "photo" {
		t.Fatalf("library id/name not derived: %+v", out.Libraries[0])
	}
}

func TestLoadCreatesAndUpdatesFile(t *testing.T) {
	dir := t.TempDir()
	manager, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); err != nil {
		t.Fatalf("settings.json not created: %v", err)
	}

	next := manager.Get()
	next.Libraries = []Library{{ID: "lib-1", Name: "Photos", Path: "/vol1/photo"}}
	next.ThumbSize = 256
	if err := manager.Update(next); err != nil {
		t.Fatalf("update: %v", err)
	}

	reloaded, err := Load(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := reloaded.Get()
	if len(got.Libraries) != 1 || got.Libraries[0].Path != "/vol1/photo" || got.ThumbSize != 256 {
		t.Fatalf("settings not persisted: %+v", got)
	}
}

// 删除模式必须默认关闭并拒绝未知取值：拼错的配置不能变成「默认允许删除」。
func TestDeleteModeDefaultsToOffAndRejectsUnknownValues(t *testing.T) {
	if def := Defaults(); def.DeleteMode != DeleteModeOff {
		t.Fatalf("defaults must disable deletion, got %q", def.DeleteMode)
	}

	// 老版本 settings.json 没有 deleteMode 键：反序列化后是空串，必须归到 off。
	normalized, err := Validate(Settings{})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if normalized.DeleteMode != DeleteModeOff {
		t.Fatalf("empty deleteMode must normalize to off, got %q", normalized.DeleteMode)
	}
	for _, bogus := range []string{"yes", "ON", "true", "remove", "Trash"} {
		in := Defaults()
		in.DeleteMode = bogus
		out, err := Validate(in)
		if err != nil {
			t.Fatalf("validate %q: %v", bogus, err)
		}
		if out.DeleteMode != DeleteModeOff {
			t.Fatalf("deleteMode %q must fall back to off, got %q", bogus, out.DeleteMode)
		}
	}
	for _, allowed := range []string{DeleteModeTrash, DeleteModePermanent} {
		in := Defaults()
		in.DeleteMode = allowed
		out, err := Validate(in)
		if err != nil {
			t.Fatalf("validate %q: %v", allowed, err)
		}
		if out.DeleteMode != allowed {
			t.Fatalf("deleteMode %q must be preserved, got %q", allowed, out.DeleteMode)
		}
	}
}

// 关闭删除是升级带来的行为变化：必须确认它会被持久化并在重新加载后保持。
func TestDeleteModeRoundTripsThroughSettingsFile(t *testing.T) {
	dir := t.TempDir()
	manager, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := manager.Get().DeleteMode; got != DeleteModeOff {
		t.Fatalf("fresh settings must disable deletion, got %q", got)
	}

	next := manager.Get()
	next.DeleteMode = DeleteModeTrash
	if err := manager.Update(next); err != nil {
		t.Fatalf("update: %v", err)
	}
	reloaded, err := Load(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.Get().DeleteMode; got != DeleteModeTrash {
		t.Fatalf("deleteMode not persisted: %q", got)
	}
}
