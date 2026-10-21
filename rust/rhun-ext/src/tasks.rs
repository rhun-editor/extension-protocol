//! Terminals and tasks (protocol 1.1, features `terminals` and `tasks`): programs in terminal tabs
//! of rhun's panel, run as the user after the one approval per extension and project, and published
//! tasks rhun lists, runs with their dependencies, and turns into problems. The manifest needs
//! `[needs] terminal = run` (`input` for [`Ext::terminal_input`], `output` for
//! [`Ext::terminal_screen`] and output events).
//!
//! Patterns and matchers: [`check_pattern`], [`match_pattern`] and [`check_matcher`] follow rhun's
//! own matcher step for step (`v1.1/patterns.txt` holds the vectors every SDK passes).

use crate::{Error, Event, Ext, Feature};
use std::collections::HashMap;

/// A terminal tab an extension opened, or one running its task; never reused in a session.
pub type Term = u64;

/// What runs.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Command {
    /// A command line for `/bin/sh -c` (Linux, macOS) or `powershell.exe -Command` (Windows);
    /// empty in a terminal: the user's interactive shell.
    Shell(String),
    /// A program (looked up on PATH without a slash) and its arguments.
    Argv(Vec<String>),
}

/// A program and where and how it runs.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Program {
    pub command: Command,
    /// Empty: the project folder; relative: inside it.
    pub cwd: String,
    pub env: Vec<(String, String)>,
    pub unset: Vec<String>,
}

impl Program {
    pub fn shell(line: &str) -> Program {
        Program {
            command: Command::Shell(line.into()),
            cwd: String::new(),
            env: Vec::new(),
            unset: Vec::new(),
        }
    }
    pub fn argv(args: &[&str]) -> Program {
        Program {
            command: Command::Argv(args.iter().map(|a| a.to_string()).collect()),
            cwd: String::new(),
            env: Vec::new(),
            unset: Vec::new(),
        }
    }
}

/// What rhun shows when a program starts.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Reveal {
    Show,
    Focus,
    Silent,
}

impl Reveal {
    fn word(self) -> &'static str {
        match self {
            Reveal::Show => "show",
            Reveal::Focus => "focus",
            Reveal::Silent => "silent",
        }
    }
}

/// A task's matcher: a built-in (`go`, `gcc`, `rustc`, `tsc`, `msbuild`) or an inline one's text.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Matcher {
    Builtin(String),
    Text(String),
}

/// A task to publish ([`Ext::set_tasks`]) or run ([`Ext::run_task`]).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Task {
    pub id: String,
    pub label: String,
    pub detail: String,
    pub program: Program,
    /// `build`, `test`, or empty.
    pub group: String,
    pub default: bool,
    pub depends: Vec<String>,
    pub parallel: bool,
    pub matchers: Vec<Matcher>,
    /// Patterns: a background task, done for its dependents when a line matches.
    pub ready: Vec<String>,
    /// Patterns: a new cycle began (with `ready`).
    pub busy: Vec<String>,
    pub reveal: Option<Reveal>,
    pub output: bool,
}

impl Task {
    pub fn new(id: &str, label: &str, program: Program) -> Task {
        Task {
            id: id.into(),
            label: label.into(),
            detail: String::new(),
            program,
            group: String::new(),
            default: false,
            depends: Vec::new(),
            parallel: false,
            matchers: Vec::new(),
            ready: Vec::new(),
            busy: Vec::new(),
            reveal: None,
            output: false,
        }
    }
}

/// A terminal to open.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TerminalSpec {
    pub label: String,
    pub detail: String,
    pub program: Program,
    pub reveal: Option<Reveal>,
    pub output: bool,
}

/// A definition as rhun reads it from the wire ([`parse_definition`]).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Definition {
    pub id: String,
    pub form: String,
    pub label: String,
    pub command: String,
    pub argv: Vec<String>,
    pub cwd: String,
    pub detail: String,
    pub env: Vec<(String, String)>,
    pub unset: Vec<String>,
    pub group: String,
    pub default: bool,
    pub order: String,
    pub depends: Vec<String>,
    pub matchers: Vec<Matcher>,
    pub ready: Vec<String>,
    pub busy: Vec<String>,
    pub reveal: String,
    pub output: bool,
}

fn syntax(text: String) -> Error {
    Error {
        code: "syntax".into(),
        text,
    }
}

fn def_err(why: &str) -> Error {
    syntax(format!("definition: {why}"))
}

/// Whether `s` is a task id: 1 to 64 bytes of a-z 0-9 _ . -, from a letter or digit.
pub fn task_id_ok(s: &str) -> bool {
    !s.is_empty()
        && s.len() <= 64
        && s.bytes().enumerate().all(|(i, c)| {
            c.is_ascii_lowercase()
                || c.is_ascii_digit()
                || i > 0 && (c == b'_' || c == b'.' || c == b'-')
        })
}

fn env_name_ok(s: &str) -> bool {
    !s.is_empty()
        && s.len() <= 64
        && s.bytes()
            .enumerate()
            .all(|(i, c)| c == b'_' || c.is_ascii_alphabetic() || i > 0 && c.is_ascii_digit())
}

/// v1's rule for term text: valid UTF-8, one line, no control, bidi-control or zero-width characters.
fn plain_text(b: &[u8], limit: usize) -> bool {
    if b.is_empty() || b.len() > limit {
        return false;
    }
    match std::str::from_utf8(b) {
        Ok(s) => s.chars().all(|r| {
            let r = r as u32;
            !(r < 0x20
                || (0x7f..0xa0).contains(&r)
                || (0x200b..=0x200f).contains(&r)
                || (0x202a..=0x202e).contains(&r)
                || r == 0x2060
                || (0x2066..=0x2069).contains(&r)
                || r == 0xfeff)
        }),
        Err(_) => false,
    }
}

fn decimal(s: &str) -> Option<usize> {
    if s.is_empty() || s.len() > 9 || !s.bytes().all(|c| c.is_ascii_digit()) {
        return None;
    }
    s.parse().ok()
}

fn lossy(b: &[u8]) -> String {
    String::from_utf8_lossy(b).into_owned()
}

/// Reads one definition as rhun does (`task` false: a terminal-open's, id `-`, no task records):
/// the definition and the bytes it took, or a `syntax` error with rhun's reason.
pub fn parse_definition(b: &[u8], task: bool) -> Result<(Definition, usize), Error> {
    let header = "header: TASK_ID FORM LABEL_LEN CMD_LEN CWD_LEN EXTRA_LEN";
    let nl = match b.iter().position(|&c| c == b'\n') {
        Some(n) if n < 512 => n,
        _ => return Err(def_err(header)),
    };
    let head = lossy(&b[..nl]);
    let w: Vec<&str> = head.split(' ').collect();
    if w.len() != 6 {
        return Err(def_err(header));
    }
    let mut n = [0usize; 4];
    for i in 0..4 {
        n[i] = decimal(w[2 + i]).ok_or_else(|| def_err(header))?;
    }
    let total = nl + 1 + n[0] + n[1] + n[2] + n[3];
    if total > b.len() {
        return Err(def_err("header: the lengths go past the body"));
    }
    let mut d = Definition {
        id: w[0].into(),
        form: w[1].into(),
        ..Default::default()
    };
    if !task && d.id != "-" {
        return Err(def_err("id: a terminal's is -"));
    }
    if task && !task_id_ok(&d.id) {
        return Err(def_err(
            "id: 1 to 64 bytes of a-z 0-9 _ . -, from a letter or digit",
        ));
    }
    if d.form != "shell" && d.form != "argv" {
        return Err(def_err("form: shell or argv"));
    }
    let mut p = nl + 1;
    let label = &b[p..p + n[0]];
    if !plain_text(label, 128) {
        return Err(def_err("label: 1 to 128 bytes of plain text"));
    }
    d.label = lossy(label);
    p += n[0];
    let cmd = &b[p..p + n[1]];
    if d.form == "shell" {
        if cmd.is_empty() && task || !cmd.is_empty() && !plain_text(cmd, 4096) {
            return Err(def_err("command: 1 to 4096 bytes of plain text"));
        }
        d.command = lossy(cmd);
    } else {
        if cmd.is_empty()
            || cmd.len() > 32768
            || cmd[cmd.len() - 1] != 0
            || cmd[0] == 0
            || cmd.iter().filter(|&&c| c == 0).count() > 64
        {
            return Err(def_err(
                "argv: 1 to 64 arguments, each ending with a NUL, the first not empty",
            ));
        }
        d.argv = cmd[..cmd.len() - 1].split(|&c| c == 0).map(lossy).collect();
    }
    p += n[1];
    let cwd = &b[p..p + n[2]];
    if cwd.len() > 4096 || cwd.iter().any(|&c| c == 0 || c == b'\n') {
        return Err(def_err("cwd: at most 4096 bytes, no NUL or LF"));
    }
    d.cwd = lossy(cwd);
    p += n[2];
    let end = p + n[3];
    let mut env_bytes = 0;
    let rec_err = || def_err("record: NAME ARGS... LEN");
    while p < end {
        let e = b[p..end]
            .iter()
            .position(|&c| c == b'\n')
            .ok_or_else(rec_err)?;
        let line = lossy(&b[p..p + e]);
        let rw: Vec<&str> = line.split(' ').collect();
        if rw.len() < 2 || rw.len() > 8 {
            return Err(rec_err());
        }
        let size = decimal(rw[rw.len() - 1]).ok_or_else(rec_err)?;
        if p + e + 1 + size > end {
            return Err(rec_err());
        }
        let data = &b[p + e + 1..p + e + 1 + size];
        p += e + 1 + size;
        d.record(&rw, data, task, &mut env_bytes)?;
    }
    if !d.busy.is_empty() && d.ready.is_empty() {
        return Err(def_err("busy needs ready"));
    }
    Ok((d, total))
}

impl Definition {
    fn record(
        &mut self,
        w: &[&str],
        data: &[u8],
        task: bool,
        env_bytes: &mut usize,
    ) -> Result<(), Error> {
        let bad = || def_err("record: NAME ARGS... LEN");
        let flag = |n: usize| w.len() == n && data.is_empty();
        let too_many = || def_err("too many: 64 env, 4 matchers, 8 patterns");
        match w[0] {
            "detail" => {
                if w.len() != 2 {
                    return Err(bad());
                }
                if !plain_text(data, 200) {
                    return Err(def_err("detail: one line, at most 200 bytes"));
                }
                self.detail = lossy(data);
            }
            "env" => {
                if w.len() != 3 {
                    return Err(bad());
                }
                if !env_name_ok(w[1]) || data.iter().any(|&c| c == 0 || c == b'\n') {
                    return Err(def_err(
                        "env: NAME is [A-Za-z_][A-Za-z0-9_]*, the value one line",
                    ));
                }
                *env_bytes += w[1].len() + data.len();
                if self.env.len() >= 64 || *env_bytes > 32768 {
                    return Err(too_many());
                }
                self.env.push((w[1].into(), lossy(data)));
            }
            "unset" => {
                if !flag(3) {
                    return Err(bad());
                }
                if !env_name_ok(w[1]) {
                    return Err(def_err(
                        "env: NAME is [A-Za-z_][A-Za-z0-9_]*, the value one line",
                    ));
                }
                if self.unset.len() >= 64 {
                    return Err(too_many());
                }
                self.unset.push(w[1].into());
            }
            "reveal" => {
                if !flag(3) {
                    return Err(bad());
                }
                if !matches!(w[1], "show" | "focus" | "silent") {
                    return Err(def_err("reveal: show, focus or silent"));
                }
                self.reveal = w[1].into();
            }
            "output" => {
                if !flag(2) {
                    return Err(bad());
                }
                self.output = true;
            }
            "location" => {
                if w.len() != 3 {
                    return Err(bad());
                }
                if w[1] != "local" {
                    return Err(def_err("unsupported location"));
                }
            }
            "group" | "default" | "depends" | "matcher" | "ready" | "busy" => {
                if !task {
                    return Err(def_err(
                        "not in a terminal: group, default, depends, matcher, ready, busy",
                    ));
                }
                return self.task_record(w, data);
            }
            _ => {} // an unknown record is skipped
        }
        Ok(())
    }

    fn task_record(&mut self, w: &[&str], data: &[u8]) -> Result<(), Error> {
        let bad = || def_err("record: NAME ARGS... LEN");
        let too_many = || def_err("too many: 64 env, 4 matchers, 8 patterns");
        let text = lossy(data);
        match w[0] {
            "group" => {
                if w.len() != 3 || !data.is_empty() {
                    return Err(bad());
                }
                if w[1] != "build" && w[1] != "test" {
                    return Err(def_err("group: build or test"));
                }
                self.group = w[1].into();
            }
            "default" => {
                if w.len() != 2 || !data.is_empty() {
                    return Err(bad());
                }
                self.default = true;
            }
            "depends" => {
                if w.len() != 3 {
                    return Err(bad());
                }
                if w[1] != "sequence" && w[1] != "parallel" {
                    return Err(def_err("depends: sequence or parallel"));
                }
                self.order = w[1].into();
                self.depends.clear();
                if text.is_empty() {
                    return Err(def_err("depends: 1 to 16 task ids"));
                }
                for id in text.split(' ') {
                    if !task_id_ok(id) || self.depends.len() >= 16 {
                        return Err(def_err("depends: 1 to 16 task ids"));
                    }
                    self.depends.push(id.into());
                }
            }
            "matcher" => {
                if w.len() != 3 {
                    return Err(bad());
                }
                if self.matchers.len() >= 4 {
                    return Err(too_many());
                }
                if w[1] != "-" {
                    if !data.is_empty() {
                        return Err(bad());
                    }
                    if builtin_matcher(w[1]).is_none() {
                        return Err(def_err("matcher: go, gcc, rustc, tsc or msbuild"));
                    }
                    self.matchers.push(Matcher::Builtin(w[1].into()));
                    return Ok(());
                }
                if data.len() > 4096 {
                    return Err(too_many());
                }
                check_matcher(&text).map_err(|e| def_err(&e))?;
                self.matchers.push(Matcher::Text(text));
            }
            _ => {
                // ready, busy: one to eight patterns, one per line
                if w.len() != 2 {
                    return Err(bad());
                }
                let mut list = Vec::new();
                let mut rest = text.as_str();
                while !rest.is_empty() {
                    let (line, after) = match rest.find('\n') {
                        Some(i) => (&rest[..i], &rest[i + 1..]),
                        None => (rest, ""),
                    };
                    rest = after;
                    if list.len() >= 8 {
                        return Err(too_many());
                    }
                    check_pattern(line).map_err(|e| def_err(&e))?;
                    list.push(line.to_string());
                }
                if list.is_empty() {
                    return Err(def_err("ready, busy: 1 to 8 patterns"));
                }
                if w[0] == "ready" {
                    self.ready = list;
                } else {
                    self.busy = list;
                }
            }
        }
        Ok(())
    }
}

/// Reads a tasks-set body as rhun does: the count, the definitions, ids unique, depends naming ids
/// of the list, no cycle.
pub fn parse_task_list(body: &[u8]) -> Result<Vec<Definition>, Error> {
    let count_err = || syntax("the count of tasks".into());
    let nl = match body.iter().position(|&c| c == b'\n') {
        Some(n) if (1..=8).contains(&n) => n,
        _ => return Err(count_err()),
    };
    let count = decimal(&lossy(&body[..nl])).ok_or_else(count_err)?;
    if count > 1000 || body.len() > 1 << 20 {
        return Err(Error {
            code: "toobig".into(),
            text: "1000 tasks, 4 MB of definitions in all".into(),
        });
    }
    let mut rest = &body[nl + 1..];
    let mut list = Vec::with_capacity(count);
    for i in 0..count {
        let (d, n) = parse_definition(rest, true).map_err(|e| {
            syntax(format!(
                "task {}: {}",
                i + 1,
                e.text.trim_start_matches("definition: ")
            ))
        })?;
        list.push(d);
        rest = &rest[n..];
    }
    if !rest.is_empty() {
        return Err(syntax("bytes after the last definition".into()));
    }
    let mut index = HashMap::new();
    for (i, d) in list.iter().enumerate() {
        if let Some(first) = index.insert(d.id.clone(), i) {
            // rhun names the first of the two
            return Err(syntax(format!("task {}: id listed twice", first + 1)));
        }
    }
    for (i, d) in list.iter().enumerate() {
        for dep in &d.depends {
            if !index.contains_key(dep) {
                return Err(syntax(format!(
                    "task {}: depends: no task \"{dep}\"",
                    i + 1
                )));
            }
        }
    }
    fn visit(
        i: usize,
        list: &[Definition],
        index: &HashMap<String, usize>,
        state: &mut [u8],
    ) -> bool {
        match state[i] {
            1 => return false,
            2 => return true,
            _ => {}
        }
        state[i] = 1;
        for dep in &list[i].depends {
            if !visit(index[dep], list, index, state) {
                return false;
            }
        }
        state[i] = 2;
        true
    }
    let mut state = vec![0u8; list.len()];
    for i in 0..list.len() {
        if !visit(i, &list, &index, &mut state) {
            return Err(syntax(format!("task {}: depends: a cycle", i + 1)));
        }
    }
    Ok(list)
}

fn record(name: &str, args: &str, data: &[u8]) -> Vec<u8> {
    let mut v = if args.is_empty() {
        format!("{name} {}\n", data.len())
    } else {
        format!("{name} {args} {}\n", data.len())
    }
    .into_bytes();
    v.extend_from_slice(data);
    v
}

#[allow(clippy::too_many_arguments)]
fn encode_definition(
    id: &str,
    label: &str,
    detail: &str,
    p: &Program,
    reveal: Option<Reveal>,
    output: bool,
    more: &[u8],
    task: bool,
) -> Result<Vec<u8>, Error> {
    let (form, cmd) = match &p.command {
        Command::Shell(s) => ("shell", s.clone().into_bytes()),
        Command::Argv(a) => {
            let mut c = Vec::new();
            for x in a {
                c.extend_from_slice(x.as_bytes());
                c.push(0);
            }
            ("argv", c)
        }
    };
    let mut extra = Vec::new();
    if !detail.is_empty() {
        extra.extend(record("detail", "", detail.as_bytes()));
    }
    for (k, v) in &p.env {
        extra.extend(record("env", k, v.as_bytes()));
    }
    for k in &p.unset {
        extra.extend(record("unset", k, b""));
    }
    if let Some(r) = reveal {
        extra.extend(record("reveal", r.word(), b""));
    }
    if output {
        extra.extend(record("output", "", b""));
    }
    extra.extend_from_slice(more);
    let mut b = format!(
        "{id} {form} {} {} {} {}\n",
        label.len(),
        cmd.len(),
        p.cwd.len(),
        extra.len()
    )
    .into_bytes();
    b.extend_from_slice(label.as_bytes());
    b.extend_from_slice(&cmd);
    b.extend_from_slice(p.cwd.as_bytes());
    b.extend_from_slice(&extra);
    // checked as rhun will check it, so a mistake is an error here and not a refusal later
    parse_definition(&b, task)?;
    Ok(b)
}

impl Task {
    /// The task's definition on the wire, checked as rhun checks it.
    pub fn encode(&self) -> Result<Vec<u8>, Error> {
        let mut more = Vec::new();
        if !self.group.is_empty() {
            more.extend(record("group", &self.group, b""));
        }
        if self.default {
            more.extend(record("default", "", b""));
        }
        if !self.depends.is_empty() {
            let order = if self.parallel {
                "parallel"
            } else {
                "sequence"
            };
            more.extend(record("depends", order, self.depends.join(" ").as_bytes()));
        }
        for m in &self.matchers {
            match m {
                Matcher::Builtin(n) => more.extend(record("matcher", n, b"")),
                Matcher::Text(t) => more.extend(record("matcher", "-", t.as_bytes())),
            }
        }
        if !self.ready.is_empty() {
            more.extend(record("ready", "", self.ready.join("\n").as_bytes()));
        }
        if !self.busy.is_empty() {
            more.extend(record("busy", "", self.busy.join("\n").as_bytes()));
        }
        encode_definition(
            &self.id,
            &self.label,
            &self.detail,
            &self.program,
            self.reveal,
            self.output,
            &more,
            true,
        )
    }
}

impl TerminalSpec {
    /// The terminal's definition on the wire, checked as rhun checks it.
    pub fn encode(&self) -> Result<Vec<u8>, Error> {
        encode_definition(
            "-",
            &self.label,
            &self.detail,
            &self.program,
            self.reveal,
            self.output,
            b"",
            false,
        )
    }
}

/// A tasks-set body, checked whole as rhun checks it.
pub fn encode_task_list(tasks: &[Task]) -> Result<Vec<u8>, Error> {
    let mut body = format!("{}\n", tasks.len()).into_bytes();
    for t in tasks {
        body.extend(t.encode()?);
    }
    parse_task_list(&body)?;
    Ok(body)
}

/// What a terminal shows: its size, the cursor (0-based) and its rows, trailing blanks dropped.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Screen {
    pub rows: usize,
    pub cols: usize,
    pub cursor_row: usize,
    pub cursor_col: usize,
    pub lines: Vec<String>,
}

/// Decodes a terminal-screen body.
pub fn parse_screen(body: &[u8]) -> Option<Screen> {
    let text = String::from_utf8_lossy(body);
    let (head, rest) = text.split_once('\n').unwrap_or((&text, ""));
    let n: Vec<usize> = head
        .split(' ')
        .map(|f| f.parse().ok())
        .collect::<Option<_>>()?;
    if n.len() != 4 {
        return None;
    }
    let rest = rest.strip_suffix('\n').unwrap_or(rest);
    let lines: Vec<String> = if n[0] > 0 {
        rest.split('\n').map(String::from).collect()
    } else {
        Vec::new()
    };
    if lines.len() != n[0] {
        return None;
    }
    Some(Screen {
        rows: n[0],
        cols: n[1],
        cursor_row: n[2],
        cursor_col: n[3],
        lines,
    })
}

/// One of the terminals and tasks events.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum TerminalEvent {
    /// The program started (`task` empty for a terminal); again at each restart.
    Started { term: Term, pid: u64, task: String },
    /// It ended: `reason` exit, stopped or failed (`code` None: it could not start).
    Ended {
        term: Term,
        reason: String,
        code: Option<i64>,
        errors: u32,
        warnings: u32,
    },
    /// Finished lines of a terminal opened with output; `dropped` lines left out since the last.
    Output {
        term: Term,
        dropped: u64,
        lines: Vec<String>,
    },
    /// The tab closed.
    Closed { term: Term },
    /// A background task's line matched `ready`.
    Ready {
        term: Term,
        errors: u32,
        warnings: u32,
    },
    /// A background task's line matched `busy`.
    Busy { term: Term },
}

/// Decodes terminal-started, terminal-ended, terminal-output, terminal-closed, task-ready and
/// task-busy; None for any other event or a malformed one.
pub fn parse_terminal_event(ev: &Event) -> Option<TerminalEvent> {
    let f = &ev.fields;
    let term = || f.first()?.parse::<u64>().ok().filter(|&t| t > 0);
    let num = |i: usize| f.get(i)?.parse::<u32>().ok();
    Some(match ev.name.as_str() {
        "terminal-started" if f.len() == 3 => TerminalEvent::Started {
            term: term()?,
            pid: f[1].parse().ok()?,
            task: if f[2] == "-" {
                String::new()
            } else {
                f[2].clone()
            },
        },
        "terminal-ended" if f.len() == 5 => TerminalEvent::Ended {
            term: term()?,
            reason: f[1].clone(),
            code: if f[2] == "-" {
                None
            } else {
                Some(f[2].parse().ok()?)
            },
            errors: num(3)?,
            warnings: num(4)?,
        },
        "terminal-output" if f.len() == 3 => TerminalEvent::Output {
            term: term()?,
            dropped: f[1].parse().ok()?,
            lines: String::from_utf8_lossy(&ev.body)
                .lines()
                .map(String::from)
                .collect(),
        },
        "terminal-closed" if f.len() == 1 => TerminalEvent::Closed { term: term()? },
        "task-ready" if f.len() == 3 => TerminalEvent::Ready {
            term: term()?,
            errors: num(1)?,
            warnings: num(2)?,
        },
        "task-busy" if f.len() == 1 => TerminalEvent::Busy { term: term()? },
        _ => return None,
    })
}

fn agreed(ext: &Ext, f: Feature) -> Result<(), Error> {
    if ext.has(f) {
        Ok(())
    } else {
        Err(Error {
            code: "unknown".into(),
            text: format!("{} was not agreed", f.name()),
        })
    }
}

fn term_reply(r: crate::Reply) -> Result<Term, Error> {
    match r.fields.as_slice() {
        [t] => t.parse::<u64>().ok().filter(|&t| t > 0),
        _ => None,
    }
    .ok_or_else(|| Error {
        code: "syntax".into(),
        text: "a TERM reply".into(),
    })
}

impl Ext {
    /// Replaces the extension's published tasks (all of them, checked whole first).
    pub fn set_tasks(&self, tasks: &[Task]) -> Result<(), Error> {
        agreed(self, Feature::Tasks)?;
        let body = encode_task_list(tasks)?;
        self.request("tasks-set", Some(&body)).map(|_| ())
    }

    /// Runs a published task after its dependencies (the approval first when needed); a running
    /// one stays running and its Term comes back.
    pub fn start_task(&self, id: &str) -> Result<Term, Error> {
        agreed(self, Feature::Tasks)?;
        if !task_id_ok(id) {
            return Err(syntax("a task id".into()));
        }
        term_reply(self.request(&format!("task-start {id}"), None)?)
    }

    /// Runs a task that is not published (its depends may name published ones).
    pub fn run_task(&self, task: &Task) -> Result<Term, Error> {
        agreed(self, Feature::Tasks)?;
        let b = task.encode()?;
        term_reply(self.request("task-run", Some(&b))?)
    }

    /// Starts a program in a new terminal tab (the approval first when needed).
    pub fn open_terminal(&self, spec: &TerminalSpec) -> Result<Term, Error> {
        agreed(self, Feature::Terminals)?;
        let b = spec.encode()?;
        term_reply(self.request("terminal-open", Some(&b))?)
    }

    /// Ctrl+C into the program; what is left of it is killed 2 s later.
    pub fn stop_terminal(&self, t: Term) -> Result<(), Error> {
        agreed(self, Feature::Terminals)?;
        self.request(&format!("terminal-stop {t}"), None)
            .map(|_| ())
    }

    /// Stops the program, waits for its end and runs it again in the same tab.
    pub fn restart_terminal(&self, t: Term) -> Result<(), Error> {
        agreed(self, Feature::Terminals)?;
        self.request(&format!("terminal-restart {t}"), None)
            .map(|_| ())
    }

    /// Kills the program and closes its tab.
    pub fn close_terminal(&self, t: Term) -> Result<(), Error> {
        agreed(self, Feature::Terminals)?;
        self.request(&format!("terminal-close {t}"), None)
            .map(|_| ())
    }

    /// Opens the panel at the tab; `focus` gives it the keyboard.
    pub fn show_terminal(&self, t: Term, focus: bool) -> Result<(), Error> {
        agreed(self, Feature::Terminals)?;
        let how = if focus { "focus" } else { "show" };
        self.request(&format!("terminal-show {t} {how}"), None)
            .map(|_| ())
    }

    /// Types the bytes into the program (LF is Enter); needs `terminal = input`.
    pub fn terminal_input(&self, t: Term, bytes: &[u8]) -> Result<(), Error> {
        agreed(self, Feature::Terminals)?;
        if bytes.len() > 65536 {
            return Err(Error {
                code: "toobig".into(),
                text: "65536 bytes".into(),
            });
        }
        self.request(&format!("terminal-input {t}"), Some(bytes))
            .map(|_| ())
    }

    /// Reads the visible screen; needs `terminal = output`.
    pub fn terminal_screen(&self, t: Term) -> Result<Screen, Error> {
        agreed(self, Feature::Terminals)?;
        let r = self.request(&format!("terminal-screen {t}"), None)?;
        parse_screen(&r.body).ok_or_else(|| Error {
            code: "syntax".into(),
            text: "a terminal-screen body".into(),
        })
    }
}

// ---------------- patterns and matchers ----------------

/// The fields a pattern may hold, in the order captures are reported.
pub const PATTERN_FIELDS: [&str; 8] = [
    "file", "line", "col", "endline", "endcol", "severity", "code", "message",
];

/// The built-in matchers' text, by name.
pub fn builtin_matcher(name: &str) -> Option<&'static str> {
    Some(match name {
        "go" => "columns bytes\n1 {file}:{line}:{col}: {message}\n1 vet: {file}:{line}:{col}: {message}\n1  {file}:{line}: {message}\n",
        "gcc" => "columns bytes\n1 {file}:{line}:{col}: {severity}: {message}\n1 {file}:{line}: {severity}: {message}\n",
        "rustc" => "1 {severity}[{code}]: {message}\n1 {severity}: {message}\n2  --> {file}:{line}:{col}\n",
        "tsc" => "1 {file}({line},{col}): {severity} {code}: {message}\n1 {file}:{line}:{col} - {severity} {code}: {message}\n",
        "msbuild" => "1 {file}({line},{col}): {severity} {code}: {message}\n1 {file}({line},{col},{endline},{endcol}): {severity} {code}: {message}\n",
        _ => return None,
    })
}

const ANY: i32 = 8;
const LIT: i32 = -1;
const BLANK: i32 = -2;
const SEVERITY_WORDS: [&str; 9] = [
    "fatal error",
    "error",
    "fatal",
    "warning",
    "warn",
    "info",
    "note",
    "hint",
    "help",
];

struct Tok {
    kind: i32,
    lit: Vec<u8>,
}

fn compile_pattern(p: &str) -> Result<Vec<Tok>, String> {
    let b = p.as_bytes();
    if b.is_empty() || b.len() > 512 {
        return Err("a pattern is 1 to 512 bytes".into());
    }
    let mut toks: Vec<Tok> = Vec::new();
    let mut seen = [false; 8];
    fn add(toks: &mut Vec<Tok>, t: Tok) -> Result<(), String> {
        if toks.len() >= 32 {
            return Err("more than 32 parts".into());
        }
        toks.push(t);
        Ok(())
    }
    fn literal(toks: &mut Vec<Tok>, c: u8) -> Result<(), String> {
        if let Some(t) = toks.last_mut() {
            if t.kind == LIT {
                t.lit.push(c);
                return Ok(());
            }
        }
        add(
            toks,
            Tok {
                kind: LIT,
                lit: vec![c],
            },
        )
    }
    let mut i = 0;
    while i < b.len() {
        let c = b[i];
        if c < 0x20 || c == 0x7f {
            return Err("a pattern is one line of text".into());
        }
        if c == b' ' {
            while i < b.len() && b[i] == b' ' {
                i += 1;
            }
            add(
                &mut toks,
                Tok {
                    kind: BLANK,
                    lit: Vec::new(),
                },
            )?;
        } else if c == b'{' {
            if i + 1 >= b.len() {
                return Err("{ without }".into());
            }
            if b[i + 1] == b'{' {
                literal(&mut toks, b'{')?;
                i += 2;
                continue;
            }
            let end = b[i + 1..]
                .iter()
                .position(|&x| x == b'}')
                .ok_or("{ without }")?;
            let name = &p[i + 1..i + 1 + end];
            let kind = if name == "_" {
                ANY
            } else {
                PATTERN_FIELDS
                    .iter()
                    .position(|f| *f == name)
                    .ok_or("unknown field")? as i32
            };
            if kind != ANY {
                if seen[kind as usize] {
                    return Err("a field twice".into());
                }
                seen[kind as usize] = true;
            }
            add(
                &mut toks,
                Tok {
                    kind,
                    lit: Vec::new(),
                },
            )?;
            i += end + 2;
        } else if c == b'}' {
            if i + 1 >= b.len() || b[i + 1] != b'}' {
                return Err("a } alone (write }})".into());
            }
            literal(&mut toks, b'}')?;
            i += 2;
        } else {
            literal(&mut toks, c)?;
            i += 1;
        }
    }
    Ok(toks)
}

/// What is wrong with a pattern, as rhun says it.
pub fn check_pattern(p: &str) -> Result<(), String> {
    compile_pattern(p).map(|_| ())
}

struct Run<'a> {
    toks: &'a [Tok],
    line: &'a [u8],
    steps: u32,
    caps: [(usize, usize); 8],
}

impl Run<'_> {
    fn from(&mut self, tok: usize, pos: usize) -> bool {
        self.steps += 1;
        if self.steps > 10000 {
            return false;
        }
        if tok == self.toks.len() {
            return pos == self.line.len();
        }
        let line = self.line;
        let kind = self.toks[tok].kind;
        match kind {
            LIT => {
                let lit = &self.toks[tok].lit;
                if !line[pos..].starts_with(lit) {
                    return false;
                }
                let n = lit.len();
                self.from(tok + 1, pos + n)
            }
            BLANK => {
                let mut q = pos;
                while q < line.len() && (line[q] == b' ' || line[q] == b'\t') {
                    q += 1;
                    if self.from(tok + 1, q) {
                        return true;
                    }
                    if self.steps > 10000 {
                        return false;
                    }
                }
                false
            }
            5 => {
                for w in SEVERITY_WORDS {
                    if pos + w.len() > line.len()
                        || !line[pos..pos + w.len()].eq_ignore_ascii_case(w.as_bytes())
                    {
                        continue;
                    }
                    self.caps[5] = (pos, w.len());
                    if self.from(tok + 1, pos + w.len()) {
                        return true;
                    }
                }
                false
            }
            1..=4 => {
                let mut n = 1;
                while n <= 9 && pos + n <= line.len() {
                    if !line[pos + n - 1].is_ascii_digit() {
                        return false;
                    }
                    self.caps[kind as usize] = (pos, n);
                    if self.from(tok + 1, pos + n) {
                        return true;
                    }
                    if self.steps > 10000 {
                        return false;
                    }
                    n += 1;
                }
                false
            }
            _ => {
                // {file}, {code}, {message}: 1 or more bytes; {_}: 0 or more; the fewest first
                let mut n = if kind == ANY { 0 } else { 1 };
                if kind == 0 && (pos >= line.len() || line[pos] == b' ' || line[pos] == b'\t') {
                    return false;
                }
                while pos + n <= line.len() {
                    if kind == 0 && n >= 3 && &line[pos + n - 3..pos + n] == b"://" {
                        return false;
                    }
                    if kind != ANY {
                        self.caps[kind as usize] = (pos, n);
                    }
                    if self.from(tok + 1, pos + n) {
                        return true;
                    }
                    if self.steps > 10000 {
                        return false;
                    }
                    n += 1;
                }
                false
            }
        }
    }
}

/// Matches a whole line against a pattern as rhun does: the captured fields (name, text), in
/// [`PATTERN_FIELDS`] order, or None when the line does not match or the pattern is not valid.
pub fn match_pattern(p: &str, line: &str) -> Option<Vec<(&'static str, String)>> {
    let toks = compile_pattern(p).ok()?;
    let mut run = Run {
        toks: &toks,
        line: line.as_bytes(),
        steps: 0,
        caps: [(0, 0); 8],
    };
    if !run.from(0, 0) {
        return None;
    }
    let mut out = Vec::new();
    for (k, name) in PATTERN_FIELDS.iter().enumerate() {
        if toks.iter().any(|t| t.kind == k as i32) {
            let (s, n) = run.caps[k];
            out.push((
                *name,
                String::from_utf8_lossy(&line.as_bytes()[s..s + n]).into_owned(),
            ));
        }
    }
    Some(out)
}

/// What is wrong with an inline matcher's text, as rhun says it.
pub fn check_matcher(text: &str) -> Result<(), String> {
    if text.len() > 4096 {
        return Err("a matcher is at most 4096 bytes".into());
    }
    let bad = || "a matcher line is severity S, columns chars|bytes, N PATTERN or loop".to_string();
    let mut steps = 0;
    let mut alts = [0; 4];
    for raw in text.split('\n') {
        let line = raw.trim_matches(|c| c == ' ' || c == '\t' || c == '\r');
        if line.is_empty() {
            continue;
        }
        let (word, rest) = match line.find([' ', '\t']) {
            Some(i) => (&line[..i], Some(&line[i + 1..])),
            None => (line, None),
        };
        match (word, rest) {
            ("loop", None) => {}
            (_, None) | ("loop", Some(_)) => return Err(bad()),
            ("severity", Some(r)) => {
                if !matches!(r, "error" | "warning" | "info" | "hint") {
                    return Err(bad());
                }
            }
            ("columns", Some(r)) => {
                if r != "bytes" && r != "chars" {
                    return Err(bad());
                }
            }
            (w, Some(r)) if w.len() == 1 && (b'1'..=b'4').contains(&w.as_bytes()[0]) => {
                let n = (w.as_bytes()[0] - b'0') as usize;
                if n != steps && n != steps + 1 {
                    return Err("steps go 1, 2, 3, 4 in order".into());
                }
                steps = n;
                if alts[n - 1] >= 8 {
                    return Err("more than 8 patterns in a step".into());
                }
                alts[n - 1] += 1;
                check_pattern(r)?;
            }
            _ => return Err(bad()),
        }
    }
    if steps == 0 {
        return Err("a matcher needs a step 1 pattern".into());
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn result(p: &str, line: &str) -> String {
        if let Err(e) = check_pattern(p) {
            return format!("error={e}");
        }
        match match_pattern(p, line) {
            None => "match=0".into(),
            Some(caps) => {
                let mut s = "match=1".to_string();
                for (k, v) in caps {
                    s += &format!(" {k}={v}");
                }
                s
            }
        }
    }

    #[test]
    fn pattern_vectors() {
        let data = include_str!("../../../v1.1/patterns.txt");
        let mut n = 0;
        for row in data.lines().filter(|l| !l.starts_with('#')) {
            let j = row.find('\t').unwrap();
            let i = row.rfind('\t').unwrap();
            assert_eq!(result(&row[..j], &row[j + 1..i]), &row[i + 1..], "{row}");
            n += 1;
        }
        assert!(n >= 30);
    }

    #[test]
    fn builtins_and_matcher_checks() {
        for n in ["go", "gcc", "rustc", "tsc", "msbuild"] {
            check_matcher(builtin_matcher(n).unwrap()).unwrap();
        }
        assert_eq!(
            check_matcher("2 {file}: {message}").unwrap_err(),
            "steps go 1, 2, 3, 4 in order"
        );
        assert_eq!(
            check_matcher("loop").unwrap_err(),
            "a matcher needs a step 1 pattern"
        );
        assert_eq!(
            check_matcher(&"1 {file}: {message}\n".repeat(9)).unwrap_err(),
            "more than 8 patterns in a step"
        );
    }

    #[test]
    fn encodes_as_rhun_reads_it() {
        let mut t = Task::new("build", "Go build", Program::shell("go build ./..."));
        t.group = "build".into();
        t.default = true;
        t.matchers.push(Matcher::Builtin("go".into()));
        let b = t.encode().unwrap();
        assert_eq!(b, b"build shell 8 14 0 37\nGo buildgo build ./...group build 0\ndefault 0\nmatcher go 0\n");
        let mut s = Task::new(
            "serve",
            "Serve",
            Program::argv(&["python3", "-m", "http.server"]),
        );
        s.ready.push("Serving HTTP on {_}".into());
        s.depends.push("build".into());
        let body = encode_task_list(&[t, s.clone()]).unwrap();
        let list = parse_task_list(&body).unwrap();
        assert_eq!(list[1].argv, ["python3", "-m", "http.server"]);
        assert_eq!(list[1].order, "sequence");
        s.busy.push("x".into());
        s.ready.clear();
        assert!(s.encode().unwrap_err().text.contains("busy needs ready"));
        let spec = TerminalSpec {
            label: "Shell".into(),
            detail: String::new(),
            program: Program::shell(""),
            reveal: None,
            output: false,
        };
        assert_eq!(spec.encode().unwrap(), b"- shell 5 0 0 0\nShell");
        let dup = [
            Task::new("a", "A", Program::shell("x")),
            Task::new("a", "B", Program::shell("x")),
        ];
        assert_eq!(
            encode_task_list(&dup).unwrap_err().text,
            "task 1: id listed twice"
        );
    }

    #[test]
    fn events() {
        let ev = |name: &str, f: &str, body: &[u8]| Event {
            name: name.into(),
            fields: f.split(' ').map(String::from).collect(),
            rest: f.into(),
            body: body.to_vec(),
        };
        assert_eq!(
            parse_terminal_event(&ev("terminal-output", "3 2 6", b"ab\ncd\n")),
            Some(TerminalEvent::Output {
                term: 3,
                dropped: 2,
                lines: vec!["ab".into(), "cd".into()]
            })
        );
        assert_eq!(
            parse_terminal_event(&ev("terminal-ended", "4 failed - 0 0", b"")),
            Some(TerminalEvent::Ended {
                term: 4,
                reason: "failed".into(),
                code: None,
                errors: 0,
                warnings: 0
            })
        );
        assert_eq!(
            parse_terminal_event(&ev("terminal-started", "4 81234 build", b"")),
            Some(TerminalEvent::Started {
                term: 4,
                pid: 81234,
                task: "build".into()
            })
        );
        assert_eq!(
            parse_terminal_event(&ev("terminal-started", "0 1 -", b"")),
            None
        );
        assert_eq!(
            parse_screen(b"2 4 1 0\n$ ls\na b\n").unwrap().lines,
            ["$ ls", "a b"]
        );
    }
}
