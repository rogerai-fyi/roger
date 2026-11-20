<script>
  // The money-flow help modal on /billing (legacy: web/src/js/billing-help.js). Opened by any
  // [data-help-open] on the page (the Wallet "?" and the fine-print link); it explains in plain
  // language how money is tracked end to end: metered -> priced -> debited -> written to the
  // append-only ledger -> "Verified" re-sums that ledger to re-derive the balance -> operator
  // earnings accrue and pay out. No network, no state of its own.
  //
  // Accessibility: focus moves into the dialog on open and restores to the opener on close;
  // Esc and a backdrop click close it; Tab is trapped inside the dialog; every trigger's
  // aria-expanded is kept in sync. The #ledger-demo explainer video plays only while open and
  // never under reduced-motion (it rests on its poster frame). Its 3.7MB poster gif is
  // deferred (data-poster) so a closed modal costs zero bytes on page load: a `poster=` gif is
  // fetched on paint even under preload="none". It becomes the real poster the first time the
  // modal opens. The gif <img> covers browsers without <video>.
  import { onMount, tick } from "svelte";

  let open = $state(false);
  let closeBtn, dialog, video;
  let opener = null;

  const reducedMotion = () => typeof window.matchMedia === "function" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  function setExpanded(on) {
    for (const t of document.querySelectorAll("[data-help-open]")) t.setAttribute("aria-expanded", on ? "true" : "false");
  }
  function playDemo() {
    if (!video) return;
    const p = video.getAttribute("data-poster");
    if (p && !video.getAttribute("poster")) video.setAttribute("poster", p);
    if (reducedMotion()) {
      try { video.pause(); video.currentTime = 0; } catch { /* rests on the poster frame */ }
      return;
    }
    try { const r = video.play(); if (r && r.catch) r.catch(() => {}); } catch { /* autoplay refused: the poster stays */ }
  }
  function pauseDemo() {
    try { if (video) video.pause(); } catch { /* already paused */ }
  }

  export async function show() {
    opener = document.activeElement;
    open = true;
    document.body.classList.add("bx-help-open");
    setExpanded(true);
    playDemo();
    await tick();
    if (closeBtn) closeBtn.focus();
  }
  export function hide() {
    if (!open) return;
    open = false;
    document.body.classList.remove("bx-help-open");
    setExpanded(false);
    pauseDemo();
    if (opener && opener.focus) { try { opener.focus(); } catch { /* the opener is gone */ } }
  }

  // Keep focus inside the dialog while open (a light focus trap).
  function trap(e) {
    if (e.key !== "Tab") return;
    const f = dialog.querySelectorAll('button, a[href], select, textarea, input, [tabindex]:not([tabindex="-1"])');
    if (!f.length) return;
    const first = f[0], last = f[f.length - 1];
    if (e.shiftKey && document.activeElement === first) { last.focus(); e.preventDefault(); }
    else if (!e.shiftKey && document.activeElement === last) { first.focus(); e.preventDefault(); }
  }

  onMount(() => {
    // delegated open: any [data-help-open] anywhere on the page
    const onClick = (e) => {
      const t = e.target.closest && e.target.closest("[data-help-open]");
      if (!t) return;
      e.preventDefault();
      show();
    };
    const onKey = (e) => { if (e.key === "Escape" && open) hide(); };
    document.addEventListener("click", onClick);
    document.addEventListener("keydown", onKey);
    return () => { document.removeEventListener("click", onClick); document.removeEventListener("keydown", onKey); };
  });
</script>
<div class="bx-help" id="ledgerModal" hidden={!open}>
  <div class="bx-help__scrim" id="ledgerScrim" aria-hidden="true" onclick={hide}></div>
  <div class="bx-help__dialog" role="dialog" aria-modal="true"
       aria-labelledby="ledgerTitle" aria-describedby="ledgerLede" bind:this={dialog} onkeydown={trap}>
    <button type="button" class="bx-help__x" id="ledgerClose" aria-label="Close" bind:this={closeBtn} onclick={hide}>&times;</button>
    <h2 class="bx-help__title" id="ledgerTitle">Where your money goes</h2>
    <p class="bx-help__lede" id="ledgerLede">Every request is metered, priced, and debited from your
      wallet - and every change is written to an append-only ledger you can audit.</p>

    <!-- ANIMATED-EXPLAINER SLOT. #ledger-demo is the stable drop-in target; it hosts the
         ComfyUI-generated explainer (assets/ledger-demo.*, Ping, mono + one-red). The slot's
         role="img" + aria-label is the accessible description; the <video> is decorative
         motion, with the gif <img> as the fallback. -->
    <figure class="bx-demo">
      <div class="bx-demo__stage" id="ledger-demo" role="img"
           aria-label="Flow diagram: tokens are metered, then priced, then debited from your wallet, then written to the append-only ledger, which is re-summed to verify your balance.">
        <!-- LAZY: preload="none" (no metadata/video fetch), the poster deferred to data-poster,
             the <img> fallback loading="lazy": the closed modal costs zero bytes. -->
        <video class="bx-demo__vid" muted loop playsinline preload="none"
               data-poster="assets/ledger-demo.gif" aria-hidden="true" bind:this={video}>
          <source src="assets/ledger-demo.mp4" type="video/mp4">
          <img class="bx-demo__gif" src="assets/ledger-demo.gif" alt="" loading="lazy">
        </video>
      </div>
      <figcaption class="bx-demo__cap">tokens &rarr; price &rarr; wallet &rarr; ledger &rarr; verified</figcaption>
    </figure>

    <!-- the flow, as a station-log spec list -->
    <ol class="bx-flow">
      <li class="bx-flow__step"><span class="bx-flow__k">Metered</span>
        <span class="bx-flow__v">Each request counts the tokens going in and coming out.</span></li>
      <li class="bx-flow__step"><span class="bx-flow__k">Priced</span>
        <span class="bx-flow__v">Those tokens are priced at the model's posted rate ($ per 1M in + out).</span></li>
      <li class="bx-flow__step"><span class="bx-flow__k">Debited</span>
        <span class="bx-flow__v">That cost is debited from your wallet balance.</span></li>
      <li class="bx-flow__step"><span class="bx-flow__k">Ledgered</span>
        <span class="bx-flow__v">Every top-up and charge is written to an append-only ledger - entries are never edited or deleted.</span></li>
      <li class="bx-flow__step"><span class="bx-flow__k">Verified</span>
        <span class="bx-flow__v">"Verified" re-sums that whole ledger to re-derive your balance, as a drift check. It should always match Balance.</span></li>
      <li class="bx-flow__step"><span class="bx-flow__k">Paid out</span>
        <span class="bx-flow__v">If you run a node, your operator earnings accrue on the same ledger and pay out to you.</span></li>
    </ol>

    <!-- fair billing: you only pay for successful, quality tokens -->
    <p class="bx-help__h">What you're charged for</p>
    <ul class="bx-fair">
      <li><b>No usable output is $0.</b> If a request returns nothing usable, the hold is refunded - you pay nothing, and the operator earns nothing (and takes a strike). A model that can't actually respond never costs you.</li>
      <li><b>Counts can't be inflated.</b> Tokens are billed on the broker's own independent re-count, not the node's claim, so a provider can't pad counts to over-bill you.</li>
      <li><b>You pay only for quality.</b> An operator earns only on tokens that actually served you - a matching, usable response is the condition for payment. This is enforced server-side at the broker, so it holds for any client (the roger CLI, opencode, or any OpenAI-compatible tool).</li>
    </ul>

    <!-- the two numbers people mix up -->
    <p class="bx-help__h">Two numbers people mix up</p>
    <dl class="bx-help__pair">
      <div class="bx-help__def">
        <dt class="bx-help__term">Balance</dt>
        <dd class="bx-help__gloss">Your spendable total - the money in your wallet right now.</dd>
      </div>
      <div class="bx-help__def">
        <dt class="bx-help__term">You paid</dt>
        <dd class="bx-help__gloss">The cost of your last request (a per-turn figure). It is not your wallet.</dd>
      </div>
    </dl>

    <div class="bx-help__actions">
      <button type="button" class="bx-help__done" id="ledgerDone" onclick={hide}>Got it</button>
    </div>
  </div>
</div>
