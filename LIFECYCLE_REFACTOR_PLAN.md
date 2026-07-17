# Credimi Runner Lifecycle and Controller Refactor Plan

Status: proposed implementation plan  
Scope: dashboard process, controller, runner runtime, Docker Compose, host processes, ADB readiness, CLI lifecycle commands, Credimi registration, and lifecycle diagnostics  
Primary deployment: a runner host managed remotely over SSH  

## 1. Decisions already agreed

The implementation must preserve these product decisions.

1. `DASHBOARD_HOST=0.0.0.0` remains the default for now.
2. Nothing is installed or enabled to start automatically when the computer boots.
3. Docker must not restart Credimi Runner services merely because the Docker daemon or computer restarted.
4. The plain command remains the primary end-user command:

   ```bash
   credimi-runner
   ```

5. If no dashboard is running, the plain command starts the dashboard. If a valid configuration already exists, it also ensures that the configured runner runtime is started.
6. If the dashboard is already running, the plain command must not restart or otherwise mutate the runtime. It discovers the existing dashboard, reports its URL, attempts to open it when a browser is available, and exits successfully.
7. Explicit CLI commands may be added for operators and automation.
8. The controller writes a bounded lifecycle log in the runner configuration directory. This log records controller, dashboard, runner, device, tunnel, and registration lifecycle events without copying the complete high-volume runner logs.
9. Existing dashboard visual changes must follow `DESIGN.md`.

The default `0.0.0.0` decision means remote exposure remains intentional. The implementation must not silently change it. `0.0.0.0` is a bind address, not a useful browser destination: user-facing output should resolve a usable hostname or server address while continuing to listen on all interfaces. The dashboard should continue supporting `DASHBOARD_TOKEN`, and documentation should strongly recommend setting it on hosts reachable by untrusted networks.

## 2. Desired operator experience

### 2.1 Primary command

The command must be idempotent.

#### No configuration exists

```bash
$ credimi-runner
Credimi Runner dashboard: http://runner-server:8051
Runner is not configured. Complete setup in the dashboard.
```

The dashboard starts, but no runtime is started.

#### Configuration exists and nothing is running

```bash
$ credimi-runner
Credimi Runner dashboard: http://runner-server:8051
Starting configured runner...
```

The dashboard starts first. It creates a background start operation for the configured runtime. The HTTP dashboard becomes available immediately and displays live progress.

The CLI may continue streaming the same lifecycle operation until it succeeds or fails. The operation itself belongs to the controller and must not be cancelled if the terminal or browser disconnects.

#### Dashboard already runs

```bash
$ credimi-runner
Credimi Runner dashboard is already running: http://server.example:8051
Runner state: running
```

The command must:

- positively identify the existing Credimi dashboard;
- avoid creating another dashboard process;
- avoid calling runner start, stop, restart, registration, pull, or Compose reconciliation that changes state;
- attempt to open the dashboard only when a supported local graphical browser is available;
- always print the usable URL;
- exit with code `0`.

When invoked through SSH, browser opening will normally be skipped. Printing the URL is the authoritative behavior.

#### Dashboard is absent but runtime services remain running

This can occur after the dashboard was terminated while Docker containers or a detached host runner continued running.

The plain command starts the dashboard, observes the runtime, adopts it without restarting healthy services, validates the current public endpoint, and repairs Credimi registration only when it is missing or stale. Adoption must not recreate a healthy runner or tunnel.

#### Port 8051 belongs to another application

The command must not adopt an arbitrary listener. It returns a clear error containing:

- the conflicting address;
- the detected process when available;
- the configured dashboard port;
- commands for inspecting the conflict;
- the fact that the listener did not present a valid Credimi controller identity.

### 2.2 Reboot behavior

After a host reboot:

- the dashboard is stopped;
- the runner is stopped;
- Caddy and Cloudflare are stopped;
- no Credimi Runner container is restarted by Docker;
- no systemd service is enabled automatically;
- running `credimi-runner` manually starts the dashboard and configured runtime again.

Installing an optional service unit is acceptable, but installation must not enable it. Enabling boot startup must require a separate, explicit future operator action and is outside this refactor.

### 2.3 Dashboard termination behavior

Gracefully stopping only the dashboard must not implicitly stop an already-running runtime. This preserves the existing ability to close or replace the dashboard without interrupting jobs.

The distinction must be explicit:

- `dashboard stop`: stop the controller/dashboard only;
- `runtime stop`: stop runner, proxy, and tunnel services;
- `stop`: stop the runtime first and then stop the dashboard.

If the dashboard dies unexpectedly, the runtime may remain running until reboot or an explicit stop. Because Docker restart policies are disabled, a container that crashes while the dashboard is absent does not enter an uncontrolled restart loop.

## 3. Problems to remove

The implementation is complete only when it removes these failure classes.

1. In-memory booleans are currently treated as runtime truth even though Docker and detached processes have independent state.
2. A new dashboard process loses previous runtime state.
3. Docker uses `restart: unless-stopped`, causing partial and uncoordinated boot startup.
4. Container startup is considered successful as soon as `docker compose up -d` exits.
5. Container runner readiness is not verified before tunnel discovery and registration.
6. Android startup waits for any connected ADB device instead of the configured serial.
7. A 30-second browser request owns long-running pulls, startup, and registration.
8. Client disconnection can cancel orchestration and leave partial external state.
9. A failed detached host child can leave a non-nil `exec.Cmd`, preventing a later start.
10. Host process reuse currently proves only that a TCP port accepts connections, not that the process is the intended runner.
11. A second plain CLI invocation reports only a port conflict instead of reopening the existing dashboard.
12. Quick-tunnel URLs can become stale when Docker starts a new tunnel without the dashboard.
13. Dashboard probes observe real Docker state but do not reconcile it into controller state.
14. Full runner output is too noisy to serve as an understandable controller incident record.

## 4. Architecture

Do not rewrite the complete application. Replace the orchestration core and adapt the existing dashboard and CLI around it.

### 4.1 Keep

Keep and incrementally adapt:

- dashboard templates, renderer, static assets, and SSE transport;
- configuration storage and normalization;
- runtime-plan and Compose-generation concepts;
- host, Docker, ADB, iOS, and Temporal probes;
- Credimi registration client;
- runner HTTP server;
- runner pause, resume, and heartbeat client;
- maintenance and binary/image upgrade capabilities.

### 4.2 Replace

Replace:

- `internal/dashboard/runtime.LifecycleManager` as lifecycle authority;
- synchronous request-owned runtime actions;
- status booleans mutated only by the last command;
- generic ADB startup waits;
- implicit Compose restart policies;
- blind dashboard port binding;
- detached-child state based only on `*exec.Cmd`.

### 4.3 New package boundaries

Create the following packages. Exact names may change only if an existing repository rule is documented before implementation.

```text
internal/controller/
    controller.go          controller lifecycle and public application service
    instance.go            dashboard single-instance lock and identity metadata
    operation.go           background operation coordinator
    state.go               controller and runtime state types
    reconcile.go           read-only observed-state reconciliation
    startup.go             primary-command ensure behavior

internal/controller/driver/
    driver.go              runtime driver interface
    compose.go             container and proxy/tunnel driver
    host.go                detached host runner driver
    observed.go            normalized service/process observations

internal/lifecyclelog/
    logger.go              JSON Lines event writer
    event.go               schema, levels, and event names
    redact.go              allowlist-based field sanitization
    rotate.go              bounded rotation

internal/runneridentity/
    identity.go            runner identity and readiness payload
```

The controller package must not import dashboard rendering code. The dashboard may depend on the controller application service.

## 5. State model

### 5.1 Controller state

```go
type ControllerState string

const (
    ControllerStarting ControllerState = "starting"
    ControllerRunning  ControllerState = "running"
    ControllerStopping ControllerState = "stopping"
    ControllerFailed   ControllerState = "failed"
)
```

### 5.2 Runtime phase

```go
type RuntimePhase string

const (
    RuntimeUnconfigured      RuntimePhase = "unconfigured"
    RuntimeStopped           RuntimePhase = "stopped"
    RuntimeStarting          RuntimePhase = "starting"
    RuntimeWaitingForDevice  RuntimePhase = "waiting_for_device"
    RuntimeWaitingForRunner  RuntimePhase = "waiting_for_runner"
    RuntimeStartingNetwork   RuntimePhase = "starting_network"
    RuntimeDiscoveringURL    RuntimePhase = "discovering_public_url"
    RuntimeRegistering       RuntimePhase = "registering"
    RuntimeRunning           RuntimePhase = "running"
    RuntimeDegraded          RuntimePhase = "degraded"
    RuntimeStopping          RuntimePhase = "stopping"
    RuntimeFailed            RuntimePhase = "failed"
)
```

### 5.3 Observed state versus operation state

Keep these concepts separate.

```go
type ObservedRuntime struct {
    ObservedAt       time.Time
    Backend          string
    Runner           ObservedService
    Proxy            ObservedService
    Tunnel           ObservedService
    Device           ObservedDevice
    PublicURL        string
    PublicSessionID  string
    Registration     RegistrationObservation
    Healthy          bool
    DegradedReasons  []string
}

type OperationSnapshot struct {
    ID          string
    Kind        OperationKind
    Trigger     OperationTrigger
    Phase       RuntimePhase
    StartedAt   time.Time
    UpdatedAt   time.Time
    FinishedAt  time.Time
    Message     string
    Error       string
    Running     bool
}
```

`ObservedRuntime` is derived from Docker, process identity, ADB, HTTP readiness, tunnel identity, and registration evidence. It is never populated merely because a start command returned successfully.

`OperationSnapshot` describes work in progress. A runtime can, for example, be observed as degraded while a reconnect operation is running.

### 5.4 Status computation

The dashboard status endpoint must call a read-only reconcile operation or return a recent observation with an explicit age.

Rules:

- a running Docker container is not automatically a healthy service;
- the runner is healthy only when its readiness endpoint succeeds and its identity matches;
- an Android runner is healthy only when the configured device is visible and ready;
- quick-tunnel health requires a URL belonging to the current tunnel session;
- public registration health requires the registered endpoint to match the observed endpoint;
- stale observations must be labeled stale rather than presented as current truth.

The existing `RunnerRunning` and `ComposeRunning` booleans may remain temporarily in the dashboard view model as derived compatibility fields, but no code may mutate them as lifecycle authority.

## 6. Runtime driver contract

Use a small explicit interface.

```go
type Driver interface {
    Observe(ctx context.Context, cfg runtime.Values) (ObservedRuntime, error)
    Preflight(ctx context.Context, cfg runtime.Values, report Reporter) error
    StartRunner(ctx context.Context, cfg runtime.Values, report Reporter) error
    WaitRunnerReady(ctx context.Context, cfg runtime.Values, report Reporter) error
    StartNetwork(ctx context.Context, cfg runtime.Values, report Reporter) error
    ResolvePublicEndpoint(ctx context.Context, cfg runtime.Values, report Reporter) (PublicEndpoint, error)
    Stop(ctx context.Context, cfg runtime.Values, report Reporter) error
    Down(ctx context.Context, cfg runtime.Values, report Reporter) error
    Logs(ctx context.Context, cfg runtime.Values, request LogRequest) ([]runtime.LogLine, error)
}
```

Do not expose `exec.Cmd` or raw Docker command construction to controller or dashboard packages.

`Reporter` records operation progress once and fans it out to:

- lifecycle JSONL logging;
- in-memory operation history;
- dashboard SSE;
- the foreground CLI when attached.

## 7. Operation coordinator

### 7.1 Background ownership

Dashboard HTTP handlers must not execute lifecycle transitions directly.

```go
func (c *Coordinator) Start(kind OperationKind, trigger OperationTrigger) (OperationSnapshot, error)
func (c *Coordinator) Current() OperationSnapshot
func (c *Coordinator) Get(id string) (OperationSnapshot, bool)
func (c *Coordinator) Cancel(id string) error
```

`Start` creates an operation context derived from the controller lifetime, not from `http.Request.Context()`.

The HTTP handler returns `202 Accepted` with an operation ID:

```json
{
  "operation_id": "op-01J...",
  "kind": "runtime_start",
  "phase": "starting"
}
```

The dashboard follows progress through SSE or polling. Closing the page does not cancel the operation. Cancellation requires an explicit endpoint or CLI command.

### 7.2 Serialization

Only one mutating lifecycle operation may run at a time.

Read-only observation may execute concurrently, but it must use bounded timeouts and must not block behind an image pull for several minutes.

If an operation is already active, a second mutation returns a typed conflict containing the active operation ID and phase. It must not wait silently for a mutex until the request expires.

### 7.3 Timeouts

Do not use one timeout for the complete transaction. Use phase-specific limits:

| Phase | Initial limit | Notes |
|---|---:|---|
| ADB preflight | 10 seconds | Return exact serial and state |
| image pull | 30 minutes | Stream progress; explicitly cancellable |
| Compose command | 2 minutes | Command completion only |
| runner readiness | 2 minutes | Poll readiness and device health |
| quick-tunnel URL | 90 seconds | Scope logs to current session |
| Credimi registration | 30 seconds | Independent HTTP timeout |
| graceful stop | 30 seconds | Escalate only after deadline |

These limits should be constants with test overrides, not additional public configuration in the first implementation.

## 8. Start algorithm

Implement one controller transaction for CLI startup, dashboard Start, and runtime Start.

### 8.1 Read and normalize configuration

1. Load `.env` from the selected config directory.
2. Normalize values once.
3. Produce a secret-free configuration fingerprint from lifecycle-relevant keys.
4. Build the runtime plan.
5. Log `operation.started` with backend, runner type, device mode, service mode, version, and fingerprint.

Never log the environment array or raw `.env` contents.

### 8.2 Observe before mutation

Call `Driver.Observe` first.

- If the complete runtime is healthy, do not restart it.
- If the runner is healthy but the quick-tunnel registration is stale, repair only endpoint registration.
- If only non-critical services are degraded, return a degraded observation and apply the smallest required repair.
- If an incompatible or foreign service occupies the runner port, fail without killing it.

This makes start idempotent and allows dashboard reattachment.

### 8.3 Device preflight

For an Android physical device:

1. Require a non-empty configured serial.
2. Run `adb -s <serial> get-state`.
3. Require the exact response `device`.
4. Run a cheap command such as `adb -s <serial> shell echo credimi-ready`.
5. Distinguish not found, offline, unauthorized, permission denied, and ADB server unavailable.
6. Log the exact failure category and actionable message.

For Wi-Fi mode, reconnect the configured `IP:PORT` before the checks when needed.

For emulator and iOS modes, keep their existing prerequisites but report them through the same typed preflight result.

### 8.4 Image pull

Image pull must be a separate visible phase.

- Respect `RUNNER_IMAGE_PULL_POLICY`.
- Do not pull a local image configured with `never`.
- Stream milestones to the operation reporter.
- Do not write every Docker layer progress line to the lifecycle log. Store phase start, periodic summarized progress, final image ID, duration, and errors.

### 8.5 Start the runner before network registration

For container mode:

1. Atomically write the generated Compose file.
2. Start the runner service.
3. Wait for the runner readiness endpoint.
4. Verify runner identity and configured device.
5. Start Caddy and the selected tunnel only after runner readiness, unless Compose dependencies require Caddy earlier.

For host mode:

1. Verify no matching healthy runner already exists.
2. Start the detached child with a new process group and lifecycle-only child metadata.
3. Immediately start a reaper goroutine calling `Wait`.
4. Clear the in-memory child handle when it exits.
5. Record exit code, signal, duration, and bounded log-tail diagnostics.
6. Wait for authenticated runner readiness before continuing.

### 8.6 Runner readiness and identity

Add a small readiness endpoint distinct from generic TCP reachability.

Suggested response:

```json
{
  "status": "ready",
  "service": "credimi-runner",
  "runner_id": "organization/runner",
  "instance_id": "01J...",
  "boot_id": "01J...",
  "version": "v2.x",
  "device": {
    "serial": "device-serial",
    "state": "device"
  }
}
```

Requirements:

- endpoint uses the existing runner API authentication model;
- controller checks service identity, runner ID, and expected boot/session identity;
- a foreign listener on port 8050 cannot be adopted;
- readiness remains false while the configured device is absent or unauthorized;
- health and readiness are separate: liveness may remain true during temporary device loss.

### 8.7 Public endpoint

#### Manual mode

Validate and use `RUNNER_PUBLIC_URL` and `RUNNER_PUBLIC_PORT`.

#### Managed Cloudflare mode

Validate and use the configured stable domain. Confirm the named tunnel container is running.

#### Quick-tunnel mode

Never reuse a URL solely because it appears in historical logs.

1. Capture the current tunnel container ID.
2. Capture its Docker `StartedAt` timestamp.
3. Treat the pair as the public session ID.
4. Read only that container's logs since its exact start.
5. Extract the URL.
6. Store the URL with its public session ID in the current observation.
7. Reject cached URLs whose session ID differs.

### 8.8 Registration

Registration occurs only after runner readiness and endpoint resolution.

Record:

- registration attempt number;
- endpoint mode;
- public URL host, with credentials and query parameters removed;
- HTTP result category and status;
- duration;
- whether the operation created, updated, or confirmed an existing record.

Do not log API keys, authorization headers, tunnel tokens, or raw response bodies containing secrets.

After registration, observe again. Report success only if the final observed runtime is healthy and registration matches the current endpoint.

## 9. Stop, restart, and down algorithms

### 9.1 Runtime stop

1. Observe current state.
2. If already stopped, return success without mutation.
3. Set operation phase to `stopping`.
4. Send graceful termination to the runner.
5. Allow the runner lifecycle client to send its pause event.
6. Stop tunnel and proxy services.
7. Run Compose `down --remove-orphans` for the managed project.
8. Preserve named volumes unless the operator explicitly requests data removal through a future command.
9. Verify managed containers and runner listener are absent.
10. Record final stopped observation.

### 9.2 Runtime restart

Restart is one serialized operation, not `Stop` followed by an unrelated `Start` request.

- stop cleanly;
- retain one operation ID and one diagnostic timeline;
- run the complete readiness and registration sequence;
- never report success between the two halves.

### 9.3 Runtime down

`runtime down` is retained as an advanced alias for a full Compose teardown. Its distinction from `runtime stop` should be removed unless there is a documented product need. The preferred final contract is that `runtime stop` already performs a clean teardown while preserving volumes.

### 9.4 Dashboard stop

Dashboard stop must:

- stop accepting new operations;
- allow an active operation to finish or require explicit `--force` cancellation;
- flush and sync the lifecycle log;
- shut down HTTP and SSE cleanly;
- release the instance lock;
- leave an already-running runtime untouched.

## 10. Docker Compose changes

### 10.1 Restart policy

Generate this for every managed service:

```yaml
restart: "no"
```

Apply it to:

- runner;
- runner host adapter;
- Caddy;
- quick tunnel;
- named tunnel;
- emulator runner variants.

Update repository example Compose files consistently.

### 10.2 Stable project identity

Every Docker command must use the same explicit project identity, env file, and Compose file. Do not rely on the current working directory or the basename `runner`.

Derive a stable, non-secret project name from:

- current OS user ID where available;
- canonical config-directory path hash;
- a fixed `credimi-runner` prefix.

Example:

```text
credimi-runner-1000-a13f82c1
```

Add controller labels to every service:

```yaml
labels:
  io.credimi.runner.managed: "true"
  io.credimi.runner.controller-id: "${CREDIMI_CONTROLLER_ID}"
  io.credimi.runner.config-fingerprint: "${CREDIMI_CONFIG_FINGERPRINT}"
```

These labels allow safe observation and cleanup without adopting unrelated containers.

### 10.3 Health checks

Add health checks where they are reliable:

- runner: authenticated or internal readiness probe;
- Caddy: local HTTP reachability;
- tunnel: container process plus resolved current-session endpoint.

The controller remains the final health authority because Docker health cannot validate Credimi registration.

### 10.4 Existing-container migration

On the first run after upgrade:

1. Detect legacy project containers using the known Compose file and service names.
2. Record their exact current state.
3. Set their Docker restart policy to `no` without restarting healthy containers.
4. Adopt healthy containers for the current session.
5. Recreate them under the explicit project identity during the next operator-requested restart.
6. Write a one-time `migration.completed` lifecycle event.

The migration must not unexpectedly interrupt a running job merely to rename containers.

## 11. Android disconnect and reconnect behavior

### 11.1 Startup while disconnected

If the configured device is absent, start must remain in `waiting_for_device` and show the exact serial. It may wait for the configured readiness timeout, then fail with a typed error. It must not start against a different connected phone.

### 11.2 Disconnect while running

Temporary device loss should not kill the dashboard or corrupt controller state.

- runner liveness remains online;
- runner readiness becomes degraded;
- dashboard displays `Configured device disconnected`;
- lifecycle log records `device.disconnected` once per transition, not every poll;
- repeated identical probe failures are coalesced;
- when the same serial returns and passes a command probe, readiness returns automatically;
- lifecycle log records `device.reconnected` with outage duration;
- the runner process is restarted only if an integration test proves its ADB/worker stack cannot recover in place.

### 11.3 Entrypoint changes

Extend the phone entrypoint with an explicit serial argument or environment contract.

Example:

```bash
phone-connect --host-adb --usb --serial "$CREDIMI_RUNNER_SERIAL"
```

The wait loop must use:

```bash
adb -s "$serial" get-state
```

For Wi-Fi mode, the connected target and configured serial must match.

Set `ANDROID_SERIAL` inside the runner container so downstream ADB and Maestro commands select the intended device by default. Confirm that explicit activity arguments continue to override it safely.

## 12. Dashboard single-instance protocol

### 12.1 Lock

Hold an advisory lock for the complete dashboard lifetime. Store the lock in the config directory or a per-user runtime directory with mode `0700`; the file itself must be `0600`.

The lock prevents two Credimi dashboard processes from both believing they own the same config directory. A PID file alone is not sufficient authority because PIDs are reusable and stale files survive crashes.

Implement platform-specific locking behind a small interface and build tags.

### 12.2 Identity metadata

While holding the lock, atomically write controller metadata:

```json
{
  "schema": 1,
  "controller_id": "01J...",
  "pid": 1234,
  "process_started_at": "2026-07-17T12:00:00Z",
  "listen_host": "0.0.0.0",
  "listen_port": 8051,
  "probe_url": "http://127.0.0.1:8051/internal/controller/identity",
  "identity_token": "random-local-token"
}
```

The metadata file is `0600`. The identity token is generated for each dashboard boot and is used only for local controller discovery. It must never be printed or written to lifecycle logs.

### 12.3 Second invocation

When lock acquisition fails:

1. Read metadata.
2. Call the loopback identity endpoint with the local identity token.
3. Verify controller ID, process start time, executable identity where available, and config directory fingerprint.
4. Fetch aggregate status.
5. print the dashboard URL and runtime state;
6. attempt browser opening;
7. exit `0`.

If verification fails, return a conflict error. Do not kill the existing process automatically.

### 12.4 Browser reopening

Browser opening is best effort.

- Local desktop: use the platform opener.
- SSH without display: skip opener and print URL.
- `--open-browser=false`: never invoke an opener.
- The command must not fail merely because browser opening failed.

## 13. CLI contract

Add these commands while keeping the plain command primary.

```text
credimi-runner
credimi-runner status [--json]

credimi-runner dashboard start [--foreground] [--open-browser]
credimi-runner dashboard status [--json]
credimi-runner dashboard open
credimi-runner dashboard stop [--force]

credimi-runner runtime start [--wait]
credimi-runner runtime status [--json]
credimi-runner runtime stop [--wait]
credimi-runner runtime restart [--wait]
credimi-runner runtime cancel <operation-id>
credimi-runner runtime logs [--tail N] [--follow]

credimi-runner lifecycle-log path
credimi-runner lifecycle-log tail [--lines N] [--follow]
credimi-runner lifecycle-log export [--output PATH]

credimi-runner stop
```

Behavior:

- `status` reports controller, runtime, device, public endpoint, registration, and active operation.
- Commands prefer the running controller API when available.
- If a runtime command requires a controller and none exists, `runtime start` may start the dashboard controller first.
- `runtime status` remains read-only and may perform direct observation if no controller exists.
- `stop` performs runtime stop followed by dashboard stop.
- `stop-server` is removed. `credimi-runner runtime stop` is the only runtime-stop command.
- `serve` remains hidden and intended only as the controller-managed runner child/container entrypoint.

All status commands must have deterministic exit codes:

| Condition | Exit code |
|---|---:|
| healthy/running as requested | 0 |
| stopped but command succeeded | 0 |
| degraded | 2 |
| failed/unreachable | 1 |
| conflicting operation | 3 |

## 14. Lifecycle log

### 14.1 Purpose

The lifecycle log is the compact incident timeline for a human or LLM. It must answer:

- what command or dashboard action happened;
- when it happened and how long it took;
- what state was observed before and after;
- which exact phase failed;
- whether the configured device was present;
- which services were started, stopped, adopted, or found unhealthy;
- which tunnel session and public endpoint were selected;
- whether Credimi registration succeeded;
- whether a timeout, cancellation, signal, process exit, or reboot boundary was involved;
- what the operator should inspect next.

It is not a duplicate of `runner.log`, Docker logs, Maestro logs, or complete image-pull output.

### 14.2 Path and permissions

Default path:

```text
<config-dir>/lifecycle.jsonl
```

Permissions:

- config directory: `0700`;
- active log: `0600`;
- rotated logs: `0600`;
- never follow symlinks when opening or rotating lifecycle logs;
- write rotation and replacement atomically where supported.

### 14.3 JSON Lines schema

Each physical line is one valid JSON object.

```json
{
  "schema": 1,
  "timestamp": "2026-07-17T12:34:56.123456Z",
  "level": "info",
  "event": "runtime.phase_changed",
  "message": "Runner HTTP endpoint is ready",
  "controller_id": "01J...",
  "operation_id": "op-01J...",
  "trigger": "dashboard",
  "component": "runner",
  "phase": "waiting_for_runner",
  "duration_ms": 1823,
  "fields": {
    "backend": "container",
    "runner_type": "android_phone",
    "device_mode": "usb",
    "device_serial": "R3CN...",
    "runner_port": 8050,
    "container_id": "2a6f...",
    "health": "ready"
  },
  "error": null,
  "hint": ""
}
```

Use stable event names suitable for search and automated summarization.

### 14.4 Required events

At minimum:

```text
controller.starting
controller.started
controller.adopted
controller.stopping
controller.stopped
controller.signal_received
controller.crash_recovery

operation.started
operation.phase_changed
operation.succeeded
operation.failed
operation.cancel_requested
operation.cancelled
operation.conflict

runtime.observed
runtime.adopted
runtime.degraded
runtime.recovered
runtime.stopped

device.preflight_started
device.ready
device.disconnected
device.reconnected
device.unauthorized
device.offline

process.started
process.exited
process.signal_sent
process.force_killed

compose.command_started
compose.command_finished
compose.service_state_changed
compose.migration_started
compose.migration_completed

image.pull_started
image.pull_progress
image.pull_completed

tunnel.started
tunnel.endpoint_discovered
tunnel.endpoint_rejected_stale
tunnel.stopped

registration.started
registration.succeeded
registration.failed

config.loaded
config.changed
config.invalid
```

### 14.5 Redaction

Use an allowlist of permitted fields. Do not attempt to make arbitrary maps safe only with substring replacement.

Never log:

- API keys;
- internal admin keys;
- Cloudflare tunnel tokens;
- dashboard tokens;
- authorization headers;
- ADB private/public key contents;
- SSH passwords;
- raw environment arrays;
- raw `.env` content;
- request or response bodies that can contain credentials.

URLs must be sanitized:

- remove user information;
- remove query strings and fragments unless explicitly known safe;
- keep scheme, host, and non-secret port;
- quick-tunnel hostname may be retained because it is necessary to diagnose stale registration.

Device serials may be retained because exact device selection is required for diagnosis and the file is private. The export command should support `--anonymize`, replacing serials, hostnames, runner IDs, and public URLs with stable hashes.

### 14.6 Noise control

The logger must coalesce repeated state.

- Log transitions, not every two-second probe.
- Repeated identical failures increment a count and emit a summary periodically.
- Image progress emits at most one summary every five seconds or meaningful percentage transition.
- Heartbeat success is not logged every 30 seconds; log first success, state changes, and failures/recovery.
- Complete Compose or runner output is not copied.
- On failure, attach at most 20 sanitized tail lines per relevant service and cap each line and the total diagnostic payload.

### 14.7 Rotation and durability

Initial policy:

- rotate at 5 MiB;
- retain three rotated files;
- names: `lifecycle.jsonl.1`, `.2`, `.3`;
- delete only the oldest lifecycle log managed by this logger;
- flush after every event;
- call `Sync` for operation completion, failure, controller stop, and fatal events;
- tolerate log-write failure without crashing the runner, but report the failure to stderr and dashboard status.

Keep the implementation in the standard library.

### 14.8 LLM export

`lifecycle-log export` creates a bounded Markdown diagnostic report from JSONL. It should contain:

1. controller and binary version;
2. sanitized configuration summary;
3. final observed state;
4. active or last operation;
5. chronological lifecycle timeline;
6. coalesced error groups;
7. bounded service log tails captured on failures;
8. explicit reminder that secrets were redacted;
9. commands an operator can run for additional evidence.

The export must default to stdout when no output path is provided. It must not call an external LLM or send data over the network.

## 15. Dashboard changes

### 15.1 Runtime card

Replace the binary running/stopped presentation with the controller phase.

Display:

- phase and plain-language message;
- operation elapsed time;
- configured device and current state;
- runner readiness;
- tunnel mode and current endpoint;
- registration state;
- observation timestamp;
- a visible stale-data warning when applicable.

Use existing neubrutalist components and `DESIGN.md` colors:

- green for confirmed healthy;
- orange for waiting/degraded;
- coral for failed/destructive actions;
- blue for informational/adopted states;
- yellow for the primary available action.

Never rely on color alone.

### 15.2 Controls

- Disable conflicting lifecycle controls while an operation is active.
- Show `Cancel operation` only for cancellable phases.
- Keep the progress panel visible after navigation or browser reconnect.
- Do not instruct users to keep the page open.
- Render server-provided operation state after reconnect.

### 15.3 Lifecycle diagnostics

Add a compact diagnostics section containing:

- current lifecycle log path;
- `View recent lifecycle events`;
- `Copy diagnostic summary`;
- `Export LLM diagnostic report`;
- latest operation error and hint.

Do not render the complete JSONL file into the normal page. Fetch a bounded recent-event view.

## 16. Controller API

Add internal authenticated controller endpoints. They may use JSON even though normal dashboard fragments use HTML.

```text
GET    /internal/controller/identity
GET    /api/controller/status
GET    /api/controller/operations/current
GET    /api/controller/operations/{id}
POST   /api/controller/runtime/start
POST   /api/controller/runtime/stop
POST   /api/controller/runtime/restart
POST   /api/controller/operations/{id}/cancel
GET    /api/controller/lifecycle/recent
GET    /api/controller/lifecycle/export
POST   /api/controller/shutdown
```

Requirements:

- normal dashboard authentication continues to protect browser-facing controller actions;
- local CLI discovery uses the separate boot-scoped identity token;
- controller identity endpoint reveals no secrets;
- mutating endpoints return operation IDs;
- status endpoints are read-only;
- shutdown cannot be triggered without valid authentication;
- CSRF protection must be evaluated for browser POST operations before preserving the current form behavior.

## 17. Runner lifecycle integration

Retain runner-originated resume, heartbeat, and pause events. They describe runner availability and are complementary to dashboard registration.

Changes:

- include runner boot/session ID in lifecycle calls if the upstream API supports it;
- log only lifecycle failures and recovery in the controller log;
- make graceful stop wait for pause attempt within its bounded timeout;
- do not treat a failed pause HTTP request as permission to leave the local process running;
- do not use heartbeat as proof that the public endpoint registration is correct.

## 18. Testing strategy

### 18.1 Unit tests

Add table-driven tests for:

- all state derivations;
- config-present and config-absent plain-command behavior;
- dashboard lock acquisition and stale metadata;
- verified adoption versus foreign port conflict;
- operation serialization and typed conflicts;
- request cancellation not cancelling controller operations;
- phase-specific timeout behavior;
- exact Android serial selection;
- ADB offline, unauthorized, missing, and recovered states;
- host child exit reaping and later restart;
- Compose project identity and labels;
- `restart: "no"` in every generated service;
- quick-tunnel session scoping;
- stale tunnel URL rejection;
- registration only after readiness;
- lifecycle JSON schema;
- redaction of every known secret field;
- log rotation and permissions;
- event coalescing;
- anonymized Markdown export;
- CLI exit codes and JSON output.

### 18.2 Driver contract tests

Create a reusable contract suite executed against fake host and Compose drivers.

Contract assertions:

- `Start` is idempotent;
- `Stop` is idempotent;
- observe never mutates;
- successful start ends healthy;
- failed start returns a precise phase;
- partial start is observable;
- cancellation leaves an observable and recoverable state;
- healthy adoption causes no restart;
- foreign resources are not killed.

### 18.3 HTTP tests

Test:

- start returns `202` rapidly;
- operation continues after request cancellation;
- progress is available to a new client;
- conflicts include active operation details;
- identity authentication rejects invalid tokens;
- dashboard authentication remains enforced;
- lifecycle export never exposes seeded secrets.

### 18.4 Integration tests

Add an integration build tag or task for tests requiring Docker and local sockets.

Required scenarios:

1. start, stop, start with a fake ADB device service;
2. start with no device, connect configured device, recover;
3. start with the wrong device connected and ensure it is rejected;
4. disconnect the configured device while running and reconnect without restarting dashboard;
5. stop runner, disconnect device, reconnect, start runner;
6. terminate dashboard while runtime runs, restart dashboard, adopt without recreation;
7. invoke plain command twice and verify one dashboard process;
8. occupy port 8051 with a foreign HTTP server and verify safe failure;
9. interrupt the initiating HTTP client during image pull and verify operation continuity;
10. restart Docker and verify managed containers do not auto-start;
11. simulate host reboot by stopping controller and Docker state, then verify no automatic process appears;
12. restart quick tunnel and verify only its new URL is registered;
13. fail Credimi registration, preserve healthy local runtime, then retry only registration;
14. crash detached host runner and verify the next explicit start works;
15. rotate lifecycle logs while operations write concurrently.

Do not require a real phone, Credimi production API, or Cloudflare network for deterministic CI. Use fake ADB, fake registration, and fake tunnel processes. Keep a separate opt-in smoke test for real infrastructure.

### 18.5 Regression acceptance test for the reported failure

The following exact sequence is a release blocker:

```text
Given a configured Android runner and connected configured device
And the runner is healthy
When runtime stop completes
And the configured device disconnects
And the configured device reconnects
And runtime start is requested
Then the exact configured device passes preflight
And the runner readiness endpoint becomes ready
And the current tunnel endpoint is discovered
And Credimi registration matches that endpoint
And the runtime is reported healthy
And the dashboard process is never restarted
```

## 19. Implementation phases

Each phase should be a small reviewable change. Do not combine the whole refactor into one large commit.

### Phase 0: workflow gate and characterization

1. Resolve the existing repository formatter gap documented in `HITL.md` before implementation commits.
2. Add characterization tests for current plain command, stop, Compose generation, and runtime status behavior.
3. Add the reported regression test in a failing or fake-driver form.
4. Document current CLI compatibility requirements.

Exit criteria: tests capture existing behavior and the target regression is reproducible.

### Phase 1: lifecycle log foundation

1. Add `internal/lifecyclelog`.
2. Implement schema, allowlist redaction, permissions, rotation, coalescing, and export.
3. Initialize logger immediately after config-directory resolution.
4. Instrument current controller start/stop and current manager operations without changing behavior.

Exit criteria: current lifecycle produces a bounded, secret-free incident timeline.

### Phase 2: observed-state drivers

1. Introduce `Driver` and normalized observed types.
2. Move Docker inspection out of dashboard probes into shared read-only driver logic.
3. Adapt probes to consume observations where appropriate.
4. Add authenticated runner readiness/identity endpoint.
5. Add exact device readiness observation.

Exit criteria: a fresh process can accurately describe an existing runtime without relying on previous memory.

### Phase 3: operation coordinator

1. Add background operations and phase transitions.
2. Move dashboard start/stop/restart handlers onto operations.
3. Add progress/status API and reconnect-safe UI.
4. Remove the request-wide 30-second orchestration context.

Exit criteria: closing the page cannot interrupt a lifecycle transition.

### Phase 4: Compose lifecycle correction

1. Generate `restart: "no"`.
2. Add explicit project identity and labels.
3. Implement legacy restart-policy migration.
4. Start runner, wait readiness, then resolve network and register.
5. Make stop perform verified teardown.

Exit criteria: Docker or host reboot starts nothing automatically, and container success requires readiness.

### Phase 5: Android recovery

1. Pass configured serial to entrypoint/container.
2. Use exact serial preflight and readiness.
3. Add disconnect/reconnect transition monitoring.
4. Verify in-place recovery before adding any automatic runner restart.

Exit criteria: the reported stop/disconnect/reconnect/start scenario passes without dashboard restart.

### Phase 6: host runner ownership

1. Add child reaping.
2. Add identity-based adoption.
3. Keep safe process metadata as evidence, not authority.
4. Verify graceful stop and forced-stop escalation.

Exit criteria: a crashed detached runner never blocks a subsequent start, and unrelated listeners are never adopted or killed.

### Phase 7: single-instance CLI

1. Add controller lock and identity metadata.
2. Make the plain command idempotent.
3. Add dashboard/runtime/status/log commands.
4. Remove `stop-server` after the controller-backed runtime command is available.
5. Add deterministic exit codes and JSON status.

Exit criteria: repeated SSH invocation is safe and useful without manual PID discovery.

### Phase 8: cleanup

1. Remove compatibility status mutation from the old manager.
2. Delete dead lifecycle paths and duplicate registration logic.
3. Consolidate dashboard and CLI start flows onto the same controller service.
4. Update README operational instructions.
5. Run full test, race, lint, design lint, and integration suites.

Exit criteria: one lifecycle implementation owns every entrypoint.

## 20. Planned file-level changes

### Existing files

`cmd/runner.go`

- change plain command to ensure/discover controller;
- preserve version and help behavior.

`cmd/dashboard.go`

- split foreground dashboard serving from ensure behavior;
- acquire controller instance lock;
- start configured runtime asynchronously only on a newly started dashboard;
- adopt healthy existing runtime without restart;
- initialize lifecycle logger.

`cmd/stop_server.go`

- deprecate in favor of `runtime stop`;
- route through controller when present.

`internal/dashboard/server.go`

- inject controller application service;
- replace synchronous actions with operation creation;
- expose progress and lifecycle diagnostics;
- remove duplicate startup/registration sequencing.

`internal/dashboard/hub.go`

- consume controller observations;
- broadcast phase changes and stale observation state;
- stop using manager booleans for host runner truth.

`internal/dashboard/probes.go`

- retain display-focused probing;
- reuse driver observations instead of maintaining a second Compose parser;
- keep CPU/battery collection best effort and separate from lifecycle health.

`internal/dashboard/runtime/lifecycle.go`

- reduce to compatibility adapter during migration;
- delete after all callers use controller and drivers.

`internal/dashboard/runtime/compose.go`

- generate disabled restart policies, explicit labels, health checks, exact device selection, and stable project configuration.

`scripts/entrypoint.sh`

- accept exact serial;
- wait for exact device;
- emit concise machine-recognizable startup errors;
- retain human-readable stderr.

`cmd/server.go`

- expose runner readiness and identity;
- keep pause/resume/heartbeat;
- ensure shutdown records enough lifecycle evidence.

`pkg/server/health.go`

- distinguish liveness, readiness, and device status;
- preserve public API compatibility or version the new endpoint.

`README.md`

- document plain command behavior;
- document no boot startup;
- document explicit lifecycle commands;
- document lifecycle log/export;
- recommend `DASHBOARD_TOKEN` when `0.0.0.0` is reachable by untrusted clients.

`Taskfile.yml`

- add repository-approved formatting task after the Phase 0 human decision;
- add controller unit and lifecycle integration tasks.

### New command files

```text
cmd/status.go
cmd/dashboard_commands.go
cmd/runtime_commands.go
cmd/lifecycle_log.go
cmd/stop.go
```

Avoid adding one file per trivial subcommand if the result becomes fragmented; group commands by lifecycle domain.

## 21. Error model

Use typed errors internally.

```go
type LifecycleError struct {
    Code        string
    OperationID string
    Phase       RuntimePhase
    Component   string
    Retryable   bool
    Message     string
    Hint        string
    Cause       error
}
```

Stable initial codes:

```text
controller_already_running
dashboard_port_conflict
operation_conflict
operation_cancelled
config_invalid
docker_unavailable
compose_failed
runner_port_conflict
runner_identity_mismatch
runner_not_ready
device_missing
device_offline
device_unauthorized
device_mismatch
tunnel_not_ready
tunnel_url_stale
registration_failed
shutdown_timeout
```

Errors shown in UI, CLI, HTTP, and lifecycle logs must originate from the same typed value so their operation ID and phase agree.

## 22. Observability and lifecycle-log separation

OpenTelemetry, application logs, runner logs, and lifecycle logs serve different purposes.

- OpenTelemetry: distributed operational telemetry.
- runner logs: detailed worker, HTTP, Maestro, and activity output.
- Docker logs: service stdout/stderr.
- lifecycle log: bounded local control-plane incident timeline.

Do not replace existing observability with the lifecycle log. Do not route all existing logs into it.

## 23. Compatibility and migration policy

1. Existing `.env` files remain valid.
2. Existing dashboard routes remain temporarily available.
3. Existing quick, named, and manual service modes remain supported.
4. Existing host/container selection remains supported.
5. `stop-server` is removed once the controller-backed runtime command is available.
6. Existing running containers are adopted before being recreated.
7. Existing volumes are never removed by migration.
8. Existing configuration secrets are never rewritten into lifecycle metadata.
9. A failed migration leaves the old runtime untouched and records a recoverable error.

## 24. Definition of done

The refactor is complete only when all of these statements are true.

- Running `credimi-runner` twice produces one dashboard process and no runtime restart.
- Running `credimi-runner` after a manual dashboard stop starts the dashboard and starts or adopts the configured runtime.
- Rebooting the host starts no Credimi Runner process or container.
- A newly started controller can accurately observe and adopt an existing runtime.
- Start success means the exact device, runner API, tunnel endpoint, and registration are all verified.
- Stop success means managed runtime services and listeners are actually absent.
- Dashboard/browser disconnection cannot cancel a lifecycle operation.
- A disconnected and reconnected configured device recovers without killing the dashboard.
- A crashed host runner can be started again.
- A stale quick-tunnel URL cannot be registered.
- Foreign listeners and containers are never adopted or killed.
- Lifecycle logs remain bounded, readable, structurally valid, and secret-free.
- An anonymized lifecycle export is useful as a standalone LLM incident report.
- All unit, race, integration, lint, design-lint, and formatting checks pass.
- Repository changes are delivered as small Conventional Commits containing the required `reason` and `prompt` sections.

## 25. Recommended first implementation slice

Do not begin by moving all lifecycle code. Start with one vertical slice:

1. lifecycle JSONL logger with redaction and rotation;
2. real observed state for the current Compose runtime;
3. background runtime-start operation;
4. exact Android serial preflight;
5. runner readiness wait;
6. current-session tunnel URL discovery;
7. final Credimi registration;
8. regression test for stop/disconnect/reconnect/start.

This slice addresses the reported operational failure while establishing the controller abstractions needed by later CLI, host-runner, and migration work.
