//! Feature `debug` (protocol 1.1 "debug"): an extension that declares debuggers
//! (`[debugger.TYPE]` sections, `[needs] debug = yes`) starts a session of one of its own
//! debuggers with [`Ext::debug_start`], within 10 s of one of its commands, and hears
//! [`DebugEnded`] when it is over. Configurations travel as flat lists of typed leaves with dotted
//! paths ([`DebugConfig`]): `args.0`, `env.LOG_LEVEL`.

use crate::{Doc, Error, Event, Ext, Feature, Pos};

/// At most this many leaves in a configuration body.
pub const MAX_DEBUG_LEAVES: usize = 4096;
/// A configuration body's bytes.
pub const MAX_DEBUG_BODY: usize = 1 << 20;
/// A leaf's path.
pub const MAX_DEBUG_PATH: usize = 256;
/// A leaf's path segments.
pub const MAX_DEBUG_SEGMENTS: usize = 16;
/// A leaf's value.
pub const MAX_DEBUG_VALUE: usize = 64 << 10;

/// A configuration body's context record: the active document, the cursor and the path.
pub type DebugDocContext = (Doc, Pos, String);

/// One leaf's value.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum DebugValue {
    Str(String),
    /// An integer, |v| < 2^53.
    Int(i64),
    Bool(bool),
    Null,
}

impl DebugValue {
    fn kind(&self) -> char {
        match self {
            DebugValue::Str(_) => 's',
            DebugValue::Int(_) => 'n',
            DebugValue::Bool(_) => 'b',
            DebugValue::Null => 'z',
        }
    }
    fn text(&self) -> String {
        match self {
            DebugValue::Str(s) => s.clone(),
            DebugValue::Int(n) => n.to_string(),
            DebugValue::Bool(b) => b.to_string(),
            DebugValue::Null => String::new(),
        }
    }
}

/// A configuration: the launch or attach arguments, with `type` (one of the extension's debugger
/// types) and optionally `request` (`launch` or `attach`), as dotted paths in order.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct DebugConfig {
    pub entries: Vec<(String, DebugValue)>,
}

fn under(path: &str, prefix: &str) -> bool {
    path == prefix || (path.starts_with(prefix) && path.as_bytes().get(prefix.len()) == Some(&b'.'))
}

impl DebugConfig {
    pub fn new() -> DebugConfig {
        DebugConfig::default()
    }
    /// A leaf's value.
    pub fn get(&self, path: &str) -> Option<&DebugValue> {
        self.entries.iter().find(|(p, _)| p == path).map(|(_, v)| v)
    }
    /// A string leaf's text.
    pub fn get_str(&self, path: &str) -> Option<&str> {
        match self.get(path) {
            Some(DebugValue::Str(s)) => Some(s),
            _ => None,
        }
    }
    /// Replaces a leaf (and anything under its path) or adds it.
    pub fn set(&mut self, path: &str, v: DebugValue) {
        self.remove(path);
        self.entries.push((path.to_string(), v));
    }
    /// Drops a leaf and everything under its path.
    pub fn remove(&mut self, path: &str) {
        self.entries.retain(|(p, _)| !under(p, path));
    }
    /// The strings of an array (`PATH.0`, `PATH.1`, …) in index order.
    pub fn list(&self, path: &str) -> Vec<String> {
        let mut items: Vec<(usize, String)> = self
            .entries
            .iter()
            .filter_map(|(p, v)| {
                let rest = p.strip_prefix(path)?.strip_prefix('.')?;
                Some((rest.parse().ok()?, v.text()))
            })
            .collect();
        items.sort_by_key(|(i, _)| *i);
        items.into_iter().map(|(_, s)| s).collect()
    }
    /// Replaces an array with strings.
    pub fn set_list(&mut self, path: &str, items: &[&str]) {
        self.remove(path);
        for (i, s) in items.iter().enumerate() {
            self.entries
                .push((format!("{path}.{i}"), DebugValue::Str(s.to_string())));
        }
    }

    /// The configuration body: `N` LF, then each leaf `KIND PATH_LEN VALUE_LEN` LF + PATH + VALUE.
    pub fn encode(&self) -> Result<Vec<u8>, String> {
        if self.entries.len() > MAX_DEBUG_LEAVES {
            return Err("over 4096 leaves".into());
        }
        let mut b = format!("{}\n", self.entries.len()).into_bytes();
        for (p, v) in &self.entries {
            if p.is_empty()
                || p.len() > MAX_DEBUG_PATH
                || p.matches('.').count() >= MAX_DEBUG_SEGMENTS
            {
                return Err(format!("bad path {p:?}"));
            }
            if let DebugValue::Int(n) = v {
                if n.unsigned_abs() >= 1 << 53 {
                    return Err(format!("{p} is too large"));
                }
            }
            let t = v.text();
            if t.len() > MAX_DEBUG_VALUE {
                return Err(format!("{p} is over 64 KB"));
            }
            b.extend_from_slice(format!("{} {} {}\n", v.kind(), p.len(), t.len()).as_bytes());
            b.extend_from_slice(p.as_bytes());
            b.extend_from_slice(t.as_bytes());
        }
        if b.len() > MAX_DEBUG_BODY {
            return Err("the configuration is over 1 MB".into());
        }
        Ok(b)
    }

    /// Reads a configuration body and its extra records (unknown ones are skipped): the
    /// configuration and the context record's document, position and path.
    pub fn parse(body: &[u8]) -> Option<(DebugConfig, Option<DebugDocContext>)> {
        let mut rest = body;
        fn line<'a>(rest: &mut &'a [u8]) -> Option<&'a str> {
            let nl = rest.iter().position(|&c| c == b'\n')?;
            let l = std::str::from_utf8(&rest[..nl]).ok()?;
            *rest = &rest[nl + 1..];
            Some(l)
        }
        let n: usize = line(&mut rest)?.parse().ok()?;
        if n > MAX_DEBUG_LEAVES {
            return None;
        }
        let mut cfg = DebugConfig::new();
        for _ in 0..n {
            let f: Vec<&str> = line(&mut rest)?.split(' ').collect();
            if f.len() != 3 || f[0].len() != 1 {
                return None;
            }
            let pl: usize = f[1].parse().ok()?;
            let vl: usize = f[2].parse().ok()?;
            if pl == 0 || pl > MAX_DEBUG_PATH || vl > MAX_DEBUG_VALUE || pl + vl > rest.len() {
                return None;
            }
            let path = std::str::from_utf8(&rest[..pl]).ok()?.to_string();
            let val = std::str::from_utf8(&rest[pl..pl + vl]).ok()?;
            rest = &rest[pl + vl..];
            let v = match f[0] {
                "s" => DebugValue::Str(val.to_string()),
                "n" => {
                    let n: i64 = val.parse().ok()?;
                    if n.unsigned_abs() >= 1 << 53 {
                        return None;
                    }
                    DebugValue::Int(n)
                }
                "b" => match val {
                    "true" => DebugValue::Bool(true),
                    "false" => DebugValue::Bool(false),
                    _ => return None,
                },
                "z" if vl == 0 => DebugValue::Null,
                _ => return None,
            };
            cfg.entries.push((path, v));
        }
        let mut context = None;
        while !rest.is_empty() {
            let f: Vec<String> = line(&mut rest)?.split(' ').map(String::from).collect();
            let size: usize = f.last()?.parse().ok()?;
            if size > rest.len() {
                return None;
            }
            if f[0] == "context" && f.len() == 5 {
                let path = String::from_utf8_lossy(&rest[..size]).into_owned();
                context = Some((
                    f[1].parse().ok()?,
                    Pos::new(f[2].parse().ok()?, f[3].parse().ok()?),
                    path,
                ));
            }
            rest = &rest[size..];
        }
        Some((cfg, context))
    }
}

/// `0 debug-ended SESSION CODE`: a session this extension started is over; `code` is `None` when
/// the program's exit code is unknown.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DebugEnded {
    pub session: i64,
    pub code: Option<i32>,
}

impl DebugEnded {
    /// Reads a debug-ended event; `None` for any other event.
    pub fn parse(ev: &Event) -> Option<DebugEnded> {
        if ev.name != "debug-ended" || ev.fields.len() != 2 {
            return None;
        }
        let session: i64 = ev.fields[0].parse().ok().filter(|s| *s > 0)?;
        let code = match ev.fields[1].as_str() {
            "-" => None,
            c => Some(c.parse().ok()?),
        };
        Some(DebugEnded { session, code })
    }
}

/// `ID debug-resolve TYPE REQUEST NODEBUG LEN` + configuration: what [`crate::Handler::resolve_debug`]
/// gets before every launch of a debugger with `resolve = yes`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DebugResolve {
    pub id: u64,
    /// the debugger's type
    pub kind: String,
    /// `launch` or `attach`
    pub request: String,
    pub no_debug: bool,
    pub config: DebugConfig,
    /// the active document, the cursor and the document's path
    pub doc: Option<DebugDocContext>,
}

impl DebugResolve {
    /// `TYPE REQUEST NODEBUG LEN`, the fields after the verb, and the body.
    pub fn parse(id: u64, fields: &[String], body: &[u8]) -> Option<DebugResolve> {
        if fields.len() != 4 || !matches!(fields[1].as_str(), "launch" | "attach") {
            return None;
        }
        let no_debug = match fields[2].as_str() {
            "0" => false,
            "1" => true,
            _ => return None,
        };
        let (config, doc) = DebugConfig::parse(body)?;
        Some(DebugResolve {
            id,
            kind: fields[0].clone(),
            request: fields[1].clone(),
            no_debug,
            config,
            doc: doc.filter(|d| d.0 > 0),
        })
    }
}

/// The answer to [`DebugResolve`].
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Resolved {
    /// launch the configuration as it is
    Unchanged,
    /// launch this one instead (the complete configuration, with `type`)
    Config(DebugConfig),
    /// abandon the launch quietly (the user dismissed a question)
    Cancel,
    /// abandon it and show the sentence
    Refuse(String),
}

impl Ext {
    /// Starts a session of one of the extension's own debuggers (`cfg`'s `type`) named `name`
    /// (feature `debug`). It must come within 10 s of one of the extension's commands (one
    /// session per command); the user approves the extension's debugging once per project. The
    /// session's id once the adapter started; errors: `refused` (no recent command, or a type not
    /// the extension's), `cancelled` (declined), `busy` (a dialog is open), `toobig` (8 sessions).
    pub fn debug_start(&self, name: &str, cfg: &DebugConfig) -> Result<i64, Error> {
        if !self.has(Feature::Debug) {
            return Err(Error {
                code: "unknown".into(),
                text: "debug was not negotiated".into(),
            });
        }
        if name.is_empty() || name.len() > 256 || name.contains('\n') {
            return Err(Error {
                code: "syntax".into(),
                text: "a session name is 1 to 256 bytes on one line".into(),
            });
        }
        let cb = cfg.encode().map_err(|text| Error {
            code: "toobig".into(),
            text,
        })?;
        let mut body = name.as_bytes().to_vec();
        body.extend_from_slice(&cb);
        let r = self.request(&format!("debug-start {}", name.len()), Some(&body))?;
        r.fields
            .first()
            .and_then(|s| s.parse().ok())
            .ok_or_else(|| Error {
                code: "syntax".into(),
                text: "debug-start answered without a session".into(),
            })
    }

    /// Stops a session this extension started (as the toolbar's Stop: terminate, then
    /// disconnect, then the adapter is killed). Returns once the stop was asked; the session's
    /// `debug-ended` follows when it is over. One already over is fine; errors: `refused` (not a
    /// session this extension started).
    pub fn debug_stop(&self, session: i64) -> Result<(), Error> {
        if !self.has(Feature::Debug) {
            return Err(Error {
                code: "unknown".into(),
                text: "debug was not negotiated".into(),
            });
        }
        if session <= 0 {
            return Err(Error {
                code: "syntax".into(),
                text: "a session id is a positive number".into(),
            });
        }
        self.request(&format!("debug-stop {session}"), None)
            .map(|_| ())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn config_round_trip_and_context() {
        let mut c = DebugConfig::new();
        c.set("type", DebugValue::Str("go".into()));
        c.set_list("args", &["-test.run", "^TestAdd$"]);
        c.set("env.LOG", DebugValue::Str("debug".into()));
        c.set("count", DebugValue::Int(-7));
        c.set("stopOnEntry", DebugValue::Bool(true));
        c.set("extra", DebugValue::Null);
        let mut b = c.encode().unwrap();
        b.extend_from_slice(b"context 3 4 5 6\n/a/b.gounknown 1 2 3\nxyz");
        let (got, ctx) = DebugConfig::parse(&b).unwrap();
        assert_eq!(got, c);
        assert_eq!(got.list("args"), ["-test.run", "^TestAdd$"]);
        assert_eq!(ctx, Some((3, Pos::new(4, 5), "/a/b.g".to_string())));
        let mut g = got.clone();
        g.remove("args");
        assert!(g.list("args").is_empty());
        assert_eq!(g.get_str("type"), Some("go"));
        for bad in [
            &b"1\nx 1 1\nab"[..],
            b"1\nn 1 2\na1x",
            b"1\nb 1 3\nayes",
            b"1\nz 1 1\naa",
            b"2\ns 1 1\nab",
            b"x",
        ] {
            assert!(DebugConfig::parse(bad).is_none(), "{bad:?}");
        }
    }

    #[test]
    fn ended_event() {
        let ev = |f: &[&str]| Event {
            name: "debug-ended".into(),
            fields: f.iter().map(|s| s.to_string()).collect(),
            ..Event::default()
        };
        assert_eq!(
            DebugEnded::parse(&ev(&["7", "3"])),
            Some(DebugEnded {
                session: 7,
                code: Some(3)
            })
        );
        assert_eq!(
            DebugEnded::parse(&ev(&["8", "-"])),
            Some(DebugEnded {
                session: 8,
                code: None
            })
        );
        assert_eq!(DebugEnded::parse(&ev(&["0", "1"])), None);
    }
}
