/* Disk Management — live capacity, placement and storage operations. */
import * as ui from './ui.js';

const esc = ui.esc;
const fmt = {
  bytes: ui.bytes,
  pct: ui.pct,
  num: ui.num,
  timeAgo: ui.timeAgo,
  dateTime: ui.dateTime,
};

function errorText(error) {
  return error?.message || String(error || 'Unknown error');
}

function volumeTone(volume) {
  if (volume.state === 'retired' || volume.state === 'wiped') return 'neutral';
  if (!volume.online) return 'critical';
  if (Number(volume.usedPercent) >= 95) return 'critical';
  if (Number(volume.usedPercent) >= 85) return 'warning';
  return 'good';
}

function volumeState(volume) {
  if (volume.state === 'retired') return ui.statusPill('neutral', 'Retired');
  if (volume.state === 'wiped') return ui.statusPill('neutral', 'Wiped');
  if (volume.state === 'provisioning') return ui.statusPill('warning', 'Provisioning');
  if (volume.state === 'reboot_pending') return ui.statusPill('warning', 'Reboot pending');
  if (volume.state === 'retiring') return ui.statusPill('warning', 'Retiring');
  if (volume.state === 'error') return ui.statusPill('failed', 'Error');
  if (!volume.online) return ui.statusPill('failed', 'Offline');
  return ui.statusPill('active', 'Online');
}

function volumeRole(volume) {
  const topology = String(volume.topology || '').toLowerCase();
  if (topology === 'nfs') return 'NFS mail volume';
  if (topology === 'raid') return 'RAID mail volume';
  if (topology === 'multipath') return 'Multipath mail volume';
  if (topology === 'mapped') return 'Mapped mail volume';
  const fsType = String(volume.fsType || '').toLowerCase();
  if (fsType === 'nfs' || fsType === 'nfs4') return 'NFS mail volume';
  if (String(volume.devicePath || '').startsWith('/dev/md')) return 'RAID mail volume';
  if (String(volume.devicePath || '').includes('mpath')) return 'Multipath mail volume';
  if (String(volume.devicePath || '').startsWith('/dev/mapper/')) return 'Mapped mail volume';
  if (volume.name === 'legacy') return 'Legacy mail root';
  if (volume.createdBy === 'discovered') return 'Discovered mount';
  if (volume.managed) return 'Managed mail volume';
  return 'Mail volume';
}

function topologyLabel(device) {
  const labels = { local: 'Local disk', lvm: 'LVM PV', raid: 'RAID', luks: 'LUKS', multipath: 'Multipath', rom: 'Optical', loop: 'Loop' };
  return labels[device.topology] || device.topology || device.type;
}

function volumeFilterKey(volume) {
  if (volume.state === 'retired') return 'retired';
  if (volume.state === 'wiped') return 'wiped';
  if (!volume.online) return 'offline';
  if (['advisory', 'warning', 'serious', 'critical', 'emergency'].includes(String(volume.capacityState || ''))) return 'attention';
  if (volume.state && !['online', 'ready'].includes(volume.state)) return 'attention';
  return 'online';
}

function volumeMatchesFilter(volume, filter) {
  if (filter === 'all') return true;
  if (filter === 'online') return Boolean(volume.online);
  if (filter === 'attention') return Boolean(volume.online) && volumeFilterKey(volume) === 'attention';
  return volumeFilterKey(volume) === filter;
}

function volumeFilterLabel(key) {
  return ({ all: 'All volumes', online: 'Online', attention: 'Needs attention', offline: 'Offline', retired: 'Retired', wiped: 'Wiped' })[key] || 'All volumes';
}

function driveVisual(volume, tone = 'good') {
  const pct = Math.max(0, Math.min(100, Number(volume.usedPercent) || 0));
  const fill = pct <= 0 ? 0 : Math.max(4, 54 * pct / 100);
  const offline = !volume.online || volume.state === 'retired' || volume.state === 'wiped';
  return `<div class="storage-drive-visual tone-${tone}${offline ? ' is-offline' : ''}" aria-hidden="true">
    <svg viewBox="0 0 88 70" focusable="false">
      <rect class="drive-case" x="4" y="7" width="80" height="56" rx="10"></rect>
      <rect class="drive-face" x="10" y="13" width="68" height="26" rx="5"></rect>
      <circle class="drive-spindle" cx="21" cy="26" r="3.2"></circle>
      <rect class="drive-slot" x="30" y="22" width="40" height="3" rx="1.5"></rect>
      <rect class="drive-slot is-soft" x="30" y="29" width="27" height="3" rx="1.5"></rect>
      <rect class="drive-track" x="13" y="48" width="54" height="6" rx="3"></rect>
      <rect class="drive-fill" x="13" y="48" width="${fill.toFixed(1)}" height="6" rx="3"></rect>
      <circle class="drive-led${offline ? '' : ' is-on'}" cx="74" cy="51" r="3.2"></circle>
    </svg>
  </div>`;
}

function flattenDevices(list, root = null, out = []) {
  for (const device of (list || [])) {
    const parent = root || device;
    out.push({ device, root: parent });
    flattenDevices(device.children || [], parent, out);
  }
  return out;
}

function findVolumeDisk(volume, inventory) {
  if (!inventory || !volume.devicePath) return null;
  const match = flattenDevices(inventory.devices || []).find((entry) => entry.device.path === volume.devicePath);
  if (!match) return null;
  return { device: match.device, root: match.root };
}

function healthTone(health) {
  if (!health) return 'neutral';
  if (health.state === 'ok') return 'good';
  if (health.state === 'warning') return 'warning';
  if (health.state === 'critical') return 'critical';
  return 'neutral';
}

function levelTone(level) {
  if (level === 'emergency' || level === 'critical') return 'critical';
  if (level === 'serious' || level === 'warning') return 'warning';
  if (level === 'advisory') return 'info';
  if (level === 'projected_full') return 'warning';
  return 'neutral';
}

function fmtDays(days) {
  if (days === null || days === undefined) return 'No projection';
  if (Number(days) <= 0) return 'Now';
  if (Number(days) < 1) return '<1 day';
  return `${ui.num(Math.round(Number(days)))} day${Math.round(Number(days)) === 1 ? '' : 's'}`;
}

function forecastChart(forecast, totalBytes) {
  const points = Array.isArray(forecast.points) ? forecast.points.filter((point) => Number.isFinite(Date.parse(point.at))) : [];
  if (points.length < 2) {
    return `<div class="storage-forecast-empty">${ui.icon('activity', 16)}<span>Not enough daily history yet. The forecast appears after two days of samples.</span></div>`;
  }
  const width = 620, height = 170, left = 42, right = 12, top = 12, bottom = 24;
  const actual = points.filter((point) => !point.projected);
  const lastActual = actual[actual.length - 1];
  const projected = lastActual ? [lastActual, ...points.filter((point) => point.projected)] : [];
  const times = points.map((point) => Date.parse(point.at));
  const minTime = Math.min(...times), maxTime = Math.max(...times);
  const span = Math.max(1, maxTime - minTime);
  const maxUsed = Math.max(Number(totalBytes) || 0, ...points.map((point) => Number(point.usedBytes) || 0), 1);
  const x = (at) => left + ((Date.parse(at) - minTime) / span) * (width - left - right);
  const y = (used) => top + (1 - Math.min(1, (Number(used) || 0) / maxUsed)) * (height - top - bottom);
  const path = (list) => list.map((point, index) => `${index ? 'L' : 'M'}${x(point.at).toFixed(1)},${y(point.usedBytes).toFixed(1)}`).join(' ');
  const actualPath = actual.length > 1 ? path(actual) : '';
  const projectedPath = projected.length > 1 ? path(projected) : '';
  const thresholds = (forecast.thresholds || []).filter((threshold) => ['warning', 'serious', 'critical', 'emergency'].includes(threshold.level));
  const grid = [0, 25, 50, 75, 100].map((pct) => {
    const yPos = y(maxUsed * pct / 100);
    return `<line x1="${left}" y1="${yPos.toFixed(1)}" x2="${width - right}" y2="${yPos.toFixed(1)}" class="storage-chart-grid"></line><text x="${left - 7}" y="${(yPos + 3).toFixed(1)}" text-anchor="end" class="storage-chart-axis">${pct}%</text>`;
  }).join('');
  const thresholdLines = thresholds.map((threshold) => {
    const yPos = y(Number(threshold.bytes) || 0);
    return `<line x1="${left}" y1="${yPos.toFixed(1)}" x2="${width - right}" y2="${yPos.toFixed(1)}" class="storage-chart-threshold tone-${levelTone(threshold.level)}"></line>`;
  }).join('');
  const dateLabel = (value) => new Date(value).toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
  const axisDates = `<text x="${left}" y="${height - 5}" text-anchor="start" class="storage-chart-axis">${esc(dateLabel(points[0].at))}</text><text x="${(left + width - right) / 2}" y="${height - 5}" text-anchor="middle" class="storage-chart-axis">${esc(dateLabel(points[Math.floor((points.length - 1) / 2)].at))}</text><text x="${width - right}" y="${height - 5}" text-anchor="end" class="storage-chart-axis">${esc(dateLabel(points[points.length - 1].at))}</text>`;
  return `<svg class="storage-forecast-chart" viewBox="0 0 ${width} ${height}" role="img" aria-label="Capacity forecast">
    <title>Capacity forecast</title>
    ${grid}${thresholdLines}${axisDates}
    ${projectedPath ? `<path d="${projectedPath}" class="storage-chart-projected"></path>` : ''}
    ${actualPath ? `<path d="${actualPath}" class="storage-chart-actual"></path>` : ''}
    <circle cx="${x(actual[actual.length - 1]?.at || points[0].at).toFixed(1)}" cy="${y(actual[actual.length - 1]?.usedBytes || points[0].usedBytes).toFixed(1)}" r="3.5" class="storage-chart-dot"></circle>
  </svg>
  <div class="storage-chart-key"><span><i class="is-actual"></i>Actual</span><span><i class="is-projected"></i>Projected</span><span><i class="is-threshold"></i>Policy threshold</span></div>`;
}

export async function renderStorage(ctx) {
  const canManage = ctx.can('storage.manage');
  let overview = null;
  let placements = null;
  let disks = null;
  let operations = null;
  let hostInfo = null;
  let usage = null;
  let swapInfo = null;
  let forecastVolumeId = '';
  let volumeFilter = 'all';
  const operationsState = { page: 1, limit: 10, sort: 'createdAt', order: 'desc' };
  let loading = false;
  let timer = null;
  let usageTimer = null;

  ctx.setTitle('Disk Management', 'Infrastructure');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Infrastructure',
    title: 'Disk Management',
    description: 'Live capacity for every mail volume, explicit mailbox placement, and safe storage operations. Existing mail stays on the legacy root until an administrator moves it.',
    actions: `<button class="btn btn-secondary" type="button" id="storage-refresh">${ui.icon('refresh', 15)} Refresh</button>
      <button class="btn btn-secondary" type="button" id="storage-policy-open">${ui.icon('sliders', 14)} Capacity policy</button>
      ${canManage ? `<button class="btn btn-secondary" type="button" id="storage-move" disabled title="Loading placement policy">${ui.icon('forward', 14)} Move mail</button>` : ''}
      ${canManage ? `<button class="btn btn-primary" type="button" id="storage-add-disk" disabled title="Loading host capabilities">${ui.icon('plus', 14)} Add disk</button>` : ''}`,
  })}
    <div id="storage-notice"></div>
    <div id="storage-alerts"></div>
    <div id="storage-summary">${ui.loadingState('Reading filesystem capacity…', 3)}</div>
    <section class="card storage-outlook-card">
      <header class="card-head"><div><h3>Capacity outlook</h3><p>Daily history and a 30-day linear projection. Planning aid, not a promise.</p></div><div class="card-actions" id="storage-forecast-actions"></div></header>
      <div class="card-body" id="storage-forecast">${ui.loadingState('Building the capacity forecast…', 4)}</div>
    </section>
    <section class="card storage-volumes-card">
      <header class="card-head storage-volumes-head">
        <div><h3>Storage volumes</h3><p id="storage-volume-subtitle">Registered mail filesystems and their live capacity</p></div>
        <div class="card-actions storage-volumes-tools">
          <label class="storage-filter"><span>Status</span><select class="input input-sm" id="storage-volume-filter" aria-label="Filter storage volumes by status"><option value="all">All volumes</option></select></label>
          <span class="mono muted" id="storage-sampled"></span>
        </div>
      </header>
      <div class="card-body" id="storage-volumes">${ui.loadingState('Reading storage volumes…', 5)}</div>
    </section>
    <div class="grid grid-main">
      <section class="card">
        <header class="card-head"><div><h3>Placement</h3><p>Effective volume for new mailboxes. Existing mail is moved only by an explicit job.</p></div><div class="card-actions" id="storage-placement-actions"></div></header>
        <div class="card-body" id="storage-placement">${ui.loadingState('Loading placement policy…', 4)}</div>
      </section>
      <section class="card">
        <header class="card-head"><div><h3>Storage doctor</h3><p>Checks that Dovecot and the mail root are ready for placement</p></div><div class="card-actions"><button class="btn btn-ghost btn-sm" type="button" id="storage-doctor-refresh">${ui.icon('refresh', 13)} Recheck</button></div></header>
        <div class="card-body" id="storage-doctor">${ui.loadingState('Checking storage prerequisites…', 3)}</div>
      </section>
    </div>
    <div class="grid grid-main">
      <section class="card">
        <header class="card-head"><div><h3>Largest consumers</h3><p>Cached mailbox and domain sizes from the nightly low-I/O scan</p></div><div class="card-actions">${canManage ? `<button class="btn btn-ghost btn-sm" type="button" id="storage-scan-usage">${ui.icon('refresh', 13)} Run scan</button>` : ''}</div></header>
        <div class="card-body" id="storage-usage">${ui.loadingState('Loading mailbox usage…', 3)}</div>
      </section>
      <section class="card">
        <header class="card-head"><div><h3>Host provisioning</h3><p>Root-level disk operations with a superadmin gate</p></div><div class="card-actions"><button class="btn btn-ghost btn-sm" type="button" id="storage-host-refresh">${ui.icon('refresh', 13)} Recheck</button></div></header>
        <div class="card-body" id="storage-host">${ui.loadingState('Checking host capabilities…', 3)}</div>
      </section>
    </div>
    <section class="card storage-swap-card">
      <header class="card-head">
        <div><h3>Swap</h3><p>Host memory overflow space with explicit boot persistence</p></div>
        <div class="card-actions">
          <button class="btn btn-ghost btn-sm" type="button" id="storage-swap-refresh">${ui.icon('refresh', 13)} Refresh</button>
          ${canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="storage-swap-create" disabled title="Loading swap status">${ui.icon('plus', 13)} Create swap</button>` : ''}
        </div>
      </header>
      <div class="card-body" id="storage-swap">${ui.loadingState('Reading host swap…', 3)}</div>
    </section>
    <section class="card storage-disks-card">
      <header class="card-head"><div><h3>Physical disks</h3><p>Read-only host inventory used by the add-disk planner</p></div><div class="card-actions" id="storage-disk-actions"></div></header>
      <div class="card-body" id="storage-disks">${ui.loadingState('Reading block devices…', 4)}</div>
    </section>
    <section class="card storage-operations-card">
      <header class="card-head"><div><h3>Storage operations</h3><p>Verified moves, host operations and rollback windows</p></div><div class="card-actions"><button class="btn btn-secondary btn-sm" type="button" id="storage-operations-refresh">${ui.icon('refresh', 13)} Refresh</button></div></header>
      <div class="card-body" id="storage-operations">${ui.loadingState('Loading storage operations…', 3)}</div>
    </section>
    <p class="form-note" style="margin-top:14px">Live values refresh every 5 seconds while this page is visible. Capacity history is sampled every 30 seconds. No disk is formatted or mounted by this page.</p>`;

  const paintSummary = () => {
    if (!overview) return;
    const volumes = overview.volumes || [];
    const active = volumes.filter((volume) => volume.state !== 'retired' && volume.state !== 'wiped');
    const online = active.filter((volume) => volume.online).length;
    const totals = overview.totals || {};
    const doctor = overview.doctor || {};
    const summary = document.getElementById('storage-summary');
    if (!summary) return;
    summary.innerHTML = `<div class="stat-grid">
      ${ui.statTile({ label: 'Managed capacity', value: fmt.bytes(totals.totalBytes), detail: `${active.length} active of ${volumes.length} registered`, iconName: 'disk' })}
      ${ui.statTile({ label: 'Used', value: fmt.bytes(totals.usedBytes), detail: `${fmt.pct(totals.usedPercent)} of managed capacity`, iconName: 'archive', tone: Number(totals.usedPercent) >= 85 ? 'warning' : '' })}
      ${ui.statTile({ label: 'Free', value: fmt.bytes(totals.freeBytes), detail: 'Available to the mail service', iconName: 'download' })}
      ${ui.statTile({ label: 'Volumes online', value: `${online}/${active.length}`, detail: active.length === 0 ? 'No active volumes' : online === active.length ? 'All active mounts readable' : 'An active mount is unavailable', iconName: 'check-circle', tone: active.length === 0 ? '' : online === active.length ? 'good' : 'critical' })}
      ${ui.statTile({ label: 'Dovecot userdb', value: doctor.userdbReady ? 'Ready' : 'Legacy', detail: doctor.userdbReady ? 'Per-mailbox home is honored' : 'Placement is locked', iconName: doctor.userdbReady ? 'shield-check' : 'alert-circle', tone: doctor.userdbReady ? 'good' : 'warning' })}
    </div>`;
    const sampled = document.getElementById('storage-sampled');
    if (sampled) sampled.textContent = overview.generatedAt ? `Sampled ${fmt.timeAgo(overview.generatedAt)}` : '';
  };

  const paintNotice = () => {
    const host = document.getElementById('storage-notice');
    if (!host || !overview) return;
    const doctor = overview.doctor || {};
    const issues = Array.isArray(doctor.issues) ? doctor.issues : [];
    const critical = issues.find((issue) => issue.severity === 'critical');
    const warning = issues.find((issue) => issue.severity === 'warning');
    const issue = critical || warning;
    if (!issue) { host.innerHTML = ''; return; }
    host.innerHTML = ui.inlineNotice(critical ? 'critical' : 'warning', issue.title, issue.message);
  };

  const paintAlerts = () => {
    const host = document.getElementById('storage-alerts');
    if (!host || !overview) return;
    const alerts = Array.isArray(overview.alerts) ? overview.alerts : [];
    if (!alerts.length) { host.innerHTML = ''; return; }
    host.innerHTML = alerts.map((alert) => `<div class="notice tone-${levelTone(alert.level)} storage-alert">
      <span class="notice-icon">${ui.icon(alert.level === 'emergency' || alert.level === 'critical' ? 'alert-circle' : 'alert', 17)}</span>
      <div><strong>${esc(ui.humanTitle(alert.level))} · ${esc(alert.volumeName)}</strong>
        <p>${esc(alert.message || '')}</p>
        <span class="cell-sub">Seen ${esc(fmt.timeAgo(alert.lastSeenAt))}${alert.status === 'acknowledged' ? ' · Acknowledged' : alert.notifyCount ? ` · Alerted ${ui.num(alert.notifyCount)} time${alert.notifyCount === 1 ? '' : 's'}` : ''}</span>
      </div>
      ${canManage && alert.status === 'active' ? `<button class="btn btn-ghost btn-sm" type="button" data-alert-ack="${esc(alert.id)}">${ui.icon('check', 12)} Acknowledge</button>` : ''}
    </div>`).join('');
    host.querySelectorAll('[data-alert-ack]').forEach((button) => button.addEventListener('click', async () => {
      button.disabled = true; button.classList.add('is-loading');
      try {
        await ctx.api(`/control/api/storage/alerts/${encodeURIComponent(button.dataset.alertAck)}/acknowledge`, { method: 'POST' });
        ui.toast('Capacity alert acknowledged. It stays visible until usage drops.', 'good');
        await load();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not acknowledge the alert' });
        button.disabled = false; button.classList.remove('is-loading');
      }
    }));
  };

  const paintForecast = () => {
    const host = document.getElementById('storage-forecast');
    const actions = document.getElementById('storage-forecast-actions');
    if (!host || !overview) return;
    const volumes = (overview.volumes || []).filter((volume) => volume.online && volume.forecast && volume.state !== 'retired' && volume.state !== 'wiped');
    if (!volumes.length) {
      host.innerHTML = ui.emptyState('No forecast yet', 'Capacity history appears after the first samples on an online volume.', '', 'activity');
      if (actions) actions.innerHTML = '';
      return;
    }
    const withHistory = volumes.filter((volume) => volume.forecast?.enoughData);
    const candidates = withHistory.length ? withHistory : volumes;
    const selected = candidates.find((volume) => volume.id === forecastVolumeId) || [...candidates].sort((a, b) => Number(b.usedPercent) - Number(a.usedPercent))[0];
    forecastVolumeId = selected.id;
    if (actions) actions.innerHTML = `<select class="input input-sm" id="storage-forecast-select" aria-label="Volume for capacity forecast">${volumes.map((volume) => `<option value="${esc(volume.id)}"${volume.id === selected.id ? ' selected' : ''}>${esc(volume.name)}</option>`).join('')}</select>`;
    const forecast = selected.forecast || {};
    const thresholds = Array.isArray(forecast.thresholds) ? forecast.thresholds : [];
    const critical = thresholds.find((threshold) => threshold.level === 'critical');
    const growth = Number(forecast.dailyGrowthBytes) || 0;
    const thresholdRows = thresholds.length
      ? thresholds.map((threshold) => `<div class="storage-threshold-row tone-${levelTone(threshold.level)}"><span>${esc(ui.humanTitle(threshold.level))}</span><strong>${threshold.percent}%</strong><span>${fmtDays(threshold.days)}</span><span class="mono">${fmt.bytes(threshold.bytes)}</span></div>`).join('')
      : `<div class="form-note">No percentage thresholds are configured.</div>`;
    host.innerHTML = `
      <div class="storage-forecast-summary">
        <div><span>Current</span><strong>${fmt.pct(selected.usedPercent)}</strong><small>${fmt.bytes(selected.usedBytes)} of ${fmt.bytes(selected.totalBytes)}</small></div>
        <div><span>Growth</span><strong>${growth > 0 ? '+' + fmt.bytes(growth) + '/day' : growth < 0 ? fmt.bytes(growth) + '/day' : 'Flat'}</strong><small>${forecast.enoughData ? `${ui.num(forecast.sampleDays)} daily samples` : 'Needs more daily history'}</small></div>
        <div><span>Critical</span><strong>${critical ? fmtDays(critical.days) : 'No projection'}</strong><small>${critical ? `${critical.percent}% · ${fmt.bytes(critical.bytes)}` : 'No threshold'}</small></div>
        <div><span>Projected 30 days</span><strong>${fmt.bytes(forecast.projected30DaysBytes || selected.usedBytes)}</strong><small>${forecast.daysToFull !== null && forecast.daysToFull !== undefined ? 'Full in ' + fmtDays(forecast.daysToFull) : 'No full date at this rate'}</small></div>
      </div>
      <div class="storage-forecast-layout">
        <div class="storage-forecast-plot">${forecastChart(forecast, selected.totalBytes)}</div>
        <aside class="storage-forecast-thresholds" aria-label="Forecast thresholds">
          <div class="storage-forecast-thresholds-head"><span>Policy thresholds</span><strong>${forecast.enoughData ? 'Projected dates' : 'Awaiting history'}</strong></div>
          <div class="storage-threshold-list">${thresholdRows}</div>
          <p class="form-note">Thresholds come from Capacity policy and apply to alerts, the storage guard and this projection.</p>
        </aside>
      </div>`;
    document.getElementById('storage-forecast-select')?.addEventListener('change', (event) => {
      forecastVolumeId = event.currentTarget.value;
      paintForecast();
    });
  };

  const paintUsage = () => {
    const host = document.getElementById('storage-usage');
    if (!host) return;
    if (!usage) { host.innerHTML = ui.loadingState('Loading mailbox usage…', 3); return; }
    if (usage.error) { host.innerHTML = ui.inlineNotice('critical', 'Could not load mailbox usage', usage.error); return; }
    const recommendation = usage.recommendation;
    const domains = Array.isArray(usage.domains) ? usage.domains : [];
    const records = Array.isArray(usage.usage) ? usage.usage : [];
    if (!domains.length && !records.length) {
      host.innerHTML = ui.emptyState('No usage scan yet', 'Run a low-I/O scan to build the largest-consumer list and rebalance recommendation.', '', 'search');
      return;
    }
    const rows = domains.length ? domains.slice(0, 8) : records.slice(0, 8).map((item) => ({ domain: item.email, volumeName: item.volumeName, bytes: item.bytes, mailboxes: 1 }));
    host.innerHTML = `
      ${recommendation ? `<div class="storage-recommendation">
        <div class="storage-recommendation-head"><span class="eyebrow">Recommended rebalance</span>${ui.badge(recommendation.scope === 'domain' ? 'Domain move' : 'Mailbox move', 'info', 'forward')}</div>
        <p>${esc(recommendation.message)}</p>
        <div class="storage-recommendation-stats">
          <div><span>${esc(recommendation.sourceVolumeName)}</span><strong>${fmt.pct(recommendation.sourceBeforePercent)} → ${fmt.pct(recommendation.sourceAfterPercent)}</strong></div>
          <div><span>${esc(recommendation.targetVolumeName)}</span><strong>${fmt.pct(recommendation.targetBeforePercent)} → ${fmt.pct(recommendation.targetAfterPercent)}</strong></div>
          <div><span>Data</span><strong>${fmt.bytes(recommendation.bytes)}</strong></div>
          <div><span>Copy estimate</span><strong>${ui.num(Math.max(1, Math.round((recommendation.estimatedSeconds || 0) / 60)))} min</strong></div>
        </div>
        ${canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="storage-recommendation-move">${ui.icon('forward', 12)} Review move</button>` : ''}
      </div>` : `<div class="form-note" style="margin-bottom:8px">No rebalance recommendation right now. ${usage.scannedAt ? `Usage scanned ${esc(fmt.timeAgo(usage.scannedAt))}.` : ''}</div>`}
      <div class="table-wrap is-bare"><table class="data compact"><thead><tr><th>${domains.length ? 'Domain' : 'Mailbox'}</th><th>Volume</th><th class="num">Mailboxes</th><th class="num">Size</th></tr></thead><tbody>
        ${rows.map((row) => `<tr><td><span class="cell-main">${esc(row.domain)}</span></td><td>${esc(row.volumeName || '—')}</td><td class="num">${domains.length ? ui.num(row.mailboxes) : '—'}</td><td class="num">${fmt.bytes(row.bytes)}</td></tr>`).join('')}
      </tbody></table></div>
      <p class="form-note" style="margin-top:8px">${usage.scanning ? 'A usage scan is running; sizes update as mailboxes complete.' : usage.scannedAt ? `Last scan ${esc(fmt.dateTime(usage.scannedAt))}.` : 'No completed scan yet.'}</p>`;
    document.getElementById('storage-recommendation-move')?.addEventListener('click', () => openMoveWizard());
  };

  const policyScale = (policy) => {
    const stops = [
      ['Advisory', Number(policy.advisoryPercent) || 0, 'advisory'],
      ['Warning', Math.max(0, (Number(policy.warningPercent) || 0) - (Number(policy.advisoryPercent) || 0)), 'warning'],
      ['Serious', Math.max(0, (Number(policy.seriousPercent) || 0) - (Number(policy.warningPercent) || 0)), 'serious'],
      ['Critical', Math.max(0, (Number(policy.criticalPercent) || 0) - (Number(policy.seriousPercent) || 0)), 'critical'],
      ['Emergency', Math.max(0, (Number(policy.emergencyPercent) || 0) - (Number(policy.criticalPercent) || 0)), 'emergency'],
      ['Headroom', Math.max(0, 100 - (Number(policy.emergencyPercent) || 0)), 'free'],
    ];
    return `<div class="storage-policy-scale" aria-hidden="true">${stops.map(([label, width, tone]) => `<span class="tone-${tone}" style="width:${Math.max(0, width)}%" title="${esc(label)}"></span>`).join('')}</div>
      <div class="storage-policy-scale-key">${stops.map(([label, width, tone]) => `<span class="tone-${tone}"><i></i>${esc(label)}</span>`).join('')}</div>`;
  };

  const openPolicyModal = () => {
    if (!overview) {
      ui.toast('Capacity policy is still loading.', 'info');
      return;
    }
    const policy = overview.policy || {};
    const gib = (Number(policy.minFreeBytes) || 0) / (1024 ** 3);
    if (!canManage) {
      ctx.modal({
        title: 'Capacity policy',
        subtitle: 'Read-only view of the thresholds, reserve, guard and alert re-arming.',
        size: 'large',
        body: `<dl class="kv-grid is-3">
          <div><dt>Advisory</dt><dd>${ui.num(policy.advisoryPercent)}%</dd></div>
          <div><dt>Warning</dt><dd>${ui.num(policy.warningPercent)}%</dd></div>
          <div><dt>Serious</dt><dd>${ui.num(policy.seriousPercent)}%</dd></div>
          <div><dt>Critical</dt><dd>${ui.num(policy.criticalPercent)}%</dd></div>
          <div><dt>Emergency</dt><dd>${ui.num(policy.emergencyPercent)}%</dd></div>
          <div><dt>Reserve</dt><dd>${ui.num(policy.reservePercent)}%</dd></div>
          <div><dt>Minimum free</dt><dd>${fmt.bytes(policy.minFreeBytes)}</dd></div>
          <div><dt>Alert re-arm</dt><dd>${ui.num(policy.alertRearmHours)} hours</dd></div>
          <div><dt>Projected-full alert</dt><dd>${ui.num(policy.forecastAlertDays)} days</dd></div>
          <div><dt>Move concurrency</dt><dd>${ui.num(policy.maxConcurrentMoves)}</dd></div>
          <div><dt>Rollback window</dt><dd>${ui.num(policy.migrationGraceHours)} hours</dd></div>
          <div><dt>Storage guard</dt><dd>${policy.guardEnabled ? 'Enabled' : 'Disabled'}</dd></div>
        </dl>
        <div class="notice tone-info" style="margin-top:12px"><span class="notice-icon">${ui.icon('info', 17)}</span><div><strong>Thresholds are managed centrally</strong><p>Only an administrator with storage.manage can change this policy. It applies to every online volume immediately after saving.</p></div></div>`,
        footer: `<button class="btn btn-secondary" type="button" data-modal-close>Close</button>`,
      });
      return;
    }
    const modal = ctx.modal({
      title: 'Capacity policy',
      subtitle: 'These thresholds drive live capacity state, alerts, the forecast and the storage guard.',
      size: 'large',
      body: `<form id="storage-policy-form">
        <section class="storage-policy-section">
          <div class="storage-policy-section-head"><div><h4>Capacity thresholds</h4><p>Used-percentage bands. Values must increase from advisory to emergency.</p></div>${ui.badge('Live policy', 'info', 'activity')}</div>
          <div id="storage-policy-scale-wrap">${policyScale(policy)}</div>
          <div class="form-grid storage-policy-grid">
            ${ui.field({ label: 'Advisory %', name: 'advisoryPercent', type: 'number', value: policy.advisoryPercent, min: '1', max: '99' })}
            ${ui.field({ label: 'Warning %', name: 'warningPercent', type: 'number', value: policy.warningPercent, min: '1', max: '100' })}
            ${ui.field({ label: 'Serious %', name: 'seriousPercent', type: 'number', value: policy.seriousPercent, min: '1', max: '100' })}
            ${ui.field({ label: 'Critical %', name: 'criticalPercent', type: 'number', value: policy.criticalPercent, min: '1', max: '100' })}
            ${ui.field({ label: 'Emergency %', name: 'emergencyPercent', type: 'number', value: policy.emergencyPercent, min: '1', max: '100' })}
          </div>
        </section>
        <section class="storage-policy-section">
          <div class="storage-policy-section-head"><div><h4>Reserve and protection</h4><p>Space kept for recovery and how the mail path reacts at critical capacity.</p></div></div>
          <div class="form-grid storage-policy-grid">
            ${ui.field({ label: 'Reserve %', name: 'reservePercent', type: 'number', value: policy.reservePercent, min: '0', max: '50' })}
            ${ui.field({ label: 'Minimum free (GiB)', name: 'minFreeGib', type: 'number', value: gib.toFixed(1), min: '0', step: '0.1' })}
            ${ui.field({ label: 'Alert re-arm (hours)', name: 'alertRearmHours', type: 'number', value: policy.alertRearmHours, min: '1', max: '720' })}
            ${ui.field({ label: 'Projected-full alert (days)', name: 'forecastAlertDays', type: 'number', value: policy.forecastAlertDays, min: '1', max: '365' })}
          </div>
          <label class="check-field storage-policy-guard"><input type="checkbox" name="guardEnabled"${policy.guardEnabled ? ' checked' : ''}><span>Enforce the storage guard<small>At critical or emergency capacity, new inbound mail and uploads are held with SMTP 4.3.1 and webmail 507. Existing mail stays readable.</small></span></label>
        </section>
        <section class="storage-policy-section">
          <div class="storage-policy-section-head"><div><h4>Move operations</h4><p>Concurrency and the rollback window applied to verified mailbox moves.</p></div></div>
          <div class="form-grid storage-policy-grid">
            ${ui.field({ label: 'Move concurrency', name: 'maxConcurrentMoves', type: 'number', value: policy.maxConcurrentMoves, min: '1', max: '16' })}
            ${ui.field({ label: 'Rollback window (hours)', name: 'migrationGraceHours', type: 'number', value: policy.migrationGraceHours, min: '1', max: '336' })}
          </div>
        </section>
        <p class="form-note">Saving applies the policy to all online volumes. It does not reformat, mount or move any data.</p>
      </form>`,
      footer: `<button class="btn btn-ghost" type="button" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit" form="storage-policy-form" id="storage-policy-submit">${ui.icon('check', 14)} Review and save</button>`,
    });
    const form = modal.root.querySelector('#storage-policy-form');
    const button = modal.root.querySelector('#storage-policy-submit');
    const valuesFor = (source) => ({
      advisoryPercent: Number(source.advisoryPercent),
      warningPercent: Number(source.warningPercent),
      seriousPercent: Number(source.seriousPercent),
      criticalPercent: Number(source.criticalPercent),
      emergencyPercent: Number(source.emergencyPercent),
      reservePercent: Number(source.reservePercent),
      minFreeBytes: Math.round(Number(source.minFreeGib || 0) * 1024 ** 3),
      alertRearmHours: Number(source.alertRearmHours),
      forecastAlertDays: Number(source.forecastAlertDays),
      maxConcurrentMoves: Number(source.maxConcurrentMoves),
      migrationGraceHours: Number(source.migrationGraceHours),
      guardEnabled: source.guardEnabled === true,
    });
    const thresholdKeys = ['advisoryPercent', 'warningPercent', 'seriousPercent', 'criticalPercent', 'emergencyPercent'];
    const labels = {
      advisoryPercent: 'Advisory threshold', warningPercent: 'Warning threshold', seriousPercent: 'Serious threshold',
      criticalPercent: 'Critical threshold', emergencyPercent: 'Emergency threshold', reservePercent: 'Reserve',
      minFreeBytes: 'Minimum free space', alertRearmHours: 'Alert re-arm', forecastAlertDays: 'Projected-full alert',
      maxConcurrentMoves: 'Move concurrency', migrationGraceHours: 'Rollback window', guardEnabled: 'Storage guard',
    };
    const displayPolicyValue = (key, value) => {
      if (thresholdKeys.includes(key) || key === 'reservePercent') return `${ui.num(value)}%`;
      if (key === 'minFreeBytes') return fmt.bytes(value);
      if (key === 'alertRearmHours' || key === 'migrationGraceHours') return `${ui.num(value)} hours`;
      if (key === 'forecastAlertDays') return `${ui.num(value)} days`;
      if (key === 'guardEnabled') return value ? 'Enabled' : 'Disabled';
      return ui.num(value);
    };
    const refreshScale = () => {
      const scale = modal.root.querySelector('#storage-policy-scale-wrap');
      if (!scale) return;
      scale.innerHTML = policyScale(ui.formValues(form));
    };
    form?.addEventListener('input', refreshScale);
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const current = valuesFor({
        ...policy, minFreeGib: ((Number(policy.minFreeBytes) || 0) / (1024 ** 3)).toFixed(1),
      });
      const next = valuesFor({ ...values, guardEnabled: form.querySelector('[name="guardEnabled"]')?.checked === true });
      const thresholds = thresholdKeys.map((key) => next[key]);
      if (thresholds.some((value, index) => index > 0 && value <= thresholds[index - 1])) {
        ui.toast('Thresholds must increase from advisory to emergency.', 'warning', { title: 'Check the threshold order' });
        return;
      }
      if ([next.advisoryPercent, next.warningPercent, next.seriousPercent, next.criticalPercent, next.emergencyPercent].some((value) => !Number.isFinite(value) || value < 1 || value > 100)) {
        ui.toast('Enter a percentage between 1 and 100 for each threshold.', 'warning', { title: 'Check the thresholds' });
        return;
      }
      const changes = Object.keys(next).filter((key) => next[key] !== current[key]);
      if (!changes.length) {
        ui.toast('No policy changes to save.', 'info');
        return;
      }
      const details = `<ul class="storage-policy-diff">${changes.map((key) => `<li><span>${esc(labels[key] || key)}</span><strong>${esc(displayPolicyValue(key, current[key]))}</strong><i>→</i><strong>${esc(displayPolicyValue(key, next[key]))}</strong></li>`).join('')}</ul>`;
      const confirmed = await ctx.confirm({
        title: 'Save capacity policy?',
        message: 'The thresholds, reserve, guard and alert timers apply to every online volume as soon as the policy is saved.',
        details,
        confirmLabel: 'Save policy',
        tone: 'primary',
      });
      if (!confirmed) return;
      button.disabled = true; button.classList.add('is-loading');
      try {
        await ctx.api('/control/api/storage/policy', { method: 'PATCH', body: next });
        modal.close();
        ui.toast('Capacity policy saved. Alerts, forecast and the storage guard now use the new values.', 'good');
        await Promise.all([load(), loadPlacements()]);
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not save capacity policy' });
        button.disabled = false; button.classList.remove('is-loading');
      }
    });
  };

  const volumeAllowedActions = (volume) => {
    const supported = Boolean(hostInfo?.capabilities?.supported);
    const provisioning = Boolean(hostInfo?.capabilities?.provisioningEnabled);
    const inactive = volume.state === 'retired' || volume.state === 'wiped';
    const fsType = String(volume.fsType || '').toLowerCase();
    const networkVolume = volume.topology === 'nfs' || fsType === 'nfs' || fsType === 'nfs4';
    return {
      move: canManage && Boolean(placements?.doctor?.userdbReady),
      verify: canManage && supported && !inactive,
      extend: canManage && supported && provisioning && volume.online && !inactive && !networkVolume,
      retire: canManage && supported && provisioning && volume.name !== 'legacy' && !inactive,
      wipe: canManage && ctx.can('storage.destroy') && supported && provisioning && volume.state === 'retired',
    };
  };

  const volumeActionList = (volume) => {
    const allowed = volumeAllowedActions(volume);
    const actions = [{ key: 'details', label: 'Open details', description: 'Capacity, identity, health and forecast', icon: 'eye' }];
    if (allowed.move) actions.push({ key: 'move', label: 'Move mail', description: 'Plan a verified move to another volume', icon: 'forward' });
    if (allowed.verify) actions.push({ key: 'verify', label: 'Verify volume', description: 'Check the mount, UUID and write access', icon: 'check-circle' });
    if (allowed.extend) actions.push({ key: 'extend', label: 'Extend volume', description: 'Grow the filesystem online', icon: 'download' });
    if (allowed.retire) actions.push({ key: 'retire', label: 'Retire volume', description: 'Unmount safely; the data is retained', icon: 'archive' });
    if (allowed.wipe) actions.push({ key: 'wipe', label: 'Wipe volume', description: 'Destroy the retired filesystem after confirmation', icon: 'trash', danger: true });
    return actions;
  };

  const runVolumeAction = (volume, action, modal) => {
    if (modal) modal.close();
    if (action === 'details') openVolumeDetails(volume.id);
    else if (action === 'move') openMoveWizard();
    else if (action === 'verify') verifyVolume(volume.id);
    else if (action === 'extend') openExtendWizard(volume.id);
    else if (action === 'retire') openRetireWizard(volume.id);
    else if (action === 'wipe') openWipeWizard(volume.id);
  };

  const openVolumeActions = (volumeId) => {
    const volume = (overview?.volumes || []).find((item) => item.id === volumeId);
    if (!volume) return;
    const actions = volumeActionList(volume);
    const modal = ctx.modal({
      title: `${volume.name} actions`,
      subtitle: `${volumeRole(volume)} · ${volume.mountPath || 'no mount path'}`,
      size: 'small',
      body: `<div class="storage-volume-menu">${actions.map((action) => `<button class="storage-menu-item${action.danger ? ' is-danger' : ''}" type="button" data-volume-menu-action="${esc(action.key)}"><span class="storage-menu-icon">${ui.icon(action.icon, 16)}</span><span><strong>${esc(action.label)}</strong><small>${esc(action.description)}</small></span>${ui.icon('chevron-right', 15)}</button>`).join('')}</div>`,
      footer: `<button class="btn btn-secondary" type="button" data-modal-close>Cancel</button>`,
    });
    modal.root.querySelectorAll('[data-volume-menu-action]').forEach((button) => button.addEventListener('click', () => runVolumeAction(volume, button.dataset.volumeMenuAction, modal)));
  };

  const openVolumeDetails = (volumeId) => {
    const volume = (overview?.volumes || []).find((item) => item.id === volumeId);
    if (!volume) return;
    if (!disks) loadDisks();
    const tone = volumeTone(volume);
    const role = volumeRole(volume);
    const retired = volume.state === 'retired' || volume.state === 'wiped';
    const forecast = volume.forecast || {};
    const critical = (forecast.thresholds || []).find((threshold) => threshold.level === 'critical');
    const inventory = findVolumeDisk(volume, disks);
    const health = inventory?.root?.health || null;
    const healthLabel = !health ? 'Unknown' : health.state === 'ok' ? 'Healthy' : ui.humanTitle(health.state);
    const healthDetail = health
      ? [health.message, health.temperatureC !== undefined && health.temperatureC !== null ? `${health.temperatureC}°C` : ''].filter(Boolean).join(' · ')
      : inventory ? 'SMART data is unavailable for this device.' : 'No matching block device in the inventory.';
    const allowed = volumeAllowedActions(volume);
    const actionButtons = [
      allowed.move ? `<button class="btn btn-secondary btn-sm" type="button" data-volume-detail-action="move">${ui.icon('forward', 13)} Move mail</button>` : '',
      allowed.verify ? `<button class="btn btn-secondary btn-sm" type="button" data-volume-detail-action="verify">${ui.icon('check-circle', 13)} Verify</button>` : '',
      allowed.extend ? `<button class="btn btn-secondary btn-sm" type="button" data-volume-detail-action="extend">${ui.icon('download', 13)} Extend</button>` : '',
      allowed.retire ? `<button class="btn btn-secondary btn-sm" type="button" data-volume-detail-action="retire">${ui.icon('archive', 13)} Retire</button>` : '',
      allowed.wipe ? `<button class="btn btn-soft-danger btn-sm" type="button" data-volume-detail-action="wipe">${ui.icon('trash', 13)} Wipe</button>` : '',
    ].filter(Boolean).join('');
    const detailRow = (label, value, mono = false) => `<div><dt>${esc(label)}</dt><dd${mono ? ' class="mono"' : ''}>${esc(value || '—')}</dd></div>`;
    const modal = ctx.modal({
      title: volume.name,
      subtitle: role,
      size: 'large',
      body: `
        <div class="storage-detail-hero">
          ${driveVisual(volume, tone)}
          <div class="storage-detail-identity">
            <div class="storage-detail-badges">${volumeState(volume)}${ui.badge(role, 'neutral', 'disk')}${volume.managed ? ui.badge('Managed', 'info', 'shield') : ''}</div>
            <strong class="storage-detail-path mono">${esc(volume.mountPath || 'No mount path')}</strong>
            <span class="storage-detail-meta">${esc(volume.fsType || 'unknown filesystem')}${volume.devicePath ? ` · ${esc(volume.devicePath)}` : ''}</span>
          </div>
        </div>
        <div class="storage-detail-stats">
          <div><span>Capacity used</span><strong>${volume.online ? fmt.pct(volume.usedPercent) : 'Offline'}</strong><small>${volume.online ? `${fmt.bytes(volume.usedBytes)} of ${fmt.bytes(volume.totalBytes)}` : 'Mount is not readable'}</small></div>
          <div><span>Free space</span><strong>${volume.online ? fmt.bytes(volume.freeBytes) : '—'}</strong><small>${volume.online ? `${fmt.bytes(volume.totalBytes)} total` : 'No live capacity'}</small></div>
          <div><span>Inodes</span><strong>${volume.online && volume.inodeTotal > 0 ? fmt.pct(volume.inodePercent) : '—'}</strong><small>${volume.online && volume.inodeTotal > 0 ? `${ui.num(volume.inodeUsed)} of ${ui.num(volume.inodeTotal)}` : 'Not reported'}</small></div>
          <div><span>Critical in</span><strong>${volume.online ? (critical ? fmtDays(critical.days) : 'No projection') : '—'}</strong><small>${critical ? `${critical.percent}% threshold` : 'Forecast needs history'}</small></div>
        </div>
        ${volume.online ? `<div class="storage-detail-meter">${ui.meter({ label: 'Live capacity', value: volume.usedPercent, detail: `${fmt.bytes(volume.usedBytes)} used · ${fmt.bytes(volume.freeBytes)} free`, tone: tone === 'good' ? '' : tone })}</div>` : `<div class="storage-offline${retired ? ' is-inactive' : ''}">${ui.icon(retired ? 'archive' : 'alert-circle', 15)} <span>${retired ? (volume.state === 'wiped' ? 'This volume was wiped. Its filesystem signatures were removed and it is no longer mounted.' : 'This volume is retired and no longer mounted. Wiping it is a separate destructive action.') : 'This mount is not readable. Existing mail might be unavailable. Check the mount and the storage doctor.'}</span></div>`}
        <div class="storage-detail-grid">
          <section class="storage-detail-section">
            <h4>Volume identity</h4>
            <dl class="storage-detail-list">
              ${detailRow('Mount path', volume.mountPath, true)}
              ${detailRow('Device', volume.devicePath, true)}
              ${detailRow('Filesystem', volume.fsType, true)}
              ${detailRow('Filesystem UUID', volume.fsUuid, true)}
              ${detailRow('Serial', volume.deviceSerial, true)}
              ${detailRow('WWN', volume.deviceWwn, true)}
              ${detailRow('Mount unit', volume.mountUnit, true)}
              ${detailRow('Topology', ui.humanTitle(volume.topology || 'local'))}
              ${detailRow('Registry', volume.managed ? 'Managed by Alt8Mail' : volume.createdBy === 'discovered' ? 'Discovered mount' : 'Registered mount')}
              ${detailRow('Fstab', volume.fstabManaged ? 'Managed block' : 'Not managed')}
            </dl>
          </section>
          <section class="storage-detail-section">
            <h4>Health and outlook</h4>
            <dl class="storage-detail-list">
              <div><dt>Mount</dt><dd>${volume.online ? 'Readable' : 'Unavailable'}</dd></div>
              <div><dt>Reboot check</dt><dd>${volume.rebootVerified ? 'Verified' : 'Pending'}</dd></div>
              <div><dt>Last verified</dt><dd>${volume.verifiedAt ? fmt.dateTime(volume.verifiedAt) : 'Not recorded'}</dd></div>
              <div><dt>SMART</dt><dd>${ui.badge(healthLabel, healthTone(health))}</dd></div>
              ${detailRow('SMART detail', healthDetail)}
              ${detailRow('Last seen', volume.lastSeenAt ? fmt.timeAgo(volume.lastSeenAt) : 'Not recorded')}
              ${detailRow('Daily growth', forecast.dailyGrowthBytes ? `${Number(forecast.dailyGrowthBytes) > 0 ? '+' : ''}${fmt.bytes(forecast.dailyGrowthBytes)}/day` : 'Flat or unavailable')}
              ${detailRow('Forecast sample', forecast.sampleDays ? `${ui.num(forecast.sampleDays)} daily samples` : 'Not enough history')}
              ${detailRow('Projected 30 days', volume.online ? fmt.bytes(forecast.projected30DaysBytes || volume.usedBytes) : '—')}
              ${detailRow('Projected full', forecast.daysToFull !== null && forecast.daysToFull !== undefined ? fmtDays(forecast.daysToFull) : 'No full date at this rate')}
            </dl>
          </section>
        </div>
        ${volume.state === 'retired' ? ui.inlineNotice('warning', 'Retired volume', 'The filesystem is retained but no longer mounted. Move or restore any remaining data before wiping it.') : ''}
        ${volume.state === 'wiped' ? ui.inlineNotice('neutral', 'Wiped volume', 'The filesystem signatures were removed. This registry entry is retained for audit history.') : ''}`,
      footer: `<button class="btn btn-secondary" type="button" data-modal-close>Close</button>${actionButtons ? `<div class="form-actions-right">${actionButtons}</div>` : ''}`,
    });
    modal.root.querySelectorAll('[data-volume-detail-action]').forEach((button) => button.addEventListener('click', () => runVolumeAction(volume, button.dataset.volumeDetailAction, modal)));
  };

  const paintVolumes = () => {
    const host = document.getElementById('storage-volumes');
    if (!host || !overview) return;
    const volumes = Array.isArray(overview.volumes) ? overview.volumes : [];
    if (!volumes.length) {
      host.innerHTML = ui.emptyState('No storage volumes yet', 'Register the legacy mail root or mount a filesystem under the storage root to begin.', '', 'disk');
      return;
    }
    const counts = {
      all: volumes.length,
      online: volumes.filter((volume) => volumeMatchesFilter(volume, 'online')).length,
      attention: volumes.filter((volume) => volumeMatchesFilter(volume, 'attention')).length,
      offline: volumes.filter((volume) => volumeMatchesFilter(volume, 'offline')).length,
      retired: volumes.filter((volume) => volumeMatchesFilter(volume, 'retired')).length,
      wiped: volumes.filter((volume) => volumeMatchesFilter(volume, 'wiped')).length,
    };
    const filter = document.getElementById('storage-volume-filter');
    if (filter) {
      const options = ['all', 'online', 'attention', 'offline', 'retired', 'wiped'].map((key) => `<option value="${key}">${volumeFilterLabel(key)} (${counts[key]})</option>`).join('');
      if (filter.innerHTML !== options) filter.innerHTML = options;
      filter.value = volumeFilter;
    }
    const shown = volumes.filter((volume) => volumeMatchesFilter(volume, volumeFilter));
    if (!shown.length) {
      host.innerHTML = ui.emptyState(`No ${volumeFilterLabel(volumeFilter).toLowerCase()}`, 'Choose another status to see the other registered volumes.', '', 'filter');
      const subtitle = document.getElementById('storage-volume-subtitle');
      if (subtitle) subtitle.textContent = `0 of ${volumes.length} volumes shown`;
      return;
    }
    host.innerHTML = `<div class="storage-drive-grid">${shown.map((volume) => {
      const tone = volumeTone(volume);
      const role = volumeRole(volume);
      const retired = volume.state === 'retired' || volume.state === 'wiped';
      const forecast = volume.forecast || {};
      const days = forecast.daysToFull;
      const forecastLabel = !volume.online ? 'Paused while offline' : !forecast.enoughData ? 'Collecting history' : days === null || days === undefined ? 'Stable at this rate' : Number(days) <= 0 ? 'Full now' : `Full in ${fmtDays(days)}`;
      const badges = [
        volume.managed ? ui.badge('Managed', 'info', 'shield') : volume.createdBy === 'discovered' ? ui.badge('Discovered', 'neutral', 'search') : ui.badge('Registered', 'neutral', 'disk'),
        volume.rebootVerified ? ui.badge('Boot verified', 'good', 'check-circle') : '',
      ].filter(Boolean).join('');
      return `<article class="storage-drive-card tone-${tone}${volume.online ? '' : ' is-offline'}${retired ? ' is-inactive' : ''}" data-volume-open="${esc(volume.id)}" role="button" tabindex="0" aria-label="Open storage volume details for ${esc(volume.name)}">
        <div class="storage-drive-top">
          ${driveVisual(volume, tone)}
          <div class="storage-drive-identity">
            <div class="storage-drive-name-row"><strong>${esc(volume.name)}</strong>${volumeState(volume)}</div>
            <span class="storage-drive-role">${esc(role)}</span>
            <span class="storage-drive-path mono">${esc(volume.mountPath || 'No mount path')}</span>
          </div>
          <button class="icon-btn storage-drive-menu" type="button" data-volume-menu="${esc(volume.id)}" aria-label="Actions for ${esc(volume.name)}" title="Volume actions">${ui.icon('dots', 16)}</button>
        </div>
        ${volume.online ? `<div class="storage-drive-capacity">
          <div class="storage-drive-cap-head"><span>Capacity used</span><strong>${fmt.pct(volume.usedPercent)}</strong></div>
          <div class="storage-drive-meter" role="meter" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${Number(volume.usedPercent) || 0}" aria-label="${esc(volume.name)} capacity used"><span style="width:${Math.max(0, Math.min(100, Number(volume.usedPercent) || 0))}%"></span></div>
          <div class="storage-drive-cap-foot"><span>${fmt.bytes(volume.usedBytes)} used</span><span>${fmt.bytes(volume.freeBytes)} free</span><span>${fmt.bytes(volume.totalBytes)}</span></div>
        </div>` : `<div class="storage-drive-offline">${ui.icon(retired ? 'archive' : 'alert-circle', 15)}<span>${retired ? (volume.state === 'wiped' ? 'Wiped and no longer mounted.' : 'Retired and no longer mounted.') : 'This mount is not readable.'}</span></div>`}
        <div class="storage-drive-meta">
          <div><span>Filesystem</span><strong>${esc(volume.fsType || '—')}</strong></div>
          <div><span>Device</span><strong class="mono">${esc(volume.devicePath || '—')}</strong></div>
          <div><span>Outlook</span><strong>${esc(forecastLabel)}</strong></div>
        </div>
        <div class="storage-drive-foot">
          <div class="storage-drive-badges">${badges}</div>
          <span class="storage-drive-seen">${volume.lastSeenAt ? `Seen ${fmt.timeAgo(volume.lastSeenAt)}` : 'Not seen yet'}</span>
        </div>
      </article>`;
    }).join('')}</div>`;
    const subtitle = document.getElementById('storage-volume-subtitle');
    if (subtitle) {
      subtitle.textContent = `${shown.length === volumes.length ? `${volumes.length} volume${volumes.length === 1 ? '' : 's'}` : `${shown.length} of ${volumes.length} volumes shown`} · ${fmt.bytes(overview.totals?.totalBytes)} total · ${fmt.bytes(overview.totals?.freeBytes)} free`;
    }
    host.querySelectorAll('[data-volume-open]').forEach((card) => {
      card.addEventListener('click', (event) => {
        if (event.target.closest('[data-volume-menu]')) return;
        openVolumeDetails(card.dataset.volumeOpen);
      });
      card.addEventListener('keydown', (event) => {
        if (event.key !== 'Enter' && event.key !== ' ') return;
        if (event.target.closest('[data-volume-menu]')) return;
        event.preventDefault();
        openVolumeDetails(card.dataset.volumeOpen);
      });
    });
    host.querySelectorAll('[data-volume-menu]').forEach((button) => button.addEventListener('click', (event) => {
      event.stopPropagation();
      openVolumeActions(button.dataset.volumeMenu);
    }));
  };

  const verifyVolume = async (volumeId, button) => {
    if (button) { button.disabled = true; button.classList.add('is-loading'); }
    try {
      const result = await ctx.api(`/control/api/storage/volumes/${encodeURIComponent(volumeId)}/verify`, { method: 'POST' });
      ui.toast(result.verified ? 'Mount, UUID and write access verified.' : 'The mount is reachable but verification reported a problem.', result.verified ? 'good' : 'warning');
      await Promise.all([load(), loadHost()]);
    } catch (error) {
      ui.toast(errorText(error), 'critical', { title: 'Volume verification failed' });
      if (button) { button.disabled = false; button.classList.remove('is-loading'); }
    }
  };

  const paintDoctor = () => {
    const host = document.getElementById('storage-doctor');
    if (!host || !overview) return;
    const doctor = overview.doctor || {};
    const issues = Array.isArray(doctor.issues) ? doctor.issues : [];
    const severityTone = { critical: 'critical', warning: 'warning', info: 'info' };
    host.innerHTML = `
      <div class="storage-doctor-head">${ui.statusPill(doctor.userdbReady ? 'active' : 'warning', doctor.userdbReady ? 'Dovecot storage-aware' : 'Dovecot legacy home')}<span class="mono muted">${esc(doctor.configPath || '')}</span></div>
      ${issues.length ? `<div class="storage-issues">${issues.map((issue) => `<div class="storage-issue is-${esc(issue.severity || 'info')}"><span class="storage-issue-dot"></span><div><strong>${esc(issue.title)}</strong><p>${esc(issue.message)}</p></div></div>`).join('')}</div>` : `<div class="notice tone-good"><span class="notice-icon">${ui.icon('check-circle', 17)}</span><div><strong>Prerequisites look ready</strong><p>The legacy root is readable and Dovecot resolves the per-mailbox home.</p></div></div>`}
      <div class="form-actions" style="margin-top:10px"><span class="form-note">The userdb update backs up the file, validates it, reloads Dovecot and verifies a mailbox. Existing mail keeps its path.</span>${canManage && !doctor.userdbReady ? `<div class="form-actions-right"><button class="btn btn-secondary btn-sm" type="button" id="storage-apply-dovecot">${ui.icon('shield-check', 13)} Apply managed query</button></div>` : ''}</div>
      <p class="form-note" style="margin-top:6px">Applying the query is an explicit storage operation; viewing this page never changes the host.</p>`;
    document.getElementById('storage-apply-dovecot')?.addEventListener('click', async (event) => {
      const ok = await ctx.confirm({
        title: 'Update the Dovecot userdb query',
        message: 'The installed Dovecot SQL file will be backed up, the mail_home-aware query will be applied, the configuration will be validated, Dovecot will reload, and one mailbox will be verified. Existing mail keeps its current path.',
        confirmLabel: 'Apply and verify',
      });
      if (!ok) return;
      const button = event.currentTarget;
      button.classList.add('is-loading');
      button.disabled = true;
      try {
        const result = await ctx.api('/control/api/storage/doctor/dovecot', { method: 'POST' });
        ui.toast(result.message || 'Dovecot userdb updated.', 'good');
        await Promise.all([load(), loadPlacements()]);
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Dovecot update failed' });
        button.classList.remove('is-loading');
        button.disabled = false;
      }
    });
  };

  const assignPlacement = async (payload) => {
    try {
      const result = await ctx.api('/control/api/storage/assignments', { method: 'POST', body: payload });
      ui.toast(result.message || 'Placement updated. New mailboxes will use the selected volume.', 'good');
      await Promise.all([loadPlacements(), load()]);
    } catch (error) {
      ui.toast(errorText(error), 'critical', { title: 'Placement update failed' });
      await loadPlacements();
    }
  };

  const paintPlacement = () => {
    const host = document.getElementById('storage-placement');
    const actions = document.getElementById('storage-placement-actions');
    if (!host) return;
    if (!placements) { host.innerHTML = ui.loadingState('Loading placement policy…', 4); return; }
    const volumes = Array.isArray(placements.volumes) ? placements.volumes.filter((volume) => volume.state !== 'retired' && volume.state !== 'wiped') : [];
    const domains = Array.isArray(placements.domains) ? placements.domains : [];
    const policy = placements.policy || {};
    const doctor = placements.doctor || {};
    const ready = Boolean(doctor.userdbReady);
    const volumeOptions = (selected, inheritLabel) => `<option value="">${esc(inheritLabel)}</option>${volumes.map((volume) => `<option value="${esc(volume.id)}"${volume.id === selected ? ' selected' : ''}>${esc(volume.name)}${volume.legacy ? ' (legacy)' : ''}</option>`).join('')}`;
    const defaultVolume = volumes.find((volume) => volume.id === policy.defaultVolumeId);
    if (actions) actions.innerHTML = `<button class="btn btn-secondary btn-sm" type="button" id="storage-placement-refresh">${ui.icon('refresh', 13)} Recheck</button>`;
    host.innerHTML = `
      <div class="storage-placement-summary">
        <div><span>Global default</span>${canManage && ready ? `<select class="input input-sm" id="storage-global-volume">${volumeOptions(policy.defaultVolumeId, 'Legacy root (no default)')}</select>` : `<strong>${defaultVolume ? esc(defaultVolume.name) : 'Legacy root'}</strong>`}<small>${ready ? 'New mailboxes with no domain override' : 'Locked until Dovecot reads mail_home'}</small></div>
        <div><span>Domain defaults</span><strong>${ui.num(domains.length)}</strong><small>${ui.num(domains.filter((domain) => domain.volumeId).length)} explicit · ${ui.num(domains.filter((domain) => !domain.volumeId).length)} inherit global</small></div>
      </div>
      ${!ready ? `<div class="notice tone-warning" style="margin-top:12px"><span class="notice-icon">${ui.icon('alert-circle', 17)}</span><div><strong>Placement is locked</strong><p>Apply the mail_home-aware Dovecot userdb query in the storage doctor before selecting another volume for new mail.</p></div></div>` : ''}
      <div class="table-wrap" style="margin-top:12px"><table class="data compact"><thead><tr><th>Domain</th><th class="num">Mailboxes</th><th>Effective volume</th><th>Default for new mailboxes</th></tr></thead><tbody>
        ${domains.length ? domains.map((domain) => `<tr>
          <td><span class="cell-main">${esc(domain.domain)}</span><span class="cell-sub">${domain.placedCount ? `${ui.num(domain.placedCount)} explicitly placed` : 'Existing mail on the legacy root'}</span></td>
          <td class="num">${ui.num(domain.mailboxCount)}</td>
          <td>${esc(domain.volumeName || 'Legacy root')}<span class="cell-sub mono">${esc(domain.mountPath || '')}</span></td>
          <td>${canManage && ready ? `<select class="input input-sm" data-storage-domain="${esc(domain.id)}">${volumeOptions(domain.volumeId, 'Inherit global')}</select>` : `<span class="mono">${domain.volumeId ? 'Explicit' : 'Inherit global'}</span>`}</td>
        </tr>`).join('') : `<tr><td colspan="4">${ui.emptyState('No managed domains', 'Add or verify a domain before selecting a storage default.', '', 'globe')}</td></tr>`}
      </tbody></table></div>
      <div class="notice tone-info" style="margin-top:12px"><span class="notice-icon">${ui.icon('info', 17)}</span><div><strong>Existing mail does not move when you change a default</strong><p>This changes the destination for new mailboxes. Moving existing mail is a separate, verified job that retains the source until finalization.</p></div></div>`;
    document.getElementById('storage-global-volume')?.addEventListener('change', (event) => {
      assignPlacement({ scope: 'global', volumeId: event.currentTarget.value });
    });
    document.querySelectorAll('[data-storage-domain]').forEach((select) => select.addEventListener('change', (event) => {
      assignPlacement({ scope: 'domain', id: select.dataset.storageDomain, volumeId: event.currentTarget.value });
    }));
    document.getElementById('storage-placement-refresh')?.addEventListener('click', () => { loadPlacements(); load(true); });
    updateMoveButton();
  };

  const loadPlacements = async () => {
    try {
      const payload = await ctx.api('/control/api/storage/placements');
      if (!ctx.isCurrent()) return;
      placements = payload;
      paintPlacement();
    } catch (error) {
      if (!ctx.isCurrent()) return;
      const host = document.getElementById('storage-placement');
      if (host) host.innerHTML = ui.inlineNotice('critical', 'Could not load placement policy', errorText(error));
    }
  };

  const updateMoveButton = () => {
    const button = document.getElementById('storage-move');
    if (!button) return;
    const ready = Boolean(placements?.doctor?.userdbReady);
    button.disabled = !ready;
    button.title = ready ? 'Move a domain to another volume with a verified, reversible job' : 'Apply the Dovecot userdb query before moving mail';
  };

  const operationsSortHeader = (key, label, className = '') => {
    const active = operationsState.sort === key;
    const aria = active ? (operationsState.order === 'asc' ? 'ascending' : 'descending') : 'none';
    return `<th${className ? ` class="${className}"` : ''} aria-sort="${aria}"><button class="storage-sort-btn${active ? ' is-active' : ''}" type="button" data-op-sort="${key}">${label}<span class="storage-sort-icon" aria-hidden="true">${active ? (operationsState.order === 'asc' ? '↑' : '↓') : '↕'}</span></button></th>`;
  };

  const paintOperations = () => {
    const host = document.getElementById('storage-operations');
    if (!host) return;
    if (!operations) { host.innerHTML = ui.loadingState('Loading storage operations…', 3); return; }
    const rows = Array.isArray(operations.operations) ? operations.operations : [];
    if (!rows.length) {
      host.innerHTML = ui.emptyState('No storage operations', 'A move or host disk operation appears here after you preview and execute a plan. The source stays retained until finalization.', '', 'forward');
      return;
    }
    const total = Number(operations.total ?? rows.length) || 0;
    const pages = Math.max(1, Number(operations.pages) || Math.ceil(total / operationsState.limit) || 1);
    const page = Math.min(pages, Math.max(1, Number(operations.page) || operationsState.page));
    const first = total ? (page - 1) * operationsState.limit + 1 : 0;
    const last = Math.min(total, page * operationsState.limit);
    const pager = (target, label, iconName, disabled) => `<button class="btn btn-ghost btn-sm" type="button" data-op-page="${target}"${disabled ? ' disabled' : ''}>${iconName ? ui.icon(iconName, 12) : ''}${label ? ` ${esc(label)}` : ''}</button>`;
    host.innerHTML = `<div class="table-wrap is-bare"><table class="data compact storage-operations-table"><thead><tr>
      ${operationsSortHeader('operation', 'Operation')}
      ${operationsSortHeader('target', 'Target')}
      ${operationsSortHeader('mailboxes', 'Mailboxes', 'num')}
      ${operationsSortHeader('status', 'Status')}
      <th>Progress</th><th><span class="sr-only">Actions</span></th>
    </tr></thead><tbody>${rows.map((op) => {
      const statusLabel = op.status === 'grace' ? 'Verified · grace' : ui.humanTitle(op.status);
      const tone = op.status === 'grace' ? 'good' : op.status === 'succeeded' ? 'good' : op.status === 'failed' ? 'critical' : op.status === 'rolled_back' || op.status === 'canceled' ? 'neutral' : 'info';
      const volumeOp = String(op.kind || '').startsWith('volume_');
      const swapOp = op.kind === 'swap_create';
      const operationLabel = swapOp ? 'Create swap' : volumeOp ? `${ui.humanTitle(String(op.kind).replace('volume_', ''))} volume` : op.scope === 'domain' ? `${op.domain || 'Domain'} · domain move` : 'Mailbox move';
      const operationTarget = swapOp ? op.swapTarget || op.planMountPath || op.planDevice || 'Swap' : volumeOp ? op.planName || op.planVolumeName || op.planMountPath : op.targetVolume || '—';
      const operationPath = swapOp ? (op.swapTarget || op.planMountPath || op.planDevice || '') : volumeOp ? op.planMountPath || '' : op.targetMountPath || '';
      const progressDetail = swapOp ? `${fmt.bytes(op.totalBytes || 0)} swap area` : `${fmt.bytes(op.movedBytes || 0)} of ${fmt.bytes(op.totalBytes || 0)}`;
      return `<tr><td><span class="cell-main">${esc(operationLabel)}</span><span class="cell-sub">${esc(op.actorEmail || '')} · ${esc(fmt.timeAgo(op.createdAt))}</span></td><td>${esc(operationTarget || '—')}<span class="cell-sub mono">${esc(operationPath)}</span></td><td class="num">${volumeOp || swapOp ? '—' : ui.num(op.mailboxes || 0)}</td><td>${ui.badge(statusLabel, tone)}<span class="cell-sub">${esc(op.step || '')}</span></td><td><div class="storage-operation-progress">${ui.progress(op.progress || 0, op.status === 'failed' ? 'critical' : op.status === 'grace' || op.status === 'succeeded' ? 'good' : 'accent')}<span class="cell-sub">${progressDetail}</span></div></td><td class="actions"><button class="btn btn-ghost btn-sm" type="button" data-storage-operation="${esc(op.id)}">${ui.icon('terminal', 12)} Open</button></td></tr>`;
    }).join('')}</tbody></table></div>
    <div class="table-footer storage-ops-footer">
      <span class="pagination-summary">Showing <strong>${ui.num(first)}–${ui.num(last)}</strong> of <strong>${ui.num(total)}</strong> operations</span>
      <div class="pagination-controls">
        ${pager(1, 'First', '', page <= 1)}
        ${pager(page - 1, 'Previous', 'chevron-left', page <= 1)}
        <span class="pagination-page">Page ${ui.num(page)} of ${ui.num(pages)}</span>
        ${pager(page + 1, 'Next', 'chevron-right', page >= pages)}
        ${pager(pages, 'Last', '', page >= pages)}
      </div>
    </div>`;
    host.querySelectorAll('[data-op-sort]').forEach((button) => button.addEventListener('click', () => {
      const key = button.dataset.opSort;
      if (operationsState.sort === key) operationsState.order = operationsState.order === 'asc' ? 'desc' : 'asc';
      else {
        operationsState.sort = key;
        operationsState.order = key === 'mailboxes' || key === 'createdAt' ? 'desc' : 'asc';
      }
      operationsState.page = 1;
      loadOperations();
    }));
    host.querySelectorAll('[data-op-page]').forEach((button) => button.addEventListener('click', () => {
      const target = Number(button.dataset.opPage);
      if (!Number.isFinite(target) || target < 1 || target > pages || target === page) return;
      operationsState.page = target;
      loadOperations();
    }));
    host.querySelectorAll('[data-storage-operation]').forEach((button) => button.addEventListener('click', () => openOperationConsole(button.dataset.storageOperation)));
  };

  const loadOperations = async (quiet = false) => {
    const host = document.getElementById('storage-operations');
    if (!quiet && !operations && host) host.innerHTML = ui.loadingState('Loading storage operations…', 3);
    try {
      const params = new URLSearchParams({
        page: String(operationsState.page), limit: String(operationsState.limit),
        sort: operationsState.sort, order: operationsState.order,
      });
      const payload = await ctx.api(`/control/api/storage/operations?${params.toString()}`);
      if (!ctx.isCurrent()) return;
      const pages = Number(payload.pages) || 0;
      if (pages > 0 && operationsState.page > pages) {
        operationsState.page = pages;
        await loadOperations(true);
        return;
      }
      operations = payload;
      paintOperations();
    } catch (error) {
      if (!ctx.isCurrent()) return;
      if (!operations && host) host.innerHTML = ui.inlineNotice('critical', 'Could not load storage operations', errorText(error));
    }
  };

  const openOperationConsole = async (operationId) => {
    let timer = null;
    let last = null;
    const modal = ctx.modal({
      title: 'Storage operation', subtitle: 'Live host operation with runner progress, logs and audit history.',
      size: 'large',
      body: ui.loadingState('Loading operation…', 4),
      footer: `<button class="btn btn-secondary" type="button" data-modal-close>Close</button>`,
      onClose: () => { if (timer) clearInterval(timer); },
    });
    const body = modal.root.querySelector('.modal-body');
    const footer = modal.root.querySelector('.modal-footer');
    const runOperationAction = async (id, action, button) => {
      if (button) { button.disabled = true; button.classList.add('is-loading'); }
      try {
        const result = await ctx.api(`/control/api/storage/operations/${encodeURIComponent(id)}/${action}`, { method: 'POST' });
        ui.toast(result.message || `${ui.humanTitle(action)} requested.`, 'good');
        await loadOperations();
        await refresh();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: `${ui.humanTitle(action)} failed` });
        if (button) { button.disabled = false; button.classList.remove('is-loading'); }
      }
    };
    const paint = (op) => {
      last = op;
      const items = Array.isArray(op.items) ? op.items : [];
      const volumeOp = String(op.kind || '').startsWith('volume_');
      const swapOp = op.kind === 'swap_create';
      const hostOp = volumeOp || swapOp;
      const tone = op.status === 'grace' || op.status === 'succeeded' ? 'good' : op.status === 'failed' ? 'critical' : op.status === 'rolled_back' || op.status === 'canceled' ? 'neutral' : 'info';
      const statusLabel = op.status === 'grace' ? 'Verified · grace window' : op.status === 'succeeded' ? (hostOp ? 'Completed' : 'Finalized') : ui.humanTitle(op.status);
      const title = swapOp ? 'Create swap' : volumeOp ? `${ui.humanTitle(String(op.kind).replace('volume_', ''))} volume` : (op.scope === 'domain' ? op.domain || 'Domain move' : 'Mailbox move');
      const subtitle = swapOp
        ? `${op.planMode === 'device' ? 'Dedicated disk' : 'Swap file'} · <span class="mono">${esc(op.swapTarget || op.planMountPath || op.planDevice || '')}</span> · ${fmt.bytes(op.swapSizeBytes || op.totalBytes || 0)}`
        : volumeOp ? `${esc(op.planName || op.planVolumeName || '')} · ${esc(op.planMountPath || op.planDevice || '')}` : `${ui.num(op.mailboxes || 0)} mailbox${op.mailboxes === 1 ? '' : 'es'} → ${esc(op.targetVolume || '')} <span class="mono">${esc(op.targetMountPath || '')}</span>`;
      body.innerHTML = `
        <div class="storage-operation-head">
          <div><span class="mono muted">${esc(op.id)}</span><h3>${esc(title)}</h3><p>${subtitle}</p></div>
          <div class="storage-operation-status">${ui.badge(statusLabel, tone)}${op.status === 'grace' && op.finalizeAt ? `<span class="cell-sub">Finalize after ${esc(fmt.dateTime(op.finalizeAt))}</span>` : ''}</div>
        </div>
        ${ui.progress(op.progress || 0, op.status === 'failed' ? 'critical' : op.status === 'grace' || op.status === 'succeeded' ? 'good' : 'accent', `${op.progress || 0}%`)}
        <div class="storage-operation-meta"><span>${esc(op.step || '')}</span><span class="mono">${swapOp ? esc(op.swapTarget || '') : volumeOp ? esc(op.planDevice || '') : `${fmt.bytes(op.movedBytes || 0)} / ${fmt.bytes(op.totalBytes || 0)}`}</span></div>
        ${op.error ? ui.inlineNotice('critical', 'Operation error', op.error) : ''}
        ${op.status === 'grace' ? ui.inlineNotice('good', 'Verified; source retained', 'Rollback is available until finalization. Finalize only when you are satisfied; it permanently removes the retained source copy.') : ''}
        ${hostOp && Array.isArray(op.planCommands) && op.planCommands.length ? `<h4 style="margin-top:14px">Planned host steps</h4><pre class="storage-log">${op.planCommands.map((command) => esc(command)).join('\n')}</pre>` : ''}
        ${items.length ? `<div class="table-wrap" style="margin-top:12px"><table class="data compact storage-operation-items"><thead><tr><th>Mailbox</th><th>From</th><th>To</th><th>Status</th><th class="num">Size</th></tr></thead><tbody>${items.map((item) => `<tr><td><span class="cell-main">${esc(item.email)}</span><span class="cell-sub mono">${esc(item.sourceHome)}</span></td><td>${esc(item.sourceVolume || 'legacy')}</td><td>${esc(item.targetVolume || '—')}<span class="cell-sub mono">${esc(item.targetHome)}</span></td><td>${ui.badge(ui.humanTitle(item.status), item.status === 'grace' || item.status === 'finalized' ? 'good' : item.status === 'failed' ? 'critical' : 'info')}${item.error ? `<span class="cell-sub" style="color:var(--status-critical-ink)">${esc(item.error)}</span>` : ''}</td><td class="num">${fmt.bytes(item.bytes || 0)}</td></tr>`).join('')}</tbody></table></div>` : ''}
        <details style="margin-top:12px"><summary class="form-note">Runner log</summary><pre class="storage-log">${esc(op.log || 'No log lines yet.')}</pre></details>`;
      const controls = [`<button class="btn btn-secondary" type="button" data-modal-close>Close</button>`];
      if (op.canCancel) controls.push(`<button class="btn btn-soft-danger" type="button" id="storage-op-cancel">${ui.icon('pause', 13)} Cancel</button>`);
      if (op.canRollback) controls.push(`<button class="btn btn-soft-danger" type="button" id="storage-op-rollback">${ui.icon('rotate', 13)} Roll back</button>`);
      if (op.canFinalize) controls.push(`<button class="btn btn-primary" type="button" id="storage-op-finalize">${ui.icon('check', 13)} Finalize</button>`);
      footer.innerHTML = `<div class="form-actions-right">${controls.join('')}</div>`;
      footer.querySelectorAll('[data-modal-close]').forEach((button) => button.addEventListener('click', () => modal.close()));
      document.getElementById('storage-op-cancel')?.addEventListener('click', async (event) => {
        const ok = await ctx.confirm({ title: 'Cancel storage operation', message: hostOp ? 'Cancel this host operation before the runner starts? No host writes have occurred.' : 'The runner stops at the next safe checkpoint. A mailbox that already switched can still be rolled back from the grace window.', confirmLabel: 'Request cancellation', tone: 'danger' });
        if (!ok) return;
        await runOperationAction(op.id, 'cancel', event.currentTarget);
      });
      document.getElementById('storage-op-rollback')?.addEventListener('click', async (event) => {
        const ok = await ctx.confirm({ title: 'Roll back this move', message: 'Mailbox homes switch back to the retained source and any new mail on the target is copied back. This is the safe pre-finalization undo.', confirmLabel: 'Roll back', tone: 'danger' });
        if (!ok) return;
        await runOperationAction(op.id, 'rollback', event.currentTarget);
      });
      document.getElementById('storage-op-finalize')?.addEventListener('click', async (event) => {
        const ok = await ctx.confirm({ title: 'Finalize and delete the source', message: 'The retained source copies will be permanently removed. Rollback is no longer possible after this step.', confirmLabel: 'Finalize move', tone: 'danger', requireText: 'FINALIZE' });
        if (!ok) return;
        await runOperationAction(op.id, 'finalize', event.currentTarget);
      });
    };
    const refresh = async () => {
      try {
        const payload = await ctx.api(`/control/api/storage/operations/${encodeURIComponent(operationId)}`);
        if (!ctx.isCurrent()) return;
        paint(payload.operation);
      } catch (error) {
        body.innerHTML = ui.inlineNotice('critical', 'Could not load operation', errorText(error));
      }
    };
    await refresh();
    timer = setInterval(() => {
      if (!ctx.isCurrent()) return;
      if (last && ['planned', 'queued', 'running', 'grace'].includes(last.status)) refresh();
    }, 2000);
    ctx.onCleanup(() => { if (timer) clearInterval(timer); });
  };

  const openMoveWizard = async () => {
    if (!placements) await loadPlacements();
    if (!placements?.doctor?.userdbReady) {
      ui.toast('Apply the mail_home-aware Dovecot userdb query before moving mail.', 'warning');
      return;
    }
    const domains = Array.isArray(placements.domains) ? placements.domains : [];
    const volumes = Array.isArray(placements.volumes) ? placements.volumes : [];
    if (!volumes.length) {
      ui.toast('Register an online target volume before moving mail.', 'warning');
      return;
    }
    const policy = placements.policy || {};
    const targetVolumes = [...volumes].sort((a, b) => Number(Boolean(a.legacy)) - Number(Boolean(b.legacy)));
    const defaultDomainId = domains.find((domain) => Number(domain.mailboxCount) > 0)?.id || domains[0]?.id || '';
    let scope = 'domain';
    let pickerLoaded = false;
    let pickerSeq = 0;
    let searchTimer = null;
    const moveLimit = 500;
    const selected = new Map();
    const picker = { q: '', volume: '', page: 1, limit: 50, total: 0, pages: 1, rows: [], loading: false, error: '' };
    const targetOptions = targetVolumes.map((volume) => ({ value: volume.id, label: `${volume.name}${volume.legacy ? ' (legacy)' : ''} · ${volume.mountPath}` }));
    const sourceVolumeOptions = [
      { value: '', label: 'All source volumes' },
      { value: 'legacy', label: 'Legacy root (no volume)' },
      ...volumes.map((volume) => ({ value: volume.id, label: `${volume.name} · ${volume.mountPath}` })),
    ];
    const domainOptions = domains.length
      ? domains.map((domain) => ({ value: domain.id, label: `${domain.domain} · ${ui.num(domain.mailboxCount)} mailbox${Number(domain.mailboxCount) === 1 ? '' : 'es'}` }))
      : [{ value: '', label: 'No verified domains' }];
    const modal = ctx.modal({
      title: 'Move mail to another volume',
      subtitle: 'Choose the scope, select mailboxes and review the destination before anything is copied.',
      size: 'xlarge',
      body: `<form id="storage-move-form">
        <div class="storage-move-layout">
          <div class="storage-move-main">
            <div class="storage-move-tabs" role="tablist" aria-label="Move scope">
              <button class="storage-move-tab is-active" id="storage-move-tab-domain" type="button" role="tab" tabindex="0" aria-selected="true" aria-controls="storage-move-domain-pane" data-move-scope="domain">
                <span class="storage-move-tab-icon">${ui.icon('globe', 18)}</span>
                <span><strong>Whole domain</strong><small>Move every active mailbox and update the domain default</small></span>
              </button>
              <button class="storage-move-tab" id="storage-move-tab-mailbox" type="button" role="tab" tabindex="-1" aria-selected="false" aria-controls="storage-move-mailbox-pane" data-move-scope="mailbox">
                <span class="storage-move-tab-icon">${ui.icon('users', 18)}</span>
                <span><strong>Selected mailboxes</strong><small>Pick individual accounts; domain defaults stay unchanged</small></span>
              </button>
            </div>
            <div class="storage-move-pane" id="storage-move-domain-pane" role="tabpanel" aria-labelledby="storage-move-tab-domain">
              ${domains.length ? ui.field({ label: 'Domain', name: 'domainId', type: 'select', value: defaultDomainId, options: domainOptions, help: 'All active mailboxes in the domain move; the domain default changes with the job.' }) : `<div class="notice tone-warning"><span class="notice-icon">${ui.icon('alert-circle', 17)}</span><div><strong>No verified domains</strong><p>Add or verify a domain before using a whole-domain move. Individual mailboxes can still be selected.</p></div></div>`}
              <div class="storage-move-domain-summary" id="storage-move-domain-summary"></div>
            </div>
            <div class="storage-move-pane" id="storage-move-mailbox-pane" role="tabpanel" aria-labelledby="storage-move-tab-mailbox" hidden>
              <div class="storage-picker-toolbar">
                <label class="storage-picker-search">${ui.icon('search', 15)}<span class="sr-only">Search mailboxes</span><input class="input" id="storage-picker-search" type="search" placeholder="Search email or user name" autocomplete="off"></label>
                <select class="input storage-picker-volume" id="storage-picker-volume" aria-label="Filter mailboxes by source volume">${sourceVolumeOptions.map((option) => `<option value="${esc(option.value)}">${esc(option.label)}</option>`).join('')}</select>
              </div>
              <div class="storage-picker-bulk">
                <div><strong id="storage-picker-count">No mailboxes selected</strong><span id="storage-picker-size">Search or browse the mailbox list.</span></div>
                <div class="storage-picker-bulk-actions">
                  <button class="btn btn-ghost btn-sm" type="button" data-picker-bulk="page">Select page</button>
                  <button class="btn btn-ghost btn-sm" type="button" data-picker-bulk="matching">Select all matching</button>
                  <button class="btn btn-ghost btn-sm" type="button" data-picker-bulk="clear">Clear</button>
                </div>
              </div>
              <div class="table-wrap storage-picker-wrap">
                <table class="data compact storage-picker-table">
                  <thead><tr><th class="check"><input type="checkbox" id="storage-picker-page-all" aria-label="Select all mailboxes on this page"></th><th>Mailbox</th><th>Current volume</th><th class="num">Used</th><th class="num">Quota</th><th>Usage</th></tr></thead>
                  <tbody id="storage-picker-rows"><tr><td colspan="6" class="table-empty">${ui.icon('users', 18)}<span>Switch to selected mailboxes to load the directory.</span></td></tr></tbody>
                </table>
              </div>
              <div class="storage-picker-pager">
                <span id="storage-picker-range">0 mailboxes</span>
                <div class="pagination-controls">
                  <button class="btn btn-ghost btn-sm" type="button" data-picker-page="prev" disabled>${ui.icon('chevron-left', 12)} Previous</button>
                  <span class="pagination-page" id="storage-picker-page-label">Page 1 of 1</span>
                  <button class="btn btn-ghost btn-sm" type="button" data-picker-page="next" disabled>Next ${ui.icon('chevron-right', 12)}</button>
                </div>
              </div>
              <p class="form-note">Search and pagination are server-side, so large directories stay responsive. Selection is kept while you move between pages. A single operation supports up to ${ui.num(moveLimit)} individually selected mailboxes; use Whole domain for a larger move.</p>
            </div>
          </div>
          <aside class="storage-move-side">
            <div class="storage-move-side-head"><span class="eyebrow">Destination</span><h4>Target volume</h4></div>
            ${ui.field({ label: 'Target volume', name: 'targetVolumeId', type: 'select', required: true, value: targetVolumes[0]?.id || '', options: targetOptions, help: 'The destination must be online and have room for the source plus the reserve.' })}
            ${ui.field({ label: 'Verification', name: 'verify', type: 'select', value: 'standard', options: [{ value: 'standard', label: 'Standard · counts and rsync check' }, { value: 'thorough', label: 'Thorough · full checksums (slower)' }] })}
            ${ui.field({ label: 'Source retention (hours)', name: 'graceHours', type: 'number', value: String(policy.migrationGraceHours || 24), min: '1', max: '336', help: 'Rollback stays available for this window after verification.' })}
            <div class="storage-move-summary" id="storage-move-selection-summary"></div>
            <div class="notice tone-info storage-move-note"><span class="notice-icon">${ui.icon('info', 17)}</span><div><strong>Planning performs no writes</strong><p>You will review the source, destination and free space before execution.</p></div></div>
          </aside>
        </div>
      </form>`,
      footer: `<button class="btn btn-ghost" type="button" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit" form="storage-move-form" id="storage-move-submit">${ui.icon('forward', 14)} Preview plan</button>`,
      onClose: () => { if (searchTimer) clearTimeout(searchTimer); },
    });
    const form = modal.root.querySelector('#storage-move-form');
    const domainPane = modal.root.querySelector('#storage-move-domain-pane');
    const mailboxPane = modal.root.querySelector('#storage-move-mailbox-pane');
    const domainSelect = form.querySelector('[name="domainId"]');
    const targetSelect = form.querySelector('[name="targetVolumeId"]');
    const pickerSearch = modal.root.querySelector('#storage-picker-search');
    const pickerVolume = modal.root.querySelector('#storage-picker-volume');
    const pickerRows = modal.root.querySelector('#storage-picker-rows');
    const pickerPageAll = modal.root.querySelector('#storage-picker-page-all');
    const pickerCount = modal.root.querySelector('#storage-picker-count');
    const pickerSize = modal.root.querySelector('#storage-picker-size');
    const pickerRange = modal.root.querySelector('#storage-picker-range');
    const pickerPageLabel = modal.root.querySelector('#storage-picker-page-label');
    const submitButton = modal.root.querySelector('#storage-move-submit');

    const selectedBytes = () => {
      let bytes = 0;
      let unknown = 0;
      for (const mailbox of selected.values()) {
        if (mailbox) bytes += Number(mailbox.usedBytes) || 0;
        else unknown += 1;
      }
      return { bytes, unknown };
    };
    const notifyMoveLimit = () => ui.toast(`A single operation supports up to ${ui.num(moveLimit)} selected mailboxes. Narrow the selection, run another batch, or use a Whole domain move.`, 'warning', { title: 'Selection limit reached' });
    const addMailboxSelection = (id, mailbox) => {
      if (selected.has(id)) {
        if (mailbox) selected.set(id, mailbox);
        return true;
      }
      if (selected.size >= moveLimit) return false;
      selected.set(id, mailbox || null);
      return true;
    };

    const updateMoveSummary = () => {
      const summary = modal.root.querySelector('#storage-move-selection-summary');
      if (!summary) return;
      const target = targetSelect?.selectedOptions?.[0]?.textContent?.trim() || 'Choose a target';
      if (scope === 'domain') {
        const domain = domains.find((item) => item.id === domainSelect?.value);
        summary.innerHTML = `
          <div class="storage-move-summary-head">Move summary</div>
          <div class="storage-move-summary-row"><span>Scope</span><strong>${domain ? esc(domain.domain) : 'Whole domain'}</strong></div>
          <div class="storage-move-summary-row"><span>Mailboxes</span><strong>${domain ? `${ui.num(domain.mailboxCount)} active` : '—'}</strong></div>
          <div class="storage-move-summary-row"><span>Current volume</span><strong>${esc(domain?.volumeName || 'Legacy root')}</strong></div>
          <div class="storage-move-summary-row"><span>Destination</span><strong>${esc(target)}</strong></div>`;
        submitButton.innerHTML = `${ui.icon('forward', 14)} Preview domain plan`;
        return;
      }
      const { bytes, unknown } = selectedBytes();
      const domainCount = new Set([...selected.values()].filter(Boolean).map((mailbox) => mailbox.domain)).size;
      summary.innerHTML = `
        <div class="storage-move-summary-head">Move summary</div>
        <div class="storage-move-summary-row"><span>Selected</span><strong>${ui.num(selected.size)} of ${ui.num(moveLimit)} max</strong></div>
        <div class="storage-move-summary-row"><span>Known size</span><strong>${selected.size ? `${unknown ? '≥ ' : ''}${fmt.bytes(bytes)}` : '—'}</strong></div>
        <div class="storage-move-summary-row"><span>Domains</span><strong>${selected.size ? `${ui.num(domainCount)}${unknown ? '+' : ''}` : '—'}</strong></div>
        <div class="storage-move-summary-row"><span>Destination</span><strong>${esc(target)}</strong></div>`;
      submitButton.innerHTML = `${ui.icon('forward', 14)} Preview plan${selected.size ? ` · ${ui.num(selected.size)}` : ''}`;
    };

    const renderDomainSummary = () => {
      const host = modal.root.querySelector('#storage-move-domain-summary');
      if (!host) return;
      const domain = domains.find((item) => item.id === domainSelect?.value);
      if (!domain) {
        host.innerHTML = domains.length ? `<div class="form-note">Choose a domain to see its current placement.</div>` : '';
        updateMoveSummary();
        return;
      }
      host.innerHTML = `<div class="storage-move-domain-card">
        <div><span>Domain</span><strong>${esc(domain.domain)}</strong></div>
        <div><span>Active mailboxes</span><strong>${ui.num(domain.mailboxCount)}</strong></div>
        <div><span>Current volume</span><strong>${esc(domain.volumeName || 'Legacy root')}</strong><small class="mono">${esc(domain.mountPath || '')}</small></div>
        <div><span>Explicitly placed</span><strong>${ui.num(domain.placedCount)}</strong><small>${domain.legacy ? 'Existing mail on the legacy root' : 'Domain default is explicit'}</small></div>
      </div>`;
      updateMoveSummary();
    };

    const updatePickerSelection = () => {
      if (!pickerCount) return;
      const { bytes, unknown } = selectedBytes();
      pickerCount.textContent = selected.size ? `${ui.num(selected.size)} of ${ui.num(moveLimit)} selected` : 'No mailboxes selected';
      pickerSize.textContent = selected.size
        ? `${unknown ? 'At least ' : ''}${fmt.bytes(bytes)} known usage${unknown ? ` · ${ui.num(unknown)} size${unknown === 1 ? '' : 's'} calculated at plan time` : ''}.`
        : `Search or browse the mailbox list. A single operation supports up to ${ui.num(moveLimit)} individually selected mailboxes.`;
      modal.root.querySelectorAll('[data-picker-bulk]').forEach((button) => {
        if (button.dataset.pickerBulk === 'page' || button.dataset.pickerBulk === 'matching') button.disabled = selected.size >= moveLimit;
      });
      const pageIds = picker.rows.map((mailbox) => mailbox.id);
      const checkedOnPage = pageIds.filter((id) => selected.has(id)).length;
      if (pickerPageAll) {
        pickerPageAll.checked = pageIds.length > 0 && checkedOnPage === pageIds.length;
        pickerPageAll.indeterminate = checkedOnPage > 0 && checkedOnPage < pageIds.length;
      }
      pickerRows?.querySelectorAll('[data-picker-check]').forEach((input) => {
        const on = selected.has(input.dataset.pickerCheck);
        input.checked = on;
        const row = input.closest('tr');
        if (row) row.classList.toggle('is-selected', on);
      });
      updateMoveSummary();
    };

    const renderPicker = () => {
      if (!pickerRows) return;
      if (picker.loading) {
        pickerRows.innerHTML = `<tr><td colspan="6" class="table-empty">${ui.icon('activity', 18)}<span>Loading mailboxes…</span></td></tr>`;
        if (pickerRange) pickerRange.textContent = 'Loading mailboxes…';
        if (pickerPageLabel) pickerPageLabel.textContent = 'Page —';
        return;
      }
      if (picker.error) {
        pickerRows.innerHTML = `<tr><td colspan="6" class="table-empty">${ui.icon('alert-circle', 18)}<span>${esc(picker.error)}</span></td></tr>`;
        if (pickerRange) pickerRange.textContent = 'Could not load mailboxes';
        updatePickerSelection();
        return;
      }
      if (!picker.rows.length) {
        pickerRows.innerHTML = `<tr><td colspan="6" class="table-empty">${ui.icon('search', 18)}<span>No active mailboxes match this view.</span></td></tr>`;
        if (pickerRange) pickerRange.textContent = '0 mailboxes';
        if (pickerPageLabel) pickerPageLabel.textContent = 'Page 1 of 1';
        const prev = modal.root.querySelector('[data-picker-page="prev"]');
        const next = modal.root.querySelector('[data-picker-page="next"]');
        if (prev) prev.disabled = true;
        if (next) next.disabled = true;
        updatePickerSelection();
        return;
      }
      pickerRows.innerHTML = picker.rows.map((mailbox) => {
        const quota = Number(mailbox.quotaBytes) || 0;
        const used = Number(mailbox.usedBytes) || 0;
        const percent = quota > 0 ? Math.min(100, used / quota * 100) : 0;
        const over = quota > 0 && used > quota;
        const checked = selected.has(mailbox.id);
        const stateLabel = mailbox.state && mailbox.state !== 'ready' ? ui.badge(ui.humanTitle(mailbox.state), 'warning') : '';
        return `<tr data-mailbox-row="${esc(mailbox.id)}"${checked ? ' class="is-selected"' : ''}>
          <td class="check"><input type="checkbox" data-picker-check="${esc(mailbox.id)}"${checked ? ' checked' : ''} aria-label="Select ${esc(mailbox.email)}"></td>
          <td><span class="cell-main" title="${esc(mailbox.email)}">${esc(mailbox.email)}</span><span class="cell-sub" title="${esc([mailbox.userName, mailbox.domain].filter(Boolean).join(' · '))}">${esc([mailbox.userName, mailbox.domain].filter(Boolean).join(' · '))}${stateLabel ? ` ${stateLabel}` : ''}</span><span class="cell-sub storage-picker-mobile-volume">${esc(mailbox.volume || 'Legacy root')}</span></td>
          <td><span class="cell-main" title="${esc(mailbox.volume || 'Legacy root')}">${esc(mailbox.volume || 'Legacy root')}</span><span class="cell-sub mono" title="${esc(mailbox.mailHome || 'Legacy path')}">${esc(mailbox.mailHome || 'Legacy path')}</span></td>
          <td class="num">${fmt.bytes(used)}</td>
          <td class="num">${quota > 0 ? fmt.bytes(quota) : 'No quota'}</td>
          <td><div class="storage-usage-cell"><div class="storage-usage-track"><span class="${over ? 'is-over' : percent >= 85 ? 'is-warning' : ''}" style="width:${quota > 0 ? Math.max(2, percent) : 0}%"></span></div><em>${quota > 0 ? fmt.pct(percent, 0) : '—'}</em></div></td>
        </tr>`;
      }).join('');
      pickerRows.querySelectorAll('[data-picker-check]').forEach((input) => input.addEventListener('change', () => {
        const mailbox = picker.rows.find((item) => item.id === input.dataset.pickerCheck);
        if (input.checked) {
          if (!addMailboxSelection(input.dataset.pickerCheck, mailbox)) {
            input.checked = false;
            notifyMoveLimit();
          }
        } else {
          selected.delete(input.dataset.pickerCheck);
        }
        updatePickerSelection();
      }));
      pickerRows.querySelectorAll('[data-mailbox-row]').forEach((row) => row.addEventListener('click', (event) => {
        if (event.target.closest('input, button, a, label')) return;
        const id = row.dataset.mailboxRow;
        const mailbox = picker.rows.find((item) => item.id === id);
        if (selected.has(id)) selected.delete(id);
        else if (!addMailboxSelection(id, mailbox)) notifyMoveLimit();
        updatePickerSelection();
      }));
      const start = picker.total ? (picker.page - 1) * picker.limit + 1 : 0;
      const end = Math.min(picker.total, picker.page * picker.limit);
      if (pickerRange) pickerRange.textContent = `${ui.num(start)}–${ui.num(end)} of ${ui.num(picker.total)} mailboxes`;
      if (pickerPageLabel) pickerPageLabel.textContent = `Page ${ui.num(picker.page)} of ${ui.num(picker.pages)}`;
      const prev = modal.root.querySelector('[data-picker-page="prev"]');
      const next = modal.root.querySelector('[data-picker-page="next"]');
      if (prev) prev.disabled = picker.page <= 1;
      if (next) next.disabled = picker.page >= picker.pages;
      updatePickerSelection();
    };

    const loadPicker = async () => {
      const seq = ++pickerSeq;
      picker.loading = true;
      picker.error = '';
      renderPicker();
      try {
        const params = new URLSearchParams({ page: String(picker.page), limit: String(picker.limit) });
        if (picker.q) params.set('q', picker.q);
        if (picker.volume) params.set('volume', picker.volume);
        const payload = await ctx.api(`/control/api/storage/mailboxes?${params.toString()}`);
        if (seq !== pickerSeq || !ctx.isCurrent()) return;
        picker.rows = Array.isArray(payload.mailboxes) ? payload.mailboxes : [];
        picker.total = Number(payload.total) || 0;
        picker.pages = Math.max(1, Number(payload.pages) || Math.ceil(picker.total / picker.limit) || 1);
        if (picker.page > picker.pages) {
          picker.page = picker.pages;
          return loadPicker();
        }
        picker.rows.forEach((mailbox) => { if (selected.has(mailbox.id)) selected.set(mailbox.id, mailbox); });
      } catch (error) {
        if (seq === pickerSeq) picker.error = errorText(error);
      } finally {
        if (seq === pickerSeq) {
          picker.loading = false;
          renderPicker();
        }
      }
    };

    const setScope = (next) => {
      scope = next === 'mailbox' ? 'mailbox' : 'domain';
      modal.root.querySelectorAll('[data-move-scope]').forEach((button) => {
        const active = button.dataset.moveScope === scope;
        button.classList.toggle('is-active', active);
        button.setAttribute('aria-selected', active ? 'true' : 'false');
        button.tabIndex = active ? 0 : -1;
      });
      if (domainPane) domainPane.hidden = scope !== 'domain';
      if (mailboxPane) mailboxPane.hidden = scope !== 'mailbox';
      if (scope === 'mailbox' && !pickerLoaded) {
        pickerLoaded = true;
        loadPicker();
      }
      updateMoveSummary();
    };

    modal.root.querySelectorAll('[data-move-scope]').forEach((button) => button.addEventListener('click', () => setScope(button.dataset.moveScope)));
    domainSelect?.addEventListener('change', renderDomainSummary);
    targetSelect?.addEventListener('change', updateMoveSummary);
    pickerSearch?.addEventListener('input', () => {
      if (searchTimer) clearTimeout(searchTimer);
      searchTimer = setTimeout(() => {
        picker.q = pickerSearch.value.trim();
        picker.page = 1;
        if (pickerLoaded) loadPicker();
      }, 300);
    });
    pickerSearch?.addEventListener('keydown', (event) => {
      if (event.key !== 'Enter') return;
      event.preventDefault();
      if (searchTimer) clearTimeout(searchTimer);
      picker.q = pickerSearch.value.trim();
      picker.page = 1;
      if (pickerLoaded) loadPicker();
    });
    pickerVolume?.addEventListener('change', () => {
      picker.volume = pickerVolume.value;
      picker.page = 1;
      if (pickerLoaded) loadPicker();
    });
    pickerPageAll?.addEventListener('change', () => {
      let capped = false;
      for (const mailbox of picker.rows) {
        if (pickerPageAll.checked) {
          if (!addMailboxSelection(mailbox.id, mailbox)) capped = true;
        } else {
          selected.delete(mailbox.id);
        }
      }
      if (capped) notifyMoveLimit();
      updatePickerSelection();
      renderPickerRowsOnly();
    });
    modal.root.querySelectorAll('[data-picker-page]').forEach((button) => button.addEventListener('click', () => {
      const dir = button.dataset.pickerPage;
      if (dir === 'prev' && picker.page > 1) picker.page -= 1;
      else if (dir === 'next' && picker.page < picker.pages) picker.page += 1;
      else return;
      loadPicker();
    }));
    modal.root.querySelectorAll('[data-picker-bulk]').forEach((button) => button.addEventListener('click', async () => {
      const action = button.dataset.pickerBulk;
      if (action === 'page') {
        let capped = false;
        for (const mailbox of picker.rows) {
          if (!addMailboxSelection(mailbox.id, mailbox)) capped = true;
        }
        if (capped) notifyMoveLimit();
        updatePickerSelection();
        renderPickerRowsOnly();
        return;
      }
      if (action === 'clear') {
        selected.clear();
        updatePickerSelection();
        renderPickerRowsOnly();
        return;
      }
      if (action === 'matching') {
        button.disabled = true;
        try {
          const params = new URLSearchParams({ idsOnly: '1', limit: String(moveLimit) });
          if (picker.q) params.set('q', picker.q);
          if (picker.volume) params.set('volume', picker.volume);
          const payload = await ctx.api(`/control/api/storage/mailboxes?${params.toString()}`);
          if (!ctx.isCurrent()) return;
          const ids = Array.isArray(payload.ids) ? payload.ids : [];
          let capped = false;
          ids.forEach((id) => { if (!addMailboxSelection(id, null)) capped = true; });
          picker.rows.forEach((mailbox) => { if (selected.has(mailbox.id)) selected.set(mailbox.id, mailbox); });
          if (!ids.length) ui.toast('No mailboxes match this view.', 'info');
          else if (payload.truncated || capped) notifyMoveLimit();
          updatePickerSelection();
          renderPickerRowsOnly();
        } catch (error) {
          ui.toast(errorText(error), 'critical', { title: 'Could not select matching mailboxes' });
        } finally {
          updatePickerSelection();
        }
      }
    }));

    // Re-render only the checkbox state for bulk row operations. The rows
    // themselves do not change, so this avoids a needless table rebuild.
    const renderPickerRowsOnly = () => {
      pickerRows?.querySelectorAll('[data-picker-check]').forEach((input) => {
        const on = selected.has(input.dataset.pickerCheck);
        input.checked = on;
        input.closest('tr')?.classList.toggle('is-selected', on);
      });
      updatePickerSelection();
    };

    form.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      if (!values.targetVolumeId) {
        ui.toast('Choose a target volume.', 'warning');
        return;
      }
      const body = { scope, targetVolumeId: values.targetVolumeId, verify: values.verify, graceHours: Number(values.graceHours) || 24 };
      if (scope === 'domain') {
        if (!values.domainId) {
          ui.toast('Choose a verified domain to move.', 'warning');
          return;
        }
        body.domainId = values.domainId;
      } else {
        const ids = [...selected.keys()];
        if (!ids.length) {
          ui.toast('Select at least one mailbox to move.', 'warning');
          return;
        }
        if (ids.length > moveLimit) {
          notifyMoveLimit();
          return;
        }
        body.mailboxIds = ids;
      }
      submitButton.classList.add('is-loading');
      submitButton.disabled = true;
      try {
        const result = await ctx.api('/control/api/storage/moves/plan', { method: 'POST', body });
        modal.close();
        showMoveReview(result);
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not plan the move' });
        submitButton.classList.remove('is-loading');
        submitButton.disabled = false;
      }
    });

    setScope('domain');
    renderDomainSummary();
  };

  const showMoveReview = (planResult) => {
    const plan = planResult.plan || {};
    const summary = planResult.summary || {};
    const items = Array.isArray(plan.items) ? plan.items : [];
    const review = ctx.modal({
      title: 'Review move plan', subtitle: 'Nothing has been copied yet. Confirm to start the verified move.',
      size: 'medium',
      body: `<div class="notice tone-warning">${ui.icon('alert-circle', 17)}<div><strong>Source mail is retained after the move</strong><p>The source copy stays on ${esc(items[0]?.sourceVolumeName || 'its current volume')} for ${ui.num(summary.graceHours || 24)} hours after verification. Finalization is the irreversible step.</p></div></div>
        <div class="storage-review-grid"><div><span>Mailboxes</span><strong>${ui.num(summary.mailboxes || 0)}</strong></div><div><span>Data to copy</span><strong>${fmt.bytes(summary.bytes || 0)}</strong></div><div><span>Target</span><strong>${esc(summary.targetVolume || '')}</strong></div><div><span>Free after</span><strong>${fmt.bytes(summary.freeAfter || 0)}</strong></div></div>
        <div class="table-wrap" style="margin-top:12px"><table class="data compact"><thead><tr><th>Mailbox</th><th>From</th><th>To</th><th class="num">Size</th></tr></thead><tbody>${items.slice(0, 30).map((item) => `<tr><td>${esc(item.email)}</td><td>${esc(item.sourceVolumeName || 'legacy')}</td><td>${esc(item.targetVolumeName || '')}</td><td class="num">${fmt.bytes(item.bytes || 0)}</td></tr>`).join('')}${items.length > 30 ? `<tr><td colspan="4" class="muted">… and ${ui.num(items.length - 30)} more</td></tr>` : ''}</tbody></table></div>
        <label class="check-field" style="margin-top:12px"><input type="checkbox" id="storage-move-confirm"><span>I understand that the source is retained for the grace window and finalization permanently removes it.</span></label>`,
      footer: `<button class="btn btn-ghost" type="button" data-modal-close>Cancel</button><button class="btn btn-primary" type="button" id="storage-move-execute" disabled>${ui.icon('forward', 14)} Execute move</button>`,
    });
    const checkbox = review.root.querySelector('#storage-move-confirm');
    const execute = review.root.querySelector('#storage-move-execute');
    checkbox?.addEventListener('change', () => { execute.disabled = !checkbox.checked; });
    execute?.addEventListener('click', async () => {
      execute.classList.add('is-loading'); execute.disabled = true;
      try {
        await ctx.api('/control/api/storage/moves/execute', { method: 'POST', body: { operationId: planResult.operationId, token: planResult.token, confirm: true } });
        review.close();
        ui.toast('Move queued. The source remains retained until verification and finalization.', 'good');
        await loadOperations();
        openOperationConsole(planResult.operationId);
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not start the move' });
        execute.classList.remove('is-loading'); execute.disabled = false;
      }
    });
  };

  const paintHost = () => {
    const element = document.getElementById('storage-host');
    if (!element) return;
    if (!hostInfo) { element.innerHTML = ui.loadingState('Checking host capabilities…', 3); return; }
    const caps = hostInfo.capabilities || {};
    const policy = hostInfo.policy || {};
    const enabled = Boolean(caps.provisioningEnabled);
    const canChange = ctx.can('storage.destroy');
    const missing = Object.entries(caps.tools || {}).filter(([, available]) => !available).map(([tool]) => tool);
    const tone = !caps.supported || !enabled ? 'warning' : 'good';
    const title = !caps.supported ? 'Host provisioning unavailable' : enabled ? 'Host provisioning enabled' : 'Host provisioning is locked';
    const message = !caps.supported
      ? caps.reason || 'This host cannot perform managed disk operations.'
      : enabled
        ? 'The runner may partition, format and mount a verified blank or explicitly confirmed disk. Every plan is revalidated at execution.'
        : 'A superadmin must enable host disk provisioning before Add disk can run. Moves and placement remain available.';
    element.innerHTML = `
      ${ui.inlineNotice(tone, title, message)}
      <div class="storage-host-tools">${missing.length ? `<span class="muted">Missing tools:</span> ${missing.map((tool) => ui.badge(tool, 'warning')).join(' ')}` : `<span class="muted">${caps.supported ? 'Discovery and mount tools are present.' : 'Tool inventory is unavailable on this platform.'}</span>`}</div>
      ${canChange ? `<label class="switch-field" style="margin-top:12px"><span class="switch"><input type="checkbox" id="storage-provisioning-toggle"${enabled ? ' checked' : ''}${caps.supported ? '' : ' disabled'}><span class="switch-track"><span></span></span></span><span class="switch-copy"><strong>Allow host disk provisioning</strong><small>Superadmin only. Enabling exposes the Add disk, Extend and Retire operations. The environment kill switch remains authoritative.</small></span></label>` : `<p class="form-note" style="margin-top:10px">A superadmin can enable host disk provisioning.</p>`}
      <p class="form-note" style="margin-top:8px">Fstab is edited through marked blocks only. The original is backed up; a failed validation restores it.</p>`;
    document.getElementById('storage-provisioning-toggle')?.addEventListener('change', async (event) => {
      const input = event.currentTarget;
      input.disabled = true;
      try {
        await ctx.api('/control/api/storage/policy', { method: 'PATCH', body: { provisioningEnabled: input.checked } });
        ui.toast(input.checked ? 'Host disk provisioning enabled.' : 'Host disk provisioning locked.', 'good');
        await Promise.all([loadHost(), load()]);
      } catch (error) {
        input.checked = !input.checked;
        input.disabled = false;
        ui.toast(errorText(error), 'critical', { title: 'Could not change the provisioning gate' });
      }
    });
    updateHostControls();
    if (swapInfo) paintSwap();
  };

  const updateHostControls = () => {
    const add = document.getElementById('storage-add-disk');
    if (!add) return;
    const caps = hostInfo?.capabilities;
    const ready = Boolean(canManage && caps?.supported && caps?.canPartition && caps?.provisioningEnabled && caps?.tools?.['systemd-run'] !== false);
    add.disabled = !ready;
    add.title = !caps ? 'Loading host capabilities' : !caps.supported ? caps.reason || 'Host provisioning is unavailable' : !caps.provisioningEnabled ? 'A superadmin must enable host disk provisioning' : !caps.canPartition ? 'sgdisk or parted is required on the host' : 'Add a mail volume';
  };

  const loadHost = async () => {
    try {
      const payload = await ctx.api('/control/api/storage/host');
      if (!ctx.isCurrent()) return;
      hostInfo = payload;
      paintHost();
    } catch (error) {
      const element = document.getElementById('storage-host');
      if (element && ctx.isCurrent()) element.innerHTML = ui.inlineNotice('critical', 'Could not inspect host provisioning', errorText(error));
    }
  };

  const openAddDiskWizard = async () => {
    if (!hostInfo?.capabilities?.supported || !hostInfo?.capabilities?.canPartition || !hostInfo?.capabilities?.provisioningEnabled) {
      ui.toast(hostInfo?.capabilities?.reason || 'Host disk provisioning is locked.', 'warning');
      return;
    }
    if (!disks) await loadDisks();
    const flatten = (list, out = []) => {
      for (const device of (list || [])) {
        out.push(device);
        flatten(device.children || [], out);
      }
      return out;
    };
    const all = flatten(disks?.devices || []);
    const initializeDisks = all.filter((device) => device.type === 'disk' && !device.systemProtected && !device.mountpoint);
    const registered = new Set((overview?.volumes || []).map((volume) => volume.mountPath));
    const adoptable = all.filter((device) => device.mountpoint && ['ext4', 'xfs', 'nfs', 'nfs4'].includes(device.fsType) && !registered.has(device.mountpoint));
    if (!initializeDisks.length && !adoptable.length) {
      ui.toast('No unused block devices were found. Use adopt mode and enter a mounted ext4, XFS or NFS path.', 'warning');
    }
    const nextName = `mail-${(overview?.volumes || []).length + 1}`;
    const root = hostInfo?.capabilities?.storageRoot || '/srv/a8m-storage';
    const diskByPath = new Map(initializeDisks.map((disk) => [disk.path, disk]));
    const adoptByPath = new Map(adoptable.map((disk) => [disk.mountpoint, disk]));
    const modal = ctx.modal({
      title: 'Add a mail volume', subtitle: 'Discover, plan and verify a host filesystem. Nothing is written until you execute the reviewed plan.',
      size: 'medium',
      body: `<form id="storage-add-disk-form"><div class="form-grid">
        <div class="field"><label for="storage-add-mode">Mode</label><select class="input" id="storage-add-mode" name="mode"><option value="initialize">Initialize a blank or existing disk</option><option value="adopt">Adopt an existing mounted filesystem</option></select><small class="field-help">Initialize partitions and formats. Adopt registers a filesystem that is already mounted and leaves it untouched.</small></div>
        <div id="storage-add-device-wrap">${ui.field({ label: 'Disk', name: 'devicePath', type: 'select', options: initializeDisks.map((disk) => ({ value: disk.path, label: `${disk.path} · ${fmt.bytes(disk.sizeBytes)}${disk.serial ? ` · ${disk.serial}` : ''}${disk.hasSignatures || disk.children?.length ? ' · existing data' : ''}` })) })}</div>
        <div id="storage-add-adopt-wrap" hidden>${ui.field({ label: 'Mounted filesystem', name: 'adoptPath', type: 'select', options: [...adoptable.map((disk) => ({ value: disk.mountpoint, label: `${disk.mountpoint} · ${disk.fsType} · ${fmt.bytes(disk.sizeBytes)}` })), { value: '__manual__', label: 'Enter another mounted path (including NFS)' }], help: 'The path must already be mounted. NFS exports are adopted without changing the export or its mount options.' })}</div>
        ${ui.field({ label: 'Volume name', name: 'name', value: nextName, required: true, help: 'Letters, digits and hyphens. Used for the registry, filesystem label and fstab marker.' })}
        ${ui.field({ label: 'Mount path', name: 'mountPath', value: `${root}/${nextName}`, required: true, help: 'Must be empty. New volumes live under the managed storage root.' })}
        <div id="storage-add-fs-wrap">${ui.field({ label: 'Filesystem', name: 'fsType', type: 'select', value: 'ext4', options: [{ value: 'ext4', label: 'ext4 · online grow' }, { value: 'xfs', label: 'XFS · online grow' }] })}</div>
      </div><div class="form-actions"><span class="form-note">The planner revalidates the disk serial, mount state and signatures before execution.</span><div class="form-actions-right"><button class="btn btn-ghost" type="button" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">Preview plan</button></div></div></form>`,
    });
    const form = modal.root.querySelector('#storage-add-disk-form');
    const mode = form?.querySelector('[name="mode"]');
    const deviceWrap = form?.querySelector('#storage-add-device-wrap');
    const adoptWrap = form?.querySelector('#storage-add-adopt-wrap');
    const fsWrap = form?.querySelector('#storage-add-fs-wrap');
    const mountInput = form?.querySelector('[name="mountPath"]');
    const nameInput = form?.querySelector('[name="name"]');
    const syncMode = () => {
      const adopt = mode?.value === 'adopt';
      if (deviceWrap) deviceWrap.hidden = adopt;
      if (adoptWrap) adoptWrap.hidden = !adopt;
      if (fsWrap) fsWrap.hidden = adopt;
      const manual = adopt && adoptWrap?.querySelector('select')?.value === '__manual__';
      if (mountInput) mountInput.readOnly = adopt && !manual;
      if (adopt && adoptWrap) {
        const selected = adoptByPath.get(adoptWrap.querySelector('select')?.value);
        if (selected) {
          if (mountInput) mountInput.value = selected.mountpoint;
          if (nameInput) nameInput.value = selected.mountpoint.split('/').filter(Boolean).pop()?.toLowerCase().replace(/[^a-z0-9]+/g, '-').slice(0, 48) || nextName;
        } else if (manual && mountInput) {
          mountInput.value = '';
        }
      }
    };
    mode?.addEventListener('change', syncMode);
    adoptWrap?.querySelector('select')?.addEventListener('change', syncMode);
    nameInput?.addEventListener('input', () => {
      if (mode?.value === 'adopt' || !mountInput) return;
      mountInput.value = `${root}/${nameInput.value.trim().toLowerCase()}`;
    });
    syncMode();
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        const adopt = values.mode === 'adopt';
        const manualAdopt = adopt && values.adoptPath === '__manual__';
        const disk = adopt && !manualAdopt ? adoptByPath.get(values.adoptPath) : diskByPath.get(values.devicePath);
        if (!adopt && !disk) throw new Error('Choose a disk');
        const adoptPath = manualAdopt ? values.mountPath : values.adoptPath;
        if (adopt && !adoptPath) throw new Error('Enter the mounted path to adopt');
        const payload = adopt
          ? { kind: 'adopt', name: values.name, mountPath: adoptPath, fsType: disk?.fsType || '' }
          : { kind: 'add', name: values.name, mountPath: values.mountPath, devicePath: disk.path, deviceSerial: disk.serial || '', deviceWwn: disk.wwn || '', fsType: values.fsType, mode: 'initialize' };
        const result = await ctx.api('/control/api/storage/volumes/plan', { method: 'POST', body: payload });
        modal.close();
        showVolumeReview(result);
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not plan the volume' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  const openExtendWizard = async (volumeId) => {
    if (!hostInfo?.capabilities?.supported || !hostInfo?.capabilities?.provisioningEnabled) {
      ui.toast('Host disk provisioning is locked.', 'warning');
      return;
    }
    const volume = (overview?.volumes || []).find((item) => item.id === volumeId);
    if (!volume) return;
    if (!disks) await loadDisks();
    const flatten = (list, out = []) => {
      for (const device of (list || [])) { out.push(device); flatten(device.children || [], out); }
      return out;
    };
    const candidates = flatten(disks?.devices || []).filter((device) => device.type === 'disk' && !device.systemProtected && !device.mountpoint && !device.hasSignatures && !device.children?.length);
    const modal = ctx.modal({
      title: 'Extend ' + volume.name, subtitle: 'Grow the filesystem online. Adding a new disk is only used for thick LVM.',
      size: 'small',
      body: `<form id="storage-extend-form"><div class="form-grid is-single">
        <div class="note-strip">${ui.icon('info', 13)} <span>${esc(volume.mountPath)} · current size ${fmt.bytes(volume.totalBytes)}</span></div>
        ${ui.field({ label: 'New blank disk for thick LVM', name: 'devicePath', type: 'select', options: [{ value: '', label: 'No new disk — grow in place' }, ...candidates.map((disk) => ({ value: disk.path, label: `${disk.path} · ${fmt.bytes(disk.sizeBytes)}${disk.serial ? ` · ${disk.serial}` : ''}` }))], help: 'If the volume is not on thick LVM, grow the virtual disk in the hypervisor first and leave this empty.' })}
      </div><div class="form-actions"><span class="form-note">The runner validates the filesystem and free space after growth.</span><div class="form-actions-right"><button class="btn btn-ghost" type="button" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">Preview plan</button></div></div></form>`,
    });
    const form = modal.root.querySelector('#storage-extend-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        const disk = candidates.find((item) => item.path === values.devicePath);
        const result = await ctx.api('/control/api/storage/volumes/plan', { method: 'POST', body: {
          kind: 'extend', volumeId, devicePath: disk?.path || '', deviceSerial: disk?.serial || '', deviceWwn: disk?.wwn || '',
        } });
        modal.close();
        showVolumeReview(result);
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not plan volume expansion' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  const openRetireWizard = async (volumeId) => {
    if (!hostInfo?.capabilities?.supported || !hostInfo?.capabilities?.provisioningEnabled) {
      ui.toast('Host disk provisioning is locked.', 'warning');
      return;
    }
    try {
      const result = await ctx.api('/control/api/storage/volumes/plan', { method: 'POST', body: { kind: 'retire', volumeId } });
      showVolumeReview(result);
    } catch (error) {
      ui.toast(errorText(error), 'critical', { title: 'Could not plan volume retirement' });
    }
  };

  const openWipeWizard = async (volumeId) => {
    if (!hostInfo?.capabilities?.supported || !hostInfo?.capabilities?.provisioningEnabled) {
      ui.toast('Host disk provisioning is locked.', 'warning');
      return;
    }
    if (!ctx.can('storage.destroy')) {
      ui.toast('Wiping a volume requires the storage.destroy permission.', 'warning');
      return;
    }
    try {
      const result = await ctx.api('/control/api/storage/volumes/plan', { method: 'POST', body: { kind: 'wipe', volumeId } });
      showVolumeReview(result);
    } catch (error) {
      ui.toast(errorText(error), 'critical', { title: 'Could not plan the volume wipe' });
    }
  };

  const showVolumeReview = (planResult) => {
    const plan = planResult.plan || {};
    const commands = Array.isArray(plan.commands) ? plan.commands : [];
    const warnings = Array.isArray(plan.warnings) ? plan.warnings : [];
    const expected = plan.confirmValue || '';
    const title = plan.kind === 'volume_adopt' ? 'Adopt volume' : plan.kind === 'volume_extend' ? 'Extend volume' : plan.kind === 'volume_retire' ? 'Retire volume' : plan.kind === 'volume_wipe' ? 'Wipe volume' : 'Add volume';
    const modal = ctx.modal({
      title: 'Review ' + title.toLowerCase(), subtitle: 'Nothing has been executed. The runner revalidates the host before the first write.',
      size: 'medium',
      body: `${warnings.map((warning) => ui.inlineNotice('warning', 'Review before continuing', warning)).join('')}
        <div class="storage-review-grid"><div><span>Name</span><strong>${esc(plan.name || plan.volumeName || '')}</strong></div><div><span>Mount</span><strong class="mono">${esc(plan.mountPath || '')}</strong></div><div><span>Device</span><strong class="mono">${esc(plan.devicePath || '—')}</strong></div><div><span>Filesystem</span><strong>${esc(plan.fsType || '—')}</strong></div></div>
        ${commands.length ? `<h4 style="margin-top:14px">Host operations</h4><pre class="storage-log">${commands.map((command) => esc(command)).join('\n')}</pre>` : '<p class="form-note" style="margin-top:12px">No formatting or mount commands are required for this plan.</p>'}
        <div class="field" style="margin-top:13px"><label for="storage-volume-confirm">Type <strong class="mono">${esc(expected)}</strong> to confirm</label><input class="input mono" id="storage-volume-confirm" autocomplete="off" spellcheck="false"></div>`,
      footer: `<button class="btn btn-ghost" type="button" data-modal-close>Cancel</button><button class="btn ${plan.requireDestroy ? 'btn-danger' : 'btn-primary'}" type="button" id="storage-volume-execute" disabled>${ui.icon('play', 14)} Execute</button>`,
    });
    const input = modal.root.querySelector('#storage-volume-confirm');
    const execute = modal.root.querySelector('#storage-volume-execute');
    input?.addEventListener('input', () => { execute.disabled = input.value.trim() !== expected; });
    execute?.addEventListener('click', async () => {
      execute.classList.add('is-loading'); execute.disabled = true;
      try {
        await ctx.api('/control/api/storage/volumes/execute', { method: 'POST', body: {
          operationId: planResult.operationId, token: planResult.token, confirm: true, confirmValue: input.value.trim(),
        } });
        modal.close();
        ui.toast('Host operation queued. Follow the runner in Storage operations.', 'good');
        await Promise.all([loadHost(), loadOperations()]);
        openOperationConsole(planResult.operationId);
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not execute the volume plan' });
        execute.classList.remove('is-loading'); execute.disabled = false;
      }
    });
  };

  const swapTypeLabel = (type) => {
    const value = String(type || '').toLowerCase();
    if (value === 'file') return 'Swap file';
    if (value === 'partition') return 'Drive / partition';
    return ui.humanTitle(value || 'Swap');
  };

  const swapBootBadge = (entry) => {
    if (entry.managed) return ui.badge('Managed boot', 'good', 'check-circle');
    if (entry.persistent) return ui.badge('In fstab', 'info', 'archive');
    return ui.badge('Session only', 'neutral', 'alert-circle');
  };

  const paintSwap = () => {
    const host = document.getElementById('storage-swap');
    const create = document.getElementById('storage-swap-create');
    if (!host) return;
    if (!swapInfo) {
      host.innerHTML = ui.loadingState('Reading host swap…', 3);
      if (create) create.disabled = true;
      return;
    }
    const info = swapInfo;
    const tools = info.tools || {};
    const missingTools = ['mkswap', 'swapon', 'swapoff', 'fallocate'].filter((tool) => tools[tool] === false);
    const provisioningKnown = Boolean(hostInfo);
    const provisioning = Boolean(hostInfo?.capabilities?.provisioningEnabled);
    const ready = Boolean(canManage && info.supported && provisioningKnown && provisioning && missingTools.length === 0);
    if (create) {
      create.disabled = !ready;
      create.title = !info.supported
        ? info.reason || 'Swap management is unavailable on this host'
        : !provisioningKnown
          ? 'Loading host capabilities'
          : !provisioning
            ? 'A superadmin must enable host disk provisioning'
          : missingTools.length
            ? `Install ${missingTools.join(', ')} on the host`
            : 'Create a swap file or dedicate a disk to swap';
    }
    if (!info.supported) {
      host.innerHTML = ui.inlineNotice('info', 'Swap management unavailable', info.reason || 'Swap can be managed on the Linux control host only.');
      return;
    }
    const entries = Array.isArray(info.entries) ? info.entries : [];
    const total = Number(info.totalBytes) || 0;
    const used = Number(info.usedBytes) || 0;
    const free = Number(info.freeBytes) || 0;
    const percent = total > 0 ? used / total * 100 : 0;
    const candidates = Array.isArray(info.candidates) ? info.candidates : [];
    const devices = Array.isArray(info.devices) ? info.devices : [];
    const eligibleCandidates = candidates.filter((candidate) => candidate.eligible).length;
    const eligibleDevices = devices.filter((device) => device.eligible).length;
    host.innerHTML = `
      ${provisioningKnown && !provisioning ? ui.inlineNotice('warning', 'Swap creation is locked', 'A superadmin must enable host disk provisioning before creating or changing swap.') : ''}
      ${missingTools.length ? ui.inlineNotice('warning', 'Host swap tools are missing', `Install ${missingTools.join(', ')} before creating swap.`) : ''}
      <div class="storage-swap-summary">
        <div><span>Total swap</span><strong>${total > 0 ? fmt.bytes(total) : 'None'}</strong><small>${entries.length ? `${ui.num(entries.length)} active area${entries.length === 1 ? '' : 's'}` : 'No active area'}</small></div>
        <div><span>Used</span><strong>${fmt.bytes(used)}</strong><small>${total > 0 ? `${fmt.pct(percent)} of swap` : 'No swap active'}</small></div>
        <div><span>Free</span><strong>${total > 0 ? fmt.bytes(free) : '—'}</strong><small>${total > 0 ? 'Available to the kernel' : 'No swap active'}</small></div>
        <div><span>Host memory</span><strong>${info.memoryTotalBytes ? fmt.bytes(info.memoryTotalBytes) : '—'}</strong><small>${info.memoryFreeBytes ? `${fmt.bytes(info.memoryFreeBytes)} available` : 'Memory details unavailable'}</small></div>
      </div>
      ${total > 0 ? `<div class="storage-swap-meter">${ui.meter({ label: 'Swap used', value: percent, detail: `${fmt.bytes(used)} of ${fmt.bytes(total)}`, tone: percent >= 85 ? 'warning' : '' })}</div>` : ''}
      ${entries.length ? `<div class="table-wrap storage-swap-table-wrap"><table class="data compact"><thead><tr><th>Swap area</th><th>Type</th><th class="num">Size</th><th class="num">Used</th><th class="num">Priority</th><th>Boot</th></tr></thead><tbody>${entries.map((entry) => `<tr><td><span class="cell-main mono">${esc(entry.path)}</span><span class="cell-sub">${entry.managed ? 'Managed by Alt8Mail' : 'External or manual swap'}</span></td><td>${esc(swapTypeLabel(entry.type))}</td><td class="num">${fmt.bytes(entry.sizeBytes)}</td><td class="num">${fmt.bytes(entry.usedBytes)}</td><td class="num">${ui.num(entry.priority)}</td><td>${swapBootBadge(entry)}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No swap configured', 'Create a swap file on an online mail volume, or dedicate a blank disk to host memory overflow.', '', 'gauge')}
      <div class="storage-swap-foot"><span>vm.swappiness ${ui.num(info.swappiness)}</span><span>${ui.num(eligibleCandidates)} volume source${eligibleCandidates === 1 ? '' : 's'} · ${ui.num(eligibleDevices)} disk${eligibleDevices === 1 ? '' : 's'} eligible</span><span>Swap is host memory overflow, not mail storage.</span></div>`;
  };

  const loadSwap = async () => {
    const host = document.getElementById('storage-swap');
    try {
      const payload = await ctx.api('/control/api/storage/swap');
      if (!ctx.isCurrent()) return false;
      swapInfo = payload;
      paintSwap();
      return true;
    } catch (error) {
      if (!ctx.isCurrent()) return false;
      if (!swapInfo && host) host.innerHTML = ui.inlineNotice('critical', 'Could not load swap', errorText(error));
      const create = document.getElementById('storage-swap-create');
      if (create) create.disabled = true;
      return false;
    }
  };

  const openSwapWizard = async () => {
    if (!swapInfo) await loadSwap();
    if (!swapInfo) {
      ui.toast('Swap status is still loading.', 'info');
      return;
    }
    if (!swapInfo.supported) {
      ui.toast(swapInfo.reason || 'Swap management is unavailable on this host.', 'warning');
      return;
    }
    if (!hostInfo) await loadHost();
    if (!hostInfo?.capabilities?.provisioningEnabled) {
      ui.toast('A superadmin must enable host disk provisioning before creating swap.', 'warning');
      return;
    }
    const tools = swapInfo.tools || {};
    const missingTools = ['mkswap', 'swapon', 'swapoff', 'fallocate'].filter((tool) => !tools[tool]);
    if (missingTools.length) {
      ui.toast(`Install ${missingTools.join(', ')} on the host before creating swap.`, 'warning');
      return;
    }
    const candidates = Array.isArray(swapInfo.candidates) ? swapInfo.candidates : [];
    const devices = Array.isArray(swapInfo.devices) ? swapInfo.devices : [];
    const fileCandidates = candidates.filter((candidate) => candidate.eligible);
    const deviceCandidates = devices.filter((device) => device.eligible);
    const canDestroy = ctx.can('storage.destroy');
    let mode = fileCandidates.length ? 'file' : deviceCandidates.length && canDestroy ? 'device' : 'file';
    const volumeOptions = candidates.length ? candidates.map((candidate) => ({
      value: candidate.volumeId,
      label: `${candidate.name} · ${fmt.bytes(candidate.freeBytes)} free · ${candidate.fsType}${candidate.eligible ? '' : ` — ${candidate.reason || 'not eligible'}`}`,
      disabled: !candidate.eligible,
    })) : [{ value: '', label: 'No registered online volume', disabled: true }];
    const deviceOptions = devices.length ? devices.map((device) => ({
      value: device.path,
      label: `${device.path} · ${fmt.bytes(device.sizeBytes)}${device.model ? ` · ${device.model}` : ''}${device.eligible ? '' : ` — ${device.reason || 'not eligible'}`}`,
      disabled: !device.eligible,
    })) : [{ value: '', label: 'No whole-disk device found', disabled: true }];
    const defaultVolume = fileCandidates[0]?.volumeId || '';
    const defaultDevice = deviceCandidates[0]?.path || '';
    const recommendedGiB = Math.max(0.0625, Math.round((Number(swapInfo.recommendedSizeBytes) || 2 * 1024 ** 3) / 1024 ** 3 * 4) / 4);
    const modal = ctx.modal({
      title: 'Create swap',
      subtitle: 'Allocate host memory overflow as a swap file or dedicate a whole disk.',
      size: 'large',
      body: `<form id="storage-swap-form">
        <div class="storage-swap-layout">
          <div class="storage-swap-main">
            <div class="storage-swap-tabs" role="tablist" aria-label="Swap type">
              <button class="storage-swap-tab${mode === 'file' ? ' is-active' : ''}" id="storage-swap-tab-file" type="button" role="tab" tabindex="0" aria-selected="${mode === 'file' ? 'true' : 'false'}" aria-controls="storage-swap-file-pane" data-swap-mode="file">
                <span class="storage-swap-tab-icon">${ui.icon('file-plus', 18)}</span>
                <span><strong>Swap file</strong><small>Use free space on an ext4 or XFS mail volume</small></span>
              </button>
              <button class="storage-swap-tab${mode === 'device' ? ' is-active' : ''}" id="storage-swap-tab-device" type="button" role="tab" tabindex="-1" aria-selected="${mode === 'device' ? 'true' : 'false'}" aria-controls="storage-swap-device-pane" data-swap-mode="device">
                <span class="storage-swap-tab-icon">${ui.icon('disk', 18)}</span>
                <span><strong>Dedicated disk</strong><small>Erase a blank disk and dedicate it to swap</small></span>
              </button>
            </div>
            <div class="storage-swap-pane" id="storage-swap-file-pane" role="tabpanel" aria-labelledby="storage-swap-tab-file"${mode === 'file' ? '' : ' hidden'}>
              <div class="storage-swap-recommend">
                <span class="storage-swap-recommend-icon">${ui.icon('gauge', 18)}</span>
                <div><span>Recommended starting point</span><strong>${fmt.bytes(swapInfo.recommendedSizeBytes || 2 * 1024 ** 3)}</strong><small>Guided by ${swapInfo.memoryTotalBytes ? fmt.bytes(swapInfo.memoryTotalBytes) : 'host'} memory. Adjust to your workload.</small></div>
              </div>
              ${fileCandidates.length ? ui.field({ label: 'Mail volume', name: 'swapVolumeId', type: 'select', value: defaultVolume, options: volumeOptions, help: 'The hidden swap file is placed at the root of this volume. NFS and non-ext4/XFS volumes are not eligible.' }) : `<div class="notice tone-warning"><span class="notice-icon">${ui.icon('alert-circle', 17)}</span><div><strong>No eligible mail volume</strong><p>${candidates.length ? 'Every registered volume is offline, NFS-backed, or short on space after the capacity reserve.' : 'Register an online ext4 or XFS volume before creating a swap file. You can still dedicate a blank disk.'}</p></div></div>`}
              <div class="storage-swap-size-row">
                <div class="field"><label for="storage-swap-size">Size (GiB)</label><input class="input" id="storage-swap-size" name="swapSizeGib" type="number" min="0.0625" step="any" value="${recommendedGiB}"${fileCandidates.length ? '' : ' disabled'}><small class="field-help">Between 64 MiB and the volume's swap-safe free space.</small></div>
                <div class="storage-swap-quick" role="group" aria-label="Suggested swap sizes">${[1, 2, 4, 8, 16].map((size) => `<button class="btn btn-ghost btn-sm" type="button" data-swap-size="${size}">${size} GiB</button>`).join('')}</div>
              </div>
              <div id="storage-swap-file-preview" class="storage-swap-preview"></div>
              <p class="form-note">A managed fstab entry will enable the swap file at boot. Creating it changes capacity accounting but never moves or rewrites mailbox data.</p>
            </div>
            <div class="storage-swap-pane" id="storage-swap-device-pane" role="tabpanel" aria-labelledby="storage-swap-tab-device"${mode === 'device' ? '' : ' hidden'}>
              ${!canDestroy ? ui.inlineNotice('info', 'Superadmin permission required', 'Dedicating a disk to swap requires the storage.destroy permission.') : ''}
              ${deviceCandidates.length ? ui.field({ label: 'Disk', name: 'swapDevicePath', type: 'select', value: defaultDevice, options: deviceOptions, help: 'Only whole disks with no partitions, mountpoint or system protection are eligible.' }) : `<div class="notice tone-warning"><span class="notice-icon">${ui.icon('alert-circle', 17)}</span><div><strong>No eligible disk</strong><p>${devices.length ? 'Every detected disk is mounted, partitioned, protected or already used for swap.' : 'No whole-disk device was found. A swap file on a mail volume is the safer option.'}</p></div></div>`}
              <div id="storage-swap-device-preview" class="storage-swap-preview"></div>
              <div class="notice tone-warning"><span class="notice-icon">${ui.icon('alert-circle', 17)}</span><div><strong>The entire disk will be erased</strong><p>All data and filesystem signatures on the selected disk are destroyed. The plan rechecks the disk serial or WWN before the first write.</p></div></div>
            </div>
          </div>
          <aside class="storage-swap-side">
            <span class="eyebrow">Plan summary</span>
            <div class="storage-swap-current"><span>Current swap</span><strong>${fmt.bytes(Number(swapInfo.totalBytes) || 0)}</strong><small>${fmt.bytes(Number(swapInfo.usedBytes) || 0)} used · ${fmt.bytes(Number(swapInfo.freeBytes) || 0)} free</small></div>
            <div id="storage-swap-summary" class="storage-swap-plan"></div>
            <div class="notice tone-info"><span class="notice-icon">${ui.icon('info', 17)}</span><div><strong>Planning performs no writes</strong><p>You will review the target and exact host commands before execution.</p></div></div>
          </aside>
        </div>
      </form>`,
      footer: `<button class="btn btn-ghost" type="button" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit" form="storage-swap-form" id="storage-swap-submit">${ui.icon('forward', 14)} Preview plan</button>`,
    });
    const form = modal.root.querySelector('#storage-swap-form');
    if (form) form.noValidate = true;
    const filePane = modal.root.querySelector('#storage-swap-file-pane');
    const devicePane = modal.root.querySelector('#storage-swap-device-pane');
    const volumeSelect = form.querySelector('[name="swapVolumeId"]');
    const sizeInput = form.querySelector('#storage-swap-size');
    const deviceSelect = form.querySelector('[name="swapDevicePath"]');
    const filePreview = modal.root.querySelector('#storage-swap-file-preview');
    const devicePreview = modal.root.querySelector('#storage-swap-device-preview');
    const summaryHost = modal.root.querySelector('#storage-swap-summary');
    const submit = modal.root.querySelector('#storage-swap-submit');
    const selectedCandidate = () => candidates.find((candidate) => candidate.volumeId === volumeSelect?.value && candidate.eligible);
    const selectedDevice = () => devices.find((device) => device.path === deviceSelect?.value && device.eligible);
    const sizeBytes = () => Math.round(Math.max(0, Number(sizeInput?.value) || 0) * 1024 ** 3);
    const updateSizeLimit = () => {
      const candidate = selectedCandidate();
      if (!candidate) return;
      const maxGiB = Math.max(0.0625, Math.floor((Number(candidate.maxSwapBytes) / 1024 ** 3) * 4) / 4);
      if (sizeInput) {
        sizeInput.max = String(maxGiB);
        if (Number(sizeInput.value) > maxGiB) sizeInput.value = String(maxGiB);
      }
      modal.root.querySelectorAll('[data-swap-size]').forEach((button) => {
        button.disabled = Number(button.dataset.swapSize) > maxGiB;
        button.classList.toggle('is-active', Number(button.dataset.swapSize) === Number(sizeInput?.value));
      });
    };
    const updateFilePreview = () => {
      const candidate = selectedCandidate();
      if (!filePreview) return;
      if (!candidate) {
        filePreview.innerHTML = `<div class="form-note">Choose an eligible mail volume to see the swap file placement.</div>`;
        updateSwapSummary();
        return;
      }
      updateSizeLimit();
      const bytes = sizeBytes();
      const freeAfter = Math.max(0, Number(candidate.freeBytes) - bytes);
      filePreview.innerHTML = `<div class="storage-swap-preview-head"><span>${ui.icon('file-plus', 15)} ${esc(candidate.name)}</span>${ui.badge('Managed boot', 'info', 'archive')}</div>
        <dl class="storage-swap-facts"><div><dt>File</dt><dd class="mono">${esc(candidate.mountPath)}/.a8m-swapfile</dd></div><div><dt>Free after</dt><dd>${fmt.bytes(freeAfter)}</dd></div><div><dt>Filesystem</dt><dd>${esc(candidate.fsType)}</dd></div></dl>`;
      updateSwapSummary();
    };
    const updateDevicePreview = () => {
      const device = selectedDevice();
      if (!devicePreview) return;
      if (!device) {
        devicePreview.innerHTML = `<div class="form-note">Choose an eligible blank disk to dedicate to swap.</div>`;
        updateSwapSummary();
        return;
      }
      devicePreview.innerHTML = `<div class="storage-swap-preview-head"><span>${ui.icon('disk', 15)} ${esc(device.path)}</span>${device.hasSignatures ? ui.badge('Existing data', 'warning', 'alert') : ui.badge('Blank disk', 'good', 'check-circle')}</div>
        <dl class="storage-swap-facts"><div><dt>Size</dt><dd>${fmt.bytes(device.sizeBytes)}</dd></div><div><dt>Model</dt><dd>${esc(device.model || '—')}</dd></div><div><dt>Serial</dt><dd class="mono">${esc(device.serial || device.wwn || '—')}</dd></div></dl>`;
      updateSwapSummary();
    };
    const updateSwapSummary = () => {
      if (!summaryHost) return;
      const fileMode = mode === 'file';
      const candidate = fileCandidates.find((item) => item.volumeId === volumeSelect?.value);
      const device = deviceCandidates.find((item) => item.path === deviceSelect?.value);
      const target = fileMode ? (candidate ? `${candidate.name} · ${candidate.mountPath}/.a8m-swapfile` : 'No eligible volume') : (device ? device.path : 'No eligible disk');
      const bytes = fileMode ? sizeBytes() : Number(device?.sizeBytes) || 0;
      const valid = fileMode ? Boolean(candidate && bytes >= 64 * 1024 ** 2 && bytes <= Number(candidate.maxSwapBytes)) : Boolean(canDestroy && device);
      summaryHost.innerHTML = `
        <div class="storage-swap-plan-row"><span>Type</span><strong>${fileMode ? 'Swap file' : 'Dedicated disk'}</strong></div>
        <div class="storage-swap-plan-row"><span>Target</span><strong>${esc(target)}</strong></div>
        <div class="storage-swap-plan-row"><span>Size</span><strong>${bytes ? fmt.bytes(bytes) : 'Choose a size'}</strong></div>
        <div class="storage-swap-plan-row"><span>Boot</span><strong>Managed fstab entry</strong></div>
        ${!fileMode ? `<div class="storage-swap-plan-warning">${ui.icon('alert-circle', 14)} The disk will be erased.</div>` : ''}`;
      if (submit) submit.disabled = !valid;
    };
    const setMode = (next) => {
      mode = next === 'device' ? 'device' : 'file';
      modal.root.querySelectorAll('[data-swap-mode]').forEach((button) => {
        const active = button.dataset.swapMode === mode;
        button.classList.toggle('is-active', active);
        button.setAttribute('aria-selected', active ? 'true' : 'false');
        button.tabIndex = active ? 0 : -1;
      });
      if (filePane) filePane.hidden = mode !== 'file';
      if (devicePane) devicePane.hidden = mode !== 'device';
      updateSwapSummary();
    };
    modal.root.querySelectorAll('[data-swap-mode]').forEach((button) => {
      button.addEventListener('click', () => setMode(button.dataset.swapMode));
      button.addEventListener('keydown', (event) => {
        if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return;
        event.preventDefault();
        const next = mode === 'file' ? 'device' : 'file';
        setMode(next);
        modal.root.querySelector(`[data-swap-mode="${next}"]`)?.focus();
      });
    });
    volumeSelect?.addEventListener('change', updateFilePreview);
    deviceSelect?.addEventListener('change', updateDevicePreview);
    sizeInput?.addEventListener('input', updateFilePreview);
    modal.root.querySelectorAll('[data-swap-size]').forEach((button) => button.addEventListener('click', () => {
      if (!sizeInput) return;
      sizeInput.value = button.dataset.swapSize;
      updateFilePreview();
    }));
    setMode(mode);
    if (fileCandidates.length) updateFilePreview();
    if (deviceCandidates.length) updateDevicePreview();
    updateSwapSummary();
    form.addEventListener('submit', async (event) => {
      event.preventDefault();
      const body = { mode };
      if (mode === 'file') {
        const candidate = selectedCandidate();
        const bytes = sizeBytes();
        if (!candidate) { ui.toast('Choose an eligible mail volume.', 'warning'); return; }
        if (bytes < 64 * 1024 ** 2) { ui.toast('Swap size must be at least 64 MiB.', 'warning'); return; }
        if (bytes > Number(candidate.maxSwapBytes)) { ui.toast(`Choose at most ${fmt.bytes(candidate.maxSwapBytes)} on this volume.`, 'warning'); return; }
        body.volumeId = candidate.volumeId;
        body.sizeBytes = bytes;
      } else {
        const device = selectedDevice();
        if (!device) { ui.toast('Choose an eligible blank disk.', 'warning'); return; }
        if (!canDestroy) { ui.toast('Creating swap on a disk requires the storage.destroy permission.', 'warning'); return; }
        body.devicePath = device.path;
        body.deviceSerial = device.serial || '';
        body.deviceWwn = device.wwn || '';
      }
      submit.disabled = true;
      submit.classList.add('is-loading');
      try {
        const result = await ctx.api('/control/api/storage/swap/plan', { method: 'POST', body });
        modal.close();
        showSwapReview(result);
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not plan swap creation' });
        submit.disabled = false;
        submit.classList.remove('is-loading');
      }
    });
  };

  const showSwapReview = (planResult) => {
    const plan = planResult.plan || {};
    const commands = Array.isArray(plan.commands) ? plan.commands : [];
    const warnings = Array.isArray(plan.warnings) ? plan.warnings : [];
    const expected = plan.confirmValue || 'CREATE SWAP';
    const modal = ctx.modal({
      title: 'Review swap plan',
      subtitle: 'Nothing has been written. Review the target and exact host commands.',
      size: 'medium',
      body: `${warnings.map((warning) => ui.inlineNotice('warning', 'Review before continuing', warning)).join('')}
        <div class="storage-review-grid"><div><span>Type</span><strong>${esc(plan.mode === 'device' ? 'Dedicated disk' : 'Swap file')}</strong></div><div><span>Target</span><strong class="mono">${esc(plan.target || '')}</strong></div><div><span>Size</span><strong>${fmt.bytes(Number(plan.sizeBytes) || 0)}</strong></div><div><span>Boot</span><strong>Managed fstab entry</strong></div></div>
        ${commands.length ? `<h4 style="margin-top:14px">Host operations</h4><pre class="storage-log">${commands.map((command) => esc(command)).join('\n')}</pre>` : ''}
        <div class="field" style="margin-top:13px"><label for="storage-swap-confirm">Type <strong class="mono">${esc(expected)}</strong> to confirm</label><input class="input mono" id="storage-swap-confirm" autocomplete="off" spellcheck="false"></div>`,
      footer: `<button class="btn btn-ghost" type="button" data-modal-close>Cancel</button><button class="btn ${plan.requireDestroy ? 'btn-danger' : 'btn-primary'}" type="button" id="storage-swap-execute" disabled>${ui.icon('play', 14)} Create swap</button>`,
    });
    const input = modal.root.querySelector('#storage-swap-confirm');
    const execute = modal.root.querySelector('#storage-swap-execute');
    input?.addEventListener('input', () => { execute.disabled = input.value.trim() !== expected; });
    execute?.addEventListener('click', async () => {
      execute.classList.add('is-loading');
      execute.disabled = true;
      try {
        await ctx.api('/control/api/storage/swap/execute', { method: 'POST', body: {
          operationId: planResult.operationId, token: planResult.token, confirm: true, confirmValue: input.value.trim(),
        } });
        modal.close();
        ui.toast('Swap creation queued. Follow the runner in Storage operations.', 'good');
        await Promise.all([loadSwap(), loadDisks(), loadOperations()]);
        openOperationConsole(planResult.operationId);
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not create swap' });
        execute.classList.remove('is-loading');
        execute.disabled = false;
      }
    });
  };

  const paintDisks = () => {
    const host = document.getElementById('storage-disks');
    if (!host) return;
    if (!disks) { host.innerHTML = ui.loadingState('Reading block devices…', 4); return; }
    if (!disks.supported) {
      host.innerHTML = ui.inlineNotice('info', 'Block-device inventory unavailable', disks.message || 'This host does not expose lsblk.');
      return;
    }
    const devices = Array.isArray(disks.devices) ? disks.devices : [];
    if (!devices.length) { host.innerHTML = ui.emptyState('No block devices', 'lsblk returned no physical devices.', '', 'disk'); return; }
    host.innerHTML = `<div class="table-wrap is-bare"><table class="data compact"><thead><tr><th>Disk</th><th>Size</th><th>Topology</th><th>SMART</th><th>Filesystem</th><th>State</th></tr></thead><tbody>${devices.map((device) => {
      const protectedState = device.protected;
      const tone = protectedState ? 'warning' : device.candidate ? 'good' : 'neutral';
      const label = protectedState ? 'Protected' : device.candidate ? 'Candidate' : device.mountpoint ? 'In use' : 'Not a candidate';
      const health = device.health;
      const healthLabel = !health ? 'Unknown' : health.state === 'ok' ? 'OK' : ui.humanTitle(health.state);
      const healthDetail = health ? [health.message, health.temperatureC !== undefined && health.temperatureC !== null ? `${health.temperatureC}°C` : ''].filter(Boolean).join(' · ') : 'No SMART data';
      return `<tr><td><span class="cell-main mono">${esc(device.path || device.name)}</span><span class="cell-sub">${esc([device.model, device.serial ? `serial ${device.serial}` : ''].filter(Boolean).join(' · ') || device.type)}</span></td><td class="num">${fmt.bytes(device.sizeBytes)}</td><td>${ui.badge(topologyLabel(device), device.topology === 'local' ? 'neutral' : 'info')}${device.topologyReason ? `<span class="cell-sub">${esc(device.topologyReason)}</span>` : ''}</td><td>${ui.badge(healthLabel, healthTone(health))}<span class="cell-sub">${esc(healthDetail)}</span></td><td><span class="mono">${esc(device.fsType || '—')}</span><span class="cell-sub">${esc(device.mountpoint || (device.hasSignatures ? 'Has signatures' : 'No filesystem'))}</span></td><td>${ui.badge(label, tone)}${device.protection ? `<span class="cell-sub">${esc(device.protection)}</span>` : ''}</td></tr>`;
    }).join('')}</tbody></table></div>
    <p class="form-note" style="margin-top:10px">RAID, thin LVM, multipath, LUKS and NFS are detected and clearly deferred. Existing mounted filesystems can be adopted; Alt8Mail does not create or reconfigure these topologies.</p>`;
  };

  const load = async (silent = false) => {
    if (loading) return;
    loading = true;
    if (!silent) {
      const host = document.getElementById('storage-volumes');
      if (host) host.innerHTML = ui.loadingState('Reading storage volumes…', 5);
    }
    try {
      const payload = await ctx.api('/control/api/storage/overview');
      if (!ctx.isCurrent()) return;
      overview = payload;
      paintNotice();
      paintAlerts();
      paintSummary();
      paintVolumes();
      paintDoctor();
      paintForecast();
      paintUsage();
    } catch (error) {
      if (!ctx.isCurrent()) return;
      const host = document.getElementById('storage-volumes');
      if (host) host.innerHTML = ui.inlineNotice('critical', 'Could not load storage', errorText(error));
      const summary = document.getElementById('storage-summary');
      if (summary) summary.innerHTML = '';
    } finally {
      loading = false;
    }
  };

  const loadDisks = async () => {
    try {
      const payload = await ctx.api('/control/api/storage/disks');
      if (!ctx.isCurrent()) return;
      disks = payload;
      paintDisks();
    } catch (error) {
      if (!ctx.isCurrent()) return;
      disks = { supported: false, message: errorText(error) };
      paintDisks();
    }
  };

  const loadUsage = async () => {
    try {
      usage = await ctx.api('/control/api/storage/usage');
      if (ctx.isCurrent()) paintUsage();
    } catch (error) {
      if (!ctx.isCurrent()) return;
      usage = { usage: [], domains: [], scanning: false, error: errorText(error) };
      paintUsage();
    }
  };

  document.getElementById('storage-refresh')?.addEventListener('click', () => { load(); loadDisks(); loadPlacements(); loadOperations(); loadHost(); loadUsage(); loadSwap(); });
  document.getElementById('storage-host-refresh')?.addEventListener('click', () => loadHost());
  document.getElementById('storage-add-disk')?.addEventListener('click', () => openAddDiskWizard());
  document.getElementById('storage-swap-refresh')?.addEventListener('click', () => loadSwap());
  document.getElementById('storage-swap-create')?.addEventListener('click', () => openSwapWizard());
  document.getElementById('storage-operations-refresh')?.addEventListener('click', () => loadOperations());
  document.getElementById('storage-move')?.addEventListener('click', () => openMoveWizard());
  document.getElementById('storage-policy-open')?.addEventListener('click', () => openPolicyModal());
  document.getElementById('storage-volume-filter')?.addEventListener('change', (event) => {
    volumeFilter = event.currentTarget.value || 'all';
    paintVolumes();
  });
  document.getElementById('storage-doctor-refresh')?.addEventListener('click', () => { load(); loadPlacements(); });
  document.getElementById('storage-scan-usage')?.addEventListener('click', async (event) => {
    const button = event.currentTarget;
    button.disabled = true; button.classList.add('is-loading');
    try {
      const result = await ctx.api('/control/api/storage/usage/scan', { method: 'POST' });
      ui.toast(result.message || 'Usage scan started.', 'good');
      setTimeout(loadUsage, 2000);
      setTimeout(loadUsage, 10000);
    } catch (error) {
      ui.toast(errorText(error), 'critical', { title: 'Could not start the usage scan' });
    } finally {
      button.disabled = false; button.classList.remove('is-loading');
    }
  });
  ctx.listen(document, 'visibilitychange', () => {
    if (document.visibilityState === 'visible' && ctx.isCurrent()) { load(true); loadUsage(); loadSwap(); }
  });
  ctx.onCleanup(() => { if (timer) clearInterval(timer); if (usageTimer) clearInterval(usageTimer); });
  await Promise.all([load(), loadDisks(), loadPlacements(), loadOperations(), loadHost(), loadUsage(), loadSwap()]);
  timer = setInterval(() => {
    if (document.visibilityState === 'visible' && ctx.isCurrent()) { load(true); loadOperations(); }
  }, 5000);
  usageTimer = setInterval(() => {
    if (document.visibilityState === 'visible' && ctx.isCurrent()) { loadUsage(); loadSwap(); }
  }, 30000);
}
