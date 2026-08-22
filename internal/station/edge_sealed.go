package station

// edge_sealed.go is the Station serving the OPTION C, TOPOLOGY 2 path: the tower hosts the
// data plane and carries only sealed bytes, the node polls it for work, and Roger Core never
// touches the payload at all.
//
// Contract: features/tower/edge_dispatch.feature.
//
// # HOW THIS DIFFERS FROM THE OTHER TWO SERVES
//
// execute.go (relayed): sealed input opened with the session key, RELAYED grant checked
// against a digest Core computed, result sealed BACK TO CORE - because Core carries the bytes
// and recounts. edge.go (TLS edge): plaintext input off a TLS session the tower spliced,
// EDGE grant with ceilings, result returned in the clear to the session.
//
// This file is the combination Topology 2 needs and neither provides: SEALED input (the
// consumer sealed it to this Station's session key, so the tower and Core cannot read it),
// an EDGE grant (bounded scope - Core never saw the request), a TOKEN receipt (per-token
// billing), and the result sealed TO THE CONSUMER's key from the grant - so the answer
// crosses the tower and, if it ever transits Core, Core as well, unreadable to both.
//
// # WHY THE SEALING KEY COMES FROM THE GRANT
//
// The node must seal its answer to SOMEBODY. Taking a key from the request body would let
// whoever carries the request (the tower) substitute its own and read every answer. The
// grant is Core-signed and the consumer put its key there at authorize - so the key the
// node seals to is the one the authorized consumer chose, attested by Core, and the tower
// cannot swap it without breaking the signature.

import (
	"context"
	"errors"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/towercore/dispatch"
	"rogerai.fm/roger/v6/internal/towercore/envelope"
)

// AttemptCache is the node's one-serve-per-attempt guard for the sealed path: a TTL'd set of
// attempt ids this node has already served, expiring at each grant's own deadline (after which
// the grant cannot authorize anything anyway). It protects the node's COMPUTE from a hostile
// tower replaying a completed job - Core's one-use settlement already protects the money.
type AttemptCache struct {
	mu   sync.Mutex
	seen map[string]time.Time // attemptID -> the grant deadline it expires at
}

// NewAttemptCache returns an empty cache.
func NewAttemptCache() *AttemptCache {
	return &AttemptCache{seen: map[string]time.Time{}}
}

// Mark records an attempt as served, returning false if it was already marked and has not
// expired. Expired entries (and any others past their deadline) are pruned on the way.
func (c *AttemptCache) Mark(attemptID string, deadline time.Time) bool {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, exp := range c.seen {
		if now.After(exp) {
			delete(c.seen, id)
		}
	}
	if exp, ok := c.seen[attemptID]; ok && now.Before(exp) {
		return false
	}
	c.seen[attemptID] = deadline
	return true
}

// ServeSealed serves one Topology-2 job: open the sealed request, verify the edge grant
// against the plaintext, serve, sign a token receipt, and seal the result to the consumer.
// Its shape is exactly the towerhub Executor seam: (resultEnvelope, receipt, failure), where
// a failure carries no receipt - a refusal is not a result and must never settle one.
func (e EdgeExecutor) ServeSealed(ctx context.Context, grantRaw, sealedReq []byte) (resultEnvelope, receipt []byte, failure string) {
	if len(e.CoreKey) == 0 {
		// FAIL CLOSED, same as every other serve: a Station with no pinned Core key cannot tell
		// a real grant from one the tower wrote.
		return nil, nil, "this Station has no pinned Roger Core key, so it cannot verify a grant"
	}
	// The attempt id is read UNVERIFIED first, only as the envelope's additional data - a wrong
	// value means the envelope will not open, which is a refusal, not a bypass. The grant's real
	// verification happens below against the plaintext it protects. (Same order as execute.go.)
	attemptID := attemptOf(grantRaw)
	sealed, err := envelope.Parse(sealedReq)
	if err != nil {
		return nil, nil, err.Error()
	}
	request, err := envelope.OpenWith(e.Station.SessionPriv(), sealed, attemptID)
	if err != nil {
		// Sealed to somebody else, or for another attempt: either way this Station cannot read
		// it, and saying no more than that leaks nothing.
		return nil, nil, "this request is not sealed to this Station"
	}
	// ONE definition of a valid edge grant (dispatch.ParseEdgeGrant): Core's signature, THIS
	// Station, the deadline, and the input ceiling against the request's true plaintext size.
	grant, err := dispatch.ParseEdgeGrant(grantRaw, e.CoreKey, e.Network, e.Station.StationID,
		request, e.now())
	if err != nil {
		if errors.Is(err, dispatch.ErrExpired) {
			return nil, nil, "this grant has expired"
		}
		return nil, nil, err.Error()
	}
	// THE SEALING KEY IS REQUIRED HERE. On this path the answer travels back through a blind
	// tower; without a consumer key there is nobody to seal it to, and returning plaintext
	// would hand the payload to the relay this whole design exists to blind. Refuse rather
	// than degrade.
	if len(grant.ConsumerEnvKey) == 0 {
		return nil, nil, "this grant carries no consumer sealing key, and the sealed path returns nothing readable without one"
	}
	if e.Upstream == nil {
		return nil, nil, "this Station has no upstream model configured"
	}
	// ONE SERVE PER ATTEMPT AT THE NODE TOO. The tower holds the grant + envelope verbatim and
	// hosts the hub, so nothing upstream stops it re-injecting a completed job to burn this
	// node's compute (Core's one-use settle protects the MONEY, not the work). Mark the attempt
	// before serving; a repeat is refused without a receipt.
	if e.Seen != nil && !e.Seen.Mark(grant.AttemptID, grant.Deadline) {
		return nil, nil, "this attempt was already served"
	}

	body, err := e.Upstream.Serve(ctx, request)
	if err != nil {
		// GENERIC, DELIBERATELY. On this path the failure string crosses the TOWER in the
		// clear (it is the one thing that cannot be sealed - the consumer needs to read it to
		// know why nothing opened). An upstream's own error body can echo fragments of the
		// request (validation errors often quote what they rejected), and err.Error() embeds a
		// slice of that body - so forwarding it would hand consumer plaintext to the exact
		// party this design blinds. The operator still has the full error in their own logs;
		// the wire gets only the class.
		return nil, nil, "the model did not answer"
	}
	// The output ceiling is enforced at the party being paid, exactly as on the TLS edge.
	if int64(len(body)) > grant.MaxOut {
		return nil, nil, "the model returned more than this grant allows"
	}
	// The TOKEN receipt (Option C): byte usage as the tamper-evident wire measure, token usage
	// from the model's own report as the billing basis - clamped downstream to the grant's
	// token ceiling and the tokens<=bytes bound at settlement.
	rec, err := dispatch.SignReceipt(e.Station.assertionPriv, e.Network,
		dispatch.Grant{AttemptID: grant.AttemptID, StationID: grant.StationID}, request, body,
		dispatch.Usage{In: int64(len(request)), Out: int64(len(body))}, tokenUsageOf(body))
	if err != nil {
		return nil, nil, "this Station could not sign its result: " + err.Error()
	}
	// SEAL FIRST, QUEUE SECOND. The outbox copy is what reaches settlement, so it must never
	// exist for a result that failed to seal - or the consumer would be charged for an answer
	// it can never open (a consumer-supplied low-order key passes the length checks at mint and
	// fails only here, after the model ran). Sealing before Add closes that window entirely,
	// and Add still precedes the return, so a consumer that vanishes with its answer still
	// cannot leave the evidence unqueued.
	out, err := envelope.SealTo(grant.ConsumerEnvKey, body, grant.AttemptID)
	if err != nil {
		return nil, nil, "this Station could not seal its result"
	}
	raw, err := out.Marshal()
	if err != nil {
		return nil, nil, "this Station could not encode its result"
	}
	if e.Outbox != nil {
		e.Outbox.Add(Evidence{AttemptID: grant.AttemptID, StationID: grant.StationID,
			Receipt: rec.Signed})
	}
	if e.Transcripts != nil {
		e.Transcripts.Keep(Transcript{AttemptID: grant.AttemptID, Request: request, Response: body})
	}
	return raw, rec.Signed, ""
}
