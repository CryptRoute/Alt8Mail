/* Web Net: webmail and administration hostnames, edge policy and traffic. */
import * as ui from './ui.js';

const esc = ui.esc;

function splitHostnames(value) {
  return String(value || '')
    .split(/[\s,]+/)
    .map((item) => item.trim().toLowerCase().replace(/\.$/, ''))
    .filter(Boolean);
}

function serviceRoutes(routes, service) {
  return (routes || []).filter((route) => route.service === service);
}

function serviceRouteModel(routes, service) {
  const own = serviceRoutes(routes, service);
  const primary = own.find((route) => route.primary) || own[0] || {};
  return {
    primary: primary.hostname || '',
    aliases: own.filter((route) => route.hostname && route.hostname !== primary.hostname).map((route) => route.hostname).join('\n'),
    certificate: primary.certificate || '',
    ipMode: primary.ipMode || 'shared',
    ipAddress: primary.ipAddress || '',
  };
}

function certificateLabel(cert) {
  const expiry = cert.daysLeft >= 0 ? `${ui.num(cert.daysLeft)} days left` : 'expired';
  return `${cert.name} · ${expiry}${cert.selfSigned ? ' · self-signed' : ''}`;
}

function checksTable(checks) {
  const list = Array.isArray(checks) ? checks : [];
  if (!list.length) return ui.emptyState('No host checks', 'Add at least one webmail and admin hostname, then run verification.', '', 'globe');
  return `<div class="table-wrap"><table class="data compact"><thead><tr><th>Service</th><th>Hostname</th><th>DNS</th><th>Certificate</th><th>Status</th></tr></thead><tbody>${list.map((check) => `<tr>
    <td>${esc(ui.humanTitle(check.service))}</td>
    <td><span class="cell-main mono">${esc(check.hostname)}</span>${check.primary ? '<span class="cell-sub">Primary</span>' : ''}</td>
    <td><span class="muted" style="font-size:10.5px">${esc(check.dnsDetail || '—')}</span></td>
    <td><span class="muted" style="font-size:10.5px">${esc(check.certDetail || '—')}</span></td>
    <td>${ui.statusPill(check.ok ? 'active' : 'failed', check.ok ? 'Ready' : 'Needs attention')}</td>
  </tr>`).join('')}</tbody></table></div>`;
}

function serviceCard(service, title, description, route, certificates, ips, canManage) {
  const certOptions = [{ value: '', label: 'Auto — choose a certificate that covers the hostname' }];
  for (const cert of certificates) certOptions.push({ value: cert.name, label: certificateLabel(cert) });
  if (route.certificate && !certificates.some((cert) => cert.name === route.certificate)) {
    certOptions.push({ value: route.certificate, label: `${route.certificate} · missing from inventory` });
  }
  const ipOptions = (ips || []).filter((ip) => ip.local).map((ip) => ({
    value: ip.address,
    label: `${ip.address} · ${ip.interface || 'network'}${ip.public ? ' · public' : ' · private'}`,
  }));
  const disabled = canManage ? '' : 'disabled';
  return `<section class="card">
    <header class="card-head"><div><h3>${esc(title)}</h3><p>${esc(description)}</p></div>${ui.badge(route.primary ? 'Configured' : 'Not set', route.primary ? 'good' : 'warning')}</header>
    <div class="card-body"><div class="form-grid">
      ${ui.field({ label: 'Primary hostname', name: `${service}Primary`, value: route.primary, placeholder: service === 'webmail' ? 'mail.example.com' : 'admin.example.com', required: true, disabled: !canManage, help: 'A public A/AAAA record must point at this server before Web Net can be applied.' })}
      ${ui.field({ label: 'Additional hostnames', name: `${service}Aliases`, type: 'textarea', value: route.aliases, rows: 3, disabled: !canManage, placeholder: service === 'webmail' ? 'webmail.example.com, mail.example.net' : 'admin.example.com, admin.example.net', help: service === 'webmail' ? 'One per line or comma-separated. Every name must have its own DNS record and a matching certificate. Passkeys use the primary hostname as their RP ID, so aliases outside its subtree cannot use the same passkeys.' : 'One per line or comma-separated. Every name must have its own DNS record and a matching certificate.' })}
      ${ui.field({ label: 'TLS certificate', name: `${service}Certificate`, type: 'select', value: route.certificate, options: certOptions, disabled: !canManage, help: 'Select a certificate already issued or imported under TLS certificates. Auto matches the hostname against its SAN list. The choice applies to every hostname for this service.' })}
      ${ui.field({ label: 'IP binding', name: `${service}IpMode`, type: 'select', value: route.ipMode, disabled: !canManage, options: [{ value: 'shared', label: 'Shared — every address not reserved by the other service' }, { value: 'dedicated', label: 'Dedicated — one assigned address only' }], extra: `data-webnet-ipmode="${service}"`, help: 'The binding applies to every hostname for this service.' })}
      ${ui.field({ label: 'Dedicated address', name: `${service}IpAddress`, type: 'select', value: route.ipAddress, disabled: !canManage || route.ipMode !== 'dedicated', options: [{ value: '', label: 'Choose an assigned address' }].concat(ipOptions), extra: `data-webnet-ipaddress="${service}"`, help: 'Only addresses assigned to this server can be bound. Add addresses under Server & network first.' })}
      ${ui.field({ label: 'Allow list', name: `${service}Allow`, type: 'textarea', value: route.allow || '', rows: 3, disabled: !canManage, placeholder: '203.0.113.0/24, 198.51.100.10', help: 'Optional CIDRs. When set, only these networks can reach this service. Deny entries always win.' })}
      ${ui.field({ label: 'Deny list', name: `${service}Deny`, type: 'textarea', value: route.deny || '', rows: 3, disabled: !canManage, placeholder: '192.0.2.0/24', help: 'Blocked CIDRs. Applied before the allow list and inherited by every hostname for this service.' })}
    </div></div>
  </section>`;
}

function metricCard(service) {
  const metrics = service || {};
  const recent = metrics.last15Minutes || {};
  return `<div class="metric-block">
    <div class="metric-block-head"><strong>${esc(metrics.label || 'Service')}</strong>${ui.badge(metrics.available ? 'Live' : 'No log yet', metrics.available ? 'good' : 'neutral')}</div>
    <div class="mini-stat-grid">
      <div><span>Requests · 60m</span><b>${ui.num(metrics.requests || 0)}</b></div>
      <div><span>5xx · 60m</span><b>${ui.num(metrics.serverErrors || 0)}</b></div>
      <div><span>Avg response</span><b>${ui.num(metrics.avgMs || 0)} ms</b></div>
      <div><span>p95 response</span><b>${ui.num(metrics.p95Ms || 0)} ms</b></div>
      <div><span>Unique clients</span><b>${ui.num(metrics.uniqueClients || 0)}</b></div>
      <div><span>Requests · 15m</span><b>${ui.num(recent.requests || 0)}</b></div>
    </div>
    ${Array.isArray(metrics.topPaths) && metrics.topPaths.length ? `<div class="mt-3">${ui.barList(metrics.topPaths.map((item) => ({ label: item.path, count: item.count })), { labelKey: 'label', valueKey: 'count', total: metrics.requests || null })}</div>` : '<p class="muted" style="font-size:10.5px;margin:10px 0 0">Request paths appear after the edge starts logging traffic.</p>'}
  </div>`;
}

export async function renderWebNet(ctx) {
  const canManage = ctx.can('server.manage');
  ctx.setTitle('Web Net', 'Infrastructure');
  let payload;
  try {
    payload = await ctx.api('/control/api/webnet');
  } catch (error) {
    ctx.root.innerHTML = `${ui.pageHeader({ eyebrow: 'Infrastructure', title: 'Web Net', description: 'Webmail and administration hostnames, edge policy and traffic.' })}${ui.inlineNotice('critical', 'Could not load Web Net', error.message || String(error))}`;
    return;
  }
  const settings = payload.settings || {};
  const routes = Array.isArray(payload.routes) ? payload.routes : [];
  const certificates = Array.isArray(payload.certificates) ? payload.certificates : [];
  const ips = Array.isArray(payload.ips) ? payload.ips : [];
  const webmail = serviceRouteModel(routes, 'webmail');
  const admin = serviceRouteModel(routes, 'admin');
  webmail.allow = (settings.webmailAllow || []).join('\n');
  webmail.deny = (settings.webmailDeny || []).join('\n');
  admin.allow = (settings.adminAllow || []).join('\n');
  admin.deny = (settings.adminDeny || []).join('\n');
  const lastError = String(settings.lastError || '').trim();
  const draftDirty = Boolean(settings.appliedAt && settings.updatedAt &&
    new Date(settings.updatedAt).getTime() > new Date(settings.appliedAt).getTime());
  const statusBadge = draftDirty
    ? ui.badge('Draft changes not applied', 'warning', 'alert')
    : settings.appliedRevision
      ? ui.badge(`Revision ${ui.num(settings.appliedRevision)} applied`, 'good', 'check-circle')
      : ui.badge('Not applied', 'warning');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Infrastructure', title: 'Web Net',
    description: 'Give webmail and administration separate hostnames from the mail/MX host, bind them to shared or dedicated addresses, choose issued certificates, and control maintenance and network access at the edge.',
    actions: `<button class="btn btn-secondary" type="button" id="webnet-refresh">${ui.icon('refresh', 15)} Refresh</button>${canManage ? `<button class="btn btn-secondary" type="button" id="webnet-verify">${ui.icon('shield', 15)} Verify DNS &amp; TLS</button><button class="btn btn-secondary" type="button" id="webnet-save">${ui.icon('check', 15)} Save draft</button><button class="btn btn-primary" type="button" id="webnet-apply">${ui.icon('send', 15)} Apply edge config</button>` : ''}`,
  })}
  ${lastError ? ui.inlineNotice('critical', 'Last apply failed', lastError) : ''}
  <div class="grid grid-main">
    <div class="stack">
      <section class="card">
        <header class="card-head"><div><h3>Edge status</h3><p>The control plane stays on its own address and port.</p></div>${statusBadge}</header>
        <div class="card-body">
          <div class="tile-link"><span>${ui.icon('globe', 15)} Webmail</span><span class="value mono">${esc(payload.webmailUrl || 'Not configured')}</span></div>
          <div class="tile-link"><span>${ui.icon('shield', 15)} Administration</span><span class="value mono">${esc(payload.adminUrl || 'Not configured')}</span></div>
          <div class="tile-link"><span>${ui.icon('mail', 15)} Mail/MX host</span><span class="value mono">${esc(payload.mailHostname || '—')}</span></div>
          <div class="tile-link"><span>${ui.icon('network', 15)} Browser policy on mail host</span><span class="value">${settings.mailHostMode === 'drop' ? 'Drop the connection' : 'Default page'}</span></div>
          <div class="tile-link"><span>${ui.icon('clock', 15)} Last applied</span><span class="value">${esc(settings.appliedAt ? ui.dateTime(settings.appliedAt) : 'Never')}</span></div>
          <div class="notice tone-info mt-3">${ui.icon('info', 15)}<div><strong>Applying rewrites the nginx site</strong><p>Existing <span class="mono">/admin/</span> access on the webmail host is removed. Browser push, WebAuthn and notification links move to the new webmail origin; existing push registrations are cleared when that origin changes, so users opt in again on the new address. Update external OAuth redirect URLs for storage connectors if you use them.</p></div></div>
        </div>
      </section>
      <form id="webnet-form">
        ${serviceCard('webmail', 'Webmail hostnames', 'The user-facing inbox, calendar, contacts, files and API.', webmail, certificates, ips, canManage)}
        ${serviceCard('admin', 'Administration hostnames', 'The customer administration portal on its own origin.', admin, certificates, ips, canManage)}
        <section class="card">
          <header class="card-head"><div><h3>Mail/MX hostname policy</h3><p>What a browser sees at the mail host that receives SMTP.</p></div><span class="badge">${ui.icon('lock', 14)} Edge only</span></header>
          <div class="card-body"><div class="form-grid">
            ${ui.field({ label: 'Browser access', name: 'mailHostMode', type: 'select', value: settings.mailHostMode || 'default_page', disabled: !canManage, options: [{ value: 'default_page', label: 'Show a default "not a web host" page' }, { value: 'drop', label: 'Drop the connection (HTTP 444 / timeout)' }], help: 'SMTP, IMAP, POP3, ManageSieve and the control plane are not affected. Only nginx ports 80 and 443 change.' })}
            ${ui.field({ label: 'Blocked page title', name: 'blockedTitle', value: settings.blockedTitle || 'This address is not a webmail host', disabled: !canManage })}
            <div class="span-2">${ui.field({ label: 'Blocked page message', name: 'blockedMessage', type: 'textarea', rows: 3, value: settings.blockedMessage || '', disabled: !canManage })}</div>
          </div>
          <p class="form-note">The default page clears the old webmail origin's cache and service worker through <span class="mono">Clear-Site-Data</span>. Drop mode closes the connection without a response, so a previously installed PWA may keep an offline shell until its site data is cleared.</p></div>
        </section>
        <section class="card">
          <header class="card-head"><div><h3>Maintenance</h3><p>Serve a 503 maintenance page without touching the mail listeners.</p></div><span class="badge">Per service</span></header>
          <div class="card-body"><div class="form-grid">
            ${ui.field({ label: 'Maintenance title', name: 'maintenanceTitle', value: settings.maintenanceTitle || 'Alt8Mail is temporarily unavailable', disabled: !canManage })}
            ${ui.field({ label: 'Maintenance message', name: 'maintenanceMessage', type: 'textarea', rows: 3, value: settings.maintenanceMessage || '', disabled: !canManage })}
            <div class="span-2 form-grid">
              ${ui.switchField({ label: 'Webmail maintenance', name: 'webmailMaintenance', checked: Boolean(settings.webmailMaintenance), disabled: !canManage, help: 'Webmail returns 503. Mail delivery and IMAP continue.' })}
              ${ui.switchField({ label: 'Administration maintenance', name: 'adminMaintenance', checked: Boolean(settings.adminMaintenance), disabled: !canManage, help: 'The admin portal returns 503.' })}
            </div>
          </div></div>
        </section>
      </form>
      <section class="card">
        <header class="card-head"><div><h3>Verification result</h3><p>DNS, certificate SAN coverage and dedicated-address checks.</p></div></header>
        <div class="card-body" id="webnet-checks">${ui.emptyState('Not verified yet', 'Save the hostnames, then run Verify DNS & TLS or Apply edge config.', '', 'shield')}</div>
      </section>
      <section class="card">
        <header class="card-head"><div><h3>Traffic insights</h3><p>Derived from the Web Net access logs over the last 60 minutes.</p></div><span class="badge">${ui.icon('activity', 14)} nginx</span></header>
        <div class="card-body" id="webnet-metrics">${ui.loadingState('Reading edge logs…', 3)}</div>
      </section>
    </div>
    <div class="stack">
      <section class="card"><header class="card-head"><div><h3>Applied addresses</h3><p>Live routes for this host.</p></div></header><div class="card-body">
        <div class="tile-link"><span>${ui.icon('globe', 15)} Webmail</span><span class="value mono">${esc(payload.webmailUrl || 'Not applied')}</span></div>
        <div class="tile-link"><span>${ui.icon('shield', 15)} Admin</span><span class="value mono">${esc(payload.adminUrl || 'Not applied')}</span></div>
        <div class="tile-link"><span>${ui.icon('mail', 15)} Mail host</span><span class="value mono">${esc(payload.mailHostname || '—')}</span></div>
        <div class="tile-link"><span>${ui.icon('server', 15)} Server</span><span class="value mono">${esc(payload.serverHostname || '—')}</span></div>
      </div></section>
      <section class="card"><header class="card-head"><div><h3>Assigned addresses</h3><p>Available for shared or dedicated binding.</p></div></header><div class="card-body">
        ${ips.filter((ip) => ip.local).map((ip) => `<div class="tile-link"><span class="mono">${esc(ip.address)}</span><span class="value">${esc(ip.interface || '—')} · ${ip.public ? 'public' : 'private'}</span></div>`).join('') || '<p class="muted">No non-loopback addresses were detected.</p>'}
      </div></section>
      <section class="card"><header class="card-head"><div><h3>Certificate inventory</h3><p>Issued or imported under TLS certificates.</p></div><button class="btn btn-secondary btn-sm" type="button" id="webnet-tls-link">${ui.icon('key', 13)} TLS page</button></header><div class="card-body">
        ${certificates.length ? `<div class="table-wrap"><table class="data compact"><thead><tr><th>Name</th><th>Expires</th><th>Key</th></tr></thead><tbody>${certificates.slice(0, 12).map((cert) => `<tr><td><span class="cell-main">${esc(cert.name)}</span><span class="cell-sub mono">${esc((cert.sans || []).slice(0, 3).join(', '))}${(cert.sans || []).length > 3 ? '…' : ''}</span></td><td>${ui.num(cert.daysLeft)} days</td><td>${ui.badge(cert.hasKey ? 'Present' : 'Missing', cert.hasKey ? 'good' : 'warning')}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No certificates', 'Issue or import a certificate under TLS certificates first.', '', 'key')}
      </div></section>
      <section class="card"><header class="card-head"><div><h3>Managed files</h3><p>Inspect or back up outside the panel.</p></div></header><div class="card-body">
        <div class="tile-link"><span>nginx site</span><span class="value mono">${esc(payload.paths?.site || '—')}</span></div>
        <div class="tile-link"><span>Enabled link</span><span class="value mono">${esc(payload.paths?.enabled || '—')}</span></div>
        <div class="tile-link"><span>Pages root</span><span class="value mono">${esc(payload.paths?.maintenanceRoot || '—')}</span></div>
        <div class="tile-link"><span>Access log format</span><span class="value mono">${esc(payload.paths?.logFormat || '—')}</span></div>
        <div class="tile-link"><span>Access log directory</span><span class="value mono">${esc(payload.paths?.logDir || '—')}</span></div>
        <div class="tile-link"><span>App environment</span><span class="value mono">${esc(payload.paths?.appEnv || '—')}</span></div>
      </div></section>
    </div>
  </div>`;

  const form = document.getElementById('webnet-form');
  const showChecks = (checks) => {
    const host = document.getElementById('webnet-checks');
    if (host) host.innerHTML = checksTable(checks);
  };
  const resultModal = (title, tone, message, checks) => {
    const modal = ctx.modal({
      title,
      size: 'large',
      body: `<div class="notice tone-${tone}">${ui.icon(tone === 'critical' ? 'alert' : 'check-circle', 16)}<div><strong>${esc(title)}</strong><p>${esc(message || '')}</p></div></div><div class="divider"></div>${checksTable(checks)}`,
      footer: `<button class="btn btn-secondary" type="button" data-modal-close>Close</button>`,
    });
    return modal;
  };
  const collect = () => {
    const values = ui.formValues(form);
    const routesPayload = [];
    for (const [service, primaryName, aliasName] of [
      ['webmail', 'webmailPrimary', 'webmailAliases'],
      ['admin', 'adminPrimary', 'adminAliases'],
    ]) {
      const primaryTokens = splitHostnames(values[primaryName]);
      const primary = primaryTokens[0] || '';
      const aliases = Array.from(new Set([
        ...primaryTokens.slice(1),
        ...splitHostnames(values[aliasName]),
      ])).filter((hostname) => hostname !== primary);
      if (!primary && !aliases.length) continue;
      const certificate = values[`${service}Certificate`] || '';
      const ipMode = values[`${service}IpMode`] || 'shared';
      const ipAddress = ipMode === 'dedicated' ? (values[`${service}IpAddress`] || '') : '';
      if (primary) routesPayload.push({ service, hostname: primary, certificate, ipMode, ipAddress, primary: true, enabled: true });
      for (const hostname of aliases) routesPayload.push({ service, hostname, certificate, ipMode, ipAddress, primary: false, enabled: true });
    }
    return {
      mailHostMode: values.mailHostMode || 'default_page',
      webmailMaintenance: Boolean(values.webmailMaintenance),
      adminMaintenance: Boolean(values.adminMaintenance),
      maintenanceTitle: values.maintenanceTitle || '',
      maintenanceMessage: values.maintenanceMessage || '',
      blockedTitle: values.blockedTitle || '',
      blockedMessage: values.blockedMessage || '',
      webmailAllow: splitHostnames(values.webmailAllow),
      webmailDeny: splitHostnames(values.webmailDeny),
      adminAllow: splitHostnames(values.adminAllow),
      adminDeny: splitHostnames(values.adminDeny),
      routes: routesPayload,
    };
  };
  const save = async ({ quiet = false } = {}) => {
    const response = await ctx.api('/control/api/webnet', { method: 'PUT', body: collect() });
    if (!quiet) {
      ui.toast('Web Net draft saved. Apply when DNS and certificates are ready.', 'good');
      await renderWebNet(ctx);
    }
    return response;
  };
  form?.addEventListener('change', (event) => {
    const select = event.target.closest('[data-webnet-ipmode]');
    if (!select) return;
    const service = select.getAttribute('data-webnet-ipmode');
    const address = form.querySelector(`[data-webnet-ipaddress="${service}"]`);
    if (address) address.disabled = select.value !== 'dedicated' || !canManage;
  });
  form?.addEventListener('submit', (event) => {
    event.preventDefault();
    if (canManage) save().catch((error) => ui.toast(error.message || 'Could not save Web Net.', 'critical', { title: 'Save failed' }));
  });
  document.getElementById('webnet-refresh')?.addEventListener('click', () => renderWebNet(ctx));
  document.getElementById('webnet-tls-link')?.addEventListener('click', () => { window.location.hash = '#/tls'; });
  document.getElementById('webnet-save')?.addEventListener('click', async (event) => {
    if (!canManage) return;
    const button = event.currentTarget;
    button.classList.add('is-loading'); button.disabled = true;
    try { await save(); }
    catch (error) { ui.toast(error.message || 'Could not save Web Net.', 'critical', { title: 'Save failed' }); }
    finally { button.classList.remove('is-loading'); button.disabled = false; }
  });
  document.getElementById('webnet-verify')?.addEventListener('click', async (event) => {
    if (!canManage) return;
    const button = event.currentTarget;
    button.classList.add('is-loading'); button.disabled = true;
    try {
      await save({ quiet: true });
      const result = await ctx.api('/control/api/webnet/verify', { method: 'POST' });
      showChecks(result.checks);
      resultModal(result.ok ? 'DNS and certificates are ready' : 'Verification needs attention', result.ok ? 'good' : 'warning', result.ok ? 'Every hostname resolves to this server and has a matching certificate.' : 'Fix the rows marked Needs attention before applying.', result.checks);
    } catch (error) {
      ui.toast(error.message || 'Could not verify Web Net.', 'critical', { title: 'Verification failed' });
    } finally { button.classList.remove('is-loading'); button.disabled = false; }
  });
  document.getElementById('webnet-apply')?.addEventListener('click', async (event) => {
    if (!canManage) return;
    const button = event.currentTarget;
    button.classList.add('is-loading'); button.disabled = true;
    try {
      await save({ quiet: true });
      const confirmed = await ctx.confirm({
        title: 'Apply Web Net configuration',
        message: 'nginx will be rewritten and validated, then the webmail and admin Go services restart with the new public URLs. Existing browser sessions on the old hostnames end. Continue?',
        confirmLabel: 'Apply configuration',
        tone: 'danger',
      });
      if (!confirmed) return;
      const result = await ctx.api('/control/api/webnet/apply', { method: 'POST' });
      showChecks(result.checks);
      resultModal('Web Net applied', 'good', `Revision ${result.revision} is live. Webmail: ${result.webmailUrl} · Admin: ${result.adminUrl}`, result.checks);
      setTimeout(() => { if (location.hash === '#/webnet') renderWebNet(ctx); }, 900);
    } catch (error) {
      const checks = error.details && error.details.checks ? error.details.checks : null;
      if (checks) showChecks(checks);
      if (checks) resultModal('Apply blocked', 'critical', error.message || 'Verification failed.', checks);
      else ui.toast(error.message || 'Could not apply Web Net.', 'critical', { title: 'Apply failed' });
    } finally { button.classList.remove('is-loading'); button.disabled = false; }
  });

  try {
    const metrics = await ctx.api('/control/api/webnet/metrics');
    const host = document.getElementById('webnet-metrics');
    const services = Array.isArray(metrics.services) ? metrics.services : [];
    if (host) host.innerHTML = services.length ? `<div class="webnet-metrics-grid">${services.map((service) => metricCard(service)).join('')}</div>` : ui.emptyState('No traffic yet', 'Edge counters appear after nginx serves requests.', '', 'activity');
  } catch (error) {
    const host = document.getElementById('webnet-metrics');
    if (host) host.innerHTML = ui.inlineNotice('warning', 'Could not read traffic logs', error.message || String(error));
  }
}
