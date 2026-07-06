# SEO setup - the things only you can do

The code side (canonical, sitemap, structured data, the first content article) ships in
**PR #18**. These are the steps that need a dashboard, DNS, or a decision - I can't do them from
the CLI. Do them in this order; the first two are the high-leverage ones.

---

## 0. Merge PR #18 (deploys everything)
<https://github.com/rogerai-fyi/roger/pull/18> - merging it deploys the SEO baseline + the first
article. The web app rebuilds `node build.mjs` on deploy. After it's live, confirm:
`https://rogerai.fyi/sitemap.xml` returns XML (not a 404) and
`https://rogerai.fyi/broadcasts-run-local-llm.html` loads.

---

## 1. Google Search Console  (the single most important step - free, no code)
This is what powers the weekly gap-analysis loop. Without it you're flying blind.

1. Go to <https://search.google.com/search-console> and add a **Domain** property: `rogerai.fyi`.
2. It gives you a **TXT record**. Add it in **Cloudflare -> DNS**: Type `TXT`, Name `@`, Content =
   the `google-site-verification=...` string Google shows. Save, then click **Verify** in GSC.
3. In GSC left nav -> **Sitemaps** -> submit `https://rogerai.fyi/sitemap.xml`.
4. Wait a few days for data to appear (Performance report). Then run the weekly loop (see bottom).

## 2. Bing Webmaster Tools  (do NOT skip - free)
A real case study lost ~90% of Bing traffic for weeks by ignoring this.
1. <https://www.bing.com/webmasters> -> add site `https://rogerai.fyi`.
2. Easiest: **Import from Google Search Console** (one click once #1 is done). Otherwise verify via
   the same DNS-TXT method.
3. Submit the sitemap: `https://rogerai.fyi/sitemap.xml`.

---

## 3. Cloudflare Web Analytics  (privacy-first traffic numbers - cookieless, no banner)
The CSP already allows it (in this PR). Two clicks:
1. **Cloudflare dashboard -> Analytics & Logs -> Web Analytics -> Add a site** -> `rogerai.fyi`.
   Choose the **Automatic setup** (it injects the beacon at the edge - no code needed).
2. **Apply the CSP change to the edge** so the beacon isn't blocked. After PR #18 is merged, run:
   ```
   cd web && node scripts/cf-edge.mjs --apply     # needs your Cloudflare API token in the env
   ```
   (This mirrors `web/src/_headers` to the Cloudflare edge - see `web/EDGE.md`. If your edge-drift
   CI already applies `_headers` on merge, just merge and skip this.)
3. Verify: open the site, then check the Web Analytics dashboard shows a visit within a minute. If
   the browser console logs a CSP block for `static.cloudflareinsights.com`, the edge CSP wasn't
   updated yet - re-run step 2.

> Prefer GA4 instead? Not recommended here (it needs a GDPR cookie-consent banner + more CSP
> surface + Google tracking, off-brand for a content-blind product). If you truly want it, tell me
> and I'll wire gtag + the CSP + a consent banner.

---

## 4. robots.txt `Sitemap:` line  (optional - CF-managed)
Your robots.txt is served/managed by **Cloudflare** (the AI content-signals policy), not the repo,
so it has no `Sitemap:` line. Add one so crawlers auto-discover the sitemap:
- Cloudflare dashboard -> the setting that manages robots.txt (AI Audit / robots.txt) -> add:
  `Sitemap: https://rogerai.fyi/sitemap.xml`
- This is optional: submitting the sitemap directly in GSC + Bing (steps 1-2) already covers it.

---

## 5. One decision: should Privacy + Terms be indexable?
They're currently `noindex` (so they're excluded from the sitemap). Most sites **want** Privacy and
Terms indexed - Google reads them as legitimacy/trust (E-E-A-T) signals. If you agree, tell me and
I'll flip `robots` on `privacy.html` + `tos.html` (and maybe `security.html`); they'll then be
crawled and appear in the sitemap automatically. No dashboard needed - it's a one-line code change
per page.

---

## The weekly loop (once GSC has ~1-2 weeks of data)
This is the engine that compounds. ~30 min/week:
1. In GSC -> Performance -> export the **Queries** table (impressions + average position).
2. Hand it to me (or any LLM) and ask for: keyword **gaps** (impressions, no dedicated page),
   **cannibalization** (two pages competing for one query), and **emerging** queries.
3. I write 3-5 new broadcast articles targeting the top gaps (same format as
   `broadcasts-run-local-llm.html`: Quick Answer + question H2s + FAQPage schema + internal links).
4. Merge -> deploy -> in GSC use **URL Inspection -> Request indexing** on each new URL.
Repeat weekly. Each ranked article feeds the next week's impressions.

## To add another article yourself
`cp web/src/broadcasts-run-local-llm.html web/src/broadcasts-<slug>.html`, register it in
`web/build.mjs` CSS_BUNDLES, add a `<li class="bc-row">` at the top of `web/src/broadcasts.html`,
update the head title/desc + the H1/Quick-Answer/FAQ. The sitemap + canonical are automatic.
Run `bash .claude/skills/seo/scripts/bot-crawl-check.sh` after deploy to confirm bots see it.
