package rhunext

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const snippetTransformWireGolden = "U05UWAEAAAAAABAAAQAAAMEAAAAAAAAAAAAAAAAAAAABAAAAOAAAAFAAAACIAAAAOQAAAAAAAABSTlJYAQAAAAAAEAAAAAAAAAAAAAIAAAAAAAAAAAAAAAAAAAAAAAAAUAAAAAAAAAABAAAAYQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAFJOUkYBAAAAAAAQAAEAAAABAAAAOQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAQAAAAAAAAAAAAAAeA=="

func wireTransformProgram(t testing.TB) SnippetProgram {
	t.Helper()
	compiled, e := CompileSnippetTransform("a", "x", "")
	if e != nil {
		t.Fatal(e)
	}
	return SnippetProgram{Snippet: PreparedSnippet{Text: "a", Fields: []Field{{1, 0, 1, 0}}}, Transforms: []SnippetFieldTransform{{1, compiled}}}
}
func TestSnippetTransformProgramWireGoldenAndSnapshot(t *testing.T) {
	p := wireTransformProgram(t)
	want := "0 0 0 0 snippet 0 1 0 293\nafield 1 0 1 0 0\ntransforms 0 260\n" + snippetTransformWireGolden
	body, e := p.Encode()
	if e != nil || string(body) != want {
		t.Fatalf("%q %v", body, e)
	}
	raw, e := base64.StdEncoding.Strict().DecodeString(snippetTransformWireGolden)
	if e != nil {
		t.Fatal(e)
	}
	imported, e := DecodeSnippetTransforms(raw)
	if e != nil || !reflect.DeepEqual(imported, p.Transforms) {
		t.Fatal(imported, e)
	}
	it := (CompletionItem{Label: "label"}).WithSnippetProgram(p)
	p.Transforms[0].Field = 0
	p.Snippet.Fields[0].Group = 0
	got := string((CompletionList{Items: []CompletionItem{it}}).Encode())
	if !strings.Contains(got, "transforms 0 260\n"+snippetTransformWireGolden) {
		t.Fatal("snapshot", got)
	}
	got = string((CompletionList{Items: []CompletionItem{it.WithSnippet(PreparedSnippet{Text: "plain"})}}).Encode())
	if strings.Contains(got, "transforms") || strings.Contains(got, "field ") {
		t.Fatal("old metadata", got)
	}
}
func TestSnippetTransformBindingsCanonicalAndInvalid(t *testing.T) {
	compiled := wireTransformProgram(t).Transforms[0].Transform
	p := SnippetProgram{Snippet: PreparedSnippet{Text: "a a", Fields: []Field{{1, 0, 1, 0}, {1, 2, 3, 0}, {0, 3, 3, 0}}}, Transforms: []SnippetFieldTransform{{2, compiled}, {1, compiled}}}
	before := append([]SnippetFieldTransform(nil), p.Transforms...)
	if e := p.Validate(); e != nil {
		t.Fatal(e)
	}
	raw := encodeSnippetTransforms(p.Transforms, p.Snippet.Fields)
	decoded, e := DecodeSnippetTransforms(raw)
	if e != nil || len(decoded) != 2 || decoded[0].Field != 1 || decoded[1].Field != 2 || !reflect.DeepEqual(before, p.Transforms) {
		t.Fatal(decoded, e)
	}
	for _, bindings := range [][]SnippetFieldTransform{{{0, compiled}}, {{-1, compiled}}, {{4, compiled}}, {{3, compiled}}, {{1, compiled}, {1, compiled}}, {{1, CompiledSnippetTransform{}}}, make([]SnippetFieldTransform, 513)} {
		p.Transforms = bindings
		if e := p.Validate(); e == nil {
			t.Fatal("invalid accepted", bindings)
		}
		if body, e := p.Encode(); body != nil || e == nil {
			t.Fatal("partial encode")
		}
		out := (CompletionList{Items: []CompletionItem{(CompletionItem{Label: "label"}).WithSnippetProgram(p)}}).Encode()
		if !bytes.HasSuffix(out, []byte("labela a")) || bytes.Contains(out, []byte("field ")) || bytes.Contains(out, []byte("transforms ")) {
			t.Fatal("not whole metadata fallback", string(out))
		}
	}
}
func TestSnippetTransformMetadataCapsAndCompletionOmission(t *testing.T) {
	p := wireTransformProgram(t)
	compiled, e := CompileSnippetTransform("a", strings.Repeat("x", 900000), "")
	if e != nil {
		t.Fatal(e)
	}
	p.Transforms[0].Transform = compiled
	if e := p.Validate(); e != nil {
		t.Fatal("valid explicit program", e)
	}
	if got := string((CompletionList{Items: []CompletionItem{(CompletionItem{Label: "label"}).WithSnippetProgram(p)}}).Encode()); got != "0 1\n" {
		t.Fatal("oversized valid item", got[:min(100, len(got))])
	}
	p.Snippet = PreparedSnippet{Fields: make([]Field, MaxSnippetFields)}
	p.Transforms = make([]SnippetFieldTransform, MaxSnippetFields)
	for i := range p.Snippet.Fields {
		p.Snippet.Fields[i].Group = 1
		p.Transforms[i] = SnippetFieldTransform{i + 1, compiled}
	}
	if e := p.Validate(); e == nil {
		t.Fatal("aggregate payload cap")
	}
	// Length-only preflight rejects this invalid oversized pair before decoding it.
	p = wireTransformProgram(t)
	p.Transforms[0].Transform = CompiledSnippetTransform{regex: "bad", replacement: strings.Repeat("x", MaxSnippetBody)}
	if body, e := p.Encode(); e == nil || body != nil {
		t.Fatal("oversize partial")
	}
}
func TestSnippetTransformMissingNegotiationDoesNotWrite(t *testing.T) {
	p := wireTransformProgram(t)
	for _, agreed := range []map[string]bool{{}, {FeatureSnippets: true}, {FeatureSnippetTransforms: true}} {
		wire := make(wireWriter, 4)
		ext := testConnection(t, wire)
		ext.agreed = agreed
		if _, e := ext.InsertSnippetProgramContext(context.Background(), 7, 9, Range{Pos{1, 1}, Pos{1, 1}}, p); e == nil {
			t.Fatal("missing feature accepted")
		}
		if len(wire) != 0 {
			t.Fatal(fmt.Sprint("wrote without negotiation ", len(wire)))
		}
	}
}

func TestSnippetTransformExactCompletionBoundary(t *testing.T) {
	p := wireTransformProgram(t)
	p.Snippet.Text += strings.Repeat("z", MaxBody-1024)
	encode := func() []byte {
		return (CompletionList{Items: []CompletionItem{(CompletionItem{Label: "label"}).WithSnippetProgram(p)}}).Encode()
	}
	size := len(encode()) - 4 + 32
	p.Snippet.Text += strings.Repeat("z", MaxBody-size)
	if len(encode())-4+32 != MaxBody {
		t.Fatal("exact completion boundary")
	}
	p.Snippet.Text += "z"
	if e := p.Validate(); e != nil {
		t.Fatal(e)
	}
	if got := string(encode()); got != "0 1\n" {
		t.Fatal("one over", len(got))
	}
}
