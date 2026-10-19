package rhunext

// The resources group of protocol 1.1: staying alive while busy (feature "idle") and passwords
// and tokens in the system keychain (feature "secrets", with [needs] secrets = yes).

import (
	"context"
	"strings"
)

const (
	FeatureIdle    = "idle"
	FeatureSecrets = "secrets"
	// SecretValueMax is the most bytes one value holds.
	SecretValueMax = 8192
)

// ValidSecretKey says whether key is 1 to 64 bytes of a-z 0-9 _ . -.
func ValidSecretKey(key string) bool {
	if len(key) == 0 || len(key) > 64 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-') {
			return false
		}
	}
	return true
}

func notAgreed(feature string) error {
	return &Error{Code: "unknown", Text: feature + " was not negotiated"}
}

// BusyContext sends busy yes|no (feature idle): while busy, an idle = stop extension is not
// stopped for idleness.
func (e *Ext) BusyContext(ctx context.Context, busy bool) error {
	if !e.Has(FeatureIdle) {
		return notAgreed(FeatureIdle)
	}
	word := "no"
	if busy {
		word = "yes"
	}
	_, err := e.RequestContext(ctx, "busy", nil, word)
	return err
}

// SecretGetContext returns the value stored under key, and whether there is one.
func (e *Ext) SecretGetContext(ctx context.Context, key string) ([]byte, bool, error) {
	if !e.Has(FeatureSecrets) {
		return nil, false, notAgreed(FeatureSecrets)
	}
	if !ValidSecretKey(key) {
		return nil, false, &Error{Code: "syntax", Text: "invalid secret key"}
	}
	r, err := e.RequestContext(ctx, "secret-get", nil, key)
	if err != nil {
		return nil, false, err
	}
	if len(r.Args) != 2 || (r.Args[0] != "0" && r.Args[0] != "1") {
		return nil, false, &Error{Code: "syntax", Text: "invalid secret-get answer"}
	}
	return r.Body, r.Args[0] == "1", nil
}

// SecretSetContext stores value (at most SecretValueMax bytes) under key.
func (e *Ext) SecretSetContext(ctx context.Context, key string, value []byte) error {
	if !e.Has(FeatureSecrets) {
		return notAgreed(FeatureSecrets)
	}
	if !ValidSecretKey(key) {
		return &Error{Code: "syntax", Text: "invalid secret key"}
	}
	if len(value) > SecretValueMax {
		return &Error{Code: "toobig", Text: "a value is at most 8192 bytes"}
	}
	_, err := e.RequestContext(ctx, "secret-set", value, key, itoa(int64(len(value))))
	return err
}

// SecretDeleteContext removes key (also when nothing was stored).
func (e *Ext) SecretDeleteContext(ctx context.Context, key string) error {
	if !e.Has(FeatureSecrets) {
		return notAgreed(FeatureSecrets)
	}
	if !ValidSecretKey(key) {
		return &Error{Code: "syntax", Text: "invalid secret key"}
	}
	_, err := e.RequestContext(ctx, "secret-delete", nil, key)
	return err
}

// SecretKeysContext lists the keys stored for this extension.
func (e *Ext) SecretKeysContext(ctx context.Context) ([]string, error) {
	if !e.Has(FeatureSecrets) {
		return nil, notAgreed(FeatureSecrets)
	}
	r, err := e.RequestContext(ctx, "secret-keys", nil)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, k := range strings.Split(string(r.Body), "\n") {
		if k != "" {
			keys = append(keys, k)
		}
	}
	return keys, nil
}
