/* =====================================================================
   RogerAI - /models : the abuse / report-a-station flow.

   A small accessible modal opened by any [.report-btn]. It posts to the
   broker:
       POST https://broker.rogerai.fyi/report
       { "category": "abuse|csam|spam|quality|other",
         "node_id":    "<the station, may be empty>",
         "request_id": "<optional>",
         "detail":     "<free text>" }
   Anonymous is allowed (no auth header is required; the page sends none).
   Graceful success/failure copy; never throws into the page. CSP-safe:
   external (script-src 'self'), posts to the broker (connect-src allows it).
   Reduced-motion + narrow safe (no animation depended upon; the modal is a
   plain flex overlay). Page stays usable with this script absent.
   ===================================================================== */
(function () {
  "use strict";

  var BROKER = "https://broker.rogerai.fyi";

  var modal    = document.getElementById("reportModal");
  if (!modal) return;
  var scrim    = document.getElementById("reportScrim");
  var dialog   = modal.querySelector(".reportmodal__dialog");
  var closeBtn = document.getElementById("reportClose");
  var cancelBtn= document.getElementById("reportCancel");
  var form     = document.getElementById("reportForm");
  var catEl    = document.getElementById("reportCategory");
  var reqEl    = document.getElementById("reportRequestId");
  var detailEl = document.getElementById("reportDetail");
  var submitEl = document.getElementById("reportSubmit");
  var statusEl = document.getElementById("reportStatus");
  var targetEl = document.getElementById("reportTarget");

  var VALID = { abuse: 1, csam: 1, spam: 1, quality: 1, other: 1 };
  var current = { nodeId: "", model: "" };
  var lastFocus = null;

  function setStatus(msg, kind) {
    if (!statusEl) return;
    statusEl.textContent = msg || "";
    statusEl.className = "reportform__status mono" + (kind ? " is-" + kind : "");
  }

  function open(nodeId, model, callsign) {
    current.nodeId = nodeId || "";
    current.model = model || "";
    lastFocus = document.activeElement;
    // reset the form for a fresh report
    if (catEl) catEl.value = "abuse";
    if (reqEl) reqEl.value = "";
    if (detailEl) detailEl.value = "";
    if (submitEl) { submitEl.disabled = false; submitEl.textContent = "send report"; }
    setStatus("", "");
    var label = callsign ? "station " + callsign : (model ? "model " + model : "this station");
    if (targetEl) targetEl.textContent = "reporting: " + label;
    modal.hidden = false;
    document.body.classList.add("reportmodal-open");
    if (catEl && catEl.focus) catEl.focus();
  }

  function close() {
    modal.hidden = true;
    document.body.classList.remove("reportmodal-open");
    if (lastFocus && lastFocus.focus) { try { lastFocus.focus(); } catch (e) {} }
  }

  // Delegated open: any .report-btn anywhere on the page.
  document.addEventListener("click", function (e) {
    var btn = e.target.closest && e.target.closest(".report-btn");
    if (!btn) return;
    e.preventDefault();
    e.stopPropagation();
    open(btn.getAttribute("data-report-node") || "",
         btn.getAttribute("data-report-model") || "",
         btn.getAttribute("data-report-callsign") || "");
  });

  if (closeBtn) closeBtn.addEventListener("click", close);
  if (cancelBtn) cancelBtn.addEventListener("click", close);
  if (scrim) scrim.addEventListener("click", close);
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && !modal.hidden) close();
  });
  // keep focus inside the dialog while open (light focus trap)
  if (dialog) dialog.addEventListener("keydown", function (e) {
    if (e.key !== "Tab") return;
    var f = dialog.querySelectorAll('button, select, textarea, input, a[href]');
    if (!f.length) return;
    var first = f[0], last = f[f.length - 1];
    if (e.shiftKey && document.activeElement === first) { last.focus(); e.preventDefault(); }
    else if (!e.shiftKey && document.activeElement === last) { first.focus(); e.preventDefault(); }
  });

  if (form) form.addEventListener("submit", function (e) {
    e.preventDefault();
    var category = catEl ? catEl.value : "other";
    if (!VALID[category]) category = "other";
    var payload = {
      category: category,
      node_id: current.nodeId || "",
      request_id: reqEl ? reqEl.value.trim() : "",
      detail: detailEl ? detailEl.value.trim() : ""
    };
    // when reporting a whole model (no node_id), fold the model into detail so
    // the report still carries useful context.
    if (!payload.node_id && current.model) {
      payload.detail = (payload.detail ? payload.detail + "\n\n" : "") + "model: " + current.model;
    }
    if (!payload.detail && payload.category !== "csam") {
      setStatus("add a short description so we can act on it.", "err");
      return;
    }

    if (submitEl) { submitEl.disabled = true; submitEl.textContent = "sending..."; }
    setStatus("sending your report...", "");

    var ctrl = ("AbortController" in window) ? new AbortController() : null;
    var to = setTimeout(function () { if (ctrl) ctrl.abort(); }, 12000);

    fetch(BROKER + "/report", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
      signal: ctrl ? ctrl.signal : undefined
    }).then(function (r) {
      clearTimeout(to);
      if (r.ok) {
        setStatus("Thanks - your report was received. We review reports and a station's record follows it.", "ok");
        if (submitEl) submitEl.textContent = "sent";
        setTimeout(close, 2200);
      } else {
        if (submitEl) { submitEl.disabled = false; submitEl.textContent = "send report"; }
        setStatus("We couldn't submit that just now. Email abuse@rogerai.fyi instead.", "err");
      }
    }).catch(function () {
      clearTimeout(to);
      if (submitEl) { submitEl.disabled = false; submitEl.textContent = "send report"; }
      setStatus("Network error - couldn't reach the broker. Email abuse@rogerai.fyi instead.", "err");
    });
  });
})();
