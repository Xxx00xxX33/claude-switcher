package ops

import (
	"cmp"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
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
	Archive   Action = "archive"    // sync: the list's copy gets archived because A's is (its content may update too)
	Unarchive Action = "unarchive"  // sync: the list's copy gets unarchived because A's is not
)

// Step is one chat of a plan.
type Step struct {
	Action Action
	Chat   Chat   // the copy to write
	List   List   // the list the step writes
	Target string // its card file in List
	// Archived is the archived state a sync writes: A's when A holds the chat. Copy and move
	// leave the card as it is.
	Archived bool
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

// Result is what a change did.
type Result struct {
	Summary string
	Created int
	Updated int
	Skipped int // the target already had the chat as new or newer
	Removed int // source cards removed by a move
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
	index, err := e.Index(e.Projects)
	if err != nil {
		return Plan{}, err
	}
	lists, err := e.Lists()
	if err != nil {
		return Plan{}, err
	}
	type held struct {
		list  List
		chats map[string]Chat
	}
	all := []held{{list: to}}
	for _, l := range lists {
		if !sameDir(l.Dir, to.Dir) {
			all = append(all, held{list: l})
		}
	}
	newest := map[string]Chat{}
	for i := range all {
		chats, err := chatsOf(all[i].list, index)
		if err != nil {
			return Plan{}, err
		}
		all[i].chats = map[string]Chat{}
		for _, c := range chats {
			all[i].chats[c.ID] = c
			if n, ok := newest[c.ID]; !ok || c.Last.After(n.Last) {
				newest[c.ID] = c
			}
		}
	}
	p := Plan{Kind: Sync, To: to}
	for _, h := range all {
		for _, c := range slices.SortedFunc(maps.Values(newest), newestFirst) {
			archived := c.Archived
			if a, ok := all[0].chats[c.ID]; ok {
				archived = a.Archived // archived state follows A
			}
			action := Create
			if h.list.Dir == to.Dir {
				action = Pull
			}
			if have, ok := h.chats[c.ID]; ok {
				switch {
				case have.Archived != archived && archived:
					action = Archive
				case have.Archived != archived:
					action = Unarchive
				case !c.Last.After(have.Last):
					continue // already the newest copy
				default:
					action = Update
				}
			}
			p.Steps = append(p.Steps, Step{Action: action, Chat: c, List: h.list, Target: filepath.Join(h.list.Dir, c.ID+".json"), Archived: archived})
		}
	}
	return p, nil
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
	for _, s := range p.Steps {
		if err = apply(j, p.Kind, s, &res); err != nil {
			break
		}
	}
	res.Summary = summary(p)
	if serr := j.Save(res.Summary); err == nil {
		err = serr
	}
	return res, err
}

func apply(j *store.Journal, kind string, s Step, res *Result) error {
	data, err := os.ReadFile(s.Chat.Path)
	if err != nil {
		return err
	}
	card, err := claude.ParseCard(data)
	if err != nil {
		return err
	}
	a := decide(lastOf(card), s.Target)
	if kind == Sync {
		if data, err = withArchived(card, data, s.Archived); err != nil {
			return err
		}
		a = decideSync(lastOf(card), s.Archived, s.Target)
	}
	if err := writeCard(j, s.Target, data, a, res); err != nil {
		return err
	}
	if kind != Move {
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
	n := len(p.Steps)
	switch p.Kind {
	case Sync:
		return i18n.T("ops.done.sync", "n", n, "to", p.To.Label)
	case Move:
		return i18n.T("ops.done.move", "n", n, "from", p.From.Label, "to", p.To.Label)
	}
	return i18n.T("ops.done.copy", "n", n, "from", p.From.Label, "to", p.To.Label)
}
