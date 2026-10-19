//! Prepared UTF-8 text with editable native fields (protocol 1.1, feature `snippets`).

use std::collections::HashMap;

pub const MAX_FIELDS: usize = 512;
pub const MAX_GROUPS: usize = 256;
pub const MAX_DEPTH: usize = 32;
pub const MAX_BODY: usize = 4 << 20;

/// An occurrence. The first positive group occurrence is primary, others are mirrors.
/// Group zero is the final selection, empty for a caret. Parent is zero or the one-based earlier primary index.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Field {
    pub group: u32,
    pub start: usize,
    pub end: usize,
    pub parent: usize,
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct PreparedSnippet {
    pub text: String,
    pub fields: Vec<Field>,
}

impl PreparedSnippet {
    pub fn validate(&self) -> Result<(), String> {
        self.validate_variants(&[false; MAX_FIELDS])
    }

    fn validate_variants(&self, variants: &[bool; MAX_FIELDS]) -> Result<(), String> {
        if self.text.len() > MAX_BODY || self.fields.len() > MAX_FIELDS {
            return Err("snippet text or fields exceed their limits".into());
        }
        let mut primary = HashMap::new();
        let mut groups = HashMap::new();
        let mut depths = vec![1; self.fields.len()];
        let mut final_seen = false;
        for (i, field) in self.fields.iter().enumerate() {
            if field.group > i32::MAX as u32
                || field.start > field.end
                || !self.text.is_char_boundary(field.start)
                || !self.text.is_char_boundary(field.end)
                || field.parent > i
            {
                return Err("invalid snippet field".into());
            }
            if field.parent != 0 {
                let p = self.fields[field.parent - 1];
                if p.group == 0
                    || primary.get(&p.group) != Some(&(field.parent - 1))
                    || p.group == field.group
                    || field.start < p.start
                    || field.end > p.end
                {
                    return Err("invalid snippet parent".into());
                }
                depths[i] = depths[field.parent - 1] + 1;
            }
            if depths[i] > MAX_DEPTH {
                return Err("snippet nesting exceeds its limit".into());
            }
            if field.group == 0 {
                if variants[i] {
                    return Err("final selection cannot be an initial variant".into());
                }
                if final_seen {
                    return Err("duplicate final snippet selection".into());
                }
                final_seen = true;
                continue;
            }
            if let Some(&first) = primary.get(&field.group) {
                let p: Field = self.fields[first];
                if (self.text[p.start..p.end] != self.text[field.start..field.end]) != variants[i] {
                    return Err("snippet mirrors contain different text".into());
                }
            } else {
                if variants[i] {
                    return Err("primary cannot be an initial variant".into());
                }
                if primary.len() == MAX_GROUPS {
                    return Err("too many snippet groups".into());
                }
                groups.insert(field.group, primary.len());
                primary.insert(field.group, i);
            }
        }
        let ancestor = |parent: usize, child: usize| {
            let mut p = self.fields[child].parent;
            while p != 0 {
                if p - 1 == parent {
                    return true;
                }
                p = self.fields[p - 1].parent;
            }
            false
        };
        for (i, a) in self.fields.iter().enumerate() {
            for (j, b) in self.fields[..i].iter().enumerate() {
                if a.group != 0
                    && b.group != 0
                    && a.start < b.end
                    && b.start < a.end
                    && (a.group == b.group || !ancestor(i, j) && !ancestor(j, i))
                {
                    return Err("snippet fields overlap without ancestry".into());
                }
            }
        }
        let n = primary.len();
        let mut edges = vec![vec![false; n]; n];
        let mut indegree = vec![0; n];
        for field in &self.fields {
            if field.group == 0 {
                continue;
            }
            let from = groups[&field.group];
            let mut p = field.parent;
            while p != 0 {
                let to = groups[&self.fields[p - 1].group];
                if !edges[from][to] {
                    edges[from][to] = true;
                    indegree[to] += 1;
                }
                p = self.fields[p - 1].parent;
            }
        }
        let mut queue: Vec<_> = (0..n).filter(|i| indegree[*i] == 0).collect();
        let mut at = 0;
        while at < queue.len() {
            for (to, edge) in edges[queue[at]].iter().enumerate() {
                if *edge {
                    indegree[to] -= 1;
                    if indegree[to] == 0 {
                        queue.push(to);
                    }
                }
            }
            at += 1;
        }
        if queue.len() != n {
            return Err("snippet mirroring has a dependency cycle".into());
        }
        if record_size(self.text.len(), self.extra_size()).is_none() {
            return Err("snippet body exceeds its limit".into());
        }
        Ok(())
    }

    fn extra_size(&self) -> usize {
        self.fields
            .iter()
            .map(|f| format!("field {} {} {} {} 0\n", f.group, f.start, f.end, f.parent).len())
            .sum()
    }

    pub(crate) fn extra(&self) -> String {
        let mut out = String::new();
        for f in &self.fields {
            out.push_str(&format!(
                "field {} {} {} {} 0\n",
                f.group, f.start, f.end, f.parent
            ));
        }
        out
    }

    fn record(&self) -> Vec<u8> {
        let extra = self.extra();
        format!(
            "0 0 0 0 snippet 0 {} 0 {}\n{}{}",
            self.text.len(),
            extra.len(),
            self.text,
            extra
        )
        .into_bytes()
    }

    pub fn encode(&self) -> Result<Vec<u8>, String> {
        self.validate()?;
        Ok(self.record())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{CompletionItem, CompletionList, Kind, Range};

    fn f(group: u32, start: usize, end: usize, parent: usize) -> Field {
        Field {
            group,
            start,
            end,
            parent,
        }
    }

    #[test]
    fn golden_and_empty_prepared_text() {
        let s = PreparedSnippet {
            text: "x x".into(),
            fields: vec![f(1, 0, 1, 0), f(1, 2, 3, 0), f(0, 3, 3, 0)],
        };
        assert_eq!(
            s.encode().unwrap(),
            b"0 0 0 0 snippet 0 3 0 48\nx xfield 1 0 1 0 0\nfield 1 2 3 0 0\nfield 0 3 3 0 0\n"
        );
        for fields in [vec![], vec![f(1, 0, 0, 0)], vec![f(0, 0, 0, 0)]] {
            let it = CompletionItem::new(Range::default(), Kind::Text, "label").snippet(
                PreparedSnippet {
                    text: String::new(),
                    fields,
                },
            );
            let body = CompletionList::new(vec![it]).encode();
            assert!(body.contains("text 5 0 0 "));
            assert!(!body.contains("labellabel"));
        }
        assert!(CompletionList::new(vec![CompletionItem::new(
            Range::default(),
            Kind::Text,
            "label"
        )])
        .encode()
        .ends_with("labellabel"));
    }

    #[test]
    fn nested_and_coincident_fields_are_valid() {
        for s in [
            PreparedSnippet {
                text: "x x".into(),
                fields: vec![f(1, 0, 1, 0), f(2, 0, 1, 1), f(2, 2, 3, 0), f(0, 3, 3, 0)],
            },
            PreparedSnippet {
                text: "😀".into(),
                fields: vec![f(1, 0, 4, 0), f(2, 0, 4, 1), f(0, 4, 4, 2)],
            },
            PreparedSnippet {
                text: String::new(),
                fields: vec![f(1, 0, 0, 0), f(2, 0, 0, 1), f(3, 0, 0, 0), f(0, 0, 0, 1)],
            },
        ] {
            assert!(s.validate().is_ok(), "{s:?}");
        }
    }

    #[test]
    fn final_selection_is_non_owning() {
        for s in [
            PreparedSnippet {
                text: "result".into(),
                fields: vec![f(0, 0, 6, 0)],
            },
            PreparedSnippet {
                text: "xxy".into(),
                fields: vec![f(1, 0, 1, 0), f(1, 1, 2, 0), f(0, 1, 3, 0)],
            },
            PreparedSnippet {
                text: "abc".into(),
                fields: vec![f(1, 0, 3, 0), f(0, 1, 2, 1)],
            },
        ] {
            assert!(s.validate().is_ok());
        }
        assert!(PreparedSnippet {
            text: "ab".into(),
            fields: vec![f(0, 0, 2, 0), f(1, 0, 1, 1)]
        }
        .validate()
        .is_err());
        assert_eq!(
            PreparedSnippet {
                text: "result".into(),
                fields: vec![f(0, 0, 6, 0)]
            }
            .encode()
            .unwrap(),
            b"0 0 0 0 snippet 0 6 0 16\nresultfield 0 0 6 0 0\n"
        );
    }

    #[test]
    fn reject_invalid_ranges_parents_and_group_cycles() {
        for (text, fields) in [
            ("😀", vec![f(1, 1, 4, 0)]),
            ("x", vec![f(1, 0, 2, 0)]),
            ("x", vec![f(1, 1, 0, 0)]),
            ("x", vec![f(u32::MAX, 0, 1, 0)]),
            ("", vec![f(0, 0, 0, 0), f(0, 0, 0, 0)]),
            ("x", vec![f(1, 0, 1, 1)]),
            ("x", vec![f(1, 0, 1, 2), f(2, 0, 1, 0)]),
            ("xx", vec![f(1, 0, 1, 0), f(2, 1, 2, 1)]),
            ("xx", vec![f(1, 0, 1, 0), f(1, 1, 2, 0), f(2, 1, 2, 2)]),
            ("xy", vec![f(1, 0, 1, 0), f(1, 1, 2, 0)]),
            ("xx", vec![f(1, 0, 2, 0), f(2, 1, 1, 0)]),
            ("xx", vec![f(1, 0, 2, 0), f(2, 0, 2, 0)]),
            ("xxx", vec![f(1, 0, 2, 0), f(2, 1, 3, 0)]),
            (
                "x x",
                vec![f(1, 0, 1, 0), f(2, 2, 3, 0), f(2, 0, 1, 1), f(1, 2, 3, 2)],
            ),
        ] {
            assert!(PreparedSnippet {
                text: text.into(),
                fields
            }
            .encode()
            .is_err());
        }
    }

    #[test]
    fn bounded_records_groups_nesting_and_framing() {
        let mut s = PreparedSnippet {
            text: String::new(),
            fields: vec![f(1, 0, 0, 0); MAX_FIELDS],
        };
        assert!(s.validate().is_ok());
        s.fields.push(f(1, 0, 0, 0));
        assert!(s.validate().is_err());
        s.fields = (1..=MAX_GROUPS as u32).map(|g| f(g, 0, 0, 0)).collect();
        assert!(s.validate().is_ok());
        s.fields.push(f(MAX_GROUPS as u32 + 1, 0, 0, 0));
        assert!(s.validate().is_err());
        s.fields = (0..MAX_DEPTH).map(|i| f(i as u32 + 1, 0, 0, i)).collect();
        assert!(s.validate().is_ok());
        s.fields.push(f(MAX_DEPTH as u32 + 1, 0, 0, MAX_DEPTH));
        assert!(s.validate().is_err());
        assert!(PreparedSnippet {
            text: "x".repeat(MAX_BODY),
            fields: vec![]
        }
        .validate()
        .is_err());
    }
}

/// Literal values for a primary field, in display order.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SnippetChoice {
    pub group: u32,
    pub options: Vec<String>,
}

/// Prepared text and fields with owned native choices.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SnippetProgram {
    pub snippet: PreparedSnippet,
    pub choices: Vec<SnippetChoice>,
    /// One-based nonprimary fields with unequal initial text. Requires snippet-variants.
    pub initial_variants: Vec<usize>,
    /// Compiled transforms for one-based field occurrences. Requires snippet-transforms.
    pub transforms: Vec<crate::SnippetFieldTransform>,
}

pub const MAX_CHOICE_OPTIONS: usize = 256;
pub const MAX_OPTION_BYTES: usize = 1 << 20;

fn record_size(text: usize, extra: usize) -> Option<usize> {
    let size = text
        .checked_add(extra)?
        .checked_add(format!("0 0 0 0 snippet 0 {text} 0 {extra}\n").len())?;
    (size <= MAX_BODY).then_some(size)
}

impl SnippetProgram {
    fn extra_size(&self) -> Result<usize, String> {
        if self.choices.len() > MAX_GROUPS {
            return Err("too many snippet choices".into());
        }
        let (raw_transforms, _) =
            crate::snippet_transform::bindings_size(&self.transforms, &self.snippet.fields)?;
        let mut seen = std::collections::HashSet::new();
        let mut total = self.snippet.extra_size();
        for index in &self.initial_variants {
            total += format!("variant {index} 0\n").len();
        }
        for c in &self.choices {
            if c.group == 0
                || !seen.insert(c.group)
                || c.options.is_empty()
                || c.options.len() > MAX_CHOICE_OPTIONS
            {
                return Err("invalid snippet choice group or count".into());
            }
            let f = self
                .snippet
                .fields
                .iter()
                .find(|f| f.group == c.group)
                .ok_or("snippet choice has no primary")?;
            if c.options[0] != self.snippet.text[f.start..f.end] {
                return Err("snippet choice initial value does not match its primary".into());
            }
            let mut size = 0;
            for option in &c.options {
                if option.len() > MAX_OPTION_BYTES {
                    return Err("snippet option exceeds its limit".into());
                }
                size += option.len() + format!("{}\n", option.len()).len();
                if size > MAX_BODY {
                    return Err("snippet choices exceed body limit".into());
                }
            }
            total += size + format!("choice {} {} {}\n", c.group, c.options.len(), size).len();
            if total > MAX_BODY {
                return Err("snippet choices exceed body limit".into());
            }
        }
        total += crate::snippet_transform::bindings_extra_size(raw_transforms);
        if record_size(self.snippet.text.len(), total).is_none() {
            return Err("snippet program exceeds body limit".into());
        }
        crate::snippet_transform::validate_bindings(&self.transforms)?;
        Ok(total)
    }

    /// Check all metadata and encoded sizes before allocating the body.
    pub fn validate(&self) -> Result<(), String> {
        if self.initial_variants.len() > MAX_FIELDS {
            return Err("too many initial variants".into());
        }
        let mut variants = [false; MAX_FIELDS];
        for &index in &self.initial_variants {
            if index == 0
                || index > self.snippet.fields.len()
                || index > MAX_FIELDS
                || variants[index - 1]
            {
                return Err("invalid initial variant index".into());
            }
            variants[index - 1] = true;
        }
        self.snippet.validate_variants(&variants)?;
        if record_size(self.snippet.text.len(), self.extra_size()?).is_none() {
            return Err("snippet program exceeds body limit".into());
        }
        Ok(())
    }

    pub(crate) fn extra(&self) -> String {
        let mut out = self.snippet.extra();
        for index in &self.initial_variants {
            out.push_str(&format!("variant {index} 0\n"));
        }
        for c in &self.choices {
            let size: usize = c
                .options
                .iter()
                .map(|s| s.len() + format!("{}\n", s.len()).len())
                .sum();
            out.push_str(&format!(
                "choice {} {} {}\n",
                c.group,
                c.options.len(),
                size
            ));
            for option in &c.options {
                out.push_str(&format!("{}\n", option.len()));
                out.push_str(option);
            }
        }
        out.push_str(&crate::snippet_transform::bindings_extra(
            &self.transforms,
            &self.snippet.fields,
        ));
        out
    }

    pub fn encode(&self) -> Result<Vec<u8>, String> {
        self.validate()?;
        let extra = self.extra();
        Ok(format!(
            "0 0 0 0 snippet 0 {} 0 {}\n{}{}",
            self.snippet.text.len(),
            extra.len(),
            self.snippet.text,
            extra
        )
        .into_bytes())
    }
}

#[cfg(test)]
mod program_tests {
    use super::*;
    use crate::{CompletionItem, CompletionList, Kind, Range};

    fn program() -> SnippetProgram {
        SnippetProgram {
            snippet: PreparedSnippet {
                text: "x x".into(),
                fields: vec![
                    Field {
                        group: 1,
                        start: 0,
                        end: 1,
                        parent: 0,
                    },
                    Field {
                        group: 1,
                        start: 2,
                        end: 3,
                        parent: 0,
                    },
                    Field {
                        group: 0,
                        start: 3,
                        end: 3,
                        parent: 0,
                    },
                ],
            },
            choices: vec![SnippetChoice {
                group: 1,
                options: vec!["x".into(), "".into(), "a\n😀".into()],
            }],
            initial_variants: vec![],
            transforms: vec![],
        }
    }

    #[test]
    fn golden_and_completion_ownership() {
        let p = program();
        assert_eq!(p.encode().unwrap(), "0 0 0 0 snippet 0 3 0 75\nx xfield 1 0 1 0 0\nfield 1 2 3 0 0\nfield 0 3 3 0 0\nchoice 1 3 13\n1\nx0\n6\na\n😀".as_bytes());
        let item =
            CompletionItem::new(Range::default(), Kind::Text, "label").snippet_program(p.clone());
        let out = CompletionList::new(vec![item.clone()]).encode();
        assert!(out.contains("choice 1 3 13\n1\nx0\n6\na\n😀"));
        assert!(!CompletionList::new(vec![item.clone().insert("other")])
            .encode()
            .contains("choice "));
        assert!(
            !CompletionList::new(vec![item.snippet(PreparedSnippet::default())])
                .encode()
                .contains("choice ")
        );
    }

    #[test]
    fn reject_invalid_programs_as_a_whole() {
        for i in 0..8 {
            let mut p = program();
            match i {
                0 => p.choices[0].group = 0,
                1 => p.choices[0].group = 2,
                2 => p.choices.push(p.choices[0].clone()),
                3 => p.choices[0].options.clear(),
                4 => {
                    p.choices[0].options = vec![String::new(); 257];
                    p.choices[0].options[0] = "x".into();
                }
                5 => p.choices[0].options[0] = "wrong".into(),
                6 => p.choices[0].options[1] = "x".repeat(MAX_OPTION_BYTES + 1),
                _ => {
                    p.choices[0].options = vec![
                        "x".into(),
                        "y".repeat(MAX_OPTION_BYTES),
                        "y".repeat(MAX_OPTION_BYTES),
                        "y".repeat(MAX_OPTION_BYTES),
                        "y".repeat(MAX_OPTION_BYTES),
                    ]
                }
            }
            assert!(p.validate().is_err());
            assert!(p.encode().is_err());
            let out = CompletionList::new(vec![CompletionItem::new(
                Range::default(),
                Kind::Text,
                "label",
            )
            .snippet_program(p)])
            .encode();
            assert!(out.ends_with("labelx x"));
            assert!(!out.contains("field "));
            assert!(!out.contains("choice "));
        }
    }

    #[test]
    fn largest_count_and_exact_frame_limit() {
        let mut p = program();
        p.choices[0].options = vec![String::new(); 256];
        p.choices[0].options[0] = "x".into();
        p.choices[0].options[255] = "last\0😀\n".into();
        assert!(p.validate().is_ok());
        p = SnippetProgram {
            initial_variants: vec![],
            transforms: vec![],
            snippet: PreparedSnippet {
                text: String::new(),
                fields: vec![Field {
                    group: 1,
                    ..Field::default()
                }],
            },
            choices: vec![SnippetChoice {
                group: 1,
                options: vec![
                    String::new(),
                    "a".repeat(MAX_OPTION_BYTES),
                    "b".repeat(MAX_OPTION_BYTES),
                    "c".repeat(MAX_OPTION_BYTES),
                    "d".repeat(MAX_OPTION_BYTES - 200),
                ],
            }],
        };
        let size = p.encode().unwrap().len();
        p.choices[0].options[4].push_str(&"d".repeat(MAX_BODY - size));
        assert_eq!(p.encode().unwrap().len(), MAX_BODY);
        p.choices[0].options[4].push('d');
        assert!(p.validate().is_err());
    }
}

#[cfg(test)]
mod variant_tests {
    use super::*;
    use crate::{CompletionItem, CompletionList, Kind, Range};
    fn program() -> SnippetProgram {
        SnippetProgram {
            snippet: PreparedSnippet {
                text: "a ABC".into(),
                fields: vec![
                    Field {
                        group: 1,
                        start: 0,
                        end: 1,
                        parent: 0,
                    },
                    Field {
                        group: 1,
                        start: 2,
                        end: 5,
                        parent: 0,
                    },
                    Field {
                        group: 0,
                        start: 5,
                        end: 5,
                        parent: 0,
                    },
                ],
            },
            initial_variants: vec![2],
            transforms: vec![],
            choices: vec![SnippetChoice {
                group: 1,
                options: vec!["a".into(), "next".into()],
            }],
        }
    }
    #[test]
    fn golden_and_validation() {
        let p = program();
        assert_eq!(p.encode().unwrap(),b"0 0 0 0 snippet 0 5 0 82\na ABCfield 1 0 1 0 0\nfield 1 2 5 0 0\nfield 0 5 5 0 0\nvariant 2 0\nchoice 1 2 9\n1\na4\nnext");
        assert!(p.snippet.validate().is_err());
        let it = CompletionItem::new(Range::default(), Kind::Text, "label").snippet_program(p);
        assert!(CompletionList::new(vec![it.clone()])
            .encode()
            .contains("variant 2 0\n"));
        assert!(!CompletionList::new(vec![it.insert("a a")])
            .encode()
            .contains("field "));
        for indices in [
            vec![],
            vec![0],
            vec![1],
            vec![3],
            vec![4],
            vec![usize::MAX],
            vec![2, 2],
            vec![2; 513],
        ] {
            let mut p = program();
            p.initial_variants = indices;
            assert!(p.validate().is_err());
        }
        let mut p = program();
        p.snippet.text = "a a".into();
        p.snippet.fields[1].end = 3;
        p.snippet.fields.truncate(2);
        assert!(p.validate().is_err());
        p = program();
        p.choices[0].options[0] = "ABC".into();
        assert!(p.validate().is_err());
    }
    #[test]
    fn maximum_declarations_and_encoded_limit() {
        let mut p = SnippetProgram {
            snippet: PreparedSnippet {
                text: "ab".into(),
                fields: vec![Field {
                    group: 1,
                    start: 0,
                    end: 1,
                    parent: 0,
                }],
            },
            ..SnippetProgram::default()
        };
        for i in 1..MAX_FIELDS {
            p.snippet.fields.push(Field {
                group: 1,
                start: 1,
                end: 1,
                parent: 0,
            });
            p.initial_variants.push(i + 1);
        }
        assert!(p.validate().is_ok());
        p = program();
        p.snippet.text.push_str(&"z".repeat(MAX_BODY - 200));
        let len = p.encode().unwrap().len();
        p.snippet.text.push_str(&"z".repeat(MAX_BODY - len));
        assert_eq!(p.encode().unwrap().len(), MAX_BODY);
        p.snippet.text.push('z');
        assert!(p.validate().is_err());
    }
}
