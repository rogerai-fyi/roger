// Base Station (private.html) - the web half of /remote-control (v5.0.0). It lists the
// owner's remote agent sessions + private bands, opens a LIVE session view (continue the chat
// running on another machine), and mints a one-time link code to continue on a phone.
//
// Auth is the session cookie (credentialed CORS; the web origin is echoed). To VIEW a session
// the browser owner-JOINs it by id (POST /rc/{id}/join) - no code needed for your own
// already-logged-in surface; the code is only for linking a NOT-logged-in device. Streaming
// uses fetch()+ReadableStream (not EventSource, which cannot set the X-Roger-Attach header).
// Honest labels: PRIVATE = account-locked + broker-relayed, NOT end-to-end encrypted.
//
// A small PURE core is exposed as window.RCView (frameLine / stateLabel) for unit tests.
(function () {
  "use strict";

  var BROKER = "https://broker.rogerai.fyi";
  function api(path, opts) { opts = opts || {}; opts.credentials = "include"; return fetch(BROKER + path, opts); }
  function $(id) { return document.getElementById(id); }
  function show(id) { var el = $(id); if (el) el.hidden = false; }
  function hide(id) { var el = $(id); if (el) el.hidden = true; }
  function on(id, ev, fn) { var el = $(id); if (el) el.addEventListener(ev, fn); }

  // ---- PURE core (unit-tested) ----------------------------------------------

  // stateLabel maps a roster session to its {dot, label} — the one red glint is the live dot.
  function stateLabel(s) {
    if (s.revoked) return { dot: "·", label: "ended", live: false };
    if (s.online) return { dot: "◉", label: "live", live: true };
    return { dot: "○", label: "offline", live: false };
  }

  // frameLine turns one RCFrame into a {cls, text} the stream renders. Never trusts a frame to
  // be safe HTML — the caller sets textContent, so this returns plain strings.
  function frameLine(f) {
    switch (f.kind) {
      case "user": return { cls: "rc-user", text: "▸ (" + (f.origin || "someone") + ") " + (f.text || "") };
      case "assistant":
      case "final": return f.text && f.text.trim() ? { cls: "rc-asst", text: "◂ " + f.text } : null;
      case "tool_call": return { cls: "rc-tool", text: "◉ " + (f.tool || "") };
      case "tool_result": return { cls: "rc-tool", text: "✓ " + (f.tool || "") };
      case "confirm_req": return { cls: "rc-confirm", text: "? " + (f.tool || "") + " - approve? (runs on the host)", confirm: f.confirm_id || "" };
      case "confirm_done": return { cls: "rc-tool", text: "✓ " + (f.approve ? "approved" : "denied") + " from " + (f.origin || "") };
      // A guest-operator handoff (or the DJ-back return): render it dim so the viewer never sees
      // the stream go dead mid-handoff. Operator-aware + content-blind (only the guest name plus
      // the model/spend metadata ride the frame); a status with neither renders nothing.
      // Enriched copy (founder ruling 3): "<op> has the mic on <model> · $<spend>" - spend
      // formatted like the desk summary ($0.19); no model drops "on <model>", zero spend drops
      // "· $", and a frame with neither degrades to the pre-enrichment line (an old host).
      case "status": {
        var st = f.text || "";
        if (f.operator) {
          st = "◉ guest has the mic: " + f.operator;
          var spend = typeof f.spend === "number" ? f.spend : 0;
          if (f.model || spend > 0) {
            st = "◉ " + f.operator + " has the mic";
            if (f.model) st += " on " + f.model;
            if (spend > 0) st += " · $" + spend.toFixed(2);
          }
        }
        return st.trim() ? { cls: "rc-status", text: st } : null;
      }
      case "backfill": return f.text && f.text.trim() ? { cls: "rc-backfill", text: f.text } : null;
      case "error": return { cls: "rc-err", text: "✗ " + (f.text || "") };
      case "ended": return { cls: "rc-tool", text: "— session ended on the host —", ended: true };
      default: return null;
    }
  }

  window.RCView = { stateLabel: stateLabel, frameLine: frameLine };

  // ---- state ----------------------------------------------------------------

  var current = null; // { id, name, attach, reader, pendingConfirm }

  // ---- roster ---------------------------------------------------------------

  function loadRoster() {
    api("/rc/sessions").then(function (r) {
      if (r.status === 401) { hide("loading"); show("gate"); return; }
      if (!r.ok) throw new Error("sessions " + r.status);
      return r.json();
    }).then(function (data) {
      if (!data) return;
      hide("loading"); show("sessionsWrap"); show("bandsWrap");
      renderSessions(data.sessions || []);
      loadBands();
    }).catch(function () { hide("loading"); show("error"); });
  }

  function loadBands() {
    api("/bands").then(function (r) { return r.ok ? r.json() : { bands: [] }; })
      .then(function (data) { renderBands((data && data.bands) || []); })
      .catch(function () { renderBands([]); });
  }

  function renderSessions(sessions) {
    var body = $("sessionRows");
    if (!body) return;
    body.textContent = "";
    if (!sessions.length) { show("sessionsEmpty"); hide("sessionsTable"); return; }
    hide("sessionsEmpty"); show("sessionsTable");
    sessions.forEach(function (s) {
      var st = stateLabel(s);
      var tr = document.createElement("tr");
      tr.appendChild(cell(st.dot, "cn-st" + (st.live ? " is-live" : "")));
      tr.appendChild(cell(s.name || s.id, ""));
      tr.appendChild(cell(st.label, ""));
      var act = document.createElement("td"); act.className = "num";
      if (!s.revoked) {
        act.appendChild(btn(s.online ? "Open" : "View", function () { openSession(s); }, "primary"));
        act.appendChild(btn("Link", function () { linkPhone(s); }, "ghost"));
      }
      tr.appendChild(act);
      body.appendChild(tr);
    });
  }

  function renderBands(bands) {
    var body = $("bandRows");
    if (!body) return;
    body.textContent = "";
    if (!bands.length) { show("bandsEmpty"); hide("bandsTable"); return; }
    hide("bandsEmpty"); show("bandsTable");
    bands.forEach(function (b) {
      var live = b.status === "active";
      var tr = document.createElement("tr");
      tr.appendChild(cell(live ? "◉" : "·", "cn-st" + (live ? " is-live" : "")));
      tr.appendChild(cell(b.label || b.node_id || "-", ""));
      tr.appendChild(cell(b.display || "-", ""));
      tr.appendChild(cell(b.status || "-", ""));
      body.appendChild(tr);
    });
  }

  // ---- live session view ----------------------------------------------------

  function openSession(s) {
    api("/rc/" + encodeURIComponent(s.id) + "/join", { method: "POST" })
      .then(function (r) { if (!r.ok) throw new Error("join " + r.status); return r.json(); })
      .then(function (res) { openWithToken(s.id, s.name, res.attach_token); })
      .catch(function () { $("stream").textContent = "Could not attach to this session."; show("viewWrap"); });
  }

  // openWithToken opens the live view with an attach token we already hold (the owner-join
  // above, or the /r link flow which stashed one in sessionStorage). It first tears down any
  // stream already open, so re-opening (or a double-click while join is in flight) never leaks
  // an orphaned reader whose frames would bleed into the new session's transcript.
  function openWithToken(id, name, attach) {
    stopStream();
    current = { id: id, name: name, attach: attach, pendingConfirm: "" };
    $("viewTitle").textContent = "Session - " + (name || id);
    $("stream").textContent = "";
    show("viewWrap");
    streamSession();
  }

  // stopStream cancels the current session's SSE reader (idempotent).
  function stopStream() {
    if (current && current.reader) { try { current.reader.cancel(); } catch (e) {} current.reader = null; }
  }

  // pendingOpen consumes a session stashed by the /r link flow (r.js), so opening a QR link
  // lands the phone straight in the live session.
  function pendingOpen() {
    var raw;
    try { raw = sessionStorage.getItem("rc_open"); sessionStorage.removeItem("rc_open"); } catch (e) { return; }
    if (!raw) return;
    try {
      var o = JSON.parse(raw);
      if (o && o.id && o.attach) openWithToken(o.id, o.name, o.attach);
    } catch (e) {}
  }

  function appendLine(line) {
    if (!line) return;
    var stream = $("stream");
    if (line.ended) { stopStream(); current && (current.pendingConfirm = ""); }
    if (line.confirm !== undefined) {
      // Capture THIS confirm's id in each button's closure. The shared pendingConfirm is only
      // for the ask-box y/n shortcut (latest confirm); the per-line buttons must answer the
      // exact confirm they were drawn for, even when more than one is pending on the host.
      var cid = line.confirm;
      current && (current.pendingConfirm = cid);
      var wrap = document.createElement("div"); wrap.className = "rc-line rc-confirm";
      var span = document.createElement("span"); span.textContent = line.text; wrap.appendChild(span);
      wrap.appendChild(btn("Approve", function () { answerConfirm(true, cid); }, "primary"));
      wrap.appendChild(btn("Deny", function () { answerConfirm(false, cid); }, "ghost"));
      stream.appendChild(wrap);
    } else {
      var div = document.createElement("div"); div.className = "rc-line " + line.cls;
      div.textContent = line.text; stream.appendChild(div);
    }
    stream.scrollTop = stream.scrollHeight;
  }

  function streamSession() {
    if (!current) return;
    api("/rc/" + encodeURIComponent(current.id) + "/stream", {
      headers: { "X-Roger-Attach": current.attach },
    }).then(function (r) {
      if (!r.ok || !r.body) throw new Error("stream " + r.status);
      var reader = r.body.getReader();
      current.reader = reader;
      var dec = new TextDecoder(), buf = "";
      (function pump() {
        reader.read().then(function (res) {
          if (res.done) return;
          buf += dec.decode(res.value, { stream: true });
          var parts = buf.split("\n");
          buf = parts.pop();
          parts.forEach(function (ln) {
            if (ln.indexOf("data:") !== 0) return;
            try {
              var f = JSON.parse(ln.slice(5).trim());
              appendLine(frameLine(f));
            } catch (e) { /* ignore a partial/garbled line */ }
          });
          pump();
        }).catch(function () {});
      })();
    }).catch(function () { appendLine({ cls: "rc-err", text: "✗ stream error" }); });
  }

  function sendInbound(inb) {
    if (!current) return Promise.resolve();
    return api("/rc/" + encodeURIComponent(current.id) + "/send", {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Roger-Attach": current.attach },
      body: JSON.stringify(inb),
    });
  }
  function answerConfirm(approve, id) {
    if (!current) return;
    if (id === undefined) { id = current.pendingConfirm; } // ask-box y/n shortcut: latest confirm
    current.pendingConfirm = "";
    sendInbound({ kind: "confirm", approve: approve, confirm_id: id, origin: "web" })
      .catch(function () { appendLine({ cls: "rc-err", text: "✗ could not send the answer" }); });
  }

  function closeView() {
    stopStream();
    current = null;
    hide("viewWrap");
  }

  // ---- link a phone ---------------------------------------------------------

  function linkPhone(s) {
    api("/rc/" + encodeURIComponent(s.id) + "/code", { method: "POST" })
      .then(function (r) { if (!r.ok) throw new Error("code " + r.status); return r.json(); })
      .then(function (res) {
        $("linkName").textContent = s.name || s.id;
        var short = res.code_short || "";
        $("linkCode").textContent = short;
        var url = "https://rogerai.fyi/r.html#" + short;
        var a = $("linkURL"); a.textContent = url; a.href = url;
        $("copyLink").setAttribute("data-url", url);
        show("linkWrap");
      })
      .catch(function () {});
  }

  // ---- dom helpers ----------------------------------------------------------

  function cell(t, cls) { var td = document.createElement("td"); if (cls) td.className = cls; td.textContent = t; return td; }
  function btn(label, fn, kind) {
    var b = document.createElement("button"); b.type = "button";
    b.className = "rc-btn " + (kind || "ghost"); b.textContent = label;
    b.addEventListener("click", fn); return b;
  }

  // ---- wire-up --------------------------------------------------------------

  document.addEventListener("DOMContentLoaded", function () {
    var card = $("card"); if (card) card.hidden = false;
    on("closeView", "click", closeView);
    on("copyLink", "click", function (e) {
      var url = e.target.getAttribute("data-url") || "";
      if (url && navigator.clipboard) navigator.clipboard.writeText(url);
    });
    on("whatPrivate", "click", function (e) {
      e.preventDefault();
      alert("Private means account-locked and code-gated. Traffic is carried over TLS through "
        + "the RogerAI broker relay - it is not end-to-end encrypted, and tools run on the host "
        + "machine after you confirm them.");
    });
    var askForm = $("askForm");
    if (askForm) askForm.addEventListener("submit", function (e) {
      e.preventDefault();
      var v = ($("ask").value || "").trim();
      if (!v || !current) return;
      $("ask").value = "";
      // A bare y/n answers the latest pending confirm; otherwise it's a turn.
      var fail = function () { appendLine({ cls: "rc-err", text: "✗ could not send" }); };
      if (current.pendingConfirm && (/^(y|yes|n|no)$/i).test(v)) {
        var approve = /^y/i.test(v); var id = current.pendingConfirm; current.pendingConfirm = "";
        sendInbound({ kind: "confirm", approve: approve, confirm_id: id, origin: "web" }).catch(fail);
      } else {
        sendInbound({ kind: "turn", text: v, origin: "web" }).catch(fail);
      }
    });
    loadRoster();
    pendingOpen(); // if we arrived via a /r link, open that session straight away
  });
})();
