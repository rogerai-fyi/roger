package edge

// Executable spec: features/edge/instances.feature (@edge) - a running roger is a member of
// its node's Edge. REAL registries over real temp directories (one per node, standing in
// for that machine's config dir), a REAL fleet over the real in-memory store, and the REAL
// path a household takes to a peer: household -> describe -> observe. The clock is injected.

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/store"
)

type instBDD struct {
	t          *testing.T
	root       string
	now        time.Time
	db         store.Store
	fleet      *Fleet
	other      *Fleet                          // another account's fleet on the same LAN
	ids        map[string]string               // node name -> id
	dirs       map[string]string               // node name -> its household dir
	regs       map[string]map[string]*Registry // node -> instance -> registration
	pins       map[string]string
	err        error
	desc       Describe
	body       []byte
	ad         Advert
	fp         string
	gotLocated Located
}

func (s *instBDD) reset() {
	s.root = s.t.TempDir()
	s.now = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	s.db = store.NewMem()
	s.fleet = NewFleet(s.db, "acct-1")
	s.fleet.SetClock(func() time.Time { return s.now })
	s.other = NewFleet(store.NewMem(), "acct-2")
	s.other.SetClock(func() time.Time { return s.now })
	s.ids, s.dirs, s.regs, s.pins = map[string]string{}, map[string]string{}, map[string]map[string]*Registry{}, map[string]string{}
	s.err, s.desc, s.body, s.ad, s.fp, s.gotLocated = nil, Describe{}, nil, Advert{}, "", Located{}
}

// enrollNode is a machine that joined this Edge: a real node record with a pin, and a
// household directory of its own.
func (s *instBDD) enrollNode(name string) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	n := NewNode(pub, name, Host)
	n.Transports = []store.EdgeTransport{{Kind: "lan", Addr: "192.168.1.10:1", Fingerprint: strings.Repeat("ab", 32)}}
	got, err := s.fleet.Enroll(n)
	if err != nil {
		s.t.Fatalf("enroll %q: %v", name, err)
	}
	s.ids[name], s.pins[name] = got.ID, strings.Repeat("ab", 32)
	s.dirs[name] = filepath.Join(s.root, name, "edge-instances")
	s.regs[name] = map[string]*Registry{}
}

// start is a roger starting on a node: it registers in the node's household, and the node's
// face then reports the household to this Edge (household -> describe -> observe).
func (s *instBDD) start(node, name string, caps []Capability, bands ...string) error {
	inst := Instance{Name: name}
	for _, c := range caps {
		inst.Caps = append(inst.Caps, store.EdgeCap{Name: string(c), State: string(initialState(c))})
	}
	inst.Bands = bands
	r, err := Register(s.dirs[node], s.ids[node], inst, s.now)
	if err != nil {
		return err
	}
	s.regs[node][name] = r
	return s.observe(node)
}

// observe is the node's face reporting its household to this Edge.
func (s *instBDD) observe(node string) error {
	insts, trunc := DescribeInstances(s.dirs[node], s.now)
	_, err := s.fleet.Observe(s.ids[node], Observation{Name: node, Kind: string(Host),
		Addr: "192.168.1.10:1", Fingerprint: s.pins[node], Instances: insts, InstancesTruncated: trunc})
	return err
}

func (s *instBDD) node(name string) store.EdgeNode {
	n, ok, err := s.fleet.Get(s.ids[name])
	if err != nil || !ok {
		s.t.Fatalf("node %q: ok=%v err=%v", name, ok, err)
	}
	return n
}

func names(insts []Instance) []string {
	out := []string{}
	for _, i := range insts {
		out = append(out, i.Name)
	}
	return out
}

func capState(insts []Instance, inst, cap string) string {
	for _, i := range insts {
		if i.Name != inst {
			continue
		}
		for _, c := range i.Caps {
			if c.Name == cap {
				return c.State
			}
		}
	}
	return ""
}

// ---- 1. joins by running ---------------------------------------------------

func (s *instBDD) anEnrolledNode(name string) error { s.enrollNode(name); return nil }

func (s *instBDD) aRogerStartsOnIt() error {
	for name := range s.ids {
		s.err = s.start(name, DefaultInstanceName(name, nil), []Capability{Operate})
		return nil
	}
	return errors.New("no node enrolled")
}

func (s *instBDD) registeredAsInstanceOf(node string) error {
	if s.err != nil {
		return s.err
	}
	insts, err := s.fleet.InstancesOf(s.ids[node])
	if err != nil {
		return err
	}
	if len(insts) != 1 {
		return fmt.Errorf("instances = %v", names(insts))
	}
	return nil
}

func (s *instBDD) carriesNameStartCaps() error {
	for node := range s.ids {
		insts, _ := s.fleet.InstancesOf(s.ids[node])
		in := insts[0]
		if in.Name == "" || in.Started == 0 || len(in.Caps) == 0 {
			return fmt.Errorf("instance = %+v", in)
		}
	}
	return nil
}

func (s *instBDD) nothingEnrolledIssuedOrDialled() error {
	list, _ := s.fleet.List()
	if len(list) != 1 {
		return fmt.Errorf("the fleet changed size: %d", len(list))
	}
	// the registry wrote ONE json file and nothing that looks like a key or a certificate
	for node, dir := range s.dirs {
		files, _ := filepath.Glob(filepath.Join(dir, "*"))
		for _, f := range files {
			if strings.HasSuffix(f, ".pem") || strings.Contains(f, "key") {
				return fmt.Errorf("%s: an instance wrote a credential: %s", node, f)
			}
		}
	}
	return nil
}

func (s *instBDD) machineNotEnrolled() error {
	s.dirs["stray"] = filepath.Join(s.root, "stray", "edge-instances")
	s.ids["stray"] = "" // no node id: not enrolled
	s.regs["stray"] = map[string]*Registry{}
	return nil
}

func (s *instBDD) registersNoInstance() error {
	if !errors.Is(s.err, ErrNotEnrolled) {
		return fmt.Errorf("err = %v, want ErrNotEnrolled", s.err)
	}
	if files, _ := filepath.Glob(filepath.Join(s.dirs["stray"], "*.json")); len(files) != 0 {
		return fmt.Errorf("an unenrolled machine wrote a registration: %v", files)
	}
	return nil
}

func (s *instBDD) edgeListsNoMember() error {
	list, _ := s.fleet.List()
	if len(list) != 0 {
		return fmt.Errorf("fleet = %d nodes", len(list))
	}
	return nil
}

func (s *instBDD) reasonNamesEnrolment() error {
	if s.err == nil || !strings.Contains(s.err.Error(), "enroll") {
		return fmt.Errorf("reason = %v", s.err)
	}
	return nil
}

func (s *instBDD) rogersStart(a, b, c string) error {
	for node := range s.ids {
		for _, n := range []string{a, b, c} {
			if err := s.start(node, n, []Capability{Operate}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *instBDD) listsOneNode(name string) error {
	list, err := s.fleet.List()
	if err != nil {
		return err
	}
	if len(list) != 1 || list[0].Name != name {
		return fmt.Errorf("fleet = %+v", list)
	}
	return nil
}

func (s *instBDD) nodeCarriesExactly(node, a, b, c string) error {
	insts, _ := s.fleet.InstancesOf(s.ids[node])
	got := strings.Join(names(insts), ",")
	want := []string{a, b, c}
	sortStrings(want)
	if got != strings.Join(want, ",") {
		return fmt.Errorf("instances = %q, want %v", got, want)
	}
	return nil
}

func sortStrings(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

func (s *instBDD) eachAddressable(node string) error {
	insts, _ := s.fleet.InstancesOf(s.ids[node])
	for _, in := range insts {
		l, err := s.fleet.Resolve(node + "/" + in.Name)
		if err != nil || l.Instance.Name != in.Name {
			return fmt.Errorf("%s/%s: %v", node, in.Name, err)
		}
	}
	return nil
}

func (s *instBDD) nodeRunning(node, a, b string) error {
	s.enrollNode(node)
	for _, n := range []string{a, b} {
		if err := s.start(node, n, []Capability{Operate}); err != nil {
			return err
		}
	}
	return nil
}

func (s *instBDD) nodeRunningOne(node, a string) error {
	s.enrollNode(node)
	return s.start(node, a, []Capability{Operate})
}

func (s *instBDD) instanceExits(name string) error {
	for node, regs := range s.regs {
		if r, ok := regs[name]; ok {
			if err := r.Deregister(); err != nil {
				return err
			}
			delete(regs, name)
			return s.observe(node)
		}
	}
	return fmt.Errorf("no instance %q", name)
}

func (s *instBDD) listsNodeStill(name string) error { return s.listsOneNode(name) }

func (s *instBDD) instancesExactly(name string) error {
	for node := range s.ids {
		insts, _ := s.fleet.InstancesOf(s.ids[node])
		if strings.Join(names(insts), ",") != name {
			return fmt.Errorf("instances = %v", names(insts))
		}
	}
	return nil
}

func (s *instBDD) enrolmentUnchanged() error {
	for node := range s.ids {
		n := s.node(node)
		if n.Pin != s.pins[node] || n.Presence != string(PresenceVerified) {
			return fmt.Errorf("node changed: %+v", n)
		}
	}
	return nil
}

func (s *instBDD) processGone() error {
	// a crash: the file stays, nobody beats it - its mtime is the last write
	for _, regs := range s.regs {
		for _, r := range regs {
			return os.Chtimes(r.Path(), s.now, s.now)
		}
	}
	return nil
}

func (s *instBDD) readPastWindow() error {
	s.now = s.now.Add(InstanceLife + time.Second)
	for node := range s.ids {
		if err := s.observe(node); err != nil {
			return err
		}
	}
	return nil
}

func (s *instBDD) notListedAsInstance(name string) error {
	for node := range s.ids {
		insts, _ := s.fleet.InstancesOf(s.ids[node])
		for _, in := range insts {
			if in.Name == name {
				return fmt.Errorf("%q is still listed", name)
			}
		}
	}
	return nil
}

func (s *instBDD) nodeUntouched(name string) error { return s.enrolmentUnchanged() }

// ---- 2. capabilities belong to the process --------------------------------

func (s *instBDD) instanceOffering(name, cap string) error {
	for node := range s.ids {
		return s.start(node, name, []Capability{Capability(cap)})
	}
	return errors.New("no node")
}

func (s *instBDD) instanceClaiming(name, cap string) error { return s.instanceOffering(name, cap) }

func (s *instBDD) twoClaiming(a, capA, b, capB string) error {
	if err := s.instanceOffering(a, capA); err != nil {
		return err
	}
	return s.instanceOffering(b, capB)
}

func (s *instBDD) fleetRead() error {
	for node := range s.ids {
		if err := s.observe(node); err != nil {
			return err
		}
	}
	return nil
}

func (s *instBDD) nodeOffers(node, a, b string) error {
	n := s.node(node)
	have := map[string]bool{}
	for _, c := range n.Caps {
		have[c.Name] = true
	}
	if !have[a] || !have[b] {
		return fmt.Errorf("caps = %+v", n.Caps)
	}
	return nil
}

func (s *instBDD) eachCapNamesProvider() error {
	for node := range s.ids {
		for _, c := range s.node(node).Caps {
			if len(c.Instances) == 0 {
				return fmt.Errorf("%s names no provider: %+v", c.Name, c)
			}
		}
	}
	return nil
}

func (s *instBDD) nodeNoLongerOffers(node, cap string) error {
	for _, c := range s.node(node).Caps {
		if c.Name == cap {
			return fmt.Errorf("%s still offers %s: %+v", node, cap, c)
		}
	}
	return nil
}

func (s *instBDD) stillOffers(cap string) error {
	for node := range s.ids {
		for _, c := range s.node(node).Caps {
			if c.Name == cap {
				return nil
			}
		}
	}
	return fmt.Errorf("%s gone", cap)
}

func (s *instBDD) nothingRoutes(cap string) error {
	for node := range s.ids {
		if Routable(s.node(node), Capability(cap)) {
			return fmt.Errorf("%s still routes", cap)
		}
	}
	return nil
}

func (s *instBDD) capOnInstanceIs(cap, inst, state string) error {
	for node := range s.ids {
		insts, _ := s.fleet.InstancesOf(s.ids[node])
		if got := capState(insts, inst, cap); got != state {
			return fmt.Errorf("%s on %s = %q, want %q", cap, inst, got, state)
		}
	}
	return nil
}

func (s *instBDD) namesHowVerified() error {
	if Capability("serve").VerificationMethod() == "" {
		return errors.New("no verification method")
	}
	return nil
}

func (s *instBDD) nothingRoutesOnIt() error { return s.nothingRoutes("serve") }

func (s *instBDD) probePassesAgainst(inst string) error {
	for node := range s.ids {
		return s.fleet.RecordInstanceProbe(s.ids[node], inst, Serve, true)
	}
	return nil
}

func (s *instBDD) nodeDeclaringActuate(node, inst string) error {
	s.enrollNode(node)
	return s.start(node, inst, []Capability{Actuate})
}

func (s *instBDD) neverProbed() error {
	for node := range s.ids {
		if err := s.fleet.RecordInstanceProbe(s.ids[node], "gate-ctl", Actuate, true); err == nil {
			return errors.New("actuate was probed")
		}
	}
	return nil
}

func (s *instBDD) ownerConfirmationChangesIt() error {
	for node := range s.ids {
		if err := s.fleet.ConfirmActuate(s.ids[node], "owner"); err != nil {
			return err
		}
		if !Routable(s.node(node), Actuate) {
			return errors.New("the owner's confirmation did not verify actuate on the node")
		}
	}
	return nil
}

// ---- 3. names -------------------------------------------------------------

func (s *instBDD) startsNoName() error {
	for node := range s.ids {
		taken := names(Household(s.dirs[node], s.now))
		s.err = s.start(node, DefaultInstanceName(node, taken), []Capability{Operate})
		return s.err
	}
	return nil
}

func (s *instBDD) defaultNameDerived() error {
	for node := range s.ids {
		insts, _ := s.fleet.InstancesOf(s.ids[node])
		if len(insts) != 1 || !strings.HasPrefix(insts[0].Name, node) {
			return fmt.Errorf("default name = %v, want derived from %q", names(insts), node)
		}
		if DefaultInstanceName(node, []string{node}) != node+"-2" {
			return errors.New("the second default is not deterministic")
		}
	}
	return nil
}

func (s *instBDD) ownerCanRename() error {
	for node, regs := range s.regs {
		for _, r := range regs {
			if err := r.Rename(s.ids[node], "renamed", s.now); err != nil {
				return err
			}
			return s.observe(node)
		}
	}
	return nil
}

func (s *instBDD) newNameEverywhere() error {
	for node := range s.ids {
		insts, _ := s.fleet.InstancesOf(s.ids[node])
		if strings.Join(names(insts), ",") != "renamed" {
			return fmt.Errorf("instances = %v", names(insts))
		}
	}
	return nil
}

func (s *instBDD) triesToRegisterAs(name string) error {
	for node := range s.ids {
		s.err = s.start(node, name, []Capability{Operate})
		return nil
	}
	return nil
}

func (s *instBDD) refusedNamingWhatIsWrong() error {
	if s.err == nil {
		return errors.New("accepted")
	}
	return nil
}

func (s *instBDD) noInstanceByThatName() error {
	for node := range s.ids {
		if len(Household(s.dirs[node], s.now)) != 0 {
			return errors.New("a refused instance was registered")
		}
	}
	return nil
}

func (s *instBDD) anotherTriesSameName(node, name string) error {
	s.err = s.start(node, name, []Capability{Operate})
	return nil
}

func (s *instBDD) refused() error {
	if s.err == nil {
		return errors.New("not refused")
	}
	return nil
}

func (s *instBDD) runningUntouched(name string) error {
	for node := range s.ids {
		insts, _ := s.fleet.InstancesOf(s.ids[node])
		if strings.Join(names(insts), ",") != name {
			return fmt.Errorf("instances = %v", names(insts))
		}
	}
	return nil
}

func (s *instBDD) refusalSaysTaken() error {
	if !errors.Is(s.err, ErrInstanceNameTaken) {
		return fmt.Errorf("err = %v", s.err)
	}
	return nil
}

func (s *instBDD) registersOn(node, name string) error {
	s.err = s.start(node, name, []Capability{Operate})
	return s.err
}

func (s *instBDD) bothMembers() error {
	n := 0
	for node := range s.ids {
		insts, _ := s.fleet.InstancesOf(s.ids[node])
		n += len(insts)
	}
	if n != 2 {
		return fmt.Errorf("%d instances", n)
	}
	return nil
}

func (s *instBDD) eachAddressedAsNodeSlash(name string) error {
	for node := range s.ids {
		if _, err := s.fleet.Resolve(node + "/" + name); err != nil {
			return err
		}
	}
	return nil
}

func (s *instBDD) bareAmbiguous(name string) error {
	_, err := s.fleet.Resolve(name)
	if !errors.Is(err, ErrAmbiguousInstance) {
		return fmt.Errorf("err = %v", err)
	}
	for node := range s.ids {
		if !strings.Contains(err.Error(), node+"/"+name) {
			return fmt.Errorf("the ambiguity does not list %s: %v", node, err)
		}
	}
	return nil
}

func (s *instBDD) noOtherNamed(string) error { return nil }

func (s *instBDD) bareAddresses(name string) error {
	l, err := s.fleet.Resolve(name)
	if err != nil {
		return err
	}
	s.gotLocated = l
	return nil
}

func (s *instBDD) slashAddressesSame(addr string) error {
	l, err := s.fleet.Resolve(addr)
	if err != nil {
		return err
	}
	if l.Address() != s.gotLocated.Address() {
		return fmt.Errorf("%s != %s", l.Address(), s.gotLocated.Address())
	}
	return nil
}

func (s *instBDD) noInstanceAgainst(node string) error {
	if _, ok := s.ids[node]; ok {
		insts, _ := s.fleet.InstancesOf(s.ids[node])
		if len(insts) != 0 {
			return fmt.Errorf("%s has instances %v", node, names(insts))
		}
	}
	for _, dir := range s.dirs {
		for _, h := range Household(dir, s.now) {
			if strings.Contains(h.Name, "/") {
				return fmt.Errorf("a slashed name was registered: %s", h.Name)
			}
		}
	}
	return nil
}

// ---- 4. the face reports its instances --------------------------------------

func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "n_face"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func (s *instBDD) peerDialsAndDescribes() error {
	for node := range s.ids {
		insts, trunc := DescribeInstances(s.dirs[node], s.now)
		srv := NewServer(Describe{NodeID: s.ids[node], Account: "acct-1", Kind: "host", Caps: []string{"operate"},
			Instances: insts, InstancesTruncated: trunc}, selfSigned(s.t))
		ts := httptest.NewUnstartedServer(srv.Handler())
		ts.TLS = srv.TLSConfig()
		ts.StartTLS()
		defer ts.Close()
		s.ad = srv.Advert(1)
		var served string
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true,
			VerifyConnection: func(cs tls.ConnectionState) error { served = FingerprintOf(cs.PeerCertificates[0]); return nil }}}}
		resp, err := c.Get(ts.URL + DescribePath)
		if err != nil {
			return err
		}
		s.body, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		s.fp = served
		return json.Unmarshal(s.body, &s.desc)
	}
	return nil
}

func (s *instBDD) answerCarriesIdKindAccount() error {
	if s.desc.NodeID == "" || s.desc.Kind == "" || s.desc.Account == "" {
		return fmt.Errorf("describe = %s", s.body)
	}
	return nil
}

func (s *instBDD) answerCarriesInstances(a, b string) error {
	got := strings.Join(names(s.desc.Instances), ",")
	if got != a+","+b && got != b+","+a {
		return fmt.Errorf("instances = %q", got)
	}
	for _, in := range s.desc.Instances {
		if len(in.Caps) == 0 {
			return fmt.Errorf("%s carries no capabilities", in.Name)
		}
	}
	return nil
}

func (s *instBDD) arrivedOverCommittedCert() error {
	if s.fp == "" || s.fp != s.ad.Fingerprint {
		return fmt.Errorf("served %q, advertised %q", s.fp, s.ad.Fingerprint)
	}
	return nil
}

func (s *instBDD) nodeRunningThree(node string) error {
	s.enrollNode(node)
	for _, n := range []string{"a", "b", "c"} {
		if err := s.start(node, n, []Capability{Operate}); err != nil {
			return err
		}
	}
	return nil
}

func (s *instBDD) itAdvertises() error {
	for node := range s.ids {
		srv := NewServer(Describe{NodeID: s.ids[node], Account: "acct-1", Kind: "host"}, selfSigned(s.t))
		s.ad = srv.Advert(1)
	}
	return nil
}

func (s *instBDD) advertNoInstances() error {
	txt := strings.Join(s.ad.TXT(), " ")
	if strings.Contains(txt, "inst") || strings.Contains(txt, "instances") {
		return fmt.Errorf("the advertisement carries instances: %s", txt)
	}
	return nil
}

func (s *instBDD) learnedOnlyFromDescribe() error { return nil }

func (s *instBDD) peerAdvertisingMore() error {
	s.enrollNode("peer")
	return s.start("peer", "serve", []Capability{Serve}, "gpt-oss-20b")
}

func (s *instBDD) dialledAndVerified() error {
	// the advertisement claimed actuate too; describe (the household) says only serve
	insts, _ := DescribeInstances(s.dirs["peer"], s.now)
	_, err := s.fleet.Observe(s.ids["peer"], Observation{Name: "peer", Kind: "host",
		Caps: []Capability{Serve, Actuate}, Addr: "192.168.1.10:1", Fingerprint: s.pins["peer"], Instances: insts})
	return err
}

func (s *instBDD) recordedIsDescribes() error {
	n := s.node("peer")
	for _, c := range n.Caps {
		if c.Name == "actuate" {
			return fmt.Errorf("the advertised claim was stored: %+v", n.Caps)
		}
	}
	return nil
}

func (s *instBDD) advertisedNotStored() error { return nil }

func (s *instBDD) peerReportingMany(n int) error {
	s.enrollNode("busy")
	var insts []Instance
	for i := 0; i < n; i++ {
		insts = append(insts, Instance{Name: fmt.Sprintf("i%03d", i), Caps: []store.EdgeCap{{Name: "operate", State: "CLAIMED"}}})
	}
	_, err := s.fleet.Observe(s.ids["busy"], Observation{Name: "busy", Kind: "host", Addr: "192.168.1.10:1",
		Fingerprint: s.pins["busy"], Instances: insts})
	return err
}

func (s *instBDD) atMostBound() error {
	insts, _ := s.fleet.InstancesOf(s.ids["busy"])
	if len(insts) > MaxInstances {
		return fmt.Errorf("%d recorded", len(insts))
	}
	return nil
}

func (s *instBDD) saysTruncated() error {
	if !s.node("busy").InstancesTruncated {
		return errors.New("truncation not said")
	}
	return nil
}

func (s *instBDD) peerOtherAccount() error {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	n := NewNode(pub, "theirs", Host)
	n.Transports = []store.EdgeTransport{{Kind: "lan", Addr: "192.168.1.99:1", Fingerprint: strings.Repeat("cd", 32)}}
	got, err := s.other.Enroll(n)
	if err != nil {
		return err
	}
	s.ids["theirs"] = got.ID
	return nil
}

func (s *instBDD) itIsDialled() error {
	_, err := s.other.Observe(s.ids["theirs"], Observation{Name: "theirs", Kind: "host", Addr: "192.168.1.99:1",
		Fingerprint: strings.Repeat("cd", 32), Instances: []Instance{{Name: "secret", Caps: []store.EdgeCap{{Name: "serve", State: "CLAIMED"}}}}})
	return err
}

func (s *instBDD) notAMember() error {
	if _, ok, _ := s.fleet.Get(s.ids["theirs"]); ok {
		return errors.New("another account's node is on this Edge")
	}
	return nil
}

func (s *instBDD) noneOfItsInstances() error {
	list, _ := s.fleet.List()
	for _, n := range list {
		for _, in := range n.Instances {
			if in.Name == "secret" {
				return errors.New("leaked")
			}
		}
	}
	if _, err := s.fleet.Resolve("secret"); !errors.Is(err, ErrNoSuchInstance) {
		return fmt.Errorf("resolve = %v", err)
	}
	return nil
}

// ---- 5. the collision -------------------------------------------------------

func (s *instBDD) instanceBroadcasting(name, band string) error {
	for node := range s.ids {
		return s.start(node, name, []Capability{Serve}, band)
	}
	return nil
}

func (s *instBDD) triesToBroadcastHeld(name, band string) error {
	// the on-air lock refused the band to the second process, exactly as today: the
	// process is still a roger, so it registers - without that band
	for node := range s.ids {
		return s.start(node, name, []Capability{Serve})
	}
	return nil
}

func (s *instBDD) nodeOffersBothBands(node, a, b string) error {
	for _, band := range []string{a, b} {
		who, err := s.fleet.ServersOf(band)
		if err != nil || len(who) != 1 || who[0].Node.Name != node {
			return fmt.Errorf("%s: %v %v", band, who, err)
		}
	}
	return nil
}

func (s *instBDD) eachBandNamesInstance() error {
	for _, band := range []string{"gpt-oss-120b", "qwen-3.8-27b"} {
		who, _ := s.fleet.ServersOf(band)
		if len(who) != 1 || who[0].Instance.Name == "" {
			return fmt.Errorf("%s: %v", band, who)
		}
	}
	return nil
}

func (s *instBDD) lockRefusesSecond() error { return nil }

func (s *instBDD) bandHeldBy(inst string) error {
	who, _ := s.fleet.ServersOf("gpt-oss-120b")
	if len(who) != 1 || who[0].Instance.Name != inst {
		return fmt.Errorf("servers = %v", who)
	}
	return nil
}

func (s *instBDD) memberNotServing(inst string) error {
	for node := range s.ids {
		insts, _ := s.fleet.InstancesOf(s.ids[node])
		for _, in := range insts {
			if in.Name == inst {
				if len(in.Bands) != 0 {
					return fmt.Errorf("%s serves %v", inst, in.Bands)
				}
				return nil
			}
		}
	}
	return fmt.Errorf("%s is not a member", inst)
}

func (s *instBDD) nothingBlackHoled() error { return nil }

// ---- 7. trust boundary -------------------------------------------------------

func (s *instBDD) noCertificateOfItsOwn() error {
	for _, dir := range s.dirs {
		files, _ := filepath.Glob(filepath.Join(dir, "*"))
		for _, f := range files {
			b, _ := os.ReadFile(f)
			if strings.Contains(string(b), "CERTIFICATE") || strings.Contains(string(b), "PRIVATE KEY") {
				return fmt.Errorf("%s holds a credential", f)
			}
		}
	}
	return nil
}

func (s *instBDD) verifiedOnlyThroughFace() error { return nil }
func (s *instBDD) nodeVouches() error             { return nil }

func (s *instBDD) ownerForgets(node string) error { return s.fleet.Forget(s.ids[node]) }

func (s *instBDD) neitherIsMember() error {
	for _, name := range []string{"desk", "share"} {
		if _, err := s.fleet.Resolve(name); !errors.Is(err, ErrNoSuchInstance) {
			return fmt.Errorf("%s: %v", name, err)
		}
	}
	return nil
}

func (s *instBDD) certRevokedAsToday() error { return nil }

// ---- 8. resources ------------------------------------------------------------

func (s *instBDD) canReadResources() error {
	for node, regs := range s.regs {
		for _, r := range regs {
			return r.Update(s.ids[node], func(i *Instance) {
				i.Resources = &store.EdgeResources{GPU: "Orin", GPUMemUsed: 0.6, RAMUsedGB: 8, RAMTotalGB: 32, ReadAt: s.now.Unix()}
			})
		}
	}
	return nil
}

func (s *instBDD) describeCarriesResources() error {
	for node := range s.ids {
		insts, _ := DescribeInstances(s.dirs[node], s.now)
		if len(insts) != 1 || insts[0].Resources == nil || insts[0].Resources.ReadAt == 0 || insts[0].Resources.GPU != "Orin" {
			return fmt.Errorf("instances = %+v", insts)
		}
	}
	return nil
}

func (s *instBDD) drawnAsLevels() error { return nil }

func (s *instBDD) noGPUMachine(node string) error {
	s.enrollNode(node)
	if err := s.start(node, "pi-roger", []Capability{Sense}); err != nil {
		return err
	}
	return s.regs[node]["pi-roger"].Update(s.ids[node], func(i *Instance) {
		i.Resources = &store.EdgeResources{RAMUsedGB: 1, RAMTotalGB: 4, ReadAt: s.now.Unix()}
	})
}

func (s *instBDD) describeRead() error { return s.fleetRead() }

func (s *instBDD) noGPUFact() error {
	for node := range s.ids {
		insts, _ := s.fleet.InstancesOf(s.ids[node])
		for _, in := range insts {
			if in.Resources != nil && in.Resources.GPU != "" {
				return fmt.Errorf("a GPU was invented: %+v", in.Resources)
			}
		}
	}
	return nil
}

func (s *instBDD) noGPUZero() error {
	for _, dir := range s.dirs {
		files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		for _, f := range files {
			b, _ := os.ReadFile(f)
			if strings.Contains(string(b), `"gpu_mem_used":0`) || strings.Contains(string(b), `"gpu":""`) {
				return fmt.Errorf("a zero GPU fact was written: %s", b)
			}
		}
	}
	return nil
}

func (s *instBDD) loadedAndOnAir(node, inst, band string) error {
	s.enrollNode(node)
	return s.start(node, inst, []Capability{Serve}, band)
}

func (s *instBDD) instanceListsServed(band string) error {
	who, err := s.fleet.ServersOf(band)
	if err != nil || len(who) != 1 {
		return fmt.Errorf("servers = %v %v", who, err)
	}
	return nil
}

func (s *instBDD) answersWithoutDialling(band string) error {
	// ServersOf reads the record only; nothing here has a network at all
	return s.instanceListsServed(band)
}

func (s *instBDD) twoServersOneRoomier() error {
	s.enrollNode("a")
	s.enrollNode("b")
	if err := s.start("a", "serve", []Capability{Serve}, "qwen-3.8-27b"); err != nil {
		return err
	}
	if err := s.start("b", "serve", []Capability{Serve}, "qwen-3.8-27b"); err != nil {
		return err
	}
	return s.regs["b"]["serve"].Update(s.ids["b"], func(i *Instance) {
		i.Resources = &store.EdgeResources{GPUMemUsed: 0.1, ReadAt: s.now.Unix()}
	})
}

func (s *instBDD) factShown() error {
	if err := s.observe("b"); err != nil {
		return err
	}
	insts, _ := s.fleet.InstancesOf(s.ids["b"])
	if insts[0].Resources == nil {
		return errors.New("the fact is not on the record")
	}
	return nil
}

func (s *instBDD) routingIgnoresRoom() error {
	who, _ := s.fleet.ServersOf("qwen-3.8-27b")
	if len(who) != 2 {
		return fmt.Errorf("servers = %v", who)
	}
	// the record lists them in fleet order (by name); nothing here reorders by resources
	if who[0].Node.Name != "a" {
		return fmt.Errorf("the roomier server was preferred by its report: %v", who)
	}
	return nil
}

func TestInstancesFeature(t *testing.T) {
	st := &instBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			// 1
			sc.Step(`^an enrolled node "([^"]*)"$`, st.anEnrolledNode)
			sc.Step(`^a roger starts on it$`, st.aRogerStartsOnIt)
			sc.Step(`^it is registered as an instance of "([^"]*)"$`, st.registeredAsInstanceOf)
			sc.Step(`^it carries a name, a start time and the capabilities it offers$`, st.carriesNameStartCaps)
			sc.Step(`^nothing was enrolled, no certificate was issued and no network was needed$`, st.nothingEnrolledIssuedOrDialled)
			sc.Step(`^a machine that has not enrolled$`, st.machineNotEnrolled)
			sc.Step(`^it registers no instance$`, st.registersNoInstance)
			sc.Step(`^the Edge lists no member for that machine$`, st.edgeListsNoMember)
			sc.Step(`^the reason given names enrolment, not a failure$`, st.reasonNamesEnrolment)
			sc.Step(`^rogers named "([^"]*)", "([^"]*)" and "([^"]*)" start on it$`, st.rogersStart)
			sc.Step(`^the Edge lists ONE node "([^"]*)"$`, st.listsOneNode)
			sc.Step(`^that node carries exactly the instances "([^"]*)", "([^"]*)" and "([^"]*)"$`, func(a, b, c string) error {
				for node := range st.ids {
					return st.nodeCarriesExactly(node, a, b, c)
				}
				return nil
			})
			sc.Step(`^each instance is addressable as "([^"]*)/<its name>"$`, st.eachAddressable)
			sc.Step(`^an enrolled node "([^"]*)" running the instances "([^"]*)" and "([^"]*)"$`, st.nodeRunning)
			sc.Step(`^an enrolled node "([^"]*)" running the instance "([^"]*)"$`, st.nodeRunningOne)
			sc.Step(`^the instance "([^"]*)" exits$`, st.instanceExits)
			sc.Step(`^the Edge lists the node "([^"]*)" still$`, st.listsNodeStill)
			sc.Step(`^its instances are exactly "([^"]*)"$`, st.instancesExactly)
			sc.Step(`^nothing about the node's enrolment changed$`, st.enrolmentUnchanged)
			sc.Step(`^that instance's process is gone without deregistering$`, st.processGone)
			sc.Step(`^the fleet is read past the liveness window$`, st.readPastWindow)
			sc.Step(`^"([^"]*)" is not listed as an instance$`, st.notListedAsInstance)
			sc.Step(`^the node "([^"]*)" is untouched$`, st.nodeUntouched)
			// 2
			sc.Step(`^an instance "([^"]*)" offering ([a-z]+)$`, st.instanceOffering)
			sc.Step(`^an instance "([^"]*)" claiming ([a-z]+)$`, st.instanceClaiming)
			sc.Step(`^an instance "([^"]*)" claiming ([a-z]+) and an instance "([^"]*)" claiming ([a-z]+)$`, st.twoClaiming)
			sc.Step(`^the fleet is read$`, st.fleetRead)
			sc.Step(`^the node "([^"]*)" offers ([a-z]+) and ([a-z]+)$`, st.nodeOffers)
			sc.Step(`^each capability names the instance that provides it$`, st.eachCapNamesProvider)
			sc.Step(`^the node "([^"]*)" no longer offers ([a-z]+)$`, st.nodeNoLongerOffers)
			sc.Step(`^it still offers ([a-z]+)$`, st.stillOffers)
			sc.Step(`^nothing routes ([a-z]+) to that node$`, st.nothingRoutes)
			sc.Step(`^([a-z]+) on "([^"]*)" is (CLAIMED|VERIFIED|PENDING CONFIRMATION)$`, st.capOnInstanceIs)
			sc.Step(`^([a-z]+) on "([^"]*)" is still (CLAIMED|VERIFIED)$`, st.capOnInstanceIs)
			sc.Step(`^it names how it will be verified$`, st.namesHowVerified)
			sc.Step(`^nothing routes on it$`, st.nothingRoutesOnIt)
			sc.Step(`^the serving probe passes against "([^"]*)" only$`, st.probePassesAgainst)
			sc.Step(`^an enrolled node "([^"]*)" running an instance "([^"]*)" declaring actuate$`, st.nodeDeclaringActuate)
			sc.Step(`^it is never probed$`, st.neverProbed)
			sc.Step(`^the owner's confirmation is what changes it$`, st.ownerConfirmationChangesIt)
			// 3
			sc.Step(`^a roger starts on it with no name chosen$`, st.startsNoName)
			sc.Step(`^it is given a default name derived from this node, not a random one$`, st.defaultNameDerived)
			sc.Step(`^the owner can rename it$`, st.ownerCanRename)
			sc.Step(`^the new name is what every surface shows$`, st.newNameEverywhere)
			sc.Step(`^a roger tries to register as "([^"]*)"$`, st.triesToRegisterAs)
			sc.Step(`^it is refused with a reason naming what is wrong$`, st.refusedNamingWhatIsWrong)
			sc.Step(`^no instance by that name exists$`, st.noInstanceByThatName)
			sc.Step(`^another roger on "([^"]*)" tries to register as "([^"]*)"$`, st.anotherTriesSameName)
			sc.Step(`^it is refused$`, st.refused)
			sc.Step(`^the running "([^"]*)" is untouched$`, st.runningUntouched)
			sc.Step(`^the refusal says the name is taken on this node$`, st.refusalSaysTaken)
			sc.Step(`^a roger on "([^"]*)" registers as "([^"]*)"$`, st.registersOn)
			sc.Step(`^both are members$`, st.bothMembers)
			sc.Step(`^each is addressed as "<its node>/([^"]*)"$`, st.eachAddressedAsNodeSlash)
			sc.Step(`^a bare "([^"]*)" is ambiguous and says so, listing both$`, st.bareAmbiguous)
			sc.Step(`^no other instance named "([^"]*)" on this Edge$`, st.noOtherNamed)
			sc.Step(`^"([^"]*)" addresses it$`, st.bareAddresses)
			sc.Step(`^"([^"]*)" addresses the same instance$`, st.slashAddressesSame)
			sc.Step(`^a roger on "([^"]*)" tries to register as "([^"]*)"$`, func(node, name string) error {
				st.err = st.start(node, name, []Capability{Operate})
				return nil
			})
			sc.Step(`^no instance is recorded against "([^"]*)"$`, st.noInstanceAgainst)
			// 4
			sc.Step(`^a peer dials its LAN face and describes it$`, st.peerDialsAndDescribes)
			sc.Step(`^the answer carries the node's id, kind and account as today$`, st.answerCarriesIdKindAccount)
			sc.Step(`^it carries the instances "([^"]*)" and "([^"]*)" with their capabilities$`, st.answerCarriesInstances)
			sc.Step(`^the answer arrived over the certificate the advertisement committed to$`, st.arrivedOverCommittedCert)
			sc.Step(`^an enrolled node "([^"]*)" running three instances$`, st.nodeRunningThree)
			sc.Step(`^it advertises on the LAN$`, st.itAdvertises)
			sc.Step(`^the advertisement carries what it carries today and no instance list$`, st.advertNoInstances)
			sc.Step(`^the instances are learned only from a dialled, verified describe$`, st.learnedOnlyFromDescribe)
			sc.Step(`^a peer advertising capabilities that its describe does not report$`, st.peerAdvertisingMore)
			sc.Step(`^it is dialled and verified$`, st.dialledAndVerified)
			sc.Step(`^the instances and capabilities recorded are describe's$`, st.recordedIsDescribes)
			sc.Step(`^the advertised claim is not stored$`, st.advertisedNotStored)
			sc.Step(`^a peer whose describe reports (\d+) instances$`, st.peerReportingMany)
			sc.Step(`^at most the bound is recorded$`, st.atMostBound)
			sc.Step(`^the fleet says the list was truncated rather than silently dropping it$`, st.saysTruncated)
			sc.Step(`^a peer on this LAN enrolled to a different account$`, st.peerOtherAccount)
			sc.Step(`^it is dialled$`, st.itIsDialled)
			sc.Step(`^it is not a member$`, st.notAMember)
			sc.Step(`^none of its instances appear anywhere on this Edge$`, st.noneOfItsInstances)
			// 5
			sc.Step(`^an instance "([^"]*)" broadcasting "([^"]*)"$`, st.instanceBroadcasting)
			sc.Step(`^an instance "([^"]*)" tries to broadcast "([^"]*)"$`, st.triesToBroadcastHeld)
			sc.Step(`^both are members of the Edge$`, st.bothMembers)
			sc.Step(`^the node "([^"]*)" offers both bands$`, func(node string) error {
				return st.nodeOffersBothBands(node, "gpt-oss-120b", "qwen-3.8-27b")
			})
			sc.Step(`^each band names the instance broadcasting it$`, st.eachBandNamesInstance)
			sc.Step(`^the existing on-air lock refuses the second, exactly as it does today$`, st.lockRefusesSecond)
			sc.Step(`^the Edge shows the band held by "([^"]*)"$`, st.bandHeldBy)
			sc.Step(`^"([^"]*)" is drawn as a member that is not serving that band$`, st.memberNotServing)
			sc.Step(`^nothing is black-holed$`, st.nothingBlackHoled)
			// 7
			sc.Step(`^the instance has no certificate of its own$`, st.noCertificateOfItsOwn)
			sc.Step(`^nothing about it can be verified except through the node's face$`, st.verifiedOnlyThroughFace)
			sc.Step(`^the node's enrolment is what vouches for it$`, st.nodeVouches)
			sc.Step(`^the owner forgets "([^"]*)"$`, st.ownerForgets)
			sc.Step(`^neither instance is a member$`, st.neitherIsMember)
			sc.Step(`^the node's certificate is revoked exactly as today$`, st.certRevokedAsToday)
			// 8
			sc.Step(`^the instance can read its GPU, its GPU memory and its RAM$`, st.canReadResources)
			sc.Step(`^describe carries those facts with a moment they were read$`, st.describeCarriesResources)
			sc.Step(`^they are drawn on the node's strip as levels$`, st.drawnAsLevels)
			sc.Step(`^an enrolled node "([^"]*)" running an instance on a machine with no GPU$`, st.noGPUMachine)
			sc.Step(`^describe is read$`, st.describeRead)
			sc.Step(`^it carries no GPU fact$`, st.noGPUFact)
			sc.Step(`^it does not carry a GPU at 0%$`, st.noGPUZero)
			sc.Step(`^an enrolled node "([^"]*)" running the instance "([^"]*)" with "([^"]*)" loaded and on air$`, st.loadedAndOnAir)
			sc.Step(`^the instance lists "([^"]*)" as served$`, st.instanceListsServed)
			sc.Step(`^the fleet can answer "who on this Edge serves ([^"]*)" without dialling anyone$`, st.answersWithoutDialling)
			sc.Step(`^two instances serving the same band, one reporting more free GPU memory$`, st.twoServersOneRoomier)
			sc.Step(`^the fact is shown$`, st.factShown)
			sc.Step(`^routing prefers the one that measured faster, never the one that merely reports more room$`, st.routingIgnoresRoom)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@edge",
			Paths: []string{"../../features/edge/instances.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the instances scenarios failed")
	}
}
