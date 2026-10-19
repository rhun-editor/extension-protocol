//! Completion lists and signature help (sections 12b and 12c of the spec): what rhun asks, and
//! the answers, encoded the way rhun reads them. Protocol 1.1 adds the complete-items records,
//! their resolution and signature overloads (signature-list).

use crate::{Doc, Pos, Range};

/// Why rhun asks for a list.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Trigger {
    /// The user typed two characters of a word and paused.
    Typed,
    /// The user typed one of the manifest's trigger characters.
    Char(char),
    /// The user pressed the key (`ctrl+space`, `ctrl+shift+space`).
    Invoked,
    /// Typing went on in a list marked incomplete.
    Incomplete,
    /// Signature help is shown and the document or cursor changed.
    Update,
}

fn trigger(kind: &str, ch: &str) -> Option<Trigger> {
    Some(match kind {
        "typed" => Trigger::Typed,
        "trigger" => Trigger::Char(ch.chars().next()?),
        "invoked" => Trigger::Invoked,
        "incomplete" => Trigger::Incomplete,
        "update" => Trigger::Update,
        _ => return None,
    })
}

fn request(fields: &[String]) -> Option<(Doc, i64, Pos, Trigger)> {
    // DOC VERSION LINE COL KIND CHAR
    if fields.len() < 6 {
        return None;
    }
    let doc = fields[0].parse().ok()?;
    let version = fields[1].parse().ok()?;
    let at = Pos::new(fields[2].parse().ok()?, fields[3].parse().ok()?);
    Some((doc, version, at, trigger(&fields[4], &fields[5])?))
}

/// `ID complete DOC VERSION LINE COL KIND CHAR`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct CompleteRequest {
    pub doc: Doc,
    /// Every event about the document up to this version has been delivered.
    pub version: i64,
    /// The cursor.
    pub at: Pos,
    pub trigger: Trigger,
}

impl CompleteRequest {
    pub fn parse(fields: &[String]) -> Option<CompleteRequest> {
        let (doc, version, at, trigger) = request(fields)?;
        Some(CompleteRequest {
            doc,
            version,
            at,
            trigger,
        })
    }
}

/// `ID signature DOC VERSION LINE COL KIND CHAR`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct SignatureRequest {
    pub doc: Doc,
    pub version: i64,
    pub at: Pos,
    pub trigger: Trigger,
}

impl SignatureRequest {
    pub fn parse(fields: &[String]) -> Option<SignatureRequest> {
        let (doc, version, at, trigger) = request(fields)?;
        Some(SignatureRequest {
            doc,
            version,
            at,
            trigger,
        })
    }
}

/// What an item is; rhun shows an icon for it.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Kind {
    #[default]
    Text,
    Method,
    Function,
    Field,
    Variable,
    Class,
    Struct,
    Interface,
    Module,
    Property,
    Enum,
    Keyword,
    Snippet,
    Constant,
    Value,
    File,
    Folder,
}

impl Kind {
    pub fn word(self) -> &'static str {
        match self {
            Kind::Text => "text",
            Kind::Method => "method",
            Kind::Function => "function",
            Kind::Field => "field",
            Kind::Variable => "variable",
            Kind::Class => "class",
            Kind::Struct => "struct",
            Kind::Interface => "interface",
            Kind::Module => "module",
            Kind::Property => "property",
            Kind::Enum => "enum",
            Kind::Keyword => "keyword",
            Kind::Snippet => "snippet",
            Kind::Constant => "constant",
            Kind::Value => "value",
            Kind::File => "file",
            Kind::Folder => "folder",
        }
    }
}

/// One item of a list.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct CompletionItem {
    /// The range the insertion replaces, in the document at the request's version (usually the
    /// word before the cursor).
    pub range: Range,
    pub kind: Kind,
    /// What the list shows and typing filters on (one line).
    pub label: String,
    /// The text put in (may hold line breaks).
    pub insert: String,
    /// Shown beside the label (one line): a type, a package.
    pub detail: String,
    /// Shown beside the list when the item is selected.
    pub doc: String,
    /// Other replacements made with the item (an import): not overlapping the item's range or
    /// each other.
    pub edits: Vec<(Range, String)>,
    /// Where the cursor goes, in bytes into `insert` (a snippet's `$0`); `None`: after it.
    pub cursor: Option<usize>,
    /// Prepared fields. Prefer the `snippet` builder, which also assigns insertion text.
    pub snippet: Option<crate::PreparedSnippet>,
    /// Owned program metadata. Prefer the `snippet_program` builder.
    pub snippet_program: Option<crate::SnippetProgram>,
    // complete-items (protocol 1.1; rhun reads these only when it agreed the feature)
    /// Data rhun hands back in a complete-resolve request when the item is selected (at most
    /// [`MAX_RESOLVE`] bytes, else left out).
    pub resolve: Option<Vec<u8>>,
    /// The resolution may add edits: accepting the item waits for it.
    pub resolve_edits: bool,
    /// The range that replaces the rest of the word; `range` is then the insert range. Left out
    /// unless it starts where `range` does.
    pub replace: Option<Range>,
    /// Typing one of these while the item is selected accepts it first (printable ASCII, at most
    /// [`MAX_COMMIT`], each once; others are dropped).
    pub commit: String,
    /// Matched and ranked instead of the label (one line).
    pub filter: String,
    /// Drawn after the label in the muted color (one line): a signature, a module.
    pub suffix: String,
    /// Selected first when it passes the filter.
    pub preselect: bool,
    /// Drawn struck through.
    pub deprecated: bool,
    /// The lines of `insert` after the first get the insertion line's leading whitespace.
    pub adjust: bool,
    /// After accepting, rhun asks for signature help.
    pub after_signature: bool,
    /// After accepting, rhun asks for a new list.
    pub after_complete: bool,
}

/// The most bytes of an item's resolve data, and the most commit characters.
pub const MAX_RESOLVE: usize = 16 << 10;
pub const MAX_COMMIT: usize = 16;

/// The printable ASCII characters of `s`, at most [`MAX_COMMIT`], each once.
fn commit_chars(s: &str) -> String {
    let mut out = String::new();
    for c in s.bytes() {
        if out.len() == MAX_COMMIT {
            break;
        }
        if (0x21..=0x7e).contains(&c) && !out.as_bytes().contains(&c) {
            out.push(c as char);
        }
    }
    out
}

fn one_line(s: &str) -> String {
    s.chars()
        .map(|c| if c == '\n' || c == '\r' { ' ' } else { c })
        .collect()
}

impl CompletionItem {
    /// An item that replaces `range` with `label`.
    pub fn new(range: Range, kind: Kind, label: &str) -> CompletionItem {
        CompletionItem {
            range,
            kind,
            label: one_line(label),
            insert: label.to_string(),
            ..Default::default()
        }
    }

    pub fn insert(mut self, text: &str) -> CompletionItem {
        self.insert = text.to_string();
        self
    }

    pub fn detail(mut self, text: &str) -> CompletionItem {
        self.detail = one_line(text);
        self
    }

    pub fn doc(mut self, text: &str) -> CompletionItem {
        self.doc = text.to_string();
        self
    }

    pub fn edit(mut self, range: Range, text: &str) -> CompletionItem {
        self.edits.push((range, text.to_string()));
        self
    }

    /// The cursor `offset` bytes into the inserted text when the item is chosen (between
    /// parentheses, a snippet's `$0`). An offset past it or inside a character is left out.
    pub fn cursor(mut self, offset: usize) -> CompletionItem {
        self.cursor = Some(offset);
        self
    }

    /// Insert prepared native fields. Invalid metadata is omitted as a whole; text remains.
    pub fn snippet(mut self, snippet: crate::PreparedSnippet) -> CompletionItem {
        self.insert = snippet.text.clone();
        self.snippet = Some(snippet);
        self.snippet_program = None;
        self
    }

    /// Insert owned prepared fields and literal native choices.
    pub fn snippet_program(mut self, program: crate::SnippetProgram) -> CompletionItem {
        self.insert = program.snippet.text.clone();
        self.snippet = None;
        self.snippet_program = Some(program);
        self
    }

    /// Data for a complete-resolve request when the item is selected (complete-items); `edits`:
    /// the resolution may add edits, so accepting the item waits for it.
    pub fn resolve(mut self, data: &[u8], edits: bool) -> CompletionItem {
        self.resolve = Some(data.to_vec());
        self.resolve_edits = edits;
        self
    }

    /// The range that replaces the rest of the word (complete-items); it starts where the item's
    /// range does.
    pub fn replace(mut self, range: Range) -> CompletionItem {
        self.replace = Some(range);
        self
    }

    /// Characters that accept the item when typed (complete-items).
    pub fn commit(mut self, chars: &str) -> CompletionItem {
        self.commit = chars.to_string();
        self
    }

    /// What typing is matched against instead of the label (complete-items).
    pub fn filter(mut self, text: &str) -> CompletionItem {
        self.filter = one_line(text);
        self
    }

    /// Drawn after the label in the muted color (complete-items).
    pub fn suffix(mut self, text: &str) -> CompletionItem {
        self.suffix = one_line(text);
        self
    }

    /// The complete-items records, appended after the others: only those that are valid.
    fn item_extra(&self, extra: &mut Vec<u8>) {
        if let Some(data) = &self.resolve {
            if !data.is_empty() && data.len() <= MAX_RESOLVE {
                let edits = if self.resolve_edits { 1 } else { 0 };
                extra.extend_from_slice(format!("resolve {edits} {}\n", data.len()).as_bytes());
                extra.extend_from_slice(data);
            }
        }
        if let Some(r) = self.replace {
            if r.start == self.range.start {
                extra.extend_from_slice(
                    format!(
                        "replace {} {} {} {} 0\n",
                        r.start.line, r.start.col, r.end.line, r.end.col
                    )
                    .as_bytes(),
                );
            }
        }
        let c = commit_chars(&self.commit);
        if !c.is_empty() {
            extra.extend_from_slice(format!("commit {}\n{c}", c.len()).as_bytes());
        }
        for (word, text) in [("filter", &self.filter), ("suffix", &self.suffix)] {
            if !text.is_empty() {
                let t = one_line(text);
                extra.extend_from_slice(format!("{word} {}\n{t}", t.len()).as_bytes());
            }
        }
        for (on, record) in [
            (self.preselect, "preselect 0\n"),
            (self.deprecated, "deprecated 0\n"),
            (self.adjust, "adjust 0\n"),
            (self.after_signature, "after signature 0\n"),
            (self.after_complete, "after complete 0\n"),
        ] {
            if on {
                extra.extend_from_slice(record.as_bytes());
            }
        }
    }

    fn encode(&self, out: &mut Vec<u8>) {
        let mut extra = Vec::new();
        for (r, t) in &self.edits {
            extra.extend_from_slice(
                format!(
                    "edit {} {} {} {} {}\n",
                    r.start.line,
                    r.start.col,
                    r.end.line,
                    r.end.col,
                    t.len()
                )
                .as_bytes(),
            );
            extra.extend_from_slice(t.as_bytes());
        }
        if !self.doc.is_empty() {
            extra.extend_from_slice(format!("doc {}\n", self.doc.len()).as_bytes());
            extra.extend_from_slice(self.doc.as_bytes());
        }
        if let Some(c) = self.cursor {
            if c <= self.insert.len() && self.insert.is_char_boundary(c) {
                extra.extend_from_slice(format!("cursor {c} 0\n").as_bytes());
            }
        }
        if let Some(p) = &self.snippet_program {
            if p.snippet.text == self.insert && p.validate().is_ok() {
                extra.extend_from_slice(p.extra().as_bytes());
            }
        } else if let Some(s) = &self.snippet {
            if s.text == self.insert && s.validate().is_ok() {
                extra.extend_from_slice(s.extra().as_bytes());
            }
        }
        self.item_extra(&mut extra);
        let label = one_line(&self.label);
        let detail = one_line(&self.detail);
        let r = self.range;
        out.extend_from_slice(
            format!(
                "{} {} {} {} {} {} {} {} {}\n",
                r.start.line,
                r.start.col,
                r.end.line,
                r.end.col,
                self.kind.word(),
                label.len(),
                self.insert.len(),
                detail.len(),
                extra.len()
            )
            .as_bytes(),
        );
        out.extend_from_slice(label.as_bytes());
        out.extend_from_slice(self.insert.as_bytes());
        out.extend_from_slice(detail.as_bytes());
        out.extend_from_slice(&extra);
    }
}

/// The most items a list may hold, and its largest body.
pub const MAX_ITEMS: usize = 4096;
pub const MAX_BODY: usize = 1 << 20;

/// The answer to a [`CompleteRequest`].
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct CompletionList {
    pub items: Vec<CompletionItem>,
    /// Typing should ask again: the list was cut short.
    pub incomplete: bool,
}

impl CompletionList {
    pub fn new(items: Vec<CompletionItem>) -> CompletionList {
        CompletionList {
            items,
            incomplete: false,
        }
    }

    /// The body rhun reads, as text: empty when an item's resolve data is not UTF-8 (use
    /// [`CompletionList::encode_bytes`] then).
    pub fn encode(&self) -> String {
        String::from_utf8(self.encode_bytes()).unwrap_or_default()
    }

    /// The body rhun reads. Items past [`MAX_ITEMS`] or [`MAX_BODY`] are left out and the list
    /// is marked incomplete, so rhun asks again as the user types.
    pub fn encode_bytes(&self) -> Vec<u8> {
        let mut items = Vec::new();
        let mut n = 0;
        let mut incomplete = self.incomplete;
        for it in &self.items {
            let mut one = Vec::new();
            it.encode(&mut one);
            if n == MAX_ITEMS || items.len() + one.len() + 32 > MAX_BODY {
                incomplete = true;
                break;
            }
            items.extend_from_slice(&one);
            n += 1;
        }
        let mut out = format!("{} {}\n", n, if incomplete { 1 } else { 0 }).into_bytes();
        out.extend_from_slice(&items);
        out
    }
}

/// `ID complete-resolve DOC VERSION LEN` and the selected item's resolve data (complete-items).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ResolveRequest {
    /// rhun's id for it: selecting another item withdraws it (`cancel`).
    pub id: u64,
    pub doc: Doc,
    /// The version the list was asked at: the resolution's edits are in the document then.
    pub version: i64,
    /// The item's resolve data.
    pub data: Vec<u8>,
}

impl ResolveRequest {
    pub fn parse(id: u64, fields: &[String], body: &[u8]) -> Option<ResolveRequest> {
        if fields.len() != 3 || fields[2].parse::<usize>().ok()? != body.len() {
            return None;
        }
        Some(ResolveRequest {
            id,
            doc: fields[0].parse().ok()?,
            version: fields[1].parse().ok()?,
            data: body.to_vec(),
        })
    }
}

/// What resolving adds to the selected item (complete-items): documentation, a detail that
/// replaces the item's, edits (in the document at the list's version, apart from the item's range
/// and its other edits), and what rhun asks for after it is accepted.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Resolution {
    pub doc: String,
    pub detail: Option<String>,
    pub edits: Vec<crate::TextEdit>,
    pub after_signature: bool,
    pub after_complete: bool,
}

impl Resolution {
    /// The answer rhun reads: EXTRA records only (detail, doc, edit, after).
    pub fn encode(&self) -> String {
        let mut out = String::new();
        if let Some(d) = &self.detail {
            let d = one_line(d);
            out += &format!("detail {}\n{d}", d.len());
        }
        if !self.doc.is_empty() {
            out += &format!("doc {}\n{}", self.doc.len(), self.doc);
        }
        for e in &self.edits {
            let r = e.range;
            out += &format!(
                "edit {} {} {} {} {}\n{}",
                r.start.line,
                r.start.col,
                r.end.line,
                r.end.col,
                e.text.len(),
                e.text
            );
        }
        if self.after_signature {
            out += "after signature 0\n";
        }
        if self.after_complete {
            out += "after complete 0\n";
        }
        out
    }
}

/// The most overloads a signature list holds.
pub const MAX_SIGNATURES: usize = 32;

/// One overload of a [`SignatureList`].
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SignatureInfo {
    /// One line: `fn add(a: i32, b: i32) -> i32`.
    pub label: String,
    /// The active parameter's bytes in `label`, half-open; `None` for none.
    pub active: Option<(usize, usize)>,
    /// Plain text (Markdown with `markdown` agreed).
    pub doc: String,
}

/// The answer to `signatures` (signature-list): every overload, and the one shown first.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SignatureList {
    pub signatures: Vec<SignatureInfo>,
    /// Zero-based; another value shows the first.
    pub active: usize,
}

impl SignatureList {
    /// The body rhun reads: `N ACTIVE` and N records, at most [`MAX_SIGNATURES`]; an invalid
    /// active parameter is `-1 -1`. No overloads is no body, which hides the help; past 64 KB the
    /// documentation goes, then overloads, until it fits.
    pub fn encode(&self) -> String {
        let sigs = &self.signatures[..self.signatures.len().min(MAX_SIGNATURES)];
        if sigs.is_empty() {
            return String::new();
        }
        let active = if self.active < sigs.len() {
            self.active
        } else {
            0
        };
        let mut out = format!("{} {active}\n", sigs.len());
        for s in sigs {
            let label = one_line(&s.label);
            let (a, b) = match s.active {
                Some((a, b)) if a <= b && b <= label.len() => (a as i64, b as i64),
                _ => (-1, -1),
            };
            out += &format!("{a} {b} {} {}\n{label}{}", label.len(), s.doc.len(), s.doc);
        }
        if out.len() <= 65536 {
            return out;
        }
        if sigs.len() == 1 && sigs[0].doc.is_empty() {
            return String::new(); // a signature that does not fit on its own
        }
        let fewer = sigs[..(sigs.len() / 2).max(1)]
            .iter()
            .map(|s| SignatureInfo {
                doc: String::new(),
                ..s.clone()
            })
            .collect();
        SignatureList {
            signatures: fewer,
            active: 0,
        }
        .encode()
    }
}

/// The answer to a [`SignatureRequest`].
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SignatureHelp {
    /// One line: `fn add(a: i32, b: i32) -> i32`.
    pub signature: String,
    /// The active parameter's bytes in `signature`, half-open; `None` for none.
    pub active: Option<(usize, usize)>,
    /// Plain text, any number of lines.
    pub doc: String,
}

impl SignatureHelp {
    pub fn new(signature: &str) -> SignatureHelp {
        SignatureHelp {
            signature: one_line(signature),
            ..Default::default()
        }
    }

    /// Marks the parameter `param` (its text, found in the signature after the first `(`).
    pub fn active_param(mut self, param: &str) -> SignatureHelp {
        let from = self.signature.find('(').map(|i| i + 1).unwrap_or(0);
        if let Some(i) = self.signature[from..].find(param) {
            self.active = Some((from + i, from + i + param.len()));
        }
        self
    }

    pub fn doc(mut self, text: &str) -> SignatureHelp {
        self.doc = text.to_string();
        self
    }

    /// The body rhun reads, at most 64 KB: the documentation is cut (at a character) to fit; a
    /// signature that does not fit on its own is no answer, which hides the help.
    pub fn encode(&self) -> String {
        let sig = one_line(&self.signature);
        let (a, b) = match self.active {
            Some((a, b)) if a <= b && b <= sig.len() => (a as i64, b as i64),
            _ => (-1, -1),
        };
        let head = format!("{a} {b}\n{sig}\n");
        if head.len() > 65536 {
            return String::new();
        }
        let mut cut = (65536 - head.len()).min(self.doc.len());
        while !self.doc.is_char_boundary(cut) {
            cut -= 1;
        }
        head + &self.doc[..cut]
    }
}

/// The start of the word that ends at `col` on `line` (letters, digits, `_`): the usual range
/// an item replaces. `line_text` is the line's text without its line break; columns are 1-based
/// bytes.
pub fn word_range(line: u32, line_text: &str, col: u32) -> Range {
    let end = ((col.max(1) - 1) as usize).min(line_text.len());
    let bytes = line_text.as_bytes();
    let mut start = end;
    while start > 0 {
        let c = bytes[start - 1];
        if c.is_ascii_alphanumeric() || c == b'_' || c >= 0x80 {
            start -= 1;
        } else {
            break;
        }
    }
    while !line_text.is_char_boundary(start) {
        start += 1;
    }
    Range::new(
        Pos::new(line, start as u32 + 1),
        Pos::new(line, end as u32 + 1),
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fields(s: &str) -> Vec<String> {
        s.split(' ').map(str::to_string).collect()
    }

    #[test]
    fn requests_parse() {
        let r = CompleteRequest::parse(&fields("3 7 10 5 trigger .")).unwrap();
        assert_eq!(
            (r.doc, r.version, r.at, r.trigger),
            (3, 7, Pos::new(10, 5), Trigger::Char('.'))
        );
        assert!(CompleteRequest::parse(&fields("3 7 10 5 sideways -")).is_none());
        let s = SignatureRequest::parse(&fields("1 2 3 4 update -")).unwrap();
        assert_eq!(s.trigger, Trigger::Update);
    }

    #[test]
    fn a_list_is_framed_as_rhun_reads_it() {
        let r = Range::new(Pos::new(1, 1), Pos::new(1, 3));
        let list = CompletionList::new(vec![
            CompletionItem::new(r, Kind::Function, "print")
                .insert("print()")
                .detail("fn"),
            CompletionItem::new(r, Kind::Module, "fmt")
                .doc("Formatting.")
                .edit(Range::default(), "import fmt\n"),
        ]);
        assert_eq!(
            list.encode(),
            "2 0\n1 1 1 3 function 5 7 2 0\nprintprint()fn\
             1 1 1 3 module 3 3 0 45\nfmtfmtedit 0 0 0 0 11\nimport fmt\ndoc 11\nFormatting."
        );
    }

    #[test]
    fn an_item_can_place_the_cursor() {
        let r = Range::default();
        let list = CompletionList::new(vec![
            CompletionItem::new(r, Kind::Function, "f")
                .insert("f()")
                .cursor(2),
            CompletionItem::new(r, Kind::Function, "g")
                .insert("é")
                .cursor(1),
        ]);
        assert_eq!(
            list.encode(),
            "2 0\n0 0 0 0 function 1 3 0 11\nff()cursor 2 0\n0 0 0 0 function 1 2 0 0\ngé"
        );
    }

    #[test]
    fn a_long_list_is_cut_and_marked_incomplete() {
        let r = Range::default();
        let items = (0..5000)
            .map(|i| CompletionItem::new(r, Kind::Text, &format!("w{i}")))
            .collect();
        let body = CompletionList::new(items).encode();
        assert!(body.starts_with("4096 1\n"));
    }

    #[test]
    fn signature_help_marks_its_parameter() {
        let s = SignatureHelp::new("fn add(a: i32, b: i32)")
            .active_param("b: i32")
            .doc("Adds.");
        assert_eq!(s.encode(), "15 21\nfn add(a: i32, b: i32)\nAdds.");
        assert_eq!(SignatureHelp::new("x").encode(), "-1 -1\nx\n");
        let long = SignatureHelp::new("f()").doc(&"é".repeat(40000)).encode();
        assert!(long.len() <= 65536 && long.starts_with("-1 -1\nf()\n"));
        assert_eq!(SignatureHelp::new(&"x".repeat(70000)).encode(), "");
    }

    fn rg(l1: u32, c1: u32, l2: u32, c2: u32) -> Range {
        Range::new(Pos::new(l1, c1), Pos::new(l2, c2))
    }

    #[test]
    fn complete_items_records_follow_the_others() {
        let mut it = CompletionItem::new(rg(2, 5, 2, 7), Kind::Text, "print")
            .resolve(b"r1", true)
            .replace(rg(2, 5, 2, 9))
            .commit("..(\x01")
            .filter("prt")
            .suffix("(s)");
        it.preselect = true;
        it.deprecated = true;
        it.adjust = true;
        it.after_signature = true;
        let extra =
            "resolve 1 2\nr1replace 2 5 2 9 0\ncommit 2\n.(filter 3\nprtsuffix 3\n(s)preselect 0\n\
                     deprecated 0\nadjust 0\nafter signature 0\n";
        assert_eq!(
            CompletionList::new(vec![it.clone()]).encode(),
            format!("1 0\n2 5 2 7 text 5 5 0 {}\nprintprint{extra}", extra.len())
        );
        // what is not valid is left out: a replace range elsewhere, resolve data past 16 KiB,
        // commit characters that are not printable ASCII
        it = CompletionItem::new(rg(1, 1, 1, 2), Kind::Text, "x")
            .replace(rg(1, 2, 1, 3))
            .resolve(&[0; MAX_RESOLVE + 1], false)
            .commit(" \té");
        it.after_complete = true;
        assert_eq!(
            CompletionList::new(vec![it]).encode(),
            "1 0\n1 1 1 2 text 1 1 0 17\nxxafter complete 0\n"
        );
        assert_eq!(commit_chars(&"abc".repeat(10)), "abc");
        assert_eq!(
            commit_chars("abcdefghijklmnopqrstuvwxyz"),
            "abcdefghijklmnop"
        );
        // resolve data that is not UTF-8 needs the bytes
        let bin = CompletionList::new(vec![
            CompletionItem::new(Range::default(), Kind::Text, "b").resolve(&[0xff], false)
        ]);
        assert_eq!(bin.encode(), "");
        assert!(bin.encode_bytes().ends_with(b"resolve 0 1\n\xff"));
    }

    #[test]
    fn resolve_requests_and_answers() {
        let r = ResolveRequest::parse(3, &fields("4 2 2"), b"r1").unwrap();
        assert_eq!(
            (r.id, r.doc, r.version, r.data.as_slice()),
            (3, 4, 2, &b"r1"[..])
        );
        assert!(ResolveRequest::parse(3, &fields("4 2 3"), b"r1").is_none());
        assert!(ResolveRequest::parse(3, &fields("4 2"), b"").is_none());
        let res = Resolution {
            doc: "Docs r1".into(),
            detail: Some("fn\nprint()".into()),
            edits: vec![crate::TextEdit::new(rg(1, 1, 1, 1), "use x;\n")],
            after_signature: true,
            after_complete: true,
        };
        assert_eq!(
            res.encode(),
            "detail 10\nfn print()doc 7\nDocs r1edit 1 1 1 1 7\nuse x;\n\
             after signature 0\nafter complete 0\n"
        );
        assert_eq!(Resolution::default().encode(), "");
    }

    #[test]
    fn signature_lists() {
        let sig = |label: &str, active: Option<(usize, usize)>, doc: &str| SignatureInfo {
            label: label.into(),
            active,
            doc: doc.into(),
        };
        let l = SignatureList {
            signatures: vec![
                sig("f(a)", Some((2, 3)), ""),
                sig("f(a, b)", Some((5, 6)), "two"),
                sig("f()", Some((9, 12)), ""),
            ],
            active: 1,
        };
        assert_eq!(
            l.encode(),
            "3 1\n2 3 4 0\nf(a)5 6 7 3\nf(a, b)two-1 -1 3 0\nf()"
        );
        assert_eq!(SignatureList::default().encode(), "");
        let many = SignatureList {
            signatures: (0..40).map(|_| sig("g()", None, "")).collect(),
            active: 35,
        };
        assert!(many.encode().starts_with("32 0\n-1 -1 3 0\ng()"));
        // past 64 KB the documentation goes and then overloads, until it fits
        let big = SignatureList {
            signatures: (0..4)
                .map(|_| sig("h()", None, &"d".repeat(30000)))
                .collect(),
            active: 2,
        };
        assert_eq!(big.encode(), "2 0\n-1 -1 3 0\nh()-1 -1 3 0\nh()");
        let huge = SignatureList {
            signatures: vec![sig(&"x".repeat(70000), None, "")],
            active: 0,
        };
        assert_eq!(huge.encode(), "");
    }

    #[test]
    fn the_word_before_the_cursor() {
        assert_eq!(
            word_range(4, "let foo_bar", 12),
            Range::new(Pos::new(4, 5), Pos::new(4, 12))
        );
        assert_eq!(
            word_range(1, "a.", 3),
            Range::new(Pos::new(1, 3), Pos::new(1, 3))
        );
    }
}
