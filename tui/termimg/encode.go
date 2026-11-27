package termimg

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"sort"
	"strconv"
	"strings"
)

// Default cell size in pixels assumed when the real one is unknown.
const (
	DefaultCellW = 8
	DefaultCellH = 16
)

const kittyChunk = 4096

// Encode returns an escape sequence that draws img at the cursor position,
// scaled to fit maxW x maxH cells (assuming 8x16 px cells). Images are not
// upscaled. For None it returns HalfBlock truecolor text.
func Encode(p Protocol, img image.Image, maxW, maxH int) (string, error) {
	return EncodeCell(p, img, maxW, maxH, DefaultCellW, DefaultCellH, false)
}

// EncodeCell is Encode with an explicit cell pixel size; upscale lets small
// images grow to fill the box.
func EncodeCell(p Protocol, img image.Image, maxW, maxH, cellW, cellH int, upscale bool) (string, error) {
	if img == nil {
		return "", fmt.Errorf("termimg: nil image")
	}
	if maxW <= 0 || maxH <= 0 {
		return "", nil
	}
	if cellW <= 0 || cellH <= 0 {
		cellW, cellH = DefaultCellW, DefaultCellH
	}
	b := img.Bounds()
	if p == None {
		w, ph := fitPixels(b.Dx(), b.Dy(), maxW, maxH*2, upscale)
		return halfBlockSized(img, w, ph, true), nil
	}
	pw, ph := fitPixels(b.Dx(), b.Dy(), maxW*cellW, maxH*cellH, upscale)
	cols := min(maxW, max(1, (pw+cellW-1)/cellW))
	rows := min(maxH, max(1, (ph+cellH-1)/cellH))

	switch p {
	case Kitty:
		data, err := pngBytes(downscaled(img, pw, ph))
		if err != nil {
			return "", err
		}
		return kittySeq(data, cols, rows), nil
	case ITerm2:
		data, err := pngBytes(downscaled(img, pw, ph))
		if err != nil {
			return "", err
		}
		return itermSeq(data, cols, rows), nil
	case Sixel:
		return sixelEncode(scaleOver(img, pw, ph)), nil
	}
	return "", fmt.Errorf("termimg: unknown protocol %d", p)
}

// downscaled returns img resized to w x h if that is smaller; the terminal
// does the final scaling for kitty/iTerm2 so we only shrink payloads.
func downscaled(img image.Image, w, h int) image.Image {
	b := img.Bounds()
	if w >= b.Dx() || h >= b.Dy() {
		return img
	}
	return scaleKeepAlpha(img, w, h)
}

func pngBytes(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("termimg: png encode: %w", err)
	}
	return buf.Bytes(), nil
}

func kittySeq(data []byte, cols, rows int) string {
	enc := base64.StdEncoding.EncodeToString(data)
	var sb strings.Builder
	sb.Grow(len(enc) + len(enc)/kittyChunk*16 + 64)
	first := true
	for len(enc) > 0 || first {
		n := min(kittyChunk, len(enc))
		chunk := enc[:n]
		enc = enc[n:]
		more := 0
		if len(enc) > 0 {
			more = 1
		}
		sb.WriteString("\x1b_G")
		if first {
			fmt.Fprintf(&sb, "a=T,f=100,t=d,q=2,C=0,c=%d,r=%d,", cols, rows)
			first = false
		}
		fmt.Fprintf(&sb, "m=%d;", more)
		sb.WriteString(chunk)
		sb.WriteString("\x1b\\")
	}
	return sb.String()
}

func itermSeq(data []byte, cols, rows int) string {
	return fmt.Sprintf("\x1b]1337;File=inline=1;size=%d;width=%d;height=%d;preserveAspectRatio=1:%s\a",
		len(data), cols, rows, base64.StdEncoding.EncodeToString(data))
}

// ClearSequence returns a sequence that removes all images drawn with p.
// Only kitty needs (and has) one; others return "".
func ClearSequence(p Protocol) string {
	if p == Kitty {
		return "\x1b_Ga=d,d=A,q=2\x1b\\"
	}
	return ""
}

// sixelEncode encodes an opaque RGBA image as sixel, quantized to the
// 6x6x6 cube + gray ramp (240 colors max, only used ones are defined).
func sixelEncode(img *image.RGBA) string {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	idx := make([]uint8, w*h) // palette index - 16
	used := map[int]bool{}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := img.RGBAAt(b.Min.X+x, b.Min.Y+y)
			i := Color256(c.R, c.G, c.B) - 16
			idx[y*w+x] = uint8(i)
			used[i] = true
		}
	}
	colors := make([]int, 0, len(used))
	for c := range used {
		colors = append(colors, c)
	}
	sort.Ints(colors)

	var sb strings.Builder
	sb.Grow(w * h / 2)
	// P2=1: pixels with value 0 stay transparent (we cover everything anyway).
	sb.WriteString("\x1bP0;1;0q")
	fmt.Fprintf(&sb, "\"1;1;%d;%d", w, h)
	for _, c := range colors {
		r, g, bl := palette256RGB(c + 16)
		fmt.Fprintf(&sb, "#%d;2;%d;%d;%d", c, pct(r), pct(g), pct(bl))
	}
	row := make([]byte, w)
	for y0 := 0; y0 < h; y0 += 6 {
		// colors present in this band
		var present [240]bool
		for y := y0; y < min(y0+6, h); y++ {
			for x := 0; x < w; x++ {
				present[idx[y*w+x]] = true
			}
		}
		firstColor := true
		for c := 0; c < 240; c++ {
			if !present[c] {
				continue
			}
			for x := 0; x < w; x++ {
				var bits byte
				for k := 0; k < 6 && y0+k < h; k++ {
					if int(idx[(y0+k)*w+x]) == c {
						bits |= 1 << k
					}
				}
				row[x] = 63 + bits
			}
			if !firstColor {
				sb.WriteByte('$')
			}
			firstColor = false
			sb.WriteByte('#')
			sb.WriteString(strconv.Itoa(c))
			writeSixelRLE(&sb, row)
		}
		if y0+6 < h {
			sb.WriteByte('-')
		}
	}
	sb.WriteString("\x1b\\")
	return sb.String()
}

// writeSixelRLE writes sixel characters, compressing runs of >3 as !n<c>.
// Trailing empty sixels ('?') are dropped.
func writeSixelRLE(sb *strings.Builder, row []byte) {
	end := len(row)
	for end > 0 && row[end-1] == '?' {
		end--
	}
	for i := 0; i < end; {
		j := i + 1
		for j < end && row[j] == row[i] {
			j++
		}
		n := j - i
		if n > 3 {
			sb.WriteByte('!')
			sb.WriteString(strconv.Itoa(n))
			sb.WriteByte(row[i])
		} else {
			for k := 0; k < n; k++ {
				sb.WriteByte(row[i])
			}
		}
		i = j
	}
}

func pct(v int) int { return (v*100 + 127) / 255 }
