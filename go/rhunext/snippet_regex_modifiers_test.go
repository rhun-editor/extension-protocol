package rhunext

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestSnippetRegexModifiersOracle(t *testing.T) {
	data, err := os.ReadFile("testdata/snippet-regex-modifiers.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Source, Flags, Input string
		Accepted             bool
		Captures             [][]int
	}
	if err = json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		p, err := compileSnippetRegex(row.Source, row.Flags)
		if !row.Accepted {
			if err == nil {
				t.Errorf("accepted %q/%s", row.Source, row.Flags)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q/%s: %v", row.Source, row.Flags, err)
			continue
		}
		m := mustRegexMatcher(t, p, row.Input)
		result, err := m.find(0)
		if err != nil {
			t.Fatal(err)
		}
		if got := matchSpans(result); !reflect.DeepEqual(got, row.Captures) {
			t.Errorf("%q/%s input%q: got%v want%v", row.Source, row.Flags, row.Input, got, row.Captures)
		}
		if m.flags != p.flags {
			t.Fatal("leaked flags", m.flags, p.flags)
		}
	}
}

func TestSnippetRegexModifiersProgramAndState(t *testing.T) {
	for _, op := range []uint32{rxScopedAssertion, rxScopedReference} {
		mask := uint32(rxIgnoreCase)
		if op == rxScopedAssertion {
			mask |= uint32(rxMultiline)
		}
		p := &snippetRegexProgram{flags: uint32(rxUnicode), captures: 1, code: []snippetRegexInstruction{{op, 1, mask, 0}, {rxMatch, 0, 0, 0}}}
		base, err := p.encode()
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			field int
			value uint32
		}{
			{4, 0}, {4, 65}, {8, 8}, {8, 128}, {12, 1},
		} {
			data := append([]byte(nil), base...)
			binary.LittleEndian.PutUint32(data[48+tc.field:], tc.value)
			got, err := decodeSnippetRegexProgram(data)
			var detail *snippetRegexProgramError
			if got != nil || !errors.As(err, &detail) || detail.Offset != 48+tc.field {
				t.Fatal(op, tc, got, err)
			}
		}
		p.code = p.code[:1]
		if err := p.validate(); err == nil {
			t.Fatal("scoped fallthrough")
		}
	}
	p := mustCompileRegex(t, `(a)(?i:\1)`, "u")
	for _, work := range []int{0, 1, 10, 1000} {
		for _, assertion := range []bool{false, true} {
			m := mustRegexMatcher(t, p, "Aa")
			m.values[2] = 0
			m.values[3] = 1
			m.pos = 1
			m.budget.remaining = work
			if assertion {
				m.scopedAssertion(3, uint32(rxIgnoreCase|rxMultiline))
			} else {
				m.scopedReference(1, uint32(rxIgnoreCase))
			}
			if m.flags != p.flags {
				t.Fatal("flags leaked", work, assertion)
			}
			if work == 0 && m.err == nil {
				t.Fatal("uncharged scoped helper")
			}
		}
	}
	// The known engine singleton-q/ASCII shortcuts do not define local folding.
	for _, source := range []string{`(?i:[\q{a}])`, `(?i:\p{ASCII})`} {
		p := mustCompileRegex(t, source, "v")
		input := "A"
		if strings.Contains(source, "ASCII") {
			input = "K"
		}
		m := mustRegexMatcher(t, p, input)
		match, err := m.find(0)
		if err != nil || match == nil || match.end != len(input) {
			t.Fatal(source, match, err)
		}
	}
}

func snippetRegexScopedPayloadCases(t testing.TB) [][]byte {
	t.Helper()
	regex := &snippetRegexProgram{flags: uint32(rxUnicode), captures: 1, code: []snippetRegexInstruction{
		{rxScopedAssertion, 3, 6, 0}, {rxScopedReference, 1, 2, 0}, {rxMatch, 0, 0, 0},
	}}
	payload := &snippetTransformPayload{bindings: []snippetTransformBinding{{1, regex, replacementFixture()}}}
	base, err := payload.encode()
	if err != nil {
		t.Fatal(err)
	}
	cases := [][]byte{base}
	regexAt := int(binary.LittleEndian.Uint32(base[36:]))
	for _, pc := range []int{0, 1} {
		for _, field := range []int{0, 4, 8, 12} {
			values := []uint32{0, 1, 2, 4, 5, 64, 65, 0xffffffff}
			if field == 0 {
				values = []uint32{16, 0xffffffff}
			}
			if field == 8 {
				values = []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 16, 128, 0xffffffff}
			}
			if field == 12 {
				values = []uint32{0, 1, 2, 3, 0xffffffff}
			}
			for _, value := range values {
				data := append([]byte(nil), base...)
				binary.LittleEndian.PutUint32(data[regexAt+48+16*pc+field:], value)
				cases = append(cases, data)
			}
		}
	}
	// Remove MATCH and canonicalize lengths so the final reference's missing
	// fallthrough is the only error, including inside the SNTX envelope.
	data := append([]byte(nil), base[:regexAt+80]...)
	data = append(data, base[regexAt+96:]...)
	binary.LittleEndian.PutUint32(data[16:], uint32(len(data)))
	binary.LittleEndian.PutUint32(data[40:], 80)
	binary.LittleEndian.PutUint32(data[44:], uint32(regexAt+80))
	binary.LittleEndian.PutUint32(data[regexAt+20:], 2)
	binary.LittleEndian.PutUint32(data[regexAt+40:], 80)
	cases = append(cases, data)
	return cases
}

func TestSnippetRegexModifiersCompilationContracts(t *testing.T) {
	ast := mustRegex(t, `(?i:(?=(a))\1)(?-i:b)(?m:$)`, "u")
	budget := maxSnippetRegexCompileWork
	want, err := compileSnippetRegexASTBudget(ast, &budget)
	if err != nil {
		t.Fatal(err)
	}
	used := maxSnippetRegexCompileWork - budget
	if partial, err := compileSnippetRegexAST(ast, used-1); err == nil || partial != nil {
		t.Fatal("compile quota")
	}
	if exact, err := compileSnippetRegexAST(ast, used); err != nil || !reflect.DeepEqual(exact, want) {
		t.Fatal("exact compile work", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				got, err := compileSnippetRegexAST(ast, maxSnippetRegexCompileWork)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Error("concurrent", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if want.flags != uint32(rxUnicode) {
		t.Fatal("header flag leak")
	}
	for _, tc := range []struct {
		source string
		offset int
	}{
		{"(?ii:a)", 3}, {"(?i-i:a)", 4}, {"(?-:a)", 3}, {"(?i--m:a)", 4}, {"(?i", 3},
	} {
		_, err := parseSnippetRegex(tc.source, "")
		var detail *snippetRegexError
		if !errors.As(err, &detail) || detail.Offset != tc.offset {
			t.Fatal(tc, err)
		}
	}
	if ast, err := parseSnippetRegex(strings.Repeat("(?i:", 33)+"a"+strings.Repeat(")", 33), ""); err == nil || ast != nil {
		t.Fatal("modifier depth")
	}
	program, err := PrepareSnippetTemplate(`${1:aA} ${1/(?i:(a)\1)/${0:/upcase}/} ${NAME/(?i:(a)\1)/${0:/upcase}/}`, SnippetContext{Variables: map[string]string{"NAME": "aA"}})
	if err != nil || program.Snippet.Text != "aA aA AA" || len(program.Transforms) != 1 {
		t.Fatal(program, err)
	}
	source := `😀 ${NAME/(?i-i:a)/x/}`
	partial, err := PrepareSnippetTemplate(source, SnippetContext{})
	var detail *SnippetExpansionError
	if !errors.As(err, &detail) || detail.Offset != strings.Index(source, "-i")+1 || partial.Snippet.Text != "" {
		t.Fatal("mapped source", partial, err)
	}
}
