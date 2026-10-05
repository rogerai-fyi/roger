package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// Per-account MONTHLY SPEND CAP enforcement (a budget limit, modeled on Groq's "set a
// max you'll pay per month, notify + stop at the limit"). The cap is a $ ceiling on
// captured spend per CALENDAR month, stored per GitHub-linked wallet (internal/store).
// Enforcement is GLOBAL: it sits at the credit-hold gate in relay (tunnel.go), the one
// path every paid consume route (public use, --freq, grant, the [0] agent harness, in-
// channel chat) funnels through. Self-use / free ($0) never reaches it.

// capState is the month-to-date snapshot used for both enforcement and the
// near/at-cap notices surfaced in the response headers + /balance body.
type capState struct {
	cap     float64 // the account's monthly cap ($); 0 = unlimited
	spend   float64 // captured month-to-date spend ($)
	pct     float64 // spend/cap (0 when unlimited)
	near    bool    // crossed the 80% notify threshold (and not yet at the cap)
	atLimit bool    // at/over the cap (spend would be blocked)
}

// monthlyCapState reads a wallet's cap + month-to-date spend and derives the notify
// flags. An unlimited cap (0) reports near=false/atLimit=false.
func (b *broker) monthlyCapState(holder string, now time.Time) capState {
	cap, _ := b.db.MonthlyCapOf(holder)
	spend := b.monthSpend(holder, now)
	return capStateFrom(cap, spend)
}

// capStateFrom derives the cap snapshot from ALREADY-READ cap + spend values (no query),
// so a caller that already has both can build the headers/notices without re-querying.
// An unlimited cap (0) reports near=false/atLimit=false. This is the W2a refactor: it
// lets monthlyCapCheck reuse the spend/cap it already read instead of re-summing them.
func capStateFrom(cap, spend float64) capState {
	s := capState{cap: cap, spend: spend}
	if cap > 0 {
		s.pct = spend / cap
		s.atLimit = spend >= cap
		s.near = !s.atLimit && spend >= cap*store.CapNearThreshold
	}
	return s
}

// monthlyCapCheck enforces the cap for one paid relay request. It returns a non-zero
// HTTP status + message when the request must be REJECTED (the request's worst-case
// cost would push month-to-date spend past the cap), 0 to allow. On allow it also sets
// the near/at-cap notice headers so a client can warn "you've used $X of your $Y
// monthly limit" without a second round-trip. Caller only invokes this on a paid
// (maxCost>0) request, so free/self spend is never blocked.
func (b *broker) monthlyCapCheck(w http.ResponseWriter, holder string, maxCost float64, now time.Time) (int, string) {
	return b.monthlyCapCheckFor(w, nil, holder, maxCost, now)
}

// monthlyCapCheckFor is monthlyCapCheck for a request whose authenticated identity names the
// account to notify: the relay and the voice relay pass it so a threshold notice reaches the
// account's verified address (features/ops/cap_notice_emails.feature). A nil request checks
// the cap and sets the headers but mails nobody.
func (b *broker) monthlyCapCheckFor(w http.ResponseWriter, r *http.Request, holder string, maxCost float64, now time.Time) (int, string) {
	st, msg, _ := b.monthlyCapCheckCap(w, r, holder, maxCost, now)
	return st, msg
}

// monthlyCapCheckCap is monthlyCapCheckFor that also returns the cap it read (0 = no cap), so
// the settle-time notice can reuse it instead of reading the cap a second time.
func (b *broker) monthlyCapCheckCap(w http.ResponseWriter, r *http.Request, holder string, maxCost float64, now time.Time) (int, string, float64) {
	cap, _ := b.db.MonthlyCapOf(holder)
	if cap <= 0 {
		return 0, "", 0 // unlimited (opt-in feature; default off)
	}
	spend := b.monthSpend(holder, now)
	// Reject when even this request's worst-case (the hold amount) would exceed the cap.
	// Using the upper-bound cost mirrors the hold: we never authorize spend we couldn't
	// also have to capture. A request that exactly fits is allowed.
	if spend+maxCost > cap {
		// Surface the at-limit headers on the rejection too, so a client shows the same
		// "$X of $Y" line whether it was warned or hard-stopped.
		setCapHeaders(w, capState{cap: cap, spend: spend, pct: spend / cap, atLimit: true})
		// Flag-gated transactional notice (async, de-duped per holder/month). No-op
		// when RESEND_API_KEY is unset or no email on file.
		b.capNotify(r, holder, "100", spend, cap, now)
		return http.StatusPaymentRequired, fmt.Sprintf(
			"monthly spend limit reached: $%.2f of $%.2f this month - raise it with `roger limit --monthly` (or [3] CONFIG), or wait until next month",
			round6(spend), round6(cap)), cap
	}
	// Allowed: emit the near/at notice headers from the cap + spend we ALREADY read
	// (W2a) - monthlyCapState would re-query both, doubling the work; capStateFrom
	// reuses the values, so the hot paid path runs exactly ONE cap read + ONE spend read.
	cs := capStateFrom(cap, spend)
	setCapHeaders(w, cs)
	// Flag-gated transactional notice on crossing the 80% near-threshold (async,
	// de-duped per holder/month). No-op when RESEND_API_KEY is unset or no email.
	if cs.near {
		b.capNotify(r, holder, "80", spend, cap, now)
	}
	return 0, "", cap
}

// monthlyCapFits reports whether a worst-case amount fits under the holder's monthly cap
// WITHOUT the notice headers or the cap email: the relay uses it to decide whether to size
// its hold for a pricier failover candidate - a refused ceiling is not a refused request.
func (b *broker) monthlyCapFits(holder string, amount float64, now time.Time) bool {
	cap, _ := b.db.MonthlyCapOf(holder)
	if cap <= 0 {
		return true
	}
	return b.monthSpend(holder, now)+amount <= cap
}

// setCapHeaders writes the monthly-budget notice headers. They are always safe to send
// (no secrets) and let the CLI/TUI print "you've used $X of your $Y monthly limit"
// inline. Omitted entirely when the cap is unlimited (no budget to report).
func setCapHeaders(w http.ResponseWriter, s capState) {
	if s.cap <= 0 {
		return
	}
	h := w.Header()
	h.Set("X-RogerAI-Monthly-Cap", ftoa(round6(s.cap)))
	h.Set("X-RogerAI-Monthly-Spend", ftoa(round6(s.spend)))
	h.Set("X-RogerAI-Monthly-Pct", fmt.Sprintf("%.0f", s.pct*100))
	switch {
	case s.atLimit:
		h.Set("X-RogerAI-Monthly-Notice", fmt.Sprintf("monthly limit reached - $%.2f of $%.2f this month", round6(s.spend), round6(s.cap)))
	case s.near:
		h.Set("X-RogerAI-Monthly-Notice", fmt.Sprintf("you've used $%.2f of your $%.2f monthly limit (%.0f%%)", round6(s.spend), round6(s.cap), s.pct*100))
	}
}

// capNoticeAfterSettle runs after a PAID settle: when this request's spend carried the account
// across 80% (or to 100%) of its monthly cap, the account is notified now rather than on its next
// request, and - when the response has not been committed (w non-nil, non-stream) - the near/at
// headers report the spend AFTER this request. Only reads when a cap is set.
//
// knownCap is the cap the request's pre-hold check already read (0 = the account has no cap);
// capUnknown makes this read it.
func (b *broker) capNoticeAfterSettle(w http.ResponseWriter, r *http.Request, holder string, knownCap float64, now time.Time) {
	if b.db == nil || holder == "" {
		return
	}
	cap := knownCap
	if cap == capUnknown {
		cap, _ = b.db.MonthlyCapOf(holder)
	}
	if cap <= 0 {
		return
	}
	cs := capStateFrom(cap, b.monthSpend(holder, now))
	if w != nil {
		setCapHeaders(w, cs)
	}
	switch {
	case cs.atLimit:
		b.capNotify(r, holder, "100", cs.spend, cap, now)
	case cs.near:
		b.capNotify(r, holder, "80", cs.spend, cap, now)
	}
}

// capUnknown marks a cap the caller has not read yet (capNoticeAfterSettle reads it).
const capUnknown = -1.0

// capNotify sends the threshold notice, doing the cheapest checks first so a request above an
// already-notified threshold costs no lookups: the mailer is enabled, then this month's
// claim for the threshold is not already taken, then the account's address is resolved, and
// only then is the claim taken (emailCapNotice), so an account with no address on file never
// spends its once-a-month claim.
func (b *broker) capNotify(r *http.Request, holder, threshold string, spend, cap float64, now time.Time) {
	if !b.mail.enabled() || b.capNoticeClaimed(holder, threshold, now) {
		return
	}
	b.emailCapNotice(b.capNoticeAddress(r, holder), holder, threshold, spend, cap, now)
}

// capNoticeAddress resolves the verified address of the account that owns `holder` from the
// request's AUTHENTICATED identity: a signed CLI key (its bound owner row) or the web session
// (GitHub login, Apple sub, or the code-proven email address). The account wallet alone cannot
// be reversed (Apple and email wallet ids are one-way hashes), and the identity must resolve to
// the SAME account wallet that is being charged, so a request can never direct another
// account's notice. "" = no verified address on file (nothing is sent).
func (b *broker) capNoticeAddress(r *http.Request, holder string) string {
	if r == nil || b.db == nil || holder == "" {
		return ""
	}
	// A sponsored grant bills its OWNER's wallet: the notice goes to the grant's owner, never to
	// the grantee (a bot holding only the secret has no address).
	if tok := grantTokenFromHeader(r); tok != "" {
		sum := sha256.Sum256([]byte(tok))
		if g, found, err := b.db.GrantBySecretHash(hex.EncodeToString(sum[:])); err == nil && found && !g.Revoked {
			if o, ok, _ := b.db.OwnerByPubkey(g.Owner); ok {
				return capNoticeMailable(o, holder)
			}
		}
		return ""
	}
	if pub := r.Header.Get(protocol.HeaderPubkey); pub != "" {
		if o, ok, _ := b.db.OwnerByPubkey(pub); ok {
			return capNoticeMailable(o, holder)
		}
		return ""
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return ""
	}
	login, _, wallet, appleSub, ok := b.verifySessionFull(c.Value)
	if !ok || wallet != holder {
		return ""
	}
	switch {
	case appleSub != "":
		if o, found, _ := b.db.OwnerByAppleSub(appleSub); found {
			return capNoticeMailable(o, holder)
		}
	case isEmailWallet(wallet):
		if o, found, _ := b.db.OwnerByVerifiedEmail(login); found {
			return capNoticeMailable(o, holder)
		}
		return login // the session was minted by accepting a code mailed to this address
	default:
		if o, found, _ := b.db.OwnerByLogin(login); found {
			return capNoticeMailable(o, holder)
		}
		if o, found, _ := b.db.OwnerByVerifiedEmail(login); found {
			return capNoticeMailable(o, holder)
		}
	}
	return ""
}

// capNoticeMailable returns the owner's notice address when the owner is a live account that
// resolves to `holder` and its address on file is proven: by an emailed code (EmailVerifiedAt),
// or reported by the identity provider the account signed in with. An address the account typed
// into its profile (EmailUnproven) is never mailed until it is proven (founder ruling
// 2026-10-04).
func capNoticeMailable(o store.Owner, holder string) string {
	if o.Anonymized || o.Email == "" {
		return ""
	}
	if w, ok := accountWalletForOwner(o); !ok || w != holder {
		return ""
	}
	if o.EmailVerifiedAt != 0 || ((o.GitHubID != 0 || o.AppleSub != "") && !o.EmailUnproven) {
		return o.Email
	}
	return ""
}

// capNoticeClaimed reports whether this month's notice for the threshold was already claimed,
// without taking the claim. It reads the same shared key capNoticeClaim sets (falling back to
// this instance's record when the shared store is unreachable), so the hot path can skip the
// address lookup once the notice has gone out.
func (b *broker) capNoticeClaimed(holder, threshold string, now time.Time) bool {
	key := capNoticeKey(holder, threshold, now)
	if b.shared != nil {
		if _, found, err := b.shared.counterGet(key); err == nil {
			return found
		}
	}
	return b.mail.capNoticeSeen(holder, threshold, now)
}

func capNoticeKey(holder, threshold string, now time.Time) string {
	return "capnotice:" + holder + "|" + threshold + "|" + now.UTC().Format("2006-01")
}

// capNoticeClaim claims the once-per-(holder, threshold, month UTC) notice. The claim lives in the
// shared store so two instances, or one after a restart, never send it twice; when the shared
// store is unreachable it falls back to this instance's memory (at most once per instance).
func (b *broker) capNoticeClaim(holder, threshold string, now time.Time) bool {
	key := capNoticeKey(holder, threshold, now)
	if b.shared != nil {
		if set, err := b.shared.setIfAbsent(key, "1", 40*24*time.Hour); err == nil {
			return set
		}
	}
	return b.mail.capNoticeOnce(holder, threshold, now)
}
