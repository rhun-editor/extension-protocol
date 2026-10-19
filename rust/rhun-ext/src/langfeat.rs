//! Protocol 1.1's hover-query, selection-ranges, hierarchy, workspace-symbols, document-links and
//! file-diagnostics (`v1.1/v1.1.md`, "Language features"): what rhun asks, the answers encoded as
//! the Go package encodes them, and [`Ext::file_diagnostics`].

use crate::lang::{field, MAX_BODY, MAX_PATH, MAX_RECORDS, MAX_TEXT};
use crate::{Diagnostic, Doc, Error, Ext, Feature, Pos, Range, Stream, SymbolKind, Target};

fn num<T: std::str::FromStr>(fields: &[String], i: usize) -> Option<T> {
    fields.get(i)?.parse().ok()
}

fn range_fields(r: Range) -> String {
    format!(
        "{} {} {} {}",
        r.start.line, r.start.col, r.end.line, r.end.col
    )
}

/// Whether a range's start is not after its end.
fn well_formed(r: Range) -> bool {
    r.start <= r.end
}

/// Whether `a` contains `b`.
fn contains(a: Range, b: Range) -> bool {
    a.start <= b.start && b.end <= a.end
}

/// `s` cut to at most `max` bytes, at a character (line feeds kept).
fn cut(s: &str, max: usize) -> &str {
    if s.len() <= max {
        return s;
    }
    let mut n = max;
    while !s.is_char_boundary(n) {
        n -= 1;
    }
    &s[..n]
}

fn local(code: &str, text: &str) -> Error {
    Error {
        code: code.into(),
        text: text.into(),
    }
}

/// The count line and the records.
fn counted(n: usize, recs: &[u8]) -> Vec<u8> {
    let mut out = format!("{n}\n").into_bytes();
    out.extend_from_slice(recs);
    out
}

// ---- hover-query

/// `ID hover-query DOC VERSION LINE COL KIND -` (feature `hover-query`).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HoverRequest {
    /// rhun's id for it: a `cancel` event names it.
    pub id: u64,
    pub doc: Doc,
    pub version: i64,
    pub at: Pos,
    /// `pointer` (the pointer rested on text for half a second) or `invoked` (Show Hover).
    pub kind: String,
}

impl HoverRequest {
    pub fn parse(id: u64, fields: &[String]) -> Option<HoverRequest> {
        let kind = fields.get(4).filter(|k| !k.is_empty())?.clone();
        Some(HoverRequest {
            id,
            doc: num(fields, 0)?,
            version: num(fields, 1)?,
            at: Pos::new(num(fields, 2)?, num(fields, 3)?),
            kind,
        })
    }
}

/// The most bytes of a hover answer: [`Hover::encode`] cuts the text at a character to fit.
pub const MAX_HOVER_BODY: usize = 64 << 10;

/// A hover-query answer: the text (plain, or Markdown with `markdown` agreed) and the range it
/// describes, the default range (`0 0 0 0`) for the position only.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Hover {
    pub range: Range,
    pub text: String,
}

impl Hover {
    pub fn new(range: Range, text: &str) -> Hover {
        Hover {
            range,
            text: text.to_string(),
        }
    }

    /// The answer's body; empty (nothing to say) for an empty text. A range whose end comes
    /// before its start is sent as `0 0 0 0`.
    pub fn encode(&self) -> Vec<u8> {
        if self.text.is_empty() {
            return Vec::new();
        }
        let r = if well_formed(self.range) {
            self.range
        } else {
            Range::default()
        };
        let head = format!("{}\n", range_fields(r));
        let mut out = head.clone().into_bytes();
        out.extend_from_slice(cut(&self.text, MAX_HOVER_BODY - head.len()).as_bytes());
        out
    }
}

// ---- selection-ranges

/// `ID selection-ranges DOC VERSION N LEN` and the N positions (feature `selection-ranges`).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SelectionRangesRequest {
    /// rhun's id for it: a `cancel` event names it.
    pub id: u64,
    pub doc: Doc,
    pub version: i64,
    /// Each selection's cursor, 1 to 64.
    pub positions: Vec<Pos>,
}

/// The caps of a selection-ranges answer: groups (one per position), ranges in a group, bytes.
pub const MAX_SELECTION_GROUPS: usize = 64;
pub const MAX_SELECTION_RANGES: usize = 64;
pub const MAX_SELECTION_BODY: usize = 64 << 10;

impl SelectionRangesRequest {
    pub fn parse(id: u64, fields: &[String], body: &[u8]) -> Option<SelectionRangesRequest> {
        if fields.len() != 4 || fields[3].parse::<usize>().ok()? != body.len() {
            return None;
        }
        let n: usize = num(fields, 2)?;
        if !(1..=MAX_SELECTION_GROUPS).contains(&n) {
            return None;
        }
        let text = std::str::from_utf8(body).ok()?;
        let lines: Vec<&str> = text
            .strip_suffix('\n')
            .unwrap_or(text)
            .split('\n')
            .collect();
        if lines.len() != n {
            return None;
        }
        let mut positions = Vec::with_capacity(n);
        for l in lines {
            let (a, b) = l.split_once(' ')?;
            positions.push(Pos::new(a.parse().ok()?, b.parse().ok()?));
        }
        Some(SelectionRangesRequest {
            id,
            doc: num(fields, 0)?,
            version: num(fields, 1)?,
            positions,
        })
    }
}

/// Ranges as rhun takes them: an equal range is skipped, and the first that runs backwards or
/// does not contain the one before ends the chain; at most [`MAX_SELECTION_RANGES`].
pub fn selection_chain(ranges: &[Range]) -> Vec<Range> {
    let mut out: Vec<Range> = Vec::new();
    for &r in ranges {
        if out.len() == MAX_SELECTION_RANGES || !well_formed(r) {
            break;
        }
        if let Some(&last) = out.last() {
            if r == last {
                continue;
            }
            if !contains(r, last) {
                break;
            }
        }
        out.push(r);
    }
    out
}

/// The selection-ranges answer for the request's positions (`at`) and a group of ranges per
/// position, innermost first, each made a [`selection_chain`]. A group left empty becomes the
/// empty range at its position (it expands nothing), unless no group after it has ranges: the
/// answer then ends before it. Groups past the positions or [`MAX_SELECTION_GROUPS`] are left out,
/// and the outermost ranges of the longest groups go until the body fits [`MAX_SELECTION_BODY`].
pub fn encode_selection_ranges(at: &[Pos], groups: &[Vec<Range>]) -> Vec<u8> {
    let n = at.len().min(groups.len()).min(MAX_SELECTION_GROUPS);
    let mut chains: Vec<Vec<Range>> = groups[..n].iter().map(|g| selection_chain(g)).collect();
    while chains.last().is_some_and(|c| c.is_empty()) {
        chains.pop();
    }
    for (i, c) in chains.iter_mut().enumerate() {
        if c.is_empty() {
            c.push(Range::new(at[i], at[i]));
        }
    }
    let encode = |m: usize| -> Vec<u8> {
        let mut b = String::new();
        for c in &chains {
            let c = &c[..c.len().min(m)];
            b.push_str(&format!("{}\n", c.len()));
            for &r in c {
                b.push_str(&range_fields(r));
                b.push('\n');
            }
        }
        b.into_bytes()
    };
    // the most ranges per group that fit (one per group always does)
    let (mut lo, mut hi) = (1, MAX_SELECTION_RANGES);
    while lo < hi {
        let m = (lo + hi).div_ceil(2);
        if encode(m).len() <= MAX_SELECTION_BODY {
            lo = m;
        } else {
            hi = m - 1;
        }
    }
    encode(lo)
}

// ---- hierarchy

/// The caps of hierarchy answers: roots and their bytes; items or calls of one answer or part
/// and their bytes; an item's data; a call's sites.
pub const MAX_HIERARCHY_ROOTS: usize = 64;
pub const MAX_HIERARCHY_ROOTS_BODY: usize = 256 << 10;
pub const MAX_HIERARCHY_RECORDS: usize = 1000;
pub const MAX_HIERARCHY_BODY: usize = 1 << 20;
pub const MAX_HIERARCHY_DATA: usize = 16 << 10;
pub const MAX_CALL_SITES: usize = 1000;

/// A symbol of a call or type hierarchy (feature `hierarchy`): `range` is the whole symbol in
/// `target` and `selection` its name inside it (the whole range when it is not inside); `name`
/// and `detail` are one line each (cut at [`MAX_TEXT`] bytes); `data`, at most
/// [`MAX_HIERARCHY_DATA`] bytes, comes back verbatim when rhun asks about the item.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HierarchyItem {
    pub kind: SymbolKind,
    pub name: String,
    pub detail: String,
    pub target: Target,
    pub range: Range,
    pub selection: Range,
    pub data: Vec<u8>,
}

impl HierarchyItem {
    /// An item whose name range is its whole range, without detail or data.
    pub fn new(kind: SymbolKind, name: &str, target: Target, range: Range) -> HierarchyItem {
        HierarchyItem {
            kind,
            name: name.to_string(),
            detail: String::new(),
            target,
            range,
            selection: range,
            data: Vec::new(),
        }
    }

    pub fn detail(mut self, detail: &str) -> HierarchyItem {
        self.detail = detail.to_string();
        self
    }

    pub fn selection(mut self, selection: Range) -> HierarchyItem {
        self.selection = selection;
        self
    }

    pub fn data(mut self, data: &[u8]) -> HierarchyItem {
        self.data = data.to_vec();
        self
    }

    /// Its header (without the line end) and bytes, or nothing when rhun would refuse it: no
    /// name, a path over [`MAX_PATH`], data over [`MAX_HIERARCHY_DATA`], a backward range.
    fn record(&self) -> Option<(String, Vec<u8>)> {
        let (name, detail, path) = (
            field(&self.name, MAX_TEXT),
            field(&self.detail, MAX_TEXT),
            self.target.encode(),
        );
        if name.is_empty()
            || path.len() > MAX_PATH
            || self.data.len() > MAX_HIERARCHY_DATA
            || !well_formed(self.range)
        {
            return None;
        }
        let sel = if well_formed(self.selection) && contains(self.range, self.selection) {
            self.selection
        } else {
            self.range
        };
        let head = format!(
            "{} {} {} {} {} {} {}",
            self.kind.word(),
            range_fields(self.range),
            range_fields(sel),
            path.len(),
            name.len(),
            detail.len(),
            self.data.len()
        );
        let mut b = Vec::with_capacity(path.len() + name.len() + detail.len() + self.data.len());
        b.extend_from_slice(path.as_bytes());
        b.extend_from_slice(name.as_bytes());
        b.extend_from_slice(detail.as_bytes());
        b.extend_from_slice(&self.data);
        Some((head, b))
    }
}

/// An item of a call hierarchy and its call sites (1 to [`MAX_CALL_SITES`]): for calls-in, the
/// caller and where in its file it calls the item asked about; for calls-out, the callee and where
/// in the file of the item asked about it is called.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Call {
    pub item: HierarchyItem,
    pub sites: Vec<Range>,
}

impl Call {
    pub fn new(item: HierarchyItem, sites: Vec<Range>) -> Call {
        Call { item, sites }
    }
}

fn encode_items(items: &[HierarchyItem], max_n: usize, max_body: usize) -> Vec<u8> {
    let mut recs = Vec::new();
    let mut n = 0;
    for it in items {
        let Some((head, b)) = it.record() else {
            continue;
        };
        if n == max_n || recs.len() + head.len() + 1 + b.len() + 16 > max_body {
            break;
        }
        recs.extend_from_slice(head.as_bytes());
        recs.push(b'\n');
        recs.extend_from_slice(&b);
        n += 1;
    }
    counted(n, &recs)
}

/// The body of a supertypes or subtypes answer or part: items rhun would refuse (see
/// [`HierarchyItem`]) are left out, as those past [`MAX_HIERARCHY_RECORDS`] or
/// [`MAX_HIERARCHY_BODY`].
pub fn encode_hierarchy_items(items: &[HierarchyItem]) -> Vec<u8> {
    encode_items(items, MAX_HIERARCHY_RECORDS, MAX_HIERARCHY_BODY)
}

/// The body of a call-hierarchy or type-hierarchy answer: as [`encode_hierarchy_items`], at most
/// [`MAX_HIERARCHY_ROOTS`] items and [`MAX_HIERARCHY_ROOTS_BODY`] bytes.
pub fn encode_hierarchy_roots(items: &[HierarchyItem]) -> Vec<u8> {
    encode_items(items, MAX_HIERARCHY_ROOTS, MAX_HIERARCHY_ROOTS_BODY)
}

/// The body of a calls-in or calls-out answer or part: items as in [`encode_hierarchy_items`],
/// each with its call sites (backward ones, and those past [`MAX_CALL_SITES`], are left out); a
/// call without sites is left out.
pub fn encode_calls(calls: &[Call]) -> Vec<u8> {
    let mut recs = Vec::new();
    let mut n = 0;
    for c in calls {
        let Some((head, b)) = c.item.record() else {
            continue;
        };
        let mut sites = String::new();
        let mut r = 0;
        for &s in &c.sites {
            if r == MAX_CALL_SITES {
                break;
            }
            if well_formed(s) {
                sites.push_str(&range_fields(s));
                sites.push('\n');
                r += 1;
            }
        }
        if r == 0 {
            continue;
        }
        let head = format!("{head} {r}\n");
        if n == MAX_HIERARCHY_RECORDS
            || recs.len() + head.len() + b.len() + sites.len() + 16 > MAX_HIERARCHY_BODY
        {
            break;
        }
        recs.extend_from_slice(head.as_bytes());
        recs.extend_from_slice(&b);
        recs.extend_from_slice(sites.as_bytes());
        n += 1;
    }
    counted(n, &recs)
}

/// The data of `calls-in LEN` + DATA (and calls-out, supertypes, subtypes).
pub(crate) fn item_data<'a>(fields: &[String], body: &'a [u8]) -> Option<&'a [u8]> {
    (fields.len() == 1 && fields[0].parse::<usize>().ok()? == body.len()).then_some(body)
}

// ---- workspace-symbols

/// `ID workspace-symbols MAX LEN` and the query (feature `workspace-symbols`).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct WorkspaceSymbolsRequest {
    /// rhun's id for it: a `cancel` event names it (the query changed).
    pub id: u64,
    /// One line, at most 256 bytes, maybe empty.
    pub query: String,
    /// A hint of how many symbols are wanted.
    pub max: u32,
}

impl WorkspaceSymbolsRequest {
    pub fn parse(id: u64, fields: &[String], body: &[u8]) -> Option<WorkspaceSymbolsRequest> {
        if fields.len() != 2 || fields[1].parse::<usize>().ok()? != body.len() {
            return None;
        }
        Some(WorkspaceSymbolsRequest {
            id,
            query: String::from_utf8_lossy(body).into_owned(),
            max: num(fields, 0)?,
        })
    }
}

/// A symbol anywhere in the project: `range` in `target`; `name` and `detail` (its container)
/// one line each, cut at [`MAX_TEXT`] bytes.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct WorkspaceSymbol {
    pub target: Target,
    pub range: Range,
    pub kind: SymbolKind,
    pub name: String,
    pub detail: String,
}

impl WorkspaceSymbol {
    pub fn new(target: Target, range: Range, kind: SymbolKind, name: &str) -> WorkspaceSymbol {
        WorkspaceSymbol {
            target,
            range,
            kind,
            name: name.to_string(),
            detail: String::new(),
        }
    }

    pub fn detail(mut self, detail: &str) -> WorkspaceSymbol {
        self.detail = detail.to_string();
        self
    }
}

/// The body of a workspace-symbols answer or part. Symbols without a name or with a path over
/// [`MAX_PATH`] are left out, as those past [`MAX_RECORDS`] or [`MAX_BODY`].
pub fn encode_workspace_symbols(syms: &[WorkspaceSymbol]) -> Vec<u8> {
    let mut recs = Vec::new();
    let mut n = 0;
    for s in syms {
        let (name, detail, path) = (
            field(&s.name, MAX_TEXT),
            field(&s.detail, MAX_TEXT),
            s.target.encode(),
        );
        if name.is_empty() || path.len() > MAX_PATH {
            continue;
        }
        let head = format!(
            "{} {} {} {} {}\n",
            range_fields(s.range),
            s.kind.word(),
            path.len(),
            name.len(),
            detail.len()
        );
        if n == MAX_RECORDS
            || recs.len() + head.len() + path.len() + name.len() + detail.len() + 16 > MAX_BODY
        {
            break;
        }
        recs.extend_from_slice(head.as_bytes());
        recs.extend_from_slice(path.as_bytes());
        recs.extend_from_slice(name.as_bytes());
        recs.extend_from_slice(detail.as_bytes());
        n += 1;
    }
    counted(n, &recs)
}

impl Stream {
    /// Streams calls (calls-in, calls-out).
    pub fn part_calls(&self, calls: &[Call]) -> Result<(), Error> {
        self.part(&encode_calls(calls))
    }

    /// Streams items (supertypes, subtypes).
    pub fn part_hierarchy_items(&self, items: &[HierarchyItem]) -> Result<(), Error> {
        self.part(&encode_hierarchy_items(items))
    }

    /// Streams workspace symbols.
    pub fn part_workspace_symbols(&self, syms: &[WorkspaceSymbol]) -> Result<(), Error> {
        self.part(&encode_workspace_symbols(syms))
    }
}

// ---- document-links

/// `ID document-links DOC VERSION`: a request about a whole document.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct DocRequest {
    /// rhun's id for it: a `cancel` event names it.
    pub id: u64,
    pub doc: Doc,
    pub version: i64,
}

impl DocRequest {
    pub fn parse(id: u64, fields: &[String]) -> Option<DocRequest> {
        if fields.len() != 2 {
            return None;
        }
        Some(DocRequest {
            id,
            doc: num(fields, 0)?,
            version: num(fields, 1)?,
        })
    }
}

/// Where a link goes: an http or https URL, a file by its absolute path at a line and column (0
/// for none), or an open document.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum LinkTarget {
    Url(String),
    Path(String, Pos),
    Doc(Doc),
}

/// Whether `s` is an http or https URL of at most `max` bytes without white space or control
/// characters.
fn valid_url(s: &str, max: usize) -> bool {
    let l = s.to_ascii_lowercase();
    let rest = l
        .strip_prefix("http://")
        .or_else(|| l.strip_prefix("https://"));
    rest.is_some_and(|r| !r.is_empty())
        && s.len() <= max
        && s.bytes().all(|b| b > b' ' && b != 0x7f)
}

/// Whether `p` is an absolute path: `/…`, a drive (`C:\` or `C:/`) or `\\server\share`.
fn abs_path(p: &str) -> bool {
    let b = p.as_bytes();
    p.starts_with('/')
        || p.starts_with("\\\\")
        || b.len() >= 3
            && b[0].is_ascii_alphabetic()
            && b[1] == b':'
            && (b[2] == b'\\' || b[2] == b'/')
}

/// Whether a target names a file rhun takes: an absolute path of at most [`MAX_PATH`] bytes on
/// one line, or an open document.
fn valid_target(t: &Target) -> bool {
    match t {
        Target::Path(p) => p.len() <= MAX_PATH && abs_path(p) && !p.contains(['\n', '\r']),
        Target::Doc(d) => *d > 0,
    }
}

impl LinkTarget {
    /// The target as rhun reads it (`PATH:LINE:COL`, `#DOC`), or nothing when rhun would refuse
    /// it: a URL that is not http(s), a path that is not absolute, a document id below 1.
    pub fn encode(&self) -> Option<String> {
        match self {
            LinkTarget::Url(u) => valid_url(u, MAX_PATH).then(|| u.clone()),
            LinkTarget::Path(p, at) => {
                if !valid_target(&Target::Path(p.clone())) {
                    return None;
                }
                Some(match (at.line, at.col) {
                    (0, _) => p.clone(),
                    (l, 0) => format!("{p}:{l}"),
                    (l, c) => format!("{p}:{l}:{c}"),
                })
            }
            LinkTarget::Doc(d) => (*d > 0).then(|| format!("#{d}")),
        }
    }
}

/// A link in a document: ctrl+click on `range` opens `target`; `tip` is one line shown beside
/// it (cut at [`MAX_TEXT`] bytes), or empty.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DocumentLink {
    pub range: Range,
    pub target: LinkTarget,
    pub tip: String,
}

impl DocumentLink {
    pub fn new(range: Range, target: LinkTarget) -> DocumentLink {
        DocumentLink {
            range,
            target,
            tip: String::new(),
        }
    }

    pub fn tip(mut self, tip: &str) -> DocumentLink {
        self.tip = tip.to_string();
        self
    }
}

/// The document-links answer. Links whose target rhun would refuse ([`LinkTarget::encode`]) or
/// whose range runs backwards are left out, as those past [`MAX_RECORDS`] or [`MAX_BODY`].
pub fn encode_document_links(links: &[DocumentLink]) -> Vec<u8> {
    let mut recs = Vec::new();
    let mut n = 0;
    for l in links {
        let Some(target) = l.target.encode() else {
            continue;
        };
        if !well_formed(l.range) {
            continue;
        }
        let tip = field(&l.tip, MAX_TEXT);
        let head = format!("{} {} {}\n", range_fields(l.range), target.len(), tip.len());
        if n == MAX_RECORDS || recs.len() + head.len() + target.len() + tip.len() + 16 > MAX_BODY {
            break;
        }
        recs.extend_from_slice(head.as_bytes());
        recs.extend_from_slice(target.as_bytes());
        recs.extend_from_slice(tip.as_bytes());
        n += 1;
    }
    counted(n, &recs)
}

// ---- file-diagnostics

/// A diagnostic's tag: rhun draws unnecessary code faded and deprecated code struck through.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum DiagnosticTag {
    Unnecessary,
    Deprecated,
}

/// A place related to a diagnostic: `target` an absolute path or an open document, `range` there,
/// `message` one line.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RelatedInfo {
    pub target: Target,
    pub range: Range,
    pub message: String,
}

impl RelatedInfo {
    pub fn new(target: Target, range: Range, message: &str) -> RelatedInfo {
        RelatedInfo {
            target,
            range,
            message: message.to_string(),
        }
    }
}

/// The caps of file-diagnostics: records and the body of one request, a message, an id, a code
/// or source, a link, related places of one diagnostic.
pub const MAX_FILE_DIAGNOSTICS: usize = 2000;
pub const MAX_FILE_DIAGNOSTICS_BODY: usize = 4 << 20;
pub const MAX_DIAGNOSTIC_MESSAGE: usize = 4000;
pub const MAX_DIAGNOSTIC_ID: usize = 64;
pub const MAX_DIAGNOSTIC_CODE: usize = 100;
pub const MAX_DIAGNOSTIC_HREF: usize = 1000;
pub const MAX_RELATED: usize = 16;

/// Whether `s` is a diagnostic id rhun takes: 1 to 64 of `[A-Za-z0-9_.:-]`.
pub fn valid_diagnostic_id(s: &str) -> bool {
    !s.is_empty()
        && s.len() <= MAX_DIAGNOSTIC_ID
        && s.bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'_' | b'.' | b':' | b'-'))
}

/// A diagnostic's range as rhun takes it: positions of at least 1 1, the end not before the
/// start.
fn mark_range(r: Range) -> Range {
    let fix = |p: Pos| Pos::new(p.line.max(1), p.col.max(1));
    let (start, end) = (fix(r.start), fix(r.end));
    Range::new(start, if end < start { start } else { end })
}

fn diagnostic_extra(d: &Diagnostic, ids: &mut std::collections::HashSet<String>) -> Vec<u8> {
    let mut b = Vec::new();
    let rec = |name: &str, bytes: &str, b: &mut Vec<u8>| {
        b.extend_from_slice(format!("{name} {}\n{bytes}", bytes.len()).as_bytes());
    };
    if valid_diagnostic_id(&d.id) && ids.insert(d.id.clone()) {
        rec("id", &d.id, &mut b);
    }
    let code = field(&d.code, MAX_DIAGNOSTIC_CODE);
    if !code.is_empty() {
        rec("code", &code, &mut b);
    }
    let source = field(&d.source, MAX_DIAGNOSTIC_CODE);
    if !source.is_empty() {
        rec("source", &source, &mut b);
    }
    if valid_url(&d.href, MAX_DIAGNOSTIC_HREF) {
        rec("href", &d.href, &mut b);
    }
    for (tag, word) in [
        (DiagnosticTag::Unnecessary, "unnecessary"),
        (DiagnosticTag::Deprecated, "deprecated"),
    ] {
        if d.tags.contains(&tag) {
            b.extend_from_slice(format!("tag {word} 0\n").as_bytes());
        }
    }
    for r in d
        .related
        .iter()
        .filter(|r| valid_target(&r.target))
        .take(MAX_RELATED)
    {
        let (p, m) = (r.target.encode(), field(&r.message, MAX_DIAGNOSTIC_MESSAGE));
        b.extend_from_slice(
            format!(
                "related {} {} {}\n{p}{m}",
                range_fields(mark_range(r.range)),
                p.len(),
                m.len()
            )
            .as_bytes(),
        );
    }
    b
}

/// The records of a file-diagnostics request and how many there are. Diagnostics without a
/// message are left out, as those past [`MAX_FILE_DIAGNOSTICS`] or [`MAX_FILE_DIAGNOSTICS_BODY`];
/// messages are cut at [`MAX_DIAGNOSTIC_MESSAGE`] bytes (at a character) and keep their line
/// feeds; ranges get positions of at least 1 1 and an end not before their start; ids that are
/// not tokens or repeat one before, codes and sources past [`MAX_DIAGNOSTIC_CODE`] (cut), links
/// that are not http(s) and related places past [`MAX_RELATED`] are left out of a record.
pub fn encode_file_diagnostics(diags: &[Diagnostic]) -> (Vec<u8>, usize) {
    let mut recs = Vec::new();
    let mut ids = std::collections::HashSet::new();
    let mut n = 0;
    for d in diags {
        let msg = cut(&d.message, MAX_DIAGNOSTIC_MESSAGE);
        if msg.is_empty() {
            continue;
        }
        let extra = diagnostic_extra(d, &mut ids);
        let head = format!(
            "{} {} {} {}\n",
            range_fields(mark_range(d.range)),
            d.severity.word(),
            msg.len(),
            extra.len()
        );
        if n == MAX_FILE_DIAGNOSTICS
            || recs.len() + head.len() + msg.len() + extra.len() + MAX_PATH
                > MAX_FILE_DIAGNOSTICS_BODY
        {
            break;
        }
        recs.extend_from_slice(head.as_bytes());
        recs.extend_from_slice(msg.as_bytes());
        recs.extend_from_slice(&extra);
        n += 1;
    }
    (recs, n)
}

impl Ext {
    /// Replaces this extension's diagnostics for a file in rhun's Problems store (feature
    /// `file-diagnostics`; needs `documents = read`): [`Target::Doc`] for an open document at its
    /// current `version`, [`Target::Path`] for any file by its absolute path (`version` is not
    /// sent: pass `None`). The file need not be open: its diagnostics show when it opens. No
    /// diagnostics clears them. The records are [`encode_file_diagnostics`]'. rhun refusing the
    /// set is an error (`stale` with the document's current version as text, `needs`, `nodoc`,
    /// `nofile` for an untitled document, `syntax`, `toobig`), and then nothing changed.
    pub fn file_diagnostics(
        &self,
        target: &Target,
        version: Option<i64>,
        diags: &[Diagnostic],
    ) -> Result<(), Error> {
        if !self.has(Feature::FileDiagnostics) {
            return Err(local("unknown", "file-diagnostics was not agreed"));
        }
        if !valid_target(target) {
            return Err(local(
                "syntax",
                "a target is an absolute path or an open document",
            ));
        }
        let v = match (target, version) {
            (Target::Path(_), _) => "-".to_string(),
            (Target::Doc(_), Some(v)) if v >= 0 => v.to_string(),
            (Target::Doc(_), _) => {
                return Err(local(
                    "syntax",
                    "an open document's diagnostics need its version",
                ))
            }
        };
        let (recs, n) = encode_file_diagnostics(diags);
        let t = target.encode();
        let mut body = t.clone().into_bytes();
        body.extend_from_slice(&recs);
        self.request(
            &format!("file-diagnostics {v} {n} {}", t.len()),
            Some(&body),
        )
        .map(|_| ())
    }

    /// Removes every set of diagnostics this extension gave with [`Ext::file_diagnostics`]
    /// (feature `file-diagnostics`).
    pub fn reset_diagnostics(&self) -> Result<(), Error> {
        if !self.has(Feature::FileDiagnostics) {
            return Err(local("unknown", "file-diagnostics was not agreed"));
        }
        self.request("file-diagnostics-reset", None).map(|_| ())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::Severity;

    fn r(l1: u32, c1: u32, l2: u32, c2: u32) -> Range {
        Range::new(Pos::new(l1, c1), Pos::new(l2, c2))
    }

    fn fields(s: &str) -> Vec<String> {
        s.split(' ').map(str::to_string).collect()
    }

    fn xrec(name: &str, bytes: &str) -> String {
        format!("{name} {}\n{bytes}", bytes.len())
    }

    fn diag_rec(rng: &str, sev: &str, msg: &str, extra: &str) -> String {
        format!("{rng} {sev} {} {}\n{msg}{extra}", msg.len(), extra.len())
    }

    // go/rhunext/langfeat_test.go: the same bytes
    #[test]
    fn file_diagnostics_records() {
        let ds = vec![
            Diagnostic {
                range: r(2, 9, 2, 14),
                severity: Severity::Warning,
                message: "unused\ntotal".into(),
                id: "d1".into(),
                code: "W1".into(),
                source: "lint".into(),
                href: "https://example.test/W1".into(),
                tags: vec![
                    DiagnosticTag::Unnecessary,
                    DiagnosticTag::Unnecessary,
                    DiagnosticTag::Deprecated,
                ],
                related: vec![
                    RelatedInfo::new(
                        Target::Path("/p/a.go".into()),
                        r(1, 1, 1, 4),
                        "declared\nhere",
                    ),
                    RelatedInfo::new(
                        Target::Path("relative.go".into()),
                        r(1, 1, 1, 1),
                        "left out",
                    ),
                    RelatedInfo::new(Target::Doc(3), r(0, 0, 0, 0), ""),
                ],
            },
            Diagnostic {
                message: "zero".into(),
                id: "d1".into(),
                ..Default::default()
            },
            Diagnostic::default(),
            Diagnostic {
                range: r(3, 5, 3, 1),
                severity: Severity::Hint,
                message: "back".into(),
                id: "bad id".into(),
                href: "ftp://x".into(),
                code: "c".repeat(150),
                source: "a\nb".into(),
                ..Default::default()
            },
        ];
        let (got, n) = encode_file_diagnostics(&ds);
        let extra = xrec("id", "d1")
            + &xrec("code", "W1")
            + &xrec("source", "lint")
            + &xrec("href", "https://example.test/W1")
            + "tag unnecessary 0\ntag deprecated 0\n"
            + "related 1 1 1 4 7 13\n/p/a.godeclared here"
            + "related 1 1 1 1 2 0\n#3";
        let want = diag_rec("2 9 2 14", "warning", "unused\ntotal", &extra)
            + &diag_rec("1 1 1 1", "error", "zero", "")
            + &diag_rec(
                "3 5 3 5",
                "hint",
                "back",
                &(xrec("code", &"c".repeat(100)) + &xrec("source", "a b")),
            );
        assert_eq!((n, String::from_utf8(got).unwrap()), (3, want));
        let many: Vec<Diagnostic> = (0..2001)
            .map(|_| Diagnostic {
                range: r(1, 1, 1, 2),
                message: "m".into(),
                ..Default::default()
            })
            .collect();
        assert_eq!(encode_file_diagnostics(&many).1, MAX_FILE_DIAGNOSTICS);
        let long: Vec<Diagnostic> = (0..2000)
            .map(|_| Diagnostic {
                range: r(1, 1, 1, 2),
                message: "é".repeat(2500),
                ..Default::default()
            })
            .collect();
        let (b, n) = encode_file_diagnostics(&long);
        assert!(n < MAX_FILE_DIAGNOSTICS && b.len() + MAX_PATH <= MAX_FILE_DIAGNOSTICS_BODY);
        assert!(b.starts_with(b"1 1 1 2 error 4000 0\n"));
        assert!(valid_diagnostic_id("a.b:c-d_9") && !valid_diagnostic_id(&"a".repeat(65)));
    }

    #[test]
    fn hovers_and_requests() {
        let h = HoverRequest::parse(3, &fields("4 7 2 5 pointer -")).unwrap();
        assert_eq!((h.at, h.kind.as_str()), (Pos::new(2, 5), "pointer"));
        assert!(HoverRequest::parse(3, &fields("4 7 2 5")).is_none());
        assert_eq!(
            Hover::new(r(2, 1, 2, 9), "fn pointer").encode(),
            b"2 1 2 9\nfn pointer".to_vec()
        );
        assert!(Hover::default().encode().is_empty());
        let big = Hover::new(r(8, 5, 8, 1), &"é".repeat(40000)).encode();
        let head = "0 0 0 0\n";
        let n = (MAX_HOVER_BODY - head.len()) / 2;
        assert_eq!(big, format!("{head}{}", "é".repeat(n)).into_bytes());
        let s = SelectionRangesRequest::parse(3, &fields("4 7 3 12"), b"1 6\n2 3\n5 4\n").unwrap();
        assert_eq!(
            s.positions,
            vec![Pos::new(1, 6), Pos::new(2, 3), Pos::new(5, 4)]
        );
        assert!(SelectionRangesRequest::parse(3, &fields("4 7 2 4"), b"5 4\n").is_none());
        assert!(SelectionRangesRequest::parse(3, &fields("4 7 65 4"), b"5 4\n").is_none());
        let w = WorkspaceSymbolsRequest::parse(3, &fields("500 3"), b"mai").unwrap();
        assert_eq!((w.query.as_str(), w.max), ("mai", 500));
        assert_eq!(item_data(&fields("2"), b"m1"), Some(&b"m1"[..]));
        assert_eq!(item_data(&fields("3"), b"m1"), None);
        assert!(DocRequest::parse(1, &fields("4 7")).is_some());
    }

    #[test]
    fn selection_ranges_are_chains() {
        let at = [Pos::new(1, 6), Pos::new(2, 3), Pos::new(5, 4)];
        let groups = vec![
            vec![
                r(1, 5, 1, 8),
                r(1, 5, 1, 8),
                r(1, 1, 1, 20),
                r(1, 2, 2, 1),
                r(1, 1, 9, 1),
            ],
            vec![],
            vec![r(5, 1, 5, 9), r(5, 9, 5, 1)],
        ];
        assert_eq!(
            String::from_utf8(encode_selection_ranges(&at, &groups)).unwrap(),
            "2\n1 5 1 8\n1 1 1 20\n1\n2 3 2 3\n1\n5 1 5 9\n"
        );
        assert_eq!(
            encode_selection_ranges(
                &[Pos::new(5, 4), Pos::new(2, 3)],
                &[groups[2].clone(), vec![]]
            ),
            b"1\n5 1 5 9\n".to_vec()
        );
        let at = vec![Pos::new(1000000, 1000); 64];
        let groups: Vec<Vec<Range>> = (0..64)
            .map(|_| {
                (0..64)
                    .map(|j| r(1000000 - j, 1000 - j, 1000000 + j, 1000 + j))
                    .collect()
            })
            .collect();
        let b = encode_selection_ranges(&at, &groups);
        assert!(b.len() <= MAX_SELECTION_BODY && b.starts_with(b"42\n1000000 1000 1000000 1000\n"));
    }

    fn item(name: &str, data: &str) -> HierarchyItem {
        HierarchyItem::new(
            SymbolKind::Function,
            name,
            Target::Path("/p/a.go".into()),
            r(3, 1, 9, 2),
        )
        .detail("pkg")
        .selection(r(3, 6, 3, 6 + name.len() as u32))
        .data(data.as_bytes())
    }

    fn item_rec(kind: &str, sel: &str, name: &str, data: &str, sites: &[&str]) -> String {
        let mut head = format!("{kind} 3 1 9 2 {sel} 7 {} 3 {}", name.len(), data.len());
        if !sites.is_empty() {
            head += &format!(" {}", sites.len());
        }
        let mut s = format!("{head}\n/p/a.go{name}pkg{data}");
        for site in sites {
            s += site;
            s.push('\n');
        }
        s
    }

    #[test]
    fn hierarchy_records() {
        let mut bad = item("bad", "");
        bad.range = r(9, 1, 3, 1);
        let outside = item("outside", "o").selection(r(1, 1, 1, 2));
        let roots = [item("main", "m1"), item("", ""), bad, outside];
        assert_eq!(
            String::from_utf8(encode_hierarchy_roots(&roots)).unwrap(),
            "2\n".to_string()
                + &item_rec("function", "3 6 3 10", "main", "m1", &[])
                + &item_rec("function", "3 1 9 2", "outside", "o", &[])
        );
        let calls = [
            Call::new(
                item("late", ""),
                vec![r(5, 1, 5, 2), r(6, 2, 6, 1), r(7, 1, 7, 3)],
            ),
            Call::new(item("nosites", ""), vec![]),
        ];
        assert_eq!(
            String::from_utf8(encode_calls(&calls)).unwrap(),
            "1\n".to_string()
                + &item_rec("function", "3 6 3 10", "late", "", &["5 1 5 2", "7 1 7 3"])
        );
        let huge = item("Huge", &"x".repeat(MAX_HIERARCHY_DATA + 1));
        assert_eq!(
            String::from_utf8(encode_hierarchy_items(&[huge, item("Sub", "")])).unwrap(),
            "1\n".to_string() + &item_rec("function", "3 6 3 9", "Sub", "", &[])
        );
        let many: Vec<HierarchyItem> = (0..70).map(|_| item("r", "")).collect();
        assert!(encode_hierarchy_roots(&many).starts_with(b"64\n"));
        let sites = vec![r(1, 1, 1, 2); MAX_CALL_SITES + 5];
        let b = encode_calls(&[Call::new(item("c", ""), sites)]);
        assert!(String::from_utf8(b).unwrap().contains(" 1000\n"));
    }

    #[test]
    fn workspace_symbols_and_links() {
        let syms = [
            WorkspaceSymbol::new(
                Target::Path("/p/a.go".into()),
                r(3, 6, 3, 10),
                SymbolKind::Function,
                "Main",
            )
            .detail("main"),
            WorkspaceSymbol::new(
                Target::Doc(2),
                r(1, 1, 1, 4),
                SymbolKind::Function,
                "mai\nx",
            ),
            WorkspaceSymbol::new(
                Target::Path("/p/b.go".into()),
                r(1, 1, 1, 1),
                SymbolKind::Class,
                "",
            ),
        ];
        assert_eq!(
            String::from_utf8(encode_workspace_symbols(&syms)).unwrap(),
            "2\n3 6 3 10 function 7 4 4\n/p/a.goMainmain1 1 1 4 function 2 5 0\n#2mai x"
        );
        let links = [
            DocumentLink::new(
                r(1, 1, 1, 20),
                LinkTarget::Url("https://example.test/a b".into()),
            ),
            DocumentLink::new(
                r(1, 1, 1, 20),
                LinkTarget::Url("https://example.test/a".into()),
            )
            .tip("Open\nthe page"),
            DocumentLink::new(
                r(2, 3, 2, 9),
                LinkTarget::Path("/p/a.go".into(), Pos::new(4, 2)),
            ),
            DocumentLink::new(
                r(3, 3, 3, 9),
                LinkTarget::Path("/p/a.go".into(), Pos::new(4, 0)),
            ),
            DocumentLink::new(r(4, 3, 4, 9), LinkTarget::Doc(4)),
            DocumentLink::new(
                r(5, 3, 5, 9),
                LinkTarget::Path("relative.go".into(), Pos::default()),
            ),
            DocumentLink::new(
                r(6, 3, 5, 9),
                LinkTarget::Path("/p/backwards".into(), Pos::default()),
            ),
            DocumentLink::new(r(7, 1, 7, 4), LinkTarget::Url("ftp://example.test".into())),
            DocumentLink::new(r(8, 1, 8, 4), LinkTarget::Doc(0)),
        ];
        assert_eq!(
            String::from_utf8(encode_document_links(&links)).unwrap(),
            "4\n1 1 1 20 22 13\nhttps://example.test/aOpen the page2 3 2 9 11 0\n/p/a.go:4:2\
             3 3 3 9 9 0\n/p/a.go:44 3 4 9 2 0\n#4"
        );
        assert!(LinkTarget::Path("C:\\p\\a.go".into(), Pos::new(3, 0))
            .encode()
            .is_some());
        assert!(LinkTarget::Url("http://".into()).encode().is_none());
    }
}
