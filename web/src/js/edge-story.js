/* =====================================================================
   RogerAI - homepage FIG.2, the Roger Edge story reel. Two brand films
   (Story A / Story B) muted, played back to back forever - edge-story.js
   swaps <source> on `ended` and picks which one opens at random per page
   load, so the two halves trade first place across visits.

   The power-on/off is an old CRT set, and it is the screen's actual HEIGHT
   that collapses to a thin bright line and back - not an internal clip over
   a fixed-size box - so a "off" set really does give the page its space
   back, and the video content itself visibly compresses into the line
   (object-fit: cover keeps it filling whatever height remains) instead of
   an synthetic bar standing in for it. Keyed to scroll via one repeating
   IntersectionObserver, tuned to fire while the reel is still approaching
   each edge rather than once it's already crossed it - unlike the one-shot
   [data-reveal] fade site.js already runs on this element for its entrance.

   Status ownership: this file owns #pingTag (writes "on air") and
   body[data-onair], inherited from the FIG.2 dial teaser this replaced -
   the homepage hero / Ping mascot read that single status.
   ===================================================================== */
(function () {
  "use strict";

  // "reel" is the full-bleed band (#edgeBand), not the caption figure
  // (#edgeReel) - it's what actually holds the screen, so it's what the CRT
  // state classes and the IntersectionObserver need to be watching.
  var reel  = document.getElementById("edgeBand");
  var video = document.getElementById("edgeVideo");
  if (!reel || !video) return;

  var pingTag = document.getElementById("pingTag");
  document.body.setAttribute("data-onair", "live");
  if (pingTag) pingTag.textContent = "on air";

  var REDUCED = window.matchMedia &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  var tagEl  = document.getElementById("edgeTag");
  var screenEl = document.getElementById("edgeScreen");
  var muteBtn = document.getElementById("edgeMute");
  var muteLabel = document.getElementById("edgeMuteLabel");

  var STORIES = {
    a: { webm: "assets/edge/story-a.webm", mp4: "assets/edge/story-a.mp4", poster: "assets/edge/poster-a.webp", tag: "STORY A" },
    b: { webm: "assets/edge/story-b.webm", mp4: "assets/edge/story-b.mp4", poster: "assets/edge/poster-b.webp", tag: "STORY B" }
  };
  var sources = video.querySelectorAll("source");
  var current = Math.random() < 0.5 ? "a" : "b";
  var loaded = false;

  function setStory(key) {
    current = key;
    var s = STORIES[key];
    // index 0 is the mp4 <source> (listed first in the markup - see its
    // comment for why: H.264 hardware-decodes far more reliably than VP9).
    sources[0].src = s.mp4;
    sources[1].src = s.webm;
    video.setAttribute("poster", s.poster);
    if (tagEl) tagEl.textContent = s.tag;
    video.load();
  }

  function playCurrent() {
    var p = video.play();
    if (p && p.catch) p.catch(function () {});
  }

  // advance to the OTHER story and keep going - the alternating loop.
  video.addEventListener("ended", function () {
    setStory(current === "a" ? "b" : "a");
    playCurrent();
  });

  /* ---- lazy load: no bytes fetched until the reel nears the viewport --- */
  function ensureLoaded() {
    if (loaded) return;
    loaded = true;
    setStory(current);
  }

  function setScreenA11y(on) {
    if (!screenEl) return;
    screenEl.setAttribute("aria-pressed", String(on));
    screenEl.setAttribute("aria-label", on ? "Turn the Roger Edge set off" : "Turn the Roger Edge set on");
  }

  /* ---- CRT power-on/off: the screen's HEIGHT is the whole effect ---- */
  // Progressive enhancement: without this class the screen just keeps its
  // CSS aspect-ratio height, so no-IntersectionObserver / reduced-motion
  // visitors (JS still runs for them, see the REDUCED/else branches below)
  // get a plain, always-full-height looping video, never a set stuck "off".
  // True JS-off visitors get neither: the <source> tags carry no `src` in
  // the raw HTML (only JS ever sets one, for the lazy-load), so the <video>
  // has nothing playable and just shows its static `poster` frame.
  // --reel-full-h is measured from the rendered width (the aspect ratio
  // survives even once height stops being CSS-derived); --reel-collapsed-h
  // is fixed in CSS, just tall enough to read as a line.
  var CAN_FX = "IntersectionObserver" in window && !REDUCED;
  if (CAN_FX) {
    reel.classList.add("reel--fx");
    setScreenA11y(false);
    measureFullHeight();
    window.addEventListener("resize", measureFullHeight);
  }

  function measureFullHeight() {
    var w = screenEl.getBoundingClientRect().width;
    if (w > 0) screenEl.style.setProperty("--reel-full-h", Math.round(w * (544 / 1280)) + "px");
  }

  // one of "off" | "on" | "poweringOn" | "poweringOff" - a plain state
  // variable rather than trusting class presence, so a power request that
  // arrives mid-animation (a fast scroll past, or a click while it's still
  // settling) reverses cleanly instead of re-adding a class that's already
  // there (which fires no animation at all - a CSS animation restarts only
  // when its triggering class actually changes).
  var state = "off";

  function forceReflow() { void screenEl.offsetWidth; }

  function powerOn() {
    if (state === "on" || state === "poweringOn") return;
    measureFullHeight();
    ensureLoaded();
    playCurrent();
    setScreenA11y(true);
    reel.classList.remove("is-powering-off");
    if (state === "poweringOff") forceReflow();
    reel.classList.add("is-powering-on");
    state = "poweringOn";
  }

  function powerOff() {
    if (state === "off" || state === "poweringOff") return;
    setScreenA11y(false);
    reel.classList.remove("is-powering-on");
    if (state === "poweringOn") forceReflow();
    reel.classList.add("is-powering-off");
    state = "poweringOff";
  }

  // set once the FIRST power-on animation completes (below) - ioExit only
  // starts watching once the set is confirmed fully on, never at the same
  // instant as ioEnter, so a position already close to ioExit's shrunk top
  // line can't fire a conflicting powerOff() a tick after powerOn() starts.
  var exitObserving = false;

  reel.addEventListener("animationend", function (e) {
    if (e.animationName === "reelPowerOn" && state === "poweringOn") {
      reel.classList.remove("is-powering-on");
      reel.classList.add("is-on");
      state = "on";
      if (!exitObserving && CAN_FX) { exitObserving = true; ioExit.observe(reel); }
    } else if (e.animationName === "reelPowerOff" && state === "poweringOff") {
      reel.classList.remove("is-powering-off", "is-on");
      video.pause(); // freeze on the collapsed line, not mid-picture
      state = "off";
    }
  });

  /* ---- pressing the set: a manual power toggle, same as a TV's power
     button. The scroll-driven observer still wins the next time visibility
     actually changes - stepping away and back turns it on again, like
     walking back into the room. --------------------------------------- */
  function togglePower() {
    if (CAN_FX) {
      state === "on" || state === "poweringOn" ? powerOff() : powerOn();
    } else {
      // no CRT flourish (reduced motion / no IntersectionObserver): a plain
      // play/pause toggle is the equivalent "power" action.
      if (video.paused) { ensureLoaded(); playCurrent(); setScreenA11y(true); }
      else { video.pause(); setScreenA11y(false); }
    }
  }
  if (screenEl) {
    screenEl.addEventListener("click", togglePower);
    screenEl.addEventListener("keydown", function (e) {
      if (e.key === " " || e.key === "Enter" || e.key === "Spacebar") {
        e.preventDefault();
        togglePower();
      }
    });
  }

  if (REDUCED) {
    // no CRT flourish: just load + play once visible, pause once it is not.
    setScreenA11y(false);
    if ("IntersectionObserver" in window) {
      var ioR = new IntersectionObserver(function (entries) {
        entries.forEach(function (e) {
          if (e.isIntersecting) { ensureLoaded(); playCurrent(); setScreenA11y(true); }
          else { video.pause(); setScreenA11y(false); }
        });
      }, { threshold: 0.2 });
      ioR.observe(reel);
    } else {
      ensureLoaded();
      playCurrent();
      setScreenA11y(true);
    }
  } else if (CAN_FX) {
    // TWO observers, not one, each with exactly one job - a single shared
    // rootMargin can't satisfy both "turn on reliably" and "turn off with
    // room to see the animation finish" at once (tried that; see the
    // history below), because they need opposite-sized top margins.
    //
    // ioEnter: powerOn() only, on the TRUE top edge (no shrink). This is
    // what has to be trustworthy the instant observation starts (right
    // after the visitor's first scroll/wheel/key gesture) - a shrunk top
    // here previously meant a single ordinary scroll (even a Page Down)
    // could carry the reel's position past the shrunk boundary before that
    // first read ever happened, and it stayed stuck "off" from then on
    // (only scrolling back up would have fixed it). The +20% bottom margin
    // still makes entering from below fire before it's actually visible.
    var ioEnter = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) { if (e.isIntersecting) powerOn(); });
    }, { threshold: 0, rootMargin: "0px 0px 20% 0px" });
    // ioExit: BOTH powerOff() and powerOn(), against a top edge shrunk by a
    // LOT (-40%). Power-off-only was a real bug (caught in review): on a
    // viewport shorter than 40% of the reel's shrunk zone (portrait/mobile),
    // the reel can re-enter from below without ever re-crossing ioEnter's
    // TRUE (unshrunk) boundary - ioEnter had already fired once and stays
    // intersecting throughout, so nothing calls powerOn() again, and the set
    // is stuck dark until scrolled fully past. Being bidirectional here
    // restores that recovery path; ioEnter still owns the one read that has
    // to be trustworthy (the very first), since ioExit ignores its own first
    // callback (see ioExitPrimed below).
    //
    // This one only ever runs after the set is already on, so the risky
    // first-read case above does not apply to its OWN activation - it can be
    // as eager as the close animation needs, firing while a solid chunk of
    // the reel is still on screen instead of catching only its last sliver.
    var ioExitPrimed = false;
    var ioExit = new IntersectionObserver(function (entries) {
      // IntersectionObserver.observe() delivers one synchronous-ish read of
      // CURRENT geometry the instant it starts - and it starts right as the
      // power-on animation finishes (below). On a tall viewport where the
      // fully-open reel already sits above the -40% line, that first read
      // says "not intersecting" and would power off the instant power-on
      // completes: an on-then-off flash. Skipping exactly that one read (not
      // deferring observe() itself, which only delays the same problem) is
      // what actually avoids it; every read after is a real scroll change.
      if (!ioExitPrimed) { ioExitPrimed = true; return; }
      entries.forEach(function (e) { e.isIntersecting ? powerOn() : powerOff(); });
    }, { threshold: 0, rootMargin: "-40% 0px 0px 0px" });
    // Stay collapsed on load, even if the reel is already geometrically in
    // view (a tall viewport, a mid-page anchor link) - observing only
    // starts on the first real scroll OR a 2.5s fallback timer, WHICHEVER
    // FIRST: a visitor who never touches the page still sees the set come
    // on (so it isn't dead weight for someone reading without scrolling),
    // but a visitor who scrolls first gets the scroll-triggered version and
    // the fallback is cancelled outright, never firing on top of it later.
    // `scroll` alone MISSES a real gesture that doesn't move scrollY - scrolling
    // up while already at the top, a rubber-band swipe that snaps back, a wheel
    // tick too small to register. Those still fire `wheel`/`keydown`/`touchstart`
    // even though the page never actually moved, so listen for all of them; the
    // `settled` guard means only the first of any type does anything.
    var settled = false;
    function beginObserving() {
      if (settled) return;
      settled = true;
      clearTimeout(autoStartTimer);
      ioEnter.observe(reel); // ioExit starts once the first power-on completes, above
    }
    var autoStartTimer = setTimeout(beginObserving, 2500);
    ["scroll", "wheel", "touchstart", "keydown"].forEach(function (type) {
      window.addEventListener(type, beginObserving, { once: true, passive: true });
    });
  } else {
    ensureLoaded();
    playCurrent();
  }

  document.addEventListener("visibilitychange", function () {
    if (document.hidden) video.pause();
    else if (state === "on" || !CAN_FX) playCurrent();
  });

  /* ---- sound toggle: starts muted (autoplay requires it), a click is a
     real user gesture so unmuting from here is always allowed -------- */
  if (muteBtn) {
    muteBtn.addEventListener("click", function () {
      video.muted = !video.muted;
      muteBtn.setAttribute("aria-pressed", String(!video.muted));
      muteBtn.classList.toggle("is-unmuted", !video.muted);
      if (muteLabel) muteLabel.textContent = video.muted ? "sound off" : "sound on";
      muteBtn.setAttribute("aria-label", video.muted ? "Turn the film's sound on" : "Turn the film's sound off");
    });
  }
})();
