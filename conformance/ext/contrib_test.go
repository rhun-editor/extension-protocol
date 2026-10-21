package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContributionChecks(t *testing.T) {
	dir := t.TempDir()
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR\x00\x00\x00\x10\x00\x00\x00\x10\x08\x06\x00\x00\x00")
	os.MkdirAll(filepath.Join(dir, "icon-themes", "png"), 0o755)
	os.MkdirAll(filepath.Join(dir, "syntax"), 0o755)
	os.WriteFile(filepath.Join(dir, "logo.png"), png, 0o644)
	os.WriteFile(filepath.Join(dir, "icon-themes", "png", "a.png"), png, 0o644)
	os.WriteFile(filepath.Join(dir, "extension.ini"), []byte(`name = demo
[commands]
go = Go
[keys]
ctrl+k ctrl+g = go when editorFocus
x = go
y = go when view == 'alice/demo.files'
k ctrl+c = go
ctrl+alt+z = go when editorFocus ==
[menu.explorer]
a.command = go
a.when = resourceExtt == .rs
b.order = soon
[menu.nowhere]
c.command = go
[submenu.more]
title = More
d.submenu = deeper
[status]
s.text = $(play) Go
s.command = go
s.priority = 5000
s.color = pink
[context]
mode = normal
Bad = x
[icons]
logo = logo.png
missing = nope.png
[language.Lua]
continue = ---
autoclose = ( ) [
fold = sideways
bogus = 1
`), 0o644)
	os.WriteFile(filepath.Join(dir, "syntax", "toy.syn"), []byte("name = Toy\ndoc_block = /** *\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "icon-themes", "t.icons"), []byte(`title = T
fallback = maybe
[icons]
a = png/a.png muted
b = png/b.png
c = rhun:config mauve
[extensions]
rs = a
go = nothing
`), 0o644)
	h := &host{dir: dir, m: &manifest{name: "demo", commands: []string{"go"}}}
	all := captureStdout(t, h.checkContributions)
	for _, want := range []string{
		"[keys] x: a typing key needs",
		"[keys] k ctrl+c: a chord's first stroke needs ctrl, alt or super",
		"[keys] ctrl+alt+z: expected a value at byte 14",
		"[menu.explorer] a.when: unknown key resourceExtt at byte 0",
		"[menu.explorer] b.order: order is a whole number",
		"unknown menu location nowhere",
		"a submenu inside a submenu",
		"[status] s.priority: -1000 to 1000",
		"[status] s.color: fg, muted",
		"[context] Bad: a lowercase letter",
		"[icons] missing: nope.png",
		"[language.Lua] autoclose: OPEN CLOSE pairs",
		"[language.Lua] fold: brackets or indent",
		"[language.Lua] bogus: not a language key",
		"toy.syn:2 doc_block: START MIDDLE END",
		"t.icons:2: fallback is default or rhun",
		"t.icons:5: png/b.png",
		"t.icons:6: an unknown color mauve",
		`[extensions] go names no icon "nothing"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("no failure %q in:\n%s", want, all)
		}
	}
	for _, unwanted := range []string{"ctrl+k ctrl+g", "] y:", "[context] mode", "[icons] logo", "continue"} {
		if strings.Contains(all, unwanted) {
			t.Errorf("a failure for %q:\n%s", unwanted, all)
		}
	}
}

// captureStdout: what f prints (the checker reports on standard output)
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	f()
	w.Close()
	os.Stdout = old
	return <-done
}
