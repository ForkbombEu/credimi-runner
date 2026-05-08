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
DEFAULT_HOST_AVD_HOME_PATH="${HOME}/.android/avd"
DEFAULT_HOST_AVD_GOLDEN_PATH="${HOME}/avd-golden"
DEFAULT_GOLDEN_PATH="/avd-golden"
DEFAULT_BASE_AVD_ARCHIVE_URL="https://files.pn-a.com/credimi_base_image.tar.gz"
DEFAULT_GOLDEN_ARCHIVE_URL="https://files.pn-a.com/credimi_golden.tar.gz"
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

resolved_non_empty_value() {
  var_name="$1"
  fallback_value="${2-}"
  value="$(resolved_value "$var_name" "$fallback_value")"

  if [ -n "$value" ]; then
    printf '%s' "$value"
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

avd_assets_exist_for_name() {
  avd_home="$1"
  avd_name="$2"

  [ -d "${avd_home}/${avd_name}.avd" ] && [ -f "${avd_home}/${avd_name}.ini" ]
}

emulator_base_assets_present() {
  avd_home="$1"
  base_name="$2"

  if avd_assets_exist_for_name "$avd_home" "$base_name"; then
    return 0
  fi

  if [ "$base_name" != "$DEFAULT_BASE_NAME" ] && avd_assets_exist_for_name "$avd_home" "$DEFAULT_BASE_NAME"; then
    return 0
  fi

  return 1
}

golden_assets_present() {
  golden_root="$1"

  if [ -d "${golden_root}/credimi-golden" ]; then
    return 0
  fi

  [ -d "$golden_root" ] && [ "${golden_root##*/}" = "credimi-golden" ]
}

download_and_extract_tarball() {
  archive_url="$1"
  dest_dir="$2"
  archive_name="$3"
  archive_path="${dest_dir}/${archive_name}"

  mkdir -p "$dest_dir"
  rm -f "$archive_path"
  say "Downloading ${archive_name} into ${dest_dir}"
  curl -fsSL "$archive_url" -o "$archive_path"
  if tar -xzf "$archive_path" -C "$dest_dir"; then
    rm -f "$archive_path"
    return 0
  fi

  rm -f "$archive_path"
  die "failed to extract ${archive_name} into ${dest_dir}"
}

ensure_android_emulator_seed_assets() {
  avd_home="$1"
  golden_root="$2"
  base_name="$3"

  need_cmd curl
  need_cmd rm
  need_cmd tar
  mkdir -p "$avd_home" "$golden_root"

  if emulator_base_assets_present "$avd_home" "$base_name"; then
    say "Base AVD assets already present in ${avd_home}"
  else
    download_and_extract_tarball "${DEFAULT_BASE_AVD_ARCHIVE_URL}" "$avd_home" "credimi_base_image.tar.gz"
  fi

  if golden_assets_present "$golden_root"; then
    say "Golden AVD assets already present in ${golden_root}"
  else
    download_and_extract_tarball "${DEFAULT_GOLDEN_ARCHIVE_URL}" "$golden_root" "credimi_golden.tar.gz"
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

prompt_intro() {
  line_one="$1"
  line_two="${2-}"

  [ -n "$tty_path" ] || return 0
  printf '\n' >"$tty_path"
  prompt_intro_line "$line_one"
  [ -z "$line_two" ] || prompt_intro_line "$line_two"
}

prompt_intro_line() {
  line="$1"

  case "$line" in
    IMPORTANT:*)
      printf '%s%s%s\n' "${c_yellow}${c_bold}" "$line" "${c_reset}" >"$tty_path"
      ;;
    *)
      printf '%s%s%s\n' "${c_cyan}" "$line" "${c_reset}" >"$tty_path"
      ;;
  esac
}

repeat_char() {
  char="$1"
  count="$2"
  result=""

  while [ "$count" -gt 0 ]; do
    result="${result}${char}"
    count=$((count - 1))
  done

  printf '%s' "$result"
}

print_launch_command_box() {
  command_text="$1"
  title="Start the service with"
  width="${#title}"
  border=""

  if [ "${#command_text}" -gt "$width" ]; then
    width="${#command_text}"
  fi

  border="$(repeat_char "-" $((width + 2)))"
  printf '%s+%s+%s\n' "${c_cyan}${c_bold}" "$border" "${c_reset}" >&2
  printf '%s| %-*s |%s\n' "${c_cyan}${c_bold}" "$width" "$title" "${c_reset}" >&2
  printf '%s| %s%-*s%s |%s\n' "${c_cyan}${c_bold}" "${c_green}${c_bold}" "$width" "$command_text" "${c_cyan}${c_bold}" "${c_reset}" >&2
  printf '%s+%s+%s\n' "${c_cyan}${c_bold}" "$border" "${c_reset}" >&2
}

prompt_value_guided() {
  var_name="$1"
  label="$2"
  default_value="${3-}"
  secret="${4-0}"
  allow_empty="${5-0}"
  intro_one="$6"
  intro_two="${7-}"

  if ! env_var_is_set "$var_name" && [ -n "$tty_path" ]; then
    prompt_intro "$intro_one" "$intro_two"
  fi
  prompt_value "$var_name" "$label" "$default_value" "$secret" "$allow_empty"
}

prompt_choice_guided() {
  var_name="$1"
  label="$2"
  default_value="$3"
  choices="$4"
  intro_one="$5"
  intro_two="${6-}"

  if ! env_var_is_set "$var_name" && [ -n "$tty_path" ]; then
    prompt_intro "$intro_one" "$intro_two"
  fi
  prompt_choice "$var_name" "$label" "$default_value" "$choices"
}

menu_option_count() {
  printf '%s\n' "$1" | awk 'NF { count += 1 } END { print count + 0 }'
}

menu_option_value() {
  options="$1"
  selected_index="$2"
  printf '%s\n' "$options" | sed -n "${selected_index}p"
}

menu_option_index() {
  options="$1"
  target="$2"
  printf '%s\n' "$options" | awk -v target="$target" '
    $0 == target {
      print NR
      found = 1
      exit
    }
    END {
      if (!found) {
        print 0
      }
    }
  '
}

menu_terminal_rows() {
  if [ -n "$tty_path" ]; then
    tty_size="$(stty size <"$tty_path" 2>/dev/null || true)"
    tty_rows="$(printf '%s\n' "$tty_size" | awk 'NF >= 1 { print $1; exit }')"
    case "$tty_rows" in
      ''|*[!0-9]*)
        tty_rows=24
        ;;
    esac
    if [ "$tty_rows" -lt 8 ]; then
      tty_rows=8
    fi
    printf '%s' "$tty_rows"
    return 0
  fi

  printf '24'
}

menu_terminal_cols() {
  if [ -n "$tty_path" ]; then
    tty_size="$(stty size <"$tty_path" 2>/dev/null || true)"
    tty_cols="$(printf '%s\n' "$tty_size" | awk 'NF >= 2 { print $2; exit }')"
    case "$tty_cols" in
      ''|*[!0-9]*)
        tty_cols=80
        ;;
    esac
    if [ "$tty_cols" -lt 20 ]; then
      tty_cols=20
    fi
    printf '%s' "$tty_cols"
    return 0
  fi

  printf '80'
}

menu_visible_count() {
  terminal_rows="$1"
  visible_count=$((terminal_rows - 6))
  if [ "$visible_count" -lt 3 ]; then
    visible_count=3
  fi
  printf '%s' "$visible_count"
}

menu_initial_window_start() {
  option_count="$1"
  visible_count="$2"
  selected_index="$3"

  half_window=$((visible_count / 2))
  start_index=$((selected_index - half_window))
  if [ "$start_index" -lt 1 ]; then
    start_index=1
  fi

  max_start=$((option_count - visible_count + 1))
  if [ "$max_start" -lt 1 ]; then
    max_start=1
  fi
  if [ "$start_index" -gt "$max_start" ]; then
    start_index="$max_start"
  fi

  printf '%s' "$start_index"
}

menu_adjust_window_start() {
  option_count="$1"
  visible_count="$2"
  selected_index="$3"
  start_index="$4"

  end_index=$((start_index + visible_count - 1))
  if [ "$end_index" -gt "$option_count" ]; then
    end_index="$option_count"
  fi

  scroll_margin=2
  if [ "$visible_count" -le 5 ]; then
    scroll_margin=1
  fi

  upper_threshold=$((start_index + scroll_margin))
  lower_threshold=$((end_index - scroll_margin))

  if [ "$selected_index" -lt "$upper_threshold" ]; then
    start_index=$((selected_index - scroll_margin))
  elif [ "$selected_index" -gt "$lower_threshold" ]; then
    start_index=$((selected_index - visible_count + scroll_margin + 1))
  fi

  if [ "$start_index" -lt 1 ]; then
    start_index=1
  fi

  max_start=$((option_count - visible_count + 1))
  if [ "$max_start" -lt 1 ]; then
    max_start=1
  fi
  if [ "$start_index" -gt "$max_start" ]; then
    start_index="$max_start"
  fi

  printf '%s' "$start_index"
}

render_arrow_menu() {
  label="$1"
  options="$2"
  selected_index="$3"
  option_count="$4"
  visible_count="$5"
  start_index="$6"
  terminal_cols="$7"
  end_index=$((start_index + visible_count - 1))
  if [ "$end_index" -gt "$option_count" ]; then
    end_index="$option_count"
  fi

  text_width=$((terminal_cols - 4))
  if [ "$text_width" -lt 8 ]; then
    text_width=8
  fi

  printf '\033[H\033[J' >"$tty_path"
  printf '%s\n' "$(menu_truncate_text "$label" "$terminal_cols")" >"$tty_path"
  printf '%s\n' "$(menu_truncate_text "Showing ${start_index}-${end_index} of ${option_count}" "$terminal_cols")" >"$tty_path"
  if [ "$start_index" -gt 1 ]; then
    printf '%s%s%s\n' "${c_yellow}" "$(menu_truncate_text "^ more above" "$terminal_cols")" "${c_reset}" >"$tty_path"
  else
    printf '\n' >"$tty_path"
  fi

  slot_index=0
  while [ "$slot_index" -lt "$visible_count" ]; do
    current_index=$((start_index + slot_index))
    if [ "$current_index" -le "$option_count" ]; then
      option="$(menu_option_value "$options" "$current_index")"
      option="$(menu_truncate_text "$option" "$text_width")"
      if [ "$current_index" -eq "$selected_index" ]; then
        printf '%s> %s%s\n' "${c_cyan}${c_bold}" "$option" "${c_reset}" >"$tty_path"
      else
        printf '  %s\n' "$option" >"$tty_path"
      fi
    else
      printf '\n' >"$tty_path"
    fi
    slot_index=$((slot_index + 1))
  done

  if [ "$end_index" -lt "$option_count" ]; then
    printf '%s%s%s\n' "${c_yellow}" "$(menu_truncate_text "v more below" "$terminal_cols")" "${c_reset}" >"$tty_path"
  else
    printf '\n' >"$tty_path"
  fi
  printf '%s\n' "$(menu_truncate_text "Use arrow keys and press Enter. Ctrl-C cancels." "$terminal_cols")" >"$tty_path"
}

render_menu_option_row() {
  row_number="$1"
  option="$2"
  is_selected="$3"
  terminal_cols="$4"
  text_width=$((terminal_cols - 4))

  if [ "$text_width" -lt 8 ]; then
    text_width=8
  fi

  option="$(menu_truncate_text "$option" "$text_width")"
  printf '\033[%s;1H\033[2K' "$row_number" >"$tty_path"
  if [ "$is_selected" = "yes" ]; then
    printf '%s> %s%s' "${c_cyan}${c_bold}" "$option" "${c_reset}" >"$tty_path"
  else
    printf '  %s' "$option" >"$tty_path"
  fi
}

update_menu_selection_rows() {
  options="$1"
  start_index="$2"
  previous_selected_index="$3"
  selected_index="$4"
  visible_count="$5"
  terminal_cols="$6"

  previous_row_offset=$((previous_selected_index - start_index))
  if [ "$previous_row_offset" -ge 0 ] && [ "$previous_row_offset" -lt "$visible_count" ]; then
    previous_row_number=$((4 + previous_row_offset))
    previous_option="$(menu_option_value "$options" "$previous_selected_index")"
    render_menu_option_row "$previous_row_number" "$previous_option" "no" "$terminal_cols"
  fi

  selected_row_offset=$((selected_index - start_index))
  if [ "$selected_row_offset" -ge 0 ] && [ "$selected_row_offset" -lt "$visible_count" ]; then
    selected_row_number=$((4 + selected_row_offset))
    selected_option="$(menu_option_value "$options" "$selected_index")"
    render_menu_option_row "$selected_row_number" "$selected_option" "yes" "$terminal_cols"
  fi

  printf '\033[%s;1H' $((visible_count + 5)) >"$tty_path"
}

menu_truncate_text() {
  text="$1"
  max_width="$2"

  if [ "$max_width" -le 3 ]; then
    printf '%s' "$text" | awk -v max="$max_width" '{ print substr($0, 1, max) }'
    return 0
  fi

  printf '%s' "$text" | awk -v max="$max_width" '
    {
      if (length($0) <= max) {
        print $0
      } else {
        print substr($0, 1, max - 3) "..."
      }
    }
  '
}

menu_enter_fullscreen() {
  printf '\033[?1049h\033[?25l' >"$tty_path"
}

menu_leave_fullscreen() {
  printf '\033[0m\033[?25h\033[?1049l' >"$tty_path"
}

prompt_arrow_choice() {
  var_name="$1"
  label="$2"
  options="$3"
  default_value="${4-}"

  if env_var_is_set "$var_name"; then
    eval "existing_value=\${${var_name}}"
    printf '%s' "$existing_value"
    return 0
  fi

  option_count="$(menu_option_count "$options")"
  [ "$option_count" -gt 0 ] || die "no options available for ${label}"

  if [ -z "$tty_path" ]; then
    if [ -n "$default_value" ]; then
      printf '%s' "$default_value"
      return 0
    fi
    die "${var_name} is required for non-interactive install"
  fi

  selected_index=1
  if [ -n "$default_value" ]; then
    default_index="$(menu_option_index "$options" "$default_value")"
    if [ "$default_index" -gt 0 ]; then
      selected_index="$default_index"
    fi
  fi

  old_stty="$(stty -g <"$tty_path")"
  esc_char="$(printf '\033')"
  backspace_char="$(printf '\b')"
  delete_char="$(printf '\177')"
  ctrl_c_char="$(printf '\003')"
  newline_char="$(printf '\n')"
  carriage_return_char="$(printf '\r')"
  terminal_rows="$(menu_terminal_rows)"
  terminal_cols="$(menu_terminal_cols)"
  visible_count="$(menu_visible_count "$terminal_rows")"
  start_index="$(menu_initial_window_start "$option_count" "$visible_count" "$selected_index")"

  stty -echo -icanon min 1 time 0 <"$tty_path"
  menu_enter_fullscreen
  render_arrow_menu "$label" "$options" "$selected_index" "$option_count" "$visible_count" "$start_index" "$terminal_cols"
  while :; do
    previous_selected_index="$selected_index"
    previous_start_index="$start_index"
    previous_terminal_rows="$terminal_rows"
    previous_terminal_cols="$terminal_cols"
    previous_visible_count="$visible_count"
    char="$(dd bs=1 count=1 2>/dev/null <"$tty_path" || true)"
    case "$char" in
      "$newline_char"|"$carriage_return_char")
        break
        ;;
      "$ctrl_c_char")
        stty "$old_stty" <"$tty_path"
        menu_leave_fullscreen
        printf '\n' >"$tty_path"
        exit 130
        ;;
      "$esc_char")
        char_two="$(dd bs=1 count=1 2>/dev/null <"$tty_path" || true)"
        char_three="$(dd bs=1 count=1 2>/dev/null <"$tty_path" || true)"
        case "${char_two}${char_three}" in
          "[A"|"OA")
            if [ "$selected_index" -gt 1 ]; then
              selected_index=$((selected_index - 1))
            fi
            ;;
          "[B"|"OB")
            if [ "$selected_index" -lt "$option_count" ]; then
              selected_index=$((selected_index + 1))
            fi
            ;;
        esac
        ;;
      k)
        if [ "$selected_index" -gt 1 ]; then
          selected_index=$((selected_index - 1))
        fi
        ;;
      j)
        if [ "$selected_index" -lt "$option_count" ]; then
          selected_index=$((selected_index + 1))
        fi
        ;;
      "$backspace_char"|"$delete_char")
        ;;
    esac
    terminal_rows="$(menu_terminal_rows)"
    terminal_cols="$(menu_terminal_cols)"
    visible_count="$(menu_visible_count "$terminal_rows")"
    start_index="$(menu_adjust_window_start "$option_count" "$visible_count" "$selected_index" "$start_index")"
    if [ "$selected_index" = "$previous_selected_index" ] && \
       [ "$start_index" = "$previous_start_index" ] && \
       [ "$terminal_rows" = "$previous_terminal_rows" ] && \
       [ "$terminal_cols" = "$previous_terminal_cols" ] && \
       [ "$visible_count" = "$previous_visible_count" ]; then
      continue
    fi

    if [ "$start_index" = "$previous_start_index" ] && \
       [ "$terminal_rows" = "$previous_terminal_rows" ] && \
       [ "$terminal_cols" = "$previous_terminal_cols" ] && \
       [ "$visible_count" = "$previous_visible_count" ]; then
      update_menu_selection_rows "$options" "$start_index" "$previous_selected_index" "$selected_index" "$visible_count" "$terminal_cols"
    else
      render_arrow_menu "$label" "$options" "$selected_index" "$option_count" "$visible_count" "$start_index" "$terminal_cols"
    fi
  done
  stty "$old_stty" <"$tty_path"
  menu_leave_fullscreen
  printf '\n' >"$tty_path"
  menu_option_value "$options" "$selected_index"
}

simctl_list_devices() {
  xcrun simctl list devices available 2>/dev/null || xcrun simctl list devices 2>/dev/null
}

need_xcrun_simctl() {
  need_cmd xcrun
  xcrun simctl list devicetypes >/dev/null 2>&1 || die "xcrun simctl is required for ios_simulator installs"
  xcrun simctl list runtimes >/dev/null 2>&1 || die "xcrun simctl is required for ios_simulator installs"
}

ios_simulator_exists_named() {
  simulator_name="$1"
  devices_output="$(simctl_list_devices || true)"

  printf '%s\n' "$devices_output" | awk -v target="$simulator_name" '
    /^[[:space:]]/ {
      line = $0
      sub(/^[[:space:]]+/, "", line)
      if (index(line, target " (") == 1) {
        found = 1
        exit 0
      }
    }
    END {
      exit(found ? 0 : 1)
    }
  '
}

ios_simulator_present() {
  base_name="$1"
  ios_simulator_exists_named "$base_name"
}

ios_simulator_device_type_entries() {
  xcrun simctl list devicetypes | sed -n '
    s/^[[:space:]]*//
    /^==/d
    /^$/d
    s/^\(.*\) (\(com\.apple\.CoreSimulator\.SimDeviceType\.[^)]*\))$/\1	\2/p
  '
}

ios_simulator_runtime_entries() {
  xcrun simctl list runtimes | sed -n '
    s/^[[:space:]]*//
    /^==/d
    /^$/d
    /unavailable/d
    s/^\(.*\) - \(com\.apple\.CoreSimulator\.SimRuntime\.[^[:space:]]*\)$/\1	\2/p
  '
}

simctl_entry_options() {
  printf '%s\n' "$1" | awk -F '\t' 'NF >= 2 { print $1 }'
}

simctl_entry_identifier_for_label() {
  entries="$1"
  selected_label="$2"
  printf '%s\n' "$entries" | awk -F '\t' -v label="$selected_label" '$1 == label { print $2; exit }'
}

simctl_entry_label_for_identifier() {
  entries="$1"
  selected_identifier="$2"
  printf '%s\n' "$entries" | awk -F '\t' -v identifier="$selected_identifier" '$2 == identifier { print $1; exit }'
}

choose_simctl_identifier() {
  var_name="$1"
  label="$2"
  entries="$3"

  entry_count="$(menu_option_count "$(simctl_entry_options "$entries")")"
  [ "$entry_count" -gt 0 ] || die "no options available for ${label}"

  preset_identifier="$(resolved_value "$var_name")"
  if [ -n "$preset_identifier" ]; then
    preset_label="$(simctl_entry_label_for_identifier "$entries" "$preset_identifier")"
    [ -n "$preset_label" ] || die "${var_name} must match one of the available ${label} entries"
    printf '%s' "$preset_identifier"
    return 0
  fi

  options="$(simctl_entry_options "$entries")"
  selected_label="$(prompt_arrow_choice "$var_name" "$label" "$options")"
  selected_identifier="$(simctl_entry_identifier_for_label "$entries" "$selected_label")"
  [ -n "$selected_identifier" ] || die "failed to resolve selected ${label}"
  printf '%s' "$selected_identifier"
}

ensure_ios_simulator_exists() {
  base_name="$1"

  need_xcrun_simctl
  if ios_simulator_present "$base_name"; then
    say "iOS Simulator ${base_name} already exists"
    return 0
  fi

  prompt_intro "No iOS Simulator named ${base_name} was found." \
    "Choose a device type and runtime to create it now."
  device_type_entries="$(ios_simulator_device_type_entries)"
  device_type_identifier="$(choose_simctl_identifier IOS_SIMULATOR_DEVICE_TYPE_IDENTIFIER "iOS Simulator device type" "$device_type_entries")"
  runtime_entries="$(ios_simulator_runtime_entries)"
  runtime_identifier="$(choose_simctl_identifier IOS_SIMULATOR_RUNTIME_IDENTIFIER "iOS Simulator runtime" "$runtime_entries")"

  say "Creating iOS Simulator ${base_name}"
  xcrun simctl create "$base_name" "$device_type_identifier" "$runtime_identifier" >/dev/null 2>&1 || \
    die "failed to create iOS Simulator ${base_name}"
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

normalize_service_mode() {
  case "$1" in
    auto|quick)
      printf 'auto'
      ;;
    manual|direct)
      printf 'manual'
      ;;
    cloudflare-managed|named)
      printf 'cloudflare-managed'
      ;;
    *)
      die "unsupported CREDIMI_SERVICE_MODE value: $1"
      ;;
  esac
}

resolved_runner_public_url() {
  value="$(resolved_value RUNNER_PUBLIC_URL)"
  if [ -n "$value" ]; then
    printf '%s' "$value"
    return 0
  fi

  resolved_value RUNNER_PUBLIC_IP
}

clear_avdctl_ssh_config() {
  AVDCTL_SSH_TARGET=""
  AVDCTL_SSH_PASSWORD=""
  AVDCTL_SSH_KNOWN_HOSTS_PATH=""
  AVDCTL_SUDO=""
  AVDCTL_SUDO_PASSWORD=""
}

configure_avdctl_ssh() {
  avdctl_ssh_choice="$(prompt_choice_guided AVDCTL_USE_SSH_PROMPT "Use avdctl via SSH (yes/no)" "$(default_avdctl_ssh_choice)" "yes no" \
    "Choose here whether emulator or redroid management should happen on another machine over SSH." \
    "The default is no; choose yes only if the actual emulator/redroid host is remote.")"
  case "$avdctl_ssh_choice" in
    yes)
      AVDCTL_SSH_TARGET="$(prompt_value_guided AVDCTL_SSH_TARGET "AVDCTL SSH target" "$(resolved_value AVDCTL_SSH_TARGET)" 0 0 \
        "Type here the SSH destination of that remote machine, usually in the form credimi@host." \
        "There is no useful default here unless one was already saved.")"
      AVDCTL_SSH_PASSWORD="$(prompt_value_guided AVDCTL_SSH_PASSWORD "AVDCTL SSH password (optional)" "$(resolved_value AVDCTL_SSH_PASSWORD)" 1 1 \
        "Type here the SSH password for that remote machine if password login is used." \
        "You can leave it empty when SSH keys are used instead.")"
      if [ "$CREDIMI_RUNNER_BACKEND" = "container" ]; then
        AVDCTL_SSH_KNOWN_HOSTS_PATH="$(resolved_value AVDCTL_SSH_KNOWN_HOSTS_PATH "${HOME}/.ssh/known_hosts")"
      else
        AVDCTL_SSH_KNOWN_HOSTS_PATH=""
      fi
      avdctl_sudo_choice="$(prompt_choice_guided AVDCTL_USE_SUDO_PROMPT "Does avdctl need sudo (yes/no)" "$(default_avdctl_sudo_choice)" "yes no" \
        "Choose here whether avdctl commands on the remote machine need administrator privileges." \
        "The default follows the saved configuration; choose yes only if that remote machine requires sudo for these operations.")"
      case "$avdctl_sudo_choice" in
        yes)
          AVDCTL_SUDO="true"
          AVDCTL_SUDO_PASSWORD="$(prompt_value_guided AVDCTL_SUDO_PASSWORD "AVDCTL sudo password" "$(resolved_value AVDCTL_SUDO_PASSWORD)" 1 0 \
            "Type here the sudo password for the remote machine if those avdctl operations need it." \
            "There is no useful default here.")"
          ;;
        *)
          AVDCTL_SUDO="false"
          AVDCTL_SUDO_PASSWORD=""
          ;;
      esac
      ;;
    *)
      clear_avdctl_ssh_config
      AVDCTL_SUDO="false"
      ;;
  esac
}

write_compose_file() {
  compose_file="$1"
  runner_mode="${CREDIMI_CONTAINER_MODE:-${DEFAULT_CONTAINER_MODE}}"
  runner_image="${RUNNER_IMAGE:-${DEFAULT_PHONE_IMAGE}}"
  runner_ssh_known_hosts_volume=""
  runner_no_device_volumes_block=""
  caddy_network_block='    extra_hosts:
      - "host.docker.internal:host-gateway"
    networks:
      - ingress'
  caddy_proxy_target='host.docker.internal:${RUNNER_PORT:-8050}'
  tunnel_network_block='    extra_hosts:
      - "host.docker.internal:host-gateway"
    networks:
      - ingress'
  tunnel_url='http://caddy:80'
  runner_connectivity_block='    expose:
      - "8050"
    labels:
      caddy: "${RUNNER_CADDY_SITE:-:80}"
      caddy.reverse_proxy: "{{upstreams 8050}}"
    networks:
      - ingress'

  if [ "${CREDIMI_RUNNER_BACKEND:-}" = "container" ] &&
    [ "${CREDIMI_SERVICE_MODE:-auto}" = "manual" ] &&
    [ "$(uname -s)" = "Linux" ]; then
    runner_connectivity_block='    network_mode: host'
  fi

  if [ "${CREDIMI_RUNNER_BACKEND:-}" = "container" ] &&
    [ "${CREDIMI_SERVICE_MODE:-auto}" = "auto" ] &&
    [ "$(uname -s)" = "Linux" ]; then
    case "$runner_mode" in
      usb|wifi)
        caddy_network_block='    network_mode: host'
        caddy_proxy_target='127.0.0.1:${RUNNER_PORT:-8050}'
        tunnel_network_block='    network_mode: host'
        tunnel_url='http://127.0.0.1:80'
        ;;
    esac
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
    network_mode: host
    labels:
      caddy: "\${RUNNER_CADDY_SITE:-:80}"
      caddy.reverse_proxy: "${caddy_proxy_target}"
EOF
      ;;
    usb)
      cat >>"$compose_file" <<EOF
  runner:
    image: ${runner_image}
    restart: unless-stopped
    command:
      - --host-adb
      - --usb
    env_file:
      - .env
    environment:
      PORT: "\${RUNNER_PORT:-${DEFAULT_RUNNER_PORT}}"
      ADB_SERVER_SOCKET: "\${ADB_SERVER_SOCKET:-tcp:127.0.0.1:5037}"
    volumes:
      - adbkeys:/root/.android
${runner_ssh_known_hosts_volume}
    network_mode: host
    labels:
      caddy: "\${RUNNER_CADDY_SITE:-:80}"
      caddy.reverse_proxy: "${caddy_proxy_target}"
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
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - caddy_data:/data
      - caddy_config:/config
EOF
  cat >>"$compose_file" <<EOF
${caddy_network_block}

  tunnel:
    image: cloudflare/cloudflared:latest
    restart: unless-stopped
    command: tunnel --no-autoupdate --url \${CREDIMI_TUNNEL_URL:-${tunnel_url}}
${tunnel_network_block}
EOF
  cat >>"$compose_file" <<'EOF'

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

normalize_service_mode() {
  case "$1" in
    auto|quick)
      printf 'auto'
      ;;
    manual|direct)
      printf 'manual'
      ;;
    cloudflare-managed|named)
      printf 'cloudflare-managed'
      ;;
    *)
      printf 'invalid CREDIMI_SERVICE_MODE: %s\n' "$1" >&2
      return 1
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

configure_auth_headers() {
  if [[ -n "${CREDIMI_USER_API_KEY:-}" ]]; then
    auth_headers=(-H "Credimi-Api-Key: ${CREDIMI_USER_API_KEY}")
    return 0
  fi

  if [[ -n "${CREDIMI_INTERNAL_ADMIN_KEY:-}" ]]; then
    auth_headers=(-H "Credimi-Api-Key: ${CREDIMI_INTERNAL_ADMIN_KEY}")
    return 0
  fi

  printf 'missing Credimi credentials: set CREDIMI_USER_API_KEY or CREDIMI_INTERNAL_ADMIN_KEY\n' >&2
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

  if [[ "${mode}" == "cloudflare-managed" ]]; then
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

mode_arg="${1:-${CREDIMI_SERVICE_MODE:-auto}}"
case "${mode_arg}" in
  down|update-image)
    mode="${mode_arg}"
    ;;
  *)
    mode="$(normalize_service_mode "${mode_arg}")"
    ;;
esac
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

runner_image_id() {
  local image="$1"

  docker image inspect --format '{{.Id}}' "${image}" 2>/dev/null |
    head -n 1
}

check_runner_image() {
  local image="${RUNNER_IMAGE:-}"
  local before_id
  local after_id

  [[ -n "${image}" ]] || {
    printf 'RUNNER_IMAGE is required to check the container image\n' >&2
    exit 1
  }

  require_cmd docker
  before_id="$(runner_image_id "${image}")"
  docker pull "${image}"
  after_id="$(runner_image_id "${image}")"

  if [[ -z "${before_id}" ]]; then
    printf 'Pulled runner image: %s\n' "${image}" >&2
  elif [[ "${before_id}" == "${after_id}" ]]; then
    printf 'Runner image is up to date: %s\n' "${image}" >&2
  else
    printf 'Updated runner image: %s\n' "${image}" >&2
  fi
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
  auto)
    compose_services+=(tunnel)
    ;;
  cloudflare-managed)
    if [[ -z "${CLOUDFLARE_TUNNEL_TOKEN:-}" ]]; then
      printf 'CLOUDFLARE_TUNNEL_TOKEN is required in cloudflare-managed mode\n' >&2
      exit 1
    fi
    if [[ -z "${RUNNER_DOMAIN:-}" ]]; then
      printf 'RUNNER_DOMAIN is required in cloudflare-managed mode\n' >&2
      exit 1
    fi
    compose_services+=(tunnel_named)
    ;;
  manual)
    RUNNER_PUBLIC_URL="${RUNNER_PUBLIC_URL:-${RUNNER_PUBLIC_IP:-}}"
    if [[ -z "${RUNNER_PUBLIC_URL:-}" ]]; then
      printf 'RUNNER_PUBLIC_URL is required in manual mode\n' >&2
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
    check_runner_image
    exit 0
    ;;
  *)
    printf 'usage: %s [auto|manual|cloudflare-managed|down|update-image]\n' "$(basename "$0")" >&2
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

if [[ "${mode}" == "manual" ]]; then
  register_mobile_runner "${RUNNER_PUBLIC_URL}" "${RUNNER_PUBLIC_PORT:-}"
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
RUNNER_PUBLIC_URL=${RUNNER_PUBLIC_URL}
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
  append_env_if_missing "$env_file" "RUNNER_PUBLIC_URL" "${RUNNER_PUBLIC_URL}"
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
  RUNNER_PUBLIC_URL="${RUNNER_PUBLIC_URL-}"
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

  CREDIMI_URL="$(resolved_value CREDIMI_URL "${DEFAULT_CREDIMI_URL}")"
  TEMPORAL_ADDRESS="$(resolved_value TEMPORAL_ADDRESS "${DEFAULT_TEMPORAL_ADDRESS}")"
  otel_enabled_choice="$(default_yes_no_choice OTEL_ENABLED yes)"
  case "$otel_enabled_choice" in
    yes|true|TRUE|True|1|on|ON|On)
      OTEL_ENABLED="true"
      OTEL_EXPORTER_OTLP_ENDPOINT="$(resolved_value OTEL_EXPORTER_OTLP_ENDPOINT "${DEFAULT_OTEL_EXPORTER_OTLP_ENDPOINT}")"
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
  elif [ -n "$(resolved_value CREDIMI_INTERNAL_ADMIN_KEY)" ]; then
    auth_mode_default="admin"
  else
    auth_mode_default="api_key"
  fi
  auth_mode="$(prompt_choice_guided CREDIMI_INSTALL_AUTH_MODE "Auth mode (api_key/admin)" "${auth_mode_default}" "api_key admin" \
    "Choose here how this installer should log into Credimi." \
    "Use api_key if you already have a Credimi user API key; use admin only if you are setting this up with the internal admin API key.")"

  if [ "$auth_mode" = "api_key" ]; then
    CREDIMI_USER_API_KEY="$(prompt_value_guided CREDIMI_USER_API_KEY "Credimi user API key" "$(resolved_value CREDIMI_USER_API_KEY)" 1 0 \
      "Paste here the Credimi user API key you want this runner to use." \
      "There is no useful default here; if you choose api_key, this is the key you must provide.")"
    CREDIMI_INTERNAL_ADMIN_KEY=""
  else
    CREDIMI_INTERNAL_ADMIN_KEY="$(prompt_value_guided CREDIMI_INTERNAL_ADMIN_KEY "Internal admin key" "$(resolved_value CREDIMI_INTERNAL_ADMIN_KEY)" 1 0 \
      "Paste here the privileged Credimi API key to use for setup." \
      "There is no useful default here; provide it only if you are using the admin setup path (this implies you are using a different deployment than credimi.io).")"
    CREDIMI_USER_API_KEY=""
  fi

  existing_runner_id="$(resolved_value CREDIMI_RUNNER_ID)"
  use_existing_runner_id="no"
  if [ -n "$existing_runner_id" ]; then
    use_existing_runner_id="$(prompt_choice_guided CREDIMI_USE_EXISTING_RUNNER_ID "Use existing runner ID ${existing_runner_id}? (yes/no)" "yes" "yes no" \
      "Choose here whether this installation should keep using the runner already saved in the existing configuration." \
      "The default is yes; choose no only if you want to register this machine as a different runner.")"
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
    CREDIMI_RUNNER_NAME="$(prompt_value_guided CREDIMI_RUNNER_NAME "Runner name" "${runner_name_default}" 0 0 \
      "Type here the name you want to see for this runner inside Credimi." \
      "If a previous value exists it will be offered as the default; change it only if you want this runner to appear with a different name.")"
    if [ "$auth_mode" = "admin" ]; then
      runner_org_default="$(resolved_value CREDIMI_RUNNER_ORGANIZATION "$(runner_org_from_id "${existing_runner_id}")")"
      CREDIMI_RUNNER_ORGANIZATION="$(prompt_value_guided CREDIMI_RUNNER_ORGANIZATION "Runner organization canonified name" "${runner_org_default}" 0 0 \
        "Type here the organization slug that should own this runner in Credimi; you can find it in https://credimi.io/my/organization in the top right of the page." \
        "The page has a copy button and a preview button.")"
    else
      CREDIMI_RUNNER_ORGANIZATION=""
    fi
  fi
  CREDIMI_RUNNER_DESCRIPTION="$(prompt_value_guided CREDIMI_RUNNER_DESCRIPTION "Runner description (optional)" "$(resolved_value CREDIMI_RUNNER_DESCRIPTION)" 0 1 \
    "Type here an optional note to help people understand what this runner is for, for example \"Pixel 6 with Android 16 on\" or \"iOS Simulator version 26\"." \
    "The field is not mandatory, but we recommend filling it with useful information.")"

  runner_type_options="$(runner_type_choices)"
  CREDIMI_RUNNER_TYPE="$(prompt_choice_guided CREDIMI_RUNNER_TYPE "Mobile runner type (${runner_type_options})" "$(default_runner_type "${CREDIMI_RUNNER_BACKEND}")" "${runner_type_options}" \
    "IMPORTANT: choose here what kind of device this runner should control: Android phone, Android emulator, redroid, iOS simulator, or iOS phone." \
    "This choice has implication on what happens later.")"
  validate_runner_type_supported "${CREDIMI_RUNNER_TYPE}"
  if [ "$CREDIMI_RUNNER_TYPE" = "ios_simulator" ]; then
    need_xcrun_simctl
  fi
  resolve_install_runner_identity
  service_mode_default="$(normalize_service_mode "$(resolved_value CREDIMI_SERVICE_MODE "auto")")"
  service_mode_choice="$(prompt_choice_guided CREDIMI_SERVICE_MODE "Networking (auto/manual/cloudflare-managed)" "${service_mode_default}" "auto manual cloudflare-managed" \
    "Choose here how Credimi should reach this runner over the network." \
    "The default is auto; auto uses a temporary domain via a Cloudflare free tunneling service, manual uses the IP or domain that you did setup yourself for this runner, and cloudflare-managed uses your Cloudflare tunnel token and setup.")"
  CREDIMI_SERVICE_MODE="$(normalize_service_mode "$service_mode_choice")"
  RUNNER_HOST="$(resolved_value RUNNER_HOST "${DEFAULT_RUNNER_HOST}")"
  RUNNER_PORT="$(resolved_value RUNNER_PORT "${DEFAULT_RUNNER_PORT}")"

  case "$CREDIMI_SERVICE_MODE" in
    cloudflare-managed)
      RUNNER_CADDY_SITE="$(resolved_value RUNNER_CADDY_SITE "${DEFAULT_RUNNER_CADDY_SITE}")"
      RUNNER_DOMAIN="$(prompt_value_guided RUNNER_DOMAIN "Public runner domain" "$(resolved_value RUNNER_DOMAIN)" 0 0 \
        "Type here the public domain name that should point to this runner when using a named Cloudflare tunnel (you chose cloudflare-managed before)." \
        "There is no useful default here, because it depends on your own domain.")"
      CLOUDFLARE_TUNNEL_TOKEN="$(prompt_value_guided CLOUDFLARE_TUNNEL_TOKEN "Cloudflare tunnel token" "$(resolved_value CLOUDFLARE_TUNNEL_TOKEN)" 1 0 \
        "Paste here the token of the Cloudflare named tunnel that should expose this runner." \
        "There is no useful default here.")"
      RUNNER_PUBLIC_URL=""
      RUNNER_PUBLIC_PORT=""
      ;;
    manual)
      RUNNER_CADDY_SITE="$(resolved_value RUNNER_CADDY_SITE)"
      RUNNER_DOMAIN=""
      CLOUDFLARE_TUNNEL_TOKEN=""
      RUNNER_PUBLIC_URL="$(prompt_value_guided RUNNER_PUBLIC_URL "Public runner URL" "$(resolved_runner_public_url)" 0 0 \
        "Type here the public URL that Credimi can use to reach this runner directly (you chose manual before)." \
        "IMPORTANT: if you are using an IP address, which is allowed, you need to use http:// or https:// before the IP.")"
      RUNNER_PUBLIC_PORT="$(prompt_value_guided RUNNER_PUBLIC_PORT "Public runner port (optional)" "$(resolved_value RUNNER_PUBLIC_PORT)" 0 1 \
        "Type here the public port that belongs to that public URL (you chose manual before)." \
        "You can leave it empty if the default public port is already correct for your setup.")"
      ;;
    *)
      RUNNER_CADDY_SITE="$(resolved_value RUNNER_CADDY_SITE "${DEFAULT_RUNNER_CADDY_SITE}")"
      RUNNER_DOMAIN=""
      CLOUDFLARE_TUNNEL_TOKEN=""
      RUNNER_PUBLIC_URL=""
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
        ANDROID_KEYS_DIR="$(resolved_value ANDROID_KEYS_DIR "${HOME}/.android")"
        HOST_AVD_HOME_PATH="$(resolved_value HOST_AVD_HOME_PATH "${DEFAULT_HOST_AVD_HOME_PATH}")"
        HOST_AVD_GOLDEN_PATH="$(resolved_value HOST_AVD_GOLDEN_PATH "${DEFAULT_HOST_AVD_GOLDEN_PATH}")"
        BASE_NAME="$(resolved_value BASE_NAME "${DEFAULT_BASE_NAME}")"
        GOLDEN_PATH="$(resolved_value GOLDEN_PATH "${DEFAULT_GOLDEN_PATH}")"
      else
        CREDIMI_CONTAINER_MODE=""
        ANDROID_KEYS_DIR=""
        HOST_AVD_HOME_PATH=""
        HOST_AVD_GOLDEN_PATH=""
        BASE_NAME="$(resolved_value BASE_NAME "${DEFAULT_BASE_NAME}")"
        GOLDEN_PATH="$(resolved_value GOLDEN_PATH "${DEFAULT_GOLDEN_PATH}")"
      fi
      configure_avdctl_ssh
      if [ "$CREDIMI_RUNNER_BACKEND" = "container" ]; then
        ensure_android_emulator_seed_assets "${HOST_AVD_HOME_PATH}" "${HOST_AVD_GOLDEN_PATH}" "${BASE_NAME}"
      fi
      REDROID_DATA_DIR=""
      REDROID_DATA_TAR=""
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
      BASE_NAME="$(resolved_value BASE_NAME "${DEFAULT_BASE_NAME}")"
      GOLDEN_PATH=""
      AVDCTL_SSH_TARGET=""
      AVDCTL_SSH_PASSWORD=""
      AVDCTL_SSH_KNOWN_HOSTS_PATH=""
      AVDCTL_SUDO=""
      AVDCTL_SUDO_PASSWORD=""
      REDROID_DATA_DIR=""
      REDROID_DATA_TAR=""
      ensure_ios_simulator_exists "${BASE_NAME}"
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
        CREDIMI_RUNNER_DEVICE_MODE="no_device"
      else
        CREDIMI_RUNNER_DEVICE_MODE="$(prompt_choice_guided CREDIMI_RUNNER_DEVICE_MODE "Android connection mode (${android_device_mode_options})" "$(default_android_device_mode "${CREDIMI_RUNNER_TYPE}")" "${android_device_mode_options}" \
          "Choose here how this runner should reach the Android device." \
          "The default depends on the runner type and saved configuration: if you are using a physical Android phone connected to this machine via USB, choose usb; if the phone is connected via Wi-Fi, choose wifi.")"
      fi
      RUNNER_IMAGE="$(explicit_env_or_default RUNNER_IMAGE "${DEFAULT_PHONE_IMAGE}")"
      ANDROID_KEYS_DIR=""
      HOST_AVD_HOME_PATH=""
      HOST_AVD_GOLDEN_PATH=""
      BASE_NAME=""
      GOLDEN_PATH=""

      case "$CREDIMI_RUNNER_DEVICE_MODE" in
        no_device)
          CREDIMI_RUNNER_WIFI_IP="$(prompt_value_guided CREDIMI_RUNNER_WIFI_IP "Android Wi-Fi IP" "$(resolved_value CREDIMI_RUNNER_WIFI_IP)" 0 0 \
            "Type here the IP address of the Android device or Android runtime reachable over the network." \
            "There is no universal default here.")"
          CREDIMI_RUNNER_WIFI_PORT="$(prompt_value_guided CREDIMI_RUNNER_WIFI_PORT "Android Wi-Fi port" "$(resolved_non_empty_value CREDIMI_RUNNER_WIFI_PORT "${DEFAULT_ANDROID_WIFI_PORT}")" 0 0 \
            "Type here the ADB port of that network-connected Android device or runtime." \
            "The default value is 5555; write something else only if your device uses a different ADB port.")"
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
          CREDIMI_RUNNER_SERIAL="$(prompt_value_guided CREDIMI_RUNNER_SERIAL "Android device serial" "$usb_serial_default" 0 0 \
            "Type here the serial of the Android device connected over USB." \
            "If the script can detect exactly one connected device, it will offer that as the default.")"
          CREDIMI_RUNNER_WIFI_IP=""
          CREDIMI_RUNNER_WIFI_PORT=""
          if [ "$CREDIMI_RUNNER_BACKEND" = "container" ]; then
            CREDIMI_CONTAINER_MODE="usb"
          else
            CREDIMI_CONTAINER_MODE=""
          fi
          ;;
        wifi)
          CREDIMI_RUNNER_WIFI_IP="$(prompt_value_guided CREDIMI_RUNNER_WIFI_IP "Android Wi-Fi IP" "$(resolved_value CREDIMI_RUNNER_WIFI_IP)" 0 0 \
            "Type here the IP address of the Android device or Android runtime reachable over the network." \
            "There is no universal default here.")"
          CREDIMI_RUNNER_WIFI_PORT="$(prompt_value_guided CREDIMI_RUNNER_WIFI_PORT "Android Wi-Fi port" "$(resolved_non_empty_value CREDIMI_RUNNER_WIFI_PORT "${DEFAULT_ANDROID_WIFI_PORT}")" 0 0 \
            "Type here the ADB port of that network-connected Android device or runtime." \
            "The default value is 5555; write something else only if your device uses a different ADB port.")"
          CREDIMI_RUNNER_SERIAL="${CREDIMI_RUNNER_WIFI_IP}:${CREDIMI_RUNNER_WIFI_PORT}"
          if [ "$CREDIMI_RUNNER_BACKEND" = "container" ]; then
            CREDIMI_CONTAINER_MODE="wifi"
          else
            CREDIMI_CONTAINER_MODE=""
          fi
          ;;
      esac

      if [ "$CREDIMI_RUNNER_TYPE" = "redroid" ]; then
        configure_avdctl_ssh
        REDROID_DATA_DIR="$(resolved_value REDROID_DATA_DIR "${DEFAULT_REDROID_DATA_DIR}")"
        REDROID_DATA_TAR="$(resolved_value REDROID_DATA_TAR "${DEFAULT_REDROID_DATA_TAR}")"
      else
        clear_avdctl_ssh_config
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
    say "- ${c_cyan}${c_bold}${binary_path}${c_reset}"
  fi
  say "- ${c_cyan}${c_bold}${launcher_path}${c_reset}"
  say "- ${c_cyan}${c_bold}${compose_file}${c_reset}"
  say "- ${c_cyan}${c_bold}${env_file}${c_reset}"
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
        say "Configured Android transport: USB via host ADB (${CREDIMI_RUNNER_SERIAL})."
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
    say "Updated configuration in ${c_cyan}${c_bold}${env_file}${c_reset}."
  fi
  say ""
  print_launch_command_box "${PROJECT_NAME}-service"
  say ""
  say "Other commands:"
  say "${PROJECT_NAME}-service auto"
  say "${PROJECT_NAME}-service manual"
  say "${PROJECT_NAME}-service cloudflare-managed"
  say "${PROJECT_NAME}-service down"
  say "${PROJECT_NAME}-service update-image"
}

main "$@"
