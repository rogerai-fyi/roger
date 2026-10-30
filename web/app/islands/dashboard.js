// The dashboard island (/dashboard.html): the page prerendered the card; hydrate it.
import { island } from "#lib/island.js";
import DashboardApp from "#lib/dashboard/DashboardApp.svelte";
island("dashboard", DashboardApp);
