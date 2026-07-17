# Credimi Runner Lifecycle Refactor — Current Execution Plan

Status: active implementation checklist
Branch: `feat/lifecycle-controller`
Last reconciled: 2026-07-17

This document replaces the earlier aspirational plan. It distinguishes code
already delivered from work that is still required before the lifecycle can be
called robust for a remotely managed runner host.

## Product decisions that remain fixed

- `DASHBOARD_HOST=0.0.0.0` remains the default bind address.
- Nothing starts automatically on host boot.
- Docker-managed Credimi services must not restart on Docker/host restart.
- Plain `credimi-runner` is the normal operator command.
- A second plain invocation verifies and reopens the existing dashboard; it
  must not restart the runtime.
- `stop-server` is removed. `credimi-runner runtime stop` is the only
  runtime-stop command.
- Lifecycle diagnostics are bounded, local, secret-safe JSONL rather than a
  copy of runner/Docker logs.
- Every dashboard visual change follows `DESIGN.md`.

## Completed work

The following items are implemented, tested, and committed.

### Controller ownership and dashboard process

- [x] Advisory single-instance lock in the config directory.
- [x] Atomic `controller.json` metadata with restrictive permissions.
- [x] Per-boot controller identity token and config fingerprint.
- [x] Authenticated local controller identity endpoint.
- [x] Second plain invocation verifies the controller identity before reporting
  its URL; it does not mutate the runtime.
- [x] Dashboard PID stop verifies the controller identity first.
- [x] Browser opening is skipped when no graphical display is available.
- [x] Wildcard dashboard binding is displayed using the server hostname rather
  than `127.0.0.1`.

### Background lifecycle operations

- [x] Serialized background operation coordinator with conflict detection,
  operation snapshots, polling, waiting, and cancellation.
- [x] Dashboard runtime actions are queued; they no longer use the initiating
  request context or a request-wide 30-second timeout.
- [x] Plain-command automatic startup shares the same coordinator as dashboard
  actions.
- [x] Controller API supports status, operation lookup, cancellation, and
  queued runtime start/stop/restart/down.
- [x] Dashboard copy no longer tells users to keep the page open.

### Docker/runtime safety

- [x] Generated and repository Compose files use `restart: "no"`.
- [x] Docker commands use a stable explicit Compose project name derived from
  user ID and canonical config directory.
- [x] Generated services carry managed-resource labels.
- [x] Runtime status performs production read-only Compose observation after a
  dashboard restart instead of trusting only prior in-memory booleans.
- [x] `runtime stop` performs `compose down --remove-orphans`, preserves
  volumes, then verifies no managed container remains.
- [x] Host stop verifies the configured runner listener is no longer reachable.

### Android readiness and startup ordering

- [x] Generated runner configuration passes `ANDROID_SERIAL`.
- [x] Entrypoint accepts `--serial` and waits for the exact serial rather than
  any ADB device.
- [x] Startup registration waits for runner `/health` and the configured serial
  in state `device` when a serial is configured.
- [x] Container readiness is required before registration in reachable modes.

### Lifecycle diagnostics and CLI

- [x] Private bounded `lifecycle.jsonl` with rotation and basic URL/secret
  sanitization.
- [x] Controller and runtime start/stop events are recorded.
- [x] `lifecycle-log path`, `tail`, and Markdown `export` commands exist.
- [x] `status`, `dashboard status`, `dashboard open`, `dashboard stop`,
  `runtime status`, `runtime start|stop|restart|down`, and
  `runtime cancel <operation-id>` exist.
- [x] `stop-server` and its tests are deleted.

### Validation already performed

- [x] Unit tests for lock metadata/identity, coordinator serialization,
  lifecycle logs, Compose generation, exact serial entrypoint behavior,
  readiness, observed Compose status, and verified stop.
- [x] `mise exec -- task lint` passes.
- [x] `COVERAGE_MIN=86 mise exec -- task test` passes.

## Remaining implementation plan

The order below is intentional. Do not begin a later item until the acceptance
criteria for its dependencies are met.

### 1. Replace compatibility runtime state with a controller observation model

Goal: one read-only source of truth for dashboard, CLI, start, stop, and safe
adoption.

1. Create `internal/controller/state.go` with `ObservedRuntime`,
   `ObservedService`, `ObservedDevice`, `PublicEndpoint`, and registration
   evidence types.
2. Create `internal/controller/driver` with a small `Driver` interface. Keep
   raw Docker/process commands inside drivers, never in dashboard handlers.
3. Move the Compose JSON parsing currently duplicated in dashboard probes and
   runtime status into the Compose driver.
4. Add a host driver that reports a matching runner process, listener, and
   process identity without mutating anything.
5. Make the dashboard hub consume the latest controller observation and mark it
   stale when older than a bounded threshold.
6. Retain `RuntimeStatus` only as a derived compatibility view until all
   templates and APIs use `ObservedRuntime`; then delete its mutable authority
   fields.

Acceptance tests:

- A fresh controller correctly reports a running managed Compose runtime.
- A fresh controller reports a stopped runtime without mutating Docker.
- A foreign listener/container is reported as foreign and is never adopted.
- Dashboard status visibly identifies stale observations.

### 2. Add a real runner readiness/identity contract

Goal: never adopt an arbitrary TCP listener as Credimi Runner.

1. Extend the runner server with a readiness endpoint separate from `/health`.
2. Return service name, runner ID, boot ID, version, and configured device
   serial/state. Preserve `/health` for liveness/backward compatibility.
3. Generate a boot ID once for each runner process/container startup.
4. Make the controller validate the readiness identity against normalized
   config before treating the runner as healthy or adopted.
5. Add typed errors for listener conflict, runner identity mismatch, runner not
   ready, device missing/offline/unauthorized/mismatch.
6. Use those typed errors in CLI, API, UI, and lifecycle events.

Acceptance tests:

- A listener returning unrelated HTTP is not adopted.
- Correct runner ID plus exact configured serial is healthy.
- Missing, offline, unauthorized, and wrong serial produce distinct errors.

### 3. Make start an idempotent controller transaction

Goal: plain command, dashboard action, and CLI start all make the same safe
decision.

1. Implement `Controller.Start` using: observe → preflight → start runner →
   wait readiness → start network → resolve endpoint → register → observe.
2. If observation is already healthy, return an adopted success without
   restart, pull, recreate, or registration mutation.
3. If only registration is stale, repair registration only.
4. Split timeouts by phase: device 10s, pull 30m, Compose 2m, readiness 2m,
   tunnel 90s, registration 30s, stop 30s.
5. Add a single reporter that feeds operation history, lifecycle log,
   dashboard/SSE state, and `--wait` CLI output.
6. Delete duplicate start/registration logic in `cmd/dashboard.go` and
   `internal/dashboard/server.go` once both delegate to the controller.

Acceptance tests:

- Start is idempotent for a healthy observed runtime.
- Browser/request cancellation does not cancel the operation.
- A registration-only repair does not run Docker start/stop.
- An operation exposes phase progress to a newly connected client.

### 4. Complete Compose ownership, migration, and endpoint discovery

Goal: safely manage only Credimi resources and never use stale quick-tunnel
URLs.

1. Embed literal controller ID/config fingerprint labels in generated Compose
   (not unresolved interpolation defaults).
2. Add reliable runner/Caddy health checks where Compose supports them.
3. On first controller run, discover legacy known service names; record state
   and set their restart policy to `no` without restarting healthy containers.
4. Adopt healthy legacy resources for the current session; only recreate under
   the stable project on a later explicit restart.
5. For quick tunnels, identify the current tunnel container ID and `StartedAt`;
   read only logs from that session and reject URL cache/log entries from older
   sessions.
6. After every start/registration, re-observe and require endpoint/registration
   agreement before operation success.

Acceptance tests:

- Legacy migration never restarts a healthy container or removes volumes.
- A stale quick-tunnel URL is rejected.
- An owned current tunnel URL is registered.
- Foreign labeled/unlabeled containers are not removed.

### 5. Device disconnect and recovery monitoring

Goal: loss of the configured phone degrades readiness but never kills the
dashboard or switches to another device.

1. Add a controller-owned periodic exact-serial observation.
2. Record transitions only: ready, disconnected, offline, unauthorized,
   reconnected; coalesce repeated failures.
3. Store outage start time and log recovery duration.
4. Render configured-device state and degraded readiness in dashboard status.
5. Do not restart the runner automatically; first prove in-place recovery with
   deterministic tests.

Acceptance tests:

- Wrong connected device is rejected.
- Disconnect marks degraded once and leaves dashboard/runtime process alive.
- Reconnection of the same serial recovers readiness without dashboard restart.

### 6. Finish host-runner ownership

Goal: a crashed host child never prevents a later explicit start, and unrelated
processes are never killed.

1. Start host runner in its own process group and record only bounded child
   metadata.
2. Start a reaper goroutine immediately; clear the child handle when it exits
   and log exit code/signal/duration.
3. Adopt host runners only after readiness identity validation, never TCP alone.
4. Stop using PID files as authority; use verified process identity plus the
   readiness contract.
5. Preserve graceful stop then bounded forced termination escalation.

Acceptance tests:

- Crash a fake child; the next start succeeds.
- An unrelated listener on runner port is not stopped.
- Verified host runner adoption performs no restart.

### 7. Complete diagnostics, API, and dashboard presentation

Goal: operators can understand a failure after reconnecting or over SSH.

1. Replace heuristic logger sanitization with an explicit allowed field set.
2. Add event coalescing and periodic summarized image-pull progress.
3. Add `--anonymize` lifecycle export with stable hashes for serials, runner
   IDs, hosts, and public URLs.
4. Add `/api/controller/lifecycle/recent` and `/export`; preserve dashboard
   authentication for normal API routes.
5. Add authenticated controller shutdown endpoint with active-operation policy
   (`wait` or explicit `--force` cancellation), log sync, HTTP/SSE shutdown,
   and lock release.
6. Add dashboard diagnostics: operation phase/elapsed time, device/readiness,
   endpoint/registration, observation age, lifecycle path/recent events/export.
7. Disable conflicting controls while an operation is active and expose cancel.

Acceptance tests:

- Export contains no seeded secrets and anonymizes stable identifiers.
- Identity endpoint rejects bad token; controller API honors dashboard auth.
- Dashboard reconnect shows active operation and latest failure.

### 8. Finish CLI behavior and documentation

Goal: SSH operators have one predictable command surface.

1. Add `dashboard start [--foreground]`, `runtime ... --wait`,
   `runtime logs`, and top-level `stop` (runtime stop, wait, then dashboard
   stop).
2. Add JSON output for status commands and documented exit codes: healthy 0,
   degraded 2, failed 1, conflict 3.
3. Let `runtime start` ensure a controller when absent; retain read-only direct
   observation for `runtime status` when absent.
4. Update README with lifecycle commands, no-boot-start policy, lifecycle log
   export, SSH URL behavior, and `DASHBOARD_TOKEN` advice for exposed hosts.

Acceptance tests:

- Each CLI status mode has deterministic output and exit code.
- Plain command twice yields one controller and no runtime restart.
- Top-level stop leaves runtime and dashboard stopped.

### 9. Integration, race, and final cleanup

Goal: prove the original failure and remove obsolete lifecycle paths.

1. Add an opt-in integration test task using fake ADB, registration, and tunnel
   processes; it must not require a real phone or public Cloudflare.
2. Add the release-blocker scenario:

   ```text
   configured device healthy → runtime stop → device disconnect → same device
   reconnect → runtime start → readiness/current endpoint/registration healthy
   → dashboard process unchanged
   ```

3. Add scenarios for controller restart adoption, foreign port conflict, Docker
   restart policy, stale tunnel URL, host-child crash, operation disconnect,
   and concurrent lifecycle-log rotation.
4. Run unit, integration, race, formatting, design lint, and coverage at 86%.
5. Remove old manager authority, duplicate registration paths, and obsolete
   probe parsers only after replacement tests pass.

Final definition of done:

- Every lifecycle entrypoint delegates to the controller transaction.
- Controller restart accurately observes/adopts an existing healthy runtime.
- Start validates exact device, runner identity, endpoint session, and
  registration before success.
- Stop verifies owned services/listeners are gone.
- No reboot starts Credimi runtime automatically.
- Browser disconnect cannot affect a lifecycle operation.
- All validation, including integration and race tests, passes at >=86% unit
  coverage.
