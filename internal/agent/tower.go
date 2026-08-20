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
	"time"

	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/station"
	"rogerai.fm/roger/v5/internal/towercore/link"
	"rogerai.fm/roger/v5/internal/towerhub"
)

// TowerAttachment is what self-attach resolved: where to poll, and as whom.
type TowerAttachment struct {
	StationID string `json:"station_id"`
	TowerID   string `json:"tower_id"`
	Endpoint  string `json:"endpoint"`
	// HubToken is the pre-signature bearer credential. THIS NODE NO LONGER SENDS IT: hub
	// requests are signed with the Station's assertion key instead (internal/towerhub's
	// nodeauth.go), which is what stops an on-path attacker on a plaintext link from lifting a
	// reusable credential and polling this Station's queue. It is still parsed because Core
	// still mints one for towers serving nodes that have not updated, and a field silently
	// dropped from a wire type is how the next reader concludes it was never there.
	HubToken string `json:"hub_token"`
	// TowerKeyHash is the fingerprint of the identity key Core ADMITTED this relay under - hex
	// sha256 of its raw Ed25519 public key. It is what lets this node tell the relay's own
	// statements from an on-path attacker's.
	//
	// It is needed for exactly one thing, and that one thing is load-bearing. A node signs every
	// hub request over a target naming the hub's PROCESS EPOCH, and it can only learn that value
	// from the hub's own 401 - which is unauthenticated, on a link that is plaintext by
	// construction. Believing it means signing over whatever the party in front of the node
	// names: a genuine Ed25519 signature, fresh nonce, fresh timestamp, over bytes no hub has
	// ever seen. With this fingerprint the node checks the hub's signature over its own epoch
	// instead (internal/towerhub, HubKeyHeader), so the epoch is the tower's value rather than
	// the attacker's.
	TowerKeyHash string `json:"tower_key_hash"`
	// EndpointTLSSPKI is the hub certificate pin Core published for Endpoint: hex sha256 over
	// the SubjectPublicKeyInfo of the certificate that hub presents. Non-empty means this node
	// polls over https and accepts THAT CERTIFICATE AND NO OTHER; empty means the relay serves
	// plaintext, which is what every relay did before this field existed and is still legal.
	//
	// IT IS A DIFFERENT KEY FROM TowerKeyHash, DOING A DIFFERENT JOB, and the two are worth
	// telling apart. TowerKeyHash is the relay's long-term IDENTITY, and it authenticates one
	// statement - the hub's process epoch - inside a channel anyone can read. This pin
	// authenticates the CHANNEL, so that everything else the hub says (a job, a 204, a 401, a
	// completion accepted) comes from the relay rather than from whoever is on the path, and so
	// that this station's assertion public key stops riding every poll in the clear.
	//
	// Optional on purpose: see ServeTower. Making it mandatory would take every relay whose
	// operator has not turned TLS on off the air, and that is the founder's call to make on a
	// date, not a side effect of shipping the capability.
	EndpointTLSSPKI string `json:"endpoint_tls_spki"`
	State           string `json:"state"`
	Note            string `json:"note"`
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

// hubBaseURL turns Core's advertisement of a relay's data plane - an endpoint, and a hub
// certificate pin that may be empty - into a base URL and a client that will verify whatever
// answers, and says whether the result is PLAINTEXT.
//
// # THE COMMENT THAT USED TO BE HERE WAS FALSE, AND THE FIX IS WHY THIS SIGNATURE CHANGED
//
// It said "an endpoint that carries its own scheme is honored verbatim - this is how a
// TLS-fronted hub is reached". No such endpoint can exist. Both places a relay endpoint enters
// the system validate it with net.SplitHostPort - internal/towercore/link/towerlink.go on the
// tower's Hello, and cmd/roger-tower/serve.go on its own configuration - and
// net.SplitHostPort("https://relay.example:443") fails with "too many colons in address". So an
// endpoint carrying a scheme was refused at ingress and never reached here, the scheme branch
// was dead code, and the "http://" branch was the only one that had ever run. A tower operator
// who obtained a certificate and passed --hub-tls-cert got a TLS listener that every node in the
// fleet connected to in plaintext and failed against: the flags were not a path to safety, they
// were a trap.
//
// What replaced the dead branch is not a scheme in the endpoint - that would have been a
// breaking change to a field three clients concatenate onto - but a PIN advertised beside it.
// Core relays the fingerprint of the certificate the tower's hub presents, and this node accepts
// that certificate and no other. No public certificate authority is involved and no domain name
// is needed, which matters because the operators this fabric is built on are volunteers on home
// connections who can obtain neither. The whole argument is in internal/towerhub/pin.go.
//
// WHAT RIDES IN THE CLEAR ON AN UNPINNED LINK, AND WHAT NO LONGER DOES. Not the payload: the job
// and its result are sealed to keys the tower does not hold, and that was always true. It used
// to be the node's per-Station HUB BEARER TOKEN as well, on every long poll, forever - so
// anything on the path could capture it and poll that Station's queue until the attachment was
// revoked. That is gone: hub requests are SIGNED per request with the Station's assertion key
// (internal/towerhub's nodeauth.go), so what an observer captures authenticates nothing a second
// time. What is left is traffic shape, this station's assertion public key on every poll, and
// the fact that nothing authenticates the hub's ANSWERS. The second return value exists so the
// node can still say so, because "unencrypted" remains true of a relay whose operator has not
// turned TLS on, and the operator is owed the sentence.
func hubBaseURL(endpoint, pin string, hc *http.Client) (base string, client *http.Client, plaintext bool, err error) {
	base, client, err = towerhub.Reach(endpoint, pin, hc)
	if err != nil {
		return "", nil, false, err
	}
	return base, client, pin == "", nil
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
//
// ITS TEXT CHANGED WHEN SIGNED POLLS SHIPPED, and the change is the point rather than a tidy-up.
// It used to say the polling token rides in the clear, which was true and was the reason to care.
// No credential is transmitted now, so repeating that sentence would be teaching operators to
// fear the wrong thing - and an alarm that overstates its case is the one people learn to skip.
//
// IT CHANGED AGAIN, because the first rewrite went one word too far. "Traffic shape" was not the
// whole residual: X-Roger-Pubkey puts the Station's long-term ASSERTION PUBLIC KEY on the wire on
// every single poll, in the clear. That is not a session token and not a nonce - it is the
// identity the node's receipts are verified against and its earnings are paid to, stable for the
// life of the station, and it makes every poll a linkable identifier tying that identity to an
// IP address, across networks, across towers, and across re-attachments. Nothing an attacker
// captures lets them TAKE anything, which is what the signing change bought; being permanently
// identifiable is a different harm and it belongs in the same sentence rather than under it.
//
// AND ONCE MORE, FOR TWO REASONS. The residual was still understated: nothing on an unpinned
// link authenticates the hub's ANSWERS, so a party on the path can inject the status codes this
// node reasons about its own work with - a 204 for "nothing to do" while real jobs go elsewhere,
// a 401 that reads as a revoked attachment. And the notice can now name a fix, which is the
// difference between a warning and a complaint: the relay's operator passes --hub-tls, Core
// publishes the fingerprint, and this node verifies it with no certificate authority and no
// domain name involved. A standing alarm nobody can act on is one people learn to skip.
var ErrHubChannelPlaintext = errors.New(
	"this node's relay hub link is UNENCRYPTED (plain http): the sealed job and its answer stay " +
		"private, and this node proves who it is by signing every request rather than by sending " +
		"anything reusable, so nothing an observer captures here works twice. Three things still " +
		"leak or bend. The shape of the traffic - when you poll, how big each job is - which your " +
		"relay operator can see in any case. Your station's ASSERTION PUBLIC KEY, which every " +
		"request carries in the clear: it is stable for the life of this station and it is the " +
		"key your receipts and your earnings are tied to, so anyone watching this link can link " +
		"that identity to this address, and anyone watching two links can tell it is the same " +
		"operator on both. And the relay's ANSWERS are unauthenticated, so anyone on the path can " +
		"feed this node a 204 or a 401 the relay never sent. The relay's operator closes all " +
		"three by running their hub with --hub-tls, which needs no certificate authority and no " +
		"domain name")

// ErrHubRefusedThisNode marks a hub that will not accept this node's identity at all - a 401 on
// the polling route, repeated, rather than a blip.
//
// It exists because of what the relay plane does with ordinary transport errors: retries them
// forever and prints them to a writer `roger share` discards. That is right for a tower that is
// down and wrong for a tower that has decided this node is nobody, which never resolves on its
// own. The most likely cause is a version split - a relay running a roger-tower from before
// signed polls cannot verify a signature and will refuse every request from a current node - and
// an operator can only act on that if someone tells them.
var ErrHubRefusedThisNode = errors.New(
	"this node's relay hub refuses its identity (401): it is not serving any work through this " +
		"relay. The usual cause is a relay running a roger-tower older than signed hub polls; a " +
		"revoked attachment and a badly wrong system clock look the same from here")

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

// hubRefusedIdentity reports whether a hub error is an authentication refusal - the one
// transport failure that will never come right by retrying, because nothing about the next
// request will differ from this one.
func hubRefusedIdentity(err error) bool {
	var he *towerhub.HTTPError
	return errors.As(err, &he) && he.Status == http.StatusUnauthorized
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
	// KEY-TRUST TRANSPORT (audit M2): attach ships this node's keys up and the tower and
	// endpoint it is placed on back, and the grant key is pinned over the same base - plaintext
	// http to a non-loopback broker is refused. (It used to be described as bringing a hub
	// bearer token back. Core still sends the field for a node too old to sign; this node
	// ignores it and never transmits it - see towerhub's nodeauth.go.)
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
	// AN ENDPOINT IS ALL THIS NEEDS NOW. It used to also demand a hub token, which was right
	// when the token was how the node authenticated and is wrong now that it signs: refusing to
	// serve because Core did not send a credential we no longer use would strand a node over an
	// unused field.
	if at.Endpoint == "" {
		return nil, TowerAttachment{}, errors.New("attach answered without an endpoint")
	}
	// AND A FINGERPRINT FOR THE RELAY, WHICH IS NOT OPTIONAL. Without it this node cannot tell
	// the hub's own epoch from one an on-path attacker named, and the epoch is a value it signs
	// over - so "carry on without it" means emitting signatures over an attacker's choosing.
	// Refusing here is the same posture the node already takes on the credential itself (it
	// never sends a bearer, whatever the hub answers): a downgrade an attacker could provoke is
	// not a security property. Signed hub polls have not shipped in a tagged release, so
	// nothing in the field is stranded by this - but a Core older than the fingerprint is, and
	// the deployment order was already written down: Core, then Towers, then nodes.
	if at.TowerKeyHash == "" {
		return nil, TowerAttachment{}, errors.New(
			"attach answered without the relay's identity fingerprint (tower_key_hash): this node " +
				"cannot verify which hub process it is signing for without it, and signing for an " +
				"unverified one hands an on-path attacker a signature it chose. Roger Core must be " +
				"updated before the towers and nodes that talk to it")
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
	// THE HUB CLIENT IS BUILT ONCE, HERE, AND ITS TLS SETTINGS ARE NOT NEGOTIABLE LATER. A
	// timeout longer than the tower's poll TTL so a long poll is not cut short - and, when Core
	// published a pin, a transport that accepts exactly the certificate the pin names.
	//
	// TLS IS NOT REQUIRED, and that is a decision rather than an omission. Requiring it in this
	// change would take every relay whose operator has not yet turned it on off the air, and
	// with it every node attached to one; the capability has to work before its deadline can be
	// set. See docs/relay-selection-design.md section 5.7 for the recommendation and what
	// making it mandatory would cost.
	base, hubHTTP, plaintext, err := hubBaseURL(at.Endpoint, at.EndpointTLSSPKI,
		&http.Client{Timeout: 60 * time.Second})
	if err != nil {
		return fmt.Errorf("this relay's data plane cannot be reached as advertised: %w", err)
	}
	if plaintext {
		// ONCE, and on the channel that is not discarded. This is a standing property of the
		// link rather than an event, so it is said at the moment the link is established and
		// not repeated per poll.
		notice.notify(fmt.Errorf("%w (relay %s at %s)", ErrHubChannelPlaintext, at.TowerID, base))
	}
	channel := "unencrypted"
	if !plaintext {
		// Named rather than assumed: an operator reading this line is entitled to know which of
		// the two channels they got, and "encrypted" alone would be the claim an unverified TLS
		// client could also make.
		channel = "encrypted, certificate verified against the fingerprint Roger Core published"
	}
	fmt.Fprintf(out, "tower: attached as %s via %s (%s, %s) - serving %s at your listed price\n",
		at.StationID, at.TowerID, at.Endpoint, channel, cfg.Model)

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
		// THE TOWER IS PART OF WHAT IS SIGNED. Core named this tower in the attach response,
		// and the hub refuses a signature that names any other, so a request captured off this
		// plaintext link is good at this hub and nowhere else - not at a second instance behind
		// the same endpoint, and not at this one after a restart inside the skew window.
		TowerID: at.TowerID,
		// WHAT MAKES THE EPOCH THE HUB'S VALUE AND NOT THE ATTACKER'S. Core admitted this relay
		// under an identity key and handed over its fingerprint above; the hub signs its process
		// epoch with the private half, and this client refuses to adopt an epoch it cannot check
		// against this hash. Without it, a forged 401 on the plaintext link would make this node
		// sign over any epoch the party in front of it liked.
		TowerKeyHash: at.TowerKeyHash,
		// SIGNED, NOT BEARER. st.SignRequest signs each hub call with the assertion key this
		// Station's receipts are already verified against, so the plaintext link carries no
		// reusable credential for anyone on the path to lift. See towerhub's nodeauth.go.
		Sign: st.SignRequest,
		// Built above, because the certificate check belongs with the base URL that decided
		// there would be one. A client assembled here from scratch is a client that dials
		// https and verifies nothing.
		HTTP: hubHTTP,
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
				// A REFUSED IDENTITY IS NOT A BLIP. The loop will retry it every two seconds
				// until the process ends and never get anywhere, and the writer it would
				// otherwise be printed to is discarded, so this is the difference between an
				// operator learning their relay is too old and an operator seeing a station
				// that quietly never earns. The notice sink says a repeated message once.
				if hubRefusedIdentity(err) {
					notice.notify(fmt.Errorf("%w: %w", ErrHubRefusedThisNode, err))
					return
				}
				// THE RELAY SAID SOMETHING IT COULD NOT PROVE, or the endpoint is answered by
				// two hub processes. Both are standing properties of the relay rather than
				// transport chatter, both mean this node is not earning through it, and neither
				// resolves by retrying - so they travel the channel that is not discarded,
				// beside the refused-identity alarm. The sink says a repeated thing once.
				if errors.Is(err, towerhub.ErrHubEpochUnproved) || errors.Is(err, towerhub.ErrHubMultipleProcesses) {
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
