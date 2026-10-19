package main

// The resources group of protocol 1.1: idle = stop and busy (feature idle), secrets (feature
// secrets, [needs] secrets = yes). The checker keeps secrets in memory, as rhun does without a
// keyring.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var secretKey = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)

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
