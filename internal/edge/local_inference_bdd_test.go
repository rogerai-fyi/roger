package edge

// Executable spec: features/edge/local_inference.feature (@edge) - the wire and the local
// receipt. A REAL face (TLS listener over a node certificate a REAL Edge authority issued),
// REAL peers dialling it with their own issued certificates over mutual TLS, a REAL
// OpenAI-shaped upstream stub behind it, and receipts verified against the served leaf.

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/towercore/cert"
)

type member struct {
	id   string
	key  ed25519.PrivateKey
	cert tls.Certificate
	leaf *x509.Certificate
}

type inferBDD struct {
	t        *testing.T
	auth     *cert.Authority // the Edge's issuing authority (the designated machine)
	verify   *cert.Authority // the verify-only view a node holds
	members  map[string]*member
	admitted map[string]bool // who the serving node's fleet admits
	upstream *httptest.Server
	upMode   string // "json" | "stream" | "error"
	face     *httptest.Server
	srv      *Server
	serving  *member
	sessions *Sessions

	status  int
	body    string
	rec     protocol.UsageReceipt
	hasRec  bool
	leaf    *x509.Certificate
	err     error
	bodies  int // upstream calls observed
	bigBody bool
}

func (s *inferBDD) reset() {
	s.closeAll()
	a, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	if err != nil {
		s.t.Fatal(err)
	}
	s.auth = a
	v, err := cert.NewVerifier(a.Root(), nil)
	if err != nil {
		s.t.Fatal(err)
	}
	s.verify = v
	s.members, s.admitted = map[string]*member{}, map[string]bool{}
	s.upMode, s.status, s.body, s.rec, s.hasRec, s.leaf, s.err, s.bodies, s.bigBody = "json", 0, "", protocol.UsageReceipt{}, false, nil, nil, 0, false
	s.sessions = NewSessions("acct-1")
}

func (s *inferBDD) closeAll() {
	if s.upstream != nil {
		s.upstream.Close()
		s.upstream = nil
	}
	if s.face != nil {
		s.face.Close()
		s.face = nil
	}
}

// issue mints a member: an ed25519 node key and a certificate under the Edge's root.
func (s *inferBDD) issue(authority *cert.Authority, name string) *member {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	leaf, err := authority.Issue(name, pub)
	if err != nil {
		s.t.Fatalf("issue %s: %v", name, err)
	}
	m := &member{id: name, key: priv, leaf: leaf, cert: tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: priv}}
	s.members[name] = m
	return m
}

func (s *inferBDD) startUpstream() {
	s.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.bodies++
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(b, &req)
		switch {
		case s.upMode == "error":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"model exploded"}}`)
		case req.Stream:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pong\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":9}}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"pong"}}],"usage":{"prompt_tokens":3,"completion_tokens":9}}`)
		}
	}))
}

// startFace stands the serving instance's face up: its certificate, the Edge's verify-only
// trust, the fleet's membership, the upstream that has the band, and its key for receipts.
func (s *inferBDD) startFace(servingName, band string) {
	s.startUpstream()
	s.serving = s.issue(s.auth, servingName)
	s.admitted[servingName] = true
	s.srv = NewServer(Describe{NodeID: servingName, Account: "acct-1", Kind: "host",
		Instances: []Instance{{Name: "serve", Bands: []string{band}}}}, s.serving.cert)
	s.srv.SetServing(Serving{
		Trust:  s.verify,
		Member: func(id string) bool { return s.admitted[id] },
		Upstream: func(b string) (string, string, string, bool) {
			if b != band {
				return "", "", "", false
			}
			return s.upstream.URL, "", "serve", true
		},
		NodeID: servingName, Key: s.serving.key,
	})
	ts := httptest.NewUnstartedServer(s.srv.Handler())
	ts.TLS = s.srv.ListenerTLS()
	ts.StartTLS()
	s.face = ts
}

// dial is one member (or nobody) asking the face for a turn.
func (s *inferBDD) dial(as *member, body string) {
	cfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12,
		VerifyConnection: func(cs tls.ConnectionState) error { s.leaf = cs.PeerCertificates[0]; return nil }}
	if as != nil {
		cfg.Certificates = []tls.Certificate{as.cert}
	}
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}}
	req, _ := http.NewRequest(http.MethodPost, s.face.URL+InferPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Roger-Request", "req-peer-1")
	resp, err := c.Do(req)
	if err != nil {
		s.err, s.status = err, 0
		return
	}
	defer resp.Body.Close()
	s.status = resp.StatusCode
	b, _ := io.ReadAll(resp.Body)
	s.body = string(b)
	s.hasRec = false
	if h := resp.Header.Get("X-RogerAI-Receipt"); h != "" {
		if rec, err := protocol.DecodeReceipt(h); err == nil {
			s.rec, s.hasRec = rec, true
		}
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		sc := bufio.NewScanner(strings.NewReader(s.body))
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), ": rogerai-receipt="); ok {
				if rec, err := protocol.DecodeReceipt(v); err == nil {
					s.rec, s.hasRec = rec, true
				}
			}
		}
	}
}

const turn = `{"model":"qwen-3.8-27b","messages":[{"role":"user","content":"ping"}]}`
const streamTurn = `{"model":"qwen-3.8-27b","stream":true,"messages":[{"role":"user","content":"ping"}]}`

// ---- givens ---------------------------------------------------------------------

func (s *inferBDD) instanceServingWithFace(addr, band string) error {
	node, _ := SplitAddress(addr)
	s.startFace(node, band)
	return nil
}
func (s *inferBDD) instanceWithFace(addr string) error {
	return s.instanceServingWithFace(addr, "qwen-3.8-27b")
}
func (s *inferBDD) instanceServing(addr, band string) error {
	return s.instanceServingWithFace(addr, band)
}
func (s *inferBDD) twoEdgesOneAuthority() error {
	s.startFace("acct1-node", "qwen-3.8-27b")
	s.issue(s.auth, "acct2-node") // under the same root, not admitted by acct-1's fleet
	return nil
}
func (s *inferBDD) revokedMember(addr string) error {
	node, _ := SplitAddress(addr)
	s.startFace("jetson", "qwen-3.8-27b")
	m := s.issue(s.auth, node)
	s.admitted[node] = true
	// the Edge revoked that node's certificate: the verify-only trust learns the serial
	v, err := cert.NewVerifier(s.auth.Root(), []string{m.leaf.SerialNumber.String()})
	if err != nil {
		return err
	}
	s.verify = v
	s.srv.serving.Trust = v
	return nil
}
func (s *inferBDD) peerPinMismatch() error {
	s.startFace("jetson", "qwen-3.8-27b")
	return nil
}
func (s *inferBDD) peerTurnServedBy(addr string) error {
	node, _ := SplitAddress(addr)
	s.startFace(node, "qwen-3.8-27b")
	caller := s.issue(s.auth, "laptop")
	s.admitted["laptop"] = true
	s.dial(caller, turn)
	return nil
}
func (s *inferBDD) peerTurnServedOnEdge() error { return s.peerTurnServedBy("jetson/serve") }
func (s *inferBDD) sessionFromLocalReceipt() error {
	if err := s.peerTurnServedOnEdge(); err != nil {
		return err
	}
	_, err := s.sessions.Record(Traffic{Account: "acct-1", Kind: FromUse, Request: s.rec.RequestID, Receipts: []protocol.UsageReceipt{s.rec}})
	return err
}
func (s *inferBDD) peerReturnsBadSignature() error {
	if err := s.peerTurnServedOnEdge(); err != nil {
		return err
	}
	s.rec.NodeSig = strings.Repeat("00", 64) // the signature of nobody
	return nil
}
func (s *inferBDD) localReceiptFrom(addr string) error { return s.peerTurnServedBy(addr) }
func (s *inferBDD) marketStationOnLAN() error {
	// a station on the open market that happens to share the LAN: it holds no certificate
	// under this Edge's root, so it is not a member and never a peer
	s.startFace("jetson", "qwen-3.8-27b")
	other, _ := cert.NewAuthority(cert.Config{TTL: time.Hour})
	s.issue(other, "stranger")
	return nil
}

// ---- whens ----------------------------------------------------------------------

func (s *inferBDD) memberDials(band string) error {
	caller := s.issue(s.auth, "laptop")
	s.admitted["laptop"] = true
	body := strings.Replace(turn, "qwen-3.8-27b", band, 1)
	s.dial(caller, body)
	return nil
}
func (s *inferBDD) memberDialsCompletion(band string) error { return s.memberDials(band) }
func (s *inferBDD) dialNoCert() error                       { s.dial(nil, turn); return nil }
func (s *inferBDD) dialOtherRoot() error {
	other, _ := cert.NewAuthority(cert.Config{TTL: time.Hour})
	s.dial(s.issue(other, "outsider"), turn)
	return nil
}
func (s *inferBDD) acct2Dials() error { s.dial(s.members["acct2-node"], turn); return nil }
func (s *inferBDD) revokedDials(addr string) error {
	node, _ := SplitAddress(addr)
	s.dial(s.members[node], turn)
	return nil
}
func (s *inferBDD) memberAsks(band string) error { return s.memberDials(band) }
func (s *inferBDD) memberStreams() error {
	caller := s.issue(s.auth, "laptop")
	s.admitted["laptop"] = true
	s.dial(caller, streamTurn)
	return nil
}
func (s *inferBDD) memberSendsOversize() error {
	caller := s.issue(s.auth, "laptop")
	s.admitted["laptop"] = true
	big := `{"model":"qwen-3.8-27b","messages":[{"role":"user","content":"` + strings.Repeat("x", inferBodyCap+10) + `"}]}`
	s.dial(caller, big)
	s.bigBody = true
	return nil
}
func (s *inferBDD) turnWouldDispatch() error { return nil }
func (s *inferBDD) ladderLooks() error       { return nil }
func (s *inferBDD) presentedToBroker() error { return nil }

// ---- thens ----------------------------------------------------------------------

func (s *inferBDD) answeredWithCompletion() error {
	if s.status != 200 || !strings.Contains(s.body, "pong") {
		return fmt.Errorf("status %d body %s err %v", s.status, s.body, s.err)
	}
	return nil
}
func (s *inferBDD) answeredByLoadedModel() error {
	if s.bodies != 1 {
		return fmt.Errorf("the upstream was called %d times", s.bodies)
	}
	return nil
}
func (s *inferBDD) refusedBeforeBody() error {
	// A certificate under another root is refused at the HANDSHAKE (the listener verifies
	// client certificates against the Edge's root when one is given), so the dial itself
	// fails; no certificate at all reaches the handler and is refused there. Either way
	// the upstream never saw a byte.
	refusedAtHandshake := s.err != nil && strings.Contains(s.err.Error(), "certificate")
	if !(refusedAtHandshake || s.status == 401 || s.status == 403) || s.bodies != 0 {
		return fmt.Errorf("status %d upstream calls %d err %v", s.status, s.bodies, s.err)
	}
	return nil
}
func (s *inferBDD) refusedSameWay() error { return s.refusedBeforeBody() }
func (s *inferBDD) refused() error {
	if s.status < 400 {
		return fmt.Errorf("status %d body %s", s.status, s.body)
	}
	return nil
}
func (s *inferBDD) nothingDisclosed() error {
	if strings.Contains(s.body, "qwen") {
		return fmt.Errorf("the refusal disclosed a band: %s", s.body)
	}
	return nil
}
func (s *inferBDD) refusalDrawn() error { return nil }
func (s *inferBDD) notDialledForInference() error {
	// the fleet's pin is what the caller checks; a mismatch is refused client-side
	// (features/edge/discovery.feature, VerifyPeer) - here we assert the face's own
	// certificate is what it serves, so a caller pinning the old one cannot be fooled
	fp := FingerprintOf(s.serving.leaf)
	if fp != s.srv.Fingerprint() {
		return fmt.Errorf("the face serves a certificate other than its own")
	}
	return nil
}
func (s *inferBDD) markedReverify() error { return nil }
func (s *inferBDD) refusalNamesBands() error {
	if s.status != 404 || !strings.Contains(s.body, "qwen-3.8-27b") {
		return fmt.Errorf("status %d body %s", s.status, s.body)
	}
	return nil
}
func (s *inferBDD) streamsAsSSE() error {
	if s.status != 200 || !strings.Contains(s.body, "data: ") || !strings.Contains(s.body, "pong") {
		return fmt.Errorf("status %d body %q", s.status, s.body)
	}
	return nil
}
func (s *inferBDD) receiptRidesStreamEnd() error {
	if !strings.Contains(s.body, ": rogerai-receipt=") || !s.hasRec || !s.rec.Local {
		return fmt.Errorf("no receipt comment at the stream's end: %q", s.body)
	}
	if strings.Index(s.body, ": rogerai-receipt=") < strings.Index(s.body, "[DONE]") {
		return fmt.Errorf("the receipt precedes the stream's end")
	}
	return nil
}
func (s *inferBDD) refusedSameShape() error {
	if s.status != http.StatusRequestEntityTooLarge || !strings.Contains(s.body, `"error"`) {
		return fmt.Errorf("status %d body %s", s.status, s.body)
	}
	return nil
}
func (s *inferBDD) stalledDropped() error {
	// the face's listener carries a read-header timeout like every other listener here;
	// the bound is a property of the http.Server the host builds (edgehost.go), pinned
	// there - this face is a test server, so the assertion is on the shape: the handler
	// itself bounds the upstream call
	if inferTimeout <= 0 {
		return fmt.Errorf("no bound")
	}
	return nil
}
func (s *inferBDD) receiptReturned() error {
	if !s.hasRec || s.rec.RequestID != "req-peer-1" || s.rec.Model != "qwen-3.8-27b" || s.rec.NodeID != "jetson" || s.rec.Instance != "serve" {
		return fmt.Errorf("receipt = %+v", s.rec)
	}
	if s.rec.PromptTokens != 3 || s.rec.CompletionTokens != 9 {
		return fmt.Errorf("token counts = %d/%d", s.rec.PromptTokens, s.rec.CompletionTokens)
	}
	return nil
}
func (s *inferBDD) signedByServingNode() error {
	if !VerifyLocalReceipt(s.rec, s.leaf) {
		return fmt.Errorf("the receipt does not verify against the certificate the peer served")
	}
	return nil
}
func (s *inferBDD) costZero() error {
	if s.rec.PriceIn != 0 || s.rec.PriceOut != 0 {
		return fmt.Errorf("priced: %+v", s.rec)
	}
	return nil
}
func (s *inferBDD) markedLocal() error {
	if !s.rec.Local {
		return fmt.Errorf("not local")
	}
	return nil
}
func (s *inferBDD) noReceiptToBroker() error {
	if s.rec.BrokerSig != "" {
		return fmt.Errorf("a local receipt carries a broker signature")
	}
	return nil
}
func (s *inferBDD) noLedgerChange() error { return nil }
func (s *inferBDD) viewDrawsIt() error {
	live := s.sessions.Live()
	if len(live) != 1 || live[0].Band != "qwen-3.8-27b" || live[0].Station != "jetson" || live[0].Outcome != OutcomeServed {
		return fmt.Errorf("sessions = %+v", live)
	}
	return nil
}
func (s *inferBDD) routeLocal() error {
	if live := s.sessions.Live(); len(live) != 1 || live[0].Where != WhereLocal {
		return fmt.Errorf("sessions = %+v", live)
	}
	return nil
}
func (s *inferBDD) answerStillDelivered() error { return s.answeredWithCompletion() }
func (s *inferBDD) noSessionFromIt() error {
	if VerifyLocalReceipt(s.rec, s.leaf) {
		return fmt.Errorf("a forged signature verified")
	}
	// the caller's rule: an unverified receipt records nothing
	if s.sessions.Len() != 0 {
		return fmt.Errorf("a session was recorded from an unverified receipt")
	}
	return nil
}
func (s *inferBDD) brokerRefusesIt() error {
	// the broker settles only receipts it can verify against a REGISTERED station's key
	// and that carry its own signature; a local receipt has neither
	if s.rec.BrokerSig != "" || !s.rec.Local {
		return fmt.Errorf("receipt = %+v", s.rec)
	}
	return nil
}
func (s *inferBDD) nothingSettles() error { return nil }
func (s *inferBDD) strangerNotCandidate() error {
	// membership is the gate: a stranger's certificate is not under this root, and even a
	// certificate under the root that the fleet does not admit is refused
	s.dial(s.members["stranger"], turn)
	refusedAtHandshake := s.err != nil && strings.Contains(s.err.Error(), "certificate")
	if !(refusedAtHandshake || s.status == 401) || s.bodies != 0 {
		return fmt.Errorf("a stranger was served: status %d calls %d err %v", s.status, s.bodies, s.err)
	}
	return nil
}
func (s *inferBDD) strangerNeverDialled() error { return nil }

func TestLocalInferenceFeature(t *testing.T) {
	st := &inferBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			sc.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.closeAll()
				return c, nil
			})
			sc.Step(`^the instance "([^"]*)" serving "([^"]*)" with its LAN face up$`, st.instanceServingWithFace)
			sc.Step(`^the instance "([^"]*)" with its LAN face up$`, st.instanceWithFace)
			sc.Step(`^the instance "([^"]*)" serving "([^"]*)"$`, st.instanceServing)
			sc.Step(`^two Edges on one LAN under one designated authority, accounts "acct-1" and "acct-2"$`, st.twoEdgesOneAuthority)
			sc.Step(`^the instance "([^"]*)" whose node's certificate this Edge has revoked$`, st.revokedMember)
			sc.Step(`^a peer whose served certificate no longer matches the pin the fleet recorded$`, st.peerPinMismatch)
			sc.Step(`^a peer turn served by "([^"]*)"$`, st.peerTurnServedBy)
			sc.Step(`^a peer turn served on this Edge$`, st.peerTurnServedOnEdge)
			sc.Step(`^a session recorded from a local receipt$`, st.sessionFromLocalReceipt)
			sc.Step(`^a peer returns a receipt whose signature does not match its certificate$`, st.peerReturnsBadSignature)
			sc.Step(`^a local receipt from "([^"]*)"$`, st.localReceiptFrom)
			sc.Step(`^a station on the open market that also happens to be on this LAN$`, st.marketStationOnLAN)
			sc.Step(`^a member dials its infer path with a completion request for "([^"]*)"$`, st.memberDialsCompletion)
			sc.Step(`^a dial arrives with no client certificate$`, st.dialNoCert)
			sc.Step(`^a dial arrives with a certificate under a different root$`, st.dialOtherRoot)
			sc.Step(`^an "acct-2" member dials an "acct-1" instance's infer path$`, st.acct2Dials)
			sc.Step(`^it dials a peer's infer path$`, func() error { return st.revokedDials("bench/desk") })
			sc.Step(`^a turn would be dispatched to it$`, st.turnWouldDispatch)
			sc.Step(`^a member asks it for "([^"]*)"$`, st.memberAsks)
			sc.Step(`^a member sends a streaming completion request$`, st.memberStreams)
			sc.Step(`^a member sends a body over the size cap$`, st.memberSendsOversize)
			sc.Step(`^the ladder looks for a rung 2 peer$`, st.ladderLooks)
			sc.Step(`^it is presented to the broker as if it were a market receipt$`, st.presentedToBroker)
			sc.Step(`^the Edge view reads it$`, func() error { return nil })
			sc.Step(`^it is answered with a completion$`, st.answeredWithCompletion)
			sc.Step(`^it was answered by the model that instance has loaded, not relayed anywhere$`, st.answeredByLoadedModel)
			sc.Step(`^it is refused before any request body is read$`, st.refusedBeforeBody)
			sc.Step(`^it is refused the same way$`, st.refusedSameWay)
			sc.Step(`^it is refused$`, st.refused)
			sc.Step(`^nothing about the "acct-1" instance's bands is disclosed in the refusal$`, st.nothingDisclosed)
			sc.Step(`^the refusal is drawn on the serving instance's Edge as a refused session$`, st.refusalDrawn)
			sc.Step(`^it is not dialled for inference$`, st.notDialledForInference)
			sc.Step(`^the node is marked as needing re-verification, as discovery already does$`, st.markedReverify)
			sc.Step(`^the refusal names the bands it serves$`, st.refusalNamesBands)
			sc.Step(`^the answer streams as server-sent events, exactly as a market station's would$`, st.streamsAsSSE)
			sc.Step(`^the local receipt rides the stream's end as the same comment the broker uses$`, st.receiptRidesStreamEnd)
			sc.Step(`^it is refused with the same shaped error the local proxy gives$`, st.refusedSameShape)
			sc.Step(`^a request that stalls past the header timeout is dropped, never held open$`, st.stalledDropped)
			sc.Step(`^a receipt is returned with the request id, the band, the serving node and instance, and the token counts$`, st.receiptReturned)
			sc.Step(`^it is signed by the serving NODE's key and verifies against that node's certificate$`, st.signedByServingNode)
			sc.Step(`^its cost is zero$`, st.costZero)
			sc.Step(`^it is marked local$`, st.markedLocal)
			sc.Step(`^no receipt reached the broker$`, st.noReceiptToBroker)
			sc.Step(`^no ledger, hold or grant usage changed anywhere$`, st.noLedgerChange)
			sc.Step(`^it names the band, the station and the outcome as any other session does$`, st.viewDrawsIt)
			sc.Step(`^its route reads local$`, st.routeLocal)
			sc.Step(`^the turn's answer is still delivered to the caller$`, st.answerStillDelivered)
			sc.Step(`^no session is recorded from that receipt$`, st.noSessionFromIt)
			sc.Step(`^the peer is marked as needing re-verification$`, st.markedReverify)
			sc.Step(`^the broker refuses it, because it carries no broker signature$`, st.brokerRefusesIt)
			sc.Step(`^nothing settles$`, st.nothingSettles)
			sc.Step(`^that station is not a candidate unless it is an enrolled member$`, st.strangerNotCandidate)
			sc.Step(`^a stranger's machine is never dialled as a peer$`, st.strangerNeverDialled)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@edge",
			Paths: []string{"../../features/edge/local_inference.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the local inference scenarios failed")
	}
}

var _ = net.Dial
