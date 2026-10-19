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
//! language support (the [`Handler`] methods of the same names answer them).
//!
//! Built for `wasm32-wasip1` (rhun runs such extensions itself, in any system: see
//! `run.wasm32-wasip1` in the spec), the crate needs no threads: a request reads rhun's input
//! itself until its reply comes, and keeps the events it reads meanwhile for later, so the
//! behaviour is the same.

#![allow(clippy::needless_doctest_main)]

pub mod chart;
pub mod complete;
pub mod lang;
pub mod picture;
pub mod scm;
pub mod snippet;
pub mod snippet_context;
pub mod snippet_transform;
pub mod views;
pub mod watch;
pub mod resources;

use std::collections::{HashMap, VecDeque};
use std::fmt;
use std::io::{self, BufRead, BufReader, Read, Write};
use std::sync::mpsc::{channel, Receiver, Sender};
use std::sync::{Arc, Mutex};
#[cfg(not(target_os = "wasi"))]
use std::thread;

pub use complete::{
    CompleteRequest, CompletionItem, CompletionList, Kind, SignatureHelp, SignatureRequest,
};
pub use lang::{
    ActionsRequest, CodeAction, FileEdit, FormatRequest, Location, PosRequest, RenameRequest,
    SaveActionsRequest, Symbol, SymbolKind, Target, TextEdit, WorkspaceEdit,
};
pub use picture::{Color, Picture};
pub use scm::{
    Blame, BlameCommit, BlameRun, CommitMessageRequest, GitHead, GitRepo, GitState, GitStatusEntry,
    LogEntry, LogOf, QuickDiffRequest,
};
pub use snippet::{Field, PreparedSnippet, SnippetChoice, SnippetProgram};
pub use snippet_context::SnippetSnapshot;
pub use snippet_transform::{
    CompiledSnippetTransform, SnippetFieldTransform, SnippetTransformProgramError,
};
pub use watch::{parse_workspace_watch, WorkspaceWatch, WorkspaceWatchEvent};

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
            Feature::GitData => "git-data",
            Feature::CommitMessage => "commit-message",
            Feature::QuickDiff => "quickdiff",
            Feature::ViewTree => "view-tree",
            Feature::Idle => "idle",
            Feature::Secrets => "secrets",
            Feature::Uris => "uris",
        }
    }
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
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Severity {
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

/// A mark: a range, a severity and a one-line message.
#[derive(Clone, Debug)]
pub struct Diagnostic {
    pub range: Range,
    pub severity: Severity,
    pub source: String,
    pub message: String,
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
    /// One of its own settings (section 6d): at start and on every change.
    fn setting(&mut self, _ext: &Ext, _key: &str, _value: &str) {}
    /// Something happened in one of its views.
    fn view(&mut self, _ext: &Ext, _view: View, _ev: ViewEvent) {}
    /// A declared view's instance exists (feature `view-tree`): fill it with [`Ext::patch`].
    fn view_open(&mut self, _ext: &Ext, _open: &views::ViewOpen) {}
    /// The user did something in a retained view (feature `view-tree`).
    fn view_event(&mut self, _ext: &Ext, _view: View, _ev: &views::RetainedEvent) {}
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
    /// rhun is about to stop the extension.
    fn bye(&mut self, _ext: &Ext) {}
    /// rhun asks for a virtual document's text (feature `uris`): a URI of one of the schemes in
    /// `[provides] schemes`. An error shows in the document's tab.
    fn content(&mut self, _ext: &Ext, _uri: &str) -> Result<resources::Content, String> {
        Err("no such document".into())
    }
    /// The last tab showing a virtual document of this extension closed.
    fn content_closed(&mut self, _ext: &Ext, _uri: &str) {}
}

enum Msg {
    Event(Event),
    HostRequest {
        id: u64,
        verb: String,
        fields: Vec<String>,
        /// The body of a request that has one (`commit-message`, `vkids`, `vmore`).
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
    /// `workspace/applyEdit`.
    pub fn apply(&self, edit: &WorkspaceEdit) -> Result<(), Error> {
        let body = edit.encode().map_err(|text| Error {
            code: "toobig".into(),
            text,
        })?;
        self.request("apply", Some(&body)).map(|_| ())
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
        let body = edit.encode().map_err(|text| Error {
            code: "toobig".into(),
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
                    let view_body = ev.name == "view"
                        && ev.fields.get(1).is_some_and(|w| views::has_body(w));
                    if ev.name == "edits" || ev.name == "action" || view_body {
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
                    return Msg::Event(ev);
                }
                id => {
                    // a request from rhun (section 12): ID VERB FIELDS
                    if let Ok(id) = id.parse::<u64>() {
                        let verb = fields.get(1).cloned().unwrap_or_default();
                        let rest = fields[2.min(fields.len())..].to_vec();
                        // a request followed by a body: its length is the last field
                        let body_max: Option<usize> = match verb.as_str() {
                            // BRANCH_LEN DIFF_LEN LF + both: rhun sends at most 4 MB
                            "commit-message" => Some(4 << 20),
                            // vkids, vmore: the node id follows (at most 1000 bytes)
                            v if views::request_has_body(v) => Some(4096),
                            _ => None,
                        };
                        let mut body = Vec::new();
                        if let Some(max) = body_max {
                            let len: usize = match rest.last().and_then(|v| v.parse().ok()) {
                                Some(n) if n <= max => n,
                                _ => return Msg::End,
                            };
                            body = vec![0; len];
                            if input.read_exact(&mut body).is_err() {
                                return Msg::End;
                            }
                        }
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
                            let short = full
                                .split_once('.')
                                .map(|(_, c)| c)
                                .unwrap_or(&full)
                                .to_string();
                            h.command(self, &short, ev.num(1));
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
                            } else if let Some((view, vev)) = parse_view_event(&ev) {
                                h.view(self, view, vev);
                            }
                        }
                        "action" => h.action(self, &ev.body),
                        "content-closed" => h.content_closed(self, &ev.rest),
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
                    let body: Option<Result<Vec<u8>, (&str, String)>> = match verb.as_str() {
                        "complete" => CompleteRequest::parse(&fields)
                            .map(|r| Ok(h.complete(self, &r).encode().into_bytes())),
                        "signature" => SignatureRequest::parse(&fields).map(|r| {
                            Ok(h.signature(self, &r)
                                .map(|s| s.encode())
                                .unwrap_or_default()
                                .into_bytes())
                        }),
                        "definition" => PosRequest::parse(&fields)
                            .map(|r| Ok(lang::encode_locations(&h.definition(self, &r)))),
                        "references" => PosRequest::parse(&fields)
                            .map(|r| Ok(lang::encode_locations(&h.references(self, &r)))),
                        "symbols" => PosRequest::parse(&fields)
                            .map(|r| Ok(lang::encode_symbols(&h.symbols(self, &r)))),
                        "actions" => ActionsRequest::parse(&fields)
                            .map(|r| Ok(lang::encode_actions(&h.actions(self, &r)))),
                        "rename" => {
                            RenameRequest::parse(&fields).map(|r| match h.rename(self, &r) {
                                Ok(e) => e.encode().map_err(|why| ("toobig", why)),
                                Err(why) => Err(("refused", why)),
                            })
                        }
                        "format" => FormatRequest::parse(&fields)
                            .map(|r| h.format(self, &r).encode().map_err(|why| ("toobig", why))),
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
                                    Ok(e) => e.encode().map_err(|why| ("toobig", why)),
                                    Err(why) => Err(("refused", why)),
                                }
                            })
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
                        _ => None,
                    };
                    let _ = match body {
                        Some(Ok(b)) => self.write(&format!("ok {id} {}", b.len()), &b),
                        Some(Err((code, why))) => {
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
                }
            }
        }
        true
    }
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
