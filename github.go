package main

import (
	"bufio"
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// GitHub access comes from the host's own `gh` login: the repo picker lists
// what that account can see, and guests fetch the same token on demand through
// a git credential helper (`rux runner git-credential`), so it isn't written
// into the guest's config or command line.
type github struct {
	bin string

	mu       sync.Mutex
	token    string
	tokenAt  time.Time
	ident    *ghIdent
	repos    []ghRepo
	reposAt  time.Time
	reposErr error
	loading  chan struct{} // closed when the in-flight fetch finishes
}

type ghIdent struct {
	Login string `json:"login"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type ghRepo struct {
	Name          string `json:"name"` // owner/name
	Private       bool   `json:"private"`
	Pushed        string `json:"pushed"`
	DefaultBranch string `json:"default_branch"`
	Description   string `json:"description,omitempty"`
}

func (g *github) enabled() bool { return g != nil && g.bin != "" }

func (g *github) cmd(args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	c := exec.Command(g.bin, args...)
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("gh %s: %s", args[0], msg)
		}
		return nil, fmt.Errorf("gh %s: %w", args[0], err)
	}
	return out, nil
}

// credential returns the host's gh token (re-read every few minutes, in case
// gh refreshed it) and the commit identity for that account.
func (g *github) credential() (string, *ghIdent, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token == "" || time.Since(g.tokenAt) > 5*time.Minute {
		out, err := g.cmd("auth", "token", "--hostname", "github.com")
		if err != nil {
			return "", nil, err
		}
		g.token, g.tokenAt = strings.TrimSpace(string(out)), time.Now()
	}
	if g.ident == nil {
		out, err := g.cmd("api", "user", "--jq", `[.id, .login, .name // ""] | @tsv`)
		if err != nil {
			return "", nil, err
		}
		f := strings.Split(strings.TrimSpace(string(out)), "\t")
		if len(f) < 2 {
			return "", nil, fmt.Errorf("gh api user: unexpected output")
		}
		name := f[1]
		if len(f) > 2 && f[2] != "" {
			name = f[2]
		}
		g.ident = &ghIdent{Login: f[1], Name: name, Email: f[0] + "+" + f[1] + "@users.noreply.github.com"}
	}
	return g.token, g.ident, nil
}

// listRepos returns the cached repo list, refreshing it in the background once
// it's a few minutes old. The first call waits for the fetch.
func (g *github) listRepos() ([]ghRepo, error) {
	g.mu.Lock()
	if time.Since(g.reposAt) > 3*time.Minute && g.loading == nil {
		done := make(chan struct{})
		g.loading = done
		go func() { g.fetchRepos(); close(done) }()
	}
	if wait := g.loading; g.repos == nil && wait != nil {
		g.mu.Unlock()
		<-wait
		g.mu.Lock()
	}
	defer g.mu.Unlock()
	return g.repos, g.reposErr
}

func (g *github) fetchRepos() {
	out, err := g.cmd("api", "--paginate", "user/repos?sort=pushed&per_page=100&affiliation=owner,collaborator,organization_member",
		"--jq", `.[] | {name: .full_name, private, pushed: .pushed_at, default_branch, description: (.description // "")}`)
	var repos []ghRepo
	if err == nil {
		sc := bufio.NewScanner(bytes.NewReader(out))
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var r ghRepo
			if json.Unmarshal(sc.Bytes(), &r) == nil && r.Name != "" {
				repos = append(repos, r)
			}
		}
		if repos == nil {
			repos = []ghRepo{}
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.loading = nil
	if err != nil {
		log.Printf("listing GitHub repos: %v", err)
		g.reposErr = err
		if g.repos == nil {
			g.reposAt = time.Time{}
		}
		return
	}
	g.repos, g.reposAt, g.reposErr = repos, time.Now(), nil
}

func (s *server) handleRepos(w http.ResponseWriter, r *http.Request) {
	if !s.gh.enabled() {
		writeJSONResp(w, 200, map[string]any{"enabled": false, "repos": []ghRepo{}})
		return
	}
	repos, err := s.gh.listRepos()
	resp := map[string]any{"enabled": true, "repos": repos}
	if repos == nil {
		resp["repos"] = []ghRepo{}
	}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSONResp(w, 200, resp)
}

// handleGitCredential serves the runner's git credential helper. It's on the
// runner listener and needs the thread's token, like /runner.
func (s *server) handleGitCredential(w http.ResponseWriter, r *http.Request) {
	t := s.get(r.URL.Query().Get("thread"))
	if t == nil {
		http.Error(w, "forbidden", 403)
		return
	}
	t.mu.Lock()
	tok, gone := t.meta.Token, t.meta.Status == "destroyed"
	t.mu.Unlock()
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if gone || subtle.ConstantTimeCompare([]byte(auth), []byte(tok)) != 1 {
		http.Error(w, "forbidden", 403)
		return
	}
	if !s.gh.enabled() {
		http.Error(w, "GitHub access is off (--gh '')", 404)
		return
	}
	token, ident, err := s.gh.credential()
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	writeJSONResp(w, 200, map[string]any{"username": "x-access-token", "password": token,
		"name": ident.Name, "email": ident.Email, "login": ident.Login})
}
