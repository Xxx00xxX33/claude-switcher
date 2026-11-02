package ops

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	s7 = "10000000-0000-4000-8000-000000000007"
	s8 = "10000000-0000-4000-8000-000000000008"
	s9 = "10000000-0000-4000-8000-000000000009"
)

// A chat deleted in A (its marker names the chat's transcript) is deleted from every other
// list, and one undo restores every card byte for byte.
func TestSyncDeletesWhatADeleted(t *testing.T) {
	f, now := chatFixture(t)
	f.card(third, "c3", s3, "Holiday plan", now-90000, "")
	f.put(filepath.Join(f.listDir(work), "deleted_"+s3), fmt.Sprint(now))
	sessions := filepath.Join(f.data, "claude-code-sessions")
	before := f.hash(sessions)
	p, err := f.PlanSync(f.list("work"))
	must(t, err)
	if p.Count(Delete) != 2 || p.Count(Pull) != 0 {
		t.Fatalf("plan should delete c3 from personal and third, pull nothing: %+v", p.Steps)
	}
	_, err = f.Apply(p)
	must(t, err)
	for _, account := range []string{work, pers, third} {
		if f.exists(filepath.Join(f.listDir(account), "local_c3.json")) {
			t.Errorf("%s still holds the chat deleted in A", account)
		}
	}
	_, err = f.Undo()
	must(t, err)
	if f.hash(sessions) != before {
		t.Fatal("undo should restore the deleted cards byte for byte")
	}
}

// A marker naming a transcript the chat had before (a /clear or a resume started a new one)
// counts too. A marker that holds no time is dated by its file.
func TestSyncDeletesByPriorTranscript(t *testing.T) {
	f, now := chatFixture(t)
	f.card(pers, "c3", s3, "Holiday plan", now-60000, `,"priorCliSessionIds":["`+s9+`"]`)
	marker := filepath.Join(f.listDir(work), "deleted_"+s9)
	f.put(marker, "")
	must(t, os.Chtimes(marker, time.UnixMilli(now), time.UnixMilli(now)))
	p, err := f.PlanSync(f.list("work"))
	must(t, err)
	if p.Count(Delete) != 1 || p.Count(Pull) != 0 {
		t.Fatalf("plan should delete c3 by its prior transcript: %+v", p.Steps)
	}
}

// A chat used elsewhere after A deleted it is a conflict: counted, and left alone everywhere.
func TestSyncLeavesConflictsAlone(t *testing.T) {
	f, now := chatFixture(t)
	f.card(third, "c3", s3, "Holiday plan", now-90000, "")
	f.put(filepath.Join(f.listDir(work), "deleted_"+s3), fmt.Sprint(now-120000))
	sessions := filepath.Join(f.data, "claude-code-sessions")
	p, err := f.PlanSync(f.list("work"))
	must(t, err)
	if p.Count(Conflict) != 1 || p.Count(Delete) != 0 || p.Count(Pull) != 0 {
		t.Fatalf("plan should count one conflict and nothing else for c3: %+v", p.Steps)
	}
	persCard, thirdCard := f.read(filepath.Join(f.listDir(pers), "local_c3.json")), f.read(filepath.Join(f.listDir(third), "local_c3.json"))
	_, err = f.Apply(p)
	must(t, err)
	if f.exists(filepath.Join(f.listDir(work), "local_c3.json")) ||
		f.read(filepath.Join(f.listDir(pers), "local_c3.json")) != persCard ||
		f.read(filepath.Join(f.listDir(third), "local_c3.json")) != thirdCard {
		t.Fatalf("a conflict was touched:\n%s", f.hash(sessions))
	}
}

// The sales-account case: A deleted chats whose stale cards linger in other accounts and orgs.
// Sync deletes them; it never brings them back into A, then or on the next sync.
func TestSyncNeverRevivesChatsDeletedInA(t *testing.T) {
	f, now := chatFixture(t)
	org2 := filepath.Join(f.data, "claude-code-sessions", pers, "bbbbbbbb-org2")
	f.put(filepath.Join(org2, "local_c3.json"), f.read(filepath.Join(f.listDir(pers), "local_c3.json")))
	f.card(third, "c8", s8, "Pricing deck", now-500000, "")
	f.card(pers, "c8", s8, "Pricing deck", now-400000, "")
	f.card(pers, "c9", s9, "Lead list", now-300000, `,"priorCliSessionIds":["`+s7+`"]`)
	for _, s := range []string{s3, s8, s7} {
		f.put(filepath.Join(f.listDir(work), "deleted_"+s), fmt.Sprint(now))
	}
	for range 2 {
		p, err := f.PlanSync(f.list("work"))
		must(t, err)
		for _, s := range p.Steps {
			if s.Action != Delete && s.Chat.ID != "local_c1" && s.Chat.ID != "local_c2" {
				t.Fatalf("a chat deleted in A would be revived: %+v", s)
			}
		}
		_, err = f.Apply(p)
		must(t, err)
	}
	for _, dir := range []string{f.listDir(work), f.listDir(pers), f.listDir(third), org2} {
		for _, id := range []string{"c3", "c8", "c9"} {
			if f.exists(filepath.Join(dir, "local_"+id+".json")) {
				t.Errorf("%s still holds %s", dir, id)
			}
		}
	}
	for _, id := range []string{"c1", "c2"} {
		if !f.exists(filepath.Join(f.listDir(pers), "local_"+id+".json")) {
			t.Errorf("A's own chat %s should still be pushed", id)
		}
	}
}
