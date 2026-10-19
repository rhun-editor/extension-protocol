# rhun extension protocol

How a program becomes a [rhun](https://rhun.app) extension: rhun starts it and talks to it over
its standard input and output, one text line per message. This repository holds the protocol's
versions, a Go package that speaks it, and the conformance tools.

| Path | What |
| --- | --- |
| `v1/v1.md` | the specification, written to be read by people and by coding agents |
| `v1/v1.abnf` | the grammar of every line |
| `v1/v1.json` | the manifest (`extension.ini`) as a JSON Schema, with every key, pattern and default |
| `v1.1/` | additive protocol 1.1 features, including runtime command removal and the language features; v1 stays unchanged |
| `rust/rhun-ext` | the Rust crate (the first choice): framing, requests, events, views with pictures and charts, and every language feature of section 12 |
| `go/rhunext` | a Go package: framing, ids, bodies, requests that block until their reply, section 12 features |
| `c/rhunext.h` | one header for C and C++: the same, with nothing but the C library |
| `conformance/ext` | `rhun-ext-check DIR`: plays rhun against an extension and reports what it gets wrong (a `wasm32-wasip1` module through `rhun --wasm-host`) |
| `conformance/host` | scripted extensions and headless scripts that check rhun itself against the spec |

Status: **v1 is frozen** (2026-10-16, rhun 0.18.0). Later additions are features announced after
`hello 1` (section 12: views, completion, signature help, definition, references, symbols, code
actions, rename, format) and requests that need one (`apply`); nothing in v1 changes.

## Writing an extension

Start from [template-rust](https://github.com/rhun-editor/template-rust) (or
[template-go](https://github.com/rhun-editor/template-go)): a working extension with a headless
test and a release workflow that builds Linux, macOS and Windows binaries. In C or C++, copy
`c/rhunext.h`; [wordfreq](https://github.com/rhun-editor/wordfreq) and
[words](https://github.com/rhun-editor/words) are examples. Coding agents: install the `rhun`
plugin from [rhun-editor/skills](https://github.com/rhun-editor/skills).

```toml
rhun-ext = { git = "https://github.com/rhun-editor/extension-protocol", tag = "rust-v1.4.0" }
```

```sh
go get github.com/rhun-editor/extension-protocol@v1.4.0
```

Test with the checker, `rhun-ext-check DIR` (it asks every feature the extension agreed, checks
each answer as rhun reads it, applies the edits and serves `apply`) (`go run github.com/rhun-editor/extension-protocol/conformance/ext@latest DIR`),
where `DIR` is the extension laid out as installed (`OWNER/NAME/extension.ini` and `bin/`), then
headlessly against rhun as section 10 of the spec describes:
`rhun PROJECT --headless 1000x600 --extensions EXTS --script test.txt`.

## WebAssembly

An extension may ship one `wasm32-wasip1` module instead of (or besides) a binary per system:
`run.wasm32-wasip1 = bin/NAME.wasm` in the manifest. rhun runs it itself on Linux, macOS and
Windows, holding it to its `[needs]` through WASI preview 1 on every system (v1.1/v1.1.md, section
"WebAssembly extensions"). The three SDKs work unchanged in such a build:

```sh
rustup target add wasm32-wasip1 && cargo build --release --target wasm32-wasip1   # rhun-ext reads without threads there
GOOS=wasip1 GOARCH=wasm go build -o bin/NAME.wasm .                              # rhunext reads stdin non-blocking there
clang --target=wasm32-wasip1 --sysroot=$WASI_SYSROOT -O2 main.c -o bin/NAME.wasm  # rhunext.h as is
```

The checker runs a module through rhun: `rhun-ext-check -target wasm32-wasip1 -rhun PATH DIR` (it
picks the module by itself when the manifest names no program for this machine).

## Go request cancellation

`Ext.RequestContext` and `Ext.TextContext` accept a Go context. Cancellation releases the
caller while the reader still drains any late reply body, keeping subsequent messages aligned.
The existing `Request` and `Text` methods use a background context.

The connection allows 256 live requests and 512 outstanding request IDs, including
cancelled requests whose replies have not arrived. Further requests return `busy` before sending.
Request IDs held by those reservations are not reused.

One writer serializes frames, with at most 64 queued frames and 8 MiB of buffered data.
Outbound lines are limited to 64 KiB and bodies to 4 MiB. A blocked physical write ends the
connection after two seconds. Native streams are closed to interrupt blocked IO. Custom streams
passed to `RunOn` should implement `io.Closer` with interruptible IO; Go cannot stop an arbitrary
non-closable reader or writer. Context cancellation still releases callers, and no extra writer
worker is started for that connection.

## Workspace edits and the editor model

Protocol 1.1 (v1.4.0 of the SDKs) adds six features, specified in [v1.1](v1.1/v1.1.md#feature-workspace-edits):

| Feature | Rust | Go | C |
| --- | --- | --- | --- |
| `workspace-edits`: an edit is one transaction, files that are not open are edited on disk, one undo | `WorkspaceEdit::file_hashed`, `.label`, `.confirm`, `Ext::apply` returns `ApplyError` (code, entry, text) | `AddFileHashed`, `Label`, `Confirm`, `Apply` returns `*ApplyError` | `rx_wedit_file_hashed`, `rx_fnv1a64`, `rx_wedit_label`, `rx_wedit_confirm`, `rx_last_apply_error` |
| `file-operations`: create, rename and delete entries | `.create`, `.rename`, `.delete` with `Create`, `Rename`, `Delete` | `Create`, `Rename`, `Delete` with their options | `rx_wedit_create`, `rx_wedit_rename`, `rx_wedit_delete` with `RX_OVERWRITE` … |
| LSP's ordered `documentChanges` onto rhun's phases | `edits::from_ordered`, `edits::locate` | `NormalizeOrdered`, `Locate` | `rx_wedit_from_ordered`, `rx_locate` |
| `selections`: every selection | `Ext::select_many`, `Handler::selections` | `SelectMany`, `ParseSelections` | `rx_select_many`, `rx_parse_selections` |
| `reveal`: scroll to a place, list locations | `Ext::reveal`, `Ext::reveal_uri`, `Ext::show_locations` | `Reveal`, `RevealURI`, `ShowLocations` | `rx_reveal`, `rx_reveal_uri`, `rx_show_locations` |
| `diff`: a diff tab between two documents (with `uris`, virtual documents by URI) | `Ext::diff`, `Ext::diff_uris`, `Ext::diff_close` | `Diff`, `DiffURIs`, `DiffClose` | `rx_diff`, `rx_diff_uris`, `rx_diff_close` |
| `linked-editing`: ranges that change together (`[provides] linked`) | `Handler::linked` → `Linked` | `LinkedEditor` → `Linked` | the `linked` handler, `rx_linked_range`, `rx_linked_word` |

A file that is not open is named with the bytes the extension read it from (`fnv1a64:HEX`
precondition): rhun refuses the edit (`stale`) when the file changed since. The body lists the
creates, then the text groups, then the renames, then the deletes; an apply error's ENTRY counts
in that order (`WorkspaceEdit::entry`, `Entry`). The ordered normaliser rewrites an edit that
follows a rename to the name before it, composes repeated edits of one file into one group, and
refuses what the phases cannot express with the index of the change at fault (for LSP's
`failedChange`). Without the features an edit goes as 12g's grammar (the hash as `-`, no label or
confirm) and file operations fail as `unknown` without being sent; format and save-action answers
always keep 12g's grammar. A create path ending in `/` makes a folder: Go's `filepath.Join` and
Rust's `Path::join` drop that slash, so append it after joining. The checker serves all of it: it
makes transactions in its scratch project as rhun does (files written on disk, renamed documents
reopened under a new id) and answers a refused apply with `error ID CODE ENTRY TEXT`.

## Source actions before saving

Protocol 1.1 adds negotiated `save-actions` and `[provides] save_actions` patterns. Go implements
the optional `SaveActionProvider` interface with `SaveActionsRequest.Context`; use
`SaveApplyContext` for intermediate command edits. Cancellation reaches the context directly
from the input reader, including while a handler waits for a language server. Rust exposes
`Handler::save_actions` and `Ext::save_apply`; C exposes `rx_handlers.save_actions` and
`rx_save_apply`. These serial handlers must bound work. Custom asynchronous dispatchers must
retire requests when a `cancel` event names their ID. The host rechecks invocation identity,
document and version for every intermediate edit.

While developing against unreleased SDK additions, build the local checkout and checker
instead of the released SDK or `@latest`. For Go, use a `go.work` containing the SDK and extension
repositories, then `go build -o /tmp/rhun-ext-check ./conformance/ext` from this repository.
Pass that path as `RHUN_EXT_CHECK` to extension test scripts. Rust can use a local path dependency;
C can include the checkout's header. Release the SDK before extensions depending on its additions.

## Language features (1.1)

The [language features](v1.1/v1.1.md#language-features) of protocol 1.1 add negotiated
features to completion, signature help, navigation, rename, formatting, code actions, hover,
selection, hierarchies, project symbols, links and diagnostics:

- `query-stream`: parts of a long answer and progress on one of rhun's pending requests. Go's
  `StreamingCompleter` and `StreamingLocator` get a `Stream` (`Part`, `PartLocations`, `PartItems`,
  `Progress`) whose context rhun's `cancel` ends. Rust's `Handler::complete_stream`, `definition_stream` and `references_stream` get
  a `Stream` (`part`, `part_locations`, `part_items`, `progress`, `is_cancelled`); it is `Clone`
  and `Send`, so worker threads can send parts while the serial handler waits for them. C's
  `rx_handlers` gain `complete_stream`, `definition_stream` and `references_stream` with an
  `rx_stream` for `rx_stream_part`, `rx_stream_locations`, `rx_stream_items`,
  `rx_stream_progress` and `rx_stream_cancelled`. Without the feature agreed, parts fail locally
  and the answer carries everything.
- `provider-scope`: narrowing `[provides]` at run time with Go `Provides`, `ProvidesManifest` and
  `Withdraw`, Rust `Ext::provides`, `provides_manifest` and `withdraw`, and C `rx_provides`.
- `complete-items`: completion items with resolve data, a replace range, commit characters, a
  filter text, a suffix, preselect, deprecated, adjust and after-accept records (Go and Rust
  `CompletionItem` fields, C `rx_item_extra` with `rx_list_item_extra`), resolved by Go's
  `CompletionResolver`, Rust's `Handler::complete_resolve` (`Resolution`) and C's
  `complete_resolve` handler (`rx_resolve_answer`).
- `signature-list`: every overload in signature help: Go's `SignatureLister`, Rust's
  `Handler::signatures` (`SignatureList`) and C's `signatures` handler (`rx_signatures_answer`).
- `navigation`: Go to Declaration, Type Definition and Implementation, answered with locations by
  Go's `Declarer`, `TypeDefiner` and `Implementer` (or `StreamingLocator`), Rust's
  `Handler::declaration`, `type_definition` and `implementation` (and their `*_stream` variants),
  and C's `declaration`, `type_definition` and `implementation` handlers (with an `rx_stream`).
- `prepare-rename`: what F2 selects and fills the prompt with, or why nothing can be renamed:
  Go's `RenamePreparer` (`PreparedRename`, an error refuses), Rust's `Handler::prepare_rename`
  (`Ok(Some(PreparedRename))`, `Ok(None)` for the word at the cursor, `Err` refuses) and C's
  `prepare_rename` handler (`rx_prepare_rename_answer`, `rx_refuse_answer`).
- `partial-format`: Format Selection and formatting as you type, with the document's
  `trim_trailing_whitespace` and `final_newline`: Go's `RangeFormatter` and `TypeFormatter`, Rust's
  `Handler::range_format` and `type_format` (`RangeFormatRequest`, `TypeFormatRequest`) and C's
  `range_format` and `type_format` handlers (`rx_range_format_ask`, `rx_type_format_ask`).
- `code-actions`: in place of v1's `actions`, hierarchical kinds (actions of a kind not asked are
  left out before answering), preferred and disabled actions, resolve data, command references
  (`command NAME ARGS_LEN`) and the diagnostics fixed: Go's `CodeActionProvider2` (`Action`,
  `CommandRef`, `CodeActionsRequest.Wants`) and `CodeActionResolver` (`ResolvedAction`), Rust's
  `Handler::code_actions` (`Action`, `CommandRef`, `CodeActionsRequest::wants`) and
  `code_action_resolve` (`ResolvedAction`), and C's `code_actions` handler (`rx_code_action` with
  `rx_action_extra`, `rx_kind_wanted`) and `code_action_resolve` handler (`rx_resolved`,
  `rx_command_ref`).
- `hover-query`: the hover card's text and the range it describes: Go's `HoverProvider`
  (`HoverRequest` with the kind and a context, `Hover`), Rust's `Handler::hover` (`HoverRequest`,
  `Option<Hover>`) and C's `hover` handler (`rx_hover_answer`); the text is cut to fit 64 KiB.
- `selection-ranges`: Expand and Shrink Selection: Go's `SelectionRanger`
  (`SelectionRangesRequest`, `EncodeSelectionRanges`, `SelectionChain`), Rust's
  `Handler::selection_ranges` (`selection_chain`) and C's `selection_ranges` handler
  (`rx_selection_group`). Each group becomes a chain (equal ranges skipped, the first that does not
  contain the one before ends it), an empty group the empty range at its cursor, and the outermost
  ranges go until the answer fits 64 KiB.
- `hierarchy`: call and type hierarchies: Go's `CallHierarchyProvider` (`CallHierarchy`,
  `CallsIn`, `CallsOut`) and `TypeHierarchyProvider` (`TypeHierarchy`, `Supertypes`, `Subtypes`)
  with `HierarchyItem`, `Call` and `HierarchyRequest`, the item requests getting the item's data
  and a `Stream` (`PartCalls`, `PartHierarchyItems`); Rust's `Handler::call_hierarchy`, `calls_in`,
  `calls_out`, `type_hierarchy`, `supertypes` and `subtypes` (`HierarchyItem`, `Call`,
  `Stream::part_calls`, `part_hierarchy_items`); C's handlers of the same names
  (`rx_hierarchy_item` with `rx_hierarchy_add`, `rx_call_add`, `rx_stream_hierarchy`,
  `rx_stream_calls`).
- `workspace-symbols`: Go to Symbol in Project: Go's `WorkspaceSymbolProvider`
  (`WorkspaceSymbolsRequest`, `WorkspaceSymbol`, `PartWorkspaceSymbols`), Rust's
  `Handler::workspace_symbols` and C's `workspace_symbols` handler (`rx_workspace_symbol`,
  `rx_stream_workspace_symbols`); a new query withdraws the request and cancels its stream.
- `document-links`: links under ctrl+hover: Go's `DocumentLinker` (`DocRequest`, `DocumentLink`
  with `LinkURL`, `LinkPath`, `LinkDoc`, `ValidLinkTarget`), Rust's `Handler::document_links`
  (`DocumentLink`, `LinkTarget`) and C's `document_links` handler (`rx_link`,
  `rx_link_target_valid`); links to anything but an http(s) URL, an absolute path (with a line and
  column) or a document are left out.
- `file-diagnostics`: diagnostics for any file, open or not, in rhun's Problems store, with ids
  that code actions get back, codes, sources, links, tags and related places: Go's
  `Ext.FileDiagnostics` and `ResetDiagnostics` (`Diagnostic`'s `ID`, `Code`, `Source`, `Href`,
  `Tags`, `Related`), Rust's `Ext::file_diagnostics` and `reset_diagnostics` and C's
  `rx_file_diagnostics` (`rx_diagnostic`, `rx_related`) and `rx_reset_diagnostics`. The SDKs
  leave out or cut what rhun would refuse (an empty message, a bad or repeated id, a link that is
  not http(s), more than 2000 records or 4 MiB).

The SDKs read the bodies of rhun's 1.1 requests that carry data (up to 64 KiB). The checker
validates the new completion records, sends `complete-resolve` with the data a list gave and
checks the resolution, asks `signatures` instead of `signature` when agreed, serves `query-part`,
`query-progress` and `provides` against the manifest, and knows the new `[provides]` keys. It
asks `declaration`, `type-definition` and `implementation` and reads their locations, sends
`prepare-rename` before a rename, `range-format` and (after typing the first trigger)
`type-format` and checks that their edits name only the document asked about, and asks
`code-actions` for any kind and for `quickfix`, checking kinds, the extra records and command
names against the extension's commands, then resolves an action with `code-action-resolve`. It
asks `hover-query`, `selection-ranges` (each range containing the one before), `call-hierarchy`
and `type-hierarchy` and then the calls or items about the first root with its data,
`workspace-symbols` and `document-links`, reading parts only where the kind takes them, and
serves `file-diagnostics` (targets, versions, records and their ids) and
`file-diagnostics-reset`.

## Source control

Protocol 1.1 adds Git data read through rhun (`git-data`, with `[needs] git = read`), commit-message
providers (`commit-message`), quick-diff bases for the gutter (`quickdiff`) and version control
panels for other systems (`scm`). Go: `Ext.GitRepo`, `GitStatus`, `GitBlame`, `GitLog`, `GitShow`,
`GitDiffOpen`, the `CommitMessageProvider` and `QuickDiffProvider` interfaces, `QuickDiffRefresh`,
and `ScmRegister`, `ScmUpdate` with an `ScmPanel`, `ScmInput`, `ScmInputSet`. Rust: `Ext::git_*`,
`Handler::commit_message` and `Handler::quickdiff`, `Ext::quickdiff_refresh`, `Ext::scm_register`,
`scm_update` with an `ScmPanel`, `scm_input`, `scm_input_set`. C: `rx_git_*`, the
`commit_message` and `quickdiff` handlers, `rx_quickdiff_refresh`, `rx_scm_register`,
`rx_scm_update` with a body from the `rx_scm_*` appenders, `rx_scm_input`, `rx_scm_input_set`.
Panel buttons and clicks run command references, whose ARGS arrive through `command-args`
(below).

## Commands with arguments, context keys and status items

Protocol 1.1's `command-args` tells a command where it was invoked from (`palette`, `key`, `menu`,
`ref`) and gives it a body: a key binding's ARGS, a menu's `TARGET` line and the item's args, or a
command reference's ARGS. Go handlers implement `Invoker` (`Invocation.From`, `.Body`,
`.Target()` for menus), build references with `EncodeCommandRef`, run built-ins and other extensions'
callable commands with `RunCommand`, and answer calls with `Callee` (`CallRequest.Context` ends on
`cancel`). Rust has `Handler::invoke`, `command_ref`, `Ext::run_command` and `Handler::call`; C has
`rx_handlers.invoke`, `rx_menu_target`, `rx_command_ref`, `rx_run_command` and `rx_handlers.call`.
`context-keys` sets the extension's keys for when clauses (`SetContext` / `set_context` /
`rx_set_context`), and `status-items` adds status bar items (`SetStatusItem` with `StatusItem` /
`set_status_item` / `rx_set_status_item`). Without `command-args` the old `Run` / `command`
handlers keep receiving every run.

## Test controllers

Protocol 1.1's `tests` feature (`v1.1/v1.1.md`, "Feature: tests") lets an extension publish a tree
of tests, run what rhun asks, and report results, failure messages with expected and actual values,
output and line coverage. `test-exec` starts a program `[needs] tests` names (`go`, `cargo`) as the
user and streams its output back.

- Rust: `Feature::Tests`; `Handler::resolve_tests`, `run_tests` (return at once, report from a
  thread through `TestReporter`, end with `Ext::end_test_run`) and `cancel_tests`;
  `Ext::set_test_items`, `report_coverage`, and `test_exec`, whose `TestProcess` streams implement
  `Read`.
- Go: `FeatureTests`; a handler that is a `TestController`; `SetTestItems`, `NewTestReporter`,
  `TestExec`, `ReportCoverage`, `EndTestRun`.
- C: the `resolve_tests`, `run_tests`, `cancel_tests`, `test_out` and `test_exit` handlers;
  `rx_test_item`, `rx_test_update`, `rx_test_exec`, `rx_test_coverage_send`, `rx_test_end`.
- `expected_actual` (Rust) and `ExpectedActual` (Go) find compared values in Go, testify and Rust
  failure text.

`rhun-ext-check` resolves the roots and one lazy level, runs each profile the manifest offers and
cancels a run, checking every body as rhun reads it. It refuses `test-exec` unless given
`-test-exec-script FILE` (lines `out TEXT`, `err TEXT`, `sleep MS`, `exit CODE`); `-test-timeout`
bounds a run (default 60 s).

## Prepared snippet fields

Protocol 1.1 adds `snippets` for editable native fields. Go provides `PreparedSnippet`, `Field`,
`CompletionItem.WithSnippet` and `InsertSnippetContext`. Rust provides `PreparedSnippet`, `Field`,
`CompletionItem::snippet` and `Ext::insert_snippet`. C provides `rx_snippet`, `rx_snippet_field`,
`rx_list_item_snippet` and `rx_insert_snippet`. Explicit insertion requires agreement. Completion
metadata falls back to its plain prepared text on older hosts. An empty prepared insertion
stays empty rather than using its label. Parents, mirrors, UTF-8 boundaries, propagation cycles
and size limits are checked before sending explicit requests. These APIs accept expanded text;
they do not parse template syntax.

`SnippetProgram` and `SnippetChoice` add literal field choices. Go provides
`WithSnippetProgram`, `InsertSnippetProgram` and `InsertSnippetProgramContext`. Rust provides
`CompletionItem::snippet_program` and `Ext::insert_snippet_program`. C provides
`rx_snippet_program`, `rx_snippet_choice`, `rx_snippet_option` and the corresponding
`rx_snippet_program_validate`, `rx_snippet_program_encode`, `rx_list_item_snippet_program` and
`rx_insert_snippet_program` helpers. First choices must match their primary text. Up to 256
options per group are allowed, including empty, multiline and Unicode text. Programs validate
the entire encoded size before allocating a wire body. Go completion builders snapshot caller
slices, Rust owns metadata, and C borrows option bytes until encoding completes.

Go's `ParseSnippetTemplate` validates template syntax into an immutable tree and retains its
original `Source()`. It recognizes tabstops, nested defaults, choices, variables and transform
formats. A `SnippetSyntaxError` reports a byte offset. Parsing is bounded to 4 MiB, 8,192 syntax
and format nodes, 256 options per choice and 32 nesting levels. Regex source and flags are
retained for later compilation; parsing alone does not render a template or enable a host feature.

`SnippetTemplate.Prepare(SnippetContext)` expands the core template syntax into a native
`SnippetProgram`; `PrepareSnippetTemplate` parses and prepares in one call. Numeric defaults
can be declared after their references. The first valued definition supplies each group;
nested fields appear under the first rendered primary and repeated occurrences remain mirrors.
Output group IDs are local, ordered by numeric template indices and then unknown variable names.
A final default such as `${0:result}` leaves that text selected when field editing finishes.

The context supplies literal variable values. Present empty values override defaults; missing
nonstandard variables use their defaults, or become linked editable names when no default is present.
Known standard variables without a value use their defaults, or empty text without a default.
Preparation does no implicit environment, filesystem, clipboard, time or random access. Text
keeps its whitespace. Variable transforms resolve immediately; positive numeric transforms
become per-occurrence compiled bindings and run on native field exit. Missing transformed
variables use empty input, including unknown names. Group-zero choices/transforms, multiple
rendered final markers and cyclic defaults remain errors. Regex and replacement diagnostics
point to bytes in the original template, including escaped regex delimiters.
`SnippetExpansionError` reports a source byte offset, or -1 for invalid context input. Errors
return no partial program. Limits include 32 dependency and parent levels, 512 fields,
256 rendered groups, a 4 MiB encoded native program and a 128 MiB resolved-value cache.

An optional end-to-end check compiles templates and exercises their native fields, mirrors,
choice popup, final selection and undo/redo. With Python 3 available, run
`RHUN_TEST_EXE=/absolute/path/to/rhun go test ./go/rhunext -run TestSnippetExpansionNativeBridge`.
Ordinary SDK tests do not require a native editor installation.

`Ext.SnippetSnapshotContext` (Go), `Ext::snippet_snapshot` (Rust) and
`rx_get_snippet_snapshot` (C) read an immutable context for an explicit document version and
range. It contains literal selection/current-line text, file and workspace paths, insertion
indentation, effective indentation settings, CRLF mode and the grammar's line-comment token.
It requires negotiated snippets and document read authority. Readonly and inactive documents
are supported. The request does not move the editor's selection. C results own their six byte
buffers; release them with `rx_snippet_snapshot_free` before reuse. Reply framing enforces
the 4 MiB limit before allocation, including late canceled Go requests. This primitive does
not collect clipboard, environment, time or random values, or rewrite indentation.
With a native editor available, `RHUN_TEST_EXE=/absolute/path/to/rhun go test ./go/rhunext -run TestSnippetSnapshotNativeBridge`
exercises the real Go helper, template expansion, linked native fields and rejection of a stale insertion.

Go's `PrepareSnippetTemplateWithSnapshot` and `SnippetTemplate.PrepareWithSnapshot` resolve
standard selection, current-line/word, position, file/workspace and comment variables from a
`SnippetSnapshot`. `SnippetVariableOptions.Variables` supplies literal overrides. Empty selection,
clipboard, and unavailable file/comment values leave variable defaults usable. Current words
contain Unicode letters, numbers, combining marks and underscore; a language-specific provider
can override `TM_CURRENT_WORD`. Derived paths use bounded lexical POSIX or Windows rules,
selected with `PathStyle`; native is the default. Windows derived paths use forward slashes.
`TM_FILEPATH` and `WORKSPACE_FOLDER` retain their captured spelling. Paths outside the workspace
remain absolute, and POSIX backslashes remain filename bytes.

Preparation collects no clock, entropy or clipboard contents. Supply `SnippetTime` through
`Time` for calendar, Unix and timezone variables; it contains an instant, nanoseconds, offset
and zone name. `SnippetTimeFromTime(t)` is an explicit conversion step that may resolve Go's
local timezone settings. Supply exactly 16 `Random` bytes for six-digit `RANDOM`, six-character
`RANDOM_HEX` and a version-4 `UUID`. Repeated occurrences share a value within one preparation.
`Clipboard` accepts an explicitly supplied literal; it does not read the editor's clipboard.
Block comment tokens and cursor index can also be supplied explicitly. All inputs and output
remain bounded. Variables are resolved lazily, so unused large selections and aliases do not
create a second context map. Snapshot preparation formats insertion literals and captured selections as described below.

Snapshot preparation normalizes literal template CRLF/CR to internal LF and adapts leading
spaces and tabs to the captured indentation settings. Continuation lines receive the captured
base indentation. Formatting follows each original text chunk and the actual expanded output
before it. Field offsets are recorded while writing, including differently formatted copies.
The native editor handles CRLF serialization when saving a CRLF document.

Captured `SELECTION` and `TM_SELECTED_TEXT` values gain any additional wrapper indentation
on continuation lines. Explicit variable overrides and choice options retain their bytes.
`KeepWhitespace` disables template indentation edits while literal line endings still
normalize to LF. Selection wrapping still uses the literal wrapper's spaces or tabs.
Raw `Prepare` keeps its original whitespace behavior. Snapshot preparation formats an
unresolved expanded template layout before resolving variables. A variable's result cannot
change how a later literal is formatted. Selection indentation comes from the last Text
marker's leading whitespace, including marker boundaries inside defaults and mirrors.
Preparation remains pure and safe for concurrent reuse of an immutable parsed template.
The unresolved layout is bounded to 32,768 markers and 4 MiB. All ordinary output, field,
dependency, encoded-frame and raw-cache limits also apply. An error returns no partial program.
The native model flattens nested fields under repeated parent groups and canonicalizes numeric
group IDs. Ordinary unknown variables retain linked editable names. These native contracts
are tested separately from upstream resolver output.

Programs can declare initially different copies with Go `InitialVariants`, Rust
`initial_variants`, or the borrowed C `initial_variants`/`ninitial_variants` array.
These are one-based field-record indices, each identifying a nonprimary positive-group
mirror with genuinely different initial bytes. Both `snippets` and `snippet-variants`
must be negotiated. Plain prepared validators still require equal copies. Explicit helpers
check agreement before encoding or sending; completion builders snapshot declarations
(Go/Rust own their metadata; C encodes borrowed inputs during the call). Later edits
copy the primary value normally. Formatting uses this initial-layout primitive; deferred transforms can use it as well.

## Checking a host

`python3 conformance/host/run.py RHUN_BINARY` runs rhun against extensions written only from
the spec and prints one line per check.

The internal bounded regex compiler resolves the parser's captures, lookarounds,
references, character classes and counted repeats into a portable validated program.
The private Go matcher and native assembly VM execute these programs with shared
work limits and bounded branch, assertion and rollback storage. The native VM also
accepts a batch deadline and checks it before publishing a result. Shared vectors compare captures and remaining work across implementations, including
rollback, empty repeats, atomic assertions, reverse lookbehind and storage limits.
Public Go template preparation uses this compiler and matcher. Programs have explicit instruction,
class, range, repeat and byte limits; compilation also has an aggregate work limit.
Counted repeats stay compact. Decoding checks exact lengths, operands and ownership
before execution and copies its input. Unicode properties use the pinned tables below.
Case equivalence distinguishes `i` and `iu`; character consumption always uses native
Unicode scalars. The scalar grammar supports Unicode `v` sets, Unicode capture names,
and legacy no-Unicode syntax as described below. Local modifier groups set `i`, `m`, and
`s` for their own subexpression, including nested lookaround and references.

Replacement text has a separate canonical UTF-8 program. Go and native validators
check record limits, capture indices, string boundaries and exact lengths. Replacement
validation is followed by a private Go transform executor for captures, conditional
formats, global/sticky matching, zero-width matches and whole-format no-match
fallbacks. All execution stages share the work budget and return no partial result.
The native transform executor uses the same matching, fallback and formatting rules.
Shared vectors compare output and work, including failures after temporary case
output has been allocated. Borrowed matcher views are detached before it returns.
Export fixtures with `RHUN_TRANSFORM_ASM_OUT=/tmp/transforms.s` and
`go test ./go/rhunext -run '^TestSnippetTransformNativeVectorExport$'`; replace the
payload after the marker in the editor's `tests/snippet_transform_test.s`. The table
precedes its large data blocks to keep native relocations within range.
A separate Go/native step normalizes deferred transform line endings and indentation.
It preserves the first line and applies the captured insertion prefix with current
indentation settings to every subsequent line, including a trailing empty line.
Shared fixtures compare its output, errors and work. Export them with
`RHUN_TRANSFORM_INDENT_ASM_OUT=/tmp/indent.s` and
`go test ./go/rhunext -run '^TestSnippetTransformIndentNativeVectorExport$'`.
Deferred bindings use a separate canonical payload that pairs field identities
with embedded regex and replacement programs. Go and native validators check its exact
partition, nested programs and limits. Native sessions own immutable copies shared
safely by their preflight clones. Choices, transforms and original insertion indentation
share an aggregate 4 MiB nesting budget. Export validator fixtures with
`RHUN_TRANSFORM_PAYLOAD_ASM_OUT=/tmp/bindings.s` and
`go test ./go/rhunext -run '^TestSnippetTransformPayloadNativeVectorExport$'`.
Rust's `snippet_transform::validate_snippet_transforms` and C's
`rx_snippet_transform_validate` check the same payload without allocation or execution.
They report absolute error bytes and leave field existence and ownership to the
prepared snippet validator. The shared 2,210-case fixture set includes nested programs,
truncations, byte mutations, invalid Unicode boundaries and maximum binding counts.
Regenerate both fixture files with `TestSnippetTransformPortableFixtureExport`, setting
`RHUN_TRANSFORM_C_FIXTURES_OUT` and `RHUN_TRANSFORM_RUST_FIXTURES_OUT` to their paths.
Native field navigation now prepares all deferred outputs before editing, checks
ancestor mirror propagation on cloned markers, and applies the result as one undo
group. A failed batch preserves accepted text and selection. Shared work, a batch
deadline and insertion limits bound this operation. Initial selection and cancellation
do not run transforms; revisiting a field runs them again on its current value.
Export navigation fixtures with `RHUN_TRANSFORM_LEAVE_ASM_OUT` and
`TestSnippetTransformLeaveNativeVectorExport`.
The negotiated `snippet-transforms` feature now carries this payload through explicit
insertion and completion. Programs are Base64 encoded so all completion APIs remain UTF-8.
The Go factory compiles source and replacement formats without ambient state:

```go
compiled, err := rhunext.CompileSnippetTransform("(.*)", "${1:/upcase}", "")
if err != nil { return err }
program := rhunext.SnippetProgram{
    Snippet: rhunext.PreparedSnippet{Text: "name name", Fields: []rhunext.Field{
        {Group: 1, Start: 0, End: 4}, {Group: 1, Start: 5, End: 9},
    }},
    Transforms: []rhunext.SnippetFieldTransform{{Field: 2, Transform: compiled}},
}
// After agreeing to both snippets and snippet-transforms:
_, err = ext.InsertSnippetProgram(doc, version, span, program)
```

`CompiledSnippetTransform.Programs()` exports owned binary copies;
`NewCompiledSnippetTransform` validates and imports copies. `DecodeSnippetTransforms`
imports a complete canonical SNTX payload. `SnippetTransformCompileError` identifies the
source/options/format or imported program part and a byte offset within it. The zero
compiled value is invalid. Completion builders snapshot bindings with their fields.
Rust imports the same programs with `CompiledSnippetTransform::from_programs`, shares
immutable bytes across clones, and attaches `SnippetFieldTransform` values through
`SnippetProgram.transforms`. C borrows an `rx_snippet_transform_program` during each
validation/encoding call and attaches `rx_snippet_field_transform` arrays through the
appended `transforms` and `ntransforms` program fields. Recompile C consumers and update
positional initializers for the appended fields. C completion lists own the encoded bytes.
All three SDKs canonicalize field order and check whole encoded limits before allocating
transform bodies. A valid explicit-sized item too large for a completion is omitted with
an incomplete list; invalid metadata retains only plain insertion text. See
[v1.1](v1.1/v1.1.md#compiled-field-transforms) for wire limits and binary layouts.
The Go template helpers now compile variable and positive field transforms directly:

```go
program, err := rhunext.PrepareSnippetTemplate(
    "${1:name} ${1/(.*)/${1:/upcase}/} ${NAME/(.*)/${1:/downcase}/}",
    rhunext.SnippetContext{Variables: map[string]string{"NAME": "VALUE"}},
)
// Initial text: name name value. Leaving group 1 changes its second occurrence to NAME.
```

Snapshot preparation resolves selected-text wrapper indentation before applying a variable
transform, then inserts the result literally. Deferred field transforms use the host's current
indentation settings on exit. Each preparation shares a 16,777,216-unit compilation budget
and a separate execution budget of the same size across all initial transforms; each regex
also retains its 1,048,576-unit compilation cap. Encoded compiled programs cached during a
preparation total at most 4 MiB. Errors return no partial program.

Regex transforms support Unicode sets with the `v` flag, which is mutually exclusive with
`u`. Sets support nested unions, intersections (`&&`), subtraction (`--`), and literal
string alternatives (`\q{ab|abc}`). Range operands in set operations require nested brackets,
for example `[[a-z]&&[^aeiou]]`. String matching tries the longest alternative first and
can backtrack to a shorter alternative. Negation rejects sets whose static type may contain
strings. Under `iv`, case equivalence applies before set operations and complements.
The seven Unicode 16 string properties include `Basic_Emoji`, `Emoji_Keycap_Sequence`,
the RGI flag, tag, modifier and ZWJ sequence properties, and their `RGI_Emoji` union.
These compile into ordinary RNRX v1 programs with the Unicode flag, so Rust/C imports and
native execution use the existing format. Source compilation retains its normal limits;
large set expressions can fail with a compilation quota error.
Template parsing preserves escaped delimiter slashes inside `v` sets and string alternatives.
Capture names accept Unicode 16 identifier characters and Unicode escapes, with a limit of
64 decoded UTF-8 bytes per name. Escaped and literal spellings identify the same exact
code-point sequence; names are not normalized. Duplicate names are allowed in distinct
alternatives of a shared disjunction, for example `(?:(?<word>a)|(?<word>b))\k<word>`.
The reference uses whichever capture participated. Numeric references and replacement
formats retain the actual capture numbers. These names compile to existing reference
instructions, with no new host feature or program format. The 5,968 capture-name grammar
and match fixtures can be regenerated with `node tools/snippet_names_oracle.mjs`.

With neither `u` nor `v`, transforms accept legacy octal and identity escapes, literal
malformed braces, shorthand/hyphen class unions, and quantified lookahead. Decimal
references start with a nonzero digit and use the whole pattern's capture count;
fallback consumes one octal or identity atom before parsing any trailing quantifier.
Names always use strict Unicode escapes. Input remains valid Unicode scalars in every
mode, so surrogate escapes are errors and astral input consumes one scalar. Rhun also
retains scalar property escapes and valid braced Unicode scalar escapes without `u`.
Those native forms take precedence over legacy identity spelling. The 17,000 legacy
grammar and match fixtures can be regenerated with `node tools/snippet_legacy_oracle.mjs`;
they exclude these documented native differences from JavaScript.

Scoped modifiers accept forms such as `(?i:...)`, `(?-i:...)`, and `(?im-s:...)`.
Either side of the separator may be empty when at least one modifier is present.
Duplicates, conflicting flags, and flags other than `i`, `m`, and `s` are errors.
The global program flags stay unchanged. Local literals, dots, and classes compile to
scalar sets where necessary; RNRX instructions 14 and 15 carry local assertion and
reference modes. Every SDK validator accepts the same operands. Native operations restore
the enclosing flags on success, nonmatch, quota failure, and deadline failure. The 20,868
grammar/matching fixtures can be regenerated with `node tools/snippet_modifiers_oracle.mjs`.

The 75-case `testdata/snippet-template-transform.json` fixture set uses the actual upstream
[SnippetParser and Transform](https://github.com/microsoft/vscode/blob/main/src/vs/editor/contrib/snippet/browser/snippetParser.ts),
[SnippetSession.adjustWhitespace](https://github.com/microsoft/vscode/blob/main/src/vs/editor/contrib/snippet/browser/snippetSession.ts)
and [SelectionBasedVariableResolver](https://github.com/microsoft/vscode/blob/main/src/vs/editor/contrib/snippet/browser/snippetVariables.ts),
retrieved 2026-10-18, with upstream indentation primitives. Expected field groups are
canonicalized and descendants under repeated native parents are omitted. Cases marked
`RetainAllFlags` retain supported regex flags when cloning a transformed variable; the upstream
clone currently keeps only `i` and `g`. These rows also record `UpstreamText`. The corrected
clone copies only the original regex flags; resolution still runs the upstream transform.
Rust/C source-template helpers, the remaining regex grammar and final-field semantics remain
release work. The compiled import/binding APIs above are available in every SDK.
The private Go case converter implements all seven formats using pinned full mappings and contextual final sigma.
Word formats use Unicode letter categories and ASCII digits. Case operations share
the regex work budget, reject malformed UTF-8 and return no partial output on error.
They reserve validation bytes, four units per scalar decode, sixteen per property or
mapping lookup, and output bytes before writing. Input and final output are bounded
at 4 MiB; snake preparation may use 8 MiB. The native converter runs the same formats,
sharing batch deadlines and work with the regex VM. Checked-in vectors compare exact output and remaining work, including
malformed inputs and quota failures. Recoverable builders free all private buffers on
failure. Export the deterministic native fixture with `RHUN_CASE_ASM_OUT=/tmp/cases.s`
and `go test ./go/rhunext -run '^TestSnippetCaseNativeVectorExport$'`; replace the
payload after the marker in the editor's `tests/snippet_case_test.s`. Native tests
also cover buffer caps, allocation failure, late deadlines and result ownership.
Capitalize and word initials operate on Unicode scalars, including astral letters.
Locale-specific casing is deliberately excluded for consistent results across hosts.

Snippet regex and transform data is pinned to Unicode 16.0.0. Generated Go tables and native
assembly helpers use the same checked UCD source, independently of the host's locale and
Unicode library version. `tools/snippet_unicode.py` takes the official UCD ZIP and explicit
Go/assembly output paths; it verifies the pinned SHA-256 before reading members. Regeneration
requires Python 3 and `gofmt`; normal builds need neither a download nor this generator.
Run `python3 tools/test_snippet_unicode.py` for generator unit checks, or set `RHUN_UCD_ZIP`
to the pinned ZIP for full source and deterministic-output verification. Unicode notices are
in `go/rhunext/LICENSE-Unicode.txt` and the editor's associated license files. Scalar binary
properties use the explicit 53-property ECMAScript inventory. Categories, scripts and script
extensions use exact Unicode aliases; unsupported aliases fail rather than receiving a
different property's data. Native helpers expose letter, uppercase-letter and lowercase-letter predicates alongside casing and case-ignorable properties.

`tools/snippet_emoji.py` generates the string properties from the official Unicode 16
`emoji-sequences.txt` and `emoji-zwj-sequences.txt`, checking both pinned hashes and property
counts. Supply those local files followed by `--go-out go/rhunext/snippet_emoji_data.go`.
Run `python3 tools/test_snippet_emoji.py` for generator checks. Set `RHUN_EMOJI_SEQUENCES`
and `RHUN_EMOJI_ZWJ` to enable deterministic regeneration against the committed data.
The 5,758 Unicode-set grammar and matching fixtures were generated with Node 26.11.1 using
`node tools/snippet_sets_oracle.mjs`; normal tests require no JavaScript runtime. Fixtures
use stable characters shared with Unicode 16, and separate tests check all 3,790 pinned
RGI emoji sequences in both directions. See the
[ECMAScript pattern grammar](https://tc39.es/ecma262/multipage/text-processing.html#sec-patterns)
and [Unicode emoji data](https://www.unicode.org/Public/emoji/16.0/).
