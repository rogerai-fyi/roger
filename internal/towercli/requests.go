package towercli

// requests.go is the owner's side of request-and-approve from the CLI (features/join/approve.feature,
// "a request while no owner screen is open"): `roger tower requests` lists the join requests waiting
// for the serving Tower, each with its matching phrase, and `roger tower approve|refuse <id>`
// decides one. Requests live in the serving process, so these reach it through the admin socket.

import (
	"flag"
	"fmt"
	"io"
	"time"

	"rogerai.fm/roger/v6/internal/tower"
	"rogerai.fm/roger/v6/internal/toweradmin"
)

func pairingClient(dir string) (toweradmin.Client, error) {
	if dir == "" {
		return toweradmin.Client{}, fmt.Errorf("--dir is required")
	}
	if !toweradmin.Running(dir) {
		return toweradmin.Client{}, fmt.Errorf("this Tower is not serving, so no one can be asking to join: start it with roger tower serve --dir %s", dir)
	}
	return toweradmin.Client{Dir: dir}, nil
}

func cmdRequests(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("requests", flag.ContinueOnError)
	fs.SetOutput(out)
	dir, _ := dirAndConfig(fs)
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	c, err := pairingClient(*dir)
	if err != nil {
		return err
	}
	reqs, err := c.PairRequests()
	if err != nil {
		return err
	}
	if len(reqs) == 0 {
		fmt.Fprintln(out, "no one is asking to join")
		return nil
	}
	for _, r := range reqs {
		fmt.Fprintln(out, RequestLine(r, time.Now()))
		fmt.Fprintf(out, "    %s: roger tower approve %s (or refuse %s)\n", tower.ApproveHint(r.Name), r.ID, r.ID)
	}
	return nil
}

// RequestLine is how one waiting request reads on every owner screen.
func RequestLine(r tower.PairRequest, now time.Time) string {
	line := r.ID + "  " + tower.AskLine(r)
	if left := r.ExpiresAt.Sub(now).Round(time.Second); left > 0 {
		line += fmt.Sprintf(" · %s left", left)
	}
	return line
}

func cmdDecide(op string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet(op, flag.ContinueOnError)
	fs.SetOutput(out)
	dir, _ := dirAndConfig(fs)
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("name the request: roger tower %s --dir DIR ID (roger tower requests lists them)", op)
	}
	c, err := pairingClient(*dir)
	if err != nil {
		return err
	}
	id := fs.Arg(0)
	if op == "approve" {
		if err := c.ApprovePair(id); err != nil {
			return err
		}
		fmt.Fprintf(out, "approved %s\n", id)
		return nil
	}
	if err := c.RefusePair(id); err != nil {
		return err
	}
	fmt.Fprintf(out, "refused %s; nothing was admitted\n", id)
	return nil
}
