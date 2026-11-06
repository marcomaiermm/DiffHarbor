package job

import (
	"context"
	"diffharbor/internal/forge"
	"diffharbor/internal/proc"
	"diffharbor/internal/review"
	"errors"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// chatFake answers the chat pass with reply and keeps the prompt it got.
type chatFake struct{ reply, prompt string }

func (f *chatFake) Run(_ context.Context, _, _, _, prompt, _, _ string) (string, error) {
	f.prompt = prompt
	return f.reply, nil
}
func (f *chatFake) Continue(context.Context, string, string, string, string, string) (string, error) {
	return f.reply, nil
}

// chatPR is a reply in our thread; log records what Chat posts and resolves, in order.
type chatPR struct {
	skipPR
	write              bool
	writeErr, replyErr error
	resolveErr         error
	log                []string
}

func (*chatPR) Discussion() (forge.Discussion, error) {
	return forge.Discussion{Ours: true, Notes: []forge.Note{{ID: 1, Login: "bot", Body: "finding", Ours: true}, {ID: 2, Login: "ann", Body: "wrong"}}}, nil
}
func (p *chatPR) CanWrite(string) (bool, error) { return p.write, p.writeErr }
func (*chatPR) React()                          {}
func (*chatPR) ReadToken() (string, error)      { return "t", nil }
func (*chatPR) Skills(string, []string, string) {}
func (p *chatPR) File(string, ...string) string { return "" }
func (p *chatPR) Reply(b string) error {
	if p.replyErr != nil {
		return p.replyErr
	}
	p.log = append(p.log, "reply "+b)
	return nil
}
func (p *chatPR) ResolveDiscussion() error {
	if p.resolveErr != nil {
		return p.resolveErr
	}
	p.log = append(p.log, "resolve")
	return nil
}

type chatForge struct {
	skipForge
	pr *chatPR
}

func (chatForge) Instance() string                      { return "test" }
func (f chatForge) Open(*forge.Event) (forge.PR, error) { return f.pr, nil }

// The concession after a pushback: only a maintainer's decline resolves, after the reply is up.
func TestChatResolve(t *testing.T) {
	defer func(pi bool, a review.Agent) { proc.AsPi, chatAgent = pi, a }(proc.AsPi, chatAgent)
	proc.AsPi, proc.DataDir = false, t.TempDir()
	origin := t.TempDir()
	c := exec.Command("sh", "-c", `set -e; git init -q -b main; git config uploadpack.allowAnySHA1InWant true
echo 1 > a.txt; git add .; git commit -q -m base; git checkout -q -b pr; echo 2 > a.txt; git commit -q -am pr; git rev-parse pr`)
	c.Dir = origin
	c.Env = append(c.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.Output()
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(string(out))

	conceded := `{"reply":"Your call.","learning":"","shared":false,"resolved":true}`
	for _, tc := range []struct {
		name    string
		pr      chatPR
		err     bool
		log     []string // the start of each reply, "resolve"
		context string   // in the prompt
	}{
		{"maintainer", chatPR{write: true}, false, []string{"reply Your call.", "resolve"}, ""},
		{"contributor", chatPR{}, false, []string{"reply Your call."}, "has no write access"},
		{"unknown access", chatPR{write: true, writeErr: errors.New("503")}, false, []string{"reply Your call."}, "write access is unknown"},
		{"reply fails", chatPR{write: true, replyErr: errors.New("502")}, true, nil, ""},
		{"resolve fails", chatPR{write: true, resolveErr: errors.New("502")}, false, []string{"reply Your call.", "reply ⚠️ _Could not resolve"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &chatFake{reply: conceded}
			chatAgent = fake
			e := &forge.Event{Kind: "pull_request_review_comment", Forge: "github"}
			e.Repository.ID, e.Repository.CloneURL = 1, "file://"+origin
			e.PullRequest.Head.SHA, e.PullRequest.Base.Ref = head, "main"
			e.Comment.ID, e.Comment.Body, e.Comment.User.Login = 2, "wrong", "ann"
			pr := tc.pr
			err := Chat(context.Background(), chatForge{pr: &pr}, regexp.MustCompile(`@bot`), e)
			if (err != nil) != tc.err {
				t.Fatal(err)
			}
			if len(pr.log) != len(tc.log) {
				t.Fatalf("got %q, want %q", pr.log, tc.log)
			}
			for i, w := range tc.log {
				if !strings.HasPrefix(pr.log[i], w) {
					t.Errorf("%d: got %q, want %q…", i, pr.log[i], w)
				}
			}
			if resolved := strings.Contains(strings.Join(pr.log, ""), "Thread resolved"); resolved != (tc.name == "maintainer" || tc.name == "resolve fails") {
				t.Errorf("claims resolved: %v", resolved)
			}
			if !strings.Contains(fake.prompt, tc.context) {
				t.Errorf("prompt lacks %q", tc.context)
			}
		})
	}
}
