#include <arpa/inet.h>
#include <assert.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#ifndef UI_CONTROL_FIXTURES_ROOT
#define UI_CONTROL_FIXTURES_ROOT "tests/fixtures/ui-control-v1"
#endif

#define UI_CONTROL_MAX_PAYLOAD (64u * 1024u)

static unsigned char *read_fixture(const char *name, size_t *out_len) {
    char path[4096];
    assert(snprintf(path, sizeof(path), "%s/%s", UI_CONTROL_FIXTURES_ROOT, name) > 0);
    FILE *file = fopen(path, "rb");
    assert(file);
    assert(fseek(file, 0, SEEK_END) == 0);
    long end = ftell(file);
    assert(end > 0);
    assert(fseek(file, 0, SEEK_SET) == 0);
    unsigned char *payload = malloc((size_t)end + 1u);
    assert(payload);
    assert(fread(payload, 1, (size_t)end, file) == (size_t)end);
    assert(fclose(file) == 0);
    while (end > 0 && (payload[end - 1] == '\n' || payload[end - 1] == '\r' ||
                       payload[end - 1] == ' ' || payload[end - 1] == '\t')) end--;
    payload[end] = '\0';
    *out_len = (size_t)end;
    return payload;
}

static void round_trip(const char *name, const char *required) {
    size_t payload_len = 0;
    unsigned char *payload = read_fixture(name, &payload_len);
    assert(payload_len > 1 && payload_len <= UI_CONTROL_MAX_PAYLOAD);
    assert(payload[0] == '{' && payload[payload_len - 1] == '}');
    assert(strstr((char *)payload, "\"v\":1"));
    assert(strstr((char *)payload, required));

    size_t frame_len = sizeof(uint32_t) + payload_len;
    unsigned char *frame = malloc(frame_len);
    assert(frame);
    uint32_t network_len = htonl((uint32_t)payload_len);
    memcpy(frame, &network_len, sizeof(network_len));
    memcpy(frame + sizeof(network_len), payload, payload_len);

    uint32_t decoded_len = 0;
    memcpy(&decoded_len, frame, sizeof(decoded_len));
    assert((size_t)ntohl(decoded_len) == payload_len);
    assert(memcmp(frame + sizeof(decoded_len), payload, payload_len) == 0);
    free(frame);
    free(payload);
}

int main(void) {
    round_trip("status-get-request.json", "\"op\":\"status.get\"");
    round_trip("status-get-response.json", "\"capabilities\":[\"status.get\",\"card.enroll\"]");
    round_trip("card-enroll-request.json", "\"op\":\"card.enroll\"");
    round_trip("card-enroll-response.json", "\"state\":\"enrolled\"");
    round_trip("network-profile-set-request.json", "\"op\":\"network.profile.set\"");
    round_trip("network-profile-set-response.json", "\"profile\":\"lan-only\"");
    round_trip("gateway-open-request.json", "\"op\":\"gateway.open\"");
    round_trip("gateway-open-response.json", "\"pairing\":true");
    round_trip("unsupported-op-response.json", "\"code\":\"unsupported-op\"");
    round_trip("folder-onboard-plan-request.json", "\"op\":\"folder.onboard.plan\"");
    round_trip("folder-offer-plan-request.json", "\"op\":\"folder.offer.plan\"");
    round_trip("folder-onboard-create-request.json", "\"manual_edit_warning_acknowledged\":true");
    round_trip("folder-first-sync-prepare-request.json", "\"snapshot_limit_acknowledged\":true");
    round_trip("folder-first-sync-start-request.json", "\"hub_versioning_acknowledged\":true");
    round_trip("folder-type-set-request.json", "\"op\":\"folder.type.set\"");
    puts("PASS ui-control-v1 C fixture round trip (15 fixtures)");
    return 0;
}
