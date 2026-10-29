<script>
  // API keys, /keys.html (legacy: web/src/js/keys.js + account-keys.js). Two kinds of key over
  // the broker's credentialed endpoints: account keys (/account/keys, the module in
  // ./accountKeys.js, shared with the Go probe) and grant keys (/grants). No token ever touches
  // the page; the only secret here is a freshly minted one, shown once and never re-fetched.
  import { onMount } from "svelte";
  import { BROKER, api, logout } from "#lib/api.js";
  import { num } from "#lib/format.js";
  import * as AK from "./accountKeys.js";
  import AccountNav from "#lib/ui/AccountNav.svelte";

  let { ui = typeof window !== "undefined" ? window : {} } = $props();   // the dialogs, stubbed by tests

  let gate = $state("loading");   // loading | in | out

  // ---- account keys -------------------------------------------------------------------------
  const ak = AK.makeApi(BROKER, (u, o) => fetch(u, o));
  let akKeys = $state([]);
  let akLoaded = $state(false);   // the list answered once: only then is an empty list "no keys yet"
  let akErr = $state("");
  let akSecret = $state("");
  let akName = $state(""), akLimit = $state(""), akReset = $state("monthly");
  const keysByID = $derived(Object.fromEntries(akKeys.map((k) => [k.id, k])));
  async function akRefresh() {
    try { akKeys = await AK.list(ak); akLoaded = true; } catch (e) { if (e.status !== 401) akFail(e); }
  }
  const akFail = (e) => { akErr = e.message; };
  async function akMint(ev) {
    ev.preventDefault();
    akErr = "";
    const fields = { name: akName.trim() };
    const limit = AK.parseLimit(akLimit);
    if (limit.error) return akFail(new Error(limit.error));
    if (!limit.none) fields.limit_usd = limit.value;
    fields.reset = akReset;
    try {
      const k = await AK.mint(ak, fields);
      akSecret = k.secret;   // shown once; Done takes it off the page
      akName = ""; akLimit = ""; akReset = "monthly";
      await akRefresh();
    } catch (e) { akFail(e); }
  }
  // the rows and the reveal are the module's renderers (one source, also the Go probe's view);
  // their buttons are read by delegation, as the legacy section did
  async function akRowClick(ev) {
    const b = ev.target.closest("button[data-act]");
    if (!b) return;
    const id = b.getAttribute("data-id"), k = keysByID[id] || {}, act = b.getAttribute("data-act");
    akErr = "";
    let p;
    if (act === "revoke") {
      if (!ui.confirm("Revoke " + id + "? Anything using it stops working.")) return;
      p = AK.revoke(ak, id);
    } else if (act === "toggle") {
      p = AK.update(ak, id, { disabled: !k.disabled });
    } else {
      const limit = AK.parseLimit(ui.prompt("Spend limit in $ for " + id + " (0 = no limit)", k.limit_usd || 0));
      if (limit.none) return;
      if (limit.error) return akFail(new Error(limit.error));
      p = AK.update(ak, id, { limit_usd: limit.value });
    }
    try { await p; await akRefresh(); } catch (e) { akFail(e); }
  }
  function akRevealClick(ev) {
    if (ev.target.closest("[data-reveal-done]")) akSecret = "";   // the secret leaves the page
  }

  // ---- grant keys --------------------------------------------------------------------------
  let billMode = $state("free");     // free | self | priced
  let scopeMode = $state("all");     // all | pick (node scope)
  let modelScope = $state("all");    // all | pick (model scope)
  let kFree = $state(true), advOpen = $state(false);
  let kName = $state(""), kPriceOut = $state(""), kExpiry = $state("never"), kExpiryDate = $state("");
  let kDaily = $state("2000000"), kMonthly = $state("0"), kNodes = $state(""), kModels = $state("");
  let createErr = $state(""), minting = $state(false);
  let reveal = $state(null);         // { secret, env } after a mint
  let copied = $state(false);
  let grants = $state([]);
  let gview = $state("loading");     // loading | ready | empty | nooperator | fault

  const BILL_HINT = { free: "Free - costs nobody. Your nodes serve it at $0.", self: "Self - a $0 key for your OWN headless boxes and agents.", priced: "Sponsored - usage bills to your own wallet at the price below." };
  function setBill(m) { billMode = m; kFree = m === "free"; }
  function toggleFree(e) {
    if (e.target.checked) billMode = "free";
    else { if (billMode === "free") billMode = "self"; advOpen = true; }
  }
  const capLabel = (v) => (!v || v <= 0 ? "unlimited" : num(v));
  const expiryLabel = (unix) => (!unix || unix <= 0 ? "never" : new Date(unix * 1000).toISOString().slice(0, 10));
  const priceLabel = (g) => (g.self ? "self" : g.free ? "free" : g.price || "priced");
  function expiresAtUnix() {
    if (kExpiry === "never") return 0;
    if (kExpiry === "30d") return Math.floor(Date.now() / 1000) + 30 * 86400;
    if (kExpiry === "90d") return Math.floor(Date.now() / 1000) + 90 * 86400;
    if (kExpiry === "date" && kExpiryDate) {
      const t = Math.floor(new Date(kExpiryDate + "T00:00:00Z").getTime() / 1000);
      if (isFinite(t) && t > 0) return t;
    }
    return 0;
  }
  const splitList = (s) => s.split(",").map((t) => t.trim()).filter(Boolean);
  async function onCreate(e) {
    e.preventDefault();
    createErr = "";
    const name = kName.trim();
    if (!name) { createErr = "Name is required."; return; }
    const payload = {
      name,
      free: billMode !== "priced",          // self + free are both $0; only "priced" is paid
      self: billMode === "self",
      price_out: billMode === "priced" ? parseFloat(kPriceOut) || 0 : 0,
      daily_cap: parseInt(kDaily, 10) || 0,
      monthly_cap: parseInt(kMonthly, 10) || 0,
      expires_at: expiresAtUnix(),
      nodes: scopeMode === "pick" ? splitList(kNodes) : [],
      models: modelScope === "pick" ? splitList(kModels) : [],   // empty = any model the in-scope nodes serve
    };
    minting = true;
    try {
      const r = await api("/grants", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
      const j = await r.json();
      minting = false;
      if (!r.ok || !j || !j.ok || !j.secret) { createErr = (j && j.error && j.error.message) || "Could not create the key."; return; }
      reveal = { secret: j.secret, env: "OPENAI_API_BASE=" + (j.openai_api_base || BROKER + "/v1") + "\nOPENAI_API_KEY=" + j.secret };
      copied = false;
      // restore the defaults (the JS state, the hidden fields, the toggle)
      kName = ""; kPriceOut = ""; kExpiry = "never"; kExpiryDate = ""; kDaily = "2000000"; kMonthly = "0"; kNodes = ""; kModels = "";
      billMode = "free"; kFree = true; scopeMode = "all"; modelScope = "all"; advOpen = false;
      loadKeys();
    } catch { minting = false; createErr = "Network error. Try again."; }
  }
  async function copySecret() {
    const secret = reveal && reveal.secret;
    if (!secret) return;
    const done = () => { copied = true; setTimeout(() => (copied = false), 1600); };
    try { await navigator.clipboard.writeText(secret); done(); }
    catch { legacyCopy(secret, done); }
  }
  function legacyCopy(text, done) {
    try {
      const ta = document.createElement("textarea");
      ta.value = text; ta.setAttribute("readonly", "");
      ta.style.position = "absolute"; ta.style.left = "-9999px";
      document.body.appendChild(ta); ta.select();
      document.execCommand("copy");
      document.body.removeChild(ta);
      done();
    } catch { /* leave the secret visible for manual copy */ }
  }
  let gBusy = $state(false);   // a reload keeps the table up under the loading line
  async function loadKeys() {
    if (!grants.length) gview = "loading";
    gBusy = true;
    try {
      const r = await api("/grants");
      if (r.status === 403) { gview = "nooperator"; return; }   // signed in, no machine on air: normal, never a load failure
      if (!r.ok) throw new Error("load failed");
      const j = await r.json();
      const rows = (j && j.grants) || [];
      // active first, then by name
      rows.sort((a, b) => ((a.status === "active" ? 0 : 1) - (b.status === "active" ? 0 : 1)) || (a.name || "").localeCompare(b.name || ""));
      grants = rows;
      gview = rows.length ? "ready" : "empty";
    } catch { gview = "fault"; }
    gBusy = false;
  }
  // Inline cap edit: a small prompt-driven PATCH (daily + monthly tokens).
  async function editCaps(g) {
    const nd = ui.prompt('Daily token cap for "' + g.name + '" (0 = unlimited):', String(g.daily_cap || 0));
    if (nd === null) return;
    const nm = ui.prompt("Monthly token cap (0 = unlimited):", String(g.monthly_cap || 0));
    if (nm === null) return;
    let dv = parseInt(nd, 10), mv = parseInt(nm, 10);
    if (!isFinite(dv) || dv < 0) dv = 0;
    if (!isFinite(mv) || mv < 0) mv = 0;
    try {
      const r = await api("/grants/" + encodeURIComponent(g.id), { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ daily_cap: dv, monthly_cap: mv }) });
      if (!r.ok) throw new Error("patch failed");
      loadKeys();
    } catch { ui.alert("Could not update caps. Try again."); }
  }
  async function revokeGrant(g) {
    if (!ui.confirm('Revoke "' + g.name + '"? The next request with its key is rejected. This cannot be undone.')) return;
    try {
      const r = await api("/grants/" + encodeURIComponent(g.id), { method: "DELETE" });
      if (!r.ok) throw new Error("revoke failed");
      loadKeys();
    } catch { ui.alert("Could not revoke the key. Try again."); }
  }

  // ---- gate: logged in? -------------------------------------------------------------------
  onMount(async () => {
    try {
      const r = await api("/account");
      const acct = r.ok ? await r.json() : null;
      if (acct && (acct.github_login || acct.github_id)) { gate = "in"; akRefresh(); loadKeys(); }
      else gate = "out";
    } catch { gate = "out"; }   // offline / logged-out
  });
</script>
<div class="authwrap tint-panel">
<main class="card" id="card" hidden={gate !== "in"}>
  <h1>API keys</h1>
  <p class="sub">Two kinds of key. <b>Account keys</b> (<code>rog-key_...</code>) call the API
    as you, from scripts and CI, under a spend limit you set. <b>Grant keys</b>
    (<code>rog-grant_...</code>) let your bots, family and friends use your own nodes. See the
    <a href="/manual.html#s5">manual</a>.</p>

  <!-- ACCOUNT KEYS (accountKeys.js, /account/keys) -->
  <section class="panel" id="acctKeys">
    <h2>Account keys</h2>
    <p class="fine">An account key pays from your wallet and routes the whole network, held to
      its own spend limit. The secret is shown <b>once</b>, at mint. Only its hash is stored.</p>

    <form id="akForm" class="kf" autocomplete="off" onsubmit={akMint}>
      <div class="kf__field">
        <label class="kf__k" for="akName">Name</label>
        <input id="akName" class="kf__in" type="text" required maxlength="64" placeholder="ci" bind:value={akName} />
      </div>
      <div class="kf__field">
        <label class="kf__k" for="akLimit">Spend limit ($)</label>
        <input id="akLimit" class="kf__in kf__num" type="number" min="0" step="0.01"
          inputmode="decimal" placeholder="5.00" value={akLimit} oninput={(e) => (akLimit = e.currentTarget.value)} />
        <span class="kf__hint">A number of dollars, like 5 or 2.50. Leave it empty, or type 0, for no limit.</span>
      </div>
      <div class="kf__field">
        <label class="kf__k" for="akReset">Limit resets</label>
        <select id="akReset" class="kf__in kf__sel" bind:value={akReset}>
          <option value="monthly">Monthly</option>
          <option value="weekly">Weekly</option>
          <option value="daily">Daily</option>
          <option value="none">Never</option>
        </select>
      </div>
      <button type="submit" class="primary">Mint account key</button>
      <p class="kf__err" id="akErr" hidden={!akErr}>{akErr}</p>
    </form>

    <div class="kf__reveal" id="akReveal" hidden={!akSecret} onclick={akRevealClick}>{@html akSecret ? AK.revealHTML(akSecret) : ""}</div>

    <div class="kf__tablewrap" id="akWrap" hidden={akKeys.length === 0}>
      <table class="mx-table">
        <thead>
          <tr>
            <th scope="col">ID</th>
            <th scope="col">Name</th>
            <th scope="col">Hint</th>
            <th scope="col" class="num">Limit</th>
            <th scope="col" class="num">Used</th>
            <th scope="col">Resets</th>
            <th scope="col">Expires</th>
            <th scope="col">State</th>
            <th scope="col"><span class="vh">Actions</span></th>
          </tr>
        </thead>
        <tbody id="akRows" onclick={akRowClick}>{@html AK.rowsHTML(akKeys)}</tbody>
      </table>
    </div>
    <p class="mx-state" id="akEmpty" hidden={!akLoaded || akKeys.length !== 0}>No account keys yet. Mint one above, or run
      <code>roger keys mint --name ci</code>.</p>
  </section>

  <!-- GRANT KEYS: CREATE -->
  <section class="panel">
    <h2>Grant keys</h2>
    <p class="fine">Grant keys are private, labeled bearer keys your bots, family, friends, or
      your own headless boxes use as their API key, with no login of their own. They route only
      to your nodes, free or at a price you sponsor, under the caps you set.</p>
    <p class="fine">The secret is shown <b>once</b>, at create. Only its hash is stored,
      so it can never be re-displayed - copy it then.</p>

    <form id="createForm" class="kf" autocomplete="off" hidden={gview === "nooperator"} onsubmit={onCreate}>
      <!-- common path: just Name + Free -->
      <div class="kf__field">
        <label class="kf__k" for="kName">Name</label>
        <input id="kName" class="kf__in" type="text" required maxlength="64"
          placeholder="petlings-bot" bind:value={kName} />
        <span class="kf__hint">A label only you see, on your dashboard.</span>
      </div>

      <div class="kf__field">
        <label class="kf__toggle">
          <input id="kFree" class="kf__toggle-in" type="checkbox" bind:checked={kFree} onchange={toggleFree} />
          <span class="kf__toggle-box" aria-hidden="true"></span>
          <span class="kf__toggle-txt">Free key</span>
        </label>
        <span class="kf__hint" id="freeHint">Costs nobody. Your nodes serve it at $0.
          Turn off to sponsor or scope it.</span>
      </div>

      <!-- everything else: folded away by default -->
      <details class="kf__adv" id="advBlock" bind:open={advOpen}>
        <summary class="kf__adv-sum">Advanced</summary>
        <div class="kf__adv-body">
          <div class="kf__field">
            <span class="kf__k">Billing</span>
            <div class="kf__opts" role="radiogroup" aria-label="Billing">
              {#each [["free", "Free"], ["self", "Self ($0)"], ["priced", "Sponsored"]] as [m, label]}
                <button type="button" class="kf__opt{billMode === m ? ' is-active' : ''}" data-bill={m} aria-pressed={billMode === m ? "true" : "false"} onclick={() => setBill(m)}>{label}</button>
              {/each}
            </div>
            <span class="kf__hint" id="billHint">{BILL_HINT[billMode]}</span>
          </div>

          <!-- sponsored price-out, only when "Sponsored" is chosen -->
          <div class="kf__field" id="priceField" hidden={billMode !== "priced"}>
            <label class="kf__k" for="kPriceOut">Price out ($/1M tokens)</label>
            <div class="kf__money">
              <span class="kf__money-pre">$</span>
              <input id="kPriceOut" class="kf__in" type="number" min="0" step="0.01"
                inputmode="decimal" placeholder="0.30" bind:value={kPriceOut} />
            </div>
            <span class="kf__hint">You sponsor it: usage bills to your own wallet at this rate.</span>
          </div>

          <div class="kf__field">
            <label class="kf__k" for="kExpiry">Expiry</label>
            <select id="kExpiry" class="kf__in kf__sel" bind:value={kExpiry}>
              <option value="never">Never</option>
              <option value="30d">30 days</option>
              <option value="90d">90 days</option>
              <option value="date">Pick a date...</option>
            </select>
            <input id="kExpiryDate" class="kf__in" type="date" hidden={kExpiry !== "date"} style="margin-top:8px" bind:value={kExpiryDate} />
          </div>

          <div class="kf__grid">
            <div class="kf__field">
              <label class="kf__k" for="kDaily">Daily cap (tokens)</label>
              <input id="kDaily" class="kf__in kf__num" type="number" min="0" step="1"
                inputmode="numeric" bind:value={kDaily} />
              <span class="kf__hint">0 = unlimited. Default self-limits a leaked key.</span>
            </div>
            <div class="kf__field">
              <label class="kf__k" for="kMonthly">Monthly cap (tokens)</label>
              <input id="kMonthly" class="kf__in kf__num" type="number" min="0" step="1"
                inputmode="numeric" placeholder="0" bind:value={kMonthly} />
              <span class="kf__hint">0 = unlimited.</span>
            </div>
          </div>

          <div class="kf__field">
            <span class="kf__k">Node scope</span>
            <div class="kf__opts" role="radiogroup" aria-label="Node scope">
              {#each [["all", "All my nodes"], ["pick", "Pick nodes"]] as [m, label]}
                <button type="button" class="kf__opt{scopeMode === m ? ' is-active' : ''}" data-scope={m} aria-pressed={scopeMode === m ? "true" : "false"} onclick={() => (scopeMode = m)}>{label}</button>
              {/each}
            </div>
            <input id="kNodes" class="kf__in" type="text" hidden={scopeMode !== "pick"} style="margin-top:8px"
              placeholder="node-id-a, node-id-b" bind:value={kNodes} />
            <span class="kf__hint" id="scopeHint">{scopeMode === "pick" ? "This key works only on these nodes - others reject it. Comma-separated node ids." : "The key works on every node you put on air."}</span>
          </div>

          <div class="kf__field">
            <span class="kf__k">Model scope</span>
            <div class="kf__opts" role="radiogroup" aria-label="Model scope">
              {#each [["all", "Any model"], ["pick", "Pick models"]] as [m, label]}
                <button type="button" class="kf__opt{modelScope === m ? ' is-active' : ''}" data-mscope={m} aria-pressed={modelScope === m ? "true" : "false"} onclick={() => (modelScope = m)}>{label}</button>
              {/each}
            </div>
            <input id="kModels" class="kf__in" type="text" hidden={modelScope !== "pick"} style="margin-top:8px"
              placeholder="qwen3.8-27b, gpt-oss-120b" bind:value={kModels} />
            <span class="kf__hint" id="mscopeHint">{modelScope === "pick" ? "This key can only call these models - any other model is rejected. Comma-separated model names." : "The key can call any model your in-scope nodes serve."}</span>
          </div>
        </div>
      </details>

      <button type="submit" class="primary" id="createBtn" disabled={minting}>{minting ? "Minting..." : "Mint key"}</button>
      <p class="kf__err" id="createErr" hidden={!createErr}>{createErr}</p>
    </form>

    <!-- the one-time secret reveal -->
    <div class="kf__reveal" id="reveal" hidden={!reveal}>
      <div class="kf__reveal-head">
        <span class="kf__reveal-tag">shown once</span>
        <span class="kf__reveal-msg">Save this now. You will not see it again.</span>
      </div>
      <div class="kf__secret">
        <code id="secretVal" class="kf__secret-val">{reveal ? reveal.secret : "rog-grant_..."}</code>
        <button type="button" class="kf__copy" id="copySecret" data-copy={reveal ? reveal.secret : ""} onclick={copySecret}>{copied ? "Copied" : "Copy"}</button>
      </div>
      <div class="kf__paste">
        <span class="kf__paste-k">Use it as an OpenAI key, no login:</span>
        <pre class="cmd"><code id="envLines">{reveal ? reveal.env : ""}</code></pre>
      </div>
      <p class="fine">Headless setup, a worked bot example, rotation and caps are in the
        <a href="/manual.html#s5">manual &sect;5</a>.</p>
    </div>
  </section>

  <!-- GRANT KEYS: EXISTING -->
  <section class="panel">
    <h2>Your grant keys</h2>

    <div class="kf__tablewrap" id="keysWrap" hidden={gview !== "ready"}>
      <table class="mx-table kf__table">
        <thead>
          <tr>
            <th scope="col">Name</th>
            <th scope="col">Price</th>
            <th scope="col">Expiry</th>
            <th scope="col" class="num">Daily cap</th>
            <th scope="col" class="num">Used (day/mo)</th>
            <th scope="col">Status</th>
            <th scope="col"><span class="vh">Actions</span></th>
          </tr>
        </thead>
        <tbody id="keysRows">
          {#each grants as g (g.id)}
            {@const u = g.usage || {}}
            <tr class={g.status !== "active" ? "kf__row--off" : undefined}>
              <td class="mx-model">{g.name || "-"}</td>
              <td>{priceLabel(g)}</td>
              <td>{expiryLabel(g.expires_at)}</td>
              <td class="num">{capLabel(g.daily_cap)}</td>
              <td class="num">{num(u.day_tokens || 0)} / {num(u.month_tokens || 0)}</td>
              <td><span class="kf__status kf__status--{g.status || 'active'}">{g.status || "active"}</span></td>
              <td class="kf__act">{#if g.status === "active"}<button type="button" class="kf__rowbtn" title="Edit caps" onclick={() => editCaps(g)}>Caps</button><button type="button" class="kf__rowbtn kf__rowbtn--danger" onclick={() => revokeGrant(g)}>Revoke</button>{:else}-{/if}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    <p class="mx-state" id="keysLoading" hidden={gview !== "loading" && !gBusy}>Loading your keys...</p>
    <p class="mx-state" id="keysEmpty" hidden={gview !== "empty"}>No keys yet. Mint one above, or run
      <code>roger grant create --name my-bots</code>.</p>
    <section class="mx-state" id="keysNoOperator" hidden={gview !== "nooperator"}>
      <h2>Keys come from a machine on air</h2>
      <p>An API key lets someone use the models your machines serve, so it is minted by an account that has put one on air. Nothing is registered to the account you are signed in with.</p>
      <p><b>Already sharing?</b> Sign in with the account you used for <code>roger login</code> (GitHub or Apple). <b>Not yet?</b></p>
      <pre class="cmd">roger login
roger share</pre>
      <p class="fine"><a href="/login.html">Switch sign-in</a> &middot; <a href="/account.html">Account</a></p>
    </section>
    <p class="mx-state" id="keysError" hidden={gview !== "fault"}>Could not load your keys. Try again shortly.</p>
  </section>

  <div class="man-note kf__guard">
    <span class="man-note__tag">second guardrail</span>
    Per-key caps limit one key. A single <b>account-wide monthly spend cap</b> bounds what
    every sponsored key can cost you together - set it on <a href="/billing.html">Billing</a>.
  </div>

  <AccountNav current="keys" />
  <button id="logout" class="ghost" onclick={logout}>Sign out</button>
</main>

<!-- Logged-out gate: shown when the broker says we are not signed in. -->
<main class="card" id="gate" hidden={gate !== "out"}>
  <h1>API keys</h1>
  <p class="sub">Log in to mint and manage your account keys and grant keys.</p>
  <section class="acct-state tint-panel">
    <h2>Signed out</h2>
    <p>Grant keys are owner-issued: you sign in once - with GitHub, Apple, or an emailed
      code - then mint private bearer keys for your bots, family, or your own headless
      boxes. Each key is shown only once.</p>
    <p class="research-actions research-actions--lead"><a class="research-button" href="/login.html">Log in</a></p>
  </section>
</main>
</div>
