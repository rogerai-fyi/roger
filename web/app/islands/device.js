// The device approval island (/device.html): the page prerendered the card; hydrate it.
import { island } from "#lib/island.js";
import DeviceApp from "#lib/account/DeviceApp.svelte";
island("device", DeviceApp);
