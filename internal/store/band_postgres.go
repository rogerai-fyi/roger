package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"rogerai.fm/roger/v5/internal/protocol"
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
	return bandNodeError(err)
}

// bandNodeError translates the durable uniqueness backstop into the store contract. The
// preflight EXISTS gives callers an early human refusal; this mapping closes the race where
// two transactions both observed a free destination before either committed.
func bandNodeError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "private_bands_live_node" {
		return ErrBandNodeOccupied
	}
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
		return false, bandNodeError(err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UpdateBand applies label and node changes in one owner-scoped transaction. The unique
// partial index is the concurrency backstop; the EXISTS check supplies the ordinary fast
// refusal without relying on a constraint error for control flow.
func (p *Postgres) UpdateBand(id, owner string, patch BandPatch) (Band, bool, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return Band{}, false, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	b, err := p.scanBand(tx.QueryRow(`SELECT `+bandCols+` FROM rogerai.private_bands
		WHERE id=$1 AND owner=$2 FOR UPDATE`, id, owner))
	if err == sql.ErrNoRows {
		return Band{}, false, nil
	}
	if err != nil {
		return Band{}, false, err
	}
	if patch.NodeID != nil {
		if b.Revoked {
			return Band{}, false, nil
		}
		if *patch.NodeID != b.NodeID {
			var occupied bool
			if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM rogerai.private_bands
				WHERE node_id=$1 AND revoked=false AND id<>$2)`, *patch.NodeID, id).Scan(&occupied); err != nil {
				return Band{}, false, err
			}
			if occupied {
				return Band{}, false, ErrBandNodeOccupied
			}
		}
	}
	b = b.applyPatch(patch)
	if _, err := tx.Exec(`UPDATE rogerai.private_bands SET node_id=$3,label=$4
		WHERE id=$1 AND owner=$2`, id, owner, b.NodeID, b.Label); err != nil {
		return Band{}, false, bandNodeError(err)
	}
	if err := tx.Commit(); err != nil {
		return Band{}, false, bandNodeError(err)
	}
	return b, true, nil
}

// RotateBandCode swaps a LIVE band's secret in place. Same id, node, label, quota slot and
// cosmetic frequency; only code_hash and code_display change, so the OLD code stops
// resolving the instant this commits.
//
// The row is locked FOR UPDATE before the revoked check so a concurrent revoke cannot land
// between the read and the write - otherwise a rotate could resurrect a band that was
// burnt a microsecond earlier, handing back a working code for something the owner had
// just destroyed.
func (p *Postgres) RotateBandCode(id, owner, newHash, newDisplay string) (Band, bool, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return Band{}, false, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	b, err := p.scanBand(tx.QueryRow(`SELECT `+bandCols+` FROM rogerai.private_bands
		WHERE id=$1 AND owner=$2 FOR UPDATE`, id, owner))
	if err == sql.ErrNoRows {
		return Band{}, false, nil
	}
	if err != nil {
		return Band{}, false, err
	}
	if b.Revoked {
		// Revoke is final and surrendered the quota slot: rotating would resurrect a burnt
		// band under a working code. A fresh mint is the remedy, and it pays the quota.
		return Band{}, false, nil
	}
	if _, err := tx.Exec(`UPDATE rogerai.private_bands SET code_hash=$3,code_display=$4
		WHERE id=$1 AND owner=$2`, id, owner, newHash, newDisplay); err != nil {
		return Band{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Band{}, false, err
	}
	b.CodeHash, b.CodeDisplay = newHash, newDisplay
	return b, true, nil
}

// ForgetBand deletes a REVOKED band row, owner-scoped. Revoked rows were previously
// permanent and unremovable, so a list of dead entries grew forever around the live band.
// A LIVE band is refused: deleting it would drop its code out of the resolve index while
// consumers holding that code believe it still works, and free a quota slot with no
// confirm. Revoke first, then forget.
func (p *Postgres) ForgetBand(id, owner string) (bool, error) {
	res, err := p.db.Exec(`DELETE FROM rogerai.private_bands
		WHERE id=$1 AND owner=$2 AND revoked=true`, id, owner)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// MoveBand is the node-only compatibility wrapper around UpdateBand. The source-row lock
// serializes a concurrent revoke; the partial unique index serializes different source rows
// racing for one destination.
func (p *Postgres) MoveBand(id, owner, nodeID string) (bool, error) {
	_, ok, err := p.UpdateBand(id, owner, BandPatch{NodeID: &nodeID})
	return ok, err
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
