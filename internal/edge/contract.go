package edge

// THE CONTRACT A CLASSIFYING NODE CARRIES.
//
// Wave models are CONTRACT models: the device prompt is PART of the device. Unframed a
// contract model floors; framed it performs, and both were measured (the models agent's
// 2026-08-01 answer, recorded in features/web/playbox_edge_honesty.feature). So a node
// declaring `classify` declares a CONTRACT - its task class, its fixed framing, and the
// label set it may answer with - and that contract travels with every escalation it
// raises, because model and prompt ship as one unit.
//
// The framings below are the SAME excerpts the shipped Playbox carries in its
// DEVICE_PROMPTS map (web/src/js/playbox.js), copied byte for byte and pinned by a test
// that reads that file. "The framing shown is the device's real production framing, not a
// paraphrase" is only true while something keeps checking.
//
// Spec: features/edge/sessions.feature, features/web/playbox_edge_honesty.feature.

import (
	"fmt"
	"sort"

	"rogerai.fm/roger/v6/internal/store"
)

// Contract is what a classifying device IS, as opposed to what model it runs. It is the
// store's record type: one shape, no second copy to drift from.
type Contract = store.EdgeContract

// deviceFraming is the production framing map, per task class. Excerpts of the REAL
// production device prompts - never paraphrase.
var deviceFraming = map[string]string{
	"alarm_triage":            "You are an offline alarm-management analyzer on an industrial edge device, applying ANSI/ISA-18.2-2016 / IEC 62682 and EEMUA Publication 191 alarm-performance metrics. For the single record in the user message, return ONE JSON …",
	"structured_extraction":   "You are an offline industrial record and instrument-tag structured-extraction parser on an edge device, applying ANSI/ISA-5.1 tag identification, CFIHOS reference data (units of measure) and NAMUR NE 107 status. For the single …",
	"tool_selection_args":     "You are an offline maintenance / alarm assistant on an industrial edge device. Available offline tools (read-only history and drafting only -- no live values, no control actions, no alarm state changes, no approvals): - …",
	"maintenance_failuremode": "You are an offline ISO 14224:2016 maintenance-record normalizer and failure-mode coder on an industrial edge device (equipment taxonomy and Annex B failure-mode codes). For the single record in the user message, return ONE JSON …",
	"ldar_trigger":            "You are an offline methane LDAR and super-emitter regulatory router on an industrial edge device covering US EPA 40 CFR Part 60 subparts OOOOb/OOOOc (including the super-emitter program), US state programs (e.g. Colorado CDPHE …",
	"abstention_safety":       "You are an OFFLINE, READ-ONLY industrial safety and compliance advisory assistant on an edge device. You have NO authority to actuate, start, stop, open, close, trip, reset, force, inhibit, bypass, shelve, or suppress any …",
}

// TaskClasses lists the task classes this build ships a framing for, in a stable order.
func TaskClasses() []string {
	out := make([]string, 0, len(deviceFraming))
	for k := range deviceFraming {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ProductionFraming is the real production framing for a task class, or "" for a class
// this build has none for. A device that names an unknown class carries its own framing
// and is believed; this map is what the SHIPPED classes are framed with.
func ProductionFraming(class string) string { return deviceFraming[class] }

// SetContract records the contract a classifying node declares.
//
// A contract needs BOTH a class and a framing: a device declaring `classify` with no
// framing is an unframed contract model, which is the one configuration measured as
// floored. Refusing it here is cheaper than drawing an escalation that carried nothing.
func (f *Fleet) SetContract(id string, c Contract) error {
	if c.Class == "" {
		return fmt.Errorf("a contract needs a task class")
	}
	if c.Framing == "" {
		return fmt.Errorf("a contract needs its framing: model and prompt ship as one unit")
	}
	return f.mutate(id, func(n *store.EdgeNode) error {
		if !Declares(*n, Classify) {
			return fmt.Errorf("%q does not declare classify, so it has no contract to carry", n.Name)
		}
		n.Contract = c
		n.History = append(n.History, store.EdgeEvent{At: f.now().Unix(), What: "contract", Detail: c.Class})
		return nil
	})
}
