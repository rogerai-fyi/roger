package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// An email account wallet is a hash of the address, so it is guessable from the address the
// same way u_gh_ is from a GitHub id. Once email wallets are account wallets, an UNSIGNED legacy
// X-Roger-User header naming one must be refused by every account surface, or anyone who knows
// a person's address could read their balance and history.
func TestAnUnsignedHeaderCannotClaimAnEmailWallet(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)
	w := walletForEmail("erin@example.com")
	_, err := db.AddCredits(w, 5)
	require.NoError(t, err)

	require.True(t, reservedID(w), "the email wallet namespace is reserved from unsigned headers")
	require.True(t, isAccountWallet(w), "an email wallet is an account wallet")
	require.False(t, isAccountWallet("u_0123456789abcdef"), "a pubkey-derived id is not")

	for _, h := range []http.HandlerFunc{b.balance, b.me} {
		req := httptest.NewRequest(http.MethodGet, "/balance", nil)
		req.Header.Set(protocol.HeaderUser, w)
		rec := httptest.NewRecorder()
		h(rec, req)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		require.NotEqual(t, http.StatusOK, rec.Code, "an unsigned claim on an email wallet is refused: %s", rec.Body.String())
		require.Nil(t, body["balance"], "no balance is disclosed")
	}
}
