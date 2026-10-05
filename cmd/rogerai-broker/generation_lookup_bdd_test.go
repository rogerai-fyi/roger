package main

// generation_lookup_bdd_test.go makes features/routing/generation_lookup.feature EXECUTABLE
// against the REAL broker (the stream-receipt runner's harness: rpState/foState - a real
// relayBroker over the real store, Postgres when ROGERAI_TEST_DATABASE_URL is set, a real
// shared store over miniredis, real stations behind scripted model upstreams) and reads
// GET /generation through the broker's own mux (b.routes()), as every other public route.
// It also runs the two @slice3 scenarios of features/routing/model_fallback_list.feature with
// that runner's own steps (TestGenerationLookupBDD/model_fallback_list).
//
// Observation points (stated):
//   - The request id of a SERVED relay is the shortest job id its stations were dispatched
//     (sr3State.resolveReqID); of a relay still in flight, the id in its routing log line. A
//     relay that dispatched nothing (a refusal) must hand the consumer its id: the runner reads
//     X-RogerAI-Request-Id, then the error body's "request_id"/"id" - the contract (§7) does not
//     say which, so a refusal that names none fails the Given that needs it.
//   - Identities: "alice" is the harness's signed, GitHub-bound consumer; "carol"/"bob" own
//     "n-1" and "dave" owns "n-2" (signed with the station owner's key, the payout-owner door);
//     "eve" and "frank" are fresh GitHub-bound accounts; grants are real minted grants; a
//     Playbox session is a real signed session cookie behind the allow-listed Origin.
//   - "no store read happened": the lookup runs with the broker's store swapped for one whose
//     every method panics, and the shared store's command count must not move.
//   - "the same store read ... as the foreign-id case": the shared store's command count for an
//     unknown-id lookup equals the count for a foreign-id lookup (and is not zero). The contract
//     has the record readable across instances, i.e. in the shared store.
//   - "the record store refuses writes": the shared store goes unreachable (same reason).
//   - 404 assertions are never vacuous: each also checks the request's own identity reads 200.
//
// Client scenarios (`roger generation`, the TUI pane) are @cli / @tui, the Playbox inspector is
// @web, OpenAPI is @docs, and the key-funded scenario is @slice5 (no key object exists yet).

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// gl3Who is one identity a lookup (or a relay) is made as.
type gl3Who struct {
	kind   string // "sign", "anon", "grant", "cookie"
	priv   ed25519.PrivateKey
	token  string
	cookie string
	wallet string
	login  string
}

type gl3State struct {
	gl3LastBody string // the last raw relay body (early-refusal steps)
	*sr3State

	who       map[string]*gl3Who
	grants    map[string]string // grant label -> secret
	reqIDs    map[string]string // label -> request id
	relayHdr  http.Header       // the response the relay that made the request carried
	relayCode int

	genCode int
	genBody []byte
	genHdr  http.Header
	rec     map[string]any
	lastWho string
	lastID  string
	on      *broker // the instance the last lookup went to

	inflight                 chan struct{}
	flightDone               chan struct{}
	served                   int
	earnBefore               float64
	cmdsDelta                int
	panicked                 bool
	knownIP                  string
	modSrv                   *httptest.Server
	scrn                     *screener
	foreignCmds, unknownCmds int
	mrClosed                 bool
	proofWho, proofID        string // the identity and id that must read 200 for a 404 to mean anything
	aliceBody                []byte
}

func (g *gl3State) reset() error {
	if g.modSrv != nil {
		g.modSrv.Close()
		g.modSrv = nil
	}
	if g.scrn != nil {
		g.scrn.shutdown(2 * time.Second)
		g.scrn = nil
	}
	if err := g.sr3State.reset(); err != nil {
		return err
	}
	g.b.seedFunds = 0
	g.who = map[string]*gl3Who{"alice": {kind: "sign", priv: g.consumerPriv, wallet: g.wallet, login: "buyer-" + g.nonce}}
	g.grants, g.reqIDs = map[string]string{}, map[string]string{}
	g.relayHdr, g.relayCode = nil, 0
	g.genCode, g.genBody, g.genHdr, g.rec, g.lastWho, g.lastID, g.on = 0, nil, nil, nil, "", "", nil
	g.inflight, g.flightDone = nil, nil
	g.cmdsDelta, g.panicked, g.knownIP, g.mrClosed = 0, false, "", false
	g.proofWho, g.proofID = "", ""
	return nil
}

func (g *gl3State) teardown() {
	if g.inflight != nil {
		select {
		case <-g.inflight:
		default:
			close(g.inflight)
		}
		if g.flightDone != nil {
			<-g.flightDone
		}
		g.inflight = nil
	}
	if g.scrn != nil {
		g.scrn.shutdown(2 * time.Second)
		g.scrn = nil
	}
	if g.modSrv != nil {
		g.modSrv.Close()
		g.modSrv = nil
	}
	g.sr3State.teardown()
}

// --- identities ----------------------------------------------------------------------------

func (g *gl3State) boundAccount(label string, fund float64) *gl3Who {
	if w, ok := g.who[label]; ok {
		return w
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	gid, _ := rand.Int(rand.Reader, big.NewInt(1<<30))
	id := gid.Int64() + 2_000_000
	login := label + "-" + g.nonce
	if err := g.db.BindOwner(store.Owner{GitHubID: id, Login: login, Pubkey: pub}); err != nil {
		g.t.Fatalf("bind %s: %v", label, err)
	}
	w := &gl3Who{kind: "sign", priv: priv, wallet: "u_gh_" + strconv.FormatInt(id, 10), login: login}
	if fund > 0 {
		if _, err := g.db.AddCredits(w.wallet, fund); err != nil {
			g.t.Fatalf("fund %s: %v", label, err)
		}
	}
	g.who[label] = w
	return w
}

// ownerOf resolves an owner name to the station it owns in this scenario.
func (g *gl3State) ownerOf(label, station string) *gl3Who {
	st := g.st(station)
	w := &gl3Who{kind: "sign", priv: st.ownerPriv}
	g.who[label] = w
	return w
}

func (g *gl3State) mintGrant(label, ownerStation string, free bool) error {
	st := g.st(ownerStation)
	secret := "rog-grant_" + label + "_" + g.nonce
	sum := sha256.Sum256([]byte(secret))
	gr := store.Grant{ID: "grant_" + strings.ToLower(label) + "_" + g.nonce, SecretHash: hex.EncodeToString(sum[:]), Owner: st.acct,
		Label: "gl3-" + label, Nodes: []string{st.id}, Free: free, CreatedAt: time.Now().Unix()}
	if !free {
		gr.PriceIn, gr.PriceOut = 0, st.priceOut
	}
	if err := g.db.CreateGrant(gr); err != nil {
		return err
	}
	if err := rs1GrantWalletRow(g.db, gr); err != nil {
		return err
	}
	g.grants[label] = secret
	g.who["grant:"+label] = &gl3Who{kind: "grant", token: secret, wallet: "g_" + gr.ID}
	return nil
}

func (g *gl3State) authAs(w *gl3Who) func(r *http.Request, body []byte) {
	return func(r *http.Request, body []byte) {
		switch w.kind {
		case "grant":
			r.Header.Set("Authorization", "Bearer "+w.token)
		case "cookie":
			r.Header.Set("Origin", pbOrigin)
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: w.cookie})
		case "anon":
		default:
			signReq(r, w.priv, body)
		}
	}
}

// --- relays --------------------------------------------------------------------------------

// scriptJSON makes station name answer a non-stream request with `completion` claimed tokens
// (and a content marker for the runner's tokenizer), or an SSE stream when asked to stream.
func (g *gl3State) scriptJSON(name string, completion int, content string, delay time.Duration) {
	g.scripted[name] = true
	if content == "" {
		content = sr3Marker + " answer from " + name
	}
	g.st(name).set(func(_ int, w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		if bytes.Contains(buf.Bytes(), []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", content)
			fmt.Fprint(w, sr3Usage(40, completion)+"\n\n")
			fmt.Fprint(w, sr3Done+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":40,"completion_tokens":%d}}`, content, completion)
	})
}

// relayAs makes one relay as identity label, records its request id under label.
func (g *gl3State) relayAs(label string, stream bool) error {
	w := g.who[label]
	if w == nil {
		return fmt.Errorf("no identity %q in this scenario", label)
	}
	if w.kind == "sign" && label == "alice" {
		if err := g.ensureFunded(); err != nil {
			return err
		}
		g.auth = nil
	} else {
		g.auth = g.authAs(w)
		g.funded = true
	}
	if w.wallet != "" {
		g.wallet = w.wallet
	}
	err := g.send(stream)
	g.auth = nil
	if err != nil {
		return err
	}
	g.relayHdr, g.relayCode = g.lastHdr.Clone(), g.lastCode
	if g.reqID == "" {
		g.reqID = g.refusalID()
	}
	g.reqIDs[label] = g.reqID
	if g.reqID != "" && g.lastCode == 200 {
		g.proofWho, g.proofID = label, g.reqID
	}
	return nil
}

// refusalID is the request id a relay that dispatched nothing handed back, or "".
func (g *gl3State) refusalID() string {
	if v := g.lastHdr.Get("X-RogerAI-Request-Id"); v != "" {
		return v
	}
	var m map[string]any
	if json.Unmarshal(g.lastBody, &m) == nil {
		for _, k := range []string{"request_id", "id"} {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
		if e, ok := m["error"].(map[string]any); ok {
			if v, ok := e["request_id"].(string); ok && v != "" {
				return v
			}
		}
	}
	return ""
}

func (g *gl3State) needID() error {
	if g.reqID == "" {
		return fmt.Errorf("the relay (status %d) handed the consumer no request id to look up (no X-RogerAI-Request-Id, no request_id in the body): %.300s", g.lastCode, g.lastBody)
	}
	return nil
}

func (g *gl3State) servedBy(name string) error {
	if g.lastCode != 200 || g.lastHdr.Get("X-RogerAI-Provider") != g.st(name).id {
		return fmt.Errorf("relay = %d from %q, want 200 from %s: %.300s", g.lastCode, g.lastHdr.Get("X-RogerAI-Provider"), name, g.lastBody)
	}
	return nil
}

// --- Background ---------------------------------------------------------------------------

func (g *gl3State) twoOnAir(a, b, model, out string) error {
	g.model = model
	g.station(a, 0, sr3f(out))
	g.station(b, 0, sr3f(out))
	return nil
}

func (g *gl3State) loggedInAlice(_ string) error { return g.ensureFunded() }

// --- Given: requests ----------------------------------------------------------------------

func (g *gl3State) aliceNonStreamAt(model, name, cost string) error {
	g.model = model
	c := int(math.Round(sr3f(cost) / (g.st(name).priceOut / 1e6)))
	g.scriptJSON(name, c, "", 0)
	g.landOn(name)
	if err := g.relayAs("alice", false); err != nil {
		return err
	}
	if err := g.servedBy(name); err != nil {
		return err
	}
	if got := g.lastHdr.Get("X-RogerAI-Cost"); got != fmtCostHeader(sr3f(cost)) {
		return fmt.Errorf("the request cost %s, want %s", got, cost)
	}
	return nil
}

func (g *gl3State) aliceServedAt(name, cost string) error {
	return g.aliceNonStreamAt(g.model, name, cost)
}

func (g *gl3State) aliceStream(model, name string) error {
	g.model = model
	g.scriptJSON(name, 20, "", 0)
	g.landOn(name)
	if err := g.relayAs("alice", true); err != nil {
		return err
	}
	return g.servedBy(name)
}

func (g *gl3State) answers429RA(name, ra string) error {
	g.scripted[name] = true
	g.st(name).script429(ra)
	return nil
}

func (g *gl3State) failsOverTo(model, to string) error {
	g.model = model
	if !g.scripted[to] {
		g.scriptJSON(to, 20, "", 0)
	}
	g.landOn("n-1")
	if err := g.relayAs("alice", false); err != nil {
		return err
	}
	return g.servedBy(to)
}

func (g *gl3State) slowFailover(_ string, a502ms int, _ string, b200ms int) error {
	g.scripted["n-1"] = true
	g.st("n-1").set(func(_ int, w http.ResponseWriter, _ *http.Request) {
		time.Sleep(time.Duration(a502ms) * time.Millisecond)
		w.WriteHeader(502)
		_, _ = w.Write([]byte(utDefaultBody(502)))
	})
	g.scriptJSON("n-2", 20, "", time.Duration(b200ms)*time.Millisecond)
	return nil
}

func (g *gl3State) failsOverFromTo(from, to string) error {
	g.landOn(from)
	if err := g.relayAs("alice", false); err != nil {
		return err
	}
	return g.servedBy(to)
}

func (g *gl3State) aliceFallback(model, list, name string) error {
	var models []string
	if err := json.Unmarshal([]byte(list), &models); err != nil {
		return err
	}
	g.model = model
	g.extraBody = map[string]any{"models": models}
	if !g.scripted[name] {
		g.scriptJSON(name, 20, "", 0)
	}
	g.landOn(name)
	if err := g.relayAs("alice", false); err != nil {
		return err
	}
	return g.servedBy(name)
}

func (g *gl3State) aliceWalletServed(model, name string) error {
	g.model = model
	g.scriptJSON(name, 20, "", 0)
	g.landOn(name)
	if err := g.relayAs("alice", false); err != nil {
		return err
	}
	return g.servedBy(name)
}

func (g *gl3State) aliceMinTPS(model, v string) error {
	g.model = model
	for _, n := range []string{"n-1", "n-2"} {
		g.setTPS(g.st(n).id, 10) // measured, and below the floor: nothing qualifies
	}
	g.minTPS = v
	if err := g.relayAs("alice", false); err != nil {
		return err
	}
	if g.lastCode != 503 {
		return fmt.Errorf("the min-tps relay = %d, want the 503 no_match it describes: %.300s", g.lastCode, g.lastBody)
	}
	return g.needID()
}

func (g *gl3State) bothCooling(a, b string, secs int) error {
	if err := g.cool(a, secs); err != nil {
		return err
	}
	return g.cool(b, secs)
}

func (g *gl3State) modServer(flagged bool) {
	g.modSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if flagged {
			_, _ = w.Write([]byte(`{"results":[{"flagged":true,"categories":{"S1":true}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"flagged":false}]}`))
	}))
}

func (g *gl3State) moderationSync() error {
	g.modServer(true)
	g.b.mod = moderation{provider: "url", url: g.modSrv.URL, client: g.modSrv.Client(), csamCats: loadCSAMCategories(""), mode: modeSync}
	return nil
}

func (g *gl3State) aliceRejected451() error {
	g.scriptJSON("n-1", 20, "", 0)
	if err := g.relayAs("alice", false); err != nil {
		return err
	}
	if g.lastCode != 451 {
		return fmt.Errorf("the screened relay = %d, want 451: %.300s", g.lastCode, g.lastBody)
	}
	return g.needID()
}

func (g *gl3State) moderationAsync() error {
	g.modServer(true)
	g.b.mod = moderation{provider: "url", url: g.modSrv.URL, client: g.modSrv.Client(), csamCats: loadCSAMCategories(""), mode: modeAsync}
	g.scrn = newScreener(g.b, defaultScreenerConfig())
	g.b.scr = g.scrn
	g.scrn.start(1)
	return nil
}

func (g *gl3State) servedThenFlagged() error {
	g.scriptJSON("n-1", 20, "", 0)
	g.landOn("n-1")
	if err := g.relayAs("alice", false); err != nil {
		return err
	}
	if err := g.servedBy("n-1"); err != nil {
		return err
	}
	g.scrn.shutdown(3 * time.Second) // drain: the verdict lands off the response path
	g.scrn = nil
	return nil
}

func (g *gl3State) aliceSortAndPref(sortv, pref string) error {
	g.extraBody = map[string]any{"provider": map[string]any{"sort": sortv}, "roger": map[string]any{"pref": pref}}
	if err := g.relayAs("alice", false); err != nil {
		return err
	}
	if g.lastCode != 400 {
		return fmt.Errorf("the conflicting relay = %d, want 400: %.300s", g.lastCode, g.lastBody)
	}
	return g.needID()
}

func (g *gl3State) walletCannotCover() error {
	w := g.boundAccount("alice-poor", 0)
	g.who["alice"] = w
	g.consumerPriv, g.wallet = w.priv, w.wallet
	g.funded = true
	return nil
}

func (g *gl3State) aliceRequestAny(model string) error {
	g.model = model
	for n := range g.stations {
		if !g.scripted[n] {
			g.scriptJSON(n, 20, "", 0)
		}
	}
	w := g.who["alice"]
	g.auth = g.authAs(w)
	g.wallet = w.wallet
	err := g.send(false)
	g.auth = nil
	if err != nil {
		return err
	}
	if g.reqID == "" {
		g.reqID = g.refusalID()
	}
	g.reqIDs["alice"] = g.reqID
	return nil
}

func (g *gl3State) disconnectBills(cost string) error {
	c := int(math.Round(sr3f(cost) / (g.st("n-1").priceOut / 1e6)))
	// Since §14.10 a disconnect bills the FORWARDED text, recounted: the sidecar counts what
	// reached the consumer as c tokens (prompt recounts to 0 so the cost is the completion's).
	g.recCompletion = c
	g.ctxCancel = true
	// The forwarded frame carries text the size of c tokens (~4 chars each), so the bill of
	// what reached the consumer is c tokens however it is counted.
	long := fmt.Sprintf(`data: {"choices":[{"delta":{"content":"%s %s"}}]}`, sr3Marker, strings.Repeat("word ", c*4/5))
	g.sse("n-1", []sr3Frame{{line: long}, {line: sr3Content(2, "n-1"), sleep: 300 * time.Millisecond},
		{line: sr3Usage(40, c), sleep: 50 * time.Millisecond}, {line: sr3Done}})
	g.landOn("n-1")
	if err := g.relayAs("alice", true); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if d, n, _ := g.debit(); n > 0 && d > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the disconnected stream never settled")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (g *gl3State) emptyThenServes(a, b string) error {
	g.scripted[a] = true
	g.st(a).set(func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":""}}],"usage":{"prompt_tokens":0,"completion_tokens":0}}`))
	})
	g.scriptJSON(b, 20, "", 0)
	g.landOn(a)
	return nil
}

func (g *gl3State) aliceRequestServedOrAny(model string) error {
	if err := g.aliceRequestAny(model); err != nil {
		return err
	}
	return g.needID()
}

func (g *gl3State) aliceBridged(model string) error {
	g.model = model
	if err := g.relayAs("alice", false); err != nil {
		return err
	}
	if g.lastCode != 200 || g.lastHdr.Get("X-RogerAI-Relay") == "" {
		return fmt.Errorf("the relay = %d relay=%q, want 200 through the bridge: %.300s", g.lastCode, g.lastHdr.Get("X-RogerAI-Relay"), g.lastBody)
	}
	return g.needID()
}

func (g *gl3State) bobFreeGrant(_ string, model, node string) error {
	g.model = model
	g.ownerOf("bob", node)
	return g.mintGrant("that grant", node, true)
}

func (g *gl3State) bearerServed(label, node string) error {
	g.scriptJSON(node, 20, "", 0)
	g.landOn(node)
	if err := g.relayAs("grant:"+label, false); err != nil {
		return err
	}
	return g.servedBy(node)
}

func (g *gl3State) aliceMadeServedBy(name string) error {
	if !g.scripted[name] {
		g.scriptJSON(name, 20, "", 0)
	}
	g.landOn(name)
	if err := g.relayAs("alice", false); err != nil {
		return err
	}
	return g.servedBy(name)
}

func (g *gl3State) playboxServed(name string) error {
	g.t.Setenv("ROGERAI_WEB_ORIGIN", pbOrigin)
	g.t.Setenv("ROGERAI_WEB_ORIGINS", "")
	gid, _ := rand.Int(rand.Reader, big.NewInt(1<<30))
	id := gid.Int64() + 3_000_000
	login := "pb-" + g.nonce
	_, priv, _ := ed25519.GenerateKey(nil)
	if err := g.db.BindOwner(store.Owner{GitHubID: id, Login: login, Pubkey: hex.EncodeToString(priv.Public().(ed25519.PublicKey))}); err != nil {
		return err
	}
	wallet := "u_gh_" + strconv.FormatInt(id, 10)
	if _, err := g.db.AddCredits(wallet, 10); err != nil {
		return err
	}
	g.who["browser"] = &gl3Who{kind: "cookie", cookie: g.b.signSession(login, id, time.Now().Add(time.Hour).Unix()), wallet: wallet, login: login}
	g.scriptJSON(name, 20, "", 0)
	g.landOn(name)
	if err := g.relayAs("browser", false); err != nil {
		return err
	}
	return g.servedBy(name)
}

func (g *gl3State) bearerOfGrantServed(label, node string) error {
	if _, ok := g.grants[label]; !ok {
		g.ownerOf("bob", node)
		if err := g.mintGrant(label, node, true); err != nil {
			return err
		}
	}
	return g.bearerServed(label, node)
}

func (g *gl3State) twoGrants(a, b, _ string) error {
	g.ownerOf("bob", "n-1")
	if err := g.mintGrant(a, "n-1", true); err != nil {
		return err
	}
	return g.mintGrant(b, "n-1", true)
}

func (g *gl3State) bobGrantServed(label string) error {
	g.ownerOf("bob", "n-1")
	if err := g.mintGrant(label, "n-1", true); err != nil {
		return err
	}
	return g.bearerServed(label, "n-1")
}

func (g *gl3State) aliceServedOwnedBy(name, owner string) error {
	g.ownerOf(owner, name)
	return g.aliceMadeServedBy(name)
}

func (g *gl3State) carolDave(a, oa, b, ob string) error {
	g.ownerOf(oa, a)
	g.ownerOf(ob, b)
	g.scripted[a] = true
	g.st(a).script429("")
	g.scriptJSON(b, 20, "", 0)
	g.landOn(a)
	if err := g.relayAs("alice", false); err != nil {
		return err
	}
	return g.servedBy(b)
}

func (g *gl3State) failedOverOnce() error {
	g.scripted["n-1"] = true
	g.st("n-1").script429("")
	g.scriptJSON("n-2", 20, "", 0)
	return g.failsOverFromTo("n-1", "n-2")
}

func (g *gl3State) markers(p, c string) error {
	g.extraBody = map[string]any{"messages": []map[string]any{{"role": "user", "content": "please answer " + p + " now"}}}
	g.scriptJSON("n-1", 20, sr3Marker+" "+c+" done", 0)
	return g.aliceMadeServedBy("n-1")
}

func (g *gl3State) privateBand() error {
	st := g.st("n-1")
	g.b.private[st.id] = true
	code := "147.520 MHz · GLBC-" + strings.ToUpper(g.nonce[:4])
	g.freq = code
	g.extraBody = map[string]any{"roger": map[string]any{"freq": code}}
	if err := g.db.CreateBand(store.Band{ID: "band_" + g.nonce, CodeHash: protocol.BandCodeHash(code), CodeDisplay: "147.520 MHz · ••••-••••",
		Owner: st.acct, NodeID: st.id, CreatedAt: time.Now().Unix()}); err != nil {
		return err
	}
	return g.aliceMadeServedBy("n-1")
}

// knownIP: stations in this harness are injected, not registered over HTTP, so the address
// the broker could leak is the station's model endpoint the harness serves it from.
func (g *gl3State) registeredFromIP(name string) error {
	g.knownIP = strings.TrimPrefix(g.st(name).up.URL, "http://")
	return nil
}

func (g *gl3State) aliceEveEach(name string) error {
	if err := g.aliceMadeServedBy(name); err != nil {
		return err
	}
	g.boundAccount("eve", 10)
	if err := g.relayAs("eve", false); err != nil {
		return err
	}
	if err := g.servedBy(name); err != nil {
		return err
	}
	g.reqID = g.reqIDs["alice"]
	g.wallet = g.who["alice"].wallet
	return nil
}

// noLongerHeld: there is no retention window in code (the ledger keeps rows until account
// deletion), so the closest real case is a well-formed id with no lineage; the scenario's 404
// is checked against a real request of alice's reading 200.
func (g *gl3State) noLongerHeld() error {
	if err := g.aliceMadeServedBy("n-1"); err != nil {
		return err
	}
	g.reqIDs["held"] = g.reqID
	g.proofWho, g.proofID = "alice", g.reqID
	g.reqID = "00000000deadbeef"
	delete(g.reqIDs, "alice") // or "alice GETs ... for it" would swap her live id back in
	return nil
}

func (g *gl3State) aliceDeletes() error {
	// readable before the deletion, or "unreadable after" proves nothing
	g.get(g.reqID, "alice")
	if g.genCode != 200 {
		return fmt.Errorf("before the deletion alice reads %d - the record must be readable first", g.genCode)
	}
	g.proofWho = ""
	ok, err := g.db.DeleteAccount(g.who["alice"].login)
	if err != nil || !ok {
		return fmt.Errorf("DeleteAccount(%s) ok=%v err=%v", g.who["alice"].login, ok, err)
	}
	return nil
}

func (g *gl3State) exhaustLimiter() error {
	if err := g.aliceMadeServedBy("n-1"); err != nil {
		return err
	}
	g.b.rl = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 0.0001, burst: 1}
	g.get(g.reqID, "alice")
	return nil
}

func (g *gl3State) aliceCached() error {
	if err := g.aliceMadeServedBy("n-1"); err != nil {
		return err
	}
	g.get(g.reqID, "alice")
	return g.requireRecord()
}

func (g *gl3State) inFlight() error {
	release := make(chan struct{})
	g.inflight = release
	g.scripted["n-1"] = true
	g.st("n-1").set(func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w, sr3Content(1, "n-1")+"\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
		fmt.Fprint(w, sr3Usage(40, 20)+"\n\n"+sr3Done+"\n\n")
	})
	g.landOn("n-1")
	if err := g.ensureFunded(); err != nil {
		return err
	}
	mark := len(g.logs.String())
	done := make(chan struct{})
	g.flightDone = done
	go func() {
		defer close(done)
		_ = g.send(true)
	}()
	deadline := time.Now().Add(5 * time.Second)
	re := regexp.MustCompile(`routing request=([0-9a-f]{16})`)
	for {
		if m := re.FindStringSubmatch(g.logs.String()[mark:]); m != nil {
			g.reqIDs["flight"] = m[1]
			g.lastID = m[1]
			g.reqID = m[1] // "GETs /generation for it" reads g.reqID: the in-flight id, not a stale one
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the in-flight request never reached its routing pass")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (g *gl3State) streamEnds() error {
	if g.inflight == nil {
		return fmt.Errorf("no stream is in flight")
	}
	close(g.inflight)
	<-g.flightDone
	g.inflight = nil
	g.reqID = g.reqIDs["flight"]
	return nil
}

func (g *gl3State) twoInstanceShared() error {
	g.instanceB()
	return nil
}

func (g *gl3State) servedOnA() error { return g.aliceMadeServedBy("n-1") }

// crossInstanceAttempts: a failover whose second attempt is DISPATCHED by another instance
// needs a node long-polling that instance; the harness's stations answer one broker's tunnel,
// so this cannot be stood up here. Failing honestly beats a single-instance stand-in.
func (g *gl3State) crossInstanceAttempts() error {
	return fmt.Errorf("cannot construct: attempt 2 dispatched by instance B needs a station long-polling B; the harness stations answer instance A's tunnel only")
}

func (g *gl3State) sharedDown() error {
	if err := g.aliceMadeServedBy("n-1"); err != nil {
		return err
	}
	g.mr.Close()
	g.mrClosed = true
	return nil
}

func (g *gl3State) recordStoreRefuses() error {
	g.mr.Close()
	g.mrClosed = true
	return nil
}

// --- lookups --------------------------------------------------------------------------------

func (g *gl3State) do(b *broker, method, url, who string) {
	r := httptest.NewRequest(method, url, nil)
	if w := g.who[who]; w != nil {
		g.authAs(w)(r, nil)
	}
	rr := httptest.NewRecorder()
	before := 0
	if g.mr != nil && !g.mrClosed {
		before = g.mr.CommandCount()
	}
	func() {
		defer func() {
			if p := recover(); p != nil {
				g.panicked = true
				rr = httptest.NewRecorder()
				rr.WriteHeader(599)
			}
		}()
		b.routes().ServeHTTP(rr, r)
	}()
	if g.mr != nil && !g.mrClosed {
		g.cmdsDelta = g.mr.CommandCount() - before
	}
	g.genCode, g.genBody, g.genHdr = rr.Code, rr.Body.Bytes(), rr.Header()
	g.rec = nil
	_ = json.Unmarshal(g.genBody, &g.rec)
	g.lastWho, g.on = who, b
}

func (g *gl3State) get(id, who string) {
	g.lastID = id
	g.do(g.b, http.MethodGet, "/generation?id="+id, who)
}

func (g *gl3State) requireRecord() error {
	if g.genCode != 200 || g.rec == nil {
		return fmt.Errorf("GET /generation = %d %.300s", g.genCode, g.genBody)
	}
	return nil
}

func (g *gl3State) field(path string) (any, error) {
	if err := g.requireRecord(); err != nil {
		return nil, err
	}
	v, ok := sr3Path(g.rec, path)
	if !ok {
		return nil, fmt.Errorf("the record has no %s: %s", path, g.genBody)
	}
	return v, nil
}

func (g *gl3State) attempts() ([]map[string]any, error) {
	v, err := g.field("attempts")
	if err != nil {
		return nil, err
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("attempts is %T, not a list", v)
	}
	var out []map[string]any
	for _, a := range arr {
		m, _ := a.(map[string]any)
		out = append(out, m)
	}
	return out, nil
}

var gl3WellFormed = regexp.MustCompile(`^[0-9a-f]{16}$`)

// aliceGets: a well-formed id is looked up normally; anything else runs against the
// store-that-panics, so "no store read happened" is observable for a malformed id.
func (g *gl3State) aliceGets(who, id string) error {
	switch id {
	case "<that request id>":
		id = g.reqID
	default:
		id = strings.ReplaceAll(id, "<id>", g.reqID)
	}
	if !gl3WellFormed.MatchString(id) {
		return g.malformedGet(id)
	}
	g.get(id, who)
	return nil
}

func (g *gl3State) getsForIt(who string) error {
	if who == "alice" && g.reqIDs["alice"] != "" {
		g.reqID = g.reqIDs["alice"]
	}
	g.get(g.reqID, who)
	return nil
}

func (g *gl3State) ownerGets(who string) error {
	if g.who[who] == nil {
		return fmt.Errorf("no owner %q in this scenario", who)
	}
	g.get(g.reqID, who)
	return nil
}

func (g *gl3State) eveGets() error {
	g.boundAccount("eve", 10)
	g.get(g.reqID, "eve")
	return nil
}

func (g *gl3State) frankGets() error {
	g.boundAccount("frank", 0)
	g.get(g.reqID, "frank")
	return nil
}

func (g *gl3State) anonGets() error {
	g.who["anon"] = &gl3Who{kind: "anon"}
	g.get(g.reqID, "anon")
	return nil
}

func (g *gl3State) noIDGet() error {
	g.do(g.b, http.MethodGet, "/generation", "alice")
	return nil
}

func (g *gl3State) postGet() error {
	if g.reqID == "" {
		g.reqID = "0123456789abcdef"
	}
	g.do(g.b, http.MethodPost, "/generation?id="+g.reqID, "alice")
	return nil
}

func (g *gl3State) getsOnB() error {
	if g.b2 == nil {
		return fmt.Errorf("no instance B")
	}
	g.lastID = g.reqID
	g.do(g.b2, http.MethodGet, "/generation?id="+g.reqID, "alice")
	return nil
}

func (g *gl3State) getsForIPRequest(name string) error {
	if err := g.aliceMadeServedBy(name); err != nil {
		return err
	}
	g.get(g.reqID, "alice")
	return nil
}

func (g *gl3State) getsThreeTimes() error {
	if err := g.aliceMadeServedBy("n-1"); err != nil {
		return err
	}
	for i := 0; i < 3; i++ {
		g.get(g.reqID, "alice")
	}
	return nil
}

func (g *gl3State) getsForARequest() error {
	if err := g.aliceMadeServedBy("n-1"); err != nil {
		return err
	}
	g.served = g.successOf("n-1")
	g.earnBefore, _ = g.db.EarningsOf(g.st("n-1").id)
	g.get(g.reqID, "alice")
	return nil
}

func (g *gl3State) successOf(name string) int {
	g.b.metricsMu.Lock()
	defer g.b.metricsMu.Unlock()
	return g.b.successCount[g.st(name).id]
}

// --- Then: generic -------------------------------------------------------------------------

// statusIs: a 404 only means "scoped out" when the request's own maker can read the record,
// so a 404 is checked against that read too (a broker with no /generation 404s everyone).
func (g *gl3State) statusIs(code int) error {
	if g.genCode != code {
		return fmt.Errorf("GET /generation = %d, want %d: %.300s", g.genCode, code, g.genBody)
	}
	if code == http.StatusNotFound && g.proofWho != "" {
		saved := []any{g.genCode, g.genBody, g.genHdr, g.rec, g.lastWho, g.lastID}
		g.get(g.proofID, g.proofWho)
		ok := g.genCode == 200
		pc := g.genCode
		g.genCode, g.genBody, g.genHdr = saved[0].(int), saved[1].([]byte), saved[2].(http.Header)
		g.rec, _ = saved[3].(map[string]any)
		g.lastWho, g.lastID = saved[4].(string), saved[5].(string)
		if !ok {
			return fmt.Errorf("the request's own maker (%s) reads %d - the 404 proves nothing until the record is readable", g.proofWho, pc)
		}
	}
	return nil
}

var gl3KV = regexp.MustCompile(`([a-z_]+) ("[^"]*"|[^,{} ]+)`)

// matchKV checks a record object against "k v, k "v", ..." (station names map to ids).
func (g *gl3State) matchKV(obj map[string]any, spec string) error {
	for _, m := range gl3KV.FindAllStringSubmatch(spec, -1) {
		k, v := m[1], m[2]
		got, ok := obj[k]
		if !ok {
			return fmt.Errorf("%v has no %q", obj, k)
		}
		if strings.HasPrefix(v, `"`) {
			want := g.nameID(strings.Trim(v, `"`))
			if s, _ := got.(string); s != want {
				return fmt.Errorf("%s = %v, want %q", k, got, want)
			}
			continue
		}
		f, _ := got.(float64)
		if !approx(f, sr3f(v)) {
			return fmt.Errorf("%s = %v, want %s", k, got, v)
		}
	}
	return nil
}

func (g *gl3State) numIs(path, want string) error {
	v, err := g.field(path)
	if err != nil {
		return err
	}
	f, ok := v.(float64)
	if !ok || !approx(f, sr3f(want)) {
		return fmt.Errorf("%s = %v, want %s", path, v, want)
	}
	return nil
}

func (g *gl3State) strIs(path, want string) error {
	v, err := g.field(path)
	if err != nil {
		return err
	}
	if s, _ := v.(string); s != g.nameID(want) {
		return fmt.Errorf("%s = %v, want %q", path, v, g.nameID(want))
	}
	return nil
}

func (g *gl3State) isNull(path string) error {
	if err := g.requireRecord(); err != nil {
		return err
	}
	v, ok := sr3Path(g.rec, path)
	if !ok {
		return fmt.Errorf("the record has no %s key (want it present and null)", path)
	}
	if v != nil {
		return fmt.Errorf("%s = %v, want null", path, v)
	}
	return nil
}

func (g *gl3State) boolIs(path string, want bool) error {
	v, err := g.field(path)
	if err != nil {
		return err
	}
	if b, ok := v.(bool); !ok || b != want {
		return fmt.Errorf("%s = %v, want %v", path, v, want)
	}
	return nil
}

func (g *gl3State) absent(key string) error {
	if err := g.requireRecord(); err != nil {
		return err
	}
	if _, ok := g.rec[key]; ok {
		return fmt.Errorf("the record carries %q: %s", key, g.genBody)
	}
	return nil
}

func (g *gl3State) bodyLacks(s string) error {
	if err := g.requireRecord(); err != nil {
		return err
	}
	if s == "" {
		return fmt.Errorf("nothing to look for")
	}
	if bytes.Contains(g.genBody, []byte(s)) {
		return fmt.Errorf("the record contains %q: %s", s, g.genBody)
	}
	return nil
}

// --- Then: the record ------------------------------------------------------------------------

func (g *gl3State) recIDIs() error { return g.strIs("id", g.reqID) }

func (g *gl3State) servedTriple(node, model string) error {
	if err := g.strIs("served.node", node); err != nil {
		return err
	}
	if err := g.strIs("served.model", model); err != nil {
		return err
	}
	if v, _ := sr3Path(g.rec, "served.relay"); v != nil {
		return fmt.Errorf("served.relay = %v on a direct relay", v)
	}
	return nil
}

func (g *gl3State) costAndBilled(cost string) error {
	if err := g.numIs("cost", cost); err != nil {
		return err
	}
	if err := g.numIs("tokens_in", g.relayHdr.Get("X-RogerAI-Tokens-In")); err != nil {
		return err
	}
	return g.numIs("tokens_out", g.relayHdr.Get("X-RogerAI-Tokens-Out"))
}

func (g *gl3State) oneAttemptSpec(spec string) error {
	at, err := g.attempts()
	if err != nil {
		return err
	}
	if len(at) != 1 {
		return fmt.Errorf("%d attempts, want exactly one: %v", len(at), at)
	}
	spec = strings.ReplaceAll(spec, ", no error_code", "")
	if err := g.matchKV(at[0], spec); err != nil {
		return err
	}
	if v, ok := at[0]["error_code"]; ok && v != nil && v != "" {
		return fmt.Errorf("attempt 1 carries error_code %v", v)
	}
	return nil
}

func (g *gl3State) streamedCancelled(st, ca string) error {
	if err := g.boolIs("streamed", st == "true"); err != nil {
		return err
	}
	return g.boolIs("cancelled", ca == "true")
}

func (g *gl3State) recReceipt() (protocol.UsageReceipt, error) {
	v, err := g.field("receipt")
	if err != nil {
		return protocol.UsageReceipt{}, err
	}
	enc, _ := v.(string)
	if enc == "" {
		return protocol.UsageReceipt{}, fmt.Errorf("receipt is %v, want an encoded receipt", v)
	}
	return protocol.DecodeReceipt(enc)
}

func (g *gl3State) receiptVerifies() error {
	rec, err := g.recReceipt()
	if err != nil {
		return err
	}
	if !rec.VerifyBroker(g.brokerPub()) {
		return fmt.Errorf("the record's receipt does not verify under the broker key")
	}
	return nil
}

func (g *gl3State) ttftIsFirstFrame() error {
	v, err := g.field("ttft_ms")
	if err != nil {
		return err
	}
	f, _ := v.(float64)
	ts := g.contentWrites()
	if len(ts) == 0 {
		return fmt.Errorf("no content frame reached the consumer")
	}
	upper := float64(ts[0].Sub(g.t0).Milliseconds()) + 50
	if f <= 0 || f > upper {
		return fmt.Errorf("ttft_ms %v, the first content frame reached the consumer %v ms in", f, upper-50)
	}
	return nil
}

func (g *gl3State) latencyToSettle() error {
	v, err := g.field("latency_ms")
	if err != nil {
		return err
	}
	f, _ := v.(float64)
	tt, _ := sr3Path(g.rec, "ttft_ms")
	t, _ := tt.(float64)
	upper := float64(g.t1.Sub(g.t0).Milliseconds()) + 50
	if f <= 0 || f > upper || f < t {
		return fmt.Errorf("latency_ms %v (ttft %v), the relay took %v ms", f, t, upper-50)
	}
	return nil
}

func (g *gl3State) tpsEqualsChunk() error {
	v, err := g.field("tps")
	if err != nil {
		return err
	}
	f, _ := v.(float64)
	ct, err := g.usageNum("rogerai.tps")
	if err != nil {
		return err
	}
	if math.Abs(f-ct) > 1e-6*math.Max(1, ct) {
		return fmt.Errorf("record tps %v, chunk tps %v", f, ct)
	}
	return nil
}

func (g *gl3State) attemptIs(i int, spec string) error {
	at, err := g.attempts()
	if err != nil {
		return err
	}
	if i >= len(at) {
		return fmt.Errorf("%d attempts, no attempts[%d]", len(at), i)
	}
	return g.matchKV(at[i], spec)
}

func (g *gl3State) receiptIsSecond() error {
	rec, err := g.recReceipt()
	if err != nil {
		return err
	}
	if rec.RequestID != g.reqID+"-2" {
		return fmt.Errorf("the record's receipt is %s's, want the second attempt %s-2", rec.RequestID, g.reqID)
	}
	return nil
}

func (g *gl3State) costIsSecondSettled() error {
	d, _, err := g.debit()
	if err != nil {
		return err
	}
	return g.numIs("cost", strconv.FormatFloat(d, 'g', -1, 64))
}

func (g *gl3State) durationAbout(i, ms int) error {
	at, err := g.attempts()
	if err != nil {
		return err
	}
	if i >= len(at) {
		return fmt.Errorf("no attempts[%d]", i)
	}
	f, _ := at[i]["duration_ms"].(float64)
	if math.Abs(f-float64(ms)) > 0.35*float64(ms) {
		return fmt.Errorf("attempts[%d].duration_ms %v, want about %d", i, f, ms)
	}
	return nil
}

func (g *gl3State) latencyAbout(ms int) error {
	v, err := g.field("latency_ms")
	if err != nil {
		return err
	}
	f, _ := v.(float64)
	if math.Abs(f-float64(ms)) > 0.35*float64(ms) {
		return fmt.Errorf("latency_ms %v, want about %d", f, ms)
	}
	return nil
}

func (g *gl3State) modelsIs(list string) error {
	var want []string
	if err := json.Unmarshal([]byte(list), &want); err != nil {
		return err
	}
	v, err := g.field("models")
	if err != nil {
		return err
	}
	arr, _ := v.([]any)
	if len(arr) != len(want) {
		return fmt.Errorf("models %v, want %v", v, want)
	}
	for i := range want {
		if s, _ := arr[i].(string); s != want[i] {
			return fmt.Errorf("models %v, want %v", v, want)
		}
	}
	return nil
}

func (g *gl3State) exactlyNAttempts(n int) error {
	at, err := g.attempts()
	if err != nil {
		return err
	}
	if len(at) != n {
		return fmt.Errorf("%d attempts, want %d: %v", len(at), n, at)
	}
	return nil
}

func (g *gl3State) statusAndCode(st int, code string) error {
	if err := g.numIs("status", strconv.Itoa(st)); err != nil {
		return err
	}
	return g.strIs("error_code", code)
}

func (g *gl3State) servedAndReceiptNull() error {
	if err := g.isNull("served"); err != nil {
		return err
	}
	return g.isNull("receipt")
}

func (g *gl3State) moderationIs(mode, verdict string) error {
	v, err := g.field("moderation")
	if err != nil {
		return err
	}
	m, _ := v.(map[string]any)
	if m["mode"] != mode || m["verdict"] != verdict {
		return fmt.Errorf("moderation %v, want mode %q verdict %q", m, mode, verdict)
	}
	if l, ok := m["latency_ms"].(float64); !ok || l < 0 {
		return fmt.Errorf("moderation.latency_ms is %v", m["latency_ms"])
	}
	return nil
}

func (g *gl3State) attemptsEmptyNullZero() error {
	if err := g.exactlyNAttempts(0); err != nil {
		return err
	}
	if err := g.isNull("served"); err != nil {
		return err
	}
	return g.numIs("cost", "0")
}

func (g *gl3State) noCategory() error {
	if err := g.requireRecord(); err != nil {
		return err
	}
	low := strings.ToLower(string(g.genBody))
	for _, k := range []string{"categor", `"s1"`, "violence", "csam"} {
		if strings.Contains(low, k) {
			return fmt.Errorf("the record names a moderation category (%s): %s", k, g.genBody)
		}
	}
	return nil
}

func (g *gl3State) servedCostUnchanged() error {
	if err := g.strIs("served.node", "n-1"); err != nil {
		return err
	}
	d, _, err := g.debit()
	if err != nil {
		return err
	}
	return g.numIs("cost", strconv.FormatFloat(d, 'g', -1, 64))
}

func (g *gl3State) attempt0NamesRelay() error {
	at, err := g.attempts()
	if err != nil {
		return err
	}
	if len(at) == 0 {
		return fmt.Errorf("no attempts")
	}
	if s, _ := at[0]["node"].(string); s != g.relayHdr.Get("X-RogerAI-Provider") {
		return fmt.Errorf("attempts[0].node %v, X-RogerAI-Provider %q", at[0]["node"], g.relayHdr.Get("X-RogerAI-Provider"))
	}
	return nil
}

func (g *gl3State) noGrantNoWallet() error {
	for _, sec := range g.grants {
		if err := g.bodyLacks(sec); err != nil {
			return err
		}
	}
	for _, w := range g.who {
		if w.wallet != "" {
			if err := g.bodyLacks(w.wallet); err != nil {
				return err
			}
		}
	}
	return g.absent("wallet")
}

func (g *gl3State) recNodeSig(name string) error {
	rec, err := g.recReceipt()
	if err != nil {
		return err
	}
	if !rec.VerifyNode(g.st(name).pubHex) {
		return fmt.Errorf("the record's receipt node signature does not verify against %s", name)
	}
	return nil
}

func (g *gl3State) recBrokerCoverage() error {
	rec, err := g.recReceipt()
	if err != nil {
		return err
	}
	ok, covers := rec.VerifyBrokerCoverage(g.brokerPub())
	if !ok || !covers {
		return fmt.Errorf("broker coverage ok=%v covers=%v", ok, covers)
	}
	return nil
}

func (g *gl3State) costEqualsDebit() error {
	d, _, err := g.debit()
	if err != nil {
		return err
	}
	return g.numIs("cost", strconv.FormatFloat(d, 'g', -1, 64))
}

func (g *gl3State) costEqualsHeader() error {
	h := g.relayHdr.Get("X-RogerAI-Cost")
	if h == "" {
		return fmt.Errorf("the relay carried no X-RogerAI-Cost")
	}
	return g.numIs("cost", h)
}

func (g *gl3State) noBalanceNoWallet() error {
	if err := g.absent("balance"); err != nil {
		return err
	}
	if err := g.absent("wallet"); err != nil {
		return err
	}
	if w := g.who["alice"]; w != nil {
		return g.bodyLacks(w.wallet)
	}
	return nil
}

func (g *gl3State) attemptsAndCostPresent() error {
	if _, err := g.attempts(); err != nil {
		return err
	}
	_, err := g.field("cost")
	return err
}

func (g *gl3State) noReceipt() error {
	if err := g.absent("receipt"); err != nil {
		return err
	}
	if rec, _, err := g.storedReceipt(g.reqID); err == nil {
		if bytes.Contains(g.genBody, []byte(rec.User)) && rec.User != "" {
			return fmt.Errorf("the owner view carries the receipt's pseudonym")
		}
	}
	return nil
}

func (g *gl3State) noKeyState() error {
	if err := g.absent("key_limit"); err != nil {
		return err
	}
	return g.absent("key_spend_after")
}

func (g *gl3State) ownerOneAttempt(_ string, spec string) error { return g.oneAttemptSpec(spec) }

func (g *gl3State) servedAbsent() error { return g.absent("served") }

func (g *gl3State) bodyLacksName(name string) error { return g.bodyLacks(g.nameID(name)) }

func (g *gl3State) noReceiptBalanceWallet() error {
	if err := g.noReceipt(); err != nil {
		return err
	}
	return g.noBalanceNoWallet()
}

func (g *gl3State) bothInOrder() error {
	at, err := g.attempts()
	if err != nil {
		return err
	}
	if len(at) != 2 {
		return fmt.Errorf("%d attempts, want both", len(at))
	}
	if err := g.matchKV(at[0], `n 1, node "n-1", status 429`); err != nil {
		return err
	}
	return g.matchKV(at[1], `n 2, node "n-2", status 200`)
}

func (g *gl3State) receiptPresentVerifies() error { return g.receiptVerifies() }

// unknownSame compares the last 404 with an unknown-id lookup by the same identity, and makes
// sure the request's own reader is served (a broker with no /generation 404s everyone alike).
func (g *gl3State) unknownSame() error {
	if g.genCode != 404 {
		return fmt.Errorf("GET /generation = %d, want 404 first", g.genCode)
	}
	code, body, who := g.genCode, append([]byte(nil), g.genBody...), g.lastWho
	g.get("0123456789abcdef", who)
	if g.genCode != code || !bytes.Equal(g.genBody, body) {
		return fmt.Errorf("foreign id %d %q, unknown id %d %q: not uniform", code, body, g.genCode, g.genBody)
	}
	g.get(g.reqID, "alice")
	if g.genCode != 200 {
		return fmt.Errorf("the request's own consumer reads %d - the 404 proves nothing until the record is readable", g.genCode)
	}
	return nil
}

func (g *gl3State) foreignAndUnknownSame() error {
	code, body, hdr, cmds := g.genCode, append([]byte(nil), g.genBody...), g.genHdr.Clone(), g.cmdsDelta
	// the foreign-id case: a request eve made, read by alice
	g.boundAccount("eve", 10)
	if err := g.relayAs("eve", false); err != nil {
		return err
	}
	g.get(g.reqIDs["eve"], "alice")
	if g.genCode != code || !bytes.Equal(g.genBody, body) {
		return fmt.Errorf("unknown id %d %q, foreign id %d %q", code, body, g.genCode, g.genBody)
	}
	for _, k := range []string{"Content-Type", "Cache-Control", "Vary"} {
		if hdr.Get(k) != g.genHdr.Get(k) {
			return fmt.Errorf("header %s differs: %q vs %q", k, hdr.Get(k), g.genHdr.Get(k))
		}
	}
	g.foreignCmds = g.cmdsDelta
	g.unknownCmds = cmds
	return nil
}

func (g *gl3State) constantWork() error {
	if g.unknownCmds == 0 || g.unknownCmds != g.foreignCmds {
		return fmt.Errorf("shared-store commands: unknown id %d, foreign id %d - want equal and non-zero", g.unknownCmds, g.foreignCmds)
	}
	return nil
}

func (g *gl3State) errorCodeIs(code string) error {
	var m map[string]any
	if err := json.Unmarshal(g.genBody, &m); err != nil {
		return fmt.Errorf("the body is not JSON: %q", g.genBody)
	}
	e, _ := m["error"].(map[string]any)
	if e == nil || e["code"] != code {
		return fmt.Errorf("error %v, want code %q", m["error"], code)
	}
	return nil
}

func (g *gl3State) malformedGet(id string) error {
	// run the lookup with a store that panics on any call, and watch the shared store
	real := g.b.db
	g.b.db = gl3PanicStore{}
	defer func() { g.b.db = real }()
	g.panicked = false
	g.lastID = id
	g.do(g.b, http.MethodGet, "/generation?id="+id, "alice")
	return nil
}

func (g *gl3State) noStoreRead() error {
	if g.panicked {
		return fmt.Errorf("the lookup read the store before rejecting the id")
	}
	if g.cmdsDelta != 0 {
		return fmt.Errorf("the lookup ran %d shared-store commands before rejecting the id", g.cmdsDelta)
	}
	return nil
}

func (g *gl3State) neitherMarker(a, b string) error {
	if err := g.bodyLacks(a); err != nil {
		return err
	}
	return g.bodyLacks(b)
}

func (g *gl3State) noBandCode() error {
	code := g.freq
	if i := strings.LastIndex(code, " "); i >= 0 {
		code = code[i+1:]
	}
	return g.bodyLacks(code)
}

func (g *gl3State) noBandSaying() error {
	if err := g.requireRecord(); err != nil {
		return err
	}
	low := strings.ToLower(string(g.genBody))
	for _, k := range []string{"band", "freq", "private"} {
		if strings.Contains(low, k) {
			return fmt.Errorf("the record mentions %q: %s", k, g.genBody)
		}
	}
	return nil
}

func (g *gl3State) noIP() error { return g.bodyLacks(g.knownIP) }

func (g *gl3State) noAliceIdentity() error {
	a := g.who["alice"]
	if err := g.bodyLacks(a.wallet); err != nil {
		return err
	}
	return g.bodyLacks(hex.EncodeToString(a.priv.Public().(ed25519.PublicKey)))
}

func (g *gl3State) noPseudonym() error {
	rec, _, err := g.storedReceipt(g.reqID)
	if err != nil {
		return err
	}
	if rec.User == "" {
		return fmt.Errorf("the stored receipt has no pseudonym to look for")
	}
	return g.bodyLacks(rec.User)
}

func (g *gl3State) noReceiptPseudonym() error { return g.noReceipt() }

func (g *gl3State) nothingFromEve() error {
	if err := g.bodyLacks(g.reqIDs["eve"]); err != nil {
		return err
	}
	return g.bodyLacks(g.who["eve"].wallet)
}

func (g *gl3State) heldNotFound() error {
	if g.genCode != 404 {
		return fmt.Errorf("GET /generation = %d, want 404", g.genCode)
	}
	g.get(g.reqIDs["held"], "alice")
	if g.genCode != 200 {
		return fmt.Errorf("alice's live request reads %d - the 404 proves nothing", g.genCode)
	}
	return nil
}

func (g *gl3State) anonymizedGets() error {
	g.get(g.reqID, "alice")
	return nil
}

func (g *gl3State) anonymizedRowExists() error {
	if _, _, err := g.storedReceipt(g.reqID); err != nil {
		return fmt.Errorf("the anonymized ledger row is gone: %v", err)
	}
	return nil
}

func (g *gl3State) onceMore() error {
	g.get(g.reqID, "alice")
	return nil
}

func (g *gl3State) retryAfterSet() error {
	if g.genHdr.Get("Retry-After") == "" {
		return fmt.Errorf("the 429 carries no Retry-After")
	}
	return nil
}

func (g *gl3State) eveSameID() error {
	g.aliceBody = append([]byte(nil), g.genBody...)
	g.boundAccount("eve", 10)
	g.get(g.reqID, "eve")
	return nil
}

func (g *gl3State) eveNotCached() error {
	if len(g.aliceBody) == 0 || bytes.Equal(g.genBody, g.aliceBody) || bytes.Contains(g.genBody, []byte(g.reqID)) {
		return fmt.Errorf("eve received alice's record: %s", g.genBody)
	}
	return nil
}

func (g *gl3State) equalsInstanceA() error {
	bBody := append([]byte(nil), g.genBody...)
	g.get(g.reqID, "alice")
	if g.genCode != 200 || !bytes.Equal(g.genBody, bBody) {
		return fmt.Errorf("instance A %d %q, instance B %q", g.genCode, g.genBody, bBody)
	}
	return nil
}

func (g *gl3State) attemptsN1N2() error { return g.bothInOrder() }

func (g *gl3State) noPartial() error {
	var m map[string]any
	_ = json.Unmarshal(g.genBody, &m)
	for _, k := range []string{"attempts", "served", "cost", "receipt"} {
		if _, ok := m[k]; ok {
			return fmt.Errorf("a partial record was returned: %s", g.genBody)
		}
	}
	return nil
}

func (g *gl3State) debitRegardless(amount string) error {
	if err := g.ledgerDebitIs(amount); err != nil {
		return err
	}
	g.get(g.reqID, "alice")
	return g.costEqualsDebit()
}

func (g *gl3State) fullMeters() error {
	for _, h := range []string{"X-RogerAI-Receipt", "X-RogerAI-Cost", "X-RogerAI-Tokens-In", "X-RogerAI-Tokens-Out", "X-RogerAI-Balance", "X-RogerAI-Price", "X-RogerAI-TPS", "X-RogerAI-Quality"} {
		if g.relayHdr.Get(h) == "" {
			return fmt.Errorf("the relay carried no %s", h)
		}
	}
	if g.relayCode != 200 {
		return fmt.Errorf("the relay = %d", g.relayCode)
	}
	return nil
}

func (g *gl3State) recordWriteLogged() error {
	for _, l := range strings.Split(g.logs.String(), "\n") {
		low := strings.ToLower(l)
		if strings.Contains(low, "generation") && (strings.Contains(low, "fail") || strings.Contains(low, "error")) {
			return nil
		}
	}
	return fmt.Errorf("no log line names a generation record write failure")
}

func (g *gl3State) lookupsCounted(n int) error {
	if err := g.readAdminLive(); err != nil {
		return err
	}
	v, ok := g.adminResp["generation_lookups"].(float64)
	if !ok || int(v) != n {
		return fmt.Errorf("/admin/live generation_lookups = %v, want %d", g.adminResp["generation_lookups"], n)
	}
	return nil
}

func (g *gl3State) statsUnchanged() error {
	if err := g.requireRecord(); err != nil {
		return err
	}
	if got := g.successOf("n-1"); got != g.served {
		return fmt.Errorf("n-1's served count moved %d -> %d on a lookup", g.served, got)
	}
	if e, _ := g.db.EarningsOf(g.st("n-1").id); !approx(e, g.earnBefore) {
		return fmt.Errorf("n-1's earnings moved %v -> %v on a lookup", g.earnBefore, e)
	}
	return nil
}

// gl3PanicStore is a store whose every method panics (nil embedded interface): a lookup that
// touches it is caught by the runner's recover.
type gl3PanicStore struct{ store.Store }

func (g *gl3State) register(sc *godog.ScenarioContext) {
	// Background
	sc.Step(lit("a broker with an empty in-memory node registry"), g.emptyRegistry)
	sc.Step(lit("the fee rate is 30%"), g.feeRate30)
	sc.Step(`^nodes "([^"]*)" and "([^"]*)" are on air for "([^"]*)" at out-price \$([0-9.]+)/1M$`, g.twoOnAir)
	sc.Step(`^a logged-in consumer "([^"]*)" with a funded wallet$`, g.loggedInAlice)

	// Given / When: requests
	sc.Step(`^"alice" (?:makes|made) a non-streaming request for "([^"]*)" served by "([^"]*)" at \$([0-9.]+)$`, g.aliceNonStreamAt)
	sc.Step(`^"alice" (?:makes|made) a request served by "([^"]*)" at \$([0-9.]+)$`, g.aliceServedAt)
	sc.Step(`^"alice" (?:makes|made) a streaming request for "([^"]*)" served by "([^"]*)"$`, g.aliceStream)
	sc.Step(`^"([^"]*)" answers with an upstream 429 and Retry-After (\d+)$`, g.answers429RA)
	sc.Step(`^"alice" makes a request for "([^"]*)" that fails over to "([^"]*)" and is served$`, g.failsOverTo)
	sc.Step(`^"([^"]*)" takes (\d+) ms to answer 502 and "([^"]*)" takes (\d+) ms to serve$`, g.slowFailover)
	sc.Step(`^"alice" makes a request that fails over from "([^"]*)" to "([^"]*)"$`, g.failsOverFromTo)
	sc.Step(`^no station is on air for "([^"]*)"$`, g.noStationFor)
	sc.Step(`^"alice" makes a request with model "([^"]*)" and models (\[.*\]) served by "([^"]*)"$`, g.aliceFallback)
	sc.Step(`^"alice" makes a request for "([^"]*)" from her wallet served by "([^"]*)"$`, g.aliceWalletServed)
	sc.Step(`^"alice" makes a request for "([^"]*)" with X-Roger-Min-TPS "([^"]*)"$`, g.aliceMinTPS)
	sc.Step(`^"([^"]*)" and "([^"]*)" are both cooling for (\d+) more seconds$`, g.bothCooling)
	sc.Step(lit("moderation mode is sync"), g.moderationSync)
	sc.Step(lit(`"alice" makes a request the screener rejects with 451`), g.aliceRejected451)
	sc.Step(lit("moderation mode is async"), g.moderationAsync)
	sc.Step(lit(`"alice" makes a request that is served and later flagged by the off-path screener`), g.servedThenFlagged)
	sc.Step(`^"alice" makes a request with provider\.sort "([^"]*)" and roger\.pref "([^"]*)"$`, g.aliceSortAndPref)
	sc.Step(lit(`"alice"'s wallet cannot cover the cheapest hold`), g.walletCannotCover)
	sc.Step(`^(an unsigned request with no allowlisted Origin|a request with a bad signature|a request bearing an unknown rog-key_ secret|an anonymous request over the per-IP rate limit) is refused with status (\d+)$`, g.gl3RefusedEarly)
	sc.Step(`^the response still carries X-RogerAI-Request-Id$`, g.gl3HasRequestID)
	sc.Step(`^no /generation record exists for that request id$`, g.gl3NoRecord)
	sc.Step(`^a /generation record exists for that request id$`, g.gl3HasRecord)
	sc.Step(`^"([^"]+)" sends a request with an unknown routing key$`, g.gl3UnknownRoutingKey)
	sc.Step(`^the relay answered (\d+)$`, func(code int) error {
		if g.lastCode != code {
			return fmt.Errorf("the relay answered %d, want %d (%s)", g.lastCode, code, g.gl3LastBody)
		}
		return nil
	})
	sc.Step(`^"alice" disconnects mid-stream and the settle still bills \$([0-9.]+)$`, g.disconnectBills)
	sc.Step(`^"([^"]*)" returns 200 with empty output and "([^"]*)" serves$`, g.emptyThenServes)
	sc.Step(`^"alice" makes a request for "([^"]*)"$`, g.aliceRequestServedOrAny)
	sc.Step(`^no direct node is on air for "([^"]*)"$`, g.noDirectFor)
	sc.Step(`^an approved Tower "([^"]*)" serves "([^"]*)"$`, g.towerServes)
	sc.Step(`^"alice" makes a request for "([^"]*)" served through the bridge$`, g.aliceBridged)
	sc.Step(`^owner "([^"]*)" minted a free grant for "([^"]*)" on his node "([^"]*)"$`, g.bobFreeGrant)
	sc.Step(`^a bearer of that grant makes a request served by "([^"]*)"$`, func(n string) error { return g.bearerServed("that grant", n) })
	sc.Step(`^"alice" (?:makes|made) a (?:non-streaming )?request served by "([^"]*)"(?: with a signed wallet)?$`, g.aliceMadeServedBy)
	sc.Step(`^a Playbox session made a request served by "([^"]*)"$`, g.playboxServed)
	sc.Step(`^a bearer of grant ([A-Z0-9]+) made a request served by "([^"]*)"$`, g.bearerOfGrantServed)
	sc.Step(`^grants ([A-Z0-9]+) and ([A-Z0-9]+) minted by "([^"]*)"$`, g.twoGrants)
	sc.Step(`^a bearer of ([A-Z0-9]+) made a request served by "([^"]*)"$`, g.bearerOfGrantServed)
	sc.Step(`^owner "bob" minted grant ([A-Z0-9]+) and a bearer of ([A-Z0-9]+) made a request served by "bob"'s node$`, func(a, _ string) error { return g.bobGrantServed(a) })
	sc.Step(`^"alice" made a request served by "([^"]*)", owned by "([^"]*)"$`, g.aliceServedOwnedBy)
	sc.Step(`^"([^"]*)" \(owned by "([^"]*)"\) answered 429 and "([^"]*)" \(owned by "([^"]*)"\) served "alice"$`, g.carolDave)
	sc.Step(lit(`"alice" made a request <id> that failed over once`), g.failedOverOnce)
	sc.Step(`^"alice" made a request whose prompt contains "([^"]*)" and whose completion contains "([^"]*)"$`, g.markers)
	sc.Step(lit(`"alice" made a request on a private band by its code`), g.privateBand)
	sc.Step(`^"([^"]*)" registered from a known IP$`, g.registeredFromIP)
	sc.Step(`^"alice" made a request served by "carol"'s node$`, func() error { return g.aliceServedOwnedBy("n-1", "carol") })
	sc.Step(`^"alice" and "eve" each made a request served by "([^"]*)"$`, g.aliceEveEach)
	sc.Step(lit("a request id whose lineage the console feed no longer holds"), g.noLongerHeld)
	sc.Step(lit(`"alice" deletes her account`), g.aliceDeletes)
	sc.Step(lit(`"alice" exhausts the per-identity limiter on /generation`), g.exhaustLimiter)
	sc.Step(lit(`"alice" GETs /generation for her request and it is cached`), g.aliceCached)
	sc.Step(lit(`"alice"'s streaming request is still in flight`), g.inFlight)
	sc.Step(lit("the stream ends"), g.streamEnds)
	sc.Step(lit("a two-instance broker sharing a store"), g.twoInstanceShared)
	sc.Step(lit(`"alice"'s request was served on instance A`), g.servedOnA)
	sc.Step(lit("a two-instance broker"), g.twoInstanceShared)
	sc.Step(lit("attempt 1 ran on instance A and attempt 2 on instance B"), g.crossInstanceAttempts)
	sc.Step(lit("the shared store is unreachable"), g.sharedDown)
	sc.Step(lit("the record store refuses writes"), g.recordStoreRefuses)

	// When: lookups
	sc.Step(`^"(alice)" GETs /generation\?id=(.*)$`, g.aliceGets)
	sc.Step(`^"(alice)" GETs /generation for (?:it|her request)(?: with the same signed identity| immediately after the response)?$`, g.getsForIt)
	sc.Step(lit("the same browser session GETs /generation for it"), func() error { g.get(g.reqID, "browser"); return nil })
	sc.Step(`^the same bearer GETs /generation for it with Authorization Bearer ([A-Z0-9]+)$`, func(l string) error { g.get(g.reqID, "grant:"+l); return nil })
	sc.Step(`^the bearer GETs /generation for it$`, func() error { g.get(g.reqID, "grant:that grant"); return nil })
	sc.Step(`^a bearer of ([A-Z0-9]+) GETs /generation for it$`, func(l string) error { g.get(g.reqID, "grant:"+l); return nil })
	sc.Step(`^"([^"]*)" GETs /generation for it as the payout owner$`, g.ownerGets)
	sc.Step(lit(`logged-in consumer "eve" GETs /generation for it`), g.eveGets)
	sc.Step(lit(`owner "frank", who owns no station involved, GETs /generation for it`), g.frankGets)
	sc.Step(lit("an unauthenticated caller GETs /generation for it"), g.anonGets)
	sc.Step(lit(`"alice" GETs /generation with no id`), g.noIDGet)
	sc.Step(lit(`"alice" POSTs /generation?id=<a valid id>`), g.postGet)
	sc.Step(lit("the anonymized identity GETs /generation for it"), g.anonymizedGets)
	sc.Step(lit(`"alice" GETs /generation once more`), g.onceMore)
	sc.Step(lit(`"eve" GETs /generation for the same id`), g.eveSameID)
	sc.Step(lit(`"alice" GETs /generation for it on instance B`), g.getsOnB)
	sc.Step(lit(`"alice" GETs /generation for a real request id`), func() error { g.get(g.reqID, "alice"); return nil })
	sc.Step(`^"alice" GETs /generation for a request "([^"]*)" served$`, g.getsForIPRequest)
	sc.Step(lit(`"alice" GETs /generation for a request three times`), g.getsThreeTimes)
	sc.Step(lit(`"alice" GETs /generation for a request`), g.getsForARequest)

	// Then
	sc.Step(`^the status is (\d+)$`, g.statusIs)
	sc.Step(lit("the record's id is the request id"), g.recIDIs)
	sc.Step(`^served\.node is "([^"]*)", served\.model is "([^"]*)", served has no relay$`, g.servedTriple)
	sc.Step(`^cost is ([0-9.]+), tokens_in and tokens_out are the billed counts$`, g.costAndBilled)
	sc.Step(`^attempts has exactly one entry with (.+)$`, g.oneAttemptSpec)
	sc.Step(`^streamed is (true|false) and cancelled is (true|false)$`, g.streamedCancelled)
	sc.Step(lit("receipt decodes and verifies"), g.receiptVerifies)
	sc.Step(`^streamed is (true|false)$`, func(v string) error { return g.boolIs("streamed", v == "true") })
	sc.Step(lit("ttft_ms is the time to the first content frame"), g.ttftIsFirstFrame)
	sc.Step(lit("latency_ms is the time to the settle"), g.latencyToSettle)
	sc.Step(lit("tps equals the usage chunk's usage.rogerai.tps"), g.tpsEqualsChunk)
	sc.Step(`^attempts\[(\d+)\] is \{ (.+) \}$`, g.attemptIs)
	sc.Step(`^served\.(node|model|relay) is "([^"]*)"$`, func(k, v string) error { return g.strIs("served."+k, v) })
	sc.Step(lit("receipt is the second attempt's receipt"), g.receiptIsSecond)
	sc.Step(lit("cost is the second attempt's settled cost"), g.costIsSecondSettled)
	sc.Step(`^attempts\[(\d+)\]\.duration_ms is about (\d+)$`, g.durationAbout)
	sc.Step(`^latency_ms is about (\d+)$`, g.latencyAbout)
	sc.Step(`^model_requested is "([^"]*)"$`, func(v string) error { return g.strIs("model_requested", v) })
	sc.Step(`^models is (\[.*\])$`, g.modelsIs)
	sc.Step(lit("attempts has exactly one entry"), func() error { return g.exactlyNAttempts(1) })
	sc.Step(lit("key_id is null"), func() error { return g.isNull("key_id") })
	sc.Step(`^status is (\d+) and error_code is "([^"]*)"$`, g.statusAndCode)
	sc.Step(lit("served is null and receipt is null"), g.servedAndReceiptNull)
	sc.Step(`^cost is ([0-9.]+)$`, func(v string) error { return g.numIs("cost", v) })
	sc.Step(lit("attempts is []"), func() error { return g.exactlyNAttempts(0) })
	sc.Step(`^status is (\d+), error_code is "([^"]*)"$`, g.statusAndCode)
	sc.Step(`^retry_after_s is (\d+)$`, func(v string) error { return g.numIs("retry_after_s", v) })
	sc.Step(`^status is (\d+)$`, func(v string) error { return g.numIs("status", v) })
	sc.Step(lit(`moderation is { mode "sync", verdict "rejected", latency_ms <the screen time> }`), func() error { return g.moderationIs("sync", "rejected") })
	sc.Step(lit("attempts is [] and served is null and cost is 0"), g.attemptsEmptyNullZero)
	sc.Step(lit(`the record contains no category beyond "rejected"`), g.noCategory)
	sc.Step(`^moderation\.(mode|verdict) is "([^"]*)"$`, func(k, v string) error { return g.strIs("moderation."+k, v) })
	sc.Step(lit("served and cost are unchanged by the verdict"), g.servedCostUnchanged)
	sc.Step(`^status is (\d+) and attempts is \[\] and cost is 0$`, func(v string) error {
		if err := g.numIs("status", v); err != nil {
			return err
		}
		if err := g.exactlyNAttempts(0); err != nil {
			return err
		}
		return g.numIs("cost", "0")
	})
	sc.Step(lit("cancelled is true"), func() error { return g.boolIs("cancelled", true) })
	sc.Step(lit("receipt is present"), func() error { _, err := g.recReceipt(); return err })
	sc.Step(`^attempts\[(\d+)\]\.void_reason is "([^"]*)"$`, func(i int, v string) error { return g.attemptIs(i, `void_reason "`+v+`"`) })
	sc.Step(`^attempts\[(\d+)\]\.status is (\d+)$`, func(i int, v string) error { return g.attemptIs(i, "status "+v) })
	sc.Step(`^attempts\[(\d+)\]\.status is (\d+) and has no void_reason$`, func(i int, v string) error {
		if err := g.attemptIs(i, "status "+v); err != nil {
			return err
		}
		at, _ := g.attempts()
		if r, ok := at[i]["void_reason"]; ok && r != nil && r != "" {
			return fmt.Errorf("attempts[%d] carries void_reason %v", i, r)
		}
		return nil
	})
	sc.Step(lit("attempts[0].node names the relay as X-RogerAI-Provider does on the bridge path"), g.attempt0NamesRelay)
	sc.Step(lit("the record contains no grant token and no wallet"), g.noGrantNoWallet)
	sc.Step(`^the receipt's node signature verifies against "([^"]*)"'s key$`, g.recNodeSig)
	sc.Step(lit("the broker signature verifies with coverage of the billed counts"), g.recBrokerCoverage)
	sc.Step(lit("cost equals the ledger's debit for the request id"), g.costEqualsDebit)
	sc.Step(lit("cost equals the X-RogerAI-Cost header the response carried"), g.costEqualsHeader)
	sc.Step(lit("the record has no balance and no wallet"), g.noBalanceNoWallet)
	sc.Step(lit("attempts and cost are present"), g.attemptsAndCostPresent)
	sc.Step(lit("the record has no receipt"), g.noReceipt)
	sc.Step(lit("the record has no key_limit and no key_spend_after"), g.noKeyState)
	sc.Step(`^attempts has exactly one entry, ([a-z]+)'s: \{ (.+) \}$`, g.ownerOneAttempt)
	sc.Step(lit("served is absent from the body"), g.servedAbsent)
	sc.Step(`^the body does not contain "([^"]*)"$`, g.bodyLacksName)
	sc.Step(lit("the record has no receipt, no balance and no wallet"), g.noReceiptBalanceWallet)
	sc.Step(lit("attempts lists both attempts in order"), g.bothInOrder)
	sc.Step(lit("receipt is present and verifies"), g.receiptPresentVerifies)
	sc.Step(lit("the body equals the body for an unknown id"), g.unknownSame)
	sc.Step(lit("the response body and headers equal those of the foreign-id case"), g.foreignAndUnknownSame)
	sc.Step(lit("the lookup ran the same store read and the same scope comparison as the foreign-id case (constant-work structure, no early return on a miss)"), g.constantWork)
	sc.Step(`^error\.code is "([^"]*)"$`, g.errorCodeIs)
	sc.Step(lit("no store read happened"), g.noStoreRead)
	sc.Step(`^the response body contains neither marker$`, func() error { return g.neitherMarker("PROMPT-MARKER", "REPLY-MARKER") })
	sc.Step(lit("the response body does not contain the code"), g.noBandCode)
	sc.Step(lit("the record does not say the request was on a private band beyond served.node"), g.noBandSaying)
	sc.Step(lit("the response body does not contain that IP"), g.noIP)
	sc.Step(lit(`the body contains neither "alice"'s wallet nor her pubkey`), g.noAliceIdentity)
	sc.Step(lit("the body contains no pseudonym"), g.noPseudonym)
	sc.Step(lit("the body contains no receipt (the receipt's user field is the pseudonym)"), g.noReceiptPseudonym)
	sc.Step(lit(`the body contains nothing from "eve"'s request`), g.nothingFromEve)
	sc.Step(lit("the anonymized ledger row still exists for the legal window"), g.anonymizedRowExists)
	sc.Step(lit("Retry-After is set"), g.retryAfterSet)
	sc.Step(lit(`"eve" did not receive "alice"'s cached body`), g.eveNotCached)
	sc.Step(lit("the record equals the one instance A returns"), g.equalsInstanceA)
	sc.Step(lit("attempts lists n 1 then n 2 with their nodes and statuses"), g.attemptsN1N2)
	sc.Step(lit("no partial record is returned"), g.noPartial)
	sc.Step(`^the ledger debit is \$([0-9.]+) whether or not the record write succeeded$`, g.debitRegardless)
	sc.Step(lit("the response is 200 with the full meter headers"), g.fullMeters)
	sc.Step(lit("a log line names the record write failure"), g.recordWriteLogged)
	sc.Step(`^/admin/live shows generation_lookups (\d+)$`, g.lookupsCounted)
	sc.Step(lit(`"n-1"'s served count and earnings are unchanged`), g.statsUnchanged)
}

func TestGenerationLookupBDD(t *testing.T) {
	t.Run("generation_lookup", func(t *testing.T) {
		g := &gl3State{sr3State: &sr3State{rpState: &rpState{foState: &foState{t: t, logs: &utLog{}}}}}
		prev := log.Writer()
		log.SetOutput(g.logs)
		t.Cleanup(func() {
			log.SetOutput(prev)
			g.teardown()
		})
		suite := godog.TestSuite{
			Name: "generation_lookup",
			ScenarioInitializer: func(sc *godog.ScenarioContext) {
				sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { return ctx, g.reset() })
				sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
					g.teardown()
					return ctx, nil
				})
				g.register(sc)
			},
			Options: &godog.Options{
				Format: "pretty", Paths: []string{"../../features/routing/generation_lookup.feature"},
				Tags: "~@cli && ~@tui && ~@proxy && ~@harness && ~@docs && ~@later && ~@slice5 && ~@web", TestingT: t, Strict: true,
			},
		}
		if suite.Run() != 0 {
			t.Fatal("generation_lookup scenarios failed")
		}
	})
	t.Run("model_fallback_list", func(t *testing.T) {
		st := &mf1State{rpState: &rpState{foState: &foState{t: t, logs: &utLog{}}}}
		prev := log.Writer()
		log.SetOutput(st.logs)
		t.Cleanup(func() {
			log.SetOutput(prev)
			st.teardownMF1()
		})
		suite := godog.TestSuite{
			Name: "model_fallback_list@slice3",
			ScenarioInitializer: func(sc *godog.ScenarioContext) {
				sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { return ctx, st.resetMF1() })
				sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
					st.teardownMF1()
					return ctx, nil
				})
				st.register(sc)
			},
			Options: &godog.Options{
				Format: "pretty", Paths: []string{"../../features/routing/model_fallback_list.feature"},
				Tags: "@slice3", TestingT: t, Strict: true,
			},
		}
		if suite.Run() != 0 {
			t.Fatal("model_fallback_list @slice3 scenarios failed")
		}
	})
}

var _ = sync.Mutex{}

// ---- refusals before identity / rate limiting write no record (audit fix 2026-10-04) ----------

func (g *gl3State) gl3RawRelay(body []byte, prep func(r *http.Request)) (int, http.Header) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if prep != nil {
		prep(r)
	}
	w := httptest.NewRecorder()
	g.b.relay(w, r)
	g.gl3LastBody = w.Body.String()
	return w.Code, w.Header()
}

func (g *gl3State) gl3RefusedEarly(kind string, status int) error {
	body := []byte(`{"model":"qwen3-32b","messages":[{"role":"user","content":"hi"}]}`)
	var code int
	var hdr http.Header
	switch kind {
	case "an unsigned request with no allowlisted Origin":
		code, hdr = g.gl3RawRelay(body, nil)
	case "a request with a bad signature":
		code, hdr = g.gl3RawRelay(body, func(r *http.Request) {
			signReq(r, g.consumerPriv, []byte(`{"model":"other"}`)) // signed over different bytes
		})
	case "a request bearing an unknown rog-key_ secret":
		code, hdr = g.gl3RawRelay(body, func(r *http.Request) { r.Header.Set("Authorization", "Bearer rog-key_doesnotexist") })
	case "an anonymous request over the per-IP rate limit":
		saved := g.b.anonRL
		g.b.anonRL = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 1, burst: 1}
		defer func() { g.b.anonRL = saved }()
		for i := 0; i < 5; i++ {
			code, hdr = g.gl3RawRelay(body, func(r *http.Request) {
				r.Header.Set("Origin", "https://rogerai.fm")
				r.RemoteAddr = "198.51.100.77:4242"
			})
			if code == http.StatusTooManyRequests {
				break
			}
		}
	default:
		return fmt.Errorf("unknown early-refusal kind %q", kind)
	}
	if code != status {
		return fmt.Errorf("%s answered %d, want %d (%s)", kind, code, status, g.gl3LastBody)
	}
	g.reqID = hdr.Get("X-RogerAI-Request-Id")
	g.lastHdr, g.lastCode = hdr, code
	return nil
}

func (g *gl3State) gl3HasRequestID() error {
	if g.lastHdr.Get("X-RogerAI-Request-Id") == "" {
		return fmt.Errorf("the refused response carries no X-RogerAI-Request-Id")
	}
	return nil
}

func (g *gl3State) gl3NoRecord() error {
	if g.reqID == "" {
		return fmt.Errorf("no request id captured")
	}
	_, found, err := g.b.genGet(g.reqID)
	if err != nil {
		return err
	}
	if found {
		return fmt.Errorf("a /generation record was written for the early refusal %s", g.reqID)
	}
	return nil
}

func (g *gl3State) gl3HasRecord() error {
	_, found, err := g.b.genGet(g.reqID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no /generation record for %s", g.reqID)
	}
	return nil
}

func (g *gl3State) gl3UnknownRoutingKey(label string) error {
	if err := g.ensureFunded(); err != nil {
		return err
	}
	body := []byte(`{"model":"qwen3-32b","messages":[{"role":"user","content":"hi"}],"provider":{"foo":1}}`)
	code, hdr := g.gl3RawRelay(body, func(r *http.Request) { signReq(r, g.who[label].priv, body) })
	g.reqID, g.lastHdr, g.lastCode = hdr.Get("X-RogerAI-Request-Id"), hdr, code
	return nil
}
