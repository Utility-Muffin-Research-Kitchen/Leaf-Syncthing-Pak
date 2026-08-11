#include "ls_ctl1.h"

#include "cJSON.h"
#include "ls_framed_socket.h"

#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static atomic_uint ls_ctl1_sequence = 1u;

static int ls_ctl1_exchange(const char *socket_path, cJSON *request,
                            const char *request_id, cJSON **out) {
    char *body = NULL;
    char *payload = NULL;
    size_t payload_size = 0;
    const char *parse_end = NULL;
    cJSON *response = NULL;
    const cJSON *value;
    int result = -1;
    body = cJSON_PrintUnformatted(request);
    if (!body || ls_frame_request(socket_path, body, strlen(body),
                                  &payload, &payload_size, 2000) != 0) goto done;
    response = cJSON_ParseWithLengthOpts(payload, payload_size, &parse_end, false);
    if (!response || parse_end != payload + payload_size || !cJSON_IsObject(response)) goto done;
    value = cJSON_GetObjectItemCaseSensitive(response, "v");
    if (!cJSON_IsNumber(value) || value->valueint != 1) goto done;
    value = cJSON_GetObjectItemCaseSensitive(response, "id");
    if (!cJSON_IsString(value) || strcmp(value->valuestring, request_id) != 0) goto done;
    *out = response;
    response = NULL;
    result = 0;
done:
    cJSON_free(body);
    free(payload);
    cJSON_Delete(response);
    return result;
}

static cJSON *ls_ctl1_request(const char *operation, const char *service_id,
                              char request_id[64]) {
    cJSON *request = cJSON_CreateObject();
    snprintf(request_id, 64, "syncthing-ui-%u",
             atomic_fetch_add_explicit(&ls_ctl1_sequence, 1u, memory_order_relaxed));
    if (!request || !cJSON_AddNumberToObject(request, "v", 1) ||
        !cJSON_AddStringToObject(request, "op", operation) ||
        !cJSON_AddStringToObject(request, "id", request_id) ||
        (service_id && !cJSON_AddStringToObject(request, "service_id", service_id))) {
        cJSON_Delete(request);
        return NULL;
    }
    return request;
}

int ls_ctl1_get(const char *socket_path, const char *service_id,
                ls_ctl1_status *status) {
    char request_id[64];
    cJSON *request;
    cJSON *response = NULL;
    const cJSON *services;
    const cJSON *item;
    if (!socket_path || !service_id || !status) return -1;
    memset(status, 0, sizeof(*status));
    request = ls_ctl1_request("list", NULL, request_id);
    if (!request || ls_ctl1_exchange(socket_path, request, request_id, &response) != 0) {
        cJSON_Delete(request);
        return -1;
    }
    cJSON_Delete(request);
    services = cJSON_GetObjectItemCaseSensitive(response, "services");
    if (!cJSON_IsArray(services)) goto fail;
    cJSON_ArrayForEach(item, services) {
        const cJSON *id = cJSON_GetObjectItemCaseSensitive(item, "service_id");
        const cJSON *desired = cJSON_GetObjectItemCaseSensitive(item, "desired_enabled");
        const cJSON *state = cJSON_GetObjectItemCaseSensitive(item, "effective_state");
        size_t state_length;
        if (!cJSON_IsString(id) || !cJSON_IsBool(desired) || !cJSON_IsString(state)) goto fail;
        if (strcmp(id->valuestring, service_id) != 0) continue;
        state_length = strlen(state->valuestring);
        if (state_length >= sizeof(status->effective_state)) goto fail;
        status->found = true;
        status->desired_enabled = cJSON_IsTrue(desired);
        memcpy(status->effective_state, state->valuestring, state_length + 1u);
        break;
    }
    cJSON_Delete(response);
    return 0;
fail:
    cJSON_Delete(response);
    memset(status, 0, sizeof(*status));
    return -1;
}

int ls_ctl1_action(const char *socket_path, const char *operation,
                   const char *service_id, char *error, size_t error_size) {
    char request_id[64];
    cJSON *request;
    cJSON *response = NULL;
    const cJSON *ok;
    int result = -1;
    if (error && error_size > 0) error[0] = '\0';
    request = ls_ctl1_request(operation, service_id, request_id);
    if (!request || ls_ctl1_exchange(socket_path, request, request_id, &response) != 0) {
        if (error && error_size > 0) snprintf(error, error_size, "%s", "Leaf service control is unavailable");
        goto done;
    }
    ok = cJSON_GetObjectItemCaseSensitive(response, "ok");
    if (cJSON_IsTrue(ok)) {
        result = 0;
    } else {
        const cJSON *failure = cJSON_GetObjectItemCaseSensitive(response, "error");
        const cJSON *message = cJSON_GetObjectItemCaseSensitive(failure, "message");
        if (error && error_size > 0) snprintf(error, error_size, "%s",
            cJSON_IsString(message) ? message->valuestring : "Service request failed");
    }
done:
    cJSON_Delete(request);
    cJSON_Delete(response);
    return result;
}

const char *ls_ctl1_state_label(const ls_ctl1_status *status,
                                bool service_control_available,
                                bool controller_available) {
    const char *state;
    if (!service_control_available || !status || !status->found) return "Unavailable";
    state = status->effective_state;
    if (strcmp(state, "disabled") == 0 || strcmp(state, "stopped") == 0) return "Stopped";
    if (strcmp(state, "starting") == 0) return "Starting…";
    if (strcmp(state, "running") == 0) return controller_available ? "Running" : "Starting…";
    if (strcmp(state, "stopping") == 0) return "Stopping…";
    if (strcmp(state, "backoff") == 0) return "Retrying…";
    if (strcmp(state, "unavailable") == 0) return "Unavailable";
    return "Needs attention";
}

const char *ls_ctl1_action_operation(const ls_ctl1_status *status,
                                     bool service_control_available) {
    const char *state;
    if (!service_control_available || !status || !status->found) return NULL;
    state = status->effective_state;
    if (strcmp(state, "disabled") == 0 || strcmp(state, "stopped") == 0 ||
        strcmp(state, "failed") == 0) return "run";
    if (strcmp(state, "starting") == 0 || strcmp(state, "running") == 0 ||
        strcmp(state, "backoff") == 0) return "stop";
    return NULL;
}

const char *ls_ctl1_action_label(const ls_ctl1_status *status,
                                 bool service_control_available) {
    const char *operation = ls_ctl1_action_operation(status, service_control_available);
    if (!operation) return NULL;
    return strcmp(operation, "run") == 0 ? "Run Syncthing" : "Stop Syncthing";
}

bool ls_ctl1_should_query_controller(const ls_ctl1_status *status,
                                     bool service_control_available) {
    return service_control_available && status && status->found &&
           strcmp(status->effective_state, "running") == 0;
}
