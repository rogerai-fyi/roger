// Package hfhub reads the public model index so an owner can see what roger could run.
//
// It is a thin client over an HTTP API, and it stays thin on purpose: this package NEVER decides
// anything. It reports what the index says, and where the index says nothing it reports nothing
// rather than filling the gap in. The deciding happens in internal/catalog, against facts.
//
// THREE THINGS HERE ARE NOT THIN, and they are the reason the package exists:
//
//  1. TELLING A GATED REPOSITORY APART FROM A BROKEN ONE. A gated repo answers 401 and will not
//     serve its weights to anybody without accepted access. That is the far end refusing, and
//     reporting it as a network failure sends the owner to fix the wrong thing.
//  2. NEVER INVENTING A SIZE. An LFS entry carries TWO sizes: the pointer file, which is a
//     hundred-odd bytes, and the real one nested under lfs. Feeding the first into a memory
//     decision is catastrophic and looks completely normal.
//  3. JUDGING A MODEL FROM ITS FIRST MEGABYTE. A GGUF puts its architecture block before the
//     tokenizer arrays, so one ranged request answers "will this run here" about a file nobody
//     has downloaded. That is what makes a browse screen honest instead of a list of names.
//
// Rate limits are the reason browsing is cached rather than polled: the published limits are
// generous for downloads, which are a handful of requests, and tight for listing. A 429 is
// reported with its Retry-After rather than retried in a loop.
package hfhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/catalog"
	"rogerai.fm/roger/v6/internal/detect"
)

// The reasons a read failed, as values, because the next step differs for each.
var (
	ErrGated       = errors.New("the source will not serve this without accepted access")
	ErrNotFound    = errors.New("the source does not have that")
	ErrRateLimited = errors.New("the source is rate limiting us")
	ErrUpstream    = errors.New("the source answered with an error")
	ErrUnreachable = errors.New("the source did not answer")
	ErrUnexpected  = errors.New("the source answered in a shape this does not understand")
)

// DefaultBase is the public index. It is a field on the client rather than a constant so a test
// can point at a local server and an airgapped deployment can point at a mirror.
const DefaultBase = "https://huggingface.co"

// headerBytes is how much of a model file is fetched to read its architecture. The block sits
// well inside this on every GGUF observed; a file that buries it further simply reports nothing,
// which is the correct outcome for a pre-download estimate.
const headerBytes = 1 << 20

// Client reads the index. A zero Token browses anonymously, which the index allows.
type Client struct {
	HTTP  *http.Client
	Base  string
	Token string
}

// Model is one repository as the index describes it. Everything here is the index's word.
type Model struct {
	Repo      string
	Downloads int
	Likes     int
	Gated     bool
	License   string
	Pipeline  string
}

// File is one servable weight file in a repository, with the EXACT size and digest the index
// publishes - the two things a download has to verify against.
type File struct {
	Path   string
	Bytes  int64
	SHA256 string
	Quant  string
}

// Query is a search. An empty Text browses the most-downloaded, which is a reasonable front page.
type Query struct {
	Text  string
	Limit int
}

const defaultLimit = 30

// Search lists repositories carrying GGUF weights, most downloaded first.
func (c *Client) Search(ctx context.Context, q Query) ([]Model, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	v := url.Values{}
	// The filter is not a nicety: without it the index returns every format, most of which this
	// runtime cannot load, and a list of things that will not work is worse than a short list.
	v.Set("filter", "gguf")
	v.Set("sort", "downloads")
	v.Set("direction", "-1")
	v.Set("limit", strconv.Itoa(limit))
	if q.Text != "" {
		v.Set("search", q.Text)
	}
	var raw []struct {
		ID        string   `json:"id"`
		Downloads int      `json:"downloads"`
		Likes     int      `json:"likes"`
		Gated     any      `json:"gated"`
		Tags      []string `json:"tags"`
		Pipeline  string   `json:"pipeline_tag"`
	}
	if err := c.getJSON(ctx, "/api/models?"+v.Encode(), &raw); err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(raw))
	for _, m := range raw {
		out = append(out, Model{
			Repo: m.ID, Downloads: m.Downloads, Likes: m.Likes,
			Gated: gatedFlag(m.Gated), License: licenseTag(m.Tags), Pipeline: m.Pipeline,
		})
	}
	return out, nil
}

// Files lists the servable weight files in a repository, smallest first because the smallest is
// the one most likely to fit.
func (c *Client) Files(ctx context.Context, repo string) ([]File, error) {
	var raw []struct {
		Type string `json:"type"`
		Path string `json:"path"`
		Size int64  `json:"size"`
		OID  string `json:"oid"`
		LFS  struct {
			OID  string `json:"oid"`
			Size int64  `json:"size"`
		} `json:"lfs"`
	}
	if err := c.getJSON(ctx, "/api/models/"+repo+"/tree/main?recursive=true", &raw); err != nil {
		return nil, err
	}
	out := make([]File, 0, len(raw))
	for _, e := range raw {
		if e.Type != "file" || !strings.EqualFold(path.Ext(e.Path), ".gguf") {
			continue
		}
		f := File{Path: e.Path, Bytes: e.Size, SHA256: e.OID, Quant: detect.QuantInName(e.Path)}
		// THE SIZE THAT MATTERS IS THE LFS ONE. For a file stored in LFS the plain size field is
		// the pointer, about 130 bytes, and using it would tell an owner a 2.5 GB model needs
		// nothing at all. Same for the digest: the outer oid is the pointer's.
		if e.LFS.Size > 0 {
			f.Bytes = e.LFS.Size
		}
		if e.LFS.OID != "" {
			f.SHA256 = e.LFS.OID
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Bytes < out[j].Bytes })
	return out, nil
}

// ShapeOf reads a model's architecture from the first megabyte of the file, so whether it can run
// here is answerable before anything is downloaded. fileBytes comes from the INDEX, not from the
// range: the range is a prefix and its length says nothing about the whole.
func (c *Client) ShapeOf(ctx context.Context, repo, file string, fileBytes int64) (catalog.Shape, error) {
	head, err := c.head(ctx, repo, file, headerBytes)
	if err != nil {
		return catalog.Shape{}, err
	}
	return detect.ShapeFromHeader(head, fileBytes)
}

// DownloadURL is where a file is fetched from. It is resolved fresh for every attempt, including
// every resume: this redirects to a presigned URL that expires, so a cached one fails later in a
// way that looks like corruption.
func (c *Client) DownloadURL(repo, file string) string {
	return c.base() + "/" + repo + "/resolve/main/" + file
}

// AuthHeader is what a fetch of a gated file needs, or nothing when there is no token.
func (c *Client) AuthHeader() map[string]string {
	if c.Token == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + c.Token}
}

func (c *Client) head(ctx context.Context, repo, file string, n int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.DownloadURL(repo, file), nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	req.Header.Set("Range", "bytes=0-"+strconv.FormatInt(n-1, 10))
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, n))
}

func (c *Client) getJSON(ctx context.Context, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+path, nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if err := json.Unmarshal(body, into); err != nil {
		// An unexpected shape is an ERROR, not an empty result. An empty list reads as "there are
		// none", which is a different answer and a wrong one.
		return fmt.Errorf("%w: %v", ErrUnexpected, err)
	}
	return nil
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	cl := c.HTTP
	if cl == nil {
		cl = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := cl.Do(req)
	if err != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
		return resp, nil
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		why := resp.Header.Get("X-Error-Code")
		if why == "" {
			why = "accepted access is required"
		}
		return nil, fmt.Errorf("%w: %s - accept the model's terms on its page and set a token", ErrGated, why)
	case http.StatusNotFound, http.StatusGone:
		return nil, ErrNotFound
	case http.StatusTooManyRequests:
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			return nil, fmt.Errorf("%w: try again in %ss", ErrRateLimited, ra)
		}
		return nil, ErrRateLimited
	}
	return nil, fmt.Errorf("%w: status %d", ErrUpstream, resp.StatusCode)
}

func (c *Client) auth(req *http.Request) {
	// An absent token means an ANONYMOUS browse, which the index allows. Setting an empty
	// Authorization header is not the same thing and some servers treat it as a failed auth.
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
}

func (c *Client) base() string {
	if c.Base != "" {
		return strings.TrimRight(c.Base, "/")
	}
	return DefaultBase
}

// gatedFlag flattens the index's tri-state. Automatic and manual gating differ in how the owner
// gets access, not in whether they need it, and this package's job is to say that they do.
func gatedFlag(v any) bool {
	switch g := v.(type) {
	case bool:
		return g
	case string:
		return g != "" && g != "false"
	}
	return false
}

// licenseTag pulls the licence out of the index's tag list, where it is published as "license:x".
func licenseTag(tags []string) string {
	for _, t := range tags {
		if rest, ok := strings.CutPrefix(t, "license:"); ok {
			return rest
		}
	}
	return ""
}
