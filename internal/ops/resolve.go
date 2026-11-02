package ops

// Choice is what the user decided for a sync conflict.
type Choice int

const (
	Undecided    Choice = iota // left alone, which Apply does
	DeleteAnyway               // every copy is deleted, as A deleted it
	KeepAndPull                // the newest copy is pulled into A and out to every list
)

// Resolve settles a sync plan's conflicts by chat id: KeepAndPull pulls the newest copy into A
// and out to every list, as if A had never deleted it; DeleteAnyway deletes every copy anyway.
// Conflicts left Undecided stay as they are, which Apply leaves alone.
func (e *Env) Resolve(p Plan, choices map[string]Choice) (Plan, error) {
	if p.Count(Conflict) == 0 || len(choices) == 0 {
		return p, nil
	}
	all, err := e.holdings(p.To)
	if err != nil {
		return Plan{}, err
	}
	out := p
	out.Steps = nil
	for _, s := range p.Steps {
		if s.Action != Conflict {
			out.Steps = append(out.Steps, s)
			continue
		}
		switch choices[s.Chat.ID] {
		case DeleteAnyway:
			out.Steps = append(out.Steps, deletions(all, s.Chat.ID)...)
		case KeepAndPull:
			for i, h := range all {
				missing := Create
				if i == 0 {
					missing = Pull
				}
				if st, ok := push(s.Chat, s.Chat.Archived, h, missing); ok {
					out.Steps = append(out.Steps, st)
				}
			}
		default:
			out.Steps = append(out.Steps, s)
		}
	}
	return out, nil
}
