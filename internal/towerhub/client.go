package towerhub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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
	// timeout longer than the tower's poll TTL so a long poll is not cut short.
	HTTP *http.Client
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
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
		return Result{}, &HTTPError{Status: resp.StatusCode, Body: string(raw)}
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
		return Job{}, false, &HTTPError{Status: resp.StatusCode, Body: string(raw)}
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
	if resp.StatusCode != http.StatusOK {
		return &HTTPError{Status: resp.StatusCode, Body: string(raw)}
	}
	return nil
}

// HTTPError carries a non-2xx status from the hub so callers can branch on it.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("tower hub returned %d: %s", e.Status, e.Body)
}
