package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/pkg/observability"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/forkbombeu/credimi-runner/pkg/workermanager"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type runnerService struct {
	Store    *ProcessStore
	Instance utils.Instance
	Deps     Deps

	authCacheMu sync.Mutex
	authCache   map[string]time.Time
}

func NewRunnerService(store *ProcessStore, instance utils.Instance) *runnerService {
	return NewRunnerServiceWithDeps(store, instance, Deps{})
}

func NewRunnerServiceWithDeps(store *ProcessStore, instance utils.Instance, deps Deps) *runnerService {
	deps.WithDefaults()
	if deps.RuntimeConfig == nil {
		deps.RuntimeConfigLoader = func() (dashboardruntime.RunnerRuntimeConfig, error) {
			return dashboardruntime.RuntimeConfigFromEnvironment()
		}
		if config, err := dashboardruntime.RuntimeConfigFromEnvironment(); err == nil {
			deps.RuntimeConfig = &config
		}
	}
	return &runnerService{
		Store:     store,
		Instance:  instance,
		Deps:      deps,
		authCache: make(map[string]time.Time),
	}
}

func (s *runnerService) StartExistingWorkers(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, span := observability.Tracer("credimi-runner.startup").Start(ctx, "start_existing_workers")
	defer span.End()

	startDelay := startupWorkerDelay()
	startAttempts := 0
	runnerID := utils.GetEnvironmentVariable("CREDIMI_RUNNER_ID")
	if config, err := s.currentRuntimeConfig(); err == nil {
		runnerID = config.Host["CREDIMI_RUNNER_ID"]
	}
	runnerPublished, _ := strconv.ParseBool(utils.GetEnvironmentVariable("CREDIMI_RUNNER_PUBLISHED"))

	inst := s.Instance
	if inst.URL == "" {
		return nil
	}
	if inst.UserAPIKey == "" {
		namespaces, err := s.fetchAdminNamespaces(ctx, inst)
		if err != nil {
			return err
		}
		for _, namespace := range namespaces {
			startAttempts = s.startWorkerIfNeeded(ctx, span, namespace, namespace, runnerID, startAttempts, startDelay)
		}
		return nil
	}
	if runnerPublished {
		namespaces, err := s.fetchVisibleNamespaces(ctx, inst)
		if err != nil {
			return err
		}
		for _, namespace := range namespaces {
			startAttempts = s.startWorkerIfNeeded(ctx, span, namespace, namespace, runnerID, startAttempts, startDelay)
		}
		return nil
	}

	orgName, namespace, err := s.fetchUserNamespace(ctx, inst)
	if err != nil {
		return err
	}
	s.startWorkerIfNeeded(ctx, span, orgName, namespace, runnerID, startAttempts, startDelay)
	return nil
}

func (s *runnerService) fetchAdminNamespaces(ctx context.Context, inst utils.Instance) ([]string, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		utils.JoinURL(inst.URL, "api", "organizations", "namespaces"),
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create organizations namespace request: %w", err)
	}
	setAPIKeyHeader(req, inst.InternalAdminKey)

	resp, err := s.Deps.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch organization namespaces: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch organization namespaces: %s", resp.Status)
	}

	var data struct {
		Namespaces []string `json:"namespaces"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to parse organization namespaces response: %w", err)
	}

	return data.Namespaces, nil
}

func (s *runnerService) fetchVisibleNamespaces(ctx context.Context, inst utils.Instance) ([]string, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		utils.JoinURL(inst.URL, "api", "organizations", "visible-namespaces"),
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create visible organizations namespace request: %w", err)
	}
	setAPIKeyHeader(req, inst.UserAPIKey)

	resp, err := s.Deps.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch visible organization namespaces: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch visible organization namespaces: %s", resp.Status)
	}

	var data struct {
		Namespaces []string `json:"namespaces"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to parse visible organization namespaces response: %w", err)
	}

	return data.Namespaces, nil
}

func workerTraceAttrs(orgName, namespace, runnerID string) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("namespace", namespace),
	}
	if orgName != "" {
		attrs = append(attrs, attribute.String("organization.name", orgName))
	}
	if runnerID != "" {
		attrs = append(attrs, attribute.String("runner_id", runnerID))
	}
	return attrs
}

func (s *runnerService) fetchUserNamespace(
	ctx context.Context,
	inst utils.Instance,
) (string, string, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		utils.JoinURL(inst.URL, "api", "organizations", "my"),
		nil,
	)
	if err != nil {
		return "", "", fmt.Errorf("failed to create organization lookup request: %w", err)
	}
	setAPIKeyHeader(req, inst.UserAPIKey)

	resp, err := s.Deps.HTTPClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch organization for configured API key: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf(
			"failed to fetch organization for configured API key: %s",
			resp.Status,
		)
	}

	var data struct {
		Name      string `json:"name"`
		Namespace string `json:"canonified_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", "", fmt.Errorf("failed to parse organization response: %w", err)
	}
	if data.Namespace == "" {
		return "", "", fmt.Errorf("organization namespace is empty for instance %s", inst.URL)
	}

	return data.Name, data.Namespace, nil
}

func (s *runnerService) startWorkerIfNeeded(
	ctx context.Context,
	span trace.Span,
	orgName string,
	namespace string,
	runnerID string,
	startAttempts int,
	startDelay time.Duration,
) int {
	attrs := workerTraceAttrs(orgName, namespace, runnerID)
	if namespace == "" {
		span.AddEvent("worker.start_skipped", trace.WithAttributes(append(attrs, attribute.String("reason", "namespace_empty"))...))
		return startAttempts
	}
	if proc, exists := s.Store.Get(namespace); exists && proc.Running {
		span.AddEvent("worker.already_running", trace.WithAttributes(attrs...))
		log.Printf("Worker already running for namespace %s", namespace)
		observability.Info(ctx, "credimi-runner.startup", "worker already running for namespace",
			observability.String("organization.name", orgName),
			observability.String("namespace", namespace),
		)
		return startAttempts
	}
	if startDelay > 0 && startAttempts > 0 {
		s.Deps.Sleeper(startDelay)
	}
	startAttempts++
	span.AddEvent("worker.start_requested", trace.WithAttributes(attrs...))

	log.Printf("Starting worker for organization %s (%s)", orgName, namespace)
	observability.RecordWorkerStart(ctx,
		attribute.String("organization.name", orgName),
		attribute.String("namespace", namespace),
		attribute.String("runner_id", runnerID),
	)
	observability.Info(ctx, "credimi-runner.startup", "starting worker for namespace",
		observability.String("organization.name", orgName),
		observability.String("namespace", namespace),
	)
	run := s.Deps.WorkerRunnerFactory(namespace)
	if (s.Deps.RuntimeConfigLoader != nil || s.Deps.RuntimeConfig != nil) && s.Deps.InventoryWorkerRunnerFactory != nil {
		if _, err := s.currentRuntimeConfig(); err == nil {
			provider := func() (workermanager.RunnerRuntimeConfig, error) {
				config, err := s.currentRuntimeConfig()
				if err != nil {
					return workermanager.RunnerRuntimeConfig{}, err
				}
				return workerInventory(config), nil
			}
			run = s.Deps.InventoryWorkerRunnerFactory(namespace, provider)
		}
	}
	proc := NewProcess(namespace, run)
	s.Store.Add(proc)

	if err := proc.Start(); err != nil {
		span.AddEvent("worker.start_failed", trace.WithAttributes(append(attrs, attribute.String("error", err.Error()))...))
		span.RecordError(err)
		log.Printf("Failed to start worker for %s: %v", namespace, err)
		observability.RecordWorkerStartFailure(ctx,
			attribute.String("organization.name", orgName),
			attribute.String("namespace", namespace),
			attribute.String("runner_id", runnerID),
		)
		observability.Error(ctx, "credimi-runner.startup", "failed to start worker for namespace", err,
			observability.String("organization.name", orgName),
			observability.String("namespace", namespace),
		)
		return startAttempts
	}
	span.AddEvent("worker.started", trace.WithAttributes(attrs...))

	return startAttempts
}

func (s *runnerService) currentRuntimeConfig() (dashboardruntime.RunnerRuntimeConfig, error) {
	if s.Deps.RuntimeConfigLoader != nil {
		return s.Deps.RuntimeConfigLoader()
	}
	if s.Deps.RuntimeConfig != nil {
		return *s.Deps.RuntimeConfig, nil
	}
	return dashboardruntime.RunnerRuntimeConfig{}, fmt.Errorf("runtime configuration is unavailable")
}

func startupWorkerDelay() time.Duration {
	const defaultWorkerStartDelayMS = 50
	delayMS, err := utils.GetEnvironmentVariableAsInteger("CREDIMI_WORKER_START_DELAY_MS", defaultWorkerStartDelayMS)
	if err != nil {
		log.Printf("[WARN] Invalid CREDIMI_WORKER_START_DELAY_MS value: %v (using %d)", err, defaultWorkerStartDelayMS)
		return defaultWorkerStartDelayMS * time.Millisecond
	}
	if delayMS < 0 {
		log.Printf("[WARN] Negative CREDIMI_WORKER_START_DELAY_MS value %d (using %d)", delayMS, defaultWorkerStartDelayMS)
		return defaultWorkerStartDelayMS * time.Millisecond
	}
	return time.Duration(delayMS) * time.Millisecond
}
