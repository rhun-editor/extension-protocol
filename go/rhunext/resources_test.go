package rhunext

import (
	"strings"
	"testing"
)

func TestValidSecretKey(t *testing.T) {
	for _, k := range []string{"token", "a", "github.token-2_x", strings.Repeat("k", 64)} {
		if !ValidSecretKey(k) {
			t.Errorf("%q refused", k)
		}
	}
	for _, k := range []string{"", "Token", "a b", "ä", strings.Repeat("k", 65), "a/b"} {
		if ValidSecretKey(k) {
			t.Errorf("%q accepted", k)
		}
	}
}

func TestURIsAndContent(t *testing.T) {
	for _, u := range []string{"lsp-expand:/a/main.rs", "git+show:x"} {
		if !ValidURI(u) {
			t.Errorf("%q refused", u)
		}
	}
	for _, u := range []string{"C:x", "x", "Ab:x", "ab:", "ab:a\nb"} {
		if ValidURI(u) {
			t.Errorf("%q accepted", u)
		}
	}
	b, err := Content{Lang: "Visual Basic", Title: "t", Text: "hi\n"}.Encode()
	if err != nil || string(b) != "12 1 3\nVisual_Basicthi\n" {
		t.Errorf("%q %v", b, err)
	}
}
