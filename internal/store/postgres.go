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

	// seedLimit caps how many distinct wallets ever receive a non-zero starter seed
	// (<=0 = unlimited). Set via SetSeedLimit at startup; read on the seed path. A
	// plain field is safe: it is set once before serving and only read thereafter.
	seedLimit int
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
-- GitHub display name (welcome-email personalization) + the durable once-only stamp for
-- the welcome email (NULL = never welcomed). welcomed_at is what makes the welcome fire
-- exactly once across first-bind and a later email-set.
ALTER TABLE rogerai.owners ADD COLUMN IF NOT EXISTS name TEXT;
ALTER TABLE rogerai.owners ADD COLUMN IF NOT EXISTS welcomed_at TIMESTAMPTZ;
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
    payout_id BIGINT,
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
-- completed-checkout -> charge mapping. A charge.dispute.created object carries NONE
-- of the checkout metadata (no metadata.user/request_id), only a payment_intent +
-- charge id, so persist the (wallet, credits) at checkout.session.completed keyed on
-- BOTH ids to resolve the consumer wallet at dispute time. Append-only-friendly:
-- keyed on the session id, written once (idempotent on Stripe redelivery).
CREATE TABLE IF NOT EXISTS rogerai.checkout_charges (
    session_id TEXT PRIMARY KEY,
    payment_intent TEXT, charge TEXT,
    wallet TEXT NOT NULL, credits DOUBLE PRECISION NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now());
CREATE INDEX IF NOT EXISTS checkout_charges_pi ON rogerai.checkout_charges (payment_intent);
CREATE INDEX IF NOT EXISTS checkout_charges_ch ON rogerai.checkout_charges (charge);
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
CREATE INDEX IF NOT EXISTS receipts_grant ON rogerai.receipts (grant_id);
-- private bands ("frequency codes": private discovery, BANDS-DESIGN). A band makes
-- a node reachable ONLY to whoever knows its secret code, while hiding it from the
-- public /discover + /market views. code_hash UNIQUE is the resolve lookup key
-- (sha256 of the canonical Crockford tail only - the cosmetic "147.520 MHz" part is
-- NEVER folded into the key); the secret code itself is shown ONCE at mint and never
-- stored. code_display is the cosmetic full string for the owner's own re-display
-- (NOT secret). One band per node (node_id index = idempotent re-register lookup).
CREATE TABLE IF NOT EXISTS rogerai.private_bands (
    id           TEXT PRIMARY KEY,            -- band_<rand>
    code_hash    TEXT NOT NULL UNIQUE,        -- sha256(canonical secret tail); never the code
    code_display TEXT NOT NULL,               -- cosmetic "147.520 MHz · 8F3K-9M2Q" (not secret)
    owner        TEXT NOT NULL,               -- owner pubkey (rogerai.owners.pubkey)
    label        TEXT NOT NULL DEFAULT '',
    node_id      TEXT NOT NULL,               -- the private node this band routes to
    models       JSONB DEFAULT '[]',          -- allowed models ([] = any the node offers)
    expires_at   BIGINT DEFAULT 0,            -- unix; 0 = never (Phase 2 packs add expiry)
    revoked      BOOLEAN DEFAULT false,
    created_at   BIGINT NOT NULL);
CREATE INDEX IF NOT EXISTS private_bands_owner ON rogerai.private_bands (owner);
CREATE INDEX IF NOT EXISTS private_bands_node ON rogerai.private_bands (node_id);
-- tag paid lots with the payout that paid them, so a failed transfer can roll the
-- exact lots back to 'payable'. Additive.
ALTER TABLE rogerai.earning_lots ADD COLUMN IF NOT EXISTS payout_id BIGINT;
-- seed cap (bound free-credit liability): seed_grants is the per-wallet "this wallet
-- was offered the starter seed" guard (one row per wallet, idempotent); seed_counter
-- is the single-row durable count of wallets actually granted a non-zero seed. The
-- grant + the counter bump happen in ONE statement under the cap predicate, so the
-- total seeded never exceeds the configured limit even under concurrency.
CREATE TABLE IF NOT EXISTS rogerai.seed_grants (wallet TEXT PRIMARY KEY, created_at TIMESTAMPTZ DEFAULT now());
CREATE TABLE IF NOT EXISTS rogerai.seed_counter (id INT PRIMARY KEY, count BIGINT NOT NULL DEFAULT 0);
INSERT INTO rogerai.seed_counter(id,count) VALUES(1,0) ON CONFLICT (id) DO NOTHING;
-- seed_remaining tracks the UNSPENT seed (free) portion of each wallet's balance, so
-- the earning path can separate free (seed) spend from real (cleared-topup) spend: an
-- operator must NOT be able to mint a payable earning from another account's free seed
-- credits (P0-1). Seed is drained BEFORE real credits on spend; only the real
-- remainder mints an operator earning lot. Additive; defaults to 0 for existing rows.
ALTER TABLE rogerai.wallet ADD COLUMN IF NOT EXISTS seed_remaining DOUBLE PRECISION NOT NULL DEFAULT 0;
-- recount_holds: nodes with an OPEN L1 re-count discrepancy. While a node is held its
-- earning lots are NOT promoted held->payable (P0-2), so an over-reporting node's
-- earnings stay un-cashable pending review. One row per held node (idempotent).
CREATE TABLE IF NOT EXISTS rogerai.recount_holds (node TEXT PRIMARY KEY, created_at TIMESTAMPTZ DEFAULT now());
-- persisted node registry: the durable copy of the broker's in-memory node table,
-- so a broker restart/redeploy RE-HYDRATES who is registered instead of wiping it
-- (older provider binaries that don't auto-re-register would otherwise 404 forever).
-- reg is the full protocol.NodeRegistration JSON (pubkey, offers+pricing, HW, region,
-- bridge token, attestation); last_seen carries a short liveness grace across the
-- restart window; registered_at is set once. Liveness stays gated on a fresh
-- heartbeat/poll - this only stops the registry from being lost.
CREATE TABLE IF NOT EXISTS rogerai.nodes (
    node_id       TEXT PRIMARY KEY,
    reg           JSONB NOT NULL,
    confidential  BOOLEAN NOT NULL DEFAULT false,
    last_seen     BIGINT NOT NULL DEFAULT 0,
    registered_at BIGINT NOT NULL DEFAULT 0);
-- owner-authored price/schedule overrides set from the web Console. The broker seeds
-- a node's in-memory offer from here on every register (so the owner's web-set price
-- survives node re-registration + a broker restart); ActivePrice reads it at serve
-- time. owner is the authoring owner pubkey (the scope: an override never shadows
-- another account's node). schedule is the JSON-encoded []protocol.PriceWindow. This
-- only records a PUBLISHED/future price - past receipts/ledger are never touched.
CREATE TABLE IF NOT EXISTS rogerai.offer_overrides (
    node       TEXT NOT NULL,
    model      TEXT NOT NULL,
    owner      TEXT NOT NULL,
    price_in   DOUBLE PRECISION NOT NULL DEFAULT 0,
    price_out  DOUBLE PRECISION NOT NULL DEFAULT 0,
    schedule   JSONB NOT NULL DEFAULT '[]'::jsonb,
    updated_at BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (node, model));
CREATE INDEX IF NOT EXISTS offer_overrides_owner ON rogerai.offer_overrides (owner);
-- safety: preserved child-exploitation hits (18 USC 2258A). ACCESS-RESTRICTED +
-- retention-limited: the offending prompt is stored ENCRYPTED-AT-REST (the broker
-- encrypts before insert; the column is ciphertext, never plaintext). report_state
-- tracks the CyberTipline obligation (queued -> reported). pseudonym is the opaque
-- per-(user,node) id (never the real user); ip + category aid the report.
CREATE TABLE IF NOT EXISTS rogerai.csam_incidents (
    id           BIGSERIAL PRIMARY KEY,
    pseudonym    TEXT NOT NULL,
    ip           TEXT,
    category     TEXT,
    content      BYTEA NOT NULL,                 -- broker-encrypted ciphertext
    report_state TEXT NOT NULL DEFAULT 'queued', -- queued -> reported
    created_at   BIGINT NOT NULL);
CREATE INDEX IF NOT EXISTS csam_state ON rogerai.csam_incidents (report_state, id DESC);
-- abuse/quality reports (POST /report; may be anonymous). The per-node count drives
-- the auto-eject ban threshold. ip is the reporter (abuse-of-reporting forensics).
CREATE TABLE IF NOT EXISTS rogerai.reports (
    id         BIGSERIAL PRIMARY KEY,
    category   TEXT NOT NULL,
    node_id    TEXT,
    request_id TEXT,
    detail     TEXT,
    ip         TEXT,
    created_at BIGINT NOT NULL);
CREATE INDEX IF NOT EXISTS reports_node ON rogerai.reports (node_id);
-- banned/ejected nodes: flipped OUT of pick/market/discover. Re-hydrated at startup so
-- a ban survives a restart. reason records why (report threshold, manual, etc).
CREATE TABLE IF NOT EXISTS rogerai.banned_nodes (
    node_id    TEXT PRIMARY KEY,
    reason     TEXT,
    created_at TIMESTAMPTZ DEFAULT now());
-- self-serve appeals (ban hardening 3.3): a banned/struck operator files an appeal that
-- lands in the admin review queue. account_id is the AUTHENTICATED owner pubkey (never a
-- request-supplied account), so an appeal can only be filed for the caller. node_id is
-- optional (set when appealing a specific node ban). state: open -> resolved.
CREATE TABLE IF NOT EXISTS rogerai.appeals (
    id         BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL,
    node_id    TEXT,
    reason     TEXT,
    state      TEXT NOT NULL DEFAULT 'open',
    note       TEXT,
    created_at BIGINT NOT NULL);
CREATE INDEX IF NOT EXISTS appeals_acct ON rogerai.appeals (account_id, id DESC);
CREATE INDEX IF NOT EXISTS appeals_open ON rogerai.appeals (id DESC) WHERE state='open';
-- reporter-IP + window index so the distinct-reporter corroboration count (the ban
-- decision) stays cheap as the report log grows.
CREATE INDEX IF NOT EXISTS reports_node_ip_ts ON rogerai.reports (node_id, ip, created_at);
-- owner-keyed durable bans (anti-rotation): a node_id is a cheap callsign, so the
-- enforcement that must survive rotation binds to the OWNER ACCOUNT (owner pubkey).
-- A banned owner is blocked at register + relay pick + settle for every current and
-- future node. Re-hydrated at startup so the ban survives a restart. evidence holds
-- the provable record (signed-claim vs broker-recount) the operator can be shown.
CREATE TABLE IF NOT EXISTS rogerai.banned_owners (
    account_id TEXT PRIMARY KEY,
    reason     TEXT,
    evidence   JSONB,
    created_at TIMESTAMPTZ DEFAULT now());
-- owner strikes: append-only evidence-bound anti-abuse marks against an owner account.
-- At a threshold the owner is warned then banned. The evidence is provable (the node's
-- own signed claim vs the broker recount / the empty body / the impossible byte-floor)
-- so the operator can be SHOWN exactly why. idem_key (when set) makes a retried request
-- non-double-striking. Bound to the durable owner pubkey, NOT the cheap node id.
CREATE TABLE IF NOT EXISTS rogerai.owner_strikes (
    id         BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL,
    kind       TEXT NOT NULL,
    evidence   JSONB,
    idem_key   TEXT UNIQUE,
    created_at BIGINT NOT NULL);
CREATE INDEX IF NOT EXISTS owner_strikes_acct ON rogerai.owner_strikes (account_id, id DESC);
-- account_recount_holds: OWNER-level promotion hold (the owner twin of recount_holds).
-- While an owner is held, ALL of its earning lots are kept from held->payable, so the
-- hold survives a node-id rotation. One row per held owner (idempotent).
CREATE TABLE IF NOT EXISTS rogerai.account_recount_holds (account_id TEXT PRIMARY KEY, created_at TIMESTAMPTZ DEFAULT now());
-- pending_reversals: durable Stripe Transfer Reversal intents still owed on disputed,
-- already-paid lots (FAILED-REVERSAL RETRY / silent-money-leak guard). The ledger
-- clawback is recorded synchronously, but the money rail can transiently fail; this row
-- captures the intent so a background sweep retries it instead of dropping it. key =
-- "reverse:<dispute>:<lot>" (the Stripe Idempotency-Key), so a webhook redelivery or a
-- retry never double-records or double-reverses. A row is swept until done=true or it
-- hits the max attempts and is parked as dead_letter=true for manual handling.
CREATE TABLE IF NOT EXISTS rogerai.pending_reversals (
    key          TEXT PRIMARY KEY,
    dispute_id   TEXT NOT NULL,
    lot_id       BIGINT NOT NULL,
    account_id   TEXT,
    transfer_id  TEXT,
    amount       DOUBLE PRECISION NOT NULL,
    attempts     INT NOT NULL DEFAULT 0,
    done         BOOLEAN NOT NULL DEFAULT false,
    dead_letter  BOOLEAN NOT NULL DEFAULT false,
    last_error   TEXT,
    created_at   BIGINT NOT NULL,
    last_attempt BIGINT NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS pending_reversals_open ON rogerai.pending_reversals (created_at) WHERE done=false AND dead_letter=false;
-- per-account settings (the monthly spend cap = a budget limit, modeled on Groq's
-- "set a max you'll pay per month"). monthly_cap is a $ ceiling on captured spend per
-- CALENDAR month (0 = unlimited). One row per wallet; the cap is durable and per
-- GitHub-linked wallet. Month-to-date is summed from the ledger (no counter to drift).
CREATE TABLE IF NOT EXISTS rogerai.account_settings (
    holder      TEXT PRIMARY KEY,
    monthly_cap DOUBLE PRECISION NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ DEFAULT now());
-- month-to-date spend is a (holder, kind=spend, ts-in-month) SUM; index the ts so the
-- calendar-month scan stays cheap as the ledger grows.
CREATE INDEX IF NOT EXISTS ledger_holder_kind_ts ON rogerai.ledger (holder, kind, ts);
-- per-model metrics (metrics.go): the provider rollup scans a node's receipts in the
-- trailing window then GROUPs BY (model,node); index (node, ts, model) so the windowed
-- node scan is range-bounded and the group key is covered. The consumer rollup reuses
-- receipts_usr_ts (usr, ts DESC).
CREATE INDEX IF NOT EXISTS receipts_node_ts_model ON rogerai.receipts (node, ts, model);`

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

// realEarnShareTx draws `cost` against the wallet's UNSPENT seed credits first and
// returns the operator share scaled to the REAL (non-seed) funded fraction of the
// cost. Seed-funded spend earns the operator NOTHING (P0-1) - it is treated like a
// free request on the operator side while the consumer still pays in full. cost<=0 or
// ownerShare<=0 returns 0 (and consumes no seed). Runs inside the caller's tx so the
// seed drawdown and the lot mint are atomic with the spend. Must be called EXACTLY
// once per settle (it mutates seed_remaining).
func (p *Postgres) realEarnShareTx(tx *sql.Tx, wallet string, cost, ownerShare float64) (float64, error) {
	if cost <= 0 || ownerShare <= 0 {
		return 0, nil
	}
	// Draw down the seed-funded remainder by min(cost, seed_remaining) and return how
	// much of this cost was seed-funded. A CTE captures the OLD seed_remaining so the
	// returned seedUsed is exact; LEAST clamps so seed_remaining never goes negative.
	var seedUsed float64
	if err := tx.QueryRow(`
		WITH cur AS (SELECT usr, seed_remaining AS old FROM rogerai.wallet WHERE usr=$1 FOR UPDATE),
		upd AS (
			UPDATE rogerai.wallet w SET seed_remaining = w.seed_remaining - LEAST(w.seed_remaining, $2)
			FROM cur WHERE w.usr = cur.usr RETURNING cur.old
		)
		SELECT LEAST(old, $2) FROM upd`, wallet, cost).Scan(&seedUsed); err != nil {
		if err == sql.ErrNoRows {
			return ownerShare, nil // no wallet row (shouldn't happen post-debit): treat as fully real
		}
		return 0, err
	}
	realFrac := (cost - seedUsed) / cost
	if realFrac <= 0 {
		return 0, nil
	}
	return ownerShare * realFrac, nil
}

func (p *Postgres) SetSeedLimit(limit int) { p.seedLimit = limit }

// SeedStatus reads the authoritative seed_counter (the durable count of distinct
// seeded wallets) and derives how many seeds remain under the configured cap.
// remaining is -1 when unlimited (seedLimit<=0).
func (p *Postgres) SeedStatus() (seeded, limit, remaining int, err error) {
	var count int64
	if err := p.db.QueryRow(`SELECT count FROM rogerai.seed_counter WHERE id=1`).Scan(&count); err != nil {
		if err == sql.ErrNoRows {
			count = 0
		} else {
			return 0, p.seedLimit, 0, err
		}
	}
	seeded, limit = int(count), p.seedLimit
	if limit <= 0 {
		return seeded, limit, -1, nil
	}
	remaining = limit - seeded
	if remaining < 0 {
		remaining = 0
	}
	return seeded, limit, remaining, nil
}

// grantSeedTx applies the starter seed to a wallet at most once, enforcing the seed
// cap atomically, inside the caller's transaction. It returns granted=true only when
// THIS call actually credited a non-zero seed (a new wallet AND the cap allowed it).
//
// Atomicity: one statement both claims the per-wallet seed slot (seed_grants insert)
// AND, only if newly claimed and under the cap, bumps seed_counter. The counter bump
// is the authoritative gate - we credit the wallet + post the seed ledger row ONLY
// when the bump succeeded, so the ledger never records a grant that didn't happen
// (DeriveBalance stays exact) and the count can never exceed the limit under load.
func (p *Postgres) grantSeedTx(tx *sql.Tx, wallet string, seed float64) (bool, error) {
	if seed == 0 {
		return false, nil
	}
	var newlyClaimed, bumped int
	// $2 = seedLimit (<=0 means unlimited). Claim the per-wallet slot; bump the global
	// counter only when this wallet is newly claimed AND the cap is not yet hit.
	err := tx.QueryRow(`
		WITH claim AS (
			INSERT INTO rogerai.seed_grants(wallet) VALUES($1)
			ON CONFLICT (wallet) DO NOTHING
			RETURNING wallet
		),
		bump AS (
			UPDATE rogerai.seed_counter SET count = count + 1
			WHERE id = 1 AND EXISTS(SELECT 1 FROM claim) AND ($2 <= 0 OR count < $2)
			RETURNING count
		)
		SELECT (SELECT count(*) FROM claim), (SELECT count(*) FROM bump)`,
		wallet, p.seedLimit).Scan(&newlyClaimed, &bumped)
	if err != nil {
		return false, err
	}
	if bumped == 0 {
		return false, nil // already seeded, or the cap is exhausted: no credit
	}
	// Cap allowed it: credit the wallet and post the seed ledger row (idem-keyed so the
	// re-derivation drift check matches and the row is unique per wallet). Track the
	// seed-funded portion separately (seed_remaining) so the earning path can tell free
	// (seed) spend from real spend - seed credits must never mint a payout (P0-1).
	if _, err := tx.Exec(`UPDATE rogerai.wallet SET balance=balance+$2, seed_remaining=seed_remaining+$2 WHERE usr=$1`, wallet, seed); err != nil {
		return false, err
	}
	if err := appendLedger(tx, wallet, "consumer", KindAdjustment, seed, "seed:"+wallet, StatePosted, "seed", 0); err != nil {
		return false, err
	}
	return true, nil
}

func (p *Postgres) BalanceOf(user string, seed float64) (float64, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// Ensure a wallet row exists at balance 0; the seed (if any) is applied by
	// grantSeedTx, which enforces the cap and credits at most once per wallet.
	if _, err := tx.Exec(`INSERT INTO rogerai.wallet(usr,balance) VALUES($1,0) ON CONFLICT (usr) DO NOTHING`, user); err != nil {
		return 0, err
	}
	if _, err := p.grantSeedTx(tx, user, seed); err != nil {
		return 0, err
	}
	var bal float64
	if err := tx.QueryRow(`SELECT balance FROM rogerai.wallet WHERE usr=$1`, user).Scan(&bal); err != nil {
		return 0, err
	}
	return bal, tx.Commit()
}

// SeedOnce grants starter credits to a wallet exactly once (seed_grants is the unique
// per-wallet guard), subject to the seed cap. A re-login never re-seeds. seeded
// reports whether this call newly claimed the wallet's seed slot; a non-zero credit
// additionally requires the cap to allow it (grantSeedTx).
func (p *Postgres) SeedOnce(wallet string, seed float64) (float64, bool, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	// Ensure a wallet row exists (balance 0); the credit lands at most once via the
	// per-wallet seed_grants guard inside grantSeedTx.
	if _, err := tx.Exec(`INSERT INTO rogerai.wallet(usr,balance) VALUES($1,0) ON CONFLICT (usr) DO NOTHING`, wallet); err != nil {
		return 0, false, err
	}
	// seeded reports whether THIS call actually granted a non-zero seed. The credit
	// correctness (at most once per wallet, capped) is fully carried by grantSeedTx;
	// the bool is advisory (auth.go ignores it).
	seeded, err := p.grantSeedTx(tx, wallet, seed)
	if err != nil {
		return 0, false, err
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
	// Idempotency claim: the receipt row IS the lock. A non-empty request id is
	// inserted FIRST (owner_share backfilled below once the seed-scaled share is
	// known); a duplicate finds the row already present, touches NO money, and
	// returns the unchanged balance. Without this gate a redelivered settle
	// re-debited the wallet, re-drew seed + re-credited earnings, and minted a
	// second lot - silently inflating both spend and operator payout.
	if won, bal, err := p.claimReceipt(tx, user, node, cost, rec); err != nil {
		return 0, err
	} else if !won {
		return bal, tx.Commit()
	}
	var bal float64
	if err := tx.QueryRow(`UPDATE rogerai.wallet SET balance=balance-$2 WHERE usr=$1 RETURNING balance`, user, cost).Scan(&bal); err != nil {
		return 0, err
	}
	// Only the REAL (non-seed) funded portion of this cost earns the operator (P0-1):
	// realEarnShareTx draws down seed_remaining and scales the owner share. Called once.
	earnShare, err := p.realEarnShareTx(tx, user, cost, ownerShare)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO rogerai.earnings(node,balance) VALUES($1,$2)
		ON CONFLICT (node) DO UPDATE SET balance=rogerai.earnings.balance+$2`, node, earnShare); err != nil {
		return 0, err
	}
	if err := p.fillEarnShare(tx, user, node, cost, rec, earnShare); err != nil {
		return 0, err
	}
	if err := appendLedger(tx, user, "consumer", KindSpend, -cost, "spend:"+rec.RequestID, StatePosted, rec.RequestID, rec.TS); err != nil {
		return 0, err
	}
	if err := appendAdjust(tx, user, rec, cost); err != nil {
		return 0, err
	}
	if err := p.addLot(tx, node, rec.RequestID, earnShare, time.Now()); err != nil {
		return 0, err
	}
	return bal, tx.Commit()
}

// claimReceipt is the idempotency gate for Settle/Finalize. For a non-empty
// request id it inserts the receipt row (with owner_share=0, backfilled by
// fillEarnShare once the seed-scaled share is computed) and reports whether THIS
// call won the claim. A losing call (the row already exists) gets won=false plus
// the wallet's current balance so the caller can commit a clean no-op. An empty
// request id carries no idempotency key, so it always "wins" and the receipt is
// written later with the real owner_share - preserving the legacy behaviour.
func (p *Postgres) claimReceipt(tx *sql.Tx, user, node string, cost float64, rec protocol.UsageReceipt) (bool, float64, error) {
	if rec.RequestID == "" {
		return true, 0, nil
	}
	rj, _ := json.Marshal(rec)
	bpt, bct := billedTokens(rec)
	res, err := tx.Exec(`INSERT INTO rogerai.receipts
		(request_id,usr,node,model,prompt_tokens,completion_tokens,cost,owner_share,ts,receipt,grant_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,0,$8,$9,$10) ON CONFLICT (request_id) DO NOTHING`,
		rec.RequestID, user, node, rec.Model, bpt, bct, cost, rec.TS, rj, nullStr(rec.GrantID))
	if err != nil {
		return false, 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var bal float64
		if err := tx.QueryRow(`SELECT COALESCE(balance,0) FROM rogerai.wallet WHERE usr=$1`, user).Scan(&bal); err != nil {
			return false, 0, err
		}
		return false, bal, nil
	}
	return true, 0, nil
}

// fillEarnShare records the seed-scaled operator share once it is known: it
// backfills the claimed receipt row for a non-empty request id, or writes the
// receipt fresh for the (idempotency-key-less) empty-request-id path.
func (p *Postgres) fillEarnShare(tx *sql.Tx, user, node string, cost float64, rec protocol.UsageReceipt, earnShare float64) error {
	if rec.RequestID != "" {
		_, err := tx.Exec(`UPDATE rogerai.receipts SET owner_share=$2 WHERE request_id=$1`, rec.RequestID, earnShare)
		return err
	}
	rj, _ := json.Marshal(rec)
	bpt, bct := billedTokens(rec)
	_, err := tx.Exec(`INSERT INTO rogerai.receipts
		(request_id,usr,node,model,prompt_tokens,completion_tokens,cost,owner_share,ts,receipt,grant_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (request_id) DO NOTHING`,
		rec.RequestID, user, node, rec.Model, bpt, bct, cost, earnShare, rec.TS, rj, nullStr(rec.GrantID))
	return err
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

// EntriesByUser returns a wallet's receipts in the [since,until) ts window, newest
// first (the consumer time-series + savings source). Bounded by the receipts_usr_ts
// index; the handler buckets the rows by day/hour and model.
func (p *Postgres) EntriesByUser(user string, since, until int64) ([]Entry, error) {
	return p.windowed(`r.usr=$1 AND r.ts>=$2 AND r.ts<$3`, user, since, until)
}

// EntriesByAccount returns the receipts served by ALL nodes bound to an operator
// account in the [since,until) ts window, newest first (the provider time-series +
// owner console source). Joins the node->owner binding so cross-account nodes never
// leak into the result.
func (p *Postgres) EntriesByAccount(accountID string, since, until int64) ([]Entry, error) {
	return p.windowedJoin(accountID, since, until)
}

// windowed scans receipts matching a fixed WHERE clause (the args are $1=key,
// $2=since, $3=until), newest first.
func (p *Postgres) windowed(where, key string, since, until int64) ([]Entry, error) {
	rows, err := p.db.Query(`SELECT r.request_id,r.usr,r.node,r.model,r.prompt_tokens,r.completion_tokens,r.cost,r.owner_share,r.ts
		FROM rogerai.receipts r WHERE `+where+` ORDER BY r.ts DESC`, key, since, until)
	if err != nil {
		return nil, err
	}
	return scanEntries(rows)
}

// windowedJoin scans the account's served receipts (joined to its node bindings) in
// the [since,until) window, newest first.
func (p *Postgres) windowedJoin(accountID string, since, until int64) ([]Entry, error) {
	rows, err := p.db.Query(`SELECT r.request_id,r.usr,r.node,r.model,r.prompt_tokens,r.completion_tokens,r.cost,r.owner_share,r.ts
		FROM rogerai.receipts r
		JOIN rogerai.node_owner o ON o.node = r.node
		WHERE o.account_id=$1 AND r.ts>=$2 AND r.ts<$3 ORDER BY r.ts DESC`, accountID, since, until)
	if err != nil {
		return nil, err
	}
	return scanEntries(rows)
}

// scanEntries drains a receipt result set into Entry rows.
func scanEntries(rows *sql.Rows) ([]Entry, error) {
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
	// Idempotency claim (see Settle): the receipt row is the lock. A redelivered
	// Finalize must NOT re-credit held-cost, re-earn, or mint a second lot - it
	// returns the wallet balance untouched.
	if won, bal, err := p.claimReceipt(tx, user, node, cost, rec); err != nil {
		return 0, err
	} else if !won {
		return bal, tx.Commit()
	}
	var bal float64
	if err := tx.QueryRow(`UPDATE rogerai.wallet SET balance=balance+$2 WHERE usr=$1 RETURNING balance`, user, held-cost).Scan(&bal); err != nil {
		return 0, err
	}
	// Only the REAL (non-seed) funded portion of this cost earns the operator (P0-1):
	// realEarnShareTx draws down seed_remaining and scales the owner share. Called once.
	earnShare, err := p.realEarnShareTx(tx, user, cost, ownerShare)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO rogerai.earnings(node,balance) VALUES($1,$2)
		ON CONFLICT (node) DO UPDATE SET balance=rogerai.earnings.balance+$2`, node, earnShare); err != nil {
		return 0, err
	}
	if err := p.fillEarnShare(tx, user, node, cost, rec, earnShare); err != nil {
		return 0, err
	}
	// Capture: release the full reservation then debit the actual spend. Net wallet
	// delta == held-cost, matching the cache update above. The release carries a
	// non-empty idem_key so it can never post twice for one request id.
	if err := appendLedger(tx, user, "consumer", KindHoldRelease, held, "release:"+rec.RequestID, StatePosted, rec.RequestID, rec.TS); err != nil {
		return 0, err
	}
	if err := appendLedger(tx, user, "consumer", KindSpend, -cost, "spend:"+rec.RequestID, StatePosted, rec.RequestID, rec.TS); err != nil {
		return 0, err
	}
	if err := appendAdjust(tx, user, rec, cost); err != nil {
		return 0, err
	}
	if err := p.addLot(tx, node, rec.RequestID, earnShare, time.Now()); err != nil {
		return 0, err
	}
	return bal, tx.Commit()
}

// appendAdjust writes the KindAdjust audit row inside tx when the broker billed less
// than the node claimed on either axis (the postgres twin of appendAdjustLocked). $0
// money delta; idempotent on the request id (a redelivery is a no-op).
func appendAdjust(tx *sql.Tx, holder string, rec protocol.UsageReceipt, cost float64) error {
	bpt, bct := billedTokens(rec)
	if bpt >= rec.PromptTokens && bct >= rec.CompletionTokens {
		return nil // no downward adjustment: nothing to audit
	}
	return appendLedger(tx, holder, "consumer", KindAdjust, 0, "adjust:"+rec.RequestID, StatePosted, rec.RequestID, rec.TS)
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
// refresh (a re-login with the same key keeps its original bind time). The GitHub
// name + email are captured fill-if-empty via COALESCE(NULLIF(existing, empty), new):
// it keeps a user-set email (or an already-captured name) and NEVER lets a later GitHub
// login clobber it - it only fills a column that is currently empty/NULL.
func (p *Postgres) BindOwner(o Owner) error {
	_, err := p.db.Exec(`INSERT INTO rogerai.owners(pubkey,github_id,login,name,email) VALUES($1,$2,$3,$4,$5)
		ON CONFLICT (pubkey) DO UPDATE SET github_id=$2, login=$3,
			name=COALESCE(NULLIF(rogerai.owners.name,''), $4),
			email=COALESCE(NULLIF(rogerai.owners.email,''), $5)`,
		o.Pubkey, o.GitHubID, o.Login, o.Name, o.Email)
	return err
}

func (p *Postgres) OwnerByPubkey(pubkey string) (Owner, bool, error) {
	return p.scanOwner(`SELECT pubkey,github_id,login,created_at,email,stripe_connect_id,connect_status,deleted_at,anonymized,name,welcomed_at
		FROM rogerai.owners WHERE pubkey=$1`, pubkey)
}

func (p *Postgres) OwnerByLogin(login string) (Owner, bool, error) {
	return p.scanOwner(`SELECT pubkey,github_id,login,created_at,email,stripe_connect_id,connect_status,deleted_at,anonymized,name,welcomed_at
		FROM rogerai.owners WHERE login=$1 AND NOT COALESCE(anonymized,false)`, login)
}

// scanOwner runs a single-row owner query, mapping NULL columns to zero values.
func (p *Postgres) scanOwner(query string, arg string) (Owner, bool, error) {
	var o Owner
	var created, deleted, welcomed sql.NullTime
	var email, connectID, connectStatus, name sql.NullString
	var anon sql.NullBool
	err := p.db.QueryRow(query, arg).Scan(
		&o.Pubkey, &o.GitHubID, &o.Login, &created, &email, &connectID, &connectStatus, &deleted, &anon, &name, &welcomed)
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
	if welcomed.Valid {
		o.WelcomedAt = welcomed.Time.Unix()
	}
	o.Email = email.String
	o.Name = name.String
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

// ClaimWelcome atomically stamps welcomed_at=now IFF it is still NULL, reporting
// whether THIS statement claimed it (RowsAffected==1). The WHERE welcomed_at IS NULL
// guard makes it a single-winner CAS even under concurrent binds/patches, so the
// welcome email is sent exactly once.
func (p *Postgres) ClaimWelcome(pubkey string) (bool, error) {
	res, err := p.db.Exec(`UPDATE rogerai.owners SET welcomed_at=now() WHERE pubkey=$1 AND welcomed_at IS NULL`, pubkey)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
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

// UpsertNode persists a node registration. registered_at is set on first insert and
// preserved on refresh (COALESCE to the existing value); reg/confidential/last_seen
// are refreshed every register so a re-hydrated node carries its latest offers, token,
// and a recent last_seen.
func (p *Postgres) UpsertNode(n NodeRecord) error {
	reg, err := json.Marshal(n.Reg)
	if err != nil {
		return err
	}
	if n.RegisteredAt == 0 {
		n.RegisteredAt = time.Now().Unix()
	}
	_, err = p.db.Exec(`
		INSERT INTO rogerai.nodes(node_id,reg,confidential,last_seen,registered_at)
		VALUES($1,$2,$3,$4,$5)
		ON CONFLICT (node_id) DO UPDATE SET
			reg=$2, confidential=$3, last_seen=$4,
			registered_at=COALESCE(NULLIF(rogerai.nodes.registered_at,0), EXCLUDED.registered_at)`,
		n.NodeID, reg, n.Confidential, n.LastSeen, n.RegisteredAt)
	return err
}

// TouchNode bumps last_seen without a re-register (no-op for an unknown node).
func (p *Postgres) TouchNode(nodeID string, seen time.Time) error {
	_, err := p.db.Exec(`UPDATE rogerai.nodes SET last_seen=$2 WHERE node_id=$1`, nodeID, seen.Unix())
	return err
}

// AllNodes returns the persisted registry for startup re-hydration. A row whose reg
// JSON fails to decode is skipped (defensive: a single bad row never blocks startup).
func (p *Postgres) AllNodes() ([]NodeRecord, error) {
	rows, err := p.db.Query(`SELECT node_id,reg,confidential,last_seen,registered_at FROM rogerai.nodes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeRecord
	for rows.Next() {
		var (
			rec    NodeRecord
			regRaw []byte
		)
		if err := rows.Scan(&rec.NodeID, &regRaw, &rec.Confidential, &rec.LastSeen, &rec.RegisteredAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(regRaw, &rec.Reg); err != nil {
			continue // skip an undecodable row rather than fail the whole re-hydrate
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// DeleteNode removes a node's persisted registration row. Earnings (ledger) and the
// node->owner binding live in separate tables and are intentionally NOT touched.
func (p *Postgres) DeleteNode(nodeID string) error {
	_, err := p.db.Exec(`DELETE FROM rogerai.nodes WHERE node_id=$1`, nodeID)
	return err
}

// SetOfferOverride upserts an owner-authored price/schedule override for (node,model).
// The owner pubkey is stored on the row so it can never shadow another account's node.
func (p *Postgres) SetOfferOverride(ov OfferOverride) error {
	sched, err := json.Marshal(ov.Schedule)
	if err != nil {
		return err
	}
	_, err = p.db.Exec(`
		INSERT INTO rogerai.offer_overrides(node,model,owner,price_in,price_out,schedule,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (node,model) DO UPDATE SET
			owner=EXCLUDED.owner, price_in=EXCLUDED.price_in, price_out=EXCLUDED.price_out,
			schedule=EXCLUDED.schedule, updated_at=EXCLUDED.updated_at`,
		ov.NodeID, ov.Model, ov.Owner, ov.PriceIn, ov.PriceOut, sched, ov.UpdatedAt)
	return err
}

func (p *Postgres) OfferOverride(node, model string) (OfferOverride, bool, error) {
	var (
		ov     OfferOverride
		schRaw []byte
	)
	err := p.db.QueryRow(`SELECT node,model,owner,price_in,price_out,schedule,updated_at
		FROM rogerai.offer_overrides WHERE node=$1 AND model=$2`, node, model).
		Scan(&ov.NodeID, &ov.Model, &ov.Owner, &ov.PriceIn, &ov.PriceOut, &schRaw, &ov.UpdatedAt)
	if err == sql.ErrNoRows {
		return OfferOverride{}, false, nil
	}
	if err != nil {
		return OfferOverride{}, false, err
	}
	_ = json.Unmarshal(schRaw, &ov.Schedule)
	return ov, true, nil
}

func (p *Postgres) OverridesByOwner(owner string) ([]OfferOverride, error) {
	rows, err := p.db.Query(`SELECT node,model,owner,price_in,price_out,schedule,updated_at
		FROM rogerai.offer_overrides WHERE owner=$1`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OfferOverride
	for rows.Next() {
		var (
			ov     OfferOverride
			schRaw []byte
		)
		if err := rows.Scan(&ov.NodeID, &ov.Model, &ov.Owner, &ov.PriceIn, &ov.PriceOut, &schRaw, &ov.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(schRaw, &ov.Schedule)
		out = append(out, ov)
	}
	return out, rows.Err()
}

// ClearOfferOverride deletes an owner's override, OWNER-SCOPED (the owner filter in the
// WHERE clause means an owner can never clear another account's override).
func (p *Postgres) ClearOfferOverride(owner, node, model string) (bool, error) {
	res, err := p.db.Exec(`DELETE FROM rogerai.offer_overrides WHERE node=$1 AND model=$2 AND owner=$3`,
		node, model, owner)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
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

// MonthlyCapOf returns a wallet's monthly cap ($), falling back to the env default
// when the wallet has no stored row. 0 = unlimited. A stored 0 is an explicit
// "unlimited" choice and is returned as-is (NOT re-defaulted from the env).
func (p *Postgres) MonthlyCapOf(holder string) (float64, error) {
	var cap float64
	err := p.db.QueryRow(`SELECT monthly_cap FROM rogerai.account_settings WHERE holder=$1`, holder).Scan(&cap)
	if err == sql.ErrNoRows {
		return DefaultMonthlyCap(), nil
	}
	if err != nil {
		return 0, err
	}
	return cap, nil
}

// SetMonthlyCap upserts a wallet's monthly cap (cap<0 -> 0 = unlimited).
func (p *Postgres) SetMonthlyCap(holder string, cap float64) error {
	if cap < 0 {
		cap = 0
	}
	_, err := p.db.Exec(`INSERT INTO rogerai.account_settings(holder,monthly_cap,updated_at)
		VALUES($1,$2,now())
		ON CONFLICT (holder) DO UPDATE SET monthly_cap=$2, updated_at=now()`, holder, cap)
	return err
}

// MonthSpendOf sums a wallet's captured spend ($) in the calendar month containing
// `now`, from the posted `spend` ledger rows (the source of truth). Spend rows are
// negative, so the month-to-date total is the negated SUM. The [start,end) ts bound
// makes the calendar boundary exact (a previous-month row is excluded).
func (p *Postgres) MonthSpendOf(holder string, now time.Time) (float64, error) {
	start, end := monthRange(now)
	var sum float64
	err := p.db.QueryRow(`SELECT COALESCE(SUM(-amount),0) FROM rogerai.ledger
		WHERE holder=$1 AND kind=$2 AND state<>'reversed' AND ts>=$3 AND ts<$4`,
		holder, KindSpend, start, end).Scan(&sum)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return sum, err
}

// promoteLots sweeps held lots to payable when their release time has passed, in
// one transaction (sweep-on-read). A lot whose NODE has an OPEN L1 re-count
// discrepancy (rogerai.recount_holds) is NOT promoted (P0-2): an over-reporting
// node's earnings stay held pending review instead of auto-promoting on schedule.
func (p *Postgres) promoteLots(now time.Time) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE rogerai.earning_lots SET state='payable'
		WHERE state='held' AND release_at<=$1
		AND node NOT IN (SELECT node FROM rogerai.recount_holds)
		AND account_id NOT IN (SELECT account_id FROM rogerai.account_recount_holds)`, now.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func (p *Postgres) SetNodeRecountHold(node string, held bool) error {
	if held {
		// Refresh created_at on a re-flag so a still-discrepant node re-arms its
		// auto-expiry window (ExpireRecountHolds only clears holds older than the cutoff).
		_, err := p.db.Exec(`INSERT INTO rogerai.recount_holds(node) VALUES($1)
			ON CONFLICT (node) DO UPDATE SET created_at=now()`, node)
		return err
	}
	_, err := p.db.Exec(`DELETE FROM rogerai.recount_holds WHERE node=$1`, node)
	return err
}

// ExpireRecountHolds clears every node + account hold whose created_at is at or before
// olderThan (auto-expiry recourse): an honest operator hit by a false positive is
// unfrozen after the window. An abusive operator is kept held because a fresh
// discrepancy re-inserts the hold row with a current created_at (SetNodeRecountHold /
// SetAccountRecountHold re-place it on every flag), above the cutoff. Returns the count
// of node+account holds cleared.
func (p *Postgres) ExpireRecountHolds(olderThan time.Time) (int, error) {
	cut := olderThan
	rn, err := p.db.Exec(`DELETE FROM rogerai.recount_holds WHERE created_at<=$1`, cut)
	if err != nil {
		return 0, err
	}
	ra, err := p.db.Exec(`DELETE FROM rogerai.account_recount_holds WHERE created_at<=$1`, cut)
	if err != nil {
		return 0, err
	}
	an, _ := rn.RowsAffected()
	aa, _ := ra.RowsAffected()
	return int(an + aa), nil
}

func (p *Postgres) RecountHeldNodes() (map[string]bool, error) {
	rows, err := p.db.Query(`SELECT node FROM rogerai.recount_holds`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
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

func (p *Postgres) RequestPayout(accountID string, now time.Time, minPayout float64) (Payout, bool, string, error) {
	if err := p.promoteLots(now); err != nil {
		return Payout{}, false, "", err
	}
	tx, err := p.db.Begin()
	if err != nil {
		return Payout{}, false, "", err
	}
	defer tx.Rollback()
	n := now.Unix()
	// Lock the payable lots FOR UPDATE so a concurrent request can't double-debit
	// them, then sum (gross-minus-reserve, plus reserve whose tail cleared).
	if _, err := tx.Exec(`SELECT id FROM rogerai.earning_lots
		WHERE account_id=$1 AND state='payable' FOR UPDATE`, accountID); err != nil {
		return Payout{}, false, "", err
	}
	var amount float64
	if err := tx.QueryRow(`SELECT COALESCE(SUM(gross-reserve + CASE WHEN reserve_release_at<=$2 THEN reserve ELSE 0 END),0)
		FROM rogerai.earning_lots WHERE account_id=$1 AND state='payable'`, accountID, n).Scan(&amount); err != nil {
		return Payout{}, false, "", err
	}
	if amount < minPayout {
		return Payout{}, false, "below minimum payout", nil
	}
	// Insert the PENDING payout first to get its id, then tag + debit the lots with
	// it so a failed transfer can roll back exactly these lots.
	var pid int64
	if err := tx.QueryRow(`INSERT INTO rogerai.payouts(account_id,amount,stripe_transfer_id,state,created_at)
		VALUES($1,$2,'',$3,$4) RETURNING id`, accountID, amount, PayoutPending, n).Scan(&pid); err != nil {
		return Payout{}, false, "", err
	}
	if _, err := tx.Exec(`UPDATE rogerai.earning_lots SET state='paid', payout_id=$2
		WHERE account_id=$1 AND state='payable'`, accountID, pid); err != nil {
		return Payout{}, false, "", err
	}
	if err := appendLedger(tx, accountID, "operator", KindPayout, -amount, "payout:"+strconv.FormatInt(pid, 10), StatePosted, "", n); err != nil {
		return Payout{}, false, "", err
	}
	if err := tx.Commit(); err != nil {
		return Payout{}, false, "", err
	}
	return Payout{ID: pid, AccountID: accountID, Amount: amount, State: PayoutPending, CreatedAt: n}, true, "", nil
}

// SettlePayout marks a pending payout PAID and records its transfer id. Idempotent.
func (p *Postgres) SettlePayout(payoutID int64, transferID string) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE rogerai.payouts SET state=$2, stripe_transfer_id=$3
		WHERE id=$1 AND state=$4`, payoutID, PayoutPaid, transferID, PayoutPending)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return tx.Commit() // already settled / unknown: no-op
	}
	if _, err := tx.Exec(`UPDATE rogerai.ledger SET ref=$2
		WHERE kind=$3 AND idem_key=$1`, "payout:"+strconv.FormatInt(payoutID, 10), transferID, KindPayout); err != nil {
		return err
	}
	return tx.Commit()
}

// FailPayout rolls a pending payout back: its debited lots return to 'payable', the
// payout is marked FAILED, and the payout ledger row is reversed.
func (p *Postgres) FailPayout(payoutID int64) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE rogerai.payouts SET state=$2 WHERE id=$1 AND state=$3`,
		payoutID, PayoutFailed, PayoutPending)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return tx.Commit() // already settled / failed: nothing to roll back
	}
	if _, err := tx.Exec(`UPDATE rogerai.earning_lots SET state='payable', payout_id=NULL
		WHERE payout_id=$1 AND state='paid'`, payoutID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE rogerai.ledger SET state=$2
		WHERE kind=$3 AND idem_key=$1`, "payout:"+strconv.FormatInt(payoutID, 10), StateReversed, KindPayout); err != nil {
		return err
	}
	return tx.Commit()
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

// ReleaseSchedule buckets the account's still-held lots by their release calendar day
// (UTC midnight) into an ascending dated ladder. It sweeps held->payable first so an
// already-cleared lot is not shown as upcoming. Reads off earning_lots (lots_account).
func (p *Postgres) ReleaseSchedule(accountID string, now time.Time) ([]ReleaseBucket, error) {
	if err := p.promoteLots(now); err != nil {
		return nil, err
	}
	// Bucket by UTC-midnight of release_at; sum gross-minus-reserve releasing that day.
	rows, err := p.db.Query(`SELECT
		(date_trunc('day', to_timestamp(release_at) AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')::date,
		COALESCE(SUM(gross-reserve),0), COUNT(*)
		FROM rogerai.earning_lots
		WHERE account_id=$1 AND state='held' AND gross-reserve>0
		GROUP BY 1 ORDER BY 1 ASC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReleaseBucket
	for rows.Next() {
		var day time.Time
		var b ReleaseBucket
		if err := rows.Scan(&day, &b.Amount, &b.LotCount); err != nil {
			return nil, err
		}
		b.Date = day.UTC().Unix()
		out = append(out, b)
	}
	return out, rows.Err()
}

// EarningRollups returns the account's earnings per model and per node across its
// non-clawed lots (held+payable+paid gross), joining the request receipt for the model.
func (p *Postgres) EarningRollups(accountID string) (byModel, byNode []EarningRollup, err error) {
	scan := func(rows *sql.Rows) ([]EarningRollup, error) {
		defer rows.Close()
		var out []EarningRollup
		for rows.Next() {
			var r EarningRollup
			var key sql.NullString
			if err := rows.Scan(&key, &r.Amount, &r.Lots); err != nil {
				return nil, err
			}
			r.Key = key.String
			out = append(out, r)
		}
		return out, rows.Err()
	}
	mRows, err := p.db.Query(`SELECT COALESCE(r.model,''), COALESCE(SUM(l.gross),0), COUNT(*)
		FROM rogerai.earning_lots l
		LEFT JOIN rogerai.receipts r ON r.request_id=l.request_id
		WHERE l.account_id=$1 AND l.state<>'clawed'
		GROUP BY 1 ORDER BY 2 DESC, 1 ASC`, accountID)
	if err != nil {
		return nil, nil, err
	}
	if byModel, err = scan(mRows); err != nil {
		return nil, nil, err
	}
	nRows, err := p.db.Query(`SELECT COALESCE(node,''), COALESCE(SUM(gross),0), COUNT(*)
		FROM rogerai.earning_lots
		WHERE account_id=$1 AND state<>'clawed'
		GROUP BY 1 ORDER BY 2 DESC, 1 ASC`, accountID)
	if err != nil {
		return nil, nil, err
	}
	if byNode, err = scan(nRows); err != nil {
		return nil, nil, err
	}
	return byModel, byNode, nil
}

// PayoutLots returns the funding earning lots behind a payout (request-level lineage),
// owner-scoped: ok=false if the payout id is not the caller's (no cross-account leak).
func (p *Postgres) PayoutLots(accountID string, payoutID int64) ([]PayoutLot, bool, error) {
	// Ownership gate: the payout must exist AND belong to this account.
	var owner string
	switch err := p.db.QueryRow(`SELECT account_id FROM rogerai.payouts WHERE id=$1`, payoutID).Scan(&owner); {
	case err == sql.ErrNoRows:
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	if owner != accountID {
		return nil, false, nil
	}
	rows, err := p.db.Query(`SELECT l.id, l.request_id, l.node, COALESCE(r.model,''), l.gross, l.created_at
		FROM rogerai.earning_lots l
		LEFT JOIN rogerai.receipts r ON r.request_id=l.request_id
		WHERE l.payout_id=$1 ORDER BY l.created_at DESC, l.id DESC`, payoutID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []PayoutLot
	for rows.Next() {
		var pl PayoutLot
		if err := rows.Scan(&pl.LotID, &pl.RequestID, &pl.Node, &pl.Model, &pl.Gross, &pl.CreatedAt); err != nil {
			return nil, false, err
		}
		out = append(out, pl)
	}
	return out, true, rows.Err()
}

// Chargeback is the back-compat wrapper: it runs the lineage clawback and returns just
// the amount clawed from still-held/payable lots. It does NOT issue Stripe transfer
// reversals - use ChargebackLineage and act on the returned Reversals for that.
func (p *Postgres) Chargeback(disputeID, wallet, requestID string, amount float64, now time.Time) (float64, error) {
	res, err := p.ChargebackLineage(disputeID, wallet, requestID, amount, now)
	return res.Clawed, err
}

func (p *Postgres) ChargebackLineage(disputeID, wallet, requestID string, amount float64, now time.Time) (ChargebackResult, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return ChargebackResult{}, err
	}
	defer tx.Rollback()
	// Idempotent on the stripe dispute id: a fresh insert means first delivery.
	res, err := tx.Exec(`INSERT INTO rogerai.disputes(id,request_id,wallet,amount,state,created_at)
		VALUES($1,$2,$3,$4,'open',$5) ON CONFLICT (id) DO NOTHING`, disputeID, requestID, wallet, amount, now.Unix())
	if err != nil {
		return ChargebackResult{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ChargebackResult{AlreadyHandled: true}, tx.Commit() // already processed
	}
	if _, err := tx.Exec(`UPDATE rogerai.wallet SET balance=balance-$2 WHERE usr=$1`, wallet, amount); err != nil {
		return ChargebackResult{}, err
	}
	if err := appendLedger(tx, wallet, "consumer", KindChargeback, -amount, "dispute:"+disputeID, StatePosted, disputeID, now.Unix()); err != nil {
		return ChargebackResult{}, err
	}
	// Lineage: target THIS consumer wallet's OWN lots (checkout_charges resolved the
	// wallet; receipts attribute its lots), NEVER unrelated operators'. With an explicit
	// requestID we claw that one request; otherwise the wallet's lots newest first,
	// capped at the disputed amount. Held/payable AND already-paid lots are eligible (a
	// paid lot is reversed via Stripe rather than escaping the clawback). The LEFT JOIN
	// to payouts carries the transfer id needed to reverse a paid lot.
	type claw struct {
		id       int64
		acct     string
		gross    float64 // operator share recovered when this lot is clawed
		cost     float64 // CONSUMER cost billed for this lot's request (the dispute is in these units)
		state    string
		transfer string
	}
	var claws []claw
	scan := func(rows *sql.Rows) error {
		defer rows.Close()
		for rows.Next() {
			var c claw
			var tr sql.NullString
			if err := rows.Scan(&c.id, &c.acct, &c.gross, &c.state, &tr, &c.cost); err != nil {
				return err
			}
			c.transfer = tr.String
			claws = append(claws, c)
		}
		return rows.Err()
	}
	if requestID != "" {
		// Explicit request: claw that one request's lots; cost is unused (no amount cap), so
		// select 0 to satisfy the shared scan.
		rows, err := tx.Query(`SELECT l.id,l.account_id,l.gross,l.state,po.stripe_transfer_id,0::float8
			FROM rogerai.earning_lots l
			LEFT JOIN rogerai.payouts po ON po.id=l.payout_id
			WHERE l.request_id=$1 AND l.state IN ('held','payable','paid')`, requestID)
		if err != nil {
			return ChargebackResult{}, err
		}
		if err := scan(rows); err != nil {
			return ChargebackResult{}, err
		}
	} else {
		// Carry r.cost (the CONSUMER amount billed) so the loop can cap on consumer dollars,
		// not operator gross - else it over-claws by 1/(1-feeRate) into the consumer's other
		// (non-disputed) top-ups and makes an honest operator eat the platform's fee.
		rows, err := tx.Query(`SELECT l.id,l.account_id,l.gross,l.state,po.stripe_transfer_id,r.cost
			FROM rogerai.earning_lots l
			JOIN rogerai.receipts r ON r.request_id=l.request_id
			LEFT JOIN rogerai.payouts po ON po.id=l.payout_id
			WHERE r.usr=$1 AND l.state IN ('held','payable','paid')
			ORDER BY r.ts DESC, l.id DESC`, wallet)
		if err != nil {
			return ChargebackResult{}, err
		}
		if err := scan(rows); err != nil {
			return ChargebackResult{}, err
		}
	}
	var out ChargebackResult
	recovered := 0.0    // operator gross recovered (clawed + reversed)
	remaining := amount // consumer cost still to recover (wallet-recency path); caps the claw
	for _, c := range claws {
		if requestID == "" && remaining <= 1e-9 {
			break
		}
		// PRO-RATA on the overshooting lot: recover only the operator's proportional share of
		// the disputed cost still remaining, so the operator is never clawed beyond the
		// disputed amount. Full disputes claw whole lots (frac=1); explicit-requestID claws
		// whole (no amount cap). Mirrors Mem.ChargebackLineage.
		frac := 1.0
		if requestID == "" && c.cost > 0 && c.cost > remaining {
			frac = remaining / c.cost
		}
		clawGross := c.gross * frac
		if frac >= 1.0 {
			if _, err := tx.Exec(`UPDATE rogerai.earning_lots SET state='clawed' WHERE id=$1`, c.id); err != nil {
				return ChargebackResult{}, err
			}
		} else {
			// Partial claw: keep the lot, reduce its gross + reserve by the clawed fraction.
			if _, err := tx.Exec(`UPDATE rogerai.earning_lots SET gross=gross-$2, reserve=reserve*$3 WHERE id=$1`, c.id, clawGross, 1-frac); err != nil {
				return ChargebackResult{}, err
			}
		}
		if c.state == LotPaid {
			// Already paid out: payout_reversed ledger row + a returned Reversal so the
			// broker issues the Stripe Transfer Reversal (6.4 step 4).
			if err := appendLedger(tx, c.acct, "operator", KindPayoutReversed, -clawGross, "reverse:"+disputeID+":"+strconv.FormatInt(c.id, 10), StatePosted, disputeID, now.Unix()); err != nil {
				return ChargebackResult{}, err
			}
			out.Reversals = append(out.Reversals, Reversal{
				DisputeID: disputeID, LotID: c.id, AccountID: c.acct, TransferID: c.transfer, Amount: clawGross,
			})
		} else {
			if err := appendLedger(tx, c.acct, "operator", KindAdjustment, -clawGross, "claw:"+disputeID+":"+strconv.FormatInt(c.id, 10), StatePosted, disputeID, now.Unix()); err != nil {
				return ChargebackResult{}, err
			}
			out.Clawed += clawGross
		}
		recovered += clawGross
		remaining -= c.cost * frac
	}
	// Unrecovered remainder is a PLATFORM LOSS (don't claw unrelated operators).
	if remainder := amount - recovered; remainder > 1e-9 {
		out.PlatformLoss = remainder
		if err := appendLedger(tx, "platform", "platform", KindPlatformLoss, -remainder, "loss:"+disputeID, StatePosted, disputeID, now.Unix()); err != nil {
			return ChargebackResult{}, err
		}
	}
	return out, tx.Commit()
}

func (p *Postgres) LinkCharge(sessionID, paymentIntent, charge, wallet string, credits float64) error {
	_, err := p.db.Exec(`INSERT INTO rogerai.checkout_charges(session_id,payment_intent,charge,wallet,credits)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT (session_id) DO NOTHING`,
		sessionID, nullStr(paymentIntent), nullStr(charge), wallet, credits)
	return err
}

func (p *Postgres) WalletByCharge(ref string) (string, float64, bool, error) {
	if ref == "" {
		return "", 0, false, nil
	}
	var wallet string
	var credits float64
	err := p.db.QueryRow(`SELECT wallet,credits FROM rogerai.checkout_charges
		WHERE payment_intent=$1 OR charge=$1 LIMIT 1`, ref).Scan(&wallet, &credits)
	if err == sql.ErrNoRows {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	return wallet, credits, true, nil
}

func (p *Postgres) OpenDisputeCount(accountID string) (int, error) {
	var n int
	err := p.db.QueryRow(`SELECT COUNT(*) FROM rogerai.disputes d
		JOIN rogerai.earning_lots l ON l.request_id=d.request_id
		WHERE l.account_id=$1 AND d.state='open'`, accountID).Scan(&n)
	return n, err
}

func (p *Postgres) Close() error { return p.db.Close() }

// Healthy pings the Postgres connection: nil = reachable. Backs the /ready endpoint.
func (p *Postgres) Healthy() error { return p.db.Ping() }

// RecordPendingReversal durably records a Stripe Transfer Reversal intent. Idempotent
// on key (ON CONFLICT DO NOTHING): a webhook redelivery never double-records nor resets
// attempts/done on an existing row.
func (p *Postgres) RecordPendingReversal(pr PendingReversal) error {
	if pr.Key == "" {
		return nil
	}
	if pr.CreatedAt == 0 {
		pr.CreatedAt = time.Now().Unix()
	}
	_, err := p.db.Exec(`INSERT INTO rogerai.pending_reversals
		(key, dispute_id, lot_id, account_id, transfer_id, amount, created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (key) DO NOTHING`,
		pr.Key, pr.DisputeID, pr.LotID, pr.AccountID, pr.TransferID, pr.Amount, pr.CreatedAt)
	return err
}

// OpenPendingReversals returns reversals still owed (not done, not dead-lettered),
// oldest first, capped at limit (0 = all).
func (p *Postgres) OpenPendingReversals(limit int) ([]PendingReversal, error) {
	q := `SELECT key, dispute_id, lot_id, account_id, transfer_id, amount, attempts, done, dead_letter, COALESCE(last_error,''), created_at, last_attempt
		FROM rogerai.pending_reversals WHERE done=false AND dead_letter=false ORDER BY created_at ASC`
	if limit > 0 {
		q += ` LIMIT ` + strconv.Itoa(limit)
	}
	rows, err := p.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingReversal
	for rows.Next() {
		var pr PendingReversal
		if err := rows.Scan(&pr.Key, &pr.DisputeID, &pr.LotID, &pr.AccountID, &pr.TransferID, &pr.Amount,
			&pr.Attempts, &pr.Done, &pr.DeadLetter, &pr.LastError, &pr.CreatedAt, &pr.LastAttempt); err != nil {
			return nil, err
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

// MarkReversalAttempt records one reversal attempt outcome for key: bump attempts +
// last-attempt, mark done on success, or record the error and dead-letter once attempts
// reach maxAttempts. A WHERE-guard keeps an already-terminal row untouched.
func (p *Postgres) MarkReversalAttempt(key string, success bool, errMsg string, maxAttempts int, now time.Time) error {
	if success {
		_, err := p.db.Exec(`UPDATE rogerai.pending_reversals
			SET attempts=attempts+1, last_attempt=$2, done=true, last_error=''
			WHERE key=$1 AND done=false AND dead_letter=false`, key, now.Unix())
		return err
	}
	// Failure: bump attempts, record the error, and flip to dead-letter if it just
	// reached the max. attempts+1 is compared so the (maxAttempts)th failure parks it.
	_, err := p.db.Exec(`UPDATE rogerai.pending_reversals
		SET attempts=attempts+1, last_attempt=$2, last_error=$3,
		    dead_letter=($4>0 AND attempts+1>=$4)
		WHERE key=$1 AND done=false AND dead_letter=false`, key, now.Unix(), errMsg, maxAttempts)
	return err
}
