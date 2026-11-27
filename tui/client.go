package tui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The TUI's view of the rux serve JSON API (see server.go, projects.go,
// overview.go). Field names follow the server's JSON.

type Thread struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Project   string    `json:"project"`
	Kind      string    `json:"kind,omitempty"`
	Parent    string    `json:"parent,omitempty"`
	Archived  bool      `json:"archived,omitempty"`
	Replied   time.Time `json:"replied,omitempty"`
	Repo      string    `json:"repo"`
	Branch    string    `json:"branch"`
	Model     string    `json:"model,omitempty"`
	Effort    string    `json:"effort,omitempty"`
	Prompt    string    `json:"prompt"`
	Status    string    `json:"status"`
	Guest     string    `json:"guest"`
	Backend   string    `json:"backend"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
	Connected bool      `json:"connected"`
}

// Offline mirrors the PWA: a thread that should have a runner but doesn't.
func (t Thread) Offline() bool {
	return !t.Connected && (t.Status == "idle" || t.Status == "running")
}

type Project struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Repo            string    `json:"repo,omitempty"`
	Branch          string    `json:"branch,omitempty"`
	Model           string    `json:"model,omitempty"`
	Effort          string    `json:"effort,omitempty"`
	Notes           string    `json:"notes,omitempty"`
	Archived        bool      `json:"archived,omitempty"`
	Tools           []string  `json:"tools,omitempty"`
	Created         time.Time `json:"created"`
	Updated         time.Time `json:"updated"`
	Threads         int       `json:"threads"`
	Active          int       `json:"active"`
	Activity        time.Time `json:"activity"`
	ArchivedThreads int       `json:"archived_threads"`
	Manager         string    `json:"manager,omitempty"`
	ManagerStatus   string    `json:"manager_status,omitempty"`
	ManagerReplied  time.Time `json:"manager_replied,omitempty"`
}

// Event is one stored event: {id, ts, src, ev}.
type Event struct {
	ID  int64           `json:"id"`
	TS  int64           `json:"ts"`
	Src string          `json:"src"`
	Ev  json.RawMessage `json:"ev"`
}

type Page struct {
	Start  int64   `json:"start"`
	Events []Event `json:"events"`
}

type Overview struct {
	At   int64 `json:"at"`
	Host struct {
		Name     string    `json:"name"`
		CPUs     int       `json:"cpus"`
		MemBytes int64     `json:"mem_bytes"`
		Load     []float64 `json:"load"`
	} `json:"host"`
	Guests  []OvGuest `json:"guests"`
	Devices []struct {
		Name    string `json:"name"`
		OS      string `json:"os"`
		Online  bool   `json:"online"`
		Self    bool   `json:"self"`
		Viewing bool   `json:"viewing"`
	} `json:"devices"`
	Usage struct {
		Windows []struct {
			Name        string  `json:"name"`
			Utilization float64 `json:"utilization"`
			ResetsAt    int64   `json:"resets_at"`
		} `json:"windows"`
		TodayTurns   int     `json:"today_turns"`
		TodayCost    float64 `json:"today_cost"`
		TodayTools   int     `json:"today_tools"`
		TodayThreads int     `json:"today_threads"`
		Hourly       []int   `json:"hourly"`
	} `json:"usage"`
	Statuses map[string]int `json:"statuses"`
	Other    []OvGuest      `json:"other"`
}

type OvGuest struct {
	Name        string  `json:"name"`
	Phase       string  `json:"phase"`
	Project     string  `json:"project"`
	CPU         float64 `json:"cpu"`
	MemBytes    int64   `json:"mem_bytes"`
	MemLimit    int64   `json:"mem_limit"`
	Procs       int     `json:"procs"`
	ThreadID    string  `json:"thread_id,omitempty"`
	ThreadTitle string  `json:"thread_title,omitempty"`
	Model       string  `json:"model,omitempty"`
	Status      string  `json:"status,omitempty"`
	Connected   bool    `json:"connected"`
	Streaming   bool    `json:"streaming"`
	Managed     bool    `json:"managed"`
}

type Image struct{ Name, Mime, Data string } // Data is base64

// Source is everything the TUI needs from rux serve. The HTTP client talks
// to a real server; the demo source fakes one in memory.
type Source interface {
	Name() string
	Projects(ctx context.Context) ([]Project, error)
	Threads(ctx context.Context) ([]Thread, error)
	Page(ctx context.Context, id string, before, min int64) (Page, error)
	// Stream sends events after `after` until ctx ends; caughtUp is called
	// once the backlog has been sent.
	Stream(ctx context.Context, id string, after int64, ev func(Event), caughtUp func()) error
	OverviewStream(ctx context.Context, fn func(Overview)) error
	Send(ctx context.Context, id, text string, images []Image) error
	SendManager(ctx context.Context, project, text string) (Thread, error)
	Create(ctx context.Context, project, prompt string) (Thread, error)
	Interrupt(ctx context.Context, id string) error
	Destroy(ctx context.Context, id string) error
	Archive(ctx context.Context, id string, archived bool) error
	AnswerUI(ctx context.Context, id, ui, action string, params, state map[string]any) error
	Asset(ctx context.Context, url string) ([]byte, string, error)
	AssetURL(url string) string // absolute URL for opening in a browser
}

// ---- HTTP client ----

type httpSource struct {
	base string
	hc   *http.Client // short requests
	sc   *http.Client // streams
}

func newHTTPSource(base string) *httpSource {
	return &httpSource{base: strings.TrimRight(base, "/"), hc: &http.Client{Timeout: 30 * time.Second}, sc: &http.Client{}}
}

func (h *httpSource) Name() string { return h.base }

func (h *httpSource) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func (h *httpSource) Projects(ctx context.Context) (out []Project, err error) {
	return out, h.do(ctx, "GET", "/api/projects", nil, &out)
}

func (h *httpSource) Threads(ctx context.Context) (out []Thread, err error) {
	return out, h.do(ctx, "GET", "/api/threads", nil, &out)
}

func (h *httpSource) Page(ctx context.Context, id string, before, min int64) (p Page, err error) {
	q := url.Values{}
	if before > 0 {
		q.Set("before", strconv.FormatInt(before, 10))
	}
	if min > 0 {
		q.Set("min", strconv.FormatInt(min, 10))
	}
	return p, h.do(ctx, "GET", "/api/threads/"+url.PathEscape(id)+"/events/page?"+q.Encode(), nil, &p)
}

// sse reads a text/event-stream, calling fn for each event.
func (h *httpSource) sse(ctx context.Context, path string, fn func(event, id, data string)) error {
	req, err := http.NewRequestWithContext(ctx, "GET", h.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := h.sc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var event, id string
	var data strings.Builder
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if data.Len() > 0 || event != "" {
				fn(event, id, strings.TrimSuffix(data.String(), "\n"))
			}
			event, id = "", ""
			data.Reset()
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "id:"):
			id = strings.TrimSpace(line[3:])
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(line[5:], " "))
			data.WriteByte('\n')
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

func (h *httpSource) Stream(ctx context.Context, id string, after int64, ev func(Event), caughtUp func()) error {
	last := after
	for backoff := time.Second; ; {
		err := h.sse(ctx, fmt.Sprintf("/api/threads/%s/events?after=%d", url.PathEscape(id), last), func(event, _, data string) {
			if event == "caughtup" {
				caughtUp()
				return
			}
			var e Event
			if json.Unmarshal([]byte(data), &e) == nil && e.ID > last {
				last = e.ID
				ev(e)
			}
		})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_ = err
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		backoff = min(backoff*2, 15*time.Second)
	}
}

func (h *httpSource) OverviewStream(ctx context.Context, fn func(Overview)) error {
	for {
		h.sse(ctx, "/api/overview/stream", func(_, _, data string) {
			var ov Overview
			if json.Unmarshal([]byte(data), &ov) == nil {
				fn(ov)
			}
		})
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (h *httpSource) Send(ctx context.Context, id, text string, images []Image) error {
	return h.do(ctx, "POST", "/api/threads/"+url.PathEscape(id)+"/messages", map[string]any{"text": text, "images": images}, nil)
}

func (h *httpSource) SendManager(ctx context.Context, project, text string) (t Thread, err error) {
	return t, h.do(ctx, "POST", "/api/projects/"+url.PathEscape(project)+"/manager", map[string]any{"text": text}, &t)
}

func (h *httpSource) Create(ctx context.Context, project, prompt string) (t Thread, err error) {
	return t, h.do(ctx, "POST", "/api/threads", map[string]any{"project": project, "prompt": prompt}, &t)
}

func (h *httpSource) Interrupt(ctx context.Context, id string) error {
	return h.do(ctx, "POST", "/api/threads/"+url.PathEscape(id)+"/interrupt", map[string]any{}, nil)
}

func (h *httpSource) Destroy(ctx context.Context, id string) error {
	return h.do(ctx, "POST", "/api/threads/"+url.PathEscape(id)+"/destroy", map[string]any{}, nil)
}

func (h *httpSource) Archive(ctx context.Context, id string, archived bool) error {
	return h.do(ctx, "POST", "/api/threads/"+url.PathEscape(id)+"/archive", map[string]any{"archived": archived}, nil)
}

func (h *httpSource) AnswerUI(ctx context.Context, id, ui, action string, params, state map[string]any) error {
	return h.do(ctx, "POST", "/api/threads/"+url.PathEscape(id)+"/ui/"+url.PathEscape(ui)+"/answer",
		map[string]any{"action": action, "params": params, "state": state}, nil)
}

func (h *httpSource) AssetURL(u string) string {
	if strings.HasPrefix(u, "/") {
		return h.base + u
	}
	return u
}

func (h *httpSource) Asset(ctx context.Context, u string) ([]byte, string, error) {
	if strings.HasPrefix(u, "data:") {
		return decodeDataURL(u)
	}
	if !strings.HasPrefix(u, "/api/") {
		return nil, "", errors.New("only assets served by rux are fetched")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", h.base+u, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := h.hc.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return b, resp.Header.Get("Content-Type"), err
}
