package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// exportDemo writes the demo data as a rux serve data directory
// (projects/ID.json, threads/ID/{meta.json,events.jsonl,assets/}), so the
// real HTTP client can be exercised against `rux serve --data DIR`.
func exportDemo(dir string) error {
	d := newDemoSource()
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(dir, "projects"), 0o700); err != nil {
		return err
	}
	for _, p := range d.projects {
		b, _ := json.MarshalIndent(map[string]any{"id": p.ID, "name": p.Name, "repo": p.Repo, "notes": p.Notes, "created": p.Created, "updated": p.Updated}, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "projects", p.ID+".json"), b, 0o600); err != nil {
			return err
		}
	}
	for _, t := range d.threads {
		td := filepath.Join(dir, "threads", t.ID)
		os.MkdirAll(filepath.Join(td, "assets"), 0o700)
		meta := map[string]any{"id": t.ID, "title": t.Title, "project": t.Project, "kind": t.Kind, "parent": t.Parent, "archived": t.Archived,
			"replied": t.Replied, "repo": t.Repo, "branch": t.Branch, "model": t.Model, "prompt": t.Prompt, "status": t.Status, "guest": t.Guest,
			"backend": "local", "created": t.Created, "updated": t.Updated, "prompt_sent": true, "token": "demo"}
		b, _ := json.MarshalIndent(meta, "", "  ")
		os.WriteFile(filepath.Join(td, "meta.json"), b, 0o600)
		var lines []string
		for _, e := range t.events {
			l, _ := json.Marshal(e)
			lines = append(lines, string(l))
		}
		os.WriteFile(filepath.Join(td, "events.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600)
		prefix := "/api/threads/" + t.ID + "/assets/"
		for u, b := range d.assets {
			if strings.HasPrefix(u, prefix) {
				os.WriteFile(filepath.Join(td, "assets", strings.TrimPrefix(u, prefix)), b, 0o600)
			}
		}
	}
	return nil
}
