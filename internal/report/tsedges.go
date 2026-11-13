package report

import (
	"cmp"
	"diffharbor/internal/proc"
	_ "embed"
	"log"
	"os"
	"path/filepath"
	"time"
)

//go:embed tsedges.js
var tsEdgesJS string

// tsAPI is TypeScript's package, 7.1 or newer: its API runs the native checker.
var tsAPI = cmp.Or(os.Getenv("DIFFHARBOR_TS"), "/usr/local/lib/node_modules/typescript")

// tsEdges adds what the TypeScript checker resolves in the TS projects of the indexes (a tsconfig.json beside the
// index), all of it, a walk goes on from any: calls by type and callbacks, JSX renders, routes and their handlers,
// reads and writes of stores; see tsedges.js. A project the checker can't load, or past runNode's minute, adds none: codegraph's alone.
func (idx *cgIndex) tsEdges(sb *proc.Sandbox, repo string) {
	if _, err := os.Stat(filepath.Join(tsAPI, "dist", "api", "sync", "api.js")); err != nil {
		return
	}
	for _, d := range idx.dbs {
		dir := filepath.Join(repo, filepath.FromSlash(d.Prefix))
		if _, err := os.Stat(filepath.Join(dir, "tsconfig.json")); err != nil {
			continue
		}
		start := time.Now()
		var got []cgEdge
		if !runNode(sb, tsEdgesJS, map[string]any{"dir": dir, "ts": tsAPI}, &got) {
			log.Printf("ts %s: codegraph alone: the checker failed or took past a minute", d.Prefix)
			continue
		}
		for _, e := range got {
			e.SPath = d.Prefix + e.SPath
			if e.TPath != "" {
				e.TPath = d.Prefix + e.TPath
			}
			idx.Edges = append(idx.Edges, e)
		}
		log.Printf("ts %s: %d edges in %s", d.Prefix, len(got), time.Since(start).Round(time.Millisecond))
	}
}
