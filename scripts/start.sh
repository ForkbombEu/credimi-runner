#!/bin/bash
set -euxo pipefail

# Check if /dev/kvm is accessible
need_kvm() {
  if [ ! -e /dev/kvm ]; then
    echo "ERROR: /dev/kvm not found. Run container with: --device /dev/kvm" >&2
    exit 1
  fi
  if [ ! -r /dev/kvm ] || [ ! -w /dev/kvm ]; then
    echo "ERROR: /dev/kvm is not readable/writable." >&2
    echo "Fix on host: sudo chmod 666 /dev/kvm  (or add user to kvm group)" >&2
    exit 1
  fi
}

echo "Cleaning up existing emulator processes..."
killall -9 qemu-system-x86_64 || true
killall -9 emulator || true
adb kill-server || true
sleep 2


need_kvm

echo "Starting ADB server..."
adb start-server

echo "✅ All images downloaded."
echo "Starting credimi-runner..."
exec credimi-runner serve

