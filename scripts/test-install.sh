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

  [[ ! -e "$path" ]] || ! rg -Fq -- "$needle" "$path" || fail "did not expect '${needle}' in ${path}"
}

assert_empty_or_missing() {
  local path="$1"

  [[ ! -e "$path" || ! -s "$path" ]] || fail "expected empty or missing file: $path"
}

assert_file_equals() {
  local path="$1"
  local expected="$2"
  local actual

  actual="$(cat "$path")"
  [[ "${actual}" == "${expected}" ]] || fail "unexpected content in ${path}"
}

assert_file_starts_with() {
  local path="$1"
  local expected_prefix="$2"
  local actual

  actual="$(cat "$path")"
  [[ "${actual}" == "${expected_prefix}"* ]] || fail "unexpected prefix in ${path}"
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
for arg in "$@"; do
  if [[ "${previous}" == "-o" ]]; then
    out="${arg}"
    break
  fi
  previous="${arg}"
done

if [[ -n "${out}" ]]; then
  printf '#!/usr/bin/env bash\nexit 0\n' >"${out}"
  chmod +x "${out}"
fi
EOF

  cat >"${mock_dir}/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

: "${MOCK_LOG_DIR:?}"

printf '%s\n' "$*" >>"${MOCK_LOG_DIR}/docker.log"

if [[ "${1:-}" == "compose" ]]; then
  exit 0
fi

printf 'unexpected docker invocation: %s\n' "$*" >&2
exit 1
EOF

  chmod +x "${mock_dir}/uname" "${mock_dir}/curl" "${mock_dir}/docker"
}

run_install() {
  local case_dir="$1"
  local mock_dir="${case_dir}/mocks"

  PATH="${mock_dir}:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_BIN_HOME="${case_dir}/bin" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  CREDIMI_URL="https://credimi.example" \
  TEMPORAL_ADDRESS="temporal.example:7233" \
  CREDIMI_RUNNER_ID="/org-id/runner-01" \
  CREDIMI_INSTALL_AUTH_MODE="api_key" \
  CREDIMI_USER_API_KEY="user-api-key" \
  CREDIMI_INTERNAL_ADMIN_KEY="internal-admin-key" \
  CREDIMI_SERVICE_MODE="quick" \
  RUNNER_HOST="0.0.0.0" \
  RUNNER_PORT="8050" \
  RUNNER_CADDY_SITE=":80" \
  sh "${install_script}" >/dev/null
}

run_linux_usb_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs"
  create_mocks "${case_dir}/mocks"

  FAKE_UNAME_S="Linux" FAKE_UNAME_M="x86_64" CREDIMI_CONTAINER_MODE="usb" run_install "${case_dir}"

  local bin_dir="${case_dir}/bin"
  local config_dir="${case_dir}/config/credimi/runner"
  local launcher="${bin_dir}/credimi-runner-service"
  local binary="${bin_dir}/credimi-runner"
  local env_file="${config_dir}/.env"
  local compose_file="${config_dir}/docker-compose.yaml"
  local docker_log="${case_dir}/logs/docker.log"
  local curl_log="${case_dir}/logs/curl.log"

  assert_file_exists "${launcher}"
  assert_file_exists "${env_file}"
  assert_file_exists "${compose_file}"
  assert_file_absent "${binary}"
  assert_contains "CREDIMI_RUNNER_BACKEND=container" "${env_file}"
  assert_contains "CREDIMI_CONTAINER_MODE=usb" "${env_file}"
  assert_contains "runner:" "${compose_file}"
  assert_contains "--usb" "${compose_file}"
  assert_contains "privileged: true" "${compose_file}"
  assert_contains "/dev/bus/usb:/dev/bus/usb" "${compose_file}"
  assert_empty_or_missing "${curl_log}"

  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  "${launcher}" quick >/dev/null

  assert_contains "compose version" "${docker_log}"
  assert_contains "up runner caddy tunnel" "${docker_log}"
  assert_not_contains "runner_host caddy tunnel" "${docker_log}"
}

run_linux_emulator_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs"
  create_mocks "${case_dir}/mocks"

  FAKE_UNAME_S="Linux" \
  FAKE_UNAME_M="x86_64" \
  CREDIMI_CONTAINER_MODE="emulator" \
  ANDROID_KEYS_DIR="/srv/android-keys" \
  HOST_AVD_HOME_PATH="/srv/credimi/avd-home" \
  HOST_AVD_GOLDEN_PATH="/srv/credimi/avd-golden" \
  BASE_NAME="credimi" \
  GOLDEN_PATH="/avd-golden/credimi-golden" \
  run_install "${case_dir}"

  local config_dir="${case_dir}/config/credimi/runner"
  local env_file="${config_dir}/.env"
  local compose_file="${config_dir}/docker-compose.yaml"

  assert_file_exists "${env_file}"
  assert_file_exists "${compose_file}"
  assert_contains "CREDIMI_CONTAINER_MODE=emulator" "${env_file}"
  assert_contains "RUNNER_IMAGE=ghcr.io/forkbombeu/credimi-runner-emulator:latest" "${env_file}"
  assert_contains "ANDROID_KEYS_DIR=/srv/android-keys" "${env_file}"
  assert_contains "--emulator" "${compose_file}"
  assert_contains "/dev/kvm:/dev/kvm" "${compose_file}"
  assert_contains '${ANDROID_KEYS_DIR}:/root/.android' "${compose_file}"
}

run_darwin_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs"
  create_mocks "${case_dir}/mocks"

  FAKE_UNAME_S="Darwin" FAKE_UNAME_M="arm64" run_install "${case_dir}"

  local bin_dir="${case_dir}/bin"
  local config_dir="${case_dir}/config/credimi/runner"
  local launcher="${bin_dir}/credimi-runner-service"
  local binary="${bin_dir}/credimi-runner"
  local env_file="${config_dir}/.env"
  local curl_log="${case_dir}/logs/curl.log"

  assert_file_exists "${launcher}"
  assert_file_exists "${env_file}"
  assert_file_exists "${binary}"
  assert_executable "${binary}"
  assert_contains "CREDIMI_RUNNER_BACKEND=host" "${env_file}"
  assert_contains "BASE_NAME=credimi" "${env_file}"
  assert_contains "credimi-runner-Darwin-arm64" "${curl_log}"
}

run_linux_arm64_host_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs"
  create_mocks "${case_dir}/mocks"

  FAKE_UNAME_S="Linux" FAKE_UNAME_M="arm64" CREDIMI_RUNNER_BACKEND="host" run_install "${case_dir}"

  local curl_log="${case_dir}/logs/curl.log"
  assert_contains "credimi-runner-Linux-aarch64" "${curl_log}"
}

run_noninteractive_empty_optional_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs"
  create_mocks "${case_dir}/mocks"

  FAKE_UNAME_S="Linux" \
  FAKE_UNAME_M="x86_64" \
  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_BIN_HOME="${case_dir}/bin" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  CREDIMI_URL="https://credimi.example" \
  TEMPORAL_ADDRESS="temporal.example:7233" \
  CREDIMI_RUNNER_ID="/org-id/runner-01" \
  CREDIMI_INSTALL_AUTH_MODE="api_key" \
  CREDIMI_USER_API_KEY="user-api-key" \
  CREDIMI_INTERNAL_ADMIN_KEY="" \
  CREDIMI_SERVICE_MODE="quick" \
  RUNNER_HOST="0.0.0.0" \
  RUNNER_PORT="8050" \
  RUNNER_CADDY_SITE=":80" \
  CREDIMI_CONTAINER_MODE="usb" \
  sh "${install_script}" >/dev/null

  local env_file="${case_dir}/config/credimi/runner/.env"
  assert_file_exists "${env_file}"
  assert_contains "CREDIMI_INTERNAL_ADMIN_KEY=" "${env_file}"
}

run_quick_mode_with_domain_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs"
  create_mocks "${case_dir}/mocks"

  FAKE_UNAME_S="Linux" FAKE_UNAME_M="x86_64" CREDIMI_CONTAINER_MODE="usb" run_install "${case_dir}"

  local launcher="${case_dir}/bin/credimi-runner-service"
  local env_file="${case_dir}/config/credimi/runner/.env"
  local docker_log="${case_dir}/logs/docker.log"

  cat >"${env_file}" <<'EOF'
CREDIMI_RUNNER_BACKEND=container
RUNNER_DOMAIN=runner.example.com
RUNNER_CADDY_SITE=:80
EOF

  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  "${launcher}" quick >/dev/null

  assert_contains "up runner caddy tunnel" "${docker_log}"
}

run_literal_env_loading_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs"
  create_mocks "${case_dir}/mocks"

  FAKE_UNAME_S="Linux" FAKE_UNAME_M="x86_64" CREDIMI_CONTAINER_MODE="usb" run_install "${case_dir}"

  local launcher="${case_dir}/bin/credimi-runner-service"
  local env_file="${case_dir}/config/credimi/runner/.env"
  local docker_log="${case_dir}/logs/docker.log"
  local marker="${case_dir}/command-substitution-ran"

  cat >"${env_file}" <<EOF
CREDIMI_RUNNER_BACKEND=container
RUNNER_DOMAIN=runner.example.com
CLOUDFLARE_TUNNEL_TOKEN=\$(touch "${marker}")
RUNNER_CADDY_SITE=:80
EOF

  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  "${launcher}" named >/dev/null

  assert_file_absent "${marker}"
  assert_contains "up runner caddy tunnel_named" "${docker_log}"
}

run_host_bind_host_readiness_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs"
  create_mocks "${case_dir}/mocks"

  FAKE_UNAME_S="Darwin" FAKE_UNAME_M="arm64" run_install "${case_dir}"

  local launcher="${case_dir}/bin/credimi-runner-service"
  local binary="${case_dir}/bin/credimi-runner"
  local env_file="${case_dir}/config/credimi/runner/.env"
  local curl_log="${case_dir}/logs/curl.log"

  cat >"${binary}" <<'EOF'
#!/usr/bin/env bash
sleep 2
EOF
  chmod +x "${binary}"

  cat >"${env_file}" <<'EOF'
CREDIMI_RUNNER_BACKEND=host
RUNNER_HOST=192.0.2.10
RUNNER_PORT=9000
RUNNER_CADDY_SITE=:80
EOF

  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  "${launcher}" quick >/dev/null

  assert_contains "http://192.0.2.10:9000/" "${curl_log}"
}

run_existing_env_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs" "${case_dir}/config/credimi/runner"
  create_mocks "${case_dir}/mocks"

  local env_file="${case_dir}/config/credimi/runner/.env"
  local original_env
  original_env='CREDIMI_URL=https://persisted.example
CREDIMI_RUNNER_ID=/org-id/persisted-runner
CREDIMI_USER_API_KEY=persisted-user-api-key
CREDIMI_PB_ADMIN=
CREDIMI_PB_PASS=
CREDIMI_INTERNAL_ADMIN_KEY=persisted-internal-admin-key
TEMPORAL_ADDRESS=temporal.persisted.example:7233
CREDIMI_RUNNER_BACKEND=container
RUNNER_HOST=127.0.0.1
RUNNER_PORT=9000
RUNNER_DOMAIN=
RUNNER_CADDY_SITE=:8080
CLOUDFLARE_TUNNEL_TOKEN=
CREDIMI_SERVICE_MODE=quick
RUNNER_IMAGE=ghcr.io/example/custom-runner:latest'
  printf '%s\n' "${original_env}" >"${env_file}"

  FAKE_UNAME_S="Linux" FAKE_UNAME_M="x86_64" \
  CREDIMI_CONTAINER_MODE="usb" \
  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_BIN_HOME="${case_dir}/bin" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  sh "${install_script}" >/dev/null

  local launcher="${case_dir}/bin/credimi-runner-service"
  local compose_file="${case_dir}/config/credimi/runner/docker-compose.yaml"
  local curl_log="${case_dir}/logs/curl.log"

  assert_file_exists "${launcher}"
  assert_file_exists "${compose_file}"
  assert_contains "CREDIMI_URL=https://persisted.example" "${env_file}"
  assert_contains "CREDIMI_RUNNER_ID=/org-id/persisted-runner" "${env_file}"
  assert_contains "CREDIMI_USER_API_KEY=persisted-user-api-key" "${env_file}"
  assert_contains "CREDIMI_CONTAINER_MODE=usb" "${env_file}"
  assert_contains "RUNNER_PORT=9000" "${env_file}"
  assert_contains "RUNNER_CADDY_SITE=:8080" "${env_file}"
  assert_contains "--usb" "${compose_file}"
  assert_empty_or_missing "${curl_log}"
}

run_existing_env_mode_switch_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs" "${case_dir}/config/credimi/runner"
  create_mocks "${case_dir}/mocks"

  local env_file="${case_dir}/config/credimi/runner/.env"
  cat >"${env_file}" <<'EOF'
CREDIMI_URL=https://persisted.example
CREDIMI_RUNNER_ID=/org-id/persisted-runner
CREDIMI_USER_API_KEY=persisted-user-api-key
CREDIMI_PB_ADMIN=
CREDIMI_PB_PASS=
CREDIMI_INTERNAL_ADMIN_KEY=
TEMPORAL_ADDRESS=temporal.persisted.example:7233
CREDIMI_RUNNER_BACKEND=container
CREDIMI_CONTAINER_MODE=emulator
RUNNER_HOST=127.0.0.1
RUNNER_PORT=9000
RUNNER_DOMAIN=
RUNNER_CADDY_SITE=:8080
CLOUDFLARE_TUNNEL_TOKEN=
CREDIMI_SERVICE_MODE=quick
RUNNER_IMAGE=ghcr.io/forkbombeu/credimi-runner-emulator:latest
ANDROID_KEYS_DIR=/srv/android-keys
HOST_AVD_HOME_PATH=/srv/credimi/avd-home
HOST_AVD_GOLDEN_PATH=/srv/credimi/avd-golden
BASE_NAME=credimi
GOLDEN_PATH=/avd-golden/credimi-golden
EOF

  FAKE_UNAME_S="Linux" FAKE_UNAME_M="x86_64" \
  CREDIMI_CONTAINER_MODE="usb" \
  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_BIN_HOME="${case_dir}/bin" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  sh "${install_script}" >/dev/null

  assert_contains "CREDIMI_CONTAINER_MODE=usb" "${env_file}"
  assert_contains "RUNNER_IMAGE=ghcr.io/forkbombeu/credimi-runner-phone:latest" "${env_file}"
  assert_contains "ANDROID_KEYS_DIR=" "${env_file}"
  assert_contains "HOST_AVD_HOME_PATH=" "${env_file}"
  assert_contains "HOST_AVD_GOLDEN_PATH=" "${env_file}"
  assert_contains "BASE_NAME=" "${env_file}"
  assert_contains "GOLDEN_PATH=" "${env_file}"
}

run_linux_usb_case
run_linux_emulator_case
run_darwin_case
run_linux_arm64_host_case
run_noninteractive_empty_optional_case
run_quick_mode_with_domain_case
run_literal_env_loading_case
run_host_bind_host_readiness_case
run_existing_env_case
run_existing_env_mode_switch_case

printf 'install.sh tests passed\n'
