//! The resources group of protocol 1.1: staying alive while busy (feature `idle`), passwords
//! and tokens in the system keychain (feature `secrets`, with `[needs] secrets = yes`), and
//! URIs with virtual documents (feature `uris`, with `[provides] schemes`).
use crate::{Doc, Error, Event, Ext, Feature, Pos};

/// What became of an [`Ext::open_url`] the user answered.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum UrlOpened {
    /// Opened in the default browser (also without asking, once the user chose Always).
    Opened,
    /// The user copied the link instead.
    Copied,
}

/// `0 link ID LEN` + the link: the id of the [`Ext::link`] request that made the token, and the
/// whole link as it reached rhun (with the path and query the browser added).
pub fn parse_link(event: &Event) -> Option<(u64, String)> {
    if event.name != "link" || event.fields.len() != 2 {
        return None;
    }
    let id = event.fields[0].parse().ok()?;
    Some((id, String::from_utf8(event.body.clone()).ok()?))
}

/// A URI rhun takes for a resource: `SCHEME:REST`, SCHEME `[a-z][a-z0-9+.-]` of 2 to 32 bytes,
/// REST 1 to 4095 bytes without LF, CR or NUL.
pub fn valid_uri(uri: &str) -> bool {
    let Some((scheme, rest)) = uri.split_once(':') else {
        return false;
    };
    let b = scheme.as_bytes();
    (2..=32).contains(&b.len())
        && b[0].is_ascii_lowercase()
        && b.iter().all(|c| {
            c.is_ascii_lowercase() || c.is_ascii_digit() || matches!(c, b'+' | b'.' | b'-')
        })
        && (1..=4095).contains(&rest.len())
        && !rest.bytes().any(|c| matches!(c, b'\n' | b'\r' | 0))
}

/// A virtual document's text, the answer to rhun's content request (feature `uris`).
#[derive(Clone, Debug, Default)]
pub struct Content {
    /// A grammar name ("Rust"), or empty to choose by the URI's last segment.
    pub lang: String,
    /// The tab's title, or empty for the URI's last segment.
    pub title: String,
    /// At most 4 MiB of UTF-8.
    pub text: String,
}

impl Content {
    pub fn encode(&self) -> Result<Vec<u8>, String> {
        let lang = self.lang.replace(' ', "_");
        if lang.len() > 255 || self.title.len() > 255 || self.title.contains('\n') {
            return Err("a language or title over 255 bytes or with a line break".into());
        }
        if self.text.len() > 4 << 20 {
            return Err("the text is over 4 MiB".into());
        }
        let mut out =
            format!("{} {} {}\n", lang.len(), self.title.len(), self.text.len()).into_bytes();
        out.extend_from_slice(lang.as_bytes());
        out.extend_from_slice(self.title.as_bytes());
        out.extend_from_slice(self.text.as_bytes());
        Ok(out)
    }
}

/// A key: 1 to 64 bytes of `a-z 0-9 _ . -`.
pub fn valid_secret_key(key: &str) -> bool {
    !key.is_empty()
        && key.len() <= 64
        && key.bytes().all(|c| {
            c.is_ascii_lowercase() || c.is_ascii_digit() || matches!(c, b'_' | b'.' | b'-')
        })
}

/// A value holds at most this many bytes.
pub const SECRET_VALUE_MAX: usize = 8192;

fn not_agreed(feature: &str) -> Error {
    Error {
        code: "unknown".into(),
        text: format!("{feature} was not negotiated"),
    }
}

fn syntax(text: &str) -> Error {
    Error {
        code: "syntax".into(),
        text: text.into(),
    }
}

impl Ext {
    /// Asks rhun to open an http(s) URL in the user's browser (feature `urls`). The user sees the
    /// extension and the host and chooses Open, Copy Link or Cancel (`Err` with code `cancelled`).
    pub fn open_url(&self, url: &str) -> Result<UrlOpened, Error> {
        if !self.has(Feature::Urls) {
            return Err(not_agreed("urls"));
        }
        let ok = (url.starts_with("https://") || url.starts_with("http://"))
            && url.len() <= 8192
            && !url.bytes().any(|c| c <= b' ' || c == 0x7f);
        if !ok {
            return Err(syntax("not an http or https URL"));
        }
        let reply = self.request("open-url", Some(url.as_bytes()))?;
        match reply.fields.first().map(String::as_str) {
            Some("copied") => Ok(UrlOpened::Copied),
            _ => Ok(UrlOpened::Opened),
        }
    }

    /// The URI the user's browser can use for `uri` (a loopback URL, a rhun: link): pass every
    /// loopback URL you hand to a browser through it (a remote window forwards the port).
    pub fn external_uri(&self, uri: &str) -> Result<String, Error> {
        if !self.has(Feature::Urls) {
            return Err(not_agreed("urls"));
        }
        let reply = self.request("external-uri", Some(uri.as_bytes()))?;
        String::from_utf8(reply.body).map_err(|_| syntax("not UTF-8"))
    }

    /// A one-shot `rhun://ext/OWNER/NAME/TOKEN` link (ten minutes); append a path and query.
    /// When it reaches rhun, the event `link` (see [`parse_link`]) brings it back.
    pub fn link(&self) -> Result<String, Error> {
        if !self.has(Feature::Urls) {
            return Err(not_agreed("urls"));
        }
        let reply = self.request("link", None)?;
        String::from_utf8(reply.body).map_err(|_| syntax("not UTF-8"))
    }

    /// The value of one of the extension's own settings in force for a folder (its id; 0 for the
    /// window) and a language (a grammar name, "" for none) (feature `scoped-settings`).
    pub fn setting_get(&self, key: &str, folder: u64, lang: &str) -> Result<String, Error> {
        if !self.has(Feature::ScopedSettings) {
            return Err(not_agreed("scoped-settings"));
        }
        if key.is_empty() || key.contains([' ', '\r', '\n']) {
            return Err(syntax("invalid setting key"));
        }
        let lang = if lang.is_empty() {
            "-".to_string()
        } else {
            lang.replace(' ', "_")
        };
        let reply = self.request(&format!("setting-get {key} {folder} {lang}"), None)?;
        String::from_utf8(reply.body).map_err(|_| syntax("not UTF-8"))
    }

    /// Opens a virtual document (feature `uris`): rhun asks the scheme's provider for its text.
    /// A `file:` URI opens that file. [`Ext::open`] takes a URI as well.
    pub fn open_uri(&self, uri: &str, at: Pos) -> Result<Doc, Error> {
        if !self.has(Feature::Uris) {
            return Err(not_agreed("uris"));
        }
        if !valid_uri(uri) {
            return Err(syntax("invalid URI"));
        }
        self.open(uri, at)
    }

    /// The text of the virtual documents open for `uri` changed: rhun asks again.
    pub fn content_changed(&self, uri: &str) -> Result<(), Error> {
        if !self.has(Feature::Uris) {
            return Err(not_agreed("uris"));
        }
        if !valid_uri(uri) {
            return Err(syntax("invalid URI"));
        }
        self.request(&format!("content-changed {uri}"), None)?;
        Ok(())
    }

    /// `busy yes|no` (feature `idle`): while busy, an `idle = stop` extension is not stopped for
    /// idleness.
    pub fn busy(&self, busy: bool) -> Result<(), Error> {
        if !self.has(Feature::Idle) {
            return Err(not_agreed("idle"));
        }
        self.request(if busy { "busy yes" } else { "busy no" }, None)?;
        Ok(())
    }

    /// The value stored under `key`, or `None`.
    pub fn secret_get(&self, key: &str) -> Result<Option<Vec<u8>>, Error> {
        if !self.has(Feature::Secrets) {
            return Err(not_agreed("secrets"));
        }
        if !valid_secret_key(key) {
            return Err(syntax("invalid secret key"));
        }
        let reply = self.request(&format!("secret-get {key}"), None)?;
        match reply.fields.first().map(String::as_str) {
            Some("1") => Ok(Some(reply.body)),
            Some("0") => Ok(None),
            _ => Err(syntax("invalid secret-get answer")),
        }
    }

    /// Stores `value` (at most [`SECRET_VALUE_MAX`] bytes) under `key`.
    pub fn secret_set(&self, key: &str, value: &[u8]) -> Result<(), Error> {
        if !self.has(Feature::Secrets) {
            return Err(not_agreed("secrets"));
        }
        if !valid_secret_key(key) {
            return Err(syntax("invalid secret key"));
        }
        if value.len() > SECRET_VALUE_MAX {
            return Err(Error {
                code: "toobig".into(),
                text: "a value is at most 8192 bytes".into(),
            });
        }
        self.request(&format!("secret-set {key}"), Some(value))?;
        Ok(())
    }

    /// Removes `key` (also when nothing was stored).
    pub fn secret_delete(&self, key: &str) -> Result<(), Error> {
        if !self.has(Feature::Secrets) {
            return Err(not_agreed("secrets"));
        }
        if !valid_secret_key(key) {
            return Err(syntax("invalid secret key"));
        }
        self.request(&format!("secret-delete {key}"), None)?;
        Ok(())
    }

    /// The keys stored for this extension.
    pub fn secret_keys(&self) -> Result<Vec<String>, Error> {
        if !self.has(Feature::Secrets) {
            return Err(not_agreed("secrets"));
        }
        let reply = self.request("secret-keys", None)?;
        let text = String::from_utf8(reply.body).map_err(|_| syntax("keys are not UTF-8"))?;
        Ok(text
            .split('\n')
            .filter(|k| !k.is_empty())
            .map(str::to_string)
            .collect())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn uris_and_content() {
        assert!(valid_uri("lsp-expand:/a/main.rs") && valid_uri("git+show:x"));
        assert!(
            !valid_uri("C:x")
                && !valid_uri("x")
                && !valid_uri("Ab:x")
                && !valid_uri("ab:")
                && !valid_uri("ab:a\nb")
        );
        let c = Content {
            lang: "Visual Basic".into(),
            title: "t".into(),
            text: "hi\n".into(),
        };
        assert_eq!(c.encode().unwrap(), b"12 1 3\nVisual_Basicthi\n");
    }
    #[test]
    fn keys_are_checked() {
        for k in ["token", "a", "github.token-2_x", &"k".repeat(64)] {
            assert!(valid_secret_key(k), "{k}");
        }
        for k in ["", "Token", "a b", "ä", &"k".repeat(65), "a/b"] {
            assert!(!valid_secret_key(k), "{k}");
        }
    }
}
