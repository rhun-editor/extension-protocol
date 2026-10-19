# rhun extension protocol

How a program becomes a [rhun](https://rhun.app) extension: rhun starts it and talks to it over
its standard input and output, one text line per message. This repository holds the protocol's
versions, a Go package that speaks it, and the conformance tools.

| Path | What |
| --- | --- |
| `v1/v1.md` | the specification, written to be read by people and by coding agents |
| `v1/v1.abnf` | the grammar of every line |
| `v1/v1.json` | the manifest (`extension.ini`) as a JSON Schema, with every key, pattern and default |
| `v1.1/` | additive protocol 1.1 features, including runtime command removal; v1 stays unchanged |
| `rust/rhun-ext` | the Rust crate (the first choice): framing, requests, events, views with pictures and charts, and every language feature of section 12 |
| `go/rhunext` | a Go package: framing, ids, bodies, requests that block until their reply, section 12 features |
| `c/rhunext.h` | one header for C and C++: the same, with nothing but the C library |
| `conformance/ext` | `rhun-ext-check DIR`: plays rhun against an extension and reports what it gets wrong |
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
rhun-ext = { git = "https://github.com/rhun-editor/extension-protocol", tag = "rust-v1.3.0" }
```

```sh
go get github.com/rhun-editor/extension-protocol@v1.3.0
```

Test with the checker, `rhun-ext-check DIR` (it asks every feature the extension agreed, checks
each answer as rhun reads it, applies the edits and serves `apply`) (`go run github.com/rhun-editor/extension-protocol/conformance/ext@latest DIR`),
where `DIR` is the extension laid out as installed (`OWNER/NAME/extension.ini` and `bin/`), then
headlessly against rhun as section 10 of the spec describes:
`rhun PROJECT --headless 1000x600 --extensions EXTS --script test.txt`.

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

## Terminals and tasks

Protocol 1.1's `terminals` and `tasks` features (`[needs] terminal = run`): programs in terminal
tabs and published tasks with dependencies, readiness and problem matchers. Go: `Task`, `Program`,
`TerminalSpec`, `SetTasks`, `StartTask`, `RunTask`, `OpenTerminal` (their `…Context` forms send
`cancel` when the context ends while the approval dialog is open, and stop a terminal whose start
won that race), `StopTerminal`, `RestartTerminal`, `CloseTerminal`, `ShowTerminal`,
`TerminalInput`, `TerminalScreen`, `ParseTerminalEvent`. Rust: the `tasks` module and the same
`Ext` methods. C: `rx_task`, `rx_terminal_spec`, `rx_set_tasks`, `rx_start_task`, `rx_run_task`,
`rx_open_terminal` and the `rx_*_terminal` calls, `rx_parse_terminal_event`. Every SDK checks a
definition as rhun reads it before sending it, and has rhun's pattern matcher (`CheckPattern`,
`MatchPattern`, `CheckMatcher`; `check_pattern`…; `rx_check_pattern`…), tested against the shared
vectors in `v1.1/patterns.txt`. The checker validates `tasks-set` and every definition exactly as
rhun does and plays the events of a run.

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
