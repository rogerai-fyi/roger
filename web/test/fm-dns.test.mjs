// fm-dns.test.mjs makes the "Nameservers change only after the authoritative zone is
// prepared" scenario of features/domain/domain_operations.feature EXECUTABLE: it drives the
// real record planner in web/scripts/fm-dns.mjs with a stand-in legacy zone and asserts the
// staged record set is complete before any delegation can be recommended.
//
// The planner derives targets from the LIVE legacy zone rather than hard-coded hostnames, so
// the public repository never carries production topology.

import test from "node:test";
import assert from "node:assert/strict";

import { planRecords, delegationReadiness } from "../scripts/fm-dns.mjs";

const LEGACY = "rogerai.fyi";
const CANON = "rogerai.fm";

// A representative legacy zone: website + API + admin hosts, mailbox mail, transactional
// mail on a subdomain, plus per-domain proof records that must NOT be copied.
const legacyRecords = [
  { type: "CNAME", name: LEGACY, content: "web-app.example-host.app", proxied: true },
  { type: "CNAME", name: `www.${LEGACY}`, content: "web-app.example-host.app", proxied: true },
  { type: "CNAME", name: `broker.${LEGACY}`, content: "broker-app.example-host.app", proxied: false },
  { type: "CNAME", name: `control.${LEGACY}`, content: "admin-app.example-host.app", proxied: true },
  { type: "MX", name: LEGACY, content: "mx.mailhost.example", priority: 10, proxied: false },
  { type: "MX", name: LEGACY, content: "mx2.mailhost.example", priority: 20, proxied: false },
  { type: "TXT", name: LEGACY, content: "v=spf1 include:mailhost.example ~all" },
  { type: "TXT", name: LEGACY, content: "mailhost-verification=abc123legacyproof" },
  { type: "TXT", name: `zmail._domainkey.${LEGACY}`, content: "v=DKIM1; k=rsa; p=LEGACYKEY" },
  { type: "MX", name: `send.${LEGACY}`, content: "feedback.txmail.example", priority: 10 },
  { type: "TXT", name: `send.${LEGACY}`, content: "v=spf1 include:txmail.example ~all" },
  { type: "TXT", name: `resend._domainkey.${LEGACY}`, content: "p=LEGACYTXKEY" },
];

function plan() {
  return planRecords(legacyRecords, { legacyZone: LEGACY, newZone: CANON });
}

function find(records, type, name) {
  return records.filter((r) => r.type === type && r.name === name);
}

test("the website hosts are mirrored onto the new zone through the proxy", () => {
  const { records } = plan();

  const apex = find(records, "CNAME", CANON);
  assert.equal(apex.length, 1, "the new apex needs exactly one website record");
  assert.equal(apex[0].content, "web-app.example-host.app", "apex must reach the existing web app");
  assert.equal(apex[0].proxied, true, "the canonical site is served through the edge");

  const www = find(records, "CNAME", `www.${CANON}`);
  assert.equal(www.length, 1);
  assert.equal(www[0].proxied, true, "www must be proxied so the redirect rule can run on it");
});

test("the broker alias stays off the proxy so streaming and WebSocket traffic is untouched", () => {
  const { records } = plan();

  const broker = find(records, "CNAME", `broker.${CANON}`);
  assert.equal(broker.length, 1, "the branded broker alias must be staged");
  assert.equal(broker[0].content, "broker-app.example-host.app");
  assert.equal(
    broker[0].proxied,
    false,
    "the broker carries API/SSE/WebSocket traffic and must match the legacy broker's DNS-only mode",
  );
});

test("mailbox and transactional mail routing is mirrored with priorities intact", () => {
  const { records } = plan();

  const mx = find(records, "MX", CANON);
  assert.deepEqual(
    mx.map((r) => [r.content, r.priority]).sort(),
    [
      ["mx.mailhost.example", 10],
      ["mx2.mailhost.example", 20],
    ].sort(),
    "inbound mail for the new domain must reach the same mail hosts, priorities preserved",
  );
  assert.ok(mx.every((r) => r.proxied !== true), "MX records are never proxied");

  const spf = find(records, "TXT", CANON).filter((r) => r.content.startsWith("v=spf1"));
  assert.equal(spf.length, 1, "the new apex needs exactly one SPF record");
  assert.match(spf[0].content, /include:mailhost\.example/);

  const sendMx = find(records, "MX", `send.${CANON}`);
  assert.equal(sendMx.length, 1, "the transactional bounce host must be mirrored");
});

test("per-domain proof records are never copied from the legacy zone", () => {
  const { records, needsUniqueValues } = plan();

  const copied = records.filter((r) => /legacyproof|LEGACYKEY|LEGACYTXKEY/i.test(r.content || ""));
  assert.deepEqual(
    copied,
    [],
    "ownership-verification and DKIM values are issued per domain - copying them stages a " +
      "record that can never validate and silently breaks signed mail",
  );

  // They must not be silently dropped either: the operator has to know to generate new ones.
  const flagged = needsUniqueValues.join(" ");
  assert.match(flagged, /_domainkey/, "DKIM must be reported as needing new per-domain values");
  assert.match(flagged, /verification/i, "domain ownership proof must be reported as outstanding");
});

test("the new zone gets a DMARC record in monitor mode", () => {
  const { records } = plan();

  const dmarc = find(records, "TXT", `_dmarc.${CANON}`);
  assert.equal(dmarc.length, 1, "a migration is the moment to add the DMARC the legacy zone lacks");
  assert.match(dmarc[0].content, /^v=DMARC1/);
  assert.match(dmarc[0].content, /p=none/, "start in monitor mode, tighten once reports are clean");
  assert.match(dmarc[0].content, /rua=mailto:/, "monitor mode is pointless without a report address");
});

test("delegation is withheld until the staged zone is complete", () => {
  const { records } = plan();

  const complete = delegationReadiness(records, { newZone: CANON });
  assert.equal(complete.ready, true, `a fully staged zone may be delegated: ${complete.missing}`);

  // Given the zone is not populated with the required website and mail records, the
  // registrar nameservers must not be changed.
  const noWeb = delegationReadiness(
    records.filter((r) => !(r.type === "CNAME" && r.name === CANON)),
    { newZone: CANON },
  );
  assert.equal(noWeb.ready, false, "a zone with no website record must not be delegated");
  assert.match(noWeb.missing.join(" "), new RegExp(CANON.replace(".", "\\.")));

  const noMail = delegationReadiness(
    records.filter((r) => r.type !== "MX"),
    { newZone: CANON },
  );
  assert.equal(noMail.ready, false, "delegating without MX would black-hole inbound mail");
  assert.match(noMail.missing.join(" "), /MX/);

  const noBroker = delegationReadiness(
    records.filter((r) => !(r.type === "CNAME" && r.name === `broker.${CANON}`)),
    { newZone: CANON },
  );
  assert.equal(noBroker.ready, false, "the broker alias is part of a complete zone");
});
