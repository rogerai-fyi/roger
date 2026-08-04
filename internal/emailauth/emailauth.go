// Package emailauth is first-party sign-in: a RogerAI account of our own, entered with a
// code we mail.
//
// WHY IT EXISTS. Every identity in the system used to be borrowed - an owner row keyed on
// a GitHub id or an Apple sub - so a person holding neither could not sign in at all, two
// third parties could lock a customer out of a paid account holding a wallet balance, and
// a provider outage was a total sign-in outage.
//
// WHY A MAILED CODE AND NOT A PASSWORD. A password we do not store cannot leak, be reused
// from another site's breach, be stuffed, or need a reset flow - and a reset flow is
// itself a mailed-code flow, so a password would mean building both and defending both.
//
// WHAT THIS PACKAGE DELIBERATELY DOES NOT KNOW. It never consults an account store. It
// cannot tell a known address from an unknown one, which is the strongest possible form of
// "the response reveals nothing": there is no branch to leak, no second code path, and no
// timing difference, because the information simply is not here. Deciding what an accepted
// address MEANS - create an account, resolve an existing one, refuse to link - belongs to
// the caller, after this package has said the person holds the address.
package emailauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/mail"
	"strings"
	"sync"
	"time"
)

// defaultCodeLen is six digits: what a person can read off a phone and type into a
// terminal. Six digits is only a million possibilities, which is why the per-address
// attempt budget below is what actually makes guessing infeasible - not the code length.
const defaultCodeLen = 6

// maxAddressLen bounds what we will even look at. RFC 5321 caps a path at 256 octets; a
// longer input is not a real address and has no business reaching a mail provider.
const maxAddressLen = 254

var (
	// ErrInvalidAddress means the input is not something we can mail. It is returned
	// BEFORE anything is enqueued or recorded.
	ErrInvalidAddress = errors.New("that does not look like an email address we can reach")

	// ErrRejected is the UNIFORM refusal. A wrong code, an expired code, a spent code, an
	// address that never had one, and a spent attempt budget all return exactly this: a
	// distinguishable error would tell an attacker which of those they had found.
	ErrRejected = errors.New("that code is not valid")

	// ErrRateLimited means the caller asked too often. It is distinct from ErrRejected
	// because it is not a statement about any code.
	ErrRateLimited = errors.New("too many sign-in requests - wait a moment and try again")

	// ErrUnavailable means the store could not be reached. Never conflate it with
	// ErrRejected: telling a person their address is unusable because our backend blinked
	// sends them to support with the wrong problem.
	ErrUnavailable = errors.New("sign-in is temporarily unavailable")
)

// Config tunes the flow. All of it is policy the broker owns.
type Config struct {
	TTL           time.Duration
	MaxWrongCodes int
	// RequestsPerAddress bounds how often ONE address may be mailed. Without it, anyone
	// can use our mailer to flood a person's inbox and our sending domain wears the spam
	// complaints.
	RequestsPerAddress int
	// RequestsPerSource bounds one sender across DIFFERENT addresses. The per-address
	// limit alone does not stop somebody walking an address list, which is both a
	// mail-bomb amplifier and the reconnaissance half of an enumeration attack.
	RequestsPerSource int
	// SubmitsPerSource bounds code guessing from one sender across different addresses.
	SubmitsPerSource int
	Window           time.Duration
}

func (c *Config) withDefaults() {
	if c.TTL <= 0 {
		c.TTL = 10 * time.Minute
	}
	if c.MaxWrongCodes <= 0 {
		c.MaxWrongCodes = 5
	}
	if c.RequestsPerAddress <= 0 {
		c.RequestsPerAddress = 5
	}
	if c.RequestsPerSource <= 0 {
		c.RequestsPerSource = 20
	}
	if c.SubmitsPerSource <= 0 {
		c.SubmitsPerSource = 30
	}
	if c.Window <= 0 {
		c.Window = time.Hour
	}
}

// Flow is the sign-in state machine.
type Flow struct {
	cfg    Config
	store  Store
	mu     sync.Mutex
	offset time.Duration
}

// New builds a flow over the in-process store.
func New(cfg Config) *Flow { return NewWithStore(cfg, NewMemStore()) }

// NewWithStore builds a flow over an explicit store, so pending codes can be shared across
// broker instances exactly as pending device logins are.
func NewWithStore(cfg Config, store Store) *Flow {
	cfg.withDefaults()
	if store == nil {
		store = NewMemStore()
	}
	return &Flow{cfg: cfg, store: store}
}

func (f *Flow) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return time.Now().Add(f.offset)
}

// advance moves the flow's clock. Test-only seam: the alternative is sleeping through real
// expiry windows, which makes the suite slow and flaky.
func (f *Flow) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.offset += d
}

// Normalize is how an address becomes the one thing we store and compare.
//
// It trims surrounding whitespace and lowercases, and STOPS THERE. It deliberately does
// not strip plus-tags or dots: those rules belong to individual providers, differ between
// them, and change without notice. Collapsing "a.b+x@example.com" into "ab@example.com"
// would bake one provider's rules into our identity model, and the collapsing direction is
// the dangerous one - get it wrong and two different people share one account.
// It trims only SPACES AND TABS, never newlines. strings.TrimSpace would quietly strip a
// trailing CRLF and hand ValidAddress a clean address, so a header-injection attempt would
// be silently normalized into acceptance instead of refused. A control character in an
// address is never legitimate input, and the conservative answer is to refuse the input we
// were actually given rather than to guess at a safe version of it.
func Normalize(addr string) string {
	return strings.ToLower(strings.Trim(addr, " \t"))
}

// ValidAddress reports whether we are willing to mail this.
//
// The CRLF check is not redundant with the parser: a newline in an address is a mail
// header-injection attempt, and the whole point is that it never reaches the code that
// builds a provider request body.
func ValidAddress(addr string) bool {
	if addr == "" || len(addr) > maxAddressLen {
		return false
	}
	if strings.ContainsAny(addr, "\r\n\x00") {
		return false
	}
	parsed, err := mail.ParseAddress(addr)
	if err != nil || parsed.Address != addr {
		return false
	}
	at := strings.LastIndex(addr, "@")
	if at <= 0 || at == len(addr)-1 {
		return false
	}
	domain := addr[at+1:]
	// A domain with no dot is a local name (localhost, a container alias). Mail to it
	// never leaves the host, so accepting one lets an address exist that no human can
	// ever prove they hold.
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	return true
}

func hashAddr(addr string) string { return sha256hex("addr:" + addr) }
func hashCode(addr, code string) string {
	// The address is mixed in so a code is only ever valid for the address it was mailed
	// to, even if two addresses happen to draw the same six digits at the same moment.
	return sha256hex("code:" + addr + ":" + code)
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func unavailable(err error) error {
	if errors.Is(err, ErrUnavailable) {
		return ErrUnavailable
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}

// Request issues a code for addr and returns it for mailing. The caller mails it; this
// package never touches a mail provider, so a test can drive the whole state machine
// without one.
//
// A code that could not be RECORDED is never returned, because a code we cannot check is a
// code the person will type in vain.
func (f *Flow) Request(addr, source string) (string, error) {
	addr = Normalize(addr)
	if !ValidAddress(addr) {
		return "", ErrInvalidAddress
	}
	ah := hashAddr(addr)

	ok, err := f.store.AllowRequest(ah, source, f.cfg.RequestsPerAddress, f.cfg.RequestsPerSource, f.cfg.Window, f.now())
	if err != nil {
		return "", unavailable(err)
	}
	if !ok {
		return "", ErrRateLimited
	}

	code, err := randomCode(defaultCodeLen)
	if err != nil {
		return "", err
	}
	now := f.now()
	// Put REPLACES any existing record for the address, which is what retires the previous
	// code: a person who requests twice because the first mail was slow otherwise leaves a
	// live spare credential sitting in their inbox.
	rec := Record{
		AddrHash: ah,
		CodeHash: hashCode(addr, code),
		Issued:   now,
		Expires:  now.Add(f.cfg.TTL),
	}
	if err := f.store.Put(rec); err != nil {
		return "", unavailable(err)
	}
	return code, nil
}

// Submit checks a code and, on success, returns the canonical address whose holder has
// just been proven. What that address MEANS is the caller's decision.
func (f *Flow) Submit(addr, code, source string) (string, error) {
	addr = Normalize(addr)
	if !ValidAddress(addr) {
		// Still uniform: an invalid address must not be a cheaper way to probe than a
		// valid one that has no code.
		return "", ErrRejected
	}
	ah := hashAddr(addr)

	// The per-source submission budget is spent FIRST, so walking an address list costs
	// the attacker whether or not any of the addresses exist.
	ok, err := f.store.AllowSubmit(source, f.cfg.SubmitsPerSource, f.cfg.Window, f.now())
	if err != nil {
		return "", unavailable(err)
	}
	if !ok {
		return "", ErrRejected
	}

	rec, found, err := f.store.ByAddress(ah)
	if err != nil {
		return "", unavailable(err)
	}
	if !found || rec.Attempts >= f.cfg.MaxWrongCodes || f.now().After(rec.Expires) {
		return "", ErrRejected
	}

	// Constant time: an early return on the first differing digit leaks how much of the
	// code a guess got right, which turns a million-possibility space into six
	// ten-possibility ones.
	if subtle.ConstantTimeCompare([]byte(rec.CodeHash), []byte(hashCode(addr, code))) != 1 {
		if _, err := f.store.Penalize(ah, f.cfg.TTL); err != nil {
			return "", unavailable(err)
		}
		return "", ErrRejected
	}

	// Spending the code is the CAS itself rather than a read followed by a delete, so of N
	// concurrent submissions of the same correct code exactly one is accepted.
	won, err := f.store.Consume(rec)
	if err != nil {
		return "", unavailable(err)
	}
	if !won {
		return "", ErrRejected
	}
	return addr, nil
}

// randomCode draws from the operating-system random source. crypto/rand, not math/rand:
// a predictable sign-in code is not a credential at all.
func randomCode(n int) (string, error) {
	out := make([]byte, n)
	ten := big.NewInt(10)
	for i := range out {
		d, err := rand.Int(rand.Reader, ten)
		if err != nil {
			return "", err
		}
		out[i] = byte('0' + d.Int64())
	}
	return string(out), nil
}
