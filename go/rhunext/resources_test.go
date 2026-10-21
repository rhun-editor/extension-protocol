package rhunext

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
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

func TestPKCEAndLoopback(t *testing.T) {
	if c := PKCEChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); c != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Errorf("challenge %q", c)
	}
	if p := NewPKCE(); len(p.Verifier) != 43 || PKCEChallenge(p.Verifier) != p.Challenge {
		t.Errorf("pkce %+v", p)
	}
	lb, err := ListenLoopback()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		resp, err := http.Get(lb.RedirectURI() + "?code=c%2B1&state=xyz")
		if err == nil {
			resp.Body.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	q, err := lb.Wait(ctx, "xyz")
	if err != nil || q.Get("code") != "c+1" {
		t.Errorf("%v %v", q, err)
	}
}

func TestParseLink(t *testing.T) {
	id, link, ok := ParseLink(Event{Name: "link", Args: []string{"7", "5"}, Body: []byte("rhun:")})
	if !ok || id != 7 || link != "rhun:" {
		t.Errorf("%d %q %v", id, link, ok)
	}
}

func TestParseFolders(t *testing.T) {
	body := "2\n1 local 3 3 6\napp/w/app4 local 2 3 6\nlib/w/lib"
	f, ok := ParseFolders(Event{Name: "folders", Args: []string{"x"}, Body: []byte(body)})
	if !ok || len(f) != 2 || f[0].Path != "/w/app" || f[1].ID != 4 || f[1].Name != "lib" || f[0].Flags != 3 {
		t.Fatalf("%+v %v", f, ok)
	}
	if _, ok := ParseFolders(Event{Name: "folders", Body: []byte("1\n1 local 3 3 99\napp")}); ok {
		t.Fatal("a short body accepted")
	}
}
