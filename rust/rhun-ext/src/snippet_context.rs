//! Read-only context for a versioned native snippet insertion range.
use crate::{Doc, Error, Ext, Feature, Pos, Range, Reply};

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SnippetSnapshot {
    pub version: i64,
    pub position: Pos,
    pub tab_width: u32,
    pub insert_spaces: bool,
    pub crlf: bool,
    pub selected_text: String,
    pub current_line: String,
    pub path: String,
    pub workspace: String,
    pub indent: String,
    pub line_comment: String,
}

pub(crate) fn decimal(s: &str) -> Option<i64> {
    if s.is_empty() || !s.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    s.parse().ok()
}

fn invalid() -> Error {
    Error {
        code: "syntax".into(),
        text: "invalid snippet context reply".into(),
    }
}

fn decode(r: Reply, version: i64, at: Pos) -> Result<SnippetSnapshot, Error> {
    if r.fields.len() != 2
        || decimal(&r.fields[0]) != Some(version)
        || decimal(&r.fields[1]) != Some(r.body.len() as i64)
        || r.body.len() > crate::snippet::MAX_BODY
    {
        return Err(invalid());
    }
    let end = r
        .body
        .iter()
        .position(|&b| b == b'\n')
        .filter(|&n| n <= 256)
        .ok_or_else(invalid)?;
    let head = std::str::from_utf8(&r.body[..end]).map_err(|_| invalid())?;
    let f: Vec<&str> = head.split(' ').collect();
    if f.len() != 13 || f[0] != "context" || f[1] != "1" {
        return Err(invalid());
    }
    let mut n = [0i64; 11];
    for (i, v) in n.iter_mut().enumerate() {
        *v = decimal(f[i + 2]).ok_or_else(invalid)?;
    }
    if at.line == 0
        || at.col == 0
        || n[0] != i64::from(at.line)
        || n[1] != i64::from(at.col)
        || !(1..=16).contains(&n[2])
        || n[3] > 1
        || n[4] > 1
    {
        return Err(invalid());
    }
    let mut rest = &r.body[end + 1..];
    let mut parts = [""; 6];
    for (i, part) in parts.iter_mut().enumerate() {
        let len = usize::try_from(n[i + 5]).map_err(|_| invalid())?;
        if len > rest.len() {
            return Err(invalid());
        }
        *part = std::str::from_utf8(&rest[..len]).map_err(|_| invalid())?;
        rest = &rest[len..];
    }
    let col = (at.col - 1) as usize;
    if !rest.is_empty() || parts[1].contains('\n') || !parts[1].is_char_boundary(col) {
        return Err(invalid());
    }
    let indent = parts[1].as_bytes()[..col]
        .iter()
        .take_while(|&&b| b == b' ' || b == b'\t')
        .count();
    if parts[4] != &parts[1][..indent] {
        return Err(invalid());
    }
    Ok(SnippetSnapshot {
        version,
        position: at,
        tab_width: n[2] as u32,
        insert_spaces: n[3] == 1,
        crlf: n[4] == 1,
        selected_text: parts[0].into(),
        current_line: parts[1].into(),
        path: parts[2].into(),
        workspace: parts[3].into(),
        indent: parts[4].into(),
        line_comment: parts[5].into(),
    })
}

impl Ext {
    /// Snapshot explicit range context without changing the document or selection.
    /// Requires agreed snippets and documents=read; never includes the clipboard.
    pub fn snippet_snapshot(
        &self,
        doc: Doc,
        version: i64,
        range: Range,
    ) -> Result<SnippetSnapshot, Error> {
        if !self.has(Feature::Snippets) {
            return Err(Error {
                code: "unknown".into(),
                text: "snippets was not negotiated".into(),
            });
        }
        if doc <= 0
            || version < 0
            || range.start.line == 0
            || range.start.col == 0
            || range.end.line == 0
            || range.end.col == 0
            || range.end < range.start
        {
            return Err(Error {
                code: "syntax".into(),
                text: "invalid snippet target or range".into(),
            });
        }
        let r = self.request(
            &format!(
                "snippet-context {doc} {version} {} {} {} {}",
                range.start.line, range.start.col, range.end.line, range.end.col
            ),
            None,
        )?;
        decode(r, version, range.start)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn reply(parts: [&str; 6]) -> Reply {
        let lengths = parts
            .iter()
            .map(|s| s.len().to_string())
            .collect::<Vec<_>>()
            .join(" ");
        let body = format!("context 1 2 3 8 0 1 {lengths}\n{}", parts.concat()).into_bytes();
        Reply {
            fields: vec!["9".into(), body.len().to_string()],
            body,
        }
    }
    #[test]
    fn snapshot_decode_preserves_bytes_and_rejects_malformed_context() {
        let parts = ["😀\n\0", "\t xy😀", "/p/a.go", "/p", "\t ", "//"];
        let got = decode(reply(parts), 9, Pos::new(2, 3)).unwrap();
        assert_eq!(got.selected_text, parts[0]);
        assert_eq!(got.indent, "\t ");
        assert_eq!(got.tab_width, 8);
        assert!(!got.insert_spaces);
        assert!(got.crlf);
        for (old, new) in [
            (" 8 ", " +8 "),
            (" 8 ", " 17 "),
            ("8 0 1", "8 2 1"),
            ("8 0 1", "8 0 2"),
            ("1 2 3", "1 2 4"),
            ("1 2 3", "1  2 3"),
            (" 8 ", " 9223372036854775808 "),
        ] {
            let mut r = reply(parts);
            r.body = String::from_utf8(r.body)
                .unwrap()
                .replacen(old, new, 1)
                .into_bytes();
            r.fields[1] = r.body.len().to_string();
            assert!(decode(r, 9, Pos::new(2, 3)).is_err());
        }
        for (i, text) in [(1, ""), (1, "😀"), (1, "\t \nx"), (4, "\t")] {
            let mut p = parts;
            p[i] = text;
            assert!(decode(reply(p), 9, Pos::new(2, 3)).is_err());
        }
        for fields in [
            vec!["9"],
            vec!["+9", "1"],
            vec!["10", "1"],
            vec!["9", "1", "extra"],
        ] {
            let mut r = reply(parts);
            r.fields = fields.iter().map(|s| s.to_string()).collect();
            assert!(decode(r, 9, Pos::new(2, 3)).is_err());
        }
        let mut r = reply(parts);
        let end = r.body.len() - 1;
        r.body[end] = 255;
        assert!(decode(r, 9, Pos::new(2, 3)).is_err());
        let mut r = reply(parts);
        r.body.push(0);
        r.fields[1] = r.body.len().to_string();
        assert!(decode(r, 9, Pos::new(2, 3)).is_err());
        for s in ["", "+1", "-1", "1x", "9223372036854775808"] {
            assert!(decimal(s).is_none());
        }
    }
}
