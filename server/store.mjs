import fs from "node:fs/promises";
import path from "node:path";

/**
 * Tiny JSON data store. Atomic writes (temp file + rename), serialized so
 * concurrent requests cannot interleave partial files. Swap this adapter for
 * your database of choice without touching the HTTP layer.
 */
export class DataStore {
  constructor({ file, seed }) {
    this.file = file;
    this.seed = seed;
    this.db = null;
    this._chain = Promise.resolve();
  }

  async load() {
    try {
      const raw = await fs.readFile(this.file, "utf8");
      this.db = JSON.parse(raw);
      if (!this.db || this.db.version !== 2) throw new Error("Unsupported data version");
      return this.db;
    } catch (error) {
      if (error.code !== "ENOENT" && !(error instanceof SyntaxError) && error.message !== "Unsupported data version") {
        throw error;
      }
      this.db = this.seed();
      await this.save();
      return this.db;
    }
  }

  async reset() {
    this.db = this.seed();
    await this.save();
    return this.db;
  }

  async save() {
    const payload = JSON.stringify(this.db, null, 2);
    const target = this.file;
    this._chain = this._chain.then(async () => {
      await fs.mkdir(path.dirname(target), { recursive: true });
      const temp = `${target}.${process.pid}.${Date.now()}.tmp`;
      await fs.writeFile(temp, payload, "utf8");
      await fs.rename(temp, target);
    });
    return this._chain;
  }
}
