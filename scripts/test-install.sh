#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
install_source="${repo_root}/install.sh"

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

runner_asset() {
  case "$1:$2" in
    Linux:x86_64|Linux:amd64) printf 'credimi-runner-Linux-x86_64' ;;
    Linux:arm64|Linux:aarch64) printf 'credimi-runner-Linux-aarch64' ;;
    Darwin:x86_64|Darwin:amd64) printf 'credimi-runner-Darwin-x86_64' ;;
    Darwin:arm64|Darwin:aarch64) printf 'credimi-runner-Darwin-arm64' ;;
    *) fail "unsupported fixture platform: $1 $2" ;;
  esac
}

create_mocks() {
  local mock_dir="$1"
  mkdir -p "$mock_dir"

  cat >"${mock_dir}/uname" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
  -s) printf '%s\n' "${FAKE_UNAME_S:?}" ;;
  -m) printf '%s\n' "${FAKE_UNAME_M:?}" ;;
  *) exec /usr/bin/uname "$@" ;;
esac
EOF

  cat >"${mock_dir}/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

: "${MOCK_LOG_DIR:?}"

printf '%s\n' "$*" >>"${MOCK_LOG_DIR}/curl.log"
out=""
url=""
while (($#)); do
  case "$1" in
    -o)
      out="$2"
      shift 2
      ;;
    *)
      url="$1"
      shift
      ;;
  esac
done

[[ -n "$out" ]] || { printf 'mock curl missing output path\n' >&2; exit 97; }
case "$url" in
  https://api.github.com/repos/ForkbombEu/credimi-runner/releases/latest)
    cp "${MOCK_RELEASE_JSON:?}" "$out"
    ;;
  */releases/download/v9.9.9/credimi-runner-*)
    cp "${MOCK_RUNNER_BYTES:?}" "$out"
    ;;
  */releases/download/v9.9.9/credimi-runner_v9.9.9_checksums.txt)
    cp "${MOCK_CHECKSUM_FILE:?}" "$out"
    ;;
  https://github.com/cloudflare/cloudflared/releases/download/2026.8.2/cloudflared-darwin-amd64.tgz|https://github.com/cloudflare/cloudflared/releases/download/2026.8.2/cloudflared-darwin-arm64.tgz)
    cp "${MOCK_CLOUDFLARED_ARCHIVE:?}" "$out"
    ;;
  *)
    printf 'unexpected mock curl URL: %s\n' "$url" >&2
    exit 97
    ;;
esac
EOF

  chmod +x "${mock_dir}/uname" "${mock_dir}/curl"
}

prepare_install_script() {
  local case_dir="$1"
  local amd64_sha="$2"
  local arm64_sha="$3"

  sed \
    -e "s/f1727723c586500e2092368ae21871b3df7ddfd2cb097f22d81bee4a9c458bb4/${amd64_sha}/g" \
    -e "s/9042c2c5d8b2de78e60f313d5fb31b6c5c1cebde787a3caf1f2c9588084ac442/${arm64_sha}/g" \
    "$install_source" >"${case_dir}/install.sh"
  chmod +x "${case_dir}/install.sh"
}

new_case_dir() {
  local case_dir
  case_dir="$(mktemp -d)"
  mkdir -p "${case_dir}/logs"
  create_mocks "${case_dir}/mocks"

  printf '%s\n' '{"tag_name":"v9.9.9"}' >"${case_dir}/release.json"
  cat >"${case_dir}/runner-new" <<'EOF'
#!/bin/sh
printf 'dashboard:%s\n' "$*" >>"${MOCK_LOG_DIR}/launch.log"
EOF
  chmod +x "${case_dir}/runner-new"

  mkdir -p "${case_dir}/cloud-good" "${case_dir}/cloud-empty"
  cat >"${case_dir}/cloud-good/cloudflared" <<'EOF'
#!/bin/sh
exit 0
EOF
  chmod +x "${case_dir}/cloud-good/cloudflared"
  tar -czf "${case_dir}/cloud-good.tgz" -C "${case_dir}/cloud-good" cloudflared
  tar -czf "${case_dir}/cloud-empty.tgz" -C "${case_dir}/cloud-empty" .
  good_sha="$(sha256sum "${case_dir}/cloud-good.tgz" | awk '{print $1}')"
  empty_sha="$(sha256sum "${case_dir}/cloud-empty.tgz" | awk '{print $1}')"
  prepare_install_script "$case_dir" "$good_sha" "$good_sha"
  printf '%s\t%s\n' "$empty_sha" "$good_sha" >"${case_dir}/archive-sha"
  printf '%s' "$case_dir"
}

write_runner_checksum() {
  local case_dir="$1"
  local asset="$2"
  local digest
  digest="$(sha256sum "${case_dir}/runner-new" | awk '{print $1}')"
  printf '%s  %s.old\n%s  %s\n' "$digest" "$asset" "$digest" "$asset" >"${case_dir}/checksums"
}

run_install() {
  local case_dir="$1"
  shift

  PATH="${case_dir}/mocks:${PATH}" \
  HOME="${case_dir}/home" \
  XDG_BIN_HOME="${case_dir}/bin" \
  MOCK_LOG_DIR="${case_dir}/logs" \
  MOCK_RELEASE_JSON="${case_dir}/release.json" \
  MOCK_RUNNER_BYTES="${case_dir}/runner-new" \
  MOCK_CHECKSUM_FILE="${case_dir}/checksums" \
  MOCK_CLOUDFLARED_ARCHIVE="${case_dir}/cloud-good.tgz" \
  "$@" \
  sh "${case_dir}/install.sh" --from-test >"${case_dir}/stdout.log" 2>"${case_dir}/stderr.log"
}

assert_no_temporary_files() {
  local case_dir="$1"
  if find "${case_dir}/bin" -maxdepth 1 \( -name '.credimi-*' -o -name '*.tmp.*' \) -print -quit | grep -q .; then
    fail "temporary installer files remain in ${case_dir}/bin"
  fi
}

assert_runner_install_contract() {
  local case_dir="$1"
  local os_name="$2"
  local arch_name="$3"
  local asset
  asset="$(runner_asset "$os_name" "$arch_name")"
  assert_file_exists "${case_dir}/bin/credimi-runner"
  assert_executable "${case_dir}/bin/credimi-runner"
  assert_contains "api.github.com/repos/ForkbombEu/credimi-runner/releases/latest" "${case_dir}/logs/curl.log"
  assert_contains "/releases/download/v9.9.9/${asset}" "${case_dir}/logs/curl.log"
  assert_contains "/releases/download/v9.9.9/credimi-runner_v9.9.9_checksums.txt" "${case_dir}/logs/curl.log"
  assert_not_contains "/releases/latest/download/" "${case_dir}/logs/curl.log"
  assert_contains "dashboard:--from-test" "${case_dir}/logs/launch.log"
  assert_no_temporary_files "$case_dir"
}

run_linux_case() {
  local os_name="$1"
  local arch_name="$2"
  local case_dir
  case_dir="$(new_case_dir)"
  asset="$(runner_asset "$os_name" "$arch_name")"
  write_runner_checksum "$case_dir" "$asset"
  run_install "$case_dir" env FAKE_UNAME_S="$os_name" FAKE_UNAME_M="$arch_name"
  assert_runner_install_contract "$case_dir" "$os_name" "$arch_name"
  assert_file_absent "${case_dir}/bin/credimi-cloudflared"
}

run_darwin_case() {
  local arch_name="$1"
  local case_dir
  case_dir="$(new_case_dir)"
  asset="$(runner_asset Darwin "$arch_name")"
  write_runner_checksum "$case_dir" "$asset"
  run_install "$case_dir" env FAKE_UNAME_S=Darwin FAKE_UNAME_M="$arch_name"
  assert_runner_install_contract "$case_dir" Darwin "$arch_name"
  assert_file_exists "${case_dir}/bin/credimi-cloudflared"
  assert_executable "${case_dir}/bin/credimi-cloudflared"
  assert_contains "cloudflared-darwin-${arch_name/x86_64/amd64}.tgz" "${case_dir}/logs/curl.log"
  if [[ "$arch_name" == arm64 ]]; then
    assert_contains "cloudflared-darwin-arm64.tgz" "${case_dir}/logs/curl.log"
  fi
}

run_runner_failure_case() {
  local name="$1"
  local checksum="$2"
  local case_dir
  case_dir="$(new_case_dir)"
  asset="$(runner_asset Linux x86_64)"
  printf '%s' "$checksum" >"${case_dir}/checksums"
  mkdir -p "${case_dir}/bin"
  printf 'OLD-RUNNER' >"${case_dir}/bin/credimi-runner"
  if run_install "$case_dir" env FAKE_UNAME_S=Linux FAKE_UNAME_M=x86_64; then
    fail "${name} should fail"
  fi
  [[ "$(cat "${case_dir}/bin/credimi-runner")" == OLD-RUNNER ]] || fail "runner changed for ${name}"
  assert_file_absent "${case_dir}/logs/launch.log"
  assert_no_temporary_files "$case_dir"
}

run_cloudflared_mismatch_case() {
  local case_dir
  case_dir="$(new_case_dir)"
  asset="$(runner_asset Darwin arm64)"
  write_runner_checksum "$case_dir" "$asset"
  mkdir -p "${case_dir}/bin"
  printf 'OLD-RUNNER' >"${case_dir}/bin/credimi-runner"
  printf 'OLD-CLOUDFLARED' >"${case_dir}/bin/credimi-cloudflared"
  printf 'bad archive\n' >"${case_dir}/cloud-bad.tgz"
  if run_install "$case_dir" env FAKE_UNAME_S=Darwin FAKE_UNAME_M=arm64 MOCK_CLOUDFLARED_ARCHIVE="${case_dir}/cloud-bad.tgz"; then
    fail "cloudflared mismatch should fail"
  fi
  [[ "$(cat "${case_dir}/bin/credimi-runner")" == OLD-RUNNER ]] || fail "runner changed after cloudflared mismatch"
  [[ "$(cat "${case_dir}/bin/credimi-cloudflared")" == OLD-CLOUDFLARED ]] || fail "cloudflared changed after mismatch"
  assert_file_absent "${case_dir}/logs/launch.log"
  assert_no_temporary_files "$case_dir"
}

run_extraction_failure_case() {
  local case_dir
  local empty_sha
  local good_sha
  case_dir="$(new_case_dir)"
  IFS=$'\t' read -r empty_sha good_sha <"${case_dir}/archive-sha"
  prepare_install_script "$case_dir" "$empty_sha" "$empty_sha"
  asset="$(runner_asset Darwin x86_64)"
  write_runner_checksum "$case_dir" "$asset"
  mkdir -p "${case_dir}/bin"
  printf 'OLD-RUNNER' >"${case_dir}/bin/credimi-runner"
  printf 'OLD-CLOUDFLARED' >"${case_dir}/bin/credimi-cloudflared"
  if run_install "$case_dir" env FAKE_UNAME_S=Darwin FAKE_UNAME_M=x86_64 MOCK_CLOUDFLARED_ARCHIVE="${case_dir}/cloud-empty.tgz"; then
    fail "cloudflared extraction failure should fail"
  fi
  [[ "$(cat "${case_dir}/bin/credimi-runner")" == OLD-RUNNER ]] || fail "runner changed after extraction failure"
  [[ "$(cat "${case_dir}/bin/credimi-cloudflared")" == OLD-CLOUDFLARED ]] || fail "cloudflared changed after extraction failure"
  assert_file_absent "${case_dir}/logs/launch.log"
  assert_no_temporary_files "$case_dir"
}

run_custom_bin_dir_case() {
  local case_dir
  case_dir="$(new_case_dir)"
  asset="$(runner_asset Linux amd64)"
  write_runner_checksum "$case_dir" "$asset"
  custom_bin="${case_dir}/custom-bin"
  PATH="${case_dir}/mocks:${PATH}" HOME="${case_dir}/home" CREDIMI_RUNNER_BIN_DIR="$custom_bin" \
    MOCK_LOG_DIR="${case_dir}/logs" MOCK_RELEASE_JSON="${case_dir}/release.json" MOCK_RUNNER_BYTES="${case_dir}/runner-new" \
    MOCK_CHECKSUM_FILE="${case_dir}/checksums" MOCK_CLOUDFLARED_ARCHIVE="${case_dir}/cloud-good.tgz" \
    FAKE_UNAME_S=Linux FAKE_UNAME_M=amd64 sh "${case_dir}/install.sh" >"${case_dir}/stdout.log" 2>"${case_dir}/stderr.log"
  assert_file_exists "${custom_bin}/credimi-runner"
  assert_executable "${custom_bin}/credimi-runner"
  assert_contains "/releases/download/v9.9.9/${asset}" "${case_dir}/logs/curl.log"
}

run_unsupported_platform_case() {
  local case_dir
  case_dir="$(new_case_dir)"
  if run_install "$case_dir" env FAKE_UNAME_S=FreeBSD FAKE_UNAME_M=x86_64; then
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
  asset="$(runner_asset Linux x86_64)"
  write_runner_checksum "$case_dir" "$asset"
  run_install "$case_dir" env SHELL="/usr/bin/${shell_name}" FAKE_UNAME_S=Linux FAKE_UNAME_M=x86_64
  assert_contains "ACTION REQUIRED: credimi-runner is not available on PATH" "${case_dir}/stderr.log"
  assert_contains "Detected shell: ${shell_name}" "${case_dir}/stderr.log"
  assert_contains "$expected_message" "${case_dir}/stderr.log"
}

run_path_already_configured_case() {
  local case_dir
  case_dir="$(new_case_dir)"
  asset="$(runner_asset Linux x86_64)"
  write_runner_checksum "$case_dir" "$asset"
  mkdir -p "${case_dir}/bin"
  PATH="${case_dir}/bin:${case_dir}/mocks:${PATH}" HOME="${case_dir}/home" XDG_BIN_HOME="${case_dir}/bin" \
    MOCK_LOG_DIR="${case_dir}/logs" MOCK_RELEASE_JSON="${case_dir}/release.json" MOCK_RUNNER_BYTES="${case_dir}/runner-new" \
    MOCK_CHECKSUM_FILE="${case_dir}/checksums" MOCK_CLOUDFLARED_ARCHIVE="${case_dir}/cloud-good.tgz" \
    FAKE_UNAME_S=Linux FAKE_UNAME_M=x86_64 sh "${case_dir}/install.sh" >"${case_dir}/stdout.log" 2>"${case_dir}/stderr.log"
  assert_not_contains "ACTION REQUIRED" "${case_dir}/stderr.log"
}

run_default_bin_path_message_case() {
  local case_dir
  case_dir="$(new_case_dir)"
  asset="$(runner_asset Linux x86_64)"
  write_runner_checksum "$case_dir" "$asset"
  PATH="${case_dir}/mocks:${PATH}" HOME="${case_dir}/home" MOCK_LOG_DIR="${case_dir}/logs" \
    MOCK_RELEASE_JSON="${case_dir}/release.json" MOCK_RUNNER_BYTES="${case_dir}/runner-new" MOCK_CHECKSUM_FILE="${case_dir}/checksums" \
    MOCK_CLOUDFLARED_ARCHIVE="${case_dir}/cloud-good.tgz" SHELL=/bin/bash FAKE_UNAME_S=Linux FAKE_UNAME_M=x86_64 \
    sh "${case_dir}/install.sh" >"${case_dir}/stdout.log" 2>"${case_dir}/stderr.log"
  assert_file_exists "${case_dir}/home/.local/bin/credimi-runner"
  assert_contains 'export PATH="$HOME/.local/bin:$PATH"' "${case_dir}/stderr.log"
}

run_linux_case Linux x86_64
run_linux_case Linux arm64
run_darwin_case x86_64
run_darwin_case arm64
zero_sha="$(printf '0%.0s' {1..64})"
run_runner_failure_case "runner checksum mismatch" "${zero_sha}  credimi-runner-Linux-x86_64"$'\n'
run_runner_failure_case "missing runner checksum" "${zero_sha}  other"$'\n'
run_runner_failure_case "malformed runner checksum" "abc  credimi-runner-Linux-x86_64"$'\n'
run_runner_failure_case "duplicate runner checksum" "${zero_sha}  credimi-runner-Linux-x86_64"$'\n'"${zero_sha}  credimi-runner-Linux-x86_64"$'\n'
run_cloudflared_mismatch_case
run_extraction_failure_case
run_custom_bin_dir_case
run_unsupported_platform_case
run_shell_path_message_case bash "~/.bashrc"
run_shell_path_message_case zsh "~/.zshrc"
run_shell_path_message_case fish "fish_add_path"
run_shell_path_message_case dash "~/.profile"
run_shell_path_message_case elvish "your shell's startup file"
run_path_already_configured_case
run_default_bin_path_message_case

assert_contains 'CLOUDFLARED_VERSION="2026.8.2"' "$install_source"
assert_contains 'cloudflared-darwin-amd64.tgz' "$install_source"
assert_contains 'cloudflared-darwin-arm64.tgz' "$install_source"
assert_contains 'sha256sum' "$install_source"
assert_contains 'shasum -a 256' "$install_source"

printf 'install.sh tests passed\n'
