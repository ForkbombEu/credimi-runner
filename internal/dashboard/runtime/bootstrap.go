package runtime

import (
	"strconv"
	"strings"
)

const (
	BootstrapImageEnv       = "CREDIMI_BOOTSTRAP_IMAGE"
	BootstrapPullPolicyEnv  = "CREDIMI_BOOTSTRAP_PULL_POLICY"
	ConfigOwnerUIDEnv       = "CREDIMI_CONFIG_OWNER_UID"
	ConfigOwnerGIDEnv       = "CREDIMI_CONFIG_OWNER_GID"
	HostHomeEnv             = "CREDIMI_HOST_HOME"
	HostAndroidDirEnv       = "CREDIMI_HOST_ANDROID_DIR"
	HostGoldenRootEnv       = "CREDIMI_HOST_GOLDEN_ROOT"
	ContainerAndroidDirEnv  = "CREDIMI_CONTAINER_ANDROID_DIR"
	ContainerAVDHomeEnv     = "CREDIMI_CONTAINER_AVD_HOME"
	ContainerGoldenRootEnv  = "CREDIMI_CONTAINER_GOLDEN_ROOT"
	BootstrapHostNetworkEnv = "CREDIMI_BOOTSTRAP_HOST_NETWORK"
)

// BootstrapContext contains host-only values needed before typed TOML exists.
// It is passed through the launcher/container boundary and is never written
// by the dashboard's TOML adapter.
type BootstrapContext struct {
	RunnerImage         string
	PullPolicy          string
	HostUID             int
	HostGID             int
	HostHome            string
	HostAndroidDir      string
	HostGoldenRoot      string
	ContainerAndroidDir string
	ContainerAVDHome    string
	ContainerGoldenRoot string
	HostNetwork         bool
}

func (c BootstrapContext) Apply(values Values) Values {
	if values == nil {
		values = Values{}
	}
	set := func(key, value string) {
		if strings.TrimSpace(value) != "" {
			values[key] = value
		}
	}
	set(BootstrapImageEnv, c.RunnerImage)
	set(BootstrapPullPolicyEnv, c.PullPolicy)
	if c.HostUID > 0 {
		set(ConfigOwnerUIDEnv, strconv.Itoa(c.HostUID))
	}
	if c.HostGID > 0 {
		set(ConfigOwnerGIDEnv, strconv.Itoa(c.HostGID))
	}
	set(HostHomeEnv, c.HostHome)
	set(HostAndroidDirEnv, c.HostAndroidDir)
	set(HostGoldenRootEnv, c.HostGoldenRoot)
	set(ContainerAndroidDirEnv, c.ContainerAndroidDir)
	set(ContainerAVDHomeEnv, c.ContainerAVDHome)
	set(ContainerGoldenRootEnv, c.ContainerGoldenRoot)
	if c.HostNetwork {
		values[BootstrapHostNetworkEnv] = "true"
	}
	return values
}
