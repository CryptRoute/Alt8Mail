/* Alt8Mail System Control — shared UI primitives. No inline scripts: CSP-safe. */

export const esc = (value) =>
  String(value ?? '').replace(/[&<>"']/g, (ch) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[ch]));

export const attr = esc;

export function num(value) {
  const n = Number(value);
  if (!Number.isFinite(n)) return '0';
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 }).format(n);
}

export function pct(value, digits = 1) {
  const n = Number(value);
  if (!Number.isFinite(n)) return '0%';
  return `${n.toFixed(digits).replace(/\.0$/, '')}%`;
}

export function bytes(value, digits = 1) {
  let n = Number(value) || 0;
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i += 1; }
  if (i === 0) return `${Math.round(n)} ${units[i]}`;
  return `${n.toFixed(digits).replace(/\.0$/, '')} ${units[i]}`;
}

export function dateTime(value, fallback = '—') {
  if (!value) return fallback;
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return fallback;
  return new Intl.DateTimeFormat(undefined, {
    year: 'numeric', month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit',
  }).format(d);
}

export function dateOnly(value, fallback = '—') {
  if (!value) return fallback;
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return fallback;
  return new Intl.DateTimeFormat(undefined, { year: 'numeric', month: 'short', day: '2-digit' }).format(d);
}

export function timeOnly(value, fallback = '—') {
  if (!value) return fallback;
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return fallback;
  return new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(d);
}

export function timeAgo(value, fallback = '—') {
  if (!value) return fallback;
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return fallback;
  const seconds = Math.round((Date.now() - d.getTime()) / 1000);
  if (Math.abs(seconds) < 45) return 'just now';
  const ranges = [
    ['year', 31536000], ['month', 2592000], ['week', 604800],
    ['day', 86400], ['hour', 3600], ['minute', 60],
  ];
  const formatter = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });
  for (const [unit, size] of ranges) {
    if (Math.abs(seconds) >= size) return formatter.format(Math.round(-seconds / size), unit);
  }
  return 'just now';
}

export function duration(seconds) {
  let s = Math.max(0, Math.round(Number(seconds) || 0));
  const days = Math.floor(s / 86400); s %= 86400;
  const hours = Math.floor(s / 3600); s %= 3600;
  const minutes = Math.floor(s / 60);
  if (days) return `${days}d ${hours}h`;
  if (hours) return `${hours}h ${minutes}m`;
  if (minutes) return `${minutes}m`;
  return `${s}s`;
}

export function humanTitle(value) {
  const text = String(value ?? '').replace(/[_.-]+/g, ' ').trim();
  if (!text) return '—';
  return text.charAt(0).toUpperCase() + text.slice(1);
}

export function truncate(value, max = 80) {
  const text = String(value ?? '');
  return text.length > max ? `${text.slice(0, max - 1)}…` : text;
}

export function safeJSON(value) {
  try { return JSON.parse(value); } catch { return null; }
}

const ICONS = {
  dashboard: '<rect x="3" y="3" width="7" height="9" rx="1.5"/><rect x="14" y="3" width="7" height="5" rx="1.5"/><rect x="14" y="12" width="7" height="9" rx="1.5"/><rect x="3" y="16" width="7" height="5" rx="1.5"/>',
  activity: '<path d="M3 12h4l2.5-7 5 14 2.5-7H21"/>',
  globe: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a15 15 0 0 1 0 18M12 3a15 15 0 0 0 0 18"/>',
  mail: '<rect x="3" y="5" width="18" height="14" rx="2"/><path d="m3.5 7 8.5 6 8.5-6"/>',
  users: '<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75"/>',
  user: '<circle cx="12" cy="8" r="4"/><path d="M5 21v-2a7 7 0 0 1 14 0v2"/>',
  forward: '<path d="m15 17 5-5-5-5"/><path d="M4 18v-2a4 4 0 0 1 4-4h12"/>',
  upload: '<path d="M12 16V4m0 0L7 9m5-5 5 5"/><path d="M4 16v3a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-3"/>',
  download: '<path d="M12 4v12m0 0 5-5m-5 5-5-5"/><path d="M4 18v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2"/>',
  send: '<path d="m22 2-7 20-4-9-9-4Z"/><path d="M22 2 11 13"/>',
  queue: '<path d="M4 6h16M4 12h16M4 18h10"/><circle cx="19" cy="18" r="2"/>',
  relay: '<path d="M5 12h14m0 0-4-4m4 4-4 4"/><path d="M4 5h9M4 19h9"/>',
  'shield-off': '<path d="m2 2 20 20"/><path d="M5 5a11 11 0 0 1 7-3 11 11 0 0 0 7 3v6c0 2.5-.8 5-2.2 7.1M9 20.1A12 12 0 0 1 5 11V8"/>',
  shield: '<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z"/><path d="m9 12 2 2 4-4"/>',
  lock: '<rect x="4" y="11" width="16" height="10" rx="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4"/>',
  key: '<circle cx="7.5" cy="15.5" r="4.5"/><path d="m10.7 12.3 8.8-8.8M15 8l2.5 2.5M18 5l2 2"/>',
  webhook: '<path d="M18 16.98h-5.99c-1.1 0-1.95.94-2.48 1.9A4 4 0 0 1 2 17c0-1.8 1.2-3.3 2.8-3.8"/><path d="m6 17 3.13-5.78c.53-.97.1-2.18-.5-3.1a4 4 0 1 1 6.89-4.06"/><path d="m12 6 3.13 5.73C15.66 12.7 16.9 13 18 13a4 4 0 0 1 0 8"/>',
  bell: '<path d="M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9"/><path d="M10 21h4"/>',
  server: '<rect x="3" y="4" width="18" height="6" rx="1.5"/><rect x="3" y="14" width="18" height="6" rx="1.5"/><path d="M7 7h.01M7 17h.01"/>',
  plug: '<path d="M12 22v-5M9 8V2M15 8V2M6 8h12v5a6 6 0 0 1-12 0Z"/>',
  'file-code': '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z"/><path d="M14 2v6h6M9 13l-2 2 2 2M15 13l2 2-2 2"/>',
  list: '<path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01"/>',
  archive: '<rect x="3" y="4" width="18" height="4" rx="1"/><path d="M5 8v11a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8M10 12h4"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  calendar: '<rect x="3" y="5" width="18" height="16" rx="2"/><path d="M16 3v4M8 3v4M3 10h18"/>',
  network: '<rect x="9" y="2" width="6" height="6" rx="1"/><rect x="2" y="16" width="6" height="6" rx="1"/><rect x="16" y="16" width="6" height="6" rx="1"/><path d="M12 8v4M5 16v-4h14v4"/>',
  flame: '<path d="M12 22c4 0 6-2.7 6-6 0-4-3-5.5-3-9 0 0-3 2-3 5-1-1-1.5-2.5-1.5-4C7.5 10 6 12.5 6 16c0 3.3 2 6 6 6Z"/>',
  database: '<ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v14c0 1.7 4 3 9 3s9-1.3 9-3V5"/><path d="M3 12c0 1.7 4 3 9 3s9-1.3 9-3"/>',
  refresh: '<path d="M20 11a8 8 0 1 0-2.3 5.7"/><path d="M20 4v7h-7"/>',
  rotate: '<path d="M3 12a9 9 0 1 0 9-9"/><path d="M3 4v6h6"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="m20 20-4-4"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  minus: '<path d="M5 12h14"/>',
  copy: '<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>',
  external: '<path d="M15 3h6v6M10 14 21 3"/><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/>',
  trash: '<path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6M10 11v5M14 11v5"/>',
  edit: '<path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z"/>',
  close: '<path d="M18 6 6 18M6 6l12 12"/>',
  check: '<path d="m20 6-11 11-5-5"/>',
  'check-circle': '<circle cx="12" cy="12" r="9"/><path d="m8.5 12.5 2.5 2.5 4.5-5"/>',
  alert: '<path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z"/><path d="M12 9v4M12 17h.01"/>',
  'alert-circle': '<circle cx="12" cy="12" r="9"/><path d="M12 8v4M12 16h.01"/>',
  x: '<path d="M18 6 6 18M6 6l12 12"/>',
  'x-circle': '<circle cx="12" cy="12" r="9"/><path d="m15 9-6 6M9 9l6 6"/>',
  'chevron-down': '<path d="m6 9 6 6 6-6"/>',
  'chevron-right': '<path d="m9 6 6 6-6 6"/>',
  'chevron-left': '<path d="m15 6-6 6 6 6"/>',
  menu: '<path d="M4 6h16M4 12h16M4 18h16"/>',
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>',
  moon: '<path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8Z"/>',
  logout: '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><path d="m16 17 5-5-5-5M21 12H9"/>',
  dots: '<circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/>',
  eye: '<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/>',
  play: '<path d="m6 3 14 9-14 9Z"/>',
  pause: '<rect x="6" y="4" width="4" height="16" rx="1"/><rect x="14" y="4" width="4" height="16" rx="1"/>',
  filter: '<path d="M22 3H2l8 9.5V19l4 2v-8.5Z"/>',
  settings: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.9.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.9 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1A1.7 1.7 0 0 0 4.6 9a1.7 1.7 0 0 0-.3-1.9l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.9.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.9-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.9V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1Z"/>',
  terminal: '<rect x="2" y="4" width="20" height="16" rx="2"/><path d="m6 9 3 3-3 3M12 15h5"/>',
  'arrow-up-right': '<path d="M7 17 17 7M7 7h10v10"/>',
  layers: '<path d="m12 2 9 5-9 5-9-5Z"/><path d="m3 12 9 5 9-5M3 17l9 5 9-5"/>',
  sliders: '<path d="M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6"/>',
  'mail-open': '<path d="M3 9 12 3l9 6v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z"/><path d="m3 9 9 7 9-7"/>',
  'shield-check': '<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z"/><path d="m9 12 2 2 4-4"/>',
  'key-round': '<path d="M2 18a6 6 0 1 0 4.2-5.7"/><path d="m9 15 12-12M15 9l3 3M18 6l2 2"/>',
  link: '<path d="M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7"/><path d="M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7"/>',
  inbox: '<path d="M22 12h-6l-2 3h-4l-2-3H2"/><path d="M5.5 5.5 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.5-6.5A2 2 0 0 0 16.7 4H7.3a2 2 0 0 0-1.8 1.5Z"/>',
  'circle-dot': '<circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="2.5" fill="currentColor"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v5M12 8h.01"/>',
  'panel-left': '<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M9 3v18"/>',
  'file-plus': '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z"/><path d="M14 2v6h6M12 18v-6M9 15h6"/>',
  gauge: '<path d="M12 14 8 8"/><path d="M3.3 18a9 9 0 1 1 17.4 0"/><circle cx="12" cy="14" r="1.5"/>',
  star: '<path d="m12 2.5 2.9 5.9 6.5.9-4.7 4.6 1.1 6.5-5.8-3-5.8 3 1.1-6.5-4.7-4.6 6.5-.9Z"/>',
  cloud: '<path d="M17.5 19H9a7 7 0 1 1 6.4-9.8A5 5 0 0 1 17.5 19Z"/>',
  power: '<path d="M12 2v9"/><path d="M18.4 5.6a9 9 0 1 1-12.8 0"/>',
  sparkles: '<path d="m12 3 1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5Z"/><path d="M19 15l.7 2.3L22 18l-2.3.7L19 21l-.7-2.3L16 18l2.3-.7Z"/>',
};

export function icon(name, size = 18, cls = '') {
  const paths = ICONS[name] || ICONS['circle-dot'];
  return `<svg class="icon ${cls}" width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${paths}</svg>`;
}

const TONES = {
  good: { icon: 'check-circle', label: 'Good' },
  warning: { icon: 'alert-circle', label: 'Warning' },
  serious: { icon: 'alert', label: 'Attention' },
  critical: { icon: 'x-circle', label: 'Critical' },
  info: { icon: 'info', label: 'Info' },
  neutral: { icon: 'circle-dot', label: 'Neutral' },
};

export function toneForStatus(status) {
  const s = String(status ?? '').toLowerCase();
  if (['sent', 'received', 'delivered', 'active', 'up', 'ok', 'pass', 'completed', 'complete', 'verified', 'ready', 'valid', 'enabled', 'good', 'healthy', 'synchronized'].includes(s)) return 'good';
  if (['deferred', 'queued', 'running', 'warning', 'warn', 'pending', 'starting', 'provisioning', 'expiring', 'manual', 'unknown'].includes(s)) return 'warning';
  if (['bounced', 'rejected', 'expired', 'failed', 'error', 'down', 'critical', 'revoked', 'invalid', 'disabled', 'stopped', 'inactive', 'locked', 'not_found', 'not installed'].includes(s)) return 'critical';
  if (['inbound', 'outbound', 'internal', 'read', 'write', 'admin', 'info'].includes(s)) return 'info';
  return 'neutral';
}

export function statusPill(status, label) {
  const tone = toneForStatus(status);
  const text = label ?? humanTitle(status || 'unknown');
  return `<span class="status-pill tone-${tone}">${icon(TONES[tone].icon, 13)}<span>${esc(text)}</span></span>`;
}

export function badge(text, tone = 'neutral', iconName = '') {
  return `<span class="badge tone-${esc(tone)}">${iconName ? icon(iconName, 12) : ''}${esc(text)}</span>`;
}

export function pageHeader({ eyebrow = '', title, description = '', actions = '', meta = '' }) {
  return `<header class="page-head">
    <div class="page-head-copy">
      ${eyebrow ? `<div class="eyebrow">${esc(eyebrow)}</div>` : ''}
      <h1>${esc(title)}</h1>
      ${description ? `<p class="page-description">${esc(description)}</p>` : ''}
      ${meta ? `<div class="page-meta">${meta}</div>` : ''}
    </div>
    ${actions ? `<div class="page-actions">${actions}</div>` : ''}
  </header>`;
}

export function sectionHead(title, description = '', actions = '') {
  return `<div class="section-head"><div><h2>${esc(title)}</h2>${description ? `<p>${esc(description)}</p>` : ''}</div>${actions ? `<div class="section-actions">${actions}</div>` : ''}</div>`;
}

export function card(body, { title = '', subtitle = '', actions = '', className = '', flush = false, id = '' } = {}) {
  return `<section class="card ${className}"${id ? ` id="${attr(id)}"` : ''}>
    ${title || actions ? `<header class="card-head"><div>${title ? `<h3>${esc(title)}</h3>` : ''}${subtitle ? `<p>${esc(subtitle)}</p>` : ''}</div>${actions ? `<div class="card-actions">${actions}</div>` : ''}</header>` : ''}
    <div class="card-body${flush ? ' is-flush' : ''}">${body}</div>
  </section>`;
}

export function statTile({ label, value, detail = '', iconName = 'activity', href = '', tone = '', spark = '' }) {
  const content = `<div class="stat-icon ${tone ? `tone-${tone}` : ''}">${icon(iconName, 18)}</div>
    <div class="stat-copy"><span class="stat-label">${esc(label)}</span><strong class="stat-value">${esc(value)}</strong>${detail ? `<span class="stat-detail">${esc(detail)}</span>` : ''}</div>
    ${spark ? `<div class="stat-spark">${spark}</div>` : ''}`;
  return href ? `<a class="stat-tile" href="${attr(href)}">${content}</a>` : `<div class="stat-tile">${content}</div>`;
}

export function meter({ label, value, detail = '', tone = '', iconName = '' }) {
  const v = Math.max(0, Math.min(100, Number(value) || 0));
  const computedTone = tone || (v >= 95 ? 'critical' : v >= 85 ? 'warning' : 'good');
  const computedIcon = iconName || TONES[computedTone].icon;
  return `<div class="meter tone-${computedTone}" style="--meter-value:${v}%;--meter-tone:var(--status-${computedTone === 'good' ? 'good' : computedTone === 'warning' ? 'warning' : computedTone === 'critical' ? 'critical' : 'info'})">
    <div class="meter-head"><span>${icon(computedIcon, 15)}${esc(label)}</span><strong>${pct(v)}</strong></div>
    <div class="meter-track"><div class="meter-fill" style="width:${v}%"></div></div>
    ${detail ? `<div class="meter-detail">${esc(detail)}</div>` : ''}
  </div>`;
}

export function progress(value, tone = 'accent', label = '') {
  const v = Math.max(0, Math.min(100, Number(value) || 0));
  return `<div class="progress-row"><div class="progress tone-${esc(tone)}"><span style="width:${v}%"></span></div>${label ? `<span class="progress-label">${esc(label)}</span>` : ''}</div>`;
}

export function sparkline(values, { width = 132, height = 38, color = 'var(--series-1)', label = 'Trend' } = {}) {
  const points = (Array.isArray(values) ? values : []).map((v) => Number(v) || 0);
  if (points.length < 2) return `<span class="spark-empty" aria-hidden="true"></span>`;
  const max = Math.max(...points, 1);
  const min = Math.min(...points, 0);
  const span = max - min || 1;
  const pad = 3;
  const coords = points.map((v, i) => {
    const x = pad + (i / (points.length - 1)) * (width - pad * 2);
    const y = height - pad - ((v - min) / span) * (height - pad * 2);
    return [x, y];
  });
  const line = coords.map(([x, y], i) => `${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`).join(' ');
  const area = `${line} L${coords[coords.length - 1][0].toFixed(1)},${height - pad} L${coords[0][0].toFixed(1)},${height - pad} Z`;
  return `<svg class="sparkline" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}" role="img" aria-label="${attr(label)}"><path class="spark-area" d="${area}" style="fill:${color}"/><path class="spark-line" d="${line}" style="stroke:${color}"/><circle cx="${coords[coords.length - 1][0].toFixed(1)}" cy="${coords[coords.length - 1][1].toFixed(1)}" r="2.2" style="fill:${color}"/></svg>`;
}

const SERIES_COLORS = ['var(--series-1)', 'var(--series-2)', 'var(--series-3)', 'var(--series-4)'];
const chartRegistry = new Map();
let chartSeq = 0;

export function lineChart({ labels, series, height = 260, ariaLabel = 'Mail volume chart' }) {
  if (!Array.isArray(labels) || !labels.length || !Array.isArray(series) || !series.length) {
    return `<div class="chart-empty"><span>No mail volume recorded yet</span><small>Events will appear after the first message flows through Postfix.</small></div>`;
  }
  const id = `chart-${++chartSeq}`;
  const width = 940;
  const pad = { top: 18, right: 108, bottom: 34, left: 54 };
  const plotW = width - pad.left - pad.right;
  const plotH = height - pad.top - pad.bottom;
  series = series.map((s) => ({ ...s, values: (s.values || []).map((v) => Number(v) || 0) }));
  const values = series.flatMap((s) => s.values.map((v) => Number(v) || 0));
  const rawMax = Math.max(1, ...values);
  const magnitude = Math.pow(10, Math.max(0, Math.floor(Math.log10(rawMax))));
  const step = magnitude / 2 >= 1 ? magnitude / 2 : 1;
  const yMax = Math.max(step, Math.ceil(rawMax / step) * step);
  const n = labels.length;
  const x = (i) => pad.left + (n <= 1 ? plotW / 2 : (i / (n - 1)) * plotW);
  const y = (v) => pad.top + plotH - (Math.max(0, v) / yMax) * plotH;
  const gridCount = 4;
  const grid = [];
  for (let g = 0; g <= gridCount; g += 1) {
    const value = (yMax / gridCount) * g;
    const yy = y(value);
    grid.push(`<line class="chart-grid" x1="${pad.left}" y1="${yy.toFixed(1)}" x2="${width - pad.right}" y2="${yy.toFixed(1)}"/><text class="chart-axis" x="${pad.left - 10}" y="${(yy + 4).toFixed(1)}" text-anchor="end">${value % 1 === 0 ? num(value) : value.toFixed(1)}</text>`);
  }
  const stride = Math.max(1, Math.ceil(n / 7));
  const xLabels = labels.map((label, i) => {
    if (i % stride !== 0 && i !== n - 1) return '';
    return `<text class="chart-axis" x="${x(i).toFixed(1)}" y="${height - 10}" text-anchor="middle">${esc(label)}</text>`;
  }).join('');
  const paths = series.map((s, si) => {
    const color = SERIES_COLORS[si % SERIES_COLORS.length];
    const line = s.values.map((v, i) => `${i === 0 ? 'M' : 'L'}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join(' ');
    return `<path class="chart-line${s.dashed ? ' is-dashed' : ''}" d="${line}" style="stroke:${color}"/><circle class="chart-last" cx="${x(n - 1).toFixed(1)}" cy="${y(s.values[n - 1] || 0).toFixed(1)}" r="3" style="fill:${color};stroke:var(--surface)"/>`;
  }).join('');
  // End labels are mandatory with four series. Resolve vertical collisions.
  const labelPositions = series.map((s, si) => ({ si, y: y(s.values[n - 1] || 0) })).sort((a, b) => a.y - b.y);
  for (let i = 1; i < labelPositions.length; i += 1) {
    if (labelPositions[i].y - labelPositions[i - 1].y < 15) labelPositions[i].y = labelPositions[i - 1].y + 15;
  }
  const endLabels = labelPositions.filter(({ si }) => series[si].values.some((value) => (Number(value) || 0) > 0)).map(({ si, y: ly }) => {
    const color = SERIES_COLORS[si % SERIES_COLORS.length];
    return `<text class="chart-end-label" x="${width - pad.right + 10}" y="${(ly + 4).toFixed(1)}" style="fill:var(--text-2)"><tspan fill="${color}">●</tspan> ${esc(series[si].name)}</text>`;
  }).join('');
  const legend = series.map((s, si) => `<span class="legend-item"><i style="background:${SERIES_COLORS[si % SERIES_COLORS.length]}"></i>${esc(s.name)}</span>`).join('');
  chartRegistry.set(id, { labels, series, x, y, n, pad, plotW, plotH, width, height, top: pad.top });
  return `<div class="chart-shell" data-chart-id="${id}">
    <div class="chart-legend">${legend}</div>
    <div class="chart-wrap">
      <svg class="line-chart" viewBox="0 0 ${width} ${height}" preserveAspectRatio="xMidYMid meet" role="img" aria-label="${attr(ariaLabel)}">
        ${grid.join('')}${xLabels}${paths}${endLabels}
        <g class="chart-cursor" hidden><line x1="0" y1="${pad.top}" x2="0" y2="${pad.top + plotH}" class="chart-crosshair"/><g class="chart-dots"></g></g>
      </svg>
      <div class="chart-tooltip" hidden></div>
    </div>
    <details class="chart-table"><summary>View data as a table</summary>
      <div class="table-wrap"><table class="data compact"><thead><tr><th scope="col">Date</th>${series.map((s) => `<th scope="col" class="num">${esc(s.name)}</th>`).join('')}</tr></thead><tbody>
      ${labels.map((label, i) => `<tr><td>${esc(label)}</td>${series.map((s) => `<td class="num">${num(s.values[i])}</td>`).join('')}</tr>`).join('')}
      </tbody></table></div>
    </details>
  </div>`;
}

export function bindLineChart(root = document) {
  root.querySelectorAll('.chart-shell[data-chart-id]').forEach((shell) => {
    if (shell.dataset.bound === '1') return;
    const chart = chartRegistry.get(shell.dataset.chartId);
    if (!chart) return;
    shell.dataset.bound = '1';
    const svg = shell.querySelector('svg.line-chart');
    const cursor = shell.querySelector('.chart-cursor');
    const crosshair = shell.querySelector('.chart-crosshair');
    const dots = shell.querySelector('.chart-dots');
    const tooltip = shell.querySelector('.chart-tooltip');
    if (!svg || !cursor || !tooltip) return;
    const colors = SERIES_COLORS;
    const move = (event) => {
      const rect = svg.getBoundingClientRect();
      if (!rect.width) return;
      const px = ((event.clientX - rect.left) / rect.width) * chart.width;
      let index = 0;
      let best = Infinity;
      for (let i = 0; i < chart.n; i += 1) {
        const d = Math.abs(chart.x(i) - px);
        if (d < best) { best = d; index = i; }
      }
      const tx = chart.x(index);
      crosshair.setAttribute('x1', tx); crosshair.setAttribute('x2', tx);
      dots.innerHTML = chart.series.map((s, si) => `<circle cx="${tx.toFixed(1)}" cy="${chart.y(s.values[index] || 0).toFixed(1)}" r="4" style="fill:${colors[si % colors.length]};stroke:var(--surface)"/>`).join('');
      cursor.hidden = false;
      tooltip.hidden = false;
      tooltip.innerHTML = `<strong>${esc(chart.labels[index])}</strong>${chart.series.map((s, si) => `<span><i style="background:${colors[si % colors.length]}"></i>${esc(s.name)}<b>${num(s.values[index])}</b></span>`).join('')}`;
      const shellRect = shell.getBoundingClientRect();
      const left = Math.min(shellRect.width - 190, Math.max(8, event.clientX - shellRect.left + 16));
      tooltip.style.left = `${left}px`;
      tooltip.style.top = `${Math.max(8, event.clientY - shellRect.top - 20)}px`;
    };
    const leave = () => { cursor.hidden = true; tooltip.hidden = true; };
    svg.addEventListener('pointermove', move);
    svg.addEventListener('pointerleave', leave);
  });
}

export function barList(items, { valueKey = 'count', labelKey = 'label', total = null, empty = 'No data yet' } = {}) {
  if (!items.length) return emptyState(empty, 'Nothing to rank in this period.');
  const max = Math.max(...items.map((item) => Number(item[valueKey]) || 0), 1);
  return `<div class="bar-list">${items.map((item, i) => {
    const width = ((Number(item[valueKey]) || 0) / max) * 100;
    return `<div class="bar-row"><span class="bar-rank">${i + 1}</span><span class="bar-label" title="${attr(item[labelKey])}">${esc(item[labelKey])}</span><span class="bar-track"><span class="bar-fill" style="width:${width.toFixed(1)}%"></span></span><span class="bar-value">${num(item[valueKey])}${total ? `<small>${pct((Number(item[valueKey]) / total) * 100)}</small>` : ''}</span></div>`;
  }).join('')}</div>`;
}

export function emptyState(title, description = '', action = '', iconName = 'inbox') {
  return `<div class="empty-state"><div class="empty-icon">${icon(iconName, 22)}</div><h3>${esc(title)}</h3>${description ? `<p>${esc(description)}</p>` : ''}${action || ''}</div>`;
}

export function loadingState(label = 'Loading…', rows = 3) {
  return `<div class="loading-state" role="status" aria-live="polite"><span class="sr-only">${esc(label)}</span>${Array.from({ length: rows }, (_, i) => `<div class="skeleton" style="width:${[92, 76, 84][i % 3]}%"></div>`).join('')}</div>`;
}

export function errorState(message, retryLabel = 'Try again') {
  return `<div class="error-state" role="alert"><div class="error-icon">${icon('alert', 22)}</div><div><h3>Could not load this view</h3><p>${esc(message)}</p></div><button class="btn btn-secondary" type="button" data-retry>${icon('refresh', 15)}${esc(retryLabel)}</button></div>`;
}

export function inlineNotice(tone = 'info', title = '', message = '', action = '') {
  const iconName = TONES[tone]?.icon || 'info';
  return `<div class="notice tone-${esc(tone)}"><span class="notice-icon">${icon(iconName, 17)}</span><div><strong>${esc(title)}</strong>${message ? `<p>${esc(message)}</p>` : ''}${action ? `<div class="notice-action">${action}</div>` : ''}</div></div>`;
}

export function tabs(items, { className = '' } = {}) {
  return `<nav class="tabs ${className}" aria-label="Section tabs">${items.map((item) => `<a class="tab${item.active ? ' is-active' : ''}" href="${attr(item.href)}"${item.active ? ' aria-current="page"' : ''}>${item.icon ? icon(item.icon, 15) : ''}${esc(item.label)}${item.count !== undefined ? `<span class="tab-count">${num(item.count)}</span>` : ''}</a>`).join('')}</nav>`;
}

export function field({ label, name, type = 'text', value = '', placeholder = '', help = '', required = false, extra = '', autocomplete = 'off', min = '', max = '', step = '', rows = 4, options = null, disabled = false }) {
  const id = `f-${name}-${Math.random().toString(36).slice(2, 8)}`;
  const common = `id="${id}" name="${attr(name)}" class="input"${required ? ' required' : ''}${disabled ? ' disabled' : ''} ${extra}`;
  let control = '';
  if (type === 'textarea') {
    control = `<textarea ${common} rows="${rows}" placeholder="${attr(placeholder)}">${esc(value)}</textarea>`;
  } else if (type === 'select') {
    control = `<select ${common}>${(options || []).map((opt) => `<option value="${attr(opt.value)}"${String(opt.value) === String(value) ? ' selected' : ''}>${esc(opt.label)}</option>`).join('')}</select>`;
  } else if (type === 'checkbox') {
    return `<label class="check-field"><input type="checkbox" name="${attr(name)}"${value ? ' checked' : ''}${disabled ? ' disabled' : ''}><span>${esc(label)}</span>${help ? `<small>${esc(help)}</small>` : ''}</label>`;
  } else {
    control = `<input ${common} type="${attr(type)}" value="${attr(value)}" placeholder="${attr(placeholder)}"${min !== '' ? ` min="${attr(min)}"` : ''}${max !== '' ? ` max="${attr(max)}"` : ''}${step !== '' ? ` step="${attr(step)}"` : ''} autocomplete="${attr(autocomplete)}">`;
  }
  return `<div class="field"><label for="${id}">${esc(label)}${required ? ' <span class="req">*</span>' : ''}</label>${control}${help ? `<small class="field-help">${esc(help)}</small>` : ''}</div>`;
}

export function switchField({ label, name, checked = false, help = '', disabled = false }) {
  return `<label class="switch-field"><span class="switch"><input type="checkbox" name="${attr(name)}"${checked ? ' checked' : ''}${disabled ? ' disabled' : ''}><span class="switch-track"><span></span></span></span><span class="switch-copy"><strong>${esc(label)}</strong>${help ? `<small>${esc(help)}</small>` : ''}</span></label>`;
}

export function codeBlock(text, { language = '', maxHeight = '' } = {}) {
  const style = maxHeight ? ` style="max-height:${attr(maxHeight)}"` : '';
  return `<div class="code-block"><div class="code-head"><span>${esc(language || 'text')}</span><button type="button" class="icon-btn" data-copy="${attr(text)}" title="Copy" aria-label="Copy">${icon('copy', 14)}</button></div><pre${style}><code>${esc(text)}</code></pre></div>`;
}

export function diffBlock(lines, empty = 'No changes') {
  if (!lines || !lines.length) return `<p class="muted">${esc(empty)}</p>`;
  return `<pre class="diff-block">${lines.map((line) => {
    const sign = line.startsWith('+') ? 'add' : line.startsWith('-') ? 'del' : 'ctx';
    return `<span class="diff-${sign}">${esc(line)}</span>`;
  }).join('\n')}</pre>`;
}

export function kv(rows) {
  return `<dl class="kv-grid">${rows.filter(Boolean).map(([key, value]) => `<div><dt>${esc(key)}</dt><dd>${value ?? '—'}</dd></div>`).join('')}</dl>`;
}

export function formActions(primary = 'Save changes', { cancel = true, danger = false, extra = '', primaryAttrs = '' } = {}) {
  return `<div class="form-actions">${extra}<div class="form-actions-right">${cancel ? `<button type="button" class="btn btn-ghost" data-modal-close>Cancel</button>` : ''}<button type="submit" class="btn ${danger ? 'btn-danger' : 'btn-primary'}"${primaryAttrs}>${esc(primary)}</button></div></div>`;
}

export function formValues(form) {
  const values = {};
  new FormData(form).forEach((value, key) => {
    if (Object.prototype.hasOwnProperty.call(values, key)) {
      if (Array.isArray(values[key])) values[key].push(value);
      else values[key] = [values[key], value];
    } else {
      values[key] = value;
    }
  });
  form.querySelectorAll('input[type="checkbox"]').forEach((input) => {
    values[input.name] = input.checked;
  });
  return values;
}

export function selectedValues(container, name) {
  return Array.from(container.querySelectorAll(`input[name="${name}"]:checked`)).map((i) => i.value);
}

/* Modal ------------------------------------------------------------------ */

const modalStack = [];

export function openModal({ title, subtitle = '', body = '', footer = '', size = '', onMount = null, onClose = null, closeLabel = 'Close' }) {
  const root = document.getElementById('modal-root');
  const overlay = document.createElement('div');
  overlay.className = 'modal-overlay';
  const titleId = `modal-title-${Math.random().toString(36).slice(2, 8)}`;
  overlay.innerHTML = `<div class="modal ${size ? `modal-${size}` : ''}" role="dialog" aria-modal="true" aria-labelledby="${titleId}">
    <header class="modal-head"><div><h2 id="${titleId}">${esc(title)}</h2>${subtitle ? `<p>${esc(subtitle)}</p>` : ''}</div><button type="button" class="icon-btn modal-close" aria-label="${esc(closeLabel)}" data-modal-close>${icon('close', 18)}</button></header>
    <div class="modal-body">${body}</div>
    ${footer ? `<footer class="modal-footer">${footer}</footer>` : ''}
  </div>`;
  root.appendChild(overlay);
  document.body.classList.add('modal-open');
  const previousFocus = document.activeElement;
  let settled = false;
  const close = (result = undefined) => {
    if (settled) return;
    settled = true;
    overlay.classList.add('is-closing');
    setTimeout(() => {
      overlay.remove();
      if (!root.children.length) document.body.classList.remove('modal-open');
    }, 120);
    const stackIndex = modalStack.indexOf(instance);
    if (stackIndex >= 0) modalStack.splice(stackIndex, 1);
    document.removeEventListener('keydown', onKey);
    if (onClose) onClose(result);
    if (previousFocus && typeof previousFocus.focus === 'function') previousFocus.focus();
  };
  const onKey = (event) => {
    if (event.key === 'Escape' && modalStack[modalStack.length - 1] === instance) {
      event.preventDefault();
      close();
      return;
    }
    if (event.key !== 'Tab') return;
    const focusables = overlay.querySelectorAll('a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])');
    if (!focusables.length) return;
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
  };
  const instance = { overlay, close, root: overlay.querySelector('.modal') };
  modalStack.push(instance);
  overlay.addEventListener('click', (event) => {
    if (event.target === overlay) close();
    const closer = event.target.closest('[data-modal-close]');
    if (closer) close();
  });
  document.addEventListener('keydown', onKey);
  setTimeout(() => {
    const autofocus = overlay.querySelector('[autofocus]');
    const first = autofocus || overlay.querySelector('input:not([type="hidden"]), select, textarea, button');
    if (first) first.focus();
  }, 30);
  if (onMount) onMount(instance.root, close);
  return instance;
}

export function confirmDialog({ title = 'Confirm action', message = '', confirmLabel = 'Confirm', cancelLabel = 'Cancel', tone = 'danger', requireText = '', details = '' }) {
  return new Promise((resolve) => {
    const formId = `confirm-${Math.random().toString(36).slice(2, 8)}`;
    const body = `<form id="${formId}">
      <div class="confirm-copy">${esc(message)}</div>
      ${details ? `<div class="confirm-details">${details}</div>` : ''}
      ${requireText ? `<div class="field"><label for="${formId}-text">Type <strong class="mono">${esc(requireText)}</strong> to continue</label><input class="input" id="${formId}-text" name="confirm" autocomplete="off" required><small class="field-help">This step cannot be undone.</small></div>` : ''}
    </form>`;
    const footer = `<button type="button" class="btn btn-ghost" data-cancel>${esc(cancelLabel)}</button><button type="submit" form="${formId}" class="btn ${tone === 'danger' ? 'btn-danger' : 'btn-primary'}">${esc(confirmLabel)}</button>`;
    const modal = openModal({
      title, body, footer, size: 'small', onClose: (result) => { if (result !== true) resolve(false); },
      onMount: (el) => {
        const form = el.querySelector(`#${formId}`);
        const cancel = el.querySelector('[data-cancel]');
        if (cancel) cancel.addEventListener('click', () => { modal.close(false); resolve(false); });
        form.addEventListener('submit', (event) => {
          event.preventDefault();
          if (requireText) {
            const input = form.querySelector('[name="confirm"]');
            if (input.value.trim() !== requireText) {
              input.classList.add('is-invalid');
              input.focus();
              return;
            }
          }
          resolve(true);
          modal.close(true);
        });
      },
    });
  });
}

/* Toast ------------------------------------------------------------------ */

export function toast(message, tone = 'info', { title = '', timeout = 4500 } = {}) {
  const host = document.getElementById('toasts');
  if (!host) return;
  const el = document.createElement('div');
  el.className = `toast tone-${tone}`;
  el.setAttribute('role', tone === 'critical' ? 'alert' : 'status');
  el.innerHTML = `<span class="toast-icon">${icon(TONES[tone]?.icon || 'info', 18)}</span><div class="toast-copy">${title ? `<strong>${esc(title)}</strong>` : ''}<span>${esc(message)}</span></div><button type="button" class="icon-btn toast-close" aria-label="Dismiss">${icon('close', 14)}</button><span class="toast-progress"></span>`;
  host.appendChild(el);
  let timer = null;
  const dismiss = () => {
    if (!el.isConnected || el.classList.contains('is-leaving')) return;
    el.classList.add('is-leaving');
    if (timer) clearTimeout(timer);
    setTimeout(() => el.remove(), 180);
  };
  el.querySelector('.toast-close').addEventListener('click', dismiss);
  timer = setTimeout(dismiss, timeout);
  el.addEventListener('mouseenter', () => { if (timer) clearTimeout(timer); });
  el.addEventListener('mouseleave', () => { timer = setTimeout(dismiss, 1600); });
}

export async function copy(text, label = 'Copied to clipboard') {
  const value = String(text ?? '');
  try {
    await navigator.clipboard.writeText(value);
    toast(label, 'good');
  } catch {
    const area = document.createElement('textarea');
    area.value = value;
    area.setAttribute('readonly', '');
    area.style.position = 'fixed';
    area.style.opacity = '0';
    document.body.appendChild(area);
    area.select();
    try { document.execCommand('copy'); toast(label, 'good'); } catch { toast('Copying is blocked by the browser', 'critical'); }
    area.remove();
  }
}

/* Keyboard shortcut labels ------------------------------------------------- */

export function kbd(text) {
  return `<kbd>${esc(text)}</kbd>`;
}
