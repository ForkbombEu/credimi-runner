# credimi-runner

A tiny Docker image that bundles Android platform-tools (adb/fastboot) and Maestro,
with a simple entrypoint that connects to a physical Android phone over Wi-Fi ADB.
Use it to run Maestro tests or adb commands from inside the container.

## 🚀 Quick start

Install `credimi-runner` with the bootstrap installer:

```bash
curl -sL credimi.run | sh
```

This works on **macOS** (Intel and Apple Silicon) and **Linux** (x86\_64 and arm64).

The installer downloads the latest release binary, makes it executable, and starts
the dashboard immediately:

```bash
credimi-runner
```

The dashboard writes and manages the runtime configuration under
`~/.config/credimi/runner/`. The installer only installs the binary into
`~/.local/bin` by default and launches the dashboard.

See [One-command install](#one-command-install) and [Run API server locally](#run-api-server-locally-serve) for configuration details and alternate workflows.

## One-command install

For a no-clone host install, use the bootstrap script:

```bash
curl -fsSL https://raw.githubusercontent.com/ForkbombEu/credimi-runner/main/install.sh | sh
```

What it does:

- downloads the latest release binary for your OS and CPU
- installs `credimi-runner` into `~/.local/bin` by default
- makes the binary executable
- starts the dashboard, which handles configuration and runtime startup

Once you publish the same script behind your own domain, the flow can be:

```bash
curl -fsSL https://credimi.run | sh
```

## Quickstart (first-time users)

### 1) Phone setup (enable Wi‑Fi debugging and find IP)

These steps vary slightly by manufacturer, but the flow is similar on most Android 11+ devices.

1. Open **Settings** → **About phone**.
1. Tap **Version** -> **Build number** 7 times until you see “You are now a developer.”
1. Go back to **Settings** and open **System** → **Developer options**.
1. Turn on **Developer options** (if there’s a master toggle).
1. Turn on **USB debugging** (required for USB/cable workflows; separate from Wi‑Fi debugging).
1. Find **Wireless debugging** and enable it.
1. Tap **Wireless debugging** to open its screen:
   - Your phone’s IP address is shown there (often under **IP address** or **Device IP**).
   - Some phones show **IP:PORT** (for example `192.168.1.42:38349`). You can pass that as-is.
   - Ensure **Wireless debugging** stays enabled while you connect.
1. Turn on **Developer Options > Stay awake**, if that doesn't work set it using adb:
   - ```adb -s <serial> shell settings put global stay_on_while_plugged_in 3```
1. (Optional) If you see a **Pair device with pairing code** option, that is for `adb pair`
   and is required on many Android 11+ devices before `adb connect` works.

If your device doesn’t show **Wireless debugging**, you can enable **ADB debugging** and
then use “ADB over network” or “ADB over Wi‑Fi” if your OEM provides it. The goal is to
have the device listening on a TCP port shown on the phone.

### 2) Start the service (recommended)

Preferred day-to-day entrypoints:

- `task service:phone` runs the published Android phone runner behind Dockerized Caddy and a quick Cloudflare tunnel.
- `task service:local` runs the local `credimi-runner serve` binary behind Dockerized Caddy and a quick Cloudflare tunnel. This is the preferred path for macOS host runs, including iOS simulator workflows that need host tools such as `xcrun`.

Advanced variants:

- `task -a service:phone:named` for a named Cloudflare tunnel with the published phone image.
- `task -a service:local:named` for a named Cloudflare tunnel with the local host binary.

Low-level direct container examples are still available below for debugging and one-off usage.

### 3) Low-level container runs (optional)

These examples are useful for debugging or direct device access without Caddy/Tunnel.

> [!IMPORTANT]
> **Wi‑Fi (Wireless debugging)**
> ```bash
> # If your phone shows IP:PORT, pass it as a single argument
> docker run --rm -it --network host \
>   -e CREDIMI_URL=https://credimi.io \
>   -e CREDIMI_USER_API_KEY=your-user-api-key \
>   -e CREDIMI_RUNNER_ID=/org-id/runner-phone-01 \
>   -v adbkeys:/root/.android \
>   ghcr.io/ForkbombEu/credimi-runner-phone:latest 192.168.1.42:38349
> ```

> [!IMPORTANT]
> **USB (cable)**
> ```bash
> docker run --rm -it --privileged --network host \
>   -e CREDIMI_URL=https://credimi.io \
>   -e CREDIMI_USER_API_KEY=your-user-api-key \
>   -e CREDIMI_RUNNER_ID=/owner-org-id/runner-phone-01 \
>   -v /dev/bus/usb:/dev/bus/usb \
>   -v adbkeys:/root/.android \
>   ghcr.io/ForkbombEu/credimi-runner-phone:latest --usb
> ```

> [!IMPORTANT]
> **Emulator (requires KVM)**
> ```bash
> docker run --rm -it --device /dev/kvm --network host \
>   -e CREDIMI_URL=https://credimi.io \
>   -e CREDIMI_USER_API_KEY=your-user-api-key \
>   -e CREDIMI_RUNNER_ID=/owner-org-id/runner-emulator-01 \
>   -e GOLDEN_PATH=/avd-golden/credimi-golden \
>   -v /srv/credimi/avd-home:/avd-home \
>   -v /srv/credimi/avd-golden:/avd-golden \
>   -v /path/to/.android:/root/.android \
>   ghcr.io/ForkbombEu/credimi-runner-emulator:latest
> ```

Minimum useful environment variables (all modes):
- `CREDIMI_RUNNER_ID`

Authentication options (choose one):
- `CREDIMI_USER_API_KEY`
- `CREDIMI_INTERNAL_ADMIN_KEY`

Defaulted/optional environment variables (all modes):
- `CREDIMI_URL` (default: `https://credimi.io`)
- `CREDIMI_RUNNER_LIFECYCLE_ENABLED` (default: `true`)
- `CREDIMI_RUNNER_HEARTBEAT_INTERVAL` (default: `30s`)
- `CREDIMI_RUNNER_LIFECYCLE_REQUEST_TIMEOUT` (default: `5s`)
- `TEMPORAL_ADDRESS` (default: `temporal.credimi.io:7233`)
- `CREDIMI_INTERNAL_ADMIN_KEY` (added as `Credimi-Api-Key` on internal Credimi API requests)

Emulator-only optional environment variables:
- `BASE_NAME` (default: `credimi`)
- `GOLDEN_PATH` (default: `/avd-golden/<BASE_NAME>-golden`)
- `ADB_PRIVATE_KEY` and `ADB_PUBLIC_KEY` (provide to inject ADB keys; otherwise the container uses mounted keys if present, or disables ADB auth keys)

<details>
<summary>▶ Verify device (optional)</summary>

```bash
docker exec -it <container> adb devices -l
```
</details>

<details>
<summary>▶ Pairing (Android 11+)</summary>

If your phone shows a pairing code, pair once:

```bash
docker exec -it <container> adb pair 192.168.1.42:38645
```

Then connect:

```bash
docker exec -it <container> adb connect 192.168.1.42:38349
```
</details>

<details>
<summary>▶ One-shot example (connect and exit)</summary>

```bash
docker run --rm -it --network host \
  -v adbkeys:/root/.android \
  ghcr.io/ForkbombEu/credimi-runner-phone:latest --no-wait 192.168.1.42:38349
```

`--no-wait` only attempts the ADB connect and then exits. It does not start `credimi-runner serve`.
</details>

<details>
<summary>▶ Start server without waiting for a device</summary>

```bash
docker run --rm -it --network host \
  -e CREDIMI_URL=https://credimi.io \
  -e CREDIMI_USER_API_KEY=your-user-api-key \
  -e CREDIMI_RUNNER_ID=/owner-org-id/runner-phone-01 \
  ghcr.io/ForkbombEu/credimi-runner-phone:latest --no-device
```
</details>

<details>
<summary>▶ USB via host ADB (recommended if host already sees the device)</summary>

If `adb devices -l` works on the host, you can reuse the host's ADB server from the container.
This avoids USB passthrough conflicts and does not require `--privileged`.

On the host:

```bash
adb kill-server
adb start-server
adb devices -l
```

Then run the container:

```bash
docker run --rm -it --network host \
  -e ADB_SERVER_SOCKET=tcp:127.0.0.1:5037 \
  -v adbkeys:/root/.android \
  ghcr.io/ForkbombEu/credimi-runner-phone:latest --host-adb --usb
```

Notes:
- `--host-adb` skips starting a server in the container and uses the host's server.
- If the host ADB server is running, it may lock USB; either use this mode or stop the host server.
</details>

<details>
<summary>▶ Troubleshooting (USB vs Wi‑Fi)</summary>

- USB: run with `--usb` and confirm the device appears in `adb devices -l`.
- USB: if you see `unauthorized`, unlock the phone and accept the RSA prompt.
- USB: avoid `adb connect` or any `IP:PORT` when using a cable.
- USB: if the host `adb` already sees the device, use "USB via host ADB" above or stop the host server.
- Wi‑Fi: use `--network host` on Linux or ensure the container can reach the phone's LAN IP.
- Wi‑Fi: the port shown in Android's Wireless debugging screen is required; do not assume `5555`.
</details>

<details>
<summary>▶ Notes</summary>

- Phone prerequisites: enable Developer options and Wireless debugging / TCP ADB.
- Android 11+ “Wireless debugging pairing” may require `adb pair` and a pairing code.
  This image targets the `adb connect <ip>:<port>` workflow.
- Networking: `--network host` is simplest on Linux. Without it, the container must
  still reach the phone on the LAN (same Wi-Fi, routable subnet, port 5555 reachable).
</details>

<details>
<summary>▶ Troubleshooting</summary>

- Ensure the phone and container host are on the same Wi-Fi/LAN.
- Confirm the device is listening on the port shown in Wireless debugging.
- Try restarting the server: `adb kill-server && adb start-server`.
- Verify the IP address from the phone’s Wireless debugging screen.
- If connect fails, re-enable Wireless debugging or toggle TCP ADB.
</details>

<details>
<summary>▶ Releases and images</summary>

This repo uses semantic-release (Conventional Commits) on every push to `master`.
Each release publishes a Docker image to GitHub Container Registry:

- `ghcr.io/ForkbombEu/credimi-runner-phone:latest`
- `ghcr.io/ForkbombEu/credimi-runner-phone:vX.Y.Z`
- `ghcr.io/ForkbombEu/credimi-runner-emulator:latest`
- `ghcr.io/ForkbombEu/credimi-runner-emulator:vX.Y.Z`
</details>

<details>
<summary>▶ Run API server locally (`serve`)</summary>

Build and run:

```bash
task build
./bin/credimi-runner serve --host 127.0.0.1 --port 8050
```

Or with Task:

```bash
task build
task test
```

Environment variables used by `serve`:

- `CREDIMI_URL` (default: `https://credimi.io`)
- `CREDIMI_RUNNER_ID` (required when workers are started)
- `CREDIMI_USER_API_KEY` for user-scoped workers, or `CREDIMI_INTERNAL_ADMIN_KEY` for admin workers on `CREDIMI_URL`
- `CREDIMI_RUNNER_LIFECYCLE_ENABLED` (default: `true`)
- `CREDIMI_RUNNER_HEARTBEAT_INTERVAL` (default: `30s`)
- `CREDIMI_RUNNER_LIFECYCLE_REQUEST_TIMEOUT` (default: `5s`)
- `TEMPORAL_ADDRESS` (optional, default: `temporal.credimi.io:7233`)
- `CREDIMI_INTERNAL_ADMIN_KEY` (forwarded as `Credimi-Api-Key` on internal Credimi API requests)

Local env loading for `serve`:

- If a `.env` file exists in the current working directory, it is loaded before startup.
- Otherwise `serve` falls back to `$XDG_CONFIG_HOME/credimi/runner/.env`, or `~/.config/credimi/runner/.env` when `XDG_CONFIG_HOME` is unset.
- The runner pushes `resume`, `heartbeat`, and best-effort `pause` lifecycle events to Credimi; local `/health` remains local diagnostics only.
- Heartbeat timeout, shutdown-after timing, and running-run cancellation policy are configured in Credimi, not in the runner.

Runner container envs (phone/emulator):

Minimum useful:
- `CREDIMI_RUNNER_ID`

Authentication options:
- `CREDIMI_USER_API_KEY`
- `CREDIMI_INTERNAL_ADMIN_KEY`

Defaulted/optional:
- `CREDIMI_URL` (default: `https://credimi.io`)
- `CREDIMI_RUNNER_LIFECYCLE_ENABLED` (default: `true`)
- `CREDIMI_RUNNER_HEARTBEAT_INTERVAL` (default: `30s`)
- `CREDIMI_RUNNER_LIFECYCLE_REQUEST_TIMEOUT` (default: `5s`)
- `TEMPORAL_ADDRESS` (default: `temporal.credimi.io:7233`)
- `CREDIMI_INTERNAL_ADMIN_KEY` (forwarded as `Credimi-Api-Key` on internal Credimi API requests)
- `BASE_NAME` (emulator only, default: `credimi`)
- `GOLDEN_PATH` (emulator only, default: `/avd-golden/<BASE_NAME>-golden`)
- `ADB_PRIVATE_KEY` and `ADB_PUBLIC_KEY` (emulator only, provide to inject ADB keys; otherwise the container uses mounted keys if present, or disables ADB auth keys)

Example `.env` for local serve:

```bash
CREDIMI_URL=https://credimi.io
CREDIMI_USER_API_KEY=your-user-api-key
CREDIMI_RUNNER_ID=local-runner
CREDIMI_RUNNER_LIFECYCLE_ENABLED=true
CREDIMI_RUNNER_HEARTBEAT_INTERVAL=30s
CREDIMI_RUNNER_LIFECYCLE_REQUEST_TIMEOUT=5s
TEMPORAL_ADDRESS=temporal.credimi.io:7233
```

Alternative `.env` using the internal admin API key:

```bash
CREDIMI_URL=https://credimi.io
CREDIMI_INTERNAL_ADMIN_KEY=your-internal-admin-api-key
CREDIMI_RUNNER_ID=local-runner
CREDIMI_RUNNER_LIFECYCLE_ENABLED=true
CREDIMI_RUNNER_HEARTBEAT_INTERVAL=30s
CREDIMI_RUNNER_LIFECYCLE_REQUEST_TIMEOUT=5s
TEMPORAL_ADDRESS=temporal.credimi.io:7233
```

### iOS local usage

There is no separate iOS build target in this repo. For iOS, run the same `credimi-runner serve`
binary and call the API with `"platform": "ios"`. The server supports iOS installers in
`/credimi/installer-action` and iOS pipeline uploads in `/credimi/pipeline-result`.

Typical `.env` for a local iOS workflow:

```bash
CREDIMI_URL=https://credimi.io
CREDIMI_RUNNER_ID=local-ios-runner
CREDIMI_INTERNAL_ADMIN_KEY=
TEMPORAL_ADDRESS=temporal.credimi.io:7233

# Choose one auth mode
CREDIMI_USER_API_KEY=
# or
CREDIMI_INTERNAL_ADMIN_KEY=
```

Notes:

- `CREDIMI_USER_API_KEY` is for user-scoped workers; `CREDIMI_INTERNAL_ADMIN_KEY` is for admin workers.
- `BASE_NAME` is only used by the Android emulator flow. It is not required for local iOS usage.
- `CREDIMI_TEMP_DIR` is set in the Docker image, but the local `serve` command does not currently read it.

</details>

<details>
<summary>▶ Expose the API with Cloudflare Tunnel (+ optional custom domain)</summary>

Use `docker-compose.yaml` to run:

- `runner` (`credimi-runner serve` on port `8050`)
- `caddy` (Docker-label reverse proxy)
- `cloudflared` (public tunnel)

Quick tunnel (instant public URL on `trycloudflare.com`):

```bash
task service:phone
```

The tunnel URL is printed in the running output.

Quick tunnel with the local macOS/Linux binary instead of Docker:

```bash
task service:local
```

This starts the local `credimi-runner serve` binary on the host and runs `runner_host`, `caddy`, and `cloudflared` from `docker-compose.yaml`. It is the recommended path for macOS/iOS simulator workflows.

Named tunnel (your own domain):

```bash
# Configure in .env (recommended)
# CLOUDFLARE_TUNNEL_TOKEN=xxxxxxxx
# RUNNER_DOMAIN=api.example.com
# RUNNER_CADDY_SITE=:80
task -a service:phone:named
```

Named tunnel with the local macOS/Linux binary:

```bash
# Configure in .env (recommended)
# CLOUDFLARE_TUNNEL_TOKEN=xxxxxxxx
# RUNNER_DOMAIN=api.example.com
task -a service:local:named
```

Notes:

- `task --list` shows the recommended day-to-day workflows.
- `task -a` shows the advanced variants (named tunnels, local images, build-only tasks).
- For named tunnels, configure the public hostname in Cloudflare to point to `http://caddy:80`.
- `RUNNER_DOMAIN` is used by the OpenAPI docs server URL (Stoplight "Try it").
- For temporary `trycloudflare.com` tunnels, leave `RUNNER_DOMAIN` empty (or unset) so docs use same-origin URLs.
- Keep `RUNNER_CADDY_SITE=:80` when running behind Cloudflare Tunnel.
- `docker compose --profile named up` starts both `tunnel` and `tunnel_named`; prefer explicit services as above.
- The compose file uses `ghcr.io/forkbombeu/credimi-runner-phone:latest` by default.
- To run your local image instead: `task -a service:phone:local:named`.
- The local Taskfile flow uses Dockerized `caddy` and `cloudflared`; `cloudflared` does not need to be installed on the host.
- The local host-binary flow binds `credimi-runner serve` on `RUNNER_HOST` so Docker can reach it. The Taskfile default is `0.0.0.0`.
- Stop everything: `docker compose down --remove-orphans`.

</details>

<details>
<summary>▶ Deploy emulator with Coolify</summary>

Use `docker-compose.coolify.yaml` as the Compose file in Coolify.

This compose file keeps `runner_emulator` under the `emulator` profile.
Set this in Coolify environment variables:

```bash
COMPOSE_PROFILES=emulator
```

This enables the `emulator` profile without passing `--profile emulator` on every command.

</details>

<details>
<summary>▶ Contributing / Hacking</summary>

Build phone image locally:

```bash
task -a build:phone
```

Build emulator locally:

```bash
task -a build:emulator
```

Run the emulator locally with preloaded assets mounted from the host:

```bash
HOST_AVD_HOME_PATH=/srv/credimi/avd-home
HOST_AVD_GOLDEN_PATH=/srv/credimi/avd-golden
GOLDEN_PATH=/avd-golden/credimi-golden
task run:service:emulator
```

`HOST_AVD_HOME_PATH` and `HOST_AVD_GOLDEN_PATH` are host folders. `GOLDEN_PATH` is the
path inside the container and must stay under `/avd-golden`.
If your bind already points at the extracted `credimi-golden` directory itself, use
`GOLDEN_PATH=/avd-golden` instead of the nested default.



Run a locally built image:

```bash
docker run --rm -it --network host credimi-runner-phone 192.168.1.42
```

Quick entrypoint argument checks (no device required):

```bash
./scripts/test-entrypoint-args.sh
```
</details>
