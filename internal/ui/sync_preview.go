package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/nyxon-tech/claude-switcher/v3/internal/i18n"
	"github.com/nyxon-tech/claude-switcher/v3/internal/ops"
)

// syncHeadings label the preview's groups, which follow ops.SyncOrder.
var syncHeadings = map[ops.Action]string{
	ops.Pull:      "ui.sync.group.pull",
	ops.Create:    "ui.sync.group.create",
	ops.Update:    "ui.sync.group.update",
	ops.Delete:    "ui.sync.group.delete",
	ops.Archive:   "ui.sync.group.archive",
	ops.Unarchive: "ui.sync.group.unarchive",
	ops.Conflict:  "ui.sync.group.conflict",
}

// syncPreview is the sync plan before it is written, filling the accounts body: a line per chat
// under a heading per action. A chat can be left out, which leaves it out of every list; each
// conflict needs a choice instead.
type syncPreview struct {
	name   string
	plan   ops.Plan
	groups []ops.ChatSteps
	lines  []previewLine
	cursor int                   // index into lines, always on a chat
	top    int                   // first list line shown
	out    map[string]bool       // chats left out, by id
	chosen map[string]ops.Choice // conflicts, by chat id
}

// previewLine is a group heading (group < 0) or one chat of a group.
type previewLine struct {
	heading string
	group   int // index into groups
}

func newSyncPreview(name string, plan ops.Plan) *syncPreview {
	p := &syncPreview{name: name, plan: plan, groups: plan.ByChat(), out: map[string]bool{}, chosen: map[string]ops.Choice{}}
	for i, g := range p.groups {
		if i == 0 || p.groups[i-1].Action != g.Action {
			p.lines = append(p.lines, previewLine{heading: i18n.T(syncHeadings[g.Action], "name", name, "n", plan.Chats(g.Action)), group: -1})
		}
		p.lines = append(p.lines, previewLine{group: i})
	}
	p.cursor = p.next(0, 1)
	return p
}

// next is the first chat line from i on, going d (1 or -1); it stays put when there is none.
func (p *syncPreview) next(i, d int) int {
	for ; i >= 0 && i < len(p.lines); i += d {
		if p.lines[i].group >= 0 {
			return i
		}
	}
	return p.cursor
}

// at is the chat the cursor is on.
func (p *syncPreview) at() ops.ChatSteps { return p.groups[p.lines[p.cursor].group] }

// unsettled is the first line of a conflict still waiting for a choice (-1 when none is) and how
// many are.
func (p *syncPreview) unsettled() (first, n int) {
	first = -1
	for li, l := range p.lines {
		if l.group >= 0 && p.groups[l.group].Action == ops.Conflict && p.chosen[p.groups[l.group].Chat.ID] == ops.Undecided {
			if first < 0 {
				first = li
			}
			n++
		}
	}
	return first, n
}

// kept is how many of the plan's chats are kept, and how many it has.
func (p *syncPreview) kept() (n, total int) {
	seen := map[string]bool{}
	for _, g := range p.groups {
		if !seen[g.Chat.ID] {
			seen[g.Chat.ID] = true
			if !p.out[g.Chat.ID] {
				n++
			}
		}
	}
	return n, len(seen)
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

// update handles a key, click or wheel turn; done reports that the preview closes.
func (p *syncPreview) update(c *Ctx, msg tea.Msg, page int) (cmd tea.Cmd, done bool) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return p.key(c, msg.String(), page)
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			return p.key(c, "up", page)
		case tea.MouseWheelDown:
			return p.key(c, "down", page)
		}
	}
	return nil, false
}

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
		if g := p.at(); g.Action != ops.Conflict { // a conflict needs d or p
			p.out[g.Chat.ID] = !p.out[g.Chat.ID]
		}
	case "d":
		if g := p.at(); g.Action == ops.Conflict {
			p.chosen[g.Chat.ID] = ops.DeleteAnyway
		}
	case "p":
		if g := p.at(); g.Action == ops.Conflict {
			p.chosen[g.Chat.ID] = ops.KeepAndPull
		}
	case "esc":
		return nil, true
	case "enter":
		return p.confirm(c)
	}
	return nil, false
}

// confirm runs the sync without the chats left out and with the conflicts settled.
func (p *syncPreview) confirm(c *Ctx) (tea.Cmd, bool) {
	if li, n := p.unsettled(); n > 0 {
		p.cursor = li
		return c.Toast(toastWarn, i18n.T("ui.sync.conflict.unsettled", "n", n)), false
	}
	var out []string
	for id, left := range p.out {
		if left {
			out = append(out, id)
		}
	}
	plan, choices := p.plan.Without(out...), p.chosen
	if len(plan.Steps) == 0 {
		return c.Toast(toastInfo, i18n.T("ui.sync.none_kept")), false
	}
	env, name := c.Env, p.name
	return c.Change(change{
		title: i18n.T("ui.sync.running", "name", name),
		steps: []string{i18n.T("ui.step.synced", "name", name)},
		run: func() (string, error) {
			resolved, err := env.Resolve(plan, choices)
			if err != nil {
				return "", err
			}
			r, err := env.Apply(resolved)
			return r.Summary, err
		},
	}), true
}

// view draws the title, how many chats are kept, then the lines that fit, scrolled to the cursor.
func (p *syncPreview) view(c *Ctx, width, height int) string {
	l, st := c.L(), c.St
	kept, total := p.kept()
	summary := st.Muted.Render(l.Fit(i18n.T("ui.sync.kept", "n", kept, "total", total), width))
	if _, n := p.unsettled(); n > 0 {
		summary = l.Row(summary, st.Warn.Render(l.Fit(i18n.T("ui.sync.conflict.unsettled", "n", n), width/2)), width)
	}
	lines := []string{l.Pad(st.Title.Render(l.Fit(i18n.T("ui.sync.title", "name", p.name), width)), width), summary, ""}
	rows := max(1, height-len(lines))
	first := p.cursor
	if first > 0 && p.lines[first-1].group < 0 {
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

// line is a heading, or a chat: the cursor bar, its mark, the title, then the project, the
// accounts the action changes (for a pull, the one it comes from) and, for a conflict, the
// choice or, for an update, an archived change it also makes.
func (p *syncPreview) line(c *Ctx, li, width int) string {
	l, st := c.L(), c.St
	pl := p.lines[li]
	if pl.group < 0 {
		return l.Pad(st.Accent.Render(l.Fit(pl.heading, width)), width)
	}
	g := p.groups[pl.group]
	text, muted := st.Text, st.Muted
	lead := "  "
	if li == p.cursor {
		lead, text = st.Bar.Render("▌")+" ", text.Bold(true)
	}
	mark := st.Accent.Render("☑")
	var trail []string
	switch {
	case g.Action == ops.Conflict:
		switch p.chosen[g.Chat.ID] {
		case ops.DeleteAnyway:
			mark, trail = st.Fail.Render("✕"), []string{st.Fail.Render(l.Text(i18n.T("ui.sync.conflict.delete")))}
		case ops.KeepAndPull:
			mark, trail = st.OK.Render("↩"), []string{st.OK.Render(l.Text(i18n.T("ui.sync.conflict.keep", "name", p.name)))}
		default:
			mark, trail = st.Warn.Render("?"), []string{st.Warn.Render(l.Text(i18n.T("ui.sync.conflict.open")))}
		}
	case g.Steps[0].Also != "":
		trail = []string{muted.Render(l.Text(i18n.T("ui.sync.also." + string(g.Steps[0].Also))))}
	}
	if p.out[g.Chat.ID] {
		mark, text, muted, trail = st.Dim.Render("☐"), st.Dim, st.Dim, nil
	}
	where := make([]string, len(g.Steps))
	for i, s := range g.Steps {
		where[i] = s.List.Label
		if g.Action == ops.Pull || g.Action == ops.Conflict {
			where[i] = s.Chat.List.Label
		}
	}
	cols := []string{muted.Render(l.Data(g.Chat.Project, max(8, width/6))), muted.Render(l.Data(strings.Join(where, ", "), max(8, width/4)))}
	if width < 70 {
		cols = cols[1:] // a narrow window drops the project first
	}
	right := strings.Join(append(cols, trail...), "  ")
	lead += mark + " "
	titleW := max(1, width-ansi.StringWidth(lead)-ansi.StringWidth(right)-2)
	title := text.Render(l.Data(g.Chat.Title, titleW))
	if g.Chat.Title == "" {
		title = st.Dim.Render(l.Fit(i18n.T("chats.untitled"), titleW))
	}
	return l.Row(lead+title, right, width)
}
