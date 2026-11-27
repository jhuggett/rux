package uirender

import (
	"sort"
	"strings"
)

type gnode struct {
	id, label, sub, status string
	col                    int
	hasCol                 bool
}

type gedge struct {
	from, to, label string
	live, dashed    bool
}

// graph connector directions
const (
	dUp uint8 = 1 << iota
	dDown
	dLeft
	dRight
)

var boxChars = map[uint8]rune{
	dLeft: '─', dRight: '─', dLeft | dRight: '─',
	dUp: '│', dDown: '│', dUp | dDown: '│',
	dDown | dRight: '╭', dDown | dLeft: '╮', dUp | dRight: '╰', dUp | dLeft: '╯',
	dUp | dDown | dRight: '├', dUp | dDown | dLeft: '┤',
	dLeft | dRight | dDown: '┬', dLeft | dRight | dUp: '┴',
	dUp | dDown | dLeft | dRight: '┼',
}

// graphColumns assigns columns like web/ui.js: explicit "col", else the
// longest path from a node with no incoming edges.
func graphColumns(nodes []gnode, edges []gedge) map[string]int {
	col := map[string]int{}
	byID := map[string]*gnode{}
	for i := range nodes {
		byID[nodes[i].id] = &nodes[i]
		if nodes[i].hasCol {
			col[nodes[i].id] = nodes[i].col
		}
	}
	hasIn := map[string]bool{}
	for _, e := range edges {
		hasIn[e.to] = true
	}
	for pass := 0; pass < len(nodes); pass++ {
		changed := false
		for _, n := range nodes {
			if _, ok := col[n.id]; !ok && !hasIn[n.id] {
				col[n.id] = 0
				changed = true
			}
		}
		for _, e := range edges {
			f, ok := col[e.from]
			t := byID[e.to]
			if !ok || t == nil || t.hasCol {
				continue
			}
			cur, has := col[e.to]
			if (!has || cur < f+1) && f+1 < len(nodes) {
				col[e.to] = f + 1
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	for _, n := range nodes {
		if _, ok := col[n.id]; !ok {
			col[n.id] = 0
		}
	}
	return col
}

// renderGraph lays nodes out as boxes in columns and draws edges between
// adjacent columns with box-drawing connectors ending in ▶. Edges it can't
// draw (backwards, same column, skipping columns) and labelled edges are also
// listed below as "a ──▶ b  label". When columns would be narrower than 12
// cells it falls back to a plain list.
func renderGraph(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	var nodes []gnode
	idx := map[string]int{}
	for _, x := range list(p["nodes"]) {
		m := obj(x)
		if m == nil || m["id"] == nil {
			continue
		}
		n := gnode{id: str(m["id"]), label: str(firstOf(m, "label", "id")), sub: str(m["sub"]), status: str(m["status"])}
		if m["col"] != nil {
			n.col, n.hasCol = max(0, int(num(m["col"], 0))), true
			n.col = min(n.col, 50)
		}
		if _, dup := idx[n.id]; dup {
			continue
		}
		idx[n.id] = len(nodes)
		nodes = append(nodes, n)
	}
	var edges []gedge
	for _, x := range list(p["edges"]) {
		m := obj(x)
		if m == nil || m["from"] == nil || m["to"] == nil {
			continue
		}
		edges = append(edges, gedge{from: str(m["from"]), to: str(m["to"]), label: str(m["label"]), live: truthy(m["live"]), dashed: truthy(m["dashed"])})
	}
	if len(nodes) == 0 {
		return c.Paint(sFaint, trunc("no nodes", width))
	}
	colOf := graphColumns(nodes, edges)
	ncols := 0
	for _, v := range colOf {
		ncols = max(ncols, v+1)
	}
	// drop empty columns (explicit cols may skip numbers)
	used := map[int]bool{}
	for _, v := range colOf {
		used[v] = true
	}
	var order []int
	for k := range used {
		order = append(order, k)
	}
	sort.Ints(order)
	remap := map[int]int{}
	for i, k := range order {
		remap[k] = i
	}
	for k, v := range colOf {
		colOf[k] = remap[v]
	}
	ncols = len(order)

	const gapW = 6
	colW := width
	if ncols > 1 {
		colW = (width - gapW*(ncols-1)) / ncols
	}
	colW = min(colW, 32)
	if colW < 12 && !(ncols == 1 && colW >= 6) {
		return c.graphList(nodes, edges, colOf, width)
	}

	// place nodes
	type placed struct {
		y, h int
		n    *gnode
	}
	cols := make([][]placed, ncols)
	pos := map[string]placed{}
	heights := make([]int, ncols)
	for i := range nodes {
		n := &nodes[i]
		k := colOf[n.id]
		h := 3
		if n.sub != "" {
			h = 4
		}
		y := heights[k]
		if y > 0 {
			y++ // gap
		}
		cols[k] = append(cols[k], placed{y, h, n})
		heights[k] = y + h
	}
	H := 0
	for _, h := range heights {
		H = max(H, h)
	}
	for k := range cols {
		off := (H - heights[k]) / 2
		for j := range cols[k] {
			cols[k][j].y += off
			pos[cols[k][j].n.id] = cols[k][j]
		}
	}
	// column blocks
	blocks := make([][]string, ncols)
	for k := range cols {
		blocks[k] = make([]string, H)
		for r := range blocks[k] {
			blocks[k][r] = rep(" ", colW)
		}
		for _, pl := range cols[k] {
			for j, l := range c.graphNode(pl.n, colW) {
				if pl.y+j < H {
					blocks[k][pl.y+j] = l
				}
			}
		}
	}
	// connectors
	type gcell struct {
		dirs  uint8
		arrow bool
		color int
	}
	gaps := make([][][]gcell, max(0, ncols-1))
	for g := range gaps {
		gaps[g] = make([][]gcell, H)
		for r := range gaps[g] {
			gaps[g][r] = make([]gcell, gapW)
		}
	}
	var listed []gedge
	channel := map[[2]int]int{} // (gap, source y) → channel x
	nextCh := make([]int, len(gaps))
	for _, e := range edges {
		a, okA := pos[e.from]
		b, okB := pos[e.to]
		if !okA || !okB {
			listed = append(listed, e)
			continue
		}
		ca, cb := colOf[e.from], colOf[e.to]
		if cb != ca+1 {
			listed = append(listed, e)
			continue
		}
		if e.label != "" {
			listed = append(listed, e)
		}
		gp := gaps[ca]
		y1, y2 := a.y+1, b.y+1
		key := [2]int{ca, y1}
		ch, ok := channel[key]
		if !ok {
			ch = 1 + nextCh[ca]%(gapW-3)
			nextCh[ca]++
			channel[key] = ch
		}
		color := cMid2
		if e.live {
			color = cBright
		} else if e.dashed {
			color = cFaint
		}
		mark := func(x, y int, d uint8) {
			gp[y][x].dirs |= d
			gp[y][x].color = color
		}
		if y1 == y2 {
			ch = gapW - 2
		}
		mark(0, y1, dLeft)
		for x := 0; x < ch; x++ {
			mark(x, y1, dRight)
			mark(x+1, y1, dLeft)
		}
		if y1 != y2 {
			step, du, dd := 1, dDown, dUp
			if y2 < y1 {
				step, du, dd = -1, dUp, dDown
			}
			for y := y1; y != y2; y += step {
				mark(ch, y, du)
				mark(ch, y+step, dd)
			}
		}
		for x := ch; x < gapW-2; x++ {
			mark(x, y2, dRight)
			mark(x+1, y2, dLeft)
		}
		mark(gapW-2, y2, dRight)
		gp[y2][gapW-1].arrow = true
		gp[y2][gapW-1].color = color
	}
	var out []string
	for r := 0; r < H; r++ {
		var b strings.Builder
		for k := 0; k < ncols; k++ {
			if k > 0 {
				row := newCells(gapW)
				for x, gc := range gaps[k-1][r] {
					switch {
					case gc.arrow:
						row[x] = cell{'▶', Style{FG: gc.color}}
					case gc.dirs != 0:
						row[x] = cell{boxChars[gc.dirs], Style{FG: gc.color}}
					}
				}
				b.WriteString(c.cellRow(row))
			}
			b.WriteString(blocks[k][r])
		}
		out = append(out, b.String())
	}
	out = append(out, c.edgeList(listed, nodes, idx, width)...)
	return strings.Join(out, "\n")
}

func (c *Ctx) graphNode(n *gnode, w int) []string {
	glyph, gc := statusGlyph(n.status)
	border := Style{FG: cDim}
	switch n.status {
	case "running":
		border.FG = cMid
	case "failed":
		border.FG = cErr
	case "waiting", "starting":
		border.FG = cWarn
	}
	inner := w - 4
	label := n.label
	head := ""
	if glyph != "" {
		head = c.fg(gc, glyph) + " "
		inner -= 2
	}
	head += c.Paint(sText, pad(label, max(0, inner)))
	side := c.Paint(border, "│")
	lines := []string{
		c.Paint(border, "╭"+rep("─", w-2)+"╮"),
		side + " " + head + " " + side,
	}
	if n.sub != "" {
		lines = append(lines, side+" "+c.Paint(sMuted, pad(n.sub, w-4))+" "+side)
	}
	lines = append(lines, c.Paint(border, "╰"+rep("─", w-2)+"╯"))
	return lines
}

func (c *Ctx) edgeList(edges []gedge, nodes []gnode, idx map[string]int, width int) []string {
	var out []string
	name := func(id string) string {
		if i, ok := idx[id]; ok {
			return nodes[i].label
		}
		return id
	}
	for _, e := range edges {
		arrow := " ──▶ "
		if e.dashed {
			arrow = " ╌╌▶ "
		}
		s := c.Paint(sText, name(e.from)) + c.fg(cMid2, arrow) + c.Paint(sText, name(e.to))
		if e.label != "" {
			s += "  " + c.Paint(sMuted, e.label)
		}
		out = append(out, trunc(s, width))
	}
	return out
}

// graphList is the narrow fallback: nodes by column, then every edge.
func (c *Ctx) graphList(nodes []gnode, edges []gedge, colOf map[string]int, width int) string {
	var out []string
	sorted := make([]gnode, len(nodes))
	copy(sorted, nodes)
	sort.SliceStable(sorted, func(i, j int) bool { return colOf[sorted[i].id] < colOf[sorted[j].id] })
	for _, n := range sorted {
		glyph, gc := statusGlyph(n.status)
		if glyph == "" {
			glyph, gc = "□", cDim
		}
		s := c.fg(gc, glyph) + " " + c.Paint(sText, n.label)
		if n.sub != "" {
			s += " " + c.Paint(sMuted, n.sub)
		}
		out = append(out, trunc(s, width))
	}
	idx := map[string]int{}
	for i, n := range nodes {
		idx[n.id] = i
	}
	out = append(out, c.edgeList(edges, nodes, idx, width)...)
	return strings.Join(out, "\n")
}
