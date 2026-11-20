// The payouts island (/payouts.html): the page prerendered the card; hydrate it.
import { island } from "#lib/island.js";
import PayoutsApp from "#lib/account/PayoutsApp.svelte";
island("payouts", PayoutsApp);
