package report

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

const schemaBase = `CREATE TABLE users (
  id bigserial PRIMARY KEY,
  email text NOT NULL UNIQUE
);
CREATE TABLE orders (
  id bigserial PRIMARY KEY,
  user_id bigint REFERENCES users(id),
  total numeric(10, 2),
  note text, -- free text
  legacy_code text
);
CREATE INDEX orders_note_idx ON orders (note);
-- +goose Down
DROP TABLE orders;
`

const schemaPR = `-- +goose Up
ALTER TABLE orders ADD COLUMN status text NOT NULL DEFAULT 'open';
ALTER TABLE orders ALTER COLUMN total TYPE bigint, ALTER COLUMN total SET NOT NULL;
ALTER TABLE orders RENAME COLUMN note TO memo;
ALTER TABLE "public"."orders" DROP COLUMN IF EXISTS legacy_code;
CREATE TABLE refunds (
  id bigserial PRIMARY KEY,
  order_id bigint NOT NULL,
  reason text DEFAULT 'a; b',
  CONSTRAINT refunds_order_fk FOREIGN KEY (order_id) REFERENCES orders (id)
);
CREATE UNIQUE INDEX refunds_order_idx ON refunds (order_id);
DROP INDEX orders_note_idx;
DROP TABLE users;
-- +goose Down
DROP TABLE refunds;
`

// show is a table as one line per column and index: what the page shows of it.
func show(ts []Table) string {
	var b strings.Builder
	for _, t := range ts {
		fmt.Fprintf(&b, "%s %s base=%v %s:%d\n", t.Name, t.Kind, t.Base, t.Path, t.Line)
		for _, c := range t.Cols {
			fmt.Fprintf(&b, "  %s %q %s was=%q ref=%s %v @%d", c.Name, c.Type, c.State, c.Was, c.Ref, c.Notes, c.Line)
			if c.OnDelete != "" || c.OnUpdate != "" {
				fmt.Fprintf(&b, " delete=%s update=%s", c.OnDelete, c.OnUpdate)
			}
			if c.Guess {
				b.WriteString(" by name")
			}
			if c.Part {
				b.WriteString(" part")
			}
			b.WriteByte('\n')
		}
		for _, ix := range t.Indexes {
			fmt.Fprintf(&b, "  index %s (%s) unique=%v %s @%d\n", ix.Name, ix.Cols, ix.Unique, ix.State, ix.Line)
		}
		if len(t.Joins) > 0 {
			fmt.Fprintf(&b, "  joins: %s\n", strings.Join(t.Joins, ", "))
		}
		for _, r := range t.Risks {
			fmt.Fprintf(&b, "  risk: %s\n", strings.ReplaceAll(r, "`", "'"))
		}
	}
	return b.String()
}

func TestSchema(t *testing.T) {
	const p = "db/migrations/0008_status.sql"
	d := Data{Diff: newFile(p, schemaPR), Sources: map[string]string{p: schemaPR}}
	base := func(names []string) sdb {
		if !slices.Equal(names, []string{"orders", "refunds", "users"}) {
			t.Errorf("base asked for %v", names)
		}
		db := sdb{}
		db.read(schemaBase)
		return db
	}
	want := `orders changed base=true db/migrations/0008_status.sql:2
  id "bigserial"  was="" ref= [primary key] @0
  user_id "bigint"  was="" ref=users.id [] @0
  total "bigint" changed was="numeric(10, 2), nullable" ref= [not null] @3
  memo "text" renamed was="note" ref= [] @4
  legacy_code "text" dropped was="" ref= [] @5
  status "text" new was="" ref= [not null default 'open'] @2
  index orders_note_idx (note) unique=false dropped @13
  risk: Setting 'total' NOT NULL fails while any row holds a null.
refunds created base=false db/migrations/0008_status.sql:6
  id "bigserial"  was="" ref= [primary key] @6
  order_id "bigint"  was="" ref=orders.id [not null] @6
  reason "text"  was="" ref= [default 'a; b'] @6
  index refunds_order_idx (order_id) unique=true new @12
users dropped base=true db/migrations/0008_status.sql:14
  id "bigserial"  was="" ref= [primary key] @0
  email "text"  was="" ref= [not null unique] @0
  risk: 'orders.user_id' still references it.
`
	if got := show(schema(d, base)); got != want {
		t.Errorf("schema:\n%s\nwant:\n%s", got, want)
	}

	// without the base: only what the PR's statements say
	want = `orders changed base=false db/migrations/0008_status.sql:2
  status "text" new was="" ref= [not null default 'open'] @2
  total "bigint" changed was="" ref= [not null] @3
  memo "" renamed was="note" ref= [] @4
  legacy_code "" dropped was="" ref= [] @5
refunds created base=false db/migrations/0008_status.sql:6
  id "bigserial"  was="" ref= [primary key] @6
  order_id "bigint"  was="" ref=orders.id [not null] @6
  reason "text"  was="" ref= [default 'a; b'] @6
  index refunds_order_idx (order_id) unique=true new @12
users dropped base=false db/migrations/0008_status.sql:14
`
	if got := show(schema(d, nil)); got != want {
		t.Errorf("schema without a base:\n%s\nwant:\n%s", got, want)
	}

	// MySQL's forms, and SQL that isn't a table's
	const q = "db/migrations/0009_mysql.sql"
	my := "ALTER TABLE `orders` MODIFY COLUMN `total` decimal(12,2) NOT NULL, CHANGE `note` `memo` varchar(255);\n" +
		"CREATE FUNCTION f() RETURNS trigger AS $$ BEGIN UPDATE orders SET x = 1; END; $$ LANGUAGE plpgsql;\nSELECT 1;\n"
	d = Data{Diff: newFile(q, my), Sources: map[string]string{q: my}}
	want = `orders changed base=true db/migrations/0009_mysql.sql:1
  id "bigserial"  was="" ref= [primary key] @0
  user_id "bigint"  was="" ref=users.id [] @0
  total "decimal(12,2)" changed was="numeric(10, 2), nullable" ref= [not null] @1
  memo "varchar(255)" renamed was="note" ref= [] @1
  legacy_code "text"  was="" ref= [] @0
  risk: Setting 'total' NOT NULL fails while any row holds a null.
users context base=true :0
  id "bigserial"  was="" ref= [primary key] @0
`
	if got := show(schema(d, func([]string) sdb { db := sdb{}; db.read(schemaBase); return db })); got != want {
		t.Errorf("schema, MySQL:\n%s\nwant:\n%s", got, want)
	}

	// keys: their actions, what they reach outside the PR, a key by name only, and what the statements may break
	const k = "db/migrations/0010_keys.sql"
	keys := "ALTER TABLE orders ADD COLUMN created_by integer REFERENCES users (id) ON DELETE SET NULL ON UPDATE CASCADE;\n" +
		"ALTER TABLE orders ADD COLUMN region_id bigint NOT NULL;\n" +
		"DROP TABLE carts;\n" +
		"CREATE TABLE user_regions (user_id bigint REFERENCES users (id), region_id bigint NOT NULL REFERENCES regions (id), PRIMARY KEY (user_id, region_id));\n"
	other := "CREATE TABLE regions (id bigint PRIMARY KEY);\nCREATE TABLE cart_items (id bigint, cart_id bigint REFERENCES carts(id));\n"
	asked := [][]string{}
	d = Data{Diff: newFile(k, keys), Sources: map[string]string{k: keys}}
	want = `orders changed base=true db/migrations/0010_keys.sql:1
  id "bigserial"  was="" ref= [primary key] @0
  user_id "bigint"  was="" ref=users.id [] @0
  total "numeric(10, 2)"  was="" ref= [] @0
  note "text"  was="" ref= [] @0
  legacy_code "text"  was="" ref= [] @0
  created_by "integer" new was="" ref=users.id [] @1 delete=set null update=cascade
  region_id "bigint" new was="" ref=regions.id [not null] @2 by name
  risk: 'created_by' is integer, 'users.id' is bigserial: the key compares across types.
  risk: No index on 'created_by': Postgres doesn't create one for a foreign key.
  risk: 'region_id' is NOT NULL with no default: adding it fails while 'orders' has rows.
carts dropped base=false db/migrations/0010_keys.sql:3
  risk: 'cart_items.cart_id' still references it.
user_regions created base=false db/migrations/0010_keys.sql:4
  user_id "bigint"  was="" ref=users.id [primary key] @4 part
  region_id "bigint"  was="" ref=regions.id [primary key] @4 part
  joins: users, regions
  risk: No index on 'region_id': Postgres doesn't create one for a foreign key.
users context base=true :0
  id "bigserial"  was="" ref= [primary key] @0
regions context base=true :0
  id "bigint"  was="" ref= [primary key] @0
cart_items context base=true :0
  cart_id "bigint"  was="" ref=carts.id [] @0
`
	got := show(schema(d, func(names []string) sdb {
		asked = append(asked, names)
		db := sdb{}
		db.read(schemaBase)
		if len(asked) > 1 {
			db.read(other)
		}
		return db
	}))
	if got != want {
		t.Errorf("schema, keys:\n%s\nwant:\n%s", got, want)
	}
	if !slices.Equal(asked[len(asked)-1], []string{"region", "regiones", "regions"}) {
		t.Errorf("second base asked for %v", asked)
	}

	// a column added inside a CREATE TABLE the PR doesn't add: the statement whole, at the added line
	const n = "db/schema.sql"
	notes := "CREATE TABLE notes (\n  id bigserial PRIMARY KEY,\n  body text,\n  author_id bigint REFERENCES users (id)\n);\n"
	edit := "diff --git a/" + n + " b/" + n + "\n--- a/" + n + "\n+++ b/" + n + "\n@@ -1,4 +1,5 @@\n CREATE TABLE notes (\n" +
		"   id bigserial PRIMARY KEY,\n   body text,\n+  author_id bigint REFERENCES users (id)\n );\n"
	d = Data{Diff: edit, Sources: map[string]string{n: notes}}
	want = `notes changed base=true db/schema.sql:4
  id "bigserial"  was="" ref= [primary key] @0
  body "text"  was="" ref= [] @0
  author_id "bigint" new was="" ref=users.id [] @4
  risk: No index on 'author_id': Postgres doesn't create one for a foreign key.
users context base=true :0
  id "bigserial"  was="" ref= [primary key] @0
`
	got = show(schema(d, func([]string) sdb {
		db := sdb{}
		db.read("CREATE TABLE notes (\n  id bigserial PRIMARY KEY,\n  body text\n);\n" + schemaBase)
		return db
	}))
	if got != want {
		t.Errorf("schema, a column added inside a CREATE TABLE:\n%s\nwant:\n%s", got, want)
	}
}

func TestSchemaEdits(t *testing.T) {
	const n = "db/schema.sql"
	edit := func(hunk string) string {
		return "diff --git a/" + n + " b/" + n + "\n--- a/" + n + "\n+++ b/" + n + "\n" + hunk
	}
	const notes = "CREATE TABLE IF NOT EXISTS notes (\n  id bigserial PRIMARY KEY,\n  body text,\n  legacy text\n);\n"
	cases := []struct {
		name, diff, src, base, want string
	}{{
		name: "a column removed from a CREATE TABLE, nothing added",
		diff: edit("@@ -1,5 +1,4 @@\n CREATE TABLE IF NOT EXISTS notes (\n   id bigserial PRIMARY KEY,\n-  body text,\n-  legacy text\n+  body text\n );\n"),
		src:  "CREATE TABLE IF NOT EXISTS notes (\n  id bigserial PRIMARY KEY,\n  body text\n);\n",
		base: notes,
		want: `notes changed base=true db/schema.sql:3
  id "bigserial"  was="" ref= [primary key] @0
  body "text"  was="" ref= [] @0
  legacy "text" dropped was="" ref= [] @3
`,
	}, {
		name: "a line removed inside a CREATE TABLE only",
		diff: edit("@@ -2,4 +2,3 @@\n   id bigserial PRIMARY KEY,\n   body text,\n-  legacy text,\n );\n"),
		src:  "CREATE TABLE IF NOT EXISTS notes (\n  id bigserial PRIMARY KEY,\n  body text,\n);\n",
		base: notes,
		want: `notes changed base=true db/schema.sql:4
  id "bigserial"  was="" ref= [primary key] @0
  body "text"  was="" ref= [] @0
  legacy "text" dropped was="" ref= [] @4
`,
	}, {
		name: "a column added to a CREATE TABLE IF NOT EXISTS",
		diff: edit("@@ -3,3 +3,4 @@\n   body text,\n   legacy text\n+  , pinned boolean NOT NULL DEFAULT false\n );\n"),
		src:  "CREATE TABLE IF NOT EXISTS notes (\n  id bigserial PRIMARY KEY,\n  body text,\n  legacy text\n  , pinned boolean NOT NULL DEFAULT false\n);\n",
		base: notes,
		want: `notes changed base=true db/schema.sql:5
  id "bigserial"  was="" ref= [primary key] @0
  body "text"  was="" ref= [] @0
  legacy "text"  was="" ref= [] @0
  pinned "boolean" new was="" ref= [not null default false] @5
`,
	}, {
		name: "an index dropped alone",
		diff: newFile("db/migrations/0009_drop.sql", "DROP INDEX IF EXISTS orders_note_idx;\n"),
		base: schemaBase,
		want: `orders changed base=true db/migrations/0009_drop.sql:1
  id "bigserial"  was="" ref= [primary key] @0
  user_id "bigint"  was="" ref=users.id [] @0
  total "numeric(10, 2)"  was="" ref= [] @0
  note "text"  was="" ref= [] @0
  legacy_code "text"  was="" ref= [] @0
  index orders_note_idx (note) unique=false dropped @1
users context base=true :0
  id "bigserial"  was="" ref= [primary key] @0
`,
	}, {
		name: "constraints dropped by name, declared or default, after a rename",
		diff: newFile("db/migrations/0010_keys.sql", "ALTER TABLE orders RENAME COLUMN user_id TO account_id;\nALTER TABLE orders DROP CONSTRAINT orders_user_id_fkey;\n"+
			"ALTER TABLE users DROP CONSTRAINT IF EXISTS users_email_key;\nALTER TABLE refunds DROP CONSTRAINT refunds_order_fk;\n"),
		base: "/* the schema\n   so far */\nCREATE TABLE refunds (\n  id bigserial,\n  order_id bigint,\n" +
			"  CONSTRAINT refunds_order_fk FOREIGN KEY (order_id) REFERENCES orders (id)\n);\n" + schemaBase,
		want: `orders changed base=true db/migrations/0010_keys.sql:1
  id "bigserial"  was="" ref= [primary key] @0
  account_id "bigint" renamed was="user_id" ref= [] @2
  total "numeric(10, 2)"  was="" ref= [] @0
  note "text"  was="" ref= [] @0
  legacy_code "text"  was="" ref= [] @0
users changed base=true db/migrations/0010_keys.sql:3
  id "bigserial"  was="" ref= [primary key] @0
  email "text" changed was="unique" ref= [not null] @3
refunds changed base=true db/migrations/0010_keys.sql:4
  id "bigserial"  was="" ref= [] @0
  order_id "bigint" changed was="references orders.id" ref=orders.id [] @4 by name
`,
	}, {
		name: "an edited CREATE TABLE a later base migration altered",
		diff: edit("@@ -2,4 +2,5 @@\n   id bigserial PRIMARY KEY,\n-  body text,\n-  legacy text\n+  body char(9) NOT NULL,\n+  legacy int\n+  , pinned boolean, later bigint\n );\n"),
		src:  "CREATE TABLE IF NOT EXISTS notes (\n  id bigserial PRIMARY KEY,\n  body char(9) NOT NULL,\n  legacy int\n  , pinned boolean, later bigint\n);\n",
		base: notes + "ALTER TABLE notes ADD COLUMN later int;\nALTER TABLE notes ALTER COLUMN body TYPE varchar(80);\n",
		want: `notes changed base=true db/schema.sql:3
  id "bigserial"  was="" ref= [primary key] @0
  body "varchar(80)" changed was="nullable" ref= [not null] @3
  legacy "int" changed was="text" ref= [] @3
  later "int"  was="" ref= [] @0
  pinned "boolean" new was="" ref= [] @3
  risk: Setting 'body' NOT NULL fails while any row holds a null.
`,
	}, {
		name: "table constraints before the columns, a composite foreign key",
		diff: newFile("db/migrations/0011_lines.sql", "CREATE TABLE lines (\n  PRIMARY KEY (tenant_id, id),\n"+
			"  FOREIGN KEY (tenant_id, order_id) REFERENCES orders (tenant_id, id),\n  tenant_id bigint,\n  id bigint,\n  order_id bigint\n);\n"),
		base: schemaBase,
		want: `lines created base=false db/migrations/0011_lines.sql:1
  tenant_id "bigint"  was="" ref=orders.tenant_id [primary key] @1 part
  id "bigint"  was="" ref= [primary key] @1 part
  order_id "bigint"  was="" ref=orders.id [] @1
  risk: No index on 'order_id': Postgres doesn't create one for a foreign key.
orders context base=true :0
  id "bigserial"  was="" ref= [primary key] @0
`,
	}, {
		name: "a constraint dropped while another keeps the key",
		diff: newFile("db/migrations/0012_uq.sql", "ALTER TABLE users DROP CONSTRAINT users_email_key;\n"),
		base: "CREATE TABLE users (\n  id bigserial PRIMARY KEY,\n  email text NOT NULL UNIQUE,\n  CONSTRAINT users_email_uq UNIQUE (email)\n);\n",
		want: `users changed base=true db/migrations/0012_uq.sql:1
  id "bigserial"  was="" ref= [primary key] @0
  email "text"  was="" ref= [not null unique] @0
`,
	}}
	for _, c := range cases {
		d := Data{Diff: c.diff}
		if c.src != "" {
			d.Sources = map[string]string{n: c.src}
		}
		got := show(schema(d, func([]string) sdb {
			db := sdb{}
			db.read(c.base)
			return db
		}))
		if got != c.want {
			t.Errorf("%s:\n%s\nwant:\n%s", c.name, got, c.want)
		}
	}
}
