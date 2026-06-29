package main

// grants_bdd_test.go makes features/grants/grants.feature an EXECUTABLE Cucumber suite,
// driving the REAL grant path: store.CreateGrant/GrantBySecretHash/SetGrantRevoked/
// UpdateGrant(applyPatch)/AddGrantUsage and the broker resolveGrantToken (sha256 lookup,
// revoked/expired rejection, owner-node ∩ allow-list, model allow-list) + grantCapCheck
// (daily/monthly token caps, unlimited=0). Every assertion reads STORE/broker state back
// (only the secret HASH is persisted; resolution finds it; a revoked/expired key is refused;
// a patch leaves nil fields untouched).

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/store"
)

type grState struct {
	db        *store.Mem
	b         *broker
	owner     string
	secret    string
	hash      string
	grant     store.Grant
	created   store.Grant // snapshot at create, to verify a patch leaves fields unchanged
	gc        grantContext
	resolveOK bool
	resolveErr string
	capStat   int
	capMsg    string
	useNode   string
	useModel  string
}

func grBoolPtr(b bool) *bool { return &b }

func (s *grState) reset() {
	s.db = store.NewMem()
	_, priv, _ := ed25519.GenerateKey(nil)
	s.b = buildBroker(s.db, priv, 0.30, 0, time.Hour)
	s.owner = "op1"
	s.secret, s.hash = "", ""
	s.grant = store.Grant{}
	s.created = store.Grant{}
	s.gc = grantContext{}
	s.resolveOK, s.resolveErr = false, ""
	s.capStat, s.capMsg = 0, ""
	s.useNode, s.useModel = "", ""
}

func (s *grState) freshStore() error { s.reset(); return nil }

// mkGrant creates a grant with the given fields, generating a secret + storing only its hash.
func (s *grState) mkGrant(label string, g store.Grant) error {
	s.secret = grantPrefix + "secret_" + label
	sum := sha256.Sum256([]byte(s.secret))
	s.hash = hex.EncodeToString(sum[:])
	g.ID = "grant_" + label
	g.SecretHash = s.hash
	g.Owner = s.owner
	g.Label = label
	g.CreatedAt = time.Now().Unix()
	if err := s.db.CreateGrant(g); err != nil {
		return err
	}
	s.grant = g
	s.created = g
	return nil
}

// --- Given / setup ----------------------------------------------------------

func (s *grState) mintsGrant(label string) error { return s.mkGrant(label, store.Grant{}) }
func (s *grState) grantExists(label string) error { return s.mkGrant(label, store.Grant{}) }

func (s *grState) revokedGrant() error {
	if err := s.mkGrant("revoked", store.Grant{}); err != nil {
		return err
	}
	_, err := s.db.SetGrantRevoked(s.grant.ID, s.owner, true)
	return err
}

func (s *grState) expiredGrant() error {
	return s.mkGrant("expired", store.Grant{ExpiresAt: time.Now().Add(-time.Hour).Unix()})
}

func (s *grState) grantDailyCap(v string) error {
	n, err := capParseTokens(v)
	if err != nil {
		return err
	}
	return s.mkGrant("capped", store.Grant{DailyCap: n})
}

func (s *grState) grantUnlimited() error {
	return s.mkGrant("unlimited", store.Grant{DailyCap: 0, MonthlyCap: 0})
}

func (s *grState) grantRestricted(node, model string) error {
	if err := s.db.BindNode(node, s.owner); err != nil {
		return err
	}
	return s.mkGrant("scoped", store.Grant{Nodes: []string{node}, Models: []string{model}})
}

func (s *grState) grantFree() error { return s.mkGrant("free", store.Grant{Free: true}) }

func (s *grState) existingGrant() error {
	return s.mkGrant("editable", store.Grant{
		Nodes: []string{"n1"}, Models: []string{"m1"}, PriceIn: 2, PriceOut: 3,
		DailyCap: 1000, MonthlyCap: 30000, RPM: 60, Burst: 5,
	})
}

func capParseTokens(v string) (int64, error) {
	// accepts "100k" or a plain integer
	if len(v) > 1 && (v[len(v)-1] == 'k' || v[len(v)-1] == 'K') {
		n, err := strconv.ParseInt(v[:len(v)-1], 10, 64)
		return n * 1000, err
	}
	return strconv.ParseInt(v, 10, 64)
}

// --- When -------------------------------------------------------------------

func (s *grState) presentsSecret() error {
	s.gc, s.resolveOK, s.resolveErr = s.b.resolveGrantToken(s.secret)
	return nil
}

func (s *grState) usageReachesDailyCap() error {
	return s.db.AddGrantUsage(s.grant.ID, s.grant.DailyCap, time.Now())
}

func (s *grState) usesDifferentNodeOrModel() error {
	s.gc, s.resolveOK, s.resolveErr = s.b.resolveGrantToken(s.secret)
	s.useNode, s.useModel = "n_other", "model_other"
	return nil
}

func (s *grState) patchRevokedOnly() error {
	g, ok, err := s.db.UpdateGrant(s.grant.ID, s.owner, store.GrantPatch{Revoked: grBoolPtr(true)})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("UpdateGrant did not find grant %q", s.grant.ID)
	}
	s.grant = g
	return nil
}

// --- Then -------------------------------------------------------------------

func (s *grState) secretShownOnce() error {
	if s.secret == "" {
		return fmt.Errorf("no grant secret was produced")
	}
	return nil
}

func (s *grState) onlyHashPersisted() error {
	g, found, err := s.db.GrantBySecretHash(s.hash)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("grant not resolvable by its secret hash")
	}
	if g.SecretHash != s.hash {
		return fmt.Errorf("stored secret_hash = %q, want %q", g.SecretHash, s.hash)
	}
	// The PLAINTEXT secret must NOT resolve a grant (only its sha256 is stored).
	if _, foundPlain, _ := s.db.GrantBySecretHash(s.secret); foundPlain {
		return fmt.Errorf("the plaintext secret resolved a grant - it must never be stored")
	}
	return nil
}

func (s *grState) resolvesAndServed() error {
	if !s.resolveOK {
		return fmt.Errorf("grant did not resolve (err %q)", s.resolveErr)
	}
	if s.gc.grant.ID != s.grant.ID {
		return fmt.Errorf("resolved grant id = %q, want %q", s.gc.grant.ID, s.grant.ID)
	}
	return nil
}

func (s *grState) rejectedRevoked() error {
	if s.resolveOK {
		return fmt.Errorf("revoked grant resolved, want rejected")
	}
	if s.resolveErr == "" {
		return fmt.Errorf("expected a rejection message for the revoked grant")
	}
	return nil
}

func (s *grState) rejectedExpired() error {
	if s.resolveOK {
		return fmt.Errorf("expired grant resolved, want rejected")
	}
	if s.resolveErr == "" {
		return fmt.Errorf("expected a rejection message for the expired grant")
	}
	return nil
}

func (s *grState) refusedUntilNextDay() error {
	st, _ := s.b.grantCapCheck(s.grant)
	if st != http.StatusTooManyRequests {
		return fmt.Errorf("grant cap check = %d, want 429 (daily cap reached)", st)
	}
	return nil
}

func (s *grState) monthlyEnforcedSameWay() error {
	g := store.Grant{ID: "grant_monthly", SecretHash: "h_m", Owner: s.owner, MonthlyCap: 30000}
	if err := s.db.CreateGrant(g); err != nil {
		return err
	}
	if err := s.db.AddGrantUsage(g.ID, 30000, time.Now()); err != nil {
		return err
	}
	st, msg := s.b.grantCapCheck(g)
	if st != http.StatusTooManyRequests {
		return fmt.Errorf("monthly cap check = %d %q, want 429", st, msg)
	}
	return nil
}

func (s *grState) volumeNeverRefuses() error {
	if err := s.db.AddGrantUsage(s.grant.ID, 100_000_000, time.Now()); err != nil {
		return err
	}
	st, _ := s.b.grantCapCheck(s.grant)
	if st != 0 {
		return fmt.Errorf("unlimited grant refused (%d) on volume, want allowed", st)
	}
	return nil
}

func (s *grState) refusedOutsideAllowList() error {
	if !s.resolveOK {
		return fmt.Errorf("grant did not resolve (err %q)", s.resolveErr)
	}
	if s.gc.nodeAllow[s.useNode] {
		return fmt.Errorf("node %q is in the allow-list but should be outside it", s.useNode)
	}
	if !s.gc.modelDenied(s.useModel) {
		return fmt.Errorf("model %q was allowed but should be outside the grant's model allow-list", s.useModel)
	}
	return nil
}

func (s *grState) costsNothing() error {
	if !s.grant.Free {
		return fmt.Errorf("grant is not marked free")
	}
	return nil // Free => the relay skips the wallet debit entirely (price 0/0)
}

func (s *grState) onlyRevocationChanged() error {
	if !s.grant.Revoked {
		return fmt.Errorf("patch did not set Revoked")
	}
	c := s.created
	g := s.grant
	if g.DailyCap != c.DailyCap || g.MonthlyCap != c.MonthlyCap || g.PriceIn != c.PriceIn || g.PriceOut != c.PriceOut || g.RPM != c.RPM || g.Burst != c.Burst {
		return fmt.Errorf("a nil patch field changed: caps/prices/rate differ from the original")
	}
	if len(g.Nodes) != len(c.Nodes) || (len(g.Nodes) > 0 && g.Nodes[0] != c.Nodes[0]) {
		return fmt.Errorf("the node allow-list changed under a nil patch field")
	}
	if len(g.Models) != len(c.Models) || (len(g.Models) > 0 && g.Models[0] != c.Models[0]) {
		return fmt.Errorf("the model allow-list changed under a nil patch field")
	}
	return nil
}

func TestGrantsBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &grState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^an operator mints a grant labelled "([^"]*)"$`, st.mintsGrant)
			sc.Step(`^a grant exists for "([^"]*)"$`, st.grantExists)
			sc.Step(`^a grant that the owner has revoked$`, st.revokedGrant)
			sc.Step(`^a grant whose ExpiresAt is in the past$`, st.expiredGrant)
			sc.Step(`^a grant with a DailyCap of ([\dkK]+) tokens$`, st.grantDailyCap)
			sc.Step(`^a grant with DailyCap 0 and MonthlyCap 0$`, st.grantUnlimited)
			sc.Step(`^a grant restricted to node "([^"]*)" and model "([^"]*)"$`, st.grantRestricted)
			sc.Step(`^a grant marked free$`, st.grantFree)
			sc.Step(`^an existing grant$`, st.existingGrant)

			sc.Step(`^a bot presents the grant secret$`, st.presentsSecret)
			sc.Step(`^a bot presents its secret$`, st.presentsSecret)
			sc.Step(`^usage reaches the daily cap$`, st.usageReachesDailyCap)
			sc.Step(`^a bot uses it for a different node or model$`, st.usesDifferentNodeOrModel)
			sc.Step(`^the owner PATCHes only Revoked=true \(other fields nil\)$`, st.patchRevokedOnly)

			sc.Step(`^a grant secret is shown ONCE$`, st.secretShownOnce)
			sc.Step(`^only sha256\(secret\) \(secret_hash\) is persisted, never the secret$`, st.onlyHashPersisted)
			sc.Step(`^GrantBySecretHash resolves it and the request is served under that grant$`, st.resolvesAndServed)
			sc.Step(`^the request is rejected \(revoked grants serve nothing\)$`, st.rejectedRevoked)
			sc.Step(`^the request is rejected \(expired\)$`, st.rejectedExpired)
			sc.Step(`^further requests on that grant are refused until the next UTC day$`, st.refusedUntilNextDay)
			sc.Step(`^a MonthlyCap is enforced the same way across the month$`, st.monthlyEnforcedSameWay)
			sc.Step(`^token volume alone never refuses it$`, st.volumeNeverRefuses)
			sc.Step(`^the request is refused \(outside the grant's allow-list\)$`, st.refusedOutsideAllowList)
			sc.Step(`^requests through it cost the owner/bot nothing$`, st.costsNothing)
			sc.Step(`^only revocation changes; caps, prices, and allow-lists are untouched$`, st.onlyRevocationChanged)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/grants/grants.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("grants behavior scenarios failed (see godog output above)")
	}
}
