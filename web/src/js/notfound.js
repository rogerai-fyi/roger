// 404 page: surface the dropped request path the visitor actually asked for.
// Same-origin, so allowed by the CSP (script-src 'self'). No inline script, so
// the no-flash theme hash in _headers stays untouched.
(function () {
  try {
    var el = document.getElementById("lostPath");
    if (!el) return;
    var path = (location.pathname || "/") + (location.search || "");
    // keep it terse and safe; textContent never interprets markup
    el.textContent = path.length > 64 ? path.slice(0, 61) + "..." : path;
  } catch (e) {}
})();
