// Package deviceauth is the broker-mediated device login: how `roger login` and
// `roger-tower login` authenticate through RogerAI instead of through a provider.
//
// The CLI never sees a provider. It asks the broker to start a login, prints a RogerAI
// URL and a short code, and polls. A human opens that URL, signs in with whichever
// provider they like, and approves. Which providers exist becomes a server-side decision
// that already-installed binaries inherit, and the CLI's only outbound host is the broker.
//
// THE ONE PROPERTY EVERYTHING RESTS ON: the device code is bound to the requesting key AT
// ISSUE. No later step in the flow accepts a key as input, so there is no point at which a
// different key can be substituted. Approval decides WHICH ACCOUNT; it can never decide
// which key.
//
// The residual risk every device flow shares is social: an attacker starts a flow on their
// machine and talks a victim into approving the resulting code, binding the attacker's key
// to the victim's account. That cannot be solved in this state machine - it is solved by
// what the approval screen shows, which is why Describe deliberately exposes the request
// time and withholds the device code.
package deviceauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"
)

// userCodeAlphabet omits I, L, O, U, 0, 1 - the characters people misread or mis-hear
// when reading a code off a screen to someone, or typing it from a phone.
const userCodeAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"

const userCodeLen = 8 // 30^8 ~= 6.6e11, comfortably past the 32-bit floor the spec sets

// Status is what a poll reports.
type Status string

const (
	StatusPending  Status = "pending"
	StatusSlowDown Status = "slow_down"
	StatusApproved Status = "approved"
	StatusDenied   Status = "denied"
	StatusExpired  Status = "expired"
)

// errRejected is the ONLY error approval returns. Uniform by design: a distinguishable
// error would tell an attacker whether a guessed code exists.
var errRejected = errors.New("that code is not valid")

// Config tunes the flow. All of it is policy the broker owns, not the CLI.
type Config struct {
	TTL           time.Duration
	Interval      time.Duration
	MaxWrongCodes int
	// VerificationURI is the RogerAI page a user opens. It is deliberately OUR address:
	// the CLI must never be handed a provider endpoint.
	VerificationURI string
}

// Pending is what Start hands back to the CLI.
type Pending struct {
	DeviceCode       string
	UserCode         string
	VerificationURI  string
	IntervalSeconds  int
	ExpiresInSeconds int
}

// Result is what a poll hands back.
type Result struct {
	Status          Status
	Account         string
	BoundKey        string
	IntervalSeconds int
}

// Info is what the APPROVAL SCREEN may show. It carries what a human needs to judge the
// request and deliberately omits the device code, which is the CLI's secret - an approver
// who learned it could redeem the login themselves.
type Info struct {
	UserCode    string
	RequestedAt time.Time
	DeviceCode  string // always empty; present so the omission is explicit, not accidental
}

// Flow is the device-login state machine. It holds no login state of its own: everything
// lives in the Store, so a login belongs to the DEPLOYMENT rather than to whichever
// process happened to issue it. See store.go for why that matters.
type Flow struct {
	cfg    Config
	store  Store
	mu     sync.Mutex // guards offset only
	now    func() time.Time
	offset time.Duration
}

// New builds a flow over the in-process store, with sensible floors so a zero Config is
// still safe. This is the single-instance default: no new dependency, no configuration.
func New(cfg Config) *Flow { return NewWithStore(cfg, NewMemStore()) }

// NewWithStore builds a flow over an explicit store. Behind more than one broker instance
// this is what makes the flow completable at all: approval and polling reach different
// processes, so the state they share has to be outside both.
func NewWithStore(cfg Config, store Store) *Flow {
	if cfg.TTL <= 0 {
		cfg.TTL = 10 * time.Minute
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Second
	}
	if cfg.MaxWrongCodes <= 0 {
		cfg.MaxWrongCodes = 10
	}
	if cfg.VerificationURI == "" {
		cfg.VerificationURI = "https://rogerai.fm/device"
	}
	if store == nil {
		store = NewMemStore()
	}
	f := &Flow{cfg: cfg, store: store}
	f.now = func() time.Time { return time.Now().Add(f.readOffset()) }
	return f
}

func (f *Flow) readOffset() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.offset
}

// advance moves the flow's clock. Test-only seam: the alternative is sleeping through
// real expiry windows, which makes the suite slow and flaky.
func (f *Flow) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.offset += d
}

// unavailable wraps a store failure. It is deliberately NOT errRejected: see ErrUnavailable.
func unavailable(err error) error {
	if errors.Is(err, ErrCorruptRecord) {
		return ErrCorruptRecord
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}

// Start issues a code pair bound to pubKey.
//
// If the store cannot record the login, Start REFUSES rather than returning a code pair it
// knows it will lose. Issuing a code we cannot durably record is worse than refusing: the
// person walks away, finds the mail, approves - and only then learns none of it counted.
func (f *Flow) Start(pubKey string) (Pending, error) {
	if pubKey == "" {
		return Pending{}, errors.New("a login must be started by a signed request")
	}
	if err := f.store.Reap(f.now()); err != nil {
		return Pending{}, unavailable(err)
	}

	dev, err := randomToken(32)
	if err != nil {
		return Pending{}, err
	}
	user, err := randomUserCode()
	if err != nil {
		return Pending{}, err
	}
	now := f.now()
	rec := Record{
		DevHash:   hashCode(dev),
		UserHash:  hashCode(user),
		BoundKey:  pubKey,
		Status:    StatusPending,
		Requested: now,
		Expires:   now.Add(f.cfg.TTL),
		Interval:  f.cfg.Interval,
	}
	if err := f.store.Create(rec); err != nil {
		return Pending{}, unavailable(err)
	}
	return Pending{
		DeviceCode:       dev,
		UserCode:         user,
		VerificationURI:  f.cfg.VerificationURI,
		IntervalSeconds:  int(f.cfg.Interval.Seconds()),
		ExpiresInSeconds: int(f.cfg.TTL.Seconds()),
	}, nil
}

// Poll reports progress. It refuses any key other than the one recorded at issue, so a
// leaked device code is still not redeemable by anyone else - and it re-checks that against
// the STORED record, so a store an operator can edit is not a way to redirect a login.
func (f *Flow) Poll(deviceCode, pubKey string) (Result, error) {
	rec, ok, err := f.store.ByDevice(hashCode(deviceCode))
	if err != nil {
		return Result{}, unavailable(err)
	}
	if !ok || rec.Consumed {
		return Result{}, errRejected
	}
	if subtle.ConstantTimeCompare([]byte(rec.BoundKey), []byte(pubKey)) != 1 {
		return Result{}, errRejected
	}
	now := f.now()
	if now.After(rec.Expires) {
		return Result{Status: StatusExpired}, nil
	}
	// Polling faster than the interval slows the caller down rather than failing them.
	// The penalty is CAPPED and the poll is recorded: an uncapped, unrecorded penalty
	// grows on every call, so a tight loop could push the interval past the TTL and
	// permanently strand the legitimate CLI from its own login.
	if !rec.LastPoll.IsZero() && now.Sub(rec.LastPoll) < rec.Interval {
		rec.Interval += f.cfg.Interval
		if max := f.cfg.TTL / 4; rec.Interval > max {
			rec.Interval = max
		}
		rec.LastPoll = now
		if _, err := f.store.CAS(rec); err != nil {
			return Result{}, unavailable(err)
		}
		return Result{Status: StatusSlowDown, IntervalSeconds: int(rec.Interval.Seconds())}, nil
	}
	rec.LastPoll = now

	switch rec.Status {
	case StatusApproved:
		// The first successful poll after approval consumes the code. Consumption is the
		// CAS itself, not a read followed by a write: with two instances polling the same
		// approved login, exactly one may win, or the code is redeemable twice.
		rec.Consumed = true
		won, err := f.store.CAS(rec)
		if err != nil {
			return Result{}, unavailable(err)
		}
		if !won {
			return Result{}, errRejected
		}
		return Result{Status: StatusApproved, Account: rec.Account, BoundKey: rec.BoundKey}, nil
	case StatusDenied:
		return Result{Status: StatusDenied}, nil
	default:
		if _, err := f.store.CAS(rec); err != nil {
			return Result{}, unavailable(err)
		}
		return Result{Status: StatusPending, IntervalSeconds: int(rec.Interval.Seconds())}, nil
	}
}

// Approve binds an account to the pending login. The account comes from the approver's
// authenticated session; the KEY comes from the login record, never from this call.
//
// The account also identifies the SUBMITTER for guess-budget purposes: approval requires
// an authenticated session, so every attempt is attributable, and one person burning
// their budget cannot affect anyone else.
func (f *Flow) Approve(userCode, account string) error {
	if account == "" {
		return errRejected // an approval with no identity binds nobody to nothing
	}
	return f.settle(userCode, account, StatusApproved)
}

// Deny closes a pending login permanently.
func (f *Flow) Deny(userCode, account string) error {
	return f.settle(userCode, account, StatusDenied)
}

// settle is the one path by which a pending login stops being pending. Approval and denial
// differ only in the state they write, and routing both through a single CAS is what makes
// "it never reports both" true when they race on different instances.
func (f *Flow) settle(userCode, submitter string, to Status) error {
	rec, err := f.claimAttempt(userCode, submitter)
	if err != nil {
		return err
	}
	if rec.Status != StatusPending {
		return errRejected
	}
	rec.Status = to
	if to == StatusApproved {
		rec.Account = submitter
	}
	won, err := f.store.CAS(rec)
	if err != nil {
		return unavailable(err)
	}
	if !won {
		// Somebody else settled this login between our read and our write. Their outcome
		// stands; ours never happened.
		return errRejected
	}
	return nil
}

// claimAttempt spends a guessing budget slot BEFORE looking the code up, so a wrong guess
// costs the attacker whether or not it was close. The budget is PER SUBMITTER: a global
// counter would let one attacker lock everyone else out. It lives in the store, so it is
// neither refilled by a restart nor multiplied by spreading guesses across instances.
func (f *Flow) claimAttempt(userCode, submitter string) (Record, error) {
	spent, err := f.store.Budget(submitter)
	if err != nil {
		return Record{}, unavailable(err)
	}
	if spent >= f.cfg.MaxWrongCodes {
		return Record{}, errRejected
	}
	rec, ok, err := f.store.ByUser(hashCode(userCode))
	if err != nil {
		return Record{}, unavailable(err)
	}
	if !ok || rec.Consumed || f.now().After(rec.Expires) {
		if _, err := f.store.Penalize(submitter, f.cfg.TTL); err != nil {
			return Record{}, unavailable(err)
		}
		return Record{}, errRejected
	}
	return rec, nil
}

// BoundKey returns the key a pending or just-approved login is bound to. It is how the
// approval path learns WHICH key to bind - the key is never taken from the approving
// request, only from the record made at issue.
func (f *Flow) BoundKey(userCode string) (string, bool) {
	rec, ok, err := f.store.ByUser(hashCode(userCode))
	if err != nil || !ok {
		return "", false
	}
	return rec.BoundKey, true
}

// Describe is what the approval screen may render.
//
// It spends a guess-budget slot exactly like Approve. Without that it would be a free
// existence oracle: an attacker could enumerate user codes through the approval screen
// at zero cost and then spend a single Approve on a confirmed hit, never touching the
// budget the guessing defence relies on.
func (f *Flow) Describe(userCode, viewer string) (Info, bool) {
	rec, err := f.claimAttempt(userCode, viewer)
	if err != nil || rec.Status != StatusPending {
		return Info{}, false
	}
	// The plaintext user code is echoed from the ARGUMENT, never from the record: the
	// record holds only its hash, which is the point.
	return Info{UserCode: userCode, RequestedAt: rec.Requested}, true
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randomUserCode() (string, error) {
	out := make([]byte, userCodeLen)
	max := big.NewInt(int64(len(userCodeAlphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = userCodeAlphabet[n.Int64()]
	}
	return string(out), nil
}
