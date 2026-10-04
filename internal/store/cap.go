package store

import (
	"os"
	"strconv"
	"time"
)

// Per-account MONTHLY SPEND CAP (a budget limit, modeled on Groq's "set a max you'll
// pay per month, notify + stop at the limit"). The cap is a per-wallet $ ceiling on
// CAPTURED spend within the current CALENDAR month; enforcement lives broker-side at
// the credit-hold path so it is GLOBAL across every paid consume path. Default =
// unlimited (opt-in); an env default (ROGERAI_DEFAULT_MONTHLY_CAP, 0 = unlimited)
// seeds a starting cap for new wallets. Self-use / free ($0) is never blocked.

// CapNearThreshold is the fraction of the cap at which the "approaching your monthly
// budget" notification fires (80%). At/above 100% spend is rejected before dispatch.
const CapNearThreshold = 0.80

// monthRange returns [start, end) unix-second bounds of the CALENDAR month containing
// `now`, in UTC. A spend row counts toward the month-to-date total iff start <= ts <
// end. The UTC month matches the grant-usage month window (monthKey) so the two cap
// systems share one calendar definition.
func monthRange(now time.Time) (start, end int64) {
	u := now.UTC()
	s := time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
	e := s.AddDate(0, 1, 0)
	return s.Unix(), e.Unix()
}

// DefaultMonthlyCap reads the env-configured starting cap for a NEW wallet. 0 (the
// default) means unlimited. Negative is treated as unlimited (0). This is the cap a
// wallet has before the account ever sets its own.
func DefaultMonthlyCap() float64 {
	v := os.Getenv("ROGERAI_DEFAULT_MONTHLY_CAP")
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 0
	}
	return f
}

// --- Mem monthly-cap storage ---------------------------------------------
//
// A wallet's explicitly-set cap overrides the env default. The map stores only
// explicit choices; an absent entry resolves to DefaultMonthlyCap (so changing the
// env default moves every un-set wallet at once). A stored 0 means the account chose
// "unlimited" explicitly and is NOT re-defaulted.

func (m *Mem) MonthlyCapOf(holder string) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.monthlyCap != nil {
		if c, ok := m.monthlyCap[holder]; ok {
			return c, nil
		}
	}
	return DefaultMonthlyCap(), nil
}

// SetMonthlyCap durably records a wallet's monthly cap. cap<=0 stores 0 = unlimited
// (an explicit opt-out that is not re-defaulted from the env).
func (m *Mem) SetMonthlyCap(holder string, cap float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.monthlyCap == nil {
		m.monthlyCap = map[string]float64{}
	}
	if cap < 0 {
		cap = 0
	}
	m.monthlyCap[holder] = cap
	return nil
}

// MonthSpendOf returns a holder's CAPTURED spend (positive credits) within the
// calendar month containing `now`, summed from the append-only ledger's posted
// `spend` rows. Boundary-correct (a row exactly at the previous month's end is
// excluded; the new month starts clean) and DeriveBalance-style (the ledger is the
// source of truth, so a maintained counter can never drift from it).
func (m *Mem) MonthSpendOf(holder string, now time.Time) (float64, error) {
	start, end := monthRange(now)
	m.mu.Lock()
	defer m.mu.Unlock()
	var sum float64
	for _, r := range m.ledger {
		if r.Holder != holder || r.Kind != KindSpend || r.State == StateReversed {
			continue
		}
		if r.TS >= start && r.TS < end {
			sum += -r.Amount // spend rows are negative; month-to-date spend is positive
		}
	}
	return sum, nil
}

// CappedHold is the outcome of HoldForCapped.
type CappedHold struct {
	OK      bool    // the hold landed
	OverCap bool    // the monthly cap refused it (nothing reserved)
	Spend   float64 // month-to-date captured spend the decision read
	Pending float64 // the wallet's open pending holds the decision read (before this hold)
}

// capEpsilon absorbs float noise so a request that exactly fits the cap is allowed
// ($0.90 + $0.10 against $1.00) while any real excess ($0.9000001 + $0.10) is refused.
const capEpsilon = 1e-9

// overCap reports whether spend + pending + amount exceeds a positive cap.
func overCap(spend, pending, amount, cap float64) bool {
	return cap > 0 && spend+pending+amount > cap+capEpsilon
}

// HoldForCapped is HoldFor under the monthly cap, decided under m.mu: the month spend and
// the open pending holds are read and the hold placed in the same critical section, so two
// concurrent requests can never both fit a cap that has room for one. See the Store interface.
func (m *Mem) HoldForCapped(user, requestID string, amount, monthlyCap float64, now time.Time) (CappedHold, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var res CappedHold
	if monthlyCap > 0 {
		start, end := monthRange(now)
		for _, r := range m.ledger {
			if r.Holder == user && r.Kind == KindSpend && r.State != StateReversed && r.TS >= start && r.TS < end {
				res.Spend += -r.Amount
			}
		}
		for _, h := range m.pendingHolds {
			if h.user == user {
				res.Pending += h.amount
			}
		}
		if overCap(res.Spend, res.Pending, amount, monthlyCap) {
			res.OverCap = true
			return res, nil
		}
	}
	if !m.holdLocked(user, amount) {
		return res, nil
	}
	m.pendingHolds[requestID] = pendingHold{user: user, amount: amount, placedAt: time.Now().Unix()}
	res.OK = true
	return res, nil
}

// PriceQuote is a durable 24 h price lock (see Store.QuotePrice).
type PriceQuote struct {
	In, Out float64
	Until   time.Time
}

// QuotePrice is the in-memory model of the price_quotes table: insert-if-absent under m.mu,
// an expired quote replaced. See the Store interface.
func (m *Mem) QuotePrice(user, node, model string, in, out float64, now time.Time, window time.Duration) (PriceQuote, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.priceQuotes == nil {
		m.priceQuotes = map[string]PriceQuote{}
	}
	key := user + "|" + node + "|" + model
	if q, ok := m.priceQuotes[key]; ok && now.Before(q.Until) {
		return q, nil
	}
	q := PriceQuote{In: in, Out: out, Until: now.Add(window)}
	m.priceQuotes[key] = q
	return q, nil
}
