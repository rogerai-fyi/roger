package towerjoin

// The Tower's side of the admission handshake, against a stub Roger Core.
//
// The broker's own tests prove the server half. These prove the half that runs on an
// operator's machine: that it presents the right proofs, stores what it was issued, and -
// the one that matters after a bad night - retries an interrupted enrollment as the SAME
// enrollment rather than asking for a second Tower.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stubCore is a Roger Core that admits anything well-formed, and records what it was sent
// so a test can assert on the proofs rather than only on the outcome.
type stubCore struct {
	mu sync.Mutex

	caKey  ed25519.PrivateKey
	caCert *x509.Certificate

	seenTxn      []string
	seenIdentity []byte
	seenNonce    string
	enrollments  int

	// issued is returned again for a repeated transaction, as the real one does.
	issued map[string][]byte

	failEnroll   int  // fail this many enroll calls before succeeding
	badCertBytes bool // return something that is not a certificate
}

func newStubCore(t *testing.T) (*stubCore, *httptest.Server) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "stub CA"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	c := &stubCore{caKey: priv, caCert: caCert, issued: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/tower/token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"token": "tok-1", "expires_in": 3600})
	})
	mux.HandleFunc("/tower/enroll/challenge", func(w http.ResponseWriter, r *http.Request) {
		input := []byte("rogerai-tower-enroll-v1\x00tok-1\x00nonce-1")
		writeJSON(w, map[string]any{
			"nonce":         "nonce-1",
			"signing_input": base64.StdEncoding.EncodeToString(input),
		})
	})
	mux.HandleFunc("/tower/enroll", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		var req struct {
			TransactionID string `json:"transaction_id"`
			Nonce         string `json:"nonce"`
			IdentityKey   string `json:"identity_key"`
			Signature     string `json:"signature"`
			CSR           string `json:"csr"`
			Version       int    `json:"protocol_version"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		if c.failEnroll > 0 {
			c.failEnroll--
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "temporarily unavailable"})
			return
		}

		c.seenTxn = append(c.seenTxn, req.TransactionID)
		c.seenNonce = req.Nonce
		c.seenIdentity, _ = base64.StdEncoding.DecodeString(req.IdentityKey)

		// The identity signature must verify against the challenge we handed out.
		sig, _ := base64.StdEncoding.DecodeString(req.Signature)
		input := []byte("rogerai-tower-enroll-v1\x00tok-1\x00nonce-1")
		if !ed25519.Verify(c.seenIdentity, input, sig) {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad challenge signature"})
			return
		}
		// The CSR must be signed by the key it carries, and that key must NOT be the
		// identity key.
		csrDER, _ := base64.StdEncoding.DecodeString(req.CSR)
		csr, err := x509.ParseCertificateRequest(csrDER)
		if err != nil || csr.CheckSignature() != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad csr"})
			return
		}
		if other, ok := csr.PublicKey.(ed25519.PublicKey); ok && other.Equal(ed25519.PublicKey(c.seenIdentity)) {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "channel key must differ"})
			return
		}

		leafDER, ok := c.issued[req.TransactionID]
		if !ok {
			c.enrollments++
			leaf := &x509.Certificate{
				SerialNumber: big.NewInt(int64(100 + c.enrollments)),
				Subject:      pkix.Name{CommonName: "tw-stub"},
				NotBefore:    time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
			}
			leafDER, _ = x509.CreateCertificate(rand.Reader, leaf, c.caCert, csr.PublicKey, c.caKey)
			c.issued[req.TransactionID] = leafDER
		}
		if c.badCertBytes {
			leafDER = []byte("not a certificate")
		}
		writeJSON(w, map[string]any{
			"tower_id":      "tw-stub",
			"certificate":   base64.StdEncoding.EncodeToString(leafDER),
			"ca":            base64.StdEncoding.EncodeToString(c.caCert.Raw),
			"state":         "quarantine",
			"lease_expires": time.Now().Add(24 * time.Hour).Unix(),
			"not_after":     time.Now().Add(time.Hour).Unix(),
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return c, srv
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func enrollHarness(t *testing.T) (*stubCore, *httptest.Server) {
	t.Helper()
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	c, srv := newStubCore(t)
	t.Setenv("ROGER_BROKER", srv.URL)
	return c, srv
}

func TestRegisterCompletesAndStoresWhatItWasIssued(t *testing.T) {
	c, _ := enrollHarness(t)
	st := joinedTower(t)

	require.NoError(t, Register(st, Account{Login: "alice"}))

	// The certificate and its issuer are on disk and readable.
	certDER, err := os.ReadFile(filepath.Join(st.Dir(), certFile))
	require.NoError(t, err)
	_, err = x509.ParseCertificate(certDER)
	require.NoError(t, err)

	caDER, err := os.ReadFile(filepath.Join(st.Dir(), caFile))
	require.NoError(t, err)
	require.Equal(t, c.caCert.Raw, caDER, "the Tower keeps the issuer so it can pin it")

	adm, ok := LoadAdmission(st.Dir())
	require.True(t, ok)
	require.Equal(t, "tw-stub", adm.TowerID)
	require.Equal(t, "quarantine", adm.State, "a new Tower is not trusted on arrival")
	require.NotEmpty(t, adm.TransactionID)
}

func TestRegisterProvesTheIdentityKeyAndASeparateChannelKey(t *testing.T) {
	// The stub refuses a bad challenge signature, a bad CSR, and a CSR reusing the identity
	// key - so reaching success at all is the proof.
	c, _ := enrollHarness(t)
	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))

	identity, err := st.IdentityKey()
	require.NoError(t, err)
	require.Equal(t, []byte(identity.Public().(ed25519.PublicKey)), c.seenIdentity,
		"the identity presented is the Tower's own identity key")
	require.Equal(t, "nonce-1", c.seenNonce)
}

func TestAnInterruptedRegistrationRetriesAsTheSameEnrollment(t *testing.T) {
	// The case this exists for: the first attempt fails after the operator has already
	// committed to it. The retry must ask about the SAME enrollment, or they end up with a
	// second Tower and a spent token.
	c, _ := enrollHarness(t)
	st := joinedTower(t)

	c.mu.Lock()
	c.failEnroll = 1
	c.mu.Unlock()

	require.Error(t, Register(st, Account{Login: "alice"}), "the first attempt fails")

	// The transaction id was recorded before the call, so it survives the failure.
	adm, _ := LoadAdmission(st.Dir())
	require.NotEmpty(t, adm.TransactionID)
	first := adm.TransactionID

	require.NoError(t, Register(st, Account{Login: "alice"}), "the retry succeeds")

	adm, ok := LoadAdmission(st.Dir())
	require.True(t, ok)
	require.Equal(t, first, adm.TransactionID, "the retry is the same enrollment, not a new one")

	c.mu.Lock()
	defer c.mu.Unlock()
	require.Equal(t, 1, c.enrollments, "exactly one Tower was ever issued")
	for _, txn := range c.seenTxn {
		require.Equal(t, first, txn)
	}
}

func TestRegisterRefusesACertificateItCannotRead(t *testing.T) {
	// Writing bytes we cannot parse would leave the Tower believing it is admitted and
	// failing at its first handshake instead, far from the cause.
	c, _ := enrollHarness(t)
	st := joinedTower(t)
	c.mu.Lock()
	c.badCertBytes = true
	c.mu.Unlock()

	require.Error(t, Register(st, Account{Login: "alice"}))
	_, err := os.Stat(filepath.Join(st.Dir(), certFile))
	require.True(t, os.IsNotExist(err), "nothing unusable is left on disk")
}

func TestRegisterSurfacesABrokerRefusal(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	mux := http.NewServeMux()
	mux.HandleFunc("/tower/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "this account may not run a Tower"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("ROGER_BROKER", srv.URL)

	st := joinedTower(t)
	err := Register(st, Account{Login: "alice"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "may not run a Tower",
		"the operator sees the reason, not a generic failure")
}

func TestLoadAdmissionIsAbsentBeforeRegistering(t *testing.T) {
	st := joinedTower(t)
	_, ok := LoadAdmission(st.Dir())
	require.False(t, ok)

	// And unreadable content reads as absent rather than as a half-admitted Tower.
	require.NoError(t, os.WriteFile(filepath.Join(st.Dir(), admitted), []byte("{not json"), 0o600))
	_, ok = LoadAdmission(st.Dir())
	require.False(t, ok)
}

func TestBrokerBaseFallsBackToThePublicNetwork(t *testing.T) {
	t.Setenv("ROGER_BROKER", "")
	require.NotEmpty(t, brokerBase())
	t.Setenv("ROGER_BROKER", "http://example.invalid")
	require.Equal(t, "http://example.invalid", brokerBase())
}

func TestRegisterRefusesAnUnusableChallenge(t *testing.T) {
	// A challenge we cannot use must stop the flow HERE. Signing something malformed would
	// produce a signature that simply never verifies, and the operator would see "invalid
	// enrollment" for a problem that was never theirs.
	for name, handler := range map[string]http.HandlerFunc{
		"no signing input": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"nonce": "n1", "signing_input": ""})
		},
		"unreadable signing input": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"nonce": "n1", "signing_input": "!!!not base64!!!"})
		},
		"no nonce": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"nonce": "", "signing_input": base64.StdEncoding.EncodeToString([]byte("x"))})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
			mux := http.NewServeMux()
			mux.HandleFunc("/tower/token", func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, map[string]any{"token": "tok-1"})
			})
			mux.HandleFunc("/tower/enroll/challenge", handler)
			srv := httptest.NewServer(mux)
			defer srv.Close()
			t.Setenv("ROGER_BROKER", srv.URL)

			st := joinedTower(t)
			require.Error(t, Register(st, Account{Login: "alice"}))
		})
	}
}

func TestRegisterRefusesWhenNoTokenIsIssued(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	mux := http.NewServeMux()
	mux.HandleFunc("/tower/token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{}) // 200, but no token
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("ROGER_BROKER", srv.URL)

	st := joinedTower(t)
	err := Register(st, Account{Login: "alice"})
	require.Error(t, err, "a 200 carrying no token is not a token")
}

func TestRegisterRefusesAnUnusableIssuerCertificate(t *testing.T) {
	c, _ := enrollHarness(t)
	st := joinedTower(t)
	c.mu.Lock()
	c.caCert = &x509.Certificate{Raw: []byte("not a certificate")}
	c.mu.Unlock()

	require.Error(t, Register(st, Account{Login: "alice"}),
		"a Tower that cannot read its issuer cannot verify anything later")
}

func TestRegisterSurfacesANonJSONFailure(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	mux := http.NewServeMux()
	mux.HandleFunc("/tower/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>gateway</html>"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("ROGER_BROKER", srv.URL)

	st := joinedTower(t)
	err := Register(st, Account{Login: "alice"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "502", "an unparseable failure still names the status")
}

func TestRegisterRefusesWhenTheTowersOwnKeysAreUnusable(t *testing.T) {
	// A truncated or replaced key file must stop enrollment with a message about THAT,
	// rather than producing a signature that never verifies and a rejection the operator
	// would read as the network refusing them.
	for name, file := range map[string]string{
		"identity key": "identity.key",
		"channel key":  "tls.key",
	} {
		t.Run(name, func(t *testing.T) {
			_, _ = enrollHarness(t)
			st := joinedTower(t)
			require.NoError(t, os.WriteFile(filepath.Join(st.Dir(), file), []byte("truncated"), 0o600))

			err := Register(st, Account{Login: "alice"})
			require.Error(t, err)
			require.Contains(t, err.Error(), "unreadable", "the message names the real problem")
		})
	}
}

func TestSaveAccountRefusesAnUnwritableDirectory(t *testing.T) {
	// Reporting success while the credential did not land would leave the operator
	// convinced they are signed in.
	require.Error(t, SaveAccount(filepath.Join(t.TempDir(), "no-such-dir"), Account{Login: "alice"}))
}

func TestAdmissionIsNotWrittenWhenItCannotBeSerialised(t *testing.T) {
	// A directory that vanished under us must surface, not be swallowed.
	dir := t.TempDir()
	require.NoError(t, os.RemoveAll(dir))
	require.Error(t, saveAdmission(dir, Admission{TowerID: "tw-1"}))
}

func TestRegisterRefusesUnreadableEncodings(t *testing.T) {
	// Distinct from "unparseable certificate": here the transport encoding itself is wrong,
	// which means we never even get bytes to inspect.
	for name, body := range map[string]map[string]any{
		"certificate": {
			"tower_id": "tw-1", "certificate": "!!!not base64!!!", "ca": "", "state": "quarantine",
		},
		"issuer": {
			"tower_id": "tw-1", "certificate": base64.StdEncoding.EncodeToString([]byte("x")),
			"ca": "!!!not base64!!!", "state": "quarantine",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
			mux := http.NewServeMux()
			mux.HandleFunc("/tower/token", func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, map[string]any{"token": "tok-1"})
			})
			mux.HandleFunc("/tower/enroll/challenge", func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, map[string]any{
					"nonce":         "n1",
					"signing_input": base64.StdEncoding.EncodeToString([]byte("input")),
				})
			})
			mux.HandleFunc("/tower/enroll", func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, body)
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			t.Setenv("ROGER_BROKER", srv.URL)

			st := joinedTower(t)
			require.Error(t, Register(st, Account{Login: "alice"}))
			_, err := os.Stat(filepath.Join(st.Dir(), certFile))
			require.True(t, os.IsNotExist(err), "nothing is written from a response we could not read")
		})
	}
}

func TestRegisterRefusesAnUnusableBrokerAddress(t *testing.T) {
	// A misconfigured address must fail before anything is signed or sent.
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	t.Setenv("ROGER_BROKER", "http://\x7f invalid")

	st := joinedTower(t)
	require.Error(t, Register(st, Account{Login: "alice"}))
}
