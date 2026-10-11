package towercli

// invite_wait.go keeps `roger tower invite` on screen until its invite is used, then says who
// joined and how to undo it (features/join/invite.feature, "The owner sees the join as it
// happens"). It reads the data directory without the lock, so it works while serve holds it.

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mattn/go-isatty"

	"rogerai.fm/roger/v6/internal/tower"
)

// isTerminal reports whether out is a terminal someone is watching.
func isTerminal(out io.Writer) bool {
	f, ok := out.(*os.File)
	return ok && (isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd()))
}

// waitForJoin polls the invite until it is used, voided or expired (or until deadline).
func waitForJoin(dir, id string, out io.Writer, poll time.Duration, deadline time.Time) {
	for {
		line, over := inviteOutcome(dir, id)
		if over || time.Now().After(deadline) {
			if !over || line == "" {
				line = inviteUnused
			}
			fmt.Fprintln(out, line)
			return
		}
		time.Sleep(poll)
	}
}

// inviteUnused is what an invite that ran out unused says.
const inviteUnused = "no one used this invite; mint another with: roger tower invite"

// inviteOutcome is one look at an invite: once it is used, who joined and how to undo it; once it
// can no longer be used, the unused line; otherwise not over yet. It reads the data directory
// without the lock, so it works while serve holds it. An unreadable directory ends the wait quietly.
func inviteOutcome(dir, id string) (line string, over bool) {
	st, err := tower.Open(dir)
	if err != nil {
		return "", true
	}
	inv, err := st.Invitation(id)
	switch {
	case err != nil:
		return "", true
	case inv.Consumed:
		return tower.JoinedLine(inv.JoinedName, inv.ClientKeyHash, inv.Kind, stationModels(st, inv.ClientKeyHash)), true
	case inv.Voided || time.Now().UnixNano() > inv.ExpiresAt:
		return inviteUnused, true
	}
	return "", false
}

// stationModels is what the Station attached under keyHash serves, for the joined line.
func stationModels(st *tower.State, keyHash string) []string {
	sts, _ := st.Stations()
	for _, s := range sts {
		if s.KeyHash == keyHash {
			return s.Models
		}
	}
	return nil
}
