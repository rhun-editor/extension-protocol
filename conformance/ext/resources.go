package main

// The resources group of protocol 1.1: idle = stop and busy (feature idle), secrets (feature
// secrets, [needs] secrets = yes), virtual documents (feature uris), and URLs with link callbacks
// (feature urls). The checker keeps secrets in memory, as rhun does without a keyring; it answers
// open-url as a user choosing Open, and delivers every link it makes right away.

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

var secretKey = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)
var uriScheme = regexp.MustCompile(`^[a-z][a-z0-9+.-]{1,31}:`)

// validURI: SCHEME:REST, REST 1 to 4095 bytes without LF, CR or NUL
func validURI(uri string) bool {
	loc := uriScheme.FindStringIndex(uri)
	if loc == nil {
		return false
	}
	rest := uri[loc[1]:]
	return len(rest) >= 1 && len(rest) <= 4095 && !strings.ContainsAny(rest, "\r\n\x00")
}

// validURL: http(s), 8192 bytes at most, no spaces or controls
func validURL(u string) bool {
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") || len(u) > 8192 {
		return false
	}
	return !strings.ContainsFunc(u, func(r rune) bool { return r <= ' ' || r == 0x7f })
}

var reservedSchemes = map[string]bool{"file": true, "untitled": true, "rhun": true, "http": true, "https": true,
	"mailto": true, "data": true, "git": true, "remote": true}

// schemes is the manifest's [provides] schemes
func (m *manifest) schemes() []string { return strings.Fields(m.provides["schemes"]) }

// checkResources is the group's part of step 12: a provider of schemes answers a content request
func (h *host) checkResources() {
	if !h.features["uris"] || len(h.m.schemes()) == 0 {
		if len(h.m.schemes()) > 0 {
			h.fail("provides", "[provides] schemes without uris agreed after hello: rhun never asks")
		}
		return
	}
	uri := h.m.schemes()[0] + ":/conformance/sample.txt"
	m, ok := h.ask("content " + uri)
	switch {
	case !ok:
		h.fail("uris", "no answer to a content request within 3 s (rhun waits 5)")
	case strings.HasPrefix(m.line, "error "):
		h.ok("answers a content request it cannot serve with " + m.line)
	default:
		if why := checkContent(m.body); why != "" {
			h.fail("uris", why)
		} else {
			h.ok("answers content requests with a document rhun can show")
		}
	}
}

// checkContent: LANG_LEN TITLE_LEN TEXT_LEN LF, the three; UTF-8 text of 4 MiB at most
func checkContent(b []byte) string {
	head, rest, ok := strings.Cut(string(b), "\n")
	f := strings.Fields(head)
	if !ok || len(f) != 3 {
		return "a content answer starts with LANG_LEN TITLE_LEN TEXT_LEN and LF"
	}
	n := make([]int, 3)
	for i := range f {
		v, err := strconv.Atoi(f[i])
		if err != nil || v < 0 {
			return fmt.Sprintf("content: %q is not a length", f[i])
		}
		n[i] = v
	}
	if n[0] > 255 || n[1] > 255 || n[2] > 4<<20 {
		return "content: LANG and TITLE are at most 255 bytes, the text 4 MiB"
	}
	if n[0]+n[1]+n[2] > len(rest) {
		return "content: the lengths are past the body"
	}
	if !utf8.ValidString(rest[n[0]+n[1] : n[0]+n[1]+n[2]]) {
		return "content: the text is not UTF-8 (rhun shows an error instead)"
	}
	return ""
}

// resourcesManifest checks the group's manifest keys.
func resourcesManifest(m *manifest, bad func(string)) {
	switch m.top["idle"] {
	case "", "keep", "stop":
	default:
		bad(fmt.Sprintf("idle = %s is not keep or stop", m.top["idle"]))
	}
	if v, ok := m.top["idle.after"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 10 || n > 86400 {
			bad(fmt.Sprintf("idle.after = %s is not 10 to 86400 seconds", v))
		}
		if m.top["idle"] != "stop" {
			bad("idle.after without idle = stop does nothing")
		}
	}
	for _, sc := range m.schemes() {
		if !regexp.MustCompile(`^[a-z][a-z0-9+.-]{1,31}$`).MatchString(sc) {
			bad(fmt.Sprintf("[provides] schemes: %q is not [a-z][a-z0-9+.-]{1,31}", sc))
		} else if reservedSchemes[sc] {
			bad(fmt.Sprintf("[provides] schemes: %s is reserved", sc))
		} else if sc != m.name && !strings.HasPrefix(sc, m.name+"-") {
			bad(fmt.Sprintf("[provides] schemes: %s is not %s or %s-...", sc, m.name, m.name))
		}
	}
	switch m.top["runs"] {
	case "", "workspace", "local":
	default:
		bad(fmt.Sprintf("runs = %s is not workspace or local", m.top["runs"]))
	}
}

// resourcesRequest answers the group's requests; false for any other verb.
func (h *host) resourcesRequest(verb string, id int64, args []string, body func(string) ([]byte, bool),
	reply func(string), rerr func(code, text string)) bool {
	switch verb {
	case "open":
		uri := strings.Join(args[min(2, len(args)):], " ")
		if !h.features["uris"] || len(args) < 3 || !validURI(uri) || strings.HasPrefix(uri, "file:") {
			return false
		}
		scheme, _, _ := strings.Cut(uri, ":")
		if !slices.Contains(h.m.schemes(), scheme) {
			h.warn("uris", fmt.Sprintf("open %s: no scheme of this extension (rhun asks the extension that provides it)", uri))
			rerr("nofile", "no such file")
			return true
		}
		d := h.newDoc(uri, "", nil)
		reply(fmt.Sprintf("ok %d %d", id, d.id))
	case "content-changed":
		if !h.features["uris"] {
			h.fail("features", "content-changed without uris agreed after hello")
			rerr("unknown", "no such request")
			return true
		}
		uri := strings.Join(args, " ")
		if !validURI(uri) {
			h.fail("request grammar", "content-changed takes a URI")
			rerr("syntax", "cannot parse the line")
			return true
		}
		reply(fmt.Sprintf("ok %d", id))
	case "open-url", "external-uri", "link":
		var arg []byte
		if verb != "link" {
			if len(args) != 1 {
				h.fail("request grammar", verb+" takes LEN and the body")
				rerr("syntax", "cannot parse the line")
				return true
			}
			b, ok := body(args[0])
			if !ok {
				return true
			}
			arg = b
		} else if len(args) != 0 {
			h.fail("request grammar", "link takes nothing")
			rerr("syntax", "cannot parse the line")
			return true
		}
		if !h.features["urls"] {
			h.fail("features", verb+" without urls agreed after hello")
			rerr("unknown", "no such request")
			return true
		}
		switch verb {
		case "open-url":
			u := string(arg)
			if !validURL(u) {
				h.fail("urls", fmt.Sprintf("open-url %q: rhun opens http and https URLs only, of 8192 bytes at most", u))
				rerr("syntax", "not an http or https URL")
				return true
			}
			reply(fmt.Sprintf("ok %d opened", id))
		case "external-uri":
			u := string(arg)
			if !validURL(u) && !(strings.HasPrefix(u, "rhun:") && len(u) <= 8192) {
				h.fail("urls", "external-uri takes an http(s) URL or a rhun: link")
				rerr("syntax", "not an http(s) URL or rhun: link")
				return true
			}
			h.write(fmt.Sprintf("ok %d %d\n%s", id, len(arg), arg))
		case "link":
			owner := h.m.owner
			if owner == "" {
				owner = "local"
			}
			link := fmt.Sprintf("rhun://ext/%s/%s/%016x", owner, h.m.name, id*7919+12345)
			h.write(fmt.Sprintf("ok %d %d\n%s", id, len(link), link))
			back := link + "/callback?code=conformance"
			h.write(fmt.Sprintf("0 link %d %d\n%s", id, len(back), back))
			h.ok("asks for a rhun:// link (the checker delivers it at once)")
		}
	case "find-files", "search-text":
		if len(args) != 4 {
			h.fail("request grammar", verb+" takes FOLDER MAX FLAGS LEN")
			rerr("syntax", "cannot parse the line")
			return true
		}
		b, ok := body(args[3])
		if !ok {
			return true
		}
		h.filesRequest(verb, id, args, b, reply, rerr)
	case "folder-of":
		if !h.features["workspace-folders"] {
			h.fail("features", "folder-of without workspace-folders agreed after hello")
			rerr("unknown", "no such request")
			return true
		}
		path := strings.Join(args, " ")
		if path == "" {
			h.fail("request grammar", "folder-of takes a path")
			rerr("syntax", "cannot parse the line")
			return true
		}
		folder := 0
		if rel, err := filepath.Rel(h.project, strings.TrimPrefix(path, "file://")); err == nil && !strings.HasPrefix(rel, "..") {
			folder = 1
		}
		reply(fmt.Sprintf("ok %d %d", id, folder))
	case "setting-get":
		if !h.features["scoped-settings"] {
			h.fail("features", "setting-get without scoped-settings agreed after hello")
			rerr("unknown", "no such request")
			return true
		}
		if len(args) != 3 {
			h.fail("request grammar", "setting-get takes KEY FOLDER LANG (- for none)")
			rerr("syntax", "cannot parse the line")
			return true
		}
		f, ok := h.m.settingFields[args[0]]
		if !ok {
			h.fail("settings", "setting-get "+args[0]+": not one of its own settings")
			rerr("refused", "not a setting of this extension")
			return true
		}
		if n, err := strconv.Atoi(args[1]); err != nil || n < 0 {
			h.fail("request grammar", "setting-get: FOLDER is a folder id or 0")
			rerr("syntax", "cannot parse the line")
			return true
		} else if n > 1 {
			rerr("nofile", "no such folder")
			return true
		}
		v := f["default"]
		h.write(fmt.Sprintf("ok %d %d\n%s", id, len(v), v))
	case "busy":
		if !h.features["idle"] {
			h.fail("features", "busy without idle agreed after hello")
			rerr("unknown", "no such request")
			return true
		}
		if len(args) != 1 || (args[0] != "yes" && args[0] != "no") {
			h.fail("request grammar", "busy takes yes or no")
			rerr("syntax", "cannot parse the line")
			return true
		}
		reply(fmt.Sprintf("ok %d", id))
	case "secret-get", "secret-set", "secret-delete", "secret-keys":
		want := map[string]int{"secret-get": 1, "secret-set": 2, "secret-delete": 1, "secret-keys": 0}[verb]
		var value []byte
		if verb == "secret-set" && len(args) == 2 {
			b, ok := body(args[1])
			if !ok {
				return true
			}
			value = b
		}
		if !h.features["secrets"] {
			h.fail("features", verb+" without secrets agreed after hello")
			rerr("unknown", "no such request")
			return true
		}
		if h.m.needs["secrets"] != "yes" {
			h.fail("needs", verb+" needs secrets=yes but the manifest does not say so")
			rerr("needs", "secrets=yes")
			return true
		}
		if len(args) != want || (want > 0 && !secretKey.MatchString(args[0])) {
			h.fail("request grammar", verb+": KEY is [a-z0-9_.-]{1,64}")
			rerr("syntax", "cannot parse the line")
			return true
		}
		if h.secrets == nil {
			h.secrets = map[string][]byte{}
		}
		switch verb {
		case "secret-get":
			if v, ok := h.secrets[args[0]]; ok {
				h.write(fmt.Sprintf("ok %d 1 %d\n%s", id, len(v), v))
			} else {
				reply(fmt.Sprintf("ok %d 0 0", id))
			}
		case "secret-set":
			if len(value) > 8192 {
				h.fail("limits", fmt.Sprintf("a secret of %d bytes (8192 at most)", len(value)))
				rerr("toobig", "a value is at most 8192 bytes")
				return true
			}
			if _, ok := h.secrets[args[0]]; !ok && len(h.secrets) >= 64 {
				h.fail("limits", "more than 64 secret keys")
				rerr("toobig", "64 keys")
				return true
			}
			h.secrets[args[0]] = value
			reply(fmt.Sprintf("ok %d", id))
		case "secret-delete":
			delete(h.secrets, args[0])
			reply(fmt.Sprintf("ok %d", id))
		case "secret-keys":
			var b strings.Builder
			for k := range h.secrets {
				b.WriteString(k + "\n")
			}
			h.write(fmt.Sprintf("ok %d %d\n%s", id, b.Len(), b.String()))
		}
	default:
		return false
	}
	return true
}

// scopedSettingsManifest checks [settings] KEY.scope and KEY.languages; a project must never choose
// what an extension runs, so a key a workspace or folder may set that names a program is warned of
func scopedSettingsManifest(m *manifest, bad, warn func(string)) {
	for _, key := range m.settings {
		f := m.settingFields[key]
		switch f["scope"] {
		case "", "user":
		case "workspace", "folder":
			for _, w := range []string{"path", "command", "program", "bin", "args", "server"} {
				if strings.Contains(key, w) {
					warn(fmt.Sprintf("[settings] %s.scope = %s: a repository could choose what the extension runs (%q)",
						key, f["scope"], w))
					break
				}
			}
		default:
			bad(fmt.Sprintf("[settings] %s.scope = %s is not user, workspace or folder", key, f["scope"]))
		}
		switch f["languages"] {
		case "", "yes", "no":
		default:
			bad(fmt.Sprintf("[settings] %s.languages = %s is not yes or no", key, f["languages"]))
		}
	}
}

// sendFolders: "0 folders LEN" before "0 ready" for an extension that agreed workspace-folders: the
// checker's project as the one, primary and trusted, folder
func (h *host) sendFolders() {
	if !h.features["workspace-folders"] {
		return
	}
	body := fmt.Sprintf("1\n1 local 3 7 %d\nproject%s", len(h.project), h.project)
	h.write(fmt.Sprintf("0 folders %d\n%s", len(body), body))
}
