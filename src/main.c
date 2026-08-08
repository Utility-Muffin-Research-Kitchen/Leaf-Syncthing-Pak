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

#define LS_SERVICE_ID "org.umrk.syncthing"
#define LS_QR_MAX_VERSION 12

typedef struct {
    char control_socket[1024];
    char daemon_socket[1024];
    ls_ctl1_status service;
    ls_ui_status status;
    int controller_available;
    char error[256];
} ls_app;

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
    if (ls_join_path(app->control_socket, sizeof(app->control_socket), runtime,
                     "services/org.umrk.syncthing/control.sock") != 0) return -1;
    if (daemon && daemon[0]) {
        if (snprintf(app->daemon_socket, sizeof(app->daemon_socket), "%s", daemon) >=
            (int)sizeof(app->daemon_socket)) return -1;
    } else if (ls_join_path(app->daemon_socket, sizeof(app->daemon_socket), runtime,
                            "jawakad.sock") != 0) return -1;
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
        char local[32], global[32], detail[1024];
        ls_format_bytes(folder->local_bytes, local, sizeof(local));
        ls_format_bytes(folder->global_bytes, global, sizeof(global));
        snprintf(detail, sizeof(detail), "%s\nPath: %s\nType: %s\nState: %s\nLocal: %s\nGlobal: %s\nPeers: %d\nLast sync: %s\nVersioning: %s",
                 folder->label, folder->path, folder->type, folder->state,
                 local, global, folder->peer_count,
                 folder->last_sync[0] ? folder->last_sync : "Unknown",
                 folder->versioning[0] ? folder->versioning : "Off");
        ls_message(detail);
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
        cat_option value_options[7];
        cat_options_item items[9];
        cat_footer_item footer[] = {{.button = CAT_BTN_B, .label = "Exit"},
                                    {.button = CAT_BTN_A, .label = "Choose", .is_confirm = true}};
        cat_options_list_opts options = {0};
        cat_options_list_result result = {0};
        char service_value[64];
        char card_value[32];
        char folder_value[32];
        char issue_value[32];
        int item_count = 0;
        int action;
        ls_refresh(app);
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
            snprintf(issue_value, sizeof(issue_value), "%d", app->status.issue_count);
            value_options[1] = (cat_option){.label = card_value, .value = card_value};
            value_options[2] = (cat_option){.label = folder_value, .value = folder_value};
            value_options[3] = (cat_option){.label = app->status.upstream_version, .value = app->status.upstream_version};
            value_options[4] = (cat_option){
                .label = app->status.network_present ? app->status.network_profile : "unavailable",
                .value = app->status.network_present ? app->status.network_profile : "unavailable"};
            value_options[5] = (cat_option){.label = issue_value, .value = issue_value};
            value_options[6] = (cat_option){
                .label = app->status.gateway_present && app->status.gateway_open ? "Open" : "Closed",
                .value = app->status.gateway_present && app->status.gateway_open ? "Open" : "Closed"};
            items[item_count++] = (cat_options_item){.label = "Cards", .type = CAT_OPT_CLICKABLE,
                .options = &value_options[1], .option_count = 1};
            items[item_count++] = (cat_options_item){.label = "Folders", .type = CAT_OPT_CLICKABLE,
                .options = &value_options[2], .option_count = 1};
            items[item_count++] = (cat_options_item){.label = "My Device", .type = CAT_OPT_CLICKABLE,
                .options = &value_options[3], .option_count = 1};
            items[item_count++] = (cat_options_item){.label = "Network", .type = CAT_OPT_CLICKABLE,
                .options = &value_options[4], .option_count = 1};
            items[item_count++] = (cat_options_item){.label = "Web Interface", .type = CAT_OPT_CLICKABLE,
                .options = &value_options[6], .option_count = 1};
            items[item_count++] = (cat_options_item){.label = "Issues", .type = CAT_OPT_CLICKABLE,
                .options = &value_options[5], .option_count = 1};
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
        } else if (app->controller_available && focus == 3) {
            ls_show_cards(app);
        } else if (app->controller_available && focus == 4) {
            ls_show_folders(app);
        } else if (app->controller_available && focus == 5) {
            char detail[512];
            snprintf(detail, sizeof(detail), "Syncthing %s\n\nDevice ID:\n%s",
                     app->status.upstream_version, app->status.device_id);
            ls_message(detail);
        } else if (app->controller_available && focus == 6) {
            ls_change_network(app);
        } else if (app->controller_available && focus == 7) {
            ls_show_gateway(app);
        } else if (app->controller_available && focus == 8) {
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
