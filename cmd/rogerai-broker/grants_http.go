package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// The /grants endpoints (GRANT-KEYS-DESIGN section 6.1). All are owner-auth: the
// request must be signed AND the signing pubkey bound to a GitHub owner
// (requireOwner), exactly like priced node registration. Every row is scoped to
// owner == caller.Pubkey, so an owner only ever sees/edits their own grants.
//
//	POST   /grants            create  (returns id + secret ONCE)
//	GET    /grants            list    (the caller-owner's grants + usage)
//	GET    /grants/{id}       show
//	PATCH  /grants/{id}       edit    (caps/models/nodes/price/revoked)
//	DELETE /grants/{id}       revoke

// newGrantSecret mints a fresh "rog-grant_<random>" bearer secret (crypto/rand).
func newGrantSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return grantPrefix + hex.EncodeToString(b)
}

func newGrantID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "grant_" + hex.EncodeToString(b)
}

func secretHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// grants is the collection handler (POST create, GET list). The per-grant
// handler (grantByID) covers GET/PATCH/DELETE on /grants/{id}.
func (b *broker) grants(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	// /grants/{id} -> the item handler.
	if id := strings.TrimPrefix(r.URL.Path, "/grants/"); id != "" && id != r.URL.Path {
		b.grantByID(w, r, strings.Trim(id, "/"))
		return
	}
	owner, ok := b.requireOwner(r)
	if !ok {
		jsonErr(w, http.StatusForbidden, "creating grants requires a GitHub-linked owner - run `rogerai login`")
		return
	}
	switch r.Method {
	case http.MethodPost:
		b.grantCreate(w, r, owner)
	case http.MethodGet:
		b.grantList(w, r, owner)
	default:
		w.Header().Set("Allow", "GET, POST")
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// grantCreateReq is the create body. Free defaults true when no price is given.
type grantCreateReq struct {
	Name       string   `json:"name"`
	Free       *bool    `json:"free,omitempty"`
	PriceIn    float64  `json:"price_in,omitempty"`
	PriceOut   float64  `json:"price_out,omitempty"`
	Models     []string `json:"models,omitempty"`
	Nodes      []string `json:"nodes,omitempty"`
	RPM        float64  `json:"rpm,omitempty"`
	Burst      float64  `json:"burst,omitempty"`
	DailyCap   int64    `json:"daily_cap,omitempty"`
	MonthlyCap int64    `json:"monthly_cap,omitempty"`
	ExpiresAt  int64    `json:"expires_at,omitempty"`
	Self       bool     `json:"self,omitempty"`
}

func (b *broker) grantCreate(w http.ResponseWriter, r *http.Request, owner store.Owner) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var req grantCreateReq
	if json.Unmarshal(body, &req) != nil || strings.TrimSpace(req.Name) == "" {
		jsonErr(w, http.StatusBadRequest, "name required")
		return
	}
	// Free is the default; a custom price (and not --free) makes it priced.
	free := true
	if req.Free != nil {
		free = *req.Free
	} else if req.PriceIn > 0 || req.PriceOut > 0 {
		free = false
	}
	secret := newGrantSecret()
	g := store.Grant{
		ID: newGrantID(), SecretHash: secretHash(secret), Owner: owner.Pubkey,
		Label: req.Name, Nodes: req.Nodes, Models: req.Models,
		Free: free, PriceIn: req.PriceIn, PriceOut: req.PriceOut,
		RPM: req.RPM, Burst: req.Burst, DailyCap: req.DailyCap, MonthlyCap: req.MonthlyCap,
		Self: req.Self, ExpiresAt: req.ExpiresAt, CreatedAt: time.Now().Unix(),
	}
	if err := b.db.CreateGrant(g); err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not create grant")
		return
	}
	// The secret is returned ONCE; only its hash is stored, so it can never be
	// re-displayed. Include ready-to-paste env lines for the remote/no-proxy pattern.
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "grant": grantView(g, store.GrantUsage{}),
		"secret":          secret,
		"openai_api_base": b.selfURL() + "/v1",
		"openai_api_key":  secret,
		"note":            "save this secret now - it is shown only once",
	})
}

func (b *broker) grantList(w http.ResponseWriter, r *http.Request, owner store.Owner) {
	list, err := b.db.GrantsByOwner(owner.Pubkey)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	now := time.Now()
	out := make([]map[string]any, 0, len(list))
	for _, g := range list {
		u, _ := b.db.GrantUsageOf(g.ID, now)
		out = append(out, grantView(g, u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"grants": out})
}

// grantByID handles GET/PATCH/DELETE for a single grant, owner-scoped.
func (b *broker) grantByID(w http.ResponseWriter, r *http.Request, id string) {
	owner, ok := b.requireOwner(r)
	if !ok {
		jsonErr(w, http.StatusForbidden, "managing grants requires a GitHub-linked owner - run `rogerai login`")
		return
	}
	switch r.Method {
	case http.MethodGet:
		// Show is scoped to the owner's grants (list + filter keeps the store surface small).
		list, _ := b.db.GrantsByOwner(owner.Pubkey)
		for _, g := range list {
			if g.ID == id {
				u, _ := b.db.GrantUsageOf(g.ID, time.Now())
				writeJSON(w, http.StatusOK, map[string]any{"grant": grantView(g, u)})
				return
			}
		}
		jsonErr(w, http.StatusNotFound, "no such grant")
	case http.MethodDelete:
		ok, err := b.db.SetGrantRevoked(id, owner.Pubkey, true)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "store error")
			return
		}
		if !ok {
			jsonErr(w, http.StatusNotFound, "no such grant")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": true})
	case http.MethodPatch:
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		var patch store.GrantPatch
		if json.Unmarshal(body, &patch) != nil {
			jsonErr(w, http.StatusBadRequest, "bad patch")
			return
		}
		g, ok, err := b.db.UpdateGrant(id, owner.Pubkey, patch)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "store error")
			return
		}
		if !ok {
			jsonErr(w, http.StatusNotFound, "no such grant")
			return
		}
		u, _ := b.db.GrantUsageOf(g.ID, time.Now())
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "grant": grantView(g, u)})
	default:
		w.Header().Set("Allow", "GET, PATCH, DELETE")
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// grantView is the public (secret-free) JSON shape of a grant + its usage. NEVER
// includes the secret or its hash.
func grantView(g store.Grant, u store.GrantUsage) map[string]any {
	status := "active"
	if g.Revoked {
		status = "revoked"
	} else if g.Expired(time.Now()) {
		status = "expired"
	}
	price := "free"
	if !g.Free && !g.Self {
		price = "$" + ftoa(g.PriceIn) + "/$" + ftoa(g.PriceOut)
	}
	return map[string]any{
		"id": g.ID, "name": g.Label, "nodes": g.Nodes, "models": g.Models,
		"free": g.Free, "self": g.Self, "price": price,
		"price_in": g.PriceIn, "price_out": g.PriceOut,
		"rpm": g.RPM, "burst": g.Burst, "daily_cap": g.DailyCap, "monthly_cap": g.MonthlyCap,
		"expires_at": g.ExpiresAt, "revoked": g.Revoked, "status": status,
		"created_at": g.CreatedAt,
		"usage":      map[string]any{"day_tokens": u.DayTokens, "month_tokens": u.MonthTokens},
	}
}

// selfURL is the broker's externally-reachable base URL, for the ready-to-paste
// grant env lines. Overridable via ROGERAI_BROKER_URL.
func (b *broker) selfURL() string {
	return envOr("ROGERAI_BROKER_URL", "https://broker.rogerai.fyi")
}
