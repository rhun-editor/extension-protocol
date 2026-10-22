package main

import (
	"os"
	"path/filepath"
	"strings"
)

// isData says whether a manifest is a data extension (rhun 0.18.1, v1.1.md "Manifest: data
// extensions"): no run, run.TARGET or runtime, no [commands] or [provides] (only a program answers
// those), no pack and no debuggers. Its themes, grammars, icon themes and [language.NAME] keys load
// and it never starts.
func isData(m *manifest) bool {
	return len(m.run) == 0 && m.runtime == "" && m.script == "" && len(m.commands) == 0 &&
		len(m.provides) == 0 && m.pack == "" && len(m.debuggers) == 0
}

// dataEntries counts what a data extension brings: themes/*.theme, syntax/*.syn and
// icon-themes/*.icons files, and its manifest's [language.NAME] and [keys] sections.
func dataEntries(dir string) int {
	n := 0
	for _, glob := range []string{"themes/*.theme", "syntax/*.syn", "icon-themes/*.icons"} {
		files, _ := filepath.Glob(filepath.Join(dir, filepath.FromSlash(glob)))
		n += len(files)
	}
	data, err := os.ReadFile(filepath.Join(dir, "extension.ini"))
	if err != nil {
		return n
	}
	secs := map[string]bool{}
	for _, l := range readINI(data) {
		if strings.HasPrefix(l.sec, "language.") || l.sec == "keys" {
			secs[l.sec] = true
		}
	}
	return n + len(secs)
}
