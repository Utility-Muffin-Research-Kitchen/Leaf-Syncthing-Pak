package catui

/*
#cgo CFLAGS: -std=gnu11 -I${SRCDIR}/../../../Catastrophe/include
#cgo pkg-config: sdl2 SDL2_image SDL2_ttf
#cgo darwin LDFLAGS: -framework Cocoa
#cgo linux LDFLAGS: -lm -lpthread
#include <stdlib.h>
#include "cat_bridge.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"image"
	"image/draw"
	"sync"
	"unsafe"
)

var (
	ErrClosed         = errors.New("catui: context is closed")
	ErrWrongThread    = errors.New("catui: call made outside the owning OS thread")
	ErrInvalidTexture = errors.New("catui: invalid or destroyed texture")
)

type Button int

const (
	ButtonNone Button = iota
	ButtonUp
	ButtonDown
	ButtonLeft
	ButtonRight
	ButtonA
	ButtonB
	ButtonX
	ButtonY
	ButtonL1
	ButtonL2
	ButtonR1
	ButtonR2
	ButtonStart
	ButtonSelect
	ButtonMenu
	ButtonPower
	ButtonQuit
	ButtonStick
)

type FontTier int

const (
	FontExtraLarge FontTier = iota
	FontLarge
	FontMedium
	FontSmall
	FontTiny
	FontMicro
)

type ThemeRole int

const (
	RoleBackground ThemeRole = iota
	RoleText
	RoleHighlightedText
	RoleHighlight
	RoleAccent
	RoleHint
	RoleEmphasis
	RoleDisabled
	RoleButtonLabel
)

type Direction int

const (
	DirectionLeft Direction = iota
	DirectionRight
	DirectionUp
	DirectionDown
)

type Color uint32

func RGBA(r, g, b, a uint8) Color {
	return Color(uint32(r) | uint32(g)<<8 | uint32(b)<<16 | uint32(a)<<24)
}

type InputEvent struct {
	Button   Button
	Pressed  bool
	Repeated bool
	Wake     bool
}

type Config struct {
	Title            string
	FontPath         string
	FallbackFontsDir string
	LogPath          string
}

type Context struct {
	mu          sync.Mutex
	initialized bool
	closed      bool
	textures    map[*Texture]struct{}
}

func statusError(status C.int) error {
	switch int(status) {
	case int(C.CATUI_OK):
		return nil
	case int(C.CATUI_WRONG_THREAD):
		return ErrWrongThread
	case int(C.CATUI_CLOSED):
		return ErrClosed
	case int(C.CATUI_INVALID_TEXTURE):
		return ErrInvalidTexture
	default:
		return fmt.Errorf("catui: bridge failure (%d)", int(status))
	}
}

func cString(value string) (*C.char, func()) {
	if value == "" {
		return nil, func() {}
	}
	valueC := C.CString(value)
	return valueC, func() { C.free(unsafe.Pointer(valueC)) }
}

func Init(config Config) (*Context, error) {
	title, freeTitle := cString(config.Title)
	defer freeTitle()
	font, freeFont := cString(config.FontPath)
	defer freeFont()
	fallback, freeFallback := cString(config.FallbackFontsDir)
	defer freeFallback()
	logPath, freeLogPath := cString(config.LogPath)
	defer freeLogPath()

	if err := statusError(C.catui_init(title, font, fallback, logPath)); err != nil {
		return nil, err
	}
	return &Context{initialized: true, textures: make(map[*Texture]struct{})}, nil
}

func (c *Context) ensureOpen() error {
	if c == nil || !c.initialized || c.closed {
		return ErrClosed
	}
	return nil
}

func (c *Context) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureOpen(); err != nil {
		return err
	}
	if err := statusError(C.catui_quit()); err != nil {
		return err
	}
	for texture := range c.textures {
		texture.id = 0
		texture.closed = true
	}
	c.textures = nil
	c.closed = true
	return nil
}

// Keyboard runs Catastrophe's device-native blocking keyboard widget. A
// cancelled edit returns the original text with accepted=false.
func (c *Context) Keyboard(initial string) (value string, accepted bool, err error) {
	if err := c.ensureOpen(); err != nil {
		return initial, false, err
	}
	initialC, freeInitial := cString(initial)
	defer freeInitial()
	const outputSize = 1024
	output := C.malloc(outputSize)
	if output == nil {
		return initial, false, errors.New("catui: keyboard allocation failed")
	}
	defer C.free(output)
	var acceptedC C.int
	if err := statusError(C.catui_keyboard(initialC, (*C.char)(output), C.size_t(outputSize), &acceptedC)); err != nil {
		return initial, false, err
	}
	if acceptedC == 0 {
		return initial, false, nil
	}
	return C.GoString((*C.char)(output)), true, nil
}

func (c *Context) IsOwnerThread() bool {
	return c != nil && c.ensureOpen() == nil && C.catui_is_owner_thread() != 0
}

func (c *Context) ScreenSize() (int, int, error) {
	if err := c.ensureOpen(); err != nil {
		return 0, 0, err
	}
	return int(C.catui_screen_width()), int(C.catui_screen_height()), nil
}

func (c *Context) Scale(value int) int { return int(C.catui_scale(C.int(value))) }
func (c *Context) Ticks() uint32       { return uint32(C.catui_ticks()) }
func (c *Context) RequestFrame()       { C.catui_request_frame() }
func (c *Context) RequestFrameIn(ms uint32) {
	C.catui_request_frame_in(C.uint32_t(ms))
}

func (c *Context) PollInput() (InputEvent, bool, error) {
	if err := c.ensureOpen(); err != nil {
		return InputEvent{}, false, err
	}
	var event C.catui_input_event
	status := C.catui_poll_input(&event)
	if status < 0 {
		return InputEvent{}, false, statusError(status)
	}
	if status == 0 {
		return InputEvent{}, false, nil
	}
	return InputEvent{
		Button:   Button(event.button),
		Pressed:  event.pressed != 0,
		Repeated: event.repeated != 0,
		Wake:     event.wake != 0,
	}, true, nil
}

// Wake is the only Context method intended for worker goroutines. It queues a
// wake event without transferring renderer ownership away from the main thread.
func (c *Context) Wake() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureOpen(); err != nil {
		return err
	}
	return statusError(C.catui_wake())
}

func (c *Context) Clear() error {
	if err := c.ensureOpen(); err != nil {
		return err
	}
	return statusError(C.catui_clear())
}

func (c *Context) Present() error {
	if err := c.ensureOpen(); err != nil {
		return err
	}
	return statusError(C.catui_present())
}

func (c *Context) DrawTitleIn(rect Rect, title string) error {
	if err := c.ensureOpen(); err != nil {
		return err
	}
	titleC, freeTitle := cString(title)
	defer freeTitle()
	return statusError(C.catui_draw_title_in(C.int(rect.X), C.int(rect.Y), C.int(rect.W), C.int(rect.H), titleC))
}

func (c *Context) TitleHeight() int   { return int(C.catui_title_height()) }
func (c *Context) HintsEnabled() bool { return C.catui_hints_enabled() != 0 }
func (c *Context) FooterHeight() int  { return int(C.catui_footer_height()) }

type FooterItem struct {
	Button     Button
	Label      string
	ButtonText string
	IsConfirm  bool
}

func (c *Context) DrawFooter(items []FooterItem) error {
	if err := c.ensureOpen(); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	memory := C.calloc(C.size_t(len(items)), C.size_t(unsafe.Sizeof(C.catui_footer_item{})))
	if memory == nil {
		return errors.New("catui: allocate footer items")
	}
	defer C.free(memory)
	cItems := unsafe.Slice((*C.catui_footer_item)(memory), len(items))
	labels := make([]*C.char, len(items))
	buttonTexts := make([]*C.char, len(items))
	for i, item := range items {
		labels[i] = C.CString(item.Label)
		defer C.free(unsafe.Pointer(labels[i]))
		cItems[i].button = C.int(item.Button)
		cItems[i].label = labels[i]
		if item.ButtonText != "" {
			buttonTexts[i] = C.CString(item.ButtonText)
			defer C.free(unsafe.Pointer(buttonTexts[i]))
			cItems[i].button_text = buttonTexts[i]
		}
		cItems[i].is_confirm = 0
		if item.IsConfirm {
			cItems[i].is_confirm = 1
		}
	}
	return statusError(C.catui_draw_footer((*C.catui_footer_item)(memory), C.int(len(items))))
}

type Rect struct{ X, Y, W, H int }

type Box struct {
	X, Y, W, H                           int
	PadTop, PadRight, PadBottom, PadLeft int
}

func NewBox(x, y, w, h, padding int) Box {
	return Box{x, y, w, h, padding, padding, padding, padding}
}

func (b Box) c() C.catui_box {
	return C.catui_box{x: C.int(b.X), y: C.int(b.Y), w: C.int(b.W), h: C.int(b.H),
		pad_t: C.int(b.PadTop), pad_r: C.int(b.PadRight),
		pad_b: C.int(b.PadBottom), pad_l: C.int(b.PadLeft)}
}

func boxFromC(value C.catui_box) Box {
	return Box{int(value.x), int(value.y), int(value.w), int(value.h),
		int(value.pad_t), int(value.pad_r), int(value.pad_b), int(value.pad_l)}
}

func rectFromC(value C.catui_rect) Rect {
	return Rect{int(value.x), int(value.y), int(value.w), int(value.h)}
}

func (b Box) Content() Rect {
	converted := b.c()
	return rectFromC(C.catui_box_content(&converted))
}

func (b *Box) CarveTop(height int) Box {
	converted := b.c()
	strip := C.catui_box_carve_top(&converted, C.int(height))
	*b = boxFromC(converted)
	return boxFromC(strip)
}

func (b *Box) CarveBottom(height int) Box {
	converted := b.c()
	strip := C.catui_box_carve_bottom(&converted, C.int(height))
	*b = boxFromC(converted)
	return boxFromC(strip)
}

func (b Box) SplitColumns(leftWidth, gutter int) (Box, Box) {
	converted := b.c()
	var left, right C.catui_box
	C.catui_box_split_cols(&converted, C.int(leftWidth), C.int(gutter), &left, &right)
	return boxFromC(left), boxFromC(right)
}

func (b Box) FitRows(baseHeight, itemCount, visibleRows int) (Rect, int, int) {
	converted := b.c()
	rows, itemHeight := C.int(visibleRows), C.int(0)
	rect := C.catui_box_fit_rows(&converted, C.int(baseHeight), C.int(itemCount), &rows, &itemHeight)
	return rectFromC(rect), int(rows), int(itemHeight)
}

func (c *Context) ThemeColor(role ThemeRole) Color {
	return Color(C.catui_theme_color(C.int(role)))
}
func (c *Context) FontHeight(tier FontTier) int { return int(C.catui_font_height(C.int(tier))) }
func (c *Context) FontBump() int                { return int(C.catui_font_bump()) }
func (c *Context) SetFontBump(bump int) error {
	if err := c.ensureOpen(); err != nil {
		return err
	}
	return statusError(C.catui_set_font_bump(C.int(bump)))
}

func (c *Context) MeasureText(tier FontTier, text string) int {
	textC, freeText := cString(text)
	defer freeText()
	return int(C.catui_measure_text(C.int(tier), textC))
}

func (c *Context) DrawText(tier FontTier, text string, x, y int, color Color, maxWidth int, ellipsize bool) (int, error) {
	if err := c.ensureOpen(); err != nil {
		return 0, err
	}
	textC, freeText := cString(text)
	defer freeText()
	ellipsizeC := C.int(0)
	if ellipsize {
		ellipsizeC = 1
	}
	result := C.catui_draw_text(C.int(tier), textC, C.int(x), C.int(y), C.uint32_t(color), C.int(maxWidth), ellipsizeC)
	if result < 0 {
		return 0, statusError(result)
	}
	return int(result), nil
}

func (c *Context) MeasureFallbackText(tier FontTier, text string) int {
	textC, freeText := cString(text)
	defer freeText()
	return int(C.catui_measure_fallback_text(C.int(tier), textC))
}

func (c *Context) DrawFallbackText(tier FontTier, text string, x, y int, color Color, maxWidth int) (int, error) {
	if err := c.ensureOpen(); err != nil {
		return 0, err
	}
	textC, freeText := cString(text)
	defer freeText()
	result := C.catui_draw_fallback_text(C.int(tier), textC, C.int(x), C.int(y), C.uint32_t(color), C.int(maxWidth))
	if result < 0 {
		return 0, statusError(result)
	}
	return int(result), nil
}

func (c *Context) DrawRect(rect Rect, color Color) error {
	if err := c.ensureOpen(); err != nil {
		return err
	}
	return statusError(C.catui_draw_rect(C.int(rect.X), C.int(rect.Y), C.int(rect.W), C.int(rect.H), C.uint32_t(color)))
}
func (c *Context) DrawRoundedRect(rect Rect, radius int, color Color) error {
	if err := c.ensureOpen(); err != nil {
		return err
	}
	return statusError(C.catui_draw_rounded_rect(C.int(rect.X), C.int(rect.Y), C.int(rect.W), C.int(rect.H), C.int(radius), C.uint32_t(color)))
}
func (c *Context) DrawPill(rect Rect, color Color) error {
	if err := c.ensureOpen(); err != nil {
		return err
	}
	return statusError(C.catui_draw_pill(C.int(rect.X), C.int(rect.Y), C.int(rect.W), C.int(rect.H), C.uint32_t(color)))
}
func (c *Context) DrawProgress(rect Rect, progress float32, foreground, background Color) error {
	if err := c.ensureOpen(); err != nil {
		return err
	}
	return statusError(C.catui_draw_progress(C.int(rect.X), C.int(rect.Y), C.int(rect.W), C.int(rect.H), C.float(progress), C.uint32_t(foreground), C.uint32_t(background)))
}
func (c *Context) DrawTriangle(rect Rect, direction Direction, color Color) error {
	if err := c.ensureOpen(); err != nil {
		return err
	}
	return statusError(C.catui_draw_triangle(C.int(rect.X), C.int(rect.Y), C.int(rect.W), C.int(rect.H), C.int(direction), C.uint32_t(color)))
}

// DrawScrollbar renders the launcher's list scroll indicator (track + thumb)
// down the right edge at x. It no-ops when total <= visible.
func (c *Context) DrawScrollbar(x, y, h, visible, total, offset int) error {
	if err := c.ensureOpen(); err != nil {
		return err
	}
	return statusError(C.catui_draw_scrollbar(C.int(x), C.int(y), C.int(h), C.int(visible), C.int(total), C.int(offset)))
}
func (c *Context) SetClip(rect Rect) error {
	if err := c.ensureOpen(); err != nil {
		return err
	}
	return statusError(C.catui_set_clip(C.int(rect.X), C.int(rect.Y), C.int(rect.W), C.int(rect.H)))
}
func (c *Context) ResetClip() error {
	if err := c.ensureOpen(); err != nil {
		return err
	}
	return statusError(C.catui_reset_clip())
}

type Texture struct {
	ctx    *Context
	id     C.catui_texture_id
	closed bool
}

func (c *Context) TextureFromImage(source image.Image) (*Texture, error) {
	if err := c.ensureOpen(); err != nil {
		return nil, err
	}
	bounds := source.Bounds()
	if bounds.Empty() {
		return nil, errors.New("catui: empty image")
	}
	rgba := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(rgba, rgba.Bounds(), source, bounds.Min, draw.Src)
	id := C.catui_texture_create_rgba(unsafe.Pointer(&rgba.Pix[0]), C.int(rgba.Rect.Dx()), C.int(rgba.Rect.Dy()), C.int(rgba.Stride))
	if id == 0 {
		return nil, errors.New("catui: upload RGBA texture")
	}
	texture := &Texture{ctx: c, id: id}
	c.mu.Lock()
	c.textures[texture] = struct{}{}
	c.mu.Unlock()
	return texture, nil
}

func (c *Context) TextureCapacity() int {
	if c == nil || c.ensureOpen() != nil {
		return 0
	}
	return int(C.catui_texture_capacity())
}

func (c *Context) TextureCount() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.textures)
}

func (c *Context) LoadTexture(path string) (*Texture, error) {
	if err := c.ensureOpen(); err != nil {
		return nil, err
	}
	pathC, freePath := cString(path)
	defer freePath()
	id := C.catui_texture_load(pathC)
	if id == 0 {
		return nil, fmt.Errorf("catui: load texture %q", path)
	}
	texture := &Texture{ctx: c, id: id}
	c.mu.Lock()
	c.textures[texture] = struct{}{}
	c.mu.Unlock()
	return texture, nil
}

func (t *Texture) ensureOpen() error {
	if t == nil || t.closed || t.id == 0 || t.ctx == nil {
		return ErrInvalidTexture
	}
	return t.ctx.ensureOpen()
}

func (t *Texture) Size() (int, int, error) {
	if err := t.ensureOpen(); err != nil {
		return 0, 0, err
	}
	var width, height C.int
	if err := statusError(C.catui_texture_size(t.id, &width, &height)); err != nil {
		return 0, 0, err
	}
	return int(width), int(height), nil
}

func (t *Texture) Draw(rect Rect) error {
	if err := t.ensureOpen(); err != nil {
		return err
	}
	return statusError(C.catui_texture_draw(t.id, C.int(rect.X), C.int(rect.Y), C.int(rect.W), C.int(rect.H)))
}

func (t *Texture) Destroy() error {
	if err := t.ensureOpen(); err != nil {
		return err
	}
	if err := statusError(C.catui_texture_destroy(t.id)); err != nil {
		return err
	}
	t.ctx.mu.Lock()
	delete(t.ctx.textures, t)
	t.ctx.mu.Unlock()
	t.id = 0
	t.closed = true
	return nil
}

func (c *Context) ScreenshotPNG(path string) error {
	if err := c.ensureOpen(); err != nil {
		return err
	}
	pathC, freePath := cString(path)
	defer freePath()
	return statusError(C.catui_screenshot_png(pathC))
}

func (c *Context) BeginCapture() error {
	if err := c.ensureOpen(); err != nil {
		return err
	}
	return statusError(C.catui_capture_begin())
}

func (c *Context) EndCapture() error {
	if err := c.ensureOpen(); err != nil {
		return err
	}
	return statusError(C.catui_capture_end())
}
