# Credimi Runner

Credimi Runner is a persistent service for a multi-device Credimi runner. The
service owns the Dashboard and local control API for its entire lifetime;
runtime execution (the runner API, Temporal workers, lifecycle heartbeat and
edge tunnel) is started and stopped independently.

There is no `.env` runtime configuration or per-device runner image. One
Android-capable image is shared by every configured device.

## Install

Build the native binary on Linux or macOS:

```bash
git clone https://github.com/forkbombeu/credimi-runner.git
cd credimi-runner
task build
mkdir -p "$HOME/.local/bin"
install -m 755 bin/credimi-runner "$HOME/.local/bin/credimi-runner"
```

Create the configuration with private permissions. The default location is
`$XDG_CONFIG_HOME/credimi-runner/config.toml`, or
`~/.config/credimi-runner/config.toml` when `XDG_CONFIG_HOME` is unset.

```bash
config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/credimi-runner"
mkdir -p "$config_dir"
chmod 700 "$config_dir"
install -m 600 config.example.toml "$config_dir/config.toml"
${EDITOR:-vi} "$config_dir/config.toml"
credimi-runner validate-config
```

Pass `--config /path/to/config.toml` to use a different location. The process
rejects symlinks and files readable by group or others; credentials never come
from runtime environment variables.

## Service and runtime

The root command starts the service, waits for the Dashboard, and follows its
logs. Pressing Ctrl+C only detaches the log follower; it does not stop the
service.

```bash
credimi-runner
credimi-runner service start
credimi-runner service status
credimi-runner logs --follow

credimi-runner runtime start
credimi-runner runtime stop
credimi-runner runtime restart
credimi-runner runtime status
credimi-runner dashboard
```

On Linux the service is one Docker Compose `runner` container. On macOS it is
a per-user LaunchAgent. Runtime stop keeps the Dashboard alive while closing
the runner API, workers, heartbeat and edge exposure. Service restart preserves
the runtime desired state and lets the application restore it after startup.

The Dashboard control API listens on `127.0.0.1:8051`. The execution API is
owned by the active runtime generation (normally port `8050`). Useful endpoints
include:

```text
GET  /healthz
GET  /api/config
PUT  /api/config
GET  /api/devices
POST /api/devices
PUT  /api/devices/{id}
DELETE /api/devices/{id}
GET  /monitoring
GET  /api/system/metrics
GET  /api/system/metrics?range=hourly
```

Set `server.dashboard_token` to require either `X-Dashboard-Token` or an
`Authorization: Bearer …` header for the dashboard API. The main API remains
the generated GoA API and is available at `/docs/openapi.yaml`.

The Dashboard remains available while runtime operations are in progress and
reports desired versus actual runtime state.

## TOML configuration

`config.example.toml` is the canonical starting point. It has one
`schema_version = 1`, runner identity, Credimi/Temporal connection, listener,
exposure, storage and unified runner-runtime settings, followed by `[[devices]]`
tables. Unknown TOML fields are rejected.

Each device ID is a canonical child of `runner.id`, for example
`example-org/office-runner/pixel-7`. The configuration permits at most one
Android emulator and one iOS Simulator. Android physical devices and Redroid
devices may be repeated only with different serials.

Select exactly one credential:

```toml
[credimi]
url = "https://credimi.example"
auth_mode = "user"
user_api_key = "replace-me"
```

or:

```toml
[credimi]
url = "https://credimi.example"
auth_mode = "internal_admin"
internal_admin_key = "replace-me"
```

Exposure modes are `manual`, `quick_tunnel`, and `named_tunnel`. Manual mode
requires `exposure.public_url`; named tunnels require
`exposure.cloudflare_token`; quick tunnels intentionally do not accept a
Cloudflare token. A quick-tunnel URL is ephemeral and should never be treated
as ready until the tunnel and runner health checks have succeeded.

## Device support

| Device type | Linux | macOS | Requirements |
| --- | --- | --- | --- |
| `android_physical` | USB or Wi-Fi ADB | Wi-Fi ADB only | Docker when enabled; unique serial |
| `android_emulator` | Yes | Yes | Docker and `/dev/kvm`; one per runner |
| `redroid` | Yes | No | Docker; unique serial; one managed Redroid resource per device |
| `ios_simulator` | No | Yes | Xcode and an explicit Simulator UDID; one per runner |

### Android and Docker

Every enabled Android device uses the unified runner image declared by
`android.runner_image`. Persistent state, SDK/tool caches, AVD data and ADB
keys live in configured volumes, so replacing the runner image does not discard
provisioned assets. Missing SDK platform-tools and emulator packages are
installed idempotently into the persistent SDK volume. The runner container contains the dashboard, GoA server,
Temporal workers and common Android tools.

Docker is required only if Android is enabled. An iOS-Simulator-only manual
runner on macOS does not need Docker. On Linux, the user running
`credimi-runner` must be allowed to access the Docker daemon. USB Android
devices use the host ADB connection; grant the operator the normal udev/group
permissions for the attached phone and reconnect it after changing rules.

Android emulator devices require `/dev/kvm` on Linux. Verify it before
enabling an emulator:

```bash
test -r /dev/kvm -a -w /dev/kvm && echo KVM-ready
```

Redroid is Linux-only and ephemeral. Its SSH/avdctl configuration is validated
when configured; no local container is created and no ADB connection is
required while idle. Existing Credimi activities create and clean up the
temporary runtime for each run.

On macOS, physical Android devices must use Wi-Fi ADB, for example:

```toml
[[devices]]
id = "example-org/office-runner/pixel-7"
name = "Pixel 7"
type = "android_physical"
enabled = true

[devices.android_physical]
transport = "wifi"
serial = "192.168.1.42:5555"
```

## Device execution boundary

Credimi Runner owns configuration, lifecycle registration, tunnel exposure and
Temporal worker startup. Device provisioning, emulator/simulator lifecycle,
ADB and Maestro execution are owned by the Credimi-2 activities registered by
the worker manager. The runner only hosts those activities and does not expose
a second operation or agent API.

## Monitoring and troubleshooting

The monitoring page reports live CPU load, RAM load, disk activity and free
space. Samples are collected every two seconds (half a second with
`--debug-verbose`) and persisted as newline-delimited JSON; the hourly view
aggregates the previous 24 hours.

Useful checks:

```bash
credimi-runner validate-config --config /path/to/config.toml
curl -fsS http://127.0.0.1:8051/healthz
curl -fsS http://127.0.0.1:8051/api/system/metrics
credimi-runner service status
credimi-runner logs --lines 200
```

If a device is offline, inspect the Credimi-2 activity and device logs. A
healthy runner does not imply every device is ready. For a quick tunnel, wait
for the newly-created URL to be registered only after readiness succeeds. A
crash or failed reconciliation is surfaced by the dashboard API rather than
silently waiting for a URL.

## Development gates

```bash
task format
task lint
task test
task test:race
task build
```
