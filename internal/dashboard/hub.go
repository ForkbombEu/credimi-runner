package dashboard

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

// ─────────────────────────────────────────────────────────────────────────────
// SSE hub + background poller.
//
// One poller goroutine builds a Snapshot every interval and fans rendered HTML
// fragments out to every connected client. Clients are plain http flushers
// registered by the /events/* handlers.
// ─────────────────────────────────────────────────────────────────────────────

type event struct {
	name string // SSE event: name (matches htmx sse-swap)
	data string // rendered HTML fragment (newlines are escaped for SSE framing)
}

type client struct {
	ch     chan event
	stream string // which stream this client subscribed to: health|devices|workers|log
}

type Hub struct {
	mu       sync.RWMutex
	clients  map[*client]struct{}
	cfg      *Config
	render   *Renderer
	statusFn func() dashboardruntime.RuntimeStatus

	snapMu  sync.RWMutex
	snap    Snapshot
	workers []Worker
}

type Worker struct {
	ID      string
	Env     string
	Host    string
	Queue   string
	Scope   string // user | admin
	Status  Status
	Enabled bool
}

func NewHub(cfg *Config, r *Renderer, statusFn func() dashboardruntime.RuntimeStatus) *Hub {
	return &Hub{clients: map[*client]struct{}{}, cfg: cfg, render: r, statusFn: statusFn}
}

func (h *Hub) add(c *client) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}
func (h *Hub) remove(c *client) {
	h.mu.Lock()
	delete(h.clients, c)
	close(c.ch)
	h.mu.Unlock()
}

func (h *Hub) broadcast(stream string, ev event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c.stream != stream {
			continue
		}
		select {
		case c.ch <- ev:
		default: // drop if the client is slow; next tick recovers
		}
	}
}

func (h *Hub) CurrentSnapshot() Snapshot {
	h.snapMu.RLock()
	defer h.snapMu.RUnlock()
	return h.snap
}
func (h *Hub) CurrentWorkers() []Worker {
	h.snapMu.RLock()
	defer h.snapMu.RUnlock()
	return h.workers
}

// Run starts the poll loop until ctx is cancelled.
func (h *Hub) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	h.poll(ctx) // immediate first sample
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.poll(ctx)
		}
	}
}

func (h *Hub) poll(ctx context.Context) {
	devices := append(probeAndroid(ctx), probeIOS(ctx)...)
	values := dashboardruntime.Values(h.cfg.Snapshot())
	runtimeRunning := false
	if h.statusFn != nil {
		status := h.statusFn()
		runtimeRunning = status.RunnerRunning
	}
	services := probeServices(values, runtimeRunning)

	snap := Snapshot{Services: services, Devices: devices, Time: time.Now()}
	workers := h.runningWorkers(ctx, services)

	h.snapMu.Lock()
	h.snap = snap
	h.workers = workers
	h.snapMu.Unlock()

	// Render and broadcast each live region.
	h.broadcast("health", event{name: "pill", data: h.render.Fragment("pill", h.pillData(snap))})
	h.broadcast("devices", event{name: "rows", data: h.render.Fragment("device_rows", devices)})
	h.broadcast("devices", event{name: "configured", data: h.render.Fragment("configured_device_rows", PageData{Runner: h.cfg, Snapshot: snap})})
	h.broadcast("workers", event{name: "rows", data: h.render.Fragment("worker_rows", workers)})
}

func (h *Hub) runningWorkers(ctx context.Context, services []Service) []Worker {
	workers, ok := h.fetchRunningWorkers(ctx)
	if ok {
		return workers
	}
	return h.deriveWorkers(services)
}

func (h *Hub) fetchRunningWorkers(ctx context.Context) ([]Worker, bool) {
	url := h.runnerAPIURL() + "/workers"
	reqCtx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false
	}
	if apiKey := h.cfg.Get("CREDIMI_USER_API_KEY"); apiKey != "" {
		req.Header.Set("Credimi-Api-Key", apiKey)
	} else if apiKey := h.cfg.Get("CREDIMI_INTERNAL_ADMIN_KEY"); apiKey != "" {
		req.Header.Set("Credimi-Api-Key", apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var names []string
	if err := json.NewDecoder(resp.Body).Decode(&names); err != nil {
		return nil, false
	}
	workers := make([]Worker, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		workers = append(workers, Worker{
			ID:      "runner-" + canonifyWorkerID(name),
			Env:     "runner",
			Host:    h.runnerAPIURL(),
			Queue:   name,
			Scope:   h.cfg.AuthMode(),
			Status:  Online,
			Enabled: true,
		})
	}
	return workers, true
}

func (h *Hub) runnerAPIURL() string {
	host := strings.TrimSpace(h.cfg.Get("RUNNER_HOST"))
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	port := strings.TrimSpace(h.cfg.Get("RUNNER_PORT"))
	if port == "" {
		port = "8050"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func canonifyWorkerID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if b.Len() > 0 && !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// deriveWorkers keeps the page useful before the runner API is reachable.
func (h *Hub) deriveWorkers(services []Service) []Worker {
	runtimeUp := false
	temporalUp := false
	for _, s := range services {
		switch s.ID {
		case "runner":
			runtimeUp = runtimeUp || s.Status == Online
		case "temporal":
			temporalUp = s.Status == Online
		}
	}
	scope := h.cfg.AuthMode()
	org := h.cfg.Get("CREDIMI_RUNNER_ORGANIZATION")
	if org == "" {
		org = "runner"
	}
	mk := func(env, host, suffix string, configured bool) Worker {
		w := Worker{
			ID: env + "-" + suffix, Env: env, Host: host, Scope: scope,
			Queue: "mobile-runner." + org, Enabled: configured,
		}
		switch {
		case !configured:
			w.Status = Idle
		case runtimeUp && temporalUp:
			w.Status = Online
		default:
			w.Status = Idle
		}
		return w
	}
	return []Worker{
		mk("runner", h.cfg.Get("CREDIMI_URL"), "mr", h.cfg.Get("CREDIMI_USER_API_KEY") != "" || h.cfg.Get("CREDIMI_INTERNAL_ADMIN_KEY") != ""),
	}
}

type PillData struct {
	Issues int
	Label  string
	OK     bool
}

func (h *Hub) pillData(s Snapshot) PillData {
	issues := 0
	for _, sv := range s.Services {
		if !sv.Expected || !sv.Critical {
			continue
		}
		if sv.Status == Offline || sv.Status == Degraded {
			issues++
		}
	}
	configuredIssues := map[string]bool{}
	if h.cfg != nil {
		for _, configured := range (PageData{Runner: h.cfg, Snapshot: s}).ConfiguredDeviceViews() {
			if configured.ADBWarning {
				if serial := strings.TrimSpace(configured.Serial); serial != "" {
					configuredIssues[serial] = true
				}
				issues++
			}
		}
	}
	for _, d := range s.Devices {
		if (d.Status == Offline || d.Status == Degraded) && !configuredIssues[strings.TrimSpace(d.Serial)] {
			issues++
		}
	}
	p := PillData{Issues: issues, OK: issues == 0, Label: "All healthy"}
	if issues > 0 {
		p.Label = pluralIssues(issues)
	}
	return p
}

// ── tiny utils shared across files ──

func mustCompile(expr string) *regexp.Regexp { return regexp.MustCompile(expr) }

func pluralIssues(n int) string {
	if n == 1 {
		return "1 issue"
	}
	return itoa(n) + " issues"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
