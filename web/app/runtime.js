// The site runtime, one module. Every page used to load these as nine deferred classic
// scripts (lib/head.js); a page asks for them with { runtime: true } and gets this bundle,
// built by vite.islands.config.mjs to dist/js/runtime.js. Each is still the plain script in
// src/js (an IIFE, strict, no exports; the guard suite reads and runs those files as they are),
// and the order is the order they ran in. promo.js self-gates on the strip (#promoBar), so a
// page that renders no strip runs none of it. theme-init.js stays a sync head script (the
// no-flash theme) and easter-egg.js its own deferred one.
import "../src/js/lets-talk.js";
import "../src/js/site.js";
import "../src/js/session.js";
import "../src/js/promo.js";
import "../src/js/tuner.js";
import "../src/js/scrub.js";
import "../src/js/anchor-hold.js";
import "../src/js/range-twin.js";
import "../src/js/cycle.js";
