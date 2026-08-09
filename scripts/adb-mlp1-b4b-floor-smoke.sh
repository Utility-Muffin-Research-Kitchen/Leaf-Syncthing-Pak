#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PACKAGE_DIR="$ROOT_DIR/build/mlp1/floor/package/Syncthing.pak"
REMOTE_ROOT="${B4B_FLOOR_REMOTE_ROOT:-/tmp/leaf-syncthing-b4b-floor}"
EVIDENCE_DIR="$ROOT_DIR/build/b4b-device-evidence"
LIVE_PID=
FLOOR_PID=

case "$REMOTE_ROOT" in
    /tmp/leaf-syncthing-b4b-*) ;;
    *) echo "unsafe B4b remote root: $REMOTE_ROOT" >&2; exit 2 ;;
esac

if [ -n "${ADB_SERIAL:-}" ]; then
    ADB=(adb -s "$ADB_SERIAL")
else
    serial="$(adb devices | awk 'NR > 1 && $2 == "device" { print $1; exit }')"
    [ -n "$serial" ] || { echo "No online ADB device found" >&2; exit 1; }
    ADB=(adb -s "$serial")
fi

cleanup() {
    local status=$?
    set +e
    if [ -n "$FLOOR_PID" ]; then
        "${ADB[@]}" shell "kill -TERM '$FLOOR_PID' 2>/dev/null || true" >/dev/null
        sleep 0.2
        "${ADB[@]}" shell "kill -KILL '$FLOOR_PID' 2>/dev/null || true" >/dev/null
    fi
    "${ADB[@]}" shell "for pid in \$(pidof leaf-syncthing-floor 2>/dev/null); do kill -KILL \"\$pid\" 2>/dev/null || true; done" >/dev/null
    if [ -n "$LIVE_PID" ]; then
        "${ADB[@]}" shell "kill -CONT '$LIVE_PID' 2>/dev/null || true" >/dev/null
    fi
    "${ADB[@]}" shell "rm -rf '$REMOTE_ROOT'" >/dev/null 2>&1 || true
    exit "$status"
}
trap cleanup EXIT HUP INT TERM

[ -d "$PACKAGE_DIR" ] || {
    echo "missing floor package; run make package-floor-mlp1 first" >&2
    exit 1
}
mkdir -p "$EVIDENCE_DIR"
"${ADB[@]}" shell "rm -rf '$REMOTE_ROOT'; mkdir -p '$REMOTE_ROOT'"
"${ADB[@]}" push "$PACKAGE_DIR" "$REMOTE_ROOT/" >/dev/null

before_count="$("${ADB[@]}" shell \
    "find /mnt/sdcard /media/sdcard1 -iname '*syncthing*' 2>/dev/null | wc -l" | tr -d '\r')"
[ "$before_count" -eq 0 ] || {
    echo "device already has Syncthing paths; refusing ambiguous floor audit" >&2
    exit 1
}

LIVE_PID="$("${ADB[@]}" shell "pidof loong_pangu" | tr -d '\r' | awk '{print $1}')"
[ -n "$LIVE_PID" ] || { echo "live loong_pangu not found" >&2; exit 1; }
"${ADB[@]}" shell "kill -STOP '$LIVE_PID'"

"${ADB[@]}" shell \
    "cd '$REMOTE_ROOT/Syncthing.pak'; setsid env UMRK_ENV_FILE=/mnt/sdcard/.system/leaf/platforms/mlp1/launcher/env.sh ./launch.sh </dev/null >'$REMOTE_ROOT/floor.log' 2>&1 & echo \$! >'$REMOTE_ROOT/floor.pid'"
FLOOR_PID="$("${ADB[@]}" shell "cat '$REMOTE_ROOT/floor.pid'" | tr -d '\r')"
[ -n "$FLOOR_PID" ] || { echo "floor UI did not return a pid" >&2; exit 1; }
sleep 2
"${ADB[@]}" shell "kill -0 '$FLOOR_PID'" || {
    "${ADB[@]}" shell "cat '$REMOTE_ROOT/floor.log'" >&2
    echo "floor UI exited before inspection" >&2
    exit 1
}

# The single-quoted body is intentionally expanded only by the device shell;
# FLOOR_PID is spliced in by the host at the two /proc paths.
# shellcheck disable=SC2016
internet_socket_count="$("${ADB[@]}" shell '
    count=0
    for fd in /proc/'"$FLOOR_PID"'/fd/*; do
        link=$(readlink "$fd" 2>/dev/null || true)
        case "$link" in
            socket:\[*\]) inode=${link#socket:\[}; inode=${inode%\]} ;;
            *) continue ;;
        esac
        for table in /proc/'"$FLOOR_PID"'/net/tcp /proc/'"$FLOOR_PID"'/net/tcp6 /proc/'"$FLOOR_PID"'/net/udp /proc/'"$FLOOR_PID"'/net/udp6; do
            [ -f "$table" ] || continue
            awk -v inode="$inode" '\''NR > 1 && $10 == inode { found=1 } END { exit found ? 0 : 1 }'\'' "$table" && {
                count=$((count + 1))
                break
            }
        done
    done
    echo "$count"
' | tr -d '\r')"
[ "$internet_socket_count" -eq 0 ] || {
    echo "floor UI opened an IPv4/IPv6 socket" >&2
    exit 1
}

after_count="$("${ADB[@]}" shell \
    "find /mnt/sdcard /media/sdcard1 -iname '*syncthing*' 2>/dev/null | wc -l" | tr -d '\r')"
[ "$after_count" -eq 0 ] || {
    echo "floor UI wrote a Syncthing path to an SD card" >&2
    exit 1
}

if "${ADB[@]}" shell "command -v screencap >/dev/null 2>&1"; then
    "${ADB[@]}" shell "screencap -p '$REMOTE_ROOT/floor.png'"
    "${ADB[@]}" pull "$REMOTE_ROOT/floor.png" "$EVIDENCE_DIR/floor.png" >/dev/null
fi
"${ADB[@]}" shell "cat '$REMOTE_ROOT/floor.log'" >"$EVIDENCE_DIR/floor.log"

echo "PASS B4b floor UI: alive, zero internet sockets, zero SD Syncthing writes"
echo "B4b floor evidence: $EVIDENCE_DIR"
