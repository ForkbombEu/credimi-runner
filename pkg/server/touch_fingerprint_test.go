package server

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
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
	server := NewRunnerServiceWithDeps(NewProcessStore(), nil, deps)

	result, apiErr := server.touchFingerprintLogic()

	require.Nil(t, apiErr)
	require.Equal(t, "fingerprint touch executed", result.Status)
	require.Equal(t, "adb", runner.name)
	require.Equal(t, []string{"-e", "emu", "finger", "touch", "1"}, runner.args)
	require.Equal(t, 5*time.Second, slept)
}

func TestTouchFingerprint_Error(t *testing.T) {
	fakeRunner := &fakeCommandRunner{output: []byte("oops"), err: errors.New("boom")}
	deps := Deps{
		CommandRunner: fakeRunner,
		Sleeper:       func(time.Duration) {},
	}
	server := NewRunnerServiceWithDeps(NewProcessStore(), nil, deps)

	result, apiErr := server.touchFingerprintLogic()

	require.Nil(t, result)
	require.Equal(t, &runner.APIError{
		Code:    http.StatusInternalServerError,
		Domain:  "adb",
		Reason:  "fingerprint touch failed",
		Message: "error: boom, output: oops",
	}, apiErr)
}
