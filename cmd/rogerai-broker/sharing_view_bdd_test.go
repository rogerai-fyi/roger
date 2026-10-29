package main

// sharing_view_bdd_test.go makes the broker half of features/categories/web_sharing_view.feature
// EXECUTABLE (every scenario not tagged @web): the REAL owner-scoped GET /provider/models,
// signed as the owner, over nodes bound to that owner (and one bound to another).

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
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v7/internal/protocol"
)

type sharingViewState struct {
	t    *testing.T
	b    *broker
	priv ed25519.PrivateKey
	rows []map[string]any
}

func (s *sharingViewState) station(owner, model string, tx protocol.Taxonomy) error {
	if s.b == nil {
		s.b, s.priv, _, _ = newBandBroker(s.t)
	}
	id := "st-" + model
	s.b.mu.Lock()
	s.b.nodes[id] = protocol.NodeRegistration{NodeID: id, Offers: []protocol.ModelOffer{{Model: model, Taxonomy: tx}}}
	s.b.lastSeen[id] = time.Now()
	s.b.mu.Unlock()
	return s.b.db.BindNode(id, owner)
}

func (s *sharingViewState) mine() string {
	if s.b == nil {
		s.b, s.priv, _, _ = newBandBroker(s.t)
	}
	return hex.EncodeToString(s.priv.Public().(ed25519.PublicKey))
}

func tax(pair, tags, src string) protocol.Taxonomy {
	cat, sub := protocol.SplitCategory(pair)
	t := protocol.Taxonomy{Category: cat, Subcategory: sub, TaxonomySource: src}
	if tags != "" {
		t.Tags = strings.Split(tags, ",")
	}
	return t
}

func (s *sharingViewState) declared(model, pair, tags string) error {
	return s.station(s.mine(), model, tax(pair, tags, protocol.SourceDeclared))
}
func (s *sharingViewState) inferred(model, pair string) error {
	return s.station(s.mine(), model, tax(pair, "", protocol.SourceInferred))
}
func (s *sharingViewState) bare(model string) error {
	return s.station(s.mine(), model, protocol.Taxonomy{})
}
func (s *sharingViewState) other(model, pair, tags string) error {
	s.mine()
	_, otherPriv, _ := ed25519.GenerateKey(nil)
	return s.station(hex.EncodeToString(otherPriv.Public().(ed25519.PublicKey)), model, tax(pair, tags, protocol.SourceDeclared))
}

func (s *sharingViewState) get() error {
	r := httptest.NewRequest(http.MethodGet, "/provider/models", nil)
	signReq(r, s.priv, nil)
	w := httptest.NewRecorder()
	s.b.providerModels(w, r)
	if w.Code != http.StatusOK {
		return fmt.Errorf("GET /provider/models = %d: %s", w.Code, w.Body.String())
	}
	var v struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		return err
	}
	s.rows = v.Models
	return nil
}

func (s *sharingViewState) row(model string) (map[string]any, error) {
	for _, r := range s.rows {
		if r["model"] == model {
			return r, nil
		}
	}
	return nil, fmt.Errorf("no /provider/models row for %q", model)
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
		return strings.Join(out, ",")
	}
	return ""
}

func (s *sharingViewState) rowHas(model, cat, sub, tags, src string) error {
	r, err := s.row(model)
	if err != nil {
		return err
	}
	got := strings.Join([]string{str(r["category"]), str(r["subcategory"]), str(r["tags"]), str(r["taxonomy_source"])}, "|")
	want := strings.Join([]string{cat, sub, tags, src}, "|")
	if got != want {
		return fmt.Errorf("row %s: %q, got %q", model, want, got)
	}
	return nil
}
func (s *sharingViewState) noKeys(model, keys string) error {
	r, err := s.row(model)
	if err != nil {
		return err
	}
	for _, k := range strings.Split(keys, ",") {
		if _, ok := r[k]; ok {
			return fmt.Errorf("row %s carries key %q", model, k)
		}
	}
	return nil
}
func (s *sharingViewState) notListed(model string) error {
	if _, err := s.row(model); err == nil {
		return fmt.Errorf("another owner's %q is listed", model)
	}
	return nil
}

func TestSharingViewBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &sharingViewState{t: t}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = sharingViewState{t: t}
				return ctx, nil
			})
			q := `"([^"]*)"`
			sc.Step(`^an owner with a station sharing `+q+` declared as `+q+` with tags `+q+`$`, st.declared)
			sc.Step(`^an owner with a station sharing `+q+` inferred as `+q+`$`, st.inferred)
			sc.Step(`^an owner with a station sharing `+q+` with no taxonomy$`, st.bare)
			sc.Step(`^another owner with a station sharing `+q+` declared as `+q+` with tags `+q+`$`, st.other)
			sc.Step(`^the owner GETs /provider/models$`, st.get)
			sc.Step(`^the row for `+q+` has category `+q+`, subcategory `+q+`, tags `+q+` and source `+q+`$`, st.rowHas)
			sc.Step(`^the JSON row for `+q+` has none of the keys `+q+`$`, st.noKeys)
			sc.Step(`^no row for `+q+` is listed$`, st.notListed)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/categories/web_sharing_view.feature"},
			Tags:     "~@web",
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("categories/web_sharing_view (broker) behavior scenarios failed (see godog output above)")
	}
}
