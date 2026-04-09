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

tty_path=""
if [ -r /dev/tty ]; then
  tty_path="/dev/tty"
fi

say() {
  printf '%s\n' "$*" >&2
}

die() {
  say "Error: $*"
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

prompt_value() {
  var_name="$1"
  label="$2"
  default_value="${3-}"
  secret="${4-0}"

  existing_value="$(printenv "$var_name" 2>/dev/null || true)"
  if [ -n "$existing_value" ]; then
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
      stty -echo <"$tty_path"
      IFS= read -r answer <"$tty_path" || true
      stty echo <"$tty_path"
      printf '\n' >"$tty_path"
    else
      IFS= read -r answer <"$tty_path" || true
    fi

    if [ -z "$answer" ]; then
      answer="$default_value"
    fi

    if [ -n "$answer" ] || [ -n "$default_value" ]; then
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

write_compose_file() {
  compose_file="$1"
  cat >"$compose_file" <<'EOF'
services:
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

if [[ -f "${env_file}" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "${env_file}"
  set +a
fi

mode="${1:-${CREDIMI_SERVICE_MODE:-quick}}"
runner_host="${RUNNER_HOST:-0.0.0.0}"
runner_port="${RUNNER_PORT:-8050}"
compose_services=(runner_host caddy)

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
    if curl --silent --output /dev/null "http://127.0.0.1:${runner_port}/" >/dev/null 2>&1; then
      return 0
    fi
    if ! kill -0 "${runner_pid}" >/dev/null 2>&1; then
      printf 'runner exited before becoming ready\n' >&2
      return 1
    fi
    sleep 0.2
  done

  printf 'runner did not become ready on http://127.0.0.1:%s/\n' "${runner_port}" >&2
  return 1
}

[[ -x "${bin_path}" ]] || {
  printf 'missing installed binary: %s\n' "${bin_path}" >&2
  exit 1
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
require_cmd curl
docker compose version >/dev/null 2>&1 || {
  printf 'docker compose is required\n' >&2
  exit 1
}

case "${mode}" in
  quick)
    if [[ -n "${RUNNER_DOMAIN:-}" ]]; then
      printf 'RUNNER_DOMAIN must be empty in quick mode\n' >&2
      exit 1
    fi
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

"${bin_path}" serve --host "${runner_host}" --port "${runner_port}" &
runner_pid=$!

wait_for_runner

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
RUNNER_HOST=${RUNNER_HOST}
RUNNER_PORT=${RUNNER_PORT}
RUNNER_DOMAIN=${RUNNER_DOMAIN}
RUNNER_CADDY_SITE=${RUNNER_CADDY_SITE}
CLOUDFLARE_TUNNEL_TOKEN=${CLOUDFLARE_TUNNEL_TOKEN}
CREDIMI_SERVICE_MODE=${CREDIMI_SERVICE_MODE}
EOF
}

main() {
  need_cmd curl
  need_cmd chmod
  need_cmd mkdir

  bin_dir="${CREDIMI_RUNNER_BIN_DIR:-${XDG_BIN_HOME:-${HOME}/.local/bin}}"
  config_home="${XDG_CONFIG_HOME:-${HOME}/.config}"
  config_dir="${CREDIMI_RUNNER_CONFIG_DIR:-${config_home}/credimi/runner}"
  binary_path="${bin_dir}/${PROJECT_NAME}"
  launcher_path="${bin_dir}/${PROJECT_NAME}-service"
  compose_file="${config_dir}/docker-compose.yaml"
  env_file="${config_dir}/.env"
  asset_name="$(normalize_asset_name)"
  binary_url="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/latest/download/${asset_name}"

  say "Installing ${PROJECT_NAME} for ${asset_name}"

  CREDIMI_URL="$(prompt_value CREDIMI_URL "Credimi API URL" "${DEFAULT_CREDIMI_URL}")"
  TEMPORAL_ADDRESS="$(prompt_value TEMPORAL_ADDRESS "Temporal address" "${DEFAULT_TEMPORAL_ADDRESS}")"
  CREDIMI_RUNNER_ID="$(prompt_value CREDIMI_RUNNER_ID "Runner ID")"
  auth_mode="$(prompt_choice CREDIMI_INSTALL_AUTH_MODE "Auth mode (api_key/admin)" "api_key" "api_key admin")"

  CREDIMI_USER_API_KEY=""
  CREDIMI_PB_ADMIN=""
  CREDIMI_PB_PASS=""
  if [ "$auth_mode" = "api_key" ]; then
    CREDIMI_USER_API_KEY="$(prompt_value CREDIMI_USER_API_KEY "Credimi user API key" "" 1)"
  else
    CREDIMI_PB_ADMIN="$(prompt_value CREDIMI_PB_ADMIN "Credimi admin email")"
    CREDIMI_PB_PASS="$(prompt_value CREDIMI_PB_PASS "Credimi admin password" "" 1)"
  fi

  CREDIMI_INTERNAL_ADMIN_KEY="$(prompt_value CREDIMI_INTERNAL_ADMIN_KEY "Internal admin key (optional)" "" 1)"
  CREDIMI_SERVICE_MODE="$(prompt_choice CREDIMI_SERVICE_MODE "Tunnel mode (quick/named)" "quick" "quick named")"
  RUNNER_HOST="$(prompt_value RUNNER_HOST "Runner bind host" "${DEFAULT_RUNNER_HOST}")"
  RUNNER_PORT="$(prompt_value RUNNER_PORT "Runner port" "${DEFAULT_RUNNER_PORT}")"
  RUNNER_CADDY_SITE="$(prompt_value RUNNER_CADDY_SITE "Caddy listen site" "${DEFAULT_RUNNER_CADDY_SITE}")"

  RUNNER_DOMAIN=""
  CLOUDFLARE_TUNNEL_TOKEN=""
  if [ "$CREDIMI_SERVICE_MODE" = "named" ]; then
    RUNNER_DOMAIN="$(prompt_value RUNNER_DOMAIN "Public runner domain")"
    CLOUDFLARE_TUNNEL_TOKEN="$(prompt_value CLOUDFLARE_TUNNEL_TOKEN "Cloudflare tunnel token" "" 1)"
  fi

  mkdir -p "$bin_dir" "$config_dir"

  say "Downloading ${binary_url}"
  curl -fsSL "$binary_url" -o "$binary_path"
  chmod +x "$binary_path"

  write_compose_file "$compose_file"
  write_launcher "$launcher_path"
  write_env_file "$env_file"

  say ""
  say "Installed:"
  say "- ${binary_path}"
  say "- ${launcher_path}"
  say "- ${compose_file}"
  say "- ${env_file}"
  say ""
  if ! echo ":$PATH:" | grep -q ":$bin_dir:"; then
    say "Add ${bin_dir} to PATH if needed:"
    say "export PATH=\"${bin_dir}:\$PATH\""
    say ""
  fi
  say "Before starting the service, make sure Docker is installed and the daemon is running."
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
