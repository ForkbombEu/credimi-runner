package driver

import (
	"context"
	"errors"
	"testing"
)

type fakeRunner struct {
	output []byte
	name   string
	args   []string
}

func TestComposeReportsUnavailableDockerAndCommandFailure(t *testing.T) {
	old := execLookPath
	t.Cleanup(func() { execLookPath = old })
	execLookPath = func(string) (string, error) { return "", errors.New("missing") }
	result := Compose{}.Observe(context.Background(), Request{ComposeServices: []ExpectedService{{Name: "runner", Kind: "compose"}}})
	if result.Error == nil {
		t.Fatal("expected docker error")
	}

	execLookPath = func(string) (string, error) { return "docker", nil }
	result = (Compose{Runner: failingRunner{}}).Observe(context.Background(), Request{ComposeServices: []ExpectedService{{Name: "runner", Kind: "compose"}}})
	if result.Error == nil {
		t.Fatal("expected compose command error")
	}
}

func TestParseComposePSIgnoresMalformedRows(t *testing.T) {
	rows := ParseComposePS([]byte("not json\n{\"Name\":\"fallback\",\"State\":\"running\"}\n"))
	if rows["fallback"].State != "running" {
		t.Fatalf("rows = %#v", rows)
	}
}

type failingRunner struct{}

func (failingRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("boom")
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.name, f.args = name, args
	return f.output, nil
}

func TestComposeObservesConfiguredProjectOnly(t *testing.T) {
	old := execLookPath
	execLookPath = func(string) (string, error) { return "docker", nil }
	t.Cleanup(func() { execLookPath = old })
	fake := &fakeRunner{output: []byte("{\"Service\":\"runner\",\"State\":\"running\",\"Status\":\"Up\",\"Image\":\"runner:latest\"}\n{\"Service\":\"tunnel\",\"State\":\"running\",\"Status\":\"Up\",\"Image\":\"cloudflare\"}\n")}
	result := (Compose{Runner: fake}).Observe(context.Background(), Request{ComposeProject: "test", EnvPath: "/tmp/.env", ComposePath: "/tmp/docker-compose.yaml", ComposeServices: []ExpectedService{{ID: "runner", Name: "runner", Kind: "compose", Critical: true}, {ID: "tunnel", Name: "tunnel", Kind: "compose", Critical: true}}})
	if result.Error != nil || fake.name != "docker" || len(result.Services) == 0 {
		t.Fatalf("unexpected result: %#v command=%s", result, fake.name)
	}
	for _, service := range result.Services {
		if service.Name == "runner" && (!service.Owned || !service.Running) {
			t.Fatalf("runner = %#v", service)
		}
	}
}
