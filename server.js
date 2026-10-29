import path from "node:path";
import { fileURLToPath } from "node:url";
import { startServer } from "./server/app.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const port = Number(process.env.PORT || 8001);
const host = process.env.HOST || "127.0.0.1";
const dataFile = process.env.DATA_FILE || path.join(__dirname, "data", "db.json");

const app = await startServer({ port, host, dataFile });
const shownHost = host === "0.0.0.0" ? "localhost" : host === "127.0.0.1" ? "localhost" : host;
console.log(`\n  Meridian Mail (Aurora template)`);
console.log(`  → http://${shownHost}:${app.port}`);
console.log(`  data: ${app.dataFile}`);
console.log(`  demo: avery.chen@meridian.example / meridian-demo\n`);

let closing = false;
async function shutdown(signal) {
  if (closing) return;
  closing = true;
  console.log(`\n${signal}: shutting down…`);
  await app.close();
  process.exit(0);
}
process.on("SIGINT", () => shutdown("SIGINT"));
process.on("SIGTERM", () => shutdown("SIGTERM"));
