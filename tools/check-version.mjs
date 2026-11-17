import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const read = (file) => fs.readFileSync(path.join(root, file), "utf8");
const version = read("VERSION").trim();
const problems = [];

if (!/^\d+\.\d+\.\d+$/.test(version)) problems.push(`VERSION is not semver: ${version}`);

const pkg = JSON.parse(read("package.json"));
if (pkg.version !== version) problems.push(`package.json is ${pkg.version}, expected ${version}`);

const lock = JSON.parse(read("package-lock.json"));
if (lock.version !== version) problems.push(`package-lock.json is ${lock.version}, expected ${version}`);
if (lock.packages?.[""]?.version !== version) {
  problems.push(`package-lock.json root package is ${lock.packages?.[""]?.version}, expected ${version}`);
}

function checkAssetQueries(file) {
  const source = read(file);
  const found = [...source.matchAll(/\?v=([0-9][^"'#\s]*)/g)].map((match) => match[1]);
  if (!found.length) problems.push(`${file} has no versioned asset URLs`);
  for (const value of new Set(found)) {
    if (value !== version) problems.push(`${file} asset version is ${value}, expected ${version}`);
  }
  return found.length;
}

const indexAssets = checkAssetQueries("public/index.html");
const workerAssets = checkAssetQueries("public/sw.js");
checkAssetQueries("admin/index.html");
checkAssetQueries("backend/internal/control/web/index.html");
checkAssetQueries("backend/internal/control/web/app.js");
checkAssetQueries("backend/internal/control/web/views-office.js");

const workerVersion = read("public/sw.js").match(/const VERSION = "([^"]+)";/)?.[1];
if (workerVersion !== version) problems.push(`public/sw.js cache version is ${workerVersion}, expected ${version}`);

const controlVersion = read("backend/internal/control/web/app.js").match(/const APP_VERSION = '([^']+)';/)?.[1];
if (controlVersion !== version) problems.push(`System Control APP_VERSION is ${controlVersion}, expected ${version}`);

const providerVersion = read("public/app.js").match(/const MAIL_PROVIDER_ASSET_VERSION = "([^"]+)";/)?.[1];
if (providerVersion !== version) problems.push(`provider asset version is ${providerVersion}, expected ${version}`);

for (const asset of [
  "public/icons/icon-512.png",
  "public/icons/logo-white.png",
  "admin/brand/icon-512.png",
  "admin/brand/logo-white.png",
  "backend/internal/control/web/brand/icon-512.png"
]) {
  if (!fs.existsSync(path.join(root, asset))) problems.push(`missing brand asset: ${asset}`);
}

if (problems.length) {
  for (const problem of problems) console.error(`✗ ${problem}`);
  process.exit(1);
}

console.log(`Version check passed: ${version} (${indexAssets} shell assets, ${workerAssets} cached assets)`);
