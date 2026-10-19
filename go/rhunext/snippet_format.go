package rhunext

import "strings"

// MaxSnippetFormatParts bounds expanded snapshot layout markers independently of text bytes.
const MaxSnippetFormatParts = 32768

type snippetPart struct {
	text string
	at   int
}
type snippetBuffer struct{ strings.Builder }

type snippetFormatter struct {
	base                   string
	width                  int
	spaces, keepWhitespace bool
	last                   byte
}

func newSnippetFormatter(s SnippetSnapshot, keep bool) *snippetFormatter {
	return &snippetFormatter{base: s.Indent, width: s.TabWidth, spaces: s.InsertSpaces, keepWhitespace: keep}
}
func (f *snippetFormatter) observe(text string, _ int) {
	if len(text) > 0 {
		f.last = text[len(text)-1]
	}
}

// Write indentation without constructing a combined base+literal string.
func (f *snippetFormatter) indentation(e *snippetExpander, base, leading string, at int) error {
	columns := 0
	maxColumns := MaxSnippetBody
	if !f.spaces {
		maxColumns *= f.width
	}
	for _, piece := range []string{base, leading} {
		for i := 0; i < len(piece); i++ {
			step := 1
			if piece[i] == '\t' {
				step = f.width - columns%f.width
			}
			if step > maxColumns-columns {
				return expansionError(at, "formatted indentation exceeds byte limit")
			}
			columns += step
		}
	}
	tabs, spaces := 0, columns
	if !f.spaces {
		tabs, spaces = columns/f.width, columns%f.width
	}
	if tabs+spaces > MaxSnippetBody-e.out.Len() {
		return expansionError(at, "formatted indentation exceeds byte limit")
	}
	if tabs != 0 {
		if err := e.appendRaw(strings.Repeat("\t", tabs), at); err != nil {
			return err
		}
	}
	if spaces != 0 {
		return e.appendRaw(strings.Repeat(" ", spaces), at)
	}
	return nil
}
func (f *snippetFormatter) literal(e *snippetExpander, part snippetPart) error {
	text := part.text
	subsequent := false
	for {
		end := strings.IndexAny(text, "\r\n")
		line := text
		if end >= 0 {
			line = text[:end]
		}
		normalize := !f.keepWhitespace && (subsequent || e.out.Len() == 0 || f.last == '\r' || f.last == '\n')
		if normalize {
			leading := 0
			for leading < len(line) && (line[leading] == ' ' || line[leading] == '\t') {
				leading++
			}
			base := ""
			if subsequent || e.out.Len() != 0 {
				base = f.base
			}
			if err := f.indentation(e, base, line[:leading], part.at); err != nil {
				return err
			}
			line = line[leading:]
		}
		if err := e.appendRaw(line, part.at); err != nil {
			return err
		}
		if end < 0 {
			return nil
		}
		skip := 1
		if text[end] == '\r' && end+1 < len(text) && text[end+1] == '\n' {
			skip = 2
		}
		text = text[end+skip:]
		if err := e.appendRaw("\n", part.at); err != nil {
			return err
		}
		subsequent = true
	}
}
