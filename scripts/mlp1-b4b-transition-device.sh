#!/usr/bin/env bash
# Runs the B4b floor-on-Secondary transition against two bind-mounted MLP1 FAT cards.
set -euo pipefail

PRIMARY="${B4B_PRIMARY:?B4B_PRIMARY is required}"
SECONDARY="${B4B_SECONDARY:?B4B_SECONDARY is required}"
BASE_URL="${B4B_BASE_URL:?B4B_BASE_URL is required}"
FLOOR_URL="${B4B_FLOOR_URL:?B4B_FLOOR_URL is required}"
FLOOR_SHA="${B4B_FLOOR_SHA:?B4B_FLOOR_SHA is required}"
FLOOR_VERSION="${B4B_FLOOR_VERSION:-0.0.1}"
REAL_VERSION="${B4B_REAL_VERSION:-0.0.2}"
MIN_LEAF_VERSION="${B4B_MIN_LEAF_VERSION:-99.99.99}"
BIN_ROOT="${B4B_BIN_ROOT:?B4B_BIN_ROOT is required}"
RUNTIME_LIB_DIR="${B4B_RUNTIME_LIB_DIR:-}"
EVIDENCE="${B4B_EVIDENCE:-/tmp/leaf-syncthing-b4b-evidence}"

PAKRAT="$BIN_ROOT/jawaka-pakrat-smoke"
DAEMON="$BIN_ROOT/jawakad"
CTL="$BIN_ROOT/jawaka-platformctl"
PRIMARY_APPS="$PRIMARY/Apps"
SECONDARY_APPS="$SECONDARY/Apps"
PRIMARY_USERDATA="$PRIMARY/.userdata/mlp1"
SECONDARY_USERDATA="$SECONDARY/.userdata/mlp1"
STATE="$PRIMARY/.umrk/mlp1"
PLATFORM_ROOT="$PRIMARY/.system/leaf/platforms/mlp1"
RUNTIME="/tmp/leaf-syncthing-b4b-runtime"
SOCKET="$RUNTIME/jawakad.sock"
TMP_ROOT="/tmp/leaf-syncthing-b4b-device"
STORE_ID="org.umrk.syncthing"
INSTALL_REL="mlp1/Syncthing.pak"
PRIMARY_TARGET="$PRIMARY_APPS/$INSTALL_REL"
SECONDARY_TARGET="$SECONDARY_APPS/$INSTALL_REL"
DAEMON_PID=""

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

cleanup() {
    if [ -n "$DAEMON_PID" ]; then
        kill "$DAEMON_PID" >/dev/null 2>&1 || true
        wait "$DAEMON_PID" >/dev/null 2>&1 || true
    fi
}
trap cleanup EXIT HUP INT TERM

case "$PRIMARY:$SECONDARY" in
    /tmp/leaf-syncthing-b4b-primary:/tmp/leaf-syncthing-b4b-secondary) ;;
    *) fail "unsafe B4b fixture roots" ;;
esac
for path in "$PAKRAT" "$DAEMON" "$CTL"; do
    [ -x "$path" ] || fail "missing binary $path"
done
for root in "$PRIMARY" "$SECONDARY"; do
    line="$(awk -v target="$root" '$5 == target {print; exit}' /proc/self/mountinfo)"
    case "$line" in
        *" - vfat "*|*" - msdos "*|*" - fat "*) ;;
        *) fail "$root is not an exact FAT bind mount: $line" ;;
    esac
done

rm -rf "$TMP_ROOT" "$RUNTIME" "$EVIDENCE"
mkdir -p "$TMP_ROOT" "$RUNTIME" "$EVIDENCE"

find "$PRIMARY" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} \;
find "$SECONDARY" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} \;
for root in "$PRIMARY" "$SECONDARY"; do
    mkdir -p "$root/Apps/mlp1" "$root/Apps/shared" "$root/Roms" \
        "$root/Images" "$root/Music" "$root/BIOS" "$root/Saves" \
        "$root/States" "$root/Cheats" "$root/.userdata/mlp1"
done
mkdir -p "$STATE/store" "$PLATFORM_ROOT" "$PRIMARY_USERDATA/logs"
printf '%s\n' '{"managed_apps":[]}' >"$PLATFORM_ROOT/manifest.json"
printf '%s\n' "$BASE_URL" >"$STATE/store/dev-catalog-url"

write_release() {
    printf '%s\n' \
        "{\"schema\":1,\"product\":\"leaf\",\"platform\":\"mlp1\",\"version\":\"$1\",\"release_id\":\"$1\"}" \
        >"$STATE/release.json"
}

clean_env() {
    env -i \
        PATH=/sbin:/usr/sbin:/bin:/usr/bin \
        HOME=/tmp \
        LD_LIBRARY_PATH="$RUNTIME_LIB_DIR" \
        PLATFORM=mlp1 \
        SDCARD_PATH="$PRIMARY" \
        SDCARD_PATHS="$PRIMARY:$SECONDARY" \
        JAWAKA_SDCARD_ROOT="$PRIMARY" \
        ROMS_PATHS="$PRIMARY/Roms:$SECONDARY/Roms" \
        IMAGES_PATHS="$PRIMARY/Images:$SECONDARY/Images" \
        MUSIC_PATHS="$PRIMARY/Music:$SECONDARY/Music" \
        APPS_PATH="$PRIMARY_APPS" \
        APPS_PATHS="$PRIMARY_APPS:$SECONDARY_APPS" \
        BIOS_PATHS="$PRIMARY/BIOS:$SECONDARY/BIOS" \
        SAVES_PATHS="$PRIMARY/Saves:$SECONDARY/Saves" \
        STATES_PATHS="$PRIMARY/States:$SECONDARY/States" \
        CHEATS_PATHS="$PRIMARY/Cheats:$SECONDARY/Cheats" \
        USERDATA_PATH="$PRIMARY_USERDATA" \
        USERDATA_PATHS="$PRIMARY_USERDATA:$SECONDARY_USERDATA" \
        LOGS_PATH="$PRIMARY_USERDATA/logs" \
        UMRK_RUNTIME_PATH="$RUNTIME" \
        JAWAKA_RUNTIME_DIR="$RUNTIME" \
        UMRK_DAEMON_SOCKET="$SOCKET" \
        JAWAKA_SOCKET_PATH="$SOCKET" \
        UMRK_INTERNAL_DATA_PATH="$STATE" \
        UMRK_PLATFORM_PATH="$PLATFORM_ROOT" \
        JAWAKA_OSD=0 \
        "$@"
}

run_pakrat() {
    clean_env "$PAKRAT" --platform mlp1 --sdcard-root "$PRIMARY" \
        --state-dir "$STATE" --db "$STATE/library.db" \
        --platform-root "$PLATFORM_ROOT" --runtime-dir "$RUNTIME" \
        --socket "$SOCKET" "$@"
}

request() {
    clean_env "$CTL" --socket "$SOCKET" request "$1"
}

status_state() {
    request "{\"v\":1,\"op\":\"status\",\"id\":\"b4b-status\",\"service_id\":\"$STORE_ID\"}" \
        2>/dev/null | sed -n 's/.*"effective_state":"\([^"]*\)".*/\1/p'
}

start_daemon() {
    rm -f "$SOCKET"
    clean_env "$DAEMON" --daemon-only >"$TMP_ROOT/jawakad.log" 2>&1 &
    DAEMON_PID=$!
    for _ in $(seq 1 500); do
        [ -S "$SOCKET" ] && return 0
        kill -0 "$DAEMON_PID" >/dev/null 2>&1 || {
            cat "$TMP_ROOT/jawakad.log" >&2
            fail "daemon exited before opening its socket"
        }
        sleep 0.02
    done
    fail "daemon socket missing"
}

db_value() {
    sqlite3 -cmd '.timeout 5000' "$STATE/library.db" "$1" 2>/dev/null
}

package_count() {
    count=0
    [ ! -d "$PRIMARY_TARGET" ] || count=$((count + 1))
    [ ! -d "$SECONDARY_TARGET" ] || count=$((count + 1))
    printf '%s\n' "$count"
}

write_release 0.9.0
run_pakrat rescan >"$TMP_ROOT/initial-rescan.out"
start_daemon

echo "phase 1: install the actual inert floor fixture on Secondary"
wget -q -O "$TMP_ROOT/floor.zip" "$FLOOR_URL"
printf '%s  %s\n' "$FLOOR_SHA" "$TMP_ROOT/floor.zip" | sha256sum -c - >/dev/null
mkdir -p "$TMP_ROOT/floor-unzip"
unzip -q "$TMP_ROOT/floor.zip" -d "$TMP_ROOT/floor-unzip"
[ -d "$TMP_ROOT/floor-unzip/Syncthing.pak" ] || fail "floor archive shape is invalid"
mv "$TMP_ROOT/floor-unzip/Syncthing.pak" "$SECONDARY_TARGET"
sqlite3 -cmd '.timeout 5000' "$STATE/library.db" <<SQL
INSERT INTO pakrat_installs(store_id,version,platform,source_id,install_path,artifact_sha256,installed_at,commit_token)
VALUES('$STORE_ID','$FLOOR_VERSION','mlp1','secondary_sd','$INSTALL_REL','$FLOOR_SHA',strftime('%Y-%m-%dT%H:%M:%SZ','now'),NULL);
INSERT OR REPLACE INTO pakrat_service_metadata(store_id,install_path,package_id,display_name,has_service,service_id,state_root,revoke_json,retained_json,validated_at)
VALUES('$STORE_ID','$INSTALL_REL','$STORE_ID','Syncthing',0,'','','[]','[]',strftime('%Y-%m-%dT%H:%M:%SZ','now'));
SQL
run_pakrat rescan >"$TMP_ROOT/floor-rescan.out"
run_pakrat list >"$EVIDENCE/below-minimum.tsv"
grep -F "$(printf '\t%s\t%s\t' "$STORE_ID" "$FLOOR_VERSION")" \
    "$EVIDENCE/below-minimum.tsv" >/dev/null || fail "below-minimum client did not select the floor"
grep -F "gated=$REAL_VERSION" "$EVIDENCE/below-minimum.tsv" >/dev/null ||
    fail "below-minimum client did not explain the gated real version"
[ "$(db_value "SELECT source_id FROM pakrat_installs WHERE store_id='$STORE_ID';")" = secondary_sd ] ||
    fail "floor ownership is not Secondary"
[ "$(package_count)" = 1 ] || fail "floor setup created duplicate package ids"
[ ! -e "$SECONDARY_TARGET/bin/syncthing" ] || fail "floor contains upstream Syncthing"
[ ! -e "$SECONDARY_TARGET/service.sh" ] || fail "floor contains a service entry point"
! grep -F 'min_leaf_version' "$SECONDARY_TARGET/pak.json" >/dev/null ||
    fail "floor runtime manifest is gated"

echo "phase 2: crossing the minimum refuses a cross-root service update"
write_release "$MIN_LEAF_VERSION"
run_pakrat list >"$EVIDENCE/at-minimum-before-uninstall.tsv"
grep -F "$(printf '\t%s\t%s\t' "$STORE_ID" "$REAL_VERSION")" \
    "$EVIDENCE/at-minimum-before-uninstall.tsv" >/dev/null ||
    fail "at-minimum client did not select the real package"
set +e
run_pakrat install "$STORE_ID" >"$EVIDENCE/cross-root-refusal.log" 2>&1
install_rc=$?
set -e
[ "$install_rc" -ne 0 ] || fail "cross-root service update unexpectedly succeeded"
grep -F "installed on Secondary. Uninstall it there first, then install the service pak on Primary." \
    "$EVIDENCE/cross-root-refusal.log" >/dev/null || fail "cross-root refusal reason missing"
[ -d "$SECONDARY_TARGET" ] || fail "refused update removed the floor"
[ ! -e "$PRIMARY_TARGET" ] || fail "refused update created a Primary duplicate"
[ "$(package_count)" = 1 ] || fail "refused update changed package-id cardinality"
[ "$(db_value "SELECT source_id || '|' || version FROM pakrat_installs WHERE store_id='$STORE_ID';")" = "secondary_sd|$FLOOR_VERSION" ] ||
    fail "refused update changed floor ownership"

echo "phase 3: uninstall commits before the real package is offered on Primary"
run_pakrat uninstall "$STORE_ID" >"$EVIDENCE/secondary-uninstall.log"
[ ! -e "$SECONDARY_TARGET" ] || fail "Secondary floor remained after uninstall"
[ ! -e "$PRIMARY_TARGET" ] || fail "Primary package appeared before uninstall completed"
[ "$(db_value "SELECT COUNT(*) FROM pakrat_installs WHERE store_id='$STORE_ID';")" = 0 ] ||
    fail "floor install record remained after uninstall"
run_pakrat list >"$EVIDENCE/at-minimum-after-uninstall.tsv"
grep -F "$(printf 'available\t%s\t%s\t' "$STORE_ID" "$REAL_VERSION")" \
    "$EVIDENCE/at-minimum-after-uninstall.tsv" >/dev/null ||
    fail "real package was not offered after uninstall committed"

run_pakrat install "$STORE_ID" >"$EVIDENCE/primary-real-install.log"
[ -d "$PRIMARY_TARGET" ] || fail "real package was not installed on Primary"
[ ! -e "$SECONDARY_TARGET" ] || fail "real install left a Secondary duplicate"
[ "$(package_count)" = 1 ] || fail "real install created duplicate package ids"
[ "$(db_value "SELECT source_id || '|' || version FROM pakrat_installs WHERE store_id='$STORE_ID';")" = "primary|$REAL_VERSION" ] ||
    fail "real package ownership/version is wrong"
grep -F "\"pak_version\": \"$REAL_VERSION\"" "$PRIMARY_TARGET/pak.json" >/dev/null ||
    fail "real runtime pak version does not match the catalog"
grep -F "\"min_leaf_version\": \"$MIN_LEAF_VERSION\"" "$PRIMARY_TARGET/pak.json" >/dev/null ||
    fail "real runtime minimum does not match the catalog"
[ -x "$PRIMARY_TARGET/service.sh" ] || fail "real service wrapper is missing"
[ -x "$PRIMARY_TARGET/bin/syncthing" ] || fail "real upstream binary is missing"
for _ in $(seq 1 300); do
    [ "$(status_state || true)" = disabled ] && break
    sleep 0.02
done
[ "$(status_state || true)" = disabled ] || fail "real service did not install disabled"

write_release 0.9.0
set +e
clean_env "$PRIMARY_TARGET/service.sh" >"$EVIDENCE/runtime-below-minimum.log" 2>&1
runtime_rc=$?
set -e
[ "$runtime_rc" -eq 64 ] || fail "real service runtime gate exited $runtime_rc instead of 64"
write_release "$MIN_LEAF_VERSION"

sync
cp "$TMP_ROOT/jawakad.log" "$EVIDENCE/"
awk -v a="$PRIMARY" -v b="$SECONDARY" '$5 == a || $5 == b {print}' \
    /proc/self/mountinfo >"$EVIDENCE/bind-mountinfo.txt"
sqlite3 "$STATE/library.db" '.dump pakrat_installs' >"$EVIDENCE/final-install-record.sql"
sha256sum "$PRIMARY_TARGET/pak.json" "$PRIMARY_TARGET/launch.sh" \
    "$PRIMARY_TARGET/service.sh" "$PRIMARY_TARGET/bin/syncthing" \
    >"$EVIDENCE/final-package-sha256.txt"
printf '%s\n' \
    'B4b actual-artifact two-card transition: PASS' \
    "Floor: $FLOOR_VERSION ungated on Secondary" \
    "Real: $REAL_VERSION requires Leaf $MIN_LEAF_VERSION on Primary" \
    'Order: cross-root refusal -> durable uninstall -> Primary install' \
    'Duplicate package ids: 0 at every transition boundary' \
    >"$EVIDENCE/summary.txt"

echo "PASS mlp1-b4b-transition-device"
