package store

import "time"

// admin_postgres.go is the Postgres twin of admin.go (the super-admin aggregates). Each
// method is one (or a few) bounded GROUP BY / COUNT over the existing tables, so an
// admin overview is cheap even on a large ledger. Parity with Mem is asserted by the
// store parity tests. All amounts are credits ($ at credit_usd=1).

func (p *Postgres) AdminFinancials(now time.Time) (AdminFinancials, error) {
	var f AdminFinancials
	// Sweep held->payable for any lot whose release has passed, so the split matches the
	// per-account sweep-on-read (same promoteLots the per-account split uses).
	if err := p.promoteLots(now); err != nil {
		return f, err
	}
	// Consumer spend + wallet liability/count off receipts + wallet.
	if err := p.db.QueryRow(`SELECT COALESCE(SUM(cost),0) FROM rogerai.receipts`).Scan(&f.ConsumerSpend); err != nil {
		return f, err
	}
	if err := p.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(balance),0) FROM rogerai.wallet`).Scan(&f.WalletCount, &f.WalletBalance); err != nil {
		return f, err
	}
	if err := p.db.QueryRow(`SELECT COUNT(*) FROM rogerai.owners WHERE COALESCE(anonymized,false)=false`).Scan(&f.OwnerCount); err != nil {
		return f, err
	}
	if err := p.db.QueryRow(`SELECT COUNT(*) FROM rogerai.node_owner`).Scan(&f.NodeBindings); err != nil {
		return f, err
	}
	// Lot lifecycle totals. held/payable carry gross-minus-reserve; the reserve splits by
	// reserve_release_at. operator_earned is gross across non-clawed lots.
	rows, err := p.db.Query(`
		SELECT state,
		       COALESCE(SUM(gross),0),
		       COALESCE(SUM(gross-reserve),0),
		       COALESCE(SUM(reserve),0),
		       COALESCE(SUM(CASE WHEN reserve_release_at <= $1 THEN reserve ELSE 0 END),0)
		FROM rogerai.earning_lots GROUP BY state`, now.Unix())
	if err != nil {
		return f, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var gross, net, reserve, reserveReleased float64
		if err := rows.Scan(&state, &gross, &net, &reserve, &reserveReleased); err != nil {
			return f, err
		}
		switch state {
		case LotHeld:
			f.Held += net
			f.Reserved += reserve
			f.OperatorEarned += gross
		case LotPayable:
			f.Payable += net + reserveReleased
			f.Reserved += reserve - reserveReleased
			f.OperatorEarned += gross
		case LotPaid:
			f.Paid += gross
			f.OperatorEarned += gross
		case LotClawed:
			f.Clawed += gross
		}
	}
	if err := rows.Err(); err != nil {
		return f, err
	}
	// Topup volume + platform loss off the ledger (exclude reversed rows).
	if err := p.db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM rogerai.ledger
		WHERE kind=$1 AND state<>$2`, KindTopup, StateReversed).Scan(&f.TopupVolume); err != nil {
		return f, err
	}
	if err := p.db.QueryRow(`SELECT COALESCE(-SUM(amount),0) FROM rogerai.ledger
		WHERE kind=$1 AND state<>$2`, KindPlatformLoss, StateReversed).Scan(&f.PlatformLoss); err != nil {
		return f, err
	}
	f.PlatformFee = f.ConsumerSpend - f.OperatorEarned
	if f.PlatformFee < 0 {
		f.PlatformFee = 0
	}
	return roundFinancials(f), nil
}

func (p *Postgres) AdminMarketTotals(since, until int64) (AdminMarketTotals, error) {
	var t AdminMarketTotals
	if err := p.db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0)
		FROM rogerai.receipts`).Scan(&t.Requests, &t.TokensIn, &t.TokensOut); err != nil {
		return t, err
	}
	if err := p.db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0)
		FROM rogerai.receipts WHERE ts >= $1 AND ts < $2`, since, until).
		Scan(&t.WindowRequests, &t.WindowTokensIn, &t.WindowTokensOut); err != nil {
		return t, err
	}
	return t, nil
}

func (p *Postgres) AdminPayoutQueue(now time.Time, limit int) ([]AdminPayoutQueueRow, error) {
	if err := p.promoteLots(now); err != nil {
		return nil, err
	}
	q := `
		WITH lots AS (
			SELECT account_id,
			       SUM(CASE WHEN state='payable' THEN (gross-reserve) + CASE WHEN reserve_release_at <= $1 THEN reserve ELSE 0 END ELSE 0 END) AS payable,
			       SUM(CASE WHEN state='held' THEN gross-reserve ELSE 0 END) AS held,
			       SUM(CASE WHEN state='paid' THEN gross ELSE 0 END) AS paid
			FROM rogerai.earning_lots GROUP BY account_id),
		pend AS (
			SELECT account_id, COALESCE(SUM(amount),0) AS pending
			FROM rogerai.payouts WHERE state='pending' GROUP BY account_id)
		SELECT l.account_id, COALESCE(l.payable,0), COALESCE(l.held,0), COALESCE(l.paid,0), COALESCE(pend.pending,0)
		FROM lots l LEFT JOIN pend ON pend.account_id = l.account_id
		ORDER BY COALESCE(l.payable,0) DESC, COALESCE(l.held,0) DESC, l.account_id`
	rows, err := p.db.Query(q, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminPayoutQueueRow
	for rows.Next() {
		var r AdminPayoutQueueRow
		if err := rows.Scan(&r.AccountID, &r.Payable, &r.Held, &r.Paid, &r.Pending); err != nil {
			return nil, err
		}
		r.Payable, r.Held, r.Paid, r.Pending = round6(r.Payable), round6(r.Held), round6(r.Paid), round6(r.Pending)
		out = append(out, r)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

func (p *Postgres) AdminAllPayouts(limit int) ([]Payout, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := p.db.Query(`SELECT id, account_id, amount, COALESCE(stripe_transfer_id,''), state, created_at
	      FROM rogerai.payouts ORDER BY id DESC LIMIT $1`, limit)
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

func (p *Postgres) AdminAbuse() (AdminAbuse, error) {
	var a AdminAbuse
	// strike counts per account (drives struck-account + per-banned-owner strike count).
	strikeByAcct := map[string]int{}
	rows, err := p.db.Query(`SELECT account_id, COUNT(*) FROM rogerai.owner_strikes GROUP BY account_id`)
	if err != nil {
		return a, err
	}
	for rows.Next() {
		var acct string
		var n int
		if err := rows.Scan(&acct, &n); err != nil {
			rows.Close()
			return a, err
		}
		strikeByAcct[acct] = n
		a.TotalStrikes += n
		a.StruckAccounts++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return a, err
	}
	brows, err := p.db.Query(`SELECT account_id, COALESCE(reason,'') FROM rogerai.banned_owners ORDER BY account_id`)
	if err != nil {
		return a, err
	}
	a.BannedOwners = []AdminBannedOwner{}
	for brows.Next() {
		var bo AdminBannedOwner
		if err := brows.Scan(&bo.AccountID, &bo.Reason); err != nil {
			brows.Close()
			return a, err
		}
		bo.Strikes = strikeByAcct[bo.AccountID]
		a.BannedOwners = append(a.BannedOwners, bo)
	}
	brows.Close()
	if err := brows.Err(); err != nil {
		return a, err
	}
	_ = p.db.QueryRow(`SELECT COUNT(*) FROM rogerai.csam_incidents`).Scan(&a.CSAMTotal)
	_ = p.db.QueryRow(`SELECT COUNT(*) FROM rogerai.csam_incidents WHERE report_state=$1`, CSAMQueued).Scan(&a.CSAMQueued)
	_ = p.db.QueryRow(`SELECT COUNT(*) FROM rogerai.reports`).Scan(&a.ReportCount)
	_ = p.db.QueryRow(`SELECT COUNT(*) FROM rogerai.banned_nodes`).Scan(&a.BannedNodes)
	_ = p.db.QueryRow(`SELECT COUNT(*) FROM rogerai.disputes`).Scan(&a.DisputeCount)
	_ = p.db.QueryRow(`SELECT COUNT(*) FROM rogerai.account_recount_holds`).Scan(&a.AccountHolds)
	return a, nil
}

func (p *Postgres) AdminActivity(limit int) ([]LedgerRow, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := p.db.Query(`SELECT id, holder, side, kind, amount, COALESCE(idem_key,''), state, COALESCE(ref,''), ts
	      FROM rogerai.ledger ORDER BY id DESC LIMIT $1`, limit)
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
