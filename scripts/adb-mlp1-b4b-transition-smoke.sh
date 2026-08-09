#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKSPACE="$(cd "$ROOT_DIR/.." && pwd)"
JAWAKA_DIR="$WORKSPACE/Jawaka"
LEAF_DIR="$WORKSPACE/Leaf"
PRIMARY_MOUNT="${B4B_PRIMARY_MOUNT:-/mnt/sdcard}"
SECONDARY_MOUNT="${B4B_SECONDARY_MOUNT:-/media/sdcard1}"
PRIMARY_BIND="/tmp/leaf-syncthing-b4b-primary"
SECONDARY_BIND="/tmp/leaf-syncthing-b4b-secondary"
DEVICE_ROOT="/tmp/leaf-syncthing-b4b-transition"
DEVICE_EVIDENCE="/tmp/leaf-syncthing-b4b-evidence"
PRIMARY_UNDERLAY="$PRIMARY_MOUNT/.b4b-syncthing-fixture"
SECONDARY_UNDERLAY="$SECONDARY_MOUNT/.b4b-syncthing-fixture"
HOST_TMP="$(mktemp -d "${TMPDIR:-/tmp}/leaf-syncthing-b4b.XXXXXX")"
SERVER_PID=""
PORT=""

if [ "${CONFIRM_B4B_FAT_SMOKE:-0}" != 1 ]; then
    echo "Set CONFIRM_B4B_FAT_SMOKE=1 after confirming both selected cards are expendable." >&2
    exit 2
fi
case "$PRIMARY_MOUNT:$SECONDARY_MOUNT" in
    /mnt/sdcard:/media/sdcard1|/media/sdcard1:/mnt/sdcard) ;;
    *) echo "unsupported B4b card roots" >&2; exit 2 ;;
esac

if [ -n "${ADB_SERIAL:-}" ]; then
    ADB=(adb -s "$ADB_SERIAL")
else
    serial="$(adb devices | awk 'NR > 1 && $2 == "device" {print $1; exit}')"
    [ -n "$serial" ] || { echo "No online ADB device found" >&2; exit 1; }
    ADB=(adb -s "$serial")
fi

kill_fixture_processes() {
    # Expanded by the device shell, not this host shell.
    # shellcheck disable=SC2016
    "${ADB[@]}" shell '
        fixture_pids=""
        for proc in /proc/[0-9]*; do
            pid="${proc##*/}"
            [ "$pid" = "$$" ] && continue
            cmdline="$(tr "\000" " " <"$proc/cmdline" 2>/dev/null)"
            case "$cmdline" in
                *leaf-syncthing-b4b-transition*|*leaf-syncthing-b4b-device*|*leaf-syncthing-b4b-runtime*)
                    fixture_pids="$fixture_pids $pid"
                    ;;
            esac
        done
        for pid in $fixture_pids; do
            kill -TERM "$pid" >/dev/null 2>&1 || true
        done
        sleep 0.1
        for pid in $fixture_pids; do
            kill -KILL "$pid" >/dev/null 2>&1 || true
        done
    ' >/dev/null 2>&1 || true
}

cleanup() {
    local result=$?
    set +e
    if [ -n "$PORT" ]; then
        "${ADB[@]}" reverse --remove "tcp:$PORT" >/dev/null 2>&1 || true
    fi
    if [ -n "$SERVER_PID" ]; then
        kill "$SERVER_PID" >/dev/null 2>&1 || true
        wait "$SERVER_PID" >/dev/null 2>&1 || true
    fi
    kill_fixture_processes
    "${ADB[@]}" shell \
        "umount -l '$PRIMARY_BIND' >/dev/null 2>&1 || true; umount -l '$SECONDARY_BIND' >/dev/null 2>&1 || true; rm -rf '$DEVICE_ROOT' '$DEVICE_EVIDENCE' '$PRIMARY_BIND' '$SECONDARY_BIND' '$PRIMARY_UNDERLAY' '$SECONDARY_UNDERLAY' /tmp/leaf-syncthing-b4b-runtime /tmp/leaf-syncthing-b4b-device" \
        >/dev/null 2>&1 || true
    rm -rf "$HOST_TMP"
    exit "$result"
}
trap cleanup EXIT HUP INT TERM

echo "Using adb device: $("${ADB[@]}" get-serialno)"
for mount in "$PRIMARY_MOUNT" "$SECONDARY_MOUNT"; do
    line="$("${ADB[@]}" shell "awk -v target='$mount' '\$5 == target {print; exit}' /proc/self/mountinfo" | tr -d '\r')"
    case "$line" in
        *" - vfat "*|*" - msdos "*|*" - fat "*) ;;
        *) echo "selected B4b root is not an exact FAT mount: $line" >&2; exit 1 ;;
    esac
done

PORT="$(python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)"
BASE_URL="http://127.0.0.1:$PORT/pakrat/v1/"
fixture_args=(--base-url "$BASE_URL")
if [ "${B4B_SKIP_BUILD:-0}" = 1 ]; then
    fixture_args+=(--skip-build)
else
    make -C "$JAWAKA_DIR" mlp1-pakrat-smoke
    make -C "$JAWAKA_DIR" mlp1
fi
python3 "$ROOT_DIR/scripts/build_b4b_fixture.py" "${fixture_args[@]}"

SMOKE_BIN="$JAWAKA_DIR/build/mlp1/bin/jawaka-pakrat-smoke"
DAEMON_BIN="$JAWAKA_DIR/build/mlp1/bin/jawakad"
CTL_BIN="$JAWAKA_DIR/build/mlp1/bin/jawaka-platformctl"
for path in "$SMOKE_BIN" "$DAEMON_BIN" "$CTL_BIN"; do
    [ -x "$path" ] || { echo "missing current Jawaka binary: $path" >&2; exit 1; }
done

read -r FLOOR_URL FLOOR_SHA < <(python3 - "$LEAF_DIR/build/pakrat-local/syncthing-b4b/pakrat/v1/storefront.json" <<'PY'
import json
import sys
catalog = json.load(open(sys.argv[1], encoding="utf-8"))
app = next(app for app in catalog["apps"] if app["id"] == "org.umrk.syncthing")
package = next(package for package in app["packages"] if package["platform"] == "mlp1")
floor = next(version for version in package["versions"] if version["version"] == "0.0.1")
print(floor["artifact"]["url"], floor["artifact"]["sha256"])
PY
)

FEED_ROOT="$LEAF_DIR/build/pakrat-local/syncthing-b4b"
python3 -m http.server "$PORT" --bind 127.0.0.1 --directory "$FEED_ROOT" \
    >"$HOST_TMP/http.log" 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 50); do
    curl -fsS "$BASE_URL/storefront.json" >/dev/null 2>&1 && break
    sleep 0.1
done
curl -fsS "$BASE_URL/storefront.json" >/dev/null
"${ADB[@]}" reverse "tcp:$PORT" "tcp:$PORT" >/dev/null

runtime_lib_dir=""
for candidate in \
    "$PRIMARY_MOUNT/.system/leaf/platforms/mlp1/launcher/lib" \
    "$SECONDARY_MOUNT/.system/leaf/platforms/mlp1/launcher/lib"; do
    if "${ADB[@]}" shell "test -d '$candidate'"; then
        runtime_lib_dir="$candidate"
        break
    fi
done
[ -n "$runtime_lib_dir" ] || { echo "MLP1 launcher runtime libraries not found" >&2; exit 1; }

kill_fixture_processes
"${ADB[@]}" shell \
    "umount -l '$PRIMARY_BIND' >/dev/null 2>&1 || true; umount -l '$SECONDARY_BIND' >/dev/null 2>&1 || true; rm -rf '$DEVICE_ROOT' '$DEVICE_EVIDENCE' '$PRIMARY_BIND' '$SECONDARY_BIND' '$PRIMARY_UNDERLAY' '$SECONDARY_UNDERLAY'; mkdir -p '$DEVICE_ROOT/bin' '$DEVICE_EVIDENCE' '$PRIMARY_BIND' '$SECONDARY_BIND' '$PRIMARY_UNDERLAY' '$SECONDARY_UNDERLAY'; mount --bind '$PRIMARY_UNDERLAY' '$PRIMARY_BIND'; mount --bind '$SECONDARY_UNDERLAY' '$SECONDARY_BIND'"
"${ADB[@]}" push "$SMOKE_BIN" "$DEVICE_ROOT/bin/jawaka-pakrat-smoke" >/dev/null
"${ADB[@]}" push "$DAEMON_BIN" "$DEVICE_ROOT/bin/jawakad" >/dev/null
"${ADB[@]}" push "$CTL_BIN" "$DEVICE_ROOT/bin/jawaka-platformctl" >/dev/null
"${ADB[@]}" push "$ROOT_DIR/scripts/mlp1-b4b-transition-device.sh" "$DEVICE_ROOT/run.sh" >/dev/null
"${ADB[@]}" shell "chmod 755 '$DEVICE_ROOT/bin/'* '$DEVICE_ROOT/run.sh'"

set +e
"${ADB[@]}" shell \
    "B4B_PRIMARY='$PRIMARY_BIND' B4B_SECONDARY='$SECONDARY_BIND' B4B_BASE_URL='$BASE_URL' B4B_FLOOR_URL='$FLOOR_URL' B4B_FLOOR_SHA='$FLOOR_SHA' B4B_BIN_ROOT='$DEVICE_ROOT/bin' B4B_RUNTIME_LIB_DIR='$runtime_lib_dir' B4B_EVIDENCE='$DEVICE_EVIDENCE' bash '$DEVICE_ROOT/run.sh'"
device_status=$?
set -e

evidence_dir="$ROOT_DIR/build/b4b-transition-evidence"
rm -rf "$evidence_dir"
mkdir -p "$evidence_dir"
"${ADB[@]}" pull "$DEVICE_EVIDENCE/." "$evidence_dir/" >/dev/null 2>&1 || true
printf '%s\n' "$("${ADB[@]}" get-serialno)" >"$evidence_dir/adb-serial.txt"
cp "$HOST_TMP/http.log" "$evidence_dir/http.log"
[ "$device_status" -eq 0 ] || exit "$device_status"

echo "PASS B4b actual floor-to-real two-card transition"
echo "B4b transition evidence: $evidence_dir"
