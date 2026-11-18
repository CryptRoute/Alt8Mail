/* System Control — offline license status, seat metering and token upload. */
import * as ui from './ui.js?v=3.9.4';

const esc = ui.esc;
const fmt = {
  num: ui.num,
  dateOnly: ui.dateOnly,
  dateTime: ui.dateTime,
  bytes: ui.bytes,
};

const DOCS_URL = 'https://github.com/CryptRoute/Alt8Mail/blob/main/docs/LICENSING.md';
const MAX_TOKEN_BYTES = 64 * 1024;
const LOADED_STATES = new Set(['valid', 'grace', 'expired']);

const STATE_META = {
  valid: { label: 'Valid', tone: 'good', icon: 'check-circle' },
  grace: { label: 'Grace period', tone: 'warning', icon: 'clock' },
  expired: { label: 'Expired', tone: 'critical', icon: 'alert-circle' },
  unlicensed: { label: 'Community edition', tone: 'neutral', icon: 'circle-dot' },
  invalid: { label: 'Invalid license', tone: 'critical', icon: 'x-circle' },
};

const STATUS_ERROR_TEXT = {
  not_configured: 'No usable license path or public key is configured on this server (not_configured).',
  unreadable: 'The configured license file exists but could not be read (unreadable). Check its permissions and the 64 KiB size limit.',
  malformed: 'The configured license token is malformed (malformed). The file may be incomplete or corrupt.',
  signature: 'The configured license token failed signature verification (signature). Check that the public key matches the token issuer.',
};

// Fixed operator-facing messages for the upload error codes. Unknown failures
// fall back to a generic sentence that never echoes server or parser detail.
const UPLOAD_ERROR_TEXT = {
  LICENSE_NOT_CONFIGURED: 'License storage or verification is not configured. Set CONTROL_LICENSE_FILE and CONTROL_LICENSE_PUBLIC_KEY in /etc/a8m/control.env, and the same path/key as ALT8MAIL_LICENSE_FILE and ALT8MAIL_LICENSE_PUBLIC_KEY in /etc/a8m/app.env, then restart both services.',
  LICENSE_TOKEN: 'That license token is not valid. Check that the complete token was pasted and that it was issued for this server\'s license public key.',
  LICENSE_EXPIRED: 'That license token has expired. Upload a renewed token instead.',
  LICENSE_TOO_LARGE: 'License tokens must be 64 KiB or smaller.',
  CONTENT_TYPE: 'The upload was rejected because the request format was not supported. Try again from this page.',
  LICENSE_WRITE: 'The license file could not be written. Check that /etc/a8m is writable by the Control service and that the license group exists.',
  BAD_JSON: 'The license upload was malformed. Try again from this page.',
  FORBIDDEN: 'Your panel role cannot upload licenses. Ask a superadmin for settings.write permission.',
  CSRF: 'Your session security token expired. Reload the page and try again.',
};

function docsLink(label = 'Licensing guide') {
  return `<a class="btn btn-secondary btn-sm" href="${DOCS_URL}" target="_blank" rel="noopener noreferrer">${ui.icon('external', 13)} ${esc(label)}</a>`;
}

function upgradeUrl(license, feature = '') {
  const base = String(license?.portalUrl || '').trim().replace(/\/+$/, '');
  if (!base) return '';
  const suffix = feature ? `?feature=${encodeURIComponent(feature)}` : '';
  return `${base}/upgrade${suffix}`;
}

function upgradeButton(license, label = 'Upgrade', feature = '') {
  const href = upgradeUrl(license, feature);
  if (!href) return '';
  return `<a class="btn btn-primary btn-sm" href="${esc(href)}" target="_blank" rel="noopener noreferrer">${ui.icon('arrow-up-right', 13)} ${esc(label)}</a>`;
}

function stateMeta(state) {
  return STATE_META[state] || { label: ui.humanTitle(state || 'unknown'), tone: 'neutral', icon: 'circle-dot' };
}

function statePill(state) {
  const meta = stateMeta(state);
  return `<span class="status-pill tone-${esc(meta.tone)}">${ui.icon(meta.icon, 13)}<span>${esc(meta.label)}</span></span>`;
}

function editionLabel(edition) {
  const value = String(edition || 'oss');
  return value.toLowerCase() === 'oss' ? 'Community' : ui.humanTitle(value);
}

function filePathText(license) {
  return license?.filePath ? license.filePath : 'Not configured';
}

function dayCountText(days) {
  return `${ui.num(Math.abs(days))} day${Math.abs(days) === 1 ? '' : 's'}`;
}

function expirySummary(license) {
  if (!license?.expiresAt) return 'No expiry recorded';
  const expiry = Date.parse(license.expiresAt);
  if (!Number.isFinite(expiry)) return 'No expiry recorded';
  const days = Math.ceil((expiry - Date.now()) / 86400000);
  if (license.expired) return days < 0 ? `Expired ${dayCountText(days)} ago` : 'Expired';
  if (license.inGrace) return `Expiry passed ${dayCountText(Math.min(days, -1))} ago · grace period`;
  if (days <= 0) return 'Expires today';
  return `${dayCountText(days)} remaining`;
}

function featureChips(license) {
  const features = Array.isArray(license?.features) ? license.features : [];
  const missing = Array.isArray(license?.missingFeatures) ? license.missingFeatures : [];
  const catalog = Array.isArray(license?.featureCatalog) ? license.featureCatalog : [];
  const labelFor = (key) => catalog.find((feature) => feature.key === key)?.label || ui.humanTitle(String(key || '').replace(/_/g, ' '));
  const entitled = features.length
    ? `<div class="license-features">${features.map((feature) => ui.badge(labelFor(feature), 'info', 'sparkles')).join('')}</div>`
    : '';
  if (!missing.length) {
    return entitled || '<p class="form-note">This license includes every paid capability in the current catalog.</p>';
  }
  const shown = missing.slice(0, 8).map((feature) => labelFor(feature));
  const more = missing.length > shown.length ? ` and ${missing.length - shown.length} more` : '';
  const detail = license?.state === 'unlicensed'
    ? 'The community edition includes 25 active users and the core mail platform. Paid capabilities are locked until a license is uploaded.'
    : license?.state === 'invalid'
      ? 'Paid capabilities are locked until a valid license is uploaded.'
      : license?.state === 'expired'
        ? 'Paid capabilities are disabled while the license is expired.'
        : 'The active license does not include every paid capability.';
  const action = `${upgradeButton(license, license?.state === 'expired' ? 'Renew license' : 'Upgrade')} ${docsLink()}`;
  return `${entitled}
    <div class="mt-3">${ui.inlineNotice('info', 'Paid capabilities', `${detail} Locked: ${shown.join(', ')}${more}.`, action)}</div>`;
}

function tenantScopeText(license) {
  if (!LOADED_STATES.has(license?.state)) return 'Not applicable';
  return license.tenant ? `Tenant ${license.tenant}` : 'Global — all tenants';
}

function statusNotice(license) {
  const docs = docsLink();
  switch (license?.state) {
    case 'unlicensed': {
      const detail = license?.filePath
        ? `Alt8Mail is running in the free community edition: 25 active users are included. No license file was found at ${license.filePath}. Upload a signed token to raise the seat limit and unlock paid capabilities.`
        : 'Alt8Mail is running in the free community edition: 25 active users are included. No license path is configured (CONTROL_LICENSE_FILE is empty). Upload cannot be stored until a path is configured.';
      return `${ui.emptyState('Community edition · 25 active users', detail, `${upgradeButton(license, 'View plans')} ${docsLink()}`, 'key')}
        <p class="form-note license-file-note">Configured path: <span class="mono break-all">${esc(filePathText(license))}</span></p>`;
    }
    case 'invalid': {
      const text = STATUS_ERROR_TEXT[license?.error] || 'The configured license could not be verified.';
      return `${ui.inlineNotice('critical', 'The configured license cannot be used', text, docs)}
        <p class="form-note license-file-note">Configured path: <span class="mono break-all">${esc(filePathText(license))}</span></p>`;
    }
    case 'grace':
      return `${binaryMismatchNotice(license)}${ui.inlineNotice(
        'warning',
        'The license is in its grace period',
        `Paid features remain available during grace, but the license expired on ${fmt.dateOnly(license.expiresAt)}. Renew before the grace period ends to avoid losing paid entitlements.`,
        docs,
      )}`;
    case 'expired':
      return ui.inlineNotice(
        'critical',
        'The license has expired',
        `Paid features are disabled and the deployment has fallen back to the 25-user community limit. Core mail delivery continues. The license expired on ${fmt.dateOnly(license.expiresAt)}. Upload a renewed token to restore paid capabilities and the licensed seat count.`,
        `${upgradeButton(license, 'Renew')} ${docs}`,
      );
    case 'valid':
      return `${binaryMismatchNotice(license)}${ui.inlineNotice(
        'good',
        'The license is active',
        `The license is valid. The webmail enables paid features after it loads this file. It expires on ${fmt.dateOnly(license.expiresAt)} (${expirySummary(license)}).`,
        docs,
      )}`;
    default:
      return ui.inlineNotice('info', 'License state is unknown', 'Reload the page or check the Control service log.', docs);
  }
}

function statusMarkup(license) {
  const loaded = LOADED_STATES.has(license?.state);
  const hasSeats = Number(license?.seats) > 0;
  const rows = [
    ['Edition', esc(editionLabel(license?.edition))],
    ['Binary edition', license?.binaryEdition === 'enterprise'
      ? ui.badge('Enterprise', 'good', 'check-circle')
      : ui.badge('Community', license?.edition === 'oss' ? 'neutral' : 'warning', 'layers')],
    ['State', statePill(license?.state)],
    ['License file', `<span class="mono break-all">${esc(filePathText(license))}</span>`],
  ];
  if (loaded) {
    rows.push(
      ['Expires', license.expiresAt ? esc(fmt.dateOnly(license.expiresAt)) : '—'],
      ['Remaining', esc(expirySummary(license))],
      ['Seats', `${fmt.num(license.seats || 0)} ${license.seatSource === 'community' ? 'community' : 'licensed'}`],
      ['Tenant scope', esc(tenantScopeText(license))],
      ['License ID', `<span class="mono break-all">${esc(license.licenseId || '—')}</span>`],
      ['File updated', license.lastModified ? esc(fmt.dateTime(license.lastModified)) : '—'],
    );
  } else if (license?.state === 'invalid') {
    rows.push(
      ['Failure category', `<span class="mono">${esc(license.error || 'unknown')}</span>`],
      ['File updated', license.lastModified ? esc(fmt.dateTime(license.lastModified)) : '—'],
    );
  }
  if (!loaded && hasSeats) {
    rows.push(['Seats', `${fmt.num(license.seats || 0)} community`], ['Limit source', 'Community edition']);
  }
  if (license?.trustAnchor === 'config') {
    rows.push(['Trust anchor', 'Configuration (development/migration only)']);
  } else if (license?.trustAnchor === 'missing') {
    rows.push(['Trust anchor', ui.badge('Not configured', 'critical')]);
  } else if (license?.trustAnchor === 'builtin') {
    rows.push(['Trust anchor', 'Compiled vendor key']);
  }
  return `<div class="diagnostic-summary">
      ${statePill(license?.state)}
      ${ui.badge(editionLabel(license?.edition), license?.state === 'unlicensed' ? 'neutral' : 'info', 'layers')}
      ${hasSeats ? `<span class="summary-pill">${ui.icon('users', 12)} ${fmt.num(license.usedSeats || 0)} / ${fmt.num(license.seats || 0)} ${license.seatSource === 'community' ? 'community' : 'licensed'} seats used</span>` : ''}
      ${license?.warning ? `<span class="summary-pill">${ui.icon('alert-circle', 12)} Seat warning</span>` : ''}
    </div>
    <div class="mt-3">${statusNotice(license)}</div>
    <div class="mt-4">${ui.kv(rows, 'is-3')}</div>
    ${featureChips(license)}`;
}

function seatTone(license, used, seats) {
  if (used > seats) return 'critical';
  if (license?.warning) return 'warning';
  if (used >= seats) return 'critical';
  return 'good';
}

function seatMarkup(license) {
  const seats = Math.max(0, Number(license.seats) || 0);
  const used = Math.max(0, Number(license.usedSeats) || 0);
  const remaining = Math.max(0, Number(license.remainingSeats) || 0);
  if (seats === 0) {
    return ui.emptyState('No seat limit is available', 'The license or community policy did not provide a seat allowance. Core mail remains available.', docsLink(), 'users');
  }
  const source = license?.seatSource === 'community' ? 'community' : 'licensed';
  const enforcing = source === 'community' || license.state === 'valid' || license.state === 'grace';
  const over = Math.max(0, used - seats);
  // Floor the displayed percentage: rounding up would show 80% for 199/250
  // while the server's exact comparison correctly reports no warning.
  const percent = Math.min(100, Math.floor((used / seats) * 100));
  const warningPercent = Math.min(100, Math.max(1, Number(license.warningPercent) || 80));
  const tone = seatTone(license, used, seats);
  const valueText = `${used} of ${seats} ${source} seats used, ${remaining} remaining${over ? `, ${over} over the limit` : ''}`;
  const warningDetail = `Usage is at or above the configured ${warningPercent}% warning threshold (CONTROL_LICENSE_WARNING_PERCENT). When every seat is in use, new activations in webmail, administration, SCIM and System Control are refused until a seat is freed or the license is upgraded.`;
  const overDetail = `${over} more active seat${over === 1 ? '' : 's'} than the ${source} limit. Existing users are not deactivated automatically; new activations are refused until usage falls below ${seats}.`;
  const warnings = [
    license.warning ? ui.inlineNotice('warning', 'Seat warning threshold reached', warningDetail) : '',
    over ? ui.inlineNotice('critical', 'Seat limit exceeded', overDetail) : '',
  ].join('');
  const ariaNow = Math.min(used, seats);
  return `${warnings}
    <div class="seat-meter tone-${tone}" role="meter" tabindex="0" aria-label="License seat usage"
      aria-valuenow="${ariaNow}" aria-valuemin="0" aria-valuemax="${seats}" aria-valuetext="${esc(valueText)}">
      <div class="seat-meter-head"><span>Active seats</span><strong>${fmt.num(used)} / ${fmt.num(seats)}</strong></div>
      <div class="seat-meter-track">
        <div class="seat-meter-fill" style="width:${percent}%"></div>
        <div class="seat-meter-threshold" style="${warningPercent >= 100 ? 'right:0' : `left:${warningPercent}%`}" title="Warning threshold ${warningPercent}%" aria-hidden="true"></div>
      </div>
      <div class="seat-meter-legend">
        <span>${fmt.num(used)} used</span>
        <span>${fmt.num(remaining)} remaining</span>
        <span>${percent}% of available seats</span>
        <span>Warning at ${warningPercent}%</span>
      </div>
    </div>
    <div class="mt-3">${ui.kv([
      ['Seats used', fmt.num(used)],
      ['Seat limit', fmt.num(seats)],
      ['Seats remaining', over ? `0 (${fmt.num(over)} over limit)` : fmt.num(remaining)],
      ['Limit source', source === 'community' ? 'Community edition (25 users)' : 'Active license'],
      ['Warning threshold', `${warningPercent}% · ${license.warning ? 'reached' : 'not reached'}`],
      ['Enforcement', enforcing ? 'Active' : 'Inactive'],
      ['Counting rule', 'Active, non-deleted users; platform system administrators excluded'],
    ], 'is-3')}</div>
    ${source === 'community' ? `<div class="mt-3">${upgradeButton(license, 'Raise the seat limit')}</div>` : ''}
    <p class="form-note">Seat usage is read live from PostgreSQL and does not require a restart. Counted users are <span class="mono">status='active'</span>, not deleted, and not platform system administrators.</p>`;
}

function tenantSeatMarkup(license, byTenant) {
  if (license?.tenant && LOADED_STATES.has(license?.state)) {
    return ui.emptyState(
      'Tenant-scoped license',
      `This license counts and enforces seats only for tenant ${license.tenant}. Other tenants are not counted or limited by it, and per-tenant rows are intentionally hidden so other usage cannot leak here.`,
      docsLink(),
      'users',
    );
  }
  const rows = Array.isArray(byTenant) ? byTenant : [];
  if (!rows.length) {
    return ui.emptyState('No tenant seat data', 'Per-tenant usage appears when tenants have active users.', '', 'users');
  }
  const total = rows.reduce((sum, row) => sum + Math.max(0, Number(row.activeSeats) || 0), 0);
  return `<div class="table-wrap"><table class="data compact">
      <caption class="sr-only">Active seats by tenant</caption>
      <thead><tr><th scope="col">Tenant</th><th scope="col">Tenant ID</th><th scope="col" class="num">Active seats</th></tr></thead>
      <tbody>${rows.map((row) => `<tr>
        <td>${esc(row.name || 'Unnamed tenant')}</td>
        <td class="mono break-all">${esc(row.tenantId || '—')}</td>
        <td class="num">${fmt.num(row.activeSeats || 0)}</td>
      </tr>`).join('')}</tbody>
      <tfoot><tr><th scope="row" colspan="2">Total</th><td class="num">${fmt.num(total)}</td></tr></tfoot>
    </table></div>
    <p class="form-note">Rows are ordered by tenant name. ${license?.seatSource === 'community' ? 'The community allowance' : 'This license'} counts active users across every tenant.</p>`;
}

const INSTALL_PHASES = [
  ['requesting', 'Authorize the release download'],
  ['downloading', 'Download and checksum the signed release'],
  ['installing', 'Install binaries and restart services'],
  ['complete', 'Enterprise edition active'],
];

function installProgressMarkup(job) {
  const phase = String(job?.phase || 'requesting');
  const order = INSTALL_PHASES.map(([key]) => key);
  const index = order.indexOf(phase === 'error' ? 'requesting' : phase);
  const failed = phase === 'error';
  const items = INSTALL_PHASES.map(([key, label], i) => {
    const done = !failed && index > i || phase === 'complete';
    const active = !failed && index === i && phase !== 'complete';
    const iconName = done ? 'check-circle' : active ? 'refresh' : 'circle-dot';
    const tone = done ? 'good' : active ? 'info' : 'neutral';
    return `<li class="check-item"><span class="check-dot ${tone}">${ui.icon(iconName, 13)}</span>${esc(label)}</li>`;
  }).join('');
  const detail = failed
    ? ui.inlineNotice('critical', 'Enterprise install failed', esc(job.error || job.message || 'The install did not complete.'))
    : phase === 'complete'
      ? ui.inlineNotice('good', 'Enterprise edition installed', esc(job.message || 'The enterprise release is active.'))
      : ui.inlineNotice('info', 'Installing Alt8Mail Enterprise', esc(job.message || 'Working…'));
  return `<div class="stack"><ul class="check-list">${items}</ul>${detail}</div>`;
}

function openEnterpriseInstall(ctx, license, reload) {
  let timer = null;
  let closed = false;
  let retries = 0;
  const modal = ui.openModal({
    title: 'Activate Alt8Mail Enterprise',
    subtitle: 'Your license is valid. This server is still running the community build.',
    size: 'small',
    body: `<div class="stack">
      <p class="form-note">Control will fetch the signed enterprise release from the licensing portal, verify its checksum and Ed25519 signature, install it beside the current release and restart the services. If any step fails, the community release keeps running.</p>
      <div id="enterprise-install-progress">${installProgressMarkup({ phase: 'requesting', message: 'Starting the installer…' })}</div>
    </div>`,
    footer: `<button type="button" class="btn btn-ghost" data-modal-close>Close</button><button type="button" class="btn btn-primary" id="enterprise-install-start">${ui.icon('sparkles', 15)} Install enterprise release</button>`,
    onClose: () => { closed = true; if (timer) clearTimeout(timer); },
  });
  const host = modal.root.querySelector('#enterprise-install-progress');
  const button = modal.root.querySelector('#enterprise-install-start');
  const setBusy = (busy) => {
    if (!button) return;
    button.disabled = busy;
    button.innerHTML = busy ? `${ui.icon('refresh', 15)} Installing…` : `${ui.icon('sparkles', 15)} Install enterprise release`;
  };
  const poll = async () => {
    if (closed) return;
    try {
      const job = await ctx.api('/control/api/license/upgrade/status');
      if (closed) return;
      retries = 0;
      if (host) host.innerHTML = installProgressMarkup(job);
      if (job.phase === 'complete') {
        setBusy(false);
        if (button) button.hidden = true;
        ui.toast(`Alt8Mail Enterprise ${job.version || ''} installed.`, 'good');
        setTimeout(() => { if (!closed) modal.close(); }, 1200);
        if (reload) await reload();
        return;
      }
      if (job.phase === 'error') {
        setBusy(false);
        return;
      }
    } catch (error) {
      if (closed || error?.name === 'AbortError') return;
      // The installer restarts the control service, so a short run of
      // network/session errors is expected. Keep reconnecting instead of
      // reporting a failed install.
      const transient = !error?.status || error.status >= 500 || error.code === 'UNAUTHENTICATED';
      if (transient && retries < 20) {
        retries += 1;
        if (host) host.innerHTML = installProgressMarkup({ phase: 'installing', message: 'Control is restarting; reconnecting to the installer…' });
        timer = setTimeout(poll, 3000);
        return;
      }
      if (host) host.innerHTML = installProgressMarkup({ phase: 'error', error: error.message || 'Could not read the install status.' });
      setBusy(false);
      return;
    }
    timer = setTimeout(poll, 2500);
  };
  const start = async () => {
    setBusy(true);
    if (host) host.innerHTML = installProgressMarkup({ phase: 'requesting', message: 'Authorizing the release download…' });
    try {
      await ctx.api('/control/api/license/enterprise/install', { method: 'POST', body: {} });
      poll();
    } catch (error) {
      if (host) host.innerHTML = installProgressMarkup({ phase: 'error', error: error.message || 'Could not start the install.' });
      setBusy(false);
    }
  };
  button?.addEventListener('click', start);
  start();
}

function binaryMismatchNotice(license) {
  if (!license || license.binaryEdition === 'enterprise') return '';
  if (license.edition === 'oss' || !LOADED_STATES.has(license.state)) return '';
  return ui.inlineNotice(
    'warning',
    'Enterprise license installed, community binary running',
    'This token unlocks enterprise entitlements, but the running binaries are the community build. Install the enterprise release to activate shared inboxes, SSO, SCIM, migration, AI and the other paid capabilities.',
    `<button class="btn btn-primary btn-sm" type="button" data-install-enterprise>${ui.icon('sparkles', 13)} Install enterprise release</button>`,
  );
}

function uploadSuccessMarkup(result) {
  const license = result?.license || {};
  const restart = result?.restartRequired !== false;
  const mismatch = result?.enterpriseInstallRequired
    ? `<p><strong>This server is still running the community binary.</strong> The license is valid; install the enterprise release below to activate its paid capabilities.</p>`
    : '';
  return `<div class="notice tone-good" role="status">
      ${ui.icon('check-circle', 17)}
      <div>
        <strong>License uploaded and verified</strong>
        ${mismatch}
        <p>${restart ? 'Restart the Alt8Mail webmail service to activate this license.' : 'The new license is ready to use.'}</p>
        <p>Seat usage below updates immediately. The running webmail keeps using the token it loaded at startup until it restarts.</p>
        <p>Control does not validate <span class="mono">ALT8MAIL_LICENSE_TENANT</span>. In a tenant-pinned deployment, a token whose tenant claim differs from the webmail pin will be rejected by the webmail after restart.</p>
        <p class="mono">${esc(editionLabel(license.edition))} · ${esc(stateMeta(license.state).label)} · ${fmt.num(license.seats || 0)} seats · expires ${esc(license.expiresAt ? fmt.dateOnly(license.expiresAt) : '—')}</p>
      </div>
    </div>`;
}

function uploadErrorMarkup(message) {
  return `<div class="notice tone-critical" role="alert">
      ${ui.icon('alert-circle', 17)}
      <div><strong>License upload failed</strong><p>${esc(message)}</p></div>
    </div>`;
}

function uploadValidationMarkup(message) {
  return `<div class="notice tone-warning" role="alert">
      ${ui.icon('alert', 17)}
      <div><strong>Check the license input</strong><p>${esc(message)}</p></div>
    </div>`;
}

function uploadErrorText(error) {
  if (error?.name === 'AbortError') return '';
  const code = String(error?.code || '');
  if (UPLOAD_ERROR_TEXT[code]) return UPLOAD_ERROR_TEXT[code];
  if (error?.status === 413) return UPLOAD_ERROR_TEXT.LICENSE_TOO_LARGE;
  const status = Number(error?.status);
  return `The license upload could not be completed${status ? ` (HTTP ${status})` : ''}. Check the Control service log and try again.`;
}

export async function renderLicense(ctx) {
  ctx.setTitle('License', 'Infrastructure');
  let license = { configured: false, filePath: '', state: 'unlicensed', edition: 'oss', features: [] };
  let byTenant = [];

  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Infrastructure',
    title: 'License',
    description: 'See the community or licensed seat limit, monitor live usage, compare paid capabilities and upload a signed license.',
    meta: `<span class="icon-text">${ui.icon('key', 13)} Offline Ed25519 verification · no network call</span>`,
    actions: `<button class="btn btn-secondary" type="button" id="license-refresh">${ui.icon('refresh', 15)} Refresh</button>${docsLink()}`,
  })}
    <div class="grid grid-main">
      <div class="stack">
        <section class="card">
          <header class="card-head"><div><h3>License status</h3><p>Read from the shared token file on every request</p></div><span id="license-state-chip"></span></header>
          <div class="card-body" id="license-status-body">${ui.loadingState('Reading license state…', 4)}</div>
        </section>
        <section class="card">
          <header class="card-head"><div><h3>Seat usage</h3><p>Live active-user count against the licensed allowance</p></div><span id="license-seat-chip"></span></header>
          <div class="card-body" id="license-seat-body">${ui.loadingState('Counting active seats…', 3)}</div>
        </section>
        <section class="card">
          <header class="card-head"><div><h3>Per-tenant seats</h3><p>Shown for global licenses only</p></div></header>
          <div class="card-body" id="license-tenant-body">${ui.loadingState('Reading tenant usage…', 3)}</div>
        </section>
      </div>
      <div class="stack">
        <section class="card" id="license-upgrade-card">
          <header class="card-head"><div><h3>One-click upgrade</h3><p>Install the signed license and private enterprise release</p></div>${ui.badge('Portal', 'info', 'sparkles')}</header>
          <div class="card-body">
            <p class="form-note">Start a trial or purchase in the licensing portal. Alt8Mail claims the signed license automatically, installs the enterprise release and restarts the services.</p>
            <div id="license-upgrade-status" aria-live="polite"><p class="form-note">No upgrade in progress.</p></div>
            <div class="form-actions"><span class="form-note">Requires the signing portal and a published enterprise bundle.</span><div class="form-actions-right"><button class="btn btn-primary" type="button" id="license-upgrade-start">${ui.icon('sparkles', 15)} Upgrade to Enterprise</button></div></div>
          </div>
        </section>
        <section class="card" id="license-upload-card">
          <header class="card-head"><div><h3>Upload license</h3><p>Validate and install a signed token</p></div>${ui.badge('64 KiB max', 'neutral', 'upload')}</header>
          <div class="card-body">
            <form id="license-upload-form" novalidate>
              <div class="field">
                <label for="license-file">License file</label>
                <input class="input" id="license-file" name="licenseFile" type="file" accept=".token,.a8m,.txt,text/plain" aria-describedby="license-file-help">
                <small class="field-help" id="license-file-help">Select the signed <span class="mono">A8M1.…</span> token file. A selected file takes precedence over pasted text. Maximum 64 KiB.</small>
                <small class="field-help" id="license-file-name"></small>
              </div>
              <div class="divider"></div>
              <div class="field">
                <label for="license-token">Or paste the token</label>
                <textarea class="input mono" id="license-token" name="token" rows="5" autocomplete="off" spellcheck="false" placeholder="A8M1.…" aria-describedby="license-token-help"></textarea>
                <small class="field-help" id="license-token-help">Paste the complete signed token. It is sent once to this server; the page never displays it back, stores it in browser storage or logs it.</small>
              </div>
              <div id="license-upload-result" aria-live="polite"></div>
              <div class="form-actions">
                <span class="form-note">A successful upload does not change the running webmail until it restarts. Existing users are never deactivated by an upload.</span>
                <div class="form-actions-right"><button class="btn btn-primary" type="submit" id="license-upload-submit">${ui.icon('upload', 15)} Validate and upload</button></div>
              </div>
            </form>
            <div class="divider"></div>
            <p class="form-note license-file-note">Configured path: <span class="mono break-all" id="license-upload-path">—</span></p>
            <p class="form-note">The webmail reads the same token through <span class="mono">ALT8MAIL_LICENSE_FILE</span> and verifies it with <span class="mono">ALT8MAIL_LICENSE_PUBLIC_KEY</span>. Both services must point at the same path/key. Control does not validate <span class="mono">ALT8MAIL_LICENSE_TENANT</span>; a tenant-pinned webmail rejects a token with a different claim after restart.</p>
            <p class="form-note">The Control Plane writes the token atomically with mode <span class="mono">0640</span> and normalizes its group to <span class="mono">a8m</span> (or <span class="mono">CONTROL_LICENSE_GROUP</span>). An existing file's owner is preserved, so a legacy root-owned token becomes webmail-readable after the upload. A rejected upload leaves the existing file byte-for-byte unchanged.</p>
          </div>
        </section>
        <section class="card">
          <header class="card-head"><div><h3>How licensing works</h3><p>Shared file, ownership and restart rules</p></div></header>
          <div class="card-body">
            <div class="tile-link"><span>${ui.icon('file-code', 15)} Shared file</span><span class="value mono">CONTROL_LICENSE_FILE = ALT8MAIL_LICENSE_FILE</span></div>
            <div class="tile-link"><span>${ui.icon('lock', 15)} Ownership</span><span class="value">group a8m · 0640</span></div>
            <div class="tile-link"><span>${ui.icon('refresh', 15)} Activation</span><span class="value">Webmail restart required</span></div>
            <div class="tile-link"><span>${ui.icon('users', 15)} Seat counting</span><span class="value">Active · non-deleted · non-admin</span></div>
            <div class="tile-link"><span>${ui.icon('shield', 15)} Community limit</span><span class="value">25 active users, enforced in webmail, admin, SCIM and Control</span></div>
            <div class="tile-link"><span>${ui.icon('activity', 15)} Verification</span><span class="value">Offline Ed25519 · compiled trust anchor</span></div>
            <div class="note-strip" style="margin-top:11px">${ui.icon('info', 13)} <span>The webmail reads and verifies the token at startup. Control re-reads the same path on every status request, so uploaded state and seat usage appear here immediately. Restarting the webmail does not affect queued mail.</span></div>
            <p class="form-note" style="margin-top:10px"><a href="${DOCS_URL}" target="_blank" rel="noopener noreferrer">Read the full licensing guide ${ui.icon('arrow-up-right', 11)}</a></p>
          </div>
        </section>
      </div>
    </div>`;

  const statusBody = document.getElementById('license-status-body');
  statusBody?.addEventListener('click', (event) => {
    if (event.target.closest('[data-install-enterprise]')) openEnterpriseInstall(ctx, license, load);
  });
  const seatBody = document.getElementById('license-seat-body');
  const tenantBody = document.getElementById('license-tenant-body');
  const stateChip = document.getElementById('license-state-chip');
  const seatChip = document.getElementById('license-seat-chip');
  const uploadPath = document.getElementById('license-upload-path');
  const fileInput = document.getElementById('license-file');
  const tokenInput = document.getElementById('license-token');
  const fileName = document.getElementById('license-file-name');
  const resultHost = document.getElementById('license-upload-result');
  const submit = document.getElementById('license-upload-submit');
  const upgradeStatus = document.getElementById('license-upgrade-status');
  const upgradeStart = document.getElementById('license-upgrade-start');
  let upgradeTimer = null;

  const renderUpgrade = (job) => {
    if (!job || !job.phase || job.phase === 'idle') {
      if (upgradeStatus) upgradeStatus.innerHTML = '<p class="form-note">No upgrade in progress.</p>';
      return;
    }
    const tone = job.phase === 'error' ? 'critical' : job.phase === 'complete' ? 'success' : 'info';
    const title = job.phase === 'error' ? 'Upgrade failed' : job.phase === 'complete' ? 'Upgrade complete' : 'Upgrade in progress';
    if (upgradeStatus) {
      upgradeStatus.innerHTML = ui.inlineNotice(tone, title, esc(job.message || job.error || job.phase)) +
        (job.licenseId ? `<p class="form-note mono">${esc(job.licenseId)}${job.version ? ' · ' + esc(job.version) : ''}</p>` : '');
    }
    if (upgradeTimer) { clearTimeout(upgradeTimer); upgradeTimer = null; }
    if (job.phase === 'complete') { setTimeout(load, 1200); return; }
    if (job.phase !== 'error') upgradeTimer = setTimeout(pollUpgrade, 3000);
  };

  const pollUpgrade = async () => {
    try {
      const job = await ctx.api('/control/api/license/upgrade/status');
      renderUpgrade(job);
    } catch (error) {
      if (upgradeStatus) upgradeStatus.innerHTML = ui.inlineNotice('warning', 'Upgrade status unavailable', esc(error.message || 'Could not read the upgrade status.'));
    }
  };

  upgradeStart?.addEventListener('click', async () => {
    upgradeStart.disabled = true;
    let portalTab = null;
    try {
      portalTab = window.open('about:blank', '_blank');
      if (portalTab) portalTab.opener = null;
    } catch (_) { portalTab = null; }
    try {
      const data = await ctx.api('/control/api/license/upgrade/start', { method: 'POST', body: {} });
      if (data?.portalUrl) {
        if (portalTab) portalTab.location.href = data.portalUrl;
        else window.open(data.portalUrl, '_blank', 'noopener');
        if (upgradeStatus) {
          upgradeStatus.innerHTML = ui.inlineNotice('info', 'Portal opened', 'Complete the trial or purchase in the licensing portal. This page updates automatically.') +
            `<p class="form-note"><a href="${esc(data.portalUrl)}" target="_blank" rel="noopener noreferrer">Open the licensing portal</a></p>`;
        }
      } else if (upgradeStatus) {
        upgradeStatus.innerHTML = ui.inlineNotice('warning', 'Portal link unavailable', 'The upgrade started but no portal URL was returned.');
      }
      upgradeTimer = setTimeout(pollUpgrade, 2500);
    } catch (error) {
      if (portalTab) portalTab.close();
      if (upgradeStatus) upgradeStatus.innerHTML = ui.inlineNotice('critical', 'Could not start the upgrade', esc(error.message || 'Try again.'));
    } finally {
      upgradeStart.disabled = false;
    }
  });

  const paint = () => {
    const upgradeCard = document.getElementById('license-upgrade-card');
    if (upgradeCard) upgradeCard.hidden = license?.binaryEdition === 'enterprise';
    if (statusBody) statusBody.innerHTML = statusMarkup(license);
    if (seatBody) seatBody.innerHTML = seatMarkup(license);
    if (tenantBody) tenantBody.innerHTML = tenantSeatMarkup(license, byTenant);
    if (uploadPath) uploadPath.textContent = filePathText(license);
    if (stateChip) stateChip.innerHTML = statePill(license?.state);
    if (seatChip) {
      const hasSeats = Number(license?.seats) > 0;
      seatChip.innerHTML = hasSeats ? ui.badge(`${fmt.num(license.usedSeats || 0)} / ${fmt.num(license.seats || 0)}`, seatTone(license, Number(license.usedSeats) || 0, Number(license.seats) || 0), 'users') : '';
    }
  };

  const load = async () => {
    const payload = await ctx.api('/control/api/license');
    if (!ctx.isCurrent()) return;
    license = payload?.license || { configured: false, filePath: '', state: 'unlicensed', edition: 'oss', features: [] };
    byTenant = Array.isArray(payload?.byTenant) ? payload.byTenant : [];
    paint();
  };

  const setBusy = (busy) => {
    if (!submit) return;
    submit.disabled = busy;
    submit.classList.toggle('is-loading', busy);
    submit.innerHTML = busy ? `${ui.icon('refresh', 15)} Uploading…` : `${ui.icon('upload', 15)} Validate and upload`;
  };

  const clearInputs = () => {
    if (fileInput) fileInput.value = '';
    if (tokenInput) tokenInput.value = '';
    if (fileName) fileName.textContent = '';
  };

  fileInput?.addEventListener('change', () => {
    const file = fileInput.files?.[0];
    if (fileName) fileName.textContent = file ? `${file.name} · ${fmt.bytes(file.size)}` : '';
  });

  document.getElementById('license-upload-form')?.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (submit?.disabled) return;
    if (resultHost) resultHost.innerHTML = '';
    setBusy(true);
    let token = '';
    try {
      const file = fileInput?.files?.[0];
      if (file) {
        if (file.size > MAX_TOKEN_BYTES) {
          if (resultHost) resultHost.innerHTML = uploadValidationMarkup(UPLOAD_ERROR_TEXT.LICENSE_TOO_LARGE);
          return;
        }
        token = await file.text();
      } else {
        token = tokenInput?.value || '';
      }
      token = token.trim();
      if (!token) {
        if (resultHost) resultHost.innerHTML = uploadValidationMarkup('Choose a license file or paste a token.');
        return;
      }
      if (new TextEncoder().encode(token).length > MAX_TOKEN_BYTES) {
        if (resultHost) resultHost.innerHTML = uploadValidationMarkup(UPLOAD_ERROR_TEXT.LICENSE_TOO_LARGE);
        return;
      }
      const result = await ctx.api('/control/api/license', { method: 'POST', body: { token } });
      // Clear the secret immediately after the request resolves, before the
      // follow-up status read or any other work.
      token = '';
      clearInputs();
      if (!ctx.isCurrent()) return;
      if (resultHost) resultHost.innerHTML = uploadSuccessMarkup(result);
      if (result.enterpriseInstallRequired) {
        ui.toast('Enterprise license accepted. Installing the enterprise release…', 'info', { title: 'License updated' });
        openEnterpriseInstall(ctx, result.license || {}, load);
      } else {
        ui.toast('License uploaded. Restart the Alt8Mail webmail service to activate it.', 'good', { title: 'License updated' });
      }
      try {
        await load();
      } catch {
        // The upload succeeded; a transient status refresh failure must not
        // turn the success panel into an error. The next refresh retries.
      }
    } catch (error) {
      if (error?.name === 'AbortError') return;
      if (resultHost) resultHost.innerHTML = uploadErrorMarkup(uploadErrorText(error));
    } finally {
      token = '';
      clearInputs();
      setBusy(false);
    }
  });

  const refresh = async (event) => {
    const button = event?.currentTarget;
    if (button) {
      button.classList.add('is-loading');
      button.disabled = true;
    }
    try {
      await load();
      ui.toast('License status refreshed.', 'good');
    } catch (error) {
      ctx.handleError(error, 'Could not refresh license status');
    } finally {
      if (button) {
        button.classList.remove('is-loading');
        button.disabled = false;
      }
    }
  };
  document.getElementById('license-refresh')?.addEventListener('click', refresh);

  try {
    await load();
  } catch (error) {
    if (error?.name === 'AbortError') return;
    if (statusBody) statusBody.innerHTML = ui.inlineNotice('critical', 'Could not read the license status', error?.message || String(error), docsLink('Licensing guide'));
    if (seatBody) seatBody.innerHTML = '';
    if (tenantBody) tenantBody.innerHTML = '';
  }
}
