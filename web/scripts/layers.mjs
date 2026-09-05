#!/usr/bin/env node
/**
 * Enforces the Feature-Sliced import rules.
 *
 * Two rules, and both exist because breaking either one turns the layering back
 * into the ball of mutual imports it replaced:
 *
 *   1. A layer may only import from layers below it. Otherwise "shared" ends up
 *      importing a page, and nothing can be read or moved on its own again.
 *
 *   2. A slice may not reach into another slice's internals. Crossing at the
 *      index means each slice has a surface that is deliberately chosen; going
 *      straight to entities/expense/model/queries makes every file public and
 *      every rename a breaking change.
 *
 * Run in CI. A convention nothing checks is a convention for about a month.
 */
import { readdir, readFile } from "node:fs/promises";
import { join, relative, sep } from "node:path";
import { fileURLToPath } from "node:url";

const SRC = fileURLToPath(new URL("../src", import.meta.url));

// Lowest first. The index is the rank: a layer may import strictly below it.
const LAYERS = ["shared", "entities", "features", "widgets", "pages", "app"];

const IMPORT = /(?:^|\n)\s*(?:import|export)[\s\S]*?from\s+["']([^"']+)["']/g;

async function* walk(dir) {
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) yield* walk(path);
    else if (/\.(ts|tsx)$/.test(entry.name)) yield path;
  }
}

/** ["entities", "expense", "model", "queries.ts"] for a path inside src. */
const segmentsOf = (path) => relative(SRC, path).split(sep);

const violations = [];

for await (const file of walk(SRC)) {
  const [layer, slice] = segmentsOf(file);
  const rank = LAYERS.indexOf(layer);
  if (rank === -1) continue; // main.tsx, test setup

  const source = await readFile(file, "utf8");

  for (const [, specifier] of source.matchAll(IMPORT)) {
    if (!specifier.startsWith("@/")) continue;

    const [importedLayer, importedSlice, ...rest] = specifier.slice(2).split("/");
    const importedRank = LAYERS.indexOf(importedLayer);
    if (importedRank === -1) continue;

    const where = `${relative(SRC, file)}: ${specifier}`;

    if (importedRank > rank) {
      violations.push(`${where}\n    ${layer} may not import from ${importedLayer}`);
      continue;
    }

    if (importedRank === rank) {
      // Shared has no slices; its modules are meant to be imported directly.
      if (layer === "shared") continue;
      if (importedSlice === slice) continue;
      violations.push(
        `${where}\n    ${layer}/${slice} may not import a sibling slice; move the shared part down a layer`,
      );
      continue;
    }

    // Below, but it has to come through that slice's public surface.
    if (importedLayer !== "shared" && rest.length > 0) {
      violations.push(
        `${where}\n    reaches past ${importedLayer}/${importedSlice}'s index; import from "@/${importedLayer}/${importedSlice}"`,
      );
    }
  }
}

if (violations.length > 0) {
  console.error(`Feature-Sliced boundaries broken in ${violations.length} place(s):\n`);
  for (const violation of violations) console.error(`  ${violation}\n`);
  process.exit(1);
}

console.log("Feature-Sliced boundaries hold.");
