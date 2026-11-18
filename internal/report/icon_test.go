package report

import "testing"

func TestFileLook(t *testing.T) {
	for in, want := range map[string]string{
		"internal/orders/orders.go:2": "seti/go2", "internal/orders/orders_test.go": "flask-conical",
		"db/migrations/0007_orders.sql:2": "seti/db", "web/App.tsx": "seti/react", "config/app.yaml": "seti/yml",
		"docs/guide.md": "seti/markdown", "Dockerfile": "seti/docker", "Makefile": "seti/makefile", "a/b.unknown": "seti/default",
		"tsconfig.base.json": "seti/tsconfig", "3 files": "", "2 queries, and 1 call": "", "docs/release notes.md:4": "seti/markdown",
	} {
		if got := fileLook(in).Icon; got != want {
			t.Errorf("fileLook(%q) = %q, want %q", in, got, want)
		}
	}
	if kindLook("Major finding").Cls != "ik-major" || kindLook("Minor finding").Cls != "ik-off" {
		t.Error("kindLook: a major finding is ik-major, anything else ik-off")
	}
}

// TestLookIcons: every icon a look names is a file in icons/.
func TestLookIcons(t *testing.T) {
	ls := []look{fileLook("a_test.go"), fileLook("Dockerfile"), fileLook("Makefile"), fileLook("LICENSE"), fileLook("tsconfig.json"), fileLook("yarn.lock"), fileLook("x.unknown")}
	for _, l := range byExt {
		ls = append(ls, l)
	}
	for _, k := range []string{"Critical finding", "Major finding", "Difference", "Before merging", "Test change", "Gap in the review", "Minor finding"} {
		ls = append(ls, kindLook(k))
	}
	for _, l := range ls {
		if _, ok := icons[l.Icon]; !ok {
			t.Errorf("icon %q is not in icons/", l.Icon)
		}
	}
}
