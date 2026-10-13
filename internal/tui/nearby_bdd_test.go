package tui

// Executable spec: features/edge/empty_edge_simple.feature §7 (@tui) - a nearby device can be
// invited or approved from the sighting (founder ruling 2026-10-12). The real model and renderer;
// candidates the way the host hands them over; a REAL local authority (internal/edgeauth, on disk
// in a temp dir) behind i, so "an invite bound to one device is refused for another" is checked
// against the real issuing code with real signed requests, not a fake; the owner's join hooks
// (request-and-approve) through the same ownerjoin seam the host fills.

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/look"
	"rogerai.fm/roger/v6/internal/ownerjoin"
	"rogerai.fm/roger/v6/internal/store"
	towerpkg "rogerai.fm/roger/v6/internal/tower"
)

// nearbyFixture is §7's world: the devices pinging, their node keys (a node id is derived from a
// node key, so the spec's labels map onto real ids), the authority, the owner's hooks.
type nearbyFixture struct {
	t       *testing.T
	cands   []store.EdgeNode
	keys    map[string]ed25519.PublicKey // spec label -> the device's node key
	ids     map[string]string            // spec label -> its node id
	local   *edgeauth.Local
	invited map[string]string // node id -> the join line i minted
	reqs    []towerpkg.PairRequest
	decided map[string]bool
	tower   ownerjoin.Tower
	serving bool
	addr    string
	lastErr error
	// kept copies, for "when instead the owner presses n"
	savedCands []store.EdgeNode
	savedReqs  []towerpkg.PairRequest
}

func (f *nearbyFixture) reset(t *testing.T) {
	f.t = t
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	if err != nil {
		t.Fatal(err)
	}
	f.cands, f.reqs, f.lastErr = nil, nil, nil
	f.keys, f.ids, f.invited, f.decided = map[string]ed25519.PublicKey{}, map[string]string{}, map[string]string{}, map[string]bool{}
	f.local, f.serving = local, false
	f.addr = "192.168.1.5:8791"
	f.tower = ownerjoin.Tower{Dir: "edge:authority", Label: "shed", Addr: f.addr, FP: edgeauth.ShortFingerprint(local.Descriptor().Fingerprint)}
}

// id is the real node id behind a spec label, minting a node key the first time.
func (f *nearbyFixture) id(label string) string {
	if id, ok := f.ids[label]; ok {
		return id
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		f.t.Fatal(err)
	}
	f.keys[label], f.ids[label] = pub, edgeauth.NodeID(pub)
	return f.ids[label]
}

// add puts a device on the network: a phone (no certificate) or a roger device serving one.
func (f *nearbyFixture) add(label, name, kind, pin string, seen time.Time) {
	f.cands = append(f.cands, store.EdgeNode{ID: f.id(label), Name: name, Kind: kind, Pin: pin,
		Presence: string(edge.PresenceCandidate), LastSeen: seen.Unix()})
}

func (f *nearbyFixture) drop(id string) {
	kept := f.cands[:0:0]
	for _, c := range f.cands {
		if c.ID != id {
			kept = append(kept, c)
		}
	}
	f.cands = kept
}

// wire fills the hooks the way the host does: candidates, the bound invite (real authority), the
// claim grant for a, the adopting list and codes, and the owner's request-and-approve.
func (f *nearbyFixture) wire(h *Hooks, fleet *edge.Fleet, now func() time.Time) {
	h.EdgeInviteFor = func(n store.EdgeNode) (string, time.Time, error) {
		code, err := f.local.MintBearerCodeFor(n.ID)
		if err != nil {
			return "", time.Time{}, err
		}
		if n.Pin == "" {
			if err := f.local.GrantClaim(n.ID, n.Name); err != nil {
				return "", time.Time{}, err
			}
			if _, err := f.local.MintNodeCode(n.ID); err != nil {
				return "", time.Time{}, err
			}
		}
		line := joininvite.Invite{Kind: joininvite.Edge, Addrs: []string{f.addr}, FP: f.local.Descriptor().Fingerprint, Code: code, For: n.ID}.String()
		f.invited[n.ID] = line
		return line, now().Add(edgeauth.CodeLifetime), nil
	}
	h.EdgeAdopt = func(id, name string) error {
		for _, c := range f.cands {
			if c.ID == id && c.Pin == "" {
				if err := f.local.GrantClaim(id, name); err != nil {
					return err
				}
				_, err := f.local.MintNodeCode(id)
				f.drop(id)
				return err
			}
		}
		return errors.New("not a claim-only candidate")
	}
	h.EdgeAdopting = func() []store.EdgeNode {
		grants, _ := f.local.Claims()
		var out []store.EdgeNode
		for _, g := range grants {
			if _, known, _ := fleet.Get(g.NodeID); known {
				continue
			}
			out = append(out, store.EdgeNode{ID: g.NodeID, Name: g.Name})
		}
		return out
	}
	h.EdgeAdoptCode = func(id string) (string, time.Time, bool) {
		code, until, ok := f.local.NodeCode(id)
		return edgeauth.FormatCode(code), until, ok
	}
	h.Owner = &ownerjoin.Hooks{
		Edge:     func() (ownerjoin.Tower, bool) { return f.tower, f.serving },
		Requests: func(t ownerjoin.Tower) ([]towerpkg.PairRequest, error) { return f.reqs, nil },
		Decide: func(dir, id string, approve bool) error {
			for i, r := range f.reqs {
				if r.ID != id {
					continue
				}
				f.decided[id] = approve
				f.reqs = append(f.reqs[:i:i], f.reqs[i+1:]...)
				if approve {
					// The device enrolls with its key: a member now, and off the candidate list on the
					// next pass (as the host's edgeDropMembers does).
					for _, c := range f.cands {
						if c.Name == r.Name {
							if _, err := fleet.Enroll(store.EdgeNode{ID: c.ID, Name: c.Name, Kind: c.Kind, Presence: "VERIFIED", LastSeen: now().Unix()}); err != nil {
								return err
							}
							f.drop(c.ID)
						}
					}
				}
				return nil
			}
			return errors.New("no such request")
		},
	}
}

// present is a device with the given node key presenting an invite's code: a real signed request,
// issued against the real authority.
func (f *nearbyFixture) present(line string, nodePub ed25519.PublicKey) error {
	in, err := joininvite.Parse(line)
	if err != nil {
		return err
	}
	_, user, _ := ed25519.GenerateKey(rand.Reader)
	req := edgeauth.NewRequest(edgeauth.LocalAccount, "device", "host", nodePub, time.Now())
	req.Sign(user)
	req.JoinProof = edgeauth.JoinProof(in.Code, in.FP, req)
	_, err = f.local.Issue(req)
	return err
}

func (f *nearbyFixture) otherKey() ed25519.PublicKey {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	return pub
}

// ---- givens ----------------------------------------------------------------

func (s *emptySimpleBDD) rootsEdge() error {
	if err := s.enrolledAs("shed"); err != nil {
		return err
	}
	s.status.AuthorityAddr = "http://" + s.nb.addr
	s.nb.serving = true
	return nil
}

func (s *emptySimpleBDD) noEdgePhonePings(id, kind, name string) error {
	s.nb.add(id, name, kind, "", s.now.Add(-12*time.Second))
	return nil
}
func (s *emptySimpleBDD) rootsWithMemberAndPhone(id string) error {
	if err := s.rootsEdge(); err != nil {
		return err
	}
	if _, err := s.fleet.Enroll(store.EdgeNode{ID: "node-garage", Name: "garage", Kind: "host", Presence: "VERIFIED", LastSeen: s.now.Unix()}); err != nil {
		return err
	}
	s.nb.add(id, "gentle-ibex-14", "mobile", "", s.now.Add(-12*time.Second))
	return nil
}
func (s *emptySimpleBDD) rootsAndPhone(id string) error {
	if err := s.rootsEdge(); err != nil {
		return err
	}
	s.nb.add(id, "gentle-ibex-14", "mobile", "", s.now.Add(-12*time.Second))
	return nil
}
func (s *emptySimpleBDD) invitedFromRow(id string) error {
	if err := s.rootsAndPhone(id); err != nil {
		return err
	}
	if err := s.selectsAndPresses(id, "i"); err != nil {
		return err
	}
	s.key("esc") // close the invite panel; the row is what is checked
	s.render(100)
	return nil
}
func (s *emptySimpleBDD) rootsServingAndRoger(id, name string) error {
	if err := s.rootsEdge(); err != nil {
		return err
	}
	s.nb.add(id, name, "host", strings.Repeat("ab", 32), s.now.Add(-5*time.Second))
	return nil
}
func (s *emptySimpleBDD) phoneNoCert(id string) error { return s.rootsAndPhone(id) }
func (s *emptySimpleBDD) phoneStale() error {
	if err := s.rootsEdge(); err != nil {
		return err
	}
	s.nb.add("stale", "gentle-ibex-14", "mobile", "", s.now.Add(-20*time.Minute))
	return nil
}
func (s *emptySimpleBDD) phoneNamedNearby(name, id string) error {
	if err := s.rootsEdge(); err != nil {
		return err
	}
	s.nb.add(id, name, "mobile", "", s.now.Add(-12*time.Second))
	return nil
}
func (s *emptySimpleBDD) impostorSameName(id string) error {
	name := s.nb.cands[0].Name
	s.nb.add(id, name, "mobile", "", s.now.Add(-3*time.Second))
	return nil
}
func (s *emptySimpleBDD) twoPhonesNamed(name string) error {
	if err := s.rootsEdge(); err != nil {
		return err
	}
	s.nb.add("one", name, "mobile", "", s.now.Add(-12*time.Second))
	s.nb.add("two", name, "mobile", "", s.now.Add(-9*time.Second))
	return nil
}

// ---- whens -----------------------------------------------------------------

// selectCand puts the highlight on a device's row, on the map or in the empty state.
func (s *emptySimpleBDD) selectCand(id string) error {
	if err := s.open(100); err != nil {
		return err
	}
	if s.m.edgeMapActive() {
		for i, it := range s.m.edgeNetItems() {
			if it.id == id {
				s.m.edge.netSel, s.m.edge.netSelSet = i, true
				return nil
			}
		}
		return fmt.Errorf("%s is not on the map", id)
	}
	s.m.edge.sel = id
	return nil
}

// selectsAndPresses selects the labelled device's row and presses k, landing what it dispatched.
func (s *emptySimpleBDD) selectsAndPresses(label, k string) error {
	id := s.nb.id(label)
	if err := s.selectCand(id); err != nil {
		return err
	}
	cmd := s.key(k)
	for _, msg := range execCmd(cmd) {
		s.update(msg)
	}
	s.render(100)
	return nil
}
func (s *emptySimpleBDD) selectsAndPressesLast(k string) error {
	return s.selectsAndPresses("stale", k)
}
func (s *emptySimpleBDD) otherDevicePresents() error {
	s.nb.lastErr = s.nb.present(s.oneInvite(), s.nb.otherKey())
	return nil
}
func (s *emptySimpleBDD) deviceJoinsWithInvite() error {
	label := s.onlyLabel()
	if err := s.nb.present(s.oneInvite(), s.nb.keys[label]); err != nil {
		return fmt.Errorf("the device the invite is for could not join: %v", err)
	}
	// What the host does once the device has its certificate and checks in: a member, no longer
	// a candidate.
	c := s.nb.cands[0]
	if _, err := s.fleet.Enroll(store.EdgeNode{ID: c.ID, Name: c.Name, Kind: c.Kind, Presence: "VERIFIED", LastSeen: s.now.Unix()}); err != nil {
		return err
	}
	s.nb.drop(c.ID)
	s.update(edgeStatusTickMsg{})
	s.m.refreshEdge()
	s.render(100)
	return nil
}
func (s *emptySimpleBDD) asksToJoin(name string) error {
	s.nb.savedCands, s.nb.savedReqs = append([]store.EdgeNode(nil), s.nb.cands...), nil
	s.nb.reqs = []towerpkg.PairRequest{{ID: "r1", Name: name, Kind: towerpkg.JoinClient, Phrase: "amber-kettle-harbor", ExpiresAt: s.now.Add(2 * time.Minute)}}
	s.nb.savedReqs = append([]towerpkg.PairRequest(nil), s.nb.reqs...)
	return s.pollOwner()
}
func (s *emptySimpleBDD) pollOwner() error {
	cmd := s.update(OwnerJoinRefresh{})
	for _, msg := range execCmd(cmd) {
		s.update(msg)
	}
	s.render(100)
	return nil
}
func (s *emptySimpleBDD) pressesYN(k string) error {
	cmd := s.key(k)
	for _, msg := range execCmd(cmd) {
		s.update(msg)
	}
	s.m.refreshEdge()
	s.render(100)
	return nil
}
func (s *emptySimpleBDD) insteadPressesN() error {
	// The same sighting and the same ask, answered the other way.
	s.nb.cands = append([]store.EdgeNode(nil), s.nb.savedCands...)
	s.nb.reqs = append([]towerpkg.PairRequest(nil), s.nb.savedReqs...)
	s.nb.decided = map[string]bool{}
	s.fleet = edge.NewFleet(store.NewMem(), "acct-1")
	s.fleet.SetClock(func() time.Time { return s.now })
	s.built = false
	s.m = model{}
	if err := s.selectsAndPresses(s.onlyLabel(), "a"); err != nil {
		return err
	}
	if err := s.pollOwner(); err != nil {
		return err
	}
	return s.pressesYN("n")
}

func (s *emptySimpleBDD) oneInvite() string {
	for _, line := range s.nb.invited {
		return line
	}
	return "" // no invite: present() then fails to parse, which the step reports
}
func (s *emptySimpleBDD) onlyLabel() string {
	for label := range s.nb.ids {
		return label
	}
	return ""
}

// ---- thens -----------------------------------------------------------------

// nearbyRows are the lines under the NEARBY eyebrow (up to the next blank line).
func (s *emptySimpleBDD) nearbyRows() ([]string, error) {
	lines := strings.Split(s.out, "\n")
	for i, ln := range lines {
		if strings.Contains(ln, look.Eyebrow(look.NearbyEyebrow)) {
			var rows []string
			for _, r := range lines[i+1:] {
				if strings.TrimSpace(r) == "" {
					break
				}
				if strings.HasPrefix(strings.TrimSpace(r), "↳") && len(rows) > 0 {
					rows[len(rows)-1] += " " + strings.TrimSpace(r) // a row's state, continued under it
					continue
				}
				rows = append(rows, r)
			}
			return rows, nil
		}
	}
	return nil, fmt.Errorf("no NEARBY eyebrow on the screen:\n%s", s.out)
}

func (s *emptySimpleBDD) nearbyRowReads(name, kind string) error {
	rows, err := s.nearbyRows()
	if err != nil {
		return err
	}
	id := s.nb.cands[0].ID
	short := strings.TrimPrefix(id, "n_")[:12]
	for _, r := range rows {
		if strings.Contains(r, name) && strings.Contains(r, kind) && strings.Contains(r, "ago") && strings.Contains(r, short) {
			return nil
		}
	}
	return fmt.Errorf("no row with %q, %q, an age and the short key %q under NEARBY:\n%s", name, kind, short, strings.Join(rows, "\n"))
}
func (s *emptySimpleBDD) rowOffers(what string) error {
	rows, err := s.nearbyRows()
	if err != nil {
		return err
	}
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, what) && !strings.Contains(s.out, what) {
		return fmt.Errorf("the rows do not offer %q:\n%s", what, s.out)
	}
	return nil
}
func (s *emptySimpleBDD) nowhereOnScreen(what string) error {
	if strings.Contains(s.frame(), what) {
		return fmt.Errorf("%q is on the screen:\n%s", what, s.frame())
	}
	return nil
}
func (s *emptySimpleBDD) sameRowUnderMembers(what string) error {
	rows, err := s.nearbyRows()
	if err != nil {
		return err
	}
	ni := strings.Index(s.out, look.Eyebrow(look.NearbyEyebrow))
	mi := strings.Index(s.out, "garage")
	if mi < 0 || mi > ni {
		return fmt.Errorf("the member garage is not drawn above NEARBY:\n%s", s.out)
	}
	if !strings.Contains(strings.Join(rows, "\n"), "gentle-ibex-14") {
		return fmt.Errorf("the phone's row is not under NEARBY:\n%s", s.out)
	}
	return s.rowOffers(what)
}
func (s *emptySimpleBDD) inviteMintedFor(label string) error {
	id := s.nb.id(label)
	line, ok := s.nb.invited[id]
	if !ok {
		return fmt.Errorf("no invite was minted for %s", label)
	}
	in, err := joininvite.Parse(line)
	if err != nil {
		return err
	}
	if in.Kind != joininvite.Edge || in.For != id || in.Code == "" {
		return fmt.Errorf("the invite is %+v, want kind edge, for=%s, a code", in, id)
	}
	return nil
}
func (s *emptySimpleBDD) showsQRAndLineRowInvited() error {
	// Quoted: the invite carries & between its fields, so an unquoted line pasted into a shell runs
	// `roger join roger://join?kind=edge` in the background and drops the code.
	if !strings.Contains(s.joined(), "roger join 'roger://join?") {
		return fmt.Errorf("the join line is not on the screen:\n%s", s.out)
	}
	if !strings.Contains(s.out, "▀") && !strings.Contains(s.out, "▄") && !strings.Contains(s.out, "█") && !strings.Contains(s.out, "paste the line") {
		return fmt.Errorf("no QR (or its too-small note) on the screen:\n%s", s.out)
	}
	s.key("esc")
	s.render(100)
	return s.rowReads(look.NearbyInvited)
}
func (s *emptySimpleBDD) rowReads(what string) error {
	rows, err := s.nearbyRows()
	if err != nil {
		return err
	}
	if !strings.Contains(strings.Join(rows, "\n"), what) {
		return fmt.Errorf("no row reads %q:\n%s", what, strings.Join(rows, "\n"))
	}
	return nil
}
func (s *emptySimpleBDD) claimGranted(label string) error {
	if !s.nb.local.ClaimGranted(s.nb.id(label)) {
		return fmt.Errorf("no claim grant for %s on the authority", label)
	}
	return nil
}
func (s *emptySimpleBDD) refusedNotSpent() error {
	if !errors.Is(s.nb.lastErr, edgeauth.ErrInviteForOther) {
		return fmt.Errorf("another device presenting the invite got %v, want %v", s.nb.lastErr, edgeauth.ErrInviteForOther)
	}
	// Not spent: a second wrong device is refused the same way (a spent invite says unknown machine).
	if err := s.nb.present(s.oneInvite(), s.nb.otherKey()); !errors.Is(err, edgeauth.ErrInviteForOther) {
		return fmt.Errorf("the invite was spent by the refusal: second attempt got %v", err)
	}
	return nil
}
func (s *emptySimpleBDD) rowStillInvited(label string) error {
	s.m.refreshEdge()
	s.render(100)
	return s.rowReads(look.NearbyInvited)
}
func (s *emptySimpleBDD) leftNearbyIsMember() error {
	name := "gentle-ibex-14"
	if rows, err := s.nearbyRows(); err == nil && strings.Contains(strings.Join(rows, "\n"), name) {
		return fmt.Errorf("%s is still under NEARBY:\n%s", name, s.out)
	}
	if !strings.Contains(s.out, name) {
		return fmt.Errorf("%s is not drawn as a member:\n%s", name, s.out)
	}
	return nil
}
func (s *emptySimpleBDD) statusSaysJoined() error {
	st := stripANSI(s.m.status)
	if !strings.Contains(st, "gentle-ibex-14") || !strings.Contains(st, "joined") {
		return fmt.Errorf("the status line reads %q", st)
	}
	return nil
}
func (s *emptySimpleBDD) waitingForAsk(name string) error {
	f := strings.Join(strings.Fields(s.frame()), " ")
	if err := contains(f, look.NearbyWaitAsk(name), "roger join "+s.nb.addr); err != nil {
		return fmt.Errorf("%v\n%s", err, s.frame())
	}
	return nil
}
func (s *emptySimpleBDD) phraseBesideRow() error {
	rows, err := s.nearbyRows()
	if err != nil {
		return err
	}
	for _, r := range rows {
		if strings.Contains(r, "deskside") && strings.Contains(r, "amber-kettle-harbor") {
			return nil
		}
	}
	return fmt.Errorf("the phrase is not beside deskside's row:\n%s", s.out)
}
func (s *emptySimpleBDD) admittedMember(name string) error {
	if !s.nb.decided["r1"] {
		return fmt.Errorf("the request was not approved: %v", s.nb.decided)
	}
	if rows, err := s.nearbyRows(); err == nil && strings.Contains(strings.Join(rows, "\n"), name) {
		return fmt.Errorf("%s is still under NEARBY:\n%s", name, s.out)
	}
	if !strings.Contains(s.out, name) {
		return fmt.Errorf("%s is not drawn as a member:\n%s", name, s.out)
	}
	return nil
}
func (s *emptySimpleBDD) refusedStaysNearby(name string) error {
	if v, ok := s.nb.decided["r1"]; !ok || v {
		return fmt.Errorf("the request was not refused: %v", s.nb.decided)
	}
	return s.rowReads(name)
}
func (s *emptySimpleBDD) claimGrantedCodeShown() error {
	label := s.onlyLabel()
	if err := s.claimGranted(label); err != nil {
		return err
	}
	code, _, ok := s.nb.local.NodeCode(s.nb.id(label))
	if !ok {
		return fmt.Errorf("no code was minted")
	}
	f := s.frame() + "\n" + stripANSI(s.m.status)
	if !strings.Contains(f, edgeauth.FormatCode(code)) {
		return fmt.Errorf("the code %s is not shown:\n%s", edgeauth.FormatCode(code), f)
	}
	return nil
}
func (s *emptySimpleBDD) staleRow() error {
	return s.rowReads("last seen 20m ago")
}
func (s *emptySimpleBDD) staleInviteWorks() error {
	if err := s.selectsAndPressesLast("i"); err != nil {
		return err
	}
	if len(s.nb.invited) != 1 {
		return fmt.Errorf("i minted %d invites", len(s.nb.invited))
	}
	s.key("esc")
	s.render(100)
	return s.rowReads(look.NearbyComeBack)
}
func (s *emptySimpleBDD) twoRowsById() error {
	rows, err := s.nearbyRows()
	if err != nil {
		return err
	}
	var hits []string
	for _, r := range rows {
		if strings.Contains(r, s.nb.cands[0].Name) {
			hits = append(hits, r)
		}
	}
	if len(hits) != 2 {
		return fmt.Errorf("want two rows named %q, got %d:\n%s", s.nb.cands[0].Name, len(hits), strings.Join(rows, "\n"))
	}
	a, b := strings.TrimPrefix(s.nb.cands[0].ID, "n_")[:12], strings.TrimPrefix(s.nb.cands[1].ID, "n_")[:12]
	if !(strings.Contains(hits[0], a) && strings.Contains(hits[1], b)) && !(strings.Contains(hits[0], b) && strings.Contains(hits[1], a)) {
		return fmt.Errorf("the two rows are not told apart by id:\n%s", strings.Join(hits, "\n"))
	}
	return nil
}
func (s *emptySimpleBDD) inviteNotRedeemableBy(forLabel, byLabel string) error {
	if err := s.selectsAndPresses(forLabel, "i"); err != nil {
		return err
	}
	s.key("esc")
	line, ok := s.nb.invited[s.nb.id(forLabel)]
	if !ok {
		return fmt.Errorf("no invite for %s", forLabel)
	}
	if err := s.nb.present(line, s.nb.keys[byLabel]); !errors.Is(err, edgeauth.ErrInviteForOther) {
		return fmt.Errorf("%s redeemed %s's invite: %v", byLabel, forLabel, err)
	}
	return nil
}

func (s *emptySimpleBDD) nearbySteps(sc *godog.ScenarioContext) {
	sc.Given(`^this machine is in no Edge and a phone advertises itself with node id "([^"]*)", kind "([^"]*)", name "([^"]*)"$`, s.noEdgePhonePings)
	sc.Given(`^this machine roots an Edge with one member and a phone advertises itself with node id "([^"]*)"$`, s.rootsWithMemberAndPhone)
	sc.Given(`^this machine roots an Edge and a phone advertises itself with node id "([^"]*)"$`, s.rootsAndPhone)
	sc.Given(`^the owner invited node "([^"]*)" from its row$`, s.invitedFromRow)
	sc.Given(`^this machine roots an Edge with its authority serving, and a roger with no Edge advertises itself with node id "([^"]*)", name "([^"]*)"$`, s.rootsServingAndRoger)
	sc.Given(`^a phone advertises itself with node id "([^"]*)" and no certificate$`, s.phoneNoCert)
	sc.Given(`^a phone advertised itself 20 minutes ago and has not been seen since$`, s.phoneStale)
	sc.Given(`^a phone "([^"]*)" with node id "([^"]*)" is nearby$`, s.phoneNamedNearby)
	sc.Given(`^an impostor advertises the SAME name with node id "([^"]*)"$`, s.impostorSameName)
	sc.Given(`^two phones both named "([^"]*)" advertise with different node ids$`, s.twoPhonesNamed)

	sc.When(`^the owner selects it and presses (i|a)$`, func(k string) error { return s.selectsAndPresses(s.onlyLabel(), k) })
	sc.When(`^a device with a different node id presents that invite's code$`, s.otherDevicePresents)
	sc.When(`^that device joins with the invite$`, s.deviceJoinsWithInvite)
	sc.When(`^"([^"]*)" asks to join$`, s.asksToJoin)
	sc.When(`^the owner presses (y|n)$`, s.pressesYN)
	sc.When(`^instead the owner presses n$`, s.insteadPressesN)

	sc.Then(`^under a muted "NEARBY" eyebrow one row reads the name "([^"]*)", the kind "([^"]*)", how long ago it was seen, and a short key fingerprint$`, s.nearbyRowReads)
	sc.Then(`^the row offers "([^"]*)"$`, s.rowOffers)
	sc.Then(`^nowhere on the screen is "([^"]*)"$`, s.nowhereOnScreen)
	sc.Then(`^the same NEARBY row is drawn under the members, with "([^"]*)"$`, s.sameRowUnderMembers)
	sc.Then(`^an invite is minted for node "([^"]*)" only \(kind edge, for=[^)]*\), with a one-time code$`, s.inviteMintedFor)
	sc.Then("^the screen shows the QR and the `roger join` line, and the row reads \"invited · waiting\"$", s.showsQRAndLineRowInvited)
	sc.Then(`^the authority's claim grant for "([^"]*)" exists, so the phone's own claim poll is answered$`, s.claimGranted)
	sc.Then(`^it is refused and the invite is not spent$`, s.refusedNotSpent)
	sc.Then(`^the row for "([^"]*)" still reads "invited · waiting"$`, s.rowStillInvited)
	sc.Then(`^it is no longer under NEARBY and is drawn as a member with its name$`, s.leftNearbyIsMember)
	sc.Then(`^the status line says it joined$`, s.statusSaysJoined)
	sc.Then(`^the screen says it is waiting for "([^"]*)" to ask, and names what the device runs: roger join <this authority's address>$`, s.waitingForAsk)
	sc.Then(`^the matching phrase appears on the authority's screen beside the row$`, s.phraseBesideRow)
	sc.Then(`^"([^"]*)" is admitted and drawn as a member$`, s.admittedMember)
	sc.Then(`^"([^"]*)" is refused and stays under NEARBY$`, s.refusedStaysNearby)
	sc.Then(`^the claim is granted and its code is shown, as i does, because a claim-only device joins by claim, not by asking$`, s.claimGrantedCodeShown)
	sc.Then(`^its row reads "last seen 20m ago" in the dim style$`, s.staleRow)
	sc.Then(`^i still works \(the invite waits for the device\), but the row says the device must come back to use it$`, s.staleInviteWorks)
	sc.Then(`^two rows show, told apart by their node ids and fingerprints, never merged$`, s.twoRowsById)
	sc.Then(`^an invite minted for "([^"]*)" cannot be redeemed by "([^"]*)"$`, s.inviteNotRedeemableBy)
	sc.Then(`^NEARBY shows two rows, each with its own id and fingerprint$`, s.twoRowsById)
}

var _ = tea.KeyMsg{} // the harness drives keys through emptySimpleBDD.key
