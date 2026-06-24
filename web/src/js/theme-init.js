// No-flash theme init for the standalone auth pages (login / dashboard / console).
// Applies the saved roger-theme (the marketing site's toggle writes it), falling
// back to the OS preference, BEFORE first paint - so these pages match whatever
// theme the visitor last chose instead of always following the OS. Loaded
// synchronously in <head>; same-origin so it is allowed by the CSP (script-src 'self').
(function () {
  try {
    var saved = localStorage.getItem("roger-theme");
    var dark = saved ? saved === "dark"
      : window.matchMedia("(prefers-color-scheme: dark)").matches;
    if (dark) document.documentElement.setAttribute("data-theme", "dark");
  } catch (e) {}
})();
