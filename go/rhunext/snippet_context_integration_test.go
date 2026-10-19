package rhunext

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// This optional bridge runs the real Go request helper inside an extension
// process, rather than reconstructing its protocol bytes in a fixture peer.
func TestSnippetSnapshotNativeBridge(t *testing.T) {
	native := os.Getenv("RHUN_TEST_EXE")
	if native == "" {
		t.Skip("set RHUN_TEST_EXE for native snippet context integration")
	}
	native, err := filepath.Abs(native)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	folder := filepath.Join(dir, "extensions", "sdk-tests", "snapshot")
	config := filepath.Join(dir, "config", "rhun")
	for _, path := range []string{project, folder, config} {
		if err = os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	note := filepath.Join(project, "a.txt")
	write(note, "  name\n")
	transformNote := filepath.Join(project, "transform.txt")
	write(transformNote, "")
	modifiersNote := filepath.Join(project, "modifiers.txt")
	write(modifiersNote, "")
	legacyNote := filepath.Join(project, "legacy.txt")
	write(legacyNote, "")
	selectionNote := filepath.Join(project, "selection.txt")
	write(selectionNote, "  a\n b\n")
	formattedNote := filepath.Join(project, "b.txt")
	write(formattedNote, "  old\r\n")
	write(filepath.Join(config, "config"), "[ui]\nsidebar = false\nagents_panel = false\n[files]\nrestore_session = false\nrestore_project = false\n[updates]\ncheck = false\n[git]\nenabled = false\n")
	write(filepath.Join(folder, "extension.ini"), "name = snapshot\nowner = sdk-tests\ntitle = Snapshot\nversion = 1\nprotocol = 1\nrun.macos-arm64 = peer\nrun.linux-x86_64 = peer\nrun.windows-x64 = peer.exe\non = command\n[commands]\ninsert = Insert Snapshot\nstale = Stale Snapshot\nvariants = Insert Initial Variants\nformat = Insert Formatted Template\ntransform = Insert Compiled Transform\nselection = Transform Selected Text\nlegacy = Insert Legacy Transform\nmodifiers = Insert Scoped Modifiers\n[needs]\ndocuments = write\nproject = read\n[subscribe]\nopened = *\nedits = *\n[provides]\ncomplete = *.txt\n")
	source := filepath.Join(dir, "peer.go")
	write(source, `package main
import("encoding/json";"errors";"os";"path/filepath";"strconv";"strings";rx "github.com/rhun-editor/extension-protocol/go/rhunext")
type peer struct{v map[rx.Doc]int64;project string}
func must(err error){if err!=nil{panic(err)}}
func(p *peer)Ready(e *rx.Ext,project string){p.project=project}
func(p *peer)Event(e *rx.Ext,ev rx.Event){if ev.Name=="opened"||ev.Name=="resync"||ev.Name=="edits"{id,_:=strconv.ParseInt(ev.Args[0],10,64);v,_:=strconv.ParseInt(ev.Args[1],10,64);p.v[rx.Doc(id)]=v}}
func transformProgram() rx.SnippetProgram {
 program,err:=rx.PrepareSnippetTemplate("${1:é} ${1/\\k<字>(?:(?<字>[\\q{é|É|aa}]+)|(?<字>bb))/${1:/upcase}/v} ${0:${NAME/(?:(?<name>[\\q{END}])|(?<name>else))/${1:/downcase}/v}}",rx.SnippetContext{Variables:map[string]string{"NAME":"END"}});must(err)
 if program.Snippet.Text!="é é end"||len(program.Transforms)!=1||program.Transforms[0].Field!=2 {panic("wrong source-template binding")}
 regex,format:=program.Transforms[0].Transform.Programs();compiled,err:=rx.NewCompiledSnippetTransform(regex,format);must(err)
 for i:=range regex {regex[i]=0};for i:=range format {format[i]=0}
 program.Transforms[0].Transform=compiled
 return program
}
func(p *peer)Complete(e *rx.Ext,r rx.Request) rx.CompletionList {return rx.CompletionList{Items:[]rx.CompletionItem{(rx.CompletionItem{Label:"transform",Range:rx.Range{Start:rx.Pos{Line:1,Col:1},End:rx.Pos{Line:1,Col:1}}}).WithSnippetProgram(transformProgram())}}}
func(p *peer)Run(e *rx.Ext,name string,doc rx.Doc){
 if strings.HasSuffix(name,".modifiers") {
  program,err:=rx.PrepareSnippetTemplate("${1:aA} ${1/(?im:^(a)\\1$)/${0:/upcase}/} ${NAME/(?im:^(a)\\1$)/${0:/upcase}/}",rx.SnippetContext{Variables:map[string]string{"NAME":"aA"}});must(err)
  if program.Snippet.Text!="aA aA AA"||len(program.Transforms)!=1 {panic("wrong scoped source template")}
  _,err=e.InsertSnippetProgram(doc,p.v[doc],rx.Range{Start:rx.Pos{Line:1,Col:1},End:rx.Pos{Line:1,Col:1}},program);must(err);return
 }
 if strings.HasSuffix(name,".legacy") {
  program,err:=rx.PrepareSnippetTemplate("${1:aa} ${1/(\\141+)/${1:/upcase}/} ${NAME/(\\xZZ)/${1:/upcase}/}",rx.SnippetContext{Variables:map[string]string{"NAME":"xZZ"}});must(err)
  if program.Snippet.Text!="aa aa XZZ"||len(program.Transforms)!=1 {panic("wrong legacy source template")}
  _,err=e.InsertSnippetProgram(doc,p.v[doc],rx.Range{Start:rx.Pos{Line:1,Col:1},End:rx.Pos{Line:1,Col:1}},program);must(err);return
 }
 if strings.HasSuffix(name,".selection") {
  span:=rx.Range{Start:rx.Pos{Line:1,Col:3},End:rx.Pos{Line:3,Col:1}}
  snapshot,err:=e.SnippetSnapshot(doc,p.v[doc],span);must(err)
  program,err:=rx.PrepareSnippetTemplateWithSnapshot("\t${SELECTION/(.*)/${1:/upcase}/}",snapshot,rx.SnippetVariableOptions{});must(err)
  if program.Snippet.Text!="    A\n   b\n  "||len(program.Transforms)!=0 {panic("wrong initial selected transform")}
  _,err=e.InsertSnippetProgram(doc,snapshot.Version,span,program);must(err);return
 }
 if strings.HasSuffix(name,".transform") {
  _,err:=e.InsertSnippetProgram(doc,p.v[doc],rx.Range{Start:rx.Pos{Line:1,Col:1},End:rx.Pos{Line:1,Col:1}},transformProgram());must(err);return
 }
 if strings.HasSuffix(name,".format") {
  span:=rx.Range{Start:rx.Pos{Line:1,Col:3},End:rx.Pos{Line:1,Col:6}}
  s,err:=e.SnippetSnapshot(doc,p.v[doc],span);must(err);if !s.CRLF{panic("lost CRLF context")}
  program,err:=rx.PrepareSnippetTemplateWithSnapshot("${1:\tx}\n$1$0",s,rx.SnippetVariableOptions{});must(err)
  if len(program.InitialVariants)!=1||program.InitialVariants[0]!=2{panic("missing formatted variant")}
  _,err=e.InsertSnippetProgram(doc,s.Version,span,program);must(err);return
 }
 if strings.HasSuffix(name,".variants") {
  program:=rx.SnippetProgram{Snippet:rx.PreparedSnippet{Text:"a ABC",Fields:[]rx.Field{{Group:1,Start:0,End:1},{Group:1,Start:2,End:5}}},InitialVariants:[]int{2}}
  _,err:=e.InsertSnippetProgram(doc,p.v[doc],rx.Range{Start:rx.Pos{Line:1,Col:1},End:rx.Pos{Line:1,Col:1}},program);must(err);return
 }
 span:=rx.Range{Start:rx.Pos{Line:1,Col:3},End:rx.Pos{Line:1,Col:7}}
 s,err:=e.SnippetSnapshot(doc,p.v[doc],span);must(err)
 if strings.HasSuffix(name,".insert"){
  if s.SelectedText!="name"||s.CurrentLine!="  name"||s.Indent!="  "||s.Workspace!=p.project||s.TabWidth!=4||!s.InsertSpaces{panic("wrong native snapshot")}
  b,err:=json.Marshal(s);must(err);must(os.WriteFile(filepath.Join(os.Getenv("RHUN_STATE"),"snapshot.json"),b,0644))
  program,err:=rx.PrepareSnippetTemplateWithSnapshot("${1:$TM_SELECTED_TEXT} $1 ${0:end}",s,rx.SnippetVariableOptions{});must(err)
  _,err=e.InsertSnippetProgram(doc,s.Version,span,program);must(err)
 }else{
  _,err=e.Replace(doc,s.Version,rx.Range{Start:span.Start,End:span.Start},[]byte("!"));must(err)
  _,err=e.InsertSnippet(doc,s.Version,span,rx.PreparedSnippet{Text:"BAD"});var stale *rx.Error
  if !errors.As(err,&stale)||stale.Code!="stale"{panic("stale snapshot insertion accepted")}
 }
}
func main(){must(rx.RunWith([]string{rx.FeatureSnippets,rx.FeatureSnippetVariants,rx.FeatureSnippetTransforms,rx.FeatureComplete},&peer{v:map[rx.Doc]int64{}}))}
`)
	_, file, _, _ := runtime.Caller(0)
	sdkroot := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	peer := filepath.Join(folder, "peer")
	if runtime.GOOS == "windows" {
		peer += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", peer, source)
	build.Dir = sdkroot
	for _, entry := range os.Environ() {
		if key, _, _ := strings.Cut(entry, "="); !strings.EqualFold(key, "GOWORK") {
			build.Env = append(build.Env, entry)
		}
	}
	build.Env = append(build.Env, "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build peer: %v\n%s", err, output)
	}
	script := filepath.Join(dir, "actions.rsc")
	write(script, strings.Join([]string{"wait 100", "open " + filepath.ToSlash(note), "cmd snapshot.insert", "wait 400", "print-state", "print-doc", "type x", "print-doc", "cmd snapshot.stale", "wait 400", "print-doc", "cmd snapshot.variants", "wait 200", "print-doc", "type z", "print-doc", "cmd undo", "print-doc", "open " + filepath.ToSlash(formattedNote), "cmd snapshot.format", "wait 200", "print-doc", "type z", "print-doc", "cmd save", "wait 100", "open " + filepath.ToSlash(transformNote), "cmd snapshot.transform", "wait 250", "key Tab", "print-doc", "cmd undo", "print-doc", "cmd redo", "print-doc", "cmd undo", "cmd undo", "cmd complete", "wait 150", "key Return", "key Tab", "print-doc", "open " + filepath.ToSlash(modifiersNote), "cmd snapshot.modifiers", "wait 250", "key Tab", "print-doc", "cmd undo", "print-doc", "cmd redo", "print-doc", "open " + filepath.ToSlash(legacyNote), "cmd snapshot.legacy", "wait 250", "key Tab", "print-doc", "cmd undo", "print-doc", "cmd redo", "print-doc", "open " + filepath.ToSlash(selectionNote), "cmd snapshot.selection", "wait 250", "print-doc", "cmd undo", "print-doc", "cmd redo", "print-doc", "quit"}, "\n")+"\n")
	cmd := exec.CommandContext(ctx, native, project, "--headless", "1000x600", "--scale", "1", "--extensions", filepath.Join(dir, "extensions"), "--script", script)
	overrides := map[string]string{"HOME": dir, "XDG_CONFIG_HOME": filepath.Join(dir, "config"), "XDG_STATE_HOME": filepath.Join(dir, "state")}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replace := overrides[strings.ToUpper(key)]; !replace {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	for key, value := range overrides {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native context bridge: %v\n%s", err, output)
	}
	saved, err := os.ReadFile(formattedNote)
	if err != nil || string(saved) != "  z\r\n  z\r\n" {
		t.Fatalf("formatted CRLF save %q: %v\n%s", saved, err, output)
	}
	for _, want := range []string{"      A\n   b\n  \n<eod>", "é É end\n<eod>", "é é end\n<eod>", "col=7 sel=4 ", "  name name end\n\n<eod>", "  x x end\n\n<eod>", "  !x x end\n\n<eod>", "a ABC  !x x end\n\n<eod>", "z z  !x x end\n\n<eod>", "      x\n  \tx\n\n<eod>", "  z\n  z\n\n<eod>"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("missing %q\n%s", want, output)
		}
	}
	// The last nine documents check scoped, legacy and selection transform undo/redo.
	// Compare their order so a no-op undo or redo cannot pass by containment.
	snapshots := strings.Split(string(output), "\n<eod>\n")
	if len(snapshots) < 10 {
		t.Fatalf("missing transform snapshots\n%s", output)
	}
	last := snapshots[len(snapshots)-10 : len(snapshots)-1]
	wantDocuments := []string{"aA AA AA", "aA aA AA", "aA AA AA", "aa AA XZZ", "aa aa XZZ", "aa AA XZZ", "      A\n   b\n  ", "  a\n b\n", "      A\n   b\n  "}
	for i, want := range wantDocuments {
		got := last[i]
		// Earlier command output can precede the first transform snapshot.
		if i == 0 {
			if !strings.HasSuffix(got, want) {
				t.Fatalf("transform snapshot%d %q want suffix %q", i, got, want)
			}
		} else if got != want {
			t.Fatalf("transform snapshot%d %q want %q", i, got, want)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "state", "rhun", "extensions", "sdk-tests--snapshot", "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot SnippetSnapshot
	if err = json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Path != filepath.ToSlash(note) || snapshot.Workspace != filepath.ToSlash(project) || snapshot.SelectedText != "name" {
		t.Fatalf("%+v", snapshot)
	}
}
