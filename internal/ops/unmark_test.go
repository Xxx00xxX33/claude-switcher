package ops

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	s4 = "10000000-0000-4000-8000-000000000004"
	s5 = "10000000-0000-4000-8000-000000000005"
)

// A list a chat is written into loses its own markers for the chat's current and prior
// transcripts; markers for chats not written, and other lists' markers, stay.
func TestWritingAChatRemovesOnlyItsMarkers(t *testing.T) {
	f, now := chatFixture(t)
	f.card(work, "c4", s4, "Moved on", now-1000, `,"priorCliSessionIds":["`+s5+`"]`)
	other := filepath.Join(f.listDir(pers), "deleted_"+s7)
	inA := filepath.Join(f.listDir(work), "deleted_"+s7)
	f.put(filepath.Join(f.listDir(pers), "deleted_"+s4), "1")
	f.put(filepath.Join(f.listDir(pers), "deleted_"+s5), "1")
	f.put(other, "1")
	f.put(inA, "1")
	p, err := f.PlanSync(f.list("work"))
	must(t, err)
	if p.Chats(Unmark) != 1 {
		t.Fatalf("only c4's markers in personal should go: %+v", p.Steps)
	}
	for _, s := range p.Steps {
		if s.Action == Unmark && (s.Chat.ID != "local_c4" || s.List.Dir != f.listDir(pers) || len(s.Markers) != 2) {
			t.Fatalf("unmark step = %+v", s)
		}
	}
	res, err := f.Apply(p)
	must(t, err)
	if res.Unmarked != 2 {
		t.Errorf("unmarked %d, want 2", res.Unmarked)
	}
	for _, s := range []string{s4, s5} {
		if f.exists(filepath.Join(f.listDir(pers), "deleted_"+s)) {
			t.Errorf("personal still holds its marker %s for c4, written there", s)
		}
	}
	if !f.exists(other) || !f.exists(inA) {
		t.Error("markers for chats not written should stay")
	}
}

// Apply decides an unmark again against the disk: a marker already gone is passed over, and
// none goes from a list that does not hold the chat's card.
func TestUnmarkRechecksTheDisk(t *testing.T) {
	f, now := chatFixture(t)
	f.card(work, "c4", s4, "Moved on", now-1000, `,"priorCliSessionIds":["`+s5+`"]`)
	f.put(filepath.Join(f.listDir(pers), "deleted_"+s4), "1")
	f.put(filepath.Join(f.listDir(pers), "deleted_"+s5), "1")
	p, err := f.PlanSync(f.list("work"))
	must(t, err)
	must(t, os.Remove(filepath.Join(f.listDir(pers), "deleted_"+s4)))
	only := p
	only.Steps = nil
	for _, s := range p.Steps {
		if s.Action == Unmark {
			only.Steps = append(only.Steps, s)
		}
	}
	res, err := f.Apply(only)
	must(t, err)
	if res.Unmarked != 0 || !f.exists(filepath.Join(f.listDir(pers), "deleted_"+s5)) {
		t.Fatal("no marker should go from a list that lacks the chat's card")
	}
	res, err = f.Apply(p)
	must(t, err)
	if res.Unmarked != 1 || f.exists(filepath.Join(f.listDir(pers), "deleted_"+s5)) {
		t.Fatalf("the marker still there should go, the one already gone be passed over: %+v", res)
	}
}
