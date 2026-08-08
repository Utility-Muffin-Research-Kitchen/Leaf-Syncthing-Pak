#include "ls_ui_control.h"

#include "cJSON.h"
#include "ls_framed_socket.h"

#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static atomic_uint ls_request_sequence = 1u;

static void ls_error(char *error, size_t size, const char *message) {
    if (error && size > 0) snprintf(error, size, "%s", message ? message : "Request failed");
}

static int ls_copy_json(char *target, size_t size, const cJSON *value) {
    size_t length;
    if (!target || size == 0 || !cJSON_IsString(value) || !value->valuestring) return -1;
    length = strlen(value->valuestring);
    if (length >= size) return -1;
    memcpy(target, value->valuestring, length + 1u);
    return 0;
}

static int ls_parse_status(const cJSON *result, ls_ui_status *status) {
    const cJSON *upstream;
    const cJSON *game;
    const cJSON *recovery;
    const cJSON *network;
    const cJSON *cards;
    const cJSON *folders;
    const cJSON *issues;
    const cJSON *capabilities;
    const cJSON *value;
    int index;

    if (!cJSON_IsObject(result) || !status) return -1;
    memset(status, 0, sizeof(*status));
    if (ls_copy_json(status->controller, sizeof(status->controller),
                     cJSON_GetObjectItemCaseSensitive(result, "controller")) != 0) return -1;
    upstream = cJSON_GetObjectItemCaseSensitive(result, "upstream");
    game = cJSON_GetObjectItemCaseSensitive(result, "game");
    recovery = cJSON_GetObjectItemCaseSensitive(result, "recovery");
    if (!cJSON_IsObject(upstream) || !cJSON_IsObject(game) || !cJSON_IsObject(recovery) ||
        ls_copy_json(status->upstream_state, sizeof(status->upstream_state),
                     cJSON_GetObjectItemCaseSensitive(upstream, "state")) != 0 ||
        ls_copy_json(status->upstream_version, sizeof(status->upstream_version),
                     cJSON_GetObjectItemCaseSensitive(upstream, "version")) != 0 ||
        ls_copy_json(status->device_id, sizeof(status->device_id),
                     cJSON_GetObjectItemCaseSensitive(upstream, "device_id")) != 0 ||
        ls_copy_json(status->game_launch_id, sizeof(status->game_launch_id),
                     cJSON_GetObjectItemCaseSensitive(game, "launch_id")) != 0 ||
        ls_copy_json(status->game_source_id, sizeof(status->game_source_id),
                     cJSON_GetObjectItemCaseSensitive(game, "source_id")) != 0 ||
        ls_copy_json(status->recovery_state, sizeof(status->recovery_state),
                     cJSON_GetObjectItemCaseSensitive(recovery, "state")) != 0) return -1;
    value = cJSON_GetObjectItemCaseSensitive(game, "active");
    if (!cJSON_IsBool(value)) return -1;
    status->game_active = cJSON_IsTrue(value);
    value = cJSON_GetObjectItemCaseSensitive(recovery, "changed");
    if (!cJSON_IsBool(value)) return -1;
    status->recovery_changed = cJSON_IsTrue(value);

    network = cJSON_GetObjectItemCaseSensitive(result, "network");
    if (network) {
        const cJSON *allowed;
        if (!cJSON_IsObject(network) ||
            ls_copy_json(status->network_profile, sizeof(status->network_profile),
                         cJSON_GetObjectItemCaseSensitive(network, "profile")) != 0) return -1;
        allowed = cJSON_GetObjectItemCaseSensitive(network, "allowed_networks");
        if (!cJSON_IsArray(allowed) || cJSON_GetArraySize(allowed) > LS_UI_MAX_NETWORKS) return -1;
        status->allowed_network_count = cJSON_GetArraySize(allowed);
        for (index = 0; index < status->allowed_network_count; index++) {
            if (ls_copy_json(status->allowed_networks[index], sizeof(status->allowed_networks[index]),
                             cJSON_GetArrayItem(allowed, index)) != 0) return -1;
        }
        value = cJSON_GetObjectItemCaseSensitive(network, "route_changed");
        if (!cJSON_IsBool(value)) return -1;
        status->network_route_changed = cJSON_IsTrue(value);
        status->network_present = true;
    }

    cards = cJSON_GetObjectItemCaseSensitive(result, "cards");
    folders = cJSON_GetObjectItemCaseSensitive(result, "folders");
    issues = cJSON_GetObjectItemCaseSensitive(result, "issues");
    capabilities = cJSON_GetObjectItemCaseSensitive(result, "capabilities");
    if (!cJSON_IsArray(cards) || cJSON_GetArraySize(cards) > LS_UI_MAX_CARDS ||
        !cJSON_IsArray(folders) || cJSON_GetArraySize(folders) > LS_UI_MAX_FOLDERS ||
        !cJSON_IsArray(issues) || cJSON_GetArraySize(issues) > LS_UI_MAX_ISSUES ||
        !cJSON_IsArray(capabilities) || cJSON_GetArraySize(capabilities) > LS_UI_MAX_CAPABILITIES) return -1;

    status->card_count = cJSON_GetArraySize(cards);
    for (index = 0; index < status->card_count; index++) {
        const cJSON *item = cJSON_GetArrayItem(cards, index);
        ls_ui_card *card = &status->cards[index];
        if (!cJSON_IsObject(item) ||
            ls_copy_json(card->id, sizeof(card->id), cJSON_GetObjectItemCaseSensitive(item, "id")) != 0 ||
            ls_copy_json(card->id_suffix, sizeof(card->id_suffix), cJSON_GetObjectItemCaseSensitive(item, "id_suffix")) != 0 ||
            ls_copy_json(card->slot, sizeof(card->slot), cJSON_GetObjectItemCaseSensitive(item, "slot")) != 0 ||
            ls_copy_json(card->root, sizeof(card->root), cJSON_GetObjectItemCaseSensitive(item, "root")) != 0 ||
            ls_copy_json(card->state, sizeof(card->state), cJSON_GetObjectItemCaseSensitive(item, "state")) != 0) return -1;
        value = cJSON_GetObjectItemCaseSensitive(item, "retained_bytes");
        if (!cJSON_IsNumber(value) || value->valuedouble < 0) return -1;
        card->retained_bytes = (long long)value->valuedouble;
        value = cJSON_GetObjectItemCaseSensitive(item, "enrolled");
        if (!cJSON_IsBool(value)) return -1;
        card->enrolled = cJSON_IsTrue(value);
        value = cJSON_GetObjectItemCaseSensitive(item, "present");
        if (!cJSON_IsBool(value)) return -1;
        card->present = cJSON_IsTrue(value);
        value = cJSON_GetObjectItemCaseSensitive(item, "writable");
        if (!cJSON_IsBool(value)) return -1;
        card->writable = cJSON_IsTrue(value);
        value = cJSON_GetObjectItemCaseSensitive(item, "duplicate_id");
        if (!cJSON_IsBool(value)) return -1;
        card->duplicate_id = cJSON_IsTrue(value);
    }

    status->folder_count = cJSON_GetArraySize(folders);
    for (index = 0; index < status->folder_count; index++) {
        const cJSON *item = cJSON_GetArrayItem(folders, index);
        ls_ui_folder *folder = &status->folders[index];
        if (!cJSON_IsObject(item) ||
            ls_copy_json(folder->id, sizeof(folder->id), cJSON_GetObjectItemCaseSensitive(item, "id")) != 0 ||
            ls_copy_json(folder->label, sizeof(folder->label), cJSON_GetObjectItemCaseSensitive(item, "label")) != 0 ||
            ls_copy_json(folder->card_id, sizeof(folder->card_id), cJSON_GetObjectItemCaseSensitive(item, "card_id")) != 0 ||
            ls_copy_json(folder->kind, sizeof(folder->kind), cJSON_GetObjectItemCaseSensitive(item, "kind")) != 0 ||
            ls_copy_json(folder->path, sizeof(folder->path), cJSON_GetObjectItemCaseSensitive(item, "path")) != 0 ||
            ls_copy_json(folder->type, sizeof(folder->type), cJSON_GetObjectItemCaseSensitive(item, "type")) != 0 ||
            ls_copy_json(folder->state, sizeof(folder->state), cJSON_GetObjectItemCaseSensitive(item, "state")) != 0 ||
            ls_copy_json(folder->last_sync, sizeof(folder->last_sync), cJSON_GetObjectItemCaseSensitive(item, "last_sync")) != 0 ||
            ls_copy_json(folder->versioning, sizeof(folder->versioning), cJSON_GetObjectItemCaseSensitive(item, "versioning")) != 0) return -1;
        value = cJSON_GetObjectItemCaseSensitive(item, "paused");
        if (!cJSON_IsBool(value)) return -1;
        folder->paused = cJSON_IsTrue(value);
        value = cJSON_GetObjectItemCaseSensitive(item, "pending_rescan");
        if (!cJSON_IsBool(value)) return -1;
        folder->pending_rescan = cJSON_IsTrue(value);
        value = cJSON_GetObjectItemCaseSensitive(item, "local_bytes");
        if (!cJSON_IsNumber(value) || value->valuedouble < 0) return -1;
        folder->local_bytes = (long long)value->valuedouble;
        value = cJSON_GetObjectItemCaseSensitive(item, "global_bytes");
        if (!cJSON_IsNumber(value) || value->valuedouble < 0) return -1;
        folder->global_bytes = (long long)value->valuedouble;
        value = cJSON_GetObjectItemCaseSensitive(item, "peer_count");
        if (!cJSON_IsNumber(value) || value->valuedouble < 0) return -1;
        folder->peer_count = value->valueint;
    }

    status->issue_count = cJSON_GetArraySize(issues);
    for (index = 0; index < status->issue_count; index++) {
        const cJSON *item = cJSON_GetArrayItem(issues, index);
        ls_ui_issue *issue = &status->issues[index];
        if (!cJSON_IsObject(item) ||
            ls_copy_json(issue->code, sizeof(issue->code), cJSON_GetObjectItemCaseSensitive(item, "code")) != 0 ||
            ls_copy_json(issue->message, sizeof(issue->message), cJSON_GetObjectItemCaseSensitive(item, "message")) != 0 ||
            ls_copy_json(issue->scope, sizeof(issue->scope), cJSON_GetObjectItemCaseSensitive(item, "scope")) != 0 ||
            ls_copy_json(issue->subject_id, sizeof(issue->subject_id), cJSON_GetObjectItemCaseSensitive(item, "subject_id")) != 0) return -1;
    }

    status->capability_count = cJSON_GetArraySize(capabilities);
    for (index = 0; index < status->capability_count; index++) {
        if (ls_copy_json(status->capabilities[index], sizeof(status->capabilities[index]),
                         cJSON_GetArrayItem(capabilities, index)) != 0) return -1;
    }
    return 0;
}

int ls_ui_parse_response(const char *payload, size_t payload_size,
                         const char *request_id, ls_ui_status *status,
                         char *error, size_t error_size) {
    const char *parse_end = NULL;
    const cJSON *value;
    cJSON *response;
    int result = -1;
    if (!payload || payload_size == 0 || payload_size > LS_FRAME_MAX ||
        !request_id || !status || memchr(payload, '\0', payload_size)) goto malformed;
    response = cJSON_ParseWithLengthOpts(payload, payload_size, &parse_end, false);
    if (!response || parse_end != payload + payload_size || !cJSON_IsObject(response)) {
        cJSON_Delete(response);
        goto malformed;
    }
    value = cJSON_GetObjectItemCaseSensitive(response, "v");
    if (!cJSON_IsNumber(value) || value->valueint != 1) goto done;
    value = cJSON_GetObjectItemCaseSensitive(response, "id");
    if (!cJSON_IsString(value) || strcmp(value->valuestring, request_id) != 0) goto done;
    value = cJSON_GetObjectItemCaseSensitive(response, "ok");
    if (!cJSON_IsBool(value)) goto done;
    if (!cJSON_IsTrue(value)) {
        const cJSON *failure = cJSON_GetObjectItemCaseSensitive(response, "error");
        const cJSON *message = cJSON_GetObjectItemCaseSensitive(failure, "message");
        if (!cJSON_IsObject(failure) || !cJSON_IsString(message)) goto done;
        ls_error(error, error_size, message->valuestring);
        cJSON_Delete(response);
        return 1;
    }
    if (ls_parse_status(cJSON_GetObjectItemCaseSensitive(response, "result"), status) != 0) goto done;
    if (error && error_size > 0) error[0] = '\0';
    result = 0;
done:
    cJSON_Delete(response);
    if (result == 0) return 0;
malformed:
    ls_error(error, error_size, "Controller returned an invalid response");
    return -1;
}

static int ls_ui_exchange(const char *socket_path,
                          const char *operation,
                          cJSON *arguments,
                          ls_ui_status *status,
                          char *error,
                          size_t error_size) {
    char request_id[64];
    char *encoded = NULL;
    char *response_payload = NULL;
    size_t response_size = 0;
    cJSON *request = NULL;
    int result = -1;

    if (!socket_path || !operation || !arguments || !status) goto done;
    snprintf(request_id, sizeof(request_id), "ui-%u",
             atomic_fetch_add_explicit(&ls_request_sequence, 1u, memory_order_relaxed));
    request = cJSON_CreateObject();
    if (!request || !cJSON_AddNumberToObject(request, "v", 1) ||
        !cJSON_AddStringToObject(request, "id", request_id) ||
        !cJSON_AddStringToObject(request, "op", operation)) goto done;
    cJSON_AddItemToObject(request, "args", arguments);
    arguments = NULL;
    encoded = cJSON_PrintUnformatted(request);
    if (!encoded || ls_frame_request(socket_path, encoded, strlen(encoded),
                                     &response_payload, &response_size, 10000) != 0) {
        ls_error(error, error_size, "Syncthing controller is unavailable");
        goto done;
    }
    result = ls_ui_parse_response(response_payload, response_size, request_id,
                                  status, error, error_size);
done:
    cJSON_Delete(arguments);
    cJSON_Delete(request);
    cJSON_free(encoded);
    free(response_payload);
    return result;
}

int ls_ui_status_get(const char *socket_path, ls_ui_status *status,
                     char *error, size_t error_size) {
    return ls_ui_exchange(socket_path, "status.get", cJSON_CreateObject(),
                          status, error, error_size);
}

int ls_ui_card_enroll(const char *socket_path, const char *source_id,
                      ls_ui_status *status, char *error, size_t error_size) {
    cJSON *arguments = cJSON_CreateObject();
    if (!arguments || !source_id ||
        !cJSON_AddStringToObject(arguments, "source_id", source_id)) {
        cJSON_Delete(arguments);
        ls_error(error, error_size, "Could not create enrollment request");
        return -1;
    }
    return ls_ui_exchange(socket_path, "card.enroll", arguments,
                          status, error, error_size);
}

int ls_ui_network_profile_set(const char *socket_path, const char *profile,
                              ls_ui_status *status, char *error, size_t error_size) {
    cJSON *arguments = cJSON_CreateObject();
    if (!arguments || !profile ||
        (strcmp(profile, "lan-only") != 0 && strcmp(profile, "sync-anywhere") != 0) ||
        !cJSON_AddStringToObject(arguments, "profile", profile) ||
        !cJSON_AddBoolToObject(arguments, "confirmed", true)) {
        cJSON_Delete(arguments);
        ls_error(error, error_size, "Could not create network request");
        return -1;
    }
    return ls_ui_exchange(socket_path, "network.profile.set", arguments,
                          status, error, error_size);
}

bool ls_ui_has_capability(const ls_ui_status *status, const char *operation) {
    int index;
    if (!status || !operation) return false;
    for (index = 0; index < status->capability_count; index++) {
        if (strcmp(status->capabilities[index], operation) == 0) return true;
    }
    return false;
}
