package controller

import (
	"context"
	"testing"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/controller/driver"
	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

type observationDriver struct{ result driver.Result }

func (d observationDriver) Observe(context.Context, driver.Request) driver.Result { return d.result }

func TestObserverReportsManagedComposeRuntime(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	observed := Observer{Now: func() time.Time { return now }, Drivers: []driver.Driver{observationDriver{result: driver.Result{Services: []driver.Service{
		{ID: "runner", Running: true, Owned: true, Critical: true},
		{ID: "tunnel", Running: true, Owned: true, Critical: true},
	}}}}}.Observe(context.Background(), t.TempDir(), dashboardruntime.Values{})
	if observed.State != StateRunning || observed.ObservedAt != now || len(observed.Services) != 2 {
		t.Fatalf("unexpected observation: %#v", observed)
	}
}

func TestObserverReportsStoppedWithoutMutation(t *testing.T) {
	observed := Observer{Drivers: []driver.Driver{observationDriver{result: driver.Result{Services: []driver.Service{{ID: "runner", Owned: false, Critical: true}}}}}}.Observe(context.Background(), t.TempDir(), dashboardruntime.Values{})
	if observed.State != StateStopped {
		t.Fatalf("state = %q, want stopped", observed.State)
	}
}

func TestObserverNeverAdoptsForeignListener(t *testing.T) {
	observed := Observer{Drivers: []driver.Driver{observationDriver{result: driver.Result{Services: []driver.Service{{ID: "runner_host_process", Detail: "foreign listener", Critical: true}}}}}}.Observe(context.Background(), t.TempDir(), dashboardruntime.Values{})
	if observed.State != StateForeign || observed.Services[0].Owned {
		t.Fatalf("foreign listener was adopted: %#v", observed)
	}
}

func TestObservedRuntimeStale(t *testing.T) {
	now := time.Now()
	if !(ObservedRuntime{}).Stale(now, time.Second) {
		t.Fatal("expected zero observation to be stale")
	}
	if !(ObservedRuntime{ObservedAt: now.Add(-2 * time.Second)}).Stale(now, time.Second) {
		t.Fatal("expected stale observation")
	}
	if (ObservedRuntime{ObservedAt: now}).Stale(now, time.Second) {
		t.Fatal("expected fresh observation")
	}
}
