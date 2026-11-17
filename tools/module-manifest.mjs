#!/usr/bin/env node
/*
 * Walk the webmail ES-module graph and ensure the service worker precaches
 * every local module needed for an offline first load.
 *
 * Usage:
 *   node tools/module-manifest.mjs
 *   node tools/module-manifest.mjs --json
 */
import fs from "node:fs";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "..");
const publicRoot = path.join(root, "public");
const entry = path.join(publicRoot, "app.js");
const importPattern = /(?:import\s+(?:[^"'()]*?\s+from\s+)?|export\s+[^"']*?\s+from\s+|import\s*\(\s*)["']([^"']+)["']/g;

const graph = new Set();

function resolveModule(fromFile, specifier) {
  if (!specifier.startsWith(".")) return null;
  const resolved = path.resolve(path.dirname(fromFile), specifier);
  if (!resolved.startsWith(publicRoot)) return null;
  if (!fs.existsSync(resolved)) throw new Error(`missing module ${specifier} imported by ${path.relative(root, fromFile)}`);
  return resolved;
}

function walk(file) {
  const relative = path.relative(publicRoot, file).replaceAll(path.sep, "/");
  if (graph.has(relative)) return;
  graph.add(relative);
  const source = fs.readFileSync(file, "utf8");
  for (const match of source.matchAll(importPattern)) {
    const target = resolveModule(file, match[1]);
    if (target) walk(target);
  }
}

walk(entry);
const modules = [...graph].sort();

const sw = fs.readFileSync(path.join(publicRoot, "sw.js"), "utf8");
const shellMatch = sw.match(/const SHELL = \[([\s\S]*?)\n\];/);
if (!shellMatch) {
  console.error("public/sw.js does not expose a SHELL array");
  process.exit(1);
}
const cached = new Set([...shellMatch[1].matchAll(/"(\/[^"?]+)(?:\?[^"]*)?"/g)].map((match) => match[1].replace(/^\//, "")));
const missing = modules.filter((module) => !cached.has(module));

if (process.argv.includes("--json")) {
  console.log(JSON.stringify({ modules, missing }, null, 2));
} else {
  console.log(`Module graph: ${modules.length} local modules`);
  for (const module of modules) console.log(`  /${module}`);
  if (missing.length) {
    console.error("\nMissing from the service worker SHELL:");
    for (const module of missing) console.error(`  /${module}`);
    process.exit(1);
  }
  console.log("Service worker precaches the full module graph.");
}
