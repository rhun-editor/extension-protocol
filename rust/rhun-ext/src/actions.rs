//! Protocol 1.1's navigation, prepare-rename, partial-format and code-actions (`v1.1/v1.1.md`,
//! "Language features"): what rhun asks, and the answers, encoded as the Go package encodes them.
//! Declaration, type definition and implementation answer with locations
//! ([`crate::lang::encode_locations`]); range and on-type formatting with a workspace edit.

use crate::lang::{field, EditCaps, MAX_ACTIONS, MAX_ACTIONS_BODY, MAX_TEXT};
use crate::{Doc, Pos, Range, WorkspaceEdit};

/// The most bytes of a prepare-rename placeholder, of a disabled action's reason, of an action's
/// resolve data, of a command reference's args, and of a hierarchical kind.
pub const MAX_PLACEHOLDER: usize = 200;
pub const MAX_REASON: usize = 1000;
pub const MAX_ACTION_RESOLVE: usize = 16 << 10;
pub const MAX_ARGS: usize = 8192;
pub const MAX_ACTION_KIND: usize = 128;

fn num<T: std::str::FromStr>(fields: &[String], i: usize) -> Option<T> {
    fields.get(i)?.parse().ok()
}

fn yes_no(s: &str) -> Option<bool> {
    match s {
        "yes" => Some(true),
        "no" => Some(false),
        _ => None,
    }
}

fn range_at(fields: &[String], i: usize) -> Option<Range> {
    Some(Range::new(
        Pos::new(num(fields, i)?, num(fields, i + 1)?),
        Pos::new(num(fields, i + 2)?, num(fields, i + 3)?),
    ))
}

fn range_fields(r: Range) -> String {
    format!(
        "{} {} {} {}",
        r.start.line, r.start.col, r.end.line, r.end.col
    )
}

// ---- prepare-rename

/// What F2 shows (feature `prepare-rename`): the range selected and the name the prompt starts
/// with.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PreparedRename {
    pub range: Range,
    /// One line, cut at [`MAX_PLACEHOLDER`] bytes at a character.
    pub placeholder: String,
}

impl PreparedRename {
    pub fn new(range: Range, placeholder: &str) -> PreparedRename {
        PreparedRename {
            range,
            placeholder: placeholder.to_string(),
        }
    }

    /// The answer's body: `L1 C1 L2 C2 PLEN` LF and the placeholder.
    pub fn encode(&self) -> Vec<u8> {
        let ph = field(&self.placeholder, MAX_PLACEHOLDER);
        format!("{} {}\n{ph}", range_fields(self.range), ph.len()).into_bytes()
    }
}

// ---- partial-format

/// `ID range-format DOC VERSION L1 C1 L2 C2 TAB_WIDTH SPACES TRIM FINAL` (feature
/// `partial-format`): format the range, with the document's settings in force.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct RangeFormatRequest {
    pub doc: Doc,
    pub version: i64,
    pub range: Range,
    pub tab_width: u32,
    /// Indent with spaces (else tabs).
    pub spaces: bool,
    /// `trim_trailing_whitespace` in force.
    pub trim: bool,
    /// `final_newline` in force.
    pub final_newline: bool,
}

/// The four settings at the end of a partial-format request: TAB_WIDTH (at least 1) SPACES TRIM
/// FINAL.
fn options(f: &[String]) -> Option<(u32, bool, bool, bool)> {
    if f.len() != 4 {
        return None;
    }
    let tab: u32 = f[0].parse().ok().filter(|t| *t >= 1)?;
    Some((tab, yes_no(&f[1])?, yes_no(&f[2])?, yes_no(&f[3])?))
}

impl RangeFormatRequest {
    pub fn parse(fields: &[String]) -> Option<RangeFormatRequest> {
        if fields.len() != 10 {
            return None;
        }
        let (tab_width, spaces, trim, final_newline) = options(&fields[6..])?;
        Some(RangeFormatRequest {
            doc: num(fields, 0)?,
            version: num(fields, 1)?,
            range: range_at(fields, 2)?,
            tab_width,
            spaces,
            trim,
            final_newline,
        })
    }
}

/// `ID type-format DOC VERSION LINE COL CHAR TAB_WIDTH SPACES TRIM FINAL` (feature
/// `partial-format`): `ch` was typed (`newline` for Enter) and the cursor is `at`, after it. rhun
/// applies the edit only while the document is still at `version`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TypeFormatRequest {
    pub doc: Doc,
    pub version: i64,
    pub at: Pos,
    pub ch: String,
    pub tab_width: u32,
    pub spaces: bool,
    pub trim: bool,
    pub final_newline: bool,
}

impl TypeFormatRequest {
    pub fn parse(fields: &[String]) -> Option<TypeFormatRequest> {
        if fields.len() != 9 || fields[4].is_empty() {
            return None;
        }
        let (tab_width, spaces, trim, final_newline) = options(&fields[5..])?;
        Some(TypeFormatRequest {
            doc: num(fields, 0)?,
            version: num(fields, 1)?,
            at: Pos::new(num(fields, 2)?, num(fields, 3)?),
            ch: fields[4].clone(),
            tab_width,
            spaces,
            trim,
            final_newline,
        })
    }
}

// ---- code-actions

/// `ID code-actions DOC VERSION L1 C1 L2 C2 TRIGGER KINDS DIAGS` (feature `code-actions`).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CodeActionsRequest {
    /// rhun's id for it: a `cancel` event names it.
    pub id: u64,
    pub doc: Doc,
    pub version: i64,
    /// The selection; without one the diagnostic at the cursor, or the cursor.
    pub range: Range,
    /// `invoked` (the user asked) or `auto` (the lightbulb).
    pub trigger: String,
    /// The hierarchical kinds wanted; none: any.
    pub kinds: Vec<String>,
    /// The ids of this extension's diagnostics in the range (feature `file-diagnostics`).
    pub diagnostics: Vec<String>,
}

impl CodeActionsRequest {
    pub fn parse(id: u64, fields: &[String]) -> Option<CodeActionsRequest> {
        if fields.len() != 9 || (fields[6] != "invoked" && fields[6] != "auto") {
            return None;
        }
        let list = |s: &str| -> Vec<String> {
            if s == "-" {
                Vec::new()
            } else {
                s.split(',').map(str::to_string).collect()
            }
        };
        Some(CodeActionsRequest {
            id,
            doc: num(fields, 0)?,
            version: num(fields, 1)?,
            range: range_at(fields, 2)?,
            trigger: fields[6].clone(),
            kinds: list(&fields[7]),
            diagnostics: list(&fields[8]),
        })
    }

    /// Whether an action of that kind matches the kinds asked: it equals or descends from one
    /// (`source.organizeImports.go` matches `source.organizeImports`), or none were asked. rhun
    /// drops actions that do not; the crate leaves them out before it answers.
    pub fn wants(&self, kind: &str) -> bool {
        self.kinds.is_empty()
            || self.kinds.iter().any(|k| {
                kind == k
                    || kind
                        .strip_prefix(k.as_str())
                        .is_some_and(|rest| rest.starts_with('.'))
            })
    }
}

/// A reference to one of the extension's own commands (feature `command-args`): its local name
/// (1 to 40 of `[a-z0-9_]`, without the extension's prefix) and args rhun hands back verbatim as
/// the body of `0 run EXT.NAME DOC ref LEN` (at most [`MAX_ARGS`] bytes).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct CommandRef {
    pub name: String,
    pub args: Vec<u8>,
}

impl CommandRef {
    pub fn new(name: &str, args: &[u8]) -> CommandRef {
        CommandRef {
            name: name.to_string(),
            args: args.to_vec(),
        }
    }

    /// The record `command NAME ARGS_LEN` and the args; nothing for a name rhun does not take or
    /// args over [`MAX_ARGS`] (rhun would refuse the whole message).
    pub fn encode(&self) -> Vec<u8> {
        let name_ok = !self.name.is_empty()
            && self.name.len() <= 40
            && self
                .name
                .bytes()
                .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'_');
        if !name_ok || self.args.len() > MAX_ARGS {
            return Vec::new();
        }
        let mut out = format!("command {} {}\n", self.name, self.args.len()).into_bytes();
        out.extend_from_slice(&self.args);
        out
    }
}

/// A 1.1 code action (feature `code-actions`): chosen, its `edit` is applied, else its `resolve`
/// data is handed back in a code-action-resolve request ([`crate::Handler::code_action_resolve`]);
/// then its `command` runs. A disabled action is listed last with its reason.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Action {
    /// Hierarchical: `quickfix`, `refactor.extract`, `source.organizeImports` (empty is
    /// `quickfix`; cut at [`MAX_ACTION_KIND`] bytes).
    pub kind: String,
    /// One line; an action without one is left out.
    pub title: String,
    pub edit: Option<WorkspaceEdit>,
    /// The one Auto Fix applies at once.
    pub preferred: bool,
    /// Why it cannot apply now (one line, cut at [`MAX_REASON`] bytes); empty when it can.
    pub disabled: String,
    /// Data for code-action-resolve, at most [`MAX_ACTION_RESOLVE`] bytes (else left out).
    pub resolve: Vec<u8>,
    pub command: Option<CommandRef>,
    /// The ids of the diagnostics it fixes.
    pub fixes: Vec<String>,
}

impl Action {
    pub fn new(kind: &str, title: &str) -> Action {
        Action {
            kind: kind.to_string(),
            title: title.to_string(),
            ..Default::default()
        }
    }

    pub fn edit(mut self, edit: WorkspaceEdit) -> Action {
        self.edit = Some(edit);
        self
    }

    pub fn preferred(mut self) -> Action {
        self.preferred = true;
        self
    }

    pub fn disabled(mut self, reason: &str) -> Action {
        self.disabled = reason.to_string();
        self
    }

    pub fn resolve(mut self, data: &[u8]) -> Action {
        self.resolve = data.to_vec();
        self
    }

    pub fn command(mut self, command: CommandRef) -> Action {
        self.command = Some(command);
        self
    }

    pub fn fixes(mut self, ids: &[&str]) -> Action {
        self.fixes = ids.iter().map(|s| s.to_string()).collect();
        self
    }

    /// The kind as rhun reads it: without white space and control characters, at most
    /// [`MAX_ACTION_KIND`] bytes, `quickfix` when nothing is left.
    pub fn kind_word(&self) -> String {
        let w: String = self
            .kind
            .chars()
            .filter(|c| !c.is_whitespace() && !c.is_control())
            .collect();
        let w = field(&w, MAX_ACTION_KIND);
        if w.is_empty() {
            "quickfix".to_string()
        } else {
            w
        }
    }

    /// Its record (header, title, edit, extra) for a rhun that agreed `caps`, or nothing when it
    /// is left out: no title, an edit that cannot be encoded (or needs a feature rhun did not
    /// agree), or nothing to do (no edit, resolve data or command, and not disabled).
    fn encode(&self, caps: EditCaps) -> Option<Vec<u8>> {
        let title = field(&self.title, MAX_TEXT);
        if title.is_empty() {
            return None;
        }
        let edit = match &self.edit {
            Some(e) => e.encode_with(caps).ok()?,
            None => Vec::new(),
        };
        let mut extra = Vec::new();
        if self.preferred {
            extra.extend_from_slice(b"preferred 0\n");
        }
        if !self.disabled.is_empty() {
            let d = field(&self.disabled, MAX_REASON);
            extra.extend_from_slice(format!("disabled {}\n{d}", d.len()).as_bytes());
        }
        let resolve = !self.resolve.is_empty() && self.resolve.len() <= MAX_ACTION_RESOLVE;
        if resolve {
            extra.extend_from_slice(format!("resolve {}\n", self.resolve.len()).as_bytes());
            extra.extend_from_slice(&self.resolve);
        }
        let cmd = self
            .command
            .as_ref()
            .map(CommandRef::encode)
            .unwrap_or_default();
        extra.extend_from_slice(&cmd);
        if !self.fixes.is_empty() {
            let f = self.fixes.join(" ");
            extra.extend_from_slice(format!("fixes {}\n{f}", f.len()).as_bytes());
        }
        if edit.is_empty() && !resolve && cmd.is_empty() && self.disabled.is_empty() {
            return None;
        }
        let mut out = format!(
            "{} {} {} {}\n",
            self.kind_word(),
            title.len(),
            edit.len(),
            extra.len()
        )
        .into_bytes();
        out.extend_from_slice(title.as_bytes());
        out.extend_from_slice(&edit);
        out.extend_from_slice(&extra);
        Some(out)
    }
}

/// The code-actions answer, the edits in the grammar of a rhun that agreed `workspace-edits` and
/// `file-operations` (a handler's answer follows what was agreed). Actions that are left out (see
/// [`Action`]) are skipped, and those past [`MAX_ACTIONS`] or [`MAX_ACTIONS_BODY`] are left out.
pub fn encode_code_actions(actions: &[Action]) -> Vec<u8> {
    encode_code_actions_with(actions, EditCaps::ALL)
}

/// [`encode_code_actions`] for a rhun that agreed `caps`: an action whose edit needs a feature it
/// did not agree is left out (rhun would refuse the edit when it is chosen).
pub(crate) fn encode_code_actions_with(actions: &[Action], caps: EditCaps) -> Vec<u8> {
    let mut recs = Vec::new();
    let mut n = 0;
    for a in actions {
        let Some(one) = a.encode(caps) else { continue };
        if n == MAX_ACTIONS || recs.len() + one.len() + 16 > MAX_ACTIONS_BODY {
            break;
        }
        recs.extend_from_slice(&one);
        n += 1;
    }
    let mut out = format!("{n}\n").into_bytes();
    out.extend_from_slice(&recs);
    out
}

/// `ID code-action-resolve DOC VERSION LEN` and the chosen action's resolve data.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ActionResolveRequest {
    /// rhun's id for it: a `cancel` event names it.
    pub id: u64,
    pub doc: Doc,
    pub version: i64,
    pub data: Vec<u8>,
}

impl ActionResolveRequest {
    pub fn parse(id: u64, fields: &[String], body: &[u8]) -> Option<ActionResolveRequest> {
        if fields.len() != 3 || fields[2].parse::<usize>().ok()? != body.len() {
            return None;
        }
        Some(ActionResolveRequest {
            id,
            doc: num(fields, 0)?,
            version: num(fields, 1)?,
            data: body.to_vec(),
        })
    }
}

/// The rest of a chosen action (the code-action-resolve answer): its edit, and the command to run
/// after it (the action's own runs when this has none).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ResolvedAction {
    pub edit: Option<WorkspaceEdit>,
    pub command: Option<CommandRef>,
}

impl ResolvedAction {
    /// The answer: `EDIT_LEN EXTRA_LEN` LF, the edit, the command record; or why the edit cannot
    /// be sent (past a workspace edit's limits). The edit is in the grammar of a rhun that agreed
    /// `workspace-edits` and `file-operations` (a handler's answer follows what was agreed).
    pub fn encode(&self) -> Result<Vec<u8>, String> {
        self.encode_with(EditCaps::ALL).map_err(|e| e.1)
    }

    /// [`ResolvedAction::encode`] for a rhun that agreed `caps`; errors carry the code to answer
    /// with (`toobig`, or `unknown` for an edit that needs a feature it did not agree).
    pub(crate) fn encode_with(&self, caps: EditCaps) -> Result<Vec<u8>, (&'static str, String)> {
        let edit = match &self.edit {
            Some(e) => e.encode_with(caps)?,
            None => Vec::new(),
        };
        let cmd = self
            .command
            .as_ref()
            .map(CommandRef::encode)
            .unwrap_or_default();
        let mut out = format!("{} {}\n", edit.len(), cmd.len()).into_bytes();
        out.extend_from_slice(&edit);
        out.extend_from_slice(&cmd);
        Ok(out)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::TextEdit;

    fn fields(s: &str) -> Vec<String> {
        s.split(' ').map(str::to_string).collect()
    }

    fn r(l1: u32, c1: u32, l2: u32, c2: u32) -> Range {
        Range::new(Pos::new(l1, c1), Pos::new(l2, c2))
    }

    #[test]
    fn requests_parse() {
        assert_eq!(
            RangeFormatRequest::parse(&fields("4 7 1 1 2 1 4 yes yes no")),
            Some(RangeFormatRequest {
                doc: 4,
                version: 7,
                range: r(1, 1, 2, 1),
                tab_width: 4,
                spaces: true,
                trim: true,
                final_newline: false,
            })
        );
        for bad in [
            "4 7 1 1 2 1 0 yes yes no",
            "4 7 1 1 2 1 4 yes maybe no",
            "4 7 1 1 2 1 4 yes yes",
            "4 7 1 1 2 x 4 yes yes no",
        ] {
            assert!(RangeFormatRequest::parse(&fields(bad)).is_none(), "{bad}");
        }
        let t = TypeFormatRequest::parse(&fields("4 7 3 5 newline 4 yes no yes")).unwrap();
        assert_eq!(
            (
                t.at,
                t.ch.as_str(),
                t.tab_width,
                t.spaces,
                t.trim,
                t.final_newline
            ),
            (Pos::new(3, 5), "newline", 4, true, false, true)
        );
        assert!(TypeFormatRequest::parse(&fields("4 7 3 5  4 yes no yes")).is_none());
        let c = CodeActionsRequest::parse(9, &fields("4 7 1 1 1 3 auto quickfix,refactor d1,d2"))
            .unwrap();
        assert_eq!(
            (c.id, c.range, c.trigger.as_str()),
            (9, r(1, 1, 1, 3), "auto")
        );
        assert_eq!((c.kinds.len(), c.diagnostics.len()), (2, 2));
        let any = CodeActionsRequest::parse(9, &fields("4 7 1 1 1 3 invoked - -")).unwrap();
        assert!(any.kinds.is_empty() && any.diagnostics.is_empty());
        assert!(CodeActionsRequest::parse(9, &fields("4 7 1 1 1 3 later - -")).is_none());
        let res = ActionResolveRequest::parse(3, &fields("4 7 1"), b"R").unwrap();
        assert_eq!(
            (res.doc, res.version, res.data.as_slice()),
            (4, 7, &b"R"[..])
        );
        assert!(ActionResolveRequest::parse(3, &fields("4 7 2"), b"R").is_none());
    }

    #[test]
    fn kinds_match_by_hierarchy() {
        let req = CodeActionsRequest::parse(
            1,
            &fields("1 1 1 1 1 1 invoked source.organizeImports,quickfix -"),
        )
        .unwrap();
        assert!(req.wants("quickfix"));
        assert!(req.wants("source.organizeImports.go"));
        assert!(!req.wants("source.organizeImportsX"));
        assert!(!req.wants("source"));
        assert!(!req.wants("refactor"));
    }

    #[test]
    fn prepared_renames_are_one_line_of_at_most_200_bytes() {
        assert_eq!(
            PreparedRename::new(r(2, 9, 2, 14), "total").encode(),
            b"2 9 2 14 5\ntotal".to_vec()
        );
        let long = format!("a\n{}é", "x".repeat(197));
        let body = PreparedRename::new(r(1, 1, 1, 2), &long).encode();
        assert_eq!(
            body,
            format!("1 1 1 2 199\na {}", "x".repeat(197)).into_bytes()
        );
    }

    // go/rhunext/actions_test.go: the same bytes
    #[test]
    fn code_actions_are_framed_as_the_go_package_frames_them() {
        let acts = [
            Action::new("quickfix", "Fix")
                .preferred()
                .resolve(b"R")
                .fixes(&["d1"]),
            Action::new("refactor.extract", "Extract").command(CommandRef::new("extract", b"a b")),
            Action::new("quickfix", "Cannot").disabled("No fix here"),
            Action::new("quickfix", "Nothing"),
        ];
        assert_eq!(
            String::from_utf8(encode_code_actions(&acts)).unwrap(),
            "3\nquickfix 3 0 33\nFixpreferred 0\nresolve 1\nRfixes 2\nd1refactor.extract 7 0 21\n\
             Extractcommand extract 3\na bquickfix 6 0 23\nCannotdisabled 11\nNo fix here"
        );
        let edit = WorkspaceEdit::new().doc(4, 7, vec![TextEdit::new(r(1, 1, 1, 1), "R")]);
        let res = ResolvedAction {
            edit: Some(edit.clone()),
            command: Some(CommandRef::new("fix", b"z")),
        };
        let e = edit.encode().unwrap();
        let mut want = format!("{} 15\n", e.len()).into_bytes();
        want.extend_from_slice(&e);
        want.extend_from_slice(b"command fix 1\nz");
        assert_eq!(res.encode().unwrap(), want);
        assert_eq!(
            ResolvedAction::default().encode().unwrap(),
            b"0 0\n".to_vec()
        );
        let one = encode_code_actions(&[Action::new("", "E").edit(edit)]);
        assert!(one.starts_with(format!("1\nquickfix 1 {} 0\nE", e.len()).as_bytes()));
    }

    #[test]
    fn what_rhun_would_refuse_is_left_out() {
        // a bad command name or args over 8192 bytes: no record, so an action doing nothing else
        // is left out
        for c in [
            CommandRef::new("", b""),
            CommandRef::new("Fix", b""),
            CommandRef::new("fix-it", b""),
            CommandRef::new(&"x".repeat(41), b""),
            CommandRef::new("fix", &[b'a'; MAX_ARGS + 1]),
        ] {
            assert!(c.encode().is_empty(), "{c:?}");
            assert_eq!(
                encode_code_actions(&[Action::new("quickfix", "T").command(c)]),
                b"0\n"
            );
        }
        assert_eq!(
            CommandRef::new("fix_2", &[b'a'; MAX_ARGS]).encode().len(),
            "command fix_2 8192\n".len() + MAX_ARGS
        );
        // resolve data over 16 KiB is left out, and with it the action
        let big = Action::new("quickfix", "Big").resolve(&[0; MAX_ACTION_RESOLVE + 1]);
        assert_eq!(encode_code_actions(&[big]), b"0\n");
        // no title, a reason over 1000 bytes, a kind over 128 bytes
        let reason = format!("{}é", "r".repeat(999));
        let body = encode_code_actions(&[
            Action::new("quickfix", "").disabled("x"),
            Action::new(&format!("refactor.{}", "k".repeat(200)), "Long").disabled(&reason),
        ]);
        let kind = format!("refactor.{}", "k".repeat(119));
        let want = format!("1\n{kind} 4 0 1012\nLongdisabled 999\n{}", "r".repeat(999));
        assert_eq!(String::from_utf8(body).unwrap(), want);
        // past 1000 actions the rest are left out
        let many: Vec<Action> = (0..1001)
            .map(|_| Action::new("quickfix", "T").disabled("x"))
            .collect();
        assert!(encode_code_actions(&many).starts_with(b"1000\n"));
        // an edit past the limits leaves the action out, and is no resolve answer
        let huge =
            WorkspaceEdit::new().doc(1, 1, vec![TextEdit::default(); crate::lang::MAX_EDITS + 1]);
        assert_eq!(
            encode_code_actions(&[Action::new("quickfix", "H").edit(huge.clone())]),
            b"0\n"
        );
        assert!(ResolvedAction {
            edit: Some(huge),
            command: None
        }
        .encode()
        .is_err());
    }
}
