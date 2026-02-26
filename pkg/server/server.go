package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/forkbombeu/credimi-runner/pkg/utils"
)

type runnerService struct {
	Store     *ProcessStore
	Instances map[string]utils.Instance
	Deps      Deps
}

func NewRunnerService(store *ProcessStore, instances map[string]utils.Instance) *runnerService {
	return NewRunnerServiceWithDeps(store, instances, Deps{})
}

func NewRunnerServiceWithDeps(store *ProcessStore, instances map[string]utils.Instance, deps Deps) *runnerService {
	deps.WithDefaults()
	return &runnerService{
		Store:     store,
		Instances: instances,
		Deps:      deps,
	}
}

func (s *runnerService) StartExistingWorkers() error {
	startDelay := startupWorkerDelay()
	startAttempts := 0

	for name, inst := range s.Instances {
		token, err := s.Deps.TokenProvider(inst)
		if err != nil {
			log.Printf("[WARN] Skipping instance %q: cannot fetch admin token: %v", name, err)
			continue
		}
		orgsURL := utils.JoinURL(inst.URL, "api", "collections", "organizations", "records")

		const perPage = 200
		for page := 1; ; page++ {
			req, err := http.NewRequest("GET", orgsURL, nil)
			if err != nil {
				return fmt.Errorf("failed to create request: %w", err)
			}
			query := req.URL.Query()
			query.Set("page", strconv.Itoa(page))
			query.Set("perPage", strconv.Itoa(perPage))
			req.URL.RawQuery = query.Encode()
			req.Header.Set("Authorization", "Bearer "+token)

			resp, err := s.Deps.HTTPClient.Do(req)
			if err != nil {
				return fmt.Errorf("failed to fetch organizations: %w", err)
			}

			if resp.StatusCode != http.StatusOK {
				status := resp.Status
				_ = resp.Body.Close()
				return fmt.Errorf("failed to fetch organizations: %s", status)
			}

			var data struct {
				Page       int `json:"page"`
				PerPage    int `json:"perPage"`
				TotalPages int `json:"totalPages"`
				Items      []struct {
					Name      string `json:"name"`
					Namespace string `json:"canonified_name"`
				} `json:"items"`
			}

			if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
				_ = resp.Body.Close()
				return fmt.Errorf("failed to parse organizations response: %w", err)
			}
			_ = resp.Body.Close()

			for _, org := range data.Items {
				if org.Namespace == "" {
					continue
				}

				if proc, exists := s.Store.Get(org.Namespace); exists && proc.Running {
					log.Printf("Worker already running for org.Namespace %s", org.Namespace)
					continue
				}

				if startDelay > 0 && startAttempts > 0 {
					s.Deps.Sleeper(startDelay)
				}
				startAttempts++

				log.Printf("Starting worker for organization %s (%s)", org.Name, org.Namespace)
				proc := NewProcess(org.Namespace, s.Deps.WorkerRunnerFactory(org.Namespace))
				s.Store.Add(proc)

				if err := proc.Start(); err != nil {
					log.Printf("Failed to start worker for %s: %v", org.Namespace, err)
				}
			}

			if data.TotalPages > 0 {
				if page >= data.TotalPages {
					break
				}
				continue
			}

			if data.PerPage <= 0 || len(data.Items) < data.PerPage {
				break
			}
		}
	}
	return nil
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

func (s *runnerService) getInstanceByURL(rawURL string) (utils.Instance, error) {
	normalizedInput, err := utils.NormalizeURL(rawURL)
	if err != nil {
		return utils.Instance{}, err
	}

	for _, inst := range s.Instances {

		if inst.URL == normalizedInput {
			return inst, nil
		}
	}

	return utils.Instance{}, fmt.Errorf("no instance found for URL: %s", rawURL)
}
