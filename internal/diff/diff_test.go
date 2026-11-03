package diff

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestDiff(t *testing.T) {
	file := func(p, hunk string) string {
		return "diff --git a/" + p + " b/" + p + "\nindex 1..2 100644\n--- a/" + p + "\n+++ b/" + p + "\n" + hunk
	}
	src := file("src/a.ts", "@@ -1,3 +10,4 @@ fn\n ctx\n-old\n+new\n+new2\n ctx2\n")
	diff := src + file("pnpm-lock.yaml", "@@ -1 +1 @@\n-a\n+b\n") +
		"diff --git a/gone.ts b/gone.ts\ndeleted file mode 100644\n--- a/gone.ts\n+++ /dev/null\n@@ -1 +0,0 @@\n-x\n"
	if got, _ := Filter(diff, nil, nil, 1); len(got) != 1 || got[0] != src+"\n# 1 file(s) deleted: gone.ts. Check that nothing still uses them.\n" {
		t.Fatalf("filterDiff: %q", got)
	}
	if got, _ := Filter(diff, map[string]bool{"other.ts": true}, nil, 1); got != nil {
		t.Fatalf("filterDiff only: %q", got)
	}
	if got, _ := Filter(file("big.ts", strings.Repeat("+x", Max)), nil, nil, 4); len(got) != 1 || !strings.HasPrefix(got[0], "\n# 1 more file(s) omitted") {
		t.Fatalf("filterDiff big: %q", got)
	}
	want := map[string]bool{"src/a.ts:10": true, "src/a.ts:11": true, "src/a.ts:12": true, "src/a.ts:13": true}
	if got := RightLines(src, true); !maps.Equal(got, want) {
		t.Fatalf("rightLines: %v", got)
	}
	if got := RightLines(src, false); !maps.Equal(got, map[string]bool{"src/a.ts:11": true, "src/a.ts:12": true}) {
		t.Fatalf("rightLines added: %v", got)
	}
	want["pnpm-lock.yaml:1"] = true // and nothing for the deleted gone.ts
	if got := RightLines(diff, true); !maps.Equal(got, want) {
		t.Fatalf("rightLines deleted file: %v", got)
	}
	if got := Paths(src); len(got) != 1 || got[0] != "src/a.ts" {
		t.Fatalf("paths: %v", got)
	}
}

func TestPatchIDs(t *testing.T) {
	reviewed := "diff --git a/a.go b/a.go\nindex 1..2 100644\n--- a/a.go\n+++ b/a.go\n@@ -3,3 +3,3 @@ func f() {\n ctx\n-old\n+new\n ctx\n" +
		"diff --git a/b.go b/b.go\nindex 3..4 100644\n--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n-x\n+y\n"
	// Rebased: main moved a.go's lines and context, b.go got a fix on top.
	rebased := "diff --git a/a.go b/a.go\nindex 7..8 100644\n--- a/a.go\n+++ b/a.go\n@@ -9,3 +9,3 @@ func g() {\n other\n-old\n+new\n other\n" +
		"diff --git a/b.go b/b.go\nindex 3..5 100644\n--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n-x\n+z\n"
	a, b := PatchIDs(reviewed), PatchIDs(rebased)
	if len(a) != 2 || a["a.go"] != b["a.go"] || a["b.go"] == b["b.go"] {
		t.Fatalf("patchIDs: %v %v", a, b)
	}
}

func TestSkipAndOmitted(t *testing.T) {
	for _, p := range []string{"api/x.pb.go", "src/routeTree.gen.ts", "web/__generated__/q.ts", ".terraform.lock.hcl", "Gemfile.lock"} {
		if !Skip.MatchString(p) {
			t.Errorf("not skipped: %s", p)
		}
	}
	if Skip.MatchString("src/generator.ts") {
		t.Error("skipped generator.ts")
	}
	file := func(p string) string {
		return "diff --git a/" + p + " b/" + p + "\n+++ b/" + p + "\n" + strings.Repeat("+x", Max/4) + "\n"
	}
	if got, n := Filter(file("a.ts")+file("b.ts")+file("c.ts"), nil, nil, 1); n != 2 || !strings.HasSuffix(got[0], "read them in the repo: b.ts, c.ts\n") {
		t.Fatalf("omitted: %q", got[0][max(0, len(got[0])-100):])
	}
	// code fills the budget first, docs last: the input order doesn't matter
	all := file("docs/a.md") + file("tests/c.test.ts") + file("drizzle/s.json") + file("src/b.ts")
	got, _ := Filter(all, nil, nil, 1)
	if !strings.HasPrefix(got[0], "diff --git a/src/b.ts") || !strings.HasSuffix(got[0], "read them in the repo: tests/c.test.ts, drizzle/s.json, docs/a.md\n") {
		t.Fatalf("order: %q … %q", got[0][:40], got[0][max(0, len(got[0])-100):])
	}
	// In parts nothing is left out, files of one directory share a part, each part keeps the diff's order.
	sized := func(p string, tenths int) string {
		return "diff --git a/" + p + " b/" + p + "\n+++ b/" + p + "\n" + strings.Repeat("x", Max*tenths/10-50) + "\n"
	}
	got, n := Filter(sized("a/1.ts", 4)+sized("b/1.ts", 7)+sized("b/2.ts", 2)+sized("a/2.ts", 1), nil, nil, 3)
	var paths []string
	for _, p := range got {
		paths = append(paths, strings.Join(Paths(p), " "))
	}
	if n != 0 || strings.Join(paths, ", ") != "a/1.ts a/2.ts, b/1.ts b/2.ts" {
		t.Fatalf("parts: %v, %d omitted", paths, n)
	}
	if Docs(all) != file("docs/a.md") {
		t.Fatalf("docs: %q", Docs(all))
	}
}

func TestOverlap(t *testing.T) {
	ours := OldRanges("diff --git a/x b/x\n@@ -10,7 +10,8 @@ func a\n ctx\n@@ -40 +41 @@\n-a\n+b\n")
	if want := [][2]int{{10, 17}, {40, 41}}; !slices.Equal(ours, want) {
		t.Fatalf("OldRanges = %v, want %v", ours, want)
	}
	for _, c := range []struct {
		patch string
		want  bool
	}{
		{"@@ -16,3 +16,4 @@\n", true},  // shares line 16
		{"@@ -17,3 +17,3 @@\n", false}, // starts where ours ends
		{"@@ -40,0 +41,2 @@\n", true},  // inserts at 40
		{"@@ -1,3 +1,3 @@\n", false},
	} {
		if got := Overlap(ours, OldRanges(c.patch)); got != c.want {
			t.Errorf("Overlap(%q) = %v, want %v", c.patch, got, c.want)
		}
	}
}
