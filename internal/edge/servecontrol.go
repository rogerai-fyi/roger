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
	if f := m.Fetch; f != nil {
		switch f.State {
		case "fetching":
			parts = append(parts, fmt.Sprintf("fetching %s: %s of %s", f.File, byteWord(f.Done), byteWord(f.Total)))
		case "failed":
			parts = append(parts, "the last fetch failed: "+f.Err)
		case "interrupted":
			parts = append(parts, "the last fetch was interrupted and will resume, never served half-done")
		}
	}
	return strings.Join(parts, " · ")
}

func byteWord(n int64) string {
	f, u, i := float64(n), []string{"B", "KiB", "MiB", "GiB", "TiB"}, 0
	for f >= 1024 && i < len(u)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", f, u[i])
}
