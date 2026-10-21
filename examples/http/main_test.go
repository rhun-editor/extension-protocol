package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestURLsAndRendering(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:8000/", "https://example.com/a?b=1"} {
		if why := checkURL(u); why != "" {
			t.Fatalf("%s: %s", u, why)
		}
	}
	for _, u := range []string{"example.com", "ftp://x/", "http://", "::"} {
		if checkURL(u) == "" {
			t.Fatalf("%s passed", u)
		}
	}
	resp := &http.Response{Status: "200 OK", Header: http.Header{"Content-Type": {"application/json"}}}
	md := render("GET", "http://x/", resp, []byte(`{"a":[1,2]}`), 12*time.Millisecond)
	if !strings.Contains(md, "**200 OK** · 12 ms") || !strings.Contains(md, "```json\n{\n  \"a\": [") {
		t.Fatal(md)
	}
}
