package tui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jhuggett/rux/tui/uirender"
)

// A transcript is a list of turns, each started by a user message (or a
// [rux] wake-up). When a turn ends, its intermediate steps fold into one
// line, like the PWA: the reply, files, UI and errors stay visible.

type block struct {
	kind   string // user wake text think step asset ui run err note done setup
	key    string
	text   string
	name   string
	arg    string
	state  string // step: "", ok, err
	detail []string
	images []string // urls (or data: URLs)
	asset  struct {
		url, name, caption, mime string
		size                     int64
		inline                   bool
	}
	ui struct {
		id, title string
		ask       bool
		raw       json.RawMessage
	}
	logs []string
}

type turn struct {
	key     string
	head    *block
	work    []*block
	summary *block
	done    bool
}

var wakeRe = regexp.MustCompile(`^\[rux\] worker \S+ \("(.*?)"\)(?::| )(.*?)(?:\. Its last message|$)`)

func (tv *threadView) buildTurns() []*turn {
	var turns []*turn
	cur := &turn{key: "pre", done: true}
	turns = append(turns, cur)
	steps := map[string]*block{}
	runs := map[string]*block{}
	var setup, stderr *block
	add := func(b *block) { cur.work = append(cur.work, b) }
	for _, se := range tv.events {
		var e map[string]any
		if json.Unmarshal(se.Ev, &e) != nil {
			continue
		}
		str := func(k string) string { s, _ := e[k].(string); return s }
		key := fmt.Sprint(se.ID)
		switch str("type") {
		case "rux_setup":
			if setup == nil {
				setup = &block{kind: "setup", key: "setup" + key, name: "guest", arg: "setting up…"}
				add(setup)
			}
			setup.logs = append(setup.logs, str("text"))
		case "rux_guest":
			if setup != nil && str("name") != "" {
				setup.arg = str("name")
			}
		case "rux_runner_connected":
			if setup != nil {
				setup.state = "ok"
				if setup.arg == "setting up…" {
					setup.arg = cmpOr(str("hostname"), "ready")
				}
			}
		case "rux_preparing":
			setup = &block{kind: "setup", key: "ws" + key, name: "workspace", arg: "cloning " + shortRepo(tv.meta.Repo) + "…"}
			if tv.meta.Repo == "" {
				setup.arg = "creating a blank repo…"
			}
			add(setup)
		case "rux_checkout":
			if setup != nil {
				setup.arg = strings.TrimSpace(str("branch") + "  " + str("dir"))
			}
		case "rux_ready":
			if setup != nil && setup.state == "" {
				setup.state = "ok"
			}
		case "rux_error":
			add(&block{kind: "err", key: key, text: str("text")})
			if setup != nil && setup.state == "" {
				setup.state = "err"
			}
		case "rux_user":
			text := str("text")
			if t, ok := cutPrefix(text, "[rux] "); ok {
				b := &block{kind: "wake", key: "wake" + key, text: t, name: "worker", arg: clip(strings.SplitN(t, "\n", 2)[0], 100)}
				if m := wakeRe.FindStringSubmatch(text); m != nil {
					b.arg = m[1] + " · " + m[2]
				} else if strings.HasPrefix(t, "The user answered") {
					b.name = "answer"
				}
				cur = &turn{key: key, head: b}
			} else {
				b := &block{kind: "user", key: key, text: text}
				if imgs, ok := e["images"].([]any); ok {
					for _, u := range imgs {
						if s, ok := u.(string); ok {
							b.images = append(b.images, s)
						}
					}
				}
				cur = &turn{key: key, head: b}
			}
			turns = append(turns, cur)
			stderr = nil
		case "rux_interrupt":
			add(&block{kind: "note", key: key, text: "stopped"})
		case "rux_claude_start":
			stderr = nil
		case "rux_stderr", "rux_stdout":
			if stderr == nil {
				stderr = &block{kind: "setup", key: "stderr" + key, name: "claude stderr"}
				add(stderr)
			}
			stderr.logs = append(stderr.logs, str("text"))
			stderr.arg = clip(str("text"), 60)
		case "rux_exit":
			if c, _ := e["code"].(float64); c != 0 {
				add(&block{kind: "err", key: key, text: fmt.Sprintf("claude exited (%d)", int(c))})
			}
			for _, t := range turns {
				t.done = true
			}
		case "rux_settings":
			add(&block{kind: "note", key: key, text: "model → " + cmpOr(str("model"), "default") + " · effort " + cmpOr(str("effort"), "default")})
		case "rux_asset":
			if q, _ := e["quiet"].(bool); q {
				continue
			}
			b := &block{kind: "asset", key: "asset" + key}
			b.asset.url, b.asset.name, b.asset.caption, b.asset.mime = str("url"), str("name"), str("caption"), str("mime")
			if s, ok := e["size"].(float64); ok {
				b.asset.size = int64(s)
			}
			b.asset.inline, _ = e["inline"].(bool)
			add(b)
		case "rux_ui":
			b := &block{kind: "ui", key: "ui" + str("ui")}
			b.ui.id, b.ui.title = str("ui"), str("title")
			b.ui.ask, _ = e["ask"].(bool)
			var raw struct {
				Spec json.RawMessage `json:"spec"`
			}
			json.Unmarshal(se.Ev, &raw)
			b.ui.raw = raw.Spec
			add(b)
		case "rux_ui_answer":
			tv.uiAnswered[str("ui")] = cmpOr(str("action"), "answered")
		case "rux_tool":
			r := runs[str("run")]
			if r == nil {
				r = &block{kind: "run", key: "run" + str("run"), name: str("tool")}
				runs[str("run")] = r
				add(r)
			}
			if l := str("log"); l != "" {
				r.logs = append(r.logs, l)
			}
			if a, ok := e["args"].(map[string]any); ok {
				var parts []string
				for k, v := range a {
					parts = append(parts, k+"="+fmt.Sprint(v))
				}
				r.detail = parts
			}
			if st := str("state"); st != "" {
				label := map[string]string{"approval": "waiting for your ok", "running": "running on the host"}[st]
				r.arg = cmpOr(label, st)
				switch st {
				case "done":
					r.state = "ok"
				case "failed", "denied":
					r.state = "err"
				default:
					r.state = ""
				}
				if ms, ok := e["ms"].(float64); ok {
					r.arg += fmt.Sprintf(" · %ds", int(ms/1000))
				}
				if s := str("summary"); s != "" {
					r.text = s
				}
			}
		case "assistant":
			msg, _ := e["message"].(map[string]any)
			parts, _ := msg["content"].([]any)
			for _, p := range parts {
				pm, _ := p.(map[string]any)
				ps := func(k string) string { s, _ := pm[k].(string); return s }
				switch ps("type") {
				case "text":
					if strings.TrimSpace(ps("text")) != "" {
						add(&block{kind: "text", key: key, text: ps("text")})
					}
				case "thinking":
					if ps("thinking") != "" {
						add(&block{kind: "think", key: "think" + key, name: "thinking", text: ps("thinking")})
					}
				case "tool_use":
					in, _ := pm["input"].(map[string]any)
					b := &block{kind: "step", key: "step" + ps("id"), name: strings.TrimPrefix(ps("name"), "mcp__rux__"), arg: argOf(in)}
					js, _ := json.MarshalIndent(in, "", "  ")
					b.detail = strings.Split(clipLines(string(js), 40), "\n")
					steps[ps("id")] = b
					add(b)
				}
			}
		case "user":
			msg, _ := e["message"].(map[string]any)
			parts, _ := msg["content"].([]any)
			for _, p := range parts {
				pm, _ := p.(map[string]any)
				if pm["type"] != "tool_result" {
					continue
				}
				id, _ := pm["tool_use_id"].(string)
				b := steps[id]
				if b == nil {
					continue
				}
				b.state = "ok"
				if isErr, _ := pm["is_error"].(bool); isErr {
					b.state = "err"
				}
				var text []string
				switch c := pm["content"].(type) {
				case string:
					text = append(text, c)
				case []any:
					for _, ci := range c {
						cm, _ := ci.(map[string]any)
						switch cm["type"] {
						case "text":
							s, _ := cm["text"].(string)
							text = append(text, s)
						case "image":
							src, _ := cm["source"].(map[string]any)
							if src["type"] == "base64" {
								mt, _ := src["media_type"].(string)
								data, _ := src["data"].(string)
								b.images = append(b.images, "data:"+mt+";base64,"+data)
							}
						}
					}
				}
				if t := strings.TrimSpace(strings.Join(text, "\n")); t != "" {
					b.detail = append(b.detail, "→")
					b.detail = append(b.detail, strings.Split(clipLines(t, 40), "\n")...)
				}
			}
		case "result":
			var bits []string
			isErr, _ := e["is_error"].(bool)
			if st := str("subtype"); isErr || (st != "" && st != "success") {
				bits = append(bits, cmpOr(st, "error"))
			}
			if d, ok := e["duration_ms"].(float64); ok && d > 0 {
				bits = append(bits, fmt.Sprintf("%ds", int(d/1000)))
			}
			if n, ok := e["num_turns"].(float64); ok && n > 0 {
				bits = append(bits, fmt.Sprintf("%d turn%s", int(n), cond(n == 1, "", "s")))
			}
			if c, ok := e["total_cost_usd"].(float64); ok && c > 0 {
				bits = append(bits, fmt.Sprintf("~$%.2f", c))
			}
			cur.summary = &block{kind: "done", key: key, text: strings.Join(bits, " · ")}
			cur.done = true
		}
	}
	return turns
}

func cutPrefix(s, p string) (string, bool) {
	if strings.HasPrefix(s, p) {
		return s[len(p):], true
	}
	return s, false
}

func clipLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n… %d more lines", len(lines)-n)
	}
	return s
}

func argOf(in map[string]any) string {
	for _, k := range []string{"command", "file_path", "pattern", "url", "query", "title", "description", "thread_id"} {
		if s, ok := in[k].(string); ok && s != "" {
			return s
		}
	}
	for _, v := range in {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// renderLines renders the transcript at width w and records items.
func (tv *threadView) renderLines(w int, compact bool) []string {
	turns := tv.buildTurns()
	var out []string
	tv.items = nil
	mark := func(it item, from int) {
		it.line, it.end = from, len(out)
		tv.items = append(tv.items, it)
	}
	for _, t := range turns {
		if t.head != nil {
			if len(out) > 0 {
				out = append(out, "")
			}
			from := len(out)
			out = append(out, tv.renderBlock(t.head, w, compact)...)
			if t.head.kind == "wake" {
				mark(item{key: t.head.key, kind: "wake"}, from)
			}
			for _, u := range t.head.images {
				from := len(out)
				out = append(out, tv.imageLines(u, w, compact)...)
				mark(item{key: "img" + u, kind: "image", url: u, name: "attachment.png"}, from)
			}
		}
		work := t.work
		if t.done && t.head != nil && !compact {
			// fold the intermediate steps of a finished turn
			var final *block
			for _, b := range work {
				if b.kind == "text" {
					final = b
				}
			}
			var keep, steps []*block
			for _, b := range work {
				switch {
				case b == final, b.kind == "asset", b.kind == "ui", b.kind == "err", b.kind == "run":
					keep = append(keep, b)
				default:
					steps = append(steps, b)
				}
			}
			if len(steps) > 0 {
				fk := "fold" + t.key
				open := tv.isExpanded(fk)
				n := 0
				for _, s := range steps {
					if s.kind == "step" {
						n++
					}
				}
				label := fmt.Sprintf("%d step%s", n, cond(n == 1, "", "s"))
				if more := len(steps) - n; more > 0 {
					label += fmt.Sprintf(" · %d note%s", more, cond(more == 1, "", "s"))
				}
				from := len(out)
				out = append(out, sDim.Render(cond(open, "▾ ", "▸ "))+sFaint.Render(label))
				mark(item{key: fk, kind: "fold"}, from)
				if open {
					work = append(steps, keep...)
					// keep document order
					work = orderLike(t.work, work)
				} else {
					work = keep
				}
			}
		}
		for _, b := range work {
			if compact && (b.kind == "step" || b.kind == "think" || b.kind == "setup") {
				out = append(out, tv.renderBlock(b, w, true)...)
				continue
			}
			from := len(out)
			out = append(out, tv.renderBlock(b, w, compact)...)
			switch b.kind {
			case "step", "think", "setup", "run":
				mark(item{key: b.key, kind: b.kind, name: b.name}, from)
				for _, u := range b.images {
					f2 := len(out)
					out = append(out, tv.imageLines(u, w-2, compact)...)
					mark(item{key: "img" + b.key + u[:min(len(u), 40)], kind: "image", url: u, name: b.name + ".png"}, f2)
				}
			case "asset":
				mark(item{key: b.key, kind: "asset", url: b.asset.url, name: b.asset.name, mime: b.asset.mime}, from)
			case "ui":
				spec, _ := uirender.Parse(b.ui.raw)
				inter := false
				if spec != nil {
					f, bt := uirender.Interactive(spec, tv.stateFor(b.ui.id, spec))
					inter = len(f)+len(bt) > 0
				}
				mark(item{key: b.key, kind: "ui", uiID: b.ui.id, interactive: inter}, from)
			}
		}
		if t.summary != nil && t.summary.text != "" {
			s := " " + t.summary.text + " "
			side := max((min(w, 70)-lipgloss.Width(s))/2, 2)
			out = append(out, sDeep.Render(strings.Repeat("─", side))+sFaint.Render(s)+sDeep.Render(strings.Repeat("─", side)))
		}
	}
	return out
}

func orderLike(all, subset []*block) []*block {
	in := map[*block]bool{}
	for _, b := range subset {
		in[b] = true
	}
	var out []*block
	for _, b := range all {
		if in[b] {
			out = append(out, b)
		}
	}
	return out
}

func wrap(s string, w int) []string {
	return strings.Split(ansi.Wrap(s, max(w, 4), ""), "\n")
}

func (tv *threadView) stateFor(id string, spec *uirender.Spec) map[string]any {
	if st := tv.uiState[id]; st != nil {
		return st
	}
	return spec.State
}

func (tv *threadView) uiSpec(id string) *uirender.Spec {
	for _, se := range tv.events {
		var e struct {
			Type string          `json:"type"`
			UI   string          `json:"ui"`
			Spec json.RawMessage `json:"spec"`
		}
		if json.Unmarshal(se.Ev, &e) == nil && e.Type == "rux_ui" && e.UI == id {
			s, _ := uirender.Parse(e.Spec)
			return s
		}
	}
	return nil
}

func (tv *threadView) imageLines(url string, w int, compact bool) []string {
	mw, mh := min(w-2, 72), 18
	if tv.vh > 0 {
		mh = min(mh, max(6, tv.vh*2/3))
	}
	if compact {
		mw, mh = min(w-2, 40), 8
	}
	return prefixAll(strings.Split(tv.images.block(url, mw, mh), "\n"), "  ")
}

func markdown(text string, w int) []string {
	spec := &uirender.Spec{Root: "t", Elements: map[string]*uirender.Element{"t": {Type: "Text", Props: map[string]any{"text": text}}}}
	return strings.Split(uirender.Render(spec, uirender.Options{Width: w, Color: true}), "\n")
}

func (tv *threadView) renderBlock(b *block, w int, compact bool) []string {
	mark := func(state string) string {
		switch state {
		case "ok":
			return sGreen.Render(" ✓")
		case "err":
			return sErr.Render(" ✗")
		}
		return sWarn.Render(" …")
	}
	line := func(sigil, name, arg, state string) string {
		s := sDim.Render(sigil+" ") + sGreen.Render(name) + "  "
		return s + sFaint.Render(clip(strings.ReplaceAll(arg, "\n", " "), max(w-lipgloss.Width(s)-3, 4))) + mark(state)
	}
	detail := func(lines []string) []string {
		var out []string
		for _, l := range lines {
			for _, wl := range wrap(l, w-4) {
				out = append(out, sDeep.Render("  │ ")+sFaint.Render(wl))
			}
		}
		return out
	}
	switch b.kind {
	case "user":
		var out []string
		out = append(out, sBright.Render("▶ you"))
		for _, l := range wrap(b.text, w-2) {
			out = append(out, sBright.Render("┃ ")+sWhite.UnsetBold().Render(l))
		}
		if compact && len(out) > 4 {
			out = append(out[:4], sFaint.Render("┃ …"))
		}
		return out
	case "wake":
		out := []string{line("#", b.name, b.arg, "ok")}
		if tv.isExpanded(b.key) && !compact {
			out = append(out, detail(strings.Split(b.text, "\n"))...)
		}
		return out
	case "text":
		lines := markdown(b.text, w)
		if compact && len(lines) > 8 {
			lines = append(lines[:8], sFaint.Render("…"))
		}
		return lines
	case "think":
		out := []string{sDim.Render("~ ") + sFaint.Render("thinking")}
		if tv.isExpanded(b.key) && !compact {
			for _, l := range wrap(b.text, w-4) {
				out = append(out, sDeep.Render("  │ ")+lipgloss.NewStyle().Foreground(cFaint).Italic(true).Render(l))
			}
		}
		return out
	case "step":
		out := []string{line("$", b.name, b.arg, b.state)}
		if tv.isExpanded(b.key) && !compact {
			out = append(out, detail(b.detail)...)
		}
		return out
	case "setup":
		st := b.state
		out := []string{line("#", b.name, b.arg, st)}
		if tv.isExpanded(b.key) && !compact {
			out = append(out, detail(b.logs)...)
		}
		return out
	case "run":
		out := []string{line("⚙", b.name, b.arg, b.state)}
		if b.text != "" {
			out = append(out, sDeep.Render("  └ ")+sText.Render(clip(b.text, w-4)))
		}
		if tv.isExpanded(b.key) && !compact {
			out = append(out, detail(append(b.detail, b.logs...))...)
		}
		return out
	case "err":
		var out []string
		for i, l := range wrap(b.text, w-2) {
			out = append(out, sErr.Render(cond(i == 0, "✗ ", "  ")+l))
		}
		return out
	case "note":
		return []string{sFaint.Render("· " + b.text)}
	case "asset":
		a := b.asset
		cap := cmpOr(a.caption, a.name)
		head := sGreen.Render("▣ ") + sText.Render(clip(cap, w-30)) + sFaint.Render("  "+a.name+" · "+fmtBytes(a.size))
		if a.inline && strings.HasPrefix(a.mime, "image/") {
			out := []string{head}
			return append(out, tv.imageLines(a.url, w, compact)...)
		}
		if compact {
			return []string{sGreen.Render("▤ ") + sText.Render(a.name) + sFaint.Render(" · "+fmtBytes(a.size))}
		}
		icon := "▤"
		switch {
		case strings.HasPrefix(a.mime, "video/"):
			icon = "▶"
		case a.mime == "application/pdf":
			icon = "◫"
		}
		cw := min(w-2, 60)
		card := box(cw, 4, sDim, sGreen.Render(icon+" ")+sWhite.Render(clip(a.name, cw-10)), []string{
			" " + sText.Render(clip(cap, cw-4)),
			" " + sFaint.Render(cmpOr(a.mime, "file")+" · "+fmtBytes(a.size)) + "   " + sKey.Render(" o ") + sFaint.Render(" open  ") + sKey.Render(" s ") + sFaint.Render(" save"),
		})
		return strings.Split(card, "\n")
	case "ui":
		spec, err := uirender.Parse(b.ui.raw)
		title := cmpOr(b.ui.title, cond(b.ui.ask, "question", "ui"))
		head := sDim.Render("# ") + sText.Render(title)
		if ans, ok := tv.uiAnswered[b.ui.id]; ok {
			head += sGreen.Render("  ✓ " + ans)
		} else if b.ui.ask {
			head += "  " + sWarn.Render("[needs you]") + sFaint.Render(" enter to answer")
		}
		if err != nil || spec == nil {
			return []string{head, sErr.Render(fmt.Sprintf("  bad spec: %v", err))}
		}
		body := uirender.Render(spec, uirender.Options{Width: w - 2, Color: true, State: tv.uiState[b.ui.id],
			Image: func(src string, iw int) string { return tv.images.block(src, min(iw, 72), 16) },
			Fetch: func(src string) (string, bool) { return tv.images.text(src) }})
		lines := strings.Split(body, "\n")
		if compact && len(lines) > 12 {
			lines = append(lines[:12], sFaint.Render("…"))
		}
		return append([]string{head}, prefixAll(lines, "  ")...)
	}
	return nil
}
