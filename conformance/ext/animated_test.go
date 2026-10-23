package main

import (
	"testing"
	"time"
)

func TestClosedPictureAllowsOnlyOneInflightDraw(t *testing.T) {
	h := &host{features: map[string]bool{"views": true}, closingViews: map[int64]int{1: 1}}
	var code string
	draw := func(view string) {
		code = ""
		h.viewRequest(1, "draw", []string{view, "0"}, nil,
			func(string) { t.Error("a closed picture accepted a draw") },
			func(c, _ string) { code = c })
		if code != "noview" {
			t.Fatalf("draw to closed view: %q, want noview", code)
		}
	}
	draw("1")
	if len(h.fails) != 0 {
		t.Fatal("the draw already in flight was reported as a protocol failure", h.fails)
	}
	draw("1")
	if len(h.fails) != 1 {
		t.Fatal("a second draw after close must fail", h.fails)
	}
	draw("99")
	if len(h.fails) != 2 {
		t.Fatal("an unknown view must still fail", h.fails)
	}
}

func TestServeReturnsWithContinuousTraffic(t *testing.T) {
	h := &host{msgs: make(chan message)}
	stop, done := make(chan struct{}), make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case h.msgs <- message{}:
			case <-stop:
				return
			}
		}
	}()
	go func() { h.serve(20 * time.Millisecond); close(done) }()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("continuous picture traffic prevented the checker from advancing")
	}
}
