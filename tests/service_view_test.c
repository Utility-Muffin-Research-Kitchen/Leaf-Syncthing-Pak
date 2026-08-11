#include "ls_ctl1.h"

#include <assert.h>
#include <stdio.h>
#include <string.h>

static ls_ctl1_status status(const char *state) {
    ls_ctl1_status value = {.found = true};
    snprintf(value.effective_state, sizeof(value.effective_state), "%s", state);
    return value;
}

static void check(const char *state, bool controller_available,
                  const char *label, const char *operation) {
    ls_ctl1_status value = status(state);
    assert(strcmp(ls_ctl1_state_label(&value, true, controller_available), label) == 0);
    const char *actual = ls_ctl1_action_operation(&value, true);
    assert((!actual && !operation) || (actual && operation && strcmp(actual, operation) == 0));
    if (operation) {
        assert(strcmp(ls_ctl1_action_label(&value, true),
                      strcmp(operation, "run") == 0 ? "Run Syncthing" : "Stop Syncthing") == 0);
    } else {
        assert(ls_ctl1_action_label(&value, true) == NULL);
    }
}

int main(void) {
    check("disabled", false, "Stopped", "run");
    check("stopped", false, "Stopped", "run");
    check("starting", false, "Starting…", "stop");
    check("running", false, "Starting…", "stop");
    check("running", true, "Running", "stop");
    check("stopping", false, "Stopping…", NULL);
    check("backoff", false, "Retrying…", "stop");
    check("failed", false, "Needs attention", "run");
    check("stale-generation", false, "Needs attention", NULL);
    check("unavailable", false, "Unavailable", NULL);

    ls_ctl1_status running = status("running");
    ls_ctl1_status starting = status("starting");
    ls_ctl1_status missing = {0};
    assert(ls_ctl1_should_query_controller(&running, true));
    assert(!ls_ctl1_should_query_controller(&starting, true));
    assert(!ls_ctl1_should_query_controller(&running, false));
    assert(strcmp(ls_ctl1_state_label(&running, false, false), "Unavailable") == 0);
    assert(strcmp(ls_ctl1_state_label(&missing, true, false), "Unavailable") == 0);
    assert(ls_ctl1_action_label(&missing, true) == NULL);
    assert(!ls_ctl1_should_query_controller(&missing, true));
    puts("PASS service view states and controller probe policy");
    return 0;
}
