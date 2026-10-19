//! Coalesced workspace invalidations. Reconcile disk after either event kind.
use crate::{Error, Event, Ext, Feature};

pub type WorkspaceWatch = i64;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum WorkspaceWatchEvent {
    Changed,
    Rescan,
}

pub fn parse_workspace_watch(event: &Event) -> Option<(WorkspaceWatch, WorkspaceWatchEvent)> {
    if event.name != "watch" || event.fields.len() != 2 {
        return None;
    }
    let handle = parse_handle(&event.fields[0])?;
    let kind = match event.fields[1].as_str() {
        "changed" => WorkspaceWatchEvent::Changed,
        "rescan" => WorkspaceWatchEvent::Rescan,
        _ => return None,
    };
    Some((handle, kind))
}

fn parse_handle(raw: &str) -> Option<i64> {
    if raw.is_empty() || raw.len() > 10 || !raw.bytes().all(|c| c.is_ascii_digit()) {
        return None;
    }
    raw.parse::<i32>().ok().filter(|id| *id > 0).map(i64::from)
}

impl Ext {
    pub fn watch_workspace(&self) -> Result<WorkspaceWatch, Error> {
        if !self.has(Feature::WorkspaceWatch) {
            return Err(Error {
                code: "unknown".into(),
                text: "workspace_watch was not negotiated".into(),
            });
        }
        let reply = self.request("watch-workspace", None)?;
        if reply.fields.len() == 1 {
            if let Some(handle) = parse_handle(&reply.fields[0]) {
                return Ok(handle);
            }
        }
        Err(Error {
            code: "syntax".into(),
            text: "invalid workspace watch handle".into(),
        })
    }

    pub fn unwatch_workspace(&self, watch: WorkspaceWatch) -> Result<(), Error> {
        if !self.has(Feature::WorkspaceWatch) || watch <= 0 || watch > i64::from(i32::MAX) {
            return Err(Error {
                code: "syntax".into(),
                text: "invalid workspace watch".into(),
            });
        }
        self.request(&format!("unwatch-workspace {watch}"), None)?;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn invalidations_are_strictly_parsed() {
        for (word, kind) in [
            ("changed", WorkspaceWatchEvent::Changed),
            ("rescan", WorkspaceWatchEvent::Rescan),
        ] {
            let event = Event {
                name: "watch".into(),
                fields: vec!["7".into(), word.into()],
                ..Event::default()
            };
            assert_eq!(parse_workspace_watch(&event), Some((7, kind)));
        }
        for fields in [
            vec!["0", "changed"],
            vec!["-1", "rescan"],
            vec!["+7", "changed"],
            vec!["2147483648", "changed"],
            vec!["7", "unknown"],
            vec!["7", "changed", "extra"],
        ] {
            let event = Event {
                name: "watch".into(),
                fields: fields.into_iter().map(String::from).collect(),
                ..Event::default()
            };
            assert_eq!(parse_workspace_watch(&event), None);
        }
    }
}
