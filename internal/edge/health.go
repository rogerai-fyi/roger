package edge

// FLEET HEALTH (features/edge/health.feature §2): one snapshot of every node's LATEST readings and
// their age, for the surfaces to draw. A node that reports nothing carries no readings - never a
// record of zeros. A stale reading is kept and labelled with its age, never presented as current.
// An offline node says how long it has been dark, judged by the always-on authority's last-heard
// time rather than by anything the dead node could send.

import (
	"time"

	"rogerai.fm/roger/v6/internal/store"
)

// NodeHealth is one node's health as the fleet last knew it.
type NodeHealth struct {
	ID, Name, Kind string
	Presence       string
	// Tags are the owner's words for this unit, so a tag-scoped rule can judge it
	// (features/edge/tags.feature).
	Tags      []string
	Dark      bool
	DarkFor   time.Duration // how long it has been unheard, when Dark
	LastHeard time.Time
	// Readings is the newest report across the node's instances, or nil when it reports nothing.
	Readings *store.EdgeResources
	ReadAge  time.Duration // how old Readings is
	// Power is the node's newest power report - from its own readings when it is dialed, from its
	// signed v3 presence when it dials out - and PowerAge how old it is. nil when it reports none.
	Power    *store.EdgePower
	PowerAge time.Duration
	// Asleep is a phone whose last beat said the system put its app in the background.
	Asleep bool
}

// MemoryLevel is memory as a level of a REAL total, drawable as a bar. ok is false when the node
// reports no total: a figure with no denominator is shown as a figure, not a bar.
func (h NodeHealth) MemoryLevel() (float64, bool) {
	if h.Readings == nil || h.Readings.RAMTotalGB <= 0 {
		return 0, false
	}
	return h.Readings.RAMUsedGB / h.Readings.RAMTotalGB, true
}

// DiskLevel is the fullest mounted disk as a level, or ok=false when no disk reports a total.
func (h NodeHealth) DiskLevel() (float64, bool) {
	if h.Readings == nil {
		return 0, false
	}
	best, ok := 0.0, false
	for _, d := range h.Readings.Disks {
		if d.TotalGB > 0 {
			if l := d.UsedGB / d.TotalGB; !ok || l > best {
				best, ok = l, true
			}
		}
	}
	return best, ok
}

// FleetHealth reads the whole fleet's health once.
func FleetHealth(f *Fleet, now time.Time) ([]NodeHealth, error) {
	nodes, err := f.List()
	if err != nil {
		return nil, err
	}
	out := make([]NodeHealth, 0, len(nodes))
	for _, n := range nodes {
		h := NodeHealth{ID: n.ID, Name: n.Name, Kind: n.Kind, Presence: n.Presence,
			Tags: append([]string(nil), n.Tags...), Dark: n.Presence == string(PresenceDark)}
		if n.LastSeen > 0 {
			h.LastHeard = time.Unix(n.LastSeen, 0)
			if h.Dark {
				h.DarkFor = now.Sub(h.LastHeard)
			}
		}
		for i := range n.Instances {
			r := n.Instances[i].Resources
			if r == nil || r.ReadAt == 0 {
				continue
			}
			if h.Readings == nil || r.ReadAt > h.Readings.ReadAt {
				h.Readings = r
			}
		}
		if h.Readings != nil {
			h.ReadAge = now.Sub(time.Unix(h.Readings.ReadAt, 0))
		}
		h.Power = NodePower(n)
		if h.Power != nil {
			h.PowerAge = now.Sub(time.Unix(h.Power.ReadAt, 0))
		}
		h.Asleep = PhoneAsleep(n.Kind, n.Phone)
		out = append(out, h)
	}
	return out, nil
}
