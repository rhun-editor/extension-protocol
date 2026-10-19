//! Structural validation of self-contained native snippet transform payloads.
//! Validation borrows bytes without allocating, retaining, or executing programs.

use std::fmt;

/// The absolute byte offset of the first invalid payload value.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct SnippetTransformError {
    pub offset: usize,
}

impl fmt::Display for SnippetTransformError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            f,
            "snippet transforms byte {}: invalid program",
            self.offset
        )
    }
}
impl std::error::Error for SnippetTransformError {}

/// A diagnostic in an imported regex/replacement program. Part program and
/// offset zero denote a combined size error.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct SnippetTransformProgramError {
    pub part: &'static str,
    pub offset: usize,
}
impl fmt::Display for SnippetTransformProgramError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            f,
            "snippet transform {} byte {}: invalid program",
            self.part, self.offset
        )
    }
}
impl std::error::Error for SnippetTransformProgramError {}

/// Owned immutable compiled programs. Copies share immutable bytes. The default
/// value is invalid when supplied as a transform.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct CompiledSnippetTransform {
    regex: std::sync::Arc<[u8]>,
    replacement: std::sync::Arc<[u8]>,
}
impl CompiledSnippetTransform {
    /// Import canonical RNRX/RNRF v1 programs, owning copies after validation.
    pub fn from_programs(
        regex: &[u8],
        replacement: &[u8],
    ) -> Result<Self, SnippetTransformProgramError> {
        validate_pair(regex, replacement)?;
        Ok(Self {
            regex: std::sync::Arc::from(regex),
            replacement: std::sync::Arc::from(replacement),
        })
    }
    /// Borrow immutable program bytes for interchange with another SDK.
    pub fn programs(&self) -> (&[u8], &[u8]) {
        (&self.regex, &self.replacement)
    }
    pub fn validate(&self) -> Result<(), SnippetTransformProgramError> {
        validate_pair(&self.regex, &self.replacement)
    }
}
/// A compiled transform for one one-based positive-group field occurrence.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SnippetFieldTransform {
    pub field: usize,
    pub transform: CompiledSnippetTransform,
}
fn pair_size(r: usize, f: usize) -> Result<(), SnippetTransformProgramError> {
    if r > 262144 {
        return Err(SnippetTransformProgramError {
            part: "regex",
            offset: 40,
        });
    }
    if f > 4 << 20 {
        return Err(SnippetTransformProgramError {
            part: "replacement",
            offset: 20,
        });
    }
    if f > (4 << 20) - 56 - r {
        return Err(SnippetTransformProgramError {
            part: "program",
            offset: 0,
        });
    }
    Ok(())
}
fn validate_pair(r: &[u8], f: &[u8]) -> Result<(), SnippetTransformProgramError> {
    pair_size(r.len(), f.len())?;
    regex(r).map_err(|offset| SnippetTransformProgramError {
        part: "regex",
        offset,
    })?;
    replacement(f).map_err(|offset| SnippetTransformProgramError {
        part: "replacement",
        offset,
    })
}

/// Validate canonical SNTX v1 bindings and their RNRX/RNRF programs.
/// Field existence and group ownership require a prepared snippet and are not
/// checked here. No bytes are retained and no transform is executed.
pub fn validate_snippet_transforms(data: &[u8]) -> Result<(), SnippetTransformError> {
    payload(data).map_err(|offset| SnippetTransformError { offset })
}

fn word(data: &[u8], at: usize) -> u32 {
    u32::from_le_bytes([data[at], data[at + 1], data[at + 2], data[at + 3]])
}
fn scalar(n: u32) -> bool {
    n <= 0x10ffff && !(0xd800..=0xdfff).contains(&n)
}
fn header(data: &[u8], fields: &[(usize, u32)]) -> Result<(), usize> {
    for &(at, value) in fields {
        if word(data, at) != value {
            return Err(at);
        }
    }
    Ok(())
}

fn regex(data: &[u8]) -> Result<(), usize> {
    if data.len() < 48 {
        return Err(data.len());
    }
    if data.len() > 262144 {
        return Err(40);
    }
    header(data, &[(0, 0x58524e52), (4, 1), (8, 0x00100000), (44, 0)])?;
    let code = word(data, 20) as usize;
    let classes = word(data, 24) as usize;
    let ranges = word(data, 28) as usize;
    let repeats = word(data, 32) as usize;
    if code == 0 || code > 8192 {
        return Err(20);
    }
    if classes > 256 {
        return Err(24);
    }
    if ranges > 8192 {
        return Err(28);
    }
    if repeats > 256 {
        return Err(32);
    }
    let class_at = 48 + code * 16;
    let range_at = class_at + classes * 8;
    let repeat_at = range_at + ranges * 8;
    let size = repeat_at + repeats * 16;
    if size > 262144 || size != data.len() || word(data, 40) as usize != size {
        return Err(40);
    }
    if word(data, 12) & !127 != 0 {
        return Err(12);
    }
    let captures = word(data, 16);
    if captures > 64 {
        return Err(16);
    }
    if word(data, 36) as usize >= code {
        return Err(36);
    }
    let slots = 2 * (captures + 1);
    for i in 0..code {
        let at = 48 + i * 16;
        let op = word(data, at);
        let x = word(data, at + 4);
        let y = word(data, at + 8);
        let z = word(data, at + 12);
        match op {
            0 | 2 => {
                if x | y | z != 0 {
                    return Err(at + 4);
                }
            }
            1 | 3 | 4 | 5 | 9 | 10 => {
                let valid = match op {
                    1 => scalar(x),
                    3 => (x as usize) < classes,
                    4 => (1..=4).contains(&x),
                    5 => x >= 2 && x < slots,
                    9 => x >= 1 && x <= captures,
                    _ => (x as usize) < repeats,
                };
                if !valid {
                    return Err(at + 4);
                }
                if y | z != 0 {
                    return Err(at + 8);
                }
            }
            6 => {
                if x < 2
                    || x & 1 != 0
                    || y == 0
                    || y & 1 != 0
                    || u64::from(x) + u64::from(y) > u64::from(slots)
                {
                    return Err(at + 4);
                }
                if z != 0 {
                    return Err(at + 12);
                }
            }
            14 | 15 => {
                let mask = if op == 14 { 6 } else { 2 };
                if if op == 14 {
                    !(1..=4).contains(&x)
                } else {
                    x < 1 || x > captures
                } {
                    return Err(at + 4);
                }
                if y & !mask != 0 {
                    return Err(at + 8);
                }
                if z != 0 {
                    return Err(at + 12);
                }
            }
            7 | 8 | 11 | 12 | 13 => {
                if if op == 11 || op == 12 {
                    (x as usize) >= repeats
                } else {
                    (x as usize) >= code
                } {
                    return Err(at + 4);
                }
                if op == 8 {
                    if y | z != 0 {
                        return Err(at + 8);
                    }
                } else {
                    if (y as usize) >= code {
                        return Err(at + 8);
                    }
                    if op == 11 {
                        if (z as usize) >= code {
                            return Err(at + 12);
                        }
                    } else if (op == 13 && z & !3 != 0) || (op != 13 && z != 0) {
                        return Err(at + 12);
                    }
                }
            }
            _ => return Err(at),
        }
        if matches!(op, 1..=6 | 9 | 10 | 14 | 15) && i + 1 == code {
            return Err(at);
        }
    }
    let mut next = 0;
    for i in 0..classes {
        let at = class_at + i * 8;
        let count = word(data, at + 4) as usize;
        if word(data, at) as usize != next || count > ranges - next {
            return Err(at);
        }
        let mut previous = 0;
        for j in 0..count {
            let at = range_at + (next + j) * 8;
            let lo = word(data, at);
            let hi = word(data, at + 4);
            if !scalar(lo) || !scalar(hi) || lo > hi || lo < 0xd800 && hi > 0xdfff {
                return Err(at);
            }
            if j > 0 && previous + 1 >= lo {
                return Err(at);
            }
            previous = hi;
        }
        next += count;
    }
    if next != ranges {
        return Err(range_at + next * 8);
    }
    for i in 0..repeats {
        let at = repeat_at + i * 16;
        let min = word(data, at);
        let max = word(data, at + 4);
        if min > 0x7fffffff {
            return Err(at);
        }
        if max != 0xffffffff && (max > 0x7fffffff || max < min) {
            return Err(at + 4);
        }
        if word(data, at + 8) & !1 != 0 {
            return Err(at + 8);
        }
        if word(data, at + 12) != 0 {
            return Err(at + 12);
        }
    }
    Ok(())
}

fn replacement(data: &[u8]) -> Result<(), usize> {
    if data.len() < 32 {
        return Err(data.len());
    }
    if data.len() > 4 << 20 {
        return Err(20);
    }
    header(
        data,
        &[(0, 0x46524e52), (4, 1), (8, 0x00100000), (24, 0), (28, 0)],
    )?;
    let count = word(data, 12) as usize;
    if count > 8192 {
        return Err(12);
    }
    let blob = 32 + count * 24;
    let text_size = u64::from(word(data, 16));
    let size = blob as u64 + text_size;
    if size > 4 << 20 || size != data.len() as u64 || u64::from(word(data, 20)) != size {
        return Err(20);
    }
    let text = std::str::from_utf8(&data[blob..]).map_err(|e| blob + e.valid_up_to())?;
    let mut cursor = 0;
    for i in 0..count {
        let at = 32 + i * 24;
        let op = word(data, at);
        let index = word(data, at + 4);
        if op > 11 {
            return Err(at);
        }
        if index > 0x7fffffff || op == 0 && index != 0 {
            return Err(at + 4);
        }
        for (at, used) in [(at + 8, op == 0 || op >= 9), (at + 16, op == 11)] {
            let offset = word(data, at) as usize;
            let len = word(data, at + 4) as usize;
            if !used {
                if offset | len != 0 {
                    return Err(at);
                }
                continue;
            }
            if offset != cursor {
                return Err(at);
            }
            if len > text.len() - cursor {
                return Err(at + 4);
            }
            if !text.is_char_boundary(cursor) {
                return Err(at);
            }
            if !text.is_char_boundary(cursor + len) {
                return Err(at + 4);
            }
            cursor += len;
        }
    }
    if cursor != text.len() {
        return Err(blob + cursor);
    }
    Ok(())
}

fn payload(data: &[u8]) -> Result<(), usize> {
    if data.len() < 32 {
        return Err(data.len());
    }
    if data.len() > 4 << 20 {
        return Err(16);
    }
    header(
        data,
        &[
            (0, 0x58544e53),
            (4, 1),
            (8, 0x00100000),
            (20, 0),
            (24, 0),
            (28, 0),
        ],
    )?;
    let count = word(data, 12) as usize;
    if count > 512 {
        return Err(12);
    }
    let mut cursor = 32 + count * 24;
    if word(data, 16) as usize != data.len() || cursor > data.len() {
        return Err(16);
    }
    let mut previous = 0;
    for i in 0..count {
        let at = 32 + i * 24;
        let field = word(data, at);
        if field <= previous || field > 512 {
            return Err(at);
        }
        if word(data, at + 20) != 0 {
            return Err(at + 20);
        }
        if word(data, at + 4) as usize != cursor {
            return Err(at + 4);
        }
        let rlen = word(data, at + 8) as usize;
        if rlen > data.len() - cursor {
            return Err(at + 8);
        }
        let roff = cursor;
        cursor += rlen;
        if word(data, at + 12) as usize != cursor {
            return Err(at + 12);
        }
        let flen = word(data, at + 16) as usize;
        if flen > data.len() - cursor {
            return Err(at + 16);
        }
        let foff = cursor;
        cursor += flen;
        regex(&data[roff..foff]).map_err(|e| roff + e)?;
        replacement(&data[foff..cursor]).map_err(|e| foff + e)?;
        previous = field;
    }
    if cursor != data.len() {
        return Err(16);
    }
    Ok(())
}

pub(crate) fn bindings_size(
    bindings: &[SnippetFieldTransform],
    fields: &[crate::Field],
) -> Result<(usize, [usize; 512]), String> {
    let mut order = [0; 512];
    if bindings.is_empty() {
        return Ok((0, order));
    }
    if bindings.len() > 512 {
        return Err("too many snippet transforms".into());
    }
    let mut size = 32 + bindings.len() * 24;
    for (i, b) in bindings.iter().enumerate() {
        if b.field == 0
            || b.field > 512
            || b.field > fields.len()
            || fields[b.field - 1].group == 0
            || order[b.field - 1] != 0
        {
            return Err("invalid snippet transform field".into());
        }
        order[b.field - 1] = i + 1;
        let (r, f) = b.transform.programs();
        pair_size(r.len(), f.len()).map_err(|e| e.to_string())?;
        for n in [r.len(), f.len()] {
            if n > (4 << 20) - size {
                return Err("snippet transforms exceed byte limit".into());
            }
            size += n;
        }
    }
    Ok((size, order))
}
pub(crate) fn validate_bindings(bindings: &[SnippetFieldTransform]) -> Result<(), String> {
    for b in bindings {
        b.transform
            .validate()
            .map_err(|e| format!("snippet field {}: {e}", b.field))?;
    }
    Ok(())
}
pub(crate) fn bindings_extra_size(raw: usize) -> usize {
    if raw == 0 {
        return 0;
    }
    let encoded = 4 * raw.div_ceil(3);
    encoded + format!("transforms 0 {encoded}\n").len()
}
fn put(data: &mut [u8], at: usize, value: usize) {
    data[at..at + 4].copy_from_slice(&(value as u32).to_le_bytes());
}
fn base64(data: &[u8]) -> String {
    const ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::with_capacity(4 * data.len().div_ceil(3));
    for b in data.chunks(3) {
        let a = u32::from(b[0]);
        let c = if b.len() > 1 { u32::from(b[1]) } else { 0 };
        let d = if b.len() > 2 { u32::from(b[2]) } else { 0 };
        let n = (a << 16) | (c << 8) | d;
        out.push(ALPHABET[(n >> 18) as usize] as char);
        out.push(ALPHABET[((n >> 12) & 63) as usize] as char);
        out.push(if b.len() > 1 {
            ALPHABET[((n >> 6) & 63) as usize] as char
        } else {
            '='
        });
        out.push(if b.len() > 2 {
            ALPHABET[(n & 63) as usize] as char
        } else {
            '='
        });
    }
    out
}
pub(crate) fn bindings_extra(
    bindings: &[SnippetFieldTransform],
    fields: &[crate::Field],
) -> String {
    let (size, order) = bindings_size(bindings, fields).expect("validated snippet transforms");
    if size == 0 {
        return String::new();
    }
    let mut raw = vec![0; size];
    put(&mut raw, 0, 0x58544e53);
    put(&mut raw, 4, 1);
    put(&mut raw, 8, 0x00100000);
    put(&mut raw, 12, bindings.len());
    put(&mut raw, 16, size);
    let mut cursor = 32 + bindings.len() * 24;
    let mut record = 32;
    for (field, index) in order.into_iter().enumerate() {
        if index == 0 {
            continue;
        }
        let (r, f) = bindings[index - 1].transform.programs();
        put(&mut raw, record, field + 1);
        put(&mut raw, record + 4, cursor);
        put(&mut raw, record + 8, r.len());
        raw[cursor..cursor + r.len()].copy_from_slice(r);
        cursor += r.len();
        put(&mut raw, record + 12, cursor);
        put(&mut raw, record + 16, f.len());
        raw[cursor..cursor + f.len()].copy_from_slice(f);
        cursor += f.len();
        record += 24;
    }
    let encoded = base64(&raw);
    format!("transforms 0 {}\n{encoded}", encoded.len())
}

#[cfg(test)]
mod tests {
    use super::*;
    struct Fixture {
        base: &'static [u8],
        len: usize,
        patch: isize,
        value: u8,
        valid: bool,
        offset: usize,
    }
    include!("snippet_transform_fixtures.rs");

    #[test]
    fn shared_validation_and_unchanged_input() {
        let max = ST_FIXTURES.iter().map(|v| v.len).max().unwrap();
        let mut bytes = vec![0; max];
        for (i, v) in ST_FIXTURES.iter().enumerate() {
            bytes[..v.len].copy_from_slice(&v.base[..v.len]);
            if v.patch >= 0 {
                bytes[v.patch as usize] = v.value;
            }
            let got = validate_snippet_transforms(&bytes[..v.len]);
            assert_eq!(got.is_ok(), v.valid, "fixture {i}: {got:?}");
            assert_eq!(got.err().map_or(0, |e| e.offset), v.offset, "fixture {i}");
            for (j, &byte) in bytes[..v.len].iter().enumerate() {
                assert_eq!(
                    byte,
                    if v.patch >= 0 && v.patch as usize == j {
                        v.value
                    } else {
                        v.base[j]
                    },
                    "fixture {i} byte {j}"
                );
            }
        }
    }
    #[test]
    fn handwritten_empty_and_precedence() {
        let mut empty = [0u8; 32];
        empty[..4].copy_from_slice(b"SNTX");
        empty[4] = 1;
        empty[10] = 16;
        empty[16] = 32;
        assert_eq!(validate_snippet_transforms(&empty), Ok(()));
        empty[0] = 0;
        empty[20] = 1;
        assert_eq!(
            validate_snippet_transforms(&empty),
            Err(SnippetTransformError { offset: 0 })
        );
        empty[0] = b'S';
        let error = validate_snippet_transforms(&empty).unwrap_err();
        assert_eq!(error.offset, 20);
        assert_eq!(
            error.to_string(),
            "snippet transforms byte 20: invalid program"
        );
        assert_eq!(validate_snippet_transforms(&[]).unwrap_err().offset, 0);
        assert_eq!(
            validate_snippet_transforms(&empty[..31])
                .unwrap_err()
                .offset,
            31
        );
    }
    #[test]
    fn compiled_import_ownership_and_errors() {
        let v = ST_FIXTURES
            .iter()
            .find(|v| v.valid && v.patch < 0 && word(v.base, 12) > 0)
            .unwrap();
        let p = &v.base[..v.len];
        let roff = word(p, 36) as usize;
        let foff = word(p, 44) as usize;
        let flen = word(p, 48) as usize;
        let mut r = p[roff..foff].to_vec();
        let mut f = p[foff..foff + flen].to_vec();
        let program = CompiledSnippetTransform::from_programs(&r, &f).unwrap();
        r[0] = 0;
        f[0] = 0;
        assert!(program.validate().is_ok());
        let cloned = program.clone();
        assert!(std::sync::Arc::ptr_eq(&cloned.regex, &program.regex));
        let (r, f) = program.programs();
        assert_eq!(
            CompiledSnippetTransform::from_programs(r, f).unwrap(),
            program
        );
        let error = CompiledSnippetTransform::default().validate().unwrap_err();
        assert_eq!(
            error,
            SnippetTransformProgramError {
                part: "regex",
                offset: 0
            }
        );
        assert!(!error.to_string().is_empty());
        assert_eq!(
            CompiledSnippetTransform::from_programs(r, &[])
                .unwrap_err()
                .part,
            "replacement"
        );
        assert_eq!(
            pair_size(262145, 0).unwrap_err(),
            SnippetTransformProgramError {
                part: "regex",
                offset: 40
            }
        );
        assert_eq!(pair_size(0, (4 << 20) + 1).unwrap_err().part, "replacement");
        assert_eq!(pair_size(80, 4 << 20).unwrap_err().part, "program");
        fn send_sync<T: Send + Sync>() {}
        send_sync::<CompiledSnippetTransform>();
    }

    #[test]
    fn concurrent_borrowed_validation() {
        let cases: Vec<_> = ST_FIXTURES
            .iter()
            .filter(|v| v.valid && v.patch < 0)
            .collect();
        std::thread::scope(|scope| {
            for _ in 0..8 {
                let cases = &cases;
                scope.spawn(move || {
                    for _ in 0..20 {
                        for v in cases {
                            assert!(validate_snippet_transforms(&v.base[..v.len]).is_ok());
                        }
                    }
                });
            }
        });
    }
}

#[cfg(test)]
mod wire_tests {
    use super::*;
    use crate::{
        CompletionItem, CompletionList, Field, Kind, PreparedSnippet, Range, SnippetProgram,
    };
    fn program() -> SnippetProgram {
        let regex: &[u8] = &[
            82, 78, 82, 88, 1, 0, 0, 0, 0, 0, 16, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0,
            0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 80, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 97, 0, 0,
            0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
        ];
        let replacement: &[u8] = &[
            82, 78, 82, 70, 1, 0, 0, 0, 0, 0, 16, 0, 1, 0, 0, 0, 1, 0, 0, 0, 57, 0, 0, 0, 0, 0, 0,
            0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
            120,
        ];
        SnippetProgram {
            snippet: PreparedSnippet {
                text: "a".into(),
                fields: vec![Field {
                    group: 1,
                    start: 0,
                    end: 1,
                    parent: 0,
                }],
            },
            transforms: vec![SnippetFieldTransform {
                field: 1,
                transform: CompiledSnippetTransform::from_programs(regex, replacement).unwrap(),
            }],
            ..SnippetProgram::default()
        }
    }
    #[test]
    fn golden_snapshot_and_replacement() {
        let mut p = program();
        assert_eq!(p.encode().unwrap(),concat!("0 0 0 0 snippet 0 1 0 293\nafield 1 0 1 0 0\ntransforms 0 260\n","U05UWAEAAAAAABAAAQAAAMEAAAAAAAAAAAAAAAAAAAABAAAAOAAAAFAAAACIAAAAOQAAAAAAAABSTlJYAQAAAAAAEAAAAAAAAAAAAAIAAAAAAAAAAAAAAAAAAAAAAAAAUAAAAAAAAAABAAAAYQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAFJOUkYBAAAAAAAQAAEAAAABAAAAOQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAQAAAAAAAAAAAAAAeA==").as_bytes());
        let item =
            CompletionItem::new(Range::default(), Kind::Text, "label").snippet_program(p.clone());
        p.transforms[0].field = 0;
        p.snippet.fields[0].group = 0;
        assert!(CompletionList::new(vec![item.clone()]).encode().contains("transforms 0 260\nU05UWAEAAAAAABAAAQAAAMEAAAAAAAAAAAAAAAAAAAABAAAAOAAAAFAAAACIAAAAOQAAAAAAAABSTlJYAQAAAAAAEAAAAAAAAAAAAAIAAAAAAAAAAAAAAAAAAAAAAAAAUAAAAAAAAAABAAAAYQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAFJOUkYBAAAAAAAQAAEAAAABAAAAOQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAQAAAAAAAAAAAAAAeA=="));
        assert!(
            !CompletionList::new(vec![item.snippet(PreparedSnippet::default())])
                .encode()
                .contains("transforms ")
        );
    }
    #[test]
    fn ordering_and_invalid_metadata_fallback() {
        let mut p = program();
        p.snippet.text = "a a".into();
        p.snippet.fields.push(Field {
            group: 1,
            start: 2,
            end: 3,
            parent: 0,
        });
        let t = p.transforms[0].transform.clone();
        p.transforms = vec![
            SnippetFieldTransform {
                field: 2,
                transform: t.clone(),
            },
            SnippetFieldTransform {
                field: 1,
                transform: t.clone(),
            },
        ];
        let before = p.transforms.clone();
        let out = p.encode().unwrap();
        assert_eq!(before, p.transforms);
        p.transforms.reverse();
        assert_eq!(out, p.encode().unwrap());
        p.snippet.fields.push(Field {
            group: 0,
            start: 3,
            end: 3,
            parent: 0,
        });
        for bindings in [
            vec![SnippetFieldTransform {
                field: 0,
                transform: t.clone(),
            }],
            vec![SnippetFieldTransform {
                field: 4,
                transform: t.clone(),
            }],
            vec![SnippetFieldTransform {
                field: 3,
                transform: t.clone(),
            }],
            vec![before[0].clone(); 2],
            vec![SnippetFieldTransform {
                field: 1,
                ..SnippetFieldTransform::default()
            }],
            vec![before[0].clone(); 513],
        ] {
            p.transforms = bindings;
            assert!(p.encode().is_err());
            let body = CompletionList::new(vec![CompletionItem::new(
                Range::default(),
                Kind::Text,
                "label",
            )
            .snippet_program(p.clone())])
            .encode();
            assert!(body.ends_with("labela a"));
            assert!(!body.contains("field "));
            assert!(!body.contains("transforms "));
        }
    }
    #[test]
    fn exact_completion_boundary_and_one_over() {
        let mut p = program();
        p.snippet.text.push_str(&"z".repeat((1 << 20) - 1024));
        let item = |p: SnippetProgram| {
            CompletionItem::new(Range::default(), Kind::Text, "label").snippet_program(p)
        };
        let size = CompletionList::new(vec![item(p.clone())]).encode().len() - 4 + 32;
        p.snippet.text.push_str(&"z".repeat((1 << 20) - size));
        assert_eq!(
            CompletionList::new(vec![item(p.clone())]).encode().len() - 4 + 32,
            1 << 20
        );
        p.snippet.text.push('z');
        assert!(p.validate().is_ok());
        assert_eq!(CompletionList::new(vec![item(p)]).encode(), "0 1\n");
    }
}
