package tui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jhuggett/rux/tui/termimg"
	"github.com/jhuggett/rux/tui/uirender"
)

// Thread mode: a full chat view of one agent (a worker, a project manager,
// or the root console), live over SSE.

const (
	focusLog = iota
	focusInput
)

type (
	chanMsg struct{ msg tea.Msg }
	pageMsg struct {
		gen   int
		page  Page
		older bool
		err   error
	}
	evMsg struct {
		gen int
		ev  Event
	}
	caughtUpMsg struct{ gen int }
	sentMsg     struct{ err error }
	managerMsg  struct{ t Thread }
	viewerDone  struct {
		open bool
		url  string
		name string
		err  error
	}
)

var streamGen int

type threadView struct {
	id, kind, projectID, title string
	meta                       Thread
	events                     []Event
	firstID                    int64
	gen                        int
	cancel                     context.CancelFunc
	caughtUp, loadingOlder     bool
	err                        string

	images     *imageCache
	focus      int
	input      lineEditor
	attach     []Image
	scroll     int // lines up from the bottom
	sel        int // selected item, -1 = none
	expanded   map[string]bool
	expandAll  bool
	uiState    map[string]map[string]any
	uiAnswered map[string]string
	form       *formState

	dirty  bool
	cacheW int
	lines  []string
	items  []item
	nevs   int
	vh     int // last viewport height
}

type item struct {
	key, kind   string // kind: fold, step, asset, image, ui, run, setup, wake, think
	line, end   int
	url, name   string
	mime        string
	uiID        string
	interactive bool
}

func (m *model) openThread(kind, id, pid string) tea.Cmd {
	if m.th != nil && m.th.cancel != nil {
		m.th.cancel()
	}
	tv := &threadView{id: id, kind: kind, projectID: pid, images: m.images, sel: -1, dirty: true,
		expanded: map[string]bool{}, uiState: map[string]map[string]any{}, uiAnswered: map[string]string{}}
	switch kind {
	case "root":
		tv.title = "root console"
	case "manager":
		p, _ := m.project(pid)
		tv.title = p.Name + " · manager"
		if id == "" {
			tv.focus = focusInput
		}
	default:
		t, _ := m.thread(id)
		tv.title = t.Title
	}
	tv.syncMeta(m)
	from := m.cardRect()
	if m.mode == modeMonitor {
		from = rect{m.w/2 - 20, m.h / 2, 40, 3}
	}
	m.startZoom(from, m.threadRect(), true, m.mode, tv.title)
	m.th = tv
	m.mode = modeThread
	return tv.start(m)
}

func (tv *threadView) start(m *model) tea.Cmd {
	if tv.id == "" {
		return nil
	}
	streamGen++
	tv.gen = streamGen
	ctx, cancel := context.WithCancel(m.ctx)
	tv.cancel = cancel
	gen, id, src := tv.gen, tv.id, m.src
	go func() {
		pg, err := src.Page(ctx, id, 0, 200)
		m.post(pageMsg{gen: gen, page: pg, err: err})
		var last int64
		if n := len(pg.Events); n > 0 {
			last = pg.Events[n-1].ID
		}
		src.Stream(ctx, id, last, func(e Event) { m.post(evMsg{gen, e}) }, func() { m.post(caughtUpMsg{gen}) })
	}()
	return nil
}

func (tv *threadView) syncMeta(m *model) {
	if tv.kind == "manager" && tv.id == "" {
		if p, ok := m.project(tv.projectID); ok && p.Manager != "" {
			tv.id = p.Manager
			tv.start(m)
		}
	}
	if t, ok := m.thread(tv.id); ok {
		tv.meta = t
		if tv.kind == "thread" {
			tv.title = t.Title
		}
	}
	tv.dirty = true
}

func (tv *threadView) close() {
	if tv.cancel != nil {
		tv.cancel()
	}
}

// threadMsg handles the async messages of thread mode and previews.
func (m *model) threadMsg(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case chanMsg:
		_, cmd := m.Update(msg.msg)
		return tea.Batch(cmd, m.listen())
	case previewMsg:
		p := m.previews[msg.id]
		if p == nil {
			p = &preview{}
			m.previews[msg.id] = p
		}
		p.events, p.at, p.loading = msg.events, time.Now(), false
	case imageLoadedMsg:
		if m.th != nil {
			m.th.dirty = true
		}
	case pageMsg:
		tv := m.th
		if tv == nil || msg.gen != tv.gen {
			return nil
		}
		if msg.err != nil {
			tv.err = msg.err.Error()
			tv.loadingOlder = false
			return nil
		}
		if msg.older {
			tv.events = append(msg.page.Events, tv.events...)
			tv.loadingOlder = false
		} else {
			tv.events = msg.page.Events
		}
		tv.firstID = msg.page.Start
		tv.dirty = true
	case evMsg:
		tv := m.th
		if tv == nil || msg.gen != tv.gen {
			return nil
		}
		if n := len(tv.events); n > 0 && msg.ev.ID <= tv.events[n-1].ID {
			return nil
		}
		tv.events = append(tv.events, msg.ev)
		m.noteEvent(tv.id, msg.ev.TS)
		if tv.scroll > 0 {
			tv.scroll += 0 // keep position; lines are counted from the bottom, so re-anchor below
		}
		tv.dirty = true
		var p struct{ Type string }
		json.Unmarshal(msg.ev.Ev, &p)
		if p.Type == "rux_status" || p.Type == "result" {
			return m.fetch()
		}
	case caughtUpMsg:
		if m.th != nil && msg.gen == m.th.gen {
			m.th.caughtUp = true
			m.seen.mark(m.th.id, m.th.meta.Replied)
		}
	case sentMsg:
		if msg.err != nil {
			m.flash(msg.err.Error(), true)
		}
	case managerMsg:
		if m.th != nil && m.th.kind == "manager" && m.th.id == "" {
			m.th.id = msg.t.ID
			m.th.start(m)
		}
		return m.fetch()
	case viewerDone:
		if msg.err != nil {
			m.flash(msg.err.Error(), true)
		} else if msg.open {
			return m.openExternal(msg.url, msg.name)
		}
	}
	return nil
}

// ---- keys ----

func (tv *threadView) hints() []string {
	if tv.form != nil {
		return []string{keyHint("↓↑", "field"), keyHint("←→ space", "change"), keyHint("enter", "press"), keyHint("esc", "close")}
	}
	if tv.focus == focusInput {
		return []string{keyHint("enter", "send"), keyHint("alt+enter", "newline"), keyHint("ctrl+o", "attach image"), keyHint("ctrl+x", "interrupt"), keyHint("esc", "stop writing")}
	}
	h := []string{keyHint("j/k", "scroll"), keyHint("[ ]", "items"), keyHint("enter", "open"), keyHint("i", "write")}
	if it := tv.selected(); it != nil && (it.kind == "asset" || it.kind == "image") {
		h = append(h, keyHint("o", "open externally"), keyHint("s", "save"))
	}
	return append(h, keyHint("e", "expand"), keyHint("ctrl+x", "interrupt"), keyHint("esc", "back"), keyHint("tab", "monitor"))
}

func (tv *threadView) selected() *item {
	if tv.sel >= 0 && tv.sel < len(tv.items) {
		return &tv.items[tv.sel]
	}
	return nil
}

func (tv *threadView) key(m *model, k tea.KeyMsg) tea.Cmd {
	if tv.form != nil {
		return tv.formKey(m, k)
	}
	s := k.String()
	if s == "ctrl+x" {
		if tv.id == "" {
			return nil
		}
		id := tv.id
		return m.do("interrupt sent", func(ctx context.Context) error { return m.src.Interrupt(ctx, id) })
	}
	if tv.focus == focusInput {
		switch s {
		case "esc":
			tv.focus = focusLog
			return nil
		case "enter":
			return tv.send(m)
		case "ctrl+o":
			m.prompt = &prompt{title: "attach image", hint: "Path to a PNG, JPEG, GIF or WebP (under 5 MB):", onDone: func(m *model, path string) tea.Cmd {
				if path == "" {
					return nil
				}
				if err := tv.addAttachment(path); err != nil {
					m.flash(err.Error(), true)
				} else {
					m.flash("attached "+filepath.Base(path), false)
				}
				return nil
			}}
			return nil
		case "ctrl+r":
			if n := len(tv.attach); n > 0 {
				tv.attach = tv.attach[:n-1]
			}
			return nil
		}
		tv.input.key(k, true)
		return nil
	}
	switch s {
	case "esc", "backspace":
		tv.close()
		m.mode = modeNav
		m.startZoom(m.threadRect(), m.cardRect(), false, modeNav, tv.title)
		return m.wantPreview()
	case "i", "a":
		if tv.kind == "root" {
			m.flash("there is no global manager to talk to yet", true)
			return nil
		}
		tv.focus = focusInput
	case "j", "down":
		return tv.scrollBy(m, -1)
	case "k", "up":
		return tv.scrollBy(m, 1)
	case "pgdown", "ctrl+d", " ":
		return tv.scrollBy(m, -max(tv.vh-3, 1))
	case "pgup", "ctrl+u":
		return tv.scrollBy(m, max(tv.vh-3, 1))
	case "G", "end":
		tv.scroll, tv.sel = 0, -1
		tv.dirty = true
	case "g", "home":
		return tv.scrollBy(m, 1<<20)
	case "]", "n":
		tv.moveSel(1)
	case "[", "p":
		tv.moveSel(-1)
	case "e":
		tv.expandAll = !tv.expandAll
		tv.expanded = map[string]bool{}
		tv.dirty = true
	case "enter":
		return tv.activate(m)
	case "o":
		if it := tv.selected(); it != nil && it.url != "" {
			return m.openExternal(it.url, it.name)
		}
	case "s":
		if it := tv.selected(); it != nil && it.url != "" {
			return m.saveAsset(it.url, it.name)
		}
	}
	return nil
}

func (tv *threadView) scrollBy(m *model, d int) tea.Cmd {
	maxScroll := max(len(tv.lines)-tv.vh, 0)
	tv.scroll = min(max(tv.scroll+d, 0), maxScroll)
	if tv.scroll >= maxScroll && tv.firstID > 1 && !tv.loadingOlder && tv.id != "" {
		tv.loadingOlder = true
		gen, id, before, src, ctx := tv.gen, tv.id, tv.firstID, m.src, m.ctx
		return func() tea.Msg {
			pg, err := src.Page(ctx, id, before, 200)
			return pageMsg{gen: gen, page: pg, older: true, err: err}
		}
	}
	if tv.scroll == 0 && tv.caughtUp {
		m.seen.mark(tv.id, tv.meta.Replied)
	}
	return nil
}

// moveSel selects the previous/next item, starting from what's on screen.
func (tv *threadView) moveSel(d int) {
	if len(tv.items) == 0 {
		return
	}
	if tv.sel < 0 {
		// start from the bottom-most visible item
		bottom := len(tv.lines) - tv.scroll
		tv.sel = 0
		for i, it := range tv.items {
			if it.line < bottom {
				tv.sel = i
			}
		}
		if d > 0 && tv.scroll == 0 {
			tv.sel = len(tv.items) - 1
		}
	} else {
		tv.sel = min(max(tv.sel+d, 0), len(tv.items)-1)
	}
	tv.dirty = true
	tv.ensureVisible()
}

func (tv *threadView) ensureVisible() {
	it := tv.selected()
	if it == nil || tv.vh == 0 {
		return
	}
	top := len(tv.lines) - tv.vh - tv.scroll
	if it.line < top {
		tv.scroll = len(tv.lines) - tv.vh - it.line
	} else if it.line >= top+tv.vh {
		tv.scroll = max(len(tv.lines)-it.line-tv.vh/2, 0)
	}
	tv.scroll = min(max(tv.scroll, 0), max(len(tv.lines)-tv.vh, 0))
}

func (tv *threadView) activate(m *model) tea.Cmd {
	it := tv.selected()
	if it == nil {
		if tv.kind != "root" {
			tv.focus = focusInput
		}
		return nil
	}
	switch it.kind {
	case "fold", "step", "setup", "wake", "think", "run":
		tv.expanded[it.key] = !tv.isExpanded(it.key)
		tv.dirty = true
	case "image":
		return m.viewImage(it.url, it.name)
	case "asset":
		if strings.HasPrefix(it.mime, "image/") {
			return m.viewImage(it.url, it.name)
		}
		return m.openExternal(it.url, it.name)
	case "ui":
		spec := tv.uiSpec(it.uiID)
		if spec == nil {
			return nil
		}
		if !it.interactive {
			m.flash("this UI has nothing to answer", false)
			return nil
		}
		st := tv.uiState[it.uiID]
		if st == nil {
			st = uirender.CloneState(spec.State)
		}
		tv.form = newForm(it.uiID, spec, uirender.CloneState(st))
	}
	return nil
}

func (tv *threadView) isExpanded(key string) bool {
	if v, ok := tv.expanded[key]; ok {
		return v
	}
	return tv.expandAll
}

func (tv *threadView) addAttachment(path string) error {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[2:])
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(b) > 5<<20 {
		return fmt.Errorf("%s is over 5 MB", filepath.Base(path))
	}
	mime := map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp"}[strings.ToLower(filepath.Ext(path))]
	if mime == "" {
		return fmt.Errorf("attach PNG, JPEG, GIF or WebP images")
	}
	if len(tv.attach) >= 10 {
		return fmt.Errorf("at most 10 images per message")
	}
	tv.attach = append(tv.attach, Image{Name: filepath.Base(path), Mime: mime, Data: base64.StdEncoding.EncodeToString(b)})
	return nil
}

func (tv *threadView) send(m *model) tea.Cmd {
	text := strings.TrimSpace(tv.input.String())
	if text == "" && len(tv.attach) == 0 {
		return nil
	}
	imgs := tv.attach
	tv.input.clear()
	tv.attach = nil
	tv.scroll, tv.sel = 0, -1
	src, ctx := m.src, m.ctx
	if tv.kind == "manager" && tv.id == "" {
		if len(imgs) > 0 {
			m.flash("images go with later messages; the first one starts the manager", true)
		}
		pid := tv.projectID
		return func() tea.Msg {
			t, err := src.SendManager(ctx, pid, text)
			if err != nil {
				return sentMsg{err}
			}
			return managerMsg{t}
		}
	}
	id := tv.id
	return func() tea.Msg { return sentMsg{src.Send(ctx, id, text, imgs)} }
}

// ---- external viewers ----

func (m *model) viewImage(url, name string) tea.Cmd {
	e := m.images.get(url)
	if e == nil || e.img == nil {
		m.images.load(url, true)
		m.flash("image still loading…", false)
		return nil
	}
	b := e.img.Bounds()
	v := termimg.NewViewer(e.img, fmt.Sprintf("%s  %dx%d", name, b.Dx(), b.Dy()), termimg.Detect())
	return tea.Exec(v, func(err error) tea.Msg { return viewerDone{open: v.OpenRequested, url: url, name: name, err: err} })
}

func (m *model) openExternal(url, name string) tea.Cmd {
	src, ctx := m.src, m.ctx
	return func() tea.Msg {
		b, _, err := src.Asset(ctx, url)
		if err != nil {
			return toastMsg{err.Error(), true}
		}
		p, err := termimg.SaveTemp(safeName(name), b)
		if err != nil {
			return toastMsg{err.Error(), true}
		}
		if err := termimg.OpenExternal(p); err != nil {
			return toastMsg{"saved " + p + " (couldn't open: " + err.Error() + ")", true}
		}
		return toastMsg{"opened " + p, false}
	}
}

func (m *model) saveAsset(url, name string) tea.Cmd {
	src, ctx := m.src, m.ctx
	return func() tea.Msg {
		b, _, err := src.Asset(ctx, url)
		if err != nil {
			return toastMsg{err.Error(), true}
		}
		dir, _ := os.UserHomeDir()
		if d := filepath.Join(dir, "Downloads"); dirExists(d) {
			dir = d
		} else if wd, err := os.Getwd(); err == nil {
			dir = wd
		}
		p := filepath.Join(dir, safeName(name))
		for i := 1; fileExists(p); i++ {
			ext := filepath.Ext(name)
			p = filepath.Join(dir, fmt.Sprintf("%s-%d%s", strings.TrimSuffix(safeName(name), ext), i, ext))
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return toastMsg{err.Error(), true}
		}
		return toastMsg{"saved " + p, false}
	}
}

func dirExists(p string) bool  { st, err := os.Stat(p); return err == nil && st.IsDir() }
func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// ---- view ----

func (tv *threadView) view(m *model, c *canvas) {
	r := m.threadRect()
	top, bot := r.y+1, r.y+r.h-1
	cw := r.w - 2
	x0 := r.x + 1

	// title bar
	glyph, gst := statusGlyph(tv.meta.Status, tv.meta.Offline(), m.frame)
	if tv.kind == "root" {
		glyph, gst = "◆", sBright
	}
	title := gst.Render(glyph) + " " + sWhite.Render(clip(tv.title, cw/2))
	if tv.kind == "manager" {
		title = sBright.Render("⌘ ") + title
	}
	info := ""
	if tv.meta.ID != "" {
		bits := []string{statusText(tv.meta)}
		if tv.meta.Repo != "" {
			bits = append(bits, shortRepo(tv.meta.Repo)+cond(tv.meta.Branch != "", " @ "+tv.meta.Branch, ""))
		}
		if tv.meta.Model != "" {
			bits = append(bits, shortModel(tv.meta.Model))
		}
		info = sFaint.Render(strings.Join(bits, " · "))
	}
	c.fill(r.x, r.y, r.w, r.h)
	if tv.focus == focusLog && tv.form == nil {
		c.blit(r.x, r.y, glowBox(r.w, r.h, m.phase(), title, nil), 0)
	} else {
		c.blit(r.x, r.y, box(r.w, r.h, sMid, title, nil), 0)
	}
	if w := lipgloss.Width(info); w < cw-lipgloss.Width(title)-8 {
		c.blit(r.x+r.w-w-4, r.y, " "+info+" ", 0)
	}

	// input box
	inH := 3
	if tv.focus == focusInput {
		inH = min(2+max(len(tv.input.renderLines(cw-6, 6, m.frame, true)), 1), 8)
	}
	if tv.kind == "root" {
		inH = 0
	}
	logBot := bot - inH
	vh := logBot - top
	tv.vh = vh
	if tv.dirty || tv.cacheW != cw-3 {
		tv.rebuild(m, cw-3)
	}
	// log
	c.fill(x0, top, cw, vh)
	start := max(len(tv.lines)-vh-tv.scroll, 0)
	for i := 0; i < vh && start+i < len(tv.lines); i++ {
		c.blit(x0+2, top+i, tv.lines[start+i], cw-2)
	}
	if it := tv.selected(); it != nil {
		for y := it.line; y < it.end; y++ {
			if yy := y - start; yy >= 0 && yy < vh {
				c.blit(x0, top+yy, sBright.Render("▌"), 0)
			}
		}
	}
	if start > 0 {
		more := "▲ more above"
		if tv.loadingOlder {
			more = "loading earlier…"
		}
		c.blit(x0+cw-lipgloss.Width(more)-1, top, sDim.Render(more), 0)
	}
	if tv.scroll > 0 {
		c.blit(x0+cw-14, logBot-1, sRev.Render(" ↓ newer ↓ "), 0)
	}
	if len(tv.lines) == 0 {
		msg := "loading…"
		switch {
		case tv.err != "":
			msg = "error: " + tv.err
		case tv.kind == "manager" && tv.id == "":
			msg = "No manager yet. Your first message starts one, with the repo checked out; it runs workers for this project."
		}
		c.blit(x0+2, top+1, sFaint.Render(clip(msg, cw-4)), 0)
	}
	if inH == 0 {
		tv.viewChrome(m, c, r)
		return
	}
	// input
	border := sDim
	label := sFaint.Render("i to write")
	var body []string
	if tv.focus == focusInput {
		border = sBright
		label = sWhite.Render("message")
		body = tv.input.renderLines(cw-6, inH-2, m.frame, true)
		body[0] = sBright.Render("› ") + body[0]
		for i := 1; i < len(body); i++ {
			body[i] = "  " + body[i]
		}
	} else {
		hint := "write to " + map[string]string{"manager": "the manager", "thread": "this thread"}[tv.kind]
		if s := tv.input.String(); s != "" {
			hint = clip(strings.ReplaceAll(s, "\n", "↵"), cw-8)
		}
		body = []string{sDim.Render("› ") + sFaint.Render(hint)}
	}
	if len(tv.attach) > 0 {
		var names []string
		for _, a := range tv.attach {
			names = append(names, a.Name)
		}
		label += sWarn.Render(" ⊕ " + clip(strings.Join(names, ", "), cw/2))
	}
	c.blit(x0+1, logBot, box(cw-2, inH, border, label, prefixAll(body, " ")), 0)

	tv.viewChrome(m, c, r)
	if tv.form != nil {
		tv.viewForm(m, c)
	}
}

func cond(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}

// rebuild re-renders the transcript into lines and selectable items.
func (tv *threadView) rebuild(m *model, w int) {
	selKey := ""
	if it := tv.selected(); it != nil {
		selKey = it.key
	}
	oldLen := len(tv.lines)
	if tv.kind == "root" {
		tv.lines, tv.items = rootConsole(m, w), nil
	} else {
		tv.lines = tv.renderLines(w, false)
	}
	// keep the reader's position when new lines arrive below
	if tv.scroll > 0 && len(tv.lines) > oldLen && len(tv.events) == tv.nevs+(len(tv.events)-tv.nevs) {
		tv.scroll += len(tv.lines) - oldLen
	}
	tv.nevs = len(tv.events)
	tv.sel = -1
	for i, it := range tv.items {
		if it.key == selKey && selKey != "" {
			tv.sel = i
		}
	}
	tv.cacheW, tv.dirty = w, false
	if tv.scroll == 0 && tv.caughtUp && m.mode == modeThread {
		m.seen.mark(tv.id, tv.meta.Replied)
	}
}

// renderAll renders without selection, for previews.
func (tv *threadView) renderAll(w int, compact bool) []string { return tv.renderLines(w, compact) }

func rootConsole(m *model, w int) []string {
	logo := []string{
		"█▀▀▄ █  █ ▀▄ ▄▀",
		"█▄▄▀ █  █   █  ",
		"█  █ ▀▄▄▀ ▄▀ ▀▄",
	}
	var out []string
	for i, l := range logo {
		st := []lipgloss.Style{sBright, sGreen, sMid}[i]
		out = append(out, st.Render(l))
	}
	out = append(out, "")
	for _, l := range strings.Split(ansi.Wrap("rux has no overall manager agent yet: each project has its own manager (enter on a project), which runs that project's workers. This console is the root of the tree; it summarizes what needs you across every project.", w, ""), "\n") {
		out = append(out, sText.Render(l))
	}
	out = append(out, "", sBright.Render("NEEDS YOU"))
	n := 0
	for _, t := range m.threads {
		if t.Archived || t.Status == "destroyed" {
			continue
		}
		why := ""
		switch {
		case t.Status == "failed":
			why = sErr.Render("failed")
		case m.unseen(t):
			why = sWarn.Render("new reply")
		case t.Offline():
			why = sFaint.Render("offline")
		}
		if why == "" {
			continue
		}
		n++
		p, _ := m.project(t.Project)
		g, gs := statusGlyph(t.Status, t.Offline(), m.frame)
		out = append(out, fmt.Sprintf("  %s %s  %s  %s", gs.Render(g), sWhite.Render(clip(t.Title, 40)), why, sFaint.Render(p.Name+" · "+ago(t.Updated))))
	}
	if n == 0 {
		out = append(out, sFaint.Render("  nothing — all quiet"))
	}
	out = append(out, "", sBright.Render("PROJECTS"))
	for _, p := range m.visibleProjects() {
		mgr := sFaint.Render("no manager")
		if p.Manager != "" {
			g, gs := statusGlyph(p.ManagerStatus, p.ManagerStatus == "offline", m.frame)
			mgr = gs.Render(g) + sText.Render(" manager "+statusText(Thread{Status: p.ManagerStatus, Connected: p.ManagerStatus != "offline"}))
		}
		out = append(out, fmt.Sprintf("  %s  %s  %s", sPale.Render(padRight(clip(p.Name, 26), 26)), mgr, sFaint.Render(fmt.Sprintf("%d threads · %d active · %s", p.Threads, p.Active, ago(p.Activity)))))
	}
	out = append(out, "")
	for _, l := range wrap("Missing for a real global manager: a top-level agent thread and API (e.g. /api/manager) that can see and drive every project's manager.", w) {
		out = append(out, sFaint.Render(l))
	}
	return out
}

// viewChrome draws the status line on the frame's bottom edge and, on wide
// terminals, the files & UI side panel.
func (tv *threadView) viewChrome(m *model, c *canvas, r rect) {
	// status line
	var cost float64
	turns, files := 0, 0
	for _, se := range tv.events {
		var e struct {
			Type string  `json:"type"`
			Cost float64 `json:"total_cost_usd"`
		}
		if json.Unmarshal(se.Ev, &e) == nil {
			switch e.Type {
			case "result":
				turns++
				cost += e.Cost
			case "rux_asset":
				files++
			}
		}
	}
	pos := "end"
	if n := len(tv.lines) - tv.vh; n > 0 && tv.scroll > 0 {
		pos = fmt.Sprintf("%d%%", 100-tv.scroll*100/n)
	}
	bits := []string{fmt.Sprintf("%d events", len(tv.events)), fmt.Sprintf("%d turns", turns)}
	if files > 0 {
		bits = append(bits, fmt.Sprintf("%d files", files))
	}
	if cost > 0 {
		bits = append(bits, fmt.Sprintf("~$%.2f", cost))
	}
	if tv.meta.Effort != "" {
		bits = append(bits, "effort "+tv.meta.Effort)
	}
	bits = append(bits, pos)
	if !tv.caughtUp && tv.id != "" {
		bits = append(bits, "syncing…")
	}
	st := " " + strings.Join(bits, " · ") + " "
	if lipgloss.Width(st) < r.w-6 {
		c.blit(r.x+r.w-lipgloss.Width(st)-3, r.y+r.h-1, sFaint.Render(st), 0)
	}
	if tv.focus == focusInput {
		c.blit(r.x+3, r.y+r.h-1, sBright.Render(" ✎ writing "), 0)
	}

	// side panel
	sp := m.sidePanelW()
	if sp == 0 {
		return
	}
	x := r.x + r.w + 1
	var lines []string
	sel := tv.selected()
	if sel != nil && (sel.kind == "image" || strings.HasPrefix(sel.mime, "image/")) {
		lines = append(lines, prefixAll(strings.Split(tv.images.block(sel.url, sp-4, 12), "\n"), " ")...)
		lines = append(lines, "")
	}
	n := 0
	for i, it := range tv.items {
		var icon, name string
		switch it.kind {
		case "asset":
			icon, name = "▤", it.name
			if strings.HasPrefix(it.mime, "image/") {
				icon = "▣"
			} else if strings.HasPrefix(it.mime, "video/") {
				icon = "▶"
			}
		case "image":
			icon, name = "▣", it.name
		case "ui":
			icon, name = "#", it.uiID
			if spec := tv.uiTitle(it.uiID); spec != "" {
				name = spec
			}
			if it.interactive {
				if _, done := tv.uiAnswered[it.uiID]; !done {
					name += " " + sWarn.Render("?")
				}
			}
		case "run":
			icon, name = "⚙", cmpOr(it.name, strings.TrimPrefix(it.key, "run"))
		default:
			continue
		}
		n++
		row := " " + sGreen.Render(icon) + " " + sText.Render(clip(name, sp-6))
		if i == tv.sel {
			row = sBright.Render("▌") + sGreen.Render(icon) + " " + sWhite.Render(clip(name, sp-6))
		}
		lines = append(lines, row)
	}
	if n == 0 {
		lines = append(lines, " "+sFaint.Render("no files or UI yet"))
	}
	lines = append(lines, "", " "+sFaint.Render("[ ] select · enter open"), " "+sFaint.Render("o open externally · s save"))
	c.fill(x, r.y, sp, r.h)
	c.blit(x, r.y, box(sp, r.h, sDim, sWhite.Render("files & ui")+sFaint.Render(fmt.Sprintf(" · %d", n)), lines), 0)
}

func (tv *threadView) uiTitle(id string) string {
	for _, se := range tv.events {
		var e struct {
			Type, UI, Title string
		}
		if json.Unmarshal(se.Ev, &e) == nil && e.Type == "rux_ui" && e.UI == id {
			return e.Title
		}
	}
	return ""
}
