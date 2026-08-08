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
    char *payload = read_fixture("status-get-response.json", &size);
    assert(ls_ui_parse_response(payload, size, "fixture-status", &status,
                                error, sizeof(error)) == 0);
    assert(strcmp(status.controller, "running") == 0);
    assert(strcmp(status.upstream_version, "v2.1.2") == 0);
    assert(status.card_count == 1 && !status.cards[0].present);
    assert(status.folder_count == 1 && status.folders[0].paused);
    assert(status.issue_count == 1);
    assert(ls_ui_has_capability(&status, "card.enroll"));
    assert(ls_ui_parse_response(payload, size, "wrong-id", &status,
                                error, sizeof(error)) == -1);
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
    puts("PASS ui-control-v1 C semantic client (3 fixtures)");
    return 0;
}
