#ifndef LS_CTL1_H
#define LS_CTL1_H

#include <stdbool.h>
#include <stddef.h>

typedef struct {
    bool found;
    bool desired_enabled;
    char effective_state[32];
} ls_ctl1_status;

int ls_ctl1_get(const char *socket_path,
                const char *service_id,
                ls_ctl1_status *status);
int ls_ctl1_action(const char *socket_path,
                   const char *operation,
                   const char *service_id,
                   char *error,
                   size_t error_size);
const char *ls_ctl1_state_label(const ls_ctl1_status *status,
                                bool service_control_available,
                                bool controller_available);
const char *ls_ctl1_action_label(const ls_ctl1_status *status,
                                 bool service_control_available);
const char *ls_ctl1_action_operation(const ls_ctl1_status *status,
                                     bool service_control_available);
bool ls_ctl1_should_query_controller(const ls_ctl1_status *status,
                                     bool service_control_available);

#endif
