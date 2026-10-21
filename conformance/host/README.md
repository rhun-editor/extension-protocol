# rhun extension protocol v1: host conformance suite

Plays scripted fake extensions against a rhun build and checks that the editor (the host)
implements `../../v1/v1.md` as written. It was written from the specification only.

    python3 run.py /path/to/rhun            # whole suite, about one minute with -j 4
    python3 run.py RHUN -k edit.            # only scenarios whose name contains "edit."
    python3 run.py RHUN --quick             # skip the scenarios that take more than 5 s
    python3 run.py RHUN --dump NAME         # rhun's output and the extension's log for one scenario
    python3 run.py RHUN -j 8 --keep         # more in parallel; keep the temporary folders

It prints `ok NAME` or `FAIL NAME: what differed` per check (NAME is `scenario.check`) and a
summary on stderr; the exit status is 1 if anything failed. Needs Python 3 and a `python3` on
`PATH` (the fake extension uses it as its `runtime`). Every run gets its own `HOME`,
`XDG_CONFIG_HOME`, `XDG_STATE_HOME`, project folder and extension folder under a temporary
directory; leaked extension processes are found by pid, reported and killed (never by name).

## Parts

- `ext.py` is the fake extension (standard library only). `CONF_SCENARIO`/`CONF_OPS` and a few
  `CONF_*` switches (see its docstring) select what it does: a JSON list of operations
  (`text`, `range`, `replace`, `insert`, `select`, `toast`, `status`, `open`, `cancel`, `req` for
  any verb, `raw` bytes, `batch` = one write, `send` = one write without reading replies,
  `flood`, `close`, `spawn`, `writefile`, `sleep`, `drain`, `mark` ...). Options on an operation
  can lie about the body length, split the write with a pause, or write a byte at a time. It logs
  every line it receives (`<`), every line it sends (`>`), every body (`B`/`S`: length, SHA-256 and
  the text when short), the environment (`E`) and notes (`X`) to `CONF_LOG`, one record per line.
- `manifests/OWNER/NAME/extension.ini`: one folder per manifest variant (`runtime = python3`,
  `run = EXT_PY`; the runner copies the folders it needs into a temporary `--extensions`
  folder and replaces `EXT_PY` with the real path). `full` (everything subscribed, documents
  write), `rd` (documents read), `nocap` (no `[needs]`), `nosub` (no `[subscribe]`), `gosub` and
  `pat` (name patterns), `startup`/`onlang`/`onopen`/`onmake`/`onproj` (activation), `args`,
  `wrapper` (per-target `run.*` and an executable shell wrapper), `keyed` (a default key),
  `badinterp`, `noexec`, `missing`, `noruntime`, `norun`, `miss-*`, `badname`, `badowner`,
  `badparse` (bad manifests), `slot01`..`slot17`, `tests` (a test controller, protocol 1.1), `tsug`, `trun`,
  `tout` (`[needs] terminal = suggest|run|output`, protocol 1.1 terminals and tasks).
- `run.py` prepares the project (`a.txt`, `main.go`, `utf.txt` with a two-byte character,
  `data.json`, `crlf.txt`, `with space.txt`, `Makefile`, `pic.png`), writes the control script,
  runs `rhun PROJECT --headless 1000x600 --scale 1 --extensions DIR --script FILE`, then checks
  rhun's output (`echo @label` splits it into segments for `print-state`, `print-doc`,
  `print-extensions`) and the extension's log.

## Scenarios and the specification

| Scenarios | Checks | Spec |
| --- | --- | --- |
| `manifest.listing` | good manifests listed with their commands before any start; ten bad ones recorded with a reason, never started | 2 |
| `act.*` | `startup`, default `command`, `lang:`, `open:` glob and exact name, `project` with and without a project | 2 Activation |
| `start.*`, `cmd.default-key` | `run.TARGET` program and cwd, `runtime`+`run`+`args`, bad interpreter wording, a default key runs the command | 1, 2, 4 |
| `env.*` | every variable of the table (`HOME` is the state folder, `TMPDIR` is `STATE/tmp`, caches under it), `RHUN_SOCKET` only with `--control`, no project, stderr log file, log cut back over 1 MB | 4.1 |
| `hs.*` | `ready` first, `opened` fields (`LANG` `Go`/`-`, PATH with a space, untitled `-`), order ready/opened/active/cursor, selection at catch-up, no documents (`active 0`), late hello (3.5 s), extra hello fields, CRLF line ends, hello and a request in one write, no hello in 5 s, first line not hello, `hello 2` | 3, 4.2, 4.3, 8 |
| `cmd.*` | `run COMMAND DOC` with the active document, a command given while starting is delivered after `ready`, a second one replaces it | 2, 8 |
| `read.text-and-range` | exact bodies and `ok ID VERSION LEN`, CRLF never seen, half-open ranges, `L+1 1`, LF position, two-byte characters, `badpos`/`nodoc` cases, highest request id | 6, 8 |
| `edit.*` | `replace` (versions, `stale` text is the current version, `badpos`, `nodoc`, body that looks like protocol, deletion, multibyte, `changed` versions, dirty), one undo step, cursor moves only when inside the range, `insert` (selection replaced, stale), `select` (anchor/cursor order, equal positions, hidden document keeps it), CRLF document saved with CRLF and `saved` version | 6, 8 |
| `open.request` | `ok ID DOC`, `opened` and `active` follow, same DOC for an open file, relative path, path with a space, position, clamping, `nofile`, `nodoc` for an image | 8 |
| `err.*` | `unknown` (and five in a row do not stop it), `needs documents=read|write` exact text and the body of a refused request skipped, read-only vs write, `syntax` for bad arguments, `cancel` always `ok` | 3, 5 |
| `ui.*` | status truncation at 80 bytes on a character boundary (2, 3, 4-byte), clear, replace; toast/status bodies over their limit; status removed when the extension is stopped | 7, 4.5 |
| `tr.*` | bytes one at a time, body/header split with a pause, a body shorter than LEN is read exactly, 9 messages in one write (stale sees the first), 600 in one write are not flooding, 3 MB both ways, a 60000-byte line, syntax counter reset by a good request, exit in the middle of a body, rhun's queue (8 MB at once, 1 MB for 10 s) | 3, 7 |
| `life.*` | three syntax errors, three bad requests, line over 64 KB, replace/insert body over 4 MB (header only, and fully sent), flooding; each: state `stopped`, reason wording, ended promptly, rhun's document intact, a command does not restart it. Exit after hello is `crashed` with the status, commands stay, a command restarts it, three crashes leave it `stopped`; closing stdin/stdout; `bye` on quit, SIGTERM after 300 ms, programs the extension started; 17 at once (protocol 1.1), then the other 16 staying running when one exits | 3, 4, 7 |
| `register.*`, `option.*` | `register` runs from `cmd` and a palette pick and a default key, shows "TITLE (Extension)", a name in use (earlier register, manifest command) is `refused`, malformed/overlong name or missing title `syntax`, long titles cut, a key already bound stays with its command, registered commands vanish when the extension exits or is stopped; `option` overrides one document only (and one that is not active), the four keys, `-` removes one key, bad keys/values/DOC are `syntax`/`nodoc`, Tab and save follow the overrides, never in the config file, the user's settings return when the extension exits, is stopped or the document closes, two extensions: last writer wins and one's `-` leaves the other's | 4, 6c, 8 |
| `choose.*`, `ask.*`, `term.*` | the prompt and items are shown, typing filters and `ok ID N` counts in the original list, Enter/arrows/Esc, a second prompt (`choose`, `ask`, `term`) or the open palette is `busy`, opening the palette or `cancel ID` answers `cancelled`, an open prompt goes when the extension stops; `ask` returns the text as `ok ID LEN` + body (spaces, multibyte, empty); `term` without `terminal = yes` is `needs terminal=yes`, control characters (tab, LF, CR, ESC, NUL, BEL, DEL, trailing LF) are `syntax`, the line is shown, can be edited, runs only on Enter (output and a file prove it) and not on Esc or an empty line | 3, 5, 7, 8, 8a |
| `cmd.*` | commands from the list run as the user would (`duplicate_line`, `select_all`, `undo`, `redo`) and every name of 8a is `ok` (no capability needed); `quit`, `save`, `close_tab`, an unknown name, an upper-case name, an extension command are `refused` and do nothing, no name is `syntax` | 8, 8a |
| `lim.*` | `choose` 1001 items `toobig` (1000 accepted), prompt and item text cut at 200 bytes on a character boundary, `term` 4097 bytes `toobig` (4096 accepted), the 65th `register` `toobig` | 7 |
| `tests.requests` | protocol 1.1 `tests`: rhun asks for the roots and sends `test-run`; `test-items` replace/add, unknown parent `refused unknown parent`, bad kind `syntax`, bad range or a range without a path `badpos`, 10,001 items `toobig`; `test-remove` with an unknown id; `test-update`/`test-end`/`test-coverage` for an unknown or ended run `stale run over`; `test-exec` of a program `[needs] tests` does not name (`needs tests=ls`), not a bare name, a NUL | v1.1 tests |
| `terminals.*` | protocol 1.1 `terminals`: `terminal = suggest` refuses every verb with `needs terminal=run`; `terminal-open` asks once per extension and project (the dialog shown), answers `ok ID TERM`, then `terminal-started TERM PID -` and `terminal-ended TERM exit 3 0 0`; argv form as given; `terminal-input`/`terminal-screen`/an `output` record without the level are `needs terminal=input|output`; `noterm` for an unknown or closed TERM; definition errors (`not in a terminal: ...`, `id: a terminal's is -`, `unsupported location`), an unknown record skipped; `terminal-show`, `terminal-restart` (a second start and end), `terminal-close` (`terminal-closed`); with `terminal = output`, typed input comes back as `terminal-output TERM DROPPED LEN` lines and `terminal-screen` is `ok ID LEN` + `ROWS COLS ROW COL` and the rows; a `terminal-input` body over 64 KiB stops the extension (`a body over its cap`) | v1.1 terminals |
| `tasks.*` | protocol 1.1 `tasks`: agreed only with `terminals`; `tasks-set` checked whole (`the count of tasks`, `task 1: id listed twice`, `depends: no task "nope"`, `depends: a cycle`, a bad pattern, `busy needs ready`, an unknown matcher, a bad id, `needs terminal=output`, `bytes after the last definition`); `task-start` of an unknown id is `notask`; a chain runs its dependencies first, in their tabs, and answers its own TERM; `task-run` with a published dependency, `syntax ... not published` for another; a running task's start answers its TERM; `terminal-stop` ends it `stopped`; a background dependency's `task-ready` comes before its dependent starts; `cancel ID` while the approval dialog is open answers `cancelled` (`task-start`, `terminal-open`), closes the dialog and remembers nothing | v1.1 tasks |
| `sub.*`, `ev.*` | `opened` names gate every other event, default is no, patterns (`*.json`, exact `Makefile`), `saved`/`changed` versions, `closed` and a new id on reopen, user typing gives `changed`, a reload keeps the id and gets a new version | 2, 6, 8 |

## Not covered

Gate 3 verbs and codes (`readonly`, `edits`, `hover`, `diagnostics`), the `project` event,
Windows and Linux specifics (the macOS build is what was run), key conflicts between extensions
and the user's `[keys]` winning over a registered key, the install dialog.

## Places where the specification is ambiguous (what the checks assume)

- A `cursor` event when a selection is made or exists (catch-up, `select`): the spec says
  `cursor` is sent when the cursor moved and the catch-up has "`cursor`/`selection`"; the
  checks expect both. (Failing checks: `hs.catch-up-selection.catch-up-cursor`,
  `edit.select.*-cursor-at-L2-C2`.)
- `toast`/`status` bodies far over their limit: section 7 says "truncated"; the checks expect
  no stop. The spec gives no other cap for these bodies.
- The `reason` wording is only checked where section 4 or 7 quotes it; the state `stopped` for
  limits and `crashed` for exits on their own follow sections 4.6 and 10. Crash reasons must
  contain the exit status.
- `active` for a document that `opened` does not name, and `selection`/`cursor` with an empty
  selection at catch-up, are not asserted.
- A non-numeric `DOC` is taken as a `syntax` error ("a request's arguments could not [be
  parsed]"); position 0 as `badpos`; an error for a request whose id is known is expected to
  carry that id.
- `open L C PATH` for a file that is already open: the position is expected to apply too, and
  a line past the end is clamped to the end of the document.
- "Stopped at once" for a closed pipe is checked as: not `running`, process gone within 3 s.
- The log cut at 1 MB is checked both at start and while the program keeps writing; the spec
  does not say when the cut happens.
- `register`: `NAME` that is malformed or over 40 bytes is taken as `syntax` (section 7 lists
  `syntax` for the row, `toobig` for the count); the 64 are counted among the registered
  commands, not the manifest's; a long `TITLE` is only checked to be accepted.
- Prompts: the label line of `print-palette` is `choose=` for `choose` and `prompt=` for `ask` and
  `term`; the text of the `term` prompt's label is not checked, only that the command is the field.
  A DEL (0x7f) in a `term` body and a trailing LF count as control characters.
- `cmd` with an upper-case spelling of a listed name is taken as not on the list (`refused`).
- `option` with a DOC that does not exist is `nodoc` (section 3: "no such document").
- The scenarios pace the extension with `waitrun` (it takes the `run` event of `cmd full.go`, then
  of `cmd full.go2`), so the script decides when a request is sent; the only sleeps are inside
  the extension where a later script step must see the earlier request applied.
