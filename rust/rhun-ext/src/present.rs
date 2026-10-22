//! Protocol 1.1's editor presentation (v1.1/v1.1.md, "Feature: decorations", "Feature:
//! diagnostic-tags" and "Presentation providers"): an extension styles ranges of open documents
//! ([`Ext::decoration_type`], [`Ext::decorate`]) and hears which lines are shown
//! ([`parse_visible`]); providers answer rhun's folding-ranges, semantic-tokens, inlay-hints and
//! code-lens requests through [`Handler`](crate::Handler). rhun asks about the active document
//! only, by itself, and withdraws a request when a newer one replaces it.

use crate::{Doc, Error, Event, Ext, Pos, Range};

fn num<T: std::str::FromStr>(fields: &[String], i: usize) -> Option<T> {
    fields.get(i)?.parse().ok()
}

/// `s` on one line, cut to at most `max` bytes at a character boundary.
fn line_cut(s: &str, max: usize) -> String {
    let s = s.replace(['\n', '\r'], " ");
    cut(&s, max).to_string()
}

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

fn well_formed(r: &Range) -> bool {
    (r.end.line, r.end.col) >= (r.start.line, r.start.col)
}

fn range_fields(r: &Range) -> String {
    format!(
        "{} {} {} {}",
        r.start.line, r.start.col, r.end.line, r.end.col
    )
}

/// A reference to one of the extension's own commands: the record `command NAME LEN` and ARGS
/// (nothing when rhun would refuse it).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct CommandLink {
    pub name: String,
    pub args: Vec<u8>,
}

impl CommandLink {
    pub fn new(name: &str, args: &[u8]) -> CommandLink {
        CommandLink {
            name: name.to_string(),
            args: args.to_vec(),
        }
    }
    fn encode(&self) -> Vec<u8> {
        crate::command_ref(&self.name, &self.args).unwrap_or_default()
    }
}

// ---- decorations

/// Decoration limits (v1.1.md, Feature decorations: Limits).
pub const MAX_DECORATIONS: usize = 20000;
pub const MAX_DECORATION_TEXT: usize = 200;
pub const MAX_DECORATION_TIP: usize = 1000;
pub const MAX_DECORATION_EXTRA: usize = 9000;

/// One range of a decorate request: `before` and `after` are virtual text (one line each, shown
/// when the type has before or after), `tip` shows while the pointer rests on the range and
/// `command` runs when its virtual text is clicked.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct DecorationRange {
    pub range: Range,
    pub before: String,
    pub after: String,
    pub tip: String,
    pub command: Option<CommandLink>,
}

/// Whether `s` names a decoration type: 1 to 40 of `[a-z0-9_-]`.
pub fn valid_decoration_name(s: &str) -> bool {
    !s.is_empty()
        && s.len() <= 40
        && s.bytes()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == b'_' || c == b'-')
}

/// A decorate request's body and record count. Texts are made one line and cut to their limits;
/// ranges whose end comes before their start are left out, as those past [`MAX_DECORATIONS`].
pub fn encode_decorations(ranges: &[DecorationRange]) -> (Vec<u8>, usize) {
    let mut b = Vec::new();
    let mut n = 0;
    for r in ranges {
        if !well_formed(&r.range) || n == MAX_DECORATIONS {
            continue;
        }
        let before = line_cut(&r.before, MAX_DECORATION_TEXT);
        let after = line_cut(&r.after, MAX_DECORATION_TEXT);
        let tip = cut(&r.tip, MAX_DECORATION_TIP);
        let mut extra = r
            .command
            .as_ref()
            .map(CommandLink::encode)
            .unwrap_or_default();
        if extra.len() > MAX_DECORATION_EXTRA {
            extra.clear();
        }
        b.extend_from_slice(
            format!(
                "{} {} {} {} {}\n",
                range_fields(&r.range),
                before.len(),
                after.len(),
                tip.len(),
                extra.len()
            )
            .as_bytes(),
        );
        b.extend_from_slice(before.as_bytes());
        b.extend_from_slice(after.as_bytes());
        b.extend_from_slice(tip.as_bytes());
        b.extend_from_slice(&extra);
        n += 1;
    }
    (b, n)
}

/// The visible event: the 1-based first and last lines the editor shows of the active document
/// (`doc` 0: none).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Visible {
    pub doc: Doc,
    pub first: u32,
    pub last: u32,
}

/// Reads a visible event (`0 visible DOC L1 L2`).
pub fn parse_visible(ev: &Event) -> Option<Visible> {
    if ev.name != "visible" || ev.fields.len() != 3 {
        return None;
    }
    Some(Visible {
        doc: num(&ev.fields, 0).filter(|d: &Doc| *d >= 0)?,
        first: num(&ev.fields, 1)?,
        last: num(&ev.fields, 2)?,
    })
}

impl Ext {
    /// Registers this extension's decoration type `name`, or replaces its style. `body` is
    /// `KEY ARGS` lines (color, background, line-background, border, underline, strike, faded,
    /// before, after, width, gutter, scrollbar, grow, priority); see the feature's Type body.
    pub fn decoration_type(&self, name: &str, body: &str) -> Result<(), Error> {
        if !valid_decoration_name(name) {
            return Err(Error {
                code: "syntax".into(),
                text: "a decoration type name is 1 to 40 of a-z 0-9 _ -".into(),
            });
        }
        self.request(&format!("decoration-type {name}"), Some(body.as_bytes()))
            .map(|_| ())
    }

    /// Replaces this extension's ranges of type `name` on `doc` whose first line is in
    /// `from..=to` (1-based; 0 0: all of them). `version` may be the current one or one rhun still
    /// keeps the edits since.
    pub fn decorate(
        &self,
        doc: Doc,
        version: i64,
        name: &str,
        from: u32,
        to: u32,
        ranges: &[DecorationRange],
    ) -> Result<(), Error> {
        let (body, n) = encode_decorations(ranges);
        self.request(
            &format!("decorate {doc} {version} {name} {from} {to} {n}"),
            Some(&body),
        )
        .map(|_| ())
    }

    /// Registers a semantic token legend: the types and modifiers its tokens index, each name
    /// optionally followed by `=EFFECT` (a class name, strike, underline, faded or -).
    pub fn semantic_legend(
        &self,
        name: &str,
        types: &[&str],
        modifiers: &[&str],
    ) -> Result<(), Error> {
        let body = format!("{}\n{}", types.join(" "), modifiers.join(" "));
        self.request(&format!("semantic-legend {name}"), Some(body.as_bytes()))
            .map(|_| ())
    }
}

// ---- folding ranges

/// Folding range limits.
pub const MAX_FOLDING_RANGES: usize = 50000;
pub const MAX_FOLDING_RANGE_TEXT: usize = 80;

/// Lets lines `start + 1..=end` hide behind line `start`, which then shows `text` (one line, at
/// most 80 bytes; empty for rhun's own). `kind` is "region", "comment", "imports" or empty.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct FoldingRange {
    pub start: u32,
    pub end: u32,
    pub kind: String,
    pub text: String,
}

/// The folding-ranges answer. Ranges with `start` not before `end`, or `start` 0, are left out, as
/// those past [`MAX_FOLDING_RANGES`].
pub fn encode_folding_ranges(ranges: &[FoldingRange]) -> Vec<u8> {
    let mut recs = Vec::new();
    let mut n = 0;
    for r in ranges {
        if r.start < 1 || r.end <= r.start || n == MAX_FOLDING_RANGES {
            continue;
        }
        let kind = match r.kind.as_str() {
            "region" | "comment" | "imports" => r.kind.as_str(),
            _ => "-",
        };
        let text = line_cut(&r.text, MAX_FOLDING_RANGE_TEXT);
        recs.extend_from_slice(
            format!("{} {} {kind} {}\n{text}", r.start, r.end, text.len()).as_bytes(),
        );
        n += 1;
    }
    let mut out = format!("{n}\n").into_bytes();
    out.extend_from_slice(&recs);
    out
}

// ---- semantic tokens

/// Semantic token limits.
pub const MAX_SEMANTIC_TOKENS: usize = 200000;
pub const MAX_SEMANTIC_EDITS: usize = 10000;

/// `semantic-tokens DOC VERSION PREV` (`prev` the last result's id, empty for none) or, for a
/// document over 200,000 lines or 8 MB, `semantic-tokens-range DOC VERSION L1 L2` (`range` lines
/// `first..=last`).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SemanticTokensRequest {
    pub id: u64,
    pub doc: Doc,
    pub version: i64,
    pub prev: String,
    pub range: Option<(u32, u32)>,
}

impl SemanticTokensRequest {
    pub fn parse(verb: &str, id: u64, fields: &[String]) -> Option<SemanticTokensRequest> {
        let mut r = SemanticTokensRequest {
            id,
            doc: num(fields, 0)?,
            version: num(fields, 1)?,
            ..Default::default()
        };
        match (verb, fields.len()) {
            ("semantic-tokens", 3) => {
                if fields[2] != "-" {
                    r.prev = fields[2].clone();
                }
            }
            ("semantic-tokens-range", 4) => {
                let (a, b): (u32, u32) = (num(fields, 2)?, num(fields, 3)?);
                if a < 1 || b < a {
                    return None;
                }
                r.range = Some((a, b));
            }
            _ => return None,
        }
        Some(r)
    }
}

/// An edit of a delta: `del` ints at `start` of the previous result's array become `data`.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SemanticEdit {
    pub start: u32,
    pub del: u32,
    pub data: Vec<u32>,
}

/// An answer: `result` its id (empty for none), `legend` the legend its types index. `data` is
/// LSP's five ints per token (DLINE DCOL LEN TYPE MODS, byte columns, the first DLINE from line
/// 1); with `edits` set (`delta`) the answer is a delta against the request's `prev`.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SemanticTokens {
    pub result: String,
    pub legend: String,
    pub data: Vec<u32>,
    pub delta: bool,
    pub edits: Vec<SemanticEdit>,
}

/// Whether `s` is a legend name or result id: 1 to 64 of `[A-Za-z0-9_.-]`.
pub fn valid_semantic_name(s: &str) -> bool {
    !s.is_empty()
        && s.len() <= 64
        && s.bytes()
            .all(|c| c.is_ascii_alphanumeric() || c == b'_' || c == b'.' || c == b'-')
}

fn push_ints(b: &mut Vec<u8>, data: &[u32]) {
    for (i, v) in data.iter().enumerate() {
        if i > 0 {
            b.push(b' ');
        }
        b.extend_from_slice(v.to_string().as_bytes());
    }
}

impl SemanticTokens {
    /// The answer's body, for a range request when `range`: empty for data that is not whole
    /// tokens, over the limits, or a bad legend or result name.
    pub fn encode(&self, range: bool) -> Vec<u8> {
        if !valid_semantic_name(&self.legend)
            || (!self.result.is_empty() && !valid_semantic_name(&self.result))
        {
            return Vec::new();
        }
        let result = if self.result.is_empty() {
            "-"
        } else {
            self.result.as_str()
        };
        let mut b = Vec::new();
        if self.delta && !range {
            if self.edits.len() > MAX_SEMANTIC_EDITS {
                return Vec::new();
            }
            b.extend_from_slice(
                format!("{result} {} delta {}\n", self.legend, self.edits.len()).as_bytes(),
            );
            for e in &self.edits {
                b.extend_from_slice(format!("{} {} {}\n", e.start, e.del, e.data.len()).as_bytes());
                push_ints(&mut b, &e.data);
                if !e.data.is_empty() {
                    b.push(b'\n');
                }
            }
            return b;
        }
        if self.data.len() % 5 != 0 || self.data.len() / 5 > MAX_SEMANTIC_TOKENS {
            return Vec::new();
        }
        let mode = if range { "range" } else { "full" };
        b.extend_from_slice(
            format!("{result} {} {mode} {}\n", self.legend, self.data.len() / 5).as_bytes(),
        );
        push_ints(&mut b, &self.data);
        b
    }
}

// ---- inlay hints and code lens

/// A request about lines `first..=last` of a document (`inlay-hints DOC VERSION L1 L2`,
/// `code-lens DOC VERSION L1 L2`).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct LinesRequest {
    pub id: u64,
    pub doc: Doc,
    pub version: i64,
    pub first: u32,
    pub last: u32,
}

impl LinesRequest {
    pub fn parse(id: u64, fields: &[String]) -> Option<LinesRequest> {
        if fields.len() != 4 {
            return None;
        }
        let r = LinesRequest {
            id,
            doc: num(fields, 0)?,
            version: num(fields, 1)?,
            first: num(fields, 2)?,
            last: num(fields, 3)?,
        };
        (r.first >= 1 && r.last >= r.first).then_some(r)
    }
}

/// Inlay hint limits.
pub const MAX_INLAY_HINTS: usize = 5000;
pub const MAX_INLAY_LABEL: usize = 120;
pub const MAX_INLAY_PARTS: usize = 8;
pub const MAX_INLAY_TIP: usize = 4000;
pub const MAX_INLAY_EDITS: usize = 16;

/// Part of a hint's label: `tip` shows while the pointer rests on it; a click runs `command`, or
/// opens `goto` (a path, empty for this document, and a position).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct InlayHintPart {
    pub label: String,
    pub tip: String,
    pub command: Option<CommandLink>,
    pub goto: Option<(String, Pos)>,
}

/// A hint at `at`: `before` keeps it before the text after it (a parameter name), otherwise it
/// follows the text before it (a type). A double click makes `edits` (range, text).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct InlayHint {
    pub at: Pos,
    pub before: bool,
    pub parts: Vec<InlayHintPart>,
    pub edits: Vec<(Range, String)>,
}

/// The inlay-hints answer. Labels are made one line and cut so a hint's parts hold at most
/// [`MAX_INLAY_LABEL`] bytes; hints with nothing to show, or at a position below 1 1, are left out,
/// as those past [`MAX_INLAY_HINTS`].
pub fn encode_inlay_hints(hints: &[InlayHint]) -> Vec<u8> {
    let mut recs = Vec::new();
    let mut n = 0;
    for h in hints {
        if h.at.line < 1 || h.at.col < 1 || n == MAX_INLAY_HINTS {
            continue;
        }
        let mut parts = Vec::new();
        let (mut total, mut np) = (0, 0);
        for p in &h.parts {
            if np == MAX_INLAY_PARTS {
                break;
            }
            let label = line_cut(&p.label, MAX_INLAY_LABEL - total);
            if label.is_empty() {
                continue;
            }
            total += label.len();
            let tip = cut(&p.tip, MAX_INLAY_TIP);
            let mut extra = p
                .command
                .as_ref()
                .map(CommandLink::encode)
                .unwrap_or_default();
            if let Some((path, at)) = &p.goto {
                let path = line_cut(path, usize::MAX);
                extra.extend_from_slice(
                    format!("goto {} {} {}\n{path}", at.line, at.col, path.len()).as_bytes(),
                );
            }
            parts.extend_from_slice(
                format!(
                    "{} {} {}\n{label}{tip}",
                    label.len(),
                    tip.len(),
                    extra.len()
                )
                .as_bytes(),
            );
            parts.extend_from_slice(&extra);
            np += 1;
        }
        if np == 0 {
            continue;
        }
        let mut extra = Vec::new();
        for (r, text) in h.edits.iter().take(MAX_INLAY_EDITS) {
            if !well_formed(r) {
                break;
            }
            extra.extend_from_slice(
                format!("edit {} {}\n{text}", range_fields(r), text.len()).as_bytes(),
            );
        }
        let side = if h.before { "before" } else { "after" };
        recs.extend_from_slice(
            format!("{} {} {side} {np} {}\n", h.at.line, h.at.col, extra.len()).as_bytes(),
        );
        recs.extend_from_slice(&parts);
        recs.extend_from_slice(&extra);
        n += 1;
    }
    let mut out = format!("{n}\n").into_bytes();
    out.extend_from_slice(&recs);
    out
}

/// Code lens limits.
pub const MAX_CODE_LENSES: usize = 10000;
pub const MAX_CODE_LENS_TITLE: usize = 200;

/// A title over `line` (empty while not resolved: drawn "…", asked again when shown); a click
/// runs `command`, `tip` shows while the pointer rests on it.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct CodeLens {
    pub line: u32,
    pub title: String,
    pub tip: String,
    pub command: Option<CommandLink>,
}

/// The code-lens answer: every lens of the document. Titles are made one line and cut to
/// [`MAX_CODE_LENS_TITLE`]; lenses on line 0 are left out, as those past [`MAX_CODE_LENSES`].
pub fn encode_code_lenses(lenses: &[CodeLens]) -> Vec<u8> {
    let mut recs = Vec::new();
    let mut n = 0;
    for l in lenses {
        if l.line < 1 || n == MAX_CODE_LENSES {
            continue;
        }
        let title = line_cut(&l.title, MAX_CODE_LENS_TITLE);
        let mut extra = l
            .command
            .as_ref()
            .map(CommandLink::encode)
            .unwrap_or_default();
        if !l.tip.is_empty() {
            let tip = cut(&l.tip, 1000);
            extra.extend_from_slice(format!("tip {}\n{tip}", tip.len()).as_bytes());
        }
        recs.extend_from_slice(
            format!("{} {} {}\n{title}", l.line, title.len(), extra.len()).as_bytes(),
        );
        recs.extend_from_slice(&extra);
        n += 1;
    }
    let mut out = format!("{n}\n").into_bytes();
    out.extend_from_slice(&recs);
    out
}

// ---- document highlights, colors, inline completion

/// The most ranges a highlights answer holds.
pub const MAX_HIGHLIGHTS: usize = 5000;

/// A place of the symbol under the caret: `kind` "text", "read" or "write".
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Highlight {
    pub range: Range,
    pub kind: &'static str,
}

/// The highlights answer: backwards ranges are left out, as those past [`MAX_HIGHLIGHTS`].
pub fn encode_highlights(hs: &[Highlight]) -> Vec<u8> {
    let mut recs = Vec::new();
    let mut n = 0;
    for h in hs {
        if !well_formed(&h.range)
            || h.range.start.line < 1
            || h.range.start.col < 1
            || n == MAX_HIGHLIGHTS
        {
            continue;
        }
        let kind = match h.kind {
            "read" | "write" => h.kind,
            _ => "text",
        };
        recs.extend_from_slice(format!("{} {kind}\n", range_fields(&h.range)).as_bytes());
        n += 1;
    }
    let mut out = format!("{n}\n").into_bytes();
    out.extend_from_slice(&recs);
    out
}

/// The most colors an answer holds.
pub const MAX_COLORS: usize = 10000;

/// A color in a document, `rgba` as 0xRRGGBBAA.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct DocumentColor {
    pub range: Range,
    pub rgba: u32,
}

/// The colors answer: backwards ranges are left out, as those past [`MAX_COLORS`].
pub fn encode_colors(cs: &[DocumentColor]) -> Vec<u8> {
    let mut recs = Vec::new();
    let mut n = 0;
    for c in cs {
        if !well_formed(&c.range)
            || c.range.start.line < 1
            || c.range.start.col < 1
            || n == MAX_COLORS
        {
            continue;
        }
        recs.extend_from_slice(format!("{} {:08x}\n", range_fields(&c.range), c.rgba).as_bytes());
        n += 1;
    }
    let mut out = format!("{n}\n").into_bytes();
    out.extend_from_slice(&recs);
    out
}

/// `color-edit DOC VERSION L1 C1 L2 C2 RRGGBBAA`: the user chose a new color for the one at
/// `range`.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct ColorEditRequest {
    pub id: u64,
    pub doc: Doc,
    pub version: i64,
    pub range: Range,
    pub rgba: u32,
}

impl ColorEditRequest {
    pub fn parse(id: u64, fields: &[String]) -> Option<ColorEditRequest> {
        if fields.len() != 7 || fields[6].len() != 8 {
            return None;
        }
        let p = |i| num::<u32>(fields, i).filter(|v| *v >= 1);
        Some(ColorEditRequest {
            id,
            doc: num(fields, 0)?,
            version: num(fields, 1)?,
            range: Range {
                start: Pos {
                    line: p(2)?,
                    col: p(3)?,
                },
                end: Pos {
                    line: p(4)?,
                    col: p(5)?,
                },
            },
            rgba: u32::from_str_radix(&fields[6], 16).ok()?,
        })
    }
}

/// `inline-complete DOC VERSION LINE COL KIND`, `kind` "typed" or "invoked".
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct InlineRequest {
    pub id: u64,
    pub doc: Doc,
    pub version: i64,
    pub at: Pos,
    pub kind: String,
}

impl InlineRequest {
    pub fn parse(id: u64, fields: &[String]) -> Option<InlineRequest> {
        if fields.len() != 5 {
            return None;
        }
        Some(InlineRequest {
            id,
            doc: num(fields, 0)?,
            version: num(fields, 1)?,
            at: Pos {
                line: num(fields, 2)?,
                col: num(fields, 3)?,
            },
            kind: fields[4].clone(),
        })
    }
}

/// Inline completion limits.
pub const MAX_INLINE_ITEMS: usize = 8;
pub const MAX_INLINE_TEXT: usize = 64 << 10;

/// An item: `text` replaces `range` when taken (it shows when the range holds the caret and its
/// text agrees with `text` around the caret); `command` runs after, `edits` are made with it.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct InlineItem {
    pub range: Range,
    pub text: String,
    pub command: Option<CommandLink>,
    pub edits: Vec<(Range, String)>,
}

/// The inline-complete answer: items with a backwards range, or an empty text or one over
/// [`MAX_INLINE_TEXT`], are left out, as those past [`MAX_INLINE_ITEMS`].
pub fn encode_inline_items(items: &[InlineItem]) -> Vec<u8> {
    let mut recs = Vec::new();
    let mut n = 0;
    for it in items {
        if !well_formed(&it.range)
            || it.range.start.line < 1
            || it.text.is_empty()
            || it.text.len() > MAX_INLINE_TEXT
            || n == MAX_INLINE_ITEMS
        {
            continue;
        }
        let mut extra = it
            .command
            .as_ref()
            .map(CommandLink::encode)
            .unwrap_or_default();
        for (r, text) in it.edits.iter().take(MAX_INLAY_EDITS) {
            if !well_formed(r) {
                break;
            }
            extra.extend_from_slice(
                format!("edit {} {}\n{text}", range_fields(r), text.len()).as_bytes(),
            );
        }
        recs.extend_from_slice(
            format!(
                "{} {} {}\n{}",
                range_fields(&it.range),
                it.text.len(),
                extra.len(),
                it.text
            )
            .as_bytes(),
        );
        recs.extend_from_slice(&extra);
        n += 1;
    }
    let mut out = format!("{n}\n").into_bytes();
    out.extend_from_slice(&recs);
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn r(a: u32, b: u32, c: u32, d: u32) -> Range {
        Range {
            start: Pos { line: a, col: b },
            end: Pos { line: c, col: d },
        }
    }

    #[test]
    fn encoders() {
        let (b, n) = encode_decorations(&[
            DecorationRange {
                range: r(2, 1, 2, 1),
                after: "you,\n2h".into(),
                tip: "who".into(),
                command: Some(CommandLink::new("show", b"x")),
                ..Default::default()
            },
            DecorationRange {
                range: r(3, 5, 3, 1),
                ..Default::default()
            },
        ]);
        assert_eq!(n, 1);
        assert_eq!(b, b"2 1 2 1 0 7 3 16\nyou, 2hwhocommand show 1\nx");
        assert_eq!(
            encode_folding_ranges(&[
                FoldingRange {
                    start: 1,
                    end: 9,
                    ..Default::default()
                },
                FoldingRange {
                    start: 3,
                    end: 5,
                    kind: "region".into(),
                    text: "consts\nmore".into()
                },
                FoldingRange {
                    start: 4,
                    end: 4,
                    ..Default::default()
                },
            ]),
            b"2\n1 9 - 0\n3 5 region 11\nconsts more"
        );
        let t = SemanticTokens {
            result: "r1".into(),
            legend: "ra".into(),
            data: vec![0, 3, 4, 0, 0, 1, 8, 1, 1, 1],
            ..Default::default()
        };
        assert_eq!(t.encode(false), b"r1 ra full 2\n0 3 4 0 0 1 8 1 1 1");
        let d = SemanticTokens {
            result: "r2".into(),
            legend: "ra".into(),
            delta: true,
            edits: vec![SemanticEdit {
                start: 3,
                del: 1,
                data: vec![1],
            }],
            ..Default::default()
        };
        assert_eq!(d.encode(false), b"r2 ra delta 1\n3 1 1\n1\n");
        assert!(SemanticTokens {
            legend: "bad name".into(),
            ..Default::default()
        }
        .encode(false)
        .is_empty());
        assert_eq!(
            encode_inlay_hints(&[
                InlayHint {
                    at: Pos { line: 2, col: 10 },
                    parts: vec![InlayHintPart {
                        label: ": i32".into(),
                        tip: "the type".into(),
                        ..Default::default()
                    }],
                    edits: vec![(r(2, 10, 2, 10), ": i32".into())],
                    ..Default::default()
                },
                InlayHint {
                    at: Pos { line: 4, col: 14 },
                    before: true,
                    parts: vec![
                        InlayHintPart {
                            label: "fmt:".into(),
                            command: Some(CommandLink::new("a", b"")),
                            ..Default::default()
                        },
                        InlayHintPart {
                            label: "x".into(),
                            goto: Some((String::new(), Pos { line: 1, col: 4 })),
                            ..Default::default()
                        },
                    ],
                    ..Default::default()
                },
                InlayHint {
                    at: Pos { line: 5, col: 1 },
                    ..Default::default()
                },
            ]),
            b"2\n2 10 after 1 22\n5 8 0\n: i32the typeedit 2 10 2 10 5\n: i32\
4 14 before 2 0\n4 0 12\nfmt:command a 0\n1 0 11\nxgoto 1 4 0\n"
        );
        assert_eq!(
            encode_code_lenses(&[
                CodeLens {
                    line: 1,
                    title: "Run".into(),
                    command: Some(CommandLink::new("run", b"")),
                    ..Default::default()
                },
                CodeLens {
                    line: 1,
                    title: "2 refs".into(),
                    tip: "used".into(),
                    ..Default::default()
                },
                CodeLens {
                    line: 3,
                    ..Default::default()
                },
                CodeLens {
                    line: 0,
                    title: "nowhere".into(),
                    ..Default::default()
                },
            ]),
            b"3\n1 3 14\nRuncommand run 0\n1 6 10\n2 refstip 4\nused3 0 0\n"
        );
        let f = |v: &[&str]| v.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        assert_eq!(
            SemanticTokensRequest::parse("semantic-tokens-range", 8, &f(&["4", "8", "10", "20"]))
                .unwrap()
                .range,
            Some((10, 20))
        );
        assert_eq!(
            SemanticTokensRequest::parse("semantic-tokens", 7, &f(&["4", "8", "r1"]))
                .unwrap()
                .prev,
            "r1"
        );
        assert!(LinesRequest::parse(9, &f(&["4", "8", "9"])).is_none());
        let ev = Event {
            name: "visible".into(),
            fields: f(&["4", "1", "40"]),
            ..Default::default()
        };
        assert_eq!(
            parse_visible(&ev),
            Some(Visible {
                doc: 4,
                first: 1,
                last: 40
            })
        );
    }
}
