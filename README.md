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
