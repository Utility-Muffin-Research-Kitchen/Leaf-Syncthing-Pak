#!/bin/sh
set -eu

PAK_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
PLATFORM=${PLATFORM:-mlp1}

if [ -n "${UMRK_ENV_FILE:-}" ] && [ -f "$UMRK_ENV_FILE" ]; then
    # shellcheck disable=SC1090
    . "$UMRK_ENV_FILE"
elif [ -n "${SDCARD_PATH:-}" ] &&
     [ -f "$SDCARD_PATH/.system/leaf/platforms/$PLATFORM/launcher/env.sh" ]; then
    # shellcheck disable=SC1090
    . "$SDCARD_PATH/.system/leaf/platforms/$PLATFORM/launcher/env.sh"
fi

SDCARD_PATH=${SDCARD_PATH:-/mnt/sdcard}
# shellcheck source=lib/leaf-version-gate.sh
. "$PAK_DIR/lib/leaf-version-gate.sh"

gate_status=0
leaf_version_gate_manifest "$PAK_DIR/pak.json" \
    "$SDCARD_PATH/.umrk/$PLATFORM/release.json" || gate_status=$?
case "$gate_status" in
    0|2) ;;
    *)
        echo "leaf-syncthing: refusing to start: $LEAF_VERSION_GATE_REASON" >&2
        echo "leaf-syncthing: installed=$LEAF_INSTALLED_VERSION required=$LEAF_REQUIRED_VERSION" >&2
        exit 64
        ;;
esac

exec "$PAK_DIR/bin/leaf-syncthing" service run
