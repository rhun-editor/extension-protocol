package rhunext

import "testing"

func TestTablesFormsDocsAndQuick(t *testing.T) {
	p := (&Patch{}).Columns(Column{Title: "Name", Sort: true}, Column{Title: "Size", Align: "right", Min: 6}).
		Sorted(2, true).Kids(nil, []*Node{NewNode("a", "a.rs").Cell("12")}, false)
	b, err := p.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := "3\ncolumns 2 34\nleft 0 sort 4\nNameright 6 - 4\nSizesorted 2 desc 0\nkids 0 1 0 25\n1 4 9 leaf\naa.rscell 2\n12"
	if string(b) != want {
		t.Fatalf("%q", b)
	}
	f, _ := (&Patch{}).Field(NewFormField("port", "number", 1, 1, "Port").Value(-1, "8080")).Bytes()
	if string(f) != "1\nfield 39\nport number 1 1 4 14\nPortvalue - 4\n8080" {
		t.Fatalf("%q", f)
	}
	d, _ := (&Patch{}).Doc("# Hi\n").Base("/p").Follow(3).Bytes()
	if string(d) != "3\ndoc 5\n# Hi\nbase 2\n/pfollow 3 0\n" {
		t.Fatalf("%q", d)
	}
	s := QuickPick("Branch").Step(1, 2).Items(NewQuickItem("main", "main").Desc("current")).Bytes()
	if string(s) != "title 6\nBranchmode pick 0\nstep 1 2 0\nitems 1 34\n4 4 14 item\nmainmaindesc 7\ncurrent" {
		t.Fatalf("%q", s)
	}
	ev, ok := parseQuickEvent(Event{Name: "quick", Args: []string{"3", "accept", "5", "2", "9"}, Body: []byte("a\nb\nxyz")})
	if !ok || ev.Q != 3 || ev.Seq != 5 || len(ev.IDs) != 2 || ev.IDs[1] != "b" || ev.Text != "xyz" {
		t.Fatalf("%+v", ev)
	}
	_, r, ok := parseRetainedEvent(Event{Name: "view", Args: []string{"4", "change", "2", "port", "7", "4"}, Body: []byte("8081")})
	if !ok || r.Key != "port" || r.Seq != 7 || string(r.Body) != "8081" {
		t.Fatalf("%+v", r)
	}
	_, r, ok = parseRetainedEvent(Event{Name: "view", Args: []string{"4", "sort", "2", "3", "desc"}})
	if !ok || r.Col != 3 || !r.Desc {
		t.Fatalf("%+v", r)
	}
}
