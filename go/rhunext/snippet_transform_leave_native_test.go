package rhunext

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

type snippetLeaveBinding struct {
	field                 int
	source, flags, format string
}
type snippetLeaveStep struct {
	direction          int
	text               string
	cur, anchor, group int
	changed, undo      bool
}
type snippetLeaveVector struct {
	text, prefix  string
	fields        []Field
	bindings      []snippetLeaveBinding
	steps         []snippetLeaveStep
	width, spaces int
}

func snippetLeaveVectors() []snippetLeaveVector {
	heavy := strings.Repeat("a", 80) + "!"
	failureText := "a-" + heavy + "-b"
	f := []Field{{1, 0, 1, 0}, {1, 2, 3, 0}, {2, 4, 5, 0}, {0, 5, 5, 0}}
	return []snippetLeaveVector{
		{"a-z a-z", "", []Field{{2, 0, 3, 0}, {1, 0, 1, 1}, {2, 4, 7, 0}, {3, 7, 7, 0}}, []snippetLeaveBinding{{2, "a", "", "XX"}}, []snippetLeaveStep{{1, "XX-z XX-z", 4, 0, 2, true, true}}, 4, 1},
		{"a", "", []Field{{1, 0, 1, 0}}, []snippetLeaveBinding{{1, "a", "", ""}}, []snippetLeaveStep{{1, "", 0, 0, 0, true, true}}, 4, 1},
		{failureText, "", []Field{{1, 0, 1, 0}, {1, 2, 2 + len(heavy), 0}, {2, 3 + len(heavy), 4 + len(heavy), 0}}, []snippetLeaveBinding{{1, "a", "", "x"}, {2, "^(a+)+$", "", "x"}}, []snippetLeaveStep{{1, failureText, 1, 0, 0, false, false}}, 4, 1},
		{"a-a-b", "", f, []snippetLeaveBinding{{2, "a", "", "XX"}}, []snippetLeaveStep{{1, "a-XX-b", 6, 5, 2, true, true}}, 4, 1},
		{"a-b-c", "", f, []snippetLeaveBinding{{1, "a", "", "A"}, {2, "b", "", "BB"}}, []snippetLeaveStep{{1, "A-BB-c", 6, 5, 2, true, true}}, 4, 1},
		{"a-a-b", "", f, []snippetLeaveBinding{{1, "a", "", "XX"}}, []snippetLeaveStep{{1, "XX-a-b", 6, 5, 2, true, true}}, 4, 1},
		{"a", "", []Field{{1, 0, 1, 0}}, []snippetLeaveBinding{{1, "", "", "x$0"}}, []snippetLeaveStep{{-1, "xa", 2, 0, 1, true, false}, {-1, "xxa", 3, 0, 1, true, false}, {1, "xxxa", 4, 4, 0, true, true}}, 4, 1},
		{"a😀a", "", []Field{{1, 0, 6, 0}}, []snippetLeaveBinding{{1, "a", "g", "x"}}, []snippetLeaveStep{{1, "x😀x", 6, 6, 0, true, true}}, 4, 1},
		{"", "", []Field{{1, 0, 0, 0}, {2, 0, 0, 0}}, []snippetLeaveBinding{{1, "", "g", "x"}}, []snippetLeaveStep{{1, "x", 1, 1, 2, true, true}}, 4, 1},
		{"a", "", []Field{{1, 0, 1, 0}}, []snippetLeaveBinding{{1, "a", "", "a"}}, []snippetLeaveStep{{1, "a", 1, 1, 0, false, false}}, 4, 1},
		{"", "", []Field{{1, 0, 0, 0}}, []snippetLeaveBinding{{1, "a", "", ""}}, []snippetLeaveStep{{1, "", 0, 0, 0, false, false}}, 4, 1},
		{"b", "", []Field{{1, 0, 1, 0}}, []snippetLeaveBinding{{1, "(a)", "", "${1:-fallback}"}}, []snippetLeaveStep{{1, "fallback", 8, 8, 0, true, true}}, 4, 1},
		{"a", " \t", []Field{{1, 0, 1, 0}}, []snippetLeaveBinding{{1, "a", "", "x\r\n\ty\r"}}, []snippetLeaveStep{{1, " \tx\n        y\n    ", 18, 18, 0, true, true}}, 4, 1},
		{"a", "  ", []Field{{1, 0, 1, 0}}, []snippetLeaveBinding{{1, "a", "", "x\n  y\n"}}, []snippetLeaveStep{{1, "  x\n\ty\n  ", 9, 9, 0, true, true}}, 4, 0},
		{"a", "", []Field{{1, 0, 1, 0}, {2, 0, 1, 1}, {3, 1, 1, 0}}, []snippetLeaveBinding{{1, "a", "", "XX"}}, []snippetLeaveStep{{1, "XX", 2, 2, 3, true, true}}, 4, 1},
		{"a\x00b", "", []Field{{1, 0, 3, 0}}, []snippetLeaveBinding{{1, "(.*)", "", "${1:/upcase}"}}, []snippetLeaveStep{{1, "A\x00B", 3, 3, 0, true, true}}, 4, 1},
		{"a-a-b", "", f, []snippetLeaveBinding{{2, "a", "", "XX"}}, []snippetLeaveStep{{1, "a-XX-b", 6, 5, 2, true, false}, {-1, "a-XX-b", 1, 0, 1, false, false}, {1, "a-XX-b", 6, 5, 2, false, false}, {1, "a-XX-b", 6, 6, 0, false, false}}, 4, 1},
		{"aa", "", []Field{{1, 0, 0, 0}, {1, 0, 0, 0}, {2, 2, 2, 0}}, []snippetLeaveBinding{{1, "", "", "A"}, {2, "", "", "B"}}, []snippetLeaveStep{{1, "ABaa", 4, 4, 2, true, true}}, 4, 1},
	}
}

func snippetLeaveNativeVectors(t testing.TB) []byte {
	t.Helper()
	var data, rows bytes.Buffer
	for i, v := range snippetLeaveVectors() {
		p := SnippetProgram{Snippet: PreparedSnippet{Text: v.text, Fields: v.fields}}
		p.detectInitialVariants()
		if e := p.Validate(); e != nil {
			t.Fatalf("vector%d: %v", i, e)
		}
		variants := map[int]bool{}
		for _, n := range p.InitialVariants {
			variants[n] = true
		}
		payload := &snippetTransformPayload{}
		for _, b := range v.bindings {
			regex, err := compileSnippetRegex(b.source, b.flags)
			if err != nil {
				t.Fatal(err)
			}
			payload.bindings = append(payload.bindings, snippetTransformBinding{b.field, regex, snippetTransformFormat(t, b.format)})
		}
		raw, e := payload.encode()
		if e != nil {
			t.Fatal(e)
		}
		fmt.Fprintf(&rows, ".quad .Lstlv_text_%d,%d,%d,.Lstlv_fields_%d,%d,.Lstlv_payload_%d,%d,.Lstlv_steps_%d,%d,%d,%d\n", i, len(v.prefix+v.text), len(v.prefix), i, len(v.fields), i, len(raw), i, len(v.steps), v.width, v.spaces)
		fmt.Fprintf(&data, ".Lstlv_text_%d:\n", i)
		snippetRegexEmitBytes(&data, []byte(v.prefix+v.text))
		data.WriteString(".p2align 3\n")
		fmt.Fprintf(&data, ".Lstlv_fields_%d:\n", i)
		for j, f := range v.fields {
			flag := 0
			if variants[j+1] {
				flag = 64
			}
			fmt.Fprintf(&data, ".quad %d,%d,%d,%d,%d\n", f.Group, f.Start, f.End, f.Parent, flag)
		}
		fmt.Fprintf(&data, ".Lstlv_payload_%d:\n", i)
		snippetRegexEmitBytes(&data, raw)
		data.WriteString(".p2align 3\n")
		fmt.Fprintf(&data, ".Lstlv_steps_%d:\n", i)
		for j, s := range v.steps {
			ch, un := 0, 0
			if s.changed {
				ch = 1
			}
			if s.undo {
				un = 1
			}
			fmt.Fprintf(&data, ".quad %d,.Lstlv_output_%d_%d,%d,%d,%d,%d,%d,%d\n", s.direction, i, j, len(s.text), s.cur, s.anchor, s.group, ch, un)
		}
		for j, s := range v.steps {
			fmt.Fprintf(&data, ".Lstlv_output_%d_%d:\n", i, j)
			snippetRegexEmitBytes(&data, []byte(s.text))
		}
	}
	api := &snippetTransformPayload{bindings: []snippetTransformBinding{{1, func() *snippetRegexProgram {
		p, e := compileSnippetRegex("a", "")
		if e != nil {
			t.Fatal(e)
		}
		return p
	}(), snippetTransformFormat(t, "XX")}}}
	apiRaw, err := api.encode()
	if err != nil {
		t.Fatal(err)
	}
	data.WriteString(".Lstlv_api_payload:\n")
	snippetRegexEmitBytes(&data, apiRaw)
	data.WriteString(".Lstlv_api_payload_end:\n")
	var out bytes.Buffer
	out.WriteString(".p2align 3\n.Lstlv_vectors:\n")
	out.Write(rows.Bytes())
	out.WriteString(".quad 0\n")
	out.Write(data.Bytes())
	return out.Bytes()
}

func TestSnippetTransformLeaveNativeVectorExport(t *testing.T) {
	a, b := snippetLeaveNativeVectors(t), snippetLeaveNativeVectors(t)
	if !bytes.Equal(a, b) {
		t.Fatal("nondeterministic export")
	}
	if path := os.Getenv("RHUN_TRANSFORM_LEAVE_ASM_OUT"); path != "" {
		if e := snippetRegexWriteExport(path, a); e != nil {
			t.Fatal(e)
		}
	}
}
