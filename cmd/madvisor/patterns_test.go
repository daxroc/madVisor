package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultUnits(t *testing.T) {
	cfg, err := loadDefaultUnits()
	if err != nil {
		t.Fatalf("loadDefaultUnits: %v", err)
	}
	if len(cfg.Units) == 0 {
		t.Fatal("expected at least one unit group")
	}

	units := make(map[string]bool)
	for _, u := range cfg.Units {
		units[u.Unit] = true
		if len(u.Matchers) == 0 {
			t.Errorf("unit %q has no matchers", u.Unit)
		}
		if u.Suffix == "" {
			t.Errorf("unit %q has no suffix", u.Unit)
		}
	}
	for _, expected := range []string{"bytes", "duration", "timestamp", "percent", "count"} {
		if !units[expected] {
			t.Errorf("missing expected unit %q", expected)
		}
	}
}

func TestCompileUnits(t *testing.T) {
	cfg := &UnitsConfig{
		Units: []UnitEntry{
			{Unit: "bytes", Suffix: " [bytes]", Matchers: []string{"_bytes$"}},
			{Unit: "count", Suffix: " [count]", Matchers: []string{"_total$"}},
		},
	}
	um, err := compileUnits(cfg)
	if err != nil {
		t.Fatalf("compileUnits: %v", err)
	}
	if len(um.units) != 2 {
		t.Fatalf("compiled units = %d, want 2", len(um.units))
	}
}

func TestCompileUnitsInvalidRegex(t *testing.T) {
	cfg := &UnitsConfig{
		Units: []UnitEntry{
			{Unit: "bad", Suffix: "", Matchers: []string{"[invalid"}},
		},
	}
	_, err := compileUnits(cfg)
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestUnitMatcherMatch(t *testing.T) {
	cfg, err := loadDefaultUnits()
	if err != nil {
		t.Fatalf("loadDefaultUnits: %v", err)
	}
	um, err := compileUnits(cfg)
	if err != nil {
		t.Fatalf("compileUnits: %v", err)
	}

	tests := []struct {
		name     string
		wantUnit string
	}{
		{"go_memstats_alloc_bytes", "bytes"},
		{"go_memstats_alloc_bytes_total", "bytes"},
		{"go_gc_duration_seconds", "duration"},
		{"request_duration_milliseconds", "duration_ms"},
		{"cpu_usage_percent", "percent"},
		{"disk_ratio", "percent"},
		{"go_memstats_last_gc_time_seconds", "timestamp"},
		{"process_start_timestamp", "timestamp"},
		{"promhttp_metric_handler_requests_total", "count"},
		{"unknown_metric", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := um.Match(tt.name)
			got := ""
			if m != nil {
				got = m.Unit
			}
			if got != tt.wantUnit {
				t.Errorf("Match(%q) = %q, want %q", tt.name, got, tt.wantUnit)
			}
		})
	}
}

func TestTimestampBeforeDuration(t *testing.T) {
	cfg, err := loadDefaultUnits()
	if err != nil {
		t.Fatalf("loadDefaultUnits: %v", err)
	}
	um, err := compileUnits(cfg)
	if err != nil {
		t.Fatalf("compileUnits: %v", err)
	}

	m := um.Match("go_memstats_last_gc_time_seconds")
	if m == nil || m.Unit != "timestamp" {
		unit := ""
		if m != nil {
			unit = m.Unit
		}
		t.Errorf("_time_seconds should match timestamp before duration, got %q", unit)
	}
}

func TestMergeUnitsOverride(t *testing.T) {
	base := &UnitsConfig{
		Units: []UnitEntry{
			{Unit: "bytes", Suffix: " [bytes]", Matchers: []string{"_bytes$"}},
			{Unit: "duration", Suffix: " [duration]", Matchers: []string{"_seconds$"}},
		},
	}
	override := &UnitsConfig{
		Units: []UnitEntry{
			{Unit: "bytes", Suffix: " [B]", Matchers: []string{"_bytes$", "_octets$"}},
			{Unit: "custom", Suffix: " [custom]", Matchers: []string{"_custom$"}},
		},
	}

	merged := mergeUnits(base, override)

	unitMap := make(map[string]UnitEntry)
	for _, u := range merged.Units {
		unitMap[u.Unit] = u
	}

	if e, ok := unitMap["bytes"]; !ok {
		t.Error("missing bytes in merged")
	} else if len(e.Matchers) != 2 {
		t.Errorf("bytes matchers = %d, want 2 (override)", len(e.Matchers))
	} else if e.Suffix != " [B]" {
		t.Errorf("bytes suffix = %q, want ' [B]' (override)", e.Suffix)
	}

	if _, ok := unitMap["custom"]; !ok {
		t.Error("missing custom unit from override")
	}

	if _, ok := unitMap["duration"]; !ok {
		t.Error("missing duration from base (should be preserved)")
	}
}

func TestMergeUnitsNilOverride(t *testing.T) {
	base := &UnitsConfig{
		Units: []UnitEntry{
			{Unit: "bytes", Suffix: " [bytes]", Matchers: []string{"_bytes$"}},
		},
	}
	merged := mergeUnits(base, nil)
	if len(merged.Units) != 1 {
		t.Errorf("merged units = %d, want 1", len(merged.Units))
	}
}

func TestLoadUnitsFile(t *testing.T) {
	content := `units:
  - unit: bytes
    suffix: " [bytes]"
    matchers:
      - "_bytes$"
      - "_octets$"
  - unit: special
    suffix: " [special]"
    matchers:
      - "^myapp_"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "units.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	cfg, err := loadUnitsFile(path)
	if err != nil {
		t.Fatalf("loadUnitsFile: %v", err)
	}
	if len(cfg.Units) != 2 {
		t.Fatalf("units = %d, want 2", len(cfg.Units))
	}
	if cfg.Units[0].Unit != "bytes" {
		t.Errorf("first unit = %q, want bytes", cfg.Units[0].Unit)
	}
	if len(cfg.Units[0].Matchers) != 2 {
		t.Errorf("bytes matchers = %d, want 2", len(cfg.Units[0].Matchers))
	}
}

func TestInitPatternsWithUserFile(t *testing.T) {
	content := `units:
  - unit: bytes
    suffix: " [B]"
    matchers:
      - "_bytes$"
      - "_octets$"
  - unit: custom
    suffix: " [custom]"
    matchers:
      - "^myprefix_"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	oldMatcher := globalUnitMatcher
	defer func() { globalUnitMatcher = oldMatcher }()

	if err := initPatterns(path); err != nil {
		t.Fatalf("initPatterns: %v", err)
	}

	m := globalUnitMatcher.Match("myprefix_metric")
	if m == nil || m.Unit != "custom" {
		t.Errorf("Match(myprefix_metric) = %v, want custom", m)
	}

	m = globalUnitMatcher.Match("memory_bytes")
	if m == nil || m.Unit != "bytes" {
		t.Errorf("Match(memory_bytes) = %v, want bytes (from user override)", m)
	}

	m = globalUnitMatcher.Match("go_gc_duration_seconds")
	if m == nil || m.Unit != "duration" {
		t.Errorf("Match(go_gc_duration_seconds) = %v, want duration (from defaults)", m)
	}
}

func TestInitPatternsInvalidFile(t *testing.T) {
	err := initPatterns("/nonexistent/path.yaml")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}
