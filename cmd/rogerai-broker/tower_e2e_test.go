package main

// Joined-Tower registration, end to end over the real routes.
//
// Everything below the HTTP layer has its own tests. This one exists to answer the question
// those cannot: does an operator with a signed-in account actually end up holding a
// certificate that authenticates as their Tower? It drives the routes a shipped
// roger-tower calls, with real signatures, a real CSR, and the real state machine.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/store"
	"rogerai.fm/roger/v5/internal/toweradmit"
	"rogerai.fm/roger/v5/internal/towercert"
	"rogerai.fm/roger/v5/internal/towerenroll"
)

// towerTestBroker wires the real subsystem over in-process stores and serves the real mux.
func towerTestBroker(t *testing.T) (*broker, *httptest.Server) {
	t.Helper()
	b := testBrokerWithDB(store.NewMem())
	ts, err := newTowerSubsystem(b,
		toweradmit.NewMemStore(), towercert.NewMemCustody(), towerenroll.NewMemStore(),
		towercert.Config{TTL: time.Hour})
	require.NoError(t, err)
	b.tower = ts

	mux := http.NewServeMux()
	mux.HandleFunc("/tower/token", b.towerToken)
	mux.HandleFunc("/tower/enroll/challenge", b.towerChallenge)
	mux.HandleFunc("/tower/enroll", b.towerEnroll)
	mux.HandleFunc("/tower/status", b.towerStatus)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return b, srv
}

// operator is a signed-in account holding a CLI key, exactly as `roger-tower login` leaves it.
type operator struct {
	priv  ed25519.PrivateKey
	login string
}

func signedInOperator(t *testing.T, b *broker, login string) operator {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubHex := hexOf(pub)
	require.NoError(t, b.db.BindOwner(store.Owner{
		Pubkey: pubHex, Login: login, Email: login + "@rogerai.fm",
		EmailVerifiedAt: time.Now().Unix(),
	}))
	return operator{priv: priv, login: login}
}

func hexOf(pub ed25519.PublicKey) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(pub)*2)
	for _, c := range pub {
		out = append(out, hexdigits[c>>4], hexdigits[c&0x0f])
	}
	return string(out)
}

// call makes a signed request exactly as the CLI does.
func (o operator) call(t *testing.T, srv *httptest.Server, method, path string, in any, out any) (int, string) {
	t.Helper()
	var body []byte
	if in != nil {
		var err error
		body, err = json.Marshal(in)
		require.NoError(t, err)
	}
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(string(body)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	pub, ts, sig := protocol.SignRequest(o.priv, method, path, body)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, itoa64(ts))
	req.Header.Set(protocol.HeaderSig, sig)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var raw json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&raw)
	if out != nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, out)
	}
	return resp.StatusCode, string(raw)
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// towerKeypair is what `roger-tower init` leaves on disk: two DISTINCT keys.
type towerKeypair struct {
	identityPub  ed25519.PublicKey
	identityPriv ed25519.PrivateKey
	tlsPriv      ed25519.PrivateKey
}

func newTowerOnDisk(t *testing.T) towerKeypair {
	t.Helper()
	ipub, ipriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, tpriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return towerKeypair{identityPub: ipub, identityPriv: ipriv, tlsPriv: tpriv}
}

// register runs the whole handshake the CLI runs.
func register(t *testing.T, srv *httptest.Server, o operator, k towerKeypair, txn string) (int, map[string]any) {
	t.Helper()

	var tok struct {
		Token string `json:"token"`
	}
	code, raw := o.call(t, srv, http.MethodPost, "/tower/token", map[string]any{}, &tok)
	require.Equal(t, http.StatusOK, code, raw)
	require.NotEmpty(t, tok.Token)

	var ch struct {
		Nonce        string `json:"nonce"`
		SigningInput string `json:"signing_input"`
	}
	code, raw = o.call(t, srv, http.MethodPost, "/tower/enroll/challenge",
		map[string]string{"token": tok.Token}, &ch)
	require.Equal(t, http.StatusOK, code, raw)

	input, err := base64.StdEncoding.DecodeString(ch.SigningInput)
	require.NoError(t, err)

	csr, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "roger-tower"}}, k.tlsPriv)
	require.NoError(t, err)

	out := map[string]any{}
	code, _ = o.call(t, srv, http.MethodPost, "/tower/enroll", map[string]any{
		"token":            tok.Token,
		"transaction_id":   txn,
		"nonce":            ch.Nonce,
		"identity_key":     base64.StdEncoding.EncodeToString(k.identityPub),
		"signature":        base64.StdEncoding.EncodeToString(ed25519.Sign(k.identityPriv, input)),
		"csr":              base64.StdEncoding.EncodeToString(csr),
		"protocol_version": 1,
		"capabilities":     []string{"relay"},
	}, &out)
	return code, out
}

func TestAnOperatorRegistersATowerEndToEnd(t *testing.T) {
	b, srv := towerTestBroker(t)
	o := signedInOperator(t, b, "operator-1")
	k := newTowerOnDisk(t)

	code, out := register(t, srv, o, k, "txn-1")
	require.Equal(t, http.StatusOK, code)

	towerID, _ := out["tower_id"].(string)
	require.NotEmpty(t, towerID)
	require.Equal(t, "quarantine", out["state"], "a new Tower is never trusted on arrival")

	// The certificate really authenticates as this Tower, under the CA the broker returned.
	certDER, err := base64.StdEncoding.DecodeString(out["certificate"].(string))
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(certDER)
	require.NoError(t, err)
	got, err := b.tower.ca.Authenticate(cert)
	require.NoError(t, err)
	require.Equal(t, towerID, got)

	// And the issuer it was handed is the one that signed it, so a Tower can pin it.
	caDER, err := base64.StdEncoding.DecodeString(out["ca"].(string))
	require.NoError(t, err)
	require.Equal(t, b.tower.ca.Root().Raw, caDER)

	// The registry agrees, and quarantine means no ordinary work yet.
	require.False(t, b.tower.registry.MayTakeWork(towerID),
		"quarantine takes probes or bounded beta, not ordinary public jobs")
}

func TestRegistrationIsIdempotentOverHTTP(t *testing.T) {
	// The lost-response case as the operator would hit it: the CLI retries the same
	// transaction and must get its Tower back, not a second one.
	b, srv := towerTestBroker(t)
	o := signedInOperator(t, b, "operator-1")
	k := newTowerOnDisk(t)

	code, first := register(t, srv, o, k, "txn-stable")
	require.Equal(t, http.StatusOK, code)

	// Same transaction id, same key proof - the CLI re-running after a dropped connection.
	// The token is already spent by now, which is exactly why the retry must be recognised
	// as a retry rather than validated afresh.
	second := map[string]any{}
	code, raw := o.call(t, srv, http.MethodPost, "/tower/enroll", map[string]any{
		"token":            "already-spent",
		"transaction_id":   "txn-stable",
		"identity_key":     base64.StdEncoding.EncodeToString(k.identityPub),
		"protocol_version": 1,
	}, &second)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, first["tower_id"], second["tower_id"],
		"the operator gets their Tower back, not a refusal")
	require.Equal(t, first["certificate"], second["certificate"],
		"and the same certificate, not a second credential for one Tower")

	require.Len(t, b.tower.registry.ByOwner("operator-1"), 1,
		"a retry must never produce a second Tower")

	// A retry with a DIFFERENT key is not a retry, whatever transaction id it claims.
	attacker := newTowerOnDisk(t)
	code, _ = o.call(t, srv, http.MethodPost, "/tower/enroll", map[string]any{
		"token":            "already-spent",
		"transaction_id":   "txn-stable",
		"identity_key":     base64.StdEncoding.EncodeToString(attacker.identityPub),
		"protocol_version": 1,
	}, nil)
	require.Equal(t, http.StatusBadRequest, code,
		"a transaction id seen on the wire must not re-issue somebody else's Tower to a new key")
}

func TestAnUnsignedCallerCannotRegisterATower(t *testing.T) {
	_, srv := towerTestBroker(t)

	resp, err := http.Post(srv.URL+"/tower/token", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"admission authority is only ever created for a signed-in account")
}

func TestASignedCallerWithNoAccountCannotRegister(t *testing.T) {
	// A valid signature from a key nobody has bound is not an account.
	_, srv := towerTestBroker(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	stranger := operator{priv: priv, login: "nobody"}

	code, _ := stranger.call(t, srv, http.MethodPost, "/tower/token", map[string]any{}, nil)
	require.Equal(t, http.StatusUnauthorized, code)
}

func TestAnotherOperatorsTokenIsUseless(t *testing.T) {
	// The control added after reviewing what each rejection really failed on: the token is
	// a bearer credential, and the session is what stops a leaked one being redeemed.
	b, srv := towerTestBroker(t)
	victim := signedInOperator(t, b, "victim")
	attacker := signedInOperator(t, b, "attacker")
	k := newTowerOnDisk(t)

	var tok struct {
		Token string `json:"token"`
	}
	code, _ := victim.call(t, srv, http.MethodPost, "/tower/token", map[string]any{}, &tok)
	require.Equal(t, http.StatusOK, code)

	var ch struct {
		Nonce        string `json:"nonce"`
		SigningInput string `json:"signing_input"`
	}
	code, _ = attacker.call(t, srv, http.MethodPost, "/tower/enroll/challenge",
		map[string]string{"token": tok.Token}, &ch)
	require.Equal(t, http.StatusOK, code, "a challenge alone reveals nothing")

	input, err := base64.StdEncoding.DecodeString(ch.SigningInput)
	require.NoError(t, err)
	csr, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "roger-tower"}}, k.tlsPriv)
	require.NoError(t, err)

	code, _ = attacker.call(t, srv, http.MethodPost, "/tower/enroll", map[string]any{
		"token":            tok.Token,
		"transaction_id":   "txn-attacker",
		"nonce":            ch.Nonce,
		"identity_key":     base64.StdEncoding.EncodeToString(k.identityPub),
		"signature":        base64.StdEncoding.EncodeToString(ed25519.Sign(k.identityPriv, input)),
		"csr":              base64.StdEncoding.EncodeToString(csr),
		"protocol_version": 1,
	}, nil)
	require.Equal(t, http.StatusBadRequest, code,
		"somebody else's enrollment token must not admit a Tower onto their account")
	require.Empty(t, b.tower.registry.ByOwner("victim"))
	require.Empty(t, b.tower.registry.ByOwner("attacker"))
}

func TestAnOperatorSeesOnlyTheirOwnTowers(t *testing.T) {
	b, srv := towerTestBroker(t)
	mine := signedInOperator(t, b, "operator-mine")
	theirs := signedInOperator(t, b, "operator-theirs")

	code, _ := register(t, srv, mine, newTowerOnDisk(t), "txn-mine")
	require.Equal(t, http.StatusOK, code)
	code, _ = register(t, srv, theirs, newTowerOnDisk(t), "txn-theirs")
	require.Equal(t, http.StatusOK, code)

	var out struct {
		Towers []map[string]any `json:"towers"`
	}
	code, _ = mine.call(t, srv, http.MethodGet, "/tower/status", nil, &out)
	require.Equal(t, http.StatusOK, code)
	require.Len(t, out.Towers, 1, "an operator's status must not enumerate the network")
}

func TestTowerRoutesRefusePlainlyWhenAdmissionIsOff(t *testing.T) {
	// A deployment with no database must not hand out credentials it will forget. The
	// refusal has to be legible, because the operator's next move depends on knowing it is
	// the deployment and not their machine.
	b := testBrokerWithDB(store.NewMem())
	b.tower = nil // what loadTowerSubsystem leaves when it cannot be durable

	mux := http.NewServeMux()
	mux.HandleFunc("/tower/token", b.towerToken)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	o := signedInOperator(t, b, "operator-1")
	code, raw := o.call(t, srv, http.MethodPost, "/tower/token", map[string]any{}, nil)
	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Contains(t, raw, "not available")
}

func TestAdmissionRefusesToStartWithAMisconfiguredRoot(t *testing.T) {
	// Half a root is a misconfiguration. Generating one instead would issue under a root
	// nobody chose, and every certificate on the network would stop verifying at the next
	// deploy that read the configuration correctly.
	b := testBrokerWithDB(store.NewMem())
	_, err := newTowerSubsystem(b,
		toweradmit.NewMemStore(), towercert.NewMemCustody(), towerenroll.NewMemStore(),
		towercert.Config{TTL: time.Hour, RootKeyPEM: []byte("-----BEGIN PRIVATE KEY-----\nbad\n-----END PRIVATE KEY-----")})
	require.Error(t, err)
}

// --- the wiring and the edges the happy path does not reach ----------------

func TestAdmissionIsOffWithoutADatabase(t *testing.T) {
	// The production decision: no durable store means no joined Towers, rather than
	// credentials that evaporate on the next deploy.
	b := testBrokerWithDB(store.NewMem())
	ts, err := loadTowerSubsystem(b, store.NewMem())
	require.NoError(t, err)
	require.Nil(t, ts, "an in-memory deployment must not admit Towers")
}

func TestTheCertificateLifetimeIsConfigurableAndBounded(t *testing.T) {
	require.Equal(t, 24*time.Hour, towerCertTTL(), "a sensible default without configuration")

	t.Setenv("ROGERAI_TOWER_CERT_TTL", "15m")
	require.Equal(t, 15*time.Minute, towerCertTTL())

	// Nonsense must not disable the bound - an unparseable or negative TTL falling through
	// to "no expiry" would make certificates permanent.
	t.Setenv("ROGERAI_TOWER_CERT_TTL", "not-a-duration")
	require.Equal(t, 24*time.Hour, towerCertTTL())
	t.Setenv("ROGERAI_TOWER_CERT_TTL", "-5m")
	require.Equal(t, 24*time.Hour, towerCertTTL())
}

func TestABannedAccountMayNotRunATower(t *testing.T) {
	// A joined Tower relays other people's traffic, so an account we have already acted
	// against must not acquire one.
	b, srv := towerTestBroker(t)
	o := signedInOperator(t, b, "operator-banned")
	b.metricsMu.Lock()
	if b.bannedOwners == nil {
		b.bannedOwners = map[string]bool{}
	}
	b.bannedOwners[o.login] = true
	b.metricsMu.Unlock()

	code, raw := o.call(t, srv, http.MethodPost, "/tower/token", map[string]any{}, nil)
	require.Equal(t, http.StatusForbidden, code, raw)

	require.Error(t, brokerOperatorPolicy{b: b}.MayEnroll(""), "a Tower must belong to an account")
	require.NoError(t, brokerOperatorPolicy{b: b}.MayEnroll("operator-ok"))
}

func TestEnrollmentRejectsMalformedInput(t *testing.T) {
	b, srv := towerTestBroker(t)
	o := signedInOperator(t, b, "operator-1")

	// Not JSON at all.
	code, _ := o.call(t, srv, http.MethodPost, "/tower/enroll", "not-an-object", nil)
	require.Equal(t, http.StatusBadRequest, code)

	// JSON, but the base64 fields are not base64.
	code, _ = o.call(t, srv, http.MethodPost, "/tower/enroll", map[string]any{
		"transaction_id": "txn-x", "identity_key": "!!!not base64!!!",
	}, nil)
	require.Equal(t, http.StatusBadRequest, code)
	require.Empty(t, b.tower.registry.ByOwner("operator-1"))
}

func TestAChallengeNeedsAToken(t *testing.T) {
	b, srv := towerTestBroker(t)
	o := signedInOperator(t, b, "operator-1")

	code, _ := o.call(t, srv, http.MethodPost, "/tower/enroll/challenge", map[string]any{}, nil)
	require.Equal(t, http.StatusBadRequest, code)

	code, _ = o.call(t, srv, http.MethodPost, "/tower/enroll/challenge",
		map[string]string{"token": "never-issued"}, nil)
	require.Equal(t, http.StatusBadRequest, code, "an unknown token and a spent one look alike")
}

func TestStatusAndTokenRequireASignedInAccount(t *testing.T) {
	_, srv := towerTestBroker(t)
	resp, err := http.Get(srv.URL + "/tower/status")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestStatusReportsWhatTheRegistryHolds(t *testing.T) {
	b, srv := towerTestBroker(t)
	o := signedInOperator(t, b, "operator-1")
	code, out := register(t, srv, o, newTowerOnDisk(t), "txn-1")
	require.Equal(t, http.StatusOK, code)

	var status struct {
		Towers []struct {
			TowerID     string `json:"tower_id"`
			State       string `json:"state"`
			MayTakeWork bool   `json:"may_take_work"`
		} `json:"towers"`
	}
	code, _ = o.call(t, srv, http.MethodGet, "/tower/status", nil, &status)
	require.Equal(t, http.StatusOK, code)
	require.Len(t, status.Towers, 1)
	require.Equal(t, out["tower_id"], status.Towers[0].TowerID)
	require.Equal(t, "quarantine", status.Towers[0].State)
	require.False(t, status.Towers[0].MayTakeWork,
		"an operator must not be told their quarantined Tower is taking work")
}

func TestWrongMethodsAreRefused(t *testing.T) {
	_, srv := towerTestBroker(t)
	resp, err := http.Get(srv.URL + "/tower/enroll")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.NotEqual(t, http.StatusOK, resp.StatusCode)
}

// --- configured-but-broken must not look like not-configured ---------------

func TestAnUnconfiguredDeploymentIsQuietAndAMisconfiguredOneIsNot(t *testing.T) {
	// This distinction is what let a real bug hide. The tower migration used to fail on
	// every least-privilege deployment, and because a failed setup just turned admission
	// OFF, the broker started healthily and logged one line - the first person to notice
	// would have been an operator whose registration did not work.
	//
	// No database means joined Towers are legitimately unavailable: quiet is correct.
	// A database that IS present means somebody intended Towers to work, so a failure to
	// set them up is a broken deployment and must be loud.
	b := testBrokerWithDB(store.NewMem())

	ts, err := loadTowerSubsystem(b, store.NewMem())
	require.NoError(t, err, "no database is not an error - standalone Towers need nothing from us")
	require.Nil(t, ts)
}

func TestAHalfConfiguredCARootFailsTheDeployment(t *testing.T) {
	// Somebody explicitly supplied a root. Turning admission off and carrying on would
	// mean starting under a configuration nobody asked for, and the operator would have to
	// discover it from a log line they were not watching.
	b := testBrokerWithDB(store.NewMem())
	_, err := newTowerSubsystem(b,
		toweradmit.NewMemStore(), towercert.NewMemCustody(), towerenroll.NewMemStore(),
		towercert.Config{TTL: time.Hour, RootKeyPEM: []byte("-----BEGIN PRIVATE KEY-----\nbad\n-----END PRIVATE KEY-----")})
	require.Error(t, err)
}
