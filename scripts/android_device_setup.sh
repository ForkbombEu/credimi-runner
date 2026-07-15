#!/usr/bin/env bash
set -euo pipefail

SCRIPT_NAME="$(basename "$0")"

DEVICE=""
DO_CONFIG=0

# Desired Credimi runner baseline.
DESIRED_STAY_ON_WHILE_PLUGGED_IN="3"     # 3 = AC + USB
DESIRED_SCREEN_OFF_TIMEOUT="1800000"     # 30 minutes
DESIRED_SCREENSAVER_ENABLED="0"
DESIRED_ACCELEROMETER_ROTATION="0"
DESIRED_USER_ROTATION="0"
DESIRED_LOCKSCREEN_DISABLED="1"
DESIRED_LOW_POWER="0"

usage() {
  cat <<EOF
Usage:
  ./$SCRIPT_NAME
  ./$SCRIPT_NAME -d <DEVICE_SERIAL>
  ./$SCRIPT_NAME -d <DEVICE_SERIAL> --config

Examples:
  ./$SCRIPT_NAME
  ./$SCRIPT_NAME -d 1B171FDF6008G7
  ./$SCRIPT_NAME -d 37131JEHN05321 --config
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    -d|--device)
      if [ "${2:-}" = "" ]; then
        echo "ERROR: missing value for $1" >&2
        exit 1
      fi
      DEVICE="$2"
      shift 2
      ;;
    --config)
      DO_CONFIG=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "ERROR: unknown argument: $1" >&2
      usage
      exit 1
      ;;
  esac
done

get_attached_devices() {
  adb devices | awk 'NR > 1 && $2 == "device" { print $1 }'
}

select_device() {
  mapfile -t DEVICES < <(get_attached_devices)

  if [ -n "$DEVICE" ]; then
    if ! printf '%s\n' "${DEVICES[@]}" | grep -qx "$DEVICE"; then
      echo "ERROR: selected device is not attached or not authorized: $DEVICE" >&2
      echo
      echo "Attached devices:"
      adb devices
      exit 1
    fi
    return
  fi

  if [ "${#DEVICES[@]}" -eq 0 ]; then
    echo "ERROR: no ADB device attached." >&2
    echo
    adb devices
    exit 1
  fi

  if [ "${#DEVICES[@]}" -gt 1 ]; then
    echo "More than one ADB device is attached:"
    echo
    adb devices
    echo
    echo "Please select the device you want to run the script on, for example:"
    for dev in "${DEVICES[@]}"; do
      echo "  ./$SCRIPT_NAME -d $dev"
    done
    echo
    echo "To configure a selected device, run:"
    for dev in "${DEVICES[@]}"; do
      echo "  ./$SCRIPT_NAME -d $dev --config"
    done
    exit 1
  fi

  DEVICE="${DEVICES[0]}"
}

adb_get_setting() {
  local namespace="$1"
  local key="$2"
  adb -s "$DEVICE" shell settings get "$namespace" "$key" 2>/dev/null | tr -d '\r'
}

adb_put_setting() {
  local namespace="$1"
  local key="$2"
  local value="$3"
  adb -s "$DEVICE" shell settings put "$namespace" "$key" "$value"
}

print_header() {
  echo
  echo "============================================================"
  echo "$1"
  echo "============================================================"
}

setting_needs_config() {
  local label="$1"
  local actual="$2"
  local desired="$3"

  if [ "$actual" != "$desired" ]; then
    CONFIG_NEEDED=1
    CONFIG_REASONS+=("$label is '$actual', expected '$desired'")
  fi
}

inspect_config_need() {
  CONFIG_NEEDED=0
  CONFIG_REASONS=()

  local screen_off_timeout
  local stay_on_while_plugged_in
  local screensaver_enabled
  local accelerometer_rotation
  local user_rotation
  local lockscreen_disabled
  local low_power

  screen_off_timeout="$(adb_get_setting system screen_off_timeout)"
  stay_on_while_plugged_in="$(adb_get_setting global stay_on_while_plugged_in)"
  screensaver_enabled="$(adb_get_setting secure screensaver_enabled)"
  accelerometer_rotation="$(adb_get_setting system accelerometer_rotation)"
  user_rotation="$(adb_get_setting system user_rotation)"
  lockscreen_disabled="$(adb_get_setting secure lockscreen.disabled)"
  low_power="$(adb_get_setting global low_power)"

  setting_needs_config "system.screen_off_timeout" "$screen_off_timeout" "$DESIRED_SCREEN_OFF_TIMEOUT"
  setting_needs_config "global.stay_on_while_plugged_in" "$stay_on_while_plugged_in" "$DESIRED_STAY_ON_WHILE_PLUGGED_IN"
  setting_needs_config "secure.screensaver_enabled" "$screensaver_enabled" "$DESIRED_SCREENSAVER_ENABLED"
  setting_needs_config "system.accelerometer_rotation" "$accelerometer_rotation" "$DESIRED_ACCELEROMETER_ROTATION"
  setting_needs_config "system.user_rotation" "$user_rotation" "$DESIRED_USER_ROTATION"
  setting_needs_config "secure.lockscreen.disabled" "$lockscreen_disabled" "$DESIRED_LOCKSCREEN_DISABLED"
  setting_needs_config "global.low_power" "$low_power" "$DESIRED_LOW_POWER"
}

print_config_recommendation() {
  print_header "Configuration recommendation"

  if [ "$CONFIG_NEEDED" -eq 0 ]; then
    cat <<EOF
It appears your phone does not need any extra configuration.

To perform the configuration anyway, use:
  ./$SCRIPT_NAME -d $DEVICE --config
EOF
  else
    cat <<EOF
It appears your phone needs some extra configuration:
EOF
    echo
    for reason in "${CONFIG_REASONS[@]}"; do
      echo "  - $reason"
    done
    echo
    cat <<EOF
Run:
  ./$SCRIPT_NAME -d $DEVICE --config
EOF
  fi
}

run_diagnostics() {
  print_header "Credimi runner device diagnostics"

  echo "Device serial: $DEVICE"
  echo "Date: $(date -Is)"
  echo

  echo "Device info:"
  echo "  manufacturer: $(adb -s "$DEVICE" shell getprop ro.product.manufacturer | tr -d '\r')"
  echo "  model:        $(adb -s "$DEVICE" shell getprop ro.product.model | tr -d '\r')"
  echo "  device:       $(adb -s "$DEVICE" shell getprop ro.product.device | tr -d '\r')"
  echo "  android:      $(adb -s "$DEVICE" shell getprop ro.build.version.release | tr -d '\r')"
  echo "  sdk:          $(adb -s "$DEVICE" shell getprop ro.build.version.sdk | tr -d '\r')"
  echo

  echo "Display geometry:"
  adb -s "$DEVICE" shell wm size | tr -d '\r' | sed 's/^/  /'
  adb -s "$DEVICE" shell wm density | tr -d '\r' | sed 's/^/  /'
  echo

  echo "Runner-relevant settings:"
  echo "  system.screen_off_timeout=$(adb_get_setting system screen_off_timeout)"
  echo "  global.stay_on_while_plugged_in=$(adb_get_setting global stay_on_while_plugged_in)"
  echo "  secure.screensaver_enabled=$(adb_get_setting secure screensaver_enabled)"
  echo "  secure.lockscreen.disabled=$(adb_get_setting secure lockscreen.disabled)"
  echo "  secure.lock_screen_locking_enabled=$(adb_get_setting secure lock_screen_locking_enabled)"
  echo "  system.accelerometer_rotation=$(adb_get_setting system accelerometer_rotation)"
  echo "  system.user_rotation=$(adb_get_setting system user_rotation)"
  echo "  global.low_power=$(adb_get_setting global low_power)"
  echo

  echo "Current window/keyguard state:"
  adb -s "$DEVICE" shell dumpsys window | tr -d '\r' | grep -Ei \
    'mCurrentFocus|mFocusedApp|mDreamingLockscreen|mShowingLockscreen|mKeyguard|isStatusBarKeyguard|mAwake|mScreenOn|NotificationShade' \
    | sed 's/^/  /' || true
  echo

  echo "Current power state:"
  adb -s "$DEVICE" shell dumpsys power | tr -d '\r' | grep -Ei \
    'mWakefulness|mIsPowered|mPlugType|mStayOn|mStayOnWhilePluggedInSetting|mWakeLockSummary|mUserActivityTimeoutOverrideFromWindowManager' \
    | sed 's/^/  /' || true

  print_header "Manual requirement"

  cat <<EOF
The phone must have no secure screen lock.

Required manual setup:
  Settings → Security & privacy → Device unlock → Screen lock → None

Fingerprint, PIN, password, and pattern unlock must be disabled for unattended Credimi runner automation.
ADB cannot reliably remove an existing secure lockscreen on non-rooted production Android devices.
EOF

  inspect_config_need
  print_config_recommendation
}

run_configuration() {
  print_header "Configuring Credimi runner device"

  echo "Device serial: $DEVICE"
  echo

  adb -s "$DEVICE" wait-for-device

  # Keep screen awake while charging.
  # 3 = AC + USB. This is the preferred runner baseline.
  adb_put_setting global stay_on_while_plugged_in "$DESIRED_STAY_ON_WHILE_PLUGGED_IN"

  # Long fallback timeout. Stay-awake should dominate while powered,
  # but this avoids aggressive sleep if USB power state flickers.
  adb_put_setting system screen_off_timeout "$DESIRED_SCREEN_OFF_TIMEOUT"

  # Disable screensaver / daydream.
  adb_put_setting secure screensaver_enabled "$DESIRED_SCREENSAVER_ENABLED"
  adb_put_setting secure screensaver_activate_on_dock 0 || true
  adb_put_setting secure screensaver_activate_on_sleep 0 || true

  # Reduce ambient/doze-style lockscreen behavior where these keys exist.
  adb_put_setting secure doze_enabled 0 || true
  adb_put_setting secure doze_always_on 0 || true
  adb_put_setting secure doze_pulse_on_pick_up 0 || true
  adb_put_setting secure doze_pulse_on_double_tap 0 || true
  adb_put_setting secure doze_pulse_on_tap 0 || true

  # Make Maestro coordinate-based flows deterministic.
  adb_put_setting system accelerometer_rotation "$DESIRED_ACCELEROMETER_ROTATION"
  adb_put_setting system user_rotation "$DESIRED_USER_ROTATION"

  # Try to make non-secure keyguard less sticky.
  # This only works as intended when the phone has no PIN/password/pattern/fingerprint lock.
  adb_put_setting secure lockscreen.disabled "$DESIRED_LOCKSCREEN_DISABLED" || true
  adb_put_setting secure lock_screen_locking_enabled 0 || true

  # Avoid battery saver interference.
  adb_put_setting global low_power "$DESIRED_LOW_POWER" || true

  echo "Configuration applied."

  run_diagnostics
}

select_device

if [ "$DO_CONFIG" -eq 1 ]; then
  run_configuration
else
  run_diagnostics
fi