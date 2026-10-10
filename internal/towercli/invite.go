package towercli

// invite.go is `roger tower invite` (features/join/invite.feature). With no --client it mints a
// BEARER invite and prints one line, `roger join <invite>`: the Tower's addresses, its identity
// fingerprint and a one-time code. --station invites a Station; --for limits it to one joiner
// name; --list and --void manage outstanding invites. --client keeps the older two-step flow
// (invite bound to a key, then admit) for one release.

import (
	"flag"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/netpriv"
	"rogerai.fm/roger/v6/internal/tower"
	"rogerai.fm/roger/v6/internal/toweradmin"
)

// addrList is a repeatable --at.
type addrList []string

func (a *addrList) String() string     { return strings.Join(*a, ",") }
func (a *addrList) Set(v string) error { *a = append(*a, v); return nil }

// inviteInterfaceAddrs is a seam: the addresses this machine's interfaces hold.
var inviteInterfaceAddrs = net.InterfaceAddrs

func cmdInvite(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("invite", flag.ContinueOnError)
	fs.SetOutput(out)
	dir, cfg := dirAndConfig(fs)
	client := fs.String("client", "", "the older flow: bind the code to this client's key hash (then roger tower admit)")
	ttl := fs.Duration("ttl", 15*time.Minute, "how long the invite stays valid")
	attempts := fs.Int("attempts", 5, "the older flow: wrong guesses allowed before lockout")
	station := fs.Bool("station", false, "invite a Station (it reports its own models) instead of a client")
	forName := fs.String("for", "", "only a joiner that names itself this may use the invite")
	models := fs.String("models", "", "a Station may serve only these (comma-separated)")
	list := fs.Bool("list", false, "list outstanding invites (never their codes)")
	void := fs.String("void", "", "stop the outstanding invite with this id")
	wait := fs.Bool("wait", isTerminal(out), "stay on screen until the invite is used, then say who joined (default when run in a terminal)")
	var at addrList
	fs.Var(&at, "at", "an address the invite names (host:port; repeatable). Default: where the Tower serves")
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	st, release, err := openOps(*dir, *cfg)
	if err != nil {
		return err
	}
	defer release()
	switch {
	case *list:
		return inviteList(st, out)
	case *void != "":
		if err := st.VoidInvitation(*void); err != nil {
			return err
		}
		fmt.Fprintf(out, "invite %s no longer works\n", *void)
		return nil
	case *client != "":
		inv, code, err := st.CreateInvitation(*client, *ttl, *attempts)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "invitation: %s\n", inv.ID)
		fmt.Fprintf(out, "code: %s\n", code)
		fmt.Fprintf(out, "expires in %s, %d attempts\n", *ttl, *attempts)
		fmt.Fprintf(out, "\nThis code is shown once. It is not stored and cannot be printed again.\n")
		fmt.Fprintf(out, "this older two-step flow goes away next release; the new way: roger tower invite (one paste: roger join)\n")
		return nil
	}
	kind, jkind := tower.JoinClient, joininvite.TowerClient
	if *station {
		kind, jkind = tower.JoinStation, joininvite.TowerStation
	}
	var allow []string
	for _, m := range strings.Split(*models, ",") {
		if m = strings.TrimSpace(m); m != "" {
			allow = append(allow, m)
		}
	}
	addrs := []string(at)
	loopbackOnly := false
	if len(addrs) == 0 {
		addrs, loopbackOnly, err = inviteAddrs(*cfg)
		if err != nil {
			return err
		}
	}
	fp, err := st.JoinFingerprint()
	if err != nil {
		return err
	}
	minted, code, err := st.CreateJoinInvitation(kind, *forName, allow, *ttl)
	if err != nil {
		return err
	}
	inv := joininvite.Invite{Kind: jkind, Addrs: addrs, FP: fp, Code: code, For: *forName}
	fmt.Fprintf(out, "roger join %s\n\n", inv)
	who := "a client"
	if *station {
		who = "a Station"
	}
	fmt.Fprintf(out, "paste that line on %s. It is valid for %d minutes and works once.\n", who, int(ttl.Minutes()))
	if loopbackOnly {
		fmt.Fprintf(out, "this Tower serves on loopback, so only this machine can use it: serve on a LAN address (--bind) to invite other machines.\n")
	}
	if *wait {
		fmt.Fprintf(out, "waiting for it to be used (Ctrl+C stops waiting; the invite still works)...\n")
		waitForJoin(*dir, minted.ID, out, time.Second, time.Now().Add(*ttl))
	}
	return nil
}

// inviteAddrs is where the Tower serves: its configured bind (or the default), with an
// all-interfaces bind expanded to this machine's home addresses.
func inviteAddrs(cfgPath string) (addrs []string, loopbackOnly bool, err error) {
	bind := localplane.DefaultBind
	if cfgPath != "" {
		c, err := loadConfig(cfgPath)
		if err != nil {
			return nil, false, err
		}
		if c.Standalone != nil && c.Standalone.Bind != "" {
			bind = c.Standalone.Bind
		}
	}
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		return nil, false, fmt.Errorf("the serve bind %q is not host:port", bind)
	}
	ip := net.ParseIP(host)
	if host != "" && (ip == nil || !ip.IsUnspecified()) {
		return []string{bind}, ip != nil && ip.IsLoopback(), nil
	}
	ifs, err := inviteInterfaceAddrs()
	if err != nil {
		return nil, false, err
	}
	for _, a := range ifs {
		if n, ok := a.(*net.IPNet); ok && netpriv.IsHome(n.IP) && !n.IP.IsLoopback() {
			addrs = append(addrs, net.JoinHostPort(n.IP.String(), port))
		}
	}
	if len(addrs) == 0 {
		return []string{net.JoinHostPort("127.0.0.1", port)}, true, nil
	}
	return addrs, false, nil
}

func inviteList(st toweradmin.Ops, out io.Writer) error {
	live := st.LiveInvitations()
	if len(live) == 0 {
		fmt.Fprintln(out, "no outstanding invites")
		return nil
	}
	for _, inv := range live {
		left := time.Until(time.Unix(0, inv.ExpiresAt)).Round(time.Minute)
		extra := ""
		if inv.For != "" {
			extra = " for " + inv.For
		}
		fmt.Fprintf(out, "%s  %s  %s left%s\n", inv.ID, inv.Kind, left, extra)
	}
	return nil
}
