import { readFileSync } from "node:fs";

import { describe, expect, it } from "vitest";

const BUYER = new URL("./buy.mjs", import.meta.url);
const MANIFEST = JSON.parse(readFileSync(new URL("../../package.json", import.meta.url), "utf8"));

// The package a bare import specifier resolves into: "@scope/name/sub" is in
// "@scope/name", "name/sub" in "name".
function packageOf(specifier) {
  const parts = specifier.split("/");
  return specifier.startsWith("@") ? parts.slice(0, 2).join("/") : parts[0];
}

describe("buy.mjs, run as the package's x402-gno command", () => {
  // npx and npm install the package with its dependencies and peers only, so
  // importing a devDependency works in the repository and fails for everyone
  // running the command.
  it("imports only what installing the package brings", () => {
    const source = readFileSync(BUYER, "utf8");
    const imported = [...source.matchAll(/\bfrom\s+"([^"]+)"/g)]
      .map(([, specifier]) => specifier)
      .filter((specifier) => !specifier.startsWith("node:") && !specifier.startsWith("."));
    const installed = new Set([
      MANIFEST.name,
      ...Object.keys(MANIFEST.dependencies),
      ...Object.keys(MANIFEST.peerDependencies),
    ]);

    expect(imported.length).toBeGreaterThan(0);
    expect(imported.map(packageOf).filter((name) => !installed.has(name))).toEqual([]);
  });
});
