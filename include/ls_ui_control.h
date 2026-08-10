#ifndef LS_UI_CONTROL_H
#define LS_UI_CONTROL_H

#include <stdbool.h>
#include <stddef.h>

#define LS_UI_MAX_CARDS 16
#define LS_UI_MAX_FOLDERS 32
#define LS_UI_MAX_ISSUES 64
#define LS_UI_MAX_CAPABILITIES 32
#define LS_UI_MAX_NETWORKS 32
#define LS_UI_MAX_PEERS 32
#define LS_UI_MAX_FOLDER_OFFERS 32
#define LS_UI_MAX_FOLDER_DEVICES 33
#define LS_UI_MAX_RESET_PATHS 64
#define LS_UI_MAX_STORAGE_ROWS 128
#define LS_UI_MAX_CONFLICTS 64

typedef struct {
    char code[64];
    char message[256];
    char scope[32];
    char subject_id[128];
} ls_ui_issue;

typedef struct {
    char id[128];
    char source_id[65];
    char id_suffix[16];
    char slot[32];
    char root[512];
    char state[32];
    bool enrolled;
    bool present;
    bool writable;
    bool duplicate_id;
    long long retained_bytes;
} ls_ui_card;

typedef struct {
    char id[128];
    char label[128];
    char card_id[128];
    char kind[32];
    char path[512];
    char type[32];
    char state[32];
    bool paused;
    bool pending_rescan;
    long long local_bytes;
    long long global_bytes;
    int local_items;
    int global_items;
    long long need_bytes;
    int need_items;
    char remote_state[32];
    char remote_peer[65];
    long long remote_need_bytes;
    int remote_need_items;
    int peer_count;
    char device_ids[LS_UI_MAX_FOLDER_DEVICES][128];
    int device_count;
    char last_sync[64];
    char versioning[64];
    char first_sync_state[32];
    char snapshot_name[97];
    int snapshot_files;
    int snapshot_directories;
    long long snapshot_bytes;
    char first_sync_message[241];
    int conflict_count;
    char conflicts[LS_UI_MAX_CONFLICTS][257];
    int conflict_path_count;
} ls_ui_folder;

typedef struct {
    char id[128];
    char id_suffix[16];
    char name[96];
    char state[32];
    char connection[32];
    char address[256];
    bool paused;
    bool introducer;
    bool pending;
} ls_ui_peer;

typedef struct {
    char folder_id[65];
    char label[97];
    char device_id[128];
    char device_id_suffix[16];
    char device_name[65];
    char offered_at[65];
    bool receive_encrypted;
    bool remote_encrypted;
    bool ignored;
} ls_ui_folder_offer;

typedef struct {
    char card_suffix[16];
    char category[16];
    char kind[16];
    char name[160];
    long long bytes;
} ls_ui_storage_row;

typedef struct {
    char plan_id[33];
    char source_id[65];
    char card_id[129];
    char kind[16];
    char folder_type[32];
    char folder_id[65];
    char label[97];
    char path[1025];
    int file_count;
    int directory_count;
    long long content_bytes;
    long long available_bytes;
    bool snapshot_possible;
    int peer_count;
    bool states_warning;
    bool join_existing;
    char offer_device_id[128];
    char expires_at[65];
} ls_ui_onboarding;

typedef struct {
    char controller[32];
    char upstream_state[32];
    char upstream_version[64];
    char device_id[128];
    bool game_active;
    char game_launch_id[128];
    char game_source_id[64];
    char recovery_state[32];
    bool recovery_changed;
    char reset_plan_id[40];
    char reset_plan_action[32];
    char reset_remove_paths[LS_UI_MAX_RESET_PATHS][768];
    int reset_remove_count;
    char reset_retained_paths[LS_UI_MAX_RESET_PATHS][768];
    int reset_retained_count;
    bool network_present;
    char network_profile[32];
    char allowed_networks[LS_UI_MAX_NETWORKS][64];
    int allowed_network_count;
    bool network_route_changed;
    bool gateway_present;
    bool gateway_open;
    bool gateway_pairing;
    char gateway_url[256];
    char gateway_pin[8];
    char gateway_qr_url[512];
    char gateway_offer_expires[64];
    char gateway_fingerprint[128];
    int gateway_trusted_browsers;
    char gateway_extension_expires[64];
    bool transfer_present;
    char transfer_state[32];
    long long transfer_local_bytes;
    long long transfer_global_bytes;
    long long transfer_need_bytes;
    long long transfer_in_bytes;
    long long transfer_out_bytes;
    bool logging_present;
    char log_level[16];
    char debug_expires[64];
    bool storage_present;
    long long snapshot_bytes;
    long long version_bytes;
    int snapshot_count;
    int version_groups;
    ls_ui_storage_row storage_rows[LS_UI_MAX_STORAGE_ROWS];
    int storage_row_count;
    char diagnostics_path[768];
    char diagnostics_exported[64];
    bool onboarding_present;
    ls_ui_onboarding onboarding;
    ls_ui_card cards[LS_UI_MAX_CARDS];
    int card_count;
    ls_ui_folder folders[LS_UI_MAX_FOLDERS];
    int folder_count;
    ls_ui_peer peers[LS_UI_MAX_PEERS];
    int peer_count;
    ls_ui_folder_offer folder_offers[LS_UI_MAX_FOLDER_OFFERS];
    int folder_offer_count;
    ls_ui_issue issues[LS_UI_MAX_ISSUES];
    int issue_count;
    char capabilities[LS_UI_MAX_CAPABILITIES][64];
    int capability_count;
} ls_ui_status;

typedef enum {
    LS_UI_NEEDS_ATTENTION = 0,
    LS_UI_SYNCING = 1,
    LS_UI_UP_TO_DATE = 2,
} ls_ui_top_state;

typedef struct {
    ls_ui_top_state state;
    long long need_bytes;
    int need_items;
    char message[256];
} ls_ui_status_summary;

int ls_ui_status_get(const char *socket_path,
                     ls_ui_status *status,
                     char *error,
                     size_t error_size);
int ls_ui_card_enroll(const char *socket_path,
                      const char *source_id,
                      ls_ui_status *status,
                      char *error,
                      size_t error_size);
int ls_ui_network_profile_set(const char *socket_path,
                              const char *profile,
                              ls_ui_status *status,
                              char *error,
                              size_t error_size);
int ls_ui_gateway_action(const char *socket_path,
                         const char *operation,
                         bool confirmed,
                         ls_ui_status *status,
                         char *error,
                         size_t error_size);
int ls_ui_folder_action(const char *socket_path,
                        const char *operation,
                        const char *folder_id,
                        const char *label,
                        ls_ui_status *status,
                        char *error,
                        size_t error_size);
int ls_ui_folder_onboard_plan(const char *socket_path,
                              const char *source_id,
                              const char *kind,
                              const char *folder_type,
                              const char *const *device_ids,
                              size_t device_count,
                              ls_ui_status *status,
                              char *error,
                              size_t error_size);
int ls_ui_folder_offer_plan(const char *socket_path,
                            const char *folder_id,
                            const char *device_id,
                            const char *source_id,
                            const char *kind,
                            const char *folder_type,
                            ls_ui_status *status,
                            char *error,
                            size_t error_size);
int ls_ui_folder_offer_action(const char *socket_path,
                              const char *folder_id,
                              const char *device_id,
                              bool ignored,
                              ls_ui_status *status,
                              char *error,
                              size_t error_size);
int ls_ui_folder_onboard_create(const char *socket_path,
                                const char *plan_id,
                                bool states_warning_acknowledged,
                                bool manual_edit_warning_acknowledged,
                                ls_ui_status *status,
                                char *error,
                                size_t error_size);
int ls_ui_folder_first_sync_prepare(const char *socket_path,
                                    const char *folder_id,
                                    ls_ui_status *status,
                                    char *error,
                                    size_t error_size);
int ls_ui_folder_first_sync_start(const char *socket_path,
                                  const char *folder_id,
                                  ls_ui_status *status,
                                  char *error,
                                  size_t error_size);
int ls_ui_folder_type_set(const char *socket_path,
                          const char *folder_id,
                          const char *folder_type,
                          ls_ui_status *status,
                          char *error,
                          size_t error_size);
int ls_ui_folder_membership(const char *socket_path,
                            const char *operation,
                            const char *folder_id,
                            const char *device_id,
                            ls_ui_status *status,
                            char *error,
                            size_t error_size);
int ls_ui_device_action(const char *socket_path,
                        const char *operation,
                        const char *device_id,
                        const char *name,
                        ls_ui_status *status,
                        char *error,
                        size_t error_size);
int ls_ui_reset_prepare(const char *socket_path,
                        const char *action,
                        const char *confirmation,
                        ls_ui_status *status,
                        char *error,
                        size_t error_size);
int ls_ui_log_level_set(const char *socket_path,
                        const char *level,
                        ls_ui_status *status,
                        char *error,
                        size_t error_size);
int ls_ui_diagnostics_export(const char *socket_path,
                             ls_ui_status *status,
                             char *error,
                             size_t error_size);
int ls_ui_storage_cleanup(const char *socket_path,
                          const ls_ui_storage_row *row,
                          ls_ui_status *status,
                          char *error,
                          size_t error_size);
int ls_ui_parse_response(const char *payload,
                         size_t payload_size,
                         const char *request_id,
                         ls_ui_status *status,
                         char *error,
                         size_t error_size);
bool ls_ui_has_capability(const ls_ui_status *status, const char *operation);
const char *ls_ui_top_state_label(ls_ui_top_state state);
const char *ls_ui_folder_state_label(const ls_ui_folder *folder);
const char *ls_ui_guided_progress_label(const ls_ui_status *status);
int ls_ui_summarize_status(const ls_ui_status *status, ls_ui_status_summary *summary);

#endif
