package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mum4k/termdash"
	"github.com/mum4k/termdash/cell"
	"github.com/mum4k/termdash/container"
	"github.com/mum4k/termdash/container/grid"
	"github.com/mum4k/termdash/keyboard"
	"github.com/mum4k/termdash/linestyle"
	"github.com/mum4k/termdash/terminal/tcell"
	"github.com/mum4k/termdash/terminal/terminalapi"
	"github.com/mum4k/termdash/widgets/linechart"
	"github.com/mum4k/termdash/widgets/text"
	"golang.org/x/term"
)

var (
	version = "dev"
	commit  = "unknown"
	branch  = "unknown"
)

const (
	ringSize          = 120
	scrapeInterval    = 1 * time.Second
	refreshInterval   = 250 * time.Millisecond
	defaultRateWindow = 5 * time.Second
)

var rateWindowSteps = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	15 * time.Second,
	30 * time.Second,
	60 * time.Second,
}

type rateWindowState struct {
	mu  sync.Mutex
	idx int
}

var rws = rateWindowState{idx: 2}

func rateWindowGet() time.Duration {
	rws.mu.Lock()
	defer rws.mu.Unlock()
	return rateWindowSteps[rws.idx]
}

func rateWindowUp() time.Duration {
	rws.mu.Lock()
	defer rws.mu.Unlock()
	if rws.idx < len(rateWindowSteps)-1 {
		rws.idx++
	}
	return rateWindowSteps[rws.idx]
}

func rateWindowDown() time.Duration {
	rws.mu.Lock()
	defer rws.mu.Unlock()
	if rws.idx > 0 {
		rws.idx--
	}
	return rateWindowSteps[rws.idx]
}

func rateWindowSet(d time.Duration) {
	rws.mu.Lock()
	defer rws.mu.Unlock()
	for i, s := range rateWindowSteps {
		if s >= d {
			rws.idx = i
			return
		}
	}
	rws.idx = len(rateWindowSteps) - 1
}

const logo = `
                        ██╗   ██╗██╗███████╗ ██████╗ ██████╗
   ███╗███╗  ███╗███╗   ██║   ██║██║██╔════╝██╔═══██╗██╔══██╗
   ████████╗ ████████╗  ██║   ██║██║███████╗ ██║   ██║██████╔╝
   ██╔█╔███║ ██╔█╔███║  ╚██╗ ██╔╝██║╚════██║██║   ██║██╔══██╗
   ██║╚╝███║ ██║╚╝███║   ╚████╔╝ ██║███████║╚██████╔╝██║  ██║
   ╚═╝  ╚══╝ ╚═╝  ╚══╝    ╚═══╝  ╚═╝╚══════╝ ╚═════╝ ╚═╝  ╚═╝

              m a d V i s o r
         real-time pod metric visualizer
`

// --- ring buffer series ---

type metricSeries struct {
	key    string
	name   string
	labels map[string]string
	help   string
	mtype  string
	values []float64
	times  []time.Time
	idx    int
	full   bool
}

func (s *metricSeries) push(v float64) {
	now := time.Now()
	s.values[s.idx] = v
	s.times[s.idx] = now
	s.idx = (s.idx + 1) % ringSize
	if s.idx == 0 {
		s.full = true
	}
}

func (s *metricSeries) pushAt(v float64, t time.Time) {
	s.values[s.idx] = v
	s.times[s.idx] = t
	s.idx = (s.idx + 1) % ringSize
	if s.idx == 0 {
		s.full = true
	}
}

func detectMetricType(name, mtype string) string {
	if mtype != "" {
		return mtype
	}
	return "gauge"
}

func metricTypeBadge(mtype string) string {
	switch mtype {
	case "counter":
		return "[C]"
	case "gauge":
		return "[G]"
	case "histogram":
		return "[H]"
	case "summary":
		return "[S]"
	default:
		return "[?]"
	}
}

func (s *metricSeries) detectedType() string {
	return detectMetricType(s.name, s.mtype)
}

func (s *metricSeries) isCounter() bool {
	return s.detectedType() == "counter"
}

func matchUnit(name string) *UnitMatch {
	if globalUnitMatcher != nil {
		return globalUnitMatcher.Match(name)
	}
	return nil
}

func isTimestampMetric(name string) bool {
	m := matchUnit(name)
	return m != nil && m.Unit == "timestamp"
}

func (s *metricSeries) shouldRate() bool {
	dt := s.detectedType()
	if dt == "counter" {
		return true
	}
	if dt == "summary" && (strings.HasSuffix(s.name, "_count") || strings.HasSuffix(s.name, "_sum")) {
		return true
	}
	if dt == "histogram" && (strings.HasSuffix(s.name, "_count") || strings.HasSuffix(s.name, "_sum")) {
		return true
	}
	return false
}

func (s *metricSeries) rate(window time.Duration) float64 {
	n := s.count()
	if n < 2 {
		return 0
	}

	newestIdx := s.idx - 1
	if newestIdx < 0 {
		newestIdx = ringSize - 1
	}
	newest := s.values[newestIdx]
	newestT := s.times[newestIdx]
	cutoff := newestT.Add(-window)

	oldestIdx := newestIdx
	oldestVal := newest
	oldestT := newestT

	for j := 1; j < n; j++ {
		i := newestIdx - j
		if i < 0 {
			i += ringSize
		}
		if s.times[i].Before(cutoff) {
			break
		}
		oldestIdx = i
		oldestVal = s.values[i]
		oldestT = s.times[i]
	}
	_ = oldestIdx

	elapsed := newestT.Sub(oldestT).Seconds()
	if elapsed <= 0 {
		return 0
	}
	delta := newest - oldestVal
	if delta < 0 {
		return 0
	}
	return delta / elapsed
}

func (s *metricSeries) count() int {
	if s.full {
		return ringSize
	}
	return s.idx
}

func (s *metricSeries) rateSlice(window time.Duration) []float64 {
	n := s.count()
	if n < 2 {
		return nil
	}

	start := 0
	if s.full {
		start = s.idx
	}

	rates := make([]float64, 0, n-1)
	prevVal := s.values[start%ringSize]
	prevT := s.times[start%ringSize]

	for j := 1; j < n; j++ {
		i := (start + j) % ringSize
		dt := s.times[i].Sub(prevT).Seconds()
		var r float64
		if dt > 0 {
			delta := s.values[i] - prevVal
			if delta < 0 {
				delta = 0
			}
			r = delta / dt
		}
		rates = append(rates, r)
		prevVal = s.values[i]
		prevT = s.times[i]
	}
	return rates
}

func (s *metricSeries) slice() []float64 {
	if !s.full {
		return append([]float64{}, s.values[:s.idx]...)
	}
	out := make([]float64, ringSize)
	copy(out, s.values[s.idx:])
	copy(out[ringSize-s.idx:], s.values[:s.idx])
	return out
}

func (s *metricSeries) last() float64 {
	if s.idx == 0 && !s.full {
		return 0
	}
	i := s.idx - 1
	if i < 0 {
		i = ringSize - 1
	}
	return s.values[i]
}

func (s *metricSeries) displayName() string {
	if len(s.labels) == 0 {
		return s.name
	}
	parts := make([]string, 0, len(s.labels))
	keys := make([]string, 0, len(s.labels))
	for k := range s.labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, k, s.labels[k]))
	}
	return s.name + "{" + strings.Join(parts, ",") + "}"
}

// --- value formatting ---

func formatValue(name string, v float64) string {
	m := matchUnit(name)
	if m == nil {
		return formatGeneric(v)
	}
	switch m.Unit {
	case "bytes":
		return formatBytes(v)
	case "megabytes":
		return formatBytes(v * 1024 * 1024)
	case "kilobytes":
		return formatBytes(v * 1024)
	case "duration":
		return formatDuration(v)
	case "duration_ms":
		return formatDuration(v / 1000)
	case "percent":
		return fmt.Sprintf("%.1f%%", v)
	case "timestamp":
		return formatTimestamp(v)
	case "count":
		return formatCount(v)
	default:
		return formatGeneric(v)
	}
}

func formatBytes(b float64) string {
	switch {
	case b >= 1<<40:
		return fmt.Sprintf("%.2f TiB", b/(1<<40))
	case b >= 1<<30:
		return fmt.Sprintf("%.2f GiB", b/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.2f MiB", b/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.2f KiB", b/(1<<10))
	default:
		return fmt.Sprintf("%.0f B", b)
	}
}

func formatDuration(sec float64) string {
	switch {
	case sec >= 86400:
		return fmt.Sprintf("%.1fd", sec/86400)
	case sec >= 3600:
		return fmt.Sprintf("%.1fh", sec/3600)
	case sec >= 60:
		return fmt.Sprintf("%.1fm", sec/60)
	case sec >= 1:
		return fmt.Sprintf("%.2fs", sec)
	case sec >= 0.001:
		return fmt.Sprintf("%.1fms", sec*1000)
	case sec >= 0.000001:
		return fmt.Sprintf("%.1fµs", sec*1e6)
	default:
		return fmt.Sprintf("%.0fns", sec*1e9)
	}
}

func formatCount(v float64) string {
	switch {
	case v >= 1e9:
		return fmt.Sprintf("%.2fG", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%.2fM", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%.2fk", v/1e3)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

func formatTimestamp(v float64) string {
	if v == 0 {
		return "0"
	}
	sec := int64(v)
	nsec := int64((v - float64(sec)) * 1e9)
	t := time.Unix(sec, nsec).UTC()
	now := time.Now().UTC()
	diff := now.Sub(t)

	if diff < 0 {
		return fmt.Sprintf("in %s", formatRelDuration(-diff))
	}
	return fmt.Sprintf("%s ago", formatRelDuration(diff))
}

func formatRelDuration(d time.Duration) string {
	totalSec := int(d.Seconds())
	if totalSec < 60 {
		return fmt.Sprintf("%ds", totalSec)
	}
	totalMin := int(d.Minutes())
	if totalMin < 60 {
		return fmt.Sprintf("%dm%ds", totalMin, totalSec%60)
	}
	totalHr := int(d.Hours())
	if totalHr < 24 {
		return fmt.Sprintf("%dh%dm", totalHr, totalMin%60)
	}
	days := totalHr / 24
	if days < 365 {
		return fmt.Sprintf("%dd%dh", days, totalHr%24)
	}
	years := days / 365
	remDays := days % 365
	return fmt.Sprintf("%dy%dd", years, remDays)
}

func formatGeneric(v float64) string {
	if v == 0 {
		return "0"
	}
	abs := v
	if abs < 0 {
		abs = -abs
	}
	switch {
	case abs >= 1e6:
		return fmt.Sprintf("%.2fM", v/1e6)
	case abs >= 1e3:
		return fmt.Sprintf("%.2fk", v/1e3)
	case abs >= 1:
		return fmt.Sprintf("%.2f", v)
	case abs >= 0.01:
		return fmt.Sprintf("%.3f", v)
	default:
		return fmt.Sprintf("%.4f", v)
	}
}

func rateAxisFormatter() linechart.ValueFormatter {
	return func(v float64) string {
		if math.IsNaN(v) {
			return ""
		}
		return formatGeneric(v) + "/s"
	}
}

func yAxisFormatter(metricName string) linechart.ValueFormatter {
	return func(v float64) string {
		if math.IsNaN(v) {
			return ""
		}
		return formatValue(metricName, v)
	}
}

func unitSuffix(name string) string {
	m := matchUnit(name)
	if m != nil {
		return m.Suffix
	}
	return ""
}

// --- store ---

type store struct {
	mu          sync.RWMutex
	series      map[string]*metricSeries
	order       []string
	metricNames []string
	nameSet     map[string]bool
}

func newStore() *store {
	return &store{
		series:  make(map[string]*metricSeries),
		nameSet: make(map[string]bool),
	}
}

func seriesKey(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return name + "{" + strings.Join(parts, ",") + "}"
}

func (st *store) update(name string, labels map[string]string, help, mtype string, value float64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	key := seriesKey(name, labels)
	s, ok := st.series[key]
	if !ok {
		s = &metricSeries{
			key:    key,
			name:   name,
			labels: labels,
			help:   help,
			mtype:  mtype,
			values: make([]float64, ringSize),
			times:  make([]time.Time, ringSize),
		}
		st.series[key] = s
		st.order = append(st.order, key)
		sort.Strings(st.order)
		if !st.nameSet[name] {
			st.nameSet[name] = true
			st.metricNames = append(st.metricNames, name)
			sort.Strings(st.metricNames)
		}
	}
	s.push(value)
}

func (st *store) snapshot() []*metricSeries {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]*metricSeries, 0, len(st.order))
	for _, k := range st.order {
		out = append(out, st.series[k])
	}
	return out
}

func (st *store) names() []string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return append([]string{}, st.metricNames...)
}

func (st *store) seriesForName(name string) []*metricSeries {
	st.mu.RLock()
	defer st.mu.RUnlock()
	var out []*metricSeries
	for _, k := range st.order {
		s := st.series[k]
		if s.name == name {
			out = append(out, s)
		}
	}
	return out
}

func (st *store) seriesCount(name string) int {
	st.mu.RLock()
	defer st.mu.RUnlock()
	count := 0
	for _, k := range st.order {
		if st.series[k].name == name {
			count++
		}
	}
	return count
}

func (st *store) get(key string) *metricSeries {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.series[key]
}

func (st *store) firstType(name string) string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	for _, k := range st.order {
		s := st.series[k]
		if s.name == name {
			return s.detectedType()
		}
	}
	return "gauge"
}

// --- scraper ---

func scrape(ctx context.Context, targets []string, st *store) {
	client := &http.Client{Timeout: 2 * time.Second}

	for _, target := range targets {
		scrapeTarget(client, target, st)
	}

	ticker := time.NewTicker(scrapeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, target := range targets {
				go func(t string) { scrapeTarget(client, t, st) }(target)
			}
		}
	}
}

func parseLabels(s string) (string, map[string]string) {
	idx := strings.Index(s, "{")
	if idx < 0 {
		return s, nil
	}
	name := s[:idx]
	rest := s[idx+1:]
	end := strings.Index(rest, "}")
	if end < 0 {
		return name, nil
	}
	labelStr := rest[:end]
	labels := map[string]string{}
	for _, pair := range strings.Split(labelStr, ",") {
		pair = strings.TrimSpace(pair)
		eqIdx := strings.Index(pair, "=")
		if eqIdx < 0 {
			continue
		}
		k := pair[:eqIdx]
		v := strings.Trim(pair[eqIdx+1:], `"`)
		labels[k] = v
	}
	return name, labels
}

func scrapeTarget(client *http.Client, target string, st *store) {
	url := fmt.Sprintf("http://%s/metrics", target)
	resp, err := client.Get(url)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var currentHelp, currentType, currentBaseName string

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "# HELP ") {
			parts := strings.SplitN(line[7:], " ", 2)
			currentBaseName = parts[0]
			if len(parts) > 1 {
				currentHelp = parts[1]
			}
			continue
		}
		if strings.HasPrefix(line, "# TYPE ") {
			parts := strings.SplitN(line[7:], " ", 2)
			currentBaseName = parts[0]
			if len(parts) > 1 {
				currentType = parts[1]
			}
			continue
		}
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		spaceIdx := strings.LastIndex(line, " ")
		if spaceIdx < 0 {
			continue
		}
		metricPart := line[:spaceIdx]
		valStr := line[spaceIdx+1:]
		val, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			continue
		}

		name, labels := parseLabels(metricPart)
		help, mtype := "", ""
		if name == currentBaseName {
			help = currentHelp
			mtype = currentType
		}
		st.update(name, labels, help, mtype, val)
	}
}

// --- TTY guard ---

func waitForTTY() {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return
	}
	fmt.Fprintln(os.Stderr, "madvisor: no TTY detected, waiting for terminal attachment...")
	for {
		time.Sleep(2 * time.Second)
		if term.IsTerminal(int(os.Stdin.Fd())) {
			fmt.Fprintln(os.Stderr, "madvisor: TTY detected, starting dashboard")
			return
		}
	}
}

// --- UI state ---

const defaultPageSize = 30

type focusPanel int

const (
	focusSidebar focusPanel = iota
	focusSeriesTable
)

type uiState struct {
	mu           sync.Mutex
	allKeys      []string
	filtered     []string
	selectedIdx  int
	scrollOffset int
	pageSize     int
	filterText   string
	filterMode   bool
	regexValid   bool

	focus          focusPanel
	seriesIdx      int
	seriesScroll   int
	seriesPageSize int
}

func (u *uiState) setKeys(keys []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(keys) == len(u.allKeys) {
		same := true
		for i := range keys {
			if keys[i] != u.allKeys[i] {
				same = false
				break
			}
		}
		if same {
			return
		}
	}
	u.allKeys = keys
	u.applyFilter()
}

func (u *uiState) applyFilter() {
	if u.filterText == "" {
		u.filtered = append([]string{}, u.allKeys...)
		u.regexValid = true
	} else {
		u.filtered = nil
		re, err := regexp.Compile("(?i)" + u.filterText)
		if err != nil {
			u.regexValid = false
			lower := strings.ToLower(u.filterText)
			for _, k := range u.allKeys {
				if strings.Contains(strings.ToLower(k), lower) {
					u.filtered = append(u.filtered, k)
				}
			}
		} else {
			u.regexValid = true
			for _, k := range u.allKeys {
				if re.MatchString(k) {
					u.filtered = append(u.filtered, k)
				}
			}
		}
	}
	if u.selectedIdx >= len(u.filtered) {
		u.selectedIdx = len(u.filtered) - 1
	}
	if u.selectedIdx < 0 {
		u.selectedIdx = 0
	}
	u.scrollOffset = 0
	u.seriesIdx = 0
	u.seriesScroll = 0
	u.adjustScroll()
}

func (u *uiState) adjustScroll() {
	ps := u.pageSize
	if ps <= 0 {
		ps = defaultPageSize
	}
	if u.selectedIdx < u.scrollOffset {
		u.scrollOffset = u.selectedIdx
	}
	if u.selectedIdx >= u.scrollOffset+ps {
		u.scrollOffset = u.selectedIdx - ps + 1
	}
	if u.scrollOffset < 0 {
		u.scrollOffset = 0
	}
}

func (u *uiState) adjustSeriesScroll() {
	ps := u.seriesPageSize
	if ps <= 0 {
		ps = 10
	}
	if u.seriesIdx < u.seriesScroll {
		u.seriesScroll = u.seriesIdx
	}
	if u.seriesIdx >= u.seriesScroll+ps {
		u.seriesScroll = u.seriesIdx - ps + 1
	}
	if u.seriesScroll < 0 {
		u.seriesScroll = 0
	}
}

func (u *uiState) moveUp() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.focus == focusSidebar {
		if u.selectedIdx > 0 {
			u.selectedIdx--
			u.adjustScroll()
			u.seriesIdx = 0
			u.seriesScroll = 0
		}
	} else {
		if u.seriesIdx > 0 {
			u.seriesIdx--
			u.adjustSeriesScroll()
		}
	}
}

func (u *uiState) moveDown() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.focus == focusSidebar {
		if u.selectedIdx < len(u.filtered)-1 {
			u.selectedIdx++
			u.adjustScroll()
			u.seriesIdx = 0
			u.seriesScroll = 0
		}
	} else {
		u.seriesIdx++
		u.adjustSeriesScroll()
	}
}

func (u *uiState) clampSeriesIdx(max int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.seriesIdx >= max {
		u.seriesIdx = max - 1
	}
	if u.seriesIdx < 0 {
		u.seriesIdx = 0
	}
}

func (u *uiState) selectedKey() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.selectedIdx >= 0 && u.selectedIdx < len(u.filtered) {
		return u.filtered[u.selectedIdx]
	}
	return ""
}

func (u *uiState) toggleFocus() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.focus == focusSidebar {
		u.focus = focusSeriesTable
	} else {
		u.focus = focusSidebar
	}
}

func (u *uiState) addFilterChar(ch rune) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.filterText += string(ch)
	u.applyFilter()
}

func (u *uiState) backspaceFilter() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.filterText) > 0 {
		u.filterText = u.filterText[:len(u.filterText)-1]
		u.applyFilter()
	}
}

func (u *uiState) clearFilter() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.filterText = ""
	u.filterMode = false
	u.applyFilter()
}

func (u *uiState) startFilter() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.filterMode = true
}

func (u *uiState) snapshot() (filtered []string, selIdx int, scrollOff int, filter string, filterMode bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string{}, u.filtered...), u.selectedIdx, u.scrollOffset, u.filterText, u.filterMode
}

func (u *uiState) seriesSnapshot() (seriesIdx int, seriesScroll int, focus focusPanel, regexOK bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.seriesIdx, u.seriesScroll, u.focus, u.regexValid
}

// --- colors ---

func colorForIndex(i int) cell.Color {
	palette := []cell.Color{
		cell.ColorGreen,
		cell.ColorCyan,
		cell.ColorMagenta,
		cell.ColorYellow,
		cell.ColorBlue,
		cell.ColorRed,
		cell.ColorWhite,
	}
	return palette[i%len(palette)]
}

// --- grid builders ---

func buildSplashGrid(logoWidget *text.Text, statusWidget *text.Text) ([]container.Option, error) {
	builder := grid.New()
	builder.Add(grid.RowHeightPerc(80,
		grid.ColWidthPerc(99,
			grid.Widget(logoWidget,
				container.Border(linestyle.Round),
				container.BorderColor(cell.ColorCyan),
			),
		),
	))
	builder.Add(grid.RowHeightPerc(19,
		grid.ColWidthPerc(99,
			grid.Widget(statusWidget,
				container.Border(linestyle.Light),
				container.BorderTitle(" status "),
				container.BorderColor(cell.ColorGreen),
			),
		),
	))
	return builder.Build()
}

// --- render metric name list (sidebar) ---

func renderMetricList(w *text.Text, st *store, filtered []string, selIdx int, scrollOff int, filter string, filterMode bool, regexOK bool, focus focusPanel) {
	w.Reset()

	if filterMode || filter != "" {
		w.Write("Filter", text.WriteCellOpts(cell.FgColor(cell.ColorYellow)))
		if !regexOK {
			w.Write("(err)", text.WriteCellOpts(cell.FgColor(cell.ColorRed)))
		}
		w.Write(": ", text.WriteCellOpts(cell.FgColor(cell.ColorYellow)))
		w.Write(filter, text.WriteCellOpts(cell.FgColor(cell.ColorWhite)))
		w.Write("█\n", text.WriteCellOpts(cell.FgColor(cell.ColorYellow)))
		w.Write("\n")
	}

	if scrollOff > 0 {
		w.Write(fmt.Sprintf("  ↑ %d more\n", scrollOff), text.WriteCellOpts(cell.FgColor(cell.ColorYellow)))
	}

	end := len(filtered)
	if end > scrollOff+defaultPageSize {
		end = scrollOff + defaultPageSize
	}

	for i := scrollOff; i < end; i++ {
		name := filtered[i]
		mtype := st.firstType(name)
		count := st.seriesCount(name)

		prefix := "  "
		fg := cell.ColorWhite
		if i == selIdx {
			if focus == focusSidebar {
				prefix = "▶ "
				fg = cell.ColorCyan
			} else {
				prefix = "› "
				fg = cell.ColorBlue
			}
		}

		badge := metricTypeBadge(mtype)
		countStr := ""
		if count > 1 {
			countStr = fmt.Sprintf(" (%d)", count)
		}

		w.Write(prefix, text.WriteCellOpts(cell.FgColor(fg)))
		w.Write(badge+" ", text.WriteCellOpts(cell.FgColor(cell.ColorMagenta)))
		w.Write(name, text.WriteCellOpts(cell.FgColor(fg)))
		w.Write(countStr+"\n", text.WriteCellOpts(cell.FgColor(cell.ColorGreen)))
	}

	if end < len(filtered) {
		w.Write(fmt.Sprintf("  ↓ %d more\n", len(filtered)-end), text.WriteCellOpts(cell.FgColor(cell.ColorYellow)))
	}

	if len(filtered) == 0 {
		w.Write("\n  no metrics match filter", text.WriteCellOpts(cell.FgColor(cell.ColorRed)))
	}
}

// --- render series table ---

func renderSeriesTable(w *text.Text, st *store, metricName string, seriesIdx int, seriesScroll int, focus focusPanel) {
	w.Reset()

	if metricName == "" {
		w.Write("  select a metric name", text.WriteCellOpts(cell.FgColor(cell.ColorYellow)))
		return
	}

	seriesList := st.seriesForName(metricName)
	if len(seriesList) == 0 {
		w.Write("  no series for "+metricName, text.WriteCellOpts(cell.FgColor(cell.ColorRed)))
		return
	}

	mtype := st.firstType(metricName)
	w.Write(fmt.Sprintf(" %s %s — %d series\n", metricTypeBadge(mtype), metricName, len(seriesList)),
		text.WriteCellOpts(cell.FgColor(cell.ColorCyan)))

	if seriesList[0].help != "" {
		w.Write(" "+seriesList[0].help+"\n", text.WriteCellOpts(cell.FgColor(cell.ColorWhite)))
	}
	w.Write("\n")

	pageSize := 10
	end := len(seriesList)
	if end > seriesScroll+pageSize {
		end = seriesScroll + pageSize
	}

	if seriesScroll > 0 {
		w.Write(fmt.Sprintf("  ↑ %d more\n", seriesScroll), text.WriteCellOpts(cell.FgColor(cell.ColorYellow)))
	}

	for i := seriesScroll; i < end; i++ {
		s := seriesList[i]
		prefix := "  "
		fg := cell.ColorWhite
		if i == seriesIdx && focus == focusSeriesTable {
			prefix = "▶ "
			fg = cell.ColorCyan
		}

		labelStr := ""
		if len(s.labels) > 0 {
			parts := make([]string, 0, len(s.labels))
			keys := make([]string, 0, len(s.labels))
			for k := range s.labels {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				parts = append(parts, fmt.Sprintf(`%s="%s"`, k, s.labels[k]))
			}
			labelStr = "{" + strings.Join(parts, ", ") + "}"
		} else {
			labelStr = "(no labels)"
		}

		raw := s.last()
		rawStr := strconv.FormatFloat(raw, 'f', -1, 64)
		var valStr string
		if s.shouldRate() {
			r := s.rate(rateWindowGet())
			valStr = formatGeneric(r) + "/s"
		} else {
			valStr = formatValue(s.name, raw)
		}

		display := valStr
		if valStr != rawStr {
			display = valStr + " (" + rawStr + ")"
		}

		w.Write(prefix, text.WriteCellOpts(cell.FgColor(fg)))
		w.Write(labelStr, text.WriteCellOpts(cell.FgColor(fg)))
		w.Write(" = "+display+"\n", text.WriteCellOpts(cell.FgColor(cell.ColorGreen)))
	}

	if end < len(seriesList) {
		w.Write(fmt.Sprintf("  ↓ %d more\n", len(seriesList)-end), text.WriteCellOpts(cell.FgColor(cell.ColorYellow)))
	}
}

// --- main run ---

func run(targets []string) error {
	dbg, _ := os.Create("/tmp/madvisor-debug.log")
	if dbg != nil {
		defer dbg.Close()
	}
	dlog := func(format string, args ...interface{}) {
		if dbg != nil {
			fmt.Fprintf(dbg, format+"\n", args...)
		}
	}

	t, err := tcell.New()
	if err != nil {
		return fmt.Errorf("tcell.New: %w", err)
	}
	defer t.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := newStore()
	go scrape(ctx, targets, st)

	ui := &uiState{}

	logoWidget, err := text.New(text.WrapAtRunes())
	if err != nil {
		return err
	}
	for _, line := range strings.Split(logo, "\n") {
		logoWidget.Write(line+"\n", text.WriteCellOpts(cell.FgColor(cell.ColorCyan)))
	}

	statusWidget, err := text.New(text.WrapAtRunes())
	if err != nil {
		return err
	}
	statusWidget.Write(
		fmt.Sprintf("Connecting to %s ...", strings.Join(targets, ", ")),
		text.WriteCellOpts(cell.FgColor(cell.ColorYellow)),
	)

	const rootID = "root"

	splashOpts, err := buildSplashGrid(logoWidget, statusWidget)
	if err != nil {
		return err
	}
	splashOpts = append([]container.Option{container.ID(rootID)}, splashOpts...)
	c, err := container.New(t, splashOpts...)
	if err != nil {
		return fmt.Errorf("container.New: %w", err)
	}

	chart, err := linechart.New(linechart.YAxisAdaptive())
	if err != nil {
		return err
	}

	listWidget, err := text.New(text.WrapAtRunes())
	if err != nil {
		return err
	}

	seriesWidget, err := text.New(text.WrapAtRunes())
	if err != nil {
		return err
	}

	prevSelName := ""
	prevSeriesKey := ""

	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				names := st.names()
				dlog("tick: names=%d", len(names))
				if len(names) == 0 {
					continue
				}

				ui.setKeys(names)

				filtered, selIdx, scrollOff, filter, filterMode := ui.snapshot()
				seriesIdx, seriesScroll, focus, regexOK := ui.seriesSnapshot()
				dlog("ui: filtered=%d selIdx=%d scrollOff=%d filter=%q filterMode=%v focus=%d", len(filtered), selIdx, scrollOff, filter, filterMode, focus)

				renderMetricList(listWidget, st, filtered, selIdx, scrollOff, filter, filterMode, regexOK, focus)

				selName := ""
				if selIdx >= 0 && selIdx < len(filtered) {
					selName = filtered[selIdx]
				}

				seriesList := st.seriesForName(selName)
				ui.clampSeriesIdx(len(seriesList))
				seriesIdx, seriesScroll, focus, _ = ui.seriesSnapshot()

				renderSeriesTable(seriesWidget, st, selName, seriesIdx, seriesScroll, focus)

				var chartSeries []*metricSeries
				if focus == focusSeriesTable && seriesIdx >= 0 && seriesIdx < len(seriesList) {
					chartSeries = []*metricSeries{seriesList[seriesIdx]}
				} else {
					chartSeries = seriesList
				}

				chartKey := ""
				if len(chartSeries) > 0 {
					for _, cs := range chartSeries {
						chartKey += cs.key + ";"
					}
				}

				if chartKey != prevSeriesKey || selName != prevSelName {
					chartOpts := []linechart.Option{linechart.YAxisAdaptive()}
					if len(chartSeries) > 0 {
						first := chartSeries[0]
						if first.shouldRate() {
							chartOpts = append(chartOpts, linechart.YAxisFormattedValues(rateAxisFormatter()))
						} else if isTimestampMetric(first.name) {
							chartOpts = append(chartOpts, linechart.YAxisFormattedValues(func(v float64) string {
								if math.IsNaN(v) {
									return ""
								}
								return formatRelDuration(time.Duration(v * float64(time.Second)))
							}))
						} else {
							chartOpts = append(chartOpts, linechart.YAxisFormattedValues(yAxisFormatter(first.name)))
						}
					}
					newChart, chartErr := linechart.New(chartOpts...)
					if chartErr == nil {
						chart = newChart
					} else {
						dlog("chart create error: %v", chartErr)
					}
					prevSelName = selName
					prevSeriesKey = chartKey
				}

				for i, cs := range chartSeries {
					var data []float64
					if cs.shouldRate() {
						data = cs.rateSlice(rateWindowGet())
					} else if isTimestampMetric(cs.name) {
						nowSec := float64(time.Now().Unix())
						raw := cs.slice()
						data = make([]float64, len(raw))
						for j, v := range raw {
							if v > 0 {
								data[j] = nowSec - v
							}
						}
					} else {
						data = cs.slice()
					}
					if len(data) >= 2 {
						label := cs.displayName()
						if seriesErr := chart.Series(label, data,
							linechart.SeriesCellOpts(cell.FgColor(colorForIndex(i))),
						); seriesErr != nil {
							dlog("chart.Series error: %v", seriesErr)
						}
					}
				}

				chartTitle := " chart "
				if selName != "" {
					mtype := st.firstType(selName)
					if focus == focusSeriesTable && len(chartSeries) == 1 {
						cs := chartSeries[0]
						if cs.shouldRate() {
							chartTitle = fmt.Sprintf(" %s [rate/s] ", cs.displayName())
						} else if isTimestampMetric(cs.name) {
							chartTitle = fmt.Sprintf(" %s [age] ", cs.displayName())
						} else {
							chartTitle = fmt.Sprintf(" %s%s ", cs.displayName(), unitSuffix(cs.name))
						}
					} else {
						chartTitle = fmt.Sprintf(" %s %s (%d series) ", metricTypeBadge(mtype), selName, len(seriesList))
					}
				}

				sidebarBorderColor := cell.ColorGreen
				seriesBorderColor := cell.ColorBlue
				if focus == focusSidebar {
					sidebarBorderColor = cell.ColorCyan
					seriesBorderColor = cell.ColorBlue
				} else {
					sidebarBorderColor = cell.ColorGreen
					seriesBorderColor = cell.ColorCyan
				}

				allSeries := st.snapshot()
				statusWidget.Reset()
				statusWidget.Write(fmt.Sprintf(
					" madVisor %s │ Targets: %s │ Metrics: %d/%d │ Series: %d │ Rate: %s │ Q: quit │ /: filter │ Tab: focus │ ↑↓: nav │ []: rate",
					version,
					strings.Join(targets, ", "),
					len(filtered), len(names),
					len(allSeries),
					rateWindowGet(),
				), text.WriteCellOpts(cell.FgColor(cell.ColorGreen)))

				builder := grid.New()
				builder.Add(grid.RowHeightPerc(95,
					grid.ColWidthPerc(70,
						grid.RowHeightPerc(60,
							grid.Widget(chart,
								container.Border(linestyle.Light),
								container.BorderTitle(chartTitle),
								container.BorderColor(cell.ColorCyan),
							),
						),
						grid.RowHeightPerc(39,
							grid.Widget(seriesWidget,
								container.Border(linestyle.Light),
								container.BorderTitle(" series "),
								container.BorderColor(seriesBorderColor),
							),
						),
					),
					grid.ColWidthPerc(29,
						grid.Widget(listWidget,
							container.Border(linestyle.Light),
							container.BorderTitle(" metric names "),
							container.BorderColor(sidebarBorderColor),
						),
					),
				))
				builder.Add(grid.RowHeightPerc(4,
					grid.ColWidthPerc(99,
						grid.Widget(statusWidget),
					),
				))
				opts, buildErr := builder.Build()
				if buildErr != nil {
					dlog("grid.Build error: %v", buildErr)
				} else {
					if updateErr := c.Update(rootID, opts...); updateErr != nil {
						dlog("container.Update error: %v", updateErr)
					}
				}
			}
		}
	}()

	err = termdash.Run(ctx, t, c,
		termdash.KeyboardSubscriber(func(k *terminalapi.Keyboard) {
			_, _, _, _, filterMode := ui.snapshot()

			if filterMode {
				switch k.Key {
				case keyboard.KeyEsc:
					ui.clearFilter()
				case keyboard.KeyBackspace, keyboard.KeyBackspace2, keyboard.KeyDelete:
					ui.backspaceFilter()
				case keyboard.KeyEnter:
					ui.mu.Lock()
					ui.filterMode = false
					ui.mu.Unlock()
				default:
					if k.Key >= 0x20 && k.Key < 0x7f {
						ui.addFilterChar(rune(k.Key))
					}
				}
				return
			}

			switch k.Key {
			case keyboard.KeyEsc:
				_, _, _, f, _ := ui.snapshot()
				if f != "" {
					ui.clearFilter()
				} else {
					cancel()
				}
			case keyboard.Key('q'), keyboard.Key('Q'):
				cancel()
			case keyboard.KeyArrowUp, keyboard.Key('k'):
				ui.moveUp()
			case keyboard.KeyArrowDown, keyboard.Key('j'):
				ui.moveDown()
			case keyboard.KeyTab:
				ui.toggleFocus()
			case keyboard.Key('/'):
				ui.startFilter()
			case keyboard.Key(']'), keyboard.Key('+'):
				rateWindowUp()
			case keyboard.Key('['), keyboard.Key('-'):
				rateWindowDown()
			}
		}),
		termdash.RedrawInterval(refreshInterval),
	)
	return err
}

var (
	flagTargets    = flag.String("targets", "", "comma-separated host:port list of Prometheus endpoints (env: METRIC_TARGETS)")
	flagRateWindow = flag.String("rate-window", "", "rate calculation window duration, e.g. 10s (env: RATE_WINDOW)")
	flagPatterns   = flag.String("patterns", "", "path to custom metric patterns YAML file (overrides built-in defaults)")
	flagVersion    = flag.Bool("version", false, "print version and exit")
)

func parseTargets(flagVal string) []string {
	val := flagVal
	if val == "" {
		val = os.Getenv("METRIC_TARGETS")
	}
	if val == "" {
		val = "localhost:8080"
	}
	parts := strings.Split(val, ",")
	var targets []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			targets = append(targets, p)
		}
	}
	return targets
}

func parseRateWindow(flagVal string) {
	val := flagVal
	if val == "" {
		val = os.Getenv("RATE_WINDOW")
	}
	if val != "" {
		d, err := time.ParseDuration(val)
		if err == nil && d > 0 {
			rateWindowSet(d)
		} else {
			log.Printf("madvisor: invalid rate-window %q, using default %s", val, defaultRateWindow)
		}
	}
}

func main() {
	flag.Parse()

	if *flagVersion {
		fmt.Printf("madvisor %s (commit=%s branch=%s)\n", version, commit, branch)
		os.Exit(0)
	}

	if err := initPatterns(*flagPatterns); err != nil {
		log.Fatalf("madvisor: %v", err)
	}

	targets := parseTargets(*flagTargets)
	parseRateWindow(*flagRateWindow)
	log.Printf("madvisor %s (commit=%s branch=%s)", version, commit, branch)
	log.Printf("madvisor: targets=%v rateWindow=%s", targets, rateWindowGet())

	waitForTTY()

	if err := run(targets); err != nil {
		log.Fatalf("madvisor: %v", err)
	}
}
