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
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/edgeauth/enrollhttp"
	"rogerai.fm/roger/v6/internal/joininvite"
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

// edgeUserPubHex is the public half of the key this machine signs its enrollment with - the key an
// authority must allow. It is said from the same seam that signs, so the key shown is the key used.
func edgeUserPubHex() string { return hex.EncodeToString(edgeUserKey().Public().(ed25519.PublicKey)) }

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

func cmdEdgeEnroll(cfg config, args []string) error { return edgeEnrollCmd(cfg, args, false) }

// edgeEnrollCmd is `roger edge enroll`, or (joined) the same enrollment run by `roger join <invite>`,
// which says it the way every join says it: what happened, then one next step
// (features/join/surfaces.feature).
func edgeEnrollCmd(cfg config, args []string, joined bool) error {
	leaf, _ := edgeLeaf("enroll")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	name := ""
	if len(argv.pos) > 0 {
		name = argv.pos[0]
	}
	endpoint, tr, err := edgeTrustFromArgs(argv, argv.flags["authority"])
	if err != nil {
		return err
	}
	if edgeIsInteractive() {
		tr.Ask = func(prompt string) string { fmt.Print(prompt); return edgeReadLine() }
	}
	account := client.LinkedLogin()
	if want, ok := argv.flags["account"]; ok && want != account {
		// Never repeated back, and nothing is sent: a refusal that names the other
		// account turns "does this account exist?" into a question anyone can ask.
		return fmt.Errorf("this machine is logged in as %s, and can only enroll into its own Edge", account)
	}
	res, err := edgeEnrollJoin(cfg, name, endpoint, account, tr)
	if err != nil {
		if joined && errors.Is(err, edgeauth.ErrUnknownMachine) {
			// An invite admits its holder's key when its code is used; refused as unknown, the code
			// was already used or has expired. The fix is a new invite, not the allow steps.
			return fmt.Errorf("that invite was already used or has expired. Ask the owner for a new one: roger edge invite")
		}
		return edgeEnrollErr(err, name, endpoint)
	}
	if res.LeftAlone {
		fmt.Println("left alone: this machine stays on the Edge it is on.")
		return nil
	}
	if res.RootUnchanged {
		fmt.Printf("the root is unchanged (%s): this machine is already on that Edge, so nothing is replaced.\n",
			edgeauth.ShortFingerprint(res.RootFingerprint))
		return nil
	}
	if res.AlreadyEnrolled {
		fmt.Println(edgeEnrolledLine(res, edgeStationName()))
		fmt.Printf("  its certificate is good until %s; nothing to do.\n",
			res.NotAfter.UTC().Format(time.RFC3339))
		return nil
	}
	if joined {
		edgeName := res.Edge
		if edgeName == "" {
			edgeName = res.Authority
		}
		fmt.Printf("joined the Edge %s as %s\n", edgeName, edge.SelfLabel(res.Name, edgeStationName()))
	} else {
		fmt.Println(edgeEnrolledLine(res, edgeStationName()))
	}
	if res.ReplacedRoot != "" {
		fmt.Printf("  this machine left the Edge rooted at %s and joined the one rooted at %s.\n",
			edgeauth.ShortFingerprint(res.ReplacedRoot), edgeauth.ShortFingerprint(res.RootFingerprint))
	}
	switch res.ProvenBy {
	case "code":
		fmt.Printf("  root %s - the same short fingerprint `roger edge authority` shows there, for reassurance;\n"+
			"  the code is what was checked.\n", edgeauth.ShortFingerprint(res.RootFingerprint))
	case "pin":
		fmt.Printf("  root %s - it matched the full fingerprint you gave.\n", edgeauth.ShortFingerprint(res.RootFingerprint))
	}
	fmt.Printf("  this machine now advertises itself, so your other machines can see it.\n")
	if res.TrustWarning != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", res.TrustWarning)
	}
	if joined {
		fmt.Println("open roger and press 3 to see it")
	}
	return nil
}

// edgeEnrollResult is what an enrollment did, for a caller that prints its own UX (the CLI
// wrapper) or drives a wizard (the TUI hook). No printing happens in the core.
// edgeEnrolledLine is the enroll confirmation: this machine by the name every screen calls it
// (edge.SelfLabel), never by its node id.
func edgeEnrolledLine(res edgeEnrollResult, station string) string {
	name := edge.SelfLabel(res.Name, station)
	if res.AlreadyEnrolled {
		return fmt.Sprintf("already enrolled as %q, on the Edge rooted at %s.", name, res.Authority)
	}
	return fmt.Sprintf("enrolled %q on the Edge rooted at %s.", name, res.Authority)
}

type edgeEnrollResult struct {
	NodeID          string
	Name            string
	Authority       string // the descriptor's human name, for "rooted at ..."
	Edge            string // the name the owner gave the Edge, from its verified answer ("" if none)
	AlreadyEnrolled bool
	NotAfter        time.Time
	// TrustWarning is a non-fatal note (e.g. the revocation list could not be refreshed at enroll
	// time), for the caller's own UX. The core does not print it - the CLI prints it, the TUI can
	// surface it in the wizard.
	TrustWarning string
	// RootFingerprint is the root this machine now holds; ProvenBy says what vouched for it on a
	// first join ("code" or "pin"; "" when the root was already held, or came over TLS from Core).
	RootFingerprint string
	ProvenBy        string
	// ReplacedRoot is the fingerprint of the root a deliberate move left behind (ruling D11).
	ReplacedRoot string
	// RootUnchanged: --replace-root was asked for, and the authority's root is the one held.
	RootUnchanged bool
	// LeftAlone: the owner declined a root change, so nothing changed.
	LeftAlone bool
}

// edgeEnrollCore is the enrollment with nothing to vouch for a new root: a Core enrollment, the
// authority enrolling itself, or a member renewing. A first join against an authority that needs
// the owner's code is refused by it (the callers that can ask for the code use edgeEnrollJoin).
func edgeEnrollCore(cfg config, name, endpoint, account string) (edgeEnrollResult, error) {
	return edgeEnrollJoin(cfg, name, endpoint, account, edgeJoinTrust{})
}

// edgeEnrollJoin is the whole enrollment orchestration with NO printing (beyond the code prompt a
// caller's Ask performs), so the CLI and the TUI wizard drive the same code. It returns typed
// errors the wizard can classify - in particular a join refused because this machine is not on the
// authority's allow-list comes back wrapping edgeauth.ErrUnknownMachine, so a caller can tell
// "allow me" from every other refusal. account is the resolved login (may be empty); name "" takes
// the default. With tr.Hold, a first join returns errEdgeNeedsCode and a held answer to confirm.
func edgeEnrollJoin(cfg config, name, endpoint, account string, tr edgeJoinTrust) (edgeEnrollResult, error) {
	res, held, err := edgeEnrollBegin(cfg, name, endpoint, account, tr)
	if err != nil || held == nil {
		return res, err
	}
	return held.finish()
}

// edgeEnrollBegin runs a join up to the point where the root has to be vouched for. A nil held
// means the join already finished (or needed no vouching); a non-nil one is an answer that passed
// every check but the owner's, with nothing written.
func edgeEnrollBegin(cfg config, name, endpoint, account string, tr edgeJoinTrust) (edgeEnrollResult, *edgeHeldJoin, error) {
	var res edgeEnrollResult
	local, isAuthority, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil {
		return res, nil, err
	}
	// A login is what ties this machine to an account. It is NOT needed when the Edge is
	// rooted locally - the owner naming an authority, or this machine being one - because
	// on a network with no Core there is nothing to log in to, and it is the authority
	// that decides who may join.
	if account == "" && endpoint == "" && !isAuthority {
		return res, nil, usagef("this machine is not logged in, so there is no account to enroll it into.\n" +
			"  log in first:                 roger login\n" +
			"  or, with no internet at all:  roger edge authority local <name>, then " +
			"roger edge enroll <name> --authority <address> --code <the code allow prints there>")
	}
	if name == "" {
		name = edgeDefaultName()
	}
	if err := edge.ValidName(name); err != nil {
		return res, nil, usagef("%v", err)
	}
	res.Name = name

	st := edgeIdentityStore()
	held, heldKey, enrolled, err := st.LoadIdentity()
	if err != nil {
		return res, nil, err
	}
	now := edgeNow()
	heldRoot := edgeHeldRoot(st)
	if enrolled {
		heldRoot = held.Root
	}
	if heldRoot == nil && isAuthority {
		// The machine that roots the Edge knows its own root: even reached over its own LAN
		// address, the answer must carry exactly that root.
		heldRoot = local.Authority().Root()
	}

	// ONE EDGE, ONE AUTHORITY. When the owner names an authority and this machine already
	// has a root, ask that authority whose root it is BEFORE asking it for anything. Only a
	// deliberate --replace-root, backed by the new root's code or pin, goes past it (D11).
	replacing := false
	if endpoint != "" && heldRoot != nil {
		if tr.ReplaceRoot {
			if theirs := edgeRootAt(endpoint); theirs != nil && theirs.Equal(heldRoot) {
				res.RootUnchanged, res.RootFingerprint = true, edgeauth.RootFingerprint(heldRoot)
				return res, nil, nil
			}
			replacing = true
		} else if err := edgeRefuseASecondAuthority(st, endpoint); err != nil {
			return res, nil, err
		}
	}
	if enrolled && !replacing && !held.NeedsRenewal(now) {
		res.NodeID, res.Name = held.NodeID, edgeNameOf(held.NodeID, name)
		res.Authority, res.AlreadyEnrolled, res.NotAfter = edgeDescriptor().Names(), true, held.NotAfter()
		return res, nil, nil
	}

	// WHAT MUST VOUCH FOR THE ROOT. Core arrives over TLS that authenticated Core; the authority
	// enrolling itself holds its own root; a member renewing is checked against the root it holds.
	// Everything else - a first join against an authority, a deliberate move - needs the owner.
	needProof := endpoint != "" && (heldRoot == nil || replacing)
	if replacing && tr.Code == "" && tr.Pin == "" {
		return res, nil, usagef("a new root needs the code from its authority (or its full fingerprint):\n" +
			"  on the new authority, allow or adopt prints one; then add --code <code> here.\n" +
			"  nothing was asked for and nothing changed")
	}
	if replacing && !tr.Yes && tr.Ask == nil {
		return res, nil, usagef("a root change must be confirmed: this machine would leave the Edge it is on.\n" +
			"  add --yes to confirm it with no prompt. nothing was asked for and nothing changed")
	}
	if needProof && tr.Code == "" && tr.Pin == "" && tr.Ask == nil && !tr.Hold {
		return res, nil, usagef("the root of the Edge you are joining has to be confirmed before this machine keeps it.\n"+
			"  easiest: ask the owner for an invite (roger edge invite on the authority), then run: roger join <invite>\n"+
			"  or pass --code <code>: the authority prints it when you allow or adopt this machine there\n"+
			"    (roger edge authority allow %s)\n"+
			"  or pass --root sha256:<fingerprint>: the full fingerprint `roger edge authority` prints there.\n"+
			"  nothing was sent and nothing was kept", edgeUserPubHex())
	}
	if endpoint == "" && !isAuthority && !edgeBrokerAuthenticated(cfg.Broker) {
		return res, nil, usagef("Core enrollment needs an https broker, and %s is not one: over plain http the root\n"+
			"  this machine would be handed is only as good as the network it crossed.\n"+
			"  to enroll on this network instead, use a local authority with a code:\n"+
			"    roger edge authority local <name>   (on the machine that will root the Edge)\n"+
			"  nothing was sent and nothing was kept", cfg.Broker)
	}

	// A renewal keeps the key, and therefore the node id, and therefore its place, its
	// name and its history. A first enrollment (or a move to a new root) generates one, here,
	// and it never leaves.
	pub, priv := ed25519.PublicKey(nil), heldKey
	if enrolled && !replacing {
		pub = heldKey.Public().(ed25519.PublicKey)
	} else {
		pub, priv, err = ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return res, nil, err
		}
	}

	// WHICH ACCOUNT. Against Core, this machine's login says. Against an authority the
	// owner NAMED, or this machine being one, there is no login to speak for: the
	// authority owns its Edge's account and says which it is.
	claim := account
	if endpoint != "" || isAuthority {
		claim = ""
	}
	req := edgeauth.NewRequest(claim, name, string(edge.Host), pub, now)
	req.Sign(edgeUserKey())
	// With the invite's code and the root's fingerprint in hand, prove the code to the authority, so
	// a key-less invite (roger edge invite) admits this device's key (bearer.go). An allowed key's
	// join ignores the proof.
	if tr.Code != "" && tr.Pin != "" {
		req.JoinProof = edgeauth.JoinProof(tr.Code, tr.Pin, req)
	}

	var resp edgeauth.Response
	switch {
	case endpoint == "" && isAuthority:
		resp, err = local.Issue(req)
	case endpoint == "":
		resp, err = enrollhttp.Enroll(context.Background(), cfg.Broker, req)
		if err != nil {
			return res, nil, edgeUnreachableAuthority(err)
		}
	default:
		resp, err = enrollhttp.Enroll(context.Background(), endpoint, req)
		if err != nil {
			return res, nil, edgeClassifyEnrollErr(err)
		}
	}
	if err != nil {
		return res, nil, err
	}

	want := edgeauth.Expect{NodeID: req.NodeID, Account: claim, Key: pub, Now: now}
	if !needProof {
		want.Root = heldRoot
	}
	id, err := edgeauth.CheckResponse(resp, want)
	if errors.Is(err, edgeauth.ErrDifferentAuthority) {
		return res, nil, edgeDifferentRootRefusal(isAuthority)
	}
	if err != nil {
		return res, nil, err
	}

	h := &edgeHeldJoin{id: id, resp: resp, nonce: req.Nonce, where: edgeAuthorityLabel(endpoint), received: edgeNow()}
	h.finish = func() (edgeEnrollResult, error) {
		r, err := edgeEnrollFinish(res, st, id, priv, pub, name, endpoint, local, isAuthority, now, replacing, held, enrolled, heldRoot)
		r.Edge = edgeauth.EdgeLabel(resp.Edge) // the Edge's own name, from the answer just verified
		return r, err
	}
	if !needProof {
		h.verified = true
		return res, h, nil
	}
	if tr.Pin != "" {
		if err := edgeCheckPin(tr.Pin, id.Root, h.where); err != nil {
			return res, nil, err
		}
		res.ProvenBy = "pin"
	}
	switch {
	case tr.Code != "":
		// --code is ONE attempt: a wrong one is not retried behind the owner's back (D5).
		if _, err := h.check(tr.Code); err != nil {
			return res, nil, err
		}
		res.ProvenBy = "code"
	case tr.Pin != "":
		h.verified = true
	case tr.Hold:
		return res, h, errEdgeNeedsCode
	default:
		if err := edgeAskForCode(h, tr.Ask); err != nil {
			return res, nil, err
		}
		res.ProvenBy = "code"
	}
	if replacing && !tr.Yes {
		fmt.Printf("replace this machine's Edge root %s with %s? it leaves the Edge it is on now [y/N]: ",
			edgeauth.ShortFingerprint(edgeauth.RootFingerprint(heldRoot)), h.rootShort())
		if !edgeConfirmed() {
			res.LeftAlone = true
			return res, nil, nil
		}
	}
	h.finish = func() (edgeEnrollResult, error) {
		r, err := edgeEnrollFinish(res, st, id, priv, pub, name, endpoint, local, isAuthority, now, replacing, held, enrolled, heldRoot)
		r.Edge = edgeauth.EdgeLabel(resp.Edge) // the Edge's own name, from the answer just verified
		return r, err
	}
	return res, h, nil
}

// confirm finishes a held join with the owner's code: the TUI wizard's second step.
func (h *edgeHeldJoin) confirm(typed string) (edgeEnrollResult, bool, error) {
	done, err := h.check(typed)
	if err != nil {
		return edgeEnrollResult{}, done, err
	}
	res, err := h.finish()
	res.ProvenBy = "code"
	return res, true, err
}

// edgeRootAt is the root an authority says it signs under, or nil when it cannot say. It is a HINT,
// used only to tell "nothing to replace" from a move; a move still needs the new root's proof.
func edgeRootAt(endpoint string) *x509.Certificate {
	pemText, err := enrollhttp.Root(context.Background(), endpoint)
	if err != nil {
		return nil
	}
	c, err := edgeauth.DecodeCert(pemText)
	if err != nil {
		return nil
	}
	return c
}

// edgeDifferentRootRefusal is what a machine is told when an authority answers with a root other
// than the one it holds. A member is told how a deliberate move is made; only the authority itself
// is pointed at the authority's own migration command.
func edgeDifferentRootRefusal(isAuthority bool) error {
	if isAuthority {
		return fmt.Errorf("this Edge is rooted at %s, and that certificate comes from somewhere else.\n"+
			"  an Edge has exactly one authority; moving it is a deliberate migration:\n"+
			"    roger edge authority local <name> --force", edgeDescriptor().Names())
	}
	return fmt.Errorf("this machine belongs to the Edge rooted at %s, and that certificate comes from a different authority.\n"+
		"  if the owner moved the Edge on purpose, the new authority's allow or adopt prints a code:\n"+
		"    roger edge enroll <name> --authority <its address> --code <that code> --replace-root\n"+
		"  nothing changed", edgeDescriptor().Names())
}

// edgeEnrollFinish writes a vouched-for identity. Only now is anything written. Everything before
// it is checks; nothing here can leave half an identity behind, because there was nothing to
// half-write until here.
func edgeEnrollFinish(res edgeEnrollResult, st edgeauth.Store, id *edgeauth.Identity, priv ed25519.PrivateKey,
	pub ed25519.PublicKey, name, endpoint string, local *edgeauth.Local, isAuthority bool, now time.Time,
	replacing bool, held *edgeauth.Identity, enrolled bool, heldRoot *x509.Certificate) (edgeEnrollResult, error) {
	if replacing {
		res.ReplacedRoot = edgeauth.RootFingerprint(heldRoot)
		// The machine's old membership ends in the same step it gains the new one: its own row under
		// the old root is retired so the name it had comes with it.
		if enrolled {
			if state, err := loadEdgeState(); err == nil {
				if _, ok, _ := state.fleet.Get(held.NodeID); ok {
					_ = state.fleet.Forget(held.NodeID)
					_ = state.save()
				}
			}
		}
	}
	if err := st.SaveAccount(id.Account); err != nil {
		return res, err
	}
	if err := st.SaveIdentity(id, priv, edgeAuthorityDescriptor(endpoint, local, isAuthority)); err != nil {
		return res, err
	}
	warn, err := edgeRefreshTrust(st, endpoint, local, isAuthority, now)
	if err != nil {
		return res, err
	}
	res.TrustWarning = warn
	if err := edgeRecordSelf(id, pub, name, now); err != nil {
		return res, err
	}
	res.NodeID, res.Authority = id.NodeID, edgeDescriptor().Names()
	res.RootFingerprint = edgeauth.RootFingerprint(id.Root)
	return res, nil
}

// edgeClassifyEnrollErr turns the authority's flattened 403 back into a typed error a
// wizard can act on: a refusal because this machine is not on the allow-list wraps
// edgeauth.ErrUnknownMachine, so the caller shows the allow instructions; anything it
// cannot classify is returned as-is, so a stale or foreign-account refusal is never
// mislabeled "you need to be allowed". (The transport flattens the type; this is the
// contained fix - the wire-level typed sentinel is a larger change noted in the design.)
func edgeClassifyEnrollErr(err error) error {
	if err != nil && strings.Contains(err.Error(), edgeauth.ErrUnknownMachine.Error()) {
		return fmt.Errorf("%w", edgeauth.ErrUnknownMachine)
	}
	// An invite minted for another device (empty_edge_simple.feature §7) says what to do about it.
	if err != nil && strings.Contains(err.Error(), edgeauth.ErrInviteForOther.Error()) {
		// Reads as look.JoinSaysForOther, and is still edgeauth.ErrInviteForOther to a caller.
		return fmt.Errorf("%w: ask for one for this one", edgeauth.ErrInviteForOther)
	}
	return err
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
	move := "  to move this Edge deliberately, so every member is told to re-enroll:\n" +
		"    roger edge authority local <name> --force"
	if _, isAuthority, _ := edgeauth.OpenLocal(edgeAuthDir()); !isAuthority {
		move = "  the root it answers with is not this Edge's: that certificate comes from a different authority.\n" +
			"  if the owner moved the Edge there on purpose, its allow or adopt prints a code:\n" +
			"    roger edge enroll <name> --authority " + endpoint + " --code <that code> --replace-root"
	}
	return fmt.Errorf("this Edge is rooted at %s, and %s is a different authority.\n"+
		"  an Edge has exactly one root: a second one would mean a peer that verifies for\n"+
		"  half your fleet and not the other half. Nothing was asked for and no root was created.\n"+
		"%s", edgeDescriptor().Names(), edgeAuthorityLabel(endpoint), move)
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
func edgeRefreshTrust(st edgeauth.Store, endpoint string, local *edgeauth.Local, isAuthority bool, now time.Time) (string, error) {
	if isAuthority && endpoint == "" {
		revs, err := local.Revocations()
		if err != nil {
			return "", err
		}
		return "", st.SaveTrust(revs, now)
	}
	if endpoint != "" {
		// A failed or refused fetch leaves what is stored exactly as it is (so edgeTrustNote still
		// warns) and RETURNS a warning for the caller to surface, rather than printing into the core
		// (which the TUI shares).
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return edgeTakeSignedRevocations(ctx, st, endpoint, now), nil
	}
	// A node with no authority and no endpoint has no source to refresh from, so it likewise must not
	// claim a fresh list. Leave the stored trust (and its RefreshedAt) untouched.
	return "", nil
}

// edgeRecordSelf puts this machine on its own fleet, or leaves the row it already has
// exactly where it is. A renewal is not a new member.
func edgeRecordSelf(id *edgeauth.Identity, pub ed25519.PublicKey, name string, now time.Time) error {
	state, err := loadEdgeState()
	if err != nil {
		return err
	}
	fp := edge.FingerprintOf(id.Cert)
	// A member the owner was told to RE-ENROLL, doing exactly that. Its old row is that
	// instruction, not a rival for the name: retire it, so the machine can come back
	// under the Edge's new authority. (A live run caught this: without it an authority
	// migration told every member to re-enroll and then refused them the name they were
	// listed under.)
	if old, ok, err := state.fleet.ByName(name); err != nil {
		return err
	} else if ok && old.ID != id.NodeID && old.Presence == string(edge.PresenceReenroll) {
		if err := state.fleet.Forget(old.ID); err != nil {
			return err
		}
	}
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
	// A read that FAILED is not an answer. "This Edge is rooted at Core" printed because
	// the record could not be read would be a confident lie, and the owner would act on
	// it - which is the whole failure this layer exists to make visible.
	d, _, err := edgeIdentityStore().Descriptor()
	if err != nil {
		return fmt.Errorf("this machine's Edge record could not be read, so what roots this "+
			"Edge is unknown: %w", err)
	}
	local, isAuthority, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil {
		return fmt.Errorf("this machine's Edge authority could not be read: %w", err)
	}
	fmt.Printf("this Edge is rooted at %s.\n", d.Names())
	fmt.Printf("  %s\n", d.NetworkLine())
	if d.Fingerprint != "" {
		fmt.Printf("  root %s\n", edgeauth.ShortFingerprint(d.Fingerprint))
		fmt.Printf("  to pin it when joining:  --root sha256:%s\n", d.Fingerprint)
	}
	if isAuthority {
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
	local, err := edgeDesignateCore(dir, where)
	if err != nil {
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

// edgeDesignateCore generates this Edge's root here and allows this machine, with NO
// printing, so the CLI and the TUI wizard share it. Force/migration stays in the wrapper.
func edgeDesignateCore(dir, where string) (*edgeauth.Local, error) {
	local, err := edgeauth.Designate(dir, where)
	if err != nil {
		return nil, err
	}
	// The machine that designated is the first machine allowed to enroll against it, and its own key
	// is PROTECTED (a later forget must never evict it, or the authority could lock itself out of
	// renewing its own certificate) - both in one locked step so a crash cannot leave it
	// allowed-but-unprotected (audit 2026-09-24).
	pub := edgeUserKey().Public().(ed25519.PublicKey)
	if err := local.AllowProtected(hex.EncodeToString(pub)); err != nil {
		return nil, err
	}
	if err := edgeIdentityStore().SaveDescriptor(local.Descriptor()); err != nil {
		return nil, err
	}
	return local, nil
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
	// The Edge's public root goes with the authority that made it. Its absence is not a
	// failure: a machine that designated an authority and never enrolled against it has
	// nothing here to remove.
	if err := os.Remove(filepath.Join(dir, edgeauth.RootCertFile)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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
	code, err := local.MintKeyCode(userKey)
	if err != nil {
		return err
	}
	shown := edgeauth.FormatCode(code)
	fp := local.Descriptor().Fingerprint
	fmt.Printf("that machine may now enroll against this authority.\n")
	fmt.Printf("  enrollment code: %s  (good for %d minutes, for one join; allowing again makes a new one)\n",
		shown, int(edgeauth.CodeLifetime.Minutes()))
	fmt.Printf("  root %s - the joining machine shows the same when it has joined\n", edgeauth.ShortFingerprint(fp))
	fmt.Println("  a machine running a roger from before root proofs does not check the code: update roger on it before it joins.")
	b, err := os.ReadFile(edgeAuthorityPortFile())
	port := strings.TrimSpace(string(b))
	ips := edgeLANIPs()
	if err != nil || port == "" || len(ips) == 0 {
		fmt.Println("  start roger on this machine so the authority answers on your network; then, on that machine:")
		fmt.Println("  roger edge enroll <name> --authority http://<this machine's address>:<port in " + edgeAuthorityPortFile() + "> --code " + shown)
		fmt.Println("  or paste one invite:")
		fmt.Println("  " + joininvite.JoinLine(edgeInvite("<this machine's address>:<port>", fp, code)))
		return nil
	}
	fmt.Println("  on it, run (with roger running here, so the authority answers):")
	for _, ip := range ips {
		fmt.Printf("    roger edge enroll <name> --authority http://%s --code %s\n", net.JoinHostPort(ip, port), shown)
	}
	fmt.Println("  or paste one invite:")
	fmt.Printf("    %s\n", joininvite.JoinLine(edgeInvite(net.JoinHostPort(ips[0], port), fp, code)))
	return nil
}

// edgeLANIPs is this machine's addresses another machine on the LAN could reach it at: IPv4,
// interfaces that are up, not loopback.
var edgeLANIPs = func() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, i := range ifaces {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsGlobalUnicast() {
				out = append(out, n.IP.String())
			}
		}
	}
	return out
}

// edgeEnrollErr turns an enroll the authority refused for not knowing this machine into the fix, as
// the setup wizard says it: the allow command with this machine's key, then the enroll again.
func edgeEnrollErr(err error, name, endpoint string) error {
	if !errors.Is(err, edgeauth.ErrUnknownMachine) {
		return err
	}
	retry := "roger edge enroll " + name
	if endpoint != "" {
		retry += " --authority " + endpoint
	}
	return fmt.Errorf("%w - the authority has not allowed this machine yet.\n"+
		"  on the AUTHORITY machine, run:  roger edge authority allow %s\n"+
		"  then here, again:                %s", err, edgeUserPubHex(), retry)
}

// --- forgetting a node, and taking its certificate back -------------------

// edgeRevokeOnForget ends the certificate of a node leaving this Edge, so a peer that
// already pinned it refuses it from then on - with no internet, and after a restart.
//
// Two things happen and they are separate on purpose: the AUTHORITY revokes (if this
// machine is one and it issued that certificate), and this machine records the serial
// in its own trust store (so its own verification refuses it immediately, rather than
// at the next refresh).
func edgeRevokeOnForget(nodeID string) ([]string, string, error) {
	st := edgeIdentityStore()
	now := time.Now()
	var authorityRevoked []string // what THIS machine, as the authority, actually revoked
	var evictedKey string         // the enrolling key forget removed from the allow-list, or ""

	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil {
		// The authority is here but could not be opened (a corrupt issued.json / revoked.json). We
		// must NOT go on to forget the node from the fleet and report success while revoking nothing
		// - that is the fail-open the audit caught. Stop and report (audit 2026-09-24).
		return nil, "", fmt.Errorf("could not open the Edge authority to revoke %s: %w", edgeDisplayName(nodeID), err)
	}
	if ok {
		// Migrate an authority designated before protection existed: protect THIS machine's own key
		// (if it is on the allow-list) before any eviction, so a forget can never evict it.
		if err := local.BackfillProtection(hex.EncodeToString(edgeUserKey().Public().(ed25519.PublicKey))); err != nil {
			return nil, "", fmt.Errorf("could not protect this machine's Edge key before forgetting %s: %w", edgeDisplayName(nodeID), err)
		}
		// Clear any standing claim grant AND revoke every certificate the node was issued in ONE
		// locked step, so a claim cannot mint a fresh certificate between the two (audit 2026-09-24).
		// Errors are reported, not swallowed, so a forget that could not take effect is never
		// reported as done.
		revoked, ek, err := local.Forget(nodeID)
		if err != nil {
			return nil, "", fmt.Errorf("could not forget %s from the authority: %w", edgeDisplayName(nodeID), err)
		}
		authorityRevoked, evictedKey = revoked, ek
	}
	// The node being forgotten may be THIS machine, in which case its certificate is
	// right here and leaving the Edge means giving it up.
	held, _, ok, err := st.LoadIdentity()
	if err != nil {
		return nil, "", fmt.Errorf("could not read this machine's identity to forget %s: %w", edgeDisplayName(nodeID), err)
	}
	toRecord := append([]string{}, authorityRevoked...)
	if ok && held.NodeID == nodeID {
		// Forgetting THIS machine's own identity: give it up, and record its serial in this machine's
		// own trust store. That is LOCAL bookkeeping, not an authority revocation, so it is not
		// counted as "revoked" for the caller's message.
		if len(toRecord) == 0 {
			toRecord = append(toRecord, held.Cert.SerialNumber.String())
		}
		if err := st.ForgetIdentity(); err != nil {
			return nil, "", err
		}
	}
	// Record every revoked serial in THIS machine's own trust store too, so its verification refuses
	// them at once rather than at the next refresh.
	for _, s := range toRecord {
		if err := st.Revoke(s, now); err != nil {
			return nil, "", err
		}
	}
	// Return what the authority actually revoked AND the enrolling key it evicted (or ""), so the
	// caller's message claims only what really happened (audit 2026-09-24).
	return authorityRevoked, evictedKey, nil
}

// edgeTrustNote is the line a fleet view prints when this machine's revocation list has
// gone stale. It is STATED rather than assumed either way: the risk is not that the
// list is old, it is that a revoked node looks fine because nobody said so.
func edgeTrustNote(now time.Time) string {
	// Only a machine that is ON an Edge has a revocation list that matters. A machine that never
	// enrolled has nothing to warn about.
	_, _, enrolled, err := edgeIdentityStore().LoadIdentity()
	if err != nil {
		return "(trust information is unreadable: this machine's Edge identity could not be read - " +
			"it cannot say whether a node has been revoked)"
	}
	if !enrolled {
		return ""
	}
	t, err := edgeIdentityStore().LoadTrust()
	if err != nil {
		return "(trust information is unreadable: this machine's revocation list could not be read - " +
			"a revoked node would still verify here)"
	}
	// Never refreshed is NOT the same as fresh: an enrolled machine that has never synced its
	// revocation list cannot vouch for anyone, so it warns rather than staying silent (audit
	// 2026-09-24).
	if t.RefreshedAt == 0 {
		return "(trust information is unknown: this machine's revocation list has never been refreshed " +
			"from the authority - a revoked node would still verify here)"
	}
	if !t.Stale(now, edgeauth.FreshnessWindow) {
		return ""
	}
	return fmt.Sprintf("(trust information is stale: this machine's revocation list was last refreshed %s - "+
		"a node revoked since then would still verify here)", edgeAge(t.RefreshedAt))
}

// edgeTrustEvery is how often a running roger refreshes its revocation list: well inside the
// freshness window, rare enough to cost nothing.
const edgeTrustEvery = 15 * time.Minute

// edgeRefreshTrustNow brings this machine's revocation list up to date while roger runs
// (enrollment.feature: "a peer that has the current revocation list"). The authority re-reads its
// own list, which is the source. A member fetches its authority's signed list and ADDS it to what it
// has, so an answer missing a serial never un-revokes it; a re-admitted device is issued a new serial,
// so nothing legitimate is lost. It returns a warning to log, or "".
func edgeRefreshTrustNow(now time.Time) string {
	st := edgeIdentityStore()
	if _, _, enrolled, err := st.LoadIdentity(); err != nil || !enrolled {
		return ""
	}
	local, isAuthority, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil {
		return "could not read this machine's authority to refresh its revocation list: " + err.Error()
	}
	if isAuthority {
		if _, err := edgeRefreshTrust(st, "", local, true, now); err != nil {
			return "could not refresh the revocation list: " + err.Error()
		}
		return ""
	}
	d, _, err := st.Descriptor()
	if err != nil || d.Kind != edgeauth.KindLocal || !strings.Contains(d.Where, ":") {
		return "" // not a member of a locally-rooted Edge reached by address: nothing to fetch from here
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return edgeTakeSignedRevocations(ctx, st, "http://"+d.Where, now)
}

// edgeSignedSkew is how far ahead of this machine's clock a signed list may say it was signed.
const edgeSignedSkew = 5 * time.Minute

// edgeTakeSignedRevocations fetches the list signed by the Edge's root and ADDS its serials to this
// machine's list (features/edge/revocation.feature). Not one serial is used from a list that is
// unsigned, fails its signature, was signed before the last one taken (a replay), or was signed in the
// future; the stored list and its age stay as they were. A list that is taken is as fresh as its
// signing. It returns a warning to show, or "".
func edgeTakeSignedRevocations(ctx context.Context, st edgeauth.Store, endpoint string, now time.Time) string {
	signed, err := enrollhttp.SignedRevocations(ctx, endpoint)
	switch {
	case errors.Is(err, enrollhttp.ErrNotSigned):
		return fmt.Sprintf("the revocation list from %s could not be checked (it was not signed), so it was not used - this machine's list may be stale", endpoint)
	case err != nil:
		return fmt.Sprintf("could not refresh the revocation list from %s - this machine's list may be stale", endpoint)
	}
	rootPEM, rerr := os.ReadFile(filepath.Join(edgeAuthDir(), edgeauth.RootCertFile))
	root, derr := edgeauth.DecodeCert(string(rootPEM))
	if rerr != nil || derr != nil {
		return "could not read this Edge's root to check the revocation list, so it was not used"
	}
	if verr := edgeauth.VerifyRevocations(root, signed); verr != nil {
		return fmt.Sprintf("refused the revocation list from %s: its signature did not verify against this Edge's root - this machine's list is unchanged", endpoint)
	}
	signedAt := time.Unix(signed.At, 0)
	if signedAt.After(now.Add(edgeSignedSkew)) {
		return fmt.Sprintf("refused the revocation list from %s: it says it was signed %s from now - the authority's clock or the list is wrong; this machine's list is unchanged", endpoint, signedAt.Sub(now).Round(time.Second))
	}
	t, err := st.LoadTrust()
	if err != nil {
		return "could not read this machine's revocation list: " + err.Error()
	}
	if signed.At < t.SignedAt {
		return fmt.Sprintf("refused the revocation list from %s: it is older than the one this machine already took - this machine's list is unchanged", endpoint)
	}
	have := map[string]bool{}
	merged := append([]string(nil), t.Revoked...)
	for _, s := range merged {
		have[s] = true
	}
	for _, s := range signed.Revoked {
		if !have[s] {
			merged, have[s] = append(merged, s), true
		}
	}
	if err := st.SaveSignedTrust(merged, signedAt); err != nil {
		return "could not save the refreshed revocation list: " + err.Error()
	}
	return ""
}
