package dispatch

// transcript.go is the Station-signed transcript an audit checks.
//
// Contract: features/tower/edge_dispatch.feature.
//
// # WHY IT IS SIGNED
//
// A transcript travels from the Station to Core through the Tower - the one party that is not
// trusted. If the transcript were unsigned, a Tower could FABRICATE one that does not match
// the receipt and frame the Station for a mismatch it did not commit. So the Station signs the
// transcript with the same assertion key it signed the receipt with, and Core checks that
// signature before it checks anything else. A mismatch on a Station-signed transcript is then
// genuinely the Station's fault: it signed two things that contradict each other.
//
// The Tower can still WITHHOLD a transcript, but that is the "cannot produce" case, and a
// Station that cannot show its work for a sampled attempt is suspect regardless of which party
// dropped it - which is why withholding is not a way to escape an audit, only to fail it.

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"

	"rogerai.fm/roger/v5/internal/towerobj"
)

// TypeTranscript identifies the signed object.
const TypeTranscript = "dispatch.transcript"

// SignedTranscript is a Station's attested record of one attempt's exact bytes.
type SignedTranscript struct {
	AttemptID      string
	RequestDigest  string
	ResponseDigest string
	// Request and Response are the plaintext, for Core to inspect. They are NOT what
	// attribution rests on - the digests are - but they are the point of an audit: the actual
	// content Core never saw at dispatch time.
	Request  []byte
	Response []byte
	Signed   []byte
}

// SignTranscript attests the exact bytes of one attempt.
//
// The signature is over the DIGESTS, not the bytes, so it is the same commitment the receipt
// made - a transcript whose digests match a receipt's is, by construction, a record of the
// same attempt. The bytes ride alongside for Core to read; a bytes-vs-digest disagreement is
// caught by Core re-hashing, below.
func SignTranscript(priv ed25519.PrivateKey, network, attemptID string, request, response []byte) (SignedTranscript, error) {
	if attemptID == "" {
		return SignedTranscript{}, errors.New("a transcript names its attempt")
	}
	tr := SignedTranscript{
		AttemptID:      attemptID,
		RequestDigest:  digestOf(request),
		ResponseDigest: digestOf(response),
		Request:        request,
		Response:       response,
	}
	body, err := json.Marshal(map[string]any{
		"network":         network,
		"type":            TypeTranscript,
		"version":         towerobj.FormatInt(Version),
		"attempt_id":      tr.AttemptID,
		"request_digest":  tr.RequestDigest,
		"response_digest": tr.ResponseDigest,
	})
	if err != nil {
		return SignedTranscript{}, err
	}
	signed, err := towerobj.Sign(priv, network, TypeTranscript, Version, body, "station_sig")
	if err != nil {
		return SignedTranscript{}, err
	}
	tr.Signed = signed
	return tr, nil
}

// AuditResult is what Core concluded from a transcript.
type AuditResult struct {
	// Matches is true when the transcript's signed digests equal the receipt's AND the
	// carried bytes hash to those digests. Only then is the content Core is looking at
	// provably the content both ends signed for.
	Matches bool
	// Reason explains a false Matches, for the record and for a disputing operator.
	Reason string
}

// AuditTranscript checks a Station-signed transcript against what settlement recorded.
//
// It takes the receipt digests rather than re-reading the receipt, because the receipt was
// already verified at settlement and its digests are what Core committed to bill against.
// Three things must hold, in order: the Station really signed this transcript, the transcript
// commits to the same digests the receipt did, and the carried bytes actually hash to those
// digests. Skip any one and a Tower or a Station has room to hand Core content that is not
// what was served.
func AuditTranscript(raw []byte, assertionKey ed25519.PublicKey, network, attemptID,
	receiptRequestDigest, receiptResponseDigest string) (SignedTranscript, AuditResult, error) {

	if err := towerobj.Verify(assertionKey, network, TypeTranscript, Version, raw, "station_sig"); err != nil {
		return SignedTranscript{}, AuditResult{}, fmt.Errorf("this transcript is not signed by the recorded Station key: %w", err)
	}
	var obj struct {
		AttemptID      string `json:"attempt_id"`
		RequestDigest  string `json:"request_digest"`
		ResponseDigest string `json:"response_digest"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return SignedTranscript{}, AuditResult{}, fmt.Errorf("this transcript cannot be read: %w", err)
	}
	if obj.AttemptID != attemptID {
		return SignedTranscript{}, AuditResult{}, fmt.Errorf("this transcript is for attempt %q, not this one", obj.AttemptID)
	}
	tr := SignedTranscript{
		AttemptID: obj.AttemptID, RequestDigest: obj.RequestDigest,
		ResponseDigest: obj.ResponseDigest, Signed: raw,
	}
	// The digests the STATION signed must match what it signed at settlement. A Station that
	// signs one digest in a receipt and a different one in a transcript has attributed the
	// disagreement to itself.
	if obj.RequestDigest != receiptRequestDigest {
		return tr, AuditResult{Reason: "the transcript's request digest is not the one on the receipt"}, nil
	}
	if obj.ResponseDigest != receiptResponseDigest {
		return tr, AuditResult{Reason: "the transcript's response digest is not the one on the receipt"}, nil
	}
	return tr, AuditResult{Matches: true}, nil
}

// VerifyBytes confirms the carried plaintext hashes to the signed digests. Separate from
// AuditTranscript so a caller that only has the object (no bytes yet) can still attribute a
// digest mismatch, and one that has the bytes can additionally confirm the content is real.
func (t SignedTranscript) VerifyBytes(request, response []byte) error {
	if digestOf(request) != t.RequestDigest {
		return errors.New("the transcript's request bytes do not hash to its signed request digest")
	}
	if digestOf(response) != t.ResponseDigest {
		return errors.New("the transcript's response bytes do not hash to its signed response digest")
	}
	return nil
}
