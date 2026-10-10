// Command roger-tower is the self-hosted RogerAI relay.
//
// Two modes, chosen once per data directory and never changed in place:
//
//	standalone  a self-governed local network with its own trust root. No RogerAI
//	            discovery, settlement, or advertisement - structurally, not by setting.
//	joined      an untrusted child relay of the public RogerAI network. Roger Core
//	            stays the admission, routing, settlement and revocation authority.
//
// Phase 1 of the Tower network plan (internal design note) shipped standalone first; the joined protocol is
// Phase 2. `serve` holds the link - session, heartbeat, clean drain - and, given `--hub`,
// hosts the SEALED data plane: consumers submit encrypted work, self-attached
// `roger share --tower` nodes poll for it, and the settle courier carries receipts to Core.
// The commands that still need something unbuilt say so plainly rather than pretending.
package towercli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"rogerai.fm/roger/v6/internal/buildinfo"
	"runtime"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/clockprobe"
	"rogerai.fm/roger/v6/internal/tower"
	"rogerai.fm/roger/v6/internal/toweradmin"
	"rogerai.fm/roger/v6/internal/towerjoin"
	"rogerai.fm/roger/v6/internal/towerpin"
	"rogerai.fm/roger/v6/internal/towerstore"
)

const usage = `roger tower - run a self-hosted RogerAI relay (a Tower)

usage: roger tower <command> [flags]

  init      create a Tower data directory: --dir DIR --mode joined|standalone
  config    validate or print an operator configuration: validate|print --config FILE [--redact]
  doctor    a Tower's readiness and clock (NTP): --config FILE [--clock-check] [--offline] [--ntp HOST:PORT]
  ready     durable startup preflight: --config FILE
  status    a Tower data directory's state and fingerprint: --dir DIR [--full]
  revoke-client  stop one client using this Tower, by the name it joined under: --dir DIR NAME
  rotate-key  a new TLS key: the Tower serves under a new fingerprint and every client re-joins: --dir DIR
  requests  the join requests waiting for this serving Tower, each with its matching phrase: --dir DIR
  approve   let a waiting request join (only if the device shows the same phrase): --dir DIR ID
  refuse    turn a waiting request down: --dir DIR ID
  invite    print one roger join line for a client (or --station): --dir DIR [--for NAME] [--ttl 15m] [--list] [--void ID]
  admit     admit an invited client: --dir DIR --client KEYHASH --id ID --code CODE
  attach    attach a Station: --dir DIR --station ID --key KEYHASH --models a,b
  stations  list attached Stations: --dir DIR
  route     where a client's request would go: --dir DIR --client KEYHASH --model NAME
  serve     serve the directory as its mode says: --dir DIR
              standalone: the consumer plane [--bind HOST:PORT --allow-public --advertise --label NAME --check]
              joined: the link and the sealed hub [--hub :8444 --relay-public HOST:PORT --hub-tls]
  login     mark a Tower directory as signed in (roger login plus the directory marker; joined only): --dir DIR
  logout    remove the directory's sign-in marker (roger stays signed in): --dir DIR
  register  submit a joined Tower for admission (after login): --dir DIR
  earnings  what this account has earned
  probe     drive the edge path as a consumer: --model NAME [--broker URL]
  station   station revoke: the kill switch for a Station under a joined Tower
  drain     stop taking new work, keep the link: --dir DIR
  resume    lift a drain on a joined Tower: --dir DIR
  revoke    retire this Tower for good: --dir DIR --yes
  version   print the version (also --version, -v)

invite, admit, attach, stations, route and serve also take --config FILE. Pass it
whenever the configuration selects durable storage: without it the command keeps state
in the data directory, which is the wrong answer for a node whose disk is not durable.

Standalone needs NO account: nothing leaves your machine. Joining the public network
needs one, because a joined Tower relays other people's traffic and must stay
accountable. Joined Towers run on Linux; standalone runs everywhere roger does.

A data directory is initialized as ONE mode for life. To change mode, initialize a new
data directory: nothing is copied automatically, because an identity, trust root, or
Station registry must never cross that boundary.
`

// Run is `roger tower ...`: it runs one tower command and returns the process exit code. An
// error is printed to stderr as "roger tower: <error>"; a refusal that carries its own code
// (exitError) exits with it, every other failure exits 1.
func Run(args []string, stdout, stderr io.Writer) int {
	err := run(args, stdout)
	if err == nil || errors.Is(err, errHelpShown) {
		return 0
	}
	fmt.Fprintln(stderr, "error:", err) // roger's own style, so a script reads every command alike
	var ee exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return 1
}

// Platform is the operating system `roger tower` runs on (runtime.GOOS; a test that runs roger
// as a subprocess sets it to exercise another platform's refusals).
var Platform = runtime.GOOS

// parseFlags parses a tower command's flags in roger's house style: a bad flag is a usage
// error (exit 2) naming the flag and the command's usage line; -h prints the command's flags.
func parseFlags(fs *flag.FlagSet, args []string, out io.Writer) error {
	fs.SetOutput(io.Discard)
	err := fs.Parse(args)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprintf(out, "usage: %s\n", commandUsage(fs.Name()))
		fs.SetOutput(out)
		fs.PrintDefaults()
		return errHelpShown
	}
	return exitError{2, fmt.Sprintf("%v\nusage: %s", err, commandUsage(fs.Name()))}
}

// errHelpShown stops a command whose -h already printed its help; Run exits 0 for it.
var errHelpShown = errors.New("help shown")

// commandUsage is one command's line of the tower usage table: "roger tower <cmd> <flags>".
func commandUsage(name string) string {
	first, _, _ := strings.Cut(name, " ")
	for _, line := range strings.Split(usage, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != first {
			continue
		}
		if _, flags, ok := strings.Cut(line, ": "); ok {
			return "roger tower " + name + " " + strings.TrimSpace(flags)
		}
		return "roger tower " + name
	}
	return "roger tower " + name + " (see roger tower help)"
}

// exitError is a refusal with a specific exit code (2: a usage mistake caught before anything
// is opened).
type exitError struct {
	code int
	msg  string
}

func (e exitError) Error() string { return e.msg }

// run is the whole CLI, taking its args and output so it is testable without a process.
func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(out, usage)
		return nil
	}
	switch args[0] {
	case "init":
		return cmdInit(args[1:], out)
	case "config":
		return cmdConfig(args[1:], out)
	case "doctor":
		return cmdDoctor(args[1:], out)
	case "ready":
		return cmdReady(args[1:], out)
	case "invite":
		return cmdInvite(args[1:], out)
	case "admit":
		return cmdAdmit(args[1:], out)
	case "attach":
		return cmdAttach(args[1:], out)
	case "stations":
		return cmdStations(args[1:], out)
	case "route":
		return cmdRoute(args[1:], out)
	case "login":
		return cmdLogin(args[1:], out)
	case "logout":
		return cmdLogout(args[1:], out)
	case "register":
		return cmdRegister(args[1:], out)
	case "probe":
		return cmdProbe(args[1:], out)
	case "status":
		return cmdStatus(args[1:], out)
	case "rotate-key":
		return cmdRotateKey(args[1:], out)
	case "revoke-client":
		return cmdRevokeClient(args[1:], out)
	case "requests":
		return cmdRequests(args[1:], out)
	case "approve", "refuse":
		return cmdDecide(args[0], args[1:], out)
	case "earnings":
		return cmdEarnings(args[1:], out)
	case "version", "--version", "-v":
		fmt.Fprintln(out, buildinfo.Version)
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
		return nil
	case "serve":
		return cmdServe(args[1:], out)
	case "station":
		return cmdStation(args[1:], out)
	case "drain":
		return cmdDrain(args[1:], out)
	case "resume":
		return cmdResume(args[1:], out)
	case "revoke":
		return cmdRevoke(args[1:], out)
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
	}
}

func cmdInit(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "Tower data directory (must be empty)")
	mode := fs.String("mode", "", "joined or standalone")
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("--dir is required")
	}
	m, err := tower.ParseMode(*mode)
	if err != nil {
		return fmt.Errorf("--mode: %w", err)
	}
	// Joined Towers were proven only on Linux (the hub, the systemd unit, the non-root runtime);
	// standalone is plain Go and runs everywhere roger does.
	if m == tower.ModeJoined && Platform != "linux" {
		return fmt.Errorf("joined Towers run on Linux; on %s, initialize a standalone Tower (--mode standalone)", Platform)
	}
	st, err := tower.Init(*dir, m)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "initialized %s Tower in %s\n", st.Mode, *dir)
	fmt.Fprintf(out, "tower id: %s\n", st.TowerID)
	if st.LocalNetworkID != "" {
		fmt.Fprintf(out, "local network: %s (separate from the public RogerAI network)\n", st.LocalNetworkID)
	}
	return nil
}

func cmdConfig(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("config needs a subcommand: validate or print")
	}
	sub := args[0]
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("config", "", "path to the Tower configuration file")
	redact := fs.Bool("redact", true, "never read or print secret file contents")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	c, err := loadConfig(*path)
	if err != nil {
		return err
	}
	switch sub {
	case "validate":
		fmt.Fprintf(out, "configuration is valid for %s mode\n", c.Mode)
		return nil
	case "print":
		if !*redact {
			// There is no unredacted print. A flag that could dump key material is a
			// flag someone will eventually run in a shared terminal.
			return fmt.Errorf("--redact=false is not supported: configuration is always printed secret-safe")
		}
		fmt.Fprint(out, c.PrintRedacted())
		return nil
	default:
		return fmt.Errorf("unknown config subcommand %q", sub)
	}
}

func cmdDoctor(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("config", "", "path to the Tower configuration file")
	// A Tower needs no GPU and runs no model. Its requirements are a stable address, an
	// exposed port, bandwidth and a SYNCHRONISED CLOCK - and the clock is the one an
	// operator has no other way to find out is wrong, because a Tower past the signature
	// window refuses every honest node with a 401 that says nothing about time.
	//
	// Measuring it needs an external reference, which means one UDP packet to an NTP
	// server, and WHETHER THAT IS ALLOWED DEPENDS ON THE MODE. A standalone Tower's whole
	// promise is that it makes no outbound connection - that is a Phase 1 gate, not a
	// setting - so doctor does not quietly break it to check a clock: standalone measures
	// nothing unless the operator asks with --clock-check, and says in the report that it
	// did not. A joined Tower already talks to Roger Core, so it measures by default and
	// --offline opts out.
	clockCheck := fs.Bool("clock-check", false, "measure this machine's clock against an NTP server even in standalone mode (standalone otherwise makes no outbound connection at all)")
	offline := fs.Bool("offline", false, "skip the network clock check entirely; the kernel's own time-sync state is still reported")
	ntpServer := fs.String("ntp", clockprobe.DefaultServer, "NTP server to measure this machine's clock against")
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	c, err := loadConfig(*path)
	if err != nil {
		return err
	}
	var opts []tower.DoctorOption
	if measure := !*offline && (c.Mode != tower.ModeStandalone || *clockCheck); measure {
		// Two seconds: doctor is interactive, and an unreachable NTP server is an ordinary
		// condition in a hardened network rather than something to hang on. A timeout is
		// reported as "not determined", never as a clock fault.
		opts = append(opts, tower.WithClockSource(clockprobe.NTP(*ntpServer, 2*time.Second)))
	} else if !*offline {
		opts = append(opts, tower.WithClockSourceRefused(
			"this is a standalone Tower, which makes no outbound connection by design, so its clock "+
				"was not measured against anything. Run `roger tower doctor --clock-check` to allow one NTP query"))
	}
	rep := tower.Doctor(c, opts...)
	fmt.Fprint(out, rep.String())
	if !rep.OK {
		return fmt.Errorf("doctor found %d problem(s)", len(rep.Problems))
	}
	return nil
}

// cmdReady is the durable-startup preflight. It exits non-zero when the Tower must not
// serve, so it drops straight into a systemd ExecStartPre or a container readiness probe:
// a Tower that cannot keep its state should refuse rather than serve and lose it.
func cmdReady(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("ready", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("config", "", "path to the Tower configuration file")
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	c, err := loadConfig(*path)
	if err != nil {
		return err
	}
	rep := tower.Ready(c)
	fmt.Fprint(out, rep.String())
	if !rep.OK {
		return fmt.Errorf("not ready: %d dependency problem(s); `roger tower doctor --config %s` checks each with its repair", len(rep.Problems), *path)
	}
	return nil
}

func cmdStatus(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "Tower data directory")
	full := fs.Bool("full", false, "show the whole fingerprint, not its short form")
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("--dir is required")
	}
	st, err := tower.Open(*dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "mode: %s\n", st.Mode)
	fmt.Fprintf(out, "tower id: %s\n", st.TowerID)
	if st.LocalNetworkID != "" {
		fmt.Fprintf(out, "local network: %s\n", st.LocalNetworkID)
		if fp, err := st.JoinFingerprint(); err == nil {
			shown := towerpin.Short(fp)
			if *full {
				shown = "sha256:" + fp
			}
			fmt.Fprintf(out, "fingerprint: %s\n", shown)
		}
		return nil
	}
	fmt.Fprintf(out, "network: RogerAI public\n")
	return printCoreStatus(st, out)
}

// printCoreStatus reports what ROGER CORE believes about this Tower.
//
// The local files answer a different and much weaker question. They record what this Tower
// was TOLD at enrollment, and go stale the instant an administrator promotes, suspends or
// revokes it, or a lease lapses - none of which the Tower is notified about. An operator
// asking "why is nothing happening" needs Core's answer, and until now no binary could get
// it: /tower/status existed and nothing called it.
//
// A Tower that has not registered, or a Core that cannot be reached, is REPORTED AND NOT
// FATAL. The local half above is still worth having, and `status` failing outright because
// the network is down is the opposite of useful at the moment somebody runs it.
func printCoreStatus(st *tower.State, out io.Writer) error {
	adm, ok := towerjoin.LoadAdmission(st.Dir())
	if !ok {
		fmt.Fprint(out, "not registered yet - run `roger tower register`\n")
		return nil
	}
	towers, err := towerjoin.FetchStatus(st)
	if err != nil {
		fmt.Fprintf(out, "could not ask RogerAI for this Tower's state: %v\n", err)
		return nil
	}
	for _, tw := range towers {
		// An account may hold several Towers. Only this data directory's is being asked
		// about, and printing the others would invite acting on the wrong one.
		//
		// Matched on the ADMISSION id, not st.TowerID: those are two different identifiers.
		// st.TowerID is the local identity minted by `init` before this Tower had ever heard
		// of Roger Core, and Core's id is allocated at enrollment. Comparing them silently
		// matched nothing and printed an empty report - which reads exactly like a Tower Core
		// has never seen.
		if adm.TowerID != "" && tw.TowerID != adm.TowerID {
			continue
		}
		fmt.Fprint(out, "\nRoger Core says:\n")
		fmt.Fprintf(out, "  state:         %s\n", tw.State)
		fmt.Fprintf(out, "  may take work: %t\n", tw.MayTakeWork)
		fmt.Fprintf(out, "  link live:     %t\n", tw.LinkLive)
		if tw.InventoryRevision > 0 {
			fmt.Fprintf(out, "  inventory:     revision %d\n", tw.InventoryRevision)
		}
		if len(tw.Routable) == 0 {
			fmt.Fprint(out, "  routable:      none\n")
		}
		for _, s := range tw.Routable {
			fmt.Fprintf(out, "  routable:      %s %s (%s, capacity %d)\n",
				s.StationID, s.Model, s.Modality, s.Capacity)
		}
		if tw.State == "quarantine" {
			// The most common state, and the most commonly misread. Nothing is broken.
			fmt.Fprint(out, "\nQuarantine is the state a Tower is admitted INTO. It is not a fault:\n"+
				"eligibility is a separate decision from admission, and Roger Core makes it.\n")
		}
		if !tw.CarriesTraffic && tw.Note != "" {
			// Core saying routing is not shipped answers "everything looks right and nothing
			// is happening", which is otherwise an unanswerable question.
			fmt.Fprintf(out, "\nnote: %s\n", tw.Note)
		}
	}
	return nil
}

// cmdInvite mints the one-time bootstrap code that turns the first local client into
// this network's operator. The plaintext is printed ONCE, here, and never again: it is
// not stored, not logged, and not retrievable from the invitation record.
// cmdAdmit consumes a bootstrap code. Every failure reports the same thing, because a
// distinguishable error would tell an attacker which part they got right.
func cmdAdmit(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("admit", flag.ContinueOnError)
	fs.SetOutput(out)
	dir, cfg := dirAndConfig(fs)
	client := fs.String("client", "", "hash of the client's public key")
	id := fs.String("id", "", "invitation id")
	code := fs.String("code", "", "bootstrap code")
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	st, release, err := openOps(*dir, *cfg)
	if err != nil {
		return err
	}
	defer release()
	cred, err := st.ConsumeInvitation(*id, *code, *client)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "admitted as %s\n", cred.Role)
	fmt.Fprintf(out, "network: %s\n", cred.NetworkID)
	fmt.Fprintf(out, "pinned offline-root fingerprint: %s\n", cred.RootFingerprint)
	fmt.Fprintf(out, "admit goes away next release; the new way: roger tower invite, then roger join\n")
	return nil
}

// cmdAttach admits a local Station. Standalone routes only to Stations it admitted.
func cmdAttach(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fs.SetOutput(out)
	dir, cfg := dirAndConfig(fs)
	station := fs.String("station", "", "Station id")
	key := fs.String("key", "", "hash of the Station's public key")
	models := fs.String("models", "", "comma-separated models this Station serves")
	curated := fs.String("curated", "", "attach as a CURATED proxy of the named commercial provider (e.g. openrouter): the label rides discovery and receipts so a proxy never reads as local hardware; the local plane stays free either way")
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	st, release, err := openOps(*dir, *cfg)
	if err != nil {
		return err
	}
	defer release()
	var list []string
	for _, m := range strings.Split(*models, ",") {
		if m = strings.TrimSpace(m); m != "" {
			list = append(list, m)
		}
	}
	var s tower.Station
	var err2 error
	// Any non-empty --curated value routes to the curated attach, whitespace included:
	// AttachCuratedStation sanitizes and refuses an empty provider, which is the right
	// answer for a blank flag - silently attaching as HUMAN would be the mislabel.
	if *curated != "" {
		s, err2 = st.AttachCuratedStation(*station, *key, list, *curated)
	} else {
		s, err2 = st.AttachStation(*station, *key, list)
	}
	if err2 != nil {
		return err2
	}
	fmt.Fprintf(out, "attached station %s on local network %s\n", s.ID, s.NetworkID)
	fmt.Fprintf(out, "models: %s\n", strings.Join(s.Models, ", "))
	if s.Curated {
		fmt.Fprintf(out, "curated via %s - a labeled pass-through; the local plane stays free\n", s.CuratedProvider)
	}
	fmt.Fprintf(out, "attach by key goes away next release; the new way: roger tower invite --station\n")
	return nil
}

func cmdStations(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("stations", flag.ContinueOnError)
	fs.SetOutput(out)
	dir, cfg := dirAndConfig(fs)
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	// Read-only: a running `serve` holds the exclusive lock, and listing stations must not
	// be blocked by it. openDirReadOnly opens without the lock; the snapshot's atomic writes
	// make the unlocked read consistent.
	st, release, err := openDirReadOnly(*dir, *cfg)
	if err != nil {
		return err
	}
	defer release()
	list, err := st.Stations()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(out, "no stations attached")
		return nil
	}
	for _, s := range list {
		label := ""
		if s.Curated {
			label = "  curated via " + s.CuratedProvider
		}
		fmt.Fprintf(out, "%s  models=%s  attached=%s%s\n",
			s.ID, strings.Join(s.Models, ","), time.Unix(s.AttachedAt, 0).Format(time.RFC3339), label)
	}
	return nil
}

// cmdRoute picks a Station for a model and prints the LOCAL receipt. The wording is
// part of the contract: it names the local network and claims nothing about RogerAI.
func cmdRoute(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("route", flag.ContinueOnError)
	fs.SetOutput(out)
	dir, cfg := dirAndConfig(fs)
	client := fs.String("client", "", "hash of the admitted client's public key")
	model := fs.String("model", "", "model to route")
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	// Read-only: Route computes a receipt and persists nothing locally. A standalone client
	// is routed WHILE serve holds the lock, so it must not take the exclusive lock.
	st, release, err := openDirReadOnly(*dir, *cfg)
	if err != nil {
		return err
	}
	defer release()
	rec, err := st.Route(*client, *model)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, rec.String())
	return nil
}

// cmdLogin signs the operator in through RogerAI.
//
// The broker-mediated flow means this binary reaches only our broker - no provider
// endpoint, no client id compiled in - and the operator picks whichever sign-in their
// account supports on our page. That is why roger-tower can have a login at all: the
// provider-direct flow would have needed a client id this binary does not carry.
func cmdLogin(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "Tower data directory")
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	st, release, err := openDir(*dir)
	if err != nil {
		return err
	}
	defer release()
	if st.Mode != tower.ModeJoined {
		return fmt.Errorf("a standalone Tower has no RogerAI account by design: nothing it does leaves this machine")
	}
	// `roger tower login` is `roger login` plus this directory's marker: a machine already
	// signed in to roger reuses that account, with no second flow and no second credential.
	login, ok := signedIn()
	if !ok {
		// ROGER_BROKER is what register and probe read; ROGERAI_BROKER was this command's own
		// spelling. Honour both, the shared one first, so one variable points every command at
		// the same Core - the first person to set one and not the other enrolls with prod.
		login, err = deviceLogin(envOr("ROGER_BROKER", envOr("ROGERAI_BROKER", "https://broker.rogerai.fm")))
		if err != nil {
			return err
		}
	}
	if err := towerjoin.SaveAccount(st.Dir(), towerjoin.Account{Login: login}); err != nil {
		return err
	}
	fmt.Fprintf(out, "signed in as @%s\n", login)
	fmt.Fprintf(out, "next: roger tower register --dir %s\n", *dir)
	return nil
}

// deviceLogin is the brokered sign-in, behind a seam so the rest of cmdLogin is testable
// without a network round trip.
var deviceLogin = client.DeviceLoginRun

// signedIn is roger's own sign-in record (a seam, like deviceLogin).
var signedIn = client.SignedIn

func cmdLogout(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "Tower data directory")
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	st, release, err := openDir(*dir)
	if err != nil {
		return err
	}
	defer release()
	if err := towerjoin.SignOut(st.Dir()); err != nil {
		return err
	}
	fmt.Fprintln(out, "signed out; this Tower's identity and data directory are untouched")
	return nil
}

// cmdRegister submits the Tower for admission. Both refusals - wrong mode, not signed in
// - happen before any network call.
func cmdRegister(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "Tower data directory")
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	st, release, err := openDir(*dir)
	if err != nil {
		return err
	}
	defer release()
	acct, _ := towerjoin.LoadAccount(st.Dir())
	return towerjoin.Register(st, acct)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// openDir opens a Tower data directory AND takes its lock.
//
// The lock is what makes "exactly one local operator for the life of the network" true
// across processes: without it two concurrent `admit` runs both read "no operator yet",
// both write a credential, and both are admitted. The in-process mutex cannot see the
// other process.
// storeFor returns the persistence a config asks for. The database store lives in its
// own package so this binary's standalone core never links a driver - see the no-egress
// gate in internal/tower.

func openDir(dir string) (*tower.State, func() error, error) { return openDirWith(dir, "") }

// openDirWith opens a data directory and, when a config asks for durable storage, opens that
// too. Every command that touches local-admission state goes through here.
//
// THIS WIRING WAS MISSING. storeFor existed, was tested, and was called by nothing: a Tower
// configured with the durable storage profile silently kept its state on local disk, which is
// the exact deployment the profile exists for - one whose disk is not durable. Nothing failed;
// the operator got a Tower that looked configured and would lose its operator credential, its
// verifier secret and its Station registry on the first replacement of the node. A reachability
// pass over the binary found it.
//
// It fails CLOSED: if the config asks for a database and the database cannot be opened, the
// command stops. Falling back to the file store is what caused the problem in the first place.
func openDirWith(dir, configPath string) (*tower.State, func() error, error) {
	return openDirMode(dir, configPath, true)
}

// openOps opens a data directory for an administration command. With no serve running it takes
// the directory lock as before; while a serve holds the lock, the command goes to that running
// Tower through its owner-only admin socket (features/join/admin_socket.feature).
func openOps(dir, configPath string) (toweradmin.Ops, func() error, error) {
	st, release, err := openDirWith(dir, configPath)
	if err == nil {
		return st, release, nil
	}
	if errors.Is(err, tower.ErrLocked) && toweradmin.Running(dir) {
		return toweradmin.Client{Dir: dir}, func() error { return nil }, nil
	}
	return nil, nil, err
}

// openDirReadOnly opens a data directory for a command that only READS state, WITHOUT
// taking the exclusive lock. The lock exists to stop two writers from corrupting one
// identity's session and registry; a reader threatens neither, and taking the exclusive
// lock would lock a read-only command out of a directory a `serve` is actively holding -
// exactly when the operator most wants to look. Reads are safe unlocked: the snapshot
// store writes via a temp-file rename (store.go), so a concurrent read sees the whole old
// file or the whole new one, never a torn one.
func openDirReadOnly(dir, configPath string) (*tower.State, func() error, error) {
	return openDirMode(dir, configPath, false)
}

// openDirMode is the shared open path; lock decides whether it takes exclusive ownership.
func openDirMode(dir, configPath string, lock bool) (*tower.State, func() error, error) {
	if dir == "" {
		return nil, nil, fmt.Errorf("--dir is required")
	}
	st, err := tower.Open(dir)
	if err != nil {
		return nil, nil, err
	}
	release := func() error { return nil }
	if lock {
		release, err = st.Lock()
		if err != nil {
			return nil, nil, err
		}
	}
	if configPath == "" {
		return st, release, nil
	}
	c, err := loadConfig(configPath)
	if err != nil {
		_ = release()
		return nil, nil, err
	}
	stored, closeStore, err := towerstore.ForConfig(c, st)
	if err != nil {
		_ = release()
		return nil, nil, err
	}
	// Release in the reverse order of acquisition, and report the FIRST failure: a database
	// that will not close cleanly matters more than the lock file, and swallowing it would
	// hide a half-written snapshot.
	return stored, func() error {
		cerr := closeStore()
		rerr := release()
		if cerr != nil {
			return cerr
		}
		return rerr
	}, nil
}

// dirAndConfig registers the two flags every state-touching command shares.
func dirAndConfig(fs *flag.FlagSet) (*string, *string) {
	dir := fs.String("dir", "", "Tower data directory")
	cfg := fs.String("config", "", "Tower configuration file (required for durable storage)")
	return dir, cfg
}

// loadConfig is tower.LoadConfig: one loader for every tower command and standalone serve.
func loadConfig(path string) (*tower.Config, error) { return tower.LoadConfig(path) }
