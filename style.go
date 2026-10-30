package main

import (
	_ "embed"

	"github.com/egoist/mygo/ui"
	"retty/internal/terminal"
)

// colors are the app's own, in light and dark windows.
type colors struct {
	cardBorderFocused ui.Color
	shadowFocused     ui.Color
	headerBorder      ui.Color

	text, textMuted, textFaint, iconMuted ui.Color
	hover, pressed                        ui.Color

	track ui.Color

	busy, attention ui.Color

	// accent is the one color of a primary action and of selection; the
	// others tell a state, as a run's or a finding's, and read the same in
	// either window until each is tuned for its own.
	accent, info, success, warning, danger ui.Color
	// onAccent is text and icons on a filled accent or danger button.
	onAccent ui.Color

	panel, panelBorder, panelSel, backdrop ui.Color
	// scrim dims the window behind a dialog that waits for an answer,
	// darker than a palette's backdrop.
	scrim ui.Color

	// sidebar is the sidebar's own surface and its edge, a step off the
	// terminals' paper in a light window.
	sidebar, sidebarBorder ui.Color

	// meter fills a usage meter, an accent quieter than a button's.
	meter ui.Color
	// popoverShadow lifts a small popover off the window.
	popoverShadow ui.Color
	// dialogShadow lifts a dialog such as the palette; dialogShadowNear
	// is its tight contact shadow.
	dialogShadow, dialogShadowNear ui.Color
	// sheetScrim dims the phone behind a bottom sheet.
	sheetScrim ui.Color
	// canvas is the file panel's ground, a step off the window's.
	canvas ui.Color
	// screenCanvas is the ground of a remote screen, and onScreenCanvas the
	// text on it, the same in either window.
	screenCanvas, onScreenCanvas ui.Color
}

var lightColors = colors{
	cardBorderFocused: ui.RGBA(255, 255, 255, 1),
	shadowFocused:     ui.RGBA(60, 40, 50, 0.12),
	headerBorder:      ui.RGBA(0, 0, 0, 0.08),

	text:      ui.Hex("#1d1d1f"),
	textMuted: ui.Hex("#565960"),
	// textFaint stays 4.5:1 on the sidebar, a step below textMuted.
	textFaint: ui.Hex("#6b6e76"),
	iconMuted: ui.Hex("#737680"),
	hover:     ui.RGBA(0, 0, 0, 0.055),
	pressed:   ui.RGBA(0, 0, 0, 0.1),

	track: ui.RGBA(255, 255, 255, 0.34),

	busy:      ui.Hex("#26773b"),
	attention: ui.Hex("#9a6500"),

	accent:   ui.Hex("#2f6fdb"),
	onAccent: ui.Hex("#ffffff"),
	// The states read as text on light paper, a shade deeper than their dots
	// would need, as a pill's label does.
	info:    ui.Hex("#1d4ed8"),
	success: ui.Hex("#157d3c"),
	warning: ui.Hex("#b05109"),
	danger:  ui.Hex("#b91c1c"),

	panel:       ui.RGBA(252, 252, 253, 0.97),
	panelBorder: ui.RGBA(0, 0, 0, 0.08),
	panelSel:    ui.RGBA(46, 111, 208, 0.12),
	backdrop:    ui.RGBA(40, 20, 40, 0.06),
	scrim:       ui.RGBA(0, 0, 0, 0.3),

	sidebar:       ui.Hex("#f0f1f4"),
	sidebarBorder: ui.RGBA(0, 0, 0, 0.08),

	meter:            ui.Hex("#4976bc"),
	popoverShadow:    ui.RGBA(0, 0, 0, 0.14),
	dialogShadow:     ui.RGBA(0, 0, 0, 0.28),
	dialogShadowNear: ui.RGBA(0, 0, 0, 0.06),
	sheetScrim:       ui.RGBA(0, 0, 0, 0.4),
	canvas:           ui.Hex("#f6f7f9"),
	screenCanvas:     ui.Hex("#000000"),
	onScreenCanvas:   ui.Hex("#ffffff"),
}

var darkColors = colors{
	// Neutral charcoal chrome, inspired by iTerm2's dark appearance.
	cardBorderFocused: ui.Hex("#4a4e54"),
	shadowFocused:     ui.RGBA(0, 0, 0, 0.28),
	headerBorder:      ui.Hex("#303337"),

	text:      ui.Hex("#ededed"),
	textMuted: ui.Hex("#b6b8bc"),
	textFaint: ui.Hex("#91959c"),
	iconMuted: ui.Hex("#a1a5ac"),
	hover:     ui.RGBA(255, 255, 255, 0.07),
	pressed:   ui.RGBA(255, 255, 255, 0.12),

	track: ui.Hex("#1c1e21"),

	busy:      ui.Hex("#4ade80"),
	attention: ui.Hex("#fbbf24"),

	accent:   ui.Hex("#4c8df6"),
	onAccent: ui.Hex("#ffffff"),
	info:     ui.Hex("#60a5fa"),
	success:  ui.Hex("#22c55e"),
	warning:  ui.Hex("#f59e0b"),
	danger:   ui.Hex("#f87171"),

	panel:       ui.Hex("#25272b"),
	panelBorder: ui.Hex("#44484f"),
	panelSel:    ui.Hex("#354863"),
	backdrop:    ui.RGBA(0, 0, 0, 0.32),
	scrim:       ui.RGBA(0, 0, 0, 0.3),

	sidebar:       ui.Hex("#1c1e21"),
	sidebarBorder: ui.Hex("#303337"),

	meter:            ui.Hex("#84a7e8"),
	popoverShadow:    ui.RGBA(0, 0, 0, 0.14),
	dialogShadow:     ui.RGBA(0, 0, 0, 0.28),
	dialogShadowNear: ui.RGBA(0, 0, 0, 0.06),
	sheetScrim:       ui.RGBA(0, 0, 0, 0.4),
	canvas:           ui.Hex("#25272b"),
	screenCanvas:     ui.Hex("#000000"),
	onScreenCanvas:   ui.Hex("#ffffff"),
}

// The chrome's spacing steps, on a 4-point grid; spaceS is for dense rows
// alone. docs/ui-design.md says which to use where. Like the radii and type
// sizes below they are untyped, so they fit any length a style takes.
const (
	space2XS = 2
	spaceXS  = 4
	spaceS   = 6
	spaceM   = 8
	spaceL   = 12
	spaceXL  = 16
	space2XL = 24
	// space3XL is a page's margin and the gap between its sections.
	space3XL = 32
)

// The corner radii: small for pills and fields, medium for buttons and rows,
// large for cards and panels, and extra large for the phone's cards. A dot
// or a capsule takes half its height instead.
const (
	radiusS  = 4
	radiusM  = 8
	radiusL  = 12
	radiusXL = 16
	// radiusSheet is a phone bottom sheet's top corners.
	radiusSheet = 20
)

// The chrome's type sizes. The terminal's own font is termFont's, and the
// user's font size setting scales it, not these.
const (
	// textMicro is the sidebar's dense metadata alone: branches, counts,
	// times and its section labels.
	textMicro   = 10
	textCaption = 11
	textSmall   = 12
	textBody    = 13
	textTitle   = 15
	textHeading = 18
	// textDisplay is a figure read at a glance, as a metric's value.
	textDisplay = 22
)

// The phone's type sizes, as iOS sets them: a larger body than the
// desktop's, for reading at arm's length. Below these the phone shares the
// desktop's caption, small and body sizes.
const (
	mobileSubhead = 14
	mobileBody    = 15
	// mobileCallout is a field's and a button's, large enough to tap.
	mobileCallout    = 16
	mobileHeadline   = 17
	mobileTitle      = 18
	mobileLargeTitle = 32
)

func colorsOf(c *ui.Context) *colors {
	if c.Theme().Dark {
		return &darkColors
	}
	return &lightColors
}

// pageTheme is the settings page's, the connections drawer's and the usage
// panel's: the app's own colors on a surface of their own, so a page and
// the chrome around it read as one app.
func pageTheme(original *ui.Theme) ui.Theme {
	theme := *original
	k := &lightColors
	theme.Background, theme.Surface = k.canvas, ui.Hex("#ffffff")
	if theme.Dark {
		k = &darkColors
		theme.Background, theme.Surface = k.track, k.panel
	}
	theme.Text, theme.TextMuted, theme.Border = k.text, k.textMuted, k.panelBorder
	theme.SurfaceHover, theme.SurfacePressed = k.hover, k.pressed
	theme.Accent = k.accent
	theme.AccentHover, theme.AccentPressed = k.accent.Mix(k.onAccent, 0.1), k.accent.Mix(ui.Hex("#000000"), 0.1)
	theme.FontSize, theme.Radius, theme.Spacing = textBody, radiusM, spaceXS
	return theme
}

// connectionTheme is the phone's, and its connection pages': larger
// controls on a surface of their own.
func connectionTheme(original *ui.Theme) ui.Theme {
	theme := *original
	theme.FontSize, theme.Radius = mobileCallout, radiusL
	if theme.Dark {
		theme.Background = ui.Hex("#111315")
		theme.Surface = ui.Hex("#1e2126")
	} else {
		theme.Background = ui.Hex("#f5f6f8")
		theme.Surface = ui.Hex("#ffffff")
	}
	return theme
}

func terminalBackground(c *ui.Context) ui.Color {
	if c.Theme().Dark {
		return darkTerm.Background
	}
	return lightTerm.Background
}

//go:embed assets/fonts/JetBrainsMono-Regular.ttf
var fontRegular []byte

//go:embed assets/fonts/JetBrainsMono-Bold.ttf
var fontBold []byte

//go:embed assets/fonts/JetBrainsMono-Italic.ttf
var fontItalic []byte

//go:embed assets/fonts/JetBrainsMono-BoldItalic.ttf
var fontBoldItalic []byte

func registerFonts() {
	for _, f := range [][]byte{fontRegular, fontBold, fontItalic, fontBoldItalic} {
		ui.RegisterFont(f, "JetBrains Mono")
	}
}

var termFont = terminal.Font{Family: "JetBrains Mono, SF Mono, Menlo, monospace", Size: 11.6, LineHeight: 1.0}

// The terminals' colors: soft ink on the panes' paper, and the dark kind.
var lightTerm = &terminal.Theme{
	Foreground:      ui.Hex("#2a2d31"),
	Background:      ui.Hex("#fbfbfb"),
	Cursor:          ui.Hex("#3a3d42"),
	Selection:       ui.Hex("#d4e4fc"),
	SelectionText:   ui.Hex("#1f2a3d"),
	MinimumContrast: 4.5,
	Palette: [16]ui.Color{
		ui.Hex("#2a2d31"), ui.Hex("#b33449"), ui.Hex("#327343"), ui.Hex("#896014"),
		ui.Hex("#245fb5"), ui.Hex("#7c3daf"), ui.Hex("#176b7a"), ui.Hex("#61656c"),
		ui.Hex("#656971"), ui.Hex("#be354c"), ui.Hex("#28763e"), ui.Hex("#916000"),
		ui.Hex("#2767bc"), ui.Hex("#8544b5"), ui.Hex("#147484"), ui.Hex("#4b4f56"),
	},
}

var darkTerm = &terminal.Theme{
	Foreground:    ui.Hex("#d8d8d8"),
	Background:    ui.Hex("#111315"),
	Cursor:        ui.Hex("#e8e8e8"),
	CursorText:    ui.Hex("#111315"),
	Selection:     ui.Hex("#385779"),
	SelectionText: ui.Hex("#ffffff"),
	Palette: [16]ui.Color{
		ui.Hex("#1b1d1f"), ui.Hex("#cc6666"), ui.Hex("#b5bd68"), ui.Hex("#f0c674"),
		ui.Hex("#81a2be"), ui.Hex("#b294bb"), ui.Hex("#8abeb7"), ui.Hex("#c5c8c6"),
		ui.Hex("#707880"), ui.Hex("#e88989"), ui.Hex("#c7d28c"), ui.Hex("#f4d58d"),
		ui.Hex("#a1bed6"), ui.Hex("#c9aed3"), ui.Hex("#a5d4ce"), ui.Hex("#ffffff"),
	},
}
