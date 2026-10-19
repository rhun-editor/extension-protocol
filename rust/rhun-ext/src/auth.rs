//! Signing in from an extension: OAuth's authorization code flow with PKCE, the redirect received
//! on a one-shot loopback HTTP listener, and random values. Standard library only.
//!
//! ```no_run
//! # fn demo(ext: &rhun_ext::Ext) -> Result<(), rhun_ext::Error> {
//! use rhun_ext::auth::{Loopback, Pkce, random_hex};
//! let pkce = Pkce::new();
//! let state = random_hex(16);
//! let lb = Loopback::bind()?;
//! let redirect = ext.external_uri(&lb.redirect_uri())?;
//! let url = format!("https://example.com/authorize?response_type=code&client_id=ID&redirect_uri={}\
//!                    &state={state}&code_challenge={}&code_challenge_method=S256",
//!                   rhun_ext::auth::percent_encode(&redirect), pkce.challenge);
//! ext.open_url(&url)?;
//! let query = lb.wait(&state, std::time::Duration::from_secs(300))?;
//! let code = &query["code"];   // exchange it with pkce.verifier at the token endpoint
//! # Ok(()) }
//! ```
//! The listener needs `[needs] network = yes` (which the token exchange needs anyway).
use crate::Error;
use std::collections::HashMap;
use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::time::{Duration, Instant};

fn err(code: &str, text: impl Into<String>) -> Error {
    Error { code: code.into(), text: text.into() }
}

/// `n` random bytes from the operating system.
pub fn random_bytes(n: usize) -> Vec<u8> {
    let mut out = vec![0u8; n];
    #[cfg(windows)]
    {
        #[link(name = "bcrypt")]
        extern "system" {
            fn BCryptGenRandom(alg: *mut u8, buf: *mut u8, len: u32, flags: u32) -> i32;
        }
        // BCRYPT_USE_SYSTEM_PREFERRED_RNG
        let status = unsafe { BCryptGenRandom(std::ptr::null_mut(), out.as_mut_ptr(), n as u32, 2) };
        assert!(status >= 0, "no random source");
    }
    #[cfg(not(windows))]
    {
        let mut f = std::fs::File::open("/dev/urandom").expect("no random source");
        f.read_exact(&mut out).expect("no random source");
    }
    out
}

/// `n` random bytes as lowercase hex (a `state`, a nonce).
pub fn random_hex(n: usize) -> String {
    random_bytes(n).iter().map(|b| format!("{b:02x}")).collect()
}

/// base64url without padding (RFC 7636's encoding).
pub fn base64url(data: &[u8]) -> String {
    const A: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
    let mut out = String::new();
    for chunk in data.chunks(3) {
        let b = [chunk[0], *chunk.get(1).unwrap_or(&0), *chunk.get(2).unwrap_or(&0)];
        let n = (b[0] as u32) << 16 | (b[1] as u32) << 8 | b[2] as u32;
        for i in 0..chunk.len() + 1 {
            out.push(A[(n >> (18 - 6 * i) & 63) as usize] as char);
        }
    }
    out
}

/// SHA-256 of `data`.
pub fn sha256(data: &[u8]) -> [u8; 32] {
    const K: [u32; 64] = [
        0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
        0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
        0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
        0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
        0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
        0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
        0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
        0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
    ];
    let mut h: [u32; 8] = [
        0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
    ];
    let mut msg = data.to_vec();
    msg.push(0x80);
    while msg.len() % 64 != 56 {
        msg.push(0);
    }
    msg.extend_from_slice(&((data.len() as u64) * 8).to_be_bytes());
    for block in msg.chunks(64) {
        let mut w = [0u32; 64];
        for i in 0..16 {
            w[i] = u32::from_be_bytes([block[4 * i], block[4 * i + 1], block[4 * i + 2], block[4 * i + 3]]);
        }
        for i in 16..64 {
            let s0 = w[i - 15].rotate_right(7) ^ w[i - 15].rotate_right(18) ^ (w[i - 15] >> 3);
            let s1 = w[i - 2].rotate_right(17) ^ w[i - 2].rotate_right(19) ^ (w[i - 2] >> 10);
            w[i] = w[i - 16].wrapping_add(s0).wrapping_add(w[i - 7]).wrapping_add(s1);
        }
        let mut v = h;
        for i in 0..64 {
            let s1 = v[4].rotate_right(6) ^ v[4].rotate_right(11) ^ v[4].rotate_right(25);
            let ch = (v[4] & v[5]) ^ (!v[4] & v[6]);
            let t1 = v[7].wrapping_add(s1).wrapping_add(ch).wrapping_add(K[i]).wrapping_add(w[i]);
            let s0 = v[0].rotate_right(2) ^ v[0].rotate_right(13) ^ v[0].rotate_right(22);
            let maj = (v[0] & v[1]) ^ (v[0] & v[2]) ^ (v[1] & v[2]);
            let t2 = s0.wrapping_add(maj);
            v = [t1.wrapping_add(t2), v[0], v[1], v[2], v[3].wrapping_add(t1), v[4], v[5], v[6]];
        }
        for i in 0..8 {
            h[i] = h[i].wrapping_add(v[i]);
        }
    }
    let mut out = [0u8; 32];
    for i in 0..8 {
        out[4 * i..4 * i + 4].copy_from_slice(&h[i].to_be_bytes());
    }
    out
}

/// A PKCE pair (RFC 7636, S256): send `challenge` with the authorization request and `verifier`
/// with the token request.
#[derive(Clone, Debug)]
pub struct Pkce {
    pub verifier: String,
    pub challenge: String,
}

impl Pkce {
    pub fn new() -> Pkce {
        let verifier = base64url(&random_bytes(32));
        let challenge = base64url(&sha256(verifier.as_bytes()));
        Pkce { verifier, challenge }
    }
}

impl Default for Pkce {
    fn default() -> Self {
        Self::new()
    }
}

/// Percent-encoding for a query value (RFC 3986 unreserved characters stay).
pub fn percent_encode(s: &str) -> String {
    let mut out = String::new();
    for b in s.bytes() {
        if b.is_ascii_alphanumeric() || matches!(b, b'-' | b'.' | b'_' | b'~') {
            out.push(b as char);
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    out
}

fn percent_decode(s: &str) -> String {
    let b = s.as_bytes();
    let mut out = Vec::new();
    let mut i = 0;
    while i < b.len() {
        if b[i] == b'%' && i + 2 < b.len() {
            let hex = |c: u8| (c as char).to_digit(16);
            if let (Some(h), Some(l)) = (hex(b[i + 1]), hex(b[i + 2])) {
                out.push((h * 16 + l) as u8);
                i += 3;
                continue;
            }
        }
        out.push(if b[i] == b'+' { b' ' } else { b[i] });
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// The pairs of a query string (`a=1&b=two`).
pub fn parse_query(q: &str) -> HashMap<String, String> {
    q.split('&')
        .filter(|p| !p.is_empty())
        .map(|p| {
            let (k, v) = p.split_once('=').unwrap_or((p, ""));
            (percent_decode(k), percent_decode(v))
        })
        .collect()
}

/// A one-shot HTTP listener on 127.0.0.1 that receives an OAuth redirect.
pub struct Loopback {
    listener: TcpListener,
    port: u16,
}

impl Loopback {
    /// Listens on 127.0.0.1 and a port the system picks.
    pub fn bind() -> Result<Loopback, Error> {
        let listener = TcpListener::bind("127.0.0.1:0").map_err(|e| err("refused", e.to_string()))?;
        let port = listener.local_addr().map_err(|e| err("refused", e.to_string()))?.port();
        Ok(Loopback { listener, port })
    }

    pub fn port(&self) -> u16 {
        self.port
    }

    /// `http://127.0.0.1:PORT/callback`: pass it through [`crate::Ext::external_uri`].
    pub fn redirect_uri(&self) -> String {
        format!("http://127.0.0.1:{}/callback", self.port)
    }

    /// Waits for the browser's redirect: its query, once its `state` equals `state` (an `error`
    /// parameter, a wrong state or the timeout is an `Err`). The browser is told "Signed in. You
    /// can close this tab."
    pub fn wait(&self, state: &str, timeout: Duration) -> Result<HashMap<String, String>, Error> {
        let end = Instant::now() + timeout;
        self.listener.set_nonblocking(true).map_err(|e| err("refused", e.to_string()))?;
        loop {
            match self.listener.accept() {
                Ok((stream, _)) => {
                    if let Some(q) = serve(stream, state)? {
                        return Ok(q);
                    }
                }
                Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                    if Instant::now() >= end {
                        return Err(err("cancelled", "no sign-in within the time allowed"));
                    }
                    std::thread::sleep(Duration::from_millis(50));
                }
                Err(e) => return Err(err("refused", e.to_string())),
            }
        }
    }
}

// one request: the redirect (its query), or None for another path (a favicon)
fn serve(mut stream: TcpStream, state: &str) -> Result<Option<HashMap<String, String>>, Error> {
    stream.set_nonblocking(false).ok();
    stream.set_read_timeout(Some(Duration::from_secs(5))).ok();
    let mut req = Vec::new();
    let mut buf = [0u8; 4096];
    while !req.windows(4).any(|w| w == b"\r\n\r\n") && req.len() < 65536 {
        match stream.read(&mut buf) {
            Ok(0) | Err(_) => break,
            Ok(n) => req.extend_from_slice(&buf[..n]),
        }
    }
    let text = String::from_utf8_lossy(&req);
    let target = text.split(' ').nth(1).unwrap_or("");
    let (path, query) = target.split_once('?').unwrap_or((target, ""));
    if path != "/callback" {
        let _ = stream.write_all(b"HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n");
        return Ok(None);
    }
    let q = parse_query(query);
    let ok = q.get("state").map(String::as_str) == Some(state) && q.contains_key("code");
    let body = if ok {
        "<!doctype html><title>Signed in</title><p>Signed in. You can close this tab and return to rhun."
    } else {
        "<!doctype html><title>Sign-in failed</title><p>Sign-in failed. You can close this tab."
    };
    let _ = write!(
        stream,
        "HTTP/1.1 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    );
    if let Some(e) = q.get("error") {
        return Err(err("refused", format!("the provider said {e}")));
    }
    if !ok {
        return Err(err("refused", "the redirect's state does not match"));
    }
    Ok(Some(q))
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn sha256_and_pkce() {
        let hex: String = sha256(b"abc").iter().map(|b| format!("{b:02x}")).collect();
        assert_eq!(hex, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
        // RFC 7636 appendix B
        let challenge = base64url(&sha256(b"dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"));
        assert_eq!(challenge, "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM");
        assert_eq!(Pkce::new().verifier.len(), 43);
        assert_eq!(random_hex(16).len(), 32);
    }
    #[test]
    fn loopback_receives_the_redirect() {
        let lb = Loopback::bind().unwrap();
        let port = lb.port();
        let t = std::thread::spawn(move || {
            let mut s = TcpStream::connect(("127.0.0.1", port)).unwrap();
            s.write_all(b"GET /callback?code=c%2B1&state=xyz HTTP/1.1\r\nHost: x\r\n\r\n").unwrap();
            let mut r = String::new();
            s.read_to_string(&mut r).unwrap();
            r
        });
        let q = lb.wait("xyz", Duration::from_secs(5)).unwrap();
        assert_eq!(q["code"], "c+1");
        assert!(t.join().unwrap().contains("Signed in"));
    }
}
