#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
entrypoint="${repo_root}/scripts/entrypoint.sh"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

calls_file="${tmp_dir}/adb.calls"
adb_stub="${tmp_dir}/adb"
killall_stub="${tmp_dir}/killall"

cat > "${adb_stub}" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
echo "adb $*" >> "${ADB_CALLS_FILE}"
case "${1:-}" in
  start-server) exit 0 ;;
  kill-server) exit 0 ;;
  connect) exit 0 ;;
  wait-for-device) exit 0 ;;
  devices) exit 0 ;;
  shell)
    # emulate: adb shell getprop sys.boot_completed -> "1"
    if [[ "${2:-}" == "getprop" && "${3:-}" == "sys.boot_completed" ]]; then
      echo "1"
      exit 0
    fi
    exit 0
    ;;
  *) exit 0 ;;
esac
EOF
chmod +x "${adb_stub}"

cat > "${killall_stub}" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
echo "killall $*" >> "${ADB_CALLS_FILE}"
exit 0
EOF
chmod +x "${killall_stub}"

export PATH="${tmp_dir}:${PATH}"
export ADB_CALLS_FILE="${calls_file}"

find_in_calls() {
  if command -v rg >/dev/null 2>&1; then
    rg -q "$1" "${calls_file}"
  else
    grep -q "$1" "${calls_file}"
  fi
}

reset_calls() { : > "${calls_file}"; }

run_ok() { "${entrypoint}" "$@" >/dev/null 2>&1; }

run_fail() {
  if "${entrypoint}" "$@" >/dev/null 2>&1; then
    echo "Expected failure but command succeeded: $*" >&2
    exit 1
  fi
}

# For tests that would exec credimi-runner, stub it so we don't start anything real.
credimi_stub="${tmp_dir}/credimi-runner"
cat > "${credimi_stub}" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
echo "credimi-runner $*" >> "${ADB_CALLS_FILE}"
exit 0
EOF
chmod +x "${credimi_stub}"

echo "Testing: --help"
reset_calls
run_ok --help

echo "Testing: --no-device starts service without adb usage"
reset_calls
run_ok --no-device
if find_in_calls "adb "; then
  echo "FAIL: adb should not be called in --no-device mode" >&2
  exit 1
fi
if find_in_calls "credimi-runner serve"; then
  echo "PASS: credimi-runner started in --no-device mode"
else
  echo "FAIL: credimi-runner not started in --no-device mode" >&2
  exit 1
fi

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

echo "Testing: Wi-Fi mode connects"
reset_calls
run_ok --no-wait 192.168.1.42:5555
if find_in_calls "adb connect"; then
  echo "PASS: adb connect invoked for Wi-Fi mode"
else
  echo "FAIL: adb connect not invoked for Wi-Fi mode" >&2
  exit 1
fi
if find_in_calls "adb start-server"; then
  echo "PASS: adb start-server invoked in Wi-Fi mode"
else
  echo "FAIL: adb start-server not invoked in Wi-Fi mode" >&2
  exit 1
fi

echo "Testing: --host-adb skips adb start-server"
reset_calls
run_ok --host-adb --usb --no-wait
if find_in_calls "adb start-server"; then
  echo "FAIL: adb start-server should not be called in host ADB mode" >&2
  exit 1
fi

echo "Testing: key materialization is ordered before emulator adb start-server"
materialize_call_line="$(
  rg -n "^materialize_adb_keys_from_env$" "${entrypoint}" | cut -d: -f1
)"
emulator_adb_start_line="$(
  rg -n "^    adb start-server$" "${entrypoint}" | head -n1 | cut -d: -f1
)"
if [[ -z "${materialize_call_line}" || -z "${emulator_adb_start_line}" ]]; then
  echo "FAIL: unable to locate key materialization or emulator adb start lines" >&2
  exit 1
fi
if (( materialize_call_line < emulator_adb_start_line )); then
  echo "PASS: key materialization is ordered before emulator adb start-server"
else
  echo "FAIL: key materialization must happen before emulator adb start-server" >&2
  exit 1
fi

echo "Testing: emulator asset checks are ordered before emulator adb start-server"
asset_check_call_line="$(
  rg -n "^  ensure_emulator_assets$" "${entrypoint}" | cut -d: -f1
)"
if [[ -z "${asset_check_call_line}" ]]; then
  echo "FAIL: unable to locate ensure_emulator_assets call in entrypoint" >&2
  exit 1
fi
if (( asset_check_call_line < emulator_adb_start_line )); then
  echo "PASS: emulator asset checks are ordered before emulator adb start-server"
else
  echo "FAIL: emulator asset checks must happen before emulator adb start-server" >&2
  exit 1
fi

echo "Testing: --emulator requires no args and runs cleanup + adb start-server"
reset_calls
# Make /dev/kvm checks pass even on systems without it:
# If your script strictly checks /dev/kvm existence, this test must run in CI where /dev/kvm exists.
# Alternative: have the script allow SKIP_KVM_CHECK=1 in tests.
if [[ -e /dev/kvm ]]; then
  test_avd_home="${tmp_dir}/avd-home"
  test_golden_root="${tmp_dir}/avd-golden"
  test_golden_path="${test_golden_root}/credimi-golden"
  mkdir -p "${test_avd_home}/credimi.avd" "${test_golden_path}"
  : > "${test_avd_home}/credimi.ini"

  export ANDROID_AVD_HOME="${test_avd_home}"
  export AVDCTL_GOLDEN_DIR="${test_golden_root}"
  export BASE_NAME="credimi"
  export GOLDEN_PATH="${test_golden_path}"

  run_ok --emulator
  if find_in_calls "killall -9 qemu-system-x86_64" && find_in_calls "killall -9 emulator"; then
    echo "PASS: emulator cleanup invoked"
  else
    echo "FAIL: emulator cleanup not invoked" >&2
    exit 1
  fi
  if find_in_calls "adb start-server"; then
    echo "PASS: adb start-server invoked in emulator mode"
  else
    echo "FAIL: adb start-server not invoked in emulator mode" >&2
    exit 1
  fi
  if find_in_calls "credimi-runner serve"; then
    echo "PASS: credimi-runner started in emulator mode"
  else
    echo "FAIL: credimi-runner not started in emulator mode" >&2
    exit 1
  fi
else
  echo "SKIP: --emulator test skipped because /dev/kvm is not present on this host."
  echo "      (Run this test in an environment with /dev/kvm, or add SKIP_KVM_CHECK=1 support.)"
fi

echo "All checks passed."
