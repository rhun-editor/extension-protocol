package rhunext

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// SnippetTime is an explicit instant and zone. Preparation never resolves Local.
type SnippetTime struct {
	UnixSeconds               int64
	Nanosecond, OffsetSeconds int
	ZoneName                  string
}

// SnippetTimeFromTime captures an already chosen time. It does not read a clock,
// but Go may resolve local timezone settings when the supplied location is Local.
// Call this explicitly before preparation when that collection is desired.
func SnippetTimeFromTime(t time.Time) SnippetTime {
	_, offset := t.Zone()
	return SnippetTime{UnixSeconds: t.Unix(), Nanosecond: t.Nanosecond(), OffsetSeconds: offset, ZoneName: t.Location().String()}
}

// SnippetVariableOptions supplies optional values explicitly. Nil Time, Random or
// Clipboard leaves those variables unavailable. Random must contain 16 bytes.
// Variables overrides standard values, including with an explicit empty string.
// Do not mutate maps, slices or pointed-to inputs during preparation.
type SnippetVariableOptions struct {
	Variables                          map[string]string
	Time                               *SnippetTime
	Random                             []byte
	Clipboard                          *string
	BlockCommentStart, BlockCommentEnd string
	CursorIndex                        int
	PathStyle                          SnippetPathStyle
	// KeepWhitespace disables template indentation changes; literal line endings become LF.
	// Selected-text wrapper indentation still follows the literal Text marker.
	KeepWhitespace bool
}

// PrepareSnippetTemplateWithSnapshot parses and prepares using captured context.
func PrepareSnippetTemplateWithSnapshot(source string, snapshot SnippetSnapshot, options SnippetVariableOptions) (SnippetProgram, error) {
	t, err := ParseSnippetTemplate(source)
	if err != nil {
		return SnippetProgram{}, err
	}
	return t.PrepareWithSnapshot(snapshot, options)
}

// PrepareWithSnapshot resolves standard variables without reading clocks, files,
// the environment or clipboard. Literal template text follows captured indentation
// and internal LF line endings before variables resolve. KeepWhitespace preserves
// template indentation. Captured multiline selections gain their Text marker wrapper
// indentation before an optional transform. Explicit overrides stay literal.
func (t *SnippetTemplate) PrepareWithSnapshot(snapshot SnippetSnapshot, options SnippetVariableOptions) (SnippetProgram, error) {
	if t == nil {
		return SnippetProgram{}, expansionError(-1, "nil template")
	}
	variables, err := newSnippetVariables(snapshot, options)
	if err != nil {
		return SnippetProgram{}, err
	}
	return t.prepare(SnippetContext{Variables: options.Variables}, variables)
}

func knownSnippetVariable(name string) bool {
	switch name {
	case "SELECTION", "TM_SELECTED_TEXT", "TM_CURRENT_LINE", "TM_CURRENT_WORD", "TM_LINE_INDEX", "TM_LINE_NUMBER",
		"TM_FILENAME", "TM_FILENAME_BASE", "TM_DIRECTORY", "TM_DIRECTORY_BASE", "TM_FILEPATH", "RELATIVE_FILEPATH",
		"CURSOR_INDEX", "CURSOR_NUMBER", "WORKSPACE_NAME", "WORKSPACE_FOLDER", "LINE_COMMENT", "BLOCK_COMMENT_START", "BLOCK_COMMENT_END",
		"CLIPBOARD", "RANDOM", "RANDOM_HEX", "UUID", "CURRENT_YEAR", "CURRENT_YEAR_SHORT", "CURRENT_MONTH", "CURRENT_DATE",
		"CURRENT_HOUR", "CURRENT_MINUTE", "CURRENT_SECOND", "CURRENT_MILLISECOND", "CURRENT_DAY_NAME", "CURRENT_DAY_NAME_SHORT",
		"CURRENT_MONTH_NAME", "CURRENT_MONTH_NAME_SHORT", "CURRENT_SECONDS_UNIX", "CURRENT_MILLISECONDS_UNIX", "CURRENT_TIMEZONE_OFFSET", "CURRENT_TIMEZONE_NAME":
		return true
	}
	return false
}

func validateSnippetSnapshot(s SnippetSnapshot) error {
	if s.Version < 0 || s.Position.Line <= 0 || s.Position.Col <= 0 || s.TabWidth < 1 || s.TabWidth > 16 {
		return expansionError(-1, "invalid snippet snapshot metadata")
	}
	parts := [6]string{s.SelectedText, s.CurrentLine, s.Path, s.Workspace, s.Indent, s.LineComment}
	total := 0
	for _, p := range parts {
		if len(p) > MaxSnippetBody-total {
			return expansionError(-1, "snippet snapshot exceeds its byte limit")
		}
		total += len(p)
	}
	spaces, crlf := 0, 0
	if s.InsertSpaces {
		spaces = 1
	}
	if s.CRLF {
		crlf = 1
	}
	head := fmt.Sprintf("context 1 %d %d %d %d %d", s.Position.Line, s.Position.Col, s.TabWidth, spaces, crlf)
	for _, p := range parts {
		head += fmt.Sprintf(" %d", len(p))
	}
	head += "\n"
	if len(head) > MaxSnippetBody-total {
		return expansionError(-1, "snippet snapshot exceeds its byte limit")
	}
	for _, p := range parts {
		if !utf8.ValidString(p) {
			return expansionError(-1, "snippet snapshot is not UTF-8")
		}
	}
	col := s.Position.Col - 1
	if strings.ContainsRune(s.CurrentLine, '\n') || col > len(s.CurrentLine) || col < len(s.CurrentLine) && !utf8.RuneStart(s.CurrentLine[col]) {
		return expansionError(-1, "invalid snippet snapshot position")
	}
	indent := 0
	for indent < col && (s.CurrentLine[indent] == ' ' || s.CurrentLine[indent] == '\t') {
		indent++
	}
	if s.Indent != s.CurrentLine[:indent] {
		return expansionError(-1, "invalid snippet snapshot indentation")
	}
	return nil
}

type snippetVariableResult struct {
	text      string
	available bool
}
type snippetVariables struct {
	snapshot                                  SnippetSnapshot
	keepWhitespace                            bool
	style                                     SnippetPathStyle
	file, workspace                           *snippetPath
	instant                                   *time.Time
	zoneName, clipboard, blockStart, blockEnd string
	entropy                                   [16]byte
	hasRandom                                 bool
	cursor                                    int
	cache                                     map[string]snippetVariableResult
}

func newSnippetVariables(s SnippetSnapshot, o SnippetVariableOptions) (*snippetVariables, error) {
	if err := validateSnippetSnapshot(s); err != nil {
		return nil, err
	}
	if o.CursorIndex < 0 || o.CursorIndex > 2147483646 || (o.Random != nil && len(o.Random) != 16) || (o.BlockCommentStart == "") != (o.BlockCommentEnd == "") {
		return nil, expansionError(-1, "invalid snippet variable options")
	}
	v := &snippetVariables{snapshot: s, keepWhitespace: o.KeepWhitespace, cursor: o.CursorIndex, blockStart: o.BlockCommentStart, blockEnd: o.BlockCommentEnd, cache: map[string]snippetVariableResult{}}
	if o.Clipboard != nil {
		v.clipboard = *o.Clipboard
	}
	var clock SnippetTime
	if o.Time != nil {
		clock = *o.Time
		v.zoneName = clock.ZoneName
	}
	optional := []string{v.clipboard, v.blockStart, v.blockEnd, v.zoneName}
	total := len(o.Random)
	for _, text := range optional {
		if len(text) > MaxSnippetBody-total {
			return nil, expansionError(-1, "snippet variable options exceed byte limit")
		}
		total += len(text)
	}
	for _, text := range optional {
		if !utf8.ValidString(text) {
			return nil, expansionError(-1, "snippet variable option is not UTF-8")
		}
	}
	style, err := snippetPathStyle(o.PathStyle)
	if err != nil {
		return nil, err
	}
	v.style = style
	v.file, err = parseSnippetPath(s.Path, style)
	if err != nil {
		return nil, err
	}
	v.workspace, err = parseSnippetPath(s.Workspace, style)
	if err != nil {
		return nil, err
	}
	if o.Time != nil {
		// Bound seconds before time.Time's internal epoch addition, then check the
		// supplied zone's local year. No operation here observes time.Local.
		if clock.Nanosecond < 0 || clock.Nanosecond >= 1e9 || clock.OffsetSeconds <= -86400 || clock.OffsetSeconds >= 86400 ||
			clock.UnixSeconds < -62135596800-86400 || clock.UnixSeconds > 253402300799+86400 {
			return nil, expansionError(-1, "invalid snippet time")
		}
		t := time.Unix(clock.UnixSeconds, int64(clock.Nanosecond)).In(time.FixedZone(clock.ZoneName, clock.OffsetSeconds))
		if t.Year() < 1 || t.Year() > 9999 {
			return nil, expansionError(-1, "snippet time year is outside its limit")
		}
		v.instant = &t
	}
	if o.Random != nil {
		copy(v.entropy[:], o.Random)
		v.hasRandom = true
	}
	return v, nil
}

func (v *snippetVariables) lookup(name string) (string, bool) {
	if !knownSnippetVariable(name) {
		return "", false
	}
	if result, ok := v.cache[name]; ok {
		return result.text, result.available
	}
	text, available := v.resolve(name)
	v.cache[name] = snippetVariableResult{text, available}
	return text, available
}

func (v *snippetVariables) resolve(name string) (string, bool) {
	s := v.snapshot
	switch name {
	case "SELECTION", "TM_SELECTED_TEXT":
		return s.SelectedText, s.SelectedText != ""
	case "TM_CURRENT_LINE":
		return s.CurrentLine, true
	case "TM_CURRENT_WORD":
		word := snippetCurrentWord(s.CurrentLine, s.Position.Col-1)
		return word, word != ""
	case "TM_LINE_INDEX":
		return strconv.Itoa(s.Position.Line - 1), true
	case "TM_LINE_NUMBER":
		return strconv.Itoa(s.Position.Line), true
	case "CURSOR_INDEX":
		return strconv.Itoa(v.cursor), true
	case "CURSOR_NUMBER":
		return strconv.Itoa(v.cursor + 1), true
	case "TM_FILENAME", "TM_FILENAME_BASE", "TM_DIRECTORY", "TM_DIRECTORY_BASE", "TM_FILEPATH", "RELATIVE_FILEPATH":
		if v.file == nil {
			return "", false
		}
		switch name {
		case "TM_FILENAME":
			return v.file.name(), true
		case "TM_FILENAME_BASE":
			base := v.file.name()
			if dot := strings.LastIndexByte(base, '.'); dot > 0 {
				base = base[:dot]
			}
			return base, true
		case "TM_DIRECTORY":
			parent := v.file.directory()
			return parent.String(), true
		case "TM_DIRECTORY_BASE":
			parent := v.file.directory()
			return parent.name(), true
		case "TM_FILEPATH":
			return s.Path, true
		case "RELATIVE_FILEPATH":
			return snippetRelativePath(v.file, v.workspace, v.style, s.Path), true
		}
	case "WORKSPACE_NAME":
		if v.workspace != nil {
			return v.workspace.name(), true
		}
	case "WORKSPACE_FOLDER":
		return s.Workspace, s.Workspace != ""
	case "LINE_COMMENT":
		return s.LineComment, s.LineComment != ""
	case "BLOCK_COMMENT_START":
		return v.blockStart, v.blockStart != ""
	case "BLOCK_COMMENT_END":
		return v.blockEnd, v.blockEnd != ""
	case "CLIPBOARD":
		return v.clipboard, v.clipboard != ""
	case "RANDOM", "RANDOM_HEX", "UUID":
		if !v.hasRandom {
			return "", false
		}
		switch name {
		case "RANDOM":
			return fmt.Sprintf("%06d", binary.BigEndian.Uint32(v.entropy[:4])%1000000), true
		case "RANDOM_HEX":
			return hex.EncodeToString(v.entropy[:3]), true
		case "UUID":
			uuid := v.entropy
			uuid[6] = uuid[6]&15 | 64
			uuid[8] = uuid[8]&63 | 128
			return fmt.Sprintf("%x-%x-%x-%x-%x", uuid[:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:]), true
		}
	}
	if v.instant != nil && strings.HasPrefix(name, "CURRENT_") {
		return v.timeValue(name)
	}
	return "", false
}

func (v *snippetVariables) timeValue(name string) (string, bool) {
	t := v.instant
	switch name {
	case "CURRENT_YEAR":
		return fmt.Sprintf("%04d", t.Year()), true
	case "CURRENT_YEAR_SHORT":
		return fmt.Sprintf("%02d", t.Year()%100), true
	case "CURRENT_MONTH":
		return fmt.Sprintf("%02d", t.Month()), true
	case "CURRENT_DATE":
		return fmt.Sprintf("%02d", t.Day()), true
	case "CURRENT_HOUR":
		return fmt.Sprintf("%02d", t.Hour()), true
	case "CURRENT_MINUTE":
		return fmt.Sprintf("%02d", t.Minute()), true
	case "CURRENT_SECOND":
		return fmt.Sprintf("%02d", t.Second()), true
	case "CURRENT_MILLISECOND":
		return fmt.Sprintf("%03d", t.Nanosecond()/1e6), true
	case "CURRENT_DAY_NAME":
		return t.Weekday().String(), true
	case "CURRENT_DAY_NAME_SHORT":
		return t.Weekday().String()[:3], true
	case "CURRENT_MONTH_NAME":
		return t.Month().String(), true
	case "CURRENT_MONTH_NAME_SHORT":
		return t.Month().String()[:3], true
	case "CURRENT_SECONDS_UNIX":
		return strconv.FormatInt(t.Unix(), 10), true
	case "CURRENT_MILLISECONDS_UNIX":
		return strconv.FormatInt(t.UnixMilli(), 10), true
	case "CURRENT_TIMEZONE_NAME":
		return v.zoneName, v.zoneName != ""
	case "CURRENT_TIMEZONE_OFFSET":
		_, offset := t.Zone()
		sign := "+"
		if offset < 0 {
			sign = "-"
			offset = -offset
		}
		minutes := offset / 60
		return fmt.Sprintf("%s%02d:%02d", sign, minutes/60, minutes%60), true
	}
	return "", false
}

func snippetWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r)
}
func snippetCurrentWord(line string, col int) string {
	start, end := col, col
	for start > 0 {
		r, n := utf8.DecodeLastRuneInString(line[:start])
		if !snippetWordRune(r) {
			break
		}
		start -= n
	}
	for end < len(line) {
		r, n := utf8.DecodeRuneInString(line[end:])
		if !snippetWordRune(r) {
			break
		}
		end += n
	}
	return line[start:end]
}
