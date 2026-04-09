#!/bin/sh
set -eu

REPO_OWNER="ForkbombEu"
REPO_NAME="credimi-runner"
PROJECT_NAME="credimi-runner"
DEFAULT_CREDIMI_URL="https://credimi.io"
DEFAULT_TEMPORAL_ADDRESS="temporal.credimi.io:7233"
DEFAULT_RUNNER_HOST="0.0.0.0"
DEFAULT_RUNNER_PORT="8050"
DEFAULT_RUNNER_CADDY_SITE=":80"
DEFAULT_CONTAINER_MODE="usb"
DEFAULT_PHONE_IMAGE="ghcr.io/forkbombeu/credimi-runner-phone:latest"
DEFAULT_EMULATOR_IMAGE="ghcr.io/forkbombeu/credimi-runner-emulator:latest"
DEFAULT_BASE_NAME="credimi"
DEFAULT_HOST_AVD_HOME_PATH="/srv/credimi/avd-home"
DEFAULT_HOST_AVD_GOLDEN_PATH="/srv/credimi/avd-golden"

tty_path=""
if [ -r /dev/tty ]; then
  tty_path="/dev/tty"
fi

supports_color() {
  [ -t 2 ] || return 1
  [ "${TERM:-}" != "dumb" ]
}

if supports_color; then
  c_reset="$(printf '\033[0m')"
  c_red="$(printf '\033[31m')"
  c_green="$(printf '\033[32m')"
  c_yellow="$(printf '\033[33m')"
  c_blue="$(printf '\033[34m')"
  c_bold="$(printf '\033[1m')"
else
  c_reset=""
  c_red=""
  c_green=""
  c_yellow=""
  c_blue=""
  c_bold=""
fi

say() {
  printf '%s%s%s\n' "${c_blue}" "$*" "${c_reset}" >&2
}

warn() {
  printf '%s%s%s\n' "${c_yellow}" "$*" "${c_reset}" >&2
}

success() {
  printf '%s%s%s\n' "${c_green}" "$*" "${c_reset}" >&2
}

die() {
  printf '%sError:%s %s\n' "${c_red}${c_bold}" "${c_reset}" "$*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

load_env_defaults() {
  env_file="$1"
  [ -f "$env_file" ] || return 0

  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      ""|\#*)
        continue
        ;;
      *=*)
        key="${line%%=*}"
        value="${line#*=}"
        current_value="$(printenv "$key" 2>/dev/null || true)"
        if [ -z "$current_value" ]; then
          export "$key=$value"
        fi
        ;;
    esac
  done <"$env_file"
}

env_file_has_key() {
  env_file="$1"
  key="$2"
  if command -v rg >/dev/null 2>&1; then
    rg -q "^${key}=" "$env_file"
  else
    grep -q "^${key}=" "$env_file"
  fi
}

path_has_dir() {
  target_dir="$1"
  printf '%s\n' ":$PATH:" | grep -Fq ":${target_dir}:"
}

append_env_if_missing() {
  env_file="$1"
  key="$2"
  value="$3"

  if [ ! -f "$env_file" ] || ! env_file_has_key "$env_file" "$key"; then
    printf '%s=%s\n' "$key" "$value" >>"$env_file"
    appended_env_keys="1"
  fi
}

read_secret_value() {
  answer=""
  backspace_char="$(printf '\b')"
  delete_char="$(printf '\177')"
  ctrl_c_char="$(printf '\003')"
  newline_char="$(printf '\n')"
  carriage_return_char="$(printf '\r')"
  old_stty="$(stty -g <"$tty_path")"

  stty -echo -icanon min 1 time 0 <"$tty_path"
  while :; do
    char="$(dd bs=1 count=1 2>/dev/null <"$tty_path" || true)"
    case "$char" in
      "$newline_char"|"$carriage_return_char")
        break
        ;;
      "$backspace_char"|"$delete_char")
        if [ -n "$answer" ]; then
          answer="${answer%?}"
          printf '\b \b' >"$tty_path"
        fi
        ;;
      "$ctrl_c_char")
        stty "$old_stty" <"$tty_path"
        printf '\n' >"$tty_path"
        exit 130
        ;;
      "")
        break
        ;;
      *)
        answer="${answer}${char}"
        printf '*' >"$tty_path"
        ;;
    esac
  done
  stty "$old_stty" <"$tty_path"
  printf '\n' >"$tty_path"
  printf '%s' "$answer"
}

prompt_value() {
  var_name="$1"
  label="$2"
  default_value="${3-}"
  secret="${4-0}"
  allow_empty="${5-0}"

  eval "is_set=\${${var_name}+x}"
  existing_value="$(printenv "$var_name" 2>/dev/null || true)"
  if [ -n "${is_set:-}" ] && { [ -n "$existing_value" ] || [ "$allow_empty" = "1" ]; }; then
    printf '%s' "$existing_value"
    return 0
  fi

  [ -n "$tty_path" ] || die "$var_name is required for non-interactive install"

  while :; do
    if [ -n "$default_value" ]; then
      printf '%s [%s]: ' "$label" "$default_value" >"$tty_path"
    else
      printf '%s: ' "$label" >"$tty_path"
    fi

    if [ "$secret" = "1" ]; then
      answer="$(read_secret_value)"
    else
      IFS= read -r answer <"$tty_path" || true
    fi

    if [ -z "$answer" ]; then
      answer="$default_value"
    fi

    if [ -n "$answer" ] || [ -n "$default_value" ] || [ "$allow_empty" = "1" ]; then
      printf '%s' "$answer"
      return 0
    fi
  done
}

prompt_choice() {
  var_name="$1"
  label="$2"
  default_value="$3"
  choices="$4"

  existing_value="$(printenv "$var_name" 2>/dev/null || true)"
  if [ -n "$existing_value" ]; then
    printf '%s' "$existing_value"
    return 0
  fi

  [ -n "$tty_path" ] || die "$var_name is required for non-interactive install"

  while :; do
    printf '%s [%s]: ' "$label" "$default_value" >"$tty_path"
    IFS= read -r answer <"$tty_path" || true
    if [ -z "$answer" ]; then
      answer="$default_value"
    fi
    for choice in $choices; do
      if [ "$answer" = "$choice" ]; then
        printf '%s' "$answer"
        return 0
      fi
    done
    printf 'Please enter one of: %s\n' "$choices" >"$tty_path"
  done
}

normalize_asset_name() {
  os_name="$(uname -s)"
  arch_name="$(uname -m)"

  case "$os_name" in
    Linux) os_part="Linux" ;;
    Darwin) os_part="Darwin" ;;
    *) die "unsupported operating system: $os_name" ;;
  esac

  case "$os_name:$arch_name" in
    Linux:x86_64|Linux:amd64|Darwin:x86_64|Darwin:amd64) arch_part="x86_64" ;;
    Linux:aarch64|Linux:arm64) arch_part="aarch64" ;;
    Darwin:arm64|Darwin:aarch64) arch_part="arm64" ;;
    *) die "unsupported architecture: $arch_name on $os_name" ;;
  esac

  printf '%s-%s-%s' "$PROJECT_NAME" "$os_part" "$arch_part"
}

default_service_backend() {
  case "$(uname -s)" in
    Darwin) printf 'host' ;;
    Linux) printf 'container' ;;
    *) die "unsupported operating system: $(uname -s)" ;;
  esac
}

write_compose_file() {
  compose_file="$1"
  runner_mode="${CREDIMI_CONTAINER_MODE:-${DEFAULT_CONTAINER_MODE}}"
  runner_image="${RUNNER_IMAGE:-${DEFAULT_PHONE_IMAGE}}"

  cat >"$compose_file" <<EOF
services:
EOF

  case "$runner_mode" in
    usb)
      cat >>"$compose_file" <<EOF
  runner:
    image: ${runner_image}
    restart: unless-stopped
    command:
      - --usb
    privileged: true
    env_file:
      - .env
    volumes:
      - /dev/bus/usb:/dev/bus/usb
      - adbkeys:/root/.android
    expose:
      - "8050"
    labels:
      caddy: "\${RUNNER_CADDY_SITE:-:80}"
      caddy.reverse_proxy: "{{upstreams 8050}}"
    networks:
      - ingress
EOF
      ;;
    emulator)
      cat >>"$compose_file" <<EOF
  runner:
    image: ${runner_image}
    restart: unless-stopped
    command:
      - --emulator
    env_file:
      - .env
    environment:
      BASE_NAME: "\${BASE_NAME:-${DEFAULT_BASE_NAME}}"
      GOLDEN_PATH: "\${GOLDEN_PATH:-/avd-golden/\${BASE_NAME:-${DEFAULT_BASE_NAME}}-golden}"
    devices:
      - /dev/kvm:/dev/kvm
    volumes:
      - \${ANDROID_KEYS_DIR}:/root/.android
      - \${HOST_AVD_HOME_PATH}:/avd-home
      - \${HOST_AVD_GOLDEN_PATH}:/avd-golden
    expose:
      - "8050"
    labels:
      caddy: "\${RUNNER_CADDY_SITE:-:80}"
      caddy.reverse_proxy: "{{upstreams 8050}}"
    networks:
      - ingress
EOF
      ;;
    no_device)
      cat >>"$compose_file" <<EOF
  runner:
    image: ${runner_image}
    restart: unless-stopped
    command:
      - --no-device
    env_file:
      - .env
    expose:
      - "8050"
    labels:
      caddy: "\${RUNNER_CADDY_SITE:-:80}"
      caddy.reverse_proxy: "{{upstreams 8050}}"
    networks:
      - ingress
EOF
      ;;
    *)
      die "unsupported CREDIMI_CONTAINER_MODE: $runner_mode"
      ;;
  esac

  cat >>"$compose_file" <<'EOF'

  runner_host:
    image: alpine:3.21
    restart: unless-stopped
    command:
      - /bin/sh
      - -c
      - "trap : TERM INT; while true; do sleep 3600; done"
    extra_hosts:
      - "host.docker.internal:host-gateway"
    labels:
      caddy: "${RUNNER_CADDY_SITE:-:80}"
      caddy.reverse_proxy: "host.docker.internal:${RUNNER_PORT:-8050}"
    networks:
      - ingress

  caddy:
    image: lucaslorentz/caddy-docker-proxy:2.9-alpine
    restart: unless-stopped
    environment:
      CADDY_INGRESS_NETWORKS: ${CADDY_INGRESS_NETWORKS:-credimi-runner-ingress}
    extra_hosts:
      - "host.docker.internal:host-gateway"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - caddy_data:/data
      - caddy_config:/config
    networks:
      - ingress

  tunnel:
    image: cloudflare/cloudflared:latest
    restart: unless-stopped
    command: tunnel --no-autoupdate --url http://caddy:80
    depends_on:
      - caddy
    networks:
      - ingress

  tunnel_named:
    image: cloudflare/cloudflared:latest
    restart: unless-stopped
    command: tunnel --no-autoupdate run
    environment:
      TUNNEL_TOKEN: ${CLOUDFLARE_TUNNEL_TOKEN:-}
    depends_on:
      - caddy
    networks:
      - ingress

networks:
  ingress:
    name: ${CADDY_INGRESS_NETWORKS:-credimi-runner-ingress}

volumes:
  adbkeys:
  caddy_data:
  caddy_config:
EOF
}

write_launcher() {
  launcher_path="$1"
  cat >"$launcher_path" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
config_home="${XDG_CONFIG_HOME:-${HOME}/.config}"
config_dir="${CREDIMI_RUNNER_CONFIG_DIR:-${config_home}/credimi/runner}"
env_file="${config_dir}/.env"
compose_file="${config_dir}/docker-compose.yaml"
bin_path="${script_dir}/credimi-runner"
backend="${CREDIMI_RUNNER_BACKEND:-container}"

load_env_file() {
  local path="$1"
  local line key value

  while IFS= read -r line || [[ -n "${line}" ]]; do
    case "${line}" in
      ''|\#*)
        continue
        ;;
      *=*)
        key="${line%%=*}"
        value="${line#*=}"
        export "${key}=${value}"
        ;;
    esac
  done <"${path}"
}

runner_ready_url() {
  local host="$1"
  local port="$2"

  case "${host}" in
    ''|0.0.0.0)
      printf 'http://127.0.0.1:%s/\n' "${port}"
      ;;
    '::'|'[::]')
      printf 'http://[::1]:%s/\n' "${port}"
      ;;
    *:*)
      printf 'http://[%s]:%s/\n' "${host}" "${port}"
      ;;
    *)
      printf 'http://%s:%s/\n' "${host}" "${port}"
      ;;
  esac
}

if [[ -f "${env_file}" ]]; then
  load_env_file "${env_file}"
fi

mode="${1:-${CREDIMI_SERVICE_MODE:-quick}}"
runner_host="${RUNNER_HOST:-0.0.0.0}"
runner_port="${RUNNER_PORT:-8050}"
runner_url="$(runner_ready_url "${runner_host}" "${runner_port}")"
backend="${CREDIMI_RUNNER_BACKEND:-${backend}}"
compose_services=(caddy)

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    printf 'missing required command: %s\n' "$1" >&2
    exit 1
  }
}

cleanup() {
  if [[ -n "${runner_pid:-}" ]] && kill -0 "${runner_pid}" >/dev/null 2>&1; then
    kill "${runner_pid}" >/dev/null 2>&1 || true
    wait "${runner_pid}" >/dev/null 2>&1 || true
  fi
  docker compose --env-file "${env_file}" -f "${compose_file}" stop "${compose_services[@]}" >/dev/null 2>&1 || true
  docker compose --env-file "${env_file}" -f "${compose_file}" rm -f "${compose_services[@]}" >/dev/null 2>&1 || true
}

wait_for_runner() {
  local attempt

  for attempt in $(seq 1 50); do
    if curl --silent --output /dev/null "${runner_url}" >/dev/null 2>&1; then
      return 0
    fi
    if ! kill -0 "${runner_pid}" >/dev/null 2>&1; then
      printf 'runner exited before becoming ready\n' >&2
      return 1
    fi
    sleep 0.2
  done

  printf 'runner did not become ready on %s\n' "${runner_url}" >&2
  return 1
}

[[ -f "${compose_file}" ]] || {
  printf 'missing compose file: %s\n' "${compose_file}" >&2
  exit 1
}
[[ -f "${env_file}" ]] || {
  printf 'missing env file: %s\n' "${env_file}" >&2
  exit 1
}

require_cmd docker
docker compose version >/dev/null 2>&1 || {
  printf 'docker compose is required\n' >&2
  exit 1
}

case "${backend}" in
  host)
    [[ -x "${bin_path}" ]] || {
      printf 'missing installed binary: %s\n' "${bin_path}" >&2
      exit 1
    }
    require_cmd curl
    compose_services=(runner_host caddy)
    ;;
  container)
    compose_services=(runner caddy)
    ;;
  *)
    printf 'invalid CREDIMI_RUNNER_BACKEND: %s\n' "${backend}" >&2
    exit 1
    ;;
esac

case "${mode}" in
  quick)
    compose_services+=(tunnel)
    ;;
  named)
    if [[ -z "${CLOUDFLARE_TUNNEL_TOKEN:-}" ]]; then
      printf 'CLOUDFLARE_TUNNEL_TOKEN is required in named mode\n' >&2
      exit 1
    fi
    if [[ -z "${RUNNER_DOMAIN:-}" ]]; then
      printf 'RUNNER_DOMAIN is required in named mode\n' >&2
      exit 1
    fi
    compose_services+=(tunnel_named)
    ;;
  down)
    docker compose --env-file "${env_file}" -f "${compose_file}" down --remove-orphans
    exit 0
    ;;
  *)
    printf 'usage: %s [quick|named|down]\n' "$(basename "$0")" >&2
    exit 1
    ;;
esac

trap cleanup EXIT INT TERM

if [[ "${backend}" == "host" ]]; then
  "${bin_path}" serve --host "${runner_host}" --port "${runner_port}" &
  runner_pid=$!

  wait_for_runner
fi

docker compose --env-file "${env_file}" -f "${compose_file}" up "${compose_services[@]}"
EOF
  chmod +x "$launcher_path"
}

write_env_file() {
  env_file="$1"
  cat >"$env_file" <<EOF
CREDIMI_URL=${CREDIMI_URL}
CREDIMI_RUNNER_ID=${CREDIMI_RUNNER_ID}
CREDIMI_USER_API_KEY=${CREDIMI_USER_API_KEY}
CREDIMI_PB_ADMIN=${CREDIMI_PB_ADMIN}
CREDIMI_PB_PASS=${CREDIMI_PB_PASS}
CREDIMI_INTERNAL_ADMIN_KEY=${CREDIMI_INTERNAL_ADMIN_KEY}
TEMPORAL_ADDRESS=${TEMPORAL_ADDRESS}
CREDIMI_RUNNER_BACKEND=${CREDIMI_RUNNER_BACKEND}
CREDIMI_CONTAINER_MODE=${CREDIMI_CONTAINER_MODE}
RUNNER_HOST=${RUNNER_HOST}
RUNNER_PORT=${RUNNER_PORT}
RUNNER_DOMAIN=${RUNNER_DOMAIN}
RUNNER_CADDY_SITE=${RUNNER_CADDY_SITE}
CLOUDFLARE_TUNNEL_TOKEN=${CLOUDFLARE_TUNNEL_TOKEN}
CREDIMI_SERVICE_MODE=${CREDIMI_SERVICE_MODE}
RUNNER_IMAGE=${RUNNER_IMAGE}
ANDROID_KEYS_DIR=${ANDROID_KEYS_DIR}
HOST_AVD_HOME_PATH=${HOST_AVD_HOME_PATH}
HOST_AVD_GOLDEN_PATH=${HOST_AVD_GOLDEN_PATH}
BASE_NAME=${BASE_NAME}
GOLDEN_PATH=${GOLDEN_PATH}
EOF
}

write_missing_env_values() {
  env_file="$1"
  appended_env_keys="0"

  append_env_if_missing "$env_file" "CREDIMI_RUNNER_BACKEND" "${CREDIMI_RUNNER_BACKEND}"
  append_env_if_missing "$env_file" "CREDIMI_CONTAINER_MODE" "${CREDIMI_CONTAINER_MODE}"
  append_env_if_missing "$env_file" "RUNNER_IMAGE" "${RUNNER_IMAGE}"
  append_env_if_missing "$env_file" "RUNNER_HOST" "${RUNNER_HOST}"
  append_env_if_missing "$env_file" "RUNNER_PORT" "${RUNNER_PORT}"
  append_env_if_missing "$env_file" "RUNNER_DOMAIN" "${RUNNER_DOMAIN}"
  append_env_if_missing "$env_file" "RUNNER_CADDY_SITE" "${RUNNER_CADDY_SITE}"
  append_env_if_missing "$env_file" "CLOUDFLARE_TUNNEL_TOKEN" "${CLOUDFLARE_TUNNEL_TOKEN}"
  append_env_if_missing "$env_file" "CREDIMI_SERVICE_MODE" "${CREDIMI_SERVICE_MODE}"
  append_env_if_missing "$env_file" "ANDROID_KEYS_DIR" "${ANDROID_KEYS_DIR}"
  append_env_if_missing "$env_file" "HOST_AVD_HOME_PATH" "${HOST_AVD_HOME_PATH}"
  append_env_if_missing "$env_file" "HOST_AVD_GOLDEN_PATH" "${HOST_AVD_GOLDEN_PATH}"
  append_env_if_missing "$env_file" "BASE_NAME" "${BASE_NAME}"
  append_env_if_missing "$env_file" "GOLDEN_PATH" "${GOLDEN_PATH}"
}

main() {
  need_cmd chmod
  need_cmd mkdir

  bin_dir="${CREDIMI_RUNNER_BIN_DIR:-${XDG_BIN_HOME:-${HOME}/.local/bin}}"
  config_home="${XDG_CONFIG_HOME:-${HOME}/.config}"
  config_dir="${CREDIMI_RUNNER_CONFIG_DIR:-${config_home}/credimi/runner}"
  binary_path="${bin_dir}/${PROJECT_NAME}"
  launcher_path="${bin_dir}/${PROJECT_NAME}-service"
  compose_file="${config_dir}/docker-compose.yaml"
  env_file="${config_dir}/.env"
  existing_env="0"
  if [ -f "$env_file" ]; then
    existing_env="1"
    load_env_defaults "$env_file"
  fi
  CREDIMI_RUNNER_BACKEND="${CREDIMI_RUNNER_BACKEND:-$(default_service_backend)}"
  CREDIMI_CONTAINER_MODE="${CREDIMI_CONTAINER_MODE-}"
  RUNNER_IMAGE="${RUNNER_IMAGE-}"
  ANDROID_KEYS_DIR="${ANDROID_KEYS_DIR-}"
  HOST_AVD_HOME_PATH="${HOST_AVD_HOME_PATH-}"
  HOST_AVD_GOLDEN_PATH="${HOST_AVD_GOLDEN_PATH-}"
  BASE_NAME="${BASE_NAME-}"
  GOLDEN_PATH="${GOLDEN_PATH-}"

  case "$CREDIMI_RUNNER_BACKEND" in
    host)
      need_cmd curl
      asset_name="$(normalize_asset_name)"
      binary_url="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/latest/download/${asset_name}"
      say "Installing ${PROJECT_NAME} for ${asset_name}"
      ;;
    container)
      say "Installing ${PROJECT_NAME} service using the published runner container"
      ;;
    *)
      die "unsupported CREDIMI_RUNNER_BACKEND: $CREDIMI_RUNNER_BACKEND"
      ;;
  esac

  if [ "$existing_env" = "1" ]; then
    warn "Existing configuration found at ${env_file}; keeping it unchanged."
    CREDIMI_URL="${CREDIMI_URL:-${DEFAULT_CREDIMI_URL}}"
    TEMPORAL_ADDRESS="${TEMPORAL_ADDRESS:-${DEFAULT_TEMPORAL_ADDRESS}}"
    RUNNER_HOST="${RUNNER_HOST:-${DEFAULT_RUNNER_HOST}}"
    RUNNER_PORT="${RUNNER_PORT:-${DEFAULT_RUNNER_PORT}}"
    RUNNER_CADDY_SITE="${RUNNER_CADDY_SITE:-${DEFAULT_RUNNER_CADDY_SITE}}"
    CREDIMI_SERVICE_MODE="${CREDIMI_SERVICE_MODE:-quick}"

    CREDIMI_RUNNER_ID="${CREDIMI_RUNNER_ID:-}"
    [ -n "$CREDIMI_RUNNER_ID" ] || die "existing config is missing CREDIMI_RUNNER_ID: ${env_file}"

    if [ -n "${CREDIMI_USER_API_KEY:-}" ]; then
      CREDIMI_PB_ADMIN=""
      CREDIMI_PB_PASS=""
    else
      CREDIMI_PB_ADMIN="${CREDIMI_PB_ADMIN:-}"
      CREDIMI_PB_PASS="${CREDIMI_PB_PASS:-}"
      [ -n "$CREDIMI_PB_ADMIN" ] || die "existing config is missing CREDIMI_PB_ADMIN: ${env_file}"
      [ -n "$CREDIMI_PB_PASS" ] || die "existing config is missing CREDIMI_PB_PASS: ${env_file}"
      CREDIMI_USER_API_KEY=""
    fi

    CREDIMI_INTERNAL_ADMIN_KEY="${CREDIMI_INTERNAL_ADMIN_KEY:-}"
    if [ "$CREDIMI_SERVICE_MODE" = "named" ]; then
      RUNNER_DOMAIN="${RUNNER_DOMAIN:-}"
      CLOUDFLARE_TUNNEL_TOKEN="${CLOUDFLARE_TUNNEL_TOKEN:-}"
      [ -n "$RUNNER_DOMAIN" ] || die "existing config is missing RUNNER_DOMAIN for named mode: ${env_file}"
      [ -n "$CLOUDFLARE_TUNNEL_TOKEN" ] || die "existing config is missing CLOUDFLARE_TUNNEL_TOKEN for named mode: ${env_file}"
    else
      RUNNER_DOMAIN=""
      CLOUDFLARE_TUNNEL_TOKEN=""
    fi
  else
    CREDIMI_URL="$(prompt_value CREDIMI_URL "Credimi API URL" "${DEFAULT_CREDIMI_URL}")"
    TEMPORAL_ADDRESS="$(prompt_value TEMPORAL_ADDRESS "Temporal address" "${DEFAULT_TEMPORAL_ADDRESS}")"
    CREDIMI_RUNNER_ID="$(prompt_value CREDIMI_RUNNER_ID "Runner ID")"
    auth_mode="$(prompt_choice CREDIMI_INSTALL_AUTH_MODE "Auth mode (api_key/admin)" "api_key" "api_key admin")"

    if [ "$auth_mode" = "api_key" ]; then
      CREDIMI_USER_API_KEY="$(prompt_value CREDIMI_USER_API_KEY "Credimi user API key" "" 1)"
      CREDIMI_PB_ADMIN=""
      CREDIMI_PB_PASS=""
    else
      CREDIMI_PB_ADMIN="$(prompt_value CREDIMI_PB_ADMIN "Credimi admin email")"
      CREDIMI_PB_PASS="$(prompt_value CREDIMI_PB_PASS "Credimi admin password" "" 1)"
      CREDIMI_USER_API_KEY=""
    fi

    CREDIMI_INTERNAL_ADMIN_KEY="$(prompt_value CREDIMI_INTERNAL_ADMIN_KEY "Internal admin key (optional)" "" 1 1)"
    CREDIMI_SERVICE_MODE="$(prompt_choice CREDIMI_SERVICE_MODE "Tunnel mode (quick/named)" "quick" "quick named")"
    RUNNER_HOST="$(prompt_value RUNNER_HOST "Runner bind host" "${DEFAULT_RUNNER_HOST}")"
    RUNNER_PORT="$(prompt_value RUNNER_PORT "Runner port" "${DEFAULT_RUNNER_PORT}")"
    RUNNER_CADDY_SITE="$(prompt_value RUNNER_CADDY_SITE "Caddy listen site" "${DEFAULT_RUNNER_CADDY_SITE}")"

    if [ "$CREDIMI_SERVICE_MODE" = "named" ]; then
      RUNNER_DOMAIN="$(prompt_value RUNNER_DOMAIN "Public runner domain")"
      CLOUDFLARE_TUNNEL_TOKEN="$(prompt_value CLOUDFLARE_TUNNEL_TOKEN "Cloudflare tunnel token" "" 1)"
    else
      RUNNER_DOMAIN=""
      CLOUDFLARE_TUNNEL_TOKEN=""
    fi
  fi

  if [ "$CREDIMI_RUNNER_BACKEND" = "container" ]; then
    CREDIMI_CONTAINER_MODE="$(prompt_choice CREDIMI_CONTAINER_MODE "Linux runner mode (usb/emulator/no_device)" "${DEFAULT_CONTAINER_MODE}" "usb emulator no_device")"

    case "$CREDIMI_CONTAINER_MODE" in
      usb)
        RUNNER_IMAGE="${RUNNER_IMAGE:-${DEFAULT_PHONE_IMAGE}}"
        ANDROID_KEYS_DIR=""
        HOST_AVD_HOME_PATH=""
        HOST_AVD_GOLDEN_PATH=""
        BASE_NAME=""
        GOLDEN_PATH=""
        ;;
      emulator)
        RUNNER_IMAGE="${RUNNER_IMAGE:-${DEFAULT_EMULATOR_IMAGE}}"
        ANDROID_KEYS_DIR="$(prompt_value ANDROID_KEYS_DIR "ADB keys directory" "${HOME}/.android")"
        HOST_AVD_HOME_PATH="$(prompt_value HOST_AVD_HOME_PATH "Host AVD home path" "${DEFAULT_HOST_AVD_HOME_PATH}")"
        HOST_AVD_GOLDEN_PATH="$(prompt_value HOST_AVD_GOLDEN_PATH "Host golden assets path (parent or extracted dir)" "${DEFAULT_HOST_AVD_GOLDEN_PATH}")"
        BASE_NAME="$(prompt_value BASE_NAME "Emulator base name" "${DEFAULT_BASE_NAME}")"
        GOLDEN_PATH="$(prompt_value GOLDEN_PATH "Container golden path" "/avd-golden/${BASE_NAME}-golden")"
        ;;
      no_device)
        RUNNER_IMAGE="${RUNNER_IMAGE:-${DEFAULT_PHONE_IMAGE}}"
        ANDROID_KEYS_DIR=""
        HOST_AVD_HOME_PATH=""
        HOST_AVD_GOLDEN_PATH=""
        BASE_NAME=""
        GOLDEN_PATH=""
        ;;
    esac
  else
    CREDIMI_CONTAINER_MODE=""
    ANDROID_KEYS_DIR=""
    HOST_AVD_HOME_PATH=""
    HOST_AVD_GOLDEN_PATH=""
    BASE_NAME=""
    GOLDEN_PATH=""
  fi

  mkdir -p "$bin_dir" "$config_dir"

  if [ "$CREDIMI_RUNNER_BACKEND" = "host" ]; then
    say "Downloading ${binary_url}"
    curl -fsSL "$binary_url" -o "$binary_path"
    chmod +x "$binary_path"
  fi

  write_compose_file "$compose_file"
  write_launcher "$launcher_path"
  if [ "$existing_env" = "0" ]; then
    write_env_file "$env_file"
  else
    write_missing_env_values "$env_file"
  fi

  say ""
  success "Installed:"
  if [ "$CREDIMI_RUNNER_BACKEND" = "host" ]; then
    say "- ${binary_path}"
  fi
  say "- ${launcher_path}"
  say "- ${compose_file}"
  say "- ${env_file}"
  say ""
  if ! path_has_dir "$bin_dir"; then
    if [ "$bin_dir" = "${HOME}/.local/bin" ]; then
      say "~/.local/bin is not in PATH for this shell."
      say "Add it before running ${PROJECT_NAME}-service:"
      say "export PATH=\"\$HOME/.local/bin:\$PATH\""
    else
      say "${bin_dir} is not in PATH for this shell."
      say "Add it before running ${PROJECT_NAME}-service:"
      say "export PATH=\"${bin_dir}:\$PATH\""
    fi
    say ""
  fi
  say "Before starting the service, make sure Docker is installed and the daemon is running."
  if [ "$CREDIMI_RUNNER_BACKEND" = "container" ]; then
    say "This install uses the published runner container and does not start a local ${PROJECT_NAME} binary."
    case "$CREDIMI_CONTAINER_MODE" in
      usb)
        say "Configured Linux runner mode: USB phone."
        ;;
      emulator)
        say "Configured Linux runner mode: Android emulator."
        ;;
      no_device)
        say "Configured Linux runner mode: no-device."
        ;;
    esac
  fi
  if [ "$existing_env" = "1" ]; then
    say "Reused existing configuration from ${env_file}."
    if [ "${appended_env_keys:-0}" = "1" ]; then
      say "Appended missing runtime settings to ${env_file}."
    fi
  fi
  say ""
  say "Start the service with:"
  say "${PROJECT_NAME}-service"
  say ""
  say "Other commands:"
  say "${PROJECT_NAME}-service quick"
  say "${PROJECT_NAME}-service named"
  say "${PROJECT_NAME}-service down"
}

main "$@"
