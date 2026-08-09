package catui

import (
	"errors"
	"strings"
)

// Shared logical spacing. These are scaled once by NewComposer; screen code
// should not invent additional root, artwork, or modal padding values.
const (
	BasePad  = 16
	ArtPad   = 12
	ModalPad = 24
)

type LayoutMetrics struct {
	Width, Height                  int
	TitleHeight, FooterHeight      int
	BasePadding                    int
	HintsEnabled, HasFooterContent bool
}

type ScreenLayout struct {
	Screen, Title, SubHeader, Content, Footer Box
	FooterVisible                             bool
}

// ComputeScreenLayout is the single root title/content/footer carve used by
// migrated screens and by deterministic geometry tests.
func ComputeScreenLayout(metrics LayoutMetrics, subHeaderHeight int) ScreenLayout {
	// Apply the screen padding to root BEFORE carving the title, so the padding
	// sits at the top of the layout (giving the title breathing room from the
	// screen edge) instead of being orphaned as a gap between the title band and
	// the subheader. The title, subheader, and content then abut cleanly, and
	// each region owns its space from within the box model.
	pad := metrics.BasePadding
	root := NewBox(0, 0, metrics.Width, metrics.Height, 0)
	root.PadTop += pad
	root.PadRight += pad
	root.PadBottom += pad
	root.PadLeft += pad

	title := root.CarveTop(metrics.TitleHeight)
	footer := Box{}
	footerVisible := metrics.HintsEnabled && metrics.HasFooterContent
	if footerVisible {
		footer = root.CarveBottom(metrics.FooterHeight)
	}

	subHeader := Box{}
	if subHeaderHeight > 0 {
		subHeader = root.CarveTop(subHeaderHeight)
		root.CarveTop(pad / 2)
	}
	return ScreenLayout{
		Screen:        NewBox(0, 0, metrics.Width, metrics.Height, 0),
		Title:         title,
		SubHeader:     subHeader,
		Content:       root,
		Footer:        footer,
		FooterVisible: footerVisible,
	}
}

type FooterHint struct {
	Button           Button
	ButtonText       string
	NarrowButtonText string
	Label            string
	NarrowLabel      string
	IsConfirm        bool
}

type FooterGroups struct {
	Left, Right []FooterItem
}

func (g FooterGroups) Items() []FooterItem {
	items := make([]FooterItem, 0, len(g.Left)+len(g.Right))
	items = append(items, g.Left...)
	items = append(items, g.Right...)
	return items
}

// ResolveFooterGroups preserves Catastrophe's left/action and right/confirm
// grouping, switching all labels to their narrow variants when the estimated
// pixel width would overlap. Catastrophe remains the final footer renderer.
func ResolveFooterGroups(hints []FooterHint, availableWidth, badgeWidth, itemGap int,
	measureLabel, measureButton func(string) int) FooterGroups {
	labels := make([]string, len(hints))
	buttonTexts := make([]string, len(hints))
	for i := range hints {
		labels[i] = hints[i].Label
		buttonTexts[i] = hints[i].ButtonText
	}
	estimate := func() int {
		total := itemGap
		for i := range hints {
			resolvedBadgeWidth := badgeWidth
			if buttonTexts[i] != "" {
				// Cat renders multi-character overrides in its tiny font with a
				// half-badge inset.
				resolvedBadgeWidth = badgeWidth/2 + measureButton(buttonTexts[i])
			}
			total += resolvedBadgeWidth + measureLabel(labels[i]) + itemGap
		}
		return total
	}
	if estimate() > availableWidth {
		for i := range hints {
			if hints[i].NarrowLabel != "" {
				labels[i] = hints[i].NarrowLabel
			}
			if hints[i].NarrowButtonText != "" {
				buttonTexts[i] = hints[i].NarrowButtonText
			}
		}
	}

	groups := FooterGroups{}
	for i, hint := range hints {
		item := FooterItem{Button: hint.Button, ButtonText: buttonTexts[i], Label: labels[i], IsConfirm: hint.IsConfirm}
		if hint.IsConfirm {
			groups.Right = append(groups.Right, item)
		} else {
			groups.Left = append(groups.Left, item)
		}
	}
	return groups
}

type Composer struct {
	ctx                                   *Context
	BasePadding, ArtPadding, ModalPadding int
}

func NewComposer(ctx *Context) (*Composer, error) {
	if err := ctx.ensureOpen(); err != nil {
		return nil, err
	}
	return &Composer{
		ctx:          ctx,
		BasePadding:  ctx.Scale(BasePad),
		ArtPadding:   ctx.Scale(ArtPad),
		ModalPadding: ctx.Scale(ModalPad),
	}, nil
}

type ScreenSpec struct {
	Title           string
	SubHeaderHeight int
	Footer          []FooterHint
}

type ScreenFrame struct {
	Layout ScreenLayout
	footer []FooterItem
	ui     *Composer
}

func (ui *Composer) BeginScreen(spec ScreenSpec) (*ScreenFrame, error) {
	// A screen frame owns the full renderer. Never let a content clip inherited
	// from a previous draw suppress background or inherited title/status chrome.
	if err := ui.ctx.ResetClip(); err != nil {
		return nil, err
	}
	if err := ui.ctx.Clear(); err != nil {
		return nil, err
	}
	width, height, err := ui.ctx.ScreenSize()
	if err != nil {
		return nil, err
	}
	groups := ResolveFooterGroups(spec.Footer, width-ui.BasePadding*2,
		ui.ctx.Scale(34), ui.ctx.Scale(14), func(text string) int {
			return ui.ctx.MeasureText(FontSmall, text)
		}, func(text string) int {
			return ui.ctx.MeasureText(FontTiny, text)
		})
	footer := groups.Items()
	layout := ComputeScreenLayout(LayoutMetrics{
		Width:            width,
		Height:           height,
		TitleHeight:      ui.ctx.TitleHeight(),
		FooterHeight:     ui.ctx.FooterHeight(),
		BasePadding:      ui.BasePadding,
		HintsEnabled:     ui.ctx.HintsEnabled(),
		HasFooterContent: len(footer) > 0,
	}, spec.SubHeaderHeight)
	if err := ui.ctx.DrawTitleIn(layout.Title.Content(), spec.Title); err != nil {
		return nil, err
	}
	return &ScreenFrame{Layout: layout, footer: footer, ui: ui}, nil
}

func (frame *ScreenFrame) Finish() error {
	if frame == nil || frame.ui == nil {
		return ErrClosed
	}
	// Footer chrome is outside content clips by definition.
	if err := frame.ui.ctx.ResetClip(); err != nil {
		return err
	}
	if frame.Layout.FooterVisible {
		return frame.ui.ctx.DrawFooter(frame.footer)
	}
	return nil
}

func (ui *Composer) DrawSubHeader(box Box, text string) error {
	rect := box.Content()
	y := rect.Y + (rect.H-ui.ctx.FontHeight(FontSmall))/2
	_, err := ui.ctx.DrawText(FontSmall, text, rect.X, y,
		ui.ctx.ThemeColor(RoleHint), rect.W, true)
	return err
}

type ListDetailLayout struct{ List, Detail Box }

// previewCardColor matches the subtle rounded panel the native launcher fills
// behind its games/apps/recents preview pane (#ffffff10).
var previewCardColor = RGBA(0xff, 0xff, 0xff, 0x10)

func ListDetailSplit(body Box, leftPercent, gutter int) ListDetailLayout {
	if leftPercent < 0 {
		leftPercent = 0
	}
	if leftPercent > 100 {
		leftPercent = 100
	}
	content := body.Content()
	left, right := body.SplitColumns(content.W*leftPercent/100, gutter)
	return ListDetailLayout{List: left, Detail: right}
}

// DrawPreviewCard fills the games-style rounded panel behind a preview pane and
// returns the padded inner rect to draw the preview content into, so list/detail
// screens match the games/apps/recents gutter.
func (ui *Composer) DrawPreviewCard(pane Rect) (Rect, error) {
	if err := ui.ctx.DrawRoundedRect(pane, ui.ctx.Scale(8), previewCardColor); err != nil {
		return Rect{}, err
	}
	return insetRect(pane, ui.ArtPadding, ui.ArtPadding), nil
}

func FullWidthBody(body Box) Rect { return body.Content() }

type ListGeometry struct {
	Region                 Rect
	VisibleRows, RowHeight int
}

func FitScrollingList(box Box, baseRowHeight, itemCount, cachedVisibleRows int) ListGeometry {
	region, visible, rowHeight := box.FitRows(baseRowHeight, itemCount, cachedVisibleRows)
	return ListGeometry{Region: region, VisibleRows: visible, RowHeight: rowHeight}
}

func (g ListGeometry) Row(index int) Rect {
	if index < 0 || index >= g.VisibleRows {
		return Rect{}
	}
	return Rect{X: g.Region.X, Y: g.Region.Y + index*g.RowHeight, W: g.Region.W, H: g.RowHeight}
}

func (ui *Composer) DrawListRow(rect Rect, primary, secondary string, selected bool) error {
	if rect.W <= 0 || rect.H <= 0 {
		return nil
	}
	if selected {
		pill := insetRect(rect, 0, ui.ctx.Scale(3))
		// Reserve the same right-edge margin the native launcher does
		// (iw - CAT_S(4)) so the pill clears the scrollbar/gutter.
		pill.W -= ui.ctx.Scale(4)
		if err := ui.ctx.DrawPill(pill, ui.ctx.ThemeColor(RoleHighlight)); err != nil {
			return err
		}
	}
	return ui.withClip(rect, func() error {
		color := ui.ctx.ThemeColor(RoleText)
		if selected {
			color = ui.ctx.ThemeColor(RoleHighlightedText)
		}
		pad := ui.ctx.Scale(12)
		x := rect.X + pad
		primaryY := rect.Y + (rect.H-ui.ctx.FontHeight(FontMedium))/2
		maxWidth := rect.W - pad*2
		if secondary != "" {
			secondaryWidth := minInt(ui.ctx.MeasureText(FontTiny, secondary), maxInt(0, rect.W/3))
			maxWidth -= secondaryWidth + pad
			if secondaryWidth > 0 {
				secondaryY := rect.Y + (rect.H-ui.ctx.FontHeight(FontTiny))/2
				if _, err := ui.ctx.DrawText(FontTiny, secondary,
					rect.X+rect.W-pad-secondaryWidth, secondaryY,
					ui.ctx.ThemeColor(RoleHint), secondaryWidth, true); err != nil {
					return err
				}
			}
		}
		if primary == "" || maxWidth <= 0 {
			return nil
		}
		_, err := ui.ctx.DrawFallbackText(FontMedium, primary, x, primaryY, color, maxWidth)
		return err
	})
}

func (ui *Composer) DrawValueRow(rect Rect, label, value string, selected, cycler bool) error {
	if rect.W <= 0 || rect.H <= 0 {
		return nil
	}
	if selected {
		pill := insetRect(rect, 0, ui.ctx.Scale(3))
		if err := ui.ctx.DrawPill(pill, ui.ctx.ThemeColor(RoleHighlight)); err != nil {
			return err
		}
	}
	return ui.withClip(rect, func() error {
		pad := ui.ctx.Scale(12)
		arrow := ui.ctx.Scale(10)
		valueWidth := minInt(ui.ctx.MeasureText(FontSmall, value), maxInt(0, rect.W/2))
		valueX := rect.X + rect.W - pad - valueWidth
		if cycler {
			valueX -= arrow*2 + ui.ctx.Scale(12)
		}
		color := ui.ctx.ThemeColor(RoleHint)
		if selected {
			color = ui.ctx.ThemeColor(RoleHighlightedText)
		}
		y := rect.Y + (rect.H-ui.ctx.FontHeight(FontSmall))/2
		if value != "" && valueWidth > 0 {
			if _, err := ui.ctx.DrawText(FontSmall, value, valueX, y, color, valueWidth, true); err != nil {
				return err
			}
		}
		labelWidth := maxInt(0, valueX-rect.X-pad*2)
		if label != "" && labelWidth > 0 {
			labelY := rect.Y + (rect.H-ui.ctx.FontHeight(FontMedium))/2
			if _, err := ui.ctx.DrawFallbackText(FontMedium, label, rect.X+pad, labelY, color, labelWidth); err != nil {
				return err
			}
		}
		if cycler {
			triY := rect.Y + (rect.H-arrow)/2
			if err := ui.ctx.DrawTriangle(Rect{valueX - arrow - ui.ctx.Scale(5), triY, arrow, arrow}, DirectionLeft, color); err != nil {
				return err
			}
			return ui.ctx.DrawTriangle(Rect{rect.X + rect.W - pad - arrow, triY, arrow, arrow}, DirectionRight, color)
		}
		return nil
	})
}

func (ui *Composer) DrawScrollingBody(rect Rect, title string, paragraphs []string, offset int) error {
	return ui.withClip(rect, func() error {
		x, y := rect.X, rect.Y
		if title != "" {
			if _, err := ui.ctx.DrawFallbackText(FontLarge, title, x, y,
				ui.ctx.ThemeColor(RoleEmphasis), rect.W); err != nil {
				return err
			}
			y += ui.ctx.FontHeight(FontLarge) + ui.BasePadding/2
		}
		lineHeight := ui.ctx.FontHeight(FontSmall) + ui.ctx.Scale(5)
		lines := make([]string, 0, len(paragraphs)*2)
		for _, paragraph := range paragraphs {
			lines = append(lines, wrapText(paragraph, rect.W, func(value string) int {
				return ui.ctx.MeasureText(FontSmall, value)
			})...)
			lines = append(lines, "")
		}
		if offset < 0 {
			offset = 0
		}
		for i := offset; i < len(lines) && y+lineHeight <= rect.Y+rect.H; i++ {
			if lines[i] != "" {
				if _, err := ui.ctx.DrawFallbackText(FontSmall, lines[i], x, y,
					ui.ctx.ThemeColor(RoleText), rect.W); err != nil {
					return err
				}
			}
			y += lineHeight
		}
		return nil
	})
}

func CenteredModalRect(bounds Rect, widthPercent, heightPercent, margin int) Rect {
	if widthPercent <= 0 || widthPercent > 100 {
		widthPercent = 72
	}
	if heightPercent <= 0 || heightPercent > 100 {
		heightPercent = 56
	}
	w, h := bounds.W*widthPercent/100, bounds.H*heightPercent/100
	w = maxInt(0, w-margin*2)
	h = maxInt(0, h-margin*2)
	return Rect{X: bounds.X + (bounds.W-w)/2, Y: bounds.Y + (bounds.H-h)/2, W: w, H: h}
}

func (ui *Composer) DrawModal(bounds Rect, title, body string) error {
	widthPercent, heightPercent := 72, 58
	if bounds.W < ui.ctx.Scale(600) {
		widthPercent, heightPercent = 90, 72
	}
	modal := CenteredModalRect(bounds, widthPercent, heightPercent, 0)
	if err := ui.ctx.DrawRect(modal, ui.ctx.ThemeColor(RoleAccent)); err != nil {
		return err
	}
	if err := ui.ctx.DrawPill(Rect{X: modal.X, Y: modal.Y, W: modal.W, H: ui.ctx.Scale(5)},
		ui.ctx.ThemeColor(RoleHighlight)); err != nil {
		return err
	}
	inner := insetRect(modal, ui.ModalPadding, ui.ModalPadding)
	if _, err := ui.ctx.DrawFallbackText(FontLarge, title, inner.X, inner.Y,
		ui.ctx.ThemeColor(RoleEmphasis), inner.W); err != nil {
		return err
	}
	inner.Y += ui.ctx.FontHeight(FontLarge) + ui.BasePadding
	inner.H -= ui.ctx.FontHeight(FontLarge) + ui.BasePadding
	return ui.DrawScrollingBody(inner, "", []string{body}, 0)
}

func (ui *Composer) DrawWarningCover(bounds Rect, title, body string) error {
	if err := ui.ctx.DrawRect(bounds, ui.ctx.ThemeColor(RoleHighlight)); err != nil {
		return err
	}
	inner := insetRect(bounds, ui.ModalPadding, ui.ModalPadding)
	color := ui.ctx.ThemeColor(RoleHighlightedText)
	if _, err := ui.ctx.DrawFallbackText(FontLarge, title, inner.X, inner.Y, color, inner.W); err != nil {
		return err
	}
	y := inner.Y + ui.ctx.FontHeight(FontLarge) + ui.BasePadding/2
	for _, line := range wrapText(body, inner.W, func(value string) int {
		return ui.ctx.MeasureText(FontSmall, value)
	}) {
		if _, err := ui.ctx.DrawText(FontSmall, line, inner.X, y, color, inner.W, false); err != nil {
			return err
		}
		y += ui.ctx.FontHeight(FontSmall) + ui.ctx.Scale(4)
	}
	return nil
}

func (ui *Composer) DrawProgressView(bounds Rect, title, detail string, progress float32) error {
	inner := insetRect(bounds, ui.ModalPadding, ui.ModalPadding)
	y := inner.Y + maxInt(0, (inner.H-ui.ctx.Scale(84))/2)
	if _, err := ui.ctx.DrawFallbackText(FontLarge, title, inner.X, y,
		ui.ctx.ThemeColor(RoleEmphasis), inner.W); err != nil {
		return err
	}
	y += ui.ctx.FontHeight(FontLarge) + ui.BasePadding/2
	if _, err := ui.ctx.DrawText(FontSmall, detail, inner.X, y,
		ui.ctx.ThemeColor(RoleHint), inner.W, true); err != nil {
		return err
	}
	y += ui.ctx.FontHeight(FontSmall) + ui.BasePadding
	return ui.ctx.DrawProgress(Rect{X: inner.X, Y: y, W: inner.W, H: ui.ctx.Scale(9)},
		progress, ui.ctx.ThemeColor(RoleHighlight), ui.ctx.ThemeColor(RoleDisabled))
}

func (ui *Composer) DrawTextField(bounds Rect, label, value string, active bool) error {
	color := ui.ctx.ThemeColor(RoleAccent)
	if active {
		color = ui.ctx.ThemeColor(RoleHighlight)
	}
	if err := ui.ctx.DrawPill(bounds, color); err != nil {
		return err
	}
	inner := insetRect(bounds, ui.ctx.Scale(12), ui.ctx.Scale(5))
	textColor := contrastText(color)
	text := value
	if text == "" {
		text = label
	}
	y := inner.Y + (inner.H-ui.ctx.FontHeight(FontSmall))/2
	_, err := ui.ctx.DrawFallbackText(FontSmall, text, inner.X, y, textColor, inner.W)
	return err
}

type KeyboardLayout struct {
	Cells []Rect
	Rows  int
}

func LayoutKeyboard(bounds Rect, keys [][]string, gap int) KeyboardLayout {
	layout := KeyboardLayout{Rows: len(keys)}
	if len(keys) == 0 {
		return layout
	}
	rowHeight := (bounds.H - gap*(len(keys)-1)) / len(keys)
	y := bounds.Y
	for _, row := range keys {
		if len(row) == 0 {
			y += rowHeight + gap
			continue
		}
		cellWidth := (bounds.W - gap*(len(row)-1)) / len(row)
		x := bounds.X
		for range row {
			layout.Cells = append(layout.Cells, Rect{X: x, Y: y, W: cellWidth, H: rowHeight})
			x += cellWidth + gap
		}
		y += rowHeight + gap
	}
	return layout
}

func (ui *Composer) DrawKeyboard(bounds Rect, keys [][]string, selected int) error {
	layout := LayoutKeyboard(bounds, keys, ui.ctx.Scale(5))
	index := 0
	for _, row := range keys {
		for _, key := range row {
			cell := layout.Cells[index]
			active := index == selected
			if active {
				if err := ui.ctx.DrawPill(cell, ui.ctx.ThemeColor(RoleHighlight)); err != nil {
					return err
				}
			}
			color := ui.ctx.ThemeColor(RoleText)
			if active {
				color = ui.ctx.ThemeColor(RoleHighlightedText)
			}
			width := ui.ctx.MeasureText(FontTiny, key)
			if width > cell.W {
				width = cell.W
			}
			x := cell.X + (cell.W-width)/2
			y := cell.Y + (cell.H-ui.ctx.FontHeight(FontTiny))/2
			if _, err := ui.ctx.DrawText(FontTiny, key, x, y, color, cell.W, true); err != nil {
				return err
			}
			index++
		}
	}
	return nil
}

func (ui *Composer) DrawTagPills(bounds Rect, tags []string) (int, error) {
	background := ui.ctx.ThemeColor(RoleAccent)
	return drawTagPills(bounds, tags, ui.ctx.FontHeight(FontTiny), ui.ctx.Scale(8), ui.ctx.Scale(6),
		func(value string) int { return ui.ctx.MeasureFallbackText(FontTiny, value) },
		func(rect Rect, value string) error {
			if err := ui.ctx.DrawPill(rect, background); err != nil {
				return err
			}
			y := rect.Y + (rect.H-ui.ctx.FontHeight(FontTiny))/2
			_, err := ui.ctx.DrawFallbackText(FontTiny, value, rect.X+ui.ctx.Scale(8), y,
				contrastText(background), rect.W-ui.ctx.Scale(16))
			return err
		})
}

func contrastText(background Color) Color {
	r := int(uint32(background) & 0xff)
	g := int((uint32(background) >> 8) & 0xff)
	b := int((uint32(background) >> 16) & 0xff)
	// Integer approximation of perceived luminance. A slightly conservative
	// threshold keeps small Cat font tiers readable on saturated colours.
	if (r*299+g*587+b*114)/1000 >= 145 {
		return RGBA(24, 28, 39, 255)
	}
	return RGBA(247, 242, 232, 255)
}

func drawTagPills(bounds Rect, tags []string, fontHeight, horizontalPad, gap int,
	measure func(string) int, draw func(Rect, string) error) (int, error) {
	if len(tags) == 0 || bounds.W <= 0 {
		return 0, nil
	}
	pillHeight := fontHeight + gap
	x, y := bounds.X, bounds.Y
	for _, tag := range tags {
		width := minInt(bounds.W, measure(tag)+horizontalPad*2)
		if x > bounds.X && x+width > bounds.X+bounds.W {
			x = bounds.X
			y += pillHeight + gap
		}
		if y+pillHeight > bounds.Y+bounds.H {
			break
		}
		if err := draw(Rect{X: x, Y: y, W: width, H: pillHeight}, tag); err != nil {
			return y - bounds.Y, err
		}
		x += width + gap
	}
	return minInt(bounds.H, y-bounds.Y+pillHeight), nil
}

func FitImage(sourceWidth, sourceHeight int, viewport Rect, artPadding int) Rect {
	inner := insetRect(viewport, artPadding, artPadding)
	if sourceWidth <= 0 || sourceHeight <= 0 || inner.W <= 0 || inner.H <= 0 {
		return Rect{X: inner.X, Y: inner.Y}
	}
	w, h := inner.W, sourceHeight*inner.W/sourceWidth
	if h > inner.H {
		h = inner.H
		w = sourceWidth * inner.H / sourceHeight
	}
	return Rect{X: inner.X + (inner.W-w)/2, Y: inner.Y + (inner.H-h)/2, W: w, H: h}
}

func (ui *Composer) DrawImageFit(texture *Texture, viewport Rect) error {
	width, height, err := texture.Size()
	if err != nil {
		return err
	}
	return ui.withClip(viewport, func() error {
		return texture.Draw(FitImage(width, height, viewport, ui.ArtPadding))
	})
}

type GalleryLayout struct {
	Main       Rect
	Thumbnails []Rect
}

func LayoutGallery(bounds Rect, count, gap, thumbnailHeight int) GalleryLayout {
	if count < 0 {
		count = 0
	}
	thumbBand := 0
	if count > 1 {
		thumbBand = thumbnailHeight + gap
	}
	main := Rect{X: bounds.X, Y: bounds.Y, W: bounds.W, H: maxInt(0, bounds.H-thumbBand)}
	layout := GalleryLayout{Main: main}
	if count <= 1 {
		return layout
	}
	if gap*(count-1) > bounds.W-count {
		gap = maxInt(0, (bounds.W-count)/(count-1))
	}
	width := maxInt(1, (bounds.W-gap*(count-1))/count)
	y := bounds.Y + main.H + gap
	x := bounds.X
	for i := 0; i < count; i++ {
		layout.Thumbnails = append(layout.Thumbnails, Rect{X: x, Y: y, W: width, H: thumbnailHeight})
		x += width + gap
	}
	return layout
}

func (ui *Composer) DrawGallery(bounds Rect, textures []*Texture, selected int) error {
	if len(textures) == 0 {
		return ui.DrawState(bounds, StateEmpty, "No screenshots", "This game has no gallery images.")
	}
	if selected < 0 || selected >= len(textures) {
		selected = 0
	}
	layout := LayoutGallery(bounds, len(textures), ui.ctx.Scale(7), ui.ctx.Scale(74))
	if err := ui.DrawImageFit(textures[selected], layout.Main); err != nil {
		return err
	}
	if len(textures) == 1 {
		return nil
	}
	for index, texture := range textures {
		if index == selected {
			if err := ui.ctx.DrawPill(layout.Thumbnails[index], ui.ctx.ThemeColor(RoleHighlight)); err != nil {
				return err
			}
		}
		thumb := insetRect(layout.Thumbnails[index], ui.ctx.Scale(3), ui.ctx.Scale(3))
		if err := ui.DrawImageFit(texture, thumb); err != nil {
			return err
		}
	}
	return nil
}

type StateKind int

const (
	StateEmpty StateKind = iota
	StateLoading
	StateOffline
	StateError
)

func (kind StateKind) label() string {
	switch kind {
	case StateLoading:
		return "Loading"
	case StateOffline:
		return "Offline"
	case StateError:
		return "Error"
	default:
		return "Empty"
	}
}

func (ui *Composer) DrawState(bounds Rect, kind StateKind, title, detail string) error {
	if title == "" {
		title = kind.label()
	}
	inner := insetRect(bounds, ui.ModalPadding, ui.ModalPadding)
	titleHeight := ui.ctx.FontHeight(FontLarge)
	detailHeight := ui.ctx.FontHeight(FontSmall)
	total := titleHeight + ui.BasePadding/2 + detailHeight
	y := inner.Y + maxInt(0, (inner.H-total)/2)
	color := ui.ctx.ThemeColor(RoleEmphasis)
	if kind == StateError {
		color = ui.ctx.ThemeColor(RoleHighlight)
	}
	width := ui.ctx.MeasureFallbackText(FontLarge, title)
	if width > inner.W {
		width = inner.W
	}
	if _, err := ui.ctx.DrawFallbackText(FontLarge, title, inner.X+(inner.W-width)/2,
		y, color, width); err != nil {
		return err
	}
	y += titleHeight + ui.BasePadding/2
	if detail == "" {
		return nil
	}
	detailWidth := minInt(inner.W, ui.ctx.MeasureText(FontSmall, detail))
	_, err := ui.ctx.DrawText(FontSmall, detail, inner.X+(inner.W-detailWidth)/2,
		y, ui.ctx.ThemeColor(RoleHint), detailWidth, true)
	return err
}

func (ui *Composer) withClip(rect Rect, draw func() error) error {
	if err := ui.ctx.SetClip(rect); err != nil {
		return err
	}
	drawErr := draw()
	resetErr := ui.ctx.ResetClip()
	return errors.Join(drawErr, resetErr)
}

func insetRect(rect Rect, horizontal, vertical int) Rect {
	return Rect{
		X: rect.X + horizontal,
		Y: rect.Y + vertical,
		W: maxInt(0, rect.W-horizontal*2),
		H: maxInt(0, rect.H-vertical*2),
	}
}

func wrapText(text string, maxWidth int, measure func(string) int) []string {
	if maxWidth <= 0 {
		return nil
	}
	var lines []string
	for _, paragraph := range strings.Split(text, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		line := words[0]
		for _, word := range words[1:] {
			candidate := line + " " + word
			if measure(candidate) <= maxWidth {
				line = candidate
				continue
			}
			lines = append(lines, line)
			line = word
		}
		lines = append(lines, line)
	}
	return lines
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
