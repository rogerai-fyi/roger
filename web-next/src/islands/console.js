// Island measurement: the console page component mounted by Svelte alone (no SvelteKit client
// router). An upper bound: it bundles the whole page component, chrome included.
import { mount } from "svelte";
import Page from "../routes/console/+page.svelte";
mount(Page, { target: document.getElementById("console-root") });
