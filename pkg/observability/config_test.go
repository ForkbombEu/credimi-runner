package observability

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/buildinfo"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

func TestConfigFromEnvDefaults(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_ENABLED", "")
	t.Setenv("OTEL_SERVICE_NAME", "")
	t.Setenv("OTEL_DEPLOYMENT_ENVIRONMENT", "")
	t.Setenv("CREDIMI_ENV", "")
	t.Setenv("GO_ENV", "")
	t.Setenv("CREDIMI_RUNNER_ID", "")
	t.Setenv("OTEL_SERVICE_INSTANCE_ID", "")

	cfg := ConfigFromEnv()
	if cfg.Enabled {
		t.Fatalf("expected disabled by default")
	}
	if cfg.Endpoint != defaultHTTPEndpoint {
		t.Fatalf("expected default endpoint %q, got %q", defaultHTTPEndpoint, cfg.Endpoint)
	}
	if cfg.ServiceName != defaultServiceName {
		t.Fatalf("expected default service name %q, got %q", defaultServiceName, cfg.ServiceName)
	}
	if cfg.ServiceVersion != buildinfo.String() {
		t.Fatalf("expected default service version %q, got %q", buildinfo.String(), cfg.ServiceVersion)
	}
	if cfg.InstanceID == "" {
		t.Fatalf("expected non-empty instance id")
	}
}

func TestConfigFromEnvExplicit(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://collector:4318")
	t.Setenv("OTEL_ENABLED", "true")
	t.Setenv("OTEL_SERVICE_NAME", "runner-test")
	t.Setenv("OTEL_SERVICE_VERSION", "1.2.3")
	t.Setenv("OTEL_DEPLOYMENT_ENVIRONMENT", "")
	t.Setenv("CREDIMI_ENV", "staging")
	t.Setenv("GO_ENV", "ignored")
	t.Setenv("CREDIMI_RUNNER_ID", " /runner-42")
	t.Setenv("OTEL_SERVICE_INSTANCE_ID", "instance-1")

	cfg := ConfigFromEnv()
	if !cfg.Enabled {
		t.Fatalf("expected enabled config")
	}
	if cfg.Environment != "staging" {
		t.Fatalf("expected environment from CREDIMI_ENV, got %q", cfg.Environment)
	}
	if cfg.ServiceName != "runner-test" || cfg.ServiceVersion != "1.2.3" {
		t.Fatalf("unexpected service metadata: %+v", cfg)
	}
	if cfg.RunnerID != "runner-42" || cfg.InstanceID != "instance-1" {
		t.Fatalf("unexpected runner/instance metadata: %+v", cfg)
	}
}

func TestParseResourceAttributes(t *testing.T) {
	attrs := parseResourceAttributes("a=1, b = 2,invalid,=skip,c=3")
	if len(attrs) != 3 {
		t.Fatalf("expected 3 attributes, got %d: %#v", len(attrs), attrs)
	}
	if attrs["a"] != "1" || attrs["b"] != "2" || attrs["c"] != "3" {
		t.Fatalf("unexpected parsed attributes: %#v", attrs)
	}
	if parseResourceAttributes("") != nil {
		t.Fatalf("expected nil for empty attributes")
	}
}

func TestBuildResourceIncludesConfiguredAttributes(t *testing.T) {
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "region=eu,team=platform")
	res, err := buildResource(context.Background(), Config{
		ServiceName:    "runner-test",
		ServiceVersion: "1.0.0",
		Environment:    "dev",
		RunnerID:       "/runner-id",
		InstanceID:     "instance-id",
	})
	if err != nil {
		t.Fatalf("buildResource returned error: %v", err)
	}

	assertResourceAttr(t, res, semconv.ServiceNameKey, "runner-test")
	assertResourceAttr(t, res, semconv.ServiceVersionKey, "1.0.0")
	assertResourceAttr(t, res, attribute.Key("deployment.environment"), "dev")
	assertResourceAttr(t, res, attribute.Key("runner_id"), "runner-id")
	assertResourceAttr(t, res, attribute.Key("service.instance.id"), "instance-id")
	assertResourceAttr(t, res, attribute.Key("region"), "eu")
	assertResourceAttr(t, res, attribute.Key("team"), "platform")
}

func TestWrapHandler(t *testing.T) {
	if WrapHandler(nil, "x") != nil {
		t.Fatalf("expected nil handler when input is nil")
	}

	var hit bool
	handler := WrapHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusNoContent)
	}), "")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !hit {
		t.Fatalf("wrapped handler was not called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("unexpected status %d", rec.Code)
	}
}

func TestNewHTTPClient(t *testing.T) {
	base := &http.Client{}
	client := NewHTTPClient(base)
	if client == base {
		t.Fatalf("expected cloned client")
	}
	if client.Transport == nil {
		t.Fatalf("expected wrapped transport")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("client GET failed: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response failed: %v", err)
	}
	if strings.TrimSpace(string(body)) != "ok" {
		t.Fatalf("unexpected response body %q", string(body))
	}

	defaultClient := NewHTTPClient(nil)
	if defaultClient == nil || defaultClient.Transport == nil {
		t.Fatalf("expected default client to be constructed")
	}
}

func TestTracerAndTemporalInterceptor(t *testing.T) {
	_, span := Tracer("").Start(context.Background(), "span")
	if span == nil {
		t.Fatalf("expected tracer span creation to work")
	}
	span.End()

	interceptor, err := NewTemporalInterceptor()
	if err != nil {
		t.Fatalf("NewTemporalInterceptor returned error: %v", err)
	}
	if interceptor == nil {
		t.Fatalf("expected non-nil Temporal interceptor")
	}
}

func TestStaticSpanAttributeProcessorAddsRunnerID(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(staticSpanAttributeProcessor{
			attrs: buildSpanAttributes(Config{RunnerID: " /runner-1"}),
		}),
		sdktrace.WithSpanProcessor(recorder),
	)

	_, span := tp.Tracer("test").Start(context.Background(), "span")
	span.End()

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected exactly one ended span, got %d", len(spans))
	}

	got, ok := findSpanAttribute(spans[0].Attributes(), attribute.Key("runner_id"))
	if !ok {
		t.Fatalf("expected runner_id span attribute to be present")
	}
	if got != "runner-1" {
		t.Fatalf("expected runner_id span attribute to be runner-1, got %q", got)
	}
}

func TestSetupDisabledAndEnabled(t *testing.T) {
	shutdown, err := Setup(context.Background(), Config{})
	if err != nil {
		t.Fatalf("Setup disabled returned error: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("disabled shutdown returned error: %v", err)
	}

	shutdown, err = Setup(context.Background(), Config{
		Enabled:        true,
		ServiceName:    "runner-test",
		ServiceVersion: "1.0.0",
		Environment:    "test",
		RunnerID:       "runner-1",
		InstanceID:     "instance-1",
		Endpoint:       "http://127.0.0.1:4318",
	})
	if err != nil {
		t.Fatalf("Setup enabled returned error: %v", err)
	}
	if err := shutdown(context.Background()); err != nil && !strings.Contains(err.Error(), "connection") {
		t.Fatalf("unexpected shutdown error: %v", err)
	}

	shutdown, err = Setup(context.Background(), Config{
		Enabled:     true,
		ServiceName: "runner-test",
		InstanceID:  "instance-2",
	})
	if err != nil {
		t.Fatalf("Setup enabled with default endpoint returned error: %v", err)
	}
	if err := shutdown(context.Background()); err != nil && !strings.Contains(err.Error(), "connection") {
		t.Fatalf("unexpected shutdown error for default endpoint setup: %v", err)
	}
}

func TestSetupInvalidEndpointAndDefaultInstanceID(t *testing.T) {
	shutdown, err := Setup(context.Background(), Config{
		Enabled:     true,
		ServiceName: "runner-test",
		InstanceID:  "instance-3",
		Endpoint:    "://bad-endpoint",
	})
	if err != nil {
		t.Fatalf("unexpected Setup error for raw endpoint value: %v", err)
	}
	if err := shutdown(context.Background()); err != nil &&
		!strings.Contains(err.Error(), "connection") &&
		!strings.Contains(err.Error(), "HTTPS client") {
		t.Fatalf("unexpected shutdown error for raw endpoint value: %v", err)
	}

	instanceID := defaultInstanceID()
	if !strings.Contains(instanceID, "-") {
		t.Fatalf("expected instance id to contain separators, got %q", instanceID)
	}
	if strings.TrimSpace(instanceID) == "" {
		t.Fatalf("expected non-empty default instance id")
	}
}

func assertResourceAttr(t *testing.T, res interface{ Set() *attribute.Set }, key attribute.Key, want string) {
	t.Helper()
	value, ok := res.Set().Value(key)
	if !ok {
		t.Fatalf("missing resource attribute %q", key)
	}
	if value.AsString() != want {
		t.Fatalf("resource attribute %q = %q, want %q", key, value.AsString(), want)
	}
}

func findSpanAttribute(attrs []attribute.KeyValue, key attribute.Key) (string, bool) {
	for _, attr := range attrs {
		if attr.Key == key {
			return attr.Value.AsString(), true
		}
	}
	return "", false
}
