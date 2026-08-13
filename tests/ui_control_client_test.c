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
        "\"upstream\":{\"state\":\"running\",\"version\":\"v2.1.3\",\"device_id\":\"LOCAL\"},"
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
            "\"need_bytes\":5,\"need_items\":1,\"remote_state\":\"syncing\",\"remote_peer\":\"Laptop\","
            "\"remote_need_bytes\":7,\"remote_need_items\":2,"
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
            "\"offered_at\":\"2026-08-09T12:34:56Z\",\"receive_encrypted\":false,\"remote_encrypted\":false,\"ignored\":true}],"
        "\"issues\":[],\"capabilities\":[\"log.level.set\",\"diagnostics.export\"]}}";
    char *payload = read_fixture("status-get-response.json", &size);
    assert(ls_ui_parse_response(payload, size, "fixture-status", &status,
                                error, sizeof(error)) == 0);
    assert(strcmp(status.controller, "running") == 0);
    assert(strcmp(status.upstream_version, "v2.1.3") == 0);
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
           strcmp(status.folder_offers[0].device_name, "Laptop") == 0 && status.folder_offers[0].ignored);
    assert(status.onboarding_present && status.onboarding.snapshot_possible);
    assert(status.onboarding.join_existing && strcmp(status.onboarding.offer_device_id, "PEER") == 0);
    assert(status.card_count == 1 && strcmp(status.cards[0].source_id, "primary") == 0);
    assert(status.onboarding.file_count == 3 && status.onboarding.available_bytes == 8192);
    assert(status.folder_count == 1 && status.folders[0].local_items == 3 &&
           status.folders[0].global_items == 4);
    assert(status.folders[0].need_bytes == 5 && status.folders[0].need_items == 1);
    assert(strcmp(status.folders[0].remote_state, "syncing") == 0 &&
           strcmp(status.folders[0].remote_peer, "Laptop") == 0 &&
           status.folders[0].remote_need_bytes == 7 && status.folders[0].remote_need_items == 2);
    assert(status.folders[0].device_count == 2 &&
           strcmp(status.folders[0].device_ids[1], "PEER") == 0);
    assert(strcmp(status.folders[0].first_sync_state, "ready") == 0 &&
           status.folders[0].snapshot_files == 3 && status.folders[0].snapshot_bytes == 4096);
    assert(strcmp(ls_ui_guided_progress_label(&status), "Finish first sync") == 0);
    status.cards[0].present = false;
    assert(strcmp(ls_ui_guided_progress_label(&status), "Fix card") == 0);
    status.cards[0].enrolled = false;
    assert(strcmp(ls_ui_guided_progress_label(&status), "Enroll card") == 0);
    status.cards[0].enrolled = true;
    status.cards[0].present = true;
    status.peers[0].pending = true;
    assert(strcmp(ls_ui_guided_progress_label(&status), "Connect device") == 0);
    status.peers[0].pending = false;
    status.folder_count = 0;
    assert(strcmp(ls_ui_guided_progress_label(&status), "Set up Saves") == 0);
    assert(!ls_ui_guided_setup_complete(&status));
    status.folder_count = 1;
    assert(!ls_ui_guided_setup_complete(&status));
    assert(strcmp(ls_ui_folder_state_label(&status.folders[0]), "Needs attention") == 0);
    {
        ls_ui_status_summary summary;
        assert(ls_ui_summarize_status(&status, &summary) == 0);
        assert(summary.state == LS_UI_NEEDS_ATTENTION);
        assert(strcmp(ls_ui_top_state_label(summary.state), "Needs attention") == 0);

        status.issue_count = 0;
        status.folder_offer_count = 0;
        status.folders[0].conflict_count = 0;
        snprintf(status.folders[0].first_sync_state, sizeof(status.folders[0].first_sync_state), "complete");
        status.folders[0].need_bytes = 0;
        status.folders[0].need_items = 0;
        snprintf(status.folders[0].remote_state, sizeof(status.folders[0].remote_state), "syncing");
        status.folders[0].remote_need_bytes = 7;
        status.folders[0].remote_need_items = 2;
        assert(strcmp(ls_ui_folder_state_label(&status.folders[0]), "Syncing") == 0);
        assert(ls_ui_summarize_status(&status, &summary) == 0 && summary.state == LS_UI_SYNCING &&
               summary.need_bytes == 7 && summary.need_items == 2);
        assert(strcmp(ls_ui_guided_progress_label(&status), "Syncing") == 0);

        snprintf(status.folders[0].remote_state, sizeof(status.folders[0].remote_state), "current");
        status.folders[0].remote_need_bytes = 0;
        status.folders[0].remote_need_items = 0;
        assert(strcmp(ls_ui_folder_state_label(&status.folders[0]), "Up to date") == 0);
        assert(ls_ui_summarize_status(&status, &summary) == 0 && summary.state == LS_UI_UP_TO_DATE);
        assert(strcmp(ls_ui_guided_progress_label(&status), "Complete") == 0);
        assert(ls_ui_guided_setup_complete(&status));

        status.folder_offers[0].ignored = false;
        status.folder_offer_count = 1;
        assert(strcmp(ls_ui_guided_progress_label(&status), "Review offer") == 0);
        status.folder_offers[1] = status.folder_offers[0];
        status.folder_offer_count = 2;
        assert(strcmp(ls_ui_guided_progress_label(&status), "Review offer") == 0);
        status.folder_offers[0].ignored = true;
        status.folder_offers[1].ignored = true;
        assert(strcmp(ls_ui_guided_progress_label(&status), "Complete") == 0);
        status.folder_offer_count = 0;

        snprintf(status.folders[0].remote_state, sizeof(status.folders[0].remote_state), "offline");
        assert(ls_ui_summarize_status(&status, &summary) == 0 && summary.state == LS_UI_NEEDS_ATTENTION &&
               strstr(summary.message, "Laptop is offline") != NULL &&
               strstr(summary.message, "Start Syncthing") != NULL);
        assert(strcmp(ls_ui_guided_progress_label(&status), "Fix issue") == 0);
        assert(ls_ui_guided_setup_complete(&status));

        snprintf(status.folders[0].remote_state, sizeof(status.folders[0].remote_state), "paused");
        assert(ls_ui_summarize_status(&status, &summary) == 0 && strstr(summary.message, "Resume the folder") != NULL);
        snprintf(status.folders[0].remote_state, sizeof(status.folders[0].remote_state), "not-sharing");
        assert(ls_ui_summarize_status(&status, &summary) == 0 && strstr(summary.message, "Share it with Leaf") != NULL);
        snprintf(status.folders[0].remote_state, sizeof(status.folders[0].remote_state), "unknown");
        assert(ls_ui_summarize_status(&status, &summary) == 0 &&
               strstr(summary.message, "accepted and shared") != NULL);
        memset(status.folders[0].remote_peer, 'P', sizeof(status.folders[0].remote_peer) - 1);
        status.folders[0].remote_peer[sizeof(status.folders[0].remote_peer) - 1] = '\0';
        memset(status.folders[0].label, 'F', sizeof(status.folders[0].label) - 1);
        status.folders[0].label[sizeof(status.folders[0].label) - 1] = '\0';
        assert(ls_ui_summarize_status(&status, &summary) == 0 &&
               strlen(summary.message) < sizeof(summary.message) &&
               strstr(summary.message, "accepted and shared") != NULL);

        snprintf(status.controller, sizeof(status.controller), "stopped");
        assert(!ls_ui_guided_setup_complete(&status));
        snprintf(status.controller, sizeof(status.controller), "running");
    }
    puts("PASS ui-control-v1 C semantic client (4 frozen fixtures + B3 rich status)");
    return 0;
}
