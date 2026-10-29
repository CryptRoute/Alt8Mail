import { createHmac, randomInt } from "node:crypto";

const ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";

export function base32Decode(value) {
  const clean = String(value || "").toUpperCase().replace(/[^A-Z2-7]/g, "");
  let bits = 0;
  let carry = 0;
  const bytes = [];
  for (const char of clean) {
    const index = ALPHABET.indexOf(char);
    if (index === -1) continue;
    carry = (carry << 5) | index;
    bits += 5;
    if (bits >= 8) {
      bytes.push((carry >>> (bits - 8)) & 0xff);
      bits -= 8;
    }
  }
  return Buffer.from(bytes);
}

export function hotp(secret, counter, digits = 6) {
  const key = base32Decode(secret);
  const buffer = Buffer.alloc(8);
  buffer.writeBigUInt64BE(BigInt(counter));
  const digest = createHmac("sha1", key).update(buffer).digest();
  const offset = digest[digest.length - 1] & 0x0f;
  const binary = ((digest[offset] & 0x7f) << 24)
    | ((digest[offset + 1] & 0xff) << 16)
    | ((digest[offset + 2] & 0xff) << 8)
    | (digest[offset + 3] & 0xff);
  return String(binary % (10 ** digits)).padStart(digits, "0");
}

export function generateTotp(secret, timestamp = Date.now(), step = 30, digits = 6) {
  return hotp(secret, Math.floor(timestamp / 1000 / step), digits);
}

export function normalizeOtp(value) {
  return String(value || "").replace(/\D/g, "").slice(0, 8);
}

export function verifyTotp(secret, code, { window = 1, timestamp = Date.now(), step = 30, digits = 6 } = {}) {
  const normalized = normalizeOtp(code);
  if (normalized.length !== digits) return false;
  const counter = Math.floor(timestamp / 1000 / step);
  for (let offset = -window; offset <= window; offset += 1) {
    if (hotp(secret, counter + offset, digits) === normalized) return true;
  }
  return false;
}

export function generateRecoveryCodes(count = 6) {
  const groups = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789";
  const codes = new Set();
  while (codes.size < count) {
    let code = "MRDN-";
    for (let group = 0; group < 2; group += 1) {
      if (group) code += "-";
      for (let i = 0; i < 4; i += 1) code += groups[randomInt(0, groups.length)];
    }
    codes.add(code);
  }
  return Array.from(codes);
}

export function otpAuthUri({ secret, account, issuer = "Meridian Mail" }) {
  const label = encodeURIComponent(`${issuer}:${account}`);
  const params = new URLSearchParams({ secret, issuer, algorithm: "SHA1", digits: "6", period: "30" });
  return `otpauth://totp/${label}?${params.toString()}`;
}
