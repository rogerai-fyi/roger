package tui

// operator_steps_enrichment_test.go - godog step definitions for the OPERATOR FRAME
// ENRICHMENT follow-up (features/operator/rc_enrichment.feature): the "guest has the
// mic" status frame carries the additive model/spend metadata, populated from the LIVE
// ProxyOptionsHolder through the real money path (stub billing broker -> real hardened
// proxy -> holder accumulator), relayed through a REAL client.RCBridge to the stub RC
// broker. Founder rulings applied: spend YES; Band DROPPED (no field at all - the
// private Freq secret must never appear on any frame field). No mocks.

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// --- Background -----------------------------------------------------------------------------

// tunedBandModelAndDetectedGuest is the enrichment Background's 2-arg form: it re-tunes
// the live holder to the named public model (the same SetBand a real re-tune performs;
// the bridge + money servers from the host-session step stay attached) and detects the
// guest. The model is what the enrichment must report.
func (s *opBDD) tunedBandModelAndDetectedGuest(mdl, guest string) error {
	s.holder.SetBand(client.ProxyOptions{Broker: s.brokerSrv.URL, User: "tester", Model: mdl})
	return s.addDetected(guest)
}

// --- frame plumbing -------------------------------------------------------------------------

// waitStatusFrame waits for a status frame satisfying ok and returns it.
func (s *opBDD) waitStatusFrame(what string, ok func(protocol.RCFrame) bool) (protocol.RCFrame, error) {
	var got protocol.RCFrame
	if !waitFor(func() bool {
		for _, f := range s.framesSnapshot() {
			if f.Kind == protocol.RCKindStatus && ok(f) {
				got = f
				return true
			}
		}
		return false
	}, 2*time.Second) {
		return protocol.RCFrame{}, fmt.Errorf("no status frame %s (frames: %+v)", what, s.framesSnapshot())
	}
	return got, nil
}

// startFrame is the handoff-start status frame: the FIRST status frame on the wire (the
// emit precedes the Park and the pump preserves order).
func (s *opBDD) startFrame() (protocol.RCFrame, error) {
	if _, err := s.waitStatusFrame("at all", func(protocol.RCFrame) bool { return true }); err != nil {
		return protocol.RCFrame{}, err
	}
	for _, f := range s.framesSnapshot() {
		if f.Kind == protocol.RCKindStatus {
			return f, nil
		}
	}
	return protocol.RCFrame{}, fmt.Errorf("no status frame on the wire")
}

// lastStatusFrame is the most recent status frame (the parked auto-frame just answered).
func (s *opBDD) lastStatusFrame() (protocol.RCFrame, error) {
	if _, err := s.waitStatusFrame("at all", func(protocol.RCFrame) bool { return true }); err != nil {
		return protocol.RCFrame{}, err
	}
	frames := s.framesSnapshot()
	for i := len(frames) - 1; i >= 0; i-- {
		if frames[i].Kind == protocol.RCKindStatus {
			return frames[i], nil
		}
	}
	return protocol.RCFrame{}, fmt.Errorf("no status frame on the wire")
}

func spendEquals(got float64, want string) error {
	w, err := strconv.ParseFloat(want, 64)
	if err != nil {
		return fmt.Errorf("bad spend literal %q: %v", want, err)
	}
	if math.Abs(got-w) > 1e-9 {
		return fmt.Errorf("frame spend = %v, want $%s", got, want)
	}
	return nil
}

// --- E1: enriched handoff-start frame -------------------------------------------------------

func (s *opBDD) frameCarriesModel(mdl string) error {
	f, err := s.startFrame()
	if err != nil {
		return err
	}
	if f.Model != mdl {
		return fmt.Errorf("start frame model = %q, want %q", f.Model, mdl)
	}
	return nil
}

func (s *opBDD) frameCarriesSpend(amount string) error {
	f, err := s.startFrame()
	if err != nil {
		return err
	}
	return spendEquals(f.Spend, amount)
}

func (s *opBDD) noFrameWireJSONCarriesKey(key string) error {
	for _, f := range s.framesSnapshot() {
		raw, err := json.Marshal(f)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), `"`+key+`"`) {
			return fmt.Errorf("a frame's wire JSON carries a %q key: %s", key, raw)
		}
	}
	return nil
}

func (s *opBDD) previousHandoffLeftSpend(amount string) error {
	// Accumulate REAL spend on the holder through the money path (a prior session's
	// figure the fresh handoff must never inherit - ResetSpend runs at exec).
	return s.proxyCallsCosts([]string{amount})
}

func (s *opBDD) startFrameCarriesSpend(amount string) error { return s.frameCarriesSpend(amount) }

func (s *opBDD) startFrameCarriesModel(mdl string) error { return s.frameCarriesModel(mdl) }

// --- E2: live spend on parked auto-frames ----------------------------------------------------

func (s *opBDD) guestHasSpentSoFar(amount string) error {
	// Drive real guest-shaped calls through the real proxy while the mic is handed off;
	// the holder accumulator (the enrichment's source) moves to the billed figure.
	return s.proxyCallsCosts([]string{amount})
}

func (s *opBDD) thatStatusFrameCarriesSpend(amount string) error {
	f, err := s.lastStatusFrame()
	if err != nil {
		return err
	}
	return spendEquals(f.Spend, amount)
}

func (s *opBDD) thatStatusFrameCarriesModel(mdl string) error {
	f, err := s.lastStatusFrame()
	if err != nil {
		return err
	}
	if f.Model != mdl {
		return fmt.Errorf("parked status frame model = %q, want %q", f.Model, mdl)
	}
	return nil
}

func (s *opBDD) viewerReceivesStatusFrameWithSpend(amount string) error {
	w, err := strconv.ParseFloat(amount, 64)
	if err != nil {
		return err
	}
	_, err = s.waitStatusFrame(fmt.Sprintf("carrying spend $%s", amount), func(f protocol.RCFrame) bool {
		return math.Abs(f.Spend-w) <= 1e-9
	})
	return err
}

// --- E3: the DJ-back frame carries no enrichment ---------------------------------------------

func (s *opBDD) djBackFrame() (protocol.RCFrame, error) {
	return s.waitStatusFrame("announcing the DJ is back", func(f protocol.RCFrame) bool {
		return strings.Contains(f.Text, "DJ is back")
	})
}

func (s *opBDD) djBackFrameCarriesNoEnrichment() error {
	f, err := s.djBackFrame()
	if err != nil {
		return err
	}
	if f.Operator != "" || f.Model != "" || f.Spend != 0 {
		return fmt.Errorf("the DJ-back frame carries enrichment: %+v", f)
	}
	return nil
}

func (s *opBDD) djBackWireJSONOmitsKeys(k1, k2, k3 string) error {
	f, err := s.djBackFrame()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	for _, k := range []string{k1, k2, k3} {
		if strings.Contains(string(raw), `"`+k+`"`) {
			return fmt.Errorf("the DJ-back wire JSON carries %q: %s", k, raw)
		}
	}
	return nil
}

// --- E4: omitempty / degrade-clean -----------------------------------------------------------

func (s *opBDD) statusFrameBuiltBare() error {
	raw, err := json.Marshal(client.OperatorStatusFrame("opencode", "", 0))
	if err != nil {
		return err
	}
	s.builtFrameJSON = raw
	return nil
}

func (s *opBDD) builtWireJSONOmitsKey(key string) error {
	if strings.Contains(string(s.builtFrameJSON), `"`+key+`"`) {
		return fmt.Errorf("the bare frame's wire JSON carries %q: %s", key, s.builtFrameJSON)
	}
	return nil
}

func (s *opBDD) builtWireJSONCarriesKeys(k1, k2 string) error {
	for _, k := range []string{k1, k2} {
		if !strings.Contains(string(s.builtFrameJSON), `"`+k+`"`) {
			return fmt.Errorf("the bare frame's wire JSON must carry %q: %s", k, s.builtFrameJSON)
		}
	}
	return nil
}

func (s *opBDD) openMarketNoPrivateBand() error {
	// The open market is the no-Freq tune: same model, no private frequency code.
	s.holder.SetBand(client.ProxyOptions{Broker: s.brokerSrv.URL, User: "tester", Model: "gpt-oss-120b"})
	if s.holder.Get().Freq != "" {
		return fmt.Errorf("the open-market tune must carry no Freq")
	}
	return s.addDetected("opencode")
}

// --- E5: content-blind + the Freq secret ------------------------------------------------------

func (s *opBDD) everyFrameIsStatus() error {
	// Wait for the handoff-start frame (async pump), then sweep the whole wire.
	if _, err := s.waitStatusFrame("at all", func(protocol.RCFrame) bool { return true }); err != nil {
		return err
	}
	for _, f := range s.framesSnapshot() {
		if f.Kind != protocol.RCKindStatus {
			return fmt.Errorf("a non-status frame rode the wire during the handoff: %+v", f)
		}
	}
	return nil
}

func (s *opBDD) noFrameCarriesGuestContent() error {
	for _, f := range s.framesSnapshot() {
		if f.Tool != "" || f.Args != "" {
			return fmt.Errorf("a frame carries tool traffic: %+v", f)
		}
		if f.Text != "" && !strings.Contains(f.Text, "guest has the mic") && !strings.Contains(f.Text, "DJ is back") {
			return fmt.Errorf("a frame carries text beyond the fixed status templates: %+v", f)
		}
	}
	return nil
}

// statusFramesCarryOnlyEnrichmentMetadata executes "each status frame carries only
// operator, model, and spend metadata": beyond the envelope (Seq/TS/Kind/Text - the
// fixed template), every other wire field is unset.
func (s *opBDD) statusFramesCarryOnlyEnrichmentMetadata() error {
	for _, f := range s.framesSnapshot() {
		if f.Kind != protocol.RCKindStatus {
			continue
		}
		if f.Origin != "" || f.Tool != "" || f.Args != "" || f.ConfirmID != "" ||
			f.Approve != nil || f.Viewer != "" || f.HostUp != nil {
			return fmt.Errorf("a status frame carries more than operator/model/spend metadata: %+v", f)
		}
	}
	return nil
}

func (s *opBDD) tunedBandHasFreqCode(code string) error {
	// A PRIVATE band tune: the frequency code rides ProxyOptions.Freq (the hash-at-rest
	// secret) - and must never surface on any frame field.
	s.holder.SetBand(client.ProxyOptions{Broker: s.brokerSrv.URL, User: "tester", Model: "gpt-oss-120b", Freq: code})
	return nil
}

func (s *opBDD) noFrameCarriesText(secret string) error {
	// Wait for the handoff-start frame to land (the bridge pump is async), THEN sweep
	// every frame on the wire for the secret.
	if _, err := s.waitStatusFrame("at all", func(protocol.RCFrame) bool { return true }); err != nil {
		return err
	}
	for _, f := range s.framesSnapshot() {
		raw, err := json.Marshal(f)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), secret) {
			return fmt.Errorf("a frame leaked the private frequency code: %s", raw)
		}
	}
	return nil
}

// rcFrameCarriesNoBandField pins founder ruling 2 STRUCTURALLY: the wire type has no
// band (or freq) field for a secret to ever ride.
func (s *opBDD) rcFrameCarriesNoBandField() error {
	rt := reflect.TypeOf(protocol.RCFrame{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		name, tag := strings.ToLower(f.Name), strings.ToLower(f.Tag.Get("json"))
		if strings.Contains(name, "band") || strings.Contains(tag, "band") ||
			strings.Contains(name, "freq") || strings.Contains(tag, "freq") {
			return fmt.Errorf("RCFrame carries a band/freq field: %s (json:%q)", f.Name, tag)
		}
	}
	return nil
}

// --- E6: one shared constructor ---------------------------------------------------------------

func (s *opBDD) guestHasTheMicOn(mdl string) error {
	if err := s.ensureHandoff("opencode"); err != nil {
		return err
	}
	if got := s.holder.Get().Model; got != mdl {
		return fmt.Errorf("the live band model is %q, want %q", got, mdl)
	}
	return nil
}

func (s *opBDD) compareStartAndParkedFrames() error {
	start, err := s.startFrame()
	if err != nil {
		return err
	}
	if err := s.viewerSendsTurn("compare probe"); err != nil {
		return err
	}
	parked, err := s.lastStatusFrame()
	if err != nil {
		return err
	}
	if start.Seq == parked.Seq && start.TS == parked.TS && start.Spend == parked.Spend {
		// same frame twice would make the comparison vacuous
		return fmt.Errorf("the parked auto-frame never arrived (only the start frame is on the wire)")
	}
	s.cmpStart, s.cmpParked = &start, &parked
	return nil
}

func (s *opBDD) bothCarrySameOperatorAndModel() error {
	if s.cmpStart == nil || s.cmpParked == nil {
		return fmt.Errorf("no compared frames")
	}
	if s.cmpStart.Operator != s.cmpParked.Operator || s.cmpStart.Model != s.cmpParked.Model {
		return fmt.Errorf("start %+v and parked %+v drifted on operator/model", s.cmpStart, s.cmpParked)
	}
	return nil
}

func (s *opBDD) bothAreStatusWithText(kind, text string) error {
	for _, f := range []*protocol.RCFrame{s.cmpStart, s.cmpParked} {
		if f == nil {
			return fmt.Errorf("no compared frames")
		}
		if f.Kind != kind || !strings.Contains(f.Text, text) {
			return fmt.Errorf("frame %+v is not kind %q carrying %q", f, kind, text)
		}
	}
	return nil
}

// initializeEnrichmentSteps registers the rc_enrichment.feature steps (called from
// initializeOperatorScenarios so the whole operator suite shares one opBDD state).
func initializeEnrichmentSteps(st *opBDD, sc *godog.ScenarioContext) {
	sc.Step(`^a tuned band "([^"]*)" and a detected guest "([^"]*)"$`, st.tunedBandModelAndDetectedGuest)
	sc.Step(`^the frame carries the model "([^"]*)"$`, st.frameCarriesModel)
	sc.Step(`^the frame carries a spend of \$([0-9.]+)$`, st.frameCarriesSpend)
	sc.Step(`^no emitted frame's wire JSON carries a "([^"]*)" key$`, st.noFrameWireJSONCarriesKey)
	sc.Step(`^a previous handoff left the spend accumulator at \$([0-9.]+)$`, st.previousHandoffLeftSpend)
	sc.Step(`^the handoff-start frame carries a spend of \$([0-9.]+)$`, st.startFrameCarriesSpend)
	sc.Step(`^the handoff-start frame carries the model "([^"]*)"$`, st.startFrameCarriesModel)
	sc.Step(`^the guest has spent \$([0-9.]+) so far$`, st.guestHasSpentSoFar)
	sc.Step(`^that status frame carries a spend of \$([0-9.]+)$`, st.thatStatusFrameCarriesSpend)
	sc.Step(`^that status frame carries the model "([^"]*)"$`, st.thatStatusFrameCarriesModel)
	sc.Step(`^the viewer receives a status frame carrying a spend of \$([0-9.]+)$`, st.viewerReceivesStatusFrameWithSpend)
	sc.Step(`^that frame carries no operator, model, or spend$`, st.djBackFrameCarriesNoEnrichment)
	sc.Step(`^its wire JSON omits the "([^"]*)", "([^"]*)", and "([^"]*)" keys entirely$`, st.djBackWireJSONOmitsKeys)
	sc.Step(`^an operator status frame is built with no model and zero spend$`, st.statusFrameBuiltBare)
	sc.Step(`^its wire JSON omits the "([^"]*)" key$`, st.builtWireJSONOmitsKey)
	sc.Step(`^its wire JSON still carries the "([^"]*)" and "([^"]*)" keys$`, st.builtWireJSONCarriesKeys)
	sc.Step(`^the tuned channel is the open market with no private band$`, st.openMarketNoPrivateBand)
	sc.Step(`^every frame emitted during the handoff is a status frame$`, st.everyFrameIsStatus)
	sc.Step(`^no frame carries any guest terminal output, prompt, tool call, or assistant text$`, st.noFrameCarriesGuestContent)
	sc.Step(`^each status frame carries only operator, model, and spend metadata$`, st.statusFramesCarryOnlyEnrichmentMetadata)
	sc.Step(`^the tuned band has the private frequency code "([^"]*)"$`, st.tunedBandHasFreqCode)
	sc.Step(`^no emitted frame carries the text "([^"]*)" in any field$`, st.noFrameCarriesText)
	sc.Step(`^the RCFrame wire type carries no band field at all$`, st.rcFrameCarriesNoBandField)
	sc.Step(`^the guest has the mic on "([^"]*)"$`, st.guestHasTheMicOn)
	sc.Step(`^the handoff-start frame and a parked-turn frame are compared$`, st.compareStartAndParkedFrames)
	sc.Step(`^both carry the same operator and model$`, st.bothCarrySameOperatorAndModel)
	sc.Step(`^both are kind "([^"]*)" carrying the "([^"]*)" text$`, st.bothAreStatusWithText)
}
