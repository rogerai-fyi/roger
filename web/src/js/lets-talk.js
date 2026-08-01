/* =====================================================================
   RogerAI - the "Let's talk" contact dialog.

   Same accessibility contract as js/billing-help.js: focus moves in on
   open and restores to the opener on close, Tab is trapped, Escape and
   the backdrop close, every trigger's aria-expanded stays in sync.

   HONESTY CONTRACT (features/web/lets_talk.feature): the site has no
   form backend and collects nothing. Send composes a mailto so the
   visitor's own mail client carries the message; with this script
   absent, every [data-lets-talk] trigger is already a plain mailto
   link. CSP-safe: external file, script-src 'self', no network use.
   ===================================================================== */
(function () {
  "use strict";

  var modal = document.getElementById("letsTalk");
  if (!modal) return;
  var scrim = document.getElementById("letsTalkScrim");
  var closeBtn = document.getElementById("letsTalkClose");
  var sendBtn = document.getElementById("letsTalkSend");
  var topic = document.getElementById("ltTopic");
  var note = document.getElementById("ltNote");
  var lastFocus = null;
  var noteDirty = false;

  // Starter notes: enough structure to write a good first email, nothing that
  // reads like a questionnaire. Plain hyphens only (founder style: no em dashes).
  var STARTERS = {
    industrial: "Hi RogerAI team - we are looking at an advisory model beside our existing control systems.\n\n- Site and industry:\n- The workload (alarms, work orders, logs):\n- Where a box could sit (control room, purged cabinet):\n\nWhat would a pilot look like?",
    custom: "Hi RogerAI team - I would like to talk about a custom or optimized model.\n\n- Use case:\n- Base model or family of interest:\n- Target hardware and constraints:\n\nCould you walk me through the options?",
    station: "Hi RogerAI team - I have hardware and want to put it on air.\n\n- Hardware (GPU, RAM):\n- Models I want to serve:\n- Anything unusual about my setup:\n",
    press: "Hi RogerAI team - reaching out about a partnership or press.\n\n- Who we are:\n- What we have in mind:\n- Timeline:\n",
    other: "Hi RogerAI team -\n\n"
  };
  var SUBJECTS = {
    industrial: "Industrial pilot",
    custom: "Custom or optimized model",
    station: "Run a station",
    press: "Partnership or press",
    other: "Hello from the website"
  };

  function fillStarter() {
    if (noteDirty && note.value.trim() !== "") return;
    note.value = STARTERS[topic.value] || STARTERS.other;
    noteDirty = false;
  }
  // A hand-edited note is the visitor's text: switching topic never overwrites it
  // (fillStarter only writes into a pristine or empty note).
  topic.addEventListener("change", fillStarter);
  note.addEventListener("input", function () { noteDirty = true; });

  var FOCUSABLE = 'a[href], button:not([disabled]), input, select, textarea, [tabindex]:not([tabindex="-1"])';

  function setExpanded(v) {
    var triggers = document.querySelectorAll("[data-lets-talk]");
    for (var i = 0; i < triggers.length; i++) triggers[i].setAttribute("aria-expanded", v ? "true" : "false");
  }
  function open(fromEl) {
    lastFocus = fromEl || document.activeElement;
    modal.hidden = false; scrim.hidden = false;
    document.documentElement.classList.add("lt-open");
    fillStarter();
    setExpanded(true);
    var first = modal.querySelector("input, select, textarea");
    if (first) first.focus();
  }
  function close() {
    modal.hidden = true; scrim.hidden = true;
    document.documentElement.classList.remove("lt-open");
    setExpanded(false);
    if (lastFocus && lastFocus.focus) lastFocus.focus();
  }

  document.addEventListener("click", function (e) {
    var t = e.target.closest ? e.target.closest("[data-lets-talk]") : null;
    if (t) { e.preventDefault(); open(t); }
  });
  closeBtn.addEventListener("click", close);
  scrim.addEventListener("click", close);
  // The modal container (inset:0) sits above the scrim, so the padding area around
  // the dialog IS the visible backdrop - a click there must close too.
  modal.addEventListener("click", function (e) { if (e.target === modal) close(); });
  document.addEventListener("keydown", function (e) {
    if (modal.hidden) return;
    if (e.key === "Escape" || e.key === "Esc") { e.preventDefault(); close(); return; }
    if (e.key !== "Tab") return;
    var items = modal.querySelectorAll(FOCUSABLE);
    if (!items.length) return;
    var first = items[0], last = items[items.length - 1];
    if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
  });

  sendBtn.addEventListener("click", function () {
    var name = document.getElementById("ltName").value.trim();
    var email = document.getElementById("ltEmail").value.trim();
    var company = document.getElementById("ltCompany").value.trim();
    var role = document.getElementById("ltRole").value.trim();
    var lines = [note.value.trim(), ""];
    if (name) lines.push(name);
    if (role || company) lines.push([role, company].filter(Boolean).join(", "));
    if (email) lines.push(email);
    var subject = SUBJECTS[topic.value] || SUBJECTS.other;
    window.location.href = "mailto:labs@rogerai.fm?subject=" +
      encodeURIComponent(subject) + "&body=" + encodeURIComponent(lines.join("\n"));
  });
})();
