#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REMOTE_BINARY="/tmp/leaf-syncthing-b1-cards.test"
MOUNT_A="${LEAF_SYNCTHING_CARD_MOUNT_A:-/mnt/sdcard}"
MOUNT_B="${LEAF_SYNCTHING_CARD_MOUNT_B:-/media/sdcard1}"
USERDATA_A="$MOUNT_A/.userdata/mlp1-b1-card-smoke"
USERDATA_B="$MOUNT_B/.userdata/mlp1-b1-card-smoke"

if [[ ! "$MOUNT_A" =~ ^/(mnt|media)/[A-Za-z0-9._/-]+$ ]] ||
   [[ ! "$MOUNT_B" =~ ^/(mnt|media)/[A-Za-z0-9._/-]+$ ]] ||
   [[ "$MOUNT_A" == *".."* ]] || [[ "$MOUNT_B" == *".."* ]] ||
   [[ "$MOUNT_A" == "$MOUNT_B" ]]; then
    echo "unsafe or duplicate card mounts: $MOUNT_A $MOUNT_B" >&2
    exit 1
fi

if [ -n "${ADB_SERIAL:-}" ]; then
    ADB=(adb -s "$ADB_SERIAL")
else
    serial="$(adb devices | awk 'NR>1 && $2=="device" {print $1; exit}')"
    if [ -z "${serial:-}" ]; then
        echo "No online adb device found." >&2
        exit 1
    fi
    ADB=(adb -s "$serial")
fi

cleanup() {
    "${ADB[@]}" shell "rm -f '$REMOTE_BINARY'" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "Using adb device: $("${ADB[@]}" get-serialno)"
"${ADB[@]}" shell "mountpoint -q '$MOUNT_A' && mountpoint -q '$MOUNT_B' && test ! -e '$USERDATA_A' && test ! -e '$USERDATA_B'"
mkdir -p "$ROOT_DIR/build/mlp1/tests"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c \
    -o "$ROOT_DIR/build/mlp1/tests/cards.test" "$ROOT_DIR/internal/cards"
"${ADB[@]}" push "$ROOT_DIR/build/mlp1/tests/cards.test" "$REMOTE_BINARY" >/dev/null
"${ADB[@]}" shell "chmod 755 '$REMOTE_BINARY' && env \
    LEAF_SYNCTHING_DEVICE_CARD_TEST=1 \
    LEAF_SYNCTHING_CARD_MOUNT_A='$MOUNT_A' \
    LEAF_SYNCTHING_CARD_MOUNT_B='$MOUNT_B' \
    LEAF_SYNCTHING_CARD_USERDATA_A='$USERDATA_A' \
    LEAF_SYNCTHING_CARD_USERDATA_B='$USERDATA_B' \
    '$REMOTE_BINARY' -test.run '^TestMLP1TwoCardSafety$' -test.v"
"${ADB[@]}" shell "test ! -e '$USERDATA_A' && test ! -e '$USERDATA_B'"
echo "PASS MLP1 B1 two-card safety (enroll, slot reversal, replacement, cloned-id refusal)"
