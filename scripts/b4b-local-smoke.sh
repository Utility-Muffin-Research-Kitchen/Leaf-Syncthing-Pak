#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKSPACE="$(cd "$ROOT_DIR/.." && pwd)"
LEAF_DIR="$WORKSPACE/Leaf"
JAWAKA_DIR="$WORKSPACE/Jawaka"
PORT="$(python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)"
BASE_URL="http://127.0.0.1:$PORT/pakrat/v1/"
FEED_ROOT="$LEAF_DIR/build/pakrat-local/syncthing-b4b"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/syncthing-b4b-smoke.XXXXXX")"
SERVER_PID=

cleanup() {
    if [ -n "$SERVER_PID" ]; then
        kill "$SERVER_PID" >/dev/null 2>&1 || true
        wait "$SERVER_PID" >/dev/null 2>&1 || true
    fi
    rm -rf "$TMP_ROOT"
}
trap cleanup EXIT HUP INT TERM

python3 "$ROOT_DIR/scripts/build_b4b_fixture.py" \
    --base-url "$BASE_URL" --skip-build

python3 -m http.server "$PORT" --bind 127.0.0.1 \
    --directory "$FEED_ROOT" >"$TMP_ROOT/http.log" 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 50); do
    curl -fsS "$BASE_URL/storefront.json" >/dev/null 2>&1 && break
    kill -0 "$SERVER_PID" >/dev/null 2>&1 || {
        cat "$TMP_ROOT/http.log" >&2
        exit 1
    }
    sleep 0.1
done
curl -fsS "$BASE_URL/storefront.json" >/dev/null

BUILD_DIR="build/syncthing-b4b-smoke"
make -C "$JAWAKA_DIR" BUILD="$BUILD_DIR" jawaka-pakrat-smoke >/dev/null
SMOKE="$JAWAKA_DIR/$BUILD_DIR/bin/jawaka-pakrat-smoke"
SD_ROOT="$TMP_ROOT/sd"
STATE_DIR="$SD_ROOT/.umrk/mlp1"
PLATFORM_ROOT="$SD_ROOT/.system/leaf/platforms/mlp1"
mkdir -p "$STATE_DIR" "$PLATFORM_ROOT"
printf '%s\n' '{"managed_apps":[]}' >"$PLATFORM_ROOT/manifest.json"

write_release() {
    printf '{"schema":1,"product":"leaf","platform":"mlp1","version":"%s","release_id":"%s"}\n' \
        "$1" "$1" >"$STATE_DIR/release.json"
}

run_smoke() {
    PAKRAT_CATALOG_BASE_URL="$BASE_URL" "$SMOKE" \
        --platform mlp1 --sdcard-root "$SD_ROOT" "$@"
}

expect_app_set() {
    local output=$1
    for app_id in org.umrk.itchio org.umrk.discoboy org.umrk.nimbus \
        org.umrk.portmaster org.helaas.sdlreader org.umrk.syncthing; do
        grep -F "$app_id" "$output" >/dev/null || {
            echo "storefront omitted $app_id" >&2
            cat "$output" >&2
            exit 1
        }
    done
}

write_release v0.9.0
below="$TMP_ROOT/below.tsv"
run_smoke list >"$below"
expect_app_set "$below"
grep -F $'available\torg.umrk.syncthing\t0.0.1\t' "$below" >/dev/null
grep -F $'gated=0.0.2\tmin_leaf=99.99.99' "$below" >/dev/null
run_smoke install org.umrk.syncthing >/dev/null
python3 - "$SD_ROOT/Apps/mlp1/Syncthing.pak/pak.json" <<'PY'
import json
import sys
manifest = json.load(open(sys.argv[1], encoding="utf-8"))
assert manifest["pak_version"] == "0.0.1"
assert "min_leaf_version" not in manifest
assert "service" not in manifest
PY
test ! -e "$SD_ROOT/Apps/mlp1/Syncthing.pak/bin/syncthing"
if run_smoke install-target org.umrk.syncthing 0.0.2 >/dev/null 2>&1; then
    echo "below-minimum install-time recheck accepted the real pak" >&2
    exit 1
fi

set +e
SDCARD_PATH="$SD_ROOT" PLATFORM=mlp1 \
    "$ROOT_DIR/build/mlp1/package/Syncthing.pak/service.sh" \
    >"$TMP_ROOT/runtime-gate.log" 2>&1
runtime_status=$?
set -e
[ "$runtime_status" -eq 64 ] || {
    cat "$TMP_ROOT/runtime-gate.log" >&2
    echo "runtime self-check returned $runtime_status instead of 64" >&2
    exit 1
}
grep -F 'refusing to start: below-minimum' "$TMP_ROOT/runtime-gate.log" >/dev/null

write_release v99.99.99
exact="$TMP_ROOT/exact.tsv"
run_smoke list >"$exact"
expect_app_set "$exact"
grep -F $'org.umrk.syncthing\t0.0.2\tinstalled=0.0.1' "$exact" >/dev/null

write_release v100.0.0
above="$TMP_ROOT/above.tsv"
run_smoke list >"$above"
expect_app_set "$above"
grep -F $'org.umrk.syncthing\t0.0.2\tinstalled=0.0.1' "$above" >/dev/null

echo "B4b local catalog smoke: PASS"
