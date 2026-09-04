package runtimesupervisor

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/config"
)

type httpAPITestAddr string

func (a httpAPITestAddr) Network() string { return "tcp" }
func (a httpAPITestAddr) String() string  { return string(a) }

type staticListener struct{ address string }

func (l staticListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (l staticListener) Close() error              { return nil }
func (l staticListener) Addr() net.Addr            { return httpAPITestAddr(l.address) }

type blockingListener struct{ done chan struct{} }

func (l *blockingListener) Accept() (net.Conn, error) { <-l.done; return nil, net.ErrClosed }
func (l *blockingListener) Close() error {
	select {
	case <-l.done:
		return net.ErrClosed
	default:
		close(l.done)
		return nil
	}
}
func (*blockingListener) Addr() net.Addr { return httpAPITestAddr("127.0.0.1:8050") }

type failingListener struct{ err error }

func (l failingListener) Accept() (net.Conn, error) { return nil, l.err }
func (failingListener) Close() error                { return nil }
func (failingListener) Addr() net.Addr              { return httpAPITestAddr("127.0.0.1:8050") }

func TestHTTPAPINormalRealTCP(t *testing.T) {
	cfg := validConfig()
	cfg.Server.APIListen = "127.0.0.1:0"
	cfg.Server.ReadHeaderTimeout = config.Duration(17 * time.Second)
	cfg.Server.ShutdownTimeout = config.Duration(time.Second)
	a, err := NewHTTPAPI(cfg, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	if err != nil {
		t.Fatal(err)
	}
	if a.Server.ReadHeaderTimeout != 17*time.Second || a.shutdownTimeout != time.Second {
		t.Fatalf("timeouts=%s/%s", a.Server.ReadHeaderTimeout, a.shutdownTimeout)
	}
	if a.Listening() {
		t.Fatal("new API reports listening")
	}
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	if !a.Listening() {
		t.Fatal("started API does not report listening")
	}
	if err := a.Start(); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	response, err := http.Get("http://" + a.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.Listening() {
		t.Fatal("API still listening after shutdown")
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(); err == nil {
		t.Fatal("Start after shutdown unexpectedly succeeded")
	}
	select {
	case failure := <-a.Failures():
		t.Fatalf("normal shutdown reported failure: %v", failure)
	default:
	}
}

func TestHTTPAPILocalOriginUsesActualListenerAddress(t *testing.T) {
	cfg := validConfig()
	cfg.Server.APIListen = "127.0.0.1:0"
	a, err := NewHTTPAPI(cfg, http.NewServeMux())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Listener.Close()
	origin, err := a.LocalOrigin()
	if err != nil {
		t.Fatal(err)
	}
	if origin != "http://127.0.0.1:"+strings.TrimPrefix(a.Listener.Addr().String(), "127.0.0.1:") {
		t.Fatalf("origin=%q listener=%q", origin, a.Listener.Addr())
	}
}

func TestHTTPAPILocalOriginNormalizesWildcardAndIPv6Addresses(t *testing.T) {
	for _, tc := range []struct {
		name, address, want string
	}{
		{"ipv4 wildcard", "0.0.0.0:8050", "http://127.0.0.1:8050"},
		{"ipv6 wildcard", "[::]:8050", "http://127.0.0.1:8050"},
		{"ipv6 loopback", "[::1]:8050", "http://[::1]:8050"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newHTTPAPI(validConfig(), http.NewServeMux(), staticListener{address: tc.address})
			got, err := a.LocalOrigin()
			if err != nil || got != tc.want {
				t.Fatalf("LocalOrigin()=%q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestHTTPAPIShutdownRetriesActiveHandler(t *testing.T) {
	cfg := validConfig()
	cfg.Server.APIListen = "127.0.0.1:0"
	cfg.Server.ShutdownTimeout = config.Duration(20 * time.Millisecond)
	entered := make(chan struct{})
	release := make(chan struct{})
	handlerDone := make(chan struct{})
	a, err := NewHTTPAPI(cfg, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusOK)
		close(handlerDone)
	}))
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseHandler()
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + a.Listener.Addr().String())
		if response != nil {
			_ = response.Body.Close()
		}
		requestDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request handler did not start")
	}
	if err := a.Shutdown(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first shutdown error=%v", err)
	}
	if a.Listening() {
		t.Fatal("API still listening after shutdown closed its listener")
	}
	if err := a.Start(); err == nil {
		t.Fatal("Start after timed-out shutdown unexpectedly succeeded")
	}
	select {
	case <-handlerDone:
		t.Fatal("handler finished before release")
	default:
	}
	if err := a.Shutdown(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second shutdown error=%v", err)
	}
	releaseHandler()
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("handler did not finish")
	}
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPAPIShutdownHonorsEarlierParentDeadline(t *testing.T) {
	cfg := validConfig()
	cfg.Server.APIListen = "127.0.0.1:0"
	cfg.Server.ShutdownTimeout = config.Duration(time.Second)
	entered := make(chan struct{})
	release := make(chan struct{})
	handlerDone := make(chan struct{})
	a, err := NewHTTPAPI(cfg, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusOK)
		close(handlerDone)
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + a.Listener.Addr().String())
		if response != nil {
			_ = response.Body.Close()
		}
		requestDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request handler did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := a.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error=%v", err)
	}
	close(release)
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("handler did not finish")
	}
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPAPIUnexpectedServeFailureCanBeCleanedUp(t *testing.T) {
	cause := errors.New("accept failed")
	a := newHTTPAPI(validConfig(), http.NewServeMux(), failingListener{err: cause})
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case failure := <-a.Failures():
		if !errors.Is(failure, cause) {
			t.Fatalf("failure does not wrap accept error: %v", failure)
		}
	case <-time.After(time.Second):
		t.Fatal("serve failure was not reported")
	}
	if a.Listening() {
		t.Fatal("failed API still reports listening")
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned historical serve error: %v", err)
	}
	if err := a.Start(); err == nil {
		t.Fatal("Start after Serve failure unexpectedly succeeded")
	}
	select {
	case failure := <-a.Failures():
		t.Fatalf("duplicate failure: %v", failure)
	default:
	}
}

func TestHTTPAPINeverStartedShutdown(t *testing.T) {
	listener := &blockingListener{done: make(chan struct{})}
	a := newHTTPAPI(validConfig(), http.NewServeMux(), listener)
	if a.Listening() {
		t.Fatal("never-started API reports listening")
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !channelClosed(listener.done) {
		t.Fatal("never-started shutdown did not close listener")
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(); err == nil {
		t.Fatal("Start after never-started shutdown unexpectedly succeeded")
	}
}

func TestHTTPAPIBindFailureUsesOccupiedAddress(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = occupied.Close() })
	cfg := validConfig()
	cfg.Server.APIListen = occupied.Addr().String()
	if _, err := NewHTTPAPI(cfg, http.NewServeMux()); err == nil {
		t.Fatal("expected bind failure")
	}
}

func TestHTTPAPINilShutdownIsSafe(t *testing.T) {
	var api *HTTPAPI
	if err := api.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.Listening() {
		t.Fatal("nil API reports listening")
	}
}
