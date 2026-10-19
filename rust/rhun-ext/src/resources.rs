//! The resources group of protocol 1.1: staying alive while busy (feature `idle`) and passwords
//! and tokens in the system keychain (feature `secrets`, with `[needs] secrets = yes`).
use crate::{Error, Ext, Feature};

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
    fn keys_are_checked() {
        for k in ["token", "a", "github.token-2_x", &"k".repeat(64)] {
            assert!(valid_secret_key(k), "{k}");
        }
        for k in ["", "Token", "a b", "ä", &"k".repeat(65), "a/b"] {
            assert!(!valid_secret_key(k), "{k}");
        }
    }
}
