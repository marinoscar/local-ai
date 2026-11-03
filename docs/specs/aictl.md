# Spec: `aictl` command-line interface

> **Status:** approved design · **Epic:** E1 · **Code:** `agent/internal/cli/` (same binary as the agent, multi-call) · **Doctor area:** `LAI-CLI`
> Canonical names: [overview.md](../architecture/overview.md) §3. API: [agent.md](agent.md) §3.8. Modes: [modes.md](../architecture/modes.md).

## 1. Purpose and scope

`aictl` is how humans and scripts operate a node from a terminal. It is a **thin client of the agent API** for everything that changes node state, plus a few **local commands** that must work even when the agent is down or that need root on the machine itself (builds, PIN, token, backups, lint, docs).

Design goals:
1. **Plain language.** Output reads like instructions for a non-expert: what happened, and what to do next. Every problem carries a doctor code (§3.3).
2. **Scriptable.** `--json` gives stable machine output; exit codes are documented (§3.4).
3. **Works offline.** No command needs the internet except those that are explicitly about downloading (`download`, `ark sync`, `catalog refresh`, `update`, `cloud enroll`, `rebuild-llama` without a local source).
4. **One code path.** API-backed commands call the same agent routes as the desktop, the Portal and the cloud.

### Non-goals
- No interactive TUI. (Progress bars on a TTY are the only dynamic output.)
- No remote nodes: `aictl` talks to the agent on **this** machine only. The lab-wide view is the cloud.
- No configuration editor: `node.yaml` changes go through specific commands (`profile set`, `cloud enroll`) or a text editor.

## 2. Concepts
| Term | Meaning |
|---|---|
| **API command** | Calls the agent over the socket (or TCP fallback). Fails with exit 3 if the agent is down |
| **Local command** | Runs in the `aictl` process itself (same Go packages as the agent). Marked **[local]** |
| **Root command** | Must run as uid 0 (`sudo aictl …`). Marked **[root]** |
| **Admin command** | Needs admin on the API: root over the socket, or the admin PIN (§3.2.3). Marked **[admin]** |

## 3. Interfaces

### 3.1 Invocation and global flags
```
aictl [global flags] <command> [subcommand] [flags] [args]
aiagent ctl [global flags] <command> …        (same, when the /usr/local/bin/aictl symlink is missing)
```

| Flag | Default | Meaning |
|---|---|---|
| `--json` | off | Machine output on stdout (§3.5). Disables colour, spinners and prompts (prompts become errors) |
| `--lang en\|es` | `$LANG` (`es*` → es) else en | Language of human output (sent as `Accept-Language`) |
| `--no-color` | off | Also implied by `NO_COLOR` set, `TERM=dumb`, or stdout not a TTY |
| `-y`, `--yes` | off | Answer "yes" to confirmations |
| `-q`, `--quiet` | off | Only errors (stderr) and the essential result line |
| `-v`, `--verbose` | off | Extra detail: absolute times, ids, request ids, raw codes for passed checks |
| `--socket <path>` | `/run/localai/agent.sock` | Unix socket |
| `--url <url>` | `http://127.0.0.1:7070` | TCP fallback base URL (must be loopback) |
| `--token-file <path>` | `$LOCALAI_TOKEN_FILE` else `/etc/localai/agent-token` | Token for the TCP fallback |
| `--timeout <dur>` | per command (§4) | Overall deadline (e.g. `90s`, `10m`) |
| `--pin-stdin` | off | Read the admin PIN from stdin (one line) instead of prompting |
| `-h`, `--help` | | Help for the command |

Environment: `LOCALAI_SOCKET`, `LOCALAI_URL`, `LOCALAI_TOKEN_FILE` override the defaults (flags win). The PIN is **never** accepted as a flag or environment variable (it would leak through shell history and `/proc/<pid>/environ`).

**Parsing rules (AICTL-01).** Global flags come **before** the command name; a flag after it belongs to the command (stdlib `flag`, one `FlagSet` per command). Command flags may appear before, between or after positional arguments; `--` ends the flags. `-y` and `--yes` (and the other short forms) are the same flag; `--timeout 0` means no deadline; a negative `--timeout`, an unknown `--lang`, an unknown option or a missing option value is `LAI-CLI-005` / exit 2. `aictl help [command [subcommand]]` and `-h`/`--help` print help (exit 0); `aictl` with no command prints the same help on stderr and exits 2; help lists every command with its marks (`[local]`, `[root]`, `[admin]`) and default timeout. `--url` is validated (http, `127.0.0.1`, `localhost` or `[::1]`, no credentials, no path) when the client is first needed, never for a `[local]` command, so offline recovery works with any transport setting; a bad value is `LAI-CLI-005` / exit 2 and no request is made, even when the socket would have worked. A command whose synopsis shows `--yes` or `--pin-stdin` **after** the command name (`catalog use <dir> --test --yes`, `backup-config --restore <file> --yes`, `pin set --pin-stdin`) declares that flag itself and folds it into the global one, so both positions work for it; `-y` is accepted there too.

### 3.2 Transport and authentication

#### 3.2.1 Default: unix socket
`aictl` connects to `/run/localai/agent.sock` (root:localai-operators 0660). The agent identifies the caller by `SO_PEERCRED`: uid 0 is admin; members of `localai-operators` are operators. No token is sent.

#### 3.2.2 Fallback: TCP + token file
Used when the socket path does not exist, connect fails with `ECONNREFUSED`/`ENOENT`, or the socket is not accessible (`EACCES`) **and** the token file is readable:
- `http://127.0.0.1:7070` with `Authorization: Bearer <contents of token file, trimmed>` and `Host: 127.0.0.1:7070`.
- If neither works: exit 3 `LAI-CLI-001` (agent not reachable) or exit 4 `LAI-CLI-002` (permission: socket `EACCES` and token unreadable).
- `-v` prints which transport was used (`via socket` / `via tcp`).
- On `503 LAI-AGT-024` (agent starting/stopping) the CLI retries every 1 s for up to 10 s before failing with exit 3.

#### 3.2.3 Admin and the PIN
For an **[admin]** command:
1. If `geteuid() == 0` and the socket works → the agent already sees admin.
2. Otherwise the CLI sends the request; on `403` with `details.pin_required: true` it asks `Admin PIN: ` on the TTY (no echo, `golang.org/x/term`), calls `POST /pin/verify`, and retries once with `X-LocalAI-Pin-Grant`. With `--pin-stdin` it reads one line from stdin instead. With `--json` or no TTY and no `--pin-stdin` → exit 4 `LAI-CLI-008`.
3. Wrong PIN → `LAI-SEC-003`, up to 3 prompts per command; rate limit → `LAI-SEC-004` with the wait time.

**[root]** commands check `geteuid() == 0` first and fail with exit 4 `LAI-CLI-006` ("Run it with sudo: `sudo aictl …`").

#### 3.2.4 Version skew
Every API command reads `GET /health` once (cached for the process). If the agent's version differs from the CLI's (possible for a few seconds during self-update, or when a recovery binary is used) → warning `LAI-CLI-003` on stderr; the command continues. Unknown fields in responses are ignored; a response that cannot be decoded at all → exit 1 `LAI-CLI-009`.

#### 3.2.5 Client behaviour (AICTL-01)
Implemented in `agent/internal/cli` (`Client`, `Config`, `Registry`, `ExitFor`); later commands only register themselves (`cli.Register(&cli.Command{...})`) and return errors.
- **Transport choice is made once per process**, on the first request, which is the `/health` call of §3.2.4. Any failure to *connect* to the socket (missing path, nobody listening, `EACCES`) falls back to TCP **if** the token file is readable and not empty; the token file is read only then. If TCP also fails to connect, the socket error decides: `EACCES`/`EPERM` is `LAI-CLI-002` (exit 4), anything else `LAI-CLI-001` (exit 3). A socket error with an unreadable token gives the same two results.
- **Retry window.** A `503` whose code is `LAI-AGT-024` is retried every second for up to 10 s, then `LAI-CLI-001` (exit 3) with `details.reason: "starting"`. A `503` with any other code is not retried and is exit 3 with the agent's own envelope. After the agent has answered once in this process, a lost connection (it is restarting) is retried in the same window; the very first contact is never retried, so an agent that is down fails at once.
- **Deadline.** The command's deadline (`--timeout`, else its default) covers `/health`, retries and the call itself; expiry is `LAI-CLI-004` (exit 7). Ctrl-C cancels the request, never the operation, and is `LAI-CLI-010` (exit 130).
- **Requests.** `Accept-Language` carries the language; every mutating request (`POST`, `PUT`, `PATCH`, `DELETE`) carries `Content-Type: application/json` and the body `{}` when there is none; reads carry no body. The TCP client ignores the proxy environment and never follows redirects. A body over 32 MiB is `LAI-CLI-009`.
- **Errors.** A non-2xx answer is decoded into `APIError{Status, Code, Message, Fix, Details, RequestID, OpID}`; one that is not the agent's envelope (no `error.code`) is `LAI-CLI-009` (exit 1) and the request id is printed. `ExitFor(err)` is the single mapping to exit codes; a command returns `ExitWith(8)` or `ExitWith(9)` for results that are not failures.
- **Output of the framework's own problems.** Human mode follows §3.3 (message, two spaces, `[code]`, then `→ Fix:` / `→ Solución:`; `-v` adds details and the request id; the request id is always shown for `LAI-CLI-009`). With `--json` a failure is the envelope on stdout and stderr stays empty. Warnings (`LAI-CLI-003`) go to stderr after the command, except with `--quiet`, and with `--json` when the command failed.
- **Broken pipe.** A write error `EPIPE` on stdout ends the run with exit 0 and no message; later writes are discarded.
- **Debug.** `LOCALAI_CLI_DEBUG=1` logs one JSON line per request and response to stderr through the agent's scrubber (`internal/logx`): the `Authorization` value and secret-named body fields are redacted.

#### 3.2.6 Output, prompts and the PIN flow (AICTL-02)
Implemented in `agent/internal/cli/out` (printing), `agent/internal/cli/i18n` (the en/es labels) and `agent/internal/cli` (`confirm.go`, `admin.go`, `terminal.go`). A command prints through `c.Out` (an `*out.Printer`) and never writes colour or symbols itself; the framework prints the problems a command returns.
- **Printer.** `OK` and `Info` (stdout), `Warn`, `Error`, `Critical`, `Progress` and `Problem` (stderr), `Table`, `KV`, `Secondary`, `Result`, `Blank` and `Finding` (stdout), `When` (a time for people), `PrintSecretWarning`. `Problem` takes the doctor `Code`, `Message`, `Fix`, `Details`, the request id and a wait time. `--quiet` hides everything except errors, the essential `Result` and `Table`/`KV` output, and the secret warning; `Finding` is `Problem` for a command whose result is a list of problems (`doctor`, the alerts of `status`): the same layout on stdout, so a pipe keeps it; `--quiet` hides its info and warning findings, never an error. `--json` turns every stdout method into a no-op (the `out.JSON` writer owns stdout) and an error-severity problem into the envelope.
- **`--json` writer** (`c.Out.JSON()`): `Document(v)` or `Raw(body)` once, or `Line(v)` repeatedly for JSON Lines. A second document, or a document after lines, is refused (`ErrSecondDocument`) and writes nothing. If a command has already written its document and then fails, stdout keeps that one document and only the exit code reports the failure; stderr stays empty.
- **Terminal.** Prompts read through a `Terminal` (`Interactive`, `ReadLine`, `ReadSecret`), real (`golang.org/x/term` on stdin) or scripted in tests (`cli/fake`); the question is written to **stderr**. Interactive means stdin is a terminal; a pipe, `/dev/null` and a closed stdin are not. Nothing is ever read from a non-terminal stdin for a confirmation, so piped data meant for a command is never consumed by one. Reads are bounded (4 KiB per line).
- **Confirm** (`c.Confirm(question)`). Writes `<question> [y/N] ` (es: `[s/N]`). Yes is `y`/`yes` (es also `s`/`si`/`sí`), case-insensitive; Enter, anything else and end of input (Ctrl-D) are **No** and the command stops without changing anything. `--yes` answers yes without asking. Without `--yes`, `--json` or no terminal is `LAI-CLI-007` (exit 2). **ConfirmTyped** (`c.ConfirmTyped(prompt, expected)`) asks the user to type `expected` exactly (the model id of `delete --allow-last-copy`); `--yes` does **not** answer it (a person decides), so without a terminal it is `LAI-CLI-007` even with `--yes`. Ctrl-C at either prompt ends the command at once with exit 130 and **no message** (nothing had started, so "still running in the background" would be false).
- **Admin flow** (`c.Admin(fn)`, `fn` being the `[admin]` API call, safe to run twice). `fn` runs; for root over the socket, or with a valid grant, nothing else happens. On `403` with `details.pin_required: true` the CLI obtains the PIN, calls `POST /pin/verify`, keeps the grant **in memory in the client** (every later request of the command carries `X-LocalAI-Pin-Grant`, so a second admin call asks nothing) and runs `fn` once more; that second result is returned, never a third attempt.
  - *Source of the PIN.* `--pin-stdin`: one line from stdin (on a terminal, without echo). Otherwise `Admin PIN: ` on the terminal, no echo. With `--json`, or stdin not a terminal, and no `--pin-stdin`: `LAI-CLI-008`, exit 4. `--json` with `--pin-stdin` works and prints no prompt, so stderr stays empty. An empty answer (Enter, end of input) is `LAI-CLI-008` with "No PIN was entered." and nothing is sent.
  - *Tries.* On a terminal without `--json`, up to **3 prompts** per command: after each wrong PIN (`LAI-SEC-003`) the problem is shown as a warning with `Tries left: N.` (from `details.remaining_attempts`) and the PIN is asked again; the third wrong PIN ends the command with the agent's `LAI-SEC-003` error (exit 4). With `--pin-stdin` on a pipe, or with `--json`, there is one try.
  - *Lockout.* `LAI-SEC-004` ends the command at once; the error carries the `Retry-After` wait and the layout adds `→ Wait: 4 min 12 s` (es: `→ Espere:`). Exit **4** (PIN locked), not the generic 6 of other `429` answers (§3.4).
  - *Other answers.* `LAI-SEC-005` (no PIN set) and any other error are shown as the agent sent them. A `403` without `pin_required` (not an operator) is not a PIN request and is returned untouched. Ctrl-C at the prompt is exit 130.
- **Secret hygiene.** The PIN comes only from the terminal or stdin, never from a flag or the environment; it is never printed, never in an error or a `details` object, and never logged: `LOCALAI_CLI_DEBUG=1` logs neither the body of `POST /pin/verify` nor its answer. The grant is never written to disk and is gone when the command ends. `PrintSecretWarning` (used by `key show`, `key snippets --with-key`, AICTL-06) writes `! This is the secret key for this node. Do not paste it in chats or emails.` to stderr, also with `--quiet`, never with `--json`.

### 3.3 Output style guide (human mode)

**Streams.** Results go to **stdout**; warnings, errors and progress go to **stderr**. A command that succeeds with warnings still exits 0 (except `doctor`/`drill`, §3.4).

**Symbols and colours** (colour only on a TTY; the symbol is always printed so meaning never depends on colour):

| Meaning | Symbol | Colour (ANSI) |
|---|---|---|
| ok / done | `✓` | green (32) |
| info | `i` | blue (34) |
| warning | `!` | yellow (33) |
| error, critical | `✗` | red (31); critical also bold |
| in progress | `…` (spinner `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏` on a TTY) | cyan (36) |
| secondary text (ids, paths, times) | — | dim (2) |

When the terminal cannot print UTF-8 (the first of `LC_ALL`, `LC_CTYPE`, `LANG` that is set does not name UTF-8, or none is set), the symbols become `[ok]`, `[i]`, `[!]`, `[x]`, `...`, and the arrow of the fix line becomes `->`. Colour is on only when stdout is a terminal and none of `--no-color`, `NO_COLOR` (set and not empty), `TERM=dumb` and `--json` applies; the symbols are printed either way.

**How a problem is shown** (same layout for errors, warnings and doctor results):
```
✗ The AI model service is not running.  [LAI-LLM-001]
  → Fix: Run `aictl restart llama`. If it fails again, run `aictl logs --unit llama`.
```
- Line 1: symbol, the `message` (one sentence, ends with a period), two spaces, the code in brackets (dim).
- Line 2: two-space indent, `→ Fix:` and the `fix` sentence. Commands inside the fix are in backticks so they can be copied.
- `-v` adds `  details: {…}` and `  request: req_…` (always for `LAI-CLI-009`). A rate-limited answer adds `  → Wait: 4 min 12 s`.
- In Spanish: `→ Solución:` (`→ Espere:`); the code and the `details`/`request` labels are never translated.
- Lines are wrapped at 80 columns: the message continues under its first word, the fix under the text after `→ Fix:`. A `backticked command` stays on one line when it fits, so it can be copied; a word wider than the line (a path) is never cut. The code goes on the last line of the message, or on a line of its own when it does not fit.

**Wording rules.**
- Say what happened in everyday words first ("The model is loaded."), technical names second ("qwen3.5-9b-q4km").
- Sizes: decimal units (kB = 1000 B) with one decimal, `5.9 GB`, `19.0 GB`, `12.3 MB`; from 100 of a unit up the decimal is dropped (`820 MB`, `450 GB`). Memory sizes the agent reports in MiB (graphics memory, RAM) are divided by 1024 and shown in GB with one decimal, so an "8 GB" card reads `8.0 GB`.
- Times: relative ("12 hours ago", "in 3 minutes", "just now" under 5 s; amounts rounded down); absolute RFC 3339 (UTC) only with `-v`, in parentheses after the relative time.
- Durations: `7.1 s` under a minute, `3 min 20 s` (`3 min` when the seconds are zero), `2 h 5 min` from an hour.
- Never print secrets (except `key show`, §4.8.1), stack traces, Go error chains or HTTP status numbers in human mode.
- Lines ≤ 80 columns; tables use two spaces between columns, headers in plain words, no box drawing. A table wider than the line shortens its widest column (not the first) with an ellipsis; sizes and counts are right-aligned. A key/value block (`Label  value`) pads the labels to the longest and wraps a long value under itself.
- Confirmations: `Delete qwen3.6-35b-a3b-q4km from this computer (19.0 GB)? [y/N] `. Default is always **No**. Non-interactive without `--yes` → exit 2 `LAI-CLI-007` (§3.2.6).

**Progress.** Long operations (load, sync, verify, import, mode survival, rebuild, update, drill) subscribe to `GET /events` and show:
- TTY (stderr is a terminal and `TERM` is not `dumb`): one updating line, e.g. `… Loading Qwen 3.5 9B  ███████░░░  70%  (5 s)` (a spinner instead of `…`, redraws at most 10 times a second, cut to 79 columns); a new state keeps the finished one on its own line above; the line is cleared at the end and the command prints its own final `✓` line.
- Not a TTY: one line per state change (a change of the state key, not of the percentage), no carriage returns and no escape sequences.
- `-q`: no progress lines. `--json`: nothing on stderr; the command waits (respecting `--timeout`) and prints the final document.
- `Ctrl-C` stops **waiting**, never the operation (it keeps running in the agent), prints `i Still running in the background. Check with \`aictl status\`.` and exits 130 (`LAI-CLI-010`); no cancel or `DELETE` request is ever sent. With `--json` the `LAI-CLI-010` envelope is the output. Exception: `ark sync` in the foreground asks `Cancel the sync too? [y/N]` through the interrupt hook below.

#### 3.3.1 Following an operation (AICTL-03)
Implemented by `internal/cli/events` (SSE parser and client), `internal/cli/progress` (the `Waiter` and the renderer) and `Call.Follow` in `internal/cli`; commands (AICTL-05, 06, 09, 10) only say *which* events they watch and how to word them.
- **Order.** `Follow` opens `GET /events?types=…` first (so no event is missed), then issues the command's mutating request, then correlates by the `op_id` / `run_id` of the answer (also `X-LocalAI-Op-Id`). Events that arrive before the key is known are held and matched afterwards. The request must be quick (`wait: false`, answer `202`): a request that blocks until the operation ends cannot be left behind on Ctrl-C.
- **Parsing.** `id:`, `event:`, `data:` (multi-line data joined with `\n`), `: ping` comments, LF/CRLF/CR line ends; unknown fields and unknown event types are ignored; a frame whose data is not JSON does not end the stream. `resync` and `shutdown` carry only their payload; a frame without `id:` never moves `Last-Event-ID`.
- **Never trust events for correctness.** After a `resync` (`gap` or `restart`), a `shutdown`, a dropped or idle stream, and after every reconnect, the command's re-read function (`GET /models/{id}`, `GET /mode`, `GET /ark/sync`, …) is called and its answer, not the events, decides how the wait ends.
- **Reconnect.** A lost stream (EOF, error, `shutdown`, or no byte, pings included, for 45 s = three keepalives) is reopened with `Last-Event-ID: <last id seen>`; the first attempt is immediate (after `shutdown`: after the backoff), then every 1 s until the deadline. While the stream is down the re-read function is polled once per attempt, so an operation that finished while the agent was restarting is still reported. Outage and recovery print one `i` line each (not with `-q`/`--json`). Only "agent unreachable" is retried; an answer from the agent (404, 403, 500) ends the wait with that error.
- **Deadline.** `--timeout` (else the command's default) is measured from the subscription; expiry is `LAI-CLI-004`, exit 7, and sends nothing.
- **Interrupt hook.** A command may supply `OnInterrupt` (ark sync): on Ctrl-C the live line is cleared and the hook decides to `Leave` (the normal Ctrl-C result) or `KeepWaiting`; `Follow` then takes over the signals (`cli.Interrupts`) so every later press reaches the hook.

### 3.4 Exit codes

| Code | Meaning | From HTTP |
|---|---|---|
| 0 | Success (warnings allowed) | 2xx |
| 1 | The operation failed (upstream error, rollback happened, internal error) | 500, 502 |
| 2 | Usage error or invalid input (bad flag, unknown model id format, lint failed, confirmation required) | 400, 413, 415 |
| 3 | The agent is not reachable (or still starting after 10 s) | connection errors, 503 |
| 4 | Not allowed: not root, not an operator, PIN required/wrong/locked | 401, 403 |
| 5 | Not found (model, service, content, drive) | 404 |
| 6 | Refused right now: busy, not allowed in this mode, does not fit, rate limited | 409, 422, 429 |
| 7 | Timed out | 504, client deadline |
| 8 | Finished with warnings: `doctor` warnings and no errors; `drill` overall `warn`; `ark sync\|verify\|import` with `--wait` or `--scheduled` where some items failed or were skipped (last good copies kept) | — |
| 9 | Finished with errors: `doctor` errors or critical problems; `drill` overall `fail`; `ark sync\|verify\|import` run `failed` or aborted | — |
| 130 | Interrupted by Ctrl-C | — |

Rules: a `LAI-SEC-004` answer (PIN locked after too many wrong tries, `429`) is exit **4**, the "PIN locked" of the row above, not the generic 6 of other `429` answers; exit 3 means **only** "agent not reachable". A **skipped** `--scheduled` run (`aictl ark sync --scheduled`, `aictl drill --scheduled`: offline, mode, battery, disabled, busy, survival) prints `i … skipped: <reason>` and exits **0**. The units `localai-sync.service` and `localai-drill.service` set `SuccessExitStatus=8`, so a run with warnings is a success for systemd, while 9 (errors) and 3 (agent down) mark the unit failed and alert ([ark-sync.md](ark-sync.md) §3.7, [drill.md](drill.md) §3.3).

### 3.5 `--json` mode
- stdout gets exactly **one JSON document** per command (no extra lines), UTF-8, no colour.
- For API commands it is the agent's response body unchanged (schemas in agent.md §3.8 / `agent/api/openapi.yaml`), except where §4 says otherwise.
- On failure stdout gets the error envelope `{"error":{"code","message","fix","details"}}` (the agent's, or one built by the CLI with a `LAI-CLI-*` code, whatever the severity of the code) and the exit code is set; stderr stays empty. If the command had already written its document, that document stays the only one.
- Progress is not printed; the command waits (respecting `--timeout`) and prints the final document.
- Streaming commands (`logs -f`) print **JSON Lines** (one object per line).

## 4. Commands

Each command lists: synopsis, flags, transport (API route or **[local]**), offline behaviour, a human output example, the `--json` output, exit codes beyond the generic 3/4. Default timeouts in brackets.

### 4.1 `aictl status`  [10 s]
```
aictl status [--watch]
```
`GET /status`. `--watch` redraws every 2 s and whenever the agent publishes an event (`GET /events`, any type, bursts coalesced to one redraw at most every 0.5 s; if the stream cannot be opened it only polls). `--watch` needs a terminal and is refused with `--json` (exit 2, `LAI-CLI-005`); Ctrl-C ends it with exit 0; it has no overall deadline unless `--timeout` is given (each request still has its own 10 s). A refresh that fails (the agent restarting) is drawn as the problem and retried on the next tick. Offline: works (reports `Internet: not checked` in offline/survival).
```
Node ai-n3 · HP ZBook (ai-n3) · profile small-8gb
Mode       online      (since 17 hours ago)
Internet   ✓ full      (cloud reachable)
Brain      ✓ Qwen 3.5 9B (qwen3.5-9b-q4km) — default, loaded
Graphics   6.6 / 8.0 GB used · 61 °C
Memory     5.0 GB free of 14.0 GB
Power      ✓ plugged in · battery 100%
Services   ✓ llama   ✓ kiwix   ✓ whisper
Ark        ! internal 82% ready · last sync 12 hours ago · no ark drive
Cloud      ✓ linked to https://ai.marin.cr · last heartbeat 5 s ago
Tailnet    ✓ published (pattern B)

! Running on battery.  [LAI-HW-012]
  → Fix: Plug in the charger. A laptop server stops when the battery runs out.
```
(Alerts are printed after the summary in the problem layout of §3.3, on stdout.) `--json`: `GET /status` body. Exit 0 even with alerts.

Line rules (AICTL-04). A section the agent leaves out is skipped, never printed as zeros; a time that cannot be read is left out. Times are relative (§3.3), so "since yesterday 22:10" is `since 17 hours ago`. Sizes in MiB are shown as `MiB / 1024` GB (§3.3), so 14336 MiB reads `14.0 GB`.
- *Header*: `Node <id> · <display name> · profile <profile>`; the display name is left out when it equals the id.
- *Mode*: the mode and `(since …)`; during a transition `online (switching to survival)`.
- *Internet*: `✓ full (cloud reachable)`, `! internet (cloud not reachable)`, `! tailnet (no internet)`, `✗ no connection`; `not checked` for `not_checked` and always in `offline` and `survival`.
- *Brain*: `✓ <display name> (<id>) — default, loaded` (the display name comes from a best-effort `GET /models` with a 3 s deadline; if that fails the id is shown alone, and `--json` never makes the call). Not loaded: `… loading <model>`, or `! no model loaded · default is <model>`, or `! no model stored on this computer`.
- *Graphics*: `used / total GB` and the GPU temperature; `no graphics memory (CPU only)` without a GPU. *Power*: `✓ plugged in[, charging]`, `! on battery`, plus `battery N%` when there is a battery.
- *Services*: `✓` running and healthy, `!` running but not healthy, `✗` stopped.
- *Ark*: one `<kind> N% ready` per root (`!` unless every root is 100%), `last sync <when>` / `sync running` / `never synced`, `ark drive connected` / `no ark drive`.
- *Cloud*: `✓ linked to <url> · last heartbeat …`, `! not connected right now, retrying`, `i paused until the computer is online` (state `stopped`), `not linked (optional)`. *Tailnet*: `✓ published (pattern B)`, `! not published`, `not managed by this node`.

### 4.2 `aictl models`  [10 s]
```
aictl models [--loadable] [--all] [--engine llama|whisper|piper]
aictl models show <model-id>
```
`GET /models` (`?loadable=1` with `--loadable`, `?engine=` with `--engine`; another engine is exit 2). Default shows models in the profile plus any present (a client-side filter); `--all` shows the whole catalog; `--loadable` shows the agent's list as it comes. Offline: works.
```
MODEL                    SIZE     STATUS    NOTES
qwen3.5-9b-q4km          5.9 GB   loaded    default · chat, reasoning, coder
small-vlm-q4km           3.2 GB   stored    vision
tiny-fallback-q4km       2.1 GB   stored    chat
qwen3.6-35b-a3b-q4km     19.0 GB  absent    ✗ Needs 11.0 GB of graphics memory; this…
whisper-small            466 MB   stored    speech-to-text
```
NOTES carries `default`, the roles and, for a model that cannot be loaded, `✗` and the agent's `loadable_reason.message` (already in the user's language through `Accept-Language`). A table wider than 80 columns shortens its widest column with an ellipsis (§3.3), so a long reason is cut: `aictl models show <id>` prints it whole, with its code. `--json`: `GET /models` body. Exit 0.

`aictl models show <model-id>` (`GET /models/{id}`, id escaped as one path segment): the display name, summary and a key/value block (engine, status, size, roles, default, loadable with the reason and its code, where it is stored, what it needs, aliases, last load time, file count). An unknown id is exit 5. `--json`: the body.

### 4.3 `aictl load <model>`  [load timeout × 3 + 60 s]
```
aictl load <model-id> [--keep-others] [--no-wait]
```
`POST /models/{id}/load` with `{"wait": false, "unload_others": !keep-others}`; progress from `/events`. Offline: works (local).

aictl **always sends `wait: false`**, also when it waits. The operation outlives its caller either way (llama-router.md §4.4.1), but a waiting request only answers at the end: the `op_id` and the events could not be matched while the model loads, so no progress could be shown, and the framework's `Follow` needs the `op_id` of a quick `202` (§3.3.1). aictl then follows `/events` (opened before the request) for the `op_id` of the `202`: `model.loading` with `unloading` names the models stepping aside, `model.loading` with `progress` draws the bar, `model.loaded` of **the requested model** ends the wait (a rollback loads the previous models again under the same `op_id`; those events are ignored), `model.failed` of the requested model ends it with the failure. After a lost stream `GET /models/{id}` decides (`loaded`: done; `loading`: keep waiting; `failed`: `LAI-LLM-009`). Ctrl-C stops the waiting only (exit 130, "Still running in the background") and never calls unload. `--no-wait` prints the `202` result and exits 0. The deadline is 16 min (3 × the default 300 s load timeout + 60 s). An answer that is already `200 loaded` (the model was loaded) ends it at once.
```
… Unloading Small VLM to make room
… Loading Qwen 3.5 9B  ██████████  100%
✓ Qwen 3.5 9B is ready. (7.1 s)
  Use it at http://127.0.0.1:8080/v1 with model "qwen3.5-9b-q4km".
```
Failure (the sentence is built from the event's code, in the user's language; the fix is the doctor table's):
```
✗ The model Qwen 3.6 35B failed to start. The previous model was started again.  [LAI-LLM-009]
  → Fix: Choose a smaller model (`aictl models --loadable`) or check `aictl logs --unit llama`.
```
`--json`: the `202` body with `--no-wait`; otherwise the final answer in the shape of the `200` body (`id`, `status: "loaded"`, `already_loaded`, `load_ms`, `unloaded`, `op_id`), or the error envelope with `details.rolled_back` and `details.restored`. A `warning` (`LAI-HW-005`, `LAI-HW-006`) is shown when the agent sends it; the `202` body of the agent does not carry it today, so it only appears on a `200`.
Exit: 5 unknown id; 6 busy (`LAI-AGT-022`), doesn't fit (`LAI-CAT-005`), files missing (`LAI-LLM-008`) or no room (`LAI-LLM-011`); 1 failed (`LAI-LLM-009`, rolled back), nothing could be started (`LAI-LLM-012`) or the router does not answer (`LAI-LLM-004`); 7 the model took too long (`LAI-LLM-010`, rolled back) or the deadline. A model id that is not `^[a-z0-9][a-z0-9.\-]{2,63}$` is exit 2 before the agent is asked (the same check for `unload`, `default`, `download`, `delete` and `bench`).

### 4.4 `aictl unload <model>`  [120 s]
```
aictl unload <model-id> [--force]
```
`POST /models/{id}/unload`. `--force` is required to unload the last model in survival mode. Offline: works.
```
✓ Small VLM was unloaded.
```
`--json`: response body. Exit 6 when refused in survival without `--force` (`LAI-AGT-023`) or while another model operation runs (`LAI-AGT-022`); 5 unknown id. An unload of a model that was not loaded says `i Small VLM was already unloaded.`; when the model would not stop and the agent restarted llama (`restarted: true`) the result is still `✓`, followed by the `LAI-LLM-031` warning that names the models loaded again.

### 4.5 `aictl default [<model>]`  [10 s]
```
aictl default                 # show
aictl default <model-id>      # set
```
Show: `GET /models` → `default`. Set: `PUT /models/default`. Offline: works.
```
✓ The default model is now Qwen 3.5 9B. It starts automatically when the computer starts.
  It is not loaded right now. Load it with `aictl load qwen3.5-9b-q4km`.
```
`--json`: `{"default": "…"}` (show; `""` when none is set) or the PUT body. Exit 6 when not present/loadable (`LAI-CAT-005`, `LAI-LLM-008`), 5 unknown id. The second line comes from `GET /models/{id}` (`It is loaded right now.` when it is loaded) and is left out when that read fails.

Show (AICTL-04): `The default model is Qwen 3.5 9B (qwen3.5-9b-q4km). It is loaded.` (or `It is not loaded right now.`, `It is not stored on this computer.`); with none set, `i No default model is set. Choose one with \`aictl models\`.`.
### 4.6 `aictl download <model>`  [30 s; `--wait`: none]
```
aictl download <model-id> [--root <path>] [--wait] [--allow-offline-mode]
```
`POST /models/{id}/download`; with `--wait`, follows `sync.*` events until the run ends. **Offline behaviour:** needs internet; in `offline` mode it requires `--allow-offline-mode` (uses whatever network exists); in `survival` it is refused (`LAI-SYN-010`, exit 6).
```
✓ Download of Small VLM (3.2 GB) has started. Follow it with `aictl ark status`.
```
`--json`: `202` body (or the final `sync.done` data with `--wait`).

`--root` is a path or a root id (repeatable; anything else is exit 2); the body is `{"allow_offline_mode": <bool>, "roots": [...]}` (`roots` only when `--root` is given). Without `--wait` the command has a 30 s deadline and ends with the `202`. With `--wait` it has no deadline of its own and follows the `sync.*` events of the returned `sync_run_id` like `aictl ark sync --wait`, drawing `… Downloading Small VLM` with the bar, bytes, rate and time left; it ends with `✓ Small VLM (3.2 GB) was downloaded.` when the model's item was verified (exit 0), and with the item's own problem (code and fix, as `ark sync` prints them) and exit **1** when it was not (failed, quarantined, skipped, or the run stopped). After a lost stream `GET /ark/sync` decides; Ctrl-C stops the waiting only (exit 130) and sends nothing. Exit 6 for the refusals above and for `LAI-SYN-005` (a sync is already running), 5 for an unknown id. Also exit 6 for `LAI-ARK-001` (no writable content root).

### 4.7 `aictl delete <model>`  [60 s]  [admin]
```
aictl delete <model-id> [--root <path>] [--allow-last-copy] [--yes]
```
`DELETE /models/{id}?root=…&keep_last=<!allow-last-copy>` after a confirmation. `--allow-last-copy` asks a **second** confirmation ("This is the last verified copy anywhere. Type the model id to confirm:"). Offline: works.
```
Delete Qwen 3.6 35B from this computer (19.0 GB)? [y/N] y
✓ Deleted Qwen 3.6 35B from /srv/localai (19.0 GB freed).
```
`--json`: response body (requires `--yes`). Exit 6 when loaded or last copy; 2 without `--yes` in non-interactive mode.

Details. The question names the model and its size from `GET /models/{id}` (a model that does not exist ends there with exit 5, before any question); the default answer is No and says `i Nothing was deleted.`. `--root` is a path or a root id; the question then reads `from <root>`. The request always carries `keep_last` (`true`, or `false` with `--allow-last-copy`). `--allow-last-copy` asks the typed question after the first one; `--yes` answers the first question but **never the typed one**, so with `--allow-last-copy` a terminal is needed even with `--yes`, and `--json` cannot be combined with it (exit 2, `LAI-CLI-007`, nothing sent). The admin PIN is asked after the confirmations and only when the agent asks for it (`403 pin_required`: an operator over the socket; root is never asked); a No never asks for it. With `--yes` and no terminal an operator needs `--pin-stdin` (`LAI-CLI-008`, exit 4). One result line per content root in the answer's `deleted` (`✓ Deleted Qwen 3.6 35B from /srv/localai (19.0 GB freed).`); an empty `deleted` is `i … was not stored there. Nothing was deleted.`. The agent's refusals (a loaded model `LAI-AGT-022`, the last verified copy, survival mode) are printed as they come, exit 6. The last verified copy is refused with `409 LAI-SYN-044`. An answer that kept a root's copy as the last verified one (`kept`, reason `last_verified_copy`) says so after the result line (`i Kept the copy in <root>: it is the last verified copy. Add --allow-last-copy to delete it too.`); `warnings` (a problem of the router reload that follows, the files are already gone) are shown as warnings, exit 0.

### 4.8 `aictl key …`
#### 4.8.1 `aictl key show`  [10 s]
`GET /key` (audited). The only command that prints the full key. Prints a warning line to stderr first: `! This is the secret key for this node. Do not paste it in chats or emails.` Offline: works.
```
Key          3f9a…(64 hex characters)…c012
Fingerprint  3f9a…c012
Local URL    http://127.0.0.1:8080/v1
Tailnet URL  https://hp-linux.<TAILNET>.ts.net/v1
Public URL   https://ai-n3.marin.cr/v1
Models       qwen3.5-9b-q4km, small-vlm-q4km
```
(`<TAILNET>` is shown here as a placeholder; the real command prints the machine's real MagicDNS name.) An address the node does not have (`null` in the body) is shown as `not available` (`no disponible`). `--json`: `GET /key` body. `LOCALAI_CLI_DEBUG` logs never hold the key (the debug log scrubs 64-hex values), and a `|` to a closed pipe is exit 0.

#### 4.8.2 `aictl key copy`  [10 s]
`GET /key`, then puts the key on the clipboard, trying in order: `wl-copy` (if `WAYLAND_DISPLAY`), `xclip -selection clipboard` / `xsel --clipboard --input` (if `DISPLAY`), OSC 52 escape sequence (if stdout is a TTY). Never prints the key. The tool is run through `execx` with a fixed argv and the key on **stdin**, never in argv or in its environment; a tool that forks into the background and keeps our pipes open (xclip, wl-copy do) still counts as success. Under `sudo` the clipboard tools run as `$SUDO_USER` (`execx.Cmd.Credential`) with `HOME` and `XDG_RUNTIME_DIR=/run/user/<uid>` of that user and the `DISPLAY` / `WAYLAND_DISPLAY` / `XAUTHORITY` that sudo kept. OSC 52 is written to stdout only when stdout is a terminal and never with `--json`, where stdout is the one document.
```
✓ The API key is on the clipboard. Paste it where it is needed, then copy something else.
```
(Wrapped at 80 columns like every line, §3.3: the second half goes on a second line.)
None available → exit 1 `LAI-CLI-011` ("Use `aictl key show` instead"). `--json`: `{"copied": true, "method": "wl-copy"}`; `method` is the program (`wl-copy`, `xclip`, `xsel`) or `osc52`.

#### 4.8.3 `aictl key rotate`  [15 min]  [admin]
`POST /key/rotate` after confirmation ("Programs using the old key will stop working until you update them. Continue? [y/N]"). Offline: works (the cloud is updated on the next link start).
```
✓ New key created (fingerprint c0de…77aa). The model was started again.
  Update these programs with the new key (`aictl key copy`):
   • OpenCode on this computer: ~/.config/opencode/llama-api-key
   • Any script that uses the old key directly
  The cloud and the gateway were updated automatically.
```
`--json`: response body. The sentence about the model says `The models were started again.` for several and is left out when none was running; `cloud_synced: false` replaces the last line with a warning (`! The cloud has not received the new key yet … run `aictl cloud resend-key``). Exit 1 if llama did not restart (key is still rotated, `details.key_rotated: true`): the error is shown after `! The new key is in place, but the model service did not start with it …`. The confirmation comes before the PIN: a person who answers No is never asked for the PIN.

#### 4.8.4 `aictl key snippets`  [10 s]
```
aictl key snippets [--for curl|env|opencode|python] [--url local|tailnet|public] [--with-key]
```
`GET /key`; prints ready-to-paste snippets from `snippets`, with the chosen base URL. The key appears as `$LLAMA_API_KEY` unless `--with-key` (then it substitutes the real key and prints the warning line). Default `--for` all, `--url local`.
```
# curl
export LLAMA_API_KEY=$(aictl key show --json | jq -r .key)
curl -s http://127.0.0.1:8080/v1/chat/completions -H "Authorization: Bearer $LLAMA_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"qwen3.5-9b-q4km","messages":[{"role":"user","content":"Hello"}]}'
```
`--json`: `{"snippets": {…}, "base_url": "…"}` (keys `curl`, `env`, `opencode`, `openai_python`; one line per snippet, no `export` helper line). The `# <kind>` headers appear only when all kinds are printed; the agent's one-line `curl` is split before its `-H 'Content-Type` and `-d '` parts as shown; the python snippet reads `os.environ["LLAMA_API_KEY"]` (with `import os`) unless `--with-key`; `opencode` is pretty-printed JSON whose `apiKey` is the `{file:…}` reference. `--url tailnet|public` replaces the local address inside every snippet; an address the node does not have is exit 2 (`This node has no tailnet address right now.`). An unknown `--for` or `--url` is exit 2.

### 4.9 `aictl mode [online|offline|survival]`  [show 10 s; change 20 min]
```
aictl mode                     # show
aictl mode <target> [--no-wait] [--reapply]
```
Show: `GET /mode`. Change: `POST /mode {"target", "wait": !no-wait}`; for `survival` the CLI uses `POST /mode/survival` (no auth needed, works for anyone). Leaving survival: root, or socket operator (admin*), else PIN. `--reapply` re-runs all steps even if already in that mode: it sends `"reapply": true` in the `POST /mode` (or `POST /mode/survival`) body ([agent.md](agent.md) §3.8.15/§3.8.16); without it a same-target request answers `200 {"already": true}` and the CLI prints `i Already in <mode> mode.` Offline: works (it is the offline switch).
```
$ aictl mode survival
… Stopping cloud connection
… Unpublishing this node from the tailnet
… Starting Qwen 3.5 9B
… Starting Tiny fallback model
… Starting the library and speech
✓ Survival mode is on. Open http://127.0.0.1:7070 for the Survival Portal.
! In survival mode the preferred model did not start; the next one is used.  [LAI-LLM-020]
  → Fix: Nothing to do now. Later, check `aictl logs --unit llama`.
```
Show:
```
Mode  online (since yesterday 22:10)
      Switch with: aictl mode offline | aictl mode survival
```
`--json`: `GET /mode` body, or the final `200` body of `POST /mode`. Exit 6 if another change is running; 4 if leaving survival without permission.

**Implementation (AICTL-06).** `aictl mode` with extra arguments or an unknown mode is `LAI-CLI-005` / exit 2 before any request.
- **Show.** The line is `Mode  <mode> (since <day> <hh:mm>)` with the day as `today`, `yesterday` or a date (local time; `-v` adds the RFC 3339 time), then `Switch with:` the `allowed_targets`. A running `transition` adds `Changing to <target>: <step>`, `leave_requires_admin` adds `Leaving this mode needs the admin PIN (not with sudo).`, `suggest_survival` adds an `i` line (no internet for 30 minutes). Deadline 10 s unless `--timeout` is given.
- **Change.** Deadline 20 min. `POST /mode` carries `{"target", "wait": !no-wait}` plus `"reapply": true` with `--reapply`; the `200 {op_id, from, mode, warnings}` of a waiting request is the result (no progress is drawn: the answer is already in hand), and a `202` is followed through the events like survival. `--no-wait` returns after the answer with `i The change to <mode> mode has started. Check it with `aictl mode`.` Leaving survival goes through `Call.Admin`, which asks for the PIN only when the agent answers 403 with `pin_required` (root and socket operators never see a prompt); the other targets use it too, harmlessly.
- **Survival.** `POST /mode/survival` with `{}` or `{"reapply": true}`, no `Call.Admin`. `Follow` watches `mode.step`, `mode.changed` and `model.loading` (correlated by `op_id`; a `mode.changed` whose `to` is the target also counts) and re-reads `GET /mode` after a lost stream. Labels: `link_stop` Stopping cloud connection, `sync_cancel` Stopping the running sync, `tailscale_off` Unpublishing this node from the tailnet, `survival_services` Starting the library and speech, `tailscale_on` Publishing this node on the tailnet, `link_start` Connecting to the cloud; `survival_models` has no line of its own: each `model.loading` of that step reads `Starting <display name>` (names from `GET /models`, the id when that fails). `prober_*`, `persist`, `emit` and `sync_catchup` are silent. Warnings of `mode.changed` are printed after the `✓` line in the problem layout, with the title and fix of the doctor table in the user's language. `--json` prints `{"op_id","from","mode","warnings"}` built from `mode.changed`.
- **`202 {queued: true}`** (the agent is still starting): `… Survival mode will start as soon as the agent is ready.` and the wait goes on.
- **Anyone at the keyboard.** The client is told `AllowAnonymous` for this route only: when the socket cannot be opened and the token cannot be read (a user outside `localai-operators`), the TCP request goes out **without** an `Authorization` header, which the route accepts. If the event stream is then refused (401/403) or not yet available (the agent is starting), aictl sends the request without following it and prints `i Survival mode is starting. In a minute, open <portal> for the Survival Portal.` (exit 0). Every other command, and every other mode target, still needs the token and fails the usual way. If the agent is down: `LAI-CLI-001` and its fix, exit 3.

### 4.10 `aictl ark …`
The ark commands are summarized in [ark-sync.md](ark-sync.md) §3.4, which is authoritative for their flags and semantics; this section adds the CLI-side details. Roots are given as root ids (`internal`, `ark-<8 hex>`) or paths.

| Command | API | Offline | Notes |
|---|---|---|---|
| `aictl ark` / `aictl ark status` | `GET /ark` (+ `GET /ark/sync`) | works | READY.json summary per root |
| `aictl ark drives` | `GET /ark/drives` | works | includes unusable drives and why |
| `aictl ark drives set-profile <root-id> <profile>` [admin] | `PUT /ark/drives/{root_id}/profile` | works | |
| `aictl ark sync [--root <id>]... [--item <id>]... [--dry-run] [--allow-offline-mode] [--scheduled] [--wait]` | `POST /ark/sync`, then `/events` | needs network | returns after the run starts unless `--wait`; `--scheduled` is used only by `localai-sync.service` |
| `aictl ark sync --cancel` | `DELETE /ark/sync` | works | graceful; `.part` files stay |
| `aictl ark verify [--root <id>]... [--item <id>]... [--full] [--wait]` | `POST /ark/verify` | works | |
| `aictl ark import [--from <root-id>] [--item <id>]... [--add-to-profile] [--catalog] [--dry-run] [--wait]` | `POST /ark/import` | works (its purpose) | `--from` defaults to the only connected drive; several → exit 2 |
| `aictl ark eject <root-id>` | `POST /ark/drives/{root_id}/eject` | works | |
| `aictl ark init <mount-path\|device\|root-id>` | `GET /ark/drives`, then `POST /ark/drives/{root_id}/init` | works | asks for confirmation; the CLI resolves the argument to a root id from `GET /ark/drives` (every listed drive, initialised or not, carries a `root_id`; an uninitialised drive shows its candidate id); no match → exit 5 |
| `aictl ark mount <root-id>` | `POST /ark/drives/{root_id}/mount` | works | |
| `aictl ark prune [--root <id>] [--orphans] [--trash] [--dry-run] [--yes]` [admin] | `POST /ark/prune` | works | never deletes the last verified copy |

`--scheduled` semantics: when the mode is not `online`, prints `i Sync skipped: the node is in <mode> mode.` and exits **0**; no internet → `skipped: offline`, exit 0 (every preflight skip of [ark-sync.md](ark-sync.md) §4.3 exits 0). `--wait`/`--scheduled` exit codes when the run finishes: 0 all items ok, 8 some items failed or were skipped, 9 the run failed or was aborted (§3.4). Interactive sync in `offline` mode needs `--allow-offline-mode`; in `survival` it is refused (exit 6, `LAI-SYN-010`).

Human output examples:
```
$ aictl ark status
Internal disk  internal     ! 82% ready   (180 GB of 220 GB budget, 273 GB free)
  missing: wikipedia-es-nopic (12.0 GB), osm-central-america (2.5 GB)
  last sync 12 hours ago: ok
Ark drive      not connected

$ aictl ark sync --wait
… Downloading wikipedia-es-nopic  ████░░░░░░  41%  4.9/12.0 GB  18 MB/s  6 min left
✓ Sync finished: 2 downloaded, 14 already up to date, 0 failed. Internal disk is ready.

$ aictl ark drives
AI-ARK  ark-3f9c0a12  /media/<user>/AI-ARK   ext4   900 GB free of 2.0 TB   ✓ ready
```
`--json`: the API body (`ark status` prints `{"ark": <GET /ark>, "sync": <GET /ark/sync>}`; `sync --wait` prints the final run summary). Verify mismatches are shown with their `LAI-SYN-*` codes; with `--wait` the run ends 0, 8 or 9 like every other run (the earlier "exit 1" for a mismatch is replaced by that rule: a finished verify with failed items is 8, a verify that failed or was stopped is 9).

**Implementation of the state-changing commands (AICTL-06).**
- **Flags.** `--root` and `--item` repeat; a root is `internal`, `ark-<8 hex>` or an absolute path, an item id is `[a-z0-9][a-z0-9.-]{2,63}`; anything else is `LAI-CLI-005` / exit 2 before a request. `ark sync`, `verify` and `import` take no positional argument.
- **Starting.** Without `--wait` the command returns after the `202` with `✓ Sync started (run <id>). It keeps running in the background; follow it with `aictl ark status`.` (`Check`, `Import` for the others); the deadline of that request is 30 s. `--scheduled` implies `--wait` (the unit runs `aictl ark sync --scheduled --wait` anyway) and sends `"trigger": "scheduled"`, otherwise `"manual"`.
- **Following.** `Follow` opens `GET /events?types=sync.*` first, then the `POST`, and correlates by `run_id`. One progress line per item: `Downloading <item>` (`Copying` for import, `Checking` for verify and for a `verifying` state) with the bar and, after it, `4.8/12.0 GB  20.0 MB/s  6 min left` (sizes in the style of §3.3, so `20.0 MB/s`, not `20 MB/s`; the parts are dropped from the right when the line is too long). After a lost stream `GET /ark/sync` decides. Deadline: none unless `--timeout`.
- **The end.** `done` with nothing failed or skipped: `✓ Sync finished: <n> downloaded, <n> already up to date, <n> failed.` (`, <n> skipped` when some were) and, per root, `Internal disk is ready.` (green), `… is usable but needs attention (yellow).`, `… is not ready (red): run `aictl ark status`.`: `sync.done` carries the colour of `READY.json`, not a percentage. Exit 0. `done` with failures or skips: the same line behind `!`, one problem per item (title and fix of its `LAI-SYN-*` code, at most ten), exit 8. `failed`: `✗ Sync did not finish. Run `aictl doctor` to see why.`, exit 9. `cancelled` by the agent: `i Sync stopped. …`, exit 9; cancelled because this command sent the cancel (below): exit 130. `verify` words the line `Check finished: <n> verified, <n> with problems.`, `import` `Import finished: <n> copied, <n> already here, <n> failed.` "Downloaded" is the number of items that reported progress and ended verified; the rest of `totals.done` is "already up to date". `--json` prints `{run_id, state, totals, ready}` (the `sync.done` data) and the exit code carries 0/8/9.
- **`--scheduled` skips.** A refusal `409 LAI-SYN-010` or `LAI-SYN-011` (mode) or `LAI-SYN-005` (another run holds the slot), a `POST` answered `state: "skipped"` with its `reason`, and a `sync.done` with `state: "skipped"` (the reason is then read from `GET /ark/sync`) all print `i Sync skipped: <why>.` and exit **0**: `the node is in <mode> mode`, `offline`, `the computer is running on battery`, `it is turned off (sync.enabled is false)`, `there is no place to store the content`, `another sync is already running`. A manual `ark sync` gets the refusal as an error (exit 6) instead. `--json` prints `{"run_id"?, "state": "skipped", "reason"}`. Exit 3 stays "agent not reachable".
- **Ctrl-C.** A followed sync takes the interrupt over (`Follow` with `OnInterrupt`): `Cancel the sync too? [y/N] ` (`[s/N]`) on stderr, read with the context Follow hands over (the process context is already cancelled by the first press). Yes → `DELETE /ark/sync`, and the wait goes on until the run reports `cancelled` (exit 130). No, or no terminal and not `--scheduled`, or `--json` → `Still running in the background` and exit 130 with nothing sent. `--scheduled` (a SIGTERM from `systemctl stop localai-sync`) cancels at once without asking. `verify` and `import` use the plain rule (stop waiting, send nothing).
- **`--cancel`.** `DELETE /ark/sync` → `✓ The sync is stopping. Downloads in progress are kept and continue next time.`
- **`--dry-run`** (sync and import) prints §4.4.4 of [ark-sync.md](ark-sync.md): the table `# Item Tier Action Version Size Note` (the table must fit 80 columns, so the note is a word or two: `refresh due`, `budget`, `local copy`, `orphan` with a line below naming `aictl ark prune --orphans`; versions are cut to 10 characters), `Downloads: <n> items, <size> from the internet, <size> local copy.`, the peak line and `Nothing was changed (dry run).` Sizes follow §3.3 (`220 GB` from 100 up, no decimal). The answer is one plan object, or `{"plans": [...]}` / an array for several roots (the spec leaves the several-roots shape open; both are accepted). `--json` is the body.
- **`ark import --from`.** Without it, `GET /ark/drives` and the one drive that has a `root_id` and state `ready` or `read_only`; several → exit 2 with `LAI-ARK-015` and the ids; none → exit 5 `No ark drive is connected.` `--from` must be a root id.
- **`ark drives set-profile <root-id> <profile>`** checks both ids (exit 2), then `PUT /ark/drives/{root_id}/profile` through the admin PIN flow; an unknown profile is `422 LAI-CAT-003` → exit 6.

Read side (AICTL-04). `ark` and `ark status` take no arguments and make two reads, `GET /ark` then `GET /ark/sync`; the second is extra detail, so an answer from the agent that is an error (no run yet) leaves the status printed and `"sync": null` under `--json`. Each root is one line `<Internal disk|Ark drive>  <root id>  <symbol> N% ready` followed by `(<used> of <budget> budget, <free> free)` (on its own indented line when the first would pass 80 columns), a `missing:` line (items whose status is `missing`, with their size when known, wrapped under the label) and `last sync <when>: ok|failed|cancelled|skipped` from the root's `last_sync`, or `never synced`. A root of another kind than `internal` is the ark drive; with none, `Ark drive  not connected`. A run in progress adds `i Sync running: 22 of 31 items.` The symbol follows the root's `overall` colour (green `✓`, yellow `!`, red `✗`). **Percent ready** is the agent's `ready_pct` when it sends one, else `verified / (verified + stale + missing + skipped + failed + quarantined)` from the READY.json `counts` (orphans are not wanted, so they do not count); unknown when nothing is wanted (`not checked`). The `problems[]` of every root follow in the problem layout on stdout.

`ark drives` prints one line per drive, columns aligned, no header: label, root id (`—` when the agent has none), mount path, filesystem, `<free> free of <size>`, and the state with a symbol (`ready` `✓`, `read-only` `!`, `not mounted` `!`, `not set up yet` `!`, `ejecting` `…`, `not shut down cleanly` `✗`, `cannot be used` `✗`); an unusable drive's `problems[]` follow indented (`  ✗ This drive is not formatted as ext4.  [LAI-ARK-002]`); with no drive, `i No ark drive found.`

### 4.11 `aictl catalog …`
#### 4.11.1 `aictl catalog lint <dir>`  [local]
Runs the catalog validator in-process ([catalog.md](catalog.md) §6). No agent needed. `--online` also HEAD-checks URLs.
```
catalog/models.yaml:models[3].source.revision  LAI-CAT-011  revision must be a 40-hex commit, got "main"
✗ 1 problem found.
```
Each problem is one line `<file>:<yaml-path>  LAI-CAT-0NN  <message>` (two spaces between fields; this form is canonical and catalog.md §6 uses it). Exit 0 valid, **2** invalid (catalog.md §6 contract). `--json`: `{"valid": false, "problems": [{"code","file","path","message"}]}`. `aictl catalog lint --json <dir>` is a stable machine interface: the cloud runs it on the VPS to lint imported catalogs ([cloud-app.md](cloud-app.md) §4.6, `LOCALAI_AICTL_PATH`), and it never needs an agent, root or network (except `--online`).
The CAT-01 implementation is a `[local]` command registered with the AICTL-01 framework (`internal/cli/catalog_lint.go`; it never builds the agent client, `--json` and `--online` may come before or after the directory, and a global `--json` before `catalog` works too); a problem about a whole file has an empty yaml-path (`<file>:  LAI-CAT-019  ...`), and results go to stdout in both modes (stderr only for usage errors).

#### 4.11.2 `aictl catalog refresh`  [120 s]
`POST /catalog/refresh`. Needs network; refused in offline/survival (exit 6).
```
✓ Catalog updated: 2026.10.20-1 → 2026.11.02-1 (from the cloud).
```
The body is `{}`. `changed: false` prints `i The catalog is already up to date (<version>).`; no `previous` prints `Catalog updated to <version> (from GitHub).`; the source words are `from the cloud`, `from GitHub`, `from the rebuild kit`. `--json`: the body. `409 LAI-AGT-023` (offline or survival) and `422 LAI-CAT-002` (rejected, the previous catalog stays) are exit 6. The command replaces the "not implemented yet" stub through `RegisterLate` (`internal/cli/register_late.go`): the `ark`, `catalog`, `profile` and `cloud` groups get subcommands from several issues, so a file that only adds subcommands queues a hook that `Main` applies once after every `init`, and a real command replaces a stub of the same name.

#### 4.11.3 `aictl catalog restore`  [local] [root]  [30 s]
Copies the last good catalog from the kit (`<root>/kit/catalog/`, internal root first, then the drive; [rebuild-kit.md](rebuild-kit.md)) into `/etc/localai/catalog/` atomically (write `catalog.new/`, lint it, rename the old one to `catalog.prev/`, rename `catalog.new/` → `catalog/`). The agent notices the `CATALOG_VERSION` change within 10 s (agent.md §3.3); the CLI waits until `GET /catalog` reports the restored version (if the agent is running). Offline: works (this is the offline fix for `LAI-CAT-001`).
```
✓ Restored catalog 2026.10.20-1 from the ark kit on the internal disk.
```
When `/etc/localai/catalog.prev/` exists because of `aictl catalog use --test` (§4.11.4), `restore` puts that previous catalog back instead of the kit copy.

Details (AICTL-07, `internal/cli/cmd_catalog_local.go`, `internal/cli/local/catalog.go`):
- **Sources, in order.** (1) `/etc/localai/catalog.prev/`, only when the live catalog carries the `TEST` marker, `catalog.prev/` exists and has no `TEST` marker itself; (2) `<root>/kit/catalog/` of the internal root; (3) the same on every mounted AI-ARK drive, sorted by mount path. A drive is found without the agent: an ext4 mount of `/proc/self/mountinfo` whose `<mount>/localai/` exists and whose `ROOT.json` says `kind: ark_drive` or whose filesystem label is exactly `AI-ARK` (`blkid`, through `execx`); an uninitialised drive (no `localai/`) is never used.
- **First valid copy wins.** Each source is linted in-process; the first that passes is installed, so one damaged copy does not stop a restore while another is intact. When none passes the command fails with `LAI-CAT-002` (exit 1; the message carries the first problem, `-v` up to ten) and `/etc/localai/catalog/` is byte-identical; when there is no source at all it is `LAI-KIT-040` (exit 5, "run `aictl ark sync` or `aictl ark import`").
- **Swap.** `catalog.Install`: copy to `catalog.new/` (every file fsynced), lint the copy, rename `catalog/` → `catalog.prev/` (replacing an older one), rename `catalog.new/` → `catalog/`, fsync `/etc/localai`. Every directory is created `root:localai` 0750 and every file 0640. After restoring from `catalog.prev/` it is removed (it only held the draft that was replaced).
- **Locking.** The command holds an exclusive, non-blocking lock (`/etc/localai/.aictl.lock`, `flock`) with every other aictl command that changes files under `/etc/localai`; a second one exits 6 `LAI-CLI-012` ("another aictl command is changing the files of this node"). The kernel drops the lock if the process dies.
- **Agent.** It waits (≤ 30 s, polling `GET /catalog` every second) only when the agent answers; if the agent is down it returns at once. If the agent answers but does not report the version in time it prints `! The catalog is installed, but the agent has not reported it yet…` and still exits 0 (the swap is done; the agent re-reads every 10 s). Ctrl-C stops the wait (exit 130).
- **Audit.** `catalog.restore` with `target.version`, `target.from` (`kit` or `previous`) and `target.root`; a failure is recorded with `result: "fail"` and its code.
- `--json`: `{"version","previous","from"}` (`from` is `kit` or `previous`; `previous` is the version that was replaced, `""` when none).

#### 4.11.4 `aictl catalog use <dir> --test`  [local] [root]  [30 s]
```
aictl catalog use <dir> --test [--yes]
```
Installs a **draft** catalog on a test node so a new entry can be measured with `aictl bench` before the catalog PR is merged. `--test` is required (there is no non-test mode; without it: exit 2). Steps: lint `<dir>` in-process (problems → printed as in §4.11.1, exit 2); confirm (`--yes` skips; default No; no terminal or `--json` without `--yes` → `LAI-CLI-007`, exit 2); copy the current `/etc/localai/catalog/` to `/etc/localai/catalog.prev/`; install `<dir>` atomically as `/etc/localai/catalog/` (same `catalog.new/` + rename, ownership, modes, lock and agent wait as §4.11.3) with an extra marker file `TEST`. The agent picks it up through the `CATALOG_VERSION` mtime watch (agent.md §3.3) and reports `GET /catalog` `source: "test"` (agent.md §3.8.21); the CLI waits for that. The test catalog stays until `aictl catalog restore` (which restores `catalog.prev/`) or the next successful `aictl catalog refresh`. Offline: works.

A second `use --test` while a test catalog is live replaces the draft but **keeps the real catalog in `catalog.prev/`** (the draft is set aside and deleted, `catalog.Install` with `KeepPrevious`), so one `restore` always returns to the real catalog. A directory that fails lint, a declined confirmation and a non-root run change nothing. The catalog version must be a real `YYYY.MM.DD-N` (catalog.md §1), so a draft is for example `2026.11.03-1`.
```
✓ Test catalog 2026.11.03-1 is in use (from ./catalog). Undo with `sudo aictl catalog restore`.
```
`--json`: `{"version","source":"test","previous"}` (a declined confirmation prints `{"cancelled":true}`, exit 0). Audit: `catalog.use_test` with `target.dir`, `target.version` and `target.previous`.

### 4.12 `aictl profile …`
| Command | Transport | Output |
|---|---|---|
| `aictl profile show` | `GET /profile` | profile id and name, overrides, effective model/content lists, planned vs budget bytes |
| `aictl profile suggest` | `GET /profile` (`suggested`, `match_reasons`) | `✓ This computer matches "small-8gb" (cuda, 8.0 GB graphics memory, 14.3 GB memory).` |
| `aictl profile set <id> [--yes]` [admin] | `GET /catalog`, then `PUT /profile {"profile": id}` | confirmation listing the target profile's models and content with their sizes (from `GET /catalog`; no sync plan is computed), then `✓ Profile set to cuda-12gb. Run \`aictl ark sync\` to fetch the new content.` |

All work offline. Exit 2/6 for unknown or invalid profile (`LAI-CAT-003`). `--json`: `GET /profile` / `PUT` body.

Read side (AICTL-04). `profile show` prints a key/value block: `Profile` (`small-8gb — Small GPU laptop (8 GB VRAM)`), `Suggested` (`✓ the same`, or `! <id>` when the agent would pick another), `Overrides` (the names of the overridden keys, or `none`), `Models`, `Content` and `Disk plan` (`180 GB planned of 220 GB budget`, with `!` and `(over budget)` when the plan is larger). `profile suggest` builds its sentence from the hardware, so besides `GET /profile` it makes a best-effort `GET /status` (backend, graphics memory, memory; sizes as in §3.3); if that fails the agent's own `match_reasons` stand in (`(backend cuda; vram 8192 MiB in 7000..11999)`). When the profile in use differs from the suggestion the command says so (`! This computer looks like "small-8gb" (…), but it uses "cpu-only". See \`aictl profile show\`.`) and still exits 0; with no suggestion, `i No profile matches this computer.`

**Implementation of `profile set` (AICTL-06).** A malformed id (`[a-z0-9][a-z0-9-]*`) is exit 2 before any request. Without `--yes`: no terminal (or `--json`) → `LAI-CLI-007` / exit 2 before any request; else `GET /profile` (the same profile → `i The profile is already <id>.`, no `PUT`) and `GET /catalog`, then the preview on **stderr**:
```
Profile now:  small-8gb (Small GPU laptop)
Switch to:    cuda-12gb (CUDA 12 GB)
Models (3, 13.1 GB):
  qwen3.5-9b-q4km   5.9 GB
  ...
Content (1, 12.0 GB):
  wikipedia-es-nopic  12.0 GB
Total 25.1 GB of a 300 GB budget. Nothing is deleted now.
Switch to the profile cuda-12gb? [y/N]
```
The lists are the target profile's `include` lists plus the node's `profile_overrides` (`add_*`, minus `remove_*`; `budget_bytes` replaces the profile's budget), sorted, with the size of each id from `models[]` / `content[]` of the catalog (`?` when unknown). `GET /catalog` therefore carries a `profiles` array (added to [agent.md](agent.md) §3.8.21 and the OpenAPI `Catalog` schema, optional): `[{"id", "display_name", "include": {"models": [], "content": []}, "budget_bytes"}]`. A target that is not in that array is `LAI-CAT-003`, exit 6, nothing asked; a catalog without the array (an older agent) gets the question without the lists. `--yes` skips the reads and the preview and sends only the `PUT {"profile": id}` (through the admin PIN flow, after the confirmation).

### 4.13 `aictl services`  [10 s]
```
aictl services
aictl services start|stop|restart <llama|kiwix|whisper> [--force]
```
`GET /services`; the subcommands call `POST /services/{name}/{action}`. `aictl services restart <name>` is the canonical form; `aictl restart <name>` (§4.14) is an alias and is what fixes in [companion-services.md](companion-services.md) §8 print. `stop llama` in survival mode needs `--force`. Offline: works.
```
SERVICE  STATE     HEALTH   SINCE
llama    running   ✓        8 hours ago
kiwix    running   ✓        8 hours ago
whisper  stopped   ✗        —   last exit code 1
```
The body is `{"services": [...]}` (agent.md section 3.8.22; aictl also accepts a bare array). STATE is `running`, `stopped`, `failed` or `starting` from the unit's `active` state; HEALTH is `✓` healthy, `…` starting, `!` degraded, `✗` otherwise. A service without a unit (voices, maps) is shown by the supervisor's `state` (`available`, `starting`, `disabled`, `stopped`). The untitled last column holds notes (`last exit code N` for a non-zero exit, `disabled`, `restarted N times`) and exists only when some row has one. Only the list exists in `aictl services` so far (AICTL-04): `start|stop|restart` is exit 1 `not implemented yet` until AICTL-10. `aictl restart <name>` (§4.14) is implemented (AICTL-05) by `restartService`, which AICTL-10 binds `services restart` to, so the two forms behave identically.

### 4.14 `aictl restart <service>`  [120 s]
Alias of `aictl services restart <name>` (§4.13). `aictl restart llama|kiwix|whisper [--force]` → `POST /services/{name}/restart` with `{}` or `{"force": true}`. For llama the agent waits for the router and for the models to come back (`restored_models`), so the request takes up to its 120 s deadline and aictl waits for the answer (there is no event to follow); Ctrl-C ends the waiting with exit 130 and the agent finishes the restart by itself. `--force` is sent as given: the agent needs it only to **stop** llama in survival mode (agent.md §3.8.22: restarting and starting llama are allowed in survival, so a restart never needs it). An unknown service is exit 2 before any request. Offline: works.
```
… Restarting the AI model service
✓ The AI model service is running again. Qwen 3.5 9B is loaded.
```
The service is called "the AI model service" (llama), "the library service" (kiwix) and "the speech service" (whisper). Several restored models read `Qwen 3.5 9B and Small VLM are loaded.`; llama with nothing to restore says `No model is loaded yet: load one with `aictl load <model-id>`.`. Exit 6 for `LAI-AGT-022` (another operation) or `LAI-AGT-023`; 1 for the service not coming back (`502`, `LAI-LLM-001`, `LAI-SVC-002`, `LAI-SVC-011`); 7 for the deadline.

### 4.15 `aictl logs`  [10 s; `-f`: none]
```
aictl logs [--unit agent|llama|kiwix|whisper|sync|drill] [-n <lines>] [--since <time>] [-f]
```
`GET /logs` (`unit` default `agent` in the CLI, which always sends it; `-n` default 100, 1 to 2000). `--since` takes an RFC 3339 time or an age (`15m`, `2h`) before now, sent as RFC 3339 UTC. `-f` polls every 2 s with `since` = the last line's time and `lines=2000`. Secrets are already scrubbed by the agent. Offline: works.
```
08:00:05  llama_server: loading model
08:00:12  llama_server: model loaded
```
`--json`: response body; with `-f`, JSON Lines of `{"at","priority","message"}`.

Details (AICTL-04). The time column is HH:MM:SS in this computer's time zone (with `-v`, RFC 3339 UTC). Lines are printed as the agent sent them, oldest first, never wrapped (so `grep` and `head` see whole lines); control characters (an escape sequence in a logged value could drive the terminal) are replaced by a space, tabs stay. A `truncated` answer prints `! The output was cut at the agent's size limit. Ask for fewer lines with -n.` on stderr. A bad `--unit`, `-n` or `--since` is exit 2 before any request. **`-f`**: the journal's `since` is inclusive, so the lines of the last printed second come back; they are matched by content (`at`, priority, message) and skipped, so a new line in the same second is still printed and identical lines are counted. Ctrl-C ends it with exit 0 and no message (it is how `tail -f` ends), a closed pipe ends it with exit 0, an error from the agent ends it with that error's exit code, and there is no overall deadline unless `--timeout` is given (each request has its own 10 s).

### 4.16 `aictl doctor`  [60 s]
```
aictl doctor [--area AGT|NET|HW|LLM|CAT|SYN|ARK|SVC|CLD|SEC|…]... [--fast] [--all]
```
`GET /doctor` (`?area=HW&area=LLM` for each `--area`, `?fast=1` for `--fast`). `--area` may be repeated and may hold commas (`--area hw,net`); a value is two to five letters (case is ignored) and is sent in upper case, because the set of areas belongs to the agent. Prints problems grouped by severity (critical, error, warning, info), then a summary. `--all` also lists passed checks. Offline: works (checks that need the network report `info` "not checked offline").
```
✗ The AI model service is not running.  [LAI-LLM-001]
  → Fix: Run `aictl restart llama`. If it fails again, run `aictl logs --unit llama`.
! Running on battery.  [LAI-HW-012]
  → Fix: Plug in the charger. A laptop server stops when the battery runs out.

41 checks passed · 1 warning · 1 error
```
The problems are the result of the command, so they go to stdout (`Printer.Finding`), not stderr. The summary counts passed checks first and then every severity that occurred, least serious first (`41 checks passed · 1 notice · 1 warning · 1 error · 1 critical problem`), wrapped at 80 columns; the counts are the larger of the agent's `summary` and the listed `results`. With no problem at all it is one line, `✓ 42 checks passed.`. `--all` lists the passed check ids as `✓ <id>` before the summary. With `-v` every problem is followed by its `details` and `  check: <id>`. `-q` shows errors and critical problems only (the exit code still reports warnings).

**Doctor code display rules:** the code is always printed (dim, in brackets) after the message; codes are never translated; the fix line is mandatory; `-v` adds `check: <id>` and details. `--json`: response body. Exit **0** all ok/info, **8** warnings only, **9** any error/critical; the exit is decided by the listed results and the summary counts, whichever says more, and is the same with `--json`.

### 4.17 `aictl drill`  [30 min]
```
aictl drill [--quick] [--only <check-id>]... [--wait] [--scheduled]
aictl drill --cancel
aictl drill show [<run-id>]
aictl drill record-manual --result pass|fail [--notes "<text>"]   [admin]
```
`aictl drill` calls `POST /drill` (`{"quick", "only", "trigger"}`, [drill.md](drill.md) §3.1). On a TTY it follows `drill.*` events and waits for the end; `--wait` forces waiting when not on a TTY (and with `--json`). `--quick` skips full hashing and non-default model loads (about 2 min). `--scheduled` is what `localai-drill.service` runs (`trigger: scheduled`; skipped runs print `i Drill skipped: <reason>` and exit **0**, drill.md §4.1, §3.4 here). Steps and scoring: [drill.md](drill.md). Offline: works (it is an offline test). `409 LAI-DRL-002`/`003`/`004`/`005` → exit 6. A `--scheduled` start that answers `202` with `state: skipped` has no events to follow: it prints `i Drill skipped: <reason>.` from the answer and exits 0 (drill.md §3.1).
- `aictl drill --cancel`: `DELETE /drill` (drill.md §3.1); the agent aborts the run and restores state. A waiting `aictl drill` that gets SIGTERM (e.g. from `localai-drill.service` stop) sends the same call. Exit 0, or 5 when no drill runs.
- `aictl drill show [<run-id>]`: `GET /drill` (or `?run_id=`); human summary of the last (or that) run, one line per non-passing check with its code and fix. `--json`: the results document. Exit 0/8/9 like `aictl drill`.
- `aictl drill record-manual`: `POST /drill/manual` (admin) records the manual quarterly "pull the plug" drill ([drill.md](drill.md) §4.6 step 17). Exit 2 if `--result` is missing.
- (`aictl drill --last` is an alias of `aictl drill show`.)
```
… Drill: switching to a private test (nothing is published during the test)
✓ Brain answered in 2.1 s
✓ Library search worked
✓ Maps tiles served
! Speech-to-text slow (8.2 s)
✓ Drill passed with 1 warning. Next drill: in 30 days.
```
Exit 0 pass, 8 pass with warnings, 9 fail.

### 4.18 `aictl cloud …`
| Command | Transport | Notes |
|---|---|---|
| `aictl cloud enroll [--url <url>] [--credential-stdin]` [admin] | `POST /cloud/enroll` | prompts `Node credential (nod_…): ` without echo, or reads stdin with `--credential-stdin`. Never accepted as an argument. `--url` default `https://ai.marin.cr`. Offline mode: stored, link starts when online |
| `aictl cloud status` | `GET /cloud` | state, url, last heartbeat, credential `nod_…a1b2`, key synced |
| `aictl cloud unenroll [--yes]` [admin] | `DELETE /cloud` | confirmation first |
| `aictl cloud resend-key` [admin] | `POST /cloud/resend-key` | pushes the llama key to the cloud again (e.g. after the cloud lost its encryption key, [cloud-app.md](cloud-app.md) §3.8). Needs online mode |

```
$ sudo aictl cloud enroll
Node credential (nod_…):
✓ Connected to https://ai.marin.cr as ai-n3. The node shows up on the Nodes page.
```
Errors: rejected credential → exit 6 `LAI-CLD-051`; network down → exit 0 with `! Saved. The cloud is not reachable right now; the node will connect by itself.  [LAI-CLD-053]`.

`cloud status` (AICTL-04) prints a key/value block: `State` (`✓ linked`, `! not connected right now, retrying`, `i paused until the computer is online`), `Address`, `Node name`, `Last heartbeat`, `Credential`, `Key synced` (`✓ yes` / `! no`) and `Last error` when there is one. The credential is shown as the agent sends it, masked (`nod_…a1b2`); should an answer ever carry more, only the prefix and the last four characters are printed. A node that is not enrolled prints `i This node is not linked to a cloud app. That is fine: everything works without it.` and exits 0. `--json`: the body.

**Implementation (AICTL-06).** The credential is read first, once, then the request goes through `Call.Admin` (the retry after a PIN sends the same credential). From a terminal: the prompt `Node credential (nod_…): ` (`Credencial del nodo (nod_…): `) on stderr and a read without echo (`Terminal.ReadSecret`). With `--credential-stdin`: one line of stdin; with `--pin-stdin` as well, the credential line comes first and the PIN line second. No terminal (or `--json`) and no `--credential-stdin` → exit 2 with the fix. A value that does not start with `nod_` or is empty is exit 2, and **no message repeats a credential**: a credential given as an argument (`aictl cloud enroll nod_…`) is refused with `The node credential is never given as an argument.`, and an option such as `--credential` does not exist (`Unknown option`). `--url` must be an http or https address with a host and no user name or password (exit 2 otherwise); a trailing `/` is dropped. `✓ Connected to <url> as <name>.` takes the name from `GET /cloud` (the line is worded without it when that fails) and adds `Its earlier record was reused.` for `reattached`. `state: "backoff"` is a warning problem with `[LAI-CLD-053]` and exit 0; `state: "stopped"` is `i Saved. The node connects to the cloud when it is in online mode.` `unenroll` confirms (`Disconnect this node from the cloud? …`) before the PIN; `resend-key` prints `✓ The API key (fingerprint …) was sent to the cloud again.` and exits 1 when `key_synced` is false; `409 LAI-AGT-023` (not linked) is exit 6. `--json`: the body.

### 4.19 `aictl pin set`  [local] [root]
Prompts twice (`New admin PIN (4 to 8 digits):`, `Repeat:`), or reads one line from stdin with `--pin-stdin`. Writes `/etc/localai/admin-pin` (argon2id, agent.md §3.9.2) atomically, 0600 root, and appends an audit line (`pin.set`, `actor.kind: cli-root`). The agent reads the file on every verify, so no restart is needed. Offline: works.
```
✓ The admin PIN was changed. Keep it somewhere safe (for example, on paper with the ark drive).
```
Exit 2 for a PIN that is not 4–8 digits or the two entries differ.

Details (AICTL-07, `internal/cli/cmd_pin.go`):
- Checks root first (`LAI-CLI-006`, exit 4) before asking anything or touching a file. The PIN comes only from the terminal (no echo, prompts `New admin PIN (4 to 8 digits):` and `Repeat:` on stderr) or from one line of stdin with `--pin-stdin` (a single entry, no repeat; prompted only on a terminal). A first entry that is not 4 to 8 digits is rejected before `Repeat:` is asked. Without a terminal and without `--pin-stdin`, or with `--json` and no `--pin-stdin`: `LAI-CLI-008`, exit 4. An empty entry or empty stdin: `LAI-CLI-008` "No PIN was entered.", exit 4. Ctrl-C at a prompt: exit 130.
- The file holds the PHC string and a trailing newline (the agent trims it); it is written with `config.WriteFileAtomic` (temp file, fsync, rename, fsync of the folder), `root:root` 0600, under the lock of §4.11.3. A failed write leaves the old file in place and no temp file (`LAI-CLI-012`, exit 1, naming `/etc/localai`/the file and the reason).
- Audit: `pin.set`, `actor.kind: cli-root`; a failed write is recorded with `result: "fail"`, `code: "LAI-CLI-012"`. The PIN is in no message, error or audit field.
- `--json`: `{"changed":true}`. There is no `pin clear`: the agent treats a missing PIN file as "no PIN is set" (`LAI-SEC-005`), and removing it is a deliberate manual act.

### 4.20 `aictl ui-code`  [10 s]
`POST /ui-code` (root or a `localai-operators` socket peer). Offline: works.
```
Login code  482 913   (valid for 5 minutes, one use)
Open http://127.0.0.1:7070 in a browser and type this code.
```
`--json`: response body. The code is shown in groups of three digits, the minutes are rounded from `expires_at` (5 when it cannot be read), and the two lines are the essential result: `-q` keeps them. The portal address is the `--url` in force (`http://127.0.0.1:7070` by default).

### 4.21 `aictl token rotate`  [local] [root]
Writes a new `lat_…` token to `/etc/localai/agent-token` (0640 root:localai-operators) atomically and appends an audit line. The agent re-reads the file when its mtime changes (≤ 10 s); existing sessions stay valid; ai-desktop re-reads the token on its next 401. Offline: works.
```
✓ A new agent token was created. ai-desktop picks it up by itself. Scripts that copied the old token must read the file again.
```
Details (AICTL-07, `internal/cli/cmd_token.go`): root check first (exit 4); the group `localai-operators` is looked up **before** anything is written, and a machine without it fails with `LAI-CLI-012` (exit 1, "the group \"localai-operators\" does not exist", fix: `sudo install-node.sh --repair`) and an untouched token file. The token is `lat_` + 43 base64url characters from `crypto/rand` and is **never printed**; the file ends with a newline. Written atomically under the lock of §4.11.3; audit `token.rotate` (`cli-root`). `--json`: `{"rotated":true,"path":"/etc/localai/agent-token"}`. There is no `token show`: scripts read the file.

### 4.22 `aictl docs render`  [local]  [10 min]
```
aictl docs render [--src <docs-dir>] [--inventory <file>] [--out <dir>]... [--no-pdf] [--node <ai-nN>]
```
Renders runbooks and specs to HTML (+ PDF) with placeholders filled from `inventory.local.yaml` ([security-model.md](../architecture/security-model.md) §5, ADR-0007). **Output is never written inside a git checkout** (the command refuses an `--out` that is inside a directory containing `.git`: exit 2, and nothing is written to any `--out`). Implemented in `agent/internal/docsrender` (the engine, no doctor codes) and `agent/internal/cli/cmd_docs.go` (flags, defaults, codes, output). Flags go after `render`; `--json` and `--lang` are global flags and go before `docs`.
- `--src`: default `./docs` when the current directory is a repo checkout (`./docs/runbooks/` exists); otherwise `--src` is required (exit 2). Sync passes it explicitly ([ark-sync.md](ark-sync.md) §4.9.8).
- `--inventory`: default, first found: `/etc/localai/inventory.local.yaml`, then `<ark-drive>/inventory.local.yaml`, then `./inventory.local.yaml`. Placeholder ↔ field pairing and the secret markers: [security-model.md](../architecture/security-model.md) §5. Missing → warning `LAI-CLI-013`; placeholders stay as `<UPPER_SNAKE>`. A secret placeholder on the survival card → exit 1 `LAI-CLI-016` (survival-portal.md §4.12).
- `--out` (repeatable): default `/srv/localai/docs` plus `<drive>/localai/docs` for every mounted drive (a mount point named `AI-ARK` in `/proc/mounts`). Requires write access (root for `/srv/localai`). When run as root the files are owned by the `localai` user if it exists. A file or folder that cannot be written is `LAI-CLI-012` (exit 1); the other `--out` folders are still written.
- `--node <ai-nN>`: the node the survival card is for. Default: `node.id` of `/etc/localai/node.yaml`; else the `nodes[]` entry of the inventory whose `machine` is this host's name; else `unconfigured` (the agent's safe-default id) with a warning, so a node with damaged configuration still gets its manuals.
- **What is rendered.** Every `.md` file of `--src` except the `source/` folder (the redacted archive, "history only") and the card template. A page is `<slug>.html` in one flat folder (`<root>/docs/<slug>.html`, [ark-sync.md](ark-sync.md) §4.15 links to these names); the slug is the file name without `.md`. Two files with the same name (every folder's `README.md`) both take their folder as a prefix (`runbooks-README`, `docs-README`), so a slug never depends on file order. `title` is the first `# ` heading (the slug when there is none); `title_es` comes from `title_es:` in a front-matter block or from a `<!-- title_es: … -->` comment, else `null`.
- **Markdown.** The vendored `github.com/yuin/goldmark` (agent.md §3.2 allowed modules, pinned in `go.mod`, vendored under `agent/vendor`) with the GFM tables, strikethrough and task-list extensions only: no linkify, no raw HTML (raw HTML in a source file is dropped, it is never passed through), no hard wraps. Headings get GitHub-style ids (lowercase, letters, digits, `_` and `-`, each space a `-`, repeats numbered `-1`, `-2`), so `#anchor` links written for GitHub work in the output. The one HTML construct kept is the page-break line `<div style="page-break-after: always"></div>`, which becomes `<div class="pagebreak"></div>`.
- **Placeholders** (`<UPPER_SNAKE_CASE>`, 2 to 64 characters) are replaced in the Markdown source before parsing, in code blocks too. A value is inserted as plain text (it is escaped, never parsed as Markdown or HTML). A placeholder the inventory has no value for stays visible as `<NAME>`. `inventory.example.yaml` is read from `--src`, else from the folder above it (the root of a checkout, `src/` of sync's staging); if it is missing while a local inventory exists the values cannot be paired, nothing is filled and `LAI-CLI-013` is raised with that explanation. An `inventory.local.yaml` that is not valid YAML is `LAI-CLI-012` (exit 1). Inventory pairing: [security-model.md](../architecture/security-model.md) §5; list entries are matched by `id`, and for lists without `id` (`ark_drives`) by `label`, then `name`. A placeholder that is paired under several nodes (`<DESKTOP_USER>`) takes the value of the node chosen above, or the shared value when every node agrees. The node-state placeholders `<NODE_ID>`, `<EMERGENCY_NUMBER>`, `<BRAINS>`, `<CATALOG_VERSION>`, `<RENDER_DATE>` are filled **on the survival card only** (the other documents use `<CATALOG_VERSION>` and `<NODE_ID>` as prose, for example `catalog-<CATALOG_VERSION>`). The committed card template writes the brains as `<BRAINS_INSTALLED>`; both names are filled.
- **Links.** `foo.md` becomes `foo.html`, `foo.md#sec` becomes `foo.html#sec`; a link to a folder goes to that folder's `README` page, else to `index.html`; a link to the card template goes to `survival-card-<node-id>.html`. A link that leaves the page set (a file outside `--src`, a non-Markdown file, the unrendered `source/` folder) is not a link: its text stays and `(<GitHub URL>)` follows it in plain text, so it can be typed from paper. `http`, `https` and `mailto` links stay links (printed with their address under `@media print`); any other scheme (`javascript:`, `file:`) becomes plain text. A link whose file or `#anchor` does not exist is **reported** (a warning with the count, `-v` lists them) and the run goes on; `scripts/check-links.sh` is what keeps them out of the repository.
- **Offline rule.** A page links only `print.css` (and `card.css` on the card), by relative `<link>`; there is no `<script>`, no web font, no `url()` or `@import` in the CSS, no inline `style` attribute (the CSP of `/docs/` is `default-src 'self'; img-src 'self' data:`), and table alignment uses the `align` attribute. An image that is not a `data:` URI is replaced by `[alt text]` and never fetched. Tests grep every output page for these.
- **`index.json`** (written once per `--out`, with that folder as `root`): the shape of agent.md §3.8.29, plus a `kind` per document (`card`, `runbook`, `spec`, `architecture`, `decision`, `other`) so the Portal can list only runbooks under Manuals. The survival card is the first entry (`slug` `survival-card-<node-id>`). `index.html` lists the same documents in sections (English and Spanish headings), with the Spanish title and a PDF link where there is one.
- **Survival card.** `docs/runbooks/00-survival-card.md` is rendered for this node to `<root>/docs/survival-card-<node-id>.{html,pdf}` ([survival-portal.md](survival-portal.md) §4.12), with `card.css` (US Letter, 0.25 in margins = the 8.0 × 10.5 in safe area, headings 18 pt, body 11 pt, black on white, two columns). Sources of the node state: `node.id` and `portal.emergency_number` (default `911`) from `/etc/localai/node.yaml`; `<BRAINS>` is the display names (from the catalog's `models.yaml`, else the model id) of every `models/<id>/MANIFEST.json` with `state: verified` under the content roots (`/srv/localai` and each drive's `localai/`), or "none yet / ninguno todavía"; `<CATALOG_VERSION>` is the `CATALOG_VERSION` of `/etc/localai/catalog`, else `<root>/kit/catalog`, else a manifest's `catalog_version`, else `unknown`; `<RENDER_DATE>` is the local date; `<DESKTOP_USER>` is `nodes[id=<node>].desktop_user`, else the first `desktop_user` string found in `/var/lib/localai/install/state.json` (installer.md: the installer records the primary desktop user there). A secret placeholder in the template (`secret_fields` of the example inventory or the built-in list, see Placeholders) is refused **before anything is rendered or written**: exit 1, `LAI-CLI-016`, naming the placeholders (never a value). A card whose PDF has more than one page (counted as `/Type /Page` objects) is refused the same way (`LAI-CLI-015`, nothing written); with no PDF printer the check cannot run and the card is HTML only. If the template does not exist the card is skipped with a warning.
- **PDF.** For every page and the card: `<chromium|chromium-browser|google-chrome|google-chrome-stable> --headless --no-sandbox --disable-gpu --disable-dev-shm-usage --no-pdf-header-footer --user-data-dir=<temp> --print-to-pdf=<file> file://<html>`, found in the fixed PATH of `internal/execx` (no shell, a minimal environment with `HOME` in the scratch folder, 60 s per page, scratch folder removed afterwards). No PDF library is vendored. No browser: `LAI-CLI-014` (info), HTML only, exit 0; `--no-pdf` skips the search and says nothing. A page the browser fails on keeps its HTML, has no PDF and is reported (`pdf: null` in `index.json`). PDFs are optional: HTML-only output is valid, and sync's docs verification then marks docs yellow instead of failing ([ark-sync.md](ark-sync.md) §4.9.8). (`START-HERE.html` on the drive is written by sync, not by this command: [ark-sync.md](ark-sync.md) §4.15.)
- **Atomic output.** Per `--out`: the files are written to `<out>.tmp-<random>/`, an existing `<out>` is renamed to `<out>.prev`, the new folder is renamed to `<out>`, and only then `.prev` is removed. If a step fails the old `<out>` is put back and the temporary folder is removed. A crash between the two renames is repaired by the next run (`.prev` is restored first). Files are `0644` and folders `0755`.
- **Order of checks.** (1) every `--out` against the git guard, (2) the card's secret placeholders, (3) the PDFs and the card's page count, (4) the writes. Only (4) touches `--out`.
- Offline: works.
```
✓ Rendered 12 runbooks, 14 specs and 17 other documents to /srv/localai/docs (PDF: 44 files).
i Survival card: /srv/localai/docs/survival-card-ai-n3.html
! inventory.local.yaml not found; real addresses are shown as placeholders.  [LAI-CLI-013]
```
`--json` (the one document on stdout): `{"outputs": ["/srv/localai/docs"], "pages": 43, "pdf": 44, "inventory": true, "survival_card": "survival-card-ai-n3"}`; `survival_card` is `null` when there is no template; `pages` does not count the card, `pdf` does. The summary line says `N runbooks and M specs` when there are no other documents.

### 4.23 `aictl rebuild-llama`  [local] [root]  [3 h]
```
aictl rebuild-llama [--commit <40hex|short>] [--backend cuda|vulkan|cpu] [--cuda-arch <list>]
                    [--source <dir> | --from-kit | --prebuilt <tarball>]
                    [--jobs <n>] [--activate | --no-activate] [--keep <n>] [--dry-run] [--yes] [--json]
```
This section is authoritative for the flags (rebuild-kit.md §4.3 refers here); the build itself is [llama-router.md](llama-router.md) §4.10 and the offline preflight is [rebuild-kit.md](rebuild-kit.md) §4.3. `--cuda-arch <list>`: semicolon-separated compute capabilities without the dot (e.g. `75` or `75;86`); default from `node.yaml`/`nvidia-smi`. `--yes` skips the confirmation; the global `--json` prints the result document. `--from-kit` forces the kit tarball/mirror as source, `--prebuilt` restores an exported build ([rebuild-kit.md](rebuild-kit.md) §4.2), `--dry-run` runs only the preflight checks and exits 0 only if all pass (the drill uses it), `--no-activate` builds and smoke-tests without switching `current` (default `--activate`), `--keep` builds to keep besides `current` (default 2: the previous build and the newest other; the previous build is never deleted). Runs the build manager in-process ([llama-router.md](llama-router.md) §4.10). After a successful build and smoke test it switches `current` (atomic symlink) and calls `POST /services/llama/restart` (the agent takes its model lock, restarts and restores models); if that fails it switches back and restarts again. Offline: works when the source is in `/opt/localai/llama.cpp/src/` or in the kit; otherwise exit 1 `LAI-KIT-002` (the kit doesn't have the needed llama.cpp version). `LAI-LLM-018` means only "building llama.cpp failed" (exit 1). After the switch the check is `/health` within 60 s, then a load of the default model and a 16-token completion, all within 180 s; failure → switch back, `LAI-LLM-019`, exit 1 ([llama-router.md](llama-router.md) §4.10.3).
```
… Building llama.cpp f3586e66b for cuda (sm 75), 5 parallel jobs  (about 20–40 minutes)
✓ Build f3586e66b-cuda passed its test (CUDA0: NVIDIA GeForce RTX 2070 with Max-Q Design).
✓ Switched to the new build. Qwen 3.5 9B is loaded again.
```
`--json`: `{"build_id","switched","previous_build","build_json":{…}}`.

### 4.23a `aictl rebuild-whisper`  [local] [root]  [1 h]
```
aictl rebuild-whisper [--commit <sha>] [--backend auto|cuda|vulkan|cpu] [--source <dir> | --from-kit | --prebuilt <tarball>]
                      [--activate | --no-activate] [--dry-run] [--yes]
```
Same build manager and flow as `rebuild-llama`, for whisper.cpp ([companion-services.md](companion-services.md) §4.2, requested in its §11): builds into `/opt/localai/whisper.cpp/builds/<commit>-<backend>`, switches `current`, then `POST /services/whisper/restart`. Offline: works with the kit tarball.
```
✓ Build 1a2b3c4d5-cuda of whisper.cpp passed its test. Speech-to-text is running again.
```

### 4.23b `aictl rebuild-agent`  [local] [root]  [1 h]
```
aictl rebuild-agent [--from-kit] [--dry-run] [--yes]
```
Builds `aiagent` from `kit/src/local-ai-<tag>-src.tar.gz` with the kit Go toolchain, fully offline (`GOPROXY=off`, vendored modules), then swaps it in with the self-update rules (keep `aiagent.prev`, restart, 60 s health check, rollback) ([rebuild-kit.md](rebuild-kit.md) §4.3.4). `--dry-run` checks that the toolchain and the vendored source are present and complete (`LAI-KIT-030` otherwise); the drill uses it. Offline: works (its purpose). Exit 1 on build failure or rollback.
```
✓ Built aiagent v0.4.0 from the ark kit (no internet used). The agent restarted and is healthy.
```

### 4.23c `aictl kit …`
The rebuild kit commands. [rebuild-kit.md](rebuild-kit.md) §3.5 is authoritative for their semantics; transport rules are as for `rebuild-*` (run in-process as root; use the agent API only to restart services when it is up).

| Command | Marks | Offline | Notes |
|---|---|---|---|
| `aictl kit` / `aictl kit status [--root <id>]` | API (`GET /ark`), local fallback | works | which kit parts are present and verified for this node, and for every pinned node on a drive (`LAI-KIT-040`/`041`/`042`) |
| `aictl kit verify [--root <id>] [--part <name>] [--full]` | [local] | works | re-hash the kit ([rebuild-kit.md](rebuild-kit.md) §4.4); `--part` limits it to one top-level kit part of rebuild-kit.md §3.1 (unknown part → exit 2); mismatches exit 1 with `LAI-KIT-001` |
| `aictl kit install <kiwix-tools\|pmtiles\|piper\|agent>` | [local] [root] | works | install a tool from the kit into `/opt/localai`, verifying first |
| `aictl kit install-debs [--root <id>] [--packages <p,…>]` | [local] [root] | works | install packages from the kit's local apt repo ([rebuild-kit.md](rebuild-kit.md) §4.1.4); the fix for `LAI-KIT-010`/`011` |
| `aictl kit export-build` | [local] [root] | works | export this node's current llama/whisper builds into `kit/prebuilt/` on every writable root |

`--json` prints the status / verify report. Exit 2 for an unknown part, 5 for no root with a kit.

### 4.24 `aictl bench <model>`  [root]  [30 min]
```
aictl bench <model-id> [--yes]
```
API + local exec ([llama-router.md](llama-router.md) §4.13). Asks to unload current models (`--yes` skips). Offline: works. Root only (`LAI-CLI-006`, exit 4, checked before anything is asked or changed): the engine samples the GPU in-process, runs `llama-bench` with the node's llama environment and writes `/var/lib/localai/bench`, none of which an operator may do, and failing at the file after twenty minutes of measuring would be the worse way to find out.
```
… Measuring graphics memory at full context (32768 tokens)
… Measuring speed with llama-bench
✓ qwen3.5-9b-q4km on NVIDIA GeForce RTX 2070 with Max-Q Design:
  graphics memory 6.4 GB at ctx 32768 → suggested min_vram_mib: 7240
  prompt 1450 tokens/s · generation 38.4 tokens/s (41.0 with MTP, 71% accepted)
  Saved /var/lib/localai/bench/qwen3.5-9b-q4km-20261102T103000Z.json
  For the catalog PR:
    requirements:
      min_vram_mib: 7240   # measured on RTX 2070 Max-Q 8 GB, build f3586e66b-cuda, 2026-11-02
```
`--json`: the bench JSON. Exit 6 if another model operation runs; 1 `LAI-LLM-025` on failure. The measurement engine is `internal/router/bench` ([llama-router.md](llama-router.md) §4.13.1, ROUTER-10): the command implements its `Agent`, `Chatter` and `Procs` seams over the agent client, confirms the unload, and prints the returned `Result` (`Result.Snippet()` is the catalog YAML; no suggestion is printed when `Result.Disturbed`; a non-empty `Result.RestoreFailed` is printed as a warning with `aictl load <id>`). A `*bench.BusyError` is exit 6, a `*bench.Error` is exit 1 with its code.

Details. **Order:** usage and the root check; `GET /models` (a model that is `loading` is somebody else's operation: exit 6, `LAI-AGT-022`, nothing asked); `GET /key` (the key and `base_urls.local`; an address that is not on this computer is refused with `LAI-LLM-025` before anything is asked or changed, and the key is sent only in the `Authorization` header of the one chat request, never printed or put in an error); the question, **only when a model is loaded** (`The test needs the graphics memory to itself: Small VLM will be unloaded and loaded again afterwards. Continue?`; default No, `i Nothing was changed.`; with `--json` or no terminal and no `--yes` it is exit 2, `LAI-CLI-007`, nothing changed); then the engine. **Seams** (`cmd_bench_seams.go`): `Agent` is `GET /models`, `GET /models/{id}` (with `preset`), `POST /models/{id}/load` with `{"wait": true, "unload_others": false}`, `POST /models/{id}/unload` with `{}` (never forced), and `GET /events?types=model.*` opened by the engine before anything else; a `409 LAI-AGT-022` becomes `*bench.BusyError`. `Chatter` posts the one chat completion (`max_tokens: 64`) to `base_urls.local`. `Procs` is `bench.NewProcFS` and the GPU sampler is the agent's own probes (`hardware.New`). `llama-bench` gets the llama environment of the node: the `Environment=` lines of `/etc/systemd/system/localai-llama.service.d/50-env.conf` with `llama.extra_env` of `node.yaml` winning; the node id and `llama.backend` come from `node.yaml` (an unreadable file only means less detail). **Progress** (stderr, from the engine's own steps, since the engine has no hook): `… Unloading Small VLM so the test has the memory to itself`, `… Measuring graphics memory at full context (32768 tokens)` (`memory` on a CPU-only node), `… Measuring speed with llama-bench`, `… Loading Small VLM again` for each model put back. **Units:** memory is shown with `GBFromMiB` (MiB ÷ 1024, as `models show` and `status` do), so 6581 MiB reads `6.4 GB`; the suggestion is the engine's integer `min_vram_mib`. A CPU-only result says `memory` and `min_ram_mib`. **Warnings** (exit 0): a disturbed result keeps the numbers, prints no suggestion and says `Another model operation ran during the test, …` with the fix `Run aictl bench <id> again …`; models in `RestoreFailed` are named with `Load it with aictl load <id>`. **Exits:** `*bench.Error` with `LAI-AGT-022` is 6, `LAI-AGT-021` is 5, `LAI-LLM-008` is 6, `LAI-LLM-025` is 1 and its message ends with `(<step>: <cause>)`; Ctrl-C is 130 after the engine has loaded the previous models again (`Stopped. The models that were loaded before were started again.`); the deadline is 7. `--json` is the result file's JSON on one line (stderr empty); a failure is the error envelope with `details.step`.

### 4.25 `aictl backup-config`  [local] [root]
```
aictl backup-config [--out <file>] [--to-ark] [--no-secrets]
aictl backup-config --restore <file> [--yes]
```
Creates `localai-config-<node-id>-<YYYYMMDDTHHMMSSZ>.tar.gz` (0600 root) containing `/etc/localai/` (node.yaml, catalog/, models.ini, api-key, agent-token, cloud-credential, admin-pin, hf-token if present), `/var/lib/localai/state.json`, the generated unit files, and a `MANIFEST.json` with sha256 per file. `--no-secrets` excludes the five secret files. `--to-ark` writes it to the drive's kit config folder ([rebuild-kit.md](rebuild-kit.md) §Config backups) instead of the current directory. `--restore` verifies the manifest, shows what will be replaced, keeps each replaced file as `<file>.pre-restore`, restores with original modes/owners, `daemon-reload`, and restarts the agent. Offline: works.
```
✓ Saved ./localai-config-ai-n3-20261102T150405Z.tar.gz (14 files, includes secrets — keep it private).
```
Details (AICTL-07, `internal/cli/cmd_backup.go`, `internal/cli/local/backup.go`, `restore.go`):

**Making a backup.**
- Root check first (exit 4). `--restore` cannot be combined with `--out`, `--to-ark` or `--no-secrets`, and `--to-ark` not with `--out` (exit 2). Without `--out` the file goes to `./`; `--out` may be a file or an existing folder (the generated name is then used inside it). An existing file is **never overwritten** (`LAI-CLI-012`, "it already exists").
- The `<node-id>` comes from `node.id` of `node.yaml`, read without validating the rest of the file so a damaged `node.yaml` does not stop a backup; an id that is not `ai-n<N>` becomes `unconfigured` (the id ends up in file and folder names). Files that do not exist are skipped (`hf-token`, the drop-in); a source that is a symlink or other non-regular file, or larger than 8 MiB, is an error naming it (a backup never follows a link). A node with nothing to save is an error.
- `MANIFEST.json` is the first entry. Per file: `path` (no leading slash: `etc/localai/node.yaml`), `sha256`, `size`, `mode` (octal string, `"0600"`), `uid`, `gid`, and `owner`/`group` (the names behind the ids, so a restore on a rebuilt machine gives the file to the same account even when the numeric ids differ); top level `node_id`, `created_at`, `agent_version`, `secrets_included`. The output line says `includes secrets — keep it private` or `no secrets`.
- `--to-ark` uses the AI-ARK drives found as in §4.11.3 (no drive: exit 5, `LAI-ARK-001`; the first writable one by mount path is used, with an `i` line when several are mounted; a read-only one fails with `LAI-CLI-012`). Unless `--no-secrets`, it asks first (default No, `--yes` skips, no terminal without `--yes` is exit 2): "The AI-ARK drive at <mount> is not encrypted and this backup holds secrets … Save it there anyway?"; declining writes nothing and exits 0. It creates `<root>/kit/config/<node-id>/` (`kit/` 0755, the two below it 0700) and afterwards keeps the newest 5 files named exactly `localai-config-<node-id>-<UTC time>.tar.gz` there (other names and other nodes' backups are never touched; a failed removal is a warning).
- Audit: `config.backup` with `target.path`, `target.node` and `target.includes_secrets`. `--json`: `{"path","node_id","files","secrets_included"}`.

**Restoring.** The file is read completely and verified **before** anything on the node changes:
- Refused (`LAI-CLI-012`, exit 1, "This file cannot be restored: …"): not a gzip tar; an entry that is a symlink, hard link or device; a name that is absolute, has a `.`/`..`/empty segment, a backslash or a NUL; a duplicate; an entry over 8 MiB or more than 64 MiB in total; no `MANIFEST.json`; a manifest and an archive that do not list exactly the same files; a size or sha256 that does not match; a `mode` that is not octal, above 0777 or world-writable; a manifest that says `secrets_included: false` but lists a secret file.
- **Allowlist.** A manifest may name only `etc/localai/{node.yaml,models.ini,api-key,agent-token,cloud-credential,admin-pin,hf-token}`, files below `etc/localai/catalog/`, `var/lib/localai/state.json`, `etc/systemd/system/localai-llama.service` and `…/localai-llama.service.d/50-env.conf`. Anything else (`etc/passwd`, another unit) is refused, so a crafted backup cannot write elsewhere.
- It then lists what will be written (secret files marked, files already identical left out), warns when the backup is from another node (`This backup is from node ai-n3, and this node is ai-n9.`), and confirms (default No; `--yes`; no terminal without `--yes` is exit 2). If nothing differs it says `Nothing to restore` and exits 0.
- **`.pre-restore`.** Under the lock of §4.11.3, every file that exists and will change is first copied, with its mode and owner, to `<file>.pre-restore` (for `/etc/localai/admin-pin`: `/etc/localai/admin-pin.pre-restore`; the catalog folder is renamed to `catalog.pre-restore/`); a file that is already identical to the backup gets no copy, so a restore that was interrupted and is run again keeps the true originals. Then each file is written atomically with the manifest's mode and the owner (account names first, then the numeric ids, then root); the catalog is staged in `catalog.restoring/` and swapped in last, so the agent sees `CATALOG_VERSION` as the newest write; files of the live catalog that are not in the backup disappear with the swap (they are in `catalog.pre-restore/`).
- Finally `systemctl daemon-reload` and `systemctl restart localai-agent.service` (fixed argv through `execx`). If either fails the files are in place, the command prints `! Restored N files, but the agent could not be restarted. Run …` and exits 1.
- Audit: `config.restore` with `target.path`, `target.node`, `target.files`. `--json`: `{"restored","kept","restarted"}` (`{"restored":0}` when nothing differed).

### 4.26 `aictl version`  [5 s]
```
aictl 0.1.0 (a1b2c3d, linux/amd64, built 2026-11-01T12:00:00Z)
agent 0.1.0 · llama.cpp f3586e66b-cuda · catalog 2026.11.02-1
```
CLI line is local; the second line needs the agent (`/health` + `/status`); agent down → `agent  not reachable` and exit 0. `--json`: `{"cli": {…}, "agent": {"version"} | null, "llama_build", "catalog_version"}`.

Details (AICTL-04). `cli` is `{version, commit, os, arch, built_at}`; `llama_build` and `catalog_version` are `null` when unknown (agent down, no llama build, `/status` failing after `/health` answered: the second line then shows the agent version alone). "Agent down" means exit 3 (not reachable, or still starting after 10 s); any other failure of `/health` (not allowed, timed out, undecodable) is that failure with its own exit code, because "your user may not talk to the agent" (`LAI-CLI-002`) is information `version` must not hide. A different agent version raises the usual `LAI-CLI-003` warning on stderr after the two lines (also with `--json`, since the command succeeded).

### 4.27 `aictl update`  [local] [root]  [15 min]
```
aictl update [--version <x.y.z>] [--check] [--yes]
```
Self-update in-process (agent.md §4.10, steps 1–8), then waits up to 120 s for the restarted agent's `/health` to report the new version (or the rollback). `--check` only resolves and prints the newest version (`LAI-AGT-015`). Needs internet; offline/survival → exit 6. Recovery without internet: copy the binary from the kit (rebuild-kit.md).
```
i Version 0.2.0 is available (you have 0.1.0).
Update now? The agent restarts; models keep running. [y/N] y
… Downloading and checking the signature
✓ Updated to 0.2.0. Everything is running.
```
Rollback → exit 1 `LAI-AGT-009`. Bad signature → exit 1 `LAI-AGT-010`.

### 4.27a `aictl verify-release <dir>`  [local]  [60 s]
```
aictl verify-release <dir> [--json]
```
Verifies a downloaded or kit release directory **offline**: `SHA256SUMS.minisig` against the minisign public key embedded in the binary, then every file listed in `SHA256SUMS` (the same checks as self-update, agent.md §4.10). Used for the desktop `.deb` and the agent binaries in the kit ([desktop.md](desktop.md) §4.11, [rebuild-kit.md](rebuild-kit.md) §3.4). Needs no agent and no root. Exit 0 all good; 1 a hash or signature mismatch (`LAI-AGT-010`, or `LAI-KIT-044` for a kit directory); 2 missing `SHA256SUMS*`. `<dir>` is a **kit directory** when it contains `kit/MANIFEST.json`, or is itself a `kit/` directory with a `MANIFEST.json`; anything else is a release directory.
```
✓ Signature OK (Local AI release key). 4 files checked: all match.
```

### 4.28 Commands not in the public surface (internal, not a stable CLI)
Documented here so nobody re-invents them. They may change between versions without notice:
- `aiagent serve`, `aiagent version --json`, `aiagent update-rollback-check` (agent.md §4.10).
- `aiagent migrate llama-qwen [--dry-run|--rollback|--accept-catalog-flags|--restore-files]` ([llama-router.md](llama-router.md) §4.9). Root only. `aiagent migrate` with no argument runs every pending migration (today only `llama-qwen`) and exits 0 when there is nothing to do; the installer calls it ([installer.md](installer.md) §4.3 P8).
- `aiagent setup <step> [--dry-run] [--json]`, the installer's steps (`preflight`, `profile-suggest`, `dirs`, `node-yaml`, `catalog`, `harden`, `binaries`, `units`, `api-key`, `migrate`, `verify`, `summary`). Root only; owned by [installer.md](installer.md) §3.5.

`aictl help` lists only §4.1–§4.27a (including §4.23a–c).

### 4.29 Offline behaviour summary
| Works with no network at all | Needs network | Refused in survival |
|---|---|---|
| status, models, load, unload, default, delete, key show/copy/rotate/snippets, mode, ark status/verify/import/drives/eject/init/mount/prune, catalog lint/restore/use --test, profile *, services (+ start/stop/restart), restart, logs, doctor, drill (+ show, record-manual), cloud status, pin set, ui-code, token rotate, docs render, rebuild-llama/rebuild-whisper (kit source), rebuild-agent, kit *, verify-release, bench, backup-config, version | download, ark sync, catalog refresh, update, cloud enroll (stores the credential offline; connects later), cloud resend-key, rebuild-llama without local source | download, ark sync (`LAI-SYN-010`), catalog refresh, update |

## 5. Failure handling and edge cases
| Situation | Behaviour | Code / exit |
|---|---|---|
| Agent not running | Socket and TCP both fail; message tells the user to run `sudo systemctl start localai-agent` | `LAI-CLI-001` / 3 |
| User not in `localai-operators` | Socket `EACCES`, token unreadable | `LAI-CLI-002` / 4 (fix: `sudo usermod -aG localai-operators $USER`, then log out and in) |
| Agent restarting during a command | Retries for 10 s | — / 3 if still down |
| Agent restarts during a long wait (load, sync) | SSE `resync`/`shutdown`/disconnect → the CLI reconnects with `Last-Event-ID` every 1 s, re-reads the resource status (also while the stream is down) and reports it; an interrupted load shows the router's real state (§3.3.1) | — |
| Admin command, no TTY | No prompt possible | `LAI-CLI-008` / 4 |
| `--json` with a command that needs confirmation and no `--yes` | Refuse | `LAI-CLI-007` / 2 |
| Ctrl-C while waiting | Stop waiting, operation continues | `LAI-CLI-010` / 130 |
| Unknown command or flag | Short usage + "Run `aictl help`" | `LAI-CLI-005` / 2 |
| Response not decodable | Print request id | `LAI-CLI-009` / 1 |
| Clipboard tool missing | Suggest `key show` | `LAI-CLI-011` / 1 |
| Local file op fails (render/backup/restore) | Name the file and the OS error in plain words | `LAI-CLI-012` / 1 |
| A local root command runs while another one that changes `/etc/localai` is running | Refuse at once; the lock is dropped by the kernel if the other process dies | `LAI-CLI-012` / 6 |
| Very long output in a pipe (`aictl logs -n 2000 \| head`) | `EPIPE` → exit 0 silently | — |

## 6. Security considerations
- Secrets (PIN, cloud credential) are read only from a no-echo prompt or stdin, never from argv or the environment.
- `key show` and `key snippets --with-key` print a warning line; `key copy` never prints the key.
- Root-only local commands check the euid before touching files; files are written atomically with the canonical modes (overview §3.2).
- The TCP fallback refuses a non-loopback `--url` (exit 2), so a token is never sent off the machine.
- `docs render` refuses to write into a git checkout, so filled-in inventory values cannot be committed by accident.
- Backups with secrets are 0600 and say so in the output.

## 7. Observability
- `-v` prints the transport, request ids and op ids.
- Every API mutation appears in the agent's audit log with `actor.via: socket uid=<n>` or `bearer`; local root commands append their own audit lines (`actor.kind: cli-root`).
- `LOCALAI_CLI_DEBUG=1` logs HTTP requests/responses (bodies scrubbed with the agent's scrubber) to stderr.

## 8. Doctor codes (`LAI-CLI`)
| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-CLI-001 | error | `aictl` cannot reach the agent | Start it: `sudo systemctl start localai-agent`. If it keeps stopping: `journalctl -u localai-agent -n 50` |
| LAI-CLI-002 | error | Your user may not talk to the agent | Run `sudo usermod -aG localai-operators $USER`, then log out and back in |
| LAI-CLI-003 | warning | `aictl` and the agent are different versions | Usually temporary during an update. If it stays, run `sudo systemctl restart localai-agent` |
| LAI-CLI-004 | error | The command took too long | Check `aictl status`; the action may still finish in the background |
| LAI-CLI-005 | error | Unknown command or option | Run `aictl help` |
| LAI-CLI-006 | error | This command must be run as root | Run it with `sudo aictl …` |
| LAI-CLI-007 | error | This command needs a yes/no confirmation | Run it in a terminal, or add `--yes` |
| LAI-CLI-008 | error | The admin PIN is needed but there is no terminal to ask for it | Run it in a terminal, use `sudo`, or pass `--pin-stdin` |
| LAI-CLI-009 | error | The agent sent an answer `aictl` does not understand | Update with `sudo aictl update`, or report the request id |
| LAI-CLI-010 | info | You stopped waiting; the action continues in the background | Check with `aictl status` |
| LAI-CLI-011 | warning | No clipboard tool is available | Use `aictl key show` and copy the key by hand |
| LAI-CLI-012 | error | A file could not be read or written | Check the path and free space named in the message |
| LAI-CLI-013 | warning | `inventory.local.yaml` was not found; placeholders were left in the documents | Plug in the ark drive or pass `--inventory <file>` |
| LAI-CLI-014 | info | No PDF printer (Chromium) is installed; only HTML was made | Install `chromium` to also get PDFs, or print the HTML pages from a browser |
| LAI-CLI-015 | error | The survival card does not fit on one page | Shorten the text in `docs/runbooks/00-survival-card.md` (survival-portal.md §4.12) |
| LAI-CLI-016 | error | The survival card would show a secret value (an address, the tailnet or an e-mail) | Remove that placeholder from `docs/runbooks/00-survival-card.md` (survival-portal.md §4.12) |

## 9. Test plan

### 9.1 Unit tests (table-driven)
| Area | Cases |
|---|---|
| Transport | socket ok; socket ENOENT → TCP ok; socket EACCES + token unreadable → exit 4; both down → exit 3; 503 retried then success; non-loopback `--url` refused |
| Exit code map | every row of §3.4 from a fake agent returning that status/code |
| Admin flow | root over socket (no prompt); operator → 403 pin_required → prompt → grant retry; `--pin-stdin`; no TTY → `LAI-CLI-008`; wrong PIN 3× |
| Output | golden files for every command's human output in `en` and `es`, colour on/off, UTF-8 and ASCII symbol sets; problem layout (message, code, fix); 80-column wrap |
| `--json` | every command emits exactly one JSON document; errors as envelope on stdout with empty stderr |
| Confirmations | default No; `--yes`; non-interactive without `--yes` → exit 2 |
| Read-only commands (AICTL-04) | golden `en`/`es` for `status` (online, survival, partial), `models`, `models show`, `default`, `services`, `logs`, `doctor` (mixed, warnings, all fine, `--all -v`), `version`, `ark`, `ark drives`, `cloud status`, `profile show/suggest` from fixture bodies that are valid answers of `agent/api/openapi.yaml`; `--json` equals the agent body (or the documented composite); `doctor` exits 0/8/9 with and without `--json`; agent down (exit 3, `version` still 0), 401/403 (exit 4), 503 starting retried, undecodable answer (exit 1 with request id), partial bodies, unknown fields ignored, 2000 long log lines, closed pipe (exit 0, `logs -f` stops), `logs -f` de-duplication and Ctrl-C, `status --watch` redraw on tick, on event and after a failed refresh |
| Progress | SSE fake over the real event bus: load progress (TTY golden, non-TTY lines, `-q`, `--json`), rollback events, `Last-Event-ID` on reconnect, `gap` and `restart` resync, shutdown, agent down mid-wait (re-read finishes the wait), idle stream, deadline → exit 7, Ctrl-C → exit 130 and no cancel request sent, interrupt hook, SSE parser (multi-line data, comments, missing id, CRLF, oversized line) |
| Model and service commands (AICTL-05) | request bodies from the flags (`load` `wait:false` and `unload_others`, `unload` `force`, `download` `roots` and `allow_offline_mode`, `delete` `keep_last` and `root`, `restart` `force`); the model id format check (exit 2 before any request) for every command; fixture bodies that are valid answers of `agent/api/openapi.yaml`; `load` with scripted events in the order the orchestrator publishes them (eviction, progress, a rollback that loads the previous model again under the same `op_id`, `model.failed` with `LAI-LLM-009`/`010`/`012`), exit 0/1/7, human `en`/`es`, `--json`, `--quiet`, `--no-wait` (no event stream), already loaded, a lost stream settled by `GET /models/{id}`, Ctrl-C (exit 130, no unload ever sent); every refusal of §4.3 to §4.7 and §4.14 (404, 409 `LAI-AGT-022`/`023`/`LAI-SYN-010`, 422, 500, 502, 503, agent down) with its exit code; `download --wait` with the item verified, failed, stopped, another run's events, a lost stream, Ctrl-C; `delete` question (name and size), No by default, `--yes`, `--json` needs `--yes`, `--allow-last-copy` typed confirmation (right, wrong, empty, `--yes` does not answer it, no terminal), the PIN flow of an operator (after the confirmation, never after a No, `--pin-stdin`, `LAI-CLI-008`); `restart` for the three services; `bench` with the real engine over a fake machine (agent, chat endpoint, GPU that follows the loaded models, `llama-bench`): the question, `--yes`, nothing loaded, `en`/`es`, `--json` equal to the result file, the node's llama environment, not root, busy, unknown model, files missing, chat failure, `llama-bench` failure, an address that is not this computer, restore failure warning, a disturbed run, Ctrl-C with the models put back, the key in no output |
| Local commands | `pin set` writes argon2id PHC string with mode 0600; `token rotate` format `lat_` + 43 chars; `catalog restore` atomic swap and lint; `backup-config` manifest and modes; `--restore` round trip; `docs render` refuses `.git` output, fills placeholders from a fixture inventory and follows the inventory lookup order of §4.22; `catalog use --test` refuses a failing lint (exit 2), keeps `catalog.prev/` and `restore` puts it back (also after two `use --test` in a row); every local root command: not root exits 4 before any file is touched, an interrupted write (injected rename failure) keeps the old file and leaves no temp file, a second concurrent run exits 6, an audit line with `actor.kind: cli-root` for success and failure, and no secret in any output; `pin set` round trip with the agent's verifier, bad length / non-digits / mismatch exit 2, `--pin-stdin`; `--restore` refuses path traversal, absolute paths, links, unlisted paths, hash and mode tampering, and keeps `.pre-restore` copies; `catalog lint` output matches `<file>:<yaml-path>  LAI-CAT-0NN  <message>` and the `--json` shape |

### 9.2 Integration tests
Run `aictl` against a real `aiagent serve` started by the integration harness (agent.md §9.2) with fakes behind it: `status`, `load`/`unload` with rollback, `mode survival` progress, `doctor` exit codes 0/8/9, `ark sync --scheduled` in offline mode exits 0 with "skipped"; `drill --scheduled` while busy exits 0 with "skipped"; `ark sync --wait` with one failing item exits 8.

### 9.3 Manual tests on a real node (ai-n3)
| # | Command | Expected |
|---|---|---|
| 1 | `aictl status` (as the desktop user, in `localai-operators`) | summary as §4.1, exit 0 |
| 2 | `sudo -u nobody aictl status` | `✗ … [LAI-CLI-002]`, exit 4 |
| 3 | `sudo systemctl stop localai-agent; aictl status; echo $?` | `[LAI-CLI-001]`, `3`; then start it again |
| 4 | `aictl load small-vlm-q4km && aictl load qwen3.5-9b-q4km` | both succeed; progress line; `aictl models` reflects it |
| 5 | `aictl delete small-vlm-q4km` (as operator, not root) | PIN prompt, confirmation, deletion |
| 6 | `aictl doctor; echo $?` | `0`, `8` or `9` consistent with the listed problems |
| 7 | `aictl key copy` in the desktop session, paste into a text editor | the key; nothing printed in the terminal |
| 8 | `aictl --json models \| jq '.models[0].id'` | a model id; no other output |
| 9 | `LANG=es_CR.UTF-8 aictl status` | Spanish labels; codes unchanged |
| 10 | `aictl mode survival`, then `aictl mode online` as operator | survival without PIN; leaving works for an operator over the socket |
| 11 | `aictl status --watch` in one terminal; in another `aictl load small-vlm-q4km` | the Brain line changes within about a second of the event (not 2 s later); Ctrl-C leaves with exit 0 |
| 12 | `aictl models`, `aictl models --all`, `aictl models show qwen3.5-9b-q4km`, `aictl default` | rows agree with `aictl --json models \| jq '.models[] \| {id,status}'`; the default matches the Brain line of `aictl status` |
| 13 | `sudo systemctl stop localai-whisper; aictl services; aictl status; sudo systemctl start localai-whisper` | whisper `stopped ✗` (with `last exit code` only if it crashed), `✗ whisper` on the Services line; healthy again after the start |
| 14 | `aictl logs --unit llama -n 20 --since 10m`; `aictl logs -f` while `aictl load …` runs; `aictl logs -n 2000 \| head -3; echo ${PIPESTATUS[0]}` | the last 20 lines of the last 10 minutes; new lines appear once each; Ctrl-C leaves with exit 0; the piped command ends silently with `0` |
| 15 | `aictl doctor --fast --area HW; echo $?`, then unplug the charger and run `aictl doctor --all -v; echo $?` | `0`, then the battery warning with `check: hw.battery` and exit `8` (or `9` with an error listed) |
| 16 | `aictl version`; `sudo systemctl stop localai-agent; aictl version; echo $?; sudo systemctl start localai-agent` | both lines; then `agent  not reachable` and `0`; with an older `aictl` binary the skew warning `[LAI-CLI-003]` on stderr |
| 17 | `aictl ark` and `aictl ark drives` with the ark drive unplugged, then plugged in, then with a non-ext4 stick named AI-ARK | `Ark drive  not connected`; the drive's line `✓ ready`; the stick `✗ cannot be used` with its `[LAI-ARK-002]` problem |
| 18 | `aictl cloud status` linked, then `aictl mode offline` | `✓ linked`, a heartbeat time, a masked credential; then `i paused until the computer is online` |
| 19 | `aictl profile show; aictl profile suggest` | the profile in use with its plan; `✓ This computer matches "<id>" (cuda, 8.0 GB graphics memory, 14.0 GB memory).` |
| 20 | Pull the network cable and stop Tailscale, then `time aictl status; echo $?` | `Internet  not checked`, exit `0`, well under a second (no read-only command waits for the network) |
| 21 | `sudo -u nobody aictl mode survival`, then `sudo -u nobody aictl mode online` (a user outside `localai-operators`, token unreadable) | survival starts (`i Survival mode is starting. …`, the Portal opens within a minute) with no PIN and no token; the second command fails with `LAI-CLI-002`, exit 4 |
| 22 | `aictl ark sync --scheduled; echo $?` in offline mode, then in survival mode | `i Sync skipped: the node is in … mode.`, `0` (never `3`) |
| 23 | `aictl ui-code`, open the address, type the code; type it again | signed in once; the second time is refused |
| 24 | `sudo aictl key copy` in a Wayland session, in an X11 session, and with `wl-copy`, `xclip` and `xsel` hidden from `/usr/bin` | the key is pasted in an editor and nothing is printed; with none, OSC 52 in a terminal that supports it, else `LAI-CLI-011` exit 1 |
| 25 | `aictl ark sync --wait` with the ark drive connected; Ctrl-C and answer `n`; run again, Ctrl-C and answer `y` | `n`: `Still running in the background`, the run continues; `y`: the run stops, `.part` files stay, exit 130 |
| 26 | `printf '%s\n' "$NODE_CRED" \| sudo aictl cloud enroll --credential-stdin` with the network cable out, then plug it in | `! Saved. … [LAI-CLD-053]`, exit 0; the node appears on the Nodes page by itself |
| 27 | `aictl ark import` with one drive, then with two | copies from the one; with two, exit 2 and the list of root ids |
| 28 | `aictl profile set <other-profile>`; answer `y`; `aictl profile show` | the preview lists models and content with sizes; the profile changes; nothing is deleted |

## 10. Implementation breakdown

1. **AICTL-01 CLI framework and transport** — command tree (stdlib `flag` per subcommand), global flags, socket/TCP fallback, token file, `/health` version check, retry on 503, error envelope decoding, exit-code map. *Accept:* §9.1 transport and exit-code cases. *Deps:* AGENT-01, AGENT-05.
2. **AICTL-02 Output layer** — style guide §3.3 (symbols, colours, ASCII fallback, problem layout, sizes/times formatting, en/es strings), `--json` writer, confirmations, PIN prompt/grant flow. *Accept:* golden tests for the layout; `NO_COLOR`/non-TTY behaviour. *Deps:* AICTL-01.
3. **AICTL-03 Progress via SSE** — event subscriber, TTY progress line and plain-line mode, Ctrl-C semantics, resync handling. *Accept:* §9.1 progress cases. *Deps:* AICTL-01, AGENT-08.
4. **AICTL-04 Read-only commands** — `status`, `models`, `services`, `logs` (+`-f`), `doctor` (exit 0/8/9), `version`, `ark status|drives`, `cloud status`, `profile show|suggest`, `default` (show). *Accept:* golden outputs; manual 1, 6, 8, 9. *Deps:* AICTL-02.
5. **AICTL-05 Model and service commands** — `load`, `unload`, `default <id>`, `download`, `delete`, `restart`, `bench` (command wiring, confirmation, human/`--json` output and exit codes only; the measurement engine is ROUTER-10, [llama-router.md](llama-router.md) §4.13). *Accept:* integration load/rollback; manual 4–5. *Deps:* AICTL-03, ROUTER-05, ROUTER-10. *Done in* `internal/cli/cmd_{load,unload,default,download,delete,restart,bench}.go` (+ `cmd_bench_seams.go`, `cmd_model_common.go`). The load/rollback scenario runs against a fake agent that publishes the orchestrator's event sequence; the run against the real `aiagent serve` harness waits for AGENT-20.
6. **AICTL-06 Key, mode, ark and cloud mutations** — `key show|copy|rotate|snippets`, `mode`, `ark sync|verify|import`, `catalog refresh`, `profile set`, `cloud enroll|unenroll|resend-key`, `ark drives set-profile`, `ui-code`. (`ark eject|init|mount|prune` are SYNC-03/SYNC-08 in [ark-sync.md](ark-sync.md) §10; `drill` is AICTL-10.) *Accept:* golden outputs; manual 7, 10; `ark sync --scheduled` skip semantics (exit 0). *Deps:* AICTL-03.
7. **AICTL-07 Local root commands** — `pin set`, `token rotate`, `catalog restore`, `catalog use --test` (§4.11.4), `backup-config` (+`--restore`), with audit lines (`actor.kind: cli-root`, one `O_APPEND` write per line; only the agent rotates, agent.md §4.11). `catalog lint` (linter and command) is owned by CAT-01 ([catalog.md](catalog.md) §10); this item only checks its output against §4.11.1. *Accept:* §9.1 local command cases. *Deps:* AICTL-02, AGENT-02, AGENT-04, AGENT-06.
8. **AICTL-08 Docs render** — goldmark rendering, placeholder fill from inventory, link rewriting, index.json, optional Chromium PDF, `.git` guard. (START-HERE.html is written by sync, SYNC-14.) *Accept:* fixture render; redaction check passes on the repo (no output inside it). *Deps:* AICTL-02.
9. **AICTL-09 Build and update commands** — `rebuild-llama` command surface only: flag parsing, output and exit codes (build engine and switch: ROUTER-08, [llama-router.md](llama-router.md) §10; offline flags/preflight/activation extensions: KIT-04, [rebuild-kit.md](rebuild-kit.md) §10), `update` (agent.md §4.10 in-process, wait for new version). *Accept:* build on ai-n3; update against a fake release server with rollback. *Deps:* AICTL-06, ROUTER-08, KIT-04, AGENT-19.
10. **AICTL-10 Command-surface completion** — `verify-release` (§4.27a, offline minisign + SHA256SUMS check with the embedded key), `services start|stop|restart` (§4.13), `drill`, `drill show`, `drill record-manual` CLI surface (§4.17; engine, API and handlers: DRILL-01), `kit status|verify|export-build` and `rebuild-agent` entry points calling rebuild-kit code (§4.23b–c; the `kit install*` dispatcher is KIT-06, `rebuild-whisper` is SVC-05), help listing and the §4.29 table. *Accept:* golden outputs; `verify-release` rejects a tampered file and a bad signature; `aictl help` lists exactly §4.1–§4.27a. *Deps:* AICTL-04, AICTL-06, AGENT-19, KIT-03, KIT-05, KIT-06, DRILL-06.

## 11. Open questions
1. **PDF rendering offline.** Chromium is large and not on every node. Default: HTML is the primary output and PDFs are optional; the ark drive carries PDFs rendered on a node that has Chromium. Revisit if a pure-Go PDF path becomes necessary.
