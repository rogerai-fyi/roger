package store

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// Postgres band storage (BANDS-DESIGN). Mirrors the grant methods: JSONB for the
// model allow-list, an indexed code_hash for the resolve lookup, a node_id index
// for the idempotent re-register lookup, and an owner index for the dashboard +
// the free-cap count. Only the code HASH is stored; the secret code is shown once.

func (p *Postgres) CreateBand(b Band) error {
	if b.CreatedAt == 0 {
		b.CreatedAt = time.Now().Unix()
	}
	_, err := p.db.Exec(`INSERT INTO rogerai.private_bands
		(id,code_hash,code_display,owner,label,node_id,models,expires_at,revoked,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		b.ID, b.CodeHash, b.CodeDisplay, b.Owner, b.Label, b.NodeID, jsonStrSlice(b.Models),
		b.ExpiresAt, b.Revoked, b.CreatedAt)
	return err
}

const bandCols = `id,code_hash,code_display,owner,label,node_id,models,expires_at,revoked,created_at`

// scanBand maps one private_bands row into a Band.
func (p *Postgres) scanBand(row interface{ Scan(...any) error }) (Band, error) {
	var b Band
	var models []byte
	err := row.Scan(&b.ID, &b.CodeHash, &b.CodeDisplay, &b.Owner, &b.Label, &b.NodeID,
		&models, &b.ExpiresAt, &b.Revoked, &b.CreatedAt)
	if err != nil {
		return Band{}, err
	}
	_ = json.Unmarshal(models, &b.Models)
	return b, nil
}

func (p *Postgres) BandByCodeHash(hash string) (Band, bool, error) {
	b, err := p.scanBand(p.db.QueryRow(`SELECT `+bandCols+` FROM rogerai.private_bands WHERE code_hash=$1`, hash))
	if err == sql.ErrNoRows {
		return Band{}, false, nil
	}
	if err != nil {
		return Band{}, false, err
	}
	return b, true, nil
}

func (p *Postgres) BandByNode(nodeID string) (Band, bool, error) {
	// A node has at most one band; if more than one ever existed (it shouldn't), the
	// newest non-revoked wins so a re-register binds to the live one.
	b, err := p.scanBand(p.db.QueryRow(`SELECT `+bandCols+` FROM rogerai.private_bands
		WHERE node_id=$1 ORDER BY revoked ASC, created_at DESC LIMIT 1`, nodeID))
	if err == sql.ErrNoRows {
		return Band{}, false, nil
	}
	if err != nil {
		return Band{}, false, err
	}
	return b, true, nil
}

func (p *Postgres) BandsByOwner(owner string) ([]Band, error) {
	rows, err := p.db.Query(`SELECT `+bandCols+` FROM rogerai.private_bands WHERE owner=$1 ORDER BY created_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Band
	for rows.Next() {
		b, err := p.scanBand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (p *Postgres) SetBandRevoked(id, owner string, revoked bool) (bool, error) {
	res, err := p.db.Exec(`UPDATE rogerai.private_bands SET revoked=$3 WHERE id=$1 AND owner=$2`, id, owner, revoked)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (p *Postgres) CountActiveBands(owner string, now time.Time) (int, error) {
	var n int
	err := p.db.QueryRow(`SELECT COUNT(*) FROM rogerai.private_bands
		WHERE owner=$1 AND revoked=false AND (expires_at=0 OR expires_at>$2)`, owner, now.Unix()).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// RemaskBandDisplays re-masks every persisted band's code_display into the
// NON-RECOVERABLE form (protocol.MaskBandDisplay), leaving code_hash UNCHANGED so the
// owner's one-time full code still resolves. It reads each row's display, computes the
// masked form in Go (ONE source of truth shared with Mem + the mint path - no SQL
// re-implementation to drift), and UPDATEs only the rows that actually change. The full
// result set is drained before any UPDATE (so the read cursor and the writes don't share
// an open connection). Returns the number of rows re-masked; IDEMPOTENT (already-masked
// rows are skipped, so a re-run changes 0).
func (p *Postgres) RemaskBandDisplays() (int, error) {
	rows, err := p.db.Query(`SELECT id, code_display FROM rogerai.private_bands`)
	if err != nil {
		return 0, err
	}
	type rec struct{ id, display string }
	var recs []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.id, &r.display); err != nil {
			rows.Close()
			return 0, err
		}
		recs = append(recs, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	n := 0
	for _, r := range recs {
		masked := protocol.MaskBandDisplay(r.display)
		if masked == r.display {
			continue
		}
		if _, err := p.db.Exec(`UPDATE rogerai.private_bands SET code_display=$2 WHERE id=$1`, r.id, masked); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
