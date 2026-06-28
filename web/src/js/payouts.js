// Payouts page glue for /payouts: earnings provenance (the held -> payable -> paid
// split, the hold timeline, the per-model/per-node breakdown, payout history and the
// ledger lineage), plus Stripe Connect status + payout request. Thin reads over the
// broker behind the credentialed session cookie (no tokens ever touch JS). Logged-out
// visitors are routed to /login. Honest-empty: every section shows a plain note when
// the broker returns nothing rather than inventing numbers.
(function () {
  var BROKER = "https://broker.rogerai.fyi";

  function api(path, opts) {
    opts = opts || {};
    opts.credentials = "include";
    return fetch(BROKER + path, opts).then(function (r) {
      return r.ok ? r.json() : null;
    }).catch(function () { return null; });
  }
  function get(path) { return api(path); }

  function text(id, v) { var el = document.getElementById(id); if (el) el.textContent = v; }
  function show(id) { var el = document.getElementById(id); if (el) el.hidden = false; }
  function hide(id) { var el = document.getElementById(id); if (el) el.hidden = true; }
  function on(id, ev, fn) { var el = document.getElementById(id); if (el) el.addEventListener(ev, fn); }
  function addClass(id, c) { var el = document.getElementById(id); if (el) el.classList.add(c); }

  // Money in dollars (1 credit = $1; display relabel only). Adaptive precision so a
  // real sub-cent earning never reads as $0.00.
  function cr(n) {
    if (typeof n !== "number" || !isFinite(n)) return "-";
    if (window.RogerFmt) return RogerFmt.usdSigned(n); // canonical money renderer (web == CLI/TUI)
    if (n === 0) return "$0.00";
    var s = n < 0 ? "-" : "";
    var a = Math.abs(n);
    if (a >= 0.01) return s + "$" + a.toFixed(2);
    var p = a.toPrecision(3);
    if (/e/i.test(p)) p = a.toFixed(20).replace(/0+$/, "");
    else p = p.replace(/0+$/, "").replace(/\.$/, "");
    return s + "$" + p;
  }
  function when(ts) {
    if (!ts) return "";
    return new Date(ts * 1000).toLocaleDateString();
  }
  // Human "in N days" / "N days ago" for a hold release date.
  function rel(ts) {
    if (!ts) return "";
    var days = Math.round((ts * 1000 - Date.now()) / 86400000);
    if (days > 1) return "in " + days + " days";
    if (days === 1) return "tomorrow";
    if (days === 0) return "today";
    if (days === -1) return "yesterday";
    return Math.abs(days) + " days ago";
  }

  function li(left, right, leftClass) {
    var el = document.createElement("li");
    var a = document.createElement("span");
    a.className = leftClass || "r-model";
    a.textContent = left;
    var b = document.createElement("span");
    b.className = "r-cost";
    b.textContent = right;
    el.appendChild(a);
    el.appendChild(b);
    return el;
  }
  function fill(listId, emptyId, rows, render) {
    var ul = document.getElementById(listId);
    if (!ul) return;
    if (!rows || !rows.length) { if (emptyId) show(emptyId); return; }
    rows.forEach(function (row) { ul.appendChild(render(row)); });
    show(listId);
  }

  function wireLogout() {
    on("logout", "click", function () {
      fetch(BROKER + "/auth/logout", { method: "POST", credentials: "include" })
        .then(function () { location.replace("/login.html"); });
    });
  }

  var path = location.pathname.replace(/\/$/, "").replace(/\.html$/, "");
  if (!path.endsWith("/payouts")) return;

  // ---- the held -> payable -> paid meter. Segments are proportional to the three
  // amounts; a zero total collapses to an empty rail (honest-empty, no fake fill). ----
  function renderMeter(held, payable, paid) {
    var total = held + payable + paid;
    function pct(v) { return total > 0 ? (v / total * 100) : 0; }
    var h = document.getElementById("segHeld");
    var p = document.getElementById("segPayable");
    var d = document.getElementById("segPaid");
    if (h) h.style.width = pct(held) + "%";
    if (p) p.style.width = pct(payable) + "%";
    if (d) d.style.width = pct(paid) + "%";
  }

  // ---- the hold timeline. Mark each step done/active from the live split: an
  // earning is always Earned; Held is active when there is held money (with the
  // soonest release date); Payable is active/done when there is payable; Paid done
  // when lifetime paid > 0. ----
  function renderTimeline(e) {
    var held = e.held || 0, payable = e.payable || 0, paid = e.paid || 0;
    if (held + payable + paid <= 0) { show("timelineEmpty"); return; }
    show("timeline");
    if (held > 0) {
      addClass("tlHeld", "is-active");
      text("tlHeldSub", cr(held) + " in the hold window");
      if (e.next_release) {
        text("tlHeldWhen", "next clears " + when(e.next_release) + " (" + rel(e.next_release) + ")");
      }
    } else {
      addClass("tlHeld", "is-done");
    }
    if (payable > 0) {
      addClass("tlPayable", "is-active");
      text("tlPayableSub", cr(payable) + " cleared, withdrawable now");
    } else if (paid > 0) {
      addClass("tlPayable", "is-done");
    }
    if (paid > 0) {
      addClass("tlPaid", "is-done");
      text("tlPaidWhen", "");
    }
  }

  // ---- where the money came from: per-model rows, each annotated with its node and
  // request count, sorted by earnings. Driven by /metrics/provider (the owner's served
  // traffic, with the 70% earnings share already computed). ----
  function renderBreakdown(models) {
    var rows = (models || []).filter(function (m) { return (m.earnings_usd || 0) > 0; });
    if (!rows.length) { show("breakdownEmpty"); return; }
    rows.sort(function (a, b) { return (b.earnings_usd || 0) - (a.earnings_usd || 0); });
    var ul = document.getElementById("breakdown");
    rows.forEach(function (m) {
      var el = document.createElement("li");
      var left = document.createElement("span");
      left.className = "r-model po-break__left";
      var name = document.createElement("b");
      name.textContent = m.model || "model";
      var meta = document.createElement("span");
      meta.className = "po-break__meta";
      var node = m.node_id ? m.node_id : "node";
      meta.textContent = node + " - " + (window.RogerFmt ? RogerFmt.count(m.requests || 0) : (m.requests || 0)) + " req";
      left.appendChild(name);
      left.appendChild(meta);
      var right = document.createElement("span");
      right.className = "r-cost";
      right.textContent = cr(m.earnings_usd || 0);
      el.appendChild(left);
      el.appendChild(right);
      ul.appendChild(el);
    });
    show("breakdown");
  }

  // Human label for a ledger kind.
  function kindLabel(k) {
    if (k === "payout") return "Payout";
    if (k === "chargeback") return "Clawback (dispute)";
    if (k === "adjustment") return "Adjustment";
    return k || "entry";
  }

  get("/account").then(function (a) {
    if (!a) { location.replace("/login.html"); return; }
    var e = a.earnings || {};
    text("held", cr(e.held || 0));
    text("payable", cr(e.payable || 0));
    text("paid", cr(e.paid || 0));
    if ((e.reserved || 0) > 0) {
      text("reservedNote", "Plus " + cr(e.reserved) + " in reserve, released after the hold.");
      show("reservedNote");
    }
    renderMeter(e.held || 0, e.payable || 0, e.paid || 0);
    renderTimeline(e);
    show("card");
    wireLogout();
    refreshConnect();
    loadEarnings();
    loadHistory();
    loadBreakdown();
  });

  // ---- the release LADDER. /payouts/earnings carries releases[] - the still-held lots
  // bucketed by clear date - so we render a real dated ladder ("$X clears Jun 30, $Y
  // clears Jul 15") instead of only the single soonest date the split's next_release
  // carries. Honest-empty: no held money -> no ladder. ----
  function renderLadder(releases) {
    var rows = (releases || []).filter(function (r) { return (r.amount || 0) > 0; });
    if (!rows.length) return;
    var ul = document.getElementById("ladder");
    if (!ul) return;
    rows.forEach(function (r) {
      var el = document.createElement("li");
      el.className = "po-ladder__step";
      var left = document.createElement("span");
      left.className = "po-ladder__date";
      left.textContent = when(r.date);
      var meta = document.createElement("span");
      meta.className = "po-ladder__meta";
      var lots = r.lot_count || 0;
      meta.textContent = rel(r.date) + " - " + lots + (lots === 1 ? " lot" : " lots");
      left.appendChild(meta);
      var right = document.createElement("span");
      right.className = "po-ladder__amount";
      right.textContent = cr(r.amount || 0);
      el.appendChild(left);
      el.appendChild(right);
      ul.appendChild(el);
    });
    show("ladderWrap");
  }

  function loadEarnings() {
    get("/payouts/earnings").then(function (e) {
      if (!e) return;
      renderLadder(e.releases);
    });
  }

  // /connect/status carries the live split AND the policy (hold days, min, schedule),
  // so it both confirms the KYC gate and labels the hold/minimum facts truthfully.
  function refreshConnect() {
    get("/connect/status").then(function (s) {
      if (!s) return;
      var status = s.status || "none";
      text("connect", status);
      if (status === "active") { hide("onboard"); show("request"); }
      else { show("onboard"); hide("request"); }
      if (typeof s.min_payout === "number") text("minNote", cr(s.min_payout));
      if (typeof s.hold_days === "number") {
        text("holdNote", s.hold_days + " days");
        text("tlHeldSub", "held for " + s.hold_days + " days");
      }
      if (s.schedule) text("scheduleNote", s.schedule);
    });
  }

  // ---- one EXPANDABLE payout-history row. The header is the date/state/amount; click
  // (or keyboard) toggles a drawer that lazily fetches /payouts/{id}/lots - the exact
  // earning receipts (model, node, gross, request id, date) that funded the transfer -
  // the request-level lineage. Fetched once, then cached on the row. ----
  function payoutRow(p) {
    var el = document.createElement("li");
    el.className = "po-payout";

    var head = document.createElement("button");
    head.type = "button";
    head.className = "po-payout__head";
    head.setAttribute("aria-expanded", "false");
    var label = document.createElement("span");
    label.className = "r-model";
    var chev = document.createElement("span");
    chev.className = "po-payout__chev";
    chev.setAttribute("aria-hidden", "true");
    chev.textContent = "›"; // single right-pointing angle
    label.appendChild(chev);
    label.appendChild(document.createTextNode(when(p.created_at) + " - " + p.state));
    var amount = document.createElement("span");
    amount.className = "r-cost";
    amount.textContent = cr(p.amount);
    head.appendChild(label);
    head.appendChild(amount);
    if (p.stripe_transfer_id) head.title = "Stripe transfer " + p.stripe_transfer_id;

    var drawer = document.createElement("div");
    drawer.className = "po-payout__drawer";
    drawer.hidden = true;

    var loaded = false;
    function expand() {
      el.classList.add("is-open");
      head.setAttribute("aria-expanded", "true");
      drawer.hidden = false;
      if (loaded) return;
      loaded = true;
      var note = document.createElement("p");
      note.className = "fine po-payout__loading";
      note.textContent = "loading receipts...";
      drawer.appendChild(note);
      get("/payouts/" + p.id + "/lots").then(function (res) {
        drawer.removeChild(note);
        var lots = (res && res.lots) || [];
        if (!lots.length) {
          var empty = document.createElement("p");
          empty.className = "fine";
          empty.textContent = "No funding receipts found for this payout.";
          drawer.appendChild(empty);
          return;
        }
        var ul = document.createElement("ul");
        ul.className = "recent po-lots";
        lots.forEach(function (l) {
          var row = document.createElement("li");
          var left = document.createElement("span");
          left.className = "r-model po-lots__left";
          var name = document.createElement("b");
          name.textContent = l.model || "model";
          var meta = document.createElement("span");
          meta.className = "po-lots__meta";
          meta.textContent = (l.node || "node") + " - " + (l.request_id || "?") + " - " + when(l.created_at);
          left.appendChild(name);
          left.appendChild(meta);
          var right = document.createElement("span");
          right.className = "r-cost";
          right.textContent = cr(l.gross || 0);
          row.appendChild(left);
          row.appendChild(right);
          ul.appendChild(row);
        });
        drawer.appendChild(ul);
      });
    }
    function collapse() {
      el.classList.remove("is-open");
      head.setAttribute("aria-expanded", "false");
      drawer.hidden = true;
    }
    head.addEventListener("click", function () {
      if (el.classList.contains("is-open")) collapse(); else expand();
    });

    el.appendChild(head);
    el.appendChild(drawer);
    return el;
  }

  function loadHistory() {
    get("/payouts/history").then(function (h) {
      if (!h) return;
      fill("payouts", "payoutsEmpty", h.payouts, payoutRow);
      fill("ledger", "ledgerEmpty", h.ledger, function (g) {
        var label = when(g.ts) + " - " + kindLabel(g.kind);
        var row = li(label, cr(g.amount));
        if (g.ref) {
          var note = document.createElement("span");
          note.className = "po-ledger__ref";
          note.textContent = g.ref;
          row.querySelector(".r-model").appendChild(note);
        }
        return row;
      });
    });
  }

  function loadBreakdown() {
    get("/metrics/provider?days=30").then(function (m) {
      if (!m) { show("breakdownEmpty"); return; }
      if (m.period_days) {
        text("breakdownWindow", "Earnings over the last " + m.period_days + " days, by what you served.");
      }
      renderBreakdown(m.models);
    });
  }

  on("onboard", "click", function () {
    api("/connect/onboard", { method: "POST" }).then(function (r) {
      if (r && r.url) { location.href = r.url; } else { text("payMsg", "could not start onboarding"); }
    });
  });
  on("request", "click", function () {
    text("payMsg", "requesting...");
    fetch(BROKER + "/payouts/request", { method: "POST", credentials: "include" }).then(function (r) {
      return r.json().then(function (body) {
        if (r.ok) { text("payMsg", "payout requested"); location.reload(); }
        else { text("payMsg", (body && body.error && body.error.message) || "payout failed"); }
      });
    }).catch(function () { text("payMsg", "payout failed"); });
  });
})();
