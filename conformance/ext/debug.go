package main

// Feature debug: the [debugger.TYPE] sections of a manifest (protocol 1.1 "debug").

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
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

// debugStart answers debug-start NAME_LEN LEN + NAME + a configuration: a session of one of the
// extension's own debuggers (the checker pretends it started, and ends it at once)
func (h *host) debugStart(id int64, args []string, msg message, reply func(string), rerr func(string, string)) {
	if !h.features["debug"] {
		h.fail("debug", "debug-start without debug agreed after hello")
		rerr("unknown", "no such request")
		return
	}
	if h.m.needs["debug"] != "yes" {
		h.fail("needs", "debug-start needs debug = yes in [needs]")
		rerr("needs", "debug=yes")
		return
	}
	bad := func(why string) {
		h.fail("request grammar", "debug-start: "+why)
		rerr("syntax", "cannot parse the line")
	}
	if len(args) != 2 || args[1] != strconv.Itoa(len(msg.body)) {
		bad("NAME_LEN LEN, with LEN the body's length")
		return
	}
	nl, err := strconv.Atoi(args[0])
	if err != nil || nl < 1 || nl > 256 || nl > len(msg.body) {
		bad("NAME_LEN is 1 to 256 and within the body")
		return
	}
	cfg, _, err := rhunext.ParseDebugConfig(msg.body[nl:])
	if err != nil {
		bad(err.Error())
		return
	}
	if h.m.debugger[cfg.String("type")] == nil {
		h.fail("debug", fmt.Sprintf("debug-start of type %q, not one of the manifest's debuggers", cfg.String("type")))
		rerr("refused", "the configuration's type is not one of your debuggers")
		return
	}
	h.debugSessions++
	reply(fmt.Sprintf("ok %d %d", id, h.debugSessions))
	h.write(fmt.Sprintf("0 debug-ended %d 0\n", h.debugSessions))
	h.ok("starts a debug session of its own debugger " + cfg.String("type"))
}

// debugStop answers debug-stop SESSION: one of the sessions debug-start "started" (each ended at
// once, and stopping a session already over is ok)
func (h *host) debugStop(id int64, args []string, reply func(string), rerr func(string, string)) {
	if !h.features["debug"] {
		h.fail("debug", "debug-stop without debug agreed after hello")
		rerr("unknown", "no such request")
		return
	}
	if h.m.needs["debug"] != "yes" {
		h.fail("needs", "debug-stop needs debug = yes in [needs]")
		rerr("needs", "debug=yes")
		return
	}
	var s int64
	var err error
	if len(args) == 1 {
		s, err = strconv.ParseInt(args[0], 10, 64)
	}
	if len(args) != 1 || err != nil || s < 0 || strings.HasPrefix(args[0], "+") {
		h.fail("request grammar", "debug-stop: SESSION, a number")
		rerr("syntax", "cannot parse the line")
		return
	}
	if s < 1 || s > h.debugSessions {
		h.fail("debug", fmt.Sprintf("debug-stop %d: not a session debug-start gave it", s))
		rerr("refused", "not a session you started")
		return
	}
	reply(fmt.Sprintf("ok %d", id))
	h.ok("stops a debug session it started")
}

// checkDebug: debuggers with resolve = yes answer debug-resolve for each of their templates (and a
// minimal configuration) within 5 s, with a configuration that parses, and keep answering after a
// cancel
func (h *host) checkDebug() {
	if h.features["debug"] && len(h.m.debuggers) == 0 {
		h.warn("debug", "debug agreed after hello but the manifest declares no [debugger.TYPE]")
	}
	for _, t := range h.m.debuggers {
		d := h.m.debugger[t]
		if d["resolve"] != "yes" {
			continue
		}
		if !h.features["debug"] {
			h.fail("debug", fmt.Sprintf("[debugger.%s] resolve = yes without saying debug after hello 1: every launch fails", t))
			continue
		}
		active := h.docs[h.active]
		base := filepath.Base(active.path)
		vars := strings.NewReplacer("${project}", h.project, "${projectName}", filepath.Base(h.project),
			"${file}", active.path, "${fileDir}", filepath.Dir(active.path), "${fileName}", base,
			"${fileStem}", strings.TrimSuffix(base, filepath.Ext(base)), "${fileExt}", filepath.Ext(base),
			"${relativeFile}", base, "${line}", "1", "${column}", "1", "${selection}", "", "$$", "$")
		configs := []rhunext.DebugConfig{}
		for _, tpl := range strings.Fields(d["templates"]) {
			configs = append(configs, templateConfigs(filepath.Join(h.dir, tpl), t, d, vars)...)
		}
		if len(configs) == 0 {
			var c rhunext.DebugConfig
			c.Set("type", rhunext.DebugStr(t))
			c.Set("request", rhunext.DebugStr("launch"))
			c.Set("name", rhunext.DebugStr("Checker"))
			configs = append(configs, c)
		}
		answered := 0
		for i, c := range configs {
			b, err := c.Encode()
			if err != nil {
				h.fail("debug", fmt.Sprintf("[debugger.%s] template configuration %d: %v", t, i+1, err))
				continue
			}
			b = append(b, fmt.Sprintf("context %d 1 1 %d\n%s", active.id, len(active.path), active.path)...)
			m, ok := h.askBody(fmt.Sprintf("debug-resolve %s launch 0", t), b, 5*time.Second)
			f := strings.Fields(m.line)
			switch {
			case !ok:
				h.fail("debug", fmt.Sprintf("[debugger.%s] no answer to debug-resolve within 5 s (rhun waits 60 s, the user sees Preparing...)", t))
			case len(f) > 1 && f[0] == "error":
				if len(f) < 3 || (f[2] != "cancelled" && f[2] != "refused") {
					h.fail("debug", "debug-resolve answered "+m.line+" (cancelled or refused TEXT)")
				} else {
					h.warn("debug", fmt.Sprintf("[debugger.%s] debug-resolve of %q answered %s", t, c.String("name"), m.line))
					answered++
				}
			default:
				if len(m.body) > 0 {
					rc, _, err := rhunext.ParseDebugConfig(m.body)
					if err != nil {
						h.fail("debug", "debug-resolve's configuration does not parse: "+err.Error())
						continue
					}
					if h.m.debugger[rc.String("type")] == nil {
						h.fail("debug", fmt.Sprintf("debug-resolve answered type %q, not one of the manifest's debuggers", rc.String("type")))
						continue
					}
				}
				answered++
			}
		}
		if answered == len(configs) {
			h.ok(fmt.Sprintf("[debugger.%s] answers debug-resolve (%d configurations)", t, answered))
		}
		// withdrawn at once: it must keep answering
		b, _ := configs[0].Encode()
		h.hostSeq++
		h.write(fmt.Sprintf("%d debug-resolve %s launch 0 %d\n%s0 cancel %d\n", h.hostSeq, t, len(b), b, h.hostSeq))
		h.hostReqs[h.hostSeq] = "debug-resolve"
		if _, ok := h.askBody(fmt.Sprintf("debug-resolve %s launch 0", t), b, 5*time.Second); !ok {
			h.fail("debug", "no answer to debug-resolve after a withdrawn one (0 cancel)")
		} else {
			h.ok("answers debug-resolve after a withdrawn one")
		}
	}
}

// templateConfigs reads a launch.ini fragment's sections as configurations, with the variables
// replaced (the checker's project and active document) and the debugger's attr.* types applied as
// rhun applies them (a value that does not fit stays a string)
func templateConfigs(path, typ string, d map[string]string, vars *strings.Replacer) []rhunext.DebugConfig {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []rhunext.DebugConfig
	var cur *rhunext.DebugConfig
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' && strings.HasSuffix(line, "]") {
			out = append(out, rhunext.DebugConfig{})
			cur = &out[len(out)-1]
			cur.Set("name", rhunext.DebugStr(line[1:len(line)-1]))
			cur.Set("request", rhunext.DebugStr("launch"))
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || cur == nil {
			continue
		}
		k, v = strings.TrimSpace(k), vars.Replace(strings.TrimSpace(v))
		switch k {
		case "before", "after", "server", "compound", "hidden":
			continue
		}
		switch t, _, _ := strings.Cut(d["attr."+k], " "); t {
		case "int":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				cur.Set(k, rhunext.DebugInt(n))
				continue
			}
		case "bool":
			if v == "yes" || v == "true" || v == "no" || v == "false" {
				cur.Set(k, rhunext.DebugBoolean(v == "yes" || v == "true"))
				continue
			}
		case "list":
			cur.SetList(k, append(cur.List(k), strings.Fields(v)...))
			continue
		}
		cur.Set(k, rhunext.DebugStr(v))
	}
	for i := range out {
		if out[i].String("type") == "" {
			out[i].Set("type", rhunext.DebugStr(typ))
		}
	}
	return out
}
