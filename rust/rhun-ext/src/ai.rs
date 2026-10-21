//! AI features of protocol 1.1 (`v1.1/v1.1.md`, "Agents, proposals and tools"): agent sessions an
//! extension lists in rhun's agents panel ([`Feature::Agents`]), edit proposals the user reviews
//! file by file ([`Feature::EditReview`]), tools extensions declare and call through rhun
//! ([`Feature::Tools`]), and prompts, Stop and cards for an extension's sessions
//! ([`Feature::AgentPrompts`]).
//!
//! ```no_run
//! use rhun_ext::{run, ai, Ext, Feature, Handler};
//!
//! struct Bridge;
//!
//! impl Handler for Bridge {
//!     fn ready(&mut self, ext: &Ext, project: Option<&str>) {
//!         let s = ai::Session::new("s1", "Fix the build").state(ai::SessionState::Working)
//!             .cwd(project.unwrap_or(""));
//!         let _ = ext.agent_sessions(&[s]);
//!     }
//!     fn agent_transcript(&mut self, _ext: &Ext, _sid: &str, from: u64) -> Result<ai::Transcript, ai::Refusal> {
//!         let all = vec![ai::Message::new(ai::Role::User, "fix the build")];
//!         Ok(ai::Transcript::page(&all, from))
//!     }
//! }
//!
//! fn main() {
//!     run(&[Feature::Agents], Bridge).unwrap();
//! }
//! ```

use crate::{Error, Ext, Reply, WorkspaceEdit};

/// At most this many rows in one [`Ext::agent_sessions`].
pub const MAX_SESSIONS: usize = 1000;
/// A session id: 1 to 128 bytes of visible ASCII.
pub const MAX_SID: usize = 128;
/// rhun cuts a message's text at 70 KB; [`Transcript::page`] stops before this many bytes.
pub const MAX_TEXT: usize = 71680;
/// Messages in one transcript answer.
pub const MAX_MESSAGES: usize = 2000;
/// Any answer rhun reads is at most 4 MB.
pub const MAX_BODY: usize = 4 << 20;
/// Proposals of one extension waiting for the user.
pub const MAX_PROPOSALS: usize = 8;

/// What a session is doing, as its row shows it.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Default)]
pub enum SessionState {
    #[default]
    Idle,
    Working,
    /// Waiting for the user (a question, a permission).
    Waiting,
    Error,
}

impl SessionState {
    pub fn word(self) -> &'static str {
        match self {
            SessionState::Idle => "idle",
            SessionState::Working => "working",
            SessionState::Waiting => "waiting",
            SessionState::Error => "error",
        }
    }
}

/// A row of the agents panel.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Session {
    pub id: String,
    pub title: String,
    pub model: String,
    /// The session's folder: rhun shows the row when it is the project, a folder in it or one of
    /// its worktrees; empty for every project.
    pub cwd: String,
    pub state: SessionState,
    /// Unix seconds of its last change (rows sort by it; "1m ago").
    pub updated: u64,
}

impl Session {
    pub fn new(id: &str, title: &str) -> Session {
        Session {
            id: id.to_string(),
            title: title.to_string(),
            updated: std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map(|d| d.as_secs())
                .unwrap_or(0),
            ..Session::default()
        }
    }
    pub fn model(mut self, model: &str) -> Session {
        self.model = model.to_string();
        self
    }
    pub fn cwd(mut self, cwd: &str) -> Session {
        self.cwd = cwd.to_string();
        self
    }
    pub fn state(mut self, state: SessionState) -> Session {
        self.state = state;
        self
    }
    pub fn updated(mut self, unix_seconds: u64) -> Session {
        self.updated = unix_seconds;
        self
    }
}

/// Whether a session id can travel: 1 to 128 bytes of visible ASCII (no spaces).
pub fn sid_ok(sid: &str) -> bool {
    !sid.is_empty() && sid.len() <= MAX_SID && sid.bytes().all(|b| (0x21..=0x7e).contains(&b))
}

/// The body of `agent-sessions`: N, then a row per session.
pub fn encode_sessions(sessions: &[Session]) -> Result<Vec<u8>, String> {
    if sessions.len() > MAX_SESSIONS {
        return Err(format!(
            "{} sessions: at most {MAX_SESSIONS}",
            sessions.len()
        ));
    }
    let mut b = format!("{}\n", sessions.len()).into_bytes();
    for (i, s) in sessions.iter().enumerate() {
        if !sid_ok(&s.id) {
            return Err(format!(
                "session {}: the id must be 1 to 128 visible ASCII bytes",
                i + 1
            ));
        }
        if sessions[..i].iter().any(|o| o.id == s.id) {
            return Err(format!("session id {} appears twice", s.id));
        }
        let cwd = s.cwd.as_bytes();
        if !cwd.is_empty() && (!is_absolute(&s.cwd) || cwd.contains(&0) || cwd.contains(&b'\n')) {
            return Err(format!("session {}: the folder must be absolute", s.id));
        }
        let title = cut(&s.title, 4096);
        let model = cut(&s.model, 4096);
        b.extend_from_slice(
            format!(
                "{} {} {} {} {} {}\n",
                s.updated,
                s.state.word(),
                s.id.len(),
                title.len(),
                model.len(),
                cwd.len()
            )
            .as_bytes(),
        );
        b.extend_from_slice(s.id.as_bytes());
        b.extend_from_slice(title.as_bytes());
        b.extend_from_slice(model.as_bytes());
        b.extend_from_slice(cwd);
    }
    if b.len() > 1 << 20 {
        return Err("the sessions are over 1 MB".into());
    }
    Ok(b)
}

fn is_absolute(p: &str) -> bool {
    let b = p.as_bytes();
    b.first() == Some(&b'/')
        || b.first() == Some(&b'\\')
        || (b.len() >= 3 && b[1] == b':' && (b[2] == b'/' || b[2] == b'\\'))
}

/// `s` cut to at most `max` bytes at a character boundary.
pub fn cut(s: &str, max: usize) -> &str {
    if s.len() <= max {
        return s;
    }
    let mut n = max;
    while !s.is_char_boundary(n) {
        n -= 1;
    }
    &s[..n]
}

/// Who wrote a message of a transcript.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Role {
    User,
    Assistant,
    /// A tool call: its name in [`Message::name`], a summary (a command, a path) as the text.
    Tool,
    /// A tool's output (shown as a short excerpt).
    Result,
    Error,
    Info,
}

impl Role {
    pub fn word(self) -> &'static str {
        match self {
            Role::User => "user",
            Role::Assistant => "assistant",
            Role::Tool => "tool",
            Role::Result => "result",
            Role::Error => "error",
            Role::Info => "info",
        }
    }
    pub fn parse(w: &str) -> Option<Role> {
        Some(match w {
            "user" => Role::User,
            "assistant" => Role::Assistant,
            "tool" => Role::Tool,
            "result" => Role::Result,
            "error" => Role::Error,
            "info" => Role::Info,
            _ => return None,
        })
    }
}

/// A message of a session.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Message {
    pub role: Role,
    /// A tool's name; otherwise empty.
    pub name: String,
    pub text: String,
    /// The model that wrote a reply (rhun labels a reply where the model changes).
    pub model: String,
}

impl Message {
    pub fn new(role: Role, text: &str) -> Message {
        Message {
            role,
            name: String::new(),
            text: text.to_string(),
            model: String::new(),
        }
    }
    pub fn name(mut self, name: &str) -> Message {
        self.name = name.to_string();
        self
    }
    pub fn model(mut self, model: &str) -> Message {
        self.model = model.to_string();
        self
    }
    fn encoded_len(&self) -> usize {
        40 + self.name.len().min(4096) + self.text.len().min(MAX_TEXT) + self.model.len().min(4096)
    }
}

/// The answer to `agent-transcript`: how many messages the session has, and some of them from the
/// index rhun asked for.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Transcript {
    pub total: u64,
    pub messages: Vec<Message>,
}

impl Transcript {
    /// The messages of `all` from `from` on, as many as fit one answer (rhun asks for the rest).
    pub fn page(all: &[Message], from: u64) -> Transcript {
        let mut t = Transcript {
            total: all.len() as u64,
            messages: Vec::new(),
        };
        let mut size = 32;
        for m in all.iter().skip(from as usize) {
            if t.messages.len() >= MAX_MESSAGES || size + m.encoded_len() > MAX_BODY {
                break;
            }
            size += m.encoded_len();
            t.messages.push(m.clone());
        }
        t
    }

    pub fn encode(&self) -> Vec<u8> {
        let mut b = format!("{} {}\n", self.total, self.messages.len()).into_bytes();
        for m in &self.messages {
            let name = cut(&m.name, 4096);
            let text = cut(&m.text, MAX_TEXT);
            let model = cut(&m.model, 4096);
            b.extend_from_slice(
                format!(
                    "{} {} {} {}\n",
                    m.role.word(),
                    name.len(),
                    text.len(),
                    model.len()
                )
                .as_bytes(),
            );
            b.extend_from_slice(name.as_bytes());
            b.extend_from_slice(text.as_bytes());
            b.extend_from_slice(model.as_bytes());
        }
        b
    }

    /// Reads an answer back (for tests and the checker).
    pub fn parse(body: &[u8]) -> Result<Transcript, String> {
        let mut r = Rd { b: body, p: 0 };
        let total = r.num()?;
        let n = r.num()?;
        r.eol()?;
        let mut t = Transcript {
            total,
            messages: Vec::new(),
        };
        for _ in 0..n {
            let role = Role::parse(&r.word()?).ok_or("unknown role")?;
            let nl = r.num()? as usize;
            let tl = r.num()? as usize;
            let ml = r.num()? as usize;
            r.eol()?;
            let name = r.text(nl)?;
            let text = r.text(tl)?;
            let model = r.text(ml)?;
            t.messages.push(Message {
                role,
                name,
                text,
                model,
            });
        }
        if r.p != body.len() {
            return Err("bytes after the messages".into());
        }
        Ok(t)
    }
}

pub(crate) struct Rd<'a> {
    pub b: &'a [u8],
    pub p: usize,
}

impl Rd<'_> {
    pub fn word(&mut self) -> Result<String, String> {
        let s = self.p;
        while self.p < self.b.len() && self.b[self.p] != b' ' && self.b[self.p] != b'\n' {
            self.p += 1;
        }
        let w = String::from_utf8_lossy(&self.b[s..self.p]).into_owned();
        if self.p < self.b.len() && self.b[self.p] == b' ' {
            self.p += 1;
        }
        if w.is_empty() {
            return Err("a field is missing".into());
        }
        Ok(w)
    }
    pub fn num(&mut self) -> Result<u64, String> {
        let w = self.word()?;
        if w.len() > 18 || !w.bytes().all(|c| c.is_ascii_digit()) {
            return Err(format!("not a number: {w}"));
        }
        w.parse().map_err(|_| format!("not a number: {w}"))
    }
    pub fn eol(&mut self) -> Result<(), String> {
        if self.b.get(self.p) == Some(&b'\n') {
            self.p += 1;
            Ok(())
        } else {
            Err("a line feed is missing".into())
        }
    }
    pub fn take(&mut self, n: usize) -> Result<&[u8], String> {
        if self.b.len() - self.p < n {
            return Err("the body is short".into());
        }
        let s = &self.b[self.p..self.p + n];
        self.p += n;
        Ok(s)
    }
    pub fn text(&mut self, n: usize) -> Result<String, String> {
        Ok(String::from_utf8_lossy(self.take(n)?).into_owned())
    }
}

/// Why a request from rhun is refused: an error code of the protocol and a sentence.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Refusal {
    pub code: String,
    pub text: String,
}

impl Refusal {
    pub fn new(code: &str, text: &str) -> Refusal {
        Refusal {
            code: code.to_string(),
            text: text.to_string(),
        }
    }
}

/// What became of a proposal's file, or of the whole proposal (`0 proposal` events).
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ProposalEvent {
    /// The file at `index` (from 1, in the edit's order) was accepted whole.
    Accepted {
        proposal: u64,
        index: u64,
    },
    Rejected {
        proposal: u64,
        index: u64,
    },
    /// It changed since it was proposed: not applied.
    Stale {
        proposal: u64,
        index: u64,
    },
    /// Some of its hunks were accepted.
    Partial {
        proposal: u64,
        index: u64,
        accepted: u64,
        hunks: u64,
    },
    /// Every file is decided: how many were accepted (whole or partly) and rejected (or stale).
    Done {
        proposal: u64,
        accepted: u64,
        rejected: u64,
    },
}

impl ProposalEvent {
    /// The event's fields after `proposal`.
    pub fn parse(fields: &[String]) -> Option<ProposalEvent> {
        let n = |i: usize| fields.get(i).and_then(|f| f.parse::<u64>().ok());
        let proposal = n(0)?;
        if fields.get(1).map(String::as_str) == Some("done") {
            return Some(ProposalEvent::Done {
                proposal,
                accepted: n(2)?,
                rejected: n(3)?,
            });
        }
        let index = n(1)?;
        Some(match fields.get(2)?.as_str() {
            "accepted" => ProposalEvent::Accepted { proposal, index },
            "rejected" => ProposalEvent::Rejected { proposal, index },
            "stale" => ProposalEvent::Stale { proposal, index },
            "partial" => ProposalEvent::Partial {
                proposal,
                index,
                accepted: n(3)?,
                hunks: n(4)?,
            },
            _ => return None,
        })
    }
}

/// A tool as `tools` lists it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ToolInfo {
    /// `OWNER/NAME.tool` or `rhun.tool`.
    pub name: String,
    /// Changes nothing: called without asking the user.
    pub readonly: bool,
    pub title: String,
    pub description: String,
    /// What its input is, in words.
    pub input: String,
}

/// Reads a `tools` answer.
pub fn parse_tools(body: &[u8]) -> Result<Vec<ToolInfo>, String> {
    let mut r = Rd { b: body, p: 0 };
    let n = r.num()?;
    r.eol()?;
    let mut v = Vec::new();
    for _ in 0..n {
        let name = r.word()?;
        let flags = r.word()?;
        let tl = r.num()? as usize;
        let dl = r.num()? as usize;
        let il = r.num()? as usize;
        r.eol()?;
        let title = r.text(tl)?;
        let description = r.text(dl)?;
        let input = r.text(il)?;
        v.push(ToolInfo {
            name,
            readonly: flags == "ro",
            title,
            description,
            input,
        });
    }
    Ok(v)
}

/// The body of a `tools` answer (for tests and hosts written in Rust).
pub fn encode_tools(tools: &[ToolInfo]) -> Vec<u8> {
    let mut b = format!("{}\n", tools.len()).into_bytes();
    for t in tools {
        b.extend_from_slice(
            format!(
                "{} {} {} {} {}\n",
                t.name,
                if t.readonly { "ro" } else { "rw" },
                t.title.len(),
                t.description.len(),
                t.input.len()
            )
            .as_bytes(),
        );
        b.extend_from_slice(t.title.as_bytes());
        b.extend_from_slice(t.description.as_bytes());
        b.extend_from_slice(t.input.as_bytes());
    }
    b
}

/// The input of a built-in `rhun.*` tool: `KEY VALUE` lines, and `KEY LEN` with the bytes after
/// the line feed for `text` and `edit`.
#[derive(Clone, Debug, Default)]
pub struct ToolInput {
    b: Vec<u8>,
}

impl ToolInput {
    pub fn new() -> ToolInput {
        ToolInput::default()
    }
    /// `key value`: the value is one line.
    pub fn set(mut self, key: &str, value: &str) -> ToolInput {
        let v: String = value
            .chars()
            .map(|c| if c == '\n' { ' ' } else { c })
            .collect();
        self.b.extend_from_slice(format!("{key} {v}\n").as_bytes());
        self
    }
    /// `text LEN` or `edit LEN` and the bytes.
    pub fn bytes(mut self, key: &str, value: &[u8]) -> ToolInput {
        self.b
            .extend_from_slice(format!("{key} {}\n", value.len()).as_bytes());
        self.b.extend_from_slice(value);
        self
    }
    pub fn into_bytes(self) -> Vec<u8> {
        self.b
    }
}

/// A tool call's input, as an extension's tool handler sees it ([`crate::Handler::tool`]).
#[derive(Clone, Debug)]
pub struct ToolCall {
    /// The tool's own name, without `OWNER/NAME.`.
    pub name: String,
    /// The calling extension: `OWNER/NAME`.
    pub caller: String,
    pub input: Vec<u8>,
}

// ---- requests ----

impl Ext {
    /// Replaces this extension's rows in the agents panel (feature agents).
    pub fn agent_sessions(&self, sessions: &[Session]) -> Result<(), Error> {
        let body = encode_sessions(sessions).map_err(|e| Error {
            code: "toobig".into(),
            text: e,
        })?;
        self.request("agent-sessions", Some(&body)).map(|_| ())
    }

    /// Says the session now has `total` messages, the first changed one at `first`; rhun asks
    /// for them when the session is open.
    pub fn agent_changed(&self, sid: &str, total: u64, first: u64) -> Result<(), Error> {
        self.request(&format!("agent-changed {sid} {total} {first}"), None)
            .map(|_| ())
    }

    /// Proposes an edit for the user to review (feature edit-review): the proposal's id, at once.
    /// What becomes of it arrives as [`crate::Handler::proposal`] events. The edit goes in 12g's
    /// grammar whatever was agreed: a hash as `-`, without its label and `confirm`; file
    /// operations are refused (`unknown`).
    pub fn propose(&self, label: &str, sid: &str, edit: &WorkspaceEdit) -> Result<u64, Error> {
        let body = proposal_body(label, sid, edit)?;
        let r = self.request("propose", Some(&body))?;
        Ok(r.fields.first().and_then(|f| f.parse().ok()).unwrap_or(0))
    }

    /// Withdraws a proposal: its Review tab closes, nothing more is said about it.
    pub fn unpropose(&self, proposal: u64) -> Result<(), Error> {
        self.request(&format!("unpropose {proposal}"), None)
            .map(|_| ())
    }

    /// The tools rhun offers: its own and those of installed extensions (feature tools).
    pub fn tools(&self) -> Result<Vec<ToolInfo>, Error> {
        let r: Reply = self.request("tools", None)?;
        parse_tools(&r.body).map_err(|e| Error {
            code: "syntax".into(),
            text: e,
        })
    }

    /// Calls a tool and waits for its output. A tool that changes anything asks the user first:
    /// `refused denied by the user` when they say no. Call it from a thread of your own when your
    /// handler must go on meanwhile.
    pub fn tool_call(&self, tool: &str, input: &[u8]) -> Result<Vec<u8>, Error> {
        self.request(&format!("tool-call {tool}"), Some(input))
            .map(|r| r.body)
    }

    /// Shows a card in the session's conversation (feature agent-prompts): a prompt line and the
    /// options as buttons; the option the user picked, from 0, or `cancelled`.
    pub fn agent_ask(&self, sid: &str, prompt: &str, options: &[&str]) -> Result<usize, Error> {
        let mut body = prompt.replace('\n', " ");
        for o in options {
            body.push('\n');
            body.push_str(&o.replace('\n', " "));
        }
        let r = self.request(&format!("agent-ask {sid}"), Some(body.as_bytes()))?;
        let n: usize = r.fields.first().and_then(|f| f.parse().ok()).unwrap_or(0);
        Ok(n.saturating_sub(1))
    }
}

/// A proposal's body: the head and the edit in 12g's grammar whatever was agreed (the Review tab
/// reads 12g: a hash goes as `-`, the edit's label and `confirm` are left out, and file operations
/// are refused `unknown`).
pub(crate) fn proposal_body(
    label: &str,
    sid: &str,
    edit: &WorkspaceEdit,
) -> Result<Vec<u8>, Error> {
    let e = edit
        .encode_with(crate::lang::EditCaps::V1)
        .map_err(|(code, text)| Error {
            code: code.into(),
            text,
        })?;
    let mut body = format!("{} {}\n{label}{sid}", label.len(), sid.len()).into_bytes();
    body.extend_from_slice(&e);
    Ok(body)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn proposals_are_12g_whatever_was_agreed() {
        use crate::lang::{Create, TextEdit};
        let at = crate::Pos::new(1, 1);
        let insert = TextEdit::new(crate::Range { start: at, end: at }, "y");
        let edit = WorkspaceEdit::new()
            .file_hashed("/p/a.txt", b"x\r\n", vec![insert])
            .label("Fix")
            .confirm();
        let b = proposal_body("L", "s1", &edit).unwrap();
        assert_eq!(
            b,
            b"1 2\nLs1"
                .iter()
                .chain(b"1\n8 - 1\n/p/a.txt1 1 1 1 1\ny")
                .copied()
                .collect::<Vec<u8>>()
        );
        let ops = WorkspaceEdit::new().create("/p/b.txt", Create::default());
        assert_eq!(proposal_body("L", "", &ops).unwrap_err().code, "unknown");
    }

    #[test]
    fn sessions_encode() {
        let s = Session::new("s1", "Fix it")
            .updated(12)
            .state(SessionState::Working)
            .model("m")
            .cwd("/p");
        let b = encode_sessions(&[s]).unwrap();
        assert_eq!(b, b"1\n12 working 2 6 1 2\ns1Fix itm/p");
        assert!(encode_sessions(&[Session::new("a b", "")]).is_err());
        assert!(encode_sessions(&[Session::new("a", "").cwd("rel")]).is_err());
        assert!(encode_sessions(&[Session::new("a", ""), Session::new("a", "")]).is_err());
    }

    #[test]
    fn transcript_round_trip_and_pages() {
        let all: Vec<Message> = (0..5)
            .map(|i| Message::new(Role::Assistant, &format!("m{i}")).model("x"))
            .collect();
        let t = Transcript::page(&all, 3);
        assert_eq!(t.total, 5);
        assert_eq!(t.messages.len(), 2);
        let back = Transcript::parse(&t.encode()).unwrap();
        assert_eq!(back, t);
        let tool = Message::new(Role::Tool, "src/main.rs").name("read_file");
        assert_eq!(
            Transcript {
                total: 1,
                messages: vec![tool]
            }
            .encode(),
            b"1 1\ntool 9 11 0\nread_filesrc/main.rs"
        );
    }

    #[test]
    fn proposal_events() {
        let f = |s: &str| s.split(' ').map(str::to_string).collect::<Vec<_>>();
        assert_eq!(
            ProposalEvent::parse(&f("3 2 accepted")),
            Some(ProposalEvent::Accepted {
                proposal: 3,
                index: 2
            })
        );
        assert_eq!(
            ProposalEvent::parse(&f("3 1 partial 1 2")),
            Some(ProposalEvent::Partial {
                proposal: 3,
                index: 1,
                accepted: 1,
                hunks: 2
            })
        );
        assert_eq!(
            ProposalEvent::parse(&f("3 done 1 1")),
            Some(ProposalEvent::Done {
                proposal: 3,
                accepted: 1,
                rejected: 1
            })
        );
        assert_eq!(ProposalEvent::parse(&f("3 1 maybe")), None);
    }

    #[test]
    fn tools_round_trip() {
        let t = vec![ToolInfo {
            name: "rhun.read".into(),
            readonly: true,
            title: "Read".into(),
            description: "d".into(),
            input: "path PATH".into(),
        }];
        assert_eq!(parse_tools(&encode_tools(&t)).unwrap(), t);
        assert_eq!(
            ToolInput::new()
                .set("path", "a b")
                .bytes("text", b"x\ny")
                .into_bytes(),
            b"path a b\ntext 3\nx\ny"
        );
    }
}
