package rhunext

import "testing"

func TestNoteBytes(t *testing.T) {
	n := NewNote("Reload the window?").ButtonCommand("Reload", "reload", nil).Button("Later")
	want := "18 46\nReload the window?button 6\nReloadcommand reload 0\nbutton 5\nLater"
	if got := string(n.Bytes()); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	n = NewNote("x").Detail("a\nb").Sticky().Button("1").Button("2").Button("3").Button("4")
	if got := string(n.Bytes()); got != "1 51\nxdetail 3\na bsticky 0\nbutton 1\n1button 1\n2button 1\n3" {
		t.Fatalf("got %q", got)
	}
}

func TestNoteEvents(t *testing.T) {
	ev, ok := parseNoteEvent(Event{Name: "notify", Args: []string{"4", "pressed", "2"}})
	if !ok || ev.Note != 4 || ev.Pressed != 2 || ev.Closed {
		t.Fatalf("%v %v", ev, ok)
	}
	ev, ok = parseNoteEvent(Event{Name: "notify", Args: []string{"4", "closed"}})
	if !ok || !ev.Closed {
		t.Fatalf("%v %v", ev, ok)
	}
	if p, ok := parseProgressCancel(Event{Name: "progress", Args: []string{"7", "cancel"}}); !ok || p != 7 {
		t.Fatalf("%v %v", p, ok)
	}
	if ProgressView(3) != "view:3" {
		t.Fatal(ProgressView(3))
	}
}

func TestLogPatch(t *testing.T) {
	b, err := NewPatch().Log(LogLine{LogInfo, "start"}, LogLine{LogError, "two\nlines"}).Clear().Bytes()
	if err != nil || string(b) != "2\nlog 2 27\ninfo start\nerror two lines\nclear 0\n" {
		t.Fatalf("%q %v", b, err)
	}
}

func TestPictureKeyboardEvents(t *testing.T) {
	_, ev, ok := parseViewEvent(Event{Name: "view", Args: []string{"4", "key", "ctrl+shift", "a"}})
	if !ok || ev.Kind != "key" || ev.Mods != "ctrl+shift" || ev.Key != "a" {
		t.Fatalf("%+v", ev)
	}
	_, ev, _ = parseViewEvent(Event{Name: "view", Args: []string{"4", "text", "2"}, Body: []byte("é")})
	if ev.Text != "é" {
		t.Fatalf("%+v", ev)
	}
	_, ev, _ = parseViewEvent(Event{Name: "view", Args: []string{"4", "click", "5", "6", "shift"}})
	if ev.X != 5 || ev.Mods != "shift" {
		t.Fatalf("%+v", ev)
	}
	if _, _, ok := parseRetainedEvent(Event{Name: "view", Args: []string{"4", "key", "-", "a"}}); ok {
		t.Fatal("key is a picture event")
	}
	if got := string((&Picture{}).Image(0, 0, 40, 40, 7).Bytes()); got != "image 0 0 40 40 7\n" {
		t.Fatal(got)
	}
}
