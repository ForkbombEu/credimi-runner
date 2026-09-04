package runtimesupervisor

import (
	"context"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/config"
	"github.com/forkbombeu/credimi-runner/internal/edge"
	"github.com/forkbombeu/credimi-runner/pkg/server"
)

func TestLocalOriginURL(t *testing.T) {
	tests := []struct {
		listen string
		want   string
	}{
		{"0.0.0.0:8050", "http://127.0.0.1:8050"},
		{":8050", "http://127.0.0.1:8050"},
		{"[::]:8050", "http://127.0.0.1:8050"},
		{"127.0.0.1:8050", "http://127.0.0.1:8050"},
		{"[::1]:8050", "http://[::1]:8050"},
		{"192.168.1.10:8050", "http://192.168.1.10:8050"},
	}
	for _, tt := range tests {
		got, err := localOriginURL(tt.listen)
		if err != nil || got != tt.want {
			t.Errorf("localOriginURL(%q)=%q, %v; want %q", tt.listen, got, err, tt.want)
		}
	}
	for _, listen := range []string{"", "not-an-address", "127.0.0.1:", "127.0.0.1:tcp", "127.0.0.1:65536"} {
		if _, err := localOriginURL(listen); err == nil {
			t.Fatalf("localOriginURL(%q) unexpectedly succeeded", listen)
		}
	}
}

func TestActivationPassesLocalOriginToEdge(t *testing.T) {
	e := &testEdge{}
	s, err := New(t.TempDir(), func() (config.Config, error) {
		cfg := validConfig()
		cfg.Server.APIListen = "0.0.0.0:8050"
		return cfg, nil
	}, Dependencies{
		NewEdge:    func(config.Config) (edge.Edge, error) { return e, nil },
		NewAPI:     func(config.Config, context.Context, *server.ProcessStore) (API, error) { return &testAPI{}, nil },
		NewWorkers: func(config.Config, *server.ProcessStore) WorkerSet { return &testWorkers{} },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	got := append([]string(nil), e.origins...)
	e.mu.Unlock()
	if len(got) != 1 || got[0] != "http://127.0.0.1:8050" {
		t.Fatalf("edge origins=%v", got)
	}
	if err := s.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestActivationUsesAPILocalOriginInsteadOfDesiredListen(t *testing.T) {
	e := &testEdge{}
	s, err := New(t.TempDir(), func() (config.Config, error) {
		cfg := validConfig()
		cfg.Server.APIListen = "192.0.2.10:8050"
		return cfg, nil
	}, Dependencies{
		NewEdge: func(config.Config) (edge.Edge, error) { return e, nil },
		NewAPI: func(config.Config, context.Context, *server.ProcessStore) (API, error) {
			return &testAPI{origin: "http://127.0.0.1:8050"}, nil
		},
		NewWorkers: func(config.Config, *server.ProcessStore) WorkerSet { return &testWorkers{} },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.origins) != 1 || e.origins[0] != "http://127.0.0.1:8050" {
		t.Fatalf("edge origins=%v", e.origins)
	}
}
