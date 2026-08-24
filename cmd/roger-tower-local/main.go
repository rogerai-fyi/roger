// Command roger-tower-local serves a STANDALONE Tower's consumer plane, and it is Core-free
// by construction: its entire dependency graph links none of towerjoin, towercore, or
// towerhub, so there is no code in this binary that could dial Roger Core or bridge local
// traffic onto the Open Market. A dependency-graph test enforces that. The listener lives
// here, in main; the consumer handler (internal/localplane) opens no socket of its own and
// makes no outbound call.
//
// Contract: features/tower/standalone_consumer_plane.feature.
package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/tower"
)

// osExit is a seam so main's failure exit is testable without ending the test process.
var osExit = os.Exit

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "roger-tower-local:", err)
		osExit(1)
	}
}

// prepared is a consumer plane wired and bound, ready to serve. Splitting preparation from
// the serve loop keeps every decision that can fail - flags, bind posture, mode, lock, bind -
// testable without standing up a blocking server.
type prepared struct {
	srv     *http.Server
	ln      net.Listener
	release func() error
}

// prepare validates flags and the bind posture, opens the standalone Tower, takes its lock,
// and binds the listener. It returns everything the serve loop needs, or an error that names
// exactly what was wrong - before anything is half up.
func prepare(args []string, out io.Writer) (*prepared, error) {
	fs := flag.NewFlagSet("roger-tower-local", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "standalone Tower data directory")
	bind := fs.String("bind", localplane.DefaultBind, "address to listen on (host:port); loopback by default")
	allowPublic := fs.Bool("allow-public", false, "acknowledge binding a public or all-interfaces address (a broker lookalike risk)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if *dir == "" {
		return nil, fmt.Errorf("--dir is required")
	}

	// The bind posture is decided BEFORE the data directory is touched: an exposure mistake
	// should fail fast with a precise message, not after the network is half up.
	addr, note, err := localplane.ResolveBind(*bind, *allowPublic)
	if err != nil {
		return nil, err
	}

	st, err := tower.Open(*dir)
	if err != nil {
		return nil, err
	}
	// STANDALONE ONLY. A joined Tower's consumer clients are admitted by Roger Core; serving
	// them from this Core-free binary would be serving an identity this process cannot verify.
	if err := st.RequireMode(tower.ModeStandalone); err != nil {
		return nil, err
	}
	// The consumer plane reads admission and (as it grows) records receipts, so it takes the
	// identity-directory lock: two planes on one directory would race the local state.
	release, err := st.Lock()
	if err != nil {
		return nil, err
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		_ = release()
		return nil, fmt.Errorf("cannot listen on %s: %w", addr, err)
	}
	fmt.Fprintf(out, "roger-tower-local: %s\n", note)
	fmt.Fprintf(out, "serving the standalone consumer plane on http://%s\n", ln.Addr())
	fmt.Fprintf(out, "point a client at it with: roger config set broker http://%s\n", ln.Addr())

	return &prepared{
		srv:     &http.Server{Handler: localplane.New(st).Handler(), ReadHeaderTimeout: 10 * time.Second},
		ln:      ln,
		release: release,
	}, nil
}

// serve runs the bound plane until the server is closed, then releases the directory lock.
func (p *prepared) serve() error {
	defer func() { _ = p.release() }()
	if serr := p.srv.Serve(p.ln); serr != nil && serr != http.ErrServerClosed {
		return serr
	}
	return nil
}

func run(args []string, out io.Writer) error {
	p, err := prepare(args, out)
	if err != nil {
		return err
	}
	return p.serve()
}
