//! When clauses (`v1.1/v1.1.md` "When clauses"): [`check`] compiles a clause as rhun does and
//! reports the first error with its byte offset, so a bad clause shows before rhun drops the item
//! it guards. The grammar, limits, messages and offsets are rhun's; `v1.1/when-vectors.txt` holds
//! the shared test vectors.

/// A clause is at most this many bytes.
pub const WHEN_MAX: usize = 512;
/// It compiles to at most this many operations.
pub const WHEN_OPS_MAX: usize = 64;
/// It nests `!` and parentheses at most this deep.
pub const WHEN_DEPTH_MAX: usize = 8;

/// rhun's built-in context keys (a typo in one is an error).
pub const BUILTIN_KEYS: &[&str] = &[
    "editorFocus",
    "textInputFocus",
    "paletteFocus",
    "findFocus",
    "explorerFocus",
    "agentsFocus",
    "terminalFocus",
    "scmInputFocus",
    "settingsFocus",
    "findVisible",
    "completionVisible",
    "signatureVisible",
    "snippetActive",
    "activeTab",
    "activeTabPinned",
    "tabCount",
    "resourceLang",
    "resourceExt",
    "resourceFilename",
    "resourceDirname",
    "resourcePath",
    "resourceScheme",
    "resourceInProject",
    "editorReadonly",
    "editorDirty",
    "editorHasSelection",
    "editorHasMultipleSelections",
    "editorProviders",
    "vimMode",
    "sidebarVisible",
    "agentsVisible",
    "terminalVisible",
    "projectOpen",
    "projectPath",
    "gitRepo",
    "gitBranch",
    "gitChanges",
    "isLinux",
    "isMac",
    "isWindows",
    "isWayland",
    "targetKind",
    "explorerResourceIsFolder",
    "explorerResourceIsRoot",
    "tabKind",
    "viewFocus",
    "view",
    "viewItem",
    "debugState",
    "inDebugMode",
    "debugType",
    "gitTrusted",
    "scmProvider",
    "scmResourceGroup",
    "scmResourceStatus",
    "notebookFocus",
    "notebookCellEditing",
    "notebookCellType",
    "notebookKernelState",
    "remoteName",
    "remoteHost",
];

/// rhun's settings, `SECTION.KEY`, as `config.SECTION.KEY` names them.
pub const SETTINGS: &[&str] = &[
    "ui.follow_system",
    "ui.theme",
    "ui.dark_theme",
    "ui.light_theme",
    "ui.scale",
    "ui.font_size",
    "ui.font",
    "ui.file_icons",
    "ui.sidebar",
    "ui.sidebar_width",
    "ui.agents_panel",
    "ui.agents_width",
    "ui.tooltips",
    "ui.auto_hide_scrollbars",
    "ui.decorations",
    "ui.scroll_sensitivity",
    "ui.fast_scroll_sensitivity",
    "editor.font_size",
    "editor.font",
    "editor.line_height",
    "editor.tab_width",
    "editor.insert_spaces",
    "editor.line_numbers",
    "editor.highlight_line",
    "editor.animate_disk_changes",
    "editor.match_brackets",
    "editor.indent_guides",
    "editor.word_wrap",
    "editor.whitespace",
    "editor.cursor_blink",
    "editor.smooth_caret",
    "editor.auto_pairs",
    "editor.multi_cursor_paste",
    "editor.multi_cursor_modifier",
    "editor.scroll_past_end",
    "editor.vim_mode",
    "editor.complete_replace",
    "editor.complete_commit_characters",
    "editor.lightbulb",
    "editor.format_on_type",
    "editor.format_on_paste",
    "editor.folding",
    "files.trim_trailing_whitespace",
    "files.final_newline",
    "files.format_on_save",
    "files.format_on_save_timeout",
    "files.code_actions_on_save",
    "files.code_actions_on_save_timeout",
    "files.restore_session",
    "files.restore_project",
    "files.exclude",
    "agents.claude",
    "agents.codex",
    "agents.grok",
    "agents.opencode",
    "terminal.shell",
    "terminal.font_size",
    "terminal.font",
    "terminal.line_height",
    "terminal.scrollback",
    "terminal.height",
    "git.enabled",
    "git.commit_ai",
    "git.commit_model",
    "git.ai_setup",
    "extensions.secrets",
    "updates.check",
    "updates.check_now",
    "updates.extensions",
    "updates.check_extensions",
    "debug.show_on_start",
    "debug.keys_in_terminal",
    "debug.console_lines",
    "remote.agent_binary",
    "testing.gutter",
];

struct P<'a> {
    s: &'a [u8],
    p: usize,
    ops: usize,
    depth: usize,
    err: Option<(usize, String)>,
}

/// `Ok(())` when the clause compiles, else the byte offset rhun reports and the reason.
pub fn check(clause: &str) -> Result<(), (usize, String)> {
    if clause.len() > WHEN_MAX {
        return Err((WHEN_MAX, "too long (512 bytes at most)".into()));
    }
    let mut w = P {
        s: clause.as_bytes(),
        p: 0,
        ops: 0,
        depth: 0,
        err: None,
    };
    w.ws();
    if w.p >= w.s.len() {
        return Err((0, "empty clause".into()));
    }
    w.or();
    if w.err.is_none() {
        w.ws();
        if w.p < w.s.len() {
            w.fail("expected && or ||");
        }
    }
    match w.err {
        Some(e) => Err(e),
        None => Ok(()),
    }
}

fn word_byte(c: u8) -> bool {
    c == b'_' || c == b'.' || c == b'/' || c == b'-' || c.is_ascii_alphanumeric()
}

fn local_ok(k: &str) -> bool {
    let b = k.as_bytes();
    !b.is_empty()
        && b.len() <= 40
        && b[0].is_ascii_lowercase()
        && b[1..]
            .iter()
            .all(|c| *c == b'_' || c.is_ascii_alphanumeric())
}

fn setting_ok(k: &str) -> bool {
    !k.is_empty()
        && k.len() <= 64
        && k.bytes()
            .all(|c| c == b'_' || c.is_ascii_digit() || c.is_ascii_lowercase())
}

impl P<'_> {
    fn fail_at(&mut self, at: usize, text: &str) {
        if self.err.is_none() {
            self.err = Some((at, text.to_string()));
        }
    }
    fn fail(&mut self, text: &str) {
        self.fail_at(self.p, text)
    }
    fn ws(&mut self) {
        while self.p < self.s.len() && (self.s[self.p] == b' ' || self.s[self.p] == b'\t') {
            self.p += 1;
        }
    }
    fn peek2(&self) -> (u8, u8) {
        (
            self.s.get(self.p).copied().unwrap_or(0),
            self.s.get(self.p + 1).copied().unwrap_or(0),
        )
    }
    fn emit(&mut self) {
        if self.ops >= WHEN_OPS_MAX {
            self.fail("too many operations (64 at most)");
        } else {
            self.ops += 1;
        }
    }
    fn or(&mut self) {
        self.and();
        while self.err.is_none() {
            self.ws();
            if self.peek2() != (b'|', b'|') {
                return;
            }
            self.p += 2;
            self.and();
            self.emit();
        }
    }
    fn and(&mut self) {
        self.unary();
        while self.err.is_none() {
            self.ws();
            if self.peek2() != (b'&', b'&') {
                return;
            }
            self.p += 2;
            self.unary();
            self.emit();
        }
    }
    fn deeper(&mut self) -> bool {
        self.depth += 1;
        if self.depth > WHEN_DEPTH_MAX {
            self.fail("nested too deeply (8 levels at most)");
            return false;
        }
        true
    }
    fn unary(&mut self) {
        if self.err.is_some() {
            return;
        }
        self.ws();
        let (a, b) = self.peek2();
        match a {
            b'!' => {
                if b == b'=' {
                    return self.fail("expected a key, true, false, ! or (");
                }
                self.p += 1;
                if !self.deeper() {
                    return;
                }
                self.unary();
                self.depth -= 1;
                self.emit();
            }
            b'(' => {
                self.p += 1;
                if !self.deeper() {
                    return;
                }
                self.or();
                if self.err.is_some() {
                    return;
                }
                self.depth -= 1;
                self.ws();
                if self.peek2().0 != b')' {
                    return self.fail("expected )");
                }
                self.p += 1;
            }
            b'\'' | b'"' => {
                self.quoted();
                if self.err.is_some() {
                    return;
                }
                self.emit();
                self.ws();
                if self.in_word() == 0 {
                    return self.fail("expected in or not in");
                }
                self.list();
                self.emit();
            }
            _ => {
                let start = self.p;
                let word = self.word();
                if word.is_empty() {
                    return self.fail("expected a key, true, false, ! or (");
                }
                if word == "true" || word == "false" {
                    return self.emit();
                }
                self.key(start, &word);
                if self.err.is_none() {
                    self.tail();
                }
            }
        }
    }
    fn tail(&mut self) {
        self.ws();
        let (a, b) = self.peek2();
        let two = match a {
            b'=' => {
                if b != b'=' && b != b'~' {
                    return self.fail("expected == != < <= > >= =~ in or not in");
                }
                true
            }
            b'!' => {
                if b != b'=' {
                    return self.fail("expected == != < <= > >= =~ in or not in");
                }
                true
            }
            b'<' | b'>' => b == b'=',
            _ => {
                if self.in_word() != 0 {
                    self.list();
                    self.emit();
                }
                return;
            }
        };
        self.p += if two { 2 } else { 1 };
        self.value();
        self.emit();
    }
    fn in_word(&mut self) -> u8 {
        let start = self.p;
        let word = self.word();
        if word == "in" {
            return 1;
        }
        if word == "not" {
            self.ws();
            let after = self.p;
            if self.word() == "in" {
                return 2;
            }
            self.p = after;
            self.fail("expected in or not in");
            return 0;
        }
        self.p = start;
        0
    }
    fn value(&mut self) {
        self.ws();
        let a = self.peek2().0;
        if a == b'\'' || a == b'"' {
            self.quoted();
        } else if self.bare().is_empty() {
            return self.fail("expected a value");
        }
        if self.err.is_none() {
            self.emit();
        }
    }
    fn list(&mut self) {
        self.ws();
        let a = self.peek2().0;
        if a == b'\'' || a == b'"' {
            self.quoted();
            if self.err.is_none() {
                self.emit();
            }
            return;
        }
        let start = self.p;
        let word = self.word();
        if word.is_empty() {
            return self.fail("expected a key or a quoted list");
        }
        self.key(start, &word);
    }
    fn word(&mut self) -> String {
        let start = self.p;
        while self.p < self.s.len() && word_byte(self.s[self.p]) {
            self.p += 1;
        }
        String::from_utf8_lossy(&self.s[start..self.p]).into_owned()
    }
    fn bare(&mut self) -> String {
        let start = self.p;
        while self.p < self.s.len()
            && (b"_-./:*?+#@$".contains(&self.s[self.p]) || self.s[self.p].is_ascii_alphanumeric())
        {
            self.p += 1;
        }
        String::from_utf8_lossy(&self.s[start..self.p]).into_owned()
    }
    fn quoted(&mut self) {
        let q = self.s[self.p];
        let open = self.p;
        let mut i = self.p + 1;
        while i < self.s.len() && self.s[i] != q {
            if self.s[i] < 0x20 || self.s[i] == 0x7f {
                return self.fail_at(open, "unclosed quote");
            }
            i += 1;
        }
        if i >= self.s.len() {
            return self.fail_at(open, "unclosed quote");
        }
        if std::str::from_utf8(&self.s[open + 1..i]).is_err() {
            return self.fail_at(open, "a quoted value that is not UTF-8");
        }
        self.p = i + 1;
    }
    fn key(&mut self, start: usize, k: &str) {
        let mut cut = k.len().min(60);
        while !k.is_char_boundary(cut) {
            cut -= 1;
        }
        let unknown = format!("unknown key {}", &k[..cut]);
        if let Some(rest) = k.strip_prefix("config.") {
            match rest.find('.') {
                Some(d) if d > 0 && d + 1 < rest.len() && SETTINGS.contains(&rest) => self.emit(),
                _ => self.fail_at(start, &unknown),
            }
            return;
        }
        let ok = match k.find('.') {
            None => BUILTIN_KEYS.contains(&k),
            Some(d) if d == 0 || d + 1 == k.len() => false,
            Some(d) => {
                let rest = &k[d + 1..];
                match rest.strip_prefix("setting.") {
                    Some(s) => setting_ok(s),
                    None => local_ok(rest),
                }
            }
        };
        if ok {
            self.emit()
        } else {
            self.fail_at(start, &unknown)
        }
    }
}

#[cfg(test)]
mod tests {
    #[test]
    fn vectors() {
        let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../v1.1/when-vectors.txt");
        let text = std::fs::read_to_string(path).unwrap();
        let mut n = 0;
        for line in text.lines() {
            if line.is_empty() || line.starts_with('#') {
                continue;
            }
            let (res, clause) = line.split_once('\t').unwrap();
            n += 1;
            let got = super::check(clause);
            match res.strip_prefix("error ") {
                Some(at) => {
                    assert_eq!(got.map_err(|e| e.0), Err(at.parse().unwrap()), "{clause:?}")
                }
                None => assert!(got.is_ok(), "{clause:?}: {got:?}"),
            }
        }
        assert!(n >= 50);
    }
}
