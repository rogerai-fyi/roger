// Regression locks for the founder's billing/account money-flow work:
//   1) the wallet total is prominent on the account page;
//   2) the billing Wallet has a "?" that opens an accessible money-flow modal;
//   3) the modal carries the stable #ledger-demo slot (the animated-explainer
//      drop-in target a separate workstream depends on) and clears up the two
//      numbers people confuse (Balance vs "You paid").
// Static-content assertions over web/src (no build/DOM needed), matching the
// node:fs test infra. Run: node --test test/billing-help.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const SRC = path.join(path.dirname(fileURLToPath(import.meta.url)), "../src");
const read = (p) => readFileSync(path.join(SRC, p), "utf8");

const account = read("account.html");
const billing = read("billing.html");
const helpJs = read("js/billing-help.js");

test("account: the wallet balance is a prominent hero, with a single #balance", () => {
  assert.match(account, /class="ac-wallet"/, "wallet hero plate present");
  assert.match(account, /class="ac-wallet__amt"[^>]*id="balance"/, "balance is the big hero figure");
  // id must stay unique now that #balance moved out of the Profile stats.
  const n = (account.match(/id="balance"/g) || []).length;
  assert.equal(n, 1, `exactly one #balance (found ${n})`);
  // it should point people to where they top up / read the ledger.
  assert.match(account, /href="\/billing\.html"/, "links to billing");
});

test("billing: a '?' affordance near Wallet opens the modal, keyboard-reachable", () => {
  // the "?" lives in the Wallet heading and is a real <button> (Enter/Space).
  assert.match(billing, /<h2>Wallet<button type="button" class="bx-help-btn"/, "? button in the Wallet h2");
  // every opener carries the dialog wiring.
  assert.match(billing, /id="walletHelp"[^>]*data-help-open/, "? button opens the help");
  assert.match(billing, /aria-controls="ledgerModal"/, "trigger points at the modal");
  assert.match(billing, /aria-haspopup="dialog"/, "trigger announces a dialog");
  // a secondary, more discoverable text trigger too.
  assert.match(billing, /class="bx-help-link"[^>]*data-help-open/, "fine-print trigger");
  // the script is wired.
  assert.match(billing, /<script src="js\/billing-help\.js">/, "billing-help.js loaded");
});

test("billing: the modal is an accessible dialog", () => {
  assert.match(billing, /id="ledgerModal"/, "modal element present");
  assert.match(billing, /role="dialog"\s+aria-modal="true"/, "dialog + modal semantics");
  assert.match(billing, /aria-labelledby="ledgerTitle"/, "labelled by its title");
  assert.match(billing, /id="ledgerScrim"/, "backdrop present (click-to-close target)");
  assert.match(billing, /id="ledgerClose"[^>]*aria-label="Close"/, "labelled close control");
});

test("billing: the #ledger-demo slot is the stable animated-explainer target", () => {
  // this id is a contract with the animation workstream - it must not drift.
  assert.match(billing, /id="ledger-demo"/, "#ledger-demo slot present");
  assert.match(billing, /class="bx-demo__cap"/, "captioned frame around the slot");
});

test("billing: the modal explains the flow and the two confused numbers", () => {
  assert.match(billing, /append-only ledger/i, "names the append-only ledger");
  assert.match(billing, /re-sums.+ledger.+re-derive/is, "explains Verified as a re-sum drift check");
  // the Balance vs You-paid clarification the founder called out.
  assert.match(billing, /Balance/);
  assert.match(billing, /You paid/);
  assert.match(billing, /per-turn/i, "frames 'You paid' as a per-turn figure");
});

test("billing-help.js: focus trap, Esc, backdrop and aria-expanded sync", () => {
  assert.match(helpJs, /"Escape"/, "Esc closes");
  assert.match(helpJs, /"Tab"/, "Tab is trapped");
  assert.match(helpJs, /aria-expanded/, "syncs aria-expanded on triggers");
  assert.match(helpJs, /lastFocus/, "restores focus to the opener on close");
  assert.match(helpJs, /data-help-open/, "delegated open on any trigger");
});
