package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
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
	ringSize        = 120
	scrapeInterval  = 1 * time.Second
	refreshInterval = 250 * time.Millisecond
)

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
	idx    int
	full   bool
}

func (s *metricSeries) push(v float64) {
	s.values[s.idx] = v
	s.idx = (s.idx + 1) % ringSize
	if s.idx == 0 {
		s.full = true
	}
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

type uiState struct {
	mu          sync.Mutex
	allKeys     []string
	filtered    []string
	selectedIdx int
	filterText  string
	filterMode  bool
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
}

func (u *uiState) moveUp() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.selectedIdx > 0 {
		u.selectedIdx--
	}
}

func (u *uiState) moveDown() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.selectedIdx < len(u.filtered)-1 {
		u.selectedIdx++
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

func (u *uiState) snapshot() (filtered []string, selIdx int, filter string, filterMode bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string{}, u.filtered...), u.selectedIdx, u.filterText, u.filterMode
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

func renderMetricList(w *text.Text, st *store, filtered []string, selIdx int, filter string, filterMode bool) {
	w.Reset()

	if filterMode || filter != "" {
		w.Write("Filter: ", text.WriteCellOpts(cell.FgColor(cell.ColorYellow)))
		w.Write(filter, text.WriteCellOpts(cell.FgColor(cell.ColorWhite)))
		w.Write("█\n", text.WriteCellOpts(cell.FgColor(cell.ColorYellow)))
		w.Write("\n")
	}

	for i, key := range filtered {
		s := st.get(key)
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
		valStr := fmt.Sprintf(" = %.2f", s.last())

		w.Write(prefix, text.WriteCellOpts(cell.FgColor(fg)))
		w.Write(display, text.WriteCellOpts(cell.FgColor(fg)))
		w.Write(valStr+"\n", text.WriteCellOpts(cell.FgColor(cell.ColorGreen)))
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

				filtered, selIdx, filter, filterMode := ui.snapshot()
				dlog("ui: filtered=%d selIdx=%d filter=%q filterMode=%v", len(filtered), selIdx, filter, filterMode)

				renderMetricList(listWidget, st, filtered, selIdx, filter, filterMode)

				selKey := ""
				if selIdx >= 0 && selIdx < len(filtered) {
					selKey = filtered[selIdx]
				}
				dlog("selKey=%q prevSelKey=%q", selKey, prevSelKey)

				if selKey != prevSelKey {
					newChart, chartErr := linechart.New(linechart.YAxisAdaptive())
					if chartErr == nil {
						chart = newChart
					} else {
						dlog("chart create error: %v", chartErr)
					}
					prevSelKey = selKey
				}

				sel := st.get(selKey)
				if sel != nil {
					data := sel.slice()
					dlog("sel=%s dataLen=%d", sel.displayName(), len(data))
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
					chartTitle = fmt.Sprintf(" %s ", sel.displayName())
				}

				statusWidget.Reset()
				statusWidget.Write(fmt.Sprintf(
					" madVisor %s │ Targets: %s │ Series: %d/%d │ Q/ESC: quit │ /: filter │ ↑↓: navigate",
					version,
					strings.Join(targets, ", "),
					len(filtered), len(allSeries),
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
			_, _, _, filterMode := ui.snapshot()

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
				_, _, f, _ := ui.snapshot()
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

func main() {
	targets := parseTargets()
	log.Printf("madvisor %s (commit=%s branch=%s)", version, commit, branch)
	log.Printf("madvisor: targets=%v", targets)

	waitForTTY()

	if err := run(targets); err != nil {
		log.Fatalf("madvisor: %v", err)
	}
}
