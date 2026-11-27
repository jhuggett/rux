package tui

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The navigation screen is a spatial tree: root (column 0) → projects
// (column 1) → a project's threads (column 2). The current level is always
// drawn in the middle of the screen with the selected node on the center
// row; ancestors fan out to the left and children to the right.

type node struct {
	id, title, sub, meta string
	status               string
	offline, unseen, dim bool
	act                  []int64 // recent event times, for the sparkline
}

func (m *model) column(level int) ([]node, int) {
	switch level {
	case 0:
		working, total := 0, 0
		for _, t := range m.threads {
			if t.Kind == "" && !t.Archived && t.Status != "destroyed" {
				total++
				if t.Status == "running" {
					working++
				}
			}
		}
		return []node{{id: "root", title: "rux", sub: "no global manager · " + m.src.Name(),
			meta: fmt.Sprintf("%d projects · %d threads · %d working", len(m.visibleProjects()), total, working), status: "idle", act: m.actFor(func(Thread) bool { return true })}}, 0
	case 1:
		var out []node
		for _, p := range m.visibleProjects() {
			sub := "github · " + cmpOr(p.Branch, "default branch")
			if p.Repo == "" {
				sub = "blank workspaces"
			} else if !strings.Contains(p.Repo, "github.com") {
				sub = shortRepo(p.Repo)
			}
			if n := strings.TrimSpace(strings.SplitN(p.Notes, "\n", 2)[0]); n != "" {
				sub = n
			}
			mgr := "no manager yet"
			if p.Manager != "" {
				mgr = "manager " + statusText(Thread{Status: p.ManagerStatus, Connected: p.ManagerStatus != "offline"})
			}
			meta := fmt.Sprintf("%s · %d threads", mgr, p.Threads)
			if p.Active > 0 {
				meta += fmt.Sprintf(" · %d active", p.Active)
			}
			st := "idle"
			switch {
			case p.Active > 0:
				st = "running"
			case p.Threads == 0 && p.Manager == "":
				st = ""
			}
			out = append(out, node{id: p.ID, title: p.Name, sub: sub, meta: meta, status: st, unseen: m.projectUnseen(p), dim: p.Archived, act: m.actFor(func(t Thread) bool { return t.Project == p.ID })})
		}
		return out, max(idx(m.visibleProjects(), m.selID[1]), 0)
	case 2:
		ts := m.projectThreads(m.selID[1])
		var out []node
		for _, t := range ts {
			sub := strings.Trim(strings.Join([]string{t.Branch, shortModel(t.Model)}, " · "), " ·")
			if sub == "" {
				sub = "default branch"
			}
			out = append(out, node{id: t.ID, title: t.Title, sub: sub, meta: statusText(t) + " · " + ago(t.Updated),
				status: t.Status, offline: t.Offline(), unseen: m.unseen(t), dim: t.Archived || t.Status == "destroyed", act: m.actFor(func(x Thread) bool { return x.ID == t.ID })})
		}
		return out, max(idx(ts, m.selID[2]), 0)
	}
	return nil, 0
}

func (m *model) navKey(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "left", "h", "backspace", "esc":
		if m.level > 0 {
			m.level--
			m.navX = springVal{pos: m.navX.pos - float64(m.colW()+6), vel: m.navX.vel}
		}
	case "right", "l":
		if nodes, _ := m.column(m.level + 1); m.level < 2 && len(nodes) > 0 {
			m.level++
			m.navX = springVal{pos: m.navX.pos + float64(m.colW()+6), vel: m.navX.vel}
		}
	case "up", "k":
		m.moveSel(-1)
	case "down", "j":
		m.moveSel(1)
	case "g", "home":
		m.moveSel(-1 << 20)
	case "G", "end":
		m.moveSel(1 << 20)
	case "enter":
		return m.openSelected()
	case "a":
		m.showArchived = !m.showArchived
		m.fixSelection()
		m.flash(map[bool]string{true: "showing archived and removed", false: "hiding archived and removed"}[m.showArchived], false)
	case "n":
		if m.level == 0 {
			m.flash("pick a project first (→)", true)
			return nil
		}
		pid := m.selID[1]
		p, _ := m.project(pid)
		m.prompt = &prompt{title: "new thread · " + p.Name, hint: "What should the worker do? (its first message)", onDone: func(m *model, text string) tea.Cmd {
			if text == "" {
				return nil
			}
			src, ctx := m.src, m.ctx
			return func() tea.Msg {
				t, err := src.Create(ctx, pid, text)
				if err != nil {
					return toastMsg{err.Error(), true}
				}
				return createdMsg{t}
			}
		}}
	case "x", "z":
		if m.level != 2 {
			return nil
		}
		t, ok := m.thread(m.selID[2])
		if !ok {
			return nil
		}
		verb, ok2 := "remove the guest of", "guest removing"
		if k.String() == "z" {
			verb, ok2 = "archive", "archived"
			if t.Archived {
				verb, ok2 = "unarchive", "unarchived"
			}
		}
		key := k.String()
		m.prompt = &prompt{title: "confirm", hint: fmt.Sprintf("%s %q?", verb, clip(t.Title, 40)), confirm: true, onDone: func(m *model, _ string) tea.Cmd {
			return tea.Sequence(m.do(ok2, func(ctx context.Context) error {
				if key == "x" {
					return m.src.Destroy(ctx, t.ID)
				}
				return m.src.Archive(ctx, t.ID, !t.Archived)
			}), m.fetch())
		}}
	}
	return m.wantPreview()
}

func (m *model) moveSel(d int) {
	nodes, sel := m.column(m.level)
	if len(nodes) == 0 || m.level == 0 {
		return
	}
	n := min(max(sel+d, 0), len(nodes)-1)
	if n == sel {
		return
	}
	m.selID[m.level] = nodes[n].id
	if m.level == 1 {
		ts := m.projectThreads(m.selID[1])
		m.selID[2] = ""
		if len(ts) > 0 {
			m.selID[2] = ts[0].ID
		}
	}
	m.navY = springVal{pos: m.navY.pos + float64((n-sel)*cardPitch), vel: m.navY.vel}
}

func (m *model) openSelected() tea.Cmd {
	switch m.level {
	case 0:
		return m.openThread("root", "", "")
	case 1:
		p, ok := m.project(m.selID[1])
		if !ok {
			return nil
		}
		return m.openThread("manager", p.Manager, p.ID)
	case 2:
		return m.openThread("thread", m.selID[2], m.selID[1])
	}
	return nil
}

// ---- preview of the selected thread (right side at the leaf level) ----

type preview struct {
	events  []Event
	at      time.Time
	loading bool
}

type previewMsg struct {
	id     string
	events []Event
}

func (m *model) wantPreview() tea.Cmd {
	if m.mode != modeNav || m.level == 0 || m.selID[2] == "" {
		return nil
	}
	id := m.selID[2]
	p := m.previews[id]
	if p != nil && (p.loading || time.Since(p.at) < 4*time.Second) {
		return nil
	}
	if p == nil {
		p = &preview{}
		m.previews[id] = p
	}
	p.loading = true
	src, ctx := m.src, m.ctx
	return func() tea.Msg {
		pg, _ := src.Page(ctx, id, 0, 80)
		return previewMsg{id, pg.Events}
	}
}

// ---- drawing ----

func (m *model) bodyBounds() (top, bot, mid int) {
	top, bot = 3, m.h-5
	return top, bot, (top + bot) / 2
}

func (m *model) viewNav(c *canvas) {
	top, bot, mid := m.bodyBounds()
	if bot-top < 6 || m.w < 40 {
		c.blit(1, top, sWarn.Render("window too small"), 0)
		return
	}
	if !m.loaded {
		msg := "connecting to " + m.src.Name() + "…"
		if m.lastErr != "" {
			msg = "can't reach " + m.src.Name() + ": " + m.lastErr
		}
		c.blit(max((m.w-lipgloss.Width(msg))/2, 0), mid, sGreen.Render(msg), 0)
		return
	}
	dx := int(math.Round(m.navX.pos))
	cw := m.colW()
	cx := (m.w-cw)/2 + dx
	g := 6

	// center column
	nodes, sel := m.column(m.level)
	m.drawColumn(c, cx, cw, nodes, sel, styleCard, mid, int(math.Round(m.navY.pos)), true)
	if len(nodes) == 0 {
		msg := map[int]string{1: "no projects yet", 2: "no threads — press n to start one"}[m.level]
		c.blit(cx+2, mid, sFaint.Render(msg), 0)
	}

	// left: ancestors
	if m.level >= 1 {
		lw := min(32, cx-g-1)
		lx := cx - g - lw
		if lw >= 12 {
			pn, ps := m.column(m.level - 1)
			m.drawColumn(c, lx, lw, pn, ps, styleCompact, mid, 0, false)
			c.blit(lx+lw, mid, sBright.Render(strings.Repeat("─", g-1)+"┤"), 0)
			if m.level == 2 {
				w2 := min(18, lx-4)
				if w2 >= 8 {
					rn, rs := m.column(0)
					m.drawColumn(c, lx-3-w2, w2, rn, rs, styleTiny, mid, 0, false)
					c.blit(lx-3, mid, sMid.Render("──╴"), 0)
				}
			}
		} else {
			c.blit(cx-3, mid, sBright.Render("◀"), 0)
		}
	}

	// right: children, or the preview at the leaf
	rx := cx + cw + g
	if m.level < 2 {
		rw := min(34, m.w-rx-1)
		cn, cs := m.column(m.level + 1)
		if rw >= 12 && len(cn) > 0 {
			anchors := m.drawColumn(c, rx, rw, cn, cs, styleCompact, mid, 0, false)
			m.drawFan(c, cx+cw, g, mid, anchors, top, bot)
			// grandchildren
			if m.level == 0 {
				x2 := rx + rw + 3
				w2 := min(34, m.w-x2-1)
				gn, gs := m.column(2)
				if w2 >= 10 && len(gn) > 0 {
					st := styleTiny
					if w2 >= 24 {
						st = styleCompact
					}
					m.drawColumn(c, x2, w2, gn, gs, st, mid, 0, false)
					c.blit(rx+rw, mid, sMid.Render("──╴"), 0)
				}
			} else if x2 := rx + rw + 3; m.level == 1 && m.w-x2-1 >= 40 && m.selID[2] != "" {
				// wide: a live preview of the selected project's top thread
				c.blit(rx+rw, mid, sMid.Render("──▶"), 0)
				m.drawPreview(c, x2, top, min(m.w-x2-1, 100), bot-top+1)
			}
		} else if len(cn) > 0 {
			c.blit(cx+cw+2, mid, sBright.Render("▶"), 0)
		}
	} else if pw := min(m.w-rx-1, 110); pw >= 20 {
		c.blit(cx+cw, mid, sBright.Render(strings.Repeat("─", g-1)+"▶"), 0)
		m.drawPreview(c, rx, top, pw, bot-top+1)
	}
}

type colStyle int

const (
	styleCard colStyle = iota
	styleCompact
	styleTiny
)

// drawColumn draws nodes with the selected one centered on row mid and
// returns the anchor row of each node that was drawn (-1 if off screen).
func (m *model) drawColumn(c *canvas, x, w int, nodes []node, sel int, st colStyle, mid, vshift int, focus bool) []int {
	top, bot, _ := m.bodyBounds()
	h, pitch := 5, cardPitch
	switch st {
	case styleCompact:
		h, pitch = 2, 3
	case styleTiny:
		h, pitch = 1, 2
	}
	anchors := make([]int, len(nodes))
	above, below := 0, 0
	for i, n := range nodes {
		y := mid - h/2 + (i-sel)*pitch + vshift
		anchors[i] = -1
		if y < top {
			above++
			continue
		}
		if y+h-1 > bot {
			below++
			continue
		}
		anchors[i] = y + h/2
		selected := i == sel
		glyph, gst := statusGlyph(n.status, n.offline, m.frame)
		title := n.title
		tst, sub := sText, sFaint
		if n.dim {
			tst = sFaint
		}
		switch st {
		case styleCard:
			border := sDim
			if selected && focus {
				border = sBright
				tst = sWhite
			}
			badge := ""
			if n.unseen {
				badge = sWarn.Render(" ◆ new")
			}
			sw := 0
			spark := ""
			if n.act != nil && w >= 36 {
				sw = min(12, w/5)
				spark = sparkline(n.act, sw, time.Hour, n.status == "running")
			}
			lines := []string{
				" " + gst.Render(glyph) + " " + tst.Render(clip(title, w-6-lipgloss.Width(badge))) + badge,
				"   " + sGreen.Render(clip(n.sub, w-6)),
				"   " + padRight(sub.Render(clip(n.meta, w-8-sw)), w-6-sw) + spark,
			}
			if selected && focus {
				c.blit(x-1, y-1, box(w+2, h+2, sDeep, "", nil), 0)
				c.blit(x, y, glowBox(w, h, m.phase(), "", lines), 0)
				if m.frame/7%2 == 0 {
					c.blit(x-3, y+2, sBright.Render("▶"), 0)
				}
			} else {
				c.blit(x, y, box(w, h, border, "", lines), 0)
			}
		case styleCompact:
			bar := sDeep.Render("│")
			if selected {
				bar, tst = sBright.Render("▌"), sPale
			}
			badge := ""
			if n.unseen {
				badge = sWarn.Render(" ◆")
			}
			c.fill(x, y, w, h)
			c.blit(x, y, bar+gst.Render(glyph)+" "+tst.Render(clip(title, w-4-lipgloss.Width(badge)))+badge, w)
			c.blit(x, y+1, bar+"  "+sub.Render(clip(n.sub, w-3)), w)
		case styleTiny:
			if selected {
				tst = sPale
			} else {
				tst = sDim
			}
			c.fill(x, y, w, 1)
			c.blit(x, y, gst.Render(glyph)+" "+tst.Render(clip(title, w-2)), w)
		}
	}
	if above > 0 {
		c.blit(x+2, top, sDim.Render(fmt.Sprintf("▲ %d more", above)), w)
	}
	if below > 0 {
		c.blit(x+2, bot, sDim.Render(fmt.Sprintf("▼ %d more", below)), w)
	}
	return anchors
}

// drawFan draws the tree connector from (x0, mid) to each child anchor.
func (m *model) drawFan(c *canvas, x0, g, mid int, anchors []int, top, bot int) {
	lo, hi := mid, mid
	for _, a := range anchors {
		if a < 0 {
			continue
		}
		lo, hi = min(lo, a), max(hi, a)
	}
	jx := x0 + 2
	c.blit(x0, mid, sBright.Render("──"), 0)
	for y := lo; y <= hi; y++ {
		ch := "│"
		child := false
		for _, a := range anchors {
			if a == y {
				child = true
			}
		}
		switch {
		case y == lo && y == hi:
			ch = "─"
		case y == lo && child && y == mid:
			ch = "┬"
		case y == hi && child && y == mid:
			ch = "┴"
		case y == lo:
			ch = "┌"
		case y == hi:
			ch = "└"
		case child && y == mid:
			ch = "┼"
		case child:
			ch = "├"
		case y == mid:
			ch = "┤"
		}
		st := sDim
		if y == mid {
			st = sBright
		}
		c.blit(jx, y, st.Render(ch), 0)
		if child {
			c.blit(jx+1, y, st.Render(strings.Repeat("─", g-4)+"╴"), 0)
		}
	}
}

func (m *model) drawPreview(c *canvas, x, y, w, h int) {
	t, ok := m.thread(m.selID[2])
	if !ok {
		return
	}
	title := sWhite.Render("preview") + sFaint.Render(" · enter to open")
	var lines []string
	head := " " + sGreen.Render(shortRepo(t.Repo))
	if t.Branch != "" {
		head += sFaint.Render(" @ ") + sPale.Render(t.Branch)
	}
	lines = append(lines, head, " "+sFaint.Render(fmt.Sprintf("%s · %s · created %s", statusText(t), cmpOr(shortModel(t.Model), "default model"), ago(t.Created))), "")
	p := m.previews[t.ID]
	avail := h - 2 - len(lines)
	if p == nil || (p.events == nil && p.loading) {
		lines = append(lines, " "+sFaint.Render("loading…"))
	} else {
		tv := &threadView{id: t.ID, kind: "thread", images: m.images, expanded: map[string]bool{}, uiState: map[string]map[string]any{}}
		tv.events = p.events
		all := tv.renderAll(w-3, true)
		if len(all) > avail {
			all = all[len(all)-avail:]
		}
		for _, l := range all {
			lines = append(lines, " "+l)
		}
	}
	c.fill(x, y, w, h)
	c.blit(x, y, box(w, h, sDim, title, lines), 0)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

const cardPitch = 7

func (m *model) colW() int { return min(max(m.w*30/100, 30), 64) }

// cardRect is where the selected node's card sits when nothing is moving.
func (m *model) cardRect() rect {
	_, _, mid := m.bodyBounds()
	cw := m.colW()
	return rect{(m.w - cw) / 2, mid - 2, cw, 5}
}

// threadRect is the frame of thread mode's content.
func (m *model) threadRect() rect {
	w := m.w
	if sp := m.sidePanelW(); sp > 0 {
		w -= sp + 1
	}
	return rect{0, 2, w, m.h - 5}
}

// sidePanelW is the width of thread mode's files & UI panel (wide terminals only).
func (m *model) sidePanelW() int {
	if m.w >= 150 {
		return min(52, m.w/4)
	}
	return 0
}

// actFor merges the activity of the threads matching keep; nil until the
// first activity poll has landed.
func (m *model) actFor(keep func(Thread) bool) []int64 {
	if len(m.activity) == 0 {
		return nil
	}
	out := []int64{}
	for _, t := range m.threads {
		if keep(t) {
			out = append(out, m.activity[t.ID]...)
		}
	}
	return out
}
