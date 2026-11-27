package uirender

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
)

// Options control one Render call.
type Options struct {
	Width int                                     // available columns (<= 0 means 80)
	State map[string]any                          // if non-nil, used instead of spec.State (live form edits)
	Image func(src string, width int) string      // renders an Image's picture; nil → "[image: src]"
	Fetch func(src string) (body string, ok bool) // loads a Diff's src; ok=false → "loading…"
	Focus string                                  // element id to highlight (the TUI's form cursor)
	Color bool                                    // false → plain text, no escape codes

	// Tabs selects a tab per Tabs element id. A Tabs element with no entry
	// shows every tab stacked under its header instead.
	Tabs map[string]int
	// Now is "now" for Timeline (open-ended bars and the now marker); zero
	// means time.Now().
	Now time.Time
}

// Ctx is the rendering context passed to components.
type Ctx struct {
	Spec  *Spec
	Opts  Options
	State map[string]any
	Now   time.Time
	depth int
	memo  map[memoKey]string
}

type memoKey struct {
	id    string
	width int
}

// Component renders one element at the given width. props are already
// resolved against state; el carries the raw element (children, on, and the
// unresolved props for bindings). The result is lines joined by "\n"; lines
// wider than width are truncated by the caller.
type Component func(c *Ctx, id string, el *Element, props map[string]any, width int) string

// Registry maps component types to renderers. Unknown types render as a dim
// box with the type and props. It is filled in init (its entries refer back
// to Registry through Ctx.Children).
var Registry = map[string]Component{}

func init() {
	for k, v := range map[string]Component{
		"Card": renderCard, "Stack": renderStack, "Grid": renderGrid, "Tabs": renderTabs,
		"Heading": renderHeading, "Text": renderText, "Badge": renderBadge, "Divider": renderDivider,
		"Stat": renderStat, "Meter": renderMeter, "Table": renderTable, "List": renderList,
		"KeyValue": renderKeyValue, "Timeline": renderTimeline,
		"Chart": renderChart, "Graph": renderGraph,
		"Choice": renderChoice, "TextInput": renderTextInput, "Toggle": renderToggle, "Select": renderSelect,
		"Button": renderButton, "Buttons": renderButtons,
		"Code": renderCode, "Diff": renderDiff, "Image": renderImage,
	} {
		Registry[k] = v
	}
}

const maxDepth = 40

func newCtx(spec *Spec, opts Options) *Ctx {
	st := opts.State
	if st == nil && spec != nil {
		st = spec.State
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	return &Ctx{Spec: spec, Opts: opts, State: st, Now: now, memo: map[memoKey]string{}}
}

// Render renders the spec's root. Every line fits opts.Width display cells.
func Render(spec *Spec, opts Options) string {
	w := opts.Width
	if w <= 0 {
		w = 80
	}
	c := newCtx(spec, opts)
	var out string
	if spec == nil || spec.Elements == nil {
		out = c.errLine("bad spec", w)
	} else {
		out, _ = c.Node(spec.Root, w)
	}
	ls := strings.Split(out, "\n")
	for i, l := range ls {
		ls[i] = strings.TrimRight(trunc(l, w), " ")
	}
	return strings.Join(ls, "\n")
}

// Node renders one element by id. visible is false when its "visible"
// condition fails (out is then "").
func (c *Ctx) Node(id string, width int) (out string, visible bool) {
	if width < 1 {
		width = 1
	}
	el := c.Spec.Elements[id]
	if el == nil {
		return c.errLine("missing element "+strconv.Quote(id), width), true
	}
	if c.depth >= maxDepth {
		return c.errLine("nested too deeply at "+strconv.Quote(id), width), true
	}
	if el.Visible != nil && !c.Cond(el.Visible) {
		return "", false
	}
	key := memoKey{id, width}
	if s, ok := c.memo[key]; ok {
		return s, true
	}
	c.depth++
	out = clampBlock(c.call(id, el, width), width)
	c.depth--
	c.memo[key] = out
	return out, true
}

func (c *Ctx) call(id string, el *Element, width int) (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = c.errLine(fmt.Sprintf("%s: %v", el.Type, r), width)
		}
	}()
	props, _ := c.Resolve(map[string]any(el.Props)).(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	comp := Registry[el.Type]
	if comp == nil {
		return renderUnknown(c, id, el, props, width)
	}
	return comp(c, id, el, props, width)
}

// Children renders the element's visible children at width, dropping empty
// ones.
func (c *Ctx) Children(el *Element, width int) []string {
	var out []string
	for _, k := range el.Children {
		if s, ok := c.Node(k, width); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// visibleKids lists the ids of children whose condition passes (missing ids
// are kept so they render as errors).
func (c *Ctx) visibleKids(el *Element) []string {
	var out []string
	for _, k := range el.Children {
		e := c.Spec.Elements[k]
		if e != nil && e.Visible != nil && !c.Cond(e.Visible) {
			continue
		}
		out = append(out, k)
	}
	return out
}

func (c *Ctx) errLine(msg string, width int) string {
	ls := wrapPlain("⚠ "+clean(msg), width)
	for i, l := range ls {
		ls[i] = c.Paint(sErr, l)
	}
	return strings.Join(ls, "\n")
}

// ---- styles ----

// Style is a terminal text style with 256-color foreground/background
// (0 = unset; use 16 for black).
type Style struct {
	FG, BG                           int
	Bold, Italic, Underline, Reverse bool
	Faint                            bool
}

// Palette ("matrix" phosphor on black).
const (
	cBright  = 46
	cBright2 = 82
	cMid     = 40
	cMid2    = 34
	cDim     = 28
	cDim2    = 22
	cText    = 157
	cText2   = 120
	cWhite   = 231
	cFaint   = 240
	cWarn    = 214
	cErr     = 203
	cHunk    = 37
	cCode    = 151
	cBlack   = 16
)

var (
	sText   = Style{FG: cText}
	sMuted  = Style{FG: cDim}
	sFaint  = Style{FG: cFaint}
	sLine   = Style{FG: cDim2}
	sBorder = Style{FG: cDim}
	sTitle  = Style{FG: cWhite, Bold: true}
	sGreen  = Style{FG: cBright}
	sErr    = Style{FG: cErr}
	sWarn   = Style{FG: cWarn}
	sFocus  = Style{FG: cBright, Bold: true}
)

func toneColor(tone string, def int) int {
	switch tone {
	case "green", "success", "ok":
		return cBright
	case "warn", "warning":
		return cWarn
	case "error", "danger":
		return cErr
	case "muted":
		return cDim
	case "faint":
		return cFaint
	}
	return def
}

func statusGlyph(status string) (string, int) {
	switch status {
	case "running":
		return "●", cBright
	case "idle":
		return "○", cMid2
	case "done":
		return "✓", cMid2
	case "failed":
		return "✗", cErr
	case "waiting", "starting":
		return "◌", cWarn
	case "":
		return "", 0
	}
	return "•", cFaint
}

var (
	rendOnce sync.Once
	rend     *lipgloss.Renderer
)

func renderer() *lipgloss.Renderer {
	rendOnce.Do(func() {
		rend = lipgloss.NewRenderer(io.Discard)
		rend.SetColorProfile(termenv.ANSI256)
		rend.SetHasDarkBackground(true)
	})
	return rend
}

// Paint styles text (each line separately). With Color off it returns text
// unchanged.
func (c *Ctx) Paint(st Style, text string) string {
	if !c.Opts.Color || text == "" || st == (Style{}) {
		return text
	}
	ls := renderer().NewStyle()
	if st.FG > 0 {
		ls = ls.Foreground(lipgloss.Color(strconv.Itoa(st.FG)))
	}
	if st.BG > 0 {
		ls = ls.Background(lipgloss.Color(strconv.Itoa(st.BG)))
	}
	ls = ls.Bold(st.Bold).Italic(st.Italic).Underline(st.Underline).Reverse(st.Reverse).Faint(st.Faint)
	if !strings.Contains(text, "\n") {
		return ls.Render(text)
	}
	parts := strings.Split(text, "\n")
	for i, p := range parts {
		if p != "" {
			parts[i] = ls.Render(p)
		}
	}
	return strings.Join(parts, "\n")
}

func (c *Ctx) fg(color int, text string) string { return c.Paint(Style{FG: color}, text) }

// link wraps text in an OSC 8 hyperlink when color is on and the URL is a
// plain http(s) URL.
func (c *Ctx) link(url, text string) string {
	if !c.Opts.Color || !safeURL(url) {
		return text
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

func safeURL(u string) bool {
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return false
	}
	for _, r := range u {
		if r <= 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return false
		}
	}
	return true
}

// ---- text layout helpers ----

func sw(s string) int { return ansi.StringWidth(s) }

// trunc cuts s to w cells with an ellipsis.
func trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if sw(s) <= w {
		return s
	}
	if w == 1 {
		return ansi.Truncate(s, 1, "")
	}
	return ansi.Truncate(s, w, "…")
}

func rep(s string, n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(s, n)
}

// pad truncates or right-pads s to exactly w cells.
func pad(s string, w int) string {
	n := sw(s)
	if n > w {
		s = trunc(s, w)
		n = sw(s)
	}
	return s + rep(" ", w-n)
}

func padLeft(s string, w int) string {
	n := sw(s)
	if n > w {
		return trunc(s, w)
	}
	return rep(" ", w-n) + s
}

func center(s string, w int) string {
	n := sw(s)
	if n >= w {
		return trunc(s, w)
	}
	l := (w - n) / 2
	return rep(" ", l) + s + rep(" ", w-n-l)
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func blockWidth(s string) int {
	w := 0
	for _, l := range strings.Split(s, "\n") {
		if n := sw(l); n > w {
			w = n
		}
	}
	return w
}

func clampBlock(s string, w int) string {
	if s == "" {
		return s
	}
	ls := strings.Split(s, "\n")
	changed := false
	for i, l := range ls {
		if sw(l) > w {
			ls[i] = trunc(l, w)
			changed = true
		}
	}
	if !changed {
		return s
	}
	return strings.Join(ls, "\n")
}

// wrapPlain word-wraps unstyled text to w cells, keeping explicit newlines
// and hard-breaking words longer than w. Leading indentation is kept.
func wrapPlain(s string, w int) []string {
	if w < 1 {
		w = 1
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		indent := para[:len(para)-len(strings.TrimLeft(para, " "))]
		if sw(indent) > w/2 {
			indent = ""
		}
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line, lw := indent, sw(indent)
		start := true
		for _, wd := range words {
			ww := runewidth.StringWidth(wd)
			if !start && lw+1+ww <= w {
				line += " " + wd
				lw += 1 + ww
				continue
			}
			if !start {
				out = append(out, line)
				line, lw = indent, sw(indent)
			}
			start = false
			if lw+ww <= w {
				line += wd
				lw += ww
				continue
			}
			chunks := breakWord(wd, w-lw)
			for _, ch := range chunks[:len(chunks)-1] {
				out = append(out, line+ch)
				line, lw = indent, sw(indent)
			}
			last := chunks[len(chunks)-1]
			line += last
			lw += runewidth.StringWidth(last)
		}
		out = append(out, line)
	}
	return out
}

// breakWord splits s into chunks of at most w cells (preserving spaces).
func breakWord(s string, w int) []string {
	if w < 1 {
		w = 1
	}
	var out []string
	var cur strings.Builder
	cw := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if cw+rw > w && cw > 0 {
			out = append(out, cur.String())
			cur.Reset()
			cw = 0
		}
		cur.WriteRune(r)
		cw += rw
	}
	out = append(out, cur.String())
	return out
}

// paintLines wraps text and paints every line.
func (c *Ctx) paintWrapped(st Style, text string, w int) []string {
	ls := wrapPlain(text, w)
	for i, l := range ls {
		ls[i] = c.Paint(st, l)
	}
	return ls
}

// joinGap stacks blocks vertically with gap blank lines between them.
func joinGap(blocks []string, gap int) string {
	var nb []string
	for _, b := range blocks {
		if b != "" {
			nb = append(nb, b)
		}
	}
	return strings.Join(nb, "\n"+rep("\n", gap))
}

// hjoin places blocks side by side, each padded to its width, gap columns
// apart. valign is start|center|end.
func hjoin(blocks []string, widths []int, gap int, valign string) string {
	h := 0
	ls := make([][]string, len(blocks))
	for i, b := range blocks {
		ls[i] = splitLines(b)
		if len(ls[i]) > h {
			h = len(ls[i])
		}
	}
	var out []string
	for r := 0; r < h; r++ {
		var row strings.Builder
		for i := range blocks {
			if i > 0 {
				row.WriteString(rep(" ", gap))
			}
			off := 0
			switch valign {
			case "center":
				off = (h - len(ls[i])) / 2
			case "end":
				off = h - len(ls[i])
			}
			line := ""
			if k := r - off; k >= 0 && k < len(ls[i]) {
				line = ls[i][k]
			}
			row.WriteString(pad(line, widths[i]))
		}
		out = append(out, row.String())
	}
	return strings.Join(out, "\n")
}

// innerWidth is the content width inside a box of width w.
func innerWidth(w int) int {
	if w >= 8 {
		return w - 4
	}
	return w
}

// box draws a rounded border around body lines with title (and an optional
// right-hand note) in the top edge: ╭─ Title ──── note ─╮. Below 8 columns
// it draws no border.
func (c *Ctx) box(title string, titleSt Style, right string, border Style, body []string, width int) string {
	if width < 8 {
		var out []string
		if title != "" {
			out = append(out, c.Paint(titleSt, trunc(title, width)))
		}
		return strings.Join(append(out, body...), "\n")
	}
	inner := width - 4
	avail := width - 2 // between the corners
	var top strings.Builder
	top.WriteString(c.Paint(border, "╭"))
	used := 0
	if title != "" {
		t := trunc(title, avail-4)
		top.WriteString(c.Paint(border, "─ "))
		top.WriteString(c.Paint(titleSt, t))
		top.WriteString(" ")
		used = sw(t) + 3
	}
	rightPart := ""
	if right != "" && avail-used-4 >= 3 {
		r := trunc(right, avail-used-4)
		rightPart = " " + r + " " + c.Paint(border, "─")
		used += sw(r) + 3
	}
	top.WriteString(c.Paint(border, rep("─", avail-used)))
	top.WriteString(rightPart)
	top.WriteString(c.Paint(border, "╮"))
	out := []string{top.String()}
	side := c.Paint(border, "│")
	for _, b := range body {
		for _, l := range strings.Split(b, "\n") {
			out = append(out, side+" "+pad(l, inner)+" "+side)
		}
	}
	out = append(out, c.Paint(border, "╰"+rep("─", avail)+"╯"))
	return strings.Join(out, "\n")
}

// cell is one styled terminal cell, for grids (charts, timelines, graphs).
type cell struct {
	r  rune
	st Style
}

func (c *Ctx) cellRow(row []cell) string {
	var b strings.Builder
	var run strings.Builder
	var cur Style
	flush := func() {
		if run.Len() > 0 {
			b.WriteString(c.Paint(cur, run.String()))
			run.Reset()
		}
	}
	for i, ce := range row {
		r := ce.r
		if r == 0 {
			r = ' '
		}
		if i == 0 || ce.st != cur {
			flush()
			cur = ce.st
		}
		run.WriteRune(r)
	}
	flush()
	return b.String()
}

func newCells(w int) []cell {
	if w < 0 {
		w = 0
	}
	row := make([]cell, w)
	for i := range row {
		row[i].r = ' '
	}
	return row
}
