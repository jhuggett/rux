package termimg

import (
	"os"
	"strings"
)

// Protocol is a terminal graphics protocol.
type Protocol int

const (
	None Protocol = iota
	Kitty
	ITerm2
	Sixel
)

func (p Protocol) String() string {
	switch p {
	case Kitty:
		return "kitty"
	case ITerm2:
		return "iterm2"
	case Sixel:
		return "sixel"
	default:
		return "none"
	}
}

// ParseProtocol parses "kitty", "iterm"/"iterm2", "sixel", "none"/"off"/"text".
func ParseProtocol(s string) (Protocol, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "kitty":
		return Kitty, true
	case "iterm", "iterm2":
		return ITerm2, true
	case "sixel":
		return Sixel, true
	case "none", "off", "text", "halfblock":
		return None, true
	}
	return None, false
}

// Detect guesses the graphics protocol from the environment.
// RUX_TUI_GRAPHICS=kitty|iterm|sixel|none overrides. Inside tmux it returns
// None unless overridden (passthrough is unreliable).
func Detect() Protocol {
	if p, ok := ParseProtocol(os.Getenv("RUX_TUI_GRAPHICS")); ok {
		return p
	}
	if os.Getenv("TMUX") != "" || strings.HasPrefix(os.Getenv("TERM"), "screen") {
		return None
	}
	term := strings.ToLower(os.Getenv("TERM"))
	prog := strings.ToLower(os.Getenv("TERM_PROGRAM"))

	switch {
	case os.Getenv("KITTY_WINDOW_ID") != "", term == "xterm-kitty",
		prog == "ghostty", term == "xterm-ghostty", os.Getenv("GHOSTTY_RESOURCES_DIR") != "":
		return Kitty
	case prog == "wezterm":
		return ITerm2
	case prog == "iterm.app", os.Getenv("LC_TERMINAL") == "iTerm2":
		return ITerm2
	case prog == "vscode", prog == "mintty", prog == "warpterminal":
		return ITerm2
	case strings.Contains(term, "sixel"), strings.HasPrefix(term, "foot"),
		strings.HasPrefix(term, "mlterm"), prog == "mlterm":
		return Sixel
	}
	return None
}
