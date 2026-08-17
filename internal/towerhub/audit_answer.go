package towerhub

// audit_answer.go is the NODE's audit loop: on a slow cadence, ask the hub what Core wants,
// look each attempt up in the local transcript store, and answer - with the Station-signed
// transcript when retained, or a truthful "not retained" so Core is not left waiting out a
// deadline on silence.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"rogerai.fm/roger/v5/internal/towercore/envelope"
)

// TranscriptSource yields a Station-signed transcript for an attempt, ok=false when the
// attempt was not retained. station.EdgeExecutor.Transcript adapts to this seam.
type TranscriptSource interface {
	SignedTranscript(attemptID string) (signed, request, response []byte, ok bool, err error)
}

// auditAnswerEvery is the node's audit-poll cadence. Slow on purpose: audits have a
// 30-minute deadline and this loop rides beside the hot job loop, not inside it.
const auditAnswerEvery = 45 * time.Second

// AnswerAudits runs until ctx is done. every <= 0 uses the default cadence. Errors are
// reported and retried next round. coreEnvKey is Roger Core's X25519 envelope key (from the
// same pinned fetch as the grant key): every transcript is SEALED to it, so the tower relays
// audit content exactly as blind as it relays the jobs themselves.
func AnswerAudits(ctx context.Context, c *Client, station string, src TranscriptSource, coreEnvKey []byte, every time.Duration, onError func(error)) {
	if len(coreEnvKey) != 32 {
		report(onError, errors.New("audit answering disabled: no Core envelope key to seal transcripts to"))
		return
	}
	if every <= 0 {
		every = auditAnswerEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		wanted, err := c.AuditWanted(ctx, station)
		if err != nil {
			report(onError, err)
			continue
		}
		for _, attemptID := range wanted {
			reply := TranscriptReply{AttemptID: attemptID}
			signed, reqB, respB, ok, terr := src.SignedTranscript(attemptID)
			if terr != nil {
				report(onError, terr)
				continue // retried next round rather than answered wrong
			}
			if ok {
				bundle, berr := json.Marshal(map[string]string{
					"transcript": base64.StdEncoding.EncodeToString(signed),
					"request":    base64.StdEncoding.EncodeToString(reqB),
					"response":   base64.StdEncoding.EncodeToString(respB),
				})
				if berr != nil {
					report(onError, berr)
					continue
				}
				sealed, serr := envelope.SealTo(coreEnvKey, bundle, attemptID)
				if serr != nil {
					report(onError, serr)
					continue
				}
				raw, merr := sealed.Marshal()
				if merr != nil {
					report(onError, merr)
					continue
				}
				reply.Available = true
				reply.SealedBundle = base64.StdEncoding.EncodeToString(raw)
			}
			if aerr := c.AnswerAudit(ctx, station, reply); aerr != nil {
				report(onError, aerr)
			}
		}
	}
}
