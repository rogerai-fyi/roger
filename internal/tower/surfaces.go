package tower

// surfaces.go is what the owner's screens say during a join (features/join/surfaces.feature): the
// CLI, the TUI and the console all take their sentences from here, so they say the same thing.

import (
	"fmt"
	"strings"
	"time"
)

// InviteHeading says what an invite is for and how long it lasts: "invite for one device, valid 15 min:".
func InviteHeading(kind JoinKind, validFor time.Duration) string {
	who := "one device"
	if kind == JoinStation {
		who = "one station"
	}
	return fmt.Sprintf("invite for %s, valid %d min:", who, int(validFor.Round(time.Minute).Minutes()))
}

// InviteNext is the one next step: where to run the invite line that follows it.
func InviteNext(kind JoinKind) string {
	if kind == JoinStation {
		return "on the station run:"
	}
	return "on the other machine run:"
}

// AskLine is who is asking to join and the phrase both screens show: "laptop-x wants to join ·
// amber-river-71", with a station's models.
func AskLine(r PairRequest) string {
	who := r.Name
	if r.Kind == JoinStation {
		who += " (a station)"
	}
	line := fmt.Sprintf("%s wants to join · %s", who, r.Phrase)
	if len(r.Models) > 0 {
		line += " · serving: " + strings.Join(r.Models, ", ")
	}
	return line
}

// ApproveHint is the one rule the owner applies before approving.
func ApproveHint(name string) string {
	return fmt.Sprintf("approve only if %s shows the same phrase", name)
}

// MinutesLeft is how long an invite still works, in whole minutes, for a screen that shows it.
func MinutesLeft(expiresAt int64, now time.Time) string {
	left := time.Unix(0, expiresAt).Sub(now)
	if left <= 0 {
		return "expired"
	}
	m := int((left + time.Minute - 1) / time.Minute)
	if m == 1 {
		return "1 min left"
	}
	return fmt.Sprintf("%d min left", m)
}
