package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
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
	ringSize         = 120
	scrapeInterval   = 1 * time.Second
	refreshInterval  = 250 * time.Millisecond
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

func (s *metricSeries) isCounter() bool {
	return s.mtype == "counter" || strings.HasSuffix(s.name, "_total")
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
	switch {
	case strings.HasSuffix(name, "_bytes"):
		return formatBytes(v)
	case strings.HasSuffix(name, "_megabytes"):
		return formatBytes(v * 1024 * 1024)
	case strings.HasSuffix(name, "_kilobytes"):
		return formatBytes(v * 1024)
	case strings.HasSuffix(name, "_seconds"):
		return formatDuration(v)
	case strings.HasSuffix(name, "_milliseconds") || strings.HasSuffix(name, "_ms"):
		return formatDuration(v / 1000)
	case strings.HasSuffix(name, "_percent"):
		return fmt.Sprintf("%.1f%%", v)
	case strings.HasSuffix(name, "_total"):
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
	switch {
	case strings.HasSuffix(name, "_bytes"),
		strings.HasSuffix(name, "_megabytes"),
		strings.HasSuffix(name, "_kilobytes"):
		return " [bytes]"
	case strings.HasSuffix(name, "_seconds"):
		return " [duration]"
	case strings.HasSuffix(name, "_milliseconds") || strings.HasSuffix(name, "_ms"):
		return " [duration]"
	case strings.HasSuffix(name, "_percent"):
		return " [%]"
	case strings.HasSuffix(name, "_total"):
		return " [count]"
	default:
		return ""
	}
}

// --- store ---

type store struct {
	mu     sync.RWMutex
	series map[string]*metricSeries
	order  []string
}

func newStore() *store {
	return &store{series: make(map[string]*metricSeries)}
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

func (st *store) get(key string) *metricSeries {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.series[key]
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

type uiState struct {
	mu           sync.Mutex
	allKeys      []string
	filtered     []string
	selectedIdx  int
	scrollOffset int
	pageSize     int
	filterText   string
	filterMode   bool
}

func (u *uiState) setKeys(keys []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.allKeys = keys
	u.applyFilter()
}

func (u *uiState) applyFilter() {
	if u.filterText == "" {
		u.filtered = append([]string{}, u.allKeys...)
	} else {
		u.filtered = nil
		lower := strings.ToLower(u.filterText)
		for _, k := range u.allKeys {
			if strings.Contains(strings.ToLower(k), lower) {
				u.filtered = append(u.filtered, k)
			}
		}
	}
	if u.selectedIdx >= len(u.filtered) {
		u.selectedIdx = len(u.filtered) - 1
	}
	if u.selectedIdx < 0 {
		u.selectedIdx = 0
	}
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

func (u *uiState) moveUp() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.selectedIdx > 0 {
		u.selectedIdx--
		u.adjustScroll()
	}
}

func (u *uiState) moveDown() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.selectedIdx < len(u.filtered)-1 {
		u.selectedIdx++
		u.adjustScroll()
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

// --- render metric list ---

func renderMetricList(w *text.Text, st *store, filtered []string, selIdx int, scrollOff int, filter string, filterMode bool) {
	w.Reset()

	if filterMode || filter != "" {
		w.Write("Filter: ", text.WriteCellOpts(cell.FgColor(cell.ColorYellow)))
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
		s := st.get(filtered[i])
		if s == nil {
			continue
		}
		prefix := "  "
		fg := cell.ColorWhite
		if i == selIdx {
			prefix = "▶ "
			fg = cell.ColorCyan
		}

		display := s.displayName()
		var valStr string
		if s.isCounter() {
			r := s.rate(rateWindowGet())
			valStr = " = " + formatGeneric(r) + "/s"
		} else {
			valStr = " = " + formatValue(s.name, s.last())
		}

		w.Write(prefix, text.WriteCellOpts(cell.FgColor(fg)))
		w.Write(display, text.WriteCellOpts(cell.FgColor(fg)))
		w.Write(valStr+"\n", text.WriteCellOpts(cell.FgColor(cell.ColorGreen)))
	}

	if end < len(filtered) {
		w.Write(fmt.Sprintf("  ↓ %d more\n", len(filtered)-end), text.WriteCellOpts(cell.FgColor(cell.ColorYellow)))
	}

	if len(filtered) == 0 {
		w.Write("\n  no metrics match filter", text.WriteCellOpts(cell.FgColor(cell.ColorRed)))
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

	prevSelKey := ""

	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				allSeries := st.snapshot()
				dlog("tick: allSeries=%d", len(allSeries))
				if len(allSeries) == 0 {
					continue
				}

				keys := make([]string, len(allSeries))
				for i, s := range allSeries {
					keys[i] = s.key
				}
				ui.setKeys(keys)

				filtered, selIdx, scrollOff, filter, filterMode := ui.snapshot()
				dlog("ui: filtered=%d selIdx=%d scrollOff=%d filter=%q filterMode=%v", len(filtered), selIdx, scrollOff, filter, filterMode)

				renderMetricList(listWidget, st, filtered, selIdx, scrollOff, filter, filterMode)

				selKey := ""
				if selIdx >= 0 && selIdx < len(filtered) {
					selKey = filtered[selIdx]
				}
				dlog("selKey=%q prevSelKey=%q", selKey, prevSelKey)

				if selKey != prevSelKey {
					selSeries := st.get(selKey)
					chartOpts := []linechart.Option{linechart.YAxisAdaptive()}
					if selSeries != nil {
						if selSeries.isCounter() {
							chartOpts = append(chartOpts, linechart.YAxisFormattedValues(rateAxisFormatter()))
						} else {
							chartOpts = append(chartOpts, linechart.YAxisFormattedValues(yAxisFormatter(selSeries.name)))
						}
					}
					newChart, chartErr := linechart.New(chartOpts...)
					if chartErr == nil {
						chart = newChart
					} else {
						dlog("chart create error: %v", chartErr)
					}
					prevSelKey = selKey
				}

				sel := st.get(selKey)
				if sel != nil {
					var data []float64
					if sel.isCounter() {
						data = sel.rateSlice(rateWindowGet())
					} else {
						data = sel.slice()
					}
					dlog("sel=%s dataLen=%d counter=%v", sel.displayName(), len(data), sel.isCounter())
					if len(data) >= 2 {
						if seriesErr := chart.Series(sel.displayName(), data,
							linechart.SeriesCellOpts(cell.FgColor(cell.ColorCyan)),
						); seriesErr != nil {
							dlog("chart.Series error: %v", seriesErr)
						}
					}
				}

				chartTitle := " chart "
				if sel != nil {
					if sel.isCounter() {
						chartTitle = fmt.Sprintf(" %s [rate/s] ", sel.displayName())
					} else {
						chartTitle = fmt.Sprintf(" %s%s ", sel.displayName(), unitSuffix(sel.name))
					}
				}

				statusWidget.Reset()
				statusWidget.Write(fmt.Sprintf(
					" madVisor %s │ Targets: %s │ Series: %d/%d │ Rate: %s │ Q/ESC: quit │ /: filter │ ↑↓: nav │ []: rate",
					version,
					strings.Join(targets, ", "),
					len(filtered), len(allSeries),
					rateWindowGet(),
				), text.WriteCellOpts(cell.FgColor(cell.ColorGreen)))

				builder := grid.New()
				builder.Add(grid.RowHeightPerc(95,
					grid.ColWidthPerc(70,
						grid.Widget(chart,
							container.Border(linestyle.Light),
							container.BorderTitle(chartTitle),
							container.BorderColor(cell.ColorCyan),
						),
					),
					grid.ColWidthPerc(29,
						grid.Widget(listWidget,
							container.Border(linestyle.Light),
							container.BorderTitle(" metrics "),
							container.BorderColor(cell.ColorGreen),
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

func parseTargets() []string {
	env := os.Getenv("METRIC_TARGETS")
	if env == "" {
		env = "localhost:8080"
	}
	parts := strings.Split(env, ",")
	var targets []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			targets = append(targets, p)
		}
	}
	return targets
}

func parseRateWindow() {
	if env := os.Getenv("RATE_WINDOW"); env != "" {
		d, err := time.ParseDuration(env)
		if err == nil && d > 0 {
			rateWindowSet(d)
		} else {
			log.Printf("madvisor: invalid RATE_WINDOW %q, using default %s", env, defaultRateWindow)
		}
	}
}

func main() {
	targets := parseTargets()
	parseRateWindow()
	log.Printf("madvisor %s (commit=%s branch=%s)", version, commit, branch)
	log.Printf("madvisor: targets=%v rateWindow=%s", targets, rateWindowGet())

	waitForTTY()

	if err := run(targets); err != nil {
		log.Fatalf("madvisor: %v", err)
	}
}
