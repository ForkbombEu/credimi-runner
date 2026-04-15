#!/usr/bin/env bash

set -euo pipefail

mode="${1:-quick}"

find_env_file() {
  if [[ -f .env ]]; then
    printf '.env\n'
    return 0
  fi

  local config_home="${XDG_CONFIG_HOME:-${HOME}/.config}"
  local config_env="${config_home}/credimi/runner/.env"
  if [[ -f "${config_env}" ]]; then
    printf '%s\n' "${config_env}"
    return 0
  fi

  return 1
}

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

if env_file="$(find_env_file)"; then
  load_env_file "${env_file}"
fi

bin_path="${BIN_PATH:-./bin/credimi-runner}"
runner_host="${RUNNER_HOST:-0.0.0.0}"
runner_port="${RUNNER_PORT:-8050}"
runner_domain="${RUNNER_DOMAIN:-}"
cloudflare_token="${CLOUDFLARE_TUNNEL_TOKEN:-}"
compose_services=(runner_host caddy)

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    printf 'missing required command: %s\n' "$1" >&2
    exit 1
  fi
}

cleanup() {
  if [[ -n "${runner_pid:-}" ]] && kill -0 "${runner_pid}" >/dev/null 2>&1; then
    kill "${runner_pid}" >/dev/null 2>&1 || true
    wait "${runner_pid}" >/dev/null 2>&1 || true
  fi

  docker compose stop "${compose_services[@]}" >/dev/null 2>&1 || true
  docker compose rm -f "${compose_services[@]}" >/dev/null 2>&1 || true
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

require_cmd "${bin_path}"
require_cmd docker
require_cmd curl

if [[ "${mode}" == "named" ]] && [[ -z "${cloudflare_token}" ]]; then
  printf 'CLOUDFLARE_TUNNEL_TOKEN is required for named tunnels\n' >&2
  exit 1
fi

if [[ "${mode}" == "quick" ]] && [[ -n "${runner_domain}" ]]; then
  printf 'RUNNER_DOMAIN should be empty for quick tunnels so docs stay same-origin\n' >&2
  exit 1
fi

if [[ "${mode}" == "named" ]]; then
  compose_services+=(tunnel_named)
else
  compose_services+=(tunnel)
fi

trap cleanup EXIT INT TERM

"${bin_path}" serve --host "${runner_host}" --port "${runner_port}" &
runner_pid=$!

wait_for_runner

docker compose up "${compose_services[@]}"
