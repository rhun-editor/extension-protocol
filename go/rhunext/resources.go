package rhunext

// The resources group of protocol 1.1: staying alive while busy (feature "idle"), passwords
// and tokens in the system keychain (feature "secrets", with [needs] secrets = yes), URIs
// with virtual documents (feature "uris", with [provides] schemes), and opening URLs with link
// callbacks (feature "urls"; sign-in helpers in auth.go).

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
)

const (
	FeatureIdle    = "idle"
	FeatureSecrets = "secrets"
	FeatureURIs    = "uris"
	FeatureURLs    = "urls"
	// FeatureScopedSettings: setting-get and the setting-changed event.
	FeatureScopedSettings = "scoped-settings"
	// FeatureWorkspaceFolders: the folders event and folder-of.
	FeatureWorkspaceFolders = "workspace-folders"
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

// ValidURI says whether uri is SCHEME:REST as rhun takes it: SCHEME [a-z][a-z0-9+.-] of 2 to 32
// bytes, REST 1 to 4095 bytes without LF, CR or NUL.
func ValidURI(uri string) bool {
	i := strings.IndexByte(uri, ':')
	if i < 2 || i > 32 || uri[0] < 'a' || uri[0] > 'z' {
		return false
	}
	for j := 0; j < i; j++ {
		c := uri[j]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '.' || c == '-') {
			return false
		}
	}
	rest := uri[i+1:]
	return len(rest) >= 1 && len(rest) <= 4095 && !strings.ContainsAny(rest, "\n\r\x00")
}

// Content is a virtual document's text, the answer to rhun's content request.
type Content struct {
	Lang  string // a grammar name, or "" to choose by the URI's last segment
	Title string // the tab's title, or "" for the URI's last segment
	Text  string // at most 4 MiB of UTF-8
}

// Encode is the content answer's body: LANG_LEN TITLE_LEN TEXT_LEN LF and the three.
func (c Content) Encode() ([]byte, error) {
	lang := strings.ReplaceAll(c.Lang, " ", "_")
	if len(lang) > 255 || len(c.Title) > 255 || strings.ContainsAny(c.Title, "\n\r") {
		return nil, fmt.Errorf("a language or title over 255 bytes or with a line break")
	}
	if len(c.Text) > 4<<20 {
		return nil, fmt.Errorf("the text is over 4 MiB")
	}
	return []byte(fmt.Sprintf("%d %d %d\n%s%s%s", len(lang), len(c.Title), len(c.Text), lang, c.Title, c.Text)), nil
}

// ContentProvider answers content requests for the schemes in [provides] schemes (feature uris).
// An error shows in the document's tab.
type ContentProvider interface {
	Content(ext *Ext, uri string) (Content, error)
}

// OpenURIContext opens a virtual document (feature uris); a file: URI opens that file.
func (e *Ext) OpenURIContext(ctx context.Context, uri string, at Pos) (Doc, error) {
	if !e.Has(FeatureURIs) {
		return 0, notAgreed(FeatureURIs)
	}
	if !ValidURI(uri) {
		return 0, &Error{Code: "syntax", Text: "invalid URI"}
	}
	r, err := e.RequestContext(ctx, "open", nil, itoa(int64(at.Line)), itoa(int64(at.Col)), uri)
	if err != nil {
		return 0, err
	}
	return Doc(argInt(r, 0)), nil
}

// ContentChangedContext says the virtual documents open for uri changed: rhun asks again.
func (e *Ext) ContentChangedContext(ctx context.Context, uri string) error {
	if !e.Has(FeatureURIs) {
		return notAgreed(FeatureURIs)
	}
	if !ValidURI(uri) {
		return &Error{Code: "syntax", Text: "invalid URI"}
	}
	_, err := e.RequestContext(ctx, "content-changed", nil, uri)
	return err
}

// ParseContentClosed reads "0 content-closed URI": the last tab showing it closed.
func ParseContentClosed(ev Event) (string, bool) {
	if ev.Name != "content-closed" || len(ev.Args) == 0 {
		return "", false
	}
	return strings.Join(ev.Args, " "), true
}

// OpenURLContext asks rhun to open an http(s) URL in the user's browser (feature urls). The user
// sees the extension and the host and chooses Open, Copy Link or Cancel (an *Error with Code
// "cancelled"). copied is true when the user copied the link instead.
func (e *Ext) OpenURLContext(ctx context.Context, url string) (copied bool, err error) {
	if !e.Has(FeatureURLs) {
		return false, notAgreed(FeatureURLs)
	}
	if !(strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://")) || len(url) > 8192 ||
		strings.IndexFunc(url, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
		return false, &Error{Code: "syntax", Text: "not an http or https URL"}
	}
	r, err := e.RequestContext(ctx, "open-url", []byte(url), itoa(int64(len(url))))
	if err != nil {
		return false, err
	}
	return len(r.Args) > 0 && r.Args[0] == "copied", nil
}

// ExternalURIContext returns the URI the user's browser can use for uri (a loopback URL, a rhun:
// link): pass every loopback URL you hand to a browser through it (a remote window forwards the
// port).
func (e *Ext) ExternalURIContext(ctx context.Context, uri string) (string, error) {
	if !e.Has(FeatureURLs) {
		return "", notAgreed(FeatureURLs)
	}
	r, err := e.RequestContext(ctx, "external-uri", []byte(uri), itoa(int64(len(uri))))
	if err != nil {
		return "", err
	}
	return string(r.Body), nil
}

// LinkContext returns a one-shot rhun://ext/OWNER/NAME/TOKEN link (ten minutes); append a path
// and query. When it reaches rhun, the event link (see ParseLink) brings it back.
func (e *Ext) LinkContext(ctx context.Context) (string, error) {
	if !e.Has(FeatureURLs) {
		return "", notAgreed(FeatureURLs)
	}
	r, err := e.RequestContext(ctx, "link", nil)
	if err != nil {
		return "", err
	}
	return string(r.Body), nil
}

// ParseLink reads "0 link ID LEN" and its body: the id of the link request that made the token,
// and the whole link as it reached rhun (with the path and query the browser added).
func ParseLink(ev Event) (id int64, link string, ok bool) {
	if ev.Name != "link" || len(ev.Args) != 2 {
		return 0, "", false
	}
	id, err := strconv.ParseInt(ev.Args[0], 10, 64)
	if err != nil {
		return 0, "", false
	}
	return id, string(ev.Body), true
}

// SettingGetContext returns the value of one of the extension's own settings in force for a folder
// (its id; 0 for the window) and a language (a grammar name, "" for none) (feature
// scoped-settings): folder@LANG, folder, workspace@LANG, workspace, user@LANG, user, default.
func (e *Ext) SettingGetContext(ctx context.Context, key string, folder int64, lang string) (string, error) {
	if !e.Has(FeatureScopedSettings) {
		return "", notAgreed(FeatureScopedSettings)
	}
	if key == "" || strings.ContainsAny(key, " \r\n") {
		return "", &Error{Code: "syntax", Text: "invalid setting key"}
	}
	l := strings.ReplaceAll(lang, " ", "_")
	if l == "" {
		l = "-"
	}
	r, err := e.RequestContext(ctx, "setting-get", nil, key, itoa(folder), l)
	if err != nil {
		return "", err
	}
	return string(r.Body), nil
}

// ParseSettingChanged reads "0 setting-changed KEY": a stored value of KEY changed at some scope.
func ParseSettingChanged(ev Event) (string, bool) {
	if ev.Name != "setting-changed" || len(ev.Args) != 1 {
		return "", false
	}
	return ev.Args[0], true
}

// Folder is a workspace folder: ID stays the same for the rhun session; Flags has 1 for the
// primary folder (0 ready's PROJECT) and 2 when it is trusted; Location is "local".
type Folder struct {
	ID       int64
	Location string
	Flags    int
	Name     string
	Path     string
}

// ParseFolders reads "0 folders LEN" (feature workspace-folders): the whole list, sent before
// "0 ready" and again when it changes for a program that keeps running.
func ParseFolders(ev Event) ([]Folder, bool) {
	if ev.Name != "folders" {
		return nil, false
	}
	b := ev.Body
	nl := bytes.IndexByte(b, '\n')
	if nl < 0 {
		return nil, false
	}
	n, err := strconv.Atoi(string(b[:nl]))
	if err != nil || n < 0 || n > 16 {
		return nil, false
	}
	b = b[nl+1:]
	out := make([]Folder, 0, n)
	for i := 0; i < n; i++ {
		nl = bytes.IndexByte(b, '\n')
		if nl < 0 {
			return nil, false
		}
		f := strings.Fields(string(b[:nl]))
		if len(f) != 5 {
			return nil, false
		}
		id, e1 := strconv.ParseInt(f[0], 10, 64)
		flags, e2 := strconv.Atoi(f[2])
		nlen, e3 := strconv.Atoi(f[3])
		plen, e4 := strconv.Atoi(f[4])
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || nlen < 0 || plen < 0 || nl+1+nlen+plen > len(b) {
			return nil, false
		}
		b = b[nl+1:]
		out = append(out, Folder{ID: id, Location: f[1], Flags: flags, Name: string(b[:nlen]), Path: string(b[nlen : nlen+plen])})
		b = b[nlen+plen:]
	}
	return out, len(b) == 0
}

// FolderOfContext returns the id of the workspace folder path is in, or 0.
func (e *Ext) FolderOfContext(ctx context.Context, path string) (int64, error) {
	if !e.Has(FeatureWorkspaceFolders) {
		return 0, notAgreed(FeatureWorkspaceFolders)
	}
	if path == "" || strings.ContainsAny(path, "\r\n") {
		return 0, &Error{Code: "syntax", Text: "invalid path"}
	}
	r, err := e.RequestContext(ctx, "folder-of", nil, path)
	if err != nil {
		return 0, err
	}
	return int64(argInt(r, 0)), nil
}
