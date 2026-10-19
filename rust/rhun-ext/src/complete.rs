//! Completion lists and signature help (sections 12b and 12c of the spec): what rhun asks, and
//! the answers, encoded the way rhun reads them.

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

    /// The body rhun reads. Items past [`MAX_ITEMS`] or [`MAX_BODY`] are left out and the list
    /// is marked incomplete, so rhun asks again as the user types.
    pub fn encode(&self) -> String {
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
        // labels and texts came in as String: the body is UTF-8
        String::from_utf8(out).unwrap_or_default()
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
