package main

// `roger edge grant` (features/edge/control.feature, the @cli scenarios): the owner issues, lists and
// revokes Edge grants. An Edge grant is store.Grant SIGNED BY THIS EDGE'S AUTHORITY, so any member
// verifies it with the public root alone - no broker, which is what an airgapped Edge requires. It is
// therefore issued ONLY on the authority machine (the one holding the root key), and kept in a plain
// file under the edge dir (edge.GrantStore) so it can be listed, revoked, and carried by
// `roger edge invoke`.
//
// The issue path splits in two: edgeIssueGrant is the pure core (validate, build, sign, store) that
// tests drive with a real authority and no fleet; cmdEdgeGrant is the glue that resolves the caller
// and callee ADDRESSES to node ids and opens this machine's authority.

import (
	"crypto"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/store"
)

// edgeGrantSpec is a validated request to issue one Edge grant. Caller and callee are already
// resolved to node ids; capability is one of the edge vocabulary.
type edgeGrantSpec struct {
	caller     string // node id allowed to invoke; "" = any of the owner's own instances
	callee     string // node id the grant addresses
	capability string // operate | sense | actuate | ...
	expiresAt  int64  // unix; 0 = never (but the CLI always sets one)
	maxHops    int    // onward-delegation bound (0 = none)
}

// edgeGrantStore returns the ONE GrantStore for the current edge dir (the owner's issued-grants file,
// under the edge dir - not the authority dir, which stays Core-free). It is a per-path singleton so
// its mutex actually serialises concurrent read-modify-write on grants.json across the webui, TUI and
// CLI in THIS process. A fresh store per call would each carry its own mutex and a concurrent Put
// could lose an update. Re-created when the path changes (a test's temp config), keyed by that path.
// NOTE: this serialises in-process only; a separate CLI process writing grants.json concurrently
// with the daemon is not yet flock-guarded (grants are low-frequency owner actions; the atomic
// temp+rename prevents corruption, only a lost update). The grant-CRL slice will add the flock.
var (
	edgeGrantStoreMu   sync.Mutex
	edgeGrantStoreInst *edge.GrantStore
	edgeGrantStorePath string
)

func edgeGrantStore() *edge.GrantStore {
	edgeGrantStoreMu.Lock()
	defer edgeGrantStoreMu.Unlock()
	p := edgeGrantsPath()
	if edgeGrantStoreInst == nil || edgeGrantStorePath != p {
		edgeGrantStoreInst = &edge.GrantStore{Path: p}
		edgeGrantStorePath = p
	}
	return edgeGrantStoreInst
}

func edgeGrantsPath() string { return edgeAuthDir() + "/grants.json" }

// edgeNewGrantID mints a random, collision-resistant grant id.
func edgeNewGrantID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "grant_" + hex.EncodeToString(b)
}

// edgeIssueGrant is the pure core: validate the capability, build the record, sign it under the
// authority's root, and store it. It contacts nothing and reaches no fleet - the signature is what a
// member checks, offline.
func edgeIssueGrant(st *edge.GrantStore, owner string, key crypto.Signer, spec edgeGrantSpec, now time.Time) (store.Grant, error) {
	if !edge.Capability(spec.capability).Valid() {
		return store.Grant{}, fmt.Errorf("%q is not an edge capability (one of: serve classify sense actuate relay operate)", spec.capability)
	}
	if strings.TrimSpace(spec.callee) == "" {
		return store.Grant{}, fmt.Errorf("a grant needs a callee - the node it authorizes acting on")
	}
	if spec.expiresAt != 0 && spec.expiresAt <= now.Unix() {
		return store.Grant{}, fmt.Errorf("that grant would already be expired - give --for a future duration like 7d")
	}
	g := store.Grant{
		ID:        edgeNewGrantID(),
		Owner:     owner,
		Caller:    spec.caller,
		Nodes:     []string{spec.callee},
		Caps:      []string{spec.capability},
		MaxHops:   spec.maxHops,
		ExpiresAt: spec.expiresAt,
		CreatedAt: now.Unix(),
		Self:      spec.caller == "", // an owner-to-own-instances grant is a Self grant: always $0
	}
	signed, err := edge.SignGrant(g, key)
	if err != nil {
		return store.Grant{}, fmt.Errorf("the authority could not sign that grant: %w", err)
	}
	if err := st.Put(signed); err != nil {
		return store.Grant{}, err
	}
	return signed, nil
}

// cmdEdgeGrant is the group: no args lists; "revoke <id>" revokes; otherwise it issues.
func cmdEdgeGrant(cfg config, args []string) error {
	if len(args) == 0 {
		return cmdEdgeGrantList()
	}
	switch args[0] {
	case "revoke", "rm":
		if len(args) < 2 {
			return usagef("usage: roger edge grant revoke <id>")
		}
		return cmdEdgeGrantRevoke(args[1])
	case "list", "ls":
		return cmdEdgeGrantList()
	}
	return cmdEdgeGrantIssue(cfg, args)
}

// cmdEdgeGrantIssue: roger edge grant <caller> <capability> <callee> [--for 7d] [--hops N]
func cmdEdgeGrantIssue(cfg config, args []string) error {
	// Pull the flags out of the positionals by hand: caller/cap/callee come first, --for/--hops after.
	var pos []string
	forDur, hops := "30d", 0
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--for", "-for":
			if i+1 >= len(args) {
				return usagef("--for needs a value, e.g. --for 7d")
			}
			forDur = args[i+1]
			i++
		case "--hops", "-hops":
			if i+1 >= len(args) {
				return usagef("--hops needs a number")
			}
			if _, err := fmt.Sscanf(args[i+1], "%d", &hops); err != nil || hops < 0 {
				return usagef("--hops wants a non-negative number, got %q", args[i+1])
			}
			i++
		default:
			pos = append(pos, args[i])
		}
	}
	if len(pos) != 3 {
		return usagef("usage: roger edge grant <caller> <capability> <callee> [--for 7d] [--hops N]\n" +
			"  e.g. roger edge grant laptop/desk operate workstation/agent --for 7d")
	}
	callerArg, capArg, calleeArg := pos[0], pos[1], pos[2]

	g, line, err := edgeGrantIssueCore(callerArg, capArg, calleeArg, forDur, hops)
	if err != nil {
		return err
	}
	fmt.Printf("issued %s\n", g.ID)
	fmt.Printf("  %s\n", line)
	fmt.Printf("  signed by this Edge's authority; any member verifies it with the public root alone\n")
	return nil
}

// edgeGrantIssueCore issues a grant from addresses as typed - shared by the CLI and the fleet
// screen, so both do exactly the same thing. It returns the grant and its one-line description.
func edgeGrantIssueCore(callerArg, capArg, calleeArg, forDur string, hops int) (store.Grant, string, error) {
	local, hasRoot, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil {
		return store.Grant{}, "", err
	}
	if !hasRoot {
		return store.Grant{}, "", fmt.Errorf("this machine is not the authority of a locally-rooted Edge; grants are issued where the root key lives (run `roger edge authority local <name>` to designate one)")
	}
	callerID, err := edgeGrantResolveNode(callerArg, true)
	if err != nil {
		return store.Grant{}, "", err
	}
	calleeID, err := edgeGrantResolveNode(calleeArg, false)
	if err != nil {
		return store.Grant{}, "", err
	}
	expiresAt, err := parseExpires(forDur)
	if err != nil {
		// parseExpires is shared with `roger grant` and words its error for --expires; this command's
		// flag is --for, so re-word it here rather than send the user chasing a flag that does not exist.
		return store.Grant{}, "", fmt.Errorf("bad --for %q, want e.g. 7d or 2026-12-31", forDur)
	}
	g, err := edgeIssueGrant(edgeGrantStore(), local.Account(), local.Authority().RootKey(),
		edgeGrantSpec{caller: callerID, callee: calleeID, capability: capArg, expiresAt: expiresAt, maxHops: hops}, time.Now())
	if err != nil {
		return store.Grant{}, "", err
	}
	line := fmt.Sprintf("%s may %s %s until %s", edgeGrantWho(callerID), capArg, calleeArg, time.Unix(g.ExpiresAt, 0).Format("2006-01-02 15:04"))
	return g, line, nil
}

// edgeGrantResolveNode turns a caller/callee ADDRESS into a node id. A caller may be empty-meaning
// "any of the owner's own instances" only when it is literally "self" or "-"; otherwise the address
// must name a node on this Edge. The instance part (node/instance) is dropped: a grant scopes to the
// node.
func edgeGrantResolveNode(addr string, isCaller bool) (string, error) {
	addr = strings.TrimSpace(addr)
	if isCaller && (addr == "self" || addr == "-" || addr == "") {
		return "", nil // a Self grant: any of the owner's own instances
	}
	node := addr
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		node = addr[:i]
	}
	// "." and "self" mean THIS machine wherever a node is named - a grant's callee, an alert rule's
	// scope, an expectation's, a schedule's. The face resolver has said so for a while; this is the
	// same promise for everything that names a node by way of the fleet.
	if node == "." || (node == "self" && !isCaller) {
		if id := edgeThisNodeID(); id != "" {
			return id, nil
		}
	}
	st, err := loadEdgeState()
	if err != nil {
		return "", err
	}
	nodes, err := st.list()
	if err != nil {
		return "", err
	}
	n, ok, err := edgeResolve(nodes, node)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errNoSuchNode(node)
	}
	return n.ID, nil
}

// edgeGrantWho renders a caller id for a line: a Self grant reads as the owner's own instances.
func edgeGrantWho(callerID string) string {
	if callerID == "" {
		return "your own instances"
	}
	return edgeShortID(callerID)
}

func cmdEdgeGrantList() error {
	gs, err := edgeGrantStore().List()
	if err != nil {
		return err
	}
	if len(gs) == 0 {
		fmt.Println("no edge grants issued yet - `roger edge grant <caller> <capability> <callee>` issues one")
		return nil
	}
	now := time.Now()
	for _, g := range gs {
		callee := ""
		if len(g.Nodes) > 0 {
			callee = edgeShortID(g.Nodes[0])
		}
		state := "live"
		switch {
		case g.Revoked:
			state = "REVOKED"
		case g.Expired(now):
			state = "expired"
		}
		fmt.Printf("%s  %-8s  %s may %s %s  (until %s)\n",
			g.ID, state, edgeGrantWho(g.Caller), strings.Join(g.Caps, ","), callee,
			time.Unix(g.ExpiresAt, 0).Format("2006-01-02"))
	}
	return nil
}

func cmdEdgeGrantRevoke(id string) error {
	found, err := edgeGrantStore().Revoke(id)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no such grant %q (run `roger edge grant` to list)", id)
	}
	fmt.Printf("revoked %s\n", id)
	// Be honest about reach: today only the issuing authority's own CLI and its own node honor this at
	// invoke time (the caller here skips revoked grants, and a same-node callee reads this store). A
	// callee on ANOTHER machine will keep honoring a held copy until the grant expires, until the
	// distributed grant-CRL slice lands.
	fmt.Println("  (enforced by this authority and its own node now; other machines honor it at the grant's expiry until the grant-CRL ships)")
	return nil
}

// edgeTrustedRevocation returns g with its revocation set from a source the callee TRUSTS - this
// machine's authority grant store - never from the wire value, which is unsigned and so freely forged
// by a caller holding a revoked grant. A grant this node did not issue has no local standing here
// (its cross-node revocation rides a distributed grant-CRL, a later slice); such a grant keeps
// whatever the signed fields say, still bounded by expiry and by node-level cert revocation.
func edgeTrustedRevocation(st *edge.GrantStore, g store.Grant) (store.Grant, error) {
	g.Revoked = false
	rec, ok, err := st.Get(g.ID)
	if err != nil {
		// The store could not be read, so we cannot confirm the grant is still live. Fail CLOSED:
		// return the error so Authorize refuses rather than honoring a possibly-revoked grant.
		return store.Grant{}, fmt.Errorf("could not read this Edge's grant store to check revocation: %w", err)
	}
	if ok {
		g.Revoked = rec.Revoked
	}
	return g, nil
}
