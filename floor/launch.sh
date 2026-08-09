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
# shellcheck source=../lib/leaf-version-gate.sh
. "$PAK_DIR/lib/leaf-version-gate.sh"

minimum=$(leaf_version_read_requirement "$PAK_DIR/requirements/min-leaf-version") || {
    echo "Syncthing floor pak has no valid Leaf requirement" >&2
    exit 64
}
installed=$(leaf_version_json_string \
    "$SDCARD_PATH/.umrk/$PLATFORM/release.json" version 2>/dev/null || printf '%s\n' Unknown)
leaf_version_components "$installed" 1 >/dev/null 2>&1 || installed=Unknown

exec "$PAK_DIR/bin/leaf-syncthing-floor" "$minimum" "$installed"
