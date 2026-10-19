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
