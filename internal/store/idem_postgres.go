package store

// Postgres Idempotency-Key claims (idem.go): one row per (payer, key) in
// rogerai.idempotency_claims. The first INSERT wins (ON CONFLICT DO NOTHING), so two instances
// racing the same key claim it exactly once; an expired row is taken over by a conditional
// UPDATE that only one racer can satisfy.

const idemCols = `payer,key,fingerprint,request_id,state,deadline,created`

func (p *Postgres) ClaimIdempotency(c IdemClaim, since int64, maxPerPayer int) (IdemClaim, bool, error) {
	res, err := p.db.Exec(`INSERT INTO rogerai.idempotency_claims (`+idemCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (payer,key) DO NOTHING`, c.Payer, c.Key, c.Fingerprint, c.RequestID, c.State, c.Deadline, c.Created)
	if err != nil {
		return IdemClaim{}, false, err
	}
	won, _ := res.RowsAffected()
	if won == 0 {
		// Taken: an expired claim is replaced (only one racer's conditional update lands).
		res, err = p.db.Exec(`UPDATE rogerai.idempotency_claims SET fingerprint=$3,request_id=$4,state=$5,deadline=$6,created=$7,
			seq=nextval(pg_get_serial_sequence('rogerai.idempotency_claims','seq'))
			WHERE payer=$1 AND key=$2 AND (created < $8 OR (state=$9 AND deadline > 0 AND deadline < $7))`,
			c.Payer, c.Key, c.Fingerprint, c.RequestID, c.State, c.Deadline, c.Created, since, IdemInFlight)
		if err != nil {
			return IdemClaim{}, false, err
		}
		if won, _ = res.RowsAffected(); won == 0 {
			var cur IdemClaim
			err := p.db.QueryRow(`SELECT `+idemCols+` FROM rogerai.idempotency_claims WHERE payer=$1 AND key=$2`, c.Payer, c.Key).
				Scan(&cur.Payer, &cur.Key, &cur.Fingerprint, &cur.RequestID, &cur.State, &cur.Deadline, &cur.Created)
			return cur, false, err
		}
	}
	// At most maxPerPayer keys per payer: the oldest FINISHED claims fall out early. An in-flight
	// claim is never evicted (a retry would win a second claim, so a second job and hold).
	if _, err := p.db.Exec(`DELETE FROM rogerai.idempotency_claims WHERE payer=$1 AND state<>$3 AND (created < $4 OR key IN (
		SELECT key FROM rogerai.idempotency_claims WHERE payer=$1 ORDER BY created DESC, seq DESC OFFSET $2))`, c.Payer, maxPerPayer, IdemInFlight, since); err != nil {
		return IdemClaim{}, false, err
	}
	return c, true, nil
}

func (p *Postgres) FinishIdempotency(payer, key, requestID, state string) error {
	_, err := p.db.Exec(`UPDATE rogerai.idempotency_claims SET state=$4 WHERE payer=$1 AND key=$2 AND request_id=$3`, payer, key, requestID, state)
	return err
}

func (p *Postgres) ReleaseIdempotency(payer, key, requestID string) error {
	_, err := p.db.Exec(`DELETE FROM rogerai.idempotency_claims WHERE payer=$1 AND key=$2 AND request_id=$3`, payer, key, requestID)
	return err
}
