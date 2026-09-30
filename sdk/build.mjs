// Builds the committed bundle the collector embeds and serves at
// /js/twillingate.js. Deterministic for a given esbuild version, so CI can
// rebuild and `git diff --exit-code` the artifact.
import { build } from "esbuild";

// __TWILLINGATE_VERSION__ is substituted by the collector at serve time
// with its own build version (the release tag), so the served file names
// the release it shipped in while the committed artifact stays
// deterministic for CI's drift check.
await build({
  entryPoints: ["src/entry.ts"],
  bundle: true,
  format: "iife",
  target: "es2018",
  minify: true,
  legalComments: "none",
  banner: {
    js: "/* twillingate.js __TWILLINGATE_VERSION__ — views and product analytics SDK.\n * MIT License. Source: sdk/ in https://github.com/dmtrkzntsv/twillingate */",
  },
  outfile: "../internal/server/twillingate.js",
});
console.log("built ../internal/server/twillingate.js");

// The Web Vitals bundle, served at /js/twillingate-vitals.js and loaded by
// the SDK only when an instance enables vitals. It carries web-vitals'
// Apache-2.0 notice; the main bundle stays MIT.
await build({
  entryPoints: ["src/vitals-bundle.ts"],
  bundle: true,
  format: "iife",
  target: "es2018",
  minify: true,
  legalComments: "none",
  banner: {
    js: "/* twillingate-vitals.js __TWILLINGATE_VERSION__ — Web Vitals for the twillingate SDK.\n * MIT License. Source: sdk/ in https://github.com/dmtrkzntsv/twillingate\n * Bundles web-vitals (https://github.com/GoogleChrome/web-vitals), Copyright Google LLC,\n * licensed under the Apache License, Version 2.0: http://www.apache.org/licenses/LICENSE-2.0 */",
  },
  outfile: "../internal/server/twillingate-vitals.js",
});
console.log("built ../internal/server/twillingate-vitals.js");
