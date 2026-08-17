package towerhub

// audit_answer.go is the NODE's audit loop: on a slow cadence, ask the hub what Core wants,
// look each attempt up in the local transcript store, and answer - with the Station-signed
// transcript when retained, or a truthful "not retained" so Core is not left waiting out a
// deadline on silence.

import (
	"context"
	"encoding/base64"
	"time"
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
// reported and retried next round.
func AnswerAudits(ctx context.Context, c *Client, station string, src TranscriptSource, every time.Duration, onError func(error)) {
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
				reply.Available = true
				reply.Transcript = base64.StdEncoding.EncodeToString(signed)
				reply.Request = base64.StdEncoding.EncodeToString(reqB)
				reply.Response = base64.StdEncoding.EncodeToString(respB)
			}
			if aerr := c.AnswerAudit(ctx, station, reply); aerr != nil {
				report(onError, aerr)
			}
		}
	}
}
