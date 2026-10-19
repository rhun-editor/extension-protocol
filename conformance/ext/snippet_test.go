package main

import (
	"encoding/base64"
	"fmt"
	rx "github.com/rhun-editor/extension-protocol/go/rhunext"
	"strings"
	"testing"
)

func TestDecodePreparedSnippetWireAndRejectMalformedRecords(t *testing.T) {
	valid := "0 0 0 0 snippet 0 3 0 48\nx xfield 1 0 1 0 0\nfield 1 2 3 0 0\nfield 0 3 3 0 0\n"
	s, err := decodeSnippet([]byte(valid))
	if err != nil || s.Text != "x x" || len(s.Fields) != 3 {
		t.Fatal(s, err)
	}
	for _, body := range []string{
		"", strings.TrimSuffix(valid, "\n"), valid + "x",
		strings.Replace(valid, "snippet 0", "text 0", 1),
		strings.Replace(valid, "snippet 0", "snippet 1", 1),
		strings.Replace(valid, "0 3 0 48", "0 9223372036854775808 0 48", 1),
		strings.Replace(valid, "field 1 0 1 0 0", "field 1 0 1 0 1", 1),
		strings.Replace(valid, "field 1 0 1 0 0", "field 1 0 1 1 0", 1),
		strings.Replace(valid, "field 1 0 1 0 0", "other 1 0 1 0 0", 1),
	} {
		if _, err := decodeSnippet([]byte(body)); err == nil {
			t.Fatal("accepted malformed record", body)
		}
	}
}

func TestSnippetRequestValidatesBeforeAtomicReplacementAndSelectsFirstGroup(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		body, code string
		agreed     bool
	}{
		{"insert", []string{"1", "0", "1", "1", "1", "1"}, "0 0 0 0 snippet 0 3 0 32\nx yfield 2 0 1 0 0\nfield 1 2 3 0 0\n", "", true},
		{"empty", []string{"1", "0", "1", "1", "1", "1"}, "0 0 0 0 snippet 0 0 0 0\n", "", true},
		{"feature", []string{"1", "0", "1", "1", "1", "1"}, "", "unknown", false},
		{"stale", []string{"1", "1", "1", "1", "1", "1"}, "", "stale", true},
		{"overflow", []string{"1", "9223372036854775808", "1", "1", "1", "1"}, "", "syntax", true},
		{"closed", []string{"99", "0", "1", "1", "1", "1"}, "", "nodoc", true},
		{"badpos", []string{"1", "0", "1", "0", "1", "1"}, "", "badpos", true},
		{"reversed", []string{"1", "0", "1", "2", "1", "1"}, "", "badpos", true},
		{"malformed", []string{"1", "0", "1", "1", "1", "1"}, "0 0 0 0 snippet 0 1 0 16\nxfield 1 0 2 0 0\n", "syntax", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := testHost(t)
			h.stdin = &sink{}
			h.features = map[string]bool{"snippets": tc.agreed}
			d := h.docs[1]
			before, version := string(d.text), d.version
			code, result := "", ""
			h.snippetRequest(tc.args, []byte(tc.body), func(s string) { result = s }, func(c, s string) { code = c }, 101)
			if code != tc.code {
				t.Fatalf("code %q want %q", code, tc.code)
			}
			if code != "" {
				if string(d.text) != before || d.version != version {
					t.Fatal("invalid request changed document")
				}
			} else if result != fmt.Sprintf("ok 101 %d", d.version) {
				t.Fatal(result)
			}
			if tc.name == "insert" && (!strings.HasPrefix(string(d.text), "x ypackage") || d.cur != 3 || d.anchor != 2) {
				t.Fatal(string(d.text), d.cur, d.anchor)
			}
			if tc.name == "empty" && d.version != version {
				t.Fatal("empty insertion changed version")
			}
		})
	}
}

func TestCompletionExtraChecksPreparedFieldGraph(t *testing.T) {
	for _, tc := range []struct {
		insert, extra string
		valid         bool
	}{
		{"x x", "field 1 0 1 0 0\nfield 1 2 3 0 0\n", true},
		{"x x", "field 1 0 1 0 0\nfield 2 2 3 0 0\nfield 2 0 1 1 0\nfield 1 2 3 2 0\n", false},
		{"😀", "field 1 1 4 0 0\n", false},
		{"x", "field 1 0 2 0 0\n", false},
	} {
		if why := checkExtra([]byte(tc.extra), &doc{}, 0, 0, []byte(tc.insert), nil); (why == "") != tc.valid {
			t.Fatal(tc, why)
		}
	}
}

func TestDecodeSnippetChoicesBeforeAndAfterFields(t *testing.T) {
	fields := "field 1 0 1 0 0\nfield 1 2 3 0 0\n"
	choice := "choice 1 3 13\n1\nx0\n6\na\n😀"
	frame := func(extra string) []byte {
		return []byte(fmt.Sprintf("0 0 0 0 snippet 0 3 0 %d\nx x%s", len(extra), extra))
	}
	for _, extra := range []string{fields + choice, choice + fields} {
		p, err := decodeSnippetProgram(frame(extra))
		if err != nil || len(p.Choices) != 1 || len(p.Choices[0].Options) != 3 || p.Choices[0].Options[2] != "a\n😀" {
			t.Fatal(p, err)
		}
		if why := checkExtra([]byte(extra), &doc{}, 0, 0, []byte("x x"), nil); why != "" {
			t.Fatal(why)
		}
	}
	for _, bad := range []string{
		"choice 1 0 0\n", "choice 1 257 3\n1\nx", "choice 0 1 3\n1\nx", "choice 2 1 3\n1\nx",
		"choice 1 1 3\n1\ny", "choice 1 1 3\n1\n\xff", "choice 1 2 3\n1\nx",
		"choice 1 1 4\n1\nx!", "choice 1 1 2\n1x", "choice 1 1 3\n2\nx",
		"choice 1 1 3\n-1\n", "choice 1 1 3\n+1x", "choice 1 1 3\n1\nx" + choice,
		"choice 1 1 9223372036854775808\n", "choice 1 9223372036854775808 3\n1\nx",
		"choice 9223372036854775808 1 3\n1\nx", "choice 1 1 3\n1\nxextra", "choice 1 1 0\n",
	} {
		if _, err := decodeSnippetProgram(frame(fields + bad)); err == nil {
			t.Fatalf("accepted %q", bad)
		}
		if why := checkExtra([]byte(fields+bad), &doc{}, 0, 0, []byte("x x"), nil); why == "" {
			t.Fatalf("completion accepted %q", bad)
		}
	}
}

func TestSnippetFinalOnlySelectsWholeRange(t *testing.T) {
	h, _ := testHost(t)
	h.stdin = &sink{}
	h.features = map[string]bool{"snippets": true}
	h.snippetRequest([]string{"1", "0", "1", "1", "1", "1"}, []byte("0 0 0 0 snippet 0 6 0 16\nresultfield 0 0 6 0 0\n"), func(string) {}, func(c, s string) { t.Fatal(c, s) }, 101)
	if h.docs[1].cur != 6 || h.docs[1].anchor != 0 {
		t.Fatal(h.docs[1].cur, h.docs[1].anchor)
	}
}

func TestInitialVariantFramingAndAgreement(t *testing.T) {
	fields := "field 1 0 1 0 0\nfield 1 2 5 0 0\n"
	valid := fields + "variant 2 0\n"
	frame := func(extra string) []byte {
		return []byte(fmt.Sprintf("0 0 0 0 snippet 0 5 0 %d\na ABC%s", len(extra), extra))
	}
	both := map[string]bool{"snippets": true, "snippet-variants": true}
	for _, extra := range []string{valid, "variant 2 0\n" + fields, "variant " + strings.Repeat("0", 1000) + "2 0\n" + fields} {
		p, err := decodeSnippetProgram(frame(extra))
		if err != nil || len(p.InitialVariants) != 1 || p.InitialVariants[0] != 2 {
			t.Fatalf("%+v %v", p, err)
		}
		for _, features := range []map[string]bool{nil, {"snippets": true}, {"snippet-variants": true}, both} {
			why := checkExtra([]byte(extra), &doc{}, 0, 0, []byte("a ABC"), features)
			if (why == "") != (features["snippets"] && features["snippet-variants"]) {
				t.Fatalf("%v: %s", features, why)
			}
		}
	}
	for _, line := range []string{"variant 1 0\n", "variant 0 0\n", "variant 3 0\n", "variant 2 00\n", "variant 2 0\nvariant 2 0\n", "variant 18446744073709551618 0\n", "variant -2 0\n", "variant 2 1\nx"} {
		if _, err := decodeSnippetProgram(frame(fields + line)); err == nil {
			t.Fatalf("accepted %q", line)
		}
		if why := checkExtra([]byte(fields+line), &doc{}, 0, 0, []byte("a ABC"), both); why == "" {
			t.Fatalf("accepted completion %q", line)
		}
	}
}

func TestCompiledTransformFramingAndAgreement(t *testing.T) {
	compiled, err := rx.CompileSnippetTransform("a", "x", "")
	if err != nil {
		t.Fatal(err)
	}
	p := rx.SnippetProgram{Snippet: rx.PreparedSnippet{Text: "a", Fields: []rx.Field{{Group: 1, Start: 0, End: 1}}}, Transforms: []rx.SnippetFieldTransform{{Field: 1, Transform: compiled}}}
	body, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeSnippetProgram(body)
	if err != nil || len(decoded.Transforms) != 1 {
		t.Fatal(decoded, err)
	}
	extra := string(body[strings.Index(string(body), "field "):])
	split := strings.Index(extra, "transforms ")
	record := extra[split:]
	fields := extra[:split]
	frame := func(extra string) []byte {
		return []byte(fmt.Sprintf("0 0 0 0 snippet 0 1 0 %d\na%s", len(extra), extra))
	}
	both := map[string]bool{"snippets": true, "snippet-transforms": true}
	for _, extra := range []string{fields + record, record + fields} {
		for _, features := range []map[string]bool{{}, {"snippets": true}, {"snippet-transforms": true}, both} {
			_, err := decodeSnippetProgramFeatures(frame(extra), features)
			why := checkExtra([]byte(extra), &doc{}, 0, 0, []byte("a"), features)
			valid := features["snippets"] && features["snippet-transforms"]
			if (err == nil) != valid || (why == "") != valid {
				t.Fatal(features, err, why)
			}
		}
	}
	empty := make([]byte, 32)
	copy(empty, "SNTX")
	empty[4] = 1
	empty[10] = 16
	empty[16] = 32
	encoded := base64.StdEncoding.EncodeToString(empty)
	emptyRecord := fmt.Sprintf("transforms 0 %d\n%s", len(encoded), encoded)
	if _, err := decodeSnippetProgramFeatures(frame(fields+emptyRecord), both); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{record + record, record + emptyRecord, strings.Replace(record, "transforms 0 ", "transforms 00 ", 1), "transforms 0 0\n", "transforms 0 4\nZh==", "transforms 0 4\nZg=\n", strings.Replace(record, "field", "bad", 1) + "garbage", record[:len(record)-1]} {
		if _, err := decodeSnippetProgramFeatures(frame(fields+bad), both); err == nil {
			t.Fatal("accepted", bad)
		}
		if why := checkExtra([]byte(fields+bad), &doc{}, 0, 0, []byte("a"), both); why == "" {
			t.Fatal("completion accepted", bad)
		}
	}
}
