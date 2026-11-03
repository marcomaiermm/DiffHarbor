package bench

import (
	"diffharbor/internal/review"
	"strings"
	"testing"
)

// Every bug credited once, a neighbour in the same file not, an accepted extra counted apart from noise.
func TestScore(t *testing.T) {
	bc := benchCase{
		Bugs: []bug{
			{ID: "B1", At: []spot{{"cache.go", [][2]int{{59, 59}}}}, Words: []string{"key"}},
			{ID: "B2", At: []spot{{"cache.go", [][2]int{{103, 103}}}}, Words: []string{"ttl", "expire"}},
		},
		Also: []bug{{ID: "tests", Words: []string{"test"}}},
	}
	found, also, unknown := bc.score([]review.Comment{
		{Path: "internal/cache.go", Line: 62, Title: "Cache key omits the dataset"},
		{Path: "internal/cache.go", Line: 60, Title: "Key collision again"},
		{Path: "internal/cache.go", Line: 89, Title: "Waiters hang on errors"},
		{Path: "internal/cache_test.go", Line: 1, Title: "No tests for eviction"},
	})
	if strings.Join(found, " ") != "B1" || also != 1 || len(unknown) != 1 || !strings.Contains(unknown[0], ":89 ") {
		t.Fatalf("found %v, also %d, unknown %q", found, also, unknown)
	}
}
