package main

// Feature files: find-files and search-text against the checker's project folder. The checker
// walks the folder itself (no git), applies the globs as rhun does, searches literals, and answers
// RNRX programs with no matches (it does not run them) after a warning.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type fileGlob struct {
	exclude, slash bool
	pat            string
}

// parseGlobs reads +GLOB / -GLOB lines; braces expand to their alternatives
func parseGlobs(b []byte) ([]fileGlob, string) {
	var out []fileGlob
	lines := 0
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSuffix(l, "\r")
		if l == "" {
			continue
		}
		lines++
		if lines > 64 || len(l) > 1025 {
			return nil, "toobig"
		}
		if (l[0] != '+' && l[0] != '-') || len(l) < 2 {
			return nil, "syntax"
		}
		alts, ok := expandBraces(l[1:])
		if !ok {
			return nil, "syntax"
		}
		if len(alts) > 32 {
			return nil, "toobig"
		}
		for _, a := range alts {
			out = append(out, fileGlob{exclude: l[0] == '-', slash: strings.Contains(a, "/"), pat: a})
		}
	}
	return out, ""
}

func expandBraces(g string) ([]string, bool) {
	i := strings.IndexAny(g, "{[")
	for i >= 0 && g[i] == '[' {
		j := strings.IndexByte(g[i+1:], ']')
		if j < 0 {
			return nil, false
		}
		k := strings.IndexAny(g[i+j+2:], "{[")
		if k < 0 {
			i = -1
		} else {
			i = i + j + 2 + k
		}
	}
	if i < 0 {
		if strings.ContainsAny(g, "}") {
			return nil, false
		}
		return []string{g}, true
	}
	j := strings.IndexByte(g[i:], '}')
	if j < 0 || strings.ContainsAny(g[i+1:i+j], "{") {
		return nil, false
	}
	rest, ok := expandBraces(g[i+j+1:])
	if !ok {
		return nil, false
	}
	var out []string
	for _, alt := range strings.Split(g[i+1:i+j], ",") {
		for _, r := range rest {
			out = append(out, g[:i]+alt+r)
		}
	}
	return out, true
}

// globMatch: * within a segment, ** across, ? one character, [..] one byte
func globMatch(p, s string) bool {
	for len(p) > 0 {
		switch {
		case strings.HasPrefix(p, "**"):
			q := strings.TrimLeft(p, "*")
			if q == "" {
				return true
			}
			if q[0] == '/' {
				q = q[1:]
				for t := s; ; {
					if globMatch(q, t) {
						return true
					}
					i := strings.IndexByte(t, '/')
					if i < 0 {
						return false
					}
					t = t[i+1:]
				}
			}
			for i := 0; i <= len(s); i++ {
				if globMatch(q, s[i:]) {
					return true
				}
			}
			return false
		case p[0] == '*':
			for i := 0; i <= len(s); i++ {
				if globMatch(p[1:], s[i:]) {
					return true
				}
				if i < len(s) && s[i] == '/' {
					return false
				}
			}
			return false
		case p[0] == '?':
			if s == "" || s[0] == '/' {
				return false
			}
			_, n := firstRune(s)
			p, s = p[1:], s[n:]
		case p[0] == '[':
			j := strings.IndexByte(p[2:], ']')
			if j < 0 || s == "" || s[0] == '/' {
				return false
			}
			set := p[1 : j+2]
			neg := set[0] == '!' || set[0] == '^'
			if neg {
				set = set[1:]
			}
			in := false
			for k := 0; k < len(set); k++ {
				lo, hi := set[k], set[k]
				if k+2 < len(set) && set[k+1] == '-' {
					hi = set[k+2]
					k += 2
				}
				if s[0] >= lo && s[0] <= hi {
					in = true
				}
			}
			if in == neg {
				return false
			}
			p, s = p[j+3:], s[1:]
		default:
			if s == "" {
				return p == "/**"
			}
			if p[0] != s[0] {
				return false
			}
			p, s = p[1:], s[1:]
		}
	}
	return s == ""
}

func firstRune(s string) (rune, int) {
	for i := range s {
		if i > 0 {
			return 0, i
		}
	}
	return 0, len(s)
}

func globEntry(g fileGlob, path string) bool {
	if !g.slash {
		path = path[strings.LastIndexByte(path, '/')+1:]
	}
	return globMatch(g.pat, path)
}

func globsList(globs []fileGlob, path string) bool {
	inc, hit := false, false
	for _, g := range globs {
		if !g.exclude {
			inc = true
			if globEntry(g, path) {
				hit = true
			}
		}
	}
	if inc && !hit {
		return false
	}
	for _, g := range globs {
		if !g.exclude {
			continue
		}
		for i := 0; i <= len(path); i++ {
			if (i == len(path) || path[i] == '/') && globEntry(g, path[:i]) {
				return false
			}
		}
	}
	return true
}

// filesRequest answers find-files and search-text
func (h *host) filesRequest(verb string, id int64, args []string, body []byte, reply func(string),
	rerr func(code, text string)) {
	if !h.features["files"] {
		h.fail("features", verb+" without files agreed after hello")
		rerr("unknown", "no such request")
		return
	}
	if p := h.m.needs["project"]; p != "read" && p != "write" {
		h.fail("needs", verb+" needs project=read but the manifest does not say so")
		rerr("needs", "project=read")
		return
	}
	n := make([]int64, 3)
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseInt(args[i], 10, 64)
		if err != nil || v < 0 {
			h.fail("request grammar", verb+": FOLDER MAX FLAGS are decimals")
			rerr("syntax", "cannot parse the line")
			return
		}
		n[i] = v
	}
	folder, max, flags := n[0], n[1], n[2]
	limit, allowed := int64(100000), int64(15)
	if verb == "search-text" {
		limit, allowed = 20000, 0x77
	}
	switch {
	case folder > 1:
		rerr("nofile", "no such folder")
		return
	case max == 0 || flags&^allowed != 0:
		h.fail("request grammar", verb+": MAX is at least 1, FLAGS known bits")
		rerr("syntax", "cannot parse the request")
		return
	case max > limit:
		h.fail("limits", fmt.Sprintf("%s: MAX %d (at most %d)", verb, max, limit))
		rerr("toobig", "MAX")
		return
	}
	var pattern []byte
	globBody := body
	if verb == "search-text" {
		head, rest, ok := bytes.Cut(body, []byte("\n"))
		var plen, glen int
		if _, err := fmt.Sscanf(string(head), "%d %d", &plen, &glen); !ok || err != nil || plen < 1 || plen+glen != len(rest) {
			h.fail("request grammar", "search-text's body is PATTERN_LEN GLOBS_LEN LF, the pattern, the globs")
			rerr("syntax", "cannot parse the request")
			return
		}
		pattern, globBody = rest[:plen], rest[plen:]
		if flags&64 == 0 && (len(pattern) > 4096 || bytes.IndexByte(pattern, '\n') >= 0) {
			h.fail("request grammar", "a literal is at most 4096 bytes without LF")
			rerr("syntax", "a literal")
			return
		}
	}
	globs, bad := parseGlobs(globBody)
	if bad != "" {
		h.fail("request grammar", verb+": a glob line is +GLOB or -GLOB with closed [ and {, 64 lines, 32 alternatives")
		rerr(bad, "globs")
		return
	}
	var paths []string
	filepath.WalkDir(h.project, func(p string, d os.DirEntry, err error) error {
		if err != nil || p == h.project {
			return nil
		}
		rel, _ := filepath.Rel(h.project, p)
		rel = filepath.ToSlash(rel)
		name := d.Name()
		if name == ".git" || (flags&2 != 0 && strings.HasPrefix(name, ".")) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if flags&8 != 0 && verb == "find-files" && globsList(globs, rel) {
				paths = append(paths, rel+"/")
			}
			return nil
		}
		if globsList(globs, rel) {
			paths = append(paths, rel)
		}
		return nil
	})
	sort.Strings(paths)
	var batch bytes.Buffer
	recs := 0
	flush := func() {
		if recs > 0 {
			b := fmt.Sprintf("%d\n", recs) + batch.String()
			h.write(fmt.Sprintf("0 found %d %d\n%s", id, len(b), b))
			batch.Reset()
			recs = 0
		}
	}
	count, files := int64(0), int64(0)
	status := "complete"
	if verb == "find-files" {
		for _, p := range paths {
			if count == max {
				status = "limit"
				break
			}
			fmt.Fprintf(&batch, "1 %d\n%s", len(p), p)
			recs++
			count++
			if recs == 2000 {
				flush()
			}
		}
		flush()
		reply(fmt.Sprintf("ok %d %d %s", id, count, status))
		h.ok(fmt.Sprintf("reads find-files batches (%d paths)", count))
		return
	}
	if flags&64 != 0 {
		h.warn("files", "the checker does not run RNRX programs: it answers this search with no matches")
		paths = nil
	}
	for _, p := range paths {
		data, err := os.ReadFile(filepath.Join(h.project, filepath.FromSlash(p)))
		if err != nil || bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 {
			continue
		}
		var ms bytes.Buffer
		m := 0
		for ln, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSuffix(line, "\r")
			hay, pat := line, string(pattern)
			if flags&16 != 0 {
				hay, pat = strings.ToLower(hay), strings.ToLower(pat)
			}
			for at := 0; count < max; {
				i := strings.Index(hay[at:], pat)
				if i < 0 {
					break
				}
				c := at + i
				fmt.Fprintf(&ms, "%d %d %d 1 %d\n%s", ln+1, c+1, c+1+len(pat), len(line), line)
				m++
				count++
				at = c + len(pat)
			}
		}
		if m > 0 {
			fmt.Fprintf(&batch, "1 0 0 %d %d\n%s%s", len(p), m, p, ms.String())
			recs++
			files++
		}
		if count == max {
			status = "limit"
			break
		}
	}
	flush()
	reply(fmt.Sprintf("ok %d %d %d 0 %s", id, count, files, status))
	h.ok(fmt.Sprintf("reads search-text batches (%d matches)", count))
}
