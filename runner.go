package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const defaultAllowedTools = "Bash Edit Write Read Glob Grep WebFetch WebSearch TodoWrite NotebookEdit Task"

// runner lives inside a guest. It owns the claude process (so the server can
// restart without killing a thread), numbers every event, keeps them on disk,
// and replays whatever the server is missing each time it (re)connects.
type runner struct {
	serverURL, thread, token string
	dir, claudeBin, model    string
	allowedTools, stateDir   string

	mu        sync.Mutex
	seq       int64
	lines     [][]byte // lines[i] is the encoded wire event with seq i+1
	logf      *os.File
	out       chan []byte // live queue of the current connection; nil if none
	outCancel context.CancelFunc

	sessionID string
	started   atomic.Bool // claude has persisted the session, so use --resume

	procMu sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
}

func runRunner(args []string) {
	fs := flag.NewFlagSet("runner", flag.ExitOnError)
	r := &runner{}
	fs.StringVar(&r.serverURL, "server", "", "server WebSocket URL, e.g. ws://192.168.64.1:7701/runner")
	fs.StringVar(&r.thread, "thread", "", "thread ID")
	fs.StringVar(&r.token, "token", "", "thread token")
	fs.StringVar(&r.dir, "dir", "", "working directory for claude (default $ROX_PROJECT_DIR, else cwd)")
	fs.StringVar(&r.claudeBin, "claude", "claude", "claude binary")
	fs.StringVar(&r.model, "model", "", "claude --model")
	fs.StringVar(&r.allowedTools, "allowed-tools", defaultAllowedTools, "claude --allowedTools")
	fs.StringVar(&r.stateDir, "state", "", "state directory (default ~/.rux/THREAD)")
	fs.Parse(args)
	if r.serverURL == "" || r.thread == "" || r.token == "" {
		log.Fatal("runner: --server, --thread and --token are required")
	}
	if r.dir == "" {
		r.dir = os.Getenv("ROX_PROJECT_DIR")
	}
	if r.dir == "" {
		r.dir, _ = os.Getwd()
	}
	if r.stateDir == "" {
		home, _ := os.UserHomeDir()
		r.stateDir = filepath.Join(home, ".rux", r.thread)
	}
	if err := r.load(); err != nil {
		log.Fatal(err)
	}
	log.Printf("runner: thread %s, dir %s, session %s, %d stored events", r.thread, r.dir, r.sessionID, r.seq)

	backoff := time.Second
	for {
		connected, err := r.connectOnce(context.Background())
		if connected {
			backoff = time.Second
		}
		log.Printf("runner: disconnected (%v); retrying in %s", err, backoff)
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (r *runner) load() error {
	if err := os.MkdirAll(r.stateDir, 0o700); err != nil {
		return err
	}
	if b, err := os.ReadFile(filepath.Join(r.stateDir, "session")); err == nil {
		f := strings.Fields(string(b))
		if len(f) > 0 {
			r.sessionID = f[0]
			r.started.Store(len(f) > 1 && f[1] == "started")
		}
	}
	if r.sessionID == "" {
		r.sessionID = newUUID()
		r.saveSession()
	}
	f, err := os.OpenFile(filepath.Join(r.stateDir, "events.jsonl"), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), wsReadLimit)
	for sc.Scan() {
		r.lines = append(r.lines, append([]byte(nil), sc.Bytes()...))
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("reading events: %w", err)
	}
	r.seq = int64(len(r.lines))
	r.logf = f
	return nil
}

func (r *runner) saveSession() {
	s := r.sessionID
	if r.started.Load() {
		s += " started"
	}
	os.WriteFile(filepath.Join(r.stateDir, "session"), []byte(s+"\n"), 0o600)
}

// emit records an event and forwards it to the server if connected.
func (r *runner) emit(ev []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	line, _ := json.Marshal(wireMsg{T: "event", Seq: r.seq, Ev: ev})
	r.lines = append(r.lines, line)
	r.logf.Write(append(line, '\n'))
	if r.out != nil {
		select {
		case r.out <- line:
		default: // server too slow: drop the connection, it will resync on reconnect
			r.outCancel()
			r.out, r.outCancel = nil, nil
		}
	}
}

func (r *runner) connectOnce(ctx context.Context) (bool, error) {
	u := fmt.Sprintf("%s?thread=%s&token=%s", r.serverURL, url.QueryEscape(r.thread), url.QueryEscape(r.token))
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	c, _, err := websocket.Dial(dctx, u, nil)
	cancel()
	if err != nil {
		return false, err
	}
	defer c.CloseNow()
	c.SetReadLimit(wsReadLimit)

	cctx, ccancel := context.WithCancel(ctx)
	defer ccancel()
	host, _ := os.Hostname()
	if err := writeJSON(cctx, c, wireMsg{T: "hello", Info: map[string]string{
		"hostname": host, "dir": r.dir, "session_id": r.sessionID,
	}}); err != nil {
		return true, err
	}
	var sync wireMsg
	if err := readJSON(cctx, c, &sync); err != nil {
		return true, err
	}
	if sync.T != "sync" {
		return true, fmt.Errorf("expected sync, got %q", sync.T)
	}

	out := make(chan []byte, 4096)
	r.mu.Lock()
	if r.outCancel != nil {
		r.outCancel()
	}
	have := min(max(sync.Have, 0), r.seq)
	if sync.Have > r.seq {
		log.Printf("runner: server has seq %d but runner only %d (state lost?)", sync.Have, r.seq)
	}
	backlog := r.lines[have:r.seq]
	r.out, r.outCancel = out, ccancel
	r.mu.Unlock()
	log.Printf("runner: connected; replaying %d events", len(backlog))

	errc := make(chan error, 2)
	go func() {
		for _, l := range backlog {
			if err := c.Write(cctx, websocket.MessageText, l); err != nil {
				errc <- err
				return
			}
		}
		for {
			select {
			case l := <-out:
				if err := c.Write(cctx, websocket.MessageText, l); err != nil {
					errc <- err
					return
				}
			case <-cctx.Done():
				errc <- cctx.Err()
				return
			}
		}
	}()
	go func() {
		for {
			var m wireMsg
			if err := readJSON(cctx, c, &m); err != nil {
				errc <- err
				return
			}
			r.handle(m)
		}
	}()
	err = <-errc

	r.mu.Lock()
	if r.out == out {
		r.out, r.outCancel = nil, nil
	}
	r.mu.Unlock()
	return true, err
}

func (r *runner) handle(m wireMsg) {
	switch m.T {
	case "user":
		r.sendUser(m.Text)
	case "interrupt":
		r.procMu.Lock()
		if r.cmd != nil && r.cmd.Process != nil {
			r.cmd.Process.Signal(os.Interrupt)
			r.emit(ruxEv("rux_interrupt"))
		}
		r.procMu.Unlock()
	}
}

func (r *runner) sendUser(text string) {
	r.procMu.Lock()
	defer r.procMu.Unlock()
	if r.cmd == nil {
		if err := r.startClaude(); err != nil {
			r.emit(ruxEv("rux_error", "text", "starting claude: "+err.Error()))
			return
		}
	}
	r.emit(ruxEv("rux_user", "text", text))
	b, _ := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	})
	if _, err := r.stdin.Write(append(b, '\n')); err != nil {
		r.emit(ruxEv("rux_error", "text", "writing to claude: "+err.Error()))
	}
}

// startClaude must be called with procMu held.
func (r *runner) startClaude() error {
	args := []string{"-p",
		"--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		// No approvals UI yet: anything not pre-allowed is denied instead of hanging.
		"--permission-prompts", "none",
	}
	if r.allowedTools != "" {
		args = append(args, "--allowedTools", r.allowedTools)
	}
	if r.model != "" {
		args = append(args, "--model", r.model)
	}
	if r.started.Load() {
		args = append(args, "--resume", r.sessionID)
	} else {
		args = append(args, "--session-id", r.sessionID)
	}
	cmd := exec.Command(r.claudeBin, args...)
	cmd.Dir = r.dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	r.cmd, r.stdin = cmd, stdin
	r.emit(ruxEv("rux_claude_start", "args", strings.Join(args, " "), "dir", r.dir))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), wsReadLimit)
		for sc.Scan() {
			b := append([]byte(nil), sc.Bytes()...)
			if !json.Valid(b) {
				r.emit(ruxEv("rux_stdout", "text", string(b)))
				continue
			}
			r.noteStarted(b)
			r.emit(b)
		}
	}()
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			r.emit(ruxEv("rux_stderr", "text", sc.Text()))
		}
	}()
	go func() {
		wg.Wait()
		err := cmd.Wait()
		r.procMu.Lock()
		if r.cmd == cmd {
			r.cmd, r.stdin = nil, nil
		}
		r.procMu.Unlock()
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		r.emit(ruxEv("rux_exit", "code", cmd.ProcessState.ExitCode(), "error", msg))
	}()
	return nil
}

// noteStarted marks the session as resumable once claude has produced a turn.
func (r *runner) noteStarted(b []byte) {
	if r.started.Load() {
		return
	}
	var e struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(b, &e) == nil && (e.Type == "assistant" || e.Type == "result") {
		r.started.Store(true)
		r.saveSession()
	}
}
