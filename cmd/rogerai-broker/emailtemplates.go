package main

import (
	"fmt"
	"html"
	"time"
)

// emailtemplates.go holds the on-brand transactional templates + the broker-level
// touchpoint helpers that wire them to live events. Every helper is a no-op when the
// mailer is disabled (RESEND_API_KEY unset) AND when the account has no email on file,
// and every send is async (the mailer fires in a goroutine). Templates are monochrome,
// in the radio voice: a plain header, the message, and a footer link. Each carries a
// plain-text fallback.

// brandHTML wraps the body in the shared RogerAI shell: a monochrome header, the
// message, and a footer link to the operating manual. heading is escaped here;
// bodyHTML is inserted as-is (callers escape any account-derived text first).
func brandHTML(heading, bodyHTML string) string {
	return `<!doctype html><html><body style="margin:0;padding:0;background:#0a0a0a;">` +
		`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#0a0a0a;">` +
		`<tr><td align="center" style="padding:32px 16px;">` +
		`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" ` +
		`style="max-width:520px;background:#111;border:1px solid #2a2a2a;border-radius:8px;` +
		`font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;color:#e8e8e8;">` +
		`<tr><td style="padding:20px 24px;border-bottom:1px solid #2a2a2a;">` +
		`<div style="font-size:14px;letter-spacing:3px;color:#9a9a9a;">&#9673; ROGERAI</div></td></tr>` +
		`<tr><td style="padding:24px;">` +
		`<h1 style="margin:0 0 16px;font-size:18px;font-weight:600;color:#fff;">` + esc(heading) + `</h1>` +
		`<div style="font-size:14px;line-height:1.6;color:#cfcfcf;">` + bodyHTML + `</div></td></tr>` +
		`<tr><td style="padding:16px 24px;border-top:1px solid #2a2a2a;font-size:12px;color:#7a7a7a;">` +
		`Over and out, the RogerAI desk &middot; ` +
		`<a href="https://rogerai.fyi" style="color:#9a9a9a;">rogerai.fyi</a></td></tr>` +
		`</table></td></tr></table></body></html>`
}

// brandText wraps a plain-text body with the radio-voice header + footer.
func brandText(heading, body string) string {
	return "ROGERAI\n\n" + heading + "\n\n" + body + "\n\nOver and out, the RogerAI desk\nhttps://rogerai.fyi\n"
}

// esc is a short alias for HTML-escaping interpolated values.
func esc(s string) string { return html.EscapeString(s) }

// p wraps a line in a paragraph for the HTML body.
func p(s string) string { return `<p style="margin:0 0 12px;">` + s + `</p>` }

// emailOf resolves the GitHub email for an account/wallet pubkey. Returns "" when
// there is no owner binding, no email on file, or the store is unavailable - the
// caller then skips the send.
func (b *broker) emailOf(pubkey string) string {
	if b.db == nil || pubkey == "" {
		return ""
	}
	if o, ok, _ := b.db.OwnerByPubkey(pubkey); ok {
		return o.Email
	}
	return ""
}

// ---- Touchpoint: payout sent --------------------------------------------------

// emailPayoutSent notifies the operator that a payout transfer completed. No-op when
// the mailer is disabled or the owner has no email on file.
func (b *broker) emailPayoutSent(email string, amountCredits float64, transferID string) {
	if !b.mail.enabled() || email == "" {
		return
	}
	usd := round6(amountCredits * b.bill.creditUSD)
	subj := "Payout sent"
	body := fmt.Sprintf("Your payout of $%.2f (%.4f credits) is on its way to your connected account.", usd, amountCredits)
	ref := "Transfer reference: " + transferID
	b.mail.sendEmail(email, subj,
		brandHTML(subj, p(esc(body))+p(`<span style="color:#7a7a7a;">`+esc(ref)+`</span>`)),
		brandText(subj, body+"\n\n"+ref))
}

// ---- Touchpoint: payout held / reversed (dispute clawback) --------------------

// emailPayoutReversed notifies the operator that a paid-out earning was clawed back
// because the funding charge was disputed. No-op when disabled or no email.
func (b *broker) emailPayoutReversed(email string, amountCredits float64, disputeID string) {
	if !b.mail.enabled() || email == "" {
		return
	}
	usd := round6(amountCredits * b.bill.creditUSD)
	subj := "Payout reversed - charge disputed"
	body := fmt.Sprintf("A charge that funded $%.2f (%.4f credits) of your earnings was disputed, so that amount has been reversed from your connected account.", usd, amountCredits)
	ref := "Dispute reference: " + disputeID
	tail := "If you believe this is in error, reply to this message with the reference above."
	b.mail.sendEmail(email, subj,
		brandHTML(subj, p(esc(body))+p(`<span style="color:#7a7a7a;">`+esc(ref)+`</span>`)+p(esc(tail))),
		brandText(subj, body+"\n\n"+ref+"\n\n"+tail))
}

// ---- Touchpoint: charge dispute opened (consumer side) ------------------------

// emailDisputeOpened notifies the consumer whose charge was disputed. No-op when
// disabled or no email.
func (b *broker) emailDisputeOpened(email string, amountCredits float64, disputeID string) {
	if !b.mail.enabled() || email == "" {
		return
	}
	usd := round6(amountCredits * b.bill.creditUSD)
	subj := "A charge on your account was disputed"
	body := fmt.Sprintf("We received a dispute for $%.2f (%.4f credits) on a charge to your account. The corresponding credits have been adjusted.", usd, amountCredits)
	ref := "Dispute reference: " + disputeID
	tail := "If you did not intend to dispute this charge, reply to this message and we will help sort it out."
	b.mail.sendEmail(email, subj,
		brandHTML(subj, p(esc(body))+p(`<span style="color:#7a7a7a;">`+esc(ref)+`</span>`)+p(esc(tail))),
		brandText(subj, body+"\n\n"+ref+"\n\n"+tail))
}

// ---- Touchpoint: account warning + ban (enforcement) --------------------------

// emailAccountWarning notifies the owner that a strike was recorded (warn threshold,
// not yet banned). evidence is the human-readable summary of what tripped it.
func (b *broker) emailAccountWarning(email, kind, evidence string, count, banAt int) {
	if !b.mail.enabled() || email == "" {
		return
	}
	subj := "Account warning"
	body := fmt.Sprintf("We flagged activity on your operator account (%s). This is strike %d of %d - one more class of violation will suspend the account.", kind, count, banAt)
	b.mail.sendEmail(email, subj,
		brandHTML(subj, p(esc(body))+evidenceHTML(evidence)),
		brandText(subj, body+evidenceText(evidence)))
}

// emailAccountBanned notifies the owner that the account was suspended, with the
// evidence summary that tripped it.
func (b *broker) emailAccountBanned(email, reason, evidence string) {
	if !b.mail.enabled() || email == "" {
		return
	}
	subj := "Account suspended"
	body := fmt.Sprintf("Your operator account has been suspended (%s). Provider registration, routing, and settlement are now blocked for all nodes under this account.", reason)
	tail := "If you believe this is a mistake, reply to this message with the details below and we will review."
	b.mail.sendEmail(email, subj,
		brandHTML(subj, p(esc(body))+evidenceHTML(evidence)+p(esc(tail))),
		brandText(subj, body+evidenceText(evidence)+"\n\n"+tail))
}

// evidenceHTML / evidenceText render an optional evidence summary block. Empty in,
// empty out.
func evidenceHTML(evidence string) string {
	if evidence == "" {
		return ""
	}
	return `<pre style="margin:12px 0 0;padding:12px;background:#0a0a0a;border:1px solid #2a2a2a;` +
		`border-radius:6px;font-size:12px;color:#9a9a9a;white-space:pre-wrap;">` + esc(evidence) + `</pre>`
}

func evidenceText(evidence string) string {
	if evidence == "" {
		return ""
	}
	return "\n\nEvidence:\n" + evidence
}

// ---- Touchpoint: monthly spend-cap 80% / 100% ---------------------------------

// emailCapNotice notifies the holder that they crossed a monthly-budget threshold
// ("80" near, "100" at limit). De-duped per (holder, threshold, month) so the hot
// relay path emits at most one email per threshold per month. No-op when disabled or
// no email.
func (b *broker) emailCapNotice(holder string, threshold string, spend, cap float64, now time.Time) {
	if !b.mail.enabled() {
		return
	}
	email := b.emailOf(holder)
	if email == "" {
		return
	}
	if !b.mail.capNoticeOnce(holder, threshold, now) {
		return
	}
	var subj, body string
	if threshold == "100" {
		subj = "Monthly spend limit reached"
		body = fmt.Sprintf("You've reached your monthly spend limit: $%.2f of $%.2f. New paid requests are paused until next month, or until you raise the limit with `rogerai limit --monthly` (or [3] CONFIG).", round6(spend), round6(cap))
	} else {
		subj = "Monthly spend at 80%"
		pct := 0.0
		if cap > 0 {
			pct = spend / cap * 100
		}
		body = fmt.Sprintf("Heads up: you've used $%.2f of your $%.2f monthly limit (%.0f%%). We'll pause paid requests if you reach the cap.", round6(spend), round6(cap), pct)
	}
	b.mail.sendEmail(email, subj,
		brandHTML(subj, p(esc(body))),
		brandText(subj, body))
}
