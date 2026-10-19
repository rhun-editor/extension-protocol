//! The resources group of protocol 1.1: staying alive while busy (feature `idle`), passwords
//! and tokens in the system keychain (feature `secrets`, with `[needs] secrets = yes`), and
//! URIs with virtual documents (feature `uris`, with `[provides] schemes`).
use crate::{Doc, Error, Ext, Feature, Pos};

/// A URI rhun takes for a resource: `SCHEME:REST`, SCHEME `[a-z][a-z0-9+.-]` of 2 to 32 bytes,
/// REST 1 to 4095 bytes without LF, CR or NUL.
pub fn valid_uri(uri: &str) -> bool {
    let Some((scheme, rest)) = uri.split_once(':') else { return false };
    let b = scheme.as_bytes();
    (2..=32).contains(&b.len())
        && b[0].is_ascii_lowercase()
        && b.iter().all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || matches!(c, b'+' | b'.' | b'-'))
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
        let mut out = format!("{} {} {}\n", lang.len(), self.title.len(), self.text.len()).into_bytes();
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
        && key
            .bytes()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || matches!(c, b'_' | b'.' | b'-'))
}

/// A value holds at most this many bytes.
pub const SECRET_VALUE_MAX: usize = 8192;

fn not_agreed(feature: &str) -> Error {
    Error { code: "unknown".into(), text: format!("{feature} was not negotiated") }
}

fn syntax(text: &str) -> Error {
    Error { code: "syntax".into(), text: text.into() }
}

impl Ext {
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
            return Err(Error { code: "toobig".into(), text: "a value is at most 8192 bytes".into() });
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
        Ok(text.split('\n').filter(|k| !k.is_empty()).map(str::to_string).collect())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn uris_and_content() {
        assert!(valid_uri("lsp-expand:/a/main.rs") && valid_uri("git+show:x"));
        assert!(!valid_uri("C:x") && !valid_uri("x") && !valid_uri("Ab:x") && !valid_uri("ab:") && !valid_uri("ab:a\nb"));
        let c = Content { lang: "Visual Basic".into(), title: "t".into(), text: "hi\n".into() };
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
