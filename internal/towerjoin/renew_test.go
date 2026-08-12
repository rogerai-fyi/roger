package towerjoin

// renew_test.go covers the schedule and the exchange that keep a Tower alive.
//
// Contract: features/tower/public_enrollment.feature.
//
// THE BUG THESE EXIST FOR was not a wrong answer, it was an absent caller: Core's renewal
// logic and this client were both fully written, fully tested, and reachable from nothing.
// Certificates and leases are 24 hours, so every Tower would have died a day after
// enrolling. So the tests that matter most here are the ones that fail if renewal stops
// being CALLED - not just the ones that check it computes the right thing.

import (
	"crypto/x509"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// renewingCore answers the two renewal routes with a freshly issued credential.
// The counter is ATOMIC rather than mutex-guarded. The handler writes it on the server's
// goroutine and the test reads it from require.Eventually; a mutex local to this function
// protects only the write, which is the race the first version of this shipped with.
func renewingCore(t *testing.T, c *stubCore, mux *http.ServeMux, life time.Duration) *atomic.Int64 {
	t.Helper()
	var count atomic.Int64
	mux.HandleFunc("/tower/renew/challenge", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"nonce":         "renew-nonce",
			"expires_at":    time.Now().Add(time.Minute).Unix(),
			"signing_input": base64.StdEncoding.EncodeToString([]byte("renew-signing-input")),
		})
	})
	mux.HandleFunc("/tower/renew", func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		writeJSON(w, map[string]any{
			"tower_id":      "tw-1",
			"certificate":   base64.StdEncoding.EncodeToString(c.issueLeaf(t, life)),
			"ca":            base64.StdEncoding.EncodeToString(c.caCert.Raw),
			"state":         "active",
			"lease_expires": time.Now().Add(24 * time.Hour).Unix(),
			"not_after":     time.Now().Add(life).Unix(),
		})
	})
	return &count
}

// THE SCHEDULE, as a pure function. Two thirds of the way through, so a broker restart or a
// partition has a third of the lifetime as margin - the failure mode of renewing late is not
// a retry, it is re-enrollment through an administrator.
func TestRenewalIsDueTwoThirdsThroughTheCertificatesLife(t *testing.T) {
	issued := time.Unix(1_700_000_000, 0)
	due := DueAt(issued, issued.Add(24*time.Hour))
	require.Equal(t, issued.Add(16*time.Hour), due)
}

// A credential with no life left is due IMMEDIATELY rather than never. "Never" is how a
// clock problem turns into a Tower that quietly stops.
func TestACredentialWithNoLifeLeftIsDueAtOnce(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	require.Equal(t, at, DueAt(at, at))
	require.Equal(t, at, DueAt(at, at.Add(-time.Hour)))
}

func TestRenewalDoesNothingBeforeItIsDue(t *testing.T) {
	enrollHarness(t)
	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))

	// The stub issues an hour of life, so nothing is due immediately.
	did, err := RenewIfDue(st, time.Now())
	require.NoError(t, err)
	require.False(t, did)
}

func TestAnUnenrolledTowerHasNothingToRenewAndSaysNothing(t *testing.T) {
	enrollHarness(t)
	st := joinedTower(t)

	did, err := RenewIfDue(st, time.Now())
	require.NoError(t, err, "a Tower that has not enrolled yet is not a failure")
	require.False(t, did)
}

// THE EXCHANGE. A due Tower renews, and the new credential replaces the old on disk.
func TestADueTowerRenewsAndStoresTheNewCredential(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	c, mux := newStubCoreMux(t)
	calls := renewingCore(t, c, mux, 48*time.Hour)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("ROGER_BROKER", srv.URL)

	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))
	before, err := os.ReadFile(filepath.Join(st.Dir(), certFile))
	require.NoError(t, err)

	// Far enough into the certificate's life that renewal is due.
	did, err := RenewIfDue(st, time.Now().Add(48*time.Hour))
	require.NoError(t, err)
	require.True(t, did)
	require.Equal(t, int64(1), calls.Load())

	after, err := os.ReadFile(filepath.Join(st.Dir(), certFile))
	require.NoError(t, err)
	require.NotEqual(t, before, after, "the new certificate must replace the old one")
	_, err = x509.ParseCertificate(after)
	require.NoError(t, err)

	// And the recorded expiry moved out, which is what stops it renewing again on every tick.
	adm, found := LoadAdmission(st.Dir())
	require.True(t, found)
	require.True(t, adm.NotAfter.After(time.Now().Add(24*time.Hour)))
}

// A CREDENTIAL THAT CANNOT BE READ MUST NOT REPLACE ONE THAT WORKS. Writing unparseable
// bytes would turn a renewal into the outage it exists to prevent.
func TestAnUnusableReissueLeavesTheWorkingCertificateAlone(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	c, mux := newStubCoreMux(t)
	mux.HandleFunc("/tower/renew/challenge", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"nonce": "n", "expires_at": time.Now().Add(time.Minute).Unix(),
			"signing_input": base64.StdEncoding.EncodeToString([]byte("in")),
		})
	})
	mux.HandleFunc("/tower/renew", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"tower_id":    "tw-1",
			"certificate": base64.StdEncoding.EncodeToString([]byte("not a certificate")),
			"ca":          base64.StdEncoding.EncodeToString(c.caCert.Raw),
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("ROGER_BROKER", srv.URL)

	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))
	before, err := os.ReadFile(filepath.Join(st.Dir(), certFile))
	require.NoError(t, err)

	_, err = RenewIfDue(st, time.Now().Add(48*time.Hour))
	require.ErrorContains(t, err, "unusable")

	after, err := os.ReadFile(filepath.Join(st.Dir(), certFile))
	require.NoError(t, err)
	require.Equal(t, before, after, "a failed renewal must leave the working credential in place")
}

func TestARefusedRenewalIsReportedRatherThanSwallowed(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	_, mux := newStubCoreMux(t)
	mux.HandleFunc("/tower/renew/challenge", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"that Tower cannot renew"}}`, http.StatusBadRequest)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("ROGER_BROKER", srv.URL)

	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))
	_, err := RenewIfDue(st, time.Now().Add(48*time.Hour))
	require.Error(t, err)
}

func TestAChallengeWithNothingInItIsRefused(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	_, mux := newStubCoreMux(t)
	mux.HandleFunc("/tower/renew/challenge", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("ROGER_BROKER", srv.URL)

	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))
	_, err := RenewIfDue(st, time.Now().Add(48*time.Hour))
	require.ErrorContains(t, err, "no renewal challenge")
}

func TestAnUnreadableChallengeIsRefused(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	_, mux := newStubCoreMux(t)
	mux.HandleFunc("/tower/renew/challenge", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"nonce": "n", "signing_input": "!!!not base64!!!"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("ROGER_BROKER", srv.URL)

	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))
	_, err := RenewIfDue(st, time.Now().Add(48*time.Hour))
	require.ErrorContains(t, err, "could not be read")
}

// THE TEST THE ORIGINAL BUG WOULD HAVE FAILED. Not "does renewal work" but "does anything
// call it" - the loop a serving Tower runs must actually renew when the time comes.
func TestTheServingLoopActuallyRenews(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	c, mux := newStubCoreMux(t)
	calls := renewingCore(t, c, mux, 48*time.Hour)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("ROGER_BROKER", srv.URL)

	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))
	// The stub issues one hour, so by wall-clock now the credential is already past two
	// thirds of its life and the very first tick should renew it.
	forceExpiry(t, st.Dir(), time.Now().Add(-time.Minute))

	tick := make(chan time.Time, 1)
	stop := make(chan struct{})
	var out syncWriter
	done := make(chan struct{})
	go func() {
		defer close(done)
		KeepRenewed(st, &out, stop, func(time.Duration) (<-chan time.Time, func()) {
			return tick, func() {}
		})
	}()

	tick <- time.Now()
	require.Eventually(t, func() bool { return calls.Load() > 0 },
		5*time.Second, 10*time.Millisecond)

	close(stop)
	<-done
	require.Contains(t, out.String(), "renewed this Tower's certificate")
}

// A failed renewal is reported and retried, never fatal: the current certificate is still
// valid, so a broker that is briefly down must not take the Tower down with it.
func TestTheServingLoopSurvivesAFailedRenewalAndSaysSo(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	_, mux := newStubCoreMux(t)
	mux.HandleFunc("/tower/renew/challenge", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusServiceUnavailable)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("ROGER_BROKER", srv.URL)

	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))
	forceExpiry(t, st.Dir(), time.Now().Add(-time.Minute))

	tick := make(chan time.Time, 1)
	stop := make(chan struct{})
	var out syncWriter
	done := make(chan struct{})
	go func() {
		defer close(done)
		KeepRenewed(st, &out, stop, func(time.Duration) (<-chan time.Time, func()) {
			return tick, func() {}
		})
	}()

	tick <- time.Now()
	require.Eventually(t, func() bool {
		return len(out.String()) > 0
	}, 5*time.Second, 10*time.Millisecond)

	close(stop)
	<-done
	require.Contains(t, out.String(), "could not renew")
	require.Contains(t, out.String(), "still valid")
}

func TestTheServingLoopStopsWhenAsked(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	st := joinedTower(t)
	stop := make(chan struct{})
	done := make(chan struct{})
	var out syncWriter
	go func() {
		defer close(done)
		KeepRenewed(st, &out, stop, func(time.Duration) (<-chan time.Time, func()) {
			return make(chan time.Time), func() {}
		})
	}()
	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the renewal loop did not stop")
	}
}

// forceExpiry rewrites the recorded expiry so a test does not have to wait a day.
func forceExpiry(t *testing.T, dir string, notAfter time.Time) {
	t.Helper()
	adm, found := LoadAdmission(dir)
	require.True(t, found)
	adm.NotAfter = notAfter
	require.NoError(t, saveAdmission(dir, adm))
}

type syncWriter struct {
	mu sync.Mutex
	b  []byte
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.b = append(w.b, p...)
	return len(p), nil
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.b)
}

// The issue time comes from the CERTIFICATE, not from a timestamp we wrote down: the two can
// differ after a clock change or a directory copied between machines, and the certificate is
// what Core will actually judge. When it cannot be read, fall back to a conservative window
// rather than treating the credential as fresh.
func TestTheIssueTimeFallsBackWhenTheCertificateCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	notAfter := time.Unix(1_700_000_000, 0)
	adm := Admission{NotAfter: notAfter}

	// No certificate at all.
	require.Equal(t, notAfter.Add(-24*time.Hour), issuedAt(dir, adm))

	// Present but unparseable.
	require.NoError(t, os.WriteFile(filepath.Join(dir, certFile), []byte("not a certificate"), 0o644))
	require.Equal(t, notAfter.Add(-24*time.Hour), issuedAt(dir, adm))
}

func TestAnUnusableIssuerCertificateIsRefused(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	c, mux := newStubCoreMux(t)
	mux.HandleFunc("/tower/renew/challenge", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"nonce": "n", "expires_at": time.Now().Add(time.Minute).Unix(),
			"signing_input": base64.StdEncoding.EncodeToString([]byte("in")),
		})
	})
	mux.HandleFunc("/tower/renew", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"tower_id":    "tw-1",
			"certificate": base64.StdEncoding.EncodeToString(c.issueLeaf(t, time.Hour)),
			"ca":          base64.StdEncoding.EncodeToString([]byte("not a certificate")),
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("ROGER_BROKER", srv.URL)

	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))
	_, err := RenewIfDue(st, time.Now().Add(48*time.Hour))
	require.ErrorContains(t, err, "issuer certificate is unusable")
}

// A credential the broker returns as unreadable base64 must not replace a working one either.
func TestAnUnreadableReissueIsRefused(t *testing.T) {
	for name, reply := range map[string]map[string]any{
		"certificate not base64": {"tower_id": "tw-1", "certificate": "!!!", "ca": "AAAA"},
		"ca not base64":          {"tower_id": "tw-1", "certificate": "AAAA", "ca": "!!!"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
			_, mux := newStubCoreMux(t)
			mux.HandleFunc("/tower/renew/challenge", func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, map[string]any{
					"nonce": "n", "expires_at": time.Now().Add(time.Minute).Unix(),
					"signing_input": base64.StdEncoding.EncodeToString([]byte("in")),
				})
			})
			mux.HandleFunc("/tower/renew", func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, reply)
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			t.Setenv("ROGER_BROKER", srv.URL)

			st := joinedTower(t)
			require.NoError(t, Register(st, Account{Login: "alice"}))
			_, err := RenewIfDue(st, time.Now().Add(48*time.Hour))
			require.ErrorContains(t, err, "could not be read")
		})
	}
}

func TestARefusedRenewalPostIsReported(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	_, mux := newStubCoreMux(t)
	mux.HandleFunc("/tower/renew/challenge", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"nonce": "n", "expires_at": time.Now().Add(time.Minute).Unix(),
			"signing_input": base64.StdEncoding.EncodeToString([]byte("in")),
		})
	})
	mux.HandleFunc("/tower/renew", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"that renewal is not valid"}}`, http.StatusBadRequest)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("ROGER_BROKER", srv.URL)

	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))
	_, err := RenewIfDue(st, time.Now().Add(48*time.Hour))
	require.Error(t, err)
}
