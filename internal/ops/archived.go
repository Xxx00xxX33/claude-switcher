package ops

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/nyxon-tech/claude-switcher/v3/internal/claude"
	"github.com/nyxon-tech/claude-switcher/v3/internal/store"
)

// archiveRecord is each list's archived state for its chats as the last sync left them, by list
// ("account/org") and card id. A list whose state differs from it now was changed by the user
// since, and a sync carries that change to every list.
type archiveRecord map[string]map[string]bool

func (e *Env) archiveRecordPath() string { return filepath.Join(e.Vault.Dir, "_archived.json") }

func listKey(l List) string { return l.Account + "/" + l.Org }

// readArchiveRecord reads the record; none yet is an empty one.
func (e *Env) readArchiveRecord() (archiveRecord, error) {
	data, err := os.ReadFile(e.archiveRecordPath())
	if errors.Is(err, fs.ErrNotExist) {
		return archiveRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	var rec archiveRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return archiveRecord{}, nil // a damaged record only loses what it remembered
	}
	return rec, nil
}

// changedSince reports whether h's copy c of a chat has another archived state than the record
// says the last sync left there.
func (r archiveRecord) changedSince(h holding, c Chat) bool {
	was, ok := r[listKey(h.list)][c.ID]
	return ok && was != c.Archived
}

// latestArchived is the archived state chat id ends up with. base is A's state (the newest copy's
// when A lacks the chat). When lists other than A changed it since the last sync and agree, their
// state wins; a change made in A, or changes that disagree, leave base.
func latestArchived(all []holding, rec archiveRecord, id string, base bool) bool {
	if a, ok := all[0].chats[id]; ok && rec.changedSince(all[0], a) {
		return base
	}
	var got []bool
	for _, h := range all[1:] {
		if c, ok := h.chats[id]; ok && rec.changedSince(h, c) {
			got = append(got, c.Archived)
		}
	}
	if len(got) == 0 || slices.Contains(got, !got[0]) {
		return base
	}
	return got[0]
}

// recordArchived saves every list's archived state after a sync to to, journaled when j is not
// nil. A chat whose copies still disagree (left out, or left alone as a conflict) keeps what the
// record said, so its change is still seen next time.
func (e *Env) recordArchived(j *store.Journal, to List) error {
	all, err := e.holdings(to)
	if err != nil {
		return err
	}
	old, err := e.readArchiveRecord()
	if err != nil {
		return err
	}
	states := map[string][]bool{}
	for _, h := range all {
		for id, c := range h.chats {
			states[id] = append(states[id], c.Archived)
		}
	}
	rec := archiveRecord{}
	for _, h := range all {
		key := listKey(h.list)
		rec[key] = map[string]bool{}
		for id, c := range h.chats {
			if s := states[id]; !slices.Contains(s, !s[0]) {
				rec[key][id] = c.Archived
			} else if was, ok := old[key][id]; ok {
				rec[key][id] = was
			}
		}
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	path := e.archiveRecordPath()
	if j != nil {
		journal := j.Created
		if _, err := os.Stat(path); err == nil {
			journal = j.Removed
		}
		if err := journal(path); err != nil {
			return err
		}
	}
	return claude.WriteFileAtomic(path, data)
}

// RecordSync saves the archive record after a sync that had nothing to write, when there is no
// record yet, so the first archive change after it is seen.
func (e *Env) RecordSync(to List) error {
	if _, err := os.Stat(e.archiveRecordPath()); err == nil {
		return nil
	}
	return e.recordArchived(nil, to)
}
