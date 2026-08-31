package server

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/forkbombeu/credimi-runner/pkg/workermanager"
)

const defaultCredimiRoot = "/credimi"

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type FileStore interface {
	MkdirAll(path string, perm os.FileMode) error
	Stat(name string) (os.FileInfo, error)
	Lstat(name string) (os.FileInfo, error)
	ReadDir(name string) ([]os.DirEntry, error)
	Create(name string) (io.WriteCloser, error)
	Open(name string) (io.ReadCloser, error)
	Remove(path string) error
	RemoveAll(path string) error
}

type CommandRunner interface {
	Run(name string, args ...string) ([]byte, error)
}

type WorkerRunnerFactory func(namespace string) func(ctx context.Context) error
type WorkerReadyRunnerFactory func(namespace string) func(ctx context.Context, ready func(error)) error
type InventoryWorkerRunnerFactory func(namespace string, provider workermanager.RuntimeConfigProvider) func(ctx context.Context) error
type InventoryWorkerReadyRunnerFactory func(namespace string, provider workermanager.RuntimeConfigProvider) func(ctx context.Context, ready func(error)) error
type RuntimeConfigLoader func() (dashboardruntime.RunnerRuntimeConfig, error)
type WorkerStartupCheck func(namespace string) error

type Deps struct {
	HTTPClient                        HTTPClient
	FileStore                         FileStore
	CommandRunner                     CommandRunner
	WorkerRunnerFactory               WorkerRunnerFactory
	WorkerReadyRunnerFactory          WorkerReadyRunnerFactory
	InventoryWorkerRunnerFactory      InventoryWorkerRunnerFactory
	InventoryWorkerReadyRunnerFactory InventoryWorkerReadyRunnerFactory
	WorkerStartupCheck                WorkerStartupCheck
	RuntimeConfig                     *dashboardruntime.RunnerRuntimeConfig
	RuntimeConfigLoader               RuntimeConfigLoader
	Sleeper                           func(time.Duration)
	ManagedWorkflowRoot               string
}

func (d *Deps) WithDefaults() {
	if d.HTTPClient == nil {
		d.HTTPClient = http.DefaultClient
	}
	if d.FileStore == nil {
		d.FileStore = osFileStore{}
	}
	if d.CommandRunner == nil {
		d.CommandRunner = execCommandRunner{}
	}
	defaultWorkerRunner := d.WorkerRunnerFactory == nil
	if defaultWorkerRunner {
		d.WorkerRunnerFactory = workermanager.RunTemporalWorker
		d.WorkerReadyRunnerFactory = workermanager.RunTemporalWorkerReady
	}
	defaultInventoryWorker := d.InventoryWorkerRunnerFactory == nil
	if defaultInventoryWorker {
		d.InventoryWorkerRunnerFactory = workermanager.RunTemporalWorkerWithConfigProvider
	}
	if d.InventoryWorkerReadyRunnerFactory == nil && defaultWorkerRunner && defaultInventoryWorker {
		d.InventoryWorkerReadyRunnerFactory = workermanager.RunTemporalWorkerWithConfigProviderReady
	}
	if d.WorkerStartupCheck == nil && defaultWorkerRunner {
		d.WorkerStartupCheck = workermanager.VerifyTemporalWorker
	}
	if d.Sleeper == nil {
		d.Sleeper = time.Sleep
	}
	if d.ManagedWorkflowRoot == "" {
		d.ManagedWorkflowRoot = managedWorkflowRootFromEnvironment()
	}
}

func managedWorkflowRootFromEnvironment() string {
	for _, name := range []string{"CREDIMI_DIR", "CREDIMI_TEMP_DIR"} {
		if root := strings.TrimSpace(utils.GetEnvironmentVariable(name)); root != "" {
			return filepath.Join(root, "workflows")
		}
	}
	return filepath.Join(defaultCredimiRoot, "workflows")
}

type osFileStore struct{}

func (osFileStore) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (osFileStore) Stat(name string) (os.FileInfo, error) {
	return os.Stat(name)
}

func (osFileStore) Lstat(name string) (os.FileInfo, error) {
	return os.Lstat(name)
}

func (osFileStore) ReadDir(name string) ([]os.DirEntry, error) {
	return os.ReadDir(name)
}

func (osFileStore) Create(name string) (io.WriteCloser, error) {
	return os.Create(name)
}

func (osFileStore) Open(name string) (io.ReadCloser, error) {
	return os.Open(name)
}

func (osFileStore) Remove(path string) error {
	return os.Remove(path)
}

func (osFileStore) RemoveAll(path string) error {
	return os.RemoveAll(path)
}

type execCommandRunner struct{}

func (execCommandRunner) Run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}
