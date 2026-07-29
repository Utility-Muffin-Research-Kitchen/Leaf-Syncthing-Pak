#ifndef UMRK_ITCHIO_CAT_BRIDGE_H
#define UMRK_ITCHIO_CAT_BRIDGE_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef uint64_t catui_texture_id;

typedef struct {
    int x, y, w, h;
    int pad_t, pad_r, pad_b, pad_l;
} catui_box;

typedef struct {
    int x, y, w, h;
} catui_rect;

typedef struct {
    int button;
    int pressed;
    int repeated;
    int wake;
} catui_input_event;

typedef struct {
    int button;
    const char *label;
    int is_confirm;
    const char *button_text;
} catui_footer_item;

enum {
    CATUI_OK = 0,
    CATUI_ERROR = -1,
    CATUI_WRONG_THREAD = -2,
    CATUI_CLOSED = -3,
    CATUI_INVALID_TEXTURE = -4,
};

enum {
    CATUI_ROLE_BACKGROUND = 0,
    CATUI_ROLE_TEXT,
    CATUI_ROLE_HIGHLIGHTED_TEXT,
    CATUI_ROLE_HIGHLIGHT,
    CATUI_ROLE_ACCENT,
    CATUI_ROLE_HINT,
    CATUI_ROLE_EMPHASIS,
    CATUI_ROLE_DISABLED,
    CATUI_ROLE_BUTTON_LABEL,
};

int catui_init(const char *title, const char *font_path,
               const char *fallback_fonts_dir, const char *log_path);
int catui_quit(void);
int catui_is_owner_thread(void);
int catui_screen_width(void);
int catui_screen_height(void);
int catui_scale(int value);
uint32_t catui_ticks(void);
void catui_request_frame(void);
void catui_request_frame_in(uint32_t ms);

int catui_poll_input(catui_input_event *out);
int catui_wake(void);
int catui_keyboard(const char *initial_text, char *out_text,
                   size_t out_size, int *accepted);

int catui_clear(void);
int catui_present(void);
int catui_draw_title_in(int x, int y, int w, int h, const char *title);
int catui_title_height(void);
int catui_hints_enabled(void);
int catui_footer_height(void);
int catui_draw_footer(const catui_footer_item *items, int count);

catui_rect catui_box_content(const catui_box *box);
catui_box catui_box_carve_top(catui_box *box, int height);
catui_box catui_box_carve_bottom(catui_box *box, int height);
void catui_box_split_cols(const catui_box *box, int left_w, int gutter,
                          catui_box *left, catui_box *right);
catui_rect catui_box_fit_rows(const catui_box *box, int base_item_h,
                              int item_count, int *visible_rows,
                              int *item_h);

uint32_t catui_theme_color(int role);
int catui_font_height(int tier);
int catui_font_bump(void);
int catui_set_font_bump(int bump);
int catui_measure_text(int tier, const char *text);
int catui_draw_text(int tier, const char *text, int x, int y,
                    uint32_t color, int max_w, int ellipsize);
int catui_measure_fallback_text(int tier, const char *text);
int catui_draw_fallback_text(int tier, const char *text, int x, int y,
                             uint32_t color, int max_w);

int catui_draw_rect(int x, int y, int w, int h, uint32_t color);
int catui_draw_rounded_rect(int x, int y, int w, int h, int radius,
                            uint32_t color);
int catui_draw_pill(int x, int y, int w, int h, uint32_t color);
int catui_draw_progress(int x, int y, int w, int h, float progress,
                        uint32_t foreground, uint32_t background);
int catui_draw_triangle(int x, int y, int w, int h, int direction,
                        uint32_t color);
int catui_draw_scrollbar(int x, int y, int h, int visible, int total,
                         int offset);
int catui_set_clip(int x, int y, int w, int h);
int catui_reset_clip(void);

catui_texture_id catui_texture_create_rgba(const void *pixels, int width,
                                           int height, int stride);
catui_texture_id catui_texture_load(const char *path);
int catui_texture_size(catui_texture_id texture, int *width, int *height);
int catui_texture_draw(catui_texture_id texture, int x, int y, int w, int h);
int catui_texture_destroy(catui_texture_id texture);
int catui_texture_capacity(void);
int catui_capture_begin(void);
int catui_capture_end(void);
int catui_screenshot_png(const char *path);

#ifdef __cplusplus
}
#endif

#endif
