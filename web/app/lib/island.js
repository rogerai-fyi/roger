// Mount an island over the markup the page prerendered for it (lib/ui/Island.svelte).
// Hydration is strict (recover: false) so a markup mismatch is an error here, not a silent
// remount; the fallback then mounts from scratch so the page still works, and the island
// says which happened in data-hydrated (the browser flow asserts "true").
import { hydrate, mount } from "svelte";

export function island(name, Component, props = {}) {
  const target = document.querySelector(`[data-island="${name}"]`);
  if (!target) throw new Error(`no [data-island="${name}"] in the page`);
  try {
    hydrate(Component, { target, props, recover: false });
    target.dataset.hydrated = "true";
  } catch (err) {
    console.error(`island ${name}: hydration failed, mounting from scratch`, err);
    target.replaceChildren();
    mount(Component, { target, props });
    target.dataset.hydrated = "false";
  }
}
