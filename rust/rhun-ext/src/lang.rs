//! Go to definition, references, symbols, code actions, rename and format (sections 12d to 12h
//! of the spec): what rhun asks, and the answers, encoded the way rhun reads them.

use crate::{Doc, Pos, Range};

fn one_line(s: &str) -> String {
    s.chars()
        .map(|c| if c == '\n' || c == '\r' { ' ' } else { c })
        .collect()
}

/// The most bytes of a name, title, detail or preview, and of a kind word, that rhun reads: a
/// longer one would make it drop the whole answer, so the encoders cut them.
pub const MAX_TEXT: usize = 1000;
pub const MAX_KIND: usize = 40;
/// The longest path rhun reads.
pub const MAX_PATH: usize = 4096;

/// One line, at most `max` bytes, cut at a character.
pub(crate) fn field(s: &str, max: usize) -> String {
    let mut s = one_line(s);
    if s.len() > max {
        let mut cut = max;
        while !s.is_char_boundary(cut) {
            cut -= 1;
        }
        s.truncate(cut);
    }
    s
}

/// A kind word: no spaces, at most MAX_KIND bytes.
fn kind_word(s: &str, default: &str) -> String {
    let w: String = s
        .chars()
        .filter(|c| !c.is_whitespace() && !c.is_control())
        .collect();
    let w = field(&w, MAX_KIND);
    if w.is_empty() {
        default.to_string()
    } else {
        w
    }
}

fn num<T: std::str::FromStr>(fields: &[String], i: usize) -> Option<T> {
    fields.get(i)?.parse().ok()
}

/// `ID definition|references|symbols DOC VERSION LINE COL invoked -`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct PosRequest {
    pub doc: Doc,
    /// Every event about the document up to this version has been delivered.
    pub version: i64,
    /// The cursor.
    pub at: Pos,
}

impl PosRequest {
    pub fn parse(fields: &[String]) -> Option<PosRequest> {
        Some(PosRequest {
            doc: num(fields, 0)?,
            version: num(fields, 1)?,
            at: Pos::new(num(fields, 2)?, num(fields, 3)?),
        })
    }
}

/// `ID actions DOC VERSION L1 C1 L2 C2`: the selection, or an empty range at the cursor.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ActionsRequest {
    pub doc: Doc,
    pub version: i64,
    pub range: Range,
}

impl ActionsRequest {
    pub fn parse(fields: &[String]) -> Option<ActionsRequest> {
        let start = Pos::new(num(fields, 2)?, num(fields, 3)?);
        let end = Pos::new(num(fields, 4)?, num(fields, 5)?);
        Some(ActionsRequest {
            doc: num(fields, 0)?,
            version: num(fields, 1)?,
            range: Range::new(start, end),
        })
    }
}

/// `ID rename DOC VERSION LINE COL invoked NAME`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RenameRequest {
    pub doc: Doc,
    pub version: i64,
    pub at: Pos,
    /// The new name the user typed (no spaces).
    pub name: String,
}

impl RenameRequest {
    pub fn parse(fields: &[String]) -> Option<RenameRequest> {
        let name = fields.get(5)?.clone();
        if name.is_empty() {
            return None;
        }
        Some(RenameRequest {
            doc: num(fields, 0)?,
            version: num(fields, 1)?,
            at: Pos::new(num(fields, 2)?, num(fields, 3)?),
            name,
        })
    }
}

/// `ID format DOC VERSION TAB_WIDTH SPACES`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct FormatRequest {
    pub doc: Doc,
    pub version: i64,
    pub tab_width: u32,
    /// Indent with spaces (else tabs).
    pub spaces: bool,
}

impl FormatRequest {
    pub fn parse(fields: &[String]) -> Option<FormatRequest> {
        let spaces = match fields.get(3)?.as_str() {
            "yes" => true,
            "no" => false,
            _ => return None,
        };
        Some(FormatRequest {
            doc: num(fields, 0)?,
            version: num(fields, 1)?,
            tab_width: num(fields, 2)?,
            spaces,
        })
    }
}

/// `ID save-actions DOC VERSION KIND` (negotiated `save-actions`).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SaveActionsRequest {
    /// The save invocation. Intermediate edits must name this ID.
    pub id: u64,
    pub doc: Doc,
    pub version: i64,
    pub kind: String,
}

impl SaveActionsRequest {
    pub fn parse(id: u64, fields: &[String]) -> Option<Self> {
        if id == 0
            || id > 2147483647
            || fields.len() != 3
            || !fields[..2]
                .iter()
                .all(|s| !s.is_empty() && s.bytes().all(|b| b.is_ascii_digit()))
        {
            return None;
        }
        let doc = num(fields, 0)?;
        let version = num(fields, 1)?;
        let kind = fields[2].clone();
        if doc <= 0 || version < 0 || !valid_source_kind(&kind) {
            return None;
        }
        Some(Self {
            id,
            doc,
            version,
            kind,
        })
    }
}

pub(crate) fn valid_source_kind(kind: &str) -> bool {
    kind.len() <= 128
        && (kind == "source"
            || kind.strip_prefix("source.").is_some_and(|rest| {
                rest.split('.').all(|s| {
                    !s.is_empty()
                        && s.bytes()
                            .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
                })
            }))
}

/// Where a location or an edit is: a file by its absolute path, or an open document by its id
/// (an untitled one).
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Target {
    Path(String),
    Doc(Doc),
}

impl Target {
    pub(crate) fn encode(&self) -> String {
        match self {
            Target::Path(p) => p.clone(),
            Target::Doc(d) => format!("#{d}"),
        }
    }
}

/// The most records a list of locations or symbols may hold, and its largest body.
pub const MAX_RECORDS: usize = 10000;
pub const MAX_BODY: usize = 1 << 20;
/// The most code actions, and their largest body (their edits included).
pub const MAX_ACTIONS: usize = 1000;
pub const MAX_ACTIONS_BODY: usize = 4 << 20;
/// A workspace edit's limits.
pub const MAX_EDIT_BODY: usize = 4 << 20;
pub const MAX_EDIT_FILES: usize = 1000;
pub const MAX_EDITS: usize = 100000;

/// A place in a file: the answer to definition and references is a list of them.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Location {
    pub target: Target,
    /// In the open document when the file is open, else in the file on disk.
    pub range: Range,
    /// A one-line preview (the line's text), or empty.
    pub text: String,
}

impl Location {
    pub fn new(target: Target, range: Range) -> Location {
        Location {
            target,
            range,
            text: String::new(),
        }
    }

    pub fn text(mut self, text: &str) -> Location {
        self.text = field(text.trim(), MAX_TEXT);
        self
    }

    /// Its record, or nothing for a path rhun cannot take (empty, or over MAX_PATH bytes).
    fn encode(&self, out: &mut Vec<u8>) {
        let p = self.target.encode();
        if p.is_empty() || p.len() > MAX_PATH {
            return;
        }
        let t = field(&self.text, MAX_TEXT);
        let r = self.range;
        out.extend_from_slice(
            format!(
                "{} {} {} {} {} {}\n",
                r.start.line,
                r.start.col,
                r.end.line,
                r.end.col,
                p.len(),
                t.len()
            )
            .as_bytes(),
        );
        out.extend_from_slice(p.as_bytes());
        out.extend_from_slice(t.as_bytes());
    }
}

/// The body for a list of locations. Those past [`MAX_RECORDS`] or [`MAX_BODY`] are left out.
pub fn encode_locations(locs: &[Location]) -> Vec<u8> {
    let mut recs = Vec::new();
    let mut n = 0;
    for l in locs {
        let mut one = Vec::new();
        l.encode(&mut one);
        if one.is_empty() {
            continue;
        }
        if n == MAX_RECORDS || recs.len() + one.len() + 16 > MAX_BODY {
            break;
        }
        recs.extend_from_slice(&one);
        n += 1;
    }
    let mut out = format!("{n}\n").into_bytes();
    out.extend_from_slice(&recs);
    out
}

/// What a symbol is.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum SymbolKind {
    File,
    Module,
    Namespace,
    Package,
    Class,
    Method,
    Property,
    Field,
    Constructor,
    Enum,
    Interface,
    #[default]
    Function,
    Variable,
    Constant,
    String,
    Number,
    Boolean,
    Array,
    Object,
    Key,
    Null,
    EnumMember,
    Struct,
    Event,
    Operator,
    TypeParameter,
}

impl SymbolKind {
    pub fn word(self) -> &'static str {
        use SymbolKind::*;
        match self {
            File => "file",
            Module => "module",
            Namespace => "namespace",
            Package => "package",
            Class => "class",
            Method => "method",
            Property => "property",
            Field => "field",
            Constructor => "constructor",
            Enum => "enum",
            Interface => "interface",
            Function => "function",
            Variable => "variable",
            Constant => "constant",
            String => "string",
            Number => "number",
            Boolean => "boolean",
            Array => "array",
            Object => "object",
            Key => "key",
            Null => "null",
            EnumMember => "enum_member",
            Struct => "struct",
            Event => "event",
            Operator => "operator",
            TypeParameter => "type_parameter",
        }
    }
}

/// A symbol of a document: the answer to symbols is a list of them, in document order.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Symbol {
    /// The whole symbol; the cursor goes to its start.
    pub range: Range,
    pub kind: SymbolKind,
    /// 0 at the top level, one more for each level it is nested in.
    pub depth: u32,
    pub name: String,
    /// Shown beside it: a signature, a type.
    pub detail: String,
}

impl Symbol {
    pub fn new(range: Range, kind: SymbolKind, depth: u32, name: &str) -> Symbol {
        Symbol {
            range,
            kind,
            depth,
            name: field(name, MAX_TEXT),
            detail: String::new(),
        }
    }

    pub fn detail(mut self, text: &str) -> Symbol {
        self.detail = field(text, MAX_TEXT);
        self
    }

    /// Its record, or nothing for a symbol without a name.
    fn encode(&self, out: &mut Vec<u8>) {
        let n = field(&self.name, MAX_TEXT);
        if n.is_empty() {
            return;
        }
        let d = field(&self.detail, MAX_TEXT);
        let r = self.range;
        out.extend_from_slice(
            format!(
                "{} {} {} {} {} {} {} {}\n",
                r.start.line,
                r.start.col,
                r.end.line,
                r.end.col,
                self.kind.word(),
                self.depth,
                n.len(),
                d.len()
            )
            .as_bytes(),
        );
        out.extend_from_slice(n.as_bytes());
        out.extend_from_slice(d.as_bytes());
    }
}

/// The body for a list of symbols. Those past [`MAX_RECORDS`] or [`MAX_BODY`] are left out.
pub fn encode_symbols(syms: &[Symbol]) -> Vec<u8> {
    let mut recs = Vec::new();
    let mut n = 0;
    for s in syms {
        let mut one = Vec::new();
        s.encode(&mut one);
        if one.is_empty() {
            continue;
        }
        if n == MAX_RECORDS || recs.len() + one.len() + 16 > MAX_BODY {
            break;
        }
        recs.extend_from_slice(&one);
        n += 1;
    }
    let mut out = format!("{n}\n").into_bytes();
    out.extend_from_slice(&recs);
    out
}

/// One replacement in a text.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct TextEdit {
    pub range: Range,
    pub text: String,
}

impl TextEdit {
    pub fn new(range: Range, text: &str) -> TextEdit {
        TextEdit {
            range,
            text: text.to_string(),
        }
    }
}

/// The edits of one file, all made against one text: the open document at `version`, or the
/// file on disk (`version` `None`). They must not overlap; insertions at one position keep their
/// order.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct FileEdit {
    pub target: Target,
    pub version: Option<i64>,
    pub edits: Vec<TextEdit>,
    /// With feature `workspace-edits` and `version` `None`: the 64-bit FNV-1a hash of the file's
    /// bytes on disk ([`fnv1a64`]), which the file must still hash to (PRE `fnv1a64:HEX`).
    /// Without the feature such an edit is refused (`unknown`) and not sent.
    pub hash: Option<u64>,
}

impl Default for Target {
    fn default() -> Target {
        Target::Path(String::new())
    }
}

/// The 64-bit FNV-1a hash of some bytes: offset basis `0xcbf29ce484222325`, prime
/// `0x100000001b3`. A text group's `fnv1a64:HEX` precondition is this hash of the file's bytes on
/// disk (CR included).
pub fn fnv1a64(bytes: &[u8]) -> u64 {
    let mut h: u64 = 0xcbf2_9ce4_8422_2325;
    for &b in bytes {
        h ^= u64::from(b);
        h = h.wrapping_mul(0x0000_0100_0000_01b3);
    }
    h
}

/// How a `create` entry treats a path that exists (feature `file-operations`). `overwrite` wins
/// over `ignore_existing`; with neither, an existing path refuses the edit.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Create {
    /// Replace an existing file with an empty one.
    pub overwrite: bool,
    /// Skip the entry when the path exists.
    pub ignore_existing: bool,
}

/// How a `rename` entry treats a target that exists (feature `file-operations`).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Rename {
    /// Replace an existing file at the target (a folder is never replaced).
    pub overwrite: bool,
    /// Skip the entry when the target exists.
    pub ignore_existing: bool,
}

/// How a `delete` entry treats folders and missing paths (feature `file-operations`).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Delete {
    /// A folder and everything in it; without it only a file or an empty folder.
    pub recursive: bool,
    /// Skip the entry when the path does not exist.
    pub ignore_missing: bool,
}

/// A file operation of a workspace edit (feature `file-operations`). Paths are absolute; a
/// create path ending in `/` makes a folder.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum FileOp {
    Create {
        path: String,
        opts: Create,
    },
    Rename {
        from: String,
        to: String,
        opts: Rename,
    },
    Delete {
        path: String,
        opts: Delete,
    },
}

fn flags(pairs: &[(bool, &str)]) -> String {
    let words: Vec<&str> = pairs.iter().filter(|p| p.0).map(|p| p.1).collect();
    if words.is_empty() {
        "-".to_string()
    } else {
        words.join(",")
    }
}

impl FileOp {
    /// Its phase: creates first, renames after the text groups, deletes last.
    fn phase(&self) -> u8 {
        match self {
            FileOp::Create { .. } => 0,
            FileOp::Rename { .. } => 2,
            FileOp::Delete { .. } => 3,
        }
    }

    fn paths(&self) -> Vec<&str> {
        match self {
            FileOp::Create { path, .. } | FileOp::Delete { path, .. } => vec![path],
            FileOp::Rename { from, to, .. } => vec![from, to],
        }
    }

    fn encode(&self, out: &mut Vec<u8>) {
        match self {
            FileOp::Create { path, opts } => {
                let f = flags(&[
                    (opts.overwrite, "overwrite"),
                    (opts.ignore_existing, "ignore-existing"),
                ]);
                out.extend_from_slice(format!("create {} {f}\n{path}", path.len()).as_bytes());
            }
            FileOp::Rename { from, to, opts } => {
                let f = flags(&[
                    (opts.overwrite, "overwrite"),
                    (opts.ignore_existing, "ignore-existing"),
                ]);
                out.extend_from_slice(
                    format!("rename {} {} {f}\n{from}{to}", from.len(), to.len()).as_bytes(),
                );
            }
            FileOp::Delete { path, opts } => {
                let f = flags(&[
                    (opts.recursive, "recursive"),
                    (opts.ignore_missing, "ignore-missing"),
                ]);
                out.extend_from_slice(format!("delete {} {f}\n{path}", path.len()).as_bytes());
            }
        }
    }
}

/// The longest label rhun shows (longer is cut there; the encoder cuts it at a character).
pub const MAX_LABEL: usize = 200;

/// Edits across files (section 12g): rhun checks all of them before it changes anything. Without
/// feature `workspace-edits` it opens the files that are not open and applies each file's edits
/// as one undo step, saving nothing; with it the edit is one transaction (files that are not open
/// are written on disk, undone as one step), and with `file-operations` it may also create,
/// rename and delete files.
///
/// The body lists the entries in rhun's phases: the creates, then the text groups (`files`),
/// then the renames, then the deletes, each in the order added. [`WorkspaceEdit::entry`] maps
/// an error's ENTRY back to them.
///
/// [`crate::Ext::apply`] and the answers that carry an edit follow the features rhun agreed: an
/// edit with file operations, a hashed file, a label or `confirm` is refused (`unknown`) when they
/// were not, and in the bodies that keep 12g's grammar (format and save-actions answers,
/// [`crate::Ext::save_apply`]). [`crate::Ext::propose`] sends 12g's grammar too, a hash as `-` and
/// without the label and `confirm`.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct WorkspaceEdit {
    pub files: Vec<FileEdit>,
    /// File operations (feature `file-operations`).
    pub ops: Vec<FileOp>,
    /// Names the change in the confirmation list, toasts and Undo (feature `workspace-edits`).
    pub label: Option<String>,
    /// Show the confirmation list even when the extension may write (feature `workspace-edits`).
    pub confirm: bool,
}

/// What an entry of a workspace edit's body is: [`WorkspaceEdit::entry`].
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Entry {
    /// `files[i]`
    File(usize),
    /// `ops[i]`
    Op(usize),
}

/// Which parts of the grammar a body may use: `ws` and `ops` what the receiving rhun agreed
/// (`workspace-edits`, `file-operations`); `fixed` names a body that keeps 12g's grammar whatever
/// was agreed (a format or save-actions answer, save-apply, propose).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct EditCaps {
    pub ws: bool,
    pub ops: bool,
    pub fixed: Option<&'static str>,
}

impl EditCaps {
    pub(crate) const ALL: EditCaps = EditCaps {
        ws: true,
        ops: true,
        fixed: None,
    };

    /// What a rhun reads that agreed `ws` and `ops`.
    pub(crate) const fn agreed(ws: bool, ops: bool) -> EditCaps {
        EditCaps {
            ws,
            ops,
            fixed: None,
        }
    }

    /// 12g's grammar whatever was agreed, for the body `what` names.
    pub(crate) const fn fixed(what: &'static str) -> EditCaps {
        EditCaps {
            ws: false,
            ops: false,
            fixed: Some(what),
        }
    }
}

impl WorkspaceEdit {
    pub fn new() -> WorkspaceEdit {
        WorkspaceEdit::default()
    }

    /// Edits for an open document, made against its text at `version`.
    pub fn doc(mut self, doc: Doc, version: i64, edits: Vec<TextEdit>) -> WorkspaceEdit {
        self.files.push(FileEdit {
            target: Target::Doc(doc),
            version: Some(version),
            edits,
            hash: None,
        });
        self
    }

    /// Edits for a file by its path: the open document at `version`, or with `None` the file as
    /// it is on disk (rhun opens it; with `workspace-edits`, edits it on disk).
    pub fn file(mut self, path: &str, version: Option<i64>, edits: Vec<TextEdit>) -> WorkspaceEdit {
        self.files.push(FileEdit {
            target: Target::Path(path.to_string()),
            version,
            edits,
            hash: None,
        });
        self
    }

    /// Edits for a file that is not open, made against `disk`, its bytes as read from disk (CR
    /// included): with `workspace-edits` the file must still hash to them (`fnv1a64:HEX`), else
    /// the edit is `stale`. Positions refer to rhun's text of it (CR before LF dropped).
    pub fn file_hashed(mut self, path: &str, disk: &[u8], edits: Vec<TextEdit>) -> WorkspaceEdit {
        self.files.push(FileEdit {
            target: Target::Path(path.to_string()),
            version: None,
            edits,
            hash: Some(fnv1a64(disk)),
        });
        self
    }

    /// Creates a file (empty) or, with a path ending in `/`, a folder (feature `file-operations`).
    pub fn create(mut self, path: &str, opts: Create) -> WorkspaceEdit {
        self.ops.push(FileOp::Create {
            path: path.to_string(),
            opts,
        });
        self
    }

    /// Renames a file or folder (feature `file-operations`); text groups name it by `from`.
    pub fn rename(mut self, from: &str, to: &str, opts: Rename) -> WorkspaceEdit {
        self.ops.push(FileOp::Rename {
            from: from.to_string(),
            to: to.to_string(),
            opts,
        });
        self
    }

    /// Deletes a file or folder (feature `file-operations`).
    pub fn delete(mut self, path: &str, opts: Delete) -> WorkspaceEdit {
        self.ops.push(FileOp::Delete {
            path: path.to_string(),
            opts,
        });
        self
    }

    /// Names the change for the user (one line, cut at 200 bytes).
    pub fn label(mut self, label: &str) -> WorkspaceEdit {
        self.label = Some(field(label, MAX_LABEL));
        self
    }

    /// Asks rhun to show the confirmation list even when the extension may write.
    pub fn confirm(mut self) -> WorkspaceEdit {
        self.confirm = true;
        self
    }

    /// Whether it changes nothing: no file operation and no edit.
    pub fn is_empty(&self) -> bool {
        self.ops.is_empty() && self.files.iter().all(|f| f.edits.is_empty())
    }

    /// The entries in the order the body lists them (rhun's phases).
    pub(crate) fn order(&self) -> Vec<Entry> {
        let mut out = Vec::with_capacity(self.files.len() + self.ops.len());
        for phase in 0..4u8 {
            if phase == 1 {
                out.extend((0..self.files.len()).map(Entry::File));
                continue;
            }
            for (i, op) in self.ops.iter().enumerate() {
                if op.phase() == phase {
                    out.push(Entry::Op(i));
                }
            }
        }
        out
    }

    /// What the body's 1-based entry `n` is (an apply error's ENTRY), or `None` for 0 and past
    /// the last.
    pub fn entry(&self, n: u32) -> Option<Entry> {
        if n == 0 {
            return None;
        }
        self.order().get(n as usize - 1).copied()
    }

    /// The body rhun reads with `workspace-edits` and `file-operations` agreed, or why it cannot
    /// be one (over [`MAX_EDIT_BODY`], [`MAX_EDIT_FILES`] entries or [`MAX_EDITS`], a path that is
    /// empty or over [`MAX_PATH`] bytes): an edit cut short would leave the files half changed,
    /// so none is sent. Without file operations, hashes, a label or `confirm` it is exactly the
    /// 12g body. [`crate::Ext::encode_edit`] is the body for the features a rhun agreed.
    pub fn encode(&self) -> Result<Vec<u8>, String> {
        self.encode_with(EditCaps::ALL).map_err(|e| e.1)
    }

    /// The first part of the edit that only the transaction grammar of `workspace-edits` has:
    /// file operations, a file's fnv1a64 precondition, the label, `confirm`.
    fn tx_part(&self) -> Option<&'static str> {
        if !self.ops.is_empty() {
            Some("file operations")
        } else if self
            .files
            .iter()
            .any(|f| f.version.is_none() && f.hash.is_some())
        {
            Some("fnv1a64 precondition")
        } else if self.label.is_some() {
            Some("label")
        } else if self.confirm {
            Some("confirm record")
        } else {
            None
        }
    }

    /// The body for a rhun that agreed `caps`: 12g's grammar without `workspace-edits`. An edit
    /// that needs more than `caps` allow (file operations without both features; a hash, a label
    /// or `confirm` without `workspace-edits` or in a fixed 12g body) is refused as `unknown`, one
    /// that does not fit as `toobig`: nothing rhun would refuse is sent. Errors carry the code to
    /// answer with.
    pub(crate) fn encode_with(&self, caps: EditCaps) -> Result<Vec<u8>, (&'static str, String)> {
        if let Some(part) = self.tx_part() {
            if let Some(what) = caps.fixed {
                return Err((
                    "unknown",
                    format!("{what} keeps 12g's grammar, which has no {part}"),
                ));
            }
            if !(self.ops.is_empty() || caps.ws && caps.ops) {
                return Err((
                    "unknown",
                    "file operations need the workspace-edits and file-operations features".into(),
                ));
            }
            if !caps.ws {
                return Err((
                    "unknown",
                    format!(
                        "the edit's {part} needs the workspace-edits feature, which was not agreed"
                    ),
                ));
            }
        }
        let entries = self.files.len() + self.ops.len();
        if entries > MAX_EDIT_FILES {
            return Err((
                "toobig",
                format!("{entries} entries: at most {MAX_EDIT_FILES}"),
            ));
        }
        let edits: usize = self.files.iter().map(|f| f.edits.len()).sum();
        if edits > MAX_EDITS {
            return Err(("toobig", format!("{edits} edits: at most {MAX_EDITS}")));
        }
        let mut out = format!("{entries}\n").into_bytes();
        for e in self.order() {
            match e {
                Entry::Op(i) => {
                    let op = &self.ops[i];
                    for p in op.paths() {
                        if p.is_empty() || p.len() > MAX_PATH {
                            return Err((
                                "toobig",
                                format!("a path of {} bytes: 1 to {MAX_PATH}", p.len()),
                            ));
                        }
                    }
                    op.encode(&mut out);
                }
                Entry::File(i) => {
                    let f = &self.files[i];
                    let p = f.target.encode();
                    if p.is_empty() || p.len() > MAX_PATH {
                        return Err((
                            "toobig",
                            format!("a path of {} bytes: 1 to {MAX_PATH}", p.len()),
                        ));
                    }
                    let pre = match (f.version, f.hash) {
                        (Some(v), _) => v.to_string(),
                        // refused above without workspace-edits
                        (None, Some(h)) => format!("fnv1a64:{h:016x}"),
                        (None, None) => "-".to_string(),
                    };
                    out.extend_from_slice(
                        format!("{} {} {}\n", p.len(), pre, f.edits.len()).as_bytes(),
                    );
                    out.extend_from_slice(p.as_bytes());
                    for e in &f.edits {
                        let r = e.range;
                        out.extend_from_slice(
                            format!(
                                "{} {} {} {} {}\n",
                                r.start.line,
                                r.start.col,
                                r.end.line,
                                r.end.col,
                                e.text.len()
                            )
                            .as_bytes(),
                        );
                        out.extend_from_slice(e.text.as_bytes());
                    }
                }
            }
            if out.len() > MAX_EDIT_BODY {
                return Err(("toobig", format!("over {} bytes", MAX_EDIT_BODY)));
            }
        }
        // both refused above without workspace-edits
        if let Some(l) = &self.label {
            let l = field(l, MAX_LABEL);
            out.extend_from_slice(format!("label {}\n{l}", l.len()).as_bytes());
        }
        if self.confirm {
            out.extend_from_slice(b"confirm 0\n");
        }
        if out.len() > MAX_EDIT_BODY {
            return Err(("toobig", format!("over {} bytes", MAX_EDIT_BODY)));
        }
        Ok(out)
    }
}

/// A code action: shown in the list by its title; choosing it applies `edit` and then sends
/// `data` back ([`crate::Handler::action`]).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CodeAction {
    /// `quickfix` (listed first), `refactor`, `source`, or another word shown beside the title.
    pub kind: String,
    pub title: String,
    pub edit: Option<WorkspaceEdit>,
    pub data: Vec<u8>,
}

impl CodeAction {
    pub fn new(kind: &str, title: &str) -> CodeAction {
        CodeAction {
            kind: kind_word(kind, "quickfix"),
            title: field(title, MAX_TEXT),
            edit: None,
            data: Vec::new(),
        }
    }

    pub fn edit(mut self, edit: WorkspaceEdit) -> CodeAction {
        self.edit = Some(edit);
        self
    }

    pub fn data(mut self, data: &[u8]) -> CodeAction {
        self.data = data.to_vec();
        self
    }
}

/// The body for a list of code actions, their edits in the grammar of a rhun that agreed
/// `workspace-edits` and `file-operations` (a handler's answer follows what was agreed). Actions
/// past [`MAX_ACTIONS`] or [`MAX_ACTIONS_BODY`], and those whose edit cannot be encoded, are left
/// out.
pub fn encode_actions(actions: &[CodeAction]) -> Vec<u8> {
    encode_actions_with(actions, EditCaps::ALL)
}

/// [`encode_actions`] for a rhun that agreed `caps`: an action whose edit needs a feature it did
/// not agree is left out (rhun would refuse the edit when it is chosen).
pub(crate) fn encode_actions_with(actions: &[CodeAction], caps: EditCaps) -> Vec<u8> {
    let mut recs = Vec::new();
    let mut n = 0;
    for a in actions {
        let edit = match &a.edit {
            Some(e) => match e.encode_with(caps) {
                Ok(b) => b,
                Err(_) => continue,
            },
            None => Vec::new(),
        };
        let title = field(&a.title, MAX_TEXT);
        if title.is_empty() {
            continue;
        }
        let head = format!(
            "{} {} {} {}\n",
            kind_word(&a.kind, "quickfix"),
            title.len(),
            edit.len(),
            a.data.len()
        );
        if n == MAX_ACTIONS
            || recs.len() + head.len() + title.len() + edit.len() + a.data.len() + 16
                > MAX_ACTIONS_BODY
        {
            break;
        }
        recs.extend_from_slice(head.as_bytes());
        recs.extend_from_slice(title.as_bytes());
        recs.extend_from_slice(&edit);
        recs.extend_from_slice(&a.data);
        n += 1;
    }
    let mut out = format!("{n}\n").into_bytes();
    out.extend_from_slice(&recs);
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fields(s: &str) -> Vec<String> {
        s.split(' ').map(str::to_string).collect()
    }

    fn r(l1: u32, c1: u32, l2: u32, c2: u32) -> Range {
        Range::new(Pos::new(l1, c1), Pos::new(l2, c2))
    }

    #[test]
    fn requests_parse() {
        assert_eq!(
            PosRequest::parse(&fields("3 7 10 5 invoked -")),
            Some(PosRequest {
                doc: 3,
                version: 7,
                at: Pos::new(10, 5)
            })
        );
        assert_eq!(
            ActionsRequest::parse(&fields("3 7 1 2 3 4")).unwrap().range,
            r(1, 2, 3, 4)
        );
        let rn = RenameRequest::parse(&fields("3 7 1 2 invoked new_name")).unwrap();
        assert_eq!((rn.at, rn.name.as_str()), (Pos::new(1, 2), "new_name"));
        assert!(RenameRequest::parse(&fields("3 7 1 2 invoked")).is_none());
        assert_eq!(
            FormatRequest::parse(&fields("3 7 4 yes")),
            Some(FormatRequest {
                doc: 3,
                version: 7,
                tab_width: 4,
                spaces: true
            })
        );
        assert!(FormatRequest::parse(&fields("3 7 4 maybe")).is_none());
    }

    #[test]
    fn locations_are_framed_as_rhun_reads_them() {
        let body = encode_locations(&[
            Location::new(Target::Path("/a/b.rs".into()), r(1, 4, 1, 8)).text("  fn main() {\n"),
            Location::new(Target::Doc(7), r(2, 1, 2, 1)),
        ]);
        assert_eq!(
            body,
            b"2\n1 4 1 8 7 11\n/a/b.rsfn main() {2 1 2 1 2 0\n#7".to_vec()
        );
        assert_eq!(encode_locations(&[]), b"0\n".to_vec());
    }

    #[test]
    fn symbols_are_framed_as_rhun_reads_them() {
        let body = encode_symbols(&[
            Symbol::new(r(1, 1, 3, 2), SymbolKind::Struct, 0, "Point"),
            Symbol::new(r(2, 5, 2, 11), SymbolKind::Field, 1, "x").detail("i32"),
        ]);
        assert_eq!(
            body,
            b"2\n1 1 3 2 struct 0 5 0\nPoint2 5 2 11 field 1 1 3\nxi32".to_vec()
        );
        let many: Vec<Symbol> = (0..10001)
            .map(|i| Symbol::new(r(1, 1, 1, 1), SymbolKind::Variable, 0, &i.to_string()))
            .collect();
        assert!(encode_symbols(&many).starts_with(b"10000\n"));
    }

    #[test]
    fn workspace_edits_are_framed_and_checked() {
        let e = WorkspaceEdit::new()
            .doc(3, 9, vec![TextEdit::new(r(1, 1, 1, 4), "new")])
            .file(
                "/p/x.rs",
                None,
                vec![
                    TextEdit::new(r(2, 1, 2, 1), "a\n"),
                    TextEdit::new(r(5, 1, 5, 3), ""),
                ],
            );
        assert_eq!(
            e.encode().unwrap(),
            b"2\n2 9 1\n#31 1 1 4 3\nnew7 - 2\n/p/x.rs2 1 2 1 2\na\n5 1 5 3 0\n".to_vec()
        );
        let big = WorkspaceEdit::new().doc(1, 1, vec![TextEdit::default(); MAX_EDITS + 1]);
        assert!(big.encode().is_err());
        assert!(WorkspaceEdit::new().is_empty());
        assert_eq!(WorkspaceEdit::new().encode().unwrap(), b"0\n".to_vec());
    }

    #[test]
    fn fnv1a64_is_the_standard_hash() {
        assert_eq!(fnv1a64(b""), 0xcbf29ce484222325);
        assert_eq!(fnv1a64(b"a"), 0xaf63dc4c8601ec8c);
        assert_eq!(fnv1a64(b"foobar"), 0x85944171f73967e8);
    }

    #[test]
    fn transactions_are_framed_as_the_spec_says() {
        let e = WorkspaceEdit::new()
            .label("Rename to 'total'\nnow")
            .delete(
                "/p/old.rs",
                Delete {
                    recursive: true,
                    ignore_missing: true,
                },
            )
            .file_hashed(
                "/p/calc.rs",
                b"a\r\nb\r\n",
                vec![TextEdit::new(r(2, 1, 2, 2), "B")],
            )
            .rename(
                "/p/util.rs",
                "/p/helpers.rs",
                Rename {
                    overwrite: true,
                    ignore_existing: false,
                },
            )
            .doc(3, 9, vec![TextEdit::new(r(1, 1, 1, 1), "x")])
            .create("/p/new/", Create::default())
            .create(
                "/p/new/a.rs",
                Create {
                    overwrite: false,
                    ignore_existing: true,
                },
            )
            .confirm();
        let h = format!("{:016x}", fnv1a64(b"a\r\nb\r\n"));
        let want = format!(
            "6\ncreate 7 -\n/p/new/create 11 ignore-existing\n/p/new/a.rs\
             10 fnv1a64:{h} 1\n/p/calc.rs2 1 2 2 1\nB2 9 1\n#31 1 1 1 1\nx\
             rename 10 13 overwrite\n/p/util.rs/p/helpers.rs\
             delete 9 recursive,ignore-missing\n/p/old.rs\
             label 21\nRename to 'total' nowconfirm 0\n"
        );
        assert_eq!(String::from_utf8(e.encode().unwrap()).unwrap(), want);
        // entries are numbered in that order
        assert_eq!(e.entry(1), Some(Entry::Op(2)));
        assert_eq!(e.entry(2), Some(Entry::Op(3)));
        assert_eq!(e.entry(3), Some(Entry::File(0)));
        assert_eq!(e.entry(4), Some(Entry::File(1)));
        assert_eq!(e.entry(5), Some(Entry::Op(1)));
        assert_eq!(e.entry(6), Some(Entry::Op(0)));
        assert_eq!(e.entry(0), None);
        assert_eq!(e.entry(7), None);
        // a rhun without file-operations gets no body (transaction_parts_need_the_feature has
        // the rest)
        let ws = EditCaps::agreed(true, false);
        assert_eq!(e.encode_with(ws).unwrap_err().0, "unknown");
        let plain = WorkspaceEdit::new()
            .label("L")
            .file_hashed("/p/a", b"x", vec![])
            .confirm();
        assert_eq!(
            String::from_utf8(plain.encode_with(ws).unwrap()).unwrap(),
            format!(
                "1\n4 fnv1a64:{:016x} 0\n/p/alabel 1\nLconfirm 0\n",
                fnv1a64(b"x")
            )
        );
        // a label is one line of at most 200 bytes, cut at a character
        let long = WorkspaceEdit::new().label(&format!("{}é", "x".repeat(199)));
        assert_eq!(long.label.as_deref(), Some("x".repeat(199).as_str()));
        // file operations count as entries, and their paths are checked
        let many = (0..1001).fold(WorkspaceEdit::new(), |e, i| {
            e.delete(&format!("/p/{i}"), Delete::default())
        });
        assert_eq!(many.encode_with(EditCaps::ALL).unwrap_err().0, "toobig");
        assert!(WorkspaceEdit::new()
            .create("", Create::default())
            .encode()
            .is_err());
        assert!(!WorkspaceEdit::new()
            .create("/p/x", Create::default())
            .is_empty());
    }

    #[test]
    fn transaction_parts_need_the_feature() {
        let x = || vec![TextEdit::new(r(1, 1, 1, 1), "x")];
        let hashed = WorkspaceEdit::new().file_hashed("/p/a", b"x", x());
        let labelled = WorkspaceEdit::new().label("L").doc(3, 9, x());
        let confirmed = WorkspaceEdit::new().confirm().doc(3, 9, x());
        let ops = WorkspaceEdit::new().create("/p/n", Create::default());
        for (e, part) in [
            (&hashed, "fnv1a64 precondition"),
            (&labelled, "label"),
            (&confirmed, "confirm record"),
        ] {
            assert_eq!(
                e.encode_with(EditCaps::agreed(false, false)),
                Err((
                    "unknown",
                    format!(
                        "the edit's {part} needs the workspace-edits feature, which was not agreed"
                    )
                ))
            );
            assert_eq!(
                e.encode_with(EditCaps::fixed("a format answer")),
                Err((
                    "unknown",
                    format!("a format answer keeps 12g's grammar, which has no {part}")
                ))
            );
            assert!(e.encode_with(EditCaps::agreed(true, false)).is_ok());
        }
        for caps in [
            EditCaps::agreed(false, false),
            EditCaps::agreed(true, false),
            EditCaps::agreed(false, true),
        ] {
            assert_eq!(
                ops.encode_with(caps).unwrap_err().1,
                "file operations need the workspace-edits and file-operations features"
            );
        }
        assert_eq!(
            ops.encode_with(EditCaps::fixed("propose")).unwrap_err().1,
            "propose keeps 12g's grammar, which has no file operations"
        );
        assert!(ops.encode().is_ok());
        // plain text edits are 12g's body whatever was agreed; a hash beside a version is not sent
        let mut plain = WorkspaceEdit::new().doc(3, 9, x()).file("/p/a", None, x());
        plain.files.push(FileEdit {
            target: Target::Doc(4),
            version: Some(2),
            edits: x(),
            hash: Some(1),
        });
        let want = b"3\n2 9 1\n#31 1 1 1 1\nx4 - 1\n/p/a1 1 1 1 1\nx2 2 1\n#41 1 1 1 1\nx".to_vec();
        for caps in [
            EditCaps::agreed(false, false),
            EditCaps::ALL,
            EditCaps::fixed("a format answer"),
        ] {
            assert_eq!(plain.encode_with(caps), Ok(want.clone()));
        }
    }

    #[test]
    fn actions_carry_their_edit_and_data() {
        let edit = WorkspaceEdit::new().doc(1, 2, vec![TextEdit::new(r(1, 1, 1, 1), "x")]);
        let body = encode_actions(&[
            CodeAction::new("quickfix", "Add x").edit(edit),
            CodeAction::new("source", "Organize imports").data(b"organize"),
        ]);
        let edit = b"1\n2 2 1\n#11 1 1 1 1\nx";
        let mut want = format!("2\nquickfix 5 {} 0\nAdd x", edit.len()).into_bytes();
        want.extend_from_slice(edit);
        want.extend_from_slice(b"source 16 0 8\nOrganize importsorganize");
        assert_eq!(body, want);
        assert_eq!(CodeAction::new(" ", "t").kind, "quickfix");
    }

    #[test]
    fn fields_rhun_would_refuse_are_cut_or_left_out() {
        // a two-byte character across byte 1000 is left out whole
        let long = format!("{}é{}", "x".repeat(999), "y".repeat(10));
        let s = Symbol::new(r(1, 1, 1, 1), SymbolKind::Function, 0, &long);
        assert_eq!(s.name, "x".repeat(999));
        let body = encode_actions(&[
            CodeAction::new(&"k".repeat(50), &long),
            CodeAction::new("source", ""),
        ]);
        assert!(body.starts_with(format!("1\n{} 999 0 0\n", "k".repeat(40)).as_bytes()));
        let far = Location::new(
            Target::Path(format!("/{}", "p".repeat(4096))),
            r(1, 1, 1, 1),
        );
        let near = Location::new(Target::Path("/p".into()), r(1, 1, 1, 1));
        assert!(encode_locations(&[far.clone(), near]).starts_with(b"1\n"));
        assert!(
            encode_symbols(&[Symbol::new(r(1, 1, 1, 1), SymbolKind::Function, 0, "")]) == b"0\n"
        );
        let e = WorkspaceEdit::new().file(&format!("/{}", "p".repeat(4096)), None, vec![]);
        assert!(e.encode().is_err());
    }
}
