package main

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// --- metricSeries tests ---

func newTestSeries(name string, labels map[string]string) *metricSeries {
	return &metricSeries{
		key:    seriesKey(name, labels),
		name:   name,
		labels: labels,
		values: make([]float64, ringSize),
		times:  make([]time.Time, ringSize),
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

	filtered, selIdx, _, _, _ := u.snapshot()
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

	filtered, _, _, filter, fm := u.snapshot()
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

	filtered, _, _, _, _ := u.snapshot()
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

	filtered, _, _, _, _ := u.snapshot()
	if len(filtered) != 1 {
		t.Fatalf("after 'ab': filtered = %d, want 1", len(filtered))
	}

	u.backspaceFilter()
	filtered, _, _, filter, _ := u.snapshot()
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

	filtered, _, _, filter, fm := u.snapshot()
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

	_, selIdx, _, _, _ := u.snapshot()
	if selIdx != 0 {
		t.Errorf("selIdx should clamp to 0 when filter shrinks list, got %d", selIdx)
	}
}

func TestUIStateScrollOffset(t *testing.T) {
	u := &uiState{pageSize: 5}
	keys := make([]string, 20)
	for i := range keys {
		keys[i] = fmt.Sprintf("metric_%02d", i)
	}
	u.setKeys(keys)

	_, _, scrollOff, _, _ := u.snapshot()
	if scrollOff != 0 {
		t.Errorf("initial scrollOff = %d, want 0", scrollOff)
	}

	for i := 0; i < 7; i++ {
		u.moveDown()
	}
	_, selIdx, scrollOff, _, _ := u.snapshot()
	if selIdx != 7 {
		t.Errorf("selIdx = %d, want 7", selIdx)
	}
	if scrollOff != 3 {
		t.Errorf("scrollOff = %d, want 3 (selIdx 7 - pageSize 5 + 1)", scrollOff)
	}

	for i := 0; i < 7; i++ {
		u.moveUp()
	}
	_, selIdx, scrollOff, _, _ = u.snapshot()
	if selIdx != 0 {
		t.Errorf("selIdx after moveUp = %d, want 0", selIdx)
	}
	if scrollOff != 0 {
		t.Errorf("scrollOff after moveUp = %d, want 0", scrollOff)
	}
}

func TestUIStateScrollResetOnFilter(t *testing.T) {
	u := &uiState{pageSize: 3}
	keys := make([]string, 10)
	for i := range keys {
		keys[i] = fmt.Sprintf("metric_%02d", i)
	}
	u.setKeys(keys)

	for i := 0; i < 8; i++ {
		u.moveDown()
	}
	_, _, scrollOff, _, _ := u.snapshot()
	if scrollOff == 0 {
		t.Fatal("scrollOff should be non-zero after navigating down")
	}

	u.startFilter()
	u.addFilterChar('0')
	u.addFilterChar('1')

	_, selIdx, scrollOff, _, _ := u.snapshot()
	if selIdx != 0 {
		t.Errorf("selIdx after filter = %d, want 0", selIdx)
	}
	if scrollOff != 0 {
		t.Errorf("scrollOff after filter = %d, want 0", scrollOff)
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

// --- formatValue tests ---

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		val  float64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.00 KiB"},
		{1536, "1.50 KiB"},
		{1048576, "1.00 MiB"},
		{1073741824, "1.00 GiB"},
		{1099511627776, "1.00 TiB"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := formatBytes(tt.val); got != tt.want {
				t.Errorf("formatBytes(%f) = %q, want %q", tt.val, got, tt.want)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		sec  float64
		want string
	}{
		{0.0000001, "100ns"},
		{0.00035, "350.0µs"},
		{0.045, "45.0ms"},
		{1.5, "1.50s"},
		{90, "1.5m"},
		{5400, "1.5h"},
		{172800, "2.0d"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := formatDuration(tt.sec); got != tt.want {
				t.Errorf("formatDuration(%f) = %q, want %q", tt.sec, got, tt.want)
			}
		})
	}
}

func TestFormatCount(t *testing.T) {
	tests := []struct {
		val  float64
		want string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1500, "1.50k"},
		{2500000, "2.50M"},
		{3500000000, "3.50G"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := formatCount(tt.val); got != tt.want {
				t.Errorf("formatCount(%f) = %q, want %q", tt.val, got, tt.want)
			}
		})
	}
}

func TestFormatGeneric(t *testing.T) {
	tests := []struct {
		val  float64
		want string
	}{
		{0, "0"},
		{0.005, "0.0050"},
		{0.15, "0.150"},
		{42.5, "42.50"},
		{1500, "1.50k"},
		{2500000, "2.50M"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := formatGeneric(tt.val); got != tt.want {
				t.Errorf("formatGeneric(%f) = %q, want %q", tt.val, got, tt.want)
			}
		})
	}
}

func TestFormatValueDispatch(t *testing.T) {
	tests := []struct {
		name string
		val  float64
		want string
	}{
		{"memory_usage_bytes", 1048576, "1.00 MiB"},
		{"memory_usage_megabytes", 256, "256.00 MiB"},
		{"memory_usage_kilobytes", 1024, "1.00 MiB"},
		{"request_duration_seconds", 0.045, "45.0ms"},
		{"request_duration_milliseconds", 45, "45.0ms"},
		{"request_duration_ms", 45, "45.0ms"},
		{"cpu_usage_percent", 65.3, "65.3%"},
		{"http_requests_total", 1500, "1.50k"},
		{"active_connections", 42.5, "42.50"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatValue(tt.name, tt.val); got != tt.want {
				t.Errorf("formatValue(%q, %f) = %q, want %q", tt.name, tt.val, got, tt.want)
			}
		})
	}
}

func TestYAxisFormatter(t *testing.T) {
	f := yAxisFormatter("memory_usage_bytes")
	if got := f(1048576); got != "1.00 MiB" {
		t.Errorf("yAxisFormatter bytes(1048576) = %q, want '1.00 MiB'", got)
	}

	f2 := yAxisFormatter("request_duration_seconds")
	if got := f2(0.045); got != "45.0ms" {
		t.Errorf("yAxisFormatter seconds(0.045) = %q, want '45.0ms'", got)
	}
}

func TestYAxisFormatterNaN(t *testing.T) {
	f := yAxisFormatter("cpu_bytes")
	if got := f(math.NaN()); got != "" {
		t.Errorf("yAxisFormatter(NaN) = %q, want empty", got)
	}
}

func TestUnitSuffix(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"memory_usage_bytes", " [bytes]"},
		{"memory_usage_megabytes", " [bytes]"},
		{"memory_usage_kilobytes", " [bytes]"},
		{"request_duration_seconds", " [duration]"},
		{"request_duration_milliseconds", " [duration]"},
		{"latency_ms", " [duration]"},
		{"cpu_usage_percent", " [%]"},
		{"http_requests_total", " [count]"},
		{"active_connections", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := unitSuffix(tt.name); got != tt.want {
				t.Errorf("unitSuffix(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

// --- rate tests ---

func TestIsCounter(t *testing.T) {
	tests := []struct {
		name  string
		mtype string
		want  bool
	}{
		{"http_requests_total", "counter", true},
		{"http_requests_total", "gauge", true},
		{"http_requests_total", "", true},
		{"some_metric", "counter", true},
		{"some_metric", "gauge", false},
		{"memory_bytes", "", false},
	}
	for _, tt := range tests {
		s := &metricSeries{name: tt.name, mtype: tt.mtype, values: make([]float64, ringSize), times: make([]time.Time, ringSize)}
		if got := s.isCounter(); got != tt.want {
			t.Errorf("isCounter(name=%q, mtype=%q) = %v, want %v", tt.name, tt.mtype, got, tt.want)
		}
	}
}

func TestCount(t *testing.T) {
	s := newTestSeries("test", nil)
	if s.count() != 0 {
		t.Errorf("count on empty = %d, want 0", s.count())
	}
	s.push(1)
	s.push(2)
	if s.count() != 2 {
		t.Errorf("count after 2 pushes = %d, want 2", s.count())
	}
}

func TestRateBasic(t *testing.T) {
	s := &metricSeries{
		name:   "http_requests_total",
		mtype:  "counter",
		values: make([]float64, ringSize),
		times:  make([]time.Time, ringSize),
	}

	base := time.Now()
	s.pushAt(100, base)
	s.pushAt(110, base.Add(1*time.Second))
	s.pushAt(120, base.Add(2*time.Second))
	s.pushAt(130, base.Add(3*time.Second))
	s.pushAt(140, base.Add(4*time.Second))
	s.pushAt(150, base.Add(5*time.Second))

	r := s.rate(5 * time.Second)
	if r < 9.9 || r > 10.1 {
		t.Errorf("rate(5s) = %f, want ~10.0", r)
	}
}

func TestRateShortWindow(t *testing.T) {
	s := &metricSeries{
		name:   "req_total",
		mtype:  "counter",
		values: make([]float64, ringSize),
		times:  make([]time.Time, ringSize),
	}

	base := time.Now()
	s.pushAt(0, base)
	s.pushAt(10, base.Add(1*time.Second))
	s.pushAt(20, base.Add(2*time.Second))
	s.pushAt(50, base.Add(3*time.Second))

	r := s.rate(2 * time.Second)
	if r < 19.9 || r > 20.1 {
		t.Errorf("rate(2s) = %f, want ~20.0 (last 2s: 20→50)", r)
	}
}

func TestRateSingleSample(t *testing.T) {
	s := newTestSeries("counter_total", nil)
	s.pushAt(100, time.Now())

	if r := s.rate(5 * time.Second); r != 0 {
		t.Errorf("rate with 1 sample = %f, want 0", r)
	}
}

func TestRateCounterReset(t *testing.T) {
	s := &metricSeries{
		name:   "req_total",
		mtype:  "counter",
		values: make([]float64, ringSize),
		times:  make([]time.Time, ringSize),
	}

	base := time.Now()
	s.pushAt(100, base)
	s.pushAt(50, base.Add(1*time.Second))

	r := s.rate(5 * time.Second)
	if r != 0 {
		t.Errorf("rate on counter reset = %f, want 0 (negative delta clamped)", r)
	}
}

func TestRateSlice(t *testing.T) {
	s := &metricSeries{
		name:   "req_total",
		mtype:  "counter",
		values: make([]float64, ringSize),
		times:  make([]time.Time, ringSize),
	}

	base := time.Now()
	s.pushAt(0, base)
	s.pushAt(10, base.Add(1*time.Second))
	s.pushAt(30, base.Add(2*time.Second))
	s.pushAt(60, base.Add(3*time.Second))

	rates := s.rateSlice(5 * time.Second)
	if len(rates) != 3 {
		t.Fatalf("rateSlice len = %d, want 3", len(rates))
	}

	expected := []float64{10, 20, 30}
	for i, want := range expected {
		if rates[i] < want-0.1 || rates[i] > want+0.1 {
			t.Errorf("rateSlice[%d] = %f, want ~%f", i, rates[i], want)
		}
	}
}

func TestRateSliceTooFew(t *testing.T) {
	s := newTestSeries("x_total", nil)
	s.pushAt(100, time.Now())
	if rates := s.rateSlice(5 * time.Second); rates != nil {
		t.Errorf("rateSlice with 1 sample = %v, want nil", rates)
	}
}

func TestRateSliceCounterReset(t *testing.T) {
	s := &metricSeries{
		name:   "req_total",
		mtype:  "counter",
		values: make([]float64, ringSize),
		times:  make([]time.Time, ringSize),
	}

	base := time.Now()
	s.pushAt(100, base)
	s.pushAt(50, base.Add(1*time.Second))
	s.pushAt(60, base.Add(2*time.Second))

	rates := s.rateSlice(5 * time.Second)
	if len(rates) != 2 {
		t.Fatalf("rateSlice len = %d, want 2", len(rates))
	}
	if rates[0] != 0 {
		t.Errorf("rateSlice[0] on reset = %f, want 0", rates[0])
	}
	if rates[1] < 9.9 || rates[1] > 10.1 {
		t.Errorf("rateSlice[1] after reset = %f, want ~10", rates[1])
	}
}

func TestRateAxisFormatter(t *testing.T) {
	f := rateAxisFormatter()
	if got := f(42.5); got != "42.50/s" {
		t.Errorf("rateAxisFormatter(42.5) = %q, want '42.50/s'", got)
	}
	if got := f(math.NaN()); got != "" {
		t.Errorf("rateAxisFormatter(NaN) = %q, want empty", got)
	}
}

func TestRateWindowUpDown(t *testing.T) {
	rateWindowSet(5 * time.Second)
	defer rateWindowSet(defaultRateWindow)

	if got := rateWindowGet(); got != 5*time.Second {
		t.Fatalf("initial rateWindow = %s, want 5s", got)
	}

	got := rateWindowUp()
	if got != 10*time.Second {
		t.Errorf("rateWindowUp from 5s = %s, want 10s", got)
	}
	if rateWindowGet() != 10*time.Second {
		t.Errorf("rateWindowGet after Up = %s, want 10s", rateWindowGet())
	}

	got = rateWindowDown()
	if got != 5*time.Second {
		t.Errorf("rateWindowDown from 10s = %s, want 5s", got)
	}

	rateWindowSet(1 * time.Second)
	got = rateWindowDown()
	if got != 1*time.Second {
		t.Errorf("rateWindowDown at min = %s, want 1s (clamped)", got)
	}

	rateWindowSet(60 * time.Second)
	got = rateWindowUp()
	if got != 60*time.Second {
		t.Errorf("rateWindowUp at max = %s, want 60s (clamped)", got)
	}
}

func TestRateWindowSetSnaps(t *testing.T) {
	defer rateWindowSet(defaultRateWindow)

	rateWindowSet(3 * time.Second)
	if got := rateWindowGet(); got != 5*time.Second {
		t.Errorf("rateWindowSet(3s) snapped to %s, want 5s", got)
	}

	rateWindowSet(100 * time.Second)
	if got := rateWindowGet(); got != 60*time.Second {
		t.Errorf("rateWindowSet(100s) snapped to %s, want 60s", got)
	}
}

func TestParseRateWindow(t *testing.T) {
	defer rateWindowSet(defaultRateWindow)

	os.Setenv("RATE_WINDOW", "10s")
	defer os.Unsetenv("RATE_WINDOW")
	parseRateWindow()
	if got := rateWindowGet(); got != 10*time.Second {
		t.Errorf("rateWindow = %s, want 10s", got)
	}

	os.Setenv("RATE_WINDOW", "invalid")
	rateWindowSet(defaultRateWindow)
	parseRateWindow()
	if got := rateWindowGet(); got != defaultRateWindow {
		t.Errorf("rateWindow = %s, want default %s on invalid input", got, defaultRateWindow)
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
