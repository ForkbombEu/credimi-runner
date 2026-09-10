package server

import (
	"errors"
	"net/http"
	"testing"
	"time"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/stretchr/testify/require"
)

type fakeCommandRunner struct {
	name string
	args []string

	output []byte
	err    error
}

func (f *fakeCommandRunner) Run(name string, args ...string) ([]byte, error) {
	f.name = name
	f.args = append([]string(nil), args...)
	return f.output, f.err
}

func TestTouchFingerprint_Success(t *testing.T) {
	runner := &fakeCommandRunner{output: []byte("ok")}
	var slept time.Duration
	deps := Deps{
		CommandRunner: runner,
		Sleeper: func(d time.Duration) {
			slept = d
		},
	}
	deps.RuntimeConfig = &dashboardruntime.RunnerRuntimeConfig{Host: dashboardruntime.Values{"CREDIMI_RUNNER_ID": "acme/runner"}, Devices: []dashboardruntime.DeviceRuntimeConfig{{ID: "acme/runner/emulator", Enabled: true, Serial: "emulator-5554"}}}
	server := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{}, deps)

	result, apiErr := server.touchFingerprintLogic("acme/runner/emulator")

	require.Nil(t, apiErr)
	require.Equal(t, "fingerprint touch executed", result.Status)
	require.Equal(t, "adb", runner.name)
	require.Equal(t, []string{"-s", "emulator-5554", "emu", "finger", "touch", "1"}, runner.args)
	require.Equal(t, 5*time.Second, slept)
}

func TestTouchFingerprint_Error(t *testing.T) {
	fakeRunner := &fakeCommandRunner{output: []byte("oops"), err: errors.New("boom")}
	deps := Deps{
		CommandRunner: fakeRunner,
		Sleeper:       func(time.Duration) {},
	}
	deps.RuntimeConfig = &dashboardruntime.RunnerRuntimeConfig{Host: dashboardruntime.Values{"CREDIMI_RUNNER_ID": "acme/runner"}, Devices: []dashboardruntime.DeviceRuntimeConfig{{ID: "acme/runner/emulator", Enabled: true, Serial: "emulator-5554"}}}
	server := NewRunnerServiceWithDeps(NewProcessStore(), utils.Instance{}, deps)

	result, apiErr := server.touchFingerprintLogic("acme/runner/emulator")

	require.Nil(t, result)
	require.Equal(t, &runner.APIError{
		Code:    http.StatusInternalServerError,
		Domain:  "adb",
		Reason:  "fingerprint touch failed",
		Message: "error: boom, output: oops",
	}, apiErr)
}
