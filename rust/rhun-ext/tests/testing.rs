//! The tests feature against a scripted rhun: items, resolve, runs, test-exec streams, results,
//! coverage, cancel, and what happens without agreement.

use std::io::{self, BufRead, BufReader, Read, Write};
use std::sync::mpsc::{channel, Receiver, Sender};
use std::thread;
use std::time::Duration;

use rhun_ext::{
    run_on, CoverageRange, Error, Ext, Feature, FileCoverage, Handler, Pos, Range, TestCommand,
    TestItem, TestKind, TestMessage, TestReporter, TestResult, TestRun, TestState,
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
    /// The next request `ID VERB ARGS… [LEN]`: its id, its line without the id, and its body
    /// when the verb has one.
    fn request(&mut self) -> (u64, String, String) {
        self.request_skipping("")
    }
    /// [`Host::request`], skipping the line `skip` (a reply racing the extension's own thread).
    fn request_skipping(&mut self, skip: &str) -> (u64, String, String) {
        let mut l = self.line();
        if !skip.is_empty() && l == skip {
            l = self.line();
        }
        let (id, rest) = l.split_once(' ').unwrap();
        let verb = rest.split(' ').next().unwrap();
        let body = if verb == "test-end" {
            String::new()
        } else {
            let n: usize = rest.rsplit(' ').next().unwrap().parse().unwrap();
            self.body(n)
        };
        (id.parse().unwrap(), rest.to_string(), body)
    }
}

fn start<H: Handler + 'static>(
    features: &'static [Feature],
    h: H,
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
            features,
            h,
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

fn refused(text: &str) -> Error {
    Error {
        code: "refused".into(),
        text: text.into(),
    }
}

struct Ctl(Sender<String>);

impl Handler for Ctl {
    fn ready(&mut self, ext: &Ext, _project: Option<&str>) {
        let r = ext.set_test_items(
            "",
            &[TestItem::new(TestKind::Group, "pkg", "pkg").lazy()],
            true,
        );
        let g = ext.remove_test_items(&["gone"]);
        self.0
            .send(format!("ready {} {}", r.is_ok(), g.is_ok()))
            .unwrap();
    }
    fn resolve_tests(&mut self, _ext: &Ext, parent: &str) -> Result<Vec<TestItem>, Error> {
        match parent {
            "" => Ok(vec![TestItem::new(TestKind::Group, "pkg", "pkg").lazy()]),
            "pkg" => Ok(vec![TestItem::new(TestKind::Test, "pkg/A", "A")
                .at("/p/a.rs", Some(Range::new(Pos::new(3, 1), Pos::new(5, 2))))]),
            _ => Err(refused("no such item")),
        }
    }
    fn run_tests(&mut self, ext: &Ext, run: &TestRun) -> Result<(), Error> {
        if run.profile == "debug" {
            return Err(Error {
                code: "busy".into(),
                text: "the debugger is busy".into(),
            });
        }
        let (ext, run, log) = (ext.clone(), run.clone(), self.0.clone());
        thread::spawn(move || {
            let p = ext
                .test_exec(run.run, &TestCommand::new("cargo").arg("test").env("A=1"))
                .unwrap();
            let mut out = String::new();
            let mut so = p.stdout.clone();
            so.read_to_string(&mut out).unwrap();
            let mut err = String::new();
            let mut se = p.stderr.clone();
            se.read_to_string(&mut err).unwrap();
            let code = p.wait();
            let rep = TestReporter::new(&ext, run.run);
            if out.contains("ok") {
                rep.add(TestResult::new("pkg/A", TestState::Passed));
            } else {
                rep.add(
                    TestResult::new("pkg/A", TestState::Failed)
                        .took(Duration::from_millis(3))
                        .message(TestMessage::new("boom").values("1", "2")),
                );
            }
            rep.close().unwrap();
            if run.profile == "coverage" {
                ext.report_coverage(
                    run.run,
                    &[FileCoverage {
                        path: "/p/a.rs".into(),
                        ranges: vec![CoverageRange {
                            from: 3,
                            to: 5,
                            count: 1,
                        }],
                    }],
                )
                .unwrap();
            }
            ext.end_test_run(run.run).unwrap();
            log.send(format!("ran {} {code} {out:?} {err:?}", run.run))
                .unwrap();
        });
        Ok(())
    }
    fn cancel_tests(&mut self, _ext: &Ext, run: u64) {
        self.0.send(format!("cancel {run}")).unwrap();
    }
}

#[test]
fn a_test_controller_session() {
    let (log, seen) = channel();
    let (mut host, done) = start(&[Feature::Tests], Ctl(log));
    assert_eq!(host.line(), "hello 1 tests");
    host.send("0 features tests\n0 ready /p\n");
    let (id, line, body) = host.request();
    assert_eq!(
        (line.as_str(), body.as_str()),
        (
            "test-items replace 37",
            "0 1\ngroup 0 0 0 0 lazy 3 3 0 0\npkgpkg"
        )
    );
    host.send(&format!("ok {id}\n"));
    let (id, line, body) = host.request();
    assert_eq!(
        (line.as_str(), body.as_str()),
        ("test-remove 1 5", "gone\n")
    );
    host.send(&format!("ok {id}\n"));
    assert_eq!(
        seen.recv_timeout(Duration::from_secs(2)).unwrap(),
        "ready true true"
    );

    // tests-resolve: the roots, a lazy item, a refusal
    host.send("5 tests-resolve 0\n");
    assert_eq!(host.line(), "ok 5 37");
    assert_eq!(host.body(37), "0 1\ngroup 0 0 0 0 lazy 3 3 0 0\npkgpkg");
    host.send("6 tests-resolve 3\npkg");
    let l = host.line();
    let n: usize = l.strip_prefix("ok 6 ").unwrap().parse().unwrap();
    assert_eq!(
        host.body(n),
        "3 1\npkgtest 3 1 5 2 - 5 1 7 0\npkg/AA/p/a.rs"
    );
    host.send("7 tests-resolve 1\nx");
    assert_eq!(host.line(), "error 7 refused no such item");

    // a run: its process's output and end arrive after the exec reply
    host.send("8 test-run 1 coverage 1 6\npkg/A\n");
    let (id, line, body) = host.request_skipping("ok 8 0");
    let exec = "5 0 1 1\ncargo4\ntest3\nA=1";
    assert_eq!(
        (line, body.as_str()),
        (format!("test-exec 1 {}", exec.len()), exec)
    );
    host.send(&format!("ok {id} 3\n"));
    host.send("0 test-out 3 out 3\nok\n0 test-out 3 err 2\nw\n0 test-exit 3 0\n");
    let mut msgs = Vec::new();
    loop {
        let (id, line, body) = host.request_skipping("ok 8 0");
        host.send(&format!("ok {id}\n"));
        let end = line.starts_with("test-end");
        msgs.push((line, body));
        if end {
            break;
        }
    }
    let update = "1\npassed - 5 0\npkg/A";
    let cov = "1\n7 1\n/p/a.rs3 5 1\n";
    assert_eq!(
        msgs,
        vec![
            (
                format!("test-update 1 {}", update.len()),
                update.to_string()
            ),
            (format!("test-coverage 1 {}", cov.len()), cov.to_string()),
            ("test-end 1".to_string(), String::new()),
        ]
    );
    assert_eq!(
        seen.recv_timeout(Duration::from_secs(2)).unwrap(),
        "ran 1 0 \"ok\\n\" \"w\\n\""
    );

    // a process that ended before its exec reply arrived still has its output and code
    host.send("9 test-run 2 run 0 0\n");
    let (id, line, _) = host.request_skipping("ok 9 0");
    assert!(line.starts_with("test-exec 2 "));
    host.send(&format!(
        "0 test-out 4 out 4\nfail0 test-exit 4 signal 9\nok {id} 4\n"
    ));
    let failed = "1\nfailed 3 5 36\npkg/Amessage 4\nboomexpected 1\n1actual 1\n2";
    let (id, line, body) = host.request_skipping("ok 9 0");
    assert_eq!(
        (line, body.as_str()),
        (format!("test-update 2 {}", failed.len()), failed)
    );
    host.send(&format!("ok {id}\n"));
    let (id, line, _) = host.request_skipping("ok 9 0");
    assert_eq!(line, "test-end 2");
    host.send(&format!("ok {id}\n"));
    assert_eq!(
        seen.recv_timeout(Duration::from_secs(2)).unwrap(),
        "ran 2 signal 9 \"fail\" \"\""
    );

    // refused runs, a malformed run, cancel
    host.send("10 test-run 3 debug 0 0\n");
    assert_eq!(host.line(), "error 10 busy the debugger is busy");
    host.send("11 test-run x run 0 0\n");
    assert_eq!(host.line(), "error 11 syntax bad test-run");
    host.send("0 test-cancel 4\n");
    assert_eq!(
        seen.recv_timeout(Duration::from_secs(2)).unwrap(),
        "cancel 4"
    );
    host.send("0 bye\n");
    done.join().unwrap().unwrap();
}

#[test]
fn processes_end_killed_when_rhun_goes() {
    struct Waits(Sender<String>);
    impl Handler for Waits {
        fn run_tests(&mut self, ext: &Ext, run: &TestRun) -> Result<(), Error> {
            let (ext, run, log) = (ext.clone(), run.clone(), self.0.clone());
            thread::spawn(move || {
                let p = ext.test_exec(run.run, &TestCommand::new("go")).unwrap();
                log.send(p.wait()).unwrap();
                let err = ext.end_test_run(run.run).unwrap_err();
                log.send(err.code).unwrap();
            });
            Ok(())
        }
    }
    let (log, seen) = channel();
    let (mut host, done) = start(&[Feature::Tests], Waits(log));
    host.line();
    host.send("0 features tests\n0 ready -\n1 test-run 1 run 0 0\n");
    let (id, line, _) = host.request_skipping("ok 1 0");
    assert_eq!(line, "test-exec 1 10");
    host.send(&format!("ok {id} 9\n"));
    thread::sleep(Duration::from_millis(50));
    drop(host);
    assert_eq!(seen.recv_timeout(Duration::from_secs(2)).unwrap(), "killed");
    assert_eq!(seen.recv_timeout(Duration::from_secs(2)).unwrap(), "closed");
    done.join().unwrap().unwrap();
}

#[test]
fn without_agreement_tests_are_unknown() {
    struct Early(Sender<String>);
    impl Handler for Early {
        fn ready(&mut self, ext: &Ext, _project: Option<&str>) {
            let a = ext.set_test_items("", &[], true).unwrap_err().code;
            let b = ext
                .test_exec(1, &TestCommand::new("go"))
                .err()
                .unwrap()
                .code;
            self.0.send(format!("{a} {b}")).unwrap();
        }
        fn resolve_tests(&mut self, _ext: &Ext, _parent: &str) -> Result<Vec<TestItem>, Error> {
            Ok(Vec::new())
        }
        fn cancel_tests(&mut self, _ext: &Ext, run: u64) {
            self.0.send(format!("cancel {run}")).unwrap();
        }
    }
    let (log, seen) = channel();
    let (mut host, done) = start(&[Feature::Tests], Early(log));
    assert_eq!(host.line(), "hello 1 tests");
    host.send("0 features\n0 ready -\n");
    assert_eq!(
        seen.recv_timeout(Duration::from_secs(2)).unwrap(),
        "unknown unknown"
    );
    host.send("3 tests-resolve 0\n0 test-cancel 1\n4 test-run 1 run 0 0\n");
    assert_eq!(host.line(), "error 3 unknown no such request");
    assert_eq!(host.line(), "error 4 unknown no such request");
    assert!(seen.recv_timeout(Duration::from_millis(200)).is_err());
    host.send("0 bye\n");
    done.join().unwrap().unwrap();
}

#[test]
fn a_test_out_length_rhun_never_sends_ends_the_session() {
    let (log, _seen) = channel();
    let (mut host, done) = start(&[Feature::Tests], Ctl(log));
    host.line();
    host.send("0 test-out 1 out 99999999\n");
    done.join().unwrap().unwrap();
    drop(host);
}
