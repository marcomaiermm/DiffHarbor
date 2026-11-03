package report

import (
	"html"
	"html/template"
)

// icons are Lucide's (lucide.dev, ISC license, lucide-static 0.577.0): what goes inside each 24×24 svg.
var icons = map[string]string{
	"arrow-down":            `<path d="M12 5v14" /><path d="m19 12-7 7-7-7" />`,
	"arrow-right-left":      `<path d="m16 3 4 4-4 4" /><path d="M20 7H4" /><path d="m8 21-4-4 4-4" /><path d="M4 17h16" />`,
	"arrow-up-right":        `<path d="M7 7h10v10" /><path d="M7 17 17 7" />`,
	"check":                 `<path d="M20 6 9 17l-5-5" />`,
	"chevron-down":          `<path d="m6 9 6 6 6-6" />`,
	"chevron-right":         `<path d="m9 18 6-6-6-6" />`,
	"chevrons-down-up":      `<path d="m7 20 5-5 5 5" /><path d="m7 4 5 5 5-5" />`,
	"circle-question-mark":  `<circle cx="12" cy="12" r="10" /><path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3" /><path d="M12 17h.01" />`,
	"columns-2":             `<rect width="18" height="18" x="3" y="3" rx="2" /><path d="M12 3v18" />`,
	"copy":                  `<rect width="14" height="14" x="8" y="8" rx="2" ry="2" /><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2" />`,
	"corner-down-left":      `<path d="M20 4v7a4 4 0 0 1-4 4H4" /><path d="m9 10-5 5 5 5" />`,
	"expand":                `<path d="m15 15 6 6" /><path d="m15 9 6-6" /><path d="M21 16v5h-5" /><path d="M21 8V3h-5" /><path d="M3 16v5h5" /><path d="m3 21 6-6" /><path d="M3 8V3h5" /><path d="M9 9 3 3" />`,
	"eye":                   `<path d="M2.062 12.348a1 1 0 0 1 0-.696 10.75 10.75 0 0 1 19.876 0 1 1 0 0 1 0 .696 10.75 10.75 0 0 1-19.876 0" /><circle cx="12" cy="12" r="3" />`,
	"eye-off":               `<path d="M10.733 5.076a10.744 10.744 0 0 1 11.205 6.575 1 1 0 0 1 0 .696 10.747 10.747 0 0 1-1.444 2.49" /><path d="M14.084 14.158a3 3 0 0 1-4.242-4.242" /><path d="M17.479 17.499a10.75 10.75 0 0 1-15.417-5.151 1 1 0 0 1 0-.696 10.75 10.75 0 0 1 4.446-5.143" /><path d="m2 2 20 20" />`,
	"file":                  `<path d="M6 22a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h8a2.4 2.4 0 0 1 1.704.706l3.588 3.588A2.4 2.4 0 0 1 20 8v12a2 2 0 0 1-2 2z" /><path d="M14 2v5a1 1 0 0 0 1 1h5" />`,
	"file-text":             `<path d="M6 22a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h8a2.4 2.4 0 0 1 1.704.706l3.588 3.588A2.4 2.4 0 0 1 20 8v12a2 2 0 0 1-2 2z" /><path d="M14 2v5a1 1 0 0 0 1 1h5" /><path d="M10 9H8" /><path d="M16 13H8" /><path d="M16 17H8" />`,
	"files":                 `<path d="M15 2h-4a2 2 0 0 0-2 2v11a2 2 0 0 0 2 2h8a2 2 0 0 0 2-2V8" /><path d="M16.706 2.706A2.4 2.4 0 0 0 15 2v5a1 1 0 0 0 1 1h5a2.4 2.4 0 0 0-.706-1.706z" /><path d="M5 7a2 2 0 0 0-2 2v11a2 2 0 0 0 2 2h8a2 2 0 0 0 1.732-1" />`,
	"flask-conical":         `<path d="M14 2v6a2 2 0 0 0 .245.96l5.51 10.08A2 2 0 0 1 18 22H6a2 2 0 0 1-1.755-2.96l5.51-10.08A2 2 0 0 0 10 8V2" /><path d="M6.453 15h11.094" /><path d="M8.5 2h7" />`,
	"focus":                 `<circle cx="12" cy="12" r="3" /><path d="M3 7V5a2 2 0 0 1 2-2h2" /><path d="M17 3h2a2 2 0 0 1 2 2v2" /><path d="M21 17v2a2 2 0 0 1-2 2h-2" /><path d="M7 21H5a2 2 0 0 1-2-2v-2" />`,
	"folder":                `<path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z" />`,
	"git-commit-horizontal": `<circle cx="12" cy="12" r="3" /><line x1="3" x2="9" y1="12" y2="12" /><line x1="15" x2="21" y1="12" y2="12" />`,
	"git-pull-request":      `<circle cx="18" cy="18" r="3" /><circle cx="6" cy="6" r="3" /><path d="M13 6h3a2 2 0 0 1 2 2v7" /><line x1="6" x2="6" y1="9" y2="21" />`,
	"layout-grid":           `<rect width="7" height="7" x="3" y="3" rx="1" /><rect width="7" height="7" x="14" y="3" rx="1" /><rect width="7" height="7" x="14" y="14" rx="1" /><rect width="7" height="7" x="3" y="14" rx="1" />`,
	"list":                  `<path d="M3 5h.01" /><path d="M3 12h.01" /><path d="M3 19h.01" /><path d="M8 5h13" /><path d="M8 12h13" /><path d="M8 19h13" />`,
	"maximize":              `<path d="M8 3H5a2 2 0 0 0-2 2v3" /><path d="M21 8V5a2 2 0 0 0-2-2h-3" /><path d="M3 16v3a2 2 0 0 0 2 2h3" /><path d="M16 21h3a2 2 0 0 0 2-2v-3" />`,
	"panel-left":            `<rect width="18" height="18" x="3" y="3" rx="2" /><path d="M9 3v18" />`,
	"rotate-ccw":            `<path d="M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8" /><path d="M3 3v5h5" />`,
	"rows-2":                `<rect width="18" height="18" x="3" y="3" rx="2" /><path d="M3 12h18" />`,
	"search":                `<path d="m21 21-4.34-4.34" /><circle cx="11" cy="11" r="8" />`,
	"sparkles":              `<path d="M11.017 2.814a1 1 0 0 1 1.966 0l1.051 5.558a2 2 0 0 0 1.594 1.594l5.558 1.051a1 1 0 0 1 0 1.966l-5.558 1.051a2 2 0 0 0-1.594 1.594l-1.051 5.558a1 1 0 0 1-1.966 0l-1.051-5.558a2 2 0 0 0-1.594-1.594l-5.558-1.051a1 1 0 0 1 0-1.966l5.558-1.051a2 2 0 0 0 1.594-1.594z" /><path d="M20 2v4" /><path d="M22 4h-4" /><circle cx="4" cy="20" r="2" />`,
	"slash":                 `<path d="M22 2 2 22" />`,
	"trash-2":               `<path d="M10 11v6" /><path d="M14 11v6" /><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6" /><path d="M3 6h18" /><path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />`,
	"triangle-alert":        `<path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3" /><path d="M12 9v4" /><path d="M12 17h.01" />`,
	"workflow":              `<rect width="8" height="8" x="3" y="3" rx="2" /><path d="M7 11v4a2 2 0 0 0 2 2h4" /><rect width="8" height="8" x="13" y="13" rx="2" />`,
	"x":                     `<path d="M18 6 6 18" /><path d="m6 6 12 12" />`,
}

// icon is a Lucide icon as an inline svg; screen readers skip it, the text next to it names what it shows.
func icon(name, class string) template.HTML {
	//nolint:gosec // the markup is the constant above, the class is escaped
	return template.HTML(`<svg class="` + html.EscapeString(class) + `" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" ` +
		`stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` + icons[name] + `</svg>`)
}
