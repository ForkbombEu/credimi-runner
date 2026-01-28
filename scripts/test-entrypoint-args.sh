#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
entrypoint="${repo_root}/entrypoint.sh"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

calls_file="${tmp_dir}/adb.calls"
adb_stub="${tmp_dir}/adb"

cat > "${adb_stub}" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
echo "adb $*" >> "${ADB_CALLS_FILE}"
case "${1:-}" in
  start-server) exit 0 ;;
  connect) exit 0 ;;
  devices|-l) exit 0 ;;
  *) exit 0 ;;
esac
EOF
chmod +x "${adb_stub}"

export PATH="${tmp_dir}:${PATH}"
export ADB_CALLS_FILE="${calls_file}"

find_in_calls() {
  if command -v rg >/dev/null 2>&1; then
    rg -q "$1" "${calls_file}"
  else
    grep -q "$1" "${calls_file}"
  fi
}

reset_calls() {
  : > "${calls_file}"
}

run_ok() {
  "${entrypoint}" "$@" >/dev/null 2>&1
}

run_fail() {
  if "${entrypoint}" "$@" >/dev/null 2>&1; then
    echo "Expected failure but command succeeded: $*" >&2
    exit 1
  fi
}

echo "Testing: --help"
reset_calls
run_ok --help

echo "Testing: --usb without args"
reset_calls
run_ok --usb --no-wait
if find_in_calls "adb connect"; then
  echo "FAIL: adb connect should not be called in USB mode" >&2
  exit 1
fi
if find_in_calls "adb start-server"; then
  echo "PASS: adb start-server invoked in USB mode"
else
  echo "FAIL: adb start-server not invoked in USB mode" >&2
  exit 1
fi

echo "Testing: --usb rejects IP args"
reset_calls
run_fail --usb 127.0.0.1:5555

echo "Testing: Wi-Fi requires IP"
reset_calls
run_ok --no-wait 192.168.1.42:5555

if find_in_calls "adb connect"; then
  echo "PASS: adb connect invoked for Wi-Fi mode"
else
  echo "FAIL: adb connect not invoked for Wi-Fi mode" >&2
  exit 1
fi

if find_in_calls "adb connect" && find_in_calls "adb start-server"; then
  echo "PASS: adb start-server invoked"
fi

echo "Testing: --host-adb skips adb start-server"
reset_calls
run_ok --host-adb --usb --no-wait
if find_in_calls "adb start-server"; then
  echo "FAIL: adb start-server should not be called in host ADB mode" >&2
  exit 1
fi

echo "All checks passed."
