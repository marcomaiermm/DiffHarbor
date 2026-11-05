package assets

import (
	"bytes"
	"os"
	"testing"
)

// The docs site serves the lighthouse when the server has no public URL: both must show the same.
func TestReviewingMatchesDocs(t *testing.T) {
	docs, err := os.ReadFile("../../docs/public/reviewing.svg")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(docs, Reviewing) {
		t.Fatal("internal/assets/reviewing.svg and docs/public/reviewing.svg differ: copy one over the other")
	}
}
