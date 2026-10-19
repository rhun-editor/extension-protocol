//! Protocol 1.1's language queries (`v1.1/v1.1.md`, "Language features"): parts of a long answer
//! and progress on one of rhun's pending requests (feature `query-stream`), and narrowing
//! `[provides]` while running (feature `provider-scope`).

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

use crate::{CompletionItem, CompletionList, Error, Ext, Feature, Location};

/// rhun's requests whose last field is the length of a body that follows (resolve data,
/// hierarchy items' data, a workspace-symbols query, selection-ranges' positions).
pub(crate) const BODIED: &[&str] = &[
    "complete-resolve",
    "code-action-resolve",
    "calls-in",
    "calls-out",
    "supertypes",
    "subtypes",
    "workspace-symbols",
    "selection-ranges",
];

/// The longest body one of rhun's requests carries; a longer one ends the session.
pub const MAX_REQUEST_BODY: usize = 64 << 10;

/// The most pending requests whose cancellation is tracked; past it a request's stream starts
/// cancelled.
const MAX_TRACKED: usize = 4096;

fn local(code: &str, text: &str) -> Error {
    Error {
        code: code.into(),
        text: text.into(),
    }
}

/// One of rhun's pending requests to the extension: [`Stream::part`] sends part of the answer at
/// once, [`Stream::progress`] how far the work has come. It is cancelled when rhun withdraws the
/// request (`0 cancel ID`), answers a part `cancelled` or `toobig`, the answer is sent, or the
/// session ends; then nothing more is sent.
///
/// Handlers run one at a time, and rhun's other requests and events wait while one runs. A
/// stream is `Clone` and `Send`: a long query may hand clones of it and of the [`Ext`] to worker
/// threads that send parts as they find results, and return the rest when they are done.
#[derive(Clone)]
pub struct Stream {
    ext: Ext,
    id: u64,
    cancelled: Arc<AtomicBool>,
}

impl std::fmt::Debug for Stream {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Stream")
            .field("id", &self.id)
            .field("cancelled", &self.is_cancelled())
            .finish()
    }
}

impl Stream {
    pub(crate) fn new(ext: &Ext, id: u64, cancelled: Arc<AtomicBool>) -> Stream {
        Stream {
            ext: ext.clone(),
            id,
            cancelled,
        }
    }

    /// The request's id.
    pub fn id(&self) -> u64 {
        self.id
    }

    /// Whether rhun no longer needs the answer: stop working on it.
    pub fn is_cancelled(&self) -> bool {
        self.cancelled.load(Ordering::SeqCst)
    }

    fn check(&self) -> Result<(), Error> {
        if !self.ext.has(Feature::QueryStream) {
            return Err(local("unknown", "query-stream was not agreed"));
        }
        if self.is_cancelled() {
            return Err(local("cancelled", "rhun no longer needs the answer"));
        }
        Ok(())
    }

    /// Sends part of the answer, in the grammar of the request's answer (the count line and that
    /// many records), and waits for rhun's reply: that is the flow control. An error `cancelled`
    /// or `toobig` means stop: rhun keeps what it has. Without query-stream agreed it fails
    /// without sending (`unknown`): return everything in the answer then.
    pub fn part(&self, body: &[u8]) -> Result<(), Error> {
        self.check()?;
        let r = self
            .ext
            .request(&format!("query-part {}", self.id), Some(body));
        if let Err(e) = &r {
            if e.code == "cancelled" || e.code == "toobig" {
                self.cancelled.store(true, Ordering::SeqCst);
            }
        }
        r.map(|_| ())
    }

    /// Streams locations (definition, references).
    pub fn part_locations(&self, locs: &[Location]) -> Result<(), Error> {
        self.part(&crate::lang::encode_locations(locs))
    }

    /// Streams completion items.
    pub fn part_items(&self, items: &[CompletionItem]) -> Result<(), Error> {
        self.part(&CompletionList::new(items.to_vec()).encode_bytes())
    }

    /// Reports how far the work has come: `percent` 0 to 100 (or `None`), and one line of text,
    /// cut at 200 bytes at a character. Each report extends the request's time limit.
    pub fn progress(&self, percent: Option<u8>, text: &str) -> Result<(), Error> {
        self.check()?;
        let p = percent.map_or("-".to_string(), |p| p.min(100).to_string());
        let mut t: String = text
            .chars()
            .map(|c| if c == '\n' || c == '\r' { ' ' } else { c })
            .collect();
        if t.len() > 200 {
            let mut cut = 200;
            while !t.is_char_boundary(cut) {
                cut -= 1;
            }
            t.truncate(cut);
        }
        let r = self.ext.request(
            &format!("query-progress {} {p}", self.id),
            Some(t.as_bytes()),
        );
        if let Err(e) = &r {
            if e.code == "cancelled" {
                self.cancelled.store(true, Ordering::SeqCst);
            }
        }
        r.map(|_| ())
    }
}

impl Ext {
    /// Starts tracking one of rhun's requests (from the reader, before its `cancel` can come).
    pub(crate) fn begin_query(&self, id: u64) {
        let mut q = self.queries.lock().unwrap();
        if q.len() < MAX_TRACKED && !q.contains_key(&id) {
            q.insert(id, Arc::new(AtomicBool::new(false)));
        }
    }

    /// Marks one of rhun's requests cancelled, or all of them.
    pub(crate) fn cancel_query(&self, id: Option<u64>) {
        let q = self.queries.lock().unwrap();
        for (k, flag) in q.iter() {
            if id.is_none() || id == Some(*k) {
                flag.store(true, Ordering::SeqCst);
            }
        }
    }

    /// The stream of one of rhun's requests: a cancelled one when it is not tracked.
    pub(crate) fn stream(&self, id: u64) -> Stream {
        let flag = self.queries.lock().unwrap().get(&id).cloned();
        Stream::new(
            self,
            id,
            flag.unwrap_or_else(|| Arc::new(AtomicBool::new(true))),
        )
    }

    /// The request is answered: its stream is cancelled and forgotten.
    pub(crate) fn finish_query(&self, id: u64) {
        if let Some(flag) = self.queries.lock().unwrap().remove(&id) {
            flag.store(true, Ordering::SeqCst);
        }
    }

    /// Narrows a `[provides]` key until the process stops (feature `provider-scope`): `values`
    /// is a subset of the manifest's own values for `key`, each written as there; none withdraws
    /// the kind. Without the feature agreed it fails without sending (`unknown`).
    pub fn provides(&self, key: &str, values: &[&str]) -> Result<(), Error> {
        if values.is_empty() {
            return self.withdraw(key);
        }
        self.provides_line(key, &values.join(" "))
    }

    /// Restores the manifest's values for `key` (feature `provider-scope`).
    pub fn provides_manifest(&self, key: &str) -> Result<(), Error> {
        self.provides_line(key, "*")
    }

    /// Stops rhun asking for `key`'s kind until [`Ext::provides_manifest`] or [`Ext::provides`]
    /// (feature `provider-scope`).
    pub fn withdraw(&self, key: &str) -> Result<(), Error> {
        self.provides_line(key, "-")
    }

    fn provides_line(&self, key: &str, values: &str) -> Result<(), Error> {
        if !self.has(Feature::ProviderScope) {
            return Err(local("unknown", "provider-scope was not agreed"));
        }
        if key.is_empty() || key.contains([' ', '\n', '\r']) || values.contains(['\n', '\r']) {
            return Err(local("syntax", "a key is one word and values one line"));
        }
        self.request(&format!("provides {key} {values}"), None)
            .map(|_| ())
    }
}
