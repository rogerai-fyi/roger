package station

// edge.go is the Station serving a CONSUMER directly, through a Tower that cannot read the
// session.
//
// Contract: features/tower/edge_dispatch.feature.
//
// # HOW THIS DIFFERS FROM execute.go
//
// On the relayed path the Station is handed a sealed envelope by a Tower and answers with
// another sealed envelope. Roger Core is at both ends and the Station never meets the
// consumer. Here the consumer is the other end of the TLS session, so:
//
//	the request arrives as PLAINTEXT, because the confidentiality is the TLS session rather
//	than an envelope. There is no key to seal to: the consumer has none Core has recorded.
//	the grant is bounded rather than digest-bound, because Core never saw the request.
//	the response goes back as the model's own bytes, so an unmodified OpenAI-compatible
//	client works without knowing any of this exists.
//
// # WHY THE RECEIPT IS A HEADER
//
// The whole value of the edge path is that anything which can talk to an OpenAI-compatible
// endpoint can use it. A client that had to unwrap a Roger-shaped envelope to find its
// completion would not be that client any more. So the body is the model's answer verbatim
// and the evidence rides alongside in a header, where a first-party client can find it and
// everybody else ignores it.
//
// A consumer that ignores the header simply never acknowledges, and the attempt settles
// uncorroborated. That is a deliberate, funded position - see dispatch/evidence.go.

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"rogerai.fm/roger/v5/internal/towercore/dispatch"
)

// GrantHeader carries Core's authorization from the consumer to the Station, base64 so it
// survives a header field intact.
const GrantHeader = "X-Rogerai-Grant"

// ReceiptHeader carries the Station's signed statement back. A client that does not know
// about it is unaffected; one that does can acknowledge against it.
const ReceiptHeader = "X-Rogerai-Receipt"

// EdgeRequest is one consumer call.
type EdgeRequest struct {
	// Grant is the base64 of Core's signed edge grant, exactly as the consumer received it.
	Grant string
	// Body is the request, in the clear. The TLS session it arrived on is what kept it from
	// the Tower; there is no envelope here and no key to open one with.
	Body []byte
}

// EdgeResponse is what goes back to the consumer.
type EdgeResponse struct {
	// Body is the model's own bytes, unchanged, so an ordinary client works.
	Body []byte
	// Receipt is base64 of the Station's signed statement, for the ReceiptHeader.
	Receipt string
	// Failure is set when the Station would not serve. It carries no receipt: a refusal is
	// not a result and must never be capable of settling one.
	Failure string
	// Status is the HTTP status to answer with. Unlike the relayed path - where a refusal is
	// a RESULT the Tower must relay to Core - the caller here is the consumer, and a consumer
	// needs an error to look like an error.
	Status int
}

// EdgeExecutor serves consumers on the edge path.
type EdgeExecutor struct {
	Station *Station
	// CoreKey is Core's grant-signing public key, pinned by the operator out of band. A
	// Station never talks to Core, so it cannot fetch this over the only channel it has -
	// which is the Tower, precisely the party a forged grant would come from.
	CoreKey  []byte
	Network  string
	Upstream Upstream
	// Outbox is where a copy of every receipt waits for the Tower to collect it. The
	// consumer's copy rides the TLS session; this one is the copy that can actually reach
	// settlement, because a Station cannot reach Core and the Tower can.
	Outbox *Outbox
	// Transcripts keeps a bounded, sampled record of exact bytes for post-hoc audit - the
	// only route by which Tower-served content is reviewed, since Core never saw it. Nil
	// means this Station keeps none, which is legal and means it can never pass an audit.
	Transcripts *Transcripts
	// Seen is the sealed path's one-serve-per-attempt guard (see AttemptCache). Nil disables
	// replay suppression - acceptable only in tests; a wired node should always carry one.
	Seen *AttemptCache
	Now  func() time.Time
}

func (e EdgeExecutor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Serve verifies, runs, and signs for exactly what it returns.
func (e EdgeExecutor) Serve(ctx context.Context, in EdgeRequest) EdgeResponse {
	if len(e.CoreKey) == 0 {
		// FAIL CLOSED. A Station with no pinned key cannot tell a real grant from one anybody
		// wrote, and serving anyway would make every check below theatre. 500 rather than 403:
		// this is the operator's mistake, not the caller's.
		return fail(500, "this Station has no pinned Roger Core key, so it cannot verify a grant")
	}
	if in.Grant == "" {
		return fail(401, "this request carries no Roger Core grant")
	}
	raw, err := base64.StdEncoding.DecodeString(in.Grant)
	if err != nil {
		return fail(400, "this request's grant is not valid base64")
	}
	if len(in.Body) == 0 {
		return fail(400, "this request has no body")
	}
	// EVERY CHECK IS IN HERE, not duplicated. dispatch.ParseEdgeGrant is the single definition
	// of what makes an edge grant valid, so the issuing side and this side cannot drift into
	// disagreeing about whether an authorization is good.
	grant, err := dispatch.ParseEdgeGrant(raw, e.CoreKey, e.Network, e.Station.StationID,
		in.Body, e.now())
	if err != nil {
		if errors.Is(err, dispatch.ErrExpired) {
			return fail(403, "this grant has expired")
		}
		return fail(403, err.Error())
	}
	if e.Upstream == nil {
		return fail(500, "this Station has no upstream model configured")
	}

	body, err := e.Upstream.Serve(ctx, in.Body)
	if err != nil {
		// The upstream's own words: an operator debugging a Station needs what the model
		// actually said, and a consumer needs to know it was the model rather than the grant.
		return fail(502, "the model did not answer: "+err.Error())
	}
	// THE CEILING APPLIES TO THE ANSWER TOO. Without this the output bound in the grant would
	// be a number Core wrote down and nobody enforced - and output is the expensive direction.
	if int64(len(body)) > grant.MaxOut {
		return fail(502, "the model returned more than this grant allows")
	}
	// Signed over what is being RETURNED, produced from the same bytes that go on the wire.
	// Signing a re-encoding would leave a gap between what was attested and what was sent.
	// The usage claim, measured from the exact bytes in and out. Byte counts rather than
	// tokens, deliberately: bytes are what both ends can measure identically without sharing
	// a tokenizer, and what the relay's own accounting can be compared against.
	// Token usage is signed alongside bytes for the Option C per-token path, parsed from the
	// model's own reported usage. Zero when the upstream reports none (or a non-JSON body), in
	// which case the per-token settle path bills nothing and the byte fields + audit govern.
	rec, err := dispatch.SignReceipt(e.Station.assertionPriv, e.Network,
		dispatch.Grant{AttemptID: grant.AttemptID, StationID: grant.StationID}, in.Body, body,
		dispatch.Usage{In: int64(len(in.Body)), Out: int64(len(body))}, tokenUsageOf(body))
	if err != nil {
		return fail(500, "this Station could not sign its result: "+err.Error())
	}
	if e.Outbox != nil {
		// Queued BEFORE the consumer sees the answer, so a consumer that disconnects the
		// moment it has its bytes cannot leave the evidence unqueued.
		e.Outbox.Add(Evidence{AttemptID: grant.AttemptID, StationID: grant.StationID,
			Receipt: rec.Signed})
	}
	if e.Transcripts != nil {
		// Kept for a possible audit. The store samples and bounds itself, so this is a hint
		// to remember rather than a promise to; a Station that keeps none simply fails audits.
		e.Transcripts.Keep(Transcript{AttemptID: grant.AttemptID, Request: in.Body, Response: body})
	}
	return EdgeResponse{
		Body:    body,
		Receipt: base64.StdEncoding.EncodeToString(rec.Signed),
		Status:  200,
	}
}

func fail(status int, msg string) EdgeResponse {
	return EdgeResponse{Failure: msg, Status: status}
}

// Transcript signs the stored bytes for one attempt, for an audit Core asked for.
//
// Signed HERE, on demand, so the assertion key never leaves the executor and the plaintext
// store holds only bytes - a store that also held signatures would be a store whose theft
// yielded forgeable attestations. The bool is false when this Station did not keep the
// attempt (never sampled, or aged out), which the audit treats as "cannot produce".
func (e EdgeExecutor) Transcript(attemptID string) (dispatch.SignedTranscript, bool, error) {
	if e.Transcripts == nil {
		return dispatch.SignedTranscript{}, false, nil
	}
	t, ok := e.Transcripts.Get(attemptID)
	if !ok {
		return dispatch.SignedTranscript{}, false, nil
	}
	signed, err := dispatch.SignTranscript(e.Station.assertionPriv, e.Network,
		attemptID, t.Request, t.Response)
	if err != nil {
		return dispatch.SignedTranscript{}, false, err
	}
	return signed, true, nil
}
