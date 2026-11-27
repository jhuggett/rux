package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"
)

// demoSource is an in-memory rux serve: a few projects, managers and
// workers with realistic transcripts, and simulated live activity.
type demoSource struct {
	mu       sync.Mutex
	projects []*Project
	threads  []*demoThread
	assets   map[string][]byte
	start    time.Time
}

type demoThread struct {
	Thread
	events []Event
	subs   map[int]func(Event)
	nsub   int
	script []json.RawMessage // pending scripted events while running
}

func newDemoSource() *demoSource {
	d := &demoSource{assets: map[string][]byte{}, start: time.Now()}
	d.seed()
	go d.loop()
	return d
}

func (d *demoSource) Name() string { return "demo" }

func ev(typ string, kv ...any) json.RawMessage {
	m := map[string]any{"type": typ}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	b, _ := json.Marshal(m)
	return b
}

func evUser(text string) json.RawMessage { return ev("rux_user", "text", text) }
func evText(text string) json.RawMessage {
	return ev("assistant", "message", map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}})
}
func evThink(text string) json.RawMessage {
	return ev("assistant", "message", map[string]any{"content": []any{map[string]any{"type": "thinking", "thinking": text}}})
}
func evTool(id, name string, input map[string]any) json.RawMessage {
	return ev("assistant", "message", map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}})
}
func evResult(id, out string, isErr bool) json.RawMessage {
	return ev("user", "message", map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": out, "is_error": isErr}}})
}
func evDone(secs, turns int, cost float64) json.RawMessage {
	return ev("result", "subtype", "success", "duration_ms", secs*1000, "num_turns", turns, "total_cost_usd", cost)
}

var toolSeq int

func tool(name string, input map[string]any, out string) []json.RawMessage {
	toolSeq++
	id := fmt.Sprintf("toolu_%04d", toolSeq)
	return []json.RawMessage{evTool(id, name, input), evResult(id, out, false)}
}

func flat(parts ...any) []json.RawMessage {
	var out []json.RawMessage
	for _, p := range parts {
		switch v := p.(type) {
		case json.RawMessage:
			out = append(out, v)
		case []json.RawMessage:
			out = append(out, v...)
		}
	}
	return out
}

func (d *demoSource) addProject(id, name, repo, notes string) *Project {
	p := &Project{ID: id, Name: name, Repo: repo, Notes: notes, Created: d.start.Add(-30 * 24 * time.Hour), Updated: d.start.Add(-48 * time.Hour)}
	d.projects = append(d.projects, p)
	return p
}

var demoIDn int

func (d *demoSource) addThread(p *Project, kind, title, status, branch, model string, ago time.Duration, evs []json.RawMessage) *demoThread {
	demoIDn++
	created := d.start.Add(-ago)
	t := &demoThread{Thread: Thread{
		ID: created.UTC().Format("20060102-150405") + fmt.Sprintf("-%03x", 0xa00+demoIDn*37), Title: title, Project: p.ID, Kind: kind,
		Repo: p.Repo, Branch: branch, Model: model, Status: status, Backend: "rox", Created: created, Connected: status != "failed",
		Guest: fmt.Sprintf("rv-%s-%08x", strings.ReplaceAll(p.ID, "/", "-"), rand.Uint32()),
	}, subs: map[int]func(Event){}}
	if kind == "manager" {
		t.Title = p.Name + " · manager"
	}
	// spread events across the thread's life, ending a little before now
	n := len(evs)
	for i, e := range evs {
		frac := float64(i) / float64(max(n, 1))
		ts := created.Add(time.Duration(frac * float64(ago-2*time.Minute)))
		t.events = append(t.events, Event{ID: int64(i + 1), TS: ts.UnixMilli(), Src: srcOf(e), Ev: e})
		var probe struct{ Type string }
		json.Unmarshal(e, &probe)
		if probe.Type == "result" {
			t.Replied = ts
		}
	}
	if len(t.events) > 0 {
		t.Updated = time.UnixMilli(t.events[len(t.events)-1].TS)
	}
	d.threads = append(d.threads, t)
	return t
}

func srcOf(e json.RawMessage) string {
	var p struct{ Type string }
	json.Unmarshal(e, &p)
	if strings.HasPrefix(p.Type, "rux_status") || p.Type == "rux_asset" || p.Type == "rux_ui" {
		return "rux"
	}
	return "runner"
}

func (d *demoSource) asset(threadID, name, mime string, b []byte, caption string) json.RawMessage {
	file := fmt.Sprintf("%03d-%s", len(d.assets), name)
	u := "/api/threads/" + threadID + "/assets/" + file
	d.assets[u] = b
	inline := strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "video/")
	return ev("rux_asset", "file", file, "name", name, "caption", caption, "mime", mime, "inline", inline, "size", len(b), "url", u)
}

func setup(repo string) []json.RawMessage {
	return flat(
		ev("rux_status", "status", "provisioning", "connected", false),
		ev("rux_setup", "text", "$ rox up container --no-project --no-git-agent --no-tailscale --memory 4G -- bash -lc …"),
		ev("rux_setup", "text", "pulling image ghcr.io/rox/base:latest"),
		ev("rux_setup", "text", "guest rv-7f3a91c2e0 started"),
		ev("rux_runner_connected", "hostname", "rv-7f3a91c2e0", "dir", "/work"),
		ev("rux_preparing"),
		ev("rux_checkout", "branch", "main", "dir", "/work/"+shortRepo(repo)),
		ev("rux_ready"),
	)
}

const demoPatch = `diff --git a/tui/nav.go b/tui/nav.go
index 3b1f2a9..8c4d0e1 100644
--- a/tui/nav.go
+++ b/tui/nav.go
@@ -41,12 +41,17 @@ func (m *model) moveLevel(dir int) {
 	if m.level+dir < 0 || m.level+dir > 2 {
 		return
 	}
-	m.level += dir
-	m.sel[m.level] = 0
+	from := m.level
+	m.level += dir
+	if m.sel[m.level] >= len(m.column(m.level)) {
+		m.sel[m.level] = 0
+	}
+	m.slide = dir * slideFrames
+	m.syncBreadcrumb()
 	m.syncPreview()
 }

 func (m *model) column(level int) []node {
-	return nil
+	return m.tree[level]
 }
diff --git a/README.md b/README.md
--- a/README.md
+++ b/README.md
@@ -12,3 +12,6 @@
 - Threads are stored in ~/.rux/threads/ID/
+
+## Terminal UI
+` + "`rux tui`" + ` is a terminal client for the same API.
`

func (d *demoSource) seed() {
	rux := d.addProject("jhuggett-rux", "jhuggett/rux", "https://github.com/jhuggett/rux.git", "Go, single main package at repo root; web PWA in web/.")
	wtfc := d.addProject("jhuggett-wtfc", "jhuggett/wtfc", "https://github.com/jhuggett/wtfc.git", "")
	tide := d.addProject("jhuggett-tidepool", "jhuggett/tidepool", "https://github.com/jhuggett/tidepool.git", "Godot 4 game. Publish with the itch-publish action.")
	d.addProject("scratch", "scratch", "", "")

	// --- rux: manager with a dashboard, workers in several states ---
	mgr := d.addThread(rux, "manager", "", "idle", "main", "opus", 5*time.Hour, nil)
	statSpec := map[string]any{"root": "main", "elements": map[string]any{
		"main":  map[string]any{"type": "Stack", "props": map[string]any{"gap": 2}, "children": []string{"stats", "chart", "tbl"}},
		"stats": map[string]any{"type": "Grid", "props": map[string]any{"columns": 4}, "children": []string{"s1", "s2", "s3", "s4"}},
		"s1":    map[string]any{"type": "Stat", "props": map[string]any{"label": "workers", "value": "4", "sub": "2 working", "tone": "green"}},
		"s2":    map[string]any{"type": "Stat", "props": map[string]any{"label": "turns today", "value": "37", "trend": 12}},
		"s3":    map[string]any{"type": "Stat", "props": map[string]any{"label": "tool calls", "value": "412", "trend": 30}},
		"s4":    map[string]any{"type": "Stat", "props": map[string]any{"label": "spend", "value": "$6.80", "sub": "today", "tone": "warn"}},
		"chart": map[string]any{"type": "Chart", "props": map[string]any{"kind": "bar", "height": 120, "labels": []string{"9", "10", "11", "12", "13", "14", "15", "16"}, "series": []any{map[string]any{"name": "tool calls", "data": []int{12, 30, 44, 18, 52, 61, 40, 75}}}, "yLabel": "tool calls / hour"}},
		"tbl": map[string]any{"type": "Table", "props": map[string]any{"columns": []any{"worker", "status", map[string]any{"key": "turns", "label": "turns", "align": "right"}, map[string]any{"key": "branch", "label": "branch", "mono": true}},
			"rows": []any{
				map[string]any{"worker": "TUI prototype", "status": "working", "turns": 14, "branch": "tui-prototype"},
				map[string]any{"worker": "Fix SSE reconnect", "status": "ready", "turns": 6, "branch": "sse-reconnect"},
				map[string]any{"worker": "Push notifications", "status": "failed", "turns": 3, "branch": "push"},
				map[string]any{"worker": "Docs pass", "status": "ready", "turns": 4, "branch": "docs"},
			}}},
	}}
	askSpec := map[string]any{"root": "card", "state": map[string]any{"plan": "Ship the TUI first", "notify": true}, "elements": map[string]any{
		"card":   map[string]any{"type": "Card", "props": map[string]any{"title": "What next?", "subtitle": "pick one and I'll brief a worker"}, "children": []string{"plan", "notify", "btns"}},
		"plan":   map[string]any{"type": "Choice", "props": map[string]any{"label": "priority", "options": []string{"Ship the TUI first", "Fix push notifications", "Both in parallel"}, "value": map[string]any{"$bindState": "/plan"}}},
		"notify": map[string]any{"type": "Toggle", "props": map[string]any{"label": "ping me when done", "value": map[string]any{"$bindState": "/notify"}}},
		"btns":   map[string]any{"type": "Buttons", "children": []string{"go", "later"}},
		"go":     map[string]any{"type": "Button", "props": map[string]any{"label": "Go", "variant": "primary"}, "on": map[string]any{"press": map[string]any{"action": "submit"}}},
		"later":  map[string]any{"type": "Button", "props": map[string]any{"label": "Not now"}, "on": map[string]any{"press": map[string]any{"action": "dismiss"}}},
	}}
	mgr.events = nil
	mgrEvs := flat(setup(rux.Repo),
		evUser("Build a prototype terminal UI for rux in Go with Bubble Tea. Matrix aesthetic; spatial tree navigation."),
		evThink("The user wants a TUI. I'll start one worker on the prototype and keep the SSE fix going."),
		tool("mcp__rux__start_thread", map[string]any{"title": "TUI prototype", "prompt": "Build rux tui …", "branch": "tui-prototype"}, "started worker 20261127-101500-a25"),
		evText("Started a worker on the **TUI prototype** (branch `tui-prototype`). I'll check in when it finishes a turn."),
		evDone(41, 3, 0.31),
		evUser("[rux] worker 20261127-090000-a4b (\"Fix SSE reconnect\") finished a turn after 212s. Its last message:\n\nReconnect now resumes from Last-Event-ID; pushed sse-reconnect."),
		tool("mcp__rux__read_thread", map[string]any{"thread_id": "20261127-090000-a4b"}, "…transcript…"),
		evText("The SSE worker pushed `sse-reconnect`: reconnects now resume from `Last-Event-ID`, so no duplicate events after a laptop sleep."),
		evDone(18, 2, 0.12),
		evUser("how's it going?"),
		tool("mcp__rux__thread_stats", map[string]any{}, "{…}"),
		ev("rux_ui", "ui", "ui-stats-1", "title", "rux · workers", "ask", false, "spec", statSpec),
		evText("Two workers are busy, one failed (push notifications needs an APNs key). Spend is about **$6.80** today."),
		evDone(25, 2, 0.22),
		ev("rux_ui", "ui", "ui-ask-1", "title", "next step", "ask", true, "spec", askSpec),
		evDone(4, 1, 0.03),
	)
	d.reseat(mgr, mgrEvs, 5*time.Hour)

	tuiW := d.addThread(rux, "", "TUI prototype", "running", "tui-prototype", "opus", 70*time.Minute, nil)
	shot := demoScreenshot(640, 400)
	tuiEvs := flat(setup(rux.Repo),
		evUser("Build rux tui: Bubble Tea, matrix palette, tree nav (root → projects → threads), thread mode, monitor mode."),
		evThink("Read server.go routes first, then the PWA's render() to mirror event handling."),
		tool("Read", map[string]any{"file_path": "/work/rux/server.go"}, "1\tpackage main\n…1211 lines"),
		tool("Grep", map[string]any{"pattern": "rux_[a-z_]+", "path": "web/index.html"}, "rux_setup rux_ui rux_tool rux_asset …"),
		tool("Bash", map[string]any{"command": "go get github.com/charmbracelet/bubbletea@v1.3.10"}, "go: added github.com/charmbracelet/bubbletea v1.3.10"),
		evText("Routes and event types mapped. Writing `tui/client.go` (HTTP + SSE) and a demo source next."),
		tool("Write", map[string]any{"file_path": "/work/rux/tui/client.go"}, "ok"),
		tool("Bash", map[string]any{"command": "go build ./... && go vet ./..."}, ""),
		d.asset(tuiW.ID, "nav-projects.png", "image/png", shot, "navigation · projects level"),
		ev("rux_ui", "ui", "ui-diff-1", "title", "moveLevel: keep selection, slide", "ask", false, "spec", map[string]any{"root": "d", "elements": map[string]any{
			"d": map[string]any{"type": "Diff", "props": map[string]any{"patch": demoPatch}}}}),
		evText("Here's the projects level and the diff for the slide transition. Monitor mode is next."),
		evDone(380, 22, 1.84),
		evUser("Looks great. Add the monitor dashboard with sparklines."),
		tool("Read", map[string]any{"file_path": "/work/rux/overview.go"}, "…"),
		evText("Reading `overview.go` to mirror the PWA's stat tiles."),
	)
	d.reseat(tuiW, tuiEvs, 70*time.Minute)
	tuiW.Parent = mgr.ID

	sse := d.addThread(rux, "", "Fix SSE reconnect", "idle", "sse-reconnect", "sonnet", 3*time.Hour, nil)
	d.reseat(sse, flat(setup(rux.Repo),
		evUser("The PWA duplicates events after the laptop sleeps. Make reconnects resume cleanly."),
		tool("Grep", map[string]any{"pattern": "Last-Event-ID"}, "server.go:664"),
		tool("Edit", map[string]any{"file_path": "/work/rux/web/index.html"}, "ok"),
		tool("Bash", map[string]any{"command": "go test ./..."}, "ok  \tgithub.com/jhuggett/rux\t0.412s"),
		tool("Bash", map[string]any{"command": "git push -u origin sse-reconnect"}, "branch 'sse-reconnect' set up to track 'origin/sse-reconnect'."),
		evText("Reconnect now resumes from `Last-Event-ID`; pushed `sse-reconnect`.\n\n- the server already honoured the header\n- the client was passing `after=0` on reconnect\n\n```js\nes = new EventSource(`/api/threads/${id}/events?after=${lastId}`);\n```"),
		evDone(212, 9, 0.64),
	), 3*time.Hour)
	sse.Parent = mgr.ID
	sse.Replied = time.Now().Add(-3 * time.Minute) // unseen reply

	push := d.addThread(rux, "", "Push notifications", "failed", "push", "sonnet", 2*time.Hour, nil)
	d.reseat(push, flat(setup(rux.Repo),
		evUser("Add web push notifications when a thread finishes."),
		tool("Bash", map[string]any{"command": "go get github.com/SherClockHolmes/webpush-go"}, "ok"),
		ev("rux_stderr", "text", "Error: VAPID private key not configured"),
		ev("rux_exit", "code", 1, "error", "exit status 1"),
		ev("rux_error", "text", "claude exited with code 1; a new message will restart it"),
	), 2*time.Hour)
	push.Status = "failed"

	docs := d.addThread(rux, "", "Docs pass", "idle", "docs", "haiku", 26*time.Hour, nil)
	d.reseat(docs, flat(setup(rux.Repo),
		evUser("Tighten the README; document actions."),
		tool("Read", map[string]any{"file_path": "/work/rux/README.md"}, "…"),
		tool("Edit", map[string]any{"file_path": "/work/rux/README.md"}, "ok"),
		evText("README tightened and an **Actions** section added. Pushed `docs`."),
		evDone(95, 5, 0.08),
	), 26*time.Hour)

	// --- wtfc ---
	wm := d.addThread(wtfc, "manager", "", "idle", "main", "sonnet", 30*time.Hour, nil)
	d.reseat(wm, flat(setup(wtfc.Repo), evUser("Cut v0.3.0 once the MCP fixes land."), evText("Will do — waiting on the MCP worker."), evDone(12, 1, 0.04)), 30*time.Hour)
	mcp := d.addThread(wtfc, "", "MCP: validate enums", "running", "mcp-enums", "sonnet", 40*time.Minute, nil)
	d.reseat(mcp, flat(setup(wtfc.Repo),
		evUser("MCP change tool should reject values outside schema enums."),
		tool("Read", map[string]any{"file_path": "/work/wtfc/mcp.go"}, "…"),
		tool("Bash", map[string]any{"command": "go test ./internal/..."}, "--- FAIL: TestEnum (0.00s)"),
		evText("Test reproduces it. Adding enum validation in `change.Validate`."),
	), 40*time.Minute)
	mcp.Parent = wm.ID
	d.addThread(wtfc, "", "Changelog RSS feed", "idle", "rss", "haiku", 50*time.Hour, flat(setup(wtfc.Repo), evUser("Generate an RSS feed from changelog.json"), evText("Added `wtfc feed` writing `feed.xml`."), evDone(140, 7, 0.11)))

	// --- tidepool (a game): screenshots, an action run, a timeline ---
	tw := d.addThread(tide, "", "Crab AI pathing", "running", "crab-ai", "opus", 25*time.Minute, nil)
	game := demoGameShot(480, 300)
	d.reseat(tw, flat(setup(tide.Repo),
		evUser("Crabs get stuck on rocks. Fix pathing and show me a screenshot."),
		tool("Grep", map[string]any{"pattern": "NavigationAgent2D"}, "crab.gd:14"),
		tool("Edit", map[string]any{"file_path": "/work/tidepool/crab.gd"}, "ok"),
		ev("rux_tool", "run", "r1", "tool", "itch-publish", "state", "running", "args", map[string]any{"source": "."}),
		ev("rux_tool", "run", "r1", "log", "butler push build/itch jhuggett/tidepool:mac-crab-ai"),
		ev("rux_tool", "run", "r1", "state", "done", "ms", 48000, "summary", "pushed build 1182 to mac-crab-ai"),
		d.asset(tw.ID, "tidepool-crabs.png", "image/png", game, "crabs now route around rocks"),
		d.asset(tw.ID, "pathing-demo.mp4", "video/mp4", make([]byte, 2_400_000), "30s capture"),
		d.asset(tw.ID, "profile.pdf", "application/pdf", make([]byte, 182_000), "frame profile"),
		ev("rux_ui", "ui", "ui-tl-1", "title", "today's work", "ask", false, "spec", map[string]any{"root": "t", "elements": map[string]any{
			"t": map[string]any{"type": "Timeline", "props": map[string]any{"items": []any{
				map[string]any{"label": "repro", "start": time.Now().Add(-24 * time.Minute).UnixMilli(), "end": time.Now().Add(-18 * time.Minute).UnixMilli(), "status": "done", "lane": "crab-ai"},
				map[string]any{"label": "fix", "start": time.Now().Add(-18 * time.Minute).UnixMilli(), "end": time.Now().Add(-8 * time.Minute).UnixMilli(), "status": "done", "lane": "crab-ai"},
				map[string]any{"label": "publish", "start": time.Now().Add(-8 * time.Minute).UnixMilli(), "end": time.Now().Add(-6 * time.Minute).UnixMilli(), "status": "done", "lane": "itch"},
				map[string]any{"label": "tune", "start": time.Now().Add(-6 * time.Minute).UnixMilli(), "status": "running", "lane": "crab-ai"},
			}}}}}),
		evText("Crabs now route around rocks (screenshot above); build 1182 is on itch as `mac-crab-ai`. Tuning avoidance radius next."),
	), 25*time.Minute)
	d.addThread(tide, "", "Title screen music", "starting", "music", "sonnet", 1*time.Minute, setup(tide.Repo)[:3])

	d.addThread(d.projects[3], "", "Try out htmx", "idle", "", "haiku", 72*time.Hour, flat(evUser("Make a tiny htmx demo page"), evText("Done: `index.html` with a click-to-load list."), evDone(30, 2, 0.02)))
	d.addThread(d.projects[3], "", "Old experiment", "destroyed", "", "haiku", 200*time.Hour, flat(evUser("hello"), evText("hi"), evDone(3, 1, 0.01)))
	d.threads[len(d.threads)-1].Archived = true

	for _, t := range d.threads {
		if t.Status == "running" {
			t.script = workScript(t.Title)
		}
	}
}

// reseat replaces a thread's events, spread over its lifetime.
func (d *demoSource) reseat(t *demoThread, evs []json.RawMessage, ago time.Duration) {
	t.events = nil
	created := d.start.Add(-ago)
	for i, e := range evs {
		frac := math.Pow(float64(i)/float64(max(len(evs), 1)), 0.8)
		ts := created.Add(time.Duration(frac * float64(ago-90*time.Second)))
		t.events = append(t.events, Event{ID: int64(i + 1), TS: ts.UnixMilli(), Src: srcOf(e), Ev: e})
		var p struct{ Type string }
		json.Unmarshal(e, &p)
		if p.Type == "result" {
			t.Replied = ts
		}
	}
	if n := len(t.events); n > 0 {
		t.Updated = time.UnixMilli(t.events[n-1].TS)
	}
}

func workScript(title string) []json.RawMessage {
	files := []string{"tui/monitor.go", "tui/nav.go", "tui/thread.go", "tui/canvas.go", "tui/style.go"}
	var out []json.RawMessage
	for i := 0; i < 400; i++ {
		f := files[i%len(files)]
		switch i % 7 {
		case 0:
			out = append(out, tool("Read", map[string]any{"file_path": "/work/" + f}, "…")...)
		case 1:
			out = append(out, tool("Edit", map[string]any{"file_path": "/work/" + f}, "ok")...)
		case 2:
			out = append(out, tool("Bash", map[string]any{"command": "go build ./... && go vet ./..."}, "")...)
		case 3:
			out = append(out, evText(fmt.Sprintf("Step %d of %q: %s compiles; moving on.", i/7+1, title, f)))
		case 4:
			out = append(out, tool("Grep", map[string]any{"pattern": "func (m \\*model)"}, "12 matches")...)
		case 5:
			out = append(out, tool("Bash", map[string]any{"command": "go test ./tui/..."}, "ok")...)
		case 6:
			out = append(out, evThink("Keep the center column fixed; animate only the sides."))
		}
	}
	return out
}

// loop drives the simulation: running threads emit their script.
func (d *demoSource) loop() {
	tick := time.NewTicker(1400 * time.Millisecond)
	for range tick.C {
		d.mu.Lock()
		for _, t := range d.threads {
			if t.Status == "running" && len(t.script) > 0 && rand.Float64() < 0.7 {
				e := t.script[0]
				t.script = t.script[1:]
				d.appendLocked(t, e)
				// the evResult right after a tool_use comes with it
				if len(t.script) > 0 && strings.Contains(string(t.script[0]), "tool_result") {
					d.appendLocked(t, t.script[0])
					t.script = t.script[1:]
				}
			}
			if t.Status == "starting" && rand.Float64() < 0.05 {
				d.appendLocked(t, ev("rux_ready"))
				d.setStatusLocked(t, "idle")
			}
		}
		d.mu.Unlock()
	}
}

func (d *demoSource) appendLocked(t *demoThread, e json.RawMessage) {
	se := Event{ID: int64(len(t.events)) + 1, TS: time.Now().UnixMilli(), Src: srcOf(e), Ev: e}
	t.events = append(t.events, se)
	t.Updated = time.Now()
	var p struct{ Type string }
	json.Unmarshal(e, &p)
	switch p.Type {
	case "result":
		t.Replied = time.Now()
		d.setStatusLocked(t, "idle")
	case "rux_user":
		d.setStatusLocked(t, "running")
	}
	for _, fn := range t.subs {
		go fn(se)
	}
}

func (d *demoSource) setStatusLocked(t *demoThread, st string) {
	if t.Status == st {
		return
	}
	t.Status = st
	d.appendLocked(t, ev("rux_status", "status", st, "connected", true))
}

func (d *demoSource) find(id string) *demoThread {
	for _, t := range d.threads {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (d *demoSource) Projects(context.Context) ([]Project, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Project
	for _, p := range d.projects {
		v := *p
		v.Activity = p.Updated
		for _, t := range d.threads {
			if t.Project != p.ID || t.Status == "destroyed" {
				if t.Project == p.ID && t.Archived {
					v.ArchivedThreads++
				}
				continue
			}
			if t.Kind == "manager" {
				v.Manager, v.ManagerStatus, v.ManagerReplied = t.ID, t.Status, t.Replied
				continue
			}
			v.Threads++
			if t.Status == "running" || t.Status == "starting" || t.Status == "provisioning" {
				v.Active++
			}
			if t.Updated.After(v.Activity) {
				v.Activity = t.Updated
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Activity.After(out[j].Activity) })
	return out, nil
}

func (d *demoSource) Threads(context.Context) ([]Thread, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Thread
	for _, t := range d.threads {
		out = append(out, t.Thread)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

func (d *demoSource) Page(_ context.Context, id string, before, mn int64) (Page, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.find(id)
	if t == nil {
		return Page{}, errors.New("no such thread")
	}
	n := int64(len(t.events))
	if before <= 0 || before > n+1 {
		before = n + 1
	}
	if mn <= 0 {
		mn = 200
	}
	start := max(before-mn, 1)
	for start > 1 && !strings.Contains(string(t.events[start-1].Ev), `"rux_user"`) {
		start--
	}
	return Page{Start: start, Events: append([]Event(nil), t.events[start-1:before-1]...)}, nil
}

func (d *demoSource) Stream(ctx context.Context, id string, after int64, fn func(Event), caughtUp func()) error {
	d.mu.Lock()
	t := d.find(id)
	if t == nil {
		d.mu.Unlock()
		return errors.New("no such thread")
	}
	backlog := append([]Event(nil), t.events[min(max(after, 0), int64(len(t.events))):]...)
	ch := make(chan Event, 256)
	d.nsubInc(t, ch)
	k := t.nsub
	d.mu.Unlock()
	for _, e := range backlog {
		fn(e)
	}
	caughtUp()
	last := after
	if len(backlog) > 0 {
		last = backlog[len(backlog)-1].ID
	}
	defer func() { d.mu.Lock(); delete(t.subs, k); d.mu.Unlock() }()
	for {
		select {
		case e := <-ch:
			if e.ID > last {
				last = e.ID
				fn(e)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (d *demoSource) nsubInc(t *demoThread, ch chan Event) {
	t.nsub++
	t.subs[t.nsub] = func(e Event) {
		select {
		case ch <- e:
		default:
		}
	}
}

func (d *demoSource) OverviewStream(ctx context.Context, fn func(Overview)) error {
	for {
		fn(d.overview())
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (d *demoSource) overview() Overview {
	d.mu.Lock()
	defer d.mu.Unlock()
	var ov Overview
	ov.At = time.Now().UnixMilli()
	ov.Host.Name, ov.Host.CPUs, ov.Host.MemBytes = "studio", 12, 64<<30
	ov.Host.Load = []float64{2.1 + rand.Float64(), 1.8, 1.6}
	ov.Statuses = map[string]int{}
	ov.Usage.Hourly = make([]int, 24)
	now := time.Now()
	hourStart := now.Truncate(time.Hour).Add(-23 * time.Hour).UnixMilli()
	for h := range ov.Usage.Hourly { // background shape so the chart isn't empty
		ov.Usage.Hourly[h] = int(8 + 6*math.Sin(float64(h)/3.0) + float64(h)*0.6)
	}
	for _, t := range d.threads {
		ov.Statuses[t.Status]++
		for _, e := range t.events {
			if strings.Contains(string(e.Ev), `"tool_use"`) {
				if h := int((e.TS - hourStart) / 3600000); h >= 0 && h < 24 {
					ov.Usage.Hourly[h]++
				}
				ov.Usage.TodayTools++
			}
			if strings.Contains(string(e.Ev), `"type":"result"`) {
				ov.Usage.TodayTurns++
				var r struct {
					Cost float64 `json:"total_cost_usd"`
				}
				json.Unmarshal(e.Ev, &r)
				ov.Usage.TodayCost += r.Cost
			}
		}
		if t.Status != "destroyed" && t.Status != "failed" {
			streaming := t.Status == "running"
			g := OvGuest{Name: t.Guest, Phase: "running", Project: t.Project, ThreadID: t.ID, ThreadTitle: t.Title, Model: t.Model, Status: t.Status,
				Connected: t.Connected, Streaming: streaming, Managed: true, MemLimit: 4 << 30, Procs: 9 + rand.Intn(20)}
			g.CPU = 2 + rand.Float64()*8
			g.MemBytes = int64(600<<20) + rand.Int63n(400<<20)
			if streaming {
				g.CPU = 35 + rand.Float64()*60
				g.MemBytes += 900 << 20
			}
			ov.Guests = append(ov.Guests, g)
		}
	}
	ov.Usage.TodayThreads = 5
	ov.Usage.Windows = append(ov.Usage.Windows,
		struct {
			Name        string  `json:"name"`
			Utilization float64 `json:"utilization"`
			ResetsAt    int64   `json:"resets_at"`
		}{"five_hour", 0.42 + float64(time.Since(d.start).Seconds())/3000, now.Add(2*time.Hour + 14*time.Minute).Unix()},
		struct {
			Name        string  `json:"name"`
			Utilization float64 `json:"utilization"`
			ResetsAt    int64   `json:"resets_at"`
		}{"seven_day", 0.27, now.Add(3*24*time.Hour + 5*time.Hour).Unix()})
	ov.Other = []OvGuest{{Name: "buildkit", Phase: "running", CPU: 0.4, MemBytes: 210 << 20, MemLimit: 2 << 30, Procs: 3}}
	ov.Devices = append(ov.Devices, struct {
		Name    string `json:"name"`
		OS      string `json:"os"`
		Online  bool   `json:"online"`
		Self    bool   `json:"self"`
		Viewing bool   `json:"viewing"`
	}{"iphone", "iOS", true, false, true}, struct {
		Name    string `json:"name"`
		OS      string `json:"os"`
		Online  bool   `json:"online"`
		Self    bool   `json:"self"`
		Viewing bool   `json:"viewing"`
	}{"macbook", "macOS", true, false, false})
	return ov
}

func (d *demoSource) Send(_ context.Context, id, text string, images []Image) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.find(id)
	if t == nil {
		return errors.New("no such thread")
	}
	var urls []string
	for _, im := range images {
		b, _ := base64.StdEncoding.DecodeString(im.Data)
		u := fmt.Sprintf("/api/threads/%s/assets/%03d-%s", id, len(d.assets), im.Name)
		d.assets[u] = b
		urls = append(urls, u)
	}
	d.appendLocked(t, ev("rux_user", "text", text, "images", urls))
	reply := fmt.Sprintf("Got it — %q. (This is the demo source; nothing is really running.)", clip(text, 60))
	if len(images) > 0 {
		reply = fmt.Sprintf("Thanks for the %d image(s). %s", len(images), reply)
	}
	t.script = append(flat(
		evThink("The user said: "+clip(text, 80)),
		tool("Grep", map[string]any{"pattern": "TODO"}, "3 matches"),
		evText(reply),
		evDone(9, 2, 0.05),
	), t.script...)
	return nil
}

func (d *demoSource) SendManager(ctx context.Context, project, text string) (Thread, error) {
	d.mu.Lock()
	var m *demoThread
	for _, t := range d.threads {
		if t.Project == project && t.Kind == "manager" {
			m = t
		}
	}
	if m == nil {
		var p *Project
		for _, pp := range d.projects {
			if pp.ID == project {
				p = pp
			}
		}
		if p == nil {
			d.mu.Unlock()
			return Thread{}, errors.New("no such project")
		}
		m = d.addThread(p, "manager", "", "idle", p.Branch, "", time.Second, setup(p.Repo))
	}
	d.mu.Unlock()
	return m.Thread, d.Send(ctx, m.ID, text, nil)
}

func (d *demoSource) Create(ctx context.Context, project, prompt string) (Thread, error) {
	d.mu.Lock()
	var p *Project
	for _, pp := range d.projects {
		if pp.ID == project {
			p = pp
		}
	}
	if p == nil {
		d.mu.Unlock()
		return Thread{}, errors.New("no such project")
	}
	title := clip(strings.SplitN(prompt, "\n", 2)[0], 80)
	t := d.addThread(p, "", title, "starting", "", "sonnet", time.Second, setup(p.Repo)[:4])
	d.mu.Unlock()
	go func() {
		time.Sleep(3 * time.Second)
		d.mu.Lock()
		d.appendLocked(t, ev("rux_ready"))
		d.setStatusLocked(t, "idle")
		d.mu.Unlock()
		d.Send(ctx, t.ID, prompt, nil)
	}()
	return t.Thread, nil
}

func (d *demoSource) Interrupt(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.find(id)
	if t == nil {
		return errors.New("no such thread")
	}
	t.script = nil
	d.appendLocked(t, ev("rux_interrupt"))
	d.appendLocked(t, ev("result", "subtype", "error_during_execution", "is_error", true, "duration_ms", 1000))
	return nil
}

func (d *demoSource) Destroy(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t := d.find(id); t != nil {
		t.script = nil
		d.setStatusLocked(t, "destroyed")
		t.Connected = false
	}
	return nil
}

func (d *demoSource) Archive(_ context.Context, id string, archived bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t := d.find(id); t != nil {
		t.Archived = archived
		if archived && t.Status != "destroyed" {
			t.script = nil
			d.setStatusLocked(t, "destroyed")
		}
	}
	return nil
}

func (d *demoSource) AnswerUI(_ context.Context, id, ui, action string, params, state map[string]any) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.find(id)
	if t == nil {
		return errors.New("no such thread")
	}
	b, _ := json.Marshal(map[string]any{"action": action, "params": params, "state": state})
	d.appendLocked(t, ev("rux_ui_answer", "ui", ui, "action", action, "state", state))
	d.appendLocked(t, evUser("[rux] The user answered your UI "+ui+": "+string(b)))
	t.script = append(flat(evText(fmt.Sprintf("Noted: **%s** with %v. Briefing a worker.", action, state)), evDone(6, 1, 0.02)), t.script...)
	return nil
}

func (d *demoSource) Asset(_ context.Context, u string) ([]byte, string, error) {
	if strings.HasPrefix(u, "data:") {
		return decodeDataURL(u)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if b, ok := d.assets[u]; ok {
		return b, "", nil
	}
	return nil, "", errors.New("no such asset")
}

func (d *demoSource) AssetURL(u string) string { return "demo:" + u }

func decodeDataURL(u string) ([]byte, string, error) {
	head, data, ok := strings.Cut(u, ",")
	if !ok {
		return nil, "", errors.New("bad data URL")
	}
	mime := strings.TrimSuffix(strings.TrimPrefix(head, "data:"), ";base64")
	b, err := base64.StdEncoding.DecodeString(data)
	return b, mime, err
}

// ---- synthetic images ----

func pngBytes(img image.Image) []byte {
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func fillRect(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	for y := max(y0, 0); y < min(y1, img.Rect.Dy()); y++ {
		for x := max(x0, 0); x < min(x1, img.Rect.Dx()); x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

// demoScreenshot draws something like a screenshot of the TUI itself.
func demoScreenshot(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fillRect(img, 0, 0, w, h, color.RGBA{4, 10, 6, 255})
	fillRect(img, 0, 0, w, 22, color.RGBA{10, 40, 18, 255})
	fillRect(img, 12, 8, 60, 14, color.RGBA{80, 255, 120, 255})
	fillRect(img, 0, h-18, w, h, color.RGBA{10, 30, 14, 255})
	cols := []int{20, w/2 - 110, w - 190}
	for ci, x := range cols {
		n := []int{1, 4, 5}[ci]
		cw := []int{120, 220, 170}[ci]
		for i := 0; i < n; i++ {
			y := h/2 - n*30 + i*60
			bc := color.RGBA{50, 190, 90, 255}
			if ci == 1 && i == 1 {
				bc = color.RGBA{90, 255, 130, 255}
			}
			fillRect(img, x, y, x+cw, y+46, bc)
			fillRect(img, x+2, y+2, x+cw-2, y+44, color.RGBA{6, 18, 10, 255})
			fillRect(img, x+10, y+10, x+10+cw/2, y+20, color.RGBA{190, 255, 200, 255})
			fillRect(img, x+10, y+27, x+10+cw/3, y+35, color.RGBA{80, 180, 100, 255})
		}
	}
	return pngBytes(img)
}

// demoGameShot draws a little tide-pool scene: water, rocks, crabs.
func demoGameShot(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := math.Sin(float64(x)/23+float64(y)/31)*12 + math.Sin(float64(y)/9)*6
			img.SetRGBA(x, y, color.RGBA{uint8(20 + v), uint8(90 + v + float64(y)/6), uint8(140 + v), 255})
		}
	}
	disc := func(cx, cy, r int, c color.RGBA) {
		for y := cy - r; y <= cy+r; y++ {
			for x := cx - r; x <= cx+r; x++ {
				if (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r && x >= 0 && y >= 0 && x < w && y < h {
					img.SetRGBA(x, y, c)
				}
			}
		}
	}
	for _, r := range [][3]int{{120, 110, 38}, {300, 180, 52}, {400, 70, 26}, {60, 240, 30}} {
		disc(r[0], r[1], r[2], color.RGBA{110, 100, 90, 255})
		disc(r[0]-r[2]/4, r[1]-r[2]/4, r[2]/2, color.RGBA{140, 130, 118, 255})
	}
	for _, c := range [][2]int{{200, 120}, {240, 250}, {360, 120}, {90, 170}, {420, 230}} {
		disc(c[0], c[1], 9, color.RGBA{230, 80, 50, 255})
		disc(c[0]-7, c[1]-9, 3, color.RGBA{250, 120, 80, 255})
		disc(c[0]+7, c[1]-9, 3, color.RGBA{250, 120, 80, 255})
	}
	// path line
	for t := 0.0; t < 1; t += 0.002 {
		x := int(200 + 160*t)
		y := int(120 + 60*math.Sin(t*math.Pi))
		disc(x, y, 1, color.RGBA{255, 240, 120, 255})
	}
	return pngBytes(img)
}
