import { describe, it, expect } from "vitest";
import { cr, num, kfmt, ago, shortId, priceStr, parseDays, daysStr } from "#lib/console/format.js";

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
