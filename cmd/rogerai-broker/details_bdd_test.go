package main

// details_bdd_test.go makes the @broker scenarios of features/details/*.feature EXECUTABLE:
// the REAL public handlers (/discover, /v1/models) over httptest with RogerAI's curated notes
// stamped at publish, the REAL signed registration (b.register), the REAL owner-authenticated
// /provider/meta and /provider/models, and pickFor for routing. Reuses catState (the
// /discover harness and its no-recompute lock probe) and ownerOverrideState (owner signing).

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/catalog"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/store"
)

// demoNotes is curated_catalog.feature's Background, for files that cite it.
var demoNotes = []catalog.Note{
	{ID: "demo-1b-chat", Summary: "General chat on the demo 1B base.", BestAt: "Conversation, short technical writing.",
		NotFor: "Fire decisions or reading sensors.", BaseModel: "demo-1b", License: "Apache-2.0", Page: "/research-demo-1b.html"},
	{ID: "demo-1b-fire-decision", Summary: "Picks one fire-defense action from site state.", BestAt: "Choosing among a fixed list of actions.",
		NotFor: "Chat. It answers with one action only.", BaseModel: "demo-1b", License: "Apache-2.0", Page: "/research-demo-1b.html#fire"},
	{ID: "demo-1b-fire-vl", Summary: "Confirms or rejects smoke or flame in a frame.", BestAt: "Single camera frames, yes or no.",
		NotFor: "Video, or deciding what to do about it.", BaseModel: "demo-1b", License: "Apache-2.0", Page: "/research-demo-1b.html#vl"},
}

type detailsBrokerState struct {
	t     *testing.T
	cat   catState
	own   ownerOverrideState
	notes []catalog.Note
	n     int
	last  offerView
	entry map[string]any // one /v1/models entry
	list  []map[string]any
	code  int
	ctype string
	raw   []byte
	other []byte
	split [2]map[string]int
	err   error
}

func (s *detailsBrokerState) setNotes(ns []catalog.Note) error {
	s.notes = ns
	b, _ := json.Marshal(ns)
	parsed, err := catalog.ParseNotes(b)
	if err != nil {
		return err
	}
	s.cat.b.notes = parsed
	s.own.b.notes = parsed
	return nil
}

func (s *detailsBrokerState) notesTable(t *godog.Table) error {
	head := t.Rows[0].Cells
	var ns []catalog.Note
	for _, row := range t.Rows[1:] {
		v := map[string]string{}
		for i, c := range row.Cells {
			v[head[i].Value] = c.Value
		}
		ns = append(ns, catalog.Note{ID: v["id"], Summary: v["summary"], BestAt: v["best_at"], NotFor: v["not_for"],
			BaseModel: v["base_model"], License: v["license"], Page: v["page"]})
	}
	return s.setNotes(ns)
}

// notesDescribe takes "a", "b" and "c" ids from the shared demo notes.
func (s *detailsBrokerState) notesDescribe(ids string) error {
	var ns []catalog.Note
	for _, id := range strings.Split(strings.NewReplacer(`" and "`, ",", `", "`, ",").Replace(ids), ",") {
		for _, n := range demoNotes {
			if n.ID == strings.Trim(id, `"`) {
				ns = append(ns, n)
			}
		}
	}
	return s.setNotes(ns)
}

func (s *detailsBrokerState) noteNoLicense(id string) error {
	ns := append([]catalog.Note(nil), s.notes...)
	for i := range ns {
		if ns[i].ID == id {
			ns[i].License = ""
		}
	}
	return s.setNotes(ns)
}
func (s *detailsBrokerState) notePage(id, page string) error {
	ns := append([]catalog.Note(nil), s.notes...)
	for i := range ns {
		if ns[i].ID == id {
			ns[i].Page = page
		}
	}
	return s.setNotes(ns)
}
func (s *detailsBrokerState) notesEmpty() error { return s.setNotes(nil) }

// ---- stations
func (s *detailsBrokerState) station(o protocol.ModelOffer) {
	s.n++
	s.cat.put("s"+strconv.Itoa(s.n), o, true, 100)
}
func (s *detailsBrokerState) onAir(model string) error {
	s.station(protocol.ModelOffer{Model: model})
	return nil
}
func (s *detailsBrokerState) onAirSummary(model, sum string) error {
	s.station(protocol.ModelOffer{Model: unescCat(model), Taxonomy: protocol.Taxonomy{Summary: sum}})
	return nil
}
func (s *detailsBrokerState) onAirLicense(model, lic string) error {
	s.station(protocol.ModelOffer{Model: model, Taxonomy: protocol.Taxonomy{License: lic}})
	return nil
}
func (s *detailsBrokerState) onAirBaseLicense(model, base, lic string) error {
	s.station(protocol.ModelOffer{Model: model, Taxonomy: protocol.Taxonomy{BaseModel: base, License: lic}})
	return nil
}
func (s *detailsBrokerState) onAirFull(model, pair, tags, tuning, policy string, out float64) error {
	c, sub := protocol.SplitCategory(pair)
	s.station(protocol.ModelOffer{Model: model, PriceIn: out, PriceOut: out, Taxonomy: protocol.Taxonomy{
		Category: c, Subcategory: sub, Tags: splitNonEmpty(tags), Tuning: tuning, ContentPolicy: policy}})
	return nil
}
func (s *detailsBrokerState) offAirListed(model string) error {
	s.station(protocol.ModelOffer{Model: model})
	s.cat.b.lastSeen["s"+strconv.Itoa(s.n)] = s.cat.now.Add(-2 * nodeTTL)
	return nil
}

func (s *detailsBrokerState) chatOffers(t *godog.Table) error {
	head := t.Rows[0].Cells
	for _, row := range t.Rows[1:] {
		v := map[string]string{}
		for i, c := range row.Cells {
			v[head[i].Value] = strings.TrimSpace(c.Value)
		}
		s.cat.put(v["node"], protocol.ModelOffer{Model: v["model"], Taxonomy: protocol.Taxonomy{
			Tuning: v["tuning"], BaseModel: v["base_model"], License: v["license"],
			Summary: v["summary"], BestAt: v["best_at"], NotFor: v["not_for"]}}, true, 100)
	}
	return nil
}
func (s *detailsBrokerState) offerSummary(model, sum string) error {
	s.cat.put("x1", protocol.ModelOffer{Model: model, Taxonomy: protocol.Taxonomy{Summary: sum}}, true, 100)
	return nil
}

// ---- reads
func (s *detailsBrokerState) readDiscover() error { return s.cat.getDiscover() }
func (s *detailsBrokerState) offerFor(model string) (offerView, error) {
	for _, o := range s.cat.offers {
		if o.Model == model {
			s.last = o
			return o, nil
		}
	}
	return offerView{}, fmt.Errorf("no offer for %q in /discover", model)
}
func (s *detailsBrokerState) offerSummaryIs(model, want string) error {
	o, err := s.offerFor(model)
	if err != nil {
		return err
	}
	return want1("summary", want, o.Summary)
}
func (s *detailsBrokerState) offerSummarySource(model, sum, src string) error {
	if err := s.offerSummaryIs(model, sum); err != nil {
		return err
	}
	return want1("details source", src, s.last.DetailsSource)
}
func (s *detailsBrokerState) offerSource(model, src string) error {
	o, err := s.offerFor(model)
	if err != nil {
		return err
	}
	return want1("details source", src, o.DetailsSource)
}
func (s *detailsBrokerState) onlySource(src string) error {
	if len(s.cat.offers) != 1 {
		return fmt.Errorf("expected one offer, got %d", len(s.cat.offers))
	}
	return want1("details source", src, s.cat.offers[0].DetailsSource)
}
func (s *detailsBrokerState) offerLicense(model, lic string) error {
	o, err := s.offerFor(model)
	if err != nil {
		return err
	}
	return want1("license", lic, o.License)
}
func (s *detailsBrokerState) offerBaseLicense(model, base, lic string) error {
	o, err := s.offerFor(model)
	if err != nil {
		return err
	}
	if err := want1("base model", base, o.BaseModel); err != nil {
		return err
	}
	return want1("license", lic, o.License)
}
func (s *detailsBrokerState) offerKeeps(model, pair, tags, tuning, policy string, out float64) error {
	o, err := s.offerFor(model)
	if err != nil {
		return err
	}
	got := fmt.Sprintf("%s|%s|%s|%s|%v", catPairOf(o.Taxonomy), strings.Join(o.Tags, ","), o.Tuning, o.ContentPolicy, o.Out)
	if want := fmt.Sprintf("%s|%s|%s|%s|%v", pair, tags, tuning, policy, out); got != want {
		return fmt.Errorf("offer kept %q, got %q", want, got)
	}
	return nil
}
func (s *detailsBrokerState) itsBest(v string) error   { return want1("best_at", v, s.last.BestAt) }
func (s *detailsBrokerState) itsNotFor(v string) error { return want1("not_for", v, s.last.NotFor) }
func (s *detailsBrokerState) itsSource(v string) error {
	return want1("details source", v, s.last.DetailsSource)
}
func (s *detailsBrokerState) itsPage(v string) error { return want1("page", v, s.last.Page) }
func (s *detailsBrokerState) itsNoPage() error       { return want1("page", "", s.last.Page) }

func want1(what, want, got string) error {
	if want != got {
		return fmt.Errorf("expected %s %q, got %q", what, want, got)
	}
	return nil
}

func (s *detailsBrokerState) readN(n int, path string) error {
	if err := s.cat.warm(); err != nil {
		return err
	}
	for i := 0; i < n; i++ {
		s.cat.do(http.MethodGet, path)
	}
	return nil
}
func (s *detailsBrokerState) discoverN(n int) error { return s.readN(n, "/discover") }
func (s *detailsBrokerState) modelsN(n int) error {
	s.getModels("/v1/models") // cold fill of the models entry
	s.cat.holdLock = true
	for i := 0; i < n; i++ {
		s.getModels("/v1/models")
	}
	return nil
}
func (s *detailsBrokerState) noRecompute() error {
	if s.cat.recomputes != 0 {
		return fmt.Errorf("%d reads recomputed instead of reading the cache", s.cat.recomputes)
	}
	return nil
}

// ---- /v1/models
func (s *detailsBrokerState) getModels(path string) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if !s.cat.holdLock {
		s.cat.b.models(w, r)
	} else {
		s.cat.b.mu.Lock()
		done := make(chan struct{})
		go func() { s.cat.b.models(w, r); close(done) }()
		select {
		case <-done:
		case <-time.After(300 * time.Millisecond):
			s.cat.recomputes++
		}
		s.cat.b.mu.Unlock()
		<-done
	}
	s.code, s.ctype, s.raw = w.Code, w.Header().Get("Content-Type"), w.Body.Bytes()
	s.entry, s.list = nil, nil
	if strings.HasPrefix(path, "/v1/models/") {
		_ = json.Unmarshal(s.raw, &s.entry)
		return
	}
	var l struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(s.raw, &l)
	s.list = l.Data
}
func (s *detailsBrokerState) readModelsPath(p string) error {
	s.getModels(p)
	if s.code != http.StatusOK {
		return fmt.Errorf("GET %s = %d: %s", p, s.code, s.raw)
	}
	return nil
}
func (s *detailsBrokerState) entryDescription(v string) error {
	d, _ := s.entry["description"].(string)
	return want1("description", v, d)
}
func (s *detailsBrokerState) noDescription() error {
	if _, ok := s.entry["description"]; ok {
		return fmt.Errorf("expected no description key, got %v", s.entry["description"])
	}
	return nil
}
func (s *detailsBrokerState) detailsBlock() map[string]any {
	ra, _ := s.entry["rogerai"].(map[string]any)
	d, _ := ra["details"].(map[string]any)
	return d
}
func (s *detailsBrokerState) noDetailsKey() error {
	ra, _ := s.entry["rogerai"].(map[string]any)
	if _, ok := ra["details"]; ok {
		return fmt.Errorf("expected no rogerai.details, got %v", ra["details"])
	}
	return nil
}
func (s *detailsBrokerState) detailsIs(t *godog.Table) error {
	d := s.detailsBlock()
	for _, row := range t.Rows[1:] {
		k, v := row.Cells[0].Value, row.Cells[1].Value
		if got := fmt.Sprint(d[k]); got != v {
			return fmt.Errorf("rogerai.details.%s: expected %q, got %q", k, v, got)
		}
	}
	return nil
}
func (s *detailsBrokerState) detailsKey(k, v string) error {
	return want1("rogerai.details."+k, v, fmt.Sprint(s.detailsBlock()[k]))
}
func (s *detailsBrokerState) detailsNum(k string, n int) error {
	return want1("rogerai.details."+k, strconv.Itoa(n), fmt.Sprint(s.detailsBlock()[k]))
}
func (s *detailsBrokerState) detailsNoKey(k string) error {
	if _, ok := s.detailsBlock()[k]; ok {
		return fmt.Errorf("expected no rogerai.details.%s", k)
	}
	return nil
}
func (s *detailsBrokerState) stripDetails() error {
	s.cat.b.notes = nil // curated text is a details field too
	for id, n := range s.cat.b.nodes {
		for i := range n.Offers {
			n.Offers[i].Taxonomy = n.Offers[i].Taxonomy.WithDetailsOf(protocol.Taxonomy{})
		}
		s.cat.b.nodes[id] = n
	}
	return nil
}
func (s *detailsBrokerState) noNewKeys() error {
	if err := s.readModelsPath("/v1/models"); err != nil {
		return err
	}
	for _, k := range []string{`"description"`, `"details"`, `"summary"`, `"best_at"`, `"not_for"`} {
		if bytes.Contains(s.raw, []byte(k)) {
			return fmt.Errorf("a catalog with no details carries %s: %s", k, s.raw)
		}
	}
	return nil
}
func (s *detailsBrokerState) listAndOne() error {
	if err := s.readModelsPath("/v1/models"); err != nil {
		return err
	}
	list := s.list
	if err := s.readModelsPath("/v1/models/someone-7b"); err != nil {
		return err
	}
	for _, e := range list {
		if e["id"] == "someone-7b" {
			e["created"], s.entry["created"] = nil, nil
			if !reflect.DeepEqual(e, s.entry) {
				return fmt.Errorf("list entry %v != single entry %v", e, s.entry)
			}
			return nil
		}
	}
	return fmt.Errorf("someone-7b not in the list")
}
func (s *detailsBrokerState) sameEntry(string) error { return nil } // asserted by listAndOne
func (s *detailsBrokerState) everyDescribed() error {
	if len(s.list) == 0 {
		return fmt.Errorf("the filtered list is empty")
	}
	for _, e := range s.list {
		ra, _ := e["rogerai"].(map[string]any)
		if d, ok := ra["details"].(map[string]any); ok && e["description"] != d["summary"] {
			return fmt.Errorf("entry %v: description %v, summary %v", e["id"], e["description"], d["summary"])
		}
	}
	return nil
}
func (s *detailsBrokerState) classNoDescription() error {
	for _, e := range s.list {
		if id, _ := e["id"].(string); strings.HasPrefix(id, "@class/") {
			if _, ok := e["description"]; ok {
				return fmt.Errorf("class entry %s carries a description", id)
			}
		}
	}
	return nil
}
func (s *detailsBrokerState) contentType(v string) error {
	if !strings.HasPrefix(s.ctype, v) {
		return fmt.Errorf("Content-Type %q, got %q", v, s.ctype)
	}
	return nil
}

// ---- page links
func (s *detailsBrokerState) discoverPage(p string) error {
	if len(s.cat.b.nodes) == 0 {
		s.station(protocol.ModelOffer{Model: "demo-1b-chat"})
	}
	if err := s.readDiscover(); err != nil {
		return err
	}
	if _, err := s.offerFor("demo-1b-chat"); err != nil {
		return err
	}
	return s.itsPage(p)
}
func (s *detailsBrokerState) modelsPage(p string) error {
	if err := s.readModelsPath("/v1/models/demo-1b-chat"); err != nil {
		return err
	}
	return s.detailsKey("page", p)
}
func (s *detailsBrokerState) tuiPage(p string) error {
	return want1("TUI page", p, catalog.PageDisplay(s.last.Page))
}

// ---- filters, routing, registration
func (s *detailsBrokerState) discoverQuery(q string) error {
	if err := s.readDiscover(); err != nil {
		return err
	}
	s.other = s.cat.raw
	s.cat.do(http.MethodGet, "/discover?"+q)
	return nil
}
func (s *detailsBrokerState) sameAsNoQuery() error {
	if s.cat.code != http.StatusOK || !bytes.Equal(s.cat.raw, s.other) {
		return fmt.Errorf("/discover with the query = %d %s, without = %s", s.cat.code, s.cat.raw, s.other)
	}
	return nil
}
func (s *detailsBrokerState) twoStations(model, sum string) error {
	s.cat.put("r1", protocol.ModelOffer{Model: model, PriceIn: 1, PriceOut: 1}, true, 100)
	s.cat.put("r2", protocol.ModelOffer{Model: model, PriceIn: 1, PriceOut: 1, Taxonomy: protocol.Taxonomy{Summary: sum}}, true, 100)
	return nil
}
func (s *detailsBrokerState) pickN(model string, n int) error {
	for round := 0; round < 2; round++ {
		if round == 1 { // the same stations, neither carrying a summary
			for _, id := range []string{"r1", "r2"} {
				reg := s.cat.b.nodes[id]
				reg.Offers[0].Taxonomy = reg.Offers[0].Taxonomy.WithDetailsOf(protocol.Taxonomy{})
				s.cat.b.nodes[id] = reg
			}
		}
		s.split[round] = map[string]int{}
		for i := 0; i < n; i++ {
			reg, _, ok := s.cat.b.pickFor(model, false, 0, 0, 0, "", nil, nil, nil, pickReq{})
			if !ok {
				return fmt.Errorf("no pick for %s", model)
			}
			s.split[round][reg.NodeID]++
		}
	}
	return nil
}
func (s *detailsBrokerState) splitEqual() error {
	for _, id := range []string{"r1", "r2"} {
		a, b := s.split[0][id], s.split[1][id]
		if d := a - b; d > 100 || d < -100 { // 10% of 1000: the router's own exploration noise
			return fmt.Errorf("station %s picked %d times with details, %d without", id, a, b)
		}
	}
	return nil
}

func (s *detailsBrokerState) postRegister(body []byte) {
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.own.b.register(w, r)
	s.code, s.raw = w.Code, w.Body.Bytes()
}
func (s *detailsBrokerState) signedBody(o protocol.ModelOffer) []byte {
	pub, priv, _ := ed25519.GenerateKey(nil)
	reg := protocol.NodeRegistration{NodeID: "old-node", PubKey: hex.EncodeToString(pub), TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{o}}
	reg.SignRegistration(priv)
	b, _ := json.Marshal(reg)
	return b
}
func (s *detailsBrokerState) oldNodeBody() error {
	s.raw = s.signedBody(protocol.ModelOffer{Model: "demo-1b-chat"})
	if bytes.Contains(s.raw, []byte("summary")) || bytes.Contains(s.raw, []byte("details_source")) {
		return fmt.Errorf("an old node's body carries details keys: %s", s.raw)
	}
	return nil
}
func (s *detailsBrokerState) acceptReg() error { s.postRegister(s.raw); return nil }
func (s *detailsBrokerState) regAccepted() error {
	if s.code != http.StatusOK {
		return fmt.Errorf("register = %d: %s", s.code, s.raw)
	}
	return nil
}
func (s *detailsBrokerState) regNoSource() error {
	for _, o := range s.own.b.nodes["old-node"].Offers {
		if o.DetailsSource != "" {
			return fmt.Errorf("details source %q", o.DetailsSource)
		}
	}
	return nil
}
func (s *detailsBrokerState) oversized() error {
	s.raw = s.signedBody(protocol.ModelOffer{Model: "m", Taxonomy: protocol.Taxonomy{Summary: strings.Repeat("a", 2<<20)}})
	return nil
}
func (s *detailsBrokerState) brokerReads() error {
	s.postRegister(s.raw)
	s.other = []byte(strconv.Itoa(s.code))
	return nil
}
func (s *detailsBrokerState) refusedAsOversized() error {
	// Any other oversized registration: the same size, carried by a field that is not details.
	body := s.signedBody(protocol.ModelOffer{Model: "m", Taxonomy: protocol.Taxonomy{License: strings.Repeat("a", 2<<20)}})
	s.postRegister(body)
	if string(s.other) != strconv.Itoa(s.code) || s.code == http.StatusOK {
		return fmt.Errorf("details oversized = %s, other oversized = %d", s.other, s.code)
	}
	return nil
}

func (s *detailsBrokerState) brokerStarts() error {
	s.err = nil
	defer func() {
		if r := recover(); r != nil {
			s.err = fmt.Errorf("broker start panicked: %v", r)
		}
	}()
	_, priv, _ := ed25519.GenerateKey(nil)
	b := buildBroker(store.NewMem(), priv, 0.2, 0, time.Minute)
	if len(b.notes) == 0 {
		s.err = fmt.Errorf("the broker started with no curated notes")
	}
	return nil
}
func (s *detailsBrokerState) notesLoaded() error { return s.err }

// ---- owner override (declaration.feature)
func (s *detailsBrokerState) ownerSignedIn(node string) error {
	if err := s.own.owner("alice"); err != nil {
		return err
	}
	return s.own.mem.BindNode(node, pubHex(s.own.owners["alice"]))
}
func (s *detailsBrokerState) nodeOnAir(node, model string) error {
	return s.own.register(node, "alice", model, "", protocol.Taxonomy{})
}
func (s *detailsBrokerState) ownerSaves(sum string) error {
	if _, ok := s.own.b.nodes["n1"]; !ok { // the curated scenario names no node: sign in and go on air
		return s.ownerSavesCurated(sum)
	}
	s.own.postMeta("alice", map[string]any{"node": "n1", "model": s.ownModel(), "summary": sum})
	return nil
}
func (s *detailsBrokerState) ownModel() string {
	for _, o := range s.own.b.nodes["n1"].Offers {
		return o.Model
	}
	return "demo-1b-fire-decision"
}
func (s *detailsBrokerState) appliedLive(v string) error {
	var r struct {
		Applied string `json:"applied"`
	}
	_ = json.Unmarshal(s.own.body, &r)
	if s.own.code != http.StatusOK || r.Applied != v {
		return fmt.Errorf("/provider/meta = %d %s, want applied %q", s.own.code, s.own.body, v)
	}
	return nil
}
func (s *detailsBrokerState) discoverShows(sum string) error {
	o, err := s.own.offer("n1", s.ownModel())
	if err != nil {
		return err
	}
	return want1("summary", sum, o.Summary)
}
func (s *detailsBrokerState) savedOffAir(sum, node string) error {
	if err := s.ownerSignedIn(node); err != nil {
		return err
	}
	s.own.postMeta("alice", map[string]any{"node": node, "model": "demo-1b-fire-decision", "summary": sum})
	if s.own.code != http.StatusOK {
		return fmt.Errorf("/provider/meta = %d %s", s.own.code, s.own.body)
	}
	return nil
}
func (s *detailsBrokerState) savedFor(sum, node string) error {
	if err := s.savedOffAir(sum, node); err != nil {
		return err
	}
	return s.own.register(node, "alice", "demo-1b-fire-decision", "", protocol.Taxonomy{})
}
func (s *detailsBrokerState) nodeRegisters(node, model string) error {
	return s.own.register(node, "alice", model, "", protocol.Taxonomy{})
}
func (s *detailsBrokerState) nonOwnerPosts(node string) error {
	if err := s.own.owner("mallory"); err != nil {
		return err
	}
	s.own.postMeta("mallory", map[string]any{"node": node, "model": "demo-1b-fire-decision", "summary": "x"})
	return nil
}
func (s *detailsBrokerState) status403(msg string) error {
	if s.own.code != http.StatusForbidden || !strings.Contains(string(s.own.body), msg) {
		return fmt.Errorf("expected 403 %q, got %d %s", msg, s.own.code, s.own.body)
	}
	return nil
}
func (s *detailsBrokerState) postLongBest(n int) error {
	if err := s.ownerSignedIn("n1"); err != nil {
		return err
	}
	s.own.postMeta("alice", map[string]any{"node": "n1", "model": "demo-1b-fire-decision", "best_at": strings.Repeat("b", n)})
	return nil
}
func (s *detailsBrokerState) status400(msg string) error {
	if s.own.code != http.StatusBadRequest || !strings.Contains(string(s.own.body), msg) {
		return fmt.Errorf("expected 400 %q, got %d %s", msg, s.own.code, s.own.body)
	}
	return nil
}
func (s *detailsBrokerState) ownerClears() error {
	s.own.postMeta("alice", map[string]any{"node": "n1", "model": "demo-1b-fire-decision", "clear": true})
	return nil
}
func (s *detailsBrokerState) nextRegOwnOnly(node string) error {
	if err := s.own.register(node, "alice", "demo-1b-fire-decision", "", protocol.Taxonomy{Summary: "node words"}); err != nil {
		return err
	}
	return s.discoverShows("node words")
}
func (s *detailsBrokerState) inCurated(id string) error { return s.notesDescribe(`"` + id + `"`) }
func (s *detailsBrokerState) ownerSavesCurated(sum string) error {
	if err := s.ownerSignedIn("n1"); err != nil {
		return err
	}
	if err := s.nodeOnAir("n1", "demo-1b-fire-decision"); err != nil {
		return err
	}
	s.own.postMeta("alice", map[string]any{"node": "n1", "model": "demo-1b-fire-decision", "summary": sum})
	return nil
}
func (s *detailsBrokerState) saveSucceeds() error {
	if s.own.code != http.StatusOK {
		return fmt.Errorf("/provider/meta = %d %s", s.own.code, s.own.body)
	}
	return nil
}
func (s *detailsBrokerState) discoverShowsCurated(model string) error {
	o, err := s.own.offer("n1", model)
	if err != nil {
		return err
	}
	for _, n := range demoNotes {
		if n.ID == model {
			return want1("summary", n.Summary, o.Summary)
		}
	}
	return fmt.Errorf("no demo note for %s", model)
}

// editorSays: the owner's /provider/models row is flagged curated_note, and the website
// editor (CatEditor.svelte) shows the sentence exactly when that flag is set.
func (s *detailsBrokerState) editorSays(text string) error {
	s.own.send("/provider/models", http.MethodGet, "alice", nil)
	var r struct {
		Models []map[string]any `json:"models"`
	}
	_ = json.Unmarshal(s.own.body, &r)
	if len(r.Models) == 0 || r.Models[0]["curated_note"] != true {
		return fmt.Errorf("/provider/models row not flagged curated_note: %s", s.own.body)
	}
	src, err := os.ReadFile("../../web/app/lib/console/CatEditor.svelte")
	if err != nil {
		return err
	}
	if !bytes.Contains(src, []byte("m.curated_note")) || !bytes.Contains(src, []byte(text)) {
		return fmt.Errorf("CatEditor.svelte does not show %q behind m.curated_note", text)
	}
	return nil
}

func TestDetailsBrokerBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &detailsBrokerState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = detailsBrokerState{t: t}
				st.cat.reset()
				st.own = ownerOverrideState{t: t, mem: store.NewMem(),
					owners: map[string]ed25519.PrivateKey{}, nodes: map[string]ed25519.PrivateKey{}}
				st.own.freshBroker()
				return ctx, nil
			})
			q := `"([^"]*)"`
			// Backgrounds that set context for other runners' halves.
			sc.Step(`^a node registering an offer with the broker$`, func() error { return nil })
			sc.Step(`^a share controller with a detected model `+q+` inferred as `+q+`$`, func(string, string) error { return nil })
			sc.Step(`^RogerAI's curated notes contain:$`, st.notesTable)
			sc.Step(`^RogerAI's curated notes describe (.*) as in curated_catalog\.feature$`, st.notesDescribe)
			sc.Step(`^these on-air chat offers:$`, st.chatOffers)

			sc.Step(`^a station on air with model `+q+`$`, st.onAir)
			sc.Step(`^a station on air with model `+q+` and no details$`, st.onAir)
			sc.Step(`^a station on air with model `+q+` and summary `+q+`$`, st.onAirSummary)
			sc.Step(`^a station on air with model `+q+` declaring license `+q+`$`, st.onAirLicense)
			sc.Step(`^a station on air with model `+q+` declaring base model `+q+` and license `+q+`$`, st.onAirBaseLicense)
			sc.Step(`^a station on air with model `+q+` declaring category `+q+`, tags `+q+`, tuning `+q+`, policy `+q+` and price out ([\d.]+)$`, st.onAirFull)
			sc.Step(`^a station with model `+q+` that is off air but still listed$`, st.offAirListed)
			sc.Step(`^RogerAI's curated note for `+q+` has no license$`, st.noteNoLicense)
			sc.Step(`^RogerAI's curated notes are empty$`, st.notesEmpty)
			sc.Step(`^the note for `+q+` has page `+q+`$`, st.notePage)
			sc.Step(`^an on-air offer for `+q+` with summary "(.*)"$`, st.offerSummary)

			sc.Step(`^a consumer reads /discover$`, st.readDiscover)
			sc.Step(`^the offer for `+q+` has summary `+q+`$`, st.offerSummaryIs)
			sc.Step(`^the offer for `+q+` has summary `+q+` and details source `+q+`$`, st.offerSummarySource)
			sc.Step(`^the offer for `+q+` has details source `+q+`$`, st.offerSource)
			sc.Step(`^the offer's details source is `+q+`$`, st.onlySource)
			sc.Step(`^the offer for `+q+` has license `+q+`$`, st.offerLicense)
			sc.Step(`^the offer for `+q+` has base model `+q+` and license `+q+`$`, st.offerBaseLicense)
			sc.Step(`^the offer for `+q+` keeps category `+q+`, tags `+q+`, tuning `+q+`, policy `+q+` and price out ([\d.]+)$`, st.offerKeeps)
			sc.Step(`^its best_at is `+q+`$`, st.itsBest)
			sc.Step(`^its not_for is `+q+`$`, st.itsNotFor)
			sc.Step(`^its details source is `+q+`$`, st.itsSource)
			sc.Step(`^its page is `+q+`$`, st.itsPage)
			sc.Step(`^it has no page$`, st.itsNoPage)
			sc.Step(`^/discover is read (\d+) times within the cache window$`, st.discoverN)
			sc.Step(`^/v1/models is read (\d+) times within the cache window$`, st.modelsN)
			sc.Step(`^the curated notes were applied once per cache fill$`, st.noRecompute)
			sc.Step(`^the details were computed once per cache fill$`, st.noRecompute)

			sc.Step(`^a consumer reads (/v1/models\S*)$`, st.readModelsPath)
			sc.Step(`^a consumer reads /v1/models and /v1/models/someone-7b$`, st.listAndOne)
			sc.Step(`^the `+q+` entry is identical in both$`, st.sameEntry)
			sc.Step(`^the entry's "description" is `+q+`$`, st.entryDescription)
			sc.Step(`^the "description" decodes to exactly "(.*)"$`, st.entryDescription)
			sc.Step(`^the entry has no "description" key$`, st.noDescription)
			sc.Step(`^the entry's rogerai block has no "details" key$`, st.noDetailsKey)
			sc.Step(`^the entry's rogerai\.details is:$`, st.detailsIs)
			sc.Step(`^the entry's rogerai\.details\.(\w+) is `+q+`$`, st.detailsKey)
			sc.Step(`^the entry's rogerai\.details\.(\w+) is (\d+)$`, st.detailsNum)
			sc.Step(`^the entry's rogerai\.details has no `+q+`$`, st.detailsNoKey)
			sc.Step(`^the same offers with every details field removed$`, st.stripDetails)
			sc.Step(`^the response is byte-identical to the response before this change$`, st.noNewKeys)
			sc.Step(`^every returned entry with text carries its description$`, st.everyDescribed)
			sc.Step(`^no "@class/" entry has a "description" key$`, st.classNoDescription)
			sc.Step(`^the response Content-Type is `+q+`$`, st.contentType)
			sc.Step(`^/discover carries page `+q+`$`, st.discoverPage)
			sc.Step(`^/v1/models carries page `+q+`$`, st.modelsPage)
			sc.Step(`^the TUI shows `+q+`$`, st.tuiPage)

			sc.Step(`^a consumer queries /discover with `+q+`$`, st.discoverQuery)
			sc.Step(`^the response is the same as /discover with no query$`, st.sameAsNoQuery)
			sc.Step(`^two stations for `+q+` identical except that one has summary `+q+`$`, st.twoStations)
			sc.Step(`^a consumer asks for `+q+` (\d+) times$`, st.pickN)
			sc.Step(`^the routing split equals the split with neither station carrying a summary$`, st.splitEqual)
			sc.Step(`^a registration body produced by a node without the details fields$`, st.oldNodeBody)
			sc.Step(`^the broker accepts the registration$`, st.acceptReg)
			sc.Step(`^the registration is accepted$`, st.regAccepted)
			sc.Step(`^the offer for that model has no details source$`, st.regNoSource)
			sc.Step(`^a registration body larger than the broker's registration limit because of details text$`, st.oversized)
			sc.Step(`^the broker reads it$`, st.brokerReads)
			sc.Step(`^it is refused exactly as any oversized registration is$`, st.refusedAsOversized)
			sc.Step(`^the broker starts$`, st.brokerStarts)
			sc.Step(`^the curated notes load with no error$`, st.notesLoaded)

			sc.Step(`^the owner of node `+q+` is signed in on the website$`, st.ownerSignedIn)
			sc.Step(`^node `+q+` is on air with `+q+`$`, st.nodeOnAir)
			sc.Step(`^the owner saves summary `+q+` in the website editor$`, st.ownerSaves)
			sc.Step(`^/provider/meta answers applied `+q+`$`, st.appliedLive)
			sc.Step(`^/discover shows summary `+q+` for that offer$`, st.discoverShows)
			sc.Step(`^the owner saved summary `+q+` for node `+q+` while it was off air$`, st.savedOffAir)
			sc.Step(`^the owner saved summary `+q+` for node `+q+`$`, st.savedFor)
			sc.Step(`^node `+q+` registers `+q+`$`, st.nodeRegisters)
			sc.Step(`^a signed-in user who does not own node `+q+` posts details for it$`, st.nonOwnerPosts)
			sc.Step(`^the response is 403 `+q+`$`, st.status403)
			sc.Step(`^the owner posts a (\d+)-character best_at to /provider/meta$`, st.postLongBest)
			sc.Step(`^the response is 400 with `+q+`$`, st.status400)
			sc.Step(`^the owner clears the override$`, st.ownerClears)
			sc.Step(`^the next registration of node `+q+` carries only the node's own details$`, st.nextRegOwnOnly)
			sc.Step(`^`+q+` is in RogerAI's curated notes$`, st.inCurated)
			sc.Step(`^the save succeeds$`, st.saveSucceeds)
			sc.Step(`^/discover shows RogerAI's summary for `+q+`$`, st.discoverShowsCurated)
			sc.Step(`^the website editor says `+q+`$`, st.editorSays)
		},
		Options: &godog.Options{
			Format: "pretty",
			Paths: []string{"../../features/details/offer_details.feature", "../../features/details/text_sanitization.feature",
				"../../features/details/curated_catalog.feature", "../../features/details/api_cli.feature",
				"../../features/details/declaration.feature"},
			Tags:     "@broker",
			TestingT: t,
			Strict:   true,
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("details @broker behavior scenarios failed (see godog output above)")
	}
}
