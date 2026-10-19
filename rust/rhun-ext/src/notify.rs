//! Notifications and progress (protocol 1.1, features `notify` and `progress`). A [`Note`] is a
//! card at the bottom right with up to 3 buttons; a button runs a command reference or reports
//! [`NoteEvent::Pressed`] to [`crate::Handler::note_event`]. Progress shows in the status bar, in a
//! card or in one of the extension's views; Cancel reaches [`crate::Handler::progress_cancel`].
//!
//! ```
//! use rhun_ext::notify::Note;
//!
//! let note = Note::new("Reload the window?").button_command("Reload", "reload", b"").button("Later");
//! assert_eq!(note.encode(), b"18 46\nReload the window?button 6\nReloadcommand reload 0\nbutton 5\nLater".to_vec());
//! ```

use crate::{Event, View};

/// A card's severity.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Severity {
    Info,
    Warning,
    Error,
}

impl Severity {
    pub fn word(self) -> &'static str {
        match self {
            Severity::Info => "info",
            Severity::Warning => "warning",
            Severity::Error => "error",
        }
    }
}

/// A button's command reference: one of the extension's commands and its ARGS.
type CommandRef = Option<(String, Vec<u8>)>;

/// A notification's content.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Note {
    msg: String,
    detail: Option<String>,
    sticky: bool,
    buttons: Vec<(String, CommandRef)>,
}

fn cut(s: &str, max: usize) -> &str {
    if s.len() <= max {
        return s;
    }
    let mut n = max;
    while !s.is_char_boundary(n) {
        n -= 1;
    }
    &s[..n]
}

impl Note {
    /// The message: at most 2000 bytes (LF breaks a line; long lines wrap).
    pub fn new(msg: &str) -> Note {
        Note { msg: cut(msg, 2000).to_string(), ..Note::default() }
    }
    /// A muted line under the message.
    pub fn detail(mut self, text: &str) -> Note {
        self.detail = Some(cut(&text.replace('\n', " "), 1000).to_string());
        self
    }
    /// It stays until closed (info cards go after 8 s, warnings after 12 s otherwise).
    pub fn sticky(mut self) -> Note {
        self.sticky = true;
        self
    }
    /// A button that reports [`NoteEvent::Pressed`] (3 at most).
    pub fn button(mut self, title: &str) -> Note {
        if self.buttons.len() < 3 {
            self.buttons.push((cut(&title.replace('\n', " "), 60).to_string(), None));
        }
        self
    }
    /// A button that runs one of the extension's commands with ARGS (a command reference).
    pub fn button_command(mut self, title: &str, command: &str, args: &[u8]) -> Note {
        if self.buttons.len() < 3 {
            self.buttons
                .push((cut(&title.replace('\n', " "), 60).to_string(), Some((command.to_string(), args.to_vec()))));
        }
        self
    }
    /// The body of `notify` and `notify-set`.
    pub fn encode(&self) -> Vec<u8> {
        let mut extra = Vec::new();
        let mut rec = |head: String, data: &[u8]| {
            extra.extend_from_slice(format!("{head} {}\n", data.len()).as_bytes());
            extra.extend_from_slice(data);
        };
        if let Some(d) = &self.detail {
            rec("detail".into(), d.as_bytes());
        }
        if self.sticky {
            rec("sticky".into(), b"");
        }
        for (title, command) in &self.buttons {
            rec("button".into(), title.as_bytes());
            if let Some((name, args)) = command {
                rec(format!("command {name}"), args);
            }
        }
        let mut out = format!("{} {}\n", self.msg.len(), extra.len()).into_bytes();
        out.extend_from_slice(self.msg.as_bytes());
        out.extend_from_slice(&extra);
        out
    }
}

/// What the user did with a card.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum NoteEvent {
    /// Button N (from 1) without a command reference; the card closed.
    Pressed(u32),
    /// Closed by the user, a timeout or Clear All.
    Closed,
}

/// Where progress shows.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ProgressWhere {
    /// The status bar (a click opens its card).
    Status,
    /// A card with a bar.
    Note,
    /// A bar at the top of one of the extension's views.
    View(View),
}

impl ProgressWhere {
    pub fn word(self) -> String {
        match self {
            ProgressWhere::Status => "status".into(),
            ProgressWhere::Note => "note".into(),
            ProgressWhere::View(v) => format!("view:{v}"),
        }
    }
}

/// `0 notify NOTE pressed N` or `0 notify NOTE closed`.
pub fn parse_note_event(ev: &Event) -> Option<(u64, NoteEvent)> {
    if ev.name != "notify" {
        return None;
    }
    let note = ev.fields.first()?.parse().ok()?;
    match ev.fields.get(1)?.as_str() {
        "pressed" => Some((note, NoteEvent::Pressed(ev.fields.get(2)?.parse().ok()?))),
        "closed" => Some((note, NoteEvent::Closed)),
        _ => None,
    }
}

/// `0 progress P cancel`: the user pressed Cancel.
pub fn parse_progress_cancel(ev: &Event) -> Option<u64> {
    if ev.name != "progress" || ev.fields.get(1).map(String::as_str) != Some("cancel") {
        return None;
    }
    ev.fields.first()?.parse().ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn event(name: &str, fields: &[&str]) -> Event {
        Event { name: name.into(), fields: fields.iter().map(|f| f.to_string()).collect(), ..Event::default() }
    }

    #[test]
    fn a_note_encodes_its_records_in_order() {
        let n = Note::new("Saved").detail("3 files").sticky().button("A").button("B").button("C").button("D");
        assert_eq!(n.encode(), b"5 55\nSaveddetail 7\n3 filessticky 0\nbutton 1\nAbutton 1\nBbutton 1\nC".to_vec());
    }

    #[test]
    fn events_parse() {
        assert_eq!(parse_note_event(&event("notify", &["4", "pressed", "2"])), Some((4, NoteEvent::Pressed(2))));
        assert_eq!(parse_note_event(&event("notify", &["4", "closed"])), Some((4, NoteEvent::Closed)));
        assert_eq!(parse_progress_cancel(&event("progress", &["7", "cancel"])), Some(7));
        assert_eq!(parse_progress_cancel(&event("progress", &["7", "other"])), None);
        assert_eq!(ProgressWhere::View(3).word(), "view:3");
    }
}
