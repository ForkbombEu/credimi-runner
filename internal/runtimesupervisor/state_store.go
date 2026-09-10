package runtimesupervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/forkbombeu/credimi-runner/internal/atomicfile"
)

type StateStore struct{ Path string }

func (s StateStore) Load(configured bool) (PersistentState, error) {
	defaultState := PersistentState{Desired: DesiredStopped, Actual: ActualStopped}
	if !configured {
		return defaultState, nil
	}
	defaultState.Desired = DesiredRunning
	file, err := os.Open(s.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultState, nil
		}
		return PersistentState{}, fmt.Errorf("open runtime state: %w", err)
	}
	defer file.Close()
	var state PersistentState
	if err := json.NewDecoder(file).Decode(&state); err != nil {
		if errors.Is(err, io.EOF) {
			return PersistentState{}, errors.New("runtime state is empty")
		}
		return PersistentState{}, fmt.Errorf("decode runtime state: %w", err)
	}
	if state.Desired != DesiredRunning && state.Desired != DesiredStopped {
		return PersistentState{}, fmt.Errorf("invalid runtime desired state %q", state.Desired)
	}
	if state.Actual != ActualStarting && state.Actual != ActualRunning && state.Actual != ActualStopping && state.Actual != ActualStopped && state.Actual != ActualFailed {
		return PersistentState{}, fmt.Errorf("invalid runtime actual state %q", state.Actual)
	}
	return state, nil
}

func (s StateStore) Save(state PersistentState) error {
	if s.Path == "" {
		return errors.New("runtime state path is empty")
	}
	if state.Desired == "" {
		state.Desired = DesiredStopped
	}
	if state.Actual == "" {
		state.Actual = ActualStopped
	}
	state.UpdatedAt = time.Now().UTC()
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create runtime state directory: %w", err)
	}
	return atomicfile.WriteAtomic(s.Path, 0o600, atomicfile.FromEnvironment(), func(writer io.Writer) error {
		if err := json.NewEncoder(writer).Encode(state); err != nil {
			return fmt.Errorf("write runtime state: %w", err)
		}
		return nil
	})
}
