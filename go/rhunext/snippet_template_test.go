package rhunext

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// Summaries expose tree structure and decoded values, independently of wire encoding.
func stSummary(nodes []stNode) string {
	var out []string
	for _, n := range nodes {
		switch n.kind {
		case stText:
			out = append(out, "text:"+strconv.Quote(n.text))
		case stTabstop:
			out = append(out, fmt.Sprintf("stop:%d", n.index))
		case stPlaceholder:
			out = append(out, fmt.Sprintf("field:%d[%s]", n.index, stSummary(n.children)))
		case stChoice:
			out = append(out, fmt.Sprintf("choice:%d%q", n.index, n.choices))
		case stVariable:
			s := "var:" + n.name
			if n.hasDefault {
				s += "[" + stSummary(n.children) + "]"
			}
			out = append(out, s)
		case stTransform:
			target := n.name
			if target == "" {
				target = strconv.Itoa(n.index)
			}
			out = append(out, fmt.Sprintf("transform:%s regex=%q format=%s flags=%q",
				target, n.transform.regex, stFormatSummary(n.transform.format), n.transform.options))
		}
	}
	return strings.Join(out, ";")
}

func stFormatSummary(parts []stFormat) string {
	var out []string
	for _, part := range parts {
		switch part.kind {
		case stFormatText:
			out = append(out, "text:"+strconv.Quote(part.text))
		case stCapture:
			out = append(out, fmt.Sprintf("capture:%d", part.index))
		case stCase:
			out = append(out, fmt.Sprintf("case:%d:%s", part.index, part.text))
		case stIf:
			out = append(out, fmt.Sprintf("if:%d:%q", part.index, part.text))
		case stElse:
			out = append(out, fmt.Sprintf("else:%d:%q", part.index, part.text))
		case stConditional:
			out = append(out, fmt.Sprintf("conditional:%d:%q:%q", part.index, part.text, part.otherwise))
		}
	}
	return "[" + strings.Join(out, ";") + "]"
}

func TestSnippetTemplateSyntaxGoldens(t *testing.T) {
	cases := []struct{ source, want string }{
		{"", ""},
		{"é 😀", `text:"é 😀"`},
		{"literal } $$ $- $", `text:"literal } $$ $- $"`},
		{`\$1 \} \\ \n \t \x`, `text:"$1 } \\ \\n \\t \\x"`},
		{"$01 ${002} $2147483647 $0", `stop:1;text:" ";stop:2;text:" ";stop:2147483647;text:" ";stop:0`},
		{"${0:final} $0", `field:0[text:"final"];text:" ";stop:0`},
		{"$1 ${1:default} $1", `stop:1;text:" ";field:1[text:"default"];text:" ";stop:1`},
		{"${1:$1}", "field:1[stop:1]"}, // a semantic cycle, intentionally still syntax
		{"${1:outer ${2:inner} $2}", `field:1[text:"outer ";field:2[text:"inner"];text:" ";stop:2]`},
		{"${1:}${VAR:}", "field:1[];var:VAR[]"},
		{"$VAR $_x1 ${VAR2} ${VAR:before $1}", `var:VAR;text:" ";var:_x1;text:" ";var:VAR2;text:" ";var:VAR[text:"before ";stop:1]`},
		{`${1|one,two\,three,pipe\|,\\,$x,}|}`, `choice:1["one" "two,three" "pipe|" "\\" "$x" "}"]`},
		{"${1||}${2|,|}", `choice:1[""];choice:2["" ""]`},
		{`${1|\$x,\},\n|}`, `choice:1["\\$x" "\\}" "\\n"]`},
		{"${1///}", `transform:1 regex="" format=[] flags=""`},
		{"${VAR/a/$0-$01-${2}/gimSY}", `transform:VAR regex="a" format=[capture:0;text:"-";capture:1;text:"-";capture:2] flags="gimSY"`},
		{`${1/a\/b\d/\/$1\$2\}\\\q/}`, `transform:1 regex="a/b\\d" format=[text:"/";capture:1;text:"$2}\\\\q"] flags=""`},
		{"${1/x/}/}", `transform:1 regex="x" format=[text:"}"] flags=""`},
		{"${VAR/[/x/qq}", `transform:VAR regex="[" format=[text:"x"] flags="qq"`}, // regex/flag validation is later
		{"${1/a/${1:+yes}-${2:-no}-${3:default}-${4:?if:else}/}",
			`transform:1 regex="a" format=[if:1:"yes";text:"-";else:2:"no";text:"-";else:3:"default";text:"-";conditional:4:"if":"else"] flags=""`},
		{`${1/a/${1:?a\:b:c:d}/}`, `transform:1 regex="a" format=[conditional:1:"a:b":"c:d"] flags=""`},
		{`${1/a/${1:+\}\/$foo}-${2:?:}/}`, `transform:1 regex="a" format=[if:1:"}/$foo";text:"-";conditional:2:"":""] flags=""`},
	}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			parsed, err := ParseSnippetTemplate(tc.source)
			if err != nil {
				t.Fatal(err)
			}
			if got := stSummary(parsed.nodes); got != tc.want {
				t.Fatalf("tree:\n got %s\nwant %s", got, tc.want)
			}
			if parsed.Source() != tc.source {
				t.Fatal("source changed")
			}
		})
	}
}

func TestSnippetTemplateCaseFormats(t *testing.T) {
	for _, name := range []string{"upcase", "downcase", "capitalize", "camelcase", "pascalcase", "snakecase", "kebabcase"} {
		source := "${1/(.*)/${1:/" + name + "}/}"
		parsed, err := ParseSnippetTemplate(source)
		if err != nil {
			t.Fatal(name, err)
		}
		part := parsed.nodes[0].transform.format[0]
		if part.kind != stCase || part.text != name || part.index != 1 {
			t.Fatal(name, part)
		}
	}
}

func TestSnippetTemplateMalformedReturnsNoPartialTree(t *testing.T) {
	for _, source := range []string{
		"${", "${}", "${1", "${NAME", "${1:unclosed", "${1:ok ${2}", "${1?}", "${A!}",
		"${-1}", "$2147483648", "${999999999999999999999999}", "${1/x/$999999999999999999999/}",
		"${NAME|a,b|}", "${1|a}", "${1|a|x}", "${1|a,b|", "${1/a", "${1/a/b", "${1/a/b/g", "${1/a/b/1}",
		"${1/a/${}/}", "${1/a/${VAR}/}", "${1/a/${1:/unknown}/}", "${1/a/${1:/upcase!}/}",
		"${1/a/${1:?yes}/}", "${1/a/${1:?a:b/}", "${1/a/${1:+}/", "${1/a/${1!}/}", "${1/a/${1:/}/}",
		"a\xff", "\xed\xa0\x80", "\xf4\x90\x80\x80", "\xc0\x80",
	} {
		t.Run(strconv.Quote(source), func(t *testing.T) {
			parsed, err := ParseSnippetTemplate(source)
			if parsed != nil || err == nil {
				t.Fatalf("partial/valid tree=%+v, err=%v", parsed, err)
			}
			var syntax *SnippetSyntaxError
			if !errors.As(err, &syntax) || syntax.Offset < 0 || syntax.Offset > len(source) {
				t.Fatal("invalid error position", err)
			}
			if len(err.Error()) > 160 {
				t.Fatal("unbounded diagnostic", err)
			}
		})
	}
}

func TestSnippetTemplateByteSpans(t *testing.T) {
	source := "é${1:😀$2}z${V/a/${1:/upcase}/g}"
	parsed, err := ParseSnippetTemplate(source)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.nodes[0]; got.from != 0 || got.to != 2 || got.text != "é" {
		t.Fatal(got)
	}
	field := parsed.nodes[1]
	if field.from != 2 || field.to != 13 || field.children[0].from != 6 || field.children[0].to != 10 ||
		field.children[1].from != 10 || field.children[1].to != 12 {
		t.Fatal("nested byte spans", field, field.children)
	}
	transform := parsed.nodes[3]
	part := transform.transform.format[0]
	if source[transform.from:transform.to] != "${V/a/${1:/upcase}/g}" || source[part.from:part.to] != "${1:/upcase}" {
		t.Fatal("transform spans", transform, part)
	}
}

func TestSnippetTemplateBounds(t *testing.T) {
	check := func(source string, good bool) {
		t.Helper()
		parsed, err := ParseSnippetTemplate(source)
		if (err == nil) != good || (parsed != nil) != good {
			t.Fatalf("source len %d, good %v: %v", len(source), good, err)
		}
	}
	check(strings.Repeat("x", MaxSnippetBody), true)
	check(strings.Repeat("x", MaxSnippetBody+1), false)
	check(strings.Repeat("${1:", MaxSnippetDepth)+strings.Repeat("}", MaxSnippetDepth), true)
	check(strings.Repeat("${1:", MaxSnippetDepth+1)+strings.Repeat("}", MaxSnippetDepth+1), false)
	// Braced transform captures use the same structural depth budget as defaults.
	check(strings.Repeat("${1:", MaxSnippetDepth-2)+"${V/a/${1}/}"+strings.Repeat("}", MaxSnippetDepth-2), true)
	check(strings.Repeat("${1:", MaxSnippetDepth-1)+"${V/a/${1}/}"+strings.Repeat("}", MaxSnippetDepth-1), false)
	check("${1|"+strings.Repeat(",", MaxSnippetChoiceOptions-1)+"|}", true)
	check("${1|"+strings.Repeat(",", MaxSnippetChoiceOptions)+"|}", false)
	check(strings.Repeat("${1}", MaxSnippetTemplateNodes), true)
	check(strings.Repeat("${1}", MaxSnippetTemplateNodes+1), false)
	// The transform itself counts, and its capture format nodes share the global budget.
	check("${1/a/"+strings.Repeat("${1}", MaxSnippetTemplateNodes-1)+"/}", true)
	check("${1/a/"+strings.Repeat("${1}", MaxSnippetTemplateNodes)+"/}", false)
	// Literal nodes cannot bypass the same node bound.
	check(strings.Repeat("x${1}", MaxSnippetTemplateNodes/2), true)
	check(strings.Repeat("x${1}", MaxSnippetTemplateNodes/2)+"x", false)
	longEscapes := strings.Repeat(`\$`, MaxSnippetBody/2)
	parsed, err := ParseSnippetTemplate(longEscapes)
	if err != nil || len(parsed.nodes) != 1 || len(parsed.nodes[0].text) != MaxSnippetBody/2 {
		t.Fatalf("long escapes: %v", err)
	}
	check("$"+strings.Repeat("0", MaxSnippetBody-1), true)
}

func TestSnippetTemplateParallelParsing(t *testing.T) {
	source := "${1:hello ${2|a,b|}} $1 ${V/([a-z]+)/${1:/upcase}/gi}"
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 50 {
				parsed, err := ParseSnippetTemplate(source)
				if err != nil || parsed.Source() != source || len(parsed.nodes) != 5 {
					t.Errorf("parallel parse: %+v %v", parsed, err)
					return
				}
			}
		})
	}
	wg.Wait()
}

func FuzzSnippetTemplate(f *testing.F) {
	for _, s := range []string{"", "$1", "${1:x $2} $1", `${1|a\,b,c|}`, "${V/a/${1:?b:c}/gi}",
		`${1/a\/b/\/$1/}`, "${", "${1/a/${}/}", "\xff", "é${1:😀}"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, source string) {
		parsed, err := ParseSnippetTemplate(source)
		if err != nil {
			var syntax *SnippetSyntaxError
			if parsed != nil || !errors.As(err, &syntax) || syntax.Offset < 0 || syntax.Offset > len(source) {
				t.Fatal("invalid error result", parsed, err)
			}
			return
		}
		if parsed == nil || parsed.Source() != source || !utf8.ValidString(source) || len(source) > MaxSnippetBody {
			t.Fatal("invalid successful result")
		}
		count := 0
		var walk func([]stNode, int, int)
		walk = func(nodes []stNode, from, to int) {
			for _, node := range nodes {
				count++
				if node.from < from || node.to > to || node.from >= node.to ||
					!utf8.ValidString(node.text) || node.index < 0 || node.index > 2147483647 {
					t.Fatal("invalid node", node)
				}
				walk(node.children, node.from, node.to)
				for _, option := range node.choices {
					if !utf8.ValidString(option) {
						t.Fatal("invalid choice text")
					}
				}
				if tr := node.transform; tr != nil {
					if !utf8.ValidString(tr.regex) {
						t.Fatal("invalid regex text")
					}
					for _, part := range tr.format {
						count++
						if part.from < node.from || part.to > node.to || part.from >= part.to ||
							!utf8.ValidString(part.text) || !utf8.ValidString(part.otherwise) {
							t.Fatal("invalid format", part)
						}
					}
				}
				from = node.to
			}
		}
		walk(parsed.nodes, 0, len(source))
		if count > MaxSnippetTemplateNodes {
			t.Fatal("node bound escaped", count)
		}
	})
}
