//! Feature `files` (`[needs] project = read`): [`Ext::find_files`] lists the workspace's files by
//! glob, [`Ext::search_text`] searches their text, through rhun (git's ignore rules where git
//! runs, the exclude setting, unsaved editor text). Results come in batches while the request runs.
use crate::{Doc, Error, Ext, Feature, StreamItem};

/// [`FileQuery::flags`]: ignored files too (no `.gitignore`, no exclude setting).
pub const IGNORED: u32 = 1;
/// Skip names starting with `.`.
pub const SKIP_HIDDEN: u32 = 2;
/// Follow links to folders.
pub const FOLLOW_LINKS: u32 = 4;
/// find-files: folders too, with a trailing `/`.
pub const FOLDERS: u32 = 8;
/// search-text: Unicode simple case folding.
pub const IGNORE_CASE: u32 = 16;
/// search-text: whole words only.
pub const WHOLE_WORD: u32 = 32;
const REGEX: u32 = 64;

/// Where to look: `folder` a folder id (1 the primary) or 0 for all; `max` 1 to 100000 paths
/// (find) or 1 to 20000 matches (search); globs relative to the folder (`*` `**` `?` `[..]`
/// `{a,b}`; without `/` they match the base name).
#[derive(Clone, Debug, Default)]
pub struct FileQuery {
    pub folder: u64,
    pub max: u32,
    pub flags: u32,
    pub include: Vec<String>,
    pub exclude: Vec<String>,
}

impl FileQuery {
    fn globs(&self) -> Result<Vec<u8>, Error> {
        let mut b = Vec::new();
        for (sign, list) in [(b'+', &self.include), (b'-', &self.exclude)] {
            for g in list {
                if g.is_empty() || g.contains(['\n', '\r']) {
                    return Err(Error {
                        code: "syntax".into(),
                        text: "an empty glob or one with a line break".into(),
                    });
                }
                b.push(sign);
                b.extend_from_slice(g.as_bytes());
                b.push(b'\n');
            }
        }
        Ok(b)
    }
}

/// A literal (1 to 4096 bytes without LF) or an RNRX program (compiled elsewhere, for instance by
/// the Go SDK's `CompileSearchRegex`).
#[derive(Clone, Debug)]
pub enum Pattern {
    Literal(String),
    Program(Vec<u8>),
}

/// One find-files record; a folder's path ends with `/`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct FoundPath {
    pub folder: u64,
    pub path: String,
}

/// A match: `col..end` is the half-open byte range on `line` (one-based); `text` is the line, or
/// 512 bytes of a long one, whose first byte is at column `pre`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SearchMatch {
    pub line: u32,
    pub col: u32,
    pub end: u32,
    pub pre: u32,
    pub text: String,
}

/// A file's matches; `doc` and `version` are the open document that was searched, or 0.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct FoundFile {
    pub folder: u64,
    pub doc: Doc,
    pub version: i64,
    pub path: String,
    pub matches: Vec<SearchMatch>,
}

/// A run's reply: `count` paths or matches, `files` with matches, `skipped` (too big or binary),
/// `status` complete, limit or partial.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Finished {
    pub count: u64,
    pub files: u64,
    pub skipped: u64,
    pub status: String,
}

fn bad(text: &str) -> Error {
    Error {
        code: "syntax".into(),
        text: text.into(),
    }
}

impl Ext {
    /// Lists files; `on_batch` gets each batch as it comes and returns false to stop the run (it
    /// is cancelled: the error's code is `cancelled`).
    pub fn find_files(
        &self,
        q: &FileQuery,
        mut on_batch: impl FnMut(&[FoundPath]) -> bool,
    ) -> Result<Finished, Error> {
        if !self.has(Feature::Files) {
            return Err(Error {
                code: "unknown".into(),
                text: "files was not negotiated".into(),
            });
        }
        let body = q.globs()?;
        let reply = self.run_files("find-files", q, q.flags, &body, |b| {
            let paths = parse_found_paths(b)?;
            Ok(on_batch(&paths))
        })?;
        match reply.fields.as_slice() {
            [n, status] => Ok(Finished {
                count: n.parse().unwrap_or(0),
                status: status.clone(),
                ..Default::default()
            }),
            _ => Err(bad("invalid find-files answer")),
        }
    }

    /// Searches the files' text (an open document's where there is one).
    pub fn search_text(
        &self,
        pattern: &Pattern,
        q: &FileQuery,
        mut on_batch: impl FnMut(&[FoundFile]) -> bool,
    ) -> Result<Finished, Error> {
        if !self.has(Feature::Files) {
            return Err(Error {
                code: "unknown".into(),
                text: "files was not negotiated".into(),
            });
        }
        let globs = q.globs()?;
        let (pat, flags) = match pattern {
            Pattern::Literal(s) => {
                if s.is_empty() || s.len() > 4096 || s.contains('\n') {
                    return Err(bad("a literal is 1 to 4096 bytes without LF"));
                }
                (s.as_bytes(), q.flags & !REGEX)
            }
            Pattern::Program(p) => (p.as_slice(), q.flags | REGEX),
        };
        let mut body = format!("{} {}\n", pat.len(), globs.len()).into_bytes();
        body.extend_from_slice(pat);
        body.extend_from_slice(&globs);
        let reply = self.run_files("search-text", q, flags, &body, |b| {
            let files = parse_found_files(b)?;
            Ok(on_batch(&files))
        })?;
        match reply.fields.as_slice() {
            [n, f, s, status] => Ok(Finished {
                count: n.parse().unwrap_or(0),
                files: f.parse().unwrap_or(0),
                skipped: s.parse().unwrap_or(0),
                status: status.clone(),
            }),
            _ => Err(bad("invalid search-text answer")),
        }
    }

    fn run_files(
        &self,
        verb: &str,
        q: &FileQuery,
        flags: u32,
        body: &[u8],
        mut batch: impl FnMut(&[u8]) -> Result<bool, Error>,
    ) -> Result<crate::Reply, Error> {
        let (id, rx) =
            self.request_stream(&format!("{verb} {} {} {flags}", q.folder, q.max), body)?;
        let mut cancelled = false;
        let mut failure = None;
        loop {
            match rx.recv() {
                Ok(StreamItem::Batch(b)) => {
                    if cancelled || failure.is_some() {
                        continue;
                    }
                    let more = match batch(&b) {
                        Ok(more) => more,
                        Err(e) => {
                            failure = Some(e);
                            false
                        }
                    };
                    if !more {
                        cancelled = true;
                        let _ = self.request(&format!("cancel {id}"), None);
                    }
                }
                Ok(StreamItem::Done(res)) => {
                    if let Some(e) = failure {
                        return Err(e);
                    }
                    return res;
                }
                Err(_) => {
                    return Err(Error {
                        code: "closed".into(),
                        text: "rhun went away".into(),
                    })
                }
            }
        }
    }
}

struct Framed<'a> {
    b: &'a [u8],
    at: usize,
}

impl<'a> Framed<'a> {
    fn line(&mut self) -> Result<&'a str, Error> {
        let rest = &self.b[self.at..];
        let i = rest
            .iter()
            .position(|&c| c == b'\n')
            .ok_or_else(|| bad("found: a record without its line"))?;
        self.at += i + 1;
        std::str::from_utf8(&rest[..i]).map_err(|_| bad("found: not UTF-8"))
    }
    fn numbers(&mut self, n: usize) -> Result<Vec<u64>, Error> {
        let l = self.line()?;
        let v: Vec<u64> = l.split(' ').filter_map(|x| x.parse().ok()).collect();
        if v.len() != n || l.split(' ').count() != n {
            return Err(bad("found: a record line with the wrong fields"));
        }
        Ok(v)
    }
    fn bytes(&mut self, n: u64) -> Result<String, Error> {
        let n = usize::try_from(n).map_err(|_| bad("found: a length past the batch"))?;
        if self.at + n > self.b.len() {
            return Err(bad("found: a length past the batch"));
        }
        let s = String::from_utf8_lossy(&self.b[self.at..self.at + n]).into_owned();
        self.at += n;
        Ok(s)
    }
    fn end(&self) -> Result<(), Error> {
        if self.at == self.b.len() {
            Ok(())
        } else {
            Err(bad("found: bytes after the records"))
        }
    }
}

/// A find-files batch: `N` LF, then `N` records `FOLDER PATH_LEN` LF `PATH`.
pub fn parse_found_paths(b: &[u8]) -> Result<Vec<FoundPath>, Error> {
    let mut r = Framed { b, at: 0 };
    let n = r.numbers(1)?[0];
    let mut out = Vec::new();
    for _ in 0..n {
        let f = r.numbers(2)?;
        out.push(FoundPath {
            folder: f[0],
            path: r.bytes(f[1])?,
        });
    }
    r.end()?;
    Ok(out)
}

/// A search-text batch: `N` LF, then `N` groups `FOLDER DOC VERSION PATH_LEN M` LF `PATH`, each
/// followed by `M` matches `LINE COL END PRE TEXT_LEN` LF `TEXT`.
pub fn parse_found_files(b: &[u8]) -> Result<Vec<FoundFile>, Error> {
    let mut r = Framed { b, at: 0 };
    let n = r.numbers(1)?[0];
    let mut out = Vec::new();
    for _ in 0..n {
        let f = r.numbers(5)?;
        let mut file = FoundFile {
            folder: f[0],
            doc: f[1] as Doc,
            version: f[2] as i64,
            path: r.bytes(f[3])?,
            matches: Vec::new(),
        };
        for _ in 0..f[4] {
            let m = r.numbers(5)?;
            file.matches.push(SearchMatch {
                line: m[0] as u32,
                col: m[1] as u32,
                end: m[2] as u32,
                pre: m[3] as u32,
                text: r.bytes(m[4])?,
            });
        }
        out.push(file);
    }
    r.end()?;
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn batches_parse() {
        let p = parse_found_paths(b"2\n1 7\nmain.go1 4\nsrc/").unwrap();
        assert_eq!(
            p[1],
            FoundPath {
                folder: 1,
                path: "src/".into()
            }
        );
        let f = parse_found_files(b"1\n1 3 7 7 1\nmain.go2 4 8 1 7\n//TODO!").unwrap();
        assert_eq!(f[0].doc, 3);
        assert_eq!(f[0].matches[0].text, "//TODO!");
        for b in [&b""[..], b"1\n", b"1\n1 9\nab", b"1\n1 2\nab!"] {
            assert!(parse_found_paths(b).is_err());
        }
    }
}
