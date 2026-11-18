package report

import (
	"diffharbor/internal/assets"
	"embed"
	"encoding/base64"
	"html"
	"html/template"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// logo and favicons are the header's logo and the page's icons, as data: URLs: the only images its CSP allows.
var logo, favicons = images(assets.Logo, assets.FaviconSVG, assets.FaviconPNG)

// images is the logo (64 px, shown at 28) and the favicons; the PNG for browsers without SVG icons.
func images(logo, svg, png []byte) (img, links template.HTML) {
	b64 := base64.StdEncoding.EncodeToString
	//nolint:gosec // base64 of our files or the operator's
	return template.HTML(`<img src="data:image/png;base64,` + b64(logo) + `" alt="DiffHarbor" title="DiffHarbor" width="28" height="28" class="size-7 shrink-0">`),
		template.HTML(`<link rel="icon" type="image/png" sizes="32x32" href="data:image/png;base64,` + b64(png) + `">` + "\n" + //nolint:gosec // as above
			`<link rel="icon" type="image/svg+xml" href="data:image/svg+xml;base64,` + b64(svg) + `">`)
}

// The icons are SVG files, one per icon, as the projects ship them: Lucide's (lucide.dev, ISC, lucide-static 0.577.0)
// in icons/lucide, drawn with a stroke; Seti UI's file icons (github.com/jesseweed/seti-ui, MIT) in icons/seti,
// filled, named "seti/{name}". Each folder holds its license. They are read once; the page draws them inline.
//
//go:embed icons/lucide/*.svg icons/seti/*.svg
var iconFiles embed.FS

// glyph is an icon's viewBox and what goes inside its svg; fill: a filled glyph, else a stroked one.
type glyph struct {
	box, body string
	fill      bool
}

var icons = loadIcons()

var svgRoot = regexp.MustCompile(`(?s)^\s*<svg[^>]*?viewBox="([^"]+)"[^>]*>(.*)</svg>\s*$`)

// loadIcons reads the icon files; a file that isn't one svg stops the program, as a template that doesn't parse does.
func loadIcons() map[string]glyph {
	m := map[string]glyph{}
	for _, dir := range []string{"lucide", "seti"} {
		files, err := fs.Glob(iconFiles, "icons/"+dir+"/*.svg")
		if err != nil {
			panic(err)
		}
		for _, f := range files {
			b, err := iconFiles.ReadFile(f)
			if err != nil {
				panic(err)
			}
			p := svgRoot.FindStringSubmatch(string(b))
			if p == nil {
				panic("icon " + f + ": not one svg with a viewBox")
			}
			name := strings.TrimSuffix(path.Base(f), ".svg")
			if dir == "seti" {
				name = "seti/" + name
			}
			m[name] = glyph{box: p[1], body: strings.TrimSpace(p[2]), fill: dir == "seti"}
		}
	}
	return m
}

// paint is the svg's drawing attributes: a fill for Seti's glyphs, Lucide's stroke otherwise.
func (g glyph) paint(stroke string) string {
	if g.fill {
		return `viewBox="` + g.box + `" fill="currentColor"`
	}
	return `viewBox="` + g.box + `" fill="none" stroke="currentColor" stroke-width="` + stroke + `" stroke-linecap="round" stroke-linejoin="round"`
}

// ico is a Lucide icon at size px, its strokes heavier at 12 and under, as the v4 components draw them.
func ico(name string, size int) template.HTML {
	w, sw := strconv.Itoa(size), "2"
	if size <= 12 {
		sw = "2.5"
	}
	g := icons[name]
	//nolint:gosec // the markup is our icon files'
	return template.HTML(`<svg width="` + w + `" height="` + w + `" aria-hidden="true" ` + g.paint(sw) + `>` + g.body + `</svg>`)
}

// icon is a Lucide icon as an inline svg; screen readers skip it, the text next to it names what it shows.
func icon(name, class string) template.HTML {
	g := icons[name]
	//nolint:gosec // the markup is our icon files', the class is escaped
	return template.HTML(`<svg class="` + html.EscapeString(class) + `" aria-hidden="true" ` + g.paint("2") + `>` + g.body + `</svg>`)
}

// look is an icon and its color class (decision 37): color says what kind of thing a row is, never alone, the icon
// and the words beside it say it too.
type look struct{ Icon, Cls string }

// kindLook is an item's kind: findings by severity, then what the reader does with the rest.
func kindLook(kind string) look {
	switch kind {
	case "Critical finding":
		return look{"octagon-alert", "ik-crit"}
	case "Major finding":
		return look{"triangle-alert", "ik-major"}
	case "Difference":
		return look{"git-compare", "ik-diff"}
	case "Before merging":
		return look{"git-merge", "ik-before"}
	case "Test change":
		return look{"flask-conical", "ik-test"}
	case "Gap in the review":
		return look{"eye-off", "ik-off"}
	}
	return look{"circle", "ik-off"}
}

// byExt is a file's Seti icon and its color by extension (decision 41): the language's, as editors draw it.
var byExt = map[string]look{
	".go": {"seti/go2", "fk-cyan"}, ".ts": {"seti/typescript", "fk-blue"}, ".mts": {"seti/typescript", "fk-blue"},
	".cts": {"seti/typescript", "fk-blue"}, ".tsx": {"seti/react", "fk-cyan"}, ".jsx": {"seti/react", "fk-cyan"},
	".js": {"seti/javascript", "fk-yellow"}, ".mjs": {"seti/javascript", "fk-yellow"}, ".cjs": {"seti/javascript", "fk-yellow"},
	".py": {"seti/python", "fk-blue"}, ".rs": {"seti/rust", "fk-orange"}, ".java": {"seti/java", "fk-orange"},
	".rb": {"seti/ruby", "fk-red"}, ".php": {"seti/php", "fk-purple"}, ".c": {"seti/c", "fk-blue"}, ".h": {"seti/c", "fk-blue"},
	".cc": {"seti/cpp", "fk-blue"}, ".cpp": {"seti/cpp", "fk-blue"}, ".cxx": {"seti/cpp", "fk-blue"}, ".hpp": {"seti/cpp", "fk-blue"},
	".cs": {"seti/c-sharp", "fk-purple"}, ".swift": {"seti/swift", "fk-orange"}, ".sql": {"seti/db", "fk-yellow"},
	".prisma": {"seti/prisma", "fk-mute"}, ".json": {"seti/json", "fk-yellow"}, ".yml": {"seti/yml", "fk-purple"},
	".yaml": {"seti/yml", "fk-purple"}, ".toml": {"seti/config", "fk-mute"}, ".ini": {"seti/config", "fk-mute"},
	".cfg": {"seti/config", "fk-mute"}, ".conf": {"seti/config", "fk-mute"}, ".env": {"seti/config", "fk-mute"},
	".xml": {"seti/xml", "fk-orange"}, ".md": {"seti/markdown", "fk-blue"}, ".mdx": {"seti/markdown", "fk-blue"},
	".html": {"seti/html", "fk-orange"}, ".htm": {"seti/html", "fk-orange"}, ".css": {"seti/css", "fk-blue"},
	".scss": {"seti/sass", "fk-purple"}, ".sass": {"seti/sass", "fk-purple"}, ".vue": {"seti/vue", "fk-green"},
	".svelte": {"seti/svelte", "fk-orange"}, ".graphql": {"seti/graphql", "fk-purple"}, ".gql": {"seti/graphql", "fk-purple"},
	".tf": {"seti/terraform", "fk-purple"}, ".tfvars": {"seti/terraform", "fk-purple"}, ".sh": {"seti/shell", "fk-green"},
	".bash": {"seti/shell", "fk-green"}, ".zsh": {"seti/shell", "fk-green"}, ".fish": {"seti/shell", "fk-green"},
	".csv": {"seti/csv", "fk-green"}, ".svg": {"seti/svg", "fk-yellow"}, ".png": {"seti/image", "fk-purple"},
	".jpg": {"seti/image", "fk-purple"}, ".jpeg": {"seti/image", "fk-purple"}, ".gif": {"seti/image", "fk-purple"},
	".webp": {"seti/image", "fk-purple"}, ".lock": {"seti/lock", "fk-mute"}, ".sum": {"seti/lock", "fk-mute"},
}

// fileLook is a file's icon by its path, a place's "path:line" too: a test file the flask (decision 37), a file by
// its name or extension Seti's icon in its language's color, else Seti's plain file. A place that names no file
// ("3 files", "2 queries, and 1 call": a count first) has none.
func fileLook(place string) look {
	p := place
	if n, _, ok := strings.Cut(p, " "); p == "" || ok && n != "" && strings.Trim(n, "0123456789") == "" {
		return look{}
	}
	if i := strings.LastIndexByte(p, ':'); i > 0 && strings.Trim(p[i+1:], "0123456789-") == "" {
		p = p[:i]
	}
	base := strings.ToLower(path.Base(p))
	switch {
	case isTest(p):
		return look{"flask-conical", "fk-cyan"}
	case base == "dockerfile" || strings.HasSuffix(base, ".dockerfile") || strings.HasPrefix(base, "docker-compose"):
		return look{"seti/docker", "fk-blue"}
	case base == "makefile" || base == "gnumakefile":
		return look{"seti/makefile", "fk-orange"}
	case strings.HasPrefix(base, "license") || strings.HasPrefix(base, "copying"):
		return look{"seti/license", "fk-yellow"}
	case strings.HasPrefix(base, "tsconfig") && strings.HasSuffix(base, ".json"):
		return look{"seti/tsconfig", "fk-blue"}
	case base == "package-lock.json" || base == "pnpm-lock.yaml" || base == "yarn.lock":
		return look{"seti/lock", "fk-mute"}
	}
	if l, ok := byExt[path.Ext(base)]; ok {
		return l
	}
	return look{"seti/default", "fk-mute"}
}
