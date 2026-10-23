---
name: retty-control
description: Operate Retty terminals and browser panes through its CLI, including reading, authorized input, splits, GUI actions and task status when coordinating work in Retty.
---

Use the installed `retty` executable; if it is absent from PATH inside Retty, use `"$RETTY_HOOK"` as the executable. In a Retty terminal, `RETTY_SESSION` is the caller's stable session ID, `RETTY_AGENT_SOCKET` selects its owning service, and `RETTY_DIR` selects its GUI namespace. Outside Retty, select `--data-dir PATH` or `--socket PATH`; do not start or replace a server to make a read work. Run `retty session help` for current flags.

Read before controlling an existing terminal:

1. `retty list --json` identifies sessions. Use the full returned ID, or an unambiguous prefix of at least four characters; titles and row numbers are not identities.
2. `retty read ID --lines 200 --json` returns text, size and `output` version without attaching or resizing. Inspect it to confirm the intended terminal and its current activity.
3. For authorized input, use `retty send ID --text 'literal text' --enter --if-output N`, with the returned output version when the target should remain unchanged. Omit `--enter` to avoid submitting. Use `--stdin` for exact multiline data, or `--key ctrl-c` for an authorized interruption. Sending writes once; after any transport error, read again before deciding whether to send more.

Prefer a separate session for new work: `retty new --dir /absolute/project --json -- COMMAND ARG...` preserves argv and starts the process directly. `retty wait ID --until exit --exit --json` propagates its real exit status; timeout is 124 and cancellation is 130. `wait --until done|blocked|idle` waits for the corresponding explicit Agent state or shell state. A line sent to an existing shell has no per-command exit status: read its output or start a direct process instead. Do not treat an elapsed timeout as completion.

`retty split ID --direction right|down --json -- COMMAND ARG...` requires the desktop GUI for that local service. The result is the new session ID. `retty actions --json` lists stable action IDs and whether CLI invocation is allowed; use `retty action ACTION_ID --target ID`. Close, restart, end-all, clear and update actions remain GUI-only. Do not substitute another destructive command when an action is denied.

Scripts can report explicit state without injecting terminal escapes:

```sh
retty report --state working --id build/tests --app builder --progress 40
retty report --state blocked --id build/tests --kind question --message 'Choose a test target'
retty report --state done --id build/tests --message 'Tests passed'
```

Use `--state error` for failure and `retty report --state clear --id PARENT` to remove a task and its descendants. Reports replace the whole record; include progress/title/message again when they should remain. These reports share Retty's OSC 7501 state and notification deduplication; native Agent lifecycle hooks continue to operate. Report actual outcomes, not intent. Do not change unrelated hooks, skills or service configuration as part of session control.

For the desktop's browser panes, run `retty browser help`. Use `browser list --json` to identify persistent `web-…` IDs and available remote host IDs. `browser open URL --beside SESSION_OR_BROWSER_ID` splits next to that pane and inherits its host; without `--beside`, it opens a tab. To choose another connected host, use the returned host ID with `--host`. A loopback URL on a remote pane uses that host's authenticated forwarded port; it must never be replaced by a local browser URL on connection failure.

Read with `retty browser read WEB_ID --json` to get bounded visible text and CSS targets. `browser click WEB_ID --selector CSS` and `browser type WEB_ID --selector CSS --text TEXT` require one visible enabled element and run once without retry. Typing replaces the field; it does not implicitly submit. Keep the user's authorization for the action itself, especially form submissions. DOM control activates the selected browser pane. After a navigation or uncertain result, read again before more input.

Use `browser wait WEB_ID --selector CSS --text TEXT --timeout 10s` to wait for visible content (maximum two minutes), and `browser screenshot WEB_ID --output /absolute/page.png` for the actual page PNG. `browser navigate WEB_ID URL` changes that pane's URL. `browser close WEB_ID` closes only that browser/WebView, leaving all terminal processes alive. Browser operations require the owning desktop GUI and accept `--data-dir`; `--socket` is for session commands. Do not infer a browser target from the focused terminal or use a terminal session ID to close a browser.
