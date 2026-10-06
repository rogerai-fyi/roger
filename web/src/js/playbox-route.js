// PlayboxRoute - the Playbox routing drawer's pure functions: the stored-state cleaner, the
// request body a turn sends, and the one-line summary. Kept apart from playbox.js so the
// exact body is unit-tested (web/test/playbox-route-exec.test.mjs) without a browser.
// Pure, dependency-free, CSP-safe (external script). Loaded before playbox.js.
(function () {
  "use strict";

  var MAX_FALLBACKS = 4;
  // the values the drawer's selects offer ("" is unset); anything else stored is dropped
  var REGION = /^[a-z]{2,8}$/;   // the broker's region token rule
  var SORTS = ["price", "throughput", "latency"], PREFS = ["cheap", "fast", "reliable"],
    TRUSTS = ["verified", "confidential"];

  // stored state is untrusted (an old version, a hand edit): keep only well-formed fields
  function clean(o) {
    var r = {};
    if (!o || typeof o !== "object" || Array.isArray(o)) return r;
    function num(v) { return typeof v === "number" && isFinite(v) && v >= 0 ? v : null; }
    function str(v) { return typeof v === "string" ? v : ""; }
    if (Array.isArray(o.models)) {
      r.models = o.models.filter(function (x) { return typeof x === "string" && x.trim(); }).slice(0, MAX_FALLBACKS);
    }
    // a price cap of 0 is no cap (the contract reads 0 as unset), so it is never kept as one
    ["out", "in", "turn"].forEach(function (k) { r[k] = num(o[k]) || null; });
    r.tps = num(o.tps);
    ["ctx", "ttft"].forEach(function (k) { r[k] = num(o[k]) || null; });
    ["selfHosted", "confidential", "tools", "vision"].forEach(function (k) { r[k] = o[k] === true; });
    r.quant = str(o.quant);
    r.region = REGION.test(str(o.region)) ? o.region : "";   // the broker 400s any other token
    function oneOf(v, xs) { return xs.indexOf(v) !== -1 ? v : ""; }
    r.sort = oneOf(o.sort, SORTS); r.trust = oneOf(o.trust, TRUSTS);
    r.pref = r.sort ? "" : oneOf(o.pref, PREFS);   // sort by replaces prefer
    var z = o.size;
    r.size = Array.isArray(z) && z.length === 2 && num(z[0]) !== null && num(z[1]) !== null && z[0] <= z[1] && z[1] > 0 ? z : null;
    return r;
  }

  // body adds the drawer's keys to a turn's body - only for fields that are set
  // opt.routed: this turn is on the routed tape's model; any other turn (the own-image vision
  // turn) carries none of the drawer's keys, only the signed-out free rule.
  function body(r, model, b, opt) {
    var loggedIn = !!(opt && opt.loggedIn), p = {}, g = {}, mp = {};
    function free(m) { return /:free$/.test(m) ? m : m + ":free"; }
    r = r || {};
    if (!loggedIn) b.model = free(model);   // signed out: free stations only, never a paid plan
    if (!(opt && opt.routed)) return b;
    if (r.models && r.models.length) {
      b["models"] = r.models.slice(0, MAX_FALLBACKS).map(function (m) { return loggedIn ? m : free(m); });
    }
    if (loggedIn) {
      if (r.out) mp.completion = r.out;
      if (r.in) mp.prompt = r.in;
      if (r.turn) mp.request = r.turn;
    }
    if (Object.keys(mp).length) p.max_price = mp;
    if (r.quant) p.quantizations = [r.quant];
    if (r.sort && (loggedIn || r.sort !== "price")) p.sort = r.sort;
    if (r.pref && !p.sort) g.pref = r.pref;
    if (r.tps) g.min_tps = r.tps;
    if (r.selfHosted) g.self_hosted_only = true;
    if (r.confidential) g.confidential = true;
    var need = []; if (r.tools) need.push("tools"); if (r.vision) need.push("vision");
    if (need.length) g.require = need;
    if (r.size) g.params_b = r.size;
    if (r.region) g.region = [r.region];
    if (r.trust) g.trust_min = r.trust;
    if (r.ctx) g.min_ctx = r.ctx;
    if (r.ttft) g.max_ttft_ms = r.ttft;
    if (Object.keys(p).length) b.provider = p;
    if (Object.keys(g).length) b.roger = g;
    return b;
  }

  // the one dim summary line a routed turn shows above its reply (none when nothing is set)
  function summary(r, loggedIn) {
    var parts = [];
    r = r || {};
    if (r.sort) parts.push(r.sort); else if (r.pref) parts.push(r.pref);
    if (r.models && r.models.length) parts.push("also " + r.models.join(","));
    if (r.tools || r.vision) parts.push("needs " + [r.tools && "tools", r.vision && "vision"].filter(Boolean).join(","));
    if (r.size) parts.push(r.size[0] + "-" + r.size[1] + "B");
    if (r.ctx) parts.push("ctx ≥ " + r.ctx);
    if (r.ttft) parts.push("first token ≤ " + r.ttft + "ms");
    if (r.trust) parts.push("trust " + r.trust);
    if (r.selfHosted) parts.push("self-hosted");
    if (r.region) parts.push("region " + r.region);
    if (r.quant) parts.push("quant " + r.quant);
    if (r.confidential) parts.push("confidential");
    if (r.tps) parts.push("≥" + r.tps + " t/s");
    if (loggedIn && r.in) parts.push("in ≤ $" + r.in + "/1M");
    if (loggedIn && r.out) parts.push("out ≤ $" + r.out + "/1M");
    if (loggedIn && r.turn) parts.push("turn ≤ $" + r.turn);
    return parts.join(" · ");
  }

  // ownsInput: the event's target takes its own keys and pointer (a field, contenteditable,
  // the routing drawer or its toggle), so the cassette bay around it must not act on them
  function ownsInput(t) {
    if (!t) return false;
    var tag = (t.tagName || "").toLowerCase();
    if (tag === "input" || tag === "select" || tag === "textarea" || t.isContentEditable) return true;
    return !!(t.closest && (t.closest("#dkRoute") || t.closest("#dkRouteBtn")));
  }

  // regionChoices: the station-declared regions the broker would accept in roger.region
  function regionChoices(list) {
    return (list || []).filter(function (x) { return typeof x === "string" && REGION.test(x); });
  }

  // the drawer's label for each contract key, so a 400 names the field to fix
  var LABELS = {
    models: "also try", completion: "max $/1M out", prompt: "max $/1M in", request: "max $/turn",
    min_tps: "min t/s", self_hosted_only: "self-hosted", confidential: "confidential", require: "needs tools / vision",
    quantizations: "quant", params_b: "size", region: "region", pref: "prefer", sort: "sort by",
    trust_min: "trust", min_ctx: "min ctx", max_ttft_ms: "max first token"
  };

  // nameFields swaps the contract key a refusal names for the drawer field's label. models
  // is a top-level key, named bare ("for models"); the rest carry their carrier prefix.
  function nameFields(msg) {
    msg = String(msg);
    Object.keys(LABELS).forEach(function (k) {
      var re = k === "models" ? /\bfor models\b/ : new RegExp("(roger|provider)\\.(max_price\\.)?" + k + "\\b");
      msg = msg.replace(re, k === "models" ? "for " + LABELS[k] : LABELS[k]);
    });
    return msg;
  }

  // isControl: a focused button, link or control-role element in the deck keeps its own keys
  // (Space and Enter press it), so the page's shortcut keys must not act on them too.
  function isControl(t) {
    if (!t) return false;
    var tag = (t.tagName || "").toLowerCase();
    if (tag === "button" || tag === "a" || tag === "summary") return true;
    var role = t.getAttribute ? t.getAttribute("role") : null;
    return /^(button|checkbox|switch|tab|menuitem|radio|link)$/.test(role || "");
  }

  var api = { MAX_FALLBACKS: MAX_FALLBACKS, clean: clean, body: body, summary: summary, ownsInput: ownsInput,
    regionChoices: regionChoices, LABELS: LABELS, nameFields: nameFields, isControl: isControl };
  if (typeof window !== "undefined") window.PlayboxRoute = api;
  if (typeof module !== "undefined" && module.exports) module.exports = api; // node test
})();
