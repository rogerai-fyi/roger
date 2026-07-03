package store

import (
	"database/sql"
)

// Postgres remote-control roster storage (rc.go). Mirrors the band methods: an indexed
// code_hash for the constant-work attach lookup, an owner_wallet index for the BASE STATION
// roster, a session_id index on attach tokens. ROSTER ONLY — no transcript, no frame is ever
// written here (see REMOTE-CONTROL-DESIGN.md AD-2). code_hash is nullable (a closed/rotated
// window drops it), so it round-trips through sql.NullString.

func nullHash(h string) any {
	if h == "" {
		return nil
	}
	return h
}

func (p *Postgres) CreateRCSession(s RCSession) error {
	_, err := p.db.Exec(`INSERT INTO rogerai.rc_sessions
		(id,owner_wallet,name,code_hash,code_expires,code_display,host_token_hash,created_at,last_host_seen,revoked)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		s.ID, s.OwnerWallet, s.Name, nullHash(s.CodeHash), s.CodeExpires, s.CodeDisplay,
		s.HostTokenHash, s.CreatedAt, s.LastHostSeen, s.Revoked)
	return err
}

const rcCols = `id,owner_wallet,name,COALESCE(code_hash,''),code_expires,code_display,host_token_hash,created_at,last_host_seen,revoked`

func scanRCSession(row interface{ Scan(...any) error }) (RCSession, error) {
	var s RCSession
	err := row.Scan(&s.ID, &s.OwnerWallet, &s.Name, &s.CodeHash, &s.CodeExpires, &s.CodeDisplay,
		&s.HostTokenHash, &s.CreatedAt, &s.LastHostSeen, &s.Revoked)
	return s, err
}

func (p *Postgres) RCSessionByID(id string) (RCSession, bool, error) {
	s, err := scanRCSession(p.db.QueryRow(`SELECT `+rcCols+` FROM rogerai.rc_sessions WHERE id=$1`, id))
	if err == sql.ErrNoRows {
		return RCSession{}, false, nil
	}
	if err != nil {
		return RCSession{}, false, err
	}
	return s, true, nil
}

func (p *Postgres) RCSessionByCodeHash(hash string) (RCSession, bool, error) {
	if hash == "" {
		return RCSession{}, false, nil
	}
	s, err := scanRCSession(p.db.QueryRow(`SELECT `+rcCols+` FROM rogerai.rc_sessions WHERE code_hash=$1`, hash))
	if err == sql.ErrNoRows {
		return RCSession{}, false, nil
	}
	if err != nil {
		return RCSession{}, false, err
	}
	return s, true, nil
}

func (p *Postgres) RCSessionsByOwner(wallet string) ([]RCSession, error) {
	rows, err := p.db.Query(`SELECT `+rcCols+` FROM rogerai.rc_sessions WHERE owner_wallet=$1 ORDER BY created_at DESC`, wallet)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RCSession
	for rows.Next() {
		s, err := scanRCSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *Postgres) UpdateRCSession(s RCSession) error {
	_, err := p.db.Exec(`UPDATE rogerai.rc_sessions
		SET name=$2, code_hash=$3, code_expires=$4, code_display=$5, host_token_hash=$6, last_host_seen=$7, revoked=$8
		WHERE id=$1`,
		s.ID, s.Name, nullHash(s.CodeHash), s.CodeExpires, s.CodeDisplay, s.HostTokenHash, s.LastHostSeen, s.Revoked)
	return err
}

func (p *Postgres) PutRCAttachToken(t RCAttachToken) error {
	_, err := p.db.Exec(`INSERT INTO rogerai.rc_attach_tokens(hash,session_id,device_label,created_at)
		VALUES($1,$2,$3,$4) ON CONFLICT (hash) DO NOTHING`,
		t.Hash, t.SessionID, t.DeviceLabel, t.CreatedAt)
	return err
}

func (p *Postgres) RCAttachTokenByHash(hash string) (RCAttachToken, bool, error) {
	var t RCAttachToken
	err := p.db.QueryRow(`SELECT hash,session_id,device_label,created_at
		FROM rogerai.rc_attach_tokens WHERE hash=$1`, hash).Scan(&t.Hash, &t.SessionID, &t.DeviceLabel, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return RCAttachToken{}, false, nil
	}
	if err != nil {
		return RCAttachToken{}, false, err
	}
	return t, true, nil
}

// RevokeRCSessions revokes all of a wallet's sessions and deletes their attach tokens, in one
// transaction. code_hash is nulled so a revoked session's code can never resolve again.
func (p *Postgres) RevokeRCSessions(wallet string) (int, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM rogerai.rc_attach_tokens a
		USING rogerai.rc_sessions s
		WHERE a.session_id=s.id AND s.owner_wallet=$1 AND s.revoked=false`, wallet); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`UPDATE rogerai.rc_sessions
		SET revoked=true, code_hash=NULL, code_expires=0
		WHERE owner_wallet=$1 AND revoked=false`, wallet)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), tx.Commit()
}

// PruneRCSessions hard-deletes an owner's revoked sessions + those idle since before idleCutoff
// (unix). Attach tokens cascade via the same USING-delete; live/recent rows are kept.
func (p *Postgres) PruneRCSessions(wallet string, idleCutoff int64) (int, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM rogerai.rc_attach_tokens a
		USING rogerai.rc_sessions s
		WHERE a.session_id=s.id AND s.owner_wallet=$1 AND (s.revoked=true OR s.last_host_seen < $2)`, wallet, idleCutoff); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`DELETE FROM rogerai.rc_sessions
		WHERE owner_wallet=$1 AND (revoked=true OR last_host_seen < $2)`, wallet, idleCutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), tx.Commit()
}
