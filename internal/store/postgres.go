package store

import (
	"database/sql"
	"encoding/json"

	"github.com/bownux/rogerai/internal/protocol"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Postgres is a durable Store. Tables are prefixed `rogerai_` so they share an
// existing database cleanly. Swap this out for any other Store impl freely.
type Postgres struct{ db *sql.DB }

// The `rogerai` schema is provisioned by an admin and OWNED by the app's DB user
// (least privilege: the user has no DB-level CREATE, only its own schema). The app
// just manages tables inside it.
const schema = `
CREATE TABLE IF NOT EXISTS rogerai.wallet   (usr  TEXT PRIMARY KEY, balance DOUBLE PRECISION NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS rogerai.earnings (node TEXT PRIMARY KEY, balance DOUBLE PRECISION NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS rogerai.receipts (
    request_id TEXT PRIMARY KEY, usr TEXT, node TEXT, model TEXT,
    prompt_tokens INT, completion_tokens INT, cost DOUBLE PRECISION,
    ts BIGINT, receipt JSONB, created_at TIMESTAMPTZ DEFAULT now());
ALTER TABLE rogerai.receipts ADD COLUMN IF NOT EXISTS owner_share DOUBLE PRECISION NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS receipts_usr_ts  ON rogerai.receipts (usr, ts DESC);
CREATE INDEX IF NOT EXISTS receipts_node_ts ON rogerai.receipts (node, ts DESC);
CREATE TABLE IF NOT EXISTS rogerai.processed_events (key TEXT PRIMARY KEY, at TIMESTAMPTZ DEFAULT now());
CREATE TABLE IF NOT EXISTS rogerai.owners (
    pubkey TEXT PRIMARY KEY,                    -- hex ed25519 user pubkey (the binding key)
    github_id BIGINT NOT NULL,
    login TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now());`

func NewPostgres(dsn string) (*Postgres, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return &Postgres{db: db}, nil
}

func (p *Postgres) BalanceOf(user string, seed float64) (float64, error) {
	if _, err := p.db.Exec(`INSERT INTO rogerai.wallet(usr,balance) VALUES($1,$2) ON CONFLICT (usr) DO NOTHING`, user, seed); err != nil {
		return 0, err
	}
	var bal float64
	err := p.db.QueryRow(`SELECT balance FROM rogerai.wallet WHERE usr=$1`, user).Scan(&bal)
	return bal, err
}

func (p *Postgres) Settle(user, node string, cost, ownerShare float64, rec protocol.UsageReceipt) (float64, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var bal float64
	if err := tx.QueryRow(`UPDATE rogerai.wallet SET balance=balance-$2 WHERE usr=$1 RETURNING balance`, user, cost).Scan(&bal); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO rogerai.earnings(node,balance) VALUES($1,$2)
		ON CONFLICT (node) DO UPDATE SET balance=rogerai.earnings.balance+$2`, node, ownerShare); err != nil {
		return 0, err
	}
	rj, _ := json.Marshal(rec)
	if _, err := tx.Exec(`INSERT INTO rogerai.receipts
		(request_id,usr,node,model,prompt_tokens,completion_tokens,cost,owner_share,ts,receipt)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (request_id) DO NOTHING`,
		rec.RequestID, user, node, rec.Model, rec.PromptTokens, rec.CompletionTokens, cost, ownerShare, rec.TS, rj); err != nil {
		return 0, err
	}
	return bal, tx.Commit()
}

func (p *Postgres) EarningsOf(node string) (float64, error) {
	var bal float64
	err := p.db.QueryRow(`SELECT COALESCE(balance,0) FROM rogerai.earnings WHERE node=$1`, node).Scan(&bal)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return bal, err
}

func (p *Postgres) SpendOf(user string) (float64, error) {
	var spend float64
	err := p.db.QueryRow(`SELECT COALESCE(SUM(cost),0) FROM rogerai.receipts WHERE usr=$1`, user).Scan(&spend)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return spend, err
}

func (p *Postgres) RecentByUser(user string, limit int) ([]Entry, error) {
	return p.recent(`usr`, user, limit)
}

func (p *Postgres) RecentByNode(node string, limit int) ([]Entry, error) {
	return p.recent(`node`, node, limit)
}

// recent returns the most-recent receipts where `col` (a trusted literal column
// name, usr|node) equals val, newest first. limit<=0 defaults to 50.
func (p *Postgres) recent(col, val string, limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := p.db.Query(`SELECT request_id,usr,node,model,prompt_tokens,completion_tokens,cost,owner_share,ts
		FROM rogerai.receipts WHERE `+col+`=$1 ORDER BY ts DESC LIMIT $2`, val, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.RequestID, &e.User, &e.Node, &e.Model, &e.PromptTokens, &e.CompletionTokens, &e.Cost, &e.OwnerShare, &e.TS); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (p *Postgres) AddCredits(user string, amount float64) (float64, error) {
	var bal float64
	err := p.db.QueryRow(`INSERT INTO rogerai.wallet(usr,balance) VALUES($1,$2)
		ON CONFLICT (usr) DO UPDATE SET balance=rogerai.wallet.balance+$2 RETURNING balance`, user, amount).Scan(&bal)
	return bal, err
}

func (p *Postgres) MarkProcessed(key string) (bool, error) {
	res, err := p.db.Exec(`INSERT INTO rogerai.processed_events(key) VALUES($1) ON CONFLICT (key) DO NOTHING`, key)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (p *Postgres) CreditOnce(key, user string, amount float64) (bool, float64, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO rogerai.processed_events(key) VALUES($1) ON CONFLICT (key) DO NOTHING`, key)
	if err != nil {
		return false, 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var bal float64
		_ = tx.QueryRow(`SELECT COALESCE(balance,0) FROM rogerai.wallet WHERE usr=$1`, user).Scan(&bal)
		return false, bal, tx.Commit()
	}
	var bal float64
	if err := tx.QueryRow(`INSERT INTO rogerai.wallet(usr,balance) VALUES($1,$2)
		ON CONFLICT (usr) DO UPDATE SET balance=rogerai.wallet.balance+$2 RETURNING balance`, user, amount).Scan(&bal); err != nil {
		return false, 0, err
	}
	return true, bal, tx.Commit()
}

// Hold atomically reserves credits: the WHERE balance>=amount makes concurrent
// holds serialize at the row, so a wallet can never be driven negative.
func (p *Postgres) Hold(user string, amount float64) (bool, error) {
	res, err := p.db.Exec(`UPDATE rogerai.wallet SET balance=balance-$2 WHERE usr=$1 AND balance>=$2`, user, amount)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (p *Postgres) Finalize(user, node string, held, cost, ownerShare float64, rec protocol.UsageReceipt) (float64, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var bal float64
	if err := tx.QueryRow(`UPDATE rogerai.wallet SET balance=balance+$2 WHERE usr=$1 RETURNING balance`, user, held-cost).Scan(&bal); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO rogerai.earnings(node,balance) VALUES($1,$2)
		ON CONFLICT (node) DO UPDATE SET balance=rogerai.earnings.balance+$2`, node, ownerShare); err != nil {
		return 0, err
	}
	rj, _ := json.Marshal(rec)
	if _, err := tx.Exec(`INSERT INTO rogerai.receipts
		(request_id,usr,node,model,prompt_tokens,completion_tokens,cost,owner_share,ts,receipt)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (request_id) DO NOTHING`,
		rec.RequestID, user, node, rec.Model, rec.PromptTokens, rec.CompletionTokens, cost, ownerShare, rec.TS, rj); err != nil {
		return 0, err
	}
	return bal, tx.Commit()
}

func (p *Postgres) ReleaseHold(user string, held float64) (float64, error) {
	var bal float64
	err := p.db.QueryRow(`UPDATE rogerai.wallet SET balance=balance+$2 WHERE usr=$1 RETURNING balance`, user, held).Scan(&bal)
	return bal, err
}

// BindOwner upserts the owner binding for a pubkey, preserving created_at on
// refresh (a re-login with the same key keeps its original bind time).
func (p *Postgres) BindOwner(o Owner) error {
	_, err := p.db.Exec(`INSERT INTO rogerai.owners(pubkey,github_id,login) VALUES($1,$2,$3)
		ON CONFLICT (pubkey) DO UPDATE SET github_id=$2, login=$3`, o.Pubkey, o.GitHubID, o.Login)
	return err
}

func (p *Postgres) OwnerByPubkey(pubkey string) (Owner, bool, error) {
	var o Owner
	var created sql.NullTime
	err := p.db.QueryRow(`SELECT pubkey,github_id,login,created_at FROM rogerai.owners WHERE pubkey=$1`, pubkey).
		Scan(&o.Pubkey, &o.GitHubID, &o.Login, &created)
	if err == sql.ErrNoRows {
		return Owner{}, false, nil
	}
	if err != nil {
		return Owner{}, false, err
	}
	if created.Valid {
		o.CreatedAt = created.Time.Unix()
	}
	return o, true, nil
}

func (p *Postgres) Close() error { return p.db.Close() }
