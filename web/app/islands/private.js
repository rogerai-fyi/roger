// The Base Station island (/private.html): the page prerendered the card; hydrate it.
import { island } from "#lib/island.js";
import PrivateApp from "#lib/private/PrivateApp.svelte";
island("private", PrivateApp);
