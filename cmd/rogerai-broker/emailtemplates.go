package main

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// emailtemplates.go holds the on-brand transactional templates + the broker-level
// touchpoint helpers that wire them to live events. Every helper is a no-op when the
// mailer is disabled (RESEND_API_KEY unset) AND when the account has no email on file,
// and every send is async (the mailer fires in a goroutine). Templates are the
// "Live Operating Manual" look in email-safe form: a table-based, inline-styled layout
// on a warm-paper ground, the [ (R) ROGERAI ] beacon header, mono numerals/labels for
// machine truth, exactly ONE red glint (the on-air beacon + the kicker), and a
// bulletproof CTA. Each carries a plain-text fallback for deliverability.
//
// EMAIL-CLIENT SAFETY (the hard constraint): Gmail/Outlook/Apple Mail strip <style>
// blocks, flexbox, grid, and external CSS, so the layout is 100% role="presentation"
// tables with INLINE styles only, no JS, a 600px centered container, and a
// table+<a> "bulletproof" button (inline padding+bg, NOT a CSS button). Web fonts do
// NOT load in email, so the site's Space Grotesk / JetBrains Mono are APPROXIMATED with
// system sans + system mono stacks. A hidden preheader feeds the inbox preview line.

// ---- email-safe font stacks (web fonts don't load in mail; approximate them) ------
const (
	fontSans = "-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif"
	fontMono = "'SFMono-Regular',ui-monospace,Menlo,Consolas,'Liberation Mono',monospace"
)

// ---- palette (mirrors web/src/styles/tokens.css: warm paper + ink + one red) -------
const (
	colPaper    = "#FBFBFA" // warm off-white card / receipt fill
	colPaper2   = "#F4F4F2" // outer frame band / evidence fill
	colWhite    = "#FFFFFF" // raised container surface
	colHairline = "#E6E5E1" // the only divider
	colInk900   = "#15140F" // headings, button fill (pure black is banned)
	colInk700   = "#33312B" // body text
	colInk500   = "#6B685F" // secondary text
	colInk400   = "#9A968B" // labels / captions
	colLive     = "#E0231C" // THE red beacon (on air / the whole accent budget)
)

// labelStyle is the shared uppercase mono "operating-manual" label (eyebrows, ref
// keys, receipt headers).
var labelStyle = "font-family:" + fontMono + ";font-size:10px;letter-spacing:0.16em;text-transform:uppercase;color:" + colInk400 + ";"

// emailDoc is the content of one transactional email; renderHTML / renderText turn it
// into the branded HTML shell + the plain-text fallback. bodyHTML is inserted as-is
// (callers escape any account-derived text first); bodyText is the matching plain body.
type emailDoc struct {
	kicker    string // uppercase mono eyebrow, e.g. "PAYOUT SENT" (rendered in red)
	heading   string // the human headline
	preheader string // hidden inbox-preview line
	bodyHTML  string // pre-rendered HTML body (paragraphs, receipts, evidence)
	bodyText  string // matching plain-text body
	ctaLabel  string // bulletproof button label (empty => no button)
	ctaHref   string // bulletproof button target
}

// renderHTML wraps a doc in the shared RogerAI shell: a hidden preheader, the
// [ (R) ROGERAI ] beacon header, a scannable body, an optional bulletproof CTA, and the
// "Over and out" footer. All table-based + inline-styled so it survives Gmail/Outlook.
func renderHTML(d emailDoc) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head>`)
	b.WriteString(`<meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString(`<meta name="color-scheme" content="light dark">`)
	b.WriteString(`<meta name="supported-color-schemes" content="light dark">`)
	b.WriteString(`<title>` + esc(d.heading) + `</title></head>`)
	b.WriteString(`<body style="margin:0;padding:0;background:` + colPaper2 + `;-webkit-text-size-adjust:100%;">`)

	// Hidden preheader: feeds the inbox preview, then padded so the body text doesn't
	// bleed into the preview. mso-hide:all hides it in Outlook too.
	if d.preheader != "" {
		b.WriteString(`<div style="display:none;max-height:0;overflow:hidden;mso-hide:all;font-size:1px;line-height:1px;color:` + colPaper2 + `;opacity:0;">`)
		b.WriteString(esc(d.preheader))
		b.WriteString(strings.Repeat("&#8203;&#847; ", 24))
		b.WriteString(`</div>`)
	}

	// Outer frame.
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:` + colPaper2 + `;">`)
	b.WriteString(`<tr><td align="center" style="padding:28px 14px;">`)

	// Centered 600px container.
	b.WriteString(`<table role="presentation" width="600" cellpadding="0" cellspacing="0" border="0" style="width:100%;max-width:600px;background:` + colWhite + `;border:1px solid ` + colHairline + `;border-radius:10px;">`)

	// Header: [ (R) ROGERAI ] beacon + the manual tagline, with a thin red on-air rule.
	b.WriteString(`<tr><td style="padding:22px 30px 0;">`)
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr>`)
	b.WriteString(`<td align="left" style="font-family:` + fontMono + `;font-size:17px;font-weight:600;letter-spacing:0.20em;color:` + colInk900 + `;white-space:nowrap;">`)
	b.WriteString(`<span style="color:` + colInk400 + `;">[</span>&nbsp;<span style="color:` + colLive + `;">&#9673;</span>&nbsp;ROGERAI&nbsp;<span style="color:` + colInk400 + `;">]</span>`)
	b.WriteString(`</td>`)
	b.WriteString(`<td align="right" style="font-family:` + fontMono + `;font-size:9px;letter-spacing:0.16em;text-transform:uppercase;color:` + colInk400 + `;">The Live Operating Manual</td>`)
	b.WriteString(`</tr></table></td></tr>`)
	// the on-air hairline (mostly ink-hairline with one short red glint at the left)
	b.WriteString(`<tr><td style="padding:16px 30px 0;"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr>`)
	b.WriteString(`<td width="36" style="font-size:0;line-height:0;border-bottom:2px solid ` + colLive + `;">&nbsp;</td>`)
	b.WriteString(`<td style="font-size:0;line-height:0;border-bottom:1px solid ` + colHairline + `;">&nbsp;</td>`)
	b.WriteString(`</tr></table></td></tr>`)

	// Body.
	b.WriteString(`<tr><td style="padding:26px 30px 4px;">`)
	if d.kicker != "" {
		b.WriteString(`<div style="font-family:` + fontMono + `;font-size:11px;font-weight:600;letter-spacing:0.18em;text-transform:uppercase;color:` + colLive + `;margin:0 0 12px;">` + esc(d.kicker) + `</div>`)
	}
	b.WriteString(`<h1 style="margin:0 0 16px;font-family:` + fontSans + `;font-size:22px;line-height:1.28;font-weight:700;letter-spacing:-0.01em;color:` + colInk900 + `;">` + esc(d.heading) + `</h1>`)
	b.WriteString(`<div style="font-family:` + fontSans + `;font-size:15px;line-height:1.6;color:` + colInk700 + `;">` + d.bodyHTML + `</div>`)
	b.WriteString(`</td></tr>`)

	// CTA (bulletproof: table + <a> with inline padding & bg, never a CSS button).
	if cta := button(d.ctaLabel, d.ctaHref); cta != "" {
		b.WriteString(`<tr><td style="padding:8px 30px 4px;">` + cta + `</td></tr>`)
	}

	// Footer.
	b.WriteString(`<tr><td style="padding:24px 30px 26px;">`)
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr><td style="border-top:1px solid ` + colHairline + `;padding-top:18px;">`)
	b.WriteString(`<div style="font-family:` + fontSans + `;font-size:13px;color:` + colInk500 + `;margin:0 0 6px;">Over and out, the RogerAI desk.</div>`)
	b.WriteString(`<div style="font-family:` + fontMono + `;font-size:11px;letter-spacing:0.04em;color:` + colInk400 + `;">`)
	b.WriteString(`<a href="https://rogerai.fyi" target="_blank" style="color:` + colInk500 + `;text-decoration:none;">rogerai.fyi</a>&nbsp;&middot;&nbsp;a two-way radio for GPUs</div>`)
	b.WriteString(`<div style="font-family:` + fontSans + `;font-size:11px;line-height:1.5;color:` + colInk400 + `;margin:12px 0 0;">You are receiving this because you have a RogerAI account on file. Replies reach the RogerAI desk.</div>`)
	b.WriteString(`</td></tr></table></td></tr>`)

	b.WriteString(`</table></td></tr></table></body></html>`)
	return b.String()
}

// renderText builds the plain-text fallback: a radio-voice header, the kicker/heading,
// the body, the CTA as a labelled URL, and the footer. Good plain text matters for
// deliverability and clients that prefer text.
func renderText(d emailDoc) string {
	var b strings.Builder
	b.WriteString("[ (R) ROGERAI ]  -  The Live Operating Manual\n")
	b.WriteString(strings.Repeat("-", 52) + "\n\n")
	if d.kicker != "" {
		b.WriteString(strings.ToUpper(d.kicker) + "\n")
	}
	b.WriteString(d.heading + "\n\n")
	b.WriteString(strings.TrimRight(d.bodyText, "\n") + "\n")
	if d.ctaLabel != "" && d.ctaHref != "" {
		b.WriteString("\n" + d.ctaLabel + ": " + d.ctaHref + "\n")
	}
	b.WriteString("\n" + strings.Repeat("-", 52) + "\n")
	b.WriteString("Over and out, the RogerAI desk\n")
	b.WriteString("rogerai.fyi  -  a two-way radio for GPUs\n")
	b.WriteString("You are receiving this because you have a RogerAI account on file.\n")
	return b.String()
}

// button renders the BULLETPROOF CTA: a one-cell table with a bg-filled <a> whose
// padding lives inline on the link (not a CSS button), so the whole tap target paints
// in every client. Empty label/href => no button.
func button(label, href string) string {
	if label == "" || href == "" {
		return ""
	}
	return `<table role="presentation" cellpadding="0" cellspacing="0" border="0"><tr>` +
		`<td bgcolor="` + colInk900 + `" style="border-radius:5px;">` +
		`<a href="` + esc(href) + `" target="_blank" style="display:inline-block;padding:13px 26px;font-family:` + fontMono +
		`;font-size:12px;font-weight:600;letter-spacing:0.12em;text-transform:uppercase;color:` + colWhite +
		`;text-decoration:none;border-radius:5px;">` + esc(label) + ` &rarr;</a>` +
		`</td></tr></table>`
}

// receipt renders a bordered "receipt" card on the warm-paper fill. hero is optional
// pre-rendered HTML (a <tr> from heroAmount); rows are label/value pairs shown as
// uppercase-mono key + mono value, right-aligned, long values wrap instead of overflow.
func receipt(hero string, rows [][2]string) string {
	var b strings.Builder
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="margin:2px 0 18px;border:1px solid ` + colHairline + `;border-radius:8px;background:` + colPaper + `;">`)
	b.WriteString(hero)
	if len(rows) > 0 {
		b.WriteString(`<tr><td style="padding:12px 18px;"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">`)
		for _, r := range rows {
			b.WriteString(`<tr>`)
			b.WriteString(`<td valign="top" style="font-family:` + fontMono + `;font-size:11px;letter-spacing:0.10em;text-transform:uppercase;color:` + colInk400 + `;padding:5px 10px 5px 0;white-space:nowrap;">` + esc(r[0]) + `</td>`)
			b.WriteString(`<td valign="top" align="right" style="font-family:` + fontMono + `;font-size:12px;color:` + colInk700 + `;padding:5px 0;word-break:break-all;">` + esc(r[1]) + `</td>`)
			b.WriteString(`</tr>`)
		}
		b.WriteString(`</table></td></tr>`)
	}
	b.WriteString(`</table>`)
	return b.String()
}

// heroAmount is the emphasized big-mono figure row inside a receipt (the payout/dispute
// amount). sub (e.g. the credit count) is optional.
func heroAmount(label, big, sub string) string {
	s := `<tr><td style="padding:18px 18px 14px;border-bottom:1px solid ` + colHairline + `;">`
	s += `<div style="` + labelStyle + `margin:0 0 7px;">` + esc(label) + `</div>`
	s += `<div style="font-family:` + fontMono + `;font-size:30px;font-weight:600;line-height:1;color:` + colInk900 + `;">` + esc(big) + `</div>`
	if sub != "" {
		s += `<div style="font-family:` + fontMono + `;font-size:12px;color:` + colInk500 + `;margin:7px 0 0;">` + esc(sub) + `</div>`
	}
	s += `</td></tr>`
	return s
}

// esc is a short alias for HTML-escaping interpolated values.
func esc(s string) string { return html.EscapeString(s) }

// p wraps a line in a paragraph for the HTML body (inherits the body's sans styling).
func p(s string) string { return `<p style="margin:0 0 14px;">` + s + `</p>` }

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
	hero := heroAmount("Payout amount", fmt.Sprintf("$%.2f", usd), fmt.Sprintf("%.4f credits", amountCredits))
	bodyHTML := receipt(hero, [][2]string{{"Transfer ref", transferID}}) +
		p(`Your payout is on its way to your connected account. Funds typically settle within a few business days, depending on your bank.`)
	bodyText := fmt.Sprintf("Payout amount: $%.2f (%.4f credits)\nTransfer ref: %s\n\nYour payout is on its way to your connected account. Funds typically settle within a few business days, depending on your bank.", usd, amountCredits, transferID)
	d := emailDoc{
		kicker:    "Payout sent",
		heading:   "Your payout is on its way",
		preheader: fmt.Sprintf("$%.2f is heading to your connected account.", usd),
		bodyHTML:  bodyHTML,
		bodyText:  bodyText,
		ctaLabel:  "View payouts",
		ctaHref:   "https://rogerai.fyi/payouts.html",
	}
	b.mail.sendEmail(email, subj, renderHTML(d), renderText(d))
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
	hero := heroAmount("Reversed amount", fmt.Sprintf("$%.2f", usd), fmt.Sprintf("%.4f credits", amountCredits))
	bodyHTML := receipt(hero, [][2]string{{"Dispute ref", disputeID}}) +
		p(`A charge that funded part of your earnings was disputed, so the amount above has been reversed from your connected account.`) +
		p(`<span style="color:`+colInk500+`;">If you believe this is in error, reply to this message with the reference above and the RogerAI desk will review it.</span>`)
	bodyText := fmt.Sprintf("Reversed amount: $%.2f (%.4f credits)\nDispute ref: %s\n\nA charge that funded part of your earnings was disputed, so that amount has been reversed from your connected account.\n\nIf you believe this is in error, reply to this message with the reference above and the RogerAI desk will review it.", usd, amountCredits, disputeID)
	d := emailDoc{
		kicker:    "Payout reversed",
		heading:   "A payout was reversed",
		preheader: fmt.Sprintf("$%.2f was reversed after a charge dispute.", usd),
		bodyHTML:  bodyHTML,
		bodyText:  bodyText,
		ctaLabel:  "View payouts",
		ctaHref:   "https://rogerai.fyi/payouts.html",
	}
	b.mail.sendEmail(email, subj, renderHTML(d), renderText(d))
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
	hero := heroAmount("Disputed amount", fmt.Sprintf("$%.2f", usd), fmt.Sprintf("%.4f credits", amountCredits))
	bodyHTML := receipt(hero, [][2]string{{"Dispute ref", disputeID}}) +
		p(`We received a dispute for the charge above, so the corresponding credits have been adjusted on your account.`) +
		p(`<span style="color:`+colInk500+`;">If you did not intend to dispute this charge, reply to this message and we will help sort it out.</span>`)
	bodyText := fmt.Sprintf("Disputed amount: $%.2f (%.4f credits)\nDispute ref: %s\n\nWe received a dispute for the charge above, so the corresponding credits have been adjusted on your account.\n\nIf you did not intend to dispute this charge, reply to this message and we will help sort it out.", usd, amountCredits, disputeID)
	d := emailDoc{
		kicker:    "Charge disputed",
		heading:   "A charge on your account was disputed",
		preheader: fmt.Sprintf("A dispute for $%.2f was received; credits adjusted.", usd),
		bodyHTML:  bodyHTML,
		bodyText:  bodyText,
		ctaLabel:  "View account",
		ctaHref:   "https://rogerai.fyi/account.html",
	}
	b.mail.sendEmail(email, subj, renderHTML(d), renderText(d))
}

// ---- Touchpoint: account warning + ban (enforcement) --------------------------

// emailAccountWarning notifies the owner that a strike was recorded (warn threshold,
// not yet banned). evidence is the human-readable summary of what tripped it.
func (b *broker) emailAccountWarning(email, kind, evidence string, count, banAt int) {
	if !b.mail.enabled() || email == "" {
		return
	}
	subj := "Account warning"
	bodyHTML := receipt("", [][2]string{
		{"Flag", kind},
		{"Strike", fmt.Sprintf("%d of %d", count, banAt)},
	}) +
		p(`We flagged activity on your operator account. This is a warning - one more class of violation will suspend the account.`) +
		evidenceHTML(evidence)
	bodyText := fmt.Sprintf("Flag: %s\nStrike: %d of %d\n\nWe flagged activity on your operator account. This is a warning - one more class of violation will suspend the account.%s", kind, count, banAt, evidenceText(evidence))
	d := emailDoc{
		kicker:    "Account warning",
		heading:   "We flagged activity on your account",
		preheader: fmt.Sprintf("Strike %d of %d on your operator account.", count, banAt),
		bodyHTML:  bodyHTML,
		bodyText:  bodyText,
		ctaLabel:  "View account",
		ctaHref:   "https://rogerai.fyi/account.html",
	}
	b.mail.sendEmail(email, subj, renderHTML(d), renderText(d))
}

// emailAccountBanned notifies the owner that the account was suspended, with the
// evidence summary that tripped it.
func (b *broker) emailAccountBanned(email, reason, evidence string) {
	if !b.mail.enabled() || email == "" {
		return
	}
	subj := "Account suspended"
	bodyHTML := receipt("", [][2]string{{"Reason", reason}}) +
		p(`Your operator account has been suspended. Provider registration, routing, and settlement are now blocked for all nodes under this account.`) +
		evidenceHTML(evidence) +
		p(`<span style="color:`+colInk500+`;">If you believe this is a mistake, reply to this message with the details above and we will review.</span>`)
	bodyText := fmt.Sprintf("Reason: %s\n\nYour operator account has been suspended. Provider registration, routing, and settlement are now blocked for all nodes under this account.%s\n\nIf you believe this is a mistake, reply to this message with the details above and we will review.", reason, evidenceText(evidence))
	d := emailDoc{
		kicker:    "Account suspended",
		heading:   "Your operator account is suspended",
		preheader: "Registration, routing, and settlement are blocked.",
		bodyHTML:  bodyHTML,
		bodyText:  bodyText,
		ctaLabel:  "View account",
		ctaHref:   "https://rogerai.fyi/account.html",
	}
	b.mail.sendEmail(email, subj, renderHTML(d), renderText(d))
}

// evidenceHTML / evidenceText render an optional evidence summary block. Empty in,
// empty out.
func evidenceHTML(evidence string) string {
	if evidence == "" {
		return ""
	}
	return `<div style="` + labelStyle + `margin:18px 0 7px;">Evidence</div>` +
		`<pre style="margin:0 0 6px;padding:14px;background:` + colPaper2 + `;border:1px solid ` + colHairline +
		`;border-radius:8px;font-family:` + fontMono + `;font-size:12px;line-height:1.5;color:` + colInk700 +
		`;white-space:pre-wrap;word-break:break-word;">` + esc(evidence) + `</pre>`
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

	pct := 0.0
	if cap > 0 {
		pct = spend / cap * 100
	}
	hero := heroAmount("Spend this month", fmt.Sprintf("$%.2f", round6(spend)),
		fmt.Sprintf("of $%.2f limit  (%.0f%%)", round6(cap), pct))

	var subj string
	var d emailDoc
	if threshold == "100" {
		subj = "Monthly spend limit reached"
		bodyHTML := receipt(hero, nil) +
			p(`You have reached your monthly spend limit. New paid requests are paused until next month, or until you raise the limit.`) +
			p(`<span style="color:`+colInk500+`;">Raise it from the billing page, or on the CLI with <span style="font-family:`+fontMono+`;">rogerai limit --monthly</span> (or [3] CONFIG).</span>`)
		bodyText := fmt.Sprintf("Spend this month: $%.2f of $%.2f limit (%.0f%%)\n\nYou have reached your monthly spend limit. New paid requests are paused until next month, or until you raise the limit with `rogerai limit --monthly` (or [3] CONFIG).", round6(spend), round6(cap), pct)
		d = emailDoc{
			kicker:    "Spend limit reached",
			heading:   "You hit your monthly spend limit",
			preheader: fmt.Sprintf("$%.2f of $%.2f used - paid requests paused.", round6(spend), round6(cap)),
			bodyHTML:  bodyHTML,
			bodyText:  bodyText,
			ctaLabel:  "Top up",
			ctaHref:   "https://rogerai.fyi/billing.html",
		}
	} else {
		subj = "Monthly spend at 80%"
		bodyHTML := receipt(hero, nil) +
			p(`Heads up: you are close to your monthly spend limit. We will pause paid requests if you reach the cap.`)
		bodyText := fmt.Sprintf("Spend this month: $%.2f of $%.2f limit (%.0f%%)\n\nHeads up: you are close to your monthly spend limit. We will pause paid requests if you reach the cap.", round6(spend), round6(cap), pct)
		d = emailDoc{
			kicker:    "Spend at 80%",
			heading:   "You are near your monthly spend limit",
			preheader: fmt.Sprintf("$%.2f of $%.2f used this month.", round6(spend), round6(cap)),
			bodyHTML:  bodyHTML,
			bodyText:  bodyText,
			ctaLabel:  "Top up",
			ctaHref:   "https://rogerai.fyi/billing.html",
		}
	}
	b.mail.sendEmail(email, subj, renderHTML(d), renderText(d))
}
