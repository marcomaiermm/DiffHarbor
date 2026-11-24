package report

import (
	"cmp"
	"diffharbor/internal/proc"
	_ "embed"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

//go:embed tsedges.cjs
var tsEdgesJS string

// tsAPI is TypeScript's package, 7.1 or newer: its API runs the native checker.
var tsAPI = cmp.Or(os.Getenv("DIFFHARBOR_TS"), "/usr/local/lib/node_modules/typescript")

// tsEdges adds what the TypeScript checker resolves in the TS projects of the indexes (tsRoots), all of it, a walk goes on from any: calls by type and callbacks, JSX renders, routes and their handlers,
// reads and writes of stores; see tsedges.cjs. A project the checker can't load, or past runNode's minute, adds none: codegraph's alone.
func (idx *cgIndex) tsEdges(sb *proc.Sandbox, repo string) {
	if _, err := os.Stat(filepath.Join(tsAPI, "dist", "api", "sync", "api.js")); err != nil {
		return
	}
	roots := idx.tsRoots(repo)
	if len(roots) == 0 && slices.ContainsFunc(idx.paths, func(p string) bool { return jsExt[path.Ext(p)] }) {
		log.Print("ts: codegraph alone: no tsconfig.json beside an index or above a changed file")
	}
	for _, prefix := range roots {
		start := time.Now()
		var got []cgEdge
		if !runNode(sb, tsEdgesJS, map[string]any{"dir": filepath.Join(repo, filepath.FromSlash(prefix)), "ts": tsAPI}, &got) {
			log.Printf("ts %s: codegraph alone: the checker failed or took past a minute", prefix)
			continue
		}
		for _, e := range got {
			e.SPath = prefix + e.SPath
			if e.TPath != "" {
				e.TPath = prefix + e.TPath
			}
			idx.Edges = append(idx.Edges, e)
		}
		log.Printf("ts %s: %d edges in %s", prefix, len(got), time.Since(start).Round(time.Millisecond))
	}
}

// tsRoots is the directories of the TS projects to check, as prefixes: an index's own with a tsconfig.json beside
// it, which lists or references the rest; else, in a monorepo whose root has none, the nearest one above each
// changed file.
// ponytail: then a project no changed file sits in isn't checked, and its calls into the change stay codegraph's;
// read the workspaces if that's missed.
func (idx *cgIndex) tsRoots(repo string) []string {
	has := func(prefix string) bool {
		_, err := os.Stat(filepath.Join(repo, filepath.FromSlash(prefix), "tsconfig.json"))
		return err == nil
	}
	var roots []string
	for _, d := range idx.dbs {
		if has(d.Prefix) {
			roots = append(roots, d.Prefix)
			continue
		}
		for _, p := range idx.paths {
			if !jsExt[path.Ext(p)] || !strings.HasPrefix(p, d.Prefix) {
				continue
			}
			for dir := path.Dir(p); dir != "." && dir+"/" != d.Prefix; dir = path.Dir(dir) {
				if has(dir + "/") {
					if !slices.Contains(roots, dir+"/") {
						roots = append(roots, dir+"/")
					}
					break
				}
			}
		}
	}
	return roots
}
