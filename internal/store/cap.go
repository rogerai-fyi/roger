package store

import (
	"os"
	"strconv"
	"strings"
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

// HoldGuard is every limit one hold must fit (HoldGuarded): the account key's spend limit
// (KeyID set and Key.USD > 0; the reservation is attributed to KeyID either way) and the
// wallet's monthly cap (MonthlyCap > 0, judged at Now).
type HoldGuard struct {
	KeyID      string
	KeyTS      int64
	Key        KeyLimit
	MonthlyCap float64
	Now        time.Time
}

// CappedHold is the outcome of HoldForCapped.
type CappedHold struct {
	OK      bool    // the hold landed
	OverCap bool    // the monthly cap refused it (nothing reserved)
	Spend   float64 // month-to-date captured spend the decision read
	Pending float64 // the wallet's open pending holds the decision read (before this hold)
}

// CapEpsilon absorbs float noise so a request that exactly fits the cap is allowed
// ($0.90 + $0.10 against $1.00) while any real excess ($0.9000001 + $0.10) is refused.
const CapEpsilon = 1e-9

// overCap reports whether spend + pending + amount exceeds a positive cap.
func overCap(spend, pending, amount, cap float64) bool {
	return cap > 0 && spend+pending+amount > cap+CapEpsilon
}

// HoldForCapped is HoldFor under the monthly cap, decided under m.mu: the month spend and
// the open pending holds are read and the hold placed in the same critical section, so two
// concurrent requests can never both fit a cap that has room for one. See the Store interface.
func (m *Mem) HoldForCapped(user, requestID string, amount, monthlyCap float64, now time.Time) (CappedHold, error) {
	return m.HoldGuarded(user, requestID, amount, HoldGuard{MonthlyCap: monthlyCap, Now: now})
}

// HoldGuarded: see Store. Every check runs under the one store mutex, like the transaction.
func (m *Mem) HoldGuarded(user, requestID string, amount float64, g HoldGuard) (CappedHold, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var res CappedHold
	if g.KeyID != "" && g.Key.USD > 0 && !g.Key.fits(m.keySpendLocked(g.KeyID, g.Key.From, g.Key.To), m.keyReservedLocked(g.KeyID, requestID), amount) {
		return res, ErrKeyLimit
	}
	if monthlyCap := g.MonthlyCap; monthlyCap > 0 {
		start, end := monthRange(g.Now)
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
	if g.KeyID != "" {
		if !m.holdRefLocked(user, amount, requestID) {
			return res, nil
		}
		m.pendingHolds[requestID] = pendingHold{user: user, amount: amount, placedAt: time.Now().Unix(), keyID: g.KeyID, keyTS: g.KeyTS}
		res.OK = true
		return res, nil
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
	Promo   bool // minted under a briefly posted price: it ends when that price does (LockPrice)
}

// StoredQuote is one price lock as stored: whose, on which station and model.
type StoredQuote struct {
	User, Node, Model string
	PriceQuote
}

// LockOpts are LockPrice's rules: Mint writes a lock when none is live; Promo marks the one
// it writes as a promo lock.
type LockOpts struct {
	Mint, Promo bool
}

// live reports whether q still binds at now against the station's current price.
func (q PriceQuote) live(now time.Time, curIn, curOut float64) bool {
	return now.Before(q.Until) && (!q.Promo || (q.In == curIn && q.Out == curOut))
}

// QuotePrice is the in-memory model of the price_quotes table: insert-if-absent under m.mu,
// an expired quote replaced. See the Store interface.
func (m *Mem) QuotePrice(user, node, model string, in, out float64, now time.Time, window time.Duration) (PriceQuote, error) {
	q, _, err := m.LockPrice(user, node, model, in, out, now, window, LockOpts{Mint: true})
	return q, err
}

// PriceQuotes: see Store.
func (m *Mem) PriceQuotes(node, model string) ([]StoredQuote, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []StoredQuote
	for k, q := range m.priceQuotes {
		parts := strings.SplitN(k, "|", 3)
		if len(parts) != 3 || (node != "" && parts[1] != node) || (model != "" && parts[2] != model) {
			continue
		}
		out = append(out, StoredQuote{User: parts[0], Node: parts[1], Model: parts[2], PriceQuote: q})
	}
	return out, nil
}

// LockPrice is the in-memory model of LockPrice's statement. See the Store interface.
func (m *Mem) LockPrice(user, node, model string, in, out float64, now time.Time, window time.Duration, o LockOpts) (PriceQuote, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.priceQuotes == nil {
		m.priceQuotes = map[string]PriceQuote{}
	}
	key := user + "|" + node + "|" + model
	if q, ok := m.priceQuotes[key]; ok && q.live(now, in, out) {
		return q, true, nil
	}
	if !o.Mint {
		return PriceQuote{}, false, nil
	}
	q := PriceQuote{In: in, Out: out, Until: now.Add(window), Promo: o.Promo}
	m.priceQuotes[key] = q
	return q, true, nil
}

// PruneExpiredPriceQuotes is the in-memory model of the bounded expired-quote delete.
func (m *Mem) PruneExpiredPriceQuotes(now time.Time, limit int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, q := range m.priceQuotes {
		if n >= limit {
			break
		}
		if !now.Before(q.Until) {
			delete(m.priceQuotes, k)
			n++
		}
	}
	return n, nil
}
