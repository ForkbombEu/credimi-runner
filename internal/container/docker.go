package container

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}
type DockerCLI struct{ Runner CommandRunner }
type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
func (d DockerCLI) runner() CommandRunner {
	if d.Runner == nil {
		return execRunner{}
	}
	return d.Runner
}

// EnsureNetwork creates the private shared network exactly once. Docker's
// inspect/create split is safe under concurrent runner starts: an "already
// exists" create race is resolved by one final inspect.
func (d DockerCLI) EnsureNetwork(ctx context.Context, name string) error {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	if _, err := d.runner().Run(ctx, "docker", "network", "inspect", name); err == nil {
		return nil
	}
	if _, err := d.runner().Run(ctx, "docker", "network", "create", name); err == nil {
		return nil
	}
	if _, err := d.runner().Run(ctx, "docker", "network", "inspect", name); err == nil {
		return nil
	}
	return fmt.Errorf("ensure Docker network %q", name)
}
func (d DockerCLI) List(ctx context.Context, labels map[string]string) ([]Resource, error) {
	args := []string{"ps", "-a", "--format", "{{.Names}}\t{{.State}}\t{{.Labels}}"}
	for k, v := range labels {
		args = append(args, "--filter", "label="+k+"="+v)
	}
	out, err := d.runner().Run(ctx, "docker", args...)
	if err != nil {
		return nil, fmt.Errorf("list Docker resources: %w", err)
	}
	var result []Resource
	for _, line := range splitLines(string(out)) {
		var name, state, labelText string
		if _, err := fmt.Sscanf(line, "%s\t%s\t%s", &name, &state, &labelText); err != nil {
			continue
		}
		result = append(result, Resource{Name: name, Running: state == "running", Labels: parseLabels(labelText)})
	}
	return result, nil
}
func (d DockerCLI) Pull(ctx context.Context, image string) error {
	_, err := d.runner().Run(ctx, "docker", "pull", image)
	return err
}
func (d DockerCLI) Create(ctx context.Context, s Spec) error {
	args := []string{"create", "--name", s.Name}
	keys := sorted(s.Labels)
	for _, k := range keys {
		args = append(args, "--label", k+"="+s.Labels[k])
	}
	for _, m := range s.Mounts {
		value := m.Source + ":" + m.Target
		if m.ReadOnly {
			value += ":ro"
		}
		args = append(args, "--volume", value)
	}
	for _, p := range s.Ports {
		args = append(args, "--publish", p.HostIP+":"+strconv.Itoa(p.HostPort)+":"+strconv.Itoa(p.ContainerPort))
	}
	for _, v := range s.Devices {
		args = append(args, "--device", v)
	}
	if s.Privileged {
		args = append(args, "--privileged")
	}
	for _, host := range s.ExtraHosts {
		args = append(args, "--add-host", host)
	}
	if s.Network != "" {
		args = append(args, "--network", s.Network)
	}
	for _, k := range sorted(s.Environment) {
		args = append(args, "--env", k+"="+s.Environment[k])
	}
	args = append(args, s.Image)
	args = append(args, s.Command...)
	_, err := d.runner().Run(ctx, "docker", args...)
	return err
}
func (d DockerCLI) Start(ctx context.Context, name string) error {
	_, err := d.runner().Run(ctx, "docker", "start", name)
	return err
}
func (d DockerCLI) Stop(ctx context.Context, name string) error {
	_, err := d.runner().Run(ctx, "docker", "stop", name)
	return err
}
func (d DockerCLI) Remove(ctx context.Context, name string) error {
	_, err := d.runner().Run(ctx, "docker", "rm", name)
	return err
}
func (d DockerCLI) Logs(ctx context.Context, name string, lines int) (string, error) {
	out, err := d.runner().Run(ctx, "docker", "logs", "--tail", strconv.Itoa(lines), name)
	return string(out), err
}
func sorted(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
func splitLines(s string) []string {
	var result []string
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}
func parseLabels(value string) map[string]string {
	result := map[string]string{}
	for _, entry := range strings.Split(value, ",") {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			result[parts[0]] = parts[1]
		}
	}
	return result
}
