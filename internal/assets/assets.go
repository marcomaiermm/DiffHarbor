// Package assets holds the files the server embeds: the report's favicons and logo, the reviewing banner's lighthouse.
package assets

import "embed"

//go:generate go run lighthouse.go

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
	// Reviewing is the lighthouse the walkthrough shows while a review runs: reviewing.svg follows the OS's color
	// scheme, -light and -dark are fixed for GitHub's theme setting. docs/public serves the same files.
	//go:embed reviewing*.svg
	Reviewing embed.FS
)
