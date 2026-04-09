package observability

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/buildinfo"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	logglobal "go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	clueotel "goa.design/clue/clue"
	cluelog "goa.design/clue/log"
)

const (
	defaultServiceName  = "credimi-runner"
	defaultHTTPEndpoint = "http://127.0.0.1:4318"
)

type Config struct {
	Enabled        bool
	ServiceName    string
	ServiceVersion string
	Environment    string
	RunnerID       string
	InstanceID     string
	Endpoint       string
}

func ConfigFromEnv() Config {
	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	enabled := endpoint != ""
	if rawEnabled, ok := os.LookupEnv("OTEL_ENABLED"); ok {
		if parsed, err := strconv.ParseBool(strings.TrimSpace(rawEnabled)); err == nil {
			enabled = parsed
		}
	}
	if endpoint == "" {
		endpoint = defaultHTTPEndpoint
	}

	serviceName := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME"))
	if serviceName == "" {
		serviceName = defaultServiceName
	}
	serviceVersion := strings.TrimSpace(os.Getenv("OTEL_SERVICE_VERSION"))
	if serviceVersion == "" {
		serviceVersion = buildinfo.String()
	}

	environment := strings.TrimSpace(os.Getenv("OTEL_DEPLOYMENT_ENVIRONMENT"))
	if environment == "" {
		environment = strings.TrimSpace(os.Getenv("CREDIMI_ENV"))
	}
	if environment == "" {
		environment = strings.TrimSpace(os.Getenv("GO_ENV"))
	}

	runnerID := strings.TrimSpace(os.Getenv("CREDIMI_RUNNER_ID"))
	instanceID := strings.TrimSpace(os.Getenv("OTEL_SERVICE_INSTANCE_ID"))
	if instanceID == "" {
		instanceID = defaultInstanceID()
	}

	return Config{
		Enabled:        enabled,
		ServiceName:    serviceName,
		ServiceVersion: serviceVersion,
		Environment:    environment,
		RunnerID:       runnerID,
		InstanceID:     instanceID,
		Endpoint:       endpoint,
	}
}

func Setup(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}

	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = defaultHTTPEndpoint
	}

	traceExporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(otlpSignalEndpoint(endpoint, "/v1/traces")))
	if err != nil {
		return nil, err
	}
	metricExporter, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(otlpSignalEndpoint(endpoint, "/v1/metrics")))
	if err != nil {
		return nil, err
	}
	logExporter, err := otlploghttp.New(ctx, otlploghttp.WithEndpointURL(otlpSignalEndpoint(endpoint, "/v1/logs")))
	if err != nil {
		return nil, err
	}

	res, err := buildResource(ctx, cfg)
	if err != nil {
		return nil, err
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		sdktrace.WithBatcher(traceExporter),
	)
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
	)
	loggerProvider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
	)

	clueotel.ConfigureOpenTelemetry(ctx, &clueotel.Config{
		MeterProvider:  meterProvider,
		TracerProvider: tracerProvider,
		Propagators:    opentelemetry.DefaultTextMapPropagator,
		ErrorHandler:   clueotel.NewErrorHandler(ctx),
	})
	logglobal.SetLoggerProvider(loggerProvider)

	return func(shutdownCtx context.Context) error {
		var shutdownErr error
		if err := loggerProvider.Shutdown(shutdownCtx); err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
		}
		if err := meterProvider.Shutdown(shutdownCtx); err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
		}
		if err := tracerProvider.Shutdown(shutdownCtx); err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
		}
		return shutdownErr
	}, nil
}

func WrapHandler(handler http.Handler, name string) http.Handler {
	if handler == nil {
		return nil
	}
	if strings.TrimSpace(name) == "" {
		name = defaultServiceName + ".http"
	}
	return otelhttp.NewHandler(handler, name)
}

func NewHTTPClient(base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	cloned := *base
	transport := cloned.Transport
	if transport == nil {
		if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
			transport = defaultTransport.Clone()
		} else {
			transport = http.DefaultTransport
		}
	}
	cloned.Transport = cluelog.Client(otelhttp.NewTransport(transport))
	return &cloned
}

func NewTemporalInterceptor() (interceptor.Interceptor, error) {
	return opentelemetry.NewTracingInterceptor(opentelemetry.TracerOptions{
		Tracer:                  otelapi.Tracer("credimi-runner.temporal"),
		TextMapPropagator:       otelapi.GetTextMapPropagator(),
		AllowInvalidParentSpans: true,
	})
}

func Tracer(name string) trace.Tracer {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		trimmed = defaultServiceName
	}
	return otelapi.Tracer(trimmed)
}

func buildResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{
		semconv.ServiceNameKey.String(cfg.ServiceName),
		attribute.String("service.instance.id", cfg.InstanceID),
	}
	if strings.TrimSpace(cfg.ServiceVersion) != "" {
		attrs = append(attrs, semconv.ServiceVersionKey.String(cfg.ServiceVersion))
	}
	if strings.TrimSpace(cfg.Environment) != "" {
		attrs = append(attrs, attribute.String("deployment.environment", cfg.Environment))
	}
	if strings.TrimSpace(cfg.RunnerID) != "" {
		attrs = append(attrs, attribute.String("runner_id", cfg.RunnerID))
	}
	for key, value := range parseResourceAttributes(os.Getenv("OTEL_RESOURCE_ATTRIBUTES")) {
		attrs = append(attrs, attribute.String(key, value))
	}
	return resource.New(
		ctx,
		resource.WithProcess(),
		resource.WithHost(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(attrs...),
	)
}

func parseResourceAttributes(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	attrs := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		parts := strings.SplitN(strings.TrimSpace(pair), "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			continue
		}
		attrs[key] = value
	}
	return attrs
}

func otlpSignalEndpoint(baseEndpoint, defaultPath string) string {
	trimmed := strings.TrimSpace(baseEndpoint)
	if trimmed == "" {
		return defaultPath
	}

	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return trimmed
	}

	if u.Path == "" || u.Path == "/" {
		u.Path = defaultPath
		return u.String()
	}

	return trimmed
}

func defaultInstanceID() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "unknown-host"
	}
	return host + "-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().Unix(), 10)
}
