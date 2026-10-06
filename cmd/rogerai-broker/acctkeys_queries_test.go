package main

// acctkeys_queries_test.go: reading a key and attributing an account's key spend cost a fixed
// number of store reads, not one per window per key (slice-5 review 2026-10-05). A key listing
// of 32 keys or a /usage call must not fan out into hundreds of queries.

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
)

// keyQueryCounter counts the store reads that sum or attribute key spend.
type keyQueryCounter struct {
	store.Store
	mu sync.Mutex
	n  int
}

func (c *keyQueryCounter) hit() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}
func (c *keyQueryCounter) KeySpend(id string, from, to int64) (float64, error) {
	c.hit()
	return c.Store.KeySpend(id, from, to)
}
func (c *keyQueryCounter) KeyReserved(id string) (float64, error) {
	c.hit()
	return c.Store.KeyReserved(id)
}
func (c *keyQueryCounter) KeySpendSince(id string, froms []int64) ([]float64, error) {
	c.hit()
	return c.Store.KeySpendSince(id, froms)
}
func (c *keyQueryCounter) AccountKeySpendRefs(acct string) (map[string]string, error) {
	c.hit()
	return c.Store.AccountKeySpendRefs(acct)
}
func (c *keyQueryCounter) take() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.n
	c.n = 0
	return n
}

func TestKeyReadsAreNotFannedOut(t *testing.T) {
	db := &keyQueryCounter{Store: store.NewMem()}
	b := &broker{db: db}
	acct := "u_q_" + fmt.Sprint(time.Now().UnixNano())
	var k store.AccountKey
	for i := 0; i < 5; i++ {
		var err error
		k, err = db.CreateAccountKey(store.AccountKey{ID: fmt.Sprintf("key_q%d", i), SecretHash: fmt.Sprintf("hq%d", i),
			Account: acct, Reset: "daily", LimitUSD: 5, CreatedAt: time.Now().UnixNano()}, store.MintKeyRules{})
		require.NoError(t, err)
	}
	db.take()

	e := b.keyEntry(k, time.Now())
	require.NotNil(t, e["limit_remaining"])
	require.LessOrEqual(t, db.take(), 2, "one key's entry: its window sums in one read, plus its open reservations")

	b.keyRefsOf(acct)
	require.LessOrEqual(t, db.take(), 1, "attributing an account's key spend is one read, not one per key")
}
