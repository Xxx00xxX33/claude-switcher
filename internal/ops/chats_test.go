package ops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	s1 = "10000000-0000-4000-8000-000000000001"
	s2 = "10000000-0000-4000-8000-000000000002"
	s3 = "10000000-0000-4000-8000-000000000003"
)

// chatFixture has profiles work (signed in, in use) and personal, and the v2 chats: c1 and c2 in
// work, c3 in personal.
func chatFixture(t *testing.T) (*fixture, int64) {
	f := newFixture(t)
	f.signIn(pers, "personal")
	must(t, f.Save("personal", false))
	must(t, f.Add("work"))
	f.signIn(work, "work")
	now := time.Now().UnixMilli()
	f.card(work, "c1", s1, "Design review", now-3600000, "")
	f.card(work, "c2", s2, "Release notes", now-7200000, "")
	f.card(pers, "c3", s3, "Holiday plan", now-60000, "")
	for _, s := range []string{s1, s2, s3} {
		f.transcript(s, "", "hi", time.UnixMilli(now))
	}
	must(t, f.Switch("personal"))
	must(t, f.Switch("work"))
	return f, now
}

func TestListsAndChats(t *testing.T) {
	f, _ := chatFixture(t)
	st, err := f.Status()
	must(t, err)
	if len(st.Lists) != 2 || st.Lists[0].Label != "work" || st.Lists[0].Chats != 2 || !st.Lists[0].SignedIn || st.Lists[1].Chats != 1 {
		t.Fatalf("lists = %+v", st.Lists)
	}
	for _, p := range st.Profiles {
		if want := map[string]int{"work": 2, "personal": 1}[p.Name]; p.Chats != want || p.SignedIn != (p.Name == "work") || p.Current != (p.Name == "work") {
			t.Errorf("profile %+v", p)
		}
	}

	chats, err := f.Chats(f.list("work"))
	must(t, err)
	if len(chats) != 2 || chats[0].Title != "Design review" || chats[1].Title != "Release notes" {
		t.Fatalf("chats should be newest first: %+v", chats)
	}
	if c := chats[0]; c.ID != "local_c1" || c.Project != "proj" || !c.HasHistory || c.Transcript == "" || c.Session != s1 {
		t.Errorf("chat = %+v", c)
	}

	for ref, want := range map[string]string{"work": work, "signed-in": work, "2222": pers, "22222222-2222-4222-8222-222222222222/aaaa": pers, "WORK": work} {
		if l, err := f.FindList(ref); err != nil || l.Account != want {
			t.Errorf("FindList(%q) = %s, %v", ref, l.Account, err)
		}
	}
	_, err = f.FindList("9999")
	wantCode(t, err, NoList)
	f.put(filepath.Join(f.data, "claude-code-sessions", work, "bbbbbbbb-org2", "local_x.json"), `{}`)
	_, err = f.FindList("work")
	wantCode(t, err, AmbiguousList)
	if l := f.list("work/bbbb"); l.Chats != 1 {
		t.Error("account/org picks one of several lists")
	}
}

func TestCopyMoveUndo(t *testing.T) {
	f, _ := chatFixture(t)
	workDir, persDir := f.listDir(work), f.listDir(pers)
	workBefore := f.hash(workDir)
	persBefore := f.hash(persDir)
	from, to := f.list("work"), f.list("personal")

	p, err := f.PlanTransfer(from, to, []string{"c1"}, false)
	must(t, err)
	if len(p.Steps) != 1 || p.Count(Create) != 1 {
		t.Fatalf("plan = %+v", p)
	}
	res, err := f.Apply(p)
	must(t, err)
	if res.Created != 1 || !f.exists(filepath.Join(persDir, "local_c1.json")) || f.hash(workDir) != workBefore {
		t.Fatal("copy should put the chat in the target and leave the source alone")
	}
	if f.read(filepath.Join(persDir, "local_c1.json")) != f.read(filepath.Join(workDir, "local_c1.json")) {
		t.Error("the copy should be byte for byte")
	}
	_, err = f.Undo()
	must(t, err)
	if f.hash(persDir) != persBefore {
		t.Fatal("undo should remove the copied chat")
	}

	p, err = f.PlanTransfer(from, to, []string{"local_c2"}, true)
	must(t, err)
	res, err = f.Apply(p)
	must(t, err)
	if res.Created != 1 || res.Removed != 1 || !f.exists(filepath.Join(persDir, "local_c2.json")) || f.exists(filepath.Join(workDir, "local_c2.json")) {
		t.Fatal("move should put the chat in the target and remove it from the source")
	}
	_, err = f.Undo()
	must(t, err)
	if f.hash(workDir) != workBefore || f.hash(persDir) != persBefore {
		t.Fatal("undo of a move should restore both lists byte for byte")
	}

	_, err = f.PlanTransfer(from, to, []string{"nothing-like-this"}, true)
	wantCode(t, err, ChatNotFound)
	_, err = f.PlanTransfer(from, to, []string{"c"}, true)
	wantCode(t, err, AmbiguousChat)
	_, err = f.PlanTransfer(from, from, []string{"c1"}, false)
	wantCode(t, err, SameList)
	if f.hash(workDir) != workBefore {
		t.Error("a refused plan changed something")
	}
	if p, _ := f.PlanTransfer(from, to, []string{s1, "c1"}, false); len(p.Steps) != 1 {
		t.Error("a transcript id prefix picks the chat, once")
	}
}

// The v2 newest-wins fixture: sync and copy never replace a newer card with an older one.
func TestNewestWins(t *testing.T) {
	f, now := chatFixture(t)
	workDir, persDir := f.listDir(work), f.listDir(pers)
	workBefore := f.hash(workDir)
	work, personal := f.list("work"), f.list("personal")

	f.card(pers, "c1", s1, "Design review (older copy)", now-9999999, "")
	persBefore := f.hash(persDir)
	p, err := f.PlanSync(work)
	must(t, err)
	if len(p.Steps) != 3 || p.Count(Pull) != 1 || p.Count(Update) != 1 || p.Count(Create) != 1 {
		t.Fatalf("sync should pull c3 into work, then update c1 and create c2 in personal: %+v", p.Steps)
	}
	_, err = f.Apply(p)
	must(t, err)
	if !f.exists(filepath.Join(workDir, "local_c3.json")) || !strings.Contains(f.read(filepath.Join(workDir, "local_c1.json")), `"Design review"`) {
		t.Fatal("sync should bring what work lacks and keep its newer copy")
	}
	if strings.Contains(f.read(filepath.Join(persDir, "local_c1.json")), "older copy") || !f.exists(filepath.Join(persDir, "local_c2.json")) {
		t.Fatal("sync should push work's newer cards to personal")
	}
	_, err = f.Undo()
	must(t, err)
	if f.hash(workDir) != workBefore || f.hash(persDir) != persBefore {
		t.Fatal("undo of a sync should restore every list")
	}

	p, err = f.PlanTransfer(personal, work, []string{"c1"}, false)
	must(t, err)
	res, err := f.Apply(p)
	must(t, err)
	if p.Steps[0].Action != SkipNewer || res.Skipped != 1 || f.hash(workDir) != workBefore {
		t.Fatal("copy should leave a newer copy in the target alone")
	}
	if js, _ := f.History(); len(js) != 1 || !js[0].Undone {
		t.Error("a change that wrote nothing should leave no journal")
	}

	f.card(pers, "c1", s1, "Design review (continued)", now, "")
	p, err = f.PlanTransfer(personal, work, []string{"c1"}, false)
	must(t, err)
	res, err = f.Apply(p)
	must(t, err)
	if p.Steps[0].Action != Update || res.Updated != 1 || !strings.Contains(f.read(filepath.Join(workDir, "local_c1.json")), "continued") {
		t.Fatal("copy should bring an older copy in the target up to date")
	}
	_, err = f.Undo()
	must(t, err)
	if f.hash(workDir) != workBefore {
		t.Fatal("undo should restore the older copy byte for byte")
	}

	p, err = f.PlanSync(work)
	must(t, err)
	if p.Count(Update) != 1 || p.Count(Pull) != 1 || p.Count(Create) != 1 {
		t.Fatalf("sync should update older copies in A too: %+v", p.Steps)
	}
	_, err = f.Apply(p)
	must(t, err)
	if !strings.Contains(f.read(filepath.Join(workDir, "local_c1.json")), "continued") || !f.exists(filepath.Join(workDir, "local_c3.json")) {
		t.Fatal("sync did not update")
	}
	_, err = f.Undo()
	must(t, err)
	if f.hash(workDir) != workBefore {
		t.Fatal("undo of that sync should restore the target")
	}
}

// After a sync every list holds the newest copy of every chat found in any list.
func TestSyncTakesNewestAcrossLists(t *testing.T) {
	f, now := chatFixture(t)
	f.card(third, "c9", s3, "old", now-5000, "")
	f.card(pers, "c9", s3, "newest", now-1000, "")
	f.card(work, "c9", s3, "oldest", now-9000, "")
	p, err := f.PlanSync(f.list("work"))
	must(t, err)
	_, err = f.Apply(p)
	must(t, err)
	for _, account := range []string{work, pers, third} {
		dir := f.listDir(account)
		for _, id := range []string{"c1", "c2", "c3"} {
			if !f.exists(filepath.Join(dir, "local_"+id+".json")) {
				t.Errorf("%s lacks %s after sync", account, id)
			}
		}
		if got := f.read(filepath.Join(dir, "local_c9.json")); !strings.Contains(got, `"newest"`) {
			t.Errorf("%s holds %s, want the newest copy", account, got)
		}
	}
}

// Sync covers every org list of every account, and one undo restores them all byte for byte.
func TestSyncCoversEveryOrg(t *testing.T) {
	f, now := chatFixture(t)
	org2 := filepath.Join(f.data, "claude-code-sessions", pers, "bbbbbbbb-org2")
	f.put(filepath.Join(org2, "local_c7.json"), fmt.Sprintf(`{"sessionId":"local_c7","cwd":%s,"title":"Second org","lastActivityAt":%d}`, str(proj), now-1000))
	sessions := filepath.Join(f.data, "claude-code-sessions")
	before := f.hash(sessions)
	p, err := f.PlanSync(f.list("work"))
	must(t, err)
	_, err = f.Apply(p)
	must(t, err)
	for _, dir := range []string{f.listDir(work), f.listDir(pers), org2} {
		for _, id := range []string{"c1", "c2", "c3", "c7"} {
			if !f.exists(filepath.Join(dir, "local_"+id+".json")) {
				t.Errorf("%s lacks %s after sync", dir, id)
			}
		}
	}
	if js, _ := f.History(); len(js) != 1 || js[0].Action != Sync {
		t.Fatalf("one sync should be one journal entry: %+v", js)
	}
	_, err = f.Undo()
	must(t, err)
	if f.hash(sessions) != before {
		t.Fatal("one undo should restore every list byte for byte")
	}
}

// The caller can leave steps out (the preview's exclude), and a stale plan never writes over a
// card that became newer after it was worked out.
func TestSyncAppliesOnlyFreshKeptSteps(t *testing.T) {
	f, now := chatFixture(t)
	workDir, persDir := f.listDir(work), f.listDir(pers)
	p, err := f.PlanSync(f.list("work"))
	must(t, err)
	var kept []Step
	for _, s := range p.Steps {
		if s.Chat.ID != "local_c2" {
			kept = append(kept, s)
		}
	}
	p.Steps = kept
	f.card(work, "c3", s3, "Holiday plan (newer in work now)", now, "")
	_, err = f.Apply(p)
	must(t, err)
	if f.exists(filepath.Join(persDir, "local_c2.json")) {
		t.Error("an excluded step was written")
	}
	if !strings.Contains(f.read(filepath.Join(workDir, "local_c3.json")), "newer in work now") {
		t.Error("a stale step wrote over a newer card")
	}
	if !f.exists(filepath.Join(persDir, "local_c1.json")) {
		t.Error("a kept step was not written")
	}
}

// Changes recorded as merge by earlier versions still list and undo.
func TestOldMergeEntryUndoes(t *testing.T) {
	f, _ := chatFixture(t)
	workDir := f.listDir(work)
	before := f.hash(workDir)
	j, err := f.Vault.NewJournal("merge", time.Now())
	must(t, err)
	card := filepath.Join(workDir, "local_c3.json")
	must(t, j.Created(card))
	f.put(card, f.read(filepath.Join(f.listDir(pers), "local_c3.json")))
	must(t, j.Save("Copied 1 chat into work"))
	if js, _ := f.History(); len(js) != 1 || js[0].Action != "merge" {
		t.Fatalf("history = %+v", js)
	}
	_, err = f.Undo()
	must(t, err)
	if f.hash(workDir) != before {
		t.Fatal("undo of an old merge should restore the list")
	}
}

func TestApplyKeepsEveryCardField(t *testing.T) {
	f, now := chatFixture(t)
	odd := `,"isStarred":true,"rewindEdges":[{"id":"e1","at":1.5e12}],"prs":[{"url":"https://x/1?a=<b>&c"}],"futureKey":{"nested":[null,"‌"]}`
	src := f.card(work, "odd", "", "عنوان فارسی", now, odd)
	p, err := f.PlanTransfer(f.list("work"), f.list("personal"), []string{"odd"}, false)
	must(t, err)
	_, err = f.Apply(p)
	must(t, err)
	if got := f.read(filepath.Join(f.listDir(pers), "local_odd.json")); got != f.read(src) {
		t.Fatalf("fields changed on the way:\n%s\n%s", got, f.read(src))
	}
}

// Every write refuses while Desktop runs, and changes nothing.
func TestWritesWaitForDesktopToClose(t *testing.T) {
	f, now := chatFixture(t)
	f.transcript("20000000-0000-4000-8000-000000000001", "Lost", "q", time.UnixMilli(now))
	p, err := f.PlanTransfer(f.list("work"), f.list("personal"), []string{"c1"}, true)
	must(t, err)
	_, err = f.Apply(p)
	must(t, err)
	p, err = f.PlanTransfer(f.list("personal"), f.list("work"), []string{"c1"}, true)
	must(t, err)

	f.desk.running = true
	before := f.hash(f.root)
	writes := map[string]func() error{
		"save":   func() error { return f.Save("work", false) },
		"add":    func() error { return f.Add("third") },
		"switch": func() error { return f.Switch("personal") },
		"apply":  func() error { _, err := f.Apply(p); return err },
		"sync": func() error {
			p, err := f.PlanSync(f.list("work"))
			if err != nil {
				return err
			}
			_, err = f.Apply(p)
			return err
		},
		"rescue": func() error { _, err := f.Rescue(f.list("work"), []string{"2000"}); return err },
		"undo":   func() error { _, err := f.Undo(); return err },
	}
	for name, write := range writes {
		if err := write(); !Is(err, DesktopRunning) {
			t.Errorf("%s: got %v, want desktop-running", name, err)
		}
	}
	if f.hash(f.root) != before {
		t.Error("a refused write changed files")
	}
	if entries, _ := os.ReadDir(filepath.Join(f.Vault.Dir, "_journal")); len(entries) != 1 {
		t.Errorf("refused writes left %d journal folders", len(entries)-1)
	}
}

func TestUndoWithNothingToUndo(t *testing.T) {
	f := newFixture(t)
	_, err := f.Undo()
	wantCode(t, err, NothingToUndo)
}

// A sync's done summary counts the cards it wrote, not the plan's: a step the re-check skips is
// left out of it.
func TestSyncSummaryCountsWhatWasWritten(t *testing.T) {
	f, now := chatFixture(t)
	p, err := f.PlanSync(f.list("work")) // pull c3 into work, push c1 and c2 to personal
	must(t, err)
	f.card(work, "c3", s3, "Holiday plan (newer in work now)", now, "")
	res, err := f.Apply(p)
	must(t, err)
	if res.Created != 2 || res.Skipped != 1 {
		t.Fatalf("result %+v, want the two pushes created and the pull skipped", res)
	}
	if want := "Synced every chat list with work: 2 cards written"; res.Summary != want {
		t.Errorf("summary %q, want %q", res.Summary, want)
	}
}
