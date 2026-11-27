package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// canvas is a grid of cells that ANSI-styled strings (lipgloss output) are
// blitted onto, so overlays (help, prompts, forms) can sit on top of a view.
// Cells track whether anything was drawn on them.
type cell struct {
	r   rune
	w   int8   // display width; 0 for the right half of a wide rune
	sgr string // accumulated SGR state for this cell ("" = default)
	set bool   // something was drawn here
}

type canvas struct {
	w, h  int
	cells []cell
}

func newCanvas(w, h int) *canvas {
	c := &canvas{w: max(w, 1), h: max(h, 1)}
	c.cells = make([]cell, c.w*c.h)
	return c
}

func (c *canvas) at(x, y int) *cell {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return nil
	}
	return &c.cells[y*c.w+x]
}

// blit draws a (multi-line, ANSI-styled) string with its top-left at x,y,
// clipped to the canvas and to maxW columns if maxW > 0.
func (c *canvas) blit(x, y int, s string, maxW int) {
	for i, line := range strings.Split(s, "\n") {
		c.blitLine(x, y+i, line, maxW)
	}
}

func (c *canvas) blitLine(x0, y int, s string, maxW int) {
	if y < 0 || y >= c.h {
		return
	}
	x, sgr := x0, ""
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) {
			switch s[i+1] {
			case '[': // CSI
				j := i + 2
				for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
					j++
				}
				if j < len(s) && s[j] == 'm' {
					seq := s[i : j+1]
					if seq == "\x1b[0m" || seq == "\x1b[m" {
						sgr = ""
					} else {
						sgr += seq
					}
				}
				i = j + 1
				continue
			case ']': // OSC: skip to BEL or ST
				j := i + 2
				for j < len(s) && s[j] != 0x07 && !(s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\') {
					j++
				}
				if j < len(s) && s[j] == 0x1b {
					j++
				}
				i = j + 1
				continue
			}
			i++
			continue
		}
		r, size := rune(s[i]), 1
		if r >= 0x80 {
			r, size = decodeRune(s[i:])
		}
		i += size
		if r == '\t' {
			r = ' '
		}
		rw := runewidth.RuneWidth(r)
		if rw == 0 {
			continue
		}
		if maxW > 0 && x+rw > x0+maxW {
			return
		}
		if cl := c.at(x, y); cl != nil {
			*cl = cell{r: r, w: int8(rw), sgr: sgr, set: true}
			if rw == 2 {
				if cr := c.at(x+1, y); cr != nil {
					*cr = cell{w: 0, sgr: sgr, set: true}
				} else {
					*cl = cell{r: ' ', w: 1, sgr: sgr, set: true}
				}
			}
		}
		x += rw
	}
}

func decodeRune(s string) (rune, int) { return utf8.DecodeRuneInString(s) }

// fill marks a rectangle as drawn (blank), so effects leave it alone.
func (c *canvas) fill(x, y, w, h int) {
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			if cl := c.at(xx, yy); cl != nil {
				*cl = cell{r: ' ', w: 1, set: true}
			}
		}
	}
}

func (c *canvas) String() string {
	var b strings.Builder
	b.Grow(c.w * c.h * 2)
	for y := 0; y < c.h; y++ {
		cur := ""
		for x := 0; x < c.w; x++ {
			cl := c.cells[y*c.w+x]
			if cl.set && cl.w == 0 {
				continue
			}
			if cl.sgr != cur {
				b.WriteString("\x1b[0m")
				b.WriteString(cl.sgr)
				cur = cl.sgr
			}
			if cl.r == 0 {
				b.WriteByte(' ')
			} else {
				b.WriteRune(cl.r)
			}
		}
		if cur != "" {
			b.WriteString("\x1b[0m")
		}
		if y < c.h-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
