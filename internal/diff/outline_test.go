package diff

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestAnalyze(t *testing.T) {
	var d strings.Builder
	d.WriteString(`diff --git a/auth_test.go b/auth_test.go
--- a/auth_test.go
+++ b/auth_test.go
@@ -10,8 +10,10 @@ func TestLogin(t *testing.T) {
 	res := login(t, "bob")
-	if res.Code != 403 {
+	if res.Code != 200 {
 		t.Fatalf("code %d", res.Code)
 	}
-	require.Equal(t, "denied", res.Body)
 }
-func TestRefundTwice(t *testing.T) {
-}
+func TestLogout(t *testing.T) {
+	t.Skip("flaky")
+}
diff --git a/user.spec.ts b/user.spec.ts
--- a/user.spec.ts
+++ b/user.spec.ts
@@ -1,3 +1,4 @@
 describe("user", () => {
-  expect(invite(old)).toBe("expired");
+  expect(invite(old)).toBe("valid");
+  it("rejects a blocked user", () => {});
 });
diff --git a/old_test.py b/old_test.py
deleted file mode 100644
--- a/old_test.py
+++ /dev/null
@@ -1,2 +0,0 @@
-def test_gone():
-    assert True
diff --git a/a.go b/b.go
similarity index 100%
rename from a.go
rename to b.go
diff --git a/fmt.go b/fmt.go
--- a/fmt.go
+++ b/fmt.go
@@ -1 +1 @@
-x:=1
+x := 1
diff --git a/go.sum b/go.sum
--- a/go.sum
+++ b/go.sum
@@ -1 +1 @@
-a v1
+a v2
`)
	for i := range 3 {
		fmt.Fprintf(&d, "diff --git a/pkg%d/x.go b/pkg%d/x.go\n--- a/pkg%d/x.go\n+++ b/pkg%d/x.go\n@@ -1 +1 @@\n-import \"old/log\"\n+import \"new/log\"\n", i, i, i, i)
	}
	o := Analyze(d.String())
	moved := Analyze(`diff --git a/main_test.go b/main_test.go
deleted file mode 100644
--- a/main_test.go
+++ /dev/null
@@ -1,3 +0,0 @@
-func TestDiff(t *testing.T) {
-	require.Equal(t, 1, n)
-}
diff --git a/diff/diff_test.go b/diff/diff_test.go
new file mode 100644
--- /dev/null
+++ b/diff/diff_test.go
@@ -0,0 +1,3 @@
+func TestDiff(t *testing.T) {
+	require.Equal(t, 1, n)
+}
`)
	if len(moved.Added)+len(moved.Removed)+len(moved.Weakened) > 0 {
		t.Errorf("a test moved to another file: %+v", moved)
	}

	if want := []string{"auth_test.go: TestLogout", "user.spec.ts: rejects a blocked user"}; !slices.Equal(o.Added, want) {
		t.Errorf("added %q, want %q", o.Added, want)
	}
	if want := []string{"auth_test.go: TestRefundTwice", "old_test.py: test_gone"}; !slices.Equal(o.Removed, want) {
		t.Errorf("removed %q, want %q", o.Removed, want)
	}
	var got []string
	for _, f := range o.Weakened {
		got = append(got, f.At+" "+f.What)
	}
	for _, w := range []string{
		"auth_test.go:11 expectation", // 403 -> 200
		"auth_test.go:16 skip/only",
		"auth_test.go 1 assertions",   // require.Equal gone
		"user.spec.ts:2 expectation",  // "expired" -> "valid"
		"old_test.py 1 assertions",    // the whole file
		"old_test.py: test_gone test", // deleted
	} {
		if !slices.ContainsFunc(got, func(g string) bool { return strings.HasPrefix(g, w) }) {
			t.Errorf("weakened %q lacks %q", got, w)
		}
	}
	groups := map[string][]string{}
	for _, g := range o.Mechanical {
		groups[strings.Fields(g.What)[0]] = g.Files
	}
	if !slices.Equal(groups["Same"], []string{"pkg0/x.go", "pkg1/x.go", "pkg2/x.go"}) || !slices.Equal(groups["Moved"], []string{"a.go → b.go"}) ||
		!slices.Equal(groups["Whitespace"], []string{"fmt.go"}) || !slices.Equal(groups["Lockfiles"], []string{"go.sum"}) {
		t.Errorf("mechanical %v", o.Mechanical)
	}
	if w := "`import \"old/log\"` → `import \"new/log\"`"; !strings.Contains(o.Mechanical[0].What, w) {
		t.Errorf("same edit %q lacks %q", o.Mechanical[0].What, w)
	}
	if s := o.Skippable(); !s["b.go"] || !s["pkg1/x.go"] || s["auth_test.go"] {
		t.Errorf("skippable %v", s)
	}
}
