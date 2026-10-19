//! Custom editors (feature `custom-editors`, v1.1/v1.1.md): the extension shows a kind of file in a
//! view rhun opens for it. A **text** model edits rhun's document (the editor's `CDoc` is its
//! document id: use [`Ext::text`] and [`Ext::replace`]); an **owned** model keeps the file itself,
//! reads it with [`Ext::editor_read`], says whether it differs from the file with
//! [`Ext::editor_dirty`] and gives rhun the bytes in parts when it saves
//! ([`crate::Handler::editor_save`]).

use crate::{Error, Ext, Feature, View};

/// A custom editor's document: the id of rhun's document for a text model, of a stand-in for an
/// owned one.
pub type CDoc = i64;

/// The most bytes an editor-read answer and an editor-save part carry.
pub const PART_MAX: usize = 4 << 20;
/// The most bytes a save carries in all.
pub const SAVE_MAX: usize = 1 << 30;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Model {
    Text,
    Owned,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SaveKind {
    Save,
    SaveAs,
    Backup,
}

/// `editor-open`: the file rhun opened in the extension's editor.
#[derive(Clone, Debug)]
pub struct EditorOpen {
    pub id: u64,
    pub cdoc: CDoc,
    /// The view rhun opened for the tab: draw it with [`Ext::draw`].
    pub view: View,
    pub model: Model,
    /// `file` (`backup` when a later revision restores unsaved changes).
    pub source: String,
    pub path: String,
}

impl EditorOpen {
    pub(crate) fn parse(id: u64, fields: &[String], body: &[u8]) -> Option<EditorOpen> {
        if fields.len() != 5 {
            return None;
        }
        Some(EditorOpen {
            id,
            cdoc: fields[0].parse().ok()?,
            view: fields[1].parse().ok()?,
            model: match fields[2].as_str() {
                "text" => Model::Text,
                "owned" => Model::Owned,
                _ => return None,
            },
            source: fields[3].clone(),
            path: String::from_utf8_lossy(body).into_owned(),
        })
    }
}

/// `editor-save`: which document, what kind of save, which part (from 0).
#[derive(Clone, Copy, Debug)]
pub struct EditorSave {
    pub id: u64,
    pub cdoc: CDoc,
    pub kind: SaveKind,
    pub part: u64,
}

impl EditorSave {
    pub(crate) fn parse(id: u64, fields: &[String]) -> Option<EditorSave> {
        if fields.len() != 3 {
            return None;
        }
        Some(EditorSave {
            id,
            cdoc: fields[0].parse().ok()?,
            kind: match fields[1].as_str() {
                "save" => SaveKind::Save,
                "saveas" => SaveKind::SaveAs,
                "backup" => SaveKind::Backup,
                _ => return None,
            },
            part: fields[2].parse().ok()?,
        })
    }
}

/// An editor-save answer's body: the MORE line, then the part.
pub fn save_part(more: bool, bytes: &[u8]) -> Vec<u8> {
    let mut b = Vec::with_capacity(bytes.len() + 2);
    b.extend_from_slice(if more { b"1\n" } else { b"0\n" });
    b.extend_from_slice(bytes);
    b
}

/// A part of `data` for `editor-save`: the bytes of part `part` of at most `size` bytes each
/// (at most [`PART_MAX`]), and whether another part follows.
pub fn part_of(data: &[u8], part: u64, size: usize) -> (bool, &[u8]) {
    let size = size.clamp(1, PART_MAX);
    let start = (part as usize).saturating_mul(size).min(data.len());
    let end = start.saturating_add(size).min(data.len());
    (end < data.len(), &data[start..end])
}

fn agreed(ext: &Ext) -> Result<(), Error> {
    if ext.has(Feature::CustomEditors) {
        Ok(())
    } else {
        Err(Error {
            code: "unknown".into(),
            text: "custom-editors was not agreed".into(),
        })
    }
}

impl Ext {
    /// Up to `max` bytes (at most [`PART_MAX`]) of the editor's file from `offset`; empty at its
    /// end.
    pub fn editor_read(&self, cdoc: CDoc, offset: u64, max: usize) -> Result<Vec<u8>, Error> {
        agreed(self)?;
        let max = max.min(PART_MAX);
        let r = self.request(&format!("editor-read {cdoc} {offset} {max}"), None)?;
        Ok(r.body)
    }

    /// The whole file, read in parts of 4 MB.
    pub fn editor_read_all(&self, cdoc: CDoc) -> Result<Vec<u8>, Error> {
        let mut data = Vec::new();
        loop {
            let part = self.editor_read(cdoc, data.len() as u64, PART_MAX)?;
            if part.is_empty() {
                return Ok(data);
            }
            data.extend_from_slice(&part);
        }
    }

    /// An owned model differs from its file (`true`) or not: the dirty dot and the close question.
    pub fn editor_dirty(&self, cdoc: CDoc, dirty: bool) -> Result<(), Error> {
        agreed(self)?;
        self.request(&format!("editor-dirty {cdoc} {}", dirty as u8), None).map(|_| ())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_and_parts() {
        let f: Vec<String> = ["3", "7", "owned", "file", "6"].iter().map(|s| s.to_string()).collect();
        let o = EditorOpen::parse(1, &f, b"/a.bin").unwrap();
        assert_eq!((o.cdoc, o.view, o.model, o.path.as_str()), (3, 7, Model::Owned, "/a.bin"));
        let f: Vec<String> = ["3", "saveas", "2"].iter().map(|s| s.to_string()).collect();
        let s = EditorSave::parse(1, &f).unwrap();
        assert_eq!((s.kind, s.part), (SaveKind::SaveAs, 2));
        let data = [1u8, 2, 3, 4, 5, 6, 7];
        assert_eq!(part_of(&data, 0, 3), (true, &data[0..3]));
        assert_eq!(part_of(&data, 2, 3), (false, &data[6..7]));
        assert_eq!(part_of(&[], 0, 3), (false, &[][..]));
        assert_eq!(save_part(true, b"ab"), b"1\nab".to_vec());
    }
}
