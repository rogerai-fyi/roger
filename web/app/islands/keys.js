// The API keys island (/keys.html): the page prerendered both cards; hydrate them.
import { island } from "#lib/island.js";
import KeysApp from "#lib/keys/KeysApp.svelte";
island("keys", KeysApp);
