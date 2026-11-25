# rux

Run Claude Code threads in disposable [Rox](https://chrisdmacrae.github.io/rox/) guests, driven from a
phone over Tailscale.

```
phone (PWA) ──HTTPS (tailscale serve)──▶ rux serve        (Mac: web app, thread store, runs `rox up`)
                                          ▲ WebSocket, dialed by the guest
                                     rux runner           (in each guest: owns `claude -p` stream-json)
```

- The runner owns the `claude` process and keeps a numbered event log, so restarting `rux serve`
  doesn't kill a thread. On reconnect it replays whatever the server is missing.
- If `claude` exits, the next message restarts it with `--resume` on the same session.
- Threads are stored in `~/.rux/threads/ID/` (`meta.json` and `events.jsonl`).
- A thread starts from a repo (picked from your GitHub list, `owner/name`, or an HTTPS URL) or from a
  blank workspace (an empty git repo, for starting something new).

## GitHub access

rux uses the host's `gh` login: if `gh auth status` works on the host, private repos work in rux.

- The repo picker lists what that account can see (`gh api user/repos`).
- The runner clones the repo inside the guest. Git there, for the clone and for anything Claude runs,
  uses `rux git-credential`, which fetches the host's token (`gh auth token`) from `rux serve` when
  git needs it. The token isn't written to the guest's disk or git config. Commits use your GitHub
  name and noreply address.
- That token can reach everything your account can and doesn't expire, so treat a guest like your
  own shell. `--gh ''` turns GitHub access off.
- Guests get no SSH keys: rux starts them with `rox up --no-git-agent`, and SSH remotes are rewritten
  to HTTPS.

## Screenshots and files

Claude in a guest is told (via `--append-system-prompt`) that you can't see its screen, and has two
commands for showing you things. Both upload over the runner listener with the thread's token, and
files are stored in `~/.rux/threads/ID/assets/` and shown inline in the thread:

- `rux screenshot URL [--mobile] [--height N] [--caption TEXT]`: headless Chromium (installed with
  `sudo apt-get` on first use), then shared.
- `rux share FILE... [--caption TEXT]`: any file up to 25 MB. Images and videos show inline, and
  anything else is a download.

Images Claude opens with its Read tool also show up in the thread.

## Rich UI, diffs and dashboards

Agents can show UI in their thread, not just text: `rux ui spec.json` renders a
[json-render](https://json-render.dev) style spec with the app's own components (`web/ui.js`):
layout, Stat tiles, Meter, Table, List, Timeline, Chart (line, area, bar, stacked bar, sparkline),
Graph, Code, Diff and Image, plus inputs (Choice, Select, TextInput, Toggle, Button).

- `rux ui --ask spec.json` asks a question. Your answer (the form values and which button) goes back
  to the agent as a message, so you can answer whenever.
- `rux diff [git diff args]` shows a diff with a file browser, unified or split view, and full
  screen. By default it shows uncommitted changes, including new files.
- `rux ui --catalog` prints the format and components; agents read it before first use.
- Managers have a `thread_stats` tool and are told to answer "how's it going?" with a dashboard.

## Projects and managers

Threads are grouped into projects, one per repo; blank threads go in `scratch`. A project has defaults
for new threads (branch, model, effort) and notes that every thread in it gets in its system prompt.

The project page is a conversation with the project's **manager**: a long-lived thread, started by
your first message, with the repo checked out. It runs worker threads through an MCP server that
`rux serve` provides on the runner listener (`/mcp`), scoped to its own project: it can start,
message, read, interrupt and remove workers, view their shared files, and edit the project notes.
When one of its workers finishes a turn or fails, it gets a `[rux] ...` message automatically.
Workers push branches; only you merge.

## Build and run

```sh
make build                      # dist/rux and dist/rux-linux-arm64 (served to guests)
./dist/rux serve --allow-user you@example.com
tailscale serve --bg 7700       # HTTPS on your tailnet -> 127.0.0.1:7700
```

Open the `https://<machine>.<tailnet>.ts.net` URL on your phone, then Share → Add to Home Screen.

### One-time host setup

```sh
brew install container && container system start
claude setup-token && rox config agents claude   # Claude credentials for every guest
```

Guests reach the runner listener at `--advertise`. By default that's the gateway of Apple
`container`'s default network (`container network inspect default`) on port 7701. Only addresses in
`--guest-cidrs` (default: that network's subnet) and loopback may connect, and each thread has its
own token.

### Testing without Rox

`rux serve --backend local` clones the repo on the host and runs the runner there. It is
**unsandboxed** and only for exercising the app.

## Not yet

Approvals and push notifications, screenshots, SQLite, multiple hosts, a scheduler, and an MCP facade.
For now, tools are pre-allowed (`--allowedTools`) and anything else is denied (`--permission-prompts none`).
