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
	"crypto/ed25519"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"rogerai.fm/roger/v6/internal/towerpin"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/coreclient"
	"rogerai.fm/roger/v6/internal/egressguard"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/netbind"
	"rogerai.fm/roger/v6/internal/tower"
	"rogerai.fm/roger/v6/internal/toweradmin"
	"rogerai.fm/roger/v6/internal/towerstore"
)

// Prepared is a consumer plane wired and bound, ready to serve. Splitting preparation from
// the serve loop keeps every decision that can fail - flags, bind posture, mode, lock, bind -
// testable without standing up a blocking server.
type Prepared struct {
	st        *tower.State
	dir       string
	admin     net.Listener // the owner-only admin socket; nil where it is not available
	srv       *http.Server
	ln        net.Listener
	release   func() error
	advertise func(context.Context) // nil unless --advertise and the bind is a private address
	checked   bool                  // --check: everything resolved, nothing bound
}

// Prepare validates flags and the bind posture, opens the standalone Tower, takes its lock,
// and binds the listener. It returns everything the serve loop needs, or an error that names
// exactly what was wrong - before anything is half up.
func Prepare(args []string, out io.Writer) (*Prepared, error) {
	fs := flag.NewFlagSet("roger tower serve", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "standalone Tower data directory")
	cfgPath := fs.String("config", "", "operator configuration file (standalone.bind, standalone.label and standalone.advertise; a flag wins over the file)")
	check := fs.Bool("check", false, "resolve the directory, configuration and bind, say what would be served, and exit without listening")
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
	var cfg *tower.Config
	if *cfgPath != "" {
		c, err := tower.LoadConfig(*cfgPath)
		if err != nil {
			return nil, err
		}
		if c.Mode != tower.ModeStandalone {
			return nil, fmt.Errorf("%s is a %s configuration; this command serves a standalone directory", *cfgPath, c.Mode)
		}
		cfg = c
		set := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
		if sc := c.Standalone; sc != nil {
			if !set["bind"] && sc.Bind != "" {
				*bind = sc.Bind
			}
			if !set["label"] && sc.Label != "" {
				*label = sc.Label
			}
			if !set["advertise"] && sc.Advertise {
				*advertise = true
			}
		}
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
	// A configured database is the store admit and attach wrote to, so the plane reads it too
	// (fail closed: an unreadable or non-local database refuses, never falls back to the files).
	st, closeStore, err := towerstore.ForConfig(cfg, st)
	if err != nil {
		return nil, err
	}
	if *check {
		_ = closeStore()
		fmt.Fprintf(out, "roger tower serve: %s\n", note)
		fmt.Fprintf(out, "would serve the standalone consumer plane on %s\n", addr)
		return &Prepared{checked: true}, nil
	}
	// The consumer plane reads admission and (as it grows) records receipts, so it takes the
	// identity-directory lock: two planes on one directory would race the local state.
	unlock, err := st.Lock()
	if err != nil {
		_ = closeStore()
		return nil, err
	}
	release := func() error {
		err := unlock()
		if cerr := closeStore(); err == nil {
			err = cerr
		}
		return err
	}

	ln, err := towerListen("tcp", addr)
	if err != nil {
		_ = release()
		return nil, fmt.Errorf("cannot listen on %s: %w", addr, err)
	}
	// TLS under the Tower's own key, on the same port as plain http for one release
	// (features/join/tower_identity.feature): a client pinned to this fingerprint trusts exactly it.
	tlsKey, err := st.TLSKey()
	if err != nil {
		_ = ln.Close()
		_ = release()
		return nil, err
	}
	tlsCfg, err := towerpin.ServerTLS(tlsKey)
	if err != nil {
		_ = ln.Close()
		_ = release()
		return nil, err
	}
	fp := towerpin.Fingerprint(tlsKey.Public().(ed25519.PublicKey))
	served := ln
	ln = towerpin.Sniff(ln, tlsCfg)
	fmt.Fprintf(out, "roger tower serve: %s\n", note)
	// The serving line comes last: a caller waiting for it has read everything above.
	fmt.Fprintf(out, "identity sha256:%s (%s)\n", fp, towerpin.Short(fp))
	fmt.Fprintf(out, "point a client at it with: roger lan add %s on each client, or let one in with: roger tower invite --dir %s (one roger join line)\n", served.Addr(), *dir)
	fmt.Fprintf(out, "serving the standalone consumer plane on %s (TLS, and plain http for one more release)\n", served.Addr())

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

	// The plane names the Tower in join answers: the --label (or config) name, else this machine's.
	plane := localplane.New(st)
	name := *label
	if name == "" {
		name, _ = os.Hostname()
	}
	plane.SetLabel(name)

	// The owner's administration commands reach this running Tower through an owner-only socket in
	// the data directory (features/join/admin_socket.feature), since serve holds the lock.
	adminLn, aerr := toweradmin.Listen(*dir)
	if aerr != nil {
		fmt.Fprintf(out, "admin socket not open (%v): stop serve to run administration commands\n", aerr)
		adminLn = nil
	}

	return &Prepared{
		st:        st,
		dir:       *dir,
		admin:     adminLn,
		advertise: adv,
		// MaxHeaderBytes well below the 1 MiB default: the plane's signing headers are small, and a
		// tight cap stops an unauthenticated caller from making the Tower buffer a large header.
		srv:     &http.Server{Handler: plane.Handler(), ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 64 << 10},
		ln:      ln,
		release: release,
	}, nil
}

// Addr is the address the plane is bound to.
func (p *Prepared) Addr() string {
	if p.ln == nil {
		return ""
	}
	return p.ln.Addr().String()
}

// Close stops the server; Serve then returns, closes the admin socket and releases the directory
// lock.
func (p *Prepared) Close() error {
	if p.admin != nil {
		_ = p.admin.Close()
		toweradmin.Remove(p.dir)
	}
	if p.srv == nil {
		return nil
	}
	return p.srv.Close()
}

// Serve runs the bound plane until the server is closed, then releases the directory lock.
func (p *Prepared) Serve() error {
	if p.checked {
		return nil
	}
	defer func() { _ = p.release() }()
	if p.admin != nil {
		go func() { _ = toweradmin.Serve(p.admin, p.st, log.Printf) }()
		defer func() { _ = p.admin.Close(); toweradmin.Remove(p.dir) }()
	}
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
