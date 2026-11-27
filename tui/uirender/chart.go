package uirender

import (
	"math"
	"strings"
)

// Series colors (green shades, white, amber, ...) and the dimmer shade used
// for area fills.
var (
	seriesColors = []int{cBright, cWhite, cWarn, cMid2, cText2, cErr, cHunk}
	seriesDim    = map[int]int{cBright: cDim, cWhite: 244, cWarn: 136, cMid2: cDim2, cText2: 65, cErr: 131, cHunk: 23}
)

type series struct {
	name  string
	data  []float64
	color int
}

const blocks8 = "▁▂▃▄▅▆▇█"

func block8(n int) string { // n in 1..8 eighths
	n = clampInt(n, 1, 8)
	return string([]rune(blocks8)[n-1])
}

// renderChart draws line/area charts with braille dots (2x4 per cell),
// bar/stackedBar with eighth blocks, and sparkline as one row of ▁..█.
// height (px) maps to rows at ~20px per row, clamped to 3..14.
func renderChart(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	kind := str(p["kind"])
	if kind == "" {
		kind = "line"
	}
	var ss []series
	n := 0
	for i, x := range list(p["series"]) {
		m := obj(x)
		if m == nil {
			continue
		}
		s := series{name: str(m["name"]), color: seriesColors[i%len(seriesColors)]}
		for _, d := range list(m["data"]) {
			if dm := obj(d); dm != nil {
				d = dm["y"]
			}
			s.data = append(s.data, num(d, 0))
		}
		n = max(n, len(s.data))
		ss = append(ss, s)
	}
	if n == 0 {
		return c.Paint(sFaint, trunc("no data", width))
	}
	if kind == "sparkline" {
		return c.sparklines(ss, p, width)
	}
	rows := clampInt(int(math.Round(num(p["height"], 180)/20)), 3, 14)
	if rows%2 == 1 && rows >= 5 { // even, so the middle label sits exactly at half
		rows++
	}
	stacked := kind == "stackedBar"
	isBar := kind == "bar" || stacked
	lo, hi := 0.0, 0.0
	for i := 0; i < n; i++ {
		tot := 0.0
		for _, s := range ss {
			if i < len(s.data) {
				v := s.data[i]
				tot += v
				hi = math.Max(hi, v)
				if !isBar {
					lo = math.Min(lo, v)
				}
			}
		}
		if stacked {
			hi = math.Max(hi, tot)
		}
	}
	if p["max"] != nil {
		hi = num(p["max"], hi)
	} else {
		hi = niceCeil(hi)
	}
	if lo < 0 {
		lo = -niceCeil(-lo)
	}
	if hi <= lo {
		hi = lo + 1
	}
	unit := str(p["yUnit"])
	ylab := map[int]string{0: fmtNum(hi) + unit}
	if rows >= 5 {
		ylab[rows/2] = fmtNum(hi-(hi-lo)*float64(rows/2)/float64(rows)) + unit
	}
	bottom := fmtNum(lo) + unit
	lw := sw(bottom)
	for _, l := range ylab {
		lw = max(lw, sw(l))
	}
	pw := width - lw - 1
	if pw < 4 {
		return c.sparklines(ss, p, width)
	}
	var grid [][]cell
	var xpos []int // x label positions (cell columns), per data index
	labels := list(p["labels"])
	first := 0 // index of the first data point shown
	if isBar {
		grid, xpos, first = c.bars(ss, n, rows, pw, hi, stacked)
	} else {
		grid, xpos = c.lines(ss, n, rows, pw, lo, hi, kind == "area")
	}
	var out []string
	if s := str(p["yLabel"]); s != "" {
		out = append(out, c.Paint(sFaint, trunc(s, width)))
	}
	for r := 0; r < rows; r++ {
		axis := "│"
		l, ok := ylab[r]
		if ok {
			axis = "┤"
		}
		out = append(out, c.Paint(sFaint, padLeft(l, lw)+axis)+c.cellRow(grid[r]))
	}
	out = append(out, c.Paint(sFaint, padLeft(bottom, lw)+"└"+rep("─", pw)))
	if len(labels) > 0 {
		step := 1
		var pos []int
		var ls []string
		cnt := 0
		for i := range xpos {
			if first+i < len(labels) && labels[first+i] != nil {
				cnt++
			}
		}
		if cnt > 8 {
			step = (cnt + 7) / 8
		}
		for i, x := range xpos {
			if (first+i)%step != 0 || first+i >= len(labels) || labels[first+i] == nil {
				continue
			}
			pos = append(pos, x)
			ls = append(ls, str(labels[first+i]))
		}
		out = append(out, rep(" ", lw+1)+c.Paint(sFaint, placeLabels(pos, ls, pw)))
	}
	if len(ss) > 1 || ss[0].name != "" {
		out = append(out, c.legend(ss, width)...)
	}
	if s := str(p["xLabel"]); s != "" {
		out = append(out, rep(" ", lw+1)+c.Paint(sFaint, center(trunc(s, pw), pw)))
	}
	return strings.Join(out, "\n")
}

func (c *Ctx) legend(ss []series, width int) []string {
	var out []string
	line, lw := "", 0
	for _, s := range ss {
		name := s.name
		if name == "" {
			name = "series"
		}
		item := c.fg(s.color, "■") + " " + c.Paint(sMuted, trunc(name, max(1, width-2)))
		iw := sw(trunc(name, max(1, width-2))) + 2
		if lw > 0 && lw+2+iw > width {
			out = append(out, line)
			line, lw = "", 0
		}
		if lw > 0 {
			line += "  "
			lw += 2
		}
		line += item
		lw += iw
	}
	if lw > 0 {
		out = append(out, line)
	}
	return out
}

var brailleBits = [4][2]uint8{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}

// lines plots series on a braille canvas (2x4 dots per cell). Area fills
// below each line in a dimmer shade.
func (c *Ctx) lines(ss []series, n, rows, pw int, lo, hi float64, area bool) ([][]cell, []int) {
	W, H := pw*2, rows*4
	bits := make([][]uint8, rows)
	col := make([][]int, rows)
	isLine := make([][]bool, rows)
	for r := range bits {
		bits[r] = make([]uint8, pw)
		col[r] = make([]int, pw)
		isLine[r] = make([]bool, pw)
	}
	set := func(x, y, color int, line bool) {
		if x < 0 || y < 0 || x >= W || y >= H {
			return
		}
		cx, cy := x/2, y/4
		bits[cy][cx] |= brailleBits[y%4][x%2]
		if line {
			col[cy][cx], isLine[cy][cx] = color, true
		} else if !isLine[cy][cx] {
			col[cy][cx] = color
		}
	}
	px := func(i int) int {
		if n == 1 {
			return W / 2
		}
		return int(math.Round(float64(i) * float64(W-1) / float64(n-1)))
	}
	py := func(v float64) int {
		f := (v - lo) / (hi - lo)
		return clampInt(H-1-int(math.Round(f*float64(H-1))), 0, H-1)
	}
	type pt struct{ x, y int }
	paths := make([][]pt, len(ss))
	for k, s := range ss {
		var prev *pt
		for i, v := range s.data {
			cur := pt{px(i), py(v)}
			if prev == nil {
				paths[k] = append(paths[k], cur)
			} else { // Bresenham
				x0, y0, x1, y1 := prev.x, prev.y, cur.x, cur.y
				dx, dy := abs(x1-x0), -abs(y1-y0)
				sx, sy := sign(x1-x0), sign(y1-y0)
				e := dx + dy
				for {
					if x0 != prev.x || y0 != prev.y {
						paths[k] = append(paths[k], pt{x0, y0})
					}
					if x0 == x1 && y0 == y1 {
						break
					}
					e2 := 2 * e
					if e2 >= dy {
						e += dy
						x0 += sx
					}
					if e2 <= dx {
						e += dx
						y0 += sy
					}
				}
			}
			prev = &cur
		}
	}
	if area {
		for k, s := range ss {
			top := map[int]int{}
			for _, q := range paths[k] {
				if t, ok := top[q.x]; !ok || q.y < t {
					top[q.x] = q.y
				}
			}
			for x, t := range top {
				for y := t + 1; y < H; y++ {
					set(x, y, seriesDim[s.color], false)
				}
			}
		}
	}
	for k, s := range ss {
		for _, q := range paths[k] {
			set(q.x, q.y, s.color, true)
		}
	}
	grid := make([][]cell, rows)
	for r := range grid {
		grid[r] = newCells(pw)
		for x := 0; x < pw; x++ {
			if bits[r][x] != 0 {
				grid[r][x] = cell{rune(0x2800 + int(bits[r][x])), Style{FG: col[r][x]}}
			}
		}
	}
	var xpos []int
	for i := 0; i < n; i++ {
		xpos = append(xpos, px(i)/2)
	}
	return grid, xpos
}

// bars draws grouped (or stacked) vertical bars with eighth-block tops. If
// there isn't room for every group, the last ones that fit are shown; first
// is the index of the first shown.
func (c *Ctx) bars(ss []series, n, rows, pw int, hi float64, stacked bool) ([][]cell, []int, int) {
	per := len(ss)
	if stacked {
		per = 1
	}
	first := 0
	if n*per > pw {
		first = n - pw/per
		if first < 0 {
			first = 0
		}
	}
	shown := n - first
	gw := pw / max(1, shown)
	inner := gw
	if gw > per {
		inner = gw - 1
	}
	bw := clampInt(inner/per, 1, 6)
	grid := make([][]cell, rows)
	for r := range grid {
		grid[r] = newCells(pw)
	}
	levels := float64(rows * 8)
	var xpos []int
	for g := 0; g < shown; g++ {
		i := first + g
		gx := g*gw + (gw-bw*per)/2
		xpos = append(xpos, gx+bw*per/2)
		if stacked {
			type seg struct {
				top   int
				color int
			}
			var segs []seg
			tot := 0.0
			for _, s := range ss {
				if i < len(s.data) && s.data[i] > 0 {
					tot += s.data[i]
					segs = append(segs, seg{int(math.Round(math.Min(tot, hi) / hi * levels)), s.color})
				}
			}
			colorAt := func(level float64) int {
				for _, sg := range segs {
					if level < float64(sg.top) {
						return sg.color
					}
				}
				if len(segs) > 0 {
					return segs[len(segs)-1].color
				}
				return cBright
			}
			T := 0
			if len(segs) > 0 {
				T = segs[len(segs)-1].top
			}
			for r := 0; r < rows; r++ {
				base := (rows - 1 - r) * 8
				fill := T - base
				if fill <= 0 {
					continue
				}
				ch := "█"
				mid := float64(base) + 4
				if fill < 8 {
					ch = block8(fill)
					mid = float64(base) + float64(fill)/2
				}
				for x := gx; x < gx+bw && x < pw; x++ {
					grid[r][x] = cell{[]rune(ch)[0], Style{FG: colorAt(mid)}}
				}
			}
			continue
		}
		for k, s := range ss {
			if i >= len(s.data) {
				continue
			}
			lv := int(math.Round(math.Max(0, math.Min(s.data[i], hi)) / hi * levels))
			if lv == 0 && s.data[i] > 0 {
				lv = 1
			}
			for r := 0; r < rows; r++ {
				fill := lv - (rows-1-r)*8
				if fill <= 0 {
					continue
				}
				ch := '█'
				if fill < 8 {
					ch = []rune(block8(fill))[0]
				}
				for x := gx + k*bw; x < gx+(k+1)*bw && x < pw; x++ {
					grid[r][x] = cell{ch, Style{FG: s.color}}
				}
			}
		}
	}
	return grid, xpos, first
}

// sparklines draws each series as one row: name, ▁..█ (bucket-averaged to
// fit), last value.
func (c *Ctx) sparklines(ss []series, p map[string]any, width int) string {
	nw := 0
	for _, s := range ss {
		nw = max(nw, sw(s.name))
	}
	nw = min(nw, width/3)
	var out []string
	for _, s := range ss {
		if len(s.data) == 0 {
			continue
		}
		last := fmtNum(s.data[len(s.data)-1]) + str(p["yUnit"])
		avail := width
		if nw > 0 {
			avail -= nw + 1
		}
		if avail-sw(last)-1 >= 4 {
			avail -= sw(last) + 1
		} else {
			last = ""
		}
		data := s.data
		if len(data) > avail && avail > 0 {
			b := make([]float64, avail)
			for i := range b {
				lo, hi := i*len(data)/avail, (i+1)*len(data)/avail
				sum := 0.0
				for _, v := range data[lo:max(hi, lo+1)] {
					sum += v
				}
				b[i] = sum / float64(max(hi-lo, 1))
			}
			data = b
		}
		hi := 0.0
		for _, v := range data {
			hi = math.Max(hi, v)
		}
		if p["max"] != nil {
			hi = num(p["max"], hi)
		}
		if hi <= 0 {
			hi = 1
		}
		var b strings.Builder
		for _, v := range data {
			b.WriteString(block8(int(math.Round(math.Max(0, math.Min(v, hi))/hi*7)) + 1))
		}
		line := c.fg(s.color, b.String())
		if nw > 0 {
			line = c.Paint(sMuted, pad(s.name, nw)) + " " + line
		}
		if last != "" {
			line += " " + c.Paint(sFaint, last)
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// niceCeil rounds x up to 1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6 or 8 × 10^k.
func niceCeil(x float64) float64 {
	if x <= 0 || math.IsInf(x, 0) || math.IsNaN(x) {
		return x
	}
	e := math.Pow(10, math.Floor(math.Log10(x)))
	for _, f := range []float64{1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10} {
		if v := f * e; v >= x*(1-1e-9) {
			return v
		}
	}
	return 10 * e
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func sign(x int) int {
	switch {
	case x > 0:
		return 1
	case x < 0:
		return -1
	}
	return 0
}
