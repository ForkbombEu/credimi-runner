#!/usr/bin/env bash
set -euo pipefail

print_help() {
  cat <<'USAGE'
Usage:
  phone-connect [--emulator] [--no-wait] [--usb] [--host-adb] [--help|-h] PHONE_IP[:PORT] [PORT]

Modes:
  --emulator          Validate KVM, cleanup emulator leftovers, start adb, then run credimi-runner.
  Wi-Fi (default)     adb connect to PHONE_IP[:PORT]
  --usb               Use USB passthrough (no adb connect)
  --host-adb          Do not start adb server; use host adb via ADB_SERVER_SOCKET

Options:
  --no-wait           Exit after attempting the connection (device modes only)
  -h, --help          Show this help message

Examples:
  phone-connect 192.168.1.42
  phone-connect 192.168.1.42 5555
  phone-connect 192.168.1.42:5555
  phone-connect --usb
  phone-connect --host-adb --usb
  phone-connect --emulator
USAGE
}

need_kvm() {
  if [ ! -e /dev/kvm ]; then
    echo "ERROR: /dev/kvm not found. Run container with: --device /dev/kvm" >&2
    exit 1
  fi
  if [ ! -r /dev/kvm ] || [ ! -w /dev/kvm ]; then
    echo "ERROR: /dev/kvm is not readable/writable." >&2
    echo "Fix on host: sudo chmod 666 /dev/kvm  (or add user to kvm group)" >&2
    exit 1
  fi
}

cleanup_emulator_leftovers() {
  echo "Cleaning up existing emulator processes..."
  killall -9 qemu-system-x86_64 2>/dev/null || true
  killall -9 emulator 2>/dev/null || true
  adb kill-server 2>/dev/null || true
  sleep 2
}

# Flags
no_wait=false
usb_mode=false
host_adb=false
emulator_mode=false

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
    --emulator)
      emulator_mode=true
      shift
      ;;
    *)
      break
      ;;
  esac
done

# Emulator mode: no PHONE args, just validate + start adb + run service
if [[ "$emulator_mode" == true ]]; then
  if [[ $# -gt 0 ]]; then
    echo "Error: --emulator does not accept PHONE_IP/PORT arguments." >&2
    exit 1
  fi

  need_kvm
  cleanup_emulator_leftovers

  if [[ "$host_adb" == true ]]; then
    if [[ -z "${ADB_SERVER_SOCKET:-}" ]]; then
      echo "Warning: --host-adb is set but ADB_SERVER_SOCKET is not. The client may still use the container server." >&2
    fi
    echo "Host ADB mode enabled. Skipping adb start-server."
  else
    echo "Starting ADB server..."
    adb start-server
  fi

  echo "Connected devices:"
  adb devices -l || true

  echo "✅ Emulator prerequisites OK."
  echo "Starting credimi-runner..."
  exec credimi-runner serve --host 0.0.0.0 --port 8050
fi

# Device modes (wifi/usb/host-adb)
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

max_wait_seconds=30
elapsed_seconds=0

echo "Waiting for an adb device to be listed (timeout: ${max_wait_seconds}s)..."
while true; do
  if adb devices | awk 'NR>1 && $2=="device" {found=1} END {exit !found}'; then
    break
  fi
  sleep 1
  elapsed_seconds=$((elapsed_seconds + 1))
  if [[ "$elapsed_seconds" -ge "$max_wait_seconds" ]]; then
    echo -e "\033[1;31m" >&2
    echo "====================================================================" >&2
    echo "   ❌  NO ADB DEVICE DETECTED AFTER ${max_wait_seconds}s" >&2
    echo "--------------------------------------------------------------------" >&2
    echo "   📱  Please attach a phone or enable Wireless/USB debugging." >&2
    echo "   🔌  If using USB, check cable + permissions." >&2
    echo "   📶  If using Wi-Fi, verify IP:PORT and run adb pair/connect." >&2
    echo "--------------------------------------------------------------------" >&2
    echo "   Tip: run 'adb devices -l' to confirm detection." >&2
    echo "====================================================================" >&2
    echo -e "\033[0m" >&2
    exit 1
  fi
done

echo "Starting credimi-runner..."
exec credimi-runner serve --host 0.0.0.0 --port 8050
