/* RogerAI - the model taxonomy rules for the site (features/categories/web_tuner.feature).
 *
 * ONE place for what a band's category means on the page, so bands.js only wires it and the
 * rules are tested by running them (test/categories.test.mjs), not by matching source text.
 *
 * The rule a label follows is the dial's and /market's: the pair MOST of a band's stations
 * carry (ties alphabetical), marked "~" when no station backing it DECLARED it - a guess is
 * always labelled as one. Anything a station sent is escaped before it is rendered.
 */
(function (root) {
  "use strict";

  var GUESS = "~";

  function esc(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }
  function pair(cat, sub) { return sub ? cat + "/" + sub : cat; }

  // fold: the band taxonomy from its /discover stations, by the /market rule (founder ruling
  // 2026-10-05): online HUMAN stations vote; curated proxies label only a band no human
  // station labels; a band with nothing on air falls back to every station. Tags and grades
  // come from every on-air station (all of them when none is on air).
  //   -> { cat, sub, declared (how many voters declared the winning pair), tags, policies }
  function fold(stations) {
    stations = stations || [];
    var online = stations.filter(function (s) { return s.online !== false; });
    var human = online.filter(function (s) { return !s.curated; });
    var hasCat = function (list) { return list.some(function (s) { return !!s.category; }); };
    var voters = hasCat(human) ? human : online.filter(function (s) { return !!s.curated; });
    var pool = online;
    if (!online.length) { voters = stations; pool = stations; }
    var pairs = {}, declared = {}, tags = {}, pols = {};
    voters.forEach(function (s) {
      if (!s.category) return;
      var k = pair(s.category, s.subcategory || "");
      pairs[k] = (pairs[k] || 0) + 1;
      if (s.taxonomy_source === "declared") declared[k] = (declared[k] || 0) + 1;
    });
    pool.forEach(function (s) {
      (s.tags || []).forEach(function (t) { tags[t] = (tags[t] || 0) + 1; });
      if (s.content_policy) pols[s.content_policy] = true;
    });
    var best = 0, key = "";
    Object.keys(pairs).forEach(function (k) {
      if (pairs[k] > best || (pairs[k] === best && k < key)) { best = pairs[k]; key = k; }
    });
    var cut = key.indexOf("/");
    return {
      cat: cut < 0 ? key : key.slice(0, cut),
      sub: cut < 0 ? "" : key.slice(cut + 1),
      declared: declared[key] || 0,
      tags: Object.keys(tags).sort(function (a, b) { return (tags[b] - tags[a]) || (a < b ? -1 : a > b ? 1 : 0); }),
      policies: Object.keys(pols).sort()
    };
  }

  // fromMarketRow: the band taxonomy from a /market row, which says nothing about who
  // declared it - so it reads as a guess (declared -1 = unknown) until /discover says more.
  function fromMarketRow(m) {
    m = m || {};
    return {
      cat: m.category || "", sub: m.subcategory || "", declared: -1,
      tags: (m.tags || []).slice(), policies: (m.content_policies || []).slice()
    };
  }

  function label(t) {
    if (!t || !t.cat) return "";
    return pair(t.cat, t.sub) + (t.declared > 0 ? "" : GUESS);
  }

  // grade: the least-refusing grade any station declared ("aligned" goes unbadged).
  function grade(t) {
    var p = (t && t.policies) || [];
    if (p.indexOf("uncensored") >= 0) return "uncensored";
    if (p.indexOf("reduced-refusal") >= 0) return "reduced-refusal";
    return "";
  }

  function matches(t, f) {
    if (!f) return true;
    if (f.category && (!t || t.cat !== f.category)) return false;
    if (f.sub && (!t || t.sub !== f.sub)) return false;
    return true;
  }

  function search(model, t, q) {
    q = String(q || "").trim().toLowerCase();
    if (!q) return true;
    var hay = [model || ""];
    if (t) hay = hay.concat([t.cat, t.sub], t.tags || []);
    return hay.some(function (h) { return String(h || "").toLowerCase().indexOf(q) !== -1; });
  }

  // options: the Category select, from GET /categories (canonical order, empty aisles kept,
  // counted by online stations - human and curated).
  function options(resp) {
    var out = [{ value: "", text: "All categories" }];
    ((resp && resp.categories) || []).forEach(function (c) {
      var n = (+c.providers || 0) + (+c.curated_providers || 0);
      out.push({ value: c.name, text: c.name + " (" + n + ")" });
    });
    return out;
  }

  // subOptions: the Subcategory select for one category.
  function subOptions(resp, cat) {
    var out = [{ value: "", text: "All" }];
    ((resp && resp.categories) || []).forEach(function (c) {
      if (c.name !== cat) return;
      (c.subcategories || []).forEach(function (s) {
        var n = (+s.providers || 0) + (+s.curated_providers || 0);
        out.push({ value: s.name, text: s.name + " (" + n + ")" });
      });
    });
    return out;
  }

  // fromQuery: ?category=&sub= from the page address, kept only when it names a real value.
  function fromQuery(qs, resp) {
    var p = {};
    String(qs || "").replace(/^\?/, "").split("&").forEach(function (kv) {
      if (!kv) return;
      var i = kv.indexOf("=");
      // A malformed pair (a stray "%", in any parameter) is skipped, never fatal.
      try {
        var k = decodeURIComponent(i < 0 ? kv : kv.slice(0, i));
        p[k] = decodeURIComponent(i < 0 ? "" : kv.slice(i + 1)).trim().toLowerCase();
      } catch (_) { /* skip */ }
    });
    var cats = (resp && resp.categories) || [];
    var c = null;
    cats.forEach(function (x) { if (x.name === p.category) c = x; });
    if (!c) return { category: "", sub: "" };
    var subOK = (c.subcategories || []).some(function (s) { return s.name === p.sub; });
    return { category: c.name, sub: subOK ? p.sub : "" };
  }

  // tagsHTML: the row's chips - the label, the grade, then the tags - escaped.
  function tagsHTML(t) {
    var h = "";
    var l = label(t);
    if (l) h += ' <span class="band-tag band-tag--cat" title="' + esc(t.declared > 0
      ? "category declared by a station - its own description, not verified"
      : "category guessed (~), not declared") + '">' + esc(l) + "</span>";
    var g = grade(t);
    if (g) h += ' <span class="band-tag band-tag--grade" title="a station declares this grade for its weights - RogerAI still screens every request">' + esc(g) + "</span>";
    ((t && t.tags) || []).slice(0, 4).forEach(function (tag) { h += ' <span class="band-tag">' + esc(tag) + "</span>"; });
    return h;
  }

  root.RogerTaxonomy = {
    fold: fold, fromMarketRow: fromMarketRow, label: label, grade: grade, matches: matches,
    search: search, options: options, subOptions: subOptions, fromQuery: fromQuery,
    tagsHTML: tagsHTML
  };
})(window);
