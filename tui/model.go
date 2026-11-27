package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type mode int

const (
	modeNav mode = iota
	modeThread
	modeMonitor
)

const (
	frameEvery = 70 * time.Millisecond
)

type (
	tickMsg time.Time
	pollMsg struct{}
	dataMsg struct {
		projects []Project
		threads  []Thread
		err      error
	}
	toastMsg struct {
		text string
		err  bool
	}
	ovMsg       Overview
	createdMsg  struct{ t Thread }
	asyncMsg    struct{ fn func(*model) tea.Cmd } // run on the UI goroutine
	externalMsg struct{ err error }
)

// prompt is a one-line modal input (new thread, attach a file, confirm).
type prompt struct {
	title, hint string
	input       lineEditor
	confirm     bool // y/n only
	onDone      func(m *model, text string) tea.Cmd
}

type model struct {
	src    Source
	ctx    context.Context
	cancel context.CancelFunc
	msgs   chan tea.Msg

	w, h      int
	frame     int
	projects  []Project
	threads   []Thread
	loaded    bool
	lastErr   string
	lastFetch time.Time

	// navigation
	level        int
	selID        [3]string // selected node id per level (root, project, thread)
	showArchived bool
	navX, navY   springVal // column slide (cells) and sibling move (rows) offsets
	grow         springVal // monitor charts growing in, 0..1
	zoom         *zoom
	t0           time.Time
	activity     map[string][]int64 // thread id -> recent event times (ms)

	mode, prevMode mode
	help           bool
	prompt         *prompt
	toast          string
	toastErr       bool
	toastUntil     time.Time

	th       *threadView
	previews map[string]*preview
	mon      *monitor
	seen     *seenStore
	images   *imageCache
	graphics string // detected protocol name, for the help screen
}

func newModel(src Source, seen *seenStore) *model {
	ctx, cancel := context.WithCancel(context.Background())
	m := &model{src: src, ctx: ctx, cancel: cancel, msgs: make(chan tea.Msg, 512), seen: seen,
		previews: map[string]*preview{}, t0: time.Now(), activity: map[string][]int64{}}
	m.selID[0] = "root"
	m.images = newImageCache(src, m.post)
	m.mon = newMonitor()
	return m
}

// post hands a message to the UI loop from any goroutine.
func (m *model) post(msg tea.Msg) {
	select {
	case m.msgs <- msg:
	case <-m.ctx.Done():
	}
}

func (m *model) listen() tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-m.msgs:
			return chanMsg{msg}
		case <-m.ctx.Done():
			return nil
		}
	}
}

func (m *model) tick() tea.Cmd {
	d := 100 * time.Millisecond
	if m.animating() {
		d = time.Second / animFPS
	}
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) fetch() tea.Cmd {
	src, ctx := m.src, m.ctx
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		ps, err := src.Projects(c)
		if err != nil {
			return dataMsg{err: err}
		}
		ts, err := src.Threads(c)
		return dataMsg{projects: ps, threads: ts, err: err}
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.fetch(), m.tick(), m.listen(), m.startActivity(), tea.Tick(3*time.Second, func(time.Time) tea.Msg { return pollMsg{} }))
}

func (m *model) flash(text string, isErr bool) {
	m.toast, m.toastErr, m.toastUntil = text, isErr, time.Now().Add(4*time.Second)
}

// do runs fn in the background and toasts its error (or ok text).
func (m *model) do(ok string, fn func(ctx context.Context) error) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if err := fn(c); err != nil {
			return toastMsg{err.Error(), true}
		}
		if ok == "" {
			return nil
		}
		return toastMsg{ok, false}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		if m.th != nil {
			m.th.dirty = true
		}
		return m, nil
	case tickMsg:
		m.frame = int(time.Since(m.t0) / frameEvery)
		m.stepAnim()
		if m.toast != "" && time.Now().After(m.toastUntil) {
			m.toast = ""
		}
		return m, m.tick()
	case pollMsg:
		return m, tea.Batch(m.fetch(), tea.Tick(3*time.Second, func(time.Time) tea.Msg { return pollMsg{} }))
	case dataMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, nil
		}
		m.lastErr, m.loaded, m.lastFetch = "", true, time.Now()
		m.projects, m.threads = msg.projects, msg.threads
		m.fixSelection()
		if m.th != nil {
			m.th.syncMeta(m)
		}
		return m, m.wantPreview()
	case toastMsg:
		m.flash(msg.text, msg.err)
		return m, nil
	case createdMsg:
		m.flash("started "+clip(msg.t.Title, 40), false)
		m.selID[1], m.selID[2], m.level = msg.t.Project, msg.t.ID, 2
		return m, m.fetch()
	case asyncMsg:
		return m, msg.fn(m)
	case ovMsg:
		m.mon.update(Overview(msg))
		return m, nil
	case externalMsg:
		if msg.err != nil {
			m.flash(msg.err.Error(), true)
		}
		return m, nil
	case tea.MouseMsg:
		if m.mode == modeThread && m.th != nil {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				return m, m.th.scrollBy(m, 3)
			case tea.MouseButtonWheelDown:
				return m, m.th.scrollBy(m, -3)
			}
		}
		return m, nil
	case tea.KeyMsg:
		return m, m.key(msg)
	}
	if cmd := m.threadMsg(msg); cmd != nil {
		return m, cmd
	}
	return m, nil
}

func (m *model) key(k tea.KeyMsg) tea.Cmd {
	s := k.String()
	if s == "ctrl+c" {
		if m.mode == modeThread && m.th != nil && m.th.focus == focusInput && len(m.th.input.text) > 0 {
			m.th.input.clear()
			return nil
		}
		m.cancel()
		return tea.Quit
	}
	if m.prompt != nil {
		return m.promptKey(k)
	}
	if m.help {
		m.help = false
		return nil
	}
	if m.mode == modeThread && m.th != nil && (m.th.focus == focusInput || m.th.form != nil) {
		return m.th.key(m, k)
	}
	switch s {
	case "q":
		m.cancel()
		return tea.Quit
	case "?":
		m.help = true
		return nil
	case "tab", "m":
		return m.toggleMonitor()
	case "r":
		return m.fetch()
	}
	switch m.mode {
	case modeNav:
		return m.navKey(k)
	case modeThread:
		return m.th.key(m, k)
	case modeMonitor:
		return m.monitorKey(k)
	}
	return nil
}

func (m *model) toggleMonitor() tea.Cmd {
	if m.mode == modeMonitor {
		m.mode = m.prevMode
		m.mon.stop()
		return nil
	}
	m.prevMode, m.mode = m.mode, modeMonitor
	m.grow = springVal{0, 0, 1}
	return m.mon.start(m)
}

func (m *model) promptKey(k tea.KeyMsg) tea.Cmd {
	p := m.prompt
	if p.confirm {
		switch k.String() {
		case "y", "Y", "enter":
			m.prompt = nil
			return p.onDone(m, "y")
		default:
			m.prompt = nil
		}
		return nil
	}
	switch k.String() {
	case "esc":
		m.prompt = nil
		return nil
	case "enter":
		m.prompt = nil
		return p.onDone(m, strings.TrimSpace(p.input.String()))
	}
	p.input.key(k, false)
	return nil
}

// ---- data helpers ----

func (m *model) visibleProjects() []Project {
	var out []Project
	for _, p := range m.projects {
		if p.Archived && !m.showArchived {
			continue
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].ID == "scratch") != (out[j].ID == "scratch") {
			return out[j].ID == "scratch"
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (m *model) projectThreads(pid string) []Thread {
	var out []Thread
	for _, t := range m.threads {
		if t.Project != pid || t.Kind == "manager" {
			continue
		}
		if (t.Archived || t.Status == "destroyed") && !m.showArchived {
			continue
		}
		out = append(out, t)
	}
	// newest first, by creation: positions stay put while threads work
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

func (m *model) thread(id string) (Thread, bool) {
	for _, t := range m.threads {
		if t.ID == id {
			return t, true
		}
	}
	return Thread{}, false
}

func (m *model) project(id string) (Project, bool) {
	for _, p := range m.projects {
		if p.ID == id {
			return p, true
		}
	}
	return Project{}, false
}

func (m *model) unseen(t Thread) bool { return m.seen.unseen(t.ID, t.Replied) }

func (m *model) projectUnseen(p Project) bool {
	for _, t := range m.threads {
		if t.Project == p.ID && !t.Archived && m.unseen(t) {
			return true
		}
	}
	return false
}

func (m *model) fixSelection() {
	ps := m.visibleProjects()
	if len(ps) == 0 {
		m.selID[1], m.selID[2] = "", ""
		if m.level > 0 {
			m.level = 0
		}
		return
	}
	if idx(ps, m.selID[1]) < 0 {
		m.selID[1] = ps[0].ID
	}
	ts := m.projectThreads(m.selID[1])
	if idx(ts, m.selID[2]) < 0 {
		m.selID[2] = ""
		if len(ts) > 0 {
			m.selID[2] = ts[0].ID
		}
	}
	if m.level == 2 && len(ts) == 0 {
		m.level = 1
	}
}

func idx[T Project | Thread](list []T, id string) int {
	for i, v := range list {
		switch x := any(v).(type) {
		case Project:
			if x.ID == id {
				return i
			}
		case Thread:
			if x.ID == id {
				return i
			}
		}
	}
	return -1
}

// ---- view ----

func (m *model) View() string {
	if m.w == 0 {
		return ""
	}
	c := newCanvas(m.w, m.h)
	m.header(c)
	m.footer(c)
	show := m.mode
	if m.zoom != nil && m.zoom.in {
		show = m.zoom.under
	}
	switch show {
	case modeNav:
		m.viewNav(c)
	case modeThread:
		if m.th != nil {
			m.th.view(m, c)
		}
	case modeMonitor:
		m.mon.view(m, c)
	}
	if m.zoom != nil {
		m.drawZoom(c)
	}
	if m.help {
		m.viewHelp(c)
	}
	if m.prompt != nil {
		m.viewPrompt(c)
	}
	if m.toast != "" {
		st, b := sGreen, sMid
		if m.toastErr {
			st, b = sErr, sErr
		}
		t := clip(m.toast, m.w-10)
		w := lipgloss.Width(t) + 4
		c.blit(m.w-w-2, m.h-6, box(w, 3, b, "", []string{" " + st.Render(t)}), 0)
	}
	return c.String()
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func (m *model) header(c *canvas) {
	brand := sBright.Render("▓▒░ RUX ░▒▓")
	tabs := []string{"NAV", "THREAD", "MONITOR"}
	var parts []string
	for i, t := range tabs {
		if mode(i) == m.mode {
			parts = append(parts, sRev.Render(" "+t+" "))
		} else {
			parts = append(parts, sDim.Render(" "+t+" "))
		}
	}
	c.blit(1, 0, brand+"  "+strings.Join(parts, sDeep.Render("│")), 0)
	state := sBright.Render("●") + sGreen.Render(" live")
	if m.lastErr != "" {
		state = sErr.Render("● ") + sErr.Render(clip(m.lastErr, 40))
	} else if !m.loaded {
		state = sWarn.Render("◌ connecting")
	}
	cursor := " "
	if m.frame/7%2 == 0 {
		cursor = sBright.Render("▌")
	}
	right := state + sFaint.Render("  "+m.src.Name()+"  ") + sPale.Render(time.Now().Format("15:04:05")) + cursor
	c.blit(m.w-lipgloss.Width(right)-1, 0, right, 0)
	c.blit(0, 1, sDeep.Render(strings.Repeat("━", m.w)), 0)
}

func (m *model) footer(c *canvas) {
	c.blit(0, m.h-3, sDeep.Render(strings.Repeat("─", m.w)), 0)
	crumbs := m.breadcrumb()
	c.blit(1, m.h-2, crumbs, m.w-2)
	pos := m.positionIndicator()
	if lipgloss.Width(crumbs)+lipgloss.Width(pos)+4 < m.w {
		c.blit(m.w-lipgloss.Width(pos)-1, m.h-2, pos, 0)
	}
	c.blit(1, m.h-1, m.hints(), m.w-2)
}

func (m *model) breadcrumb() string {
	sep := sDim.Render(" ▸ ")
	parts := []string{sGreen.Render("◆ rux")}
	if p, ok := m.project(m.selID[1]); ok && (m.level >= 1 || m.mode == modeThread && m.th != nil && m.th.kind != "root") {
		parts = append(parts, sGreen.Render(p.Name))
	}
	if m.mode == modeThread && m.th != nil {
		switch m.th.kind {
		case "manager":
			parts = append(parts, sWhite.Render("manager"))
		case "thread":
			parts = append(parts, sWhite.Render(clip(m.th.title, 50)))
		}
	} else if t, ok := m.thread(m.selID[2]); ok && m.level == 2 {
		parts = append(parts, sWhite.Render(clip(t.Title, 50)))
	}
	if len(parts) > 0 {
		parts[len(parts)-1] = sWhite.Render(ansi.Strip(parts[len(parts)-1]))
	}
	return strings.Join(parts, sep)
}

func (m *model) positionIndicator() string {
	names := []string{"root", "projects", "threads"}
	var out []string
	for i, n := range names {
		switch {
		case i == m.level && m.mode == modeNav:
			out = append(out, sRev.Render(" "+strings.ToUpper(n)+" "))
		case i == m.level:
			out = append(out, sBright.Render(strings.ToUpper(n)))
		default:
			out = append(out, sDim.Render(n))
		}
	}
	extra := ""
	switch m.level {
	case 1:
		ps := m.visibleProjects()
		extra = fmt.Sprintf(" %d/%d", idx(ps, m.selID[1])+1, len(ps))
	case 2:
		ts := m.projectThreads(m.selID[1])
		extra = fmt.Sprintf(" %d/%d", idx(ts, m.selID[2])+1, len(ts))
	}
	return sDim.Render("◀ ") + strings.Join(out, sDeep.Render(" ─ ")) + sDim.Render(" ▶") + sFaint.Render(extra)
}

func keyHint(k, label string) string { return sKey.Render(" "+k+" ") + sFaint.Render(" "+label+"  ") }

func (m *model) hints() string {
	var h []string
	switch {
	case m.prompt != nil:
		h = []string{keyHint("enter", "ok"), keyHint("esc", "cancel")}
	case m.mode == modeNav:
		h = []string{keyHint("←↓↑→", "move"), keyHint("enter", "open"), keyHint("n", "new thread"), keyHint("tab", "monitor"), keyHint("a", "archived"), keyHint("?", "help"), keyHint("q", "quit")}
	case m.mode == modeThread && m.th != nil:
		h = m.th.hints()
	case m.mode == modeMonitor:
		h = []string{keyHint("↓↑", "select"), keyHint("enter", "open thread"), keyHint("tab", "back"), keyHint("?", "help"), keyHint("q", "quit")}
	}
	return strings.Join(h, "")
}

func (m *model) viewPrompt(c *canvas) {
	p := m.prompt
	w := min(72, m.w-4)
	lines := []string{"", " " + sText.Render(p.hint), ""}
	if p.confirm {
		lines = append(lines, " "+sWarn.Render("y")+sFaint.Render(" confirm   any other key cancels"))
	} else {
		lines = append(lines, " "+sBright.Render("› ")+p.input.render(w-6, m.frame, true))
	}
	lines = append(lines, "")
	b := box(w, len(lines)+2, sBright, sWhite.Render(p.title), lines)
	x, y := (m.w-w)/2, m.h/2-4
	c.fill(x-1, y-1, w+2, len(lines)+4)
	c.blit(x, y, b, 0)
}

func (m *model) viewHelp(c *canvas) {
	sec := func(t string) string { return sBright.Render(t) }
	k := func(keys, what string) string { return "  " + sPale.Render(padRight(keys, 16)) + sText.Render(what) }
	lines := []string{
		sec("NAVIGATION") + sFaint.Render("  — the tree: root → projects → threads; your node stays centered"),
		k("←/h  →/l", "up / down a level (parents left, children right)"),
		k("↑/k  ↓/j", "previous / next sibling   g/G first / last"),
		k("enter", "open: root console · project → its manager · thread"),
		k("n", "new worker thread in the selected project"),
		k("x / z", "remove a thread's guest / archive it"),
		k("a", "show archived and removed"),
		"",
		sec("THREAD") + sFaint.Render("  — log focus, or input focus after i"),
		k("j/k pgup/pgdn", "scroll   g/G top/bottom   (wheel works too)"),
		k("[ ]  or  p n", "select previous / next item (steps, files, UI, images)"),
		k("enter", "item: expand · view image full screen · answer a UI form"),
		k("o / s", "open item externally / save it to ~/Downloads"),
		k("e", "expand / fold all steps"),
		k("i", "write a message (enter sends, alt+enter newline, esc stops)"),
		k("ctrl+o", "attach an image file to the message"),
		k("ctrl+x", "interrupt the agent"),
		k("esc", "back to navigation, same spot"),
		"",
		sec("GLOBAL"),
		k("tab / m", "monitor dashboard (toggle back to where you were)"),
		k("r", "refresh   ?  this help   q / ctrl+c  quit"),
		"",
		sFaint.Render("graphics: " + m.graphics + "  ·  source: " + m.src.Name()),
	}
	w := min(84, m.w-4)
	h := len(lines) + 4
	b := box(w, h, sBright, sWhite.Render("help"), append([]string{""}, prefixAll(lines, " ")...))
	x, y := (m.w-w)/2, max((m.h-h)/2, 2)
	c.fill(x-1, y-1, w+2, h+2)
	c.blit(x, y, b, 0)
}

func prefixAll(lines []string, p string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = p + l
	}
	return out
}

func truncANSI(s string, n int) string { return ansi.Truncate(s, n, "…") }
