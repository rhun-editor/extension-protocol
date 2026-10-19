//! Ordered workspace edits (LSP's `documentChanges`) mapped onto rhun's phases (features
//! `workspace-edits` and `file-operations`).
//!
//! LSP applies its changes one after the other: text edits of a file, creates, renames and
//! deletes in any order, each against the files as the changes before it left them. rhun applies
//! a workspace edit in phases: the creates, then the text groups (naming files as they are after
//! the creates and before the renames), then the renames, then the deletes. [`from_ordered`] turns
//! the one into the other: a text edit that follows a rename names the file by its name before
//! the rename, repeated edits of one file become one group (the later ones' positions mapped back
//! onto the first one's text), edits of a file a later delete removes are left out, and what
//! rhun's phases cannot express is refused with the number of the change at fault.
//!
//! [`locate`] says, for an extension converting positions as it goes (UTF-16 to bytes), which
//! text an edit at some point of the sequence refers to.

use std::collections::HashMap;
use std::fmt;

use crate::lang::{
    Create, Delete, Entry, FileEdit, FileOp, Rename, Target, TextEdit, WorkspaceEdit,
};
use crate::Pos;

/// One step of an ordered workspace edit.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Change {
    /// Text edits of a file, in rhun's positions (byte columns) against the file's text at this
    /// point of the sequence ([`locate`]); all of one step against the same text, not
    /// overlapping. `version` names the open document's version, `hash` the file on disk
    /// ([`crate::lang::fnv1a64`]); neither is `-`. Only the first step of a file sets the group's
    /// precondition; a file an earlier step creates takes `-`.
    Edit {
        path: String,
        version: Option<i64>,
        hash: Option<u64>,
        edits: Vec<TextEdit>,
    },
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

/// Why an ordered edit cannot be made in rhun's phases: the 0-based index of the change at
/// fault and a sentence.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct OrderError {
    pub change: usize,
    pub text: String,
}

impl fmt::Display for OrderError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "change {}: {}", self.change, self.text)
    }
}

impl std::error::Error for OrderError {}

/// Where the text of a file an edit names comes from at some point of the sequence.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Located {
    /// The file's name before the sequence (after rewriting the renames before that point):
    /// read its open document or the file on disk.
    pub path: String,
    /// An earlier step created it: its text starts empty.
    pub created: bool,
}

fn is_sep(c: u8) -> bool {
    c == b'/' || (cfg!(windows) && c == b'\\')
}

fn trim(p: &str) -> &str {
    let b = p.as_bytes();
    let mut n = b.len();
    while n > 1 && is_sep(b[n - 1]) {
        n -= 1;
    }
    &p[..n]
}

fn same(a: &str, b: &str) -> bool {
    trim(a) == trim(b)
}

/// The rest of `path` after the folder `dir` (starting with its separator), when it is inside it.
fn under<'a>(path: &'a str, dir: &str) -> Option<&'a str> {
    let d = trim(dir);
    if path.len() > d.len() && path.starts_with(d) && is_sep(path.as_bytes()[d.len()]) {
        Some(&path[d.len()..])
    } else {
        None
    }
}

/// Where the text of `path` comes from after `before`: the changes are walked back, rewriting a
/// rename's target to its source, and stop at a create of it; an earlier rename away or delete
/// of it (or of a folder holding it) is an error.
pub fn locate(before: &[Change], path: &str) -> Result<Located, String> {
    let mut p = path.to_string();
    for c in before.iter().rev() {
        match c {
            Change::Rename { from, to, .. } => {
                if same(&p, to) {
                    p = from.clone();
                } else if let Some(rest) = under(&p, to) {
                    p = format!("{}{}", trim(from), rest);
                } else if same(&p, from) || under(&p, from).is_some() {
                    return Err(format!(
                        "an edit to {path} after {from} was renamed to {to}"
                    ));
                }
            }
            Change::Delete { path: d, .. } => {
                if same(&p, d) || under(&p, d).is_some() {
                    return Err(format!("an edit to {path} after {d} was deleted"));
                }
            }
            Change::Create { path: c, .. } => {
                if same(&p, c) {
                    return Ok(Located {
                        path: p,
                        created: true,
                    });
                }
            }
            Change::Edit { .. } => {}
        }
    }
    Ok(Located {
        path: p,
        created: false,
    })
}

/// [`from_ordered_origins`] without the origins.
pub fn from_ordered(changes: &[Change]) -> Result<WorkspaceEdit, OrderError> {
    from_ordered_origins(changes).map(|r| r.0)
}

/// The workspace edit for an ordered list of changes, and for each entry of its body (in the
/// order [`WorkspaceEdit::entry`] numbers them) the index of the change it came from (a merged
/// group's first): an apply error's ENTRY `n` came from `origins[n - 1]`.
pub fn from_ordered_origins(changes: &[Change]) -> Result<(WorkspaceEdit, Vec<usize>), OrderError> {
    struct Group {
        file: FileEdit,
        first: usize,
    }
    let fail = |change: usize, text: String| OrderError { change, text };
    let mut groups: Vec<Group> = Vec::new();
    let mut by_path: HashMap<String, usize> = HashMap::new();
    let mut ops: Vec<(FileOp, usize)> = Vec::new();
    for (i, c) in changes.iter().enumerate() {
        // the paths of a file operation: none may be another's, nor inside a folder another
        // renames or deletes
        let paths: Vec<&str> = match c {
            Change::Edit { .. } => Vec::new(),
            Change::Create { path, .. } | Change::Delete { path, .. } => vec![path],
            Change::Rename { from, to, .. } => vec![from, to],
        };
        for p in &paths {
            if p.is_empty() {
                return Err(fail(i, "a file operation without a path".into()));
            }
            for (op, _) in &ops {
                let folders: Vec<&str> = match op {
                    FileOp::Create { .. } => Vec::new(),
                    FileOp::Rename { from, to, .. } => vec![from, to],
                    FileOp::Delete { path, .. } => vec![path],
                };
                let theirs: Vec<&str> = match op {
                    FileOp::Create { path, .. } | FileOp::Delete { path, .. } => vec![path],
                    FileOp::Rename { from, to, .. } => vec![from, to],
                };
                if theirs.iter().any(|q| same(p, q)) {
                    return Err(fail(i, format!("{p} is the path of two file operations")));
                }
                let mine_hold = matches!(c, Change::Rename { .. } | Change::Delete { .. });
                if folders.iter().any(|q| under(p, q).is_some())
                    || (mine_hold && theirs.iter().any(|q| under(q, p).is_some()))
                {
                    return Err(fail(
                        i,
                        format!("{p} and a folder another file operation renames or deletes"),
                    ));
                }
            }
        }
        match c {
            Change::Edit {
                path,
                version,
                hash,
                edits,
            } => {
                let at = locate(&changes[..i], path).map_err(|t| fail(i, t))?;
                if let Some(&g) = by_path.get(trim(&at.path)) {
                    let merged = compose(&groups[g].file.edits, edits)
                        .map_err(|t| fail(i, format!("{}: {t}", at.path)))?;
                    groups[g].file.edits = merged;
                } else {
                    let (version, hash) = if at.created {
                        (None, None)
                    } else {
                        (*version, *hash)
                    };
                    by_path.insert(trim(&at.path).to_string(), groups.len());
                    groups.push(Group {
                        file: FileEdit {
                            target: Target::Path(at.path),
                            version,
                            edits: edits.clone(),
                            hash,
                        },
                        first: i,
                    });
                }
            }
            Change::Create { path, opts } => {
                if by_path.contains_key(trim(path)) {
                    return Err(fail(i, format!("{path} is created after an edit of it")));
                }
                ops.push((
                    FileOp::Create {
                        path: path.clone(),
                        opts: *opts,
                    },
                    i,
                ));
            }
            Change::Rename { from, to, opts } => {
                if by_path.contains_key(trim(to)) {
                    return Err(fail(
                        i,
                        format!("{from} is renamed onto {to} after an edit of {to}"),
                    ));
                }
                ops.push((
                    FileOp::Rename {
                        from: from.clone(),
                        to: to.clone(),
                        opts: *opts,
                    },
                    i,
                ));
            }
            Change::Delete { path, opts } => {
                ops.push((
                    FileOp::Delete {
                        path: path.clone(),
                        opts: *opts,
                    },
                    i,
                ));
            }
        }
    }
    // edits of a file that is deleted afterwards change nothing anyone sees: left out (rhun
    // refuses a text group for a deleted path)
    groups.retain(|g| {
        let Target::Path(p) = &g.file.target else {
            return true;
        };
        !ops.iter()
            .any(|(op, _)| matches!(op, FileOp::Delete { path, .. } if same(path, p)))
    });
    let edit = WorkspaceEdit {
        files: groups.iter().map(|g| g.file.clone()).collect(),
        ops: ops.iter().map(|o| o.0.clone()).collect(),
        label: None,
        confirm: false,
    };
    let origins = edit
        .order()
        .into_iter()
        .map(|e| match e {
            Entry::File(i) => groups[i].first,
            Entry::Op(i) => ops[i].1,
        })
        .collect();
    Ok((edit, origins))
}

/// Where a text puts the position after it when inserted at `p`.
fn advance(p: Pos, text: &str) -> Pos {
    match text.rfind('\n') {
        None => Pos::new(p.line, p.col + text.len() as u32),
        Some(i) => Pos::new(
            p.line + text.bytes().filter(|&b| b == b'\n').count() as u32,
            (text.len() - i) as u32,
        ),
    }
}

/// A position at or after `oe` (the end of the last edit before it, in the text before) in the
/// text after, where that end is `pe`.
fn forward(p: Pos, oe: Pos, pe: Pos) -> Pos {
    if p.line == oe.line {
        Pos::new(pe.line, pe.col + (p.col - oe.col))
    } else {
        Pos::new(p.line - oe.line + pe.line, p.col)
    }
}

/// The inverse of [`forward`].
fn back(p: Pos, oe: Pos, pe: Pos) -> Pos {
    if p.line == pe.line {
        Pos::new(oe.line, oe.col + (p.col - pe.col))
    } else {
        Pos::new(p.line - pe.line + oe.line, p.col)
    }
}

/// One group of edits made against a text, then `next` against the text that left: the edits of
/// both against the first text, in an order rhun's stable sort keeps. An edit of `next` that
/// touches text `base` put in, or spans text it removed, cannot be one of them.
pub(crate) fn compose(base: &[TextEdit], next: &[TextEdit]) -> Result<Vec<TextEdit>, String> {
    let mut b: Vec<&TextEdit> = base.iter().collect();
    b.sort_by(|x, y| (x.range.start, x.range.end).cmp(&(y.range.start, y.range.end)));
    for w in b.windows(2) {
        if w[0].range.end > w[1].range.start {
            return Err("two edits overlap".into());
        }
    }
    for e in base.iter().chain(next) {
        if e.range.end < e.range.start || e.range.start.line == 0 || e.range.start.col == 0 {
            return Err("an edit's range ends before it starts".into());
        }
    }
    // where each edit of base is in the text after base
    let mut post = Vec::with_capacity(b.len());
    let (mut oe, mut pe) = (Pos::new(1, 1), Pos::new(1, 1));
    for e in &b {
        let ps = forward(e.range.start, oe, pe);
        let pend = advance(ps, &e.text);
        post.push((ps, pend));
        oe = e.range.end;
        pe = pend;
    }
    // a start where base removed text (and put none in) goes after what it removed, an end
    // before it, so the edit does not overlap base's
    let inv = |p: Pos, start: bool| -> Result<Pos, String> {
        let (mut oe, mut pe) = (Pos::new(1, 1), Pos::new(1, 1));
        for (k, e) in b.iter().enumerate() {
            let (ps, pend) = post[k];
            if p < ps {
                return Ok(back(p, oe, pe));
            }
            if p == ps && !(start && ps == pend) {
                return Ok(e.range.start);
            }
            if p < pend {
                return Err(format!(
                    "an edit at {}:{} lands in text an earlier edit of the file put in",
                    p.line, p.col
                ));
            }
            oe = e.range.end;
            pe = pend;
        }
        Ok(back(p, oe, pe))
    };
    // (start, end, rank, group, index): insertions at one place keep the order they had in the
    // text after base, the later edit's first when they meet
    let mut all: Vec<(Pos, Pos, Pos, u8, usize, TextEdit)> = Vec::new();
    for (k, e) in b.iter().enumerate() {
        all.push((e.range.start, e.range.end, post[k].0, 1, k, (*e).clone()));
    }
    for (k, e) in next.iter().enumerate() {
        let (a, z) = (e.range.start, e.range.end);
        for &(ps, pend) in &post {
            if (a < pend && ps < z) || (ps == pend && a < ps && ps < z) {
                return Err(format!(
                    "an edit from {}:{} to {}:{} overlaps an earlier edit of the file",
                    a.line, a.col, z.line, z.col
                ));
            }
        }
        let start = inv(a, true)?;
        let end = if a == z { start } else { inv(z, false)? };
        all.push((
            start,
            end,
            a,
            0,
            k,
            TextEdit {
                range: crate::Range::new(start, end),
                text: e.text.clone(),
            },
        ));
    }
    all.sort_by(|x, y| (x.0, x.1, x.2, x.3, x.4).cmp(&(y.0, y.1, y.2, y.3, y.4)));
    Ok(all.into_iter().map(|x| x.5).collect())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::Range;

    fn r(l1: u32, c1: u32, l2: u32, c2: u32) -> Range {
        Range::new(Pos::new(l1, c1), Pos::new(l2, c2))
    }

    fn te(l1: u32, c1: u32, l2: u32, c2: u32, t: &str) -> TextEdit {
        TextEdit::new(r(l1, c1, l2, c2), t)
    }

    /// The text after the edits, made as rhun makes them: sorted stably by start then end, from
    /// the last to the first.
    fn apply(text: &str, edits: &[TextEdit]) -> String {
        let off = |p: Pos| -> usize {
            let mut o = 0;
            for _ in 1..p.line {
                o += text[o..].find('\n').unwrap() + 1;
            }
            o + p.col as usize - 1
        };
        let mut es: Vec<&TextEdit> = edits.iter().collect();
        es.sort_by(|x, y| (x.range.start, x.range.end).cmp(&(y.range.start, y.range.end)));
        let mut out = text.to_string();
        for e in es.iter().rev() {
            out.replace_range(off(e.range.start)..off(e.range.end), &e.text);
        }
        out
    }

    fn edit(path: &str, edits: Vec<TextEdit>) -> Change {
        Change::Edit {
            path: path.into(),
            version: None,
            hash: Some(7),
            edits,
        }
    }

    #[test]
    fn composing_matches_making_the_groups_one_after_the_other() {
        let text = "alpha\nbeta\ngamma\n";
        let cases: Vec<(Vec<TextEdit>, Vec<TextEdit>)> = vec![
            // a line put in above; the second group edits what is now line 3
            (vec![te(1, 1, 1, 1, "zero\n")], vec![te(3, 1, 3, 5, "BETA")]),
            // a replacement, then an insertion right after it and one right before it
            (
                vec![te(2, 1, 2, 5, "B")],
                vec![te(2, 2, 2, 2, "!"), te(2, 1, 2, 1, "<")],
            ),
            // an insertion, then insertions before and after the text it put in
            (
                vec![te(1, 3, 1, 3, "XY")],
                vec![te(1, 3, 1, 3, "["), te(1, 5, 1, 5, "]")],
            ),
            // lines removed, then an edit on a line that moved up, on the same line as the join
            (
                vec![te(1, 3, 3, 2, "")],
                vec![te(1, 3, 1, 4, "M"), te(2, 1, 2, 1, "end")],
            ),
            // lines put in and an edit at the end of the text
            (
                vec![te(2, 1, 2, 1, "a\nb\n"), te(3, 6, 4, 1, "")],
                vec![te(5, 6, 5, 6, "tail")],
            ),
            // two edits on one line, then one between them
            (
                vec![te(3, 1, 3, 2, "GG"), te(3, 4, 3, 6, "")],
                vec![te(3, 3, 3, 4, "_")],
            ),
        ];
        for (base, next) in cases {
            let want = apply(&apply(text, &base), &next);
            let merged = compose(&base, &next).unwrap();
            assert_eq!(
                apply(text, &merged),
                want,
                "base {base:?} next {next:?} merged {merged:?}"
            );
        }
    }

    #[test]
    fn composing_refuses_edits_of_text_put_in_or_removed() {
        let base = vec![te(1, 3, 1, 3, "XYZ"), te(2, 1, 2, 3, "")];
        assert!(compose(&base, &[te(1, 4, 1, 4, "!")]).is_err());
        assert!(compose(&base, &[te(1, 2, 1, 5, "")]).is_err());
        assert!(compose(&base, &[te(1, 8, 2, 2, "")]).is_err());
        assert!(compose(&base, &[te(1, 6, 1, 6, "ok")]).is_ok());
    }

    #[test]
    fn edits_after_a_rename_name_the_file_before_it() {
        let changes = vec![
            edit("/p/a.rs", vec![te(1, 1, 1, 1, "x")]),
            Change::Rename {
                from: "/p/a.rs".into(),
                to: "/p/b.rs".into(),
                opts: Rename::default(),
            },
            edit("/p/b.rs", vec![te(1, 2, 1, 2, "y")]),
            edit("/p/c.rs", vec![te(1, 1, 1, 1, "c")]),
            Change::Create {
                path: "/p/new.rs".into(),
                opts: Create::default(),
            },
            edit("/p/new.rs", vec![te(1, 1, 1, 1, "fn main() {}\n")]),
            Change::Rename {
                from: "/p/dir".into(),
                to: "/p/dir2".into(),
                opts: Rename::default(),
            },
            edit("/p/dir2/m.rs", vec![te(1, 1, 1, 1, "m")]),
            edit("/p/gone.rs", vec![te(1, 1, 1, 1, "g")]),
            Change::Delete {
                path: "/p/gone.rs".into(),
                opts: Delete::default(),
            },
        ];
        let (e, origins) = from_ordered_origins(&changes).unwrap();
        let body = String::from_utf8(e.encode().unwrap()).unwrap();
        assert_eq!(
            body,
            "8\ncreate 9 -\n/p/new.rs7 fnv1a64:0000000000000007 2\n/p/a.rs1 1 1 1 1\nx1 1 1 1 1\ny\
             7 fnv1a64:0000000000000007 1\n/p/c.rs1 1 1 1 1\nc9 - 1\n/p/new.rs1 1 1 1 13\nfn main() {}\n\
             11 fnv1a64:0000000000000007 1\n/p/dir/m.rs1 1 1 1 1\nm\
             rename 7 7 -\n/p/a.rs/p/b.rsrename 6 7 -\n/p/dir/p/dir2delete 10 -\n/p/gone.rs"
        );
        assert_eq!(origins, vec![4, 0, 3, 5, 7, 1, 6, 9]);
        assert_eq!(e.entry(1), Some(Entry::Op(1)));
        assert_eq!(e.entry(2), Some(Entry::File(0)));
        assert_eq!(e.entry(9), None);
    }

    #[test]
    fn what_the_phases_cannot_express_is_refused() {
        let ren = |f: &str, t: &str| Change::Rename {
            from: f.into(),
            to: t.into(),
            opts: Rename::default(),
        };
        let del = |p: &str| Change::Delete {
            path: p.into(),
            opts: Delete::default(),
        };
        let x = || vec![te(1, 1, 1, 1, "x")];
        let bad: Vec<(Vec<Change>, usize, &str)> = vec![
            (
                vec![del("/p/a"), edit("/p/a", x())],
                1,
                "after /p/a was deleted",
            ),
            (
                vec![ren("/p/a", "/p/b"), edit("/p/a", x())],
                1,
                "after /p/a was renamed",
            ),
            (vec![del("/p/d"), edit("/p/d/f", x())], 1, "was deleted"),
            (
                vec![ren("/p/a", "/p/b"), ren("/p/b", "/p/c")],
                1,
                "two file operations",
            ),
            (
                vec![edit("/p/b", x()), ren("/p/a", "/p/b")],
                1,
                "after an edit of /p/b",
            ),
            (
                vec![
                    edit("/p/n", x()),
                    Change::Create {
                        path: "/p/n".into(),
                        opts: Create::default(),
                    },
                ],
                1,
                "created after",
            ),
            (vec![ren("/p/d", "/p/e"), del("/p/d/f")], 1, "folder"),
            (vec![del("/p/d/f"), del("/p/d")], 1, "folder"),
            (
                vec![
                    edit("/p/a", vec![te(1, 1, 1, 1, "XY")]),
                    edit("/p/a", vec![te(1, 2, 1, 2, "!")]),
                ],
                1,
                "earlier edit",
            ),
        ];
        for (changes, at, part) in bad {
            let err = from_ordered(&changes).unwrap_err();
            assert_eq!(err.change, at, "{changes:?}: {err}");
            assert!(err.text.contains(part), "{changes:?}: {err}");
        }
        // a create of a folder and of a file inside it is fine
        let ok = vec![
            Change::Create {
                path: "/p/d/".into(),
                opts: Create::default(),
            },
            Change::Create {
                path: "/p/d/f".into(),
                opts: Create {
                    overwrite: true,
                    ignore_existing: true,
                },
            },
        ];
        let body = from_ordered(&ok).unwrap().encode().unwrap();
        assert_eq!(
            body,
            b"2\ncreate 5 -\n/p/d/create 6 overwrite,ignore-existing\n/p/d/f".to_vec()
        );
    }

    #[test]
    fn locating_the_text_an_edit_refers_to() {
        let before = vec![
            Change::Rename {
                from: "/p/a".into(),
                to: "/p/b".into(),
                opts: Rename::default(),
            },
            Change::Create {
                path: "/p/c".into(),
                opts: Create::default(),
            },
        ];
        assert_eq!(
            locate(&before, "/p/b"),
            Ok(Located {
                path: "/p/a".into(),
                created: false
            })
        );
        assert_eq!(
            locate(&before, "/p/c"),
            Ok(Located {
                path: "/p/c".into(),
                created: true
            })
        );
        assert_eq!(
            locate(&before, "/p/z"),
            Ok(Located {
                path: "/p/z".into(),
                created: false
            })
        );
        assert!(locate(&before, "/p/a").is_err());
    }
}
