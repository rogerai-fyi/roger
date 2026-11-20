// GENERATES app/lib/fmt.js from src/js/fmt.js: the canonical money/count renderer (web ==
// CLI/TUI) as an ES module. The body is the legacy file verbatim; only the export differs.
// test/dual-build.test.mjs pins the generated file to this transform.
import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

export function transform(legacy) {
  if (!/^\(function \(\) \{/m.test(legacy)) throw new Error("unexpected fmt.js shape");
  const body = legacy
    .replace(/^\s*if \(typeof module !== "undefined" && module\.exports\) module\.exports = R;.*$/m, "")
    .replace(/\}\)\(\);\s*$/, "  return R;\n})();\nexport default RogerFmt;\n");
  return "// GENERATED from web/src/js/fmt.js by scripts/gen-fmt.mjs. Do not edit; regenerate.\nconst RogerFmt = " + body;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const src = readFileSync(new URL("../src/js/fmt.js", import.meta.url), "utf8");
  writeFileSync(new URL("../app/lib/fmt.js", import.meta.url), transform(src));
  console.log("app/lib/fmt.js generated");
}
