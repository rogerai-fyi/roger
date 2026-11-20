// The billing island (/billing.html): the page prerendered the card and the help modal; hydrate them.
import { island } from "#lib/island.js";
import BillingApp from "#lib/billing/BillingApp.svelte";
island("billing", BillingApp);
