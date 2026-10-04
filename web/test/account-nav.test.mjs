// The account dropdown (session.js) and the account-page footer sub-nav (accountnav.html)
// must list the same pages in the same order, and Stations + Keys must be reachable from
// the signed-in menus (they were only reachable from deep inside the Account page).
// Run: node --test test/account-nav.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { src } from "./_pagevm.mjs";

const menu = [...src("js/session.js").matchAll(/href: "(\/[a-z]+\.html)"/g)].map((m) => m[1]);
const subnav = [...src("_partials/accountnav.html").matchAll(/<a href="(\/[a-z]+\.html)"/g)].map((m) => m[1]);

test("Stations is in the account dropdown", () => assert.ok(menu.includes("/stations.html")));
test("Stations is in the footer sub-nav", () => assert.ok(subnav.includes("/stations.html")));
test("dropdown and sub-nav agree on order (Base Station is dropdown-only)", () => {
  assert.deepEqual(menu.filter((h) => h !== "/private.html"), subnav);
});
test("API keys stays reachable: the dropdown links it", () => assert.ok(menu.includes("/keys.html")));
