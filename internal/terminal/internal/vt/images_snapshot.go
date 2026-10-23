package vt

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"image"
	"slices"
	"strconv"
	"strings"
)

type imageTransmission struct {
	generation uint64
	data       []byte
}

// VTGraphicsRecent adds visible, non-virtual Kitty placements to a normal VT
// snapshot. Pixel transfers are cached by generation; cursor and origin mode
// are restored after placement. File and shared-memory transfers are never
// emitted, so restoring on another host cannot read that host's filesystem.
func (t *Terminal) VTGraphicsRecent(historyRows int) []byte {
	data := t.vtRecent(historyRows)
	var placements []Placement
	t.Placements(func(p Placement) {
		if !p.Virtual && p.Visible && p.Width > 0 && p.Height > 0 {
			placements = append(placements, p)
		}
	})
	if len(placements) == 0 {
		clear(t.imageTransmissions)
		return data
	}
	slices.SortFunc(placements, func(a, b Placement) int {
		if a.ImageID < b.ImageID {
			return -1
		}
		if a.ImageID > b.ImageID {
			return 1
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	if t.imageTransmissions == nil {
		t.imageTransmissions = map[uint32]imageTransmission{}
	}
	seen := map[uint32]bool{}
	var out bytes.Buffer
	out.Write(data)
	out.WriteString("\x1b7\x1b[?6l\x1b_Ga=d,d=A,q=2;\x1b\\")
	for _, p := range placements {
		if seen[p.ImageID] {
			continue
		}
		seen[p.ImageID] = true
		generation := t.ImageGeneration(p.ImageID)
		cached := t.imageTransmissions[p.ImageID]
		if cached.generation != generation || generation == 0 {
			img, ok := t.Image(p.ImageID)
			if !ok {
				continue
			}
			cached = imageTransmission{generation: generation, data: imageTransfer(img)}
			t.imageTransmissions[p.ImageID] = cached
		}
		out.Write(cached.data)
	}
	for id := range t.imageTransmissions {
		if !seen[id] {
			delete(t.imageTransmissions, id)
		}
	}
	for _, p := range placements {
		if len(t.imageTransmissions[p.ImageID].data) == 0 {
			continue
		}
		row, col, rows := p.Row, p.Col, max(p.Rows, 1)
		srcY, srcH := p.SrcY, p.SrcH
		if row < 0 {
			cut := min(int(int64(-row)*int64(srcH)/int64(rows)), srcH)
			srcY, srcH = srcY+cut, srcH-cut
			rows += row
			row = 0
		}
		if rows <= 0 || col < 0 || srcH <= 0 {
			continue
		}
		fmt.Fprintf(&out, "\x1b[%d;%dH\x1b_Ga=p,i=%d,p=%d,c=%d,r=%d,x=%d,y=%d,w=%d,h=%d,X=%d,Y=%d,z=%d,C=1,q=2;\x1b\\", row+1, col+1, p.ImageID, p.ID, max(p.Cols, 1), rows, p.SrcX, srcY, p.SrcW, srcH, p.XOffset, p.YOffset, p.Z)
	}
	if t.Mode(6) {
		out.WriteString("\x1b[?6h")
	}
	out.WriteString("\x1b8")
	if t.Mode(6) {
		// The native formatter emits an absolute final CUP even with DECOM
		// restored. Correct that position in the restored margin coordinates.
		top, left := snapshotMargins(data)
		x, y := t.Cursor()
		fmt.Fprintf(&out, "\x1b[%d;%dH", max(y-top+1, 1), max(x-left+1, 1))
	}
	return out.Bytes()
}

// Read only formatter-generated CSI margins, skipping opaque strings so a
// hyperlink's URI cannot masquerade as a scrolling-region command.
func snapshotMargins(data []byte) (top, left int) {
	for i := 0; i+1 < len(data); i++ {
		if data[i] != 27 {
			continue
		}
		if strings.ContainsRune("]P_^X", rune(data[i+1])) {
			i += 2
			for i < len(data) {
				if data[i] == 7 {
					break
				}
				if data[i] == 27 && i+1 < len(data) && data[i+1] == '\\' {
					i++
					break
				}
				i++
			}
			continue
		}
		if data[i+1] != '[' {
			continue
		}
		start := i + 2
		j := start
		for j < len(data) && (data[j] < 0x40 || data[j] > 0x7e) {
			j++
		}
		if j == len(data) {
			break
		}
		if data[j] == 'r' || data[j] == 's' {
			parts := strings.Split(string(data[start:j]), ";")
			if len(parts) == 2 {
				a, err := strconv.Atoi(parts[0])
				if err == nil {
					if data[j] == 'r' {
						top = max(a, 1) - 1
					} else {
						left = max(a, 1) - 1
					}
				}
			}
		}
		i = j
	}
	return
}

func imageTransfer(img Image) []byte {
	format, pixels := 32, img.Pixels
	if img.Format == ImageRGB {
		format = 24
	} else if img.Format != ImageRGBA {
		pixels = img.NRGBA().Pix
	}
	var compressed bytes.Buffer
	writer, _ := zlib.NewWriterLevel(&compressed, zlib.BestSpeed)
	writer.Write(pixels)
	writer.Close()
	encoded := base64.StdEncoding.EncodeToString(compressed.Bytes())
	var out bytes.Buffer
	for i := 0; i < len(encoded); i += 4096 {
		more := 0
		if i+4096 < len(encoded) {
			more = 1
		}
		if i == 0 {
			fmt.Fprintf(&out, "\x1b_Ga=t,t=d,f=%d,s=%d,v=%d,i=%d,o=z,q=2,m=%d;", format, img.Width, img.Height, img.ID, more)
		} else {
			fmt.Fprintf(&out, "\x1b_Gm=%d;", more)
		}
		out.WriteString(encoded[i:min(i+4096, len(encoded))])
		out.WriteString("\x1b\\")
	}
	return out.Bytes()
}

func (img Image) NRGBA() *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, img.Width, img.Height))
	px := img.Pixels
	switch img.Format {
	case ImageRGBA:
		copy(out.Pix, px)
	case ImageRGB:
		for i := 0; i < img.Width*img.Height && 3*i+2 < len(px); i++ {
			copy(out.Pix[4*i:], px[3*i:3*i+3])
			out.Pix[4*i+3] = 255
		}
	case ImageGrayAlpha:
		for i := 0; i < img.Width*img.Height && 2*i+1 < len(px); i++ {
			g := px[2*i]
			copy(out.Pix[4*i:], []byte{g, g, g, px[2*i+1]})
		}
	case ImageGray:
		for i := 0; i < img.Width*img.Height && i < len(px); i++ {
			g := px[i]
			copy(out.Pix[4*i:], []byte{g, g, g, 255})
		}
	}
	return out
}
