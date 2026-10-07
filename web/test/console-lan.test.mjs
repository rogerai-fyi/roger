// The console's LAN marks (features/lan/ui_visibility.feature, money_and_accounts.feature):
// the shipped internal/webui/assets/console-lan.js is EXECUTED over a minimal document.
import { test } from "node:test";
import assert from "node:assert/strict";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const api = require("../../internal/webui/assets/console-lan.js");

function doc() {
  const badge = { hidden: true, textContent: "" };
  return { badge, d: { getElementById: (id) => (id === "top-lan" ? badge : null) } };
}

test("LAN mode: the top bar shows LAN and the cost line says nothing is billed", () => {
  const { badge, d } = doc();
  api.renderLAN(d, { lan: true });
  assert.equal(badge.hidden, false);
  assert.equal(badge.textContent, "LAN");
  assert.equal(api.costLine({ lan: true }), "local, nothing billed");
});

test("outside LAN mode: no badge, and the share settles on the broker", () => {
  const { badge, d } = doc();
  badge.hidden = false;
  badge.textContent = "LAN";
  api.renderLAN(d, { lan: false });
  assert.equal(badge.hidden, true);
  assert.equal(badge.textContent, "");
  assert.equal(api.costLine({}), "settles on the broker");
  assert.equal(api.costLine(null), "settles on the broker");
});

test("a page without the badge element is left alone", () => {
  assert.doesNotThrow(() => api.renderLAN({ getElementById: () => null }, { lan: true }));
});
