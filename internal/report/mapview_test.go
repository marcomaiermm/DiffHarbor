package report

import (
	"strings"
	"testing"
	"time"
)

// A system's name with hundreds of dots stays quick: a line past the width ends the search for that line.
func TestSysLinesLongName(t *testing.T) {
	name := strings.Repeat("a.", 400) + "com"
	done := make(chan []string, 1)
	go func() { done <- sysLines(name, 3, 13) }()
	select {
	case got := <-done:
		if len(got) != 3 {
			t.Errorf("%q, want 3 lines", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sysLines still running after 5s")
	}
}
