package main

// Feature debug: the [debugger.TYPE] sections of a manifest (protocol 1.1 "debug").

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var debugAttrTypes = map[string]bool{"string": true, "path": true, "int": true, "bool": true, "list": true, "map": true}

// debuggerProblems lists what is wrong with the manifest's debuggers: types, keys, values, caps,
// files inside the folder, and [needs] debug = yes.
func debuggerProblems(dir string, m *manifest) []string {
	var out []string
	if len(m.debuggers) == 0 {
		return nil
	}
	if m.needs["debug"] != "yes" {
		out = append(out, "[debugger.*] sections need debug = yes under [needs] (rhun does not offer them otherwise)")
	}
	if len(m.debuggers) > 8 {
		out = append(out, fmt.Sprintf("%d debuggers (8 at most)", len(m.debuggers)))
	}
	typeRe := regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	for _, t := range m.debuggers {
		d := m.debugger[t]
		where := "[debugger." + t + "]"
		if !typeRe.MatchString(t) {
			out = append(out, fmt.Sprintf("%s: the type is not [a-z0-9-]{1,32}", where))
		}
		if strings.TrimSpace(d["program"]) == "" {
			out = append(out, where+": no program")
		}
		attrs := 0
		for k, v := range d {
			base, _, _ := strings.Cut(k, ".")
			switch base {
			case "title", "languages", "paths", "install", "templates":
			case "program", "args":
				if base == "args" && len(v) > 4096 {
					out = append(out, fmt.Sprintf("%s: args is %d bytes (4096 at most)", where, len(v)))
				}
				if base == "program" {
					for _, name := range strings.Fields(v) {
						if strings.Contains(name, "/") && !strings.HasPrefix(name, "~") && !strings.HasPrefix(name, "$") &&
							!filepath.IsAbs(name) {
							if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
								out = append(out, fmt.Sprintf("%s: %s = %s is not in the extension's folder", where, k, name))
							}
						}
					}
				}
			case "transport":
				if v != "stdio" && v != "tcp" {
					out = append(out, fmt.Sprintf("%s: transport = %s is not stdio or tcp", where, v))
				}
				if v == "tcp" && !strings.Contains(d["args"]+d["program"], "${port}") {
					out = append(out, where+": a tcp debugger's args name ${port} (the port rhun picks)")
				}
			case "requests":
				for _, r := range strings.Fields(v) {
					if r != "launch" && r != "attach" {
						out = append(out, fmt.Sprintf("%s: requests lists %s (launch or attach)", where, r))
					}
				}
			case "resolve":
				if v != "yes" && v != "no" {
					out = append(out, fmt.Sprintf("%s: resolve = %s is not yes or no", where, v))
				}
			case "attr":
				attrs++
				w := strings.Fields(v)
				if len(w) == 0 || (!debugAttrTypes[w[0]] && w[0] != "choice") || (w[0] == "choice" && len(w) < 2) {
					out = append(out, fmt.Sprintf("%s: %s = %s is not string, path, int, bool, list, map or choice A B ...", where, k, v))
				}
			default:
				out = append(out, fmt.Sprintf("%s: %s is not a debugger key", where, k))
			}
		}
		if attrs > 128 {
			out = append(out, fmt.Sprintf("%s: %d attr.* (128 at most)", where, attrs))
		}
		for _, tpl := range strings.Fields(d["templates"]) {
			out = append(out, templateProblems(dir, where, t, tpl)...)
		}
	}
	return out
}

// templateProblems checks a launch.ini fragment: in the folder, 64 KB at most, sections whose
// type is this debugger's.
func templateProblems(dir, where, typ, tpl string) []string {
	data, err := os.ReadFile(filepath.Join(dir, tpl))
	if err != nil {
		return []string{fmt.Sprintf("%s: template %s: %v", where, tpl, err)}
	}
	if len(data) > 64<<10 {
		return []string{fmt.Sprintf("%s: template %s is over 64 KB", where, tpl)}
	}
	var out []string
	section, sections := "", 0
	types := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			sections++
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || section == "" {
			out = append(out, fmt.Sprintf("%s: template %s: %q is not KEY = VALUE inside a section", where, tpl, line))
			continue
		}
		if strings.TrimSpace(k) == "type" {
			types[section] = strings.TrimSpace(v)
		}
	}
	if sections == 0 {
		out = append(out, fmt.Sprintf("%s: template %s has no sections", where, tpl))
	}
	for s, t := range types {
		if t != typ {
			out = append(out, fmt.Sprintf("%s: template %s: [%s] has type = %s, not %s", where, tpl, s, t, typ))
		}
	}
	return out
}
