package towerhub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// Client speaks the tower hub's HTTP protocol from the other two sides: a serving NODE (Poll +
// Complete, authenticated by its per-Station token) and a CONSUMER (Submit, presenting a
// Core-signed grant). It is the counterpart to Server and shares the wire types with it, so the
// two cannot drift. Everything it carries - grant, sealed request, sealed result, receipt - is
// opaque bytes; the Client no more reads content than the tower does.
type Client struct {
	// BaseURL is the tower's hub root; endpoint sub-paths (PathSubmit/PathPoll/PathComplete) are
	// appended to it.
	BaseURL string
	// Token is the node's per-Station bearer token, sent on Poll/Complete. Empty for a consumer.
	Token string
	// HTTP is the client used for requests; nil means http.DefaultClient. A node should set a
	// timeout longer than the tower's poll TTL so a long poll is not cut short. Its redirect
	// policy is replaced - see httpClient.
	HTTP *http.Client

	// once/http build the effective client exactly once, so the redirect policy below is
	// installed without mutating a *http.Client the caller may be sharing (which would be a
	// data race between the poll workers and the audit loop).
	once sync.Once
	http *http.Client
}

// httpClient is the client every call here uses: the caller's, with REDIRECTS REFUSED.
//
// It used to be the caller's client verbatim, which meant no CheckRedirect at all - unlike every
// broker call the node makes, which all pass protocol.NoDowngradeRedirect. That gap matters more
// here than almost anywhere: the request this Client makes most often is a long poll carrying the
// node's per-Station bearer token in an Authorization header, and Go's default policy would carry
// that header wherever the answering party pointed it.
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
		"a relay does not get to choose where this node sends its polling credential", req.URL.Redacted())
}

func (c *Client) url(path string) string {
	return strings.TrimRight(c.BaseURL, "/") + path
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
	u := c.url(PathPoll) + "?station=" + url.QueryEscape(station)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Job{}, false, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.httpClient().Do(req)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(PathComplete), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.httpClient().Do(req)
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
	u := c.url(PathAuditWanted) + "?station=" + url.QueryEscape(station)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.httpClient().Do(req)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(PathAuditTranscript), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.httpClient().Do(req)
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
