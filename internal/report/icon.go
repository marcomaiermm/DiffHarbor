package report

import (
	"diffharbor/internal/assets"
	"encoding/base64"
	"html"
	"html/template"
	"strconv"
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

// icons are Lucide's (lucide.dev, ISC license, lucide-static 0.577.0): what goes inside each 24×24 svg.
var icons = map[string]string{
	"arrow-down":            `<path d="M12 5v14" /><path d="m19 12-7 7-7-7" />`,
	"arrow-left":            `<path d="m12 19-7-7 7-7" /><path d="M19 12H5" />`,
	"arrow-right":           `<path d="M5 12h14" /><path d="m12 5 7 7-7 7" />`,
	"arrow-right-left":      `<path d="m16 3 4 4-4 4" /><path d="M20 7H4" /><path d="m8 21-4-4 4-4" /><path d="M4 17h16" />`,
	"arrow-up-right":        `<path d="M7 7h10v10" /><path d="M7 17 17 7" />`,
	"braces":                `<path d="M8 3H7a2 2 0 0 0-2 2v5a2 2 0 0 1-2 2 2 2 0 0 1 2 2v5c0 1.1.9 2 2 2h1" /><path d="M16 21h1a2 2 0 0 0 2-2v-5c0-1.1.9-2 2-2a2 2 0 0 1-2-2V5a2 2 0 0 0-2-2h-1" />`,
	"brackets":              `<path d="M16 3h3v18h-3" /><path d="M8 21H5V3h3" />`,
	"check":                 `<path d="M20 6 9 17l-5-5" />`,
	"chevron-down":          `<path d="m6 9 6 6 6-6" />`,
	"chevron-right":         `<path d="m9 18 6-6-6-6" />`,
	"chevrons-down-up":      `<path d="m7 20 5-5 5 5" /><path d="m7 4 5 5 5-5" />`,
	"circle":                `<circle cx="12" cy="12" r="10" />`,
	"circle-alert":          `<circle cx="12" cy="12" r="10" /><path d="M12 8v4" /><path d="M12 16h.01" />`,
	"circle-check":          `<circle cx="12" cy="12" r="10" /><path d="m9 12 2 2 4-4" />`,
	"circle-dashed":         `<circle cx="12" cy="12" r="8" stroke-dasharray="3 3" />`,
	"circle-question-mark":  `<circle cx="12" cy="12" r="10" /><path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3" /><path d="M12 17h.01" />`,
	"circle-slash":          `<circle cx="12" cy="12" r="10" /><line x1="9" x2="15" y1="15" y2="9" />`,
	"circle-x":              `<circle cx="12" cy="12" r="10" /><path d="m15 9-6 6" /><path d="m9 9 6 6" />`,
	"code":                  `<path d="m16 18 6-6-6-6" /><path d="m8 6-6 6 6 6" />`,
	"code-xml":              `<path d="m18 16 4-4-4-4" /><path d="m6 8-4 4 4 4" /><path d="m14.5 4-5 16" />`,
	"columns-2":             `<rect width="18" height="18" x="3" y="3" rx="2" /><path d="M12 3v18" />`,
	"copy":                  `<rect width="14" height="14" x="8" y="8" rx="2" ry="2" /><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2" />`,
	"corner-down-left":      `<path d="M20 4v7a4 4 0 0 1-4 4H4" /><path d="m9 10-5 5 5 5" />`,
	"database":              `<ellipse cx="12" cy="5" rx="9" ry="3" /><path d="M3 5V19A9 3 0 0 0 21 19V5" /><path d="M3 12A9 3 0 0 0 21 12" />`,
	"ellipsis":              `<circle cx="12" cy="12" r="1" /><circle cx="19" cy="12" r="1" /><circle cx="5" cy="12" r="1" />`,
	"ellipsis-vertical":     `<circle cx="12" cy="12" r="1" /><circle cx="12" cy="5" r="1" /><circle cx="12" cy="19" r="1" />`,
	"expand":                `<path d="m15 15 6 6" /><path d="m15 9 6-6" /><path d="M21 16v5h-5" /><path d="M21 8V3h-5" /><path d="M3 16v5h5" /><path d="m3 21 6-6" /><path d="M3 8V3h5" /><path d="M9 9 3 3" />`,
	"external-link":         `<path d="M15 3h6v6" /><path d="M10 14 21 3" /><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6" />`,
	"eye":                   `<path d="M2.062 12.348a1 1 0 0 1 0-.696 10.75 10.75 0 0 1 19.876 0 1 1 0 0 1 0 .696 10.75 10.75 0 0 1-19.876 0" /><circle cx="12" cy="12" r="3" />`,
	"eye-off":               `<path d="M10.733 5.076a10.744 10.744 0 0 1 11.205 6.575 1 1 0 0 1 0 .696 10.747 10.747 0 0 1-1.444 2.49" /><path d="M14.084 14.158a3 3 0 0 1-4.242-4.242" /><path d="M17.479 17.499a10.75 10.75 0 0 1-15.417-5.151 1 1 0 0 1 0-.696 10.75 10.75 0 0 1 4.446-5.143" /><path d="m2 2 20 20" />`,
	"file":                  `<path d="M6 22a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h8a2.4 2.4 0 0 1 1.704.706l3.588 3.588A2.4 2.4 0 0 1 20 8v12a2 2 0 0 1-2 2z" /><path d="M14 2v5a1 1 0 0 0 1 1h5" />`,
	"file-text":             `<path d="M6 22a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h8a2.4 2.4 0 0 1 1.704.706l3.588 3.588A2.4 2.4 0 0 1 20 8v12a2 2 0 0 1-2 2z" /><path d="M14 2v5a1 1 0 0 0 1 1h5" /><path d="M10 9H8" /><path d="M16 13H8" /><path d="M16 17H8" />`,
	"files":                 `<path d="M15 2h-4a2 2 0 0 0-2 2v11a2 2 0 0 0 2 2h8a2 2 0 0 0 2-2V8" /><path d="M16.706 2.706A2.4 2.4 0 0 0 15 2v5a1 1 0 0 0 1 1h5a2.4 2.4 0 0 0-.706-1.706z" /><path d="M5 7a2 2 0 0 0-2 2v11a2 2 0 0 0 2 2h8a2 2 0 0 0 1.732-1" />`,
	"flask-conical":         `<path d="M14 2v6a2 2 0 0 0 .245.96l5.51 10.08A2 2 0 0 1 18 22H6a2 2 0 0 1-1.755-2.96l5.51-10.08A2 2 0 0 0 10 8V2" /><path d="M6.453 15h11.094" /><path d="M8.5 2h7" />`,
	"focus":                 `<circle cx="12" cy="12" r="3" /><path d="M3 7V5a2 2 0 0 1 2-2h2" /><path d="M17 3h2a2 2 0 0 1 2 2v2" /><path d="M21 17v2a2 2 0 0 1-2 2h-2" /><path d="M7 21H5a2 2 0 0 1-2-2v-2" />`,
	"folder":                `<path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z" />`,
	"git-branch":            `<line x1="6" x2="6" y1="3" y2="15" /><circle cx="18" cy="6" r="3" /><circle cx="6" cy="18" r="3" /><path d="M18 9a9 9 0 0 1-9 9" />`,
	"git-commit-horizontal": `<circle cx="12" cy="12" r="3" /><line x1="3" x2="9" y1="12" y2="12" /><line x1="15" x2="21" y1="12" y2="12" />`,
	"git-compare":           `<circle cx="18" cy="18" r="3" /><circle cx="6" cy="6" r="3" /><path d="M13 6h3a2 2 0 0 1 2 2v7" /><path d="M11 18H8a2 2 0 0 1-2-2V9" />`,
	"git-pull-request":      `<circle cx="18" cy="18" r="3" /><circle cx="6" cy="6" r="3" /><path d="M13 6h3a2 2 0 0 1 2 2v7" /><line x1="6" x2="6" y1="9" y2="21" />`,
	"history":               `<path d="M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8" /><path d="M3 3v5h5" /><path d="M12 7v5l4 2" />`,
	"layout-grid":           `<rect width="7" height="7" x="3" y="3" rx="1" /><rect width="7" height="7" x="14" y="3" rx="1" /><rect width="7" height="7" x="14" y="14" rx="1" /><rect width="7" height="7" x="3" y="14" rx="1" />`,
	"link":                  `<path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71" /><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71" />`,
	"link-2":                `<path d="M9 17H7A5 5 0 0 1 7 7h2" /><path d="M15 7h2a5 5 0 1 1 0 10h-2" /><line x1="8" x2="16" y1="12" y2="12" />`,
	"list":                  `<path d="M3 5h.01" /><path d="M3 12h.01" /><path d="M3 19h.01" /><path d="M8 5h13" /><path d="M8 12h13" /><path d="M8 19h13" />`,
	"maximize":              `<path d="M8 3H5a2 2 0 0 0-2 2v3" /><path d="M21 8V5a2 2 0 0 0-2-2h-3" /><path d="M3 16v3a2 2 0 0 0 2 2h3" /><path d="M16 21h3a2 2 0 0 0 2-2v-3" />`,
	"minus":                 `<path d="M5 12h14" />`,
	"octagon":               `<polygon points="7.86 2 16.14 2 22 7.86 22 16.14 16.14 22 7.86 22 2 16.14 2 7.86 7.86 2" />`,
	"panel-left":            `<rect width="18" height="18" x="3" y="3" rx="2" /><path d="M9 3v18" />`,
	"parentheses":           `<path d="M8 21s-4-3-4-9 4-9 4-9" /><path d="M16 3s4 3 4 9-4 9-4 9" />`,
	"pause":                 `<rect x="14" y="4" width="4" height="16" rx="1" /><rect x="6" y="4" width="4" height="16" rx="1" />`,
	"play":                  `<polygon points="6 3 20 12 6 21 6 3" />`,
	"plus":                  `<path d="M5 12h14" /><path d="M12 5v14" />`,
	"quote":                 `<path d="M16 3a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2 1 1 0 0 1 1 1v1a2 2 0 0 1-2 2 1 1 0 0 0-1 1v2a1 1 0 0 0 1 1 6 6 0 0 0 6-6V5a2 2 0 0 0-2-2z" /><path d="M5 3a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2 1 1 0 0 1 1 1v1a2 2 0 0 1-2 2 1 1 0 0 0-1 1v2a1 1 0 0 0 1 1 6 6 0 0 0 6-6V5a2 2 0 0 0-2-2z" />`,
	"repeat":                `<path d="m17 2 4 4-4 4" /><path d="M3 11v-1a4 4 0 0 1 4-4h14" /><path d="m7 22-4-4 4-4" /><path d="M21 13v1a4 4 0 0 1-4 4H3" />`,
	"rotate-ccw":            `<path d="M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8" /><path d="M3 3v5h5" />`,
	"rows-2":                `<rect width="18" height="18" x="3" y="3" rx="2" /><path d="M3 12h18" />`,
	"search":                `<path d="m21 21-4.34-4.34" /><circle cx="11" cy="11" r="8" />`,
	"slash":                 `<path d="M22 2 2 22" />`,
	"sparkles":              `<path d="M11.017 2.814a1 1 0 0 1 1.966 0l1.051 5.558a2 2 0 0 0 1.594 1.594l5.558 1.051a1 1 0 0 1 0 1.966l-5.558 1.051a2 2 0 0 0-1.594 1.594l-1.051 5.558a1 1 0 0 1-1.966 0l-1.051-5.558a2 2 0 0 0-1.594-1.594l-5.558-1.051a1 1 0 0 1 0-1.966l5.558-1.051a2 2 0 0 0 1.594-1.594z" /><path d="M20 2v4" /><path d="M22 4h-4" /><circle cx="4" cy="20" r="2" />`,
	"split":                 `<path d="M16 3h5v5" /><path d="M8 3H3v5" /><path d="M12 22v-8.3a4 4 0 0 0-1.172-2.872L3 3" /><path d="m15 9 6-6" />`,
	"square-function":       `<rect width="18" height="18" x="3" y="3" rx="2" ry="2" /><path d="M9 17c2 0 2.8-1 2.8-2.8V10c0-2 1-3.3 3.2-3" /><path d="M9 11.2h5.7" />`,
	"square-plus":           `<rect width="18" height="18" x="3" y="3" rx="2" /><path d="M8 12h8" /><path d="M12 8v8" />`,
	"ticket":                `<path d="M2 9a3 3 0 0 1 0 6v2a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-2a3 3 0 0 1 0-6V7a2 2 0 0 0-2-2H4a2 2 0 0 0-2 2Z" /><path d="M13 5v2" /><path d="M13 17v2" /><path d="M13 11v2" />`,
	"trash-2":               `<path d="M10 11v6" /><path d="M14 11v6" /><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6" /><path d="M3 6h18" /><path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />`,
	"triangle-alert":        `<path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3" /><path d="M12 9v4" /><path d="M12 17h.01" />`,
	"user-x":                `<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" /><circle cx="9" cy="7" r="4" /><line x1="17" x2="22" y1="8" y2="13" /><line x1="22" x2="17" y1="8" y2="13" />`,
	"variable":              `<path d="M8 21s-4-3-4-9 4-9 4-9" /><path d="M16 3s4 3 4 9-4 9-4 9" /><line x1="15" x2="9" y1="9" y2="15" /><line x1="9" x2="15" y1="9" y2="15" />`,
	"workflow":              `<rect width="8" height="8" x="3" y="3" rx="2" /><path d="M7 11v4a2 2 0 0 0 2 2h4" /><rect width="8" height="8" x="13" y="13" rx="2" />`,
	"x":                     `<path d="M18 6 6 18" /><path d="m6 6 12 12" />`,
}

// ico is a Lucide icon at size px, its strokes heavier at 12 and under, as the v4 components draw them.
func ico(name string, size int) template.HTML {
	w, sw := strconv.Itoa(size), "2"
	if size <= 12 {
		sw = "2.5"
	}
	//nolint:gosec // the markup is the constant above
	return template.HTML(`<svg width="` + w + `" height="` + w + `" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" ` +
		`stroke-width="` + sw + `" stroke-linecap="round" stroke-linejoin="round">` + icons[name] + `</svg>`)
}

// icon is a Lucide icon as an inline svg; screen readers skip it, the text next to it names what it shows.
func icon(name, class string) template.HTML {
	//nolint:gosec // the markup is the constant above, the class is escaped
	return template.HTML(`<svg class="` + html.EscapeString(class) + `" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" ` +
		`stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` + icons[name] + `</svg>`)
}
