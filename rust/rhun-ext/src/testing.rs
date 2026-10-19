//! The `tests` feature (protocol 1.1, "Feature: tests"): a test controller publishes a tree of
//! test items, answers [`Handler::resolve_tests`](crate::Handler::resolve_tests) for lazy ones,
//! runs what rhun asks with [`Handler::run_tests`](crate::Handler::run_tests) and reports results
//! until [`Ext::end_test_run`]. [`Ext::test_exec`] starts a program as the user (one of the
//! manifest's `[needs] tests`) and streams its output back.
//!
//! Lines and columns here are one-based, as the feature's bodies carry them.

use std::collections::{HashMap, VecDeque};
use std::io::{self, Read};
use std::sync::mpsc::{channel, RecvTimeoutError, Sender};
use std::sync::{Arc, Condvar, Mutex};
use std::thread::{self, JoinHandle};
use std::time::Duration;

use crate::{Error, Ext, Feature, Pos, Range};

/// Bytes of an item id.
pub const MAX_TEST_ID: usize = 512;
/// Bytes of an item label.
pub const MAX_TEST_LABEL: usize = 200;
/// Items per body.
pub const MAX_TEST_ITEMS: usize = 10_000;
/// Records per `test-update`.
pub const MAX_TEST_RESULTS: usize = 2_000;
/// Bytes per `test-update` body.
pub const MAX_TEST_RESULTS_BODY: usize = 1 << 20;
/// Bytes of a message rhun keeps.
pub const MAX_TEST_MESSAGE: usize = 8_000;
/// Bytes of an expected or actual value rhun keeps.
pub const MAX_TEST_VALUE: usize = 64 << 10;
const MAX_ITEMS_BODY: usize = 4 << 20;
const MAX_COVERAGE_BODY: usize = 4 << 20;
/// Bytes of a process's output waiting to be read, per stream.
const MAX_OUT_QUEUE: usize = 1 << 20;

fn err(code: &str, text: impl Into<String>) -> Error {
    Error {
        code: code.into(),
        text: text.into(),
    }
}

/// What an item is.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum TestKind {
    /// A module, package, crate or folder.
    Group,
    File,
    Suite,
    Test,
}

impl TestKind {
    pub fn name(self) -> &'static str {
        match self {
            TestKind::Group => "group",
            TestKind::File => "file",
            TestKind::Suite => "suite",
            TestKind::Test => "test",
        }
    }

    pub fn parse(s: &str) -> Option<TestKind> {
        Some(match s {
            "group" => TestKind::Group,
            "file" => TestKind::File,
            "suite" => TestKind::Suite,
            "test" => TestKind::Test,
            _ => return None,
        })
    }
}

/// One node of the tree. `range` (one-based lines and byte columns) is in `path`, an absolute
/// file path (or `#DOC` for an open document's file).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TestItem {
    pub id: String,
    pub label: String,
    pub kind: TestKind,
    pub path: String,
    pub range: Option<Range>,
    /// It has children rhun has not been given: rhun asks with `tests-resolve`.
    pub lazy: bool,
    /// No Debug for it.
    pub nodebug: bool,
    /// Dim text after the label.
    pub desc: String,
    /// A discovery problem shown on the row.
    pub error: String,
}

impl TestItem {
    pub fn new(kind: TestKind, id: impl Into<String>, label: impl Into<String>) -> TestItem {
        TestItem {
            id: id.into(),
            label: label.into(),
            kind,
            path: String::new(),
            range: None,
            lazy: false,
            nodebug: false,
            desc: String::new(),
            error: String::new(),
        }
    }

    /// Where it is: a file and, when `range` is given, its lines and columns there.
    pub fn at(mut self, path: impl Into<String>, range: Option<Range>) -> TestItem {
        self.path = path.into();
        self.range = range;
        self
    }

    pub fn lazy(mut self) -> TestItem {
        self.lazy = true;
        self
    }
}

/// A run rhun asks for: its number, profile (`run`, `debug` or `coverage`) and the items
/// (none: everything).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TestRun {
    pub run: u64,
    pub profile: String,
    pub items: Vec<String>,
}

/// A result's state.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum TestState {
    Queued,
    Started,
    Passed,
    Failed,
    Errored,
    Skipped,
}

impl TestState {
    pub fn name(self) -> &'static str {
        match self {
            TestState::Queued => "queued",
            TestState::Started => "started",
            TestState::Passed => "passed",
            TestState::Failed => "failed",
            TestState::Errored => "errored",
            TestState::Skipped => "skipped",
        }
    }
}

/// A failure's text, where it points, and the values compared.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct TestMessage {
    pub text: String,
    pub path: String,
    pub range: Option<Range>,
    pub expected: Option<String>,
    pub actual: Option<String>,
}

impl TestMessage {
    pub fn new(text: impl Into<String>) -> TestMessage {
        TestMessage {
            text: text.into(),
            ..TestMessage::default()
        }
    }

    /// Where it points: a file, lines and columns.
    pub fn at(mut self, path: impl Into<String>, range: Range) -> TestMessage {
        self.path = path.into();
        self.range = Some(range);
        self
    }

    /// The values compared (rhun shows a diff).
    pub fn values(mut self, expected: impl Into<String>, actual: impl Into<String>) -> TestMessage {
        self.expected = Some(expected.into());
        self.actual = Some(actual.into());
        self
    }
}

/// One record of a `test-update`: an item (`""` for the run itself, which takes only
/// [`TestState::Errored`] or no state), a state (`None`: output only), a duration, messages
/// and output.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct TestResult {
    pub item: String,
    pub state: Option<TestState>,
    pub duration: Option<Duration>,
    pub messages: Vec<TestMessage>,
    pub output: Vec<u8>,
}

impl TestResult {
    pub fn new(item: impl Into<String>, state: TestState) -> TestResult {
        TestResult {
            item: item.into(),
            state: Some(state),
            ..TestResult::default()
        }
    }

    /// Output of an item (`""`: the run's), with no state.
    pub fn output(item: impl Into<String>, bytes: impl Into<Vec<u8>>) -> TestResult {
        TestResult {
            item: item.into(),
            output: bytes.into(),
            ..TestResult::default()
        }
    }

    pub fn took(mut self, d: Duration) -> TestResult {
        self.duration = Some(d);
        self
    }

    pub fn message(mut self, m: TestMessage) -> TestResult {
        self.messages.push(m);
        self
    }
}

/// Lines `from` to `to` (one-based, inclusive) and how often they ran.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct CoverageRange {
    pub from: u32,
    pub to: u32,
    pub count: u64,
}

/// A file's covered ranges.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct FileCoverage {
    pub path: String,
    pub ranges: Vec<CoverageRange>,
}

/// A program to start as the user: a bare name from `[needs] tests`, its folder (`""` for the
/// project), arguments and extra `NAME=VALUE` variables.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct TestCommand {
    pub program: String,
    pub dir: String,
    pub args: Vec<String>,
    pub env: Vec<String>,
}

impl TestCommand {
    pub fn new(program: impl Into<String>) -> TestCommand {
        TestCommand {
            program: program.into(),
            ..TestCommand::default()
        }
    }

    pub fn dir(mut self, dir: impl Into<String>) -> TestCommand {
        self.dir = dir.into();
        self
    }

    pub fn arg(mut self, a: impl Into<String>) -> TestCommand {
        self.args.push(a.into());
        self
    }

    pub fn args<I, S>(mut self, args: I) -> TestCommand
    where
        I: IntoIterator<Item = S>,
        S: Into<String>,
    {
        self.args.extend(args.into_iter().map(Into::into));
        self
    }

    pub fn env(mut self, name_value: impl Into<String>) -> TestCommand {
        self.env.push(name_value.into());
        self
    }
}

fn text_ok(s: &str, max: usize, allow_empty: bool) -> bool {
    s.len() <= max && (allow_empty || !s.is_empty()) && !s.contains(['\n', '\r', '\0'])
}

/// The items body for `parent` (`""`: the roots).
pub fn encode_test_items(parent: &str, items: &[TestItem]) -> Result<Vec<u8>, Error> {
    if items.len() > MAX_TEST_ITEMS {
        return Err(err("toobig", "10000 items per body"));
    }
    if !parent.is_empty() && !text_ok(parent, MAX_TEST_ID, false) {
        return Err(err("syntax", "bad parent id"));
    }
    let mut b = format!("{} {}\n{parent}", parent.len(), items.len()).into_bytes();
    for it in items {
        if !text_ok(&it.id, MAX_TEST_ID, false)
            || !text_ok(&it.label, MAX_TEST_LABEL, true)
            || !text_ok(&it.path, 4095, true)
            || !text_ok(&it.desc, 200, true)
            || it.error.len() > 1000
        {
            return Err(err("syntax", format!("bad item {:?}", it.id)));
        }
        let mut flags = Vec::new();
        if it.lazy {
            flags.push("lazy");
        }
        if it.nodebug {
            flags.push("nodebug");
        }
        let flags = if flags.is_empty() {
            "-".to_string()
        } else {
            flags.join(",")
        };
        let mut extra = String::new();
        if !it.desc.is_empty() {
            extra.push_str(&format!("desc {}\n{}", it.desc.len(), it.desc));
        }
        if !it.error.is_empty() {
            extra.push_str(&format!("error {}\n{}", it.error.len(), it.error));
        }
        let (l1, c1, l2, c2) = match it.range {
            None => (0, 0, 0, 0),
            Some(r) => {
                let (l1, c1, l2, c2) = (r.start.line, r.start.col, r.end.line, r.end.col);
                if l1 < 1
                    || c1 < 1
                    || c2 < 1
                    || l2 < l1
                    || (l2 == l1 && c2 < c1)
                    || it.path.is_empty()
                {
                    return Err(err("badpos", format!("bad range of {:?}", it.id)));
                }
                (l1, c1, l2, c2)
            }
        };
        b.extend_from_slice(
            format!(
                "{} {l1} {c1} {l2} {c2} {flags} {} {} {} {}\n{}{}{}{extra}",
                it.kind.name(),
                it.id.len(),
                it.label.len(),
                it.path.len(),
                extra.len(),
                it.id,
                it.label,
                it.path
            )
            .as_bytes(),
        );
    }
    if b.len() > MAX_ITEMS_BODY {
        return Err(err("toobig", "4 MiB per body"));
    }
    Ok(b)
}

fn split_line(b: &[u8]) -> Option<(&str, &[u8])> {
    let nl = b.iter().position(|&c| c == b'\n')?;
    Some((std::str::from_utf8(&b[..nl]).ok()?, &b[nl + 1..]))
}

/// Splits `NAME ARGS… LEN` LF and LEN bytes off the front: name, data, rest.
fn extra_record(b: &[u8]) -> Option<(&str, &[u8], &[u8])> {
    let (head, rest) = split_line(b)?;
    let f: Vec<&str> = head.split(' ').collect();
    if f.len() < 2 {
        return None;
    }
    let n: usize = f[f.len() - 1].parse().ok()?;
    if n > rest.len() {
        return None;
    }
    Some((f[0], &rest[..n], &rest[n..]))
}

/// Decodes an items body (checkers, tests): the parent and the items.
pub fn parse_test_items(body: &[u8]) -> Result<(String, Vec<TestItem>), String> {
    let (head, mut b) = split_line(body).ok_or("items: no header")?;
    let h: Vec<usize> = head
        .split(' ')
        .map(|s| s.parse().map_err(|_| "items: bad header"))
        .collect::<Result<_, _>>()?;
    if h.len() != 2 || h[0] > b.len() {
        return Err("items: bad header".into());
    }
    let parent = String::from_utf8_lossy(&b[..h[0]]).into_owned();
    b = &b[h[0]..];
    let mut items = Vec::new();
    for _ in 0..h[1] {
        let (line, rest) = split_line(b).ok_or("items: short item")?;
        let f: Vec<&str> = line.split(' ').collect();
        if f.len() != 10 {
            return Err("items: an item header has not 10 fields".into());
        }
        let num = |s: &str| {
            s.parse::<usize>()
                .map_err(|_| "items: bad number".to_string())
        };
        let kind = TestKind::parse(f[0]).ok_or("items: bad kind")?;
        let r = [num(f[1])?, num(f[2])?, num(f[3])?, num(f[4])?];
        let l = [num(f[6])?, num(f[7])?, num(f[8])?, num(f[9])?];
        let total: usize = l.iter().sum();
        if total > rest.len() {
            return Err("items: short item bytes".into());
        }
        let s = |a: usize, n: usize| String::from_utf8_lossy(&rest[a..a + n]).into_owned();
        let mut it = TestItem::new(kind, s(0, l[0]), s(l[0], l[1]));
        it.path = s(l[0] + l[1], l[2]);
        if r != [0; 4] {
            let p = |i: usize| r[i] as u32;
            it.range = Some(Range::new(Pos::new(p(0), p(1)), Pos::new(p(2), p(3))));
        }
        for fl in f[5].split(',') {
            match fl {
                "lazy" => it.lazy = true,
                "nodebug" => it.nodebug = true,
                _ => {}
            }
        }
        let mut extra = &rest[l[0] + l[1] + l[2]..total];
        while !extra.is_empty() {
            let (name, data, more) = extra_record(extra).ok_or("items: bad extra record")?;
            match name {
                "desc" => it.desc = String::from_utf8_lossy(data).into_owned(),
                "error" => it.error = String::from_utf8_lossy(data).into_owned(),
                _ => {}
            }
            extra = more;
        }
        items.push(it);
        b = &rest[total..];
    }
    if !b.is_empty() {
        return Err("items: trailing bytes".into());
    }
    Ok((parent, items))
}

/// A `test-update` body.
pub fn encode_test_results(rs: &[TestResult]) -> Result<Vec<u8>, Error> {
    if rs.len() > MAX_TEST_RESULTS {
        return Err(err("toobig", "2000 records per body"));
    }
    let mut b = format!("{}\n", rs.len()).into_bytes();
    for r in rs {
        if r.item.is_empty() && !matches!(r.state, None | Some(TestState::Errored)) {
            return Err(err("syntax", "the run itself takes errored or no state"));
        }
        if r.item.len() > MAX_TEST_ID || r.item.contains(['\n', '\r', '\0']) {
            return Err(err("syntax", "bad item"));
        }
        let state = r.state.map(TestState::name).unwrap_or("-");
        let ms = match (r.state, r.duration) {
            (Some(_), Some(d)) => d.as_millis().to_string(),
            _ => "-".to_string(),
        };
        let mut extra = Vec::new();
        for m in &r.messages {
            extra.extend_from_slice(format!("message {}\n{}", m.text.len(), m.text).as_bytes());
            if let (Some(rg), false) = (m.range, m.path.is_empty()) {
                extra.extend_from_slice(
                    format!(
                        "location {} {} {} {} {}\n{}",
                        rg.start.line,
                        rg.start.col,
                        rg.end.line,
                        rg.end.col,
                        m.path.len(),
                        m.path
                    )
                    .as_bytes(),
                );
            }
            if let Some(e) = &m.expected {
                extra.extend_from_slice(format!("expected {}\n{e}", e.len()).as_bytes());
            }
            if let Some(a) = &m.actual {
                extra.extend_from_slice(format!("actual {}\n{a}", a.len()).as_bytes());
            }
        }
        if !r.output.is_empty() {
            extra.extend_from_slice(format!("output {}\n", r.output.len()).as_bytes());
            extra.extend_from_slice(&r.output);
        }
        b.extend_from_slice(
            format!("{state} {ms} {} {}\n{}", r.item.len(), extra.len(), r.item).as_bytes(),
        );
        b.extend_from_slice(&extra);
    }
    if b.len() > MAX_TEST_RESULTS_BODY {
        return Err(err("toobig", "1 MiB per body"));
    }
    Ok(b)
}

/// A coverage body of these files.
pub fn encode_test_coverage(files: &[FileCoverage]) -> Vec<u8> {
    let mut b = format!("{}\n", files.len()).into_bytes();
    for f in files {
        b.extend_from_slice(format!("{} {}\n{}", f.path.len(), f.ranges.len(), f.path).as_bytes());
        for r in &f.ranges {
            b.extend_from_slice(format!("{} {} {}\n", r.from, r.to, r.count).as_bytes());
        }
    }
    b
}

/// A `test-exec` command body.
pub fn encode_test_command(c: &TestCommand) -> Result<Vec<u8>, Error> {
    let mut b = format!(
        "{} {} {} {}\n{}{}",
        c.program.len(),
        c.dir.len(),
        c.args.len(),
        c.env.len(),
        c.program,
        c.dir
    );
    for a in c.args.iter().chain(c.env.iter()) {
        b.push_str(&format!("{}\n{a}", a.len()));
    }
    if b.contains('\0') {
        return Err(err("syntax", "a NUL in the command"));
    }
    Ok(b.into_bytes())
}

// ---- test-exec processes

/// A stream of one process's output, filled by the thread that reads from rhun.
#[derive(Default)]
struct StreamBuf {
    buf: VecDeque<u8>,
    closed: bool,
}

#[derive(Default)]
struct Stream {
    m: Mutex<StreamBuf>,
    cv: Condvar,
}

impl Stream {
    /// Adds bytes, waiting while the stream holds 1 MiB (rhun's back-pressure reaches the program
    /// through its pipe); a closed stream drops them.
    fn put(&self, b: &[u8]) {
        let mut s = self.m.lock().unwrap();
        while s.buf.len() >= MAX_OUT_QUEUE && !s.closed {
            s = self.cv.wait(s).unwrap();
        }
        if !s.closed {
            s.buf.extend(b);
        }
        self.cv.notify_all();
    }

    fn close(&self) {
        self.m.lock().unwrap().closed = true;
        self.cv.notify_all();
    }
}

/// What one process wrote to its standard output or error, as rhun reads it.
#[derive(Clone)]
pub struct TestStream(Arc<Stream>);

impl Read for TestStream {
    fn read(&mut self, out: &mut [u8]) -> io::Result<usize> {
        let mut s = self.0.m.lock().unwrap();
        while s.buf.is_empty() && !s.closed {
            s = self.0.cv.wait(s).unwrap();
        }
        let n = out.len().min(s.buf.len());
        for (o, b) in out.iter_mut().zip(s.buf.drain(..n)) {
            *o = b;
        }
        self.0.cv.notify_all();
        Ok(n)
    }
}

#[derive(Default)]
struct ProcState {
    code: Option<String>,
    /// TestExec has it (an exit before its reply was read keeps the entry for it).
    claimed: bool,
}

#[derive(Default)]
pub(crate) struct Proc {
    out: Arc<Stream>,
    err: Arc<Stream>,
    st: Mutex<ProcState>,
    cv: Condvar,
}

impl Proc {
    fn end(&self, code: &str) {
        self.out.close();
        self.err.close();
        let mut s = self.st.lock().unwrap();
        if s.code.is_none() {
            s.code = Some(code.to_string());
        }
        self.cv.notify_all();
    }
}

/// The processes of `test-exec`, by EXEC.
pub(crate) type Procs = Arc<Mutex<HashMap<u64, Arc<Proc>>>>;

fn proc_for(procs: &Procs, ex: u64) -> Arc<Proc> {
    procs.lock().unwrap().entry(ex).or_default().clone()
}

/// `0 test-out EXEC STREAM LEN` + bytes, on the reading thread.
pub(crate) fn test_out(procs: &Procs, ex: u64, stream: &str, body: &[u8]) {
    let p = proc_for(procs, ex);
    if stream == "err" {
        p.err.put(body);
    } else {
        p.out.put(body);
    }
}

/// `0 test-exit EXEC CODE`, on the reading thread.
pub(crate) fn test_exit(procs: &Procs, ex: u64, code: &str) {
    let p = proc_for(procs, ex);
    p.end(code);
    let mut m = procs.lock().unwrap();
    if p.st.lock().unwrap().claimed {
        m.remove(&ex);
    }
}

/// rhun went away: every process's streams end, as killed.
pub(crate) fn tests_gone(procs: &Procs) {
    let ps: Vec<Arc<Proc>> = procs.lock().unwrap().drain().map(|(_, p)| p).collect();
    for p in ps {
        p.end("killed");
    }
}

/// A program rhun started for a run. Read `stdout` and `stderr` on threads that never wait for
/// rhun (report through a [`TestReporter`]): rhun stops sending output while it is not read, and
/// the thread that reads from rhun waits while a stream holds 1 MiB.
pub struct TestProcess {
    pub exec: u64,
    pub stdout: TestStream,
    pub stderr: TestStream,
    p: Arc<Proc>,
}

impl TestProcess {
    /// Waits for the program's end: its exit status (`"0"`, `"1"`), `"signal N"`, or `"killed"`
    /// (rhun stopped it).
    pub fn wait(&self) -> String {
        let mut s = self.p.st.lock().unwrap();
        while s.code.is_none() {
            s = self.p.cv.wait(s).unwrap();
        }
        s.code.clone().unwrap_or_default()
    }

    /// [`TestProcess::wait`]'s code as a number, -1 when it is not an exit status.
    pub fn exit_code(&self) -> i32 {
        self.wait().parse().unwrap_or(-1)
    }
}

impl Ext {
    fn tests_agreed(&self) -> Result<(), Error> {
        if self.has(Feature::Tests) {
            Ok(())
        } else {
            Err(err("unknown", "tests was not negotiated"))
        }
    }

    /// Publishes items under `parent` (`""` for the roots): `replace` makes them its only
    /// children, in this order; otherwise they are added or updated.
    pub fn set_test_items(
        &self,
        parent: &str,
        items: &[TestItem],
        replace: bool,
    ) -> Result<(), Error> {
        self.tests_agreed()?;
        let body = encode_test_items(parent, items)?;
        let mode = if replace { "replace" } else { "add" };
        self.request(&format!("test-items {mode}"), Some(&body))
            .map(|_| ())
    }

    /// Removes items and their descendants.
    pub fn remove_test_items(&self, ids: &[&str]) -> Result<(), Error> {
        self.tests_agreed()?;
        let mut b = Vec::new();
        for id in ids {
            if !text_ok(id, MAX_TEST_ID, false) {
                return Err(err("syntax", "bad id"));
            }
            b.extend_from_slice(id.as_bytes());
            b.push(b'\n');
        }
        self.request(&format!("test-remove {}", ids.len()), Some(&b))
            .map(|_| ())
    }

    /// Sends results of the run; bodies past rhun's caps (2,000 records, 1 MiB) are split.
    pub fn report_tests(&self, run: u64, rs: &[TestResult]) -> Result<(), Error> {
        self.tests_agreed()?;
        let mut rs = rs;
        while !rs.is_empty() {
            let mut n = rs.len().min(MAX_TEST_RESULTS);
            let mut body = encode_test_results(&rs[..n]);
            while n > 1 && matches!(&body, Err(e) if e.code == "toobig") {
                n /= 2;
                body = encode_test_results(&rs[..n]);
            }
            self.request(&format!("test-update {run}"), Some(&body?))?;
            rs = &rs[n..];
        }
        Ok(())
    }

    /// Says the run is over.
    pub fn end_test_run(&self, run: u64) -> Result<(), Error> {
        self.tests_agreed()?;
        self.request(&format!("test-end {run}"), None).map(|_| ())
    }

    /// Sends line coverage found by a coverage run, in messages of at most 4 MiB.
    pub fn report_coverage(&self, run: u64, files: &[FileCoverage]) -> Result<(), Error> {
        self.tests_agreed()?;
        let mut files = files;
        while !files.is_empty() {
            let mut n = 1;
            let mut size = encode_test_coverage(&files[..1]).len();
            while n < files.len() {
                let more = encode_test_coverage(&files[n..n + 1]).len() - 2;
                if size + more > MAX_COVERAGE_BODY - 32 {
                    break;
                }
                size += more;
                n += 1;
            }
            let body = encode_test_coverage(&files[..n]);
            self.request(&format!("test-coverage {run}"), Some(&body))?;
            files = &files[n..];
        }
        Ok(())
    }

    /// Asks rhun to start a program for the run (it may ask the user first; the call waits).
    pub fn test_exec(&self, run: u64, c: &TestCommand) -> Result<TestProcess, Error> {
        self.tests_agreed()?;
        let body = encode_test_command(c)?;
        let r = self.request(&format!("test-exec {run}"), Some(&body))?;
        let ex: u64 = match r.fields.as_slice() {
            [e] => e.parse().ok().filter(|&n| n > 0),
            _ => None,
        }
        .ok_or_else(|| err("syntax", "test-exec: bad reply"))?;
        let p = proc_for(&self.procs, ex);
        {
            let mut m = self.procs.lock().unwrap();
            let mut s = p.st.lock().unwrap();
            s.claimed = true;
            if s.code.is_some() {
                m.remove(&ex);
            }
        }
        Ok(TestProcess {
            exec: ex,
            stdout: TestStream(p.out.clone()),
            stderr: TestStream(p.err.clone()),
            p,
        })
    }
}

// ---- reporting

enum Entry {
    Result(TestResult),
    Items(String, Vec<TestItem>),
}

struct ReporterShared {
    ext: Ext,
    run: u64,
    send: Mutex<()>,
    queue: Mutex<Vec<Entry>>,
    err: Mutex<Option<Error>>,
}

impl ReporterShared {
    fn fail(&self, e: Error) {
        let mut s = self.err.lock().unwrap();
        if s.is_none() {
            *s = Some(e);
        }
    }

    fn flush(&self) {
        let _one = self.send.lock().unwrap();
        let q = std::mem::take(&mut *self.queue.lock().unwrap());
        let mut batch = Vec::new();
        let send = |batch: &mut Vec<TestResult>| {
            if !batch.is_empty() {
                if let Err(e) = self.ext.report_tests(self.run, batch) {
                    self.fail(e);
                }
                batch.clear();
            }
        };
        for en in q {
            match en {
                Entry::Result(r) => batch.push(r),
                Entry::Items(parent, items) => {
                    send(&mut batch);
                    if let Err(e) = self.ext.set_test_items(&parent, &items, false) {
                        self.fail(e);
                    }
                }
            }
        }
        send(&mut batch);
    }
}

/// Batches results of one run and sends them from a thread of its own every 100 ms, so the
/// thread that reads a test program's output never waits for rhun.
pub struct TestReporter {
    shared: Arc<ReporterShared>,
    kick: Option<Sender<()>>,
    thread: Option<JoinHandle<()>>,
}

impl TestReporter {
    pub fn new(ext: &Ext, run: u64) -> TestReporter {
        let shared = Arc::new(ReporterShared {
            ext: ext.clone(),
            run,
            send: Mutex::new(()),
            queue: Mutex::new(Vec::new()),
            err: Mutex::new(None),
        });
        let (kick, rx) = channel::<()>();
        let s = shared.clone();
        let thread = thread::spawn(move || loop {
            match rx.recv_timeout(Duration::from_millis(100)) {
                Err(RecvTimeoutError::Disconnected) => {
                    s.flush();
                    return;
                }
                _ => s.flush(),
            }
        });
        TestReporter {
            shared,
            kick: Some(kick),
            thread: Some(thread),
        }
    }

    /// Queues results.
    pub fn add(&self, r: TestResult) {
        self.shared.queue.lock().unwrap().push(Entry::Result(r));
    }

    /// Queues items to add under `parent` (a subtest found while running), sent before the
    /// results queued after them.
    pub fn add_items(&self, parent: &str, items: Vec<TestItem>) {
        if !items.is_empty() {
            self.shared
                .queue
                .lock()
                .unwrap()
                .push(Entry::Items(parent.to_string(), items));
        }
    }

    /// Sends what is queued now, from the calling thread (before a coverage report, say).
    pub fn flush(&self) {
        self.shared.flush();
    }

    /// Sends what is queued and stops the reporter: the first error a send had.
    pub fn close(mut self) -> Result<(), Error> {
        self.stop();
        match self.shared.err.lock().unwrap().take() {
            Some(e) => Err(e),
            None => Ok(()),
        }
    }

    fn stop(&mut self) {
        self.kick.take();
        if let Some(t) = self.thread.take() {
            let _ = t.join();
        }
    }
}

impl Drop for TestReporter {
    fn drop(&mut self) {
        self.stop();
    }
}

// ---- requests from rhun

pub(crate) fn parse_test_run(fields: &[String], body: &[u8]) -> Option<TestRun> {
    // RUN PROFILE N LEN
    if fields.len() != 4 {
        return None;
    }
    let run: u64 = fields[0].parse().ok().filter(|&n| n > 0)?;
    let items: Vec<String> = String::from_utf8_lossy(body)
        .split('\n')
        .filter(|s| !s.is_empty())
        .map(str::to_string)
        .collect();
    Some(TestRun {
        run,
        profile: fields[1].clone(),
        items,
    })
}

/// A `test-cancel` event's run.
pub fn parse_test_cancel(ev: &crate::Event) -> Option<u64> {
    if ev.name != "test-cancel" || ev.fields.len() != 1 {
        return None;
    }
    ev.fields[0].parse().ok().filter(|&n| n > 0)
}

// ---- failure values

fn line_value<'a>(text: &'a str, key: &str) -> Option<&'a str> {
    // "^\s*KEY\s*:\s?(.*)$" with KEY's own spaces as written ("got", "expected", "left")
    for line in text.split('\n') {
        let t = line.trim_start();
        if let Some(rest) = t.strip_prefix(key) {
            let rest = rest.trim_start_matches([' ', '\t']);
            if let Some(v) = rest.strip_prefix(':') {
                return Some(v.strip_prefix(' ').unwrap_or(v).trim_end_matches('\r'));
            }
        }
    }
    None
}

/// The values a failure compared: Go's `got: X` / `want: Y` lines or `f(x) = GOT, want WANT`,
/// testify's `expected: X` / `actual  : Y`, and Rust's `left: X` / `right: Y`
/// (`assert_eq!(actual, expected)` by convention: left is actual). Quoted values are unquoted
/// (Go strings, Rust Debug strings). `(expected, actual)`.
pub fn expected_actual(text: &str) -> Option<(String, String)> {
    for (e, a) in [("want", "got"), ("expected", "actual"), ("right", "left")] {
        if let (Some(ev), Some(av)) = (line_value(text, e), line_value(text, a)) {
            return Some((unquote(ev), unquote(av)));
        }
    }
    for line in text.split('\n') {
        if let Some(eq) = line.find("= ") {
            let rest = &line[eq + 2..];
            if let Some(w) = rest.rfind(", want ") {
                let (got, want) = (&rest[..w], &rest[w + 7..]);
                if !got.is_empty() && !want.is_empty() {
                    return Some((unquote(want), unquote(got)));
                }
            }
        }
    }
    None
}

/// A Go or Rust Debug string literal's value (`"a\nb"` with its escapes), a Go raw string's, or
/// the text as it is when it is not one.
pub fn unquote(s: &str) -> String {
    let s = s.trim();
    let b = s.as_bytes();
    if b.len() >= 2 && b[0] == b'`' && b[b.len() - 1] == b'`' {
        return s[1..s.len() - 1].to_string();
    }
    if b.len() >= 2 && b[0] == b'"' && b[b.len() - 1] == b'"' {
        return unescape(&s[1..s.len() - 1]);
    }
    s.to_string()
}

/// Go's and Rust's escapes: \n \t \r \\ \" \' \0 \a \b \f \v \xHH \u{X…} \uXXXX \UXXXXXXXX.
fn unescape(s: &str) -> String {
    let mut out = String::new();
    let mut bytes: Vec<u8> = Vec::new(); // \xHH runs (Go strings are bytes)
    let flush = |bytes: &mut Vec<u8>, out: &mut String| {
        if !bytes.is_empty() {
            out.push_str(&String::from_utf8_lossy(bytes));
            bytes.clear();
        }
    };
    let mut it = s.char_indices().peekable();
    while let Some((i, c)) = it.next() {
        if c != '\\' {
            flush(&mut bytes, &mut out);
            out.push(c);
            continue;
        }
        let Some((_, e)) = it.next() else {
            out.push('\\');
            break;
        };
        let hex = |from: usize, n: usize| -> Option<u32> {
            s.get(from..from + n)
                .and_then(|h| u32::from_str_radix(h, 16).ok())
        };
        match e {
            'x' => {
                if let Some(v) = hex(i + 2, 2) {
                    bytes.push(v as u8);
                    it.next();
                    it.next();
                    continue;
                }
                flush(&mut bytes, &mut out);
                out.push_str("\\x");
                continue;
            }
            _ => flush(&mut bytes, &mut out),
        }
        match e {
            'n' => out.push('\n'),
            't' => out.push('\t'),
            'r' => out.push('\r'),
            '0' => out.push('\0'),
            'a' => out.push('\x07'),
            'b' => out.push('\x08'),
            'f' => out.push('\x0c'),
            'v' => out.push('\x0b'),
            'u' if s[i + 2..].starts_with('{') => {
                let close = s[i + 2..].find('}');
                match close.and_then(|end| {
                    u32::from_str_radix(&s[i + 3..i + 2 + end], 16)
                        .ok()
                        .and_then(char::from_u32)
                        .map(|ch| (ch, end))
                }) {
                    Some((ch, end)) => {
                        out.push(ch);
                        for _ in 0..=end {
                            it.next();
                        }
                    }
                    None => out.push_str("\\u"),
                }
            }
            'u' | 'U' => {
                let n = if e == 'u' { 4 } else { 8 };
                match hex(i + 2, n).and_then(char::from_u32) {
                    Some(ch) => {
                        out.push(ch);
                        for _ in 0..n {
                            it.next();
                        }
                    }
                    None => {
                        out.push('\\');
                        out.push(e);
                    }
                }
            }
            other => out.push(other),
        }
    }
    flush(&mut bytes, &mut out);
    out
}

#[cfg(test)]
mod unit {
    use super::*;

    #[test]
    fn items_round_trip() {
        let mut it = TestItem::new(TestKind::Test, "pkg/TestA", "TestA").at(
            "/p/a_test.go",
            Some(Range::new(Pos::new(3, 1), Pos::new(9, 2))),
        );
        it.desc = "dim".into();
        it.error = "two\nlines".into();
        it.nodebug = true;
        let g = TestItem::new(TestKind::Group, "pkg", "pkg").lazy();
        let body = encode_test_items("root", &[it.clone(), g.clone()]).unwrap();
        let (parent, items) = parse_test_items(&body).unwrap();
        assert_eq!(parent, "root");
        assert_eq!(items, vec![it, g]);
        assert_eq!(
            String::from_utf8(
                encode_test_items("", &[TestItem::new(TestKind::File, "f", "")]).unwrap()
            )
            .unwrap(),
            "0 1\nfile 0 0 0 0 - 1 0 0 0\nf"
        );
    }

    #[test]
    fn items_are_checked() {
        let bad = TestItem::new(TestKind::Test, "a\nb", "x");
        assert_eq!(encode_test_items("", &[bad]).unwrap_err().code, "syntax");
        let r = TestItem::new(TestKind::Test, "a", "x")
            .at("/f", Some(Range::new(Pos::new(2, 5), Pos::new(2, 4))));
        assert_eq!(encode_test_items("", &[r]).unwrap_err().code, "badpos");
        let nopath = TestItem::new(TestKind::Test, "a", "x")
            .at("", Some(Range::new(Pos::new(1, 1), Pos::new(1, 1))));
        assert_eq!(encode_test_items("", &[nopath]).unwrap_err().code, "badpos");
        let many = vec![TestItem::new(TestKind::Test, "a", ""); MAX_TEST_ITEMS + 1];
        assert_eq!(encode_test_items("", &many).unwrap_err().code, "toobig");
    }

    #[test]
    fn results_body() {
        let r = TestResult::new("t", TestState::Failed)
            .took(Duration::from_millis(12))
            .message(
                TestMessage::new("boom")
                    .at("/f.rs", Range::new(Pos::new(4, 5), Pos::new(4, 5)))
                    .values("1", "2"),
            );
        let out = TestResult::output("", "build\n");
        let body = String::from_utf8(encode_test_results(&[r, out]).unwrap()).unwrap();
        assert_eq!(
            body,
            "2\nfailed 12 1 60\ntmessage 4\nboomlocation 4 5 4 5 5\n/f.rsexpected 1\n1actual 1\n2\
             - - 0 15\noutput 6\nbuild\n"
        );
        let bad = TestResult::new("", TestState::Passed);
        assert_eq!(encode_test_results(&[bad]).unwrap_err().code, "syntax");
    }

    #[test]
    fn command_and_coverage_bodies() {
        let c = TestCommand::new("go")
            .dir("/p")
            .args(["test", "-json"])
            .env("A=1");
        assert_eq!(
            String::from_utf8(encode_test_command(&c).unwrap()).unwrap(),
            "2 2 2 1\ngo/p4\ntest5\n-json3\nA=1"
        );
        assert_eq!(
            encode_test_command(&TestCommand::new("x").arg("a\0"))
                .unwrap_err()
                .code,
            "syntax"
        );
        let f = FileCoverage {
            path: "/a.go".into(),
            ranges: vec![CoverageRange {
                from: 1,
                to: 3,
                count: 2,
            }],
        };
        assert_eq!(
            String::from_utf8(encode_test_coverage(&[f])).unwrap(),
            "1\n5 1\n/a.go1 3 2\n"
        );
    }

    #[test]
    fn failure_values() {
        assert_eq!(
            expected_actual("    got: \"a\\nb\"\n    want: \"c\""),
            Some(("c".into(), "a\nb".into()))
        );
        assert_eq!(
            expected_actual("Add(1, 2) = 4, want 3"),
            Some(("3".into(), "4".into()))
        );
        assert_eq!(
            expected_actual("Error: Not equal:\n expected: 1\n actual  : 2"),
            Some(("1".into(), "2".into()))
        );
        assert_eq!(
            expected_actual(
                "assertion `left == right` failed\n  left: \"x\\u{e9}\"\n right: \"y\""
            ),
            Some(("y".into(), "x\u{e9}".into()))
        );
        assert_eq!(expected_actual("just failed"), None);
        assert_eq!(unquote("`raw\\n`"), "raw\\n");
        assert_eq!(unquote("\"\\xc3\\xa9\\t\""), "\u{e9}\t");
        assert_eq!(unquote("plain"), "plain");
    }
}
