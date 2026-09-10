#!/bin/sh
set -eu

REPO_OWNER="ForkbombEu"
REPO_NAME="credimi-runner"
PROJECT_NAME="credimi-runner"
CLOUDFLARED_VERSION="2026.8.2"

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

path_notice() {
  printf '%s%s%s%s\n' "${c_yellow}" "${c_bold}" "$*" "${c_reset}" >&2
}

path_command() {
  printf '  %s%s%s%s\n' "${c_cyan}" "${c_bold}" "$*" "${c_reset}" >&2
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

show_path_instructions() {
  target_dir="$1"
  shell_path="${SHELL:-}"
  shell_name="${shell_path##*/}"
  display_dir="$target_dir"
  if [ "$target_dir" = "${HOME}/.local/bin" ]; then
    display_dir='$HOME/.local/bin'
  fi

  path_notice ""
  path_notice "======================================================================"
  path_notice "ACTION REQUIRED: ${PROJECT_NAME} is not available on PATH"
  path_notice "======================================================================"
  say "Installed binary: ${target_dir}/${PROJECT_NAME}"
  say "Detected shell: ${shell_name:-unknown}"
  say "The installer did not modify your shell configuration."
  say ""

  case "$shell_name" in
    fish)
      say "Run this command; Fish will persist the path for future sessions:"
      path_command "fish_add_path \"${display_dir}\""
      ;;
    bash)
      say "Run this command for the current Bash session:"
      path_command "export PATH=\"${display_dir}:\$PATH\""
      say "Add the same export to ~/.bashrc (or ~/.bash_profile for login shells)."
      ;;
    zsh)
      say "Run this command for the current Zsh session:"
      path_command "export PATH=\"${display_dir}:\$PATH\""
      say "Add the same export to ~/.zshrc for future sessions."
      ;;
    sh|dash|ksh|mksh|ash)
      say "Run this command for the current ${shell_name} session:"
      path_command "export PATH=\"${display_dir}:\$PATH\""
      say "Add the same export to ~/.profile for future sessions."
      ;;
    *)
      say "Run this command for the current session:"
      path_command "export PATH=\"${display_dir}:\$PATH\""
      say "Add the equivalent command to your shell's startup file."
      ;;
  esac

  path_notice "======================================================================"
  path_notice ""
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

cleanup_tmp() {
  if [ -n "${tmp_binary_path:-}" ] && [ -e "$tmp_binary_path" ]; then
    rm -f "$tmp_binary_path"
  fi
  if [ -n "${release_metadata_path:-}" ] && [ -e "$release_metadata_path" ]; then
    rm -f "$release_metadata_path"
  fi
  if [ -n "${checksum_path:-}" ] && [ -e "$checksum_path" ]; then
    rm -f "$checksum_path"
  fi
  if [ -n "${cloudflared_archive_path:-}" ] && [ -e "$cloudflared_archive_path" ]; then
    rm -f "$cloudflared_archive_path"
  fi
  if [ -n "${cloudflared_staged_path:-}" ] && [ -e "$cloudflared_staged_path" ]; then
    rm -f "$cloudflared_staged_path"
  fi
  if [ -n "${cloudflared_extract_dir:-}" ] && [ -d "$cloudflared_extract_dir" ]; then
    rm -rf "$cloudflared_extract_dir"
  fi
}

sha256_file() {
  case "$os_name" in
    Linux) sha256sum "$1" | awk '{print $1}' ;;
    Darwin) shasum -a 256 "$1" | awk '{print $1}' ;;
    *) die "unsupported operating system: $os_name" ;;
  esac
}

valid_sha256() {
  printf '%s\n' "$1" | awk 'length($0) == 64 && $0 !~ /[^[:xdigit:]]/ { exit 0 } { exit 1 }'
}

checksum_for_asset() {
  checksum_file_path="$1"
  checksum_asset_name="$2"
  awk -v asset="$checksum_asset_name" '
    NF == 0 { next }
    NF != 2 {
      if ($NF == asset || index($0, asset) > 0) malformed = 1
      next
    }
    $2 == asset { count++; value = $1 }
    END {
      if (malformed || count != 1) exit 1
      print value
    }
  ' "$checksum_file_path"
}

release_tag_from_metadata() {
  sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$1" | sed -n '1p'
}

main() {
  need_cmd chmod
  need_cmd curl
  need_cmd grep
  need_cmd awk
  need_cmd sed
  need_cmd mkdir
  need_cmd mktemp
  need_cmd mv
  need_cmd rm
  need_cmd uname
  os_name="$(uname -s)"
  if [ "$os_name" = "Linux" ]; then
    need_cmd sha256sum
  elif [ "$os_name" = "Darwin" ]; then
    need_cmd shasum
    need_cmd tar
    need_cmd find
    need_cmd cp
  else
    die "unsupported operating system: $os_name"
  fi

  bin_dir="${CREDIMI_RUNNER_BIN_DIR:-${XDG_BIN_HOME:-${HOME}/.local/bin}}"
  binary_path="${bin_dir}/${PROJECT_NAME}"
  arch_name="$(uname -m)"
  asset_name="$(normalize_asset_name)"
  mkdir -p "$bin_dir"

  release_metadata_path="$(mktemp "${bin_dir}/.credimi-release.XXXXXX")"
  tmp_binary_path="$(mktemp "${bin_dir}/.credimi-runner.XXXXXX")"
  checksum_path=""
  cloudflared_archive_path=""
  cloudflared_extract_dir=""
  cloudflared_staged_path=""
  trap cleanup_tmp EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM

  release_api_url="https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/releases/latest"
  say "Resolving latest Credimi Runner release"
  curl -fsSL "$release_api_url" -o "$release_metadata_path"
  release_tag="$(release_tag_from_metadata "$release_metadata_path")"
  case "$release_tag" in
    ""|*/*|*[![:graph:]]) die "invalid release tag in GitHub metadata" ;;
  esac

  binary_url="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/download/${release_tag}/${asset_name}"
  checksum_asset="${PROJECT_NAME}_${release_tag}_checksums.txt"
  checksum_url="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/download/${release_tag}/${checksum_asset}"
  say "Downloading ${PROJECT_NAME} for ${asset_name}"
  say "${binary_url}"
  curl -fsSL "$binary_url" -o "$tmp_binary_path"
  checksum_path="$(mktemp "${bin_dir}/.credimi-checksums.XXXXXX")"
  say "Downloading checksum ${checksum_asset}"
  curl -fsSL "$checksum_url" -o "$checksum_path"
  expected_sha="$(checksum_for_asset "$checksum_path" "$asset_name")" || die "checksum file does not contain exactly one entry for ${asset_name}"
  valid_sha256 "$expected_sha" || die "malformed SHA256 for ${asset_name}"
  actual_sha="$(sha256_file "$tmp_binary_path")"
  if ! awk -v expected="$expected_sha" -v actual="$actual_sha" 'BEGIN { exit(tolower(expected) == tolower(actual) ? 0 : 1) }'; then
    die "checksum verification failed for ${asset_name}"
  fi

  if [ "$os_name" = "Darwin" ]; then
    cloudflared_path="${bin_dir}/credimi-cloudflared"
    case "$arch_name" in
      x86_64|amd64)
        cloudflared_asset="cloudflared-darwin-amd64.tgz"
        cloudflared_sha256="f1727723c586500e2092368ae21871b3df7ddfd2cb097f22d81bee4a9c458bb4"
        ;;
      arm64|aarch64)
        cloudflared_asset="cloudflared-darwin-arm64.tgz"
        cloudflared_sha256="9042c2c5d8b2de78e60f313d5fb31b6c5c1cebde787a3caf1f2c9588084ac442"
        ;;
      *) die "unsupported macOS architecture: $arch_name" ;;
    esac
    cloudflared_url="https://github.com/cloudflare/cloudflared/releases/download/${CLOUDFLARED_VERSION}/${cloudflared_asset}"
    cloudflared_archive_path="$(mktemp "${bin_dir}/.credimi-cloudflared.XXXXXX")"
    say "Downloading cloudflared ${CLOUDFLARED_VERSION}"
    curl -fsSL "$cloudflared_url" -o "$cloudflared_archive_path"
    actual_sha="$(sha256_file "$cloudflared_archive_path")"
    if ! awk -v expected="$cloudflared_sha256" -v actual="$actual_sha" 'BEGIN { exit(tolower(expected) == tolower(actual) ? 0 : 1) }'; then
      die "cloudflared checksum verification failed for ${cloudflared_asset}"
    fi
    cloudflared_extract_dir="$(mktemp -d "${bin_dir}/.credimi-cloudflared-extract.XXXXXX")"
    tar -xzf "$cloudflared_archive_path" -C "$cloudflared_extract_dir"
    cloudflared_source="$(find "$cloudflared_extract_dir" -type f -name cloudflared -print | sed -n '1p')"
    [ -n "$cloudflared_source" ] || die "cloudflared archive does not contain an executable"
    cloudflared_staged_path="$(mktemp "${bin_dir}/.credimi-cloudflared-staged.XXXXXX")"
    cp "$cloudflared_source" "$cloudflared_staged_path"
    chmod +x "$cloudflared_staged_path"
    [ -x "$cloudflared_staged_path" ] || die "extracted cloudflared is not executable"
  fi

  chmod +x "$tmp_binary_path"
  mv "$tmp_binary_path" "$binary_path"
  tmp_binary_path=""
  if [ "$os_name" = "Darwin" ]; then
    mv "$cloudflared_staged_path" "$cloudflared_path"
    cloudflared_staged_path=""
    success "Installed cloudflared at ${cloudflared_path}"
  fi
  success "Installed ${PROJECT_NAME} at ${binary_path}"
  if ! path_has_dir "$bin_dir"; then
    show_path_instructions "$bin_dir"
  fi

  say "Starting Credimi Runner dashboard..."
  cleanup_tmp
  exec "$binary_path" "$@"
}

main "$@"
