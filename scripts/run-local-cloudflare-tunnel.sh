#!/usr/bin/env bash

set -euo pipefail

mode="${1:-quick}"

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi

bin_path="${BIN_PATH:-./bin/credimi-runner}"
runner_host="${RUNNER_HOST:-127.0.0.1}"
runner_port="${RUNNER_PORT:-8050}"
runner_domain="${RUNNER_DOMAIN:-}"
cloudflare_token="${CLOUDFLARE_TUNNEL_TOKEN:-}"

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
}

wait_for_runner() {
  local attempt

  for attempt in $(seq 1 50); do
    if curl --silent --output /dev/null "http://${runner_host}:${runner_port}/" >/dev/null 2>&1; then
      return 0
    fi

    if ! kill -0 "${runner_pid}" >/dev/null 2>&1; then
      printf 'runner exited before becoming ready\n' >&2
      return 1
    fi

    sleep 0.2
  done

  printf 'runner did not become ready on http://%s:%s/\n' "${runner_host}" "${runner_port}" >&2
  return 1
}

require_cmd "${bin_path}"
require_cmd cloudflared
require_cmd curl

if [[ "${mode}" == "named" ]] && [[ -z "${cloudflare_token}" ]]; then
  printf 'CLOUDFLARE_TUNNEL_TOKEN is required for named tunnels\n' >&2
  exit 1
fi

if [[ "${mode}" == "quick" ]] && [[ -n "${runner_domain}" ]]; then
  printf 'RUNNER_DOMAIN should be empty for quick tunnels so docs stay same-origin\n' >&2
  exit 1
fi

trap cleanup EXIT INT TERM

"${bin_path}" serve --host "${runner_host}" --port "${runner_port}" &
runner_pid=$!

wait_for_runner

if [[ "${mode}" == "named" ]]; then
  TUNNEL_TOKEN="${cloudflare_token}" cloudflared tunnel --no-autoupdate run
  exit $?
fi

cloudflared tunnel --no-autoupdate --url "http://${runner_host}:${runner_port}"
