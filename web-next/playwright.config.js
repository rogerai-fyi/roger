// One real-browser flow for the spike: the prerendered console shell hydrates, calls the
// broker (intercepted at the network layer, so the hard-coded broker origin stays), and
// renders the operator view. Serves the OVERLAID dist (legacy assets + spike pages).
import { defineConfig } from "@playwright/test";
// SPIKE_DIST: a legacy web/dist that scripts/overlay.mjs has laid this build over
const DIST = process.env.SPIKE_DIST || new URL("../web/dist", import.meta.url).pathname;
export default defineConfig({
  testDir: "e2e",
  timeout: 30_000,
  use: { baseURL: "http://127.0.0.1:4178", headless: true },
  webServer: { command: `python3 -m http.server 4178 --bind 127.0.0.1 --directory ${DIST}`, url: "http://127.0.0.1:4178/faq.html", reuseExistingServer: true },
  projects: [{ name: "chromium", use: { browserName: "chromium" } }],
});
