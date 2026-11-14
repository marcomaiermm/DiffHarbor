package report

import (
	"diffharbor/internal/checks"
	"diffharbor/internal/review"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The v4 page from hand-built Data: a large PR with a map, a tiny one without, a degraded one. REPORT_V4_OUT=<dir>
// writes the pages there, for a browser; without it the test checks their key markup.

func editFile(p string, old, now []string) string {
	s := "diff --git a/" + p + " b/" + p + "\n--- a/" + p + "\n+++ b/" + p + "\n@@ -1," + strconv.Itoa(len(old)+1) + " +1," + strconv.Itoa(len(now)+1) + " @@\n package x\n"
	for _, l := range old {
		s += "-" + l + "\n"
	}
	for _, l := range now {
		s += "+" + l + "\n"
	}
	return s
}

func largeData() Data {
	diffs := newFile("internal/payments/client.go", "package payments\n\nimport \"os\"\n\n// Charge sends a charge to the payment service.\nfunc Charge(id string) error {\n\turl := os.Getenv(\"PAYMENTS_API_URL\")\n\t_ = url\n\treturn nil\n}\n") +
		editFile("internal/orders/orders.go", []string{"func Place() {}"}, []string{"func Place(id string) error {", "\treturn payments.Charge(id)", "}"}) +
		goneFile("internal/orders/old.go") +
		editFile("internal/catalog/catalog.go", []string{"func Lookup() {}"}, []string{"func Lookup(id string) Item { return Item{} }"}) +
		editFile("internal/api/routes.go", []string{"// routes"}, []string{"func routes() { orders.Place(\"x\") }"}) +
		editFile("internal/util/strings.go", []string{"func Trim() {}"}, []string{"func Trim(s string) string { return s }"}) +
		newFile("db/migrations/0007_orders.sql", "CREATE TABLE orders (id text);\nDROP TABLE legacy_orders;\n") +
		editFile(".github/workflows/ci.yml", []string{"go: 1.24"}, []string{"go: 1.25"}) +
		editFile("internal/orders/orders_test.go", []string{"func TestPlace() {}"}, []string{"func TestPlace(t *testing.T) {}"}) +
		editFile("README.md", []string{"old"}, []string{"new"}) +
		goneFile("legacy/a.go") + goneFile("legacy/b.go")
	created := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	m := &Map{
		Parts: []MapPart{
			{ID: "internal/payments", Label: "payments", Path: "internal/payments", Files: []string{"internal/payments/client.go"}, New: true, Col: 1, Row: 0, Unread: 1},
			{ID: "internal/orders", Label: "orders", Path: "internal/orders", Files: []string{"internal/orders/orders.go", "internal/orders/old.go"}, Deleted: 1, Col: 2, Row: 0, Reach: 3, DB: true},
			{ID: "internal/catalog", Label: "catalog", Path: "internal/catalog", Files: []string{"internal/catalog/catalog.go"}, Col: 2, Row: 1, Reach: 1},
			{ID: "internal/api", Label: "api", Path: "internal/api", Files: []string{"internal/api/routes.go"}, Col: 3, Row: 0},
			{ID: "internal/util", Label: "util", Path: "internal/util", Files: []string{"internal/util/strings.go"}, Alone: true},
		},
		Order:   []string{"internal/payments", "internal/orders", "internal/catalog", "internal/api", "internal/util"},
		Removed: &MapBox{N: 2, Folders: []MapFolder{{Path: "legacy", N: 2}}},
		Lines: []MapLine{
			{From: "internal/payments", To: "internal/orders", Names: []MapName{{Name: "Charge", Kind: "calls", At: "internal/orders/orders.go:2", N: 3}}, Calls: 3, Weight: 3, Width: 2, Style: "solid", Chevron: "to"},
			{From: "internal/catalog", To: "internal/orders", Names: []MapName{{Name: "Lookup", Kind: "calls", At: "internal/orders/orders.go:3", N: 1}}, Calls: 1, Weight: 1, Width: 1, Style: "solid", Chevron: "to"},
			{From: "internal/orders", To: "internal/api", Names: []MapName{{Name: "Place", Kind: "calls", At: "internal/api/routes.go:2", N: 12, Writes: []string{"orders"}}}, Calls: 12, Weight: 12, Width: 3, Style: "solid", Chevron: "from"},
			{From: "internal/catalog", To: "internal/api", Names: []MapName{{Name: "Item", Kind: "named", At: "internal/api/routes.go:2", N: 2}}, Named: 2, Style: "dashed"},
			{From: "internal/payments", To: "internal/api", Names: []MapName{{Name: "Charger", Kind: "through", Via: "Charger", At: "internal/api/routes.go:2", N: 2}}, Through: 2, Weight: 2, Width: 1, Style: "dashed", Chevron: "to", Via: []string{"Charger"}},
		},
		Systems: []MapSystem{{Name: "PAYMENTS_API_URL", Parts: []string{"internal/payments"}, Calls: []OutsideCall{{System: "PAYMENTS_API_URL", Path: "internal/payments/client.go", Line: 7, URL: "PAYMENTS_API_URL", URLAt: "internal/payments/client.go:7"}}, Row: 0}},
		Deploy: []MapDeploy{
			{Kind: "migration", Label: "Migration 0007", Path: "db/migrations/0007_orders.sql", Drops: []string{"legacy_orders"}},
			{Kind: "environment", Label: "Environment"},
			{Kind: "ci", Label: "CI"},
		},
		Tables: []MapTable{{Name: "legacy_orders", Path: "db/migrations/0007_orders.sql", Line: 2, Parts: []MapTablePart{{Part: "internal/orders", Files: []string{"internal/orders/old.go"}}}, Tests: 2}},
		Stores: []MapStore{
			{
				Name: "orders", Path: "db/schema.go", Line: 12, Names: []MapStoreName{{Name: "o", At: "internal/orders/orders.go:2"}},
				Parts: []MapStorePart{
					{Part: "internal/orders", Uses: []MapStoreUse{{Kind: "writes", Decl: "Place", At: "internal/orders/orders.go:2", N: 2}, {Kind: "reads", Decl: "Place", At: "internal/orders/orders.go:2", N: 1}}},
					{Part: "internal/api", Uses: []MapStoreUse{{Kind: "reads", Decl: "Place", At: "internal/orders/orders.go:1", Through: true, N: 1}}},
				},
				Migrations: []MapStoreMigration{{Path: "db/migrations/0007_orders.sql", Label: "Migration 0007", Kind: "created", Lines: []int{1}}},
			},
			{Name: "items_view", Subquery: true, Parts: []MapStorePart{{Part: "internal/catalog", Uses: []MapStoreUse{{Kind: "reads", Decl: "Lookup", At: "internal/catalog/catalog.go:2", N: 1}}}}},
		},
		Reach: MapReach{Places: 4, Tests: 3, Decls: 2, List: []MapReachDecl{
			{Part: "internal/orders", Name: "Place", Path: "internal/orders/orders.go", Line: 2, N: 3, Places: []Ref{{Path: "cmd/shop/main.go", Line: 14}}},
			{Part: "internal/catalog", Name: "Lookup", Path: "internal/catalog/catalog.go", Line: 2, N: 1, Places: []Ref{{Path: "cmd/shop/main.go", Line: 20}}},
		}},
		Trace: &MapTrace{
			Method: "POST", Route: "/orders", At: ref{Path: "internal/api/routes.go", Line: 2},
			Rows: []wrow{
				{Name: "routes", Path: "internal/api/routes.go", Call: "routes", Tag: "changed", At: ref{Path: "internal/api/routes.go", Line: 2}},
				{Name: "Place", Path: "internal/orders/orders.go", Call: "Place(…)", Tag: "signature", At: ref{Path: "internal/api/routes.go", Line: 2}, D: 1},
				{Name: "Charge", Path: "internal/payments/client.go", Call: "Charge(…)", Tag: "new", At: ref{Path: "internal/orders/orders.go", Line: 2}, D: 2},
			},
			Call: OutsideCall{System: "PAYMENTS_API_URL", Path: "internal/payments/client.go", Line: 7}, Part: "internal/payments",
		},
		Cols: 3,
	}
	return Data{
		Repo: "acme/shop", Number: 42, Title: "Charge orders through the payment service", URL: "https://github.com/acme/shop/pull/42",
		SHA: "abcdef0123456789", Forge: Forge{Name: "GitHub", Ref: "#", Blob: "https://github.com/acme/shop/blob/abcdef0123456789/", Files: "https://github.com/acme/shop/pull/42/files"},
		PR:   PR{Created: created, Author: "dev-one", Head: "feat/charge", Base: "main", Commits: 6},
		Diff: diffs,
		Brief: []review.Sentence{
			{Text: "A new payments client charges each order through the payment service.", At: "internal/payments/client.go:6"},
			{Text: "Orders now call it when an order is placed.", At: "internal/orders/orders.go:2"},
		},
		Flow: []review.Step{
			{Title: "The payment client", Why: "Everything else calls it.", Check: "Charge returns the service's error.", Files: []string{"internal/payments/client.go"}},
			{Title: "Placing an order", Why: "Orders charge before they save.", Files: []string{"internal/orders/orders.go", "internal/api/routes.go"}},
		},
		Findings: []review.Comment{
			{Path: "internal/orders/orders.go", Line: 2, Severity: "major", Title: "A failed charge still saves the order", Body: "`Place` returns the charge's error, but the caller saves first. Charge before `save`.", Evidence: "agreed"},
			{Path: "internal/payments/client.go", Line: 7, Severity: "major", Title: "The URL is read without a default", Body: "An unset `PAYMENTS_API_URL` sends requests to an empty host."},
			{Path: "internal/catalog/catalog.go", Line: 2, Severity: "minor", Title: "Lookup ignores its id", Body: "The id is unused."},
			{Path: "db/migrations/0007_orders.sql", Line: 2, Severity: "major", Title: "The drop runs before the copy", Body: "`legacy_orders` is dropped before its rows move."},
		},
		Omitted:   []string{"internal/payments/client.go"},
		Map:       m,
		Knowledge: map[string]Know{"internal/orders/orders.go": {Days: 40, People: []string{"dev-one", "dev-two"}}, "internal/catalog/catalog.go": {Days: 120, People: []string{"dev-two"}}, "internal/api/routes.go": {Days: 12, People: []string{"dev-one"}}},
		Bot:       "shop-review",
	}
}

func tinyData() Data {
	return Data{
		Repo: "acme/tools", Number: 3, Title: "Trim the input", SHA: "1111111222222", URL: "https://github.com/acme/tools/pull/3",
		Forge: Forge{Name: "GitHub", Ref: "#"},
		Diff:  editFile("trim.go", []string{"func Trim() {}"}, []string{"func Trim(s string) string { return s }"}),
		Tests: checks.Tests{Ran: map[string]string{"TestTrim": "pass"}},
	}
}

func degradedData() Data {
	d := largeData()
	d.Number, d.Title = 43, "Charge orders, a review that ran short"
	d.Knowledge = nil
	d.Failed = []string{"internal/orders/orders.go"}
	d.Omitted = []string{"internal/payments/client.go", "internal/catalog/catalog.go"}
	m := *d.Map
	m.NoGraph, m.Lines, m.Systems, m.Trace, m.Stores = true, nil, nil, nil, nil
	parts := make([]MapPart, len(m.Parts))
	copy(parts, m.Parts)
	for i := range parts {
		parts[i].Alone = true
	}
	m.Parts = parts
	d.Map = &m
	d.Conflicts = true
	return d
}

func TestPageFixtures(t *testing.T) {
	out := os.Getenv("REPORT_V4_OUT")
	clock = func() time.Time { return time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC) }
	defer func() { clock = time.Now }()
	for _, c := range []struct {
		name string
		d    Data
		want []string
	}{
		{"large", largeData(), []string{
			`<h2 id="h-map" tabindex="-1">The change, by part</h2>`, `data-box="part:internal/payments"`, `data-box="system:PAYMENTS_API_URL"`,
			`<p class="mrl dp">Tables it writes</p>`, `<p class="mrl dp">Tables it only reads</p>`, `aria-label="items_view, a subquery`,
			"A subquery the project declares. The scan doesn&#39;t follow it to the tables it reads.",
			`<p class="mrl dp">Before merging</p>`, "Follow one path from", "Outside this PR, 4 places in code name 2 of the declarations it changes.",
			`id="before"`, "Migration 0007 drops 1 table", `id="finding-1"`, "A failed charge still saves the order",
			`id="part-internal-orders"`, "How it connects", "Called from", `Writes <code translate="no">orders</code>`,
			`id="covered"`, `id="team"`, "2 people committed to these files", "This page posts nothing.",
		}},
		{"tiny", tinyData(), []string{`id="read"`, "No open items", ">test run here<", `id="part-code"`}},
		{"degraded", degradedData(), []string{
			"Blocked: a conflict with main", "No code graph for this review, so the map has no lines.",
			"The review didn&#39;t read 3 code files", "No git history reached this review", "A review part failed: 1 code file",
		}},
	} {
		b, _, err := Render(c.d)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		s := string(b)
		for _, w := range c.want {
			if !strings.Contains(s, w) {
				t.Errorf("%s: %q missing", c.name, w)
			}
		}
		if c.name == "tiny" && strings.Contains(s, `id="map"`) {
			t.Error("tiny: a map with one part")
		}
		if out != "" {
			if err := os.WriteFile(filepath.Join(out, c.name+".html"), b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
