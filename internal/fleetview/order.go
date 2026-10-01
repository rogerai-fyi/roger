package fleetview

// THE RACK'S ORDER AND FILTER (features/edge/fleet_window.feature). The machine you are at and its
// rogers stay on top, the bench stays at the bottom; between them the units are ordered worst first
// by default - DARK, then TROUBLE, then HEARD, then ON AIR - each group in the order the fleet
// arrived in, so a unit never jumps while it is being read.

import (
	"sort"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
)

type rackOrder int

const (
	orderWorst rackOrder = iota
	orderName
	orderKind
	orderHeard
	orderTemp
)

var orderWords = map[rackOrder]string{orderWorst: "worst first", orderName: "by name", orderKind: "by kind",
	orderHeard: "last heard", orderTemp: "hottest first"}

func nextOrder(o rackOrder) rackOrder { return (o + 1) % rackOrder(len(orderWords)) }

// rank is a unit's place in the worst-first order: lower is worse.
func rank(it FleetItem) int {
	switch {
	case it.Dark:
		return 0
	case len(it.Alerts) > 0:
		return 1
	case !it.Controllable:
		return 2
	}
	return 3
}

func temp(it FleetItem) (float64, bool) {
	if it.Health == nil || it.Health.Readings == nil || it.Health.Readings.CPUTempC == nil {
		return 0, false
	}
	return *it.Health.Readings.CPUTempC, true
}

// matches is whether a unit answers what the owner typed: words match its name, kind and tags;
// kind:, tag: and state: narrow by that field alone.
func matches(it FleetItem, q string, now time.Time) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return true
	}
	field, val, scoped := strings.Cut(q, ":")
	kind := strings.ToLower(edge.KindWord(it.NodeKind))
	if scoped {
		switch field {
		case "kind":
			return kind == val || strings.ToLower(it.NodeKind) == val
		case "tag":
			for _, t := range it.Tags {
				if strings.ToLower(t) == val {
					return true
				}
			}
			return false
		case "state":
			w, _, _ := it.state(now)
			return strings.ToLower(w) == val || strings.HasPrefix(strings.ToLower(w), val)
		}
	}
	hay := strings.ToLower(it.Name + " " + it.label() + " " + kind + " " + strings.Join(it.Tags, " "))
	return strings.Contains(hay, q)
}

// arrange is the rack as drawn: this machine and its rogers, the units in order and filtered, then
// the bench filtered.
func arrange(items []FleetItem, o rackOrder, q string, now time.Time) []FleetItem {
	var top, units, bench []FleetItem
	for _, it := range items {
		switch it.Kind {
		case ItemMember:
			if matches(it, q, now) {
				units = append(units, it)
			}
		case ItemCandidate:
			if matches(it, q, now) {
				bench = append(bench, it)
			}
		default:
			top = append(top, it)
		}
	}
	sort.SliceStable(units, func(i, j int) bool {
		a, b := units[i], units[j]
		switch o {
		case orderName:
			return strings.ToLower(a.label()) < strings.ToLower(b.label())
		case orderKind:
			return edge.KindWord(a.NodeKind) < edge.KindWord(b.NodeKind)
		case orderHeard:
			return a.LastSeen.After(b.LastSeen)
		case orderTemp:
			ta, oka := temp(a)
			tb, okb := temp(b)
			if oka != okb {
				return oka
			}
			return oka && ta > tb
		}
		return rank(a) < rank(b)
	})
	return append(append(top, units...), bench...)
}

// hiddenBy counts the units and devices a filter hides, so the summary can say so.
func hiddenBy(items []FleetItem, q string, now time.Time) int {
	n := 0
	for _, it := range items {
		if (it.Kind == ItemMember || it.Kind == ItemCandidate) && !matches(it, q, now) {
			n++
		}
	}
	return n
}
