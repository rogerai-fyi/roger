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
// `roger share --tower` (flag wiring in cmd/rogerai): the same share they already run, with
// the serving fabric pointed at a tower instead of the broker's own long-poll. Prices are the
// share's ordinary $/1M-token prices, converted to the tower path's micro-USD integers.

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

// hubBaseURL turns Core's advertised hub endpoint into a base URL. An endpoint that carries
// its own scheme is honored verbatim (audit M1: this is how a TLS-fronted hub is reached);
// a bare host:port defaults to http for today's dev topology - the PAYLOAD is sealed
// end-to-end regardless, but the polling token rides the clear until Core's advertisements
// carry an explicit https scheme.
func hubBaseURL(endpoint string) string {
	if strings.Contains(endpoint, "://") {
		return endpoint
	}
	return "http://" + endpoint
}

// AttachTower self-attaches this node as a servable station: keys from the persistent station
// identity under dir, the offer from cfg (model/modality/prices), the request signed with the
// node's account-bound key. Idempotent on retry with the same identity.
func AttachTower(cfg Config, priv ed25519.PrivateKey, dir string) (*station.Station, TowerAttachment, error) {
	// KEY-TRUST TRANSPORT (audit M2): attach ships this node's keys up and a hub bearer token
	// back, and the grant key is pinned over the same base - plaintext http to a non-loopback
	// broker is refused.
	if err := protocol.TrustedBase(cfg.Broker); err != nil {
		return nil, TowerAttachment{}, err
	}
	st, err := station.Init(filepath.Join(dir, "tower-station"))
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
func ServeTower(ctx context.Context, cfg Config, priv ed25519.PrivateKey, dir string, out io.Writer) error {
	st, at, err := AttachTower(cfg, priv, dir)
	if err != nil {
		return err
	}
	coreKey, coreEnvKey, err := fetchCoreKeys(cfg.Broker)
	if err != nil {
		return fmt.Errorf("cannot pin Roger Core's keys: %w", err)
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
		BaseURL: hubBaseURL(at.Endpoint),
		Token:   at.HubToken,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
	workers := cfg.Parallel
	if workers <= 0 {
		workers = 2
	}
	// The audit-answer loop rides beside the workers: fetch what Core wants from this
	// Station (relayed by the hub) and answer with signed transcripts.
	go towerhub.AnswerAudits(ctx, client, at.StationID, transcriptSource{exec}, coreEnvKey, 0, func(err error) {
		fmt.Fprintf(out, "tower audit: %v\n", err)
	})
	done := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			done <- towerhub.ServeLoop(ctx, client, at.StationID, sealedExec{exec}, func(err error) {
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
