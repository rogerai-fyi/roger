package store

import (
	"database/sql"
	"encoding/json"

	"rogerai.fm/roger/v6/internal/protocol"
)

// MetaOverride is an owner-authored taxonomy for one (node, model) - see Store.SetMetaOverride.
type MetaOverride struct {
	Owner     string            `json:"owner"`
	NodeID    string            `json:"node_id"`
	Model     string            `json:"model"`
	Taxonomy  protocol.Taxonomy `json:"taxonomy"`
	UpdatedAt int64             `json:"updated_at"`
}

func (m *Mem) SetMetaOverride(ov MetaOverride) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.metaOverrides == nil {
		m.metaOverrides = map[string]MetaOverride{}
	}
	ov.Taxonomy.Tags = append([]string(nil), ov.Taxonomy.Tags...) // never alias the caller's slice
	m.metaOverrides[overrideKey(ov.NodeID, ov.Model)] = ov
	return nil
}

func (m *Mem) MetaOverride(node, model string) (MetaOverride, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ov, ok := m.metaOverrides[overrideKey(node, model)]
	ov.Taxonomy.Tags = append([]string(nil), ov.Taxonomy.Tags...) // a copy: a reader never edits the store
	return ov, ok, nil
}

func (m *Mem) ClearMetaOverride(owner, node, model string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := overrideKey(node, model)
	ov, ok := m.metaOverrides[k]
	if !ok || ov.Owner != owner { // owner-scoped: never clear another account's override
		return false, nil
	}
	delete(m.metaOverrides, k)
	return true, nil
}

func (p *Postgres) SetMetaOverride(ov MetaOverride) error {
	tx, err := json.Marshal(ov.Taxonomy)
	if err != nil {
		return err
	}
	_, err = p.db.Exec(`
		INSERT INTO rogerai.offer_meta_overrides(node,model,owner,taxonomy,updated_at)
		VALUES($1,$2,$3,$4,$5)
		ON CONFLICT (node,model) DO UPDATE SET
			owner=EXCLUDED.owner, taxonomy=EXCLUDED.taxonomy, updated_at=EXCLUDED.updated_at`,
		ov.NodeID, ov.Model, ov.Owner, tx, ov.UpdatedAt)
	return err
}

func (p *Postgres) MetaOverride(node, model string) (MetaOverride, bool, error) {
	var (
		ov  MetaOverride
		raw []byte
	)
	err := p.db.QueryRow(`SELECT node, model, owner, taxonomy, updated_at
		FROM rogerai.offer_meta_overrides WHERE node=$1 AND model=$2`, node, model).
		Scan(&ov.NodeID, &ov.Model, &ov.Owner, &raw, &ov.UpdatedAt)
	if err == sql.ErrNoRows {
		return MetaOverride{}, false, nil
	}
	if err != nil {
		return MetaOverride{}, false, err
	}
	if err := json.Unmarshal(raw, &ov.Taxonomy); err != nil {
		return MetaOverride{}, false, err
	}
	return ov, true, nil
}

func (p *Postgres) ClearMetaOverride(owner, node, model string) (bool, error) {
	res, err := p.db.Exec(`DELETE FROM rogerai.offer_meta_overrides WHERE node=$1 AND model=$2 AND owner=$3`,
		node, model, owner)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
