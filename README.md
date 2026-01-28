# credimi-runner

A tiny Docker image that bundles Android platform-tools (adb/fastboot) and Maestro,
with a simple entrypoint that connects to a physical Android phone over Wi-Fi ADB.
Use it to run Maestro tests or adb commands from inside the container.

## Quickstart (first-time users)

### 1) Phone setup (enable Wi‑Fi debugging and find IP)

These steps vary slightly by manufacturer, but the flow is similar on most Android 11+ devices.

1. Open **Settings** → **About phone**.
2. Tap **Build number** 7 times until you see “You are now a developer.”
3. Go back to **Settings** and open **System** → **Developer options**.
4. Turn on **Developer options** (if there’s a master toggle).
5. Turn on **USB debugging** (required for USB/cable workflows; separate from Wi‑Fi debugging).
6. Find **Wireless debugging** and enable it.
7. Tap **Wireless debugging** to open its screen:
   - Your phone’s IP address is shown there (often under **IP address** or **Device IP**).
   - Some phones show **IP:PORT** (for example `192.168.1.42:38349`). You can pass that as-is.
   - Ensure **Wireless debugging** stays enabled while you connect.
7. (Optional) If you see a **Pair device with pairing code** option, that is for `adb pair`
   and is required on many Android 11+ devices before `adb connect` works.

If your device doesn’t show **Wireless debugging**, you can enable **ADB debugging** and
then use “ADB over network” or “ADB over Wi‑Fi” if your OEM provides it. The goal is to
have the device listening on a TCP port shown on the phone.

### 2) Run the image (no build required)

Below are the only two commands you need after phone setup.

> [!IMPORTANT]
> **Wi‑Fi (Wireless debugging)**
> ```bash
> # If your phone shows IP:PORT, pass it as a single argument
> docker run --rm -it --network host \
>   -v adbkeys:/root/.android \
>   ghcr.io/ForkbombEu/credimi-runner:latest 192.168.1.42:38349
> ```

> [!IMPORTANT]
> **USB (cable)**
> ```bash
> docker run --rm -it --privileged \
>   -v /dev/bus/usb:/dev/bus/usb \
>   -v adbkeys:/root/.android \
>   ghcr.io/ForkbombEu/credimi-runner:latest --usb
> ```

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
  ghcr.io/ForkbombEu/credimi-runner:latest --no-wait 192.168.1.42:38349
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
  ghcr.io/ForkbombEu/credimi-runner:latest --host-adb --usb
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

- `ghcr.io/ForkbombEu/credimi-runner:latest`
- `ghcr.io/ForkbombEu/credimi-runner:vX.Y.Z`
</details>

<details>
<summary>▶ Contributing / Hacking</summary>

Build locally:

```bash
docker build -t credimi-runner .
```

Run a locally built image:

```bash
docker run --rm -it --network host credimi-runner 192.168.1.42
```

Quick entrypoint argument checks (no device required):

```bash
./scripts/test-entrypoint-args.sh
```
</details>
