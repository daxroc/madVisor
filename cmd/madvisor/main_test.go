package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// --- metricSeries tests ---

func newTestSeries(name string, labels map[string]string) *metricSeries {
	return &metricSeries{
		key:    seriesKey(name, labels),
		name:   name,
		labels: labels,
		values: make([]float64, ringSize),
	}
}

func TestMetricSeriesPush(t *testing.T) {
	s := newTestSeries("test", nil)
	s.push(1.0)
	s.push(2.0)
	s.push(3.0)

	if s.idx != 3 {
		t.Errorf("idx = %d, want 3", s.idx)
	}
	if s.full {
		t.Error("should not be full after 3 pushes")
	}
}

func TestMetricSeriesPushWraps(t *testing.T) {
	s := newTestSeries("test", nil)
	for i := 0; i < ringSize+5; i++ {
		s.push(float64(i))
	}
	if !s.full {
		t.Error("should be full after ringSize+5 pushes")
	}
	if s.idx != 5 {
		t.Errorf("idx = %d, want 5", s.idx)
	}
}

func TestMetricSeriesSlicePartial(t *testing.T) {
	s := newTestSeries("test", nil)
	s.push(10)
	s.push(20)
	s.push(30)

	got := s.slice()
	if len(got) != 3 {
		t.Fatalf("slice len = %d, want 3", len(got))
	}
	if got[0] != 10 || got[1] != 20 || got[2] != 30 {
		t.Errorf("slice = %v, want [10 20 30]", got)
	}
}

func TestMetricSeriesSliceFull(t *testing.T) {
	s := newTestSeries("test", nil)
	for i := 0; i < ringSize+3; i++ {
		s.push(float64(i))
	}

	got := s.slice()
	if len(got) != ringSize {
		t.Fatalf("slice len = %d, want %d", len(got), ringSize)
	}
	if got[0] != 3 {
		t.Errorf("first element = %f, want 3 (oldest after wrap)", got[0])
	}
	if got[ringSize-1] != float64(ringSize+2) {
		t.Errorf("last element = %f, want %f", got[ringSize-1], float64(ringSize+2))
	}
}

func TestMetricSeriesSliceIsCopy(t *testing.T) {
	s := newTestSeries("test", nil)
	s.push(1)
	s.push(2)

	got := s.slice()
	got[0] = 999

	again := s.slice()
	if again[0] == 999 {
		t.Error("slice should return a copy, not a reference")
	}
}

func TestMetricSeriesLast(t *testing.T) {
	s := newTestSeries("test", nil)

	if s.last() != 0 {
		t.Errorf("last() on empty = %f, want 0", s.last())
	}

	s.push(42)
	if s.last() != 42 {
		t.Errorf("last() = %f, want 42", s.last())
	}

	s.push(99)
	if s.last() != 99 {
		t.Errorf("last() = %f, want 99", s.last())
	}
}

func TestMetricSeriesLastWrapped(t *testing.T) {
	s := newTestSeries("test", nil)
	for i := 0; i < ringSize; i++ {
		s.push(float64(i))
	}
	if s.last() != float64(ringSize-1) {
		t.Errorf("last() = %f, want %f", s.last(), float64(ringSize-1))
	}

	s.push(999)
	if s.last() != 999 {
		t.Errorf("last() after wrap = %f, want 999", s.last())
	}
}

func TestMetricSeriesDisplayName(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"no labels", nil, "cpu"},
		{"single label", map[string]string{"env": "prod"}, `cpu{env="prod"}`},
		{"sorted labels", map[string]string{"method": "GET", "env": "prod"}, `cpu{env="prod",method="GET"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSeries("cpu", tt.labels)
			if got := s.displayName(); got != tt.want {
				t.Errorf("displayName() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- seriesKey tests ---

func TestSeriesKey(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"no labels", nil, "metric"},
		{"empty labels", map[string]string{}, "metric"},
		{"single", map[string]string{"a": "1"}, "metric{a=1}"},
		{"sorted", map[string]string{"b": "2", "a": "1"}, "metric{a=1,b=2}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := seriesKey("metric", tt.labels); got != tt.want {
				t.Errorf("seriesKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSeriesKeyDeterministic(t *testing.T) {
	labels := map[string]string{"z": "3", "a": "1", "m": "2"}
	k1 := seriesKey("x", labels)
	k2 := seriesKey("x", labels)
	if k1 != k2 {
		t.Errorf("seriesKey not deterministic: %q != %q", k1, k2)
	}
}

// --- store tests ---

func TestStoreUpdateAndGet(t *testing.T) {
	st := newStore()
	st.update("cpu", map[string]string{"env": "prod"}, "help", "gauge", 42)

	s := st.get("cpu{env=prod}")
	if s == nil {
		t.Fatal("get() returned nil for existing key")
	}
	if s.last() != 42 {
		t.Errorf("last() = %f, want 42", s.last())
	}
}

func TestStoreGetMissing(t *testing.T) {
	st := newStore()
	if got := st.get("nonexistent"); got != nil {
		t.Errorf("get() for missing key = %v, want nil", got)
	}
}

func TestStoreSnapshot(t *testing.T) {
	st := newStore()
	st.update("b_metric", nil, "", "gauge", 1)
	st.update("a_metric", nil, "", "gauge", 2)

	snap := st.snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(snap))
	}
	if snap[0].name != "a_metric" {
		t.Errorf("snapshot[0].name = %q, want a_metric (sorted)", snap[0].name)
	}
}

func TestStoreUpdateAccumulates(t *testing.T) {
	st := newStore()
	st.update("m", nil, "", "gauge", 1)
	st.update("m", nil, "", "gauge", 2)
	st.update("m", nil, "", "gauge", 3)

	s := st.get("m")
	if s == nil {
		t.Fatal("get() returned nil")
	}
	data := s.slice()
	if len(data) != 3 {
		t.Fatalf("slice len = %d, want 3", len(data))
	}
	if data[2] != 3 {
		t.Errorf("last value = %f, want 3", data[2])
	}
}

func TestStoreDistinctLabelSets(t *testing.T) {
	st := newStore()
	st.update("cpu", map[string]string{"env": "prod"}, "", "gauge", 10)
	st.update("cpu", map[string]string{"env": "staging"}, "", "gauge", 20)

	snap := st.snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d, want 2 (distinct label sets)", len(snap))
	}
}

// --- parseLabels tests ---

func TestParseLabels(t *testing.T) {
	tests := []struct {
		input      string
		wantName   string
		wantLabels map[string]string
	}{
		{`cpu_usage`, "cpu_usage", nil},
		{`cpu_usage{}`, "cpu_usage", map[string]string{}},
		{`http_requests{method="GET"}`, "http_requests", map[string]string{"method": "GET"}},
		{`http_requests{method="GET",path="/api"}`, "http_requests", map[string]string{"method": "GET", "path": "/api"}},
		{`m{a="1", b="2"}`, "m", map[string]string{"a": "1", "b": "2"}},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			name, labels := parseLabels(tt.input)
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
			if tt.wantLabels == nil {
				if labels != nil {
					t.Errorf("labels = %v, want nil", labels)
				}
				return
			}
			for k, v := range tt.wantLabels {
				if labels[k] != v {
					t.Errorf("labels[%q] = %q, want %q", k, labels[k], v)
				}
			}
		})
	}
}

// --- parseTargets tests ---

func TestParseTargets(t *testing.T) {
	os.Setenv("METRIC_TARGETS", "host1:8080,host2:9090")
	defer os.Unsetenv("METRIC_TARGETS")

	got := parseTargets()
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0] != "host1:8080" || got[1] != "host2:9090" {
		t.Errorf("parseTargets() = %v", got)
	}
}

func TestParseTargetsDefault(t *testing.T) {
	os.Unsetenv("METRIC_TARGETS")

	got := parseTargets()
	if len(got) != 1 || got[0] != "localhost:8080" {
		t.Errorf("parseTargets() default = %v, want [localhost:8080]", got)
	}
}

func TestParseTargetsTrimsWhitespace(t *testing.T) {
	os.Setenv("METRIC_TARGETS", " host1:8080 , host2:9090 ")
	defer os.Unsetenv("METRIC_TARGETS")

	got := parseTargets()
	if len(got) != 2 || got[0] != "host1:8080" || got[1] != "host2:9090" {
		t.Errorf("parseTargets() = %v", got)
	}
}

func TestParseTargetsSkipsEmpty(t *testing.T) {
	os.Setenv("METRIC_TARGETS", "host1:8080,,host2:9090,")
	defer os.Unsetenv("METRIC_TARGETS")

	got := parseTargets()
	if len(got) != 2 {
		t.Errorf("parseTargets() len = %d, want 2 (skip empties)", len(got))
	}
}

// --- uiState tests ---

func TestUIStateSetKeys(t *testing.T) {
	u := &uiState{}
	u.setKeys([]string{"a", "b", "c"})

	filtered, selIdx, _, _ := u.snapshot()
	if len(filtered) != 3 {
		t.Fatalf("filtered len = %d, want 3", len(filtered))
	}
	if selIdx != 0 {
		t.Errorf("selIdx = %d, want 0", selIdx)
	}
}

func TestUIStateNavigation(t *testing.T) {
	u := &uiState{}
	u.setKeys([]string{"a", "b", "c"})

	u.moveDown()
	if u.selectedKey() != "b" {
		t.Errorf("after moveDown: selectedKey = %q, want b", u.selectedKey())
	}

	u.moveDown()
	if u.selectedKey() != "c" {
		t.Errorf("after 2x moveDown: selectedKey = %q, want c", u.selectedKey())
	}

	u.moveDown()
	if u.selectedKey() != "c" {
		t.Errorf("moveDown past end: selectedKey = %q, want c (clamped)", u.selectedKey())
	}

	u.moveUp()
	if u.selectedKey() != "b" {
		t.Errorf("after moveUp: selectedKey = %q, want b", u.selectedKey())
	}
}

func TestUIStateMoveUpAtTop(t *testing.T) {
	u := &uiState{}
	u.setKeys([]string{"a", "b"})
	u.moveUp()
	if u.selectedKey() != "a" {
		t.Errorf("moveUp at top: selectedKey = %q, want a", u.selectedKey())
	}
}

func TestUIStateFilter(t *testing.T) {
	u := &uiState{}
	u.setKeys([]string{"cpu_usage{env=prod}", "cpu_usage{env=staging}", "memory_usage{env=prod}"})

	u.startFilter()
	u.addFilterChar('c')
	u.addFilterChar('p')
	u.addFilterChar('u')

	filtered, _, filter, fm := u.snapshot()
	if filter != "cpu" {
		t.Errorf("filter = %q, want cpu", filter)
	}
	if !fm {
		t.Error("filterMode should be true")
	}
	if len(filtered) != 2 {
		t.Errorf("filtered len = %d, want 2 (cpu matches)", len(filtered))
	}
}

func TestUIStateFilterCaseInsensitive(t *testing.T) {
	u := &uiState{}
	u.setKeys([]string{"CPU_usage", "memory"})

	u.startFilter()
	u.addFilterChar('c')
	u.addFilterChar('p')
	u.addFilterChar('u')

	filtered, _, _, _ := u.snapshot()
	if len(filtered) != 1 {
		t.Errorf("filtered len = %d, want 1 (case insensitive)", len(filtered))
	}
}

func TestUIStateBackspaceFilter(t *testing.T) {
	u := &uiState{}
	u.setKeys([]string{"abc", "def"})

	u.startFilter()
	u.addFilterChar('a')
	u.addFilterChar('b')

	filtered, _, _, _ := u.snapshot()
	if len(filtered) != 1 {
		t.Fatalf("after 'ab': filtered = %d, want 1", len(filtered))
	}

	u.backspaceFilter()
	filtered, _, filter, _ := u.snapshot()
	if filter != "a" {
		t.Errorf("after backspace: filter = %q, want a", filter)
	}
	if len(filtered) != 1 {
		t.Errorf("after backspace: filtered = %d, want 1", len(filtered))
	}
}

func TestUIStateClearFilter(t *testing.T) {
	u := &uiState{}
	u.setKeys([]string{"a", "b", "c"})

	u.startFilter()
	u.addFilterChar('a')
	u.clearFilter()

	filtered, _, filter, fm := u.snapshot()
	if filter != "" {
		t.Errorf("after clear: filter = %q, want empty", filter)
	}
	if fm {
		t.Error("after clear: filterMode should be false")
	}
	if len(filtered) != 3 {
		t.Errorf("after clear: filtered = %d, want 3", len(filtered))
	}
}

func TestUIStateFilterClampsSelection(t *testing.T) {
	u := &uiState{}
	u.setKeys([]string{"a", "b", "c"})
	u.moveDown()
	u.moveDown()

	if u.selectedKey() != "c" {
		t.Fatalf("selectedKey = %q, want c", u.selectedKey())
	}

	u.startFilter()
	u.addFilterChar('a')

	_, selIdx, _, _ := u.snapshot()
	if selIdx != 0 {
		t.Errorf("selIdx should clamp to 0 when filter shrinks list, got %d", selIdx)
	}
}

func TestUIStateEmptyKeys(t *testing.T) {
	u := &uiState{}
	if u.selectedKey() != "" {
		t.Errorf("selectedKey on empty = %q, want empty", u.selectedKey())
	}
	u.moveDown()
	u.moveUp()
	if u.selectedKey() != "" {
		t.Errorf("selectedKey after nav on empty = %q, want empty", u.selectedKey())
	}
}

// --- colorForIndex tests ---

func TestColorForIndex(t *testing.T) {
	first := colorForIndex(0)
	wrapped := colorForIndex(7)
	if first != wrapped {
		t.Errorf("colorForIndex should wrap: index 0 = %v, index 7 = %v", first, wrapped)
	}
}

// --- scrapeTarget integration test ---

func TestScrapeTargetParsesPrometheus(t *testing.T) {
	body := `# HELP http_requests_total Total requests
# TYPE http_requests_total counter
http_requests_total{method="GET",path="/api"} 42.5
http_requests_total{method="POST",path="/api"} 10.0
# HELP cpu_usage CPU usage
# TYPE cpu_usage gauge
cpu_usage{env="prod"} 65.3
cpu_usage 50.0
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	st := newStore()
	client := &http.Client{}
	target := strings.TrimPrefix(srv.URL, "http://")
	scrapeTarget(client, target, st)

	snap := st.snapshot()
	if len(snap) != 4 {
		t.Fatalf("snapshot len = %d, want 4", len(snap))
	}

	s := st.get("http_requests_total{method=GET,path=/api}")
	if s == nil {
		t.Fatal("missing http_requests_total{method=GET,path=/api}")
	}
	if s.last() != 42.5 {
		t.Errorf("value = %f, want 42.5", s.last())
	}
	if s.help != "Total requests" {
		t.Errorf("help = %q, want 'Total requests'", s.help)
	}
	if s.mtype != "counter" {
		t.Errorf("mtype = %q, want counter", s.mtype)
	}

	bare := st.get("cpu_usage")
	if bare == nil {
		t.Fatal("missing cpu_usage (no labels)")
	}
	if bare.last() != 50.0 {
		t.Errorf("cpu_usage value = %f, want 50.0", bare.last())
	}
}

func TestScrapeTargetHandlesError(t *testing.T) {
	st := newStore()
	client := &http.Client{}
	scrapeTarget(client, "localhost:1", st)

	if len(st.snapshot()) != 0 {
		t.Error("scrapeTarget should not populate store on connection error")
	}
}
