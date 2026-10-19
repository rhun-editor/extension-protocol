//! The editor model of protocol 1.1: several selections (feature `selections`), scrolling to a place and listing locations (feature `reveal`), diff tabs
//! (feature `diff`) and linked editing ranges (feature `linked-editing`).

use crate::lang::{encode_locations, Location};
use crate::{Doc, Event, Pos, Range};

/// The most selections a document has, and a `select-many` sends.
pub const MAX_SELECTIONS: usize = 512;
/// The longest title of `show-locations` and `diff` rhun shows.
pub const MAX_TITLE: usize = 200;
/// The most ranges of a linked editing answer.
pub const MAX_LINKED: usize = 64;

/// A selection: the anchor and the cursor (equal for a caret).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Selection {
    pub anchor: Pos,
    pub cursor: Pos,
}

impl Selection {
    pub fn new(anchor: Pos, cursor: Pos) -> Selection {
        Selection { anchor, cursor }
    }

    /// A caret at a position.
    pub fn caret(at: Pos) -> Selection {
        Selection {
            anchor: at,
            cursor: at,
        }
    }
}

fn pos_at(f: &[&str], i: usize) -> Option<Pos> {
    Some(Pos::new(
        f.get(i)?.parse().ok()?,
        f.get(i + 1)?.parse().ok()?,
    ))
}

/// The event `0 selections DOC N LEN` and its N lines `L1 C1 L2 C2` (feature `selections`):
/// every selection of the document, the primary first.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Selections {
    pub doc: Doc,
    pub sels: Vec<Selection>,
}

impl Selections {
    /// The event's selections, or `None` when it is another event or does not read.
    pub fn parse(ev: &Event) -> Option<Selections> {
        if ev.name != "selections" || ev.fields.len() != 3 {
            return None;
        }
        let doc = ev.fields[0].parse().ok()?;
        let n: usize = ev.fields[1].parse().ok()?;
        if n > MAX_SELECTIONS {
            return None;
        }
        let text = std::str::from_utf8(&ev.body).ok()?;
        let lines: Vec<&str> = text
            .strip_suffix('\n')
            .unwrap_or(text)
            .split('\n')
            .collect();
        if n == 0 || lines.len() != n {
            return None;
        }
        let mut sels = Vec::with_capacity(n);
        for l in lines {
            let f: Vec<&str> = l.split(' ').collect();
            if f.len() != 4 {
                return None;
            }
            sels.push(Selection::new(pos_at(&f, 0)?, pos_at(&f, 2)?));
        }
        Some(Selections { doc, sels })
    }
}

/// The body of `select-many`: N lines `L1 C1 L2 C2`, anchor then cursor; or why not (none, or
/// more than [`MAX_SELECTIONS`]).
pub fn encode_selections(sels: &[Selection]) -> Result<Vec<u8>, String> {
    if sels.is_empty() || sels.len() > MAX_SELECTIONS {
        return Err(format!("{} selections: 1 to {MAX_SELECTIONS}", sels.len()));
    }
    let mut out = String::new();
    for s in sels {
        out.push_str(&format!(
            "{} {} {} {}\n",
            s.anchor.line, s.anchor.col, s.cursor.line, s.cursor.col
        ));
    }
    Ok(out.into_bytes())
}

/// Where `reveal` puts the position in the view.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Reveal {
    Top,
    Center,
    /// As little scrolling as needed.
    Nearest,
    /// Centered, unless it shows already.
    CenterIfOutside,
}

impl Reveal {
    pub fn word(self) -> &'static str {
        match self {
            Reveal::Top => "top",
            Reveal::Center => "center",
            Reveal::Nearest => "nearest",
            Reveal::CenterIfOutside => "center-if-outside",
        }
    }
}

/// Whether a diff tab is activated when it opens.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Show {
    /// Opened and activated (`show`).
    Front,
    /// Opened behind the active tab (`background`).
    Background,
}

impl Show {
    pub fn word(self) -> &'static str {
        match self {
            Show::Front => "show",
            Show::Background => "background",
        }
    }
}

/// One line, at most `max` bytes, cut at a character.
pub(crate) fn title(s: &str, max: usize) -> String {
    let mut t: String = s
        .chars()
        .map(|c| if c == '\n' || c == '\r' { ' ' } else { c })
        .collect();
    if t.len() > max {
        let mut cut = max;
        while !t.is_char_boundary(cut) {
            cut -= 1;
        }
        t.truncate(cut);
    }
    t
}

/// The body of `show-locations`: a title line, then the locations (12d).
pub fn encode_show_locations(heading: &str, locs: &[Location]) -> Vec<u8> {
    let mut out = title(heading, MAX_TITLE).into_bytes();
    out.push(b'\n');
    out.extend_from_slice(&encode_locations(locs));
    out
}

/// `ID linked DOC VERSION LINE COL` (feature `linked-editing`): the cursor rests in a word.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct LinkedRequest {
    pub doc: Doc,
    pub version: i64,
    pub at: Pos,
}

impl LinkedRequest {
    pub fn parse(fields: &[String]) -> Option<LinkedRequest> {
        if fields.len() != 4 {
            return None;
        }
        let n = |i: usize| fields[i].parse::<i64>().ok();
        let (line, col) = (n(2)?, n(3)?);
        if line < 1 || col < 1 || line > u32::MAX as i64 || col > u32::MAX as i64 {
            return None;
        }
        Some(LinkedRequest {
            doc: n(0)?,
            version: n(1)?,
            at: Pos::new(line as u32, col as u32),
        })
    }
}

/// The answer to `linked`: the ranges that change together (2 to 64, at the request's version,
/// with equal text, not overlapping, one holding the cursor), none for nothing, and the ASCII
/// characters that may be typed in them besides letters, digits and non-ASCII (`None`: rhun's
/// default `-_.:`).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Linked {
    pub ranges: Vec<Range>,
    pub word: Option<String>,
}

impl Linked {
    pub fn new(ranges: Vec<Range>) -> Linked {
        Linked { ranges, word: None }
    }

    pub fn word(mut self, chars: &str) -> Linked {
        self.word = Some(chars.to_string());
        self
    }

    /// The body rhun reads: `N`, the ranges, and the `word` record. A count rhun would refuse
    /// (one range, or more than 64) is sent as none; the word keeps printable ASCII characters,
    /// each once.
    pub fn encode(&self) -> Vec<u8> {
        let n = self.ranges.len();
        if !(2..=MAX_LINKED).contains(&n) {
            return b"0\n".to_vec();
        }
        let mut out = format!("{n}\n");
        for r in &self.ranges {
            out.push_str(&format!(
                "{} {} {} {}\n",
                r.start.line, r.start.col, r.end.line, r.end.col
            ));
        }
        if let Some(w) = &self.word {
            let mut kept = String::new();
            for c in w.chars() {
                if (' '..='~').contains(&c) && !kept.contains(c) {
                    kept.push(c);
                }
            }
            out.push_str(&format!("word {}\n{kept}", kept.len()));
        }
        out.into_bytes()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::lang::Target;

    fn ev(line: &str, body: &str) -> Event {
        let fields: Vec<String> = line.split(' ').skip(2).map(str::to_string).collect();
        Event {
            name: line.split(' ').nth(1).unwrap().to_string(),
            fields,
            rest: line.splitn(3, ' ').nth(2).unwrap_or("").to_string(),
            body: body.as_bytes().to_vec(),
        }
    }

    #[test]
    fn selection_events_parse() {
        let s = Selections::parse(&ev("0 selections 4 2 16", "3 1 3 5\n1 2 1 2\n")).unwrap();
        assert_eq!(s.doc, 4);
        assert_eq!(
            s.sels,
            vec![
                Selection::new(Pos::new(3, 1), Pos::new(3, 5)),
                Selection::caret(Pos::new(1, 2))
            ]
        );
        assert!(Selections::parse(&ev("0 selections 4 3 16", "3 1 3 5\n1 2 1 2\n")).is_none());
        assert!(Selections::parse(&ev("0 selections 4 1 6", "3 1 3\n")).is_none());
    }

    #[test]
    fn bodies_are_framed_as_rhun_reads_them() {
        let sels = [
            Selection::new(Pos::new(1, 1), Pos::new(1, 4)),
            Selection::caret(Pos::new(2, 3)),
        ];
        assert_eq!(
            encode_selections(&sels).unwrap(),
            b"1 1 1 4\n2 3 2 3\n".to_vec()
        );
        assert!(encode_selections(&[]).is_err());
        assert!(encode_selections(&vec![Selection::default(); 513]).is_err());
        let locs = [Location::new(
            Target::Path("/a.rs".into()),
            Range::new(Pos::new(1, 1), Pos::new(1, 3)),
        )];
        assert_eq!(
            encode_show_locations("Implementations\nof x", &locs),
            b"Implementations of x\n1\n1 1 1 3 5 0\n/a.rs".to_vec()
        );
        assert_eq!(
            encode_show_locations(&"é".repeat(150), &[]).len(),
            200 + 1 + 2
        );
    }

    #[test]
    fn linked_answers() {
        let r = |l1, c1, l2, c2| Range::new(Pos::new(l1, c1), Pos::new(l2, c2));
        let two = Linked::new(vec![r(1, 2, 1, 5), r(3, 3, 3, 6)]);
        assert_eq!(two.encode(), b"2\n1 2 1 5\n3 3 3 6\n".to_vec());
        assert_eq!(
            two.clone().word("-_\n-é:").encode(),
            b"2\n1 2 1 5\n3 3 3 6\nword 3\n-_:".to_vec()
        );
        assert_eq!(Linked::new(vec![r(1, 2, 1, 5)]).encode(), b"0\n".to_vec());
        assert_eq!(Linked::default().encode(), b"0\n".to_vec());
        assert_eq!(
            Linked::new(vec![r(1, 1, 1, 1); 65]).encode(),
            b"0\n".to_vec()
        );
        let req = LinkedRequest::parse(&["3".into(), "9".into(), "2".into(), "4".into()]).unwrap();
        assert_eq!((req.doc, req.version, req.at), (3, 9, Pos::new(2, 4)));
        assert!(LinkedRequest::parse(&["3".into(), "9".into(), "0".into(), "4".into()]).is_none());
    }
}
