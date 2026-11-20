/* What a model is for (features/details): the ABOUT block of the website's QSL card and of
   the node console's BROWSE row. ONE script, shipped twice: as web/src/js/details.js and as
   internal/webui/assets/console-details.js (a test holds the two byte-identical).

   The rules are the Go ones (internal/protocol FoldDetails, internal/about): the band's text
   is RogerAI's curated note when a station carries it, else what most online human stations
   declared; every string is cleaned again here; text is only ever set with textContent; a
   link exists only for a curated note's same-site page, never for station text. */
(function (root) {
  "use strict";
  var SUMMARY_MAX = 140, DETAIL_MAX = 200;

  // clean mirrors protocol.DetailsText: controls (so ESC), bidi and zero-width characters go,
  // line breaks and tabs become one space, runs of spaces collapse, the ends are trimmed.
  function clean(s, max) {
    var out = "", space = false, n = 0;
    s = s == null ? "" : String(s);
    for (var i = 0; i < s.length; i++) {
      var c = s.charCodeAt(i);
      if (c === 32 || c === 9 || c === 10 || c === 13 || c === 0x2028 || c === 0x2029) { space = true; continue; }
      if (c < 0x20 || c === 0x7f || (c >= 0x80 && c <= 0x9f) || (c >= 0x200b && c <= 0x200f) ||
          (c >= 0x202a && c <= 0x202e) || (c >= 0x2066 && c <= 0x2069) || c === 0xfeff) continue;
      if (space && out) { if (n + 1 > max) break; out += " "; n++; }
      space = false;
      var ch = s.charAt(i);
      if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length) { ch += s.charAt(i + 1); i++; }
      if (n + 1 > max) break;
      out += ch; n++;
    }
    return out.replace(/\s+$/, "");
  }

  function sitePath(p) { return typeof p === "string" && /^\/[A-Za-z0-9._\/#-]{1,199}$/.test(p) && p.charAt(1) !== "/"; }

  function text(o) {
    return { summary: clean(o.summary, SUMMARY_MAX), best_at: clean(o.best_at, DETAIL_MAX), not_for: clean(o.not_for, DETAIL_MAX) };
  }
  function hasText(t) { return !!(t.summary || t.best_at || t.not_for); }

  // fold is protocol.FoldDetails over /discover offers (or the website's station rows).
  function fold(offers) {
    offers = offers || [];
    for (var i = 0; i < offers.length; i++) {
      var o = offers[i], t = text(o);
      if (String(o.details_source || "").toLowerCase() === "rogerai" && hasText(t)) {
        t.source = "rogerai"; t.page = sitePath(o.page) ? o.page : "";
        return t;
      }
    }
    var human = [], proxy = [], off = [];
    offers.forEach(function (o) { (!o.online ? off : o.curated ? proxy : human).push(o); });
    var pool = human;
    if (!human.some(function (o) { return hasText(text(o)); })) pool = proxy;
    if (!human.length && !proxy.length) pool = off;
    var count = {}, keys = [];
    pool.forEach(function (o) {
      var t = text(o);
      if (!hasText(t)) return;
      var k = t.summary + "\u0000" + t.best_at + "\u0000" + t.not_for;
      if (!count[k]) { count[k] = 0; keys.push(k); }
      count[k]++;
    });
    if (!keys.length) return null;
    keys.sort(function (a, b) { return count[b] - count[a] || (a < b ? -1 : a > b ? 1 : 0); });
    var p = keys[0].split("\u0000");
    return { summary: p[0], best_at: p[1], not_for: p[2], source: "declared", declared_by: count[keys[0]], stations: pool.length };
  }

  var TUNINGS = { base: 1, instruct: 1, finetune: 1, merge: 1, quantized: 1, adapter: 1 };
  function tuning(o) { var t = String(o.tuning || "").toLowerCase(); return TUNINGS[t] ? t : ""; }

  // facts is about.Facts: tuning, base and license of the station whose words won.
  function facts(offers, d) {
    var f = { tuning: "", base: "", license: "" };
    (offers || []).some(function (o) {
      var t = text(o);
      if (d && t.summary === d.summary && t.best_at === d.best_at && t.not_for === d.not_for) {
        f = { tuning: tuning(o), base: clean(o.base_model, 128), license: clean(o.license, 64) };
        return true;
      }
      return false;
    }) || (offers || []).forEach(function (o) {
      f.tuning = f.tuning || tuning(o);
      f.base = f.base || clean(o.base_model, 128);
      f.license = f.license || clean(o.license, 64);
    });
    return f;
  }

  function kind(modality, vision, tuning, base) {
    var k = modality === "tts" ? "text in, speech out" : modality === "stt" ? "speech in, text out" :
      vision ? "text and image in, text out" : "text in, text out";
    if (tuning === "adapter" && base) return k + " · adapter on " + base;
    return tuning ? k + " · " + tuning : k;
  }

  // siblings: the other on-air models on the same base.
  function siblings(all, model, base) {
    var key = clean(base, 128).toLowerCase(), seen = {}, out = [];
    if (!key) return out;
    seen[model] = true;
    (all || []).forEach(function (o) {
      if (o.online && !seen[o.model] && clean(o.base_model, 128).toLowerCase() === key) { seen[o.model] = true; out.push(o.model); }
    });
    return out.sort();
  }

  // lines is the block as [{label, text}] (label "" = a sentence), plus link and heading.
  function lines(all, model, opt) {
    opt = opt || {};
    var caps = !!opt.caps, mine = (all || []).filter(function (o) { return o.model === model; });
    var d = fold(mine), f = facts(mine, d), out = [];
    var lab = function (s) { return caps ? s.charAt(0).toUpperCase() + s.slice(1) + ": " : s + ": "; };
    var add = function (label, v) { if (v) out.push({ label: label ? lab(label) : "", text: v }); };
    var heading = "";
    if (d && d.source === "rogerai") heading = "RogerAI's description of this model";
    else if (d) heading = (caps ? "The" : "the") + " station's own words · " + d.declared_by + " of " + d.stations + (d.stations === 1 ? " station" : " stations");
    if (d) {
      add("", d.summary); add("best at", d.best_at); add("not for", d.not_for);
    } else {
      out.push({ label: "", text: caps ? "No description yet. The station has not said what this model is for." :
        "no description yet - the station has not said what this model is for" });
    }
    var vision = mine.some(function (o) { return (o.capabilities || []).indexOf("vision") >= 0; });
    add("kind", kind((mine[0] || {}).modality || "", vision, f.tuning, f.base));
    if (!(f.tuning === "adapter" && f.base)) add("base", f.base);
    var sib = siblings(all, model, f.base);
    if (sib.length) add("same base", sib.slice(0, 4).join(", ") + (sib.length > 4 ? " +" + (sib.length - 4) + " more" : ""));
    add("license", f.license);
    if (d && d.source === "rogerai" && !mine.some(function (o) { return o.confidential; })) {
      out.push({ label: "", text: (caps ? "Weights" : "weights") + " not verified: any station can use this model name." });
    }
    var link = d && d.page ? { text: caps ? "Model page" : "model page", href: (opt.origin || "") + d.page } : null;
    return { heading: heading, lines: out, link: link };
  }

  // render fills node with the block, by textContent only.
  function render(doc, node, all, model, opt) {
    while (node.firstChild) node.removeChild(node.firstChild);
    var b = lines(all, model, opt);
    var p = function (cls, t) { var e = doc.createElement("p"); e.className = cls; e.textContent = t; node.appendChild(e); return e; };
    if (b.heading) p("about__head", b.heading);
    b.lines.forEach(function (l) { p(l.label ? "about__line" : "about__text", l.label + l.text); });
    if (b.link) {
      var a = doc.createElement("a");
      a.className = "about__link"; a.textContent = b.link.text; a.href = b.link.href; a.rel = "noopener";
      node.appendChild(a);
    }
    return b;
  }

  root.RogerDetails = { clean: clean, fold: fold, facts: facts, kind: kind, siblings: siblings, lines: lines, render: render, sitePath: sitePath };
})(typeof window !== "undefined" ? window : this);
