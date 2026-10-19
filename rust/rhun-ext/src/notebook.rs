//! Notebooks (feature `notebooks`, `v1.1/v1.1.md`): the extension reads a notebook file into
//! cells ([`crate::Handler::nb_open`]), writes them back ([`crate::Handler::nb_save`]) and runs
//! cells ([`crate::Handler::nb_run`]), answering with [`crate::Ext::nb_state`],
//! [`crate::Ext::nb_output`], [`crate::Ext::nb_stream`] and [`crate::Ext::nb_kernel_label`] from any
//! thread. rhun keeps the cells, their metadata and outputs; it never parses the file.
//!
//! ```no_run
//! use rhun_ext::notebook::{Cell, Notebook, NbOpen, NbRun, NbSave, Output, RunState};
//! use rhun_ext::{run, Ext, Feature, Handler};
//!
//! struct Toy;
//!
//! impl Handler for Toy {
//!     fn nb_open(&mut self, _ext: &Ext, req: &NbOpen) -> Result<Notebook, String> {
//!         let text = String::from_utf8_lossy(&req.data);
//!         Ok(Notebook { meta: Vec::new(), cells: vec![Cell::code("python", text.as_bytes())] })
//!     }
//!     fn nb_save(&mut self, _ext: &Ext, req: &NbSave) -> Result<Vec<u8>, String> {
//!         Ok(req.notebook.cells.iter().flat_map(|c| c.source.clone()).collect())
//!     }
//!     fn nb_run(&mut self, ext: &Ext, run: &NbRun) {
//!         let _ = ext.nb_state(run.nb, run.cell, run.run, RunState::Running, None);
//!         let _ = ext.nb_stream(run.nb, run.cell, run.run, false, b"hello\n");
//!         let _ = ext.nb_state(run.nb, run.cell, run.run, RunState::Done, Some(1));
//!     }
//! }
//!
//! fn main() {
//!     run(&[Feature::Notebooks], Toy).unwrap();
//! }
//! ```

use crate::{Error, Ext, Feature};

/// A notebook's id, a cell's and a run's, as rhun hands them out.
pub type NbId = u64;
pub type CellId = u64;
pub type RunId = u64;

/// `nb-open` and `nb-save` bodies, each way.
pub const BODY_MAX: usize = 64 << 20;
pub const CELLS_MAX: usize = 5000;
pub const SOURCE_MAX: usize = 4 << 20;
pub const OUTPUTS_MAX: usize = 1000;
pub const ITEMS_MAX: usize = 32;
pub const ITEM_MAX: usize = 16 << 20;
/// An `nb-stream` message's bytes.
pub const STREAM_MAX: usize = 64 << 10;
/// The kernel button's text.
pub const LABEL_MAX: usize = 200;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CellKind {
    Code,
    Markup,
}

/// A cell: its kind, language (a grammar name with spaces as `_`, `-` for none and for markup),
/// execution count, source, opaque metadata and outputs.
#[derive(Clone, Debug, PartialEq)]
pub struct Cell {
    pub kind: CellKind,
    pub lang: String,
    pub count: Option<u64>,
    pub source: Vec<u8>,
    pub meta: Vec<u8>,
    pub outputs: Vec<Output>,
}

impl Cell {
    pub fn code(lang: &str, source: &[u8]) -> Cell {
        Cell {
            kind: CellKind::Code,
            lang: if lang.is_empty() {
                "-".into()
            } else {
                lang.replace(' ', "_")
            },
            count: None,
            source: source.to_vec(),
            meta: Vec::new(),
            outputs: Vec::new(),
        }
    }

    pub fn markup(source: &[u8]) -> Cell {
        Cell {
            kind: CellKind::Markup,
            lang: "-".into(),
            count: None,
            source: source.to_vec(),
            meta: Vec::new(),
            outputs: Vec::new(),
        }
    }
}

/// A notebook body: its opaque metadata and its cells.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Notebook {
    pub meta: Vec<u8>,
    pub cells: Vec<Cell>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum OutputKind {
    Stdout,
    Stderr,
    Result,
    Display,
    Error,
}

impl OutputKind {
    pub fn word(self) -> &'static str {
        match self {
            OutputKind::Stdout => "stdout",
            OutputKind::Stderr => "stderr",
            OutputKind::Result => "result",
            OutputKind::Display => "display",
            OutputKind::Error => "error",
        }
    }

    fn parse(w: &str) -> Option<OutputKind> {
        Some(match w {
            "stdout" => OutputKind::Stdout,
            "stderr" => OutputKind::Stderr,
            "result" => OutputKind::Result,
            "display" => OutputKind::Display,
            "error" => OutputKind::Error,
            _ => return None,
        })
    }
}

/// One representation of an output: `kind` is `text`, `ansi`, `png`, `jpeg`, `gif`, `rich`,
/// `picture` or another token rhun keeps and returns; `width`/`height` the intrinsic logical size
/// of an image (0 0 natural).
#[derive(Clone, Debug, PartialEq)]
pub struct Item {
    pub kind: String,
    pub width: u32,
    pub height: u32,
    pub data: Vec<u8>,
}

impl Item {
    pub fn new(kind: &str, data: &[u8]) -> Item {
        Item {
            kind: kind.into(),
            width: 0,
            height: 0,
            data: data.to_vec(),
        }
    }
    pub fn text(s: &str) -> Item {
        Item::new("text", s.as_bytes())
    }
    /// UTF-8 with SGR colors.
    pub fn ansi(s: &str) -> Item {
        Item::new("ansi", s.as_bytes())
    }
    pub fn png(bytes: &[u8]) -> Item {
        Item::new("png", bytes)
    }
    pub fn jpeg(bytes: &[u8]) -> Item {
        Item::new("jpeg", bytes)
    }
    pub fn gif(bytes: &[u8]) -> Item {
        Item::new("gif", bytes)
    }
    /// The image's logical size (one image pixel is one logical pixel without it).
    pub fn size(mut self, width: u32, height: u32) -> Item {
        self.width = width;
        self.height = height;
        self
    }
}

/// An output: its kind, an optional id (Jupyter's display id), opaque bytes rhun returns on save
/// (`keep`), and its items in order of preference.
#[derive(Clone, Debug, PartialEq)]
pub struct Output {
    pub kind: OutputKind,
    pub id: Option<String>,
    pub keep: Vec<u8>,
    pub items: Vec<Item>,
}

impl Output {
    pub fn new(kind: OutputKind, items: Vec<Item>) -> Output {
        Output {
            kind,
            id: None,
            keep: Vec::new(),
            items,
        }
    }
}

fn token_ok(t: &str) -> bool {
    !t.is_empty()
        && t.len() <= 64
        && t.bytes()
            .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b"_.+/-".contains(&b))
}

struct Reader<'a> {
    d: &'a [u8],
    p: usize,
}

impl<'a> Reader<'a> {
    fn line(&mut self) -> Result<Vec<&'a str>, String> {
        let rest = &self.d[self.p..];
        let n = rest
            .iter()
            .position(|&b| b == b'\n')
            .ok_or("a line without its end")?;
        let line = std::str::from_utf8(&rest[..n]).map_err(|_| "a line that is not UTF-8")?;
        self.p += n + 1;
        Ok(line.split(' ').filter(|f| !f.is_empty()).collect())
    }
    fn take(&mut self, n: usize) -> Result<&'a [u8], String> {
        if self.d.len() - self.p < n {
            return Err("a body shorter than its lengths".into());
        }
        let b = &self.d[self.p..self.p + n];
        self.p += n;
        Ok(b)
    }
}

fn num(f: &str, max: usize) -> Result<usize, String> {
    if f.is_empty() || f.len() > 18 || !f.bytes().all(|b| b.is_ascii_digit()) {
        return Err(format!("not a number: {f}"));
    }
    let v: usize = f.parse().map_err(|_| format!("not a number: {f}"))?;
    if v > max {
        return Err(format!("{v} is over its cap {max}"));
    }
    Ok(v)
}

impl Notebook {
    /// Reads a notebook body.
    pub fn parse(body: &[u8]) -> Result<Notebook, String> {
        let mut r = Reader { d: body, p: 0 };
        let f = r.line()?;
        if f.len() != 2 {
            return Err("the notebook's first line is N META_LEN".into());
        }
        let n = num(f[0], CELLS_MAX)?;
        let meta = r.take(num(f[1], BODY_MAX)?)?.to_vec();
        let mut cells = Vec::with_capacity(n);
        for _ in 0..n {
            let f = r.line()?;
            if f.len() != 6 {
                return Err("a cell's line is KIND LANG COUNT SOURCE_LEN META_LEN NOUT".into());
            }
            let kind = match f[0] {
                "code" => CellKind::Code,
                "markup" => CellKind::Markup,
                k => return Err(format!("unknown cell kind {k}")),
            };
            let count = if f[2] == "-" {
                None
            } else {
                Some(num(f[2], usize::MAX >> 1)? as u64)
            };
            let source = r.take(num(f[3], SOURCE_MAX)?)?.to_vec();
            let cmeta = r.take(num(f[4], BODY_MAX)?)?.to_vec();
            let nout = num(f[5], OUTPUTS_MAX)?;
            let mut outputs = Vec::with_capacity(nout);
            for _ in 0..nout {
                outputs.push(read_output(&mut r)?);
            }
            cells.push(Cell {
                kind,
                lang: f[1].to_string(),
                count,
                source,
                meta: cmeta,
                outputs,
            });
        }
        if r.p != body.len() {
            return Err("bytes after the last cell".into());
        }
        Ok(Notebook { meta, cells })
    }

    /// Writes a notebook body.
    pub fn encode(&self) -> Vec<u8> {
        let mut b = format!("{} {}\n", self.cells.len(), self.meta.len()).into_bytes();
        b.extend_from_slice(&self.meta);
        for c in &self.cells {
            let kind = match c.kind {
                CellKind::Code => "code",
                CellKind::Markup => "markup",
            };
            let lang = if c.kind == CellKind::Markup || c.lang.is_empty() {
                "-"
            } else {
                c.lang.as_str()
            };
            let count = c.count.map(|n| n.to_string()).unwrap_or_else(|| "-".into());
            b.extend_from_slice(
                format!(
                    "{kind} {lang} {count} {} {} {}\n",
                    c.source.len(),
                    c.meta.len(),
                    c.outputs.len()
                )
                .as_bytes(),
            );
            b.extend_from_slice(&c.source);
            b.extend_from_slice(&c.meta);
            for o in &c.outputs {
                o.encode_into(&mut b);
            }
        }
        b
    }
}

fn read_output(r: &mut Reader) -> Result<Output, String> {
    let f = r.line()?;
    if f.len() != 4 {
        return Err("an output's line is KIND OUTID KEEP_LEN NITEM".into());
    }
    let kind = OutputKind::parse(f[0]).ok_or_else(|| format!("unknown output kind {}", f[0]))?;
    let id = if f[1] == "-" {
        None
    } else if token_ok(f[1]) {
        Some(f[1].to_string())
    } else {
        return Err(format!("bad output id {}", f[1]));
    };
    let keep = r.take(num(f[2], BODY_MAX)?)?.to_vec();
    let n = num(f[3], ITEMS_MAX)?;
    let mut items = Vec::with_capacity(n);
    for _ in 0..n {
        let f = r.line()?;
        if f.len() != 4 || !token_ok(f[0]) {
            return Err("an item's line is TYPE W H LEN".into());
        }
        let width = num(f[1], 65536)? as u32;
        let height = num(f[2], 65536)? as u32;
        let data = r.take(num(f[3], ITEM_MAX)?)?.to_vec();
        items.push(Item {
            kind: f[0].to_string(),
            width,
            height,
            data,
        });
    }
    Ok(Output {
        kind,
        id,
        keep,
        items,
    })
}

impl Output {
    /// Reads one output body (the whole of `body`).
    pub fn parse(body: &[u8]) -> Result<Output, String> {
        let mut r = Reader { d: body, p: 0 };
        let o = read_output(&mut r)?;
        if r.p != body.len() {
            return Err("bytes after the output".into());
        }
        Ok(o)
    }

    /// Writes its output body.
    pub fn encode(&self) -> Vec<u8> {
        let mut b = Vec::new();
        self.encode_into(&mut b);
        b
    }

    fn encode_into(&self, b: &mut Vec<u8>) {
        let id = self.id.as_deref().filter(|i| token_ok(i)).unwrap_or("-");
        b.extend_from_slice(
            format!(
                "{} {id} {} {}\n",
                self.kind.word(),
                self.keep.len(),
                self.items.len()
            )
            .as_bytes(),
        );
        b.extend_from_slice(&self.keep);
        for it in &self.items {
            let kind = if token_ok(&it.kind) {
                it.kind.as_str()
            } else {
                "text"
            };
            b.extend_from_slice(
                format!("{kind} {} {} {}\n", it.width, it.height, it.data.len()).as_bytes(),
            );
            b.extend_from_slice(&it.data);
        }
    }
}

/// `nb-open`: the file's path and bytes (empty for a file that does not exist yet).
#[derive(Clone, Debug)]
pub struct NbOpen {
    pub id: u64,
    pub nb: NbId,
    pub path: String,
    pub data: Vec<u8>,
}

/// `nb-save`: the path to write for, and the notebook as rhun has it.
#[derive(Clone, Debug)]
pub struct NbSave {
    pub id: u64,
    pub nb: NbId,
    pub path: String,
    pub notebook: Notebook,
}

/// `0 nb-run`: run the cell's source; answer with nb-state, nb-output, nb-stream for this run.
#[derive(Clone, Debug)]
pub struct NbRun {
    pub nb: NbId,
    pub cell: CellId,
    pub run: RunId,
    /// The cell's language as the notebook named it, or `-`.
    pub lang: String,
    pub source: Vec<u8>,
}

fn split_path(body: &[u8], with_data: bool) -> Option<(String, &[u8], &[u8])> {
    let nl = body.iter().position(|&b| b == b'\n')?;
    let head = std::str::from_utf8(&body[..nl]).ok()?;
    let f: Vec<&str> = head.split(' ').collect();
    let rest = &body[nl + 1..];
    let pl: usize = f.first()?.parse().ok()?;
    if pl > rest.len() {
        return None;
    }
    let path = String::from_utf8_lossy(&rest[..pl]).into_owned();
    let after = &rest[pl..];
    if with_data {
        if f.len() != 2 {
            return None;
        }
        let dl: usize = f[1].parse().ok()?;
        if dl != after.len() {
            return None;
        }
        Some((path, after, &[]))
    } else {
        if f.len() != 1 {
            return None;
        }
        Some((path, &[], after))
    }
}

impl NbOpen {
    pub(crate) fn parse(id: u64, fields: &[String], body: &[u8]) -> Option<NbOpen> {
        let nb = fields.first()?.parse().ok()?;
        let (path, data, _) = split_path(body, true)?;
        Some(NbOpen {
            id,
            nb,
            path,
            data: data.to_vec(),
        })
    }
}

impl NbSave {
    pub(crate) fn parse(id: u64, fields: &[String], body: &[u8]) -> Option<Result<NbSave, String>> {
        let nb = fields.first()?.parse().ok()?;
        let (path, _, rest) = split_path(body, false)?;
        Some(Notebook::parse(rest).map(|notebook| NbSave {
            id,
            nb,
            path,
            notebook,
        }))
    }
}

impl NbRun {
    pub(crate) fn parse(fields: &[String], body: &[u8]) -> Option<NbRun> {
        if fields.len() != 5 {
            return None;
        }
        Some(NbRun {
            nb: fields[0].parse().ok()?,
            cell: fields[1].parse().ok()?,
            run: fields[2].parse().ok()?,
            lang: fields[3].clone(),
            source: body.to_vec(),
        })
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RunState {
    Queued,
    Running,
    Done,
    Failed,
    Cancelled,
}

impl RunState {
    pub fn word(self) -> &'static str {
        match self {
            RunState::Queued => "queued",
            RunState::Running => "running",
            RunState::Done => "done",
            RunState::Failed => "failed",
            RunState::Cancelled => "cancelled",
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum KernelState {
    Idle,
    Busy,
    Starting,
    Dead,
    None,
}

impl KernelState {
    pub fn word(self) -> &'static str {
        match self {
            KernelState::Idle => "idle",
            KernelState::Busy => "busy",
            KernelState::Starting => "starting",
            KernelState::Dead => "dead",
            KernelState::None => "none",
        }
    }
}

/// What `nb-output` does with the cell's outputs.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum OutputOp<'a> {
    Append,
    /// The cell's output at this index.
    Replace(usize),
    /// The output with this id, anywhere in the notebook (Jupyter's update_display_data).
    ReplaceId(&'a str),
    Clear,
    /// The outputs clear when the next one arrives.
    ClearWait,
}

fn agreed(ext: &Ext) -> Result<(), Error> {
    if ext.has(Feature::Notebooks) {
        Ok(())
    } else {
        Err(Error {
            code: "unknown".into(),
            text: "notebooks was not agreed".into(),
        })
    }
}

impl Ext {
    /// A run's state and, when it has one, the cell's execution count. `stale` when the run was
    /// superseded (the cell ran again, was deleted, or the notebook closed).
    pub fn nb_state(
        &self,
        nb: NbId,
        cell: CellId,
        run: RunId,
        state: RunState,
        count: Option<u64>,
    ) -> Result<(), Error> {
        agreed(self)?;
        let count = count.map(|c| c.to_string()).unwrap_or_else(|| "-".into());
        self.request(
            &format!("nb-state {nb} {cell} {run} {} {count}", state.word()),
            None,
        )
        .map(|_| ())
    }

    /// Appends, replaces or clears a cell's outputs for a run.
    pub fn nb_output(
        &self,
        nb: NbId,
        cell: CellId,
        run: RunId,
        op: OutputOp,
        output: Option<&Output>,
    ) -> Result<(), Error> {
        agreed(self)?;
        let (word, target, body) = match op {
            OutputOp::Append => ("append", "-".to_string(), output),
            OutputOp::Replace(i) => ("replace", i.to_string(), output),
            OutputOp::ReplaceId(id) => ("replace", id.to_string(), output),
            OutputOp::Clear => ("clear", "-".to_string(), None),
            OutputOp::ClearWait => ("clear-wait", "-".to_string(), None),
        };
        let bytes = match body {
            Some(o) => o.encode(),
            None if matches!(op, OutputOp::Clear | OutputOp::ClearWait) => Vec::new(),
            None => {
                return Err(Error {
                    code: "syntax".into(),
                    text: "an output is needed".into(),
                })
            }
        };
        self.request(
            &format!("nb-output {nb} {cell} {run} {word} {target}"),
            Some(&bytes),
        )
        .map(|_| ())
    }

    /// Stream text of a run (stdout, or stderr): appended to the cell's last output when it is
    /// that stream. Longer data goes in several messages of at most [`STREAM_MAX`] bytes, cut at
    /// character boundaries.
    pub fn nb_stream(
        &self,
        nb: NbId,
        cell: CellId,
        run: RunId,
        stderr: bool,
        data: &[u8],
    ) -> Result<(), Error> {
        agreed(self)?;
        let name = if stderr { "stderr" } else { "stdout" };
        let mut rest = data;
        while !rest.is_empty() {
            let mut n = rest.len().min(STREAM_MAX);
            while n < rest.len() && n > 0 && (rest[n] & 0xc0) == 0x80 {
                n -= 1;
            }
            if n == 0 {
                n = rest.len().min(STREAM_MAX);
            }
            self.request(
                &format!("nb-stream {nb} {cell} {run} {name}"),
                Some(&rest[..n]),
            )?;
            rest = &rest[n..];
        }
        Ok(())
    }

    /// The kernel button's text (one line, cut at [`LABEL_MAX`] bytes) and its state.
    pub fn nb_kernel_label(&self, nb: NbId, state: KernelState, label: &str) -> Result<(), Error> {
        agreed(self)?;
        let one: String = label
            .chars()
            .map(|c| if c.is_control() { ' ' } else { c })
            .collect();
        let mut cut = one.len().min(LABEL_MAX);
        while !one.is_char_boundary(cut) {
            cut -= 1;
        }
        self.request(
            &format!("nb-kernel-label {nb} {}", state.word()),
            Some(&one.as_bytes()[..cut]),
        )
        .map(|_| ())
    }
}

/// A kernel started as the user (`nb-exec`).
pub type ExecId = u64;

/// A program to start as the user (`nb-exec`; the same body as `test-exec`'s).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Command {
    /// A name of `[needs] kernels`, or an absolute path whose last component is one.
    pub program: String,
    /// Empty: the notebook's folder.
    pub dir: String,
    pub args: Vec<String>,
    /// `NAME=VALUE`, added to the user's environment.
    pub env: Vec<String>,
}

impl Command {
    pub fn encode(&self) -> Vec<u8> {
        let mut b = format!(
            "{} {} {} {}\n{}{}",
            self.program.len(),
            self.dir.len(),
            self.args.len(),
            self.env.len(),
            self.program,
            self.dir
        )
        .into_bytes();
        for a in self.args.iter().chain(self.env.iter()) {
            b.extend_from_slice(format!("{}\n", a.len()).as_bytes());
            b.extend_from_slice(a.as_bytes());
        }
        b
    }
}

/// What `nb-exec-signal` sends to a kernel's process group.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ExecSignal {
    /// SIGINT (refused on Windows: send Jupyter's interrupt_request instead).
    Interrupt,
    Terminate,
    Kill,
}

impl Ext {
    /// Starts a kernel for notebook `nb` as the user, outside the sandbox; rhun asks the user the
    /// first time in a project, so this may wait. Its output goes to `kernel-EXEC.log` in
    /// `$RHUN_STATE`; [`Handler::nb_exec_exit`](crate::Handler::nb_exec_exit) tells its end.
    pub fn nb_exec(&self, nb: NbId, cmd: &Command) -> Result<ExecId, Error> {
        agreed(self)?;
        let r = self.request(&format!("nb-exec {nb}"), Some(&cmd.encode()))?;
        r.fields.first().and_then(|f| f.parse().ok()).ok_or(Error {
            code: "syntax".into(),
            text: "nb-exec's answer has no EXEC".into(),
        })
    }

    /// Interrupts, terminates or kills a kernel started with [`Ext::nb_exec`].
    pub fn nb_exec_signal(&self, exec: ExecId, sig: ExecSignal) -> Result<(), Error> {
        agreed(self)?;
        let word = match sig {
            ExecSignal::Interrupt => "interrupt",
            ExecSignal::Terminate => "terminate",
            ExecSignal::Kill => "kill",
        };
        self.request(&format!("nb-exec-signal {exec} {word}"), None)
            .map(|_| ())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sample() -> Notebook {
        let mut c = Cell::code("python", b"print(1)\n");
        c.count = Some(3);
        c.meta = b"{\"id\":\"a\"}".to_vec();
        let mut o = Output::new(
            OutputKind::Display,
            vec![Item::png(&[1, 2, 3]).size(40, 20), Item::text("<Figure>\n")],
        );
        o.id = Some("d1".into());
        o.keep = b"{}".to_vec();
        c.outputs.push(o);
        Notebook {
            meta: b"{\"k\":1}".to_vec(),
            cells: vec![c, Cell::markup(b"# T")],
        }
    }

    #[test]
    fn notebook_round_trip() {
        let n = sample();
        let b = n.encode();
        assert_eq!(Notebook::parse(&b).unwrap(), n);
        assert!(b.starts_with(b"2 7\n{\"k\":1}code python 3 9 10 1\nprint(1)\n{\"id\":\"a\"}display d1 2 2\n{}png 40 20 3\n"));
    }

    #[test]
    fn bad_bodies() {
        assert!(Notebook::parse(b"1 0\ncode python - 99 0 0\nx").is_err());
        assert!(Notebook::parse(b"1 0\nlens python - 1 0 0\nx").is_err());
        assert!(Notebook::parse(b"0 0\nextra").is_err());
        assert!(Output::parse(b"stdout BAD 0 0\n").is_err());
        assert!(Output::parse(b"stdout - 0 1\nText 0 0 0\n").is_err());
    }

    #[test]
    fn requests() {
        let o = NbOpen::parse(5, &["7".into()], b"4 4\n/a.bnb\x00\x01").unwrap();
        assert_eq!(
            (o.nb, o.path.as_str(), o.data.as_slice()),
            (7, "/a.b", &b"nb\x00\x01"[..])
        );
        let mut body = b"4\n/a.b".to_vec();
        body.extend(sample().encode());
        let s = NbSave::parse(6, &["7".into()], &body).unwrap().unwrap();
        assert_eq!(s.notebook, sample());
        let r = NbRun::parse(
            &[
                "7".into(),
                "2".into(),
                "9".into(),
                "python".into(),
                "3".into(),
            ],
            b"x=1",
        )
        .unwrap();
        assert_eq!((r.cell, r.run, r.source.as_slice()), (2, 9, &b"x=1"[..]));
    }

    #[test]
    fn command_body() {
        let c = Command {
            program: "python3".into(),
            dir: String::new(),
            args: vec!["-m".into(), "ipykernel_launcher".into()],
            env: vec!["A=b".into()],
        };
        assert_eq!(
            c.encode(),
            b"7 0 2 1\npython32\n-m18\nipykernel_launcher3\nA=b".to_vec()
        );
    }
}
