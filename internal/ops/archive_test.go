package ops

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// Archiving a chat in A archives it in every list, keeping every other field of the card.
func TestSyncArchivesWhatAArchived(t *testing.T) {
	f, now := chatFixture(t)
	f.card(work, "c9", s3, "Trip", now-1000, `,"isStarred":true,"isArchived":true`)
	f.card(pers, "c9", s3, "Trip", now-1000, `,"isStarred":true,"isArchived":false`)
	p, err := f.PlanSync(f.list("work"))
	must(t, err)
	if n := p.Count(Archive); n != 1 {
		t.Errorf("plan archives %d chats, want 1", n)
	}
	_, err = f.Apply(p)
	must(t, err)
	want := `{"sessionId":"local_c9","cliSessionId":"` + s3 + `","cwd":` + str(proj) + `,"title":"Trip","lastActivityAt":` + fmt.Sprint(now-1000) + `,"isStarred":true,"isArchived":true}`
	if got := f.read(filepath.Join(f.listDir(pers), "local_c9.json")); got != want {
		t.Errorf("personal holds\n%s\nwant\n%s", got, want)
	}
}

// Unarchiving a chat in A unarchives it in every list.
func TestSyncUnarchivesWhatAUnarchived(t *testing.T) {
	f, now := chatFixture(t)
	f.card(work, "c9", s3, "Trip", now-1000, `,"isArchived":false`)
	f.card(pers, "c9", s3, "Trip", now-1000, `,"isArchived":true`)
	f.card(third, "c9", s3, "Trip", now-1000, `,"isArchived":true`)
	p, err := f.PlanSync(f.list("work"))
	must(t, err)
	if n, chats := p.Count(Unarchive), p.Chats(Unarchive); n != 2 || chats != 1 {
		t.Errorf("plan unarchives %d cards of %d chats, want 2 cards of 1 chat", n, chats)
	}
	_, err = f.Apply(p)
	must(t, err)
	for _, account := range []string{pers, third} {
		if got := f.read(filepath.Join(f.listDir(account), "local_c9.json")); !strings.HasSuffix(got, `"isArchived":false}`) {
			t.Errorf("%s holds %s, want it unarchived", account, got)
		}
	}
}

// Newer content from another list is written everywhere, A included, with A's archived state.
func TestSyncKeepsAsArchivedStateOverNewerContent(t *testing.T) {
	f, now := chatFixture(t)
	f.card(work, "c9", s3, "Trip", now-9000, `,"isArchived":true`)
	f.card(pers, "c9", s3, "Trip, revised", now-1000, `,"isArchived":false,"isStarred":true`)
	f.card(third, "c9", s3, "Trip, draft", now-20000, "")
	p, err := f.PlanSync(f.list("work"))
	must(t, err)
	// A's own copy and third's get newer content, third's archived on the way; personal's
	// content is the newest already, so it only gets archived.
	also := map[string]Action{}
	for _, s := range p.Steps {
		if s.Action == Update {
			also[s.List.Dir] = s.Also
		}
	}
	want := map[string]Action{f.listDir(work): "", f.listDir(third): Archive}
	if p.Count(Archive) != 1 || len(also) != 2 || also[f.listDir(work)] != "" || also[f.listDir(third)] != Archive {
		t.Errorf("plan archives %d, updates %v, want 1 archive and updates %v", p.Count(Archive), also, want)
	}
	_, err = f.Apply(p)
	must(t, err)
	card := `{"sessionId":"local_c9","cliSessionId":"` + s3 + `","cwd":` + str(proj) + `,"title":"Trip, revised","lastActivityAt":` + fmt.Sprint(now-1000) + `,"isArchived":true,"isStarred":true}`
	for _, account := range []string{work, pers, third} {
		if got := f.read(filepath.Join(f.listDir(account), "local_c9.json")); got != card {
			t.Errorf("%s holds\n%s\nwant\n%s", account, got, card)
		}
	}
}

// One undo puts every archived state and every byte back.
func TestUndoRestoresArchivedState(t *testing.T) {
	f, now := chatFixture(t)
	f.card(work, "c9", s3, "Trip", now-9000, `,"isArchived":true`)
	f.card(pers, "c9", s3, "Trip, revised", now-1000, `,"isArchived":false`)
	f.card(third, "c8", s2, "Notes", now-5000, `,"isArchived":true`)
	f.card(work, "c8", s2, "Notes", now-5000, `,"isArchived":false`)
	sessions := filepath.Join(f.data, "claude-code-sessions")
	before := f.hash(sessions)
	p, err := f.PlanSync(f.list("work"))
	must(t, err)
	_, err = f.Apply(p)
	must(t, err)
	_, err = f.Undo()
	must(t, err)
	if f.hash(sessions) != before {
		t.Fatal("undo should restore every list byte for byte")
	}
}

// A stale archive step never writes over a card that became newer after the plan.
func TestStaleArchiveStepKeepsNewerCard(t *testing.T) {
	f, now := chatFixture(t)
	f.card(work, "c9", s3, "Trip", now-1000, `,"isArchived":true`)
	f.card(pers, "c9", s3, "Trip", now-1000, `,"isArchived":false`)
	p, err := f.PlanSync(f.list("work"))
	must(t, err)
	newer := f.card(pers, "c9", s3, "Trip, newer", now, `,"isArchived":false`)
	_, err = f.Apply(p)
	must(t, err)
	if got := f.read(newer); !strings.Contains(got, "Trip, newer") {
		t.Errorf("a stale archive step wrote over a newer card: %s", got)
	}
}
