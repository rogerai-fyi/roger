package agent

// tower.go is `roger share` serving THROUGH A TOWER (Option C, Topology 2) - the capability
// that used to require the separate roger-station binary and its invite-file ceremony, folded
// into the one binary providers actually run.
//
// # THE FLOW
//
//	1. The node mints (or reloads) its persistent STATION identity - the assertion key that
//	   signs receipts and the X25519 session key consumers seal requests to - beside its
//	   ordinary node key, under the same data dir.
//	2. It SELF-ATTACHES: one signed call to Roger Core with its keys, model, and ITS OWN
//	   per-token price. Core assigns a live tower and returns the hub endpoint + the bearer
//	   token this node polls with. A lost reply is safe: the same call is answered
//	   idempotently with the existing registration.
//	3. It pins Core's grant key (fetched from Core itself, not from the tower - the tower is
//	   exactly the party a forged grant would come from), and runs ServeLoop workers: poll
//	   the tower's hub, ServeSealed each job (open the sealed request, verify the grant,
//	   serve the local model, sign the TOKEN receipt, seal the answer to the consumer), and
//	   return it. The tower carries only ciphertext; settlement pays this node 70% of its
//	   listed price, the tower 10%, the platform 20%.
//
// # WHAT THE OPERATOR SEES
//
// Nothing. This runs beside an ordinary `roger share` (cmd/rogerai/relayfabric.go), best
// effort and silent: the node has already registered, gone on air and printed its one line by
// the time this starts, and the relay fabric is an ADDITIONAL plane it serves on rather than a
// fabric it was moved to. There is no flag - `roger share --tower` used to be one, and it was
// wrong in shape: it made a provider choose a serving fabric for the life of the process, when
// which relay carries a request is Core's decision at the moment a consumer tunes in. Prices
// are the share's ordinary $/1M-token prices, converted to the tower path's micro-USD
// integers.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/station"
	"rogerai.fm/roger/v5/internal/towercore/link"
	"rogerai.fm/roger/v5/internal/towerhub"
)

// TowerAttachment is what self-attach resolved: where to poll and with what credential.
type TowerAttachment struct {
	StationID string `json:"station_id"`
	TowerID   string `json:"tower_id"`
	Endpoint  string `json:"endpoint"`
	HubToken  string `json:"hub_token"`
	State     string `json:"state"`
	Note      string `json:"note"`
}

// microsPerDollarPer1M converts the share's float $/1M-token price to the tower path's
// integer micro-USD per 1M tokens. Rounded, not truncated: a float that lands a hair under
// the operator's listed price must not shave a micro off what they charge (audit N2).
func microsPerDollarPer1M(price float64) int64 {
	if price <= 0 {
		return 0
	}
	return int64(math.Round(price * 1_000_000))
}

// hubBaseURL turns Core's advertised hub endpoint into a base URL, and says whether the result
// is PLAINTEXT.
//
// # THE COMMENT THAT USED TO BE HERE WAS FALSE
//
// It said "an endpoint that carries its own scheme is honored verbatim - this is how a
// TLS-fronted hub is reached". No such endpoint can exist. Both places a relay endpoint enters
// the system validate it with net.SplitHostPort - internal/towercore/link/towerlink.go on the
// tower's Hello, and cmd/roger-tower/serve.go on its own configuration - and
// net.SplitHostPort("https://relay.example:443") fails with "too many colons in address". So an
// endpoint carrying a scheme is refused at ingress and never reaches here, the scheme branch is
// dead code, and the "http://" branch is the only one that has ever run.
//
// The scheme branch is KEPT, not deleted, because the fix is a wire-protocol change (an endpoint
// format that can express a scheme, or channel-bound tokens instead of a bearer) touching Core,
// the tower and the node at once - see the open decision in docs/relay-selection-design.md. What
// is removed is the claim that the capability already exists, because that claim is what let the
// plaintext default look deliberate.
//
// WHAT RIDES IN THE CLEAR. Not the payload: the job and its result are sealed to keys the tower
// does not hold, and that is unchanged. What rides in the clear is the node's per-Station HUB
// BEARER TOKEN, on every long poll, forever - and anything on the path that captures it can poll
// for that Station's work. The second return value exists so the node can SAY so; before the
// flag removal this was one operator's opt-in choice and now it is every signed-in share's
// default, which is a change in who is owed the sentence.
func hubBaseURL(endpoint string) (base string, plaintext bool) {
	if strings.Contains(endpoint, "://") {
		return endpoint, !strings.HasPrefix(endpoint, "https://")
	}
	return "http://" + endpoint, true
}

// ErrPrivateShareNeverRelays refuses to put a PRIVATE band on the public relay fabric.
//
// A private share is hidden from /discover and /market and routable only by frequency code; the
// relay fabric is public placement by definition. The two are mutually exclusive, and before the
// flag removal the CLI said so out loud (`--tower` with `--private` was a usage error).
//
// The refusal now lives HERE, at the network act, and not only in the branch structure of
// cmd/rogerai/main.go. As shipped, the only thing keeping a private band off the fabric was that
// `go joinRelayFabric(cfgRun)` happened to sit inside `if !*private {` - a placement, not a rule.
// Nothing in joinRelayFabric, ServeTower or towerEdgeAttach ever looked at the band. Merging the
// two agentStart branches - which the duplicated setup in that function visibly invites - would
// have published private bands to the public fabric with no compile error and no failing test.
var ErrPrivateShareNeverRelays = errors.New(
	"a private band is never offered to the relay fabric: it is reachable by frequency code only, " +
		"and the fabric is public placement")

// ErrCoreKeysUnpinned marks a failure to pin Roger Core's grant and envelope keys.
//
// It is a sentinel because of what the node cannot do without those keys: tell a real Core-signed
// grant from one the TOWER forged. The tower is the party in front of the node and the exact party
// a forged grant would come from, so this is not "a fetch failed", it is "the trust assumption
// this whole plane rests on is not established". It must never be swallowed.
var ErrCoreKeysUnpinned = errors.New("Roger Core's grant key could not be pinned")

// ErrHubChannelPlaintext is the standing notice that this node's hub link is unencrypted. It is
// carried as an error because it travels the channel errors travel - the one thing that is not
// swallowed - and because it is, in fact, a defect: see hubBaseURL.
var ErrHubChannelPlaintext = errors.New(
	"this node's relay hub link is UNENCRYPTED (plain http): the sealed job and its answer stay " +
		"private, but the polling token authenticating this node to its relay rides in the clear")

// Notice is how the relay plane reports something the operator must not miss.
//
// # WHY THIS IS NOT THE io.Writer
//
// ServeTower used to have one output seam, an io.Writer, carrying both "attached, serving" and
// "you did this work and will not be paid". `roger share` passes io.Discard for it - correctly,
// because the ordinary share has already printed its on-air line and a stream of relay progress
// underneath it would describe a plane the operator did not opt into. But one writer for two
// kinds of message means discarding one discards both, and what went into the bin included
// towerhub.ErrNotCarried (the hub took the completion and never couriered the receipt: the node
// computed and will not be paid), a failed result return, every audit failure, transcripts
// evicted inside their audit window, and the key-pinning failure above.
//
// So the writer was the wrong seam, not the wrong setting. Routine progress and consequential
// errors now travel separately: the first is still discardable, the second is not.
type Notice func(error)

// notify is Notice's nil-safe call.
func (n Notice) notify(err error) {
	if n != nil && err != nil {
		n(err)
	}
}

// costlyRelayError reports whether a worker-level error is one the operator must be told about,
// as opposed to the transport chatter a long-polling loop produces all day.
//
// The line is drawn at WORK DONE. A failed poll costs nothing - there was no job. A completion
// the hub would not take, or took and did not courier, means the GPU time was spent and nobody
// will pay for it. Everything else backs off and retries, which is what the loops are for.
func costlyRelayError(err error) bool {
	return errors.Is(err, towerhub.ErrNotCarried) || errors.Is(err, towerhub.ErrResultUndelivered)
}

// AttachTower self-attaches this node as a servable station: keys from the persistent station
// identity under dir, the offer from cfg (model/modality/prices), the request signed with the
// node's account-bound key. Idempotent on retry with the same identity.
func AttachTower(cfg Config, priv ed25519.PrivateKey, dir string) (*station.Station, TowerAttachment, error) {
	// A PRIVATE BAND IS NEVER ATTACHED. Structural, at the network act, so the guarantee does not
	// depend on which branch of a caller happens to reach here. See ErrPrivateShareNeverRelays.
	if cfg.Private {
		return nil, TowerAttachment{}, ErrPrivateShareNeverRelays
	}
	// KEY-TRUST TRANSPORT (audit M2): attach ships this node's keys up and a hub bearer token
	// back, and the grant key is pinned over the same base - plaintext http to a non-loopback
	// broker is refused.
	if err := protocol.TrustedBase(cfg.Broker); err != nil {
		return nil, TowerAttachment{}, err
	}
	// InitOrOpen, NOT Init. The station identity is PERSISTENT and this call is not: a host
	// mints its keys the first time it ever attaches and must present the SAME ones on every
	// later run, because Core recorded them on the attachment and verifies every receipt
	// against them. Init alone refuses a directory that already holds a Station - correctly,
	// since re-minting would strand that attachment - so calling it here meant the first
	// `roger share` on a machine reached the relay fabric and every subsequent one failed at
	// its first line. Silently, too: the caller treats the whole join as best-effort and
	// prints nothing, which is right for "no relay is free" and quite wrong for "this host
	// can never join again". A genuinely broken directory still errors out rather than
	// minting a second identity beside the one attachments name.
	st, err := station.InitOrOpen(filepath.Join(dir, "tower-station"))
	if err != nil {
		return nil, TowerAttachment{}, fmt.Errorf("station identity: %w", err)
	}
	modality := cfg.Modality
	if modality == "" {
		modality = "chat"
	}
	body, err := json.Marshal(map[string]any{
		// THE JOIN. This is the same node id `roger share` registers, heartbeats and is
		// probed under. Sending it is what lets Core rank this station by measured health
		// instead of guessing: reliability, TTFT and TPS are all recorded against the broker
		// node id, and a station row is keyed by station id, so without this the two halves
		// of one machine have no name in common. Core does not take our word for it - it
		// requires a live registration under this id signed by the same key signing here.
		"node_id":          cfg.NodeID,
		"station_id":       st.StationID,
		"assertion_key":    hex.EncodeToString(st.AssertionPub()),
		"session_key":      hex.EncodeToString(st.SessionPub()),
		"model":            cfg.Model,
		"modality":         modality,
		"price_in_micros":  microsPerDollarPer1M(cfg.PriceIn),
		"price_out_micros": microsPerDollarPer1M(cfg.PriceOut),
	})
	if err != nil {
		return nil, TowerAttachment{}, err
	}
	const path = "/tower/edge/attach"
	req, err := http.NewRequest(http.MethodPost, cfg.Broker+path, bytes.NewReader(body))
	if err != nil {
		return nil, TowerAttachment{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	pub, ts, sig := protocol.SignRequest(priv, http.MethodPost, path, body)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, fmt.Sprintf("%d", ts))
	req.Header.Set(protocol.HeaderSig, sig)
	resp, err := (&http.Client{Timeout: 30 * time.Second, CheckRedirect: protocol.NoDowngradeRedirect}).Do(req)
	if err != nil {
		return nil, TowerAttachment{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, TowerAttachment{}, fmt.Errorf("attach refused (%d): %s", resp.StatusCode, raw)
	}
	var at TowerAttachment
	if err := json.Unmarshal(raw, &at); err != nil {
		return nil, TowerAttachment{}, fmt.Errorf("unreadable attach response: %w", err)
	}
	if at.Endpoint == "" || at.HubToken == "" {
		return nil, TowerAttachment{}, errors.New("attach answered without an endpoint or token")
	}
	return st, at, nil
}

// fetchCoreKeys pins Roger Core's grant-signing key AND its envelope key, from Core itself.
// The envelope key is what audit transcripts are sealed to, so the tower relays them exactly
// as blind as the jobs.
func fetchCoreKeys(broker string) (grantKey, envKey []byte, err error) {
	if err := protocol.TrustedBase(broker); err != nil {
		return nil, nil, err
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second, CheckRedirect: protocol.NoDowngradeRedirect}).Get(broker + "/tower/dispatch/key")
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("grant key fetch: %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		DispatchKey string `json:"dispatch_key"`
		EnvelopeKey string `json:"envelope_key"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, nil, err
	}
	grantKey, err = hex.DecodeString(out.DispatchKey)
	if err != nil || len(grantKey) != ed25519.PublicKeySize {
		return nil, nil, errors.New("the grant key is not a hex ed25519 public key")
	}
	envKey, err = hex.DecodeString(out.EnvelopeKey)
	if err != nil || len(envKey) != 32 {
		return nil, nil, errors.New("the envelope key is not a hex X25519 public key")
	}
	return grantKey, envKey, nil
}

// sealedExec adapts the station's sealed serve to the towerhub Executor seam.
type sealedExec struct{ e station.EdgeExecutor }

func (s sealedExec) Serve(ctx context.Context, grant, envelope []byte) ([]byte, []byte, string) {
	return s.e.ServeSealed(ctx, grant, envelope)
}

// transcriptSource adapts the station's transcript lookup to the audit-answer seam.
type transcriptSource struct{ e station.EdgeExecutor }

// EvictedYoung forwards the station's count of transcripts dropped inside their audit
// window, so the serve loop can say so instead of the operator discovering it as audit
// failures at Core.
func (t transcriptSource) EvictedYoung() int {
	if t.e.Transcripts == nil {
		return 0
	}
	return t.e.Transcripts.EvictedYoung()
}

func (t transcriptSource) SignedTranscript(attemptID string) (signed, request, response []byte, ok bool, err error) {
	tr, found, terr := t.e.Transcript(attemptID)
	if terr != nil || !found {
		return nil, nil, nil, false, terr
	}
	return tr.Signed, tr.Request, tr.Response, true, nil
}

// ServeTower runs the tower-serving fabric until ctx is done: self-attach, pin Core's grant
// key, then Parallel ServeLoop workers polling the assigned tower's hub. Errors before the
// workers start are returned; worker-level transport blips are reported to out and retried
// by the loops themselves.
func ServeTower(ctx context.Context, cfg Config, priv ed25519.PrivateKey, dir string, out io.Writer, notice Notice) error {
	st, at, err := AttachTower(cfg, priv, dir)
	if err != nil {
		return err
	}
	// The station directory had something wrong with it that Open could repair rather than
	// refuse - a permissive mode, most likely. Repairing it silently would leave the operator
	// believing a key that has been readable was never readable.
	for _, w := range st.Warnings {
		notice.notify(errors.New(w))
	}
	coreKey, coreEnvKey, err := fetchCoreKeys(cfg.Broker)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCoreKeysUnpinned, err)
	}
	base, plaintext := hubBaseURL(at.Endpoint)
	if plaintext {
		// ONCE, and on the channel that is not discarded. This is a standing property of the
		// link rather than an event, so it is said at the moment the link is established and
		// not repeated per poll.
		notice.notify(fmt.Errorf("%w (relay %s at %s)", ErrHubChannelPlaintext, at.TowerID, base))
	}
	fmt.Fprintf(out, "tower: attached as %s via %s (%s) - serving %s at your listed price\n",
		at.StationID, at.TowerID, at.Endpoint, cfg.Model)

	exec := station.EdgeExecutor{
		Station: st, CoreKey: coreKey, Network: link.PublicNetwork,
		Upstream: station.HTTPUpstream{URL: cfg.Upstream},
		Outbox:   station.NewOutbox(256),
		Seen:     station.NewAttemptCache(),
		// Transcripts make this node AUDITABLE: Core's sampled/adaptive audit asks for the
		// exact bytes behind a settled receipt, and a node that retains nothing can only
		// answer "not retained". Keep-all over the recent window (the store is bounded).
		Transcripts: station.NewTranscripts(0, 0),
	}
	client := &towerhub.Client{
		BaseURL: base,
		Token:   at.HubToken,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
	// THESE ARE ADDITIONAL TO THE CLASSIC POLL WORKERS, not a share of them. agent.Start
	// already spawns cfg.Parallel workers against the same local model, and since every public
	// share now offers itself to the relay fabric as well, `--parallel 4` is a ceiling of eight
	// concurrent generations rather than four. There is no shared limiter between the two
	// planes and this is not the place to invent one: a hub worker costs nothing while no
	// consumer is tuned in, so halving each plane would cut the throughput of the path most
	// requests actually take in order to bound a case few nodes reach. The flag's help says
	// "per serving plane" for exactly this reason.
	workers := cfg.Parallel
	if workers <= 0 {
		workers = 2
	}
	// The audit-answer loop rides beside the workers: fetch what Core wants from this
	// Station (relayed by the hub) and answer with signed transcripts.
	// EVERY audit-plane error goes to the notice channel, not to out. An unanswered audit is a
	// finding against this operator at Core - withholding is itself a finding - and a transcript
	// evicted inside its window is evidence destroyed before it was asked for. Neither is
	// transport chatter, even when its immediate cause is. The sink is expected to say a
	// repeated thing once (see cmd/rogerai/relayfabric.go), which is what makes it safe to be
	// generous here rather than trying to classify a hub's HTTP status.
	go towerhub.AnswerAudits(ctx, client, at.StationID, transcriptSource{exec}, coreEnvKey, 0, func(err error) {
		notice.notify(fmt.Errorf("relay audit: %w", err))
	})
	done := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			done <- towerhub.ServeLoop(ctx, client, at.StationID, sealedExec{exec}, func(err error) {
				// Work already done, and nobody will pay for it: the operator hears about it.
				// A poll that could not reach the hub is retried by the loop and stays quiet.
				if costlyRelayError(err) {
					notice.notify(err)
					return
				}
				fmt.Fprintf(out, "tower: %v\n", err)
			})
		}()
	}
	var first error
	for i := 0; i < workers; i++ {
		if werr := <-done; werr != nil && first == nil {
			first = werr
		}
	}
	if errors.Is(first, context.Canceled) {
		return nil
	}
	return first
}

// NodeKey exposes this host's persistent node key (the same identity `roger share` registers
// and `roger login` binds to an account) for the tower-serving path, which signs its
// self-attach with it.
func NodeKey() ed25519.PrivateKey { return loadOrCreateKey() }
