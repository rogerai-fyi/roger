// The console island: hydrates the card the page prerendered (lib/ui/Island.svelte).
import { island } from "#lib/island.js";
import ConsoleApp from "#lib/console/ConsoleApp.svelte";
island("console", ConsoleApp);
