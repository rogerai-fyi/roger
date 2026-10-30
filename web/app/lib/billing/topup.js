// The top-up amount rules, in one place (legacy: the chosenUsd/reflect/click trio in
// web/src/js/billing.js). The bounds are the broker's (internal/client/topup_amount.go,
// pinned by test/topup-bounds.test.mjs): a top-up is at least MIN_USD, at most MAX_USD and a
// whole number of cents. A sub-cent amount is refused rather than rounded into a different
// charge, the same rule the CLI, the TUI and the broker apply: rounding here is how a person
// ends up paying a number they did not type.
export const MIN_USD = 1;
export const MAX_USD = 999999.99;

// "$1", "$999,999.99": a whole dollar prints plain, anything else with cents.
export const money = (v) => "$" + v.toLocaleString("en-US", { minimumFractionDigits: Number.isInteger(v) ? 0 : 2 });

export const wholeCents = (usd) => Math.abs(usd * 100 - Math.round(usd * 100)) <= 1e-6;

// The guard that stops the request: { ok: true } or { ok: false, msg } with the reason.
export function check(usd) {
  if (!isFinite(usd) || usd < MIN_USD) return { ok: false, msg: "enter an amount of " + money(MIN_USD) + " or more" };
  // Both ends, not just the floor: without this a typed 1000000 rendered "Add $1,000,000.00"
  // and only failed after a broker round-trip.
  if (usd > MAX_USD) return { ok: false, msg: "the maximum top-up is " + money(MAX_USD) };
  if (Math.abs(usd * 100 - Math.round(usd * 100)) > 1e-6) return { ok: false, msg: "amount must be a whole number of cents" };
  return { ok: true };
}

// What the action button and the line under it say for an amount: the commitment is
// explicit before the redirect ("Add $20.00" reads safer than "Add money"). Shows what was
// typed, never a rounded version of it: the button says nothing about an amount it will not
// send, so a typed 10.005 never reads "Add $10.01" (nor "Add $10.00").
export function label(usd, cr) {
  if (isFinite(usd) && usd >= MIN_USD && usd <= MAX_USD && wholeCents(usd)) return { button: "Add " + cr(usd), note: "Adds " + cr(usd) + " to your wallet balance." };
  if (isFinite(usd) && usd > MAX_USD) return { button: "Add money", note: "The maximum top-up is " + money(MAX_USD) + "." };
  if (isFinite(usd) && usd >= MIN_USD) return { button: "Add money", note: "Whole cents only." };
  return { button: "Add money", note: null };
}
