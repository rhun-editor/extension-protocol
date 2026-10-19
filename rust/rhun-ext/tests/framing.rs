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

// ---- protocol 1.1 language features (go/rhunext/query_test.go, the same bytes)

/// A session with a handler: rhun's end, and the extension's thread.
fn session<H: Handler + 'static>(
    features: Vec<Feature>,
    handler: H,
) -> (Host, thread::JoinHandle<io::Result<()>>) {
    let (to_ext, ext_in) = channel();
    let (ext_out, from_ext) = channel();
    let done = thread::spawn(move || {
        run_on(
            ChanReader {
                rx: ext_in,
                buf: Vec::new(),
            },
            ChanWriter(ext_out),
            &features,
            handler,
        )
    });
    let host = Host {
        from_ext: BufReader::new(ChanReader {
            rx: from_ext,
            buf: Vec::new(),
        }),
        to_ext,
    };
    (host, done)
}

fn answer(host: &mut Host, id: u32, body: &str) {
    assert_eq!(host.line(), format!("ok {id} {}", body.len()));
    assert_eq!(host.body(body.len()), body);
}

fn rg(l1: u32, c1: u32, l2: u32, c2: u32) -> Range {
    Range::new(Pos::new(l1, c1), Pos::new(l2, c2))
}

struct QueryProbe(Sender<String>);

impl QueryProbe {
    fn locations(
        &self,
        req: &rhun_ext::PosRequest,
        out: &rhun_ext::Stream,
    ) -> Vec<rhun_ext::Location> {
        use rhun_ext::{Location, Target};
        if out.progress(Some(40), "Indexing\nall").is_err() {
            return Vec::new();
        }
        let part = [Location::new(Target::Doc(req.doc), rg(1, 1, 1, 2)).text("a")];
        if let Err(e) = out.part_locations(&part) {
            self.0
                .send(format!("{} {}", e.code, out.is_cancelled()))
                .unwrap();
            return Vec::new();
        }
        vec![Location::new(Target::Doc(req.doc), rg(2, 1, 2, 2)).text("b")]
    }
}

impl Handler for QueryProbe {
    fn references_stream(
        &mut self,
        _ext: &Ext,
        req: &rhun_ext::PosRequest,
        out: &rhun_ext::Stream,
    ) -> Vec<rhun_ext::Location> {
        self.locations(req, out)
    }
    fn definition_stream(
        &mut self,
        _ext: &Ext,
        req: &rhun_ext::PosRequest,
        out: &rhun_ext::Stream,
    ) -> Vec<rhun_ext::Location> {
        self.locations(req, out)
    }
    fn complete(&mut self, _ext: &Ext, _req: &CompleteRequest) -> CompletionList {
        let mut it = CompletionItem::new(rg(2, 5, 2, 7), Kind::Text, "print")
            .resolve(b"r1", true)
            .replace(rg(2, 5, 2, 9))
            .commit("..(\x01")
            .filter("prt")
            .suffix("(s)");
        it.preselect = true;
        it.deprecated = true;
        it.adjust = true;
        it.after_signature = true;
        CompletionList::new(vec![it])
    }
    fn complete_resolve(
        &mut self,
        _ext: &Ext,
        req: &rhun_ext::ResolveRequest,
    ) -> rhun_ext::Resolution {
        rhun_ext::Resolution {
            doc: format!("Docs {}", String::from_utf8_lossy(&req.data)),
            detail: Some("fn print()".into()),
            edits: vec![rhun_ext::TextEdit::new(rg(1, 1, 1, 1), "use x;\n")],
            ..Default::default()
        }
    }
    fn signatures(
        &mut self,
        _ext: &Ext,
        _req: &SignatureRequest,
    ) -> Option<rhun_ext::SignatureList> {
        let sig = |label: &str, active, doc: &str| rhun_ext::SignatureInfo {
            label: label.into(),
            active: Some(active),
            doc: doc.into(),
        };
        Some(rhun_ext::SignatureList {
            signatures: vec![
                sig("f(a)", (2, 3), ""),
                sig("f(a, b)", (5, 6), "two"),
                sig("f()", (9, 12), ""),
            ],
            active: 1,
        })
    }
}

#[test]
fn query_stream_parts_and_progress() {
    let (tx, cancelled) = channel();
    let (mut host, done) = session(
        vec![Feature::References, Feature::QueryStream],
        QueryProbe(tx),
    );
    assert_eq!(host.line(), "hello 1 references query-stream");
    host.send("0 features references query-stream\n0 ready -\n");
    host.send("7 references 4 2 1 1 invoked -\n");
    assert_eq!(host.line(), "1 query-progress 7 40 12");
    assert_eq!(host.body(12), "Indexing all");
    host.send("ok 1\n");
    let part = "1\n1 1 1 2 2 1\n#4a";
    assert_eq!(host.line(), format!("2 query-part 7 {}", part.len()));
    assert_eq!(host.body(part.len()), part);
    host.send("ok 2\n");
    answer(&mut host, 7, "1\n2 1 2 2 2 1\n#4b");
    // a part answered cancelled stops the handler
    host.send("8 definition 4 2 1 1 invoked -\n");
    assert_eq!(host.line(), "3 query-progress 8 40 12");
    host.body(12);
    host.send("ok 3\n");
    assert_eq!(host.line(), format!("4 query-part 8 {}", part.len()));
    host.body(part.len());
    host.send("error 4 cancelled\n");
    assert_eq!(
        cancelled.recv_timeout(Duration::from_secs(2)).unwrap(),
        "cancelled true"
    );
    answer(&mut host, 8, "0\n");
    host.send("0 bye\n");
    done.join().unwrap().unwrap();
}

#[test]
fn a_stream_without_the_feature_is_refused_locally() {
    let (tx, _cancelled) = channel();
    let (mut host, done) = session(vec![Feature::References], QueryProbe(tx));
    assert_eq!(host.line(), "hello 1 references");
    host.send("0 features references\n0 ready -\n");
    host.send("7 references 4 2 1 1 invoked -\n");
    answer(&mut host, 7, "0\n"); // progress failed (unknown): the handler gave up
    host.send("0 bye\n");
    done.join().unwrap().unwrap();
}

#[test]
fn rhuns_cancel_stops_a_stream_and_parts_come_from_any_thread() {
    use rhun_ext::{Location, PosRequest, Stream, Target};
    struct Worker(Sender<String>);
    impl Handler for Worker {
        fn references_stream(
            &mut self,
            _ext: &Ext,
            req: &PosRequest,
            out: &Stream,
        ) -> Vec<Location> {
            let first = vec![Location::new(Target::Doc(req.doc), rg(1, 1, 1, 2))];
            let s = out.clone();
            let sent = thread::spawn(move || s.part_locations(&first))
                .join()
                .unwrap();
            let again = out.part_locations(&[]).map_err(|e| e.code);
            self.0
                .send(format!(
                    "{:?} {} {:?}",
                    sent.map_err(|e| e.code),
                    out.is_cancelled(),
                    again
                ))
                .unwrap();
            Vec::new()
        }
    }
    let (tx, got) = channel();
    let (mut host, done) = session(vec![Feature::References, Feature::QueryStream], Worker(tx));
    assert_eq!(host.line(), "hello 1 references query-stream");
    host.send("0 features references query-stream\n0 ready -\n");
    host.send("9 references 4 2 1 1 invoked -\n");
    let part = "1\n1 1 1 2 2 0\n#4";
    assert_eq!(host.line(), format!("1 query-part 9 {}", part.len()));
    assert_eq!(host.body(part.len()), part);
    // rhun withdraws the request while the part waits: the next part is not sent
    host.send("0 cancel 9\nok 1\n");
    assert_eq!(
        got.recv_timeout(Duration::from_secs(2)).unwrap(),
        "Ok(()) true Err(\"cancelled\")"
    );
    answer(&mut host, 9, "0\n");
    host.send("0 bye\n");
    done.join().unwrap().unwrap();
}

#[test]
fn complete_items_records_resolve_and_signature_lists() {
    let (tx, _) = channel();
    let feats = vec![
        Feature::Complete,
        Feature::CompleteItems,
        Feature::SignatureList,
    ];
    let (mut host, done) = session(feats, QueryProbe(tx));
    assert_eq!(
        host.line(),
        "hello 1 complete complete-items signature-list"
    );
    host.send("0 features complete complete-items signature-list\n0 ready -\n");
    host.send("3 complete 4 2 2 7 invoked -\n");
    let extra =
        "resolve 1 2\nr1replace 2 5 2 9 0\ncommit 2\n.(filter 3\nprtsuffix 3\n(s)preselect 0\n\
                 deprecated 0\nadjust 0\nafter signature 0\n";
    let item = format!("2 5 2 7 text 5 5 0 {}\nprintprint{extra}", extra.len());
    answer(&mut host, 3, &format!("1 0\n{item}"));
    host.send("4 complete-resolve 4 2 2\nr1");
    answer(
        &mut host,
        4,
        "detail 10\nfn print()doc 7\nDocs r1edit 1 1 1 1 7\nuse x;\n",
    );
    host.send("5 signatures 4 2 2 7 invoked -\n");
    answer(
        &mut host,
        5,
        "3 1\n2 3 4 0\nf(a)5 6 7 3\nf(a, b)two-1 -1 3 0\nf()",
    );
    host.send("0 bye\n");
    done.join().unwrap().unwrap();

    // without the features: both are unknown, and the resolve request's body is read past
    let (tx, _) = channel();
    let (mut host, done) = session(vec![Feature::Complete], QueryProbe(tx));
    assert_eq!(host.line(), "hello 1 complete");
    host.send("0 features complete\n4 complete-resolve 4 2 2\nr15 signatures 4 2 2 7 invoked -\n");
    assert_eq!(host.line(), "error 4 unknown no such request");
    assert_eq!(host.line(), "error 5 unknown no such request");
    host.send("0 bye\n");
    done.join().unwrap().unwrap();
}

#[test]
fn a_request_body_rhun_never_sends_ends_the_session() {
    for len in ["65537", "+1", "99999999999", "x"] {
        struct Quiet;
        impl Handler for Quiet {}
        let (mut host, done) = session(vec![Feature::CompleteItems], Quiet);
        assert_eq!(host.line(), "hello 1 complete-items");
        host.send(&format!(
            "0 features complete-items\n4 complete-resolve 4 2 {len}\n"
        ));
        done.join().unwrap().unwrap();
    }
}

#[test]
fn provides_needs_the_feature() {
    struct Scope(Sender<Vec<String>>);
    impl Handler for Scope {
        fn ready(&mut self, ext: &Ext, _project: Option<&str>) {
            let code = |r: Result<(), rhun_ext::Error>| {
                r.map(|_| "ok".to_string()).unwrap_or_else(|e| e.code)
            };
            let mut got = vec![code(ext.provides("definition", &["*.go"]))];
            if ext.has(Feature::ProviderScope) {
                got.push(code(ext.withdraw("references")));
                got.push(code(ext.provides_manifest("complete.triggers")));
                got.push(code(ext.provides("bad key", &["*"])));
            }
            ext.toast("after").unwrap();
            self.0.send(got).unwrap();
        }
    }
    let (tx, got) = channel();
    let (mut host, done) = session(vec![Feature::ProviderScope], Scope(tx));
    assert_eq!(host.line(), "hello 1 provider-scope");
    host.send("0 features provider-scope\n0 ready -\n");
    assert_eq!(host.line(), "1 provides definition *.go");
    host.send("ok 1\n");
    assert_eq!(host.line(), "2 provides references -");
    host.send("error 2 refused no\n");
    assert_eq!(host.line(), "3 provides complete.triggers *");
    host.send("ok 3\n");
    assert_eq!(host.line(), "4 toast 5");
    host.body(5);
    host.send("ok 4\n");
    assert_eq!(
        got.recv_timeout(Duration::from_secs(2)).unwrap(),
        ["ok", "refused", "ok", "syntax"]
    );
    host.send("0 bye\n");
    done.join().unwrap().unwrap();

    // without the feature nothing is sent
    let (tx, got) = channel();
    let (mut host, done) = session(vec![], Scope(tx));
    assert_eq!(host.line(), "hello 1");
    host.send("0 ready -\n");
    assert_eq!(host.line(), "1 toast 5");
    host.body(5);
    host.send("ok 1\n");
    assert_eq!(
        got.recv_timeout(Duration::from_secs(2)).unwrap(),
        ["unknown"]
    );
    host.send("0 bye\n");
    done.join().unwrap().unwrap();
}

// ---- navigation, prepare-rename, partial-format, code-actions (go/rhunext/actions_test.go, the
// same bytes)

struct ActionsProbe;

impl Handler for ActionsProbe {
    fn declaration(&mut self, _ext: &Ext, req: &rhun_ext::PosRequest) -> Vec<rhun_ext::Location> {
        use rhun_ext::{Location, Target};
        vec![Location::new(Target::Doc(req.doc), rg(1, 1, 1, 2)).text("d")]
    }
    fn implementation_stream(
        &mut self,
        _ext: &Ext,
        req: &rhun_ext::PosRequest,
        out: &rhun_ext::Stream,
    ) -> Vec<rhun_ext::Location> {
        use rhun_ext::{Location, Target};
        let _ = out.part_locations(&[Location::new(Target::Doc(req.doc), rg(1, 1, 1, 2))]);
        vec![Location::new(Target::Doc(req.doc), rg(2, 1, 2, 2))]
    }
    fn prepare_rename(
        &mut self,
        _ext: &Ext,
        req: &rhun_ext::PosRequest,
    ) -> Result<Option<rhun_ext::PreparedRename>, String> {
        match req.at.line {
            9 => Err("You cannot rename a keyword".into()),
            8 => Ok(None),
            _ => Ok(Some(rhun_ext::PreparedRename::new(
                rg(2, 9, 2, 14),
                "total",
            ))),
        }
    }
    fn range_format(
        &mut self,
        _ext: &Ext,
        req: &rhun_ext::RangeFormatRequest,
    ) -> rhun_ext::WorkspaceEdit {
        let mut w = rhun_ext::WorkspaceEdit::new();
        if req.trim && !req.final_newline && req.tab_width == 4 && req.spaces {
            w = w.doc(
                req.doc,
                req.version,
                vec![rhun_ext::TextEdit::new(req.range, "x")],
            );
        }
        w
    }
    fn type_format(
        &mut self,
        _ext: &Ext,
        req: &rhun_ext::TypeFormatRequest,
    ) -> rhun_ext::WorkspaceEdit {
        let mut w = rhun_ext::WorkspaceEdit::new();
        if req.ch == "newline" {
            w = w.doc(
                req.doc,
                req.version,
                vec![rhun_ext::TextEdit::new(Range::new(req.at, req.at), "  ")],
            );
        }
        w
    }
    fn code_actions(
        &mut self,
        _ext: &Ext,
        _req: &rhun_ext::CodeActionsRequest,
    ) -> Vec<rhun_ext::Action> {
        use rhun_ext::{Action, CommandRef};
        vec![
            Action::new("quickfix", "Fix")
                .preferred()
                .resolve(b"R")
                .fixes(&["d1"]),
            Action::new("refactor.extract", "Extract").command(CommandRef::new("extract", b"a b")),
            Action::new("quickfix", "Cannot").disabled("No fix here"),
            Action::new("quickfix", "Nothing"),
        ]
    }
    fn code_action_resolve(
        &mut self,
        _ext: &Ext,
        req: &rhun_ext::ActionResolveRequest,
    ) -> Result<rhun_ext::ResolvedAction, String> {
        if req.data == b"no" {
            return Err("The fix\nis gone".into());
        }
        let text = String::from_utf8_lossy(&req.data);
        let w = rhun_ext::WorkspaceEdit::new().doc(
            req.doc,
            req.version,
            vec![rhun_ext::TextEdit::new(rg(1, 1, 1, 1), &text)],
        );
        Ok(rhun_ext::ResolvedAction {
            edit: Some(w),
            command: Some(rhun_ext::CommandRef::new("fix", b"z")),
        })
    }
}

#[test]
fn navigation_rename_format_and_actions() {
    let feats = vec![
        Feature::Navigation,
        Feature::PrepareRename,
        Feature::PartialFormat,
        Feature::CodeActions,
        Feature::QueryStream,
    ];
    let (mut host, done) = session(feats, ActionsProbe);
    assert_eq!(
        host.line(),
        "hello 1 navigation prepare-rename partial-format code-actions query-stream"
    );
    host.send(
        "0 features navigation prepare-rename partial-format code-actions query-stream\n0 ready -\n",
    );
    host.send("2 declaration 4 7 1 1 invoked -\n");
    answer(&mut host, 2, "1\n1 1 1 2 2 1\n#4d");
    host.send("3 prepare-rename 4 7 2 10 invoked -\n");
    answer(&mut host, 3, "2 9 2 14 5\ntotal");
    host.send("4 prepare-rename 4 7 9 1 invoked -\n");
    assert_eq!(host.line(), "error 4 refused You cannot rename a keyword");
    host.send("5 prepare-rename 4 7 8 1 invoked -\n");
    answer(&mut host, 5, "");
    host.send("6 range-format 4 7 1 1 2 1 4 yes yes no\n");
    answer(&mut host, 6, "1\n2 7 1\n#41 1 2 1 1\nx");
    host.send("7 type-format 4 7 3 5 newline 4 yes no yes\n");
    answer(&mut host, 7, "1\n2 7 1\n#43 5 3 5 2\n  ");
    host.send("8 code-actions 4 7 1 1 1 1 invoked quickfix -\n");
    answer(
        &mut host,
        8,
        "2\nquickfix 3 0 33\nFixpreferred 0\nresolve 1\nRfixes 2\nd1quickfix 6 0 23\nCannotdisabled 11\nNo fix here",
    );
    host.send("9 code-actions 4 7 1 1 1 1 invoked - d1,d2\n");
    answer(
        &mut host,
        9,
        "3\nquickfix 3 0 33\nFixpreferred 0\nresolve 1\nRfixes 2\nd1refactor.extract 7 0 21\n\
         Extractcommand extract 3\na bquickfix 6 0 23\nCannotdisabled 11\nNo fix here",
    );
    host.send("10 code-action-resolve 4 7 1\nR");
    answer(
        &mut host,
        10,
        "21 15\n1\n2 7 1\n#41 1 1 1 1\nRcommand fix 1\nz",
    );
    host.send("11 code-action-resolve 4 7 2\nno");
    assert_eq!(host.line(), "error 11 refused The fix is gone");
    host.send("12 code-actions 4 7 1 1 1 1 auto refactor -\n");
    answer(
        &mut host,
        12,
        "1\nrefactor.extract 7 0 21\nExtractcommand extract 3\na b",
    );
    // implementation streams a part first; type definition has no answer of its own
    host.send("13 implementation 4 7 1 1 invoked -\n");
    let part = "1\n1 1 1 2 2 0\n#4";
    assert_eq!(host.line(), format!("1 query-part 13 {}", part.len()));
    assert_eq!(host.body(part.len()), part);
    host.send("ok 1\n");
    answer(&mut host, 13, "1\n2 1 2 2 2 0\n#4");
    host.send("14 type-definition 4 7 1 1 invoked -\n");
    answer(&mut host, 14, "0\n");
    // requests that do not parse are unknown
    host.send("15 range-format 4 7 1 1 2 1 4 yes yes\n");
    assert!(host.line().starts_with("error 15 unknown"));
    host.send("16 code-actions 4 7 1 1 1 1 later - -\n");
    assert!(host.line().starts_with("error 16 unknown"));
    host.send("0 bye\n");
    done.join().unwrap().unwrap();

    // without the features every request is unknown, and the resolve request's body is read past
    let (mut host, done) = session(vec![Feature::Rename, Feature::Format], ActionsProbe);
    assert_eq!(host.line(), "hello 1 rename format");
    host.send("0 features rename format\n0 ready -\n");
    for (id, req) in [
        (2, "declaration 4 7 1 1 invoked -\n"),
        (3, "type-definition 4 7 1 1 invoked -\n"),
        (4, "implementation 4 7 1 1 invoked -\n"),
        (5, "prepare-rename 4 7 2 10 invoked -\n"),
        (6, "range-format 4 7 1 1 2 1 4 yes yes no\n"),
        (7, "type-format 4 7 3 5 newline 4 yes no yes\n"),
        (8, "code-actions 4 7 1 1 1 1 invoked - -\n"),
        (9, "code-action-resolve 4 7 1\nR"),
    ] {
        host.send(&format!("{id} {req}"));
        assert_eq!(host.line(), format!("error {id} unknown no such request"));
    }
    host.send("0 bye\n");
    done.join().unwrap().unwrap();
}

// ---- hover-query, selection-ranges, hierarchy, workspace-symbols, document-links,
// file-diagnostics (go/rhunext/langfeat_test.go, the same bytes)

struct MoreProbe(Sender<String>);

fn item(name: &str, data: &str) -> rhun_ext::HierarchyItem {
    use rhun_ext::{HierarchyItem, SymbolKind, Target};
    HierarchyItem::new(
        SymbolKind::Function,
        name,
        Target::Path("/p/a.go".into()),
        rg(3, 1, 9, 2),
    )
    .detail("pkg")
    .selection(rg(3, 6, 3, 6 + name.len() as u32))
    .data(data.as_bytes())
}

fn item_rec(sel: &str, name: &str, data: &str, sites: &[&str]) -> String {
    let mut head = format!("function 3 1 9 2 {sel} 7 {} 3 {}", name.len(), data.len());
    if !sites.is_empty() {
        head += &format!(" {}", sites.len());
    }
    let mut s = format!("{head}\n/p/a.go{name}pkg{data}");
    for site in sites {
        s += site;
        s.push('\n');
    }
    s
}

impl Handler for MoreProbe {
    fn ready(&mut self, ext: &Ext, _project: Option<&str>) {
        use rhun_ext::{Diagnostic, Target};
        let ds = [Diagnostic {
            range: rg(1, 4, 1, 9),
            message: "bad\nname".into(),
            id: "e1".into(),
            ..Default::default()
        }];
        if !ext.has(Feature::FileDiagnostics) {
            let e = ext
                .file_diagnostics(&Target::Doc(1), Some(0), &ds)
                .unwrap_err();
            let r = ext.reset_diagnostics().unwrap_err();
            let _ = self.0.send(format!("{} {}", e.code, r.code));
            return;
        }
        let stale = ext
            .file_diagnostics(&Target::Doc(1), Some(0), &ds)
            .unwrap_err();
        let path = ext.file_diagnostics(&Target::Path("/p/other.rs".into()), Some(3), &ds);
        let rel = ext
            .file_diagnostics(&Target::Path("other.rs".into()), None, &ds)
            .unwrap_err();
        let nover = ext
            .file_diagnostics(&Target::Doc(1), None, &ds)
            .unwrap_err();
        let reset = ext.reset_diagnostics();
        let _ = self.0.send(format!(
            "{} {} {:?} {} {} {:?}",
            stale.code,
            stale.text,
            path.is_ok(),
            rel.code,
            nover.code,
            reset.is_ok()
        ));
    }
    fn hover(&mut self, _ext: &Ext, req: &rhun_ext::HoverRequest) -> Option<rhun_ext::Hover> {
        (req.at.line != 9)
            .then(|| rhun_ext::Hover::new(rg(2, 1, 2, 9), &format!("fn {}", req.kind)))
    }
    fn selection_ranges(
        &mut self,
        _ext: &Ext,
        req: &rhun_ext::SelectionRangesRequest,
    ) -> Vec<Vec<Range>> {
        req.positions
            .iter()
            .map(|p| match p.line {
                1 => vec![
                    rg(1, 5, 1, 8),
                    rg(1, 5, 1, 8),
                    rg(1, 1, 1, 20),
                    rg(1, 2, 2, 1),
                ],
                2 => vec![],
                l => vec![rg(l, 1, l, 9), rg(l, 9, l, 1)],
            })
            .collect()
    }
    fn call_hierarchy(
        &mut self,
        _ext: &Ext,
        _req: &rhun_ext::PosRequest,
    ) -> Vec<rhun_ext::HierarchyItem> {
        vec![
            item("main", "m1"),
            item("outside", "o").selection(rg(1, 1, 1, 2)),
        ]
    }
    fn calls_in(&mut self, _ext: &Ext, data: &[u8], out: &rhun_ext::Stream) -> Vec<rhun_ext::Call> {
        use rhun_ext::Call;
        if out
            .part_calls(&[Call::new(item("caller", "c1"), vec![rg(4, 2, 4, 6)])])
            .is_err()
        {
            return vec![Call::new(item("nostream", ""), vec![rg(1, 1, 1, 2)])];
        }
        let name = format!("late{}", String::from_utf8_lossy(data));
        vec![
            Call::new(
                item(&name, ""),
                vec![rg(5, 1, 5, 2), rg(6, 2, 6, 1), rg(7, 1, 7, 3)],
            ),
            Call::new(item("nosites", ""), vec![]),
        ]
    }
    fn type_hierarchy(
        &mut self,
        _ext: &Ext,
        _req: &rhun_ext::PosRequest,
    ) -> Vec<rhun_ext::HierarchyItem> {
        let mut it = item("Shape", "t");
        it.kind = rhun_ext::SymbolKind::Interface;
        vec![it]
    }
    fn supertypes(
        &mut self,
        _ext: &Ext,
        data: &[u8],
        _out: &rhun_ext::Stream,
    ) -> Vec<rhun_ext::HierarchyItem> {
        vec![item(&format!("Base{}", String::from_utf8_lossy(data)), "b")]
    }
    fn workspace_symbols(
        &mut self,
        _ext: &Ext,
        req: &rhun_ext::WorkspaceSymbolsRequest,
        out: &rhun_ext::Stream,
    ) -> Vec<rhun_ext::WorkspaceSymbol> {
        use rhun_ext::{SymbolKind, Target, WorkspaceSymbol};
        let first = vec![WorkspaceSymbol::new(
            Target::Path("/p/a.go".into()),
            rg(3, 6, 3, 10),
            SymbolKind::Function,
            "Main",
        )
        .detail("main")];
        if out.part_workspace_symbols(&first).is_err() {
            return first;
        }
        vec![WorkspaceSymbol::new(
            Target::Doc(2),
            rg(1, 1, 1, 4),
            SymbolKind::Function,
            &format!("{}\nx", req.query),
        )]
    }
    fn document_links(
        &mut self,
        _ext: &Ext,
        req: &rhun_ext::DocRequest,
    ) -> Vec<rhun_ext::DocumentLink> {
        use rhun_ext::{DocumentLink, LinkTarget};
        vec![
            DocumentLink::new(
                rg(1, 1, 1, 20),
                LinkTarget::Url("https://example.test/a".into()),
            )
            .tip("Open\nthe page"),
            DocumentLink::new(
                rg(2, 3, 2, 9),
                LinkTarget::Path("/p/a.go".into(), Pos::new(4, 2)),
            ),
            DocumentLink::new(rg(4, 3, 4, 9), LinkTarget::Doc(req.doc)),
            DocumentLink::new(
                rg(5, 3, 5, 9),
                LinkTarget::Path("relative.go".into(), Pos::default()),
            ),
        ]
    }
}

#[test]
fn hover_selection_hierarchy_symbols_links_and_file_diagnostics() {
    let feats = vec![
        Feature::FileDiagnostics,
        Feature::HoverQuery,
        Feature::SelectionRanges,
        Feature::Hierarchy,
        Feature::WorkspaceSymbols,
        Feature::DocumentLinks,
        Feature::QueryStream,
    ];
    let (tx, rx) = channel();
    let (mut host, done) = session(feats, MoreProbe(tx));
    let names = "file-diagnostics hover-query selection-ranges hierarchy workspace-symbols document-links query-stream";
    assert_eq!(host.line(), format!("hello 1 {names}"));
    host.send(&format!("0 features {names}\n0 ready -\n"));
    let rec = "1 4 1 9 error 8 7\nbad\nnameid 2\ne1";
    assert_eq!(
        host.line(),
        format!("1 file-diagnostics 0 1 2 {}", 2 + rec.len())
    );
    assert_eq!(host.body(2 + rec.len()), format!("#1{rec}"));
    host.send("error 1 stale 4\n");
    assert_eq!(
        host.line(),
        format!("2 file-diagnostics - 1 11 {}", 11 + rec.len())
    );
    assert_eq!(host.body(11 + rec.len()), format!("/p/other.rs{rec}"));
    host.send("ok 2\n");
    assert_eq!(host.line(), "3 file-diagnostics-reset");
    host.send("ok 3\n");
    assert_eq!(
        rx.recv_timeout(Duration::from_secs(3)).unwrap(),
        "stale 4 true syntax syntax true"
    );
    host.send("3 hover-query 4 7 2 5 pointer -\n");
    answer(&mut host, 3, "2 1 2 9\nfn pointer");
    host.send("4 hover-query 4 7 9 1 invoked -\n");
    answer(&mut host, 4, "");
    host.send("5 selection-ranges 4 7 3 12\n1 6\n2 3\n5 4\n");
    answer(
        &mut host,
        5,
        "2\n1 5 1 8\n1 1 1 20\n1\n2 3 2 3\n1\n5 1 5 9\n",
    );
    host.send("6 call-hierarchy 4 7 3 7 invoked -\n");
    answer(
        &mut host,
        6,
        &("2\n".to_string()
            + &item_rec("3 6 3 10", "main", "m1", &[])
            + &item_rec("3 1 9 2", "outside", "o", &[])),
    );
    host.send("7 calls-in 2\nm1");
    let part = "1\n".to_string() + &item_rec("3 6 3 12", "caller", "c1", &["4 2 4 6"]);
    assert_eq!(host.line(), format!("4 query-part 7 {}", part.len()));
    assert_eq!(host.body(part.len()), part);
    host.send("ok 4\n");
    answer(
        &mut host,
        7,
        &("1\n".to_string() + &item_rec("3 6 3 12", "latem1", "", &["5 1 5 2", "7 1 7 3"])),
    );
    host.send("8 calls-out 1\nx");
    answer(&mut host, 8, "0\n");
    host.send("9 type-hierarchy 4 7 3 7 invoked -\n");
    answer(
        &mut host,
        9,
        "1\ninterface 3 1 9 2 3 6 3 11 7 5 3 1\n/p/a.goShapepkgt",
    );
    host.send("10 supertypes 1\nt");
    answer(
        &mut host,
        10,
        &("1\n".to_string() + &item_rec("3 6 3 11", "Baset", "b", &[])),
    );
    host.send("11 subtypes 1\nt");
    answer(&mut host, 11, "0\n");
    host.send("12 workspace-symbols 500 3\nmai");
    let part = "1\n3 6 3 10 function 7 4 4\n/p/a.goMainmain";
    assert_eq!(host.line(), format!("5 query-part 12 {}", part.len()));
    assert_eq!(host.body(part.len()), part);
    host.send("error 5 cancelled\n");
    answer(&mut host, 12, part);
    host.send("13 document-links 4 7\n");
    answer(
        &mut host,
        13,
        "3\n1 1 1 20 22 13\nhttps://example.test/aOpen the page2 3 2 9 11 0\n/p/a.go:4:24 3 4 9 2 0\n#4",
    );
    host.send("14 selection-ranges 4 7 2 4\n5 4\n");
    assert!(host.line().starts_with("error 14 unknown"));
    host.send("0 bye\n");
    done.join().unwrap().unwrap();

    // without the features: requests are unknown (bodies read past), file-diagnostics is not sent
    let (tx, rx) = channel();
    let (mut host, done) = session(vec![Feature::Rename], MoreProbe(tx));
    assert_eq!(host.line(), "hello 1 rename");
    host.send("0 features rename\n0 ready -\n");
    assert_eq!(
        rx.recv_timeout(Duration::from_secs(3)).unwrap(),
        "unknown unknown"
    );
    for (id, req) in [
        (2, "hover-query 4 7 2 5 pointer -\n"),
        (3, "selection-ranges 4 7 1 4\n1 6\n"),
        (4, "call-hierarchy 4 7 3 7 invoked -\n"),
        (5, "calls-in 2\nm1"),
        (6, "supertypes 1\nt"),
        (7, "workspace-symbols 500 3\nmai"),
        (8, "document-links 4 7\n"),
    ] {
        host.send(&format!("{id} {req}"));
        assert_eq!(host.line(), format!("error {id} unknown no such request"));
    }
    host.send("0 bye\n");
    done.join().unwrap().unwrap();
}
