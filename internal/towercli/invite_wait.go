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
	st, err := tower.Open(dir)
	if err != nil {
		return
	}
	for {
		inv, err := st.Invitation(id)
		switch {
		case err != nil:
			return
		case inv.Consumed:
			fmt.Fprintln(out, tower.JoinedLine(inv.JoinedName, inv.ClientKeyHash, inv.Kind))
			return
		case inv.Voided || time.Now().UnixNano() > inv.ExpiresAt || time.Now().After(deadline):
			fmt.Fprintln(out, "no one used this invite; mint another with: roger tower invite")
			return
		}
		time.Sleep(poll)
	}
}
