/* Browser-held S/MIME identities and CMS protection.
 *
 * The PKCS#12 bundle, certificate chain and passphrase stay in this browser.
 * The server receives only the public certificate chain for the tenant
 * directory. Signing, encryption, decryption and signature verification run
 * locally through the vendored node-forge build.
 */
import { api } from "../core/api.js";
import { normalizeEmail } from "../core/helpers.js";
import { buildPlaintextMIME, openClientDB, parseClientMIME } from "./client-e2ee.js";

const IDENTITY_STORE = "smime-identities";
const PIN_STORE = "smime-directory-pins";
const DRAFT_STORE = "drafts";
const MAX_CLIENT_MIME_BYTES = 24 * 1024 * 1024;
const MAX_P12_BYTES = 8 * 1024 * 1024;

let forgePromise = null;
let unlocked = null;

function forgeLib() {
  if (!forgePromise) {
    forgePromise = import("../../vendor/forge.min.mjs").then((module) => module.default).catch((error) => {
      forgePromise = null;
      throw error;
    });
  }
  return forgePromise;
}

function idbRequest(request) {
  return new Promise((resolve, reject) => {
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error || new Error("The browser S/MIME store could not be read"));
  });
}

async function readRecord(email) {
  const db = await openClientDB();
  const tx = db.transaction(IDENTITY_STORE, "readonly");
  return (await idbRequest(tx.objectStore(IDENTITY_STORE).get(normalizeEmail(email)))) || null;
}

async function writeRecord(record) {
  const db = await openClientDB();
  const tx = db.transaction(IDENTITY_STORE, "readwrite");
  await idbRequest(tx.objectStore(IDENTITY_STORE).put(record));
}

function publicMetadata(record) {
  if (!record) return null;
  return {
    email: record.email,
    emails: Array.isArray(record.emails) && record.emails.length ? record.emails : [record.email],
    fingerprint: record.fingerprint,
    subject: record.subject || "",
    issuer: record.issuer || "",
    notBefore: record.notBefore || "",
    notAfter: record.notAfter || "",
    certificatePEM: record.certificatePEM || "",
    chainPEM: record.chainPEM || "",
    createdAt: record.createdAt || "",
    hasPrivateKey: Boolean(record.p12)
  };
}

export function isClientSMIMESupported() {
  return typeof indexedDB !== "undefined";
}

export async function loadSMIMEIdentity(email) {
  return publicMetadata(await readRecord(email));
}

export async function deleteSMIMEIdentity(email) {
  const db = await openClientDB();
  const tx = db.transaction(IDENTITY_STORE, "readwrite");
  await idbRequest(tx.objectStore(IDENTITY_STORE).delete(normalizeEmail(email)));
  if (unlocked && unlocked.email === normalizeEmail(email)) unlocked = null;
}

/* ------------------------------------------------------------------ */
/* Certificate helpers                                                 */
/* ------------------------------------------------------------------ */

function certificatePEM(certificate, forge) {
  return forge.pki.certificateToPem(certificate);
}

function chainPEM(certificates, forge) {
  return certificates.map((certificate) => certificatePEM(certificate, forge)).join("");
}

function parsePEMCertificates(text, forge) {
  const out = [];
  const pattern = /-----BEGIN CERTIFICATE-----[\s\S]+?-----END CERTIFICATE-----/g;
  for (const match of String(text || "").match(pattern) || []) {
    try {
      out.push(forge.pki.certificateFromPem(match));
    } catch {
      // Ignore malformed or unrelated PEM blocks.
    }
  }
  return out;
}

function certificateDer(certificate, forge) {
  return forge.asn1.toDer(forge.pki.certificateToAsn1(certificate)).getBytes();
}

function certificateFingerprint(certificate, forge) {
  return forge.md.sha256.create().update(certificateDer(certificate, forge), "raw").digest().toHex().toUpperCase();
}

function certificateEmails(certificate) {
  const extension = certificate?.getExtension?.("subjectAltName");
  const out = [];
  const seen = new Set();
  for (const name of extension?.altNames || []) {
    const value = String(name?.value || "").trim().toLowerCase();
    if ((name?.type === 1 || value.includes("@")) && value && !seen.has(value)) {
      seen.add(value);
      out.push(value);
    }
  }
  return out;
}

function certificateCovers(certificate, email) {
  return certificateEmails(certificate).includes(normalizeEmail(email));
}

function certificateValidNow(certificate, now = new Date()) {
  return Boolean(certificate?.validity?.notBefore && certificate?.validity?.notAfter &&
    now >= certificate.validity.notBefore && now <= certificate.validity.notAfter);
}

function rsaPublicKey(certificate) {
  return Boolean(certificate?.publicKey?.n && certificate?.publicKey?.e !== undefined);
}

function keyUsageAllows(certificate, usage) {
  const extension = certificate?.getExtension?.("keyUsage");
  if (!extension) return true;
  return usage === "sign"
    ? Boolean(extension.digitalSignature || extension.nonRepudiation)
    : Boolean(extension.keyEncipherment || extension.keyAgreement);
}

function extendedUsageAllows(certificate) {
  const extension = certificate?.getExtension?.("extKeyUsage");
  if (!extension) return true;
  return Boolean(extension.emailProtection || extension.anyExtendedKeyUsage);
}

function encryptionProblem(certificate, email = "") {
  if (!certificate) return "The certificate is missing";
  if (certificate?.getExtension?.("basicConstraints")?.cA) return "The certificate is a CA certificate, not a user identity";
  if (!certificateValidNow(certificate)) return "The certificate is outside its validity period";
  if (email && !certificateCovers(certificate, email)) return `The certificate does not cover ${email}`;
  if (!rsaPublicKey(certificate)) return "S/MIME encryption requires an RSA certificate";
  if (!extendedUsageAllows(certificate)) return "The certificate lacks the emailProtection extended key usage";
  if (!keyUsageAllows(certificate, "encrypt")) return "The certificate key usage does not allow encryption";
  return "";
}

function signingProblem(certificate) {
  if (!certificate) return "The certificate is missing";
  if (!certificateValidNow(certificate)) return "The certificate is outside its validity period";
  if (!extendedUsageAllows(certificate)) return "The certificate lacks the emailProtection extended key usage";
  if (!keyUsageAllows(certificate, "sign")) return "The certificate key usage does not allow signing";
  return "";
}

function publicKeyMatches(certificate, privateKey) {
  return Boolean(certificate?.publicKey?.n && privateKey?.n &&
    String(certificate.publicKey.n) === String(privateKey.n) &&
    String(certificate.publicKey.e) === String(privateKey.e));
}

function distinguishedNameDer(attributes, forge) {
  return forge.asn1.toDer(forge.pki.distinguishedNameToAsn1({ attributes })).getBytes();
}

function orderChain(leaf, certificates, forge) {
  const remaining = certificates.filter((certificate) => certificate && certificateFingerprint(certificate, forge) !== certificateFingerprint(leaf, forge));
  const out = [];
  const seen = new Set([certificateFingerprint(leaf, forge)]);
  let current = leaf;
  while (out.length < 16) {
    const issuer = distinguishedNameDer(current.issuer.attributes, forge);
    const next = remaining.find((candidate) => {
      const fingerprint = certificateFingerprint(candidate, forge);
      if (seen.has(fingerprint)) return false;
      return distinguishedNameDer(candidate.subject.attributes, forge) === issuer;
    });
    if (!next) break;
    seen.add(certificateFingerprint(next, forge));
    out.push(next);
    current = next;
  }
  return out;
}

/* ------------------------------------------------------------------ */
/* Identity import, unlock and publication                             */
/* ------------------------------------------------------------------ */

export async function importSMIMEIdentity({ email, p12Base64, passphrase, emails = [] }) {
  const forge = await forgeLib();
  const address = normalizeEmail(email);
  if (!address) throw new Error("Choose the email address covered by this certificate");
  if (!String(passphrase || "")) throw new Error("Choose a passphrase to protect the stored certificate bundle");
  const clean = String(p12Base64 || "").replace(/\s+/g, "");
  if (!clean) throw new Error("Choose a PKCS#12 certificate bundle");
  if (clean.length * 0.75 > MAX_P12_BYTES) throw new Error("The PKCS#12 bundle is too large");
  let p12;
  try {
    p12 = forge.pkcs12.pkcs12FromAsn1(forge.asn1.fromDer(forge.util.decode64(clean)), String(passphrase));
  } catch {
    throw new Error("The PKCS#12 bundle could not be opened with that passphrase");
  }
  const keyBags = [
    ...(p12.getBags({ bagType: forge.pki.oids.pkcs8ShroudedKeyBag })[forge.pki.oids.pkcs8ShroudedKeyBag] || []),
    ...(p12.getBags({ bagType: forge.pki.oids.keyBag })[forge.pki.oids.keyBag] || [])
  ];
  const privateKey = keyBags.map((bag) => bag.key).find(Boolean);
  if (!privateKey) throw new Error("The PKCS#12 bundle contains no private key");
  const certificates = (p12.getBags({ bagType: forge.pki.oids.certBag })[forge.pki.oids.certBag] || [])
    .map((bag) => bag.cert)
    .filter(Boolean);
  const leaf = certificates.find((certificate) => publicKeyMatches(certificate, privateKey));
  if (!leaf) throw new Error("The PKCS#12 bundle does not contain the certificate for its private key");
  const problem = encryptionProblem(leaf, address);
  if (problem) throw new Error(problem);
  const covered = certificateEmails(leaf);
  const owned = [...new Set([address, ...emails.map(normalizeEmail)].filter((candidate) => candidate && covered.includes(candidate)))];
  if (!owned.includes(address)) throw new Error(`The certificate does not cover ${address}`);
  const chain = orderChain(leaf, certificates, forge);
  // Re-encode the bundle with the chosen passphrase so even a password-less
  // export from a certificate store is protected before IndexedDB sees it.
  const encoded = forge.pkcs12.toPkcs12Asn1(privateKey, [leaf, ...chain], String(passphrase), { algorithm: "aes256" });
  const record = {
    email: address,
    emails: owned,
    p12: forge.util.encode64(forge.asn1.toDer(encoded).getBytes()),
    fingerprint: certificateFingerprint(leaf, forge),
    subject: leaf.subject?.attributes?.map((attribute) => `${attribute.shortName || attribute.name}=${attribute.value}`).join(", ") || "",
    issuer: leaf.issuer?.attributes?.map((attribute) => `${attribute.shortName || attribute.name}=${attribute.value}`).join(", ") || "",
    notBefore: leaf.validity.notBefore.toISOString(),
    notAfter: leaf.validity.notAfter.toISOString(),
    certificatePEM: certificatePEM(leaf, forge),
    chainPEM: chainPEM(chain, forge),
    createdAt: new Date().toISOString()
  };
  await writeRecord(record);
  return publicMetadata(record);
}

export async function unlockSMIMEIdentity(email, passphrase) {
  const forge = await forgeLib();
  const address = normalizeEmail(email);
  const record = await readRecord(address);
  if (!record?.p12) throw new Error("No browser S/MIME certificate is stored for this address");
  let p12;
  try {
    p12 = forge.pkcs12.pkcs12FromAsn1(forge.asn1.fromDer(forge.util.decode64(record.p12)), String(passphrase || ""));
  } catch {
    throw new Error("The passphrase did not unlock this S/MIME certificate");
  }
  const keyBags = [
    ...(p12.getBags({ bagType: forge.pki.oids.pkcs8ShroudedKeyBag })[forge.pki.oids.pkcs8ShroudedKeyBag] || []),
    ...(p12.getBags({ bagType: forge.pki.oids.keyBag })[forge.pki.oids.keyBag] || [])
  ];
  const privateKey = keyBags.map((bag) => bag.key).find(Boolean);
  const certificates = (p12.getBags({ bagType: forge.pki.oids.certBag })[forge.pki.oids.certBag] || [])
    .map((bag) => bag.cert)
    .filter(Boolean);
  const leaf = certificates.find((certificate) => publicKeyMatches(certificate, privateKey));
  if (!privateKey || !leaf) throw new Error("The stored S/MIME bundle is damaged");
  if (certificateFingerprint(leaf, forge) !== record.fingerprint) throw new Error("The stored S/MIME certificate no longer matches its fingerprint");
  const problem = encryptionProblem(leaf, address);
  if (problem) throw new Error(problem);
  unlocked = {
    email: address,
    privateKey,
    certificate: leaf,
    chain: orderChain(leaf, certificates, forge)
  };
  return publicMetadata(record);
}

export function lockSMIMEIdentity() {
  unlocked = null;
}

export function getUnlockedSMIMEIdentity() {
  return unlocked;
}

export async function publishStoredSMIMEIdentity(email) {
  const record = await readRecord(email);
  if (!record) throw new Error("No browser S/MIME certificate is stored for this address");
  const publicKey = `${record.certificatePEM}${record.chainPEM}`;
  let identity = null;
  for (const address of record.emails?.length ? record.emails : [record.email]) {
    identity = await api("/api/encryption/client-identity", {
      method: "POST",
      body: { email: address, protocol: "smime", publicKey }
    }).then((data) => data?.identity || null);
  }
  return identity;
}

/* ------------------------------------------------------------------ */
/* Recipient directory and local fingerprint pinning                   */
/* ------------------------------------------------------------------ */

async function readPin(email) {
  const db = await openClientDB();
  const tx = db.transaction(PIN_STORE, "readonly");
  return idbRequest(tx.objectStore(PIN_STORE).get(normalizeEmail(email)));
}

export async function smimeDirectoryKeyPin(email) {
  return readPin(email);
}

export async function pinSMIMEDirectoryKey(email, fingerprint) {
  const address = normalizeEmail(email);
  const value = String(fingerprint || "").toUpperCase();
  if (!address || !value) throw new Error("A certificate and fingerprint are required");
  const db = await openClientDB();
  const tx = db.transaction(PIN_STORE, "readwrite");
  await idbRequest(tx.objectStore(PIN_STORE).put({ email: address, fingerprint: value, updatedAt: new Date().toISOString() }));
}

export async function resolveSMIMERecipientCertificates(emails) {
  const forge = await forgeLib();
  const unique = [...new Set((emails || []).map(normalizeEmail).filter(Boolean))];
  if (!unique.length) return new Map();
  const data = await api(`/api/encryption/directory?protocol=smime&emails=${encodeURIComponent(unique.join(","))}`);
  const directory = data?.directory || {};
  const resolved = new Map();
  const changes = [];
  for (const email of unique) {
    const entries = Array.isArray(directory[email]) ? directory[email] : [];
    for (const entry of entries) {
      const certificates = parsePEMCertificates(entry.publicKey || "", forge);
      const leaf = certificates[0];
      if (!leaf) continue;
      const problem = encryptionProblem(leaf, email);
      if (problem) continue;
      const fingerprint = certificateFingerprint(leaf, forge);
      const pin = await readPin(email);
      if (pin?.fingerprint && pin.fingerprint !== fingerprint) {
        changes.push({ email, previousFingerprint: pin.fingerprint, fingerprint });
        continue;
      }
      if (!pin) await pinSMIMEDirectoryKey(email, fingerprint);
      resolved.set(email, { certificate: leaf, chain: certificates.slice(1), fingerprint });
      break;
    }
  }
  if (changes.length) {
    const error = new Error(`The S/MIME certificate for ${changes.map((change) => change.email).join(", ")} changed`);
    error.code = "DIRECTORY_KEY_CHANGED";
    error.recipients = changes.map((change) => change.email);
    error.changes = changes;
    throw error;
  }
  return resolved;
}

/* ------------------------------------------------------------------ */
/* CMS signing, encryption and wrapping                                */
/* ------------------------------------------------------------------ */

function base64Lines(binary) {
  const clean = String(binary || "");
  if (!clean) return "";
  const encoded = forgeUtilBase64(clean);
  const lines = [];
  for (let offset = 0; offset < encoded.length; offset += 76) lines.push(encoded.slice(offset, offset + 76));
  return lines.join("\r\n");
}

let forgeBase64 = null;
function forgeUtilBase64(binary) {
  if (!forgeBase64) throw new Error("The S/MIME engine is not loaded");
  return forgeBase64.encode64(binary);
}

function wrapEnvelopedData(derBinary, forge) {
  forgeBase64 = forge.util;
  const boundary = `a8m-smime-${crypto.randomUUID().replace(/-/g, "")}`;
  return `MIME-Version: 1.0\r\nContent-Type: application/pkcs7-mime; smime-type=enveloped-data; name="smime.p7m"\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename="smime.p7m"\r\n\r\n${base64Lines(derBinary)}\r\n`;
}

function signDetached(contentBinary, identity, forge) {
  const signedData = forge.pkcs7.createSignedData();
  signedData.content = forge.util.createBuffer(contentBinary);
  signedData.detached = true;
  signedData.addCertificate(identity.certificate);
  for (const certificate of identity.chain || []) signedData.addCertificate(certificate);
  signedData.addSigner({
    key: identity.privateKey,
    certificate: identity.certificate,
    digestAlgorithm: forge.pki.oids.sha256
  });
  signedData.sign();
  const signature = forge.asn1.toDer(signedData.toAsn1()).getBytes();
  const boundary = `a8m-smime-sig-${crypto.randomUUID().replace(/-/g, "")}`;
  return `MIME-Version: 1.0\r\nContent-Type: multipart/signed; protocol="application/pkcs7-signature"; micalg=sha-256; boundary="${boundary}"\r\n\r\n--${boundary}\r\n${contentBinary}\r\n--${boundary}\r\nContent-Type: application/pkcs7-signature; name="smime.p7s"\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename="smime.p7s"\r\n\r\n${base64Lines(signature)}\r\n--${boundary}--\r\n`;
}

function encryptCMS(contentBinary, certificates, forge) {
  const enveloped = forge.pkcs7.createEnvelopedData();
  enveloped.content = forge.util.createBuffer(contentBinary);
  for (const certificate of certificates) enveloped.addRecipient(certificate);
  enveloped.encrypt();
  return forge.asn1.toDer(enveloped.toAsn1()).getBytes();
}

export async function encryptClientSMIMEMIME({ bodyText, bodyHtml, recipientEmails, sign, attachments = [] }) {
  if (!unlocked) throw new Error("Unlock your browser S/MIME certificate first");
  const forge = await forgeLib();
  forgeBase64 = forge.util;
  const recipients = await resolveSMIMERecipientCertificates(recipientEmails);
  const missing = (recipientEmails || []).map(normalizeEmail).filter((email) => email && !recipients.has(email));
  if (missing.length) {
    const error = new Error(`No S/MIME certificate is available for ${[...new Set(missing)].join(", ")}`);
    error.code = "MISSING_RECIPIENT_KEY";
    error.recipients = [...new Set(missing)];
    throw error;
  }
  const plaintext = buildPlaintextMIME({ bodyText, bodyHtml, attachments });
  let content = forge.util.encodeUtf8(plaintext);
  if (sign) content = signDetached(content, unlocked, forge);
  const certificates = [...recipients.values()].map((item) => item.certificate);
  // Keep the Sent copy readable without a second server-side plaintext copy.
  if (!certificates.some((certificate) => certificateFingerprint(certificate, forge) === certificateFingerprint(unlocked.certificate, forge))) {
    certificates.push(unlocked.certificate);
  }
  const wrapped = wrapEnvelopedData(encryptCMS(content, certificates, forge), forge);
  if (wrapped.length > MAX_CLIENT_MIME_BYTES) {
    const error = new Error("The encrypted message is too large");
    error.code = "CLIENT_MIME_TOO_LARGE";
    throw error;
  }
  return wrapped;
}

/* ------------------------------------------------------------------ */
/* Parsing and decryption                                              */
/* ------------------------------------------------------------------ */

function splitHeaderBody(text) {
  const normalized = String(text || "").replace(/\r\n/g, "\n");
  const index = normalized.indexOf("\n\n");
  if (index < 0) return { headers: "", body: normalized };
  return { headers: normalized.slice(0, index), body: normalized.slice(index + 2) };
}

function parseHeaders(raw) {
  const headers = {};
  for (const line of String(raw || "").replace(/\n[ \t]+/g, " ").split("\n")) {
    const index = line.indexOf(":");
    if (index > 0) headers[line.slice(0, index).trim().toLowerCase()] = line.slice(index + 1).trim();
  }
  return headers;
}

function contentTypeInfo(value) {
  const parts = String(value || "text/plain").split(";");
  const params = {};
  for (const part of parts.slice(1)) {
    const index = part.indexOf("=");
    if (index > 0) params[part.slice(0, index).trim().toLowerCase()] = part.slice(index + 1).trim().replace(/^"|"$/g, "");
  }
  return { mediaType: parts[0].trim().toLowerCase(), params };
}

function multipartSegments(body, boundary) {
  const delimiter = `--${boundary}`;
  const segments = [];
  let current = null;
  for (const line of String(body || "").replace(/\r\n/g, "\n").split("\n")) {
    if (line === delimiter || line === `${delimiter}--`) {
      if (current) segments.push(current.join("\r\n"));
      if (line === `${delimiter}--`) break;
      current = [];
      continue;
    }
    if (current) current.push(line);
  }
  if (current) segments.push(current.join("\r\n"));
  return segments;
}

function findSMIMECiphertext(text, depth = 0) {
  if (depth > 20) return null;
  const { headers, body } = splitHeaderBody(text);
  const parsed = parseHeaders(headers);
  const { mediaType, params } = contentTypeInfo(parsed["content-type"]);
  if (mediaType === "application/pkcs7-mime" || mediaType === "application/x-pkcs7-mime") {
    if (!params["smime-type"] || params["smime-type"].toLowerCase() === "enveloped-data") {
      return { body, encoding: parsed["content-transfer-encoding"] || "" };
    }
    return null;
  }
  if (mediaType.startsWith("multipart/") && params.boundary) {
    for (const segment of multipartSegments(body, params.boundary)) {
      const found = findSMIMECiphertext(segment, depth + 1);
      if (found) return found;
    }
  }
  return null;
}

function decodeBase64Binary(value, forge) {
  const clean = String(value || "").replace(/\s+/g, "");
  try {
    return forge.util.decode64(clean);
  } catch {
    return "";
  }
}

function certificateEmail(certificate) {
  return certificateEmails(certificate)[0] || "";
}

function verifySignedData(content, signatureDer, forge) {
  let parsed;
  try {
    parsed = forge.pkcs7.messageFromAsn1(forge.asn1.fromDer(signatureDer));
  } catch {
    return { state: "invalid", reason: "The S/MIME signature could not be parsed" };
  }
  const capture = parsed.rawCapture || {};
  if (!capture.signature) return { state: "invalid", reason: "The S/MIME signature is empty" };
  const contentType = capture.contentType ? forge.asn1.derToOid(capture.contentType) : "";
  if (contentType && contentType !== forge.pki.oids.data) {
    return { state: "invalid", reason: "The S/MIME signature covers an unsupported content type" };
  }
  let digestOid = "";
  try { digestOid = forge.asn1.derToOid(capture.digestAlgorithm); } catch { return { state: "invalid", reason: "The S/MIME digest algorithm is missing" }; }
  const digestName = forge.pki.oids[digestOid];
  const digestFactory = digestName ? forge.md[digestName] : null;
  if (!digestFactory) return { state: "invalid", reason: "The S/MIME digest algorithm is not supported" };
  let signedBytes = content;
  const attributes = capture.authenticatedAttributes;
  if (Array.isArray(attributes) && attributes.length) {
    let messageDigest = null;
    let attributeContentType = null;
    for (const attribute of attributes) {
      try {
        const oid = forge.asn1.derToOid(attribute.value[0].value);
        const value = attribute.value[1]?.value?.[0]?.value;
        if (oid === forge.pki.oids.messageDigest) messageDigest = value;
        if (oid === forge.pki.oids.contentType) attributeContentType = value;
      } catch {}
    }
    if (attributeContentType && forge.asn1.derToOid(attributeContentType) !== forge.pki.oids.data) {
      return { state: "invalid", reason: "The S/MIME signature covers an unsupported content type" };
    }
    const contentDigest = digestFactory.create().update(content, "raw").digest().getBytes();
    if (!messageDigest || messageDigest !== contentDigest) {
      return { state: "invalid", reason: "The S/MIME signed content does not match its digest" };
    }
    const attributesSet = forge.asn1.create(forge.asn1.Class.UNIVERSAL, forge.asn1.Type.SET, true, attributes);
    signedBytes = forge.asn1.toDer(attributesSet).getBytes();
  }
  const verifyDigest = digestFactory.create().update(signedBytes, "raw").digest().getBytes();
  const serial = forge.util.bytesToHex(capture.serial || "").toLowerCase();
  const signer = (parsed.certificates || []).find((certificate) => certificate.serialNumber.toLowerCase() === serial);
  if (!signer) return { state: "unknown", reason: "The S/MIME signer certificate is not included" };
  if (!signer.publicKey?.verify) return { state: "unknown", reason: "The S/MIME signer key type is not supported" };
  let verified = false;
  try { verified = signer.publicKey.verify(verifyDigest, capture.signature); } catch { verified = false; }
  if (!verified) {
    // The body part may have lost its final CRLF while being parsed. Retry
    // with the canonical trailing line break before declaring the signature bad.
    const retryDigest = digestFactory.create().update(`${signedBytes}\r\n`, "raw").digest().getBytes();
    try { verified = signer.publicKey.verify(retryDigest, capture.signature); } catch { verified = false; }
  }
  if (!verified) return { state: "invalid", reason: "The S/MIME signature did not verify" };
  if (!certificateValidNow(signer)) return { state: "expired", reason: "The S/MIME signer certificate is outside its validity period", fingerprint: certificateFingerprint(signer, forge) };
  return {
    state: "valid_untrusted",
    fingerprint: certificateFingerprint(signer, forge),
    signerEmail: certificateEmail(signer)
  };
}

function verifyInnerSignature(text, forge) {
  const { headers, body } = splitHeaderBody(text);
  const { mediaType, params } = contentTypeInfo(parseHeaders(headers)["content-type"]);
  if (mediaType !== "multipart/signed" || !params.boundary) return { state: "none" };
  const segments = multipartSegments(body, params.boundary);
  if (segments.length < 2) return { state: "invalid", reason: "The S/MIME signed message is malformed" };
  const signedContent = segments[0];
  const signatureEntity = segments[1];
  const signatureBody = splitHeaderBody(signatureEntity).body;
  const signatureDer = decodeBase64Binary(signatureBody, forge);
  if (!signatureDer) return { state: "invalid", reason: "The S/MIME signature is empty" };
  return verifySignedData(signedContent, signatureDer, forge);
}

function extractCMSSignedContent(derBinary, forge) {
  let contentInfo;
  try {
    contentInfo = forge.asn1.fromDer(derBinary);
  } catch {
    return "";
  }
  if (!contentInfo || contentInfo.type !== forge.asn1.Type.SEQUENCE || !contentInfo.value?.length) return "";
  const signedWrapper = contentInfo.value[1];
  const signedData = signedWrapper?.value?.[0];
  if (!signedData || signedData.type !== forge.asn1.Type.SEQUENCE || !signedData.value || signedData.value.length < 3) return "";
  const encapsulated = signedData.value[2];
  const explicit = encapsulated?.value?.[1];
  if (!explicit) return "";
  const chunks = [];
  const collect = (node) => {
    if (!node) return;
    if (node.type === forge.asn1.Type.OCTETSTRING && !node.constructed) {
      chunks.push(node.value || "");
      return;
    }
    for (const child of node.value || []) collect(child);
  };
  collect(explicit);
  return chunks.join("");
}

function signedContentEntity(text) {
  const { headers, body } = splitHeaderBody(text);
  const { mediaType, params } = contentTypeInfo(parseHeaders(headers)["content-type"]);
  if (mediaType !== "multipart/signed" || !params.boundary) return text;
  const segments = multipartSegments(body, params.boundary);
  return segments[0] || text;
}

export async function decryptClientSMIMEMessage(raw, senderEmail = "") {
  if (!unlocked) throw new Error("Unlock your browser S/MIME certificate first");
  const forge = await forgeLib();
  forgeBase64 = forge.util;
  const part = findSMIMECiphertext(raw);
  if (!part) {
    const error = new Error("This message is not an S/MIME enveloped message");
    error.code = "NOT_SMIME";
    throw error;
  }
  const der = decodeBase64Binary(part.body, forge);
  if (!der) throw new Error("The S/MIME ciphertext is malformed");
  let enveloped;
  try {
    enveloped = forge.pkcs7.messageFromAsn1(forge.asn1.fromDer(der));
  } catch {
    throw new Error("The S/MIME envelope could not be parsed");
  }
  const serial = String(unlocked.certificate.serialNumber || "").toLowerCase();
  const order = [
    ...(enveloped.recipients || []).filter((recipient) => String(recipient.serialNumber || "").toLowerCase() === serial),
    ...(enveloped.recipients || []).filter((recipient) => String(recipient.serialNumber || "").toLowerCase() !== serial)
  ];
  let decrypted = false;
  for (const recipient of order) {
    try {
      enveloped.decrypt(recipient, unlocked.privateKey);
      decrypted = true;
      break;
    } catch {
      // Try the next recipient; the certificate serial is a hint, not proof.
    }
  }
  if (!decrypted) throw new Error("This S/MIME message was not encrypted to your certificate");
  const content = enveloped.content?.bytes ? enveloped.content.bytes() : "";
  if (!content) throw new Error("The S/MIME message has no decrypted content");
  let text = "";
  let signature = { state: "none" };
  let parsed;
  const head = String(content).slice(0, 96).trimStart().toLowerCase();
  const looksMIME = head.startsWith("mime-version:") || head.startsWith("content-type:") ||
    head.startsWith("content-transfer-encoding:") || head.startsWith("content-disposition:");
  if (looksMIME) {
    text = forge.util.decodeUtf8(content);
    signature = verifyInnerSignature(text, forge);
    parsed = parseClientMIME(signedContentEntity(text));
  } else {
    // Some S/MIME clients put an attached CMS SignedData inside the envelope
    // instead of a multipart/signed MIME entity. Unwrap that shape too.
    const innerContent = extractCMSSignedContent(content, forge);
    if (!innerContent) throw new Error("The decrypted S/MIME content is not a signed MIME entity");
    text = forge.util.decodeUtf8(innerContent);
    signature = verifySignedData(innerContent, content, forge);
    parsed = parseClientMIME(text);
  }
  const warnings = [];
  if (signature.state === "invalid") warnings.push("The S/MIME signature did not verify");
  if (signature.state === "expired") warnings.push("The S/MIME signer certificate has expired");
  const expectedSender = normalizeEmail(senderEmail);
  if (expectedSender && signature.signerEmail && signature.signerEmail !== expectedSender) {
    warnings.push("The S/MIME signer address differs from the message sender");
  }
  return { ...parsed, signature, warnings };
}

/* ------------------------------------------------------------------ */
/* Browser-local encrypted drafts                                      */
/* ------------------------------------------------------------------ */

async function readDraftRecords(email) {
  const db = await openClientDB();
  const tx = db.transaction(DRAFT_STORE, "readonly");
  const records = await idbRequest(tx.objectStore(DRAFT_STORE).getAll());
  return (records || []).filter((record) => record.email === normalizeEmail(email) && record.protocol === "smime");
}

async function allDraftRecords(email) {
  const db = await openClientDB();
  const tx = db.transaction(DRAFT_STORE, "readonly");
  const records = await idbRequest(tx.objectStore(DRAFT_STORE).getAll());
  return (records || []).filter((record) => record.email === normalizeEmail(email));
}

async function writeDraftRecord(record) {
  const db = await openClientDB();
  const tx = db.transaction(DRAFT_STORE, "readwrite");
  await idbRequest(tx.objectStore(DRAFT_STORE).put(record));
}

export async function saveClientSMIMEDraft(email, id, payload) {
  if (!unlocked) throw new Error("Unlock your browser S/MIME certificate to save an encrypted draft");
  const forge = await forgeLib();
  forgeBase64 = forge.util;
  const address = normalizeEmail(email);
  const record = await readRecord(address);
  if (!record) throw new Error("No browser S/MIME certificate is stored for this address");
  if (!id) throw new Error("The local draft has no identifier");
  const serialized = JSON.stringify(payload || {});
  if (serialized.length > 20 * 1024 * 1024) throw new Error("This draft is too large to store locally");
  const certificates = [findStoredCertificate(record, forge)].filter(Boolean);
  if (!certificates.length) throw new Error("The browser S/MIME certificate could not be read");
  const der = encryptCMS(forge.util.encodeUtf8(serialized), certificates, forge);
  const updatedAt = new Date().toISOString();
  await writeDraftRecord({ id, email: address, protocol: "smime", updatedAt, ciphertext: forge.util.encode64(der) });
  const records = (await allDraftRecords(address)).sort((left, right) => String(right.updatedAt || "").localeCompare(String(left.updatedAt || "")));
  for (const stale of records.slice(25)) {
    const db = await openClientDB();
    const tx = db.transaction(DRAFT_STORE, "readwrite");
    await idbRequest(tx.objectStore(DRAFT_STORE).delete(stale.id));
  }
  return { id, email: address, updatedAt };
}

function findStoredCertificate(record, forge) {
  return parsePEMCertificates(record?.certificatePEM || "", forge)[0] || null;
}

export async function listClientSMIMEDrafts(email) {
  if (!unlocked) {
    const error = new Error("Unlock your browser S/MIME certificate to open encrypted drafts");
    error.code = "CLIENT_KEY_LOCKED";
    throw error;
  }
  const forge = await forgeLib();
  const drafts = [];
  for (const record of await readDraftRecords(email)) {
    try {
      const enveloped = forge.pkcs7.messageFromAsn1(forge.asn1.fromDer(forge.util.decode64(record.ciphertext || "")));
      let decrypted = false;
      for (const recipient of enveloped.recipients || []) {
        try {
          enveloped.decrypt(recipient, unlocked.privateKey);
          decrypted = true;
          break;
        } catch {}
      }
      if (!decrypted) continue;
      const payload = JSON.parse(forge.util.decodeUtf8(enveloped.content.bytes()));
      drafts.push({ id: record.id, updatedAt: record.updatedAt, ...payload, localDraft: true });
    } catch {
      // A draft encrypted to a different or damaged key must not block others.
    }
  }
  return drafts.sort((left, right) => String(right.updatedAt || "").localeCompare(String(left.updatedAt || "")));
}
