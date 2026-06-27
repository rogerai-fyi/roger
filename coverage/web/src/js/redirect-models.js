/* /bands.html -> /models.html, carrying any #band=<model> QSL hash through so
   old deep links keep working. Runs before the meta-refresh fires (it is in
   <head>); the meta-refresh is the no-JS fallback. */
(function () {
  "use strict";
  try {
    var hash = window.location.hash || "";
    window.location.replace("/models.html" + hash);
  } catch (e) {
    /* meta-refresh in <head> handles the fallback */
  }
})();
