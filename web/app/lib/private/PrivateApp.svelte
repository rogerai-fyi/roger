<script>
  // Base Station, /private.html (legacy: web/src/js/private.js): the web half of
  // /remote-control. It lists the owner's remote agent sessions and private bands, opens a
  // LIVE session view (continue the chat running on another machine), and mints a one-time
  // link code to continue on a phone.
  //
  // Auth is the session cookie (credentialed CORS; the web origin is echoed). To VIEW a session
  // the browser owner-JOINs it by id (POST /rc/{id}/join): no code needed for your own
  // already-signed-in surface; the code is only for linking a NOT-signed-in device. Streaming
  // uses fetch() + ReadableStream (not EventSource, which cannot set the X-Roger-Attach header).
  // Honest labels: PRIVATE = account-locked + broker-relayed, NOT end-to-end encrypted.
  import { onMount, tick } from "svelte";
  import { api } from "#lib/api.js";
  import { stateLabel, frameLine, sseFrames } from "./rc.js";

  let mounted = $state(false);
  let roster = $state("loading");   // loading | gate | error | ready
  let sessions = $state([]);
  let bands = $state(null);         // the list, or { failed: reason }: A FAILURE IS NOT AN EMPTY LIST
  let current = $state(null);       // { id, name, attach, pendingConfirm } while a session view is open
  let lines = $state([]);           // the transcript
  let attachError = $state(null);   // { name }: the join failed, the view says so, no session is open
  let ask = $state("");
  let link = $state(null);          // { name, code, url } once a link code was minted
  let stream;                       // the transcript element, kept scrolled to the end
  let reader = null;                // the open SSE reader

  // ---- roster ----
  async function loadRoster() {
    try {
      const r = await api("/rc/sessions");
      if (r.status === 401) { roster = "gate"; return; }
      if (!r.ok) throw new Error("sessions " + r.status);
      const data = await r.json();
      sessions = (data && data.sessions) || [];
      roster = "ready";
      loadBands();
    } catch { roster = "error"; }
  }
  // This used to map every non-ok response (and every thrown error) to an empty list, so a
  // 403 rendered "No private bands yet": the page confidently told owners they owned nothing
  // when it had simply failed to ask properly. Say which one actually happened.
  async function loadBands() {
    try {
      const r = await api("/bands");
      if (!r.ok) {
        throw new Error(r.status === 401
          ? "sign in to see your private bands"
          : r.status === 403   // signed in, but no machine is linked to this sign-in: not "sign in"
            ? "private bands belong to an account with a machine on air - none is linked to this sign-in (run roger login, then roger share)"
            : "could not load your private bands (" + r.status + ")");
      }
      const data = await r.json();
      bands = (data && data.bands) || [];
    } catch (e) { bands = { failed: (e && e.message) || "could not load your private bands" }; }
  }
  const bandList = $derived(Array.isArray(bands) ? bands : []);

  // ---- the live session view ----
  async function openSession(s) {
    try {
      const r = await api("/rc/" + encodeURIComponent(s.id) + "/join", { method: "POST" });
      if (!r.ok) throw new Error("join " + r.status);
      const res = await r.json();
      openWithToken(s.id, s.name, res.attach_token);
    } catch { stopStream(); current = null; lines = []; attachError = { name: s.name || s.id }; }
  }
  // openWithToken opens the live view with an attach token already held (the owner-join above,
  // or the /r link flow which stashed one in sessionStorage). It first tears down any stream
  // already open, so re-opening (or a double-click while join is in flight) never leaks an
  // orphaned reader whose frames would bleed into the new session's transcript.
  function openWithToken(id, name, attach) {
    stopStream();
    current = { id, name, attach, pendingConfirm: "" };
    attachError = null;
    lines = [];
    streamSession();
  }
  function stopStream() {
    if (reader) { try { reader.cancel(); } catch { /* already closed */ } reader = null; }
  }
  // pendingOpen consumes a session stashed by the /r link flow, so opening a QR link lands the
  // phone straight in the live session.
  function pendingOpen() {
    let raw;
    try { raw = sessionStorage.getItem("rc_open"); sessionStorage.removeItem("rc_open"); } catch { return; }
    if (!raw) return;
    try {
      const o = JSON.parse(raw);
      if (o && o.id && o.attach) openWithToken(o.id, o.name, o.attach);
    } catch { /* not ours */ }
  }
  async function appendLine(line) {
    if (!line) return;
    if (line.ended) { stopStream(); if (current) current.pendingConfirm = ""; }
    // the per-line buttons answer the exact confirm they were drawn for, even when more than
    // one is pending on the host; the shared pendingConfirm is only the ask-box y/n shortcut
    // (the latest confirm)
    if (line.confirm !== undefined && current) current.pendingConfirm = line.confirm;
    lines = [...lines, line];
    await tick();
    if (stream) stream.scrollTop = stream.scrollHeight;
  }
  async function streamSession() {
    if (!current) return;
    const mine = current;
    try {
      const r = await api("/rc/" + encodeURIComponent(mine.id) + "/stream", { headers: { "X-Roger-Attach": mine.attach } });
      if (current !== mine) { try { await r.body?.cancel(); } catch { /* the view moved on */ } return; }   // a later open won
      if (!r.ok || !r.body) throw new Error("stream " + r.status);
      const rd = r.body.getReader();
      reader = rd;
      const dec = new TextDecoder();
      let carry = "";
      for (;;) {
        let res;
        try { res = await rd.read(); } catch { return; }
        if (res.done || reader !== rd) return;
        const { frames, rest } = sseFrames(carry, dec.decode(res.value, { stream: true }));
        carry = rest;
        for (const f of frames) appendLine(frameLine(f));
      }
    } catch { appendLine({ cls: "rc-err", text: "✗ stream error" }); }
  }
  function sendInbound(inb) {
    if (!current) return Promise.resolve();
    return api("/rc/" + encodeURIComponent(current.id) + "/send", {
      method: "POST", headers: { "Content-Type": "application/json", "X-Roger-Attach": current.attach }, body: JSON.stringify(inb),
    });
  }
  function answerConfirm(approve, id) {
    if (!current) return;
    if (id === undefined) id = current.pendingConfirm;   // the ask-box y/n shortcut: the latest confirm
    current.pendingConfirm = "";
    sendInbound({ kind: "confirm", approve, confirm_id: id, origin: "web" })
      .catch(() => appendLine({ cls: "rc-err", text: "✗ could not send the answer" }));
  }
  function send(e) {
    e.preventDefault();
    const v = (ask || "").trim();
    if (!v || !current) return;
    ask = "";
    // a bare y/n answers the latest pending confirm; otherwise it is a turn
    const fail = () => appendLine({ cls: "rc-err", text: "✗ could not send" });
    if (current.pendingConfirm && /^(y|yes|n|no)$/i.test(v)) {
      const approve = /^y/i.test(v), id = current.pendingConfirm;
      current.pendingConfirm = "";
      sendInbound({ kind: "confirm", approve, confirm_id: id, origin: "web" }).catch(fail);
    } else {
      sendInbound({ kind: "turn", text: v, origin: "web" }).catch(fail);
    }
  }
  function closeView() {
    stopStream();
    current = null;
    attachError = null;
  }

  // ---- link a phone ----
  async function linkPhone(s) {
    try {
      const r = await api("/rc/" + encodeURIComponent(s.id) + "/code", { method: "POST" });
      if (!r.ok) throw new Error("code " + r.status);
      const res = await r.json();
      const short = res.code_short || "";
      link = { name: s.name || s.id, code: short, url: "https://rogerai.fm/r.html#" + short };
    } catch { /* the roster stays */ }
  }

  function whatPrivate(e) {
    e.preventDefault();
    window.alert("Private means account-locked and code-gated. Traffic is carried over TLS through "
      + "the RogerAI broker relay - it is not end-to-end encrypted, and tools run on the host "
      + "machine after you confirm them.");
  }

  onMount(() => {
    mounted = true;
    loadRoster();
    pendingOpen();   // arrived via a /r link: open that session straight away
    return stopStream;
  });
</script>
<div class="authwrap tint-panel">
<main class="card" id="card" hidden={!mounted}>
  <section class="panel" id="privateAbout">
    <h2>What a private band is</h2>
    <p>A station hidden from the public market, reachable only by a frequency code you
      hold - your model, on the network's routing, receipts and failover, without a
      public listing. Mint one from a share you own; hand the code only to your people.
      The <a href="/manual.html">manual</a> has the full ceremony, and
      <a href="/pricing.html#why">the why page</a> has the argument.</p>
  </section>
  <h1>Base Station</h1>
  <p class="sub">Your private side of the dial. <b>&#9673; PRIVATE</b> - your account
    only &middot; relayed through the broker &middot;
    <a href="#" id="whatPrivate" onclick={whatPrivate}>over TLS, not end-to-end encrypted</a>.</p>

  <p class="cn-state" id="loading" hidden={roster !== "loading"}>Tuning in...</p>
  <p class="cn-state" id="error" hidden={roster !== "error"}>Could not reach Base Station. Try again shortly.</p>
  <div class="acct-state tint-panel" id="gate" hidden={roster !== "gate"}><p>Sign in to see your private sessions and bands.
    <a href="/login.html">Log in</a>.</p></div>

  <!-- ============ REMOTE SESSIONS ============ -->
  <section class="panel" id="sessionsWrap" hidden={roster !== "ready"}>
    <h2>Remote sessions</h2>
    <p class="fine">Agent sessions live on your other machines. Run
      <code>/remote-control</code> inside <code>roger</code> (the <code>[0] AGENT</code>) to
      put one on the air, then continue it here.</p>
    <div class="cn-tablewrap">
      <table class="cn-table" id="sessionsTable" hidden={!sessions.length}>
        <thead>
          <tr>
            <th scope="col" class="cn-st" aria-label="Status"></th>
            <th scope="col">Session</th>
            <th scope="col">State</th>
            <th scope="col" class="num"></th>
          </tr>
        </thead>
        <tbody id="sessionRows">
          {#each sessions as s (s.id)}
            {@const st = stateLabel(s)}
            <tr>
              <td class="cn-st{st.live ? ' is-live' : ''}">{st.dot}</td>
              <td>{s.name || s.id}</td>
              <td>{st.label}</td>
              <td class="num">{#if !s.revoked}<button type="button" class="rc-btn primary" onclick={() => openSession(s)}>{s.online ? "Open" : "View"}</button><button type="button" class="rc-btn ghost" onclick={() => linkPhone(s)}>Link</button>{/if}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    <p class="fine" id="sessionsEmpty" hidden={roster !== "ready" || sessions.length > 0}>No remote sessions yet.</p>
  </section>

  <!-- ============ LIVE SESSION VIEW ============ -->
  <section class="panel" id="viewWrap" hidden={!current && !attachError}>
    <h2 id="viewTitle">{current ? "Session - " + (current.name || current.id) : attachError ? "Session - " + attachError.name : "Session"}</h2>
    <p class="sub" id="viewBadge"><b>&#9673; PRIVATE</b> - your account only &middot;
      broker relay &middot; tools run on the host</p>
    <div class="rc-stream" id="stream" aria-live="polite" bind:this={stream}>{#if attachError}Could not attach to this session.{/if}{#each lines as line}{#if line.confirm !== undefined}<div class="rc-line rc-confirm"><span>{line.text}</span><button type="button" class="rc-btn primary" onclick={() => answerConfirm(true, line.confirm)}>Approve</button><button type="button" class="rc-btn ghost" onclick={() => answerConfirm(false, line.confirm)}>Deny</button></div>{:else}<div class="rc-line {line.cls}">{line.text}</div>{/if}{/each}</div>
    <form id="askForm" class="rc-ask" autocomplete="off" onsubmit={send}>
      <input id="ask" class="kf__in" type="text"
        placeholder="ask from here - it runs on the host" bind:value={ask} />
      <button class="primary" type="submit">Send</button>
      <button class="ghost" type="button" id="closeView" onclick={closeView}>Close</button>
    </form>
  </section>

  <!-- ============ PRIVATE BANDS ============ -->
  <section class="panel" id="bandsWrap" hidden={roster !== "ready"}>
    <h2>Private bands</h2>
    <p class="fine">Hidden stations only a frequency code can tune. Mint one with
      <code>roger share --private</code>; manage keys under <a href="/keys.html">API keys</a>.</p>
    <div class="cn-tablewrap">
      <table class="cn-table" id="bandsTable" hidden={!bandList.length}>
        <thead>
          <tr>
            <th scope="col" class="cn-st" aria-label="Status"></th>
            <th scope="col">Band</th>
            <th scope="col">Frequency</th>
            <th scope="col">Status</th>
          </tr>
        </thead>
        <tbody id="bandRows">
          {#each bandList as b}
            {@const live = b.status === "active"}
            <tr>
              <td class="cn-st{live ? ' is-live' : ''}">{live ? "◉" : "·"}</td>
              <td>{b.label || b.node_id || "-"}</td>
              <td>{b.display || "-"}</td>
              <td>{b.status || "-"}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    <!-- a failed load shows its REASON here, never an empty state that reads as "you have none" -->
    <p class="fine" id="bandsEmpty" hidden={!bands || bandList.length > 0}>{bands && bands.failed ? bands.failed : "No private bands yet."}</p>
  </section>

  <!-- ============ LINK A PHONE (modal-ish panel) ============ -->
  <section class="panel" id="linkWrap" hidden={!link}>
    <h2>Link a device</h2>
    <p class="fine">A one-time link code to continue <b id="linkName">{link ? link.name : "this session"}</b> on
      another device signed into your account. It expires in 10 minutes.</p>
    <p class="rc-code" id="linkCode">{link ? link.code : "-"}</p>
    <p class="fine">Open on a phone: <a id="linkURL" href={link ? link.url : "#"}>{link ? link.url : "-"}</a>
      &middot; <button class="ghost" type="button" id="copyLink" data-copy-target="#linkURL">Copy link</button></p>
  </section>

  <p class="fine"><a href="/console.html">Console</a> &middot;
    <a href="/keys.html">API keys</a> &middot; <a href="/account.html">Account</a></p>
</main>
</div>
