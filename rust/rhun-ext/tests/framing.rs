//! The crate against a scripted rhun: what it writes, what it reads, in order.

use std::io::{self, BufRead, BufReader, Read, Write};
use std::sync::mpsc::{channel, Receiver, Sender};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::Duration;

use rhun_ext::{
    run_on, CompleteRequest, CompletionItem, CompletionList, Ext, Feature, Handler, Kind, Picture,
    Pos, Range, SignatureHelp, SignatureRequest, View, ViewEvent,
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
        self.0.lock().unwrap().events.push(format!(
            "ready {project:?} views={}",
            ext.has(Feature::Views)
        ));
        let v = ext.view("Chart").unwrap();
        let mut p = Picture::new();
        p.rect(0.0, 0.0, 10.0, 10.0, rhun_ext::Color::Accent);
        ext.draw(v, &p).unwrap();
        let err = ext.unview(99).unwrap_err();
        self.0
            .lock()
            .unwrap()
            .events
            .push(format!("view {v} unview99 {}", err.code));
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
fn runtime_command_removal_requires_negotiation_and_preserves_errors() {
    struct Commands(Sender<Vec<String>>);
    impl Handler for Commands {
        fn ready(&mut self, ext: &Ext, _project: Option<&str>) {
            let mut results = Vec::new();
            results.push(
                ext.unregister("old")
                    .map(|_| "ok".to_string())
                    .unwrap_or_else(|e| e.code),
            );
            if ext.has(Feature::Commands) {
                results.push(ext.unregister("x\n2 unregister other").unwrap_err().code);
                results.push(ext.unregister("manifest").unwrap_err().code);
            }
            self.0.send(results).unwrap();
        }
    }
    for agreed in [false, true] {
        let (to_ext, ext_in) = channel();
        let (ext_out, from_ext) = channel();
        let (seen, results) = channel();
        let done = thread::spawn(move || {
            run_on(
                ChanReader {
                    rx: ext_in,
                    buf: Vec::new(),
                },
                ChanWriter(ext_out),
                &[Feature::Commands],
                Commands(seen),
            )
        });
        let mut host = Host {
            from_ext: BufReader::new(ChanReader {
                rx: from_ext,
                buf: Vec::new(),
            }),
            to_ext,
        };
        assert_eq!(host.line(), "hello 1 commands");
        if agreed {
            host.send("0 features commands\n0 ready -\n");
            assert_eq!(host.line(), "1 unregister old");
            host.send("ok 1\n");
            assert_eq!(host.line(), "2 unregister manifest");
            host.send("error 2 refused not a runtime command\n");
            assert_eq!(
                results.recv_timeout(Duration::from_secs(2)).unwrap(),
                ["ok", "syntax", "refused"]
            );
        } else {
            host.send("0 ready -\n");
            assert_eq!(
                results.recv_timeout(Duration::from_secs(2)).unwrap(),
                ["unknown"]
            );
        }
        host.send("0 bye\n");
        done.join().unwrap().unwrap();
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
            ChanReader {
                rx: ext_in,
                buf: Vec::new(),
            },
            ChanWriter(ext_out),
            &[Feature::Views, Feature::Complete],
            probe,
        )
    });
    let mut host = Host {
        from_ext: BufReader::new(ChanReader {
            rx: from_ext,
            buf: Vec::new(),
        }),
        to_ext,
    };

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
    let done = thread::spawn(move || {
        run_on(
            ChanReader {
                rx: ext_in,
                buf: Vec::new(),
            },
            ChanWriter(ext_out),
            &[],
            w,
        )
    });
    let mut host = Host {
        from_ext: BufReader::new(ChanReader {
            rx: from_ext,
            buf: Vec::new(),
        }),
        to_ext,
    };
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
    let done = thread::spawn(move || {
        run_on(
            ChanReader {
                rx: ext_in,
                buf: Vec::new(),
            },
            ChanWriter(ext_out),
            &[],
            l,
        )
    });
    let mut host = Host {
        from_ext: BufReader::new(ChanReader {
            rx: from_ext,
            buf: Vec::new(),
        }),
        to_ext,
    };
    assert_eq!(host.line(), "hello 1");
    host.send("0 ready -\n");
    drop(host);
    done.join().unwrap().unwrap();
    assert_eq!(got.lock().unwrap().as_deref(), Some("Err(\"closed\")"));
}

#[test]
fn language_requests_and_the_action_event() {
    use rhun_ext::{
        ActionsRequest, CodeAction, FormatRequest, Location, PosRequest, RenameRequest, Symbol,
        SymbolKind, Target, TextEdit, WorkspaceEdit,
    };
    struct Lang(Arc<Mutex<Vec<String>>>);
    impl Handler for Lang {
        fn definition(&mut self, _ext: &Ext, req: &PosRequest) -> Vec<Location> {
            vec![Location::new(Target::Doc(req.doc), Range::new(req.at, req.at)).text("x")]
        }
        fn symbols(&mut self, _ext: &Ext, _req: &PosRequest) -> Vec<Symbol> {
            vec![Symbol::new(
                Range::default(),
                SymbolKind::Function,
                0,
                "main",
            )]
        }
        fn actions(&mut self, _ext: &Ext, _req: &ActionsRequest) -> Vec<CodeAction> {
            vec![CodeAction::new("source", "Go").data(b"a b\nc")]
        }
        fn action(&mut self, ext: &Ext, data: &[u8]) {
            self.0
                .lock()
                .unwrap()
                .push(String::from_utf8_lossy(data).into_owned());
            let edit = WorkspaceEdit::new().file(
                "/p/a.rs",
                None,
                vec![TextEdit::new(Range::default(), "x")],
            );
            let res = ext.apply(&edit).map_err(|e| e.code);
            self.0.lock().unwrap().push(format!("{res:?}"));
        }
        fn rename(&mut self, _ext: &Ext, req: &RenameRequest) -> Result<WorkspaceEdit, String> {
            if req.name == "bad" {
                return Err("not a name\nhere".into());
            }
            Ok(WorkspaceEdit::new().doc(
                req.doc,
                req.version,
                vec![TextEdit::new(Range::new(req.at, req.at), &req.name)],
            ))
        }
        fn format(&mut self, _ext: &Ext, req: &FormatRequest) -> WorkspaceEdit {
            if !req.spaces {
                return WorkspaceEdit::new();
            }
            WorkspaceEdit::new().doc(
                req.doc,
                req.version,
                vec![TextEdit::new(Range::default(), "")],
            )
        }
    }
    let (to_ext, ext_in) = channel();
    let (ext_out, from_ext) = channel();
    let seen = Arc::new(Mutex::new(Vec::new()));
    let lang = Lang(seen.clone());
    let feats = [
        Feature::Definition,
        Feature::Symbols,
        Feature::Actions,
        Feature::Rename,
        Feature::Format,
    ];
    let done = thread::spawn(move || {
        run_on(
            ChanReader {
                rx: ext_in,
                buf: Vec::new(),
            },
            ChanWriter(ext_out),
            &feats,
            lang,
        )
    });
    fn answer(host: &mut Host, id: u32, body: &str) {
        assert_eq!(host.line(), format!("ok {id} {}", body.len()));
        assert_eq!(host.body(body.len()), body);
    }
    let mut host = Host {
        from_ext: BufReader::new(ChanReader {
            rx: from_ext,
            buf: Vec::new(),
        }),
        to_ext,
    };
    assert_eq!(
        host.line(),
        "hello 1 definition symbols actions rename format"
    );
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
    assert_eq!(
        *seen.lock().unwrap(),
        vec!["a b\nc".to_string(), "Err(\"stale\")".to_string()]
    );
}

#[test]
fn an_action_length_rhun_never_sends_ends_the_session() {
    let (to_ext, ext_in) = channel::<Vec<u8>>();
    let (ext_out, from_ext) = channel();
    struct Quiet;
    impl Handler for Quiet {}
    let done = thread::spawn(move || {
        run_on(
            ChanReader {
                rx: ext_in,
                buf: Vec::new(),
            },
            ChanWriter(ext_out),
            &[],
            Quiet,
        )
    });
    let mut host = Host {
        from_ext: BufReader::new(ChanReader {
            rx: from_ext,
            buf: Vec::new(),
        }),
        to_ext,
    };
    assert_eq!(host.line(), "hello 1");
    host.send("0 action 18446744073709551615\n");
    // the session ends without allocating, while rhun's end is still open
    done.join().unwrap().unwrap();
}

#[test]
fn source_actions_validate_identity_and_credit_intermediate_edits() {
    use rhun_ext::{SaveActionsRequest, WorkspaceEdit};
    let fields = |kind: &str| vec!["7".into(), "9".into(), kind.into()];
    assert!(SaveActionsRequest::parse(24, &fields("source.fixAll")).is_some());
    for kind in ["quickfix", "source.", "source..x", "source.x.", "source.é"] {
        assert!(SaveActionsRequest::parse(24, &fields(kind)).is_none());
    }
    assert!(SaveActionsRequest::parse(2147483648, &fields("source")).is_none());
    assert!(SaveActionsRequest::parse(24, &["0".into(), "9".into(), "source".into()]).is_none());
    struct Save;
    impl Handler for Save {
        fn save_actions(
            &mut self,
            ext: &Ext,
            req: &SaveActionsRequest,
        ) -> Result<WorkspaceEdit, String> {
            assert_eq!(
                (req.id, req.doc, req.version, req.kind.as_str()),
                (24, 7, 9, "source.fixAll")
            );
            assert_eq!(
                ext.save_apply(0, 7, 9, &WorkspaceEdit::default())
                    .unwrap_err()
                    .code,
                "syntax"
            );
            assert_eq!(
                ext.save_apply(24, 7, 9, &WorkspaceEdit::default()).unwrap(),
                10
            );
            Ok(WorkspaceEdit::default())
        }
    }
    for agreed in [false, true] {
        let (to_ext, ext_in) = channel();
        let (ext_out, from_ext) = channel();
        let done = thread::spawn(move || {
            run_on(
                ChanReader {
                    rx: ext_in,
                    buf: Vec::new(),
                },
                ChanWriter(ext_out),
                &[Feature::SaveActions],
                Save,
            )
        });
        let mut host = Host {
            from_ext: BufReader::new(ChanReader {
                rx: from_ext,
                buf: Vec::new(),
            }),
            to_ext,
        };
        assert_eq!(host.line(), "hello 1 save-actions");
        if agreed {
            host.send("0 features save-actions\n");
        }
        host.send("24 save-actions 7 9 source.fixAll\n");
        if agreed {
            assert_eq!(host.line(), "1 save-apply 24 7 9 2");
            assert_eq!(host.body(2), "0\n");
            host.send("ok 1 10\n");
            assert_eq!(host.line(), "ok 24 2");
            assert_eq!(host.body(2), "0\n");
            host.send("25 save-actions 7 9 source.\n");
            assert_eq!(host.line(), "error 25 unknown no such request");
        } else {
            assert_eq!(host.line(), "error 24 unknown no such request");
        }
        host.send("0 bye\n");
        done.join().unwrap().unwrap();
    }
}

#[test]
fn prepared_snippet_requests_frame_empty_text_and_validate_reply() {
    use rhun_ext::PreparedSnippet;
    struct Snippet;
    impl Handler for Snippet {
        fn ready(&mut self, ext: &Ext, _project: Option<&str>) {
            let s = PreparedSnippet::default();
            let range = Range::new(Pos::new(1, 1), Pos::new(1, 1));
            assert_eq!(
                ext.insert_snippet(0, 9, range, &s).unwrap_err().code,
                "syntax"
            );
            assert_eq!(ext.insert_snippet(7, 9, range, &s).unwrap(), 10);
            for _ in 0..4 {
                assert_eq!(
                    ext.insert_snippet(7, 9, range, &s).unwrap_err().code,
                    "syntax"
                );
            }
            let mut program = rhun_ext::SnippetProgram {
                initial_variants: vec![],
                transforms: vec![],
                snippet: PreparedSnippet {
                    text: String::new(),
                    fields: vec![rhun_ext::Field {
                        group: 1,
                        ..rhun_ext::Field::default()
                    }],
                },
                choices: vec![rhun_ext::SnippetChoice {
                    group: 2,
                    options: vec![String::new(), "x".into()],
                }],
            };
            assert_eq!(
                ext.insert_snippet_program(7, 10, range, &program)
                    .unwrap_err()
                    .code,
                "syntax"
            );
            program.choices[0].group = 1;
            assert_eq!(
                ext.insert_snippet_program(7, 10, range, &program).unwrap(),
                11
            );
        }
    }
    let (to_ext, ext_in) = channel();
    let (ext_out, from_ext) = channel();
    let done = thread::spawn(move || {
        run_on(
            ChanReader {
                rx: ext_in,
                buf: Vec::new(),
            },
            ChanWriter(ext_out),
            &[Feature::Snippets],
            Snippet,
        )
    });
    let mut host = Host {
        from_ext: BufReader::new(ChanReader {
            rx: from_ext,
            buf: Vec::new(),
        }),
        to_ext,
    };
    assert_eq!(host.line(), "hello 1 snippets");
    host.send("0 features snippets\n0 ready -\n");
    let body = "0 0 0 0 snippet 0 0 0 0\n";
    for (i, reply) in ["10", "8", "+10", "9223372036854775808", "10 extra"]
        .iter()
        .enumerate()
    {
        assert_eq!(
            host.line(),
            format!("{} snippet 7 9 1 1 1 1 {}", i + 1, body.len())
        );
        assert_eq!(host.body(body.len()), body);
        host.send(&format!("ok {} {}\n", i + 1, reply));
    }
    let program = "0 0 0 0 snippet 0 0 0 34\nfield 1 0 0 0 0\nchoice 1 2 5\n0\n1\nx";
    assert_eq!(
        host.line(),
        format!("6 snippet 7 10 1 1 1 1 {}", program.len())
    );
    assert_eq!(host.body(program.len()), program);
    host.send("ok 6 11\n");
    host.send("0 bye\n");
    done.join().unwrap().unwrap();
}

#[test]
fn snippet_snapshot_body_framing_guards_and_transport_cap() {
    struct Snapshot(Sender<()>);
    impl Handler for Snapshot {
        fn ready(&mut self, ext: &Ext, _: Option<&str>) {
            let range = Range::new(Pos::new(1, 3), Pos::new(1, 3));
            assert_eq!(
                ext.snippet_snapshot(0, 9, range).unwrap_err().code,
                "syntax"
            );
            let s = ext.snippet_snapshot(7, 9, range).unwrap();
            assert_eq!(s.current_line, "  😀");
            assert_eq!(s.indent, "  ");
            assert_eq!(s.path, "/p/a");
            assert_eq!(s.version, 9);
            assert!(ext.snippet_snapshot(7, 9, range).is_err());
            self.0.send(()).unwrap();
        }
    }
    for len in ["4194305", "+0", "-1", "wat", "9223372036854775808"] {
        let (to_ext, ext_in) = channel();
        let (ext_out, from_ext) = channel();
        let (tx, rx) = channel();
        let done = thread::spawn(move || {
            run_on(
                ChanReader {
                    rx: ext_in,
                    buf: Vec::new(),
                },
                ChanWriter(ext_out),
                &[Feature::Snippets],
                Snapshot(tx),
            )
        });
        let mut host = Host {
            from_ext: BufReader::new(ChanReader {
                rx: from_ext,
                buf: Vec::new(),
            }),
            to_ext,
        };
        assert_eq!(host.line(), "hello 1 snippets");
        host.send("0 features snippets\n0 ready -\n");
        assert_eq!(host.line(), "1 snippet-context 7 9 1 3 1 3");
        let body = "context 1 1 3 4 1 0 0 6 4 2 2 0\n  😀/p/a/p  ";
        host.send(&format!("0 active 7\nok 1 9 {}\n{body}", body.len()));
        assert_eq!(host.line(), "2 snippet-context 7 9 1 3 1 3");
        host.send(&format!("ok 2 9 {len}\n"));
        // No body/EOF is supplied. The reader must reject its length immediately.
        rx.recv_timeout(Duration::from_secs(2)).unwrap();
        done.join().unwrap().unwrap();
    }
}

#[test]
fn compiled_transform_negotiation_and_framing() {
    struct Transform;
    impl Handler for Transform {
        fn ready(&mut self, ext: &Ext, _: Option<&str>) {
            let compiled = rhun_ext::CompiledSnippetTransform::from_programs(
                &[
                    82, 78, 82, 88, 1, 0, 0, 0, 0, 0, 16, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 0,
                    0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 80, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0,
                    0, 97, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
                    0, 0, 0,
                ],
                &[
                    82, 78, 82, 70, 1, 0, 0, 0, 0, 0, 16, 0, 1, 0, 0, 0, 1, 0, 0, 0, 57, 0, 0, 0,
                    0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0,
                    0, 0, 0, 0, 0, 0, 120,
                ],
            )
            .unwrap();
            let p = rhun_ext::SnippetProgram {
                snippet: rhun_ext::PreparedSnippet {
                    text: "a".into(),
                    fields: vec![rhun_ext::Field {
                        group: 1,
                        start: 0,
                        end: 1,
                        parent: 0,
                    }],
                },
                transforms: vec![rhun_ext::SnippetFieldTransform {
                    field: 1,
                    transform: compiled,
                }],
                ..rhun_ext::SnippetProgram::default()
            };
            let result =
                ext.insert_snippet_program(7, 9, Range::new(Pos::new(1, 1), Pos::new(1, 1)), &p);
            if ext.has(Feature::Snippets) && ext.has(Feature::SnippetTransforms) {
                assert_eq!(result.unwrap(), 10);
            } else {
                assert_eq!(result.unwrap_err().code, "unknown");
            }
            ext.toast("after").unwrap();
        }
    }
    for agreed in [
        "",
        "snippets",
        "snippet-transforms",
        "snippets snippet-transforms",
    ] {
        let (to_ext, ext_in) = channel();
        let (ext_out, from_ext) = channel();
        let done = thread::spawn(move || {
            run_on(
                ChanReader {
                    rx: ext_in,
                    buf: Vec::new(),
                },
                ChanWriter(ext_out),
                &[Feature::Snippets, Feature::SnippetTransforms],
                Transform,
            )
        });
        let mut host = Host {
            from_ext: BufReader::new(ChanReader {
                rx: from_ext,
                buf: Vec::new(),
            }),
            to_ext,
        };
        assert_eq!(host.line(), "hello 1 snippets snippet-transforms");
        host.send(&format!("0 features {agreed}\n0 ready -\n"));
        if agreed == "snippets snippet-transforms" {
            let header = host.line();
            let len: usize = header.split_whitespace().last().unwrap().parse().unwrap();
            assert_eq!(header, format!("1 snippet 7 9 1 1 1 1 {len}"));
            let body = host.body(len);
            assert!(body.contains("transforms 0 260\n"));
            assert_eq!(len, body.len());
            host.send("ok 1 10\n");
            assert_eq!(host.line(), "2 toast 5");
        } else {
            assert_eq!(host.line(), "1 toast 5");
        }
        assert_eq!(host.body(5), "after");
        host.send(if agreed == "snippets snippet-transforms" {
            "ok 2\n"
        } else {
            "ok 1\n"
        });
        host.send("0 bye\n");
        drop(host);
        assert!(done.join().unwrap().is_ok());
    }
}

#[test]
fn workspace_edit_transactions_and_the_editor_model() {
    use rhun_ext::{
        Create, Linked, LinkedRequest, Location, Rename, Reveal, Selection, Selections, Show,
        Target, TextEdit, WorkspaceEdit,
    };
    struct Model(Sender<String>);
    impl Handler for Model {
        fn ready(&mut self, ext: &Ext, _p: Option<&str>) {
            let r = |e: Result<(), rhun_ext::Error>| match e {
                Ok(()) => "ok".to_string(),
                Err(e) => e.code,
            };
            let at = Pos::new(2, 3);
            let sels = [
                Selection::new(Pos::new(1, 1), Pos::new(1, 4)),
                Selection::caret(at),
            ];
            self.0.send(r(ext.select_many(4, &sels))).unwrap();
            self.0.send(r(ext.select_many(4, &[]))).unwrap();
            self.0
                .send(r(ext.reveal(4, at, Reveal::CenterIfOutside)))
                .unwrap();
            let loc = Location::new(Target::Doc(4), Range::new(at, at));
            self.0
                .send(r(ext.show_locations("Callers", &[loc])))
                .unwrap();
            let tab = ext.diff(4, 5, "HEAD\nwork", Show::Background).unwrap();
            self.0.send(format!("diff {tab}")).unwrap();
            self.0.send(r(ext.diff_close(tab))).unwrap();
            let edit = WorkspaceEdit::new()
                .label("Move")
                .rename("/p/a.rs", "/p/b.rs", Rename::default())
                .create("/p/c.rs", Create::default())
                .file_hashed(
                    "/p/a.rs",
                    b"x",
                    vec![TextEdit::new(Range::new(at, at), "y")],
                );
            let e = ext.apply(&edit).unwrap_err();
            self.0
                .send(format!(
                    "{} {} {} {:?}",
                    e.code,
                    e.entry,
                    e.text,
                    edit.entry(e.entry)
                ))
                .unwrap();
        }
        fn selections(&mut self, _ext: &Ext, s: &Selections) {
            self.0
                .send(format!("selections {} {:?}", s.doc, s.sels))
                .unwrap();
        }
        fn linked(&mut self, _ext: &Ext, req: &LinkedRequest) -> Linked {
            let l = req.at.line;
            Linked::new(vec![
                Range::new(Pos::new(l, 2), Pos::new(l, 5)),
                Range::new(Pos::new(l + 1, 3), Pos::new(l + 1, 6)),
            ])
            .word("-")
        }
    }
    let (to_ext, ext_in) = channel();
    let (ext_out, from_ext) = channel();
    let (seen, got) = channel();
    let feats = [
        Feature::WorkspaceEdits,
        Feature::FileOperations,
        Feature::Selections,
        Feature::Reveal,
        Feature::Diff,
        Feature::LinkedEditing,
    ];
    let done = thread::spawn(move || {
        run_on(
            ChanReader {
                rx: ext_in,
                buf: Vec::new(),
            },
            ChanWriter(ext_out),
            &feats,
            Model(seen),
        )
    });
    let mut host = Host {
        from_ext: BufReader::new(ChanReader {
            rx: from_ext,
            buf: Vec::new(),
        }),
        to_ext,
    };
    let next = |got: &Receiver<String>| got.recv_timeout(Duration::from_secs(2)).unwrap();
    assert_eq!(
        host.line(),
        "hello 1 workspace-edits file-operations selections reveal diff linked-editing"
    );
    host.send("0 features workspace-edits file-operations selections reveal diff linked-editing\n0 ready -\n");
    assert_eq!(host.line(), "1 select-many 4 2 16");
    assert_eq!(host.body(16), "1 1 1 4\n2 3 2 3\n");
    host.send("ok 1\n");
    assert_eq!(next(&got), "ok");
    assert_eq!(next(&got), "toobig");
    assert_eq!(host.line(), "2 reveal 4 2 3 center-if-outside");
    host.send("ok 2\n");
    assert_eq!(next(&got), "ok");
    let body = "Callers\n1\n2 3 2 3 2 0\n#4";
    assert_eq!(host.line(), format!("3 show-locations {}", body.len()));
    assert_eq!(host.body(body.len()), body);
    host.send("error 3 busy the palette is open\n");
    assert_eq!(next(&got), "busy");
    assert_eq!(host.line(), "4 diff 4 5 background 9");
    assert_eq!(host.body(9), "HEAD work");
    host.send("ok 4 77\n");
    assert_eq!(next(&got), "diff 77");
    assert_eq!(host.line(), "5 diff-close 77");
    host.send("ok 5\n");
    assert_eq!(next(&got), "ok");
    let edit = format!(
        "3\ncreate 7 -\n/p/c.rs7 fnv1a64:{:016x} 1\n/p/a.rs2 3 2 3 1\nyrename 7 7 -\n/p/a.rs/p/b.rslabel 4\nMove",
        rhun_ext::fnv1a64(b"x")
    );
    assert_eq!(host.line(), format!("6 apply {}", edit.len()));
    assert_eq!(host.body(edit.len()), edit);
    host.send("error 6 stale 2 /p/a.rs changed on disk\n");
    assert_eq!(next(&got), "stale 2 /p/a.rs changed on disk Some(File(0))");
    // events with and without a body, then a linked request
    host.send("0 selections 4 2 16\n3 1 3 5\n1 2 1 2\n9 linked 4 7 3 2\n");
    assert_eq!(
        next(&got),
        "selections 4 [Selection { anchor: Pos { line: 3, col: 1 }, cursor: Pos { line: 3, col: 5 } }, Selection { anchor: Pos { line: 1, col: 2 }, cursor: Pos { line: 1, col: 2 } }]"
    );
    let linked = "2\n3 2 3 5\n4 3 4 6\nword 1\n-";
    assert_eq!(host.line(), format!("ok 9 {}", linked.len()));
    assert_eq!(host.body(linked.len()), linked);
    host.send("0 bye\n");
    done.join().unwrap().unwrap();
}

#[test]
fn without_the_features_requests_fail_and_edits_stay_v1() {
    use rhun_ext::{Create, Selection, TextEdit, WorkspaceEdit};
    struct Old(Sender<String>);
    impl Handler for Old {
        fn ready(&mut self, ext: &Ext, _p: Option<&str>) {
            let code = |e: rhun_ext::Error| e.code;
            self.0
                .send(code(
                    ext.select_many(1, &[Selection::default()]).unwrap_err(),
                ))
                .unwrap();
            let ops = WorkspaceEdit::new().create("/p/x", Create::default());
            self.0.send(ext.apply(&ops).unwrap_err().code).unwrap();
            let e = WorkspaceEdit::new().label("L").file_hashed(
                "/p/a",
                b"x",
                vec![TextEdit::default()],
            );
            let err = ext.apply(&e).unwrap_err();
            self.0
                .send(format!("{} {} {}", err.code, err.entry, err.text))
                .unwrap();
        }
    }
    let (to_ext, ext_in) = channel();
    let (ext_out, from_ext) = channel();
    let (seen, got) = channel();
    let done = thread::spawn(move || {
        run_on(
            ChanReader {
                rx: ext_in,
                buf: Vec::new(),
            },
            ChanWriter(ext_out),
            &[],
            Old(seen),
        )
    });
    let mut host = Host {
        from_ext: BufReader::new(ChanReader {
            rx: from_ext,
            buf: Vec::new(),
        }),
        to_ext,
    };
    assert_eq!(host.line(), "hello 1");
    host.send("0 ready -\n");
    let next = |got: &Receiver<String>| got.recv_timeout(Duration::from_secs(2)).unwrap();
    assert_eq!(next(&got), "unknown");
    assert_eq!(next(&got), "unknown");
    let body = "1\n4 - 1\n/p/a0 0 0 0 0\n";
    assert_eq!(host.line(), format!("1 apply {}", body.len()));
    assert_eq!(host.body(body.len()), body);
    host.send("error 1 stale 2 documents changed\n");
    assert_eq!(next(&got), "stale 0 2 documents changed");
    // a selections event the extension did not agree is not delivered (and its body is read)
    host.send("0 selections 4 1 8\n1 1 1 1\n0 bye\n");
    done.join().unwrap().unwrap();
}

#[test]
fn diff_and_reveal_take_uris() {
    use rhun_ext::{Reveal, Show};
    struct Uris(Sender<String>);
    impl Handler for Uris {
        fn ready(&mut self, ext: &Ext, _p: Option<&str>) {
            let tab = ext.diff_uris("res-x:/a.txt", "file:///p/a.rs", "t", Show::Front);
            self.0
                .send(format!("diff {:?}", tab.map_err(|e| e.code)))
                .unwrap();
            let bad = ext.diff_uris("res-x:/a b", "5", "t", Show::Front);
            self.0
                .send(format!("{:?}", bad.map_err(|e| e.code)))
                .unwrap();
            let r = ext.reveal_uri("res-x:/a.txt", Pos::new(2, 3), Reveal::Top);
            self.0.send(format!("{:?}", r.map_err(|e| e.code))).unwrap();
        }
    }
    let (to_ext, ext_in) = channel();
    let (ext_out, from_ext) = channel();
    let (seen, got) = channel();
    let feats = [Feature::Reveal, Feature::Diff];
    let done = thread::spawn(move || {
        run_on(
            ChanReader {
                rx: ext_in,
                buf: Vec::new(),
            },
            ChanWriter(ext_out),
            &feats,
            Uris(seen),
        )
    });
    let mut host = Host {
        from_ext: BufReader::new(ChanReader {
            rx: from_ext,
            buf: Vec::new(),
        }),
        to_ext,
    };
    let next = |got: &Receiver<String>| got.recv_timeout(Duration::from_secs(2)).unwrap();
    assert_eq!(host.line(), "hello 1 reveal diff");
    host.send("0 features reveal diff\n0 ready -\n");
    assert_eq!(host.line(), "1 diff res-x:/a.txt file:///p/a.rs show 1");
    assert_eq!(host.body(1), "t");
    host.send("ok 1 78\n");
    assert_eq!(next(&got), "diff Ok(78)");
    assert_eq!(next(&got), "Err(\"syntax\")");
    assert_eq!(host.line(), "2 reveal res-x:/a.txt 2 3 top");
    host.send("ok 2\n");
    assert_eq!(next(&got), "Ok(())");
    host.send("0 bye\n");
    drop(host);
    assert!(done.join().unwrap().is_ok());
}
