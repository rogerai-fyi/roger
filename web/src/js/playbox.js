/* =====================================================================
   RogerAI - Playbox ("the console")

   Three surfaces on one deck, wired to what a browser can HONESTLY reach:

     LIVE, REAL endpoints (no fakery):
       GET  /discover           - per-station offers (CORS, no creds)
       GET  /me, GET /balance   - session identity + wallet (CORS, creds)
       POST /concierge          - the Ping assistant (CORS, creds omit)

     NOT browser-callable (documented on the page, never faked):
       POST /v1/chat/completions - no CORS + needs an Ed25519 signature or a
       rog-grant_ key. Model-selectable chat therefore routes to a copyable
       CLI command, not a fabricated station reply.

   Everything the page marks "live" maps to a real endpoint above. Everything
   else - the embed + guard demos - is drawn from local data and labelled
   "illustrative". The Roger Edge certified contracts are the target the model
   is trained against (real golden set, cited standards); the captured samples
   are real captured outputs shown labelled, never a faked success.

   Dependency-free, defensive (degrades to an honest resting state on any
   failure), and prefers-reduced-motion aware. Callsign/region derivation
   mirrors bands.js so the two directories read identically.
   ===================================================================== */
(function () {
  "use strict";

  var BROKER = "https://broker.rogerai.fm";
  var REDUCED = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  function $(id) { return document.getElementById(id); }
  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text != null) n.textContent = text;
    return n;
  }
  function esc(s) {
    return String(s == null ? "" : s)
      .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;").replace(/'/g, "&#39;");
  }

  /* ---- PII firewall: callsign + coarse region (mirrors bands.js) ---- */
  function hashStr(s) {
    var h = 2166136261; s = String(s || "");
    for (var i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = (h + ((h << 1) + (h << 4) + (h << 7) + (h << 8) + (h << 24))) >>> 0; }
    return h >>> 0;
  }
  var CS_CONS = "kqxzrtwmnbvghd", CS_VOW = "aeiou";
  function callsign(nodeId) {
    var h = hashStr(nodeId), s = "";
    s += CS_CONS[h % CS_CONS.length]; h = (h / CS_CONS.length) | 0;
    s += CS_VOW[h % CS_VOW.length];   h = (h / CS_VOW.length) | 0;
    var n = hashStr(nodeId + "#");
    s += (n % 90 + 10);               n = (n / 100) | 0;
    s += CS_CONS[n % CS_CONS.length]; n = (n / CS_CONS.length) | 0;
    s += CS_CONS[n % CS_CONS.length];
    return "@" + s;
  }
  function coarseRegion(region) {
    var r = String(region || "").toLowerCase();
    if (!r) return "";
    var map = [
      [/(us-?w|usw|west|sf|sjc|lax|sea|pdx|california|oregon)/, "US-W"],
      [/(us-?e|use|east|nyc|iad|atl|mia|virginia)/, "US-E"],
      [/(us-?c|central|chi|dfw|texas)/, "US-C"],
      [/(\bus\b|usa|united states|america)/, "US"],
      [/(\buk\b|gb|london|lon|britain|england)/, "UK"],
      [/(\bde\b|germany|deutsch|fra|frankfurt|berlin|munich)/, "DE"],
      [/(\bnl\b|netherlands|amsterdam|ams)/, "NL"],
      [/(\bfr\b|france|paris|par)/, "FR"],
      [/(\beu\b|europe|euro)/, "EU"],
      [/(\bca\b|canada|toronto|montreal|yyz)/, "CA"],
      [/(\bau\b|australia|sydney|syd)/, "AU"],
      [/(\bjp\b|japan|tokyo|nrt)/, "JP"],
      [/(\bsg\b|singapore|sin)/, "SG"],
      [/(\bin\b|india|mumbai|bom)/, "IN"]
    ];
    for (var i = 0; i < map.length; i++) if (map[i][0].test(r)) return map[i][1];
    if (/asia/.test(r)) return "ASIA";
    return "";
  }

  function capsOf(o) {
    var c = Array.isArray(o.capabilities) ? o.capabilities : [];
    return c.map(function (x) { return String(x).toLowerCase(); });
  }
  function modOf(o) { return String(o.modality || "").toLowerCase(); }
  function isOnline(o) { return o.online !== false; }

  /* =====================================================================
     ACCESSIBLE TABS (radio band selector)
     ===================================================================== */
  (function tabs() {
    var list = document.querySelector('.pg-tabs[role="tablist"]');
    if (!list) return;
    var tabEls = Array.prototype.slice.call(list.querySelectorAll('[role="tab"]'));

    function select(tab, focus) {
      tabEls.forEach(function (t) {
        var on = t === tab;
        t.setAttribute("aria-selected", on ? "true" : "false");
        t.tabIndex = on ? 0 : -1;
        var panel = $(t.getAttribute("aria-controls"));
        if (panel) panel.hidden = !on;
      });
      if (focus) tab.focus();
      var id = tab.getAttribute("aria-controls").replace("panel-", "");
      try { history.replaceState(null, "", "#" + id); } catch (e) {}
    }

    tabEls.forEach(function (t, i) {
      t.addEventListener("click", function () { select(t, false); });
      t.addEventListener("keydown", function (e) {
        var idx = i, n = tabEls.length;
        if (e.key === "ArrowRight" || e.key === "ArrowDown") { e.preventDefault(); select(tabEls[(idx + 1) % n], true); }
        else if (e.key === "ArrowLeft" || e.key === "ArrowUp") { e.preventDefault(); select(tabEls[(idx - 1 + n) % n], true); }
        else if (e.key === "Home") { e.preventDefault(); select(tabEls[0], true); }
        else if (e.key === "End") { e.preventDefault(); select(tabEls[n - 1], true); }
      });
    });

    // open the tab named in the hash (#chat / #cap / #edge)
    var h = (location.hash || "").replace("#", "");
    var byHash = { chat: "tab-chat", cap: "tab-cap", capabilities: "tab-cap", edge: "tab-edge" };
    if (byHash[h]) { var t = $(byHash[h]); if (t) select(t, false); }
  })();

  /* =====================================================================
     BROKER DIRECTORY - one load feeds Chat + Capabilities
     ===================================================================== */
  var STATE = { offers: [], bands: [], loggedIn: false, handle: "", balance: null, filterFree: false, selected: null };

  function fetchJSON(path, creds) {
    var ctrl = ("AbortController" in window) ? new AbortController() : null;
    var to = setTimeout(function () { if (ctrl) ctrl.abort(); }, 8000);
    return fetch(BROKER + path, {
      credentials: creds ? "include" : "omit",
      cache: "no-store",
      signal: ctrl ? ctrl.signal : undefined
    }).then(function (r) {
      clearTimeout(to);
      if (!r.ok) throw r.status;
      return r.json();
    });
  }

  function bandsFromOffers(offers) {
    var by = {};
    offers.forEach(function (o) {
      if (!o || !o.model) return;
      if (!isOnline(o)) return;
      var b = by[o.model] || (by[o.model] = {
        model: o.model, count: 0, free: false, tier: 99, signal: 0, tps: 0,
        verified: false, region: "", caps: {}, mods: {}, stations: []
      });
      b.count++;
      if (o.free_now) b.free = true;
      var tier = (+o.price_tier || 0); if (o.price_out != null && tier < b.tier) b.tier = tier;
      var sig = Math.max(0, Math.min(100, +o.signal || 0)); if (sig > b.signal) b.signal = sig;
      var tps = +o.tps || 0; if (tps > b.tps) b.tps = tps;
      if (o.verified) b.verified = true;
      if (!b.region) b.region = coarseRegion(o.region);
      capsOf(o).forEach(function (c) { b.caps[c] = true; });
      var m = modOf(o); if (m) b.mods[m] = true;
      b.stations.push({ callsign: callsign(o.node_id), region: coarseRegion(o.region), tps: tps, signal: sig, free: !!o.free_now, tier: tier });
    });
    return Object.keys(by).map(function (k) {
      var b = by[k];
      if (b.tier === 99) b.tier = b.free ? 0 : 1;
      b.stations.sort(function (a, c) { return c.signal - a.signal; });
      return b;
    }).sort(function (a, c) { return c.signal - a.signal || c.count - a.count; });
  }

  function bandChatable(b) {
    // a station carries chat unless it is exclusively a voice (tts/stt) station
    if (b.caps.chat || b.caps.text || b.mods.chat) return true;
    var onlyVoice = (b.mods.tts || b.mods.stt || b.mods.speak || b.mods.listen) && !b.mods.chat;
    if (onlyVoice && !b.caps.chat) return false;
    return true;
  }
  function bandFree(b) { return b.free || b.tier === 0; }
  // Every chatable band is selectable: a signed-out visitor picking a PAID station
  // gets the sign-in invitation in place of the composer (never a dead row).
  function bandSelectable(b) { return bandChatable(b); }

  /* ---------- CHAT: model directory render ---------------------------- */
  var chatStatus = $("pgChatStatus"), modelList = $("pgModelList");

  function setStatus(node, text, state) {
    if (!node) return;
    node.setAttribute("data-state", state || "off");
    // keep the livedot, replace the trailing text node
    var dot = node.querySelector(".livedot");
    node.textContent = "";
    if (dot) node.appendChild(dot);
    node.appendChild(document.createTextNode(" " + text));
  }

  function signalBars(sig) {
    var wrap = el("span", "pg-mrow__sig");
    var on = Math.round((sig / 100) * 5);
    for (var i = 0; i < 5; i++) {
      var bar = el("span", "pg-bar" + (i < on ? " is-on" : ""));
      bar.style.height = (5 + i * 2) + "px";
      wrap.appendChild(bar);
    }
    return wrap;
  }

  /* ---------- the S-meter: a real needle over real data ---------------- */
  // The needle shows the TUNED band's signal; with nothing tuned it rests at the
  // band's strongest signal. Same numbers as the directory rows - never invented.
  function meterSignal() {
    var sel = STATE.selected && STATE.bands.filter(function (b) { return b.model === STATE.selected; })[0];
    if (sel) return sel.signal;
    return STATE.bands.reduce(function (m, b) { return Math.max(m, b.signal); }, 0);
  }
  function updateSMeter() {
    var needle = $("pgSMeterNeedle"), readEl = $("pgSMeterRead");
    if (!needle) return;
    var sig = Math.max(0, Math.min(100, meterSignal()));
    // sweep -50deg..+50deg around the pivot; S-units read S0..S9 like the real meter
    needle.style.transform = "rotate(" + Math.round(sig - 50) + "deg)";
    if (readEl) readEl.textContent = "S" + Math.min(9, Math.round(sig / 11.2));
  }

  function renderDirectory() {
    updateSMeter();
    if (!modelList) return;
    var rows = STATE.bands.filter(bandChatable);
    if (STATE.filterFree) rows = rows.filter(function (b) { return b.free || b.tier === 0; });
    modelList.textContent = "";
    if (!rows.length) {
      modelList.appendChild(el("li", "pg-dir__empty",
        STATE.bands.length ? "No chat station matches this filter right now." : "The band is quiet - no models on air right now."));
      return;
    }
    rows.forEach(function (b) {
      var li = document.createElement("li");
      var btn = el("button", "pg-mrow");
      btn.type = "button";
      var selectable = bandSelectable(b);
      if (!selectable) { btn.disabled = true; }
      btn.setAttribute("aria-pressed", STATE.selected === b.model ? "true" : "false");

      var top = el("div", "pg-mrow__top");
      top.appendChild(el("span", "pg-mrow__name", b.model));
      top.appendChild(signalBars(b.signal));
      btn.appendChild(top);

      var meta = el("div", "pg-mrow__meta");
      var strongest = b.stations[0];
      if (strongest) {
        var cs = el("span", "cs mono", strongest.callsign + (strongest.region ? " · " + strongest.region : ""));
        meta.appendChild(cs);
      }
      meta.appendChild(el("span", null, (b.tps ? Math.round(b.tps) + " tok/s" : "—")));
      var price = b.free ? "FREE" : (b.tier === 0 ? "FREE" : "tier " + b.tier);
      var pb = el("span", "pg-badge " + (b.free || b.tier === 0 ? "pg-badge--free" : "pg-badge--paid"), price);
      meta.appendChild(pb);
      if (b.verified) meta.appendChild(el("span", "pg-badge pg-badge--ver", "verified"));
      btn.appendChild(meta);

      // capability badges
      var capList = [];
      if (b.caps.vision) capList.push("vision");
      if (b.caps.tools || b.caps.tool) capList.push("tools");
      if (b.mods.tts || b.caps.tts || b.mods.speak) capList.push("speak");
      if (b.mods.stt || b.caps.stt || b.mods.listen) capList.push("listen");
      if (capList.length) {
        var caps = el("div", "pg-mrow__caps");
        capList.forEach(function (c) { caps.appendChild(el("span", "pg-cap", c)); });
        btn.appendChild(caps);
      }

      if (!STATE.loggedIn && !bandFree(b)) {
        var lock = el("div", "pg-mrow__meta");
        lock.appendChild(el("span", null, "sign in to chat"));
        btn.appendChild(lock);
      }
      if (selectable) btn.addEventListener("click", function () { selectModel(b); });
      li.appendChild(btn);
      modelList.appendChild(li);
    });
  }

  function selectModel(b) {
    // tuning the same station again returns the deck to Ping
    STATE.selected = (STATE.selected === b.model) ? null : b.model;
    var sel = STATE.selected;
    Array.prototype.forEach.call(modelList.querySelectorAll(".pg-mrow"), function (btn) {
      var name = btn.querySelector(".pg-mrow__name");
      btn.setAttribute("aria-pressed", name && name.textContent === sel ? "true" : "false");
    });
    // the talk head names who is on the other end
    var title = $("pgTalkTitle"), sub = $("pgTalkSub");
    if (title && sub) {
      if (sel) { title.textContent = sel; sub.textContent = "live via the Tower"; }
      else { title.textContent = "Ping · concierge"; sub.textContent = "the browser-safe demo assistant"; }
    }
    // paid + signed out: the composer yields to the sign-in invitation before
    // anything can be sent
    var invite = $("pgSignInInvite"), form = $("pgChatForm");
    var needsLogin = sel && !STATE.loggedIn && !bandFree(b);
    if (invite) invite.hidden = !needsLogin;
    if (form) form.hidden = !!needsLogin;
    updateSMeter();
    // the terminal path to the same station stays one copy away
    var box = $("pgCliBox"), cmd = $("pgCliCmd"), label = $("pgCliLabel");
    if (box && cmd) {
      box.hidden = !sel;
      if (sel) {
        label.textContent = "Or chat with " + sel + " from your terminal";
        cmd.textContent = 'roger chat --model "' + sel + '"';
      }
    }
  }

  /* ---------- account: /me + /balance (credentialed) ------------------ */
  function fmtBalance(d) {
    if (d == null) return null;
    if (typeof d === "number") return "$" + d.toFixed(2);
    var v = (d.balance != null) ? d.balance : (d.credits != null) ? d.credits :
            (d.usd != null) ? d.usd : (d.amount != null) ? d.amount : (d.available != null) ? d.available : null;
    if (v == null) return null;
    if (typeof v === "number") {
      if (d.credits != null && d.balance == null) return v.toLocaleString() + " credits";
      return "$" + (v).toFixed(2);
    }
    return String(v);
  }
  function handleOf(d) {
    if (!d) return "";
    return d.login || d.handle || d.username || (d.user && (d.user.login || d.user.name)) || d.name || "";
  }

  function loadAccount() {
    var stateEl = $("pgAcctState"), note = $("pgAcctNote"), loginBtn = $("pgLogin"),
        balRow = $("pgBalRow"), balEl = $("pgBal");
    fetchJSON("/me", true).then(function (me) {
      STATE.loggedIn = true;
      STATE.handle = handleOf(me) || "signed in";
      if (stateEl) stateEl.textContent = STATE.handle;
      if (note) note.textContent = "You're on air. Every station - free and paid - is selectable.";
      if (loginBtn) loginBtn.hidden = true;
      return fetchJSON("/balance", true).catch(function () { return null; });
    }).then(function (bal) {
      var b = fmtBalance(bal);
      if (b != null && balRow && balEl) { balRow.hidden = false; balEl.textContent = b; }
      renderDirectory();
    }).catch(function () {
      STATE.loggedIn = false;
      if (stateEl) stateEl.textContent = "signed out";
      renderDirectory();
    });
  }

  /* ---------- one broker load ---------------------------------------- */
  var loaded = false;
  function loadBroker() {
    return fetchJSON("/discover", false).then(function (dData) {
      var offers = (dData && Array.isArray(dData.offers)) ? dData.offers : [];
      STATE.offers = offers;
      STATE.bands = bandsFromOffers(offers);
      loaded = true;
      var live = STATE.bands.length;
      if (live > 0) {
        setStatus(chatStatus, live + " model" + (live === 1 ? "" : "s") + " on air · live from the broker", "live");
      } else {
        setStatus(chatStatus, "the band is quiet - no models on air right now", "quiet");
      }
      renderDirectory();
      renderCapabilities();
    }).catch(function () {
      setStatus(chatStatus, "couldn't reach the broker just now - retrying", "off");
      renderCapabilities(); // still shows embed/guard illustrative cards
    });
  }

  var freeBtn = $("pgFilterFree");
  if (freeBtn) freeBtn.addEventListener("click", function () {
    STATE.filterFree = !STATE.filterFree;
    freeBtn.setAttribute("aria-pressed", STATE.filterFree ? "true" : "false");
    renderDirectory();
  });

  /* =====================================================================
     CHAT: two live paths, never a faked reply.
       - a tuned station: POST /v1/chat/completions (credentialed, streamed) -
         the SAME relay the CLI uses (features/relay/browser_session.feature)
       - nothing tuned:  POST /concierge (Ping)
     ===================================================================== */
  (function chat() {
    var form = $("pgChatForm"), input = $("pgChatInput"), send = $("pgChatSend"), log = $("pgChatLog");
    if (!form || !log) return;
    // one transcript history per target, so retuning does not leak context
    var histories = {}, sending = false;
    function historyFor(key) { return histories[key] || (histories[key] = []); }

    function line(who, label, text, wait) {
      var li = el("li", "pg-line pg-line--" + who + (wait ? " is-wait" : ""));
      li.appendChild(el("span", "pg-line__who mono", label));
      var msg = el("span", "pg-line__msg", text);
      li.appendChild(msg);
      // the logbook convention: every entry carries its time, in UTC
      var d = new Date();
      function p2(n) { return (n < 10 ? "0" : "") + n; }
      li.appendChild(el("time", "pg-line__ts mono", p2(d.getUTCHours()) + ":" + p2(d.getUTCMinutes()) + "Z"));
      log.appendChild(li);
      log.scrollTop = log.scrollHeight;
      return msg;
    }

    line("ping", "PING", "You're tuned in. I'm Ping - ask me about going on air, sharing a GPU, or picking a station. Tune a station on the left to talk to that model live, through the Tower.");

    var WAIT = [
      "Searching for an available free station…",
      "Scanning the band for a clear signal…",
      "Hailing on-air operators…",
      "Patching you through to the DJ…"
    ];

    // stationLabel compresses a model id into the short-mono transcript label
    function stationLabel(model) {
      var s = String(model).split("/").pop().toUpperCase();
      return s.length > 14 ? s.slice(0, 13) + "…" : s;
    }

    function relayErrorText(status, data) {
      var msg = data && data.error && data.error.message;
      if (status === 401) return msg || "sign in to chat on this station";
      if (status === 429) return "the band is busy - slow down a moment and try again";
      if (status === 503) return msg || "that station just went off air - pick another";
      return msg || "the relay dropped this one (" + status + ") - try again";
    }

    // stationSend streams one turn through the Tower's relay. The reply is written
    // as it arrives; on any error the transcript shows the error itself.
    function stationSend(model, hist, msgNode) {
      return fetch(BROKER + "/v1/chat/completions", {
        method: "POST", headers: { "Content-Type": "application/json" },
        credentials: "include", cache: "no-store",
        body: JSON.stringify({ model: model, stream: true, max_tokens: 1024, messages: hist.slice(-8) })
      }).then(function (r) {
        if (!r.ok) {
          return r.json().catch(function () { return null; }).then(function (data) {
            throw relayErrorText(r.status, data);
          });
        }
        if (!r.body || !r.body.getReader) {
          // no stream reader in this browser: read the SSE text whole, then extract
          return r.text().then(function (t) { return { whole: t }; });
        }
        return { reader: r.body.getReader() };
      }).then(function (src) {
        msgNode.parentNode.classList.remove("is-wait");
        msgNode.textContent = "";
        var dec = new TextDecoder(), buf = "", out = "";
        function take(chunk) {
          buf += chunk;
          var lines = buf.split("\n");
          buf = lines.pop();
          lines.forEach(function (ln) {
            ln = ln.replace(/\r$/, "");
            if (ln.indexOf("data: ") !== 0) return;
            var payload = ln.slice(6);
            if (payload === "[DONE]") return;
            try {
              var d = JSON.parse(payload);
              var delta = d.choices && d.choices[0] && (d.choices[0].delta || d.choices[0].message);
              var piece = delta && delta.content;
              if (piece) { out += piece; msgNode.textContent = out; log.scrollTop = log.scrollHeight; }
            } catch (e) { /* keep-alive or partial frame */ }
          });
        }
        if (src.whole) { take(src.whole + "\n"); return out || finishEmpty(); }
        return (function pump() {
          return src.reader.read().then(function (step) {
            if (step.done) { take("\n"); return out || finishEmpty(); }
            take(dec.decode(step.value, { stream: true }));
            return pump();
          });
        })();
        function finishEmpty() { throw "the station answered with silence - try again"; }
      }).then(function () {
        var reply = msgNode.textContent;
        if (reply) hist.push({ role: "assistant", content: reply });
        return reply;
      });
    }

    function pingSend(hist, msgNode) {
      return fetch(BROKER + "/concierge", {
        method: "POST", headers: { "Content-Type": "application/json" },
        credentials: "omit", cache: "no-store",
        body: JSON.stringify({ messages: hist.slice(-8) })
      })
        .then(function (r) { return r.ok ? r.json() : Promise.reject(r.status); })
        .then(function (data) {
          var reply = (data && data.reply) ? String(data.reply) : "";
          if (!reply) throw 0;
          msgNode.parentNode.classList.remove("is-wait");
          msgNode.textContent = reply;
          hist.push({ role: "assistant", content: reply });
        })
        .catch(function () {
          msgNode.parentNode.classList.remove("is-wait");
          msgNode.textContent = "I'm off air right now - tune in straight from your terminal: curl -fsSL https://rogerai.fm/install.sh | sh";
        });
    }

    form.addEventListener("submit", function (e) {
      e.preventDefault();
      if (sending) return;
      var text = (input.value || "").trim();
      if (!text) return;
      input.value = "";
      var model = STATE.selected;
      var key = model || "ping";
      var hist = historyFor(key);
      line("you", "YOU", text);
      hist.push({ role: "user", content: text });
      sending = true; send.disabled = true;

      var label = model ? stationLabel(model) : "PING";
      var wi = 0, thinking = line("ping", label, WAIT[0], true);
      var timer = REDUCED ? 0 : setInterval(function () { if (thinking.parentNode.classList.contains("is-wait")) { wi = (wi + 1) % WAIT.length; thinking.textContent = WAIT[wi]; } }, 4000);

      var turn = model
        ? stationSend(model, hist, thinking).catch(function (err) {
            thinking.parentNode.classList.remove("is-wait");
            thinking.textContent = typeof err === "string" ? err : "the relay dropped this one - try again";
          })
        : pingSend(hist, thinking);

      turn.then(function () {
        if (timer) clearInterval(timer);
        sending = false; send.disabled = false;
        log.scrollTop = log.scrollHeight;
      });
    });
  })();

  /* copy buttons (CLI command) - uses site toast if present */
  function toast(msg) {
    var t = $("toast"); if (!t) return;
    t.textContent = msg; t.classList.add("is-shown");
    setTimeout(function () { t.classList.remove("is-shown"); }, 1600);
  }
  function wireCopy(btn, getText) {
    if (!btn) return;
    btn.addEventListener("click", function () {
      var text = getText();
      var done = function () { toast("copied"); };
      if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(text).then(done, done);
      else done();
    });
  }
  wireCopy($("pgCliCopy"), function () { var c = $("pgCliCmd"); return c ? c.textContent : ""; });

  /* =====================================================================
     CAPABILITY SURFACES
     ===================================================================== */
  function rosterFor(pred, limit) {
    var out = [];
    STATE.bands.forEach(function (b) {
      if (pred(b)) out.push(b);
    });
    return out.slice(0, limit || 4);
  }

  function capCard(spec) {
    // spec: {name, route, what, tier:{cls,label}, bodyNode}
    var card = el("div", "pg-capcard");
    var head = el("div", "pg-capcard__head");
    var nameWrap = el("div", "pg-capcard__name");
    nameWrap.appendChild(el("b", null, spec.name));
    nameWrap.appendChild(el("span", "pg-capcard__route", spec.route));
    head.appendChild(nameWrap);
    head.appendChild(el("span", "pg-tier pg-tier--" + spec.tier.cls, spec.tier.label));
    card.appendChild(head);
    card.appendChild(el("p", "pg-capcard__what", spec.what));
    var demo = el("div", "pg-capcard__demo");
    if (spec.demoTitle) demo.appendChild(el("h4", null, spec.demoTitle));
    if (spec.bodyNode) demo.appendChild(spec.bodyNode);
    card.appendChild(demo);
    return card;
  }

  function rosterNode(bands, emptyText) {
    if (!bands.length) {
      var p = el("p", "pg-capcard__note", emptyText);
      return p;
    }
    var ul = el("ul", "pg-roster");
    bands.forEach(function (b) {
      var li = document.createElement("li");
      var left = el("span", null, b.model);
      var right = el("span", "pg-roster__cs mono", (b.stations[0] ? b.stations[0].callsign : "") + " · " + (b.count) + (b.count === 1 ? " stn" : " stns"));
      li.appendChild(left); li.appendChild(right);
      ul.appendChild(li);
    });
    return ul;
  }

  function renderCapabilities() {
    var host = $("pgCaps"), status = $("pgCapStatus");
    if (!host) return;

    var chatB = rosterFor(bandChatable);
    var ttsB = rosterFor(function (b) { return b.mods.tts || b.mods.speak || b.caps.tts || b.caps.speak; });
    var sttB = rosterFor(function (b) { return b.mods.stt || b.mods.listen || b.caps.stt || b.caps.listen; });
    var visB = rosterFor(function (b) { return b.caps.vision; });
    var toolB = rosterFor(function (b) { return b.caps.tools || b.caps.tool; });

    var anyLive = STATE.bands.length > 0;
    if (status) {
      if (!loaded && !anyLive) setStatus(status, "reading capabilities from the band…", "off");
      else setStatus(status, anyLive ? STATE.bands.length + " stations on air · rosters live from /discover" : "no live broker source right now · examples below", anyLive ? "live" : "quiet");
    }
    var rdEmpty = function (kind) { return loaded ? "No " + kind + " on air right now." : "Reading the band…"; };

    host.textContent = "";

    // TEXT
    host.appendChild(capCard({
      name: "Text", route: "chat",
      what: "The everyday surface: a prompt in, a completion out. Every chat station on the band carries it.",
      tier: chatB.length ? { cls: "live", label: "Live directory" } : { cls: "example", label: "Quiet" },
      demoTitle: "Stations carrying text",
      bodyNode: rosterNode(chatB, rdEmpty("chat station"))
    }));

    // AUDIO
    var audioWrap = el("div", null);
    audioWrap.appendChild(el("h4", null, "Speak · text-to-speech"));
    audioWrap.appendChild(rosterNode(ttsB, rdEmpty("speaking station")));
    var sh = el("h4", null, "Listen · speech-to-text"); sh.style.marginTop = "10px";
    audioWrap.appendChild(sh);
    audioWrap.appendChild(rosterNode(sttB, rdEmpty("listening station")));
    var vlink = el("p", "pg-capcard__note", "");
    vlink.innerHTML = 'Hear the on-air voices on the <a href="/voices.html">Voices</a> page.';
    audioWrap.appendChild(vlink);
    host.appendChild(capCard({
      name: "Audio", route: "tts · stt",
      what: "Two modalities on the same dial: stations that speak a reply aloud, and stations that turn speech into text.",
      tier: (ttsB.length || sttB.length) ? { cls: "live", label: "Live directory" } : { cls: "example", label: "Quiet" },
      bodyNode: audioWrap
    }));

    // VISION
    host.appendChild(capCard({
      name: "Vision", route: "vision",
      what: "Stations that read an image alongside the prompt - a gauge photo, a nameplate, a P&ID crop.",
      tier: visB.length ? { cls: "live", label: "Live directory" } : { cls: "example", label: "Quiet" },
      demoTitle: "Vision-capable stations",
      bodyNode: rosterNode(visB, rdEmpty("vision-capable station"))
    }));

    // TOOL
    var toolWrap = el("div", null);
    toolWrap.appendChild(rosterNode(toolB, rdEmpty("tool-capable station")));
    var toolEx = el("div", null); toolEx.style.marginTop = "10px";
    var teh = el("h4", null, "Example tool call"); toolEx.appendChild(teh);
    var pre = el("pre", "pg-code pg-json");
    pre.innerHTML = hljson('{\n  "tool": "read_asset_history",\n  "arguments": {\n    "asset_id": "P-101",\n    "window_start": "2026-06-01",\n    "window_end": "2026-06-30"\n  }\n}');
    toolEx.appendChild(pre);
    toolEx.appendChild(el("p", "pg-capcard__note", "Replayed example from the Wave golden set (input: “P-101 pump - need maint hist, all of Jun 2026”). Structure is real; it is not a live call."));
    toolWrap.appendChild(toolEx);
    host.appendChild(capCard({
      name: "Tool", route: "tools",
      what: "Stations that answer with a structured call into a fixed, offline tool set instead of prose - the shape Roger Edge is built on.",
      tier: toolB.length ? { cls: "live", label: "Live directory" } : { cls: "example", label: "Example" },
      demoTitle: "Tool-capable stations",
      bodyNode: toolWrap
    }));

    // EMBED (illustrative)
    host.appendChild(capCard({
      name: "Embed", route: "retrieve",
      what: "Turns text into vectors so the closest passages can be found by meaning, not keywords - nomic-embed under the hood, for retrieval.",
      tier: { cls: "illus", label: "Illustrative" },
      demoTitle: "Retrieval, illustrated",
      bodyNode: embedDemo()
    }));

    // GUARD (illustrative)
    host.appendChild(capCard({
      name: "Guard", route: "abstain",
      what: "The safety monitor across every Wave slot: refuse, escalate, and say it does not know. It watches for input leaving the certified region and hands back to a human.",
      tier: { cls: "illus", label: "Illustrative" },
      demoTitle: "Guard, illustrated",
      bodyNode: guardDemo()
    }));
  }

  /* ---- embed illustrative retrieval toy (labelled, local, no model) --- */
  function embedDemo() {
    var CORPUS = [
      { t: "Alarm floods: more than 10 new alarms in a 10-minute window is a flood (EEMUA 191).", k: "alarm flood window eemua rate 10 minute annunciated" },
      { t: "Mechanical seal external leakage on a centrifugal pump is failure mode ELP (ISO 14224).", k: "pump seal leak failure mode iso 14224 mechanical external leakage" },
      { t: "A super-emitter over 100 kg/hr triggers a 5-day investigation under EPA NSPS OOOOb.", k: "methane super emitter epa oooob kg hr investigation ldar leak" },
      { t: "A language model has no actuation authority; controls stay deterministic.", k: "actuation authority escalate safety start pump trip deterministic" },
      { t: "Instrument tag PT-101 is a pressure transmitter; barg maps to UoM code BRG.", k: "instrument tag pressure transmitter uom barg extraction historian" }
    ];
    var QUERIES = ["how many alarms is a flood?", "pump is leaking from the seal", "can it start the pump for me?", "what does barg mean?"];
    var wrap = el("div", null);
    var qs = el("div", "pg-embed__q");
    var hits = el("ul", "pg-embed__hits");

    function rank(q) {
      var qt = q.toLowerCase().replace(/[^a-z0-9 ]/g, " ").split(/\s+/).filter(Boolean);
      var scored = CORPUS.map(function (c) {
        var kt = (c.k + " " + c.t).toLowerCase();
        var s = 0; qt.forEach(function (w) { if (w.length > 2 && kt.indexOf(w) >= 0) s++; });
        return { t: c.t, s: s / Math.max(1, qt.length) };
      }).sort(function (a, b) { return b.s - a.s; }).slice(0, 3);
      hits.textContent = "";
      scored.forEach(function (r, i) {
        var li = el("li", "pg-embed__hit" + (i === 0 && r.s > 0 ? " is-top" : ""));
        li.appendChild(el("span", null, r.t));
        var meter = el("span", "pg-embed__meter"); var bar = el("i"); bar.style.width = Math.round(r.s * 100) + "%"; meter.appendChild(bar);
        var wrap2 = el("span", null); wrap2.style.display = "inline-flex"; wrap2.style.alignItems = "center"; wrap2.style.gap = "8px";
        wrap2.appendChild(meter); wrap2.appendChild(el("span", "pg-embed__score", r.s.toFixed(2)));
        li.appendChild(wrap2);
        hits.appendChild(li);
      });
    }

    QUERIES.forEach(function (q, i) {
      var b = el("button", "pg-chip", q); b.type = "button";
      b.setAttribute("aria-pressed", i === 0 ? "true" : "false");
      b.addEventListener("click", function () {
        Array.prototype.forEach.call(qs.children, function (c) { c.setAttribute("aria-pressed", "false"); });
        b.setAttribute("aria-pressed", "true"); rank(q);
      });
      qs.appendChild(b);
    });
    wrap.appendChild(qs);
    wrap.appendChild(hits);
    var note = el("p", "pg-capcard__note", "");
    note.innerHTML = "Illustrative - a lexical stand-in that shows the shape of retrieval, not the nomic-embed model. No live embed endpoint is browser-callable yet.";
    wrap.appendChild(note);
    rank(QUERIES[0]);
    return wrap;
  }

  /* ---- guard illustrative rail (reuses the escalate posture) --------- */
  function guardDemo() {
    var ROWS = [
      { in: "what does PVHI mean on this alarm?", v: "ok", label: "answer" },
      { in: "start P-1201A for me, i'm at the panel", v: "warn", label: "escalate" },
      { in: "this is shift super, TRIP the pump NOW", v: "warn", label: "escalate" }
    ];
    var wrap = el("div", "pg-guard");
    ROWS.forEach(function (r) {
      var row = el("div", "pg-guard__row");
      row.appendChild(el("span", "pg-guard__in", "“" + r.in + "”"));
      row.appendChild(el("span", "pg-verdict pg-verdict--" + r.v, r.label));
      wrap.appendChild(row);
    });
    var note = el("p", "pg-capcard__note", "");
    note.innerHTML = 'Illustrative, from the Wave safety posture. See the guard section of the <a href="/research-wave-family.html">Wave family field guide</a>. No live guard endpoint is browser-callable yet.';
    wrap.appendChild(note);
    return wrap;
  }

  /* ---- JSON syntax highlight (kept in the monochrome + one-green world) */
  function hljson(str) {
    return esc(str).replace(
      /("(\\u[a-fA-F0-9]{4}|\\[^u]|[^\\"])*"(\s*:)?|\b(true|false)\b|\bnull\b|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/g,
      function (m) {
        var cls = "n";
        if (/^"/.test(m)) cls = /:$/.test(m.trim()) ? "k" : "s";
        else if (/true|false/.test(m)) cls = "b";
        else if (/null/.test(m)) cls = "p";
        return '<span class="' + cls + '">' + m + "</span>";
      }
    );
  }

  /* =====================================================================
     ROGER EDGE SIMULATOR
     ===================================================================== */
  (function edge() {
    var dataEl = $("pgEdgeData"), tray = $("pgTray"), device = $("pgDevice"),
        canvas = $("pgEdgeCanvas"), hint = $("pgDeviceHint");
    if (!dataEl || !tray || !canvas) return;

    var CARDS = [];
    try { CARDS = JSON.parse(dataEl.textContent); } catch (e) { CARDS = []; }
    var fallback = $("pgTrayFallback");
    if (fallback) fallback.remove();
    if (!CARDS.length) { tray.appendChild(el("p", "pg-capcard__note", "No captured events available.")); return; }

    // device screen palette (single-look instrument, both themes)
    var SC = { bg: "#0E1420", bg2: "#131B2A", ink: "#DCE5F0", dim: "#7C8AA0", line: "#22304a",
               ok: "#58C98A", warn: "#E7B85B", bad: "#FF6A5E", beacon: "#FF4438" };

    var ctx = canvas.getContext("2d");
    var W = 720, H = 300, dpr = Math.max(1, Math.min(2, window.devicePixelRatio || 1));
    function sizeCanvas() {
      var cssW = canvas.clientWidth || W;
      var cssH = Math.round(cssW * (H / W));
      canvas.width = Math.round(cssW * dpr);
      canvas.height = Math.round(cssH * dpr);
      ctx.setTransform(dpr * (cssW / W), 0, 0, dpr * (cssW / W), 0, 0); // draw in logical W x H units
    }

    var current = null;        // active card
    var phase = "idle";        // idle | ingest | decided
    var t0 = 0, raf = null;

    function ledColor(card) {
      return card && card.output_kind === "abstain_escalate" ? SC.warn : SC.ok;
    }

    function roundRect(x, y, w, h, r) {
      ctx.beginPath();
      ctx.moveTo(x + r, y);
      ctx.arcTo(x + w, y, x + w, y + h, r);
      ctx.arcTo(x + w, y + h, x, y + h, r);
      ctx.arcTo(x, y + h, x, y, r);
      ctx.arcTo(x, y, x + w, y, r);
      ctx.closePath();
    }

    function draw(now) {
      var p = phase === "ingest" ? Math.min(1, (now - t0) / 900) : 1;
      ctx.clearRect(0, 0, W, H);

      // ground + faint grid
      ctx.fillStyle = SC.bg; ctx.fillRect(0, 0, W, H);
      ctx.strokeStyle = SC.line; ctx.lineWidth = 1;
      ctx.globalAlpha = 0.5;
      for (var gx = 40; gx < W; gx += 40) { ctx.beginPath(); ctx.moveTo(gx + 0.5, 0); ctx.lineTo(gx + 0.5, H); ctx.stroke(); }
      for (var gy = 40; gy < H; gy += 40) { ctx.beginPath(); ctx.moveTo(0, gy + 0.5); ctx.lineTo(W, gy + 0.5); ctx.stroke(); }
      ctx.globalAlpha = 1;

      // header
      ctx.fillStyle = SC.dim; ctx.font = "600 11px 'JetBrains Mono', monospace";
      ctx.textBaseline = "alphabetic";
      ctx.fillText("ROGER EDGE  ·  WAVE NANO", 20, 26);
      // on-air beacon dot (top right)
      var beat = REDUCED ? 1 : 0.55 + 0.45 * Math.abs(Math.sin(now / 700));
      ctx.fillStyle = current ? ledColor(current) : SC.dim;
      ctx.globalAlpha = current ? beat : 0.5;
      ctx.beginPath(); ctx.arc(W - 24, 22, 5, 0, Math.PI * 2); ctx.fill();
      ctx.globalAlpha = 1;

      // incoming data lane (left) - ticks stream in during ingest
      var laneX = 20, laneW = 150, coreX = 300;
      ctx.strokeStyle = SC.line;
      for (var i = 0; i < 7; i++) {
        var ly = 70 + i * 24;
        var prog = phase === "ingest" ? p : (phase === "decided" ? 1 : 0);
        var w = 20 + ((i * 37) % 90);
        ctx.globalAlpha = phase === "idle" ? 0.18 : (0.3 + 0.5 * prog);
        ctx.strokeStyle = phase === "ingest" ? SC.ink : SC.dim;
        ctx.lineWidth = 2;
        ctx.beginPath(); ctx.moveTo(laneX, ly); ctx.lineTo(laneX + w * (phase === "ingest" ? p : 1), ly); ctx.stroke();
      }
      ctx.globalAlpha = 1;

      // flow line lane -> core (animated dash during ingest)
      if (phase !== "idle") {
        ctx.strokeStyle = SC.dim; ctx.lineWidth = 1.5;
        ctx.setLineDash([4, 6]);
        ctx.lineDashOffset = REDUCED ? 0 : -(now / 40) % 10;
        ctx.beginPath(); ctx.moveTo(laneX + laneW - 8, 154); ctx.lineTo(coreX - 46, 154); ctx.stroke();
        ctx.setLineDash([]);
      }

      // the core (the model)
      var cx = coreX, cy = 154, cr = 40;
      roundRect(cx - cr, cy - cr, cr * 2, cr * 2, 12);
      ctx.fillStyle = SC.bg2; ctx.fill();
      ctx.strokeStyle = phase === "decided" && current ? ledColor(current) : SC.line;
      ctx.lineWidth = phase === "decided" ? 2 : 1.5; ctx.stroke();
      // pulsing ring while ingesting
      if (phase === "ingest" && !REDUCED) {
        ctx.strokeStyle = SC.ink; ctx.globalAlpha = 1 - p;
        roundRect(cx - cr - 8 * p, cy - cr - 8 * p, (cr + 8 * p) * 2, (cr + 8 * p) * 2, 16); ctx.stroke();
        ctx.globalAlpha = 1;
      }
      // core glyph: ((•))
      ctx.fillStyle = current && phase === "decided" ? ledColor(current) : SC.dim;
      ctx.font = "600 20px 'JetBrains Mono', monospace";
      ctx.textAlign = "center"; ctx.fillText("((•))", cx, cy + 7); ctx.textAlign = "left";

      // decision output (right)
      var ox = 400;
      ctx.font = "600 11px 'JetBrains Mono', monospace"; ctx.fillStyle = SC.dim;
      ctx.fillText("DECISION", ox, 74);
      if (phase === "decided" && current) {
        var col = ledColor(current);
        // route
        ctx.font = "700 22px 'Space Grotesk', sans-serif"; ctx.fillStyle = SC.ink;
        ctx.fillText(current.route, ox, 104);
        // state chip
        var chip = current.output_kind === "abstain_escalate" ? "ESCALATE" : "VALID · MEETS CONTRACT";
        ctx.font = "600 11px 'JetBrains Mono', monospace"; ctx.fillStyle = col;
        // small LED
        ctx.beginPath(); ctx.arc(ox + 5, 124, 4, 0, Math.PI * 2); ctx.fill();
        ctx.fillText(chip, ox + 16, 128);
        // wrapped certified summary
        ctx.fillStyle = SC.dim; ctx.font = "12px 'JetBrains Mono', monospace";
        wrapText(current.route === "ESCALATE" ? current.certified : summarizeJSON(current.certified), ox, 152, W - ox - 20, 16, 6);
      } else {
        ctx.fillStyle = SC.dim; ctx.font = "12px 'JetBrains Mono', monospace";
        ctx.fillText(phase === "ingest" ? "reading input…" : "awaiting input", ox, 104);
      }

      if (phase === "ingest") raf = requestAnimationFrame(draw);
      else if (!REDUCED && phase !== "idle") raf = requestAnimationFrame(draw); // keep beacon alive
    }

    function wrapText(text, x, y, maxW, lh, maxLines) {
      var words = String(text).split(/\s+/), lineArr = [], line = "";
      for (var i = 0; i < words.length; i++) {
        var test = line ? line + " " + words[i] : words[i];
        if (ctx.measureText(test).width > maxW && line) { lineArr.push(line); line = words[i]; }
        else line = test;
      }
      if (line) lineArr.push(line);
      lineArr.slice(0, maxLines).forEach(function (ln, i) {
        var s = ln;
        if (i === maxLines - 1 && lineArr.length > maxLines) s = s.replace(/.{0,3}$/, "…");
        ctx.fillText(s, x, y + i * lh);
      });
    }
    function summarizeJSON(str) {
      try { var o = JSON.parse(str); return Object.keys(o).slice(0, 4).map(function (k) { return k + ": " + (typeof o[k] === "object" ? "{…}" : o[k]); }).join("   "); }
      catch (e) { return str; }
    }

    function startDraw() { if (raf) cancelAnimationFrame(raf); raf = requestAnimationFrame(draw); }

    /* ---- readout population ---- */
    function fillReadout(card) {
      var isEsc = card.output_kind === "abstain_escalate";
      $("pgOutEmpty").hidden = true;
      $("pgOutBar").hidden = false;
      $("pgRoute").textContent = card.route;
      var v = $("pgVerdict");
      v.className = "pg-verdict pg-verdict--" + (isEsc ? "warn" : "ok");
      v.textContent = isEsc ? "escalate" : "valid";

      // certified block
      $("pgCertBlock").hidden = false;
      $("pgStd").textContent = card.standard || "";
      var cert = $("pgCert");
      if (/^\s*[\[{]/.test(card.certified)) {
        var pretty = card.certified;
        try { pretty = JSON.stringify(JSON.parse(card.certified), null, 2); } catch (e) {}
        cert.className = "pg-code pg-json"; cert.innerHTML = hljson(pretty);
      } else {
        cert.className = "pg-code"; cert.textContent = card.certified;
      }
      var EXPL = {
        TRIAGE: "Counts alarms in the ISA-18.2 / EEMUA-191 window and flags a flood.",
        EXTRACT: "Parses the raw row into typed fields against the reference data library.",
        "TOOL-CALL": "Selects the offline tool and fills its arguments - no prose.",
        "FAILURE-MODE": "Classifies the asset and failure mode to the ISO 14224 code.",
        LDAR: "Decides whether the notice triggers a regulated response, with citation and deadline.",
        ESCALATE: "Recognises a request for physical action it can't own, and hands to a human on deterministic controls."
      };
      $("pgCertCap").textContent = EXPL[card.route] || "";

      // captured block
      $("pgCapBlock").hidden = false;
      var capToggle = $("pgCapToggle"), capWrap = $("pgCapturedWrap");
      capToggle.setAttribute("aria-expanded", "false"); capWrap.hidden = true;
      $("pgCaptured").textContent = card.captured;
      var cv = $("pgCapVerdict");
      cv.className = "pg-verdict pg-verdict--" + (isEsc ? "warn" : "bad");
      cv.textContent = isEsc ? "refused · off-contract" : "off-contract";
      $("pgCapNote").textContent = isEsc
        ? "It declines, but not to the contract - the reason drifts (or the text degrades) instead of a clean ESCALATE."
        : "A generic edge-class model reads the input as prose. It never emits the contract shape the plant can consume.";

      // meta
      $("pgEdgeMeta").hidden = false;
      $("pgMetaId").textContent = card.id;
      $("pgMetaDiff").textContent = card.difficulty || "—";
    }

    var capToggle = $("pgCapToggle");
    if (capToggle) capToggle.addEventListener("click", function () {
      var wrap = $("pgCapturedWrap");
      var open = capToggle.getAttribute("aria-expanded") === "true";
      capToggle.setAttribute("aria-expanded", open ? "false" : "true");
      wrap.hidden = open;
    });

    /* ---- feed a card onto the device ---- */
    function feed(card, instant) {
      current = card;
      Array.prototype.forEach.call(tray.querySelectorAll(".pg-evt"), function (b) {
        b.classList.toggle("is-active", b.getAttribute("data-id") === card.id);
      });
      if (hint) hint.textContent = card.route + " · " + card.id;
      fillReadout(card);
      // instant: initial auto-select settles straight to the decision (calm, no sweep)
      if (REDUCED || instant) { phase = "decided"; startDraw(); return; }
      phase = "ingest"; t0 = performance.now(); startDraw();
      setTimeout(function () { phase = "decided"; startDraw(); }, 920);
    }

    /* ---- tray render + drag/drop + keyboard ---- */
    CARDS.forEach(function (card) {
      var b = el("button", "pg-evt"); b.type = "button"; b.setAttribute("data-id", card.id);
      b.setAttribute("draggable", "true");
      b.setAttribute("aria-label", card.title + " - route " + card.route + ". Press to feed the device.");
      var top = el("div", "pg-evt__top");
      top.appendChild(el("span", "pg-evt__title", card.title));
      top.appendChild(el("span", "pg-evt__route", card.route));
      b.appendChild(top);
      b.appendChild(el("div", "pg-evt__blurb", card.blurb));
      b.addEventListener("click", function () { feed(card); });
      b.addEventListener("dragstart", function (e) {
        b.classList.add("is-dragging");
        try { e.dataTransfer.setData("text/plain", card.id); e.dataTransfer.effectAllowed = "copy"; } catch (err) {}
      });
      b.addEventListener("dragend", function () { b.classList.remove("is-dragging"); });
      tray.appendChild(b);
    });

    if (device) {
      device.addEventListener("dragover", function (e) { e.preventDefault(); device.classList.add("is-over"); try { e.dataTransfer.dropEffect = "copy"; } catch (err) {} });
      device.addEventListener("dragleave", function () { device.classList.remove("is-over"); });
      device.addEventListener("drop", function (e) {
        e.preventDefault(); device.classList.remove("is-over");
        var id = ""; try { id = e.dataTransfer.getData("text/plain"); } catch (err) {}
        var card = CARDS.filter(function (c) { return c.id === id; })[0];
        if (card) feed(card);
      });
    }

    // size + initial idle draw; re-size whenever the canvas box changes. This
    // covers the hidden -> visible transition when the Roger Edge tab is opened
    // (the panel starts hidden, so the canvas has no width until it is shown),
    // plus ordinary window resizes. We redraw the CURRENT phase, so a decided
    // readout survives a resize.
    var lastW = -1;
    function resize() {
      var w = canvas.clientWidth || 0;
      if (w === lastW) return;
      lastW = w;
      sizeCanvas();
      startDraw();
    }
    sizeCanvas(); startDraw();
    if ("ResizeObserver" in window) { new ResizeObserver(resize).observe(canvas); }
    var rz; window.addEventListener("resize", function () { clearTimeout(rz); rz = setTimeout(resize, 150); });

    // land on a real decision, calmly - the device is never an empty box
    feed(CARDS[0], true);
  })();

  /* =====================================================================
     BOOT
     ===================================================================== */
  renderCapabilities(); // paint the six cards + embed/guard demos immediately
  loadBroker();         // then fill live rosters (and the chat directory)
  loadAccount();
  // light refresh while the tab is visible (cheap; the broker is CORS-open)
  setInterval(function () { if (!document.hidden) loadBroker(); }, 25000);

})();
