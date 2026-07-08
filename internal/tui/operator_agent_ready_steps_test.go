package tui

// operator_agent_ready_steps_test.go - step definitions for
// features/operator/agent_ready_verified.feature: the AGENT view reports agent-readiness as
// VERIFIED (⌁) only when the OPEN CHANNEL's model carries the broker-probed "tools" capability
// AND ctx >= floor, INFERRED (⌁~) when the window qualifies but tools is absent, TOO SMALL (✕)
// when ctx is known and under the floor (regardless of tools), and ABSENT when the window is
// unknown. It drives the REAL bubbletea model through the same opBDD harness the band_gate
// steps use (m.connected is the open channel), asserting the real classifiers
// (operatorAgentReadyState / operatorChannelAgentTag) and the plate warn gate - no mocks.

import (
	"fmt"
	"strings"

	"github.com/cucumber/godog"
)

// setChannelTools stamps the broker-VERIFIED "tools" capability onto the OPEN CHANNEL's station
// (m.connected). If no station is on the channel yet (a Given that names tools before a window),
// it opens one at an agent-ready window so "reads VERIFIED" holds. It never lowers an existing
// window - a ctx-8192 station keeps its sub-floor window so the too-small gate still wins.
func (s *opBDD) setChannelTools() error {
	s.mutate(func(m *model) {
		if m.connected == nil {
			mdl := "qwen3-32b-fp8"
			if m.proxyHolder != nil {
				mdl = m.proxyHolder.Get().Model
			}
			m.connected = &offer{NodeID: "KDGPU-7", Model: mdl, Online: true, Ctx: 131072}
		}
		if !offerHasCapability(*m.connected, "tools") {
			m.connected.Capabilities = append(m.connected.Capabilities, "tools")
		}
	})
	if s.model().connected == nil {
		return fmt.Errorf("no open-channel station to carry the tools capability")
	}
	return nil
}

// clearChannelTools drops the verified "tools" bit from the open channel (a broker regression),
// leaving the window untouched - the reading must fall back to INFERRED, never a stale VERIFIED.
func (s *opBDD) clearChannelTools() error {
	s.mutate(func(m *model) {
		if m.connected == nil {
			return
		}
		var kept []string
		for _, c := range m.connected.Capabilities {
			if strings.EqualFold(strings.TrimSpace(c), "tools") {
				continue
			}
			kept = append(kept, c)
		}
		m.connected.Capabilities = kept
	})
	return nil
}

func (s *opBDD) channelHasNoTools() error { return s.clearChannelTools() }

func (s *opBDD) retuneToVerifiedTools() error {
	if err := s.setStation(131072, false); err != nil {
		return err
	}
	return s.setChannelTools()
}

// --- reading assertions (the real classifier, driven by m.connected) ------------------------

func (s *opBDD) readsVerified() error {
	if st := s.model().operatorAgentReadyState(); st != agentReadyVerified {
		return fmt.Errorf("AGENT view reads state %d, want VERIFIED agent-ready (ctx>=floor AND probed tools)", st)
	}
	return nil
}

func (s *opBDD) readsInferred() error {
	if st := s.model().operatorAgentReadyState(); st != agentReadyInferred {
		return fmt.Errorf("AGENT view reads state %d, want INFERRED agent-ready (ctx>=floor, tools absent)", st)
	}
	return nil
}

func (s *opBDD) readsTooSmall() error {
	if st := s.model().operatorAgentReadyState(); st != agentReadyTooSmall {
		return fmt.Errorf("AGENT view reads state %d, want TOO SMALL (ctx known and under floor)", st)
	}
	return nil
}

func (s *opBDD) verifiedMarkerNoTilde(_ string) error {
	tag := s.model().operatorChannelAgentTag()
	if tag == "" {
		return fmt.Errorf("no agent-ready marker on a verified band")
	}
	if strings.HasSuffix(tag, "~") {
		return fmt.Errorf("the verified marker carries an estimate tilde (%q) - verified must have no ~", tag)
	}
	return nil
}

func (s *opBDD) inferredMarkerTilde(_ string) error {
	tag := s.model().operatorChannelAgentTag()
	if !strings.HasSuffix(tag, "~") {
		return fmt.Errorf("the inferred marker (%q) lacks the unproven ~ tilde", tag)
	}
	return nil
}

// --- plate warn gate ------------------------------------------------------------------------

const plateToolWarn = "tool-call support unproven"

func (s *opBDD) plateDropsToolWarn() error {
	if strings.Contains(s.plateText(), plateToolWarn) {
		return fmt.Errorf("the plate still warns tool-call support is unproven on a VERIFIED band:\n%s", s.plateText())
	}
	if s.model().operatorPlate == nil {
		return fmt.Errorf("no plate is up to inspect (transcript: %s)", s.view())
	}
	return nil
}

func (s *opBDD) plateKeepsToolWarn() error {
	if !strings.Contains(s.plateText(), plateToolWarn) {
		return fmt.Errorf("the plate does not warn tool-call support is unproven on an UNPROBED band:\n%s", s.plateText())
	}
	return nil
}

func (s *opBDD) handoffProceedsThroughPlate() error {
	if s.model().operatorPlate == nil {
		return fmt.Errorf("the handoff did not proceed to the plate (transcript: %s)", s.view())
	}
	return nil
}

func (s *opBDD) handoffRefusedTooSmall() error { return s.handoffRefused() }

func (s *opBDD) verifiedDoesNotLiftRefusal() error {
	// The ctx floor is independent of tools: with a verified tools bit AND a sub-floor window,
	// the band still reads too-small and the gate still shuts (operatorBandTooSmall). Assert the
	// verified bit is present yet the too-small gate is unmoved - a probe never lifts the floor.
	if !s.model().operatorChannelTools() {
		return fmt.Errorf("the open channel is not carrying the verified tools bit for this check")
	}
	if !s.model().operatorBandTooSmall() {
		return fmt.Errorf("a verified tool-call capability lifted the too-small gate (the ctx floor must be independent)")
	}
	if st := s.model().operatorAgentReadyState(); st != agentReadyTooSmall {
		return fmt.Errorf("state is %d with verified tools under the floor, want TOO SMALL", st)
	}
	return nil
}

func (s *opBDD) plateWarnsCtxUnknown() error {
	if !strings.Contains(s.plateText(), "context window unknown") {
		return fmt.Errorf("the plate does not warn the context window is unknown:\n%s", s.plateText())
	}
	return nil
}

// absentToolsUndetermined: an unknown-window band's absent tools is UNDETERMINED, never a
// positive "no tools". Assert the reading is ABSENT, no verified/inferred marker is shown, and
// nothing in the view falsely claims the band has "no tools".
func (s *opBDD) absentToolsUndetermined(_ string) error {
	if st := s.model().operatorAgentReadyState(); st != agentReadyAbsent {
		return fmt.Errorf("unknown-window state is %d, want ABSENT", st)
	}
	if tag := s.model().operatorChannelAgentTag(); tag != "" {
		return fmt.Errorf("an unknown-window band shows an agent-ready marker %q (a claim it cannot back)", tag)
	}
	if s.model().operatorChannelTools() {
		return fmt.Errorf("absent tools read as a positive claim")
	}
	if v := s.plateText(); strings.Contains(v, "no tools") {
		return fmt.Errorf("the plate falsely claims 'no tools' for an undetermined band:\n%s", v)
	}
	return nil
}

func (s *opBDD) neverKeepsVerified() error {
	if st := s.model().operatorAgentReadyState(); st == agentReadyVerified {
		return fmt.Errorf("the reading still claims VERIFIED after the probe evidence was dropped")
	}
	if tag := s.model().operatorChannelAgentTag(); tag != "" && !strings.HasSuffix(tag, "~") {
		return fmt.Errorf("the marker still reads verified (%q) after tools was dropped", tag)
	}
	return nil
}

func initializeAgentReadyVerifiedSteps(st *opBDD, sc *godog.ScenarioContext) {
	sc.Step(`^the open channel's station model carries the verified "([^"]*)" capability$`,
		func(string) error { return st.setChannelTools() })
	sc.Step(`^the open channel's station model has no "([^"]*)" capability$`,
		func(string) error { return st.channelHasNoTools() })
	sc.Step(`^the consumer re-tunes to a station whose model carries verified "([^"]*)"$`,
		func(string) error { return st.retuneToVerifiedTools() })
	sc.Step(`^the broker drops "([^"]*)" for that model after a regression$`,
		func(string) error { return st.clearChannelTools() })

	sc.Step(`^the AGENT view reads the band as VERIFIED agent-ready$`, st.readsVerified)
	sc.Step(`^the AGENT view reads the band as INFERRED agent-ready$`, st.readsInferred)
	sc.Step(`^the AGENT view reads the band as too small for a guest$`, st.readsTooSmall)
	sc.Step(`^the verified marker carries no "([^"]*)" estimate tilde$`, st.verifiedMarkerNoTilde)
	sc.Step(`^the inferred marker carries the "([^"]*)" unproven tilde$`, st.inferredMarkerTilde)

	sc.Step(`^the pre-launch plate does NOT warn that tool-call support is unproven$`, st.plateDropsToolWarn)
	sc.Step(`^the pre-launch plate warns that tool-call support is unproven$`, st.plateKeepsToolWarn)
	sc.Step(`^the handoff proceeds through the plate as normal$`, st.handoffProceedsThroughPlate)
	sc.Step(`^the handoff is refused because the band is too small$`, st.handoffRefusedTooSmall)
	sc.Step(`^a verified tool-call capability does not lift the too-small refusal$`, st.verifiedDoesNotLiftRefusal)
	sc.Step(`^the pre-launch plate warns the context window is unknown$`, st.plateWarnsCtxUnknown)
	sc.Step(`^the absent tool-call capability is read as undetermined, never as "([^"]*)"$`, st.absentToolsUndetermined)
	sc.Step(`^it never keeps claiming verified once the probe evidence is gone$`, st.neverKeepsVerified)
}
