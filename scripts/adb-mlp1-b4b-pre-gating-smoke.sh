#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKSPACE="$(cd "$ROOT_DIR/.." && pwd)"
JAWAKA_DIR="$WORKSPACE/Jawaka"
LEAF_DIR="$WORKSPACE/Leaf"
PRE_GATING_COMMIT="${B4B_PRE_GATING_COMMIT:-95de4829b1d0e494aadb4e5b5367d3d8f6a3a00c}"
TOOLCHAIN_IMAGE="${MLP1_TOOLCHAIN_IMAGE:-ghcr.io/utility-muffin-research-kitchen/mlp1-toolchain:local}"
REMOTE_ROOT="${B4B_PRE_GATING_REMOTE_ROOT:-/tmp/leaf-syncthing-b4b-pre-gating}"
CARD_ROOT="${B4B_PRE_GATING_CARD_ROOT:-/mnt/sdcard}"
FIXTURE_UNDERLAY="$CARD_ROOT/.b4b-pre-gating-fixture"
FIXTURE_ROOT="$REMOTE_ROOT/card"
EVIDENCE_DIR="$ROOT_DIR/build/b4b-device-evidence"
OLD_PARENT="$(mktemp -d "$WORKSPACE/.b4b-pre-gating.XXXXXX")"
OLD_DIR="$OLD_PARENT/Jawaka"
SERVER_PID=
PORT=
WORKTREE_ADDED=0

case "$REMOTE_ROOT" in
    /tmp/leaf-syncthing-b4b-*) ;;
    *) echo "unsafe B4b remote root: $REMOTE_ROOT" >&2; exit 2 ;;
esac
case "$CARD_ROOT" in
    /mnt/sdcard|/media/sdcard1) ;;
    *) echo "unsupported B4b card root: $CARD_ROOT" >&2; exit 2 ;;
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
    if [ -n "$PORT" ]; then
        "${ADB[@]}" reverse --remove "tcp:$PORT" >/dev/null 2>&1 || true
    fi
    if [ -n "$SERVER_PID" ]; then
        kill "$SERVER_PID" >/dev/null 2>&1 || true
        wait "$SERVER_PID" >/dev/null 2>&1 || true
    fi
    "${ADB[@]}" shell "umount -l '$FIXTURE_ROOT' >/dev/null 2>&1 || true; rm -rf '$REMOTE_ROOT' '$FIXTURE_UNDERLAY'" >/dev/null 2>&1 || true
    if [ "$WORKTREE_ADDED" -eq 1 ]; then
        git -C "$JAWAKA_DIR" worktree remove --force "$OLD_DIR" >/dev/null 2>&1 || true
    fi
    unlink "$OLD_PARENT/http.log" >/dev/null 2>&1 || true
    rmdir "$OLD_PARENT" >/dev/null 2>&1 || true
    exit "$status"
}
trap cleanup EXIT HUP INT TERM

git -C "$JAWAKA_DIR" worktree add --detach "$OLD_DIR" "$PRE_GATING_COMMIT" >/dev/null
WORKTREE_ADDED=1
old_relative="${OLD_DIR#"$WORKSPACE/"}"
docker run --rm \
    -v "$WORKSPACE:/workspace" \
    -w "/workspace/$old_relative" \
    "$TOOLCHAIN_IMAGE" \
    make -f ports/mlp1/Makefile \
        JAWAKA_DIR="/workspace/$old_relative" \
        CATASTROPHE_DIR=/workspace/Catastrophe \
        BUILD=build/mlp1 pakrat-smoke
OLD_BINARY="$OLD_DIR/build/mlp1/bin/jawaka-pakrat-smoke"
[ -x "$OLD_BINARY" ] || { echo "historical client build is missing" >&2; exit 1; }

PORT="$(python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)"
BASE_URL="http://127.0.0.1:$PORT/pakrat/v1/"
FEED_ROOT="$LEAF_DIR/build/pakrat-local/syncthing-b4b"
python3 "$ROOT_DIR/scripts/build_b4b_fixture.py" \
    --base-url "$BASE_URL" --skip-build
python3 -m http.server "$PORT" --bind 127.0.0.1 \
    --directory "$FEED_ROOT" >"$OLD_PARENT/http.log" 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 50); do
    curl -fsS "$BASE_URL/storefront.json" >/dev/null 2>&1 && break
    sleep 0.1
done
curl -fsS "$BASE_URL/storefront.json" >/dev/null
"${ADB[@]}" reverse "tcp:$PORT" "tcp:$PORT" >/dev/null

mkdir -p "$EVIDENCE_DIR"
mount_line="$("${ADB[@]}" shell "awk -v target='$CARD_ROOT' '\$5 == target { print; exit }' /proc/self/mountinfo" | tr -d '\r')"
case "$mount_line" in
    *" - vfat "*|*" - msdos "*|*" - fat "*) ;;
    *) echo "B4b fixture root is not on an exact FAT mount: $mount_line" >&2; exit 1 ;;
esac
"${ADB[@]}" shell "umount -l '$FIXTURE_ROOT' >/dev/null 2>&1 || true; rm -rf '$REMOTE_ROOT' '$FIXTURE_UNDERLAY'; mkdir -p '$REMOTE_ROOT/bin' '$FIXTURE_ROOT' '$FIXTURE_UNDERLAY/Apps/mlp1' '$FIXTURE_UNDERLAY/Roms' '$FIXTURE_UNDERLAY/Images' '$FIXTURE_UNDERLAY/BIOS' '$FIXTURE_UNDERLAY/.umrk/mlp1' '$FIXTURE_UNDERLAY/.system/leaf/platforms/mlp1'; mount --bind '$FIXTURE_UNDERLAY' '$FIXTURE_ROOT'"
"${ADB[@]}" push "$OLD_BINARY" "$REMOTE_ROOT/bin/" >/dev/null
printf '%s\n' \
    '{"schema":1,"product":"leaf","platform":"mlp1","version":"v0.9.0","release_id":"v0.9.0"}' \
    | "${ADB[@]}" shell "cat >'$FIXTURE_ROOT/.umrk/mlp1/release.json'"
printf '%s\n' '{"managed_apps":[]}' \
    | "${ADB[@]}" shell "cat >'$FIXTURE_ROOT/.system/leaf/platforms/mlp1/manifest.json'"
"${ADB[@]}" shell sync

RUNTIME_LIB=/mnt/sdcard/.system/leaf/platforms/mlp1/launcher/lib
run_old() {
    "${ADB[@]}" shell \
        "env -i PATH='/usr/bin:/bin:/usr/sbin:/sbin' LD_LIBRARY_PATH='$RUNTIME_LIB' PLATFORM=mlp1 SDCARD_PATH='$FIXTURE_ROOT' JAWAKA_SDCARD_ROOT='$FIXTURE_ROOT' UMRK_INTERNAL_DATA_PATH='$FIXTURE_ROOT/.umrk/mlp1' UMRK_PLATFORM_PATH='$FIXTURE_ROOT/.system/leaf/platforms/mlp1' PAKRAT_CATALOG_BASE_URL='$BASE_URL' '$REMOTE_ROOT/bin/jawaka-pakrat-smoke' --platform mlp1 --sdcard-root '$FIXTURE_ROOT' --state-dir '$FIXTURE_ROOT/.umrk/mlp1' --db '$FIXTURE_ROOT/.umrk/mlp1/library.db' --platform-root '$FIXTURE_ROOT/.system/leaf/platforms/mlp1' --socket '$REMOTE_ROOT/jawakad.sock' $*"
}

# A real pre-gating device already has a library DB. Seed its exact historical
# schema against the bind-mounted card fixture, close it, then copy the DB onto
# FAT before testing Pak Rat's FAT-local staging and install path.
"${ADB[@]}" shell "mkdir -p '$REMOTE_ROOT/seed-state' && env -i PATH='/usr/bin:/bin:/usr/sbin:/sbin' LD_LIBRARY_PATH='$RUNTIME_LIB' PLATFORM=mlp1 SDCARD_PATH='$FIXTURE_ROOT' JAWAKA_SDCARD_ROOT='$FIXTURE_ROOT' UMRK_INTERNAL_DATA_PATH='$REMOTE_ROOT/seed-state' UMRK_PLATFORM_PATH='$FIXTURE_ROOT/.system/leaf/platforms/mlp1' '$REMOTE_ROOT/bin/jawaka-pakrat-smoke' --platform mlp1 --sdcard-root '$FIXTURE_ROOT' --state-dir '$REMOTE_ROOT/seed-state' --db '$REMOTE_ROOT/seed-state/library.db' --platform-root '$FIXTURE_ROOT/.system/leaf/platforms/mlp1' --socket '$REMOTE_ROOT/jawakad.sock' rescan && cp '$REMOTE_ROOT/seed-state/library.db' '$FIXTURE_ROOT/.umrk/mlp1/library.db' && sync"

list_output="$EVIDENCE_DIR/pre-gating-list.tsv"
list_error="$EVIDENCE_DIR/pre-gating-list.err"
list_ok=0
for _ in 1 2 3; do
    if run_old list >"$list_output" 2>"$list_error"; then
        list_ok=1
        break
    fi
    sleep 0.2
done
[ "$list_ok" -eq 1 ] || {
    cat "$list_error" >&2
    exit 1
}
for app_id in org.umrk.itchio org.umrk.discoboy org.umrk.nimbus \
    org.umrk.portmaster org.helaas.sdlreader org.umrk.syncthing; do
    grep -F "$app_id" "$list_output" >/dev/null || {
        echo "historical client omitted $app_id" >&2
        cat "$list_output" >&2
        exit 1
    }
done
grep -F $'available\torg.umrk.syncthing\t0.0.1\t' "$list_output" >/dev/null
if grep -F $'org.umrk.syncthing\t0.0.2\t' "$list_output" >/dev/null; then
    echo "historical client selected the gated real pak" >&2
    exit 1
fi

if ! run_old install org.umrk.syncthing \
    >"$EVIDENCE_DIR/pre-gating-install.log" 2>&1; then
    cat "$EVIDENCE_DIR/pre-gating-install.log" >&2
    "${ADB[@]}" shell "find '$FIXTURE_ROOT/Apps/mlp1' -maxdepth 3 -type f -print 2>/dev/null; sqlite3 '$FIXTURE_ROOT/.umrk/mlp1/library.db' \"select store_id,version,install_path from pakrat_installs where store_id='org.umrk.syncthing';\" 2>&1" >&2 || true
    exit 1
fi
"${ADB[@]}" shell \
    "test -x '$FIXTURE_ROOT/Apps/mlp1/Syncthing.pak/launch.sh'; test ! -e '$FIXTURE_ROOT/Apps/mlp1/Syncthing.pak/bin/syncthing'; grep -F '\"pak_version\": \"0.0.1\"' '$FIXTURE_ROOT/Apps/mlp1/Syncthing.pak/pak.json'; ! grep -F 'min_leaf_version' '$FIXTURE_ROOT/Apps/mlp1/Syncthing.pak/pak.json'; ! grep -F '\"service\"' '$FIXTURE_ROOT/Apps/mlp1/Syncthing.pak/pak.json'" \
    >/dev/null

printf '%s\n' "$PRE_GATING_COMMIT" >"$EVIDENCE_DIR/pre-gating-commit.txt"
echo "PASS B4b real pre-gating MLP1 client: all apps parsed, floor selected and installed"
echo "B4b pre-gating evidence: $EVIDENCE_DIR"
