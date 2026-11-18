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
  index orders_note_idx (memo) unique=false dropped @13
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
	}, {
		name: "guards, null defaults, unknown targets, update actions, self references, a table rename",
		diff: newFile("db/migrations/0013_mix.sql", "ALTER TABLE orders ADD COLUMN IF NOT EXISTS total text;\n"+
			"ALTER TABLE orders ADD COLUMN flag text NOT NULL DEFAULT NULL;\nALTER TABLE orders ADD COLUMN vendor_id bigint REFERENCES vendors (id);\n"+
			"ALTER TABLE employees DROP CONSTRAINT employees_manager_id_fkey, ADD CONSTRAINT employees_manager_fk FOREIGN KEY (manager_id) REFERENCES employees (id) ON UPDATE restrict;\n"+
			"ALTER TABLE employees DROP COLUMN id;\nALTER TABLE users RENAME TO accounts;\n"),
		base: "CREATE TABLE employees (\n  id bigserial PRIMARY KEY,\n  manager_id bigint REFERENCES employees (id) ON UPDATE cascade\n);\n" + schemaBase,
		want: `orders changed base=true db/migrations/0013_mix.sql:1
  id "bigserial"  was="" ref= [primary key] @0
  user_id "bigint" changed was="references users.id" ref=accounts.id [] @0
  total "numeric(10, 2)"  was="" ref= [] @0
  note "text"  was="" ref= [] @0
  legacy_code "text"  was="" ref= [] @0
  flag "text" new was="" ref= [not null default NULL] @2
  vendor_id "bigint" new was="" ref=vendors.id [] @3
  risk: 'flag' is NOT NULL with no default: adding it fails while 'orders' has rows.
  risk: No index on 'vendor_id': Postgres doesn't create one for a foreign key.
employees changed base=true db/migrations/0013_mix.sql:4
  id "bigserial" dropped was="" ref= [primary key] @5
  manager_id "bigint" changed was="on update cascade" ref=employees.id [] @4 delete= update=restrict
  risk: 'id' is dropped while 'employees.manager_id' still references it.
accounts changed base=true db/migrations/0013_mix.sql:6
  id "bigserial"  was="" ref= [primary key] @0
  email "text"  was="" ref= [not null unique] @0
  risk: Renamed from 'users': code and SQL that still name it break.
`,
	}, {
		name: "same-named keys elsewhere, a self-referencing table renamed, a new primary key, a referenced column renamed",
		diff: newFile("db/migrations/0014_keys.sql", "ALTER TABLE parent DROP COLUMN id;\nALTER TABLE nodes RENAME TO tree;\n"+
			"ALTER TABLE plain ADD COLUMN id integer PRIMARY KEY;\nALTER TABLE plain ADD COLUMN n bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY;\n"+
			"ALTER TABLE orders2 RENAME COLUMN oid TO order_id;\nALTER TABLE loose ADD PRIMARY KEY (k);\n"+
			"ALTER TABLE logs RENAME COLUMN uid TO user_id;\nALTER TABLE logs ADD FOREIGN KEY (user_id) REFERENCES users (id);\n"),
		base: "CREATE TABLE parent (\n  id bigint PRIMARY KEY\n);\nCREATE TABLE child (\n  id bigint REFERENCES parent (id)\n);\n" +
			"CREATE TABLE nodes (\n  id bigserial PRIMARY KEY,\n  up bigint REFERENCES nodes (id)\n);\nCREATE TABLE plain (\n  x int\n);\n" +
			"CREATE TABLE orders2 (\n  oid bigint PRIMARY KEY\n);\nCREATE TABLE inv (\n  o bigint REFERENCES orders2 (oid)\n);\n" +
			"CREATE TABLE kid (\n  pid bigint REFERENCES parent\n);\nCREATE TABLE loose (\n  k int\n);\n" +
			"CREATE TABLE logs (\n  uid bigint\n);\nCREATE INDEX logs_uid ON logs (uid);\n",
		want: `parent changed base=true db/migrations/0014_keys.sql:1
  id "bigint" dropped was="" ref= [primary key] @1
  risk: 'id' is dropped while 'child.id', 'kid.pid' still references it.
tree changed base=true db/migrations/0014_keys.sql:2
  id "bigserial"  was="" ref= [primary key] @0
  up "bigint" changed was="references nodes.id" ref=tree.id [] @0
  risk: Renamed from 'nodes': code and SQL that still name it break.
plain changed base=true db/migrations/0014_keys.sql:3
  x "int"  was="" ref= [] @0
  id "integer" new was="" ref= [primary key] @3
  n "bigint" new was="" ref= [primary key] @4
  risk: 'id' is NOT NULL with no default: adding it fails while 'plain' has rows.
orders2 changed base=true db/migrations/0014_keys.sql:5
  order_id "bigint" renamed was="oid" ref= [primary key] @5
loose changed base=true db/migrations/0014_keys.sql:6
  k "int" changed was="not unique" ref= [primary key] @6
  risk: Setting 'k' NOT NULL fails while any row holds a null.
logs changed base=true db/migrations/0014_keys.sql:7
  user_id "bigint" renamed was="uid" ref=users.id [] @8
child context base=true :0
  id "bigint"  was="" ref=parent.id [] @0
inv context base=true :0
  o "bigint"  was="" ref=orders2.order_id [] @0
kid context base=true :0
  pid "bigint"  was="" ref=parent [] @0
`,
	}, {
		name: "MySQL's ALTER TABLE DROP INDEX, an index dropped and re-created, DROP TABLE CASCADE",
		diff: newFile("db/migrations/0015_idx.sql", "ALTER TABLE logs2 DROP INDEX logs2_at;\nDROP INDEX orders_x;\n"+
			"CREATE INDEX orders_x ON orders (a, b);\nDROP TABLE parent CASCADE;\n"),
		base: "CREATE TABLE logs2 (\n  at int\n);\nCREATE INDEX logs2_at ON logs2 (at);\nCREATE TABLE orders (\n  a int,\n  b int\n);\n" +
			"CREATE INDEX orders_x ON orders (a);\nCREATE TABLE parent (\n  id bigint PRIMARY KEY\n);\n" +
			"CREATE TABLE child (\n  pid bigint REFERENCES parent (id)\n);\n",
		want: `logs2 changed base=true db/migrations/0015_idx.sql:1
  at "int"  was="" ref= [] @0
  index logs2_at (at) unique=false dropped @1
orders changed base=true db/migrations/0015_idx.sql:3
  a "int"  was="" ref= [] @0
  b "int"  was="" ref= [] @0
  index orders_x (a, b) unique=false new @3
parent dropped base=true db/migrations/0015_idx.sql:4
  id "bigint"  was="" ref= [primary key] @0
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

// TestDatabaseItems: only items in a migration's own file join the database section, not every item because a
// context card has no path.
func TestDatabaseItems(t *testing.T) {
	v := &view{}
	v.Schema = []Table{{Name: "orders", Path: "db/m/1.sql", Kind: "changed"}, {Name: "users", Kind: "context"}}
	v.found = []*card{{ID: "a", Place: "db/m/1.sql:3"}, {ID: "b", Place: "api/x.go:1"}, {ID: "c", Place: "db/m/1.sqlx:1"}}
	var got []string
	for _, it := range v.database().Items {
		got = append(got, it.Href)
	}
	if !slices.Equal(got, []string{"#a"}) {
		t.Errorf("items %v", got)
	}
}
