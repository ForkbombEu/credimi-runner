package servicemanager

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
)

type LogOptions struct {
	Follow bool
	Lines  int
}

const ComposeProjectEnv = "CREDIMI_COMPOSE_PROJECT"

// BootstrapOptions overrides the image used while the first service
// configuration is being prepared. It is intentionally owned by the service
// manager because only that manager renders and starts the service.
type BootstrapOptions struct {
	Image      string
	PullPolicy string
}

type Status struct {
	Running                bool
	ServiceRestartRequired bool
	DashboardURL           string
	RuntimeDesired         string
	RuntimeActual          string
	RuntimeError           string
}
type Manager interface {
	Start(context.Context) error
	Stop(context.Context) error
	Restart(context.Context) error
	Status(context.Context) (Status, error)
	Logs(context.Context, LogOptions) error
}

// ImageUpgrader is intentionally optional: only the Docker-backed service
// owns a runner image.
type ImageUpgrader interface {
	UpgradeImage(context.Context, func(string)) error
}

var ErrUnsupported = errors.New("service manager is unsupported on this platform")

// ProjectName is the canonical Compose project identity shared by service
// mutations and the Dashboard's read-only observer.
func ProjectName(configDir string, uid int) string {
	canonicalDir, err := filepath.Abs(configDir)
	if err != nil {
		canonicalDir = configDir
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(canonicalDir))
	return fmt.Sprintf("credimi-runner-%d-%08x", uid, hash.Sum32())
}

func currentHostIDs() (int, int, error) {
	uid, gid := os.Getuid(), os.Getgid()
	if uid < 0 || gid < 0 {
		return 0, 0, fmt.Errorf("resolve host UID/GID")
	}
	return uid, gid, nil
}
