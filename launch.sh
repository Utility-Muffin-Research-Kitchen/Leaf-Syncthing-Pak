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

message="The Syncthing B1 service is managed from Leaf Settings. Its device UI arrives in B2."
printf '%s\n' "$message" | tee -a "$LOGS_PATH/leaf-syncthing-b1.log"
printf 'Controller: %s\n' "$PAK_DIR/bin/leaf-syncthing"
exit 1
