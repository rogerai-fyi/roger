// The stations island (/stations.html): the page prerendered the card; hydrate it.
import { island } from "#lib/island.js";
import StationsApp from "#lib/account/StationsApp.svelte";
island("stations", StationsApp);
