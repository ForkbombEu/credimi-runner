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

file_mode() {
  local path="$1"

  if stat -c '%a' "$path" >/dev/null 2>&1; then
    stat -c '%a' "$path"
    return 0
  fi

  stat -f '%Lp' "$path"
}

assert_file_mode() {
  local path="$1"
  local expected_mode="$2"
  local actual_mode

  actual_mode="$(file_mode "$path")"
  [[ "${actual_mode}" == "${expected_mode}" ]] || fail "unexpected mode for ${path}: got ${actual_mode}, want ${expected_mode}"
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
write_out=""
url=""
data=""
previous=""
for arg in "$@"; do
  if [[ "${previous}" == "-o" ]]; then
    out="${arg}"
  fi
  if [[ "${previous}" == "--output" ]]; then
    out="${arg}"
  fi
  if [[ "${previous}" == "--write-out" ]]; then
    write_out="${arg}"
  fi
  if [[ "${previous}" == "--data" ]]; then
    data="${arg}"
  fi
  previous="${arg}"
  url="${arg}"
done

if [[ -n "${data}" ]]; then
  printf '%s\n' "${data}" >>"${MOCK_LOG_DIR}/curl.payload.log"
fi

case "${url}" in
  *"/api/collections/_superusers/auth-with-password")
    [[ -n "${out}" ]] || exit 1
    printf '{"token":"admin-token"}' >"${out}"
    printf '200'
    exit 0
    ;;
  *"/api/organizations/my")
    [[ -n "${out}" ]] || exit 1
    printf '{"canonified_name":"%s"}' "${MOCK_MY_ORG:-org-id}" >"${out}"
    printf '200'
    exit 0
    ;;
  *"/api/mobile-runner/preview-id")
    [[ -n "${out}" ]] || exit 1
    printf '{"organization":"%s","runner_id":"%s"}' \
      "${MOCK_PREVIEW_ORG:-org-id}" \
      "${MOCK_PREVIEW_RUNNER_ID:-/org-id/runner-01}" >"${out}"
    printf '200'
    exit 0
    ;;
  *"/api/mobile-runner")
    [[ -n "${out}" ]] || exit 1
    printf '{"runner_id":"%s"}' "${MOCK_STORE_RUNNER_ID:-/org-id/runner-01}" >"${out}"
    printf '200'
    exit 0
    ;;
esac

if [[ -n "${out}" ]]; then
  if [[ "${out}" == "/dev/null" ]]; then
    exit 0
  fi
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
  case "$*" in
    *" logs tunnel"*)
      printf 'INF quick tunnel ready at %s\n' "${MOCK_TUNNEL_URL:-https://runner.example.trycloudflare.com}"
      ;;
  esac
  exit 0
fi

printf 'unexpected docker invocation: %s\n' "$*" >&2
exit 1
EOF

  cat >"${mock_dir}/adb" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

: "${MOCK_LOG_DIR:?}"

printf '%s\n' "$*" >>"${MOCK_LOG_DIR}/adb.log"

case "${1:-}" in
  devices)
    printf 'List of devices attached\n'
    if [[ -n "${MOCK_ADB_DEVICE_SERIAL:-SERIAL-USB-01}" ]]; then
      printf '%s\tdevice usb:1-1 product:test model:test device:test transport_id:1\n' "${MOCK_ADB_DEVICE_SERIAL:-SERIAL-USB-01}"
    fi
    ;;
esac
EOF

  chmod +x "${mock_dir}/uname" "${mock_dir}/curl" "${mock_dir}/docker" "${mock_dir}/adb"
}

run_install() {
  local case_dir="$1"
  local mock_dir="${case_dir}/mocks"

  PATH="${mock_dir}:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_BIN_HOME="${case_dir}/bin" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  MOCK_PREVIEW_RUNNER_ID="/org-id/preview-runner" \
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

run_install_with_temp_dir() {
  local case_dir="$1"
  local temp_dir="$2"
  local mock_dir="${case_dir}/mocks"

  PATH="${mock_dir}:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_BIN_HOME="${case_dir}/bin" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  MOCK_PREVIEW_RUNNER_ID="/org-id/preview-runner" \
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
  CREDIMI_TEMP_DIR="${temp_dir}" \
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
  local curl_payload_log="${case_dir}/logs/curl.payload.log"

  assert_file_exists "${launcher}"
  assert_contains "trap 'echo \"ERROR at \${BASH_SOURCE}:\${LINENO}: \${BASH_COMMAND}\"' ERR" "${launcher}"
  assert_contains 'echo "WARNING: This script does NOT work if Docker is installed via snap. See: https://stackoverflow.com/questions/73290497/getting-docker-open-env-permission-denied-when-trying-to-pass-a-env-file. Install Docker via apt or the official shell script instead." >&2' "${launcher}"
  assert_file_exists "${env_file}"
  assert_file_exists "${compose_file}"
  assert_file_absent "${binary}"
  assert_contains "CREDIMI_RUNNER_BACKEND=container" "${env_file}"
  assert_contains "CREDIMI_RUNNER_TYPE=android_phone" "${env_file}"
  assert_contains "CREDIMI_CONTAINER_MODE=usb" "${env_file}"
  assert_contains "CREDIMI_RUNNER_SERIAL=SERIAL-USB-01" "${env_file}"
  assert_contains "CREDIMI_TEMP_DIR=/tmp/credimi-runner-tmp" "${env_file}"
  assert_contains "OTEL_ENABLED=true" "${env_file}"
  assert_contains "OTEL_EXPORTER_OTLP_ENDPOINT=https://otel-collector.credimi.io" "${env_file}"
  assert_contains "OTEL_SERVICE_NAME=credimi-runner" "${env_file}"
  assert_contains "CREDIMI_RUNNER_NAME=runner-01" "${env_file}"
  assert_contains "CREDIMI_RUNNER_ORGANIZATION=org-id" "${env_file}"
  assert_contains "runner:" "${compose_file}"
  assert_contains "--usb" "${compose_file}"
  assert_contains 'PORT: "${RUNNER_PORT:-8050}"' "${compose_file}"
  assert_not_contains '\${RUNNER_CADDY_SITE:-:80}' "${compose_file}"
  assert_contains "privileged: true" "${compose_file}"
  assert_contains "/dev/bus/usb:/dev/bus/usb" "${compose_file}"
  assert_empty_or_missing "${curl_log}"

  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  "${launcher}" quick >/dev/null

  assert_contains "compose version" "${docker_log}"
  assert_contains "up -d runner caddy tunnel" "${docker_log}"
  assert_contains "logs tunnel" "${docker_log}"
  assert_contains "logs -f runner caddy tunnel" "${docker_log}"
  assert_not_contains "runner_host caddy tunnel" "${docker_log}"
  assert_contains "/api/mobile-runner" "${curl_log}"
  assert_contains '"type":"android_phone"' "${curl_payload_log}"
  assert_contains '"serial":"SERIAL-USB-01"' "${curl_payload_log}"
}

run_linux_usb_otel_disabled_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs"
  create_mocks "${case_dir}/mocks"

  FAKE_UNAME_S="Linux" \
  FAKE_UNAME_M="x86_64" \
  CREDIMI_CONTAINER_MODE="usb" \
  OTEL_ENABLED="false" \
  run_install "${case_dir}"

  local env_file="${case_dir}/config/credimi/runner/.env"

  assert_file_exists "${env_file}"
  assert_contains "OTEL_ENABLED=false" "${env_file}"
  assert_contains "OTEL_EXPORTER_OTLP_ENDPOINT=" "${env_file}"
  assert_contains "OTEL_SERVICE_NAME=credimi-runner" "${env_file}"
}

run_linux_emulator_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs"
  create_mocks "${case_dir}/mocks"

  FAKE_UNAME_S="Linux" \
  FAKE_UNAME_M="x86_64" \
  CREDIMI_RUNNER_TYPE="android_emulator" \
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
  assert_contains "CREDIMI_RUNNER_TYPE=android_emulator" "${env_file}"
  assert_contains "CREDIMI_CONTAINER_MODE=emulator" "${env_file}"
  assert_contains "RUNNER_IMAGE=ghcr.io/forkbombeu/credimi-runner-emulator:latest" "${env_file}"
  assert_contains "ANDROID_KEYS_DIR=/srv/android-keys" "${env_file}"
  assert_contains "--emulator" "${compose_file}"
  assert_contains 'PORT: "${RUNNER_PORT:-8050}"' "${compose_file}"
  assert_contains "/dev/kvm:/dev/kvm" "${compose_file}"
  assert_contains '${ANDROID_KEYS_DIR}:/root/.android' "${compose_file}"
}

run_linux_wifi_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs"
  create_mocks "${case_dir}/mocks"

  FAKE_UNAME_S="Linux" \
  FAKE_UNAME_M="x86_64" \
  CREDIMI_RUNNER_TYPE="android_phone" \
  CREDIMI_RUNNER_DEVICE_MODE="wifi" \
  CREDIMI_RUNNER_WIFI_IP="192.168.1.42" \
  CREDIMI_RUNNER_WIFI_PORT="38349" \
  run_install "${case_dir}"

  local launcher="${case_dir}/bin/credimi-runner-service"
  local config_dir="${case_dir}/config/credimi/runner"
  local env_file="${config_dir}/.env"
  local compose_file="${config_dir}/docker-compose.yaml"
  local curl_payload_log="${case_dir}/logs/curl.payload.log"

  assert_contains "CREDIMI_RUNNER_TYPE=android_phone" "${env_file}"
  assert_contains "CREDIMI_RUNNER_DEVICE_MODE=wifi" "${env_file}"
  assert_contains "CREDIMI_RUNNER_WIFI_IP=192.168.1.42" "${env_file}"
  assert_contains "CREDIMI_RUNNER_WIFI_PORT=38349" "${env_file}"
  assert_contains "CREDIMI_RUNNER_SERIAL=192.168.1.42:38349" "${env_file}"
  assert_contains "CREDIMI_CONTAINER_MODE=wifi" "${env_file}"
  assert_contains '"${CREDIMI_RUNNER_WIFI_IP}:${CREDIMI_RUNNER_WIFI_PORT:-5555}"' "${compose_file}"
  assert_contains 'PORT: "${RUNNER_PORT:-8050}"' "${compose_file}"

  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  "${launcher}" quick >/dev/null

  assert_contains '"type":"android_phone"' "${curl_payload_log}"
  assert_contains '"serial":"192.168.1.42:38349"' "${curl_payload_log}"
}

run_linux_redroid_no_device_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs"
  create_mocks "${case_dir}/mocks"

  FAKE_UNAME_S="Linux" \
  FAKE_UNAME_M="x86_64" \
  CREDIMI_RUNNER_TYPE="redroid" \
  CREDIMI_RUNNER_DEVICE_MODE="no_device" \
  CREDIMI_RUNNER_WIFI_IP="192.168.1.77" \
  CREDIMI_RUNNER_WIFI_PORT="5557" \
  AVDCTL_SSH_TARGET="credimi@remote-host" \
  AVDCTL_SSH_PASSWORD="ssh-secret" \
  AVDCTL_SSH_KNOWN_HOSTS_PATH="/home/bario/.ssh/known_hosts" \
  AVDCTL_SUDO="true" \
  AVDCTL_SUDO_PASSWORD="sudo-secret" \
  REDROID_DATA_DIR="/srv/redroid-data" \
  REDROID_DATA_TAR="/srv/redroid-data.tar" \
  run_install "${case_dir}"

  local launcher="${case_dir}/bin/credimi-runner-service"
  local config_dir="${case_dir}/config/credimi/runner"
  local env_file="${config_dir}/.env"
  local compose_file="${config_dir}/docker-compose.yaml"
  local curl_payload_log="${case_dir}/logs/curl.payload.log"

  assert_file_exists "${launcher}"
  assert_contains "CREDIMI_RUNNER_TYPE=redroid" "${env_file}"
  assert_contains "CREDIMI_RUNNER_DEVICE_MODE=no_device" "${env_file}"
  assert_contains "CREDIMI_RUNNER_WIFI_IP=192.168.1.77" "${env_file}"
  assert_contains "CREDIMI_RUNNER_WIFI_PORT=5557" "${env_file}"
  assert_contains "CREDIMI_RUNNER_SERIAL=192.168.1.77:5557" "${env_file}"
  assert_contains "CREDIMI_CONTAINER_MODE=no_device" "${env_file}"
  assert_contains "AVDCTL_SSH_TARGET=credimi@remote-host" "${env_file}"
  assert_contains "AVDCTL_SSH_PASSWORD=ssh-secret" "${env_file}"
  assert_contains "AVDCTL_SSH_KNOWN_HOSTS_PATH=/home/bario/.ssh/known_hosts" "${env_file}"
  assert_contains "AVDCTL_SUDO=true" "${env_file}"
  assert_contains "AVDCTL_SUDO_PASSWORD=sudo-secret" "${env_file}"
  assert_contains "REDROID_DATA_DIR=/srv/redroid-data" "${env_file}"
  assert_contains "REDROID_DATA_TAR=/srv/redroid-data.tar" "${env_file}"
  assert_contains "--no-device" "${compose_file}"
  assert_contains '${AVDCTL_SSH_KNOWN_HOSTS_PATH}:/root/.ssh/known_hosts:ro' "${compose_file}"
  assert_contains 'PORT: "${RUNNER_PORT:-8050}"' "${compose_file}"

  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  "${launcher}" quick >/dev/null

  assert_contains '"type":"redroid"' "${curl_payload_log}"
  assert_contains '"serial":"192.168.1.77:5557"' "${curl_payload_log}"
}

run_direct_container_case() {
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
  CREDIMI_RUNNER_ID="/org-id/direct-runner" \
  CREDIMI_INSTALL_AUTH_MODE="api_key" \
  CREDIMI_USER_API_KEY="user-api-key" \
  CREDIMI_INTERNAL_ADMIN_KEY="" \
  CREDIMI_SERVICE_MODE="direct" \
  RUNNER_PUBLIC_IP="198.51.100.10" \
  RUNNER_PUBLIC_PORT="9443" \
  RUNNER_HOST="0.0.0.0" \
  RUNNER_PORT="8050" \
  CREDIMI_RUNNER_TYPE="android_phone" \
  CREDIMI_RUNNER_DEVICE_MODE="usb" \
  sh "${install_script}" >/dev/null

  local launcher="${case_dir}/bin/credimi-runner-service"
  local env_file="${case_dir}/config/credimi/runner/.env"
  local compose_file="${case_dir}/config/credimi/runner/docker-compose.yaml"
  local docker_log="${case_dir}/logs/docker.log"
  local curl_payload_log="${case_dir}/logs/curl.payload.log"

  assert_contains "CREDIMI_SERVICE_MODE=direct" "${env_file}"
  assert_contains "RUNNER_PUBLIC_IP=198.51.100.10" "${env_file}"
  assert_contains "RUNNER_PUBLIC_PORT=9443" "${env_file}"
  assert_contains "RUNNER_CADDY_SITE=" "${env_file}"
  assert_contains "network_mode: host" "${compose_file}"

  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  MOCK_STORE_RUNNER_ID="/org-id/direct-runner" \
  "${launcher}" direct >/dev/null

  assert_contains "compose version" "${docker_log}"
  assert_contains "up -d runner" "${docker_log}"
  assert_contains "logs -f runner" "${docker_log}"
  assert_not_contains "logs tunnel" "${docker_log}"
  assert_not_contains "caddy tunnel" "${docker_log}"
  assert_contains '"ip":"198.51.100.10"' "${curl_payload_log}"
  assert_contains '"port":"9443"' "${curl_payload_log}"
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
  assert_contains "CREDIMI_RUNNER_TYPE=ios_simulator" "${env_file}"
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

run_host_android_emulator_keeps_golden_path_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs"
  create_mocks "${case_dir}/mocks"

  FAKE_UNAME_S="Linux" \
  FAKE_UNAME_M="x86_64" \
  CREDIMI_RUNNER_BACKEND="host" \
  CREDIMI_RUNNER_TYPE="android_emulator" \
  BASE_NAME="credimi" \
  GOLDEN_PATH="/home/test/avd-golden/credimi-golden" \
  run_install "${case_dir}"

  local env_file="${case_dir}/config/credimi/runner/.env"
  local curl_log="${case_dir}/logs/curl.log"

  assert_contains "credimi-runner-Linux-x86_64" "${curl_log}"
  assert_contains "CREDIMI_RUNNER_BACKEND=host" "${env_file}"
  assert_contains "CREDIMI_RUNNER_TYPE=android_emulator" "${env_file}"
  assert_contains "CREDIMI_CONTAINER_MODE=" "${env_file}"
  assert_contains "HOST_AVD_HOME_PATH=" "${env_file}"
  assert_contains "HOST_AVD_GOLDEN_PATH=" "${env_file}"
  assert_contains "BASE_NAME=credimi" "${env_file}"
  assert_contains "GOLDEN_PATH=/home/test/avd-golden/credimi-golden" "${env_file}"
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
  assert_contains "CREDIMI_TEMP_DIR=/tmp/credimi-runner-tmp" "${env_file}"
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
CREDIMI_URL=https://credimi.example
CREDIMI_RUNNER_BACKEND=container
CREDIMI_RUNNER_ID=/org-id/runner-01
CREDIMI_RUNNER_NAME=runner-01
CREDIMI_USER_API_KEY=user-api-key
RUNNER_DOMAIN=runner.example.com
RUNNER_CADDY_SITE=:80
EOF

  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  "${launcher}" quick >/dev/null

  assert_contains "up -d runner caddy tunnel" "${docker_log}"
  assert_contains "/api/mobile-runner" "${case_dir}/logs/curl.log"
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
CREDIMI_URL=https://credimi.example
CREDIMI_RUNNER_BACKEND=container
CREDIMI_RUNNER_ID=/org-id/runner-01
CREDIMI_RUNNER_NAME=runner-01
CREDIMI_USER_API_KEY=user-api-key
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
  assert_contains "up -d runner caddy tunnel_named" "${docker_log}"
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
CREDIMI_URL=https://credimi.example
CREDIMI_RUNNER_BACKEND=host
CREDIMI_RUNNER_ID=/org-id/runner-01
CREDIMI_RUNNER_NAME=runner-01
CREDIMI_USER_API_KEY=user-api-key
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
  assert_contains "/api/mobile-runner" "${curl_log}"
}

run_direct_host_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs"
  create_mocks "${case_dir}/mocks"

  FAKE_UNAME_S="Darwin" FAKE_UNAME_M="arm64" run_install "${case_dir}"

  local launcher="${case_dir}/bin/credimi-runner-service"
  local binary="${case_dir}/bin/credimi-runner"
  local env_file="${case_dir}/config/credimi/runner/.env"
  local docker_log="${case_dir}/logs/docker.log"
  local curl_payload_log="${case_dir}/logs/curl.payload.log"

  cat >"${binary}" <<'EOF'
#!/usr/bin/env bash
sleep 2
EOF
  chmod +x "${binary}"

  cat >"${env_file}" <<'EOF'
CREDIMI_URL=https://credimi.example
CREDIMI_RUNNER_BACKEND=host
CREDIMI_RUNNER_ID=/org-id/direct-host
CREDIMI_RUNNER_NAME=direct-host
CREDIMI_RUNNER_TYPE=ios_simulator
CREDIMI_USER_API_KEY=user-api-key
CREDIMI_SERVICE_MODE=direct
RUNNER_PUBLIC_IP=198.51.100.20
RUNNER_PUBLIC_PORT=8080
RUNNER_HOST=127.0.0.1
RUNNER_PORT=9000
RUNNER_CADDY_SITE=:80
EOF

  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  MOCK_STORE_RUNNER_ID="/org-id/direct-host" \
  "${launcher}" direct >/dev/null

  assert_empty_or_missing "${docker_log}"
  assert_contains '"ip":"198.51.100.20"' "${curl_payload_log}"
  assert_contains '"port":"8080"' "${curl_payload_log}"
  assert_contains '"type":"ios_simulator"' "${curl_payload_log}"
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
CREDIMI_RUNNER_NAME=persisted-runner
CREDIMI_RUNNER_DESCRIPTION=
CREDIMI_RUNNER_ORGANIZATION=org-id
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
CREDIMI_RUNNER_TYPE=android_phone
CREDIMI_RUNNER_SERIAL=SERIAL-USB-01
CREDIMI_RUNNER_DEVICE_MODE=usb
CREDIMI_RUNNER_WIFI_IP=
CREDIMI_RUNNER_WIFI_PORT=
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
  assert_contains "CREDIMI_RUNNER_NAME=persisted-runner" "${env_file}"
  assert_contains "CREDIMI_USER_API_KEY=persisted-user-api-key" "${env_file}"
  assert_contains "CREDIMI_TEMP_DIR=/tmp/credimi-runner-tmp" "${env_file}"
  assert_contains "CREDIMI_RUNNER_TYPE=android_phone" "${env_file}"
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
CREDIMI_RUNNER_NAME=persisted-runner
CREDIMI_RUNNER_DESCRIPTION=
CREDIMI_RUNNER_ORGANIZATION=org-id
CREDIMI_USER_API_KEY=persisted-user-api-key
CREDIMI_PB_ADMIN=
CREDIMI_PB_PASS=
CREDIMI_INTERNAL_ADMIN_KEY=
TEMPORAL_ADDRESS=temporal.persisted.example:7233
CREDIMI_RUNNER_BACKEND=container
CREDIMI_RUNNER_TYPE=redroid
CREDIMI_RUNNER_SERIAL=
CREDIMI_RUNNER_DEVICE_MODE=no_device
CREDIMI_RUNNER_WIFI_IP=192.168.1.88
CREDIMI_RUNNER_WIFI_PORT=5555
CREDIMI_CONTAINER_MODE=no_device
RUNNER_HOST=127.0.0.1
RUNNER_PORT=9000
RUNNER_DOMAIN=
RUNNER_CADDY_SITE=:8080
CLOUDFLARE_TUNNEL_TOKEN=
CREDIMI_SERVICE_MODE=quick
RUNNER_IMAGE=ghcr.io/forkbombeu/credimi-runner-phone:latest
ANDROID_KEYS_DIR=
HOST_AVD_HOME_PATH=
HOST_AVD_GOLDEN_PATH=
BASE_NAME=
GOLDEN_PATH=
AVDCTL_SSH_TARGET=credimi@remote-host
AVDCTL_SSH_PASSWORD=ssh-secret
AVDCTL_SSH_KNOWN_HOSTS_PATH=/home/bario/.ssh/known_hosts
AVDCTL_SUDO=true
AVDCTL_SUDO_PASSWORD=sudo-secret
REDROID_DATA_DIR=/srv/redroid-data
REDROID_DATA_TAR=/srv/redroid-data.tar
EOF

  FAKE_UNAME_S="Linux" FAKE_UNAME_M="x86_64" \
  CREDIMI_RUNNER_TYPE="android_phone" \
  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_BIN_HOME="${case_dir}/bin" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  sh "${install_script}" >/dev/null

  assert_contains "CREDIMI_RUNNER_TYPE=android_phone" "${env_file}"
  assert_contains "CREDIMI_CONTAINER_MODE=usb" "${env_file}"
  assert_contains "RUNNER_IMAGE=ghcr.io/forkbombeu/credimi-runner-phone:latest" "${env_file}"
  assert_contains "ANDROID_KEYS_DIR=" "${env_file}"
  assert_contains "HOST_AVD_HOME_PATH=" "${env_file}"
  assert_contains "HOST_AVD_GOLDEN_PATH=" "${env_file}"
  assert_contains "BASE_NAME=" "${env_file}"
  assert_contains "GOLDEN_PATH=" "${env_file}"
  assert_contains "AVDCTL_SSH_TARGET=credimi@remote-host" "${env_file}"
  assert_contains "AVDCTL_SSH_PASSWORD=ssh-secret" "${env_file}"
  assert_contains "AVDCTL_SSH_KNOWN_HOSTS_PATH=/home/bario/.ssh/known_hosts" "${env_file}"
  assert_contains "AVDCTL_SUDO=true" "${env_file}"
  assert_contains "AVDCTL_SUDO_PASSWORD=sudo-secret" "${env_file}"
  assert_contains "REDROID_DATA_DIR=" "${env_file}"
  assert_contains "REDROID_DATA_TAR=" "${env_file}"
}

run_existing_env_invalid_key_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs" "${case_dir}/config/credimi/runner"
  create_mocks "${case_dir}/mocks"

  local env_file="${case_dir}/config/credimi/runner/.env"
  local marker="${case_dir}/invalid-key-ran"
  cat >"${env_file}" <<EOF
CREDIMI_URL=https://persisted.example
CREDIMI_RUNNER_NAME=persisted-runner
CREDIMI_USER_API_KEY=persisted-user-api-key
RUNNER_HOST=127.0.0.1
RUNNER_PORT=9000
BAD\$(touch "${marker}")=boom
EOF

  FAKE_UNAME_S="Linux" FAKE_UNAME_M="x86_64" \
  CREDIMI_CONTAINER_MODE="usb" \
  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_BIN_HOME="${case_dir}/bin" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  sh "${install_script}" >/dev/null

  assert_file_absent "${marker}"
  assert_contains "CREDIMI_RUNNER_NAME=persisted-runner" "${env_file}"
}

run_temp_dir_creation_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs"
  create_mocks "${case_dir}/mocks"

  local temp_dir="${case_dir}/credimi-temp"

  FAKE_UNAME_S="Linux" \
  FAKE_UNAME_M="x86_64" \
  CREDIMI_CONTAINER_MODE="usb" \
  run_install_with_temp_dir "${case_dir}" "${temp_dir}"

  local env_file="${case_dir}/config/credimi/runner/.env"

  [[ -d "${temp_dir}" ]] || fail "missing temp dir: ${temp_dir}"
  assert_contains "CREDIMI_TEMP_DIR=${temp_dir}" "${env_file}"
}

run_launcher_preview_case() {
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
  CREDIMI_RUNNER_NAME="preview-runner" \
  CREDIMI_INSTALL_AUTH_MODE="api_key" \
  CREDIMI_USER_API_KEY="user-api-key" \
  CREDIMI_INTERNAL_ADMIN_KEY="" \
  CREDIMI_SERVICE_MODE="quick" \
  RUNNER_HOST="0.0.0.0" \
  RUNNER_PORT="8050" \
  RUNNER_CADDY_SITE=":80" \
  sh "${install_script}" >/dev/null

  local launcher="${case_dir}/bin/credimi-runner-service"
  local env_file="${case_dir}/config/credimi/runner/.env"
  local curl_log="${case_dir}/logs/curl.log"

  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  MOCK_STORE_RUNNER_ID="/org-id/preview-runner" \
  "${launcher}" quick >/dev/null

  assert_contains "CREDIMI_RUNNER_ID=/org-id/preview-runner" "${env_file}"
  assert_contains "/api/organizations/my" "${curl_log}"
  assert_contains "/api/mobile-runner/preview-id" "${curl_log}"
  assert_contains "/api/mobile-runner" "${curl_log}"
}

run_direct_preview_preserves_env_mode_case() {
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
  MOCK_PREVIEW_RUNNER_ID="/org-id/direct-preview" \
  CREDIMI_URL="https://credimi.example" \
  TEMPORAL_ADDRESS="temporal.example:7233" \
  CREDIMI_RUNNER_NAME="direct-preview" \
  CREDIMI_INSTALL_AUTH_MODE="api_key" \
  CREDIMI_USER_API_KEY="user-api-key" \
  CREDIMI_INTERNAL_ADMIN_KEY="" \
  CREDIMI_SERVICE_MODE="direct" \
  RUNNER_PUBLIC_IP="198.51.100.20" \
  RUNNER_PUBLIC_PORT="8080" \
  RUNNER_HOST="0.0.0.0" \
  RUNNER_PORT="8050" \
  sh "${install_script}" >/dev/null

  local launcher="${case_dir}/bin/credimi-runner-service"
  local env_file="${case_dir}/config/credimi/runner/.env"

  chmod 644 "${env_file}"

  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  MOCK_STORE_RUNNER_ID="/org-id/direct-preview" \
  "${launcher}" direct >/dev/null

  assert_file_mode "${env_file}" "644"
  assert_contains "CREDIMI_RUNNER_ID=/org-id/direct-preview" "${env_file}"
  assert_contains "RUNNER_CADDY_SITE=" "${env_file}"
}

run_admin_requires_internal_key_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs"
  create_mocks "${case_dir}/mocks"

  if FAKE_UNAME_S="Linux" \
    FAKE_UNAME_M="x86_64" \
    PATH="${case_dir}/mocks:${PATH}" \
    HOME="${case_dir}/home" \
    XDG_BIN_HOME="${case_dir}/bin" \
    XDG_CONFIG_HOME="${case_dir}/config" \
    MOCK_LOG_DIR="${case_dir}/logs" \
    CREDIMI_URL="https://credimi.example" \
    TEMPORAL_ADDRESS="temporal.example:7233" \
    CREDIMI_INSTALL_AUTH_MODE="admin" \
    CREDIMI_RUNNER_NAME="admin-runner" \
    CREDIMI_RUNNER_ORGANIZATION="org-id" \
    sh "${install_script}" >"${case_dir}/stdout.log" 2>"${case_dir}/stderr.log"; then
    fail "expected admin install without internal admin key to fail"
  fi

  assert_contains "CREDIMI_INTERNAL_ADMIN_KEY is required for non-interactive install" "${case_dir}/stderr.log"
}

run_admin_requires_pb_credentials_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs"
  create_mocks "${case_dir}/mocks"

  if FAKE_UNAME_S="Linux" \
    FAKE_UNAME_M="x86_64" \
    PATH="${case_dir}/mocks:${PATH}" \
    HOME="${case_dir}/home" \
    XDG_BIN_HOME="${case_dir}/bin" \
    XDG_CONFIG_HOME="${case_dir}/config" \
    MOCK_LOG_DIR="${case_dir}/logs" \
    CREDIMI_URL="https://credimi.example" \
    TEMPORAL_ADDRESS="temporal.example:7233" \
    CREDIMI_INSTALL_AUTH_MODE="admin" \
    CREDIMI_INTERNAL_ADMIN_KEY="internal-admin-key" \
    CREDIMI_RUNNER_NAME="admin-runner" \
    CREDIMI_RUNNER_ORGANIZATION="org-id" \
    sh "${install_script}" >"${case_dir}/stdout.log" 2>"${case_dir}/stderr.log"; then
    fail "expected admin install without PB credentials to fail"
  fi

  assert_contains "CREDIMI_PB_ADMIN is required for non-interactive install" "${case_dir}/stderr.log"
}

run_admin_computes_runner_id_case() {
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
  MOCK_PREVIEW_RUNNER_ID="/org-id/admin-runner" \
  CREDIMI_URL="https://credimi.example" \
  TEMPORAL_ADDRESS="temporal.example:7233" \
  CREDIMI_INSTALL_AUTH_MODE="admin" \
  CREDIMI_INTERNAL_ADMIN_KEY="internal-admin-key" \
  CREDIMI_PB_ADMIN="admin@example.org" \
  CREDIMI_PB_PASS="admin-password" \
  CREDIMI_RUNNER_NAME="Admin Runner" \
  CREDIMI_RUNNER_ORGANIZATION="org-id" \
  CREDIMI_SERVICE_MODE="quick" \
  RUNNER_HOST="0.0.0.0" \
  RUNNER_PORT="8050" \
  RUNNER_CADDY_SITE=":80" \
  sh "${install_script}" >/dev/null

  local launcher="${case_dir}/bin/credimi-runner-service"
  local env_file="${case_dir}/config/credimi/runner/.env"

  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  MOCK_STORE_RUNNER_ID="/org-id/admin-runner" \
  "${launcher}" quick >/dev/null

  assert_contains "CREDIMI_RUNNER_ID=/org-id/admin-runner" "${env_file}"
  assert_contains "CREDIMI_INTERNAL_ADMIN_KEY=internal-admin-key" "${env_file}"
  assert_contains "CREDIMI_PB_ADMIN=admin@example.org" "${env_file}"
  assert_contains "CREDIMI_PB_PASS=admin-password" "${env_file}"
  assert_contains "CREDIMI_USER_API_KEY=" "${env_file}"
}

run_recompute_runner_id_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs" "${case_dir}/config/credimi/runner"
  create_mocks "${case_dir}/mocks"

  local env_file="${case_dir}/config/credimi/runner/.env"
  cat >"${env_file}" <<'EOF'
CREDIMI_URL=https://persisted.example
CREDIMI_RUNNER_ID=/org-id/old-runner
CREDIMI_RUNNER_NAME=old-runner
CREDIMI_RUNNER_DESCRIPTION=
CREDIMI_RUNNER_ORGANIZATION=org-id
CREDIMI_USER_API_KEY=
CREDIMI_PB_ADMIN=
CREDIMI_PB_PASS=
CREDIMI_INTERNAL_ADMIN_KEY=internal-admin-key
TEMPORAL_ADDRESS=temporal.persisted.example:7233
EOF

  FAKE_UNAME_S="Linux" \
  FAKE_UNAME_M="x86_64" \
  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_BIN_HOME="${case_dir}/bin" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  MOCK_PREVIEW_RUNNER_ID="/org-id/new-runner" \
  CREDIMI_INSTALL_AUTH_MODE="admin" \
  CREDIMI_USE_EXISTING_RUNNER_ID="no" \
  CREDIMI_INTERNAL_ADMIN_KEY="internal-admin-key" \
  CREDIMI_PB_ADMIN="admin@example.org" \
  CREDIMI_PB_PASS="admin-password" \
  CREDIMI_RUNNER_NAME="New Runner" \
  CREDIMI_RUNNER_ORGANIZATION="org-id" \
  CREDIMI_SERVICE_MODE="quick" \
  RUNNER_HOST="0.0.0.0" \
  RUNNER_PORT="8050" \
  RUNNER_CADDY_SITE=":80" \
  sh "${install_script}" >/dev/null

  local launcher="${case_dir}/bin/credimi-runner-service"

  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  MOCK_STORE_RUNNER_ID="/org-id/new-runner" \
  "${launcher}" quick >/dev/null

  assert_contains "CREDIMI_RUNNER_ID=/org-id/new-runner" "${env_file}"
  assert_not_contains "CREDIMI_RUNNER_ID=/org-id/old-runner" "${env_file}"
}

run_existing_name_updates_by_default_case() {
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
  MOCK_PREVIEW_RUNNER_ID="/org-id/duplicate-runner-1" \
  CREDIMI_URL="https://credimi.example" \
  TEMPORAL_ADDRESS="temporal.example:7233" \
  CREDIMI_RUNNER_NAME="Duplicate Runner" \
  CREDIMI_RUNNER_DESCRIPTION="updated description" \
  CREDIMI_INSTALL_AUTH_MODE="api_key" \
  CREDIMI_USER_API_KEY="user-api-key" \
  CREDIMI_SERVICE_MODE="quick" \
  RUNNER_HOST="0.0.0.0" \
  RUNNER_PORT="8050" \
  RUNNER_CADDY_SITE=":80" \
  sh "${install_script}" >/dev/null

  local launcher="${case_dir}/bin/credimi-runner-service"
  local env_file="${case_dir}/config/credimi/runner/.env"
  local curl_payload_log="${case_dir}/logs/curl.payload.log"

  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  MOCK_STORE_RUNNER_ID="/org-id/duplicate-runner" \
  "${launcher}" quick >/dev/null

  assert_contains "CREDIMI_RUNNER_ID=/org-id/duplicate-runner" "${env_file}"
  assert_contains '"runner_id":"/org-id/duplicate-runner"' "${curl_payload_log}"
  assert_not_contains '"runner_id":"/org-id/duplicate-runner-1"' "${curl_payload_log}"
  assert_contains '"description":"updated description"' "${curl_payload_log}"
}

run_existing_name_can_create_new_case() {
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
  MOCK_PREVIEW_RUNNER_ID="/org-id/duplicate-runner-1" \
  CREDIMI_RUNNER_NAME_CONFLICT_ACTION="create" \
  CREDIMI_URL="https://credimi.example" \
  TEMPORAL_ADDRESS="temporal.example:7233" \
  CREDIMI_RUNNER_NAME="Duplicate Runner" \
  CREDIMI_INSTALL_AUTH_MODE="api_key" \
  CREDIMI_USER_API_KEY="user-api-key" \
  CREDIMI_SERVICE_MODE="quick" \
  RUNNER_HOST="0.0.0.0" \
  RUNNER_PORT="8050" \
  RUNNER_CADDY_SITE=":80" \
  sh "${install_script}" >/dev/null

  local launcher="${case_dir}/bin/credimi-runner-service"
  local env_file="${case_dir}/config/credimi/runner/.env"
  local curl_payload_log="${case_dir}/logs/curl.payload.log"

  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_CONFIG_HOME="${case_dir}/config" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  MOCK_STORE_RUNNER_ID="/org-id/duplicate-runner-1" \
  "${launcher}" quick >/dev/null

  assert_contains "CREDIMI_RUNNER_ID=/org-id/duplicate-runner-1" "${env_file}"
  assert_contains '"runner_id":"/org-id/duplicate-runner-1"' "${curl_payload_log}"
}

run_linux_rejects_ios_types_case() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs"
  create_mocks "${case_dir}/mocks"

  if FAKE_UNAME_S="Linux" \
    FAKE_UNAME_M="x86_64" \
    PATH="${case_dir}/mocks:${PATH}" \
    HOME="${case_dir}/home" \
    XDG_BIN_HOME="${case_dir}/bin" \
    XDG_CONFIG_HOME="${case_dir}/config" \
    MOCK_LOG_DIR="${case_dir}/logs" \
    CREDIMI_URL="https://credimi.example" \
    TEMPORAL_ADDRESS="temporal.example:7233" \
    CREDIMI_RUNNER_NAME="linux-ios" \
    CREDIMI_INSTALL_AUTH_MODE="api_key" \
    CREDIMI_USER_API_KEY="user-api-key" \
    CREDIMI_INTERNAL_ADMIN_KEY="" \
    CREDIMI_RUNNER_TYPE="ios_simulator" \
    sh "${install_script}" >"${case_dir}/stdout.log" 2>"${case_dir}/stderr.log"; then
    fail "expected Linux install with ios_simulator to fail"
  fi

  assert_contains "runner type ios_simulator is not supported on Linux" "${case_dir}/stderr.log"
}

run_linux_usb_case
run_linux_usb_otel_disabled_case
run_linux_emulator_case
run_linux_wifi_case
run_linux_redroid_no_device_case
run_direct_container_case
run_darwin_case
run_linux_arm64_host_case
run_host_android_emulator_keeps_golden_path_case
run_noninteractive_empty_optional_case
run_quick_mode_with_domain_case
run_literal_env_loading_case
run_host_bind_host_readiness_case
run_direct_host_case
run_existing_env_case
run_existing_env_mode_switch_case
run_existing_env_invalid_key_case
run_temp_dir_creation_case
run_launcher_preview_case
run_direct_preview_preserves_env_mode_case
run_admin_requires_internal_key_case
run_admin_requires_pb_credentials_case
run_admin_computes_runner_id_case
run_recompute_runner_id_case
run_existing_name_updates_by_default_case
run_existing_name_can_create_new_case
run_linux_rejects_ios_types_case

printf 'install.sh tests passed\n'
