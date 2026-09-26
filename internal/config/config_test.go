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
