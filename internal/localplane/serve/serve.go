// Package serve runs a STANDALONE Tower's consumer plane (`roger tower serve` on a standalone
// data directory). It imports no Core code: its whole dependency graph links none of towerjoin,
// towercore, towerhub, client or agent, and a dependency-graph test keeps it that way. In the
// process, Run installs the runtime guard (internal/egressguard) before anything else, so every
// connection the process makes must go to a local address, and it refuses to start if a Core
// client was ever built (internal/coreclient). The guarantee is that package-level proof plus the
// runtime guard (features/packaging/one_binary.feature section 4); it is not a claim about every
// line of the binary that carries it. The consumer handler (internal/localplane) opens no
// socket and makes no outbound call; the listener lives here.
//
// Contract: features/tower/standalone_consumer_plane.feature.
package serve

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/coreclient"
	"rogerai.fm/roger/v6/internal/egressguard"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/netbind"
	"rogerai.fm/roger/v6/internal/tower"
)

// Prepared is a consumer plane wired and bound, ready to serve. Splitting preparation from
// the serve loop keeps every decision that can fail - flags, bind posture, mode, lock, bind -
// testable without standing up a blocking server.
type Prepared struct {
	srv       *http.Server
	ln        net.Listener
	release   func() error
	advertise func(context.Context) // nil unless --advertise and the bind is a private address
}

// Prepare validates flags and the bind posture, opens the standalone Tower, takes its lock,
// and binds the listener. It returns everything the serve loop needs, or an error that names
// exactly what was wrong - before anything is half up.
func Prepare(args []string, out io.Writer) (*Prepared, error) {
	fs := flag.NewFlagSet("roger-tower-local", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "standalone Tower data directory")
	bind := fs.String("bind", localplane.DefaultBind, "address to listen on (host:port); loopback by default")
	allowPublic := fs.Bool("allow-public", false, "acknowledge binding a public or all-interfaces address (a broker lookalike risk)")
	advertise := fs.Bool("advertise", false, "announce this Tower by mDNS on private networks, so LAN-mode clients list it as a candidate")
	label := fs.String("label", "", "the name a --advertise announcement carries (default: this machine's hostname)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if *dir == "" {
		return nil, fmt.Errorf("--dir is required")
	}
	// Fail closed: in standalone mode no Core client may exist. One that does is a bug, and the
	// plane does not open its listener beside it.
	if built := coreclient.Noted(); len(built) > 0 {
		return nil, fmt.Errorf("a Core client was constructed in standalone mode (%s): refusing to serve", strings.Join(built, ", "))
	}

	// The bind posture is decided BEFORE the data directory is touched: an exposure mistake
	// should fail fast with a precise message, not after the network is half up.
	if *bind == "" {
		*bind = localplane.DefaultBind
	}
	addr, note, err := netbind.Resolve(*bind, *allowPublic)
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

	ln, err := towerListen("tcp", addr)
	if err != nil {
		_ = release()
		return nil, fmt.Errorf("cannot listen on %s: %w", addr, err)
	}
	fmt.Fprintf(out, "roger-tower-local: %s\n", note)
	fmt.Fprintf(out, "serving the standalone consumer plane on http://%s\n", ln.Addr())
	fmt.Fprintf(out, "point a client at it with: roger config set broker http://%s\n", ln.Addr())

	var adv func(context.Context)
	if *advertise {
		host, portStr, _ := net.SplitHostPort(addr)
		port, _ := strconv.Atoi(portStr)
		if port == 0 { // an OS-chosen port: announce the one actually bound
			if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
				port = tcp.Port
			}
		}
		adv = advertiser(host, port, *label, out)
	}

	return &Prepared{
		advertise: adv,
		// MaxHeaderBytes well below the 1 MiB default: the plane's signing headers are small, and a
		// tight cap stops an unauthenticated caller from making the Tower buffer a large header.
		srv:     &http.Server{Handler: localplane.New(st).Handler(), ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 64 << 10},
		ln:      ln,
		release: release,
	}, nil
}

// Serve runs the bound plane until the server is closed, then releases the directory lock.
func (p *Prepared) Serve() error {
	defer func() { _ = p.release() }()
	if p.advertise != nil {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go p.advertise(ctx)
	}
	if serr := p.srv.Serve(p.ln); serr != nil && serr != http.ErrServerClosed {
		return serr
	}
	return nil
}

// installGuard installs the runtime guard for the life of the process (a seam: a test that runs
// Run puts the process's default transport back afterwards).
var installGuard = egressguard.Install

// Run serves a standalone directory until the server stops. The runtime guard goes in first,
// before anything in the process can dial, and stays for the life of the process.
func Run(args []string, out io.Writer) error {
	installGuard(out)
	p, err := Prepare(args, out)
	if err != nil {
		return err
	}
	return p.Serve()
}
