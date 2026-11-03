package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/nyxon-tech/claude-switcher/v3/internal/i18n"
	"github.com/nyxon-tech/claude-switcher/v3/internal/ops"
)

// openPreview is m on the account in use: work has one chat list, so the preview opens at once.
func openPreview(t *testing.T, a *app) *syncPreview {
	t.Helper()
	a.Update(press(a, "m")())
	p := a.screens[0].(*accountsScreen).preview
	if p == nil || a.dialog != nil {
		t.Fatalf("m should open the sync preview; dialog %+v", a.dialog)
	}
	return p
}

// cardsIn counts the cards in an account's chat list.
func cardsIn(t *testing.T, env *ops.Env, account string) int {
	t.Helper()
	cards, err := os.ReadDir(filepath.Join(env.Install.DataDir, "claude-code-sessions", account, orgID))
	if err != nil {
		t.Fatal(err)
	}
	return len(cards)
}

// m opens a preview grouped by action, one line per chat; a deselected line is left out of the
// sync, the rest is written, and Activity lists the sync.
func TestSyncPreviewExcludes(t *testing.T) {
	env, _ := fixture(t)
	a := newTestApp(t, env, 100, 30)
	openPreview(t, a)
	checkFrame(t, a)
	wantIn(t, a, i18n.T("ui.sync.group.pull", "name", "work", "n", 2), i18n.T("ui.sync.group.create", "name", "work", "n", 3),
		"Trip plan", "Budget sheet", "Release notes")
	chatKeys(a, "space") // the first line: Trip plan, pulled into work
	press(a, "enter")
	if a.runner == nil {
		t.Fatal("enter should start the sync")
	}
	a.Update(a.runner.try()())
	if a.runner != nil || !strings.Contains(a.toast.text, "4") {
		t.Fatalf("the sync should finish with a toast; runner %v, toast %q", a.runner, a.toast.text)
	}
	if n := cardsIn(t, env, workID); n != 4 {
		t.Errorf("work holds %d cards, want its 3 plus Budget sheet (Trip plan was left out)", n)
	}
	if n := cardsIn(t, env, personID); n != 5 {
		t.Errorf("personal holds %d cards, want 5", n)
	}
	if js, err := env.History(); err != nil || len(js) != 1 || js[0].Action != ops.Sync {
		t.Errorf("Activity should list the sync: %+v, %v", js, err)
	}
}

// threeLists is fixture with client's chat list too: Client brief, and an older, archived copy
// of work's Fix the login redirect.
func threeLists(t *testing.T) *ops.Env {
	env, _ := fixture(t)
	dir := filepath.Join(env.Install.DataDir, "claude-code-sessions", clientID, orgID)
	brief := clientID[:8] + "-0000-4000-8000-000000000000"
	put(t, filepath.Join(dir, "local_"+brief+".json"), fmt.Sprintf(`{"sessionId":"local_%s","cliSessionId":"%s","cwd":"/work/proj","title":"Client brief","lastActivityAt":%d}`,
		brief, brief, now.Add(-5*time.Hour).UnixMilli()))
	fix := workID[:8] + "-0000-4000-8000-000000000000"
	put(t, filepath.Join(dir, "local_"+fix+".json"), fmt.Sprintf(`{"sessionId":"local_%s","cliSessionId":"%s","cwd":"/work/proj","title":"Fix the login redirect","lastActivityAt":%d,"isArchived":true}`,
		fix, fix, now.Add(-9*time.Hour).UnixMilli()))
	return env
}

// The preview has a line per chat in each group, its headings count chats, and an update that
// also changes the archived state says so. Leaving a chat out leaves it out of every list.
func TestSyncPreviewExcludesWholeChat(t *testing.T) {
	env := threeLists(t)
	a := newTestApp(t, env, 120, 40)
	openPreview(t, a)
	wantIn(t, a, i18n.T("ui.sync.group.pull", "name", "work", "n", 3), i18n.T("ui.sync.group.create", "name", "work", "n", 6),
		i18n.T("ui.sync.group.update", "name", "work", "n", 1), i18n.T("ui.sync.also.unarchive"), "client, personal",
		i18n.T("ui.sync.kept", "n", 6, "total", 6))
	chatKeys(a, "space") // the first line: Trip plan, pulled into work, then pushed to client
	wantIn(t, a, i18n.T("ui.sync.kept", "n", 5, "total", 6))
	press(a, "enter")
	if a.runner == nil {
		t.Fatal("enter should start the sync")
	}
	a.Update(a.runner.try()())
	for _, want := range []struct {
		account string
		n       int
	}{{workID, 5}, {personID, 6}, {clientID, 5}} {
		if n := cardsIn(t, env, want.account); n != want.n {
			t.Errorf("%s holds %d cards, want %d (Trip plan left out everywhere)", want.account, n, want.n)
		}
	}
	trip := filepath.Join(env.Install.DataDir, "claude-code-sessions", clientID, orgID, "local_"+personID[:8]+"-0000-4000-8000-000000000000.json")
	if _, err := os.Stat(trip); err == nil {
		t.Error("Trip plan was pushed to client although it was left out")
	}
}

// conflictFixture is fixture with Trip plan (personal) deleted in work before it was last used.
func conflictFixture(t *testing.T) *ops.Env {
	env, _ := fixture(t)
	id := personID[:8] + "-0000-4000-8000-000000000000"
	put(t, filepath.Join(env.Install.DataDir, "claude-code-sessions", workID, orgID, "deleted_"+id), fmt.Sprint(now.Add(-2*time.Hour).UnixMilli()))
	return env
}

// A conflict must be settled before the sync runs: enter with one open jumps to it, space does
// not leave it out, d deletes it anyway.
func TestSyncConflictDeleteAnyway(t *testing.T) {
	env := conflictFixture(t)
	a := newTestApp(t, env, 100, 30)
	openPreview(t, a)
	wantIn(t, a, i18n.T("ui.sync.group.conflict", "name", "work", "n", 1), i18n.T("ui.sync.conflict.open"))
	press(a, "enter")
	if a.runner != nil || a.toast.text != i18n.T("ui.sync.conflict.unsettled", "n", 1) {
		t.Fatalf("enter with an open conflict should say so, not run; toast %q", a.toast.text)
	}
	chatKeys(a, "space") // a conflict is never just left out
	a.toast.text = ""
	press(a, "enter")
	if a.runner != nil || a.toast.text != i18n.T("ui.sync.conflict.unsettled", "n", 1) {
		t.Fatalf("space should not settle a conflict; toast %q", a.toast.text)
	}
	press(a, "d")
	wantIn(t, a, i18n.T("ui.sync.conflict.delete"))
	press(a, "enter")
	if a.runner == nil {
		t.Fatal("enter should run once every conflict is settled")
	}
	a.Update(a.runner.try()())
	if n := cardsIn(t, env, personID); n != 4 {
		t.Errorf("personal holds %d cards, want Budget sheet and work's 3 (Trip plan deleted)", n)
	}
	if n := cardsIn(t, env, workID); n != 5 {
		t.Errorf("work holds %d cards, want its 3, Budget sheet and the marker", n)
	}
}

// p keeps a conflicted chat and pulls it into the account synced from.
func TestSyncConflictKeep(t *testing.T) {
	env := conflictFixture(t)
	a := newTestApp(t, env, 100, 30)
	openPreview(t, a)
	press(a, "enter", "p")
	wantIn(t, a, i18n.T("ui.sync.conflict.keep", "name", "work"))
	press(a, "enter")
	a.Update(a.runner.try()())
	if n := cardsIn(t, env, workID); n != 5 {
		t.Errorf("work holds %d cards, want its 3 and personal's 2, its marker for Trip plan gone", n)
	}
	if n := cardsIn(t, env, personID); n != 5 {
		t.Errorf("personal holds %d cards, want 5", n)
	}
}

// An account a chat is written into loses its own deletion marker for it, shown in its own group;
// the marker goes with the sync.
func TestSyncPreviewShowsUnmark(t *testing.T) {
	env, _ := fixture(t)
	notes := workID[:8] + "-0002-4000-8000-000000000000" // Release notes, created in personal
	marker := filepath.Join(env.Install.DataDir, "claude-code-sessions", personID, orgID, "deleted_"+notes)
	put(t, marker, "1")
	a := newTestApp(t, env, 100, 30)
	openPreview(t, a)
	wantIn(t, a, i18n.T("ui.sync.group.unmark", "name", "work", "n", 1))
	press(a, "enter")
	a.Update(a.runner.try()())
	if _, err := os.Stat(marker); err == nil {
		t.Error("personal still holds its marker for Release notes, written there")
	}
}

// A plan with nothing to do says so; esc leaves a preview without writing anything.
func TestSyncNothingAndCancel(t *testing.T) {
	env, _ := fixture(t)
	a := newTestApp(t, env, 100, 30)
	openPreview(t, a)
	press(a, "esc")
	if a.screens[0].(*accountsScreen).preview != nil || cardsIn(t, env, workID) != 3 {
		t.Fatal("esc should close the preview and write nothing")
	}
	work, err := env.FindList("work")
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.PlanSync(work)
	if err == nil {
		_, err = env.Apply(p)
	}
	if err != nil {
		t.Fatal(err)
	}
	a.Update(press(a, "m")())
	if a.dialog == nil || a.dialog.body != i18n.T("ui.sync.nothing") {
		t.Fatalf("an empty plan should say there is nothing to sync; dialog %+v", a.dialog)
	}
}

// The preview at the sizes the goldens keep: groups, lines and the conflict, scrolled to fit.
func TestSyncPreviewFrames(t *testing.T) {
	for _, size := range [][2]int{{60, 18}, {100, 30}} {
		t.Run(fmt.Sprintf("en_%dx%d", size[0], size[1]), func(t *testing.T) {
			a := newTestApp(t, conflictFixture(t), size[0], size[1])
			openPreview(t, a)
			chatKeys(a, "end")
			checkFrame(t, a)
			golden.RequireEqual(t, frameText(a))
		})
	}
}

// A plan that arrives after the user started something else is dropped, never opened over it.
func TestLateSyncPlanIsDropped(t *testing.T) {
	env, _ := fixture(t)
	a := newTestApp(t, env, 100, 30)
	plan := press(a, "m")
	press(a, "left", "enter") // switch to personal meanwhile
	a.Update(plan())
	if a.dialog != nil {
		t.Errorf("a late plan opened %q over the switch", a.dialog.title)
	}
}

// Rename keeps the focus on the renamed card although it moves; remove asks first, with Cancel
// focused.
func TestRenameAndRemove(t *testing.T) {
	env, _ := fixture(t)
	a := newTestApp(t, env, 100, 30)
	s := a.screens[0].(*accountsScreen)
	s.focus = 0 // client
	press(a, "r")
	a.dialog.input.SetValue("zeta")
	a.Update(press(a, "enter")())
	a.Update(a.Refresh()())
	if _, ok := env.Vault.Find("zeta"); !ok || a.toast.text != i18n.T("ui.rename.done", "old", "client", "name", "zeta") {
		t.Fatalf("client should be renamed to zeta; toast %q", a.toast.text)
	}
	if ps := s.profiles(a.Ctx); ps[s.focus].Name != "zeta" {
		t.Errorf("focus is on %q, want the renamed zeta", ps[s.focus].Name)
	}

	s.focus = 0 // personal
	if press(a, "d", "enter") != nil || a.dialog != nil {
		t.Fatal("enter on the remove question should take the focused Cancel")
	}
	a.Update(press(a, "d", "y")())
	if _, ok := env.Vault.Find("personal"); ok {
		t.Error("y should remove personal")
	}
}

// A box taller than a short window drops its blank lines so its buttons stay on screen.
func TestTallDialogFits(t *testing.T) {
	env, _ := fixture(t)
	a := newTestApp(t, env, 60, 18)
	press(a, "n", "!", "x", "enter")
	if a.dialog == nil || a.dialog.problem == "" {
		t.Fatal("a bad name should keep the dialog open with the problem")
	}
	checkFrame(t, a)
	box := a.dialog.view(a.Ctx, a.Width, a.Height-2)
	last := ansi.Strip(box[strings.LastIndex(box, "\n")+1:])
	if h := lipgloss.Height(box); h > a.Height-2 || !strings.Contains(last, "╰") {
		t.Errorf("the box is %d rows for %d, last row %q:\n%s", h, a.Height-2, last, ansi.Strip(box))
	}
	if text := strings.Join(screen(a), "\n"); !strings.Contains(text, i18n.T("action.add")) {
		t.Errorf("the Add button should stay on screen:\n%s", text)
	}
}

// A slow status read that returns after a newer one never replaces it.
func TestOlderStatusIsIgnored(t *testing.T) {
	env, _ := fixture(t)
	a := newTestApp(t, env, 100, 30)
	older := a.Refresh()()
	if err := env.Vault.SetCurrent("personal"); err != nil {
		t.Fatal(err)
	}
	a.Update(a.Refresh()())
	a.Update(older)
	if a.Status.Current != "personal" {
		t.Errorf("status shows %q after an older read, want personal", a.Status.Current)
	}
}
