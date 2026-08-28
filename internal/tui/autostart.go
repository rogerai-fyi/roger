package tui

// AUTO-START AT LAUNCH: put the models the operator chose back on air.
//
// The rule an operator actually holds in their head is "the models I share are shared".
// Before this, every restart silently dropped a rig off the market and the only signal was
// an empty SHARE table nobody was looking at. So sharing arms a model, and launching honours
// it.
//
// What this file adds on top of the controller is the REPORT. Auto-start can partly succeed -
// a priced model with nobody signed in, a rig over its on-air cap, a node id another roger
// already holds - and a launch that quietly started three of five models is the same class of
// problem as the empty table: the operator's belief about what they are broadcasting drifts
// away from the truth. Every model that did not go on air is therefore NAMED, with the reason.

import (
	"sort"
	"strings"

	"rogerai.fm/roger/v6/internal/node"
)

// autoStartArmedAtLaunch reports whether Init has any reason to kick the launch detect.
// Extracted so the ignition itself is assertable: the mechanism being correct says nothing
// about whether anything calls it, which is exactly how the missing trigger survived.
func (m model) autoStartArmedAtLaunch() bool {
	return m.ctrl != nil && len(m.ctrl.AutoStartModels()) > 0
}

// runAutoStart fires once per launch, the first time the provider catalog is populated -
// auto-start cannot start a model whose row has not been detected yet.
func (m *model) runAutoStart() {
	if m.autoStarted || m.ctrl == nil {
		return
	}
	if len(m.ctrl.AutoStartModels()) == 0 {
		m.autoStarted = true
		return
	}
	// A LAUNCH THAT DETECTED NOTHING GAVE AUTO-START NO CHANCE, so it must not spend the
	// once-per-launch guard. roger routinely starts before the local model server does; if
	// the empty first scan counted as the attempt, the re-scan that finally finds the models
	// would be refused and the rig would stay dark for the whole session - while the status
	// line had already claimed ON AIR.
	if len(m.ctrl.Rows()) == 0 {
		return
	}
	m.autoStarted = true
	m.autoStartRep = m.ctrl.AutoStartAll()
	m.syncShareCache()
}

// autoStartStatus is the one-line account of what the launch did, or "" when there is
// nothing to say. Skipped models are always named: a count would tell an operator that
// something did not start without telling them WHAT, which is the worse half of silence.
func (m model) autoStartStatus() string {
	r := m.autoStartRep
	if !r.Any() {
		return ""
	}
	var parts []string
	if len(r.Started) > 0 {
		parts = append(parts, stLive.Render("ON AIR ")+stKey.Render(strings.Join(r.Started, " ")))
	}
	// HELD is not an error and must not read as one. A second roger finding its models
	// already broadcasting is the per-node-id lock doing its job - two broadcasters on one
	// node id is what scrambles earnings attribution - so it is reported as a plain fact.
	if len(r.Held) > 0 {
		parts = append(parts, stDim.Render("already on air in another roger: ")+
			stKey.Render(strings.Join(r.Held, " ")))
	}
	// Not a failure and not a success: this machine simply has no such model right now.
	if len(r.NotServed) > 0 {
		parts = append(parts, stDim.Render("not found on this machine: ")+
			stKey.Render(strings.Join(r.NotServed, " ")))
	}
	if len(r.NeedsLogin) > 0 {
		parts = append(parts, stEmber.Render("needs login: ")+stKey.Render(strings.Join(r.NeedsLogin, " ")))
	}
	if len(r.AtLimit) > 0 {
		parts = append(parts, stEmber.Render("over the on-air cap: ")+stKey.Render(strings.Join(r.AtLimit, " ")))
	}
	if len(r.Failed) > 0 {
		names := make([]string, 0, len(r.Failed))
		for mdl := range r.Failed {
			names = append(names, mdl)
		}
		sort.Strings(names)
		parts = append(parts, stEmber.Render("failed: ")+stKey.Render(strings.Join(names, " ")))
	}
	return strings.Join(parts, stDim.Render(" · "))
}

// autoStartReportFor is a test seam: it lets a test assert on the report shape without
// reaching into the controller.
func (m model) autoStartReport() node.AutoStartReport { return m.autoStartRep }
