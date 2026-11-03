package ops

import (
	"cmp"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nyxon-tech/claude-switcher/v3/internal/claude"
	"github.com/nyxon-tech/claude-switcher/v3/internal/i18n"
	"github.com/nyxon-tech/claude-switcher/v3/internal/store"
)

// Kinds of change, also the action names in the journal (v2 used the same).
const (
	Copy   = "copy"
	Move   = "move"
	Sync   = "sync"
	Rescue = "rescue"
)

// Action is what one step does to the target list. Newest wins: a card is only written over
// when the copy being written was used more recently.
type Action string

const (
	Create    Action = "create"     // the target lacks the chat
	Update    Action = "update"     // the target has an older copy; it is backed up and replaced
	SkipNewer Action = "skip-newer" // the target's copy is as new or newer; it is left alone
	Pull      Action = "pull"       // sync: list A lacks a chat another list has; it is copied into A
	Archive   Action = "archive"    // sync: the list's copy gets archived because A's is; its content is the newest already
	Unarchive Action = "unarchive"  // sync: the list's copy gets unarchived because A's is not; its content is the newest already
	Delete    Action = "delete"     // sync: the chat was deleted in A; this card is backed up and removed
	Conflict  Action = "conflict"   // sync: deleted in A but used elsewhere since; left alone
	Unmark    Action = "unmark"     // sync: the list's own deletion markers for a chat written into it are backed up and removed
)

// SyncOrder is the order a sync preview groups its steps in, one group per action.
var SyncOrder = []Action{Pull, Create, Update, Delete, Archive, Unarchive, Unmark, Conflict}

// Step is one chat of a plan.
type Step struct {
	Action Action
	Chat   Chat   // the copy to write
	List   List   // the list the step writes
	Target string // its card file in List
	// Archived is the archived state a sync writes: A's when A holds the chat. Copy and move
	// leave the card as it is.
	Archived bool
	// Also is the archived change an Update makes on the way, Archive or Unarchive; empty when
	// the list's copy already has the archived state written.
	Also Action
	// Markers are the transcript ids of the list's own deletion markers an Unmark removes: the
	// chat's current and prior ones. Its Target is the chat's card in List.
	Markers []string
}

// Plan is a change worked out before anything is written, for the caller to show and confirm.
type Plan struct {
	Kind  string // Copy, Move or Sync
	From  List   // zero for Sync, which reads every list
	To    List   // for Sync, list A: the list synced from
	Steps []Step
}

// Count is how many steps take action a.
func (p Plan) Count(a Action) int {
	n := 0
	for _, s := range p.Steps {
		if s.Action == a {
			n++
		}
	}
	return n
}

// Cards is how many steps write or delete a card: every step but conflicts, left alone, and
// unmarks, which go with a card write.
func (p Plan) Cards() int { return len(p.Steps) - p.Count(Conflict) - p.Count(Unmark) }

// Chats is how many chats have a step taking action a, however many lists those steps write.
func (p Plan) Chats(a Action) int {
	seen := map[string]bool{}
	for _, s := range p.Steps {
		if s.Action == a {
			seen[s.Chat.ID] = true
		}
	}
	return len(seen)
}

// ChatSteps is one chat's steps that take one action, one per list they write: a preview line.
type ChatSteps struct {
	Action Action
	Chat   Chat // the first step's copy
	Steps  []Step
}

// ByChat groups a sync plan's steps by action in SyncOrder, then by chat, chats in plan order.
func (p Plan) ByChat() []ChatSteps {
	var out []ChatSteps
	for _, a := range SyncOrder {
		at := map[string]int{}
		for _, s := range p.Steps {
			if s.Action != a {
				continue
			}
			i, ok := at[s.Chat.ID]
			if !ok {
				i = len(out)
				at[s.Chat.ID] = i
				out = append(out, ChatSteps{Action: a, Chat: s.Chat})
			}
			out[i].Steps = append(out[i].Steps, s)
		}
	}
	return out
}

// Without is the plan with every step of the chats named left out (the preview's exclude): a
// chat left out is written nowhere.
func (p Plan) Without(ids ...string) Plan {
	p.Steps = slices.DeleteFunc(slices.Clone(p.Steps), func(s Step) bool { return slices.Contains(ids, s.Chat.ID) })
	return p
}

// Result is what a change did.
type Result struct {
	Summary  string
	Created  int
	Updated  int
	Skipped  int // the target already had the chat as new or newer
	Removed  int // source cards removed by a move, or cards a sync deleted
	Unmarked int // deletion markers a sync removed from lists it wrote the chat into
}

// PlanTransfer plans copying (or moving) chats from one list to another. ids are chat
// references as Chats reports them; prefixes work. A move removes each source card once the
// target holds the chat, whether it was written or already newer there.
func (e *Env) PlanTransfer(from, to List, ids []string, move bool) (Plan, error) {
	if from.Dir == "" || to.Dir == "" {
		return Plan{}, errf(NoList, "ref", "")
	}
	if sameDir(from.Dir, to.Dir) {
		return Plan{}, errf(SameList)
	}
	chats, err := e.Chats(from)
	if err != nil {
		return Plan{}, err
	}
	picked, err := pickChats(chats, ids)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Kind: Copy, From: from, To: to}
	if move {
		p.Kind = Move
	}
	for _, c := range picked {
		target := filepath.Join(to.Dir, c.ID+".json")
		p.Steps = append(p.Steps, Step{Action: decide(c.Last, target), Chat: c, List: to, Target: target})
	}
	return p, nil
}

// PlanSync plans syncing every chat list with to (list A): the newest copy of every chat found
// in any list is pulled into A, and A's resulting cards are pushed to every other list, across
// every account and org. Each step names the list it writes; steps for A come first.
func (e *Env) PlanSync(to List) (Plan, error) {
	if to.Dir == "" {
		return Plan{}, errf(NoList, "ref", "")
	}
	all, err := e.holdings(to)
	if err != nil {
		return Plan{}, err
	}
	newest, copies := map[string]Chat{}, map[string][]Chat{}
	for _, h := range all {
		for _, c := range h.chats {
			copies[c.ID] = append(copies[c.ID], c)
			if n, ok := newest[c.ID]; !ok || c.Last.After(n.Last) {
				newest[c.ID] = c
			}
		}
	}
	marks, err := markers(to)
	if err != nil {
		return Plan{}, err
	}
	var gone []Step
	for _, c := range slices.SortedFunc(maps.Values(newest), newestFirst) {
		if _, kept := all[0].chats[c.ID]; kept {
			continue
		}
		when, ok := markedAt(copies[c.ID], marks)
		if !ok {
			continue // never in A, or lost from it without a marker: pulled in below
		}
		delete(newest, c.ID)
		if c.Last.After(when) {
			gone = append(gone, Step{Action: Conflict, Chat: c, List: c.List, Target: c.Path})
			continue
		}
		gone = append(gone, deletions(all, c.ID)...)
	}
	p := Plan{Kind: Sync, To: to}
	for i, h := range all {
		for _, c := range slices.SortedFunc(maps.Values(newest), newestFirst) {
			archived := c.Archived
			if a, ok := all[0].chats[c.ID]; ok {
				archived = a.Archived // archived state follows A
			}
			missing := Create
			if i == 0 {
				missing = Pull
			}
			p.Steps = append(p.Steps, writes(c, archived, h, missing)...)
		}
	}
	p.Steps = append(p.Steps, gone...)
	return p, nil
}

// holding is one chat list, its chats by id and the transcript ids its deletion markers name.
type holding struct {
	list  List
	chats map[string]Chat
	marks map[string]bool
}

// holdings reads every chat list's chats, to's first.
func (e *Env) holdings(to List) ([]holding, error) {
	index, err := e.Index(e.Projects)
	if err != nil {
		return nil, err
	}
	lists, err := e.Lists()
	if err != nil {
		return nil, err
	}
	order := []List{to}
	for _, l := range lists {
		if !sameDir(l.Dir, to.Dir) {
			order = append(order, l)
		}
	}
	all := make([]holding, len(order))
	for i, l := range order {
		chats, err := chatsOf(l, index)
		if err != nil {
			return nil, err
		}
		marks, err := l.Tombstones()
		if err != nil {
			return nil, err
		}
		all[i] = holding{list: l, chats: map[string]Chat{}, marks: map[string]bool{}}
		for _, c := range chats {
			all[i].chats[c.ID] = c
		}
		for _, id := range marks {
			all[i].marks[id] = true
		}
	}
	return all, nil
}

// writes are the steps that write c into h's list: push's, then an Unmark when the list holds its
// own deletion markers for the chat, so it never holds both a card and a marker.
func writes(c Chat, archived bool, h holding, missing Action) []Step {
	s, ok := push(c, archived, h, missing)
	if !ok {
		return nil
	}
	out := []Step{s}
	var ids []string
	for _, id := range append([]string{c.Session}, c.Prior...) {
		if id != "" && h.marks[id] && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) > 0 {
		out = append(out, Step{Action: Unmark, Chat: c, List: h.list, Target: s.Target, Markers: ids})
	}
	return out
}

// push is the step that writes c, with the archived state given, into h's list: missing when the
// list lacks the chat, Update when its copy is older (Also naming an archived change on the way),
// Archive or Unarchive when only the archived state differs. ok is false when the list already
// has the chat as it would be written.
func push(c Chat, archived bool, h holding, missing Action) (Step, bool) {
	s := Step{Action: missing, Chat: c, List: h.list, Target: filepath.Join(h.list.Dir, c.ID+".json"), Archived: archived}
	have, ok := h.chats[c.ID]
	if !ok {
		return s, true
	}
	var change Action
	if have.Archived != archived {
		change = Unarchive
		if archived {
			change = Archive
		}
	}
	switch {
	case c.Last.After(have.Last):
		s.Action, s.Also = Update, change
	case change != "":
		s.Action = change
	default:
		return Step{}, false
	}
	return s, true
}

// deletions are the steps that delete chat id from every list holding it.
func deletions(all []holding, id string) []Step {
	var out []Step
	for _, h := range all {
		if have, ok := h.chats[id]; ok {
			out = append(out, Step{Action: Delete, Chat: have, List: h.list, Target: have.Path})
		}
	}
	return out
}

// markers maps each transcript id deleted in list l to when it was deleted: the marker holds the
// time in ms; the file's modification time stands in when it does not.
func markers(l List) (map[string]time.Time, error) {
	ids, err := l.Tombstones()
	if err != nil {
		return nil, err
	}
	out := make(map[string]time.Time, len(ids))
	for _, id := range ids {
		path := filepath.Join(l.Dir, "deleted_"+id)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if ms, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err == nil && ms > 0 {
			out[id] = time.UnixMilli(ms)
		} else if st, err := os.Stat(path); err == nil {
			out[id] = st.ModTime()
		}
	}
	return out, nil
}

// markedAt is when the latest marker naming a current or prior transcript of any copy of a chat
// was made, and whether there is one.
func markedAt(copies []Chat, marks map[string]time.Time) (time.Time, bool) {
	var when time.Time
	found := false
	for _, c := range copies {
		for _, id := range append([]string{c.Session}, c.Prior...) {
			if t, ok := marks[id]; ok && id != "" && (!found || t.After(when)) {
				when, found = t, true
			}
		}
	}
	return when, found
}

func newestFirst(a, b Chat) int { return cmp.Or(b.Last.Compare(a.Last), strings.Compare(a.ID, b.ID)) }

// decide applies newest wins to a copy used at last and the target card file.
func decide(last time.Time, target string) Action {
	if _, err := os.Lstat(target); errors.Is(err, fs.ErrNotExist) {
		return Create
	}
	var have time.Time // an unreadable target counts as oldest, so it is backed up and replaced
	if c, err := claude.ReadCard(target); err == nil {
		have = lastOf(c)
	}
	if last.After(have) {
		return Update
	}
	return SkipNewer
}

// decideSync is decide for a sync, which also writes over a copy just as new when only its
// archived state differs.
func decideSync(last time.Time, archived bool, target string) Action {
	a := decide(last, target)
	if a != SkipNewer {
		return a
	}
	if c, err := claude.ReadCard(target); err == nil && lastOf(c).Equal(last) && c.Archived() != archived {
		return Update
	}
	return SkipNewer
}

// withArchived is a card's bytes with the archived state set, the bytes as they are when it
// already has it, so every other field survives.
func withArchived(card *claude.Card, data []byte, archived bool) ([]byte, error) {
	if card.Archived() == archived {
		return data, nil
	}
	if err := card.Set("isArchived", archived); err != nil {
		return nil, err
	}
	return card.Marshal(), nil
}

// freshFunc is the bytes a step writes from the source card and what newest wins says to do
// with them, decided against the target as it is now.
type freshFunc func(card *claude.Card, data []byte, s Step) ([]byte, Action, error)

// fresh is freshFunc for a copy or move: the bytes as they are.
func fresh(card *claude.Card, data []byte, s Step) ([]byte, Action, error) {
	return data, decide(lastOf(card), s.Target), nil
}

// freshSync is freshFunc for a sync: the card gets the step's archived state, and a copy just as
// new is written over when only that differs.
func freshSync(card *claude.Card, data []byte, s Step) ([]byte, Action, error) {
	data, err := withArchived(card, data, s.Archived)
	return data, decideSync(lastOf(card), s.Archived, s.Target), err
}

// Apply carries out a plan, journaled so Undo restores every byte. Each step is decided again
// against the files as they are now, so a stale plan never writes over a newer card.
func (e *Env) Apply(p Plan) (Result, error) {
	var res Result
	if err := e.closed(); err != nil {
		return res, err
	}
	j, err := e.Vault.NewJournal(p.Kind, e.Now())
	if err != nil {
		return res, err
	}
	write := fresh
	if p.Kind == Sync {
		write = freshSync
	}
	for _, s := range p.Steps {
		if err = apply(j, s, write, p.Kind == Move, &res); err != nil {
			break
		}
	}
	res.Summary = summary(p)
	if serr := j.Save(res.Summary); err == nil {
		err = serr
	}
	return res, err
}

// apply carries out one step: write works out the bytes and what to do with them, and a move
// then removes the source card.
func apply(j *store.Journal, s Step, write freshFunc, move bool, res *Result) error {
	switch s.Action {
	case Conflict:
		return nil
	case Delete:
		return removeCard(j, s, res)
	case Unmark:
		return unmark(j, s, res)
	}
	data, err := os.ReadFile(s.Chat.Path)
	if err != nil {
		return err
	}
	card, err := claude.ParseCard(data)
	if err != nil {
		return err
	}
	data, a, err := write(card, data, s)
	if err != nil {
		return err
	}
	if err := writeCard(j, s.Target, data, a, res); err != nil {
		return err
	}
	if !move {
		return nil
	}
	if err := j.Removed(s.Chat.Path); err != nil {
		return err
	}
	if err := os.Remove(s.Chat.Path); err != nil {
		return err
	}
	res.Removed++
	return nil
}

// removeCard backs up and removes a card a sync deletes, unless it was used after the plan was
// worked out.
func removeCard(j *store.Journal, s Step, res *Result) error {
	c, err := claude.ReadCard(s.Target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err == nil && lastOf(c).After(s.Chat.Last) {
		res.Skipped++
		return nil
	}
	if err := j.Removed(s.Target); err != nil {
		return err
	}
	if err := os.Remove(s.Target); err != nil {
		return err
	}
	res.Removed++
	return nil
}

// unmark backs up and removes the list's markers a step names, those still there, once the list
// holds the chat's card.
func unmark(j *store.Journal, s Step, res *Result) error {
	if _, err := os.Lstat(s.Target); err != nil {
		res.Skipped++
		return nil
	}
	for _, id := range s.Markers {
		path := filepath.Join(s.List.Dir, "deleted_"+id)
		if _, err := os.Lstat(path); err != nil {
			continue
		}
		if err := j.Removed(path); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		res.Unmarked++
	}
	return nil
}

// writeCard puts a card's bytes at target as action a says, journaling first. The bytes are copied
// as they are, so every field survives.
func writeCard(j *store.Journal, target string, data []byte, a Action, res *Result) error {
	var err error
	switch a {
	case Create:
		err = j.Created(target)
	case Update:
		err = j.Removed(target)
	default:
		res.Skipped++
		return nil
	}
	if err == nil {
		err = claude.WriteFileAtomic(target, data)
	}
	if err != nil {
		return err
	}
	if a == Create {
		res.Created++
	} else {
		res.Updated++
	}
	return nil
}

func summary(p Plan) string {
	n := p.Cards()
	switch p.Kind {
	case Sync:
		return i18n.T("ops.done.sync", "n", n, "to", p.To.Label)
	case Move:
		return i18n.T("ops.done.move", "n", n, "from", p.From.Label, "to", p.To.Label)
	}
	return i18n.T("ops.done.copy", "n", n, "from", p.From.Label, "to", p.To.Label)
}
