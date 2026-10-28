#!/bin/sh
# Run the legacy guard suite against a dist tree that carries this build's pages.
# A scratch copy of web/ gets a build.mjs that runs the legacy build and then the overlay,
# so every test that rebuilds in before() sees the overlaid tree.
set -e
HERE=$(cd "$(dirname "$0")" && pwd)
OUT=${1:?scratch dir}
rm -rf "$OUT" && mkdir -p "$OUT"
cp -r "$HERE/../../web/src" "$HERE/../../web/test" "$HERE/../../web/scripts" "$HERE/../../web/package.json" "$OUT/"
cp "$HERE/../../web/build.mjs" "$OUT/build-legacy.mjs"
cat > "$OUT/build.mjs" <<JS
import "./build-legacy.mjs";
import { execFileSync } from "node:child_process";
execFileSync("node", ["$HERE/overlay.mjs", new URL("./dist", import.meta.url).pathname], { stdio: "inherit" });
JS
cd "$OUT" && node build.mjs && node --test --test-concurrency=1 test/*.mjs
