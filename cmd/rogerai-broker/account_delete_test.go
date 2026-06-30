package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rogerai-fyi/roger/internal/store"
)

// POST /account/delete now accepts the native app's SIGNED device key (not just the web session)
// so the app can delete in-app (App Store §5.1.1(v)). These pin: signed GitHub delete works +
// anonymizes; unsigned is rejected; a positive balance blocks; an Apple-only (no login) account
// is steered to the web rather than deleting the wrong row.
func TestAccountDeleteSigned(t *testing.T) {
	mem := store.NewMem()
	b := &broker{db: mem, pubOfUser: map[string]string{}}
	_, priv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	_ = mem.BindOwner(store.Owner{GitHubID: 42, Login: "octocat", Pubkey: pubHex})

	del := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/account/delete", nil)
		signReq(r, priv, nil)
		w := httptest.NewRecorder()
		b.accountDelete(w, r)
		return w
	}

	if w := del(); w.Code != http.StatusOK {
		t.Fatalf("signed delete = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	o, _, _ := mem.OwnerByPubkey(pubHex)
	if !o.Anonymized {
		t.Error("owner should be anonymized after a signed delete")
	}

	// Unsigned → 401.
	r := httptest.NewRequest(http.MethodPost, "/account/delete", nil)
	w := httptest.NewRecorder()
	b.accountDelete(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("unsigned delete = %d, want 401", w.Code)
	}
}

func TestAccountDeleteBlocksOnBalance(t *testing.T) {
	mem := store.NewMem()
	b := &broker{db: mem, pubOfUser: map[string]string{}}
	_, priv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	_ = mem.BindOwner(store.Owner{GitHubID: 7, Login: "rich", Pubkey: pubHex})
	_, _, _ = mem.SeedOnce("u_gh_7", 100) // account wallet holds a balance

	r := httptest.NewRequest(http.MethodPost, "/account/delete", nil)
	signReq(r, priv, nil)
	w := httptest.NewRecorder()
	b.accountDelete(w, r)
	if w.Code != http.StatusConflict {
		t.Errorf("delete with positive balance = %d, want 409", w.Code)
	}
}

func TestAccountDeleteAppleOnlySteeredToWeb(t *testing.T) {
	mem := store.NewMem()
	b := &broker{db: mem, pubOfUser: map[string]string{}}
	_, priv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	_ = mem.BindOwner(store.Owner{AppleSub: "apple-sub-1", Pubkey: pubHex}) // no GitHub login

	r := httptest.NewRequest(http.MethodPost, "/account/delete", nil)
	signReq(r, priv, nil)
	w := httptest.NewRecorder()
	b.accountDelete(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("apple-only delete = %d, want 409 (steer to web), got %s", w.Code, w.Body.String())
	}
	// And it must NOT have anonymized some other row.
	if o, _, _ := mem.OwnerByPubkey(pubHex); o.Anonymized {
		t.Error("apple-only account must not be anonymized by the in-app path")
	}
}
