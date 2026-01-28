#!/usr/bin/env bash
set -euo pipefail

print_help() {
  cat <<'USAGE'
Usage: phone-connect [--no-wait] [--help|-h] PHONE_IP[:PORT] [PORT]

Connects to an Android device over Wi-Fi ADB, then keeps the container alive
for interactive use unless --no-wait is provided.

Arguments:
  PHONE_IP            Device IP address (required)
  PORT                Optional port (default: 5555)
  PHONE_IP:PORT       IP and port in one argument

Options:
  --no-wait           Exit after attempting the connection
  -h, --help          Show this help message

Examples:
  phone-connect 192.168.1.42
  phone-connect 192.168.1.42 5555
  phone-connect 192.168.1.42:5555
  phone-connect --no-wait 192.168.1.42
USAGE
}

no_wait=false

if [[ $# -eq 0 ]]; then
  print_help
  exit 0
fi

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  print_help
  exit 0
fi

if [[ "${1:-}" == "--no-wait" ]]; then
  no_wait=true
  shift
fi

if [[ $# -eq 0 ]]; then
  print_help
  exit 0
fi

phone_arg="${1:-}"
port_arg="${2:-}"

ip=""
port=""

if [[ "$phone_arg" == *":"* ]]; then
  ip="${phone_arg%%:*}"
  port="${phone_arg##*:}"
else
  ip="$phone_arg"
  port="$port_arg"
fi

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

echo "Starting adb server..."
adb start-server

echo "Connecting to ${target}..."
set +e
connect_output=$(adb connect "${target}" 2>&1)
connect_status=$?
set -e

echo "${connect_output}"

echo "Connected devices:"
adb devices -l || true

if [[ "$no_wait" == true ]]; then
  exit "$connect_status"
fi

if [[ "$connect_status" -ne 0 ]]; then
  echo "Warning: adb connect failed with exit code ${connect_status}." >&2
  echo "Container will remain running for debugging." >&2
fi

echo "Container is now idle. Use 'docker exec -it <container> bash' to interact."
# Keep container alive for interactive use
exec tail -f /dev/null
