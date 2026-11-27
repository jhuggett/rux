// Package tui is a terminal client for rux serve: a spatial tree of the
// root, projects and threads, a chat view per agent, and a monitor
// dashboard, in a green-phosphor style. It uses the same JSON API as the PWA.
package tui

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/jhuggett/rux/tui/termimg"
)

// Run is `rux tui [--server URL] [--demo]`.
func Run(args []string) {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	def := os.Getenv("RUX_SERVER")
	if def == "" {
		def = "http://127.0.0.1:7700"
	}
	server := fs.String("server", def, "rux serve URL (or $RUX_SERVER), e.g. https://mac.tailnet.ts.net")
	demo := fs.Bool("demo", false, "use a built-in fake server with sample projects, threads and live activity")
	export := fs.String("export-demo", "", "write the demo data as a rux serve data directory and exit (for testing)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: rux tui [--server URL] [--demo]")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	if *export != "" {
		if err := exportDemo(*export); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if os.Getenv("COLORTERM") == "truecolor" || os.Getenv("COLORTERM") == "24bit" {
		lipgloss.SetColorProfile(termenv.TrueColor)
	} else {
		lipgloss.SetColorProfile(termenv.ANSI256)
	}
	lipgloss.SetHasDarkBackground(true)

	var src Source
	var seen *seenStore
	if *demo {
		d := newDemoSource()
		src = d
		seen = loadSeen("")
		// one reply you haven't looked at yet, so "unseen" shows up
		for _, t := range d.threads {
			if t.Title == "Fix SSE reconnect" {
				seen.setSeen(t.ID, t.Replied.Add(-time.Hour))
			}
		}
	} else {
		src = newHTTPSource(*server)
		dir, err := os.UserConfigDir()
		if err != nil {
			dir = os.TempDir()
		}
		seen = loadSeen(filepath.Join(dir, "rux", "tui-seen.json"))
	}
	m := newModel(src, seen)
	m.graphics = termimg.Detect().String() + " (full-screen viewer) · half-blocks inline"
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "rux tui:", err)
		os.Exit(1)
	}
}
