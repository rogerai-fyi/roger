package main

// iap.go: StoreKit 2 In-App Purchase top-ups (Apple Guideline 3.1.1). The iOS app buys a consumable
// and POSTs the signed StoreKit transaction (a JWS) here; the broker VERIFIES it server-side (the
// client's word is never trusted) and credits the wallet through the SAME idempotent primitive Stripe
// uses (CreditOnce -> KindTopup), so balance/history/-me are identical to a card top-up. Pricing is
// round + 1:1 (a $10 product credits $10; RogerAI absorbs Apple's cut - see the productId map).
//
// Security posture mirrors the Stripe path and verifyAppleIdentityToken: credits derive from the
// SIGNED transaction, never from client metadata; the JWS is checked to a PINNED Apple root; only
// ES256 is accepted (alg-confusion defense); idempotent on Apple's transactionId so the purchase POST
// and the app's Transaction.updates re-delivery can both fire without double-crediting.

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"
)

// iapProducts maps a StoreKit consumable product id to the USD face value credited 1:1 (founder:
// round price points, no markup - RogerAI eats Apple's ~30%). The credited amount is server-
// authoritative; the client-sent product_id is advisory and never trusted for the amount.
var iapProducts = map[string]float64{
	"fyi.rogerai.topup.5":  5,
	"fyi.rogerai.topup.10": 10,
	"fyi.rogerai.topup.20": 20,
	"fyi.rogerai.topup.50": 50,
}

// iapBundleID is the only app whose transactions we credit.
const iapBundleID = "fyi.rogerai.app"

// appleRootG3PEM is Apple Root CA - G3, the trust anchor for StoreKit signed transactions. Pinned from
// https://www.apple.com/certificateauthority/ ("Apple Root CA - G3", self-signed, valid 2014-2039);
// verified SHA-256 fingerprint 63:34:3A:BF:B8:9A:6A:03:EB:B5:7E:9B:3F:5F:A7:BE:7C:4F:5C:75:6F:30:17:B3:
// A8:C4:88:C3:65:3E:91:79. ROGERAI_APPLE_ROOT_PEM overrides it at runtime; if neither parses,
// /iap/credit returns 503 (fail-closed, like unconfigured Stripe) so a misconfigured broker never credits
// on an unverifiable transaction.
const appleRootG3PEM = `-----BEGIN CERTIFICATE-----
MIICQzCCAcmgAwIBAgIILcX8iNLFS5UwCgYIKoZIzj0EAwMwZzEbMBkGA1UEAwwS
QXBwbGUgUm9vdCBDQSAtIEczMSYwJAYDVQQLDB1BcHBsZSBDZXJ0aWZpY2F0aW9u
IEF1dGhvcml0eTETMBEGA1UECgwKQXBwbGUgSW5jLjELMAkGA1UEBhMCVVMwHhcN
MTQwNDMwMTgxOTA2WhcNMzkwNDMwMTgxOTA2WjBnMRswGQYDVQQDDBJBcHBsZSBS
b290IENBIC0gRzMxJjAkBgNVBAsMHUFwcGxlIENlcnRpZmljYXRpb24gQXV0aG9y
aXR5MRMwEQYDVQQKDApBcHBsZSBJbmMuMQswCQYDVQQGEwJVUzB2MBAGByqGSM49
AgEGBSuBBAAiA2IABJjpLz1AcqTtkyJygRMc3RCV8cWjTnHcFBbZDuWmBSp3ZHtf
TjjTuxxEtX/1H7YyYl3J6YRbTzBPEVoA/VhYDKX1DyxNB0cTddqXl5dvMVztK517
IDvYuVTZXpmkOlEKMaNCMEAwHQYDVR0OBBYEFLuw3qFYM4iapIqZ3r6966/ayySr
MA8GA1UdEwEB/wQFMAMBAf8wDgYDVR0PAQH/BAQDAgEGMAoGCCqGSM49BAMDA2gA
MGUCMQCD6cHEFl4aXTQY2e3v9GwOAEZLuN+yRhHFD/3meoyhpmvOwgPUnPWTxnS4
at+qIxUCMG1mihDK1A3UT82NQz60imOlM27jbdoXt2QfyFMm+YhidDkLF1vLUagM
6BgD56KyKA==
-----END CERTIFICATE-----`

// appleRoot is the parsed trust anchor. nil => /iap/credit is 503. Set by loadAppleRoot() at startup;
// tests set it directly to a test root so the verifier can be exercised with a generated chain.
var appleRoot *x509.Certificate

// loadAppleRoot parses the Apple root from ROGERAI_APPLE_ROOT_PEM (preferred) or the embedded const.
func loadAppleRoot() {
	pemStr := strings.TrimSpace(os.Getenv("ROGERAI_APPLE_ROOT_PEM"))
	if pemStr == "" {
		pemStr = strings.TrimSpace(appleRootG3PEM)
	}
	if pemStr == "" {
		log.Printf("iap: no Apple root configured (set ROGERAI_APPLE_ROOT_PEM) - /iap/credit disabled")
		return
	}
	if c := parseCertPEM(pemStr); c != nil {
		appleRoot = c
		log.Printf("iap: StoreKit top-ups enabled (Apple root loaded)")
	} else {
		log.Printf("iap: Apple root PEM invalid - /iap/credit disabled")
	}
}

// parseCertPEM decodes one PEM CERTIFICATE block into an *x509.Certificate (nil on failure).
func parseCertPEM(s string) *x509.Certificate {
	blk, _ := pem.Decode([]byte(s))
	if blk == nil {
		return nil
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil
	}
	return c
}

// storeKitTxn is the subset of a StoreKit 2 JWSTransaction payload we enforce/use.
type storeKitTxn struct {
	BundleID              string `json:"bundleId"`
	ProductID             string `json:"productId"`
	TransactionID         string `json:"transactionId"`
	OriginalTransactionID string `json:"originalTransactionId"`
	Type                  string `json:"type"`
	Environment           string `json:"environment"` // "Sandbox" | "Production"
}

// verifyJWSPayload verifies an Apple JWS and returns its raw decoded payload bytes. It enforces: ES256
// only (alg-confusion defense); an x5c cert chain that verifies to `root` (the pinned Apple root) with
// valid dates at `now`; and an ECDSA signature over the signing input made by the leaf key. It is the
// crypto SHARED by a StoreKit signed transaction and an App Store Server Notification V2 (the same
// signed-JWS envelope wraps different payload shapes). ok=false on any failure (the caller maps that to
// one opaque status - never leak which check failed).
func verifyJWSPayload(jws string, root *x509.Certificate, now time.Time) ([]byte, bool) {
	if root == nil {
		return nil, false
	}
	parts := strings.Split(jws, ".")
	if len(parts) != 3 {
		return nil, false
	}
	hdrJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, false
	}
	var hdr struct {
		Alg string   `json:"alg"`
		X5c []string `json:"x5c"`
	}
	if json.Unmarshal(hdrJSON, &hdr) != nil {
		return nil, false
	}
	if hdr.Alg != "ES256" { // reject none/HS*/RS*/ES384 - alg-confusion / key-substitution defense
		return nil, false
	}
	if len(hdr.X5c) == 0 {
		return nil, false
	}
	// x5c entries are STANDARD-base64 DER certs, leaf first.
	var certs []*x509.Certificate
	for _, c := range hdr.X5c {
		der, err := base64.StdEncoding.DecodeString(c)
		if err != nil {
			return nil, false
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, false
		}
		certs = append(certs, cert)
	}
	leaf := certs[0]
	roots := x509.NewCertPool()
	roots.AddCert(root)
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	// Chain to the pinned root with valid dates. ExtKeyUsageAny: Apple's transaction-signing leaf is not
	// a TLS server cert, so we must not require ServerAuth (the default) - date + chain to the pinned
	// root is the trust we need.
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: inter,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return nil, false
	}
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, false
	}
	// JWS ES256 signature is raw r||s (64 bytes), not ASN.1 DER.
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return nil, false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, sum[:], r, s) {
		return nil, false
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	return payloadJSON, true
}

// verifyStoreKitJWS validates a StoreKit signed transaction JWS and returns its decoded payload.
func verifyStoreKitJWS(jws string, root *x509.Certificate, now time.Time) (storeKitTxn, bool) {
	payload, ok := verifyJWSPayload(jws, root, now)
	if !ok {
		return storeKitTxn{}, false
	}
	var txn storeKitTxn
	if json.Unmarshal(payload, &txn) != nil {
		return storeKitTxn{}, false
	}
	return txn, true
}

// iapWallet resolves the wallet an IAP credit lands on, requiring a web session OR a VERIFIED owner
// signature (authed). Unlike checkoutWallet it rejects an unsigned request - an IAP credit with no
// identified wallet must not succeed. A signed anon device key authes to its own pubkey wallet.
func (b *broker) iapWallet(r *http.Request, body []byte) (string, bool) {
	if _, sw, sok := b.webSession(r); sok {
		return sw, true
	}
	if u, authed, iok := b.identityOf(r, body); iok && authed {
		return b.walletOf(r, u), true
	}
	return "", false
}

// iapCredit handles POST /iap/credit: an owner-signed request carrying a StoreKit JWS transaction.
// It verifies the JWS to the pinned Apple root, maps the product to a USD face value, and credits the
// signed request's wallet ONCE (idempotent on Apple's transactionId). Response: {credited, balance, usd}.
func (b *broker) iapCredit(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	if !allow(w, r, http.MethodPost) {
		return
	}
	if appleRoot == nil {
		jsonErr(w, http.StatusServiceUnavailable, "iap not configured")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	// Resolve the wallet to credit. Unlike the Stripe checkout (which tolerates an unsigned anon
	// request because the Stripe session ties the payment to a wallet), an IAP credit MUST identify a
	// wallet up front - the JWS proves a payment happened but not whose it is. So we require a web
	// session OR a VERIFIED owner signature (authed); an unsigned request has no wallet and is refused.
	// A signed anon device key still authes (authed=true, its own pubkey wallet - claimable on login).
	user, ok := b.iapWallet(r, body)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "invalid request signature")
		return
	}
	var req struct {
		JWS       string `json:"jws"`
		ProductID string `json:"product_id"`
	}
	if json.Unmarshal(body, &req) != nil || req.JWS == "" {
		jsonErr(w, http.StatusBadRequest, "missing jws")
		return
	}
	txn, ok := verifyStoreKitJWS(req.JWS, appleRoot, time.Now())
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "invalid transaction")
		return
	}
	if txn.BundleID != iapBundleID {
		jsonErr(w, http.StatusBadRequest, "wrong app")
		return
	}
	if txn.Type != "Consumable" {
		jsonErr(w, http.StatusBadRequest, "unsupported product type")
		return
	}
	// Environment gate (mirrors the sk_live fail-closed gate): in require-live mode a Sandbox
	// transaction must never credit real balance.
	if requireLive() && txn.Environment != "Production" {
		jsonErr(w, http.StatusBadRequest, "sandbox transaction refused in production")
		return
	}
	// The JWS is truth; a client product_id that disagrees is logged and ignored (like the Stripe
	// metadata-vs-amount_total divergence check).
	if req.ProductID != "" && req.ProductID != txn.ProductID {
		log.Printf("iap: client product_id %q diverges from JWS %q - using the JWS", req.ProductID, txn.ProductID)
	}
	usd, ok := iapProducts[txn.ProductID]
	if !ok {
		jsonErr(w, http.StatusBadRequest, "unknown product")
		return
	}
	creditUSD := b.bill.creditUSD
	if creditUSD <= 0 {
		creditUSD = 1 // IAP does not require Stripe to be configured; 1 credit = $1 default
	}
	credits := usd / creditUSD

	// Atomic credit-once: idempotent on Apple's transactionId, so the purchase POST + the app's
	// Transaction.updates re-delivery collapse to a single KindTopup row (never double-credits).
	credited, newBal, err := b.db.CreditOnce("apple:"+txn.TransactionID, user, credits)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	if credited {
		log.Printf("iap: credited %s +%.4f -> %.4f (txn %s)", user, credits, newBal, txn.TransactionID)
	} else {
		log.Printf("iap: duplicate txn %s ignored", txn.TransactionID)
	}
	// Persist the Apple transaction -> wallet mapping so a later refund notification (Stage D, which
	// carries no signed request) can resolve the wallet. Reuses the SAME charge-map machinery Stripe
	// disputes use (WalletByCharge), keyed on the Apple transaction ids - no new store surface.
	if err := b.db.LinkCharge("apple-txn-"+txn.TransactionID, "apple:"+txn.TransactionID, "apple:"+txn.OriginalTransactionID, user, credits); err != nil {
		log.Printf("iap: LinkCharge(txn %s) failed: %v (refund clawback may not resolve this txn)", txn.TransactionID, err)
	}

	bal, _ := b.db.BalanceOf(user, b.seedFunds)
	writeJSON(w, http.StatusOK, map[string]any{"credited": credited, "balance": bal, "usd": usd})
}

// appStoreNotificationV2 is the subset of an App Store Server Notification V2 decoded payload we act on.
type appStoreNotificationV2 struct {
	NotificationType string `json:"notificationType"`
	Subtype          string `json:"subtype"`
	NotificationUUID string `json:"notificationUUID"`
	Data             struct {
		BundleID              string `json:"bundleId"`
		Environment           string `json:"environment"`
		SignedTransactionInfo string `json:"signedTransactionInfo"`
	} `json:"data"`
}

// iapNotifications handles POST /iap/notifications: Apple's App Store Server Notifications V2. Apple
// (not a signed client) POSTs {"signedPayload": <JWS>}; the JWS is verified to the SAME pinned Apple
// root as a credit. We act ONLY on REFUND - clawing back the credited amount through the SAME lineage
// engine a Stripe refund uses (KindRefund + operator paid-lot reversal + platform-loss), idempotent so
// an Apple redelivery claws back zero the second time. Every other notification type, a sandbox
// notification in prod, and a refund for a transaction we never credited here are all acknowledged with
// 200 so Apple stops retrying. Only an unverifiable / garbage payload is a 4xx.
func (b *broker) iapNotifications(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	if appleRoot == nil {
		jsonErr(w, http.StatusServiceUnavailable, "iap not configured")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var envlp struct {
		SignedPayload string `json:"signedPayload"`
	}
	if json.Unmarshal(body, &envlp) != nil || envlp.SignedPayload == "" {
		jsonErr(w, http.StatusBadRequest, "missing signedPayload")
		return
	}
	now := time.Now()
	payload, ok := verifyJWSPayload(envlp.SignedPayload, appleRoot, now)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "invalid notification signature")
		return
	}
	var note appStoreNotificationV2
	if json.Unmarshal(payload, &note) != nil {
		jsonErr(w, http.StatusBadRequest, "bad notification payload")
		return
	}
	if note.Data.BundleID != iapBundleID {
		jsonErr(w, http.StatusBadRequest, "wrong app")
		return
	}
	// Sandbox notification in require-live mode: acknowledge (so Apple stops retrying) but never touch
	// real balance. Sandbox uses a separate notification URL anyway.
	if requireLive() && note.Data.Environment != "Production" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ignored": "sandbox"})
		return
	}
	// Only REFUND claws back; every other type is acknowledged without acting.
	if note.NotificationType != "REFUND" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ignored": note.NotificationType})
		return
	}
	// Verify + decode the inner signed transaction to learn WHICH transaction was refunded.
	txn, ok := verifyStoreKitJWS(note.Data.SignedTransactionInfo, appleRoot, now)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "invalid transaction info")
		return
	}
	// Resolve the wallet + the exact amount we credited for this transaction (persisted by iapCredit's
	// LinkCharge). Unknown => a refund for a transaction we never credited here; acknowledge and no-op.
	chargeRef := "apple:" + txn.TransactionID
	wallet, credits, known, err := b.db.WalletByCharge(chargeRef)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	if !known || wallet == "" {
		log.Printf("iap: REFUND for unknown txn %s - no-op", txn.TransactionID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ignored": "unknown transaction"})
		return
	}
	// Claw back through the SAME lineage engine a Stripe refund uses, idempotent in its own namespace
	// ("applerefund:") so an Apple redelivery is a no-op. refundAmount is the exact amount credited, so
	// the clawback can never exceed it regardless of the charge cap.
	refundID := "applerefund:" + txn.TransactionID
	chargeRefs := []string{chargeRef, "apple:" + txn.OriginalTransactionID}
	res, eff, err := b.db.RefundLineage(refundID, chargeRefs, wallet, "apple-refund-"+txn.TransactionID, credits, now)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "clawback error")
		return
	}
	if res.AlreadyHandled {
		log.Printf("iap: REFUND redelivery for txn %s ignored (already clawed back)", txn.TransactionID)
	} else {
		log.Printf("iap: REFUND txn %s clawed back %.4f from %s", txn.TransactionID, eff, wallet)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "clawed_back": eff})
}
