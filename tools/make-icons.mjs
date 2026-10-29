import fs from "node:fs";
import path from "node:path";
import zlib from "node:zlib";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const outDir = path.resolve(__dirname, "..", "public", "icons");
fs.mkdirSync(outDir, { recursive: true });

const CRC_TABLE = (() => {
  const table = new Uint32Array(256);
  for (let n = 0; n < 256; n += 1) {
    let c = n;
    for (let k = 0; k < 8; k += 1) c = (c & 1) ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    table[n] = c >>> 0;
  }
  return table;
})();

function crc32(buffer) {
  let crc = 0xffffffff;
  for (const byte of buffer) crc = CRC_TABLE[(crc ^ byte) & 0xff] ^ (crc >>> 8);
  return (crc ^ 0xffffffff) >>> 0;
}

function chunk(type, data) {
  const typeBuf = Buffer.from(type, "ascii");
  const body = Buffer.concat([typeBuf, data]);
  const length = Buffer.alloc(4);
  length.writeUInt32BE(data.length, 0);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body), 0);
  return Buffer.concat([length, body, crc]);
}

function encodePng(width, height, rgba) {
  const signature = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]);
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0);
  ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8;
  ihdr[9] = 6;
  ihdr[10] = 0;
  ihdr[11] = 0;
  ihdr[12] = 0;
  const stride = width * 4;
  const raw = Buffer.alloc((stride + 1) * height);
  for (let y = 0; y < height; y += 1) {
    raw[y * (stride + 1)] = 0;
    rgba.copy(raw, y * (stride + 1) + 1, y * stride, y * stride + stride);
  }
  const idat = zlib.deflateSync(raw, { level: 9 });
  return Buffer.concat([signature, chunk("IHDR", ihdr), chunk("IDAT", idat), chunk("IEND", Buffer.alloc(0))]);
}

function roundedRectCoverage(x, y, size, radius) {
  const half = size / 2;
  const dx = Math.max(Math.abs(x - half) - (half - radius), 0);
  const dy = Math.max(Math.abs(y - half) - (half - radius), 0);
  const distance = Math.sqrt(dx * dx + dy * dy);
  return Math.max(0, Math.min(1, radius - distance + 0.5));
}

function segmentDistance(px, py, ax, ay, bx, by) {
  const vx = bx - ax;
  const vy = by - ay;
  const wx = px - ax;
  const wy = py - ay;
  const t = Math.max(0, Math.min(1, (wx * vx + wy * vy) / (vx * vx + vy * vy || 1)));
  const cx = ax + t * vx;
  const cy = ay + t * vy;
  return Math.hypot(px - cx, py - cy);
}

function renderIcon(size, { maskable = false } = {}) {
  const samples = 3;
  const pixels = Buffer.alloc(size * size * 4);
  const scale = size / 512;
  const pad = maskable ? 0.09 : 0;
  const rounded = maskable ? 0 : 1;
  const radius = maskable ? 0 : 112 * scale;
  const stroke = (maskable ? 46 : 52) * scale;
  const points = [
    [150, 372], [150, 148], [256, 292], [362, 148], [362, 372]
  ].map(([x, y]) => [x * scale * (1 - pad * 2) + size * pad, y * scale * (1 - pad * 2) + size * pad]);
  const top = [16, 96, 189];
  const bottom = [8, 52, 105];
  for (let y = 0; y < size; y += 1) {
    for (let x = 0; x < size; x += 1) {
      let r = 0;
      let g = 0;
      let b = 0;
      let a = 0;
      for (let sy = 0; sy < samples; sy += 1) {
        for (let sx = 0; sx < samples; sx += 1) {
          const px = x + (sx + 0.5) / samples;
          const py = y + (sy + 0.5) / samples;
          const bgCoverage = rounded ? roundedRectCoverage(px, py, size, radius) : 1;
          if (bgCoverage <= 0) continue;
          const mix = py / size;
          let cr = Math.round(top[0] + (bottom[0] - top[0]) * mix);
          let cg = Math.round(top[1] + (bottom[1] - top[1]) * mix);
          let cb = Math.round(top[2] + (bottom[2] - top[2]) * mix);
          let ink = 0;
          for (let i = 0; i < points.length - 1; i += 1) {
            const d = segmentDistance(px, py, points[i][0], points[i][1], points[i + 1][0], points[i + 1][1]);
            ink = Math.max(ink, Math.max(0, Math.min(1, stroke / 2 + 0.7 - d)));
          }
          const mixInk = ink * 0.96;
          cr = Math.round(cr * (1 - mixInk) + 255 * mixInk);
          cg = Math.round(cg * (1 - mixInk) + 255 * mixInk);
          cb = Math.round(cb * (1 - mixInk) + 255 * mixInk);
          r += cr * bgCoverage;
          g += cg * bgCoverage;
          b += cb * bgCoverage;
          a += bgCoverage;
        }
      }
      const total = samples * samples;
      const coverage = a / total;
      const index = (y * size + x) * 4;
      if (a > 0) {
        pixels[index] = Math.min(255, Math.round(r / a));
        pixels[index + 1] = Math.min(255, Math.round(g / a));
        pixels[index + 2] = Math.min(255, Math.round(b / a));
      }
      pixels[index + 3] = Math.min(255, Math.round((a / total) * 255));
    }
  }
  return encodePng(size, size, pixels);
}

const outputs = [
  ["icon-192.png", 192, false],
  ["icon-512.png", 512, false],
  ["icon-maskable-512.png", 512, true],
  ["apple-touch-icon.png", 180, true]
];

for (const [name, size, maskable] of outputs) {
  const png = renderIcon(size, { maskable });
  fs.writeFileSync(path.join(outDir, name), png);
  console.log(name, png.length, "bytes");
}
