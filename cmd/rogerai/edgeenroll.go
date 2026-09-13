package main

// ENROLLMENT: the machine you are already logged in on joins itself.
//
// Contract: features/edge/enrollment.feature.
//
// Until this existed the [3] EDGE screen was honestly still, because nothing had an
// identity to advertise. A node generates its own keypair, signs the ask with the user
// key login already left on disk, and gets back two things: a certificate naming this
// account and this node, and the account's PUBLIC Edge root. From then on the Edge
// works with no internet at all - discovery, verification and revocation all run
// against the root the machine already holds.
//
// WHO SIGNS IS THE OWNER'S CHOICE. Core by default (zero setup, one online moment per
// node), or a machine the owner designates as a LOCAL authority, which never contacts
// Core even once. This file does not branch on which: it resolves an ENDPOINT and one
// path runs. That is the same-ness the spec demands - a node cannot tell how its peer
// was signed, because there is only one way to be signed.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/edgeauth/enrollhttp"
	"rogerai.fm/roger/v6/internal/store"
)

// edgeAuthDir is where this machine keeps its Edge identity - and, on the one machine
// per Edge that is a designated authority, the root it generated. Beside config.json,
// owner-readable only.
func edgeAuthDir() string { return filepath.Join(filepath.Dir(configPath()), "edge") }

// edgeUserKey is the signing identity login left on this machine. It is a seam only
// because internal/client caches the key per PROCESS, so a test standing up a second
// machine cannot get a second key any other way; production is the real key.
var defaultEdgeUserKey = client.LoadOrCreateUserKey

var edgeUserKey = defaultEdgeUserKey

// edgeIdentityStore is this machine's Edge directory.
func edgeIdentityStore() edgeauth.Store { return edgeauth.Store{Dir: edgeAuthDir()} }

// edgeDescriptor is what roots this Edge, as this machine understands it.
func edgeDescriptor() edgeauth.Descriptor {
	d, _, err := edgeIdentityStore().Descriptor()
	if err != nil {
		return edgeauth.Descriptor{Kind: edgeauth.KindCore}
	}
	return d
}

// --- enroll ---------------------------------------------------------------

func cmdEdgeEnroll(cfg config, args []string) error {
	leaf, _ := edgeLeaf("enroll")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	name := ""
	if len(argv.pos) > 0 {
		name = argv.pos[0]
	}
	endpoint := argv.flags["authority"]
	account := client.LinkedLogin()

	// A login is what ties this machine to an account. The one case where it is not
	// needed is the owner naming an authority themselves: on a network with no Core
	// there is nothing to log in to, and the authority decides who may join.
	if account == "" && endpoint == "" {
		return usagef("this machine is not logged in, so there is no account to enroll it into.\n" +
			"  log in first:                 roger login\n" +
			"  or, with no internet at all:  roger edge authority local <name>, then " +
			"roger edge enroll <name> --authority <address>")
	}
	if want, ok := argv.flags["account"]; ok && want != account {
		// Never repeated back, and nothing is sent: a refusal that names the other
		// account turns "does this account exist?" into a question anyone can ask.
		return fmt.Errorf("this machine is logged in as %s, and can only enroll into its own Edge", account)
	}
	if name == "" {
		name = edgeDefaultName()
	}
	if err := edge.ValidName(name); err != nil {
		return usagef("%v", err)
	}

	st := edgeIdentityStore()
	held, heldKey, enrolled, err := st.LoadIdentity()
	if err != nil {
		return err
	}
	now := time.Now()

	// ONE EDGE, ONE AUTHORITY. When the owner names an authority and this machine
	// already has a root, ask that authority whose root it is BEFORE asking it for
	// anything. A second root would mean a peer that verifies for half the fleet, and
	// the refusal is worth more than the certificate it would otherwise mint first.
	if endpoint != "" {
		if err := edgeRefuseASecondAuthority(st, endpoint); err != nil {
			return err
		}
	}
	if enrolled && !held.NeedsRenewal(now) {
		fmt.Printf("already enrolled as %s (%s), on the Edge rooted at %s.\n",
			edgeNameOf(held.NodeID, name), edgeShortID(held.NodeID), edgeDescriptor().Names())
		fmt.Printf("  its certificate is good until %s; nothing to do.\n",
			held.NotAfter().UTC().Format(time.RFC3339))
		return nil
	}

	// A renewal keeps the key, and therefore the node id, and therefore its place, its
	// name and its history. A first enrollment generates one, here, and it never leaves.
	pub, priv := ed25519.PublicKey(nil), heldKey
	if enrolled {
		pub = heldKey.Public().(ed25519.PublicKey)
	} else {
		pub, priv, err = ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
	}

	local, isAuthority, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil {
		return err
	}
	// WHICH ACCOUNT. Against Core, this machine's login says, and a request naming any
	// other account is refused. Against an authority the owner NAMED - a machine in a
	// shed on a network with no Core - there is no login to speak for: the authority
	// owns its Edge's account and says which it is. Claiming one here would refuse
	// every airgap enrollment for disagreeing with a login that means nothing there.
	claim := account
	if endpoint != "" || isAuthority {
		claim = ""
	}
	req := edgeauth.NewRequest(claim, name, string(edge.Host), pub, now)
	req.Sign(edgeUserKey())

	var resp edgeauth.Response
	switch {
	case endpoint == "" && isAuthority:
		// This machine IS the authority. Nothing crosses a wire at all.
		resp, err = local.Issue(req)
	case endpoint == "":
		resp, err = enrollhttp.Enroll(context.Background(), cfg.Broker, req)
		if err != nil {
			return edgeUnreachableAuthority(err)
		}
	default:
		resp, err = enrollhttp.Enroll(context.Background(), endpoint, req)
	}
	if err != nil {
		return err
	}

	want := edgeauth.Expect{NodeID: req.NodeID, Account: claim, Key: pub, Now: now}
	if enrolled {
		want.Root = held.Root
	} else if root := edgeHeldRoot(st); root != nil {
		want.Root = root
	}
	id, err := edgeauth.CheckResponse(resp, want)
	if errors.Is(err, edgeauth.ErrDifferentAuthority) {
		return fmt.Errorf("this Edge is rooted at %s, and that certificate comes from somewhere else.\n"+
			"  an Edge has exactly one authority; moving it is a deliberate migration:\n"+
			"    roger edge authority local <name> --force", edgeDescriptor().Names())
	}
	if err != nil {
		return err
	}

	// Only now is anything written. Everything above is checks; nothing below can leave
	// half an identity behind, because there was nothing to half-write until here.
	if err := st.SaveAccount(id.Account); err != nil {
		return err
	}
	if err := st.SaveIdentity(id, priv, edgeAuthorityDescriptor(endpoint, local, isAuthority)); err != nil {
		return err
	}
	if err := edgeRefreshTrust(st, endpoint, local, isAuthority, now); err != nil {
		return err
	}
	if err := edgeRecordSelf(id, pub, name, now); err != nil {
		return err
	}
	fmt.Printf("enrolled %q as %s on the Edge rooted at %s.\n", name, edgeShortID(id.NodeID),
		edgeDescriptor().Names())
	fmt.Printf("  this machine now advertises itself, so your other machines can see it.\n")
	return nil
}

// edgeRefuseASecondAuthority refuses an authority that is not the one rooting this Edge.
func edgeRefuseASecondAuthority(st edgeauth.Store, endpoint string) error {
	mine := edgeHeldRoot(st)
	if mine == nil {
		return nil // no root yet: whichever authority the owner named becomes this Edge's
	}
	theirPEM, err := enrollhttp.Root(context.Background(), endpoint)
	if err != nil {
		return nil // it cannot say; CheckResponse still refuses a root that is not ours
	}
	theirs, err := edgeauth.DecodeCert(theirPEM)
	if err != nil || mine.Equal(theirs) {
		return nil
	}
	return fmt.Errorf("this Edge is rooted at %s, and %s is a different authority.\n"+
		"  an Edge has exactly one root: a second one would mean a peer that verifies for\n"+
		"  half your fleet and not the other half. Nothing was asked for and no root was created.\n"+
		"  to move this Edge deliberately, so every member is told to re-enroll:\n"+
		"    roger edge authority local <name> --force", edgeDescriptor().Names(),
		edgeAuthorityLabel(endpoint))
}

// edgeUnreachableAuthority is the message an owner gets when Core is the authority and
// the network is not there. It names connectivity as the reason - and it names the way
// to have an Edge with no internet at all, because that is the actual answer to the
// situation they are in.
func edgeUnreachableAuthority(err error) error {
	return fmt.Errorf("could not reach Core to enroll this machine: %v\n"+
		"  Core issues this Edge's certificates, so enrolling a new node needs the network.\n"+
		"  to enroll with no internet at all, designate a local Edge authority:\n"+
		"    roger edge authority local <name>\n"+
		"  nothing was enrolled, and this machine is exactly as it was", err)
}

// edgeHeldRoot is the root this machine already trusts, if any. It is what makes a
// second authority a refusal rather than a silent switch.
func edgeHeldRoot(st edgeauth.Store) *x509.Certificate {
	auth, _, err := st.Trust(time.Now())
	if err != nil {
		return nil
	}
	return auth.Root()
}

// edgeAuthorityDescriptor records who rooted this machine. An endpoint the owner named
// is a machine they designated; no endpoint is Core.
func edgeAuthorityDescriptor(endpoint string, local *edgeauth.Local, isAuthority bool) edgeauth.Descriptor {
	if isAuthority && endpoint == "" {
		return local.Descriptor()
	}
	if endpoint == "" {
		return edgeauth.Descriptor{Kind: edgeauth.KindCore}
	}
	if isAuthority {
		return local.Descriptor()
	}
	return edgeauth.Descriptor{Kind: edgeauth.KindLocal, Where: edgeAuthorityLabel(endpoint)}
}

// edgeAuthorityLabel names a remote authority by where it answers - the only true thing
// this machine knows about it.
func edgeAuthorityLabel(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		return u.Host
	}
	return endpoint
}

// edgeRefreshTrust brings this machine's revocation list up to date and stamps WHEN. A
// list with no timestamp cannot be called stale later, and a list that cannot be called
// stale is one that quietly claims a revoked node is fine.
func edgeRefreshTrust(st edgeauth.Store, endpoint string, local *edgeauth.Local, isAuthority bool, now time.Time) error {
	if isAuthority && endpoint == "" {
		return st.SaveTrust(local.Revocations(), now)
	}
	if endpoint != "" {
		if rev, err := enrollhttp.Revocations(context.Background(), endpoint); err == nil {
			return st.SaveTrust(rev, now)
		}
	}
	t, err := st.LoadTrust()
	if err != nil {
		return err
	}
	return st.SaveTrust(t.Revoked, now)
}

// edgeRecordSelf puts this machine on its own fleet, or leaves the row it already has
// exactly where it is. A renewal is not a new member.
func edgeRecordSelf(id *edgeauth.Identity, pub ed25519.PublicKey, name string, now time.Time) error {
	state, err := loadEdgeState()
	if err != nil {
		return err
	}
	fp := edge.FingerprintOf(id.Cert)
	if _, ok, err := state.fleet.Get(id.NodeID); err != nil {
		return err
	} else if ok {
		// Renewal: the pin follows the KEY, and the key did not change.
		if err := state.fleet.RepinNode(id.NodeID, fp); err != nil {
			return err
		}
		return state.save()
	}
	n := edge.NewNode(pub, name, edge.Host)
	n.Pin = fp
	n.Presence = string(edge.PresenceVerified)
	n.LastSeen = now.Unix()
	if _, err := state.fleet.Enroll(n); err != nil {
		return err
	}
	return state.save()
}

// edgeNameOf is the owner's name for a node already on this Edge, falling back to what
// they just typed.
func edgeNameOf(id, fallback string) string {
	state, err := loadEdgeState()
	if err != nil {
		return fallback
	}
	if n, ok, err := state.fleet.Get(id); err == nil && ok && n.Name != "" {
		return n.Name
	}
	return fallback
}

// edgeDefaultName is what this machine is called when the owner did not say. The
// hostname is what they already call it.
func edgeDefaultName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "this-machine"
	}
	h = strings.SplitN(h, ".", 2)[0]
	if edge.ValidName(h) != nil {
		return "this-machine"
	}
	return h
}

// --- authority ------------------------------------------------------------

func cmdEdgeAuthority(cfg config, args []string) error {
	leaf, _ := edgeLeaf("authority")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	if len(argv.pos) == 0 {
		return edgeShowAuthority()
	}
	switch argv.pos[0] {
	case "local":
		where := ""
		if len(argv.pos) > 1 {
			where = argv.pos[1]
		}
		return edgeDesignateLocal(where, argv.has("force"))
	case "core":
		return edgeMoveToCore(argv.has("force"))
	case "allow":
		if len(argv.pos) < 2 {
			return usagef("usage: roger edge authority allow <user key>")
		}
		return edgeAllow(argv.pos[1])
	default:
		return usagef("roger edge authority takes `local <name>`, `core`, `allow <user key>`, or nothing at all")
	}
}

// edgeShowAuthority answers the owner's real question: who roots this Edge, and can I
// add a machine to it right now without a network.
func edgeShowAuthority() error {
	d := edgeDescriptor()
	fmt.Printf("this Edge is rooted at %s.\n", d.Names())
	fmt.Printf("  %s\n", d.NetworkLine())
	if d.Fingerprint != "" {
		fmt.Printf("  root %s\n", edgeShortFP(d.Fingerprint))
	}
	if local, ok, err := edgeauth.OpenLocal(edgeAuthDir()); err == nil && ok {
		fmt.Printf("  this machine IS the authority: it holds the root's private half, and nothing else does.\n")
		allowed, _ := local.Allowed()
		fmt.Printf("  %d machine(s) may enroll against it (roger edge authority allow <user key>)\n", len(allowed))
	}
	return nil
}

// edgeDesignateLocal makes this machine the Edge's authority. It generates the root
// HERE and keeps the private half HERE: only the public root is ever distributed.
func edgeDesignateLocal(where string, force bool) error {
	if where == "" {
		where = edgeDefaultName()
	}
	if err := edge.ValidName(where); err != nil {
		return usagef("%v", err)
	}
	dir := edgeAuthDir()
	if _, ok, err := edgeauth.OpenLocal(dir); err != nil {
		return err
	} else if ok && !force {
		return fmt.Errorf("this machine already holds an Edge root (%s).\n"+
			"  an Edge has exactly one authority, and a second root would split the fleet.\n"+
			"  to move this Edge to a different authority deliberately, add --force", edgeDescriptor().Names())
	}
	before := edgeDescriptor()
	if force {
		if err := edgeRetireRoot(dir); err != nil {
			return err
		}
	}
	local, err := edgeauth.Designate(dir, where)
	if err != nil {
		return err
	}
	// The machine that designated is the first machine allowed to enroll against it.
	pub := edgeUserKey().Public().(ed25519.PublicKey)
	if err := local.Allow(hex.EncodeToString(pub)); err != nil {
		return err
	}
	st := edgeIdentityStore()
	if err := st.SaveDescriptor(local.Descriptor()); err != nil {
		return err
	}
	fmt.Printf("this machine is now the Edge authority %q.\n", where)
	fmt.Printf("  the Edge root was generated here; its private half stays on this machine and is never sent.\n")
	fmt.Printf("  %s\n", local.Descriptor().NetworkLine())
	if force {
		return edgeAnnounceMigration(before, local.Descriptor())
	}
	return nil
}

// edgeMoveToCore hands the Edge back to Core. Like every authority change it is a
// migration, never a silent switch.
func edgeMoveToCore(force bool) error {
	before := edgeDescriptor()
	if before.Kind == edgeauth.KindCore && before.Fingerprint == "" {
		fmt.Println("this Edge is already rooted at Core.")
		return nil
	}
	if !force {
		return fmt.Errorf("this Edge is rooted at %s, and moving it means every member re-enrolls.\n"+
			"  if that is what you want, add --force", before.Names())
	}
	if err := edgeRetireRoot(edgeAuthDir()); err != nil {
		return err
	}
	after := edgeauth.Descriptor{Kind: edgeauth.KindCore}
	if err := edgeIdentityStore().SaveDescriptor(after); err != nil {
		return err
	}
	fmt.Println("this Edge is now rooted at Core.")
	return edgeAnnounceMigration(before, after)
}

// edgeRetireRoot removes the authority material and this machine's own membership of
// the Edge that is being left. The FLEET is not touched here: members are told to
// re-enroll, never silently dropped.
func edgeRetireRoot(dir string) error {
	if err := os.RemoveAll(filepath.Join(dir, edgeauth.AuthorityDir)); err != nil {
		return err
	}
	st := edgeauth.Store{Dir: dir}
	if err := st.ForgetIdentity(); err != nil {
		return err
	}
	return os.Remove(filepath.Join(dir, edgeauth.RootCertFile))
}

// edgeAnnounceMigration marks every member as needing re-enrollment and says so. A
// member is KEPT and shown with the reason: a fleet that silently empties on an
// administrative change hides the very thing the owner needs to act on.
func edgeAnnounceMigration(before, after edgeauth.Descriptor) error {
	state, err := loadEdgeState()
	if err != nil {
		return err
	}
	reason := fmt.Sprintf("the Edge authority moved from %s to %s", before.Names(), after.Names())
	n, err := state.fleet.RequireReenrollment(reason)
	if err != nil {
		return err
	}
	if err := state.save(); err != nil {
		return err
	}
	fmt.Printf("EVERY member must re-enroll: %d node(s) are marked NEEDS RE-ENROLLMENT.\n", n)
	fmt.Printf("  %s.\n", reason)
	fmt.Printf("  nothing was dropped; each node stays listed, with the reason, until it re-enrolls.\n")
	return nil
}

// edgeAllow tells this machine's authority that another machine may enroll against it.
// A network with no Core has no account registry, so the owner keeps one - "no
// internet" is not a reason to let any machine on the LAN mint itself an identity.
func edgeAllow(userKey string) error {
	raw, err := hex.DecodeString(userKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return usagef("that is not a machine's user key; run `roger account` on the machine to see its key")
	}
	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("this machine is not an Edge authority, so there is nothing to allow against.\n" +
			"  designate it first: roger edge authority local <name>")
	}
	if err := local.Allow(userKey); err != nil {
		return err
	}
	fmt.Printf("that machine may now enroll against this authority.\n")
	fmt.Printf("  on it, run: roger edge enroll <name> --authority http://<this machine's LAN address>\n")
	return nil
}

// --- forgetting a node, and taking its certificate back -------------------

// edgeRevokeOnForget ends the certificate of a node leaving this Edge, so a peer that
// already pinned it refuses it from then on - with no internet, and after a restart.
//
// Two things happen and they are separate on purpose: the AUTHORITY revokes (if this
// machine is one and it issued that certificate), and this machine records the serial
// in its own trust store (so its own verification refuses it immediately, rather than
// at the next refresh).
func edgeRevokeOnForget(nodeID string, n store.EdgeNode) error {
	st := edgeIdentityStore()
	now := time.Now()
	var serial string

	if local, ok, err := edgeauth.OpenLocal(edgeAuthDir()); err == nil && ok {
		if s, err := local.Revoke(nodeID); err == nil {
			serial = s
		}
	}
	// The node being forgotten may be THIS machine, in which case its certificate is
	// right here and leaving the Edge means giving it up.
	if held, _, ok, err := st.LoadIdentity(); err == nil && ok && held.NodeID == nodeID {
		if serial == "" {
			serial = held.Cert.SerialNumber.String()
		}
		if err := st.ForgetIdentity(); err != nil {
			return err
		}
	}
	if serial == "" {
		return nil // nothing this machine can revoke: the pin going is all it can do
	}
	_ = n
	return st.Revoke(serial, now)
}

// edgeTrustNote is the line a fleet view prints when this machine's revocation list has
// gone stale. It is STATED rather than assumed either way: the risk is not that the
// list is old, it is that a revoked node looks fine because nobody said so.
func edgeTrustNote(now time.Time) string {
	t, err := edgeIdentityStore().LoadTrust()
	if err != nil || !t.Stale(now, edgeauth.FreshnessWindow) {
		return ""
	}
	return fmt.Sprintf("(trust information is stale: this machine's revocation list was last refreshed %s - "+
		"a node revoked since then would still verify here)", edgeAge(t.RefreshedAt))
}
