// console-lan.js - the console's LAN mode marks (features/lan/ui_visibility.feature,
// money_and_accounts.feature): the top bar's LAN badge and the SHARE cost line. Pure functions
// over the node snapshot, so a test can run them over a minimal document.
(function (root) {
  "use strict";

  // costLine is what the SHARE totals say about money: a LAN share bills nothing.
  function costLine(snap) {
    return snap && snap.lan ? "local, nothing billed" : "settles on the broker";
  }

  // renderLAN shows or hides the top bar's LAN badge for snap.
  function renderLAN(doc, snap) {
    var badge = doc.getElementById("top-lan");
    if (!badge) return;
    var on = !!(snap && snap.lan);
    badge.hidden = !on;
    badge.textContent = on ? "LAN" : "";
  }

  var api = { costLine: costLine, renderLAN: renderLAN };
  if (typeof module === "object" && module.exports) {
    module.exports = api;
  } else {
    root.RogerConsoleLAN = api;
  }
})(this);
