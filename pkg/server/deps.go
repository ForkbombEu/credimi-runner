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

type Deps struct {
	HTTPClient          HTTPClient
	FileStore           FileStore
	CommandRunner       CommandRunner
	WorkerRunnerFactory WorkerRunnerFactory
	Sleeper             func(time.Duration)
	ManagedWorkflowRoot string
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
	if d.WorkerRunnerFactory == nil {
		d.WorkerRunnerFactory = workermanager.RunTemporalWorker
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
