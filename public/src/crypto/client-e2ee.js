/* Browser-held OpenPGP identities and client-side MIME protection.
 *
 * Private keys are generated, protected and unlocked in the browser. Only the
 * public key is sent to the server for the tenant directory. The server never
 * receives the passphrase or the unwrapped private key.
 */
import { api } from "../core/api.js";
import { normalizeEmail } from "../core/helpers.js";

const DB_NAME = "a8m-e2ee";
const STORE = "identities";
const DRAFT_STORE = "drafts";
const DIRECTORY_PIN_STORE = "directory-pins";
const SMIME_IDENTITY_STORE = "smime-identities";
const SMIME_PIN_STORE = "smime-directory-pins";
const MAX_ATTACHMENTS = 12;
const MAX_ATTACHMENT_BYTES = 8 * 1024 * 1024;
const MAX_CLIENT_MIME_BYTES = 24 * 1024 * 1024;

let dbPromise = null;
let openpgpPromise = null;
let unlockedPrivateKey = null;
let unlockedEmail = "";

// OpenPGP.js is ~700 KB. Load it only when client-side mode actually needs a
// key operation so the normal webmail boot is not delayed for every tenant.
function openpgpLib() {
  if (!openpgpPromise) {
    openpgpPromise = import("../../vendor/openpgp.min.mjs").catch((error) => {
      openpgpPromise = null;
      throw error;
    });
  }
  return openpgpPromise;
}

function openDB() {
  if (dbPromise) return dbPromise;
  dbPromise = new Promise((resolve, reject) => {
    if (!("indexedDB" in window)) {
      reject(new Error("This browser does not provide IndexedDB"));
      return;
    }
    const request = indexedDB.open(DB_NAME, 4);
    request.onupgradeneeded = () => {
      const db = request.result;
      if (!db.objectStoreNames.contains(STORE)) db.createObjectStore(STORE, { keyPath: "email" });
      if (!db.objectStoreNames.contains(DRAFT_STORE)) db.createObjectStore(DRAFT_STORE, { keyPath: "id" });
      if (!db.objectStoreNames.contains(DIRECTORY_PIN_STORE)) db.createObjectStore(DIRECTORY_PIN_STORE, { keyPath: "email" });
      if (!db.objectStoreNames.contains(SMIME_IDENTITY_STORE)) db.createObjectStore(SMIME_IDENTITY_STORE, { keyPath: "email" });
      if (!db.objectStoreNames.contains(SMIME_PIN_STORE)) db.createObjectStore(SMIME_PIN_STORE, { keyPath: "email" });
    };
    request.onsuccess = () => {
      const db = request.result;
      // Allow another tab to upgrade the schema instead of blocking forever.
      db.onversionchange = () => db.close();
      resolve(db);
    };
    request.onerror = () => reject(request.error || new Error("Could not open the local key store"));
  }).catch((error) => {
    dbPromise = null;
    throw error;
  });
  return dbPromise;
}

export function openClientDB() {
  return openDB();
}

function idbRequest(request) {
  return new Promise((resolve, reject) => {
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error || new Error("Local key store request failed"));
  });
}

async function readRecord(email) {
  const db = await openDB();
  const tx = db.transaction(STORE, "readonly");
  return idbRequest(tx.objectStore(STORE).get(normalizeEmail(email)));
}

async function writeRecord(record) {
  const db = await openDB();
  const tx = db.transaction(STORE, "readwrite");
  await idbRequest(tx.objectStore(STORE).put(record));
}

async function removeRecord(email) {
  const db = await openDB();
  const tx = db.transaction(STORE, "readwrite");
  await idbRequest(tx.objectStore(STORE).delete(normalizeEmail(email)));
}

async function readDraftRecords(email) {
  const db = await openDB();
  const tx = db.transaction(DRAFT_STORE, "readonly");
  const records = await idbRequest(tx.objectStore(DRAFT_STORE).getAll());
  return (records || []).filter((record) => record.email === normalizeEmail(email));
}

async function writeDraftRecord(record) {
  const db = await openDB();
  const tx = db.transaction(DRAFT_STORE, "readwrite");
  await idbRequest(tx.objectStore(DRAFT_STORE).put(record));
}

export async function saveClientDraft(email, id, payload) {
  const openpgp = await openpgpLib();
  if (!unlockedPrivateKey) throw new Error("Unlock your browser encryption key to save an encrypted draft");
  const record = await readRecord(email);
  if (!record?.publicKey) throw new Error("No browser key is stored for this address");
  if (!id) throw new Error("The local draft has no identifier");
  const serialized = JSON.stringify(payload || {});
  if (serialized.length > 20 * 1024 * 1024) throw new Error("This draft is too large to store locally");
  const armored = await openpgp.encrypt({
    message: await openpgp.createMessage({ text: serialized }),
    encryptionKeys: await openpgp.readKey({ armoredKey: record.publicKey }),
    format: "armored"
  });
  const updatedAt = new Date().toISOString();
  await writeDraftRecord({ id, email: normalizeEmail(email), updatedAt, ciphertext: armored });
  const records = (await readDraftRecords(email)).sort((left, right) => String(right.updatedAt || "").localeCompare(String(left.updatedAt || "")));
  for (const stale of records.slice(25)) {
    const db = await openDB();
    const tx = db.transaction(DRAFT_STORE, "readwrite");
    await idbRequest(tx.objectStore(DRAFT_STORE).delete(stale.id));
  }
  return { id, email: normalizeEmail(email), updatedAt };
}

export async function listClientDrafts(email) {
  const openpgp = await openpgpLib();
  if (!unlockedPrivateKey) {
    const error = new Error("Unlock your browser key to open encrypted drafts");
    error.code = "CLIENT_KEY_LOCKED";
    throw error;
  }
  const records = await readDraftRecords(email);
  const drafts = [];
  for (const record of records) {
    try {
      const decrypted = await openpgp.decrypt({
        message: await openpgp.readMessage({ armoredMessage: record.ciphertext }),
        decryptionKeys: unlockedPrivateKey,
        format: "utf8"
      });
      const payload = JSON.parse(decrypted.data);
      drafts.push({ id: record.id, updatedAt: record.updatedAt, ...payload, localDraft: true });
    } catch {
      // A draft encrypted to a different or damaged key must not block others.
    }
  }
  return drafts.sort((left, right) => String(right.updatedAt || "").localeCompare(String(left.updatedAt || "")));
}

export async function deleteClientDraft(id) {
  if (!id) return;
  const db = await openDB();
  const tx = db.transaction(DRAFT_STORE, "readwrite");
  await idbRequest(tx.objectStore(DRAFT_STORE).delete(id));
}

async function readDirectoryPin(email) {
  const db = await openDB();
  const tx = db.transaction(DIRECTORY_PIN_STORE, "readonly");
  return idbRequest(tx.objectStore(DIRECTORY_PIN_STORE).get(normalizeEmail(email)));
}

export async function directoryKeyPin(email) {
  return readDirectoryPin(email);
}

export async function pinDirectoryKey(email, fingerprint) {
  const normalized = normalizeEmail(email);
  const value = String(fingerprint || "").toUpperCase();
  if (!normalized || !value) throw new Error("A directory key and fingerprint are required");
  const db = await openDB();
  const tx = db.transaction(DIRECTORY_PIN_STORE, "readwrite");
  await idbRequest(tx.objectStore(DIRECTORY_PIN_STORE).put({
    email: normalized, fingerprint: value, updatedAt: new Date().toISOString()
  }));
  return value;
}

function fingerprintOf(key) {
  return String(key.getFingerprint()).toUpperCase();
}

export function clientAttachmentLimits() {
  return { count: MAX_ATTACHMENTS, totalBytes: MAX_ATTACHMENT_BYTES, perFileBytes: MAX_ATTACHMENT_BYTES };
}

function bytesToBase64(bytes) {
  let binary = "";
  for (let offset = 0; offset < bytes.length; offset += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000));
  }
  return btoa(binary);
}

function base64ToBytes(value) {
  const clean = String(value || "").replace(/\s+/g, "");
  const binary = atob(clean);
  return Uint8Array.from(binary, (char) => char.charCodeAt(0));
}

function keyOwnsEmail(key, email) {
  const wanted = normalizeEmail(email);
  if (!wanted) return false;
  return (key.getUserIDs ? key.getUserIDs() : []).some((userID) =>
    (String(userID).match(/[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}/gi) || [])
      .some((address) => normalizeEmail(address) === wanted));
}

async function usableClientPublicKey(armored) {
  try {
    const openpgp = await openpgpLib();
    const key = await openpgp.readKey({ armoredKey: String(armored || "") });
    if (await key.isRevoked()) return null;
    const expiry = await key.getExpirationTime();
    if (expiry instanceof Date && expiry.getTime() <= Date.now()) return null;
    const encryptionKey = await key.getEncryptionKey();
    if (await encryptionKey.isRevoked()) return null;
    const encryptionExpiry = await encryptionKey.getExpirationTime();
    if (encryptionExpiry instanceof Date && encryptionExpiry.getTime() <= Date.now()) return null;
    return key;
  } catch {
    return null;
  }
}

export function isClientE2EESupported() {
  return typeof indexedDB !== "undefined" && typeof globalThis.crypto?.subtle !== "undefined";
}

export function getUnlockedClientKey() {
  return unlockedPrivateKey;
}

export function getUnlockedClientEmail() {
  return unlockedEmail;
}

export function lockClientIdentity() {
  unlockedPrivateKey = null;
  unlockedEmail = "";
}

export async function loadClientIdentity(email) {
  if (!email) return null;
  const record = await readRecord(email);
  if (!record) return null;
  return {
    email: record.email,
    emails: Array.isArray(record.emails) && record.emails.length ? record.emails : [record.email],
    publicKey: record.publicKey,
    fingerprint: record.fingerprint,
    createdAt: record.createdAt,
    hasPrivateKey: Boolean(record.privateKey)
  };
}

export async function generateClientIdentity({ email, name, passphrase, emails = [] }) {
  const openpgp = await openpgpLib();
  const normalized = normalizeEmail(email);
  if (!normalized) throw new Error("Choose a valid email identity");
  if (String(passphrase || "").length < 12) throw new Error("Use a passphrase of at least 12 characters");
  const allEmails = [...new Set([normalized, ...(emails || []).map(normalizeEmail).filter(Boolean)])];
  const generated = await openpgp.generateKey({
    type: "ecc",
    curve: "curve25519",
    userIDs: allEmails.map((address) => ({ name: normalizeEmail(address) === normalized ? (name || normalized) : address, email: address })),
    passphrase,
    format: "armored"
  });
  const publicKey = generated.publicKey;
  const key = await openpgp.readKey({ armoredKey: publicKey });
  const fingerprint = fingerprintOf(key);
  const record = {
    email: normalized,
    emails: allEmails,
    publicKey,
    privateKey: generated.privateKey,
    revocationCertificate: generated.revocationCertificate || "",
    fingerprint,
    createdAt: new Date().toISOString()
  };
  await writeRecord(record);
  lockedKeyReset();
  return record;
}

export async function importClientIdentity({ email, privateKeyArmored, passphrase, emails = [] }) {
  const openpgp = await openpgpLib();
  const normalized = normalizeEmail(email);
  if (!normalized) throw new Error("Choose a valid email identity");
  if (String(passphrase || "").length < 12) throw new Error("Use a passphrase of at least 12 characters");
  let key = await openpgp.readPrivateKey({ armoredKey: String(privateKeyArmored || "").trim() });
  if (key.isDecrypted()) {
    key = await openpgp.encryptKey({ privateKey: key, passphrase });
  } else {
    // Confirm the supplied passphrase before storing the existing encrypted key.
    await openpgp.decryptKey({ privateKey: key, passphrase });
  }
  const publicKey = key.toPublic().armor();
  const publicParsed = await openpgp.readKey({ armoredKey: publicKey });
  if (!keyOwnsEmail(publicParsed, normalized)) throw new Error("The private key does not contain this email identity");
  const ownedEmails = [...new Set([normalized, ...(emails || []).map(normalizeEmail).filter((address) => address && keyOwnsEmail(publicParsed, address))])];
  const record = {
    email: normalized,
    emails: ownedEmails,
    publicKey,
    privateKey: key.armor(),
    revocationCertificate: "",
    fingerprint: fingerprintOf(publicParsed),
    createdAt: new Date().toISOString()
  };
  await writeRecord(record);
  lockedKeyReset();
  return record;
}

function lockedKeyReset() {
  unlockedPrivateKey = null;
  unlockedEmail = "";
}

export async function unlockClientIdentity(email, passphrase) {
  const openpgp = await openpgpLib();
  const record = await readRecord(email);
  if (!record || !record.privateKey) throw new Error("No browser key is stored for this address");
  let key = await openpgp.readPrivateKey({ armoredKey: record.privateKey });
  if (!key.isDecrypted()) key = await openpgp.decryptKey({ privateKey: key, passphrase });
  unlockedPrivateKey = key;
  unlockedEmail = record.email;
  return {
    email: record.email,
    publicKey: record.publicKey,
    fingerprint: record.fingerprint,
    createdAt: record.createdAt
  };
}

export async function exportClientPrivateKey(email) {
  const record = await readRecord(email);
  if (!record) throw new Error("No browser key is stored for this address");
  return record.privateKey;
}

export async function deleteClientIdentity(email) {
  await removeRecord(email);
  if (normalizeEmail(email) === unlockedEmail) lockedKeyReset();
}

export async function publishClientIdentity(email, publicKey) {
  const data = await api("/api/encryption/client-identity", {
    method: "POST",
    body: { email: normalizeEmail(email), publicKey }
  });
  return data?.identity || null;
}

export async function publishStoredClientIdentity(email) {
  const record = await readRecord(email);
  if (!record) throw new Error("No browser key is stored for this address");
  const addresses = Array.isArray(record.emails) && record.emails.length ? record.emails : [record.email];
  let identity = null;
  for (const address of addresses) {
    identity = await publishClientIdentity(address, record.publicKey);
  }
  return identity;
}

export async function saveClientKeyBackup(email) {
  const record = await readRecord(email);
  if (!record) throw new Error("No browser key is stored for this address");
  const data = await api("/api/encryption/key-backup", {
    method: "POST",
    body: {
      email: record.email,
      fingerprint: record.fingerprint,
      publicKey: record.publicKey,
      encryptedPrivateKey: record.privateKey
    }
  });
  return data?.updatedAt || "";
}

export async function loadClientKeyBackup() {
  try {
    const data = await api("/api/encryption/key-backup");
    return data?.backup || null;
  } catch (error) {
    if (error.status === 404) return null;
    throw error;
  }
}

export async function deleteClientKeyBackup() {
  await api("/api/encryption/key-backup", { method: "DELETE", body: {} });
}

export async function resolveClientRecipientKeys(emails) {
  const unique = [...new Set((emails || []).map(normalizeEmail).filter(Boolean))];
  if (!unique.length) return new Map();
  const query = encodeURIComponent(unique.join(","));
  const [directory, contacts] = await Promise.all([
    api(`/api/encryption/directory?protocol=openpgp&emails=${query}`).catch(() => ({ directory: {} })),
    api(`/api/encryption/recipients?protocol=openpgp&emails=${query}`).catch(() => ({ recipients: {} }))
  ]);
  const result = new Map();
  const now = Date.now();
  for (const email of unique) {
    const directoryKeys = directory?.directory?.[email];
    let selected = null;
    if (Array.isArray(directoryKeys)) {
      for (const candidate of directoryKeys) {
        const parsed = await usableClientPublicKey(candidate.publicKey || candidate.armored);
        if (!parsed || !keyOwnsEmail(parsed, email)) continue;
        const fingerprint = fingerprintOf(parsed);
        const pin = await readDirectoryPin(email);
        if (pin?.fingerprint && pin.fingerprint !== fingerprint) {
          const error = new Error("The organization directory key for " + email + " changed");
          error.code = "DIRECTORY_KEY_CHANGED";
          error.recipients = [email];
          error.changes = [{ email, previousFingerprint: pin.fingerprint, fingerprint }];
          throw error;
        }
        if (!pin?.fingerprint) await pinDirectoryKey(email, fingerprint);
        selected = { ...candidate, key: parsed, fingerprint, directoryPinned: Boolean(pin?.fingerprint) };
        break;
      }
    }
    if (!selected) {
      const contactKeys = (contacts?.recipients?.[email] || []).filter((key) => {
        if (key.revokedAt || key.trust === "revoked") return false;
        if (key.notBefore && Date.parse(key.notBefore) > now) return false;
        if (key.notAfter && Date.parse(key.notAfter) <= now) return false;
        if (key.keyChangedAt && !["verified", "enterprise"].includes(key.trust)) return false;
        return true;
      });
      for (const candidate of contactKeys) {
        const parsed = await usableClientPublicKey(candidate.publicKey || candidate.armored);
        if (!parsed || !keyOwnsEmail(parsed, email)) continue;
        selected = { ...candidate, key: parsed };
        break;
      }
    }
    if (selected) result.set(email, selected);
  }
  return result;
}

function mimeParameter(value, fallback) {
  const text = String(value || fallback || "attachment").replace(/[\r\n"]/g, "'").slice(0, 200);
  return `"${text}"`;
}

function mimeFilenameParameter(value, fallback) {
  const text = String(value || fallback || "attachment").replace(/[\r\n"]/g, "'").slice(0, 200);
  if (/^[\x20-\x7e]+$/.test(text)) return `filename="${text}"`;
  return `filename*=UTF-8''${encodeURIComponent(text)}`;
}

function mimeType(value) {
  const type = String(value || "application/octet-stream").trim().toLowerCase();
  return /^[a-z0-9!#$&^_.+-]+\/[a-z0-9!#$&^_.+-]+$/.test(type) ? type : "application/octet-stream";
}

function base64MIMELines(value) {
  const clean = String(value || "").replace(/\s+/g, "");
  if (!clean || !/^[A-Za-z0-9+/]*={0,2}$/.test(clean)) throw new Error("An attachment has invalid base64 data");
  const lines = [];
  for (let offset = 0; offset < clean.length; offset += 76) lines.push(clean.slice(offset, offset + 76));
  return lines.join("\r\n");
}

function attachmentBytes(attachments) {
  return (attachments || []).reduce((total, attachment) => {
    const clean = String(attachment?.dataBase64 || "").replace(/\s+/g, "");
    return total + Math.floor((clean.length * 3) / 4);
  }, 0);
}

function validateAttachments(attachments) {
  const list = Array.isArray(attachments) ? attachments : [];
  if (list.length > MAX_ATTACHMENTS) throw new Error(`Attach at most ${MAX_ATTACHMENTS} files`);
  const total = attachmentBytes(list);
  if (total > MAX_ATTACHMENT_BYTES) throw new Error(`Encrypted attachments are limited to ${Math.round(MAX_ATTACHMENT_BYTES / (1024 * 1024))} MB total`);
  for (const attachment of list) {
    if (!String(attachment?.name || "").trim()) throw new Error("Every attachment needs a filename");
    if (String(attachment?.dataBase64 || "").replace(/\s+/g, "").length === 0) throw new Error("An attachment is empty");
  }
  return list;
}

function alternativeMIME(boundary, text, html) {
  return `Content-Type: multipart/alternative; boundary="${boundary}"\r\n\r\n--${boundary}\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n${text}\r\n--${boundary}\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n${html}\r\n--${boundary}--\r\n`;
}

export function buildPlaintextMIME({ bodyText, bodyHtml, attachments = [] }) {
  const text = String(bodyText || "").replace(/\r?\n/g, "\r\n");
  const html = String(bodyHtml || "");
  const files = validateAttachments(attachments);
  if (!files.length) {
    if (!html) {
      return `MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n${text}\r\n`;
    }
    const boundary = `a8m-client-${crypto.randomUUID().replace(/-/g, "")}`;
    return `MIME-Version: 1.0\r\n${alternativeMIME(boundary, text, html)}`;
  }
  const altBoundary = `a8m-alt-${crypto.randomUUID().replace(/-/g, "")}`;
  const mixedBoundary = `a8m-mix-${crypto.randomUUID().replace(/-/g, "")}`;
  const firstPart = html
    ? alternativeMIME(altBoundary, text, html)
    : `Content-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n${text}\r\n`;
  const parts = [`--${mixedBoundary}\r\n${firstPart}`];
  for (const attachment of files) {
    const type = mimeType(attachment.mime);
    parts.push(`--${mixedBoundary}\r\nContent-Type: ${type}; name=${mimeParameter(attachment.name)}\r\nContent-Disposition: attachment; ${mimeFilenameParameter(attachment.name)}\r\nContent-Transfer-Encoding: base64\r\n\r\n${base64MIMELines(attachment.dataBase64)}\r\n`);
  }
  return `MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary="${mixedBoundary}"\r\n\r\n${parts.join("")}--${mixedBoundary}--\r\n`;
}

function wrapPGPMIME(armored) {
  const boundary = `a8m-enc-${crypto.randomUUID().replace(/-/g, "")}`;
  const armoredCRLF = String(armored || "").replace(/\r?\n/g, "\r\n").trimEnd();
  return `MIME-Version: 1.0\r\nContent-Type: multipart/encrypted; protocol="application/pgp-encrypted"; boundary="${boundary}"\r\n\r\n--${boundary}\r\nContent-Type: application/pgp-encrypted\r\n\r\nVersion: 1\r\n--${boundary}\r\nContent-Type: application/octet-stream; name="encrypted.asc"\r\nContent-Disposition: inline; filename="encrypted.asc"\r\nContent-Transfer-Encoding: 7bit\r\n\r\n${armoredCRLF}\r\n--${boundary}--\r\n`;
}

export async function encryptClientMIME({ bodyText, bodyHtml, recipientEmails, sign, attachments = [] }) {
  if (!unlockedPrivateKey) throw new Error("Unlock your browser encryption key first");
  const openpgp = await openpgpLib();
  const keys = await resolveClientRecipientKeys(recipientEmails);
  const missing = (recipientEmails || []).map(normalizeEmail).filter((email) => email && !keys.has(email));
  if (missing.length) {
    const error = new Error(`No public key is available for ${[...new Set(missing)].join(", ")}`);
    error.code = "MISSING_RECIPIENT_KEY";
    error.recipients = [...new Set(missing)];
    throw error;
  }
  const encryptionKeys = [];
  const seenKeys = new Set();
  const addEncryptionKey = (key) => {
    const fingerprint = fingerprintOf(key);
    if (seenKeys.has(fingerprint)) return;
    seenKeys.add(fingerprint);
    encryptionKeys.push(key);
  };
  for (const item of keys.values()) {
    const parsed = item.key || await usableClientPublicKey(item.publicKey || item.armored);
    if (!parsed) continue;
    addEncryptionKey(parsed);
  }
  if (!encryptionKeys.length) {
    const error = new Error(`No usable public key is available for ${[...new Set(missing)].join(", ")}`);
    error.code = "MISSING_RECIPIENT_KEY";
    error.recipients = [...new Set(missing)];
    throw error;
  }
  // Keep the Sent copy readable by the sender without asking the server to
  // hold a second copy of the plaintext.
  const selfRecord = await readRecord(unlockedEmail);
  if (selfRecord?.publicKey) {
    const selfKey = await usableClientPublicKey(selfRecord.publicKey);
    if (selfKey && keyOwnsEmail(selfKey, unlockedEmail)) addEncryptionKey(selfKey);
  }
  const plaintext = buildPlaintextMIME({ bodyText, bodyHtml, attachments });
  const message = await openpgp.createMessage({ binary: new TextEncoder().encode(plaintext) });
  const armored = await openpgp.encrypt({
    message,
    encryptionKeys,
    signingKeys: sign ? unlockedPrivateKey : undefined,
    format: "armored"
  });
  const wrapped = wrapPGPMIME(armored);
  if (wrapped.length > MAX_CLIENT_MIME_BYTES) {
    const error = new Error("The encrypted message is too large");
    error.code = "CLIENT_MIME_TOO_LARGE";
    throw error;
  }
  return wrapped;
}

function splitHeaderBody(text) {
  const normalized = text.replace(/\r\n/g, "\n");
  const index = normalized.indexOf("\n\n");
  if (index < 0) return { headers: "", body: normalized };
  return { headers: normalized.slice(0, index), body: normalized.slice(index + 2) };
}

function parseHeaders(raw) {
  const headers = {};
  const unfolded = String(raw || "").replace(/\n[ \t]+/g, " ");
  for (const line of unfolded.split("\n")) {
    const index = line.indexOf(":");
    if (index <= 0) continue;
    headers[line.slice(0, index).trim().toLowerCase()] = line.slice(index + 1).trim();
  }
  return headers;
}

function contentTypeInfo(value) {
  const parts = String(value || "text/plain").split(";");
  const params = {};
  for (const part of parts.slice(1)) {
    const index = part.indexOf("=");
    if (index <= 0) continue;
    params[part.slice(0, index).trim().toLowerCase()] = part.slice(index + 1).trim().replace(/^"|"$/g, "");
  }
  return { mediaType: parts[0].trim().toLowerCase(), params };
}

function splitMultipart(body, boundary) {
  const lines = String(body || "").replace(/\r\n/g, "\n").split("\n");
  const delimiter = `--${boundary}`;
  const segments = [];
  let current = null;
  for (const line of lines) {
    if (line === delimiter || line === `${delimiter}--`) {
      if (current) segments.push(current.join("\n"));
      if (line === `${delimiter}--`) break;
      current = [];
      continue;
    }
    if (current) current.push(line);
  }
  if (current) segments.push(current.join("\n"));
  return segments;
}

function decodeTransferBytes(body, encoding) {
  const value = String(body || "");
  const normalized = String(encoding || "").toLowerCase();
  if (normalized === "base64") {
    try {
      return base64ToBytes(value);
    } catch {
      return new TextEncoder().encode(value);
    }
  }
  if (normalized === "quoted-printable") {
    const decoded = value
      .replace(/=\r?\n/g, "")
      .replace(/=([0-9A-Fa-f]{2})/g, (_, hex) => String.fromCharCode(parseInt(hex, 16)));
    return new Uint8Array([...decoded].map((char) => char.charCodeAt(0) & 0xff));
  }
  return new TextEncoder().encode(value);
}

function decodeTransfer(body, encoding) {
  return new TextDecoder("utf-8", { fatal: false }).decode(decodeTransferBytes(body, encoding));
}

function dispositionFilename(disposition, params = {}) {
  const extended = /filename\*\s*=\s*(?:UTF-8|utf-8)''([^;]+)/i.exec(disposition);
  if (extended) {
    try { return decodeURIComponent(extended[1].trim()); } catch {}
  }
  const plain = /filename\s*=\s*"?([^";]+)"?/i.exec(disposition);
  if (plain) return plain[1].trim();
  return String(params.name || "").replace(/^"|"$/g, "").trim();
}

export function parseClientMIME(text) {
  const result = { bodyText: "", bodyHtml: "", attachments: [], attachmentsTruncated: false };
  let parts = 0;
  let attachmentBytes = 0;
  const visit = (entity, depth = 0) => {
    if (depth > 20 || ++parts > 500) return;
    const { headers, body } = splitHeaderBody(entity);
    const parsedHeaders = parseHeaders(headers);
    const { mediaType, params } = contentTypeInfo(parsedHeaders["content-type"]);
    if (mediaType.startsWith("multipart/")) {
      if (!params.boundary) return;
      for (const segment of splitMultipart(body, params.boundary)) visit(segment, depth + 1);
      return;
    }
    const disposition = parsedHeaders["content-disposition"] || "";
    const filename = dispositionFilename(disposition, params);
    const isTextPart = mediaType === "text/plain" || mediaType === "text/html";
    if (/attachment/i.test(disposition) || (filename && !isTextPart)) {
      if (result.attachments.length >= 20) {
        result.attachmentsTruncated = true;
        return;
      }
      try {
        const data = decodeTransferBytes(body, parsedHeaders["content-transfer-encoding"] || "");
        if (attachmentBytes + data.length > 24 * 1024 * 1024) {
          result.attachmentsTruncated = true;
          return;
        }
        attachmentBytes += data.length;
        result.attachments.push({
          name: filename || `attachment-${result.attachments.length + 1}`,
          mime: mediaType || "application/octet-stream",
          size: data.length,
          data
        });
      } catch {
        result.attachmentsTruncated = true;
      }
      return;
    }
    if (mediaType === "text/plain" && !result.bodyText) {
      result.bodyText = decodeTransfer(body, parsedHeaders["content-transfer-encoding"] || "").trim().slice(0, 2_000_000);
    } else if (mediaType === "text/html" && !result.bodyHtml) {
      result.bodyHtml = decodeTransfer(body, parsedHeaders["content-transfer-encoding"] || "").trim().slice(0, 2_000_000);
    }
  };
  visit(text);
  if (!result.bodyText && !result.bodyHtml) {
    const body = splitHeaderBody(text).body.trim().slice(0, 2_000_000);
    if (body.startsWith("<")) result.bodyHtml = body;
    else result.bodyText = body;
  }
  return result;
}

function extractArmoredMessage(raw) {
  const text = String(raw || "");
  const match = text.match(/-----BEGIN PGP MESSAGE-----[\s\S]*?-----END PGP MESSAGE-----/);
  return match ? match[0] : "";
}

export async function decryptClientMessage(raw, senderEmail) {
  if (!unlockedPrivateKey) throw new Error("Unlock your browser encryption key first");
  const openpgp = await openpgpLib();
  const armored = extractArmoredMessage(raw);
  if (!armored) throw new Error("No OpenPGP ciphertext was found in this message");
  const message = await openpgp.readMessage({ armoredMessage: armored });
  let verificationKeys = null;
  let senderTrust = "";
  try {
    const keys = await resolveClientRecipientKeys([senderEmail]);
    const senderKey = keys.get(normalizeEmail(senderEmail));
    if (senderKey?.publicKey) {
      verificationKeys = await openpgp.readKey({ armoredKey: senderKey.publicKey });
      senderTrust = senderKey.trust || "";
    }
  } catch {}
  const decrypted = await openpgp.decrypt({
    message,
    decryptionKeys: unlockedPrivateKey,
    verificationKeys: verificationKeys || undefined,
    format: "binary"
  });
  const bytes = decrypted.data instanceof Uint8Array ? decrypted.data : new Uint8Array(decrypted.data);
  const plaintext = new TextDecoder("utf-8", { fatal: false }).decode(bytes);
  const mime = parseClientMIME(plaintext);
  let signature = { state: "none" };
  if (Array.isArray(decrypted.signatures) && decrypted.signatures.length) {
    const trusted = ["verified", "enterprise"].includes(senderTrust);
    signature = { state: verificationKeys ? (trusted ? "valid" : "valid_untrusted") : "unknown", signerEmail: senderEmail };
    if (verificationKeys) {
      try {
        const verified = await decrypted.signatures[0].verified;
        signature.keyId = verified?.keyID?.toHex?.() || decrypted.signatures[0].keyID?.toHex?.() || "";
      } catch (error) {
        signature = { state: "invalid", reason: error?.message || "The signature could not be verified" };
      }
    } else {
      signature.reason = "The sender's public key is not available in this mailbox";
    }
  }
  return { ...mime, signature, warnings: [] };
}
