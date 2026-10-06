package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// Postgres account-key storage (keys.go). The record lives in rogerai.account_keys; the key's
// reservation is the sum of its pending_holds rows (key_id); its spend is key_spend ledger rows
// held under the key id, written in the SAME transaction that captures the hold.

const acctKeyCols = `id,secret_hash,account,name,hint,limit_usd,reset,anchor,expires_at,allowed_models,allowed_nodes,disabled,revoked,idem_key,created_at,last_used,requests,owner_pub`

func scanAcctKey(row interface{ Scan(...any) error }) (AccountKey, error) {
	var k AccountKey
	var models, nodes []byte
	err := row.Scan(&k.ID, &k.SecretHash, &k.Account, &k.Name, &k.Hint, &k.LimitUSD, &k.Reset, &k.Anchor,
		&k.ExpiresAt, &models, &nodes, &k.Disabled, &k.Revoked, &k.IdemKey, &k.CreatedAt, &k.LastUsed, &k.Requests, &k.OwnerPub)
	if err != nil {
		return AccountKey{}, err
	}
	_ = json.Unmarshal(models, &k.AllowedModels)
	_ = json.Unmarshal(nodes, &k.AllowedNodes)
	return k, nil
}

func (p *Postgres) CreateAccountKey(k AccountKey, r MintKeyRules) (AccountKey, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return AccountKey{}, err
	}
	defer tx.Rollback()
	// One account's mints serialize on a transaction-scoped advisory lock, so the rules read
	// below and the insert are one decision across every instance.
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended('rogerai.account_keys:' || $1, 0))`, k.Account); err != nil {
		return AccountKey{}, err
	}
	rows, err := tx.Query(`SELECT id,idem_key,created_at,revoked FROM rogerai.account_keys WHERE account=$1`, k.Account)
	if err != nil {
		return AccountKey{}, err
	}
	var existing []AccountKey
	for rows.Next() {
		var x AccountKey
		if err := rows.Scan(&x.ID, &x.IdemKey, &x.CreatedAt, &x.Revoked); err != nil {
			rows.Close()
			return AccountKey{}, err
		}
		existing = append(existing, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return AccountKey{}, err
	}
	if k, err = r.admit(k, existing); err != nil {
		return AccountKey{}, err
	}
	if _, err := tx.Exec(`INSERT INTO rogerai.account_keys (`+acctKeyCols+`)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		k.ID, k.SecretHash, k.Account, k.Name, k.Hint, k.LimitUSD, k.Reset, k.Anchor, k.ExpiresAt,
		jsonStrSlice(k.AllowedModels), jsonStrSlice(k.AllowedNodes), k.Disabled, k.Revoked, k.IdemKey,
		k.CreatedAt, k.LastUsed, k.Requests, k.OwnerPub); err != nil {
		return AccountKey{}, err
	}
	return k, tx.Commit()
}

func (p *Postgres) acctKeyWhere(where string, arg any) (AccountKey, bool, error) {
	k, err := scanAcctKey(p.db.QueryRow(`SELECT `+acctKeyCols+` FROM rogerai.account_keys WHERE `+where, arg))
	if err == sql.ErrNoRows {
		return AccountKey{}, false, nil
	}
	if err != nil {
		return AccountKey{}, false, err
	}
	return k, true, nil
}

func (p *Postgres) AccountKeyByHash(hash string) (AccountKey, bool, error) {
	return p.acctKeyWhere("secret_hash=$1", hash)
}

func (p *Postgres) AccountKeyByID(id string) (AccountKey, bool, error) {
	return p.acctKeyWhere("id=$1", id)
}

func (p *Postgres) AccountKeysOf(account string) ([]AccountKey, error) {
	rows, err := p.db.Query(`SELECT `+acctKeyCols+` FROM rogerai.account_keys WHERE account=$1`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccountKey
	for rows.Next() {
		k, err := scanAcctKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (p *Postgres) UpdateAccountKey(id string, edit func(*AccountKey)) (AccountKey, bool, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return AccountKey{}, false, err
	}
	defer tx.Rollback()
	k, err := scanAcctKey(tx.QueryRow(`SELECT `+acctKeyCols+` FROM rogerai.account_keys WHERE id=$1 FOR UPDATE`, id))
	if err == sql.ErrNoRows || (err == nil && k.Revoked) {
		return AccountKey{}, false, nil
	}
	if err != nil {
		return AccountKey{}, false, err
	}
	edit(&k)
	if _, err := tx.Exec(`UPDATE rogerai.account_keys SET name=$2,limit_usd=$3,reset=$4,anchor=$5,expires_at=$6,
		allowed_models=$7,allowed_nodes=$8,disabled=$9,revoked=$10 WHERE id=$1`,
		k.ID, k.Name, k.LimitUSD, k.Reset, k.Anchor, k.ExpiresAt, jsonStrSlice(k.AllowedModels),
		jsonStrSlice(k.AllowedNodes), k.Disabled, k.Revoked); err != nil {
		return AccountKey{}, false, err
	}
	return k, true, tx.Commit()
}

func (p *Postgres) TouchAccountKey(id string, ts int64, request bool) error {
	inc := 0
	if request {
		inc = 1
	}
	_, err := p.db.Exec(`UPDATE rogerai.account_keys SET last_used=$2, requests=requests+$3 WHERE id=$1`, id, ts, inc)
	return err
}

// HoldForKey: HoldFor whose pending_holds row carries the key id and the request start (the
// date of the eventual key spend). See the Mem twin.
func (p *Postgres) HoldForKey(user, requestID string, amount float64, keyID string, keyTS int64, lim KeyLimit) (bool, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if lim.USD > 0 {
		// The key row lock serializes every limited hold on this key across instances; the
		// reservations and the window spend are then read in ONE statement (one snapshot), so a
		// capture committing in between can neither be counted twice nor missed.
		if _, err := tx.Exec(`SELECT 1 FROM rogerai.account_keys WHERE id=$1 FOR UPDATE`, keyID); err != nil {
			return false, err
		}
		to := lim.To
		if to <= 0 {
			to = 1 << 62
		}
		var reserved, spend float64
		if err := tx.QueryRow(`SELECT
			(SELECT COALESCE(SUM(amount),0) FROM rogerai.pending_holds WHERE key_id=$1 AND request_id<>$2),
			(SELECT COALESCE(SUM(-amount),0) FROM rogerai.ledger
				WHERE holder=$1 AND kind IN ($3,$4) AND state<>'reversed' AND ts>=$5 AND ts<$6)`,
			keyID, requestID, KindKeySpend, KindKeyReversal, lim.From, to).Scan(&reserved, &spend); err != nil {
			return false, err
		}
		if !lim.fits(spend, reserved, amount) {
			return false, ErrKeyLimit
		}
	}
	res, err := tx.Exec(`UPDATE rogerai.wallet SET balance=balance-$2 WHERE usr=$1 AND balance>=$2`, user, amount)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil
	}
	if err := appendLedger(tx, user, "consumer", KindHold, -amount, "", StatePending, requestID, 0); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`INSERT INTO rogerai.pending_holds(request_id,usr,amount,placed_at,key_id,key_ts) VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT (request_id) DO UPDATE SET usr=EXCLUDED.usr, amount=EXCLUDED.amount, placed_at=EXCLUDED.placed_at,
		key_id=EXCLUDED.key_id, key_ts=EXCLUDED.key_ts`,
		requestID, user, amount, time.Now().Unix(), keyID, keyTS); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (p *Postgres) KeyReserved(keyID string) (float64, error) {
	var sum float64
	err := p.db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM rogerai.pending_holds WHERE key_id=$1`, keyID).Scan(&sum)
	return sum, err
}

func (p *Postgres) KeySpend(keyID string, from, to int64) (float64, error) {
	if to <= 0 {
		to = 1 << 62
	}
	var sum float64
	err := p.db.QueryRow(`SELECT COALESCE(SUM(-amount),0) FROM rogerai.ledger
		WHERE holder=$1 AND kind IN ($2,$3) AND state<>'reversed' AND ts>=$4 AND ts<$5`,
		keyID, KindKeySpend, KindKeyReversal, from, to).Scan(&sum)
	return sum, err
}

func (p *Postgres) KeySpendSince(keyID string, froms []int64) ([]float64, error) {
	out := make([]float64, len(froms))
	if len(froms) == 0 {
		return out, nil
	}
	cols := make([]string, len(froms))
	args := []any{keyID, KindKeySpend, KindKeyReversal}
	dst := make([]any, len(froms))
	for i, f := range froms {
		cols[i] = fmt.Sprintf("COALESCE(SUM(-amount) FILTER (WHERE ts >= $%d),0)", len(args)+1)
		args = append(args, f)
		dst[i] = &out[i]
	}
	err := p.db.QueryRow(`SELECT `+strings.Join(cols, ",")+` FROM rogerai.ledger
		WHERE holder=$1 AND kind IN ($2,$3) AND state<>'reversed'`, args...).Scan(dst...)
	return out, err
}

func (p *Postgres) AccountKeySpendRefs(account string) (map[string]string, error) {
	rows, err := p.db.Query(`SELECT DISTINCT l.ref, l.holder FROM rogerai.ledger l
		JOIN rogerai.account_keys k ON k.id = l.holder
		WHERE k.account=$1 AND l.kind IN ($2,$3) AND l.state<>'reversed' AND COALESCE(l.ref,'')<>''`,
		account, KindKeySpend, KindKeyReversal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var ref, key string
		if err := rows.Scan(&ref, &key); err != nil {
			return nil, err
		}
		out[ref] = key
	}
	return out, rows.Err()
}

func (p *Postgres) AppendKeyEvent(wallet, ref string, ts int64) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := appendLedger(tx, wallet, "consumer", KindKeyEvent, 0, "", StatePosted, ref, ts); err != nil {
		return err
	}
	return tx.Commit()
}

// keySpendCaptureTx records the key spend of a captured key-attributed hold, inside the
// capture's transaction (so the key's reservation and its spend never both miss the window).
func keySpendCaptureTx(tx *sql.Tx, keyID string, keyTS int64, ref string, cost float64) error {
	if keyID == "" || cost <= 0 {
		return nil
	}
	return appendLedger(tx, keyID, "key", KindKeySpend, -cost, "", StatePosted, ref, keyTS)
}

// keyReverseTx writes the key-side reversal of a chargeback/refund of requestID: dated at the
// spend it reverses and capped at what is left of it (see the Mem twin). The spend row is locked
// so two reversals of one request cannot both read the same remainder.
func keyReverseTx(tx *sql.Tx, requestID string, amount float64) error {
	if requestID == "" || amount <= 0 {
		return nil
	}
	var holder, ref string
	var spent float64
	var ts int64
	err := tx.QueryRow(`SELECT holder, ref, -amount, ts FROM rogerai.ledger WHERE kind=$1 AND (ref=$2 OR ref LIKE $3) ORDER BY id LIMIT 1 FOR UPDATE`,
		KindKeySpend, requestID, requestID+"-%").Scan(&holder, &ref, &spent, &ts)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var reversed float64
	if err := tx.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM rogerai.ledger WHERE kind=$1 AND holder=$2 AND ref=$3`,
		KindKeyReversal, holder, ref).Scan(&reversed); err != nil {
		return err
	}
	if back := math.Min(amount, spent-reversed); back > 1e-9 {
		return appendLedger(tx, holder, "key", KindKeyReversal, back, "", StatePosted, ref, ts)
	}
	return nil
}

func (p *Postgres) RetireAccountKeys(account, anon string) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE rogerai.account_keys SET revoked=true, owner_pub='' WHERE account=$1`, account); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE rogerai.ledger SET holder=$2 WHERE holder=$1 AND kind=$3`, account, anon, KindKeyEvent); err != nil {
		return err
	}
	return tx.Commit()
}
