//! Retained views against a scripted rhun: instances, events with bodies, vkids answered, patches.

use std::io::Cursor;
use std::sync::{Arc, Mutex};

use rhun_ext::views::{ChildrenRequest, Kids, Node, Patch, RetainedEvent, ViewOpen};
use rhun_ext::{run_on, Ext, Feature, Handler, View};

#[derive(Default)]
struct Seen {
    log: Vec<String>,
}

struct Probe(Arc<Mutex<Seen>>);

impl Handler for Probe {
    fn view_open(&mut self, _ext: &Ext, open: &ViewOpen) {
        self.0.lock().unwrap().log.push(format!("open {} {} {}", open.view, open.place, open.name));
    }
    fn view_event(&mut self, _ext: &Ext, view: View, ev: &RetainedEvent) {
        self.0.lock().unwrap().log.push(format!("{view} {ev:?}"));
    }
    fn view_children(&mut self, _ext: &Ext, req: &ChildrenRequest) -> Result<Patch, String> {
        let mut p = Patch::new();
        p.kids(&req.node, &[Node::new("x1", "X one")], false);
        Ok(p)
    }
}

#[test]
fn instances_events_and_children_requests() {
    let input = b"0 features view-tree\n0 ready /p\n0 view 3 open tree sidebar todo\n\
0 view 3 select 2 1 4\nabc\n0 view 3 check 2 1 1\nc5 vkids 3 2 1\nx0 view 3 action 2 pin 1\nb0 bye\n";
    let seen = Arc::new(Mutex::new(Seen::default()));
    let out: Vec<u8> = Vec::new();
    let shared = Arc::new(Mutex::new(out));
    struct W(Arc<Mutex<Vec<u8>>>);
    impl std::io::Write for W {
        fn write(&mut self, b: &[u8]) -> std::io::Result<usize> {
            self.0.lock().unwrap().extend_from_slice(b);
            Ok(b.len())
        }
        fn flush(&mut self) -> std::io::Result<()> {
            Ok(())
        }
    }
    run_on(Cursor::new(input.to_vec()), W(shared.clone()), &[Feature::ViewTree], Probe(seen.clone())).unwrap();
    let log = seen.lock().unwrap().log.clone();
    assert_eq!(log[0], "open 3 sidebar todo");
    assert!(log[1].contains("Select { rev: 2, ids: [[97, 98, 99]] }"), "{log:?}");
    assert!(log[2].contains("Check { rev: 2, on: true, id: [99] }"), "{log:?}");
    assert!(log[3].contains("Action { rev: 2, key: \"pin\", id: [98] }"), "{log:?}");
    let written = String::from_utf8(shared.lock().unwrap().clone()).unwrap();
    assert!(written.starts_with("hello 1 view-tree\n"), "{written}");
    assert!(written.contains("ok 5 35\n1\nkids 1 1 0 19\nx2 5 0 leaf\nx1X one"), "{written}");
}

#[test]
fn actions_and_inline_records_encode() {
    let mut p = Patch::new();
    p.action("pin", Some("pin"), rhun_ext::views::Where::Node, "Pin").select_mode(true);
    p.kids(b"", &[Node::new("a", "A").kids(Kids::Leaf).inline(&["pin"]).menu(&["pin", "hide"])], false);
    let body = String::from_utf8(p.encode().unwrap()).unwrap();
    assert!(body.starts_with("3\naction pin pin node 3\nPinselect-mode multi 0\n"), "{body}");
    assert!(body.contains("inline 3\npinmenu 8\npin hide"), "{body}");
}
