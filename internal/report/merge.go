package report

import (
	"cmp"
	"diffharbor/internal/diff"
	"diffharbor/internal/review"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Other is another open PR that changes files this one does.
type Other struct {
	Number             int
	Title, URL, Author string
	Draft              bool
	Files              []string // the files both change
	Clash              []string // of them, those where both change the same lines: a conflict for whichever merges second
}

// risk is a kind of change a merge alone doesn't finish: a variable to set where the app runs, a migration to run.
type risk struct {
	Title, Hint string
	Items       []riskItem
	More        int // past maxRisks
}

// riskItem is one of a risk's changes: What a name or path, Note what happened to it, ID where the diff shows it.
type riskItem struct {
	What, Note, ID string
	Warn           bool // a secret by its name, a major version
}

// maxRisks is the items a kind of risk lists.
const maxRisks = 12

var (
	// envRead is a read of an environment variable or a CI secret: the name is the first group that matched.
	envRead = regexp.MustCompile(`\b(?:[Gg]etenv|LookupEnv|env::var(?:_os)?|System\.getenv|ENV\.fetch)\(\s*["']([A-Z][A-Z0-9_]+)["']` +
		`|\bprocess\.env\.([A-Z][A-Z0-9_]+)|\b(?:process\.env|ENV|os\.environ)\[\s*["']([A-Z][A-Z0-9_]+)` +
		`|\bimport\.meta\.env\.([A-Z][A-Z0-9_]+)|\bos\.environ\.get\(\s*["']([A-Z][A-Z0-9_]+)` +
		`|\$\{\{\s*(?:secrets|vars)\.([A-Za-z0-9_]+)`)
	// interpolated is ${NAME} where a compose file or env example takes it from the environment
	interpolated = regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]+)(?::?[-?+=][^}]*)?\}`)
	defaulted    = regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]+):?[-=]`) // ${NAME:-x}: it may stay unset
	envExample   = regexp.MustCompile(`^\s*(?:export\s+)?([A-Z][A-Z0-9_]+)=`)
	envFile      = regexp.MustCompile(`(^|/)\.env[^/]*$`)
	composeFile  = regexp.MustCompile(`(^|/)(docker-)?compose[^/]*\.ya?ml$`)
	secretName   = regexp.MustCompile(`SECRET|TOKEN|PASSWORD|PASSWD|PRIVATE|CREDENTIAL|_KEY$|^KEY$|API_?KEY|DSN`)

	migration = regexp.MustCompile(`(?i)(^|/)(migrations?|migrate|alembic|changelogs?)/|\.sql$|(^|/)schema\.prisma$`)
	deploy    = regexp.MustCompile(`(?i)(^|/)(Dockerfile[^/]*|[^/]*\.dockerfile|(docker-)?compose[^/]*\.ya?ml|Procfile|fly\.toml|vercel\.json|netlify\.toml|nixpacks\.toml|render\.ya?ml|Chart\.ya?ml|[^/]*\.tf|[^/]*\.tfvars|\.gitlab-ci\.ya?ml|Jenkinsfile|CODEOWNERS)$|(^|/)\.github/workflows/|(^|/)(k8s|kubernetes|helm|charts)/`)
	// settings files; of code only the tools' own (vite.config.ts, .eslintrc.js): a settings.go is code
	config     = regexp.MustCompile(`(?i)(^|/)(\.[^/]+rc(\.[a-z]+)?|\.env[^/]*|[^/]*(config|settings|conf)[^/]*\.[a-z]+|[^/]*\.(ini|conf|cfg|properties|toml)|\.[^/]+\.(ya?ml|json|toml))$`)
	codeConfig = regexp.MustCompile(`(^|/)([^/]+\.config|\.[^/]+rc)\.[cm]?[jt]s$`)

	// a dependency and its version, by manifest: the line's first group the name, the second the version
	manifests = []struct{ path, line *regexp.Regexp }{
		{regexp.MustCompile(`(^|/)go\.mod$`), regexp.MustCompile(`^\s*(?:require\s+)?([a-z0-9.-]+\.[a-z]{2,}/\S+)\s+(v[^\s/]+)\s*$`)}, // "// indirect" ones stay out
		{regexp.MustCompile(`(^|/)package\.json$`), regexp.MustCompile(`^\s*"(@?[\w./-]+)"\s*:\s*"((?:[~^<>=]*\s*)?\d[^"]*|workspace:[^"]*|npm:[^"]*)"`)},
		{regexp.MustCompile(`(^|/)requirements[^/]*\.txt$`), regexp.MustCompile(`^([A-Za-z0-9_.\[\]-]+)\s*(?:[=~!]=|>=?|<=?)\s*([\w.*]+)`)},
		{regexp.MustCompile(`(^|/)(pyproject|Cargo)\.toml$`), regexp.MustCompile(`^\s*"?([A-Za-z0-9_.-]+)"?\s*(?:=\s*(?:\{[^}]*?version\s*=\s*)?"[~^<>=]*\s*|[=~!<>]=?\s*)(\d[\w.*]*)`)},
		{regexp.MustCompile(`(^|/)Gemfile$`), regexp.MustCompile(`^\s*gem\s+["']([\w-]+)["']\s*,\s*["'][~><=\s]*(\d[\w.]*)`)},
	}
	notDep = map[string]bool{"version": true, "python": true, "edition": true, "rust-version": true, "requires-python": true, "go": true}
	major  = regexp.MustCompile(`\d+`)
)

// risks is what the files' diffs change that a merge doesn't finish, by kind: environment variables and CI secrets
// read or no longer read, migrations, dependencies, deploy and CI files, configuration. It sets the IDs it jumps to.
func risks(fs []*file) []risk {
	n := names{env: map[string]*[2]string{}, defaults: map[string]bool{}, deps: map[string]*lib{}}
	var data, ship, conf []riskItem
	for _, f := range fs {
		if f.Kind == "test" || diff.Skip.MatchString(f.Path) {
			continue
		}
		var manifest *regexp.Regexp
		for _, m := range manifests {
			if m.path.MatchString(f.Path) {
				manifest = m.line
			}
		}
		n.scan(f, manifest)
		file := riskItem{What: f.Path, Note: fmt.Sprintf("+%d −%d", f.Add, f.Del), ID: f.Path}
		switch {
		case migration.MatchString(f.Path):
			data = append(data, file)
		case deploy.MatchString(f.Path):
			ship = append(ship, file)
		case manifest == nil && config.MatchString(f.Path) && (f.Kind != "code" || codeConfig.MatchString(f.Path)):
			conf = append(conf, file)
		}
	}
	var rs []risk
	for _, r := range []risk{
		{Title: "Environment", Hint: "Read by added lines and by no removed one: set it where the app runs before it deploys. One no longer read can go after.", Items: n.vars()},
		{Title: "Migrations", Hint: "They run against live data: order them with the deploy and know the way back.", Items: data},
		{Title: "Dependencies", Hint: "New, removed and changed versions in the manifests; a changed major version is marked.", Items: n.libs()},
		{Title: "Build, deploy and CI", Hint: "They change how it ships, not what it does: the diff's tests don't cover them.", Items: ship},
		{Title: "Configuration", Hint: "Settings files: a changed value acts without a line of code.", Items: conf},
	} {
		if len(r.Items) > 0 {
			r.Items, r.More = r.Items[:min(len(r.Items), maxRisks)], max(0, len(r.Items)-maxRisks)
			rs = append(rs, r)
		}
	}
	return rs
}

// names is what risks collects from changed lines: environment variables and dependencies.
type names struct {
	env      map[string]*[2]string // name -> its first added line's ID, its first removed one's
	defaults map[string]bool       // names an added line gives a default
	deps     map[string]*lib       // manifest NUL name
}

// lib is a dependency's version before and after, and where the diff shows it.
type lib struct{ was, now, id string }

// scan reads a file's changed lines, manifest its dependency lines' pattern (nil: not a manifest).
func (n names) scan(f *file, manifest *regexp.Regexp) {
	reads := envRead
	if envFile.MatchString(f.Path) || composeFile.MatchString(f.Path) {
		reads = interpolated
	}
	for i := range f.Lines {
		l := &f.Lines[i]
		if l.Class != "add" && l.Class != "del" {
			continue
		}
		side := map[bool]int{true: 1}[l.Class == "del"]
		id := func() string {
			if l.ID == "" {
				l.ID = fmt.Sprintf("%s~%d", f.Path, i)
			}
			return l.ID
		}
		var vs []string
		for _, m := range reads.FindAllStringSubmatch(l.Text, -1) {
			vs = append(vs, cmp.Or(m[1:]...))
		}
		if m := envExample.FindStringSubmatch(l.Text); m != nil && envFile.MatchString(f.Path) {
			vs = append(vs, m[1])
		}
		for _, m := range defaulted.FindAllStringSubmatch(l.Text, -1) {
			n.defaults[m[1]] = n.defaults[m[1]] || side == 0
		}
		for _, v := range vs {
			if n.env[v] == nil {
				n.env[v] = &[2]string{}
			}
			n.env[v][side] = cmp.Or(n.env[v][side], id())
		}
		if m := manifestLine(manifest, l.Text); m != nil {
			d := n.deps[f.Path+"\x00"+m[1]]
			if d == nil {
				d = &lib{}
				n.deps[f.Path+"\x00"+m[1]] = d
			}
			if side == 0 {
				d.now, d.id = m[2], id()
			} else {
				d.was, d.id = m[2], cmp.Or(d.id, id())
			}
		}
	}
}

// vars is the environment variables read and no longer read; one both is only moved.
func (n names) vars() []riskItem {
	var is []riskItem
	for _, v := range slices.Sorted(maps.Keys(n.env)) {
		at := n.env[v]
		switch {
		case at[0] != "" && at[1] == "" && n.defaults[v]:
			is = append(is, riskItem{What: v, Note: "newly read, has a default", ID: at[0]})
		case at[0] != "" && at[1] == "":
			is = append(is, riskItem{What: v, Note: "newly read", ID: at[0], Warn: secretName.MatchString(v)})
		case at[1] != "" && at[0] == "":
			is = append(is, riskItem{What: v, Note: "no longer read", ID: at[1]})
		}
	}
	return is
}

// libs is the dependencies added, removed and with another version.
func (n names) libs() []riskItem {
	var is []riskItem
	for _, k := range slices.Sorted(maps.Keys(n.deps)) {
		d, name := n.deps[k], k[strings.IndexByte(k, 0)+1:]
		switch {
		case d.was == "":
			is = append(is, riskItem{What: name, Note: "new " + d.now, ID: d.id})
		case d.now == "":
			is = append(is, riskItem{What: name, Note: "removed", ID: d.id})
		case d.was != d.now:
			is = append(is, riskItem{What: name, Note: d.was + " → " + d.now, ID: d.id, Warn: major.FindString(d.was) != major.FindString(d.now)})
		}
	}
	return is
}

// manifestLine is a dependency line of a manifest: its name and version; nil for other lines.
func manifestLine(manifest *regexp.Regexp, l string) []string {
	if manifest == nil {
		return nil
	}
	if m := manifest.FindStringSubmatch(l); m != nil && !notDep[m[1]] {
		return m
	}
	return nil
}

// baseMoved is the base's commits since the fork that change files of the PR, and of them those files.
func baseMoved(behind []review.Commit, files map[string]*file) []review.Commit {
	var out []review.Commit
	for _, c := range behind {
		c.Paths = slices.DeleteFunc(slices.Clone(c.Paths), func(p string) bool { return files[p] == nil })
		if len(c.Paths) > 0 {
			out = append(out, c)
		}
	}
	return out
}
