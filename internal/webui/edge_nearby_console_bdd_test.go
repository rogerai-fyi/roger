package webui

// Executable spec: features/edge/empty_edge_simple.feature §7 (@console) - the console's NEARBY
// list. The real console server over HTTP, a REAL local authority on disk behind Invite (so the
// invite the console shows is checked against the issuing code), and the SHIPPED
// console-edge-nearby.js EXECUTED with node against that server: it renders the rows, clicks Invite,
// clicks Approve on a phone (a claim) and on a roger device (wait, then the phrase), and decides.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/edgeauth"
	"rogerai.fm/roger/v7/internal/joininvite"
	"rogerai.fm/roger/v7/internal/look"
	"rogerai.fm/roger/v7/internal/ownerjoin"
	"rogerai.fm/roger/v7/internal/store"
	"rogerai.fm/roger/v7/internal/tower"
)

type nearbyConsoleBDD struct {
	t       *testing.T
	now     time.Time
	local   *edgeauth.Local
	cands   []store.EdgeNode
	keys    map[string]ed25519.PublicKey
	asks    int // Requests calls: the roger device asks from the second read on
	decided []string
	srv     *httptest.Server
	tok     string
	out     struct {
		Text    string   `json:"text"`
		Buttons []string `json:"buttons"`
		Invite  string   `json:"invite"`
		Grant   struct {
			State string `json:"state"`
			Say   string `json:"say"`
		} `json:"grant"`
		Wait   string `json:"wait"`
		Asked  string `json:"asked"`
		Phrase string `json:"phrase"`
	}
}

func (s *nearbyConsoleBDD) reset(t *testing.T) {
	if s.srv != nil {
		s.srv.Close()
	}
	s.t, s.now = t, time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	if err != nil {
		t.Fatal(err)
	}
	s.local, s.cands, s.keys, s.asks, s.decided = local, nil, map[string]ed25519.PublicKey{}, 0, nil
	fleet := edge.NewFleet(store.NewMem(), "acct-1")
	authority := ownerjoin.Tower{Dir: "edge:authority", Label: "shed", Addr: "192.168.1.5:8791"}
	srv := New(nil, Options{
		Edge: EdgeHooks{
			Self: "shed", Fleet: fleet, Now: func() time.Time { return s.now },
			Candidates: func() []store.EdgeNode { return s.cands },
			Status: func() edge.SelfStatus {
				return edge.SelfStatus{Root: edge.RootLocal, AuthorityHere: true, Enrolled: true, Name: "shed"}
			},
			InviteFor: func(n store.EdgeNode) (string, time.Time, error) {
				code, err := s.local.MintBearerCodeFor(n.ID)
				if err != nil {
					return "", time.Time{}, err
				}
				return joininvite.Invite{Kind: joininvite.Edge, Addrs: []string{authority.Addr}, FP: s.local.Descriptor().Fingerprint,
					Code: code, For: n.ID}.String(), s.now.Add(edgeauth.CodeLifetime), nil
			},
			Adopt: func(id, name string) error { return s.local.GrantClaim(id, name) },
		},
		Owner: &ownerjoin.Hooks{
			Edge: func() (ownerjoin.Tower, bool) { return authority, true },
			Requests: func(t ownerjoin.Tower) ([]tower.PairRequest, error) {
				s.asks++
				if s.asks < 2 {
					return nil, nil
				}
				return []tower.PairRequest{{ID: "r1", Name: "deskside", Phrase: "amber-river-71", ExpiresAt: s.now.Add(time.Minute)}}, nil
			},
			Decide: func(dir, id string, approve bool) error {
				s.decided = append(s.decided, fmt.Sprintf("%s %s %v", dir, id, approve))
				return nil
			},
		},
	})
	s.tok = srv.Token()
	s.srv = httptest.NewServer(srv.Handler())
}

func (s *nearbyConsoleBDD) device(label, name, pin string) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		s.t.Fatal(err)
	}
	s.keys[label] = pub
	s.cands = append(s.cands, store.EdgeNode{ID: edgeauth.NodeID(pub), Name: name, Kind: "mobile", Pin: pin,
		Presence: string(edge.PresenceCandidate), LastSeen: s.now.Add(-12 * time.Second).Unix()})
}

func (s *nearbyConsoleBDD) phonePings(label string) error {
	s.device(label, "gentle-ibex-14", "")
	return nil
}

// opens runs the shipped module against the live server, and drives Invite and Approve on it.
func (s *nearbyConsoleBDD) opens() error {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		return fmt.Errorf("node is needed to execute the console's JavaScript: %v", err)
	}
	// The roger device whose Approve waits for it to ask (the phone's Approve is a claim).
	s.device("desk", "deskside", strings.Repeat("ab", 32))
	script := `
const api = require(process.argv[1]);
const base = process.argv[2], tok = process.argv[3], phone = process.argv[4], desk = process.argv[5];
const out = {};
function el(tag) {
  return { tag, children: [], attrs: {}, hidden: false, textContent: "", className: "", handlers: {},
    appendChild(c) { this.children.push(c); return c; }, setAttribute(k, v) { this.attrs[k] = String(v); },
    addEventListener(n, f) { this.handlers[n] = f; }, replaceChildren() { this.children = []; } };
}
function visible(e) { return e.hidden ? [] : [e].concat(e.children.flatMap(visible)); }
function words(e) { return visible(e).map((x) => x.textContent).filter(Boolean).join("\n"); }
const host = el("div");
const actions = { fetch: (p, o) => fetch(base + p + (p.includes("?") ? "&" : "?") + "t=" + tok, o), poll: false };
(async () => {
  const d = await (await actions.fetch("/api/edge", { method: "GET" })).json();
  const view = api.render({ createElement: el }, host, d, actions);
  out.text = words(host);
  out.buttons = visible(host).filter((e) => e.tag === "button").map((e) => e.textContent);
  await view.invite(phone);
  out.invite = words(host);
  out.grant = await view.approve(phone);
  await view.approve(desk);
  out.wait = words(host);
  const a = await view.check(desk);
  out.asked = words(host);
  out.phrase = a.phrase || "";
  await view.decide(a, true);
  process.stdout.write(JSON.stringify(out));
})().catch((e) => { console.error(e); process.exit(2); });
`
	cmd := exec.Command(nodeBin, "-e", script, "./assets/console-edge-nearby.js", s.srv.URL, s.tok,
		s.cands[0].ID, s.cands[len(s.cands)-1].ID)
	b, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("console-edge-nearby.js failed: %v: %s", err, b)
	}
	return json.Unmarshal(b, &s.out)
}

func (s *nearbyConsoleBDD) listWithButtons() error {
	phone := s.cands[0]
	for _, w := range []string{"NEARBY", look.NearbyActions, "gentle-ibex-14", "mobile", "seen 12s ago",
		"key " + look.NearbyShort(strings.TrimPrefix(phone.ID, "n_"))} {
		if !strings.Contains(s.out.Text, w) {
			return fmt.Errorf("the NEARBY list lacks %q:\n%s", w, s.out.Text)
		}
	}
	if strings.Count(strings.Join(s.out.Buttons, "|"), "Invite") != 2 || strings.Count(strings.Join(s.out.Buttons, "|"), "Approve") != 2 {
		return fmt.Errorf("each row should have Invite and Approve, got %v", s.out.Buttons)
	}
	if strings.Contains(s.out.Text, "roger edge adopt") {
		return fmt.Errorf("the list still names adopt:\n%s", s.out.Text)
	}
	return nil
}

func (s *nearbyConsoleBDD) inviteAndApprove() error {
	phone := s.cands[0]
	// Invite: the quoted join line, bound to the phone, and the QR.
	var line string
	for _, ln := range strings.Split(s.out.Invite, "\n") {
		if strings.HasPrefix(ln, "roger join '") {
			line = ln
		}
	}
	in, err := joininvite.Parse(joininvite.FromLine(line))
	if err != nil {
		return fmt.Errorf("no join line under the row (%v):\n%s", err, s.out.Invite)
	}
	if in.For != phone.ID {
		return fmt.Errorf("the invite is for %q, not the phone", in.For)
	}
	if !strings.ContainsAny(s.out.Invite, "▀▄█") {
		return fmt.Errorf("no QR:\n%s", s.out.Invite)
	}
	// It is bound for real: another key is refused by the authority that minted it.
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	_, user, _ := ed25519.GenerateKey(rand.Reader)
	req := edgeauth.NewRequest(edgeauth.LocalAccount, "x", "mobile", other, time.Now())
	req.Sign(user)
	req.JoinProof = edgeauth.JoinProof(in.Code, in.FP, req)
	if _, err := s.local.Issue(req); !errors.Is(err, edgeauth.ErrInviteForOther) {
		return fmt.Errorf("another device redeemed the console's invite: %v", err)
	}
	// Approve on the phone grants its claim (it joins by claim, not by asking).
	if s.out.Grant.State != "granted" || !s.local.ClaimGranted(phone.ID) {
		return fmt.Errorf("Approve on a phone did not grant its claim: %+v", s.out.Grant)
	}
	// Approve on a roger device waits for it to ask, naming what it runs, then shows the phrase.
	for _, w := range []string{look.NearbyWaitAsk("deskside"), look.NearbyRunLine("192.168.1.5:8791")} {
		if !strings.Contains(s.out.Wait, w) {
			return fmt.Errorf("Approve's wait lacks %q:\n%s", w, s.out.Wait)
		}
	}
	if s.out.Phrase != "amber-river-71" || !strings.Contains(s.out.Asked, "amber-river-71") {
		return fmt.Errorf("the phrase is not shown once it asked (%q):\n%s", s.out.Phrase, s.out.Asked)
	}
	if len(s.decided) != 1 || s.decided[0] != "edge:authority r1 true" {
		return fmt.Errorf("the decide reached %v", s.decided)
	}
	return nil
}
