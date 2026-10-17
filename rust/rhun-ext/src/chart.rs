//! Charts drawn as pictures: bars, lines and pies with axes, ticks, a legend, and the item under
//! the pointer highlighted with a tooltip. Colors follow the theme.
//!
//! ```
//! use rhun_ext::chart::{Chart, Series};
//! use rhun_ext::Metrics;
//!
//! let chart = Chart::bar("Lines by language", &["Rust", "Go", "C"], vec![Series::new("lines", vec![1200.0, 800.0, 300.0])]);
//! let drawn = chart.draw(600.0, 400.0, &Metrics::default());
//! assert!(drawn.picture.len() > 10);
//! ```

use crate::picture::{Align, Color, Picture};
use crate::{Metrics, TextSize};

/// How the values are drawn.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ChartKind {
    /// A group of bars per label, one bar per series.
    Bar,
    /// A line per series through its values.
    Line,
    /// The first series' values as wedges of a circle, one per label.
    Pie,
}

/// A named list of values, one per label.
#[derive(Clone, Debug, PartialEq)]
pub struct Series {
    pub name: String,
    pub values: Vec<f64>,
    /// `None`: the n-th series color (`c1`…`c8`), which follows the theme.
    pub color: Option<Color>,
}

impl Series {
    pub fn new(name: &str, values: Vec<f64>) -> Series {
        Series { name: name.to_string(), values, color: None }
    }

    pub fn color(mut self, c: Color) -> Series {
        self.color = Some(c);
        self
    }
}

/// The item under a point: which series, which label.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Hit {
    pub series: usize,
    pub index: usize,
}

#[derive(Clone, Debug)]
enum Area {
    Rect(f32, f32, f32, f32),
    Wedge { cx: f32, cy: f32, r: f32, from: f32, to: f32 },
}

impl Area {
    fn contains(&self, x: f32, y: f32) -> bool {
        match *self {
            Area::Rect(ax, ay, w, h) => x >= ax && x < ax + w && y >= ay && y < ay + h,
            Area::Wedge { cx, cy, r, from, to } => {
                let (dx, dy) = (x - cx, y - cy);
                if dx * dx + dy * dy > r * r {
                    return false;
                }
                let a = dx.atan2(-dy).to_degrees().rem_euclid(360.0);
                a >= from && a < to
            }
        }
    }
}

/// A chart drawn: its picture, and where its items are.
#[derive(Clone, Debug, Default)]
pub struct Drawn {
    pub picture: Picture,
    areas: Vec<(Area, Hit)>,
    /// The item under [`Chart::pointer`], if any.
    pub hovered: Option<Hit>,
}

impl Drawn {
    /// The item at a point of the view.
    pub fn hit(&self, x: f32, y: f32) -> Option<Hit> {
        self.areas.iter().rev().find(|(a, _)| a.contains(x, y)).map(|(_, h)| *h)
    }
}

/// A chart: what to draw. Build it, set [`Chart::pointer`] from the view's `hover` events, and
/// [`Chart::draw`] it for the view's size.
#[derive(Clone, Debug)]
pub struct Chart {
    pub kind: ChartKind,
    pub title: String,
    /// One per value: the categories along the bottom, or the wedges of a pie.
    pub labels: Vec<String>,
    pub series: Vec<Series>,
    /// Written after values in the tooltip and a pie's legend: `ms`, `%`, ` lines`.
    pub unit: String,
    /// Written before a label in the tooltip: `Line` makes `Line 12`.
    pub item: String,
    /// The pointer, in the view's pixels: the item under it is highlighted, with a tooltip.
    pub pointer: Option<(f32, f32)>,
}

const PAD: f32 = 12.0;

impl Chart {
    pub fn new(kind: ChartKind, title: &str, labels: &[&str], series: Vec<Series>) -> Chart {
        Chart {
            kind,
            title: title.to_string(),
            labels: labels.iter().map(|s| s.to_string()).collect(),
            series,
            unit: String::new(),
            item: String::new(),
            pointer: None,
        }
    }

    pub fn bar(title: &str, labels: &[&str], series: Vec<Series>) -> Chart {
        Chart::new(ChartKind::Bar, title, labels, series)
    }

    pub fn line(title: &str, labels: &[&str], series: Vec<Series>) -> Chart {
        Chart::new(ChartKind::Line, title, labels, series)
    }

    /// A pie of one series' values, a wedge per label.
    pub fn pie(title: &str, labels: &[&str], values: Vec<f64>) -> Chart {
        Chart::new(ChartKind::Pie, title, labels, vec![Series::new(title, values)])
    }

    pub fn unit(mut self, unit: &str) -> Chart {
        self.unit = unit.to_string();
        self
    }

    pub fn item(mut self, item: &str) -> Chart {
        self.item = item.to_string();
        self
    }

    fn color(&self, series: usize) -> Color {
        self.series.get(series).and_then(|s| s.color).unwrap_or(Color::Series((series % 8) as u8 + 1))
    }

    fn value(&self, series: usize, index: usize) -> f64 {
        // finite and within what a span can hold without overflowing
        let v = self.series.get(series).and_then(|s| s.values.get(index)).copied().filter(|v| v.is_finite());
        v.unwrap_or(0.0).clamp(-1e15, 1e15)
    }

    fn count(&self) -> usize {
        self.series.iter().map(|s| s.values.len()).max().unwrap_or(0).max(self.labels.len())
    }

    fn label(&self, index: usize) -> String {
        self.labels.get(index).cloned().unwrap_or_else(|| (index + 1).to_string())
    }

    /// The picture for a view of `width` × `height` logical pixels.
    pub fn draw(&self, width: f32, height: f32, m: &Metrics) -> Drawn {
        let plain = self.render(width, height, m, None);
        let Some((px, py)) = self.pointer else { return plain };
        let Some(hit) = plain.hit(px, py) else { return plain };
        let mut d = self.render(width, height, m, Some(hit));
        d.hovered = Some(hit);
        self.tooltip(&mut d.picture, hit, px, py, width, height, m);
        d
    }

    fn render(&self, width: f32, height: f32, m: &Metrics, hl: Option<Hit>) -> Drawn {
        let mut d = Drawn::default();
        let mut top = PAD;
        if !self.title.is_empty() {
            let t = fit(m, TextSize::Large, &self.title, width - 2.0 * PAD);
            d.picture.text(PAD, top, TextSize::Large, Align::Left, Color::Fg, &t);
            top += m.large.line_height + 6.0;
        }
        if self.count() == 0 || self.series.is_empty() || width < 4.0 * PAD || height < top + 4.0 * PAD {
            let y = (top + height) / 2.0 - m.normal.line_height / 2.0;
            d.picture.text(width / 2.0, y, TextSize::Normal, Align::Center, Color::Muted, "No data");
            return d;
        }
        match self.kind {
            ChartKind::Pie => self.draw_pie(&mut d, top, width, height, m, hl),
            _ => self.draw_axes(&mut d, top, width, height, m, hl),
        }
        d
    }

    /// The legend's entries on one or more rows from `top`; returns the height used.
    fn legend(&self, p: &mut Picture, x0: f32, top: f32, w: f32, m: &Metrics) -> f32 {
        if self.series.len() < 2 {
            return 0.0;
        }
        let lh = m.small.line_height;
        let (mut x, mut y) = (x0, top);
        for (i, s) in self.series.iter().enumerate() {
            let ew = 14.0 + m.width(TextSize::Small, &s.name) + 16.0;
            if x > x0 && x + ew > x0 + w {
                x = x0;
                y += lh;
            }
            p.rrect(x, y + (lh - 10.0) / 2.0, 10.0, 10.0, 2.0, self.color(i));
            p.text(x + 14.0, y, TextSize::Small, Align::Left, Color::Muted, &s.name);
            x += ew;
        }
        y + lh + 6.0 - top
    }

    fn draw_axes(&self, d: &mut Drawn, top: f32, w: f32, h: f32, m: &Metrics, hl: Option<Hit>) {
        let n = self.count();
        let top = top + self.legend(&mut d.picture, PAD, top, w - 2.0 * PAD, m);
        let (mut lo, mut hi) = (0.0f64, 0.0f64);
        for s in 0..self.series.len() {
            for i in 0..n {
                let v = self.value(s, i);
                lo = lo.min(v);
                hi = hi.max(v);
            }
        }
        if hi == lo {
            hi = lo + 1.0;
        }
        let sl = m.small.line_height;
        let bottom = h - PAD - sl - 4.0;
        let want = ((bottom - top) / (sl * 2.5)).floor().max(2.0) as usize;
        let step = nice_step((hi - lo) / want as f64);
        let lo = (lo / step).floor() * step;
        let hi = (hi / step).ceil() * step;
        let n = ((hi - lo) / step).round().clamp(1.0, 100.0) as usize;
        let ticks: Vec<f64> = (0..=n).map(|k| lo + k as f64 * step).collect();
        let tick_w = ticks.iter().map(|t| m.width(TextSize::Small, &compact(*t))).fold(0.0, f32::max);
        let x0 = PAD + tick_w + 8.0;
        let x1 = w - PAD;
        if x1 - x0 < 8.0 || bottom - top < 8.0 {
            return;
        }
        let y_of = |v: f64| bottom - ((v - lo) / (hi - lo)) as f32 * (bottom - top);
        let p = &mut d.picture;
        for t in &ticks {
            let y = y_of(*t);
            p.line(x0, y, x1, y, 1.0, Color::Border);
            p.text(x0 - 6.0, y - sl / 2.0, TextSize::Small, Align::Right, Color::Muted, &compact(*t));
        }
        let zero = y_of(0.0);
        p.line(x0, zero, x1, zero, 1.0, Color::Muted);
        let slot = (x1 - x0) / n as f32;
        if let Some(hit) = hl {
            p.rect(x0 + slot * hit.index as f32, top, slot, bottom - top, Color::Panel);
            // the grid again over the band
            for t in &ticks {
                let y = y_of(*t);
                p.line(x0 + slot * hit.index as f32, y, x0 + slot * (hit.index + 1) as f32, y, 1.0, Color::Border);
            }
            p.line(x0 + slot * hit.index as f32, zero, x0 + slot * (hit.index + 1) as f32, zero, 1.0, Color::Muted);
        }
        // labels along the bottom, every k-th when they do not fit
        let widest = (0..n).map(|i| m.width(TextSize::Small, &self.label(i))).fold(0.0, f32::max);
        let k = ((widest + 8.0) / slot).ceil().max(1.0) as usize;
        for i in (0..n).step_by(k) {
            let t = fit(m, TextSize::Small, &self.label(i), slot * k as f32 - 4.0);
            p.text(x0 + slot * (i as f32 + 0.5), bottom + 4.0, TextSize::Small, Align::Center, Color::Muted, &t);
        }
        match self.kind {
            ChartKind::Bar => {
                let ns = self.series.len() as f32;
                let group = slot * 0.75;
                let bw = group / ns;
                for i in 0..n {
                    for s in 0..self.series.len() {
                        let v = self.value(s, i);
                        let x = x0 + slot * i as f32 + (slot - group) / 2.0 + bw * s as f32;
                        let (ya, yb) = (y_of(v.max(0.0)), y_of(v.min(0.0)));
                        let gap = if bw > 6.0 { 1.0 } else { 0.0 };
                        p.rect(x + gap, ya, (bw - 2.0 * gap).max(0.5), (yb - ya).max(0.0), self.color(s));
                        d.areas.push((Area::Rect(x, top, bw, bottom - top), Hit { series: s, index: i }));
                    }
                }
            }
            ChartKind::Line => {
                for s in 0..self.series.len() {
                    let pts: Vec<(f32, f32)> =
                        (0..n).map(|i| (x0 + slot * (i as f32 + 0.5), y_of(self.value(s, i)))).collect();
                    p.poly(2.0, self.color(s), &pts);
                    // dots where there is room for them, and on the item under the pointer
                    for (i, (x, y)) in pts.iter().enumerate() {
                        let big = hl == Some(Hit { series: s, index: i });
                        if big {
                            p.circle(*x, *y, 5.0, self.color(s));
                            p.circle(*x, *y, 2.0, Color::Bg);
                        } else if slot >= 10.0 {
                            p.circle(*x, *y, 3.0, self.color(s));
                        }
                    }
                }
                // a point is found by its column, then the nearest series
                for i in 0..n {
                    let x = x0 + slot * i as f32;
                    let mut ys: Vec<(f32, usize)> = (0..self.series.len()).map(|s| (y_of(self.value(s, i)), s)).collect();
                    ys.sort_by(|a, b| a.0.partial_cmp(&b.0).unwrap_or(std::cmp::Ordering::Equal));
                    let mut from = top;
                    for (j, (y, s)) in ys.iter().enumerate() {
                        let to = ys.get(j + 1).map(|(y2, _)| (y + y2) / 2.0).unwrap_or(bottom);
                        d.areas.push((Area::Rect(x, from, slot, to - from), Hit { series: *s, index: i }));
                        from = to;
                    }
                }
            }
            ChartKind::Pie => {}
        }
    }

    fn draw_pie(&self, d: &mut Drawn, top: f32, w: f32, h: f32, m: &Metrics, hl: Option<Hit>) {
        let n = self.count();
        let values: Vec<f64> = (0..n).map(|i| self.value(0, i).max(0.0)).collect();
        let total: f64 = values.iter().sum();
        let p = &mut d.picture;
        if total <= 0.0 {
            p.text(w / 2.0, (top + h) / 2.0, TextSize::Normal, Align::Center, Color::Muted, "No data");
            return;
        }
        // the legend on the right when there is room, the pie in what is left
        let sl = m.small.line_height;
        let entries: Vec<String> = (0..n)
            .map(|i| format!("{}  {}  {:.0}%", self.label(i), self.fmt(values[i]), values[i] / total * 100.0))
            .collect();
        let legend_w = entries.iter().map(|e| m.width(TextSize::Small, e)).fold(0.0, f32::max) + 14.0;
        let show_legend = w - 2.0 * PAD - legend_w > 120.0;
        let area_w = if show_legend { w - 2.0 * PAD - legend_w - PAD } else { w - 2.0 * PAD };
        let r = (area_w.min(h - top - PAD) / 2.0 - 6.0).max(4.0);
        let (cx, cy) = (PAD + area_w / 2.0, top + (h - top - PAD) / 2.0);
        let mut a = 0.0f32;
        for (i, v) in values.iter().enumerate() {
            let sweep = (v / total * 360.0) as f32;
            if sweep <= 0.0 {
                continue;
            }
            let to = if i + 1 == n { 360.0 } else { a + sweep };
            let rr = if hl.map(|h| h.index) == Some(i) { r + 5.0 } else { r };
            p.pie(cx, cy, rr, a, to, Color::Series((i % 8) as u8 + 1));
            d.areas.push((Area::Wedge { cx, cy, r: r + 5.0, from: a, to }, Hit { series: 0, index: i }));
            a = to;
        }
        if n > 1 {
            // thin separators in the background color
            let mut a = 0.0f32;
            for v in &values {
                let (s, c) = a.to_radians().sin_cos();
                p.line(cx, cy, cx + s * (r + 6.0), cy - c * (r + 6.0), 1.5, Color::Bg);
                a += (v / total * 360.0) as f32;
            }
        }
        if show_legend {
            let lx = w - PAD - legend_w;
            let mut y = cy - (n as f32 * sl) / 2.0;
            for (i, e) in entries.iter().enumerate() {
                p.rrect(lx, y + (sl - 10.0) / 2.0, 10.0, 10.0, 2.0, Color::Series((i % 8) as u8 + 1));
                let c = if hl.map(|h| h.index) == Some(i) { Color::Fg } else { Color::Muted };
                p.text(lx + 14.0, y, TextSize::Small, Align::Left, c, e);
                d.areas.push((Area::Rect(lx, y, legend_w, sl), Hit { series: 0, index: i }));
                y += sl;
            }
        }
    }

    #[allow(clippy::too_many_arguments)]
    fn tooltip(&self, p: &mut Picture, h: Hit, px: f32, py: f32, w: f32, ht: f32, m: &Metrics) {
        let label = if self.item.is_empty() { self.label(h.index) } else { format!("{} {}", self.item, self.label(h.index)) };
        let name = if self.kind == ChartKind::Pie || self.series.len() < 2 {
            label
        } else {
            format!("{label} · {}", self.series[h.series].name)
        };
        let value = self.fmt(self.value(h.series, h.index));
        let nl = m.normal.line_height;
        let tw = m.width(TextSize::Normal, &name).max(m.width(TextSize::Normal, &value)) + 16.0;
        let th = nl * 2.0 + 10.0;
        let mut x = px + 14.0;
        let mut y = py + 14.0;
        if x + tw > w - 4.0 {
            x = (px - 14.0 - tw).max(4.0);
        }
        if y + th > ht - 4.0 {
            y = (py - 14.0 - th).max(4.0);
        }
        p.rrect(x - 1.0, y - 1.0, tw + 2.0, th + 2.0, 6.0, Color::Border);
        p.rrect(x, y, tw, th, 5.0, Color::Panel);
        p.text(x + 8.0, y + 5.0, TextSize::Normal, Align::Left, Color::Muted, &name);
        p.text(x + 8.0, y + 5.0 + nl, TextSize::Normal, Align::Left, Color::Fg, &value);
    }

    /// A value as the chart writes it: `1.2k`, `3.5M`, `0.25`, with the unit.
    pub fn fmt(&self, v: f64) -> String {
        format!("{}{}", compact(v), self.unit)
    }
}

/// A short form of a number: up to two decimals, `k`, `M`, `G` past a thousand.
pub fn compact(v: f64) -> String {
    let a = v.abs();
    let (x, suffix) = if a >= 1e9 {
        (v / 1e9, "G")
    } else if a >= 1e6 {
        (v / 1e6, "M")
    } else if a >= 1e4 {
        (v / 1e3, "k")
    } else {
        (v, "")
    };
    let s = if suffix.is_empty() && a >= 100.0 { format!("{x:.0}") } else { format!("{x:.2}") };
    let s = if s.contains('.') { s.trim_end_matches('0').trim_end_matches('.').to_string() } else { s };
    let s = if s == "-0" { "0".to_string() } else { s };
    format!("{s}{suffix}")
}

/// 1, 2 or 5 times a power of ten, at least `raw`.
fn nice_step(raw: f64) -> f64 {
    if raw.is_nan() || raw <= 0.0 || raw.is_infinite() {
        return 1.0;
    }
    let e = raw.log10().floor();
    let base = 10f64.powf(e);
    let f = raw / base;
    let nf = if f <= 1.0 {
        1.0
    } else if f <= 2.0 {
        2.0
    } else if f <= 5.0 {
        5.0
    } else {
        10.0
    };
    nf * base
}

/// `text` cut to fit `width` at `size`, with `…` when cut.
pub fn fit(m: &Metrics, size: TextSize, text: &str, width: f32) -> String {
    if m.width(size, text) <= width {
        return text.to_string();
    }
    let mut out = String::new();
    for c in text.chars() {
        out.push(c);
        if m.width(size, &out) + m.of(size).advance > width {
            out.pop();
            break;
        }
    }
    out.push('…');
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn steps_and_numbers() {
        assert_eq!(nice_step(0.7), 1.0);
        assert_eq!(nice_step(1.3), 2.0);
        assert_eq!(nice_step(37.0), 50.0);
        assert_eq!(nice_step(0.0), 1.0);
        assert_eq!(compact(12500.0), "12.5k");
        assert_eq!(compact(2_000_000.0), "2M");
        assert_eq!(compact(0.25), "0.25");
        assert_eq!(compact(150.0), "150");
        assert_eq!(compact(-3.0), "-3");
    }

    #[test]
    fn bars_are_hit_and_highlighted() {
        let m = Metrics::default();
        let mut c = Chart::bar("t", &["a", "b", "c"], vec![Series::new("s", vec![1.0, 5.0, 3.0])]);
        let d = c.draw(400.0, 300.0, &m);
        assert!(d.hovered.is_none());
        // the second bar (the second rect in the series color) is item 1
        let bar: Vec<f32> = d.picture.body().lines().filter(|l| l.starts_with("rect ") && l.ends_with(" c1")).nth(1).unwrap()
            .split(' ').skip(1).take(4).map(|v| v.parse().unwrap()).collect();
        let (x, y) = (bar[0] + bar[2] / 2.0, bar[1] + bar[3] / 2.0);
        assert_eq!(d.hit(x, y), Some(Hit { series: 0, index: 1 }));
        c.pointer = Some((x, y));
        let d2 = c.draw(400.0, 300.0, &m);
        assert_eq!(d2.hovered, Some(Hit { series: 0, index: 1 }));
        assert!(d2.picture.body().contains(" panel\n"));
        assert!(d2.picture.body().contains("text") && d2.picture.body().contains(" 5\n"));
    }

    #[test]
    fn a_pie_is_hit_by_angle() {
        let m = Metrics::default();
        let c = Chart::pie("p", &["x", "y"], vec![1.0, 3.0]);
        let d = c.draw(600.0, 400.0, &m);
        let body = d.picture.body();
        assert!(body.contains("pie ") && body.contains(" 0 90 c1"));
        // up from the center is the first wedge, left of it the second
        let line = body.lines().find(|l| l.starts_with("pie ")).unwrap();
        let f: Vec<f32> = line.split(' ').skip(1).take(3).map(|v| v.parse().unwrap()).collect();
        assert_eq!(d.hit(f[0] + 5.0, f[1] - 20.0), Some(Hit { series: 0, index: 0 }));
        assert_eq!(d.hit(f[0] - 20.0, f[1]), Some(Hit { series: 0, index: 1 }));
    }

    #[test]
    fn nothing_to_draw_says_so() {
        let c = Chart::line("", &[], vec![]);
        assert!(c.draw(300.0, 200.0, &Metrics::default()).picture.body().contains("No data"));
    }

    #[test]
    fn negative_values_hang_below_zero() {
        let m = Metrics::default();
        let c = Chart::bar("", &["a", "b"], vec![Series::new("s", vec![-2.0, 4.0])]);
        let body = c.draw(300.0, 200.0, &m).picture.body().to_string();
        assert!(body.contains("-2"));
    }

    #[test]
    fn huge_values_still_draw() {
        let c = Chart::bar("", &["a", "b"], vec![Series::new("s", vec![-1e308, 1e308])]);
        assert!(c.draw(600.0, 400.0, &Metrics::default()).picture.len() > 2);
    }

    #[test]
    fn labels_are_cut_to_fit() {
        let m = Metrics::default();
        assert_eq!(fit(&m, TextSize::Normal, "abcdefgh", 35.0), "abcd…");
        assert_eq!(fit(&m, TextSize::Normal, "ab", 35.0), "ab");
    }
}
