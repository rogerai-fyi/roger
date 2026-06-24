package store

import (
	"database/sql"
	"encoding/json"
	"time"
)

// Postgres grant storage (GRANT-KEYS-DESIGN). Mirrors the additive style of the
// owners / earning_lots methods: JSONB for the node/model allow-lists, an indexed
// secret_hash for the hot auth lookup, and a small grant_usage rollup for caps.

// nullStr maps an empty string to a SQL NULL (so an untagged receipt's grant_id
// stays NULL rather than ""), else the value itself.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// jsonStrSlice marshals a string slice to JSONB; nil becomes "[]" so the column
// default holds and round-trips cleanly.
func jsonStrSlice(s []string) []byte {
	if s == nil {
		s = []string{}
	}
	b, _ := json.Marshal(s)
	return b
}

func (p *Postgres) CreateGrant(g Grant) error {
	if g.CreatedAt == 0 {
		g.CreatedAt = time.Now().Unix()
	}
	_, err := p.db.Exec(`INSERT INTO rogerai.grants
		(id,secret_hash,owner,label,nodes,models,free,price_in,price_out,rpm,burst,daily_cap,monthly_cap,self,expires_at,revoked,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		g.ID, g.SecretHash, g.Owner, g.Label, jsonStrSlice(g.Nodes), jsonStrSlice(g.Models),
		g.Free, g.PriceIn, g.PriceOut, g.RPM, g.Burst, g.DailyCap, g.MonthlyCap, g.Self,
		g.ExpiresAt, g.Revoked, g.CreatedAt)
	return err
}

// scanGrant maps one grants row into a Grant.
func (p *Postgres) scanGrant(row interface{ Scan(...any) error }) (Grant, error) {
	var g Grant
	var nodes, models []byte
	err := row.Scan(&g.ID, &g.SecretHash, &g.Owner, &g.Label, &nodes, &models,
		&g.Free, &g.PriceIn, &g.PriceOut, &g.RPM, &g.Burst, &g.DailyCap, &g.MonthlyCap,
		&g.Self, &g.ExpiresAt, &g.Revoked, &g.CreatedAt)
	if err != nil {
		return Grant{}, err
	}
	_ = json.Unmarshal(nodes, &g.Nodes)
	_ = json.Unmarshal(models, &g.Models)
	return g, nil
}

const grantCols = `id,secret_hash,owner,label,nodes,models,free,price_in,price_out,rpm,burst,daily_cap,monthly_cap,self,expires_at,revoked,created_at`

func (p *Postgres) GrantBySecretHash(hash string) (Grant, bool, error) {
	g, err := p.scanGrant(p.db.QueryRow(`SELECT `+grantCols+` FROM rogerai.grants WHERE secret_hash=$1`, hash))
	if err == sql.ErrNoRows {
		return Grant{}, false, nil
	}
	if err != nil {
		return Grant{}, false, err
	}
	return g, true, nil
}

func (p *Postgres) GrantsByOwner(owner string) ([]Grant, error) {
	rows, err := p.db.Query(`SELECT `+grantCols+` FROM rogerai.grants WHERE owner=$1 ORDER BY created_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		g, err := p.scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (p *Postgres) SetGrantRevoked(id, owner string, revoked bool) (bool, error) {
	res, err := p.db.Exec(`UPDATE rogerai.grants SET revoked=$3 WHERE id=$1 AND owner=$2`, id, owner, revoked)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (p *Postgres) UpdateGrant(id, owner string, patch GrantPatch) (Grant, bool, error) {
	// Read-modify-write inside a transaction, owner-scoped: an owner can only ever
	// edit their own grants (the WHERE owner clause is the gate).
	tx, err := p.db.Begin()
	if err != nil {
		return Grant{}, false, err
	}
	defer tx.Rollback()
	g, err := p.scanGrant(tx.QueryRow(`SELECT `+grantCols+` FROM rogerai.grants WHERE id=$1 AND owner=$2 FOR UPDATE`, id, owner))
	if err == sql.ErrNoRows {
		return Grant{}, false, nil
	}
	if err != nil {
		return Grant{}, false, err
	}
	g = g.applyPatch(patch)
	if _, err := tx.Exec(`UPDATE rogerai.grants SET
		label=$3,nodes=$4,models=$5,free=$6,price_in=$7,price_out=$8,rpm=$9,burst=$10,
		daily_cap=$11,monthly_cap=$12,expires_at=$13,revoked=$14 WHERE id=$1 AND owner=$2`,
		id, owner, g.Label, jsonStrSlice(g.Nodes), jsonStrSlice(g.Models), g.Free,
		g.PriceIn, g.PriceOut, g.RPM, g.Burst, g.DailyCap, g.MonthlyCap, g.ExpiresAt, g.Revoked); err != nil {
		return Grant{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Grant{}, false, err
	}
	return g, true, nil
}

func (p *Postgres) GrantUsageOf(id string, now time.Time) (GrantUsage, error) {
	var u GrantUsage
	row := p.db.QueryRow(`SELECT
		COALESCE((SELECT tokens FROM rogerai.grant_usage WHERE grant_id=$1 AND window=$2),0),
		COALESCE((SELECT tokens FROM rogerai.grant_usage WHERE grant_id=$1 AND window=$3),0)`,
		id, dayKey(now), monthKey(now))
	if err := row.Scan(&u.DayTokens, &u.MonthTokens); err != nil {
		return GrantUsage{}, err
	}
	return u, nil
}

func (p *Postgres) AddGrantUsage(id string, tokens int64, now time.Time) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, win := range []string{dayKey(now), monthKey(now)} {
		if _, err := tx.Exec(`INSERT INTO rogerai.grant_usage(grant_id,window,tokens) VALUES($1,$2,$3)
			ON CONFLICT (grant_id,window) DO UPDATE SET tokens=rogerai.grant_usage.tokens+$3`, id, win, tokens); err != nil {
			return err
		}
	}
	return tx.Commit()
}
