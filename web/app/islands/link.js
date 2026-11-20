// The link handler island (/r.html): the page prerendered the card; hydrate it.
import { island } from "#lib/island.js";
import LinkApp from "#lib/account/LinkApp.svelte";
island("link", LinkApp);
