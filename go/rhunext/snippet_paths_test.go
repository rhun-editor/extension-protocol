package rhunext

import (
	"reflect"
	"strings"
	"testing"
)

func TestSnippetVariablesPortablePaths(t *testing.T) {
	for _, tc := range []struct {
		path, workspace string
		style           SnippetPathStyle
		want            []string
	}{
		{"/p/src/file.tar.go", "/p", SnippetPathPOSIX, []string{"file.tar.go", "file.tar", "/p/src", "src", "p", "src/file.tar.go"}},
		{"/p/.gitignore", "/p", SnippetPathPOSIX, []string{".gitignore", ".gitignore", "/p", "p", "p", ".gitignore"}},
		{"/p/file.", "/p", SnippetPathPOSIX, []string{"file.", "file", "/p", "p", "p", "file."}},
		{"/p/noext", "/p", SnippetPathPOSIX, []string{"noext", "noext", "/p", "p", "p", "noext"}},
		{"/p//a/../b/./file.go", "/p/", SnippetPathPOSIX, []string{"file.go", "file", "/p/b", "b", "p", "b/file.go"}},
		{"/project-two/a", "/project", SnippetPathPOSIX, []string{"a", "a", "/project-two", "project-two", "project", "/project-two/a"}},
		{"/p/back\\slash.go", "/p", SnippetPathPOSIX, []string{"back\\slash.go", "back\\slash", "/p", "p", "p", "back\\slash.go"}},
		{"/../../a", "/", SnippetPathPOSIX, []string{"a", "a", "/", "", "", "a"}},
		{"/", "/", SnippetPathPOSIX, []string{"", "", "/", "", "", "."}},
		{`c:\Proj\src\a.go`, "C:/proj", SnippetPathWindows, []string{"a.go", "a", "c:/Proj/src", "src", "proj", "src/a.go"}},
		{"D:/p/a", "C:/p", SnippetPathWindows, []string{"a", "a", "D:/p", "p", "p", "D:/p/a"}},
		{"C:/project-two/a", "c:/project", SnippetPathWindows, []string{"a", "a", "C:/project-two", "project-two", "project", "C:/project-two/a"}},
		{`\\server\share\project\a.go`, "//SERVER/SHARE/project", SnippetPathWindows, []string{"a.go", "a", "//server/share/project", "project", "project", "a.go"}},
		{"//server/share/a.go", "//server/share", SnippetPathWindows, []string{"a.go", "a", "//server/share/", "", "", "a.go"}},
		{"C:/", "c:/", SnippetPathWindows, []string{"", "", "C:/", "", "", "."}},
		{"C:/Ärea/a", "c:/ärea", SnippetPathWindows, []string{"a", "a", "C:/Ärea", "Ärea", "ärea", "a"}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			s := variableSnapshot()
			s.Path = tc.path
			s.Workspace = tc.workspace
			p := variableProgram(t, "$TM_FILENAME|$TM_FILENAME_BASE|$TM_DIRECTORY|$TM_DIRECTORY_BASE|$WORKSPACE_NAME|$RELATIVE_FILEPATH|$TM_FILEPATH|$WORKSPACE_FOLDER", s, SnippetVariableOptions{PathStyle: tc.style})
			want := append(append([]string{}, tc.want...), tc.path, tc.workspace)
			if p.Snippet.Text != strings.Join(want, "|") {
				t.Fatalf("%q want %q", p.Snippet.Text, strings.Join(want, "|"))
			}
		})
	}
}

func TestSnippetVariablesPathGuardsAndLimits(t *testing.T) {
	for _, tc := range []struct {
		path  string
		style SnippetPathStyle
	}{
		{"relative/a", SnippetPathPOSIX}, {"C:/a", SnippetPathPOSIX}, {"C:a", SnippetPathWindows},
		{"/a", SnippetPathWindows}, {"//server", SnippetPathWindows}, {"//server/", SnippetPathWindows},
		{"//server/../a", SnippetPathWindows}, {"//./pipe/name", SnippetPathWindows}, {"//?/C:/a", SnippetPathWindows},
		{"/a\x00b", SnippetPathPOSIX}, {"/" + strings.Repeat("x", MaxSnippetPathBytes), SnippetPathPOSIX},
		{"/" + strings.Repeat("a/", MaxSnippetPathComponents+1), SnippetPathPOSIX},
		{"//server/share/" + strings.Repeat("a/", MaxSnippetPathComponents-1), SnippetPathWindows},
	} {
		s := variableSnapshot()
		s.Path = tc.path
		s.Workspace = ""
		p, err := PrepareSnippetTemplateWithSnapshot("unused path", s, SnippetVariableOptions{PathStyle: tc.style})
		if err == nil || !reflect.DeepEqual(p, SnippetProgram{}) {
			t.Fatal(tc, p, err)
		}
	}
	for _, path := range []string{"/" + strings.Repeat("x", MaxSnippetPathBytes-1), "/" + strings.Repeat("a/", MaxSnippetPathComponents)} {
		s := variableSnapshot()
		s.Path = path
		s.Workspace = ""
		variableProgram(t, "$TM_FILENAME", s, SnippetVariableOptions{PathStyle: SnippetPathPOSIX})
	}
	s := variableSnapshot()
	s.Path = ""
	s.Workspace = "relative"
	if _, err := PrepareSnippetTemplateWithSnapshot("unused", s, SnippetVariableOptions{PathStyle: SnippetPathPOSIX}); err == nil {
		t.Fatal("relative workspace accepted")
	}
}

func FuzzSnippetVariablesPrepare(f *testing.F) {
	for _, source := range []string{"$TM_CURRENT_WORD ${1:$TM_SELECTED_TEXT} $1 ${0:end}", "${TM_FILENAME:default}|$UUID|$CUSTOM", "${1|a,b|} ${CLIPBOARD:empty}", "${TM_CURRENT_LINE:$SELECTION$TM_SELECTED_TEXT}"} {
		f.Add(source, "  café_word next", "selected", "/p/a.go", "/p", uint16(4), byte(0))
	}
	f.Fuzz(func(t *testing.T, source, line, selected, path, workspace string, column uint16, mode byte) {
		if len(source)+len(line)+len(selected)+len(path)+len(workspace) > 64<<10 {
			t.Skip()
		}
		col := int(column) % (len(line) + 1)
		indent := 0
		for indent < col && (line[indent] == ' ' || line[indent] == '\t') {
			indent++
		}
		s := SnippetSnapshot{Position: Pos{1, col + 1}, TabWidth: 4, CurrentLine: line, SelectedText: selected, Path: path, Workspace: workspace, Indent: line[:indent]}
		o := SnippetVariableOptions{PathStyle: SnippetPathStyle(mode % 4)}
		if mode%3 == 1 {
			o.Random = make([]byte, 16)
		}
		if mode%3 == 2 {
			o.Time = &SnippetTime{UnixSeconds: int64(int8(mode))}
		}
		p, err := PrepareSnippetTemplateWithSnapshot(source, s, o)
		if err != nil {
			if !reflect.DeepEqual(p, SnippetProgram{}) {
				t.Fatal("partial program", p)
			}
			return
		}
		if err := p.Validate(); err != nil {
			t.Fatal("invalid successful program", err)
		}
	})
}
