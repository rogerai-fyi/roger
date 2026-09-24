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

  var INSTALL_CMD = "curl -fsSL https://rogerai.fm/install.sh | sh";

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
  // The one copy behaviour (the copy tick): copy, mark the control .is-copied for
  // 1.6s (components.css turns its icon into a tick), and say so in the toast.
  function copyTick(btn, text) {
    copy(text).then(function () {
      btn.classList.add("is-copied");
      showToast("Copied to clipboard");
      setTimeout(function () { btn.classList.remove("is-copied"); }, 1600);
    }).catch(function () { showToast("Press ⌘/Ctrl-C to copy"); });
  }
  // EVERY install box copies, not a hardcoded list of two ids. The list was a trap: the
  // next page to offer an install command got a button that silently did nothing, which is
  // exactly what happened when the Tower page grew one.
  Array.prototype.forEach.call(document.querySelectorAll(".install__box"), function (btn) {
    btn.addEventListener("click", function () {
      // Copy the command currently displayed, not a hardcoded constant, so the
      // Windows PowerShell swap (below) always copies the right one.
      var code = btn.querySelector(".install__code");
      copyTick(btn, code ? code.textContent.trim() : INSTALL_CMD);
    });
  });
  // [data-copy-target]: the shared copy control for anything else (the footer's
  // upgrade commands, code blocks). It copies the text of the element its value
  // names ("#id"), or its own <code> when the value is empty. Delegated, so a
  // control added after load works too.
  document.addEventListener("click", function (e) {
    var btn = e.target.closest && e.target.closest("[data-copy-target]");
    if (!btn) return;
    var sel = btn.getAttribute("data-copy-target");
    var src = null;
    try { src = sel ? document.querySelector(sel) : btn.querySelector("code"); } catch (err) { src = null; }
    if (src) copyTick(btn, copyText(src));
  });
  // The text a copy control copies: the element's text without its decorative
  // aria-hidden parts (a code block's "$" prompts), so the result pastes and runs.
  function copyText(el) {
    var hidden = el.querySelectorAll("[aria-hidden='true']");
    if (!hidden.length) return el.textContent.trim();
    var clone = el.cloneNode(true);
    Array.prototype.forEach.call(clone.querySelectorAll("[aria-hidden='true']"), function (n) {
      var next = n.nextSibling; // the space that followed a prompt goes with it
      if (next && next.nodeType === 3) next.nodeValue = next.nodeValue.replace(/^ /, "");
      n.remove();
    });
    return clone.textContent.trim();
  }
  // Code blocks (components.css .code-block): each gets a copy control for its <pre>,
  // the shared copy tick above. Added here, not in the markup, so a visitor without
  // scripts sees the plain selectable <pre> and never a button that does nothing.
  var COPY_ICON = '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="9" y="9" width="11" height="11" rx="2"></rect><path d="M5 15V5a2 2 0 0 1 2-2h10"></path></svg>';
  Array.prototype.forEach.call(document.querySelectorAll(".code-block"), function (box, i) {
    var pre = box.querySelector("pre");
    if (!pre || box.querySelector(".code-block__copy")) return;
    if (!pre.id) pre.id = "code-block-" + (i + 1);
    var b = document.createElement("button");
    b.type = "button";
    b.className = "code-block__copy";
    b.setAttribute("data-copy-target", "#" + pre.id);
    b.setAttribute("aria-label", "Copy code to clipboard");
    b.innerHTML = COPY_ICON;
    box.appendChild(b);
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
  // (each upgrade command box copies its own <code>: data-copy-target, above)

  /* ---- OS detection: upgrade Windows visitors to the PowerShell command --
     Progressive enhancement: the static HTML default is the POSIX curl
     one-liner (correct for the no-JS / non-Windows majority). On Windows we
     swap the primary command (both boxes + the copy target) to the PowerShell
     one-liner and flip the helper note. mac/linux detection is kept for the
     note copy only. */
  var WIN_CMD = "irm https://rogerai.fm/install.ps1 | iex";

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
    // Swap the CLIENT install commands to PowerShell. The copy handler reads
    // .install__code at click time, so the copy target follows.
    //
    // [data-os-lock] boxes are skipped: their command only runs on one platform, and
    // rewriting one would hand a Windows reader a command for the wrong program. The Tower
    // box is the case - roger-tower is a Linux server process, and the installer refuses
    // anything else - so offering it a Windows client one-liner would be a lie the page
    // told itself.
    Array.prototype.forEach.call(
      document.querySelectorAll(".install__box:not([data-os-lock])"), function (btn) {
        var code = btn.querySelector(".install__code");
        if (code) code.textContent = WIN_CMD;
      });
    // Flip the note to point macOS / Linux users at the curl one-liner.
    var note = document.getElementById("installNote");
    if (note) {
      note.textContent = "Windows (PowerShell) · no account needed to browse";
    }
    // Every "on Windows" alt-note flips to point macOS / Linux users at the curl one-liner.
    // Class-based, NOT an id list: a new install box (e.g. app.html §10) joins simply by
    // marking its note data-alt-note, so this can never silently skip one - the exact id-list
    // trap the comment above warns about.
    Array.prototype.forEach.call(document.querySelectorAll("[data-alt-note]"), function (n) {
      n.innerHTML = 'On macOS / Linux: <code class="inline">curl -fsSL https://rogerai.fm/install.sh | sh</code>';
    });
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

/* ---------- nav disclosure panels ------------------------------------------
   Research and Company each have children that were previously reachable only by
   landing on the hub and scrolling. The caret beside each opens a panel listing
   them, so the whole site is visible from one place.

   Follows the WAI-ARIA APG disclosure-navigation pattern, and deliberately does NOT
   use role="menu"/menuitem: that role promises a keyboard contract (arrow keys,
   typeahead, focus wrapping) that site navigation does not implement, and claiming
   it makes screen readers announce a widget that then behaves like plain links.
   These ARE plain links, so they stay in the normal tab order and in the reader's
   link list.

   The top-level item next to each caret is still a real link. With JavaScript off
   this file never runs, the panels stay hidden, and that link still reaches a hub
   listing the same destinations - the panel accelerates discovery, it is never the
   only route to anything. The footer carries the full map for the same reason. */
(function () {
  var groups = [].slice.call(document.querySelectorAll(".nav__group"));
  if (!groups.length) return;

  var panels = groups.map(function (g) {
    return { btn: g.querySelector(".nav__more"), panel: g.querySelector(".nav__panel"), group: g };
  }).filter(function (p) { return p.btn && p.panel; });
  if (!panels.length) return;

  function setOpen(entry, open) {
    entry.btn.setAttribute("aria-expanded", open ? "true" : "false");
    entry.panel.hidden = !open;
    entry.group.classList.toggle("is-open", open);
  }
  function closeAll(except) {
    panels.forEach(function (p) { if (p !== except) setOpen(p, false); });
  }

  /* The Playbox decks are a disclosure nested INSIDE the Models panel. It is
     not one of `panels` (those are the top-level groups and close each other),
     so it gets its own tiny handler - same contract, same attributes. */
  var deckBtn = document.getElementById("navDecksBtn");
  var deckPanel = document.getElementById("navDecksPanel");
  if (deckBtn && deckPanel) {
    deckBtn.addEventListener("click", function (e) {
      e.stopPropagation();
      var open = deckBtn.getAttribute("aria-expanded") === "true";
      deckBtn.setAttribute("aria-expanded", open ? "false" : "true");
      deckPanel.hidden = open;
      /* the label states the ACTION the press will perform, matching the
         way the arrow turns - a screen reader hears what the arrow shows */
      deckBtn.setAttribute("aria-label",
        open ? "Show the three Playbox decks" : "Hide the three Playbox decks");
    });
  }

  panels.forEach(function (entry) {
    entry.btn.addEventListener("click", function () {
      var open = entry.btn.getAttribute("aria-expanded") === "true";
      closeAll(entry);            // one panel at a time - two open panels overlap
      setOpen(entry, !open);
    });
  });

  // Escape closes and returns focus to the button that opened it, per the APG.
  document.addEventListener("keydown", function (e) {
    if (e.key !== "Escape") return;
    panels.forEach(function (p) {
      if (p.btn.getAttribute("aria-expanded") === "true") { setOpen(p, false); p.btn.focus(); }
    });
  });

  // A click anywhere else dismisses. Pointerdown rather than click so it fires before
  // a link inside another panel steals the event.
  document.addEventListener("pointerdown", function (e) {
    var inside = panels.some(function (p) { return p.group.contains(e.target); });
    if (!inside) closeAll(null);
  });

  // Tabbing out of a group closes it: focus leaving the panel is the user moving on,
  // and a panel left open behind the focus ring covers the page.
  panels.forEach(function (entry) {
    entry.group.addEventListener("focusout", function (e) {
      if (!entry.group.contains(e.relatedTarget)) setOpen(entry, false);
    });
  });

  // ---- the cycling App word: App -> TUI -> WebUI, on the beat of the carrier gradient ---
  // One nav destination worn three ways. The visible word swaps every SWAP_EVERY loops of
  // the carrier-sweep animation (so it is literally paced by the gradient, not a lone
  // timer), and the link's href follows the visible word so a click always lands on the
  // surface being shown. Progressive enhancement: the markup ships a plain "App" -> /app.html
  // link, so JS-off / reduced-motion / no-background-clip all keep a working link to the page
  // that holds all three sections. We PAUSE while the item is hovered or focused, so the word
  // a user is about to click cannot change under them.
  (function () {
    var link = document.querySelector("[data-app-cycle]");
    var word = link && link.querySelector("[data-app-word]");
    if (!link || !word) return;

    // If the carrier animation is not actually running (reduced motion, or a browser without
    // background-clip:text where .carrier disables it), there are no iteration events to ride,
    // so we simply never cycle - the plain "App" link stands. That is the intended fallback.
    var reduce = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)");
    if (reduce && reduce.matches) return;

    var STOPS = [
      { w: "App",   h: "/app.html" },
      { w: "TUI",   h: "/app.html#cli" },
      { w: "WebUI", h: "/app.html#webui" }
    ];
    var SWAP_EVERY = 2;          // gradient loops (3.2s each) between word changes
    var i = 0, loops = 0, paused = false;

    var pause = function () { paused = true; };
    var resume = function () { paused = false; };
    link.addEventListener("mouseenter", pause);
    link.addEventListener("mouseleave", resume);
    link.addEventListener("focusin", pause);
    link.addEventListener("focusout", resume);

    function advance() {
      i = (i + 1) % STOPS.length;
      var next = STOPS[i];
      word.classList.add("is-out");                 // lift + fade the outgoing word
      window.setTimeout(function () {
        word.textContent = next.w;
        link.setAttribute("href", next.h);
        word.classList.remove("is-out");
        word.classList.add("is-in");                // place the incoming word low, no transition
        // force a reflow so removing is-in on the next frame animates up into rest
        void word.offsetWidth;
        word.classList.remove("is-in");
      }, 260);
    }

    // The carrier-sweep fires animationiteration once per loop. Count loops and swap every
    // SWAP_EVERY, unless paused. addEventListener on the WORD (which carries .carrier).
    word.addEventListener("animationiteration", function () {
      if (paused) return;
      loops += 1;
      if (loops >= SWAP_EVERY) { loops = 0; advance(); }
    });
  })();
})();
