//! The crate against a scripted rhun: what it writes, what it reads, in order.

use std::io::{self, BufRead, BufReader, Read, Write};
use std::sync::mpsc::{channel, Receiver, Sender};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::Duration;

use rhun_ext::{
    run_on, CompleteRequest, CompletionItem, CompletionList, Ext, Feature, Handler, Kind, Picture, Pos, Range,
    SignatureHelp, SignatureRequest, View, ViewEvent,
};

struct ChanReader {
    rx: Receiver<Vec<u8>>,
    buf: Vec<u8>,
}

impl Read for ChanReader {
    fn read(&mut self, out: &mut [u8]) -> io::Result<usize> {
        if self.buf.is_empty() {
            match self.rx.recv() {
                Ok(b) => self.buf = b,
                Err(_) => return Ok(0),
            }
        }
        let n = out.len().min(self.buf.len());
        out[..n].copy_from_slice(&self.buf[..n]);
        self.buf.drain(..n);
        Ok(n)
    }
}

struct ChanWriter(Sender<Vec<u8>>);

impl Write for ChanWriter {
    fn write(&mut self, b: &[u8]) -> io::Result<usize> {
        let _ = self.0.send(b.to_vec());
        Ok(b.len())
    }
    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

#[derive(Default)]
struct Seen {
    events: Vec<String>,
}

struct Probe(Arc<Mutex<Seen>>);

impl Handler for Probe {
    fn ready(&mut self, ext: &Ext, project: Option<&str>) {
        self.0.lock().unwrap().events.push(format!("ready {project:?} views={}", ext.has(Feature::Views)));
        let v = ext.view("Chart").unwrap();
        let mut p = Picture::new();
        p.rect(0.0, 0.0, 10.0, 10.0, rhun_ext::Color::Accent);
        ext.draw(v, &p).unwrap();
        let err = ext.unview(99).unwrap_err();
        self.0.lock().unwrap().events.push(format!("view {v} unview99 {}", err.code));
    }
    fn view(&mut self, _ext: &Ext, view: View, ev: ViewEvent) {
        self.0.lock().unwrap().events.push(format!("{view} {ev:?}"));
    }
    fn complete(&mut self, _ext: &Ext, req: &CompleteRequest) -> CompletionList {
        let r = Range::new(Pos::new(req.at.line, 1), req.at);
        CompletionList::new(vec![CompletionItem::new(r, Kind::Keyword, "fn")])
    }
    fn signature(&mut self, _ext: &Ext, _req: &SignatureRequest) -> Option<SignatureHelp> {
        None
    }
    fn bye(&mut self, _ext: &Ext) {
        self.0.lock().unwrap().events.push("bye".into());
    }
}

/// Reads the extension's output a line (and a body) at a time.
struct Host {
    from_ext: BufReader<ChanReader>,
    to_ext: Sender<Vec<u8>>,
}

impl Host {
    fn line(&mut self) -> String {
        let mut s = String::new();
        self.from_ext.read_line(&mut s).unwrap();
        s.trim_end_matches('\n').to_string()
    }
    fn body(&mut self, n: usize) -> String {
        let mut b = vec![0; n];
        self.from_ext.read_exact(&mut b).unwrap();
        String::from_utf8(b).unwrap()
    }
    fn send(&self, s: &str) {
        self.to_ext.send(s.as_bytes().to_vec()).unwrap();
    }
}

#[test]
fn a_session_from_hello_to_bye() {
    let (to_ext, ext_in) = channel();
    let (ext_out, from_ext) = channel();
    let seen = Arc::new(Mutex::new(Seen::default()));
    let probe = Probe(seen.clone());
    let done = thread::spawn(move || {
        run_on(
            ChanReader { rx: ext_in, buf: Vec::new() },
            ChanWriter(ext_out),
            &[Feature::Views, Feature::Complete],
            probe,
        )
    });
    let mut host = Host { from_ext: BufReader::new(ChanReader { rx: from_ext, buf: Vec::new() }), to_ext };

    assert_eq!(host.line(), "hello 1 views complete");
    host.send("0 features views\n0 ready /tmp/my project\n");
    assert_eq!(host.line(), "1 view Chart");
    host.send("ok 1 7\n");
    assert_eq!(host.line(), "2 draw 7 22");
    assert_eq!(host.body(22), "rect 0 0 10 10 accent\n");
    host.send("ok 2\n");
    assert_eq!(host.line(), "3 unview 99");
    host.send("error 3 noview no such view\n");

    // a host request in the middle of events, then one the crate does not know
    host.send("0 view 7 click 10.5 -3\n5 complete 4 2 3 3 typed -\n6 frobnicate 1\n");
    let head = host.line();
    assert_eq!(head, "ok 5 32");
    assert_eq!(host.body(32), "1 0\n3 1 3 3 keyword 2 2 0 0\nfnfn");
    assert!(host.line().starts_with("error 6 unknown"));

    // a signature request answered with nothing hides the help
    host.send("7 signature 4 2 3 3 invoked -\n");
    assert_eq!(host.line(), "ok 7 0");

    host.send("0 view 7 metrics small 6 15 normal 7.5 18 large 11 26 mono 7.5 19\n0 bye\n");
    done.join().unwrap().unwrap();
    let ev = &seen.lock().unwrap().events;
    assert_eq!(ev[0], "ready Some(\"/tmp/my project\") views=true");
    assert_eq!(ev[1], "view 7 unview99 noview");
    assert_eq!(ev[2], "7 Click { x: 10.5, y: -3.0 }");
    assert!(ev[3].starts_with("7 Metrics(Metrics { small: TextMetric { advance: 6.0, line_height: 15.0 }, normal: TextMetric { advance: 7.5"));
    assert_eq!(ev[4], "bye");
}

#[test]
fn rhun_going_away_fails_waiting_requests() {
    let (to_ext, ext_in) = channel::<Vec<u8>>();
    let (ext_out, from_ext) = channel();
    struct Waits(Arc<Mutex<Option<String>>>);
    impl Handler for Waits {
        fn ready(&mut self, ext: &Ext, _p: Option<&str>) {
            let r = ext.toast("hi");
            *self.0.lock().unwrap() = Some(format!("{:?}", r.map_err(|e| e.code)));
        }
    }
    let got = Arc::new(Mutex::new(None));
    let w = Waits(got.clone());
    let done = thread::spawn(move || run_on(ChanReader { rx: ext_in, buf: Vec::new() }, ChanWriter(ext_out), &[], w));
    let mut host = Host { from_ext: BufReader::new(ChanReader { rx: from_ext, buf: Vec::new() }), to_ext };
    assert_eq!(host.line(), "hello 1");
    host.send("0 ready -\n");
    assert_eq!(host.line(), "1 toast 2");
    thread::sleep(Duration::from_millis(50));
    drop(host); // rhun's end of the pipe closes
    done.join().unwrap().unwrap();
    assert_eq!(got.lock().unwrap().as_deref(), Some("Err(\"closed\")"));
}

#[test]
fn a_request_after_rhun_went_away_fails_at_once() {
    let (to_ext, ext_in) = channel::<Vec<u8>>();
    let (ext_out, from_ext) = channel();
    struct Late(Arc<Mutex<Option<String>>>);
    impl Handler for Late {
        fn ready(&mut self, ext: &Ext, _p: Option<&str>) {
            thread::sleep(Duration::from_millis(200)); // rhun closes meanwhile
            *self.0.lock().unwrap() = Some(format!("{:?}", ext.toast("hi").map_err(|e| e.code)));
        }
    }
    let got = Arc::new(Mutex::new(None));
    let l = Late(got.clone());
    let done = thread::spawn(move || run_on(ChanReader { rx: ext_in, buf: Vec::new() }, ChanWriter(ext_out), &[], l));
    let mut host = Host { from_ext: BufReader::new(ChanReader { rx: from_ext, buf: Vec::new() }), to_ext };
    assert_eq!(host.line(), "hello 1");
    host.send("0 ready -\n");
    drop(host);
    done.join().unwrap().unwrap();
    assert_eq!(got.lock().unwrap().as_deref(), Some("Err(\"closed\")"));
}

#[test]
fn language_requests_and_the_action_event() {
    use rhun_ext::{
        ActionsRequest, CodeAction, FormatRequest, Location, PosRequest, RenameRequest, Symbol, SymbolKind, Target,
        TextEdit, WorkspaceEdit,
    };
    struct Lang(Arc<Mutex<Vec<String>>>);
    impl Handler for Lang {
        fn definition(&mut self, _ext: &Ext, req: &PosRequest) -> Vec<Location> {
            vec![Location::new(Target::Doc(req.doc), Range::new(req.at, req.at)).text("x")]
        }
        fn symbols(&mut self, _ext: &Ext, _req: &PosRequest) -> Vec<Symbol> {
            vec![Symbol::new(Range::default(), SymbolKind::Function, 0, "main")]
        }
        fn actions(&mut self, _ext: &Ext, _req: &ActionsRequest) -> Vec<CodeAction> {
            vec![CodeAction::new("source", "Go").data(b"a b\nc")]
        }
        fn action(&mut self, ext: &Ext, data: &[u8]) {
            self.0.lock().unwrap().push(String::from_utf8_lossy(data).into_owned());
            let edit = WorkspaceEdit::new().file("/p/a.rs", None, vec![TextEdit::new(Range::default(), "x")]);
            let res = ext.apply(&edit).map_err(|e| e.code);
            self.0.lock().unwrap().push(format!("{res:?}"));
        }
        fn rename(&mut self, _ext: &Ext, req: &RenameRequest) -> Result<WorkspaceEdit, String> {
            if req.name == "bad" {
                return Err("not a name\nhere".into());
            }
            Ok(WorkspaceEdit::new().doc(req.doc, req.version, vec![TextEdit::new(Range::new(req.at, req.at), &req.name)]))
        }
        fn format(&mut self, _ext: &Ext, req: &FormatRequest) -> WorkspaceEdit {
            if !req.spaces {
                return WorkspaceEdit::new();
            }
            WorkspaceEdit::new().doc(req.doc, req.version, vec![TextEdit::new(Range::default(), "")])
        }
    }
    let (to_ext, ext_in) = channel();
    let (ext_out, from_ext) = channel();
    let seen = Arc::new(Mutex::new(Vec::new()));
    let lang = Lang(seen.clone());
    let feats = [Feature::Definition, Feature::Symbols, Feature::Actions, Feature::Rename, Feature::Format];
    let done =
        thread::spawn(move || run_on(ChanReader { rx: ext_in, buf: Vec::new() }, ChanWriter(ext_out), &feats, lang));
    fn answer(host: &mut Host, id: u32, body: &str) {
        assert_eq!(host.line(), format!("ok {id} {}", body.len()));
        assert_eq!(host.body(body.len()), body);
    }
    let mut host = Host { from_ext: BufReader::new(ChanReader { rx: from_ext, buf: Vec::new() }), to_ext };
    assert_eq!(host.line(), "hello 1 definition symbols actions rename format");
    host.send("0 features definition symbols actions rename format\n0 ready -\n");

    host.send("1 definition 4 9 2 3 invoked -\n");
    answer(&mut host, 1, "1\n2 3 2 3 2 1\n#4x");
    host.send("2 references 4 9 2 3 invoked -\n");
    answer(&mut host, 2, "0\n");
    host.send("3 symbols 4 9 2 3 invoked -\n");
    answer(&mut host, 3, "1\n0 0 0 0 function 0 4 0\nmain");
    host.send("4 actions 4 9 1 1 1 1\n");
    answer(&mut host, 4, "1\nsource 2 0 5\nGoa b\nc");
    host.send("0 action 5\na b\nc");
    let edit = "1\n7 - 1\n/p/a.rs0 0 0 0 1\nx";
    assert_eq!(host.line(), format!("1 apply {}", edit.len()));
    assert_eq!(host.body(edit.len()), edit);
    host.send("error 1 stale a document changed\n");
    host.send("5 rename 4 9 2 3 invoked y\n");
    answer(&mut host, 5, "1\n2 9 1\n#42 3 2 3 1\ny");
    host.send("6 rename 4 9 2 3 invoked bad\n");
    assert_eq!(host.line(), "error 6 refused not a name here");
    host.send("7 format 4 9 4 no\n");
    answer(&mut host, 7, "0\n");
    host.send("8 rename 4 9 2 3 invoked\n");
    assert!(host.line().starts_with("error 8 unknown"));
    host.send("0 bye\n");
    done.join().unwrap().unwrap();
    assert_eq!(*seen.lock().unwrap(), vec!["a b\nc".to_string(), "Err(\"stale\")".to_string()]);
}
