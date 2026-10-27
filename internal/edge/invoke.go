package edge

// THE INVOKE FACE (features/edge/control.feature): one member asks another to ACT - delegate a task
// to its agent, or act on a device - under an authority-signed grant. This is the endpoint server.go
// reserved ("being asked to DO something is control.feature"). Every refusal is specified and drawn;
// nothing runs until the grant, the caller, the capability, the freshness, the replay and the hop
// chain all check out.
//
// The handler owns the GUARDS (mutual TLS + membership via caller(), freshness, replay, loop and
// hop-count refusal, the signed receipt). The AUTHORIZATION decision - verify the grant's signature
// under this Edge's root, match the caller, and AuthorizeInvoke the capability on this node - and the
// RUNNER that actually executes the task are injected via Serving, so the trust composition and the
// harness live where they belong and the face stays a thin, testable boundary.

import (
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// InvokePath is the face's action endpoint.
const InvokePath = "/edge/invoke"

// invokeMaxBody bounds a task payload.
const invokeMaxBody = 1 << 20

// invokeFreshness is how far an invoke's timestamp may be from the callee's clock.
const invokeFreshness = 5 * time.Minute

// InvokeRequest is what a caller POSTs to /edge/invoke.
type InvokeRequest struct {
	Grant      store.Grant     `json:"grant"`      // the authority-signed grant authorizing this
	Capability string          `json:"capability"` // operate | actuate | sense | ...
	Task       json.RawMessage `json:"task"`       // the task payload the receiver's runner interprets
	Origin     []string        `json:"origin"`     // node ids this task has already passed through (hop chain)
	Nonce      string          `json:"nonce"`
	TS         int64           `json:"ts"`
}

// InvokeResult is what the receiver returns.
type InvokeResult struct {
	Output  json.RawMessage `json:"output,omitempty"`
	Refused string          `json:"refused,omitempty"` // set when the task itself refused (e.g. sandbox)
}

// handleInvoke serves /edge/invoke. It runs only when every guard and the injected authorization pass.
func (s *Server) handleInvoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.serving == nil || s.serving.Authorize == nil || s.serving.Invoke == nil {
		http.Error(w, "this node does not accept invocations", http.StatusNotImplemented)
		return
	}
	// 1. Mutual TLS: a member of THIS Edge, over its own certificate.
	callerID, code, msg := s.caller(r)
	if code != 0 {
		http.Error(w, msg, code)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, invokeMaxBody))
	if err != nil {
		http.Error(w, "that invocation could not be read", http.StatusBadRequest)
		return
	}
	var req InvokeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "that is not an invocation", http.StatusBadRequest)
		return
	}
	now := s.now()

	rec := protocol.UsageReceipt{
		RequestID: r.Header.Get("X-Roger-Request"), NodeID: s.serving.NodeID,
		User: callerID, TS: now.Unix(), Local: true,
	}
	refuse := func(status int, reason string) {
		// Every refusal is receipted and drawn, never nothing (control.feature).
		rec.VoidReason = "invoke_refused:" + reason
		if len(s.serving.Key) == ed25519.PrivateKeySize {
			rec.SignNode(s.serving.Key)
		}
		w.Header().Set("X-RogerAI-Receipt", protocol.EncodeReceipt(rec))
		http.Error(w, reason, status)
	}

	// 2. Fresh: not far from this node's clock.
	if d := now.Sub(time.Unix(req.TS, 0)); d > invokeFreshness || d < -invokeFreshness {
		refuse(http.StatusForbidden, "that invocation is stale")
		return
	}
	// 3. No loop: a task whose chain already names THIS node is refused (control.feature rule 4).
	for _, hop := range req.Origin {
		if hop == s.serving.NodeID {
			refuse(http.StatusForbidden, "that task's chain already names this node - refusing the loop")
			return
		}
	}
	// 4. Hop limit: onward delegation is bounded by the grant (0 = no onward delegation). Origin is
	//    caller-supplied and NOT signed, so today - with no onward delegation yet emitting it - this
	//    and the loop check above are best-effort against an honest delegation path; when the runner
	//    gains onward invoke (Slice 3), it will APPEND this node to Origin so the chain is built by the
	//    fabric, not the caller, and a signed envelope will bind it.
	if len(req.Origin) > req.Grant.MaxHops {
		refuse(http.StatusForbidden, "that task has been delegated more times than the grant allows")
		return
	}
	// 5. The AUTHORIZATION: signature under this Edge's root, the caller, and the capability on this
	//    node. Injected, composed at the host.
	if err := s.serving.Authorize(req.Grant, callerID, Capability(req.Capability)); err != nil {
		refuse(http.StatusForbidden, err.Error())
		return
	}
	// 6. Not a replay - checked AFTER authorization, so an unauthorized caller can never consume a
	//    nonce and fill the replay cache. The cache is built in NewServer (never lazily here: that raced).
	if !s.invokeSeen.admit(callerID, req.Nonce, time.Unix(req.TS, 0).Add(invokeFreshness).Unix(), now.Unix()) {
		refuse(http.StatusForbidden, "that invocation was already seen")
		return
	}
	// 7. Run it. The receiver's runner uses its OWN harness, tools and permissions (control.feature).
	res, err := s.serving.Invoke(callerID, req)
	if err != nil {
		refuse(http.StatusBadGateway, "that task could not run: "+err.Error())
		return
	}
	if len(s.serving.Key) == ed25519.PrivateKeySize {
		rec.SignNode(s.serving.Key)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-RogerAI-Provider", s.serving.NodeID)
	w.Header().Set("X-RogerAI-Receipt", protocol.EncodeReceipt(rec))
	_ = json.NewEncoder(w).Encode(res)
}
