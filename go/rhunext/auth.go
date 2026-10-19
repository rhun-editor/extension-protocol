package rhunext

// Signing in from an extension: OAuth's authorization code flow with PKCE (RFC 7636), the
// redirect received on a one-shot loopback HTTP listener, and random values.
//
//	pkce := rhunext.NewPKCE()
//	state := rhunext.RandomHex(16)
//	lb, err := rhunext.ListenLoopback()
//	redirect, err := ext.ExternalURIContext(ctx, lb.RedirectURI())
//	_, err = ext.OpenURLContext(ctx, authorizeURL+"?...&redirect_uri="+url.QueryEscape(redirect)+
//		"&state="+state+"&code_challenge="+pkce.Challenge+"&code_challenge_method=S256")
//	query, err := lb.Wait(ctx, state)   // query.Get("code"), exchanged with pkce.Verifier
//
// The listener needs [needs] network = yes (which the token exchange needs anyway).

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
)

// RandomHex returns n random bytes as lowercase hex (a state, a nonce).
func RandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("no random source")
	}
	return hex.EncodeToString(b)
}

// PKCE is a verifier and its S256 challenge: send Challenge with the authorization request and
// Verifier with the token request.
type PKCE struct {
	Verifier  string
	Challenge string
}

// NewPKCE makes a random 43-character verifier and its challenge.
func NewPKCE() PKCE {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("no random source")
	}
	v := base64.RawURLEncoding.EncodeToString(b)
	return PKCE{Verifier: v, Challenge: PKCEChallenge(v)}
}

// PKCEChallenge is base64url(SHA-256(verifier)) without padding.
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Loopback is a one-shot HTTP listener on 127.0.0.1 that receives an OAuth redirect.
type Loopback struct {
	ln   net.Listener
	Port int
}

// ListenLoopback listens on 127.0.0.1 and a port the system picks.
func ListenLoopback() (*Loopback, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	return &Loopback{ln: ln, Port: ln.Addr().(*net.TCPAddr).Port}, nil
}

// RedirectURI is http://127.0.0.1:PORT/callback: pass it through ExternalURIContext.
func (l *Loopback) RedirectURI() string {
	return fmt.Sprintf("http://127.0.0.1:%d/callback", l.Port)
}

// Close stops listening.
func (l *Loopback) Close() error { return l.ln.Close() }

// Wait serves until the browser's redirect arrives and returns its query once its state equals
// state (an error parameter, a wrong state or ctx ending is an error). The browser is told
// "Signed in. You can close this tab." The listener is closed afterwards.
func (l *Loopback) Wait(ctx context.Context, state string) (url.Values, error) {
	type result struct {
		q   url.Values
		err error
	}
	done := make(chan result, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		var err error
		switch {
		case q.Get("error") != "":
			err = &Error{Code: "refused", Text: "the provider said " + q.Get("error")}
		case q.Get("state") != state || q.Get("code") == "":
			err = &Error{Code: "refused", Text: "the redirect's state does not match"}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err != nil {
			fmt.Fprint(w, "<!doctype html><title>Sign-in failed</title><p>Sign-in failed. You can close this tab.")
		} else {
			fmt.Fprint(w, "<!doctype html><title>Signed in</title><p>Signed in. You can close this tab and return to rhun.")
		}
		select {
		case done <- result{q, err}:
		default:
		}
	})}
	go srv.Serve(l.ln)
	defer srv.Close()
	select {
	case r := <-done:
		return r.q, r.err
	case <-ctx.Done():
		return nil, &Error{Code: "cancelled", Text: "no sign-in within the time allowed"}
	}
}
