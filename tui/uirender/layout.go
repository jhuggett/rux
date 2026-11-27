package uirender

import (
	"encoding/json"
	"strings"
)

func renderCard(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	border := Style{FG: toneColor(str(p["tone"]), cDim)}
	inner := innerWidth(width)
	var body []string
	if sub := str(p["subtitle"]); sub != "" {
		body = append(body, c.paintWrapped(sMuted, sub, inner)...)
	}
	kids := joinGap(c.Children(el, inner), 1)
	if kids != "" {
		if len(body) > 0 {
			body = append(body, "")
		}
		body = append(body, kids)
	}
	return c.box(str(p["title"]), sTitle, "", border, body, width)
}

func renderStack(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	gap := clampInt(int(num(p["gap"], 2)), 0, 6)
	align := str(p["align"])
	if str(p["direction"]) == "horizontal" {
		va := "center" // web rows center items vertically
		switch align {
		case "start", "stretch":
			va = "start"
		case "end":
			va = "end"
		}
		return c.row(c.visibleKids(el), width, max(1, gap), va, truthy(p["wrap"]), gap/2)
	}
	kids := c.Children(el, width)
	if align == "center" || align == "end" {
		for i, k := range kids {
			bw := blockWidth(k)
			off := width - bw
			if align == "center" {
				off /= 2
			}
			if off > 0 {
				ls := strings.Split(k, "\n")
				for j, l := range ls {
					ls[j] = rep(" ", off) + l
				}
				kids[i] = strings.Join(ls, "\n")
			}
		}
	}
	return joinGap(kids, min(gap/2, 3))
}

// row lays children out side by side. Children keep their natural width when
// everything fits; otherwise space is shared (narrow children keep theirs,
// wide ones split the rest). With wrap, children flow onto more rows instead.
// If there's no room for even 4 columns each, it stacks vertically.
func (c *Ctx) row(ids []string, width, gap int, valign string, wrap bool, rowGap int) string {
	type item struct {
		id  string
		out string
		nat int
	}
	var items []item
	for _, id := range ids {
		s, ok := c.Node(id, width)
		if !ok || s == "" {
			continue
		}
		items = append(items, item{id, s, blockWidth(s)})
	}
	if len(items) == 0 {
		return ""
	}
	if wrap {
		var rows []string
		var blocks []string
		var widths []int
		used := 0
		flush := func() {
			if len(blocks) > 0 {
				rows = append(rows, hjoin(blocks, widths, gap, valign))
			}
			blocks, widths, used = nil, nil, 0
		}
		for _, it := range items {
			w := min(it.nat, width)
			if len(blocks) > 0 && used+gap+w > width {
				flush()
			}
			if len(blocks) > 0 {
				used += gap
			}
			blocks = append(blocks, it.out)
			widths = append(widths, w)
			used += w
		}
		flush()
		return joinGap(rows, rowGap)
	}
	n := len(items)
	avail := width - gap*(n-1)
	if avail < n*4 {
		var blocks []string
		for _, it := range items {
			blocks = append(blocks, it.out)
		}
		return joinGap(blocks, max(rowGap, 0))
	}
	nats := make([]int, n)
	for i, it := range items {
		nats[i] = it.nat
	}
	widths := allocate(nats, avail)
	blocks := make([]string, n)
	for i, it := range items {
		if widths[i] >= it.nat {
			blocks[i] = it.out
		} else {
			blocks[i], _ = c.Node(it.id, widths[i])
		}
	}
	return hjoin(blocks, widths, gap, valign)
}

// allocate shares avail columns: items whose natural width fits their fair
// share keep it; the rest split what's left.
func allocate(nats []int, avail int) []int {
	n := len(nats)
	w := make([]int, n)
	total := 0
	for _, x := range nats {
		total += x
	}
	if total <= avail {
		copy(w, nats)
		return w
	}
	fixed := make([]bool, n)
	rem, left := avail, n
	for changed := true; changed && left > 0; {
		changed = false
		share := rem / left
		for i := range nats {
			if !fixed[i] && nats[i] <= share {
				w[i], fixed[i] = nats[i], true
				rem -= nats[i]
				left--
				changed = true
			}
		}
	}
	if left > 0 {
		share, extra := rem/left, rem%left
		for i := range nats {
			if !fixed[i] {
				w[i] = share
				if extra > 0 {
					w[i]++
					extra--
				}
			}
		}
	}
	return w
}

func renderGrid(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	const gap = 2
	cols := clampInt(int(num(p["columns"], 2)), 1, 6)
	minW := max(6, int(num(p["minWidth"], 150)/10)) // ~10px per cell
	if fit := (width + gap) / (minW + gap); fit < cols {
		cols = max(1, fit)
	}
	cellW := (width - gap*(cols-1)) / cols
	if cellW < 1 {
		cols, cellW = 1, width
	}
	kids := c.Children(el, cellW)
	var rows []string
	for i := 0; i < len(kids); i += cols {
		end := min(i+cols, len(kids))
		widths := make([]int, end-i)
		for j := range widths {
			widths[j] = cellW
		}
		rows = append(rows, hjoin(kids[i:end], widths, gap, "start"))
	}
	return joinGap(rows, 1)
}

// renderTabs: with Options.Tabs[id] set it draws a tab bar and the selected
// tab; otherwise (a terminal can't click) every tab is shown stacked under a
// "▸ Label ───" header.
func renderTabs(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	var labels []string
	for _, t := range list(p["tabs"]) {
		labels = append(labels, str(t))
	}
	ids := c.visibleKids(el)
	label := func(i int) string {
		if i < len(labels) && labels[i] != "" {
			return labels[i]
		}
		return "Tab " + itoa(i+1)
	}
	n := max(len(labels), len(ids))
	if sel, ok := c.Opts.Tabs[id]; ok {
		sel = clampInt(sel, 0, max(0, n-1))
		var bar strings.Builder
		for i := 0; i < n; i++ {
			if i > 0 {
				bar.WriteString(" ")
			}
			if i == sel {
				bar.WriteString(c.Paint(Style{FG: cBright, Bold: true, Underline: true}, "["+label(i)+"]"))
			} else {
				bar.WriteString(c.Paint(sMuted, " "+label(i)+" "))
			}
		}
		out := []string{trunc(bar.String(), width), c.Paint(sLine, rep("─", width))}
		if sel < len(ids) {
			if s, _ := c.Node(ids[sel], width); s != "" {
				out = append(out, s)
			}
		}
		return strings.Join(out, "\n")
	}
	var secs []string
	for i, k := range ids {
		l := trunc(label(i), max(1, width-4))
		head := c.Paint(sFocus, "▸ "+l) + " " + c.Paint(sLine, rep("─", width-sw(l)-3))
		s, _ := c.Node(k, width)
		if s != "" {
			head += "\n" + s
		}
		secs = append(secs, head)
	}
	return joinGap(secs, 1)
}

func renderHeading(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	text := str(p["text"])
	switch clampInt(int(num(p["level"], 2)), 1, 3) {
	case 1:
		return strings.Join(c.paintWrapped(Style{FG: cBright, Bold: true}, text, width), "\n")
	case 2:
		return strings.Join(c.paintWrapped(sTitle, text, width), "\n")
	}
	ls := wrapPlain(text, max(1, width-2))
	for i, l := range ls {
		pre := "  "
		if i == 0 {
			pre = c.Paint(sFaint, "# ")
		}
		ls[i] = pre + c.Paint(Style{FG: cMid2}, l)
	}
	return strings.Join(ls, "\n")
}

func renderBadge(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	return c.badge(str(p["text"]), str(p["tone"]), width)
}

func (c *Ctx) badge(text, tone string, width int) string {
	return c.fg(toneColor(tone, cMid2), trunc("["+text+"]", width))
}

func renderDivider(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	return c.Paint(sLine, rep("─", width))
}

func renderButtons(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	return c.row(c.visibleKids(el), width, 2, "start", true, 0)
}

// renderUnknown shows an unknown component as a dim box with its type and
// props (compact JSON), then its children.
func renderUnknown(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	inner := innerWidth(width)
	var body []string
	if len(p) > 0 {
		b, _ := json.Marshal(p)
		body = append(body, c.paintWrapped(sFaint, clean(string(b)), inner)...)
	}
	if kids := joinGap(c.Children(el, inner), 1); kids != "" {
		body = append(body, kids)
	}
	return c.box("? "+clean(el.Type), Style{FG: cFaint, Bold: true}, "unknown", sFaint, body, width)
}

func itoa(i int) string { return fmtJSNum(float64(i)) }
