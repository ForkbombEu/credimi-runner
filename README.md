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
5. Find **Wireless debugging** and enable it.
6. Tap **Wireless debugging** to open its screen:
   - Your phone’s IP address is shown there (often under **IP address** or **Device IP**).
   - Some phones show **IP:PORT** (for example `192.168.1.42:38349`). You can pass that as-is.
   - Ensure **Wireless debugging** stays enabled while you connect.
7. (Optional) If you see a **Pair device with pairing code** option, that is for `adb pair`
   and is required on many Android 11+ devices before `adb connect` works.

If your device doesn’t show **Wireless debugging**, you can enable **ADB debugging** and
then use “ADB over network” or “ADB over Wi‑Fi” if your OEM provides it. The goal is to
have the device listening on a TCP port shown on the phone.

### 2) Run the image (no build required)

```bash
# If your phone shows IP:PORT, pass it as a single argument
docker run --rm -it --network host \
  -v adbkeys:/root/.android \
  ghcr.io/ForkbombEu/credimi-runner:latest 192.168.1.42:38349
```

### 3) Pair (Android 11+)

If your phone shows a pairing code, pair once:

```bash
docker exec -it <container> adb pair 192.168.1.42:38645
```

Then connect:

```bash
docker exec -it <container> adb connect 192.168.1.42:38349
```

### 4) Check that it works (quick healthcheck)

In another terminal:

```bash
docker exec -it <container> bash -lc 'adb devices -l && maestro --version'
```

## One-shot example

Connect and exit after the attempt:

```bash
docker run --rm -it --network host \
  -v adbkeys:/root/.android \
  ghcr.io/ForkbombEu/credimi-runner:latest --no-wait 192.168.1.42:38349
```

## USB (wired) alternative

Yes, you can use USB instead of Wi‑Fi. You’ll need USB debugging enabled on the phone and
to pass the USB device into the container:

```bash
docker run --rm -it --privileged \
  -v /dev/bus/usb:/dev/bus/usb \
  -v adbkeys:/root/.android \
  ghcr.io/ForkbombEu/credimi-runner:latest --no-wait 127.0.0.1:5555
```

Then inside the container:

```bash
adb devices -l
```

Notes:
- If your device asks to authorize the computer’s RSA key, accept it on the phone.
- You can skip `--no-wait` if you want the container to stay open.

## Notes

- Phone prerequisites: enable Developer options and Wireless debugging / TCP ADB.
- Android 11+ “Wireless debugging pairing” may require `adb pair` and a pairing code.
  This image targets the `adb connect <ip>:<port>` workflow.
- Networking: `--network host` is simplest on Linux. Without it, the container must
  still reach the phone on the LAN (same Wi-Fi, routable subnet, port 5555 reachable).

## Troubleshooting

- Ensure the phone and container host are on the same Wi-Fi/LAN.
- Confirm the device is listening on the port shown in Wireless debugging.
- Try restarting the server: `adb kill-server && adb start-server`.
- Verify the IP address from the phone’s Wireless debugging screen.
- If connect fails, re-enable Wireless debugging or toggle TCP ADB.

## Releases and images

This repo uses semantic-release (Conventional Commits) on every push to `master`.
Each release publishes a Docker image to GitHub Container Registry:

- `ghcr.io/ForkbombEu/credimi-runner:latest`
- `ghcr.io/ForkbombEu/credimi-runner:vX.Y.Z`

## Contributing / Hacking

Build locally:

```bash
docker build -t credimi-runner .
```

Run a locally built image:

```bash
docker run --rm -it --network host credimi-runner 192.168.1.42
```
