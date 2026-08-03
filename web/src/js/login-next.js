// Carry a `next` from /login.html through to whichever provider the person picks.
//
// Someone who reached the sign-in page mid-task - approving a device, for instance -
// should land back where they were, not on the dashboard having lost their place. The
// broker validates `next` as a same-site path and ignores anything else, so this only
// ever moves a person around our own site.
(function () {
  var m = /[?&]next=([^&]+)/.exec(location.search);
  if (!m) return;
  var next = decodeURIComponent(m[1]);
  // Belt and braces: the broker re-validates, but there is no reason for this page to
  // forward something that obviously leaves the site.
  if (next.charAt(0) !== "/" || next.indexOf("//") === 0 || next.indexOf("/\\") === 0) return;

  var links = document.querySelectorAll('a[href*="/auth/"][href*="/login"]');
  for (var i = 0; i < links.length; i++) {
    var a = links[i];
    a.href += (a.href.indexOf("?") === -1 ? "?" : "&") + "next=" + encodeURIComponent(next);
  }
})();
