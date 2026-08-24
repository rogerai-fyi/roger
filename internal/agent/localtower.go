package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
)

// ServeLocalTower runs a share node against a STANDALONE Tower's consumer plane. It is the
// standalone counterpart of ServeTower, and it is deliberately plain: no billing, no receipts
// (the Tower records its own free local receipts), no sealed hub, no streaming to a broker. The
// node POLLS the Tower for work (the Tower never dials the node), runs each job against its own
// upstream model, and returns the answer - so a private plant serves its own clients with the
// Tower as a pure local switchboard.
//
// The node signs every poll and completion with its node key AND a per-request nonce, so the
// Tower's replay guard can refuse a captured poll resent on the LAN (which would otherwise be
// handed a pending consumer prompt). The node must already be an ATTACHED station of the Tower
// (roger-tower attach --key <the node's tower client id>); an unattached key is refused.
//
// It loops until the context is cancelled. Poll and per-job errors are transient - the node
// simply re-polls - because a standalone plant should keep serving across a Tower blip.
func ServeLocalTower(ctx context.Context, cfg Config, priv ed25519.PrivateKey, out io.Writer) error {
	pollClient := &http.Client{Timeout: 40 * time.Second} // longer than the plane's poll window
	execClient := &http.Client{Timeout: 10 * time.Minute} // a real prompt is real work
	fmt.Fprintf(out, "serving the local network's stations at %s (polling for work)\n", cfg.Broker)
	var lastPollErrLog time.Time
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		job, got, err := pollLocalJob(ctx, pollClient, cfg.Broker, priv)
		if err != nil && ctx.Err() == nil {
			// A poll error that keeps recurring - an unattached key (401), a wrong Tower address -
			// is worth surfacing, but not on every spin. Log the first, then at most once a minute,
			// so a misconfigured node is visible instead of silently idle. A cancelled context is
			// not an error worth a line: it is the operator stopping the node, so it is skipped.
			if now := time.Now(); now.Sub(lastPollErrLog) > time.Minute {
				fmt.Fprintf(out, "still trying to poll %s: %v\n", cfg.Broker, err)
				lastPollErrLog = now
			}
		}
		if err != nil || !got {
			// No work (204), a transient error, or a cancelled context: pause briefly and re-poll
			// rather than hammering. The plane's long-poll already absorbs most of the wait.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}
		answer := runLocalJob(execClient, cfg, job.Request)
		completeLocalJob(execClient, cfg.Broker, priv, job.ID, answer)
	}
}

// localJob is one unit of work the Tower hands a polling station: the id to complete against and
// the consumer's request to run verbatim.
type localJob struct {
	ID      string          `json:"job_id"`
	Model   string          `json:"model"`
	Request json.RawMessage `json:"request"`
}

// pollLocalJob long-polls the Tower for a job. 200 with a job, 204 (no work), anything else is
// treated as "no job this round".
func pollLocalJob(ctx context.Context, c *http.Client, broker string, priv ed25519.PrivateKey) (localJob, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, broker+"/local/poll", nil)
	if err != nil {
		return localJob{}, false, err
	}
	signLocal(req, nil, priv)
	resp, err := c.Do(req)
	if err != nil {
		return localJob{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return localJob{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return localJob{}, false, fmt.Errorf("poll: status %d", resp.StatusCode)
	}
	var job localJob
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&job); err != nil {
		return localJob{}, false, err
	}
	if job.ID == "" {
		return localJob{}, false, fmt.Errorf("poll: job with no id")
	}
	return job, true, nil
}

// runLocalJob executes one job against the node's own upstream model and returns the answer
// bytes. The request is forced NON-streaming (the plane's completion is one answer, not a
// stream), and pinned to the offer's model on Osaurus, exactly as the joined serve path does.
func runLocalJob(c *http.Client, cfg Config, request []byte) []byte {
	body := unstreamLocal(request)
	if cfg.Osaurus {
		body = pinModel(body, cfg.Model)
	}
	upReq, err := http.NewRequest(http.MethodPost, cfg.Upstream, bytes.NewReader(body))
	if err != nil {
		return localError("could not build the upstream request")
	}
	upReq.Header.Set("Content-Type", "application/json")
	if cfg.UpstreamKey != "" {
		upReq.Header.Set("Authorization", "Bearer "+cfg.UpstreamKey)
	}
	if cfg.Osaurus {
		upReq.Header.Set("X-Persist", "false")
	}
	resp, err := c.Do(upReq)
	if err != nil {
		return localError("the local model did not answer")
	}
	defer resp.Body.Close()
	ans, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	// Never let the node's own upstream key leak back to the consumer if the upstream echoed it.
	ans = redactUpstreamKey(ans, cfg.UpstreamKey)
	// The answer must be valid JSON: it is completed back as a json.RawMessage, and a non-JSON
	// upstream reply (a plain-text error page) would fail to marshal and silently strand the
	// consumer until its 120s timeout. Wrap anything unparseable as a readable local error.
	if !json.Valid(ans) {
		return localError("the local model returned an unreadable response")
	}
	return ans
}

// completeLocalJob returns the answer to the Tower for a job the node polled. Best-effort: if
// the completion fails, the consumer times out and retries; the node just moves on.
func completeLocalJob(c *http.Client, broker string, priv ed25519.PrivateKey, jobID string, answer []byte) {
	body, err := json.Marshal(map[string]any{"job_id": jobID, "answer": json.RawMessage(answer)})
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, broker+"/local/complete", bytes.NewReader(body))
	if err != nil {
		return
	}
	signLocal(req, body, priv)
	req.Header.Set("Content-Type", "application/json")
	if resp, err := c.Do(req); err == nil {
		resp.Body.Close()
	}
}

// signLocal signs a request to the local Tower with the node key and a fresh per-request nonce.
// A standalone station always sends a nonce: the Tower may be on a LAN where a replay could be
// captured, and a nonce is harmless where it could not (the Tower still just accepts the first
// use). body must be exactly what is sent (nil for the empty-bodied poll).
func signLocal(req *http.Request, body []byte, priv ed25519.PrivateKey) {
	nonce := protocol.NewNonce()
	pubHex, ts, sigHex := protocol.SignRequestNonce(priv, req.Method, req.URL.Path, body, nonce)
	req.Header.Set(protocol.HeaderPubkey, pubHex)
	req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	req.Header.Set(protocol.HeaderSig, sigHex)
	req.Header.Set(protocol.HeaderNonce, nonce)
}

// unstreamLocal forces "stream":false so the upstream returns one JSON answer the node can
// return whole - the plane's completion is a single answer, not a byte stream. It leaves a body
// it cannot parse untouched.
func unstreamLocal(body []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil || m == nil {
		return body // not a JSON object (e.g. a literal null) - leave it untouched
	}
	m["stream"] = json.RawMessage("false")
	delete(m, "stream_options")
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// localError is the answer body the node returns when it could not serve, in the OpenAI error
// shape so the consumer gets a readable message rather than an empty reply.
func localError(msg string) []byte {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "local_station_error"}})
	return b
}
