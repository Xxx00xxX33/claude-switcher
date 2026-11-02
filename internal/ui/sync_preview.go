package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/nyxon-tech/claude-switcher/v3/internal/i18n"
	"github.com/nyxon-tech/claude-switcher/v3/internal/ops"
)

// syncGroups are the preview's groups in order, one per step action. A new action is one entry;
// a step whose action has none still shows, in a group of its own at the end.
var syncGroups = []struct {
	action ops.Action
	key    string
}{
	{ops.Pull, "ui.sync.group.pull"},
	{ops.Create, "ui.sync.group.create"},
	{ops.Update, "ui.sync.group.update"},
	{ops.Delete, "ui.sync.group.delete"},
	{ops.Archive, "ui.sync.group.archive"},
	{ops.Unarchive, "ui.sync.group.unarchive"},
	{ops.Conflict, "ui.sync.group.conflict"},
}

// choice is what the user decided for a conflict.
type choice int

const (
	undecided choice = iota
	deleteAnyway
	keepAndPull
)

// syncPreview is the sync plan before it is written, filling the accounts body: a line per step
// under a heading per action. Each line can be left out; each conflict needs a choice first.
type syncPreview struct {
	name   string
	plan   ops.Plan
	lines  []previewLine
	cursor int            // index into lines, always on a step
	top    int            // first list line shown
	out    map[int]bool   // steps left out, by index into plan.Steps
	chosen map[int]choice // conflicts, by index into plan.Steps
}

// previewLine is a group heading (step < 0) or one step.
type previewLine struct {
	heading string
	step    int
}

func newSyncPreview(name string, plan ops.Plan) *syncPreview {
	p := &syncPreview{name: name, plan: plan, out: map[int]bool{}, chosen: map[int]choice{}}
	known := map[ops.Action]bool{}
	group := func(a ops.Action, heading func(n int) string) {
		var idx []int
		for i, s := range plan.Steps {
			if s.Action == a {
				idx = append(idx, i)
			}
		}
		if len(idx) == 0 {
			return
		}
		p.lines = append(p.lines, previewLine{heading: heading(len(idx)), step: -1})
		for _, i := range idx {
			p.lines = append(p.lines, previewLine{step: i})
		}
	}
	for _, g := range syncGroups {
		known[g.action] = true
		group(g.action, func(n int) string { return i18n.T(g.key, "name", name, "n", n) })
	}
	for _, s := range plan.Steps {
		if a := s.Action; !known[a] {
			known[a] = true
			group(a, func(n int) string { return string(a) + " (" + i18n.N(int64(n)) + ")" })
		}
	}
	p.cursor = p.next(0, 1)
	return p
}

// next is the first step line from i on, going d (1 or -1); it stays put when there is none.
func (p *syncPreview) next(i, d int) int {
	for ; i >= 0 && i < len(p.lines); i += d {
		if p.lines[i].step >= 0 {
			return i
		}
	}
	return p.cursor
}

func (p *syncPreview) step() (int, ops.Step) {
	i := p.lines[p.cursor].step
	return i, p.plan.Steps[i]
}

// open is the first line of a conflict still waiting for a choice, -1 when none is.
func (p *syncPreview) open() (int, int) {
	first, n := -1, 0
	for li, l := range p.lines {
		if l.step >= 0 && p.plan.Steps[l.step].Action == ops.Conflict && !p.out[l.step] && p.chosen[l.step] == undecided {
			if first < 0 {
				first = li
			}
			n++
		}
	}
	return first, n
}

func (p *syncPreview) keys() []key.Binding {
	keys := []key.Binding{
		bind("↑↓", "ui.key.move", "up", "down", "k", "j", "pgup", "pgdown", "home", "end"),
		bind("space", "ui.key.include"),
	}
	if p.plan.Count(ops.Conflict) > 0 {
		keys = append(keys, bind("d", "ui.key.delete_anyway"), bind("p", "ui.key.keep"))
	}
	return append(keys, bind("enter", "ui.key.sync_now"), bind("esc", "ui.key.cancel"))
}

// key handles a key; done reports that the preview closes.
func (p *syncPreview) key(c *Ctx, k string, page int) (cmd tea.Cmd, done bool) {
	switch k {
	case "up", "k":
		p.cursor = p.next(p.cursor-1, -1)
	case "down", "j":
		p.cursor = p.next(p.cursor+1, 1)
	case "pgup":
		p.cursor = p.next(max(0, p.cursor-page), 1)
	case "pgdown":
		p.cursor = p.next(min(len(p.lines)-1, p.cursor+page), -1)
	case "home":
		p.cursor = p.next(0, 1)
	case "end":
		p.cursor = p.next(len(p.lines)-1, -1)
	case "space":
		i, _ := p.step()
		p.out[i] = !p.out[i]
	case "d", "p":
		if i, s := p.step(); s.Action == ops.Conflict {
			p.out[i], p.chosen[i] = false, map[string]choice{"d": deleteAnyway, "p": keepAndPull}[k]
		}
	case "esc":
		return nil, true
	case "enter":
		return p.confirm(c)
	}
	return nil, false
}

// confirm runs the sync with the lines kept and the conflicts settled.
func (p *syncPreview) confirm(c *Ctx) (tea.Cmd, bool) {
	if li, n := p.open(); n > 0 {
		p.cursor = li
		return c.Toast(toastWarn, i18n.T("ui.sync.conflict.unsettled", "n", n)), false
	}
	plan, keep := p.plan, map[string]bool{}
	plan.Steps = nil
	for i, s := range p.plan.Steps {
		if p.out[i] {
			continue
		}
		if s.Action == ops.Conflict {
			keep[s.Chat.ID] = p.chosen[i] == keepAndPull
		}
		plan.Steps = append(plan.Steps, s)
	}
	if len(plan.Steps) == 0 {
		return c.Toast(toastInfo, i18n.T("ui.sync.none_kept")), false
	}
	env, name := c.Env, p.name
	return c.Change(change{
		title: i18n.T("ui.sync.running", "name", name),
		steps: []string{i18n.T("ui.step.synced", "name", name)},
		run: func() (string, error) {
			resolved, err := env.Resolve(plan, keep)
			if err != nil {
				return "", err
			}
			r, err := env.Apply(resolved)
			return r.Summary, err
		},
	}), true
}

// view draws the title, how much is kept, then the lines that fit, scrolled to the cursor.
func (p *syncPreview) view(c *Ctx, width, height int) string {
	l, st := c.L(), c.St
	kept := 0
	for i := range p.plan.Steps {
		if !p.out[i] {
			kept++
		}
	}
	summary := st.Muted.Render(l.Fit(i18n.T("ui.sync.kept", "n", kept, "total", len(p.plan.Steps)), width))
	if _, n := p.open(); n > 0 {
		summary = l.Row(summary, st.Warn.Render(l.Fit(i18n.T("ui.sync.conflict.unsettled", "n", n), width/2)), width)
	}
	lines := []string{l.Pad(st.Title.Render(l.Fit(i18n.T("ui.sync.title", "name", p.name), width)), width), summary, ""}
	rows := max(1, height-len(lines))
	first := p.cursor
	if first > 0 && p.lines[first-1].step < 0 {
		first-- // keep the heading of the cursor's group in sight
	}
	if first < p.top {
		p.top = first
	} else if p.cursor >= p.top+rows {
		p.top = p.cursor - rows + 1
	}
	p.top = max(0, min(p.top, len(p.lines)-rows))
	for li := p.top; li < min(len(p.lines), p.top+rows); li++ {
		lines = append(lines, p.line(c, li, width))
	}
	return strings.Join(lines, "\n")
}

// line is a heading, or a step: the cursor bar, its mark, the title, then the project, the
// account it affects (for a pull, the one it comes from) and, for a conflict, the choice.
func (p *syncPreview) line(c *Ctx, li, width int) string {
	l, st := c.L(), c.St
	pl := p.lines[li]
	if pl.step < 0 {
		return l.Pad(st.Accent.Render(l.Fit(pl.heading, width)), width)
	}
	s := p.plan.Steps[pl.step]
	text, muted := st.Text, st.Muted
	lead := "  "
	if li == p.cursor {
		lead, text = st.Bar.Render("▌")+" ", text.Bold(true)
	}
	mark := st.Accent.Render("☑")
	var trail []string
	if s.Action == ops.Conflict {
		switch p.chosen[pl.step] {
		case deleteAnyway:
			mark, trail = st.Fail.Render("✕"), []string{st.Fail.Render(l.Text(i18n.T("ui.sync.conflict.delete")))}
		case keepAndPull:
			mark, trail = st.OK.Render("↩"), []string{st.OK.Render(l.Text(i18n.T("ui.sync.conflict.keep", "name", p.name)))}
		default:
			mark, trail = st.Warn.Render("?"), []string{st.Warn.Render(l.Text(i18n.T("ui.sync.conflict.open")))}
		}
	}
	if p.out[pl.step] {
		mark, text, muted, trail = st.Dim.Render("☐"), st.Dim, st.Dim, nil
	}
	where := s.List.Label
	if s.Action == ops.Pull || s.Action == ops.Conflict {
		where = s.Chat.List.Label
	}
	cols := []string{muted.Render(l.Data(s.Chat.Project, max(8, width/6))), muted.Render(l.Data(where, max(8, width/6)))}
	if width < 70 {
		cols = cols[1:] // a narrow window drops the project first
	}
	right := strings.Join(append(cols, trail...), "  ")
	lead += mark + " "
	titleW := max(1, width-ansi.StringWidth(lead)-ansi.StringWidth(right)-2)
	title := text.Render(l.Data(s.Chat.Title, titleW))
	if s.Chat.Title == "" {
		title = st.Dim.Render(l.Fit(i18n.T("chats.untitled"), titleW))
	}
	return l.Row(lead+title, right, width)
}
