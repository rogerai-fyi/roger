package localmodel

// MODELS ARE GIGABYTES AND THEY ACCUMULATE (local_models.feature §6). With no budget nothing is
// ever removed. With one, room is made from the models served least recently that roger fetched
// itself; a model being served and a file the owner placed by hand are never removed.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// lastUsed is when a model was last served, or fetched when it never was.
func (m Model) lastUsed() time.Time {
	if m.Served.After(m.Fetched) {
		return m.Served
	}
	return m.Fetched
}

// MarkServed records that a model went on the air, which is what "least recently used" counts.
func (h Home) MarkServed(id string, at time.Time) error {
	m, err := h.Find(id)
	if err != nil {
		return err
	}
	if !m.Verified() {
		return nil // a file placed by hand has no record of roger's to write to, and is never evicted
	}
	m.Served = at.UTC()
	return h.record(m)
}

// RoomFor is the models that would be removed to fit need more bytes inside the owner's budget,
// least recently used first. Nothing on the air and nothing still loading is ever on the list.
func (h Home) RoomFor(need int64) ([]Model, error) {
	s, err := h.Settings()
	if err != nil || s.DiskBudget <= 0 {
		return nil, err
	}
	if need > s.DiskBudget {
		return nil, fmt.Errorf("%s is larger than the %s you set for models (roger model budget)",
			BytesWord(need), BytesWord(s.DiskBudget))
	}
	all, err := h.List()
	if err != nil {
		return nil, err
	}
	busy := map[string]string{}
	for _, s := range h.LiveAll() {
		busy[s.ID] = " is being served"
	}
	for _, id := range h.loadingIDs() {
		busy[id] = " is being loaded"
	}
	var used int64
	var can []Model
	var held []string
	for _, m := range all {
		used += m.Bytes
		switch {
		case busy[m.ID] != "":
			held = append(held, m.ID+busy[m.ID])
		case !m.Verified():
			held = append(held, m.ID+" was placed by hand")
		default:
			can = append(can, m)
		}
	}
	sort.SliceStable(can, func(i, j int) bool { return can[i].lastUsed().Before(can[j].lastUsed()) })
	var out []Model
	for _, m := range can {
		if used+need <= s.DiskBudget {
			break
		}
		out = append(out, m)
		used -= m.Bytes
	}
	if used+need > s.DiskBudget {
		return nil, fmt.Errorf("the %s you set for models is held by what roger will not remove (%s); "+
			"raise it (roger model budget) or remove one", BytesWord(s.DiskBudget), strings.Join(held, ", "))
	}
	return out, nil
}

// MakeRoom removes what RoomFor planned, and says what went.
func (h Home) MakeRoom(need int64) ([]Model, error) {
	plan, err := h.RoomFor(need)
	if err != nil {
		return nil, err
	}
	for i, m := range plan {
		if err := h.Remove(m); err != nil {
			return plan[:i], fmt.Errorf("making room: %w", err)
		}
	}
	return plan, nil
}

// RoomLine says what fetching need bytes would remove, or "" when it removes nothing.
func RoomLine(plan []Model) string {
	if len(plan) == 0 {
		return ""
	}
	names := make([]string, len(plan))
	for i, m := range plan {
		names[i] = fmt.Sprintf("%s (%s)", m.ID, BytesWord(m.Bytes))
	}
	return "to stay within the budget you set for models, roger removes first, least recently served: " + strings.Join(names, ", ")
}
