#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
install_script="${repo_root}/install.sh"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_file_exists() {
  [[ -f "$1" ]] || fail "missing file: $1"
}

assert_file_absent() {
  [[ ! -e "$1" ]] || fail "unexpected path: $1"
}

assert_executable() {
  [[ -x "$1" ]] || fail "missing executable bit: $1"
}

assert_contains() {
  local needle="$1"
  local path="$2"

  rg -Fq -- "$needle" "$path" || fail "expected '${needle}' in ${path}"
}

assert_not_contains() {
  local needle="$1"
  local path="$2"

  if rg -Fq -- "$needle" "$path"; then
    fail "did not expect '${needle}' in ${path}"
  fi
}

create_mocks() {
  local mock_dir="$1"
  mkdir -p "$mock_dir"

  cat >"${mock_dir}/uname" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
  -s)
    printf '%s\n' "${FAKE_UNAME_S:?}"
    ;;
  -m)
    printf '%s\n' "${FAKE_UNAME_M:?}"
    ;;
  *)
    exec /usr/bin/uname "$@"
    ;;
esac
EOF

  cat >"${mock_dir}/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

: "${MOCK_LOG_DIR:?}"

printf '%s\n' "$*" >>"${MOCK_LOG_DIR}/curl.log"

out=""
previous=""
url=""
for arg in "$@"; do
  if [[ "${previous}" == "-o" ]]; then
    out="${arg}"
  fi
  previous="${arg}"
  url="${arg}"
done

[[ -n "${out}" ]] || exit 1
cat >"${out}" <<'BIN'
#!/usr/bin/env bash
set -euo pipefail

: "${MOCK_LOG_DIR:?}"
printf 'dashboard:%s\n' "$*" >>"${MOCK_LOG_DIR}/launch.log"
BIN
chmod +x "${out}"
EOF

  chmod +x "${mock_dir}/uname" "${mock_dir}/curl"
}

run_install() {
  local case_dir="$1"
  shift

  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_BIN_HOME="${case_dir}/bin" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  "$@" \
  sh "${install_script}" --from-test >"${case_dir}/stdout.log" 2>"${case_dir}/stderr.log"
}

new_case_dir() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs"
  create_mocks "${case_dir}/mocks"
  printf '%s' "$case_dir"
}

assert_dashboard_install_contract() {
  local case_dir="$1"
  local expected_asset="$2"
  local binary="${case_dir}/bin/credimi-runner"

  assert_file_exists "$binary"
  assert_executable "$binary"
  assert_contains "releases/latest/download/${expected_asset}" "${case_dir}/logs/curl.log"
  assert_contains "dashboard:--from-test" "${case_dir}/logs/launch.log"
  assert_contains "Starting Credimi Runner dashboard" "${case_dir}/stderr.log"
}

run_linux_x86_64_case() {
  local case_dir
  case_dir="$(new_case_dir)"

  run_install "$case_dir" env FAKE_UNAME_S="Linux" FAKE_UNAME_M="x86_64"

  assert_dashboard_install_contract "$case_dir" "credimi-runner-Linux-x86_64"
}

run_linux_arm64_case() {
  local case_dir
  case_dir="$(new_case_dir)"

  run_install "$case_dir" env FAKE_UNAME_S="Linux" FAKE_UNAME_M="arm64"

  assert_dashboard_install_contract "$case_dir" "credimi-runner-Linux-aarch64"
}

run_darwin_arm64_case() {
  local case_dir
  case_dir="$(new_case_dir)"

  run_install "$case_dir" env FAKE_UNAME_S="Darwin" FAKE_UNAME_M="arm64"

  assert_dashboard_install_contract "$case_dir" "credimi-runner-Darwin-arm64"
}

run_custom_bin_dir_case() {
  local case_dir
  local custom_bin
  case_dir="$(new_case_dir)"
  custom_bin="${case_dir}/custom-bin"

  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  CREDIMI_RUNNER_BIN_DIR="$custom_bin" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  FAKE_UNAME_S="Linux" \
  FAKE_UNAME_M="amd64" \
  sh "${install_script}" >"${case_dir}/stdout.log" 2>"${case_dir}/stderr.log"

  assert_file_exists "${custom_bin}/credimi-runner"
  assert_executable "${custom_bin}/credimi-runner"
  assert_contains "releases/latest/download/credimi-runner-Linux-x86_64" "${case_dir}/logs/curl.log"
  assert_contains "dashboard:" "${case_dir}/logs/launch.log"
}

run_unsupported_platform_case() {
  local case_dir
  case_dir="$(new_case_dir)"

  if run_install "$case_dir" env FAKE_UNAME_S="FreeBSD" FAKE_UNAME_M="x86_64"; then
    fail "unsupported platform should fail"
  fi

  assert_file_absent "${case_dir}/bin/credimi-runner"
  assert_contains "unsupported operating system: FreeBSD" "${case_dir}/stderr.log"
}

run_shell_path_message_case() {
  local shell_name="$1"
  local expected_message="$2"
  local case_dir
  case_dir="$(new_case_dir)"

  run_install "$case_dir" env SHELL="/usr/bin/${shell_name}" FAKE_UNAME_S="Linux" FAKE_UNAME_M="x86_64"

  assert_contains "ACTION REQUIRED: credimi-runner is not available on PATH" "${case_dir}/stderr.log"
  assert_contains "Detected shell: ${shell_name}" "${case_dir}/stderr.log"
  assert_contains "The installer did not modify your shell configuration." "${case_dir}/stderr.log"
  assert_contains "$expected_message" "${case_dir}/stderr.log"
}

run_path_already_configured_case() {
  local case_dir
  case_dir="$(new_case_dir)"
  mkdir -p "${case_dir}/bin"

  PATH="${case_dir}/bin:${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_BIN_HOME="${case_dir}/bin" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  SHELL="/bin/bash" \
  FAKE_UNAME_S="Linux" \
  FAKE_UNAME_M="x86_64" \
  sh "${install_script}" >"${case_dir}/stdout.log" 2>"${case_dir}/stderr.log"

  assert_not_contains "ACTION REQUIRED" "${case_dir}/stderr.log"
}

run_default_bin_path_message_case() {
  local case_dir
  case_dir="$(new_case_dir)"

  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  SHELL="/bin/bash" \
  FAKE_UNAME_S="Linux" \
  FAKE_UNAME_M="x86_64" \
  sh "${install_script}" >"${case_dir}/stdout.log" 2>"${case_dir}/stderr.log"

  assert_file_exists "${case_dir}/home/.local/bin/credimi-runner"
  assert_contains 'export PATH="$HOME/.local/bin:$PATH"' "${case_dir}/stderr.log"
}

run_linux_x86_64_case
run_linux_arm64_case
run_darwin_arm64_case
run_custom_bin_dir_case
run_unsupported_platform_case
run_shell_path_message_case "bash" "~/.bashrc"
run_shell_path_message_case "zsh" "~/.zshrc"
run_shell_path_message_case "fish" "fish_add_path"
run_shell_path_message_case "dash" "~/.profile"
run_shell_path_message_case "elvish" "your shell's startup file"
run_path_already_configured_case
run_default_bin_path_message_case

printf 'install.sh tests passed\n'
