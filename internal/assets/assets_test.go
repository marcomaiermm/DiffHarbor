package assets

import (
	"bytes"
	"io/fs"
	"os"
	"testing"
)

// The docs site serves the lighthouse when the server has no public URL: both must show the same.
func TestReviewingMatchesDocs(t *testing.T) {
	names, _ := fs.Glob(Reviewing, "*.svg")
	if len(names) != 3 {
		t.Fatalf("want reviewing, -light and -dark, got %v", names)
	}
	for _, name := range names {
		ours, _ := Reviewing.ReadFile(name)
		docs, err := os.ReadFile("../../docs/public/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(docs, ours) {
			t.Errorf("internal/assets/%s and docs/public/%[1]s differ: copy one over the other", name)
		}
	}
}
