package store

import (
	"database/sql"
	"time"
)

// Postgres safety storage (safety.go): csam_incidents, reports, banned_nodes. Mirrors
// the additive grant/owner style - small focused tables, indexed on the hot lookups
// (report_state, node_id). Content is the broker-encrypted ciphertext blob.

func (p *Postgres) PreserveCSAM(inc CSAMIncident) (int64, error) {
	if inc.CreatedAt == 0 {
		inc.CreatedAt = time.Now().Unix()
	}
	if inc.ReportState == "" {
		inc.ReportState = CSAMQueued
	}
	var id int64
	err := p.db.QueryRow(`INSERT INTO rogerai.csam_incidents
		(pseudonym,ip,category,content,report_state,created_at)
		VALUES($1,$2,$3,$4,$5,$6) RETURNING id`,
		inc.Pseudonym, nullStr(inc.IP), nullStr(inc.Category), inc.Content, inc.ReportState, inc.CreatedAt).Scan(&id)
	return id, err
}

func (p *Postgres) PendingCSAMReports(limit int) ([]CSAMIncident, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := p.db.Query(`SELECT id,pseudonym,COALESCE(ip,''),COALESCE(category,''),content,report_state,created_at
		FROM rogerai.csam_incidents WHERE report_state=$1 ORDER BY id DESC LIMIT $2`, CSAMQueued, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CSAMIncident
	for rows.Next() {
		var inc CSAMIncident
		if err := rows.Scan(&inc.ID, &inc.Pseudonym, &inc.IP, &inc.Category, &inc.Content, &inc.ReportState, &inc.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, inc)
	}
	return out, rows.Err()
}

func (p *Postgres) MarkCSAMReported(id int64) error {
	_, err := p.db.Exec(`UPDATE rogerai.csam_incidents SET report_state=$2 WHERE id=$1`, id, CSAMReported)
	return err
}

func (p *Postgres) AddReport(r Report) (int64, error) {
	if r.CreatedAt == 0 {
		r.CreatedAt = time.Now().Unix()
	}
	var id int64
	err := p.db.QueryRow(`INSERT INTO rogerai.reports
		(category,node_id,request_id,detail,ip,created_at)
		VALUES($1,$2,$3,$4,$5,$6) RETURNING id`,
		r.Category, nullStr(r.NodeID), nullStr(r.RequestID), nullStr(r.Detail), nullStr(r.IP), r.CreatedAt).Scan(&id)
	return id, err
}

func (p *Postgres) ReportCountByNode(nodeID string) (int, error) {
	var n int
	err := p.db.QueryRow(`SELECT COUNT(*) FROM rogerai.reports WHERE node_id=$1`, nodeID).Scan(&n)
	return n, err
}

func (p *Postgres) ReportsByNode(nodeID string, limit int) ([]Report, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := p.db.Query(`SELECT id,category,COALESCE(node_id,''),COALESCE(request_id,''),COALESCE(detail,''),COALESCE(ip,''),created_at
		FROM rogerai.reports WHERE node_id=$1 ORDER BY id DESC LIMIT $2`, nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Report
	for rows.Next() {
		var r Report
		if err := rows.Scan(&r.ID, &r.Category, &r.NodeID, &r.RequestID, &r.Detail, &r.IP, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *Postgres) BanNode(nodeID, reason string) error {
	_, err := p.db.Exec(`INSERT INTO rogerai.banned_nodes(node_id,reason) VALUES($1,$2)
		ON CONFLICT (node_id) DO NOTHING`, nodeID, reason)
	return err
}

func (p *Postgres) BannedNodes() (map[string]string, error) {
	rows, err := p.db.Query(`SELECT node_id,COALESCE(reason,'') FROM rogerai.banned_nodes`)
	if err == sql.ErrNoRows {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, reason string
		if err := rows.Scan(&id, &reason); err != nil {
			return nil, err
		}
		out[id] = reason
	}
	return out, rows.Err()
}

// --- owner-keyed durable bans + strikes (anti-rotation) -------------------

func (p *Postgres) OwnerStrike(accountID, kind, evidenceJSON, idemKey string) (int, error) {
	if accountID == "" {
		return 0, nil
	}
	var ik any
	if idemKey != "" {
		ik = "strike:" + idemKey
	}
	var ev any
	if evidenceJSON != "" {
		ev = evidenceJSON
	}
	// Append the strike (idempotent on idem_key: a retried request is a no-op).
	if _, err := p.db.Exec(`INSERT INTO rogerai.owner_strikes(account_id,kind,evidence,idem_key,created_at)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT (idem_key) DO NOTHING`,
		accountID, kind, ev, ik, time.Now().Unix()); err != nil {
		return 0, err
	}
	var n int
	if err := p.db.QueryRow(`SELECT COUNT(*) FROM rogerai.owner_strikes WHERE account_id=$1`, accountID).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (p *Postgres) StrikesByOwner(accountID string, limit int) ([]Strike, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := p.db.Query(`SELECT id,account_id,kind,COALESCE(evidence::text,''),created_at
		FROM rogerai.owner_strikes WHERE account_id=$1 ORDER BY id DESC LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Strike
	for rows.Next() {
		var s Strike
		if err := rows.Scan(&s.ID, &s.AccountID, &s.Kind, &s.Evidence, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *Postgres) BanOwner(accountID, reason, evidenceJSON string) error {
	if accountID == "" {
		return nil
	}
	var ev any
	if evidenceJSON != "" {
		ev = evidenceJSON
	}
	_, err := p.db.Exec(`INSERT INTO rogerai.banned_owners(account_id,reason,evidence) VALUES($1,$2,$3)
		ON CONFLICT (account_id) DO NOTHING`, accountID, reason, ev) // first ban wins; evidence preserved
	return err
}

func (p *Postgres) IsOwnerBanned(accountID string) (bool, string, error) {
	if accountID == "" {
		return false, "", nil
	}
	var reason string
	err := p.db.QueryRow(`SELECT COALESCE(reason,'') FROM rogerai.banned_owners WHERE account_id=$1`, accountID).Scan(&reason)
	if err == sql.ErrNoRows {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	return true, reason, nil
}

func (p *Postgres) BannedOwners() (map[string]string, error) {
	rows, err := p.db.Query(`SELECT account_id,COALESCE(reason,'') FROM rogerai.banned_owners`)
	if err == sql.ErrNoRows {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, reason string
		if err := rows.Scan(&id, &reason); err != nil {
			return nil, err
		}
		out[id] = reason
	}
	return out, rows.Err()
}

// ForgiveOwner reverses all durable anti-abuse state against an owner after admin
// review, in one transaction: deletes its strikes, lifts the owner ban, and clears the
// account recount hold. Returns the number of strikes forgiven. Idempotent.
func (p *Postgres) ForgiveOwner(accountID string) (int, error) {
	if accountID == "" {
		return 0, nil
	}
	tx, err := p.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM rogerai.owner_strikes WHERE account_id=$1`, accountID)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM rogerai.banned_owners WHERE account_id=$1`, accountID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM rogerai.account_recount_holds WHERE account_id=$1`, accountID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (p *Postgres) SetAccountRecountHold(accountID string, held bool) error {
	if accountID == "" {
		return nil
	}
	if held {
		// Refresh created_at on a re-flag so a still-flagged owner re-arms auto-expiry.
		_, err := p.db.Exec(`INSERT INTO rogerai.account_recount_holds(account_id) VALUES($1)
			ON CONFLICT (account_id) DO UPDATE SET created_at=now()`, accountID)
		return err
	}
	_, err := p.db.Exec(`DELETE FROM rogerai.account_recount_holds WHERE account_id=$1`, accountID)
	return err
}
