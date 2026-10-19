//! Protocol 1.1: how commands are invoked and what they receive (feature `command-args`),
//! command references inside bodies, running other commands (`run-command`) and being run by
//! other extensions (`call`), context keys for when clauses (`context-keys`) and status bar items
//! (`status-items`). See `v1.1/v1.1.md`.

use crate::{Doc, Error, Ext, Feature};

/// A command reference's ARGS are at most this long.
pub const MAX_REF_ARGS: usize = 8192;
/// `run-command`'s and `call`'s ARGS are at most this long.
pub const MAX_RUN_ARGS: usize = 65536;
/// A call's result is at most this long.
pub const MAX_CALL_RESULT: usize = 1 << 20;
/// A context value is at most this long.
pub const MAX_CONTEXT_VALUE: usize = 256;

fn err(code: &str, text: &str) -> Error {
    Error {
        code: code.into(),
        text: text.into(),
    }
}

/// One invocation of one of the extension's commands: `0 run EXT.NAME DOC FROM LEN` and its
/// body. Without `command-args`, `from` is empty and `body` too.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Invocation {
    /// The full name, `EXT.NAME`.
    pub command: String,
    /// The local name, without `EXT.`.
    pub name: String,
    /// The document it is about, or 0.
    pub doc: Doc,
    /// `palette`, `key`, `menu`, `ref`, or empty without `command-args`.
    pub from: String,
    /// The binding's ARGS (`key`), `TARGET` LF the item's args (`menu`), the reference's ARGS
    /// (`ref`); empty from the palette.
    pub body: Vec<u8>,
}

impl Invocation {
    pub(crate) fn new(command: &str, doc: Doc, from: &str, body: Vec<u8>) -> Invocation {
        let name = command
            .split_once('.')
            .map(|(_, n)| n)
            .unwrap_or(command)
            .to_string();
        Invocation {
            command: command.to_string(),
            name,
            doc,
            from: from.to_string(),
            body,
        }
    }

    /// A menu invocation's target line and the item's args; `None` for other invocations.
    pub fn target(&self) -> Option<(MenuTarget, &[u8])> {
        if self.from != "menu" {
            return None;
        }
        let (line, args) = match self.body.iter().position(|&b| b == b'\n') {
            Some(i) => (&self.body[..i], &self.body[i + 1..]),
            None => (&self.body[..], &[][..]),
        };
        Some((MenuTarget::parse(&String::from_utf8_lossy(line)), args))
    }
}

/// A menu's target line: `kind` its first word (editor, file, folder, root, tab, terminal,
/// extension, view, item, commit, resource, group …) and `fields` the words after it, the last
/// holding the rest of the line (a path may have spaces).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct MenuTarget {
    pub kind: String,
    pub fields: Vec<String>,
    pub line: String,
}

impl MenuTarget {
    /// Reads a TARGET line; the number of fields depends on the kind.
    pub fn parse(line: &str) -> MenuTarget {
        let (kind, rest) = line.split_once(' ').unwrap_or((line, ""));
        let n = match kind {
            "editor" => 5,
            "tab" => match rest.split(' ').next().unwrap_or("") {
                "doc" => 3,
                "image" | "view" => 2,
                _ => 1,
            },
            "item" | "commit" | "resource" => 2,
            "extension" => 0,
            _ => 1,
        };
        let fields = if rest.is_empty() || n == 0 {
            Vec::new()
        } else {
            rest.splitn(n, ' ').map(str::to_string).collect()
        };
        MenuTarget {
            kind: kind.to_string(),
            fields,
            line: line.to_string(),
        }
    }

    /// The target's path: file, folder and root targets, `tab doc DOC PATH` (not `-`),
    /// `tab image PATH`, `commit SHA PATH`, `resource GROUP PATH`.
    pub fn path(&self) -> Option<&str> {
        let f = &self.fields;
        match (self.kind.as_str(), f.len()) {
            ("file" | "folder" | "root", 1) => Some(&f[0]),
            ("tab", 3) if f[0] == "doc" && f[2] != "-" => Some(&f[2]),
            ("tab", 2) if f[0] == "image" => Some(&f[1]),
            ("commit" | "resource", 2) => Some(&f[1]),
            _ => None,
        }
    }
}

/// Another extension running one of this extension's callable commands: `ID call EXT.NAME DOC
/// CALLER LEN` and ARGS. A `cancel` event names `id` (the caller's 10 seconds are up).
#[derive(Clone, Debug, Default)]
pub struct CallRequest {
    pub id: u64,
    /// The full name, `EXT.NAME`.
    pub command: String,
    /// The local name.
    pub name: String,
    /// The active document, or 0.
    pub doc: Doc,
    /// `OWNER/NAME` of the extension that called.
    pub caller: String,
    pub args: Vec<u8>,
}

impl CallRequest {
    pub(crate) fn parse(id: u64, f: &[String], args: Vec<u8>) -> Option<CallRequest> {
        if f.len() != 4 || !f[0].contains('.') || f[2].is_empty() {
            return None;
        }
        let doc: Doc = f[1].parse().ok().filter(|d: &Doc| *d >= 0)?;
        let inv = Invocation::new(&f[0], doc, "", Vec::new());
        Some(CallRequest {
            id,
            command: inv.command,
            name: inv.name,
            doc,
            caller: f[2].clone(),
            args,
        })
    }
}

/// A local command name: 1 to 40 of `[a-z0-9_]`.
pub fn valid_command_name(name: &str) -> bool {
    !name.is_empty()
        && name.len() <= 40
        && name
            .bytes()
            .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'_')
}

/// The record `command NAME LEN` LF ARGS: a reference to one of the extension's own commands
/// (local NAME) inside a body (code lens, tree items, status items …). When the user activates
/// it, rhun runs the command with FROM `ref` and the ARGS as the body.
pub fn command_ref(name: &str, args: &[u8]) -> Result<Vec<u8>, Error> {
    let mut out = Vec::new();
    push_command_ref(&mut out, name, args)?;
    Ok(out)
}

/// Appends [`command_ref`]'s record to `out`.
pub fn push_command_ref(out: &mut Vec<u8>, name: &str, args: &[u8]) -> Result<(), Error> {
    if !valid_command_name(name) {
        return Err(err("syntax", "a command name is 1 to 40 of a-z 0-9 _"));
    }
    if args.len() > MAX_REF_ARGS {
        return Err(err(
            "toobig",
            "a command reference's args are at most 8192 bytes",
        ));
    }
    out.extend_from_slice(format!("command {name} {}\n", args.len()).as_bytes());
    out.extend_from_slice(args);
    Ok(())
}

/// Whether `s` shows as it is on one line: no control characters (C0, DEL, C1), bidirectional
/// controls or zero-width characters, as rhun checks context values and status item texts.
pub fn plain_text(s: &str) -> bool {
    s.chars().all(|c| {
        let c = c as u32;
        !(c < 0x20
            || c == 0x7f
            || (0x80..0xa0).contains(&c)
            || (0x202a..=0x202e).contains(&c)
            || (0x2066..=0x2069).contains(&c)
            || (0x200b..=0x200f).contains(&c)
            || c == 0x2060
            || c == 0xfeff)
    })
}

/// A key an extension sets: a lowercase letter, then ASCII letters, digits and `_`, at most 40.
pub fn valid_context_key(key: &str) -> bool {
    let b = key.as_bytes();
    !b.is_empty()
        && b.len() <= 40
        && b[0].is_ascii_lowercase()
        && b[1..]
            .iter()
            .all(|c| c.is_ascii_alphanumeric() || *c == b'_')
}

/// A status bar item (feature `status-items`).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct StatusItem {
    /// 1 to 40 of `[a-z0-9_]`.
    pub name: String,
    /// `left` or `right` (empty: right).
    pub align: String,
    /// -1000 to 1000, higher further left on both sides.
    pub priority: i32,
    /// One line, at most 120 bytes; `$(NAME)` is an icon (a built-in or `ext:NAME`).
    pub text: String,
    /// At most 500 bytes, may hold line breaks.
    pub tooltip: String,
    /// One of the extension's own commands (local name), run with FROM `ref`; empty for none.
    pub command: String,
    pub args: Vec<u8>,
    /// fg, muted (empty), accent, error, warning, success.
    pub color: String,
    /// Empty, error or warning.
    pub background: String,
    /// A when clause; empty always.
    pub when: String,
}

impl StatusItem {
    /// The `status-item` body: `ALIGN PRIORITY TEXT_LEN TOOLTIP_LEN EXTRA_LEN` LF and the bytes.
    pub fn encode(&self) -> Result<Vec<u8>, Error> {
        let align = if self.align.is_empty() {
            "right"
        } else {
            self.align.as_str()
        };
        if !valid_command_name(&self.name) {
            return Err(err(
                "syntax",
                "a status item's name is 1 to 40 of a-z 0-9 _",
            ));
        }
        if align != "left" && align != "right" {
            return Err(err("syntax", "align is left or right"));
        }
        if !(-1000..=1000).contains(&self.priority) {
            return Err(err("syntax", "priority is -1000 to 1000"));
        }
        if self.text.len() > 120 || !plain_text(&self.text) {
            return Err(err("syntax", "text is one line of at most 120 bytes"));
        }
        if self.tooltip.len() > 500 {
            return Err(err("syntax", "a tooltip is at most 500 bytes"));
        }
        if !self.color.is_empty()
            && !["fg", "muted", "accent", "error", "warning", "success"]
                .contains(&self.color.as_str())
        {
            return Err(err(
                "syntax",
                "color is fg, muted, accent, error, warning or success",
            ));
        }
        if !self.background.is_empty() && self.background != "error" && self.background != "warning"
        {
            return Err(err("syntax", "background is error or warning"));
        }
        if self.when.len() > 512 || self.when.contains(['\r', '\n']) {
            return Err(err(
                "syntax",
                "a when clause is one line of at most 512 bytes",
            ));
        }
        let mut extra = Vec::new();
        if !self.command.is_empty() {
            push_command_ref(&mut extra, &self.command, &self.args)?;
        }
        if !self.color.is_empty() {
            extra.extend_from_slice(format!("color {} 0\n", self.color).as_bytes());
        }
        if !self.background.is_empty() {
            extra.extend_from_slice(format!("background {} 0\n", self.background).as_bytes());
        }
        if !self.when.is_empty() {
            extra.extend_from_slice(format!("when {}\n{}", self.when.len(), self.when).as_bytes());
        }
        let mut body = format!(
            "{align} {} {} {} {}\n",
            self.priority,
            self.text.len(),
            self.tooltip.len(),
            extra.len()
        )
        .into_bytes();
        body.extend_from_slice(self.text.as_bytes());
        body.extend_from_slice(self.tooltip.as_bytes());
        body.extend_from_slice(&extra);
        Ok(body)
    }
}

impl Ext {
    /// Runs a rhun built-in (`goto_line`, `rhun.find`) or another extension's callable command
    /// (`OWNER/EXT.NAME`, `EXT.NAME`) with ARGS and returns its result (always empty for a
    /// built-in). Requires agreed `command-args`.
    pub fn run_command(&self, name: &str, args: &[u8]) -> Result<Vec<u8>, Error> {
        if !self.has(Feature::CommandArgs) {
            return Err(err("unknown", "command-args was not agreed"));
        }
        if name.is_empty() || name.len() > 200 || name.contains([' ', '\r', '\n']) {
            return Err(err("syntax", "not a command name"));
        }
        if args.len() > MAX_RUN_ARGS {
            return Err(err("toobig", "run-command args are at most 65536 bytes"));
        }
        self.request(&format!("run-command {name}"), Some(args))
            .map(|r| r.body)
    }

    /// Sets one of the extension's context keys (its when clauses see it as `OWNER/NAME.KEY`):
    /// one line of at most 256 bytes. Requires agreed `context-keys`.
    pub fn set_context(&self, key: &str, value: &str) -> Result<(), Error> {
        if value == "-" {
            return Err(err("syntax", "\"-\" removes a key: use remove_context"));
        }
        self.context_request(key, value)
    }

    /// Removes the value set at run time: the manifest's `[context]` default applies again.
    pub fn remove_context(&self, key: &str) -> Result<(), Error> {
        self.context_request(key, "-")
    }

    fn context_request(&self, key: &str, value: &str) -> Result<(), Error> {
        if !self.has(Feature::ContextKeys) {
            return Err(err("unknown", "context-keys was not agreed"));
        }
        if !valid_context_key(key) {
            return Err(err(
                "syntax",
                "a context key is a lowercase letter, then letters, digits and _, at most 40 bytes",
            ));
        }
        if value.len() > MAX_CONTEXT_VALUE {
            return Err(err("toobig", "a context value is at most 256 bytes"));
        }
        if value.is_empty() || value.starts_with(' ') || !plain_text(value) {
            return Err(err("syntax", "a context value is one line of text"));
        }
        self.request(&format!("set-context {key} {value}"), None)
            .map(|_| ())
    }

    /// Adds or replaces a status bar item; an extension has at most 8 (manifest items
    /// included). Requires agreed `status-items`.
    pub fn set_status_item(&self, item: &StatusItem) -> Result<(), Error> {
        if !self.has(Feature::StatusItems) {
            return Err(err("unknown", "status-items was not agreed"));
        }
        let body = item.encode()?;
        self.request(&format!("status-item {}", item.name), Some(&body))
            .map(|_| ())
    }

    /// Removes a status item (a manifest item is hidden until the program stops).
    pub fn remove_status_item(&self, name: &str) -> Result<(), Error> {
        if !self.has(Feature::StatusItems) {
            return Err(err("unknown", "status-items was not agreed"));
        }
        if !valid_command_name(name) {
            return Err(err(
                "syntax",
                "a status item's name is 1 to 40 of a-z 0-9 _",
            ));
        }
        self.request(&format!("status-remove {name}"), None)
            .map(|_| ())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn targets() {
        let t = MenuTarget::parse("tab doc 2 /p/a b.txt");
        assert_eq!(t.kind, "tab");
        assert_eq!(t.path(), Some("/p/a b.txt"));
        assert_eq!(MenuTarget::parse("tab doc 3 -").path(), None);
        assert_eq!(MenuTarget::parse("file /a b").path(), Some("/a b"));
        assert_eq!(
            MenuTarget::parse("commit abc /p/f g").path(),
            Some("/p/f g")
        );
        let item = MenuTarget::parse("item alice/demo.files src/lib x");
        assert_eq!(item.fields, vec!["alice/demo.files", "src/lib x"]);
        assert!(MenuTarget::parse("extension").fields.is_empty());
        let inv = Invocation::new("demo.copy", 2, "menu", b"root /p\nextra".to_vec());
        let (t, args) = inv.target().unwrap();
        assert_eq!(
            (t.kind.as_str(), t.path(), args),
            ("root", Some("/p"), &b"extra"[..])
        );
        assert_eq!(inv.name, "copy");
        assert!(Invocation::new("demo.x", 0, "key", Vec::new())
            .target()
            .is_none());
    }

    #[test]
    fn refs_and_names() {
        assert_eq!(
            command_ref("run_task", b"all\nx").unwrap(),
            b"command run_task 5\nall\nx"
        );
        for bad in ["", "Run", "a.b", "x y", &"a".repeat(41)] {
            assert!(command_ref(bad, b"").is_err(), "{bad}");
        }
        assert!(command_ref("a", &vec![0; MAX_REF_ARGS + 1]).is_err());
        assert!(valid_context_key("mode") && valid_context_key("aB_1"));
        for bad in ["Mode", "1a", "a.b", "", &"a".repeat(41)] {
            assert!(!valid_context_key(bad), "{bad}");
        }
        assert!(plain_text("é ok") && !plain_text("a\tb") && !plain_text("\u{202e}x"));
    }

    #[test]
    fn status_items() {
        let it = StatusItem {
            name: "build".into(),
            align: "left".into(),
            priority: 30,
            text: "$(gear) Build".into(),
            tooltip: "Builds it".into(),
            command: "run_task".into(),
            args: b"all".to_vec(),
            color: "accent".into(),
            background: "error".into(),
            when: "projectOpen".into(),
        };
        assert_eq!(
            String::from_utf8(it.encode().unwrap()).unwrap(),
            "left 30 13 9 75\n$(gear) BuildBuilds itcommand run_task 3\nallcolor accent 0\nbackground error 0\nwhen 11\nprojectOpen"
        );
        let plain = StatusItem {
            name: "x".into(),
            text: "x".into(),
            ..Default::default()
        };
        assert_eq!(plain.encode().unwrap(), b"right 0 1 0 0\nx");
        for bad in [
            StatusItem {
                name: "X".into(),
                ..Default::default()
            },
            StatusItem {
                name: "a".into(),
                align: "up".into(),
                ..Default::default()
            },
            StatusItem {
                name: "a".into(),
                priority: 1001,
                ..Default::default()
            },
            StatusItem {
                name: "a".into(),
                text: "x".repeat(121),
                ..Default::default()
            },
            StatusItem {
                name: "a".into(),
                color: "pink".into(),
                ..Default::default()
            },
            StatusItem {
                name: "a".into(),
                background: "accent".into(),
                ..Default::default()
            },
            StatusItem {
                name: "a".into(),
                command: "Bad".into(),
                ..Default::default()
            },
        ] {
            assert!(bad.encode().is_err(), "{bad:?}");
        }
    }
}
