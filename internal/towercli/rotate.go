package towercli

// rotate.go is `roger tower rotate-key` (features/join/tower_identity.feature, a reset Tower): a
// new TLS key, so the Tower serves under a new fingerprint. Every client and Station pinned to the
// old key is refused until it re-joins with a new invite, and outstanding invites stop working.

import (
	"flag"
	"fmt"
	"io"

	"rogerai.fm/roger/v7/internal/towerpin"
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

// cmdRevokeClient is `roger tower revoke-client NAME`: the undo for a join, by the name the client
// joined under (or its key hash). It works while the Tower serves, through the admin socket.
func cmdRevokeClient(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("revoke-client", flag.ContinueOnError)
	fs.SetOutput(out)
	dir, cfg := dirAndConfig(fs)
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("name the client: roger tower revoke-client --dir DIR NAME")
	}
	ops, release, err := openOps(*dir, *cfg)
	if err != nil {
		return err
	}
	defer release()
	name := fs.Arg(0)
	if _, err := ops.RevokeClientNamed(name); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	fmt.Fprintf(out, "%s can no longer use this Tower; it needs a new invite to join again\n", name)
	return nil
}
