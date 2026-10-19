//! Write extensions for the [rhun](https://rhun.app) editor in Rust.
//!
//! An extension is a program rhun starts and talks to over its standard input and output, one
//! text line per message (the protocol: `v1/v1.md` in this repository). This crate does the
//! talking: it reads what rhun sends, calls your [`Handler`], frames your requests and waits for
//! their replies.
//!
//! ```no_run
//! use rhun_ext::{run, Ext, Handler, Doc};
//!
//! struct Hello;
//!
//! impl Handler for Hello {
//!     fn command(&mut self, ext: &Ext, name: &str, doc: Doc) {
//!         if name == "say" && doc != 0 {
//!             let _ = ext.toast("Hello from Rust");
//!         }
//!     }
//! }
//!
//! fn main() {
//!     run(&[], Hello).unwrap();
//! }
//! ```
//!
//! Requests ([`Ext::toast`], [`Ext::insert`], [`Ext::view`], …) block until rhun answers; events
//! that arrive meanwhile wait and are delivered afterwards, in order. Features (section 12 of the
//! spec) are asked for by name in [`run`]: [`Feature::Views`] for pictures in tabs of their own,
//! [`Feature::Complete`], [`Feature::Signature`], [`Feature::Definition`], [`Feature::References`],
//! [`Feature::Symbols`], [`Feature::Actions`], [`Feature::Rename`] and [`Feature::Format`] for
//! language support (the [`Handler`] methods of the same names answer them). Protocol 1.1 adds
//! [`Feature::QueryStream`] (parts and progress through a [`Stream`]), [`Feature::ProviderScope`]
//! ([`Ext::provides`]), [`Feature::CompleteItems`], [`Feature::SignatureList`],
//! [`Feature::Navigation`], [`Feature::PrepareRename`], [`Feature::PartialFormat`],
//! [`Feature::CodeActions`], [`Feature::HoverQuery`], [`Feature::SelectionRanges`],
//! [`Feature::Hierarchy`], [`Feature::WorkspaceSymbols`], [`Feature::DocumentLinks`] and
//! [`Feature::FileDiagnostics`] ([`Ext::file_diagnostics`]).
//!
//! Built for `wasm32-wasip1` (rhun runs such extensions itself, in any system: see
//! `run.wasm32-wasip1` in the spec), the crate needs no threads: a request reads rhun's input
//! itself until its reply comes, and keeps the events it reads meanwhile for later, so the
//! behaviour is the same.
//!
//! Protocol 1.1 adds workspace edits as transactions ([`Feature::WorkspaceEdits`],
//! [`Feature::FileOperations`]: [`WorkspaceEdit::file_hashed`], [`WorkspaceEdit::create`],
//! [`WorkspaceEdit::rename`], [`WorkspaceEdit::delete`], [`edits::from_ordered`] for LSP's ordered
//! changes, [`ApplyError`]), several selections ([`Feature::Selections`]: [`Ext::select_many`],
//! [`Handler::selections`]), [`Ext::reveal`] and [`Ext::show_locations`]
//! ([`Feature::Reveal`]), diff tabs ([`Feature::Diff`]: [`Ext::diff`]) and linked editing
//! ([`Feature::LinkedEditing`]: [`Handler::linked`]).

#![allow(clippy::needless_doctest_main)]

pub mod ai;
pub mod actions;
pub mod chart;
pub mod commands;
pub mod complete;
pub mod editor;
pub mod edits;
pub mod lang;
pub mod notebook;
pub mod notify;
pub mod langfeat;
pub mod picture;
pub mod scm;
pub mod query;
pub mod scmpanel;
pub mod snippet;
pub mod snippet_context;
pub mod snippet_transform;
pub mod views;
pub mod testing;
pub mod watch;
pub mod resources;
pub mod auth;
pub use resources::{parse_link, Content, UrlOpened};

use std::collections::{HashMap, VecDeque};
use std::fmt;
use std::io::{self, BufRead, BufReader, Read, Write};
use std::sync::atomic::AtomicBool;
use std::sync::mpsc::{channel, Receiver, Sender};
use std::sync::{Arc, Mutex};
#[cfg(not(target_os = "wasi"))]
use std::thread;

pub use actions::{
    encode_code_actions, Action, ActionResolveRequest, CodeActionsRequest, CommandRef,
    PreparedRename, RangeFormatRequest, ResolvedAction, TypeFormatRequest,
};
pub use commands::{command_ref, CallRequest, Invocation, MenuTarget, StatusItem};
pub use complete::{
    CompleteRequest, CompletionItem, CompletionList, Kind, Resolution, ResolveRequest,
    SignatureHelp, SignatureInfo, SignatureList, SignatureRequest,
};
pub use editor::{Linked, LinkedRequest, Reveal, Selection, Selections, Show};
pub use edits::{Change, Located, OrderError};
pub use lang::{
    fnv1a64, ActionsRequest, CodeAction, Create, Delete, Entry, FileEdit, FileOp, FormatRequest,
    Location, PosRequest, Rename, RenameRequest, SaveActionsRequest, Symbol, SymbolKind, Target,
    TextEdit, WorkspaceEdit,
};
pub use langfeat::{
    encode_calls, encode_document_links, encode_file_diagnostics, encode_hierarchy_items,
    encode_hierarchy_roots, encode_selection_ranges, encode_workspace_symbols, selection_chain,
    valid_diagnostic_id, Call, DiagnosticTag, DocRequest, DocumentLink, HierarchyItem, Hover,
    HoverRequest, LinkTarget, RelatedInfo, SelectionRangesRequest, WorkspaceSymbol,
    WorkspaceSymbolsRequest,
};
pub use picture::{Color, Picture};
pub use scm::{
    Blame, BlameCommit, BlameRun, CommitMessageRequest, GitHead, GitRepo, GitState, GitStatusEntry,
    LogEntry, LogOf, QuickDiffRequest,
};
pub use query::Stream;
pub use scmpanel::{ScmAction, ScmGroup, ScmPanel, ScmResource};
pub use snippet::{Field, PreparedSnippet, SnippetChoice, SnippetProgram};
pub use snippet_context::SnippetSnapshot;
pub use snippet_transform::{
    CompiledSnippetTransform, SnippetFieldTransform, SnippetTransformProgramError,
};
pub use watch::{parse_workspace_watch, WorkspaceWatch, WorkspaceWatchEvent};
pub use testing::{
    expected_actual, unquote, CoverageRange, FileCoverage, TestCommand, TestItem, TestKind,
    TestMessage, TestProcess, TestReporter, TestResult, TestRun, TestState, TestStream,
};

/// A URI where rhun takes a document (feature `uris`): one field, without spaces or line breaks
/// (write a space as `%20`).
fn uri_field(uri: &str) -> Result<(), Error> {
    if uri.is_empty()
        || uri.len() > 4128
        || uri.bytes().any(|b| matches!(b, b' ' | b'\n' | b'\r' | 0))
    {
        return Err(Error {
            code: "syntax".into(),
            text: "a URI field is 1 to 4128 bytes without spaces or line breaks".into(),
        });
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn whole_ranges() {
        assert_eq!(
            whole_range(b"ab\ncd"),
            Range::new(Pos::new(1, 1), Pos::new(2, 3))
        );
        assert_eq!(
            whole_range(b"ab\n"),
            Range::new(Pos::new(1, 1), Pos::new(2, 1))
        );
        assert_eq!(whole_range(b""), Range::new(Pos::new(1, 1), Pos::new(1, 1)));
    }

    fn view_event(fields: &[&str], body: &[u8]) -> Event {
        Event {
            name: "view".into(),
            fields: fields.iter().map(|f| f.to_string()).collect(),
            rest: String::new(),
            body: body.to_vec(),
        }
    }

    #[test]
    fn picture_keyboard_events() {
        let k = parse_view_event(&view_event(&["4", "key", "ctrl+shift", "a"], b""));
        assert_eq!(k, Some((4, ViewEvent::Key { mods: "ctrl+shift".into(), name: "a".into() })));
        let t = parse_view_event(&view_event(&["4", "text", "2"], "é".as_bytes()));
        assert_eq!(t, Some((4, ViewEvent::Text("é".into()))));
        assert_eq!(parse_view_event(&view_event(&["4", "selectall"], b"")), Some((4, ViewEvent::SelectAll)));
        let c = view_event(&["4", "click", "5", "6", "shift"], b"");
        assert_eq!(view_event_mods(&c), Some("shift"));
        assert_eq!(parse_view_event(&c), Some((4, ViewEvent::Click { x: 5.0, y: 6.0 })));
        assert!(views::parse_retained(&view_event(&["4", "key", "-", "a"], b"")).is_none());
    }
}

/// The protocol version this crate speaks.
pub const PROTOCOL: u32 = 1;

/// A document id, unique for the session; 0 means none.
pub type Doc = i64;

/// A position: 1-based line, 1-based byte column within the line's UTF-8 bytes.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct Pos {
    pub line: u32,
    pub col: u32,
}

impl Pos {
    pub fn new(line: u32, col: u32) -> Pos {
        Pos { line, col }
    }
}

/// A half-open range: from `start` up to, not including, `end`.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash)]
pub struct Range {
    pub start: Pos,
    pub end: Pos,
}

impl Range {
    pub fn new(start: Pos, end: Pos) -> Range {
        Range { start, end }
    }
}

/// A feature of section 12 of the spec, asked for in `hello`.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Feature {
    /// Pictures in tabs of their own: [`Ext::view`], [`Ext::draw`], [`Handler::view`].
    Views,
    /// Completion lists: [`Handler::complete`].
    Complete,
    /// Signature help: [`Handler::signature`].
    Signature,
    /// Go to definition: [`Handler::definition`].
    Definition,
    /// Find all references: [`Handler::references`].
    References,
    /// The symbols of a document: [`Handler::symbols`].
    Symbols,
    /// Code actions: [`Handler::actions`] and [`Handler::action`].
    Actions,
    /// Rename a symbol: [`Handler::rename`].
    Rename,
    /// Format a document: [`Handler::format`].
    Format,
    /// Remove runtime commands: [`Ext::unregister`] (protocol 1.1).
    Commands,
    /// Source actions before saving: [`Handler::save_actions`] and [`Ext::save_apply`].
    SaveActions,
    /// Native editable fields in completions and explicit insertion (protocol 1.1).
    Snippets,
    SnippetVariants,
    /// Compiled transforms when navigating native snippet fields.
    SnippetTransforms,
    /// Coalesced workspace filesystem invalidations.
    WorkspaceWatch,
    /// Notebooks: [`Handler::nb_open`], [`Handler::nb_save`], [`Handler::nb_run`] and the
    /// [`Ext::nb_state`] family ([`notebook`]).
    Notebooks,
    /// Git data through rhun: [`Ext::git_repo`], [`Ext::git_status`], [`Ext::git_blame`],
    /// [`Ext::git_log`], [`Ext::git_show`], [`Ext::git_diff_open`]; needs `[needs] git = read`.
    GitData,
    /// Commit messages for Generate: [`Handler::commit_message`]; `[provides] commit_message = yes`.
    CommitMessage,
    /// Quick-diff bases for the gutter: [`Handler::quickdiff`], [`Ext::quickdiff_refresh`];
    /// `[provides] quickdiff = PATTERNS`.
    QuickDiff,
    /// Retained views: manifest `[views]`, [`Ext::patch`], [`Ext::vshow`],
    /// [`Handler::view_open`] and [`Handler::view_event`] (protocol 1.1, [`views`]).
    ViewTree,
    /// Not stopped for idleness while busy: [`Ext::busy`] (resources group).
    Idle,
    /// Passwords and tokens in the system keychain: [`Ext::secret_get`] and the others.
    Secrets,
    /// URIs and virtual documents: [`Handler::content`], [`Ext::open_uri`], [`Ext::content_changed`].
    Uris,
    /// Test controllers (protocol 1.1): [`Handler::resolve_tests`], [`Handler::run_tests`],
    /// [`Ext::set_test_items`], [`Ext::test_exec`].
    Tests,
    /// Agent sessions in rhun's agents panel: [`Ext::agent_sessions`], [`Handler::agent_transcript`].
    Agents,
    /// Edit proposals the user reviews: [`Ext::propose`], [`Handler::proposal`].
    EditReview,
    /// Tools called through rhun: [`Ext::tools`], [`Ext::tool_call`], [`Handler::tool`].
    Tools,
    /// Prompts, Stop and cards for its sessions: [`Handler::agent_prompt`], [`Ext::agent_ask`].
    AgentPrompts,
    /// Workspace edits as transactions: files that are not open edited on disk (with an
    /// `fnv1a64` precondition), a label, `confirm`, one undo, [`ApplyError`] (protocol 1.1).
    WorkspaceEdits,
    /// Creating, renaming and deleting files in workspace edits (with [`Feature::WorkspaceEdits`]).
    FileOperations,
    /// Every selection ([`Handler::selections`]) and [`Ext::select_many`].
    Selections,
    /// [`Ext::reveal`] and [`Ext::show_locations`].
    Reveal,
    /// Diff tabs between two open documents: [`Ext::diff`], [`Ext::diff_close`].
    Diff,
    /// Linked editing ranges: [`Handler::linked`].
    LinkedEditing,
    /// Parts of long answers and progress on rhun's pending requests: [`Stream`] (protocol 1.1).
    QueryStream,
    /// Narrowing `[provides]` while running: [`Ext::provides`] (protocol 1.1).
    ProviderScope,
    /// Completion items with resolve data, a replace range, commit characters and more:
    /// [`CompletionItem`]'s fields and [`Handler::complete_resolve`] (protocol 1.1).
    CompleteItems,
    /// Every overload in signature help: [`Handler::signatures`] (protocol 1.1).
    SignatureList,
    /// Go to Declaration, Type Definition and Implementation: [`Handler::declaration`],
    /// [`Handler::type_definition`], [`Handler::implementation`] (protocol 1.1).
    Navigation,
    /// What F2 selects and fills the prompt with: [`Handler::prepare_rename`] (protocol 1.1).
    PrepareRename,
    /// Format Selection and formatting as you type: [`Handler::range_format`] and
    /// [`Handler::type_format`] (protocol 1.1).
    PartialFormat,
    /// Code actions with hierarchical kinds, resolve data and commands, in place of v1's
    /// `actions`: [`Handler::code_actions`] and [`Handler::code_action_resolve`] (protocol 1.1).
    CodeActions,
    /// The hover card's text and range: [`Handler::hover`] (protocol 1.1).
    HoverQuery,
    /// Expand and Shrink Selection: [`Handler::selection_ranges`] (protocol 1.1).
    SelectionRanges,
    /// Call and type hierarchies: [`Handler::call_hierarchy`], [`Handler::calls_in`],
    /// [`Handler::calls_out`], [`Handler::type_hierarchy`], [`Handler::supertypes`],
    /// [`Handler::subtypes`] (protocol 1.1).
    Hierarchy,
    /// Go to Symbol in Project: [`Handler::workspace_symbols`] (protocol 1.1).
    WorkspaceSymbols,
    /// Diagnostics for any file in rhun's Problems store: [`Ext::file_diagnostics`] and
    /// [`Ext::reset_diagnostics`] (protocol 1.1).
    FileDiagnostics,
    /// Links under ctrl+hover: [`Handler::document_links`] (protocol 1.1).
    DocumentLinks,
    /// Log views (`kind = log`, with [`Feature::ViewTree`]): [`views::Patch::log`].
    ViewLog,
    /// Pictures with the keyboard (with [`Feature::Views`]): [`ViewEvent::Key`] and the others,
    /// [`Handler::view_copy`].
    ViewKeys,
    /// Decoded images by handle: [`Ext::image`], [`picture::Picture::image`].
    ViewImages,
    /// Notification cards with buttons: [`Ext::notify`], [`Handler::note_event`] ([`notify`]).
    Notify,
    /// Progress in the status bar, a card or a view: [`Ext::progress`], [`Handler::progress_cancel`].
    Progress,
    /// Version control panels: [`Ext::scm_register`], [`Ext::scm_update`], [`Ext::scm_input`].
    Scm,
    /// Where commands are invoked from and their bodies ([`Handler::invoke`]), command
    /// references ([`command_ref`]), [`Ext::run_command`] and [`Handler::call`] (protocol 1.1).
    CommandArgs,
    /// The extension's own keys for when clauses: [`Ext::set_context`].
    ContextKeys,
    /// Status bar items: [`Ext::set_status_item`], [`Ext::remove_status_item`].
    StatusItems,
    /// Opening URLs and link callbacks: [`Ext::open_url`], [`Ext::external_uri`], [`Ext::link`]
    /// and [`parse_link`]; sign-in helpers in [`auth`].
    Urls,
}

impl Feature {
    pub fn name(self) -> &'static str {
        match self {
            Feature::Views => "views",
            Feature::Complete => "complete",
            Feature::Signature => "signature",
            Feature::Definition => "definition",
            Feature::References => "references",
            Feature::Symbols => "symbols",
            Feature::Actions => "actions",
            Feature::Rename => "rename",
            Feature::Format => "format",
            Feature::Commands => "commands",
            Feature::SaveActions => "save-actions",
            Feature::Snippets => "snippets",
            Feature::SnippetVariants => "snippet-variants",
            Feature::SnippetTransforms => "snippet-transforms",
            Feature::WorkspaceWatch => "workspace_watch",
            Feature::Notebooks => "notebooks",
            Feature::GitData => "git-data",
            Feature::CommitMessage => "commit-message",
            Feature::QuickDiff => "quickdiff",
            Feature::ViewTree => "view-tree",
            Feature::Idle => "idle",
            Feature::Secrets => "secrets",
            Feature::Uris => "uris",
            Feature::Tests => "tests",
            Feature::Agents => "agents",
            Feature::EditReview => "edit-review",
            Feature::Tools => "tools",
            Feature::AgentPrompts => "agent-prompts",
            Feature::WorkspaceEdits => "workspace-edits",
            Feature::FileOperations => "file-operations",
            Feature::Selections => "selections",
            Feature::Reveal => "reveal",
            Feature::Diff => "diff",
            Feature::LinkedEditing => "linked-editing",
            Feature::QueryStream => "query-stream",
            Feature::ProviderScope => "provider-scope",
            Feature::CompleteItems => "complete-items",
            Feature::SignatureList => "signature-list",
            Feature::HoverQuery => "hover-query",
            Feature::Navigation => "navigation",
            Feature::SelectionRanges => "selection-ranges",
            Feature::Hierarchy => "hierarchy",
            Feature::WorkspaceSymbols => "workspace-symbols",
            Feature::PrepareRename => "prepare-rename",
            Feature::PartialFormat => "partial-format",
            Feature::CodeActions => "code-actions",
            Feature::FileDiagnostics => "file-diagnostics",
            Feature::DocumentLinks => "document-links",
            Feature::ViewLog => "view-log",
            Feature::ViewKeys => "view-keys",
            Feature::ViewImages => "view-images",
            Feature::Notify => "notify",
            Feature::Progress => "progress",
            Feature::Scm => "scm",
            Feature::CommandArgs => "command-args",
            Feature::ContextKeys => "context-keys",
            Feature::StatusItems => "status-items",
            Feature::Urls => "urls",
        }
    }
}

/// A title on one line, at most 200 bytes (whole characters).
fn views_title(title: &str) -> String {
    let t = title.replace('\n', " ");
    let mut n = t.len().min(200);
    while !t.is_char_boundary(n) {
        n -= 1;
    }
    t[..n].to_string()
}

/// Anything rhun tells the extension without expecting an answer, as it arrived: `0 NAME FIELDS…`.
#[derive(Clone, Debug, Default)]
pub struct Event {
    pub name: String,
    pub fields: Vec<String>,
    /// The rest of the line after the name, for events whose last field may hold spaces (paths).
    pub rest: String,
    /// The body, for `edits` and `action`.
    pub body: Vec<u8>,
}

impl Event {
    /// The i-th field as a number, or 0.
    pub fn num(&self, i: usize) -> i64 {
        self.fields.get(i).and_then(|f| f.parse().ok()).unwrap_or(0)
    }
}

/// A document rhun reported with `opened`.
#[derive(Clone, Debug)]
pub struct Opened {
    pub doc: Doc,
    pub version: i64,
    /// The grammar's name with spaces as `_`, or `-`.
    pub lang: String,
    /// The absolute path, or `-` for an untitled document.
    pub path: String,
}

impl Opened {
    pub fn parse(ev: &Event) -> Option<Opened> {
        if ev.name != "opened" || ev.fields.len() < 4 {
            return None;
        }
        // PATH is the last field and may hold spaces: it is the rest after the third field
        let mut parts = ev.rest.splitn(4, ' ');
        let doc = parts.next()?.parse().ok()?;
        let version = parts.next()?.parse().ok()?;
        let lang = parts.next()?.to_string();
        let path = parts.next()?.to_string();
        Some(Opened {
            doc,
            version,
            lang,
            path,
        })
    }
}

/// One record of an `edits` body: at byte offset `start`, `del` bytes were replaced by `ins`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Edit {
    pub start: usize,
    pub del: usize,
    pub ins: Vec<u8>,
}

/// Decodes an `edits` body (section 6a of the spec).
pub fn parse_edits(body: &[u8]) -> Result<Vec<Edit>, String> {
    let mut out = Vec::new();
    let mut pos = 0;
    while pos < body.len() {
        let nl = body[pos..]
            .iter()
            .position(|&b| b == b'\n')
            .ok_or("an edit header without LF")?
            + pos;
        let head = std::str::from_utf8(&body[pos..nl]).map_err(|_| "a header that is not UTF-8")?;
        let f: Vec<&str> = head.split(' ').collect();
        if f.len() != 3 {
            return Err(format!("an edit header of {} fields", f.len()));
        }
        let num = |s: &str| s.parse::<usize>().map_err(|_| format!("not a number: {s}"));
        let (start, del, len) = (num(f[0])?, num(f[1])?, num(f[2])?);
        if len > body.len() - (nl + 1) {
            return Err("an edit longer than the body".into());
        }
        out.push(Edit {
            start,
            del,
            ins: body[nl + 1..nl + 1 + len].to_vec(),
        });
        pos = nl + 1 + len;
    }
    Ok(out)
}

/// Applies edit records to the text they were made against.
pub fn apply_edits(text: &mut Vec<u8>, edits: &[Edit]) -> Result<(), String> {
    for e in edits {
        if e.start
            .checked_add(e.del)
            .map_or(true, |end| end > text.len())
        {
            return Err("an edit outside the text".into());
        }
        text.splice(e.start..e.start + e.del, e.ins.iter().copied());
    }
    Ok(())
}

/// The range that covers all of a text: from 1:1 to the end of its last line.
pub fn whole_range(text: &[u8]) -> Range {
    let line = text.iter().filter(|&&b| b == b'\n').count() as u32 + 1;
    let last = text
        .iter()
        .rposition(|&b| b == b'\n')
        .map(|i| i + 1)
        .unwrap_or(0);
    Range::new(
        Pos::new(1, 1),
        Pos::new(line, (text.len() - last) as u32 + 1),
    )
}

/// The severity of a diagnostic.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Severity {
    #[default]
    Error,
    Warning,
    Info,
    Hint,
}

impl Severity {
    fn word(self) -> &'static str {
        match self {
            Severity::Error => "error",
            Severity::Warning => "warning",
            Severity::Info => "info",
            Severity::Hint => "hint",
        }
    }
}

/// A mark: a range, a severity and a one-line message. The fields after `message` travel only
/// with [`Ext::file_diagnostics`] (protocol 1.1; [`Ext::diagnostics`] sends `source` as v1 did):
/// `id`, a token of `[A-Za-z0-9_.:-]` unique in the set, comes back in code-actions requests
/// ([`CodeActionsRequest::diagnostics`]); `code` and `source` show as `source(code)`; `href` is an
/// http(s) link explaining the code; `tags`; `related` places.
#[derive(Clone, Debug, Default)]
pub struct Diagnostic {
    pub range: Range,
    pub severity: Severity,
    pub source: String,
    pub message: String,
    pub id: String,
    pub code: String,
    pub href: String,
    pub tags: Vec<DiagnosticTag>,
    pub related: Vec<RelatedInfo>,
}

/// What a view tells its extension (section 12a).
#[derive(Clone, Debug, PartialEq)]
pub enum ViewEvent {
    /// The drawable size in logical pixels: draw again for it.
    Size {
        width: f32,
        height: f32,
    },
    /// Text sizes: character advance and line height, logical pixels.
    Metrics(Metrics),
    Hover {
        x: f32,
        y: f32,
    },
    Down {
        x: f32,
        y: f32,
    },
    Drag {
        x: f32,
        y: f32,
    },
    Up {
        x: f32,
        y: f32,
    },
    Click {
        x: f32,
        y: f32,
    },
    Leave,
    Wheel {
        x: f32,
        y: f32,
        dy: f32,
    },
    /// The user closed the tab; the id is not used again.
    Closed,
    /// With [`Feature::ViewKeys`]: the keyboard came (true) or went.
    Focus(bool),
    /// A key no binding takes: modifiers (`-` or `ctrl+shift+alt+super`) and its name (`Return`,
    /// `Left`, `F5`, `a`).
    Key { mods: String, name: String },
    /// A typed character, after its [`ViewEvent::Key`].
    Text(String),
    /// The paste command's text.
    Paste(String),
    /// The select-all command.
    SelectAll,
    /// The second press of a double click.
    DoubleClick { x: f32, y: f32 },
}

/// The modifiers (`-` or `ctrl+shift+alt+super`) a pointer event carries with
/// [`Feature::ViewKeys`] (its last field): read it in [`Handler::event`].
pub fn view_event_mods(ev: &Event) -> Option<&str> {
    let what = ev.fields.get(1)?.as_str();
    let at = match what {
        "down" | "up" | "click" | "drag" | "dblclick" => 4,
        "wheel" => 5,
        _ => return None,
    };
    ev.fields.get(at).map(String::as_str)
}

/// The advance and line height of a text size, logical pixels.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct TextMetric {
    pub advance: f32,
    pub line_height: f32,
}

/// The four text sizes of a view: `small`, `normal`, `large`, `mono`. rhun's interface font is
/// monospaced, so a text's width is its characters times the advance (a wide character counts
/// twice): [`Metrics::width`].
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Metrics {
    pub small: TextMetric,
    pub normal: TextMetric,
    pub large: TextMetric,
    pub mono: TextMetric,
}

impl Default for Metrics {
    /// A guess at scale 1, for drawing before the first `metrics` event.
    fn default() -> Metrics {
        Metrics {
            small: TextMetric {
                advance: 6.0,
                line_height: 16.0,
            },
            normal: TextMetric {
                advance: 7.0,
                line_height: 18.0,
            },
            large: TextMetric {
                advance: 10.0,
                line_height: 26.0,
            },
            mono: TextMetric {
                advance: 7.0,
                line_height: 19.0,
            },
        }
    }
}

/// A text size of a view.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum TextSize {
    Small,
    Normal,
    Large,
    Mono,
}

impl TextSize {
    pub fn word(self) -> &'static str {
        match self {
            TextSize::Small => "small",
            TextSize::Normal => "normal",
            TextSize::Large => "large",
            TextSize::Mono => "mono",
        }
    }
}

impl Metrics {
    pub fn of(&self, size: TextSize) -> TextMetric {
        match size {
            TextSize::Small => self.small,
            TextSize::Normal => self.normal,
            TextSize::Large => self.large,
            TextSize::Mono => self.mono,
        }
    }

    /// How wide `text` is drawn at `size`, logical pixels.
    pub fn width(&self, size: TextSize, text: &str) -> f32 {
        let cells: usize = text.chars().map(|c| if is_wide(c) { 2 } else { 1 }).sum();
        cells as f32 * self.of(size).advance
    }

    fn parse(fields: &[String]) -> Option<Metrics> {
        // small ADV LH normal ADV LH large ADV LH mono ADV LH
        let mut m = Metrics::default();
        let mut i = 0;
        while i + 2 < fields.len() {
            let tm = TextMetric {
                advance: fields[i + 1].parse().ok()?,
                line_height: fields[i + 2].parse().ok()?,
            };
            match fields[i].as_str() {
                "small" => m.small = tm,
                "normal" => m.normal = tm,
                "large" => m.large = tm,
                "mono" => m.mono = tm,
                _ => {}
            }
            i += 3;
        }
        Some(m)
    }
}

fn is_wide(c: char) -> bool {
    matches!(c as u32, 0x1100..=0x115F | 0x2E80..=0xA4CF | 0xAC00..=0xD7A3 | 0xF900..=0xFAFF | 0xFE30..=0xFE4F
        | 0xFF00..=0xFF60 | 0xFFE0..=0xFFE6 | 0x1F300..=0x1F64F | 0x1F900..=0x1F9FF | 0x20000..=0x3FFFD)
}

/// A view's id (section 12a).
pub type View = u64;

/// An error reply from rhun: `error ID CODE TEXT`.
#[derive(Clone, Debug)]
pub struct Error {
    pub code: String,
    pub text: String,
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "rhun: {}: {}", self.code, self.text)
    }
}

impl std::error::Error for Error {}

/// A refused workspace edit: `error ID CODE ENTRY TEXT` with feature `workspace-edits` agreed,
/// where ENTRY is the 1-based number of the body's entry at fault ([`WorkspaceEdit::entry`]), or
/// 0; without the feature `entry` is 0 and `text` the whole sentence. Codes: `syntax`, `needs`,
/// `nofile`, `nodoc`, `stale`, `readonly`, `badpos`, `toobig`, `refused`, `cancelled`, `busy`,
/// `io` (the disk refused a write and rhun put everything back: the edit may be sent again), and
/// `unknown` for an edit that needs a feature rhun did not agree.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ApplyError {
    pub code: String,
    pub entry: u32,
    pub text: String,
}

impl fmt::Display for ApplyError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if self.entry > 0 {
            write!(
                f,
                "rhun: {}: entry {}: {}",
                self.code, self.entry, self.text
            )
        } else {
            write!(f, "rhun: {}: {}", self.code, self.text)
        }
    }
}

impl std::error::Error for ApplyError {}

impl From<ApplyError> for Error {
    fn from(e: ApplyError) -> Error {
        Error {
            code: e.code,
            text: if e.entry > 0 {
                format!("{} {}", e.entry, e.text)
            } else {
                e.text
            },
        }
    }
}

impl ApplyError {
    /// An error reply's `ENTRY TEXT` split when the feature is agreed.
    fn from_reply(e: Error, entries: bool) -> ApplyError {
        if entries {
            let (n, rest) = e.text.split_once(' ').unwrap_or((e.text.as_str(), ""));
            if !n.is_empty() && n.len() <= 10 && n.bytes().all(|b| b.is_ascii_digit()) {
                if let Ok(entry) = n.parse() {
                    return ApplyError {
                        code: e.code,
                        entry,
                        text: rest.to_string(),
                    };
                }
            }
        }
        ApplyError {
            code: e.code,
            entry: 0,
            text: e.text,
        }
    }
}

/// A successful reply: the fields after `ok ID`, and the body when the request has one.
#[derive(Clone, Debug, Default)]
pub struct Reply {
    pub fields: Vec<String>,
    pub body: Vec<u8>,
}

/// What the extension implements. Every method has a default that does nothing.
pub trait Handler: Send {
    /// rhun is ready: the project's folder, or `None` without one. Settings (if any) and the
    /// catch-up events follow.
    fn ready(&mut self, _ext: &Ext, _project: Option<&str>) {}
    /// Any event, before the more specific methods below are called for it.
    fn event(&mut self, _ext: &Ext, _ev: &Event) {}
    /// One of the extension's commands: `name` without the `NAME.` prefix; `doc` the active
    /// document or 0.
    fn command(&mut self, _ext: &Ext, _name: &str, _doc: Doc) {}
    /// Every invocation of one of the extension's commands, with where it came from and its body
    /// (feature `command-args`; without it `from` and `body` are empty). The default calls
    /// [`Handler::command`].
    fn invoke(&mut self, ext: &Ext, inv: &Invocation) {
        self.command(ext, &inv.name, inv.doc);
    }
    /// Another extension runs one of this extension's commands declared callable
    /// (`[command-options] NAME.callable = yes`, feature `command-args`): the result (at most
    /// 1 MiB) or an error with its code and text, which go back to the caller. A `cancel` event
    /// names `req.id`.
    fn call(&mut self, _ext: &Ext, _req: &CallRequest) -> Result<Vec<u8>, Error> {
        Err(Error {
            code: "refused".into(),
            text: "not callable".into(),
        })
    }
    /// One of its own settings (section 6d): at start and on every change.
    fn setting(&mut self, _ext: &Ext, _key: &str, _value: &str) {}
    /// Something happened in one of its views.
    fn view(&mut self, _ext: &Ext, _view: View, _ev: ViewEvent) {}
    /// A declared view's instance exists (feature `view-tree`): fill it with [`Ext::patch`].
    fn view_open(&mut self, _ext: &Ext, _open: &views::ViewOpen) {}
    /// The user did something in a retained view (feature `view-tree`).
    fn view_event(&mut self, _ext: &Ext, _view: View, _ev: &views::RetainedEvent) {}
    /// Copy or cut in a picture with the keyboard (feature `view-keys`): the text for the
    /// clipboard, or `None` to leave it.
    fn view_copy(&mut self, _ext: &Ext, _view: View, _cut: bool) -> Option<String> {
        None
    }
    /// The user pressed a button of one of its cards, or closed it (feature `notify`).
    fn note_event(&mut self, _ext: &Ext, _note: u64, _ev: notify::NoteEvent) {}
    /// The user pressed Cancel on one of its progress entries (feature `progress`); rhun removes it
    /// 10 s later unless [`Ext::progress_end`] comes first.
    fn progress_cancel(&mut self, _ext: &Ext, _progress: u64) {}
    /// rhun asks for a node's children (feature `view-tree`): a patch with a `kids` op for it.
    fn view_children(&mut self, _ext: &Ext, _req: &views::ChildrenRequest) -> Result<views::Patch, String> {
        Err("no children".into())
    }
    /// rhun asks for more children of a node whose last `kids`/`add` had `more`: an `add` op.
    fn view_more(&mut self, _ext: &Ext, _req: &views::ChildrenRequest) -> Result<views::Patch, String> {
        Err("no more".into())
    }
    /// rhun asks for a completion list (feature `complete`).
    fn complete(&mut self, _ext: &Ext, _req: &CompleteRequest) -> CompletionList {
        CompletionList::default()
    }
    /// rhun asks for signature help (feature `signature`); `None` hides it.
    fn signature(&mut self, _ext: &Ext, _req: &SignatureRequest) -> Option<SignatureHelp> {
        None
    }
    /// rhun asks where the symbol at the cursor is defined (feature `definition`); several
    /// locations make a list, none says there is none.
    fn definition(&mut self, _ext: &Ext, _req: &PosRequest) -> Vec<Location> {
        Vec::new()
    }
    /// rhun asks for the references to the symbol at the cursor (feature `references`).
    fn references(&mut self, _ext: &Ext, _req: &PosRequest) -> Vec<Location> {
        Vec::new()
    }
    /// rhun asks for the document's symbols, in document order (feature `symbols`).
    fn symbols(&mut self, _ext: &Ext, _req: &PosRequest) -> Vec<Symbol> {
        Vec::new()
    }
    /// rhun asks for code actions at the selection (feature `actions`).
    fn actions(&mut self, _ext: &Ext, _req: &ActionsRequest) -> Vec<CodeAction> {
        Vec::new()
    }
    /// The user chose a code action that has data: its edit (if any) is applied, this is the
    /// rest (run a command, ask the language server).
    fn action(&mut self, _ext: &Ext, _data: &[u8]) {}
    /// rhun asks to rename the symbol at the cursor (feature `rename`): the edits, or why not
    /// (shown to the user).
    fn rename(&mut self, _ext: &Ext, _req: &RenameRequest) -> Result<WorkspaceEdit, String> {
        Err("cannot rename here".to_string())
    }
    /// rhun asks to format a document (feature `format`): the edits for it (usually one group);
    /// an empty one changes nothing.
    fn format(&mut self, _ext: &Ext, _req: &FormatRequest) -> WorkspaceEdit {
        WorkspaceEdit::default()
    }
    /// Every selection of a document changed (feature `selections`, `[subscribe] selection`):
    /// the primary first. A caret that only moved comes as `cursor` instead.
    fn selections(&mut self, _ext: &Ext, _sels: &Selections) {}
    /// rhun asks for the ranges that change together with the word at the cursor (feature
    /// `linked-editing`, `[provides] linked`); none says there are none.
    fn linked(&mut self, _ext: &Ext, _req: &LinkedRequest) -> Linked {
        Linked::default()
    }
    /// Source actions before saving. Intermediate command edits use [`Ext::save_apply`].
    /// A `cancel` event names `req.id`. Handlers run serially, so long work must be bounded;
    /// custom asynchronous dispatchers must revoke that invocation when cancellation arrives.
    fn save_actions(
        &mut self,
        _ext: &Ext,
        _req: &SaveActionsRequest,
    ) -> Result<WorkspaceEdit, String> {
        Ok(WorkspaceEdit::default())
    }
    /// rhun asks for a notebook's cells (feature `notebooks`): the file's bytes are in `req.data`.
    /// An error's text is shown in the notebook's tab, with a way to open the file as text.
    fn nb_open(&mut self, _ext: &Ext, _req: &notebook::NbOpen) -> Result<notebook::Notebook, String> {
        Err("this extension does not read notebooks".to_string())
    }
    /// rhun asks for the file's bytes for a notebook (feature `notebooks`); rhun writes them.
    fn nb_save(&mut self, _ext: &Ext, _req: &notebook::NbSave) -> Result<Vec<u8>, String> {
        Err("this extension does not write notebooks".to_string())
    }
    /// Run a cell (feature `notebooks`). Answer with [`Ext::nb_state`], [`Ext::nb_output`] and
    /// [`Ext::nb_stream`], from another thread for a long run: handlers run one at a time.
    fn nb_run(&mut self, _ext: &Ext, _run: &notebook::NbRun) {}
    /// The user asked to interrupt the running cell.
    fn nb_interrupt(&mut self, _ext: &Ext, _nb: notebook::NbId) {}
    /// Restart the kernel (the user confirmed).
    fn nb_restart(&mut self, _ext: &Ext, _nb: notebook::NbId) {}
    /// The kernel button was clicked: offer kernels ([`Ext::choose`], from another thread).
    fn nb_kernel(&mut self, _ext: &Ext, _nb: notebook::NbId) {}
    /// The notebook's tab closed.
    fn nb_closed(&mut self, _ext: &Ext, _nb: notebook::NbId) {}
    /// A kernel started with [`Ext::nb_exec`] ended: its exit status (128 + N after signal N), or
    /// `None` when rhun ended it.
    fn nb_exec_exit(&mut self, _ext: &Ext, _exec: notebook::ExecId, _code: Option<i32>) {}
    /// A cell document it was told about (`opened` with no path) belongs to notebook `nb` as cell
    /// `cell`, at `index` among all its cells (feature `notebooks`).
    fn nb_cell(&mut self, _ext: &Ext, _doc: Doc, _nb: notebook::NbId, _cell: notebook::CellId, _index: usize) {}
    /// The cell documents it knows of notebook `nb`, in their order (after any insert, delete or
    /// move).
    fn nb_cells(&mut self, _ext: &Ext, _nb: notebook::NbId, _docs: &[Doc]) {}
    /// rhun asks for a commit message (feature `commit-message`): the message, or why not (shown
    /// to the user). rhun trims it and refuses one over 8192 bytes or with control characters
    /// ([`scm::check_commit_message`]). A `cancel` event names `req.id`.
    fn commit_message(
        &mut self,
        _ext: &Ext,
        _req: &CommitMessageRequest,
    ) -> Result<String, String> {
        Err("no commit messages here".to_string())
    }
    /// rhun asks for a file's quick-diff base (feature `quickdiff`): its bytes, or `None` to
    /// leave the gutter marks to git.
    fn quickdiff(&mut self, _ext: &Ext, _req: &QuickDiffRequest) -> Option<Vec<u8>> {
        None
    }
    /// rhun asks for the children of a test item (`""`: the controller's roots; feature
    /// `tests`): they replace the item's children. An error's text shows on the item's row.
    fn resolve_tests(&mut self, _ext: &Ext, _parent: &str) -> Result<Vec<TestItem>, Error> {
        Err(Error {
            code: "unknown".into(),
            text: "no such request".into(),
        })
    }
    /// rhun starts a run (feature `tests`). Return at once: report from a thread of your own
    /// ([`TestReporter`]) and end the run with [`Ext::end_test_run`]. An error ends the run
    /// errored and its text is shown (`busy`, `refused`).
    fn run_tests(&mut self, _ext: &Ext, _run: &TestRun) -> Result<(), Error> {
        Err(Error {
            code: "refused".into(),
            text: "this extension runs no tests".into(),
        })
    }
    /// The user cancelled a run: stop it and end it with [`Ext::end_test_run`] (rhun stops its
    /// processes after 2 s and ends it itself after 5 s).
    fn cancel_tests(&mut self, _ext: &Ext, _run: u64) {}
    /// rhun asks for messages of one of its sessions from index `from` (feature agents).
    fn agent_transcript(
        &mut self,
        _ext: &Ext,
        _sid: &str,
        _from: u64,
    ) -> Result<ai::Transcript, ai::Refusal> {
        Err(ai::Refusal::new("refused", "no such session"))
    }
    /// What became of a proposal's file, or of the proposal (feature edit-review).
    fn proposal(&mut self, _ext: &Ext, _ev: ai::ProposalEvent) {}
    /// Another extension calls one of its [tools] (feature tools): the output, or why not. The
    /// input is opaque to rhun. Handlers run one at a time, so a long tool holds up the events
    /// behind it.
    fn tool(&mut self, _ext: &Ext, _call: &ai::ToolCall) -> Result<Vec<u8>, ai::Refusal> {
        Err(ai::Refusal::new("refused", "no such tool"))
    }
    /// The user sent a prompt to one of its sessions, or with `sid` None started a new one
    /// (feature agent-prompts): the session's id.
    fn agent_prompt(
        &mut self,
        _ext: &Ext,
        _sid: Option<&str>,
        _text: &str,
    ) -> Result<String, ai::Refusal> {
        Err(ai::Refusal::new("refused", "this extension takes no prompts"))
    }
    /// The user pressed Stop in one of its sessions (feature agent-prompts).
    fn agent_stop(&mut self, _ext: &Ext, _sid: &str) {}
    /// rhun is about to stop the extension.
    fn bye(&mut self, _ext: &Ext) {}
    /// rhun asks for a virtual document's text (feature `uris`): a URI of one of the schemes in
    /// `[provides] schemes`. An error shows in the document's tab.
    fn content(&mut self, _ext: &Ext, _uri: &str) -> Result<resources::Content, String> {
        Err("no such document".into())
    }
    /// The last tab showing a virtual document of this extension closed.
    fn content_closed(&mut self, _ext: &Ext, _uri: &str) {}

    // Protocol 1.1 language features. The `*_stream` methods are what rhun's requests call; by
    // default they answer with the methods above. `out` sends parts and progress (query-stream).
    /// A completion list that may come in parts: the first part replaces what the extension said
    /// before in the session, later parts and the answer add to it.
    fn complete_stream(
        &mut self,
        ext: &Ext,
        req: &CompleteRequest,
        _out: &Stream,
    ) -> CompletionList {
        self.complete(ext, req)
    }
    /// Definition locations that may come in parts; the answer holds the rest.
    fn definition_stream(&mut self, ext: &Ext, req: &PosRequest, _out: &Stream) -> Vec<Location> {
        self.definition(ext, req)
    }
    /// References that may come in parts; the answer holds the rest.
    fn references_stream(&mut self, ext: &Ext, req: &PosRequest, _out: &Stream) -> Vec<Location> {
        self.references(ext, req)
    }
    /// rhun resolves the selected item with its resolve data (complete-items).
    fn complete_resolve(&mut self, _ext: &Ext, _req: &ResolveRequest) -> Resolution {
        Resolution::default()
    }
    /// rhun asks for every overload (signature-list; it then sends this instead of `signature`);
    /// `None` hides the help.
    fn signatures(&mut self, _ext: &Ext, _req: &SignatureRequest) -> Option<SignatureList> {
        None
    }
    /// rhun asks where the symbol at the cursor is declared (navigation): as
    /// [`Handler::definition`].
    fn declaration(&mut self, _ext: &Ext, _req: &PosRequest) -> Vec<Location> {
        Vec::new()
    }
    /// rhun asks where the type of the symbol at the cursor is defined (navigation).
    fn type_definition(&mut self, _ext: &Ext, _req: &PosRequest) -> Vec<Location> {
        Vec::new()
    }
    /// rhun asks for the implementations of the symbol at the cursor (navigation).
    fn implementation(&mut self, _ext: &Ext, _req: &PosRequest) -> Vec<Location> {
        Vec::new()
    }
    /// Declarations that may come in parts; the answer holds the rest.
    fn declaration_stream(&mut self, ext: &Ext, req: &PosRequest, _out: &Stream) -> Vec<Location> {
        self.declaration(ext, req)
    }
    /// Type definitions that may come in parts; the answer holds the rest.
    fn type_definition_stream(
        &mut self,
        ext: &Ext,
        req: &PosRequest,
        _out: &Stream,
    ) -> Vec<Location> {
        self.type_definition(ext, req)
    }
    /// Implementations that may come in parts; the answer holds the rest.
    fn implementation_stream(
        &mut self,
        ext: &Ext,
        req: &PosRequest,
        _out: &Stream,
    ) -> Vec<Location> {
        self.implementation(ext, req)
    }
    /// F2 asks before the rename prompt opens (prepare-rename): the range to select and the name
    /// to start with; `None` for the word at the cursor; why nothing can be renamed there (shown
    /// to the user; neither the prompt nor a file rename follows).
    fn prepare_rename(
        &mut self,
        _ext: &Ext,
        _req: &PosRequest,
    ) -> Result<Option<PreparedRename>, String> {
        Ok(None)
    }
    /// rhun asks to format a range of a document (partial-format): edits for that document only.
    fn range_format(&mut self, _ext: &Ext, _req: &RangeFormatRequest) -> WorkspaceEdit {
        WorkspaceEdit::default()
    }
    /// A trigger character was typed (partial-format): edits for that document only, applied
    /// while it is still at the request's version.
    fn type_format(&mut self, _ext: &Ext, _req: &TypeFormatRequest) -> WorkspaceEdit {
        WorkspaceEdit::default()
    }
    /// rhun asks for code actions (code-actions, in place of [`Handler::actions`]). Actions whose
    /// kind does not match the kinds asked ([`CodeActionsRequest::wants`]) are left out.
    fn code_actions(&mut self, _ext: &Ext, _req: &CodeActionsRequest) -> Vec<Action> {
        Vec::new()
    }
    /// The user chose an action with resolve data and no edit (code-actions): its edit and
    /// command; an error says nothing comes.
    fn code_action_resolve(
        &mut self,
        _ext: &Ext,
        _req: &ActionResolveRequest,
    ) -> Result<ResolvedAction, String> {
        Err("nothing to resolve".to_string())
    }
    /// rhun asks for the hover card's text at a position (hover-query); `None` says nothing.
    fn hover(&mut self, _ext: &Ext, _req: &HoverRequest) -> Option<Hover> {
        None
    }
    /// rhun asks for the ranges around each selection's cursor (selection-ranges): a group per
    /// position, innermost first, each range containing the one before.
    fn selection_ranges(&mut self, _ext: &Ext, _req: &SelectionRangesRequest) -> Vec<Vec<Range>> {
        Vec::new()
    }
    /// rhun asks for the call hierarchy's roots at the cursor (hierarchy).
    fn call_hierarchy(&mut self, _ext: &Ext, _req: &PosRequest) -> Vec<HierarchyItem> {
        Vec::new()
    }
    /// rhun asks for the calls to the item whose data this is (hierarchy); they may come in parts.
    fn calls_in(&mut self, _ext: &Ext, _data: &[u8], _out: &Stream) -> Vec<Call> {
        Vec::new()
    }
    /// rhun asks for the calls the item whose data this is makes (hierarchy); they may come in
    /// parts.
    fn calls_out(&mut self, _ext: &Ext, _data: &[u8], _out: &Stream) -> Vec<Call> {
        Vec::new()
    }
    /// rhun asks for the type hierarchy's roots at the cursor (hierarchy).
    fn type_hierarchy(&mut self, _ext: &Ext, _req: &PosRequest) -> Vec<HierarchyItem> {
        Vec::new()
    }
    /// rhun asks for the supertypes of the item whose data this is (hierarchy); they may come in
    /// parts.
    fn supertypes(&mut self, _ext: &Ext, _data: &[u8], _out: &Stream) -> Vec<HierarchyItem> {
        Vec::new()
    }
    /// rhun asks for the subtypes of the item whose data this is (hierarchy); they may come in
    /// parts.
    fn subtypes(&mut self, _ext: &Ext, _data: &[u8], _out: &Stream) -> Vec<HierarchyItem> {
        Vec::new()
    }
    /// rhun asks for the project's symbols matching a query (workspace-symbols); they may come in
    /// parts, and a new query withdraws the request (`out` is then cancelled).
    fn workspace_symbols(
        &mut self,
        _ext: &Ext,
        _req: &WorkspaceSymbolsRequest,
        _out: &Stream,
    ) -> Vec<WorkspaceSymbol> {
        Vec::new()
    }
    /// rhun asks for the links in a document (document-links).
    fn document_links(&mut self, _ext: &Ext, _req: &DocRequest) -> Vec<DocumentLink> {
        Vec::new()
    }
}

enum Msg {
    Event(Event),
    HostRequest {
        id: u64,
        verb: String,
        fields: Vec<String>,
        /// The body of a request that has one (`commit-message`, `vkids`, `vmore`,
        /// `tests-resolve`, `test-run`, `tool`, `agent-prompt`, `nb-open`, `nb-save`).
        body: Vec<u8>,
    },
    /// a reply went to the request waiting for it
    Delivered,
    End,
}

/// Without threads (`wasm32-wasip1`): rhun's input, read by whoever waits, and the events and
/// requests read while a request waited for its reply, delivered after it in order.
struct Inline {
    input: Mutex<Box<dyn BufRead + Send>>,
    queue: Mutex<VecDeque<Msg>>,
}

type Waiter = Sender<Result<Reply, Error>>;

/// Requests waiting for their replies, and whether rhun has gone (then none may wait).
#[derive(Default)]
struct Pending {
    waiters: HashMap<u64, (String, Waiter)>,
    closed: bool,
}

fn closed_error() -> Error {
    Error {
        code: "closed".into(),
        text: "rhun went away".into(),
    }
}

struct Shared {
    out: Box<dyn Write + Send>,
    next_id: u64,
}

/// The connection to rhun. Cloning it is cheap; requests may be made from any thread.
#[derive(Clone)]
pub struct Ext {
    shared: Arc<Mutex<Shared>>,
    pending: Arc<Mutex<Pending>>,
    features: Arc<Mutex<Vec<String>>>,
    inline: Option<Arc<Inline>>,
    procs: testing::Procs,
    /// rhun's pending requests to the extension, and whether each was cancelled (query.rs)
    queries: Arc<Mutex<HashMap<u64, Arc<AtomicBool>>>>,
}

/// Speaks the protocol over standard input and output until rhun stops the extension, with
/// the features named (those rhun agrees to are in [`Ext::has`]).
pub fn run<H: Handler + 'static>(features: &[Feature], handler: H) -> io::Result<()> {
    run_on(io::stdin(), io::stdout(), features, handler)
}

/// [`run`] over any reader and writer (for tests).
pub fn run_on<R, W, H>(input: R, output: W, features: &[Feature], mut handler: H) -> io::Result<()>
where
    R: Read + Send + 'static,
    W: Write + Send + 'static,
    H: Handler + 'static,
{
    #[allow(unused_mut)]
    let mut ext = Ext {
        shared: Arc::new(Mutex::new(Shared {
            out: Box::new(output),
            next_id: 1,
        })),
        pending: Arc::new(Mutex::new(Pending::default())),
        features: Arc::new(Mutex::new(Vec::new())),
        inline: None,
        procs: Arc::default(),
        queries: Arc::new(Mutex::new(HashMap::new())),
    };
    let mut hello = String::from("hello 1");
    for f in features {
        hello.push(' ');
        hello.push_str(f.name());
    }
    ext.write(&hello, &[])?;
    #[cfg(not(target_os = "wasi"))]
    {
        let (tx, rx) = channel();
        {
            let ext = ext.clone();
            thread::spawn(move || ext.reader(BufReader::new(input), tx));
        }
        ext.dispatch(rx, &mut handler);
    }
    #[cfg(target_os = "wasi")]
    {
        ext.inline = Some(Arc::new(Inline {
            input: Mutex::new(Box::new(BufReader::new(input))),
            queue: Mutex::new(VecDeque::new()),
        }));
        ext.dispatch_inline(&mut handler);
    }
    Ok(())
}

impl Ext {
    /// Whether rhun agreed to a feature.
    pub fn has(&self, f: Feature) -> bool {
        self.features.lock().unwrap().iter().any(|n| n == f.name())
    }

    fn write(&self, line: &str, body: &[u8]) -> io::Result<()> {
        let mut s = self.shared.lock().unwrap();
        let mut buf = Vec::with_capacity(line.len() + 1 + body.len());
        buf.extend_from_slice(line.as_bytes());
        buf.push(b'\n');
        buf.extend_from_slice(body);
        s.out.write_all(&buf)?;
        s.out.flush()
    }

    /// Sends `ID VERB ARGS` (and a body, whose length is appended as the last field when the
    /// verb takes one) and waits for rhun's answer.
    pub fn request(&self, verb_and_args: &str, body: Option<&[u8]>) -> Result<Reply, Error> {
        let (tx, rx) = channel();
        let verb = verb_and_args.split(' ').next().unwrap_or("").to_string();
        let id = {
            let mut p = self.pending.lock().unwrap();
            if p.closed {
                return Err(closed_error());
            }
            // ids are 1 to 2147483647 (section 3), and one in flight is never used again
            let mut s = self.shared.lock().unwrap();
            let mut id = s.next_id;
            while p.waiters.contains_key(&id) {
                id = if id >= 2_147_483_647 { 1 } else { id + 1 };
            }
            s.next_id = if id >= 2_147_483_647 { 1 } else { id + 1 };
            p.waiters.insert(id, (verb, tx));
            id
        };
        let line = match body {
            Some(b) => format!("{id} {verb_and_args} {}", b.len()),
            None => format!("{id} {verb_and_args}"),
        };
        if let Err(e) = self.write(&line, body.unwrap_or(&[])) {
            self.pending.lock().unwrap().waiters.remove(&id);
            return Err(Error {
                code: "io".into(),
                text: e.to_string(),
            });
        }
        self.wait(rx)
    }

    /// The reply for a request: from the reader thread, or (without threads) read here.
    fn wait(&self, rx: Receiver<Result<Reply, Error>>) -> Result<Reply, Error> {
        if let Some(inline) = &self.inline {
            loop {
                if let Ok(reply) = rx.try_recv() {
                    return reply;
                }
                let msg = {
                    let mut input = inline.input.lock().unwrap();
                    self.read_msg(&mut **input)
                };
                match msg {
                    Msg::Delivered => {}
                    Msg::End => {
                        self.close();
                        inline.queue.lock().unwrap().push_back(Msg::End);
                        return rx.try_recv().unwrap_or_else(|_| Err(closed_error()));
                    }
                    other => inline.queue.lock().unwrap().push_back(other),
                }
            }
        }
        rx.recv().unwrap_or_else(|_| Err(closed_error()))
    }

    // ---- documents ----

    /// The document's text and its version.
    pub fn text(&self, doc: Doc) -> Result<(Vec<u8>, i64), Error> {
        let r = self.request(&format!("text {doc}"), None)?;
        Ok((
            r.body,
            r.fields.first().and_then(|v| v.parse().ok()).unwrap_or(0),
        ))
    }

    /// A range's text and the document's version.
    pub fn range_text(&self, doc: Doc, r: Range) -> Result<(Vec<u8>, i64), Error> {
        let rep = self.request(
            &format!(
                "range {doc} {} {} {} {}",
                r.start.line, r.start.col, r.end.line, r.end.col
            ),
            None,
        )?;
        Ok((
            rep.body,
            rep.fields.first().and_then(|v| v.parse().ok()).unwrap_or(0),
        ))
    }

    /// Replaces a range at a version (one undo step); the new version.
    pub fn replace(&self, doc: Doc, version: i64, r: Range, text: &str) -> Result<i64, Error> {
        let rep = self.request(
            &format!(
                "replace {doc} {version} {} {} {} {}",
                r.start.line, r.start.col, r.end.line, r.end.col
            ),
            Some(text.as_bytes()),
        )?;
        Ok(rep.fields.first().and_then(|v| v.parse().ok()).unwrap_or(0))
    }

    /// Inserts at the cursor at a version; the new version.
    pub fn insert(&self, doc: Doc, version: i64, text: &str) -> Result<i64, Error> {
        let rep = self.request(&format!("insert {doc} {version}"), Some(text.as_bytes()))?;
        Ok(rep.fields.first().and_then(|v| v.parse().ok()).unwrap_or(0))
    }

    /// Applies a workspace edit (section 12g) across documents and files, as the language
    /// features' answers do: rhun checks it whole and changes nothing on any error (`stale`,
    /// `badpos`, `nodoc`, `nofile`, …). Needs `documents = write`. For a language server's
    /// `workspace/applyEdit`. With [`Feature::WorkspaceEdits`] the edit is a transaction (files
    /// that are not open edited on disk, one undo) and the error names the entry at fault; file
    /// operations need [`Feature::FileOperations`] too (`unknown` without, nothing sent).
    pub fn apply(&self, edit: &WorkspaceEdit) -> Result<(), ApplyError> {
        let body = edit
            .encode_with(self.edit_caps())
            .map_err(|(code, text)| ApplyError {
                code: code.into(),
                entry: 0,
                text,
            })?;
        self.request("apply", Some(&body))
            .map(|_| ())
            .map_err(|e| ApplyError::from_reply(e, self.has(Feature::WorkspaceEdits)))
    }

    /// The parts of the workspace edit grammar rhun agreed to.
    fn edit_caps(&self) -> lang::EditCaps {
        lang::EditCaps {
            ws: self.has(Feature::WorkspaceEdits),
            ops: self.has(Feature::FileOperations),
        }
    }

    fn need(&self, f: Feature) -> Result<(), Error> {
        if self.has(f) {
            Ok(())
        } else {
            Err(Error {
                code: "unknown".into(),
                text: format!("{} feature was not agreed", f.name()),
            })
        }
    }

    // ---- the editor model (protocol 1.1) ----

    /// Sets a document's selections, the first becoming primary; overlapping ones merge
    /// (feature `selections`). 1 to 512 (`toobig` without asking otherwise).
    pub fn select_many(&self, doc: Doc, sels: &[Selection]) -> Result<(), Error> {
        self.need(Feature::Selections)?;
        let body = editor::encode_selections(sels).map_err(|text| Error {
            code: "toobig".into(),
            text,
        })?;
        self.request(&format!("select-many {doc} {}", sels.len()), Some(&body))
            .map(|_| ())
    }

    /// Scrolls a document's view so the position shows, without moving the cursor or
    /// activating its tab (feature `reveal`).
    pub fn reveal(&self, doc: Doc, at: Pos, how: Reveal) -> Result<(), Error> {
        self.need(Feature::Reveal)?;
        self.request(
            &format!("reveal {doc} {} {} {}", at.line, at.col, how.word()),
            None,
        )
        .map(|_| ())
    }

    /// [`Ext::reveal`] for the open document with that URI (features `reveal` and `uris`): a
    /// virtual document's URI, or a `file:` URI of an open file.
    pub fn reveal_uri(&self, uri: &str, at: Pos, how: Reveal) -> Result<(), Error> {
        self.need(Feature::Reveal)?;
        uri_field(uri)?;
        self.request(
            &format!("reveal {uri} {} {} {}", at.line, at.col, how.word()),
            None,
        )
        .map(|_| ())
    }

    /// Lists locations in the palette as Find All References does, under a title (feature
    /// `reveal`); `busy` while a prompt or the palette is open.
    pub fn show_locations(&self, title: &str, locs: &[Location]) -> Result<(), Error> {
        self.need(Feature::Reveal)?;
        let body = editor::encode_show_locations(title, locs);
        self.request("show-locations", Some(&body)).map(|_| ())
    }

    /// Opens (or reuses, for the same pair and title) a read-only tab with a unified diff from
    /// document `left` to document `right` (feature `diff`); the tab's id, which is not a
    /// document.
    pub fn diff(&self, left: Doc, right: Doc, title: &str, show: Show) -> Result<Doc, Error> {
        self.diff_sides(&left.to_string(), &right.to_string(), title, show)
    }

    /// [`Ext::diff`] with sides named by URI (features `diff` and `uris`): a virtual document's
    /// URI (opened without a tab when it is not open, and closed with the last diff tab showing
    /// it unless the user opened it meanwhile), a `file:` URI of an open file, or a document id.
    pub fn diff_uris(
        &self,
        left: &str,
        right: &str,
        title: &str,
        show: Show,
    ) -> Result<Doc, Error> {
        self.need(Feature::Diff)?;
        uri_field(left)?;
        uri_field(right)?;
        self.diff_sides(left, right, title, show)
    }

    fn diff_sides(&self, left: &str, right: &str, title: &str, show: Show) -> Result<Doc, Error> {
        self.need(Feature::Diff)?;
        let t = editor::title(title, editor::MAX_TITLE);
        let rep = self.request(
            &format!("diff {left} {right} {}", show.word()),
            Some(t.as_bytes()),
        )?;
        rep.fields
            .first()
            .and_then(|v| v.parse().ok())
            .ok_or_else(|| Error {
                code: "syntax".into(),
                text: "invalid diff reply".into(),
            })
    }

    /// Closes one of this extension's diff tabs (feature `diff`).
    pub fn diff_close(&self, tab: Doc) -> Result<(), Error> {
        self.need(Feature::Diff)?;
        self.request(&format!("diff-close {tab}"), None).map(|_| ())
    }

    /// Selects a range (the cursor at its end).
    pub fn select(&self, doc: Doc, r: Range) -> Result<(), Error> {
        self.request(
            &format!(
                "select {doc} {} {} {} {}",
                r.start.line, r.start.col, r.end.line, r.end.col
            ),
            None,
        )
        .map(|_| ())
    }

    /// Opens a file (absolute, or relative to the project) at a position; its document id.
    pub fn open(&self, path: &str, at: Pos) -> Result<Doc, Error> {
        let rep = self.request(&format!("open {} {} {path}", at.line, at.col), None)?;
        Ok(rep.fields.first().and_then(|v| v.parse().ok()).unwrap_or(0))
    }

    /// Replaces the extension's marks on a document (section 6b).
    pub fn diagnostics(&self, doc: Doc, version: i64, marks: &[Diagnostic]) -> Result<(), Error> {
        let mut body = String::new();
        for d in marks {
            let msg: String = d
                .message
                .chars()
                .map(|c| if c == '\n' { ' ' } else { c })
                .collect();
            let src = if d.source.is_empty() {
                "-".to_string()
            } else {
                d.source.replace(' ', "_")
            };
            body.push_str(&format!(
                "{} {} {} {} {} {} {}\n",
                d.range.start.line,
                d.range.start.col,
                d.range.end.line,
                d.range.end.col,
                d.severity.word(),
                src,
                msg
            ));
        }
        self.request(
            &format!("diagnostics {doc} {version} {}", marks.len()),
            Some(body.as_bytes()),
        )
        .map(|_| ())
    }

    /// The text to show for the position a `hover` event named.
    pub fn hover(&self, doc: Doc, at: Pos, text: &str) -> Result<(), Error> {
        self.request(
            &format!("hover {doc} {} {}", at.line, at.col),
            Some(text.as_bytes()),
        )
        .map(|_| ())
    }

    /// A per-document option (`tab_width`, `insert_spaces`, …), or `-` to drop it.
    pub fn option(&self, doc: Doc, key: &str, value: &str) -> Result<(), Error> {
        self.request(&format!("option {doc} {key} {value}"), None)
            .map(|_| ())
    }

    // ---- the editor ----

    /// A message on screen for a while (at most 200 bytes).
    pub fn toast(&self, text: &str) -> Result<(), Error> {
        self.request("toast", Some(text.as_bytes())).map(|_| ())
    }

    /// The extension's status-bar text (empty clears it).
    pub fn status(&self, text: &str) -> Result<(), Error> {
        self.request("status", Some(text.as_bytes())).map(|_| ())
    }

    /// A command added while running: `name` (its full name is `NAME.name`), default keys (or
    /// empty) and its title.
    pub fn register(&self, name: &str, keys: &str, title: &str) -> Result<(), Error> {
        let keys = if keys.is_empty() { "-" } else { keys };
        self.request(&format!("register {name} {keys} {title}"), None)
            .map(|_| ())
    }

    /// Remove an own runtime command and its shortcut. Requires agreed `commands`.
    pub fn unregister(&self, name: &str) -> Result<(), Error> {
        if !self.has(Feature::Commands) {
            return Err(Error {
                code: "unknown".into(),
                text: "commands feature was not agreed".into(),
            });
        }
        if name.is_empty()
            || name.len() > 40
            || !name
                .bytes()
                .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'_')
        {
            return Err(Error {
                code: "syntax".into(),
                text: "invalid command name".into(),
            });
        }
        self.request(&format!("unregister {name}"), None)
            .map(|_| ())
    }

    /// Apply an intermediate edit for an active save invocation and return the native version.
    /// The host restricts edits to the saving document and rejects stale or retired invocations.
    pub fn save_apply(
        &self,
        request_id: u64,
        doc: Doc,
        version: i64,
        edit: &WorkspaceEdit,
    ) -> Result<i64, Error> {
        if !self.has(Feature::SaveActions) {
            return Err(Error {
                code: "unknown".into(),
                text: "save-actions feature was not agreed".into(),
            });
        }
        if request_id == 0 || request_id > 2147483647 || doc <= 0 || version < 0 {
            return Err(Error {
                code: "syntax".into(),
                text: "invalid save-action identity".into(),
            });
        }
        let body = edit
            .encode_with(lang::EditCaps::V1)
            .map_err(|(code, text)| Error {
                code: code.into(),
                text,
            })?;
        let reply = self.request(
            &format!("save-apply {request_id} {doc} {version}"),
            Some(&body),
        )?;
        let current = if reply.fields.len() == 1
            && !reply.fields[0].is_empty()
            && reply.fields[0].bytes().all(|b| b.is_ascii_digit())
        {
            reply.fields[0].parse::<i64>().ok()
        } else {
            None
        };
        current.filter(|v| *v >= version).ok_or_else(|| Error {
            code: "syntax".into(),
            text: "invalid save-apply reply".into(),
        })
    }

    /// Replace a range and start a native snippet session without switching tabs.
    pub fn insert_snippet(
        &self,
        doc: Doc,
        version: i64,
        span: Range,
        snippet: &PreparedSnippet,
    ) -> Result<i64, Error> {
        self.insert_snippet_record(doc, version, span, || snippet.encode())
    }

    /// Replace a range with a prepared native program, including literal choices.
    pub fn insert_snippet_program(
        &self,
        doc: Doc,
        version: i64,
        span: Range,
        program: &SnippetProgram,
    ) -> Result<i64, Error> {
        if !program.initial_variants.is_empty() && !self.has(Feature::SnippetVariants) {
            return Err(Error {
                code: "unknown".into(),
                text: "snippet-variants feature was not agreed".into(),
            });
        }
        if !program.transforms.is_empty() && !self.has(Feature::SnippetTransforms) {
            return Err(Error {
                code: "unknown".into(),
                text: "snippet-transforms feature was not agreed".into(),
            });
        }
        self.insert_snippet_record(doc, version, span, || program.encode())
    }

    fn insert_snippet_record(
        &self,
        doc: Doc,
        version: i64,
        span: Range,
        encode: impl FnOnce() -> Result<Vec<u8>, String>,
    ) -> Result<i64, Error> {
        if !self.has(Feature::Snippets) {
            return Err(Error {
                code: "unknown".into(),
                text: "snippets feature was not agreed".into(),
            });
        }
        if doc <= 0
            || version < 0
            || span.start.line == 0
            || span.start.col == 0
            || span.end.line == 0
            || span.end.col == 0
            || span.end < span.start
        {
            return Err(Error {
                code: "syntax".into(),
                text: "invalid snippet target or range".into(),
            });
        }
        let body = encode().map_err(|text| Error {
            code: "syntax".into(),
            text,
        })?;
        let reply = self.request(
            &format!(
                "snippet {doc} {version} {} {} {} {}",
                span.start.line, span.start.col, span.end.line, span.end.col
            ),
            Some(&body),
        )?;
        let current = if reply.fields.len() == 1
            && !reply.fields[0].is_empty()
            && reply.fields[0].bytes().all(|b| b.is_ascii_digit())
        {
            reply.fields[0].parse::<i64>().ok()
        } else {
            None
        };
        current.filter(|v| *v >= version).ok_or_else(|| Error {
            code: "syntax".into(),
            text: "invalid snippet reply".into(),
        })
    }

    /// A pick list; the index of the item chosen, `None` when cancelled.
    pub fn choose(&self, prompt: &str, items: &[&str]) -> Result<Option<usize>, Error> {
        let mut body = prompt.to_string();
        for i in items {
            body.push('\n');
            body.push_str(i);
        }
        match self.request("choose", Some(body.as_bytes())) {
            Ok(r) => Ok(r
                .fields
                .first()
                .and_then(|v| v.parse::<usize>().ok())
                .map(|n| n.saturating_sub(1))),
            Err(e) if e.code == "cancelled" => Ok(None),
            Err(e) => Err(e),
        }
    }

    /// A text prompt; what the user typed, `None` when cancelled.
    pub fn ask(&self, prompt: &str) -> Result<Option<String>, Error> {
        match self.request("ask", Some(prompt.as_bytes())) {
            Ok(r) => Ok(Some(String::from_utf8_lossy(&r.body).into_owned())),
            Err(e) if e.code == "cancelled" => Ok(None),
            Err(e) => Err(e),
        }
    }

    /// Suggests a command in the terminal panel; the user runs it (needs `terminal = yes`).
    pub fn term(&self, command: &str) -> Result<(), Error> {
        self.request("term", Some(command.as_bytes())).map(|_| ())
    }

    /// Runs one of rhun's own commands from the list section 8a of the spec allows.
    pub fn cmd(&self, name: &str) -> Result<(), Error> {
        self.request(&format!("cmd {name}"), None).map(|_| ())
    }

    // ---- views (feature `views`) ----

    /// Opens a tab the extension draws in; its id.
    pub fn view(&self, title: &str) -> Result<View, Error> {
        let rep = self.request(&format!("view {title}"), None)?;
        Ok(rep.fields.first().and_then(|v| v.parse().ok()).unwrap_or(0))
    }

    /// The view's picture, replacing the last one.
    pub fn draw(&self, view: View, picture: &Picture) -> Result<(), Error> {
        self.request(&format!("draw {view}"), Some(picture.body().as_bytes()))
            .map(|_| ())
    }

    /// Closes a view's tab.
    pub fn unview(&self, view: View) -> Result<(), Error> {
        self.request(&format!("unview {view}"), None).map(|_| ())
    }

    // ---- retained views (feature `view-tree`) ----

    /// Applies a patch to a retained view, whole or not at all; the view's new revision.
    pub fn patch(&self, view: View, patch: &views::Patch) -> Result<u64, Error> {
        if !self.has(Feature::ViewTree) {
            return Err(Error {
                code: "unknown".into(),
                text: "view-tree feature was not agreed".into(),
            });
        }
        let body = patch.encode().map_err(|text| Error {
            code: "toobig".into(),
            text,
        })?;
        let rep = self.request(&format!("vpatch {view}"), Some(&body))?;
        Ok(rep.fields.first().and_then(|v| v.parse().ok()).unwrap_or(0))
    }

    /// Expands a node's ancestors and scrolls it into view; `how` among `select`, `focus`,
    /// `expand`.
    pub fn vreveal(&self, view: View, id: &[u8], how: &[&str]) -> Result<(), Error> {
        let how = if how.is_empty() { "-".to_string() } else { how.join(",") };
        self.request(&format!("vreveal {view} {how}"), Some(id)).map(|_| ())
    }

    /// Opens a declared view's place on it (its manifest key); `focus` also gives it the
    /// keyboard. Its view number.
    pub fn vshow(&self, name: &str, focus: bool) -> Result<View, Error> {
        let rep = self.request(&format!("vshow {name} {}", if focus { "focus" } else { "-" }), None)?;
        Ok(rep.fields.first().and_then(|v| v.parse().ok()).unwrap_or(0))
    }

    /// A retained view (`tree`, or `log` with [`Feature::ViewLog`]) in an editor tab of its own,
    /// shown at once; [`Handler::view_open`] follows with place `tab`. Close it with
    /// [`Ext::unview`].
    pub fn vtab(&self, kind: &str, title: &str) -> Result<View, Error> {
        let rep = self.request(&format!("vtab {kind} {}", views_title(title)), None)?;
        Ok(rep.fields.first().and_then(|v| v.parse().ok()).unwrap_or(0))
    }

    /// A PNG, JPEG or GIF decoded once (feature `view-images`): its handle for
    /// [`picture::Picture::image`].
    pub fn image(&self, bytes: &[u8]) -> Result<u64, Error> {
        let rep = self.request("vimage", Some(bytes))?;
        Ok(rep.fields.first().and_then(|v| v.parse().ok()).unwrap_or(0))
    }

    /// Lets an image go.
    pub fn unimage(&self, image: u64) -> Result<(), Error> {
        self.request(&format!("unimage {image}"), None).map(|_| ())
    }

    // ---- notifications and progress (features `notify`, `progress`) ----

    /// A notification card; its NOTE for [`Ext::notify_set`], [`Ext::unnotify`] and
    /// [`Handler::note_event`].
    pub fn notify(&self, severity: notify::Severity, note: &notify::Note) -> Result<u64, Error> {
        let rep = self.request(&format!("notify {}", severity.word()), Some(&note.encode()))?;
        Ok(rep.fields.first().and_then(|v| v.parse().ok()).unwrap_or(0))
    }

    /// Replaces a card's content (`noview` when it is gone: closed by the user, timed out).
    pub fn notify_set(&self, note: u64, content: &notify::Note) -> Result<(), Error> {
        self.request(&format!("notify-set {note}"), Some(&content.encode())).map(|_| ())
    }

    /// Closes a card without an event.
    pub fn unnotify(&self, note: u64) -> Result<(), Error> {
        self.request(&format!("unnotify {note}"), None).map(|_| ())
    }

    /// Progress (nothing shows if it ends within 500 ms); `cancel` offers a Cancel button. Its P.
    pub fn progress(&self, place: notify::ProgressWhere, cancel: bool, title: &str) -> Result<u64, Error> {
        let t = views_title(title);
        let t = if t.is_empty() { "…".to_string() } else { t };
        let rep = self.request(
            &format!("progress {} {}", place.word(), if cancel { "cancel" } else { "-" }),
            Some(t.as_bytes()),
        )?;
        Ok(rep.fields.first().and_then(|v| v.parse().ok()).unwrap_or(0))
    }

    /// Its share done (`None`: indeterminate) and a message.
    pub fn progress_set(&self, progress: u64, percent: Option<u8>, message: &str) -> Result<(), Error> {
        let pct = percent.map(|p| p.min(100).to_string()).unwrap_or_else(|| "-".into());
        let m: String = message.replace('\n', " ").chars().take(200).collect();
        self.request(&format!("progress-set {progress} {pct}"), Some(m.as_bytes())).map(|_| ())
    }

    /// Removes it.
    pub fn progress_end(&self, progress: u64) -> Result<(), Error> {
        self.request(&format!("progress-end {progress}"), None).map(|_| ())
    }

    // ---- reading ----

    #[cfg(not(target_os = "wasi"))]
    fn reader<R: BufRead>(&self, mut input: R, tx: Sender<Msg>) {
        loop {
            match self.read_msg(&mut input) {
                Msg::Delivered => {}
                Msg::End => break,
                msg => {
                    if tx.send(msg).is_err() {
                        break;
                    }
                }
            }
        }
        self.close();
        let _ = tx.send(Msg::End);
    }

    /// rhun is gone: every waiting request fails, later ones fail at once
    fn close(&self) {
        let mut p = self.pending.lock().unwrap();
        p.closed = true;
        for (_, (_, w)) in p.waiters.drain() {
            let _ = w.send(Err(closed_error()));
        }
        drop(p);
        testing::tests_gone(&self.procs);
        self.cancel_query(None);
    }

    /// Reads one message: a reply goes to its waiter (Delivered); End when rhun's input ends or
    /// breaks the framing.
    fn read_msg(&self, input: &mut dyn BufRead) -> Msg {
        let mut line = Vec::new();
        loop {
            line.clear();
            match input.read_until(b'\n', &mut line) {
                Ok(0) | Err(_) => return Msg::End,
                Ok(_) => {}
            }
            while matches!(line.last(), Some(b'\n') | Some(b'\r')) {
                line.pop();
            }
            let text = String::from_utf8_lossy(&line).into_owned();
            let fields: Vec<String> = text.split(' ').map(str::to_string).collect();
            if fields.is_empty() {
                continue;
            }
            match fields[0].as_str() {
                "ok" | "error" => {
                    let id: u64 = fields.get(1).and_then(|v| v.parse().ok()).unwrap_or(0);
                    let waiter = self.pending.lock().unwrap().waiters.remove(&id);
                    let verb = waiter.as_ref().map(|w| w.0.as_str()).unwrap_or("");
                    let mut body = Vec::new();
                    if fields[0] == "ok"
                        && matches!(
                            verb,
                            "text"
                                | "range"
                                | "ask"
                                | "snippet-context"
                                | "git-repo"
                                | "git-status"
                                | "git-blame"
                                | "git-log"
                                | "git-show"
                                | "secret-get"
                                | "secret-keys"
                                | "tools"
                                | "tool-call"
                                | "run-command"
                                | "scm-input"
                                | "external-uri"
                                | "link"
                        )
                    {
                        let len: usize = if verb == "snippet-context" {
                            match fields
                                .last()
                                .and_then(|s| snippet_context::decimal(s))
                                .and_then(|v| usize::try_from(v).ok())
                            {
                                Some(n) if n <= snippet::MAX_BODY => n,
                                _ => return Msg::End,
                            }
                        } else if verb == "run-command" {
                            match fields.last().and_then(|v| v.parse::<usize>().ok()) {
                                Some(n) if n <= commands::MAX_CALL_RESULT => n,
                                _ => return Msg::End,
                            }
                        } else {
                            fields.last().and_then(|v| v.parse().ok()).unwrap_or(0)
                        };
                        body = vec![0; len];
                        if input.read_exact(&mut body).is_err() {
                            return Msg::End;
                        }
                    }
                    let res = if fields[0] == "ok" {
                        Ok(Reply {
                            fields: fields[2..].to_vec(),
                            body,
                        })
                    } else {
                        Err(Error {
                            code: fields.get(2).cloned().unwrap_or_default(),
                            text: fields.get(3..).map(|f| f.join(" ")).unwrap_or_default(),
                        })
                    };
                    if let Some((_, w)) = waiter {
                        let _ = w.send(res);
                    }
                    return Msg::Delivered;
                }
                "0" => {
                    let name = fields.get(1).cloned().unwrap_or_default();
                    let rest = text.splitn(3, ' ').nth(2).unwrap_or("").to_string();
                    let mut ev = Event {
                        name,
                        fields: fields[2.min(fields.len())..].to_vec(),
                        rest,
                        body: Vec::new(),
                    };
                    if ev.name == "nb-run" {
                        // a cell's source: 4 MB at most
                        let len: usize = match ev.fields.last().and_then(|v| v.parse().ok()) {
                            Some(n) if n <= notebook::SOURCE_MAX => n,
                            _ => return Msg::End,
                        };
                        ev.body = vec![0; len];
                        if input.read_exact(&mut ev.body).is_err() {
                            return Msg::End;
                        }
                    }
                    // tests: a process's output and end go to its streams here, never
                    // through the handler (which may be waiting for them)
                    if ev.name == "test-out" && ev.fields.len() == 3 {
                        let len: usize = match ev.fields[2].parse() {
                            Ok(n) if n <= 4 << 20 => n,
                            _ => return Msg::End,
                        };
                        let mut body = vec![0; len];
                        if input.read_exact(&mut body).is_err() {
                            return Msg::End;
                        }
                        if let Ok(ex) = ev.fields[0].parse() {
                            testing::test_out(&self.procs, ex, &ev.fields[1], &body);
                        }
                        continue;
                    }
                    if ev.name == "test-exit" && ev.fields.len() >= 2 {
                        if let Ok(ex) = ev.fields[0].parse() {
                            testing::test_exit(&self.procs, ex, &ev.fields[1..].join(" "));
                        }
                        continue;
                    }
                    let view_body = ev.name == "view"
                        && ev.fields.get(1).is_some_and(|w| views::has_body(w));
                    // with command-args, run is NAME DOC FROM LEN and a body
                    let run_body = ev.name == "run" && ev.fields.len() == 4;
                    if ev.name == "edits"
                        || ev.name == "action"
                        || ev.name == "selections"
                        || view_body
                        || run_body
                        || ev.name == "link"
                    {
                        // rhun sends at most 4 MB here: a larger length is not rhun's, and the
                        // session ends rather than allocate it
                        let len: usize = match ev.fields.last().and_then(|v| v.parse().ok()) {
                            Some(n) if n <= 4 << 20 => n,
                            _ => return Msg::End,
                        };
                        ev.body = vec![0; len];
                        if input.read_exact(&mut ev.body).is_err() {
                            return Msg::End;
                        }
                    }
                    if ev.name == "features" {
                        *self.features.lock().unwrap() = ev.fields.clone();
                    }
                    if ev.name == "cancel" {
                        if let Some(id) = ev.fields.first().and_then(|v| v.parse().ok()) {
                            self.cancel_query(Some(id));
                        }
                    }
                    if ev.name == "bye" {
                        self.cancel_query(None);
                    }
                    return Msg::Event(ev);
                }
                id => {
                    // a request from rhun (section 12): ID VERB FIELDS
                    if let Ok(id) = id.parse::<u64>() {
                        let verb = fields.get(1).cloned().unwrap_or_default();
                        let rest = fields[2.min(fields.len())..].to_vec();
                        // a request followed by a body: its length is the last field (1 to 10
                        // digits); a longer or malformed one is not rhun's, and the session ends
                        let body_max: Option<usize> = match verb.as_str() {
                            // BRANCH_LEN DIFF_LEN LF + both: rhun sends at most 4 MB
                            "commit-message" => Some(4 << 20),
                            // vkids, vmore: the node id follows (at most 1000 bytes)
                            v if views::request_has_body(v) => Some(4096),
                            // tests: the parent item's id, the run's items
                            "tests-resolve" | "test-run" => Some(4 << 20),
                            // a tool call and a prompt (at most 4 MB)
                            "tool" | "agent-prompt" => Some(4 << 20),
                            // a notebook body: 64 MB at most, and its path
                            "nb-open" | "nb-save" => Some(notebook::BODY_MAX + 65536),
                            // command-args: a call's arguments (NAME CALLER FROM LEN)
                            "call" if rest.len() == 4 => Some(4 << 20),
                            // protocol 1.1 language queries (at most 64 KiB)
                            v if query::BODIED.contains(&v) && !rest.is_empty() => {
                                Some(query::MAX_REQUEST_BODY)
                            }
                            _ => None,
                        };
                        let mut body = Vec::new();
                        if let Some(max) = body_max {
                            let len: usize = match rest.last().and_then(|v| body_len(v)) {
                                Some(n) if n <= max => n,
                                _ => return Msg::End,
                            };
                            body = vec![0; len];
                            if input.read_exact(&mut body).is_err() {
                                return Msg::End;
                            }
                        }
                        self.begin_query(id);
                        return Msg::HostRequest {
                            id,
                            verb,
                            fields: rest,
                            body,
                        };
                    }
                }
            }
        }
    }

    /// The handler loop without threads: queued messages first, then rhun's input.
    #[cfg(target_os = "wasi")]
    fn dispatch_inline<H: Handler>(&self, h: &mut H) {
        let inline = self.inline.clone().expect("inline input");
        loop {
            let queued = inline.queue.lock().unwrap().pop_front();
            let msg = match queued {
                Some(m) => m,
                None => {
                    let mut input = inline.input.lock().unwrap();
                    self.read_msg(&mut **input)
                }
            };
            match msg {
                Msg::Delivered => {}
                Msg::End => {
                    self.close();
                    break;
                }
                msg => {
                    if !self.handle(msg, h) {
                        break;
                    }
                }
            }
        }
    }

    #[cfg(not(target_os = "wasi"))]
    fn dispatch<H: Handler>(&self, rx: Receiver<Msg>, h: &mut H) {
        while let Ok(msg) = rx.recv() {
            if !self.handle(msg, h) {
                break;
            }
        }
    }

    /// One event or request from rhun for the handler; false when the session ends.
    fn handle<H: Handler>(&self, msg: Msg, h: &mut H) -> bool {
        {
            match msg {
                Msg::End => return false,
                Msg::Delivered => {}
                Msg::Event(ev) => {
                    h.event(self, &ev);
                    match ev.name.as_str() {
                        "ready" => {
                            let p = ev.rest.as_str();
                            h.ready(
                                self,
                                if p == "-" || p.is_empty() {
                                    None
                                } else {
                                    Some(p)
                                },
                            );
                        }
                        "run" => {
                            let full = ev.fields.first().cloned().unwrap_or_default();
                            let (from, body) = if ev.fields.len() == 4 {
                                (ev.fields[2].as_str(), ev.body.clone())
                            } else {
                                ("", Vec::new())
                            };
                            h.invoke(self, &Invocation::new(&full, ev.num(1), from, body));
                        }
                        "setting" => {
                            let mut it = ev.rest.splitn(2, ' ');
                            let key = it.next().unwrap_or("").to_string();
                            let value = it.next().unwrap_or("").to_string();
                            h.setting(self, &key, &value);
                        }
                        "view" => {
                            if let Some(open) = views::parse_open(&ev) {
                                h.view_open(self, &open);
                            } else if let Some((view, rev)) = views::parse_retained(&ev) {
                                h.view_event(self, view, &rev);
                                if rev == views::RetainedEvent::Closed {
                                    h.view(self, view, ViewEvent::Closed);
                                }
                                if let views::RetainedEvent::Focus(on) = rev {
                                    h.view(self, view, ViewEvent::Focus(on)); // a picture's too
                                }
                            } else if let Some((view, vev)) = parse_view_event(&ev) {
                                h.view(self, view, vev);
                            }
                        }
                        "action" => h.action(self, &ev.body),
                        "content-closed" => h.content_closed(self, &ev.rest),
                        "test-cancel" if self.has(Feature::Tests) => {
                            if let Some(run) = testing::parse_test_cancel(&ev) {
                                h.cancel_tests(self, run);
                            }
                        }
                        "proposal" => {
                            if let Some(p) = ai::ProposalEvent::parse(&ev.fields) {
                                h.proposal(self, p);
                            }
                        }
                        "agent-stop" => {
                            if let Some(sid) = ev.fields.first() {
                                h.agent_stop(self, sid);
                            }
                        }
                        "selections" if self.has(Feature::Selections) => {
                            if let Some(s) = Selections::parse(&ev) {
                                h.selections(self, &s);
                            }
                        }
                        "nb-run" if self.has(Feature::Notebooks) => {
                            if let Some(r) = notebook::NbRun::parse(&ev.fields, &ev.body) {
                                h.nb_run(self, &r);
                            }
                        }
                        "cell" if self.has(Feature::Notebooks) && ev.fields.len() == 4 => {
                            h.nb_cell(self, ev.num(0), ev.num(1) as u64, ev.num(2) as u64, ev.num(3) as usize);
                        }
                        "cells" if self.has(Feature::Notebooks) && !ev.fields.is_empty() => {
                            let docs: Vec<Doc> = ev.fields[1..].iter().filter_map(|f| f.parse().ok()).collect();
                            h.nb_cells(self, ev.num(0) as u64, &docs);
                        }
                        "nb-exec-exit" if self.has(Feature::Notebooks) && ev.fields.len() == 2 => {
                            let code = ev.fields[1].parse::<i32>().ok();
                            h.nb_exec_exit(self, ev.num(0) as u64, code);
                        }
                        "nb-interrupt" | "nb-restart" | "nb-kernel" | "nb-closed"
                            if self.has(Feature::Notebooks) =>
                        {
                            let nb = ev.num(0) as u64;
                            match ev.name.as_str() {
                                "nb-interrupt" => h.nb_interrupt(self, nb),
                                "nb-restart" => h.nb_restart(self, nb),
                                "nb-kernel" => h.nb_kernel(self, nb),
                                _ => h.nb_closed(self, nb),
                            }
                        }
                        "notify" => {
                            if let Some((note, nev)) = notify::parse_note_event(&ev) {
                                h.note_event(self, note, nev);
                            }
                        }
                        "progress" => {
                            if let Some(p) = notify::parse_progress_cancel(&ev) {
                                h.progress_cancel(self, p);
                            }
                        }
                        "bye" => {
                            h.bye(self);
                            return false;
                        }
                        _ => {}
                    }
                }
                Msg::HostRequest {
                    id,
                    verb,
                    fields,
                    body: req_body,
                } => {
                    let out = self.stream(id);
                    // a call's error code is the callee's own
                    let mut call_code = String::new();
                    let body: Option<Result<Vec<u8>, (&str, String)>> = match verb.as_str() {
                        "complete" => CompleteRequest::parse(&fields)
                            .map(|r| Ok(h.complete_stream(self, &r, &out).encode_bytes())),
                        "complete-resolve" if self.has(Feature::CompleteItems) => {
                            ResolveRequest::parse(id, &fields, &req_body)
                                .map(|r| Ok(h.complete_resolve(self, &r).encode().into_bytes()))
                        }
                        "signature" => SignatureRequest::parse(&fields).map(|r| {
                            Ok(h.signature(self, &r)
                                .map(|s| s.encode())
                                .unwrap_or_default()
                                .into_bytes())
                        }),
                        "signatures" if self.has(Feature::SignatureList) => {
                            SignatureRequest::parse(&fields).map(|r| {
                                Ok(h.signatures(self, &r)
                                    .map(|l| l.encode())
                                    .unwrap_or_default()
                                    .into_bytes())
                            })
                        }
                        "definition" => PosRequest::parse(&fields).map(|r| {
                            Ok(lang::encode_locations(&h.definition_stream(self, &r, &out)))
                        }),
                        "references" => PosRequest::parse(&fields).map(|r| {
                            Ok(lang::encode_locations(&h.references_stream(self, &r, &out)))
                        }),
                        "declaration" if self.has(Feature::Navigation) => {
                            PosRequest::parse(&fields).map(|r| {
                                Ok(lang::encode_locations(
                                    &h.declaration_stream(self, &r, &out),
                                ))
                            })
                        }
                        "type-definition" if self.has(Feature::Navigation) => {
                            PosRequest::parse(&fields).map(|r| {
                                Ok(lang::encode_locations(
                                    &h.type_definition_stream(self, &r, &out),
                                ))
                            })
                        }
                        "implementation" if self.has(Feature::Navigation) => {
                            PosRequest::parse(&fields).map(|r| {
                                Ok(lang::encode_locations(
                                    &h.implementation_stream(self, &r, &out),
                                ))
                            })
                        }
                        "prepare-rename" if self.has(Feature::PrepareRename) => {
                            PosRequest::parse(&fields).map(|r| match h.prepare_rename(self, &r) {
                                Ok(p) => Ok(p.map(|p| p.encode()).unwrap_or_default()),
                                Err(why) => Err(("refused", why)),
                            })
                        }
                        "range-format" if self.has(Feature::PartialFormat) => {
                            RangeFormatRequest::parse(&fields).map(|r| {
                                // a format answer: 12g's grammar whatever was agreed
                                h.range_format(self, &r).encode_with(lang::EditCaps::V1)
                            })
                        }
                        "type-format" if self.has(Feature::PartialFormat) => {
                            TypeFormatRequest::parse(&fields).map(|r| {
                                // a format answer: 12g's grammar whatever was agreed
                                h.type_format(self, &r).encode_with(lang::EditCaps::V1)
                            })
                        }
                        "code-actions" if self.has(Feature::CodeActions) => {
                            CodeActionsRequest::parse(id, &fields).map(|r| {
                                let acts: Vec<Action> = h
                                    .code_actions(self, &r)
                                    .into_iter()
                                    .filter(|a| r.wants(&a.kind_word()))
                                    .collect();
                                Ok(encode_code_actions(&acts))
                            })
                        }
                        "code-action-resolve" if self.has(Feature::CodeActions) => {
                            ActionResolveRequest::parse(id, &fields, &req_body).map(|r| {
                                match h.code_action_resolve(self, &r) {
                                    Ok(res) => res.encode().map_err(|why| ("toobig", why)),
                                    Err(why) => Err(("refused", why)),
                                }
                            })
                        }
                        "hover-query" if self.has(Feature::HoverQuery) => {
                            HoverRequest::parse(id, &fields).map(|r| {
                                Ok(h.hover(self, &r).map(|h| h.encode()).unwrap_or_default())
                            })
                        }
                        "selection-ranges" if self.has(Feature::SelectionRanges) => {
                            SelectionRangesRequest::parse(id, &fields, &req_body).map(|r| {
                                Ok(langfeat::encode_selection_ranges(
                                    &r.positions,
                                    &h.selection_ranges(self, &r),
                                ))
                            })
                        }
                        "call-hierarchy" if self.has(Feature::Hierarchy) => {
                            PosRequest::parse(&fields).map(|r| {
                                Ok(langfeat::encode_hierarchy_roots(
                                    &h.call_hierarchy(self, &r),
                                ))
                            })
                        }
                        "type-hierarchy" if self.has(Feature::Hierarchy) => {
                            PosRequest::parse(&fields).map(|r| {
                                Ok(langfeat::encode_hierarchy_roots(
                                    &h.type_hierarchy(self, &r),
                                ))
                            })
                        }
                        "calls-in" if self.has(Feature::Hierarchy) => {
                            langfeat::item_data(&fields, &req_body)
                                .map(|d| Ok(langfeat::encode_calls(&h.calls_in(self, d, &out))))
                        }
                        "calls-out" if self.has(Feature::Hierarchy) => {
                            langfeat::item_data(&fields, &req_body)
                                .map(|d| Ok(langfeat::encode_calls(&h.calls_out(self, d, &out))))
                        }
                        "supertypes" if self.has(Feature::Hierarchy) => {
                            langfeat::item_data(&fields, &req_body).map(|d| {
                                Ok(langfeat::encode_hierarchy_items(
                                    &h.supertypes(self, d, &out),
                                ))
                            })
                        }
                        "subtypes" if self.has(Feature::Hierarchy) => {
                            langfeat::item_data(&fields, &req_body).map(|d| {
                                Ok(langfeat::encode_hierarchy_items(&h.subtypes(self, d, &out)))
                            })
                        }
                        "workspace-symbols" if self.has(Feature::WorkspaceSymbols) => {
                            WorkspaceSymbolsRequest::parse(id, &fields, &req_body).map(|r| {
                                Ok(langfeat::encode_workspace_symbols(
                                    &h.workspace_symbols(self, &r, &out),
                                ))
                            })
                        }
                        "document-links" if self.has(Feature::DocumentLinks) => {
                            DocRequest::parse(id, &fields).map(|r| {
                                Ok(langfeat::encode_document_links(&h.document_links(self, &r)))
                            })
                        }
                        "symbols" => PosRequest::parse(&fields)
                            .map(|r| Ok(lang::encode_symbols(&h.symbols(self, &r)))),
                        "actions" => ActionsRequest::parse(&fields).map(|r| {
                            Ok(lang::encode_actions_with(
                                &h.actions(self, &r),
                                self.edit_caps(),
                            ))
                        }),
                        "rename" => {
                            RenameRequest::parse(&fields).map(|r| match h.rename(self, &r) {
                                Ok(e) => e.encode_with(self.edit_caps()),
                                Err(why) => Err(("refused", why)),
                            })
                        }
                        // format and save actions keep 12g's grammar (one open document)
                        "format" => FormatRequest::parse(&fields)
                            .map(|r| h.format(self, &r).encode_with(lang::EditCaps::V1)),
                        "vcopy" if self.has(Feature::ViewKeys) => {
                            let view: View = fields.first().and_then(|v| v.parse().ok()).unwrap_or(0);
                            let cut = fields.get(1).map(String::as_str) == Some("cut");
                            Some(Ok(h.view_copy(self, view, cut).unwrap_or_default().into_bytes()))
                        }
                        "vkids" | "vmore" if self.has(Feature::ViewTree) => {
                            views::ChildrenRequest::parse(id, &verb, &fields, &req_body).map(|r| {
                                let res = if r.have.is_some() {
                                    h.view_more(self, &r)
                                } else {
                                    h.view_children(self, &r)
                                };
                                match res {
                                    Ok(p) => p.encode().map_err(|why| ("toobig", why)),
                                    Err(why) => Err(("refused", why)),
                                }
                            })
                        }
                        "content" if self.has(Feature::Uris) => Some(
                            h.content(self, &fields.join(" "))
                                .map_err(|why| ("nofile", why))
                                .and_then(|c| c.encode().map_err(|why| ("toobig", why))),
                        ),
                        "save-actions" if self.has(Feature::SaveActions) => {
                            SaveActionsRequest::parse(id, &fields).map(|r| {
                                match h.save_actions(self, &r) {
                                    Ok(e) => e.encode_with(lang::EditCaps::V1),
                                    Err(why) => Err(("refused", why)),
                                }
                            })
                        }
                        "nb-open" if self.has(Feature::Notebooks) => notebook::NbOpen::parse(id, &fields, &req_body)
                            .map(|r| match h.nb_open(self, &r) {
                                Ok(n) => Ok(n.encode()),
                                Err(why) => Err(("refused", why)),
                            }),
                        "nb-save" if self.has(Feature::Notebooks) => {
                            notebook::NbSave::parse(id, &fields, &req_body).map(|r| match r {
                                Ok(r) => h.nb_save(self, &r).map_err(|why| ("refused", why)),
                                Err(why) => Err(("syntax", why)),
                            })
                        }
                        "call" if self.has(Feature::CommandArgs) => {
                            match CallRequest::parse(id, &fields, req_body) {
                                Some(r) => match h.call(self, &r) {
                                    Ok(b) if b.len() > commands::MAX_CALL_RESULT => Some(Err((
                                        "toobig",
                                        "a call's result is at most 1048576 bytes".to_string(),
                                    ))),
                                    Ok(b) => Some(Ok(b)),
                                    Err(e) => {
                                        let code = if !e.code.is_empty()
                                            && e.code.len() <= 40
                                            && e.code.bytes().all(|b| b.is_ascii_lowercase())
                                        {
                                            e.code
                                        } else {
                                            "refused".to_string()
                                        };
                                        call_code = code;
                                        Some(Err(("", e.text)))
                                    }
                                },
                                None => None,
                            }
                        }
                        "quickdiff" if self.has(Feature::QuickDiff) => {
                            QuickDiffRequest::parse(id, &fields)
                                .map(|r| Ok(h.quickdiff(self, &r).unwrap_or_default()))
                        }
                        "commit-message" if self.has(Feature::CommitMessage) => {
                            CommitMessageRequest::parse(id, &req_body).map(|r| {
                                match h.commit_message(self, &r) {
                                    Ok(m) => match scm::check_commit_message(&m) {
                                        Ok(m) => Ok(m.as_bytes().to_vec()),
                                        Err(why) => Err(("refused", why)),
                                    },
                                    Err(why) => Err(("refused", why)),
                                }
                            })
                        }
                        "tests-resolve" if self.has(Feature::Tests) && fields.len() == 1 => {
                            let parent = String::from_utf8_lossy(&req_body).into_owned();
                            Some(match h.resolve_tests(self, &parent) {
                                Ok(items) => testing::encode_test_items(&parent, &items)
                                    .map_err(|e| (code_str(&e.code), e.text)),
                                Err(e) => Err((code_str(&e.code), e.text)),
                            })
                        }
                        "test-run" if self.has(Feature::Tests) => {
                            match testing::parse_test_run(&fields, &req_body) {
                                None => Some(Err(("syntax", "bad test-run".to_string()))),
                                Some(run) => match h.run_tests(self, &run) {
                                    // `ok ID 0`: the empty body
                                    Ok(()) => Some(Ok(Vec::new())),
                                    Err(e) => Some(Err((code_str(&e.code), e.text))),
                                },
                            }
                        }
                        "agent-transcript" if self.has(Feature::Agents) => {
                            match (fields.first(), fields.get(1).and_then(|f| f.parse().ok())) {
                                (Some(sid), Some(from)) => Some(
                                    h.agent_transcript(self, sid, from)
                                        .map(|t| t.encode())
                                        .map_err(|r| (refusal_code(&r.code), r.text)),
                                ),
                                _ => None,
                            }
                        }
                        "tool" if self.has(Feature::Tools) && fields.len() == 3 => {
                            let call = ai::ToolCall {
                                name: fields[0].clone(),
                                caller: fields[1].clone(),
                                input: req_body,
                            };
                            Some(
                                h.tool(self, &call)
                                    .map_err(|r| (refusal_code(&r.code), r.text)),
                            )
                        }
                        "agent-prompt" if self.has(Feature::AgentPrompts) && fields.len() == 2 => {
                            let sid = fields[0].as_str();
                            let text = String::from_utf8_lossy(&req_body).into_owned();
                            Some(
                                h.agent_prompt(self, if sid == "-" { None } else { Some(sid) }, &text)
                                    .map(|s| s.into_bytes())
                                    .map_err(|r| (refusal_code(&r.code), r.text)),
                            )
                        }
                        "linked" if self.has(Feature::LinkedEditing) => {
                            LinkedRequest::parse(&fields).map(|r| Ok(h.linked(self, &r).encode()))
                        }
                        _ => None,
                    };
                    let _ = match body {
                        Some(Ok(b)) => self.write(&format!("ok {id} {}", b.len()), &b),
                        Some(Err((code, why))) => {
                            let code = if code.is_empty() {
                                call_code.as_str()
                            } else {
                                code
                            };
                            // one line, at most 1000 bytes: rhun shows a sentence
                            let mut why: String = why
                                .trim()
                                .chars()
                                .map(|c| if c == '\n' || c == '\r' { ' ' } else { c })
                                .collect();
                            if why.len() > 1000 {
                                let mut cut = 1000;
                                while !why.is_char_boundary(cut) {
                                    cut -= 1;
                                }
                                why.truncate(cut);
                            }
                            let line = if why.is_empty() {
                                format!("error {id} {code}")
                            } else {
                                format!("error {id} {code} {why}")
                            };
                            self.write(&line, &[])
                        }
                        None => self.write(&format!("error {id} unknown no such request"), &[]),
                    };
                    self.finish_query(id);
                }
            }
        }
        true
    }
}

/// An error code from a handler as one of the codes of the spec (others become `refused`).
fn code_str(code: &str) -> &'static str {
    match code {
        "unknown" => "unknown",
        "syntax" => "syntax",
        "busy" => "busy",
        "toobig" => "toobig",
        "badpos" => "badpos",
        "stale" => "stale",
        "cancelled" => "cancelled",
        "nofile" => "nofile",
        "nodoc" => "nodoc",
        "timeout" => "timeout",
        _ => "refused",
    }
}

/// An error code of the protocol for a refusal (anything else travels as refused).
fn refusal_code(code: &str) -> &'static str {
    match code {
        "syntax" => "syntax",
        "unknown" => "unknown",
        "needs" => "needs",
        "nodoc" => "nodoc",
        "nofile" => "nofile",
        "readonly" => "readonly",
        "stale" => "stale",
        "badpos" => "badpos",
        "toobig" => "toobig",
        "busy" => "busy",
        "cancelled" => "cancelled",
        _ => "refused",
    }
}

/// A body's length as rhun writes it: a plain decimal of 1 to 10 digits.
fn body_len(s: &str) -> Option<usize> {
    if s.is_empty() || s.len() > 10 || !s.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    s.parse().ok()
}

fn parse_view_event(ev: &Event) -> Option<(View, ViewEvent)> {
    let view: View = ev.fields.first()?.parse().ok()?;
    let kind = ev.fields.get(1)?.as_str();
    let f = |i: usize| {
        ev.fields
            .get(i)
            .and_then(|v| v.parse::<f32>().ok())
            .unwrap_or(0.0)
    };
    let vev = match kind {
        "size" => ViewEvent::Size {
            width: f(2),
            height: f(3),
        },
        "metrics" => ViewEvent::Metrics(Metrics::parse(&ev.fields[2..])?),
        "hover" => ViewEvent::Hover { x: f(2), y: f(3) },
        "down" => ViewEvent::Down { x: f(2), y: f(3) },
        "drag" => ViewEvent::Drag { x: f(2), y: f(3) },
        "up" => ViewEvent::Up { x: f(2), y: f(3) },
        "click" => ViewEvent::Click { x: f(2), y: f(3) },
        "leave" => ViewEvent::Leave,
        "wheel" => ViewEvent::Wheel {
            x: f(2),
            y: f(3),
            dy: f(4),
        },
        "closed" => ViewEvent::Closed,
        "key" => ViewEvent::Key {
            mods: ev.fields.get(2)?.clone(),
            name: ev.fields.get(3)?.clone(),
        },
        "text" => ViewEvent::Text(String::from_utf8_lossy(&ev.body).into_owned()),
        "paste" => ViewEvent::Paste(String::from_utf8_lossy(&ev.body).into_owned()),
        "selectall" => ViewEvent::SelectAll,
        "dblclick" => ViewEvent::DoubleClick { x: f(2), y: f(3) },
        _ => return None,
    };
    Some((view, vev))
}

/// The extension's own settings (section 6d), kept as they arrive: call [`Settings::update`]
/// from [`Handler::setting`].
#[derive(Clone, Debug, Default)]
pub struct Settings {
    values: HashMap<String, String>,
}

impl Settings {
    pub fn update(&mut self, key: &str, value: &str) {
        self.values.insert(key.to_string(), value.to_string());
    }

    pub fn string(&self, key: &str, default: &str) -> String {
        self.values
            .get(key)
            .cloned()
            .unwrap_or_else(|| default.to_string())
    }

    pub fn bool(&self, key: &str, default: bool) -> bool {
        match self.values.get(key).map(String::as_str) {
            Some("yes") => true,
            Some("no") => false,
            _ => default,
        }
    }

    pub fn int(&self, key: &str, default: i64) -> i64 {
        self.values
            .get(key)
            .and_then(|v| v.parse().ok())
            .unwrap_or(default)
    }
}
