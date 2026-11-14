package job

import (
	"cmp"
	"context"
	"diffharbor/internal/agent"
	"diffharbor/internal/proc"
	"encoding/json"
	"log"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Spend is a job's line in the usage log, /data/usage/<yyyy-mm>.jsonl: what its model passes used and cost, and where
// its time went. Counts and names only, never what a prompt or reply said.
type Spend struct {
	Time      time.Time          `json:"time"`
	Job       string             `json:"job"` // review, chat, decide
	Key       string             `json:"key"`
	SHA       string             `json:"sha,omitempty"`
	Kind      string             `json:"kind,omitempty"` // a review's: full, incremental
	Result    string             `json:"result"`         // ok, partial (files left unreviewed), stopped, failed
	Secs      float64            `json:"secs"`
	Stages    map[string]float64 `json:"stages,omitempty"` // a review's seconds per stage: checkout, context, analyze, post
	DiffChars int                `json:"diff_chars,omitempty"` // runes
	Findings  int                `json:"findings,omitempty"` // what verify kept
	Dropped   int                `json:"dropped,omitempty"`  // what verify dropped
	OffPush   int                `json:"off_push,omitempty"` // kept, then dropped as off a push's lines
	Cost      float64            `json:"cost"`
	Unread    int                `json:"unread,omitempty"` // sessions that couldn't be read: cost and passes are a floor
	Passes    []agent.Pass       `json:"passes"`
	cur       string             // the running stage, since mark
	mark      time.Time
}

// stage ends the running stage and starts next; "" only ends it. A failed job's last stage ends in logUsage.
func (s *Spend) stage(next string) {
	now := time.Now()
	if s.cur != "" {
		if s.Stages == nil {
			s.Stages = map[string]float64{}
		}
		s.Stages[s.cur] = secs(now.Sub(s.mark))
	}
	s.cur, s.mark = next, now
}

func secs(d time.Duration) float64 { return math.Round(d.Seconds()*10) / 10 }

var usageMu sync.Mutex

// logUsage appends s with the passes of sb's sessions, before sb goes. err is the job's: it makes the result stopped
// or failed, else s.Result stands. A job no model ran in writes nothing.
func logUsage(ctx context.Context, sb *proc.Sandbox, s Spend, err error) {
	s.stage("")
	s.Secs = secs(time.Since(s.Time)) // before the sessions are read: that's not the job's
	// not ctx: a stopped job's passes cost too
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	if s.Passes, s.Unread = agent.Passes(rctx, sb); len(s.Passes)+s.Unread == 0 {
		return
	}
	switch {
	case err != nil && ctx.Err() != nil:
		s.Result = "stopped"
	case err != nil:
		s.Result = "failed"
	}
	for _, p := range s.Passes {
		s.Cost += p.Cost
	}
	s.Cost = math.Round(s.Cost*1e6) / 1e6
	b, _ := json.Marshal(s)
	usageMu.Lock()
	defer usageMu.Unlock()
	// ponytail: one file per month, never pruned; a line is about 1 KB, a busy server writes a few MB a month.
	f := filepath.Join(proc.DataDir, "usage", s.Time.UTC().Format("2006-01")+".jsonl")
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		log.Println("usage:", err)
		return
	}
	w, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // our data dir
	if err == nil {
		_, err = w.Write(append(b, '\n'))
		err = cmp.Or(err, w.Close())
	}
	if err != nil {
		log.Println("usage:", err)
	}
}
