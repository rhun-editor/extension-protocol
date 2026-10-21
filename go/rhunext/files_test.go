package rhunext

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
)

type filesProbe struct{ out chan string }

func (p *filesProbe) Ready(ext *Ext, project string) {
	ctx := context.Background()
	var got []string
	f, err := ext.FindFilesContext(ctx, FileQuery{Max: 10, Include: []string{"*.go"}, Exclude: []string{"vendor"}},
		func(b []FoundPath) bool {
			for _, p := range b {
				got = append(got, fmt.Sprintf("%d:%s", p.Folder, p.Path))
			}
			return true
		})
	p.out <- fmt.Sprintf("find %v %+v %v", got, f, err)
	prog, err := CompileSearchRegex(`TODO\(\w+\)`, "")
	if err != nil {
		p.out <- err.Error()
		return
	}
	var files []FoundFile
	s, err := ext.SearchTextContext(ctx, Pattern{Program: prog}, FileQuery{Max: 5}, func(b []FoundFile) bool {
		files = append(files, b...)
		return false // stop after the first batch
	})
	p.out <- fmt.Sprintf("search %+v %+v %v", files, s, err)
}
func (p *filesProbe) Run(ext *Ext, command string, doc Doc) {}
func (p *filesProbe) Event(ext *Ext, ev Event)              {}

func TestFindAndSearch(t *testing.T) {
	toExt, hostW := io.Pipe()
	hostR, fromExt := io.Pipe()
	p := &filesProbe{out: make(chan string, 4)}
	go RunOnWith(toExt, fromExt, io.Discard, []string{FeatureFiles}, p)
	br := bufio.NewReader(hostR)
	line := func() string {
		s, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSuffix(s, "\n")
	}
	body := func(n int) string {
		b := make([]byte, n)
		io.ReadFull(br, b)
		return string(b)
	}
	if l := line(); l != "hello 1 files" {
		t.Fatal(l)
	}
	io.WriteString(hostW, "0 features files\n0 ready /p\n")
	if l := line(); l != "1 find-files 0 10 0 14" {
		t.Fatal(l)
	}
	if b := body(14); b != "+*.go\n-vendor\n" {
		t.Fatalf("%q", b)
	}
	b1 := "2\n1 7\nmain.go1 9\nsrc/x.go/"
	b2 := "1\n2 4\na.go"
	io.WriteString(hostW, fmt.Sprintf("0 found 1 %d\n%s0 found 1 %d\n%sok 1 3 complete\n", len(b1), b1, len(b2), b2))
	if got := <-p.out; got != "find [1:main.go 1:src/x.go/ 2:a.go] {Count:3 Files:0 Skipped:0 Status:complete} <nil>" {
		t.Fatal(got)
	}
	l := line()
	var plen, glen int
	if _, err := fmt.Sscanf(l, "2 search-text 0 5 64 %d", &plen); err != nil {
		t.Fatal(l)
	}
	head := line()
	fmt.Sscanf(head, "%d %d", &plen, &glen)
	if prog := body(plen); !strings.HasPrefix(prog, "RNRX") || glen != 0 {
		t.Fatalf("%q %d", prog[:4], glen)
	}
	g := "1\n1 3 7 7 1\nmain.go2 4 14 1 12\n// TODO(me)!"
	io.WriteString(hostW, fmt.Sprintf("0 found 2 %d\n%s", len(g), g))
	// the probe stops after that batch: a cancel, then the run ends cancelled
	if l := line(); l != "3 cancel 2" {
		t.Fatal(l)
	}
	io.WriteString(hostW, "error 2 cancelled\nok 3\n")
	got := <-p.out
	want := "search [{Folder:1 Doc:3 Version:7 Path:main.go Matches:[{Line:2 Col:4 End:14 Pre:1 Text:// TODO(me)!}]}] {Count:0 Files:0 Skipped:0 Status:} rhun: cancelled: "
	if got != want {
		t.Fatalf("%s\n%s", got, want)
	}
}

func TestParseFoundRejectsBadBatches(t *testing.T) {
	for _, b := range []string{"", "1\n", "1\n1 9\nab", "x\n", "1\n1 2\nab!"} {
		if _, err := ParseFoundPaths([]byte(b)); err == nil {
			t.Errorf("%q accepted", b)
		}
	}
}

type settingProbe struct{ out chan string }

func (p *settingProbe) Ready(ext *Ext, project string) {
	v, err := ext.SettingGetContext(context.Background(), "style", 1, "Visual Basic")
	p.out <- fmt.Sprintf("%q %v", v, err)
}
func (p *settingProbe) Run(ext *Ext, command string, doc Doc) {}
func (p *settingProbe) Event(ext *Ext, ev Event) {
	if k, ok := ParseSettingChanged(ev); ok {
		p.out <- "changed " + k
	}
}

func TestSettingGet(t *testing.T) {
	toExt, hostW := io.Pipe()
	hostR, fromExt := io.Pipe()
	p := &settingProbe{out: make(chan string, 4)}
	go RunOnWith(toExt, fromExt, io.Discard, []string{FeatureScopedSettings}, p)
	br := bufio.NewReader(hostR)
	line := func() string {
		s, _ := br.ReadString('\n')
		return strings.TrimSuffix(s, "\n")
	}
	if l := line(); l != "hello 1 scoped-settings" {
		t.Fatal(l)
	}
	io.WriteString(hostW, "0 features scoped-settings\n0 ready /p\n")
	if l := line(); l != "1 setting-get style 1 Visual_Basic" {
		t.Fatal(l)
	}
	io.WriteString(hostW, "ok 1 9\ngoimports0 setting-changed style\n")
	if got := <-p.out; got != `"goimports" <nil>` {
		t.Fatal(got)
	}
	if got := <-p.out; got != "changed style" {
		t.Fatal(got)
	}
}
