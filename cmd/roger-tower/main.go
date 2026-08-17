// Command roger-tower is the self-hosted RogerAI relay.
//
// Two modes, chosen once per data directory and never changed in place:
//
//	standalone  a self-governed local network with its own trust root. No RogerAI
//	            discovery, settlement, or advertisement - structurally, not by setting.
//	joined      an untrusted child relay of the public RogerAI network. Roger Core
//	            stays the admission, routing, settlement and revocation authority.
//
// Phase 1 of docs/tower-network-plan.md shipped standalone first; the joined protocol is
// Phase 2. `serve` holds the link - session, heartbeat, clean drain - and, given `--hub`,
// hosts the SEALED data plane: consumers submit encrypted work, self-attached
// `roger share --tower` nodes poll for it, and the settle courier carries receipts to Core.
// The commands that still need something unbuilt say so plainly rather than pretending.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"rogerai.fm/roger/v5/internal/client"
	"rogerai.fm/roger/v5/internal/tower"
	"rogerai.fm/roger/v5/internal/towerjoin"
	"rogerai.fm/roger/v5/internal/towerstore"
)

const usage = `roger-tower - the self-hosted RogerAI relay

usage:
  roger-tower init --dir DIR --mode joined|standalone
  roger-tower config validate --config FILE
  roger-tower config print --config FILE [--redact]
  roger-tower doctor --config FILE
  roger-tower ready  --config FILE     (durable startup preflight)
  roger-tower invite --dir DIR --client KEYHASH [--ttl 15m] [--attempts 5]
  roger-tower admit  --dir DIR --client KEYHASH --id ID --code CODE
  roger-tower attach --dir DIR --station ID --key KEYHASH --models a,b
  roger-tower stations --dir DIR
  roger-tower route --dir DIR --client KEYHASH --model NAME
  roger-tower status --dir DIR
  roger-tower login  --dir DIR        (joined mode only)
  roger-tower logout --dir DIR
  roger-tower probe    --model NAME [--broker URL]   (drive the edge path as a consumer)
  roger-tower register --dir DIR      (joined mode only; requires login)
  roger-tower serve  --dir DIR [--hub :8444 --relay-public HOST:PORT]  (holds the link; hosts the sealed data plane)
  roger-tower station revoke   (joined mode; the kill switch for a station under this tower)
  roger-tower drain  --dir DIR        (stop taking new work; keep the link)
  roger-tower resume --dir DIR        (take work again)
  roger-tower revoke --dir DIR --yes  (retire this Tower for good)
  roger-tower version

invite, admit, attach, stations, route and serve also take --config FILE. Pass it
whenever the configuration selects durable storage: without it the command keeps state
in the data directory, which is the wrong answer for a node whose disk is not durable.

Standalone needs NO account: nothing leaves your machine. Joining the public network
needs one, because a joined Tower relays other people's traffic and must stay
accountable.

A data directory is initialized as ONE mode for life. To change mode, initialize a new
data directory: nothing is copied automatically, because an identity, trust root, or
Station registry must never cross that boundary.
`

// version is set at build time by the release pipeline.
var version = "dev"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "roger-tower:", err)
		os.Exit(1)
	}
}

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
	case "version":
		fmt.Fprintln(out, version)
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("--dir is required")
	}
	m, err := tower.ParseMode(*mode)
	if err != nil {
		return fmt.Errorf("--mode: %w", err)
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := loadConfig(*path)
	if err != nil {
		return err
	}
	rep := tower.Doctor(c)
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := loadConfig(*path)
	if err != nil {
		return err
	}
	rep := tower.Ready(c)
	fmt.Fprint(out, rep.String())
	if !rep.OK {
		return fmt.Errorf("not ready: %d dependency problem(s)", len(rep.Problems))
	}
	return nil
}

func cmdStatus(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "Tower data directory")
	if err := fs.Parse(args); err != nil {
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
		fmt.Fprint(out, "not registered yet - run `roger-tower register`\n")
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
func cmdInvite(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("invite", flag.ContinueOnError)
	fs.SetOutput(out)
	dir, cfg := dirAndConfig(fs)
	client := fs.String("client", "", "hash of the requesting client's public key")
	ttl := fs.Duration("ttl", 15*time.Minute, "how long the code stays valid")
	attempts := fs.Int("attempts", 5, "how many wrong guesses are allowed before lockout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, release, err := openDirWith(*dir, *cfg)
	if err != nil {
		return err
	}
	defer release()
	inv, code, err := st.CreateInvitation(*client, *ttl, *attempts)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "invitation: %s\n", inv.ID)
	fmt.Fprintf(out, "code: %s\n", code)
	fmt.Fprintf(out, "expires in %s, %d attempts\n", *ttl, *attempts)
	fmt.Fprintf(out, "\nThis code is shown once. It is not stored and cannot be printed again.\n")
	return nil
}

// cmdAdmit consumes a bootstrap code. Every failure reports the same thing, because a
// distinguishable error would tell an attacker which part they got right.
func cmdAdmit(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("admit", flag.ContinueOnError)
	fs.SetOutput(out)
	dir, cfg := dirAndConfig(fs)
	client := fs.String("client", "", "hash of the client's public key")
	id := fs.String("id", "", "invitation id")
	code := fs.String("code", "", "bootstrap code")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, release, err := openDirWith(*dir, *cfg)
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, release, err := openDirWith(*dir, *cfg)
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
	s, err := st.AttachStation(*station, *key, list)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "attached station %s on local network %s\n", s.ID, s.NetworkID)
	fmt.Fprintf(out, "models: %s\n", strings.Join(s.Models, ", "))
	return nil
}

func cmdStations(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("stations", flag.ContinueOnError)
	fs.SetOutput(out)
	dir, cfg := dirAndConfig(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, release, err := openDirWith(*dir, *cfg)
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
		fmt.Fprintf(out, "%s  models=%s  attached=%s\n",
			s.ID, strings.Join(s.Models, ","), time.Unix(s.AttachedAt, 0).Format(time.RFC3339))
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, release, err := openDirWith(*dir, *cfg)
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, release, err := openDir(*dir)
	if err != nil {
		return err
	}
	defer release()
	if st.Mode != tower.ModeJoined {
		return fmt.Errorf("this Tower is standalone and needs no account: nothing it does leaves this machine")
	}
	login, err := deviceLogin(envOr("ROGERAI_BROKER", "https://broker.rogerai.fm"))
	if err != nil {
		return err
	}
	if err := towerjoin.SaveAccount(st.Dir(), towerjoin.Account{Login: login}); err != nil {
		return err
	}
	fmt.Fprintf(out, "signed in as @%s\n", login)
	fmt.Fprintf(out, "next: roger-tower register --dir %s\n", *dir)
	return nil
}

// deviceLogin is the brokered sign-in, behind a seam so the rest of cmdLogin is testable
// without a network round trip.
var deviceLogin = client.DeviceLoginRun

func cmdLogout(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "Tower data directory")
	if err := fs.Parse(args); err != nil {
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
	if err := fs.Parse(args); err != nil {
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
func storeFor(c *tower.Config, st *tower.State) (*tower.State, func() error, error) {
	noop := func() error { return nil }
	if c == nil || c.Storage == nil || c.Storage.URLFile == "" {
		return st, noop, nil
	}
	raw, err := os.ReadFile(c.Storage.URLFile)
	if err != nil {
		return nil, noop, fmt.Errorf("cannot read the database URL file %s: %w", c.Storage.URLFile, err)
	}
	pg, err := towerstore.Open(strings.TrimSpace(string(raw)), nil)
	if err != nil {
		return nil, noop, err
	}
	return st.WithStore(pg), pg.Close, nil
}

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
	if dir == "" {
		return nil, nil, fmt.Errorf("--dir is required")
	}
	st, err := tower.Open(dir)
	if err != nil {
		return nil, nil, err
	}
	release, err := st.Lock()
	if err != nil {
		return nil, nil, err
	}
	if configPath == "" {
		return st, release, nil
	}
	c, err := loadConfig(configPath)
	if err != nil {
		_ = release()
		return nil, nil, err
	}
	stored, closeStore, err := storeFor(c, st)
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

func loadConfig(path string) (*tower.Config, error) {
	if path == "" {
		return nil, fmt.Errorf("--config is required")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return tower.ParseConfig(b)
}
