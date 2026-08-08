#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKSPACE_ROOT="$(cd "$ROOT_DIR/.." && pwd)"
JAWAKA_DIR="${JAWAKA_DIR:-$WORKSPACE_ROOT/Jawaka}"
REMOTE_DIR="${LEAF_SYNCTHING_B1_REMOTE_DIR:-/tmp/leaf-syncthing-b1-smoke}"
BUNDLE_DIR="$ROOT_DIR/build/mlp1-b1-smoke/bundle"
SERVICE_ID="org.umrk.syncthing"
MEASURE_SECONDS="${B1_MEASURE_SECONDS:-0}"
LIVE_PANGU_PID=""
TEST_DAEMON_PID=""
FOREIGN_SYNCTHING_PID=""
WORKLOAD_WRITER_PID=""
REMOTE_CARD_MOUNTED=0

if [[ ! "$REMOTE_DIR" =~ ^/tmp/leaf-syncthing-b1-[A-Za-z0-9._/-]+$ ]] ||
   [[ "$REMOTE_DIR" == *".."* ]]; then
    echo "unsafe remote fixture root: $REMOTE_DIR" >&2
    exit 1
fi

if [ -n "${ADB_SERIAL:-}" ]; then
    ADB=(adb -s "$ADB_SERIAL")
else
    serial="$(adb devices | awk 'NR>1 && $2=="device" {print $1; exit}')"
    if [ -z "${serial:-}" ]; then
        echo "No online adb device found." >&2
        exit 1
    fi
    ADB=(adb -s "$serial")
fi

echo "Using adb device: $("${ADB[@]}" get-serialno)"

make -C "$ROOT_DIR" controller-mlp1
if [ ! -x "$ROOT_DIR/build/mlp1/package/Syncthing.pak/bin/syncthing" ]; then
    make -C "$ROOT_DIR" package-mlp1
fi
for binary in \
    "$ROOT_DIR/build/mlp1/bin/leaf-syncthing" \
    "$ROOT_DIR/build/mlp1/package/Syncthing.pak/bin/syncthing" \
    "$JAWAKA_DIR/build/mlp1/bin/jawakad" \
    "$JAWAKA_DIR/build/mlp1/bin/jawaka-platformctl"; do
    if [ ! -x "$binary" ]; then
        echo "missing MLP1 binary: $binary" >&2
        exit 1
    fi
done

rm -rf "$BUNDLE_DIR"
mkdir -p "$BUNDLE_DIR/bin" \
    "$BUNDLE_DIR/sd/Apps/mlp1/Syncthing.pak/bin" \
    "$BUNDLE_DIR/sd/.system/leaf/platforms/mlp1/defaults" \
    "$BUNDLE_DIR/sd/Roms" "$BUNDLE_DIR/sd/Images" \
    "$BUNDLE_DIR/sd/BIOS" "$BUNDLE_DIR/sd/Saves" "$BUNDLE_DIR/sd/States" \
    "$BUNDLE_DIR/sd/Music" "$BUNDLE_DIR/sd/Videos" "$BUNDLE_DIR/sd/Cheats"
cp -f "$JAWAKA_DIR/build/mlp1/bin/jawakad" \
    "$JAWAKA_DIR/build/mlp1/bin/jawaka-platformctl" "$BUNDLE_DIR/bin/"
cp -f "$ROOT_DIR/build/mlp1/bin/leaf-syncthing" \
    "$ROOT_DIR/build/mlp1/package/Syncthing.pak/bin/syncthing" \
    "$BUNDLE_DIR/sd/Apps/mlp1/Syncthing.pak/bin/"
printf '%s\n' \
    '{"id":"org.umrk.syncthing","name":"Syncthing B1 Device Fixture","platform":"mlp1","pak_version":"0.0.0-b1","service":{"schema":1,"id":"org.umrk.syncthing","run":{"path":"bin/leaf-syncthing","args":["service","run"]},"restart":"on-failure","default_enabled":false,"stop_grace_ms":10000,"lifecycle":{"game":"notify","stop_on_storage_change":true,"stop_on_suspend":true}},"state":{"root":"Syncthing","revoke_on_uninstall":["leaf/trusted-clients.json"],"retained_roots":["Syncthing"]}}' \
    >"$BUNDLE_DIR/sd/Apps/mlp1/Syncthing.pak/pak.json"
printf '%s\n' '{"version":2,"platform":"mlp1","systems":[]}' \
    >"$BUNDLE_DIR/sd/.system/leaf/platforms/mlp1/defaults/systems.json"

cleanup_test_daemon() {
    if [ -n "${TEST_DAEMON_PID:-}" ]; then
        "${ADB[@]}" shell "kill '$TEST_DAEMON_PID' 2>/dev/null || true"
        for _ in $(seq 1 100); do
            if ! "${ADB[@]}" shell "kill -0 '$TEST_DAEMON_PID' 2>/dev/null"; then
                break
            fi
            sleep 0.05
        done
        "${ADB[@]}" shell "kill -KILL '$TEST_DAEMON_PID' 2>/dev/null || true"
        TEST_DAEMON_PID=""
    fi
}

cleanup_foreign_syncthing() {
    if [ -n "${FOREIGN_SYNCTHING_PID:-}" ]; then
        "${ADB[@]}" shell "kill '$FOREIGN_SYNCTHING_PID' 2>/dev/null || true"
    fi
    for _ in $(seq 1 100); do
        if ! "${ADB[@]}" shell "ps -eo args | grep -F '$REMOTE_DIR/foreign/' | grep -v grep >/dev/null"; then
            FOREIGN_SYNCTHING_PID=""
            return 0
        fi
        sleep 0.05
    done
    "${ADB[@]}" shell "ps -eo pid,args | awk -v needle='$REMOTE_DIR/foreign/' 'index(\$0, needle) { print \$1 }' | xargs -r kill -KILL" 2>/dev/null || true
    FOREIGN_SYNCTHING_PID=""
}

cleanup_workload_writer() {
    if [ -n "${WORKLOAD_WRITER_PID:-}" ]; then
        "${ADB[@]}" shell "kill '$WORKLOAD_WRITER_PID' 2>/dev/null || true"
        WORKLOAD_WRITER_PID=""
    fi
}

cleanup() {
    status=$?
    set +e
    cleanup_workload_writer
    cleanup_foreign_syncthing
    cleanup_test_daemon
    if [ "$status" -ne 0 ]; then
        echo "B1 controller logs after failure:" >&2
        "${ADB[@]}" shell "tail -160 '$REMOTE_DIR/logs/jawakad.log' 2>/dev/null || true" >&2
        "${ADB[@]}" shell "find '$REMOTE_DIR/logs/services' -type f -maxdepth 3 -exec sh -c 'echo --- \"\$1\"; tail -120 \"\$1\"' sh {} \; 2>/dev/null || true" >&2
    fi
    if [ -n "${LIVE_PANGU_PID:-}" ]; then
        echo "Resuming live loong_pangu pid $LIVE_PANGU_PID"
        "${ADB[@]}" shell "kill -CONT '$LIVE_PANGU_PID' 2>/dev/null || true" >/dev/null
    fi
    if [ "$REMOTE_CARD_MOUNTED" -eq 1 ]; then
        "${ADB[@]}" shell "umount '$REMOTE_DIR/sd' 2>/dev/null || true" >/dev/null
    fi
    "${ADB[@]}" shell "rm -rf '$REMOTE_DIR'" >/dev/null 2>&1 || true
    rm -rf "$BUNDLE_DIR"
    exit "$status"
}
trap cleanup EXIT

echo "Deploying isolated B1 bundle to $REMOTE_DIR"
"${ADB[@]}" shell "umount '$REMOTE_DIR/sd' 2>/dev/null || true; rm -rf '$REMOTE_DIR'; mkdir -p '$REMOTE_DIR'"
"${ADB[@]}" push "$BUNDLE_DIR/." "$REMOTE_DIR/" >/dev/null
"${ADB[@]}" shell "mv '$REMOTE_DIR/sd' '$REMOTE_DIR/sd-source'; mkdir -p '$REMOTE_DIR/sd'; mount --bind '$REMOTE_DIR/sd-source' '$REMOTE_DIR/sd'; chmod 755 '$REMOTE_DIR/bin/'* '$REMOTE_DIR/sd/Apps/mlp1/Syncthing.pak/bin/'*"
REMOTE_CARD_MOUNTED=1

LIVE_PANGU_PID="$("${ADB[@]}" shell 'pidof loong_pangu 2>/dev/null || true' | tr -d '\r' | awk '{print $1}')"
if [ -n "$LIVE_PANGU_PID" ]; then
    echo "Pausing live loong_pangu pid $LIVE_PANGU_PID"
    "${ADB[@]}" shell "kill -STOP '$LIVE_PANGU_PID'"
fi

runtime="$REMOTE_DIR/runtime"
userdata="$REMOTE_DIR/sd/.userdata/mlp1"
shared_userdata="$REMOTE_DIR/sd/.userdata/shared"
state="$REMOTE_DIR/state"
logs="$REMOTE_DIR/logs"
socket="$runtime/jawakad.sock"
"${ADB[@]}" shell "mkdir -p '$runtime' '$userdata' '$shared_userdata' '$state' '$logs'"

start_test_daemon() {
    "${ADB[@]}" shell "rm -f '$socket'; (cd '$REMOTE_DIR' && exec env \
        UMRK_ENV_VERSION=2 PLATFORM=mlp1 \
        SDCARD_PATH='$REMOTE_DIR/sd' SDCARD_PATHS='$REMOTE_DIR/sd' \
        ROMS_PATH='$REMOTE_DIR/sd/Roms' ROMS_PATHS='$REMOTE_DIR/sd/Roms' \
        IMAGES_PATH='$REMOTE_DIR/sd/Images' IMAGES_PATHS='$REMOTE_DIR/sd/Images' \
        APPS_PATH='$REMOTE_DIR/sd/Apps' APPS_PATHS='$REMOTE_DIR/sd/Apps' \
        USERDATA_PATH='$userdata' USERDATA_PATHS='$userdata' \
        SHARED_USERDATA_PATH='$shared_userdata' SHARED_USERDATA_PATHS='$shared_userdata' \
        LOGS_PATH='$logs' MUSIC_PATH='$REMOTE_DIR/sd/Music' MUSIC_PATHS='$REMOTE_DIR/sd/Music' \
        VIDEO_PATH='$REMOTE_DIR/sd/Videos' VIDEO_PATHS='$REMOTE_DIR/sd/Videos' \
        BIOS_PATH='$REMOTE_DIR/sd/BIOS' BIOS_PATHS='$REMOTE_DIR/sd/BIOS' \
        SAVES_PATH='$REMOTE_DIR/sd/Saves' SAVES_PATHS='$REMOTE_DIR/sd/Saves' \
        STATES_PATH='$REMOTE_DIR/sd/States' STATES_PATHS='$REMOTE_DIR/sd/States' \
        CHEATS_PATH='$REMOTE_DIR/sd/Cheats' CHEATS_PATHS='$REMOTE_DIR/sd/Cheats' \
        UMRK_PLATFORM_PATH='$REMOTE_DIR/sd/.system/leaf/platforms/mlp1' \
        UMRK_RUNTIME_PATH='$runtime' JAWAKA_RUNTIME_DIR='$runtime' UMRK_DAEMON_SOCKET='$socket' \
        UMRK_INTERNAL_DATA_PATH='$state' JAWAKA_SDCARD_ROOT='$REMOTE_DIR/sd' \
        '$REMOTE_DIR/bin/jawakad' --daemon-only) </dev/null >>'$logs/jawakad.log' 2>&1 & echo \$! >'$REMOTE_DIR/daemon.pid'"
    TEST_DAEMON_PID="$("${ADB[@]}" shell "cat '$REMOTE_DIR/daemon.pid'" | tr -d '\r')"
}

start_test_daemon

wait_remote() {
    local command="$1"
    local attempts="${2:-300}"
    for _ in $(seq 1 "$attempts"); do
        if "${ADB[@]}" shell "$command" >/dev/null 2>&1; then
            return 0
        fi
        if ! "${ADB[@]}" shell "kill -0 '$TEST_DAEMON_PID' 2>/dev/null"; then
            echo "test daemon exited while waiting for: $command" >&2
            return 1
        fi
        sleep 0.05
    done
    echo "timed out waiting for: $command" >&2
    return 1
}

request() {
    "${ADB[@]}" shell "'$REMOTE_DIR/bin/jawaka-platformctl' --socket '$socket' request '$1'" | tr -d '\r'
}

wait_remote "test -S '$socket'"
wait_remote "'$REMOTE_DIR/bin/jawaka-platformctl' --socket '$socket' request '{\"v\":1,\"op\":\"list\",\"id\":\"discover\"}' | grep -F '$SERVICE_ID'" 400

run_and_wait() {
    local run_id="$1"
    local response
    local control_socket="$runtime/services/$SERVICE_ID/control.sock"
    local control_response
    response="$(request "{\"v\":1,\"op\":\"run\",\"id\":\"$run_id\",\"service_id\":\"$SERVICE_ID\"}")"
    grep -F '"ok":true' <<<"$response" >/dev/null
    wait_remote "'$REMOTE_DIR/bin/jawaka-platformctl' --socket '$socket' request '{\"v\":1,\"op\":\"status\",\"id\":\"status\",\"service_id\":\"$SERVICE_ID\"}' | grep -F '\"effective_state\":\"running\"' | grep -F '\"coordination\":\"subscribed\"'" 500
    wait_remote "test -S '$runtime/services/$SERVICE_ID/syncthing-gui.sock'"
    "${ADB[@]}" shell "test \"\$(stat -c %a '$runtime/services/$SERVICE_ID/syncthing-gui.sock')\" = 600"
    wait_remote "test -S '$control_socket'"
    "${ADB[@]}" shell "test \"\$(stat -c %a '$control_socket')\" = 600"
    control_response="$("${ADB[@]}" shell "'$REMOTE_DIR/bin/jawaka-platformctl' --socket '$control_socket' request '{\"v\":1,\"id\":\"device-smoke\",\"op\":\"status.get\",\"args\":{}}'" | tr -d '\r')"
    grep -F '"ok":true' <<<"$control_response" >/dev/null
    grep -F '"controller":"running"' <<<"$control_response" >/dev/null
    grep -F '"state":"running"' <<<"$control_response" >/dev/null
    grep -F '"version":"v2.1.2"' <<<"$control_response" >/dev/null
    grep -F '"capabilities":["status.get","card.enroll"]' <<<"$control_response" >/dev/null
    control_response="$("${ADB[@]}" shell "'$REMOTE_DIR/bin/jawaka-platformctl' --socket '$control_socket' request '{\"v\":1,\"id\":\"enroll-card\",\"op\":\"card.enroll\",\"args\":{\"source_id\":\"primary\"}}'" | tr -d '\r')"
    grep -F '"ok":true' <<<"$control_response" >/dev/null
    grep -F '"state":"enrolled"' <<<"$control_response" >/dev/null
    grep -F '"slot":"Primary"' <<<"$control_response" >/dev/null
    "${ADB[@]}" shell "awk '\$2 ~ /:20C0\$/ && \$4 == \"0A\" { found=1 } END { exit found ? 1 : 0 }' /proc/net/tcp /proc/net/tcp6"
    "${ADB[@]}" shell "ps -eo args | grep -F '$REMOTE_DIR/sd/Apps/mlp1/Syncthing.pak/bin/leaf-syncthing service run' | grep -v grep >/dev/null"
    "${ADB[@]}" shell "test \"\$(ps -eo args | grep -F '$REMOTE_DIR/sd/Apps/mlp1/Syncthing.pak/bin/syncthing' | grep -v grep | wc -l)\" -eq 2"
    "${ADB[@]}" shell "controller=\$(ps -eo pid,comm,args | awk -v needle='$REMOTE_DIR/sd/Apps/mlp1/Syncthing.pak/bin/leaf-syncthing service run' '\$2 == \"leaf-syncthing\" && index(\$0, needle) { print \$1; exit }'); test \"\$(ps -eo pgid,comm,args | awk -v needle='$REMOTE_DIR/sd/Apps/mlp1/Syncthing.pak/bin/' -v group=\"\$controller\" '(\$2 == \"leaf-syncthing\" || \$2 == \"syncthing\") && index(\$0, needle) && \$1 == group { count++ } END { print count+0 }')\" -eq 3"
}

stop_and_wait() {
    local stop_id="$1"
    local response
    response="$(request "{\"v\":1,\"op\":\"stop\",\"id\":\"$stop_id\",\"service_id\":\"$SERVICE_ID\"}")"
    grep -F '"ok":true' <<<"$response" >/dev/null
    wait_remote "'$REMOTE_DIR/bin/jawaka-platformctl' --socket '$socket' request '{\"v\":1,\"op\":\"status\",\"id\":\"stopped\",\"service_id\":\"$SERVICE_ID\"}' | grep -E '\"effective_state\":\"(stopped|disabled)\"'" 500
    wait_remote "! ps -eo args | grep -F '$REMOTE_DIR/sd/Apps/mlp1/Syncthing.pak/bin/' | grep -v grep" 500
    wait_remote "test ! -e '$runtime/services/$SERVICE_ID/control.sock'" 500
}

generation_pids() {
    "${ADB[@]}" shell "set -eu
        controller=\$(ps -eo pid,ppid,comm,args | awk -v needle='$REMOTE_DIR/sd/Apps/mlp1/Syncthing.pak/bin/leaf-syncthing service run' '\$3 == \"leaf-syncthing\" && index(\$0, needle) { print \$1; exit }')
        monitor=\$(ps -eo pid,ppid,comm,args | awk -v parent=\"\$controller\" -v needle='$REMOTE_DIR/sd/Apps/mlp1/Syncthing.pak/bin/syncthing' '\$2 == parent && \$3 == \"syncthing\" && index(\$0, needle) { print \$1; exit }')
        main=\$(ps -eo pid,ppid,comm,args | awk -v parent=\"\$monitor\" -v needle='$REMOTE_DIR/sd/Apps/mlp1/Syncthing.pak/bin/syncthing' '\$2 == parent && \$3 == \"syncthing\" && index(\$0, needle) { print \$1; exit }')
        test -n \"\$controller\" -a -n \"\$monitor\" -a -n \"\$main\"
        test \"\$(awk '{ print \$5 }' /proc/\$controller/stat)\" = \"\$controller\"
        test \"\$(awk '{ print \$5 }' /proc/\$monitor/stat)\" = \"\$controller\"
        test \"\$(awk '{ print \$5 }' /proc/\$main/stat)\" = \"\$controller\"
        echo \"\$controller \$monitor \$main\"" | tr -d '\r'
}

wait_generation_absent_without_overlap() {
    local old_controller="$1" old_monitor="$2" old_main="$3"
    local observation
    for _ in $(seq 1 800); do
        observation="$("${ADB[@]}" shell "old=0; for process in '$old_controller' '$old_monitor' '$old_main'; do kill -0 \"\$process\" 2>/dev/null && old=1; done; new=\$(ps -eo pid,comm,args | awk -v old='$old_controller' -v needle='$REMOTE_DIR/sd/Apps/mlp1/Syncthing.pak/bin/leaf-syncthing service run' '\$1 != old && \$2 == \"leaf-syncthing\" && index(\$0, needle) { count++ } END { print count+0 }'); echo \"\$old \$new\"" | tr -d '\r')"
        if [ "$observation" = "0 0" ] || [ "$observation" = "0 1" ]; then
            return 0
        fi
        if [ "$observation" != "1 0" ]; then
            echo "replacement controller overlapped old generation: $observation" >&2
            return 1
        fi
        sleep 0.05
    done
    echo "old generation remained after guardian deadline" >&2
    return 1
}

wait_replacement_generation() {
    local old_controller="$1"
    wait_remote "controller=\$(ps -eo pid,comm,args | awk -v old='$old_controller' -v needle='$REMOTE_DIR/sd/Apps/mlp1/Syncthing.pak/bin/leaf-syncthing service run' '\$1 != old && \$2 == \"leaf-syncthing\" && index(\$0, needle) { print \$1; exit }'); test -n \"\$controller\"; test \"\$(ps -eo pgid,comm,args | awk -v needle='$REMOTE_DIR/sd/Apps/mlp1/Syncthing.pak/bin/' -v group=\"\$controller\" '(\$2 == \"leaf-syncthing\" || \$2 == \"syncthing\") && index(\$0, needle) && \$1 == group { count++ } END { print count+0 }')\" -eq 3" 800
    wait_remote "'$REMOTE_DIR/bin/jawaka-platformctl' --socket '$socket' request '{\"v\":1,\"op\":\"status\",\"id\":\"replacement\",\"service_id\":\"$SERVICE_ID\"}' | grep -F '\"effective_state\":\"running\"'" 800
}

prepare_scan_workload() {
    echo "Preparing isolated active-scan workload"
    "${ADB[@]}" shell "set -eu
        rm -rf '$REMOTE_DIR/sd/Saves/.stfolder' '$REMOTE_DIR/sd/Saves/b1-guardian-workload'
        mkdir -p '$REMOTE_DIR/sd/Saves/b1-guardian-workload'
        index=0
        while [ \"\$index\" -lt 2000 ]; do
            printf 'seed-%s\n' \"\$index\" >'$REMOTE_DIR/sd/Saves/b1-guardian-workload/item-'\"\$index\"
            index=\$((index + 1))
        done"
}

activate_scan_workload() {
    local main_pid="$1" start_ticks
    cleanup_workload_writer
    "${ADB[@]}" shell "'$REMOTE_DIR/sd/Apps/mlp1/Syncthing.pak/bin/syncthing' cli -C '$userdata/Syncthing/config' -D '$userdata/Syncthing/data' config folders '$TEST_FOLDER_ID' paused set false >/dev/null"
    "${ADB[@]}" shell "(index=0; while [ \"\$index\" -lt 50000 ]; do slot=\$((index % 2000)); printf 'change-%s\n' \"\$index\" >'$REMOTE_DIR/sd/Saves/b1-guardian-workload/item-'\"\$slot\"; index=\$((index + 1)); done) </dev/null >/dev/null 2>&1 & echo \$! >'$REMOTE_DIR/workload-writer.pid'"
    WORKLOAD_WRITER_PID="$("${ADB[@]}" shell "cat '$REMOTE_DIR/workload-writer.pid'" | tr -d '\r')"
    start_ticks="$("${ADB[@]}" shell "awk '{ print \$14 + \$15 }' '/proc/$main_pid/stat'" | tr -d '\r')"
    wait_remote "test \$((\$(awk '{ print \$14 + \$15 }' '/proc/$main_pid/stat') - $start_ticks)) -ge 3" 400
}

verify_controller_death_cleanup() {
    local old_controller old_monitor old_main replacement
    echo "Exercising controller death under live Jawaka"
    run_and_wait controller-death-run
    read -r old_controller old_monitor old_main <<<"$(generation_pids)"
    activate_scan_workload "$old_main"
    "${ADB[@]}" shell "kill -KILL '$old_controller'"
    cleanup_workload_writer
    wait_generation_absent_without_overlap "$old_controller" "$old_monitor" "$old_main"
    wait_replacement_generation "$old_controller"
    replacement="$(generation_pids)"
    [ "${replacement%% *}" != "$old_controller" ]
    stop_and_wait controller-death-stop
}

verify_monitor_death_cleanup() {
    local old_controller old_monitor old_main replacement
    echo "Exercising direct monitor death under live Jawaka"
    run_and_wait monitor-death-run
    read -r old_controller old_monitor old_main <<<"$(generation_pids)"
    activate_scan_workload "$old_main"
    "${ADB[@]}" shell "kill -KILL '$old_monitor'"
    cleanup_workload_writer
    wait_generation_absent_without_overlap "$old_controller" "$old_monitor" "$old_main"
    wait_replacement_generation "$old_controller"
    replacement="$(generation_pids)"
    [ "${replacement%% *}" != "$old_controller" ]
    stop_and_wait monitor-death-stop
}

verify_daemon_death_guardian() {
    local old_controller old_monitor old_main gui_inode stale_response stale_status disable_response run_response replacement
    echo "Exercising Jawaka death/restart and generation-lease guardian"
    run_and_wait daemon-death-run
    read -r old_controller old_monitor old_main <<<"$(generation_pids)"
    activate_scan_workload "$old_main"
    gui_inode="$("${ADB[@]}" shell "stat -c %i '$runtime/services/$SERVICE_ID/syncthing-gui.sock'" | tr -d '\r')"
    "${ADB[@]}" shell "'$REMOTE_DIR/sd/Apps/mlp1/Syncthing.pak/bin/syncthing' cli -C '$userdata/Syncthing/config' -D '$userdata/Syncthing/data' config gui enabled set false >/dev/null"
    wait_remote "test \"\$(stat -c %i '$runtime/services/$SERVICE_ID/syncthing-gui.sock')\" != '$gui_inode'" 400
    "${ADB[@]}" shell "rm '$runtime/services/$SERVICE_ID/syncthing-gui.sock'; kill -KILL '$TEST_DAEMON_PID'"
    TEST_DAEMON_PID=""
    start_test_daemon
    wait_remote "test -S '$socket'" 400
    wait_remote "'$REMOTE_DIR/bin/jawaka-platformctl' --socket '$socket' request '{\"v\":1,\"op\":\"list\",\"id\":\"rediscover\"}' | grep -F '$SERVICE_ID'" 400
    "${ADB[@]}" shell "test \"\$(readlink '/proc/$old_controller/fd/3')\" = '$runtime/services/$SERVICE_ID/generation.lease'; kill -0 '$old_monitor'; kill -0 '$old_main'"
    stale_response="$(request '{"v":1,"op":"run","id":"stale-generation-run","service_id":"org.umrk.syncthing"}')"
    grep -F 'stale-generation' <<<"$stale_response" >/dev/null
    stale_status="$(request '{"v":1,"op":"status","id":"stale-generation-status","service_id":"org.umrk.syncthing"}')"
    grep -F '"effective_state":"stale-generation"' <<<"$stale_status" >/dev/null
    disable_response="$(request '{"v":1,"op":"disable","id":"clear-stale-run","service_id":"org.umrk.syncthing"}')"
    grep -F '"ok":true' <<<"$disable_response" >/dev/null
    "${ADB[@]}" shell "test \"\$(ps -eo comm,args | awk -v needle='$REMOTE_DIR/sd/Apps/mlp1/Syncthing.pak/bin/leaf-syncthing service run' '\$1 == \"leaf-syncthing\" && index(\$0, needle) { count++ } END { print count+0 }')\" -eq 1"
    cleanup_workload_writer
    wait_generation_absent_without_overlap "$old_controller" "$old_monitor" "$old_main"
    "${ADB[@]}" shell "set -eu
        sed 's/<gui enabled=\"false\"/<gui enabled=\"true\"/' '$userdata/Syncthing/config/config.xml' >'$userdata/Syncthing/config/config.xml.guardian-test'
        grep -q '<gui enabled=\"true\"' '$userdata/Syncthing/config/config.xml.guardian-test'
        mv '$userdata/Syncthing/config/config.xml.guardian-test' '$userdata/Syncthing/config/config.xml'
        sync"
    run_response="$(request '{"v":1,"op":"run","id":"post-guardian-run","service_id":"org.umrk.syncthing"}')"
    grep -F '"ok":true' <<<"$run_response" >/dev/null
    wait_replacement_generation "$old_controller"
    replacement="$(generation_pids)"
    [ "${replacement%% *}" != "$old_controller" ]
    stop_and_wait daemon-death-stop
}

verify_foreign_syncthing_conflict() {
    local response conflict_status foreign_count
    "${ADB[@]}" shell "set -eu
        mkdir -p '$REMOTE_DIR/foreign/config' '$REMOTE_DIR/foreign/data'
        '$REMOTE_DIR/sd/Apps/mlp1/Syncthing.pak/bin/syncthing' generate --config='$REMOTE_DIR/foreign/config' --data='$REMOTE_DIR/foreign/data' --no-port-probing >/dev/null 2>&1
        ('$REMOTE_DIR/sd/Apps/mlp1/Syncthing.pak/bin/syncthing' serve --config='$REMOTE_DIR/foreign/config' --data='$REMOTE_DIR/foreign/data' --no-browser --no-restart --no-upgrade --no-port-probing --gui-address=127.0.0.1:8384 --log-file=- >'$REMOTE_DIR/foreign/syncthing.log' 2>&1 & echo \$! >'$REMOTE_DIR/foreign/pid')"
    FOREIGN_SYNCTHING_PID="$("${ADB[@]}" shell "cat '$REMOTE_DIR/foreign/pid'" | tr -d '\r')"
    wait_remote "test \"\$(ps -eo args | grep -F '$REMOTE_DIR/foreign/config' | grep -v grep | wc -l)\" -eq 2" 500
    foreign_count="$("${ADB[@]}" shell "ps -eo args | grep -F '$REMOTE_DIR/foreign/config' | grep -v grep | wc -l" | tr -d '\r')"

    response="$(request '{"v":1,"op":"run","id":"foreign-conflict-run","service_id":"org.umrk.syncthing"}')"
    grep -F '"ok":true' <<<"$response" >/dev/null
    wait_remote "test -S '$runtime/services/$SERVICE_ID/control.sock'" 500
    conflict_status="$("${ADB[@]}" shell "'$REMOTE_DIR/bin/jawaka-platformctl' --socket '$runtime/services/$SERVICE_ID/control.sock' request '{\"v\":1,\"id\":\"foreign-conflict\",\"op\":\"status.get\",\"args\":{}}'" | tr -d '\r')"
    grep -F '"state":"conflict"' <<<"$conflict_status" >/dev/null
    grep -F '"code":"foreign-syncthing"' <<<"$conflict_status" >/dev/null
    "${ADB[@]}" shell "test \"\$(ps -eo args | grep -F '$REMOTE_DIR/foreign/config' | grep -v grep | wc -l)\" -eq '$foreign_count'"
    response="$(request '{"v":1,"op":"stop","id":"foreign-conflict-stop","service_id":"org.umrk.syncthing"}')"
    grep -F '"ok":true' <<<"$response" >/dev/null
    wait_remote "! ps -eo args | grep -F '$REMOTE_DIR/sd/Apps/mlp1/Syncthing.pak/bin/leaf-syncthing service run' | grep -v grep" 500
    wait_remote "test ! -e '$runtime/services/$SERVICE_ID/control.sock'" 500
    "${ADB[@]}" shell "test \"\$(ps -eo args | grep -F '$REMOTE_DIR/foreign/config' | grep -v grep | wc -l)\" -eq '$foreign_count'"
    cleanup_foreign_syncthing
    wait_remote "! pidof syncthing >/dev/null 2>&1" 500
}

downgrade_config_schema_fixture() {
    local config_dir="$userdata/Syncthing/config"
    local current older
    current="$("${ADB[@]}" shell "sed -n 's/.*<configuration[^>]*version=\"\([0-9][0-9]*\)\".*/\1/p' '$config_dir/config.xml' | head -1" | tr -d '\r')"
    if [[ ! "$current" =~ ^[0-9]+$ ]] || [ "$current" -le 1 ]; then
        echo "could not read a migratable Syncthing config version: $current" >&2
        exit 1
    fi
    older=$((current - 1))
    "${ADB[@]}" shell "set -eu
        sed 's/version=\"$current\"/version=\"$older\"/' '$config_dir/config.xml' >'$config_dir/config.xml.schema-test'
        sed -e 's/\"upstream_version\":\"v2.1.2\"/\"upstream_version\":\"v2.1.1\"/' -e 's/\"config_version\":$current/\"config_version\":$older/' '$config_dir/.leaf-generation-v1' >'$config_dir/.leaf-generation-v1.schema-test'
        grep -q 'version=\"$older\"' '$config_dir/config.xml.schema-test'
        grep -q '\"upstream_version\":\"v2.1.1\"' '$config_dir/.leaf-generation-v1.schema-test'
        grep -q '\"config_version\":$older' '$config_dir/.leaf-generation-v1.schema-test'
        mv '$config_dir/config.xml.schema-test' '$config_dir/config.xml'
        mv '$config_dir/.leaf-generation-v1.schema-test' '$config_dir/.leaf-generation-v1'
        sync"
    MIGRATION_CURRENT_VERSION="$current"
    MIGRATION_OLD_VERSION="$older"
}

add_managed_folder_fixture() {
    local config_dir="$userdata/Syncthing/config"
    local card_id digest
    card_id="$("${ADB[@]}" shell "sed -n 's/.*\"id\":\"\([0-9a-f][0-9a-f]*\)\".*/\1/p' '$userdata/Syncthing/card-id'" | tr -d '\r')"
    if [[ ! "$card_id" =~ ^[0-9a-f]{32}$ ]]; then
        echo "could not read the enrolled fixture card id: $card_id" >&2
        exit 1
    fi
    digest="$(printf '%s' "${card_id}saves" | shasum -a 256 | awk '{print $1}')"
    TEST_FOLDER_ID="leaf-saves-${digest:0:16}"
    TEST_MARKER=".leaf-saves-${digest:0:12}"
    "${ADB[@]}" shell "set -eu
        mkdir -p '$REMOTE_DIR/sd/Saves/$TEST_MARKER'
        sed 's#</configuration>#    <folder id=\"$TEST_FOLDER_ID\" label=\"Leaf Saves\" path=\"$REMOTE_DIR/sd/Saves\" type=\"sendonly\"><paused>false</paused><markerName>$TEST_MARKER</markerName></folder>\n</configuration>#' '$config_dir/config.xml' >'$config_dir/config.xml.folder-test'
        grep -q 'id=\"$TEST_FOLDER_ID\"' '$config_dir/config.xml.folder-test'
        mv '$config_dir/config.xml.folder-test' '$config_dir/config.xml'
        sync"
}

measure_idle() {
    local seconds="$1"
    if ! [[ "$seconds" =~ ^[0-9]+$ ]] || [ "$seconds" -le 0 ]; then
        return 0
    fi
    echo "Measuring idle controller/upstream for ${seconds}s"
    "${ADB[@]}" shell "
        set -eu
        controller_pid=\$(ps -eo pid,args | awk -v needle='$REMOTE_DIR/sd/Apps/mlp1/Syncthing.pak/bin/leaf-syncthing service run' 'index(\$0, needle) { print \$1; exit }')
        upstream_pids=\$(pidof syncthing)
        test -n \"\$controller_pid\"
        test -n \"\$upstream_pids\"
        hz=\$(getconf CLK_TCK 2>/dev/null || echo 100)
        rss_kib() { awk '/^VmRSS:/ { print \$2; exit }' /proc/\$1/status; }
        ticks() { awk '{ print \$14 + \$15 }' /proc/\$1/stat; }
        sum_upstream_rss() {
            total=0
            for pid in \$upstream_pids; do total=\$((total + \$(rss_kib \$pid))); done
            echo \$total
        }
        sum_upstream_ticks() {
            total=0
            for pid in \$upstream_pids; do total=\$((total + \$(ticks \$pid))); done
            echo \$total
        }
        controller_ticks_start=\$(ticks \$controller_pid)
        upstream_ticks_start=\$(sum_upstream_ticks)
        start_uptime=\$(awk '{ print int(\$1) }' /proc/uptime)
        interval=5
        samples=0
        controller_sum=0
        controller_max=0
        upstream_sum=0
        upstream_max=0
        total_max=0
        elapsed=0
        while [ \$elapsed -lt '$seconds' ]; do
            controller_rss=\$(rss_kib \$controller_pid)
            upstream_rss=\$(sum_upstream_rss)
            total_rss=\$((controller_rss + upstream_rss))
            controller_sum=\$((controller_sum + controller_rss))
            upstream_sum=\$((upstream_sum + upstream_rss))
            [ \$controller_rss -le \$controller_max ] || controller_max=\$controller_rss
            [ \$upstream_rss -le \$upstream_max ] || upstream_max=\$upstream_rss
            [ \$total_rss -le \$total_max ] || total_max=\$total_rss
            samples=\$((samples + 1))
            if [ \$((samples % 12)) -eq 0 ]; then
                echo \"B1_IDLE_PROGRESS elapsed_s=\$elapsed total_rss_kib=\$total_rss\"
            fi
            sleep \$interval
            now=\$(awk '{ print int(\$1) }' /proc/uptime)
            elapsed=\$((now - start_uptime))
        done
        end_uptime=\$(awk '{ print int(\$1) }' /proc/uptime)
        elapsed=\$((end_uptime - start_uptime))
        controller_ticks_end=\$(ticks \$controller_pid)
        upstream_ticks_end=\$(sum_upstream_ticks)
        controller_avg=\$((controller_sum / samples))
        upstream_avg=\$((upstream_sum / samples))
        awk -v elapsed=\$elapsed -v hz=\$hz \\
            -v cdelta=\$((controller_ticks_end - controller_ticks_start)) \\
            -v udelta=\$((upstream_ticks_end - upstream_ticks_start)) \\
            -v samples=\$samples -v cavg=\$controller_avg -v cmax=\$controller_max \\
            -v uavg=\$upstream_avg -v umax=\$upstream_max -v tmax=\$total_max \\
            'BEGIN { printf \"B1_IDLE_RESULT elapsed_s=%d samples=%d controller_avg_kib=%d controller_max_kib=%d upstream_avg_kib=%d upstream_max_kib=%d total_max_kib=%d controller_cpu_one_core_pct=%.4f upstream_cpu_one_core_pct=%.4f\\n\", elapsed, samples, cavg, cmax, uavg, umax, tmax, cdelta * 100 / hz / elapsed, udelta * 100 / hz / elapsed }'
    "
}

verify_foreign_syncthing_conflict
run_and_wait first-run
measure_idle "$MEASURE_SECONDS"
first_hashes="$("${ADB[@]}" shell "sha256sum '$userdata/Syncthing/config/cert.pem' '$userdata/Syncthing/config/key.pem' '$userdata/Syncthing/config/.leaf-generation-v1' '$userdata/Syncthing/card-id'" | tr -d '\r')"
stop_and_wait first-stop
add_managed_folder_fixture
downgrade_config_schema_fixture
run_and_wait second-run
second_hashes="$("${ADB[@]}" shell "sha256sum '$userdata/Syncthing/config/cert.pem' '$userdata/Syncthing/config/key.pem' '$userdata/Syncthing/config/.leaf-generation-v1' '$userdata/Syncthing/card-id'" | tr -d '\r')"
if [ "$first_hashes" != "$second_hashes" ]; then
    echo "identity hashes changed across supervised restart" >&2
    exit 1
fi
"${ADB[@]}" shell "set -eu
    grep -q 'version=\"$MIGRATION_CURRENT_VERSION\"' '$userdata/Syncthing/config/config.xml'
    grep -q 'version=\"$MIGRATION_CURRENT_VERSION\"' '$userdata/Syncthing/config/config.xml.bak'
    grep -q '<paused>true</paused>' '$userdata/Syncthing/config/config.xml'
    grep -q '<paused>false</paused>' '$userdata/Syncthing/config/config.xml.bak'
    test ! -e '$userdata/Syncthing/config.migrate.tmp'
    test ! -e '$userdata/Syncthing/data.migrate.tmp'"
folder_status="$("${ADB[@]}" shell "'$REMOTE_DIR/bin/jawaka-platformctl' --socket '$runtime/services/$SERVICE_ID/control.sock' request '{\"v\":1,\"id\":\"folder-status\",\"op\":\"status.get\",\"args\":{}}'" | tr -d '\r')"
grep -F "\"id\":\"$TEST_FOLDER_ID\"" <<<"$folder_status" >/dev/null
grep -F '"state":"paused"' <<<"$folder_status" >/dev/null
grep -F '"pause_reasons":["first-sync"]' <<<"$folder_status" >/dev/null
stop_and_wait second-stop

"${ADB[@]}" shell "mkdir '$REMOTE_DIR/sd/Saves/.stfolder'"
run_and_wait foreign-marker-run
folder_status="$("${ADB[@]}" shell "'$REMOTE_DIR/bin/jawaka-platformctl' --socket '$runtime/services/$SERVICE_ID/control.sock' request '{\"v\":1,\"id\":\"foreign-marker-status\",\"op\":\"status.get\",\"args\":{}}'" | tr -d '\r')"
grep -F '"code":"foreign-folder-manager"' <<<"$folder_status" >/dev/null
grep -F '"state":"error"' <<<"$folder_status" >/dev/null
stop_and_wait foreign-marker-stop

prepare_scan_workload
verify_controller_death_cleanup
verify_monitor_death_cleanup
verify_daemon_death_guardian

echo "PASS MLP1 B1 controller smoke (SVC-1/LIFE-1, private API, stable identity, disposable config migration, managed-folder pause/marker reporting, transitive guardian/restart safety)"
