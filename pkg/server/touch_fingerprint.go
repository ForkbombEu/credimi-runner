package server

import (
	"fmt"
	"net/http"
	"time"

	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
)

type touchFingerprintResult struct {
	Status string
}

func (s *runnerService) touchFingerprintLogic(deviceIdentifier string) (*touchFingerprintResult, *runner.APIError) {
	device, apiErr := s.configuredDevice(deviceIdentifier)
	if apiErr != nil {
		return nil, apiErr
	}
	if device.Serial == "" {
		return nil, &runner.APIError{Code: http.StatusBadRequest, Domain: "device", Reason: "missing device serial", Message: "device has no configured Android serial"}
	}
	s.Deps.Sleeper(5 * time.Second)

	output, err := s.Deps.CommandRunner.Run("adb", "-s", device.Serial, "emu", "finger", "touch", "1")
	if err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "adb",
			Reason:  "fingerprint touch failed",
			Message: fmt.Sprintf("error: %v, output: %s", err, string(output)),
		}
	}

	return &touchFingerprintResult{Status: "fingerprint touch executed"}, nil
}
