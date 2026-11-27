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

## Actions

Actions are jobs that run on the rux host (your Mac) and that agents in guests can trigger: building a Mac app, publishing to itch.io, anything that needs the Mac's tooling or secrets the guests mustn't see. Think of it as CI/CD on your own machine that your agents drive.

An action is a folder `~/.rux/actions/NAME/` with a `tool.json` and a script. `actions/` in this repo has examples (`cp -R actions/itch-publish ~/.rux/actions/`).

```json
{
  "description": "what it does, for the agent",
  "args": [{"name": "source", "type": "file", "description": "..."}],
  "settings": [{"name": "target", "description": "set per project in the app"}],
  "secrets": ["BUTLER_API_KEY"],
  "approval": "ask",
  "timeout": "30m",
  "run": "./run.sh"
}
```

- Actions are off until you turn them on in a project's settings (⋯ → settings → actions), where you also fill in their settings.
- In a guest, `rux action` lists the enabled actions and `rux action NAME --source . ...` runs one. `file` arguments (a file or folder) are uploaded to the host. For a git checkout, only the files git keeps are sent.
- `approval`: `ask` (the default) shows a card in the thread with Run it, Always allow here, and Deny. `every-run` always asks, and `none` never asks.
- Secrets come from `~/.rux/secrets.env` (`KEY=value` lines, keep it `chmod 600`). Only the secrets an action lists are passed to it, and their values are scrubbed from its output.
- The script runs in the action's folder with `RUX_ARG_<NAME>`, `RUX_SETTING_<NAME>`, `RUX_GIT_BRANCH`, `RUX_GIT_COMMIT`, `RUX_PROJECT`, `RUX_REPO` and `RUX_BIN` (the rux binary), plus these paths:
  - `RUX_OUT`: files written here are shared into the thread.
  - `RUX_RESULT`: a short summary written here goes back to the agent.
  - `RUX_STATE`: a folder that persists between runs.
- Output streams into the thread. A run that's waiting for approval or takes longer than `--wait` (default 90s) reports back to the agent as a `[rux]` message when it ends.
- Actions run as you. Anything that runs code from a guest, like a build, must go through `rux sandbox DIR -- CMD`. That's a macOS sandbox with no network, no Apple Events or system services, and nothing in your home folder readable or writable except DIR.

`itch-publish` builds the game on the Mac with the project's build command (default `make dist`, sandboxed) and pushes the output (default `build/itch`) with butler. Each branch goes to its own channel (`mac-BRANCH`), and the main branch goes to `mac`. It can also push a finished build with `--build`. Set `target` (`user/game`) in the project's settings, and put `BUTLER_API_KEY` in `~/.rux/secrets.env`; `butler login` saves one in `~/Library/Application Support/itch/butler_creds`.

## Terminal UI

`rux tui [--server URL]` is a terminal client for the same API as the app (default `http://127.0.0.1:7700`, or `$RUX_SERVER`; a tailnet `https://…ts.net` URL works too). `rux tui --demo` runs it against a built-in fake server with sample projects, threads and live activity.

- **Navigation**: a spatial tree, root → projects → threads. The node you're on stays centered; parents are to the left, children to the right. Arrows or hjkl move, enter opens.
- **Thread mode**: the agent's transcript with folded steps, inline images (half-block rendering; enter shows one full screen with Kitty, iTerm2 or Sixel graphics when the terminal supports them), file cards, `rux ui` specs rendered natively (`tui/uirender`) and answerable forms. `i` writes a message, ctrl+o attaches an image, ctrl+x interrupts.
- **Monitor** (tab): usage, threads, host, 24h activity, per-thread activity lanes and guests.
- `?` lists every key.

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
