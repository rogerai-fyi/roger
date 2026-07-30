#!/usr/bin/env node
// fm-dns.mjs - stage the canonical zone's DNS by MIRRORING the legacy zone, so the new domain
// serves the same website, API and mail before its nameservers are ever delegated.
//
// Why mirror instead of a checked-in record list: the record targets are production topology.
// Reading them from the live legacy zone at run time keeps them out of this repository and
// means the plan cannot drift from what is actually serving traffic today.
//
// What it deliberately does NOT copy:
//   - DKIM keys and domain-ownership proofs. Those are issued per domain; a copied value can
//     never validate, and a stale DKIM record silently breaks signed mail. They are reported
//     as outstanding work instead.
//   - NS/SOA, which the DNS provider manages.
// What it adds: a DMARC record in monitor mode (the legacy zone has none).
//
//   node web/scripts/fm-dns.mjs                    # plan only (no writes); needs CF_API_TOKEN to read
//   node web/scripts/fm-dns.mjs --apply            # create the missing records in the new zone
//
// Env: CF_API_TOKEN (or CLOUDFLARE_API_TOKEN) - Zone:Read + DNS:Edit on BOTH zones.
//      CF_ZONE (default rogerai.fm), CF_LEGACY_ZONE (default rogerai.fyi),
//      FM_DMARC_RUA (default dmarc@<new zone>) - must be a deliverable mailbox.
//
// Dependency-free: Node >=18 (global fetch).

const API = "https://api.cloudflare.com/client/v4";
const ZONE = process.env.CF_ZONE || "rogerai.fm";
const LEGACY_ZONE = process.env.CF_LEGACY_ZONE || "rogerai.fyi";

const apply = process.argv.includes("--apply");

// Record types we understand well enough to mirror verbatim.
const MIRRORED = new Set(["CNAME", "A", "AAAA", "MX", "CAA", "SRV"]);
// Zone-infrastructure types the provider owns.
const PROVIDER_OWNED = new Set(["NS", "SOA", "DNSKEY", "DS"]);

function renameHost(name, legacyZone, newZone) {
  if (name === legacyZone) return newZone;
  if (name.endsWith("." + legacyZone)) {
    return name.slice(0, name.length - legacyZone.length - 1) + "." + newZone;
  }
  return name;
}

// A TXT record is either portable policy (SPF) or a per-domain proof that must be reissued.
function classifyTxt(record) {
  const name = record.name || "";
  const content = record.content || "";
  if (/(^|\.)_domainkey\./.test(name) || /^_domainkey\./.test(name)) return "dkim";
  if (/(^|\.)_dmarc(\.|$)/.test(name)) return "dmarc";
  if (/^v=spf1/i.test(content.replace(/^"|"$/g, ""))) return "spf";
  return "proof";
}

/**
 * Build the record set to stage on the new zone.
 * @returns {{records: object[], needsUniqueValues: string[], review: string[]}}
 */
export function planRecords(legacyRecords, opts = {}) {
  const legacyZone = opts.legacyZone || LEGACY_ZONE;
  const newZone = opts.newZone || ZONE;
  const dmarcRua = opts.dmarcRua || process.env.FM_DMARC_RUA || `dmarc@${newZone}`;

  const records = [];
  const needsUniqueValues = [];
  const review = [];

  for (const r of legacyRecords) {
    const name = renameHost(r.name, legacyZone, newZone);

    if (PROVIDER_OWNED.has(r.type)) continue;

    if (r.type === "TXT") {
      const kind = classifyTxt(r);
      if (kind === "spf") {
        records.push({ type: "TXT", name, content: r.content, ttl: 1 });
      } else if (kind === "dkim") {
        needsUniqueValues.push(
          `${name} (DKIM) - generate a new signing key for ${newZone}; never reuse the legacy key`,
        );
      } else if (kind === "dmarc") {
        // superseded by the monitor-mode record added below
        review.push(`${r.name} had a DMARC record; the staged zone uses monitor mode`);
      } else {
        needsUniqueValues.push(
          `${name} (domain-ownership verification) - request a new proof value for ${newZone}`,
        );
      }
      continue;
    }

    if (MIRRORED.has(r.type)) {
      const out = { type: r.type, name, content: r.content, ttl: 1 };
      // Proxy state is behaviour, not decoration: the API/streaming host must stay DNS-only.
      if (r.type === "CNAME" || r.type === "A" || r.type === "AAAA") {
        out.proxied = r.proxied === true;
      } else {
        out.proxied = false;
      }
      if (r.priority !== undefined && r.priority !== null) out.priority = r.priority;
      if (r.data) out.data = r.data;
      records.push(out);
      continue;
    }

    review.push(`${r.type} ${r.name} was not mirrored automatically - decide by hand`);
  }

  // The migration is the moment to add the DMARC the legacy zone never had.
  records.push({
    type: "TXT",
    name: `_dmarc.${newZone}`,
    content: `v=DMARC1; p=none; rua=mailto:${dmarcRua}; fo=1`,
    ttl: 1,
  });
  review.push(`DMARC reports go to ${dmarcRua} - make sure that mailbox is deliverable`);

  return { records, needsUniqueValues, review };
}

/**
 * Whether the staged zone is complete enough that delegating the registrar's nameservers
 * cannot black-hole the website, the API or inbound mail.
 * @returns {{ready: boolean, missing: string[]}}
 */
export function delegationReadiness(records, opts = {}) {
  const newZone = opts.newZone || ZONE;
  const missing = [];
  const has = (pred) => records.some(pred);
  const addressed = (name) =>
    has((r) => ["CNAME", "A", "AAAA"].includes(r.type) && r.name === name);

  if (!addressed(newZone)) missing.push(`website record for ${newZone}`);
  if (!addressed(`www.${newZone}`)) missing.push(`website record for www.${newZone}`);
  if (!addressed(`broker.${newZone}`)) missing.push(`broker alias record for broker.${newZone}`);
  if (!has((r) => r.type === "MX" && r.name === newZone)) {
    missing.push(`MX records for ${newZone} (inbound mail would be black-holed)`);
  }
  if (!has((r) => r.type === "TXT" && r.name === newZone && /^"?v=spf1/i.test(r.content))) {
    missing.push(`SPF record for ${newZone}`);
  }
  if (!has((r) => r.type === "TXT" && r.name === `_dmarc.${newZone}`)) {
    missing.push(`DMARC record for _dmarc.${newZone}`);
  }
  return { ready: missing.length === 0, missing };
}

// ---- Cloudflare API ------------------------------------------------------------------------
async function cf(method, urlPath, body) {
  const token = process.env.CF_API_TOKEN || process.env.CLOUDFLARE_API_TOKEN;
  if (!token) throw new Error("CF_API_TOKEN / CLOUDFLARE_API_TOKEN is not set");
  const res = await fetch(API + urlPath, {
    method,
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  const json = await res.json().catch(() => ({}));
  if (!json.success) {
    throw new Error(`CF ${method} ${urlPath} -> ${res.status}: ${JSON.stringify(json.errors || json)}`);
  }
  return json;
}

async function zoneByName(name) {
  const json = await cf("GET", `/zones?name=${encodeURIComponent(name)}`);
  const z = json.result && json.result[0];
  if (!z) throw new Error(`zone ${name} is not on this account (or the token cannot see it)`);
  return z;
}

async function allRecords(zoneId) {
  const out = [];
  for (let page = 1; ; page++) {
    const json = await cf("GET", `/zones/${zoneId}/dns_records?per_page=100&page=${page}`);
    out.push(...json.result);
    const info = json.result_info || {};
    if (!info.total_pages || page >= info.total_pages) break;
  }
  return out;
}

function sameRecord(a, b) {
  return (
    a.type === b.type &&
    a.name === b.name &&
    (a.content || "").replace(/^"|"$/g, "") === (b.content || "").replace(/^"|"$/g, "") &&
    (a.priority ?? null) === (b.priority ?? null)
  );
}

// ---- CLI -----------------------------------------------------------------------------------
const isMain = process.argv[1] && process.argv[1].endsWith("fm-dns.mjs");

if (isMain) {
  console.log(`legacy zone: ${LEGACY_ZONE}  (source of truth for targets)`);
  console.log(`new zone:    ${ZONE}`);
  console.log("");

  const legacy = await zoneByName(LEGACY_ZONE);
  const legacyRecords = await allRecords(legacy.id);
  console.log(`read ${legacyRecords.length} record(s) from ${LEGACY_ZONE}`);

  const { records, needsUniqueValues, review } = planRecords(legacyRecords, {
    legacyZone: LEGACY_ZONE,
    newZone: ZONE,
  });

  console.log(`\nplanned records for ${ZONE} (${records.length}):`);
  for (const r of records) {
    const proxy = r.proxied === true ? "proxied" : "dns-only";
    const prio = r.priority !== undefined ? ` prio=${r.priority}` : "";
    console.log(`  ${r.type.padEnd(5)} ${r.name.padEnd(28)} ${proxy.padEnd(9)}${prio} ${r.content}`);
  }

  if (needsUniqueValues.length) {
    console.log(`\nNOT copied - these need new per-domain values (${needsUniqueValues.length}):`);
    for (const n of needsUniqueValues) console.log(`  - ${n}`);
  }
  if (review.length) {
    console.log(`\nreview:`);
    for (const n of review) console.log(`  - ${n}`);
  }

  const gate = delegationReadiness(records, { newZone: ZONE });
  console.log(
    `\ndelegation gate: ${gate.ready ? "COMPLETE - the zone may be delegated" : "INCOMPLETE"}`,
  );
  for (const m of gate.missing) console.log(`  missing: ${m}`);

  if (!apply) {
    console.log("\nDRY RUN (no writes). Re-run with --apply to create these in the new zone.");
    process.exit(0);
  }

  const target = await zoneByName(ZONE);
  console.log(`\nnew zone id: ${target.id}  status=${target.status}`);
  console.log(`assigned nameservers: ${(target.name_servers || []).join(", ") || "(none yet)"}`);

  const existing = await allRecords(target.id);
  let created = 0;
  let skipped = 0;
  for (const r of records) {
    if (existing.some((e) => sameRecord(e, r))) {
      skipped++;
      continue;
    }
    await cf("POST", `/zones/${target.id}/dns_records`, r);
    created++;
    console.log(`  + ${r.type} ${r.name}`);
  }
  console.log(`\ncreated ${created}, already present ${skipped}`);
  console.log(
    `\nNext: delegate ${ZONE} at the registrar to: ${(target.name_servers || []).join(", ")}`,
  );
}
