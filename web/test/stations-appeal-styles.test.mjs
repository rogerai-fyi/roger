// Every class the appeal form uses must exist in the stylesheet bundle the stations page
// actually loads (build.mjs bundles stations.html with account-base + stations.css only).
// Run: node --test test/stations-appeal-styles.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { src } from "./_pagevm.mjs";

const html = src("stations.html");
const block = html.slice(html.indexOf('id="stAppeal"'), html.indexOf("</div>", html.indexOf('id="appealMsg"')));
const used = [...block.matchAll(/class="([^"]+)"/g)].flatMap((m) => m[1].split(/\s+/));
// build.mjs: stations.html = tokens + base + components + account-base + stations
const bundle = ["tokens", "base", "components", "account-base", "stations"].map((f) => src(`styles/${f}.css`)).join("\n");

test("the appeal form's classes are defined in the bundle stations.html loads", () => {
  assert.ok(used.length > 0);
  for (const c of used.filter((c) => /^(st|ac|lt)-/.test(c))) {
    assert.match(bundle, new RegExp("\\." + c.replace(/[-_]/g, "[-_]") + "\\b"), `.${c} is not styled in account-base.css + stations.css`);
  }
});
