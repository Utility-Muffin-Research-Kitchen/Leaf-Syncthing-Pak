#!/bin/sh
set -eu

PAK_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
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
USERDATA_PATH=${USERDATA_PATH:-$SDCARD_PATH/.userdata/$PLATFORM}
LOGS_PATH=${LOGS_PATH:-$USERDATA_PATH/logs}
mkdir -p "$LOGS_PATH"

message="This is the B0a Syncthing qualification package; the production controller and UI are not implemented yet."
printf '%s\n' "$message" | tee -a "$LOGS_PATH/leaf-syncthing-b0a.log"
printf 'Pinned runtime: %s\n' "$PAK_DIR/bin/syncthing"
exit 1
