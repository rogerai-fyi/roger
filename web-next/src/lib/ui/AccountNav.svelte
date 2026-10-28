<script>
  // Account-page footer sub-nav: the same pages, same order as the profile dropdown
  // (session.js), minus the page you are on. Separators are drawn by CSS.
  let { current } = $props();
  const pages = [
    ["dashboard", "/dashboard.html", "Dashboard"], ["console", "/console.html", "Console"],
    ["stations", "/stations.html", "Stations"], ["metrics", "/usage.html", "Metrics"],
    ["billing", "/billing.html", "Billing"], ["payouts", "/payouts.html", "Payouts"],
    ["keys", "/keys.html", "API keys"], ["account", "/account.html", "Account"],
  ];
</script>
<p class="fine acctsubnav">{#each pages as [key, href, label]}{#if key !== current}<a {href}>{label}</a>
{/if}{/each}</p>
