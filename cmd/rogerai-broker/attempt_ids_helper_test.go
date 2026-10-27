package main

// attempt_ids_helper_test.go: runners that join ledger rows, receipts or lineage back to a
// consumer's X-RogerAI-Request-Id go through the per-attempt ids the broker derives for it
// (contract §14.B7 #13): a receipt, a spend row and a hold are keyed on the attempt id, never on
// the request id itself.

import (
	"encoding/json"
	"fmt"
	"strings"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// maxTestAttempts bounds the attempts a scenario's plan can make (the relay attempt cap and any
// model list the runners drive stay well under it).
const maxTestAttempts = 8

// attemptIDsOf lists the ids of attempts 1..maxTestAttempts of a request.
func attemptIDsOf(b *broker, requestID string) []string {
	out := make([]string, 0, maxTestAttempts)
	for n := 1; n <= maxTestAttempts; n++ {
		out = append(out, b.attemptID(requestID, n))
	}
	return out
}

// isAttemptOf reports whether id (a receipt's request id, or a ledger ref ending in one) names
// an attempt of requestID.
func isAttemptOf(b *broker, id, requestID string) bool {
	if requestID == "" {
		return false
	}
	for _, a := range attemptIDsOf(b, requestID) {
		if id == a || strings.HasSuffix(id, ":"+a) {
			return true
		}
	}
	return false
}

// attemptLineage checks that the given attempt ids (in attempt order) are attempts 1..n of one
// consumer request: the consumer's own rows tie each to the same request id, and each id is the
// one the broker derives for that request and position.
func attemptLineage(b *broker, consumerRows []store.Entry, ids []string) error {
	relay := map[string]string{}
	for _, e := range consumerRows {
		relay[e.RequestID] = e.RelayRequestID
	}
	req := relay[ids[0]]
	if req == "" {
		return fmt.Errorf("the consumer's rows tie attempt %q to no request id", ids[0])
	}
	for i, id := range ids {
		if relay[id] != req {
			return fmt.Errorf("attempt %q belongs to request %q, want %q (ids %v)", id, relay[id], req, ids)
		}
		if want := b.attemptID(req, i+1); id != want {
			return fmt.Errorf("attempt %d of %q is %q, want %q", i+1, req, id, want)
		}
	}
	return nil
}

// storedReceiptOf reads the receipt the broker stored under id (the JSONB column on Postgres, the
// retained receipt in memory). A consumer request id resolves to its first stored attempt.
func storedReceiptOf(b *broker, pg *store.Postgres, mem *store.Mem, id string) (protocol.UsageReceipt, map[string]any, error) {
	read := func(id string) ([]byte, error) {
		if pg != nil {
			var txt string
			if err := pg.DB().QueryRow(`SELECT receipt::text FROM rogerai.receipts WHERE request_id=$1`, id).Scan(&txt); err != nil {
				return nil, fmt.Errorf("receipt %s: %w", id, err)
			}
			return []byte(txt), nil
		}
		rec, ok := mem.ReceiptOf(id)
		if !ok {
			return nil, fmt.Errorf("no stored receipt for %s", id)
		}
		return json.Marshal(rec)
	}
	raw, err := read(id)
	for _, a := range attemptIDsOf(b, id) {
		if err == nil {
			break
		}
		if r, aerr := read(a); aerr == nil {
			raw, err = r, nil
		}
	}
	if err != nil {
		return protocol.UsageReceipt{}, nil, err
	}
	var rec protocol.UsageReceipt
	var keys map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		return rec, nil, err
	}
	if err := json.Unmarshal(raw, &keys); err != nil {
		return rec, nil, err
	}
	return rec, keys, nil
}
