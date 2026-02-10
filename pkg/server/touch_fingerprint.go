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

func (s *runnerService) touchFingerprintLogic() (*touchFingerprintResult, *runner.APIError) {
	s.Deps.Sleeper(5 * time.Second)

	output, err := s.Deps.CommandRunner.Run("adb", "-e", "emu", "finger", "touch", "1")
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
