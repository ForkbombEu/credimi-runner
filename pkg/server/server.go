package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

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

	for name, inst := range s.Instances {

		token, err := s.Deps.TokenProvider(inst)
		if err != nil {
			log.Printf("[WARN] Skipping instance %q: cannot fetch admin token: %v", name, err)
			continue
		}
		orgsURL := utils.JoinURL(inst.URL, "api", "collections", "organizations", "records")

		req, err := http.NewRequest("GET", orgsURL, nil)
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := s.Deps.HTTPClient.Do(req)
		if err != nil {
			return fmt.Errorf("failed to fetch organizations: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("failed to fetch organizations: %s", resp.Status)
		}

		var data struct {
			Items []struct {
				Name      string `json:"name"`
				Namespace string `json:"canonified_name"`
			} `json:"items"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			return fmt.Errorf("failed to parse organizations response: %w", err)
		}
		for _, org := range data.Items {
			if org.Namespace == "" {
				continue
			}

			if proc, exists := s.Store.Get(org.Namespace); exists && proc.Running {
				log.Printf("Worker already running for org.Namespace %s", org.Namespace)
				continue
			}

			log.Printf("Starting worker for organization %s (%s)", org.Name, org.Namespace)
			proc := NewProcess(org.Namespace, s.Deps.WorkerRunnerFactory(org.Namespace))
			s.Store.Add(proc)

			if err := proc.Start(); err != nil {
				log.Printf("Failed to start worker for %s: %v", org.Namespace, err)
			}
		}
	}
	return nil
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
