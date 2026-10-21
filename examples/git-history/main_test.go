package main

import (
	"strings"
	"testing"
	"time"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

func TestPages(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var log []rhunext.LogEntry
	for i := 0; i < 450; i++ {
		log = append(log, rhunext.LogEntry{SHA: strings.Repeat("a", 39) + string(rune('0'+i%10)), Author: "Ada", Time: 1_000_000 - 120, Subject: "c"})
	}
	nodes, more := commitNodes(log, 0, now)
	if len(nodes) != 200 || !more {
		t.Fatal(len(nodes), more)
	}
	nodes, more = commitNodes(log, 400, now)
	if len(nodes) != 50 || more {
		t.Fatal(len(nodes), more)
	}
	if ago(1_000_000-120, now) != "2 minutes ago" {
		t.Fatal(ago(1_000_000-120, now))
	}
}
