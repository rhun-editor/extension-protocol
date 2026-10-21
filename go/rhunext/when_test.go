package rhunext

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The shared vectors (v1.1/when-vectors.txt): every clause compiles but the "error N" ones, which
// fail at byte N.
func TestWhenVectors(t *testing.T) {
	f, err := os.Open("../../v1.1/when-vectors.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		res, clause, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("bad vector %q", line)
		}
		n++
		e := WhenCheck(clause)
		if at, isErr := strings.CutPrefix(res, "error "); isErr {
			want, _ := strconv.Atoi(at)
			if e == nil || e.Offset != want {
				t.Errorf("%q: %v, want an error at byte %d", clause, e, want)
			}
		} else if e != nil {
			t.Errorf("%q: %v", clause, e)
		}
	}
	if n < 50 {
		t.Fatalf("only %d vectors", n)
	}
	if e := WhenCheck(strings.Repeat("a && ", 200)); e == nil {
		t.Fatal("too long accepted")
	}
}
