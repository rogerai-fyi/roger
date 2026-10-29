package main

// owner_override_bdd_test.go makes features/categories/owner_override.feature EXECUTABLE. It
// drives the REAL owner-authenticated POST /provider/meta and /provider/models (signed as the
// owner), the REAL signed node registration (b.register - where overrides are re-applied),
// the public /discover computation, and the store - no seams. "the broker restarts" builds a
// fresh broker on the SAME store, as a deploy does.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cucumber/godog"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/store"
)

type ownerOverrideState struct {
	t      *testing.T
	b      *broker
	peer   *broker // a second instance over the same shared registry + store (multi-instance)
	mem    store.Store
	owners map[string]ed25519.PrivateKey
	nodes  map[string]ed25519.PrivateKey
	actor  string
	code   int
	body   []byte
	rows   []map[string]any
}

func (s *ownerOverrideState) freshBroker() {
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	s.b = &broker{
		db: s.mem, priv: brokerPriv,
		nodes: map[string]protocol.NodeRegistration{}, tunnels: map[string]*nodeTunnel{},
		lastSeen: map[string]time.Time{}, confidential: map[string]bool{}, private: map[string]bool{},
		bandOf: map[string]string{}, tps: map[string]float64{}, pubOfUser: map[string]string{},
		seedFunds: 100, localCache: map[string]localCacheEntry{}, probeSched: map[string]*probeState{},
	}
}

func (s *ownerOverrideState) owner(name string) error {
	if _, ok := s.owners[name]; !ok {
		_, priv, _ := ed25519.GenerateKey(nil)
		pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
		if err := s.mem.BindOwner(store.Owner{GitHubID: int64(len(s.owners) + 1), Login: name, Pubkey: pub}); err != nil {
			return err
		}
		s.owners[name] = priv
	}
	s.actor = name
	return nil
}

func pubHex(k ed25519.PrivateKey) string { return hex.EncodeToString(k.Public().(ed25519.PublicKey)) }

func parseTax(pair, src string) protocol.Taxonomy {
	cat, sub := protocol.SplitCategory(pair)
	return protocol.Taxonomy{Category: cat, Subcategory: sub, TaxonomySource: src}
}

// register posts a signed registration for node id offering one model, signed as owner (or
// unsigned when owner is "").
func (s *ownerOverrideState) register(id, owner, model, modality string, tx protocol.Taxonomy) error {
	nk, ok := s.nodes[id]
	if !ok {
		_, nk, _ = ed25519.GenerateKey(nil)
		s.nodes[id] = nk
	}
	reg := protocol.NodeRegistration{
		NodeID: id, PubKey: pubHex(nk), BridgeToken: "tok-" + id, TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: model, Modality: modality, Ctx: 4096, Taxonomy: tx}},
	}
	if modality == protocol.ModalityTTS {
		reg.Offers[0].Name = "Operator Voice" // a public voice needs a display name to register
	}
	reg.SignRegistration(nk)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	if owner != "" {
		signReq(r, s.owners[owner], body)
	}
	w := httptest.NewRecorder()
	s.b.register(w, r)
	if w.Code != http.StatusOK {
		return fmt.Errorf("register %s = %d: %s", id, w.Code, w.Body.String())
	}
	return nil
}

func (s *ownerOverrideState) aliceNode(id, model, pair string) error {
	return s.register(id, "alice", model, "", parseTax(pair, protocol.SourceInferred))
}
func (s *ownerOverrideState) aliceTTS(id, model string) error {
	return s.register(id, "alice", model, protocol.ModalityTTS, protocol.Taxonomy{})
}
func (s *ownerOverrideState) reregister(id, model, pair, src string) error {
	return s.register(id, "alice", model, "", parseTax(pair, src))
}
func (s *ownerOverrideState) aliceOffline(id string) error {
	return s.mem.BindNode(id, pubHex(s.owners["alice"]))
}
func (s *ownerOverrideState) unbound(id, model string) error {
	return s.register(id, "", model, "", protocol.Taxonomy{})
}
func (s *ownerOverrideState) restart() error { s.freshBroker(); return nil }

// secondInstance puts the deploy in multi-instance mode: two brokers over ONE shared
// registry (miniredis) and ONE store, n1 re-registered on the first, as in production.
func (s *ownerOverrideState) secondInstance() error {
	mr := miniredis.RunT(s.t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	s.b = newMIBroker(s.t, brokerPriv, s.mem, mr)
	s.peer = newMIBroker(s.t, brokerPriv, s.mem, mr)
	return s.aliceNode("n1", "qwen3-vl-8b", "language/chat")
}
func (s *ownerOverrideState) syncBoth() error {
	s.b.syncRegistry()
	s.peer.syncRegistry()
	return nil
}
func (s *ownerOverrideState) everyInstance(f func() error) error {
	for _, inst := range []*broker{s.b, s.peer} {
		cur := s.b
		s.b = inst
		err := f()
		s.b = cur
		if err != nil {
			return fmt.Errorf("instance %p: %w", inst, err)
		}
	}
	return nil
}
func (s *ownerOverrideState) registersTTS(id, model, tags string) error {
	return s.register(id, "alice", model, protocol.ModalityTTS,
		protocol.Taxonomy{Tags: strings.Split(tags, ","), TaxonomySource: protocol.SourceDeclared})
}
func (s *ownerOverrideState) offAir(id string) error {
	s.b.mu.Lock()
	s.b.lastSeen[id] = time.Now().Add(-2 * nodeTTL)
	s.b.mu.Unlock()
	return nil
}

// --- requests -----------------------------------------------------------------------

func (s *ownerOverrideState) send(path, method, who string, payload []byte) {
	r := httptest.NewRequest(method, path, bytes.NewReader(payload))
	if who != "" {
		if method == http.MethodGet {
			signReq(r, s.owners[who], nil)
		} else {
			signReq(r, s.owners[who], payload)
		}
	}
	w := httptest.NewRecorder()
	if strings.HasPrefix(path, "/provider/models") {
		s.b.providerModels(w, r)
	} else {
		s.b.providerMeta(w, r)
	}
	s.code, s.body = w.Code, w.Body.Bytes()
}

func (s *ownerOverrideState) postMeta(who string, m map[string]any) {
	body, _ := json.Marshal(m)
	s.send("/provider/meta", http.MethodPost, who, body)
}

func metaBody(node, model, pair, tags string) map[string]any {
	var t []string
	if tags != "" {
		t = strings.Split(tags, ",")
	}
	return map[string]any{"node": node, "model": model, "category": pair, "tags": t}
}

func (s *ownerOverrideState) postCatTags(who, node, model, pair, tags string) error {
	s.postMeta(who, metaBody(node, model, pair, tags))
	return nil
}
func (s *ownerOverrideState) postAll(who, node, model, pair, tags, tuning, base, policy, license string) error {
	m := metaBody(node, model, pair, tags)
	m["tuning"], m["base_model"], m["content_policy"], m["license"] = tuning, base, policy, license
	s.postMeta(who, m)
	return nil
}
func (s *ownerOverrideState) postPolicy(who, node, model, policy string) error {
	s.postMeta(who, map[string]any{"node": node, "model": model, "content_policy": policy})
	return nil
}
func (s *ownerOverrideState) postRaw(who, raw string) error {
	s.send("/provider/meta", http.MethodPost, who, []byte(raw))
	return nil
}
func (s *ownerOverrideState) anonymous(node, model, pair string) error {
	s.postMeta("", metaBody(node, model, pair, ""))
	return nil
}
func (s *ownerOverrideState) clear(who, node, model string) error {
	s.postMeta(who, map[string]any{"node": node, "model": model, "clear": true})
	return nil
}
func (s *ownerOverrideState) getMeta(who string) error {
	s.send("/provider/meta", http.MethodGet, who, nil)
	return nil
}
func (s *ownerOverrideState) setCategory(who, node, model, pair string) error {
	s.postMeta(who, metaBody(node, model, pair, ""))
	if s.code != http.StatusOK {
		return fmt.Errorf("setting the category = %d: %s", s.code, s.body)
	}
	return nil
}
func (s *ownerOverrideState) setPrice(who, node, model string, in, out float64) error {
	body, _ := json.Marshal(map[string]any{"node": node, "model": model, "price_in": in, "price_out": out})
	s.send("/provider/models", http.MethodPost, who, body)
	if s.code != http.StatusOK {
		return fmt.Errorf("setting the price = %d: %s", s.code, s.body)
	}
	return nil
}
func (s *ownerOverrideState) getModels(who string) error {
	s.send("/provider/models", http.MethodGet, who, nil)
	var v struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(s.body, &v); err != nil {
		return fmt.Errorf("decode /provider/models: %v (%s)", err, s.body)
	}
	s.rows = v.Models
	return nil
}

// --- assertions ---------------------------------------------------------------------

func (s *ownerOverrideState) status(code int) error {
	if s.code != code {
		return fmt.Errorf("status %d, got %d: %s", code, s.code, s.body)
	}
	return nil
}
func (s *ownerOverrideState) errMentions(want string) error {
	if !strings.Contains(string(s.body), want) {
		return fmt.Errorf("error %s does not mention %q", s.body, want)
	}
	return nil
}
func (s *ownerOverrideState) appliesLater() error {
	if !strings.Contains(string(s.body), "next time") {
		return fmt.Errorf("the response %s does not say it applies the next time the node is on air", s.body)
	}
	return nil
}

func (s *ownerOverrideState) offer(node, model string) (offerView, error) {
	res := s.b.computeDiscover().(map[string]any)
	for _, o := range res["offers"].([]offerView) {
		if o.NodeID == node && o.Model == model {
			return o, nil
		}
	}
	return offerView{}, fmt.Errorf("no public offer for %s %s", node, model)
}
func catPairOf(t protocol.Taxonomy) string {
	if t.Subcategory == "" {
		return t.Category
	}
	return t.Category + "/" + t.Subcategory
}
func (s *ownerOverrideState) offerHas(node, model, pair, tags, src string) error {
	o, err := s.offer(node, model)
	if err != nil {
		return err
	}
	got := catPairOf(o.Taxonomy) + "|" + strings.Join(o.Tags, ",") + "|" + o.TaxonomySource
	if want := pair + "|" + tags + "|" + src; got != want {
		return fmt.Errorf("offer %s %s: %q, got %q", node, model, want, got)
	}
	return nil
}
func (s *ownerOverrideState) offerFields(node, model, tuning, base, policy, license string) error {
	o, err := s.offer(node, model)
	if err != nil {
		return err
	}
	if o.Tuning != tuning || o.BaseModel != base || o.ContentPolicy != policy || o.License != license {
		return fmt.Errorf("offer fields %+v", o.Taxonomy)
	}
	return nil
}
func (s *ownerOverrideState) offerPolicy(node, model, policy string) error {
	o, err := s.offer(node, model)
	if err != nil {
		return err
	}
	if o.ContentPolicy != policy {
		return fmt.Errorf("offer content policy %q, got %q", policy, o.ContentPolicy)
	}
	return nil
}
func (s *ownerOverrideState) priced(node, model string, in, out float64) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	for _, o := range s.b.nodes[node].Offers {
		if o.Model == model {
			if o.PriceIn != in || o.PriceOut != out {
				return fmt.Errorf("priced %v/%v, got %v/%v", in, out, o.PriceIn, o.PriceOut)
			}
			return nil
		}
	}
	return fmt.Errorf("no live offer %s %s", node, model)
}
func (s *ownerOverrideState) stored(node, model, pair, tags string) error {
	ov, ok, err := s.mem.MetaOverride(node, model)
	if err != nil || !ok {
		return fmt.Errorf("no stored override for %s %s (err %v)", node, model, err)
	}
	if got := catPairOf(ov.Taxonomy) + "|" + strings.Join(ov.Taxonomy.Tags, ","); got != pair+"|"+tags {
		return fmt.Errorf("stored %q, want %q", got, pair+"|"+tags)
	}
	return nil
}
func (s *ownerOverrideState) noStored(node, model string) error {
	if ov, ok, _ := s.mem.MetaOverride(node, model); ok {
		return fmt.Errorf("a stored override exists for %s %s: %+v", node, model, ov)
	}
	return nil
}
func (s *ownerOverrideState) storedPrice(node, model string, in, out float64) error {
	ov, ok, _ := s.mem.OfferOverride(node, model)
	if !ok || ov.PriceIn != in || ov.PriceOut != out {
		return fmt.Errorf("stored price override %+v ok=%v, want %v/%v", ov, ok, in, out)
	}
	return nil
}
func (s *ownerOverrideState) row(node, model, pair, src, not string) error {
	for _, r := range s.rows {
		if r["node"] != node || r["model"] != model {
			continue
		}
		cat, _ := r["category"].(string)
		if sub, _ := r["subcategory"].(string); sub != "" {
			cat += "/" + sub
		}
		gotSrc, _ := r["taxonomy_source"].(string)
		over, _ := r["meta_overridden"].(bool)
		if cat != pair || gotSrc != src || over != (not == "") {
			return fmt.Errorf("row %s %s: %q/%q overridden=%v (%v)", node, model, cat, gotSrc, over, r)
		}
		return nil
	}
	return fmt.Errorf("no /provider/models row for %s %s", node, model)
}

// stillScreened runs the offer the override produced through the REAL relay + screen (the
// policy_honesty harness): a flagged prompt to it must be blocked before any dispatch.
func (s *ownerOverrideState) stillScreened(model string) error {
	o, err := s.offer("n1", model)
	if err != nil {
		return err
	}
	ops := &honestyState{opsState: &opsState{t: s.t, logs: &lockedBuffer{}}}
	ops.logOut, ops.logFl = log.Writer(), log.Flags()
	log.SetOutput(ops.logs)
	defer func() { ops.closeAll(); log.SetOutput(ops.logOut); log.SetFlags(ops.logFl) }()
	ops.reset()
	if err := ops.brokerRequired("sync"); err != nil {
		return err
	}
	ops.model = model
	if err := ops.declare(o.Taxonomy); err != nil {
		return err
	}
	_ = ops.verdict("unsafe S1")
	_ = ops.sendsChat(model)
	if err := ops.statusIs(451); err != nil {
		return err
	}
	return ops.neverReceives()
}

func TestOwnerOverrideBDD(t *testing.T) {
	for _, k := range []string{"MODERATION_PROVIDER", "MODERATION_URL", "MODERATION_GROQ_KEY", "GROQ_API_KEY",
		"ROGERAI_REQUIRE_MODERATION", "ROGERAI_CSAM_CATEGORIES", "MODERATION_MODEL", "ROGERAI_MODERATION_MODE"} {
		t.Setenv(k, "")
	}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &ownerOverrideState{t: t}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = ownerOverrideState{t: t, mem: store.NewMem(),
					owners: map[string]ed25519.PrivateKey{}, nodes: map[string]ed25519.PrivateKey{}}
				st.freshBroker()
				return ctx, nil
			})
			q := `"([^"]*)"`
			f := `([\d.]+)`
			num := func(v string) float64 { n, _ := strconv.ParseFloat(v, 64); return n }
			sc.Step(`^owner `+q+` is signed in$`, st.owner)
			sc.Step(`^alice's node `+q+` is on air serving `+q+` with its own taxonomy `+q+` inferred$`, st.aliceNode)
			sc.Step(`^alice's node `+q+` is on air serving `+q+` as a tts model$`, st.aliceTTS)
			sc.Step(`^node `+q+` (?:re-)?registers serving `+q+` with its own taxonomy `+q+` (declared|inferred)$`, st.reregister)
			sc.Step(`^alice also owns node `+q+` which is not on air$`, st.aliceOffline)
			sc.Step(`^an unbound node `+q+` is on air serving `+q+`$`, st.unbound)
			sc.Step(`^the broker restarts on the same store$`, st.restart)
			sc.Step(`^a second broker instance shares the registry and the store$`, st.secondInstance)
			sc.Step(`^both instances sync the shared registry$`, st.syncBoth)
			sc.Step(`^on every instance the public offer for `+q+` `+q+` has category `+q+`$`, func(n, m, pair string) error {
				return st.everyInstance(func() error {
					o, err := st.offer(n, m)
					if err != nil {
						return err
					}
					if catPairOf(o.Taxonomy) != pair {
						return fmt.Errorf("category %q, got %q", pair, catPairOf(o.Taxonomy))
					}
					return nil
				})
			})
			sc.Step(`^on every instance the public offer for `+q+` `+q+` is priced `+f+` in and `+f+` out$`, func(n, m, in, out string) error {
				return st.everyInstance(func() error { return st.priced(n, m, num(in), num(out)) })
			})
			sc.Step(`^node `+q+` registers serving `+q+` as a tts model with its own tags `+q+` declared$`, st.registersTTS)
			sc.Step(`^node `+q+` has gone off air$`, st.offAir)

			sc.Step(`^(\w+) POSTs /provider/meta for `+q+` `+q+` with category `+q+`, tags `+q+`, tuning `+q+`, base model `+q+`, content policy `+q+` and license `+q+`$`, st.postAll)
			sc.Step(`^(\w+) POSTs /provider/meta for `+q+` `+q+` with category `+q+` and tags `+q+`$`, st.postCatTags)
			sc.Step(`^(\w+) POSTs /provider/meta for `+q+` `+q+` with content policy `+q+`$`, st.postPolicy)
			sc.Step(`^(\w+) POSTs /provider/meta the raw body (.+)$`, st.postRaw)
			sc.Step(`^an anonymous client POSTs /provider/meta for `+q+` `+q+` with category `+q+`$`, st.anonymous)
			sc.Step(`^(\w+) clears the category of `+q+` `+q+`$`, st.clear)
			sc.Step(`^(\w+) GETs /provider/meta$`, st.getMeta)
			sc.Step(`^(\w+) GETs /provider/models$`, st.getModels)
			sc.Step(`^(\w+) set the category of `+q+` `+q+` to `+q+`$`, st.setCategory)
			sc.Step(`^(\w+) sets? the price of `+q+` `+q+` to `+f+` in and `+f+` out$`, func(who, n, m, in, out string) error {
				return st.setPrice(who, n, m, num(in), num(out))
			})

			sc.Step(`^the response status is (\d+)$`, st.status)
			sc.Step(`^the error mentions `+q+`$`, st.errMentions)
			sc.Step(`^the response says the category applies the next time the node is on air$`, st.appliesLater)
			sc.Step(`^the public offer for `+q+` `+q+` has category `+q+`, tags `+q+` and source `+q+`$`, st.offerHas)
			sc.Step(`^the public offer for `+q+` `+q+` carries tuning `+q+`, base model `+q+`, content policy `+q+` and license `+q+`$`, st.offerFields)
			sc.Step(`^the public offer for `+q+` `+q+` has content policy `+q+`$`, st.offerPolicy)
			sc.Step(`^the public offer for `+q+` `+q+` is priced `+f+` in and `+f+` out$`, func(n, m, in, out string) error {
				return st.priced(n, m, num(in), num(out))
			})
			sc.Step(`^the stored override for `+q+` `+q+` has category `+q+` and tags `+q+`$`, st.stored)
			sc.Step(`^there is no stored override for `+q+` `+q+`$`, st.noStored)
			sc.Step(`^the stored price override for `+q+` `+q+` is `+f+` in and `+f+` out$`, func(n, m, in, out string) error {
				return st.storedPrice(n, m, num(in), num(out))
			})
			sc.Step(`^her row for `+q+` `+q+` has category `+q+`, source `+q+` and is (not )?marked meta-overridden$`, st.row)
			sc.Step(`^moderation still screens requests to `+q+`$`, st.stillScreened)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/categories/owner_override.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("categories/owner_override behavior scenarios failed (see godog output above)")
	}
}
