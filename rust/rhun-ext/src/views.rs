//! Retained views (protocol 1.1, feature `view-tree`): views whose content rhun keeps. Declare
//! them in the manifest's `[views]` (`KEY.title`, `KEY.place` = `sidebar`|`panel`|`side`,
//! `KEY.kind` = `tree`, `KEY.open` = `yes`); after `ready` rhun opens one instance per declared
//! view ([`crate::Handler::view_open`]). Describe the content with a [`Patch`] and send it with
//! [`crate::Ext::patch`]: rhun applies it whole or not at all, keeps the user's expansion,
//! selection and scroll by node id, and reports what the user does
//! ([`crate::Handler::view_event`]).
//!
//! ```
//! use rhun_ext::views::{Icon, Kids, Node, Patch, State, ViewColor};
//!
//! let mut p = Patch::new();
//! p.title("TODO").kids(
//!     b"",
//!     &[Node::new("f:main.rs", "main.rs").desc("src").icon(Icon::file("main.rs"), None).kids(Kids::Open)],
//!     false,
//! );
//! p.kids(
//!     b"f:main.rs",
//!     &[Node::new("t:1", "TODO: parse").highlight(0..4)
//!         .icon(Icon::named("warning"), Some(ViewColor::Warning))
//!         .open("/home/me/app/src/main.rs", 2, 8)],
//!     false,
//! );
//! p.state(State::Ready);
//! assert!(p.encode().unwrap().starts_with(b"4\ntitle 4\nTODO"));
//! ```

use std::ops::Range;

use crate::{Event, View};

/// Whether a node has children, and whether it starts expanded.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Kids {
    Leaf,
    /// Collapsible, collapsed when it first appears; children not given are asked for later.
    Closed,
    /// Collapsible, expanded when it first appears.
    Open,
}

impl Kids {
    fn word(self) -> &'static str {
        match self {
            Kids::Leaf => "leaf",
            Kids::Closed => "closed",
            Kids::Open => "open",
        }
    }
}

/// A color by theme name.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ViewColor {
    Fg,
    Muted,
    Accent,
    Error,
    Warning,
    Success,
    Add,
    Mod,
    Del,
    /// A chart series color, 1 to 8.
    Series(u8),
}

impl ViewColor {
    pub fn word(self) -> String {
        match self {
            ViewColor::Fg => "fg".into(),
            ViewColor::Muted => "muted".into(),
            ViewColor::Accent => "accent".into(),
            ViewColor::Error => "error".into(),
            ViewColor::Warning => "warning".into(),
            ViewColor::Success => "success".into(),
            ViewColor::Add => "add".into(),
            ViewColor::Mod => "mod".into(),
            ViewColor::Del => "del".into(),
            ViewColor::Series(n) => format!("c{}", (n.clamp(1, 8))),
        }
    }
}

/// A node's icon: a built-in name, the Explorer's icon for a file name, or one of the
/// extension's `[icons]`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Icon(String);

impl Icon {
    /// A built-in icon: `file folder folder-open search check close plus minus dot refresh
    /// branch pin puzzle more terminal spark user arrow-up arrow-down discard gear` (others are
    /// shown as nothing by hosts that do not know them).
    pub fn named(name: &str) -> Icon {
        Icon(name.to_string())
    }
    /// The Explorer's icon and color for a file of that name.
    pub fn file(name: &str) -> Icon {
        Icon(format!("file:{name}"))
    }
    /// The extension's own icon `KEY` from its manifest's `[icons]`.
    pub fn ext(key: &str) -> Icon {
        Icon(format!("ext:{key}"))
    }
}

fn record(out: &mut Vec<u8>, head: &str, data: &[u8]) {
    out.extend_from_slice(head.as_bytes());
    out.push(b' ');
    out.extend_from_slice(data.len().to_string().as_bytes());
    out.push(b'\n');
    out.extend_from_slice(data);
}

fn one_line(s: &str) -> String {
    s.chars().map(|c| if c == '\n' || c == '\r' { ' ' } else { c }).collect()
}

fn cut(s: &str, max: usize) -> &str {
    if s.len() <= max {
        return s;
    }
    let mut end = max;
    while !s.is_char_boundary(end) {
        end -= 1;
    }
    &s[..end]
}

/// A tree node: an id (1 to 1000 bytes, no LF or NUL, unique in its view, stable across
/// patches), a one-line label and optional records.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Node {
    id: Vec<u8>,
    label: String,
    kids: Kids,
    extra: Vec<u8>,
}

impl Node {
    pub fn new(id: impl AsRef<[u8]>, label: &str) -> Node {
        Node {
            id: id.as_ref().to_vec(),
            label: cut(&one_line(label), 1000).to_string(),
            kids: Kids::Leaf,
            extra: Vec::new(),
        }
    }
    pub fn id(&self) -> &[u8] {
        &self.id
    }
    pub fn kids(mut self, kids: Kids) -> Node {
        self.kids = kids;
        self
    }
    /// Muted text after the label.
    pub fn desc(mut self, text: &str) -> Node {
        record(&mut self.extra, "desc", cut(&one_line(text), 1000).as_bytes());
        self
    }
    /// The tooltip, plain text (at most 4000 bytes).
    pub fn tip(mut self, text: &str) -> Node {
        record(&mut self.extra, "tip", cut(text, 4000).as_bytes());
        self
    }
    pub fn icon(mut self, icon: Icon, color: Option<ViewColor>) -> Node {
        let c = color.map(|c| c.word()).unwrap_or_else(|| "-".into());
        record(&mut self.extra, &format!("icon {} {c}", icon.0), b"");
        self
    }
    /// The label's color.
    pub fn color(mut self, color: ViewColor) -> Node {
        record(&mut self.extra, &format!("color {}", color.word()), b"");
        self
    }
    /// Label bytes in the accent color (at most 16 ranges).
    pub fn highlight(mut self, bytes: Range<usize>) -> Node {
        record(&mut self.extra, &format!("hl {} {}", bytes.start, bytes.end), b"");
        self
    }
    /// A count at the row's right (0: none).
    pub fn badge(mut self, n: u32) -> Node {
        record(&mut self.extra, &format!("badge {n}"), b"");
        self
    }
    /// A checkbox.
    pub fn check(mut self, on: bool) -> Node {
        record(&mut self.extra, &format!("check {}", on as u8), b"");
        self
    }
    /// The context value menus test as `viewItem` (`[A-Za-z0-9_.-]`, at most 100 bytes).
    pub fn context(mut self, value: &str) -> Node {
        record(&mut self.extra, "ctx", cut(value, 100).as_bytes());
        self
    }
    /// Bytes given back as ARGS when a manifest menu command runs for the node (at most 4096).
    pub fn data(mut self, bytes: &[u8]) -> Node {
        record(&mut self.extra, "data", &bytes[..bytes.len().min(4096)]);
        self
    }
    /// A click (on a leaf) or Enter opens the file at LINE COL (1-based), as `open` does.
    pub fn open(mut self, path: &str, line: u32, col: u32) -> Node {
        record(&mut self.extra, &format!("open {line} {col}"), path.as_bytes());
        self
    }
    /// A click (on a leaf) or Enter runs one of the extension's commands with ARGS.
    pub fn command(mut self, name: &str, args: &[u8]) -> Node {
        record(&mut self.extra, &format!("command {name}"), args);
        self
    }
    /// Action keys shown as icon buttons on the row while it is hovered or has the cursor (4 at
    /// most; actions defined with [`Patch::action`] and [`Where::Node`]).
    pub fn inline(mut self, keys: &[&str]) -> Node {
        let k = keys.iter().take(4).cloned().collect::<Vec<_>>().join(" ");
        record(&mut self.extra, "inline", k.as_bytes());
        self
    }
    /// Action keys in its context menu (16 at most).
    pub fn menu(mut self, keys: &[&str]) -> Node {
        let k = keys.iter().take(16).cloned().collect::<Vec<_>>().join(" ");
        record(&mut self.extra, "menu", k.as_bytes());
        self
    }
    /// A record this crate has no method for, as the wire writes it: `NAME ARGS... LEN` + data.
    pub fn record(mut self, head: &str, data: &[u8]) -> Node {
        record(&mut self.extra, head, data);
        self
    }

    fn check_id(&self) -> Result<(), String> {
        if self.id.is_empty() || self.id.len() > 1000 || self.id.iter().any(|&b| b == b'\n' || b == 0) {
            return Err(format!("bad node id {:?}", String::from_utf8_lossy(&self.id)));
        }
        Ok(())
    }

    fn encode(&self, out: &mut Vec<u8>) -> Result<(), String> {
        self.check_id()?;
        out.extend_from_slice(
            format!("{} {} {} {}\n", self.id.len(), self.label.len(), self.extra.len(), self.kids.word()).as_bytes(),
        );
        out.extend_from_slice(&self.id);
        out.extend_from_slice(self.label.as_bytes());
        out.extend_from_slice(&self.extra);
        Ok(())
    }
}

/// Where an action shows.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Where {
    /// A button in the view's header.
    Title,
    /// Offered to nodes' `inline` and `menu` records.
    Node,
    /// Removes the action.
    None,
}

/// A view's state.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum State {
    Ready,
    Loading,
    /// The text shown centered, and up to 3 action keys shown as buttons.
    Empty(String, Vec<String>),
    Error(String, Vec<String>),
}

/// Changes to a view, applied whole or not at all. Ops apply in order; one may name nodes
/// created earlier in the same patch.
#[derive(Clone, Debug, Default)]
pub struct Patch {
    ops: Vec<Vec<u8>>,
    err: Option<String>,
    nodes: usize,
}

/// At most this many nodes in one view (400000 in all views).
pub const MAX_NODES: usize = 100_000;

impl Patch {
    pub fn new() -> Patch {
        Patch::default()
    }
    pub fn is_empty(&self) -> bool {
        self.ops.is_empty()
    }
    fn op(&mut self, head: &str, data: &[u8]) -> &mut Patch {
        let mut v = Vec::new();
        record(&mut v, head, data);
        self.ops.push(v);
        self
    }
    /// The view's title (at most 80 bytes; empty: the manifest's).
    pub fn title(&mut self, text: &str) -> &mut Patch {
        let t = cut(&one_line(text), 80).to_string();
        self.op("title", t.as_bytes())
    }
    /// The count beside the title (0 hides it).
    pub fn badge(&mut self, n: u32) -> &mut Patch {
        self.op(&format!("badge {n}"), b"")
    }
    /// A muted line above the content (empty clears it).
    pub fn message(&mut self, text: &str) -> &mut Patch {
        let t = cut(&one_line(text), 1000).to_string();
        self.op("message", t.as_bytes())
    }
    pub fn state(&mut self, state: State) -> &mut Patch {
        let (word, text, keys) = match &state {
            State::Ready => ("ready", String::new(), Vec::new()),
            State::Loading => ("loading", String::new(), Vec::new()),
            State::Empty(t, k) => ("empty", cut(&one_line(t), 1000).to_string(), k.clone()),
            State::Error(t, k) => ("error", cut(&one_line(t), 1000).to_string(), k.clone()),
        };
        let mut data = text.clone().into_bytes();
        data.extend_from_slice(keys.iter().take(3).cloned().collect::<Vec<_>>().join(" ").as_bytes());
        self.op(&format!("state {word} {}", text.len()), &data)
    }
    /// The indeterminate bar at the top of the view.
    pub fn busy(&mut self, on: bool) -> &mut Patch {
        self.op(&format!("busy {}", on as u8), b"")
    }
    fn children(&mut self, name: &str, parent: &[u8], nodes: &[Node], more: bool) -> &mut Patch {
        let mut data = parent.to_vec();
        for n in nodes {
            if let Err(e) = n.encode(&mut data) {
                self.err.get_or_insert(e);
            }
        }
        self.nodes += nodes.len();
        self.op(&format!("{name} {} {} {}", parent.len(), nodes.len(), more as u8), &data)
    }
    /// PARENT's children (an empty parent: the root), replacing the ones it had; a child whose
    /// id it already had keeps its expansion and its own children. `more`: a "Load more" row.
    pub fn kids(&mut self, parent: &[u8], nodes: &[Node], more: bool) -> &mut Patch {
        self.children("kids", parent, nodes, more)
    }
    /// Children appended (an answer to [`crate::Handler::view_more`]).
    pub fn add(&mut self, parent: &[u8], nodes: &[Node], more: bool) -> &mut Patch {
        self.children("add", parent, nodes, more)
    }
    /// An existing node's label, kind of children and records.
    pub fn set(&mut self, node: &Node) -> &mut Patch {
        let mut data = Vec::new();
        if let Err(e) = node.encode(&mut data) {
            self.err.get_or_insert(e);
        }
        self.op("set", &data)
    }
    pub fn del(&mut self, id: &[u8]) -> &mut Patch {
        self.op("del", id)
    }
    pub fn expand(&mut self, id: &[u8]) -> &mut Patch {
        self.op("expand", id)
    }
    pub fn collapse(&mut self, id: &[u8]) -> &mut Patch {
        self.op("collapse", id)
    }
    /// Defines or replaces an action: KEY (`[a-z0-9_]{1,40}`), a built-in icon name (or none:
    /// its title as text), where it shows, its tooltip or menu label.
    pub fn action(&mut self, key: &str, icon: Option<&str>, place: Where, title: &str) -> &mut Patch {
        let w = match place {
            Where::Title => "title",
            Where::Node => "node",
            Where::None => "none",
        };
        let t = cut(&one_line(title), 200).to_string();
        self.op(&format!("action {key} {} {w}", icon.unwrap_or("-")), t.as_bytes())
    }
    /// Several nodes may be selected (Ctrl and Shift).
    pub fn select_mode(&mut self, multi: bool) -> &mut Patch {
        self.op(if multi { "select-mode multi" } else { "select-mode single" }, b"")
    }
    /// An op this crate has no method for, as the wire writes it.
    pub fn raw(&mut self, head: &str, data: &[u8]) -> &mut Patch {
        self.op(head, data)
    }
    /// The body of `vpatch`, or why it cannot be sent.
    pub fn encode(&self) -> Result<Vec<u8>, String> {
        if let Some(e) = &self.err {
            return Err(e.clone());
        }
        if self.nodes > MAX_NODES {
            return Err(format!("{} nodes in one patch (at most {MAX_NODES})", self.nodes));
        }
        let mut out = format!("{}\n", self.ops.len()).into_bytes();
        for op in &self.ops {
            out.extend_from_slice(op);
        }
        if out.len() > 4 << 20 {
            return Err("a patch over 4 MB".into());
        }
        Ok(out)
    }
}

/// A declared view's instance: `0 view VIEW open KIND PLACE NAME`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ViewOpen {
    pub view: View,
    /// `tree` (later `table`, `log`, `form`, `doc`, `picture`).
    pub kind: String,
    /// `sidebar`, `panel`, `side` or `tab`.
    pub place: String,
    /// The manifest key, or `-` for a tab of its own.
    pub name: String,
}

/// What the user did in a retained view.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum RetainedEvent {
    /// It came on (true) or went off screen.
    Visible(bool),
    /// The keyboard came (true) or went.
    Focus(bool),
    /// The selection changed: the node ids.
    Select { rev: u64, ids: Vec<Vec<u8>> },
    /// Enter, a double click, or a click on a leaf without an `open` or `command` record.
    Activate { rev: u64, id: Vec<u8> },
    Expand { rev: u64, id: Vec<u8> },
    Collapse { rev: u64, id: Vec<u8> },
    /// A checkbox toggled.
    Check { rev: u64, on: bool, id: Vec<u8> },
    /// An action invoked on a node (empty id: the header or a state button).
    Action { rev: u64, key: String, id: Vec<u8> },
    /// A user action on a node that no longer exists was dropped.
    Discarded { rev: u64, what: String, id: Vec<u8> },
    /// The user closed a tab instance.
    Closed,
    /// Any other `0 view VIEW WHAT ...` event: WHAT, the fields after it, the body.
    Other { what: String, fields: Vec<String>, body: Vec<u8> },
}

/// rhun asks for a node's children (`vkids`) or for more of them (`vmore`).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ChildrenRequest {
    /// rhun's id for it: a `cancel` event names it.
    pub id: u64,
    pub view: View,
    /// The content it was asked against.
    pub rev: u64,
    /// The node (empty: the root).
    pub node: Vec<u8>,
    /// `vmore`: how many children rhun has.
    pub have: Option<u64>,
}

impl ChildrenRequest {
    pub(crate) fn parse(id: u64, verb: &str, fields: &[String], body: &[u8]) -> Option<ChildrenRequest> {
        let num = |i: usize| fields.get(i).and_then(|v| v.parse::<u64>().ok());
        Some(ChildrenRequest {
            id,
            view: num(0)?,
            rev: num(1)?,
            node: body.to_vec(),
            have: if verb == "vmore" { Some(num(2)?) } else { None },
        })
    }
}

/// Host requests whose last field is a body's length.
pub(crate) fn request_has_body(verb: &str) -> bool {
    matches!(verb, "vkids" | "vmore")
}

/// Retained-view events carry a body: the reader reads LEN bytes after their line.
pub(crate) fn has_body(what: &str) -> bool {
    matches!(
        what,
        "select" | "activate" | "expand" | "collapse" | "check" | "action" | "discarded" | "change" | "link"
    )
}

/// `0 view VIEW open ...` as a [`ViewOpen`].
pub fn parse_open(ev: &Event) -> Option<ViewOpen> {
    if ev.name != "view" || ev.fields.get(1).map(String::as_str) != Some("open") || ev.fields.len() < 5 {
        return None;
    }
    Some(ViewOpen {
        view: ev.fields[0].parse().ok()?,
        kind: ev.fields[2].clone(),
        place: ev.fields[3].clone(),
        name: ev.fields[4].clone(),
    })
}

/// A retained view's event, `None` for 12a picture events (sizes, pointer) and `open`.
pub fn parse_retained(ev: &Event) -> Option<(View, RetainedEvent)> {
    if ev.name != "view" {
        return None;
    }
    let view: View = ev.fields.first()?.parse().ok()?;
    let what = ev.fields.get(1)?.as_str();
    let num = |i: usize| ev.fields.get(i).and_then(|v| v.parse::<u64>().ok()).unwrap_or(0);
    let id = || ev.body.clone();
    let e = match what {
        "visible" => RetainedEvent::Visible(num(2) == 1),
        "focus" => RetainedEvent::Focus(num(2) == 1),
        "select" => RetainedEvent::Select {
            rev: num(2),
            ids: ev
                .body
                .split(|&b| b == b'\n')
                .filter(|s| !s.is_empty())
                .map(<[u8]>::to_vec)
                .collect(),
        },
        "activate" => RetainedEvent::Activate { rev: num(2), id: id() },
        "expand" => RetainedEvent::Expand { rev: num(2), id: id() },
        "collapse" => RetainedEvent::Collapse { rev: num(2), id: id() },
        "check" => RetainedEvent::Check { rev: num(2), on: num(3) == 1, id: id() },
        "action" => RetainedEvent::Action {
            rev: num(2),
            key: ev.fields.get(3).cloned().unwrap_or_default(),
            id: id(),
        },
        "discarded" => RetainedEvent::Discarded {
            rev: num(2),
            what: ev.fields.get(3).cloned().unwrap_or_default(),
            id: id(),
        },
        "size" | "metrics" | "hover" | "down" | "drag" | "up" | "click" | "leave" | "wheel" | "open" => {
            return None
        }
        "closed" => RetainedEvent::Closed,
        _ => RetainedEvent::Other {
            what: what.to_string(),
            fields: ev.fields[2..].to_vec(),
            body: ev.body.clone(),
        },
    };
    Some((view, e))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_patch_encodes_as_the_spec_example() {
        let mut p = Patch::new();
        p.title("TODO").badge(3);
        p.kids(b"", &[Node::new("a", "Alpha").kids(Kids::Open).badge(1)], false);
        let body = p.encode().unwrap();
        assert_eq!(
            body,
            b"3\ntitle 4\nTODObadge 3 0\nkids 0 1 0 28\n1 5 10 open\naAlphabadge 1 0\n".to_vec()
        );
    }

    #[test]
    fn bad_ids_are_refused_before_sending() {
        let mut p = Patch::new();
        p.kids(b"", &[Node::new("a\nb", "x")], false);
        assert!(p.encode().is_err());
        let mut p = Patch::new();
        p.kids(b"", &[Node::new("", "x")], false);
        assert!(p.encode().is_err());
    }

    #[test]
    fn events_parse() {
        let ev = Event {
            name: "view".into(),
            fields: vec!["3".into(), "select".into(), "2".into(), "1".into(), "4".into()],
            rest: String::new(),
            body: b"abc\n".to_vec(),
        };
        assert_eq!(
            parse_retained(&ev),
            Some((3, RetainedEvent::Select { rev: 2, ids: vec![b"abc".to_vec()] }))
        );
        let open = Event {
            name: "view".into(),
            fields: vec!["3".into(), "open".into(), "tree".into(), "sidebar".into(), "todo".into()],
            rest: String::new(),
            body: Vec::new(),
        };
        assert_eq!(parse_open(&open).unwrap().name, "todo");
        assert_eq!(parse_retained(&open), None);
    }
}
