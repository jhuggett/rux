package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Green phosphor palette (xterm-256 so it survives every terminal and the
// headless captures).
var (
	cBright = lipgloss.Color("46")  // hot green: selection, live
	cGreen  = lipgloss.Color("40")  // primary green
	cMid    = lipgloss.Color("34")  // borders of focused things
	cDim    = lipgloss.Color("28")  // borders, rules
	cDeep   = lipgloss.Color("22")  // faint structure
	cText   = lipgloss.Color("151") // pale green body text
	cPale   = lipgloss.Color("120") // highlighted text
	cWhite  = lipgloss.Color("231") // emphasis
	cFaint  = lipgloss.Color("241") // metadata
	cWarn   = lipgloss.Color("214")
	cErr    = lipgloss.Color("203")

	sBright = lipgloss.NewStyle().Foreground(cBright).Bold(true)
	sGreen  = lipgloss.NewStyle().Foreground(cGreen)
	sMid    = lipgloss.NewStyle().Foreground(cMid)
	sDim    = lipgloss.NewStyle().Foreground(cDim)
	sDeep   = lipgloss.NewStyle().Foreground(cDeep)
	sText   = lipgloss.NewStyle().Foreground(cText)
	sPale   = lipgloss.NewStyle().Foreground(cPale)
	sWhite  = lipgloss.NewStyle().Foreground(cWhite).Bold(true)
	sFaint  = lipgloss.NewStyle().Foreground(cFaint)
	sWarn   = lipgloss.NewStyle().Foreground(cWarn)
	sErr    = lipgloss.NewStyle().Foreground(cErr)
	sKey    = lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(cGreen).Bold(true)
	sRev    = lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(cBright).Bold(true)
)

// statusText and statusGlyph follow the PWA's wording.
func statusText(t Thread) string {
	if t.Offline() {
		return "offline"
	}
	switch t.Status {
	case "provisioning":
		return "setting up"
	case "running":
		return "working"
	case "idle":
		return "ready"
	case "destroying":
		return "removing"
	case "destroyed":
		return "removed"
	}
	return t.Status
}

// glyph returns a status glyph and its style; frame animates "working".
func statusGlyph(status string, offline bool, frame int) (string, lipgloss.Style) {
	if offline {
		return "◌", sFaint
	}
	switch status {
	case "running":
		spin := []string{"◐", "◓", "◑", "◒"}
		return spin[frame/2%4], sBright
	case "provisioning", "starting":
		return []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}[frame%10], sWarn
	case "idle":
		return "●", sGreen
	case "failed":
		return "✗", sErr
	case "destroying", "destroyed":
		return "·", sFaint
	case "":
		return "○", sDim
	}
	return "○", sDim
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < 45*time.Second:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func shortRepo(repo string) string {
	r := strings.TrimSuffix(strings.TrimPrefix(repo, "https://github.com/"), ".git")
	return strings.TrimPrefix(r, "https://")
}

func shortModel(m string) string {
	m = strings.TrimPrefix(m, "claude-")
	if i := strings.Index(m, "["); i > 0 {
		m = m[:i]
	}
	return m
}

func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if lipgloss.Width(s) <= n {
		return s
	}
	out := ""
	for _, c := range r {
		if lipgloss.Width(out+string(c)) > n-1 {
			break
		}
		out += string(c)
	}
	return out + "…"
}

func padRight(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

func fmtBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%dM", b>>20)
	case b >= 1<<10:
		return fmt.Sprintf("%dK", b>>10)
	}
	return fmt.Sprintf("%dB", b)
}

// box draws a rounded box of the given size with an optional title set into
// the top border. Content lines are clipped/padded to the inside width.
func box(w, h int, border lipgloss.Style, title string, lines []string) string {
	if w < 4 {
		w = 4
	}
	in := w - 2
	var b strings.Builder
	top := "╭"
	if title != "" {
		t := " " + clip(title, in-3) + " "
		top += "─" + t + border.Render(strings.Repeat("─", max(in-1-lipgloss.Width(t), 0)))
		b.WriteString(border.Render("╭─") + t + border.Render(strings.Repeat("─", max(in-1-lipgloss.Width(t), 0))+"╮"))
	} else {
		b.WriteString(border.Render("╭" + strings.Repeat("─", in) + "╮"))
	}
	_ = top
	for i := 0; i < h-2; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		if lipgloss.Width(l) > in {
			l = truncANSI(l, in)
		}
		b.WriteString("\n" + border.Render("│") + padRight(l, in) + border.Render("│"))
	}
	b.WriteString("\n" + border.Render("╰"+strings.Repeat("─", in)+"╯"))
	return b.String()
}
