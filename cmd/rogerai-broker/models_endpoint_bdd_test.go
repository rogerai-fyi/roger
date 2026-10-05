package main

// models_endpoint_bdd_test.go makes the @slice0 scenarios of
// features/discovery/models_endpoint.feature EXECUTABLE against the REAL broker: nodes
// registered on the real routing broker (the discovery_market fixtures: registry, lastSeen,
// trust, tps, bans, private bands, cooldowns) and GET /v1/models served through the real mux
// (registerRoutes), read back over httptest exactly as an OpenAI SDK would. No mocks. The
// remaining scenarios of the feature (filters, the full rogerai block, the shared-store
// `created`) are slice 2's and stay untagged until their own RED.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

type meState struct {
	dmState
	t   *testing.T
	srv *httptest.Server

	code    int
	hdr     http.Header
	body    []byte
	prev    []byte // the previous response body, for the "identical" comparison
	decoded struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string          `json:"id"`
			Object  string          `json:"object"`
			Created json.RawMessage `json:"created"`
			OwnedBy string          `json:"owned_by"`
			RogerAI map[string]any  `json:"rogerai"`
		} `json:"data"`
	}
}

func (s *meState) reset() {
	s.dmState.reset()
	if s.srv != nil {
		s.srv.Close()
	}
	s.srv = httptest.NewServer(s.b.routes())
	s.code, s.hdr, s.body, s.prev = 0, nil, nil, nil
}

func (s *meState) emptyRegistry() error { return nil }

// nodesOnAir registers the Background table: one public node per row, on air now, with the
// listed quant / ctx / tps / prices / flags. verified = a recent passed canary.
func (s *meState) nodesOnAir(table *godog.Table) error {
	head := table.Rows[0].Cells
	idx := map[string]int{}
	for i, h := range head {
		idx[h.Value] = i
	}
	for _, row := range table.Rows[1:] {
		cells := row.Cells
		col := func(_ any, name string) string {
			if i, ok := idx[name]; ok && i < len(cells) {
				return strings.TrimSpace(cells[i].Value)
			}
			return ""
		}
		id, model := col(row, "node"), col(row, "model")
		in, _ := strconv.ParseFloat(col(row, "price_in"), 64)
		out, _ := strconv.ParseFloat(col(row, "price_out"), 64)
		ctx, _ := strconv.Atoi(col(row, "ctx"))
		tps, _ := strconv.ParseFloat(col(row, "tps"), 64)
		curated := col(row, "curated") == "true"
		reg := protocol.NodeRegistration{
			NodeID: id, Curated: curated,
			Offers: []protocol.ModelOffer{{Model: model, Quant: col(row, "quant"), Ctx: ctx, PriceIn: in, PriceOut: out}},
		}
		if curated {
			reg.CuratedProvider = "openrouter"
		}
		s.b.nodes[id] = reg
		s.b.lastSeen[id] = s.now
		if col(row, "confidential") == "true" {
			s.b.confidential[id] = true
		}
		s.b.tps[id] = tps
		if col(row, "verified") == "true" {
			s.b.trust[id] = trustState{probed: true, probeOK: true, ttftMs: 200}
			s.b.probeSched[id] = &probeState{lastMeasured: s.now}
		}
	}
	return nil
}

func (s *meState) get(path string, hdr map[string]string) error {
	req, err := http.NewRequest(http.MethodGet, s.srv.URL+path, nil)
	if err != nil {
		return err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	s.prev, s.body = s.body, buf.Bytes()
	s.code, s.hdr = res.StatusCode, res.Header
	s.decoded.Object, s.decoded.Data = "", nil
	_ = json.Unmarshal(s.body, &s.decoded)
	return nil
}

func (s *meState) consumerGetsModels() error { return s.get("/v1/models", nil) }

func (s *meState) consumerGetsModelsWithHeader(h string) error {
	k, v, _ := strings.Cut(h, ":")
	return s.get("/v1/models", map[string]string{strings.TrimSpace(k): strings.TrimSpace(v)})
}

func (s *meState) consumerGetsModelByID(id string) error { return s.get("/v1/models/"+id, nil) }

// openAISDKLists is what an OpenAI SDK does: decode the list with the SDK's own minimal
// shape (id / object / created / owned_by) and nothing else.
func (s *meState) openAISDKLists() error { return s.get("/v1/models", nil) }

func (s *meState) statusIs(code int) error {
	if s.code != code {
		return fmt.Errorf("GET /v1/models = %d, want %d: %s", s.code, code, s.body)
	}
	return nil
}

func (s *meState) contentTypeIs(ct string) error {
	if got := s.hdr.Get("Content-Type"); !strings.HasPrefix(got, ct) {
		return fmt.Errorf("Content-Type = %q, want %q", got, ct)
	}
	return nil
}

func (s *meState) bodyIsList() error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(s.body, &raw); err != nil {
		return fmt.Errorf("body is not a JSON object: %s", s.body)
	}
	if s.decoded.Object != "list" {
		return fmt.Errorf("object = %q, want \"list\": %s", s.decoded.Object, s.body)
	}
	if d, ok := raw["data"]; !ok || !bytes.HasPrefix(bytes.TrimSpace(d), []byte("[")) {
		return fmt.Errorf("body has no data array: %s", s.body)
	}
	return nil
}

func (s *meState) everyEntryShaped() error {
	if len(s.decoded.Data) == 0 {
		return fmt.Errorf("no data entries to check: %s", s.body)
	}
	for _, e := range s.decoded.Data {
		if e.Object != "model" || e.ID == "" || e.OwnedBy != "rogerai" {
			return fmt.Errorf("entry %+v is not {object:model, id, owned_by:rogerai}", e)
		}
		if _, err := strconv.ParseInt(string(e.Created), 10, 64); err != nil {
			return fmt.Errorf("entry %q created = %s, want an integer", e.ID, e.Created)
		}
	}
	return nil
}

// ids are the MODEL entries' ids: class alias entries (@class/..., §14.B4) are not models.
func (s *meState) ids() []string {
	var out []string
	for _, e := range s.decoded.Data {
		if strings.HasPrefix(e.ID, classPrefix) {
			continue
		}
		out = append(out, e.ID)
	}
	return out
}

func (s *meState) idsAreExactly(a, b, c string) error {
	want := []string{a, b, c}
	got := append([]string(nil), s.ids()...)
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		return fmt.Errorf("data ids = %v, want exactly %v (status %d: %s)", s.ids(), want, s.code, s.body)
	}
	return nil
}

func (s *meState) noIDTwice() error {
	seen := map[string]bool{}
	for _, id := range s.ids() {
		if seen[id] {
			return fmt.Errorf("id %q appears twice", id)
		}
		seen[id] = true
	}
	return nil
}

func (s *meState) idsInOrder(a, b, c string) error {
	got := s.ids()
	want := []string{a, b, c}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		return fmt.Errorf("data ids = %v, want in order %v", got, want)
	}
	return nil
}

func (s *meState) sdkDecodes(n int) error {
	if s.code != 200 {
		return fmt.Errorf("the SDK got %d: %s", s.code, s.body)
	}
	var sdk struct {
		Data []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(s.body, &sdk); err != nil {
		return fmt.Errorf("the SDK cannot decode the list: %v", err)
	}
	models := 0
	for _, e := range sdk.Data {
		if !strings.HasPrefix(e.ID, classPrefix) {
			models++
		}
	}
	if models != n {
		return fmt.Errorf("the SDK decoded %d models, want %d", models, n)
	}
	return nil
}

func (s *meState) nodeCooling(id string, secs int) error {
	s.b.coolStation(id, "llama-3.3-70b", secs)
	return nil
}

func (s *meState) nodeStale(id string) error {
	s.b.lastSeen[id] = s.now.Add(-2 * nodeTTL)
	return nil
}

func (s *meState) nodeBanned(id string) error {
	s.b.banned[id] = true
	return nil
}

func (s *meState) privateNode(id, model string) error {
	s.b.nodes[id] = protocol.NodeRegistration{NodeID: id, Offers: []protocol.ModelOffer{{Model: model, PriceIn: 0.2, PriceOut: 0.2}}}
	s.b.lastSeen[id] = s.now
	s.b.private[id] = true
	return nil
}

func (s *meState) privateNodeWithCode(id, model, code string) error {
	if err := s.privateNode(id, model); err != nil {
		return err
	}
	// A live band on the store, resolvable by its code exactly as X-Roger-Freq resolves it.
	return s.b.db.CreateBand(store.Band{ID: "band-" + id, Owner: "op-" + id, NodeID: id, CodeHash: protocol.BandCodeHash(code), CreatedAt: s.now.Unix()})
}

func (s *meState) modelListed(id string) error {
	for _, e := range s.decoded.Data {
		if e.ID == id {
			return nil
		}
	}
	return fmt.Errorf("%q is not listed (status %d): %s", id, s.code, s.body)
}

func (s *meState) modelNotListed(id string) error {
	for _, e := range s.decoded.Data {
		if e.ID == id {
			return fmt.Errorf("%q is listed and must not be: %s", id, s.body)
		}
	}
	return nil
}

func (s *meState) coolingBlock(secs int) error {
	for _, e := range s.decoded.Data {
		if e.ID != "llama-3.3-70b" {
			continue
		}
		if e.RogerAI == nil {
			return fmt.Errorf("entry carries no rogerai block: %s", s.body)
		}
		if c, _ := e.RogerAI["cooling"].(bool); !c {
			return fmt.Errorf("rogerai.cooling = %v, want true", e.RogerAI["cooling"])
		}
		until, _ := e.RogerAI["cooling_until"].(float64)
		want := float64(time.Now().Add(time.Duration(secs) * time.Second).Unix())
		if until < want-3 || until > want+3 {
			return fmt.Errorf("rogerai.cooling_until = %v, want about %v", until, want)
		}
		return nil
	}
	return fmt.Errorf("llama-3.3-70b is not listed")
}

// identicalToPrevious fetches the list WITHOUT the header and compares it byte-for-byte with
// the response the previous step got WITH it.
func (s *meState) identicalToPrevious() error {
	withHeader := append([]byte(nil), s.body...)
	if err := s.get("/v1/models", nil); err != nil {
		return err
	}
	if !bytes.Equal(withHeader, s.body) {
		return fmt.Errorf("the response differs with the band code header:\n%s\nvs\n%s", withHeader, s.body)
	}
	return nil
}

func TestModelsEndpointSlice0BDD(t *testing.T) {
	st := &meState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if st.srv != nil {
					st.srv.Close()
					st.srv = nil
				}
				return ctx, nil
			})
			sc.Step(`^a broker with an empty in-memory node registry$`, st.emptyRegistry)
			sc.Step(`^nodes are on air:$`, st.nodesOnAir)
			sc.Step(`^a consumer GETs /v1/models$`, st.consumerGetsModels)
			sc.Step(`^a consumer GETs /v1/models with header "([^"]*)"$`, st.consumerGetsModelsWithHeader)
			sc.Step(`^a consumer GETs /v1/models/([^ ]+)$`, st.consumerGetsModelByID)
			sc.Step(`^an OpenAI SDK lists models against the broker$`, st.openAISDKLists)
			sc.Step(`^the status is (\d+)$`, st.statusIs)
			sc.Step(`^the Content-Type is "([^"]*)"$`, st.contentTypeIs)
			sc.Step(`^the body has object "list" and a data array$`, st.bodyIsList)
			sc.Step(`^every data entry has object "model", a string id, an integer created and owned_by "rogerai"$`, st.everyEntryShaped)
			sc.Step(`^the data ids are exactly "([^"]*)", "([^"]*)", "([^"]*)"$`, st.idsAreExactly)
			sc.Step(`^no id appears twice$`, st.noIDTwice)
			sc.Step(`^the data ids are in order "([^"]*)", "([^"]*)", "([^"]*)"$`, st.idsInOrder)
			sc.Step(`^it decodes (\d+) models without error$`, st.sdkDecodes)
			sc.Step(`^"([^"]*)" is in a 429 cooldown for (\d+) s$`, st.nodeCooling)
			sc.Step(`^"([^"]*)" has not heartbeat within nodeTTL$`, st.nodeStale)
			sc.Step(`^"([^"]*)" is banned$`, st.nodeBanned)
			sc.Step(`^node "([^"]*)" shares "([^"]*)" on a PRIVATE band$`, st.privateNode)
			sc.Step(`^node "([^"]*)" shares "([^"]*)" on a PRIVATE band with frequency code "([^"]*)"$`, st.privateNodeWithCode)
			sc.Step(`^"([^"]*)" is listed$`, st.modelListed)
			sc.Step(`^"([^"]*)" is not listed$`, st.modelNotListed)
			sc.Step(`^its rogerai block has cooling true and cooling_until about (\d+) s from now$`, st.coolingBlock)
			sc.Step(`^the response is identical to one without the header$`, st.identicalToPrevious)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/discovery/models_endpoint.feature"},
			Tags:     "@slice0",
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("discovery/models_endpoint @slice0 scenarios failed (see godog output above)")
	}
}
