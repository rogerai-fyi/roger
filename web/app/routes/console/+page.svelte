<script>
  // Console - the live operational view. The page is the static shell (chrome + the
  // account head); the card is an island (lib/console/ConsoleApp.svelte) that mounts in
  // the browser and talks to the broker.
  import { siteHead, CSS_ACCOUNT } from "#lib/head.js";
  import Nav from "#lib/ui/Nav.svelte";
  import Footer from "#lib/ui/Footer.svelte";
  import Island from "#lib/ui/Island.svelte";
  import ConsoleApp from "#lib/console/ConsoleApp.svelte";
  const head = siteHead({ title: "Console - RogerAI", robots: "noindex", canonical: "https://rogerai.fm/console.html", css: [...CSS_ACCOUNT, "console.css"] });
</script>
<svelte:head>{@html head}</svelte:head>
<Nav variant="lean" />
<Island name="console"><ConsoleApp /></Island>
<Footer />
