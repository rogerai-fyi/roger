package main

// generation_keys_bdd_test.go: the key-funded /generation record (generation_lookup.feature,
// "A key-funded request records key_id ..."). Alice is a logged-in account that mints a real
// key through /account/keys and relays with it as the bearer; the record is then read as the
// payer and as the serving station's payout owner.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"

	"github.com/cucumber/godog"
)

func (g *gl3State) registerKeyRecord(sc *godog.ScenarioContext) {
	sc.Step(`^"(alice)" holds key "([^"]+)" with limit_usd ([0-9.]+) reset (daily|weekly|monthly|none)$`, g.holdsKey)
	sc.Step(`^"(alice)" makes a request for "([^"]+)" funded by "([^"]+)" served by "([^"]+)"$`, g.keyFundedServed)
	sc.Step(`^key_id is "([^"]+)"$`, g.keyIDIs)
	sc.Step(`^key_limit is ([0-9.]+)$`, func(v string) error { return g.recNum("key_limit", v) })
	sc.Step(`^key_spend_after equals the key's spend in the window after this settle$`, g.keySpendAfter)
	sc.Step(`^"([^"]+)", the owner of "([^"]+)", GETs /generation for it as the payout owner$`, func(owner, station string) error {
		g.ownerOf(owner, station)
		g.get(g.reqID, owner)
		return nil
	})
}

// holdsKey makes label a logged-in account and mints the key through /account/keys.
func (g *gl3State) holdsKey(label, key, limit, reset string) error {
	delete(g.who, label)
	w := g.boundAccount(label, 10)
	lim, _ := strconv.ParseFloat(limit, 64)
	body, _ := json.Marshal(map[string]any{"name": key, "limit_usd": lim, "reset": reset})
	r := httptest.NewRequest(http.MethodPost, "/account/keys", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	signReq(r, w.priv, body)
	rr := httptest.NewRecorder()
	g.b.routes().ServeHTTP(rr, r)
	var res struct {
		ID, Secret string
	}
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &res) != nil || res.Secret == "" {
		return fmt.Errorf("minting %s = %d: %s", key, rr.Code, rr.Body.String())
	}
	if g.keyIDs == nil {
		g.keyIDs = map[string]string{}
	}
	g.keyIDs[key] = res.ID
	g.who[label+":"+key] = &gl3Who{kind: "grant", token: res.Secret, wallet: w.wallet}
	return nil
}

func (g *gl3State) keyFundedServed(label, model, key, node string) error {
	g.model = model
	g.scriptJSON(node, 20, "", 0)
	g.landOn(node)
	if err := g.relayAs(label+":"+key, false); err != nil {
		return err
	}
	g.reqIDs[label] = g.reqID
	return g.servedBy(node)
}

func (g *gl3State) keyIDIs(key string) error {
	want := g.keyIDs[key]
	if got, _ := sr3Path(g.rec, "key_id"); want == "" || got != want {
		return fmt.Errorf("key_id is %v, want %s's id %q (record %s)", got, key, want, g.genBody)
	}
	return nil
}

func (g *gl3State) recNum(field, want string) error {
	w, _ := strconv.ParseFloat(want, 64)
	got, ok := kg5Num(g.rec[field])
	if !ok || math.Abs(got-w) > 1e-9 {
		return fmt.Errorf("%s is %v, want %s", field, g.rec[field], want)
	}
	return nil
}

func (g *gl3State) keySpendAfter() error {
	id := g.keyIDs["key_a1"]
	k, ok, err := g.db.AccountKeyByID(id)
	if err != nil || !ok {
		return fmt.Errorf("key %s: %v", id, err)
	}
	from, until, _ := keyWindow(k, g.b.now())
	spend, err := g.db.KeySpend(id, from, until)
	if err != nil {
		return err
	}
	got, ok := kg5Num(g.rec["key_spend_after"])
	if !ok || spend <= 0 || math.Abs(got-round6(spend)) > 1e-9 {
		return fmt.Errorf("key_spend_after is %v, the key's window spend is %v", g.rec["key_spend_after"], spend)
	}
	return nil
}
