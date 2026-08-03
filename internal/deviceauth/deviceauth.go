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

type login struct {
	deviceCode string
	userCode   string
	boundKey   string // recorded at issue; never re-read from a later request
	account    string
	status     Status
	requested  time.Time
	expires    time.Time
	lastPoll   time.Time
	interval   time.Duration
	consumed   bool
}

// Flow is the device-login state machine.
type Flow struct {
	mu     sync.Mutex
	cfg    Config
	byDev  map[string]*login
	byUser map[string]*login
	// wrong counts bad user codes PER SUBMITTER. It must not be one global counter:
	// a shared budget lets a single attacker exhaust it and lock every other person
	// out of signing in, turning an anti-guessing control into a denial of service.
	wrong  map[string]int
	now    func() time.Time
	offset time.Duration
}

// New builds a flow with sensible floors, so a zero Config is still safe.
func New(cfg Config) *Flow {
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
	f := &Flow{cfg: cfg, byDev: map[string]*login{}, byUser: map[string]*login{}, wrong: map[string]int{}}
	f.now = func() time.Time { return time.Now().Add(f.offset) }
	return f
}

// advance moves the flow's clock. Test-only seam: the alternative is sleeping through
// real expiry windows, which makes the suite slow and flaky.
func (f *Flow) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.offset += d
}

// Start issues a code pair bound to pubKey.
func (f *Flow) Start(pubKey string) (Pending, error) {
	if pubKey == "" {
		return Pending{}, errors.New("a login must be started by a signed request")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reapLocked()

	dev, err := randomToken(32)
	if err != nil {
		return Pending{}, err
	}
	user, err := randomUserCode()
	if err != nil {
		return Pending{}, err
	}
	now := f.now()
	l := &login{
		deviceCode: dev, userCode: user, boundKey: pubKey,
		status: StatusPending, requested: now, expires: now.Add(f.cfg.TTL),
		interval: f.cfg.Interval,
	}
	f.byDev[dev] = l
	f.byUser[user] = l
	return Pending{
		DeviceCode:       dev,
		UserCode:         user,
		VerificationURI:  f.cfg.VerificationURI,
		IntervalSeconds:  int(f.cfg.Interval.Seconds()),
		ExpiresInSeconds: int(f.cfg.TTL.Seconds()),
	}, nil
}

// Poll reports progress. It refuses any key other than the one recorded at issue, so a
// leaked device code is still not redeemable by anyone else.
func (f *Flow) Poll(deviceCode, pubKey string) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	l, ok := f.byDev[deviceCode]
	if !ok || l.consumed {
		return Result{}, errRejected
	}
	if subtle.ConstantTimeCompare([]byte(l.boundKey), []byte(pubKey)) != 1 {
		return Result{}, errRejected
	}
	now := f.now()
	if now.After(l.expires) {
		return Result{Status: StatusExpired}, nil
	}
	// Polling faster than the interval slows the caller down rather than failing them.
	// The penalty is CAPPED and the poll is recorded: an uncapped, unrecorded penalty
	// grows on every call, so a tight loop could push the interval past the TTL and
	// permanently strand the legitimate CLI from its own login.
	if !l.lastPoll.IsZero() && now.Sub(l.lastPoll) < l.interval {
		l.interval += f.cfg.Interval
		if max := f.cfg.TTL / 4; l.interval > max {
			l.interval = max
		}
		l.lastPoll = now
		return Result{Status: StatusSlowDown, IntervalSeconds: int(l.interval.Seconds())}, nil
	}
	l.lastPoll = now

	switch l.status {
	case StatusApproved:
		// First successful poll after approval consumes the code.
		l.consumed = true
		return Result{Status: StatusApproved, Account: l.account, BoundKey: l.boundKey}, nil
	case StatusDenied:
		return Result{Status: StatusDenied}, nil
	default:
		return Result{Status: StatusPending, IntervalSeconds: int(l.interval.Seconds())}, nil
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
	f.mu.Lock()
	defer f.mu.Unlock()
	l, err := f.claimAttempt(userCode, account)
	if err != nil {
		return err
	}
	if l.status != StatusPending {
		return errRejected
	}
	l.status = StatusApproved
	l.account = account
	return nil
}

// Deny closes a pending login permanently.
func (f *Flow) Deny(userCode, account string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, err := f.claimAttempt(userCode, account)
	if err != nil {
		return err
	}
	if l.status != StatusPending {
		return errRejected
	}
	l.status = StatusDenied
	return nil
}

// claimAttempt spends a guessing budget slot BEFORE looking the code up, so a wrong
// guess costs the attacker whether or not it was close. The budget is PER SUBMITTER:
// a global counter would let one attacker lock everyone else out. Caller holds f.mu.
func (f *Flow) claimAttempt(userCode, submitter string) (*login, error) {
	if f.wrong[submitter] >= f.cfg.MaxWrongCodes {
		return nil, errRejected
	}
	l, ok := f.byUser[userCode]
	if !ok || l.consumed || f.now().After(l.expires) {
		f.wrong[submitter]++
		return nil, errRejected
	}
	return l, nil
}

// BoundKey returns the key a pending or just-approved login is bound to. It is how the
// approval path learns WHICH key to bind - the key is never taken from the approving
// request, only from the record made at issue.
func (f *Flow) BoundKey(userCode string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.byUser[userCode]
	if !ok {
		return "", false
	}
	return l.boundKey, true
}

// Describe is what the approval screen may render.
//
// It spends a guess-budget slot exactly like Approve. Without that it would be a free
// existence oracle: an attacker could enumerate user codes through the approval screen
// at zero cost and then spend a single Approve on a confirmed hit, never touching the
// budget the guessing defence relies on.
func (f *Flow) Describe(userCode, viewer string) (Info, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, err := f.claimAttempt(userCode, viewer)
	if err != nil || l.status != StatusPending {
		return Info{}, false
	}
	return Info{UserCode: l.userCode, RequestedAt: l.requested}, true
}

// reapLocked drops logins that can no longer be used. Without it the maps only grow,
// and any signed caller could raise broker memory without bound. Caller holds f.mu.
func (f *Flow) reapLocked() {
	now := f.now()
	for code, l := range f.byUser {
		if l.consumed || now.After(l.expires) {
			delete(f.byUser, code)
			delete(f.byDev, l.deviceCode)
		}
	}
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
