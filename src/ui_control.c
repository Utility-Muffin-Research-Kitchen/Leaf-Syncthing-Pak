#include "ls_ui_control.h"

#include "cJSON.h"
#include "ls_framed_socket.h"

#include <stdatomic.h>
#include <limits.h>
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

static int ls_copy_optional_json(char *target, size_t size, const cJSON *value) {
    if (!value) {
        if (!target || size == 0) return -1;
        target[0] = '\0';
        return 0;
    }
    return ls_copy_json(target, size, value);
}

static int ls_nonnegative_int(const cJSON *value, int *target) {
    if (!cJSON_IsNumber(value) || value->valuedouble < 0 || value->valuedouble > INT_MAX) return -1;
    *target = value->valueint;
    return 0;
}

static int ls_optional_nonnegative_int(const cJSON *value, int *target) {
    if (!value) {
        *target = 0;
        return 0;
    }
    return ls_nonnegative_int(value, target);
}

static int ls_optional_nonnegative_bytes(const cJSON *value, long long *target) {
    if (!value) {
        *target = 0;
        return 0;
    }
    if (!cJSON_IsNumber(value) || value->valuedouble < 0) return -1;
    *target = (long long)value->valuedouble;
    return 0;
}

static int ls_parse_status(const cJSON *result, ls_ui_status *status) {
    const cJSON *upstream;
    const cJSON *game;
    const cJSON *recovery;
    const cJSON *network;
    const cJSON *gateway;
    const cJSON *transfer;
    const cJSON *logging;
    const cJSON *storage;
    const cJSON *diagnostics;
    const cJSON *onboarding;
    const cJSON *cards;
    const cJSON *folders;
    const cJSON *peers;
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
    value = cJSON_GetObjectItemCaseSensitive(recovery, "plan_id");
    if (value) {
        const cJSON *remove_paths;
        const cJSON *retained_paths;
        if (ls_copy_json(status->reset_plan_id, sizeof(status->reset_plan_id), value) != 0 ||
            ls_copy_json(status->reset_plan_action, sizeof(status->reset_plan_action),
                         cJSON_GetObjectItemCaseSensitive(recovery, "plan_action")) != 0) return -1;
        remove_paths = cJSON_GetObjectItemCaseSensitive(recovery, "remove_paths");
        retained_paths = cJSON_GetObjectItemCaseSensitive(recovery, "retained_paths");
        if ((remove_paths && (!cJSON_IsArray(remove_paths) || cJSON_GetArraySize(remove_paths) > LS_UI_MAX_RESET_PATHS)) ||
            (retained_paths && (!cJSON_IsArray(retained_paths) || cJSON_GetArraySize(retained_paths) > LS_UI_MAX_RESET_PATHS))) return -1;
        if (remove_paths) {
            status->reset_remove_count = cJSON_GetArraySize(remove_paths);
            for (index = 0; index < status->reset_remove_count; index++) {
                if (ls_copy_json(status->reset_remove_paths[index], sizeof(status->reset_remove_paths[index]),
                                 cJSON_GetArrayItem(remove_paths, index)) != 0) return -1;
            }
        }
        if (retained_paths) {
            status->reset_retained_count = cJSON_GetArraySize(retained_paths);
            for (index = 0; index < status->reset_retained_count; index++) {
                if (ls_copy_json(status->reset_retained_paths[index], sizeof(status->reset_retained_paths[index]),
                                 cJSON_GetArrayItem(retained_paths, index)) != 0) return -1;
            }
        }
    }

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
    gateway = cJSON_GetObjectItemCaseSensitive(result, "gateway");
    if (gateway) {
        if (!cJSON_IsObject(gateway) ||
            ls_copy_json(status->gateway_url, sizeof(status->gateway_url),
                         cJSON_GetObjectItemCaseSensitive(gateway, "url")) != 0 ||
            ls_copy_json(status->gateway_pin, sizeof(status->gateway_pin),
                         cJSON_GetObjectItemCaseSensitive(gateway, "pin")) != 0 ||
            ls_copy_json(status->gateway_qr_url, sizeof(status->gateway_qr_url),
                         cJSON_GetObjectItemCaseSensitive(gateway, "qr_url")) != 0 ||
            ls_copy_json(status->gateway_offer_expires, sizeof(status->gateway_offer_expires),
                         cJSON_GetObjectItemCaseSensitive(gateway, "offer_expires")) != 0 ||
            ls_copy_json(status->gateway_fingerprint, sizeof(status->gateway_fingerprint),
                         cJSON_GetObjectItemCaseSensitive(gateway, "fingerprint")) != 0 ||
            ls_copy_json(status->gateway_extension_expires, sizeof(status->gateway_extension_expires),
                         cJSON_GetObjectItemCaseSensitive(gateway, "extension_expires")) != 0) return -1;
        value = cJSON_GetObjectItemCaseSensitive(gateway, "open");
        if (!cJSON_IsBool(value)) return -1;
        status->gateway_open = cJSON_IsTrue(value);
        value = cJSON_GetObjectItemCaseSensitive(gateway, "pairing");
        if (!cJSON_IsBool(value)) return -1;
        status->gateway_pairing = cJSON_IsTrue(value);
        value = cJSON_GetObjectItemCaseSensitive(gateway, "trusted_browsers");
        if (!cJSON_IsNumber(value) || value->valueint < 0 || value->valueint > 32) return -1;
        status->gateway_trusted_browsers = value->valueint;
        status->gateway_present = true;
    }

    transfer = cJSON_GetObjectItemCaseSensitive(result, "transfer");
    if (transfer) {
        if (!cJSON_IsObject(transfer) ||
            ls_copy_json(status->transfer_state, sizeof(status->transfer_state),
                         cJSON_GetObjectItemCaseSensitive(transfer, "state")) != 0) return -1;
        value = cJSON_GetObjectItemCaseSensitive(transfer, "local_bytes");
        if (!cJSON_IsNumber(value) || value->valuedouble < 0) return -1;
        status->transfer_local_bytes = (long long)value->valuedouble;
        value = cJSON_GetObjectItemCaseSensitive(transfer, "global_bytes");
        if (!cJSON_IsNumber(value) || value->valuedouble < 0) return -1;
        status->transfer_global_bytes = (long long)value->valuedouble;
        value = cJSON_GetObjectItemCaseSensitive(transfer, "need_bytes");
        if (!cJSON_IsNumber(value) || value->valuedouble < 0) return -1;
        status->transfer_need_bytes = (long long)value->valuedouble;
        value = cJSON_GetObjectItemCaseSensitive(transfer, "in_bytes");
        if (!cJSON_IsNumber(value) || value->valuedouble < 0) return -1;
        status->transfer_in_bytes = (long long)value->valuedouble;
        value = cJSON_GetObjectItemCaseSensitive(transfer, "out_bytes");
        if (!cJSON_IsNumber(value) || value->valuedouble < 0) return -1;
        status->transfer_out_bytes = (long long)value->valuedouble;
        status->transfer_present = true;
    }

    logging = cJSON_GetObjectItemCaseSensitive(result, "logging");
    if (logging) {
        if (!cJSON_IsObject(logging) ||
            ls_copy_json(status->log_level, sizeof(status->log_level),
                         cJSON_GetObjectItemCaseSensitive(logging, "level")) != 0 ||
            ls_copy_json(status->debug_expires, sizeof(status->debug_expires),
                         cJSON_GetObjectItemCaseSensitive(logging, "debug_expires")) != 0) return -1;
        status->logging_present = true;
    }

    storage = cJSON_GetObjectItemCaseSensitive(result, "storage");
    if (storage) {
        const cJSON *inventory;
        if (!cJSON_IsObject(storage)) return -1;
        value = cJSON_GetObjectItemCaseSensitive(storage, "snapshot_bytes");
        if (!cJSON_IsNumber(value) || value->valuedouble < 0) return -1;
        status->snapshot_bytes = (long long)value->valuedouble;
        value = cJSON_GetObjectItemCaseSensitive(storage, "version_bytes");
        if (!cJSON_IsNumber(value) || value->valuedouble < 0) return -1;
        status->version_bytes = (long long)value->valuedouble;
        value = cJSON_GetObjectItemCaseSensitive(storage, "snapshot_count");
        if (!cJSON_IsNumber(value) || value->valueint < 0) return -1;
        status->snapshot_count = value->valueint;
        value = cJSON_GetObjectItemCaseSensitive(storage, "version_groups");
        if (!cJSON_IsNumber(value) || value->valueint < 0) return -1;
        status->version_groups = value->valueint;
        inventory = cJSON_GetObjectItemCaseSensitive(storage, "inventory");
        if (!cJSON_IsArray(inventory) || cJSON_GetArraySize(inventory) > LS_UI_MAX_STORAGE_ROWS) return -1;
        status->storage_row_count = cJSON_GetArraySize(inventory);
        for (index = 0; index < status->storage_row_count; index++) {
            const cJSON *item = cJSON_GetArrayItem(inventory, index);
            ls_ui_storage_row *row = &status->storage_rows[index];
            if (!cJSON_IsObject(item) ||
                ls_copy_json(row->card_suffix, sizeof(row->card_suffix), cJSON_GetObjectItemCaseSensitive(item, "card_suffix")) != 0 ||
                ls_copy_json(row->category, sizeof(row->category), cJSON_GetObjectItemCaseSensitive(item, "category")) != 0 ||
                ls_copy_json(row->kind, sizeof(row->kind), cJSON_GetObjectItemCaseSensitive(item, "kind")) != 0 ||
                ls_copy_json(row->name, sizeof(row->name), cJSON_GetObjectItemCaseSensitive(item, "name")) != 0) return -1;
            value = cJSON_GetObjectItemCaseSensitive(item, "bytes");
            if (!cJSON_IsNumber(value) || value->valuedouble < 0) return -1;
            row->bytes = (long long)value->valuedouble;
        }
        status->storage_present = true;
    }

    diagnostics = cJSON_GetObjectItemCaseSensitive(result, "diagnostics");
    if (diagnostics) {
        if (!cJSON_IsObject(diagnostics) ||
            ls_copy_json(status->diagnostics_path, sizeof(status->diagnostics_path),
                         cJSON_GetObjectItemCaseSensitive(diagnostics, "last_export_path")) != 0 ||
            ls_copy_json(status->diagnostics_exported, sizeof(status->diagnostics_exported),
                         cJSON_GetObjectItemCaseSensitive(diagnostics, "last_exported")) != 0) return -1;
    }

    onboarding = cJSON_GetObjectItemCaseSensitive(result, "onboarding");
    if (onboarding) {
        ls_ui_onboarding *plan = &status->onboarding;
        if (!cJSON_IsObject(onboarding) ||
            ls_copy_json(plan->plan_id, sizeof(plan->plan_id), cJSON_GetObjectItemCaseSensitive(onboarding, "plan_id")) != 0 ||
            strlen(plan->plan_id) != 32u ||
            ls_copy_json(plan->source_id, sizeof(plan->source_id), cJSON_GetObjectItemCaseSensitive(onboarding, "source_id")) != 0 ||
            ls_copy_json(plan->card_id, sizeof(plan->card_id), cJSON_GetObjectItemCaseSensitive(onboarding, "card_id")) != 0 ||
            ls_copy_json(plan->kind, sizeof(plan->kind), cJSON_GetObjectItemCaseSensitive(onboarding, "kind")) != 0 ||
            ls_copy_json(plan->folder_type, sizeof(plan->folder_type), cJSON_GetObjectItemCaseSensitive(onboarding, "folder_type")) != 0 ||
            ls_copy_json(plan->folder_id, sizeof(plan->folder_id), cJSON_GetObjectItemCaseSensitive(onboarding, "folder_id")) != 0 ||
            ls_copy_json(plan->label, sizeof(plan->label), cJSON_GetObjectItemCaseSensitive(onboarding, "label")) != 0 ||
            ls_copy_json(plan->path, sizeof(plan->path), cJSON_GetObjectItemCaseSensitive(onboarding, "path")) != 0 ||
            ls_copy_json(plan->expires_at, sizeof(plan->expires_at), cJSON_GetObjectItemCaseSensitive(onboarding, "expires_at")) != 0 ||
            ls_nonnegative_int(cJSON_GetObjectItemCaseSensitive(onboarding, "file_count"), &plan->file_count) != 0 ||
            ls_nonnegative_int(cJSON_GetObjectItemCaseSensitive(onboarding, "directory_count"), &plan->directory_count) != 0 ||
            ls_optional_nonnegative_bytes(cJSON_GetObjectItemCaseSensitive(onboarding, "content_bytes"), &plan->content_bytes) != 0 ||
            ls_optional_nonnegative_bytes(cJSON_GetObjectItemCaseSensitive(onboarding, "available_bytes"), &plan->available_bytes) != 0 ||
            ls_nonnegative_int(cJSON_GetObjectItemCaseSensitive(onboarding, "peer_count"), &plan->peer_count) != 0 ||
            plan->peer_count < 1) return -1;
        value = cJSON_GetObjectItemCaseSensitive(onboarding, "snapshot_possible");
        if (!cJSON_IsBool(value)) return -1;
        plan->snapshot_possible = cJSON_IsTrue(value);
        value = cJSON_GetObjectItemCaseSensitive(onboarding, "states_warning");
        if (!cJSON_IsBool(value)) return -1;
        plan->states_warning = cJSON_IsTrue(value);
        status->onboarding_present = true;
    }

    cards = cJSON_GetObjectItemCaseSensitive(result, "cards");
    folders = cJSON_GetObjectItemCaseSensitive(result, "folders");
    peers = cJSON_GetObjectItemCaseSensitive(result, "peers");
    issues = cJSON_GetObjectItemCaseSensitive(result, "issues");
    capabilities = cJSON_GetObjectItemCaseSensitive(result, "capabilities");
    if (!cJSON_IsArray(cards) || cJSON_GetArraySize(cards) > LS_UI_MAX_CARDS ||
        !cJSON_IsArray(folders) || cJSON_GetArraySize(folders) > LS_UI_MAX_FOLDERS ||
        (peers && (!cJSON_IsArray(peers) || cJSON_GetArraySize(peers) > LS_UI_MAX_PEERS)) ||
        !cJSON_IsArray(issues) || cJSON_GetArraySize(issues) > LS_UI_MAX_ISSUES ||
        !cJSON_IsArray(capabilities) || cJSON_GetArraySize(capabilities) > LS_UI_MAX_CAPABILITIES) return -1;

    status->card_count = cJSON_GetArraySize(cards);
    for (index = 0; index < status->card_count; index++) {
        const cJSON *item = cJSON_GetArrayItem(cards, index);
        ls_ui_card *card = &status->cards[index];
        if (!cJSON_IsObject(item) ||
            ls_copy_json(card->id, sizeof(card->id), cJSON_GetObjectItemCaseSensitive(item, "id")) != 0 ||
            ls_copy_optional_json(card->source_id, sizeof(card->source_id), cJSON_GetObjectItemCaseSensitive(item, "source_id")) != 0 ||
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

    if (peers) {
        status->peer_count = cJSON_GetArraySize(peers);
        for (index = 0; index < status->peer_count; index++) {
            const cJSON *item = cJSON_GetArrayItem(peers, index);
            ls_ui_peer *peer = &status->peers[index];
            if (!cJSON_IsObject(item) ||
                ls_copy_json(peer->id, sizeof(peer->id), cJSON_GetObjectItemCaseSensitive(item, "id")) != 0 ||
                ls_copy_json(peer->id_suffix, sizeof(peer->id_suffix), cJSON_GetObjectItemCaseSensitive(item, "id_suffix")) != 0 ||
                ls_copy_json(peer->name, sizeof(peer->name), cJSON_GetObjectItemCaseSensitive(item, "name")) != 0 ||
                ls_copy_json(peer->state, sizeof(peer->state), cJSON_GetObjectItemCaseSensitive(item, "state")) != 0 ||
                ls_copy_json(peer->connection, sizeof(peer->connection), cJSON_GetObjectItemCaseSensitive(item, "connection")) != 0 ||
                ls_copy_json(peer->address, sizeof(peer->address), cJSON_GetObjectItemCaseSensitive(item, "address")) != 0) return -1;
            value = cJSON_GetObjectItemCaseSensitive(item, "paused");
            if (!cJSON_IsBool(value)) return -1;
            peer->paused = cJSON_IsTrue(value);
            value = cJSON_GetObjectItemCaseSensitive(item, "introducer");
            if (!cJSON_IsBool(value)) return -1;
            peer->introducer = cJSON_IsTrue(value);
            value = cJSON_GetObjectItemCaseSensitive(item, "pending");
            if (!cJSON_IsBool(value)) return -1;
            peer->pending = cJSON_IsTrue(value);
        }
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
            ls_copy_json(folder->versioning, sizeof(folder->versioning), cJSON_GetObjectItemCaseSensitive(item, "versioning")) != 0 ||
            ls_copy_optional_json(folder->first_sync_state, sizeof(folder->first_sync_state), cJSON_GetObjectItemCaseSensitive(item, "first_sync_state")) != 0 ||
            ls_copy_optional_json(folder->snapshot_name, sizeof(folder->snapshot_name), cJSON_GetObjectItemCaseSensitive(item, "snapshot_name")) != 0 ||
            ls_copy_optional_json(folder->first_sync_message, sizeof(folder->first_sync_message), cJSON_GetObjectItemCaseSensitive(item, "first_sync_message")) != 0) return -1;
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
        if (ls_optional_nonnegative_int(cJSON_GetObjectItemCaseSensitive(item, "local_items"), &folder->local_items) != 0 ||
            ls_optional_nonnegative_int(cJSON_GetObjectItemCaseSensitive(item, "global_items"), &folder->global_items) != 0 ||
            ls_optional_nonnegative_int(cJSON_GetObjectItemCaseSensitive(item, "snapshot_files"), &folder->snapshot_files) != 0 ||
            ls_optional_nonnegative_int(cJSON_GetObjectItemCaseSensitive(item, "snapshot_directories"), &folder->snapshot_directories) != 0 ||
            ls_optional_nonnegative_bytes(cJSON_GetObjectItemCaseSensitive(item, "snapshot_bytes"), &folder->snapshot_bytes) != 0) return -1;
        value = cJSON_GetObjectItemCaseSensitive(item, "peer_count");
        if (!cJSON_IsNumber(value) || value->valuedouble < 0) return -1;
        folder->peer_count = value->valueint;
        value = cJSON_GetObjectItemCaseSensitive(item, "conflict_count");
        if (value) {
            if (!cJSON_IsNumber(value) || value->valueint < 0) return -1;
            folder->conflict_count = value->valueint;
        }
        value = cJSON_GetObjectItemCaseSensitive(item, "conflicts");
        if (value) {
            if (!cJSON_IsArray(value) || cJSON_GetArraySize(value) > LS_UI_MAX_CONFLICTS) return -1;
            folder->conflict_path_count = cJSON_GetArraySize(value);
            if (folder->conflict_count < folder->conflict_path_count) return -1;
            for (int conflict = 0; conflict < folder->conflict_path_count; conflict++) {
                if (ls_copy_json(folder->conflicts[conflict], sizeof(folder->conflicts[conflict]),
                                 cJSON_GetArrayItem(value, conflict)) != 0) return -1;
            }
        }
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

static int ls_ui_exchange_timeout(const char *socket_path,
                                  const char *operation,
                                  cJSON *arguments,
                                  ls_ui_status *status,
                                  char *error,
                                  size_t error_size,
                                  int timeout_ms) {
    char request_id[64];
    char *encoded = NULL;
    char *response_payload = NULL;
    size_t response_size = 0;
    cJSON *request = NULL;
    int result = -1;

    if (!socket_path || !operation || !arguments || !status || timeout_ms <= 0) goto done;
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
                                     &response_payload, &response_size, timeout_ms) != 0) {
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

static int ls_ui_exchange(const char *socket_path,
                          const char *operation,
                          cJSON *arguments,
                          ls_ui_status *status,
                          char *error,
                          size_t error_size) {
    return ls_ui_exchange_timeout(socket_path, operation, arguments, status,
                                  error, error_size, 10000);
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

int ls_ui_gateway_action(const char *socket_path, const char *operation,
                         bool confirmed, ls_ui_status *status,
                         char *error, size_t error_size) {
    cJSON *arguments = cJSON_CreateObject();
    bool requires_confirmation;
    if (!operation || !arguments) goto invalid;
    requires_confirmation = strcmp(operation, "gateway.extend") == 0 ||
                            strcmp(operation, "gateway.revoke-all") == 0;
    if (strcmp(operation, "gateway.open") != 0 &&
        strcmp(operation, "gateway.keepalive") != 0 &&
        strcmp(operation, "gateway.close") != 0 && !requires_confirmation) goto invalid;
    if (requires_confirmation && !cJSON_AddBoolToObject(arguments, "confirmed", confirmed)) goto invalid;
    return ls_ui_exchange(socket_path, operation, arguments, status, error, error_size);
invalid:
    cJSON_Delete(arguments);
    ls_error(error, error_size, "Could not create web interface request");
    return -1;
}

int ls_ui_folder_action(const char *socket_path, const char *operation,
                        const char *folder_id, const char *label,
                        ls_ui_status *status, char *error, size_t error_size) {
    cJSON *arguments = cJSON_CreateObject();
    bool rename;
    if (!operation || !folder_id || !arguments) goto invalid;
    rename = strcmp(operation, "folder.rename") == 0;
    if (strcmp(operation, "folder.pause") != 0 &&
        strcmp(operation, "folder.resume") != 0 &&
        strcmp(operation, "folder.rescan") != 0 &&
        strcmp(operation, "folder.inspect") != 0 && !rename) goto invalid;
    if (!cJSON_AddStringToObject(arguments, "folder_id", folder_id) ||
        (rename && (!label || !cJSON_AddStringToObject(arguments, "label", label)))) goto invalid;
    return ls_ui_exchange(socket_path, operation, arguments, status, error, error_size);
invalid:
    cJSON_Delete(arguments);
    ls_error(error, error_size, "Could not create folder request");
    return -1;
}

static bool ls_ui_valid_folder_type(const char *folder_type) {
    return folder_type && (strcmp(folder_type, "sendonly") == 0 ||
        strcmp(folder_type, "sendreceive") == 0 || strcmp(folder_type, "receiveonly") == 0);
}

int ls_ui_folder_onboard_plan(const char *socket_path, const char *source_id,
                              const char *kind, const char *folder_type,
                              ls_ui_status *status, char *error, size_t error_size) {
    cJSON *arguments = cJSON_CreateObject();
    if (!arguments || !source_id || !kind ||
        (strcmp(kind, "saves") != 0 && strcmp(kind, "states") != 0) ||
        !ls_ui_valid_folder_type(folder_type) ||
        !cJSON_AddStringToObject(arguments, "source_id", source_id) ||
        !cJSON_AddStringToObject(arguments, "kind", kind) ||
        !cJSON_AddStringToObject(arguments, "folder_type", folder_type)) goto invalid;
    return ls_ui_exchange(socket_path, "folder.onboard.plan", arguments, status, error, error_size);
invalid:
    cJSON_Delete(arguments);
    ls_error(error, error_size, "Could not create folder review request");
    return -1;
}

int ls_ui_folder_onboard_create(const char *socket_path, const char *plan_id,
                                bool states_warning_acknowledged,
                                bool manual_edit_warning_acknowledged,
                                ls_ui_status *status, char *error, size_t error_size) {
    cJSON *arguments = cJSON_CreateObject();
    if (!arguments || !plan_id || strlen(plan_id) != 32u ||
        !manual_edit_warning_acknowledged ||
        !cJSON_AddStringToObject(arguments, "plan_id", plan_id) ||
        !cJSON_AddBoolToObject(arguments, "confirmed", true) ||
        !cJSON_AddBoolToObject(arguments, "states_warning_acknowledged", states_warning_acknowledged) ||
        !cJSON_AddBoolToObject(arguments, "manual_edit_warning_acknowledged", manual_edit_warning_acknowledged)) goto invalid;
    return ls_ui_exchange(socket_path, "folder.onboard.create", arguments, status, error, error_size);
invalid:
    cJSON_Delete(arguments);
    ls_error(error, error_size, "Could not create managed folder request");
    return -1;
}

int ls_ui_folder_first_sync_prepare(const char *socket_path, const char *folder_id,
                                    ls_ui_status *status, char *error, size_t error_size) {
    cJSON *arguments = cJSON_CreateObject();
    if (!arguments || !folder_id ||
        !cJSON_AddStringToObject(arguments, "folder_id", folder_id) ||
        !cJSON_AddBoolToObject(arguments, "confirmed", true) ||
        !cJSON_AddBoolToObject(arguments, "snapshot_limit_acknowledged", true)) goto invalid;
    return ls_ui_exchange_timeout(socket_path, "folder.first-sync.prepare", arguments,
                                  status, error, error_size, 30 * 60 * 1000);
invalid:
    cJSON_Delete(arguments);
    ls_error(error, error_size, "Could not create first-sync preparation request");
    return -1;
}

int ls_ui_folder_first_sync_start(const char *socket_path, const char *folder_id,
                                  ls_ui_status *status, char *error, size_t error_size) {
    cJSON *arguments = cJSON_CreateObject();
    if (!arguments || !folder_id ||
        !cJSON_AddStringToObject(arguments, "folder_id", folder_id) ||
        !cJSON_AddBoolToObject(arguments, "confirmed", true) ||
        !cJSON_AddBoolToObject(arguments, "hub_versioning_acknowledged", true)) goto invalid;
    return ls_ui_exchange(socket_path, "folder.first-sync.start", arguments, status, error, error_size);
invalid:
    cJSON_Delete(arguments);
    ls_error(error, error_size, "Could not create first-sync start request");
    return -1;
}

int ls_ui_folder_type_set(const char *socket_path, const char *folder_id,
                          const char *folder_type, ls_ui_status *status,
                          char *error, size_t error_size) {
    cJSON *arguments = cJSON_CreateObject();
    if (!arguments || !folder_id || !ls_ui_valid_folder_type(folder_type) ||
        !cJSON_AddStringToObject(arguments, "folder_id", folder_id) ||
        !cJSON_AddStringToObject(arguments, "folder_type", folder_type) ||
        !cJSON_AddBoolToObject(arguments, "confirmed", true)) goto invalid;
    return ls_ui_exchange(socket_path, "folder.type.set", arguments, status, error, error_size);
invalid:
    cJSON_Delete(arguments);
    ls_error(error, error_size, "Could not create folder direction request");
    return -1;
}

int ls_ui_device_action(const char *socket_path, const char *operation,
                        const char *device_id, const char *name,
                        ls_ui_status *status, char *error, size_t error_size) {
    cJSON *arguments = cJSON_CreateObject();
    if (!operation || !device_id || !name || !arguments ||
        (strcmp(operation, "device.add") != 0 && strcmp(operation, "device.rename") != 0) ||
        !cJSON_AddStringToObject(arguments, "device_id", device_id) ||
        !cJSON_AddStringToObject(arguments, "name", name)) goto invalid;
    return ls_ui_exchange(socket_path, operation, arguments, status, error, error_size);
invalid:
    cJSON_Delete(arguments);
    ls_error(error, error_size, "Could not create device request");
    return -1;
}

int ls_ui_reset_prepare(const char *socket_path, const char *action,
                        const char *confirmation, ls_ui_status *status,
                        char *error, size_t error_size) {
    cJSON *arguments = cJSON_CreateObject();
    if (!action || !confirmation || !arguments ||
        (strcmp(action, "index-only") != 0 && strcmp(action, "full") != 0 &&
         strcmp(action, "available-only") != 0) ||
        !cJSON_AddStringToObject(arguments, "action", action) ||
        !cJSON_AddBoolToObject(arguments, "confirmed", true) ||
        !cJSON_AddStringToObject(arguments, "confirmation", confirmation)) goto invalid;
    return ls_ui_exchange(socket_path, "reset.prepare", arguments, status, error, error_size);
invalid:
    cJSON_Delete(arguments);
    ls_error(error, error_size, "Could not create reset request");
    return -1;
}

int ls_ui_log_level_set(const char *socket_path, const char *level,
                        ls_ui_status *status, char *error, size_t error_size) {
    cJSON *arguments = cJSON_CreateObject();
    if (!level || !arguments || (strcmp(level, "normal") != 0 && strcmp(level, "debug") != 0) ||
        !cJSON_AddStringToObject(arguments, "level", level) ||
        !cJSON_AddBoolToObject(arguments, "confirmed", true)) goto invalid;
    return ls_ui_exchange(socket_path, "log.level.set", arguments, status, error, error_size);
invalid:
    cJSON_Delete(arguments);
    ls_error(error, error_size, "Could not create logging request");
    return -1;
}

int ls_ui_diagnostics_export(const char *socket_path, ls_ui_status *status,
                             char *error, size_t error_size) {
    return ls_ui_exchange(socket_path, "diagnostics.export", cJSON_CreateObject(),
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
