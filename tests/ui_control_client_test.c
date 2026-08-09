#include "ls_ui_control.h"

#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#ifndef UI_CONTROL_FIXTURES_ROOT
#define UI_CONTROL_FIXTURES_ROOT "tests/fixtures/ui-control-v1"
#endif

static char *read_fixture(const char *name, size_t *size) {
    char path[4096];
    FILE *file;
    long end;
    char *payload;
    assert(snprintf(path, sizeof(path), "%s/%s", UI_CONTROL_FIXTURES_ROOT, name) > 0);
    file = fopen(path, "rb");
    assert(file);
    assert(fseek(file, 0, SEEK_END) == 0);
    end = ftell(file);
    assert(end > 0);
    assert(fseek(file, 0, SEEK_SET) == 0);
    payload = malloc((size_t)end + 1u);
    assert(payload);
    assert(fread(payload, 1, (size_t)end, file) == (size_t)end);
    assert(fclose(file) == 0);
    while (end > 0 && (payload[end - 1] == '\n' || payload[end - 1] == '\r')) end--;
    payload[end] = '\0';
    *size = (size_t)end;
    return payload;
}

int main(void) {
    ls_ui_status status;
    char error[256];
    size_t size;
    const char *rich_payload =
        "{\"v\":1,\"id\":\"fixture-rich\",\"ok\":true,\"result\":{"
        "\"controller\":\"running\","
        "\"upstream\":{\"state\":\"running\",\"version\":\"v2.1.2\",\"device_id\":\"LOCAL\"},"
        "\"game\":{\"active\":false,\"launch_id\":\"\",\"source_id\":\"\"},"
        "\"recovery\":{\"state\":\"ready\",\"changed\":false},"
        "\"transfer\":{\"state\":\"syncing\",\"local_bytes\":10,\"global_bytes\":20,"
            "\"need_bytes\":10,\"in_bytes\":30,\"out_bytes\":40},"
        "\"logging\":{\"level\":\"debug\",\"debug_expires\":\"2026-08-08T12:15:00Z\"},"
        "\"storage\":{\"snapshot_bytes\":100,\"version_bytes\":200,\"snapshot_count\":1,"
            "\"version_groups\":2,\"inventory\":[{\"card_suffix\":\"aabbccdd\","
            "\"category\":\"saves\",\"kind\":\"snapshot\",\"name\":\"first-sync\",\"bytes\":100}]},"
        "\"diagnostics\":{\"last_export_path\":\"/mnt/sdcard/Logs/leaf-syncthing-diagnostics.json\","
            "\"last_exported\":\"2026-08-08T12:00:00Z\"},"
        "\"onboarding\":{\"plan_id\":\"00112233445566778899aabbccddeeff\",\"source_id\":\"primary\","
            "\"card_id\":\"00112233445566778899aabbccddeeff\",\"kind\":\"saves\","
            "\"folder_type\":\"sendreceive\",\"folder_id\":\"leaf-saves-0011223344556677\","
            "\"label\":\"Leaf Saves — Primary\",\"path\":\"/mnt/sdcard/Saves\","
            "\"file_count\":3,\"directory_count\":2,\"content_bytes\":4096,"
            "\"available_bytes\":8192,\"snapshot_possible\":true,\"peer_count\":1,"
            "\"states_warning\":false,\"join_existing\":true,\"offer_device_id\":\"PEER\","
            "\"expires_at\":\"2026-08-08T12:05:00Z\"},"
        "\"cards\":[{\"id\":\"00112233445566778899aabbccddeeff\",\"source_id\":\"primary\","
            "\"id_suffix\":\"ccddeeff\",\"slot\":\"Primary\",\"root\":\"/mnt/sdcard\","
            "\"state\":\"enrolled\",\"enrolled\":true,\"present\":true,\"writable\":true,"
            "\"duplicate_id\":false,\"retained_bytes\":0}],"
        "\"folders\":[{\"id\":\"leaf-saves-0011223344556677\",\"label\":\"Leaf Saves\","
            "\"card_id\":\"00112233445566778899aabbccddeeff\",\"kind\":\"saves\",\"path\":\"/mnt/sdcard/Saves\","
            "\"type\":\"sendreceive\",\"state\":\"idle\",\"paused\":false,\"pause_reasons\":[],"
            "\"pending_rescan\":false,\"local_bytes\":10,\"global_bytes\":10,\"local_items\":3,\"global_items\":4,\"peer_count\":1,"
            "\"device_ids\":[\"LOCAL\",\"PEER\"],"
            "\"last_sync\":\"2026-08-08T12:00:00Z\",\"versioning\":\"simple\",\"conflict_count\":1,"
            "\"first_sync_state\":\"ready\",\"snapshot_name\":\"first-sync-20260808T120000Z\","
            "\"snapshot_files\":3,\"snapshot_directories\":2,\"snapshot_bytes\":4096,"
            "\"first_sync_message\":\"Safety snapshot is durable; explicitly start the merge\","
            "\"conflicts\":[\"game.sync-conflict-20260808-120000-PEER.sav\"],\"issues\":[]}],"
        "\"peers\":[{\"id\":\"PEER\",\"id_suffix\":\"PEER\","
            "\"name\":\"Laptop\",\"state\":\"connected\",\"connection\":\"direct\","
            "\"address\":\"tcp://192.0.2.2:22000\",\"paused\":false,\"introducer\":false,\"pending\":false}],"
        "\"folder_offers\":[{\"folder_id\":\"retro-saves\",\"label\":\"Retro Saves\","
            "\"device_id\":\"PEER\",\"device_id_suffix\":\"PEER\",\"device_name\":\"Laptop\","
            "\"offered_at\":\"2026-08-09T12:34:56Z\",\"receive_encrypted\":false,\"remote_encrypted\":false}],"
        "\"issues\":[],\"capabilities\":[\"log.level.set\",\"diagnostics.export\"]}}";
    char *payload = read_fixture("status-get-response.json", &size);
    assert(ls_ui_parse_response(payload, size, "fixture-status", &status,
                                error, sizeof(error)) == 0);
    assert(strcmp(status.controller, "running") == 0);
    assert(strcmp(status.upstream_version, "v2.1.2") == 0);
    assert(status.card_count == 1 && !status.cards[0].present);
    assert(status.folder_count == 1 && status.folders[0].paused);
    assert(status.folder_offer_count == 1 &&
           strcmp(status.folder_offers[0].folder_id, "retro-saves") == 0);
    assert(status.issue_count == 1);
    assert(ls_ui_has_capability(&status, "card.enroll"));
    assert(ls_ui_parse_response(payload, size, "wrong-id", &status,
                                error, sizeof(error)) == -1);
    free(payload);

    payload = read_fixture("gateway-open-response.json", &size);
    assert(ls_ui_parse_response(payload, size, "fixture-gateway", &status,
                                error, sizeof(error)) == 0);
    assert(status.gateway_present && status.gateway_open && status.gateway_pairing);
    assert(strcmp(status.gateway_pin, "1234") == 0);
    assert(status.gateway_trusted_browsers == 1);
    free(payload);

    payload = read_fixture("network-profile-set-response.json", &size);
    assert(ls_ui_parse_response(payload, size, "fixture-network", &status,
                                error, sizeof(error)) == 0);
    assert(status.network_present);
    assert(strcmp(status.network_profile, "lan-only") == 0);
    assert(status.allowed_network_count == 2);
    assert(strcmp(status.allowed_networks[1], "2001:db8::/64") == 0);
    free(payload);

    payload = read_fixture("unsupported-op-response.json", &size);
    assert(ls_ui_parse_response(payload, size, "fixture-unsupported", &status,
                                error, sizeof(error)) == 1);
    assert(strcmp(error, "unsupported UI control operation") == 0);
    free(payload);

    assert(ls_ui_parse_response(rich_payload, strlen(rich_payload), "fixture-rich", &status,
                                error, sizeof(error)) == 0);
    assert(status.transfer_present && status.transfer_need_bytes == 10);
    assert(status.logging_present && strcmp(status.log_level, "debug") == 0);
    assert(status.storage_present && status.snapshot_count == 1 && status.version_groups == 2);
    assert(status.storage_row_count == 1 && status.storage_rows[0].bytes == 100);
    assert(status.folder_count == 1 && status.folders[0].conflict_count == 1);
    assert(status.folders[0].conflict_path_count == 1 &&
           strstr(status.folders[0].conflicts[0], ".sync-conflict-") != NULL);
    assert(strcmp(status.diagnostics_path,
                  "/mnt/sdcard/Logs/leaf-syncthing-diagnostics.json") == 0);
    assert(status.peer_count == 1 && strcmp(status.peers[0].connection, "direct") == 0);
    assert(status.folder_offer_count == 1 &&
           strcmp(status.folder_offers[0].device_name, "Laptop") == 0);
    assert(status.onboarding_present && status.onboarding.snapshot_possible);
    assert(status.onboarding.join_existing && strcmp(status.onboarding.offer_device_id, "PEER") == 0);
    assert(status.card_count == 1 && strcmp(status.cards[0].source_id, "primary") == 0);
    assert(status.onboarding.file_count == 3 && status.onboarding.available_bytes == 8192);
    assert(status.folder_count == 1 && status.folders[0].local_items == 3 &&
           status.folders[0].global_items == 4);
    assert(status.folders[0].device_count == 2 &&
           strcmp(status.folders[0].device_ids[1], "PEER") == 0);
    assert(strcmp(status.folders[0].first_sync_state, "ready") == 0 &&
           status.folders[0].snapshot_files == 3 && status.folders[0].snapshot_bytes == 4096);
    puts("PASS ui-control-v1 C semantic client (4 frozen fixtures + B3 rich status)");
    return 0;
}
