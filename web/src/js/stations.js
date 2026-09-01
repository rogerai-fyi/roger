// Stations page glue for /stations. One credentialed read of GET /stations - the
// broker scopes the list to the authenticated owner's node bindings, so this file
// never sends or trusts a node id from the page.
//
// The chain state is presented as an AUDIT SIGNAL, never as a penalty: in the
// detect-and-record stage a break does not withhold earnings or affect standing, and
// the copy must not imply otherwise.
(function () {
  var BROKER = "https://broker.rogerai.fm";

  function get(path) {
    return fetch(BROKER + path, { credentials: "include" }).then(function (r) {
      return r.ok ? r.json() : null;
    }).catch(function () { return null; });
  }
  function el(id) { return document.getElementById(id); }
  function text(id, v) { var e = el(id); if (e) e.textContent = v; }
  function show(id) { var e = el(id); if (e) e.hidden = false; }
  function hide(id) { var e = el(id); if (e) e.hidden = true; }

  function money(n) {
    if (typeof n !== "number" || !isFinite(n)) return "-";
    if (window.RogerFmt) return RogerFmt.usdSigned(n);
    return "$" + n.toFixed(2);
  }
  function ago(unix) {
    if (!unix) return "never";
    var s = Math.max(0, Math.floor(Date.now() / 1000 - unix));
    if (s < 60) return s + "s ago";
    if (s < 3600) return Math.floor(s / 60) + "m ago";
    if (s < 86400) return Math.floor(s / 3600) + "h ago";
    return Math.floor(s / 86400) + "d ago";
  }
  function esc(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  // chainLabel turns the broker's chain_state into copy an operator can act on.
  function chainLabel(st) {
    var ch = st.chain || {};
    switch (st.chain_state) {
      case "continuous":
        return { cls: "ok", text: "Chain continuous" };
      case "breaks-recorded":
        return {
          cls: "warn",
          text: ch.breaks + (ch.breaks === 1 ? " break recorded" : " breaks recorded") + " - audit signal only",
        };
      default:
        return { cls: "muted", text: "No receipts yet" };
    }
  }

  function offerLine(o) {
    var bits = [esc(o.model)];
    if (o.modality && o.modality !== "chat") bits.push(esc(o.modality));
    bits.push(money(o.price_in) + " in / " + money(o.price_out) + " out per 1M");
    if (o.ctx) bits.push(o.ctx.toLocaleString() + " ctx");
    return bits.join(" &middot; ");
  }

  function renderStation(st) {
    var chain = chainLabel(st);
    var offers = (st.offers || []).map(function (o) {
      return '<li class="fine">' + offerLine(o) + "</li>";
    }).join("");
    var badges = [
      '<span class="badge ' + (st.on_air ? "on" : "off") + '">' + (st.on_air ? "ON AIR" : "OFF AIR") + "</span>",
    ];
    if (st.confidential) badges.push('<span class="badge">CONFIDENTIAL</span>');
    if (st.private) badges.push('<span class="badge">PRIVATE BAND</span>');
    if (st.curated) badges.push('<span class="badge">&raquo; CURATED &middot; ' + esc(st.curated_provider || "") + '</span>');

    return (
      '<article class="st-row">' +
      '<h3>' + esc(st.node_id) + " " + badges.join(" ") + "</h3>" +
      '<p class="fine">' +
      "Registered " + ago(st.registered_at) + " &middot; last seen " + ago(st.last_seen) +
      (st.region ? " &middot; " + esc(st.region) : "") +
      (st.hw ? " &middot; " + esc(st.hw) : "") +
      "</p>" +
      (offers ? "<ul>" + offers + "</ul>" : '<p class="fine">No offers published.</p>') +
      // A curated station's credits are TWO things and the copy keeps them apart
      // (features/curated/curated_web.feature, amended by the 50/50 ruling): the
      // reimbursement of the provider's list price - never income - plus the
      // operator's half of the routing fee, which is.
      '<p class="fine">' + (st.curated
        ? 'Curated credits ' + (st.earnings_unavailable ? "unavailable" : money(st.earnings)) + ' (your provider list reimbursed + your half of the routing fee)'
        : 'Earned ' + (st.earnings_unavailable ? "unavailable" : money(st.earnings))) + " &middot; " + (st.recent_served || 0) + " recent requests" +
      ' &middot; <span class="' + chain.cls + '">' + chain.text + "</span></p>" +
      "</article>"
    );
  }

  function renderStrike(k) {
    return (
      '<article class="st-row">' +
      "<h3>" + esc(k.kind) + "</h3>" +
      '<p class="fine">' + ago(k.created_at) + "</p>" +
      "<pre class=\"cmd\">" + esc(k.evidence) + "</pre>" +
      "</article>"
    );
  }

  function render(data) {
    hide("stLoading");
    show("card");
    text("who", data.github_login || "your account");

    var list = data.stations || [];
    if (!list.length) {
      show("stEmpty");
      return;
    }

    var onAir = 0, earned = 0, served = 0;
    list.forEach(function (s) {
      if (s.on_air) onAir++;
      earned += (typeof s.earnings === "number" ? s.earnings : 0);
      served += (s.recent_served || 0);
    });
    text("tileCount", String(list.length));
    text("tileOnAir", onAir + " of " + list.length);
    text("tileEarned", money(earned));
    text("tileServed", String(served));
    show("stTiles");

    el("stRows").innerHTML = list.map(renderStation).join("");
    show("stList");
    show("stChainHelp");

    var strikes = data.strikes || [];
    if (strikes.length) {
      el("stStrikes").innerHTML = strikes.map(renderStrike).join("");
      show("stEvidence");
    }
  }

  get("/stations").then(function (data) {
    if (!data) {
      hide("stLoading");
      show("card");
      show("stError");
      return;
    }
    render(data);
  });
})();
