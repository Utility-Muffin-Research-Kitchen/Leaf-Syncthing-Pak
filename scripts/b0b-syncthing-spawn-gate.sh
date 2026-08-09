#!/bin/sh
set -eu

if [ -n "${B0B_FRESH_STATE_LOG:-}" ] &&
   ! grep -F "life1: reconciled service=org.umrk.syncthing active=false" "$B0B_FRESH_STATE_LOG" >/dev/null 2>&1; then
    : >"${B0B_FRESH_STATE_VIOLATION:?}"
    exit 97
fi

exec "$0.real" "$@"
