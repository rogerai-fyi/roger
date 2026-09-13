// Package enrollhttp is the WIRE under Edge enrollment: one POST, one JSON answer.
//
// It is a separate package from internal/edgeauth on purpose, and the separation is the
// guarantee rather than tidiness. Everything that decides anything - the root, the
// issuing, the checking - lives in edgeauth, which links no HTTP client and no
// Core-dialing package and therefore CANNOT contact Core. The ability to make a network
// call lives here, on its own, where a dependency-graph test can see it.
//
// ONE ENDPOINT SHAPE FOR BOTH AUTHORITIES. Core answers on the same path as a shed on a
// plant network, over the same handler, with the same body. A node's enrollment code
// does not branch on which kind it is talking to; it only knows an address.
package enrollhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/edgeauth"
)

// Path is where an authority answers an enrollment request.
const Path = "/edge/enroll"

// RevocationsPath is where a node refreshes what this authority has revoked.
const RevocationsPath = "/edge/revocations"

// maxBody bounds what an unauthenticated caller may make us hold. An enrollment request
// is a few hundred bytes; a certificate answer is a few kilobytes.
const maxBody = 64 << 10

// timeout bounds one enrollment attempt. Enrolling is interactive: the owner is
// standing there, and a hung dial must become a message rather than a wait.
const timeout = 15 * time.Second

// Issuer is the signing side, as this package needs it. It is an interface only so a
// local authority (which also records what it issued) and a bare Issuer can both be
// served by the same handler.
type Issuer interface {
	Issue(edgeauth.Request) (edgeauth.Response, error)
}

// Revoker is an authority that can say what it has revoked.
type Revoker interface{ Revocations() []string }

// Handler answers enrollment for one authority.
//
// Every refusal is the same status - 403 - with the reason as text. The status is
// deliberately uniform: a machine that is not on this Edge learns "no", not which of
// the several possible "no"s it was, because the differences between them are facts
// about the account and not about the caller.
func Handler(iss Issuer) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(Path, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
		if err != nil {
			http.Error(w, "that request could not be read", http.StatusBadRequest)
			return
		}
		var req edgeauth.Request
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "that is not an enrollment request", http.StatusBadRequest)
			return
		}
		resp, err := iss.Issue(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc(RevocationsPath, func(w http.ResponseWriter, r *http.Request) {
		rev, ok := iss.(Revoker)
		if !ok {
			http.Error(w, "this authority publishes no revocation list", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rev.Revocations())
	})
	return mux
}

// Enroll asks one authority - Core, or the machine in the shed - for a certificate.
//
// endpoint is a base address; the caller does not have to know the path. A refusal
// comes back as an error naming what the authority said, and NOTHING is returned
// alongside it: there is no half answer to half apply.
func Enroll(ctx context.Context, endpoint string, req edgeauth.Request) (edgeauth.Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return edgeauth.Response{}, err
	}
	raw, err := post(ctx, endpoint, Path, body)
	if err != nil {
		return edgeauth.Response{}, err
	}
	var resp edgeauth.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return edgeauth.Response{}, fmt.Errorf("%w: the authority's answer could not be read",
			edgeauth.ErrMalformed)
	}
	return resp, nil
}

// Revocations refreshes what an authority has revoked.
func Revocations(ctx context.Context, endpoint string) ([]string, error) {
	raw, err := get(ctx, endpoint, RevocationsPath)
	if err != nil {
		return nil, err
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%w: the revocation list could not be read", edgeauth.ErrMalformed)
	}
	return out, nil
}

func post(ctx context.Context, endpoint, path string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, join(endpoint, path),
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return do(req)
}

func get(ctx context.Context, endpoint, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, join(endpoint, path), nil)
	if err != nil {
		return nil, err
	}
	return do(req)
}

func do(req *http.Request) ([]byte, error) {
	ctx, cancel := context.WithTimeout(req.Context(), timeout)
	defer cancel()
	resp, err := (&http.Client{}).Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the authority refused: %s", strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

// join puts a path on a base address without caring how the caller spelled it.
func join(endpoint, path string) string {
	return strings.TrimRight(endpoint, "/") + path
}
