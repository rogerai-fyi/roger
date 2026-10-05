package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// idemCtxKey carries a client request's own Idempotency-Key into the failover loop.
type idemCtxKey struct{}

func withIdempotencyKey(ctx context.Context, key string) context.Context {
	if key == "" {
		return ctx
	}
	return context.WithValue(ctx, idemCtxKey{}, key)
}

// idempotencyKeyOf is the client's own key, or a fresh one minted for this client request
// (contract §14.B2): every proxy attempt of the request carries it.
func idempotencyKeyOf(ctx context.Context) string {
	if k, _ := ctx.Value(idemCtxKey{}).(string); k != "" {
		return k
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "rogerai-" + hex.EncodeToString(b[:])
}
