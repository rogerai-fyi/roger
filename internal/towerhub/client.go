package towerhub

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"rogerai.fm/roger/v6/internal/protocol"
)

// Client speaks the tower hub's HTTP protocol from the other two sides: a serving NODE (Poll +
// Complete, authenticated by SIGNING each request with its Station's assertion key) and a
// CONSUMER (Submit, presenting a Core-signed grant). It is the counterpart to Server and shares
// the wire types with it, so the two cannot drift. Everything it carries - grant, sealed
// request, sealed result, receipt - is opaque bytes; the Client no more reads content than the
// tower does.
type Client struct {
	// BaseURL is the tower's hub root; endpoint sub-paths (PathSubmit/PathPoll/PathComplete) are
	// appended to it.
	BaseURL string
	// TowerID is the hub this client signs FOR: Core names it in the attach response, and it
	// rides in the target of every signed request so the signature is good at this hub and
	// nowhere else (see nodeauth.go). Empty on the consumer side, which signs nothing, and
	// empty against a Server that has no id of its own - the two are compared with plain
	// equality, so they have to agree.
	TowerID string
	// Sign authenticates each NODE-side call. Nil for a consumer, which authorizes with a grant
	// instead and has no Station identity to sign as.
	//
	// THERE IS NO TOKEN FIELD ANY MORE, and its absence is the fix. The node used to hold a
	// reusable per-Station bearer and put it in an Authorization header on every long poll,
	// over a channel that is plaintext by construction (see nodeauth.go). A current node never
	// transmits a reusable credential at all: it proves possession of the key its receipts are
	// already signed with, per request, and what an on-path attacker captures authenticates
	// nothing a second time.
	//
	// A hub built before this still expects the token, so a current node cannot serve an
	// out-of-date tower. That is deliberate - the alternative is a downgrade any on-path
	// attacker could provoke by answering 401 - and internal/agent surfaces the refusal on the
	// notice channel rather than retrying into silence.
	Sign Signer
	// HTTP is the client used for requests; nil means http.DefaultClient. A node should set a
	// timeout longer than the tower's poll TTL so a long poll is not cut short. Its redirect
	// policy is replaced - see httpClient.
	HTTP *http.Client

	// TowerKeyHash is the fingerprint of the TOWER IDENTITY KEY Core admitted this relay
	// under - hex sha256 of the raw Ed25519 public key, exactly the string Core keeps in its
	// admission registry and hands the node in the attach response. It is what makes the epoch
	// below the HUB'S value rather than the ATTACKER'S; see epochFrom.
	//
	// A node-side Client without one cannot learn an epoch at all, and that refusal is
	// deliberate rather than a gap to fill in later: the whole point is that the epoch may not
	// be adopted on the word of an unauthenticated 401, and "we could not check, so we believed
	// it" is that 401 with an extra step. A consumer-side Client (Sign == nil) never signs and
	// never learns, so it needs none.
	TowerKeyHash string

	// once/http build the effective client exactly once, so the redirect policy below is
	// installed without mutating a *http.Client the caller may be sharing (which would be a
	// data race between the poll workers and the audit loop).
	once sync.Once
	http *http.Client

	// epochMu/epoch cache the hub PROCESS this client is currently signing for. The tower id
	// comes from Core; the epoch cannot, because Core knows nothing about when a tower last
	// restarted (see Server.epoch). So it is learned from the hub itself: the first request
	// carries none, the hub refuses it and names its epoch in HubEpochHeader, and signedDo
	// re-signs and sends once more. That costs one extra round trip per hub restart, against
	// eight poll workers each polling every twenty-five seconds - and it is what makes a
	// signature captured before a redeploy worthless after one.
	//
	// A mutex rather than an atomic because the workers write it concurrently on the same
	// restart and a torn read would send an epoch nobody minted.
	//
	// retired is every epoch this client has MOVED OFF, newest last and bounded. It exists
	// because an epoch is 128 bits of crypto/rand minted once per process, so a hub that has
	// restarted can never name an epoch this client abandoned - only a SECOND LIVE PROCESS can.
	// See adoptEpoch: coming back to a retired epoch is therefore proof of the one deployment
	// this design does not support, with no false positive available to anybody, and the client
	// stops rather than keeps flapping.
	epochMu sync.RWMutex
	epoch   string
	retired []string
}

// maxRetiredEpochs bounds the abandoned-epoch memory. A client that legitimately moves epoch
// does so once per hub redeploy; eight is a fortnight of daily deploys and costs 8 x 32 bytes.
const maxRetiredEpochs = 8

// ErrHubEpochUnproved is a hub epoch this client cannot attribute to the relay Core named.
//
// It is an ERROR RATHER THAN A SHRUG because of what the alternative costs. The epoch rides in
// the SIGNED target, so adopting one means emitting a genuine Ed25519 signature over a target
// naming it - with a fresh nonce and a fresh timestamp, bytes no hub has ever seen and no nonce
// ring has recorded. Believing an unauthenticated 401 therefore turns this node into a signing
// oracle for whatever epoch the party in front of it names. Refusing costs a poll; believing
// costs a signature the node did not choose to make.
var ErrHubEpochUnproved = errors.New(
	"this relay named a new hub epoch it could not prove: the epoch a node signs over must be " +
		"corroborated with the relay's admitted identity key, and this one was not, so it is " +
		"refused rather than signed over")

// ErrHubMultipleProcesses reports an endpoint answered by more than one live hub process.
//
// See Client.retired for why the detection is exact. A tower is documented as running exactly
// one hub process per endpoint (docs/relay-selection-design.md section 5) because the replay
// gate is per process and in memory; two of them behind a load balancer make this client flap
// between their epochs, and every request that lands on the process it did not sign for
// MANUFACTURES a genuine, unconsumed signature for the other one - readable in the clear and
// replayable there. Stopping is the fail-closed answer, and saying so is how the operator finds
// out their deployment is the unsupported one.
var ErrHubMultipleProcesses = errors.New(
	"this relay endpoint is answered by more than one live hub process, which is an unsupported " +
		"deployment: the replay gate is per process, so a request signed for one of them is " +
		"refused by the other and left unconsumed for anyone watching the link to replay. " +
		"This node has stopped signing for it rather than keep flapping between them")

// hubEpoch reads the cached process epoch.
func (c *Client) hubEpoch() string {
	c.epochMu.RLock()
	defer c.epochMu.RUnlock()
	return c.epoch
}

// adoptEpoch moves this client onto a hub epoch it has already PROVED (see epochFrom), retiring
// the one it was using. It is idempotent: adopting the value already held is a no-op, which is
// what makes two workers learning the same restart cost one adoption rather than a race.
//
// It refuses an epoch this client previously moved off, which is the passive half of the
// two-process defect - see ErrHubMultipleProcesses.
func (c *Client) adoptEpoch(fresh string) error {
	if fresh == "" {
		return nil
	}
	c.epochMu.Lock()
	defer c.epochMu.Unlock()
	if c.epoch == fresh {
		return nil
	}
	for _, old := range c.retired {
		if old == fresh {
			return ErrHubMultipleProcesses
		}
	}
	if c.epoch != "" {
		c.retired = append(c.retired, c.epoch)
		if len(c.retired) > maxRetiredEpochs {
			c.retired = c.retired[len(c.retired)-maxRetiredEpochs:]
		}
	}
	c.epoch = fresh
	return nil
}

// epochFrom reads the epoch a hub named on a refusal and PROVES it belongs to the relay Core
// placed this node on, or refuses to read it at all.
//
// # THE CHECK WAS SOUND AND THE PROVENANCE WAS NOT
//
// Binding the hub process into the signed target closes the redeploy replay (see nodeauth.go),
// and the hub-side check of that binding is exact. What was missing is on this side: the value
// being checked arrived on an UNAUTHENTICATED 401 over a channel that is plaintext by
// construction. Anyone on the path could answer a poll with a forged "401 +
// X-Roger-Hub-Epoch: <anything>" and this client would cache it and re-sign - producing a
// genuine signature over an epoch of the attacker's choosing, with a fresh nonce and a fresh
// timestamp, which is bytes no hub has seen and therefore an UNCONSUMED signature rather than
// a replay. Everything the epoch bought was conditional on that 401 being honest.
//
// # WHY THE RELAY'S OWN IDENTITY KEY, AND NOT TLS
//
// TLS is the complete answer and it is a separate, later change (option A in section 5.2 of
// docs/relay-selection-design.md); making this fix wait on it would leave the hole open for
// the sake of tidiness. The material for a narrower answer is already in every node's hands:
// Core ADMITTED this tower under an Ed25519 identity key, keeps its fingerprint in the
// admission registry, verifies every one of the tower's own requests against it, and now hands
// that fingerprint to the node in the attach response. The tower holds the private half and
// signs its epoch with it. So the node checks the epoch against a key it got from Core - the
// party it already trusts for the tower id, the endpoint and the grant key - rather than
// against the word of whoever answered the socket.
//
// # THE PROOF BINDS THIS REQUEST'S NONCE, WHICH IS WHAT MAKES IT A CHALLENGE
//
// A signature over (tower, epoch) alone would be a bearer token for an epoch: captured once
// before a redeploy, it would let an on-path attacker point a node back at a dead epoch
// whenever it liked. The nonce this client minted for THIS request is in the statement, so a
// proof is good for one request and cannot be stockpiled. What an on-path attacker can still
// do is RELAY - forward the node's own request to a hub and hand back that hub's genuine
// answer - which is why this closes the forge-any-epoch attack outright and leaves the
// two-live-processes case to ErrHubMultipleProcesses above.
//
// It returns ("", nil) when there is nothing new to learn, so the ordinary 401 - an unknown
// Station, a bad signature, a hub that names the epoch this client already signed with - costs
// no cryptography at all.
func (c *Client) epochFrom(resp *http.Response, sentEpoch, nonce string) (string, error) {
	fresh := resp.Header.Get(HubEpochHeader)
	if fresh == "" || fresh == sentEpoch {
		return "", nil
	}
	if c.Sign == nil {
		return "", nil // a consumer signs nothing, so it has no epoch to be wrong about
	}
	if c.TowerKeyHash == "" {
		return "", ErrHubEpochUnproved
	}
	keyHex := strings.TrimSpace(resp.Header.Get(HubKeyHeader))
	raw, err := hex.DecodeString(keyHex)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return "", ErrHubEpochUnproved
	}
	sum := sha256.Sum256(raw)
	// Not constant time, and it must not be mistaken for a secret comparison: both sides of
	// this are public material (a public key and its published fingerprint), and the thing an
	// attacker lacks is the private half, not knowledge of the hash.
	if !strings.EqualFold(hex.EncodeToString(sum[:]), c.TowerKeyHash) {
		return "", ErrHubEpochUnproved
	}
	sig, err := hex.DecodeString(strings.TrimSpace(resp.Header.Get(HubProofHeader)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return "", ErrHubEpochUnproved
	}
	if !ed25519.Verify(ed25519.PublicKey(raw), hubEpochStatement(c.TowerID, fresh, nonce), sig) {
		return "", ErrHubEpochUnproved
	}
	return fresh, nil
}

// signedDo is every NODE-side call: build the target, sign it, send it, and - if the hub says
// the signature was made for a different run of itself AND PROVES that claim - learn the new
// epoch and send exactly one more.
//
// ONE RETRY, NOT A LOOP. The retry is triggered only by a proved epoch that differs from the
// one this attempt actually SENT, and the second attempt never retries, so the worst case is
// two requests per call however a hub answers.
//
// THE TRIGGER IS "DIFFERENT FROM WHAT WAS SENT", NOT "DIFFERENT FROM WHAT WAS CACHED", and the
// distinction is the whole of a defect worth naming. The old code retried only when the value
// was new to the CACHE, so on a hub restart the first worker to notice learned the epoch and
// retried while every other worker - which had already sent the stale epoch and got the same
// 401 - was told "nothing new" and hard-failed. Measured at three of four workers failing a
// single epoch change, each turning into a 2s backoff plus an ErrHubRefusedThisNode notice: an
// operator-facing "your relay refuses this node's identity" alarm on every routine redeploy,
// fired on the one channel deliberately designed not to be discardable. A worker's retry
// decision has to be about its OWN request.
//
// AND THE SECOND RESPONSE IS LEARNED FROM TOO. A hub that restarts between the two attempts
// answers the retry with a third epoch; not reading it meant the next call started from a value
// already known to be stale and burned its retry rediscovering that. No third attempt is made -
// the caller's own poll loop is the retry - but the cache is left correct.
//
// The body is a []byte rather than a reader precisely so the second attempt can send the same
// bytes; the signature covers their digest, so re-reading a stream would not do.
func (c *Client) signedDo(ctx context.Context, method, path string, q url.Values, body []byte) (*http.Response, error) {
	attempt := func() (sent, nonce string, resp *http.Response, err error) {
		sent = c.hubEpoch()
		vals := cloneValues(q)
		target := hubTarget(c.TowerID, sent, path, vals)
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, rerr := http.NewRequestWithContext(ctx, method, c.url(target), rdr)
		if rerr != nil {
			return sent, "", nil, rerr
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		c.authenticate(req, target, body)
		resp, err = c.httpClient().Do(req)
		// The nonce hubTarget minted is read back off the values it wrote it into, so the
		// challenge the proof must answer is the one that actually went on the wire rather
		// than a second guess at it.
		return sent, vals.Get(nonceParam), resp, err
	}
	drain := func(resp *http.Response) {
		// The refused response is drained and closed before the retry: leaving it open leaks a
		// connection per restart per worker, on a client whose whole job is to hold long polls.
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
	}
	sent, nonce, resp, err := attempt()
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	fresh, ferr := c.epochFrom(resp, sent, nonce)
	if ferr != nil {
		drain(resp)
		return nil, ferr
	}
	if fresh == "" {
		return resp, nil
	}
	if aerr := c.adoptEpoch(fresh); aerr != nil {
		drain(resp)
		return nil, aerr
	}
	drain(resp)
	sent2, nonce2, resp2, err := attempt()
	if err != nil || resp2.StatusCode != http.StatusUnauthorized {
		return resp2, err
	}
	if fresh2, ferr2 := c.epochFrom(resp2, sent2, nonce2); ferr2 == nil && fresh2 != "" {
		if aerr := c.adoptEpoch(fresh2); aerr != nil {
			drain(resp2)
			return nil, aerr
		}
	}
	return resp2, nil
}

// cloneValues copies a caller's query so hubTarget's per-request additions (a fresh nonce, the
// tower, the epoch) never mutate a map the caller reuses - which, on the retry above, would
// otherwise carry the FIRST attempt's nonce into the second.
func cloneValues(q url.Values) url.Values {
	out := make(url.Values, len(q)+3)
	for k, v := range q {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// httpClient is the client every call here uses: the caller's, with REDIRECTS REFUSED.
//
// It used to be the caller's client verbatim, which meant no CheckRedirect at all - unlike every
// broker call the node makes, which all pass protocol.NoDowngradeRedirect. That gap mattered
// most when the request this Client makes most often was a long poll carrying a reusable bearer
// token, which Go's default policy would have carried wherever the answering party pointed it.
//
// IT STILL MATTERS NOW THAT THE TOKEN IS GONE. A signature binds the method, the target and the
// body - not the HOST - so a hub that redirected a poll to a machine of its choosing would be
// handing that machine a signature it could present to the real hub. Refusing outright is what
// keeps "the signature is only good for the request it was made for" true of the destination
// too.
//
// STRICTER THAN NoDowngradeRedirect, on purpose. That policy exists for the BROKER, a party the
// node trusts, and it permits a redirect as long as the destination is not a plaintext downgrade
// - which still lets the redirecting party name any https host it likes. A tower is explicitly an
// UNTRUSTED party in this design; the whole sealed envelope exists because it is. And no hub has
// any legitimate reason to redirect a poll: the endpoint the node uses was handed to it by Core,
// not negotiated with the relay. So the answer is no, rather than "no, unless the relay picks a
// destination we happen to like".
func (c *Client) httpClient() *http.Client {
	c.once.Do(func() {
		base := c.HTTP
		if base == nil {
			base = http.DefaultClient
		}
		cp := *base
		cp.CheckRedirect = refuseRedirect
		c.http = &cp
	})
	return c.http
}

func refuseRedirect(req *http.Request, _ []*http.Request) error {
	return fmt.Errorf("the relay hub tried to redirect this request to %s - refusing: "+
		"a relay does not get to choose where this node sends its signed requests", req.URL.Redacted())
}

func (c *Client) url(target string) string {
	return strings.TrimRight(c.BaseURL, "/") + target
}

// authenticate signs one node-side request in place. target must be the path AND query exactly
// as they will be sent - hubTarget builds both from one place so they cannot disagree.
//
// A nil Signer is not an error here: the consumer side of this Client (SubmitJob) has no Station
// identity, and a node without one is refused by the hub with a sentence rather than by a panic
// three layers from the cause.
func (c *Client) authenticate(req *http.Request, target string, body []byte) {
	if c.Sign == nil {
		return
	}
	pub, ts, sig := c.Sign(req.Method, target, body)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	req.Header.Set(protocol.HeaderSig, sig)
	// AND THE DOOR SIGNATURE, which is the same key over the same method and target with NO
	// BODY. It is what lets the hub establish possession of this key before it has read a byte
	// of the body - which on /complete and /audit/transcript it cannot do with the signature
	// above, because that one covers a digest of bytes that have not arrived yet. See
	// HeaderDoorSig for why the public key alone could not be the admission credential.
	//
	// Sent on every signed call rather than only the two that need it: a per-route exemption is
	// a trap for whoever adds the next route, the same argument the nonce applies to every route
	// rather than only to the one that dequeues. On a GET it costs one signature over four
	// dozen bytes.
	_, dts, dsig := c.Sign(doorMethod(req.Method), target, nil)
	req.Header.Set(HeaderDoorTS, strconv.FormatInt(dts, 10))
	req.Header.Set(HeaderDoorSig, dsig)
}

// SubmitJob is the CONSUMER side: hand the tower a Core-signed grant + a request sealed to the
// serving node, and block until the node answers (or the request context / tower TTL fires). It
// returns the sealed result + node receipt. A non-2xx is returned as an error carrying the
// status, so a caller can distinguish 402/403/404/409/504.
func (c *Client) SubmitJob(ctx context.Context, grant, envelope []byte) (Result, error) {
	body, _ := json.Marshal(submitReq{
		Grant:    base64.StdEncoding.EncodeToString(grant),
		Envelope: base64.StdEncoding.EncodeToString(envelope),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(PathSubmit), bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != http.StatusOK {
		return Result{}, &HTTPError{Status: resp.StatusCode, Body: errSnippet(raw)}
	}
	var out submitResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return Result{}, fmt.Errorf("unreadable submit response: %w", err)
	}
	env, err := base64.StdEncoding.DecodeString(out.Envelope)
	if err != nil {
		return Result{}, fmt.Errorf("result envelope is not valid base64: %w", err)
	}
	rec, err := base64.StdEncoding.DecodeString(out.Receipt)
	if err != nil {
		return Result{}, fmt.Errorf("result receipt is not valid base64: %w", err)
	}
	return Result{Envelope: env, Receipt: rec, Failure: out.Failure}, nil
}

// PollJob is the NODE side: long-poll for one job for `station`. ok=false with a nil error means
// the poll returned empty (a normal timeout - poll again). An error is a transport/auth failure.
func (c *Client) PollJob(ctx context.Context, station string) (Job, bool, error) {
	resp, err := c.signedDo(ctx, http.MethodGet, PathPoll, url.Values{"station": {station}}, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return Job{}, false, nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != http.StatusOK {
		return Job{}, false, &HTTPError{Status: resp.StatusCode, Body: errSnippet(raw)}
	}
	var pr pollResp
	if err := json.Unmarshal(raw, &pr); err != nil {
		return Job{}, false, fmt.Errorf("unreadable poll response: %w", err)
	}
	grant, err := base64.StdEncoding.DecodeString(pr.Grant)
	if err != nil {
		return Job{}, false, fmt.Errorf("job grant is not valid base64: %w", err)
	}
	env, err := base64.StdEncoding.DecodeString(pr.Envelope)
	if err != nil {
		return Job{}, false, fmt.Errorf("job envelope is not valid base64: %w", err)
	}
	return Job{AttemptID: pr.AttemptID, StationID: pr.StationID, Grant: grant, Envelope: env}, true, nil
}

// CompleteResult is the NODE side: return a sealed result + receipt for an attempt it served.
// station is the Station it is authenticated for; the tower binds the completion to it.
func (c *Client) CompleteResult(ctx context.Context, station string, res Result) error {
	body, _ := json.Marshal(completeReq{
		AttemptID: res.AttemptID,
		StationID: station,
		Envelope:  base64.StdEncoding.EncodeToString(res.Envelope),
		Receipt:   base64.StdEncoding.EncodeToString(res.Receipt),
		Failure:   res.Failure,
	})
	resp, err := c.signedDo(ctx, http.MethodPost, PathComplete, url.Values{}, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusAccepted {
		// The hub took the result but has no dispatch record, so the receipt was NOT couriered
		// for settlement (audit H-2: typically a hub restart between poll and complete). The
		// work is done and delivered where possible - but the pay is at risk, and the caller
		// deserves to know loudly rather than see a quiet 200.
		return ErrNotCarried
	}
	if resp.StatusCode != http.StatusOK {
		return &HTTPError{Status: resp.StatusCode, Body: errSnippet(raw)}
	}
	return nil
}

// ErrResultUndelivered marks a completion the node SERVED but could not hand back: the hub was
// unreachable, or refused it, between the serve and the return. It is wrapped around whatever
// the transport said so a caller can branch on it.
//
// It is separated from an ordinary poll blip because the two cost the operator very different
// things. A failed poll costs nothing - there was no work. A failed complete means the GPU time
// was spent, the answer exists, and nobody will ever be billed for it or pay for it. That is the
// same class of event as ErrNotCarried and belongs on the same channel.
var ErrResultUndelivered = errors.New("this attempt was served but its result could not be returned to the hub")

// ErrNotCarried reports a completion the hub accepted but did not courier for settlement -
// the serving node's receipt did not start its ride to Core. The consumer is NOT charged (no
// settle ever runs); their pre-auth hold releases via Core's orphan-hold sweep.
var ErrNotCarried = errors.New("the hub accepted this completion but did not forward the receipt for settlement " +
	"(no dispatch record - likely a hub restart mid-job); this attempt's pay is at risk")

// errSnippet bounds and sanitizes TOWER-CONTROLLED error text before it rides an error a
// caller may print: a hostile hub must not inject megabytes or terminal escapes.
func errSnippet(raw []byte) string {
	const maxErrBody = 2048
	if len(raw) > maxErrBody {
		raw = raw[:maxErrBody]
	}
	b := make([]byte, 0, len(raw))
	for _, c := range raw {
		if c == '\n' || c == '\t' || (c >= 0x20 && c != 0x7f) {
			b = append(b, c)
		} else {
			b = append(b, ' ')
		}
	}
	return string(b)
}

// HTTPError carries a non-2xx status from the hub so callers can branch on it.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("tower hub returned %d: %s", e.Status, e.Body)
}

// AuditWanted is the NODE side of the audit plane: fetch the attempt ids Core wants this
// Station's transcripts for (relayed by the tower's hub).
func (c *Client) AuditWanted(ctx context.Context, station string) ([]string, error) {
	resp, err := c.signedDo(ctx, http.MethodGet, PathAuditWanted, url.Values{"station": {station}}, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{Status: resp.StatusCode, Body: errSnippet(raw)}
	}
	var out struct {
		Wanted []string `json:"wanted"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("unreadable wanted response: %w", err)
	}
	return out.Wanted, nil
}

// AnswerAudit uploads one Station-signed transcript (or a truthful "not retained") for a
// wanted attempt. The tower forwards it to Core; withholding is itself a finding, so an
// honest node answers everything on its list.
func (c *Client) AnswerAudit(ctx context.Context, station string, reply TranscriptReply) error {
	body, _ := json.Marshal(struct {
		StationID string `json:"station_id"`
		TranscriptReply
	}{StationID: station, TranscriptReply: reply})
	resp, err := c.signedDo(ctx, http.MethodPost, PathAuditTranscript, url.Values{}, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return &HTTPError{Status: resp.StatusCode, Body: errSnippet(raw)}
	}
	return nil
}
