package container

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type commandRunner struct {
	calls  []string
	output []byte
}

type failingCommandRunner struct{}

func (failingCommandRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("docker unavailable")
}

func (r *commandRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	return r.output, nil
}

type fakeEngine struct {
	resources map[string]Resource
	calls     []string
}

type errorEngine struct {
	resources  []Resource
	listErr    error
	networkErr error
	pullErr    error
	createErr  error
	startErr   error
	stopErr    error
	removeErr  error
}

func (e errorEngine) EnsureNetwork(context.Context, string) error { return e.networkErr }
func (e errorEngine) List(context.Context, map[string]string) ([]Resource, error) {
	return e.resources, e.listErr
}
func (e errorEngine) Pull(context.Context, string) error                { return e.pullErr }
func (e errorEngine) Create(context.Context, Spec) error                { return e.createErr }
func (e errorEngine) Start(context.Context, string) error               { return e.startErr }
func (e errorEngine) Stop(context.Context, string) error                { return e.stopErr }
func (e errorEngine) Remove(context.Context, string) error              { return e.removeErr }
func (e errorEngine) Logs(context.Context, string, int) (string, error) { return "", nil }

func (f *fakeEngine) EnsureNetwork(_ context.Context, network string) error {
	f.calls = append(f.calls, "network:"+network)
	return nil
}
func (f *fakeEngine) List(context.Context, map[string]string) ([]Resource, error) {
	out := []Resource{}
	for _, r := range f.resources {
		out = append(out, r)
	}
	return out, nil
}
func (f *fakeEngine) Pull(_ context.Context, image string) error {
	f.calls = append(f.calls, "pull:"+image)
	return nil
}
func (f *fakeEngine) Create(_ context.Context, s Spec) error {
	f.calls = append(f.calls, "create:"+s.Name)
	f.resources[s.Name] = Resource{Name: s.Name, Labels: s.Labels, Spec: s}
	return nil
}
func (f *fakeEngine) Start(_ context.Context, n string) error {
	f.calls = append(f.calls, "start:"+n)
	r := f.resources[n]
	r.Running = true
	f.resources[n] = r
	return nil
}
func (f *fakeEngine) Stop(_ context.Context, n string) error {
	f.calls = append(f.calls, "stop:"+n)
	return nil
}
func (f *fakeEngine) Remove(_ context.Context, n string) error {
	f.calls = append(f.calls, "remove:"+n)
	delete(f.resources, n)
	return nil
}
func (f *fakeEngine) Logs(context.Context, string, int) (string, error) { return "", nil }
func TestReconcilerCreatesAdoptsRecreatesAndRemoves(t *testing.T) {
	e := &fakeEngine{resources: map[string]Resource{}}
	r := Reconciler{Engine: e, RunnerID: "acme/runner"}
	desired := []Spec{{Name: "agent", Image: "agent", PullPolicy: "never"}}
	first := r.Reconcile(context.Background(), desired)
	if len(first.Created) != 1 || len(first.Started) != 1 {
		t.Fatalf("%#v", first)
	}
	second := r.Reconcile(context.Background(), desired)
	if len(second.Adopted) != 1 {
		t.Fatalf("%#v", second)
	}
	changed := r.Reconcile(context.Background(), []Spec{{Name: "agent", Image: "other", PullPolicy: "never"}})
	if len(changed.Recreated) != 1 {
		t.Fatalf("%#v", changed)
	}
	removed := r.Reconcile(context.Background(), nil)
	if len(removed.Removed) != 1 {
		t.Fatalf("%#v", removed)
	}
}

func TestReconcilerReportsEachEngineFailureWithoutContinuingTheResource(t *testing.T) {
	boom := errors.New("boom")
	test := func(engine Engine, desired []Spec, key string) {
		t.Helper()
		result := (Reconciler{Engine: engine, RunnerID: "acme/runner"}).Reconcile(context.Background(), desired)
		if !errors.Is(result.Failures[key], boom) {
			t.Fatalf("failures=%v, expected %q", result.Failures, key)
		}
	}
	test(errorEngine{listErr: boom}, nil, "list")
	test(errorEngine{networkErr: boom}, []Spec{{Name: "agent", Image: "agent", Network: "net"}}, "network:net")
	test(errorEngine{pullErr: boom}, []Spec{{Name: "agent", Image: "agent", PullPolicy: "always"}}, "agent")
	test(errorEngine{createErr: boom}, []Spec{{Name: "agent", Image: "agent", PullPolicy: "never"}}, "agent")
	test(errorEngine{startErr: boom}, []Spec{{Name: "agent", Image: "agent", PullPolicy: "never"}}, "agent")
	changed := Resource{Name: "agent", Labels: map[string]string{FingerprintLabel: "old"}, Running: true}
	test(errorEngine{resources: []Resource{changed}, stopErr: boom}, []Spec{{Name: "agent", Image: "agent", PullPolicy: "never"}}, "agent")
	test(errorEngine{resources: []Resource{changed}, removeErr: boom}, nil, "agent")
}

type networkRunner struct {
	calls     []string
	responses []error
}

func (runner *networkRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	runner.calls = append(runner.calls, name+" "+strings.Join(args, " "))
	err := runner.responses[0]
	runner.responses = runner.responses[1:]
	return nil, err
}

func TestDockerCLIEnsuresExistingOrNewPrivateNetwork(t *testing.T) {
	existing := &commandRunner{}
	if err := (DockerCLI{Runner: existing}).EnsureNetwork(context.Background(), "credimi-private"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(existing.calls, "\n"); got != "docker network inspect credimi-private" {
		t.Fatalf("existing network calls=%q", got)
	}

	created := &networkRunner{responses: []error{errors.New("missing"), nil}}
	if err := (DockerCLI{Runner: created}).EnsureNetwork(context.Background(), "credimi-private"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(created.calls, "\n"); got != "docker network inspect credimi-private\ndocker network create credimi-private" {
		t.Fatalf("created network calls=%q", got)
	}
}

func TestDockerCLICreatesDeterministicSafeArguments(t *testing.T) {
	runner := &commandRunner{}
	docker := DockerCLI{Runner: runner}
	spec := Spec{Name: "agent", Image: "agent:latest", Network: "net", Labels: map[string]string{"b": "2", "a": "1"}, Environment: map[string]string{"Z": "z", "A": "a"}, Mounts: []Mount{{"/host", "/container", true}}, Ports: []Port{{"127.0.0.1", 8060, 8060}}, Devices: []string{"/dev/kvm"}, Privileged: true, ExtraHosts: []string{"host.docker.internal:host-gateway"}, Command: []string{"agent", "--listen", "0.0.0.0:8060"}}
	if err := docker.Create(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	call := runner.calls[0]
	for _, want := range []string{"docker create --name agent", "--label a=1 --label b=2", "--volume /host:/container:ro", "--publish 127.0.0.1:8060:8060", "--device /dev/kvm", "--privileged", "--add-host host.docker.internal:host-gateway", "--network net", "--env A=a --env Z=z", "agent:latest agent --listen 0.0.0.0:8060"} {
		if !strings.Contains(call, want) {
			t.Fatalf("call=%q missing %q", call, want)
		}
	}
	runner.output = []byte("agent\trunning\tio.credimi.runner.managed=true,io.credimi.runner.id=acme/runner\n")
	resources, err := docker.List(context.Background(), map[string]string{ManagedLabel: "true"})
	if err != nil || len(resources) != 1 || !resources[0].Running || resources[0].Labels[RunnerLabel] != "acme/runner" {
		t.Fatalf("resources=%#v err=%v", resources, err)
	}
	if err := docker.Pull(context.Background(), "agent:latest"); err != nil {
		t.Fatal(err)
	}
	if err := docker.Start(context.Background(), "agent"); err != nil {
		t.Fatal(err)
	}
	if err := docker.Stop(context.Background(), "agent"); err != nil {
		t.Fatal(err)
	}
	if err := docker.Remove(context.Background(), "agent"); err != nil {
		t.Fatal(err)
	}
	if logs, err := docker.Logs(context.Background(), "agent", 20); err != nil || logs != string(runner.output) {
		t.Fatalf("logs=%q err=%v", logs, err)
	}
	if (DockerCLI{}).runner() == nil {
		t.Fatal("default runner missing")
	}
}

func TestDockerCLIPropagatesCommandFailures(t *testing.T) {
	docker := DockerCLI{Runner: failingCommandRunner{}}
	ctx := context.Background()
	_, err := docker.List(ctx, map[string]string{ManagedLabel: "true"})
	if err == nil || !strings.Contains(err.Error(), "list Docker resources") {
		t.Fatalf("list error=%v", err)
	}
	if err := docker.Pull(ctx, "agent"); err == nil {
		t.Fatal("pull unexpectedly succeeded")
	}
	if err := docker.Create(ctx, Spec{Name: "agent", Image: "agent"}); err == nil {
		t.Fatal("create unexpectedly succeeded")
	}
	if err := docker.Start(ctx, "agent"); err == nil {
		t.Fatal("start unexpectedly succeeded")
	}
	if err := docker.Stop(ctx, "agent"); err == nil {
		t.Fatal("stop unexpectedly succeeded")
	}
	if err := docker.Remove(ctx, "agent"); err == nil {
		t.Fatal("remove unexpectedly succeeded")
	}
	if _, err := docker.Logs(ctx, "agent", 1); err == nil {
		t.Fatal("logs unexpectedly succeeded")
	}
}
