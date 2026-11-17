/* Safe HTML rendering for untrusted email content. */
import { esc } from "../core/helpers.js";

export const SAFE_EMAIL_STYLE_PROPERTIES = new Set([
  "color", "background-color", "background", "font-family", "font-size", "font-weight", "font-style",
  "font-variant", "text-decoration", "text-align", "text-indent", "line-height", "letter-spacing", "vertical-align",
  "margin", "margin-top", "margin-right", "margin-bottom", "margin-left",
  "padding", "padding-top", "padding-right", "padding-bottom", "padding-left",
  "border", "border-top", "border-right", "border-bottom", "border-left",
  "border-color", "border-width", "border-style", "border-radius", "border-collapse", "border-spacing",
  "width", "max-width", "height", "max-height", "display", "white-space", "word-break", "direction", "unicode-bidi",
  "overflow-wrap", "list-style-type", "text-transform", "table-layout", "opacity",
]);

export function safeEmailStyle(value) {
  const declarations = [];
  for (const part of String(value || "").split(";")) {
    const index = part.indexOf(":");
    if (index < 0) continue;
    const property = part.slice(0, index).trim().toLowerCase();
    const declaration = part.slice(index + 1).trim();
    if (!SAFE_EMAIL_STYLE_PROPERTIES.has(property) || !declaration) continue;
    if (/url\s*\(|expression\s*\(|@import|[<>]/i.test(declaration)) continue;
    declarations.push(`${property}: ${declaration}`);
  }
  return declarations.join("; ");
}

export function emailImageProxyURL(source) {
  return `/api/mail/image?url=${encodeURIComponent(source)}`;
}

export function emailImageSource(source) {
  const value = String(source || "").trim();
  if (!value) return "";
  if (/^cid:/i.test(value)) return "";
  if (/^data:image\/(?:png|jpe?g|gif|webp|avif|bmp|x-icon)(?:;base64)?,/i.test(value)) return value;
  if (/^https?:\/\//i.test(value)) return emailImageProxyURL(value);
  if (/^\/\//.test(value)) return emailImageProxyURL(`https:${value}`);
  return "";
}

export function cidImageMap(message) {
  const map = new Map();
  for (const attachment of (message?.attachments || [])) {
    if (!attachment.contentId || !String(attachment.mime || "").startsWith("image/")) continue;
    const key = String(attachment.contentId).replace(/^<|>$/g, "").toLowerCase();
    if (key && attachment.contentUrl) map.set(key, attachment.contentUrl);
  }
  return map;
}

export const EMAIL_NAMED_COLORS = {
  black: [0, 0, 0], white: [255, 255, 255], gray: [128, 128, 128], grey: [128, 128, 128],
  silver: [192, 192, 192], red: [255, 0, 0], blue: [0, 0, 255], green: [0, 128, 0],
  navy: [0, 0, 128], maroon: [128, 0, 0], purple: [128, 0, 128], teal: [0, 128, 128],
  olive: [128, 128, 0], yellow: [255, 255, 0], lime: [0, 255, 0], aqua: [0, 255, 255],
  fuchsia: [255, 0, 255]
};

let emailColorProbe = null;
export const emailColorCache = new Map();

export function parseEmailColorFast(raw) {
  let match = raw.match(/^#([0-9a-f]{3,8})$/i);
  if (match) {
    let hex = match[1];
    if (hex.length === 3 || hex.length === 4) {
      hex = hex.split("").map((part) => part + part).join("");
    }
    if (hex.length === 6 || hex.length === 8) {
      if (hex.length === 8 && hex.slice(6) === "00") return null;
      return [parseInt(hex.slice(0, 2), 16), parseInt(hex.slice(2, 4), 16), parseInt(hex.slice(4, 6), 16)];
    }
    return null;
  }
  match = raw.match(/^rgba?\(\s*([\d.]+)\s*[,\s]\s*([\d.]+)\s*[,\s]\s*([\d.]+)(?:\s*[,/]\s*([\d.]+%?))?\s*\)$/i);
  if (match) {
    if (match[4] != null) {
      const alpha = match[4].endsWith("%") ? Number(match[4].slice(0, -1)) / 100 : Number(match[4]);
      if (alpha === 0) return null;
    }
    return [Number(match[1]), Number(match[2]), Number(match[3])];
  }
  return EMAIL_NAMED_COLORS[raw] || null;
}

export function parseEmailColor(value) {
  const raw = String(value || "").trim().toLowerCase();
  if (!raw || ["transparent", "inherit", "initial", "unset", "currentcolor", "none"].includes(raw)) return null;
  if (emailColorCache.has(raw)) return emailColorCache.get(raw);
  let result = parseEmailColorFast(raw);
  if (!result) {
    // Let the browser parse the full CSS Color grammar (hsl, hwb, lab, newer
    // named colours). The canvas normalises it to hex or rgba for us.
    try {
      emailColorProbe = emailColorProbe || document.createElement("canvas").getContext("2d");
      if (emailColorProbe) {
        emailColorProbe.fillStyle = "#010203";
        emailColorProbe.fillStyle = raw;
        const normalized = String(emailColorProbe.fillStyle || "").toLowerCase();
        if (normalized && normalized !== "#010203") result = parseEmailColorFast(normalized);
      }
    } catch {}
  }
  if (emailColorCache.size > 4096) emailColorCache.clear();
  emailColorCache.set(raw, result);
  return result;
}

export function emailColorLuminance(rgb) {
  return (0.299 * rgb[0] + 0.587 * rgb[1] + 0.114 * rgb[2]) / 255;
}

export function emailElementBackground(node) {
  if (!node || node.nodeType !== 1) return null;
  return parseEmailColor(node.style?.backgroundColor || "")
    || parseEmailColor(node.getAttribute?.("bgcolor") || "");
}

export function emailElementColor(node) {
  if (!node || node.nodeType !== 1) return null;
  return parseEmailColor(node.style?.color || "")
    || parseEmailColor(node.getAttribute?.("color") || "");
}

export function emailNearestBackground(node) {
  for (let element = node; element && element.nodeType === 1; element = element.parentElement) {
    const color = emailElementBackground(element);
    if (color) return color;
  }
  return null;
}

// Email clients routinely keep dark inline text that disappears on a dark
// reading surface. Annotate the affected nodes so CSS can adapt them in dark
// mode while leaving light-background cards and explicit brand colours intact.
export function annotateEmailContrast(node) {
  const ownBackground = emailElementBackground(node);
  if (ownBackground) {
    const luminance = emailColorLuminance(ownBackground);
    if (luminance > 0.6) node.dataset.emailLightSurface = "1";
    else if (luminance < 0.4) node.dataset.emailDarkSurface = "1";
  }
  const color = emailElementColor(node);
  if (!color) return;
  const nearest = emailNearestBackground(node);
  const luminance = emailColorLuminance(color);
  if (luminance < 0.5) {
    if (!nearest) {
      node.dataset.emailDarkPageText = "1";
    } else {
      const background = emailColorLuminance(nearest);
      if (background < 0.4) node.dataset.emailOnDark = "1";
    }
  } else if (luminance > 0.7) {
    if (!nearest) {
      node.dataset.emailLightPageText = "1";
    } else if (emailColorLuminance(nearest) > 0.6) {
      node.dataset.emailOnLight = "1";
    }
  }
}

export function safeEmailHtml(html, { message = null, allowImages = true } = {}) {
  const template = document.createElement("template");
  template.innerHTML = String(html || "");
  template.content.querySelectorAll("script, style, iframe, object, embed, form, link, meta, base, svg, math").forEach((node) => node.remove());
  const cidImages = cidImageMap(message);
  template.content.querySelectorAll("*").forEach((node) => {
    for (const attribute of Array.from(node.attributes)) {
      const name = attribute.name.toLowerCase();
      if (name.startsWith("on") || name === "srcdoc" || name === "formaction" || name === "srcset") node.removeAttribute(attribute.name);
      if (name === "style") {
        const safe = safeEmailStyle(attribute.value);
        if (safe) node.setAttribute("style", safe);
        else node.removeAttribute(attribute.name);
      }
      if ((name === "href" || name === "src") && /^\s*javascript:/i.test(attribute.value)) node.removeAttribute(attribute.name);
    }
    if (node.tagName === "A") {
      node.setAttribute("target", "_blank");
      node.setAttribute("rel", "noopener noreferrer nofollow");
    }
    if (node.tagName === "IMG") {
      const source = node.getAttribute("src") || "";
      if (source) node.setAttribute("data-email-src", source);
      let cidKey = /^cid:/i.test(source) ? source.slice(4).replace(/^<|>$/g, "").toLowerCase() : "";
      if (cidKey.includes("%")) {
        try { cidKey = decodeURIComponent(cidKey); } catch {}
      }
      const cidURL = cidKey ? cidImages.get(cidKey) : "";
      // Preserve the designer's intended dimensions (logos are often width="200"
      // or width="240px"); removing them stretched images to intrinsic size.
      for (const dimension of ["width", "height"]) {
        const value = String(node.getAttribute(dimension) || "").trim();
        if (value && !/^\d{1,5}(?:%|px)?$/i.test(value)) node.removeAttribute(dimension);
      }
      // Outlook treats a numeric width attribute as the rendered width even
      // when the email also carries width:100% in its inline style. Apply the
      // same precedence so logos do not fill the whole compose column.
      const widthValue = String(node.getAttribute("width") || "").trim();
      if (/^\d{1,5}(?:px)?$/i.test(widthValue)) {
        node.style.width = `${parseInt(widthValue, 10)}px`;
      }
      node.setAttribute("decoding", "async");
      node.setAttribute("referrerpolicy", "no-referrer");
      node.setAttribute("class", "email-image");
      if (cidURL) {
        node.setAttribute("src", cidURL);
      } else if (allowImages) {
        const proxied = emailImageSource(source);
        if (proxied) node.setAttribute("src", proxied);
        else {
          node.setAttribute("data-blocked-src", source);
          node.setAttribute("src", "data:image/gif;base64,R0lGODlhAQABAAD/ACwAAAAAAQABAAACADs=");
          node.classList.add("is-blocked-image");
        }
      } else {
        node.setAttribute("data-blocked-src", source);
        node.setAttribute("src", "data:image/gif;base64,R0lGODlhAQABAAD/ACwAAAAAAQABAAACADs=");
        node.classList.add("is-blocked-image");
      }
    }
  });
  template.content.querySelectorAll("*").forEach(annotateEmailContrast);
  return template.innerHTML;
}

export function emailHasImages(html) {
  const template = document.createElement("template");
  template.innerHTML = String(html || "");
  return Boolean(template.content.querySelector("img"));
}
