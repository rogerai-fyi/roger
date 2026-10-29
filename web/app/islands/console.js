// The console island: hydrates the card the page prerendered (lib/ui/Island.svelte).
import { hydrate } from "svelte";
import ConsoleApp from "#lib/console/ConsoleApp.svelte";
hydrate(ConsoleApp, { target: document.querySelector('[data-island="console"]') });
