package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// --- test PKI + JWS helpers (REAL crypto, no mocks) ---

func genCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen ca key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test root"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create ca: %v", err)
	}
	c, _ := x509.ParseCertificate(der)
	return c, key
}

func genLeaf(t *testing.T, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, notAfter time.Time) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen leaf key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "test leaf"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatalf("create leaf: %v", err)
	}
	c, _ := x509.ParseCertificate(der)
	return c, key
}

// makeJWS builds a StoreKit-style JWS: header{alg, x5c=std-base64 DER leaf..root}, payload, ES256 raw r||s.
func makeJWS(t *testing.T, leafKey *ecdsa.PrivateKey, chain []*x509.Certificate, payload any, alg string) string {
	t.Helper()
	x5c := make([]string, len(chain))
	for i, c := range chain {
		x5c[i] = base64.StdEncoding.EncodeToString(c.Raw)
	}
	hb, _ := json.Marshal(map[string]any{"alg": alg, "x5c": x5c})
	pb, _ := json.Marshal(payload)
	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(pb)
	sum := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, leafKey, sum[:])
	if err != nil {
		t.Fatalf("sign jws: %v", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func goodTxn() storeKitTxn {
	return storeKitTxn{
		BundleID: iapBundleID, ProductID: "fyi.rogerai.topup.10", TransactionID: "txn-1",
		OriginalTransactionID: "otxn-1", Type: "Consumable", Environment: "Sandbox",
	}
}

// --- verifier unit tests (real chain) ---

func TestVerifyStoreKitJWS(t *testing.T) {
	root, rootKey := genCA(t)
	leaf, leafKey := genLeaf(t, root, rootKey, time.Now().Add(time.Hour))
	chain := []*x509.Certificate{leaf, root}
	now := time.Now()

	// valid
	jws := makeJWS(t, leafKey, chain, goodTxn(), "ES256")
	if txn, ok := verifyStoreKitJWS(jws, root, now); !ok || txn.TransactionID != "txn-1" || txn.ProductID != "fyi.rogerai.topup.10" {
		t.Fatalf("valid jws should verify, got ok=%v txn=%+v", ok, txn)
	}
	// nil root -> false
	if _, ok := verifyStoreKitJWS(jws, nil, now); ok {
		t.Error("nil root must not verify")
	}
	// wrong (unrelated) root -> chain fails
	other, _ := genCA(t)
	if _, ok := verifyStoreKitJWS(jws, other, now); ok {
		t.Error("a JWS whose chain does not reach the pinned root must fail")
	}
	// alg != ES256 (alg-confusion) -> false, even though the bytes are ES256-signed
	if _, ok := verifyStoreKitJWS(makeJWS(t, leafKey, chain, goodTxn(), "RS256"), root, now); ok {
		t.Error("non-ES256 alg header must be rejected")
	}
	// tampered signature -> false
	bad := jws[:len(jws)-2] + func() string {
		if jws[len(jws)-1] == 'A' {
			return "BB"
		}
		return "AA"
	}()
	if _, ok := verifyStoreKitJWS(bad, root, now); ok {
		t.Error("tampered signature must be rejected")
	}
	// expired leaf -> false
	expLeaf, expKey := genLeaf(t, root, rootKey, time.Now().Add(-time.Minute))
	expJWS := makeJWS(t, expKey, []*x509.Certificate{expLeaf, root}, goodTxn(), "ES256")
	if _, ok := verifyStoreKitJWS(expJWS, root, now); ok {
		t.Error("expired leaf cert must be rejected")
	}
	// garbage
	if _, ok := verifyStoreKitJWS("not.a.jws", root, now); ok {
		t.Error("garbage must be rejected")
	}
}

// --- handler tests (full flow: verify -> credit -> idempotency) ---

func iapPost(t *testing.T, b *broker, priv ed25519.PrivateKey, jws, productID string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"jws": jws, "product_id": productID})
	r := httptest.NewRequest(http.MethodPost, "/iap/credit", bytes.NewReader(body))
	signReq(r, priv, body)
	w := httptest.NewRecorder()
	b.iapCredit(w, r)
	return w
}

func decodeIAP(t *testing.T, w *httptest.ResponseRecorder) (credited bool, balance, usd float64) {
	t.Helper()
	var out struct {
		Credited bool    `json:"credited"`
		Balance  float64 `json:"balance"`
		USD      float64 `json:"usd"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad response %q: %v", w.Body.String(), err)
	}
	return out.Credited, out.Balance, out.USD
}

func TestIAPCreditAndIdempotency(t *testing.T) {
	root, rootKey := genCA(t)
	leaf, leafKey := genLeaf(t, root, rootKey, time.Now().Add(time.Hour))
	chain := []*x509.Certificate{leaf, root}
	appleRoot = root
	defer func() { appleRoot = nil }()

	mem := store.NewMem()
	b := &broker{db: mem, pubOfUser: map[string]string{}, seedFunds: 0}
	_, priv, _ := ed25519.GenerateKey(nil)
	jws := makeJWS(t, leafKey, chain, goodTxn(), "ES256")

	// first credit
	w := iapPost(t, b, priv, jws, "fyi.rogerai.topup.10")
	if w.Code != http.StatusOK {
		t.Fatalf("first credit status %d: %s", w.Code, w.Body.String())
	}
	credited, bal, usd := decodeIAP(t, w)
	if !credited || bal != 10 || usd != 10 {
		t.Fatalf("first credit = credited:%v bal:%v usd:%v, want true/10/10", credited, bal, usd)
	}

	// REPLAY the same transaction (the app's purchase POST + Transaction.updates can both fire):
	// credits ZERO, balance unchanged.
	w2 := iapPost(t, b, priv, jws, "fyi.rogerai.topup.10")
	credited2, bal2, _ := decodeIAP(t, w2)
	if credited2 {
		t.Error("replayed transaction must not credit again")
	}
	if bal2 != 10 {
		t.Errorf("balance after replay = %v, want 10 (no double-credit)", bal2)
	}

	// the refund-resolution mapping was persisted (Stage D reuses WalletByCharge on the Apple ref)
	if wlt, cr, ok, _ := mem.WalletByCharge("apple:txn-1"); !ok || cr != 10 || wlt == "" {
		t.Errorf("apple txn should map to the wallet for refund resolution, got ok=%v wallet=%q credits=%v", ok, wlt, cr)
	}
}

func TestIAPCreditRejections(t *testing.T) {
	root, rootKey := genCA(t)
	leaf, leafKey := genLeaf(t, root, rootKey, time.Now().Add(time.Hour))
	chain := []*x509.Certificate{leaf, root}
	appleRoot = root
	defer func() { appleRoot = nil }()
	_, priv, _ := ed25519.GenerateKey(nil)

	newB := func() *broker { return &broker{db: store.NewMem(), pubOfUser: map[string]string{}, seedFunds: 0} }
	jwsOf := func(txn storeKitTxn) string { return makeJWS(t, leafKey, chain, txn, "ES256") }

	cases := []struct {
		name string
		txn  storeKitTxn
		want int
	}{
		{"wrong bundle", func() storeKitTxn { x := goodTxn(); x.BundleID = "com.evil.app"; return x }(), http.StatusBadRequest},
		{"wrong type", func() storeKitTxn { x := goodTxn(); x.Type = "Auto-Renewable Subscription"; return x }(), http.StatusBadRequest},
		{"unknown product", func() storeKitTxn { x := goodTxn(); x.ProductID = "fyi.rogerai.topup.999"; return x }(), http.StatusBadRequest},
	}
	for _, c := range cases {
		w := iapPost(t, newB(), priv, jwsOf(c.txn), "")
		if w.Code != c.want {
			t.Errorf("%s: status %d, want %d (%s)", c.name, w.Code, c.want, w.Body.String())
		}
	}

	// unsigned request -> 401
	body, _ := json.Marshal(map[string]string{"jws": jwsOf(goodTxn())})
	r := httptest.NewRequest(http.MethodPost, "/iap/credit", bytes.NewReader(body))
	w := httptest.NewRecorder()
	newB().iapCredit(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("unsigned request status %d, want 401", w.Code)
	}

	// unconfigured root -> 503
	appleRoot = nil
	w503 := iapPost(t, newB(), priv, jwsOf(goodTxn()), "")
	if w503.Code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured root status %d, want 503", w503.Code)
	}
	appleRoot = root // restore for defer symmetry (defer sets nil anyway)
}

// TestLoadAppleRoot covers the startup config: parsing a PEM root and loading it from the env
// (and the fail-closed branches - no root / invalid PEM leave appleRoot nil so /iap/credit is 503).
func TestLoadAppleRoot(t *testing.T) {
	root, _ := genCA(t)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw})

	if parseCertPEM(string(pemBytes)) == nil {
		t.Fatal("a valid CERTIFICATE PEM should parse")
	}
	if parseCertPEM("not a pem block") != nil {
		t.Error("garbage should not parse")
	}
	if parseCertPEM("-----BEGIN CERTIFICATE-----\nZm9v\n-----END CERTIFICATE-----") != nil {
		t.Error("a PEM block with non-cert DER should not parse")
	}

	defer func() { appleRoot = nil }()
	// env-provided PEM loads
	appleRoot = nil
	t.Setenv("ROGERAI_APPLE_ROOT_PEM", string(pemBytes))
	loadAppleRoot()
	if appleRoot == nil {
		t.Error("loadAppleRoot should set the root from ROGERAI_APPLE_ROOT_PEM")
	}
	// invalid PEM leaves it unset (fail-closed)
	appleRoot = nil
	t.Setenv("ROGERAI_APPLE_ROOT_PEM", "garbage")
	loadAppleRoot()
	if appleRoot != nil {
		t.Error("an invalid root PEM must leave appleRoot nil (fail-closed)")
	}
	// with no env override, the EMBEDDED Apple Root CA - G3 is pinned and loads - and it is genuinely
	// that cert (guards against a wrong/placeholder PEM ever being shipped).
	appleRoot = nil
	t.Setenv("ROGERAI_APPLE_ROOT_PEM", "")
	loadAppleRoot()
	if appleRoot == nil {
		t.Fatal("the embedded Apple root should load when no env override is set")
	}
	if cn := appleRoot.Subject.CommonName; cn != "Apple Root CA - G3" {
		t.Errorf("embedded root CN = %q, want \"Apple Root CA - G3\"", cn)
	}
}

// TestIAPRequireLiveRefusesSandbox: in require-live mode a Sandbox transaction must not credit.
func TestIAPRequireLiveRefusesSandbox(t *testing.T) {
	root, rootKey := genCA(t)
	leaf, leafKey := genLeaf(t, root, rootKey, time.Now().Add(time.Hour))
	appleRoot = root
	defer func() { appleRoot = nil }()
	t.Setenv("ROGERAI_REQUIRE_LIVE", "1")

	b := &broker{db: store.NewMem(), pubOfUser: map[string]string{}, seedFunds: 0}
	_, priv, _ := ed25519.GenerateKey(nil)
	jws := makeJWS(t, leafKey, []*x509.Certificate{leaf, root}, goodTxn(), "ES256") // Environment: Sandbox
	w := iapPost(t, b, priv, jws, "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("require-live + Sandbox status %d, want 400", w.Code)
	}
}

// --- Stage D: refund notification clawback ---

func notifPost(t *testing.T, b *broker, signedPayload string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"signedPayload": signedPayload})
	r := httptest.NewRequest(http.MethodPost, "/iap/notifications", bytes.NewReader(body))
	w := httptest.NewRecorder()
	b.iapNotifications(w, r)
	return w
}

// makeNotification builds a signed App Store Server Notification V2 (outer JWS) wrapping a signed
// transaction (inner JWS) for txn, both over the given chain.
func makeNotification(t *testing.T, leafKey *ecdsa.PrivateKey, chain []*x509.Certificate, noteType string, txn storeKitTxn) string {
	t.Helper()
	inner := makeJWS(t, leafKey, chain, txn, "ES256")
	payload := map[string]any{
		"notificationType": noteType,
		"notificationUUID": "uuid-1",
		"version":          "2.0",
		"data": map[string]any{
			"bundleId":              txn.BundleID,
			"environment":           txn.Environment,
			"signedTransactionInfo": inner,
		},
	}
	return makeJWS(t, leafKey, chain, payload, "ES256")
}

func TestIAPRefundNotification(t *testing.T) {
	root, rootKey := genCA(t)
	leaf, leafKey := genLeaf(t, root, rootKey, time.Now().Add(time.Hour))
	chain := []*x509.Certificate{leaf, root}
	appleRoot = root
	defer func() { appleRoot = nil }()

	mem := store.NewMem()
	b := &broker{db: mem, pubOfUser: map[string]string{}, seedFunds: 0}
	_, priv, _ := ed25519.GenerateKey(nil)

	// Credit $10 first so a wallet mapping exists to claw back from.
	txn := goodTxn()
	if w := iapPost(t, b, priv, makeJWS(t, leafKey, chain, txn, "ES256"), "fyi.rogerai.topup.10"); w.Code != http.StatusOK {
		t.Fatalf("setup credit failed: %d %s", w.Code, w.Body.String())
	}
	wallet, _, _, _ := mem.WalletByCharge("apple:txn-1")
	if bal, _ := mem.BalanceOf(wallet, 0); bal != 10 {
		t.Fatalf("pre-refund balance = %v, want 10", bal)
	}

	// A REFUND notification claws back the $10 once.
	note := makeNotification(t, leafKey, chain, "REFUND", txn)
	if w := notifPost(t, b, note); w.Code != http.StatusOK {
		t.Fatalf("refund status %d: %s", w.Code, w.Body.String())
	}
	if bal, _ := mem.BalanceOf(wallet, 0); bal != 0 {
		t.Errorf("balance after refund = %v, want 0 (clawed back)", bal)
	}

	// Redelivery (Apple retries) claws back ZERO - idempotent.
	if w := notifPost(t, b, note); w.Code != http.StatusOK {
		t.Errorf("refund redelivery status %d, want 200", w.Code)
	}
	if bal, _ := mem.BalanceOf(wallet, 0); bal != 0 {
		t.Errorf("balance after redelivery = %v, want 0 (no double clawback)", bal)
	}

	// A non-REFUND type is a 200 no-op (so Apple stops retrying).
	if w := notifPost(t, b, makeNotification(t, leafKey, chain, "DID_RENEW", txn)); w.Code != http.StatusOK {
		t.Errorf("non-refund notification status %d, want 200 no-op", w.Code)
	}

	// A refund for a transaction we never credited here is a 200 no-op.
	unk := txn
	unk.TransactionID = "txn-unknown"
	if w := notifPost(t, b, makeNotification(t, leafKey, chain, "REFUND", unk)); w.Code != http.StatusOK {
		t.Errorf("refund for unknown txn status %d, want 200 no-op", w.Code)
	}

	// An outer JWS that does not chain to the pinned root is rejected.
	other, otherKey := genCA(t)
	otherLeaf, otherLeafKey := genLeaf(t, other, otherKey, time.Now().Add(time.Hour))
	if w := notifPost(t, b, makeNotification(t, otherLeafKey, []*x509.Certificate{otherLeaf, other}, "REFUND", txn)); w.Code == http.StatusOK {
		t.Error("a notification not chaining to the pinned root must be rejected")
	}

	// Wrong bundleId is rejected.
	wrong := txn
	wrong.BundleID = "com.evil.app"
	if w := notifPost(t, b, makeNotification(t, leafKey, chain, "REFUND", wrong)); w.Code == http.StatusOK {
		t.Error("wrong bundleId notification must be rejected")
	}

	// A missing signedPayload is a 400.
	if w := notifPost(t, b, ""); w.Code != http.StatusBadRequest {
		t.Errorf("empty signedPayload status %d, want 400", w.Code)
	}
}

// TestIAPNotificationRequireLiveIgnoresSandbox: in require-live mode a Sandbox refund notification is
// acknowledged (200) but must NOT claw back real balance - the same fail-closed posture as the credit path.
func TestIAPNotificationRequireLiveIgnoresSandbox(t *testing.T) {
	root, rootKey := genCA(t)
	leaf, leafKey := genLeaf(t, root, rootKey, time.Now().Add(time.Hour))
	chain := []*x509.Certificate{leaf, root}
	appleRoot = root
	defer func() { appleRoot = nil }()
	t.Setenv("ROGERAI_REQUIRE_LIVE", "1")

	b := &broker{db: store.NewMem(), pubOfUser: map[string]string{}, seedFunds: 0}
	txn := goodTxn() // Environment: Sandbox
	if w := notifPost(t, b, makeNotification(t, leafKey, chain, "REFUND", txn)); w.Code != http.StatusOK {
		t.Errorf("sandbox refund in require-live status %d, want 200 no-op", w.Code)
	}
}
