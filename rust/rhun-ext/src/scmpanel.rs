//! Version control panels (protocol 1.1 feature `scm`): an extension registers a repository
//! root and fills a sidebar section for it with a status line, an optional message box, buttons
//! and groups of changed files. rhun colors those files in the explorer and the tab titles, and
//! the extension's quick-diff bases (feature `quickdiff`) apply under its roots.
use std::fmt::Write as _;

use crate::{Error, Ext, Feature};

/// The status line, the placeholder and labels: at most this many bytes.
pub const SCM_TEXT_MAX: usize = 200;
/// Buttons of a panel.
pub const SCM_ACTIONS_MAX: usize = 4;
/// Actions of one group or resource (its menu).
pub const SCM_ITEM_ACTIONS_MAX: usize = 8;
pub const SCM_GROUPS_MAX: usize = 64;
/// Resources of one update, in all groups.
pub const SCM_RESOURCES_MAX: usize = 10_000;
/// The message box's text.
pub const SCM_INPUT_MAX: usize = 65_536;
/// An update's body.
pub const SCM_BODY_MAX: usize = 4 << 20;
/// A resource's tooltip.
pub const SCM_TIP_MAX: usize = 1000;

/// A command of the extension (its local name) with an icon (a tree icon name such as `check`,
/// or empty for none; a group's or resource's action with an icon is also a button on its row
/// while the row is hovered), ARGS (the body of `0 run EXT.NAME DOC ref LEN` with command-args agreed)
/// and a label (empty: the command's title).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ScmAction {
    pub icon: String,
    pub command: String,
    pub args: Vec<u8>,
    pub label: String,
}

impl ScmAction {
    pub fn new(command: &str, label: &str) -> ScmAction {
        ScmAction {
            command: command.into(),
            label: label.into(),
            ..Default::default()
        }
    }
    pub fn icon(mut self, icon: &str) -> ScmAction {
        self.icon = icon.into();
        self
    }
    pub fn args(mut self, args: &[u8]) -> ScmAction {
        self.args = args.to_vec();
        self
    }
}

/// A changed file: its path relative to the root, a status letter (`M`, `A`, `D`, `?`, …), a
/// color (`added`, `modified`, `deleted`, `conflict`, `untracked`, `ignored`, or empty), the
/// command its click runs (`None`: a diff of the file against its quick-diff base, else the
/// file), its menu's actions, and a tooltip (at most
/// 1000 bytes).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ScmResource {
    pub path: String,
    pub code: u8,
    pub color: String,
    pub click: Option<(String, Vec<u8>)>,
    pub actions: Vec<ScmAction>,
    pub tip: String,
}

/// A titled group of resources with actions in its menu.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ScmGroup {
    pub id: String,
    pub label: String,
    pub actions: Vec<ScmAction>,
    pub resources: Vec<ScmResource>,
}

/// What an `scm-update` shows: a status line, a message box when `placeholder` is not empty, up
/// to four buttons (the first also runs on Ctrl+Enter in the message box), and groups.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ScmPanel {
    pub status: String,
    pub placeholder: String,
    pub actions: Vec<ScmAction>,
    pub groups: Vec<ScmGroup>,
}

fn text(what: &str, s: &str, max: usize) -> Result<(), String> {
    if s.len() > max {
        return Err(format!("{what}: {} bytes (at most {max})", s.len()));
    }
    if s.bytes().any(|c| c == 0 || c == b'\r' || c == b'\n') {
        return Err(format!("{what}: NUL, CR or LF"));
    }
    Ok(())
}

fn name_ok(n: &str) -> bool {
    !n.is_empty()
        && n.len() <= 40
        && n.bytes()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == b'_')
}

fn word(what: &str, w: &str) -> Result<(), String> {
    if w.is_empty() || w.len() > 64 || w.bytes().any(|c| c <= b' ') {
        return Err(format!("{what} {w:?}: 1 to 64 bytes without spaces"));
    }
    Ok(())
}

fn action(out: &mut Vec<u8>, a: &ScmAction) -> Result<(), String> {
    if !name_ok(&a.command) {
        return Err(format!("command {:?}: 1 to 40 of a-z 0-9 _", a.command));
    }
    if a.args.len() > 8192 {
        return Err(format!("command {}: ARGS over 8192 bytes", a.command));
    }
    text("label", &a.label, SCM_TEXT_MAX)?;
    let icon = if a.icon.is_empty() { "-" } else { &a.icon };
    word("icon", icon)?;
    let mut h = String::new();
    let _ = writeln!(
        h,
        "action {icon} {} {} {}",
        a.command,
        a.args.len(),
        a.label.len()
    );
    out.extend_from_slice(h.as_bytes());
    out.extend_from_slice(&a.args);
    out.extend_from_slice(a.label.as_bytes());
    Ok(())
}

const COLORS: [&str; 6] = [
    "added",
    "modified",
    "deleted",
    "conflict",
    "untracked",
    "ignored",
];

impl ScmPanel {
    /// The `scm-update` body, checked against rhun's limits.
    pub fn encode(&self) -> Result<Vec<u8>, String> {
        text("status", &self.status, SCM_TEXT_MAX)?;
        text("placeholder", &self.placeholder, SCM_TEXT_MAX)?;
        if self.actions.len() > SCM_ACTIONS_MAX {
            return Err(format!(
                "{} buttons (at most {SCM_ACTIONS_MAX})",
                self.actions.len()
            ));
        }
        if self.groups.len() > SCM_GROUPS_MAX {
            return Err(format!(
                "{} groups (at most {SCM_GROUPS_MAX})",
                self.groups.len()
            ));
        }
        let mut out = format!(
            "{} {} {} {}\n{}{}",
            self.status.len(),
            self.placeholder.len(),
            self.actions.len(),
            self.groups.len(),
            self.status,
            self.placeholder
        )
        .into_bytes();
        for a in &self.actions {
            action(&mut out, a)?;
        }
        let mut nres = 0;
        for g in &self.groups {
            word("group id", &g.id)?;
            text("group label", &g.label, SCM_TEXT_MAX)?;
            if g.actions.len() > SCM_ITEM_ACTIONS_MAX {
                return Err(format!("group {}: {} actions", g.id, g.actions.len()));
            }
            out.extend_from_slice(
                format!(
                    "group {} {} {} {}\n{}",
                    g.id,
                    g.label.len(),
                    g.actions.len(),
                    g.resources.len(),
                    g.label
                )
                .as_bytes(),
            );
            for a in &g.actions {
                action(&mut out, a)?;
            }
            for r in &g.resources {
                nres += 1;
                if nres > SCM_RESOURCES_MAX {
                    return Err(format!("more than {SCM_RESOURCES_MAX} resources"));
                }
                if r.path.is_empty()
                    || r.path.len() > 4095
                    || r.path.bytes().any(|c| c == 0 || c == b'\r' || c == b'\n')
                {
                    return Err(format!("resource path {:?}", r.path));
                }
                if !(0x21..=0x7e).contains(&r.code) {
                    return Err(format!("resource {}: status letter {}", r.path, r.code));
                }
                let color = if r.color.is_empty() {
                    "-"
                } else {
                    r.color.as_str()
                };
                if color != "-" && !COLORS.contains(&color) {
                    return Err(format!("resource {}: color {color:?}", r.path));
                }
                if r.actions.len() > SCM_ITEM_ACTIONS_MAX {
                    return Err(format!("resource {}: {} actions", r.path, r.actions.len()));
                }
                if r.tip.len() > SCM_TIP_MAX {
                    return Err(format!(
                        "resource {}: a tooltip of {} bytes",
                        r.path,
                        r.tip.len()
                    ));
                }
                let nrec =
                    r.actions.len() + r.click.is_some() as usize + !r.tip.is_empty() as usize;
                out.extend_from_slice(
                    format!(
                        "res {} {color} {} {nrec}\n{}",
                        r.code as char,
                        r.path.len(),
                        r.path
                    )
                    .as_bytes(),
                );
                if let Some((cmd, args)) = &r.click {
                    if !name_ok(cmd) || args.len() > 8192 {
                        return Err(format!("resource {}: click command {cmd:?}", r.path));
                    }
                    out.extend_from_slice(format!("command {cmd} {}\n", args.len()).as_bytes());
                    out.extend_from_slice(args);
                }
                for a in &r.actions {
                    action(&mut out, a)?;
                }
                if !r.tip.is_empty() {
                    out.extend_from_slice(format!("tip {}\n", r.tip.len()).as_bytes());
                    out.extend_from_slice(r.tip.as_bytes());
                }
            }
        }
        if out.len() > SCM_BODY_MAX {
            return Err(format!("an update of {} bytes", out.len()));
        }
        Ok(out)
    }
}

fn syntax(text: String) -> Error {
    Error {
        code: "syntax".into(),
        text,
    }
}

impl Ext {
    fn scm_feature(&self) -> Result<(), Error> {
        if self.has(Feature::Scm) {
            Ok(())
        } else {
            Err(Error {
                code: "unknown".into(),
                text: "scm was not negotiated".into(),
            })
        }
    }

    /// Adds a sidebar section titled `label` for the repository at `root` (absolute) and returns
    /// its handle; the same root again returns the same handle. At most 4 per extension and 16
    /// in all.
    pub fn scm_register(&self, root: &str, label: &str) -> Result<u64, Error> {
        self.scm_feature()?;
        if root.is_empty() || label.is_empty() {
            return Err(syntax("a root and a label".into()));
        }
        text("label", label, SCM_TEXT_MAX).map_err(syntax)?;
        let body = format!("{} {}\n{root}{label}", root.len(), label.len());
        let r = self.request("scm-register", Some(body.as_bytes()))?;
        r.fields
            .first()
            .and_then(|v| v.parse().ok())
            .filter(|&h: &u64| h > 0)
            .ok_or_else(|| syntax("invalid scm-register reply".into()))
    }

    /// Replaces what the provider's section shows.
    pub fn scm_update(&self, scm: u64, panel: &ScmPanel) -> Result<(), Error> {
        self.scm_feature()?;
        let body = panel.encode().map_err(syntax)?;
        self.request(&format!("scm-update {scm}"), Some(&body))
            .map(|_| ())
    }

    /// The text of the provider's message box.
    pub fn scm_input(&self, scm: u64) -> Result<String, Error> {
        self.scm_feature()?;
        let r = self.request(&format!("scm-input {scm}"), None)?;
        Ok(String::from_utf8_lossy(&r.body).into_owned())
    }

    /// Puts text in the provider's message box (empty clears it).
    pub fn scm_input_set(&self, scm: u64, text: &str) -> Result<(), Error> {
        self.scm_feature()?;
        if text.len() > SCM_INPUT_MAX {
            return Err(Error {
                code: "toobig".into(),
                text: "at most 65536 bytes".into(),
            });
        }
        self.request(&format!("scm-input-set {scm}"), Some(text.as_bytes()))
            .map(|_| ())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn encodes_a_panel() {
        let p = ScmPanel {
            status: "default".into(),
            placeholder: "Message".into(),
            actions: vec![ScmAction::new("commit", "Commit").icon("check")],
            groups: vec![ScmGroup {
                id: "changes".into(),
                label: "Changes".into(),
                actions: vec![ScmAction::new("revert_all", "")],
                resources: vec![
                    ScmResource {
                        path: "a.txt".into(),
                        code: b'M',
                        color: "modified".into(),
                        click: Some(("open_change".into(), b"a.txt".to_vec())),
                        actions: vec![ScmAction::new("revert", "Revert").args(b"a.txt")],
                        tip: String::new(),
                    },
                    ScmResource {
                        path: "gone.txt".into(),
                        code: b'R',
                        tip: "renamed".into(),
                        ..Default::default()
                    },
                ],
            }],
        };
        let want = "7 7 1 1\ndefaultMessage\
                    action check commit 0 6\nCommit\
                    group changes 7 1 2\nChanges\
                    action - revert_all 0 0\n\
                    res M modified 5 2\na.txt\
                    command open_change 5\na.txt\
                    action - revert 5 6\na.txtRevert\
                    res R - 8 1\ngone.txt\
                    tip 7\nrenamed";
        assert_eq!(String::from_utf8(p.encode().unwrap()).unwrap(), want);
        let bad = [
            ScmPanel {
                status: "a\nb".into(),
                ..Default::default()
            },
            ScmPanel {
                actions: vec![ScmAction::new("x", ""); 5],
                ..Default::default()
            },
            ScmPanel {
                actions: vec![ScmAction::new("Bad", "")],
                ..Default::default()
            },
            ScmPanel {
                groups: vec![ScmGroup {
                    id: "g".into(),
                    resources: vec![ScmResource {
                        path: "a".into(),
                        code: b'M',
                        color: "pink".into(),
                        ..Default::default()
                    }],
                    ..Default::default()
                }],
                ..Default::default()
            },
            ScmPanel {
                groups: vec![ScmGroup {
                    id: "has space".into(),
                    ..Default::default()
                }],
                ..Default::default()
            },
        ];
        for (i, p) in bad.iter().enumerate() {
            assert!(p.encode().is_err(), "bad panel {i} accepted");
        }
    }
}
