/* =====================================================================
   RogerAI - site bootstrap. Small, no deps.
   - sticky nav scrolled state
   - reveal-on-scroll (IntersectionObserver)
   - copy-on-click install command (+ toast)
   - OS detection for the install hint
   - earnings sparkline fill
   ===================================================================== */
(function () {
  "use strict";

  var INSTALL_CMD = "curl -fsSL https://rogerai.fyi/install.sh | sh";

  /* ---- theme toggle (light <-> dark) ----------------------------- */
  var STORE_KEY = "roger-theme";
  var root = document.documentElement;
  var toggle = document.getElementById("themeToggle");
  var mql = window.matchMedia ? window.matchMedia("(prefers-color-scheme: dark)") : null;

  function isDark() { return root.getAttribute("data-theme") === "dark"; }

  function syncToggle() {
    if (!toggle) return;
    var dark = isDark();
    // the button switches AWAY from the current theme
    toggle.setAttribute("aria-pressed", dark ? "true" : "false");
    toggle.setAttribute("aria-label", dark ? "Switch to light theme" : "Switch to dark theme");
  }

  function applyTheme(dark, animate) {
    if (animate) {
      root.classList.add("theme-anim");
      window.setTimeout(function () { root.classList.remove("theme-anim"); }, 360);
    }
    if (dark) root.setAttribute("data-theme", "dark");
    else root.removeAttribute("data-theme");
    syncToggle();
    // let theme-aware canvases (blip-map) re-read CSS variables and repaint
    window.dispatchEvent(new CustomEvent("themechange", { detail: { dark: dark } }));
  }

  syncToggle(); // reflect the pre-paint state set by the inline <head> script

  if (toggle) {
    toggle.addEventListener("click", function () {
      var next = !isDark();
      applyTheme(next, true);
      try { localStorage.setItem(STORE_KEY, next ? "dark" : "light"); } catch (e) {}
    });
  }

  // follow the OS only while the user hasn't made an explicit choice
  if (mql) {
    var onMql = function (e) {
      var saved;
      try { saved = localStorage.getItem(STORE_KEY); } catch (err) { saved = null; }
      if (!saved) applyTheme(e.matches, true);
    };
    if (mql.addEventListener) mql.addEventListener("change", onMql);
    else if (mql.addListener) mql.addListener(onMql);
  }

  /* ---- sticky nav ------------------------------------------------ */
  var nav = document.getElementById("nav");
  function onScroll() { if (nav) nav.classList.toggle("is-scrolled", window.scrollY > 8); }
  window.addEventListener("scroll", onScroll, { passive: true });
  onScroll();

  /* ---- mobile menu (burger) -------------------------------------- */
  var burger = document.getElementById("navBurger");
  var navMenu = document.getElementById("navMenu");
  if (burger && nav && navMenu) {
    var setMenu = function (open) {
      nav.classList.toggle("is-menu-open", open);
      burger.setAttribute("aria-expanded", open ? "true" : "false");
      burger.setAttribute("aria-label", open ? "Close menu" : "Open menu");
    };
    burger.addEventListener("click", function () {
      setMenu(!nav.classList.contains("is-menu-open"));
    });
    // close after tapping any link in the collapsed panel
    navMenu.addEventListener("click", function (e) {
      if (e.target.closest("a")) setMenu(false);
    });
    // close on Escape
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && nav.classList.contains("is-menu-open")) {
        setMenu(false); burger.focus();
      }
    });
    // if we grow past the mobile breakpoint, never leave a stale open state
    var mqlNav = window.matchMedia ? window.matchMedia("(min-width: 761px)") : null;
    if (mqlNav) {
      var onWide = function (e) { if (e.matches) setMenu(false); };
      if (mqlNav.addEventListener) mqlNav.addEventListener("change", onWide);
      else if (mqlNav.addListener) mqlNav.addListener(onWide);
    }
  }

  /* ---- reveal on scroll ------------------------------------------ */
  var reveals = document.querySelectorAll("[data-reveal]");
  // Reveal anything already in view on first paint, synchronously and without
  // the per-element delay, so the hero + install command never flash blank
  // while we wait for a scroll/observer callback (the html.js rule hid them).
  function isInViewport(el) {
    var r = el.getBoundingClientRect();
    var vh = window.innerHeight || document.documentElement.clientHeight;
    var vw = window.innerWidth || document.documentElement.clientWidth;
    return r.bottom > 0 && r.top < vh && r.right > 0 && r.left < vw;
  }
  if ("IntersectionObserver" in window) {
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        if (!e.isIntersecting) return;
        var el = e.target;
        var delay = parseInt(el.getAttribute("data-reveal-delay") || "0", 10);
        setTimeout(function () { el.classList.add("is-revealed"); }, delay);
        io.unobserve(el);
      });
    }, { threshold: 0.12, rootMargin: "0px 0px -8% 0px" });
    reveals.forEach(function (el) {
      if (isInViewport(el)) { el.classList.add("is-revealed"); return; }
      io.observe(el);
    });
  } else {
    reveals.forEach(function (el) { el.classList.add("is-revealed"); });
  }

  /* ---- copy install command -------------------------------------- */
  var toast = document.getElementById("toast");
  var toastTimer;
  function showToast(msg) {
    if (!toast) return;
    toast.textContent = msg;
    toast.classList.add("is-shown");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { toast.classList.remove("is-shown"); }, 1800);
  }
  function copy(text) {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      return navigator.clipboard.writeText(text);
    }
    return new Promise(function (resolve, reject) {
      try {
        var ta = document.createElement("textarea");
        ta.value = text; ta.style.position = "fixed"; ta.style.opacity = "0";
        document.body.appendChild(ta); ta.select();
        document.execCommand("copy"); document.body.removeChild(ta); resolve();
      } catch (e) { reject(e); }
    });
  }
  ["installCmd", "installCmd2"].forEach(function (id) {
    var btn = document.getElementById(id);
    if (!btn) return;
    btn.addEventListener("click", function () {
      // Copy the command currently displayed, not a hardcoded constant, so the
      // Windows PowerShell swap (below) always copies the right one.
      var code = btn.querySelector(".install__code");
      var text = code ? code.textContent.trim() : INSTALL_CMD;
      copy(text).then(function () {
        btn.classList.add("is-copied");
        showToast("Copied to clipboard");
        setTimeout(function () { btn.classList.remove("is-copied"); }, 1600);
      }).catch(function () { showToast("Press ⌘/Ctrl-C to copy"); });
    });
  });

  /* ---- "how to upgrade" disclosure (footer) ---------------------- */
  var upToggle = document.getElementById("upgradeToggle");
  var upPanel = document.getElementById("upgradePanel");
  if (upToggle && upPanel) {
    upToggle.addEventListener("click", function () {
      var open = upToggle.getAttribute("aria-expanded") === "true";
      upToggle.setAttribute("aria-expanded", open ? "false" : "true");
      upPanel.hidden = open;
    });
  }
  // each upgrade command box copies its own <code> text on click
  [["upgradeCmd1"], ["upgradeCmd2"]].forEach(function (pair) {
    var btn = document.getElementById(pair[0]);
    if (!btn) return;
    btn.addEventListener("click", function () {
      var code = btn.querySelector("code");
      var text = code ? code.textContent : "";
      copy(text).then(function () {
        btn.classList.add("is-copied");
        showToast("Copied to clipboard");
        setTimeout(function () { btn.classList.remove("is-copied"); }, 1600);
      }).catch(function () { showToast("Press ⌘/Ctrl-C to copy"); });
    });
  });

  /* ---- OS detection: upgrade Windows visitors to the PowerShell command --
     Progressive enhancement: the static HTML default is the POSIX curl
     one-liner (correct for the no-JS / non-Windows majority). On Windows we
     swap the primary command (both boxes + the copy target) to the PowerShell
     one-liner and flip the helper note. mac/linux detection is kept for the
     note copy only. */
  var WIN_CMD = "irm https://rogerai.fyi/install.ps1 | iex";

  function detectOS() {
    // Prefer the modern, high-entropy hint (Edge/Chromium support it).
    var uaData = navigator.userAgentData;
    if (uaData && uaData.platform) {
      var plat = uaData.platform;
      if (/Windows/i.test(plat)) return "Windows";
      if (/macOS/i.test(plat)) return "macOS";
      if (/Linux|Chrome OS/i.test(plat)) return "Linux";
    }
    // Fall back to the legacy navigator.platform / userAgent strings.
    var p = (navigator.platform || "") + " " + (navigator.userAgent || "");
    if (/Win(dows NT|32|64|dows)/i.test(p) || /\bWin\b/i.test(p)) return "Windows";
    if (/Mac/i.test(p)) return "macOS";
    if (/Linux|X11/i.test(p)) return "Linux";
    return null;
  }

  var os = detectOS();
  if (os === "Windows") {
    // Swap the primary install command in BOTH boxes to PowerShell. The copy
    // handler reads .install__code at click time, so the copy target follows.
    ["installCmd", "installCmd2"].forEach(function (id) {
      var btn = document.getElementById(id);
      if (!btn) return;
      var code = btn.querySelector(".install__code");
      if (code) code.textContent = WIN_CMD;
    });
    // Flip the note to point macOS / Linux users at the curl one-liner.
    var note = document.getElementById("installNote");
    if (note) {
      note.textContent = "Windows (PowerShell) · no account needed to browse";
    }
    var noteWin = document.getElementById("installNoteWin");
    if (noteWin) {
      noteWin.innerHTML = 'On macOS / Linux: <code class="inline">curl -fsSL https://rogerai.fyi/install.sh | sh</code>';
    }
    var noteWin2 = document.getElementById("installNoteWin2");
    if (noteWin2) {
      noteWin2.innerHTML = 'On macOS / Linux: <code class="inline">curl -fsSL https://rogerai.fyi/install.sh | sh</code>';
    }
  }

  /* ---- earnings sparkline ---------------------------------------- */
  var bars = document.querySelector(".earn__bars");
  if (bars) {
    var heights = [22, 34, 52, 70, 88, 100, 84, 60, 42];
    heights.forEach(function (h, i) {
      var i2 = document.createElement("i");
      i2.style.height = h + "%";
      if (h >= 70) i2.classList.add("on");
      bars.appendChild(i2);
    });
  }
})();
