// The metrics island (/usage.html): the page prerendered the card and the gate; hydrate them.
import { island } from "#lib/island.js";
import UsageApp from "#lib/usage/UsageApp.svelte";
island("usage", UsageApp);
