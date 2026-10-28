// GENERATES src/lib/fmt.js from web/src/js/fmt.js: the canonical money/count renderer (web ==
// CLI/TUI) as an ES module. The body is the legacy file verbatim; only the export differs.
import { readFileSync, writeFileSync } from "node:fs";
let s = readFileSync(new URL("../../web/src/js/fmt.js", import.meta.url), "utf8");
if (!/^\(function \(\) \{/m.test(s)) throw new Error("unexpected fmt.js shape");
s = s.replace(/^\s*if \(typeof module !== "undefined" && module\.exports\) module\.exports = R;.*$/m, "");
s = "// GENERATED from web/src/js/fmt.js by scripts/gen-fmt.mjs. Do not edit.\nconst RogerFmt = " + s.replace(/\}\)\(\);\s*$/, "  return R;\n})();\nexport default RogerFmt;\n");
writeFileSync(new URL("../src/lib/fmt.js", import.meta.url), s);
console.log("src/lib/fmt.js generated");
