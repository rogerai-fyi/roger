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
