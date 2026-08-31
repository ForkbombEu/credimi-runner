package runtimesupervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
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
	tmp, err := os.CreateTemp(dir, ".runtime-state-*")
	if err != nil {
		return fmt.Errorf("create runtime state temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	encErr := json.NewEncoder(tmp).Encode(state)
	if encErr == nil {
		encErr = tmp.Sync()
	}
	if closeErr := tmp.Close(); encErr == nil {
		encErr = closeErr
	}
	if encErr != nil {
		return fmt.Errorf("write runtime state: %w", encErr)
	}
	if err := os.Rename(tmpPath, s.Path); err != nil {
		return fmt.Errorf("replace runtime state: %w", err)
	}
	if dirFile, err := os.Open(dir); err == nil {
		if err := dirFile.Sync(); err != nil {
			_ = dirFile.Close()
			return fmt.Errorf("sync runtime state directory: %w", err)
		}
		if err := dirFile.Close(); err != nil {
			return fmt.Errorf("close runtime state directory: %w", err)
		}
	}
	return nil
}
