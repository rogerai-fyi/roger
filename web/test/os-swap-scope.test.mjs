// On Windows, site.js rewrites every .install__box WITHOUT [data-os-lock] to the
// PowerShell INSTALL one-liner. Any box whose command is not the client installer
// (a `roger use` / `roger say` line, the Tower's Linux installer) must carry
// data-os-lock, or a Windows reader sees and copies the wrong command.
import { test, before } from "node:test";
import { execFileSync } from "node:child_process";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const SRC = path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "dist");
before(() => execFileSync("node", ["build.mjs"], { cwd: path.join(SRC, "..") }));
const INSTALL = /install\.(sh|ps1)/;

test("only client-installer boxes are open to the Windows command swap", () => {
  for (const f of readdirSync(SRC).filter((n) => n.endsWith(".html"))) {
    const html = readFileSync(path.join(SRC, f), "utf8");
    for (const [box] of html.matchAll(/<button class="install__box[\s\S]*?<\/button>/g)) {
      if (/data-os-lock/.test(box)) continue;
      assert.match(box, INSTALL, `${f}: an install box whose command is not the installer must carry data-os-lock:\n${box.slice(0, 160)}`);
    }
  }
});
