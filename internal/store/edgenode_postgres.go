package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// Postgres Roger Edge fleet storage. Mirrors the additive style of the grants methods:
// the record travels as JSONB, with the three columns the database must ENFORCE lifted
// out - (account,node_id) as the primary key, node_id UNIQUE for "one Edge per node",
// and (account,name) UNIQUE for "a name is unique within the account". Enforcing those
// in the schema rather than in Go is what makes the rule hold under two brokers.

// edgeRowScan maps one edge_nodes row into an EdgeNode.
func edgeRowScan(row interface{ Scan(...any) error }) (EdgeNode, error) {
	var blob []byte
	if err := row.Scan(&blob); err != nil {
		return EdgeNode{}, err
	}
	var n EdgeNode
	if err := json.Unmarshal(blob, &n); err != nil {
		return EdgeNode{}, err
	}
	n.Station = false // never persisted; recomputed on every listing
	return n, nil
}

// edgeConflict turns a unique-violation into the spec's error. Which constraint tripped
// is the difference between "that name is taken" and "that node is on another Edge", so
// it is read from the constraint name rather than guessed.
func (p *Postgres) edgeConflict(err error, n EdgeNode) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "edge_nodes_one_edge"):
		var owner string
		if qerr := p.db.QueryRow(`SELECT account FROM rogerai.edge_nodes WHERE node_id=$1`, n.ID).Scan(&owner); qerr != nil {
			owner = ""
		}
		return &EdgeEnrolledElsewhere{Existing: owner}
	case strings.Contains(msg, "edge_nodes_name"):
		return ErrEdgeNameTaken
	}
	return err
}

func (p *Postgres) EnrollEdgeNode(n EdgeNode) (EdgeNode, error) {
	n.Station = false
	blob, err := json.Marshal(n)
	if err != nil {
		return EdgeNode{}, err
	}
	// ON CONFLICT on the PRIMARY KEY only: re-enrolling a node this account already
	// holds refreshes it, while the two UNIQUE indexes still raise for the cases the
	// spec refuses (another Edge, another node's name).
	_, err = p.db.Exec(`INSERT INTO rogerai.edge_nodes(account,node_id,name,rec)
		VALUES($1,$2,$3,$4)
		ON CONFLICT (account,node_id) DO UPDATE SET name=EXCLUDED.name, rec=EXCLUDED.rec`,
		n.Account, n.ID, n.Name, blob)
	if err != nil {
		return EdgeNode{}, p.edgeConflict(err, n)
	}
	return n, nil
}

func (p *Postgres) UpdateEdgeNode(n EdgeNode) error {
	n.Station = false
	blob, err := json.Marshal(n)
	if err != nil {
		return err
	}
	res, err := p.db.Exec(`UPDATE rogerai.edge_nodes SET name=$3, rec=$4
		WHERE account=$1 AND node_id=$2`, n.Account, n.ID, n.Name, blob)
	if err != nil {
		return p.edgeConflict(err, n)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return ErrEdgeNoSuchNode
	}
	return nil
}

func (p *Postgres) EdgeNodeByID(account, id string) (EdgeNode, bool, error) {
	n, err := edgeRowScan(p.db.QueryRow(
		`SELECT rec FROM rogerai.edge_nodes WHERE account=$1 AND node_id=$2`, account, id))
	if errors.Is(err, sql.ErrNoRows) {
		return EdgeNode{}, false, nil
	}
	if err != nil {
		return EdgeNode{}, false, err
	}
	return n, true, nil
}

func (p *Postgres) EdgeNodesOfAccount(account string) ([]EdgeNode, error) {
	rows, err := p.db.Query(`SELECT rec FROM rogerai.edge_nodes WHERE account=$1
		ORDER BY name, node_id`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EdgeNode
	for rows.Next() {
		n, err := edgeRowScan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (p *Postgres) ForgetEdgeNode(account, id string) (bool, error) {
	res, err := p.db.Exec(`DELETE FROM rogerai.edge_nodes WHERE account=$1 AND node_id=$2`, account, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
