package tui

import (
	"math"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/harmonica"
	"github.com/charmbracelet/lipgloss"
)

// Motion: harmonica springs drive column slides, sibling moves, the zoom in
// and out of thread mode, and charts growing in on the monitor.

const animFPS = 30

var (
	slideSpring = harmonica.NewSpring(harmonica.FPS(animFPS), 7.5, 0.82)
	zoomSpring  = harmonica.NewSpring(harmonica.FPS(animFPS), 9.0, 1.0)
	growSpring  = harmonica.NewSpring(harmonica.FPS(animFPS), 4.5, 0.9)
)

type springVal struct{ pos, vel, target float64 }

func (s *springVal) step(sp harmonica.Spring) {
	s.pos, s.vel = sp.Update(s.pos, s.vel, s.target)
	if math.Abs(s.pos-s.target) < 0.02 && math.Abs(s.vel) < 0.05 {
		s.pos, s.vel = s.target, 0
	}
}

func (s *springVal) moving() bool { return s.pos != s.target || s.vel != 0 }

type rect struct{ x, y, w, h int }

func lerp(a, b int, t float64) int { return a + int(math.Round(float64(b-a)*t)) }

func (r rect) lerp(o rect, t float64) rect {
	return rect{lerp(r.x, o.x, t), lerp(r.y, o.y, t), lerp(r.w, o.w, t), lerp(r.h, o.h, t)}
}

// zoom is the expand/collapse between a node and thread mode.
type zoom struct {
	from, to rect
	t        springVal // 0 = from, 1 = to
	in       bool      // expanding into the thread
	under    mode      // what's drawn beneath while it runs
	title    string
}

func (z *zoom) done() bool { return !z.t.moving() }

func (m *model) startZoom(from, to rect, in bool, under mode, title string) {
	z := &zoom{from: from, to: to, in: in, under: under, title: title}
	if in {
		z.t = springVal{0, 0, 1}
	} else {
		z.t = springVal{1, 0, 0}
	}
	m.zoom = z
}

func (m *model) drawZoom(c *canvas) {
	z := m.zoom
	t := math.Min(math.Max(z.t.pos, 0), 1.05)
	r := z.from.lerp(z.to, t)
	if r.w < 4 || r.h < 3 {
		return
	}
	c.fill(r.x, r.y, r.w, r.h)
	title := ""
	if r.w > 20 {
		title = sWhite.Render(clip(z.title, r.w-8))
	}
	c.blit(r.x, r.y, glowBox(r.w, r.h, m.phase(), title, nil), 0)
}

// animating reports whether anything is in motion (ticks run faster then).
func (m *model) animating() bool {
	return m.navX.moving() || m.navY.moving() || m.zoom != nil || m.grow.moving()
}

func (m *model) stepAnim() {
	m.navX.step(slideSpring)
	m.navY.step(slideSpring)
	m.grow.step(growSpring)
	if m.zoom != nil {
		m.zoom.t.step(zoomSpring)
		if m.zoom.done() || (m.zoom.in && m.zoom.t.pos > 0.985) || (!m.zoom.in && m.zoom.t.pos < 0.015) {
			m.zoom = nil
		}
	}
}

// phase is a slow 0..1 cycle (one loop every 5s) for the focus shine.
func (m *model) phase() float64 {
	return float64(time.Since(m.t0)%(5*time.Second)) / float64(5*time.Second)
}

// glowRamp: deep → bright → pale → white-ish highlight → back.
var glowRamp = []int{34, 40, 46, 82, 120, 157, 194, 157, 120, 82, 46, 40}

// glowBox is a rounded box whose border is a green gradient with a soft
// highlight travelling slowly around it (phase 0..1).
func glowBox(w, h int, phase float64, title string, lines []string) string {
	if w < 4 {
		w = 4
	}
	if h < 2 {
		h = 2
	}
	in := w - 2
	perim := 2*(w+h) - 4
	col := func(p int) lipgloss.Style {
		// distance from the highlight, as a fraction of the perimeter
		d := math.Abs(float64(p)/float64(perim) - phase)
		d = math.Min(d, 1-d)
		var c int
		switch {
		case d < 0.04:
			c = 194
		case d < 0.08:
			c = 157
		case d < 0.13:
			c = 120
		case d < 0.2:
			c = 82
		case d < 0.32:
			c = 46
		default:
			c = 40
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color(strconv.Itoa(c)))
	}
	// perimeter positions: top left→right, right top→bottom, bottom right→left, left bottom→top
	topRow := make([]string, w)
	for x := 0; x < w; x++ {
		ch := "─"
		if x == 0 {
			ch = "╭"
		} else if x == w-1 {
			ch = "╮"
		}
		topRow[x] = col(x).Render(ch)
	}
	if title != "" {
		t := " " + title + " "
		tw := lipgloss.Width(t)
		if tw <= in-2 {
			topRow[2] = t
			for x := 3; x < 2+tw && x < w-1; x++ {
				topRow[x] = ""
			}
		}
	}
	var b strings.Builder
	b.WriteString(strings.Join(topRow, ""))
	for y := 1; y < h-1; y++ {
		l := ""
		if y-1 < len(lines) {
			l = lines[y-1]
		}
		if lipgloss.Width(l) > in {
			l = truncANSI(l, in)
		}
		left := perim - y
		right := w - 1 + y
		b.WriteString("\n" + col(left).Render("│") + padRight(l, in) + col(right).Render("│"))
	}
	var bot strings.Builder
	for x := 0; x < w; x++ {
		ch := "─"
		if x == 0 {
			ch = "╰"
		} else if x == w-1 {
			ch = "╯"
		}
		p := w + h - 2 + (w - 1 - x)
		bot.WriteString(col(p).Render(ch))
	}
	b.WriteString("\n" + bot.String())
	return b.String()
}

// activity is a background poll of every live thread's recent event times,
// feeding the node sparklines and the monitor's lanes.
func sparkline(ts []int64, n int, span time.Duration, hot bool) string {
	if n <= 0 {
		return ""
	}
	counts := make([]int, n)
	now := time.Now().UnixMilli()
	from := now - span.Milliseconds()
	mx := 0
	for _, t := range ts {
		if i := int((t - from) * int64(n) / span.Milliseconds()); i >= 0 && i < n {
			counts[i]++
			mx = max(mx, counts[i])
		}
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	var b strings.Builder
	for i, c := range counts {
		if c == 0 {
			b.WriteString(sDeep.Render("▁"))
			continue
		}
		lv := int(math.Ceil(float64(c) / float64(max(mx, 1)) * 7))
		st := sDim
		switch {
		case i >= n-2 && hot:
			st = sBright
		case lv >= 5:
			st = sGreen
		case lv >= 3:
			st = sMid
		}
		b.WriteString(st.Render(string(blocks[min(lv, 7)])))
	}
	return b.String()
}

// startActivity polls recent event times for every live thread (every 20s).
func (m *model) startActivity() tea.Cmd {
	go func() {
		for {
			snap := make(chan []Thread, 1)
			m.post(asyncMsg{func(m *model) tea.Cmd { snap <- m.threads; return nil }})
			var threads []Thread
			select {
			case threads = <-snap:
			case <-m.ctx.Done():
				return
			}
			out := map[string][]int64{}
			for _, t := range threads {
				if t.Archived || t.Status == "destroyed" {
					continue
				}
				pg, err := m.src.Page(m.ctx, t.ID, 0, 300)
				if err != nil {
					continue
				}
				for _, e := range pg.Events {
					out[t.ID] = append(out[t.ID], e.TS)
				}
			}
			m.post(asyncMsg{func(m *model) tea.Cmd {
				for k, v := range out {
					m.activity[k] = v
				}
				return nil
			}})
			wait := 20 * time.Second
			if len(threads) == 0 {
				wait = 2 * time.Second // the first thread list hasn't arrived yet
			}
			select {
			case <-time.After(wait):
			case <-m.ctx.Done():
				return
			}
		}
	}()
	return nil
}

// noteEvent adds a live event time (from an open stream) to the activity.
func (m *model) noteEvent(id string, ts int64) {
	m.activity[id] = append(m.activity[id], ts)
}
