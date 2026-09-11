# Credimi Runner

Credimi Runner keeps a Dashboard and Credimi runtime available for one or more
configured mobile devices. Start the service once, then use the Dashboard to
configure devices and control runtime execution.

## Quick start

Install the latest release and open the Dashboard:

```bash
curl -sL credimi.run | sh
```

The installer selects the Linux or macOS release for the host architecture,
verifies its checksum, installs `credimi-runner`, and starts the persistent
service. It uses `CREDIMI_RUNNER_BIN_DIR`, `$XDG_BIN_HOME`, or
`$HOME/.local/bin` in that order. If that directory is not on `PATH`, the
installer prints the command to add it; it never changes shell startup files.

Open the Dashboard at `http://127.0.0.1:8051` unless you configured another
listener. Complete setup there, add devices, and start the runtime when the
Dashboard reports the configuration is ready.

## Day-to-day usage

```bash
credimi-runner                         # start/attach and follow service logs
credimi-runner dashboard               # open or print Dashboard access
credimi-runner service status           # inspect persistent service status
credimi-runner runtime start            # start runner API, workers, heartbeat
credimi-runner runtime stop             # stop runtime; keep Dashboard available
credimi-runner runtime status
```

The foreground root command attaches to the persistent service and follows its
logs. `Ctrl+C` detaches that terminal without stopping the service.

Use these commands when service administration is necessary:

```bash
credimi-runner service start
credimi-runner service stop
credimi-runner service restart
credimi-runner service enable           # start automatically at login/startup
credimi-runner service disable
credimi-runner logs --lines 200
```

On Linux, `service stop` stops the Docker Compose service. Docker can retain an
exited container in `docker ps -a`; only a container shown by `docker ps` is
running. Trust `credimi-runner service status` for the runner lifecycle.

On macOS, the persistent service is a per-user LaunchAgent. `service stop`
stops that LaunchAgent. A service restart preserves the runtime's desired state
and restores it after the service returns.

## Requirements and supported devices

- Linux and macOS, on x86_64/amd64 and arm64/aarch64 release architectures.
- A Credimi account or internal-admin credential, with access to the configured
  Credimi and Temporal services.
- Docker access on Linux when the runner is enabled.
- Android SDK platform tools for native Android work. On macOS, persistent
  runner activities resolve supported host tools without requiring an
  interactive-shell `PATH`; Maestro additionally needs a supported Java
  installation.
- Xcode for iOS Simulator devices on macOS.
- Readable and writable `/dev/kvm` for Linux Android emulators:

  ```bash
  test -r /dev/kvm -a -w /dev/kvm && echo KVM-ready
  ```

| Device type | Linux | macOS | Requirements |
| --- | --- | --- | --- |
| `android_physical` | USB or Wi-Fi ADB | Wi-Fi ADB | Unique serial; reachable ADB device |
| `android_emulator` | Yes | Yes | Android SDK; `/dev/kvm` on Linux; one per runner |
| `redroid` | Yes | Yes | Remote Redroid/AVDCTL endpoint and unique ADB serial |
| `ios_simulator` | No | Yes | Xcode, `xcrun simctl`, and an explicit Simulator UDID |

## Configuration

Dashboard setup is the recommended path. It stores the configuration privately
and validates it before runtime startup.

For manual configuration, copy the example into the platform's configuration
directory, make the directory and file private, edit it, then start the runner:

```bash
# Linux; XDG_CONFIG_HOME defaults to ~/.config
config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/credimi/runner"
mkdir -p "$config_dir"
chmod 700 "$config_dir"
install -m 600 config.example.toml "$config_dir/config.toml"
${EDITOR:-vi} "$config_dir/config.toml"
credimi-runner --config "$config_dir/config.toml"
```

The default file is:

- Linux: `$XDG_CONFIG_HOME/credimi/runner/config.toml`, or
  `~/.config/credimi/runner/config.toml` when `XDG_CONFIG_HOME` is unset.
- macOS: `~/Library/Application Support/credimi/runner/config.toml`.

Use `--config-dir /path/to/directory` or `CREDIMI_RUNNER_CONFIG_DIR` for a
different configuration directory. `--config /path/to/config.toml` selects the
parent directory of that `config.toml`. Configuration files must not be symlinks
or readable by group or others. Credentials belong in TOML, not runtime
environment variables. The file uses `schema_version = 1` and rejects unknown
fields.

Each device ID is a canonical child of `runner.id`, for example
`example-org/office-runner/pixel-7`. Configure at most one Android emulator and
one iOS Simulator. Android physical and Redroid devices must have unique serials.

Choose exactly one credential:

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
`exposure.cloudflare_token`; quick tunnels do not accept a Cloudflare token.

## Troubleshooting

```bash
credimi-runner service status
credimi-runner logs --lines 200
curl -fsS http://127.0.0.1:8051/healthz
```

A healthy runner does not guarantee every device is ready. Inspect the
Dashboard, Credimi activity logs, and device logs when a device is offline. For
a quick tunnel, use its URL only after the tunnel and runner health checks are
ready.

## Development

Build from a checkout when developing Credimi Runner itself:

```bash
git clone https://github.com/forkbombeu/credimi-runner.git
cd credimi-runner
task build
task test
```
