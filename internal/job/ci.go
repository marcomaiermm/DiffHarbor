package job

import (
	"context"
	"diffharbor/internal/checks"
	"diffharbor/internal/forge"
	"diffharbor/internal/proc"
	"diffharbor/internal/report"
	"log"
)

// ciOf is the report's CI: the forge's checks of sha, read once, and the CI workflows on the base. files are the
// PR's paths. A check the forge couldn't read leaves its error, for the page to say so.
func ciOf(ctx context.Context, p forge.PR, sb *proc.Sandbox, key, sha string, files []string) *report.CI {
	c := &report.CI{}
	c.Tests, c.Workflows = checks.BaseCI(ctx, sb)
	cs, err := p.Checks(sha)
	if err != nil {
		log.Println(key, "ci:", err)
		c.Err = err.Error()
	}
	c.Checks, c.Edited = cs, report.Edits(files, cs, c.Tests)
	return c
}

// membersOf is the report's Members: who has access to the repo now. A 403 leaves its error: not read.
func membersOf(p forge.PR, key string) *report.Members {
	ls, err := p.Members()
	m := &report.Members{Logins: ls}
	if err != nil {
		log.Println(key, "members:", err)
		m.Err = err.Error()
	}
	return m
}
