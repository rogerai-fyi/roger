package main

// Executable spec: features/edge/empty_edge_simple.feature, the CLI scenarios - `roger edge` with
// nothing set up says the same three things the TUI and the console show; and §7 (@cli), the
// nearby devices: `roger edge nearby`, `roger edge invite --for`, `roger edge adopt` as the old name
// for approve. Reuses the CLI suite's fixture (an isolated config dir, the real dispatch, captured
// stdout) and a REAL local authority on disk, so "refused for another device" is the real check.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/edgeauth"
	"rogerai.fm/roger/v7/internal/joininvite"
	"rogerai.fm/roger/v7/internal/look"
	"rogerai.fm/roger/v7/internal/store"
)

// nearbyCLI is §7's CLI world: the devices pinging (a spec label is mapped onto a real node id
// derived from a real node key, as the TUI suite does), written where `roger edge` reads them.
type nearbyCLI struct {
	cli  *edgeCLIBDD
	t    *testing.T
	keys map[string]ed25519.PublicKey
	ids  map[string]string
	// adoptOut is what `roger edge adopt` printed, kept past the approve run that is compared to it.
	adoptOut string
}

func (n *nearbyCLI) reset(t *testing.T, cli *edgeCLIBDD) {
	n.cli, n.t, n.keys, n.ids, n.adoptOut = cli, t, map[string]ed25519.PublicKey{}, map[string]string{}, ""
}

func (n *nearbyCLI) id(label string) string {
	if id, ok := n.ids[label]; ok {
		return id
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		n.t.Fatal(err)
	}
	n.keys[label], n.ids[label] = pub, edgeauth.NodeID(pub)
	return n.ids[label]
}

// sees puts a phone (no certificate) on this network's candidate list, as a scan pass does.
func (n *nearbyCLI) sees(label, name string) error {
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	st.candidates = append(st.candidates, store.EdgeNode{ID: n.id(label), Name: name, Kind: "mobile",
		Presence: string(edge.PresenceCandidate), LastSeen: time.Now().Add(-12 * time.Second).Unix()})
	return st.save()
}

// roots makes this machine the Edge's authority, answering on a known port. The §7 CLI scenarios act
// as the owner of an Edge (an invite and an approve need one), over the file's Background, which is
// written for the empty-state scenarios.
func (n *nearbyCLI) roots() (*edgeauth.Local, error) {
	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil || ok {
		return local, err
	}
	if local, err = edgeauth.Designate(edgeAuthDir(), "shed"); err != nil {
		return nil, err
	}
	edgeSaveAuthorityPort(8791)
	lan := edgeLANIPs
	n.t.Cleanup(func() { edgeLANIPs = lan })
	edgeLANIPs = func() []string { return []string{"192.168.1.5"} }
	return local, nil
}

// runAs runs a command line whose node id is a spec label.
func (n *nearbyCLI) runAs(line, label string) error {
	return n.cli.run(strings.ReplaceAll(line, label, n.id(label)))
}

// present is a device with key nodePub redeeming the invite in a printed `roger join` line,
// through the authority's real issuing code.
func (n *nearbyCLI) present(line string, nodePub ed25519.PublicKey) error {
	local, _, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil {
		return err
	}
	in, err := joininvite.Parse(joininvite.FromLine(line))
	if err != nil {
		return err
	}
	_, user, _ := ed25519.GenerateKey(rand.Reader)
	req := edgeauth.NewRequest(edgeauth.LocalAccount, "device", "mobile", nodePub, time.Now())
	req.Sign(user)
	req.JoinProof = edgeauth.JoinProof(in.Code, in.FP, req)
	_, err = local.Issue(req)
	return err
}

func (n *nearbyCLI) joinLine() (string, error) {
	var lines []string
	for _, ln := range strings.Split(n.cli.out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "roger join ") {
			lines = append(lines, strings.TrimSpace(ln))
		}
	}
	if len(lines) != 1 {
		return "", fmt.Errorf("want one `roger join` line, got %d:\n%s", len(lines), n.cli.out)
	}
	return lines[0], nil
}

func (n *nearbyCLI) listsRow() error {
	id := n.ids["8eda6e1a92c9"]
	for _, w := range []string{look.NearbyCLIHead, "gentle-ibex-14", "mobile", "seen 12s ago",
		"key " + look.NearbyShort(strings.TrimPrefix(id, "n_")), id} {
		if !strings.Contains(n.cli.out, w) {
			return fmt.Errorf("%q missing:\n%s", w, n.cli.out)
		}
	}
	return nil
}

func (n *nearbyCLI) printsBothCommands(invite, approve string) error {
	for _, w := range []string{invite, approve} {
		w = strings.ReplaceAll(w, "8eda6e1a92c9", n.ids["8eda6e1a92c9"])
		if !strings.Contains(n.cli.out, w) {
			return fmt.Errorf("%q missing:\n%s", w, n.cli.out)
		}
	}
	if strings.Contains(n.cli.out, "roger edge adopt") {
		return fmt.Errorf("nearby still names adopt:\n%s", n.cli.out)
	}
	return nil
}

func (n *nearbyCLI) invitesFor(label string) error {
	if _, err := n.roots(); err != nil {
		return err
	}
	if _, ok := n.ids[label]; !ok {
		if err := n.sees(label, "gentle-ibex-14"); err != nil {
			return err
		}
	}
	return n.runAs("roger edge invite --for "+label, label)
}

func (n *nearbyCLI) lineForThatNodeWithQR() error {
	if n.cli.err != nil {
		return n.cli.err
	}
	line, err := n.joinLine()
	if err != nil {
		return err
	}
	in, err := joininvite.Parse(joininvite.FromLine(line))
	if err != nil {
		return err
	}
	if in.Kind != joininvite.Edge || in.For != n.ids["8eda6e1a92c9"] || in.Code == "" {
		return fmt.Errorf("the invite is %+v, want kind edge, for=%s and a code", in, n.ids["8eda6e1a92c9"])
	}
	if !strings.ContainsAny(n.cli.out, "▀▄█") {
		return fmt.Errorf("no QR:\n%s", n.cli.out)
	}
	return nil
}

func (n *nearbyCLI) otherRefusedWithIt() error {
	line, err := n.joinLine()
	if err != nil {
		return err
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := n.present(line, other); !errors.Is(err, edgeauth.ErrInviteForOther) {
		return fmt.Errorf("another device presenting it got %v, want %v", err, edgeauth.ErrInviteForOther)
	}
	// Refused, and not spent: the device it is for still joins with it.
	if err := n.present(line, n.keys["8eda6e1a92c9"]); err != nil {
		return fmt.Errorf("the device it was minted for was refused after the other's try: %w", err)
	}
	return nil
}

func (n *nearbyCLI) adopts(label string) error {
	if _, err := n.roots(); err != nil {
		return err
	}
	err := n.runAs("roger edge adopt "+label, label)
	n.adoptOut = n.cli.out
	return err
}

// doesWhatApproveDoes: the same grant, and the same words, as approve gives a twin phone.
func (n *nearbyCLI) doesWhatApproveDoes(label string) error {
	if n.cli.err != nil {
		return n.cli.err
	}
	local, _, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil {
		return err
	}
	if !local.ClaimGranted(n.ids[label]) {
		return fmt.Errorf("adopt did not grant the phone's claim:\n%s", n.adoptOut)
	}
	adopted := strings.SplitN(n.adoptOut, "\n", 2)[1]
	if err := n.sees("twin", "quiet-otter-3"); err != nil {
		return err
	}
	if err := n.runAs("roger edge approve twin", "twin"); err != nil {
		return err
	}
	if !local.ClaimGranted(n.ids["twin"]) {
		return fmt.Errorf("approve did not grant the claim:\n%s", n.cli.out)
	}
	shape := func(s, name, code string) string {
		return strings.ReplaceAll(strings.ReplaceAll(s, name, "<name>"), code, "<code>")
	}
	codeA, _, _ := edgeAdoptCode(n.ids[label])
	codeB, _, _ := edgeAdoptCode(n.ids["twin"])
	if a, b := shape(adopted, "gentle-ibex-14", codeA), shape(n.cli.out, "quiet-otter-3", codeB); a != b {
		return fmt.Errorf("adopt and approve differ:\nadopt:   %q\napprove: %q", a, b)
	}
	if strings.Contains(n.cli.out, look.AdoptDeprecated) {
		return fmt.Errorf("approve printed the deprecation line:\n%s", n.cli.out)
	}
	return nil
}

func (n *nearbyCLI) printsDeprecationOnce(want string) error {
	if want != look.AdoptDeprecated {
		return fmt.Errorf("the spec's line %q is not look.AdoptDeprecated %q", want, look.AdoptDeprecated)
	}
	if strings.Count(n.adoptOut, want) != 1 || !strings.HasPrefix(n.adoptOut, want+"\n") {
		return fmt.Errorf("adopt should lead with the one deprecation line, got:\n%s", n.adoptOut)
	}
	return nil
}

func (s *edgeCLIBDD) printsSimpleEmpty() error {
	for _, w := range []string{look.EmptyEdgeCLIHead, look.EmptyEdgeSay, look.EmptyEdgeCLIStart, look.EmptyEdgeCLIJoin, look.EmptyEdgeStart} {
		if !strings.Contains(s.out, w) {
			return fmt.Errorf("%q missing:\n%s", w, s.out)
		}
	}
	for _, w := range []string{"not enrolled", "roger edge enroll", "roger login", "Core ·", "the only node"} {
		if strings.Contains(s.out, w) {
			return fmt.Errorf("the empty Edge still says %q:\n%s", w, s.out)
		}
	}
	return nil
}

func TestEmptyEdgeSimpleFeatureCLI(t *testing.T) {
	st := &edgeCLIBDD{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(t); return c, nil })
			sc.Given(`^this machine is in no Edge \(not enrolled and not an authority\)$`, func() error { return nil })
			sc.When("^the owner runs `roger edge` on a terminal$", func() error { return st.run("roger edge") })
			sc.Then("^it prints \"your Edge: not set up yet\", the one-sentence explanation, and two next steps: `roger edge setup` \\(start an Edge here\\) and `roger join <invite or address>`$", st.printsSimpleEmpty)

			// §7 @cli
			nb := &nearbyCLI{}
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { nb.reset(t, st); return c, nil })
			sc.Given(`^a phone advertises itself with node id "([^"]*)", name "([^"]*)"$`, nb.sees)
			sc.Given(`^a phone advertises itself with node id "([^"]*)"$`, func(label string) error { return nb.sees(label, "gentle-ibex-14") })
			sc.When(`^the owner runs "roger edge nearby"$`, func() error { return st.run("roger edge nearby") })
			sc.When(`^the owner runs "roger edge invite --for ([^"]*)"$`, nb.invitesFor)
			sc.When(`^the owner runs "roger edge adopt ([^"]*)"$`, nb.adopts)
			sc.Then(`^it prints the name, kind, how long ago, the short fingerprint and the node id$`, nb.listsRow)
			sc.Then(`^it prints "([^"]*)" and "([^"]*)"$`, nb.printsBothCommands)
			sc.Then("^it prints one `roger join` line whose invite is for that node only, and the QR$", nb.lineForThatNodeWithQR)
			sc.Then(`^a device with another node id is refused with it$`, nb.otherRefusedWithIt)
			sc.Then("^it does what `roger edge approve ([^`]*)` does$", nb.doesWhatApproveDoes)
			sc.Then(`^it prints one line: "([^"]*)"$`, nb.printsDeprecationOnce)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "~@tui && ~@console",
			Paths: []string{"../../features/edge/empty_edge_simple.feature"},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("the CLI's simple empty Edge scenario failed")
	}
}
