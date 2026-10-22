package main

import "testing"

func TestTableFormDocAndQuickBodies(t *testing.T) {
	v := newRview(1, "files", "panel")
	v.vkind = "table"
	if code, why := applyPatch(v, []byte("2\ncolumns 2 34\nleft 0 sort 4\nNameright 6 - 4\nSizesorted 2 desc 0\n")); code != "" {
		t.Fatal(code, why)
	}
	if code, _ := applyPatch(v, []byte("1\ncolumns 1 15\nmiddle 0 - 1\nX")); code != "syntax" {
		t.Fatal("a bad align passed")
	}
	if code, _ := applyPatch(v, []byte("1\ndoc 1\nx")); code != "noview" {
		t.Fatal("doc on a table passed")
	}
	f := newRview(2, "setup", "panel")
	f.vkind = "form"
	if code, why := applyPatch(f, []byte("1\nfield 39\nport number 1 1 4 14\nPortvalue - 4\n8080")); code != "" {
		t.Fatal(code, why)
	}
	if code, _ := applyPatch(f, []byte("1\nfield 22\nport color 1 1 4 0\nPort")); code != "syntax" {
		t.Fatal("a bad type passed")
	}
	d := newRview(3, "readme", "side")
	d.vkind = "doc"
	if code, why := applyPatch(d, []byte("3\ndoc 5\n# Hi\nbase 2\n/pfollow 3 0\n")); code != "" {
		t.Fatal(code, why)
	}
	if code, _ := applyPatch(d, []byte("1\nbase 3\nrel")); code != "syntax" {
		t.Fatal("a relative base passed")
	}
	if why, big := checkQuickBody([]byte("title 6\nBranchmode pick 0\nstep 1 2 0\nitems 1 34\n4 4 14 item\nmainmaindesc 7\ncurrent")); why != "" || big {
		t.Fatal(why, big)
	}
	if why, _ := checkQuickBody([]byte("flags multi shiny 0\n")); why == "" {
		t.Fatal("an unknown flag passed")
	}
}
