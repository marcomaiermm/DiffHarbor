// tsedges: what the TypeScript checker (TS >= 7.1's API) resolves in the project at dir that codegraph's index can't,
// as cgEdges, paths relative to dir. No list of frameworks or ORMs, only what a call's declaration and its name say:
//   calls    a call or a callback passed on (`xs.map(f)`) to a declaration of the project, by its type: `a.b().c()`
//   renders  a JSX element of a component of the project
//   handles  a function of the project passed to a package's call: a route, a procedure, a listener; an inline one
//            is a "route" itself, named by the call's string (`get("/x", …)`) or the key it's under (`list: q(…)`);
//            without either (`db.transaction(tx => …)`) it's part of the function it's in
//   reads, writes  a call to a package's method named like a read or a write (`findMany`, `insert`, …), to the store
//            the chain names: an argument of the project (a table), a string ("users", SQL's FROM), a property between
//            client and method (`prisma.user`), the receiver's type argument (`Repository<User>`); none: no edge.
//            A bare string only in a chain of calls: `_.get(o, "a.b")`, `createElement("div")` name no store
// Only edges whose either end is one of names. Test files aside. One checker round trip per file and one per kind.
const fs = require("fs");
const { dir, ts, names } = JSON.parse(fs.readFileSync(0, "utf8"));
const { API } = require(ts + "/dist/api/sync/api.js");
const is = require(ts + "/dist/ast/is.js");

const wanted = new Set(names);
const read = /^(find\w*|get\w*|select\w*|query\w*|count\w*|aggregate|exists|load\w*|fetch\w*|scan|search)$/;
const write = /^(insert\w*|create\w*|update\w*|upsert\w*|delete\w*|remove\w*|save\w*|set|put\w*|destroy|increment|decrement|truncate|bulk\w*|replace\w*|merge|persist|execute|exec|run)$/;
const test = /(^|\/)(__tests__|__mocks__|tests?|e2e|spec|fixtures?)\/|\.(test|spec|stories)\.[cm]?[jt]sx?$/;
const sql = /\b(select\b[\s\S]*?\bfrom|insert\s+into|update|delete\s+from|join)\s+["`]?([A-Za-z_][\w.]*)/i;

const api = new API({ cwd: dir });
let project, program, checker;
// projects is the tsconfig's projects and those it references, theirs too: a root tsconfig often only lists them
const projects = () => {
  const want = [dir + "/tsconfig.json"], known = new Set(want);
  for (let snap; ; ) {
    snap?.dispose();
    snap = api.createSnapshot({ openProjects: want });
    const n = want.length;
    for (const p of snap.getProjects())
      for (const r of p.parsedCommandLine.projectReferences || []) if (!known.has(r.path) && known.size < 64) known.add(r.path), want.push(r.path);
    if (want.length === n) return snap.getProjects();
  }
};
const root = dir.endsWith("/") ? dir : dir + "/";
const rel = p => (p.startsWith(root) && !p.includes("/node_modules/") ? p.slice(root.length) : "");
const pkg = p => { // the package a declaration is in; "" for the platform's (Array, Map, node:crypto) and the project's
  const i = p.lastIndexOf("/node_modules/");
  if (i < 0) return "";
  const parts = p.slice(i + 14).split("/"), name = parts[0].startsWith("@") ? parts[0] + "/" + parts[1] : parts[0];
  return name === "typescript" || name.startsWith("@typescript/") || name === "@types/node" ? "" : name.replace(/^@types\//, ""); // the platform's own
};
const lineOf = (node, sf) => sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1;
const endOf = (node, sf) => sf.getLineAndCharacterOfPosition(node.end).line + 1;
// resolve is getSymbolAtLocation with imports followed to what they import, in one round trip each
const Alias = 2097152;
const resolve = nodes => {
  const syms = checker.getSymbolAtLocation(nodes), al = [];
  syms.forEach((s, i) => s && s.flags & Alias && al.push(i));
  if (al.length) api.batch(...al.map(i => checker.getAliasedSymbol.gen(syms[i]))).forEach((s, j) => (syms[al[j]] = s));
  return syms;
};
const decls = new Map();
const decl = s => { // a symbol's declaration in the project: {name, path, line, fn}
  const h = s && (s.valueDeclaration || s.declarations?.[0]);
  const path = h && rel(h.path);
  if (!path) return null;
  const k = h.path + ":" + h.index;
  if (!decls.has(k)) decls.set(k, declOf(s, h, path));
  return decls.get(k);
};
const declOf = (s, h, path) => {
  const n = h.resolve(project), sf = n && n.getSourceFile();
  if (!n) return null;
  const fn = is.isFunctionDeclaration(n) || is.isMethodDeclaration(n) || is.isClassDeclaration(n) ||
    (is.isVariableDeclaration(n) && n.initializer && (is.isArrowFunction(n.initializer) || is.isFunctionExpression(n.initializer))) ||
    (is.isPropertyAssignment(n) && (is.isArrowFunction(n.initializer) || is.isFunctionExpression(n.initializer)));
  const up = k => { let p = n; for (let i = 0; i < k && p; i++) p = p.parent; return !!p && is.isSourceFile(p) };
  const top = up(1) || up(3); // a store: the module's, not a parameter
  return { name: s.name, path, line: lineOf(n, sf), fn, top };
};
const text = n => (n && (is.isIdentifier(n) || is.isStringLiteral(n) || is.isNoSubstitutionTemplateLiteral(n)) ? n.text : "");
function calleeName(c) { // the name a call calls: `m` of `a.m()` and `m()`
  if (is.isPropertyAccessExpression(c.expression)) return c.expression.name;
  if (is.isIdentifier(c.expression)) return c.expression;
  return null;
}
function declKind(n) {
  if (is.isMethodDeclaration(n)) return "method";
  if (is.isClassDeclaration(n)) return "class";
  return "function";
}
const isFn = n => n && (is.isArrowFunction(n) || is.isFunctionExpression(n));

const out = [], seen = new Set();
const emit = (kind, s, line, t) => {
  if (!s || !t || (!wanted.has(s.name) && !wanted.has(t.name))) return;
  const k = [kind, s.name, s.line, line, t.name, t.path, t.line].join("\0");
  if (seen.has(k)) return;
  seen.add(k);
  out.push({ Line: line, Kind: kind, By: "ts", SName: s.name, SKind: s.kind, SPath: s.path, SLine: s.line, SEnd: s.end,
    TName: t.name, TPath: t.path || "", TLine: t.line || 0 });
};

const done = new Set(); // a file in two projects: once
for (project of projects()) {
program = project.program, checker = project.checker;
decls.clear();
for (const file of program.getSourceFileNames()) {
  const path = rel(file);
  if (!path || test.test(path) || done.has(path)) continue;
  done.add(path);
  const sf = program.getSourceFile(file);
  if (!sf) continue;
  const ask = [], calls = [], refs = [], verbs = [], inlines = [];
  const need = n => ask.push(n) - 1;
  // scope: the declaration a node is in, as codegraph names it; an inline function passed to a call is one of its
  // own until we know whether the call is a package's (a route) or not (it's its parent's)
  const named = (n, name, kind, parent) => ({ name, kind, path, line: lineOf(n, sf), end: endOf(n, sf), parent });
  const eff = s => (s && s.inline && !s.route ? eff(s.parent) : s);
  const label = c => { // a route's name: the call's string, else the key it's under
    const lit = c.arguments.find(a => is.isStringLiteral(a) || is.isNoSubstitutionTemplateLiteral(a));
    if (lit) return lit.text;
    for (let p = c.parent; p && !is.isSourceFile(p) && !is.isBlock(p); p = p.parent)
      if (is.isPropertyAssignment(p) || (is.isVariableDeclaration(p) && is.isIdentifier(p.name))) return text(p.name);
    return "";
  };
  const routeName = c => ((calleeName(c)?.text || "") + " " + label(c)).trim(); // "get /orders", "query posts"
  const walk = (n, scope) => {
    let s = scope;
    if (is.isFunctionDeclaration(n) || is.isMethodDeclaration(n) || is.isClassDeclaration(n)) {
      if (n.name) s = named(n, text(n.name), declKind(n), scope);
    } else if (isFn(n)) {
      const p = n.parent;
      if (n.name) s = named(n, text(n.name), "function", scope);
      else if (is.isVariableDeclaration(p) && is.isIdentifier(p.name)) s = named(p, p.name.text, "function", scope);
      else if (is.isPropertyAssignment(p)) s = named(p, text(p.name), "function", scope);
      else if (is.isCallExpression(p) && p.arguments.includes(n)) {
        s = { ...named(n, "", "route", scope), inline: true, call: p };
        inlines.push(s);
      }
    }
    if (is.isCallExpression(n) || is.isNewExpression(n)) {
      const name = calleeName(n);
      const args = n.arguments || [];
      const viaPkg = args.some(isFn) || (args.some(a => is.isIdentifier(a)) && args.some(a => is.isStringLiteral(a)));
      if (name && (viaPkg || wanted.has(name.text) || wanted.has(eff(s)?.name))) calls.push({ n, at: need(name), scope: s });
      for (const a of args) if (is.isIdentifier(a) || is.isPropertyAccessExpression(a)) {
        const r = is.isIdentifier(a) ? a : a.name;
        if (viaPkg || wanted.has(r.text) || wanted.has(eff(s)?.name)) refs.push({ call: n, n: a, at: need(r), scope: s });
      }
      if (name && is.isPropertyAccessExpression(n.expression) && (read.test(name.text) || write.test(name.text)) && eff(s))
        verbs.push({ n, scope: s, at: need(name) });
    }
    if ((is.isJsxOpeningElement(n) || is.isJsxSelfClosingElement(n)) && is.isIdentifier(n.tagName) && /^[A-Z]/.test(n.tagName.text))
      calls.push({ n, at: need(n.tagName), scope: s, jsx: true });
    n.forEachChild(c => walk(c, s));
  };
  walk(sf, named(sf, path.split("/").pop(), "file", null));
  if (!ask.length) continue;
  const syms = resolve(ask);
  const ext = at => syms[at]?.declarations?.[0] && pkg(syms[at].declarations[0].path);
  const callAt = new Map(calls.map(c => [c.n, c.at]));
  for (const s of inlines) { // a package's call: its inline function is a route
    const at = callAt.get(s.call);
    // a callback with no name, a transaction's, a promise's, stays its parent's
    if (at !== undefined && ext(at) && label(s.call)) {
      s.route = true;
      s.name = routeName(s.call);
    }
  }
  const line = n => lineOf(n, sf);
  for (const c of calls) {
    const t = decl(syms[c.at]);
    if (t && (t.fn || c.jsx)) emit(c.jsx ? "renders" : "calls", eff(c.scope), line(c.n), t);
  }
  for (const r of refs) {
    const t = decl(syms[r.at]);
    if (!t || !t.fn) continue;
    const at = callAt.get(r.call);
    if (at !== undefined && ext(at)) { // a handler: from the route the call registers
      const c = r.call, l = line(c);
      emit("handles", { name: routeName(c), kind: "route", path, line: l, end: endOf(c, sf) }, l, t);
    } else emit("calls", eff(r.scope), line(r.n), t);
  }
  // reads and writes: one per chain, to the first store it names
  const chains = new Map();
  for (const v of verbs) {
    if (!ext(v.at)) continue;
    let top = v.n;
    while (is.isPropertyAccessExpression(top.parent) && is.isCallExpression(top.parent.parent)) top = top.parent.parent;
    const ch = chains.get(top) || { top, scope: v.scope, verbs: [], cands: [] };
    ch.verbs.push(calleeName(v.n).text);
    chains.set(top, ch);
  }
  if (!chains.size) continue;
  const ask2 = [];
  for (const ch of chains.values()) {
    for (let c = ch.top; c && (is.isCallExpression(c) || is.isPropertyAccessExpression(c)); c = c.expression) {
      if (is.isCallExpression(c)) for (const a of c.arguments) {
        let r = a;
        while (is.isPropertyAccessExpression(r) || is.isCallExpression(r)) r = r.expression;
        if (is.isIdentifier(r)) ch.cands.push({ sym: ask2.push(r) - 1 });
        else if (is.isStringLiteral(a) || is.isNoSubstitutionTemplateLiteral(a)) ch.cands.push({ str: a.text });
        else if (is.isTaggedTemplateExpression(a) || is.isTemplateExpression(a)) ch.cands.push({ str: sf.text.slice(a.pos, a.end) });
      } else if (!is.isCallExpression(c.parent)) ch.cands.push({ prop: c.name.text, sym: ask2.push(c.name) - 1 }); // prisma.user
    }
    ch.calls = 0;
    for (let c = ch.top; c && (is.isCallExpression(c) || is.isPropertyAccessExpression(c)); c = c.expression) if (is.isCallExpression(c)) ch.calls++;
    ch.cands.reverse(); // nearest the client first: insert(users) before .values(v)
    ch.recv = ch.top;
    while (is.isCallExpression(ch.recv) || is.isPropertyAccessExpression(ch.recv)) ch.recv = ch.recv.expression;
  }
  const syms2 = ask2.length ? resolve(ask2) : [];
  const typeless = [];
  for (const ch of chains.values()) {
    ch.store = null;
    for (const c of ch.cands) {
      if (c.str !== undefined) {
        const m = sql.exec(c.str);
        if (m) { ch.store = { name: m[2] }; if (/^(insert|update|delete)/i.test(m[1])) ch.sqlWrite = true; break }
        // a bare name: a builder's (knex("users").where(…), selectFrom("users")…), not createElement("div")
        if (ch.calls > 1 && /^[A-Za-z_][\w.]*$/.test(c.str) && c.str.length < 64) { ch.store = { name: c.str }; break }
        continue;
      }
      const s = syms2[c.sym], d = decl(s);
      if (c.prop) { if (s?.declarations?.[0] && pkg(s.declarations[0].path) && !write.test(c.prop) && !read.test(c.prop)) { ch.store = { name: c.prop }; break } continue }
      if (d && !d.fn && d.top) { ch.store = d; break }
    }
    if (!ch.store && ch.recv) typeless.push(ch);
  }
  if (typeless.length) { // the receiver's type argument: Repository<User>, Model<User>
    const types = checker.getTypeAtLocation(typeless.map(ch => ch.recv));
    typeless.forEach((ch, i) => {
      for (const a of safe(() => checker.getTypeArguments(types[i])) || []) {
        const d = decl(safe(() => a.getSymbol()));
        if (d && d.top) { ch.store = d; break }
      }
    });
  }
  for (const ch of chains.values()) {
    if (!ch.store) continue;
    const w = ch.sqlWrite || ch.verbs.some(v => write.test(v) && !/^(exec|execute|run)$/.test(v));
    emit(w ? "writes" : "reads", eff(ch.scope), line(ch.top), ch.store);
  }
}
}
function safe(f) { try { return f() } catch { return null } }
// exit once written: a pipe's write is async, and the checker's process would keep node alive
process.stdout.write(JSON.stringify(out), () => process.exit(0));
