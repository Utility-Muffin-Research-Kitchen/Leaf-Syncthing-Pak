#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
JAWAKA_DIR="${JAWAKA_DIR:-$ROOT_DIR/../Jawaka}"
REMOTE_DIR="${LEAF_SYNCTHING_B3_REMOTE_DIR:-/tmp/leaf-syncthing-b3}"
PRIMARY_CARD_ROOT="${LEAF_SYNCTHING_B3_PRIMARY_ROOT:-/mnt/sdcard/.leaf-syncthing-b3}"
SECONDARY_CARD_ROOT="${LEAF_SYNCTHING_B3_SECONDARY_ROOT:-/media/sdcard1/.leaf-syncthing-b3}"
PRIMARY_ROOT="$REMOTE_DIR/primary"
SECONDARY_ROOT="$REMOTE_DIR/secondary"
SERVICE_ID=org.umrk.syncthing
DAEMON_PID=""
LIVE_PANGU_PID=""

case "$REMOTE_DIR:$PRIMARY_CARD_ROOT:$SECONDARY_CARD_ROOT" in
    /tmp/leaf-syncthing-b3:/mnt/sdcard/.leaf-syncthing-b3:/media/sdcard1/.leaf-syncthing-b3) ;;
    *) echo "refusing unsafe B3 fixture roots" >&2; exit 1 ;;
esac

if [[ -n "${ADB_SERIAL:-}" ]]; then
    ADB=(adb -s "$ADB_SERIAL")
else
    serial="$(adb devices | awk 'NR > 1 && $2 == "device" { print $1; exit }')"
    [[ -n "$serial" ]] || { echo "No online adb device found." >&2; exit 1; }
    ADB=(adb -s "$serial")
fi

make -C "$ROOT_DIR" package-mlp1 >/dev/null
for binary in jawakad jawaka-platformctl game-writer-fixture; do
    [[ -x "$JAWAKA_DIR/build/mlp1/bin/$binary" ]] || {
        echo "missing MLP1 Jawaka binary: $binary" >&2
        exit 1
    }
done

cleanup() {
    status=$?
    set +e
    [[ -z "$DAEMON_PID" ]] || "${ADB[@]}" shell "kill '$DAEMON_PID' 2>/dev/null || true" >/dev/null
    if [[ "$status" -ne 0 ]]; then
        echo "B3 device logs after failure:" >&2
        "${ADB[@]}" shell "tail -180 '$REMOTE_DIR/logs/jawakad.log' 2>/dev/null || true; find '$REMOTE_DIR/logs/services' -maxdepth 3 -type f -exec tail -120 {} \; 2>/dev/null || true" >&2
    fi
    "${ADB[@]}" shell "ps -eo pid,comm,args | awk -v root='$REMOTE_DIR' -v a='$PRIMARY_CARD_ROOT' -v b='$SECONDARY_CARD_ROOT' '(\$2==\"jawakad\"||\$2==\"syncthing\"||\$2==\"leaf-syncthing\"||\$2==\"game-writer-fix\")&&(index(\$0,root)||index(\$0,a)||index(\$0,b)){print \$1}' | xargs -r kill -KILL" >/dev/null 2>&1 || true
    "${ADB[@]}" shell "umount '$PRIMARY_ROOT' 2>/dev/null || true; umount '$SECONDARY_ROOT' 2>/dev/null || true; rm -rf '$REMOTE_DIR' '$PRIMARY_CARD_ROOT' '$SECONDARY_CARD_ROOT'" >/dev/null 2>&1 || true
    if [[ -n "$LIVE_PANGU_PID" ]]; then
        "${ADB[@]}" shell "kill -CONT '$LIVE_PANGU_PID' 2>/dev/null || true" >/dev/null
    fi
    exit "$status"
}
trap cleanup EXIT

wait_remote() {
    local command=$1
    local attempts=${2:-600}
    for _ in $(seq 1 "$attempts"); do
        "${ADB[@]}" shell "$command" >/dev/null 2>&1 && return 0
        [[ -z "$DAEMON_PID" ]] || "${ADB[@]}" shell "kill -0 '$DAEMON_PID' 2>/dev/null" >/dev/null 2>&1 || return 1
        sleep 0.05
    done
    echo "timed out waiting for: $command" >&2
    return 1
}

request_at() {
    local socket=$1
    local payload=$2
    "${ADB[@]}" shell "'$REMOTE_DIR/bin/jawaka-platformctl' --socket '$socket' request '$payload'" | tr -d '\r'
}

daemon_request() {
    request_at "$REMOTE_DIR/runtime/jawakad.sock" "$1"
}

ui_request() {
    request_at "$control" "$1"
}

json_value() {
    local payload=$1
    local path=$2
    python3 -c 'import json,sys
value=json.load(sys.stdin)
for part in sys.argv[1].split("."):
    value=value[int(part)] if isinstance(value,list) else value[part]
if isinstance(value,bool): print(str(value).lower())
else: print(value)' "$path" <<<"$payload"
}

require_ok() {
    [[ "$(json_value "$1" ok)" == true ]] || {
        python3 -c 'import json,sys
response=json.load(sys.stdin)
failure=response.get("error", {})
print("request did not succeed: %s: %s" % (failure.get("code", "unknown"), failure.get("message", "no message")), file=sys.stderr)' <<<"$1"
        return 1
    }
}

require_failure() {
    local payload=$1
    [[ "$(json_value "$payload" ok)" == false ]] || {
        echo "unsafe request unexpectedly succeeded" >&2
        return 1
    }
}

pause_live_pangu() {
    local candidate
    for _ in $(seq 1 20); do
        candidate="$("${ADB[@]}" shell 'pidof loong_pangu 2>/dev/null || true' | tr -d '\r' | awk '{print $1}')"
        if [[ -n "$candidate" ]] &&
           "${ADB[@]}" shell "kill -STOP '$candidate' 2>/dev/null; grep -Eq '^State:[[:space:]]+T' '/proc/$candidate/status'" >/dev/null 2>&1; then
            LIVE_PANGU_PID="$candidate"
            return
        fi
        sleep 0.05
    done
}

start_daemon() {
    "${ADB[@]}" shell "mkdir -p '$state' '$REMOTE_DIR/runtime' '$REMOTE_DIR/logs'; rm -f '$REMOTE_DIR/runtime/jawakad.sock'; (cd '$REMOTE_DIR' && exec env UMRK_ENV_VERSION=2 PLATFORM=mlp1 \
        SDCARD_PATH='$PRIMARY_ROOT' SDCARD_PATHS='$PRIMARY_ROOT:$SECONDARY_ROOT' \
        UMRK_SECONDARY_SDCARD_PATH='$SECONDARY_ROOT' \
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
        '$REMOTE_DIR/bin/jawakad' --daemon-only) </dev/null >>'$REMOTE_DIR/logs/jawakad.log' 2>&1 & echo \$! >'$REMOTE_DIR/daemon.pid'"
    DAEMON_PID="$("${ADB[@]}" shell "cat '$REMOTE_DIR/daemon.pid'" | tr -d '\r')"
    wait_remote "test -S '$REMOTE_DIR/runtime/jawakad.sock'"
}

folder_dump() {
    local folder_id=$1
    "${ADB[@]}" shell "$receiver_cli folders '$folder_id' dump-json" | tr -d '\r'
}

verify_receive_folder() {
    local source_id=$1
    local expected_path=$2
    local expected_userdata=$3
    local tag=$4
    local plan create denied prepared started folder_id plan_id snapshot_name snapshot_root config

    plan="$(ui_request "{\"v\":1,\"id\":\"$tag-plan\",\"op\":\"folder.onboard.plan\",\"args\":{\"source_id\":\"$source_id\",\"kind\":\"saves\",\"folder_type\":\"sendreceive\"}}")"
    require_ok "$plan"
    [[ "$(json_value "$plan" result.onboarding.path)" == "$expected_path" ]]
    [[ "$(json_value "$plan" result.onboarding.file_count)" -ge 1 ]]
    [[ "$(json_value "$plan" result.onboarding.snapshot_possible)" == true ]]
    folder_id="$(json_value "$plan" result.onboarding.folder_id)"
    plan_id="$(json_value "$plan" result.onboarding.plan_id)"

    denied="$(ui_request "{\"v\":1,\"id\":\"$tag-warning\",\"op\":\"folder.onboard.create\",\"args\":{\"plan_id\":\"$plan_id\",\"confirmed\":true,\"states_warning_acknowledged\":false,\"manual_edit_warning_acknowledged\":false}}")"
    require_failure "$denied"
    create="$(ui_request "{\"v\":1,\"id\":\"$tag-create\",\"op\":\"folder.onboard.create\",\"args\":{\"plan_id\":\"$plan_id\",\"confirmed\":true,\"states_warning_acknowledged\":false,\"manual_edit_warning_acknowledged\":true}}")"
    require_ok "$create"
    python3 -c 'import json,sys
folder_id=sys.argv[1]
row=next(row for row in json.load(sys.stdin)["result"]["folders"] if row["id"] == folder_id)
assert row["first_sync_state"] == "required" and row["paused"] is True' "$folder_id" <<<"$create"

    denied="$(ui_request "{\"v\":1,\"id\":\"$tag-early\",\"op\":\"folder.first-sync.start\",\"args\":{\"folder_id\":\"$folder_id\",\"confirmed\":true,\"hub_versioning_acknowledged\":true}}")"
    require_failure "$denied"
    config="$(folder_dump "$folder_id")"
    [[ "$(json_value "$config" paused)" == true ]]
    [[ "$(json_value "$config" versioning.type)" == simple ]]
    [[ "$(json_value "$config" versioning.params.keep)" == 5 ]]
    [[ "$(json_value "$config" versioning.fsPath)" == "$expected_userdata/Syncthing/versions/saves" ]]
    [[ "$(json_value "$config" versioning.fsType)" == basic ]]

    prepared="$(ui_request "{\"v\":1,\"id\":\"$tag-prepare\",\"op\":\"folder.first-sync.prepare\",\"args\":{\"folder_id\":\"$folder_id\",\"confirmed\":true,\"snapshot_limit_acknowledged\":true}}")"
    require_ok "$prepared"
    snapshot_name="$(python3 -c 'import json,sys
folder_id=sys.argv[1]
for row in json.load(sys.stdin)["result"]["folders"]:
    if row["id"] == folder_id:
        assert row["first_sync_state"] == "ready" and row["snapshot_files"] >= 1
        print(row["snapshot_name"])
        break
else: raise SystemExit(1)' "$folder_id" <<<"$prepared")"
    snapshot_root="$expected_userdata/Syncthing/snapshots/saves/$snapshot_name"
    "${ADB[@]}" shell "set -eu
        test -s '$snapshot_root/snapshot.json'
        test -s '$snapshot_root/manifest.jsonl'
        test -f '$snapshot_root/files/$tag/game.sav'
        test ! -e '$expected_userdata/Syncthing/snapshots/saves/.leaf-first-sync.json'
        grep -F '\"state\":\"ready\"' '$snapshot_root/snapshot.json' >/dev/null
        grep -F '\"path\":\"$tag/game.sav\"' '$snapshot_root/manifest.jsonl' | grep -E '\"sha256\":\"[0-9a-f]{64}\"' >/dev/null
        test \"\$(sha256sum '$expected_path/$tag/game.sav' | cut -d' ' -f1)\" = \"\$(sha256sum '$snapshot_root/files/$tag/game.sav' | cut -d' ' -f1)\""

    started="$(ui_request "{\"v\":1,\"id\":\"$tag-start\",\"op\":\"folder.first-sync.start\",\"args\":{\"folder_id\":\"$folder_id\",\"confirmed\":true,\"hub_versioning_acknowledged\":true}}")"
    require_ok "$started"
    config="$(folder_dump "$folder_id")"
    [[ "$(json_value "$config" paused)" == false ]]
    "${ADB[@]}" shell "grep -F '\"state\":\"complete\"' '$expected_userdata/Syncthing/snapshots/saves/.leaf-first-sync.json' >/dev/null; test ! -e '$expected_userdata/Syncthing/snapshots/saves/.partial-'"
    printf '%s\n' "$folder_id"
}

run_game_case() {
    local label=$1
    local rom_path=$2
    local writer_pid active_run before after
    "${ADB[@]}" shell "rm -f '$REMOTE_DIR/runtime/game-writer-live' '$REMOTE_DIR/runtime/game-writer-done' '$REMOTE_DIR/runtime/active-game.json' '$REMOTE_DIR/$label.response'; ('$REMOTE_DIR/bin/jawaka-platformctl' --socket '$REMOTE_DIR/runtime/jawakad.sock' request '{\"type\":\"launch-game\",\"system\":\"N64\",\"rom_path\":\"$rom_path\"}' >'$REMOTE_DIR/$label.response') &"
    wait_remote "test -f '$REMOTE_DIR/runtime/game-writer-live'" 800
    writer_pid="$("${ADB[@]}" shell "ps -eo pid,comm,args | awk -v root='$PRIMARY_ROOT/.system/leaf/platforms/mlp1/emulators/fixture/game-writer-fixture' '\$2==\"game-writer-fix\" && index(\$0,root){print \$1; exit}'" | tr -d '\r')"
    [[ -n "$writer_pid" ]]
    "${ADB[@]}" shell "kill -STOP '$writer_pid'; test ! -S '$control'; ! ps -eo args | grep -F '$receiver_binary' | grep -v grep; ! ps -eo args | grep -F '$controller_binary' | grep -v grep"
    active_run="$(daemon_request "{\"v\":1,\"op\":\"run\",\"id\":\"$label-run\",\"service_id\":\"$SERVICE_ID\"}" || true)"
    grep -F 'lifecycle-in-progress' <<<"$active_run" >/dev/null
    before="$("${ADB[@]}" shell "find '$PRIMARY_ROOT/Saves' '$SECONDARY_ROOT/Saves' '$PRIMARY_ROOT/States' -type f -exec stat -c '%n %s %b %Y' {} \; | sort | sha256sum" | tr -d '\r')"
    sleep 0.5
    after="$("${ADB[@]}" shell "find '$PRIMARY_ROOT/Saves' '$SECONDARY_ROOT/Saves' '$PRIMARY_ROOT/States' -type f -exec stat -c '%n %s %b %Y' {} \; | sort | sha256sum" | tr -d '\r')"
    [[ "$before" == "$after" ]]
    "${ADB[@]}" shell "kill -CONT '$writer_pid'"
    wait_remote "test -f '$REMOTE_DIR/runtime/game-writer-done' && test ! -e '$REMOTE_DIR/runtime/active-game.json'" 800
    wait_remote "test -S '$control'" 800
    "${ADB[@]}" shell "ps -eo args | grep -F '$receiver_binary' | grep -v grep >/dev/null; ps -eo args | grep -F '$controller_binary' | grep -v grep >/dev/null"
    echo "B3_GAME_RESULT source=$label service_absent_during_writer=true managed_trees_unchanged=true restart_after_barrier=true"
}

echo "Using adb device: $("${ADB[@]}" get-serialno)"
"${ADB[@]}" shell "mountpoint -q /mnt/sdcard && mountpoint -q /media/sdcard1; grep -Eq '^/dev/mmcblk(1|3)p1 /mnt/sdcard vfat ' /proc/mounts; grep -Eq '^/dev/mmcblk(1|3)p1 /media/sdcard1 vfat ' /proc/mounts"
pause_live_pangu

"${ADB[@]}" shell "umount '$PRIMARY_ROOT' 2>/dev/null || true; umount '$SECONDARY_ROOT' 2>/dev/null || true; rm -rf '$REMOTE_DIR' '$PRIMARY_CARD_ROOT' '$SECONDARY_CARD_ROOT'; mkdir -p \
    '$REMOTE_DIR/bin' '$REMOTE_DIR/runtime' '$REMOTE_DIR/logs' '$PRIMARY_ROOT' '$SECONDARY_ROOT' '$PRIMARY_CARD_ROOT' '$SECONDARY_CARD_ROOT'; \
    mount --bind '$PRIMARY_CARD_ROOT' '$PRIMARY_ROOT'; mount --bind '$SECONDARY_CARD_ROOT' '$SECONDARY_ROOT'; mkdir -p \
    '$PRIMARY_ROOT/Apps/mlp1' '$PRIMARY_ROOT/.system/leaf/platforms/mlp1/defaults' '$PRIMARY_ROOT/.system/leaf/platforms/mlp1/emulators/fixture' \
    '$PRIMARY_ROOT/Roms/N64' '$PRIMARY_ROOT/Images/N64' '$PRIMARY_ROOT/BIOS' '$PRIMARY_ROOT/Saves/primary' '$PRIMARY_ROOT/States/primary' \
    '$PRIMARY_ROOT/Music' '$PRIMARY_ROOT/Videos' '$PRIMARY_ROOT/Cheats' '$PRIMARY_ROOT/.userdata/mlp1' '$PRIMARY_ROOT/.userdata/shared' \
    '$SECONDARY_ROOT/Apps/mlp1' '$SECONDARY_ROOT/Roms/N64' '$SECONDARY_ROOT/Images/N64' '$SECONDARY_ROOT/BIOS' '$SECONDARY_ROOT/Saves/secondary' '$SECONDARY_ROOT/States' \
    '$SECONDARY_ROOT/Music' '$SECONDARY_ROOT/Videos' '$SECONDARY_ROOT/Cheats' '$SECONDARY_ROOT/.userdata/mlp1' '$SECONDARY_ROOT/.userdata/shared'; \
    printf 'primary-save\n' >'$PRIMARY_ROOT/Saves/primary/game.sav'; printf 'primary-state\n' >'$PRIMARY_ROOT/States/primary/game.state'; \
    printf 'secondary-save\n' >'$SECONDARY_ROOT/Saves/secondary/game.sav'; mkdir '$SECONDARY_ROOT/States/.stfolder'; \
    printf 'rom\n' >'$PRIMARY_ROOT/Roms/N64/Primary.n64'; printf 'rom\n' >'$SECONDARY_ROOT/Roms/N64/Secondary.n64'; sync"
"${ADB[@]}" push "$JAWAKA_DIR/build/mlp1/bin/jawakad" "$JAWAKA_DIR/build/mlp1/bin/jawaka-platformctl" "$REMOTE_DIR/bin/" >/dev/null
"${ADB[@]}" push "$JAWAKA_DIR/build/mlp1/bin/game-writer-fixture" "$PRIMARY_ROOT/.system/leaf/platforms/mlp1/emulators/fixture/" >/dev/null
"${ADB[@]}" push "$ROOT_DIR/build/mlp1/package/Syncthing.pak" "$PRIMARY_ROOT/Apps/mlp1/" >/dev/null
"${ADB[@]}" shell "chmod 755 '$REMOTE_DIR/bin/'* '$PRIMARY_ROOT/Apps/mlp1/Syncthing.pak/bin/'* '$PRIMARY_ROOT/.system/leaf/platforms/mlp1/emulators/fixture/'*; \
    printf '%s\n' '{\"version\":2,\"platform\":\"mlp1\",\"cores\":[{\"id\":\"writer_fixture\",\"display_name\":\"Writer Fixture\",\"type\":\"path\",\"path\":\"emulators/fixture/game-writer-fixture\",\"supports_menu\":false,\"supports_savestate\":true,\"supports_disk_control\":false,\"needs_swap\":false,\"status\":\"packaged\"}]}' >'$PRIMARY_ROOT/.system/leaf/platforms/mlp1/defaults/cores.json'; \
    printf '%s\n' '{\"version\":2,\"platform\":\"mlp1\",\"systems\":[{\"id\":\"N64\",\"name\":\"Nintendo 64\",\"patterns\":[\"N64\"],\"extensions\":[\"n64\"],\"archive_extensions\":[],\"archive_inner_extensions\":[\"n64\"],\"archive_mode\":\"pass_through\",\"file_names\":[],\"ignore_file_names\":[],\"playlist_extensions\":[],\"m3u_generation\":\"none\",\"default_core\":\"writer_fixture\",\"alternate_cores\":[],\"rom_root\":\"Roms/N64\",\"image_root\":\"Images/N64\",\"bios_notes\":[]}]}' >'$PRIMARY_ROOT/.system/leaf/platforms/mlp1/defaults/systems.json'"

userdata="$PRIMARY_ROOT/.userdata/mlp1"
secondary_userdata="$SECONDARY_ROOT/.userdata/mlp1"
state="$userdata/Jawaka"
receiver_binary="$PRIMARY_ROOT/Apps/mlp1/Syncthing.pak/bin/syncthing"
controller_binary="$PRIMARY_ROOT/Apps/mlp1/Syncthing.pak/bin/leaf-syncthing"
receiver_gui="$REMOTE_DIR/runtime/services/$SERVICE_ID/syncthing-gui.sock"
control="$REMOTE_DIR/runtime/services/$SERVICE_ID/control.sock"

start_daemon
wait_remote "'$REMOTE_DIR/bin/jawaka-platformctl' --socket '$REMOTE_DIR/runtime/jawakad.sock' request '{\"v\":1,\"op\":\"list\",\"id\":\"discover\"}' | grep -F '$SERVICE_ID'" 800
response="$(daemon_request '{"v":1,"op":"run","id":"initial","service_id":"org.umrk.syncthing"}')"
require_ok "$response"
wait_remote "test -S '$control' && test -S '$receiver_gui'" 1000

pre_enrollment="$(ui_request '{"v":1,"id":"pre-enrollment","op":"status.get","args":{}}')"
require_ok "$pre_enrollment"
mapfile -t source_ids < <(python3 -c 'import json,sys
rows=json.load(sys.stdin)["result"]["cards"]
by_slot={row["slot"]:row.get("source_id", "") for row in rows}
assert by_slot.get("Primary") and by_slot.get("Secondary")
print(by_slot["Primary"])
print(by_slot["Secondary"])' <<<"$pre_enrollment")
primary_source=${source_ids[0]}
secondary_source=${source_ids[1]}

response="$(ui_request "{\"v\":1,\"id\":\"enroll-primary\",\"op\":\"card.enroll\",\"args\":{\"source_id\":\"$primary_source\"}}")"
require_ok "$response"
response="$(ui_request "{\"v\":1,\"id\":\"enroll-secondary\",\"op\":\"card.enroll\",\"args\":{\"source_id\":\"$secondary_source\"}}")"
require_ok "$response"
status="$(ui_request '{"v":1,"id":"card-status","op":"status.get","args":{}}')"
require_ok "$status"
python3 -c 'import json,sys
cards=json.load(sys.stdin)["result"]["cards"]
assert {row.get("source_id") for row in cards if row["enrolled"]} == set(sys.argv[1:])
assert all(row["present"] and row["writable"] and not row["duplicate_id"] for row in cards)' "$primary_source" "$secondary_source" <<<"$status"

"${ADB[@]}" shell "'$receiver_binary' generate --config='$REMOTE_DIR/hub/config' --data='$REMOTE_DIR/hub/data' --no-port-probing >/dev/null 2>&1"
hub_id="$("${ADB[@]}" shell "'$receiver_binary' -C '$REMOTE_DIR/hub/config' -D '$REMOTE_DIR/hub/data' device-id" | tr -d '\r')"
response="$(ui_request "{\"v\":1,\"id\":\"add-hub\",\"op\":\"device.add\",\"args\":{\"device_id\":\"$hub_id\",\"name\":\"B3 Hub\"}}")"
require_ok "$response"
receiver_api_key="$("${ADB[@]}" shell "sed -n 's:.*<apikey>\([^<]*\)</apikey>.*:\1:p' '$userdata/Syncthing/config/config.xml'" | tr -d '\r')"
[[ -n "$receiver_api_key" ]]
receiver_cli="'$receiver_binary' -C '$userdata/Syncthing/config' -D '$userdata/Syncthing/data' cli --gui-address='$receiver_gui' --gui-apikey='$receiver_api_key' config"

primary_folder="$(verify_receive_folder "$primary_source" "$PRIMARY_ROOT/Saves" "$userdata" primary)"
secondary_folder="$(verify_receive_folder "$secondary_source" "$SECONDARY_ROOT/Saves" "$secondary_userdata" secondary)"
[[ "$primary_folder" != "$secondary_folder" ]]

foreign="$(ui_request "{\"v\":1,\"id\":\"foreign-states\",\"op\":\"folder.onboard.plan\",\"args\":{\"source_id\":\"$secondary_source\",\"kind\":\"states\",\"folder_type\":\"sendreceive\"}}")"
require_failure "$foreign"
[[ "$(json_value "$foreign" error.code)" == foreign-folder-manager ]]
"${ADB[@]}" shell "rm -rf '$SECONDARY_ROOT/States/.stfolder'"

states_plan="$(ui_request "{\"v\":1,\"id\":\"states-plan\",\"op\":\"folder.onboard.plan\",\"args\":{\"source_id\":\"$primary_source\",\"kind\":\"states\",\"folder_type\":\"sendonly\"}}")"
require_ok "$states_plan"
[[ "$(json_value "$states_plan" result.onboarding.states_warning)" == true ]]
states_plan_id="$(json_value "$states_plan" result.onboarding.plan_id)"
states_folder="$(json_value "$states_plan" result.onboarding.folder_id)"
states_denied="$(ui_request "{\"v\":1,\"id\":\"states-denied\",\"op\":\"folder.onboard.create\",\"args\":{\"plan_id\":\"$states_plan_id\",\"confirmed\":true,\"states_warning_acknowledged\":false,\"manual_edit_warning_acknowledged\":true}}")"
require_failure "$states_denied"
response="$(ui_request "{\"v\":1,\"id\":\"states-create\",\"op\":\"folder.onboard.create\",\"args\":{\"plan_id\":\"$states_plan_id\",\"confirmed\":true,\"states_warning_acknowledged\":true,\"manual_edit_warning_acknowledged\":true}}")"
require_ok "$response"
response="$(ui_request "{\"v\":1,\"id\":\"states-start\",\"op\":\"folder.first-sync.start\",\"args\":{\"folder_id\":\"$states_folder\",\"confirmed\":true,\"hub_versioning_acknowledged\":true}}")"
require_ok "$response"
"${ADB[@]}" shell "grep -F '\"mode\":\"sendonly\"' '$userdata/Syncthing/snapshots/states/.leaf-first-sync.json' >/dev/null"

duplicate="$(ui_request "{\"v\":1,\"id\":\"duplicate-primary\",\"op\":\"folder.onboard.plan\",\"args\":{\"source_id\":\"$primary_source\",\"kind\":\"saves\",\"folder_type\":\"sendreceive\"}}")"
require_failure "$duplicate"

response="$(ui_request "{\"v\":1,\"id\":\"to-sendonly\",\"op\":\"folder.type.set\",\"args\":{\"folder_id\":\"$primary_folder\",\"folder_type\":\"sendonly\",\"confirmed\":true}}")"
require_ok "$response"
response="$(ui_request "{\"v\":1,\"id\":\"start-sendonly\",\"op\":\"folder.first-sync.start\",\"args\":{\"folder_id\":\"$primary_folder\",\"confirmed\":true,\"hub_versioning_acknowledged\":true}}")"
require_ok "$response"
response="$(ui_request "{\"v\":1,\"id\":\"back-receive\",\"op\":\"folder.type.set\",\"args\":{\"folder_id\":\"$primary_folder\",\"folder_type\":\"sendreceive\",\"confirmed\":true}}")"
require_ok "$response"
denied="$(ui_request "{\"v\":1,\"id\":\"old-snapshot-denied\",\"op\":\"folder.first-sync.start\",\"args\":{\"folder_id\":\"$primary_folder\",\"confirmed\":true,\"hub_versioning_acknowledged\":true}}")"
require_failure "$denied"
response="$(ui_request "{\"v\":1,\"id\":\"reprepare\",\"op\":\"folder.first-sync.prepare\",\"args\":{\"folder_id\":\"$primary_folder\",\"confirmed\":true,\"snapshot_limit_acknowledged\":true}}")"
require_ok "$response"
response="$(ui_request "{\"v\":1,\"id\":\"restart-receive\",\"op\":\"folder.first-sync.start\",\"args\":{\"folder_id\":\"$primary_folder\",\"confirmed\":true,\"hub_versioning_acknowledged\":true}}")"
require_ok "$response"

"${ADB[@]}" shell "printf 'conflict\n' >'$PRIMARY_ROOT/Saves/primary/game.sync-conflict-20260809-120000-HUB.sav'"
inspected="$(ui_request "{\"v\":1,\"id\":\"inspect-conflict\",\"op\":\"folder.inspect\",\"args\":{\"folder_id\":\"$primary_folder\"}}")"
require_ok "$inspected"
python3 -c 'import json,sys
folder_id=sys.argv[1]
row=next(row for row in json.load(sys.stdin)["result"]["folders"] if row["id"] == folder_id)
assert row["conflict_count"] >= 1
assert any(".sync-conflict-" in path for path in row["conflicts"])' "$primary_folder" <<<"$inspected"
"${ADB[@]}" shell "test -f '$PRIMARY_ROOT/Saves/primary/game.sync-conflict-20260809-120000-HUB.sav'"

response="$(daemon_request '{"v":1,"op":"enable","id":"enable-game-tests","service_id":"org.umrk.syncthing"}')"
require_ok "$response"
run_game_case primary "$PRIMARY_ROOT/Roms/N64/Primary.n64"
run_game_case secondary "$SECONDARY_ROOT/Roms/N64/Secondary.n64"

echo "B3_FOLDER_RESULT primary_and_secondary_receive=true same_card_snapshots=true explicit_start=true sendonly_receive_reprotected=true states_warning=true foreign_marker_refused=true conflicts_preserved=true"
echo "PASS MLP1 B3 two-card folder onboarding, first-sync protection, conflicts, and stop-only gameplay lifecycle"
