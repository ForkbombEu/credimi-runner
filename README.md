# Credimi Runner

Credimi Runner hosts a persistent Dashboard and the Credimi runtime for one or
more configured devices. The Dashboard remains available while the runtime is
started, stopped, or reconciled.

## Quick start

Install the latest release binary and start the normal Dashboard flow:

```bash
curl -sL credimi.run | sh
```

The installer detects Linux or macOS and the host architecture, verifies the
release checksum, installs `credimi-runner` in `$XDG_BIN_HOME`,
`$HOME/.local/bin`, or `CREDIMI_RUNNER_BIN_DIR`, and starts the Dashboard. If
the selected directory is not on `PATH`, it prints the exact command to add
it; the installer does not modify shell startup files.

Open the Dashboard at `http://127.0.0.1:8051` unless a different listener was
configured. The setup flow creates the configuration and validates it before
runtime startup.

To install from a checkout instead:

```bash
git clone https://github.com/forkbombeu/credimi-runner.git
cd credimi-runner
task build
install -Dm755 bin/credimi-runner "$HOME/.local/bin/credimi-runner"
credimi-runner
```

## Requirements

- Linux or macOS; x86_64/amd64 and arm64/aarch64 release binaries are
  supported.
- A Credimi account or internal-admin credential, plus access to the configured
  Credimi and Temporal services.
- Docker on Linux when the runner is enabled. The Linux service runs the
  runner in a Docker Compose `runner` container and the operator must be
  allowed to access the Docker daemon.
- macOS Xcode for iOS Simulator devices. macOS Android physical devices use
  Wi-Fi ADB; USB Android devices are not supported by the macOS host flow.
- Android SDK/platform tools for native macOS Android execution. The runner
  resolves host tools without relying on an interactive shell environment;
  Maestro also requires a supported Java installation.
- Linux Android emulators require readable and writable `/dev/kvm`:

  ```bash
  test -r /dev/kvm -a -w /dev/kvm && echo KVM-ready
  ```

## Configuration

The Dashboard setup flow is the recommended configuration path. For a manual
configuration, copy `config.example.toml` to the platform's user configuration
location, keep the directory private, and validate it:

```bash
# Linux; XDG_CONFIG_HOME defaults to ~/.config
config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/credimi/runner"
mkdir -p "$config_dir"
chmod 700 "$config_dir"
install -m 600 config.example.toml "$config_dir/config.toml"
${EDITOR:-vi} "$config_dir/config.toml"
credimi-runner validate-config
```

On macOS, the default file is
`~/Library/Application Support/credimi/runner/config.toml`. On Linux it is
`$XDG_CONFIG_HOME/credimi/runner/config.toml`, or
`~/.config/credimi/runner/config.toml` when `XDG_CONFIG_HOME` is unset. Use
`--config /path/to/config.toml` for another file.

Configuration files must not be symlinks and must not be readable by group or
others. Credentials are read from the TOML file, not runtime environment
variables. The file uses `schema_version = 1`; unknown fields are rejected.

Each device ID is a canonical child of `runner.id`, for example
`example-org/office-runner/pixel-7`. There may be at most one Android emulator
and one iOS Simulator. Android physical and Redroid devices must have unique
serials.

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
Cloudflare token.

## Service and runtime commands

The root command starts the persistent service, waits for the Dashboard, and
follows its logs. Ctrl+C detaches the foreground command without stopping the
service.

```bash
credimi-runner                         # start/attach and follow logs
credimi-runner service start            # start the persistent service
credimi-runner service stop             # stop the persistent service
credimi-runner service restart
credimi-runner service status
credimi-runner service enable           # enable login/startup integration
credimi-runner service disable
credimi-runner logs --follow

credimi-runner runtime start            # start runner API, workers, heartbeat
credimi-runner runtime stop             # stop runtime; keep Dashboard alive
credimi-runner runtime restart
credimi-runner runtime status
credimi-runner dashboard                # open or display Dashboard access
```

On Linux, `service stop` stops the Compose service. Docker may retain the
stopped container in `docker ps -a`; an exited container is not a running
service. Check the actual lifecycle with:

```bash
credimi-runner service status
docker ps
docker ps -a
```

On macOS, the persistent service is a per-user LaunchAgent. `service stop`
stops that LaunchAgent; it does not stop a separately requested runtime
operation unless the service itself is being stopped. Service restart preserves
the runtime desired state and restores it after startup.

The Dashboard control API listens on `0.0.0.0:8051` by default. Set
`server.dashboard_listen` in `config.toml`, or pass
`--dashboard-listen host:port` when starting the CLI service. The execution API
belongs to the active runtime generation, normally on port `8050`.

## Supported devices

| Device type | Linux | macOS | Requirements |
| --- | --- | --- | --- |
| `android_physical` | USB or Wi-Fi ADB | Wi-Fi ADB | Unique serial; host/device ADB access |
| `android_emulator` | Yes | Yes | Android SDK; `/dev/kvm` on Linux; one per runner |
| `redroid` | Yes | Yes | Remote Redroid/AVDCTL and unique ADB serial |
| `ios_simulator` | No | Yes | Xcode, `xcrun simctl`, and an explicit Simulator UDID |

Example macOS Wi-Fi Android device:

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

On Linux, Android emulator assets, SDK/tool caches, AVD data, and ADB keys use
configured persistent storage. Missing Android platform-tools and emulator
packages are installed idempotently by the Credimi activities when required.
On macOS, native activities use the host Android SDK and AVD/ADB state; the
persistent LaunchAgent supplies their tool environment explicitly. Redroid is
remote and ephemeral from the runner's perspective; its SSH/AVDCTL
configuration is validated before use.

## Execution and monitoring

Credimi Runner owns configuration, lifecycle registration, tunnel exposure,
and Temporal worker startup. Credimi activities own device provisioning,
emulator/Simulator lifecycle, ADB, and Maestro execution. One shared runtime
supervisor serves all configured devices; device-specific activity
configuration is scoped to the activity and remains concurrency-safe.

Useful endpoints:

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

Set `server.dashboard_token` to require `X-Dashboard-Token` or
`Authorization: Bearer …` for Dashboard API requests. The runner API exposes
its generated OpenAPI document at `/docs/openapi.yaml`.

The monitoring page reports CPU load, RAM load, disk activity, and free space.
Samples are collected every two seconds, or every half second with
`--debug-verbose`, and are persisted as newline-delimited JSON.

Troubleshooting checks:

```bash
credimi-runner validate-config --config /path/to/config.toml
curl -fsS http://127.0.0.1:8051/healthz
curl -fsS http://127.0.0.1:8051/api/system/metrics
credimi-runner service status
credimi-runner logs --lines 200
```

A healthy runner does not imply every device is ready. Inspect the Dashboard,
Credimi activity logs, and device logs when a device is offline. For a quick
tunnel, use the URL only after tunnel and runner health checks report ready.

## Development

```bash
task format
task lint
task test
task test:race
task build
```
