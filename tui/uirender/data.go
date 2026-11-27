package uirender

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

// fmtNum is web/ui.js fmtNum: 12.3k, 1.2M, integers as is, else 1-2 decimals.
func fmtNum(n float64) string {
	a := math.Abs(n)
	switch {
	case a >= 1e6:
		return fmtFixed(n/1e6, 1) + "M"
	case a >= 1e4:
		return fmtFixed(n/1e3, 1) + "k"
	case n == math.Trunc(n):
		return fmtJSNum(n)
	case a < 1:
		return fmtFixed(n, 2)
	}
	return fmtFixed(n, 1)
}

func fmtFixed(n float64, d int) string {
	p := math.Pow(10, float64(d))
	r := math.Round(n*p) / p
	s := fmtJSNum(r)
	if d > 0 {
		if i := strings.IndexByte(s, '.'); i < 0 {
			s += "." + rep("0", d)
		} else if k := len(s) - i - 1; k < d {
			s += rep("0", d-k)
		}
	}
	return s
}

func renderStat(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	out := []string{c.Paint(sMuted, trunc(str(p["label"]), width))}
	vst := Style{FG: toneColor(str(p["tone"]), cWhite), Bold: true}
	if vst.FG == cDim {
		vst.FG = cMid2
	}
	out = append(out, c.Paint(vst, trunc(str(p["value"]), width)))
	var sub strings.Builder
	used := 0
	if p["trend"] != nil {
		tr := num(p["trend"], 0)
		t, col := "▲ ", cBright
		if tr < 0 {
			t, col = "▼ ", cErr
		}
		t += fmtNum(math.Abs(tr))
		t = trunc(t, width)
		sub.WriteString(c.fg(col, t))
		used = sw(t)
	}
	if s := str(p["sub"]); s != "" && width-used > 1 {
		if used > 0 {
			sub.WriteString(" ")
			used++
		}
		sub.WriteString(c.Paint(sFaint, trunc(s, width-used)))
	}
	if sub.Len() > 0 {
		out = append(out, sub.String())
	}
	return strings.Join(out, "\n")
}

func renderMeter(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	v := num(p["value"], 0)
	mx := 1.0
	if p["max"] != nil {
		mx = num(p["max"], 1)
	} else if v > 1 {
		mx = 100
	}
	if mx == 0 {
		mx = 1
	}
	frac := math.Max(0, math.Min(1, v/mx))
	pct := fmtJSNum(math.Round(frac*100)) + "%"
	label := trunc(str(p["label"]), max(0, width-sw(pct)-1))
	head := c.Paint(sMuted, label) + rep(" ", width-sw(label)-sw(pct)) + c.Paint(sText, pct)
	if sw(pct) >= width {
		head = trunc(pct, width)
	}
	full := int(math.Round(frac * float64(width)))
	bar := c.fg(cBright, rep("█", full)) + c.fg(cDim2, rep("░", width-full))
	out := []string{head, bar}
	if s := str(p["sub"]); s != "" {
		out = append(out, c.Paint(sFaint, trunc(s, width)))
	}
	return strings.Join(out, "\n")
}

func renderKeyValue(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	type kv struct{ k, v string }
	var items []kv
	kw := 0
	for _, it := range list(p["items"]) {
		m := obj(it)
		if m == nil {
			continue
		}
		e := kv{str(firstOf(m, "key", "k", "label")), str(firstOf(m, "value", "v"))}
		items = append(items, e)
		kw = max(kw, sw(e.k))
	}
	kw = min(kw, max(4, width/3))
	var out []string
	for _, e := range items {
		vw := width - kw - 2
		if vw < 8 {
			out = append(out, c.Paint(sMuted, trunc(e.k, width)))
			for _, l := range wrapPlain(e.v, max(1, width-2)) {
				out = append(out, "  "+c.Paint(sText, l))
			}
			continue
		}
		for i, l := range wrapPlain(e.v, vw) {
			k := ""
			if i == 0 {
				k = e.k
			}
			out = append(out, c.Paint(sMuted, pad(k, kw))+"  "+c.Paint(sText, l))
		}
	}
	return strings.Join(out, "\n")
}

type tcol struct {
	key, label string
	right      bool
	mono       bool
}

func renderTable(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	var cols []tcol
	for _, x := range list(p["columns"]) {
		if m := obj(x); m != nil {
			k := str(m["key"])
			l := str(firstOf(m, "label", "key"))
			cols = append(cols, tcol{key: k, label: l, right: str(m["align"]) == "right", mono: truthy(m["mono"])})
		} else {
			cols = append(cols, tcol{key: str(x), label: str(x)})
		}
	}
	rows := list(p["rows"])
	if len(cols) == 0 && len(rows) > 0 { // infer columns from the first row
		if m := obj(rows[0]); m != nil {
			var ks []string
			for k := range m {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			for _, k := range ks {
				cols = append(cols, tcol{key: k, label: k})
			}
		} else if l := list(rows[0]); l != nil {
			for i := range l {
				cols = append(cols, tcol{key: itoa(i), label: ""})
			}
		}
	}
	if len(cols) == 0 {
		return c.Paint(sFaint, trunc("no columns", width))
	}
	maxRows := int(num(p["maxRows"], 50))
	if maxRows < 1 {
		maxRows = 50
	}
	more := 0
	if len(rows) > maxRows {
		more = len(rows) - maxRows
		rows = rows[:maxRows]
	}
	cells := make([][]string, len(rows))
	ws := make([]int, len(cols))
	for j, col := range cols {
		ws[j] = sw(col.label)
	}
	for i, r := range rows {
		cells[i] = make([]string, len(cols))
		for j, col := range cols {
			var v any
			if l, ok := r.([]any); ok {
				if j < len(l) {
					v = l[j]
				}
			} else if m := obj(r); m != nil {
				v = m[col.key]
			} else if j == 0 {
				v = r
			}
			s := strings.ReplaceAll(str(v), "\n", " ")
			cells[i][j] = s
			ws[j] = max(ws[j], sw(s))
		}
	}
	const gap = 2
	total := gap * (len(cols) - 1)
	for _, w := range ws {
		total += w
	}
	for total > width { // shrink the widest column
		bi := 0
		for j := range ws {
			if ws[j] > ws[bi] {
				bi = j
			}
		}
		if ws[bi] <= 3 {
			break
		}
		ws[bi]--
		total--
	}
	align := func(s string, j int) string {
		if cols[j].right {
			return padLeft(trunc(s, ws[j]), ws[j])
		}
		return pad(s, ws[j])
	}
	var out []string
	var hb strings.Builder
	for j, col := range cols {
		if j > 0 {
			hb.WriteString(rep(" ", gap))
		}
		hb.WriteString(c.Paint(sMuted, align(col.label, j)))
	}
	out = append(out, hb.String(), c.Paint(sLine, rep("─", min(total, width))))
	for _, r := range cells {
		var b strings.Builder
		for j, s := range r {
			if j > 0 {
				b.WriteString(rep(" ", gap))
			}
			st := sText
			if cols[j].mono {
				st = Style{FG: cMid}
			}
			b.WriteString(c.Paint(st, align(s, j)))
		}
		out = append(out, b.String())
	}
	if more > 0 {
		out = append(out, c.Paint(sFaint, trunc("… "+itoa(more)+" more row"+map[bool]string{true: "s"}[more > 1], width)))
	}
	return strings.Join(out, "\n")
}

func renderList(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	var out []string
	for _, it := range list(p["items"]) {
		m := obj(it)
		if m == nil {
			m = map[string]any{"title": it}
		}
		glyph, gc := statusGlyph(str(m["status"]))
		if glyph == "" {
			glyph, gc = "•", cDim
		}
		badge := str(m["badge"])
		bw := 0
		if badge != "" {
			badge = trunc("["+badge+"]", max(3, width/3))
			bw = sw(badge) + 1
		}
		tw := max(1, width-2-bw)
		title := wrapPlain(str(m["title"]), tw)
		href := str(m["href"])
		for i, l := range title {
			pre := "  "
			if i == 0 {
				pre = c.fg(gc, glyph) + " "
			}
			txt := c.Paint(sText, l)
			if href != "" {
				txt = c.link(href, txt)
			}
			line := pre + txt
			if i == 0 && badge != "" {
				line += rep(" ", width-2-sw(l)-sw(badge)) + c.fg(cMid2, badge)
			}
			out = append(out, line)
		}
		if s := str(m["sub"]); s != "" {
			for _, l := range wrapPlain(s, max(1, width-2)) {
				out = append(out, "  "+c.Paint(sMuted, l))
			}
		}
	}
	return strings.Join(out, "\n")
}

// ---- Timeline ----

// toMillis parses a time as ms since the epoch (numbers) or an ISO string.
func toMillis(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case string:
		s := strings.TrimSpace(t)
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
			var tm time.Time
			var err error
			if strings.Contains(layout, "Z07") {
				tm, err = time.Parse(layout, s)
			} else {
				tm, err = time.ParseInLocation(layout, s, time.Local)
			}
			if err == nil {
				return float64(tm.UnixMilli()), true
			}
		}
		if f := num(s, math.NaN()); !math.IsNaN(f) {
			return f, true
		}
	}
	return 0, false
}

func fmtTime(ms, span float64) string {
	t := time.UnixMilli(int64(ms)).In(time.Local)
	switch {
	case span > 2*86400e3:
		return t.Format("Jan 2")
	case span < 600e3:
		return t.Format("04:05")
	}
	return t.Format("15:04")
}

func barColor(status string) int {
	switch status {
	case "running":
		return cBright
	case "failed":
		return cErr
	case "waiting", "starting":
		return cWarn
	case "done", "idle":
		return cDim
	}
	return cMid2
}

func renderTimeline(c *Ctx, id string, el *Element, p map[string]any, width int) string {
	type tItem struct {
		label, lane, status string
		s, e                float64
		hasLane             bool
	}
	now := float64(c.Now.UnixMilli())
	var items []tItem
	for _, x := range list(p["items"]) {
		m := obj(x)
		if m == nil {
			continue
		}
		s, ok := toMillis(m["start"])
		if !ok {
			continue
		}
		e := now
		if m["end"] != nil {
			if v, ok := toMillis(m["end"]); ok {
				e = v
			}
		}
		it := tItem{label: str(m["label"]), status: str(m["status"]), s: s, e: e}
		if m["lane"] != nil {
			it.lane, it.hasLane = str(m["lane"]), true
		} else {
			it.lane = it.label
		}
		items = append(items, it)
	}
	if len(items) == 0 {
		return c.Paint(sFaint, trunc("no items", width))
	}
	from, to := items[0].s, items[0].e
	for _, it := range items {
		from = math.Min(from, it.s)
		to = math.Max(to, it.e)
	}
	if v, ok := toMillis(p["from"]); ok && p["from"] != nil {
		from = v
	}
	if v, ok := toMillis(p["to"]); ok && p["to"] != nil {
		to = v
	}
	if to <= from {
		to = from + 60e3
	}
	span := to - from
	var lanes []string
	seen := map[string]bool{}
	lw := 0
	for _, it := range items {
		if !seen[it.lane] {
			seen[it.lane] = true
			lanes = append(lanes, it.lane)
			lw = max(lw, sw(it.lane))
		}
	}
	lw = min(lw, max(4, width/3), 24)
	tw := width - lw - 1
	if tw < 4 {
		lw, tw = 0, width
	}
	col := func(t float64) int {
		return clampInt(int(math.Floor((t-from)/span*float64(tw))), 0, tw-1)
	}
	var out []string
	for _, lane := range lanes {
		row := newCells(tw)
		for i := range row {
			row[i] = cell{'·', Style{FG: 238}}
		}
		for _, it := range items {
			if it.lane != lane {
				continue
			}
			a := col(it.s)
			b := int(math.Ceil((it.e-from)/span*float64(tw))) - 1
			b = clampInt(b, a, tw-1)
			if it.e < from || it.s > to {
				continue
			}
			bc := barColor(it.status)
			for x := a; x <= b; x++ {
				row[x] = cell{'█', Style{FG: bc}}
			}
			// label inside the bar when lanes group several bars
			if it.hasLane && it.label != it.lane && runewidth.StringWidth(it.label) == len([]rune(it.label)) && b-a+1 >= len([]rune(it.label))+2 {
				for k, r := range []rune(it.label) {
					row[a+1+k] = cell{r, Style{FG: cBlack, BG: bc, Bold: true}}
				}
			}
		}
		if now > from && now < to {
			if x := col(now); row[x].r == '·' {
				row[x] = cell{'│', Style{FG: cBright}}
			}
		}
		line := c.cellRow(row)
		if lw > 0 {
			line = c.Paint(sMuted, pad(lane, lw)) + " " + line
		}
		out = append(out, line)
	}
	// axis: ticks at 0, ¼, ½, ¾, 1
	var pos []int
	var labels []string
	for _, f := range []float64{0, .25, .5, .75, 1} {
		pos = append(pos, int(math.Round(f*float64(tw-1))))
		labels = append(labels, fmtTime(from+span*f, span))
	}
	axis := placeLabels(pos, labels, tw)
	out = append(out, rep(" ", lw+min(lw, 1))+c.Paint(sFaint, axis))
	return strings.Join(out, "\n")
}

// placeLabels writes labels centered at positions on a line of width w. The
// first and last labels are placed first; the others fill in where they
// don't touch an already placed one.
func placeLabels(pos []int, labels []string, w int) string {
	type span struct {
		start int
		text  string
	}
	var placed []span
	try := func(i int) {
		l := labels[i]
		lw := sw(l)
		if lw > w || lw == 0 {
			return
		}
		start := clampInt(pos[i]-lw/2, 0, w-lw)
		for _, p := range placed {
			if start < p.start+sw(p.text)+1 && p.start < start+lw+1 {
				return
			}
		}
		placed = append(placed, span{start, l})
	}
	n := len(labels)
	if n > 0 {
		try(0)
	}
	if n > 1 {
		try(n - 1)
	}
	for i := 1; i < n-1; i++ {
		try(i)
	}
	sort.Slice(placed, func(a, b int) bool { return placed[a].start < placed[b].start })
	var b strings.Builder
	end := 0
	for _, p := range placed {
		b.WriteString(rep(" ", p.start-end))
		b.WriteString(p.text)
		end = p.start + sw(p.text)
	}
	return b.String()
}
