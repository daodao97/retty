package main

import (
	"bytes"
	"embed"
	"image"
	"image/color"
	"image/png"
	"strconv"

	"golang.org/x/image/draw"
	"golang.org/x/image/vector"
	"retty/internal/agentusage"
	"strings"
)

//go:embed assets/tray/*.png
var usageTrayAssets embed.FS

func usageTrayIconAgent(reading *agentusage.Reading, configured string, enabled bool) string {
	if !enabled {
		return ""
	}
	if configured == "codex" || configured == "claude" {
		return configured
	}
	if reading != nil && (reading.Agent == "codex" || reading.Agent == "claude") {
		return reading.Agent
	}
	return "codex" // Auto prioritizes Codex, including while the first read is pending.
}

func usageTrayIconFor(agent string) []byte {
	if agent == "codex" || agent == "claude" {
		data, err := usageTrayAssets.ReadFile("assets/tray/" + agent + ".png")
		if err == nil {
			return data
		}
	}
	return usageTrayIcon()
}

// Keep digits out of the bitmap: AppKit draws the title in its menu-bar
// font, with tabular digits and native colors at the display's own scale.
func usageTrayTitle(reading *agentusage.Reading) string {
	if reading == nil || len(reading.Windows) == 0 {
		return "—"
	}
	labels := make([]string, 0, 2)
	for _, w := range reading.Windows[:min(2, len(reading.Windows))] {
		labels = append(labels, strconv.Itoa(remainingUsage(w.Used))+"%")
	}
	return strings.Join(labels, " · ")
}

func usageTrayIcon() []byte {
	canvas := image.NewNRGBA(image.Rect(0, 0, 72, 72))
	ink := image.NewUniform(color.NRGBA{A: 255})
	path := vector.NewRasterizer(72, 72)
	path.MoveTo(8, 14)
	path.LineTo(29, 35)
	path.LineTo(8, 56)
	path.LineTo(4, 52)
	path.LineTo(21, 35)
	path.LineTo(4, 18)
	path.ClosePath()
	path.Draw(canvas, canvas.Bounds(), ink, image.Point{})
	draw.Draw(canvas, image.Rect(32, 50, 60, 56), ink, image.Point{}, draw.Over)
	small := image.NewNRGBA(image.Rect(0, 0, 36, 36))
	draw.CatmullRom.Scale(small, small.Bounds(), canvas, canvas.Bounds(), draw.Src, nil)
	var out bytes.Buffer
	_ = png.Encode(&out, small)
	return out.Bytes()
}
