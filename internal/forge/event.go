package forge

import (
	"fmt"
	"strings"
)

// Event is the part of a webhook payload the bot reads, in GitHub's shape: the GitLab side fills it from its own
// payloads. Kind is GitHub's event name (pull_request, issue_comment, pull_request_review_comment).
type Event struct {
	Kind         string `json:"-"`
	Forge        string `json:"forge"` // github or gitlab
	Action       string `json:"action"`
	After        string `json:"after"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"` // GitLab: path_with_namespace, may have subgroups
		CloneURL string `json:"clone_url"`
	} `json:"repository"`
	PullRequest struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		Draft   bool   `json:"draft"`
		State   string `json:"state"`
		// false: it conflicts with the base; nil until GitHub has worked it out, and in webhook payloads
		Mergeable *bool `json:"mergeable"`
		User      struct {
			Type string `json:"type"` // "Bot" for GitHub Apps: Dependabot, Renovate, release-please
		} `json:"user"`
		Head struct {
			SHA  string `json:"sha"`
			Ref  string `json:"ref"` // branch, may name a ticket
			Repo struct {
				ID int64 `json:"id"` // not the base repo's: a fork (0: the fork is gone)
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
	Issue struct {
		Number      int       `json:"number"`
		PullRequest *struct{} `json:"pull_request"` // set when the issue is a PR
	} `json:"issue"`
	Comment struct {
		ID                int64  `json:"id"`
		InReplyTo         int64  `json:"in_reply_to_id"` // GitLab: the first note of the comment's discussion
		Discussion        string `json:"discussion_id"`  // GitLab only
		Body              string `json:"body"`
		Path              string `json:"path"`
		AuthorAssociation string `json:"author_association"` // GitLab: MEMBER for maintainers, COLLABORATOR for developers
		Ours              bool   `json:"-"`                  // the bot wrote it
		User              struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"user"`
	} `json:"comment"`
	Sender struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"sender"`
	Changes struct { // an edited comment's old body
		Body struct {
			From string `json:"from"`
		} `json:"body"`
	} `json:"changes"`
}

// Repo names the repo across forges: owner/repo on GitHub, gitlab/group/project on GitLab.
func (e *Event) Repo() string {
	if e.Forge == "" || e.Forge == "github" {
		return e.Repository.FullName
	}
	return e.Forge + "/" + e.Repository.FullName
}

// Owner is the repo's owner, or its top-level group, named like Repo: learnings shared by its repos.
func (e *Event) Owner() string {
	owner, _, _ := strings.Cut(e.Repository.FullName, "/")
	if e.Forge == "" || e.Forge == "github" {
		return owner
	}
	return e.Forge + "/" + owner
}

// Key names the PR: owner/repo#n.
func (e *Event) Key() string {
	return fmt.Sprintf("%s#%d", e.Repo(), e.PullRequest.Number)
}
