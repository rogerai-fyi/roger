package main

// idempotency.go: the idempotent relay (contract §14.B2, features/routing/idempotency.feature).
// A request carrying `Idempotency-Key` claims (payer, key) in the store before anything costs
// money; a retry with the same key and fingerprint inside the window gets the first outcome
// back (or a 409 while it runs, or a 409 for a committed stream), never a second job, hold or
// settle. The claim is the money guard and lives in the store (Postgres in production); the
// stored response is a replay accelerator in the shared store, with a bounded per-instance
// fallback when the shared store is down.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/store"
)

const (
	idemKeyMax       = 128
	idemBodyMax      = 1 << 20 // a stored outcome body; larger replays as response_too_large_to_replay
	idemKeysPerPayer = 1000
	idemLocalMax     = 1000 // the no-shared-store fallback's bound
	idemLookupRPM    = 600  // the replay-lookup bucket (separate from the relay bucket)
	idemLookupBurst  = 120
)

// idemTTL is ROGERAI_IDEMPOTENCY_TTL (a Go duration, default 10m), counted from the first request.
func idemTTL() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("ROGERAI_IDEMPOTENCY_TTL")); err == nil && d > 0 {
		return d
	}
	return 10 * time.Minute
}

// validIdemKey: 1..128 printable ASCII bytes.
func validIdemKey(k string) bool {
	if k == "" || len(k) > idemKeyMax {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < 0x20 || k[i] > 0x7e {
			return false
		}
	}
	return true
}

// idemRoutingHeaders are the X-Roger-* headers the relay routes on (sorted). The signature
// headers (pubkey, timestamp, nonce, signature) are authentication, not the request: a retry
// re-signs with a fresh timestamp and is still the same request.
var idemRoutingHeaders = []string{"X-Roger-Confidential", "X-Roger-Exclude-Nodes", "X-Roger-Freq", "X-Roger-Max-Price",
	"X-Roger-Max-Price-Out", "X-Roger-Min-Tps", "X-Roger-Node", "X-Roger-Pref"}

// idemFingerprint is sha256 of the exact body bytes plus the routing headers, in a fixed
// order, so a retry that changes either is a different request (422).
func idemFingerprint(body []byte, h http.Header) string {
	sum := sha256.New()
	sum.Write(body)
	for _, k := range idemRoutingHeaders {
		sum.Write([]byte("\x00" + k + ":" + strings.Join(h.Values(k), ",")))
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// idemOutcome is a finished request's response as it is replayed.
type idemOutcome struct {
	Status int                 `json:"s"`
	Header map[string][]string `json:"h"`
	Body   []byte              `json:"b"`
}

type idemLocal struct {
	mu sync.Mutex
	m  map[string]idemOutcome
}

func idemStoreKey(payer, key string) string {
	h := sha256.Sum256([]byte(payer + "\x00" + key))
	return "idem:" + hex.EncodeToString(h[:])
}

func (b *broker) idemSave(payer, key string, o idemOutcome) {
	raw, _ := json.Marshal(o)
	if b.shared != nil && b.shared.cacheSet(idemStoreKey(payer, key), raw, idemTTL()) == nil {
		return
	}
	b.idemLocal.mu.Lock()
	defer b.idemLocal.mu.Unlock()
	if b.idemLocal.m == nil {
		b.idemLocal.m = map[string]idemOutcome{}
	}
	if len(b.idemLocal.m) >= idemLocalMax {
		for k := range b.idemLocal.m {
			delete(b.idemLocal.m, k)
			break
		}
	}
	b.idemLocal.m[idemStoreKey(payer, key)] = o
}

func (b *broker) idemLoad(payer, key string) (idemOutcome, bool) {
	var o idemOutcome
	if b.shared != nil {
		if raw, found, err := b.shared.cacheGet(idemStoreKey(payer, key)); err == nil && found && json.Unmarshal(raw, &o) == nil {
			return o, true
		}
	}
	b.idemLocal.mu.Lock()
	defer b.idemLocal.mu.Unlock()
	o, ok := b.idemLocal.m[idemStoreKey(payer, key)]
	return o, ok
}

// relayWriter stamps X-RogerAI-Attempts on every relay response and, for an idempotent request,
// keeps what it wrote so the outcome can be replayed.
type relayWriter struct {
	http.ResponseWriter
	attempts func() int
	capture  bool
	status   int
	body     bytes.Buffer
	over     bool
}

func (w *relayWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
		if h := w.Header(); h.Get("X-RogerAI-Attempts") == "" {
			h.Set("X-RogerAI-Attempts", strconv.Itoa(w.attempts()))
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *relayWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.capture && !w.over {
		if w.body.Len()+len(p) > idemBodyMax {
			w.over = true
			w.body.Reset()
		} else {
			w.body.Write(p)
		}
	}
	return w.ResponseWriter.Write(p)
}

func (w *relayWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *relayWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// idemBegin runs before any rate token, moderation, hold or dispatch. done=true means the
// request was answered here (a refusal, a 409, a 422 or a replay); otherwise finish (non-nil
// when this request holds the claim) records the outcome once the relay returns.
func (b *broker) idemBegin(w http.ResponseWriter, r *http.Request, rw *relayWriter, body []byte, scope, requestID string) (finish func(), done bool) {
	key := r.Header.Get("Idempotency-Key")
	if !validIdemKey(key) {
		jsonErrCode(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must be 1 to 128 printable ASCII characters")
		return nil, true
	}
	// The lookup has its own bucket: a replay never spends a relay token, and the lookup
	// cannot be used to hammer the store either.
	if ok, retry := b.idemRL.allowAt(scope, idemLookupRPM, idemLookupBurst); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		jsonErr(w, http.StatusTooManyRequests, "rate limit exceeded - slow down")
		return nil, true
	}
	now := b.now()
	fp := idemFingerprint(body, r.Header)
	c := store.IdemClaim{Payer: scope, Key: key, Fingerprint: fp, RequestID: requestID, State: store.IdemInFlight,
		Deadline: now.Add(nonStreamRelayWait).UnixMilli(), Created: now.UnixMilli()}
	cur, claimed, err := b.db.ClaimIdempotency(c, now.Add(-idemTTL()).UnixMilli(), idemKeysPerPayer)
	if err != nil {
		// The claim store is down: serve the request (never blocked by the guard) unguarded.
		log.Printf("idempotency claim unavailable (%v): serving request=%s without a replay guard", err, requestID)
		return nil, false
	}
	if claimed {
		rw.capture = true
		return func() {
			state := store.IdemDone
			switch {
			case strings.HasPrefix(rw.Header().Get("Content-Type"), "text/event-stream") && rw.status == http.StatusOK:
				state = store.IdemStreamed
			case rw.over:
				state = store.IdemTooLarge
			default:
				h := map[string][]string{}
				for k, v := range rw.Header() {
					if strings.HasPrefix(k, "X-Rogerai-") || k == "Content-Type" || k == "Retry-After" {
						h[k] = v
					}
				}
				b.idemSave(scope, key, idemOutcome{Status: rw.status, Header: h, Body: rw.body.Bytes()})
			}
			if err := b.db.FinishIdempotency(scope, key, requestID, state); err != nil {
				log.Printf("idempotency finish request=%s: %v", requestID, err)
			}
		}, false
	}
	w.Header().Set("X-RogerAI-Cost", "0")
	if cur.Fingerprint != fp {
		jsonErrCode(w, http.StatusUnprocessableEntity, "idempotency_key_reused", "this Idempotency-Key was used for a different request")
		return nil, true
	}
	switch cur.State {
	case store.IdemInFlight:
		secs := int(math.Ceil(float64(cur.Deadline-now.UnixMilli()) / 1000))
		w.Header().Set("Retry-After", strconv.Itoa(max(1, secs)))
		jsonErrCode(w, http.StatusConflict, "request_in_flight", "the first request with this Idempotency-Key is still running")
		return nil, true
	case store.IdemStreamed:
		jsonErrCode(w, http.StatusConflict, "stream_not_replayable", "the first request with this Idempotency-Key was a stream that already started; a stream cannot be replayed")
		return nil, true
	case store.IdemTooLarge:
		jsonErrCode(w, http.StatusConflict, "response_too_large_to_replay", "the first response with this Idempotency-Key is too large to replay")
		return nil, true
	}
	o, ok := b.idemLoad(scope, key)
	if !ok {
		// Finished, but its stored response is gone (a shared store that lost it, or another
		// instance's local fallback): the documented limit. Never a second job under one key.
		jsonErrCode(w, http.StatusConflict, "response_too_large_to_replay", "the first response with this Idempotency-Key is no longer available to replay")
		return nil, true
	}
	for k, v := range o.Header {
		w.Header()[k] = v
	}
	w.Header().Set("X-RogerAI-Idempotent-Replay", "true")
	w.WriteHeader(o.Status)
	_, _ = w.Write(o.Body)
	return nil, true
}
