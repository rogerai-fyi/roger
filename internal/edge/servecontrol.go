package edge

// SERVE CONTROL (features/edge/remote_model.feature): the vocabulary and the refusals of a request to
// change what a member serves. The host does the work (cmd/rogerai); the face carries the request
// through Serving.Invoke, so the certificate, grant, revocation, caller and replay checks are the
// ones every invoke already gets.
//
// The task is read STRICTLY: a field the protocol does not have is a malformed request, not a field
// quietly ignored. That is the property the spec asks for in so many words - there is no shape of
// request that can make a member fetch from somewhere the caller chose - and it holds because the
// decoder refuses rather than because a handler remembered to look.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"rogerai.fm/roger/v6/internal/provision"
	"rogerai.fm/roger/v6/internal/store"
)

// EdgeModel is a roger's own serving and fetch, as it reports them.
type EdgeModel = store.EdgeModel

// ModelAction is what a serve-control task asks for.
type ModelAction string

const (
	ModelServe  ModelAction = "serve"  // serve this model id, fetching it first if it is an exact index id
	ModelStatus ModelAction = "status" // what is served and fetching, right now
	ModelStop   ModelAction = "stop"   // stop serving
)

// ModelTask is the whole wire: an action, a model id, a context length. Nothing else.
type ModelTask struct {
	Action ModelAction `json:"action"`
	Model  string      `json:"model,omitempty"`
	Ctx    int         `json:"ctx,omitempty"`
}

// ModelResult is what the member answers: what it did, in words, and what it serves and fetches now.
type ModelResult struct {
	Action  ModelAction `json:"action"`
	Model   string      `json:"model,omitempty"`
	Changed bool        `json:"changed"`
	Says    string      `json:"says"`
	Now     *EdgeModel  `json:"now,omitempty"`
}

// ServeControlOffReason is the refusal of a member whose owner never turned this on THERE. It names
// the switch and where it lives, and it is the same with or without a grant.
const ServeControlOffReason = "remote serving control is off on that machine; its owner turns it on there with `roger edge serve-control on`"

// maxModelID bounds an id: a repo name, a colon and a quantisation are never longer.
const maxModelID = 200

// localID is what a model on a member's disk is called: a repo:QUANT id, or a file's own name.
var localID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)?(:[A-Za-z0-9._-]+)?$`)

var exactIndexID = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*):([A-Za-z0-9][A-Za-z0-9._-]*)$`)

// ExactIndexID splits "owner/repo:QUANT", the one form a member will fetch from its own source by
// (founder ruling 2026-10-01). Anything less exact is not looked up anywhere.
func ExactIndexID(id string) (repo, quant string, ok bool) {
	m := exactIndexID.FindStringSubmatch(id)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// ParseModelTask reads a task strictly and refuses whatever cannot mean what it says, before the host
// looks anything up. The string is the refusal, "" when the task is good.
func ParseModelTask(raw json.RawMessage) (ModelTask, string) {
	var t ModelTask
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&t) != nil {
		return t, "a malformed serve-control request: it carries an action, a model id and a context, and nothing else"
	}
	t.Action = ModelAction(strings.ToLower(strings.TrimSpace(string(t.Action))))
	t.Model = strings.TrimSpace(t.Model)
	switch t.Action {
	case ModelStatus, ModelStop:
		return t, ""
	case ModelServe:
	default:
		return t, "a serve-control request asks for one of: serve, status, stop"
	}
	if t.Model == "" {
		return t, "a serve request names the model to serve"
	}
	if len(t.Model) > maxModelID || !localID.MatchString(t.Model) || strings.Contains(t.Model, "..") ||
		strings.HasSuffix(strings.ToLower(t.Model), ".gguf") {
		return t, "that is not a model id - a member is told WHICH model, never where to get it"
	}
	if t.Ctx < 0 || t.Ctx > 1<<21 {
		return t, "that context length is not one a model can be served at"
	}
	return t, ""
}

// MaxHave bounds the model ids a member reports, so a disk full of files cannot bloat every beat.
const MaxHave = 32

// ModelOf is the model a node reported through any of its rogers: the one serving if any does, else
// the first that reports one.
func ModelOf(n store.EdgeNode) *EdgeModel {
	var first *EdgeModel
	for i := range n.Instances {
		m := n.Instances[i].Model
		if m == nil {
			continue
		}
		if m.Band != "" || (m.Fetch != nil && m.Fetch.State == "fetching") {
			return m
		}
		if first == nil {
			first = m
		}
	}
	return first
}

// ModelLine is a node's own serving and fetch in one line, worded the same on every surface. It
// keeps naming what is served NOW while a fetch for something else runs, because nothing has
// switched until the new one is proven.
func ModelLine(m *EdgeModel) string {
	if m == nil {
		return "serves nothing of its own"
	}
	var parts []string
	switch {
	case m.Band != "":
		parts = append(parts, fmt.Sprintf("serving %q (%s, %d context)", m.Band, m.ID, m.Ctx))
	case m.CannotServe != "":
		parts = append(parts, "cannot serve here: "+m.CannotServe)
	default:
		parts = append(parts, "serving nothing")
	}
	if m.Band == "" && m.ServeFailed != "" {
		parts = append(parts, "the last serve did not come on the air: "+m.ServeFailed)
	}
	if m.Band == "" && m.Stopped != "" {
		parts = append(parts, "stopped on its own: "+m.Stopped)
	}
	if f := m.Fetch; f != nil {
		switch f.State {
		case "fetching":
			parts = append(parts, fmt.Sprintf("fetching %s: %s of %s", f.File, provision.BytesWord(f.Done), provision.BytesWord(f.Total)))
		case "failed":
			parts = append(parts, "the last fetch failed: "+f.Err)
		case "interrupted":
			parts = append(parts, "the last fetch was interrupted and will resume, never served half-done")
		}
	}
	return strings.Join(parts, " · ")
}

// maxModelText bounds every word a member reports about its model.
const maxModelText = 200

// cleanText strips control characters (a terminal escape in a member's report must never reach an
// owner's screen) and bounds the length.
func cleanText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
	if len(s) > maxModelText {
		s = s[:maxModelText]
	}
	return strings.ToValidUTF8(s, "")
}

// CleanModel is a member's model report as the authority stores it: the member's word, bounded and
// cleaned whatever the sender did - its ids must be model ids, at most MaxHave of them, and no text
// carries a control character.
func CleanModel(m *EdgeModel) *EdgeModel {
	if m == nil {
		return nil
	}
	out := &EdgeModel{Band: cleanText(m.Band), ID: cleanText(m.ID), Quant: cleanText(m.Quant), Ctx: m.Ctx,
		Since: m.Since, CannotServe: cleanText(m.CannotServe), ServeFailed: cleanText(m.ServeFailed),
		Stopped: cleanText(m.Stopped)}
	if out.Ctx < 0 || out.Ctx > 1<<21 {
		out.Ctx = 0
	}
	for _, id := range m.Have {
		if len(out.Have) == MaxHave {
			break
		}
		if len(id) <= maxModelID && localID.MatchString(id) && !strings.Contains(id, "..") {
			out.Have = append(out.Have, id)
		}
	}
	if f := m.Fetch; f != nil {
		out.Fetch = &store.EdgeFetch{Repo: cleanText(f.Repo), File: cleanText(f.File), Done: f.Done, Total: f.Total,
			Rate: f.Rate, State: cleanText(f.State), Err: cleanText(f.Err)}
	}
	return out
}

// BandProven reports whether the serving probe proved band on any of the node's rogers: what the
// fabric's own check saw, not what the node says.
func BandProven(n store.EdgeNode, band string) bool {
	for _, in := range n.Instances {
		for _, b := range in.ServeProven {
			if b == band {
				return true
			}
		}
	}
	return false
}

// ModelLineFor is ModelLine for a node as the fabric has checked it: a band the serving probe has
// not proven is said as the node's claim, never as serving.
func ModelLineFor(n store.EdgeNode) string {
	m := ModelOf(n)
	if m == nil {
		return ""
	}
	if m.Band != "" && !BandProven(n, m.Band) {
		claim := *m
		claim.Band = ""
		rest := ModelLine(&claim)
		rest = strings.TrimPrefix(strings.TrimPrefix(rest, "serving nothing"), " · ")
		line := fmt.Sprintf("says it serves %q - not proven yet", m.Band)
		if rest != "" {
			line += " · " + rest
		}
		return line
	}
	return ModelLine(m)
}

// ServingShort is what a node serves in the few words a rack row has: the proven band with its
// quantisation and context, then a fetch in flight; or that it cannot serve.
func ServingShort(n store.EdgeNode) string {
	var parts []string
	m := ModelOf(n)
	if m != nil && m.Band != "" && BandProven(n, m.Band) {
		w := m.Band
		if m.Quant != "" {
			w += " " + m.Quant
		}
		if m.Ctx > 0 {
			w += " · " + ctxShort(m.Ctx)
		}
		parts = append(parts, w)
	} else if m == nil || m.Band == "" {
		for _, in := range n.Instances {
			for _, b := range in.Bands {
				if BandProven(n, b) {
					parts = append(parts, b)
				}
			}
		}
	}
	if m != nil {
		if f := m.Fetch; f != nil && f.State == "fetching" && f.Total > 0 {
			parts = append(parts, fmt.Sprintf("fetching %d%%", 100*f.Done/f.Total))
		}
		if len(parts) == 0 && m.CannotServe != "" {
			return "cannot serve here"
		}
	}
	return strings.Join(parts, " · ")
}

func ctxShort(n int) string {
	if n >= 1024 && n%1024 == 0 {
		return fmt.Sprintf("%dK", n/1024)
	}
	return fmt.Sprint(n)
}
