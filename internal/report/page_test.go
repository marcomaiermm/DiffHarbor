package report

import (
	"diffharbor/internal/checks"
	"diffharbor/internal/diff"
	"diffharbor/internal/review"
	"diffharbor/internal/ticket"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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
	d := Data{
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
		Knowledge: map[string]Know{"internal/orders/orders.go": {Days: 40, People: []string{"dev-one", "dev-two"}}, "internal/catalog/catalog.go": {Days: 120, People: []string{"dev-two"}}, "internal/api/routes.go": {Days: 12, People: []string{"dev-one"}}, "internal/util/strings.go": {Days: 400}},
		Bot:       "shop-review",
		// Phase 3: the tickets, the passes' terms, CI, the static reads
		Tickets: []ticket.Ticket{
			{Key: "SHOP-12", Title: "Charge orders before they save", State: "In Progress", Due: created.AddDate(0, 1, 20), Outcome: "Every placed order is charged once, before it is saved.", DoneWhen: []string{"A failed charge leaves no order.", "The payment URL comes from the environment."}, BlockedBy: []string{"SHOP-11"}, Read: created.AddDate(0, 1, 13)},
			{Key: "SHOP-11", Title: "Add a payments client", State: "Done", Done: true, DoneWhen: []string{"A client sends charges."}, Blocks: []string{"SHOP-12"}, Read: created.AddDate(0, 1, 13)},
		},
		FollowUps:  []ticket.Ticket{{Key: "SHOP-13", State: "Todo"}},
		TicketErrs: map[string]string{"SHOP-9": `Get "https://tracker.example.com/x": context deadline exceeded`},
		Split:      &Split{Tickets: []TicketStat{{Key: "SHOP-12", Commits: 4, Files: 3, Own: 2, Steps: 1}, {Key: "SHOP-11", Commits: 2, Files: 2, Own: 1, Steps: 1}, {Key: "SHOP-9", Commits: 1, Files: 1}}, Nameless: 1, Shared: 1},
		SplitOff:   true,
		Terms: review.Terms{
			Gates:  []review.Gate{{Quote: review.Quote{Text: "Do not merge before the payment service is live.", In: "description", Section: "Rollout"}, Name: "the payment service launch", Ticket: "SHOP-12", Waits: true}},
			Serves: []string{"The payment service accepts EUR charges only, as of 2026-11-01."},
			Order:  []string{"Run migration 0007 before the new image starts."},
			Contracts: []review.Contract{{
				Ticket: "SHOP-12", Evidence: review.CrossCheck,
				Rows: []review.Row{
					{Ask: "Outcome: Every placed order is charged once, before it is saved.", Status: review.Met, Evidence: review.CrossCheck, At: []string{"internal/orders/orders.go:2"}},
					{Ask: "Done when 1: A failed charge leaves no order.", Status: review.Deviates, Evidence: review.CrossCheck, At: []string{"internal/orders/orders.go:2-3", "internal/orders/orders_test.go:1"}, Note: "The caller saves before it charges."},
					{Ask: "Done when 2: The payment URL comes from the environment.", Status: review.MetInCode, Evidence: review.SearchAtHead, At: []string{"internal/payments/client.go:7"}, Docs: []string{"README.md:1"}},
					{Ask: "Beyond the ticket", Status: "Beyond the ticket", Evidence: review.CrossCheck, Note: "The catalog lookup takes an id."},
				},
				Changed: review.Answer{Text: "Orders charge through the new payments client when they are placed.", At: []string{"internal/orders/orders.go:2"}},
				Runs:    review.Answer{Text: "The order route, on every POST.", At: []string{"internal/api/routes.go:2"}},
				Relies:  review.Answer{Text: "`PAYMENTS_API_URL` set where the app runs."},
				First:   review.Answer{Text: "`orders.go:2`: the charge's error and the save's order.", At: []string{"internal/orders/orders.go:2"}},
			}},
			Differences: []review.Difference{
				{Ticket: "SHOP-12", Status: review.Deviates, Title: "A failed charge still leaves a saved order", Line: "SHOP-12 asks for no order after a failed charge; the caller saves first.", Asks: []review.Quote{{Text: "A failed charge leaves no order.", In: "SHOP-12", Section: "Done when 1"}}, Does: "`Place` charges after the caller saved the order.", At: "internal/orders/orders.go:2", Excerpt: "internal/orders/orders.go:1-3", Evidence: review.CrossCheck},
				{Status: review.Decided, Title: "Migration 0007 drops the legacy table", Line: "No ticket asks for it; the PR decides the legacy orders go.", Does: "The migration drops `legacy_orders`.", At: "db/migrations/0007_orders.sql:2", Excerpt: "db/migrations/0007_orders.sql:1-2", Evidence: review.Inferred},
			},
		},
		CI: &CI{
			Checks:    []Check{{Name: "test", Status: "completed", Conclusion: "failure", Workflow: "ci", Path: ".github/workflows/ci.yml", URL: "https://github.com/acme/shop/actions/runs/1"}, {Name: "lint", Status: "completed", Conclusion: "success", Workflow: "ci", Path: ".github/workflows/ci.yml"}},
			Workflows: []string{".github/workflows/ci.yml"}, Tests: "ci.yml", Edited: true,
		},
		Members: &Members{Logins: []string{"dev-one", "dev-two"}},
		Rules:   Rules{SHA: "0123456789ab", Files: []RuleFile{{Path: "AGENTS.md"}}},
		Parts:   2,
		TestChanges: TestChanges{
			With: []TestFile{{Path: "legacy/a_test.go", Tests: []TestCase{{Title: "TestLegacyCopy", Line: 4}}, Imports: "legacy/a.go"}, {Path: "legacy/c_test.go", Tests: []TestCase{{Title: "TestLegacyIDs", Line: 9}}, Names: []string{"legacyIDs"}}},
			Kept: []TestFile{{Path: "internal/orders/place_test.go", Tests: []TestCase{{Title: "TestPlaceSavesOnce", Line: 12}}, Names: []string{"Place"}, Found: []string{"internal/orders/orders.go:2"}}},
			Stay: []TestStay{{Path: "internal/orders/orders_test.go", Flags: []diff.Flag{{At: "internal/orders/orders_test.go: TestPlaceTwice", What: "test deleted"}, {At: "internal/orders/orders_test.go:9", What: "expectation changed: `want(1)` → `want(2)`"}}}},
		},
		Kinds: []FileKind{
			{Kind: "code", Files: []string{"internal/payments/client.go", "internal/orders/orders.go", "internal/catalog/catalog.go", "internal/api/routes.go", "internal/util/strings.go", "db/migrations/0007_orders.sql"}, Add: 16, Del: 4, Read: 5},
			{Kind: "tests", Files: []string{"internal/orders/orders_test.go"}, Add: 1, Del: 1, Read: 1},
			{Kind: "tooling", Files: []string{".github/workflows/ci.yml", "README.md"}, Add: 2, Del: 2, Read: 1},
			{Kind: "gone", Files: []string{"internal/orders/old.go", "legacy/a.go", "legacy/b.go"}, Del: 3, Deleted: 3, Named: 1},
		},
		Env:        map[string]EnvVar{"PAYMENTS_API_URL": {Reads: []string{"internal/payments/client.go:7"}}},
		Migrations: []Migration{{Path: "db/migrations/0007_orders.sql", Docs: []string{"README.md:1"}, Tests: []string{"internal/orders/orders_test.go:1"}}},
	}
	usesData(&d)
	return d
}

// prevData is the large PR's version before its last push: orders.go read otherwise, no difference yet, its findings one
// line down.
func prevData() Data {
	d := largeData()
	d.SHA = "9876543210fedcba"
	d.Diff = strings.Replace(d.Diff, "\treturn payments.Charge(id)", "\treturn nil", 1)
	d.Terms.Differences = d.Terms.Differences[1:]
	d.Findings = slices.Clone(d.Findings)
	for i := range d.Findings {
		d.Findings[i].Line++ // the push moved them: still the same findings
	}
	return d
}

// waitsData is a small PR that waits on the author's word for another ticket, with an open PR on the same lines.
func waitsData() Data {
	created := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	diffs := newFile("internal/catalog/price.go", "package catalog\n\n// Price is a list price.\ntype Price struct{ Cents int }\n") +
		newFile("internal/store/prices.go", "package store\n\nfunc RecordPrice(p catalog.Price) error {\n\treturn nil\n}\n") +
		editFile("cmd/ingest/main.go", []string{"func main() {}"}, []string{"func main() { store.RecordPrice(catalog.Price{}) }"}) +
		newFile("internal/store/migrations/0004_prices.sql", "CREATE TABLE prices (cents int);\n") +
		editFile("docs/ingest.md", []string{"old"}, []string{"new"})
	m := &Map{
		Parts: []MapPart{
			{ID: "internal/catalog", Label: "catalog", Path: "internal/catalog", Files: []string{"internal/catalog/price.go"}, New: true, Col: 1, Row: 0},
			{ID: "internal/store", Label: "store", Path: "internal/store", Files: []string{"internal/store/prices.go"}, New: true, Col: 2, Row: 0},
			{ID: "cmd/ingest", Label: "cmd/ingest", Path: "cmd/ingest", Files: []string{"cmd/ingest/main.go"}, Col: 3, Row: 0},
		},
		Order: []string{"internal/catalog", "internal/store", "cmd/ingest"},
		Lines: []MapLine{
			{From: "internal/catalog", To: "internal/store", Names: []MapName{{Name: "Price", Kind: "named", At: "internal/store/prices.go:3", N: 1}}, Named: 1, Style: "dashed"},
			{From: "internal/store", To: "cmd/ingest", Names: []MapName{{Name: "RecordPrice", Kind: "calls", At: "cmd/ingest/main.go:2", N: 1}}, Calls: 1, Weight: 1, Width: 1, Style: "solid", Chevron: "from"},
		},
		Deploy: []MapDeploy{{Kind: "migration", Label: "Migration 0004", Path: "internal/store/migrations/0004_prices.sql"}, {Kind: "prs", Label: "Open PRs"}},
		Cols:   3,
	}
	return Data{
		Repo: "acme/catalog", Number: 31, Title: "Track price lists per region (CAT-40)", URL: "https://github.com/acme/catalog/pull/31",
		SHA: "5e1f2a70000000", Forge: Forge{Name: "GitHub", Ref: "#", Blob: "https://github.com/acme/catalog/blob/5e1f2a7/"},
		PR:   PR{Created: created, Author: "dev-one", Head: "feat/cat-40-prices", Base: "main", Commits: 4},
		Diff: diffs, Map: m, Bot: "catalog-review",
		Brief:   []review.Sentence{{Text: "The ingest now records each new price list once.", At: "cmd/ingest/main.go:2"}},
		Others:  []Other{{Number: 29, Title: "feat: import price lists", Author: "dev-one", Draft: true, URL: "https://github.com/acme/catalog/pull/29", Files: []string{"cmd/ingest/main.go", "docs/ingest.md"}, Clash: []string{"cmd/ingest/main.go", "docs/ingest.md"}}},
		Tickets: []ticket.Ticket{{Key: "CAT-40", Title: "Track price lists per region", State: "In Review", Outcome: "Each region lists its prices.", DoneWhen: []string{"A new list adds one row."}, Read: created.AddDate(0, 0, 5)}},
		Split:   &Split{Tickets: []TicketStat{{Key: "CAT-40", Commits: 4, Files: 5, Own: 5}}},
		Terms: review.Terms{
			Gates: []review.Gate{{Quote: review.Quote{Text: "Uses 0004 so it lands after CAT-41's 0003. Merge after CAT-41.", In: "description"}, Name: "CAT-41 to merge", Ticket: "CAT-41", Waits: true}},
			Contracts: []review.Contract{{Ticket: "CAT-40", Evidence: review.CrossCheck, Rows: []review.Row{
				{Ask: "Outcome: Each region lists its prices.", Status: review.Met, Evidence: review.CrossCheck, At: []string{"internal/store/prices.go:3"}},
				{Ask: "Done when 1: A new list adds one row.", Status: review.Met, Evidence: review.CrossCheck, At: []string{"internal/store/prices.go:3", "internal/store/prices_test.go:8"}},
			}}},
			Differences: []review.Difference{{Ticket: "CAT-40", Status: review.Deviates, Title: "The lower price wins when a list comes back", Line: "CAT-40 asks for the latest price; the store keeps the lowest.", Asks: []review.Quote{{Text: "A new list adds one row.", In: "CAT-40", Section: "Done when 1"}}, Does: "`RecordPrice` keeps the lower price.", At: "internal/store/prices.go:3", Excerpt: "internal/store/prices.go:1-5", Evidence: review.CrossCheck}},
		},
		CI:    &CI{Workflows: []string{".github/workflows/deploy.yml"}, Tests: "deploy.yml"},
		Rules: Rules{SHA: "aaaabbbbcccc", Files: []RuleFile{{Path: "AGENTS.md"}}},
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
	d.Tickets, d.FollowUps = nil, nil // no ticket text was read
	d.Terms.Errs = []string{"gates: timed out", "contract: no JSON"}
	d.Terms.Gates, d.Terms.Contracts, d.Terms.Differences = nil, nil, nil
	d.CI = &CI{Err: "403 Resource not accessible by integration", Workflows: []string{".github/workflows/ci.yml"}, Tests: "ci.yml"}
	d.Members = &Members{Err: "403 Must have admin rights"}
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
			`<h2 id="h-stand" tabindex="-1">Blocked: test failed at abcdef0</h2>`, `<li data-rail-box="part:internal/orders">`,
			`aria-label="orders, 3 open items, 2 files,`, `<span class="says">Calls payments and catalog.`,
			// Phase 3
			"Also waits for the payment service launch.", `id="the-payment-service-launch-gate"`,
			"Clears when SHOP-12 is Done. DiffHarbor reads the tracker again on its next run: a run starts on a push, or when someone comments “@shop-review review” on the PR.",
			"SHOP-12, “Charge orders before they save”, is In Progress, due 2026-11-21. Read 2026-11-14.",
			`id="difference-1"`, "The ticket asks", "From SHOP-12, Done when 1", `<span class="ev">at abcdef0</span>`, "Open the run on GitHub",
			"This PR carries 3 tickets. The tracker says SHOP-11 blocks SHOP-12.", "Split suggestions are off for this repo",
			"Follow-ups the author names: SHOP-13 Todo", "Read from the tracker on 2026-11-14.", "The tracker didn&#39;t answer in time, so this ticket wasn&#39;t read.",
			"4 commits · 3 files, 2 only its own · in 1 of 2 steps · 2 asks", "1 file carries commits of two or more tickets",
			"Deleted with their code", "Tests deleted while the code they test stays", "Changed in files that stay", "Expectation changed",
			"Left to CI (ci.yml), which this PR edits", "This PR edits the CI that judges it", "Where they&#39;re read", "Docs and tests that name it",
			"What an outside service serves, in the author&#39;s words", "Run migration 0007 before the new image starts.",
			"as on main at 0123456, read whole.", "which ran in 2 parts", "· not run here", "12 files by kind",
			"No current member changed or reviewed util in 12 months.",
			// the uses page (uses spec): the data the script draws from, the traced path's items, the sheet
			`<script type="application/json" id="shapes">[{"p":"internal/api/routes.go","s":[1,1],"r":[{"k":"decl"`,
			`<script type="application/json" id="nodes">[{"n":"routes","k":"decl"`, `"ju":{"r":"call","p":"web/form.ts","l":30}`, `"e":{"k":"cycle","t":"submit"}`,
			`<ol class="pa" aria-label="Path from POST /orders to PAYMENTS_API_URL" data-items="[{&#34;t&#34;:&#34;s&#34;,&#34;n&#34;:&#34;/orders&#34;,&#34;k&#34;:&#34;route&#34;`,
			`&#34;r&#34;:&#34;request&#34;`, `<dialog id="uses" class="us" aria-labelledby="uses-title">`, "SHOP-12 In Progress · 2 differences", `data-k="[&#34;finding\tinternal/orders/orders.go: A failed charge still saves the order&#34;]"`,
		}},
		{"tiny", tinyData(), []string{`id="read"`, "No open items", ">test run here<", `id="part-code"`}},
		{"waits", waitsData(), []string{
			`<h2 id="h-stand" tabindex="-1">Waits for CAT-41 to merge</h2>`, "Waits · 4 open items", "CAT-41&#39;s state isn&#39;t on this page.",
			"Clears when a later run reads CAT-41 as Done, or the description no longer asks to wait.", "Go to the gate",
			"Clears when a later run reads CAT-41 as Done, or the description no longer asks to wait. A run starts on a push, or when someone comments “@catalog-review review” on the PR.",
			"#29 changes the same lines", `aria-expanded="true" aria-controls="ticket-cat-40-x"`, "Left to CI (deploy.yml)",
		}},
		{"degraded", degradedData(), []string{
			"Blocked: a conflict with main", "No code graph for this review, so the map has no lines.",
			"The review didn&#39;t read 3 code files", "No git history reached this review", "A review part failed: 1 code file",
			"The gate pass failed, so nothing checked whether the description or a ticket asks to wait.",
			"The contract pass failed, so nothing compared the code with what the tickets ask.",
			"Commits name 3 tickets: SHOP-12, SHOP-11 and SHOP-9. No ticket text was read.", "tickets named in commits", "None was read for this review",
			"No ticket was read, so nothing compared the code with what the tickets ask.", "The forge&#39;s checks weren&#39;t read for this review",
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
		// a raw error never reaches the page, nor a link html/template dropped
		for _, raw := range []string{"deadline exceeded", "tracker.example.com", "403", "ZgotmplZ"} {
			if strings.Contains(s, raw) {
				t.Errorf("%s: %q on the page", c.name, raw)
			}
		}
		if c.name == "tiny" && strings.Contains(s, `id="map"`) {
			t.Error("tiny: a map with one part")
		}
		if c.name == "waits" && strings.Contains(s, "Split suggestions are off") {
			t.Error("waits: split suggestions are on")
		}
		if out != "" {
			if err := os.WriteFile(filepath.Join(out, c.name+".html"), b, 0o600); err != nil {
				t.Fatal(err)
			}
			// the versions a server lists beside the page: the large PR has one before this one
			_, now, _ := Render(c.d)
			vs := []Version{now}
			if c.name == "large" {
				_, prev, _ := Render(prevData())
				now.At, prev.At = time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC), time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
				vs = []Version{now, prev}
			}
			j, _ := json.Marshal(vs)
			if err := os.WriteFile(filepath.Join(out, c.name+".versions.json"), j, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// The open items count each item once: a major finding on a deploy box is read in Before you merge, not in its part.
// A table keeps its migration's deploy box when selected (S57).
func TestPageOpen(t *testing.T) {
	v := prepare(largeData())
	v.build()
	p := v.Page
	if p.Open != 14 || countKind(p.Before, "Major finding") != 1 {
		t.Errorf("open %d, %d major findings before merging", p.Open, countKind(p.Before, "Major finding"))
	}
	for _, b := range p.Parts {
		for _, k := range b.Cards {
			if k.Box == "deploy:db/migrations/0007_orders.sql" {
				t.Errorf("%s: the migration's finding in a part too", b.Label)
			}
		}
	}
	mv := p.Map
	if tb, d := mv.byKey["table:orders"], mv.byKey["deploy:db/migrations/0007_orders.sql"]; !slices.Contains(tb.Sel, d.El) {
		t.Errorf("table orders keeps %q, not its migration's box %s", tb.Sel, d.El)
	}
	// without a map the open items cell names its kinds: here the one code file the review didn't read
	d := tinyData()
	d.Omitted = []string{"trim.go"}
	v = prepare(d)
	v.build()
	if i := slices.IndexFunc(v.Page.Status.Stats, func(s stat) bool { return s.Label == "open item" }); i < 0 || v.Page.Status.Stats[i].Second != "1 gap in the review" {
		t.Errorf("open items cell without a map: %+v", v.Page.Status.Stats)
	}
}

// Arrows point where data goes: Chevron "to" at To's end, "from" at From's; on one edge the line from higher up
// takes the higher port.
func TestRouteChevrons(t *testing.T) {
	v := &view{Data: Data{Map: &Map{
		Parts: []MapPart{
			{ID: "a", Label: "a", Col: 1, Row: 0}, {ID: "b", Label: "b", Col: 1, Row: 1}, {ID: "c", Label: "c", Col: 2, Row: 0},
		},
		Order: []string{"a", "b", "c"},
		Lines: []MapLine{
			{From: "a", To: "c", Calls: 1, Width: 1, Style: "solid", Chevron: "to"},
			{From: "b", To: "c", Calls: 1, Width: 1, Style: "solid", Chevron: "from"},
		},
	}}}
	v.mapView()
	mv := v.Page.Map
	a, b, c := mv.partBox["a"], mv.partBox["b"], mv.partBox["c"]
	tip := func(r *mroute) float64 { // the chevron's point: "M x y Lx y L x y"
		f := strings.Fields(strings.Split(r.Chev, " L")[1])
		x, _ := strconv.ParseFloat(f[0], 64)
		return x
	}
	var ac, bc *mroute
	for _, r := range mv.Lines {
		switch r.a {
		case a:
			ac = r
		case b:
			bc = r
		}
	}
	if got := tip(ac); got != float64(c.X-1) {
		t.Errorf("a to c: chevron at x %v, want at c's left edge %d", got, c.X-1)
	}
	if got := tip(bc); got != float64(b.X+b.W+1) {
		t.Errorf("b from c: chevron at x %v, want at b's right edge %d", got, b.X+b.W+1)
	}
	if ya, yb := ac.pts[len(ac.pts)-1][1], bc.pts[len(bc.pts)-1][1]; ya >= yb {
		t.Errorf("c's left edge: a's port at %v, b's at %v", ya, yb)
	}
}

// A line between columns two apart detours on another row only where no box sits at either end of it: at the end
// it would run into that box's ports and read as a line to it (#457: fetchApi to lib beside db to trpc).
func TestRouteDetourPassesNoBox(t *testing.T) {
	v := &view{Data: Data{Map: &Map{
		Parts: []MapPart{
			{ID: "db", Label: "db", Col: 1, Row: 0}, {ID: "lib", Label: "lib", Col: 2, Row: 0}, {ID: "trpc", Label: "trpc", Col: 2, Row: 1},
		},
		Order:   []string{"db", "lib", "trpc"},
		Systems: []MapSystem{{Name: "fetchApi", Parts: []string{"lib"}}},
		Lines: []MapLine{
			{From: "trpc", To: "db", Calls: 1, Width: 1, Style: "solid", Chevron: "from"},
			{From: "lib", To: "db", Calls: 1, Width: 1, Style: "solid", Chevron: "to"},
		},
	}}}
	v.mapView()
	mv := v.Page.Map
	trpc := mv.partBox["trpc"]
	for _, r := range mv.Lines {
		if r.Style != "dotted" {
			continue
		}
		for _, p := range r.pts {
			if p[1] >= float64(trpc.Y) && p[1] <= float64(trpc.Y+boxH) {
				t.Errorf("fetchApi to lib runs at y %v, in trpc's row: %v", p[1], r.pts)
			}
		}
	}
}

// A system's name breaks before "." and "_" on the fewest, most even lines, at most 3; a line still too long is cut.
func TestSysLines(t *testing.T) {
	for _, c := range []struct {
		name string
		want []string
	}{
		{"redis", []string{"redis"}},
		{"PAYMENTS_API_URL", []string{"PAYMENTS", "_API_URL"}},
		{"api.example.com", []string{"api.example", ".com"}},
		{"AVERYLONGSYSTEMNAME", []string{"AVERYLONGSYS…"}},
		{"aaaaaaaaaa.bbbbbbbbbb.cccccccccc.dddddddddd", []string{"aaaaaaaaaa", ".bbbbbbbbbb", ".cccccccccc.…"}},
	} {
		if got := sysLines(c.name, 3, 13); !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// The brief keeps a sentence only where its place is a line of the diff, without its own period before the place.
func TestBrief(t *testing.T) {
	v := prepare(Data{
		Forge: Forge{Blob: "https://example.com/blob/x/"},
		Diff:  editFile("a.go", []string{"func A() {}"}, []string{"func A(n int) {}"}),
		Brief: []review.Sentence{
			{Text: "A takes a number.", At: "a.go:2"},
			{Text: "B is outside the diff.", At: "b.go:3"},
			{Text: "C has no place.", At: ""},
		},
	})
	v.build()
	if b := v.Page.Brief; len(b) != 1 || b[0].Text != "A takes a number" || b[0].Place != "a.go:2" || b[0].Href != "#a.go%3A2" {
		t.Errorf("brief %+v", b)
	}
}

// md and wbr escape the PR's text, code spans too, and add only <wbr> after a slash and before a dot.
func TestMarkdown(t *testing.T) {
	got := string(md(`a < b & "c" ` + "`x<y>/z.go`"))
	want := `a &lt; b &amp; &#34;c&#34; <code class="cd" translate="no">x&lt;y&gt;/<wbr>z<wbr>.go</code>`
	if got != want {
		t.Errorf("md\n got %s\nwant %s", got, want)
	}
	if got := wbr(`a&"b/c.d<`); got != `a&amp;&#34;b/<wbr>c<wbr>.d&lt;` {
		t.Errorf("wbr %s", got)
	}
}

// A finding a test run showed is counted apart from those a second model checked.
func TestCoveredFindings(t *testing.T) {
	d := Data{Diff: editFile("a.go", []string{"func A() {}"}, []string{"func A(n int) {}"})}
	findings := func(cs ...review.Comment) []crow {
		d.Findings = cs
		v := prepare(d)
		v.build()
		return v.Page.Covered
	}
	line := func(rows []crow) []mline {
		for _, r := range rows {
			if r.Label == "Findings" {
				return r.Lines
			}
		}
		return nil
	}
	repro := review.Comment{Path: "a.go", Line: 2, Severity: "major", Title: "x", Evidence: review.Repro + "TestA fails"}
	agreed := review.Comment{Path: "a.go", Line: 2, Severity: "minor", Title: "y", Evidence: "agreed"}
	if l := line(findings(repro)); len(l) != 1 || l[0].Chip != evTest || l[0].Text != "1 major finding, none minor. A test run showed it." {
		t.Errorf("one run-backed finding: %+v", l)
	}
	l := line(findings(repro, agreed))
	if len(l) != 2 || l[0].Chip != evAI || l[0].Text != "2 findings: 1 major and 1 minor. A second model agreed with the other one, and neither ran the code." ||
		l[1] != (mline{Chip: evTest, Text: "1 shown by a test run."}) {
		t.Errorf("one of each: %+v", l)
	}
}

// The merge status takes one head by section 4's precedence: a conflict or a failed run blocks, a gate that waits
// makes the merge wait, else the open items. Minor findings never count, and the gap is one item however many files
// it holds.
func TestMergeStatus(t *testing.T) {
	gate := review.Gate{Quote: review.Quote{Text: "Merge after OPS-2.", In: "description"}, Name: "OPS-2 to merge", Ticket: "OPS-2", Waits: true}
	failed := &CI{Checks: []Check{{Name: "ci.yml", Status: "completed", Conclusion: "failure"}}, Workflows: []string{".github/workflows/ci.yml"}}
	for _, c := range []struct {
		name              string
		edit              func(*Data)
		kind, head, short string
		open              int
	}{
		{"none", func(*Data) {}, "none", "No open items", "No open items", 0},
		{"minors don't count", func(d *Data) {
			d.Findings = []review.Comment{{Path: "trim.go", Line: 2, Severity: "minor", Title: "m"}}
		}, "none", "No open items", "No open items", 0},
		{"the gap is one item", func(d *Data) {
			d.Diff += editFile("pad.go", []string{"a"}, []string{"b"})
			d.Failed, d.Omitted = []string{"trim.go"}, []string{"pad.go"}
		}, "open", "1 open item", "1 open item", 1},
		{"a gate that no longer waits", func(d *Data) {
			g := gate
			g.Waits = false
			d.Terms.Gates = []review.Gate{g}
		}, "none", "No open items", "No open items", 0},
		{"waits", func(d *Data) { d.Terms.Gates = []review.Gate{gate} }, "waits", "Waits for OPS-2 to merge", "Waits · 1 open item", 1},
		{"one gate quoted twice is one item", func(d *Data) { d.Terms.Gates = []review.Gate{gate, gate} }, "waits", "Waits for OPS-2 to merge", "Waits · 1 open item", 1},
		{"a failed check blocks a wait", func(d *Data) { d.Terms.Gates, d.CI = []review.Gate{gate}, failed }, "blocked", "Blocked: ci.yml failed at 1111111", "Blocked · ci.yml failed", 1},
		{"a conflict comes first", func(d *Data) { d.Terms.Gates, d.CI, d.Conflicts = []review.Gate{gate}, failed, true }, "blocked", "Blocked: a conflict with the base branch", "Blocked · conflict with the base branch", 1},
		{"a deviation is an item, no wait", func(d *Data) {
			d.Terms.Differences = []review.Difference{{Status: review.Deviates, Title: "x", At: "trim.go:1", Evidence: review.CrossCheck}}
		}, "open", "1 open item", "1 open item", 1},
	} {
		d := tinyData()
		d.Tests = checks.Tests{}
		c.edit(&d)
		v := prepare(d)
		v.build()
		st := v.Page.Status
		if st.Kind != c.kind || st.Head != c.head || st.Short != c.short || v.Page.Open != c.open {
			t.Errorf("%s: %s %q %q, %d open", c.name, st.Kind, st.Head, st.Short, v.Page.Open)
		}
	}
	// a failed contract pass isn't "No difference": the status says the comparison didn't run
	d := tinyData()
	d.Tests = checks.Tests{}
	d.Terms.Errs = []string{"contract: no JSON"}
	v := prepare(d)
	v.build()
	if st := v.Page.Status; st.Kind != "none" || strings.Contains(strings.Join(st.Note, " "), "No difference") ||
		!slices.ContainsFunc(st.Lines, func(l mline) bool { return strings.HasPrefix(l.Text, "The contract pass failed") }) {
		t.Errorf("contract failed: %+v", st)
	}
	// a wait shows its gate's link, and a blocked one says it also waits
	d = tinyData()
	d.Terms.Gates, d.CI = []review.Gate{gate}, failed
	v = prepare(d)
	v.build()
	if st := v.Page.Status; st.Gate != "#ops-2-to-merge-gate" || !slices.ContainsFunc(st.Lines, func(l mline) bool { return l.Text == "Also waits for OPS-2 to merge." }) {
		t.Errorf("%+v", st)
	}
}

// A test place reads "not run here" unless a run backs the test declared on its line: an unrelated run backs none.
func TestNotRun(t *testing.T) {
	notes := func(ran map[string]string) int {
		d := largeData()
		d.Sources = map[string]string{"internal/orders/orders_test.go": "func TestPlace(t *testing.T) {}\n"}
		d.Tests.Ran = ran
		b, _, err := Render(d)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(b), "· not run here")
	}
	// orders_test.go:1 is a contract row's place and the migration's test
	all, other, placed := notes(nil), notes(map[string]string{"TestOther": "pass"}), notes(map[string]string{"TestPlace": "pass"})
	if all < 3 || other != all || placed != all-3 {
		t.Fatalf("notes: %d none ran, %d another ran, %d TestPlace ran", all, other, placed)
	}
}

// usesData gives the large PR what the uses page draws (uses spec): the names the diff declares, their uses, the
// structures at those uses and the stations of their ways. An object-and-chain structure with two marked lines and
// folds, a Go if with its condition, a way up to a route and down to a table, one ending at a system, a cycle, a
// bound, a use drawn as its line alone, one found by name.
func usesData(d *Data) {
	d.Shapes = []Shape{
		{Path: "internal/api/routes.go", Span: [2]int{1, 1}, Rows: []ShapeRow{{Kind: "decl", Text: "func routes()", Line: 1}, {Kind: "call", Depth: 1, Text: `orders.Place("x")`, Line: 1, Use: true}}},
		{Path: "cmd/shop/main.go", Span: [2]int{10, 16}, Rows: []ShapeRow{
			{Kind: "decl", Text: "func main()", Line: 10},
			{Kind: "if", Depth: 1, Text: "if ok", Line: 12, Cond: &ShapeCond{Name: "ok", Line: 11, Text: "id, ok := parse(os.Args)"}},
			{Kind: "call", Depth: 2, Text: "orders.Place(id)", Line: 14, Use: true},
		}},
		{Path: "web/checkout.ts", Span: [2]int{10, 20}, Rows: []ShapeRow{
			{Kind: "decl", Text: "function submit(form)", Line: 10},
			{Kind: "object", Depth: 1, Text: "const req = {", Line: 11},
			{Kind: "call", Depth: 2, Text: "pay: Charge(form.total),", Line: 12, Use: true},
			{Kind: "fold", Depth: 2, N: 3, Kept: []ShapeLeaf{{Kind: "stmt", Text: `currency: "EUR",`, Line: 13}}, More: &[2]int{15, 16}},
			{Kind: "chain", Depth: 1, Text: "client", Line: 17},
			{Kind: "link", Depth: 2, Text: ".retry(3)", Line: 17},
			{Kind: "call", Depth: 2, Text: ".then(() => Charge(rest))", Line: 18, Use: true},
			{Kind: "levels", Depth: 2, N: 2, More: &[2]int{19, 20}},
		}},
		{Path: "internal/orders/orders.go", Span: [2]int{1, 3}, Rows: []ShapeRow{{Kind: "decl", Text: "func Place(id string) error", Line: 1}, {Kind: "call", Depth: 1, Text: "return payments.Charge(id)", Line: 2, Use: true}}},
		{Path: "internal/jobs/worker.go", Span: [2]int{4, 8}, Rows: []ShapeRow{
			{Kind: "decl", Text: "func run(ctx context.Context)", Line: 4},
			{Kind: "if", Depth: 1, Text: "if due", Line: 5, Cond: &ShapeCond{Name: "due", Line: 4, Text: "due := time.Now().After(next)"}},
			{Kind: "call", Depth: 2, Text: "payments.Charge(job.ID)", Line: 6, Use: true},
		}},
	}
	call := func(p string, l, sh int) *Joint { return &Joint{Rel: "call", Path: p, Line: l, Shape: sh} }
	d.Nodes = []Node{
		/* 1 */ {Name: "routes", Kind: "decl", Path: "internal/api/routes.go", Line: 1, State: "changed", Part: "api", Up: 4, JUp: &Joint{Path: "internal/api/routes.go", Line: 1}},
		/* 2 */ {Name: "Place", Kind: "decl", Path: "internal/orders/orders.go", Line: 1, State: "signature", Part: "orders", Sev: "major", Find: "#finding-1", Up: 1, JUp: call("internal/api/routes.go", 1, 1)},
		/* 3 */ {Name: "main", Kind: "decl", Path: "cmd/shop/main.go", Line: 10, State: "outside", End: &End{Kind: "main"}},
		/* 4 */ {Name: "/orders", Kind: "route", Method: "POST", Path: "internal/api/routes.go", Line: 1, Down: 1, JDown: &Joint{Path: "internal/api/routes.go", Line: 1}, End: &End{Kind: "route"}},
		/* 5 */ {Name: "Charge", Kind: "decl", Path: "internal/payments/client.go", Line: 6, State: "new", Part: "payments", Up: 2, JUp: call("internal/orders/orders.go", 2, 4), Down: 6, JDown: &Joint{Rel: "request", Path: "internal/payments/client.go", Line: 7}},
		/* 6 */ {Name: "PAYMENTS_API_URL", Kind: "system", Up: 5, JUp: &Joint{Rel: "request", Path: "internal/payments/client.go", Line: 7}, End: &End{Kind: "system"}},
		/* 7 */ {Name: "submit", Kind: "decl", Path: "web/checkout.ts", Line: 10, State: "outside", Up: 8, JUp: call("web/form.ts", 30, 0)},
		/* 8 */ {Name: "onSave", Kind: "decl", Path: "web/form.ts", Line: 28, State: "outside", Up: 7, JUp: call("web/checkout.ts", 19, 0), End: &End{Kind: "cycle", Text: "submit"}},
		/* 9 */ {Name: "orders", Kind: "table", Path: "db/schema.go", Line: 12, End: &End{Kind: "table"}},
		/* 10 */ {Name: "Save", Kind: "decl", Path: "internal/orders/store.go", Line: 5, State: "outside", Up: 2, JUp: call("internal/orders/orders.go", 2, 0), Down: 9, JDown: &Joint{Rel: "write", Path: "internal/orders/store.go", Line: 8}},
		/* 11 */ {Name: "run", Kind: "decl", Path: "internal/jobs/worker.go", Line: 4, State: "outside", Up: 12, JUp: call("internal/jobs/loop.go", 6, 0)},
		/* 12 */ {Name: "loop", Kind: "decl", Path: "internal/jobs/loop.go", Line: 3, State: "outside", Up: 16, JUp: call("internal/jobs/sched.go", 9, 0)},
		/* 13 */ {Name: "notify", Kind: "decl", Path: "hooks/notify.py", Line: 5, State: "outside", End: &End{Kind: "value", Text: "notify.py:9"}},
		/* 14 */ {Name: "legacy", Kind: "decl", Path: "vendor/legacy/x.go", Line: 2, State: "outside", End: &End{Kind: "nograph", Text: "vendor/legacy/x.go"}},
		/* 15 */ {Name: "show", Kind: "decl", Path: "internal/api/view.go", Line: 3, State: "outside", End: &End{Kind: "byname"}},
		/* 16 */ {Name: "sched", Kind: "decl", Path: "internal/jobs/sched.go", Line: 7, State: "outside", Up: 17, JUp: call("internal/jobs/boot.go", 4, 0)},
		/* 17 */ {Name: "boot", Kind: "decl", Path: "internal/jobs/boot.go", Line: 2, State: "outside", Up: 18, JUp: call("internal/jobs/start.go", 3, 0)},
		/* 18 */ {Name: "start", Kind: "decl", Path: "internal/jobs/start.go", Line: 1, State: "outside", End: &End{Kind: "bound"}},
	}
	item := "type Item struct {\n\tID string\n\tName string\n\tPrice int\n\tCurrency string\n\tStock int\n\tTags []string\n\tCreated time.Time\n\tUpdated time.Time\n\tOwner string\n\tHidden bool\n\tNote string\n…"
	d.Symbols = map[string][]Symbol{
		"Place": {{
			Kind: "function", Pkg: "orders", Unit: "package", Sig: "func Place(id string) error", Doc: "Place charges an order and returns the charge's error.", Path: "internal/orders/orders.go", Line: 1,
			Exact: true, State: "signature", Sev: "major", Uses: 3, Files: 3, Reach: []int{5, 10},
			Refs: []Ref{
				{Path: "internal/api/routes.go", Line: 1, In: "routes", Text: `func routes() { orders.Place("x") }`, Shape: 1, Rel: "call", Up: 1},
				{Path: "cmd/shop/main.go", Line: 14, In: "main", Text: "orders.Place(id)", Shape: 2, Rel: "call", Up: 3},
				{Path: "internal/orders/orders_test.go", Line: 1, In: "TestPlace", Text: "Place(\"x\")", Test: true},
			},
			Ways: []Way{{Method: "POST", Route: "/orders", Path: "internal/api/routes.go", Line: 2, Chain: []Hop{{Name: "Place", Path: "internal/orders/orders.go", Line: 1, In: "internal/api/routes.go", At: 2, Shape: 1, Rel: "call"}}}},
		}},
		"Charge": {{
			Kind: "function", Pkg: "payments", Unit: "package", Sig: "func Charge(id string) error", Doc: "Charge sends a charge to the payment service.", Path: "internal/payments/client.go", Line: 6,
			Exact: true, State: "new", Uses: 9, Files: 7, Reach: []int{6},
			Refs: []Ref{
				{Path: "internal/orders/orders.go", Line: 2, In: "Place", Text: "return payments.Charge(id)", Shape: 4, Rel: "call", Up: 2},
				{Path: "web/checkout.ts", Line: 12, In: "submit", Text: "pay: Charge(form.total),", Shape: 3, Rel: "call", Up: 7},
				{Path: "web/checkout.ts", Line: 18, In: "submit", Text: ".then(() => Charge(rest))", Shape: 3, Rel: "call", Up: 7},
				{Path: "internal/api/routes.go", Line: 1, In: "routes", Text: `func routes() { orders.Place("x") }`, Rel: "iface", Via: "Charger", Up: 1},
				{Path: "internal/api/wire.go", Line: 5, In: "wire", Text: "var _ Charger = payments.Charge", Rel: "tie", Via: "Charger"},
				{Path: "internal/jobs/worker.go", Line: 6, In: "run", Text: "payments.Charge(job.ID)", Shape: 5, Rel: "call", Up: 11},
				{Path: "hooks/notify.py", Line: 7, In: "notify", Text: "client.Charge(order)", Up: 13},
				{Path: "vendor/legacy/x.go", Line: 3, In: "legacy", Text: "payments.Charge(old)", Up: 14},
				{Path: "internal/payments/client_test.go", Line: 4, In: "TestCharge", Text: "Charge(\"x\")", Test: true},
			},
		}},
		"Lookup": {{
			Kind: "function", Pkg: "catalog", Sig: "func Lookup(id string) Item", Path: "internal/catalog/catalog.go", Line: 1, Uses: 1, Files: 1,
			Refs: []Ref{{Path: "internal/api/view.go", Line: 3, In: "show", Text: "item := catalog.Lookup(id)", Up: 15}},
		}},
		"Item": {{Kind: "type", Pkg: "catalog", Sig: item, Path: "internal/catalog/item.go", Line: 3, Exact: true, Uses: 0}},
		"Trim": {{Kind: "function", Pkg: "util", Sig: "func Trim(s string) string", Path: "internal/util/strings.go", Line: 1, Exact: true}},
	}
}
