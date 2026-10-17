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
//! [`Feature::Complete`] and [`Feature::Signature`] for language support.

pub mod chart;
pub mod complete;
pub mod picture;

use std::collections::HashMap;
use std::fmt;
use std::io::{self, BufRead, BufReader, Read, Write};
use std::sync::mpsc::{channel, Receiver, Sender};
use std::sync::{Arc, Mutex};
use std::thread;

pub use complete::{CompleteRequest, CompletionItem, CompletionList, Kind, SignatureHelp, SignatureRequest};
pub use picture::{Color, Picture};

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
}

impl Feature {
    pub fn name(self) -> &'static str {
        match self {
            Feature::Views => "views",
            Feature::Complete => "complete",
            Feature::Signature => "signature",
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
    /// The body, for `edits`.
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
        Some(Opened { doc, version, lang, path })
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
        let nl = body[pos..].iter().position(|&b| b == b'\n').ok_or("an edit header without LF")? + pos;
        let head = std::str::from_utf8(&body[pos..nl]).map_err(|_| "a header that is not UTF-8")?;
        let f: Vec<&str> = head.split(' ').collect();
        if f.len() != 3 {
            return Err(format!("an edit header of {} fields", f.len()));
        }
        let num = |s: &str| s.parse::<usize>().map_err(|_| format!("not a number: {s}"));
        let (start, del, len) = (num(f[0])?, num(f[1])?, num(f[2])?);
        if nl + 1 + len > body.len() {
            return Err("an edit longer than the body".into());
        }
        out.push(Edit { start, del, ins: body[nl + 1..nl + 1 + len].to_vec() });
        pos = nl + 1 + len;
    }
    Ok(out)
}

/// Applies edit records to the text they were made against.
pub fn apply_edits(text: &mut Vec<u8>, edits: &[Edit]) -> Result<(), String> {
    for e in edits {
        if e.start + e.del > text.len() {
            return Err("an edit outside the text".into());
        }
        text.splice(e.start..e.start + e.del, e.ins.iter().copied());
    }
    Ok(())
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
    Size { width: f32, height: f32 },
    /// Text sizes: character advance and line height, logical pixels.
    Metrics(Metrics),
    Hover { x: f32, y: f32 },
    Down { x: f32, y: f32 },
    Drag { x: f32, y: f32 },
    Up { x: f32, y: f32 },
    Click { x: f32, y: f32 },
    Leave,
    Wheel { x: f32, y: f32, dy: f32 },
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
            small: TextMetric { advance: 6.0, line_height: 16.0 },
            normal: TextMetric { advance: 7.0, line_height: 18.0 },
            large: TextMetric { advance: 10.0, line_height: 26.0 },
            mono: TextMetric { advance: 7.0, line_height: 19.0 },
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
            let tm = TextMetric { advance: fields[i + 1].parse().ok()?, line_height: fields[i + 2].parse().ok()? };
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
    /// rhun asks for a completion list (feature `complete`).
    fn complete(&mut self, _ext: &Ext, _req: &CompleteRequest) -> CompletionList {
        CompletionList::default()
    }
    /// rhun asks for signature help (feature `signature`); `None` hides it.
    fn signature(&mut self, _ext: &Ext, _req: &SignatureRequest) -> Option<SignatureHelp> {
        None
    }
    /// rhun is about to stop the extension.
    fn bye(&mut self, _ext: &Ext) {}
}

enum Msg {
    Event(Event),
    HostRequest { id: u64, verb: String, fields: Vec<String> },
    End,
}

type Waiter = Sender<Result<Reply, Error>>;

struct Shared {
    out: Box<dyn Write + Send>,
    next_id: u64,
}

/// The connection to rhun. Cloning it is cheap; requests may be made from any thread.
#[derive(Clone)]
pub struct Ext {
    shared: Arc<Mutex<Shared>>,
    pending: Arc<Mutex<HashMap<u64, (String, Waiter)>>>,
    features: Arc<Mutex<Vec<String>>>,
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
    let ext = Ext {
        shared: Arc::new(Mutex::new(Shared { out: Box::new(output), next_id: 1 })),
        pending: Arc::new(Mutex::new(HashMap::new())),
        features: Arc::new(Mutex::new(Vec::new())),
    };
    let mut hello = String::from("hello 1");
    for f in features {
        hello.push(' ');
        hello.push_str(f.name());
    }
    ext.write(&hello, &[])?;
    let (tx, rx) = channel();
    {
        let ext = ext.clone();
        thread::spawn(move || ext.reader(BufReader::new(input), tx));
    }
    ext.dispatch(rx, &mut handler);
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
        let id = {
            let mut s = self.shared.lock().unwrap();
            let id = s.next_id;
            s.next_id += 1;
            id
        };
        let verb = verb_and_args.split(' ').next().unwrap_or("").to_string();
        self.pending.lock().unwrap().insert(id, (verb, tx));
        let line = match body {
            Some(b) => format!("{id} {verb_and_args} {}", b.len()),
            None => format!("{id} {verb_and_args}"),
        };
        if let Err(e) = self.write(&line, body.unwrap_or(&[])) {
            self.pending.lock().unwrap().remove(&id);
            return Err(Error { code: "io".into(), text: e.to_string() });
        }
        rx.recv().unwrap_or_else(|_| Err(Error { code: "closed".into(), text: "rhun went away".into() }))
    }

    // ---- documents ----

    /// The document's text and its version.
    pub fn text(&self, doc: Doc) -> Result<(Vec<u8>, i64), Error> {
        let r = self.request(&format!("text {doc}"), None)?;
        Ok((r.body, r.fields.first().and_then(|v| v.parse().ok()).unwrap_or(0)))
    }

    /// A range's text and the document's version.
    pub fn range_text(&self, doc: Doc, r: Range) -> Result<(Vec<u8>, i64), Error> {
        let rep = self.request(
            &format!("range {doc} {} {} {} {}", r.start.line, r.start.col, r.end.line, r.end.col),
            None,
        )?;
        Ok((rep.body, rep.fields.first().and_then(|v| v.parse().ok()).unwrap_or(0)))
    }

    /// Replaces a range at a version (one undo step); the new version.
    pub fn replace(&self, doc: Doc, version: i64, r: Range, text: &str) -> Result<i64, Error> {
        let rep = self.request(
            &format!("replace {doc} {version} {} {} {} {}", r.start.line, r.start.col, r.end.line, r.end.col),
            Some(text.as_bytes()),
        )?;
        Ok(rep.fields.first().and_then(|v| v.parse().ok()).unwrap_or(0))
    }

    /// Inserts at the cursor at a version; the new version.
    pub fn insert(&self, doc: Doc, version: i64, text: &str) -> Result<i64, Error> {
        let rep = self.request(&format!("insert {doc} {version}"), Some(text.as_bytes()))?;
        Ok(rep.fields.first().and_then(|v| v.parse().ok()).unwrap_or(0))
    }

    /// Selects a range (the cursor at its end).
    pub fn select(&self, doc: Doc, r: Range) -> Result<(), Error> {
        self.request(&format!("select {doc} {} {} {} {}", r.start.line, r.start.col, r.end.line, r.end.col), None)
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
            let msg: String = d.message.chars().map(|c| if c == '\n' { ' ' } else { c }).collect();
            let src = if d.source.is_empty() { "-".to_string() } else { d.source.replace(' ', "_") };
            body.push_str(&format!(
                "{} {} {} {} {} {} {}\n",
                d.range.start.line, d.range.start.col, d.range.end.line, d.range.end.col,
                d.severity.word(), src, msg
            ));
        }
        self.request(&format!("diagnostics {doc} {version} {}", marks.len()), Some(body.as_bytes())).map(|_| ())
    }

    /// The text to show for the position a `hover` event named.
    pub fn hover(&self, doc: Doc, at: Pos, text: &str) -> Result<(), Error> {
        self.request(&format!("hover {doc} {} {}", at.line, at.col), Some(text.as_bytes())).map(|_| ())
    }

    /// A per-document option (`tab_width`, `insert_spaces`, …), or `-` to drop it.
    pub fn option(&self, doc: Doc, key: &str, value: &str) -> Result<(), Error> {
        self.request(&format!("option {doc} {key} {value}"), None).map(|_| ())
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
        self.request(&format!("register {name} {keys} {title}"), None).map(|_| ())
    }

    /// A pick list; the index of the item chosen, `None` when cancelled.
    pub fn choose(&self, prompt: &str, items: &[&str]) -> Result<Option<usize>, Error> {
        let mut body = prompt.to_string();
        for i in items {
            body.push('\n');
            body.push_str(i);
        }
        match self.request("choose", Some(body.as_bytes())) {
            Ok(r) => Ok(r.fields.first().and_then(|v| v.parse::<usize>().ok()).map(|n| n.saturating_sub(1))),
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
        self.request(&format!("draw {view}"), Some(picture.body().as_bytes())).map(|_| ())
    }

    /// Closes a view's tab.
    pub fn unview(&self, view: View) -> Result<(), Error> {
        self.request(&format!("unview {view}"), None).map(|_| ())
    }

    // ---- reading ----

    fn reader<R: BufRead>(&self, mut input: R, tx: Sender<Msg>) {
        let mut line = Vec::new();
        loop {
            line.clear();
            match input.read_until(b'\n', &mut line) {
                Ok(0) | Err(_) => break,
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
                    let waiter = self.pending.lock().unwrap().remove(&id);
                    let verb = waiter.as_ref().map(|w| w.0.as_str()).unwrap_or("");
                    let mut body = Vec::new();
                    if fields[0] == "ok" && matches!(verb, "text" | "range" | "ask") {
                        let len: usize = fields.last().and_then(|v| v.parse().ok()).unwrap_or(0);
                        body = vec![0; len];
                        if input.read_exact(&mut body).is_err() {
                            break;
                        }
                    }
                    let res = if fields[0] == "ok" {
                        Ok(Reply { fields: fields[2..].to_vec(), body })
                    } else {
                        Err(Error {
                            code: fields.get(2).cloned().unwrap_or_default(),
                            text: fields.get(3..).map(|f| f.join(" ")).unwrap_or_default(),
                        })
                    };
                    if let Some((_, w)) = waiter {
                        let _ = w.send(res);
                    }
                }
                "0" => {
                    let name = fields.get(1).cloned().unwrap_or_default();
                    let rest = text.splitn(3, ' ').nth(2).unwrap_or("").to_string();
                    let mut ev = Event { name, fields: fields[2.min(fields.len())..].to_vec(), rest, body: Vec::new() };
                    if ev.name == "edits" {
                        let len: usize = ev.fields.last().and_then(|v| v.parse().ok()).unwrap_or(0);
                        ev.body = vec![0; len];
                        if input.read_exact(&mut ev.body).is_err() {
                            break;
                        }
                    }
                    if ev.name == "features" {
                        *self.features.lock().unwrap() = ev.fields.clone();
                    }
                    if tx.send(Msg::Event(ev)).is_err() {
                        break;
                    }
                }
                id => {
                    // a request from rhun (section 12): ID VERB FIELDS
                    if let Ok(id) = id.parse::<u64>() {
                        let verb = fields.get(1).cloned().unwrap_or_default();
                        let rest = fields[2.min(fields.len())..].to_vec();
                        if tx.send(Msg::HostRequest { id, verb, fields: rest }).is_err() {
                            break;
                        }
                    }
                }
            }
        }
        // rhun is gone: every waiting request fails, the handler loop ends
        for (_, (_, w)) in self.pending.lock().unwrap().drain() {
            let _ = w.send(Err(Error { code: "closed".into(), text: "rhun went away".into() }));
        }
        let _ = tx.send(Msg::End);
    }

    fn dispatch<H: Handler>(&self, rx: Receiver<Msg>, h: &mut H) {
        while let Ok(msg) = rx.recv() {
            match msg {
                Msg::End => break,
                Msg::Event(ev) => {
                    h.event(self, &ev);
                    match ev.name.as_str() {
                        "ready" => {
                            let p = ev.rest.as_str();
                            h.ready(self, if p == "-" || p.is_empty() { None } else { Some(p) });
                        }
                        "run" => {
                            let full = ev.fields.first().cloned().unwrap_or_default();
                            let short = full.split_once('.').map(|(_, c)| c).unwrap_or(&full).to_string();
                            h.command(self, &short, ev.num(1));
                        }
                        "setting" => {
                            let mut it = ev.rest.splitn(2, ' ');
                            let key = it.next().unwrap_or("").to_string();
                            let value = it.next().unwrap_or("").to_string();
                            h.setting(self, &key, &value);
                        }
                        "view" => {
                            if let Some((view, vev)) = parse_view_event(&ev) {
                                h.view(self, view, vev);
                            }
                        }
                        "bye" => {
                            h.bye(self);
                            break;
                        }
                        _ => {}
                    }
                }
                Msg::HostRequest { id, verb, fields } => {
                    let body = match verb.as_str() {
                        "complete" => CompleteRequest::parse(&fields).map(|r| h.complete(self, &r).encode()),
                        "signature" => SignatureRequest::parse(&fields)
                            .map(|r| h.signature(self, &r).map(|s| s.encode()).unwrap_or_default()),
                        _ => None,
                    };
                    let _ = match body {
                        Some(b) => self.write(&format!("ok {id} {}", b.len()), b.as_bytes()),
                        None => self.write(&format!("error {id} unknown no such request"), &[]),
                    };
                }
            }
        }
    }
}

fn parse_view_event(ev: &Event) -> Option<(View, ViewEvent)> {
    let view: View = ev.fields.first()?.parse().ok()?;
    let kind = ev.fields.get(1)?.as_str();
    let f = |i: usize| ev.fields.get(i).and_then(|v| v.parse::<f32>().ok()).unwrap_or(0.0);
    let vev = match kind {
        "size" => ViewEvent::Size { width: f(2), height: f(3) },
        "metrics" => ViewEvent::Metrics(Metrics::parse(&ev.fields[2..])?),
        "hover" => ViewEvent::Hover { x: f(2), y: f(3) },
        "down" => ViewEvent::Down { x: f(2), y: f(3) },
        "drag" => ViewEvent::Drag { x: f(2), y: f(3) },
        "up" => ViewEvent::Up { x: f(2), y: f(3) },
        "click" => ViewEvent::Click { x: f(2), y: f(3) },
        "leave" => ViewEvent::Leave,
        "wheel" => ViewEvent::Wheel { x: f(2), y: f(3), dy: f(4) },
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
        self.values.get(key).cloned().unwrap_or_else(|| default.to_string())
    }

    pub fn bool(&self, key: &str, default: bool) -> bool {
        match self.values.get(key).map(String::as_str) {
            Some("yes") => true,
            Some("no") => false,
            _ => default,
        }
    }

    pub fn int(&self, key: &str, default: i64) -> i64 {
        self.values.get(key).and_then(|v| v.parse().ok()).unwrap_or(default)
    }
}
