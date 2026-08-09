#define CAT_IMPLEMENTATION
#include "catastrophe.h"
#define CAT_WIDGETS_IMPLEMENTATION
#include "catastrophe_widgets.h"
#include "cjson/cJSON.c"

#include "cat_bridge.h"

#include <stdatomic.h>

#define CATUI_TEXTURE_CAP 128
#define CATUI_FALLBACK_CAP 6
#define CATUI_RUN_BYTES 1024

typedef struct {
    SDL_Texture *texture;
    uint32_t generation;
    int width;
    int height;
} catui_texture_slot;

static struct {
    int initialized;
    pthread_t owner;
    catui_texture_slot textures[CATUI_TEXTURE_CAP];
    TTF_Font *fallbacks[CAT_FONT_TIER_COUNT][CATUI_FALLBACK_CAP];
    char fallback_dir[PATH_MAX];
    atomic_uint wake_count;
    SDL_Texture *capture_target;
} catui__state;

static const char *catui__fallback_names[CATUI_FALLBACK_CAP] = {
    "font.ttf",
    "font_fallback_arabic.ttf",
    "font_fallback_devanagari.ttf",
    "font_fallback_emoji.ttf",
    "font_fallback_hebrew.ttf",
    "font_fallback_thai.ttf",
};

static const int catui__font_base_sizes[CAT_FONT_TIER_COUNT] = {24, 16, 14, 12, 10, 7};

static int catui__owner(void) {
    return catui__state.initialized && pthread_equal(pthread_self(), catui__state.owner);
}

static int catui__guard(void) {
    if (!catui__state.initialized) return CATUI_CLOSED;
    return catui__owner() ? CATUI_OK : CATUI_WRONG_THREAD;
}

int catui_keyboard(const char *initial_text, char *out_text,
                   size_t out_size, int *accepted) {
    int guard = catui__guard();
    if (guard != CATUI_OK) return guard;
    if (!out_text || out_size == 0 || !accepted) return CATUI_ERROR;

    cat_keyboard_result result = {0};
    int rc = cat_keyboard(initial_text ? initial_text : "", NULL,
                          CAT_KB_GENERAL, &result);
    if (rc == CAT_ERROR) return CATUI_ERROR;
    *accepted = rc == CAT_OK ? 1 : 0;
    snprintf(out_text, out_size, "%s", result.text);
    return CATUI_OK;
}

static cat_draw_color catui__color(uint32_t color) {
    cat_draw_color out = {
        (uint8_t)(color & 0xffu),
        (uint8_t)((color >> 8) & 0xffu),
        (uint8_t)((color >> 16) & 0xffu),
        (uint8_t)((color >> 24) & 0xffu),
    };
    return out;
}

static uint32_t catui__pack_color(cat_draw_color color) {
    return (uint32_t)color.r | ((uint32_t)color.g << 8) |
           ((uint32_t)color.b << 16) | ((uint32_t)color.a << 24);
}

static void catui__close_fallbacks(void) {
    for (int tier = 0; tier < CAT_FONT_TIER_COUNT; tier++) {
        for (int i = 0; i < CATUI_FALLBACK_CAP; i++) {
            if (catui__state.fallbacks[tier][i]) {
                TTF_CloseFont(catui__state.fallbacks[tier][i]);
                catui__state.fallbacks[tier][i] = NULL;
            }
        }
    }
}

static void catui__load_fallbacks(void) {
    catui__close_fallbacks();
    if (!catui__state.fallback_dir[0]) return;

    int bump = cat_get_font_bump();
    for (int tier = 0; tier < CAT_FONT_TIER_COUNT; tier++) {
        int size = cat_font_size_for_resolution(catui__font_base_sizes[tier] + bump);
        if (size < 8) size = 8;
        for (int i = 0; i < CATUI_FALLBACK_CAP; i++) {
            char path[PATH_MAX];
            if (snprintf(path, sizeof(path), "%s/%s", catui__state.fallback_dir,
                         catui__fallback_names[i]) >= (int)sizeof(path)) {
                continue;
            }
            TTF_Font *font = TTF_OpenFont(path, size);
            if (font) {
                TTF_SetFontStyle(font, TTF_STYLE_BOLD);
                catui__state.fallbacks[tier][i] = font;
            }
        }
    }
}

int catui_init(const char *title, const char *font_path,
               const char *fallback_fonts_dir, const char *log_path) {
    if (catui__state.initialized) return CATUI_ERROR;
    memset(&catui__state, 0, sizeof(catui__state));
    catui__state.owner = pthread_self();
    atomic_init(&catui__state.wake_count, 0);

    cat_config config;
    memset(&config, 0, sizeof(config));
    config.window_title = title && title[0] ? title : "Itch.io Catastrophe proof";
    config.font_path = font_path && font_path[0] ? font_path : NULL;
    config.log_path = log_path && log_path[0] ? log_path : NULL;
    config.disable_background = false;
    if (cat_init(&config) != CAT_OK) return CATUI_ERROR;

    catui__state.initialized = 1;
    if (fallback_fonts_dir && fallback_fonts_dir[0]) {
        snprintf(catui__state.fallback_dir, sizeof(catui__state.fallback_dir),
                 "%s", fallback_fonts_dir);
    }
    for (int i = 0; i < CATUI_TEXTURE_CAP; i++) {
        catui__state.textures[i].generation = 1;
    }
    catui__load_fallbacks();

    return CATUI_OK;
}

int catui_quit(void) {
    int guard = catui__guard();
    if (guard != CATUI_OK) return guard;
    if (catui__state.capture_target) {
        SDL_SetRenderTarget(cat_get_renderer(), NULL);
        SDL_DestroyTexture(catui__state.capture_target);
        catui__state.capture_target = NULL;
    }
    for (int i = 0; i < CATUI_TEXTURE_CAP; i++) {
        if (catui__state.textures[i].texture) {
            SDL_DestroyTexture(catui__state.textures[i].texture);
            catui__state.textures[i].texture = NULL;
            catui__state.textures[i].generation++;
        }
    }
    catui__close_fallbacks();
    cat_quit();
    catui__state.initialized = 0;
    return CATUI_OK;
}

int catui_is_owner_thread(void) { return catui__owner(); }
int catui_screen_width(void) { return catui__guard() == CATUI_OK ? cat_get_screen_width() : 0; }
int catui_screen_height(void) { return catui__guard() == CATUI_OK ? cat_get_screen_height() : 0; }
int catui_scale(int value) { return catui__guard() == CATUI_OK ? cat_scale(value) : 0; }
uint32_t catui_ticks(void) { return SDL_GetTicks(); }
void catui_request_frame(void) { if (catui__guard() == CATUI_OK) cat_request_frame(); }
void catui_request_frame_in(uint32_t ms) { if (catui__guard() == CATUI_OK) cat_request_frame_in(ms); }

int catui_poll_input(catui_input_event *out) {
    int guard = catui__guard();
    if (guard != CATUI_OK) return guard;
    if (!out) return CATUI_ERROR;
    memset(out, 0, sizeof(*out));

    if (atomic_exchange_explicit(&catui__state.wake_count, 0,
                                 memory_order_acq_rel) > 0) {
        out->wake = 1;
        return 1;
    }

    cat_input_event event;
    if (!cat_poll_input(&event)) return 0;
    out->button = event.button;
    out->pressed = event.pressed ? 1 : 0;
    out->repeated = event.repeated ? 1 : 0;
    return 1;
}

int catui_wake(void) {
    if (!catui__state.initialized) return CATUI_CLOSED;
    atomic_store_explicit(&catui__state.wake_count, 1, memory_order_release);
    /* Desktop cat_present() pumps SDL while idle, so a user event wakes it
       immediately. MLP1 polling is bounded by the app while workers are busy;
       keeping that policy here avoids changing Catastrophe's global waiter. */
    SDL_Event event;
    memset(&event, 0, sizeof(event));
    event.type = SDL_USEREVENT;
    SDL_PushEvent(&event);
    return CATUI_OK;
}

int catui_clear(void) {
    int guard = catui__guard();
    if (guard != CATUI_OK) return guard;
    cat_draw_background();
    return CATUI_OK;
}

int catui_present(void) {
    int guard = catui__guard();
    if (guard != CATUI_OK) return guard;
    cat_present();
    return CATUI_OK;
}

int catui_draw_title_in(int x, int y, int w, int h, const char *title) {
    int guard = catui__guard();
    if (guard != CATUI_OK) return guard;
    /* No status bar in the app: draw the title alone, positioned within the box
       the layout carves for it (x/y/w/h already carry the header padding), and
       vertically centered in that box. cat_draw_screen_title top-aligns at y=0
       with no band, which reads as sitting high; this follows the box instead. */
    const char *text = title ? title : "";
    if (text[0] && w > 0) {
        static const cat_font_tier tiers[] = {
            CAT_FONT_EXTRA_LARGE, CAT_FONT_LARGE, CAT_FONT_MEDIUM };
        TTF_Font *font = NULL;
        for (size_t i = 0; i < sizeof(tiers) / sizeof(tiers[0]); i++) {
            TTF_Font *cand = cat_get_font(tiers[i]);
            if (!cand) continue;
            font = cand;
            if (cat_measure_text(cand, text) <= w) break;
        }
        if (font) {
            int ty = y + (h - TTF_FontHeight(font)) / 2;
            if (ty < y) ty = y;
            cat_draw_text_clipped(font, text, x, ty,
                                  cat_get_theme()->text, w);
        }
    }
#if SDL_VERSION_ATLEAST(2, 0, 10)
    /* A dense fallback-text list can otherwise outlive Cat's queued title and
       background texture copies before the footer presents them. */
    SDL_RenderFlush(cat_get_renderer());
#endif
    return CATUI_OK;
}

int catui_title_height(void) {
    if (catui__guard() != CATUI_OK) return 0;
    int title = cat_device_scale(40);
    int status = cat_get_status_bar_height();
    return title > status ? title : status;
}

int catui_hints_enabled(void) {
    return catui__guard() == CATUI_OK && cat_hints_enabled_from_env();
}

int catui_footer_height(void) {
    return catui__guard() == CATUI_OK ? cat_get_footer_height() : 0;
}

int catui_draw_footer(const catui_footer_item *items, int count) {
    int guard = catui__guard();
    if (guard != CATUI_OK) return guard;
    if (!items || count < 0 || count > 16) return CATUI_ERROR;
    cat_footer_item converted[16];
    for (int i = 0; i < count; i++) {
        converted[i].button = (cat_button)items[i].button;
        converted[i].label = items[i].label;
        converted[i].is_confirm = items[i].is_confirm != 0;
        converted[i].button_text = items[i].button_text;
    }
#if SDL_VERSION_ATLEAST(2, 0, 10)
    /* A footer draws its outer group and inner button pills in one call. The
       full-radius sprite path reuses the status atlas between those copies,
       and deferred SDL backends can clip a button badge after later atlas
       state changes. Use Cat's own procedural pill path for this one draw and
       flush between left/action and right/confirm groups. The Go composer has
       already selected narrow labels that fit the available footer width. */
    float pill_ratio = cat__g.theme.pill_radius_ratio;
    if (pill_ratio >= 1.0f && cat__g.status_assets)
        cat__g.theme.pill_radius_ratio = 0.999f;
    cat_footer_item left[16], right[16];
    int left_count = 0, right_count = 0;
    for (int i = 0; i < count; i++) {
        if (converted[i].is_confirm) right[right_count++] = converted[i];
        else left[left_count++] = converted[i];
    }
    SDL_RenderFlush(cat_get_renderer());
    if (left_count > 0 && right_count > 0) {
        cat_draw_footer(left, left_count);
        SDL_RenderFlush(cat_get_renderer());
        cat_draw_footer(right, right_count);
        SDL_RenderFlush(cat_get_renderer());
    } else {
        cat_draw_footer(converted, count);
        SDL_RenderFlush(cat_get_renderer());
    }
    cat__g.theme.pill_radius_ratio = pill_ratio;
#else
    cat_draw_footer(converted, count);
#endif
    return CATUI_OK;
}

static cat_box catui__to_box(const catui_box *box) {
    cat_box out = {0};
    if (box) memcpy(&out, box, sizeof(out));
    return out;
}

static catui_box catui__from_box(cat_box box) {
    catui_box out;
    memcpy(&out, &box, sizeof(out));
    return out;
}

static catui_rect catui__from_rect(SDL_Rect rect) {
    catui_rect out = {rect.x, rect.y, rect.w, rect.h};
    return out;
}

catui_rect catui_box_content(const catui_box *box) {
    cat_box converted = catui__to_box(box);
    return catui__from_rect(cat_box_content(&converted));
}

catui_box catui_box_carve_top(catui_box *box, int height) {
    cat_box converted = catui__to_box(box);
    cat_box strip = cat_box_carve_top(&converted, height);
    if (box) *box = catui__from_box(converted);
    return catui__from_box(strip);
}

catui_box catui_box_carve_bottom(catui_box *box, int height) {
    cat_box converted = catui__to_box(box);
    cat_box strip = cat_box_carve_bottom(&converted, height);
    if (box) *box = catui__from_box(converted);
    return catui__from_box(strip);
}

void catui_box_split_cols(const catui_box *box, int left_w, int gutter,
                          catui_box *left, catui_box *right) {
    cat_box converted = catui__to_box(box), c_left = {0}, c_right = {0};
    cat_box_split_cols(&converted, left_w, gutter, &c_left, &c_right);
    if (left) *left = catui__from_box(c_left);
    if (right) *right = catui__from_box(c_right);
}

catui_rect catui_box_fit_rows(const catui_box *box, int base_item_h,
                              int item_count, int *visible_rows, int *item_h) {
    cat_box converted = catui__to_box(box);
    return catui__from_rect(cat_box_fit_rows(&converted, base_item_h, item_count,
                                              visible_rows, item_h));
}

uint32_t catui_theme_color(int role) {
    if (catui__guard() != CATUI_OK) return 0;
    cat_theme *theme = cat_get_theme();
    cat_draw_color color = theme->text;
    switch (role) {
        case CATUI_ROLE_BACKGROUND: color = theme->background; break;
        case CATUI_ROLE_TEXT: color = theme->text; break;
        case CATUI_ROLE_HIGHLIGHTED_TEXT: color = theme->highlighted_text; break;
        case CATUI_ROLE_HIGHLIGHT: color = theme->highlight; break;
        case CATUI_ROLE_ACCENT: color = theme->accent; break;
        case CATUI_ROLE_HINT: color = theme->hint; break;
        case CATUI_ROLE_EMPHASIS: color = theme->emphasis; break;
        case CATUI_ROLE_DISABLED: color = theme->disabled; break;
        case CATUI_ROLE_BUTTON_LABEL: color = theme->button_label; break;
        default: break;
    }
    return catui__pack_color(color);
}

int catui_font_height(int tier) {
    if (catui__guard() != CATUI_OK) return 0;
    TTF_Font *font = cat_get_font((cat_font_tier)tier);
    return font ? TTF_FontHeight(font) : 0;
}

int catui_font_bump(void) { return catui__guard() == CATUI_OK ? cat_get_font_bump() : 0; }

int catui_set_font_bump(int bump) {
    int guard = catui__guard();
    if (guard != CATUI_OK) return guard;
    if (cat_set_font_bump(bump) != CAT_OK) return CATUI_ERROR;
    catui__load_fallbacks();
    return CATUI_OK;
}

int catui_measure_text(int tier, const char *text) {
    if (catui__guard() != CATUI_OK || !text) return 0;
    return cat_measure_text(cat_get_font((cat_font_tier)tier), text);
}

int catui_draw_text(int tier, const char *text, int x, int y,
                    uint32_t color, int max_w, int ellipsize) {
    int guard = catui__guard();
    if (guard != CATUI_OK) return guard;
    TTF_Font *font = cat_get_font((cat_font_tier)tier);
    if (!font || !text) return CATUI_ERROR;
    if (max_w > 0 && ellipsize)
        return cat_draw_text_ellipsized(font, text, x, y, catui__color(color), max_w);
    if (max_w > 0)
        return cat_draw_text_clipped(font, text, x, y, catui__color(color), max_w);
    return cat_draw_text(font, text, x, y, catui__color(color));
}

static int catui__decode_utf8(const unsigned char *text, uint32_t *codepoint) {
    if (!text || !text[0]) return 0;
    if (text[0] < 0x80) { *codepoint = text[0]; return 1; }
    if ((text[0] & 0xe0) == 0xc0 && (text[1] & 0xc0) == 0x80) {
        *codepoint = ((uint32_t)(text[0] & 0x1f) << 6) | (text[1] & 0x3f); return 2;
    }
    if ((text[0] & 0xf0) == 0xe0 && (text[1] & 0xc0) == 0x80 &&
        (text[2] & 0xc0) == 0x80) {
        *codepoint = ((uint32_t)(text[0] & 0x0f) << 12) |
                     ((uint32_t)(text[1] & 0x3f) << 6) | (text[2] & 0x3f); return 3;
    }
    if ((text[0] & 0xf8) == 0xf0 && (text[1] & 0xc0) == 0x80 &&
        (text[2] & 0xc0) == 0x80 && (text[3] & 0xc0) == 0x80) {
        *codepoint = ((uint32_t)(text[0] & 0x07) << 18) |
                     ((uint32_t)(text[1] & 0x3f) << 12) |
                     ((uint32_t)(text[2] & 0x3f) << 6) | (text[3] & 0x3f); return 4;
    }
    *codepoint = 0xfffd;
    return 1;
}

static TTF_Font *catui__font_for_codepoint(int tier, uint32_t codepoint) {
    TTF_Font *primary = cat_get_font((cat_font_tier)tier);
    if (codepoint == ' ' || codepoint == '\t' || codepoint == '\n' ||
        (primary && TTF_GlyphIsProvided32(primary, codepoint))) return primary;
    for (int i = 0; i < CATUI_FALLBACK_CAP; i++) {
        TTF_Font *font = catui__state.fallbacks[tier][i];
        if (font && TTF_GlyphIsProvided32(font, codepoint)) return font;
    }
    return NULL;
}

static int catui__draw_transient_text(TTF_Font *font, const char *text,
                                      int x, int y, cat_draw_color color) {
    if (!font || !text || !text[0]) return 0;
    SDL_Surface *surface = TTF_RenderUTF8_Blended(font, text, color);
    if (!surface) return 0;
    int width = surface->w, height = surface->h;
    SDL_Texture *texture = SDL_CreateTextureFromSurface(cat_get_renderer(), surface);
    SDL_FreeSurface(surface);
    if (!texture) return 0;
    SDL_SetTextureBlendMode(texture, SDL_BLENDMODE_BLEND);
    SDL_SetTextureColorMod(texture, 255, 255, 255);
    SDL_SetTextureAlphaMod(texture, 255);
    SDL_Rect destination = {x, y, width, height};
    SDL_RenderCopy(cat_get_renderer(), texture, NULL, &destination);
#if defined(PLATFORM_MLP1) && SDL_VERSION_ATLEAST(2, 0, 10)
    SDL_RenderFlush(cat_get_renderer());
#endif
    SDL_DestroyTexture(texture);
    return width;
}

static int catui__flush_run(TTF_Font *font, char *run, int *used,
                            int draw, int x, int y, cat_draw_color color) {
    if (!font || !run || !used || *used <= 0) return 0;
    run[*used] = '\0';
    int width = draw ? catui__draw_transient_text(font, run, x, y, color)
                     : cat_measure_text(font, run);
    *used = 0;
    return width;
}

static int catui__fallback_text(int tier, const char *text, int draw,
                                int x, int y, cat_draw_color color, int max_w) {
    if (tier < 0 || tier >= CAT_FONT_TIER_COUNT || !text) return 0;
    SDL_Renderer *renderer = cat_get_renderer();
    SDL_Rect previous = {0};
    SDL_bool had_clip = SDL_RenderIsClipEnabled(renderer);
    if (had_clip) SDL_RenderGetClipRect(renderer, &previous);
    if (draw && max_w > 0) {
        SDL_Rect clip = {x, y, max_w, cat_get_screen_height() - y};
        SDL_RenderSetClipRect(renderer, &clip);
    }

    char run[CATUI_RUN_BYTES + 1];
    int used = 0, width = 0;
    TTF_Font *run_font = NULL;
    const unsigned char *cursor = (const unsigned char *)text;
    while (*cursor) {
        uint32_t codepoint = 0;
        int bytes = catui__decode_utf8(cursor, &codepoint);
        if (bytes <= 0) break;
        TTF_Font *font = catui__font_for_codepoint(tier, codepoint);
        if (!font) { cursor += bytes; continue; }
        if (font != run_font || used + bytes > CATUI_RUN_BYTES) {
            width += catui__flush_run(run_font, run, &used, draw, x + width, y, color);
            run_font = font;
        }
        memcpy(run + used, cursor, (size_t)bytes);
        used += bytes;
        cursor += bytes;
    }
    width += catui__flush_run(run_font, run, &used, draw, x + width, y, color);

#if SDL_VERSION_ATLEAST(2, 0, 10)
    if (draw) SDL_RenderFlush(renderer);
#endif
    if (draw && max_w > 0) SDL_RenderSetClipRect(renderer, had_clip ? &previous : NULL);
    return width;
}

int catui_measure_fallback_text(int tier, const char *text) {
    if (catui__guard() != CATUI_OK) return 0;
    return catui__fallback_text(tier, text, 0, 0, 0, (cat_draw_color){0}, 0);
}

int catui_draw_fallback_text(int tier, const char *text, int x, int y,
                             uint32_t color, int max_w) {
    int guard = catui__guard();
    if (guard != CATUI_OK) return guard;
    return catui__fallback_text(tier, text, 1, x, y, catui__color(color), max_w);
}

int catui_draw_rect(int x, int y, int w, int h, uint32_t color) {
    int guard = catui__guard(); if (guard != CATUI_OK) return guard;
    cat_draw_rect(x, y, w, h, catui__color(color)); return CATUI_OK;
}
int catui_draw_rounded_rect(int x, int y, int w, int h, int radius,
                            uint32_t color) {
    int guard = catui__guard(); if (guard != CATUI_OK) return guard;
    cat_draw_rounded_rect(x, y, w, h, radius, catui__color(color));
    return CATUI_OK;
}
int catui_draw_pill(int x, int y, int w, int h, uint32_t color) {
    int guard = catui__guard(); if (guard != CATUI_OK) return guard;
#if SDL_VERSION_ATLEAST(2, 0, 10)
    /* Dense list rows can queue Cat's reusable rounded sprite until the footer
       mutates it. Bound the selected-row copy inside this pak's bridge. */
    SDL_RenderFlush(cat_get_renderer());
#endif
    cat_draw_pill(x, y, w, h, catui__color(color));
#if SDL_VERSION_ATLEAST(2, 0, 10)
    SDL_RenderFlush(cat_get_renderer());
#endif
    return CATUI_OK;
}
int catui_draw_progress(int x, int y, int w, int h, float progress,
                        uint32_t foreground, uint32_t background) {
    int guard = catui__guard(); if (guard != CATUI_OK) return guard;
#if SDL_VERSION_ATLEAST(2, 0, 10)
    /* The rounded progress bar and footer share Cat's reusable pill sprite.
       Finish earlier title/text copies before the bar mutates that sprite, and
       finish the bar before footer composition reuses it. Keep this isolation
       local to the pak's async download screen. */
    SDL_RenderFlush(cat_get_renderer());
#endif
    cat_draw_progress_bar(x, y, w, h, progress, catui__color(foreground),
                          catui__color(background));
#if SDL_VERSION_ATLEAST(2, 0, 10)
    SDL_RenderFlush(cat_get_renderer());
#endif
    return CATUI_OK;
}
int catui_draw_scrollbar(int x, int y, int h, int visible, int total,
                         int offset) {
    int guard = catui__guard(); if (guard != CATUI_OK) return guard;
    cat_draw_scrollbar(x, y, h, visible, total, offset); return CATUI_OK;
}
int catui_draw_triangle(int x, int y, int w, int h, int direction,
                        uint32_t color) {
    int guard = catui__guard(); if (guard != CATUI_OK) return guard;
    cat_draw_triangle(x, y, w, h, (cat_dir)direction, catui__color(color)); return CATUI_OK;
}
int catui_set_clip(int x, int y, int w, int h) {
    int guard = catui__guard(); if (guard != CATUI_OK) return guard;
    SDL_Rect clip = {x, y, w, h}; return SDL_RenderSetClipRect(cat_get_renderer(), &clip);
}
int catui_reset_clip(void) {
    int guard = catui__guard(); if (guard != CATUI_OK) return guard;
    return SDL_RenderSetClipRect(cat_get_renderer(), NULL);
}

static catui_texture_slot *catui__texture_slot(catui_texture_id id) {
    if (!id) return NULL;
    uint32_t index = (uint32_t)(id & 0xffffffffu);
    uint32_t generation = (uint32_t)(id >> 32);
    if (index == 0 || index > CATUI_TEXTURE_CAP) return NULL;
    catui_texture_slot *slot = &catui__state.textures[index - 1];
    return slot->texture && slot->generation == generation ? slot : NULL;
}

int catui_texture_capacity(void) { return CATUI_TEXTURE_CAP; }

static catui_texture_id catui__store_texture(SDL_Texture *texture, int width, int height) {
    if (!texture) return 0;
    for (int i = 0; i < CATUI_TEXTURE_CAP; i++) {
        catui_texture_slot *slot = &catui__state.textures[i];
        if (!slot->texture) {
            slot->texture = texture;
            slot->width = width;
            slot->height = height;
            if (slot->generation == 0) slot->generation = 1;
            return ((uint64_t)slot->generation << 32) | (uint32_t)(i + 1);
        }
    }
    SDL_DestroyTexture(texture);
    return 0;
}

catui_texture_id catui_texture_create_rgba(const void *pixels, int width,
                                           int height, int stride) {
    if (catui__guard() != CATUI_OK || !pixels || width <= 0 || height <= 0 ||
        stride < width * 4) return 0;
    SDL_Texture *texture = SDL_CreateTexture(cat_get_renderer(), SDL_PIXELFORMAT_RGBA32,
                                             SDL_TEXTUREACCESS_STATIC, width, height);
    if (!texture) return 0;
    if (SDL_UpdateTexture(texture, NULL, pixels, stride) != 0) {
        SDL_DestroyTexture(texture); return 0;
    }
    SDL_SetTextureBlendMode(texture, SDL_BLENDMODE_BLEND);
    return catui__store_texture(texture, width, height);
}

catui_texture_id catui_texture_load(const char *path) {
    if (catui__guard() != CATUI_OK || !path) return 0;
    SDL_Texture *texture = cat_load_image(path);
    if (!texture) return 0;
    int width = 0, height = 0;
    SDL_QueryTexture(texture, NULL, NULL, &width, &height);
    return catui__store_texture(texture, width, height);
}

int catui_texture_size(catui_texture_id texture, int *width, int *height) {
    int guard = catui__guard(); if (guard != CATUI_OK) return guard;
    catui_texture_slot *slot = catui__texture_slot(texture);
    if (!slot) return CATUI_INVALID_TEXTURE;
    if (width) *width = slot->width;
    if (height) *height = slot->height;
    return CATUI_OK;
}

int catui_texture_draw(catui_texture_id texture, int x, int y, int w, int h) {
    int guard = catui__guard(); if (guard != CATUI_OK) return guard;
    catui_texture_slot *slot = catui__texture_slot(texture);
    if (!slot) return CATUI_INVALID_TEXTURE;
#if SDL_VERSION_ATLEAST(2, 0, 10)
    /* The MLP1 renderer batches texture copies aggressively. Isolate an
       app-owned texture from queued Cat text/status copies so switching GIF
       frames cannot make an earlier copy observe the new texture state. */
    SDL_RenderFlush(cat_get_renderer());
#endif
    cat_draw_image(slot->texture, x, y, w, h);
#if SDL_VERSION_ATLEAST(2, 0, 10)
    SDL_RenderFlush(cat_get_renderer());
#endif
    return CATUI_OK;
}

int catui_texture_destroy(catui_texture_id texture) {
    int guard = catui__guard(); if (guard != CATUI_OK) return guard;
    catui_texture_slot *slot = catui__texture_slot(texture);
    if (!slot) return CATUI_INVALID_TEXTURE;
    SDL_DestroyTexture(slot->texture);
    slot->texture = NULL;
    slot->width = slot->height = 0;
    slot->generation++;
    if (slot->generation == 0) slot->generation = 1;
    return CATUI_OK;
}

int catui_capture_begin(void) {
    int guard = catui__guard(); if (guard != CATUI_OK) return guard;
    SDL_Renderer *renderer = cat_get_renderer();
    if (catui__state.capture_target || SDL_GetRenderTarget(renderer) != NULL)
        return CATUI_ERROR;
    SDL_Texture *target = SDL_CreateTexture(renderer, SDL_PIXELFORMAT_RGBA32,
                                            SDL_TEXTUREACCESS_TARGET,
                                            cat_get_screen_width(), cat_get_screen_height());
    if (!target) return CATUI_ERROR;
    SDL_SetTextureBlendMode(target, SDL_BLENDMODE_NONE);
#if SDL_VERSION_ATLEAST(2, 0, 10)
    /* Do not let queued commands for the visible backbuffer cross into the
       deterministic fixture target when SDL_SetRenderTarget synchronizes. */
    SDL_RenderFlush(renderer);
#endif
    if (SDL_SetRenderTarget(renderer, target) != 0) {
        SDL_DestroyTexture(target);
        return CATUI_ERROR;
    }
    catui__state.capture_target = target;
    return CATUI_OK;
}

int catui_capture_end(void) {
    int guard = catui__guard(); if (guard != CATUI_OK) return guard;
    if (!catui__state.capture_target) return CATUI_ERROR;
#if SDL_VERSION_ATLEAST(2, 0, 10)
    SDL_RenderFlush(cat_get_renderer());
#endif
    SDL_Texture *target = catui__state.capture_target;
    catui__state.capture_target = NULL;
    if (SDL_SetRenderTarget(cat_get_renderer(), NULL) != 0) {
        SDL_DestroyTexture(target);
        return CATUI_ERROR;
    }
    SDL_DestroyTexture(target);
    return CATUI_OK;
}

int catui_screenshot_png(const char *path) {
    int guard = catui__guard(); if (guard != CATUI_OK) return guard;
    if (!path || !path[0]) return CATUI_ERROR;
    int width = cat_get_screen_width(), height = cat_get_screen_height();
    SDL_Surface *surface = SDL_CreateRGBSurfaceWithFormat(0, width, height, 32,
                                                          SDL_PIXELFORMAT_RGBA32);
    if (!surface) return CATUI_ERROR;
#if SDL_VERSION_ATLEAST(2, 0, 10)
    /* Make deterministic fixture readback independent of the renderer's
       command-batching policy. SDL_RenderReadPixels is a sync point on most
       backends, but SDL_RenderFlush is the explicit contract. */
    SDL_RenderFlush(cat_get_renderer());
#endif
    int result = SDL_RenderReadPixels(cat_get_renderer(), NULL, SDL_PIXELFORMAT_RGBA32,
                                      surface->pixels, surface->pitch);
    if (result == 0) result = IMG_SavePNG(surface, path);
    SDL_FreeSurface(surface);
    return result == 0 ? CATUI_OK : CATUI_ERROR;
}
