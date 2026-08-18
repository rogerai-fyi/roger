/* wave-jobs.js - the job finder in §4 of the Wave family field guide.
 *
 * PROGRESSIVE ENHANCEMENT, and the order matters. The served page is a whole,
 * grouped, readable table; the toolbar ships `hidden` and nothing here is
 * required to read a single job. This file only ever ADDS: it reveals the
 * filters, counts what is on screen, and bounds the panel's height so the
 * section stays one screen tall as the table grows. If it never loads, or
 * fails, the reader loses the filters and keeps every job.
 *
 * The one piece of real logic is the slot cell. It is written for people
 * ("Micro to Giga", "Pico + Nano", "Nano, Micro"), not for a parser, and the
 * page's tests read it the same way - so the expansion below has to agree with
 * wave-family.test.mjs and research-page.test.mjs. Keep the three forms.
 */
(function () {
  "use strict";

  var LADDER = ["Pico", "Nano", "Micro", "Giga", "Tera", "Peta", "Exa"];

  var root = document.querySelector("[data-wj]");
  if (!root) return;
  var bar = root.querySelector("[data-wj-bar]");
  var scroll = root.querySelector("[data-wj-scroll]");
  var count = root.querySelector("[data-wj-count]");
  var empty = root.querySelector("[data-wj-empty]");
  var table = root.querySelector("table");
  if (!bar || !scroll || !table) return;

  /* "Micro to Giga" -> Micro, Giga and everything between; "Pico + Nano" and
     "Nano, Micro" -> exactly those. Anything unrecognised expands to nothing,
     which is visible (the row filters out) rather than silently wrong. */
  function slotsOf(text) {
    var s = (text || "").replace(/\s+/g, " ").trim();
    if (/\bto\b/i.test(s)) {
      var ends = s.split(/\s+to\s+/i).map(function (x) { return LADDER.indexOf(x.trim()); });
      if (ends[0] < 0 || ends[1] < 0) return [];
      return LADDER.slice(ends[0], ends[1] + 1);
    }
    return s.split(/\s*(?:\+|and|,)\s*/)
      .map(function (x) { return x.trim(); })
      .filter(function (x) { return LADDER.indexOf(x) >= 0; });
  }

  var bodies = [];
  Array.prototype.forEach.call(table.querySelectorAll("tbody[data-setting]"), function (tb) {
    var rows = [];
    Array.prototype.forEach.call(tb.querySelectorAll("tr"), function (tr) {
      var cell = tr.querySelector(".tier-cell");
      if (!cell) return;                    // the group heading row
      rows.push({ tr: tr, slots: slotsOf(cell.textContent) });
    });
    bodies.push({
      tbody: tb,
      head: tb.querySelector(".wj__group"),
      setting: tb.getAttribute("data-setting"),
      rows: rows,
    });
  });
  if (!bodies.length) return;

  var total = bodies.reduce(function (n, b) { return n + b.rows.length; }, 0);
  var state = { setting: "all", tier: "all" };

  function matches(group, row) {
    if (state.setting !== "all" && group.setting !== state.setting) return false;
    if (state.tier !== "all" && row.slots.indexOf(state.tier) < 0) return false;
    return true;
  }

  function plural(n, one, many) { return n + " " + (n === 1 ? one : many); }

  function apply() {
    var shown = 0;
    bodies.forEach(function (group) {
      var live = 0;
      group.rows.forEach(function (row) {
        var ok = matches(group, row);
        row.tr.hidden = !ok;
        if (ok) live++;
      });
      shown += live;
      group.tbody.hidden = live === 0;
      if (group.head) {
        var seen = group.head.querySelector("[data-wj-tally]");
        if (seen) seen.textContent = live === group.rows.length ? String(live) : live + " of " + group.rows.length;
      }
    });
    if (empty) empty.hidden = shown !== 0;
    if (count) {
      count.textContent = shown === total
        ? plural(total, "job", "jobs")
        : "showing " + shown + " of " + plural(total, "job", "jobs");
    }
    scroll.scrollTop = 0;
    /* The bottom fade is an affordance, not decoration: it only means anything
       while there is table under it, so it is off whenever the panel fits. */
    root.classList.toggle("has-more", scroll.scrollHeight > scroll.clientHeight + 4);
    syncHead();
  }

  /* Chip counts come from the DOM rather than from a number typed into the
     markup, so a job added to the table can never disagree with its own label. */
  function tally(chip) {
    var setting = chip.getAttribute("data-wj-setting");
    var tier = chip.getAttribute("data-wj-tier");
    var n = 0;
    bodies.forEach(function (group) {
      group.rows.forEach(function (row) {
        if (setting) { if (setting === "all" || group.setting === setting) n++; }
        else if (tier === "all" || row.slots.indexOf(tier) >= 0) n++;
      });
    });
    return n;
  }

  var chips = Array.prototype.slice.call(bar.querySelectorAll(".wj__chip"));
  chips.forEach(function (chip) {
    var n = tally(chip);
    var badge = document.createElement("span");
    badge.className = "wj__n";
    badge.textContent = String(n);
    chip.appendChild(document.createTextNode(" "));
    chip.appendChild(badge);
    /* A filter that can only ever empty the table is not a choice, so it is
       disabled rather than offered. */
    if (n === 0) {
      chip.disabled = true;
      chip.setAttribute("aria-disabled", "true");
    }
    chip.addEventListener("click", function () {
      var key = chip.getAttribute("data-wj-setting") ? "setting" : "tier";
      var value = chip.getAttribute("data-wj-" + key);
      state[key] = state[key] === value ? "all" : value;
      chips.forEach(function (other) {
        var otherKey = other.getAttribute("data-wj-setting") ? "setting" : "tier";
        if (otherKey !== key) return;
        var otherValue = other.getAttribute("data-wj-" + otherKey);
        other.setAttribute("aria-pressed", state[key] === otherValue ? "true" : "false");
      });
      apply();
    });
  });

  /* Add the group tallies to the heading rows once, in script, so the served
     markup carries no number that could go stale. */
  bodies.forEach(function (group) {
    if (!group.head) return;
    var th = group.head.querySelector("th");
    if (!th) return;
    var tallyEl = document.createElement("span");
    tallyEl.className = "wj__tally";
    tallyEl.setAttribute("data-wj-tally", "");
    th.appendChild(tallyEl);
  });

  /* The group headings stick UNDER the column headings, and the column heading
     row is two lines tall at some widths and one at others. Measure it rather
     than guess it, or the first group row scrolls up behind the header. */
  function syncHead() {
    var head = table.querySelector("thead");
    if (!head) return;
    var h = head.getBoundingClientRect().height;
    if (h > 2) root.style.setProperty("--wj-head", Math.round(h) + "px");
  }
  window.addEventListener("resize", syncHead);

  /* The panel is only bounded once there is a way to shorten it. */
  root.classList.add("is-live");
  scroll.setAttribute("tabindex", "0");
  scroll.setAttribute("role", "region");
  scroll.setAttribute("aria-label", "Jobs by slot");
  bar.hidden = false;
  apply();
  syncHead();
}());
