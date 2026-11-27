package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// lineEditor is a small text input that renders as plain styled text, so it
// can be drawn on the canvas.
type lineEditor struct {
	text []rune
	pos  int
}

func (e *lineEditor) String() string { return string(e.text) }
func (e *lineEditor) clear()         { e.text, e.pos = nil, 0 }

func (e *lineEditor) insert(rs []rune) {
	e.text = append(e.text[:e.pos], append(append([]rune{}, rs...), e.text[e.pos:]...)...)
	e.pos += len(rs)
}

// key edits; returns false when the key isn't an editing key.
func (e *lineEditor) key(k tea.KeyMsg, multiline bool) bool {
	switch k.Type {
	case tea.KeyRunes, tea.KeySpace:
		rs := k.Runes
		if k.Type == tea.KeySpace {
			rs = []rune{' '}
		}
		if k.Alt && len(rs) == 0 {
			return false
		}
		e.insert(rs)
	case tea.KeyBackspace:
		if e.pos > 0 {
			e.text = append(e.text[:e.pos-1], e.text[e.pos:]...)
			e.pos--
		}
	case tea.KeyDelete:
		if e.pos < len(e.text) {
			e.text = append(e.text[:e.pos], e.text[e.pos+1:]...)
		}
	case tea.KeyLeft:
		e.pos = max(e.pos-1, 0)
	case tea.KeyRight:
		e.pos = min(e.pos+1, len(e.text))
	case tea.KeyHome, tea.KeyCtrlA:
		e.pos = 0
	case tea.KeyEnd, tea.KeyCtrlE:
		e.pos = len(e.text)
	case tea.KeyCtrlU:
		e.text, e.pos = e.text[e.pos:], 0
	case tea.KeyCtrlK:
		e.text = e.text[:e.pos]
	case tea.KeyCtrlW:
		i := e.pos
		for i > 0 && e.text[i-1] == ' ' {
			i--
		}
		for i > 0 && e.text[i-1] != ' ' {
			i--
		}
		e.text, e.pos = append(e.text[:i], e.text[e.pos:]...), i
	case tea.KeyCtrlJ:
		if !multiline {
			return false
		}
		e.insert([]rune{'\n'})
	case tea.KeyEnter:
		if k.Alt && multiline {
			e.insert([]rune{'\n'})
			return true
		}
		return false
	default:
		return false
	}
	return true
}

// render shows the text on one line (newlines as ↵), scrolled so the cursor
// is visible, with a blinking block cursor.
func (e *lineEditor) render(width, frame int, focused bool) string {
	return strings.Join(e.renderLines(width, 1, frame, focused), "")
}

// renderLines wraps the text into at most maxLines lines of width.
func (e *lineEditor) renderLines(width, maxLines, frame int, focused bool) []string {
	type cellR struct {
		r   rune
		cur bool
	}
	var rows [][]cellR
	row, w := []cellR{}, 0
	for i := 0; i <= len(e.text); i++ {
		cur := i == e.pos && focused
		r := ' '
		if i < len(e.text) {
			r = e.text[i]
		} else if !cur {
			break
		}
		if r == '\n' {
			if maxLines == 1 {
				r = '↵'
			} else {
				row = append(row, cellR{' ', cur})
				rows, row, w = append(rows, row), nil, 0
				continue
			}
		}
		rw := runewidth.RuneWidth(r)
		if w+rw > width {
			rows, row, w = append(rows, row), nil, 0
		}
		row = append(row, cellR{r, cur})
		w += rw
	}
	rows = append(rows, row)
	// keep the cursor's row visible
	curRow := len(rows) - 1
	for ri, r := range rows {
		for _, c := range r {
			if c.cur {
				curRow = ri
			}
		}
	}
	start := max(0, curRow-maxLines+1)
	end := min(len(rows), start+maxLines)
	var out []string
	blink := frame/6%2 == 0
	for _, r := range rows[start:end] {
		var b strings.Builder
		for _, c := range r {
			switch {
			case c.cur && blink:
				b.WriteString(sRev.Render(string(c.r)))
			case c.cur:
				b.WriteString(lipgloss.NewStyle().Foreground(cBright).Underline(true).Render(string(c.r)))
			default:
				b.WriteString(sWhite.UnsetBold().Render(string(c.r)))
			}
		}
		out = append(out, b.String())
	}
	return out
}

// seenStore remembers, per thread, the latest reply you've looked at, like
// the PWA's "rux.seen" local storage. A thread is unseen when it replied
// after that. Threads never opened count as seen.
type seenStore struct {
	mu   sync.Mutex
	path string
	m    map[string]time.Time
}

func loadSeen(path string) *seenStore {
	s := &seenStore{path: path, m: map[string]time.Time{}}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			json.Unmarshal(b, &s.m)
		}
	}
	return s
}

func (s *seenStore) unseen(id string, replied time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.m[id]
	if !ok {
		if s.path != "" || replied.IsZero() {
			// first sighting: start tracking from now
			s.m[id] = replied
			return false
		}
		return false
	}
	return replied.After(t)
}

func (s *seenStore) mark(id string, replied time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if replied.IsZero() || !replied.After(s.m[id]) {
		return
	}
	s.m[id] = replied
	if s.path != "" {
		os.MkdirAll(filepath.Dir(s.path), 0o700)
		b, _ := json.Marshal(s.m)
		os.WriteFile(s.path, b, 0o600)
	}
}

// setUnseen is for the demo: pretend we've seen an older reply.
func (s *seenStore) setSeen(id string, at time.Time) {
	s.mu.Lock()
	s.m[id] = at
	s.mu.Unlock()
}
