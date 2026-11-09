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
	"github.com/nyxon-tech/claude-switcher/v3/internal/i18n"
	"github.com/nyxon-tech/claude-switcher/v3/internal/ops"
)

// openSync is m on the account in use: work has one chat list, so the dialog opens at once.
func openSync(t *testing.T, a *app) *dialog {
	t.Helper()
	a.Update(press(a, "m")())
	if a.dialog == nil {
		t.Fatal("m should ask before syncing")
	}
	return a.dialog
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

// m asks once, with how many chats each kind of change touches and no list of chats; Sync writes
// everything, and Activity lists the sync.
func TestSyncConfirms(t *testing.T) {
	env, _ := fixture(t)
	a := newTestApp(t, env, 100, 30)
	d := openSync(t, a)
	if d.title != i18n.T("ui.sync.title", "name", "work") || !strings.Contains(d.body, "2 into “work”") || !strings.Contains(d.body, "3 new elsewhere") {
		t.Fatalf("the dialog should count the changes: %q\n%s", d.title, d.body)
	}
	if strings.Contains(d.body, "Trip plan") {
		t.Errorf("the dialog should not list chats:\n%s", d.body)
	}
	checkFrame(t, a)
	press(a, "enter")
	if a.runner == nil {
		t.Fatal("Sync should start the sync")
	}
	a.Update(a.runner.try()())
	if a.runner != nil || !strings.Contains(a.toast.text, "5") {
		t.Fatalf("the sync should finish with a toast; runner %v, toast %q", a.runner, a.toast.text)
	}
	if n := cardsIn(t, env, workID); n != 5 {
		t.Errorf("work holds %d cards, want 5", n)
	}
	if n := cardsIn(t, env, personID); n != 5 {
		t.Errorf("personal holds %d cards, want 5", n)
	}
	if js, err := env.History(); err != nil || len(js) != 1 || js[0].Action != ops.Sync {
		t.Errorf("Activity should list the sync: %+v, %v", js, err)
	}
}

// conflictFixture is fixture with Trip plan (personal) deleted in work before it was last used.
func conflictFixture(t *testing.T) *ops.Env {
	env, _ := fixture(t)
	id := personID[:8] + "-0000-4000-8000-000000000000"
	put(t, filepath.Join(env.Install.DataDir, "claude-code-sessions", workID, orgID, "deleted_"+id), fmt.Sprint(now.Add(-2*time.Hour).UnixMilli()))
	return env
}

// A chat used after its deletion in the account synced from is left alone, and the dialog says so.
func TestSyncLeavesConflictsAlone(t *testing.T) {
	env := conflictFixture(t)
	a := newTestApp(t, env, 100, 30)
	d := openSync(t, a)
	if !strings.Contains(d.body, i18n.T("ui.sync.conflicts", "n", 1, "name", "work")) {
		t.Fatalf("the dialog should say the conflict is left alone:\n%s", d.body)
	}
	press(a, "enter")
	a.Update(a.runner.try()())
	if n := cardsIn(t, env, personID); n != 5 {
		t.Errorf("personal holds %d cards, want 5 (Trip plan kept)", n)
	}
	if n := cardsIn(t, env, workID); n != 5 {
		t.Errorf("work holds %d cards, want its 3, Budget sheet and the marker (Trip plan not pulled)", n)
	}
}

// Cancel writes nothing; a plan with nothing to do says so.
func TestSyncNothingAndCancel(t *testing.T) {
	env, _ := fixture(t)
	a := newTestApp(t, env, 100, 30)
	openSync(t, a)
	press(a, "esc")
	if a.dialog != nil || cardsIn(t, env, workID) != 3 {
		t.Fatal("esc should close the dialog and write nothing")
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
	if d := openSync(t, a); d.body != i18n.T("ui.sync.nothing") {
		t.Fatalf("an empty plan should say there is nothing to sync; dialog %+v", d)
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
