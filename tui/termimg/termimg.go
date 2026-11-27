// Package termimg renders images in the terminal: colored half-block text
// art that works everywhere, plus the Kitty, iTerm2 and Sixel graphics
// protocols for terminals that support them.
package termimg

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // register decoder (first frame)
	_ "image/jpeg"
	_ "image/png"
	"math"
	"strconv"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// Decode decodes png, jpeg, gif (first frame) or webp bytes. The string is
// the format name ("png", "jpeg", "gif", "webp").
func Decode(b []byte) (image.Image, string, error) {
	img, format, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, "", fmt.Errorf("termimg: decode: %w", err)
	}
	return img, format, nil
}

// Fit returns the cell size (columns, rows) HalfBlock uses for an image of
// imgW x imgH pixels within maxW x maxH cells. A cell is 1 px wide and 2 px
// tall in half-block mode. Images are never upscaled.
func Fit(imgW, imgH, maxW, maxH int) (w, h int) {
	w, ph := fitPixels(imgW, imgH, maxW, maxH*2, false)
	return w, (ph + 1) / 2
}

// fitPixels scales imgW x imgH to fit in boxW x boxH preserving aspect.
func fitPixels(imgW, imgH, boxW, boxH int, upscale bool) (int, int) {
	if imgW <= 0 || imgH <= 0 || boxW <= 0 || boxH <= 0 {
		return 0, 0
	}
	s := math.Min(float64(boxW)/float64(imgW), float64(boxH)/float64(imgH))
	if !upscale && s > 1 {
		s = 1
	}
	w := int(math.Round(float64(imgW) * s))
	h := int(math.Round(float64(imgH) * s))
	w = max(1, min(w, boxW))
	h = max(1, min(h, boxH))
	return w, h
}

// scaleOver resizes img to w x h and alpha-composites it over black.
func scaleOver(img image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
	scaler(img, w, h).Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Over, nil)
	return dst
}

// scaleKeepAlpha resizes img to w x h preserving transparency.
func scaleKeepAlpha(img image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	scaler(img, w, h).Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
	return dst
}

func scaler(img image.Image, w, h int) draw.Scaler {
	b := img.Bounds()
	if w*h > 0 && (w > b.Dx()*2 || h > b.Dy()*2) {
		// Big upscales: keep it crisp-ish and cheap.
		return draw.ApproxBiLinear
	}
	return draw.CatmullRom
}

// HalfBlock renders img as colored "▀" cells (fg = top pixel, bg = bottom
// pixel) fitting within maxW x maxH cells. With truecolor=false colors are
// quantized to the xterm 256-color palette. Lines are joined by "\n"; each
// has exactly the rendered width and ends with an SGR reset.
func HalfBlock(img image.Image, maxW, maxH int, truecolor bool) string {
	b := img.Bounds()
	w, ph := fitPixels(b.Dx(), b.Dy(), maxW, maxH*2, false)
	return halfBlockSized(img, w, ph, truecolor)
}

func halfBlockSized(img image.Image, w, ph int, truecolor bool) string {
	if w <= 0 || ph <= 0 {
		return ""
	}
	px := scaleOver(img, w, ph)
	rows := (ph + 1) / 2
	var sb strings.Builder
	sb.Grow(rows * w * 24)
	for row := 0; row < rows; row++ {
		if row > 0 {
			sb.WriteByte('\n')
		}
		var lastFg, lastBg string
		for x := 0; x < w; x++ {
			top := px.RGBAAt(x, row*2)
			fg := sgrColor(top, true, truecolor)
			bg := "49" // default background for the missing last odd row
			if row*2+1 < ph {
				bg = sgrColor(px.RGBAAt(x, row*2+1), false, truecolor)
			}
			if fg != lastFg || bg != lastBg {
				sb.WriteString("\x1b[")
				sb.WriteString(fg)
				sb.WriteByte(';')
				sb.WriteString(bg)
				sb.WriteByte('m')
				lastFg, lastBg = fg, bg
			}
			sb.WriteString("▀")
		}
		sb.WriteString("\x1b[0m")
	}
	return sb.String()
}

func sgrColor(c color.RGBA, fg, truecolor bool) string {
	if truecolor {
		p := "48;2;"
		if fg {
			p = "38;2;"
		}
		return p + strconv.Itoa(int(c.R)) + ";" + strconv.Itoa(int(c.G)) + ";" + strconv.Itoa(int(c.B))
	}
	p := "48;5;"
	if fg {
		p = "38;5;"
	}
	return p + strconv.Itoa(Color256(c.R, c.G, c.B))
}

var cubeLevels = [6]int{0, 95, 135, 175, 215, 255}

// Color256 returns the nearest xterm-256 palette index for r,g,b, choosing
// between the 6x6x6 color cube (16-231) and the grayscale ramp (232-255).
func Color256(r, g, b uint8) int {
	ci := func(v uint8) int {
		best, bd := 0, 1<<30
		for i, l := range cubeLevels {
			d := int(v) - l
			if d*d < bd {
				best, bd = i, d*d
			}
		}
		return best
	}
	ri, gi, bi := ci(r), ci(g), ci(b)
	cubeD := dist2(int(r), int(g), int(b), cubeLevels[ri], cubeLevels[gi], cubeLevels[bi])

	avg := (int(r) + int(g) + int(b)) / 3
	gi2 := 0
	if avg > 8 {
		gi2 = min(23, (avg-8+5)/10)
	}
	gv := 8 + 10*gi2
	grayD := dist2(int(r), int(g), int(b), gv, gv, gv)
	if grayD < cubeD {
		return 232 + gi2
	}
	return 16 + 36*ri + 6*gi + bi
}

// palette256RGB returns the RGB value of a cube/gray index (16-255).
func palette256RGB(i int) (r, g, b int) {
	if i >= 232 {
		v := 8 + 10*(i-232)
		return v, v, v
	}
	i -= 16
	return cubeLevels[i/36], cubeLevels[(i/6)%6], cubeLevels[i%6]
}

func dist2(r1, g1, b1, r2, g2, b2 int) int {
	dr, dg, db := r1-r2, g1-g2, b1-b2
	return dr*dr + dg*dg + db*db
}
