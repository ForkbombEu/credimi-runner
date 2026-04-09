#!/usr/bin/env bash
set -euo pipefail

print_help() {
  cat <<'USAGE'
Usage:
  phone-connect [--emulator] [--no-device] [--no-wait] [--usb] [--host-adb] [--help|-h] PHONE_IP[:PORT] [PORT]

Modes:
  --emulator          Validate KVM, cleanup emulator leftovers, start adb, then run credimi-runner.
  --no-device         Skip adb checks/connect/wait and start credimi-runner immediately.
  Wi-Fi (default)     adb connect to PHONE_IP[:PORT]
  --usb               Use USB passthrough (no adb connect)
  --host-adb          Do not start adb server; use host adb via ADB_SERVER_SOCKET

Options:
  --no-wait           Exit after attempting the connection (device modes only, no server start)
  -h, --help          Show this help message

Examples:
  phone-connect --no-device
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

materialize_adb_keys_from_env() {
  local adb_dir="/root/.android"
  local wrote_any=false

  if [[ -n "${ADB_PRIVATE_KEY:-}" || -n "${ADB_PUBLIC_KEY:-}" ]]; then
    mkdir -p "$adb_dir"
  fi

  if [[ ! -f "${adb_dir}/adbkey" && -n "${ADB_PRIVATE_KEY:-}" ]]; then
    printf "%s\n" "$ADB_PRIVATE_KEY" > "${adb_dir}/adbkey"
    chmod 600 "${adb_dir}/adbkey"
    wrote_any=true
  fi

  if [[ ! -f "${adb_dir}/adbkey.pub" && -n "${ADB_PUBLIC_KEY:-}" ]]; then
    printf "%s\n" "$ADB_PUBLIC_KEY" > "${adb_dir}/adbkey.pub"
    chmod 644 "${adb_dir}/adbkey.pub"
    wrote_any=true
  fi

  if [[ "$wrote_any" == true ]]; then
    echo "Loaded ADB key material from environment variables."
  fi
}

ensure_workflows_dir() {
  local workflows_dir="${CREDIMI_DIR:-/credimi}"
  mkdir -p "$workflows_dir/workflows"
}

resolve_golden_path() {
  local golden_root="$1"
  local configured_golden_path="$2"

  if [[ -d "$configured_golden_path" ]]; then
    printf '%s\n' "$configured_golden_path"
    return 0
  fi

  # Some deployments bind the extracted golden directory itself to /avd-golden
  # instead of binding its parent and keeping the default nested path.
  if [[ "$configured_golden_path" != "$golden_root" ]] && [[ -n "$(find "$golden_root" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ]]; then
    echo "Golden assets not found at ${configured_golden_path}; using ${golden_root} because the mount already points at the golden directory." >&2
    printf '%s\n' "$golden_root"
    return 0
  fi

  printf '%s\n' "$configured_golden_path"
}

ensure_emulator_assets() {
  local avd_home="${ANDROID_AVD_HOME:-/avd-home}"
  local golden_root="${AVDCTL_GOLDEN_DIR:-/avd-golden}"
  local base_name="${BASE_NAME:-credimi}"
  local configured_golden_path="${GOLDEN_PATH:-${golden_root}/${base_name}-golden}"
  local golden_path="${configured_golden_path}"
  local base_avd_dir="${avd_home}/${base_name}.avd"
  local base_ini="${avd_home}/${base_name}.ini"

  mkdir -p "$avd_home" "$golden_root"
  golden_path="$(resolve_golden_path "$golden_root" "$configured_golden_path")"
  export GOLDEN_PATH="$golden_path"

  if [[ ! -d "$base_avd_dir" || ! -f "$base_ini" ]]; then
    echo "ERROR: base AVD assets are missing at ${base_avd_dir} and ${base_ini}. Mount preloaded assets into ${avd_home}." >&2
  else
    echo "Base AVD assets already present at ${base_avd_dir}."
  fi

  if [[ ! -d "$golden_path" ]]; then
    echo "ERROR: golden assets are missing at ${configured_golden_path}. Mount preloaded assets into ${golden_root}, or set GOLDEN_PATH=${golden_root} if the bind already points at the extracted golden directory." >&2
  else
    echo "Golden assets already present at ${golden_path}."
  fi

  if [[ ! -d "$base_avd_dir" || ! -f "$base_ini" ]]; then
    echo "ERROR: base AVD assets are missing after setup (${base_avd_dir}, ${base_ini})." >&2
    exit 1
  fi

  if [[ ! -d "$golden_path" ]]; then
    echo "ERROR: golden assets are missing after setup (${configured_golden_path})." >&2
    exit 1
  fi
}

# Flags
no_wait=false
usb_mode=false
host_adb=false
emulator_mode=false
no_device=false
service_port="${PORT:-8050}"

if ! [[ "$service_port" =~ ^[0-9]+$ ]]; then
  echo "Error: PORT must be a number. Got: $service_port" >&2
  exit 1
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
    --no-device)
      no_device=true
      shift
      ;;
    *)
      break
      ;;
  esac
done

materialize_adb_keys_from_env
ensure_workflows_dir

# Emulator mode: no PHONE args, just validate + start adb + run service
if [[ "$emulator_mode" == true ]]; then
  if [[ "$no_device" == true ]]; then
    echo "Error: --no-device cannot be combined with --emulator." >&2
    exit 1
  fi
  if [[ $# -gt 0 ]]; then
    echo "Error: --emulator does not accept PHONE_IP/PORT arguments." >&2
    exit 1
  fi

  # Prefer mounted adb keys when present; otherwise fall back to no-key mode.
  if [[ -z "${ADB_VENDOR_KEYS:-}" ]]; then
    if [[ -f /root/.android/adbkey ]]; then
      export ADB_VENDOR_KEYS=/root/.android
    else
      export ADB_VENDOR_KEYS=/dev/null
      echo "Warning: no adb private key found; ADB auth keys are disabled (ADB_VENDOR_KEYS=/dev/null)." >&2
    fi
  fi

  need_kvm
  ensure_emulator_assets
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
  exec credimi-runner serve --host 0.0.0.0 --port "$service_port"
fi

# Device modes (wifi/usb/host-adb)
if [[ "$no_device" == true ]]; then
  if [[ "$usb_mode" == true || "$host_adb" == true ]]; then
    echo "Error: --no-device cannot be combined with --usb or --host-adb." >&2
    exit 1
  fi
  if [[ $# -gt 0 ]]; then
    echo "Error: --no-device does not accept PHONE_IP/PORT arguments." >&2
    exit 1
  fi

  echo "No-device mode enabled. Skipping adb startup/connection/wait."
  echo "Starting credimi-runner..."
  exec credimi-runner serve --host 0.0.0.0 --port "$service_port"
fi

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
exec credimi-runner serve --host 0.0.0.0 --port "$service_port"
