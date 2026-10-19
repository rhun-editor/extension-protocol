package rhunext

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt in with RHUN_TEST_EXE=/absolute/path/to/rhun. Ordinary SDK tests do not
// require the native editor or Python. The peer transports actual compiler output.
func TestSnippetExpansionNativeBridge(t *testing.T) {
	native := os.Getenv("RHUN_TEST_EXE")
	if native == "" {
		t.Skip("set RHUN_TEST_EXE for native snippet integration")
	}
	var err error
	native, err = filepath.Abs(native)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = exec.LookPath("python3"); err != nil {
		t.Fatal("native bridge requires python3:", err)
	}
	p, err := PrepareSnippetTemplate("${1:${P:default} ${2|x,y|}} $1 ${0:end}", SnippetContext{Variables: map[string]string{"P": "ab"}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	extroot := filepath.Join(dir, "extensions")
	folder := filepath.Join(extroot, "sdk-tests", "bridge")
	config := filepath.Join(dir, "config", "rhun")
	for _, path := range []string{project, folder, config} {
		if err = os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, text string) {
		t.Helper()
		if why := os.WriteFile(path, []byte(text), 0644); why != nil {
			t.Fatal(why)
		}
	}
	note := filepath.Join(project, "a.txt")
	write(note, "")
	write(filepath.Join(config, "config"), "[ui]\nsidebar = false\nagents_panel = false\n[editor]\ncursor_blink = false\n[files]\nrestore_session = false\nrestore_project = false\n[updates]\ncheck = false\n[git]\nenabled = false\n")
	write(filepath.Join(folder, "extension.ini"), "name = bridge\nowner = sdk-tests\ntitle = Snippet Bridge\nversion = 1\nprotocol = 1\nruntime = python3\nrun = main.py\non = command\n[commands]\ninsert = Insert Template\n[needs]\ndocuments = write\nproject = read\n[subscribe]\nopened = *\nedits = *\n")
	write(filepath.Join(folder, "main.py"), `import base64, sys
record = base64.b64decode("`+base64.StdEncoding.EncodeToString(body)+`")
versions = {}
def send(line, body=b''):
    sys.stdout.buffer.write(line.encode()+b'\n'+body)
    sys.stdout.buffer.flush()
send('hello 1 snippets')
while True:
    line = sys.stdin.buffer.readline()
    if not line: break
    words = line.decode().split()
    if words[:2] == ['0', 'bye']: break
    if words[:2] == ['0', 'edits']:
        sys.stdin.buffer.read(int(words[4]))
        versions[words[2]] = int(words[3])
    elif words[:2] in (['0', 'opened'], ['0', 'resync']):
        versions[words[2]] = int(words[3])
    elif words[:2] == ['0', 'run']:
        doc = words[3]
        send(f'20 snippet {doc} {versions[doc]} 1 1 1 1 {len(record)}', record)
`)
	script := filepath.Join(dir, "actions.rsc")
	write(script, strings.Join([]string{
		"wait 100", "open " + filepath.ToSlash(note), "cmd bridge.insert", "wait 300", "print-doc",
		"key Tab", "wait 50", "print-complete", "key Down", "key Return", "type c", "print-doc",
		"cmd snippet_choose", "wait 50", "key Down", "key Tab", "print-state", "print-doc",
		"cmd undo", "print-doc", "cmd redo", "print-state", "print-doc", "type q", "print-doc", "quit",
	}, "\n")+"\n")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, native, project, "--headless", "1000x600", "--scale", "1", "--extensions", extroot, "--script", script)
	// Replace rather than duplicate host variables (Windows treats names without case).
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
		t.Fatalf("native bridge: %v\n%s", err, output)
	}
	text := string(output)
	for _, want := range []string{"ab x ab x end\n<eod>", "items=2 shown=2", "ab yc ab yc end\n<eod>", "ab y ab y end\n<eod>", "ab y ab y q\n<eod>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q\n%s", want, text)
		}
	}
	if strings.Count(text, "col=14 sel=3 ") != 2 {
		t.Fatalf("final selection/redo not restored\n%s", text)
	}
}
