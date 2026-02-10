package main

import (
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	version = "dev"
	commit  = "unknown"
	branch  = "unknown"
)

type series struct {
	name   string
	labels map[string]string
	value  float64
	counter bool
}

type metrics struct {
	mu     sync.RWMutex
	series []series
}

var m = &metrics{}

func labelsStr(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, k, v))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func gauge(t float64, base, amp, period, noise float64) float64 {
	return math.Max(0, base+amp*math.Sin(t/period)+rand.Float64()*noise)
}

func (m *metrics) tick() {
	m.mu.Lock()
	defer m.mu.Unlock()

	t := float64(time.Now().UnixMilli()) / 1000.0

	methods := []string{"GET", "POST", "PUT", "DELETE"}
	paths := []string{"/api/users", "/api/orders", "/api/products", "/healthz"}
	envs := []string{"prod", "staging"}

	var ss []series

	for _, method := range methods {
		for _, path := range paths {
			base := 10.0
			if method == "GET" {
				base = 50
			}
			if path == "/healthz" {
				base = 2
			}
			for i := range m.series {
				s := &m.series[i]
				if s.name == "http_requests_total" && s.labels["method"] == method && s.labels["path"] == path {
					s.value += math.Max(0, base*rand.Float64())
				}
			}
			ss = append(ss, series{
				name:    "http_requests_total",
				labels:  map[string]string{"method": method, "path": path},
				counter: true,
			})
		}
	}

	for _, method := range methods {
		for _, path := range paths {
			ss = append(ss, series{
				name:   "http_request_duration_ms",
				labels: map[string]string{"method": method, "path": path},
				value:  gauge(t, 40, 25, 12, 10),
			})
		}
	}

	for _, env := range envs {
		ss = append(ss, series{
			name:   "cpu_usage_percent",
			labels: map[string]string{"env": env},
			value:  gauge(t, 30, 20, 10, 5),
		})
		ss = append(ss, series{
			name:   "memory_usage_megabytes",
			labels: map[string]string{"env": env},
			value:  gauge(t, 256, 64, 30, 10),
		})
		ss = append(ss, series{
			name:   "active_connections",
			labels: map[string]string{"env": env},
			value:  gauge(t, 20, 15, 6, 5),
		})
		ss = append(ss, series{
			name:   "error_rate",
			labels: map[string]string{"env": env},
			value:  gauge(t, 0.5, 0.5, 15, 0.2),
		})
		ss = append(ss, series{
			name:   "queue_depth",
			labels: map[string]string{"env": env},
			value:  gauge(t, 5, 10, 8, 3),
		})
	}

	if len(m.series) == 0 {
		for i := range ss {
			if ss[i].counter {
				ss[i].value = math.Max(0, 10*rand.Float64())
			}
		}
		m.series = ss
	} else {
		for i := range m.series {
			for j := range ss {
				if !ss[j].counter && m.series[i].name == ss[j].name && labelsMatch(m.series[i].labels, ss[j].labels) {
					m.series[i].value = ss[j].value
				}
			}
		}
	}
}

func labelsMatch(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func (m *metrics) render() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var b strings.Builder

	grouped := map[string][]series{}
	order := []string{}
	for _, s := range m.series {
		if _, ok := grouped[s.name]; !ok {
			order = append(order, s.name)
		}
		grouped[s.name] = append(grouped[s.name], s)
	}

	for _, name := range order {
		ss := grouped[name]
		mtype := "gauge"
		if ss[0].counter {
			mtype = "counter"
		}
		fmt.Fprintf(&b, "# HELP %s Synthetic %s metric.\n", name, mtype)
		fmt.Fprintf(&b, "# TYPE %s %s\n", name, mtype)
		for _, s := range ss {
			fmt.Fprintf(&b, "%s%s %.4f\n", s.name, labelsStr(s.labels), s.value)
		}
	}

	return b.String()
}

func main() {
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			m.tick()
		}
	}()

	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprint(w, m.render())
	})

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	addr := ":8080"
	log.Printf("madvisor-dummy %s (commit=%s branch=%s)", version, commit, branch)
	log.Printf("madvisor-dummy serving metrics on %s/metrics", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
