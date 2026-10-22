// http: a request form in the panel (feature view-form). Send (the button, Enter in the URL, the
// command) checks the URL, shows progress in the status bar with Cancel (feature progress) and
// opens the response in a doc tab (feature view-doc): status, time, headers and the body (JSON
// indented in a highlighted fence).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

const maxBody = 1 << 20

type client struct {
	mu     sync.Mutex
	view   rhunext.View
	values map[string]string // the fields' values as the user left them
	cancel context.CancelFunc
	prog   int64
}

func (c *client) form() *rhunext.Patch {
	return (&rhunext.Patch{}).Form(
		rhunext.NewFormField("method", "select", 1, 1, "Method").Options("GET", "POST", "PUT", "DELETE").Value(-1, "GET"),
		rhunext.NewFormField("url", "text", 1, 5, "URL").Placeholder("https://example.com/api").
			Value(-1, "http://127.0.0.1:8000/"),
		rhunext.NewFormField("headers", "area", 2, 1, "Headers (Name: value, one a line)"),
		rhunext.NewFormField("body", "area", 3, 1, "Body"),
		rhunext.NewFormField("send", "button", 4, 1, "Send").Primary(),
	)
}

// checkURL is "" for an http or https URL with a host, else why not.
func checkURL(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "an http:// or https:// URL with a host"
	}
	return ""
}

// render is the response as Markdown for the doc tab.
func render(method, target string, resp *http.Response, body []byte, took time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s %s\n\n**%s** · %d ms · %d bytes\n\n", method, target, resp.Status, took.Milliseconds(), len(body))
	names := make([]string, 0, len(resp.Header))
	for k := range resp.Header {
		names = append(names, k)
	}
	sort.Strings(names)
	b.WriteString("| Header | Value |\n|---|---|\n")
	for _, k := range names {
		fmt.Fprintf(&b, "| %s | %s |\n", k, strings.ReplaceAll(strings.Join(resp.Header[k], ", "), "|", "\\|"))
	}
	lang := "text"
	if strings.Contains(resp.Header.Get("Content-Type"), "json") || json.Valid(body) && len(body) > 0 {
		var out bytes.Buffer
		if json.Indent(&out, body, "", "  ") == nil {
			body, lang = out.Bytes(), "json"
		}
	}
	fmt.Fprintf(&b, "\n```%s\n%s\n```\n", lang, strings.ReplaceAll(string(body), "```", "` ` `"))
	return b.String()
}

func (c *client) send(ext *rhunext.Ext) {
	c.mu.Lock()
	method, target := c.values["method"], strings.TrimSpace(c.values["url"])
	headers, body := c.values["headers"], c.values["body"]
	view := c.view
	if c.cancel != nil {
		c.mu.Unlock()
		return // one request at a time
	}
	c.mu.Unlock()
	if why := checkURL(target); why != "" {
		if view != 0 {
			ext.PatchView(view, (&rhunext.Patch{}).Field(rhunext.NewFormField("url", "text", 1, 5, "URL").Msg("error", why)))
		}
		return
	}
	if view != 0 {
		ext.PatchView(view, (&rhunext.Patch{}).Field(rhunext.NewFormField("url", "text", 1, 5, "URL").Msg("info", "")))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	prog, _ := ext.Progress(rhunext.ProgressStatus, true, method+" "+target)
	c.mu.Lock()
	c.cancel, c.prog = cancel, prog
	c.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			ext.ProgressEnd(prog)
			c.mu.Lock()
			c.cancel = nil
			c.mu.Unlock()
		}()
		req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
		if err != nil {
			ext.Toast("HTTP: " + err.Error())
			return
		}
		for _, l := range strings.Split(headers, "\n") {
			if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) != "" {
				req.Header.Add(strings.TrimSpace(k), strings.TrimSpace(v))
			}
		}
		start := time.Now()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			if ctx.Err() == context.Canceled {
				ext.Toast("HTTP: cancelled")
			} else {
				ext.Toast("HTTP: " + err.Error())
			}
			return
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		resp.Body.Close()
		tab, err := ext.TabView("doc", method+" "+target)
		if err != nil {
			ext.Logf("tab: %v", err)
			return
		}
		ext.PatchView(tab, (&rhunext.Patch{}).Doc(render(method, target, resp, data, time.Since(start))))
	}()
}

func (c *client) Ready(ext *rhunext.Ext, project string) {}

func (c *client) Run(ext *rhunext.Ext, command string, doc rhunext.Doc) {
	if command == "http.send" {
		c.send(ext)
	}
}

func (c *client) Event(ext *rhunext.Ext, ev rhunext.Event) {}

func (c *client) ViewOpen(ext *rhunext.Ext, open rhunext.ViewOpen) {
	if open.Name != "request" {
		return
	}
	c.mu.Lock()
	c.view = open.View
	c.values = map[string]string{"method": "GET", "url": "http://127.0.0.1:8000/"}
	c.mu.Unlock()
	ext.PatchView(open.View, c.form())
}

func (c *client) ViewEvent(ext *rhunext.Ext, view rhunext.View, ev rhunext.RetainedEvent) {
	switch ev.Kind {
	case "change":
		c.mu.Lock()
		c.values[ev.Key] = string(ev.Body)
		c.mu.Unlock()
	case "press", "submit":
		if ev.Key == "send" || ev.Key == "url" {
			c.send(ext)
		}
	}
}

// ProgressCancel: Cancel on the progress stops the request.
func (c *client) ProgressCancel(ext *rhunext.Ext, p int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil && p == c.prog {
		c.cancel()
	}
}

func main() {
	features := []string{rhunext.FeatureViewTree, rhunext.FeatureViewForm, rhunext.FeatureViewDoc, rhunext.FeatureProgress}
	if err := rhunext.RunWith(features, &client{values: map[string]string{}}); err != nil {
		os.Exit(1)
	}
}
