// rux runs Claude Code agents in sandboxed Rox guests, driven from a mobile web app.
//
//	rux serve   - coordinator + host agent: web app, thread store, launches guests
//	rux runner  - runs inside a guest: owns the claude process and dials home
//	rux git-credential - git credential helper the runner sets up for its children
//	rux share / rux screenshot - used by claude in a guest to show the user files and pages
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "serve":
		runServe(os.Args[2:])
	case "runner":
		runRunner(os.Args[2:])
	case "git-credential":
		runGitCredential(os.Args[2:])
	case "share":
		runShare(os.Args[2:])
	case "screenshot":
		runScreenshot(os.Args[2:])
	case "ui":
		runUI(os.Args[2:])
	case "diff":
		runDiff(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: rux serve [flags] | rux runner [flags]  (-h for flags)")
	os.Exit(2)
}
