package driver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type Request struct {
	ComposeProject  string
	EnvPath         string
	ComposeEnv      []string
	ComposePath     string
	ComposeServices []ExpectedService
}

type ExpectedService struct {
	ID, Name, Role, Kind string
	Critical             bool
}

type Service struct {
	ID, Name, Role, Image, Detail string
	Running, Owned                bool
	Critical                      bool
}

type Result struct {
	Services []Service
	Error    error
}

// Compose observes the configured project's services. It never uses an
// unscoped `docker ps`, so similarly named foreign projects remain foreign.
type Compose struct{ Runner CommandRunner }

func (d Compose) Observe(ctx context.Context, request Request) Result {
	if d.Runner == nil {
		d.Runner = ExecRunner{}
	}
	if _, err := execLookPath("docker"); err != nil {
		return Result{Error: fmt.Errorf("docker unavailable: %w", err)}
	}
	if len(request.ComposeServices) == 0 {
		return Result{}
	}
	args := []string{"compose", "--project-name", request.ComposeProject}
	if len(request.ComposeEnv) == 0 && request.EnvPath != "" {
		args = append(args, "--env-file", request.EnvPath)
	}
	args = append(args, "-f", request.ComposePath, "ps", "--format", "json")
	var output []byte
	var err error
	if runner, ok := d.Runner.(EnvironmentCommandRunner); ok && len(request.ComposeEnv) > 0 {
		output, err = runner.RunWithEnv(ctx, request.ComposeEnv, "docker", args...)
	} else {
		output, err = d.Runner.Run(ctx, "docker", args...)
	}
	if err != nil {
		return Result{Error: fmt.Errorf("observe compose runtime: %w", err)}
	}
	rows, err := ParseComposePS(output)
	if err != nil {
		return Result{Error: fmt.Errorf("parse compose runtime: %w", err)}
	}
	services := make([]Service, 0, len(request.ComposeServices))
	for _, expected := range request.ComposeServices {
		if expected.Kind != "compose" {
			continue
		}
		row, found := rows[expected.Name]
		services = append(services, Service{ID: expected.ID, Name: expected.Name, Role: expected.Role, Image: row.Image, Detail: row.Status, Running: found && row.State == "running", Owned: found, Critical: expected.Critical})
	}
	return Result{Services: services}
}

type ComposeRow struct{ Service, Name, State, Status, Image string }

func ParseComposePS(raw []byte) (map[string]ComposeRow, error) {
	rows := map[string]ComposeRow{}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		var row ComposeRow
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			continue
		}
		key := row.Service
		if key == "" {
			key = row.Name
		}
		if key != "" {
			rows[key] = row
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan compose status: %w", err)
	}
	return rows, nil
}

var execLookPath = func(name string) (string, error) { return exec.LookPath(name) }
