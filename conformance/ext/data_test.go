package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsData(t *testing.T) {
	data := func(f func(m *manifest)) bool {
		m := &manifest{run: map[string]string{}, provides: map[string]string{}}
		f(m)
		return isData(m)
	}
	if !data(func(m *manifest) {}) {
		t.Error("a manifest without a program is a data extension")
	}
	for why, f := range map[string]func(m *manifest){
		"run.TARGET":        func(m *manifest) { m.run["linux-x86_64"] = "bin/x" },
		"run.wasm32-wasip1": func(m *manifest) { m.run["wasm32-wasip1"] = "x.wasm" },
		"runtime":           func(m *manifest) { m.runtime = "python3" },
		"run":               func(m *manifest) { m.script = "main.py" },
		"[commands]":        func(m *manifest) { m.commands = []string{"say"} },
		"[provides]":        func(m *manifest) { m.provides["complete"] = "*.go" },
		"pack":              func(m *manifest) { m.pack = "a/b" },
		"debuggers":         func(m *manifest) { m.debuggers = []string{"py"} },
	} {
		if data(f) {
			t.Errorf("with %s it is not a data extension", why)
		}
	}
}

func TestDataEntries(t *testing.T) {
	dir := t.TempDir()
	if n := dataEntries(dir); n != 0 {
		t.Fatalf("an empty folder: %d entries", n)
	}
	for _, p := range []string{"themes/a.theme", "themes/b.theme", "syntax/x.syn", "icon-themes/i.icons", "themes/notes.txt"} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte("name = x\n"), 0o644)
	}
	os.WriteFile(filepath.Join(dir, "extension.ini"), []byte("name = x\n[language.Lua]\ncontinue = --\nfold = indent\n[keys]\nctrl+k = save\n"), 0o644)
	if n := dataEntries(dir); n != 6 {
		t.Errorf("4 data files and 2 sections: %d entries", n)
	}
}
