// The account hub island (/account.html): the page prerendered the card; hydrate it.
import { island } from "#lib/island.js";
import AccountApp from "#lib/account/AccountApp.svelte";
island("account", AccountApp);
