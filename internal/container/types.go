// Package container reconciles Credimi Runner-managed Docker resources without
// generating or invoking Docker Compose.
package container

import "context"

const (
	ManagedLabel     = "io.credimi.runner.managed"
	RunnerLabel      = "io.credimi.runner.id"
	FingerprintLabel = "io.credimi.runner.fingerprint"
)

type Mount struct {
	Source, Target string
	ReadOnly       bool
}
type Port struct {
	HostIP                  string
	HostPort, ContainerPort int
}
type Spec struct {
	Name, Image, PullPolicy, Network string
	Labels, Environment              map[string]string
	Mounts                           []Mount
	Ports                            []Port
	Devices                          []string
	// Command is appended after the image. It is deliberately structured so a
	// reconciled resource never needs a shell for its normal startup path.
	Command    []string
	ExtraHosts []string
	Privileged bool
}
type Resource struct {
	Name    string
	Labels  map[string]string
	Running bool
	Spec    Spec
}
type Engine interface {
	EnsureNetwork(context.Context, string) error
	List(context.Context, map[string]string) ([]Resource, error)
	Pull(context.Context, string) error
	Create(context.Context, Spec) error
	Start(context.Context, string) error
	Stop(context.Context, string) error
	Remove(context.Context, string) error
	Logs(context.Context, string, int) (string, error)
}
type Result struct {
	Created, Adopted, Recreated, Started, Removed []string
	Failures                                      map[string]error
}
