package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHubClientLifecycleAndBroadcast(t *testing.T) {
	h := NewHub(&Config{values: map[string]string{}}, nil, nil)
	health := &client{ch: make(chan event, 1), stream: "health"}
	workers := &client{ch: make(chan event, 1), stream: "workers"}

	h.add(health)
	h.add(workers)
	h.broadcast("health", event{name: "pill", data: "ok"})

	select {
	case ev := <-health.ch:
		if ev.name != "pill" || ev.data != "ok" {
			t.Fatalf("event = %#v", ev)
		}
	default:
		t.Fatal("expected health client event")
	}
	select {
	case ev := <-workers.ch:
		t.Fatalf("workers client should not receive health event: %#v", ev)
	default:
	}

	h.remove(health)
	if _, ok := <-health.ch; ok {
		t.Fatal("expected removed client channel to close")
	}
}

func TestHubSnapshotAccessors(t *testing.T) {
	h := NewHub(&Config{values: map[string]string{}}, nil, nil)
	h.snap = Snapshot{Services: []Service{{ID: "runner", Status: Online}}}
	h.workers = []Worker{{ID: "production-mr", Status: Online}}

	if got := h.CurrentSnapshot().Services[0].ID; got != "runner" {
		t.Fatalf("CurrentSnapshot service = %q", got)
	}
	if got := h.CurrentWorkers()[0].ID; got != "production-mr" {
		t.Fatalf("CurrentWorkers worker = %q", got)
	}
}

func TestHubDeriveWorkers(t *testing.T) {
	cfg := &Config{values: map[string]string{
		"CREDIMI_INTERNAL_ADMIN_KEY":  "adm",
		"CREDIMI_RUNNER_ORGANIZATION": "acme",
		"CREDIMI_URL":                 "https://credimi.example",
	}}
	h := NewHub(cfg, nil, nil)

	workers := h.deriveWorkers([]Service{{ID: "runner", Status: Online}, {ID: "temporal", Status: Online}})
	if len(workers) != 1 {
		t.Fatalf("workers len = %d", len(workers))
	}
	if workers[0].Scope != "admin" || workers[0].Queue != "mobile-runner.acme" || workers[0].Status != Online {
		t.Fatalf("runner worker = %#v", workers[0])
	}

	workers = h.deriveWorkers([]Service{{ID: "temporal", Status: Offline}})
	if workers[0].Status != Idle {
		t.Fatalf("offline temporal should leave configured worker idle: %#v", workers[0])
	}
}

func TestHubFetchRunningWorkers(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Credimi-Api-Key"); got != "key" {
			t.Fatalf("Credimi-Api-Key = %q", got)
		}
		w.Write([]byte(`["acme","other-org"]`))
	}))
	defer api.Close()
	host := strings.TrimPrefix(api.URL, "http://")
	cfg := &Config{values: map[string]string{
		"RUNNER_HOST":          strings.Split(host, ":")[0],
		"RUNNER_PORT":          strings.Split(host, ":")[1],
		"CREDIMI_USER_API_KEY": "key",
	}}
	h := NewHub(cfg, nil, nil)
	workers, ok := h.fetchRunningWorkers(context.Background())
	if !ok || len(workers) != 2 {
		t.Fatalf("workers=%#v ok=%v", workers, ok)
	}
	if workers[0].Queue != "acme" || workers[0].Status != Online {
		t.Fatalf("worker = %#v", workers[0])
	}
}

func TestHubRunStopsOnContextCancel(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	h := NewHub(&Config{values: map[string]string{"TEMPORAL_ADDRESS": ""}}, r, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.Run(ctx, time.Hour)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("hub did not stop after context cancellation")
	}
}

func TestPluralIssuesAndItoa(t *testing.T) {
	tests := map[int]string{
		0:  "0 issues",
		1:  "1 issue",
		12: "12 issues",
		-3: "-3 issues",
	}
	for n, want := range tests {
		if got := pluralIssues(n); got != want {
			t.Fatalf("pluralIssues(%d) = %q, want %q", n, got, want)
		}
	}
}
