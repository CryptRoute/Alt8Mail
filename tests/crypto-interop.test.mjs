import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import * as openpgp from "../public/vendor/openpgp.min.mjs";

const PASSPHRASE = "interop-passphrase-123";

function gpgAvailable() {
  const probe = spawnSync("gpg", ["--version"], { encoding: "utf8" });
  return probe.status === 0;
}

// The Git for Windows gpg build resolves its arguments through MSYS, where a
// Windows drive path such as C:\... is treated as a relative name. Convert to
// the /c/... form when cygpath is available; native Linux/macOS gpg gets the
// path unchanged.
function gpgPath(value) {
  if (process.platform !== "win32") return value;
  const converted = spawnSync("cygpath", ["-u", value], { encoding: "utf8" });
  return converted.status === 0 && converted.stdout.trim() ? converted.stdout.trim() : value;
}

test("browser PGP/MIME ciphertext decrypts and verifies in GnuPG", { skip: !gpgAvailable() && "gpg is not installed" }, async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "a8m-gpg-"));
  try {
    const generated = await openpgp.generateKey({
      type: "ecc",
      curve: "curve25519",
      userIDs: [{ name: "Interop Test", email: "interop@example.test" }],
      passphrase: PASSPHRASE,
      format: "armored"
    });
    const privateKey = path.join(dir, "private.asc");
    const publicKey = path.join(dir, "public.asc");
    const messageFile = path.join(dir, "message.asc");
    const expectedFile = path.join(dir, "expected.txt");
    const decryptedFile = path.join(dir, "decrypted.txt");
    await fs.writeFile(privateKey, generated.privateKey);
    await fs.writeFile(publicKey, generated.publicKey);

    const plaintext = "MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nGnuPG interop marker\r\n";
    const unlocked = await openpgp.decryptKey({
      privateKey: await openpgp.readPrivateKey({ armoredKey: generated.privateKey }),
      passphrase: PASSPHRASE
    });
    const message = await openpgp.createMessage({ binary: new TextEncoder().encode(plaintext) });
    const encrypted = await openpgp.encrypt({
      message,
      encryptionKeys: await openpgp.readKey({ armoredKey: generated.publicKey }),
      signingKeys: unlocked,
      format: "armored"
    });
    await fs.writeFile(messageFile, encrypted);
    await fs.writeFile(expectedFile, plaintext);

    const imported = spawnSync("gpg", ["--homedir", gpgPath(dir), "--batch", "--import", gpgPath(privateKey)], { encoding: "utf8" });
    assert.equal(imported.status, 0, imported.stderr || "gpg import failed");

    const decrypted = spawnSync("gpg", [
      "--homedir", gpgPath(dir), "--batch", "--yes", "--pinentry-mode", "loopback",
      "--passphrase", PASSPHRASE, "--status-fd", "1",
      "--output", gpgPath(decryptedFile), "--decrypt", gpgPath(messageFile)
    ], { encoding: "utf8" });
    assert.equal(decrypted.status, 0, decrypted.stderr || "gpg decrypt failed");
    assert.match(decrypted.stdout, /\[GNUPG:\] GOODSIG /);
    assert.match(decrypted.stdout, /\[GNUPG:\] VALIDSIG /);
    assert.equal(await fs.readFile(decryptedFile, "utf8"), plaintext);
  } finally {
    await fs.rm(dir, { recursive: true, force: true });
  }
});
