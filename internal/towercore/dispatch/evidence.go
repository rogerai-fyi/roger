package dispatch

// evidence.go is what settlement rests on once Roger Core is out of the data path.
//
// Contract: features/tower/edge_dispatch.feature.
//
// # THE PROBLEM THIS SOLVES
//
// On the relayed path Core counted the bytes itself. It was first-hand observation and there
// was nothing to reconcile. On the edge path Core sees neither the request nor the response,
// so "how much work happened" has to come from somebody who was there - and everybody who
// was there has an interest in the answer.
//
// The Station is paid on its own claim about its own usage. The consumer is billed on it. So
// the two are asked separately and the answer has to survive both:
//
//	the STATION signs a receipt over the exact bytes it received and returned.
//	the CONSUMER signs an acknowledgement over the exact bytes it got.
//
// The Tower sits between them and can forge neither - it holds no key for either party, and
// on the edge path it holds no plaintext to re-hash even if it did. Two independent digests
// with a relay in the middle is the whole detection mechanism for a Tower altering traffic.
//
// # WHY THE LOWER FIGURE WINS
//
// Not an average, and not the Station's. Each party's incentive runs one way: the Station
// gains by reporting more than it spent, the consumer by reporting less than it received.
// Taking the minimum means neither can profit by lying - a Station inflating its count is
// held to the consumer's figure, and a consumer understating theirs only ever pays less than
// they used when the Station happens to agree with them.
//
// # AN ATTEMPT WITH NO ACKNOWLEDGEMENT STILL SETTLES
//
// Deliberately, and it is the decision most likely to look like a hole. Customers close
// laptops mid-stream and third-party clients will never acknowledge at all, so an operator
// who lost money every time is an operator who leaves - and a network with no operators is
// not more secure, it is empty. Such an attempt settles on the receipt alone and is marked
// UNCORROBORATED. The signal is in the rate: a Tower whose uncorroborated share is unlike
// the fleet's is investigated, which is a question about a pattern rather than a punishment
// for one closed laptop.

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"rogerai.fm/roger/v5/internal/towerobj"
)

// TypeAck identifies the consumer's signed statement.
const TypeAck = "dispatch.consumer_ack"

// Usage is what one party observed. Separate from any price: what was spent is a fact two
// parties can disagree about, and what it costs is a decision only Core makes.
type Usage struct {
	In  int64
	Out int64
}

// Ack is the consumer's signed statement about what it actually received.
type Ack struct {
	AttemptID string
	// ResponseDigest is over the exact bytes the consumer read. It is the half of the
	// evidence the Station cannot produce.
	ResponseDigest string
	Usage          Usage
	// FirstByte and Completed are the consumer's own timings. They are not used in
	// settlement - a clock nobody controls is not evidence - but they are what distinguishes
	// a slow Tower from a slow model when an operator disputes a reliability finding.
	FirstByte time.Time
	Completed time.Time
	Signed    []byte
}

// SignAck produces the consumer's acknowledgement.
//
// Signed with the consumer's own key rather than a session token, because the point of this
// object is to be checkable by somebody who was not present when it was made - and because
// it is the one claim in the exchange that Roger Core cannot simply take on trust from the
// party being paid.
func SignAck(priv ed25519.PrivateKey, network, attemptID string, response []byte,
	u Usage, firstByte, completed time.Time) (Ack, error) {

	if attemptID == "" {
		return Ack{}, errors.New("an acknowledgement names the attempt it is for")
	}
	if len(response) == 0 {
		// An acknowledgement of nothing would let any empty response stand in for any other.
		return Ack{}, errors.New("an acknowledgement commits to a response, and there is none")
	}
	if u.In < 0 || u.Out < 0 {
		return Ack{}, errors.New("an acknowledgement cannot report negative usage")
	}
	a := Ack{
		AttemptID:      attemptID,
		ResponseDigest: digestOf(response),
		Usage:          u,
		FirstByte:      firstByte,
		Completed:      completed,
	}
	body, err := json.Marshal(map[string]any{
		"network":         network,
		"type":            TypeAck,
		"version":         towerobj.FormatInt(Version),
		"attempt_id":      a.AttemptID,
		"response_digest": a.ResponseDigest,
		"usage_in":        towerobj.FormatInt(a.Usage.In),
		"usage_out":       towerobj.FormatInt(a.Usage.Out),
		"first_byte":      towerobj.FormatInt(firstByte.Unix()),
		"completed":       towerobj.FormatInt(completed.Unix()),
	})
	if err != nil {
		return Ack{}, err
	}
	signed, err := towerobj.Sign(priv, network, TypeAck, Version, body, "consumer_sig")
	if err != nil {
		return Ack{}, err
	}
	a.Signed = signed
	return a, nil
}

// ParseAck verifies an acknowledgement came from the consumer it claims to.
func ParseAck(raw []byte, consumerKey ed25519.PublicKey, network, attemptID string) (Ack, error) {
	if err := towerobj.Verify(consumerKey, network, TypeAck, Version, raw, "consumer_sig"); err != nil {
		return Ack{}, fmt.Errorf("this acknowledgement is not signed by the consumer: %w", err)
	}
	var obj struct {
		AttemptID      string `json:"attempt_id"`
		ResponseDigest string `json:"response_digest"`
		UsageIn        string `json:"usage_in"`
		UsageOut       string `json:"usage_out"`
		FirstByte      string `json:"first_byte"`
		Completed      string `json:"completed"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return Ack{}, fmt.Errorf("this acknowledgement cannot be read: %w", err)
	}
	// An acknowledgement for a DIFFERENT attempt is a real signature over a real statement
	// about other work. Without this check it would corroborate whatever it was filed against.
	if obj.AttemptID != attemptID {
		return Ack{}, fmt.Errorf("this acknowledgement is for attempt %q, not this one", obj.AttemptID)
	}
	in, err := strconv.ParseInt(obj.UsageIn, 10, 64)
	if err != nil {
		return Ack{}, errors.New("this acknowledgement's input usage is not a number")
	}
	out, err := strconv.ParseInt(obj.UsageOut, 10, 64)
	if err != nil {
		return Ack{}, errors.New("this acknowledgement's output usage is not a number")
	}
	if in < 0 || out < 0 {
		return Ack{}, errors.New("this acknowledgement reports negative usage")
	}
	fb, err := strconv.ParseInt(obj.FirstByte, 10, 64)
	if err != nil {
		return Ack{}, errors.New("this acknowledgement's first-byte time is not a time")
	}
	done, err := strconv.ParseInt(obj.Completed, 10, 64)
	if err != nil {
		return Ack{}, errors.New("this acknowledgement's completion time is not a time")
	}
	return Ack{
		AttemptID: obj.AttemptID, ResponseDigest: obj.ResponseDigest,
		Usage:     Usage{In: in, Out: out},
		FirstByte: time.Unix(fb, 0), Completed: time.Unix(done, 0), Signed: raw,
	}, nil
}

// Settlement is what Core concluded about one edge attempt.
type Settlement struct {
	AttemptID string
	// Billable is the usage the account is charged and the operator credited for.
	Billable Usage
	// Corroborated is false when no acknowledgement arrived. Not a failure - see the file
	// comment - but it is carried through to settlement so a rate can be computed from it.
	Corroborated bool
}

// ErrDigestMismatch is the one disagreement that is not a rounding difference: the Station
// and the consumer have signed for DIFFERENT BYTES, and the only party between them is the
// relay. Attributable, and refused rather than settled at the lower figure.
var ErrDigestMismatch = errors.New("the Station and the consumer signed for different responses")

// Reconcile turns the evidence into what Core will act on.
//
// The receipt is required and the acknowledgement is not, which is the asymmetry the whole
// design rests on: the Station is the party being paid and must always have signed for its
// work, while the consumer is a party that may simply have gone away.
//
// The Station's claimed usage comes FROM THE RECEIPT, never as a separate argument. An
// earlier version took it as a parameter, and its one caller filled it from the Tower's POST
// body - which handed the party being audited the pen that writes the number being audited.
func Reconcile(receipt Receipt, ack *Ack) (Settlement, error) {
	if receipt.AttemptID == "" {
		return Settlement{}, errors.New("a settlement needs the Station's receipt")
	}
	claimed := receipt.Usage
	if claimed.In < 0 || claimed.Out < 0 {
		return Settlement{}, errors.New("the Station's receipt reports negative usage")
	}
	s := Settlement{AttemptID: receipt.AttemptID, Billable: claimed}
	if ack == nil {
		// Settles on the receipt alone, and says so. See the file comment for why this is not
		// treated as a fault.
		return s, nil
	}
	if ack.AttemptID != receipt.AttemptID {
		return Settlement{}, fmt.Errorf("this acknowledgement is for attempt %q and the receipt for %q",
			ack.AttemptID, receipt.AttemptID)
	}
	if ack.ResponseDigest != receipt.ResponseDigest {
		return Settlement{}, ErrDigestMismatch
	}
	s.Corroborated = true
	s.Billable = Usage{In: minInt64(claimed.In, ack.Usage.In), Out: minInt64(claimed.Out, ack.Usage.Out)}
	return s, nil
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// ParseReceipt is the verifying side of SignReceipt, for a caller holding the receipt
// WITHOUT the response bytes.
//
// That is the edge path's situation and it is new: on the relayed path Core has the answer in
// hand and Registry.Settle checks the digest against it. Here Core never sees the response, so
// the most it can establish is that this Station really signed this statement about this
// attempt. The digest inside is then checked against the CONSUMER's, in Reconcile - which is
// the whole reason a second signed claim exists.
func ParseReceipt(raw []byte, assertionKey ed25519.PublicKey, network, attemptID, stationID string) (Receipt, error) {
	if err := towerobj.Verify(assertionKey, network, TypeReceipt, Version, raw, "station_sig"); err != nil {
		return Receipt{}, fmt.Errorf("this receipt is not signed by the recorded Station key: %w", err)
	}
	var obj struct {
		AttemptID      string `json:"attempt_id"`
		StationID      string `json:"station_id"`
		RequestDigest  string `json:"request_digest"`
		ResponseDigest string `json:"response_digest"`
		UsageIn        string `json:"usage_in"`
		UsageOut       string `json:"usage_out"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return Receipt{}, fmt.Errorf("this receipt cannot be read: %w", err)
	}
	// A perfectly signed receipt for a DIFFERENT attempt is a context mismatch. Without this
	// a valid result for one attempt would settle another.
	if obj.AttemptID != attemptID {
		return Receipt{}, fmt.Errorf("this receipt is for attempt %q, not this one", obj.AttemptID)
	}
	if obj.StationID != stationID {
		return Receipt{}, fmt.Errorf("this receipt is from Station %q, not this one", obj.StationID)
	}
	if obj.ResponseDigest == "" {
		// A receipt committing to nothing would corroborate any answer at all.
		return Receipt{}, errors.New("this receipt commits to no response")
	}
	// USAGE IS REQUIRED HERE. This parser serves the edge path, where the receipt's own
	// figure is what the Station is paid on; a receipt without one would have to be settled
	// at a number somebody else supplied, and the only somebody in the path is the relay.
	in, err := strconv.ParseInt(obj.UsageIn, 10, 64)
	if err != nil {
		return Receipt{}, errors.New("this receipt's input usage is missing or not a number")
	}
	out, err := strconv.ParseInt(obj.UsageOut, 10, 64)
	if err != nil {
		return Receipt{}, errors.New("this receipt's output usage is missing or not a number")
	}
	if in < 0 || out < 0 {
		return Receipt{}, errors.New("this receipt claims negative usage")
	}
	return Receipt{AttemptID: obj.AttemptID, RequestDigest: obj.RequestDigest,
		ResponseDigest: obj.ResponseDigest, Usage: Usage{In: in, Out: out}, Signed: raw}, nil
}
