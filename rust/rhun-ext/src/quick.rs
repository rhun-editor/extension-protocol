//! Quick input (protocol 1.1, feature `quick-input`): a multi-step quick pick or validated input
//! in a palette-like card. [`crate::Ext::quick`] opens a [`Step`]; [`crate::Ext::quick_set`]
//! changes what its records name (the next step, a message for the text, busy); what the user does
//! reaches [`crate::Handler::quick_event`]. rhun never closes a session on accept: the extension
//! answers with `quick_set` or `quick_close`.
//!
//! ```
//! use rhun_ext::quick::{Item, Step};
//!
//! let step = Step::pick("Branch").step(1, 2).items(&[Item::new("main", "main").desc("current")]);
//! assert_eq!(
//!     step.encode(),
//!     b"title 6\nBranchmode pick 0\nstep 1 2 0\nitems 1 34\n4 4 14 item\nmainmaindesc 7\ncurrent".to_vec()
//! );
//! ```

use crate::Event;

fn record(out: &mut Vec<u8>, head: &str, data: &[u8]) {
    out.extend_from_slice(head.as_bytes());
    out.push(b' ');
    out.extend_from_slice(data.len().to_string().as_bytes());
    out.push(b'\n');
    out.extend_from_slice(data);
}

fn one_line(s: &str) -> String {
    s.chars()
        .map(|c| if c == '\n' || c == '\r' { ' ' } else { c })
        .collect()
}

/// A step's flags.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Flag {
    /// Checkboxes; accept names the checked items.
    Multi,
    /// A Back button (and Alt+Left): [`QuickEvent::Back`].
    Back,
    /// A spinner in the field.
    Busy,
    /// Every item shows whatever the text; `hl` ranges mark them.
    NoFilter,
    /// The filter matches descriptions too.
    MatchDesc,
    /// The filter matches detail lines too.
    MatchDetail,
    /// The field is masked.
    Password,
}

impl Flag {
    fn word(self) -> &'static str {
        match self {
            Flag::Multi => "multi",
            Flag::Back => "back",
            Flag::Busy => "busy",
            Flag::NoFilter => "nofilter",
            Flag::MatchDesc => "match-desc",
            Flag::MatchDetail => "match-detail",
            Flag::Password => "password",
        }
    }
}

/// An item, or a separator with a label.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Item {
    id: Vec<u8>,
    label: String,
    sep: bool,
    extra: Vec<u8>,
}

impl Item {
    /// An item: its id (at most 1000 bytes) and a one-line label.
    pub fn new(id: impl AsRef<[u8]>, label: &str) -> Item {
        Item {
            id: id.as_ref().to_vec(),
            label: one_line(label),
            sep: false,
            extra: Vec::new(),
        }
    }
    /// A separator labelled so; it shows when an item under it does.
    pub fn separator(label: &str) -> Item {
        Item {
            id: Vec::new(),
            label: one_line(label),
            sep: true,
            extra: Vec::new(),
        }
    }
    /// Muted text after the label.
    pub fn desc(mut self, text: &str) -> Item {
        record(&mut self.extra, "desc", one_line(text).as_bytes());
        self
    }
    /// A second, muted line.
    pub fn detail(mut self, text: &str) -> Item {
        record(&mut self.extra, "detail", one_line(text).as_bytes());
        self
    }
    /// A built-in icon name (or `file:NAME`) and a color word or `-`.
    pub fn icon(mut self, spec: &str, color: &str) -> Item {
        record(&mut self.extra, &format!("icon {spec} {color}"), b"");
        self
    }
    /// Checked when the step opens (multi).
    pub fn picked(mut self) -> Item {
        record(&mut self.extra, "picked", b"");
        self
    }
    /// Shown whatever the filter.
    pub fn always(mut self) -> Item {
        record(&mut self.extra, "always", b"");
        self
    }
    /// Label bytes in the accent color (with [`Flag::NoFilter`]).
    pub fn highlight(mut self, from: usize, to: usize) -> Item {
        record(&mut self.extra, &format!("hl {from} {to}"), b"");
        self
    }
    /// An icon button on the active or hovered row (4 at most): [`QuickEvent::Button`].
    pub fn button(mut self, key: &str, icon: &str, tip: &str) -> Item {
        record(
            &mut self.extra,
            &format!("button {key} {icon}"),
            one_line(tip).as_bytes(),
        );
        self
    }
    fn encode(&self, out: &mut Vec<u8>) {
        out.extend_from_slice(
            format!(
                "{} {} {} {}\n",
                self.id.len(),
                self.label.len(),
                self.extra.len(),
                if self.sep { "sep" } else { "item" }
            )
            .as_bytes(),
        );
        out.extend_from_slice(&self.id);
        out.extend_from_slice(self.label.as_bytes());
        out.extend_from_slice(&self.extra);
    }
}

/// A step body: only the records set are sent, so a [`crate::Ext::quick_set`] changes only them.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Step {
    out: Vec<u8>,
}

impl Step {
    /// An empty body: nothing changes.
    pub fn new() -> Step {
        Step::default()
    }
    /// A pick list titled so.
    pub fn pick(title: &str) -> Step {
        Step::new().title(title).mode_pick()
    }
    /// An input titled so (no items).
    pub fn input(title: &str) -> Step {
        let mut s = Step::new().title(title);
        record(&mut s.out, "mode input", b"");
        s
    }
    pub fn title(mut self, text: &str) -> Step {
        record(&mut self.out, "title", one_line(text).as_bytes());
        self
    }
    fn mode_pick(mut self) -> Step {
        record(&mut self.out, "mode pick", b"");
        self
    }
    /// "N/M" in the title row (0, 0 hides it).
    pub fn step(mut self, n: u32, of: u32) -> Step {
        record(&mut self.out, &format!("step {n} {of}"), b"");
        self
    }
    pub fn placeholder(mut self, text: &str) -> Step {
        record(&mut self.out, "placeholder", one_line(text).as_bytes());
        self
    }
    /// The line under the field.
    pub fn prompt(mut self, text: &str) -> Step {
        record(&mut self.out, "prompt", one_line(text).as_bytes());
        self
    }
    /// The field's text: applied at `quick`, after an accept (the next step), or when `seq` is the
    /// field's sequence (the last [`QuickEvent::Value`]'s).
    pub fn value(mut self, seq: Option<u64>, text: &str) -> Step {
        let s = seq.map(|n| n.to_string()).unwrap_or_else(|| "-".into());
        record(
            &mut self.out,
            &format!("value {s}"),
            one_line(text).as_bytes(),
        );
        self
    }
    /// The flags in effect from now (none clears them).
    pub fn flags(mut self, flags: &[Flag]) -> Step {
        let w = if flags.is_empty() {
            "-".to_string()
        } else {
            flags
                .iter()
                .take(6)
                .map(|f| f.word())
                .collect::<Vec<_>>()
                .join(" ")
        };
        record(&mut self.out, &format!("flags {w}"), b"");
        self
    }
    /// A message about the text of sequence `seq`: `info`, `warning`, or `error` (refuses Enter).
    pub fn msg(mut self, seq: u64, severity: &str, text: &str) -> Step {
        record(
            &mut self.out,
            &format!("msg {seq} {severity}"),
            one_line(text).as_bytes(),
        );
        self
    }
    /// Title buttons (8 at most): a key, a built-in icon name, a tooltip each.
    pub fn buttons(mut self, buttons: &[(&str, &str, &str)]) -> Step {
        let mut data = Vec::new();
        for (key, icon, tip) in buttons.iter().take(8) {
            let tip = one_line(tip);
            data.extend_from_slice(format!("{key} {icon} {}\n", tip.len()).as_bytes());
            data.extend_from_slice(tip.as_bytes());
        }
        record(
            &mut self.out,
            &format!("buttons {}", buttons.len().min(8)),
            &data,
        );
        self
    }
    /// The items (10000 at most), replacing the last ones; checks and the active item stay by id.
    pub fn items(mut self, items: &[Item]) -> Step {
        let mut data = Vec::new();
        for it in items.iter().take(10000) {
            it.encode(&mut data);
        }
        record(
            &mut self.out,
            &format!("items {}", items.len().min(10000)),
            &data,
        );
        self
    }
    /// The active item.
    pub fn active(mut self, id: impl AsRef<[u8]>) -> Step {
        record(&mut self.out, "active", id.as_ref());
        self
    }
    pub fn encode(&self) -> Vec<u8> {
        self.out.clone()
    }
}

/// What the user did in a quick input. SEQ is the field's sequence: edits counted from 1.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum QuickEvent {
    /// The field changed.
    Value { seq: u64, text: String },
    /// The active item moved.
    Active { seq: u64, id: Vec<u8> },
    /// Enter or a click: the checked items (multi) or the active one, and the field's text.
    Accept {
        seq: u64,
        ids: Vec<Vec<u8>>,
        text: String,
    },
    /// Back (Alt+Left or its button).
    Back { seq: u64 },
    /// A title button (empty id) or an item's button.
    Button { seq: u64, key: String, id: Vec<u8> },
    /// Dismissed: the session is gone.
    Hide,
}

/// Quick events carry a body: the reader reads LEN bytes after their line.
pub(crate) fn has_body(what: &str) -> bool {
    matches!(what, "value" | "active" | "accept" | "button")
}

/// `0 quick Q ...` as the session's Q and a [`QuickEvent`].
pub fn parse_quick_event(ev: &Event) -> Option<(u64, QuickEvent)> {
    if ev.name != "quick" {
        return None;
    }
    let q: u64 = ev.fields.first()?.parse().ok()?;
    let num = |i: usize| {
        ev.fields
            .get(i)
            .and_then(|v| v.parse::<u64>().ok())
            .unwrap_or(0)
    };
    let e = match ev.fields.get(1)?.as_str() {
        "value" => QuickEvent::Value {
            seq: num(2),
            text: String::from_utf8_lossy(&ev.body).into_owned(),
        },
        "active" => QuickEvent::Active {
            seq: num(2),
            id: ev.body.clone(),
        },
        "accept" => {
            let n = num(3) as usize;
            let mut parts = ev.body.splitn(n + 1, |&b| b == b'\n');
            let mut ids = Vec::new();
            for _ in 0..n {
                ids.push(parts.next()?.to_vec());
            }
            let text = parts
                .next()
                .map(|t| String::from_utf8_lossy(t).into_owned())
                .unwrap_or_default();
            QuickEvent::Accept {
                seq: num(2),
                ids,
                text,
            }
        }
        "back" => QuickEvent::Back { seq: num(2) },
        "button" => QuickEvent::Button {
            seq: num(2),
            key: ev.fields.get(3).cloned().unwrap_or_default(),
            id: ev.body.clone(),
        },
        "hide" => QuickEvent::Hide,
        _ => return None,
    };
    Some((q, e))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn event(line: &str, body: &[u8]) -> Event {
        let mut f: Vec<String> = line.split(' ').map(String::from).collect();
        let name = f.remove(0);
        Event {
            name,
            rest: f.join(" "),
            fields: f,
            body: body.to_vec(),
        }
    }

    #[test]
    fn accept_splits_ids_and_text() {
        let ev = event("quick 3 accept 5 2 9", b"a\nb\nxyz");
        assert_eq!(
            parse_quick_event(&ev),
            Some((
                3,
                QuickEvent::Accept {
                    seq: 5,
                    ids: vec![b"a".to_vec(), b"b".to_vec()],
                    text: "xyz".into()
                }
            ))
        );
        let ev = event("quick 3 button 0 refresh 0", b"");
        assert_eq!(
            parse_quick_event(&ev),
            Some((
                3,
                QuickEvent::Button {
                    seq: 0,
                    key: "refresh".into(),
                    id: vec![]
                }
            ))
        );
    }

    #[test]
    fn flags_and_messages() {
        let s = Step::new()
            .flags(&[Flag::Multi, Flag::Back])
            .msg(4, "error", "taken")
            .flags(&[]);
        assert_eq!(
            s.encode(),
            b"flags multi back 0\nmsg 4 error 5\ntakenflags - 0\n".to_vec()
        );
    }
}
