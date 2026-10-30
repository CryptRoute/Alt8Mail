"use strict";

/* CSV / XLSX reader for the Alt8Mail admin import wizards.
   Everything runs locally in the browser; files are never uploaded before the
   preview is confirmed. */
(function () {
  function normalizeHeader(value) {
    return String(value == null ? "" : value).trim().toLowerCase()
      .replace(/[^a-z0-9]+/g, "_").replace(/^_+|_+$/g, "");
  }

  function parseCSV(text) {
    const source = String(text || "").replace(/^﻿/, "");
    const firstLine = source.split(/\r?\n/).find((line) => line.trim() !== "") || "";
    const delimiter = firstLine.includes("\t") && !firstLine.includes(",") ? "\t" : (!firstLine.includes(",") && firstLine.includes(";") ? ";" : ",");
    const rows = [];
    let row = [];
    let field = "";
    let quoted = false;
    for (let i = 0; i < source.length; i += 1) {
      const char = source[i];
      if (quoted) {
        if (char === '"') {
          if (source[i + 1] === '"') { field += '"'; i += 1; }
          else quoted = false;
        } else {
          field += char;
        }
        continue;
      }
      if (char === '"') { quoted = true; continue; }
      if (char === delimiter) { row.push(field); field = ""; continue; }
      if (char === "\n") { row.push(field); rows.push(row); row = []; field = ""; continue; }
      if (char === "\r") { continue; }
      field += char;
    }
    row.push(field);
    if (row.length > 1 || row[0] !== "") rows.push(row);
    return rows.filter((line) => line.some((cell) => String(cell).trim() !== ""));
  }

  function tableToObjects(matrix) {
    const rows = (matrix || []).filter((line) => line && line.some((cell) => String(cell).trim() !== ""));
    if (rows.length === 0) return { headers: [], rows: [] };
    const headers = rows[0].map((header) => String(header || "").trim());
    const objects = rows.slice(1).map((line) => {
      const record = {};
      headers.forEach((header, index) => { if (header) record[header] = line[index] == null ? "" : line[index]; });
      return record;
    });
    return { headers, rows: objects };
  }

  async function inflateRaw(bytes) {
    if (typeof DecompressionStream === "undefined") {
      throw new Error("This browser cannot open .xlsx files. Export the sheet as CSV instead.");
    }
    const stream = new Blob([bytes]).stream().pipeThrough(new DecompressionStream("deflate-raw"));
    return new Uint8Array(await new Response(stream).arrayBuffer());
  }

  async function readZip(buffer) {
    const view = new DataView(buffer);
    const bytes = new Uint8Array(buffer);
    let eocd = -1;
    for (let i = bytes.length - 22; i >= Math.max(0, bytes.length - 66000); i -= 1) {
      if (view.getUint32(i, true) === 0x06054b50) { eocd = i; break; }
    }
    if (eocd < 0) throw new Error("The .xlsx file is not a valid ZIP archive.");
    const count = view.getUint16(eocd + 10, true);
    let offset = view.getUint32(eocd + 16, true);
    const entries = new Map();
    for (let index = 0; index < count; index += 1) {
      if (view.getUint32(offset, true) !== 0x02014b50) break;
      const method = view.getUint16(offset + 10, true);
      const size = view.getUint32(offset + 20, true);
      const nameLength = view.getUint16(offset + 28, true);
      const extraLength = view.getUint16(offset + 30, true);
      const commentLength = view.getUint16(offset + 32, true);
      const localOffset = view.getUint32(offset + 42, true);
      const name = new TextDecoder().decode(bytes.slice(offset + 46, offset + 46 + nameLength));
      const localNameLength = view.getUint16(localOffset + 26, true);
      const localExtraLength = view.getUint16(localOffset + 28, true);
      const dataStart = localOffset + 30 + localNameLength + localExtraLength;
      const compressed = bytes.slice(dataStart, dataStart + size);
      entries.set(name, { method, data: compressed });
      offset += 46 + nameLength + extraLength + commentLength;
    }
    return entries;
  }

  async function unzipText(entries, name) {
    const entry = entries.get(name);
    if (!entry) return "";
    const bytes = entry.method === 0 ? entry.data : await inflateRaw(entry.data);
    return new TextDecoder().decode(bytes);
  }

  async function parseXLSX(buffer) {
    const entries = await readZip(buffer);
    const sharedXML = await unzipText(entries, "xl/sharedStrings.xml");
    const shared = [];
    if (sharedXML) {
      const doc = new DOMParser().parseFromString(sharedXML, "application/xml");
      doc.querySelectorAll("si").forEach((item) => {
        shared.push(Array.from(item.querySelectorAll("t")).map((text) => text.textContent).join(""));
      });
    }
    let sheetName = Array.from(entries.keys())
      .filter((name) => /^xl\/worksheets\/sheet\d+\.xml$/.test(name))
      .sort((a, b) => Number(a.match(/(\d+)/)[1]) - Number(b.match(/(\d+)/)[1]))[0];
    if (!sheetName) throw new Error("The workbook has no readable worksheet.");
    const sheetXML = await unzipText(entries, sheetName);
    const doc = new DOMParser().parseFromString(sheetXML, "application/xml");
    const matrix = [];
    doc.querySelectorAll("row").forEach((rowNode) => {
      const line = [];
      rowNode.querySelectorAll("c").forEach((cell) => {
        const ref = cell.getAttribute("r") || "";
        const letters = (ref.match(/^[A-Z]+/i) || [""])[0].toUpperCase();
        let index = 0;
        for (const letter of letters) index = index * 26 + (letter.charCodeAt(0) - 64);
        index = Math.max(0, index - 1);
        const type = cell.getAttribute("t") || "";
        let value = "";
        if (type === "inlineStr") {
          value = Array.from(cell.querySelectorAll("is t")).map((text) => text.textContent).join("");
        } else {
          const raw = cell.querySelector("v");
          value = raw ? raw.textContent : "";
          if (type === "s") value = shared[Number(value)] || "";
        }
        line[index] = value;
      });
      if (line.some((value) => value !== undefined && String(value).trim() !== "")) matrix.push(line);
    });
    return matrix;
  }

  async function parseFile(file) {
    const name = String(file && file.name || "").toLowerCase();
    if (name.endsWith(".xlsx") || name.endsWith(".xlsm")) {
      const matrix = await parseXLSX(await file.arrayBuffer());
      return tableToObjects(matrix);
    }
    const text = await file.text();
    return tableToObjects(parseCSV(text));
  }

  function pick(record, names) {
    const normalized = new Map();
    Object.keys(record || {}).forEach((key) => normalized.set(normalizeHeader(key), record[key]));
    for (const name of names) {
      const value = normalized.get(normalizeHeader(name));
      if (value != null && String(value).trim() !== "") return String(value).trim();
    }
    return "";
  }

  window.A8MImport = { parseFile, parseCSV, tableToObjects, pick, normalizeHeader };
})();
