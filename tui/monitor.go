package tui

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Monitor mode: the PWA's overview (usage, threads, host, guests, devices,
// 24h activity) plus per-thread activity lanes and live sparklines.

type monitor struct {
	ov      *Overview
	load    []float64 // host load history (one per snapshot)
	rate    []float64 // tool calls per snapshot interval
	lastTot int
	sel     int
	rows    []string // thread ids in lane order
	cancel  context.CancelFunc
}

type lanesMsg map[string][]int64

func newMonitor() *monitor { return &monitor{} }

func (mo *monitor) start(m *model) tea.Cmd {
	ctx, cancel := context.WithCancel(m.ctx)
	mo.cancel = cancel
	src := m.src
	go src.OverviewStream(ctx, func(ov Overview) { m.post(ovMsg(ov)) })
	return nil
}

func (mo *monitor) stop() {
	if mo.cancel != nil {
		mo.cancel()
	}
}

func (mo *monitor) update(ov Overview) {
	mo.ov = &ov
	l := 0.0
	if len(ov.Host.Load) > 0 {
		l = ov.Host.Load[0]
	}
	mo.load = appendCap(mo.load, l, 120)
	if mo.lastTot > 0 || len(mo.rate) > 0 {
		mo.rate = appendCap(mo.rate, float64(max(ov.Usage.TodayTools-mo.lastTot, 0)), 120)
	} else {
		mo.rate = appendCap(mo.rate, 0, 120)
	}
	mo.lastTot = ov.Usage.TodayTools
}

func appendCap(s []float64, v float64, n int) []float64 {
	s = append(s, v)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}

func (m *model) monitorKey(k tea.KeyMsg) tea.Cmd {
	mo := m.mon
	switch k.String() {
	case "esc":
		return m.toggleMonitor()
	case "up", "k":
		mo.sel = max(mo.sel-1, 0)
	case "down", "j":
		mo.sel = min(mo.sel+1, max(len(mo.rows)-1, 0))
	case "enter":
		if mo.sel < len(mo.rows) {
			id := mo.rows[mo.sel]
			t, _ := m.thread(id)
			mo.stop()
			m.selID[1], m.selID[2] = t.Project, id
			kind := "thread"
			if t.Kind == "manager" {
				kind = "manager"
				m.level = 1
			} else {
				m.level = 2
			}
			m.prevMode = modeNav
			return m.openThread(kind, id, t.Project)
		}
	}
	return nil
}

func (mo *monitor) view(m *model, c *canvas) {
	top, bot := 3, m.h-4
	W := m.w - 2
	x0 := 1
	if mo.ov == nil {
		c.blit(x0+2, top+1, sGreen.Render("connecting to the overview stream…"), 0)
		return
	}
	ov := mo.ov
	y := top

	// ---- tiles ----
	type tile struct {
		k, v, sub string
		meter     float64
		tone      lipgloss.Style
	}
	var tiles []tile
	for _, w := range ov.Usage.Windows {
		tiles = append(tiles, tile{k: winName(w.Name), v: fmt.Sprintf("%d%%", int(math.Round(w.Utilization*100))), sub: "resets in " + until(w.ResetsAt), meter: w.Utilization, tone: sBright})
	}
	if len(ov.Usage.Windows) == 0 {
		tiles = append(tiles, tile{k: "subscription", v: "—", sub: "shows after the next turn", meter: -1, tone: sText})
	}
	st := ov.Statuses
	tiles = append(tiles,
		tile{k: "today", v: fmt.Sprintf("%d turns", ov.Usage.TodayTurns), sub: fmt.Sprintf("%d tools · %d new · ~$%.2f", ov.Usage.TodayTools, ov.Usage.TodayThreads, ov.Usage.TodayCost), meter: -1, tone: sWhite},
		tile{k: "threads", v: fmt.Sprintf("%d working", st["running"]), sub: fmt.Sprintf("%d ready · %d starting · %d failed", st["idle"], st["provisioning"]+st["starting"], st["failed"]), meter: -1, tone: cond2(st["running"] > 0, sBright, sText)},
	)
	var gMem int64
	for _, g := range append(ov.Guests, ov.Other...) {
		gMem += g.MemBytes
	}
	load := 0.0
	if len(ov.Host.Load) > 0 {
		load = ov.Host.Load[0]
	}
	tiles = append(tiles, tile{k: "host · " + ov.Host.Name, v: fmt.Sprintf("load %.1f", load), sub: fmt.Sprintf("%d guests · %s / %s", len(ov.Guests), fmtBytes(gMem), fmtBytes(ov.Host.MemBytes)),
		meter: float64(gMem) / math.Max(float64(ov.Host.MemBytes), 1), tone: sWhite})
	if maxT := max(W/26, 2); len(tiles) > maxT {
		// narrow: keep the first window, threads, today and host first
		k := len(tiles)
		pri := []tile{tiles[0], tiles[k-2], tiles[k-3], tiles[k-1]}
		pri = append(pri, tiles[1:k-3]...)
		tiles = pri[:maxT]
	}
	n := len(tiles)
	tw := (W - (n - 1)) / n
	for i, t := range tiles {
		lines := []string{" " + sFaint.Render(clip(t.k, tw-4)), " " + t.tone.Render(clip(t.v, tw-4))}
		if t.meter >= 0 {
			lines = append(lines, " "+meterBar(t.meter*m.grow.pos, tw-4))
		} else {
			lines = append(lines, "")
		}
		lines = append(lines, " "+sDim.Render(clip(t.sub, tw-4)))
		c.blit(x0+i*(tw+1), y, box(tw, 6, sDim, "", lines), 0)
	}
	y += 7

	// ---- activity (24h bars) | live sparklines ----
	ch := 9
	lw := W * 58 / 100
	if W < 120 {
		lw = W
	}
	g := m.grow.pos
	if bot-y >= 18 {
		hourly := ov.Usage.Hourly
		total := 0
		for _, v := range hourly {
			total += v
		}
		bars := barChart(hourly, lw-4, ch-3, g)
		axis := sFaint.Render(padRight("-24h", (lw-4)/2) + padRight("-12h", (lw-4)/2-3) + "now")
		c.blit(x0, y, box(lw, ch, sDim, sWhite.Render("activity · 24h")+sFaint.Render(fmt.Sprintf(" · %d tool calls", total)), append(prefixAll(bars, " "), " "+axis)), 0)
		rw := W - lw - 1
		spark := func(label string, vals []float64, unit string) []string {
			cur := 0.0
			if len(vals) > 0 {
				cur = vals[len(vals)-1]
			}
			lbl := sFaint.Render(padRight(label, 12)) + sPale.Render(padRight(fmt.Sprintf("%.1f%s", cur, unit), 8))
			br := braille(vals, rw-24, 2, g)
			return []string{lbl + br[0], strings.Repeat(" ", 20) + br[1]}
		}
		hf := make([]float64, len(hourly))
		for i, v := range hourly {
			hf[i] = float64(v)
		}
		live := append(spark("tools · 24h", hf, ""), spark("host load", mo.load, "")...)
		live = append(live, "")
		live = append(live, strings.Split(statusBar(st, rw-4), "\n")...)
		if W >= 120 {
			c.blit(x0+lw+1, y, box(rw, ch, sDim, sWhite.Render("live")+sFaint.Render(" · "+time.UnixMilli(ov.At).Format("15:04:05")), prefixAll(live, " ")), 0)
		}
		y += ch + 1
	}

	// ---- thread lanes with guest stats ----
	guests := map[string]OvGuest{}
	for _, g := range ov.Guests {
		if g.ThreadID != "" {
			guests[g.ThreadID] = g
		}
	}
	var ts []Thread
	for _, t := range m.threads {
		if !t.Archived && t.Status != "destroyed" {
			ts = append(ts, t)
		}
	}
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].Updated.After(ts[j].Updated) })
	mo.rows = mo.rows[:0]
	laneH := bot - y
	if laneH < 4 {
		return
	}
	nameW := 26
	boxW := W
	if W >= 200 {
		boxW = W - 59
		mo.guestPanel(m, c, x0+boxW+1, y, 58, laneH)
	}
	statCol := boxW >= 110
	laneW := boxW - nameW - 36
	if !statCol {
		laneW = boxW - nameW - 8
		nameW = min(nameW, boxW/3)
		laneW = boxW - nameW - 8
	}
	span := 2 * time.Hour
	now := time.Now()
	var lines []string
	head := sFaint.Render(padRight("thread", nameW+2)) + sFaint.Render(padRight("-2h", laneW/2)+padRight("-1h", laneW-laneW/2-3)+"now") + cond(statCol, "  "+sFaint.Render("guest cpu · mem"), "")
	lines = append(lines, head)
	for i, t := range ts {
		mo.rows = append(mo.rows, t.ID)
		if len(lines) >= laneH-2 {
			continue
		}
		gl, gs := statusGlyph(t.Status, t.Offline(), m.frame)
		name := t.Title
		if t.Kind == "manager" {
			name = "⌘ " + name
		}
		nst := sText
		if i == mo.sel {
			nst = sRev
		}
		lane := laneCells(m.activity[t.ID], now.Add(-span), now, laneW, t.Status == "running", m.frame, g)
		stat := sFaint.Render("no guest")
		if gg, ok := guests[t.ID]; ok {
			stat = miniMeter(gg.CPU/100, 8) + sFaint.Render(fmt.Sprintf(" %3.0f%% ", gg.CPU)) + miniMeter(float64(gg.MemBytes)/math.Max(float64(gg.MemLimit), 1), 6) + sFaint.Render(" "+fmtBytes(gg.MemBytes))
			if gg.Streaming {
				stat += sBright.Render(" ≋")
			}
		}
		badge := ""
		if m.unseen(t) {
			badge = sWarn.Render("◆")
		}
		lines = append(lines, gs.Render(gl)+" "+nst.Render(padRight(clip(name, nameW-1), nameW-1))+cond(badge != "", badge, " ")+" "+lane+cond(statCol, "  "+stat, ""))
	}
	if len(ts) > laneH-3 {
		lines = append(lines, sFaint.Render(fmt.Sprintf("  … %d more", len(ts)-(laneH-3))))
	}
	// topology line
	topo := topology(ov)
	c.blit(x0, y, box(boxW, laneH, sDim, sWhite.Render("threads · activity lanes")+sFaint.Render(" · enter opens"), append(prefixAll(lines, " "), "", " "+topo)), 0)
}

func cond2(b bool, t, f lipgloss.Style) lipgloss.Style {
	if b {
		return t
	}
	return f
}

func winName(n string) string {
	switch n {
	case "five_hour":
		return "5-hour window"
	case "seven_day":
		return "7-day window"
	case "seven_day_opus":
		return "7-day opus"
	case "seven_day_sonnet":
		return "7-day sonnet"
	}
	return strings.ReplaceAll(n, "_", " ")
}

func until(sec int64) string {
	s := sec - time.Now().Unix()
	if s <= 0 {
		return "now"
	}
	d, h, m := s/86400, s%86400/3600, s%3600/60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh", d, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

func meterBar(frac float64, w int) string {
	frac = math.Min(math.Max(frac, 0), 1)
	n := int(math.Round(frac * float64(w)))
	st := sBright
	if frac > 0.8 {
		st = sWarn
	}
	return st.Render(strings.Repeat("█", n)) + sDeep.Render(strings.Repeat("░", w-n))
}

func miniMeter(frac float64, w int) string {
	frac = math.Min(math.Max(frac, 0), 1)
	full := frac * float64(w)
	var b strings.Builder
	parts := []rune(" ▏▎▍▌▋▊▉█")
	for i := 0; i < w; i++ {
		v := full - float64(i)
		switch {
		case v >= 1:
			b.WriteRune('█')
		case v > 0:
			b.WriteRune(parts[int(v*8)])
		default:
			b.WriteRune('·')
		}
	}
	st := sGreen
	if frac > 0.8 {
		st = sWarn
	}
	return st.Render(b.String())
}

// barChart draws vertical bars (one per value, spread over w) h rows tall.
func barChart(vals []int, w, h int, scale float64) []string {
	if len(vals) == 0 || w <= 0 || h <= 0 {
		return nil
	}
	mx := 1
	for _, v := range vals {
		mx = max(mx, v)
	}
	bw := max(w/len(vals), 1)
	blocks := []rune(" ▁▂▃▄▅▆▇█")
	rows := make([]string, h)
	for r := 0; r < h; r++ {
		var b strings.Builder
		for i, v := range vals {
			level := float64(v) / float64(mx) * float64(h*8) * scale
			cellLevel := level - float64((h-1-r)*8)
			ch := ' '
			switch {
			case cellLevel >= 8:
				ch = '█'
			case cellLevel > 0:
				ch = blocks[int(cellLevel)]
			}
			st := sGreen
			if i == len(vals)-1 {
				st = sBright
			} else if r < h/3 {
				st = sPale
			}
			seg := strings.Repeat(string(ch), max(bw-1, 1))
			b.WriteString(st.Render(seg))
			if bw > 1 {
				b.WriteByte(' ')
			}
		}
		rows[r] = b.String()
	}
	return rows
}

// braille draws an area chart of vals in w cells x h rows (2x4 dots each).
func braille(vals []float64, w, h int, scale float64) []string {
	out := make([]string, h)
	if w <= 0 {
		return out
	}
	dotsW, dotsH := w*2, h*4
	// resample to dotsW points (right-aligned: newest at the right edge)
	pts := make([]float64, dotsW)
	for i := range pts {
		pts[i] = math.NaN()
	}
	if n := len(vals); n > 0 && n <= dotsW {
		// stretch short series across the whole width
		for x := range pts {
			pts[x] = vals[min(x*n/dotsW, n-1)]
		}
	} else {
		for i := 0; i < n && i < dotsW; i++ {
			pts[dotsW-1-i] = vals[n-1-i]
		}
	}
	mx := 0.0
	for _, v := range vals {
		mx = math.Max(mx, v)
	}
	if mx == 0 {
		mx = 1
	}
	grid := make([][]bool, dotsH)
	for i := range grid {
		grid[i] = make([]bool, dotsW)
	}
	for x, v := range pts {
		if math.IsNaN(v) {
			continue
		}
		hgt := int(math.Round(v / mx * float64(dotsH-1) * scale))
		for y := 0; y <= hgt; y++ {
			grid[dotsH-1-y][x] = true
		}
	}
	bits := [4][2]rune{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}
	for r := 0; r < h; r++ {
		var b strings.Builder
		for cx := 0; cx < w; cx++ {
			var ch rune = 0x2800
			top := false
			for dy := 0; dy < 4; dy++ {
				for dx := 0; dx < 2; dx++ {
					if grid[r*4+dy][cx*2+dx] {
						ch |= bits[dy][dx]
						if r*4+dy == 0 || !grid[r*4+dy-1][cx*2+dx] {
							top = true
						}
					}
				}
			}
			st := sGreen
			if top {
				st = sBright
			}
			if cx >= w-2 {
				st = sWhite.UnsetBold()
			}
			b.WriteString(st.Render(string(ch)))
		}
		out[r] = b.String()
	}
	return out
}

// laneCells buckets event times into w cells between from and to.
func laneCells(ts []int64, from, to time.Time, w int, live bool, frame int, reveal float64) string {
	if w <= 0 {
		return ""
	}
	counts := make([]int, w)
	span := to.Sub(from).Milliseconds()
	for _, t := range ts {
		i := int((t - from.UnixMilli()) * int64(w) / max(span, 1))
		if i >= 0 && i < w {
			counts[i]++
		}
	}
	var b strings.Builder
	shown := int(math.Round(reveal * float64(w)))
	for i, n := range counts {
		var s string
		if i >= shown {
			b.WriteByte(' ')
			continue
		}
		switch {
		case n == 0:
			s = sDeep.Render("·")
		case n < 2:
			s = sDim.Render("▪")
		case n < 4:
			s = sMid.Render("■")
		case n < 8:
			s = sGreen.Render("█")
		default:
			s = sBright.Render("█")
		}
		if i == w-1 && live {
			s = []string{sBright.Render("▌"), sGreen.Render("▐")}[frame/4%2]
		}
		b.WriteString(s)
	}
	return b.String()
}

func statusBar(st map[string]int, w int) string {
	order := []struct {
		k  string
		s  lipgloss.Style
		ch string
	}{{"running", sBright, "█"}, {"idle", sGreen, "▓"}, {"starting", sWarn, "▒"}, {"provisioning", sWarn, "▒"}, {"failed", sErr, "█"}, {"destroyed", sDeep, "░"}}
	total := 0
	for _, o := range order {
		total += st[o.k]
	}
	if total == 0 {
		return sFaint.Render("no threads")
	}
	var b, legend strings.Builder
	used := 0
	for _, o := range order {
		n := st[o.k] * (w - 2) / total
		if st[o.k] > 0 && n == 0 {
			n = 1
		}
		n = min(n, w-2-used)
		used += n
		b.WriteString(o.s.Render(strings.Repeat(o.ch, n)))
		if st[o.k] > 0 {
			legend.WriteString(o.s.Render(o.ch) + sFaint.Render(fmt.Sprintf(" %s %d  ", o.k, st[o.k])))
		}
	}
	return b.String() + "\n " + legend.String()
}

func topology(ov *Overview) string {
	var devs []string
	for _, d := range ov.Devices {
		if !d.Online {
			continue
		}
		g := sDim.Render("○ ")
		if d.Viewing {
			g = sBright.Render("◉ ")
		}
		devs = append(devs, g+sText.Render(d.Name)+cond(d.Viewing, sFaint.Render(" viewing"), ""))
	}
	stream := 0
	for _, g := range ov.Guests {
		if g.Streaming {
			stream++
		}
	}
	host := sWhite.Render(cmpOr(ov.Host.Name, "host")) + sFaint.Render(fmt.Sprintf(" %d cpu · %s", ov.Host.CPUs, fmtBytes(ov.Host.MemBytes)))
	s := strings.Join(devs, sDim.Render("  "))
	if s != "" {
		s += sDim.Render(" ╌╌▶ ")
	}
	s += host + sGreen.Render(" ━━▶ ") + sText.Render(fmt.Sprintf("%d guests", len(ov.Guests))) + sBright.Render(fmt.Sprintf(" (%d streaming)", stream))
	if len(ov.Other) > 0 {
		var o []string
		for _, g := range ov.Other {
			o = append(o, g.Name+" "+fmtBytes(g.MemBytes))
		}
		s += sFaint.Render("   other: " + strings.Join(o, ", "))
	}
	return s
}

// guestPanel lists guests, devices and other containers (wide terminals).
func (mo *monitor) guestPanel(m *model, c *canvas, x, y, w, h int) {
	ov := mo.ov
	var lines []string
	for _, g := range ov.Guests {
		gl, gs := statusGlyph(g.Status, g.Managed && !g.Connected, m.frame)
		if g.Streaming {
			gl, gs = "≋", sBright
		}
		title := cmpOr(g.ThreadTitle, g.Name)
		lines = append(lines, gs.Render(gl)+" "+sText.Render(clip(title, w-6)))
		lines = append(lines, "  "+sFaint.Render("cpu ")+miniMeter(g.CPU/100*m.grow.pos, 10)+sFaint.Render(fmt.Sprintf(" %3.0f%%  mem ", g.CPU))+
			miniMeter(float64(g.MemBytes)/math.Max(float64(g.MemLimit), 1)*m.grow.pos, 8)+sFaint.Render(" "+fmtBytes(g.MemBytes)))
	}
	if len(ov.Guests) == 0 {
		lines = append(lines, sFaint.Render("no guests running"))
	}
	lines = append(lines, "", sWhite.Render("devices"))
	for _, d := range ov.Devices {
		g := sDim.Render("○ ")
		if d.Viewing {
			g = sBright.Render("◉ ")
		} else if d.Online {
			g = sGreen.Render("● ")
		}
		lines = append(lines, g+sText.Render(d.Name)+sFaint.Render(" · "+strings.ToLower(d.OS)+cond(d.Viewing, " · viewing", "")))
	}
	if len(ov.Other) > 0 {
		lines = append(lines, "", sWhite.Render("other containers"))
		for _, o := range ov.Other {
			lines = append(lines, sFaint.Render(fmt.Sprintf("%s · %s · %d procs", o.Name, fmtBytes(o.MemBytes), o.Procs)))
		}
	}
	c.blit(x, y, box(w, h, sDim, sWhite.Render("guests")+sFaint.Render(fmt.Sprintf(" · %d", len(ov.Guests))), prefixAll(lines, " ")), 0)
}
