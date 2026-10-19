package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

func snippetField(words []string) (rhunext.Field, error) {
	var field rhunext.Field
	if len(words) != 6 || words[0] != "field" || words[5] != "0" {
		return field, fmt.Errorf("expected field GROUP START END PARENT 0")
	}
	values := []*int{&field.Group, &field.Start, &field.End, &field.Parent}
	for i, value := range values {
		n, ok := saveNumber(words[i+1])
		if !ok || n > 2147483647 {
			return field, fmt.Errorf("invalid snippet field number")
		}
		*value = int(n)
	}
	return field, nil
}

// snippetVariant bounds every accumulated digit, so long leading zeroes cannot wrap.
func snippetVariant(words []string) (int, error) {
	if len(words) != 3 || words[0] != "variant" || words[2] != "0" || words[1] == "" {
		return 0, fmt.Errorf("expected variant FIELD 0")
	}
	n := 0
	for _, b := range []byte(words[1]) {
		if b < '0' || b > '9' {
			return 0, fmt.Errorf("invalid variant index")
		}
		n = n*10 + int(b-'0')
		if n > rhunext.MaxSnippetFields {
			return 0, fmt.Errorf("invalid variant index")
		}
	}
	if n == 0 {
		return 0, fmt.Errorf("invalid variant index")
	}
	return n, nil
}

func snippetChoice(words []string, body []byte) (rhunext.SnippetChoice, error) {
	var c rhunext.SnippetChoice
	if len(words) != 4 || words[0] != "choice" {
		return c, fmt.Errorf("expected choice GROUP COUNT LEN")
	}
	group, ok1 := saveNumber(words[1])
	count, ok2 := saveNumber(words[2])
	size, ok3 := saveNumber(words[3])
	if !ok1 || !ok2 || !ok3 || group <= 0 || group > 2147483647 || count <= 0 || count > rhunext.MaxSnippetChoiceOptions || size != int64(len(body)) {
		return c, fmt.Errorf("invalid snippet choice header")
	}
	c.Group = int(group)
	for i := int64(0); i < count; i++ {
		end := bytes.IndexByte(body, '\n')
		if end < 0 {
			return c, fmt.Errorf("missing snippet option length")
		}
		n, ok := saveNumber(string(body[:end]))
		body = body[end+1:]
		if !ok || n > rhunext.MaxSnippetOptionBytes || n > int64(len(body)) || !utf8.Valid(body[:int(n)]) {
			return c, fmt.Errorf("invalid snippet option length or UTF-8")
		}
		c.Options = append(c.Options, string(body[:int(n)]))
		body = body[int(n):]
	}
	if len(body) != 0 {
		return c, fmt.Errorf("trailing snippet choice bytes")
	}
	return c, nil
}

func snippetTransforms(words []string, body []byte) ([]rhunext.SnippetFieldTransform, error) {
	if len(words) != 3 || words[0] != "transforms" || words[1] != "0" {
		return nil, fmt.Errorf("expected transforms 0 LEN")
	}
	size, ok := saveNumber(words[2])
	if !ok || len(words[2]) > 10 || size != int64(len(body)) || len(body) > 5592408 || len(body)%4 != 0 || bytes.IndexAny(body, "\r\n") >= 0 {
		return nil, fmt.Errorf("invalid snippet transforms length or Base64")
	}
	decodedSize := len(body) / 4 * 3
	if len(body) > 0 && body[len(body)-1] == '=' {
		decodedSize--
	}
	if len(body) > 1 && body[len(body)-2] == '=' {
		decodedSize--
	}
	if decodedSize > rhunext.MaxSnippetBody {
		return nil, fmt.Errorf("snippet transforms payload too large")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(string(body))
	if err != nil {
		return nil, fmt.Errorf("invalid snippet transforms Base64: %w", err)
	}
	if len(raw) > rhunext.MaxSnippetBody {
		return nil, fmt.Errorf("snippet transforms payload too large")
	}
	return rhunext.DecodeSnippetTransforms(raw)
}

func decodeSnippet(body []byte) (rhunext.PreparedSnippet, error) {
	p, err := decodeSnippetProgram(body)
	return p.Snippet, err
}

func decodeSnippetProgram(body []byte) (rhunext.SnippetProgram, error) {
	return decodeSnippetProgramFeatures(body, nil)
}
func decodeSnippetProgramFeatures(body []byte, features map[string]bool) (rhunext.SnippetProgram, error) {
	transformsSeen := false
	var program rhunext.SnippetProgram
	snippet := &program.Snippet
	if len(body) > rhunext.MaxSnippetBody {
		return program, fmt.Errorf("snippet body exceeds its limit")
	}
	end := bytes.IndexByte(body, '\n')
	if end < 0 {
		return program, fmt.Errorf("missing prepared snippet header")
	}
	words := strings.Fields(string(body[:end]))
	if len(words) != 9 || strings.Join(words[:6], " ") != "0 0 0 0 snippet 0" || words[7] != "0" {
		return program, fmt.Errorf("invalid prepared snippet header")
	}
	textLen, ok1 := saveNumber(words[6])
	extraLen, ok2 := saveNumber(words[8])
	remaining := body[end+1:]
	if !ok1 || !ok2 || textLen > int64(len(remaining)) || extraLen != int64(len(remaining))-textLen {
		return program, fmt.Errorf("prepared snippet lengths do not match its body")
	}
	snippet.Text = string(remaining[:int(textLen)])
	for extra := remaining[int(textLen):]; len(extra) > 0; {
		end := bytes.IndexByte(extra, '\n')
		if end < 0 {
			return program, fmt.Errorf("invalid snippet metadata framing")
		}
		words := strings.Fields(string(extra[:end]))
		extra = extra[end+1:]
		if len(words) == 0 {
			return program, fmt.Errorf("empty snippet metadata header")
		}
		switch words[0] {
		case "field":
			if len(snippet.Fields) == rhunext.MaxSnippetFields {
				return program, fmt.Errorf("too many snippet fields")
			}
			field, err := snippetField(words)
			if err != nil {
				return program, err
			}
			snippet.Fields = append(snippet.Fields, field)
		case "variant":
			if len(program.InitialVariants) == rhunext.MaxSnippetFields {
				return program, fmt.Errorf("too many initial variants")
			}
			index, err := snippetVariant(words)
			if err != nil {
				return program, err
			}
			program.InitialVariants = append(program.InitialVariants, index)
		case "transforms":
			if transformsSeen || (features != nil && (!features["snippets"] || !features["snippet-transforms"])) {
				return program, fmt.Errorf("duplicate or unnegotiated snippet transforms")
			}
			transformsSeen = true
			if len(words) != 3 {
				return program, fmt.Errorf("invalid snippet transforms header")
			}
			n, ok := saveNumber(words[2])
			if !ok || n > int64(len(extra)) {
				return program, fmt.Errorf("invalid snippet transforms length")
			}
			bindings, err := snippetTransforms(words, extra[:int(n)])
			if err != nil {
				return program, err
			}
			program.Transforms = bindings
			extra = extra[int(n):]
		case "choice":
			if len(words) != 4 || len(program.Choices) == rhunext.MaxSnippetGroups {
				return program, fmt.Errorf("invalid snippet choice header or count")
			}
			n, ok := saveNumber(words[3])
			if !ok || n > int64(len(extra)) {
				return program, fmt.Errorf("invalid snippet choice length")
			}
			c, err := snippetChoice(words, extra[:int(n)])
			if err != nil {
				return program, err
			}
			program.Choices = append(program.Choices, c)
			extra = extra[int(n):]
		default:
			return program, fmt.Errorf("unknown snippet metadata record")
		}
	}
	return program, program.Validate()
}

func (h *host) snippetRequest(args []string, body []byte, reply func(string), rerr func(string, string), id int64) {
	if !h.features["snippets"] {
		rerr("unknown", "snippets was not agreed")
		return
	}
	if len(args) != 6 {
		rerr("syntax", "snippet takes DOC VERSION L1 C1 L2 C2 LEN")
		return
	}
	docID, ok1 := saveNumber(args[0])
	version, ok2 := saveNumber(args[1])
	if !ok1 || !ok2 || docID <= 0 {
		rerr("syntax", "invalid snippet target")
		return
	}
	d := h.docs[docID]
	if d == nil {
		rerr("nodoc", "document closed")
		return
	}
	if version != d.version {
		rerr("stale", strconv.FormatInt(d.version, 10))
		return
	}
	for _, value := range args[2:] {
		if n, ok := saveNumber(value); !ok || n <= 0 || n > 2147483647 {
			rerr("badpos", "invalid snippet position")
			return
		}
	}
	start, why := d.position(args[2], args[3])
	end := 0
	if why == "" {
		end, why = d.position(args[4], args[5])
	}
	if why != "" || end < start {
		rerr("badpos", "invalid snippet range")
		return
	}
	program, err := decodeSnippetProgramFeatures(body, h.features)
	if err != nil {
		rerr("syntax", err.Error())
		return
	}
	if len(program.InitialVariants) > 0 && !h.features["snippet-variants"] {
		rerr("unknown", "snippet-variants was not agreed")
		return
	}
	snippet := program.Snippet
	insert := []byte(snippet.Text)
	d.text = append(append(append([]byte{}, d.text[:start]...), insert...), d.text[end:]...)
	recs := records(start, end-start, insert)
	d.version += int64(len(recs))
	d.dirty = d.dirty || len(recs) > 0
	d.cur, d.anchor = start+len(insert), start+len(insert)
	group := 2147483648
	for _, field := range snippet.Fields {
		if field.Group > 0 && field.Group < group {
			group = field.Group
			d.cur, d.anchor = start+field.End, start+field.Start
		}
	}
	if group == 2147483648 {
		for _, field := range snippet.Fields {
			if field.Group == 0 {
				d.cur, d.anchor = start+field.End, start+field.Start
			}
		}
	}
	reply(fmt.Sprintf("ok %d %d", id, d.version))
	h.afterEdit(d, recs...)
}
