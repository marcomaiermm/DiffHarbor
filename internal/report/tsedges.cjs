// tsedges: what the TypeScript checker (TS >= 7.1's API) resolves in the project at dir that codegraph's index can't,
// as cgEdges, paths relative to dir. No list of frameworks or ORMs, only what a call's declaration and its name say:
//   calls    a call or a callback passed on (`xs.map(f)`) to a declaration of the project, by its type: `a.b().c()`;
//            one to a member of an interface or a type literal with Via, that interface's or type's name
//   renders  a JSX element of a component of the project
//   handles  a function of the project passed to a package's call: a route, a procedure, a listener; an inline one
//            is a "route" itself, named by the call's string (`get("/x", …)`) or, at a module's top level, the key
//            it's under (`list: q(…)`); without either (`db.transaction(tx => …)`) it's part of the function it's in
//   reads, writes  a call to a package's method named like a read or a write (`findMany`, `insert`, …), to the store
//            the chain names: an argument of the project (a table), a string ("users", SQL's FROM), a property between
//            client and method (`prisma.user`), the receiver's type argument (`Repository<User>`); none: no edge.
//            Sub: the store is a subquery the project declares (`.as("x")` on a chain), whose tables aren't followed
//            A bare string only in a chain of calls: `_.get(o, "a.b")`, `createElement("div")` name no store
//   requests  a call that sends a network request, to no declaration: the global fetch, node:http's and node:https's
//            request and get, a package's call given a URL constant (below)
//   passes   a call in an inline route's handler that passes its input on (its first parameter; `input` of a
//            destructured one) to a function of the project: the way down from a route (uses spec 9.2). A named
//            handler: from the route to it, and from it for each of its calls that passes its input on
//   references  a name of the project in the chain that registers an inline route (`p.input(schema).query(…)`),
//            from the route: the way up from the name ends there
//   url      a call with an argument that is a URL: a string, `new URL(…)` or a template starting with http:// or
//            https://, or a const that holds one, or an environment variable (`process.env.X`,
//            `import.meta.env.X`, a member read of a module-level object made from one). With the URL's text or the
//            variable, and where it stands
// All of the project's: a walk goes on from any of them. Test files aside. One checker round trip per file and kind:
// what a URL needs (urlOf) is asked in the same batch as the calls. A route under nested keys is named by their path:
// `shop: { list: q(…) }` is "query shop.list".
// With "shapes" in the input it reads code structures instead: see shaped below.
const fs = require("fs");
const { dir, ts, shapes } = JSON.parse(fs.readFileSync(0, "utf8"));
const { API } = require(ts + "/dist/api/sync/api.js");
const is = require(ts + "/dist/ast/is.js");

const read = /^(find\w*|get\w*|select\w*|query\w*|count\w*|aggregate|exists|load\w*|fetch\w*|scan|search)$/;
const write = /^(insert\w*|create\w*|update\w*|upsert\w*|delete\w*|remove\w*|save\w*|set|put\w*|destroy|increment|decrement|truncate|bulk\w*|replace\w*|merge|persist|execute|exec|run)$/;
const test = /(^|\/)(__tests__|__mocks__|tests?|e2e|spec|fixtures?)\/|\.(test|spec|stories)\.[cm]?[jt]sx?$/;
// a function after from or join is no table: from unnest($1); insert into t (cols) is one
const sql = /\b(?:(select\b[\s\S]*?\bfrom|join)\s+["`]?([A-Za-z_][\w.]*)(?!["`\w.]*\s*\()|(insert\s+into|update|delete\s+from)\s+["`]?([A-Za-z_][\w.]*))/i;

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
    (is.isVariableDeclaration(n) && n.initializer && (is.isArrowFunction(n.initializer) || is.isFunctionExpression(n.initializer) || !!inner(n.initializer))) ||
    (is.isPropertyAssignment(n) && (is.isArrowFunction(n.initializer) || is.isFunctionExpression(n.initializer)));
  const up = k => { let p = n; for (let i = 0; i < k && p; i++) p = p.parent; return !!p && is.isSourceFile(p) };
  const top = up(1) || up(3); // a store: the module's, not a parameter
  // a subquery: a chain of calls made a table of its own, `db.select(…).from(t).as("x")`
  const init = is.isVariableDeclaration(n) && n.initializer, as = init && is.isCallExpression(init) && init.expression;
  const sub = !!as && is.isPropertyAccessExpression(as) && as.name.text === "as" && is.isCallExpression(as.expression);
  return { name: s.name, path, line: lineOf(n, sf), fn, top, sub, node: n, sf };
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
// inner is the function a wrapper is given, `catchAsync(async (req, res) => …)`: a named call's first argument
const inner = x => (x && is.isCallExpression(x) && is.isIdentifier(x.expression) && isFn(x.arguments[0]) ? x.arguments[0] : null);

const out = [], seen = new Set();
const emit = (kind, s, line, t, url, via) => {
  if (!s || !t) return;
  const k = [kind, s.path, s.name, s.line, line, t.name, t.path, t.line].join("\0");
  if (seen.has(k)) return;
  seen.add(k);
  out.push({ Line: line, Kind: kind, By: "ts", SName: s.name, SKind: s.kind, SPath: s.path, SLine: s.line, SEnd: s.end,
    TName: t.name, TPath: t.path || "", TLine: t.line || 0, Sub: t.sub || undefined, URL: url?.url, Env: url?.env, URLAt: url?.at,
    Via: via || undefined });
};
// ifaceOf is the name of the interface or type whose member n is; "" for none
const ifaceOf = n => {
  if (!n || !(is.isMethodSignatureDeclaration(n) || is.isPropertySignatureDeclaration(n))) return "";
  const p = n.parent;
  if (is.isInterfaceDeclaration(p)) return text(p.name);
  return is.isTypeLiteralNode(p) && is.isTypeAliasDeclaration(p.parent) ? text(p.parent.name) : "";
};
// fnOf is the function a declaration of the project holds: its own, a variable's or a property's arrow, the one a
// variable's wrapper is given; null none
const fnOf = d => (is.isFunctionDeclaration(d.node) || is.isMethodDeclaration(d.node) ? d.node : isFn(d.node.initializer) ? d.node.initializer : inner(d.node.initializer));
const isUrl = s => /^https?:\/\//.test(s);
const envObj = s => s === "process.env" || s === "import.meta.env";
const isStr = x => is.isStringLiteral(x) || is.isNoSubstitutionTemplateLiteral(x) || is.isTemplateExpression(x);
const wrapped = x => is.isParenthesizedExpression(x) || is.isAsExpression(x) || is.isNonNullExpression(x) || is.isSatisfiesExpression(x);
const isMember = x => is.isPropertyAccessExpression(x) || is.isElementAccessExpression(x);
const memberName = x => (is.isPropertyAccessExpression(x) ? x.name.text : text(x.argumentExpression));
const upper = /^[A-Z][A-Z0-9_]*$/;
// wanted calls fn(n) with every identifier urlOf needs a symbol of, for an expression passed on: so they resolve in
// the file's one batch, not one by one. Same shape as urlOf, which reads only what was asked.
const wanted = (n, fn, sf, depth = 0) => {
  if (!n || depth > 3) return;
  if (wrapped(n)) return wanted(n.expression, fn, sf, depth);
  if (is.isIdentifier(n)) fn(n);
  else if (is.isTemplateExpression(n)) n.head.text || wanted(n.templateSpans[0]?.expression, fn, sf, depth + 1);
  else if (is.isNewExpression(n)) n.expression.text === "URL" && (wanted(n.arguments?.[0], fn, sf, depth + 1), wanted(n.arguments?.[1], fn, sf, depth + 1));
  else if (is.isBinaryExpression(n)) plus(n, sf) && (wanted(n.left, fn, sf, depth + 1), wanted(n.right, fn, sf, depth + 1));
  else if (isMember(n) && upper.test(memberName(n) || "") && is.isIdentifier(n.expression)) fn(n.expression);
};
const plus = (n, sf) => sf.text.slice(n.operatorToken.getStart(sf), n.operatorToken.end) === "+" && (isStr(n.left) || isStr(n.right)); // base + "/x", not a + b
// urlOf is the URL an expression passes: {url, at}, or {env, at} for an environment variable, null for neither.
// sym(x, p) is the symbol of an identifier of file p that wanted() named; a const is followed only at a module's top
// level. Followed to a URL it must hold what a URL is made of: a string, a template, new URL(…), an environment read;
// followed to an env object (any), whatever it holds, as its text is checked for the environment.
const urlOf = (n, sf, p, sym, depth = 0) => {
  if (!n || depth > 3) return null;
  const at = x => (p ? p + ":" + lineOf(x, sf) : "");
  const src = x => sf.text.slice(x.getStart(sf), x.end);
  const sub = x => urlOf(x, sf, p, sym, depth);
  if (wrapped(n)) return sub(n.expression);
  if (is.isStringLiteral(n) || is.isNoSubstitutionTemplateLiteral(n)) return isUrl(n.text) ? { url: n.text, at: at(n) } : null;
  if (is.isTemplateExpression(n)) // `${base}/x`: the template's static prefix, else what its first span is
    return isUrl(n.head.text) ? { url: n.head.text, at: at(n) } : n.head.text ? null : sub(n.templateSpans[0]?.expression);
  if (is.isNewExpression(n)) return n.expression.text === "URL" ? sub(n.arguments?.[0]) || sub(n.arguments?.[1]) : null; // new URL("/x", base)
  if (is.isBinaryExpression(n)) return plus(n, sf) ? sub(n.left) || sub(n.right) : null;
  const via = (x, any) => { // the const an identifier names, with its initializer
    const d = decl(sym(x, p));
    const init = d && d.top && d.node.initializer;
    const v = init && wrapped(init) ? init.expression : init;
    return v && (any || isStr(v) || is.isNewExpression(v) || isMember(v)) ? { d, init } : null;
  };
  if (isMember(n)) {
    const name = memberName(n);
    if (!name) return null;
    if (envObj(src(n.expression))) return { env: name, at: at(n) };
    // a member read of a module-level object made from the environment: `env.API_URL`
    const v = upper.test(name) && is.isIdentifier(n.expression) && via(n.expression, true);
    return v && /\b(process|import\.meta)\.env\b/.test(v.d.sf.text.slice(v.init.getStart(v.d.sf), v.init.end)) ? { env: name, at: at(n) } : null;
  }
  if (is.isIdentifier(n)) {
    const v = via(n);
    return v && urlOf(v.init, v.d.sf, v.d.path, sym, depth + 1); // another file's names are not in the batch: no symbol
  }
  return null;
};

// ambient: a declaration the platform gives every file, not a module's export: the DOM's fetch, Node's, a worker
// runtime's, Bun's, in a script's .d.ts or in `declare global`
const ambients = new Map();
const ambient = h => {
  const k = h.path + ":" + h.index;
  if (!ambients.has(k)) ambients.set(k, ambientOf(h));
  return ambients.get(k);
};
const ambientOf = h => safe(() => {
  const n = /\.d\.[cm]?ts$/.test(h.path) && h.resolve(project);
  if (!n || (!is.isFunctionDeclaration(n) && !is.isVariableDeclaration(n))) return false;
  const st = is.isVariableDeclaration(n) ? n.parent?.parent : n, sf = n.getSourceFile();
  if (!st || /^export\b/.test(sf.text.slice(st.getStart(sf), st.end))) return false;
  for (let p = st.parent; p && !is.isSourceFile(p); p = p.parent) if (is.isModuleDeclaration(p) && p.name.text !== "global") return false;
  return true;
});

const done = new Set(); // a file in two projects: once; a named handler's calls: once
const handlers = new Map(); // a file's named handlers: their declaration, their function
for (project of shapes ? [] : projects()) {
program = project.program, checker = project.checker;
decls.clear(), ambients.clear();
for (const file of program.getSourceFileNames()) {
  const path = rel(file);
  if (!path || test.test(path) || done.has(path)) continue;
  done.add(path);
  const sf = program.getSourceFile(file);
  if (!sf) continue;
  const ask = [], calls = [], refs = [], verbs = [], inlines = [];
  const need = n => ask.push(n) - 1;
  const ids = new Map(); // path:pos of a node -> its place in ask, for urlOf
  const idAt = n => ids.has(path + ":" + n.pos) || ids.set(path + ":" + n.pos, need(n));
  // scope: the declaration a node is in, as codegraph names it; an inline function passed to a call is one of its
  // own until we know whether the call is a package's (a route) or not (it's its parent's)
  const named = (n, name, kind, parent) => ({ name, kind, path, line: lineOf(n, sf), end: endOf(n, sf), parent });
  const eff = s => (s && s.inline && !s.route ? eff(s.parent) : s);
  const label = c => { // a route's name: the call's string, else the key it's under at the module's top level
    const lit = c.arguments.find(a => is.isStringLiteral(a) || is.isNoSubstitutionTemplateLiteral(a));
    if (lit && /^\S+$/.test(lit.text)) return lit.text; // "/orders", "click"; not a message
    for (let p = c.parent; p && !is.isSourceFile(p) && !is.isBlock(p); p = p.parent)
      if (is.isPropertyAssignment(p) || (is.isVariableDeclaration(p) && is.isIdentifier(p.name))) return atTop(p) ? keyPath(p) : "";
    return "";
  };
  const keyPath = p => { // a key's path through the object literals it is nested in: "shop.list"
    let k = text(p.name);
    for (let q = p; is.isPropertyAssignment(q) && is.isObjectLiteralExpression(q.parent) && is.isPropertyAssignment(q.parent.parent); q = q.parent.parent)
      k = text(q.parent.parent.name) + "." + k;
    return k;
  };
  const inputOf = f => { // the names a handler's first parameter binds: `input` of ({ input, ctx })
    const b = f.parameters?.[0]?.name;
    if (!b) return [];
    if (is.isIdentifier(b)) return [b.text];
    const ns = is.isObjectBindingPattern(b) ? b.elements.map(e => text(e.name)).filter(Boolean) : [];
    return ns.includes("input") ? ["input"] : ns;
  };
  const reads = (c, names) => { // whether a call's arguments read one of names, not as a property's name
    let hit = false;
    const v = n => {
      if (hit) return;
      if (is.isIdentifier(n) && names.includes(n.text) && !(is.isPropertyAccessExpression(n.parent) && n.parent.name === n)) hit = true;
      else n.forEachChild(v);
    };
    for (const a of c.arguments || []) v(a);
    return hit;
  };
  const atTop = n => { // in no function: `const r = db.transaction(…)` in one names a result, not a route
    for (let p = n.parent; p; p = p.parent) if (isFn(p) || is.isFunctionDeclaration(p) || is.isMethodDeclaration(p)) return false;
    return true;
  };
  const routeName = c => ((calleeName(c)?.text || "") + " " + label(c)).trim(); // "get /orders", "query posts"
  const walk = (n, scope) => {
    let s = scope;
    if (is.isFunctionDeclaration(n) || is.isMethodDeclaration(n) || is.isClassDeclaration(n)) {
      if (n.name) s = named(n, text(n.name), declKind(n), scope);
    } else if (isFn(n)) {
      const p = n.parent;
      // a variable's wrapped one is the variable's, whatever name the function has: routes are given the variable
      if (inner(p) === n && is.isVariableDeclaration(p.parent) && is.isIdentifier(p.parent.name)) s = named(p.parent, p.parent.name.text, "function", scope);
      else if (n.name) s = named(n, text(n.name), "function", scope);
      else if (is.isVariableDeclaration(p) && is.isIdentifier(p.name)) s = named(p, p.name.text, "function", scope);
      else if (is.isPropertyAssignment(p)) s = named(p, text(p.name), "function", scope);
      else if (is.isCallExpression(p) && p.arguments.includes(n)) {
        // a wrapped one passed on, `get("/x", catchAsync(async …))`, is the outer call's
        const c = inner(p) === n && is.isCallExpression(p.parent) && p.parent.arguments.includes(p) ? p.parent : p;
        s = { ...named(n, "", "route", scope), inline: true, call: c, input: inputOf(n) };
        inlines.push(s);
      }
    }
    if (is.isCallExpression(n) || is.isNewExpression(n)) {
      const name = calleeName(n);
      const args = n.arguments || [];
      if (name) calls.push({ n, at: need(name), scope: s });
      for (const a of args) wanted(a, idAt, sf);
      for (const a of args) if (is.isIdentifier(a) || is.isPropertyAccessExpression(a))
        refs.push({ call: n, n: a, at: need(is.isIdentifier(a) ? a : a.name), scope: s });
      if (name && is.isPropertyAccessExpression(n.expression) && (read.test(name.text) || write.test(name.text)) && eff(s))
        verbs.push({ n, scope: s, at: need(name) });
    }
    if ((is.isJsxOpeningElement(n) || is.isJsxSelfClosingElement(n)) && is.isIdentifier(n.tagName) && /^[A-Z]/.test(n.tagName.text))
      calls.push({ n, at: need(n.tagName), scope: s, jsx: true });
    if (is.isVariableDeclaration(n) && n.initializer && atTop(n)) wanted(n.initializer, idAt, sf); // a const urlOf follows
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
    const t = decl(syms[c.at]), r = eff(c.scope), via = t && !c.jsx && ifaceOf(t.node);
    if (t && (t.fn || c.jsx || via)) emit(c.jsx ? "renders" : "calls", r, line(c.n), t, null, via);
    if (t && t.fn && !c.jsx && r?.route && reads(c.n, r.input)) emit("passes", r, line(c.n), t);
  }
  const routeOf = new Map(inlines.filter(s => s.route).map(s => [s.call, s]));
  const chainRoute = c => { // the inline route whose registering chain c is a call of
    for (let x = c; x; x = x.parent.parent) {
      if (routeOf.has(x)) return routeOf.get(x);
      if (!is.isPropertyAccessExpression(x.parent) || x.parent.expression !== x || !is.isCallExpression(x.parent.parent)) return null;
    }
    return null;
  };
  for (const r of refs) {
    const t = decl(syms[r.at]), rt = t && chainRoute(r.call);
    if (rt) emit("references", rt, line(r.n), t);
    if (!t || !t.fn) continue;
    const at = callAt.get(r.call);
    if (at !== undefined && ext(at) && label(r.call)) { // a handler: from the route the call registers
      const c = r.call, l = line(c), rt = { name: routeName(c), kind: "route", path, line: l, end: endOf(c, sf) };
      emit("handles", rt, l, t);
      const fn = fnOf(t);
      if (fn && !handlers.has(t)) handlers.set(t, fn);
      if (fn) emit("passes", rt, l, t); // the route hands its input to it
    } else emit("calls", eff(r.scope), line(r.n), t);
  }
  // a named handler's calls that pass its input on, wherever it is declared: one round trip for the file's
  const hcalls = [];
  for (const [t, fn] of handlers) {
    if (done.has(t.path + "\0" + t.line)) continue;
    done.add(t.path + "\0" + t.line);
    const input = inputOf(fn), v = n => {
      if (is.isCallExpression(n) && calleeName(n) && reads(n, input)) hcalls.push({ t, n });
      n.forEachChild(v);
    };
    if (fn.body) v(fn.body);
  }
  handlers.clear();
  if (hcalls.length) {
    const hs = resolve(hcalls.map(h => calleeName(h.n)));
    hcalls.forEach((h, i) => {
      const d = decl(hs[i]);
      if (d && d.fn) emit("passes", { name: h.t.name, kind: "function", path: h.t.path, line: h.t.line, end: endOf(fnOf(h.t), h.t.sf) }, lineOf(h.n, h.t.sf), d);
    });
  }
  // requests: platform senders by where they are declared; urls: whatever call passes one on
  const sym = (x, p) => syms[ids.get(p + ":" + x.pos)];
  for (const c of calls) {
    if (c.jsx || !c.n.arguments) continue;
    const name = calleeName(c.n).text, dp = syms[c.at]?.declarations?.[0]?.path;
    let u = null;
    for (const a of c.n.arguments) if ((u = urlOf(a, sf, path, sym))) break;
    const sender = dp && ((name === "fetch" && ambient(syms[c.at].declarations[0])) || !rel(dp) && (/\/@types\/node\/https?\.d\.ts$/.test(dp) && /^(request|get)$/.test(name)));
    if (sender || ((u?.url || u?.env) && ext(c.at))) emit("requests", eff(c.scope), line(c.n), { name });
    if (u) emit("url", eff(c.scope), line(c.n), { name }, u);
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
        let r = is.isPropertyAccessExpression(a) && is.isIdentifier(a.expression) ? a.name : a; // schema.users: users
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
        if (m) { ch.store = { name: m[2] || m[4] }; if (m[3]) ch.sqlWrite = true; break }
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
// shaped is the --shapes mode (uses spec 5 and 9.2): for each {file, line, name, in, ways} of shapes, the declaration of
// file that holds line (the one named in, else the statement at the module's top), every line in it that names what
// the name at line resolves to, and with ways the way down to the first 3 of them: shapes.go's synUse, null where
// the program doesn't hold the file. Without a name at line, a string there that holds it as a word is the use: a
// table SQL names. shapes.go turns the ways into rows.
function shaped(items) {
  const out = items.map(() => null), byFile = new Map();
  items.forEach((it, i) => byFile.set(it.file, [...(byFile.get(it.file) || []), i]));
  const all = projects();
  for (const [file, idx] of byFile) {
    const pr = all.find(p => p.program.getSourceFile(root + file));
    if (!pr) continue;
    project = pr, program = pr.program, checker = pr.checker, decls.clear();
    const sf = program.getSourceFile(root + file), want = new Set(idx.map(i => items[i].name)), ids = new Map(), strs = [];
    const visit = n => {
      if (is.isIdentifier(n) && want.has(n.text)) ids.set(n.text, [...(ids.get(n.text) || []), n]);
      else if (isStr(n)) strs.push(n);
      n.forEachChild(visit);
    };
    visit(sf);
    const word = (t, name) => new RegExp("(^|[^\\w$])" + name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "([^\\w$]|$)").test(t);
    const asks = [];
    for (const i of idx) {
      const it = items[i], at = (ids.get(it.name) || []).find(n => lineOf(n, sf) === it.line);
      if (!at) {
        const lit = strs.find(n => lineOf(n, sf) <= it.line && it.line <= endOf(n, sf) && word(sf.text.slice(n.getStart(sf), n.end), it.name));
        if (lit) asks.push({ i, at: lit, d: holder(lit, it.in), inside: [] });
        continue;
      }
      const d = holder(at, it.in);
      asks.push({ i, at, d, inside: ids.get(it.name).filter(n => n.pos >= d.pos && n.end <= d.end) });
    }
    const nodes = asks.flatMap(a => a.inside), syms = nodes.length ? resolve(nodes) : [];
    const conds = [];
    let k = 0;
    for (const a of asks) {
      const ss = syms.slice(k, (k += a.inside.length)), me = ss[a.inside.indexOf(a.at)];
      const uses = me ? a.inside.filter((_, j) => ss[j]?.id === me.id) : [a.at];
      const lines = [], marked = [];
      for (const n of uses) if (!lines.includes(lineOf(n, sf))) lines.push(lineOf(n, sf)), marked.push(n);
      const u = { s: [lineOf(a.d, sf), endOf(a.d, sf)], n: lines, i: (is.isInterfaceDeclaration(a.d) && text(a.d.name)) || undefined };
      if (items[a.i].ways) u.w = marked.slice(0, 3).map(n => wayOf(n, a.d, uses, sf, conds));
      out[a.i] = u;
    }
    if (conds.length) { // where an if's name comes from: a variable its function declares before it
      const cs = resolve(conds.map(c => c.id));
      conds.forEach((c, j) => {
        const d = decl(cs[j]), v = d && d.node;
        if (!v || d.path !== file || !is.isVariableDeclaration(v) || !v.initializer || v.pos > c.at.pos) return;
        const fn = fnAround(c.at);
        if (fn && v.pos >= fn.pos && v.end <= fn.end) c.el.c = { n: c.id.text, l: d.line, t: textAt(v.initializer.getStart(sf), sf) };
      });
    }
  }
  return out;
}
const declNamed = n => (is.isFunctionDeclaration(n) || is.isMethodDeclaration(n) || is.isClassDeclaration(n) || is.isInterfaceDeclaration(n) ||
  is.isTypeAliasDeclaration(n) || is.isEnumDeclaration(n) || is.isVariableDeclaration(n) || is.isPropertyAssignment(n) || is.isPropertyDeclaration(n)) && n.name;
// holder is the declaration named name around n, a variable's statement with it; else the statement at the module's top
function holder(n, name) {
  let top = n;
  for (let p = n; p && !is.isSourceFile(p); p = p.parent) {
    top = p;
    if (declNamed(p) && text(p.name) === name) {
      const st = is.isVariableDeclaration(p) && p.parent.parent;
      return st && is.isVariableStatement(st) && st.declarationList.declarations.length === 1 ? st : p;
    }
  }
  return top;
}
const fnAround = n => { for (let p = n.parent; p; p = p.parent) if (isFn(p) || is.isFunctionDeclaration(p) || is.isMethodDeclaration(p)) return p; return null };
const textAt = (pos, sf) => { const e = sf.text.indexOf("\n", pos); return sf.text.slice(pos, e < 0 ? undefined : e).slice(0, 200) };
const posLine = (pos, sf) => sf.getLineAndCharacterOfPosition(pos).line + 1;
const stmtsOf = b => (b && is.isBlock(b) ? [...b.statements] : b ? [b] : []);
// entries is a node's row kind and the entries it holds as a container; null for a node that makes no row
function entries(n) {
  if (is.isFunctionDeclaration(n) || is.isMethodDeclaration(n) || is.isConstructorDeclaration(n) || is.isGetAccessorDeclaration(n) ||
    is.isSetAccessorDeclaration(n) || isFn(n)) return { k: "func", kids: n.body && is.isBlock(n.body) ? [...n.body.statements] : [] };
  if (is.isClassDeclaration(n) || is.isClassExpression(n) || is.isInterfaceDeclaration(n) || is.isTypeLiteralNode(n) || is.isEnumDeclaration(n))
    return { k: "object", kids: [...n.members] };
  if (is.isObjectLiteralExpression(n)) return { k: "object", kids: [...n.properties] };
  if (is.isArrayLiteralExpression(n)) return { k: "array", kids: [...n.elements] };
  if (is.isCallExpression(n) || is.isNewExpression(n)) return { k: "call", kids: [...(n.arguments || [])] };
  if (is.isIfStatement(n)) return { k: "if", kids: [...stmtsOf(n.thenStatement), ...(n.elseStatement ? [n.elseStatement] : [])] };
  if (is.isSwitchStatement(n)) return { k: "if", kids: [...n.caseBlock.clauses] };
  if (is.isCaseClause(n) || is.isDefaultClause(n)) return { k: "if", kids: [...n.statements] };
  if (is.isTryStatement(n)) return { k: "if", kids: [...n.tryBlock.statements, ...(n.catchClause ? [n.catchClause] : []), ...(n.finallyBlock ? [n.finallyBlock] : [])] };
  if (is.isCatchClause(n)) return { k: "if", kids: [...n.block.statements] };
  if (is.isForStatement(n) || is.isForOfStatement(n) || is.isForInStatement(n) || is.isWhileStatement(n) || is.isDoStatement(n))
    return { k: "loop", kids: stmtsOf(n.statement) };
  if (is.isJsxElement(n)) return { k: "jsx", kids: [...n.openingElement.attributes.properties, ...n.children.filter(c => !is.isJsxText(c) || c.text.trim())] };
  if (is.isJsxSelfClosingElement(n)) return { k: "jsx", kids: [...n.attributes.properties] };
  if (is.isJsxFragment(n)) return { k: "jsx", kids: n.children.filter(c => !is.isJsxText(c) || c.text.trim()) };
  if (is.isBlock(n)) return is.isIfStatement(n.parent) && n.parent.elseStatement === n ? { k: "if", kids: [...n.statements] } : null;
  if (is.isVariableDeclaration(n) || is.isPropertyAssignment(n) || is.isPropertyDeclaration(n) || is.isPropertySignatureDeclaration(n) ||
    is.isShorthandPropertyAssignment(n) || is.isJsxAttribute(n) || is.isSpreadAssignment(n)) return { k: "", kids: [] };
  if (is.isStatement(n)) return { k: "stmt", kids: [] };
  return null;
}
// kindOf is an entry's kind: the value it holds; a call or a chain on one line is a call
function kindOf(n) {
  if (is.isPropertyAssignment(n) || is.isPropertyDeclaration(n) || is.isVariableDeclaration(n)) return n.initializer ? kindOf(n.initializer) : "stmt";
  if (is.isExpressionStatement(n) || is.isReturnStatement(n)) return n.expression ? kindOf(n.expression) : "stmt";
  if (is.isVariableStatement(n)) return "stmt";
  if (is.isAwaitExpression(n) || is.isParenthesizedExpression(n)) return kindOf(n.expression);
  const e = entries(n);
  return e && e.k ? e.k : "stmt";
}
// links is a chain of calls and reads, `z.array(…).max(…)`, outermost n: its head and each link from its dot to its end
function links(n, sf) {
  const ls = [];
  let x = n;
  for (;;) {
    const call = is.isCallExpression(x) ? x : null, pa = call ? x.expression : x;
    if (!is.isPropertyAccessExpression(pa)) break;
    const dot = sf.text.indexOf(".", pa.expression.end);
    ls.unshift({ pos: dot < 0 || dot > pa.name.pos ? pa.name.pos : dot, end: x.end, call, spine: [x, pa] });
    x = pa.expression;
  }
  return { head: x, links: ls };
}
const chainTop = n => (is.isCallExpression(n) || is.isPropertyAccessExpression(n)) &&
  !((is.isPropertyAccessExpression(n.parent) || is.isCallExpression(n.parent)) && n.parent.expression === n);
// wayOf is the way from d down to the name id as synEls; uses, all the names of the subject, mark entries
function wayOf(id, d, uses, sf, conds) {
  const up = [];
  for (let p = id; p; p = p.parent) { up.unshift(p); if (p === d) break }
  const holds = (pos, end) => uses.some(u => u.pos >= pos && u.end <= end);
  const kid = (k, node, pos, end) => ({ k, l: posLine(pos, sf), e: posLine(end, sf), t: textAt(pos, sf), u: holds(pos, end) || undefined, pos, end, node });
  const nodeKid = n => kid(kindOf(n), n, n.getStart(sf), n.end);
  const way = [], skip = new Set();
  for (let i = 0; i < up.length; i++) {
    const n = up[i];
    if (skip.has(n)) continue;
    let el = null;
    if (n === id) el = { k: "", l: lineOf(n, sf), t: textAt(n.getStart(sf), sf), kids: [], pos: n.getStart(sf) };
    else if (chainTop(n)) {
      const ch = links(n, sf);
      if (ch.links.length && ch.links.some(l => posLine(l.pos, sf) > lineOf(n, sf))) { // a chain over lines: its links
        for (const l of ch.links) l.spine.forEach(x => skip.add(x));
        way.push({ k: "chain", l: lineOf(n, sf), t: textAt(n.getStart(sf), sf), kids: ch.links.map(l => kid("link", null, l.pos, l.end)), pos: n.getStart(sf) });
        const l = ch.links.find(l => id.pos >= l.pos && id.end <= l.end);
        if (l) way.push({ k: "link", l: posLine(l.pos, sf), t: textAt(l.pos, sf), kids: (l.call ? [...l.call.arguments] : []).map(nodeKid), pos: l.pos });
        continue;
      }
    }
    if (!el) {
      const e = entries(n);
      if (!e) continue;
      el = { k: e.k, l: lineOf(n, sf), t: textAt(n.getStart(sf), sf), kids: e.kids.map(nodeKid), pos: n.getStart(sf) };
      if (is.isIfStatement(n)) {
        let c = n.expression;
        while (c && !is.isIdentifier(c)) c = is.isBinaryExpression(c) ? c.left : c.expression || c.operand;
        if (c) conds.push({ el, id: c, at: n });
      }
    }
    way.push(el);
  }
  for (let j = 0; j < way.length; j++) { // the entry the next sits in; an entry on this line off the way: its own entries
    const el = way[j], npos = way[j + 1] ? way[j + 1].pos : -1;
    el.kids = el.kids.flatMap(k => {
      const sub = k.node && entries(k.node);
      return k.l === el.l && sub && sub.kids.length && !(npos >= k.pos && npos < k.end) ? sub.kids.map(nodeKid) : [k];
    });
    el.w = el.kids.findIndex(k => npos >= k.pos && npos < k.end);
    el.e = el.kids.map(({ k, l, e, t, u }) => ({ k, l, e, t, u }));
  }
  for (const el of way) delete el.kids, delete el.pos;
  return way;
}
function safe(f) { try { return f() } catch { return null } }
// exit once written: a pipe's write is async, and the checker's process would keep node alive
process.stdout.write(JSON.stringify(shapes ? shaped(shapes) : out), () => process.exit(0));
