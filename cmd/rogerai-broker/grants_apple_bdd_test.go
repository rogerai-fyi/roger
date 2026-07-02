package main

// grants_apple_bdd_test.go makes features/grants/apple_owner_management.feature EXECUTABLE:
// the founder-approved contract (roger-ios docs/EXTERNAL-READINESS.md §2) that grant MANAGEMENT
// accepts ANY bound, non-anonymized owner (GitHubID != 0 OR AppleSub != "", pubkey-bound) while
// payouts stay behind the GitHub/KYC gate, and an Apple owner's grants are funded by the Apple
// wallet (GitHub-wins precedence pinned for dual-bound owners). NO mocks: the REAL b.grants /
// b.grantByID / b.connectStatus handlers run against store.NewMem() with real signed requests.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/store"
)

// gaOwner is one test identity: its signing key + how it is bound.
type gaOwner struct {
	priv   ed25519.PrivateKey
	pubHex string
	wallet string // the ACCOUNT wallet its grants should be funded by
}

type gaState struct {
	b      *broker
	mem    *store.Mem
	owners map[string]*gaOwner

	code    int
	body    string
	errMsg  string
	secret  string // the one-time grant secret from the last mint
	grantID string // the id from the last mint / the seeded grant
}

func (s *gaState) reset() error {
	s.mem = store.NewMem()
	s.b = relayBroker(s.mem)
	s.owners = map[string]*gaOwner{}
	s.code, s.body, s.errMsg, s.secret, s.grantID = 0, "", "", "", ""
	return nil
}

func (s *gaState) newOwner(name string) *gaOwner {
	pub, priv, _ := ed25519.GenerateKey(nil)
	o := &gaOwner{priv: priv, pubHex: hex.EncodeToString(pub)}
	s.owners[name] = o
	return o
}

// --- Background / Givens ---------------------------------------------------------------

func (s *gaState) appleOwner(name string, dollars float64) error {
	o := s.newOwner(name)
	sub := "apple-sub-" + name
	if err := s.mem.BindOwner(store.Owner{AppleSub: sub, Pubkey: o.pubHex}); err != nil {
		return err
	}
	o.wallet = walletForAppleSub(sub)
	_, err := s.mem.AddCredits(o.wallet, dollars)
	return err
}

func (s *gaState) githubOwner(name string, dollars float64) error {
	o := s.newOwner(name)
	if err := s.mem.BindOwner(store.Owner{GitHubID: 71, Login: name, Pubkey: o.pubHex}); err != nil {
		return err
	}
	o.wallet = "u_gh_71"
	_, err := s.mem.AddCredits(o.wallet, dollars)
	return err
}

func (s *gaState) dualOwner(name string) error {
	o := s.newOwner(name)
	if err := s.mem.BindOwner(store.Owner{GitHubID: 72, Login: name, AppleSub: "apple-sub-" + name, Pubkey: o.pubHex}); err != nil {
		return err
	}
	o.wallet = "u_gh_72"
	_, err := s.mem.AddCredits(o.wallet, 20)
	return err
}

func (s *gaState) seededGrant(name, id string) error {
	o := s.owners[name]
	if o == nil {
		return fmt.Errorf("unknown owner %q", name)
	}
	s.grantID = id
	return s.mem.CreateGrant(store.Grant{ID: id, SecretHash: secretHash("rog-grant_" + id), Owner: o.pubHex, Free: true, Label: id})
}

func (s *gaState) anonymized(name string) error {
	o := s.owners[name]
	if o == nil {
		return fmt.Errorf("unknown owner %q", name)
	}
	rec, ok, err := s.mem.OwnerByPubkey(o.pubHex)
	if err != nil || !ok {
		return fmt.Errorf("owner %q not bound", name)
	}
	// Anonymize via the store's DeleteAccount (BindOwner deliberately preserves the existing
	// Anonymized flag, so a re-bind can't set it). NOTE: this is a defense-in-depth pin, not a
	// production flow — accountDelete REFUSES an empty login with 409, so an Apple-only owner
	// currently has NO in-app anonymize path at all; DeleteAccount("") works here only because
	// apple-ana is the sole empty-login owner in the scenario.
	done, err := s.mem.DeleteAccount(rec.Login)
	if err != nil || !done {
		return fmt.Errorf("DeleteAccount(%q) = %v, %v", rec.Login, done, err)
	}
	return nil
}

// --- Whens -------------------------------------------------------------------------------

// send issues a REAL signed request at the grants (or payout) handler and captures the result.
func (s *gaState) send(name, method, path string, payload []byte, sign bool) error {
	r := httptest.NewRequest(method, path, strings.NewReader(string(payload)))
	if sign {
		o := s.owners[name]
		if o == nil {
			return fmt.Errorf("unknown owner %q", name)
		}
		signReq(r, o.priv, payload)
	}
	w := httptest.NewRecorder()
	switch {
	case strings.HasPrefix(path, "/grants"):
		s.b.grants(w, r)
	case strings.HasPrefix(path, "/connect/status"):
		s.b.connectStatus(w, r)
	default:
		return fmt.Errorf("unroutable path %q", path)
	}
	s.code = w.Code
	s.body = w.Body.String()
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(w.Body.Bytes(), &e) == nil {
		s.errMsg = e.Error.Message
	}
	var mint struct {
		Secret string `json:"secret"`
		Grant  struct {
			ID string `json:"id"`
		} `json:"grant"`
	}
	if json.Unmarshal(w.Body.Bytes(), &mint) == nil && mint.Secret != "" {
		s.secret, s.grantID = mint.Secret, mint.Grant.ID
	}
	return nil
}

func (s *gaState) mint(name, label string) error {
	return s.send(name, http.MethodPost, "/grants", []byte(fmt.Sprintf(`{"name":%q}`, label)), true)
}
func (s *gaState) list(name string) error {
	return s.send(name, http.MethodGet, "/grants", nil, true)
}
func (s *gaState) show(name, id string) error {
	return s.send(name, http.MethodGet, "/grants/"+id, nil, true)
}
func (s *gaState) revoke(name, id string) error {
	return s.send(name, http.MethodDelete, "/grants/"+id, nil, true)
}
func (s *gaState) patch(name, id string) error {
	return s.send(name, http.MethodPatch, "/grants/"+id, []byte(`{"monthly_cap":1000}`), true)
}
func (s *gaState) anonKeypairMint() error {
	s.newOwner("anon") // signed, but never bound
	return s.send("anon", http.MethodPost, "/grants", []byte(`{"name":"x"}`), true)
}
func (s *gaState) unsignedMint() error {
	return s.send("", http.MethodPost, "/grants", []byte(`{"name":"x"}`), false)
}
func (s *gaState) payoutStatus(name string) error {
	return s.send(name, http.MethodGet, "/connect/status", nil, true)
}

// --- Thens -------------------------------------------------------------------------------

func (s *gaState) statusIs(want int) error {
	if s.code != want {
		return fmt.Errorf("status = %d, want %d (body %s)", s.code, want, s.body)
	}
	return nil
}
func (s *gaState) statusOneOf(a, bcode int) error {
	if s.code != a && s.code != bcode {
		return fmt.Errorf("status = %d, want %d or %d (body %s)", s.code, a, bcode, s.body)
	}
	return nil
}
func (s *gaState) carriesSecret() error {
	if s.secret == "" {
		return fmt.Errorf("no one-time grant secret in the response: %s", s.body)
	}
	return nil
}
func (s *gaState) recordedUnderPubkey(name string) error {
	o := s.owners[name]
	list, err := s.mem.GrantsByOwner(o.pubHex)
	if err != nil || len(list) == 0 {
		return fmt.Errorf("no grants recorded under %s's pubkey (err %v)", name, err)
	}
	return nil
}
func (s *gaState) listContains(id string) error {
	if !strings.Contains(s.body, id) {
		return fmt.Errorf("list response does not contain %q: %s", id, s.body)
	}
	return nil
}
func (s *gaState) grantRevoked(id string) error {
	for _, name := range []string{"apple-ana", "gina", "dana"} {
		if o := s.owners[name]; o != nil {
			list, _ := s.mem.GrantsByOwner(o.pubHex)
			for _, g := range list {
				if g.ID == id {
					if !g.Revoked {
						return fmt.Errorf("grant %s is not revoked", id)
					}
					return nil
				}
			}
		}
	}
	return fmt.Errorf("grant %s not found", id)
}
func (s *gaState) noGitHubMention() error {
	if strings.Contains(strings.ToLower(s.body), "github") {
		return fmt.Errorf("response mentions GitHub: %s", s.body)
	}
	return nil
}

// funderWalletIs asserts the SPONSOR wallet a spend through the minted grant bills — via the
// REAL resolutions a grant-key request uses: resolveGrantToken (the grant must resolve at all)
// and ownerSponsorWallet (the money key the relay charges for a sponsored grant, which routes
// through accountWalletForOwner and its GitHub-wins precedence).
func (s *gaState) funderWalletIs(name, kind string) error {
	if s.secret == "" {
		return fmt.Errorf("no minted secret to resolve")
	}
	gc, ok, gerr := s.b.resolveGrantToken(s.secret)
	if !ok {
		return fmt.Errorf("minted grant does not resolve: %s", gerr)
	}
	sponsor := s.b.ownerSponsorWallet(gc.grant.Owner)
	o := s.owners[name]
	if sponsor != o.wallet {
		return fmt.Errorf("sponsor wallet = %q, want %s's %q", sponsor, name, o.wallet)
	}
	var wantPrefix string
	switch kind {
	case "u_apple":
		wantPrefix = "u_apple_"
	case "u_gh":
		wantPrefix = "u_gh_"
	}
	if !strings.HasPrefix(sponsor, wantPrefix) {
		return fmt.Errorf("sponsor wallet %q is not a %s wallet", sponsor, kind)
	}
	return nil
}

func (s *gaState) payoutRefusedNoGitHub() error {
	if s.code != http.StatusUnauthorized && s.code != http.StatusForbidden {
		return fmt.Errorf("payout surface status = %d, want 401/403 (body %s)", s.code, s.body)
	}
	return nil
}

func TestGrantsAppleOwnerBDD(t *testing.T) {
	st := &gaState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { return ctx, st.reset() })

			sc.Step(`^a broker with a bound APPLE owner "([^"]*)" \(AppleSub set, no GitHub\) whose wallet holds \$([0-9.]+)$`, st.appleOwner)
			sc.Step(`^a bound GITHUB owner "([^"]*)" \(GitHubID set\) whose wallet holds \$([0-9.]+)$`, st.githubOwner)
			sc.Step(`^a bound owner "([^"]*)" with BOTH GitHub and Apple identities$`, st.dualOwner)
			sc.Step(`^"([^"]*)" has a minted grant "([^"]*)"$`, st.seededGrant)
			sc.Step(`^"([^"]*)" has anonymized their account$`, st.anonymized)

			sc.Step(`^"([^"]*)" sends a signed POST /grants with name "([^"]*)"$`, st.mint)
			sc.Step(`^"([^"]*)" sends a signed GET /grants$`, st.list)
			sc.Step(`^"([^"]*)" sends a signed GET /grants/([\w-]+)$`, st.show)
			sc.Step(`^"([^"]*)" sends a signed DELETE /grants/([\w-]+)$`, st.revoke)
			sc.Step(`^"([^"]*)" sends a signed PATCH /grants/([\w-]+) setting a monthly cap$`, st.patch)
			sc.Step(`^an anonymous signed keypair sends POST /grants$`, st.anonKeypairMint)
			sc.Step(`^an unsigned POST /grants arrives$`, st.unsignedMint)
			sc.Step(`^"([^"]*)" sends a signed payout-status request$`, st.payoutStatus)

			sc.Step(`^the response status is (\d+)$`, st.statusIs)
			sc.Step(`^the response status is one of (\d+) or (\d+)$`, st.statusOneOf)
			sc.Step(`^the response carries a one-time grant secret$`, st.carriesSecret)
			sc.Step(`^the grant is recorded under "([^"]*)"'s pubkey$`, st.recordedUnderPubkey)
			sc.Step(`^the list contains "([^"]*)"$`, st.listContains)
			sc.Step(`^the grant "([^"]*)" is revoked$`, st.grantRevoked)
			sc.Step(`^the response does not mention GitHub$`, st.noGitHubMention)
			sc.Step(`^the minted grant's funder wallet is "([^"]*)"'s (u_apple|u_gh) wallet$`, st.funderWalletIs)
			sc.Step(`^the payout request is refused for lacking a GitHub-linked account$`, st.payoutRefusedNoGitHub)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/grants/apple_owner_management.feature"}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("grants/apple_owner_management behavior scenarios failed (see godog output above)")
	}
}
