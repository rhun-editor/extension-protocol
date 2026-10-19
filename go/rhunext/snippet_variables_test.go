package rhunext

import (
	"errors"
	"math"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func variableSnapshot() SnippetSnapshot {
	return SnippetSnapshot{Version: 9, Position: Pos{2, 4}, TabWidth: 4, InsertSpaces: true, SelectedText: "chosen\ntext\x00",
		CurrentLine: "  café_word next", Path: "/p/src/file.tar.go", Workspace: "/p", Indent: "  ", LineComment: "//"}
}

func variableProgram(t *testing.T, source string, s SnippetSnapshot, o SnippetVariableOptions) SnippetProgram {
	t.Helper()
	p, err := PrepareSnippetTemplateWithSnapshot(source, s, o)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Validate(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSnippetVariablesStandardGolden(t *testing.T) {
	s := variableSnapshot()
	clock := SnippetTime{UnixSeconds: -1, Nanosecond: 123456789, OffsetSeconds: 20700, ZoneName: "Explicit/Zone"}
	clip := "literal $1\n\x00"
	entropy := make([]byte, 16)
	for i := range entropy {
		entropy[i] = byte(i)
	}
	o := SnippetVariableOptions{Time: &clock, Random: entropy, Clipboard: &clip, BlockCommentStart: "/*", BlockCommentEnd: "*/", CursorIndex: 2, PathStyle: SnippetPathPOSIX}
	cases := [][2]string{
		{"SELECTION", s.SelectedText}, {"TM_SELECTED_TEXT", s.SelectedText}, {"TM_CURRENT_LINE", s.CurrentLine}, {"TM_CURRENT_WORD", "café_word"},
		{"TM_LINE_INDEX", "1"}, {"TM_LINE_NUMBER", "2"}, {"CURSOR_INDEX", "2"}, {"CURSOR_NUMBER", "3"},
		{"TM_FILENAME", "file.tar.go"}, {"TM_FILENAME_BASE", "file.tar"}, {"TM_DIRECTORY", "/p/src"}, {"TM_DIRECTORY_BASE", "src"},
		{"TM_FILEPATH", s.Path}, {"RELATIVE_FILEPATH", "src/file.tar.go"}, {"WORKSPACE_NAME", "p"}, {"WORKSPACE_FOLDER", "/p"},
		{"LINE_COMMENT", "//"}, {"BLOCK_COMMENT_START", "/*"}, {"BLOCK_COMMENT_END", "*/"}, {"CLIPBOARD", clip},
		{"CURRENT_YEAR", "1970"}, {"CURRENT_YEAR_SHORT", "70"}, {"CURRENT_MONTH", "01"}, {"CURRENT_DATE", "01"},
		{"CURRENT_HOUR", "05"}, {"CURRENT_MINUTE", "44"}, {"CURRENT_SECOND", "59"}, {"CURRENT_MILLISECOND", "123"},
		{"CURRENT_DAY_NAME", "Thursday"}, {"CURRENT_DAY_NAME_SHORT", "Thu"}, {"CURRENT_MONTH_NAME", "January"}, {"CURRENT_MONTH_NAME_SHORT", "Jan"},
		{"CURRENT_SECONDS_UNIX", "-1"}, {"CURRENT_MILLISECONDS_UNIX", "-877"}, {"CURRENT_TIMEZONE_OFFSET", "+05:45"}, {"CURRENT_TIMEZONE_NAME", "Explicit/Zone"},
		{"RANDOM", "066051"}, {"RANDOM_HEX", "000102"}, {"UUID", "00010203-0405-4607-8809-0a0b0c0d0e0f"},
	}
	for _, tc := range cases {
		t.Run(tc[0], func(t *testing.T) {
			p := variableProgram(t, "$"+tc[0], s, o)
			if p.Snippet.Text != tc[1] || len(p.Snippet.Fields) != 0 {
				t.Fatalf("%+v want %q", p, tc[1])
			}
		})
	}
	p := variableProgram(t, "$UUID $UUID", s, o)
	if p.Snippet.Text != "00010203-0405-4607-8809-0a0b0c0d0e0f 00010203-0405-4607-8809-0a0b0c0d0e0f" {
		t.Fatal(p)
	}
	if entropy[6] != 6 || entropy[8] != 8 {
		t.Fatal("UUID mutated caller entropy")
	}
}

func TestSnippetVariablesKnownUnavailableDefaultsAndUnknownFields(t *testing.T) {
	for _, source := range []string{"$TM_SELECTED_TEXT|${TM_SELECTED_TEXT:fallback}|$CLIPBOARD|${CLIPBOARD:clip}|$UUID|${UUID:id}",
		"${1:$TM_SELECTED_TEXT}|$1|${CURRENT_YEAR:date}|${TM_FILENAME:file}"} {
		p, err := PrepareSnippetTemplate(source, SnippetContext{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(source, "$TM_SELECTED_TEXT") && p.Snippet.Text != "|fallback||clip||id" {
			t.Fatal(p)
		}
		if strings.HasPrefix(source, "${1:") && p.Snippet.Text != "||date|file" {
			t.Fatal(p)
		}
	}
	p, err := PrepareSnippetTemplate("$TM_FILENAME|$CUSTOM|$CUSTOM", SnippetContext{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Snippet.Text != "|CUSTOM|CUSTOM" || len(p.Snippet.Fields) != 2 || p.Snippet.Fields[0].Group != p.Snippet.Fields[1].Group {
		t.Fatal(p)
	}
	p, err = PrepareSnippetTemplate("${TM_FILENAME:default}", SnippetContext{Variables: map[string]string{"TM_FILENAME": ""}})
	if err != nil || p.Snippet.Text != "" || len(p.Snippet.Fields) != 0 {
		t.Fatal(p, err)
	}
	s := variableSnapshot()
	s.SelectedText = ""
	s.Path = ""
	s.Workspace = ""
	s.LineComment = ""
	for _, clip := range []*string{nil, new(string)} {
		p = variableProgram(t, "${SELECTION:sel}|${TM_FILENAME:file}|${WORKSPACE_NAME:workspace}|${LINE_COMMENT:comment}|${CLIPBOARD:clip}|${BLOCK_COMMENT_START:block}", s, SnippetVariableOptions{Clipboard: clip, PathStyle: SnippetPathPOSIX})
		if p.Snippet.Text != "sel|file|workspace|comment|clip|block" || len(p.Snippet.Fields) != 0 {
			t.Fatal(p)
		}
	}
	p = variableProgram(t, "${TM_FILENAME:default}|$CUSTOM|$CUSTOM", variableSnapshot(), SnippetVariableOptions{Variables: map[string]string{"TM_FILENAME": "", "CUSTOM": "$1"}, PathStyle: SnippetPathPOSIX})
	if p.Snippet.Text != "|$1|$1" || len(p.Snippet.Fields) != 0 {
		t.Fatal(p)
	}
	s = SnippetSnapshot{Position: Pos{1, 1}, TabWidth: 4}
	p = variableProgram(t, "${TM_CURRENT_LINE:default}|${TM_CURRENT_WORD:word}|$TM_LINE_INDEX|$CURSOR_NUMBER", s, SnippetVariableOptions{})
	if p.Snippet.Text != "|word|0|1" {
		t.Fatal(p)
	}
}

func TestSnippetVariablesWords(t *testing.T) {
	for _, tc := range []struct {
		line string
		col  int
		want string
	}{
		{"one.two", 0, "one"}, {"one.two", 3, "one"}, {"one.two", 4, "two"}, {"one.two", 7, "two"},
		{"one  two", 4, ""}, {"one  two", 5, "two"}, {"😀 café_9́。word", 0, ""},
		{"😀 café_9́。word", 5, "café_9́"}, {"😀 café_9́。word", 14, "café_9́"},
		{"a\x00b", 1, "a"}, {"a\x00b", 2, "b"}, {"", 0, ""},
	} {
		t.Run(tc.line+string(rune(tc.col)), func(t *testing.T) {
			if got := snippetCurrentWord(tc.line, tc.col); got != tc.want {
				t.Fatalf("col%d %q want%q", tc.col, got, tc.want)
			}
		})
	}
}

func TestSnippetVariablesTimeAndRandomBoundaries(t *testing.T) {
	s := variableSnapshot()
	for _, tc := range []struct {
		clock SnippetTime
		want  string
	}{
		{SnippetTime{UnixSeconds: -62135596800}, "0001-01-01 00:00:00 000 +00:00 -62135596800 -62135596800000"},
		{SnippetTime{UnixSeconds: 253402300799, Nanosecond: 999999999}, "9999-12-31 23:59:59 999 +00:00 253402300799 253402300799999"},
		{SnippetTime{UnixSeconds: 1709164800, OffsetSeconds: -1}, "2024-02-28 23:59:59 000 -00:00 1709164800 1709164800000"},
		{SnippetTime{UnixSeconds: 1709164800, OffsetSeconds: 86399}, "2024-02-29 23:59:59 000 +23:59 1709164800 1709164800000"},
	} {
		p := variableProgram(t, "$CURRENT_YEAR-$CURRENT_MONTH-$CURRENT_DATE $CURRENT_HOUR:$CURRENT_MINUTE:$CURRENT_SECOND $CURRENT_MILLISECOND $CURRENT_TIMEZONE_OFFSET $CURRENT_SECONDS_UNIX $CURRENT_MILLISECONDS_UNIX", s, SnippetVariableOptions{Time: &tc.clock, PathStyle: SnippetPathPOSIX})
		if p.Snippet.Text != tc.want {
			t.Fatal(p.Snippet.Text, tc.want)
		}
	}
	for _, tc := range []struct {
		b    byte
		want string
	}{{0, "000000 000000 00000000-0000-4000-8000-000000000000"}, {255, "967295 ffffff ffffffff-ffff-4fff-bfff-ffffffffffff"}} {
		p := variableProgram(t, "$RANDOM $RANDOM_HEX $UUID", s, SnippetVariableOptions{Random: []byte(strings.Repeat(string([]byte{tc.b}), 16)), PathStyle: SnippetPathPOSIX})
		if p.Snippet.Text != tc.want {
			t.Fatal(p.Snippet.Text, tc.want)
		}
	}
	instant := time.Unix(-1, 999999999).In(time.FixedZone("Chosen", -12600))
	clock := SnippetTimeFromTime(instant)
	if clock != (SnippetTime{UnixSeconds: -1, Nanosecond: 999999999, OffsetSeconds: -12600, ZoneName: "Chosen"}) {
		t.Fatal(clock)
	}
}

func TestSnippetVariablesInvalidInputsHaveNoProgram(t *testing.T) {
	for name, change := range map[string]func(*SnippetSnapshot, *SnippetVariableOptions){
		"version":     func(s *SnippetSnapshot, o *SnippetVariableOptions) { s.Version = -1 },
		"position":    func(s *SnippetSnapshot, o *SnippetVariableOptions) { s.Position = Pos{} },
		"tab":         func(s *SnippetSnapshot, o *SnippetVariableOptions) { s.TabWidth = 17 },
		"utf8":        func(s *SnippetSnapshot, o *SnippetVariableOptions) { s.SelectedText = "\xff" },
		"line lf":     func(s *SnippetSnapshot, o *SnippetVariableOptions) { s.CurrentLine += "\n" },
		"col outside": func(s *SnippetSnapshot, o *SnippetVariableOptions) { s.Position.Col = 100 },
		"split utf8":  func(s *SnippetSnapshot, o *SnippetVariableOptions) { s.Position.Col = 7 },
		"indent":      func(s *SnippetSnapshot, o *SnippetVariableOptions) { s.Indent = "" },
		"snapshot size": func(s *SnippetSnapshot, o *SnippetVariableOptions) {
			s.SelectedText = strings.Repeat("x", MaxSnippetBody)
		},
		"path style":      func(s *SnippetSnapshot, o *SnippetVariableOptions) { o.PathStyle = 255 },
		"cursor negative": func(s *SnippetSnapshot, o *SnippetVariableOptions) { o.CursorIndex = -1 },
		"cursor overflow": func(s *SnippetSnapshot, o *SnippetVariableOptions) { o.CursorIndex = 2147483647 },
		"random empty":    func(s *SnippetSnapshot, o *SnippetVariableOptions) { o.Random = []byte{} },
		"random short":    func(s *SnippetSnapshot, o *SnippetVariableOptions) { o.Random = make([]byte, 15) },
		"random long":     func(s *SnippetSnapshot, o *SnippetVariableOptions) { o.Random = make([]byte, 17) },
		"half comment":    func(s *SnippetSnapshot, o *SnippetVariableOptions) { o.BlockCommentStart = "/*" },
		"comment utf8": func(s *SnippetSnapshot, o *SnippetVariableOptions) {
			o.BlockCommentStart = "\xff"
			o.BlockCommentEnd = "*/"
		},
		"clipboard utf8": func(s *SnippetSnapshot, o *SnippetVariableOptions) { text := "\xff"; o.Clipboard = &text },
		"option bytes": func(s *SnippetSnapshot, o *SnippetVariableOptions) {
			text := strings.Repeat("x", MaxSnippetBody+1)
			o.Clipboard = &text
		},
		"literal context":    func(s *SnippetSnapshot, o *SnippetVariableOptions) { o.Variables = map[string]string{"A": "\xff"} },
		"time nano":          func(s *SnippetSnapshot, o *SnippetVariableOptions) { o.Time = &SnippetTime{Nanosecond: 1e9} },
		"time negative nano": func(s *SnippetSnapshot, o *SnippetVariableOptions) { o.Time = &SnippetTime{Nanosecond: -1} },
		"time offset":        func(s *SnippetSnapshot, o *SnippetVariableOptions) { o.Time = &SnippetTime{OffsetSeconds: 86400} },
		"time overflow":      func(s *SnippetSnapshot, o *SnippetVariableOptions) { o.Time = &SnippetTime{UnixSeconds: math.MaxInt64} },
		"time underflow":     func(s *SnippetSnapshot, o *SnippetVariableOptions) { o.Time = &SnippetTime{UnixSeconds: math.MinInt64} },
		"time before year1": func(s *SnippetSnapshot, o *SnippetVariableOptions) {
			o.Time = &SnippetTime{UnixSeconds: -62135596800, OffsetSeconds: -1}
		},
		"time after9999": func(s *SnippetSnapshot, o *SnippetVariableOptions) {
			o.Time = &SnippetTime{UnixSeconds: 253402300799, OffsetSeconds: 1}
		},
		"zone utf8": func(s *SnippetSnapshot, o *SnippetVariableOptions) { o.Time = &SnippetTime{ZoneName: "\xff"} },
	} {
		t.Run(name, func(t *testing.T) {
			s := variableSnapshot()
			o := SnippetVariableOptions{PathStyle: SnippetPathPOSIX}
			change(&s, &o)
			p, err := PrepareSnippetTemplateWithSnapshot("literal", s, o)
			var e *SnippetExpansionError
			if !errors.As(err, &e) || e.Offset != -1 || !reflect.DeepEqual(p, SnippetProgram{}) {
				t.Fatal(p, err)
			}
		})
	}
	var nilTemplate *SnippetTemplate
	if _, err := nilTemplate.PrepareWithSnapshot(SnippetSnapshot{}, SnippetVariableOptions{}); err == nil {
		t.Fatal("nil template")
	}
}

func TestSnippetVariablesLazyLargeContextAndConcurrentReuse(t *testing.T) {
	s := variableSnapshot()
	s.SelectedText = strings.Repeat("a", MaxSnippetBody/2)
	p := variableProgram(t, "${TM_CURRENT_LINE:$SELECTION$TM_SELECTED_TEXT}", s, SnippetVariableOptions{PathStyle: SnippetPathPOSIX})
	if p.Snippet.Text != s.CurrentLine {
		t.Fatal(p)
	}
	s = SnippetSnapshot{Position: Pos{1, 1}, TabWidth: 4, CurrentLine: strings.Repeat("x", MaxSnippetBody-64)}
	p = variableProgram(t, "${UUID:literal}", s, SnippetVariableOptions{})
	if p.Snippet.Text != "literal" {
		t.Fatal(p)
	}
	tree, err := ParseSnippetTemplate("${1:$TM_CURRENT_WORD} $1 $CUSTOM $UUID ${0:end}")
	if err != nil {
		t.Fatal(err)
	}
	s = variableSnapshot()
	o := SnippetVariableOptions{Random: make([]byte, 16), PathStyle: SnippetPathPOSIX}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := tree.PrepareWithSnapshot(s, o)
			if err != nil || p.Snippet.Text != "café_word café_word CUSTOM 00000000-0000-4000-8000-000000000000 end" {
				t.Error(p, err)
			}
			if len(p.Snippet.Fields) > 0 {
				p.Snippet.Fields[0].End = 999
			}
		}()
	}
	wg.Wait()
}

func TestSnippetVariablesTimePurityProcess(t *testing.T) {
	if os.Getenv("RHUN_TEST_SNIPPET_TIME_PURITY") == "1" {
		before := reflect.ValueOf(time.Local).Elem()
		name := before.FieldByName("name").String()
		zones := before.FieldByName("zone").Len()
		if name != "" || zones != 0 {
			t.Fatal("fresh process initialized Local before purity check", name, zones)
		}
		clock := SnippetTime{UnixSeconds: 0, OffsetSeconds: 20700, ZoneName: "Explicit"}
		p := variableProgram(t, "$CURRENT_YEAR $CURRENT_HOUR:$CURRENT_MINUTE $CURRENT_TIMEZONE_NAME", variableSnapshot(), SnippetVariableOptions{Time: &clock, PathStyle: SnippetPathPOSIX})
		if p.Snippet.Text != "1970 05:45 Explicit" {
			t.Fatal(p)
		}
		after := reflect.ValueOf(time.Local).Elem()
		if after.FieldByName("name").String() != name || after.FieldByName("zone").Len() != zones {
			t.Fatal("preparation initialized Local")
		}
		// Explicit conversion is the separate collection boundary. Its initialization
		// proves the canary above observes the lazy timezone path on this platform.
		SnippetTimeFromTime(time.Unix(0, 0).In(time.Local))
		after = reflect.ValueOf(time.Local).Elem()
		if after.FieldByName("name").String() == name && after.FieldByName("zone").Len() == zones {
			t.Fatal("Local initialization canary did not change")
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSnippetVariablesTimePurityProcess$")
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, "TZ") && !strings.EqualFold(key, "RHUN_TEST_SNIPPET_TIME_PURITY") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "TZ=America/New_York", "RHUN_TEST_SNIPPET_TIME_PURITY=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("purity child: %v\n%s", err, output)
	}
}
