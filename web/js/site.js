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

  /* ---- reveal on scroll ------------------------------------------ */
  var reveals = document.querySelectorAll("[data-reveal]");
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
    reveals.forEach(function (el) { io.observe(el); });
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
      copy(INSTALL_CMD).then(function () {
        btn.classList.add("is-copied");
        showToast("Copied to clipboard");
        setTimeout(function () { btn.classList.remove("is-copied"); }, 1600);
      }).catch(function () { showToast("Press ⌘/Ctrl-C to copy"); });
    });
  });

  /* ---- OS hint --------------------------------------------------- */
  var note = document.getElementById("installNote");
  if (note) {
    var p = (navigator.platform || "") + " " + (navigator.userAgent || "");
    var os = /Mac/i.test(p) ? "macOS" : /Win/i.test(p) ? "Windows" : /Linux|X11/i.test(p) ? "Linux" : null;
    if (os === "Windows") {
      note.innerHTML = 'On Windows? Use WSL, or <a class="install__alt" href="https://github.com/bownux/rogerai/releases" style="display:inline">grab the .exe →</a>';
    } else if (os) {
      note.textContent = "Detected " + os + " · also runs on macOS, Linux & Windows";
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
