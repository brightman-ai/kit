package transcript

import (
	"context"
	"os"
	"testing"
)

func TestProjectAgentRuns_RealCodexRollout(t *testing.T) {
	if os.Getenv("KIT_REALDATA_PROJECT") == "" {
		t.Skip("needs real data")
	}
	src := NewCodexSource()
	metas, err := src.ListSessions(context.Background(), os.Getenv("KIT_REALDATA_PROJECT"))
	if err != nil || len(metas) == 0 {
		t.Skipf("no codex sessions (%v)", err)
	}
	big := metas[0]
	for _, m := range metas {
		if m.TurnCount > big.TurnCount {
			big = m
		}
	}
	tr, err := src.LoadTranscript(context.Background(), SessionRef{ID: big.ID})
	if err != nil {
		t.Fatal(err)
	}
	runs := ProjectAgentRuns(tr)
	amend, completed, interrupted, unterm := 0, 0, 0, 0
	for _, r := range runs {
		amend += len(r.Amendments)
		switch r.Status {
		case RunCompleted:
			completed++
		case RunInterrupted:
			interrupted++
		case RunUnterminated:
			unterm++
		}
	}
	t.Logf("real codex: %d runs (%d completed / %d interrupted / %d unterminated), %d amendments, from %d raw turns",
		len(runs), completed, interrupted, unterm, amend, len(tr.Turns))
	// The point of the projection: a rollout with 2500+ raw turns is a handful of rounds.
	if len(runs) == 0 || len(runs) >= len(tr.Turns)/10 {
		t.Errorf("projection did not aggregate: %d runs from %d raw turns", len(runs), len(tr.Turns))
	}
	// Verified against the raw rollout: the human DID steer long-running tasks (assistant
	// messages keep flowing between the interjections, with no task_complete) — so
	// amendments are expected, and they must NOT have become rounds of their own.
	for _, r := range runs {
		for _, a := range r.Amendments {
			if a.Text == "" {
				t.Errorf("empty amendment in run #%d", r.Index)
			}
		}
	}
	for i, r := range runs {
		if r.UserIntent != nil {
			t.Logf("  run#%d [%s] %.40s (segments=%d, amendments=%d)", r.Index, r.Status, r.UserIntent.Text, len(r.Segments), len(r.Amendments))
		} else {
			t.Logf("  run#%d [%s] <no intent> segments=%d", i, r.Status, len(r.Segments))
		}
	}
}
