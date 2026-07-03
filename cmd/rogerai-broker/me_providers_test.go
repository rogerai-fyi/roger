package main

// me_providers_test.go pins GET /me's linked-providers field: the app reads it to show a check
// on the sign-in the account already has (GitHub / Apple) and target the missing one for "Link
// another sign-in". Resolved by the request's SIGNING PUBKEY, so it works for a github-only,
// apple-only, or dual-linked account. Real /me handler over store.NewMem(), no mocks.

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/rogerai-fyi/roger/internal/store"
)

func TestMeLinkedProviders(t *testing.T) {
	cases := []struct {
		name  string
		owner store.Owner
		want  []string
	}{
		{"github only", store.Owner{GitHubID: 7, Login: "octocat"}, []string{"github"}},
		{"apple only", store.Owner{AppleSub: "apple-sub-abc"}, []string{"apple"}},
		{"both linked", store.Owner{GitHubID: 7, Login: "octocat", AppleSub: "apple-sub-abc"}, []string{"github", "apple"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mem := store.NewMem()
			b := relayBroker(mem)
			pub, priv, _ := ed25519.GenerateKey(nil)
			o := c.owner
			o.Pubkey = hex.EncodeToString(pub)
			if err := mem.BindOwner(o); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodGet, "/me", nil)
			signReq(r, priv, nil)
			w := httptest.NewRecorder()
			b.me(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("me = %d (%s)", w.Code, w.Body.String())
			}
			var resp struct {
				LoggedIn  bool     `json:"logged_in"`
				Providers []string `json:"providers"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if !resp.LoggedIn {
				t.Fatalf("a bound owner should read logged_in=true: %s", w.Body.String())
			}
			if !reflect.DeepEqual(resp.Providers, c.want) {
				t.Fatalf("providers = %v, want %v (%s)", resp.Providers, c.want, w.Body.String())
			}
		})
	}
}

// An anonymous (unbound) keypair reports no providers and logged_in=false - no account to link.
func TestMeAnonymousNoProviders(t *testing.T) {
	b := relayBroker(store.NewMem())
	_, priv, _ := ed25519.GenerateKey(nil)
	r := httptest.NewRequest(http.MethodGet, "/me", nil)
	signReq(r, priv, nil)
	w := httptest.NewRecorder()
	b.me(w, r)
	var resp struct {
		LoggedIn  bool     `json:"logged_in"`
		Providers []string `json:"providers"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.LoggedIn {
		t.Fatalf("an unbound keypair must not be logged_in: %s", w.Body.String())
	}
	if len(resp.Providers) != 0 {
		t.Fatalf("an unbound keypair must have no providers, got %v", resp.Providers)
	}
}
