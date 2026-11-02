package ops

import "path/filepath"

// Resolve settles a sync plan's conflicts by chat id: keep pulls the newest copy into A and out
// to every list, as if A had never deleted it; otherwise every copy is deleted anyway. Conflicts
// not named stay as they are, which Apply leaves alone.
func (e *Env) Resolve(p Plan, keep map[string]bool) (Plan, error) {
	if p.Count(Conflict) == 0 || len(keep) == 0 {
		return p, nil
	}
	index, err := e.Index(e.Projects)
	if err != nil {
		return Plan{}, err
	}
	lists, err := e.Lists()
	if err != nil {
		return Plan{}, err
	}
	held := map[string]map[string]Chat{} // list dir, then chat id
	for _, l := range lists {
		chats, err := chatsOf(l, index)
		if err != nil {
			return Plan{}, err
		}
		held[l.Dir] = map[string]Chat{}
		for _, c := range chats {
			held[l.Dir][c.ID] = c
		}
	}
	out := p
	out.Steps = nil
	for _, s := range p.Steps {
		k, named := keep[s.Chat.ID]
		if s.Action != Conflict || !named {
			out.Steps = append(out.Steps, s)
			continue
		}
		if !k {
			for _, l := range lists {
				if have, ok := held[l.Dir][s.Chat.ID]; ok {
					out.Steps = append(out.Steps, Step{Action: Delete, Chat: have, List: l, Target: have.Path})
				}
			}
			continue
		}
		c := s.Chat
		out.Steps = append(out.Steps, Step{Action: Pull, Chat: c, List: p.To, Target: filepath.Join(p.To.Dir, c.ID+".json"), Archived: c.Archived})
		for _, l := range lists {
			if sameDir(l.Dir, p.To.Dir) {
				continue
			}
			action := Create
			if have, ok := held[l.Dir][c.ID]; ok {
				switch {
				case have.Archived != c.Archived && c.Archived:
					action = Archive
				case have.Archived != c.Archived:
					action = Unarchive
				case !c.Last.After(have.Last):
					continue
				default:
					action = Update
				}
			}
			out.Steps = append(out.Steps, Step{Action: action, Chat: c, List: l, Target: filepath.Join(l.Dir, c.ID+".json"), Archived: c.Archived})
		}
	}
	return out, nil
}
