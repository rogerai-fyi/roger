package store

import "sort"

// IdemClaim is one Idempotency-Key's claim (contract §14.B2, features/routing/idempotency.feature):
// scope (Payer, Key), the request that holds it, and where that request got to. Claiming is the
// money guard - one (payer, key) inside its window is claimed exactly once, so a retry never
// starts a second job, hold or settle. The stored response itself lives outside the store.
type IdemClaim struct {
	Payer       string // the scope: the paying identity (anonymous callers scoped further by the caller)
	Key         string
	Fingerprint string // sha256 of the body and the routing headers
	RequestID   string // the request holding the claim (its id is the replay's id)
	State       string // IdemInFlight | IdemDone | IdemStreamed | IdemTooLarge
	Deadline    int64  // unix ms the in-flight request gives up (the 409's Retry-After)
	Created     int64  // unix ms of the first request (the window runs from it)
}

// Claim states.
const (
	IdemInFlight = "inflight"
	IdemDone     = "done"     // finished, not a committed stream: replayable
	IdemStreamed = "streamed" // a stream that committed its first frame: never replayable
	IdemTooLarge = "toolarge" // finished, but its body is over the replay bound
)

func idemID(payer, key string) string { return payer + "\x00" + key }

func (m *Mem) ClaimIdempotency(c IdemClaim, since int64, maxPerPayer int) (IdemClaim, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idemClaims == nil {
		m.idemClaims = map[string]IdemClaim{}
	}
	if cur, ok := m.idemClaims[idemID(c.Payer, c.Key)]; ok && cur.Created >= since && !deadInflight(cur, c.Created) {
		return cur, false, nil
	}
	m.idemClaims[idemID(c.Payer, c.Key)] = c
	m.idemN++
	if m.idemSeq == nil {
		m.idemSeq = map[string]int64{}
	}
	m.idemSeq[idemID(c.Payer, c.Key)] = m.idemN // claim order breaks a tie on Created
	var mine []IdemClaim
	for id, x := range m.idemClaims {
		if x.Payer != c.Payer {
			continue
		}
		if x.Created < since && x.State != IdemInFlight {
			delete(m.idemClaims, id) // past the window and finished: swept now, not at the bound
			delete(m.idemSeq, id)
			continue
		}
		mine = append(mine, x)
	}
	if len(mine) > maxPerPayer {
		sort.Slice(mine, func(i, j int) bool {
			if mine[i].Created != mine[j].Created {
				return mine[i].Created > mine[j].Created
			}
			return m.idemSeq[idemID(mine[i].Payer, mine[i].Key)] > m.idemSeq[idemID(mine[j].Payer, mine[j].Key)]
		})
		for _, x := range mine[maxPerPayer:] {
			if x.State == IdemInFlight {
				continue // never evicted: a retry would win a second claim, so a second job and hold
			}
			delete(m.idemClaims, idemID(x.Payer, x.Key))
			delete(m.idemSeq, idemID(x.Payer, x.Key))
		}
	}
	return c, true, nil
}

// deadInflight reports whether cur is an in-flight claim past its deadline at now: its request
// gave up or its instance died, so the next claim may take the key over.
func deadInflight(cur IdemClaim, now int64) bool {
	return cur.State == IdemInFlight && cur.Deadline > 0 && cur.Deadline < now
}

func (m *Mem) FinishIdempotency(payer, key, requestID, state string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.idemClaims[idemID(payer, key)]; ok && cur.RequestID == requestID {
		cur.State = state
		m.idemClaims[idemID(payer, key)] = cur
	}
	return nil
}

func (m *Mem) ReleaseIdempotency(payer, key, requestID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.idemClaims[idemID(payer, key)]; ok && cur.RequestID == requestID {
		delete(m.idemClaims, idemID(payer, key))
		delete(m.idemSeq, idemID(payer, key))
	}
	return nil
}
