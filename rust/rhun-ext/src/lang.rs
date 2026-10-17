//! Go to definition, references, symbols, code actions, rename and format (sections 12d to 12h
//! of the spec): what rhun asks, and the answers, encoded the way rhun reads them.

use crate::{Doc, Pos, Range};

fn one_line(s: &str) -> String {
    s.chars().map(|c| if c == '\n' || c == '\r' { ' ' } else { c }).collect()
}

/// The most bytes of a name, title, detail or preview, and of a kind word, that rhun reads: a
/// longer one would make it drop the whole answer, so the encoders cut them.
pub const MAX_TEXT: usize = 1000;
pub const MAX_KIND: usize = 40;
/// The longest path rhun reads.
pub const MAX_PATH: usize = 4096;

/// One line, at most `max` bytes, cut at a character.
fn field(s: &str, max: usize) -> String {
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
    let w: String = s.chars().filter(|c| !c.is_whitespace() && !c.is_control()).collect();
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
        Some(PosRequest { doc: num(fields, 0)?, version: num(fields, 1)?, at: Pos::new(num(fields, 2)?, num(fields, 3)?) })
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
        Some(ActionsRequest { doc: num(fields, 0)?, version: num(fields, 1)?, range: Range::new(start, end) })
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
        Some(FormatRequest { doc: num(fields, 0)?, version: num(fields, 1)?, tab_width: num(fields, 2)?, spaces })
    }
}

/// Where a location or an edit is: a file by its absolute path, or an open document by its id
/// (an untitled one).
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Target {
    Path(String),
    Doc(Doc),
}

impl Target {
    fn encode(&self) -> String {
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
        Location { target, range, text: String::new() }
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
            format!("{} {} {} {} {} {}\n", r.start.line, r.start.col, r.end.line, r.end.col, p.len(), t.len()).as_bytes(),
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
        Symbol { range, kind, depth, name: field(name, MAX_TEXT), detail: String::new() }
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
        TextEdit { range, text: text.to_string() }
    }
}

/// The edits of one file, all made against one text: the open document at `version`, or the
/// file on disk (`version` `None`). They must not overlap; insertions at one position keep their
/// order.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct FileEdit {
    pub target: Target,
    pub version: Option<i64>,
    pub edits: Vec<TextEdit>,
}

/// Edits across files (section 12g): rhun checks all of them before it changes anything, opens
/// the files that are not open, and applies each file's edits as one undo step. Nothing is saved.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct WorkspaceEdit {
    pub files: Vec<FileEdit>,
}

impl WorkspaceEdit {
    pub fn new() -> WorkspaceEdit {
        WorkspaceEdit::default()
    }

    /// Edits for an open document, made against its text at `version`.
    pub fn doc(mut self, doc: Doc, version: i64, edits: Vec<TextEdit>) -> WorkspaceEdit {
        self.files.push(FileEdit { target: Target::Doc(doc), version: Some(version), edits });
        self
    }

    /// Edits for a file by its path: the open document at `version`, or with `None` the file as
    /// it is on disk (rhun opens it).
    pub fn file(mut self, path: &str, version: Option<i64>, edits: Vec<TextEdit>) -> WorkspaceEdit {
        self.files.push(FileEdit { target: Target::Path(path.to_string()), version, edits });
        self
    }

    pub fn is_empty(&self) -> bool {
        self.files.iter().all(|f| f.edits.is_empty())
    }

    /// The body rhun reads, or why it cannot be one (over [`MAX_EDIT_BODY`], [`MAX_EDIT_FILES`]
    /// or [`MAX_EDITS`]): an edit cut short would leave the files half changed, so none is sent.
    pub fn encode(&self) -> Result<Vec<u8>, String> {
        if self.files.len() > MAX_EDIT_FILES {
            return Err(format!("{} files: at most {MAX_EDIT_FILES}", self.files.len()));
        }
        let edits: usize = self.files.iter().map(|f| f.edits.len()).sum();
        if edits > MAX_EDITS {
            return Err(format!("{edits} edits: at most {MAX_EDITS}"));
        }
        let mut out = format!("{}\n", self.files.len()).into_bytes();
        for f in &self.files {
            let p = f.target.encode();
            if p.is_empty() || p.len() > MAX_PATH {
                return Err(format!("a path of {} bytes: 1 to {MAX_PATH}", p.len()));
            }
            let v = f.version.map(|v| v.to_string()).unwrap_or_else(|| "-".to_string());
            out.extend_from_slice(format!("{} {} {}\n", p.len(), v, f.edits.len()).as_bytes());
            out.extend_from_slice(p.as_bytes());
            for e in &f.edits {
                let r = e.range;
                out.extend_from_slice(
                    format!("{} {} {} {} {}\n", r.start.line, r.start.col, r.end.line, r.end.col, e.text.len())
                        .as_bytes(),
                );
                out.extend_from_slice(e.text.as_bytes());
            }
            if out.len() > MAX_EDIT_BODY {
                return Err(format!("over {} bytes", MAX_EDIT_BODY));
            }
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
        CodeAction { kind: kind_word(kind, "quickfix"), title: field(title, MAX_TEXT), edit: None, data: Vec::new() }
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

/// The body for a list of code actions. Actions past [`MAX_ACTIONS`] or [`MAX_ACTIONS_BODY`],
/// and those whose edit cannot be encoded, are left out.
pub fn encode_actions(actions: &[CodeAction]) -> Vec<u8> {
    let mut recs = Vec::new();
    let mut n = 0;
    for a in actions {
        let edit = match &a.edit {
            Some(e) => match e.encode() {
                Ok(b) => b,
                Err(_) => continue,
            },
            None => Vec::new(),
        };
        let title = field(&a.title, MAX_TEXT);
        if title.is_empty() {
            continue;
        }
        let head = format!("{} {} {} {}\n", kind_word(&a.kind, "quickfix"), title.len(), edit.len(), a.data.len());
        if n == MAX_ACTIONS || recs.len() + head.len() + title.len() + edit.len() + a.data.len() + 16 > MAX_ACTIONS_BODY {
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
            Some(PosRequest { doc: 3, version: 7, at: Pos::new(10, 5) })
        );
        assert_eq!(ActionsRequest::parse(&fields("3 7 1 2 3 4")).unwrap().range, r(1, 2, 3, 4));
        let rn = RenameRequest::parse(&fields("3 7 1 2 invoked new_name")).unwrap();
        assert_eq!((rn.at, rn.name.as_str()), (Pos::new(1, 2), "new_name"));
        assert!(RenameRequest::parse(&fields("3 7 1 2 invoked")).is_none());
        assert_eq!(
            FormatRequest::parse(&fields("3 7 4 yes")),
            Some(FormatRequest { doc: 3, version: 7, tab_width: 4, spaces: true })
        );
        assert!(FormatRequest::parse(&fields("3 7 4 maybe")).is_none());
    }

    #[test]
    fn locations_are_framed_as_rhun_reads_them() {
        let body = encode_locations(&[
            Location::new(Target::Path("/a/b.rs".into()), r(1, 4, 1, 8)).text("  fn main() {\n"),
            Location::new(Target::Doc(7), r(2, 1, 2, 1)),
        ]);
        assert_eq!(body, b"2\n1 4 1 8 7 11\n/a/b.rsfn main() {2 1 2 1 2 0\n#7".to_vec());
        assert_eq!(encode_locations(&[]), b"0\n".to_vec());
    }

    #[test]
    fn symbols_are_framed_as_rhun_reads_them() {
        let body = encode_symbols(&[
            Symbol::new(r(1, 1, 3, 2), SymbolKind::Struct, 0, "Point"),
            Symbol::new(r(2, 5, 2, 11), SymbolKind::Field, 1, "x").detail("i32"),
        ]);
        assert_eq!(body, b"2\n1 1 3 2 struct 0 5 0\nPoint2 5 2 11 field 1 1 3\nxi32".to_vec());
        let many: Vec<Symbol> = (0..10001).map(|i| Symbol::new(r(1, 1, 1, 1), SymbolKind::Variable, 0, &i.to_string())).collect();
        assert!(encode_symbols(&many).starts_with(b"10000\n"));
    }

    #[test]
    fn workspace_edits_are_framed_and_checked() {
        let e = WorkspaceEdit::new()
            .doc(3, 9, vec![TextEdit::new(r(1, 1, 1, 4), "new")])
            .file("/p/x.rs", None, vec![TextEdit::new(r(2, 1, 2, 1), "a\n"), TextEdit::new(r(5, 1, 5, 3), "")]);
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
        let body = encode_actions(&[CodeAction::new(&"k".repeat(50), &long), CodeAction::new("source", "")]);
        assert!(body.starts_with(format!("1\n{} 999 0 0\n", "k".repeat(40)).as_bytes()));
        let far = Location::new(Target::Path(format!("/{}", "p".repeat(4096))), r(1, 1, 1, 1));
        let near = Location::new(Target::Path("/p".into()), r(1, 1, 1, 1));
        assert!(encode_locations(&[far.clone(), near]).starts_with(b"1\n"));
        assert!(encode_symbols(&[Symbol::new(r(1, 1, 1, 1), SymbolKind::Function, 0, "")]) == b"0\n");
        let e = WorkspaceEdit::new().file(&format!("/{}", "p".repeat(4096)), None, vec![]);
        assert!(e.encode().is_err());
    }
}
