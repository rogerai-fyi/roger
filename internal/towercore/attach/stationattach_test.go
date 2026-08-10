package attach

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// features/tower/station_attachment.feature, transcribed as it applies to the admission
// transaction: "A Station proves both independent private keys during attachment", "Joined
// attachment commits Core authority before local bridge access", "Invalid attachment creates
// no partial Station authority", "Concurrent attachment consumes one authorization once",
// "StationAttachAuthorizationV1 origin presence is closed", "Station origin kind is
// immutable in v1".
//
// Every refusal row asserts TWO things, because the spec asks for two: the attachment is
// refused, AND the invitation is left unspent. A check that refuses but still burns the
// invitation locks an owner out of a Station they are entitled to, which is a worse outcome
// than the attack it was defending against.

const (
	net      = "roger-public"
	tower    = "tw-1"
	station  = "st-1"
	owner    = "owner_pub"
	keyA     = "assertion_key_A"
	keyK     = "session_key_K"
	authorID = "auth-1"
)

// inviteSecret is the plaintext an operator would hand a Station. Tests mint invitations
// carrying its verifier so the secret check is exercised on every path rather than bypassed.
const inviteSecret = "test-invite-secret-0123456789"

// withSecret stamps the one-use verifier onto a hand-built invitation.
func withSecret(a Authorization) Authorization {
	sum := sha256.Sum256([]byte(inviteSecret))
	a.SecretHash = hex.EncodeToString(sum[:])
	return a
}

func fixture(t *testing.T) (*Registry, Store, time.Time) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	s := NewMemStore()
	r := New(Config{Network: net, Now: func() time.Time { return now }}, s)
	require.NoError(t, s.PutAuthorization(withSecret(Authorization{
		ID: authorID, Network: net, StationID: station, Owner: owner,
		Origin:       Origin{Kind: OriginJoined, TowerID: tower},
		AssertionKey: keyA, SessionKey: keyK, CeilingHash: "ceil-1",
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	})))
	return r, s, now
}

func goodProof() Proof {
	return Proof{
		AuthID: authorID, Secret: inviteSecret, Network: net, StationID: station, Owner: owner,
		Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: keyA, SessionKey: keyK,
	}
}

func TestAJoinedAttachmentCommitsAndLandsInQuarantine(t *testing.T) {
	r, s, now := fixture(t)

	at, err := r.Admit(goodProof())
	require.NoError(t, err)
	require.Equal(t, station, at.StationID)
	require.Equal(t, owner, at.Owner)
	require.Equal(t, keyA, at.AssertionKey)
	require.Equal(t, keyK, at.SessionKey)
	require.Equal(t, Origin{Kind: OriginJoined, TowerID: tower}, at.Origin)
	require.Equal(t, int64(1), at.Epoch)
	require.Equal(t, "ceil-1", at.CeilingHash, "the capability ceiling is recorded at admission")
	require.Equal(t, now, at.AttachedAt)

	// Admission proves WHO, never that the Station is any good. Eligibility is earned later.
	require.Equal(t, StateQuarantine, at.State,
		"a freshly attached Station is quarantine inventory, not routable")

	// The invitation is spent, in the same commit.
	auth, ok, err := s.Authorization(authorID)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, auth.Consumed)
	require.Equal(t, station, auth.ConsumedBy)

	// And this is the read inv.Policy will make.
	got, ok, err := r.Station(station)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, keyA, got.AssertionKey)
}

// The race the whole design exists for.
func TestConcurrentAttachmentConsumesOneAuthorizationOnce(t *testing.T) {
	r, s, _ := fixture(t)

	const racers = 16
	var wg sync.WaitGroup
	results := make([]Attachment, racers)
	errs := make([]error, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = r.Admit(goodProof())
		}(i)
	}
	close(start)
	wg.Wait()

	// Identical proofs, so every caller gets the SAME committed outcome - a lost response is
	// not a reason to refuse somebody who did nothing wrong.
	for i := 0; i < racers; i++ {
		require.NoError(t, errs[i], "racer %d", i)
		require.Equal(t, station, results[i].StationID)
		require.Equal(t, int64(1), results[i].Epoch, "nobody may mint a second origin")
	}
	auth, _, err := s.Authorization(authorID)
	require.NoError(t, err)
	require.True(t, auth.Consumed)
	require.Equal(t, station, auth.ConsumedBy)
}

// A retry after a lost reply is answerable; reuse with different keys is not.
func TestARetryIsAnsweredAndDivergentReuseIsRefused(t *testing.T) {
	r, _, _ := fixture(t)
	first, err := r.Admit(goodProof())
	require.NoError(t, err)

	again, err := r.Admit(goodProof())
	require.NoError(t, err, "an exact retry must return the committed outcome")
	require.Equal(t, first, again)

	diverged := goodProof()
	diverged.AssertionKey = "some_other_key"
	_, err = r.Admit(diverged)
	require.ErrorIs(t, err, ErrRejected,
		"the same invitation with different keys is reuse, not a retry")
}

func TestInvalidAttachmentCreatesNoPartialAuthority(t *testing.T) {
	rows := []struct {
		defect string
		want   string
		mutate func(p *Proof)
		// authMutate adjusts the stored invitation instead, for defects that live there.
		authMutate func(a *Authorization)
	}{
		{defect: "an unknown invitation", want: "no such invitation",
			mutate: func(p *Proof) { p.AuthID = "auth-nope" }},
		{defect: "an expired invitation", want: "expired",
			authMutate: func(a *Authorization) { a.ExpiresAt = a.IssuedAt }},
		{defect: "an invitation for another network", want: "another network",
			mutate: func(p *Proof) { p.Network = "roger-private" }},
		{defect: "an invitation for another Station", want: "another Station",
			mutate: func(p *Proof) { p.StationID = "st-other" }},
		{defect: "an invitation belonging to another owner", want: "another owner",
			mutate: func(p *Proof) { p.Owner = "someone_else" }},
		{defect: "an invitation for another Tower", want: "another origin",
			mutate: func(p *Proof) { p.Origin = Origin{Kind: OriginJoined, TowerID: "tw-other"} }},
		{defect: "an assertion key the invitation does not name", want: "assertion key is not the one",
			mutate: func(p *Proof) { p.AssertionKey = "wrong_A" }},
		{defect: "a secure-session key the invitation does not name", want: "secure-session key is not the one",
			mutate: func(p *Proof) { p.SessionKey = "wrong_K" }},
		{defect: "one key reused for both purposes", want: "must be different keys",
			mutate:     func(p *Proof) { p.SessionKey = keyA },
			authMutate: func(a *Authorization) { a.SessionKey = keyA }},
		{defect: "a missing secure-session key", want: "needs both",
			mutate:     func(p *Proof) { p.SessionKey = "" },
			authMutate: func(a *Authorization) { a.SessionKey = "" }},
		{defect: "joined origin with no Tower", want: "joined origin needs exactly one",
			mutate:     func(p *Proof) { p.Origin = Origin{Kind: OriginJoined} },
			authMutate: func(a *Authorization) { a.Origin = Origin{Kind: OriginJoined} }},
		{defect: "direct origin carrying a Tower", want: "must carry no Tower ID",
			mutate:     func(p *Proof) { p.Origin = Origin{Kind: OriginDirect, TowerID: tower} },
			authMutate: func(a *Authorization) { a.Origin = Origin{Kind: OriginDirect, TowerID: tower} }},
		{defect: "an unknown origin kind", want: "unknown origin kind",
			mutate:     func(p *Proof) { p.Origin = Origin{Kind: "sideways"} },
			authMutate: func(a *Authorization) { a.Origin = Origin{Kind: "sideways"} }},
	}

	for _, row := range rows {
		t.Run(row.defect, func(t *testing.T) {
			r, s, _ := fixture(t)
			if row.authMutate != nil {
				auth, _, err := s.Authorization(authorID)
				require.NoError(t, err)
				row.authMutate(&auth)
				require.NoError(t, s.PutAuthorization(auth))
			}
			p := goodProof()
			if row.mutate != nil {
				row.mutate(&p)
			}

			_, err := r.Admit(p)
			require.ErrorIs(t, err, ErrRejected, "defect %q must be refused", row.defect)
			require.Contains(t, err.Error(), row.want,
				"defect %q was caught by a different check than the one it names", row.defect)

			// NOTHING partial: no attachment, and the invitation is still spendable.
			_, ok, serr := r.Station(station)
			require.NoError(t, serr)
			require.False(t, ok, "a refused attachment must record no Station")
			auth, ok, aerr := s.Authorization(authorID)
			require.NoError(t, aerr)
			if ok {
				require.False(t, auth.Consumed,
					"a refused attachment must leave the invitation unspent, or the owner is locked out")
			}
		})
	}
}

// Origin kind is immutable in v1: the identity carries earnings lineage and capacity, and a
// silent migration would move all of it.
func TestOriginKindIsImmutable(t *testing.T) {
	r, s, now := fixture(t)
	_, err := r.Admit(goodProof())
	require.NoError(t, err)

	// A second, perfectly valid invitation for the SAME Station under the other kind.
	require.NoError(t, s.PutAuthorization(withSecret(Authorization{
		ID: "auth-2", Network: net, StationID: station, Owner: owner,
		Origin: Origin{Kind: OriginDirect}, AssertionKey: keyA, SessionKey: keyK,
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	})))
	p := goodProof()
	p.AuthID, p.Origin = "auth-2", Origin{Kind: OriginDirect}

	_, err = r.Admit(p)
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), "origin kind cannot change")

	got, _, err := r.Station(station)
	require.NoError(t, err)
	require.Equal(t, OriginJoined, got.Origin.Kind, "the original origin must be untouched")
}

func TestAKeyCannotBeSharedBetweenTwoStations(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		mutate     func(a *Authorization, p *Proof)
	}{
		{"a shared secure-session key", "already bound to another Station",
			func(a *Authorization, p *Proof) { a.AssertionKey, p.AssertionKey = "A2", "A2" }},
		{"a shared assertion key", "already bound to another Station",
			func(a *Authorization, p *Proof) { a.SessionKey, p.SessionKey = "K2", "K2" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s, now := fixture(t)
			_, err := r.Admit(goodProof())
			require.NoError(t, err)

			auth := Authorization{
				ID: "auth-2", Network: net, StationID: "st-2", Owner: owner,
				Origin:       Origin{Kind: OriginJoined, TowerID: tower},
				AssertionKey: keyA, SessionKey: keyK,
				IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
			}
			p := Proof{
				AuthID: "auth-2", Secret: inviteSecret, Network: net, StationID: "st-2", Owner: owner,
				Origin:       Origin{Kind: OriginJoined, TowerID: tower},
				AssertionKey: keyA, SessionKey: keyK,
			}
			tc.mutate(&auth, &p)
			require.NoError(t, s.PutAuthorization(withSecret(auth)))

			_, err = r.Admit(p)
			require.ErrorIs(t, err, ErrRejected)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

// A revoked Station ID is terminal - that is what makes "revoke, then attach a new ID" the
// only cross-kind path.
func TestARevokedStationCannotBeReattached(t *testing.T) {
	r, s, now := fixture(t)
	_, err := r.Admit(goodProof())
	require.NoError(t, err)
	ok, err := r.Revoke(station)
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, s.PutAuthorization(withSecret(Authorization{
		ID: "auth-2", Network: net, StationID: station, Owner: owner,
		Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: keyA, SessionKey: keyK,
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	})))
	p := goodProof()
	p.AuthID = "auth-2"

	_, err = r.Admit(p)
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), "retired")
}

func TestPromotionIsSeparateFromAdmission(t *testing.T) {
	r, _, _ := fixture(t)
	at, err := r.Admit(goodProof())
	require.NoError(t, err)
	require.Equal(t, StateQuarantine, at.State)

	moved, err := r.Promote(station)
	require.NoError(t, err)
	require.True(t, moved)
	got, _, err := r.Station(station)
	require.NoError(t, err)
	require.Equal(t, StateActive, got.State)

	// Promoting twice is not an error, but it is also not a second promotion.
	moved, err = r.Promote(station)
	require.NoError(t, err)
	require.False(t, moved)

	// A revoked Station is never promotable back into service.
	_, err = r.Revoke(station)
	require.NoError(t, err)
	moved, err = r.Promote(station)
	require.NoError(t, err)
	require.False(t, moved)
}

func TestAZeroConfigIsSafe(t *testing.T) {
	r := New(Config{}, NewMemStore())
	require.Equal(t, "roger-public", r.cfg.Network)
	require.Positive(t, r.cfg.Skew)
	require.NotNil(t, r.cfg.Now)
}

// --- the one-use invitation secret ------------------------------------------
//
// Possession of the two keys is not proof of invitation: the OPERATOR chose those keys at
// invite time, so anyone who learned them and the authorization id could otherwise attach in
// the Station's place. The secret is what proves the presenter was actually handed this
// invitation. It is shown once and stored only as a verifier, so reading the table cannot
// give anybody an attachment.

func TestNewInviteMintsAVerifierAndShowsTheSecretOnce(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	auth, secret, err := NewInvite(Authorization{
		ID: "auth-9", Network: net, StationID: "st-9", Owner: owner,
		Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: keyA, SessionKey: keyK,
	}, time.Hour, now)
	require.NoError(t, err)

	require.NotEmpty(t, secret)
	require.NotEqual(t, secret, auth.SecretHash, "the stored value is a verifier, not the secret")
	sum := sha256.Sum256([]byte(secret))
	require.Equal(t, hex.EncodeToString(sum[:]), auth.SecretHash)
	require.Equal(t, now, auth.IssuedAt)
	require.Equal(t, now.Add(time.Hour), auth.ExpiresAt)
	require.False(t, auth.Consumed, "a fresh invitation is unspent whatever was passed in")

	// Two invitations never share a secret.
	_, second, err := NewInvite(auth, time.Hour, now)
	require.NoError(t, err)
	require.NotEqual(t, secret, second)
}

func TestNewInviteRefusesAnIncoherentRequest(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	base := Authorization{
		ID: "auth-9", Network: net, StationID: "st-9", Owner: owner,
		Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: keyA, SessionKey: keyK,
	}
	for _, tc := range []struct {
		name   string
		mutate func(a *Authorization)
		ttl    time.Duration
	}{
		{"no id", func(a *Authorization) { a.ID = "" }, time.Hour},
		{"no network", func(a *Authorization) { a.Network = "" }, time.Hour},
		{"no Station", func(a *Authorization) { a.StationID = "" }, time.Hour},
		{"no owner", func(a *Authorization) { a.Owner = "" }, time.Hour},
		{"only one key", func(a *Authorization) { a.SessionKey = "" }, time.Hour},
		{"one key for both purposes", func(a *Authorization) { a.SessionKey = a.AssertionKey }, time.Hour},
		{"a joined origin with no Tower", func(a *Authorization) { a.Origin = Origin{Kind: OriginJoined} }, time.Hour},
		{"no lifetime", func(a *Authorization) {}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := base
			tc.mutate(&a)
			_, _, err := NewInvite(a, tc.ttl, now)
			require.Error(t, err, "an invitation that cannot be redeemed must not be minted")
		})
	}
}

func TestTheWrongSecretIsRefusedAndCostsNothing(t *testing.T) {
	r, s, _ := fixture(t)

	p := goodProof()
	p.Secret = "not-the-secret"
	_, err := r.Admit(p)
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), "invitation secret does not match")

	// And the invitation is still spendable - a wrong guess must not lock the owner out.
	auth, ok, err := s.Authorization(authorID)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, auth.Consumed)

	at, err := r.Admit(goodProof())
	require.NoError(t, err)
	require.Equal(t, station, at.StationID)
}

// A row that lost its verifier must not become an invitation anyone can redeem. Fail closed:
// no verifier means unusable, not open.
func TestAnInvitationWithNoVerifierCannotBeRedeemed(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	s := NewMemStore()
	require.NoError(t, s.PutAuthorization(Authorization{
		ID: authorID, Network: net, StationID: station, Owner: owner,
		Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: keyA, SessionKey: keyK,
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}))
	r := New(Config{Network: net, Now: func() time.Time { return now }}, s)

	_, err := r.Admit(goodProof())
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), "no verifier")
}

// The secret is checked LAST, after everything cheap. A caller who gets the Station wrong
// learns that before the secret is ever compared, so the comparison is not a probing oracle
// for anything else.
func TestTheSecretIsCheckedAfterTheCheapRefusals(t *testing.T) {
	r, _, _ := fixture(t)
	p := goodProof()
	p.Secret, p.StationID = "wrong-secret", "st-other"

	_, err := r.Admit(p)
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), "another Station",
		"the cheap mismatch is reported; the secret comparison is not reached")
}

// A consumed invitation replayed with the WRONG secret must not answer.
//
// Found by audit. replay compared station, owner, keys and origin but never the secret, so
// the constant-time check in validate was bypassed entirely for consumed authorizations.
// Holding the authorization id and the two PUBLIC keys was enough to confirm an attachment
// existed and read its record - a probing oracle, even though nothing could be minted.
func TestAReplayWithTheWrongSecretIsRefused(t *testing.T) {
	r, _, _ := fixture(t)
	first, err := r.Admit(goodProof())
	require.NoError(t, err)

	p := goodProof()
	p.Secret = "not-the-secret"
	_, err = r.Admit(p)
	require.ErrorIs(t, err, ErrRejected,
		"the secret is part of identical proof, on the replay path too")

	// The genuine holder still gets their answer.
	again, err := r.Admit(goodProof())
	require.NoError(t, err)
	require.Equal(t, first, again)
}
