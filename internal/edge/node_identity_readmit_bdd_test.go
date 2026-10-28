package edge_test

// Executable spec: features/edge/node_identity.feature, "RE-ADMITTING A FORGOTTEN DEVICE" (approved
// 2026-10-02). The record a forget leaves, the name a re-admit resolves and comes back under, the
// question it asks and the listing it gives. REAL fleet over the real store.Mem, no mocks. The CLI's
// own wiring of the same rules is pinned in cmd/rogerai (edgereadmit_test.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
)

type readmitState struct {
	now      time.Time
	db       store.Store
	fleet    *edge.Fleet
	node     store.EdgeNode
	departed []edge.Departed
	typed    string
	found    edge.Departed
	ok       bool
	question string
	listing  []edge.Departed
}

func (s *readmitState) reset() {
	s.now = time.Unix(1_790_000_000, 0)
	s.db = store.NewMem()
	s.fleet = edge.NewFleet(s.db, "acct-1")
	s.fleet.SetClock(func() time.Time { return s.now })
	s.node, s.departed, s.typed, s.found, s.ok, s.question, s.listing = store.EdgeNode{}, nil, "", edge.Departed{}, false, "", nil
}

// aPi4 is a member the way a real one looks after a while on an Edge: a pin, tags, a history, and
// a reported Pi 4 board.
func (s *readmitState) aPi4(id, name string) (store.EdgeNode, error) {
	n := store.EdgeNode{ID: id, Name: name, Kind: string(edge.Host), Pin: strings.Repeat("cd", 32),
		Tags: []string{"loft"}, Instances: []store.EdgeInstance{{Name: "roger",
			Software: &store.EdgeSoftware{Machine: "Raspberry Pi 4 Model B Rev 1.4"}}}}
	return s.fleet.Enroll(n)
}

// forget is what every forget path does: keep the short record, then take the device off.
func (s *readmitState) forget(n store.EdgeNode) error {
	s.departed = edge.RecordDeparted(s.departed, edge.DepartedOf(n, s.now))
	return s.fleet.Forget(n.ID)
}

func (s *readmitState) ownerForgetsADevice() error {
	n, err := s.aPi4("n_attic0000000000000000000000000000000000", "attic")
	if err != nil {
		return err
	}
	if err := s.fleet.RecordEvent(n.ID, "model", "loaded"); err != nil {
		return err
	}
	s.node, _, _ = s.fleet.Get(n.ID)
	return s.forget(s.node)
}

func (s *readmitState) keepsNameKindHardwareAndWhen() error {
	if len(s.departed) != 1 {
		return fmt.Errorf("want one record, got %+v", s.departed)
	}
	d := s.departed[0]
	want := edge.Departed{ID: s.node.ID, Name: "attic", Kind: "host", Hardware: "Pi 4", At: s.now.Unix()}
	if d != want {
		return fmt.Errorf("record = %+v, want %+v", d, want)
	}
	return nil
}

func (s *readmitState) nothingElseKept() error {
	b, err := json.Marshal(s.departed[0])
	if err != nil {
		return err
	}
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "at,hardware,id,kind,name" {
		return fmt.Errorf("the record keeps more than it should: %s", b)
	}
	for _, leak := range []string{s.node.Pin, "loft", "loaded", "enrolled"} {
		if strings.Contains(string(b), leak) {
			return fmt.Errorf("the record keeps %q: %s", leak, b)
		}
	}
	return nil
}

func (s *readmitState) recordIsBounded() error {
	var list []edge.Departed
	for i := 0; i < edge.MaxDeparted+5; i++ {
		list = edge.RecordDeparted(list, edge.Departed{ID: fmt.Sprintf("n_%02d", i), Name: fmt.Sprintf("d%02d", i),
			At: s.now.Add(time.Duration(i) * time.Minute).Unix()})
	}
	if edge.MaxDeparted != 64 || len(list) != 64 {
		return fmt.Errorf("bound = %d, kept %d; want 64", edge.MaxDeparted, len(list))
	}
	if list[0].Name != "d05" || list[63].Name != "d68" {
		return fmt.Errorf("the oldest were not the ones dropped: first %q last %q", list[0].Name, list[63].Name)
	}
	return nil
}

func (s *readmitState) aDeviceNamedThatWasForgotten(name string) error {
	n, err := s.aPi4("n_attic0000000000000000000000000000000000", name)
	if err != nil {
		return err
	}
	s.node = n
	if err := s.forget(n); err != nil {
		return err
	}
	s.now = s.now.Add(3 * 24 * time.Hour)
	return nil
}

func (s *readmitState) ownerReAdmits(want string) error {
	s.typed = want
	var err error
	s.found, s.ok, err = edge.FindDeparted(s.departed, want, s.now)
	if err != nil {
		return err
	}
	if s.ok {
		s.question = edge.ReAdmitQuestion(s.found, s.now)
	} else {
		s.question = edge.ReAdmitUnknownQuestion(want)
	}
	return nil
}

func (s *readmitState) resolvesWithoutTheID() error {
	if !s.ok || s.found.ID != s.node.ID {
		return fmt.Errorf("%q resolved to %+v (found %v), want %s", s.typed, s.found, s.ok, s.node.ID)
	}
	return nil
}

func (s *readmitState) questionNamesItWithHardwareAndWhen(name string) error {
	want := "re-admit " + name + ", a Pi 4 forgotten 3 days ago?"
	if s.question != want {
		return fmt.Errorf("question = %q, want %q", s.question, want)
	}
	if strings.Contains(s.question, s.node.ID) {
		return fmt.Errorf("the question says the id: %q", s.question)
	}
	return nil
}

func (s *readmitState) comesBackNamed(name string) error {
	// The authority's fleet knows what a returning device was called; the device itself presents
	// whatever it calls itself, and that is not what the owner named it.
	s.fleet.NameReturning(func(id string) string { return edge.DepartedName(s.departed, id) })
	got, err := s.fleet.Observe(s.found.ID, edge.Observation{Name: "raspberrypi", Kind: string(edge.Host),
		Addr: "192.168.1.40:4181", Fingerprint: strings.Repeat("ef", 32)})
	if err != nil {
		return err
	}
	if got.Name != name {
		return fmt.Errorf("it came back as %q, want %q", got.Name, name)
	}
	return nil
}

func (s *readmitState) ownerReAdmitsAnUnknownID() error { return s.ownerReAdmits("n_gone") }

func (s *readmitState) questionQuotesWhatWasTyped() error {
	want := `re-admit the device you typed as "n_gone"? this Edge has no record of it`
	if s.ok || s.question != want {
		return fmt.Errorf("question = %q (found %v), want %q", s.question, s.ok, want)
	}
	return nil
}

func (s *readmitState) givenNoName() error {
	if s.found.Name != "" {
		return fmt.Errorf("the claim would be named %q", s.found.Name)
	}
	if got := edge.DepartedName(s.departed, s.typed); got != "" {
		return fmt.Errorf("a returning name %q was invented for a device this Edge has no record of", got)
	}
	// It checks in with nothing of its own to be called (enrolment falls back to its id): it reads as
	// what it is, never as the id.
	s.fleet.NameReturning(func(id string) string { return edge.DepartedName(s.departed, id) })
	id := "n_0123456789abcdef0123456789abcdef01234567"
	got, err := s.fleet.Observe(id, edge.Observation{Name: id, Kind: string(edge.Mobile),
		Addr: "192.168.1.41:4181", Fingerprint: strings.Repeat("ab", 32)})
	if err != nil {
		return err
	}
	if w := edge.UnitName(got, nil); w != "phone" {
		return fmt.Errorf("an unnamed returning device reads as %q, want its hardware word", w)
	}
	return nil
}

func (s *readmitState) ownerRunsReAdmitWithNoDevice() error {
	for i, name := range []string{"attic", "shed", ""} {
		n := store.EdgeNode{ID: fmt.Sprintf("n_%d000000000000000000000000000000000000000", i), Name: name, Kind: string(edge.Mobile)}
		s.departed = edge.RecordDeparted(s.departed, edge.DepartedOf(n, s.now.Add(time.Duration(i)*time.Hour)))
	}
	s.now = s.now.Add(48 * time.Hour)
	s.listing = edge.NewestDeparted(s.departed, nil)
	return nil
}

func (s *readmitState) listsNewestFirst() error {
	var lines []string
	for _, d := range s.listing {
		lines = append(lines, edge.DepartedWords(d, s.now))
	}
	want := []string{"a phone forgotten 1 day ago", "shed, a phone forgotten 1 day ago", "attic, a phone forgotten 2 days ago"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		return fmt.Errorf("listing = %q, want %q", lines, want)
	}
	return nil
}

func (s *readmitState) saysHowToReAdmit() error {
	if got := edge.ReAdmitHow(s.listing[1], s.departed, nil); got != "roger edge readmit shed" {
		return fmt.Errorf("how = %q", got)
	}
	// A device that had no name is re-admitted by the only handle there is, and only as an argument.
	if got := edge.ReAdmitHow(s.listing[0], s.departed, nil); got != "roger edge readmit "+s.listing[0].ID {
		return fmt.Errorf("how (unnamed) = %q", got)
	}
	return nil
}

func registerReAdmitSteps(sc *godog.ScenarioContext) {
	s := &readmitState{}
	sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { s.reset(); return c, nil })
	sc.Step(`^the owner forgets a device$`, s.ownerForgetsADevice)
	sc.Step(`^this Edge keeps, for that device only, its last name, its kind and hardware word, and when it was forgotten$`, s.keepsNameKindHardwareAndWhen)
	sc.Step(`^nothing else of it is kept: not its pin, its grants, its tags or its history$`, s.nothingElseKept)
	sc.Step(`^the record is bounded: the oldest is dropped once there are more than 64$`, s.recordIsBounded)
	sc.Step(`^a device named "([^"]*)" that was forgotten$`, s.aDeviceNamedThatWasForgotten)
	sc.Step(`^the owner re-admits "([^"]*)"$`, s.ownerReAdmits)
	sc.Step(`^it resolves to that device without its id being typed$`, s.resolvesWithoutTheID)
	sc.Step(`^the question names it as "([^"]*)", with its hardware and when it was forgotten$`, s.questionNamesItWithHardwareAndWhen)
	sc.Step(`^when it claims its fresh certificate it comes back named "([^"]*)"$`, s.comesBackNamed)
	sc.Step(`^the owner re-admits by an id this Edge has no record of$`, s.ownerReAdmitsAnUnknownID)
	sc.Step(`^the question quotes it as what the owner typed and says this Edge has no record of it$`, s.questionQuotesWhatWasTyped)
	sc.Step(`^the device it admits is given no name until the owner names it$`, s.givenNoName)
	sc.Step(`^the owner runs re-admit with no device$`, s.ownerRunsReAdmitWithNoDevice)
	sc.Step(`^it lists the forgotten devices by name, hardware and when, newest first$`, s.listsNewestFirst)
	sc.Step(`^says how to re-admit one$`, s.saysHowToReAdmit)
}
