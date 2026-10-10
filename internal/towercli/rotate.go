package towercli

// rotate.go is `roger tower rotate-key` (features/join/tower_identity.feature, a reset Tower): a
// new TLS key, so the Tower serves under a new fingerprint. Every client and Station pinned to the
// old key is refused until it re-joins with a new invite, and outstanding invites stop working.

import (
	"flag"
	"fmt"
	"io"

	"rogerai.fm/roger/v6/internal/towerpin"
)

func cmdRotateKey(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("rotate-key", flag.ContinueOnError)
	fs.SetOutput(out)
	dir, cfg := dirAndConfig(fs)
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	st, release, err := openDirWith(*dir, *cfg)
	if err != nil {
		return err
	}
	defer release()
	fp, voided, err := st.RotateTLSKey()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "the Tower now serves under sha256:%s (%s)\n", fp, towerpin.Short(fp))
	if voided > 0 {
		fmt.Fprintf(out, "%d outstanding invite(s) no longer work\n", voided)
	}
	fmt.Fprintf(out, "every client and station must re-join with a new invite: roger tower invite\n")
	return nil
}
