// rux runs Claude Code agents in sandboxed Rox guests, driven from a mobile web app.
//
//	rux serve   - coordinator + host agent: web app, thread store, launches guests
//	rux runner  - runs inside a guest: owns the claude process and dials home
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
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: rux serve [flags] | rux runner [flags]  (-h for flags)")
	os.Exit(2)
}
