package store

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// Postgres is a durable Store. Tables are prefixed `rogerai_` so they share an
// existing database cleanly. Swap this out for any other Store impl freely.
type Postgres struct {
	db     *sql.DB
	policy PayoutPolicy
}

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
    created_at TIMESTAMPTZ DEFAULT now());
-- account-hub fields (ACCOUNT-PAYOUTS-DESIGN section 9): extend owners into an account.
ALTER TABLE rogerai.owners ADD COLUMN IF NOT EXISTS email TEXT;
ALTER TABLE rogerai.owners ADD COLUMN IF NOT EXISTS stripe_connect_id TEXT;
ALTER TABLE rogerai.owners ADD COLUMN IF NOT EXISTS connect_status TEXT DEFAULT 'none';
ALTER TABLE rogerai.owners ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE rogerai.owners ADD COLUMN IF NOT EXISTS anonymized BOOLEAN DEFAULT false;
-- node -> operator account (owner pubkey) binding, so a node's earnings attribute
-- to an account at payout/Connect time. TOFU: first account to bind a node wins.
CREATE TABLE IF NOT EXISTS rogerai.node_owner (
    node TEXT PRIMARY KEY, account_id TEXT NOT NULL, created_at TIMESTAMPTZ DEFAULT now());
-- the append-only ledger (section 3.1): the source of truth. idem_key UNIQUE gives
-- idempotency for free on every money event.
CREATE TABLE IF NOT EXISTS rogerai.ledger (
    id BIGSERIAL PRIMARY KEY,
    holder TEXT NOT NULL,
    side TEXT NOT NULL,
    kind TEXT NOT NULL,
    amount DOUBLE PRECISION NOT NULL,
    idem_key TEXT UNIQUE,
    state TEXT NOT NULL DEFAULT 'posted',
    ref TEXT,
    ts BIGINT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now());
CREATE INDEX IF NOT EXISTS ledger_holder_ts ON rogerai.ledger (holder, id DESC);
CREATE INDEX IF NOT EXISTS ledger_kind ON rogerai.ledger (kind);
-- operator earnings lifecycle lots (section 6.1): held -> payable -> paid|clawed.
CREATE TABLE IF NOT EXISTS rogerai.earning_lots (
    id BIGSERIAL PRIMARY KEY,
    node TEXT, account_id TEXT, request_id TEXT,
    gross DOUBLE PRECISION, reserve DOUBLE PRECISION,
    state TEXT DEFAULT 'held',
    release_at BIGINT, reserve_release_at BIGINT,
    created_at BIGINT);
CREATE INDEX IF NOT EXISTS lots_account ON rogerai.earning_lots (account_id, state);
CREATE INDEX IF NOT EXISTS lots_request ON rogerai.earning_lots (request_id);
-- payout batches (one Stripe Transfer per operator per run).
CREATE TABLE IF NOT EXISTS rogerai.payouts (
    id BIGSERIAL PRIMARY KEY, account_id TEXT, amount DOUBLE PRECISION,
    stripe_transfer_id TEXT, state TEXT DEFAULT 'pending',
    idem_key TEXT UNIQUE, created_at BIGINT);
-- dispute / chargeback log (platform-liable events).
CREATE TABLE IF NOT EXISTS rogerai.disputes (
    id TEXT PRIMARY KEY, request_id TEXT, wallet TEXT, amount DOUBLE PRECISION,
    state TEXT, account_id TEXT, created_at BIGINT);
-- grant keys (GRANT-KEYS-DESIGN section 1.1): owner-issued private access keys.
-- secret_hash UNIQUE is the auth lookup key; the secret itself is never stored.
CREATE TABLE IF NOT EXISTS rogerai.grants (
    id           TEXT PRIMARY KEY,            -- grant_<rand>
    secret_hash  TEXT NOT NULL UNIQUE,        -- sha256(secret); never the secret
    owner        TEXT NOT NULL,               -- owner pubkey (rogerai.owners.pubkey)
    label        TEXT NOT NULL,
    nodes        JSONB DEFAULT '[]',          -- allowed node ids ([] = all owner nodes)
    models       JSONB DEFAULT '[]',          -- allowed models ([] = any)
    free         BOOLEAN DEFAULT false,
    price_in     DOUBLE PRECISION DEFAULT 0,
    price_out    DOUBLE PRECISION DEFAULT 0,
    rpm          DOUBLE PRECISION DEFAULT 0,
    burst        DOUBLE PRECISION DEFAULT 0,
    daily_cap    BIGINT DEFAULT 0,
    monthly_cap  BIGINT DEFAULT 0,
    self         BOOLEAN DEFAULT false,
    expires_at   BIGINT DEFAULT 0,
    revoked      BOOLEAN DEFAULT false,
    created_at   BIGINT NOT NULL);
CREATE INDEX IF NOT EXISTS grants_owner ON rogerai.grants (owner);
-- per-grant token usage rollup (daily/monthly cap check + dashboard). bucket is the UTC day/month key (window was a
-- UTC day ("YYYY-MM-DD") or month ("YYYY-MM"); tokens accumulate at settle time.
CREATE TABLE IF NOT EXISTS rogerai.grant_usage (
    grant_id TEXT NOT NULL, bucket TEXT NOT NULL, tokens BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (grant_id, bucket));
-- tag receipts with the grant that served them (NULL for public-market traffic),
-- so the dashboard can GROUP BY grant_id. Additive, like owner_share.
ALTER TABLE rogerai.receipts ADD COLUMN IF NOT EXISTS grant_id TEXT;
CREATE INDEX IF NOT EXISTS receipts_grant ON rogerai.receipts (grant_id);`

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
	return &Postgres{db: db, policy: LoadPayoutPolicy()}, nil
}

// appendLedger writes one append-only money event inside the caller's transaction.
// A duplicate idem_key is a no-op (ON CONFLICT DO NOTHING) - idempotency for free.
// idemKey="" means "no idempotency key" (a NULL row that never conflicts).
func appendLedger(tx *sql.Tx, holder, side, kind string, amount float64, idemKey, state, ref string, ts int64) error {
	var ik any
	if idemKey != "" {
		ik = idemKey
	}
	if ts == 0 {
		ts = time.Now().Unix()
	}
	_, err := tx.Exec(`INSERT INTO rogerai.ledger(holder,side,kind,amount,idem_key,state,ref,ts)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (idem_key) DO NOTHING`,
		holder, side, kind, amount, ik, state, ref, ts)
	return err
}

// addLot creates an operator earning lot (+ earn/reserve ledger rows) for a node's
// owner-share inside the caller's transaction. No-op if the node has no bound account.
func (p *Postgres) addLot(tx *sql.Tx, node, requestID string, ownerShare float64, now time.Time) error {
	if ownerShare <= 0 {
		return nil
	}
	var acct string
	err := tx.QueryRow(`SELECT account_id FROM rogerai.node_owner WHERE node=$1`, node).Scan(&acct)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	reserve := ownerShare * p.policy.Reserve
	rel := now.Add(p.policy.holdDuration()).Unix()
	if _, err := tx.Exec(`INSERT INTO rogerai.earning_lots
		(node,account_id,request_id,gross,reserve,state,release_at,reserve_release_at,created_at)
		VALUES($1,$2,$3,$4,$5,'held',$6,$6,$7)`,
		node, acct, requestID, ownerShare, reserve, rel, now.Unix()); err != nil {
		return err
	}
	if err := appendLedger(tx, acct, "operator", KindEarn, ownerShare, "earn:"+requestID, StatePending, requestID, now.Unix()); err != nil {
		return err
	}
	if reserve > 0 {
		if err := appendLedger(tx, acct, "operator", KindReserveHold, -reserve, "reserve:"+requestID, StatePending, requestID, now.Unix()); err != nil {
			return err
		}
	}
	return nil
}

func (p *Postgres) BalanceOf(user string, seed float64) (float64, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO rogerai.wallet(usr,balance) VALUES($1,$2) ON CONFLICT (usr) DO NOTHING`, user, seed)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 1 && seed != 0 {
		// A freshly-seeded wallet gets a ledger row, so the re-derivation drift check
		// matches (idem_key keeps the seed row unique per wallet).
		if err := appendLedger(tx, user, "consumer", KindAdjustment, seed, "seed:"+user, StatePosted, "seed", 0); err != nil {
			return 0, err
		}
	}
	var bal float64
	if err := tx.QueryRow(`SELECT balance FROM rogerai.wallet WHERE usr=$1`, user).Scan(&bal); err != nil {
		return 0, err
	}
	return bal, tx.Commit()
}

// SeedOnce grants starter credits to a wallet exactly once. The seed ledger row's
// idem_key "seed:<wallet>" is the unique guard: a duplicate insert is a no-op, so a
// re-login never re-seeds. seeded reports whether this call applied the credit.
func (p *Postgres) SeedOnce(wallet string, seed float64) (float64, bool, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	// Ensure a wallet row exists (balance 0); the credit is applied below only when
	// the seed ledger row is newly inserted, so the amount lands at most once.
	if _, err := tx.Exec(`INSERT INTO rogerai.wallet(usr,balance) VALUES($1,0) ON CONFLICT (usr) DO NOTHING`, wallet); err != nil {
		return 0, false, err
	}
	seeded := false
	if seed != 0 {
		res, err := tx.Exec(`INSERT INTO rogerai.ledger(holder,side,kind,amount,idem_key,state,ref,ts)
			VALUES($1,'consumer',$2,$3,$4,$5,'seed',$6) ON CONFLICT (idem_key) DO NOTHING`,
			wallet, KindAdjustment, seed, "seed:"+wallet, StatePosted, time.Now().Unix())
		if err != nil {
			return 0, false, err
		}
		// Apply the credit only when the seed ledger row was newly inserted (so a
		// re-login, which finds the row already present, never re-credits).
		if n, _ := res.RowsAffected(); n == 1 {
			seeded = true
			if _, err := tx.Exec(`UPDATE rogerai.wallet SET balance=balance+$2 WHERE usr=$1`, wallet, seed); err != nil {
				return 0, false, err
			}
		}
	}
	var bal float64
	if err := tx.QueryRow(`SELECT balance FROM rogerai.wallet WHERE usr=$1`, wallet).Scan(&bal); err != nil {
		return 0, false, err
	}
	return bal, seeded, tx.Commit()
}

// PeekBalance returns a wallet's balance without seeding it (0 if it doesn't exist).
func (p *Postgres) PeekBalance(wallet string) (float64, error) {
	var bal float64
	err := p.db.QueryRow(`SELECT balance FROM rogerai.wallet WHERE usr=$1`, wallet).Scan(&bal)
	if err == sql.ErrNoRows {
		return 0, nil
	}
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
		(request_id,usr,node,model,prompt_tokens,completion_tokens,cost,owner_share,ts,receipt,grant_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (request_id) DO NOTHING`,
		rec.RequestID, user, node, rec.Model, rec.PromptTokens, rec.CompletionTokens, cost, ownerShare, rec.TS, rj, nullStr(rec.GrantID)); err != nil {
		return 0, err
	}
	if err := appendLedger(tx, user, "consumer", KindSpend, -cost, "spend:"+rec.RequestID, StatePosted, rec.RequestID, rec.TS); err != nil {
		return 0, err
	}
	if err := p.addLot(tx, node, rec.RequestID, ownerShare, time.Now()); err != nil {
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
	tx, err := p.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var bal float64
	if err := tx.QueryRow(`INSERT INTO rogerai.wallet(usr,balance) VALUES($1,$2)
		ON CONFLICT (usr) DO UPDATE SET balance=rogerai.wallet.balance+$2 RETURNING balance`, user, amount).Scan(&bal); err != nil {
		return 0, err
	}
	if err := appendLedger(tx, user, "consumer", KindTopup, amount, "", StatePosted, "", 0); err != nil {
		return 0, err
	}
	return bal, tx.Commit()
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
	if err := appendLedger(tx, user, "consumer", KindTopup, amount, key, StatePosted, key, 0); err != nil {
		return false, 0, err
	}
	return true, bal, tx.Commit()
}

// Hold atomically reserves credits: the WHERE balance>=amount makes concurrent
// holds serialize at the row, so a wallet can never be driven negative.
func (p *Postgres) Hold(user string, amount float64) (bool, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE rogerai.wallet SET balance=balance-$2 WHERE usr=$1 AND balance>=$2`, user, amount)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil // balance can't cover it; nothing committed
	}
	if err := appendLedger(tx, user, "consumer", KindHold, -amount, "", StatePending, "", 0); err != nil {
		return false, err
	}
	return true, tx.Commit()
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
		(request_id,usr,node,model,prompt_tokens,completion_tokens,cost,owner_share,ts,receipt,grant_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (request_id) DO NOTHING`,
		rec.RequestID, user, node, rec.Model, rec.PromptTokens, rec.CompletionTokens, cost, ownerShare, rec.TS, rj, nullStr(rec.GrantID)); err != nil {
		return 0, err
	}
	// Capture: release the full reservation then debit the actual spend. Net wallet
	// delta == held-cost, matching the cache update above.
	if err := appendLedger(tx, user, "consumer", KindHoldRelease, held, "", StatePosted, rec.RequestID, rec.TS); err != nil {
		return 0, err
	}
	if err := appendLedger(tx, user, "consumer", KindSpend, -cost, "spend:"+rec.RequestID, StatePosted, rec.RequestID, rec.TS); err != nil {
		return 0, err
	}
	if err := p.addLot(tx, node, rec.RequestID, ownerShare, time.Now()); err != nil {
		return 0, err
	}
	return bal, tx.Commit()
}

func (p *Postgres) ReleaseHold(user string, held float64) (float64, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var bal float64
	if err := tx.QueryRow(`UPDATE rogerai.wallet SET balance=balance+$2 WHERE usr=$1 RETURNING balance`, user, held).Scan(&bal); err != nil {
		return 0, err
	}
	if err := appendLedger(tx, user, "consumer", KindHoldRelease, held, "", StatePosted, "", 0); err != nil {
		return 0, err
	}
	return bal, tx.Commit()
}

// BindOwner upserts the owner binding for a pubkey, preserving created_at on
// refresh (a re-login with the same key keeps its original bind time).
func (p *Postgres) BindOwner(o Owner) error {
	_, err := p.db.Exec(`INSERT INTO rogerai.owners(pubkey,github_id,login) VALUES($1,$2,$3)
		ON CONFLICT (pubkey) DO UPDATE SET github_id=$2, login=$3`, o.Pubkey, o.GitHubID, o.Login)
	return err
}

func (p *Postgres) OwnerByPubkey(pubkey string) (Owner, bool, error) {
	return p.scanOwner(`SELECT pubkey,github_id,login,created_at,email,stripe_connect_id,connect_status,deleted_at,anonymized
		FROM rogerai.owners WHERE pubkey=$1`, pubkey)
}

func (p *Postgres) OwnerByLogin(login string) (Owner, bool, error) {
	return p.scanOwner(`SELECT pubkey,github_id,login,created_at,email,stripe_connect_id,connect_status,deleted_at,anonymized
		FROM rogerai.owners WHERE login=$1 AND NOT COALESCE(anonymized,false)`, login)
}

// scanOwner runs a single-row owner query, mapping NULL columns to zero values.
func (p *Postgres) scanOwner(query string, arg string) (Owner, bool, error) {
	var o Owner
	var created, deleted sql.NullTime
	var email, connectID, connectStatus sql.NullString
	var anon sql.NullBool
	err := p.db.QueryRow(query, arg).Scan(
		&o.Pubkey, &o.GitHubID, &o.Login, &created, &email, &connectID, &connectStatus, &deleted, &anon)
	if err == sql.ErrNoRows {
		return Owner{}, false, nil
	}
	if err != nil {
		return Owner{}, false, err
	}
	if created.Valid {
		o.CreatedAt = created.Time.Unix()
	}
	if deleted.Valid {
		o.DeletedAt = deleted.Time.Unix()
	}
	o.Email = email.String
	o.ConnectID = connectID.String
	o.ConnectStatus = connectStatus.String
	o.Anonymized = anon.Bool
	return o, true, nil
}

func (p *Postgres) UpdateAccount(login, email string) (Owner, bool, error) {
	res, err := p.db.Exec(`UPDATE rogerai.owners SET email=$2 WHERE login=$1 AND NOT COALESCE(anonymized,false)`, login, email)
	if err != nil {
		return Owner{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Owner{}, false, nil
	}
	return p.OwnerByLogin(login)
}

func (p *Postgres) SetConnect(login, connectID, status string) error {
	_, err := p.db.Exec(`UPDATE rogerai.owners SET stripe_connect_id=$2, connect_status=$3
		WHERE login=$1 AND NOT COALESCE(anonymized,false)`, login, connectID, status)
	return err
}

func (p *Postgres) DeleteAccount(login string) (bool, error) {
	// Soft-delete + anonymize: scrub email/login, mark deleted. Financial rows
	// (ledger, receipts, earning_lots, payouts) are retained, de-identified by the
	// opaque pubkey. The login is replaced so it can never be resolved again.
	res, err := p.db.Exec(`UPDATE rogerai.owners
		SET email=NULL, login='deleted_'||left(md5(pubkey),8), anonymized=true, deleted_at=now()
		WHERE login=$1 AND NOT COALESCE(anonymized,false)`, login)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (p *Postgres) BindNode(node, accountID string) error {
	_, err := p.db.Exec(`INSERT INTO rogerai.node_owner(node,account_id) VALUES($1,$2)
		ON CONFLICT (node) DO NOTHING`, node, accountID) // TOFU: first account wins
	return err
}

func (p *Postgres) AccountOfNode(node string) (string, bool, error) {
	var a string
	err := p.db.QueryRow(`SELECT account_id FROM rogerai.node_owner WHERE node=$1`, node).Scan(&a)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return a, err == nil, err
}

func (p *Postgres) NodesOfAccount(accountID string) ([]string, error) {
	rows, err := p.db.Query(`SELECT node FROM rogerai.node_owner WHERE account_id=$1`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (p *Postgres) LedgerOf(holder string, kinds []string, limit int) ([]LedgerRow, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT id,holder,side,kind,amount,COALESCE(idem_key,''),state,COALESCE(ref,''),ts
		FROM rogerai.ledger WHERE holder=$1`
	args := []any{holder}
	if len(kinds) > 0 {
		q += ` AND kind = ANY($2)`
		args = append(args, kinds)
		q += ` ORDER BY id DESC LIMIT $3`
		args = append(args, limit)
	} else {
		q += ` ORDER BY id DESC LIMIT $2`
		args = append(args, limit)
	}
	rows, err := p.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LedgerRow
	for rows.Next() {
		var r LedgerRow
		if err := rows.Scan(&r.ID, &r.Holder, &r.Side, &r.Kind, &r.Amount, &r.IdemKey, &r.State, &r.Ref, &r.TS); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *Postgres) DeriveBalance(holder string) (float64, error) {
	var sum float64
	err := p.db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM rogerai.ledger
		WHERE holder=$1 AND state<>'reversed'
		AND kind IN ('topup','spend','hold','hold_release','refund','chargeback','adjustment')`, holder).Scan(&sum)
	return sum, err
}

// promoteLots sweeps held lots to payable when their release time has passed, in
// one transaction (sweep-on-read). Emits a reserve_release ledger row when the
// reserve tail also clears.
func (p *Postgres) promoteLots(now time.Time) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE rogerai.earning_lots SET state='payable'
		WHERE state='held' AND release_at<=$1`, now.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func (p *Postgres) splitQuery(col, val string, now time.Time) (EarningSplit, error) {
	if err := p.promoteLots(now); err != nil {
		return EarningSplit{}, err
	}
	var s EarningSplit
	n := now.Unix()
	// held: still-held lots (gross-minus-reserve) + their reserve.
	// payable: payable lots' gross-minus-reserve, plus reserve once its tail clears.
	// reserved: reserve still inside its release tail (held lots + payable lots whose
	//           reserve tail hasn't cleared). paid: paid lots.
	row := p.db.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN state='held' THEN gross-reserve ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN state='held' THEN reserve
		                  WHEN state='payable' AND reserve_release_at>$2 THEN reserve ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN state='payable' THEN gross-reserve
		                  + CASE WHEN reserve_release_at<=$2 THEN reserve ELSE 0 END ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN state='paid' THEN gross ELSE 0 END),0),
		COALESCE(MIN(CASE WHEN state='held' THEN release_at
		                  WHEN state='payable' AND reserve_release_at>$2 THEN reserve_release_at END),0)
		FROM rogerai.earning_lots WHERE `+col+`=$1`, val, n)
	if err := row.Scan(&s.Held, &s.Reserved, &s.Payable, &s.Paid, &s.NextRelease); err != nil {
		return EarningSplit{}, err
	}
	return s, nil
}

func (p *Postgres) EarningSplitOf(accountID string, now time.Time) (EarningSplit, error) {
	return p.splitQuery("account_id", accountID, now)
}

func (p *Postgres) EarningSplitOfNode(node string, now time.Time) (EarningSplit, error) {
	return p.splitQuery("node", node, now)
}

func (p *Postgres) RequestPayout(accountID string, now time.Time, minPayout float64, transferID string) (Payout, bool, string, error) {
	if err := p.promoteLots(now); err != nil {
		return Payout{}, false, "", err
	}
	tx, err := p.db.Begin()
	if err != nil {
		return Payout{}, false, "", err
	}
	defer tx.Rollback()
	n := now.Unix()
	// Sum the payable amount (gross-minus-reserve, plus reserve whose tail cleared).
	var amount float64
	if err := tx.QueryRow(`SELECT COALESCE(SUM(gross-reserve + CASE WHEN reserve_release_at<=$2 THEN reserve ELSE 0 END),0)
		FROM rogerai.earning_lots WHERE account_id=$1 AND state='payable'`, accountID, n).Scan(&amount); err != nil {
		return Payout{}, false, "", err
	}
	if amount < minPayout {
		return Payout{}, false, "below minimum payout", nil
	}
	if _, err := tx.Exec(`UPDATE rogerai.earning_lots SET state='paid'
		WHERE account_id=$1 AND state='payable'`, accountID); err != nil {
		return Payout{}, false, "", err
	}
	state := PayoutPending
	if transferID != "" {
		state = PayoutPaid
	}
	var pid int64
	if err := tx.QueryRow(`INSERT INTO rogerai.payouts(account_id,amount,stripe_transfer_id,state,created_at)
		VALUES($1,$2,$3,$4,$5) RETURNING id`, accountID, amount, transferID, state, n).Scan(&pid); err != nil {
		return Payout{}, false, "", err
	}
	if err := appendLedger(tx, accountID, "operator", KindPayout, -amount, "payout:"+strconv.FormatInt(pid, 10), StatePosted, transferID, n); err != nil {
		return Payout{}, false, "", err
	}
	if err := tx.Commit(); err != nil {
		return Payout{}, false, "", err
	}
	return Payout{ID: pid, AccountID: accountID, Amount: amount, StripeTransferID: transferID, State: state, CreatedAt: n}, true, "", nil
}

func (p *Postgres) PayoutsOf(accountID string, limit int) ([]Payout, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := p.db.Query(`SELECT id,account_id,amount,COALESCE(stripe_transfer_id,''),state,created_at
		FROM rogerai.payouts WHERE account_id=$1 ORDER BY id DESC LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Payout
	for rows.Next() {
		var po Payout
		if err := rows.Scan(&po.ID, &po.AccountID, &po.Amount, &po.StripeTransferID, &po.State, &po.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, po)
	}
	return out, rows.Err()
}

func (p *Postgres) Chargeback(disputeID, wallet, requestID string, amount float64, now time.Time) (float64, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// Idempotent on the stripe dispute id: a fresh insert means first delivery.
	res, err := tx.Exec(`INSERT INTO rogerai.disputes(id,request_id,wallet,amount,state,created_at)
		VALUES($1,$2,$3,$4,'open',$5) ON CONFLICT (id) DO NOTHING`, disputeID, requestID, wallet, amount, now.Unix())
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, tx.Commit() // already processed
	}
	if _, err := tx.Exec(`UPDATE rogerai.wallet SET balance=balance-$2 WHERE usr=$1`, wallet, amount); err != nil {
		return 0, err
	}
	if err := appendLedger(tx, wallet, "consumer", KindChargeback, -amount, "dispute:"+disputeID, StatePosted, disputeID, now.Unix()); err != nil {
		return 0, err
	}
	// Claw back operator earnings from the same request while not yet paid out.
	rows, err := tx.Query(`SELECT id,account_id,gross FROM rogerai.earning_lots
		WHERE request_id=$1 AND state IN ('held','payable')`, requestID)
	if err != nil {
		return 0, err
	}
	type claw struct {
		id    int64
		acct  string
		gross float64
	}
	var claws []claw
	for rows.Next() {
		var c claw
		if err := rows.Scan(&c.id, &c.acct, &c.gross); err != nil {
			rows.Close()
			return 0, err
		}
		claws = append(claws, c)
	}
	rows.Close()
	var clawed float64
	for _, c := range claws {
		if _, err := tx.Exec(`UPDATE rogerai.earning_lots SET state='clawed' WHERE id=$1`, c.id); err != nil {
			return 0, err
		}
		if err := appendLedger(tx, c.acct, "operator", KindAdjustment, -c.gross, "claw:"+disputeID+":"+strconv.FormatInt(c.id, 10), StatePosted, disputeID, now.Unix()); err != nil {
			return 0, err
		}
		clawed += c.gross
	}
	return clawed, tx.Commit()
}

func (p *Postgres) OpenDisputeCount(accountID string) (int, error) {
	var n int
	err := p.db.QueryRow(`SELECT COUNT(*) FROM rogerai.disputes d
		JOIN rogerai.earning_lots l ON l.request_id=d.request_id
		WHERE l.account_id=$1 AND d.state='open'`, accountID).Scan(&n)
	return n, err
}

func (p *Postgres) Close() error { return p.db.Close() }
