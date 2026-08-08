#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
JAWAKA_DIR="${JAWAKA_DIR:-$ROOT_DIR/../Jawaka}"
REMOTE_DIR="${LEAF_SYNCTHING_B0B_REMOTE_DIR:-/tmp/leaf-syncthing-b0b}"
PRIMARY_CARD_ROOT="${LEAF_SYNCTHING_B0B_PRIMARY_ROOT:-/mnt/sdcard/.leaf-syncthing-b0b}"
SECONDARY_CARD_ROOT="${LEAF_SYNCTHING_B0B_SECONDARY_ROOT:-/media/sdcard1/.leaf-syncthing-b0b}"
PRIMARY_ROOT="$REMOTE_DIR/primary"
SECONDARY_ROOT="$REMOTE_DIR/secondary"
SERVICE_ID=org.umrk.syncthing
LIVE_PANGU_PID=""
DAEMON_PID=""
SENDER_PID=""
FRESH_STATE_LOG=""
FRESH_STATE_VIOLATION=""
POWER_CUT_RECOVERY="none"
TRANSFER_MIB="${B0B_TRANSFER_MIB:-128}"

case "$TRANSFER_MIB" in
    ''|*[!0-9]*|0) echo "B0B_TRANSFER_MIB must be a positive integer" >&2; exit 1 ;;
esac
TRANSFER_BYTES=$((TRANSFER_MIB * 1024 * 1024))

case "$REMOTE_DIR:$PRIMARY_CARD_ROOT:$SECONDARY_CARD_ROOT" in
    /tmp/leaf-syncthing-b0b:*'.leaf-syncthing-b0b:'*'.leaf-syncthing-b0b') ;;
    *) echo "unsafe B0b fixture roots" >&2; exit 1 ;;
esac

if [ -n "${ADB_SERIAL:-}" ]; then
    ADB=(adb -s "$ADB_SERIAL")
else
    serial="$(adb devices | awk 'NR>1 && $2=="device" {print $1; exit}')"
    [ -n "$serial" ] || { echo "No online adb device found." >&2; exit 1; }
    ADB=(adb -s "$serial")
fi

make -C "$ROOT_DIR" package-mlp1 >/dev/null
for binary in jawakad jawaka-platformctl game-writer-fixture; do
    [ -x "$JAWAKA_DIR/build/mlp1/bin/$binary" ] || {
        echo "missing MLP1 Jawaka binary: $binary" >&2
        exit 1
    }
done

cleanup() {
    status=$?
    set +e
    [ -z "$SENDER_PID" ] || "${ADB[@]}" shell "kill '$SENDER_PID' 2>/dev/null || true"
    [ -z "$DAEMON_PID" ] || "${ADB[@]}" shell "kill '$DAEMON_PID' 2>/dev/null || true"
    if [ "$status" -ne 0 ]; then
        echo "B0b device logs after failure:" >&2
        "${ADB[@]}" shell "tail -160 '$REMOTE_DIR/logs/jawakad.log' 2>/dev/null || true; find '$REMOTE_DIR/logs/services' -type f -maxdepth 3 -exec tail -120 {} \; 2>/dev/null || true" >&2
    fi
    "${ADB[@]}" shell "ps -eo pid,comm,args | awk -v root='$REMOTE_DIR' -v a='$PRIMARY_CARD_ROOT' -v b='$SECONDARY_CARD_ROOT' '(\$2==\"jawakad\"||\$2==\"syncthing\"||\$2==\"syncthing.real\"||\$2==\"leaf-syncthing\"||\$2==\"game-writer-fix\")&&(index(\$0,root)||index(\$0,a)||index(\$0,b)){print \$1}' | xargs -r kill -KILL" 2>/dev/null || true
    "${ADB[@]}" shell "umount '$PRIMARY_ROOT' 2>/dev/null || true; umount '$SECONDARY_ROOT' 2>/dev/null || true; rm -rf '$REMOTE_DIR' '$PRIMARY_CARD_ROOT' '$SECONDARY_CARD_ROOT'" >/dev/null 2>&1 || true
    if [ -n "$LIVE_PANGU_PID" ]; then
        "${ADB[@]}" shell "kill -CONT '$LIVE_PANGU_PID' 2>/dev/null || true" >/dev/null
    fi
    exit "$status"
}
trap cleanup EXIT

wait_remote() {
    command=$1
    attempts=${2:-400}
    for _ in $(seq 1 "$attempts"); do
        "${ADB[@]}" shell "$command" >/dev/null 2>&1 && return 0
        [ -z "$DAEMON_PID" ] || "${ADB[@]}" shell "kill -0 '$DAEMON_PID' 2>/dev/null" || return 1
        sleep 0.05
    done
    echo "timed out waiting for: $command" >&2
    return 1
}

request() {
    "${ADB[@]}" shell "'$REMOTE_DIR/bin/jawaka-platformctl' --socket '$REMOTE_DIR/runtime/jawakad.sock' request '$1'" | tr -d '\r'
}

pause_live_pangu() {
    LIVE_PANGU_PID=""
    for _ in $(seq 1 20); do
        candidate="$("${ADB[@]}" shell 'pidof loong_pangu 2>/dev/null || true' | tr -d '\r' | awk '{print $1}')"
        if [ -n "$candidate" ] &&
           "${ADB[@]}" shell "kill -STOP '$candidate' 2>/dev/null; grep -Eq '^State:[[:space:]]+T' '/proc/$candidate/status'" >/dev/null 2>&1; then
            LIVE_PANGU_PID="$candidate"
            return 0
        fi
        sleep 0.05
    done
}

recover_read_only_mount() {
    mount_path=$1
    mode="$("${ADB[@]}" shell "awk -v target='$mount_path' '\$2==target {print \$4; exit}' /proc/mounts" | tr -d '\r')"
    case ",$mode," in
        *,rw,*) return 0 ;;
        *,ro,*) ;;
        *) echo "missing card mount: $mount_path" >&2; return 1 ;;
    esac
    device="$("${ADB[@]}" shell "awk -v target='$mount_path' '\$2==target {print \$1; exit}' /proc/mounts" | tr -d '\r')"
    case "$device" in
        /dev/mmcblk1p1|/dev/mmcblk3p1) ;;
        *) echo "refusing unexpected card device: $device" >&2; return 1 ;;
    esac
    echo "Repairing forced-reboot FAT fixture on $device ($mount_path)"
    "${ADB[@]}" shell "set +e
        umount '$mount_path' || exit 2
        fsck.vfat -a '$device'
        code=\$?
        if [ \$code -gt 1 ]; then exit \$code; fi
        mount -t vfat -o rw,nosuid,nodev,noatime,nodiratime,fmask=0022,dmask=0022,codepage=936,iocharset=utf8,shortname=mixed,errors=remount-ro '$device' '$mount_path'"
    POWER_CUT_RECOVERY="fsck"
}

measure_completion() {
    label=$1
    "${ADB[@]}" shell "set -eu
        total=0; maximum=0; samples=10; index=0
        while [ \$index -lt \$samples ]; do
            start=\$(date +%s%N)
            response=\$(printf 'GET /rest/db/completion?folder=$folder_id&device=$sender_id HTTP/1.1\r\nHost: unix\r\nX-API-Key: $receiver_api_key\r\nConnection: close\r\n\r\n' | '$REMOTE_DIR/bin/socat' - UNIX-CONNECT:'$receiver_gui')
            printf '%s' \"\$response\" | grep -F 'globalBytes' >/dev/null
            elapsed=\$((\$(date +%s%N) - start))
            total=\$((total + elapsed)); [ \$elapsed -le \$maximum ] || maximum=\$elapsed
            index=\$((index + 1))
        done
        echo B0B_PENDING_RESULT peer=$label samples=\$samples average_ms=\$((total / samples / 1000000)) maximum_ms=\$((maximum / 1000000))"
}

measure_cold_scan() {
    target=$1
    "${ADB[@]}" shell "set -eu
        mkdir -p '$PRIMARY_ROOT/Saves/b0b-tree'
        index=\$(find '$PRIMARY_ROOT/Saves/b0b-tree' -type f | wc -l)
        while [ \$index -lt '$target' ]; do
            bucket=\$((index / 1000)); mkdir -p '$PRIMARY_ROOT/Saves/b0b-tree/'\$bucket
            printf '%08d\n' \"\$index\" >'$PRIMARY_ROOT/Saves/b0b-tree/'\$bucket/item-\$index
            index=\$((index + 1))
        done
        sync"
    response="$(request "{\"v\":1,\"op\":\"stop\",\"id\":\"scan-$target-stop\",\"service_id\":\"$SERVICE_ID\"}")"
    grep -F '"ok":true' <<<"$response" >/dev/null
    wait_remote "test ! -e '$control'" 600
    "${ADB[@]}" shell "rm -rf '$userdata/Syncthing/data'"
    start_ns="$("${ADB[@]}" shell 'date +%s%N' | tr -d '\r')"
    response="$(request "{\"v\":1,\"op\":\"run\",\"id\":\"scan-$target-run\",\"service_id\":\"$SERVICE_ID\"}")"
    grep -F '"ok":true' <<<"$response" >/dev/null
    wait_remote "test -S '$receiver_gui'" 600
    "${ADB[@]}" shell "$receiver_cli folders '$folder_id' paused set false >/dev/null"
    wait_remote "printf 'GET /rest/db/status?folder=$folder_id HTTP/1.1\r\nHost: unix\r\nX-API-Key: $receiver_api_key\r\nConnection: close\r\n\r\n' | '$REMOTE_DIR/bin/socat' - UNIX-CONNECT:'$receiver_gui' | tr -d '[:space:]' | grep -F '\"state\":\"idle\"'" 3600
    end_ns="$("${ADB[@]}" shell 'date +%s%N' | tr -d '\r')"
    echo "B0B_SCAN_RESULT files=$target cold_start_to_idle_ms=$(((10#$end_ns - 10#$start_ns) / 1000000))"
}

verify_restart_races() {
    response="$(request "{\"v\":1,\"op\":\"enable\",\"id\":\"race-enable\",\"service_id\":\"$SERVICE_ID\"}")"
    grep -F '"ok":true' <<<"$response" >/dev/null
    "${ADB[@]}" shell "rm -f '$REMOTE_DIR/runtime/game-writer-live' '$REMOTE_DIR/runtime/game-writer-done' '$REMOTE_DIR/runtime/active-game.json'; \
        ('$REMOTE_DIR/bin/jawaka-platformctl' --socket '$REMOTE_DIR/runtime/jawakad.sock' request '{\"type\":\"launch-game\",\"system\":\"N64\",\"rom_path\":\"Roms/N64/Barrier.n64\"}' >/dev/null) &"
    wait_remote "test -f '$REMOTE_DIR/runtime/game-writer-live'" 600
    race_writer="$("${ADB[@]}" shell "ps -eo pid,comm,args | awk -v path='$PRIMARY_ROOT/.system/leaf/platforms/mlp1/emulators/fixture/game-writer-fixture' '\$2==\"game-writer-fix\" && index(\$0,path){print \$1; exit}'" | tr -d '\r')"
    "${ADB[@]}" shell "kill -STOP '$race_writer'; test ! -e '$control'; ! ps -eo args | grep -F '$receiver_binary' | grep -v grep; kill -KILL '$DAEMON_PID'"
    old_daemon=$DAEMON_PID
    for _ in $(seq 1 200); do
        "${ADB[@]}" shell "kill -0 '$old_daemon' 2>/dev/null" >/dev/null 2>&1 || break
        sleep 0.05
    done
    "${ADB[@]}" shell "rm -rf '$SECONDARY_ROOT/reboot-backup'; mkdir -p '$SECONDARY_ROOT/reboot-backup/Syncthing'; \
        cp -a '$userdata/Syncthing/config' '$SECONDARY_ROOT/reboot-backup/Syncthing/'; \
        cp -a '$userdata/Syncthing/card-id' '$SECONDARY_ROOT/reboot-backup/Syncthing/'; \
        cp -a '$state' '$SECONDARY_ROOT/reboot-backup/Jawaka'; sync"
    DAEMON_PID=""
    start_daemon
    wait_remote "grep -F 'life1: recovered active launch' '$REMOTE_DIR/logs/jawakad.log'" 600
    active_run="$(request "{\"v\":1,\"op\":\"run\",\"id\":\"replacement-run\",\"service_id\":\"$SERVICE_ID\"}" || true)"
    grep -F 'lifecycle-in-progress' <<<"$active_run" >/dev/null
    "${ADB[@]}" shell "test -f '$REMOTE_DIR/runtime/active-game.json'; kill -0 '$race_writer'; ! ps -eo args | grep -F '$receiver_binary' | grep -v grep"

    DAEMON_PID=""
    "${ADB[@]}" shell "reboot -nf" >/dev/null 2>&1 || true
    "${ADB[@]}" wait-for-device
    for _ in $(seq 1 240); do
        if "${ADB[@]}" shell "mountpoint -q /mnt/sdcard && mountpoint -q /media/sdcard1" >/dev/null 2>&1; then
            break
        fi
        sleep 0.25
    done
    "${ADB[@]}" shell "mountpoint -q /mnt/sdcard && mountpoint -q /media/sdcard1"
    pause_live_pangu
    primary_after="$("${ADB[@]}" shell "for root in /mnt/sdcard/.leaf-syncthing-b0b /media/sdcard1/.leaf-syncthing-b0b; do grep -qx primary \"\$root/.fixture-role\" 2>/dev/null && echo \"\$root\"; done; true" | tr -d '\r')"
    secondary_after="$("${ADB[@]}" shell "for root in /mnt/sdcard/.leaf-syncthing-b0b /media/sdcard1/.leaf-syncthing-b0b; do grep -qx secondary \"\$root/.fixture-role\" 2>/dev/null && echo \"\$root\"; done; true" | tr -d '\r')"
    if [ "$(printf '%s\n' "$primary_after" | grep -c .)" -ne 1 ] ||
       [ "$(printf '%s\n' "$secondary_after" | grep -c .)" -ne 1 ]; then
        echo "could not resolve post-reboot card roles: primary='$primary_after' secondary='$secondary_after'" >&2
        return 1
    fi
    PRIMARY_CARD_ROOT="$primary_after"
    SECONDARY_CARD_ROOT="$secondary_after"
    recover_read_only_mount "${PRIMARY_CARD_ROOT%/.leaf-syncthing-b0b}"
    recover_read_only_mount "${SECONDARY_CARD_ROOT%/.leaf-syncthing-b0b}"
    if [ "$POWER_CUT_RECOVERY" = fsck ]; then
        "${ADB[@]}" shell "set -eu
            test -s '$SECONDARY_CARD_ROOT/reboot-backup/Syncthing/config/config.xml'
            rm -rf '$PRIMARY_CARD_ROOT/.userdata/mlp1/Syncthing/config' '$PRIMARY_CARD_ROOT/.userdata/mlp1/Syncthing/data' '$PRIMARY_CARD_ROOT/.userdata/mlp1/Jawaka'
            mkdir -p '$PRIMARY_CARD_ROOT/.userdata/mlp1/Syncthing'
            cp -a '$SECONDARY_CARD_ROOT/reboot-backup/Syncthing/config' '$PRIMARY_CARD_ROOT/.userdata/mlp1/Syncthing/'
            cp -a '$SECONDARY_CARD_ROOT/reboot-backup/Syncthing/card-id' '$PRIMARY_CARD_ROOT/.userdata/mlp1/Syncthing/'
            cp -a '$SECONDARY_CARD_ROOT/reboot-backup/Jawaka' '$PRIMARY_CARD_ROOT/.userdata/mlp1/Jawaka'
            sync"
    fi
    "${ADB[@]}" shell "rm -rf '$REMOTE_DIR'; mkdir -p '$REMOTE_DIR/bin' '$REMOTE_DIR/runtime' '$REMOTE_DIR/logs' '$PRIMARY_ROOT' '$SECONDARY_ROOT'; \
        mount --bind '$PRIMARY_CARD_ROOT' '$PRIMARY_ROOT'; mount --bind '$SECONDARY_CARD_ROOT' '$SECONDARY_ROOT'; \
        cp /usr/bin/socat '$REMOTE_DIR/bin/socat'; chmod 755 '$REMOTE_DIR/bin/socat'"
    "${ADB[@]}" push "$JAWAKA_DIR/build/mlp1/bin/jawakad" "$JAWAKA_DIR/build/mlp1/bin/jawaka-platformctl" "$REMOTE_DIR/bin/" >/dev/null
    "${ADB[@]}" shell "chmod 755 '$REMOTE_DIR/bin/jawakad' '$REMOTE_DIR/bin/jawaka-platformctl'; test ! -e '$REMOTE_DIR/runtime/active-game.json'"
    FRESH_STATE_LOG="$REMOTE_DIR/logs/jawakad.log"
    FRESH_STATE_VIOLATION="$REMOTE_DIR/runtime/upstream-before-fresh-state"
    start_daemon
    wait_remote "grep -F 'life1: reconciled service=$SERVICE_ID active=false' '$REMOTE_DIR/logs/jawakad.log'" 1200
    wait_remote "test -S '$control'" 1200
    status="$(request "{\"v\":1,\"op\":\"status\",\"id\":\"post-reboot\",\"service_id\":\"$SERVICE_ID\"}")"
    grep -F '"desired_enabled":true' <<<"$status" >/dev/null
    grep -F '"effective_state":"running"' <<<"$status" >/dev/null
    "${ADB[@]}" shell "test ! -e '$FRESH_STATE_VIOLATION'; ps -eo args | grep -F '$receiver_binary' | grep -v grep >/dev/null"
    echo "B0B_RACE_RESULT daemon_restart_active=blocked forced_reboot_state=fresh-inactive upstream_gate=passed service_restart=running storage_recovery=$POWER_CUT_RECOVERY"
}

echo "Using adb device: $("${ADB[@]}" get-serialno)"
"${ADB[@]}" shell "mountpoint -q /mnt/sdcard && mountpoint -q /media/sdcard1"
pause_live_pangu

"${ADB[@]}" shell "umount '$PRIMARY_ROOT' 2>/dev/null || true; umount '$SECONDARY_ROOT' 2>/dev/null || true; rm -rf '$REMOTE_DIR' '$PRIMARY_CARD_ROOT' '$SECONDARY_CARD_ROOT'; mkdir -p \
    '$REMOTE_DIR/bin' '$REMOTE_DIR/runtime' '$REMOTE_DIR/state' '$REMOTE_DIR/logs' '$PRIMARY_ROOT' '$SECONDARY_ROOT' \
    '$PRIMARY_CARD_ROOT' '$SECONDARY_CARD_ROOT'; mount --bind '$PRIMARY_CARD_ROOT' '$PRIMARY_ROOT'; mount --bind '$SECONDARY_CARD_ROOT' '$SECONDARY_ROOT'; mkdir -p \
    '$PRIMARY_ROOT/Apps/mlp1' '$PRIMARY_ROOT/.system/leaf/platforms/mlp1/defaults' \
    '$PRIMARY_ROOT/.system/leaf/platforms/mlp1/emulators/fixture' '$PRIMARY_ROOT/Roms/N64' \
    '$PRIMARY_ROOT/Images/N64' '$PRIMARY_ROOT/BIOS' '$PRIMARY_ROOT/Saves' '$PRIMARY_ROOT/States' \
    '$PRIMARY_ROOT/Music' '$PRIMARY_ROOT/Videos' '$PRIMARY_ROOT/Cheats' \
    '$PRIMARY_ROOT/.userdata/mlp1' '$PRIMARY_ROOT/.userdata/shared' \
    '$SECONDARY_ROOT/Apps/mlp1' '$SECONDARY_ROOT/Roms' '$SECONDARY_ROOT/Images' \
    '$SECONDARY_ROOT/BIOS' '$SECONDARY_ROOT/Saves' '$SECONDARY_ROOT/States' \
    '$SECONDARY_ROOT/Music' '$SECONDARY_ROOT/Videos' '$SECONDARY_ROOT/Cheats' \
    '$SECONDARY_ROOT/.userdata/mlp1' '$SECONDARY_ROOT/.userdata/shared' '$SECONDARY_ROOT/Sender'; \
    printf 'primary\n' >'$PRIMARY_ROOT/.fixture-role'; printf 'secondary\n' >'$SECONDARY_ROOT/.fixture-role'; \
    cp /usr/bin/socat '$REMOTE_DIR/bin/socat'; chmod 755 '$REMOTE_DIR/bin/socat'"
"${ADB[@]}" push "$JAWAKA_DIR/build/mlp1/bin/jawakad" "$JAWAKA_DIR/build/mlp1/bin/jawaka-platformctl" "$REMOTE_DIR/bin/" >/dev/null
"${ADB[@]}" push "$JAWAKA_DIR/build/mlp1/bin/game-writer-fixture" "$PRIMARY_ROOT/.system/leaf/platforms/mlp1/emulators/fixture/" >/dev/null
"${ADB[@]}" push "$ROOT_DIR/build/mlp1/package/Syncthing.pak" "$PRIMARY_ROOT/Apps/mlp1/" >/dev/null
"${ADB[@]}" shell "mv '$PRIMARY_ROOT/Apps/mlp1/Syncthing.pak/bin/syncthing' '$PRIMARY_ROOT/Apps/mlp1/Syncthing.pak/bin/syncthing.real'"
"${ADB[@]}" push "$ROOT_DIR/scripts/b0b-syncthing-spawn-gate.sh" "$PRIMARY_ROOT/Apps/mlp1/Syncthing.pak/bin/syncthing" >/dev/null
"${ADB[@]}" shell "chmod 755 '$REMOTE_DIR/bin/'* '$PRIMARY_ROOT/Apps/mlp1/Syncthing.pak/bin/'* '$PRIMARY_ROOT/.system/leaf/platforms/mlp1/emulators/fixture/'*; \
    printf '%s\n' '{\"version\":2,\"platform\":\"mlp1\",\"cores\":[{\"id\":\"writer_fixture\",\"display_name\":\"Writer Fixture\",\"type\":\"path\",\"path\":\"emulators/fixture/game-writer-fixture\",\"supports_menu\":false,\"supports_savestate\":true,\"supports_disk_control\":false,\"needs_swap\":false,\"status\":\"packaged\"}]}' >'$PRIMARY_ROOT/.system/leaf/platforms/mlp1/defaults/cores.json'; \
    printf '%s\n' '{\"version\":2,\"platform\":\"mlp1\",\"systems\":[{\"id\":\"N64\",\"name\":\"Nintendo 64\",\"patterns\":[\"N64\"],\"extensions\":[\"n64\"],\"archive_extensions\":[],\"archive_inner_extensions\":[\"n64\"],\"archive_mode\":\"pass_through\",\"file_names\":[],\"ignore_file_names\":[],\"playlist_extensions\":[],\"m3u_generation\":\"none\",\"default_core\":\"writer_fixture\",\"alternate_cores\":[],\"rom_root\":\"Roms/N64\",\"image_root\":\"Images/N64\",\"bios_notes\":[]}]}' >'$PRIMARY_ROOT/.system/leaf/platforms/mlp1/defaults/systems.json'; \
    printf 'rom\n' >'$PRIMARY_ROOT/Roms/N64/Barrier.n64'"

userdata="$PRIMARY_ROOT/.userdata/mlp1"
sender_config="$SECONDARY_ROOT/.userdata/mlp1/Sender/config"
sender_data="$SECONDARY_ROOT/.userdata/mlp1/Sender/data"
receiver_binary="$PRIMARY_ROOT/Apps/mlp1/Syncthing.pak/bin/syncthing"
receiver_gui="$REMOTE_DIR/runtime/services/$SERVICE_ID/syncthing-gui.sock"
sender_gui="$REMOTE_DIR/runtime/sender-gui.sock"
state="$PRIMARY_ROOT/.userdata/mlp1/Jawaka"

start_daemon() {
    "${ADB[@]}" shell "mkdir -p '$state' '$REMOTE_DIR/runtime' '$REMOTE_DIR/logs'; rm -f '$REMOTE_DIR/runtime/jawakad.sock'; (cd '$REMOTE_DIR' && exec env UMRK_ENV_VERSION=2 PLATFORM=mlp1 \
        SDCARD_PATH='$PRIMARY_ROOT' SDCARD_PATHS='$PRIMARY_ROOT:$SECONDARY_ROOT' \
        ROMS_PATH='$PRIMARY_ROOT/Roms' ROMS_PATHS='$PRIMARY_ROOT/Roms:$SECONDARY_ROOT/Roms' \
        IMAGES_PATH='$PRIMARY_ROOT/Images' IMAGES_PATHS='$PRIMARY_ROOT/Images:$SECONDARY_ROOT/Images' \
        APPS_PATH='$PRIMARY_ROOT/Apps' APPS_PATHS='$PRIMARY_ROOT/Apps:$SECONDARY_ROOT/Apps' \
        USERDATA_PATH='$userdata' USERDATA_PATHS='$userdata:$SECONDARY_ROOT/.userdata/mlp1' \
        SHARED_USERDATA_PATH='$PRIMARY_ROOT/.userdata/shared' SHARED_USERDATA_PATHS='$PRIMARY_ROOT/.userdata/shared:$SECONDARY_ROOT/.userdata/shared' \
        LOGS_PATH='$REMOTE_DIR/logs' MUSIC_PATH='$PRIMARY_ROOT/Music' MUSIC_PATHS='$PRIMARY_ROOT/Music:$SECONDARY_ROOT/Music' \
        VIDEO_PATH='$PRIMARY_ROOT/Videos' VIDEO_PATHS='$PRIMARY_ROOT/Videos:$SECONDARY_ROOT/Videos' \
        BIOS_PATH='$PRIMARY_ROOT/BIOS' BIOS_PATHS='$PRIMARY_ROOT/BIOS:$SECONDARY_ROOT/BIOS' \
        SAVES_PATH='$PRIMARY_ROOT/Saves' SAVES_PATHS='$PRIMARY_ROOT/Saves:$SECONDARY_ROOT/Saves' \
        STATES_PATH='$PRIMARY_ROOT/States' STATES_PATHS='$PRIMARY_ROOT/States:$SECONDARY_ROOT/States' \
        CHEATS_PATH='$PRIMARY_ROOT/Cheats' CHEATS_PATHS='$PRIMARY_ROOT/Cheats:$SECONDARY_ROOT/Cheats' \
        UMRK_PLATFORM_PATH='$PRIMARY_ROOT/.system/leaf/platforms/mlp1' UMRK_RUNTIME_PATH='$REMOTE_DIR/runtime' \
        JAWAKA_RUNTIME_DIR='$REMOTE_DIR/runtime' UMRK_DAEMON_SOCKET='$REMOTE_DIR/runtime/jawakad.sock' \
        UMRK_INTERNAL_DATA_PATH='$state' JAWAKA_SDCARD_ROOT='$PRIMARY_ROOT' \
        B0B_FRESH_STATE_LOG='$FRESH_STATE_LOG' B0B_FRESH_STATE_VIOLATION='$FRESH_STATE_VIOLATION' \
        '$REMOTE_DIR/bin/jawakad' --daemon-only) </dev/null >>'$REMOTE_DIR/logs/jawakad.log' 2>&1 & echo \$! >'$REMOTE_DIR/daemon.pid'"
    DAEMON_PID="$("${ADB[@]}" shell "cat '$REMOTE_DIR/daemon.pid'" | tr -d '\r')"
    wait_remote "test -S '$REMOTE_DIR/runtime/jawakad.sock'"
}

start_daemon
wait_remote "'$REMOTE_DIR/bin/jawaka-platformctl' --socket '$REMOTE_DIR/runtime/jawakad.sock' request '{\"v\":1,\"op\":\"list\",\"id\":\"discover\"}' | grep -F '$SERVICE_ID'" 600

response="$(request '{"v":1,"op":"run","id":"initial","service_id":"org.umrk.syncthing"}')"
grep -F '"ok":true' <<<"$response" >/dev/null
wait_remote "test -S '$receiver_gui'" 600
control="$REMOTE_DIR/runtime/services/$SERVICE_ID/control.sock"
wait_remote "test -S '$control'"
response="$("${ADB[@]}" shell "'$REMOTE_DIR/bin/jawaka-platformctl' --socket '$control' request '{\"v\":1,\"id\":\"enroll\",\"op\":\"card.enroll\",\"args\":{\"source_id\":\"primary\"}}'" | tr -d '\r')"
grep -F '"ok":true' <<<"$response" >/dev/null
card_id="$("${ADB[@]}" shell "sed -n 's/.*\"id\":\"\([0-9a-f]*\)\".*/\1/p' '$userdata/Syncthing/card-id'" | tr -d '\r')"
digest="$(printf '%s' "${card_id}saves" | shasum -a 256 | awk '{print $1}')"
folder_id="leaf-saves-${digest:0:16}"
marker=".leaf-saves-${digest:0:12}"

"${ADB[@]}" shell "'$receiver_binary' generate --config='$sender_config' --data='$sender_data' --no-port-probing >/dev/null 2>&1; mkdir -p '$SECONDARY_ROOT/Sender/.stfolder' '$PRIMARY_ROOT/Saves/$marker'"
receiver_id="$("${ADB[@]}" shell "'$receiver_binary' -C '$userdata/Syncthing/config' -D '$userdata/Syncthing/data' device-id" | tr -d '\r')"
sender_id="$("${ADB[@]}" shell "'$receiver_binary' -C '$sender_config' -D '$sender_data' device-id" | tr -d '\r')"
receiver_api_key="$("${ADB[@]}" shell "sed -n 's:.*<apikey>\([^<]*\)</apikey>.*:\1:p' '$userdata/Syncthing/config/config.xml'" | tr -d '\r')"
sender_api_key="$("${ADB[@]}" shell "sed -n 's:.*<apikey>\([^<]*\)</apikey>.*:\1:p' '$sender_config/config.xml'" | tr -d '\r')"
[ -n "$receiver_api_key" ] && [ -n "$sender_api_key" ] || { echo "missing fixture API key" >&2; exit 1; }

"${ADB[@]}" shell "('$receiver_binary' -C '$sender_config' -D '$sender_data' serve --no-browser --no-restart --no-upgrade --no-port-probing --gui-address=unix://'$sender_gui' --log-file=-) </dev/null >'$REMOTE_DIR/logs/sender.log' 2>&1 & echo \$! >'$REMOTE_DIR/sender.pid'"
SENDER_PID="$("${ADB[@]}" shell "cat '$REMOTE_DIR/sender.pid'" | tr -d '\r')"
wait_remote "test -S '$sender_gui'"

receiver_cli="'$receiver_binary' -C '$userdata/Syncthing/config' -D '$userdata/Syncthing/data' cli --gui-address='$receiver_gui' --gui-apikey='$receiver_api_key' config"
sender_client="'$receiver_binary' -C '$sender_config' -D '$sender_data' cli --gui-address='$sender_gui' --gui-apikey='$sender_api_key'"
sender_cli="$sender_client config"
"${ADB[@]}" shell "$receiver_cli options raw-listen-addresses add tcp://127.0.0.1:23001 >/dev/null; \
    $receiver_cli devices add --device-id='$sender_id' --name=b0b-sender --addresses=tcp://127.0.0.1:23000 >/dev/null; \
    $receiver_cli folders add --id='$folder_id' --label='B0b Saves' --path='$PRIMARY_ROOT/Saves' --type=receiveonly --paused --marker-name='$marker' >/dev/null; \
    $receiver_cli folders '$folder_id' devices add --device-id='$sender_id' >/dev/null"
"${ADB[@]}" shell "$sender_cli options raw-listen-addresses add tcp://127.0.0.1:23000 >/dev/null; \
    $sender_cli options max-send-kbps set 1024 >/dev/null; \
    $sender_cli options limit-bandwidth-in-lan set true >/dev/null; \
    $sender_cli devices add --device-id='$receiver_id' --name=b0b-receiver --addresses=tcp://127.0.0.1:23001 >/dev/null; \
    $sender_cli folders add --id='$folder_id' --label='B0b Saves' --path='$SECONDARY_ROOT/Sender' --type=sendonly --marker-name=.stfolder >/dev/null; \
    $sender_cli folders '$folder_id' devices add --device-id='$receiver_id' >/dev/null"

"${ADB[@]}" shell "$sender_client operations shutdown >/dev/null 2>&1 || kill '$SENDER_PID' 2>/dev/null || true"
wait_remote "! ps -eo args | grep -F -- '-C $sender_config' | grep -v grep" 300
SENDER_PID=""
response="$(request '{"v":1,"op":"stop","id":"configured","service_id":"org.umrk.syncthing"}')"
grep -F '"ok":true' <<<"$response" >/dev/null
wait_remote "test ! -e '$control'" 600

response="$(request '{"v":1,"op":"run","id":"measured","service_id":"org.umrk.syncthing"}')"
grep -F '"ok":true' <<<"$response" >/dev/null
wait_remote "test -S '$receiver_gui'" 600
"${ADB[@]}" shell "('$receiver_binary' -C '$sender_config' -D '$sender_data' serve --no-browser --no-restart --no-upgrade --no-port-probing --gui-address=unix://'$sender_gui' --log-file=-) </dev/null >>'$REMOTE_DIR/logs/sender.log' 2>&1 & echo \$! >'$REMOTE_DIR/sender.pid'; dd if=/dev/zero of='$SECONDARY_ROOT/Sender/active.bin' bs=1M count='$TRANSFER_MIB' conv=fsync >/dev/null 2>&1"
SENDER_PID="$("${ADB[@]}" shell "cat '$REMOTE_DIR/sender.pid'" | tr -d '\r')"
wait_remote "test -S '$sender_gui'"
"${ADB[@]}" shell "$receiver_cli folders '$folder_id' paused set false >/dev/null"
wait_remote "find '$PRIMARY_ROOT/Saves' -name '.syncthing.active.bin.tmp' -type f | grep -q ." 1200

"${ADB[@]}" shell "rm -f '$REMOTE_DIR/runtime/game-writer-live' '$REMOTE_DIR/runtime/game-writer-done' '$REMOTE_DIR/launch.json' '$REMOTE_DIR/launch.response'; \
    ('$REMOTE_DIR/bin/jawaka-platformctl' --socket '$REMOTE_DIR/runtime/jawakad.sock' request '{\"type\":\"launch-game\",\"system\":\"N64\",\"rom_path\":\"Roms/N64/Barrier.n64\"}' >'$REMOTE_DIR/launch.response') & \
    for i in \$(seq 1 500); do if test -f '$REMOTE_DIR/runtime/active-game.json'; then cp '$REMOTE_DIR/runtime/active-game.json' '$REMOTE_DIR/launch.json'; break; fi; sleep 0.002; done; test -f '$REMOTE_DIR/launch.json'"
wait_remote "test -f '$REMOTE_DIR/runtime/game-writer-live'" 600
writer_pid="$("${ADB[@]}" shell "ps -eo pid,comm,args | awk -v path='$PRIMARY_ROOT/.system/leaf/platforms/mlp1/emulators/fixture/game-writer-fixture' '\$2==\"game-writer-fix\" && index(\$0,path){print \$1; exit}'" | tr -d '\r')"
"${ADB[@]}" shell "test ! -e '$receiver_gui' && test ! -e '$control'; kill -STOP '$writer_pid'; $sender_client operations shutdown >/dev/null 2>&1 || kill '$SENDER_PID' 2>/dev/null || true"
wait_remote "! ps -eo args | grep -F -- '-C $sender_config' | grep -v grep" 300
SENDER_PID=""
active_run="$(request '{"v":1,"op":"run","id":"active-game-run","service_id":"org.umrk.syncthing"}' || true)"
grep -F 'lifecycle-in-progress' <<<"$active_run" >/dev/null
"${ADB[@]}" shell "! ps -eo args | grep -F '$receiver_binary' | grep -v grep"
snapshot="find '$PRIMARY_ROOT/Saves' -type f -maxdepth 1 -exec stat -c '%n %s %b %y' {} \; | sort | sha256sum"
before="$("${ADB[@]}" shell "$snapshot" | tr -d '\r')"
sleep 0.5
after="$("${ADB[@]}" shell "$snapshot" | tr -d '\r')"
[ "$before" = "$after" ] || { echo "receiver wrote after ready: $before != $after" >&2; exit 1; }
"${ADB[@]}" shell "kill -CONT '$writer_pid'"

launch_id="$("${ADB[@]}" shell "sed -n 's/.*\"launch_id\":\"\([^\"]*\)\".*/\1/p' '$REMOTE_DIR/launch.json'" | tr -d '\r')"
launch_seconds=${launch_id%%-*}
launch_rest=${launch_id#*-}
launch_nanos=${launch_rest%%-*}
writer_ns="$("${ADB[@]}" shell "date -d \"\$(stat -c %y '$REMOTE_DIR/runtime/game-writer-live')\" +%s%N" | tr -d '\r')"
start_ns=$((10#$launch_seconds * 1000000000 + 10#$launch_nanos))
launch_to_writer_ms=$(((10#$writer_ns - start_ns) / 1000000))
[ "$launch_to_writer_ms" -le 10000 ] || { echo "stop path exceeded the 10 s grace window: ${launch_to_writer_ms}ms" >&2; exit 1; }

wait_remote "test -f '$REMOTE_DIR/runtime/game-writer-done' && test ! -e '$REMOTE_DIR/runtime/active-game.json'" 600
done_ns="$("${ADB[@]}" shell "date -d \"\$(stat -c %y '$REMOTE_DIR/runtime/game-writer-done')\" +%s%N" | tr -d '\r')"
wait_remote "test -S '$control'" 600
control_ns="$("${ADB[@]}" shell "date -d \"\$(stat -c %y '$control')\" +%s%N" | tr -d '\r')"
restart_to_ready_ms=$(((10#$control_ns - 10#$done_ns) / 1000000))
"${ADB[@]}" shell "('$receiver_binary' -C '$sender_config' -D '$sender_data' serve --no-browser --no-restart --no-upgrade --no-port-probing --gui-address=unix://'$sender_gui' --log-file=-) </dev/null >>'$REMOTE_DIR/logs/sender.log' 2>&1 & echo \$! >'$REMOTE_DIR/sender.pid'"
SENDER_PID="$("${ADB[@]}" shell "cat '$REMOTE_DIR/sender.pid'" | tr -d '\r')"
wait_remote "test -S '$sender_gui'"
"${ADB[@]}" shell "$receiver_cli folders '$folder_id' paused set false >/dev/null"
wait_remote "test -f '$PRIMARY_ROOT/Saves/active.bin' && test \"\$(stat -c %s '$PRIMARY_ROOT/Saves/active.bin')\" -eq '$TRANSFER_BYTES'" 2400
measure_completion online
"${ADB[@]}" shell "$sender_client operations shutdown >/dev/null 2>&1 || kill '$SENDER_PID' 2>/dev/null || true"
wait_remote "! ps -eo args | grep -F -- '-C $sender_config' | grep -v grep" 300
SENDER_PID=""
measure_completion offline
if [ "${B0B_SKIP_SCANS:-0}" != 1 ]; then
    measure_cold_scan 5000
    measure_cold_scan 25000
fi
verify_restart_races
echo "B0B_STOP_RESULT launch_to_writer_upper_bound_ms=$launch_to_writer_ms restart_to_ready_ms=$restart_to_ready_ms quiescent_snapshot=$before"
echo "PASS MLP1 B0b stop barrier and scan timing (real LIFE-1 controller, active inbound FAT transfer, writes resume after verified restart)"
