#define CAT_IMPLEMENTATION
#include "catastrophe.h"
#define CAT_WIDGETS_IMPLEMENTATION
#include "catastrophe_widgets.h"

#include <stdio.h>

int main(int argc, char **argv) {
    char message[512];
    cat_config configuration = {0};
    cat_footer_item footer[] = {
        {.button = CAT_BTN_A, .label = "OK", .is_confirm = true},
    };
    cat_message_opts options = {0};
    cat_confirm_result result = {0};

    if (argc != 3 || !argv[1][0] || !argv[2][0]) {
        fprintf(stderr, "usage: leaf-syncthing-floor MINIMUM INSTALLED\n");
        return 2;
    }
    if (snprintf(message, sizeof(message),
                 "Syncthing requires Leaf >= %s\nInstalled Leaf: %s",
                 argv[1], argv[2]) >= (int)sizeof(message)) {
        return 2;
    }

    configuration.window_title = "Syncthing";
    configuration.log_path = NULL;
    configuration.cpu_speed = CAT_CPU_SPEED_MENU;
    if (cat_init(&configuration) != CAT_OK) return 1;

    options.message = message;
    options.footer = footer;
    options.footer_count = 1;
    (void)cat_confirmation(&options, &result);
    cat_quit();
    return 0;
}
