package server

import (
	"net/http"

	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
)

type processStartResult struct {
	Status    string
	Namespace string
}

func (s *runnerService) processStart(namespace, oldNamespace string) (*processStartResult, *runner.APIError) {
	if namespace == "" {
		return nil, &runner.APIError{
			Code:    http.StatusBadRequest,
			Domain:  "Server",
			Reason:  "NamespaceMissing",
			Message: "namespace is required",
		}
	}

	if oldNamespace != "" {
		if oldProc, exist := s.Store.Get(oldNamespace); exist && oldProc.IsRunning() {
			oldProc.Stop()
		}
	}

	proc, exists := s.Store.Get(namespace)
	if !exists {
		proc = NewProcess(namespace, s.Deps.WorkerRunnerFactory(namespace))
		s.Store.Add(proc)
	}

	if proc.IsRunning() {
		return &processStartResult{Status: "already running", Namespace: namespace}, nil
	}

	if err := proc.Start(); err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "worker",
			Reason:  "worker start failed",
			Message: err.Error(),
		}
	}

	return &processStartResult{Status: "started", Namespace: namespace}, nil
}
