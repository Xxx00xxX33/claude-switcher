package ops

import (
	"fmt"
	"path/filepath"
	"testing"
)

// conflictFixture is the conflict of TestSyncLeavesConflictsAlone: work (A) deleted c3, but
// personal (newest) and third used it after.
func conflictFixture(t *testing.T) (*fixture, int64) {
	f, now := chatFixture(t)
	f.card(third, "c3", s3, "Holiday plan", now-90000, "")
	f.put(filepath.Join(f.listDir(work), "deleted_"+s3), fmt.Sprint(now-120000))
	return f, now
}

// "Delete anyway" turns a conflict into deletions from every list that holds the chat, and one
// undo brings every card back.
func TestResolvedConflictDeletesEverywhere(t *testing.T) {
	f, _ := conflictFixture(t)
	sessions := filepath.Join(f.data, "claude-code-sessions")
	before := f.hash(sessions)
	p, err := f.PlanSync(f.list("work"))
	must(t, err)
	p, err = f.Resolve(p, map[string]bool{"local_c3": false})
	must(t, err)
	if p.Count(Conflict) != 0 || p.Count(Delete) != 2 {
		t.Fatalf("the conflict should become two deletions: %+v", p.Steps)
	}
	_, err = f.Apply(p)
	must(t, err)
	for _, account := range []string{work, pers, third} {
		if f.exists(filepath.Join(f.listDir(account), "local_c3.json")) {
			t.Errorf("%s still holds the chat deleted anyway", account)
		}
	}
	_, err = f.Undo()
	must(t, err)
	if f.hash(sessions) != before {
		t.Fatal("undo should restore the deleted cards byte for byte")
	}
}

// "Keep and pull into A" pulls the newest copy into A and out to every list, as if A had never
// deleted it.
func TestResolvedConflictKeepsAndPulls(t *testing.T) {
	f, now := conflictFixture(t)
	p, err := f.PlanSync(f.list("work"))
	must(t, err)
	p, err = f.Resolve(p, map[string]bool{"local_c3": true})
	must(t, err)
	if p.Count(Conflict) != 0 || p.Count(Pull) != 1 || p.Count(Delete) != 0 {
		t.Fatalf("the conflict should become a pull into A: %+v", p.Steps)
	}
	_, err = f.Apply(p)
	must(t, err)
	for _, account := range []string{"work", "personal", "33333333"} {
		chats, err := f.Chats(f.list(account))
		must(t, err)
		found := false
		for _, c := range chats {
			if c.ID == "local_c3" {
				found = true
				if c.Last.UnixMilli() != now-60000 {
					t.Errorf("%s holds c3 used at %d, want personal's newest copy", account, c.Last.UnixMilli())
				}
			}
		}
		if !found {
			t.Errorf("%s should hold the kept chat", account)
		}
	}
}

// A conflict left out of the choices stays a conflict, which Apply leaves alone.
func TestUnresolvedConflictStays(t *testing.T) {
	f, _ := conflictFixture(t)
	p, err := f.PlanSync(f.list("work"))
	must(t, err)
	n := len(p.Steps)
	p, err = f.Resolve(p, nil)
	must(t, err)
	if p.Count(Conflict) != 1 || len(p.Steps) != n {
		t.Fatalf("an unresolved conflict should stay as it is: %+v", p.Steps)
	}
}
