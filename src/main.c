#define CAT_IMPLEMENTATION
#include "catastrophe.h"
#define CAT_WIDGETS_IMPLEMENTATION
#include "catastrophe_widgets.h"

#include "ls_ctl1.h"
#include "ls_ui_control.h"
#include "qrcodegen.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>

#define LS_SERVICE_ID "org.umrk.syncthing"
#define LS_QR_MAX_VERSION 12

typedef struct {
    char control_socket[1024];
    char daemon_socket[1024];
    char controller_binary[1024];
    ls_ctl1_status service;
    ls_ui_status status;
    int controller_available;
    char error[256];
} ls_app;

static void ls_show_folder_actions(ls_app *app, const char *folder_id);
static void ls_show_devices(ls_app *app);
static void ls_show_recovery(ls_app *app);
static void ls_show_settings(ls_app *app);

static void ls_message(const char *message) {
    cat_footer_item footer[] = {{.button = CAT_BTN_A, .label = "OK", .is_confirm = true}};
    cat_message_opts options = {
        .message = message ? message : "Request failed",
        .footer = footer,
        .footer_count = 1,
    };
    cat_confirm_result result = {0};
    (void)cat_confirmation(&options, &result);
}

static int ls_confirm(const char *message, const char *label) {
    cat_footer_item footer[] = {{.button = CAT_BTN_B, .label = "Cancel"},
                                {.button = CAT_BTN_A, .label = label, .is_confirm = true}};
    cat_message_opts options = {.message = message, .footer = footer, .footer_count = 2};
    cat_confirm_result result = {0};
    (void)cat_confirmation(&options, &result);
    return result.confirmed ? 1 : 0;
}

static int ls_join_path(char *target, size_t size, const char *left, const char *right) {
    int count;
    if (!target || !left || !right || !left[0] || left[0] != '/') return -1;
    count = snprintf(target, size, "%s/%s", left, right);
    return count > 0 && (size_t)count < size ? 0 : -1;
}

static int ls_load_paths(ls_app *app) {
    const char *runtime = getenv("UMRK_RUNTIME_PATH");
    const char *daemon = getenv("UMRK_DAEMON_SOCKET");
    if (!daemon || !daemon[0]) daemon = getenv("JAWAKA_SOCKET_PATH");
    if (!runtime || !runtime[0]) runtime = "/tmp/jawaka-runtime";
    char executable[1024];
    char *separator;
    ssize_t executable_length;
    if (ls_join_path(app->control_socket, sizeof(app->control_socket), runtime,
                     "services/org.umrk.syncthing/control.sock") != 0) return -1;
    if (daemon && daemon[0]) {
        if (snprintf(app->daemon_socket, sizeof(app->daemon_socket), "%s", daemon) >=
            (int)sizeof(app->daemon_socket)) return -1;
    } else if (ls_join_path(app->daemon_socket, sizeof(app->daemon_socket), runtime,
                            "jawakad.sock") != 0) return -1;
    executable_length = readlink("/proc/self/exe", executable, sizeof(executable) - 1u);
    if (executable_length <= 0 || (size_t)executable_length >= sizeof(executable) - 1u) return -1;
    executable[executable_length] = '\0';
    separator = strrchr(executable, '/');
    if (!separator) return -1;
    *separator = '\0';
    if (snprintf(app->controller_binary, sizeof(app->controller_binary), "%s/leaf-syncthing", executable) >=
        (int)sizeof(app->controller_binary)) return -1;
    return 0;
}

static void ls_refresh(ls_app *app) {
    app->error[0] = '\0';
    (void)ls_ctl1_get(app->daemon_socket, LS_SERVICE_ID, &app->service);
    app->controller_available =
        ls_ui_status_get(app->control_socket, &app->status,
                         app->error, sizeof(app->error)) == 0;
}

static void ls_format_bytes(long long bytes, char *target, size_t size) {
    static const char *units[] = {"B", "KiB", "MiB", "GiB", "TiB"};
    double value = (double)bytes;
    int unit = 0;
    while (value >= 1024.0 && unit < 4) {
        value /= 1024.0;
        unit++;
    }
    if (unit == 0) snprintf(target, size, "%lld %s", bytes, units[unit]);
    else snprintf(target, size, "%.1f %s", value, units[unit]);
}

static void ls_show_cards(ls_app *app) {
    cat_options_item items[LS_UI_MAX_CARDS];
    cat_option item_options[LS_UI_MAX_CARDS];
    char values[LS_UI_MAX_CARDS][96];
    cat_footer_item footer[] = {
        {.button = CAT_BTN_B, .label = "Back"},
        {.button = CAT_BTN_A, .label = "Enroll", .is_confirm = true},
    };
    int focus = 0;
    int scroll = 0;
    for (;;) {
        cat_options_list_opts options = {0};
        cat_options_list_result result = {0};
        int index;
        int action;
        ls_refresh(app);
        if (!app->controller_available) {
            ls_message(app->error);
            return;
        }
        if (app->status.card_count == 0) {
            ls_message("No configured card slots are available.");
            return;
        }
        memset(items, 0, sizeof(items));
        memset(item_options, 0, sizeof(item_options));
        for (index = 0; index < app->status.card_count; index++) {
            ls_ui_card *card = &app->status.cards[index];
            snprintf(values[index], sizeof(values[index]), "%s%s%s",
                     card->state,
                     card->id_suffix[0] ? " · " : "",
                     card->id_suffix);
            items[index].label = card->slot;
            items[index].type = CAT_OPT_CLICKABLE;
            item_options[index] = (cat_option){.label = values[index], .value = values[index]};
            items[index].options = &item_options[index];
            items[index].option_count = 1;
        }
        options.title = "Cards";
        options.items = items;
        options.item_count = app->status.card_count;
        options.footer = footer;
        options.footer_count = cat_hints_enabled_from_env() ? 2 : 0;
        options.initial_selected_index = focus;
        options.visible_start_index = scroll;
        action = cat_options_list(&options, &result);
        focus = result.focused_index;
        scroll = result.visible_start_index;
        if (action == CAT_CANCELLED || result.action == CAT_ACTION_BACK) return;
        if (result.action == CAT_ACTION_SELECTED && focus >= 0 && focus < app->status.card_count) {
            ls_ui_card *card = &app->status.cards[focus];
            const char *source = strncmp(card->id, "source:", 7) == 0 ? card->id + 7 : NULL;
            if (card->enrolled) {
                char detail[768];
                char retained[32];
                ls_format_bytes(card->retained_bytes, retained, sizeof(retained));
                snprintf(detail, sizeof(detail), "%s\nState: %s\nIdentity: ...%s\nPath: %s\nRetained: %s",
                         card->slot, card->state, card->id_suffix, card->root, retained);
                ls_message(detail);
            } else if (!source || !ls_ui_has_capability(&app->status, "card.enroll")) {
                ls_message("This card cannot be enrolled in the current state.");
            } else if (ls_ui_card_enroll(app->control_socket, source, &app->status,
                                         app->error, sizeof(app->error)) != 0) {
                ls_message(app->error);
            }
        }
    }
}

static void ls_show_folders(ls_app *app) {
    cat_options_item items[LS_UI_MAX_FOLDERS];
    cat_option item_options[LS_UI_MAX_FOLDERS];
    char values[LS_UI_MAX_FOLDERS][128];
    cat_footer_item footer[] = {{.button = CAT_BTN_B, .label = "Back"},
                                {.button = CAT_BTN_A, .label = "Details", .is_confirm = true}};
    cat_options_list_opts options = {0};
    cat_options_list_result result = {0};
    int index;
    ls_refresh(app);
    if (!app->controller_available) {
        ls_message(app->error);
        return;
    }
    if (app->status.folder_count == 0) {
        ls_message("No Syncthing folders are configured yet. Guided setup arrives in the next phase.");
        return;
    }
    memset(items, 0, sizeof(items));
    memset(item_options, 0, sizeof(item_options));
    for (index = 0; index < app->status.folder_count; index++) {
        ls_ui_folder *folder = &app->status.folders[index];
        snprintf(values[index], sizeof(values[index]), "%s · %s%s", folder->state, folder->type,
                 folder->pending_rescan ? " · rescan pending" : "");
        items[index].label = folder->label;
        items[index].type = CAT_OPT_CLICKABLE;
        item_options[index] = (cat_option){.label = values[index], .value = values[index]};
        items[index].options = &item_options[index];
        items[index].option_count = 1;
    }
    options.title = "Folders";
    options.items = items;
    options.item_count = app->status.folder_count;
    options.footer = footer;
    options.footer_count = cat_hints_enabled_from_env() ? 2 : 0;
    if (cat_options_list(&options, &result) != CAT_CANCELLED && result.action == CAT_ACTION_SELECTED &&
        result.focused_index >= 0 && result.focused_index < app->status.folder_count) {
        ls_ui_folder *folder = &app->status.folders[result.focused_index];
        char folder_id[sizeof(folder->id)];
        snprintf(folder_id, sizeof(folder_id), "%s", folder->id);
        ls_show_folder_actions(app, folder_id);
    }
}

static void ls_show_issues(ls_app *app) {
    char message[2048];
    size_t used = 0;
    int index;
    if (app->status.issue_count == 0) {
        ls_message("No Syncthing issues are currently reported.");
        return;
    }
    message[0] = '\0';
    for (index = 0; index < app->status.issue_count; index++) {
        int count = snprintf(message + used, sizeof(message) - used, "%s%s",
                             index ? "\n\n" : "", app->status.issues[index].message);
        if (count < 0 || (size_t)count >= sizeof(message) - used) break;
        used += (size_t)count;
    }
    ls_message(message);
}

static int ls_service_running(const ls_ctl1_status *status) {
    return status->found &&
        (strcmp(status->effective_state, "running") == 0 ||
         strcmp(status->effective_state, "starting") == 0 ||
         strcmp(status->effective_state, "backoff") == 0);
}

static void ls_change_network(ls_app *app) {
    const char *next;
    const char *message;
    cat_footer_item footer[] = {{.button = CAT_BTN_B, .label = "Cancel"},
                                {.button = CAT_BTN_A, .label = "Change", .is_confirm = true}};
    cat_message_opts options;
    cat_confirm_result result = {0};
    if (!app->status.network_present ||
        !ls_ui_has_capability(&app->status, "network.profile.set")) {
        ls_message("Network profile control is unavailable.");
        return;
    }
    if (strcmp(app->status.network_profile, "lan-only") == 0) {
        next = "sync-anywhere";
        message = "Enable Sync Anywhere? Syncthing may contact internet discovery and relay services, use more radio and battery, and ask your router for port mappings.";
    } else {
        next = "lan-only";
        message = "Restore LAN-only? Connections outside directly connected networks will be closed immediately.";
    }
    options = (cat_message_opts){.message = message, .footer = footer, .footer_count = 2};
    (void)cat_confirmation(&options, &result);
    if (!result.confirmed) return;
    if (ls_ui_network_profile_set(app->control_socket, next, &app->status,
                                  app->error, sizeof(app->error)) != 0) {
        ls_message(app->error);
    }
}

static int ls_min(int left, int right) { return left < right ? left : right; }

static int ls_render_qr(const char *value, uint8_t *temporary, uint8_t *code) {
    return value && value[0] && qrcodegen_encodeText(value, temporary, code,
        qrcodegen_Ecc_MEDIUM, qrcodegen_VERSION_MIN, LS_QR_MAX_VERSION,
        qrcodegen_Mask_AUTO, true);
}

static void ls_show_qr_value(const char *title, const char *value) {
    uint8_t temporary[qrcodegen_BUFFER_LEN_FOR_VERSION(LS_QR_MAX_VERSION)];
    uint8_t code[qrcodegen_BUFFER_LEN_FOR_VERSION(LS_QR_MAX_VERSION)];
    cat_footer_item footer[] = {{.button = CAT_BTN_B, .label = "Back"}};
    if (!ls_render_qr(value, temporary, code)) {
        ls_message("This value is too large to encode as QR.");
        return;
    }
    for (;;) {
        cat_input_event event;
        cat_theme *theme = cat_get_theme();
        TTF_Font *small = cat_get_font(CAT_FONT_TINY);
        SDL_Rect content = cat_get_content_rect(true, cat_hints_enabled_from_env(), false);
        int screen_width = cat_get_screen_width();
        int margin = screen_width / 40;
        int size = qrcodegen_getSize(code);
        int module_count = size + 8;
        int available = ls_min(content.h * 3 / 4, screen_width * 3 / 5);
        int module_size = available / module_count;
        int qr_size = module_size * module_count;
        int qr_x = (screen_width - qr_size) / 2;
        int qr_y = content.y + content.h - qr_size;
        while (cat_poll_input(&event)) {
            if (event.pressed && event.button == CAT_BTN_B) return;
        }
        if (!theme || !small) return;
        cat_draw_background();
        cat_draw_screen_title(title, NULL);
        cat_draw_text_wrapped(small, value, margin, content.y + margin,
                              screen_width - margin * 2, theme->text, CAT_ALIGN_CENTER);
        cat_draw_rect(qr_x, qr_y, qr_size, qr_size, (cat_draw_color){255, 255, 255, 255});
        for (int y = 0; y < size; y++) {
            for (int x = 0; x < size; x++) {
                if (qrcodegen_getModule(code, x, y)) {
                    cat_draw_rect(qr_x + (x + 4) * module_size,
                                  qr_y + (y + 4) * module_size,
                                  module_size, module_size,
                                  (cat_draw_color){0, 0, 0, 255});
                }
            }
        }
        if (cat_hints_enabled_from_env()) cat_draw_footer(footer, 1);
        cat_present();
    }
}

static ls_ui_folder *ls_find_folder(ls_app *app, const char *folder_id) {
    int index;
    for (index = 0; index < app->status.folder_count; index++) {
        if (strcmp(app->status.folders[index].id, folder_id) == 0)
            return &app->status.folders[index];
    }
    return NULL;
}

static const char *ls_identity_suffix(const char *identity) {
    size_t length = identity ? strlen(identity) : 0u;
    return length > 8u ? identity + length - 8u : identity;
}

static void ls_show_folder_history(ls_app *app, const ls_ui_folder *folder) {
    cat_options_item items[LS_UI_MAX_STORAGE_ROWS];
    cat_option values[LS_UI_MAX_STORAGE_ROWS];
    char value_text[LS_UI_MAX_STORAGE_ROWS][64];
    int row_indexes[LS_UI_MAX_STORAGE_ROWS];
    cat_footer_item footer[] = {{.button = CAT_BTN_B, .label = "Back"},
                                {.button = CAT_BTN_A, .label = "Details", .is_confirm = true}};
    cat_options_list_opts options = {0};
    cat_options_list_result result = {0};
    const char *suffix = ls_identity_suffix(folder->card_id);
    int item_count = 0;
    int index;
    if (!app->status.storage_present) {
        ls_message("Snapshot and version history is unavailable.");
        return;
    }
    memset(items, 0, sizeof(items));
    memset(values, 0, sizeof(values));
    for (index = 0; index < app->status.storage_row_count; index++) {
        ls_ui_storage_row *row = &app->status.storage_rows[index];
        char bytes[32];
        if (strcmp(row->kind, folder->kind) != 0 || strcmp(row->card_suffix, suffix) != 0) continue;
        ls_format_bytes(row->bytes, bytes, sizeof(bytes));
        snprintf(value_text[item_count], sizeof(value_text[item_count]), "%s · %s", row->category, bytes);
        values[item_count] = (cat_option){.label = value_text[item_count], .value = value_text[item_count]};
        items[item_count] = (cat_options_item){.label = row->name, .type = CAT_OPT_CLICKABLE,
                                               .options = &values[item_count], .option_count = 1};
        row_indexes[item_count] = index;
        item_count++;
    }
    if (item_count == 0) {
        ls_message("This folder has no retained snapshot or version history yet.");
        return;
    }
    options.title = "Folder History";
    options.items = items;
    options.item_count = item_count;
    options.footer = footer;
    options.footer_count = cat_hints_enabled_from_env() ? 2 : 0;
    if (cat_options_list(&options, &result) != CAT_CANCELLED &&
        result.action == CAT_ACTION_SELECTED && result.focused_index >= 0 &&
        result.focused_index < item_count) {
        ls_ui_storage_row *row = &app->status.storage_rows[row_indexes[result.focused_index]];
        char bytes[32];
        char detail[512];
        ls_format_bytes(row->bytes, bytes, sizeof(bytes));
        snprintf(detail, sizeof(detail), "%s\nType: %s\nCard: ...%s\nSize: %s",
                 row->name, row->category, row->card_suffix, bytes);
        ls_message(detail);
    }
}

static void ls_show_folder_conflicts(const ls_ui_folder *folder) {
    cat_options_item items[LS_UI_MAX_CONFLICTS];
    cat_footer_item footer[] = {{.button = CAT_BTN_B, .label = "Back"}};
    cat_options_list_opts options = {0};
    cat_options_list_result result = {0};
    int index;
    if (folder->conflict_count == 0) {
        ls_message("No Syncthing conflict files were found in this folder.");
        return;
    }
    memset(items, 0, sizeof(items));
    for (index = 0; index < folder->conflict_path_count; index++) {
        items[index] = (cat_options_item){.label = folder->conflicts[index], .type = CAT_OPT_CLICKABLE};
    }
    options.title = folder->conflict_count > folder->conflict_path_count
        ? "Conflicts (partial list)" : "Conflicts";
    options.items = items;
    options.item_count = folder->conflict_path_count;
    options.footer = footer;
    options.footer_count = cat_hints_enabled_from_env() ? 1 : 0;
    (void)cat_options_list(&options, &result);
}

static void ls_show_folder_actions(ls_app *app, const char *folder_id) {
    int focus = 0;
    int inspected = 0;
    for (;;) {
        cat_options_item items[6];
        cat_option values[4];
        cat_footer_item footer[] = {{.button = CAT_BTN_B, .label = "Back"},
                                    {.button = CAT_BTN_A, .label = "Choose", .is_confirm = true}};
        cat_options_list_opts options = {0};
        cat_options_list_result result = {0};
        char history_value[48];
        char conflict_value[32];
        ls_ui_folder *folder;
        int action;
        ls_refresh(app);
        if (!app->controller_available || !(folder = ls_find_folder(app, folder_id))) {
            ls_message(app->error[0] ? app->error : "The folder is no longer available.");
            return;
        }
        if (!inspected && ls_ui_has_capability(&app->status, "folder.inspect")) {
            inspected = 1;
            if (ls_ui_folder_action(app->control_socket, "folder.inspect", folder_id, NULL,
                                    &app->status, app->error, sizeof(app->error)) != 0) {
                ls_message(app->error);
            }
            folder = ls_find_folder(app, folder_id);
            if (!folder) return;
        }
        memset(items, 0, sizeof(items));
        memset(values, 0, sizeof(values));
        values[0] = (cat_option){.label = folder->state, .value = folder->state};
        values[1] = (cat_option){.label = folder->paused ? "Paused" : "Running",
                                 .value = folder->paused ? "Paused" : "Running"};
        {
            int history_count = 0;
            const char *suffix = ls_identity_suffix(folder->card_id);
            for (int index = 0; index < app->status.storage_row_count; index++) {
                if (strcmp(app->status.storage_rows[index].kind, folder->kind) == 0 &&
                    strcmp(app->status.storage_rows[index].card_suffix, suffix) == 0) history_count++;
            }
            snprintf(history_value, sizeof(history_value), "%d entries", history_count);
            snprintf(conflict_value, sizeof(conflict_value), "%d", folder->conflict_count);
            values[2] = (cat_option){.label = history_value, .value = history_value};
            values[3] = (cat_option){.label = conflict_value, .value = conflict_value};
        }
        items[0] = (cat_options_item){.label = "Details", .type = CAT_OPT_CLICKABLE,
                                      .options = &values[0], .option_count = 1};
        items[1] = (cat_options_item){.label = folder->paused ? "Resume" : "Pause",
                                      .type = CAT_OPT_CLICKABLE, .options = &values[1], .option_count = 1};
        items[2] = (cat_options_item){.label = "Rescan", .type = CAT_OPT_CLICKABLE};
        items[3] = (cat_options_item){.label = "Rename", .type = CAT_OPT_CLICKABLE};
        items[4] = (cat_options_item){.label = "Snapshot history", .type = CAT_OPT_CLICKABLE,
                                      .options = &values[2], .option_count = 1};
        items[5] = (cat_options_item){.label = "Conflicts", .type = CAT_OPT_CLICKABLE,
                                      .options = &values[3], .option_count = 1};
        options.title = folder->label;
        options.items = items;
        options.item_count = 6;
        options.footer = footer;
        options.footer_count = cat_hints_enabled_from_env() ? 2 : 0;
        options.initial_selected_index = focus;
        action = cat_options_list(&options, &result);
        focus = result.focused_index;
        if (action == CAT_CANCELLED || result.action == CAT_ACTION_BACK) return;
        if (result.action != CAT_ACTION_SELECTED) continue;
        if (focus == 0) {
            char local[32], global[32], detail[1024];
            ls_format_bytes(folder->local_bytes, local, sizeof(local));
            ls_format_bytes(folder->global_bytes, global, sizeof(global));
            snprintf(detail, sizeof(detail), "%s\nPath: %s\nType: %s\nState: %s\nLocal: %s\nGlobal: %s\nPeers: %d\nLast activity: %s\nVersioning: %s",
                     folder->label, folder->path, folder->type, folder->state,
                     local, global, folder->peer_count,
                     folder->last_sync[0] ? folder->last_sync : "Unknown",
                     folder->versioning[0] ? folder->versioning : "Off");
            ls_message(detail);
        } else if (focus == 1) {
            const char *operation = folder->paused ? "folder.resume" : "folder.pause";
            const char *message = folder->paused
                ? "Resume this folder? Storage and first-sync safety pauses remain in force."
                : "Pause this folder until you explicitly resume it?";
            if (ls_confirm(message, folder->paused ? "Resume" : "Pause") &&
                ls_ui_folder_action(app->control_socket, operation, folder->id, NULL,
                                    &app->status, app->error, sizeof(app->error)) != 0)
                ls_message(app->error);
        } else if (focus == 2) {
            if (ls_ui_folder_action(app->control_socket, "folder.rescan", folder->id, NULL,
                                    &app->status, app->error, sizeof(app->error)) != 0)
                ls_message(app->error);
            else if (folder->paused)
                ls_message("The rescan is queued until the final pause reason clears.");
        } else if (focus == 3) {
            cat_keyboard_result keyboard = {0};
            if (cat_keyboard(folder->label, "Rename this managed folder. Its path cannot be edited here.",
                             CAT_KB_GENERAL, &keyboard) == CAT_OK && keyboard.text[0] &&
                ls_ui_folder_action(app->control_socket, "folder.rename", folder->id, keyboard.text,
                                    &app->status, app->error, sizeof(app->error)) != 0)
                ls_message(app->error);
        } else if (focus == 4) {
            ls_show_folder_history(app, folder);
        } else if (focus == 5) {
            ls_show_folder_conflicts(folder);
        }
    }
}

static int ls_add_peer(ls_app *app, const char *device_id, const char *suggested_name) {
    cat_keyboard_result name = {0};
    const char *initial = suggested_name && suggested_name[0] ? suggested_name : "Syncthing peer";
    if (cat_keyboard(initial, "Name this peer. Unknown devices and folders are never accepted automatically.",
                     CAT_KB_GENERAL, &name) != CAT_OK || !name.text[0]) return 0;
    if (!ls_confirm("Add this peer? It cannot add folders automatically, and LAN-only applies unless you change the network profile.", "Add"))
        return 0;
    if (ls_ui_device_action(app->control_socket, "device.add", device_id, name.text,
                            &app->status, app->error, sizeof(app->error)) != 0) {
        ls_message(app->error);
        return -1;
    }
    return 1;
}

static void ls_manual_add_peer(ls_app *app) {
    cat_keyboard_result device = {0};
    if (cat_keyboard("", "Enter the peer's Syncthing device ID. A syncthing:// QR payload is also accepted.",
                     CAT_KB_GENERAL, &device) != CAT_OK || !device.text[0]) return;
    (void)ls_add_peer(app, device.text, "Syncthing peer");
}

static ls_ui_peer *ls_find_peer(ls_app *app, const char *peer_id) {
    int index;
    for (index = 0; index < app->status.peer_count; index++) {
        if (strcmp(app->status.peers[index].id, peer_id) == 0) return &app->status.peers[index];
    }
    return NULL;
}

static void ls_show_peer(ls_app *app, const char *peer_id) {
    int focus = 0;
    for (;;) {
        ls_ui_peer *peer;
        cat_options_item items[2];
        cat_option state;
        cat_footer_item footer[] = {{.button = CAT_BTN_B, .label = "Back"},
                                    {.button = CAT_BTN_A, .label = "Choose", .is_confirm = true}};
        cat_options_list_opts options = {0};
        cat_options_list_result result = {0};
        int action;
        ls_refresh(app);
        if (!app->controller_available || !(peer = ls_find_peer(app, peer_id))) {
            ls_message(app->error[0] ? app->error : "The peer is no longer available.");
            return;
        }
        if (peer->pending) {
            char prompt[512];
            snprintf(prompt, sizeof(prompt), "%s (...%s) asked to connect. Accept it as a peer? Unknown folders still require an explicit on-device action.",
                     peer->name, peer->id_suffix);
            if (ls_confirm(prompt, "Accept")) (void)ls_add_peer(app, peer->id, peer->name);
            return;
        }
        memset(items, 0, sizeof(items));
        state = (cat_option){.label = peer->state, .value = peer->state};
        items[0] = (cat_options_item){.label = "Details", .type = CAT_OPT_CLICKABLE,
                                      .options = &state, .option_count = 1};
        items[1] = (cat_options_item){.label = "Rename", .type = CAT_OPT_CLICKABLE};
        options.title = peer->name;
        options.items = items;
        options.item_count = 2;
        options.footer = footer;
        options.footer_count = cat_hints_enabled_from_env() ? 2 : 0;
        options.initial_selected_index = focus;
        action = cat_options_list(&options, &result);
        focus = result.focused_index;
        if (action == CAT_CANCELLED || result.action == CAT_ACTION_BACK) return;
        if (result.action != CAT_ACTION_SELECTED) continue;
        if (focus == 0) {
            char detail[768];
            snprintf(detail, sizeof(detail), "%s\nState: %s\nConnection: %s\nAddress: %s\nDevice ID: %s%s",
                     peer->name, peer->state, peer->connection,
                     peer->address[0] ? peer->address : "Unavailable", peer->id,
                     peer->introducer ? "\nIntroducer: yes" : "");
            ls_message(detail);
        } else if (focus == 1) {
            cat_keyboard_result keyboard = {0};
            if (cat_keyboard(peer->name, "Rename this peer on Leaf.", CAT_KB_GENERAL, &keyboard) == CAT_OK &&
                keyboard.text[0] &&
                ls_ui_device_action(app->control_socket, "device.rename", peer->id, keyboard.text,
                                    &app->status, app->error, sizeof(app->error)) != 0)
                ls_message(app->error);
        }
    }
}

static void ls_show_devices(ls_app *app) {
    int focus = 0;
    int scroll = 0;
    for (;;) {
        cat_options_item items[LS_UI_MAX_PEERS + 2];
        cat_option values[LS_UI_MAX_PEERS + 1];
        char peer_values[LS_UI_MAX_PEERS][64];
        cat_footer_item footer[] = {{.button = CAT_BTN_B, .label = "Back"},
                                    {.button = CAT_BTN_A, .label = "Choose", .is_confirm = true}};
        cat_options_list_opts options = {0};
        cat_options_list_result result = {0};
        int index;
        int item_count = 2;
        int action;
        ls_refresh(app);
        if (!app->controller_available) {
            ls_message(app->error);
            return;
        }
        memset(items, 0, sizeof(items));
        memset(values, 0, sizeof(values));
        values[0] = (cat_option){.label = app->status.upstream_version,
                                 .value = app->status.upstream_version};
        items[0] = (cat_options_item){.label = "My device ID + QR", .type = CAT_OPT_CLICKABLE,
                                      .options = &values[0], .option_count = 1};
        items[1] = (cat_options_item){.label = "Add peer by ID", .type = CAT_OPT_CLICKABLE};
        for (index = 0; index < app->status.peer_count; index++) {
            ls_ui_peer *peer = &app->status.peers[index];
            snprintf(peer_values[index], sizeof(peer_values[index]), "%s%s · %s",
                     peer->pending ? "Pending · " : "", peer->state, peer->id_suffix);
            values[index + 1] = (cat_option){.label = peer_values[index], .value = peer_values[index]};
            items[item_count++] = (cat_options_item){.label = peer->name, .type = CAT_OPT_CLICKABLE,
                                                     .options = &values[index + 1], .option_count = 1};
        }
        options.title = "Devices";
        options.items = items;
        options.item_count = item_count;
        options.footer = footer;
        options.footer_count = cat_hints_enabled_from_env() ? 2 : 0;
        options.initial_selected_index = focus < item_count ? focus : 0;
        options.visible_start_index = scroll;
        action = cat_options_list(&options, &result);
        focus = result.focused_index;
        scroll = result.visible_start_index;
        if (action == CAT_CANCELLED || result.action == CAT_ACTION_BACK) return;
        if (result.action != CAT_ACTION_SELECTED) continue;
        if (focus == 0) {
            ls_show_qr_value("My Syncthing Device", app->status.device_id);
        } else if (focus == 1) {
            ls_manual_add_peer(app);
        } else if (focus - 2 >= 0 && focus - 2 < app->status.peer_count) {
            char peer_id[sizeof(app->status.peers[0].id)];
            snprintf(peer_id, sizeof(peer_id), "%s", app->status.peers[focus - 2].id);
            ls_show_peer(app, peer_id);
        }
    }
}

static int ls_wait_service_stopped(ls_app *app) {
    int attempt;
    for (attempt = 0; attempt < 180; attempt++) {
        ls_ctl1_status service = {0};
        if (ls_ctl1_get(app->daemon_socket, LS_SERVICE_ID, &service) == 0 && service.found &&
            (strcmp(service.effective_state, "stopped") == 0 ||
             strcmp(service.effective_state, "disabled") == 0 ||
             strcmp(service.effective_state, "failed") == 0 ||
             strcmp(service.effective_state, "unavailable") == 0)) return 0;
        SDL_Delay(100);
    }
    snprintf(app->error, sizeof(app->error), "%s", "Leaf did not prove the service process group absent");
    return -1;
}

static int ls_execute_reset_helper(ls_app *app, const char *action_id) {
    pid_t child;
    int status;
    if (!action_id || strlen(action_id) != 32u || access(app->controller_binary, X_OK) != 0) {
        snprintf(app->error, sizeof(app->error), "%s", "Reset helper is unavailable");
        return -1;
    }
    child = fork();
    if (child < 0) {
        snprintf(app->error, sizeof(app->error), "%s", "Could not start reset helper");
        return -1;
    }
    if (child == 0) {
        execl(app->controller_binary, app->controller_binary, "reset-execute", action_id, (char *)NULL);
        _exit(127);
    }
    do {
        if (waitpid(child, &status, 0) < 0) {
            snprintf(app->error, sizeof(app->error), "%s", "Could not wait for reset helper");
            return -1;
        }
    } while (!WIFEXITED(status) && !WIFSIGNALED(status));
    if (!WIFEXITED(status) || WEXITSTATUS(status) != 0) {
        snprintf(app->error, sizeof(app->error), "%s", "Reset stopped safely before completion; reopen this screen after checking the cards");
        return -1;
    }
    return 0;
}

static void ls_reset_summary(ls_app *app, char *message, size_t message_size) {
    size_t used = 0;
    int index;
    int count = snprintf(message, message_size,
                         "Leaf will stop Syncthing, then remove exactly:\n");
    if (count < 0 || (size_t)count >= message_size) return;
    used = (size_t)count;
    if (app->status.reset_remove_count == 0) {
        count = snprintf(message + used, message_size - used, "\nNo current index directory was found.\n");
        if (count > 0 && (size_t)count < message_size - used) used += (size_t)count;
    }
    for (index = 0; index < app->status.reset_remove_count; index++) {
        count = snprintf(message + used, message_size - used, "\n• %s", app->status.reset_remove_paths[index]);
        if (count < 0 || (size_t)count >= message_size - used) break;
        used += (size_t)count;
    }
    if (app->status.reset_retained_count > 0 && used < message_size) {
        count = snprintf(message + used, message_size - used, "\n\nAbsent-card state deliberately retained:");
        if (count > 0 && (size_t)count < message_size - used) used += (size_t)count;
        for (index = 0; index < app->status.reset_retained_count; index++) {
            count = snprintf(message + used, message_size - used, "\n• %s", app->status.reset_retained_paths[index]);
            if (count < 0 || (size_t)count >= message_size - used) break;
            used += (size_t)count;
        }
    }
    if (used < message_size)
        (void)snprintf(message + used, message_size - used,
                       "\n\nLive Saves, States, and ROMs are not removed.");
}

static void ls_reset_flow(ls_app *app, const char *action) {
    const char *phrase;
    const char *warning;
    cat_keyboard_result confirmation = {0};
    char summary[16384];
    if (strcmp(action, "index-only") == 0) {
        phrase = "RESET INDEX";
        warning = "Rebuild Syncthing's derived index? Configuration, device identity, snapshots, versions, Saves, States, and ROMs remain.";
    } else if (strcmp(action, "available-only") == 0) {
        phrase = "RESET AVAILABLE STATE";
        warning = "Reset only currently available Syncthing state? Any enrolled absent card keeps its snapshots and versions and will be named before execution.";
    } else {
        phrase = "RESET SYNCTHING";
        warning = "Reset all Syncthing state and create a new device identity? Every enrolled card must be present. Live Saves, States, and ROMs remain.";
    }
    if (!ls_confirm(warning, "Continue")) return;
    if (cat_keyboard("", phrase, CAT_KB_GENERAL, &confirmation) != CAT_OK ||
        strcmp(confirmation.text, phrase) != 0) {
        ls_message("The confirmation phrase did not match. Nothing was changed.");
        return;
    }
    if (ls_ui_reset_prepare(app->control_socket, action, confirmation.text,
                            &app->status, app->error, sizeof(app->error)) != 0) {
        ls_message(app->error);
        return;
    }
    ls_reset_summary(app, summary, sizeof(summary));
    if (!ls_confirm(summary, "Execute")) return;
    if (ls_ctl1_action(app->daemon_socket, "stop", LS_SERVICE_ID,
                       app->error, sizeof(app->error)) != 0 ||
        ls_wait_service_stopped(app) != 0 ||
        ls_execute_reset_helper(app, app->status.reset_plan_id) != 0) {
        ls_message(app->error);
        return;
    }
    ls_message(strcmp(action, "index-only") == 0
        ? "The Syncthing index was reset. Leaf will rebuild it after restart."
        : "The confirmed Syncthing state was reset. Leaf will generate a new Syncthing device identity after restart.");
    if (ls_ctl1_action(app->daemon_socket, "run", LS_SERVICE_ID,
                       app->error, sizeof(app->error)) != 0) ls_message(app->error);
}

static void ls_show_recovery(ls_app *app) {
    int focus = 0;
    for (;;) {
        cat_options_item items[3] = {
            {.label = "Reset index only", .type = CAT_OPT_CLICKABLE},
            {.label = "Reset Syncthing", .type = CAT_OPT_CLICKABLE},
            {.label = "Reset available state only", .type = CAT_OPT_CLICKABLE},
        };
        cat_footer_item footer[] = {{.button = CAT_BTN_B, .label = "Back"},
                                    {.button = CAT_BTN_A, .label = "Choose", .is_confirm = true}};
        cat_options_list_opts options = {0};
        cat_options_list_result result = {0};
        int action;
        options.title = "Recovery";
        options.items = items;
        options.item_count = 3;
        options.footer = footer;
        options.footer_count = cat_hints_enabled_from_env() ? 2 : 0;
        options.initial_selected_index = focus;
        action = cat_options_list(&options, &result);
        focus = result.focused_index;
        if (action == CAT_CANCELLED || result.action == CAT_ACTION_BACK) return;
        if (result.action != CAT_ACTION_SELECTED) continue;
        if (focus == 0) ls_reset_flow(app, "index-only");
        else if (focus == 1) ls_reset_flow(app, "full");
        else if (focus == 2) ls_reset_flow(app, "available-only");
        return;
    }
}

static void ls_show_storage(ls_app *app) {
    cat_options_item items[LS_UI_MAX_STORAGE_ROWS + 1];
    cat_option values[LS_UI_MAX_STORAGE_ROWS + 1];
    char value_text[LS_UI_MAX_STORAGE_ROWS + 1][96];
    char total_text[160];
    char snapshots[32];
    char versions[32];
    cat_footer_item footer[] = {{.button = CAT_BTN_B, .label = "Back"},
                                {.button = CAT_BTN_A, .label = "Details", .is_confirm = true}};
    cat_options_list_opts options = {0};
    cat_options_list_result result = {0};
    int index;
    ls_refresh(app);
    if (!app->controller_available || !app->status.storage_present) {
        ls_message(app->error[0] ? app->error : "Snapshot and version inventory is unavailable.");
        return;
    }
    memset(items, 0, sizeof(items));
    memset(values, 0, sizeof(values));
    ls_format_bytes(app->status.snapshot_bytes, snapshots, sizeof(snapshots));
    ls_format_bytes(app->status.version_bytes, versions, sizeof(versions));
    snprintf(total_text, sizeof(total_text), "%d snapshots / %s\n%d version groups / %s",
             app->status.snapshot_count, snapshots, app->status.version_groups, versions);
    snprintf(value_text[0], sizeof(value_text[0]), "%s + %s", snapshots, versions);
    values[0] = (cat_option){.label = value_text[0], .value = value_text[0]};
    items[0] = (cat_options_item){.label = "Totals", .type = CAT_OPT_CLICKABLE,
                                  .options = &values[0], .option_count = 1};
    for (index = 0; index < app->status.storage_row_count; index++) {
        ls_ui_storage_row *row = &app->status.storage_rows[index];
        char bytes[32];
        ls_format_bytes(row->bytes, bytes, sizeof(bytes));
        snprintf(value_text[index + 1], sizeof(value_text[index + 1]), "%s · ...%s · %s",
                 row->kind, row->card_suffix, bytes);
        values[index + 1] = (cat_option){.label = value_text[index + 1],
                                         .value = value_text[index + 1]};
        items[index + 1] = (cat_options_item){.label = row->name, .type = CAT_OPT_CLICKABLE,
                                              .options = &values[index + 1], .option_count = 1};
    }
    options.title = "Snapshots & Versions";
    options.items = items;
    options.item_count = app->status.storage_row_count + 1;
    options.footer = footer;
    options.footer_count = cat_hints_enabled_from_env() ? 2 : 0;
    if (cat_options_list(&options, &result) == CAT_CANCELLED ||
        result.action == CAT_ACTION_BACK) return;
    if (result.action != CAT_ACTION_SELECTED) return;
    if (result.focused_index == 0) {
        ls_message(total_text);
    } else if (result.focused_index > 0 &&
               result.focused_index <= app->status.storage_row_count) {
        ls_ui_storage_row *row = &app->status.storage_rows[result.focused_index - 1];
        char bytes[32];
        char detail[512];
        ls_format_bytes(row->bytes, bytes, sizeof(bytes));
        snprintf(detail, sizeof(detail), "%s\nType: %s\nCategory: %s\nCard: ...%s\nSize: %s",
                 row->name, row->kind, row->category, row->card_suffix, bytes);
        ls_message(detail);
    }
}

static void ls_show_settings(ls_app *app) {
    int focus = 0;
    for (;;) {
        cat_options_item items[4];
        cat_option values[3];
        cat_footer_item footer[] = {{.button = CAT_BTN_B, .label = "Back"},
                                    {.button = CAT_BTN_A, .label = "Choose", .is_confirm = true}};
        cat_options_list_opts options = {0};
        cat_options_list_result result = {0};
        char storage[64];
        int action;
        ls_refresh(app);
        if (!app->controller_available || strcmp(app->status.controller, "recovery-pending") == 0) {
            ls_message(app->error[0] ? app->error : "Settings are unavailable while reset recovery is pending.");
            return;
        }
        memset(items, 0, sizeof(items));
        memset(values, 0, sizeof(values));
        snprintf(storage, sizeof(storage), "%d snapshots · %d version groups",
                 app->status.snapshot_count, app->status.version_groups);
        values[0] = (cat_option){.label = app->status.logging_present ? app->status.log_level : "unavailable",
                                 .value = app->status.logging_present ? app->status.log_level : "unavailable"};
        values[1] = (cat_option){.label = app->status.diagnostics_exported[0]
                                          ? app->status.diagnostics_exported : "Not exported",
                                 .value = app->status.diagnostics_exported[0]
                                          ? app->status.diagnostics_exported : "Not exported"};
        values[2] = (cat_option){.label = app->status.storage_present ? storage : "unavailable",
                                 .value = app->status.storage_present ? storage : "unavailable"};
        items[0] = (cat_options_item){.label = "Log level", .type = CAT_OPT_CLICKABLE,
                                      .options = &values[0], .option_count = 1};
        items[1] = (cat_options_item){.label = "Export diagnostics", .type = CAT_OPT_CLICKABLE,
                                      .options = &values[1], .option_count = 1};
        items[2] = (cat_options_item){.label = "Snapshots & versions", .type = CAT_OPT_CLICKABLE,
                                      .options = &values[2], .option_count = 1};
        items[3] = (cat_options_item){.label = "Recovery", .type = CAT_OPT_CLICKABLE};
        options.title = "Settings & Recovery";
        options.items = items;
        options.item_count = 4;
        options.footer = footer;
        options.footer_count = cat_hints_enabled_from_env() ? 2 : 0;
        options.initial_selected_index = focus;
        action = cat_options_list(&options, &result);
        focus = result.focused_index;
        if (action == CAT_CANCELLED || result.action == CAT_ACTION_BACK) return;
        if (result.action != CAT_ACTION_SELECTED) continue;
        if (focus == 0) {
            const char *next = strcmp(app->status.log_level, "debug") == 0 ? "normal" : "debug";
            const char *message = strcmp(next, "debug") == 0
                ? "Enable debug logging for 15 minutes? Logs remain redacted but use more storage and CPU. Debug expires automatically."
                : "Return to normal logging now?";
            if (!app->status.logging_present ||
                !ls_ui_has_capability(&app->status, "log.level.set")) {
                ls_message("Logging control is unavailable.");
            } else if (ls_confirm(message, "Change") &&
                       ls_ui_log_level_set(app->control_socket, next, &app->status,
                                           app->error, sizeof(app->error)) != 0) {
                ls_message(app->error);
            }
        } else if (focus == 1) {
            if (!ls_ui_has_capability(&app->status, "diagnostics.export")) {
                ls_message("Diagnostics export is unavailable.");
            } else if (ls_ui_diagnostics_export(app->control_socket, &app->status,
                                                app->error, sizeof(app->error)) != 0) {
                ls_message(app->error);
            } else {
                char message[1024];
                snprintf(message, sizeof(message), "Redacted diagnostics exported to:\n%s",
                         app->status.diagnostics_path);
                ls_message(message);
            }
        } else if (focus == 2) {
            ls_show_storage(app);
        } else if (focus == 3) {
            ls_show_recovery(app);
        }
    }
}

static void ls_show_gateway(ls_app *app) {
    uint8_t temporary[qrcodegen_BUFFER_LEN_FOR_VERSION(LS_QR_MAX_VERSION)];
    uint8_t code[qrcodegen_BUFFER_LEN_FOR_VERSION(LS_QR_MAX_VERSION)];
    char encoded_url[sizeof(app->status.gateway_qr_url)] = "";
    uint32_t next_refresh = 0;
    cat_footer_item footer[] = {
        {.button = CAT_BTN_B, .label = "Back"},
        {.button = CAT_BTN_A, .label = "New code", .is_confirm = true},
        {.button = CAT_BTN_X, .label = "15 min"},
        {.button = CAT_BTN_Y, .label = "Revoke all"},
    };
    if (ls_ui_gateway_action(app->control_socket, "gateway.open", false,
                             &app->status, app->error, sizeof(app->error)) != 0) {
        ls_message(app->error);
        return;
    }
    for (;;) {
        cat_input_event event;
        cat_theme *theme = cat_get_theme();
        TTF_Font *large = cat_get_font(CAT_FONT_LARGE);
        TTF_Font *small = cat_get_font(CAT_FONT_TINY);
        SDL_Rect content = cat_get_content_rect(true, cat_hints_enabled_from_env(), false);
        int screen_width = cat_get_screen_width();
        int margin = screen_width / 40;
        int qr_available = ls_min(content.h - margin * 2, screen_width * 46 / 100);
        int module_count;
        int module_size;
        int qr_size;
        int qr_x;
        int qr_y;
        int text_width;
        int cursor_y = content.y + margin;
        char trusted[64];
        uint32_t now = SDL_GetTicks();

        if (!theme || !large || !small) break;
        if (strcmp(encoded_url, app->status.gateway_qr_url) != 0) {
            snprintf(encoded_url, sizeof(encoded_url), "%s", app->status.gateway_qr_url);
            memset(code, 0, sizeof(code));
            if (encoded_url[0] && !ls_render_qr(encoded_url, temporary, code)) {
                ls_message("The pairing URL is too large to encode as QR.");
                encoded_url[0] = '\0';
            }
        }
        module_count = app->status.gateway_pairing && encoded_url[0] ? qrcodegen_getSize(code) + 8 : 0;
        module_size = module_count > 0 ? qr_available / module_count : 0;
        qr_size = module_size * module_count;
        qr_x = screen_width - margin - qr_size;
        qr_y = content.y + (content.h - qr_size) / 2;
        text_width = qr_x - margin * 2;
        while (cat_poll_input(&event)) {
            if (!event.pressed) continue;
            if (event.button == CAT_BTN_B) {
                (void)ls_ui_gateway_action(app->control_socket, "gateway.close", false,
                                           &app->status, app->error, sizeof(app->error));
                return;
            }
            if (event.button == CAT_BTN_A) {
                if (ls_ui_gateway_action(app->control_socket, "gateway.open", false,
                                         &app->status, app->error, sizeof(app->error)) != 0) ls_message(app->error);
                next_refresh = SDL_GetTicks() + 1000u;
            } else if (event.button == CAT_BTN_X) {
                if (app->status.gateway_trusted_browsers == 0) {
                    ls_message("Pair at least one browser before starting the 15-minute extension.");
                } else if (ls_confirm("Keep the read-only web interface open for 15 minutes after leaving this screen? New pairing will be disabled.", "Extend")) {
                    if (ls_ui_gateway_action(app->control_socket, "gateway.extend", true,
                                             &app->status, app->error, sizeof(app->error)) != 0) ls_message(app->error);
                    return;
                }
            } else if (event.button == CAT_BTN_Y &&
                       ls_confirm("Revoke every trusted browser and close the web interface now?", "Revoke")) {
                if (ls_ui_gateway_action(app->control_socket, "gateway.revoke-all", true,
                                         &app->status, app->error, sizeof(app->error)) != 0) ls_message(app->error);
                return;
            }
        }
        if ((int32_t)(now - next_refresh) >= 0) {
            if (ls_ui_gateway_action(app->control_socket, "gateway.keepalive", false,
                                     &app->status, app->error, sizeof(app->error)) != 0) {
                ls_message(app->error);
                return;
            }
            next_refresh = now + 1000u;
        }

        cat_draw_background();
        cat_draw_screen_title("Web Interface", NULL);
        cat_draw_text(small, "HTTPS address", margin, cursor_y, theme->hint);
        cursor_y += TTF_FontHeight(small) + 2;
        cat_draw_text_wrapped(small, app->status.gateway_url, margin, cursor_y,
                              text_width, theme->text, CAT_ALIGN_LEFT);
        cursor_y += cat_measure_wrapped_text_height(small, app->status.gateway_url, text_width) + margin;
        cat_draw_text(small, "Pairing PIN", margin, cursor_y, theme->hint);
        cursor_y += TTF_FontHeight(small) + 2;
        cat_draw_text(large, app->status.gateway_pairing ? app->status.gateway_pin : "Closed",
                      margin, cursor_y, theme->text);
        cursor_y += TTF_FontHeight(large) + margin;
        snprintf(trusted, sizeof(trusted), "Trusted browsers: %d", app->status.gateway_trusted_browsers);
        cat_draw_text(small, trusted, margin, cursor_y, theme->text);
        cursor_y += TTF_FontHeight(small) + margin;
        cat_draw_text(small, "Certificate fingerprint", margin, cursor_y, theme->hint);
        cursor_y += TTF_FontHeight(small) + 2;
        cat_draw_text_wrapped(small, app->status.gateway_fingerprint, margin, cursor_y,
                              text_width, theme->text, CAT_ALIGN_LEFT);

        if (module_size > 0 && qr_size > 0) {
            cat_draw_color white = {255, 255, 255, 255};
            cat_draw_color black = {0, 0, 0, 255};
            int quiet = 4;
            int size = qrcodegen_getSize(code);
            cat_draw_rect(qr_x, qr_y, qr_size, qr_size, white);
            for (int y = 0; y < size; y++) {
                for (int x = 0; x < size; x++) {
                    if (qrcodegen_getModule(code, x, y)) {
                        cat_draw_rect(qr_x + (x + quiet) * module_size,
                                      qr_y + (y + quiet) * module_size,
                                      module_size, module_size, black);
                    }
                }
            }
        }
        if (cat_hints_enabled_from_env()) cat_draw_footer(footer, 4);
        cat_present();
    }
}

static void ls_run_overview(ls_app *app) {
    int focus = 0;
    int scroll = 0;
    for (;;) {
        cat_option enabled_options[] = {{.label = "Off", .value = "Off"},
                                        {.label = "On", .value = "On"}};
        cat_option value_options[8];
        cat_options_item items[11];
        cat_footer_item footer[] = {{.button = CAT_BTN_B, .label = "Exit"},
                                    {.button = CAT_BTN_A, .label = "Choose", .is_confirm = true}};
        cat_options_list_opts options = {0};
        cat_options_list_result result = {0};
        char service_value[64];
        char card_value[32];
        char folder_value[32];
        char peer_value[32];
        char transfer_value[96];
        char issue_value[32];
        int item_count = 0;
        int action;
        int recovery_pending;
        ls_refresh(app);
        recovery_pending = app->controller_available &&
                           strcmp(app->status.controller, "recovery-pending") == 0;
        snprintf(service_value, sizeof(service_value), "%s",
                 app->service.found ? app->service.effective_state : "unavailable");
        memset(items, 0, sizeof(items));
        memset(value_options, 0, sizeof(value_options));
        value_options[0] = (cat_option){.label = service_value, .value = service_value};
        items[item_count++] = (cat_options_item){
            .label = "Service", .type = CAT_OPT_CLICKABLE,
            .options = &value_options[0], .option_count = 1};
        items[item_count++] = (cat_options_item){
            .label = "Start with Leaf", .type = CAT_OPT_STANDARD,
            .options = enabled_options, .option_count = 2,
            .selected_option = app->service.desired_enabled ? 1 : 0};
        items[item_count++] = (cat_options_item){
            .label = ls_service_running(&app->service) ? "Stop" : "Run",
            .type = CAT_OPT_CLICKABLE};
        if (app->controller_available) {
            snprintf(card_value, sizeof(card_value), "%d", app->status.card_count);
            snprintf(folder_value, sizeof(folder_value), "%d", app->status.folder_count);
            snprintf(peer_value, sizeof(peer_value), "%d", app->status.peer_count);
            snprintf(issue_value, sizeof(issue_value), "%d", app->status.issue_count);
            if (app->status.transfer_present) {
                char needed[32];
                ls_format_bytes(app->status.transfer_need_bytes, needed, sizeof(needed));
                snprintf(transfer_value, sizeof(transfer_value), "%s · %s needed",
                         app->status.transfer_state, needed);
            } else {
                snprintf(transfer_value, sizeof(transfer_value), "%s", "Unavailable");
            }
            value_options[1] = (cat_option){.label = card_value, .value = card_value};
            value_options[2] = (cat_option){.label = folder_value, .value = folder_value};
            value_options[3] = (cat_option){.label = peer_value, .value = peer_value};
            value_options[4] = (cat_option){.label = transfer_value, .value = transfer_value};
            value_options[5] = (cat_option){
                .label = app->status.network_present ? app->status.network_profile : "unavailable",
                .value = app->status.network_present ? app->status.network_profile : "unavailable"};
            value_options[6] = (cat_option){.label = issue_value, .value = issue_value};
            value_options[7] = (cat_option){
                .label = app->status.gateway_present && app->status.gateway_open ? "Open" : "Closed",
                .value = app->status.gateway_present && app->status.gateway_open ? "Open" : "Closed"};
            if (recovery_pending) {
                items[item_count++] = (cat_options_item){.label = "Recovery issue", .type = CAT_OPT_CLICKABLE,
                    .options = &value_options[6], .option_count = 1};
            } else {
                items[item_count++] = (cat_options_item){.label = "Transfer", .type = CAT_OPT_CLICKABLE,
                    .options = &value_options[4], .option_count = 1};
                items[item_count++] = (cat_options_item){.label = "Cards", .type = CAT_OPT_CLICKABLE,
                    .options = &value_options[1], .option_count = 1};
                items[item_count++] = (cat_options_item){.label = "Folders", .type = CAT_OPT_CLICKABLE,
                    .options = &value_options[2], .option_count = 1};
                items[item_count++] = (cat_options_item){.label = "Devices", .type = CAT_OPT_CLICKABLE,
                    .options = &value_options[3], .option_count = 1};
                items[item_count++] = (cat_options_item){.label = "Network", .type = CAT_OPT_CLICKABLE,
                    .options = &value_options[5], .option_count = 1};
                items[item_count++] = (cat_options_item){.label = "Web Interface", .type = CAT_OPT_CLICKABLE,
                    .options = &value_options[7], .option_count = 1};
                items[item_count++] = (cat_options_item){.label = "Settings & Recovery", .type = CAT_OPT_CLICKABLE};
                items[item_count++] = (cat_options_item){.label = "Issues", .type = CAT_OPT_CLICKABLE,
                    .options = &value_options[6], .option_count = 1};
            }
        }
        options.title = "Syncthing";
        options.items = items;
        options.item_count = item_count;
        options.footer = footer;
        options.footer_count = cat_hints_enabled_from_env() ? 2 : 0;
        options.initial_selected_index = focus < item_count ? focus : 0;
        options.visible_start_index = scroll;
        options.return_on_option_change = true;
        action = cat_options_list(&options, &result);
        focus = result.focused_index;
        scroll = result.visible_start_index;
        if (action == CAT_CANCELLED || result.action == CAT_ACTION_BACK) return;
        if (result.action == CAT_ACTION_OPTION_CHANGED && focus == 1) {
            const char *operation = items[1].selected_option ? "enable" : "disable";
            if (ls_ctl1_action(app->daemon_socket, operation, LS_SERVICE_ID,
                               app->error, sizeof(app->error)) != 0) ls_message(app->error);
            continue;
        }
        if (result.action != CAT_ACTION_SELECTED) continue;
        if (focus == 0) {
            if (app->controller_available) {
                char detail[512];
                snprintf(detail, sizeof(detail), "Controller: %s\nSyncthing: %s\nVersion: %s%s",
                         app->status.controller, app->status.upstream_state,
                         app->status.upstream_version,
                         app->status.game_active ? "\nStopped while you play" : "");
                ls_message(detail);
            } else {
                ls_message(app->error[0] ? app->error : "Syncthing controller is unavailable");
            }
        } else if (focus == 2) {
            const char *operation = ls_service_running(&app->service) ? "stop" : "run";
            if (ls_ctl1_action(app->daemon_socket, operation, LS_SERVICE_ID,
                               app->error, sizeof(app->error)) != 0) ls_message(app->error);
        } else if (recovery_pending && focus == 3) {
            ls_show_issues(app);
        } else if (app->controller_available && focus == 3) {
            char local[32], global[32], needed[32], received[32], sent[32], detail[512];
            ls_format_bytes(app->status.transfer_local_bytes, local, sizeof(local));
            ls_format_bytes(app->status.transfer_global_bytes, global, sizeof(global));
            ls_format_bytes(app->status.transfer_need_bytes, needed, sizeof(needed));
            ls_format_bytes(app->status.transfer_in_bytes, received, sizeof(received));
            ls_format_bytes(app->status.transfer_out_bytes, sent, sizeof(sent));
            snprintf(detail, sizeof(detail), "State: %s\nLocal: %s\nGlobal: %s\nNeeded: %s\nSession received: %s\nSession sent: %s",
                     app->status.transfer_present ? app->status.transfer_state : "Unavailable",
                     local, global, needed, received, sent);
            ls_message(detail);
        } else if (app->controller_available && focus == 4) {
            ls_show_cards(app);
        } else if (app->controller_available && focus == 5) {
            ls_show_folders(app);
        } else if (app->controller_available && focus == 6) {
            ls_show_devices(app);
        } else if (app->controller_available && focus == 7) {
            ls_change_network(app);
        } else if (app->controller_available && focus == 8) {
            ls_show_gateway(app);
        } else if (app->controller_available && focus == 9) {
            ls_show_settings(app);
        } else if (app->controller_available && focus == 10) {
            ls_show_issues(app);
        }
    }
}

int main(void) {
    ls_app app;
    cat_config configuration = {0};
    memset(&app, 0, sizeof(app));
    if (ls_load_paths(&app) != 0) {
        fprintf(stderr, "leaf-syncthing-ui: invalid runtime paths\n");
        return 1;
    }
    configuration.window_title = "Syncthing";
    configuration.log_path = cat_resolve_log_path("leaf-syncthing-ui");
    configuration.cpu_speed = CAT_CPU_SPEED_MENU;
    if (cat_init(&configuration) != CAT_OK) return 1;
    ls_run_overview(&app);
    if (app.controller_available && app.status.gateway_present && app.status.gateway_open) {
        (void)ls_ui_gateway_action(app.control_socket, "gateway.close", false,
                                   &app.status, app.error, sizeof(app.error));
    }
    cat_quit();
    return 0;
}
