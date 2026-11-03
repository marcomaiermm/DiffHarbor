// Package assets holds the files the report page embeds: the favicons and the logo.
package assets

import _ "embed" // the files below

var (
	// FaviconSVG is the page's icon.
	//go:embed favicon.svg
	FaviconSVG []byte
	// FaviconPNG is the icon for browsers without SVG icons, 32 px.
	//go:embed favicon-32x32.png
	FaviconPNG []byte
	// Logo is the report header's logo, 64 px.
	//go:embed logo.png
	Logo []byte
)
