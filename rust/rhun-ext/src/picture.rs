//! Pictures for views (section 12a of the spec): shapes drawn in order, in logical pixels, in
//! colors that follow the theme. Build one, then [`crate::Ext::draw`] it.
//!
//! ```
//! use rhun_ext::{Color, Picture};
//! use rhun_ext::TextSize;
//! use rhun_ext::picture::Align;
//!
//! let mut p = Picture::new();
//! p.rect(10.0, 10.0, 100.0, 40.0, Color::Accent)
//!  .text(10.0, 60.0, TextSize::Normal, Align::Left, Color::Fg, "Hello");
//! assert!(p.body().starts_with("rect 10 10 100 40 accent\n"));
//! ```

use std::fmt::Write;

use crate::TextSize;

/// A color: one that follows the theme, a chart series, or an explicit one.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Color {
    Fg,
    Muted,
    Bg,
    Panel,
    Border,
    Accent,
    Error,
    Warning,
    Success,
    /// The n-th series color of a chart, 1 to 8 (wraps).
    Series(u8),
    /// `#rrggbb`, opaque.
    Rgb(u8, u8, u8),
    /// `#rrggbbaa`, blended over what is below.
    Rgba(u8, u8, u8, u8),
}

impl Color {
    /// Parses `#rrggbb` or `#rrggbbaa`.
    pub fn hex(s: &str) -> Option<Color> {
        let h = s.strip_prefix('#')?;
        let b = |i: usize| u8::from_str_radix(h.get(i..i + 2)?, 16).ok();
        match h.len() {
            6 => Some(Color::Rgb(b(0)?, b(2)?, b(4)?)),
            8 => Some(Color::Rgba(b(0)?, b(2)?, b(4)?, b(6)?)),
            _ => None,
        }
    }

    /// The same color with an alpha (theme colors cannot carry one: they stay as they are).
    pub fn alpha(self, a: u8) -> Color {
        match self {
            Color::Rgb(r, g, b) | Color::Rgba(r, g, b, _) => Color::Rgba(r, g, b, a),
            other => other,
        }
    }

    fn word(self) -> String {
        match self {
            Color::Fg => "fg".into(),
            Color::Muted => "muted".into(),
            Color::Bg => "bg".into(),
            Color::Panel => "panel".into(),
            Color::Border => "border".into(),
            Color::Accent => "accent".into(),
            Color::Error => "error".into(),
            Color::Warning => "warning".into(),
            Color::Success => "success".into(),
            Color::Series(n) => format!("c{}", (n.max(1) - 1) % 8 + 1),
            Color::Rgb(r, g, b) => format!("#{r:02x}{g:02x}{b:02x}"),
            Color::Rgba(r, g, b, a) => format!("#{r:02x}{g:02x}{b:02x}{a:02x}"),
        }
    }
}

/// Where a text is placed about its x.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Align {
    Left,
    Center,
    Right,
}

impl Align {
    fn word(self) -> &'static str {
        match self {
            Align::Left => "left",
            Align::Center => "center",
            Align::Right => "right",
        }
    }
}

/// A picture: its body, built shape by shape.
#[derive(Clone, Debug, Default)]
pub struct Picture {
    body: String,
    shapes: usize,
}

/// A number as the protocol writes it: at most two decimals, no trailing zeros, within the
/// range rhun reads (7 digits before the point).
pub fn num(v: f32) -> String {
    let v = if v.is_finite() {
        v.clamp(-9_999_999.0, 9_999_999.0)
    } else {
        0.0
    };
    let r = (v * 100.0).round() / 100.0;
    let s = format!("{r:.2}");
    let s = s.trim_end_matches('0').trim_end_matches('.');
    if s == "-0" {
        "0".to_string()
    } else {
        s.to_string()
    }
}

impl Picture {
    pub fn new() -> Picture {
        Picture::default()
    }

    /// The text rhun reads.
    pub fn body(&self) -> &str {
        &self.body
    }

    /// How many shapes it has.
    pub fn len(&self) -> usize {
        self.shapes
    }

    pub fn is_empty(&self) -> bool {
        self.shapes == 0
    }

    fn shape(&mut self, line: String) -> &mut Picture {
        self.body.push_str(&line);
        self.body.push('\n');
        self.shapes += 1;
        self
    }

    /// A filled rectangle.
    pub fn rect(&mut self, x: f32, y: f32, w: f32, h: f32, c: Color) -> &mut Picture {
        self.shape(format!(
            "rect {} {} {} {} {}",
            num(x),
            num(y),
            num(w.max(0.0)),
            num(h.max(0.0)),
            c.word()
        ))
    }

    /// A filled rectangle with rounded corners.
    pub fn rrect(&mut self, x: f32, y: f32, w: f32, h: f32, r: f32, c: Color) -> &mut Picture {
        self.shape(format!(
            "rrect {} {} {} {} {} {}",
            num(x),
            num(y),
            num(w.max(0.0)),
            num(h.max(0.0)),
            num(r.max(0.0)),
            c.word()
        ))
    }

    /// A line with round ends.
    pub fn line(
        &mut self,
        x1: f32,
        y1: f32,
        x2: f32,
        y2: f32,
        width: f32,
        c: Color,
    ) -> &mut Picture {
        self.shape(format!(
            "line {} {} {} {} {} {}",
            num(x1),
            num(y1),
            num(x2),
            num(y2),
            num(width.max(0.0)),
            c.word()
        ))
    }

    /// A polyline with round joins (2 to 10000 points); fewer than 2 points draw nothing.
    pub fn poly(&mut self, width: f32, c: Color, points: &[(f32, f32)]) -> &mut Picture {
        if points.len() < 2 {
            return self;
        }
        let mut s = format!("poly {} {}", num(width.max(0.0)), c.word());
        for (x, y) in points.iter().take(10_000) {
            let _ = write!(s, " {} {}", num(*x), num(*y));
        }
        self.shape(s)
    }

    /// A filled polygon (3 to 10000 points), nonzero rule.
    pub fn fill(&mut self, c: Color, points: &[(f32, f32)]) -> &mut Picture {
        if points.len() < 3 {
            return self;
        }
        let mut s = format!("fill {}", c.word());
        for (x, y) in points.iter().take(10_000) {
            let _ = write!(s, " {} {}", num(*x), num(*y));
        }
        self.shape(s)
    }

    /// A filled circle.
    pub fn circle(&mut self, x: f32, y: f32, r: f32, c: Color) -> &mut Picture {
        self.shape(format!(
            "circle {} {} {} {}",
            num(x),
            num(y),
            num(r.max(0.0)),
            c.word()
        ))
    }

    /// A circle's outline.
    pub fn ring(&mut self, x: f32, y: f32, r: f32, width: f32, c: Color) -> &mut Picture {
        self.shape(format!(
            "ring {} {} {} {} {}",
            num(x),
            num(y),
            num(r.max(0.0)),
            num(width.max(0.0)),
            c.word()
        ))
    }

    /// A filled wedge from `from` to `to` degrees, 0 at the top, clockwise.
    pub fn pie(&mut self, x: f32, y: f32, r: f32, from: f32, to: f32, c: Color) -> &mut Picture {
        self.shape(format!(
            "pie {} {} {} {} {} {}",
            num(x),
            num(y),
            num(r.max(0.0)),
            num(from),
            num(to),
            c.word()
        ))
    }

    /// A line of text: `y` is the top of the line, `align` about `x`. Line breaks become spaces.
    pub fn text(
        &mut self,
        x: f32,
        y: f32,
        size: TextSize,
        align: Align,
        c: Color,
        text: &str,
    ) -> &mut Picture {
        let t: String = text
            .chars()
            .map(|ch| if ch == '\n' || ch == '\r' { ' ' } else { ch })
            .collect();
        self.shape(format!(
            "text {} {} {} {} {} {}",
            num(x),
            num(y),
            size.word(),
            align.word(),
            c.word(),
            t
        ))
    }

    /// What follows is drawn only inside this rectangle, until [`Picture::unclip`] or the next clip.
    pub fn clip(&mut self, x: f32, y: f32, w: f32, h: f32) -> &mut Picture {
        self.shape(format!(
            "clip {} {} {} {}",
            num(x),
            num(y),
            num(w.max(0.0)),
            num(h.max(0.0))
        ))
    }

    /// The whole view again.
    pub fn unclip(&mut self) -> &mut Picture {
        self.shape("unclip".into())
    }

    /// Appends another picture's shapes.
    pub fn extend(&mut self, other: &Picture) -> &mut Picture {
        self.body.push_str(&other.body);
        self.shapes += other.shapes;
        self
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn numbers_are_short_and_bounded() {
        assert_eq!(num(10.0), "10");
        assert_eq!(num(12.5), "12.5");
        assert_eq!(num(-0.004), "0");
        assert_eq!(num(7.257), "7.26");
        assert_eq!(num(1e12), "9999999");
        assert_eq!(num(f32::NAN), "0");
    }

    #[test]
    fn shapes_are_lines_rhun_reads() {
        let mut p = Picture::new();
        p.rect(1.0, 2.0, 3.0, 4.0, Color::Rgb(255, 0, 0))
            .poly(2.0, Color::Series(3), &[(0.0, 0.0), (5.0, 5.5)])
            .pie(10.0, 10.0, 5.0, 0.0, 90.0, Color::Accent)
            .text(
                0.0,
                0.0,
                TextSize::Small,
                Align::Right,
                Color::Muted,
                "a\nb",
            );
        assert_eq!(
            p.body(),
            "rect 1 2 3 4 #ff0000\npoly 2 c3 0 0 5 5.5\npie 10 10 5 0 90 accent\ntext 0 0 small right muted a b\n"
        );
        assert_eq!(p.len(), 4);
        assert_eq!(Color::Series(9).word(), "c1");
        assert_eq!(
            Color::hex("#11223344"),
            Some(Color::Rgba(0x11, 0x22, 0x33, 0x44))
        );
    }
}
