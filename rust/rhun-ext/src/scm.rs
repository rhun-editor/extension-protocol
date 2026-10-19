//! Source control (protocol 1.1): Git data through rhun (feature `git-data`, `[needs] git = read`)
//! and commit-message providers (feature `commit-message`, [`crate::Handler::commit_message`]).
//!
//! rhun runs git itself, so the trust pause holds and the extension needs no `run = git`: every
//! request answers `refused` in a folder a `rhun://` link opened until the user trusts it.
use crate::{Doc, Error, Event, Ext, Feature};

/// What HEAD is.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum GitHead {
    Branch,
    Detached,
    /// A branch without commits yet.
    Unborn,
}

/// The work tree's state.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum GitState {
    Clean,
    Dirty,
    Merging,
    Rebasing,
}

/// `git-repo`: the repository of the project.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct GitRepo {
    /// The work tree's absolute path.
    pub root: String,
    /// The branch, or the commit's first 7 hex digits when detached.
    pub branch: String,
    /// Like `origin/main`; empty without one.
    pub upstream: String,
    pub head: GitHead,
    pub ahead: u64,
    pub behind: u64,
    pub state: GitState,
}

/// One `git-status` entry: git's two status columns (`.` for an unchanged side) and the path
/// relative to the work tree (an untracked folder ends in `/`).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct GitStatusEntry {
    pub x: u8,
    pub y: u8,
    pub path: String,
}

/// A commit of a blame.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BlameCommit {
    pub sha: String,
    /// Author time, seconds since 1970.
    pub time: u64,
    pub author: String,
    pub mail: String,
    pub summary: String,
}

/// Lines `l1` to `l2` (one-based, in the document) come from commit `sha`, where they started at
/// line `orig`. Lines not committed carry the zero SHA.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BlameRun {
    pub sha: String,
    pub l1: u32,
    pub l2: u32,
    pub orig: u32,
}

/// `git-blame`: the blame of a document's current text, unsaved edits included.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Blame {
    pub commits: Vec<BlameCommit>,
    pub runs: Vec<BlameRun>,
}

impl Blame {
    /// The commit that wrote a line (one-based), if the blame covers it.
    pub fn line(&self, line: u32) -> Option<&BlameCommit> {
        let run = self.runs.iter().find(|r| r.l1 <= line && line <= r.l2)?;
        self.commits.iter().find(|c| c.sha == run.sha)
    }
}

/// Whether a SHA is the zero SHA of lines not committed.
pub fn is_uncommitted(sha: &str) -> bool {
    !sha.is_empty() && sha.bytes().all(|b| b == b'0')
}

/// A commit of `git-log`, newest first.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct LogEntry {
    pub sha: String,
    /// Empty for a root commit.
    pub parents: Vec<String>,
    pub time: u64,
    pub author: String,
    pub subject: String,
}

/// Which history `git-log` lists.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LogOf<'a> {
    /// The history of HEAD.
    Head,
    /// The commits that touched a file (absolute, or relative to the work tree), following renames.
    File(&'a str),
    /// The commits that touched lines `l1` to `l2` of a file as it is at HEAD.
    Lines(&'a str, u32, u32),
}

/// `commit-message`: rhun asks for a message for the branch and the bounded diff (the staged
/// changes, else what Commit All would take). [`crate::Handler::commit_message`] answers it; a
/// `cancel` event names `id`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CommitMessageRequest {
    pub id: u64,
    pub branch: String,
    pub diff: String,
}

impl CommitMessageRequest {
    /// The request's body: `BRANCH_LEN DIFF_LEN` LF `<BRANCH><DIFF>`.
    pub fn parse(id: u64, body: &[u8]) -> Option<CommitMessageRequest> {
        let mut p = Parser::new(body);
        let (bl, dl) = (p.num()? as usize, p.num()? as usize);
        p.eol()?;
        let branch = p.bytes(bl)?;
        let diff = p.bytes(dl)?;
        p.end()?;
        Some(CommitMessageRequest { id, branch, diff })
    }
}

/// `quickdiff`: rhun asks for a file's base text (feature `quickdiff`,
/// [`crate::Handler::quickdiff`]): the bytes rhun compares the document with for the gutter marks,
/// or `None` to leave the marks to git.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct QuickDiffRequest {
    pub id: u64,
    pub doc: Doc,
    pub version: i64,
}

impl QuickDiffRequest {
    /// `DOC VERSION`, the fields after the verb.
    pub fn parse(id: u64, fields: &[String]) -> Option<QuickDiffRequest> {
        if fields.len() != 2 {
            return None;
        }
        Some(QuickDiffRequest {
            id,
            doc: fields[0].parse().ok().filter(|d: &i64| *d > 0)?,
            version: fields[1].parse().ok().filter(|v: &i64| *v >= 0)?,
        })
    }
}

/// The checks rhun applies to a commit message: trimmed of spaces, tabs and line breaks, 1 to
/// 8192 bytes, no control characters but tab, CR and LF. `Err` says why.
pub fn check_commit_message(message: &str) -> Result<&str, String> {
    let m = message.trim_matches(|c| c == ' ' || c == '\t' || c == '\r' || c == '\n');
    if m.is_empty() || m.len() > 8192 {
        return Err(format!("a commit message of {} bytes (1 to 8192)", m.len()));
    }
    if m.bytes()
        .any(|b| (b < 0x20 && b != b'\t' && b != b'\r' && b != b'\n') || b == 0x7f)
    {
        return Err("a control character in the commit message".into());
    }
    Ok(m)
}

/// Whether an event is `0 git changed`: the status, branch or HEAD changed; ask again.
pub fn is_git_changed(event: &Event) -> bool {
    event.name == "git" && event.fields.len() == 1 && event.fields[0] == "changed"
}

/// A revision rhun hands git: 1 to 200 bytes of letters, digits and `. _ / ~ ^ @ { } + -`, not
/// starting with `-`.
pub fn valid_rev(rev: &str) -> bool {
    !rev.is_empty()
        && rev.len() <= 200
        && !rev.starts_with('-')
        && rev
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"._/~^@{}+-".contains(&b))
}

fn invalid(what: &str) -> Error {
    Error {
        code: "syntax".into(),
        text: format!("invalid {what} reply"),
    }
}

fn unagreed() -> Error {
    Error {
        code: "unknown".into(),
        text: "git-data was not negotiated".into(),
    }
}

impl Ext {
    fn git_data(&self) -> Result<(), Error> {
        if self.has(Feature::GitData) {
            Ok(())
        } else {
            Err(unagreed())
        }
    }

    /// The project's repository (`git-repo`).
    pub fn git_repo(&self) -> Result<GitRepo, Error> {
        self.git_data()?;
        let r = self.request("git-repo", None)?;
        parse_git_repo(&r.body).ok_or_else(|| invalid("git-repo"))
    }

    /// The status (`git-status`), in git's order.
    pub fn git_status(&self) -> Result<Vec<GitStatusEntry>, Error> {
        self.git_data()?;
        let r = self.request("git-status", None)?;
        parse_git_status(&r.body).ok_or_else(|| invalid("git-status"))
    }

    /// The blame of an open document's current text (`git-blame`).
    pub fn git_blame(&self, doc: Doc) -> Result<Blame, Error> {
        self.git_data()?;
        let r = self.request(&format!("git-blame {doc}"), None)?;
        parse_blame(&r.body).ok_or_else(|| invalid("git-blame"))
    }

    /// At most `max` (1 to 1000) commits of a history (`git-log`).
    pub fn git_log(&self, of: LogOf, max: u32) -> Result<Vec<LogEntry>, Error> {
        self.git_data()?;
        let (path, l1, l2) = match of {
            LogOf::Head => ("", 0, 0),
            LogOf::File(p) => (p, 0, 0),
            LogOf::Lines(p, a, b) => (p, a, b),
        };
        let r = self.request(&format!("git-log {max} {l1} {l2}"), Some(path.as_bytes()))?;
        parse_log(&r.body).ok_or_else(|| invalid("git-log"))
    }

    /// A file's bytes at a revision (`git-show`), at most 4 MiB.
    pub fn git_show(&self, rev: &str, path: &str) -> Result<Vec<u8>, Error> {
        self.git_data()?;
        if !valid_rev(rev) {
            return Err(Error {
                code: "syntax".into(),
                text: "invalid revision".into(),
            });
        }
        Ok(self
            .request(&format!("git-show {rev}"), Some(path.as_bytes()))?
            .body)
    }

    /// Asks rhun to ask again for the quick-diff bases of a file (absolute path), or of every file
    /// with `None` (feature `quickdiff`), for example after the extension's own commit.
    pub fn quickdiff_refresh(&self, path: Option<&str>) -> Result<(), Error> {
        if !self.has(Feature::QuickDiff) {
            return Err(Error {
                code: "unknown".into(),
                text: "quickdiff was not negotiated".into(),
            });
        }
        self.request("quickdiff-refresh", Some(path.unwrap_or("-").as_bytes()))?;
        Ok(())
    }

    /// Opens rhun's diff view of a file from `rev1` to `rev2` (`None`: the file in the work tree).
    pub fn git_diff_open(&self, rev1: &str, rev2: Option<&str>, path: &str) -> Result<(), Error> {
        self.git_data()?;
        let rev2 = rev2.unwrap_or("-");
        if !valid_rev(rev1) || (rev2 != "-" && !valid_rev(rev2)) {
            return Err(Error {
                code: "syntax".into(),
                text: "invalid revision".into(),
            });
        }
        self.request(
            &format!("git-diff-open {rev1} {rev2}"),
            Some(path.as_bytes()),
        )?;
        Ok(())
    }
}

/// A reader of length-framed bodies: numbers separated by spaces, LF, then raw bytes.
struct Parser<'a> {
    b: &'a [u8],
    pos: usize,
}

impl<'a> Parser<'a> {
    fn new(b: &'a [u8]) -> Self {
        Parser { b, pos: 0 }
    }
    /// The next space-separated word of the header line (not past its LF).
    fn word(&mut self) -> Option<&'a str> {
        if self.pos < self.b.len() && self.b[self.pos] == b' ' {
            self.pos += 1;
        }
        let start = self.pos;
        while self.pos < self.b.len() && self.b[self.pos] != b' ' && self.b[self.pos] != b'\n' {
            self.pos += 1;
        }
        if self.pos == start {
            return None;
        }
        std::str::from_utf8(&self.b[start..self.pos]).ok()
    }
    fn num(&mut self) -> Option<u64> {
        let w = self.word()?;
        if w.len() > 19 || !w.bytes().all(|c| c.is_ascii_digit()) {
            return None;
        }
        w.parse().ok()
    }
    fn eol(&mut self) -> Option<()> {
        if self.pos < self.b.len() && self.b[self.pos] == b'\n' {
            self.pos += 1;
            Some(())
        } else {
            None
        }
    }
    fn bytes(&mut self, n: usize) -> Option<String> {
        let end = self.pos.checked_add(n)?;
        if end > self.b.len() {
            return None;
        }
        let s = String::from_utf8_lossy(&self.b[self.pos..end]).into_owned();
        self.pos = end;
        Some(s)
    }
    fn end(&self) -> Option<()> {
        (self.pos == self.b.len()).then_some(())
    }
}

/// Decodes a `git-repo` body.
pub fn parse_git_repo(body: &[u8]) -> Option<GitRepo> {
    let mut p = Parser::new(body);
    let (rl, bl, ul) = (p.num()? as usize, p.num()? as usize, p.num()? as usize);
    let head = match p.word()? {
        "branch" => GitHead::Branch,
        "detached" => GitHead::Detached,
        "unborn" => GitHead::Unborn,
        _ => return None,
    };
    let (ahead, behind) = (p.num()?, p.num()?);
    let state = match p.word()? {
        "clean" => GitState::Clean,
        "dirty" => GitState::Dirty,
        "merging" => GitState::Merging,
        "rebasing" => GitState::Rebasing,
        _ => return None,
    };
    p.eol()?;
    let root = p.bytes(rl)?;
    let branch = p.bytes(bl)?;
    let upstream = p.bytes(ul)?;
    p.end()?;
    Some(GitRepo {
        root,
        branch,
        upstream,
        head,
        ahead,
        behind,
        state,
    })
}

/// Decodes a `git-status` body.
pub fn parse_git_status(body: &[u8]) -> Option<Vec<GitStatusEntry>> {
    let mut p = Parser::new(body);
    let n = p.num()?;
    p.eol()?;
    let mut out = Vec::new();
    for _ in 0..n {
        let xy = p.word()?.as_bytes();
        if xy.len() != 2 {
            return None;
        }
        let len = p.num()? as usize;
        p.eol()?;
        out.push(GitStatusEntry {
            x: xy[0],
            y: xy[1],
            path: p.bytes(len)?,
        });
    }
    p.end()?;
    Some(out)
}

/// Decodes a `git-blame` body.
pub fn parse_blame(body: &[u8]) -> Option<Blame> {
    let mut p = Parser::new(body);
    let (nc, nr) = (p.num()?, p.num()?);
    p.eol()?;
    let mut blame = Blame::default();
    for _ in 0..nc {
        let sha = p.word()?.to_string();
        let time = p.num()?;
        let (al, ml, sl) = (p.num()? as usize, p.num()? as usize, p.num()? as usize);
        p.eol()?;
        blame.commits.push(BlameCommit {
            sha,
            time,
            author: p.bytes(al)?,
            mail: p.bytes(ml)?,
            summary: p.bytes(sl)?,
        });
    }
    for _ in 0..nr {
        let sha = p.word()?.to_string();
        let (l1, l2, orig) = (p.num()?, p.num()?, p.num()?);
        p.eol()?;
        blame.runs.push(BlameRun {
            sha,
            l1: u32::try_from(l1).ok()?,
            l2: u32::try_from(l2).ok()?,
            orig: u32::try_from(orig).ok()?,
        });
    }
    p.end()?;
    Some(blame)
}

/// Decodes a `git-log` body.
pub fn parse_log(body: &[u8]) -> Option<Vec<LogEntry>> {
    let mut p = Parser::new(body);
    let n = p.num()?;
    p.eol()?;
    let mut out = Vec::new();
    for _ in 0..n {
        let sha = p.word()?.to_string();
        let parents = match p.word()? {
            "-" => Vec::new(),
            list => list.split(',').map(str::to_string).collect(),
        };
        let time = p.num()?;
        let (al, sl) = (p.num()? as usize, p.num()? as usize);
        p.eol()?;
        out.push(LogEntry {
            sha,
            parents,
            time,
            author: p.bytes(al)?,
            subject: p.bytes(sl)?,
        });
    }
    p.end()?;
    Some(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn repo_and_status() {
        let repo = parse_git_repo(b"4 4 11 branch 1 2 dirty\n/tmpmainorigin/main").unwrap();
        assert_eq!(repo.root, "/tmp");
        assert_eq!(repo.branch, "main");
        assert_eq!(repo.upstream, "origin/main");
        assert_eq!(
            (repo.head, repo.ahead, repo.behind, repo.state),
            (GitHead::Branch, 1, 2, GitState::Dirty)
        );
        assert!(parse_git_repo(b"4 4 11 branch 1 2 dirty\n/tmpmainorigin/mainx").is_none());
        let st = parse_git_status(b"2\n.M 5\na.txt?? 12\nnew file.txt").unwrap();
        assert_eq!(
            st[0],
            GitStatusEntry {
                x: b'.',
                y: b'M',
                path: "a.txt".into()
            }
        );
        assert_eq!(st[1].path, "new file.txt");
        assert!(parse_git_status(b"2\n.M 5\na.txt").is_none());
    }

    #[test]
    fn blame_and_log() {
        let z = "0".repeat(40);
        let a = "a".repeat(40);
        let body = format!(
            "2 2\n{a} 100 3 7 5\nAdaada@x.yFirst{z} 200 3 3 4\nNotnotWork{a} 1 2 1\n{z} 3 3 3\n"
        );
        let b = parse_blame(body.as_bytes()).unwrap();
        assert_eq!(b.commits[0].author, "Ada");
        assert_eq!(b.commits[0].mail, "ada@x.y");
        assert_eq!(b.line(2).unwrap().summary, "First");
        assert!(is_uncommitted(&b.runs[1].sha));
        assert!(b.line(4).is_none());
        let log = parse_log(format!("2\n{a} {z} 10 3 4\nAdaMore{z} - 9 3 5\nAdaFirst").as_bytes())
            .unwrap();
        assert_eq!(log[0].parents, vec![z.clone()]);
        assert!(log[1].parents.is_empty());
        assert_eq!(log[1].subject, "First");
    }

    #[test]
    fn commit_message_requests_and_checks() {
        let r = CommitMessageRequest::parse(3, b"4 6\nmain+a\n-b\n").unwrap();
        assert_eq!(
            (r.id, r.branch.as_str(), r.diff.as_str()),
            (3, "main", "+a\n-b\n")
        );
        assert!(CommitMessageRequest::parse(3, b"4 7\nmain+a\n-b\n").is_none());
        assert_eq!(
            check_commit_message("  Add x\n\nBody\n").unwrap(),
            "Add x\n\nBody"
        );
        assert!(check_commit_message("\x1b[31mred").is_err());
        assert!(check_commit_message(" \n").is_err());
        assert!(
            valid_rev("HEAD~2") && valid_rev("main@{1}") && !valid_rev("-x") && !valid_rev("a:b")
        );
    }
}
