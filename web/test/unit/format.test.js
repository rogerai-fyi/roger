import { describe, it, expect } from "vitest";
import { cr, num, kfmt, ago, shortId, priceStr, parseDays, daysStr } from "#lib/console/format.js";
import { when, rel } from "#lib/format.js";
import { readFileSync, readdirSync } from "node:fs";
import { resolve } from "node:path";

describe("console cell formatters (ported from console.js)", () => {
  it("money: canonical signed USD, dash for non-numbers", () => {
    expect(cr(0)).toBe("$0.00");
    expect(cr(1.5)).toBe("$1.50");
    expect(cr("x")).toBe("-");
    expect(cr(NaN)).toBe("-");
  });
  it("counts: compact k/M via the shared formatter", () => {
    expect(num(1234567)).toBe("1,234,567");
    expect(num(undefined)).toBe("-");
    expect(kfmt(999)).toBe("999");
    expect(kfmt(12000)).toMatch(/^12k$/);
  });
  it("relative time buckets", () => {
    const now = 1_800_000_000 * 1000;
    expect(ago(now / 1000 - 12, now)).toBe("12s");
    expect(ago(now / 1000 - 240, now)).toBe("4m");
    expect(ago(now / 1000 - 3 * 3600, now)).toBe("3h");
    expect(ago(now / 1000 - 2 * 86400, now)).toBe("2d");
    expect(ago(0, now)).not.toMatch(/[smhd]$/);   // a date, not a bucket
  });
  it("receipt id head fragment", () => {
    expect(shortId("")).toBe("-");
    expect(shortId("abcdefghijkl")).toBe("abcdefghijkl");
    expect(shortId("abcdefghijklmnop")).toBe("abcdefghijkl");
  });
  it("prices and schedule days round-trip", () => {
    expect(priceStr(0)).toBe("0");
    expect(priceStr(0.5)).toBe("0.5");
    expect(priceStr(2)).toBe("2");
    expect(parseDays("1, 3,3, 9, x, 6")).toEqual([1, 3, 6]);
    expect(daysStr([])).toBe("");
    expect(daysStr([0, 6])).toBe("0,6");
  });
});

import RogerFmt from "#lib/fmt.js";
describe("RogerFmt identity (from test/login-loop.test.mjs and test/dashboard-states.test.mjs)", () => {
  it("every island that shows the signed-in name uses the shared handle formatter (ported from login-loop.test.mjs)", () => {
    const lib = resolve("app/lib");   // vitest runs from web/
    const files = readdirSync(lib, { recursive: true }).filter((f) => f.endsWith(".svelte")).map((f) => resolve(lib, f));
    // a line that only tests for a login (`if (acct && (acct.github_login || ...))`) shows nothing
    const shows = (line) => /github_login/.test(line) && !/^\s*if \(/.test(line);
    let showing = 0;
    for (const f of files) {
      const src = readFileSync(f, "utf8");
      if (!/github_login/.test(src)) continue;
      expect(src, `${f} must not hand-roll "@"+login`).not.toMatch(/"@" \+ \(?(?:acct|a|me|d|res\.data)\.github_login/);
      if (!src.split("\n").some(shows)) continue;
      showing++;
      expect(src, `${f} shows the name through RogerFmt.handle`).toMatch(/RogerFmt\.handle\(/);
    }
    expect(showing).toBeGreaterThan(0);
  });
  it("handle: an email address is shown as the address, never '@a@b.com'", () => {
    expect(RogerFmt.handle("someone@rogerai.fm")).toBe("someone@rogerai.fm");
    expect(RogerFmt.handle("octocat")).toBe("@octocat");
    expect(RogerFmt.handle("")).toBe("@you");
    expect(RogerFmt.handle(undefined)).toBe("@you");
  });
  it("who: provider-aware identity", () => {
    expect(RogerFmt.who({ provider: "github", github_login: "octocat" })).toBe("@octocat (GitHub)");
    expect(RogerFmt.who({ provider: "apple", github_login: "a@b.com" })).toBe("a@b.com (Apple)");
    expect(RogerFmt.who({ provider: "apple", github_login: "apple" })).toBe("your Apple account");
    expect(RogerFmt.who({ provider: "email", github_login: "me@rogerai.fm" })).toBe("me@rogerai.fm");
    expect(RogerFmt.who({ github_login: "octocat" })).toBe("@octocat");
    expect(RogerFmt.who(null)).toBe("your account");
  });
});

describe("the dates the account pages print (ported from payouts.js, billing.js, account.js)", () => {
  it("when: the viewer's short date, nothing for no timestamp", () => {
    expect(when(0)).toBe("");
    expect(when(undefined)).toBe("");
    expect(when(1_800_000_000)).toBe(new Date(1_800_000_000 * 1000).toLocaleDateString());
  });
  it("rel: a release date relative to today, in whole days", () => {
    const now = 1_800_000_000 * 1000;
    const day = 86400;
    expect(rel(0, now)).toBe("");
    expect(rel(now / 1000 + 12 * day + 3600, now)).toBe("in 12 days");
    expect(rel(now / 1000 + day, now)).toBe("tomorrow");
    expect(rel(now / 1000 + 3600, now)).toBe("today");
    expect(rel(now / 1000 - day, now)).toBe("yesterday");
    expect(rel(now / 1000 - 3 * day - 3600, now)).toBe("3 days ago");
  });
});
