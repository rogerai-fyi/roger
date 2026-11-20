// The sign-in island (/login.html): the page prerendered the card; hydrate it.
import { island } from "#lib/island.js";
import LoginApp from "#lib/account/LoginApp.svelte";
island("login", LoginApp);
