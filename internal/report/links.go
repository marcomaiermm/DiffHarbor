package report

import (
	"crypto/sha256"
	"diffharbor/internal/review"
	"fmt"
	"strconv"
)

// ThreadKey names a finding for Data.Threads and forge.PR.Posted: where and what, as the model wrote it.
func ThreadKey(c review.Comment) string { return fmt.Sprintf("%s:%d:%s", c.Path, c.Line, c.Title) }

// anchor is GitHub's id of a file's section in the files view: the SHA-256 of its path.
func anchor(path string) string { return fmt.Sprintf("diff-%x", sha256.Sum256([]byte(path))) }

// File links path in the PR's files view when the PR changes it (changed), else the file at the head. "" when the
// forge has neither link.
func (f Forge) File(path string, changed bool) string {
	if changed && f.Files != "" {
		return f.Files + "#" + anchor(path)
	}
	if f.Blob == "" {
		return ""
	}
	return f.Blob + path
}

// Line links a line of path: in the files view when it is in one of the PR's hunks (lines is diff.NewLines of the
// diff), else the file at the head.
func (f Forge) Line(path string, line int, lines map[string]string) string {
	if line <= 0 { // an outdated earlier thread has none
		return f.File(path, false)
	}
	if _, ok := lines[path+":"+strconv.Itoa(line)]; ok && f.Files != "" {
		return f.Files + "#" + anchor(path) + "R" + strconv.Itoa(line)
	}
	if f.Blob == "" {
		return ""
	}
	return f.Blob + path + "#L" + strconv.Itoa(line)
}

// ThreadURL links the inline comment id, "" for none.
func (f Forge) ThreadURL(id int64) string {
	if id == 0 || f.Thread == "" {
		return ""
	}
	return f.Thread + strconv.FormatInt(id, 10)
}
