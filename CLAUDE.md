# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Retty means Reconnect TTY / Relay TTY: a native terminal workspace with persistent sessions shared across desktop, iPhone and headless servers. It is inspired by Superlogical's Rex, written in Go using MyGo's native UI (no webview, no cgo on desktop; `purego` loads libghostty-vt). The GUI `main` package builds the desktop and iPhone apps; `cmd/retty` provides the headless CLI with the `retty_cli` build tag. See README.md for user-facing behavior and shortcuts.

## Commands

Read [docs/development.md](docs/development.md) before deployment or device tests;
it is the command reference for production builds, preserving live sessions and
using each phone's existing signing setup.

MyGo's CLI is pinned as a Go tool in `go.mod` (Go 1.27+):

```sh
RETTY_DIR="$PWD/.mygo/dev-data" GOWORK=off go tool mygo dev # isolated development
GOWORK=off go tool mygo build  # production .app and .dmg
GOWORK=off go test ./...       # server + window-less view tests
GOWORK=off go test -run '^TestName$' -count=1 .
GOWORK=off go test -run '^TestName$' -count=1 ./internal/rex
GOWORK=off go vet ./... && GOWORK=off go vet -tags mygo_noinspector ./...
GOWORK=off go test . ./internal/rex ./internal/remote -race
RETTY_DIR="$PWD/.mygo/server-debug" GOWORK=off go run . -server # separate debug server
GOWORK=off go run ./tools/mkicon # redraw resources/icon.png
GOWORK=off ./scripts/build-ios.sh -ios-simulator # iOS (device: -ios-team/-ios-device)
```

`GOWORK=off` pins the MyGo fork from `go.mod`; drop it only when deliberately developing against a local MyGo in `go.work`, after `./scripts/check-mygo.sh` passes. Manual desktop builds need `-tags mygo_noinspector -ldflags '-X github.com/egoist/mygo.production=1'`; a plain `go build` is a MyGo development build even when copied into an installed `.app`. Remote/mobile end-to-end tests are opt-in (`RETTY_REMOTE_E2E=1`, `RETTY_MOBILE_RECOVERY_E2E=1`; see docs/development.md). `MYGO_TEST_IMAGES=<dir>` makes view tests (e.g. `settings_test.go`) write PNG renders of the UI there.

## Protecting live sessions

The installed app's daemon owns the user's real shells and agents. Never batch-kill Retty processes (`pkill`/`killall`/by path: the server, push worker, Codex relays and hook CLIs share the binary), delete `server.sock` or the data dir, run `mygo dev` or `go run . -server` against the real data dir, or restart a live server to activate new code. Quit only the GUI normally; tests attach only to shells they created, never to real Codex/Claude sessions. For iOS, reuse the phone's existing Team/Bundle ID/profile and overwrite-install.

## Architecture

**One binary, many roles** (dispatched by argv in `main.go`, before any AppKit setup):
- the desktop app (window, UI); on `GOOS=ios` it runs `mobileMain` instead (`mobile*.go`);
- `-server`: the session server (`rex.Serve`), a background process that owns the PTYs so shells outlive the app. `rex.Connect` starts it if it isn't running; on macOS it goes through launchd with the user's login-shell environment (`background_darwin.go`), not the caller's environment, so tool-injected variables (e.g. `NO_COLOR`) don't leak into new sessions;
- `-agent-hook <agent>`: a silent, bounded CLI invoked by Claude Code / Codex hooks (via `$RETTY_HOOK`) to report agent lifecycle events to the server. Must never initialize AppKit or spawn a server;
- `-codex-bridge` / `-codex-proxy` / `-codex-watch`: metadata-only observers of Codex app-server threads (`internal/rex/codex_*.go`); they never restart or steer the Codex daemon;
- `-push-service` / `-push-import` / `-push-status`: the APNs notification worker (`internal/push`), separate from the session server.

**Session server / client (`internal/rex`)**: a Unix socket in the data dir. One control connection of JSON lines (`Request`/`Response` in `proto.go`, ops dispatched in `server.go` `do`), plus one connection per attached session carrying raw terminal bytes. Each session keeps a headless libghostty-vt screen and sends a snapshot on attach, so full-screen programs restore exactly. The server also tracks foreground process/cwd (`proc_darwin.go`: `tcgetpgrp`, `sysctl`, `proc_pidinfo`), titles, bells, output activity, and `AgentState` (agent status lives in the server so it survives window reconnects). The saved window layout is stored by the server too (`Layout`/`SetLayout`).
- Bump `ProtocolVersion` in `proto.go` whenever the wire protocol changes. Additive fields can remain compatible with older servers; raise `MinProtocolVersion` for incompatible changes. The app accepts versions in that supported range.
- `staleServer` compares executable path + mtime. Automatic replacement is guarded by development mode and the explicit hot-reload environment. Ordinary production GUI updates must attach to the existing compatible daemon. Never replace a live daemon to activate new code while tasks are running; server replacement ends its sessions.

**App state (`state.go`)**: `App` → `Tab` → binary tree of `Node` splits → `Pane` (a `terminal.Terminal` attached to a server session via `rex.Stream`). `poll` asks the server for `SessionInfo` every 500 ms and `apply`s it (program names, dots, attention, notifications). Tree mutations made while a frame is being built go through `a.later`/`a.post` and run in `runPosted` before the next frame, never inline.

**UI**: MyGo's immediate-mode `ui.View(a.view)`; `view.go` is the root, with `tabs.go`, `host.go`, `commands.go` (menus, palette), `preferences.go` (settings page), `compact.go`, `find.go`. `programs.go` maps foreground programs to names/icons; `agent_status.go` + `internal/agents` handle agent recognition, hook installation into `~/.claude/settings.json` / `~/.codex/hooks.json`, and event → state mapping. `settings.go` persists `settings.json`.

**Remote / iPhone**: `internal/remote` carries the same Rex protocol over Tailcat's encrypted tunnel (identity in `<data dir>/remote/identity.json`; the QR code is a full-access capability). The desktop side is `pairing.go` and `desktop_connections*.go`; the phone side is `mobile*.go` (`mobileApp`). Both reuse the connection lifecycle and `session_view_stream.go`. An active remote pane takes the session's PTY size; the phone does the same when phone sizing is enabled (the default). Inactive views release their size ownership and detach, preserving the session. A view displaced by another device stays detached until explicitly reactivated. Device services (QR scanning, device name, keyboard dismissal, iOS network preparation, accessory modifier latches) come from MyGo's `main`, so Retty itself has no iOS cgo apart from the terminal's libghostty-vt build. XCUITests live in `tests/ios`, driven by `scripts/test-ios.sh`.

**MyGo** is the `daodao97/mygo` fork, required through a `replace` to a pseudo-version of its `main` branch; land MyGo features on that `main`, push, then bump the pseudo-version (`GOWORK=off go mod edit -replace github.com/egoist/mygo=github.com/daodao97/mygo@main && GOWORK=off go mod tidy`). Never pin a feature branch, an unpushed commit or `../mygo`. The `go.work` checkout `../mygo` must stay on `main` at the pinned commit, or workspace builds silently use other MyGo code; `./scripts/check-mygo.sh` verifies both (see "MyGo 依赖" in docs/development.md). Generic iOS native bridges belong in MyGo; only the terminal and its libghostty-vt build stay here.

**`internal/terminal`** is a vendored, locally modified copy of MyGo's terminal plugin (Ghostty search API, match highlighting, copy-on-select, renderer padding changes). When updating MyGo, diff it against the new upstream plugin and carry the local changes forward — see `internal/terminal/UPSTREAM.md`.

## Data directory and debugging

- State (socket, server log, `layout.json`, `settings.json`, `render.log`) lives in `$RETTY_DIR`, defaulting to `~/Library/Application Support/Retty` for the built app and `.../Retty Dev` under `mygo dev`. An inherited `RETTY_DIR` overrides that separation; use an explicit isolated development directory.
- Tests use `newTestApp` (`app_test.go`): a temp `RETTY_DIR`, an in-process `rex.Serve()`, `SHELL=/bin/sh`, and a `ui.Tester` rendering the view at 1000×620 without a window.
- `RETTY_DEBUG=<dir>` lets a script drive the running app: write commands to `<dir>/do` (`shot`, `sleep`, `type`, `size`, `tab`, `focus`, `name`, `close`, `zoom` — see `debug.go`); `shot` writes `<dir>/shot.png`.

## Conventions

Comments are full-sentence prose in the style of the existing code (often explaining *why*, Rex-like behavior referenced as "as Rex does"); match that tone and density.

UI code uses the color, spacing, radius and type tokens in `style.go`, not literal values; [docs/ui-design.md](docs/ui-design.md) says which to use where and which views are still to migrate.
