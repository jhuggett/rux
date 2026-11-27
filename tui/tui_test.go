package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// TestViewsRender drives the demo through every mode at several terminal
// sizes and checks each frame fills the window without panicking.
func TestViewsRender(t *testing.T) {
	src := newDemoSource()
	for _, sz := range [][2]int{{80, 24}, {160, 45}, {240, 60}, {40, 12}} {
		m := newModel(src, loadSeen(""))
		m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		msg := m.fetch()()
		m.Update(msg)
		check := func(what string) {
			t.Helper()
			for i := 0; i < 40; i++ { // let animations settle
				m.Update(tickMsg(time.Now()))
			}
			out := m.View()
			lines := strings.Split(out, "\n")
			if len(lines) != sz[1] {
				t.Fatalf("%dx%d %s: %d lines, want %d", sz[0], sz[1], what, len(lines), sz[1])
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w != sz[0] {
					t.Fatalf("%dx%d %s: line %d is %d wide, want %d", sz[0], sz[1], what, i, w, sz[0])
				}
			}
		}
		key := func(s string) {
			var k tea.KeyMsg
			switch s {
			case "right":
				k = tea.KeyMsg{Type: tea.KeyRight}
			case "down":
				k = tea.KeyMsg{Type: tea.KeyDown}
			case "enter":
				k = tea.KeyMsg{Type: tea.KeyEnter}
			case "esc":
				k = tea.KeyMsg{Type: tea.KeyEsc}
			case "tab":
				k = tea.KeyMsg{Type: tea.KeyTab}
			default:
				k = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
			}
			m.Update(k)
		}
		check("root")
		key("right")
		check("projects")
		key("right")
		check("threads")
		key("enter")
		// load the thread page synchronously
		if pg, err := src.Page(m.ctx, m.th.id, 0, 200); err == nil {
			m.Update(pageMsg{gen: m.th.gen, page: pg})
		}
		check("thread")
		key("]")
		key("e")
		check("thread expanded")
		key("esc")
		key("tab")
		m.mon.update(src.overview())
		check("monitor")
		key("?")
		check("help")
		m.cancel()
	}
}
