package edge

// RE-ADMITTING A FORGOTTEN DEVICE (features/edge/node_identity.feature, approved 2026-10-02).
//
// Forgetting a device takes it off the Edge, name and all. What is kept is a short record THAT it
// left: the name it had, what it is, and when - enough for the owner to bring it back by the name
// they knew it by, and for it to come back under that name. Nothing else of it survives the forget:
// not its pin, its grants, its tags or its history.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/store"
)

// MaxDeparted bounds the record: the oldest is dropped once there are more.
const MaxDeparted = 64

// Departed is what is kept of a forgotten device. Name is empty for a device its owner never named
// (an id is not a name), so it is said by what it is.
type Departed struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Hardware string `json:"hardware,omitempty"`
	At       int64  `json:"at"`
}

// DepartedOf is the record a forget of n at at leaves behind.
func DepartedOf(n store.EdgeNode, at time.Time) Departed {
	d := Departed{ID: n.ID, Kind: n.Kind, Hardware: MachineShort(MachineOf(n)), At: at.Unix()}
	if ownerNamed(n) {
		d.Name = strings.TrimSpace(n.Name)
	}
	return d
}

// RecordDeparted adds d to list: one record per device (a second forget replaces the first), oldest
// first, and no more than MaxDeparted. The list passed in is not modified.
func RecordDeparted(list []Departed, d Departed) []Departed {
	out := make([]Departed, 0, len(list)+1)
	for _, x := range list {
		if x.ID != d.ID {
			out = append(out, x)
		}
	}
	out = append(out, d)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	if len(out) > MaxDeparted {
		out = out[len(out)-MaxDeparted:]
	}
	return out
}

// NewestDeparted is what can be re-admitted, newest first: a device that is a member again is not.
func NewestDeparted(list []Departed, members []store.EdgeNode) []Departed {
	back := make(map[string]bool, len(members))
	for _, n := range members {
		back[n.ID] = true
	}
	out := make([]Departed, 0, len(list))
	for _, d := range list {
		if !back[d.ID] {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out
}

// FindDeparted resolves what the owner typed to one forgotten device: by its id, or by the name it
// had. A name two forgotten devices had is refused rather than guessed at - re-admitting the wrong
// device revokes its certificate - and the refusal gives the ids to choose by.
func FindDeparted(list []Departed, want string) (Departed, bool, error) {
	want = strings.TrimSpace(want)
	if want == "" {
		return Departed{}, false, nil
	}
	var byName []Departed
	for _, d := range list {
		if d.ID == want {
			return d, true, nil
		}
		if d.Name != "" && d.Name == want {
			byName = append(byName, d)
		}
	}
	switch len(byName) {
	case 0:
		return Departed{}, false, nil
	case 1:
		return byName[0], true, nil
	}
	ids := make([]string, 0, len(byName))
	for _, d := range byName {
		ids = append(ids, d.ID)
	}
	return Departed{}, false, fmt.Errorf("%q names %d forgotten devices - re-admit one by its node id: %s",
		want, len(byName), strings.Join(ids, ", "))
}

// DepartedName is the name a returning device had, or "" when it had none or there is no record.
func DepartedName(list []Departed, id string) string {
	for _, d := range list {
		if d.ID == id {
			return d.Name
		}
	}
	return ""
}

// What is the hardware word: the board it reported, else its kind, else "device".
func (d Departed) What() string {
	if d.Hardware != "" {
		return d.Hardware
	}
	if w := KindWord(d.Kind); w != "" {
		return w
	}
	return "device"
}

// DepartedWords says a forgotten device: "attic, a Pi 4 forgotten 3 days ago", or for one that had
// no name, "a Pi 4 forgotten 3 days ago".
func DepartedWords(d Departed, now time.Time) string {
	what := article(d.What()) + " forgotten " + Ago(now.Sub(time.Unix(d.At, 0)))
	if d.Name == "" {
		return what
	}
	return d.Name + ", " + what
}

// ReAdmitQuestion is the confirmation for a device this Edge has a record of.
func ReAdmitQuestion(d Departed, now time.Time) string {
	return "re-admit " + DepartedWords(d, now) + "?"
}

// ReAdmitUnknownQuestion is the confirmation for an id this Edge has no record of: it is quoted as
// what the owner typed, never used as a name.
func ReAdmitUnknownQuestion(typed string) string {
	return fmt.Sprintf("re-admit the device you typed as %q? this Edge has no record of it", typed)
}

// ReAdmitHow is the command that re-admits d: by its name, or, for a device that had none, by the
// only handle there is - as an argument, not as what it is called.
func ReAdmitHow(d Departed) string {
	if d.Name != "" {
		return "roger edge readmit " + d.Name
	}
	return "roger edge readmit " + d.ID
}

// Ago is a duration in words: "just now", "5 minutes ago", "1 hour ago", "3 days ago".
func Ago(d time.Duration) string {
	plural := func(n int, unit string) string {
		if n == 1 {
			return "1 " + unit + " ago"
		}
		return fmt.Sprintf("%d %ss ago", n, unit)
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour")
	default:
		return plural(int(d.Hours()/24), "day")
	}
}

// article puts "a" or "an" before a word, by its first letter.
func article(word string) string {
	if word != "" && strings.ContainsRune("aeiouAEIOU", rune(word[0])) {
		return "an " + word
	}
	return "a " + word
}
