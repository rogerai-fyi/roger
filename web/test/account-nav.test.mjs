// The account dropdown (session.js) and the account-page footer sub-nav (accountnav.html)
// must list the same pages in the same order, and Stations + Keys must be reachable from
// the signed-in menus (they were only reachable from deep inside the Account page).
// Run: node --test test/account-nav.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { component, source } from "./_html.mjs";
const src = source;   // a page or a script as authored (test/_html.mjs)

const menu = [...src("js/session.js").matchAll(/href: "(\/[a-z]+\.html)"/g)].map((m) => m[1]);
const subnav = [...component("AccountNav").matchAll(/"(\/[a-z]+\.html)"/g)].map((m) => m[1]);   // the component's page list, in order

test("Stations is in the account dropdown", () => assert.ok(menu.includes("/stations.html")));
test("Stations is in the footer sub-nav", () => assert.ok(subnav.includes("/stations.html")));
test("dropdown and sub-nav agree on order (Base Station is dropdown-only)", () => {
  assert.deepEqual(menu.filter((h) => h !== "/private.html"), subnav);
});
test("API keys stays reachable: the dropdown links it", () => assert.ok(menu.includes("/keys.html")));
