// Placeholder — the full node console UI lands in a later commit. For now it proves
// the embedded static + token-guarded SSE path end to end.
(function () {
  "use strict";
  var token = new URLSearchParams(location.search).get("t") || "";
  var app = document.getElementById("app");
  var es = new EventSource("/api/events?t=" + encodeURIComponent(token));
  es.onmessage = function (e) {
    try {
      var s = JSON.parse(e.data);
      app.textContent = "station " + s.station + " · " + s.on_air + "/" + s.max_on_air + " on air";
    } catch (_) {}
  };
})();
