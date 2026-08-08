#ifndef LS_UI_CONTROL_H
#define LS_UI_CONTROL_H

#include <stdbool.h>
#include <stddef.h>

#define LS_UI_MAX_CARDS 16
#define LS_UI_MAX_FOLDERS 32
#define LS_UI_MAX_ISSUES 64
#define LS_UI_MAX_CAPABILITIES 32

typedef struct {
    char code[64];
    char message[256];
    char scope[32];
    char subject_id[128];
} ls_ui_issue;

typedef struct {
    char id[128];
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
    int peer_count;
    char last_sync[64];
    char versioning[64];
} ls_ui_folder;

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
    ls_ui_card cards[LS_UI_MAX_CARDS];
    int card_count;
    ls_ui_folder folders[LS_UI_MAX_FOLDERS];
    int folder_count;
    ls_ui_issue issues[LS_UI_MAX_ISSUES];
    int issue_count;
    char capabilities[LS_UI_MAX_CAPABILITIES][64];
    int capability_count;
} ls_ui_status;

int ls_ui_status_get(const char *socket_path,
                     ls_ui_status *status,
                     char *error,
                     size_t error_size);
int ls_ui_card_enroll(const char *socket_path,
                      const char *source_id,
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

#endif
