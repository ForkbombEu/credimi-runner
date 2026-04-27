#!/bin/sh
set -eu

REPO_OWNER="ForkbombEu"
REPO_NAME="credimi-runner"
PROJECT_NAME="credimi-runner"
DEFAULT_CREDIMI_URL="https://credimi.io"
DEFAULT_CREDIMI_TEMP_DIR="/tmp/credimi-runner-tmp"
DEFAULT_TEMPORAL_ADDRESS="temporal.credimi.io:7233"
DEFAULT_OTEL_EXPORTER_OTLP_ENDPOINT="https://otel-collector.credimi.io"
DEFAULT_OTEL_SERVICE_NAME="credimi-runner"
DEFAULT_RUNNER_HOST="0.0.0.0"
DEFAULT_RUNNER_PORT="8050"
DEFAULT_RUNNER_CADDY_SITE=":80"
DEFAULT_CONTAINER_MODE="usb"
DEFAULT_PHONE_IMAGE="ghcr.io/forkbombeu/credimi-runner-phone:latest"
DEFAULT_EMULATOR_IMAGE="ghcr.io/forkbombeu/credimi-runner-emulator:latest"
DEFAULT_BASE_NAME="credimi"
DEFAULT_HOST_AVD_HOME_PATH="/srv/credimi/avd-home"
DEFAULT_HOST_AVD_GOLDEN_PATH="/srv/credimi/avd-golden"
DEFAULT_ANDROID_WIFI_PORT="5555"
DEFAULT_REDROID_DATA_DIR="/home/credimi/redroid-data"
DEFAULT_REDROID_DATA_TAR="/home/credimi/redroid-data.tar"

tty_path=""
# stdin is often a pipe during `curl ... | sh`; use /dev/tty directly when it is available.
if [ -r /dev/tty ] && [ -w /dev/tty ] && ( : </dev/tty >/dev/tty ) 2>/dev/null; then
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

valid_env_key() {
  case "$1" in
    ''|[0-9]*|*[!A-Za-z0-9_]*)
      return 1
      ;;
    *)
      return 0
      ;;
  esac
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
        valid_env_key "$key" || continue
        eval "INSTALL_DEFAULT_${key}=\$value"
        eval "INSTALL_DEFAULT_SET_${key}=1"
        ;;
    esac
  done <"$env_file"
}

env_var_is_set() {
  var_name="$1"
  printenv "$var_name" >/dev/null 2>&1
}

loaded_default_is_set() {
  var_name="$1"
  eval "[ \"\${INSTALL_DEFAULT_SET_${var_name}-}\" = \"1\" ]"
}

loaded_default_value() {
  var_name="$1"
  eval "printf '%s' \"\${INSTALL_DEFAULT_${var_name}-}\""
}

explicit_env_or_default() {
  var_name="$1"
  fallback_value="${2-}"

  if env_var_is_set "$var_name"; then
    eval "printf '%s' \"\${${var_name}}\""
    return 0
  fi

  printf '%s' "$fallback_value"
}

resolved_value() {
  var_name="$1"
  fallback_value="${2-}"

  if env_var_is_set "$var_name"; then
    eval "printf '%s' \"\${${var_name}}\""
    return 0
  fi

  if loaded_default_is_set "$var_name"; then
    loaded_default_value "$var_name"
    return 0
  fi

  printf '%s' "$fallback_value"
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

delete_env_key() {
  env_file="$1"
  key="$2"

  [ -f "$env_file" ] || return 0

  tmp_file="$(mktemp "$(dirname "$env_file")/.env.tmp.XXXXXX")"
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      "${key}="*)
        continue
        ;;
      *)
        printf '%s\n' "$line" >>"$tmp_file"
        ;;
    esac
  done <"$env_file"
  mv "$tmp_file" "$env_file"
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

  if env_var_is_set "$var_name"; then
    eval "existing_value=\${${var_name}}"
    printf '%s' "$existing_value"
    return 0
  fi

  if [ -z "$tty_path" ]; then
    if [ -n "$default_value" ] || [ "$allow_empty" = "1" ]; then
      printf '%s' "$default_value"
      return 0
    fi
    die "$var_name is required for non-interactive install"
  fi

  while :; do
    if [ -n "$default_value" ]; then
      if [ "$secret" = "1" ]; then
        printf '%s [%s]: ' "$label" "saved" >"$tty_path"
      else
        printf '%s [%s]: ' "$label" "$default_value" >"$tty_path"
      fi
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

  if env_var_is_set "$var_name"; then
    eval "existing_value=\${${var_name}}"
    printf '%s' "$existing_value"
    return 0
  fi

  if [ -z "$tty_path" ]; then
    printf '%s' "$default_value"
    return 0
  fi

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

default_runner_type() {
  backend="$1"
  saved_type="$(resolved_value CREDIMI_RUNNER_TYPE)"
  saved_mode="$(resolved_value CREDIMI_CONTAINER_MODE)"

  if [ -n "$saved_type" ]; then
    printf '%s' "$saved_type"
    return 0
  fi

  case "$saved_mode" in
    emulator)
      printf 'android_emulator'
      ;;
    usb|wifi)
      printf 'android_phone'
      ;;
    *)
      case "$(uname -s):$backend" in
        Darwin:host)
          printf 'ios_simulator'
          ;;
        *)
          printf 'android_phone'
          ;;
      esac
      ;;
  esac
}

runner_type_choices() {
  case "$(uname -s)" in
    Darwin)
      printf 'android_emulator ios_simulator ios_phone redroid android_phone'
      ;;
    Linux)
      printf 'android_emulator redroid android_phone'
      ;;
    *)
      die "unsupported operating system: $(uname -s)"
      ;;
  esac
}

validate_runner_type_supported() {
  runner_type="$1"

  case "$(uname -s):$runner_type" in
    Linux:ios_simulator|Linux:ios_phone)
      die "runner type ${runner_type} is not supported on Linux"
      ;;
  esac
}

default_android_device_mode() {
  runner_type="${1-}"
  saved_mode="$(resolved_value CREDIMI_RUNNER_DEVICE_MODE)"
  if [ -n "$saved_mode" ]; then
    case "$runner_type:$saved_mode" in
      redroid:no_device|redroid:usb|redroid:wifi|android_phone:usb|android_phone:wifi)
        printf '%s' "$saved_mode"
        return 0
        ;;
    esac
  fi

  if [ "$runner_type" = "redroid" ]; then
    printf 'no_device'
    return 0
  fi

  case "$(resolved_value CREDIMI_CONTAINER_MODE)" in
    wifi)
      printf 'wifi'
      ;;
    *)
      printf 'usb'
      ;;
  esac
}

default_yes_no_choice() {
  var_name="$1"
  fallback="${2:-no}"
  value="$(resolved_value "$var_name")"

  case "$value" in
    1|true|TRUE|True|yes|YES|Yes|on|ON|On)
      printf 'yes'
      ;;
    0|false|FALSE|False|no|NO|No|off|OFF|Off)
      printf 'no'
      ;;
    *)
      printf '%s' "$fallback"
      ;;
  esac
}

default_avdctl_ssh_choice() {
  if [ -n "$(resolved_value AVDCTL_SSH_TARGET)" ]; then
    printf 'yes'
    return 0
  fi

  printf 'no'
}

default_avdctl_sudo_choice() {
  if [ -n "$(resolved_value AVDCTL_SUDO_PASSWORD)" ]; then
    printf 'yes'
    return 0
  fi

  printf '%s' "$(default_yes_no_choice AVDCTL_SUDO no)"
}

detect_connected_android_usb_serial() {
  adb_output="$(adb devices -l 2>/dev/null || true)"
  serials="$(
    printf '%s\n' "$adb_output" |
      awk 'NR > 1 && $2 == "device" && $1 !~ /:/ { print $1 }'
  )"
  serial_count="$(printf '%s\n' "$serials" | awk 'NF { count++ } END { print count + 0 }')"

  if [ "$serial_count" = "1" ]; then
    printf '%s' "$(printf '%s\n' "$serials" | awk 'NF { print; exit }')"
    return 0
  fi

  return 1
}

runner_name_from_id() {
  runner_id="${1#/}"
  case "$runner_id" in
    */*)
      printf '%s' "${runner_id##*/}"
      ;;
    *)
      printf '%s' "$runner_id"
      ;;
  esac
}

runner_org_from_id() {
  runner_id="${1#/}"
  case "$runner_id" in
    */*)
      printf '%s' "${runner_id%%/*}"
      ;;
    *)
      printf ''
      ;;
  esac
}

canonify_plain() {
  value="$1"
  slug="$(
    printf '%s' "$value" |
      tr '[:upper:]' '[:lower:]' |
      sed 's/[^a-z0-9][^a-z0-9]*/-/g; s/^-//; s/-$//'
  )"

  if [ -z "$slug" ]; then
    printf 'item-name'
    return 0
  fi

  printf '%s' "$slug"
}

json_escape() {
  printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

extract_json_string() {
  key="$1"
  body="$2"

  printf '%s' "$body" |
    tr -d '\r\n' |
    sed -n "s/.*\"${key}\"[[:space:]]*:[[:space:]]*\"\\([^\"]*\\)\".*/\\1/p"
}

join_url() {
  base="$1"
  shift

  base="${base%/}"
  printf '%s' "$base"
  for part in "$@"; do
    part="${part#/}"
    part="${part%/}"
    [ -n "$part" ] || continue
    printf '/%s' "$part"
  done
}

configure_install_auth_header() {
  if [ -n "${CREDIMI_USER_API_KEY:-}" ]; then
    CREDIMI_INSTALL_AUTH_HEADER_NAME="Credimi-Api-Key"
    CREDIMI_INSTALL_AUTH_HEADER_VALUE="$CREDIMI_USER_API_KEY"
    return 0
  fi

  if [ -n "${CREDIMI_INTERNAL_ADMIN_KEY:-}" ]; then
    CREDIMI_INSTALL_AUTH_HEADER_NAME="Credimi-Api-Key"
    CREDIMI_INSTALL_AUTH_HEADER_VALUE="$CREDIMI_INTERNAL_ADMIN_KEY"
    return 0
  fi

  die "missing Credimi credentials: set CREDIMI_USER_API_KEY or CREDIMI_INTERNAL_ADMIN_KEY"
}

install_get_json() {
  url="$1"
  body_file="$(mktemp)"
  status="$(
    curl \
      --silent \
      --show-error \
      --output "$body_file" \
      --write-out '%{http_code}' \
      -H "${CREDIMI_INSTALL_AUTH_HEADER_NAME}: ${CREDIMI_INSTALL_AUTH_HEADER_VALUE}" \
      "$url"
  )"
  body="$(cat "$body_file")"
  rm -f "$body_file"

  if [ "$status" -lt 200 ] || [ "$status" -ge 300 ]; then
    die "request to ${url} failed (${status}): ${body}"
  fi

  printf '%s' "$body"
}

install_post_json() {
  url="$1"
  payload="$2"
  body_file="$(mktemp)"
  status="$(
    curl \
      --silent \
      --show-error \
      --output "$body_file" \
      --write-out '%{http_code}' \
      -H 'Content-Type: application/json' \
      -H "${CREDIMI_INSTALL_AUTH_HEADER_NAME}: ${CREDIMI_INSTALL_AUTH_HEADER_VALUE}" \
      -X POST \
      --data "$payload" \
      "$url"
  )"
  body="$(cat "$body_file")"
  rm -f "$body_file"

  if [ "$status" -lt 200 ] || [ "$status" -ge 300 ]; then
    die "request to ${url} failed (${status}): ${body}"
  fi

  printf '%s' "$body"
}

resolve_user_runner_organization() {
  org_url="$(join_url "$CREDIMI_URL" api organizations my)"
  org_response="$(install_get_json "$org_url")"
  org_name="$(extract_json_string canonified_name "$org_response")"
  [ -n "$org_name" ] || die "failed to extract canonified_name from ${org_url}"

  printf '%s' "$org_name"
}

runner_name_conflict_action() {
  base_runner_id="$1"
  preview_runner_id="$2"

  if [ -z "$tty_path" ]; then
    action="$(resolved_value CREDIMI_RUNNER_NAME_CONFLICT_ACTION update)"
    case "$action" in
      update|create)
        printf '%s' "$action"
        return 0
        ;;
      *)
        die "unsupported CREDIMI_RUNNER_NAME_CONFLICT_ACTION value: ${action}"
        ;;
    esac
  fi

  while :; do
    printf 'Runner %s already exists. Update existing or create %s? (update/create) [update]: ' "$base_runner_id" "$preview_runner_id" >"$tty_path"
    IFS= read -r action <"$tty_path" || true
    [ -n "$action" ] || action="update"
    case "$action" in
      update|create)
        printf '%s' "$action"
        return 0
        ;;
      *)
        printf 'Please enter one of: update create\n' >"$tty_path"
        ;;
    esac
  done
}

resolve_install_runner_identity() {
  [ -n "${CREDIMI_RUNNER_ID:-}" ] && return 0
  [ -n "${CREDIMI_RUNNER_NAME:-}" ] || die "CREDIMI_RUNNER_NAME is required when CREDIMI_RUNNER_ID is not set"

  need_cmd curl
  configure_install_auth_header

  if [ -z "${CREDIMI_RUNNER_ORGANIZATION:-}" ]; then
    if [ -n "${CREDIMI_USER_API_KEY:-}" ]; then
      CREDIMI_RUNNER_ORGANIZATION="$(resolve_user_runner_organization)"
    else
      die "CREDIMI_RUNNER_ORGANIZATION is required when CREDIMI_RUNNER_ID is not set"
    fi
  fi

  runner_slug="$(canonify_plain "$CREDIMI_RUNNER_NAME")"
  base_runner_id="/${CREDIMI_RUNNER_ORGANIZATION}/${runner_slug}"
  preview_payload="{\"name\":\"$(json_escape "$CREDIMI_RUNNER_NAME")\""
  if [ -n "${CREDIMI_RUNNER_ORGANIZATION:-}" ]; then
    preview_payload="${preview_payload},\"organization\":\"$(json_escape "$CREDIMI_RUNNER_ORGANIZATION")\""
  fi
  preview_payload="${preview_payload}}"
  preview_url="$(join_url "$CREDIMI_URL" api mobile-runner preview-id)"
  preview_response="$(install_post_json "$preview_url" "$preview_payload")"
  preview_runner_id="$(extract_json_string runner_id "$preview_response")"
  [ -n "$preview_runner_id" ] || die "failed to extract runner_id from ${preview_url}"

  if [ "$preview_runner_id" = "$base_runner_id" ]; then
    CREDIMI_RUNNER_ID="$base_runner_id"
    return 0
  fi

  case "$(runner_name_conflict_action "$base_runner_id" "$preview_runner_id")" in
    update)
      CREDIMI_RUNNER_ID="$base_runner_id"
      ;;
    create)
      CREDIMI_RUNNER_ID="$preview_runner_id"
      ;;
  esac
}

default_otel_enabled_choice() {
  case "$(resolved_value OTEL_ENABLED)" in
    1|true|TRUE|True|yes|YES|Yes|on|ON|On)
      printf 'yes'
      return 0
      ;;
    0|false|FALSE|False|no|NO|No|off|OFF|Off)
      printf 'no'
      return 0
      ;;
  esac

  if [ -n "$(resolved_value OTEL_EXPORTER_OTLP_ENDPOINT)" ]; then
    printf 'yes'
    return 0
  fi

  printf 'yes'
}

write_compose_file() {
  compose_file="$1"
  runner_mode="${CREDIMI_CONTAINER_MODE:-${DEFAULT_CONTAINER_MODE}}"
  runner_image="${RUNNER_IMAGE:-${DEFAULT_PHONE_IMAGE}}"
  runner_ssh_known_hosts_volume=""
  runner_no_device_volumes_block=""
  runner_connectivity_block='    expose:
      - "8050"
    labels:
      caddy: "${RUNNER_CADDY_SITE:-:80}"
      caddy.reverse_proxy: "{{upstreams 8050}}"
    networks:
      - ingress'

  if [ "${CREDIMI_RUNNER_BACKEND:-}" = "container" ] &&
    [ "${CREDIMI_SERVICE_MODE:-quick}" = "direct" ] &&
    [ "$(uname -s)" = "Linux" ]; then
    runner_connectivity_block='    network_mode: host'
  fi

  if [ -n "${AVDCTL_SSH_TARGET:-}" ] && [ -n "${AVDCTL_SSH_KNOWN_HOSTS_PATH:-}" ]; then
    runner_ssh_known_hosts_volume='      - ${AVDCTL_SSH_KNOWN_HOSTS_PATH}:/root/.ssh/known_hosts:ro'
    runner_no_device_volumes_block='    volumes:
      - ${AVDCTL_SSH_KNOWN_HOSTS_PATH}:/root/.ssh/known_hosts:ro'
  fi

  cat >"$compose_file" <<EOF
services:
EOF

  case "$runner_mode" in
    wifi)
      cat >>"$compose_file" <<EOF
  runner:
    image: ${runner_image}
    restart: unless-stopped
    command:
      - "\${CREDIMI_RUNNER_WIFI_IP}:\${CREDIMI_RUNNER_WIFI_PORT:-${DEFAULT_ANDROID_WIFI_PORT}}"
    env_file:
      - .env
    environment:
      PORT: "\${RUNNER_PORT:-${DEFAULT_RUNNER_PORT}}"
    volumes:
      - adbkeys:/root/.android
${runner_ssh_known_hosts_volume}
${runner_connectivity_block}
EOF
      ;;
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
    environment:
      PORT: "\${RUNNER_PORT:-${DEFAULT_RUNNER_PORT}}"
    volumes:
      - /dev/bus/usb:/dev/bus/usb
      - adbkeys:/root/.android
${runner_ssh_known_hosts_volume}
${runner_connectivity_block}
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
      PORT: "\${RUNNER_PORT:-${DEFAULT_RUNNER_PORT}}"
      BASE_NAME: "\${BASE_NAME:-${DEFAULT_BASE_NAME}}"
      GOLDEN_PATH: "\${GOLDEN_PATH:-/avd-golden/\${BASE_NAME:-${DEFAULT_BASE_NAME}}-golden}"
    devices:
      - /dev/kvm:/dev/kvm
    volumes:
      - \${ANDROID_KEYS_DIR}:/root/.android
      - \${HOST_AVD_HOME_PATH}:/avd-home
      - \${HOST_AVD_GOLDEN_PATH}:/avd-golden
${runner_ssh_known_hosts_volume}
${runner_connectivity_block}
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
    environment:
      PORT: "\${RUNNER_PORT:-${DEFAULT_RUNNER_PORT}}"
${runner_no_device_volumes_block}
${runner_connectivity_block}
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
trap 'echo "ERROR at ${BASH_SOURCE}:${LINENO}: ${BASH_COMMAND}"' ERR

echo "WARNING: This script does NOT work if Docker is installed via snap. See: https://stackoverflow.com/questions/73290497/getting-docker-open-env-permission-denied-when-trying-to-pass-a-env-file. Install Docker via apt or the official shell script instead." >&2

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script_name="$(basename "${BASH_SOURCE[0]}")"
config_home="${XDG_CONFIG_HOME:-${HOME}/.config}"
config_dir="${CREDIMI_RUNNER_CONFIG_DIR:-${config_home}/credimi/runner}"
env_file="${config_dir}/.env"
compose_file="${config_dir}/docker-compose.yaml"
bin_path="${script_dir}/credimi-runner"
backend="${CREDIMI_RUNNER_BACKEND:-container}"
auth_headers=()

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

join_url() {
  local base="${1%/}"
  shift

  printf '%s' "${base}"
  for part in "$@"; do
    printf '/%s' "${part#/}"
  done
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

runner_name_from_id() {
  local runner_id="${1#/}"

  case "${runner_id}" in
    */*)
      printf '%s' "${runner_id##*/}"
      ;;
    *)
      printf '%s' "${runner_id}"
      ;;
  esac
}

runner_org_from_id() {
  local runner_id="${1#/}"

  case "${runner_id}" in
    */*)
      printf '%s' "${runner_id%%/*}"
      ;;
    *)
      printf ''
      ;;
  esac
}

canonify_plain() {
  local value="${1-}"
  local slug

  slug="$(
    printf '%s' "${value}" |
      tr '[:upper:]' '[:lower:]' |
      sed 's/[^a-z0-9][^a-z0-9]*/-/g; s/^-//; s/-$//'
  )"
  if [[ -z "${slug}" ]]; then
    printf 'item-name'
    return 0
  fi

  printf '%s' "${slug}"
}

json_escape() {
  local value="${1-}"

  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  value="${value//$'\r'/\\r}"
  value="${value//$'\n'/\\n}"

  printf '%s' "${value}"
}

extract_json_string() {
  local key="$1"
  local body="$2"

  printf '%s' "${body}" |
    tr -d '\r\n' |
    sed -n "s/.*\"${key}\"[[:space:]]*:[[:space:]]*\"\\([^\"]*\\)\".*/\\1/p"
}

normalize_public_url() {
  local value="${1:-}"

  case "${value}" in
    http://*|https://*)
      printf '%s' "${value}"
      ;;
    *)
      printf 'https://%s' "${value}"
      ;;
  esac
}

upsert_env_value() {
  local path="$1"
  local key="$2"
  local value="$3"
  local tmp_dir
  local tmp_file
  local found=0
  local line

  tmp_dir="$(dirname "${path}")"
  tmp_file="$(mktemp "${tmp_dir}/.env.tmp.XXXXXX")"
  if [[ -f "${path}" ]]; then
    cp -p "${path}" "${tmp_file}"
    : >"${tmp_file}"
    while IFS= read -r line || [[ -n "${line}" ]]; do
      if [[ "${line}" == "${key}="* ]]; then
        printf '%s=%s\n' "${key}" "${value}" >>"${tmp_file}"
        found=1
        continue
      fi
      printf '%s\n' "${line}" >>"${tmp_file}"
    done <"${path}"
  fi

  if [[ "${found}" == "0" ]]; then
    printf '%s=%s\n' "${key}" "${value}" >>"${tmp_file}"
  fi

  mv "${tmp_file}" "${path}"
}

authenticate_superuser() {
  local auth_url auth_payload response token

  auth_url="$(join_url "${CREDIMI_URL}" "api" "collections" "_superusers" "auth-with-password")"
  auth_payload="{\"identity\":\"$(json_escape "${CREDIMI_PB_ADMIN}")\",\"password\":\"$(json_escape "${CREDIMI_PB_PASS}")\"}"
  response="$(post_json "${auth_url}" "${auth_payload}")"
  token="$(extract_json_string "token" "${response}")"
  [[ -n "${token}" ]] || {
    printf 'failed to extract superuser token from %s\n' "${auth_url}" >&2
    return 1
  }

  printf '%s' "${token}"
}

configure_auth_headers() {
  if [[ -n "${CREDIMI_USER_API_KEY:-}" ]]; then
    auth_headers=(-H "Credimi-Api-Key: ${CREDIMI_USER_API_KEY}")
    return 0
  fi

  if [[ -n "${CREDIMI_INTERNAL_ADMIN_KEY:-}" ]]; then
    auth_headers=(-H "Credimi-Api-Key: ${CREDIMI_INTERNAL_ADMIN_KEY}")
    return 0
  fi

  if [[ -n "${CREDIMI_PB_ADMIN:-}" ]] && [[ -n "${CREDIMI_PB_PASS:-}" ]]; then
    auth_headers=(-H "Authorization: Bearer $(authenticate_superuser)")
    return 0
  fi

  printf 'missing Credimi credentials: set CREDIMI_USER_API_KEY, CREDIMI_INTERNAL_ADMIN_KEY, or CREDIMI_PB_ADMIN/CREDIMI_PB_PASS\n' >&2
  return 1
}

post_json() {
  local url="$1"
  local payload="$2"
  local body_file
  local status
  local body

  body_file="$(mktemp)"
  status="$(
    curl \
      --silent \
      --show-error \
      --output "${body_file}" \
      --write-out '%{http_code}' \
      -H 'Content-Type: application/json' \
      "${auth_headers[@]}" \
      -X POST \
      --data "${payload}" \
      "${url}"
  )"
  body="$(cat "${body_file}")"
  rm -f "${body_file}"

  if [[ "${status}" -lt 200 || "${status}" -ge 300 ]]; then
    printf 'request to %s failed (%s): %s\n' "${url}" "${status}" "${body}" >&2
    return 1
  fi

  printf '%s' "${body}"
}

get_json() {
  local url="$1"
  local body_file
  local status
  local body

  body_file="$(mktemp)"
  status="$(
    curl \
      --silent \
      --show-error \
      --output "${body_file}" \
      --write-out '%{http_code}' \
      "${auth_headers[@]}" \
      "${url}"
  )"
  body="$(cat "${body_file}")"
  rm -f "${body_file}"

  if [[ "${status}" -lt 200 || "${status}" -ge 300 ]]; then
    printf 'request to %s failed (%s): %s\n' "${url}" "${status}" "${body}" >&2
    return 1
  fi

  printf '%s' "${body}"
}

prompt_choice() {
  local var_name="$1"
  local label="$2"
  local default_value="$3"
  local choices="$4"
  local answer
  local choice

  answer=""
  if [[ -n "${!var_name:-}" ]]; then
    printf '%s' "${!var_name}"
    return 0
  fi

  if [[ -r /dev/tty && -w /dev/tty ]] && ( : </dev/tty >/dev/tty ) 2>/dev/null; then
    while :; do
      printf '%s [%s]: ' "${label}" "${default_value}" >/dev/tty
      IFS= read -r answer </dev/tty || true
      if [[ -z "${answer}" ]]; then
        answer="${default_value}"
      fi
      for choice in ${choices}; do
        if [[ "${answer}" == "${choice}" ]]; then
          printf '%s' "${answer}"
          return 0
        fi
      done
      printf 'Please enter one of: %s\n' "${choices}" >/dev/tty
    done
  fi

  printf '%s' "${default_value}"
}

resolve_user_runner_organization() {
  local org_url org_response org_name

  org_url="$(join_url "${CREDIMI_URL}" "api" "organizations" "my")"
  org_response="$(get_json "${org_url}")"
  org_name="$(extract_json_string "canonified_name" "${org_response}")"
  [[ -n "${org_name}" ]] || {
    printf 'failed to extract canonified_name from %s\n' "${org_url}" >&2
    return 1
  }

  printf '%s' "${org_name}"
}

resolve_runner_identity() {
  if [[ -z "${CREDIMI_RUNNER_ID:-}" ]]; then
    printf 'CREDIMI_RUNNER_ID is required; rerun install.sh to choose the runner identity\n' >&2
    return 1
  fi

  if [[ -z "${CREDIMI_RUNNER_NAME:-}" ]]; then
    CREDIMI_RUNNER_NAME="$(runner_name_from_id "${CREDIMI_RUNNER_ID}")"
  fi
  if [[ -z "${CREDIMI_RUNNER_ORGANIZATION:-}" ]]; then
    CREDIMI_RUNNER_ORGANIZATION="$(runner_org_from_id "${CREDIMI_RUNNER_ID}")"
  fi

  export CREDIMI_RUNNER_ID CREDIMI_RUNNER_ORGANIZATION
}

wait_for_public_runner_url() {
  local attempt
  local tunnel_logs
  local public_url

  if [[ "${mode}" == "named" ]]; then
    printf '%s' "$(normalize_public_url "${RUNNER_DOMAIN}")"
    return 0
  fi

  for attempt in $(seq 1 60); do
    tunnel_logs="$(
      docker compose --env-file "${env_file}" -f "${compose_file}" logs tunnel 2>/dev/null || true
    )"
    public_url="$(
      printf '%s\n' "${tunnel_logs}" |
        grep -Eo 'https://[-[:alnum:].]+trycloudflare.com' |
        tail -n 1
    )"
    if [[ -n "${public_url}" ]]; then
      printf '%s' "${public_url}"
      return 0
    fi

    if [[ -n "${runner_pid:-}" ]] && ! kill -0 "${runner_pid}" >/dev/null 2>&1; then
      printf 'runner exited before the public tunnel URL was available\n' >&2
      return 1
    fi

    sleep 1
  done

  printf 'failed to detect the public tunnel URL from cloudflared logs\n' >&2
  return 1
}

register_mobile_runner() {
  local runner_ip="$1"
  local runner_port="${2:-}"
  local store_url
  local store_payload
  local store_response
  local stored_runner_id

  [[ -n "${CREDIMI_RUNNER_NAME:-}" ]] || {
    printf 'CREDIMI_RUNNER_NAME is required to register the runner\n' >&2
    return 1
  }

  store_payload="{\"runner_id\":\"$(json_escape "${CREDIMI_RUNNER_ID}")\",\"name\":\"$(json_escape "${CREDIMI_RUNNER_NAME}")\",\"ip\":\"$(json_escape "${runner_ip}")\""
  if [[ -n "${CREDIMI_RUNNER_DESCRIPTION:-}" ]]; then
    store_payload+=",\"description\":\"$(json_escape "${CREDIMI_RUNNER_DESCRIPTION}")\""
  fi
  if [[ -n "${CREDIMI_RUNNER_TYPE:-}" ]]; then
    store_payload+=",\"type\":\"$(json_escape "${CREDIMI_RUNNER_TYPE}")\""
  fi
  if [[ -n "${runner_port}" ]]; then
    store_payload+=",\"port\":\"$(json_escape "${runner_port}")\""
  fi
  if [[ -n "${CREDIMI_RUNNER_SERIAL:-}" ]]; then
    store_payload+=",\"serial\":\"$(json_escape "${CREDIMI_RUNNER_SERIAL}")\""
  fi
  if [[ -n "${CREDIMI_RUNNER_ORGANIZATION:-}" ]]; then
    store_payload+=",\"organization\":\"$(json_escape "${CREDIMI_RUNNER_ORGANIZATION}")\""
  fi
  store_payload+="}"

  store_url="$(join_url "${CREDIMI_URL}" "api" "mobile-runner")"
  store_response="$(post_json "${store_url}" "${store_payload}")"
  stored_runner_id="$(extract_json_string "runner_id" "${store_response}")"
  if [[ -n "${stored_runner_id}" ]] && [[ "${stored_runner_id}" != "${CREDIMI_RUNNER_ID}" ]]; then
    printf 'stored runner_id mismatch: expected %s, got %s\n' "${CREDIMI_RUNNER_ID}" "${stored_runner_id}" >&2
    return 1
  fi
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

installed_runner_image_digest() {
  local image="$1"

  docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "${image}" 2>/dev/null |
    sed -n 's/.*@//p' |
    head -n 1
}

latest_runner_image_digest() {
  local image="$1"

  docker manifest inspect --verbose "${image}" 2>/dev/null |
    sed -n 's/.*"digest"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' |
    head -n 1
}

warn_if_runner_image_outdated() {
  local image="${RUNNER_IMAGE:-}"
  local installed_digest
  local latest_digest

  [[ "${backend}" == "container" ]] || return 0
  [[ -n "${image}" ]] || return 0

  installed_digest="$(installed_runner_image_digest "${image}")"
  [[ -n "${installed_digest}" ]] || return 0

  latest_digest="$(latest_runner_image_digest "${image}")"
  [[ -n "${latest_digest}" ]] || return 0

  if [[ "${installed_digest}" != "${latest_digest}" ]]; then
    printf 'WARNING: installed runner image %s is outdated (%s != %s)\n' "${image}" "${installed_digest}" "${latest_digest}" >&2
    printf 'Run `%s update-image` to pull the latest runner image before restarting.\n' "${script_name}" >&2
  fi
}

update_runner_image() {
  local image="${RUNNER_IMAGE:-}"

  [[ -n "${image}" ]] || {
    printf 'RUNNER_IMAGE is required to update the container image\n' >&2
    exit 1
  }

  require_cmd docker
  docker pull "${image}"
  printf 'Updated runner image: %s\n' "${image}" >&2
}

cleanup() {
  if [[ -n "${runner_pid:-}" ]] && kill -0 "${runner_pid}" >/dev/null 2>&1; then
    kill "${runner_pid}" >/dev/null 2>&1 || true
    wait "${runner_pid}" >/dev/null 2>&1 || true
  fi
  if [[ "${#compose_services[@]}" -gt 0 ]]; then
    docker compose --env-file "${env_file}" -f "${compose_file}" stop "${compose_services[@]}" >/dev/null 2>&1 || true
    docker compose --env-file "${env_file}" -f "${compose_file}" rm -f "${compose_services[@]}" >/dev/null 2>&1 || true
  fi
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

[[ -f "${env_file}" ]] || {
  printf 'missing env file: %s\n' "${env_file}" >&2
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
  direct)
    if [[ -z "${RUNNER_PUBLIC_IP:-}" ]]; then
      printf 'RUNNER_PUBLIC_IP is required in direct mode\n' >&2
      exit 1
    fi
    if [[ "${backend}" == "host" ]]; then
      compose_services=()
    else
      compose_services=(runner)
    fi
    ;;
  down)
    [[ -f "${compose_file}" ]] || exit 0
    require_cmd docker
    docker compose --env-file "${env_file}" -f "${compose_file}" down --remove-orphans
    exit 0
    ;;
  update-image)
    update_runner_image
    exit 0
    ;;
  *)
    printf 'usage: %s [quick|named|direct|down|update-image]\n' "$(basename "$0")" >&2
    exit 1
    ;;
esac

if [[ "${#compose_services[@]}" -gt 0 ]]; then
  [[ -f "${compose_file}" ]] || {
    printf 'missing compose file: %s\n' "${compose_file}" >&2
    exit 1
  }

  require_cmd docker
  docker compose version >/dev/null 2>&1 || {
    printf 'docker compose is required\n' >&2
    exit 1
  }
fi

warn_if_runner_image_outdated

require_cmd curl
configure_auth_headers
resolve_runner_identity

trap cleanup EXIT INT TERM

if [[ "${backend}" == "host" ]]; then
  "${bin_path}" serve --host "${runner_host}" --port "${runner_port}" &
  runner_pid=$!

  wait_for_runner
fi

if [[ "${#compose_services[@]}" -gt 0 ]]; then
  docker compose --env-file "${env_file}" -f "${compose_file}" up -d "${compose_services[@]}"
fi

if [[ "${mode}" == "direct" ]]; then
  register_mobile_runner "${RUNNER_PUBLIC_IP}" "${RUNNER_PUBLIC_PORT:-}"
else
  public_runner_url="$(wait_for_public_runner_url)"
  register_mobile_runner "${public_runner_url}"
fi

if [[ "${#compose_services[@]}" -gt 0 ]]; then
  docker compose --env-file "${env_file}" -f "${compose_file}" logs -f "${compose_services[@]}"
elif [[ -n "${runner_pid:-}" ]]; then
  wait "${runner_pid}"
fi
EOF
  chmod +x "$launcher_path"
}

write_env_file() {
  env_file="$1"
  cat >"$env_file" <<EOF
CREDIMI_URL=${CREDIMI_URL}
CREDIMI_RUNNER_ID=${CREDIMI_RUNNER_ID}
CREDIMI_RUNNER_NAME=${CREDIMI_RUNNER_NAME}
CREDIMI_RUNNER_DESCRIPTION=${CREDIMI_RUNNER_DESCRIPTION}
CREDIMI_RUNNER_ORGANIZATION=${CREDIMI_RUNNER_ORGANIZATION}
CREDIMI_RUNNER_TYPE=${CREDIMI_RUNNER_TYPE}
CREDIMI_RUNNER_SERIAL=${CREDIMI_RUNNER_SERIAL}
CREDIMI_RUNNER_DEVICE_MODE=${CREDIMI_RUNNER_DEVICE_MODE}
CREDIMI_RUNNER_WIFI_IP=${CREDIMI_RUNNER_WIFI_IP}
CREDIMI_RUNNER_WIFI_PORT=${CREDIMI_RUNNER_WIFI_PORT}
CREDIMI_USER_API_KEY=${CREDIMI_USER_API_KEY}
CREDIMI_PB_ADMIN=${CREDIMI_PB_ADMIN}
CREDIMI_PB_PASS=${CREDIMI_PB_PASS}
CREDIMI_INTERNAL_ADMIN_KEY=${CREDIMI_INTERNAL_ADMIN_KEY}
CREDIMI_TEMP_DIR=${CREDIMI_TEMP_DIR}
TEMPORAL_ADDRESS=${TEMPORAL_ADDRESS}
OTEL_ENABLED=${OTEL_ENABLED}
OTEL_EXPORTER_OTLP_ENDPOINT=${OTEL_EXPORTER_OTLP_ENDPOINT}
OTEL_SERVICE_NAME=${OTEL_SERVICE_NAME}
CREDIMI_RUNNER_BACKEND=${CREDIMI_RUNNER_BACKEND}
CREDIMI_CONTAINER_MODE=${CREDIMI_CONTAINER_MODE}
RUNNER_HOST=${RUNNER_HOST}
RUNNER_PORT=${RUNNER_PORT}
RUNNER_DOMAIN=${RUNNER_DOMAIN}
RUNNER_CADDY_SITE=${RUNNER_CADDY_SITE}
CLOUDFLARE_TUNNEL_TOKEN=${CLOUDFLARE_TUNNEL_TOKEN}
CREDIMI_SERVICE_MODE=${CREDIMI_SERVICE_MODE}
RUNNER_PUBLIC_IP=${RUNNER_PUBLIC_IP}
RUNNER_PUBLIC_PORT=${RUNNER_PUBLIC_PORT}
RUNNER_IMAGE=${RUNNER_IMAGE}
ANDROID_KEYS_DIR=${ANDROID_KEYS_DIR}
HOST_AVD_HOME_PATH=${HOST_AVD_HOME_PATH}
HOST_AVD_GOLDEN_PATH=${HOST_AVD_GOLDEN_PATH}
BASE_NAME=${BASE_NAME}
GOLDEN_PATH=${GOLDEN_PATH}
AVDCTL_SSH_TARGET=${AVDCTL_SSH_TARGET}
AVDCTL_SSH_PASSWORD=${AVDCTL_SSH_PASSWORD}
AVDCTL_SSH_KNOWN_HOSTS_PATH=${AVDCTL_SSH_KNOWN_HOSTS_PATH}
AVDCTL_SUDO=${AVDCTL_SUDO}
AVDCTL_SUDO_PASSWORD=${AVDCTL_SUDO_PASSWORD}
REDROID_DATA_DIR=${REDROID_DATA_DIR}
REDROID_DATA_TAR=${REDROID_DATA_TAR}
EOF
}

write_missing_env_values() {
  env_file="$1"
  appended_env_keys="0"

  append_env_if_missing "$env_file" "CREDIMI_RUNNER_BACKEND" "${CREDIMI_RUNNER_BACKEND}"
  append_env_if_missing "$env_file" "CREDIMI_CONTAINER_MODE" "${CREDIMI_CONTAINER_MODE}"
  append_env_if_missing "$env_file" "CREDIMI_TEMP_DIR" "${CREDIMI_TEMP_DIR}"
  append_env_if_missing "$env_file" "CREDIMI_RUNNER_NAME" "${CREDIMI_RUNNER_NAME}"
  append_env_if_missing "$env_file" "CREDIMI_RUNNER_DESCRIPTION" "${CREDIMI_RUNNER_DESCRIPTION}"
  append_env_if_missing "$env_file" "CREDIMI_RUNNER_ORGANIZATION" "${CREDIMI_RUNNER_ORGANIZATION}"
  append_env_if_missing "$env_file" "CREDIMI_RUNNER_TYPE" "${CREDIMI_RUNNER_TYPE}"
  append_env_if_missing "$env_file" "CREDIMI_RUNNER_SERIAL" "${CREDIMI_RUNNER_SERIAL}"
  append_env_if_missing "$env_file" "CREDIMI_RUNNER_DEVICE_MODE" "${CREDIMI_RUNNER_DEVICE_MODE}"
  append_env_if_missing "$env_file" "CREDIMI_RUNNER_WIFI_IP" "${CREDIMI_RUNNER_WIFI_IP}"
  append_env_if_missing "$env_file" "CREDIMI_RUNNER_WIFI_PORT" "${CREDIMI_RUNNER_WIFI_PORT}"
  append_env_if_missing "$env_file" "RUNNER_IMAGE" "${RUNNER_IMAGE}"
  append_env_if_missing "$env_file" "RUNNER_HOST" "${RUNNER_HOST}"
  append_env_if_missing "$env_file" "RUNNER_PORT" "${RUNNER_PORT}"
  append_env_if_missing "$env_file" "OTEL_ENABLED" "${OTEL_ENABLED}"
  append_env_if_missing "$env_file" "OTEL_EXPORTER_OTLP_ENDPOINT" "${OTEL_EXPORTER_OTLP_ENDPOINT}"
  append_env_if_missing "$env_file" "OTEL_SERVICE_NAME" "${OTEL_SERVICE_NAME}"
  append_env_if_missing "$env_file" "RUNNER_DOMAIN" "${RUNNER_DOMAIN}"
  append_env_if_missing "$env_file" "RUNNER_CADDY_SITE" "${RUNNER_CADDY_SITE}"
  append_env_if_missing "$env_file" "CLOUDFLARE_TUNNEL_TOKEN" "${CLOUDFLARE_TUNNEL_TOKEN}"
  append_env_if_missing "$env_file" "CREDIMI_SERVICE_MODE" "${CREDIMI_SERVICE_MODE}"
  append_env_if_missing "$env_file" "RUNNER_PUBLIC_IP" "${RUNNER_PUBLIC_IP}"
  append_env_if_missing "$env_file" "RUNNER_PUBLIC_PORT" "${RUNNER_PUBLIC_PORT}"
  append_env_if_missing "$env_file" "ANDROID_KEYS_DIR" "${ANDROID_KEYS_DIR}"
  append_env_if_missing "$env_file" "HOST_AVD_HOME_PATH" "${HOST_AVD_HOME_PATH}"
  append_env_if_missing "$env_file" "HOST_AVD_GOLDEN_PATH" "${HOST_AVD_GOLDEN_PATH}"
  append_env_if_missing "$env_file" "BASE_NAME" "${BASE_NAME}"
  append_env_if_missing "$env_file" "GOLDEN_PATH" "${GOLDEN_PATH}"
  append_env_if_missing "$env_file" "AVDCTL_SSH_TARGET" "${AVDCTL_SSH_TARGET}"
  append_env_if_missing "$env_file" "AVDCTL_SSH_PASSWORD" "${AVDCTL_SSH_PASSWORD}"
  append_env_if_missing "$env_file" "AVDCTL_SSH_KNOWN_HOSTS_PATH" "${AVDCTL_SSH_KNOWN_HOSTS_PATH}"
  append_env_if_missing "$env_file" "AVDCTL_SUDO" "${AVDCTL_SUDO}"
  append_env_if_missing "$env_file" "AVDCTL_SUDO_PASSWORD" "${AVDCTL_SUDO_PASSWORD}"
  append_env_if_missing "$env_file" "REDROID_DATA_DIR" "${REDROID_DATA_DIR}"
  append_env_if_missing "$env_file" "REDROID_DATA_TAR" "${REDROID_DATA_TAR}"
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
  CREDIMI_RUNNER_BACKEND="$(resolved_value CREDIMI_RUNNER_BACKEND "$(default_service_backend)")"
  CREDIMI_CONTAINER_MODE="${CREDIMI_CONTAINER_MODE-}"
  CREDIMI_TEMP_DIR="$(resolved_value CREDIMI_TEMP_DIR "${DEFAULT_CREDIMI_TEMP_DIR}")"
  CREDIMI_RUNNER_TYPE="${CREDIMI_RUNNER_TYPE-}"
  CREDIMI_RUNNER_SERIAL="${CREDIMI_RUNNER_SERIAL-}"
  CREDIMI_RUNNER_DEVICE_MODE="${CREDIMI_RUNNER_DEVICE_MODE-}"
  CREDIMI_RUNNER_WIFI_IP="${CREDIMI_RUNNER_WIFI_IP-}"
  CREDIMI_RUNNER_WIFI_PORT="${CREDIMI_RUNNER_WIFI_PORT-}"
  RUNNER_IMAGE="${RUNNER_IMAGE-}"
  RUNNER_PUBLIC_IP="${RUNNER_PUBLIC_IP-}"
  RUNNER_PUBLIC_PORT="${RUNNER_PUBLIC_PORT-}"
  OTEL_SERVICE_NAME="$(resolved_value OTEL_SERVICE_NAME "${DEFAULT_OTEL_SERVICE_NAME}")"
  ANDROID_KEYS_DIR="${ANDROID_KEYS_DIR-}"
  HOST_AVD_HOME_PATH="${HOST_AVD_HOME_PATH-}"
  HOST_AVD_GOLDEN_PATH="${HOST_AVD_GOLDEN_PATH-}"
  BASE_NAME="${BASE_NAME-}"
  GOLDEN_PATH="${GOLDEN_PATH-}"
  AVDCTL_SSH_TARGET="${AVDCTL_SSH_TARGET-}"
  AVDCTL_SSH_PASSWORD="${AVDCTL_SSH_PASSWORD-}"
  AVDCTL_SSH_KNOWN_HOSTS_PATH="${AVDCTL_SSH_KNOWN_HOSTS_PATH-}"
  AVDCTL_SUDO="${AVDCTL_SUDO-}"
  AVDCTL_SUDO_PASSWORD="${AVDCTL_SUDO_PASSWORD-}"
  REDROID_DATA_DIR="${REDROID_DATA_DIR-}"
  REDROID_DATA_TAR="${REDROID_DATA_TAR-}"

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
    warn "Existing configuration found at ${env_file}; loaded as prompt defaults."
  fi

  CREDIMI_URL="$(prompt_value CREDIMI_URL "Credimi API URL" "$(resolved_value CREDIMI_URL "${DEFAULT_CREDIMI_URL}")")"
  TEMPORAL_ADDRESS="$(prompt_value TEMPORAL_ADDRESS "Temporal address" "$(resolved_value TEMPORAL_ADDRESS "${DEFAULT_TEMPORAL_ADDRESS}")")"
  otel_enabled_choice="$(prompt_choice OTEL_ENABLED "Enable OpenTelemetry (yes/no)" "$(default_otel_enabled_choice)" "yes no")"
  case "$otel_enabled_choice" in
    yes|true|TRUE|True|1|on|ON|On)
      OTEL_ENABLED="true"
      OTEL_EXPORTER_OTLP_ENDPOINT="$(prompt_value OTEL_EXPORTER_OTLP_ENDPOINT "OTEL collector endpoint" "$(resolved_value OTEL_EXPORTER_OTLP_ENDPOINT "${DEFAULT_OTEL_EXPORTER_OTLP_ENDPOINT}")")"
      ;;
    no|false|FALSE|False|0|off|OFF|Off)
      OTEL_ENABLED="false"
      OTEL_EXPORTER_OTLP_ENDPOINT=""
      ;;
    *)
      die "unsupported OTEL_ENABLED value: ${otel_enabled_choice}"
      ;;
  esac

  if [ -n "$(resolved_value CREDIMI_USER_API_KEY)" ]; then
    auth_mode_default="api_key"
  elif [ -n "$(resolved_value CREDIMI_INTERNAL_ADMIN_KEY)" ] || [ -n "$(resolved_value CREDIMI_PB_ADMIN)" ] || [ -n "$(resolved_value CREDIMI_PB_PASS)" ]; then
    auth_mode_default="admin"
  else
    auth_mode_default="api_key"
  fi
  auth_mode="$(prompt_choice CREDIMI_INSTALL_AUTH_MODE "Auth mode (api_key/admin)" "${auth_mode_default}" "api_key admin")"

  if [ "$auth_mode" = "api_key" ]; then
    CREDIMI_USER_API_KEY="$(prompt_value CREDIMI_USER_API_KEY "Credimi user API key" "$(resolved_value CREDIMI_USER_API_KEY)" 1)"
    CREDIMI_PB_ADMIN=""
    CREDIMI_PB_PASS=""
    CREDIMI_INTERNAL_ADMIN_KEY=""
  else
    CREDIMI_INTERNAL_ADMIN_KEY="$(prompt_value CREDIMI_INTERNAL_ADMIN_KEY "Internal admin key" "$(resolved_value CREDIMI_INTERNAL_ADMIN_KEY)" 1)"
    CREDIMI_PB_ADMIN="$(prompt_value CREDIMI_PB_ADMIN "Credimi admin email" "$(resolved_value CREDIMI_PB_ADMIN)")"
    CREDIMI_PB_PASS="$(prompt_value CREDIMI_PB_PASS "Credimi admin password" "$(resolved_value CREDIMI_PB_PASS)" 1)"
    CREDIMI_USER_API_KEY=""
  fi

  existing_runner_id="$(resolved_value CREDIMI_RUNNER_ID)"
  use_existing_runner_id="no"
  if [ -n "$existing_runner_id" ]; then
    use_existing_runner_id="$(prompt_choice CREDIMI_USE_EXISTING_RUNNER_ID "Use existing runner ID ${existing_runner_id}? (yes/no)" "yes" "yes no")"
  fi

  if [ "$use_existing_runner_id" = "yes" ]; then
    CREDIMI_RUNNER_ID="$existing_runner_id"
    CREDIMI_RUNNER_NAME="$(runner_name_from_id "$existing_runner_id")"
    CREDIMI_RUNNER_ORGANIZATION="$(runner_org_from_id "$existing_runner_id")"
  else
    if [ -n "$existing_runner_id" ]; then
      delete_env_key "$env_file" "CREDIMI_RUNNER_ID"
    fi
    CREDIMI_RUNNER_ID=""
    runner_name_default="$(resolved_value CREDIMI_RUNNER_NAME "$(runner_name_from_id "${existing_runner_id}")")"
    CREDIMI_RUNNER_NAME="$(prompt_value CREDIMI_RUNNER_NAME "Runner name" "${runner_name_default}")"
    if [ "$auth_mode" = "admin" ]; then
      runner_org_default="$(resolved_value CREDIMI_RUNNER_ORGANIZATION "$(runner_org_from_id "${existing_runner_id}")")"
      CREDIMI_RUNNER_ORGANIZATION="$(prompt_value CREDIMI_RUNNER_ORGANIZATION "Runner organization canonified name" "${runner_org_default}")"
    else
      CREDIMI_RUNNER_ORGANIZATION=""
    fi
  fi
  CREDIMI_RUNNER_DESCRIPTION="$(prompt_value CREDIMI_RUNNER_DESCRIPTION "Runner description (optional)" "$(resolved_value CREDIMI_RUNNER_DESCRIPTION)" 0 1)"

  runner_type_options="$(runner_type_choices)"
  CREDIMI_RUNNER_TYPE="$(prompt_choice CREDIMI_RUNNER_TYPE "Mobile runner type (${runner_type_options})" "$(default_runner_type "${CREDIMI_RUNNER_BACKEND}")" "${runner_type_options}")"
  validate_runner_type_supported "${CREDIMI_RUNNER_TYPE}"
  resolve_install_runner_identity
  CREDIMI_SERVICE_MODE="$(prompt_choice CREDIMI_SERVICE_MODE "Exposure mode (quick/named/direct)" "$(resolved_value CREDIMI_SERVICE_MODE "quick")" "quick named direct")"
  RUNNER_HOST="$(prompt_value RUNNER_HOST "Runner bind host" "$(resolved_value RUNNER_HOST "${DEFAULT_RUNNER_HOST}")")"
  RUNNER_PORT="$(prompt_value RUNNER_PORT "Runner port" "$(resolved_value RUNNER_PORT "${DEFAULT_RUNNER_PORT}")")"

  case "$CREDIMI_SERVICE_MODE" in
    named)
      RUNNER_CADDY_SITE="$(prompt_value RUNNER_CADDY_SITE "Caddy listen site" "$(resolved_value RUNNER_CADDY_SITE "${DEFAULT_RUNNER_CADDY_SITE}")")"
      RUNNER_DOMAIN="$(prompt_value RUNNER_DOMAIN "Public runner domain" "$(resolved_value RUNNER_DOMAIN)")"
      CLOUDFLARE_TUNNEL_TOKEN="$(prompt_value CLOUDFLARE_TUNNEL_TOKEN "Cloudflare tunnel token" "$(resolved_value CLOUDFLARE_TUNNEL_TOKEN)" 1)"
      RUNNER_PUBLIC_IP=""
      RUNNER_PUBLIC_PORT=""
      ;;
    direct)
      RUNNER_CADDY_SITE="$(resolved_value RUNNER_CADDY_SITE)"
      RUNNER_DOMAIN=""
      CLOUDFLARE_TUNNEL_TOKEN=""
      RUNNER_PUBLIC_IP="$(prompt_value RUNNER_PUBLIC_IP "Public runner IP/host" "$(resolved_value RUNNER_PUBLIC_IP)")"
      RUNNER_PUBLIC_PORT="$(prompt_value RUNNER_PUBLIC_PORT "Public runner port (optional)" "$(resolved_value RUNNER_PUBLIC_PORT)" 0 1)"
      ;;
    *)
      RUNNER_CADDY_SITE="$(prompt_value RUNNER_CADDY_SITE "Caddy listen site" "$(resolved_value RUNNER_CADDY_SITE "${DEFAULT_RUNNER_CADDY_SITE}")")"
      RUNNER_DOMAIN=""
      CLOUDFLARE_TUNNEL_TOKEN=""
      RUNNER_PUBLIC_IP=""
      RUNNER_PUBLIC_PORT=""
      ;;
  esac

  CREDIMI_RUNNER_SERIAL="$(explicit_env_or_default CREDIMI_RUNNER_SERIAL)"
  CREDIMI_RUNNER_DEVICE_MODE="$(explicit_env_or_default CREDIMI_RUNNER_DEVICE_MODE)"
  CREDIMI_RUNNER_WIFI_IP="$(explicit_env_or_default CREDIMI_RUNNER_WIFI_IP)"
  CREDIMI_RUNNER_WIFI_PORT="$(explicit_env_or_default CREDIMI_RUNNER_WIFI_PORT)"

  case "$CREDIMI_RUNNER_TYPE" in
    android_emulator)
      CREDIMI_RUNNER_SERIAL=""
      if [ "$CREDIMI_RUNNER_BACKEND" = "container" ]; then
        CREDIMI_CONTAINER_MODE="emulator"
        RUNNER_IMAGE="$(explicit_env_or_default RUNNER_IMAGE "${DEFAULT_EMULATOR_IMAGE}")"
        ANDROID_KEYS_DIR="$(prompt_value ANDROID_KEYS_DIR "ADB keys directory" "$(resolved_value ANDROID_KEYS_DIR "${HOME}/.android")")"
        HOST_AVD_HOME_PATH="$(prompt_value HOST_AVD_HOME_PATH "Host AVD home path" "$(resolved_value HOST_AVD_HOME_PATH "${DEFAULT_HOST_AVD_HOME_PATH}")")"
        HOST_AVD_GOLDEN_PATH="$(prompt_value HOST_AVD_GOLDEN_PATH "Host golden assets path (parent or extracted dir)" "$(resolved_value HOST_AVD_GOLDEN_PATH "${DEFAULT_HOST_AVD_GOLDEN_PATH}")")"
        BASE_NAME="$(prompt_value BASE_NAME "Emulator base name" "$(resolved_value BASE_NAME "${DEFAULT_BASE_NAME}")")"
        GOLDEN_PATH="$(prompt_value GOLDEN_PATH "Container golden path" "$(resolved_value GOLDEN_PATH "/avd-golden/${BASE_NAME}-golden")")"
      else
        CREDIMI_CONTAINER_MODE=""
        ANDROID_KEYS_DIR=""
        HOST_AVD_HOME_PATH=""
        HOST_AVD_GOLDEN_PATH=""
        BASE_NAME="$(prompt_value BASE_NAME "Emulator base name" "$(resolved_value BASE_NAME "${DEFAULT_BASE_NAME}")")"
        GOLDEN_PATH="$(prompt_value GOLDEN_PATH "Golden path" "$(resolved_value GOLDEN_PATH "${HOME}/avd-golden/${BASE_NAME}-golden")")"
      fi
      ;;
    ios_simulator)
      CREDIMI_RUNNER_SERIAL=""
      if [ "$CREDIMI_RUNNER_BACKEND" = "container" ]; then
        CREDIMI_CONTAINER_MODE="no_device"
        RUNNER_IMAGE="$(explicit_env_or_default RUNNER_IMAGE "${DEFAULT_PHONE_IMAGE}")"
      else
        CREDIMI_CONTAINER_MODE=""
      fi
      ANDROID_KEYS_DIR=""
      HOST_AVD_HOME_PATH=""
      HOST_AVD_GOLDEN_PATH=""
      BASE_NAME="$(prompt_value BASE_NAME "Simulator base name" "$(resolved_value BASE_NAME "${DEFAULT_BASE_NAME}")")"
      GOLDEN_PATH=""
      AVDCTL_SSH_TARGET=""
      AVDCTL_SSH_PASSWORD=""
      AVDCTL_SSH_KNOWN_HOSTS_PATH=""
      AVDCTL_SUDO=""
      AVDCTL_SUDO_PASSWORD=""
      REDROID_DATA_DIR=""
      REDROID_DATA_TAR=""
      ;;
    ios_phone)
      CREDIMI_RUNNER_SERIAL="$(prompt_value CREDIMI_RUNNER_SERIAL "iOS phone serial" "$(resolved_value CREDIMI_RUNNER_SERIAL)")"
      if [ "$CREDIMI_RUNNER_BACKEND" = "container" ]; then
        CREDIMI_CONTAINER_MODE="no_device"
        RUNNER_IMAGE="$(explicit_env_or_default RUNNER_IMAGE "${DEFAULT_PHONE_IMAGE}")"
      else
        CREDIMI_CONTAINER_MODE=""
      fi
      ANDROID_KEYS_DIR=""
      HOST_AVD_HOME_PATH=""
      HOST_AVD_GOLDEN_PATH=""
      BASE_NAME=""
      GOLDEN_PATH=""
      AVDCTL_SSH_TARGET=""
      AVDCTL_SSH_PASSWORD=""
      AVDCTL_SSH_KNOWN_HOSTS_PATH=""
      AVDCTL_SUDO=""
      AVDCTL_SUDO_PASSWORD=""
      REDROID_DATA_DIR=""
      REDROID_DATA_TAR=""
      ;;
    redroid|android_phone)
      android_device_mode_options="usb wifi"
      if [ "$CREDIMI_RUNNER_TYPE" = "redroid" ]; then
        android_device_mode_options="no_device usb wifi"
      fi
      CREDIMI_RUNNER_DEVICE_MODE="$(prompt_choice CREDIMI_RUNNER_DEVICE_MODE "Android connection mode (${android_device_mode_options})" "$(default_android_device_mode "${CREDIMI_RUNNER_TYPE}")" "${android_device_mode_options}")"
      RUNNER_IMAGE="$(explicit_env_or_default RUNNER_IMAGE "${DEFAULT_PHONE_IMAGE}")"
      ANDROID_KEYS_DIR=""
      HOST_AVD_HOME_PATH=""
      HOST_AVD_GOLDEN_PATH=""
      BASE_NAME=""
      GOLDEN_PATH=""

      case "$CREDIMI_RUNNER_DEVICE_MODE" in
        no_device)
          CREDIMI_RUNNER_WIFI_IP="$(prompt_value CREDIMI_RUNNER_WIFI_IP "Android Wi-Fi IP" "$(resolved_value CREDIMI_RUNNER_WIFI_IP)")"
          CREDIMI_RUNNER_WIFI_PORT="$(prompt_value CREDIMI_RUNNER_WIFI_PORT "Android Wi-Fi port" "$(resolved_value CREDIMI_RUNNER_WIFI_PORT "${DEFAULT_ANDROID_WIFI_PORT}")")"
          CREDIMI_RUNNER_SERIAL="${CREDIMI_RUNNER_WIFI_IP}:${CREDIMI_RUNNER_WIFI_PORT}"
          if [ "$CREDIMI_RUNNER_BACKEND" = "container" ]; then
            CREDIMI_CONTAINER_MODE="no_device"
          else
            CREDIMI_CONTAINER_MODE=""
          fi
          ;;
        usb)
          usb_serial_default="$(resolved_value CREDIMI_RUNNER_SERIAL)"
          if [ -z "$usb_serial_default" ] && command -v adb >/dev/null 2>&1; then
            usb_serial_default="$(detect_connected_android_usb_serial || true)"
          fi
          CREDIMI_RUNNER_SERIAL="$(prompt_value CREDIMI_RUNNER_SERIAL "Android device serial" "$usb_serial_default")"
          CREDIMI_RUNNER_WIFI_IP=""
          CREDIMI_RUNNER_WIFI_PORT=""
          if [ "$CREDIMI_RUNNER_BACKEND" = "container" ]; then
            CREDIMI_CONTAINER_MODE="usb"
          else
            CREDIMI_CONTAINER_MODE=""
          fi
          ;;
        wifi)
          CREDIMI_RUNNER_WIFI_IP="$(prompt_value CREDIMI_RUNNER_WIFI_IP "Android Wi-Fi IP" "$(resolved_value CREDIMI_RUNNER_WIFI_IP)")"
          CREDIMI_RUNNER_WIFI_PORT="$(prompt_value CREDIMI_RUNNER_WIFI_PORT "Android Wi-Fi port" "$(resolved_value CREDIMI_RUNNER_WIFI_PORT "${DEFAULT_ANDROID_WIFI_PORT}")")"
          CREDIMI_RUNNER_SERIAL="${CREDIMI_RUNNER_WIFI_IP}:${CREDIMI_RUNNER_WIFI_PORT}"
          if [ "$CREDIMI_RUNNER_BACKEND" = "container" ]; then
            CREDIMI_CONTAINER_MODE="wifi"
          else
            CREDIMI_CONTAINER_MODE=""
          fi
          ;;
      esac

      avdctl_ssh_choice="$(prompt_choice AVDCTL_USE_SSH_PROMPT "Use avdctl via SSH (yes/no)" "$(default_avdctl_ssh_choice)" "yes no")"
      case "$avdctl_ssh_choice" in
        yes)
          AVDCTL_SSH_TARGET="$(prompt_value AVDCTL_SSH_TARGET "AVDCTL SSH target" "$(resolved_value AVDCTL_SSH_TARGET)")"
          AVDCTL_SSH_PASSWORD="$(prompt_value AVDCTL_SSH_PASSWORD "AVDCTL SSH password (optional)" "$(resolved_value AVDCTL_SSH_PASSWORD)" 1 1)"
          if [ "$CREDIMI_RUNNER_BACKEND" = "container" ]; then
            AVDCTL_SSH_KNOWN_HOSTS_PATH="$(prompt_value AVDCTL_SSH_KNOWN_HOSTS_PATH "SSH known_hosts path to mount into the runner container" "$(resolved_value AVDCTL_SSH_KNOWN_HOSTS_PATH "${HOME}/.ssh/known_hosts")")"
          else
            AVDCTL_SSH_KNOWN_HOSTS_PATH=""
          fi
          avdctl_sudo_choice="$(prompt_choice AVDCTL_USE_SUDO_PROMPT "Does avdctl need sudo (yes/no)" "$(default_avdctl_sudo_choice)" "yes no")"
          case "$avdctl_sudo_choice" in
            yes)
              AVDCTL_SUDO="true"
              AVDCTL_SUDO_PASSWORD="$(prompt_value AVDCTL_SUDO_PASSWORD "AVDCTL sudo password" "$(resolved_value AVDCTL_SUDO_PASSWORD)" 1)"
              ;;
            *)
              AVDCTL_SUDO="false"
              AVDCTL_SUDO_PASSWORD=""
              ;;
          esac
          ;;
        *)
          AVDCTL_SSH_TARGET=""
          AVDCTL_SSH_PASSWORD=""
          AVDCTL_SSH_KNOWN_HOSTS_PATH=""
          AVDCTL_SUDO="false"
          AVDCTL_SUDO_PASSWORD=""
          ;;
      esac

      if [ "$CREDIMI_RUNNER_TYPE" = "redroid" ]; then
        redroid_data_location_note=""
        if [ -n "$AVDCTL_SSH_TARGET" ]; then
          redroid_data_location_note=" on the remote machine"
        fi
        REDROID_DATA_DIR="$(prompt_value REDROID_DATA_DIR "Redroid data dir${redroid_data_location_note}" "$(resolved_value REDROID_DATA_DIR "${DEFAULT_REDROID_DATA_DIR}")")"
        REDROID_DATA_TAR="$(prompt_value REDROID_DATA_TAR "Redroid data tar${redroid_data_location_note}" "$(resolved_value REDROID_DATA_TAR "${DEFAULT_REDROID_DATA_TAR}")")"
      else
        REDROID_DATA_DIR=""
        REDROID_DATA_TAR=""
      fi
      ;;
  esac

  if [ -z "$CREDIMI_TEMP_DIR" ]; then
    CREDIMI_TEMP_DIR="${DEFAULT_CREDIMI_TEMP_DIR}"
  fi
  if [ -e "$CREDIMI_TEMP_DIR" ] && [ ! -d "$CREDIMI_TEMP_DIR" ]; then
    fallback_temp_dir="${DEFAULT_CREDIMI_TEMP_DIR}"
    if [ "$CREDIMI_TEMP_DIR" = "$fallback_temp_dir" ]; then
      fallback_temp_dir="${DEFAULT_CREDIMI_TEMP_DIR}-${USER:-runner}"
    fi
    warn "CREDIMI_TEMP_DIR path ${CREDIMI_TEMP_DIR} exists and is not a directory; using ${fallback_temp_dir} instead."
    CREDIMI_TEMP_DIR="$fallback_temp_dir"
  fi

  mkdir -p "$bin_dir" "$config_dir" "$CREDIMI_TEMP_DIR"

  if [ "$CREDIMI_RUNNER_BACKEND" = "host" ]; then
    say "Downloading ${binary_url}"
    curl -fsSL "$binary_url" -o "$binary_path"
    chmod +x "$binary_path"
  fi

  write_compose_file "$compose_file"
  write_launcher "$launcher_path"
  write_env_file "$env_file"

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
    say "Configured mobile runner type: ${CREDIMI_RUNNER_TYPE}."
    case "$CREDIMI_CONTAINER_MODE" in
      wifi)
        say "Configured Android transport: Wi-Fi (${CREDIMI_RUNNER_SERIAL})."
        ;;
      usb)
        say "Configured Android transport: USB (${CREDIMI_RUNNER_SERIAL})."
        ;;
      emulator)
        say "Configured Android transport: emulator container."
        ;;
      no_device)
        say "Configured container mode: no-device."
        ;;
    esac
  fi
  if [ "$existing_env" = "1" ]; then
    say "Updated configuration in ${env_file}."
  fi
  say ""
  say "Start the service with:"
  say "${PROJECT_NAME}-service"
  say ""
  say "Other commands:"
  say "${PROJECT_NAME}-service quick"
  say "${PROJECT_NAME}-service named"
  say "${PROJECT_NAME}-service direct"
  say "${PROJECT_NAME}-service down"
  say "${PROJECT_NAME}-service update-image"
}

main "$@"
