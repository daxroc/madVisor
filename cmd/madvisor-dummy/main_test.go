package main

import (
	"strings"
	"testing"
)

func TestLabelsStr(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"nil labels", nil, ""},
		{"empty labels", map[string]string{}, ""},
		{"single label", map[string]string{"method": "GET"}, `{method="GET"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := labelsStr(tt.labels)
			if tt.want != "" && got == "" {
				t.Errorf("labelsStr(%v) = %q, want non-empty", tt.labels, got)
			}
			if tt.want == "" && got != "" {
				t.Errorf("labelsStr(%v) = %q, want empty", tt.labels, got)
			}
			if len(tt.labels) == 1 && got != tt.want {
				t.Errorf("labelsStr(%v) = %q, want %q", tt.labels, got, tt.want)
			}
		})
	}
}

func TestLabelsStrMultipleKeys(t *testing.T) {
	labels := map[string]string{"method": "GET", "path": "/api"}
	got := labelsStr(labels)

	if !strings.HasPrefix(got, "{") || !strings.HasSuffix(got, "}") {
		t.Fatalf("labelsStr should be wrapped in braces, got %q", got)
	}
	if !strings.Contains(got, `method="GET"`) {
		t.Errorf("labelsStr missing method label, got %q", got)
	}
	if !strings.Contains(got, `path="/api"`) {
		t.Errorf("labelsStr missing path label, got %q", got)
	}
}

func TestLabelsMatch(t *testing.T) {
	tests := []struct {
		name string
		a, b map[string]string
		want bool
	}{
		{"both nil", nil, nil, true},
		{"both empty", map[string]string{}, map[string]string{}, true},
		{"equal", map[string]string{"a": "1"}, map[string]string{"a": "1"}, true},
		{"different values", map[string]string{"a": "1"}, map[string]string{"a": "2"}, false},
		{"different keys", map[string]string{"a": "1"}, map[string]string{"b": "1"}, false},
		{"different lengths", map[string]string{"a": "1"}, map[string]string{"a": "1", "b": "2"}, false},
		{"one nil", nil, map[string]string{"a": "1"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := labelsMatch(tt.a, tt.b); got != tt.want {
				t.Errorf("labelsMatch(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestGauge(t *testing.T) {
	for i := 0; i < 100; i++ {
		v := gauge(float64(i), 10, 5, 3, 1)
		if v < 0 {
			t.Errorf("gauge returned negative value: %f", v)
		}
	}
}

func TestMetricsTick(t *testing.T) {
	m := &metrics{}
	m.tick()

	if len(m.series) == 0 {
		t.Fatal("tick() should populate series on first call")
	}

	counters := 0
	gauges := 0
	for _, s := range m.series {
		if s.counter {
			counters++
		} else {
			gauges++
		}
	}

	if counters == 0 {
		t.Error("expected at least one counter series")
	}
	if gauges == 0 {
		t.Error("expected at least one gauge series")
	}

	expectedCounters := 4 * 4 // methods * paths
	if counters != expectedCounters {
		t.Errorf("expected %d counter series (http_requests_total), got %d", expectedCounters, counters)
	}
}

func TestMetricsTickIncrementsCounters(t *testing.T) {
	m := &metrics{}
	m.tick()

	initial := make(map[string]float64)
	for _, s := range m.series {
		if s.counter {
			key := s.name + labelsStr(s.labels)
			initial[key] = s.value
		}
	}

	m.tick()

	for _, s := range m.series {
		if s.counter {
			key := s.name + labelsStr(s.labels)
			if s.value < initial[key] {
				t.Errorf("counter %s decreased: %f -> %f", key, initial[key], s.value)
			}
		}
	}
}

func TestMetricsRender(t *testing.T) {
	m := &metrics{}
	m.tick()

	output := m.render()

	if output == "" {
		t.Fatal("render() returned empty string after tick()")
	}
	if !strings.Contains(output, "# HELP") {
		t.Error("render() output missing # HELP lines")
	}
	if !strings.Contains(output, "# TYPE") {
		t.Error("render() output missing # TYPE lines")
	}
	if !strings.Contains(output, "http_requests_total") {
		t.Error("render() output missing http_requests_total")
	}
	if !strings.Contains(output, "cpu_usage_percent") {
		t.Error("render() output missing cpu_usage_percent")
	}
	if !strings.Contains(output, "counter") {
		t.Error("render() output missing counter type")
	}
	if !strings.Contains(output, "gauge") {
		t.Error("render() output missing gauge type")
	}
}

func TestMetricsRenderPrometheusFormat(t *testing.T) {
	m := &metrics{}
	m.tick()

	output := m.render()
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		spaceIdx := strings.LastIndex(line, " ")
		if spaceIdx < 0 {
			t.Errorf("metric line missing space separator: %q", line)
			continue
		}
		valStr := line[spaceIdx+1:]
		if valStr == "" {
			t.Errorf("metric line has empty value: %q", line)
		}
	}
}

func TestMetricsRenderContainsLabels(t *testing.T) {
	m := &metrics{}
	m.tick()

	output := m.render()

	if !strings.Contains(output, `method="GET"`) {
		t.Error("render() should contain method labels")
	}
	if !strings.Contains(output, `env="prod"`) {
		t.Error("render() should contain env labels")
	}
}
