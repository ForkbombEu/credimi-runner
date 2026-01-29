#!/usr/bin/env bash
set -euo pipefail

print_help() {
  cat <<'USAGE'
Usage: phone-connect [--no-wait] [--usb] [--host-adb] [--help|-h] PHONE_IP[:PORT] [PORT]

Connects to an Android device over Wi-Fi ADB (default) or uses USB mode, then
keeps the container alive for interactive use unless --no-wait is provided.

Modes:
  Wi-Fi (default)     Use adb connect to PHONE_IP[:PORT]
  USB                 Use --usb with device passthrough (no TCP connect)
  Host ADB            Use --host-adb to talk to a host adb server

Arguments:
  PHONE_IP            Device IP address (required)
  PORT                Optional port (default: 5555)
  PHONE_IP:PORT       IP and port in one argument

Options:
  --no-wait           Exit after attempting the connection
  --usb               Skip adb connect and use USB device passthrough
  --host-adb          Do not start a server; use host adb via ADB_SERVER_SOCKET
  -h, --help          Show this help message

Examples:
  phone-connect 192.168.1.42
  phone-connect 192.168.1.42 5555
  phone-connect 192.168.1.42:5555
  phone-connect --no-wait 192.168.1.42
  phone-connect --usb
  phone-connect --host-adb --usb
USAGE
}

no_wait=false
usb_mode=false
host_adb=false

if [[ $# -eq 0 ]]; then
  print_help
  exit 0
fi

while [[ $# -gt 0 ]]; do
  case "${1:-}" in
    -h|--help)
      print_help
      exit 0
      ;;
    --no-wait)
      no_wait=true
      shift
      ;;
    --usb)
      usb_mode=true
      shift
      ;;
    --host-adb)
      host_adb=true
      shift
      ;;
    *)
      break
      ;;
  esac
done

if [[ "$usb_mode" == true && $# -gt 0 ]]; then
  echo "Error: --usb mode does not accept PHONE_IP/PORT arguments." >&2
  exit 1
fi

if [[ "$usb_mode" == false && $# -eq 0 ]]; then
  print_help
  exit 0
fi

phone_arg="${1:-}"
port_arg="${2:-}"

ip=""
port=""

if [[ "$usb_mode" == false ]]; then
  if [[ "$phone_arg" == *":"* ]]; then
    ip="${phone_arg%%:*}"
    port="${phone_arg##*:}"
  else
    ip="$phone_arg"
    port="$port_arg"
  fi
fi

if [[ "$host_adb" == true ]]; then
  if [[ -z "${ADB_SERVER_SOCKET:-}" ]]; then
    echo "Warning: --host-adb is set but ADB_SERVER_SOCKET is not. The client may still use the container server." >&2
  fi
  echo "Host ADB mode enabled. Skipping adb start-server."
else
  echo "Starting adb server..."
  adb start-server
fi

connect_status=0
if [[ "$usb_mode" == false ]]; then
  if [[ -z "$ip" ]]; then
    echo "Error: PHONE_IP is required." >&2
    print_help
    exit 1
  fi

  if [[ -z "$port" ]]; then
    port="5555"
  fi

  if ! [[ "$port" =~ ^[0-9]+$ ]]; then
    echo "Error: PORT must be a number. Got: $port" >&2
    exit 1
  fi

  target="${ip}:${port}"

  echo "Connecting to ${target}..."
  set +e
  connect_output=$(adb connect "${target}" 2>&1)
  connect_status=$?
  set -e

  echo "${connect_output}"
else
  echo "USB mode enabled. Skipping adb connect."
fi

echo "Connected devices:"
adb devices -l || true

if [[ "$no_wait" == true ]]; then
  exit "$connect_status"
fi

if [[ "$connect_status" -ne 0 ]]; then
  echo "Warning: adb connect failed with exit code ${connect_status}." >&2
  echo "Container will remain running for debugging." >&2
fi

echo "Waiting for an adb device to be listed..."
while true; do
  if adb devices | awk 'NR>1 && $2=="device" {found=1} END {exit !found}'; then
    break
  fi
  sleep 1
done

echo "Starting maestro-worker..."
cd /opt/maestro-worker
exec ./maestro-worker serve
