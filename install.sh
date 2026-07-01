#!/bin/sh
set -eu

REPO_OWNER="ForkbombEu"
REPO_NAME="credimi-runner"
PROJECT_NAME="credimi-runner"

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
  c_cyan="$(printf '\033[36m')"
  c_bold="$(printf '\033[1m')"
else
  c_reset=""
  c_red=""
  c_green=""
  c_yellow=""
  c_blue=""
  c_cyan=""
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

path_has_dir() {
  target_dir="$1"
  printf '%s\n' ":$PATH:" | grep -Fq ":${target_dir}:"
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

cleanup_tmp_binary() {
  if [ -n "${tmp_binary_path:-}" ] && [ -f "$tmp_binary_path" ]; then
    rm -f "$tmp_binary_path"
  fi
}

main() {
  need_cmd chmod
  need_cmd curl
  need_cmd grep
  need_cmd mkdir
  need_cmd mv
  need_cmd rm
  need_cmd uname

  bin_dir="${CREDIMI_RUNNER_BIN_DIR:-${XDG_BIN_HOME:-${HOME}/.local/bin}}"
  binary_path="${bin_dir}/${PROJECT_NAME}"
  tmp_binary_path="${binary_path}.tmp.$$"
  asset_name="$(normalize_asset_name)"
  binary_url="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/latest/download/${asset_name}"

  trap cleanup_tmp_binary EXIT HUP INT TERM

  mkdir -p "$bin_dir"

  say "Downloading ${PROJECT_NAME} for ${asset_name}"
  say "${binary_url}"
  curl -fsSL "$binary_url" -o "$tmp_binary_path"
  chmod +x "$tmp_binary_path"
  mv "$tmp_binary_path" "$binary_path"
  tmp_binary_path=""

  success "Installed ${PROJECT_NAME} at ${binary_path}"
  if ! path_has_dir "$bin_dir"; then
    warn "${bin_dir} is not in PATH for this shell."
    if [ "$bin_dir" = "${HOME}/.local/bin" ]; then
      say "Add it with: export PATH=\"\$HOME/.local/bin:\$PATH\""
    else
      say "Add it with: export PATH=\"${bin_dir}:\$PATH\""
    fi
  fi

  say "Starting Credimi Runner dashboard..."
  exec "$binary_path" "$@"
}

main "$@"
