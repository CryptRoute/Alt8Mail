/* TLS, access control, API keys, webhooks and notification views. */
import * as ui from './ui.js';

const esc = ui.esc;
const fmt = {
  dateTime: ui.dateTime, dateOnly: ui.dateOnly, timeAgo: ui.timeAgo,
  bytes: ui.bytes, num: ui.num, pct: ui.pct,
};

function errorText(error) { return error?.message || String(error || 'Unknown error'); }

function id(prefix = 'id') {
  return `${prefix}-${(crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random()}`).toString().replace(/[^a-zA-Z0-9-]/g, '').slice(0, 18)}`;
}

function copyButton(value, label = 'Copy') {
  return `<button class="icon-btn" type="button" data-copy="${ui.attr(value)}" title="${ui.attr(label)}" aria-label="${ui.attr(label)}">${ui.icon('copy', 14)}</button>`;
}

function secretModal(ctx, title, label, value, note = '') {
  ctx.modal({
    title, subtitle: note, size: 'small',
    body: `<div class="notice tone-warning">${ui.icon('key', 16)}<div><strong>Copy this value now</strong><p>It is shown once and is not stored in plaintext.</p></div></div><div class="field" style="margin-top:14px"><label>${esc(label)}</label><div class="input-group"><input class="input mono" readonly value="${ui.attr(value)}" id="secret-value"><button class="btn btn-secondary" type="button" data-copy="${ui.attr(value)}">${ui.icon('copy', 14)} Copy</button></div></div>`,
    footer: `<button class="btn btn-primary" type="button" data-modal-close>I have saved it</button>`,
  });
  const input = document.getElementById('secret-value');
  if (input) { input.focus(); input.select(); }
}

function daysTone(days) {
  if (days < 0) return 'critical';
  if (days < 14) return 'critical';
  if (days < 30) return 'warning';
  return 'good';
}

/* =============================================================== TLS */

export async function renderTLS(ctx) {
  const canManage = ctx.can('tls.manage');
  let certificates = [];
  let settings = {};
  let wrapper = {};
  ctx.setTitle('TLS certificates', 'Security');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Security', title: 'TLS certificates',
    description: 'Let’s Encrypt issuance and renewal, imported CA certificates, service assignment, self-signed control-plane material and expiry notifications.',
    actions: `${canManage ? `<button class="btn btn-primary" type="button" id="tls-import">${ui.icon('upload', 14)} Import certificate</button><button class="btn btn-secondary" type="button" id="tls-assign">${ui.icon('link', 14)} Assign to service</button><button class="btn btn-secondary" type="button" id="tls-issue">${ui.icon('lock', 14)} Issue certificate</button><button class="btn btn-secondary" type="button" id="tls-self-signed">${ui.icon('key', 14)} Self-signed</button>` : ''}<button class="btn btn-secondary" type="button" id="tls-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    <div id="tls-summary">${ui.loadingState('Scanning certificates…', 3)}</div>
    <div class="grid grid-main mt-4">
      <div class="stack">
        <section class="card"><header class="card-head"><div><h3>Certificate inventory</h3><p>Fullchain material, SANs and expiry</p></div></header><div class="card-body" id="tls-list">${ui.loadingState('Scanning certificates…', 4)}</div></section>
        <section class="card"><header class="card-head"><div><h3>Renewal automation</h3><p>Twice-daily certbot wrapper and deploy hook</p></div></header><div class="card-body" id="tls-wrapper">${ui.loadingState('Checking renewal wrapper…', 2)}</div></section>
      </div>
      <div class="stack">
        <section class="card"><header class="card-head"><div><h3>Expiry notifications</h3><p>Email warnings before certificates expire</p></div></header><div class="card-body" id="tls-settings">${ui.loadingState('Loading settings…', 2)}</div></section>
        <section class="card"><header class="card-head"><div><h3>Certificate paths</h3><p>What the stack loads</p></div></header><div class="card-body">
          <div class="tile-link"><span>${ui.icon('lock', 15)} Postfix</span><span class="value mono">smtpd_tls_cert_file</span></div>
          <div class="tile-link"><span>${ui.icon('mail', 15)} Dovecot</span><span class="value mono">ssl_cert</span></div>
          <div class="tile-link"><span>${ui.icon('server', 15)} Control panel</span><span class="value mono">/etc/ssl/alt8mail</span></div>
          <div class="tile-link"><span>${ui.icon('globe', 15)} Web</span><span class="value mono">nginx</span></div>
          <div class="note-strip" style="margin-top:11px">${ui.icon('info', 13)} <span>Renewal reloads nginx, Postfix and Dovecot through the deploy hook. Failed renewals raise an alert if notification handles are configured.</span></div>
        </div></section>
      </div>
    </div>`;

  const load = async () => {
    const summaryHost = document.getElementById('tls-summary');
    const listHost = document.getElementById('tls-list');
    try {
      const payload = await ctx.api('/control/api/tls/certificates');
      if (!ctx.isCurrent()) return;
      certificates = Array.isArray(payload.certificates) ? payload.certificates : [];
      const expiring = certificates.filter((cert) => cert.daysLeft < 30).length;
      const expired = certificates.filter((cert) => cert.daysLeft < 0).length;
      const selfSigned = certificates.filter((cert) => cert.selfSigned).length;
      summaryHost.innerHTML = `<div class="stat-grid">
        ${ui.statTile({ label: 'Certificates', value: ui.num(certificates.length), detail: 'Discovered on this host', iconName: 'lock', tone: 'info' })}
        ${ui.statTile({ label: 'Expiring · 30d', value: ui.num(expiring), detail: expired ? `${expired} already expired` : 'Renew before expiry', iconName: 'clock', tone: expired ? 'critical' : expiring ? 'warning' : 'good' })}
        ${ui.statTile({ label: 'Self-signed', value: ui.num(selfSigned), detail: 'Not publicly trusted', iconName: 'key', tone: selfSigned ? 'warning' : 'good' })}
        ${ui.statTile({ label: 'Checked', value: fmt.timeAgo(payload.checkedAt), detail: 'Live scan of certificate files', iconName: 'refresh' })}
      </div>`;
      listHost.innerHTML = certificates.length ? `<div class="table-wrap"><table class="data"><thead><tr><th>Certificate</th><th>Issuer / SANs</th><th>Expiry</th><th>Paths</th><th></th></tr></thead><tbody>
        ${certificates.map((cert) => `<tr><td><span class="cell-main">${esc(cert.name)}</span><span class="cell-sub">${cert.selfSigned ? 'Self-signed' : 'Managed'}${cert.hasKey ? ' · key present' : ' · key missing'}</span>${payload.errors?.[cert.name] ? `<span class="cell-sub text-critical">${esc(payload.errors[cert.name])}</span>` : ''}</td>
          <td><span>${esc(cert.issuer)}</span><span class="cell-sub">${esc(cert.sans?.slice(0, 4).join(', ') || 'No SANs')}${cert.sans?.length > 4 ? ` +${cert.sans.length - 4}` : ''}</span></td>
          <td><div class="inline-kv"><span>${esc(fmt.dateOnly(cert.notAfter))}</span>${ui.badge(`${cert.daysLeft} days`, daysTone(cert.daysLeft), cert.daysLeft < 30 ? 'alert' : 'check-circle')}</div><div style="min-width:100px;margin-top:4px">${ui.progress(Math.max(0, Math.min(100, (cert.daysLeft / 90) * 100)), cert.daysLeft < 14 ? 'critical' : cert.daysLeft < 30 ? 'warning' : 'good')}</div></td>
          <td><span class="cell-sub mono">${esc(cert.certPath)}</span><span class="cell-sub mono">${esc(cert.keyPath || '—')}</span></td>
          <td class="actions"><button class="icon-btn" type="button" data-tls-detail="${esc(cert.name)}" title="Certificate details" aria-label="Certificate details">${ui.icon('eye', 15)}</button>${canManage && !cert.selfSigned ? `<button class="icon-btn" type="button" data-tls-renew="${esc(cert.name)}" title="Renew certificate" aria-label="Renew certificate">${ui.icon('refresh', 15)}</button>` : ''}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No certificates found', 'No certbot live directories or configured TLS files were readable.', canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="tls-issue-empty">${ui.icon('lock', 13)} Issue certificate</button>` : '', 'lock');
      document.getElementById('tls-issue-empty')?.addEventListener('click', openIssue);
      listHost.querySelectorAll('[data-tls-detail]').forEach((button) => button.addEventListener('click', () => showCertificate(button.dataset.tlsDetail)));
      listHost.querySelectorAll('[data-tls-renew]').forEach((button) => button.addEventListener('click', () => renewCertificate(button.dataset.tlsRenew, false)));
    } catch (error) {
      summaryHost.innerHTML = ui.inlineNotice('critical', 'Could not scan certificates', errorText(error));
      listHost.innerHTML = '';
    }
    try {
      settings = await ctx.api('/control/api/tls/settings');
      wrapper = await ctx.api('/control/api/tls/renewal-wrapper');
      if (!ctx.isCurrent()) return;
      document.getElementById('tls-settings').innerHTML = `<form id="tls-settings-form">
        <div class="form-grid is-single">
          ${ui.field({ label: 'Notification email', name: 'adminEmail', type: 'email', value: settings.adminEmail || '', placeholder: 'hostmaster@example.com', help: 'Certificate warnings and renewal test mail are sent here.' })}
          ${ui.field({ label: 'Warn before expiry (days)', name: 'warnDays', type: 'number', value: settings.warnDays || 14, min: 1, max: 90 })}
        </div>
        <div class="form-actions"><span class="form-note">Warnings use the control plane notification dispatcher.</span><div class="form-actions-right"><button class="btn btn-secondary" type="button" id="tls-test-mail" ${canManage ? '' : 'disabled'}>${ui.icon('send', 14)} Send test</button><button class="btn btn-primary" type="submit" ${canManage ? '' : 'disabled'}>Save settings</button></div></div>
      </form>`;
      document.getElementById('tls-settings-form')?.addEventListener('submit', async (event) => {
        event.preventDefault();
        if (!canManage) return;
        const values = ui.formValues(event.currentTarget);
        try {
          await ctx.api('/control/api/tls/settings', { method: 'PUT', body: { adminEmail: values.adminEmail, warnDays: Number(values.warnDays) } });
          ui.toast('TLS notification settings saved.', 'good');
          await load();
        } catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not save TLS settings' }); }
      });
      document.getElementById('tls-test-mail')?.addEventListener('click', async (event) => {
        const button = event.currentTarget;
        button.classList.add('is-loading'); button.disabled = true;
        try {
          const result = await ctx.api('/control/api/tls/test-email', { method: 'POST' });
          ctx.modal({ title: 'SSL test email sent', size: 'large', body: `<div class="notice tone-good">${ui.icon('check-circle', 16)}<div><strong>Local SMTP accepted the message</strong><p>The transcript below shows the submission path.</p></div></div><div class="divider"></div>${ui.codeBlock(result.transcript || '', { language: 'SMTP transcript', maxHeight: '46vh' })}` });
        } catch (error) { ui.toast(errorText(error), 'critical', { title: 'Test email failed' }); }
        finally { button.classList.remove('is-loading'); button.disabled = false; }
      });
      document.getElementById('tls-wrapper').innerHTML = `<div class="diagnostic-summary">${ui.statusPill(wrapper.installed ? 'active' : 'warning', wrapper.installed ? 'Installed' : 'Not installed')}<span class="summary-pill">${ui.icon('file-code', 12)} ${esc(wrapper.scriptPath)}</span><span class="summary-pill">${ui.icon('calendar', 12)} ${esc(wrapper.cronPath)}</span></div>
        <div class="table-wrap"><table class="data compact"><thead><tr><th>Component</th><th>State</th></tr></thead><tbody><tr><td>Wrapper script</td><td>${wrapper.scriptInstalled ? ui.badge('Present', 'good', 'check-circle') : ui.badge('Missing', 'warning', 'alert')}</td></tr><tr><td>Twice-daily cron</td><td>${wrapper.cronInstalled ? ui.badge('Present', 'good', 'check-circle') : ui.badge('Missing', 'warning', 'alert')}</td></tr><tr><td>Deploy hook</td><td>${ui.badge('nginx + postfix + dovecot reload', 'info')}</td></tr></tbody></table></div>
        <div class="form-actions"><span class="form-note">Renewals run at 03:17 and 15:17 UTC. Failures are written to <span class="mono">/var/log/alt8mail-ssl-renewal.log</span>.</span><div class="form-actions-right">${canManage ? `<button class="btn btn-secondary" type="button" id="wrapper-install">${ui.icon('download', 13)} ${wrapper.installed ? 'Reinstall wrapper' : 'Install wrapper'}</button>` : ''}</div></div>`;
      document.getElementById('wrapper-install')?.addEventListener('click', async () => {
        try {
          const result = await ctx.api('/control/api/tls/renewal-wrapper', { method: 'POST' });
          ui.toast('Renewal wrapper and cron installed.', 'good');
          ui.toast(`${result.script} · ${result.cron}`, 'info');
          await load();
        } catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not install wrapper' }); }
      });
    } catch (error) {
      document.getElementById('tls-settings').innerHTML = ui.inlineNotice('critical', 'Could not load TLS settings', errorText(error));
      document.getElementById('tls-wrapper').innerHTML = '';
    }
  };

  const showCertificate = async (name) => {
    const modal = ctx.modal({ title: name, size: 'large', body: ui.loadingState('Reading certificate…', 3), footer: `<button class="btn btn-secondary" type="button" data-modal-close>Close</button>${canManage ? `<button class="btn btn-secondary" type="button" id="cert-dry-run">${ui.icon('activity', 14)} Dry-run renew</button><button class="btn btn-primary" type="button" id="cert-renew">${ui.icon('refresh', 14)} Renew now</button>` : ''}` });
    try {
      const cert = await ctx.api(`/control/api/tls/certificates/${encodeURIComponent(name)}`);
      if (!modal.root.isConnected) return;
      modal.root.querySelector('.modal-body').innerHTML = `<div class="diagnostic-summary">${ui.statusPill(cert.daysLeft < 0 ? 'expired' : cert.daysLeft < 30 ? 'warning' : 'active', cert.daysLeft < 0 ? 'Expired' : cert.daysLeft < 30 ? 'Expiring soon' : 'Valid')}<span class="summary-pill">${ui.icon('calendar', 12)} expires ${esc(fmt.dateOnly(cert.notAfter))}</span><span class="summary-pill">${ui.icon('clock', 12)} ${cert.daysLeft} days</span>${cert.selfSigned ? ui.badge('Self-signed', 'warning', 'key') : ui.badge('CA issued', 'good', 'shield-check')}</div>
        <div class="kv-grid">${ui.kv([
          ['Subject', `<span class="mono break-all">${esc(cert.subject)}</span>`],
          ['Issuer', `<span class="mono break-all">${esc(cert.issuer)}</span>`],
          ['Not before', esc(fmt.dateTime(cert.notBefore))],
          ['Not after', esc(fmt.dateTime(cert.notAfter))],
          ['Certificate path', `<span class="mono break-all">${esc(cert.certPath)}</span>`],
          ['Key path', `<span class="mono break-all">${esc(cert.keyPath || '—')}</span>`],
        ])}</div>
        <h4 class="form-section-title" style="margin-top:16px">Subject alternative names</h4><div class="chip-list">${(cert.sans || []).map((san) => `<span class="mono-chip">${esc(san)}</span>`).join('') || '<span class="muted">No SANs</span>'}</div>
        ${!cert.selfSigned ? `<div class="note-strip" style="margin-top:14px">${ui.icon('info', 13)} <span>Dry-run asks Let’s Encrypt to validate the challenge without replacing the live certificate.</span></div>` : ''}`;
      if (!cert.selfSigned && canManage) {
        modal.root.querySelector('#cert-renew')?.addEventListener('click', () => { modal.close(); renewCertificate(cert.name, false); });
        modal.root.querySelector('#cert-dry-run')?.addEventListener('click', () => { modal.close(); renewCertificate(cert.name, true); });
      }
    } catch (error) { modal.root.querySelector('.modal-body').innerHTML = ui.inlineNotice('critical', 'Could not read certificate', errorText(error)); }
  };

  const renewCertificate = async (name, dryRun) => {
    if (!canManage) return;
    const ok = await ctx.confirm({
      title: dryRun ? 'Dry-run certificate renewal' : 'Renew certificate',
      message: dryRun ? `Ask Let’s Encrypt to validate renewal for ${name} without replacing the live certificate?` : `Renew ${name} now and reload nginx, Postfix and Dovecot if successful?`,
      confirmLabel: dryRun ? 'Run dry-run' : 'Renew now', tone: 'primary',
    });
    if (!ok) return;
    const working = ctx.modal({ title: dryRun ? 'Renewal dry-run' : 'Certificate renewal', size: 'large', body: `<div class="notice tone-info">${ui.icon('refresh', 16)}<div><strong>certbot is running</strong><p>This can take up to three minutes while the ACME challenge is validated.</p></div></div><div class="loading-state"><div class="skeleton" style="width:78%"></div><div class="skeleton" style="width:62%"></div></div>` });
    try {
      const result = await ctx.api('/control/api/tls/renew', { method: 'POST', body: { name, dryRun } });
      working.close();
      ctx.modal({ title: dryRun ? 'Dry-run completed' : 'Certificate renewed', size: 'large', body: `<div class="notice tone-good">${ui.icon('check-circle', 16)}<div><strong>certbot completed successfully</strong><p>${dryRun ? 'The live certificate was not replaced.' : 'The deploy hook reloaded nginx, Postfix and Dovecot.'}</p></div></div><div class="divider"></div>${ui.codeBlock(result.output || '', { language: 'certbot output', maxHeight: '50vh' })}` });
      await load();
    } catch (error) {
      working.close();
      ctx.modal({ title: 'Renewal failed', size: 'large', body: `<div class="notice tone-critical">${ui.icon('alert-circle', 16)}<div><strong>${esc(errorText(error))}</strong></div></div><div class="mt-3">${ui.codeBlock(error.details || '', { language: 'certbot output', maxHeight: '50vh' })}</div>` });
    }
  };

  const openImport = () => {
    const modal = ctx.modal({
      title: 'Import a certificate',
      subtitle: 'Paste a CA-issued certificate chain and its matching private key. The panel validates the pair before saving it.',
      size: 'large',
      body: `<form id="tls-import-form">
        <div class="form-grid">
          ${ui.field({ label: 'Certificate name', name: 'name', placeholder: 'mail-example-2026', required: true, help: 'Letters, digits, dots, underscores and hyphens. The stored name is prefixed with import-.' })}
          <div>${ui.switchField({ label: 'Replace an existing import', name: 'overwrite', help: 'Overwrite a previously imported certificate with the same name. The old files are backed up.' })}</div>
          <div class="span-2">${ui.field({ label: 'Certificate chain (PEM)', name: 'certificate', type: 'textarea', rows: 8, required: true, placeholder: '-----BEGIN CERTIFICATE-----…', help: 'Include the server certificate first, followed by any intermediate certificates supplied by your CA.' })}</div>
          <div class="span-2">${ui.field({ label: 'Private key (PEM)', name: 'privateKey', type: 'textarea', rows: 8, required: true, placeholder: '-----BEGIN PRIVATE KEY-----…', help: 'RSA, ECDSA or PKCS#8 PEM. The key is stored root-only under /etc/ssl/alt8mail/imported.' })}</div>
          <div class="span-2">${ui.field({ label: 'Key passphrase (only for encrypted PEM)', name: 'passphrase', type: 'password', autocomplete: 'new-password', help: 'Used once to decrypt the key; the saved key is normalized to unencrypted PKCS#8 PEM with root-only permissions.' })}</div>
        </div>
        <div class="notice tone-info mt-3">${ui.icon('shield', 16)}<div><strong>Nothing is assigned automatically</strong><p>After import, use <em>Assign to service</em> to update nginx, Postfix or Dovecot paths and run the validation reload.</p></div></div>
        <div class="form-actions"><span class="form-note">The key is never returned by the API or written to the audit log.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">Validate and import</button></div></div>
      </form>`,
    });
    const form = modal.root.querySelector('#tls-import-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        const result = await ctx.api('/control/api/tls/import', { method: 'POST', body: values });
        modal.close();
        ui.toast('Certificate imported.', 'good');
        ctx.modal({
          title: 'Certificate imported', size: 'medium',
          body: `<div class="notice tone-good">${ui.icon('check-circle', 16)}<div><strong>${esc(result.certificate?.name || values.name)}</strong><p>Valid until ${esc(fmt.dateOnly(result.certificate?.notAfter))}. It is now available for service assignment.</p></div></div><div class="kv-grid" style="margin-top:12px">${ui.kv([['Certificate', `<span class="mono break-all">${esc(result.certPath)}</span>`], ['Private key', `<span class="mono break-all">${esc(result.keyPath)}</span>`], ['Issuer', `<span class="mono break-all">${esc(result.certificate?.issuer || '—')}</span>`]])}</div>`,
          footer: `<button class="btn btn-primary" type="button" data-modal-close>Done</button>`,
        });
        await load();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Import failed' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  const openAssign = async () => {
    const usable = certificates.filter((cert) => cert.hasKey);
    if (!usable.length) {
      ui.toast('Import or issue a certificate with a private key before assigning it to a service.', 'warning');
      return;
    }
    let mapping;
    try {
      mapping = await ctx.api('/control/api/tls/assignments');
    } catch (error) {
      ui.toast(errorText(error), 'critical', { title: 'Could not inspect service TLS paths' });
      return;
    }
    const targets = Array.isArray(mapping.targets) ? mapping.targets : [];
    const targetMarkup = targets.map((target) => `<label class="assign-target${target.available ? '' : ' is-disabled'}">
      <input type="checkbox" name="assign-target" value="${esc(target.key)}"${target.available ? ' checked' : ' disabled'}>
      <span class="assign-target-copy"><strong>${esc(target.label)}</strong><span class="mono">${esc(target.certPath || target.detail || 'Not configured')}</span><span class="mono muted">${esc(target.keyPath || '')}</span></span>
      ${target.available ? ui.badge('Ready', 'good', 'check-circle') : ui.badge('Unavailable', 'warning', 'alert')}
    </label>`).join('');
    const modal = ctx.modal({
      title: 'Assign a certificate to services',
      subtitle: 'The selected certificate files become the configured TLS paths. Each service is validated and reloaded; failures roll back.',
      size: 'large',
      body: `<form id="tls-assign-form">
        <div class="form-grid is-single">
          ${ui.field({ label: 'Certificate', name: 'certificate', type: 'select', required: true, options: usable.map((cert) => ({ value: cert.name, label: `${cert.name} — ${cert.issuer}` })) })}
        </div>
        <div class="form-section-title" style="margin-top:14px">Targets</div>
        <div class="assign-targets">${targetMarkup || ui.inlineNotice('warning', 'No service paths were detected', 'Check nginx, Postfix and Dovecot configuration before assigning a certificate.')}</div>
        <div class="notice tone-warning mt-3">${ui.icon('alert', 16)}<div><strong>This reloads live services</strong><p>nginx, Postfix and Dovecot are validated with their native checks. If any target fails, previously changed targets are restored from their original paths and the request reports the failure.</p></div></div>
        <div class="form-actions"><span class="form-note">The certificate must include a private key and a valid expiry.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">Assign and reload</button></div></div>
      </form>`,
    });
    const form = modal.root.querySelector('#tls-assign-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const selectedTargets = Array.from(form.querySelectorAll('input[name="assign-target"]:checked')).map((input) => input.value);
      if (!selectedTargets.length) { ui.toast('Select at least one service.', 'warning'); return; }
      const confirmed = await ctx.confirm({
        title: 'Assign certificate',
        message: `Assign ${values.certificate} to ${selectedTargets.join(', ')}? The affected services will be validated and reloaded.`,
        confirmLabel: 'Assign and reload', tone: 'primary',
      });
      if (!confirmed) return;
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        const result = await ctx.api('/control/api/tls/assign', { method: 'POST', body: { certificate: values.certificate, targets: selectedTargets } });
        modal.close();
        ctx.modal({
          title: 'Certificate assigned', size: 'medium',
          body: `<div class="notice tone-good">${ui.icon('check-circle', 16)}<div><strong>${esc(result.certificate?.name || values.certificate)}</strong><p>Updated and reloaded: ${esc((result.targets || []).join(', '))}.</p></div></div><p class="form-note" style="margin-top:12px">The new paths are now visible in the service TLS mapping. Review the services page if you want to confirm the units are active.</p>`,
          footer: `<button class="btn btn-primary" type="button" data-modal-close>Done</button>`,
        });
        await load();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Assignment failed' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  const openIssue = () => {
    const modal = ctx.modal({
      title: 'Issue a Let’s Encrypt certificate', subtitle: 'certbot uses the nginx webroot challenge. DNS must already point at this host.',
      size: 'medium',
      body: `<form id="tls-issue-form"><div class="form-grid">
        ${ui.field({ label: 'Domains', name: 'domains', placeholder: 'mail.example.com, example.com', required: true, help: 'Comma-separated. All domains must resolve to this server and nginx must serve the ACME challenge on port 80.', extra: 'autocomplete="off"' })}
        ${ui.field({ label: 'Account email', name: 'email', type: 'email', value: settings.adminEmail || '', placeholder: 'hostmaster@example.com', required: true })}
        <div class="span-2">${ui.switchField({ label: 'Use Let’s Encrypt staging', name: 'staging', help: 'Use for testing; staging certificates are not trusted by browsers or mail clients.' })}</div>
      </div><div class="notice tone-warning mt-3">${ui.icon('alert', 16)}<div><strong>Rate limits apply</strong><p>Let’s Encrypt enforces issuance limits. Use staging while bringing up a new domain.</p></div></div><div class="form-actions"><span class="form-note">A successful issue installs the certificate in certbot’s live directory; configure Postfix/Dovecot/nginx to load it separately if needed.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">Run certbot</button></div></div></form>`,
    });
    const form = modal.root.querySelector('#tls-issue-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        const result = await ctx.api('/control/api/tls/issue', { method: 'POST', body: { domains: values.domains, email: values.email, staging: values.staging } });
        modal.close();
        ctx.modal({ title: 'Certificate issued', size: 'large', body: `<div class="notice tone-good">${ui.icon('check-circle', 16)}<div><strong>certbot completed</strong><p>${values.staging ? 'This was a staging certificate.' : 'The certificate is now available to the configured services.'}</p></div></div><div class="divider"></div>${ui.codeBlock(result.output || '', { language: 'certbot output', maxHeight: '50vh' })}` });
        await load();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Certificate issuance failed' });
        if (error.details) ctx.modal({ title: 'certbot output', size: 'large', body: ui.codeBlock(String(error.details), { language: 'certbot output', maxHeight: '55vh' }) });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  const openSelfSigned = () => {
    const host = location.hostname || 'localhost';
    const modal = ctx.modal({
      title: 'Generate a self-signed certificate', subtitle: 'Useful for the control endpoint or a lab before public DNS is ready.',
      size: 'medium',
      body: `<form id="tls-self-form"><div class="form-grid">
        ${ui.field({ label: 'Common name', name: 'commonName', value: host, required: true, help: 'A hostname or IP address. The file name is derived from this value.' })}
        <div class="span-2">${ui.field({ label: 'Additional SANs', name: 'sans', placeholder: 'mail.example.com, 203.0.113.10', help: 'Optional comma-separated DNS names or IP addresses.' })}</div>
        <div class="span-2">${ui.switchField({ label: 'Overwrite an existing file', name: 'force', help: 'Without this, an existing certificate with the same name is not overwritten.' })}</div>
      </div><div class="notice tone-warning mt-3">${ui.icon('alert', 16)}<div><strong>Self-signed means untrusted</strong><p>Browsers and mail clients will warn unless the certificate is explicitly trusted. Use it only for the control endpoint or testing.</p></div></div><div class="form-actions"><span class="form-note">RSA 2048 · 825-day validity · Server Auth EKU.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">Generate certificate</button></div></div></form>`,
    });
    const form = modal.root.querySelector('#tls-self-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        const result = await ctx.api('/control/api/tls/self-signed', { method: 'POST', body: { commonName: values.commonName, sans: values.sans || '', force: !!values.force } });
        modal.close();
        ctx.modal({
          title: 'Self-signed certificate created', size: 'medium',
          body: `<div class="notice tone-good">${ui.icon('check-circle', 16)}<div><strong>${esc(result.certificate?.name || values.commonName)}</strong><p>Valid until ${esc(fmt.dateOnly(result.certificate?.notAfter))}.</p></div></div><div class="kv-grid" style="margin-top:12px">${ui.kv([['Certificate', `<span class="mono break-all">${esc(result.certPath)}</span>`], ['Private key', `<span class="mono break-all">${esc(result.keyPath)}</span>`]])}</div>`,
        });
        await load();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not generate certificate' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  document.getElementById('tls-import')?.addEventListener('click', openImport);
  document.getElementById('tls-assign')?.addEventListener('click', openAssign);
  document.getElementById('tls-issue')?.addEventListener('click', openIssue);
  document.getElementById('tls-self-signed')?.addEventListener('click', openSelfSigned);
  document.getElementById('tls-refresh')?.addEventListener('click', load);
  await load();
}

/* =============================================================== access */

const PERMISSION_GROUPS = [
  { title: 'Read access', match: (p) => p.endsWith('.read') || p === 'settings.write' && false },
  { title: 'Manage access', match: (p) => p.endsWith('.manage') },
  { title: 'Panel administration', match: (p) => ['admin.manage', 'group.manage', 'sessions.manage', 'settings.write'].includes(p) },
];

function permissionLabel(permission) {
  const parts = permission.split('.');
  if (parts.length === 2) return `${ui.humanTitle(parts[1])} · ${ui.humanTitle(parts[0])}`;
  return ui.humanTitle(permission);
}

function permissionCheckboxes(permissions, selected = [], name = 'permissions') {
  const groups = [
    { title: 'Read-only capabilities', items: permissions.filter((p) => p.endsWith('.read')) },
    { title: 'Management capabilities', items: permissions.filter((p) => p.endsWith('.manage')) },
    { title: 'Panel administration', items: permissions.filter((p) => ['admin.manage', 'group.manage', 'sessions.manage', 'settings.write'].includes(p)) },
  ];
  return groups.filter((group) => group.items.length).map((group) => `<div class="form-section-title" style="margin-top:10px">${esc(group.title)}</div><div class="check-grid is-3">${group.items.map((permission) => `<label class="check-field"><input type="checkbox" name="${esc(name)}" value="${esc(permission)}"${selected.includes(permission) ? ' checked' : ''}><span>${esc(permissionLabel(permission))}<small class="mono">${esc(permission)}</small></span></label>`).join('')}</div>`).join('');
}

export async function renderAccess(ctx) {
  const canAdmin = ctx.can('admin.manage');
  const canGroups = ctx.can('group.manage');
  const canSessions = ctx.can('sessions.manage');
  const canAudit = ctx.can('audit.read');
  let requestedTab = ctx.query.tab || 'users';
  const allowedTabs = ['users', 'groups'];
  if (canSessions) allowedTabs.push('sessions');
  if (canAudit) allowedTabs.push('audit');
  allowedTabs.push('roles');
  if (!allowedTabs.includes(requestedTab)) requestedTab = 'users';
  const tab = requestedTab;
  ctx.setTitle('Access & security', 'Security');

  let meta;
  let users = [];
  let groups = [];
  try {
    const [metaPayload, userPayload, groupPayload] = await Promise.all([
      ctx.api('/control/api/access/meta'),
      ctx.api('/control/api/access/users'),
      ctx.api('/control/api/access/groups'),
    ]);
    meta = metaPayload;
    users = userPayload.users || [];
    groups = groupPayload.groups || [];
  } catch (error) {
    ctx.root.innerHTML = ui.errorState(errorText(error));
    return;
  }
  if (!ctx.isCurrent()) return;
  const permissions = meta.permissions || [];
  const roles = meta.roles || [];
  const tabLinks = [
    { id: 'users', label: 'Panel users', icon: 'users', count: users.length },
    { id: 'groups', label: 'Permission groups', icon: 'layers', count: groups.length },
    ...(canSessions ? [{ id: 'sessions', label: 'Sessions', icon: 'key' }] : []),
    ...(canAudit ? [{ id: 'audit', label: 'Audit log', icon: 'list' }] : []),
    { id: 'roles', label: 'Role matrix', icon: 'shield' },
  ].map((item) => ({ ...item, href: `#/access?tab=${item.id}`, active: item.id === tab }));

  let body = '';
  if (tab === 'users') {
    body = `<section class="card"><header class="card-head"><div><h3>Panel accounts</h3><p>${users.length} account${users.length === 1 ? '' : 's'} · roles and group-derived permissions</p></div>${canAdmin ? `<button class="btn btn-primary btn-sm" type="button" id="user-create">${ui.icon('plus', 13)} New user</button>` : ''}</header><div class="card-body is-flush">
      <div class="table-wrap is-bare"><table class="data"><thead><tr><th>Account</th><th>Role</th><th>Groups</th><th>TOTP</th><th>Status</th><th>Last sign-in</th><th></th></tr></thead><tbody>
      ${users.map((user) => `<tr><td><span class="cell-main">${esc(user.name || user.email)}${user.current ? ` ${ui.badge('You', 'good')}` : ''}</span><span class="cell-sub mono">${esc(user.email)}</span></td><td>${ui.badge(ui.humanTitle(user.role), user.role === 'superadmin' ? 'critical' : user.role === 'admin' ? 'accent' : user.role === 'operator' ? 'info' : 'neutral', user.role === 'superadmin' ? 'shield-check' : 'user')}</td><td>${esc(user.groups || '—')}</td><td>${user.totpEnabled ? ui.badge('Enabled', 'good', 'shield-check') : ui.badge('Off', 'warning', 'alert')}</td><td>${ui.statusPill(user.status, ui.humanTitle(user.status))}${user.lockedUntil ? `<span class="cell-sub text-critical">Locked until ${esc(fmt.dateTime(user.lockedUntil))}</span>` : ''}</td><td>${user.lastLoginAt ? esc(fmt.timeAgo(user.lastLoginAt)) : '<span class="muted">Never</span>'}</td><td class="actions">${canAdmin ? `<button class="icon-btn" type="button" data-user-edit="${esc(user.id)}" title="Edit user" aria-label="Edit user">${ui.icon('edit', 14)}</button><button class="icon-btn" type="button" data-user-password="${esc(user.id)}" title="Reset password" aria-label="Reset password">${ui.icon('key', 14)}</button><button class="icon-btn" type="button" data-user-revoke="${esc(user.id)}" title="Revoke sessions" aria-label="Revoke sessions">${ui.icon('logout', 14)}</button>${user.totpEnabled ? `<button class="icon-btn" type="button" data-user-totp="${esc(user.id)}" title="Disable TOTP" aria-label="Disable TOTP">${ui.icon('shield-off', 14)}</button>` : ''}${!user.current ? `<button class="icon-btn is-danger" type="button" data-user-delete="${esc(user.id)}" title="Delete account" aria-label="Delete account">${ui.icon('trash', 14)}</button>` : ''}` : ''}</td></tr>`).join('')}</tbody></table></div>
    </div></section>`;
  } else if (tab === 'groups') {
    body = `<section class="card"><header class="card-head"><div><h3>Permission groups</h3><p>Groups extend the built-in role with additional permissions.</p></div>${canGroups ? `<button class="btn btn-primary btn-sm" type="button" id="group-create">${ui.icon('plus', 13)} New group</button>` : ''}</header><div class="card-body">${groups.length ? `<div class="grid grid-2">${groups.map((group) => `<article class="card" style="box-shadow:none"><div class="card-body"><div class="flex between" style="align-items:flex-start"><div><strong style="font-size:13px">${esc(group.name)}</strong><p class="form-note" style="margin-top:3px">${esc(group.description || 'No description')}</p></div><div class="badge-row">${group.permissions.length} permission${group.permissions.length === 1 ? '' : 's'}</div></div><div class="divider-soft"></div><div class="chip-list">${group.permissions.slice(0, 8).map((permission) => `<span class="mono-chip">${esc(permission)}</span>`).join('')}${group.permissions.length > 8 ? `<span class="mono-chip">+${group.permissions.length - 8}</span>` : ''}</div><div class="divider-soft"></div><p class="form-note">Members: ${esc(group.members || 'none')}</p>${canGroups ? `<div class="form-actions" style="padding-top:9px;margin-top:9px"><span></span><div class="form-actions-right"><button class="btn btn-secondary btn-sm" type="button" data-group-edit="${esc(group.id)}">${ui.icon('edit', 13)} Edit</button><button class="btn btn-soft-danger btn-sm" type="button" data-group-delete="${esc(group.id)}">${ui.icon('trash', 13)} Delete</button></div></div>` : ''}</div></article>`).join('')}</div>` : ui.emptyState('No custom groups', 'Create a group when a panel role needs a targeted extra permission.', canGroups ? `<button class="btn btn-secondary btn-sm" type="button" id="group-create-empty">${ui.icon('plus', 13)} New group</button>` : '', 'layers')}</div></section>`;
  } else if (tab === 'sessions') {
    body = `<section class="card"><header class="card-head"><div><h3>Active panel sessions</h3><p>Every live operator session across the control plane</p></div></header><div class="card-body" id="admin-sessions">${ui.loadingState('Loading sessions…', 3)}</div></section>`;
  } else if (tab === 'audit') {
    body = `<section class="card"><header class="card-head"><div><h3>Audit log</h3><p>Append-only operator history with IP and user-agent context</p></div><a class="btn btn-secondary btn-sm" href="/control/api/audit.csv" download>${ui.icon('download', 13)} Export CSV</a></header><div class="card-body">
      <form id="audit-filter" class="filterbar">${ui.icon('search', 14)}<input class="input" name="actor" placeholder="Actor or target" style="min-width:180px"><input class="input" name="action" placeholder="Action contains…" style="min-width:150px"><input class="input" type="date" name="from" aria-label="From date"><span class="muted">to</span><input class="input" type="date" name="to" aria-label="To date"><button class="btn btn-secondary" type="submit">Filter</button><button class="btn btn-ghost" type="button" id="audit-reset">Reset</button></form>
      <div id="audit-body">${ui.loadingState('Loading audit log…', 4)}</div>
    </div></section>`;
  } else {
    body = `<div class="grid grid-2">${roles.map((role) => `<section class="card"><header class="card-head"><div><h3>${esc(ui.humanTitle(role.name))}</h3><p>${role.permissions.includes('*') ? 'Full control plane access' : `${role.permissions.length} permissions`}</p></div>${role.name === 'superadmin' ? ui.badge('Full access', 'critical', 'shield-check') : ui.badge('Scoped', 'neutral')}</header><div class="card-body"><div class="chip-list">${role.permissions.includes('*') ? '<span class="badge tone-critical">* — all permissions</span>' : role.permissions.map((permission) => `<span class="mono-chip">${esc(permission)}</span>`).join('')}</div>${role.name === 'readonly' ? `<p class="form-note" style="margin-top:11px">Read-only roles still see operational details such as audit entries and live service state. Do not use them as a public viewer.</p>` : ''}</div></section>`).join('')}</div>
      <div class="notice tone-warning mt-4">${ui.icon('alert', 16)}<div><strong>High-risk permissions</strong><p>${meta.highRiskPermissions.map((p) => `<span class="mono-chip">${esc(p)}</span>`).join(' ')} — only a superadmin can grant these through groups. Keep the number of superadmins small.</p></div></div>
      <div class="card mt-4"><header class="card-head"><div><h3>Protection model</h3><p>Applied to all panel accounts</p></div></header><div class="card-body"><div class="grid grid-3">
        <div><div class="tile-link"><span>${ui.icon('lock', 15)} Password hashing</span><span class="value">Argon2id</span></div><div class="tile-link"><span>${ui.icon('shield-check', 15)} Second factor</span><span class="value">TOTP</span></div></div>
        <div><div class="tile-link"><span>${ui.icon('alert-circle', 15)} Lockout</span><span class="value">5 failures / 15 min</span></div><div class="tile-link"><span>${ui.icon('key', 15)} Session</span><span class="value">server-side TTL</span></div></div>
        <div><div class="tile-link"><span>${ui.icon('shield', 15)} CSRF</span><span class="value">signed double-submit</span></div><div class="tile-link"><span>${ui.icon('list', 15)} Audit</span><span class="value">IP + UA</span></div></div>
      </div></div></div>`;
  }

  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Security', title: 'Access & security',
    description: 'Panel identity, least-privilege groups, session control and the complete change history.',
    actions: `<a class="btn btn-ghost" href="#/account">${ui.icon('user', 14)} My security</a><button class="btn btn-secondary" type="button" id="access-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    ${ui.tabs(tabLinks)}
    <div class="stack" id="access-body">${body}</div>
    <div class="notice tone-info mt-4">${ui.icon('info', 16)}<div><strong>Role changes require a new session to take effect</strong><p>Permissions are hydrated into the session on each request from the current role and group membership. Existing sessions are not cached with stale permissions.</p></div></div>`;

  document.getElementById('access-refresh')?.addEventListener('click', () => renderAccess(ctx));

  const showUserCredentials = (email, password) => secretModal(ctx, 'Panel account created', `Temporary password for ${email}`, password, 'The new operator must change this password after signing in.');

  const openUserEditor = (user) => {
    const editing = !!user;
    const modal = ctx.modal({
      title: editing ? 'Edit panel user' : 'Create panel user', size: 'large',
      body: `<form id="access-user-form">
        <div class="form-grid">
          ${ui.field({ label: 'Email', name: 'email', type: 'email', value: user?.email || '', required: !editing, help: editing ? 'The sign-in email is immutable.' : 'The account must be a real operator address.', extra: editing ? 'disabled' : '' })}
          ${ui.field({ label: 'Display name', name: 'name', value: user?.name || '', required: true })}
          ${ui.field({ label: 'Role', name: 'role', type: 'select', value: user?.role || 'readonly', options: roles.map((role) => ({ value: role.name, label: ui.humanTitle(role.name) })), help: 'Role permissions apply immediately. Groups can add more.' })}
          ${ui.field({ label: 'Session TTL (hours)', name: 'ttlHours', type: 'number', value: String(Math.round((user?.sessionTtlSeconds || 28800) / 3600)), min: '1', max: '168', help: 'Between 1 hour and 7 days.' })}
          ${editing ? ui.field({ label: 'Status', name: 'status', type: 'select', value: user.status, options: [{ value: 'active', label: 'Active' }, { value: 'disabled', label: 'Disabled' }] }) : `<div class="span-2">${ui.field({ label: 'Initial password (optional)', name: 'password', type: 'password', help: 'Leave blank to generate a temporary password. Minimum 12 characters.', autocomplete: 'new-password' })}</div>`}
        </div>
        <div class="form-section-title" style="margin-top:16px">Groups</div>
        <div class="check-grid">${groups.length ? groups.map((group) => `<label class="check-field"><input type="checkbox" name="groupIds" value="${esc(group.id)}"${String(user?.groups || '').split(',').map((value) => value.trim()).includes(group.name) ? ' checked' : ''}><span>${esc(group.name)}<small>${group.permissions.length} permissions · ${esc(group.description || '')}</small></span></label>`).join('') : '<p class="muted">No custom groups exist.</p>'}</div>
        <div class="form-actions"><span class="form-note">Superadmin is a high-risk role. At least one active superadmin must remain.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">${editing ? 'Save user' : 'Create user'}</button></div></div>
      </form>`,
    });
    const form = modal.root.querySelector('#access-user-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const groupIds = ui.selectedValues(form, 'groupIds');
      const ttl = Math.round(Number(values.ttlHours || 8) * 3600);
      if (ttl < 900 || ttl > 604800) { ui.toast('Session TTL must be between 1 hour and 7 days.', 'critical'); return; }
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        if (editing) {
          await ctx.api(`/control/api/access/users/${user.id}`, { method: 'PATCH', body: { name: values.name, role: values.role, status: values.status, groupIds, sessionTtlSeconds: ttl } });
          modal.close();
          ui.toast('Panel user updated.', 'good');
        } else {
          const result = await ctx.api('/control/api/access/users', { method: 'POST', body: { email: values.email, name: values.name, role: values.role, password: values.password || '', groupIds, sessionTtlSeconds: ttl } });
          modal.close();
          ui.toast('Panel user created.', 'good');
          if (result.temporaryPassword) showUserCredentials(values.email, result.temporaryPassword);
        }
        await renderAccess(ctx);
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not save user' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  const openGroupEditor = (group) => {
    const editing = !!group;
    const modal = ctx.modal({
      title: editing ? 'Edit permission group' : 'Create permission group', size: 'large',
      body: `<form id="access-group-form"><div class="form-grid">
        ${ui.field({ label: 'Group name', name: 'name', value: group?.name || '', required: true })}
        ${ui.field({ label: 'Description', name: 'description', value: group?.description || '', placeholder: 'What this group is for' })}
      </div><div class="mt-3">${permissionCheckboxes(permissions, group?.permissions || [])}</div><div class="form-actions"><span class="form-note">Grant only what the operator needs. High-risk permissions require a superadmin.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">${editing ? 'Save group' : 'Create group'}</button></div></div></form>`,
    });
    const form = modal.root.querySelector('#access-group-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const selected = ui.selectedValues(form, 'permissions');
      if (!values.name?.trim()) { ui.toast('Group name is required.', 'critical'); return; }
      if (!selected.length) { ui.toast('Select at least one permission.', 'critical'); return; }
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        if (editing) await ctx.api(`/control/api/access/groups/${group.id}`, { method: 'PATCH', body: { name: values.name, description: values.description || '', permissions: selected } });
        else await ctx.api('/control/api/access/groups', { method: 'POST', body: { name: values.name, description: values.description || '', permissions: selected } });
        modal.close();
        ui.toast(editing ? 'Group updated.' : 'Group created.', 'good');
        await renderAccess(ctx);
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not save group' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  ctx.root.querySelector('#user-create')?.addEventListener('click', () => openUserEditor(null));
  ctx.root.querySelectorAll('[data-user-edit]').forEach((button) => button.addEventListener('click', () => openUserEditor(users.find((user) => user.id === button.dataset.userEdit))));
  ctx.root.querySelectorAll('[data-user-password]').forEach((button) => button.addEventListener('click', async () => {
    const user = users.find((u) => u.id === button.dataset.userPassword);
    if (!user) return;
    const ok = await ctx.confirm({ title: 'Reset panel password', message: `Generate a new temporary password for ${user.email}? Other sessions will be revoked.`, confirmLabel: 'Reset password', tone: 'primary' });
    if (!ok) return;
    try {
      const result = await ctx.api(`/control/api/access/users/${user.id}/password`, { method: 'POST', body: { password: '' } });
      secretModal(ctx, 'Panel password reset', `Temporary password for ${user.email}`, result.temporaryPassword, 'Share it over a secure channel; the operator must change it after signing in.');
    } catch (error) { ui.toast(errorText(error), 'critical', { title: 'Password reset failed' }); }
  }));
  ctx.root.querySelectorAll('[data-user-revoke]').forEach((button) => button.addEventListener('click', async () => {
    const user = users.find((u) => u.id === button.dataset.userRevoke);
    if (!user) return;
    const ok = await ctx.confirm({ title: 'Revoke all sessions', message: `Sign ${user.email} out of every browser session except the current one?`, confirmLabel: 'Revoke sessions', tone: 'danger' });
    if (!ok) return;
    try { const result = await ctx.api(`/control/api/access/sessions/revoke-user/${user.id}`, { method: 'POST' }); ui.toast(`${result.revoked} session${result.revoked === 1 ? '' : 's'} revoked.`, 'good'); }
    catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not revoke sessions' }); }
  }));
  ctx.root.querySelectorAll('[data-user-totp]').forEach((button) => button.addEventListener('click', async () => {
    const user = users.find((u) => u.id === button.dataset.userTotp);
    if (!user) return;
    const ok = await ctx.confirm({ title: 'Disable TOTP', message: `Disable two-factor authentication for ${user.email}? Their next sign-in will require only a password.`, confirmLabel: 'Disable TOTP', tone: 'danger' });
    if (!ok) return;
    try { await ctx.api(`/control/api/access/users/${user.id}/totp-disable`, { method: 'POST' }); ui.toast('TOTP disabled.', 'warning'); await renderAccess(ctx); }
    catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not disable TOTP' }); }
  }));
  ctx.root.querySelectorAll('[data-user-delete]').forEach((button) => button.addEventListener('click', async () => {
    const user = users.find((u) => u.id === button.dataset.userDelete);
    if (!user) return;
    const ok = await ctx.confirm({ title: 'Delete panel account', message: `Delete ${user.email}? Their sessions, notification handles and audit actor link are removed; audit entries remain.`, confirmLabel: 'Delete account', tone: 'danger', requireText: user.email });
    if (!ok) return;
    try { await ctx.api(`/control/api/access/users/${user.id}`, { method: 'DELETE' }); ui.toast('Panel account deleted.', 'good'); await renderAccess(ctx); }
    catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not delete account' }); }
  }));

  ctx.root.querySelector('#group-create')?.addEventListener('click', () => openGroupEditor(null));
  ctx.root.querySelector('#group-create-empty')?.addEventListener('click', () => openGroupEditor(null));
  ctx.root.querySelectorAll('[data-group-edit]').forEach((button) => button.addEventListener('click', () => openGroupEditor(groups.find((group) => group.id === button.dataset.groupEdit))));
  ctx.root.querySelectorAll('[data-group-delete]').forEach((button) => button.addEventListener('click', async () => {
    const group = groups.find((g) => g.id === button.dataset.groupDelete);
    if (!group) return;
    const ok = await ctx.confirm({ title: 'Delete permission group', message: `Delete ${group.name}? Members keep their built-in role permissions; the group's extra permissions disappear immediately.`, confirmLabel: 'Delete group', tone: 'danger' });
    if (!ok) return;
    try { await ctx.api(`/control/api/access/groups/${group.id}`, { method: 'DELETE' }); ui.toast('Group deleted.', 'good'); await renderAccess(ctx); }
    catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not delete group' }); }
  }));

  if (tab === 'sessions') {
    const host = document.getElementById('admin-sessions');
    try {
      const payload = await ctx.api('/control/api/access/sessions');
      if (!ctx.isCurrent()) return;
      const sessions = Array.isArray(payload.sessions) ? payload.sessions : [];
      host.innerHTML = sessions.length ? `<div class="table-wrap"><table class="data"><thead><tr><th>Operator</th><th>Client</th><th>IP</th><th>Last seen</th><th>Expires</th><th></th></tr></thead><tbody>${sessions.map((session) => `<tr><td><span class="cell-main">${esc(session.name || session.email)}</span><span class="cell-sub mono">${esc(session.email)}</span></td><td><span class="cell-main">${session.current ? ui.badge('This session', 'good') : esc(ui.truncate(session.userAgent, 60) || 'Unknown client')}</span><span class="cell-sub">${esc(session.role)}</span></td><td class="mono">${esc(session.ip)}</td><td>${esc(fmt.timeAgo(session.lastSeenAt))}</td><td>${esc(fmt.dateTime(session.expiresAt))}</td><td class="actions">${session.current ? '' : `<button class="icon-btn is-danger" type="button" data-session-revoke="${esc(session.id)}" title="Revoke session" aria-label="Revoke session">${ui.icon('logout', 15)}</button>`}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No active sessions', '', '', 'key');
      host.querySelectorAll('[data-session-revoke]').forEach((button) => button.addEventListener('click', async () => {
        const ok = await ctx.confirm({ title: 'Revoke session', message: 'That browser will be signed out immediately.', confirmLabel: 'Revoke session', tone: 'danger' });
        if (!ok) return;
        try { await ctx.api(`/control/api/access/sessions/${button.dataset.sessionRevoke}`, { method: 'DELETE' }); ui.toast('Session revoked.', 'good'); await renderAccess(ctx); }
        catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not revoke session' }); }
      }));
    } catch (error) { host.innerHTML = ui.inlineNotice('critical', 'Could not load sessions', errorText(error)); }
  }

  if (tab === 'audit') {
    let page = 1;
    let filters = { actor: '', action: '', from: '', to: '' };
    const loadAudit = async () => {
      const host = document.getElementById('audit-body');
      if (!host) return;
      const params = new URLSearchParams({ page: String(page), limit: '100', ...filters });
      try {
        const payload = await ctx.api(`/control/api/audit?${params.toString()}`);
        if (!ctx.isCurrent()) return;
        const entries = Array.isArray(payload.entries) ? payload.entries : [];
        const pages = Math.max(1, Math.ceil(payload.total / payload.limit));
        host.innerHTML = entries.length ? `<div class="table-wrap"><table class="data"><thead><tr><th>Time</th><th>Actor</th><th>Action</th><th>Target</th><th>Detail</th><th>IP</th><th></th></tr></thead><tbody>${entries.map((entry) => `<tr><td class="nowrap-cell">${esc(fmt.dateTime(entry.createdAt))}</td><td>${esc(entry.actor || 'system')}</td><td><span class="cell-main">${esc(ui.humanTitle(entry.action))}</span><span class="cell-sub mono">${esc(entry.action)}</span></td><td class="wrap-anywhere">${esc(entry.target || '—')}</td><td>${esc(ui.truncate(entry.detail || '—', 72))}</td><td class="mono">${esc(entry.ip || '—')}</td><td class="actions"><button class="icon-btn" type="button" data-audit-detail="${entry.id}" title="Audit detail" aria-label="Audit detail">${ui.icon('eye', 14)}</button></td></tr>`).join('')}</tbody></table></div>
          <div class="table-footer"><span>${ui.num(payload.total)} entries · page ${page} of ${pages}</span><div class="pagination"><button class="page-btn" type="button" id="audit-prev" ${page <= 1 ? 'disabled' : ''}>${ui.icon('chevron-left', 13)}</button><span class="page-btn is-active">${page}</span><button class="page-btn" type="button" id="audit-next" ${page >= pages ? 'disabled' : ''}>${ui.icon('chevron-right', 13)}</button></div></div>` : ui.emptyState('No audit entries', 'No recorded action matches these filters.', '', 'list');
        document.getElementById('audit-prev')?.addEventListener('click', () => { if (page > 1) { page -= 1; loadAudit(); } });
        document.getElementById('audit-next')?.addEventListener('click', () => { if (page < pages) { page += 1; loadAudit(); } });
        host.querySelectorAll('[data-audit-detail]').forEach((button) => button.addEventListener('click', () => {
          const entry = entries.find((row) => String(row.id) === button.dataset.auditDetail);
          if (!entry) return;
          ctx.modal({ title: 'Audit entry', subtitle: entry.action, size: 'medium', body: `<div class="kv-grid" style="margin-bottom:12px">${ui.kv([
            ['Time', esc(fmt.dateTime(entry.createdAt))], ['Actor', esc(entry.actor || 'system')], ['Action', `<span class="mono">${esc(entry.action)}</span>`],
            ['Target', esc(entry.target || '—')], ['IP', `<span class="mono">${esc(entry.ip || '—')}</span>`], ['User agent', esc(entry.userAgent || '—')],
          ])}</div><p class="form-note">${esc(entry.detail || 'No detail recorded.')}</p><div class="mt-3">${ui.codeBlock(JSON.stringify(entry.metadata || {}, null, 2), { language: 'metadata' })}</div>` });
        }));
      } catch (error) { host.innerHTML = ui.inlineNotice('critical', 'Could not load audit log', errorText(error)); }
    };
    document.getElementById('audit-filter')?.addEventListener('submit', (event) => {
      event.preventDefault();
      const values = ui.formValues(event.currentTarget);
      filters = { actor: values.actor || '', action: values.action || '', from: values.from || '', to: values.to || '' };
      page = 1;
      loadAudit();
    });
    document.getElementById('audit-reset')?.addEventListener('click', () => {
      filters = { actor: '', action: '', from: '', to: '' };
      const form = document.getElementById('audit-filter');
      if (form) form.reset();
      page = 1;
      loadAudit();
    });
    await loadAudit();
  }
}

/* =============================================================== API keys */

export async function renderAPIKeys(ctx) {
  const canManage = ctx.can('api.manage');
  let keys = [];
  ctx.setTitle('API keys', 'Security');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Security', title: 'API keys',
    description: 'Scoped bearer credentials for the transactional send API, suppression management and template rendering.',
    actions: `${canManage ? `<button class="btn btn-primary" type="button" id="apikey-create">${ui.icon('plus', 15)} Create key</button>` : ''}<button class="btn btn-secondary" type="button" id="apikey-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    <div class="grid grid-main">
      <div class="stack">
        <section class="card"><header class="card-head"><div><h3>API keys</h3><p id="apikey-summary">Loading keys…</p></div></header><div class="card-body" id="apikey-list">${ui.loadingState('Loading API keys…', 3)}</div></section>
        <section class="card"><header class="card-head"><div><h3>Endpoint reference</h3><p>Base URL <span class="mono">https://${esc(location.hostname)}:8808/api/v1</span></p></div></header><div class="card-body">
          <div class="table-wrap"><table class="data compact"><thead><tr><th>Method</th><th>Path</th><th>Scope</th><th>Purpose</th></tr></thead><tbody>
            <tr><td>${ui.badge('GET', 'info')}</td><td class="mono">/api/v1</td><td>read</td><td>Key identity and endpoint discovery</td></tr>
            <tr><td>${ui.badge('GET', 'info')}</td><td class="mono">/api/v1/suppressions</td><td>read</td><td>List suppressions</td></tr>
            <tr><td>${ui.badge('POST', 'warning')}</td><td class="mono">/api/v1/suppressions</td><td>admin</td><td>Create a suppression</td></tr>
            <tr><td>${ui.badge('DELETE', 'critical')}</td><td class="mono">/api/v1/suppressions/{id}</td><td>admin</td><td>Delete a suppression</td></tr>
            <tr><td>${ui.badge('POST', 'warning')}</td><td class="mono">/api/v1/messages</td><td>write</td><td>Send a transactional message</td></tr>
            <tr><td>${ui.badge('GET', 'info')}</td><td class="mono">/api/v1/messages</td><td>read</td><td>List send history</td></tr>
            <tr><td>${ui.badge('GET', 'info')}</td><td class="mono">/api/v1/messages/{id}</td><td>read</td><td>Get a send result</td></tr>
            <tr><td>${ui.badge('GET', 'info')}</td><td class="mono">/api/v1/templates</td><td>read</td><td>List templates</td></tr>
            <tr><td>${ui.badge('POST', 'warning')}</td><td class="mono">/api/v1/templates</td><td>write</td><td>Create a template</td></tr>
          </tbody></table></div>
          <div class="notice tone-warning mt-3">${ui.icon('alert', 16)}<div><strong>Keys are bearer secrets</strong><p>Only a SHA-256 digest is stored. Treat a key like a password: never put it in frontend code, URLs or logs.</p></div></div>
          ${ui.codeBlock(`curl -sS https://YOUR-CONTROL-HOST:8808/api/v1 \\\n  -H "Authorization: Bearer a8m_..."`, { language: 'curl' })}
        </div></section>
      </div>
      <div class="stack">
        <section class="card"><header class="card-head"><div><h3>Scopes</h3></div></header><div class="card-body">
          <div class="tile-link"><span>${ui.icon('eye', 15)} read</span><span class="value">history, templates, suppressions</span></div>
          <div class="tile-link"><span>${ui.icon('send', 15)} write</span><span class="value">send + template writes</span></div>
          <div class="tile-link"><span>${ui.icon('shield-check', 15)} admin</span><span class="value">suppression changes</span></div>
          <div class="note-strip" style="margin-top:11px">${ui.icon('info', 13)} <span>Scopes are cumulative: admin includes write and read. Create a separate key per integration so one can be revoked without breaking the others.</span></div>
        </div></section>
        <section class="card"><header class="card-head"><div><h3>Key lifecycle</h3></div></header><div class="card-body">
          <div class="timeline">
            <div class="timeline-item"><span class="timeline-dot"></span><strong>Create</strong><span>The raw key is displayed once.</span></div>
            <div class="timeline-item"><span class="timeline-dot"></span><strong>Use</strong><span>Last-used time is updated on every authenticated call.</span></div>
            <div class="timeline-item"><span class="timeline-dot"></span><strong>Expire or revoke</strong><span>Expired and revoked keys are rejected immediately.</span></div>
          </div>
        </div></section>
      </div>
    </div>`;

  const load = async () => {
    const host = document.getElementById('apikey-list');
    try {
      const payload = await ctx.api('/control/api/access/api-keys');
      if (!ctx.isCurrent()) return;
      keys = Array.isArray(payload.apiKeys) ? payload.apiKeys : [];
      const active = keys.filter((key) => key.status === 'active').length;
      document.getElementById('apikey-summary').textContent = `${keys.length} key${keys.length === 1 ? '' : 's'} · ${active} active`;
      host.innerHTML = keys.length ? `<div class="table-wrap"><table class="data"><thead><tr><th>Name</th><th>Prefix</th><th>Scopes</th><th>Created</th><th>Last used</th><th>Status</th><th></th></tr></thead><tbody>${keys.map((key) => `<tr><td><span class="cell-main">${esc(key.name)}</span><span class="cell-sub">${key.expiresAt ? `expires ${esc(fmt.dateOnly(key.expiresAt))}` : 'no expiry'}</span></td><td class="mono">${esc(key.prefix)}…</td><td><div class="chip-list">${key.scopes.map((scope) => ui.badge(scope, scope === 'admin' ? 'critical' : scope === 'write' ? 'warning' : 'info')).join('')}</div></td><td>${esc(fmt.dateOnly(key.createdAt))}</td><td>${key.lastUsedAt ? esc(fmt.timeAgo(key.lastUsedAt)) : '<span class="muted">Never</span>'}</td><td>${ui.statusPill(key.status)}</td><td class="actions">${canManage && key.status === 'active' ? `<button class="icon-btn is-danger" type="button" data-key-revoke="${esc(key.id)}" title="Revoke key" aria-label="Revoke key">${ui.icon('trash', 15)}</button>` : ''}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No API keys', 'Create a scoped key to use the transactional send and suppression API.', canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="apikey-create-empty">${ui.icon('plus', 13)} Create key</button>` : '', 'key');
      document.getElementById('apikey-create-empty')?.addEventListener('click', openCreate);
      host.querySelectorAll('[data-key-revoke]').forEach((button) => button.addEventListener('click', async () => {
        const key = keys.find((row) => row.id === button.dataset.keyRevoke);
        if (!key) return;
        const ok = await ctx.confirm({ title: 'Revoke API key', message: `Revoke ${key.name} (${key.prefix}…)? Integrations using it fail immediately.`, confirmLabel: 'Revoke key', tone: 'danger' });
        if (!ok) return;
        try { await ctx.api(`/control/api/access/api-keys/${key.id}`, { method: 'DELETE' }); ui.toast('API key revoked.', 'good'); await load(); }
        catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not revoke key' }); }
      }));
    } catch (error) { host.innerHTML = ui.inlineNotice('critical', 'Could not load API keys', errorText(error)); }
  };

  const openCreate = () => {
    const modal = ctx.modal({
      title: 'Create API key', size: 'medium',
      body: `<form id="apikey-form"><div class="form-grid is-single">
        ${ui.field({ label: 'Key name', name: 'name', value: '', placeholder: 'Billing notification sender', required: true, help: 'Name it after the integration, not the person.' })}
        <div class="field"><label>Scopes</label><div class="option-list">
          <label class="check-field"><input type="checkbox" name="scopes" value="read" checked><span>read<small>Read history, templates and suppressions.</small></span></label>
          <label class="check-field"><input type="checkbox" name="scopes" value="write"><span>write<small>Send messages and write templates. Implies read.</small></span></label>
          <label class="check-field"><input type="checkbox" name="scopes" value="admin"><span>admin<small>Change suppressions and administrative API resources.</small></span></label>
        </div></div>
        ${ui.field({ label: 'Expires in (days)', name: 'expiresInDays', type: 'number', value: '90', min: 0, max: 730, help: '0 means no expiry. Prefer a bounded lifetime.' })}
      </div><div class="form-actions"><span class="form-note">The key is shown once. Store it in a secrets manager.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">Create key</button></div></div></form>`,
    });
    const form = modal.root.querySelector('#apikey-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const scopes = ui.selectedValues(form, 'scopes');
      if (!scopes.length) { ui.toast('Choose at least one scope.', 'critical'); return; }
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        const result = await ctx.api('/control/api/access/api-keys', { method: 'POST', body: { name: values.name, scopes, expiresInDays: Number(values.expiresInDays || 0) } });
        modal.close();
        ui.toast('API key created.', 'good');
        secretModal(ctx, 'API key created', `API key for ${values.name}`, result.key, result.message);
        await load();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not create API key' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  document.getElementById('apikey-create')?.addEventListener('click', openCreate);
  document.getElementById('apikey-refresh')?.addEventListener('click', load);
  await load();
}

/* =============================================================== webhooks */

export async function renderWebhooks(ctx) {
  const canManage = ctx.can('notifications.manage');
  let webhooks = [];
  let events = [];
  let deliveries = [];
  ctx.setTitle('Webhooks', 'Security');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Security', title: 'Webhooks',
    description: 'HMAC-signed outbound event delivery to your automation and incident tooling.',
    actions: `${canManage ? `<button class="btn btn-primary" type="button" id="webhook-create">${ui.icon('plus', 15)} New webhook</button>` : ''}<button class="btn btn-secondary" type="button" id="webhook-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    <div class="grid grid-main">
      <div class="stack">
        <section class="card"><header class="card-head"><div><h3>Endpoints</h3><p id="webhook-summary">Loading webhooks…</p></div></header><div class="card-body" id="webhook-list">${ui.loadingState('Loading webhooks…', 3)}</div></section>
        <section class="card"><header class="card-head"><div><h3>Recent deliveries</h3><p>Response codes and truncated bodies</p></div></header><div class="card-body" id="webhook-deliveries">${ui.loadingState('Loading deliveries…', 3)}</div></section>
      </div>
      <div class="stack">
        <section class="card"><header class="card-head"><div><h3>Signature verification</h3><p>Sign the raw request body with the endpoint secret</p></div></header><div class="card-body">
          <p class="form-note">Each delivery contains the headers below. Compare <span class="mono">X-Alt8Mail-Signature</span> to a hex HMAC-SHA256 of the exact raw body using the endpoint secret. Reject stale timestamps.</p>
          ${ui.codeBlock(`X-Alt8Mail-Event: service.down\nX-Alt8Mail-Signature: <hex hmac-sha256(body, secret)>\nContent-Type: application/json\n\n{"event":"service.down","timestamp":"...","data":{...}}`, { language: 'http' })}
          ${ui.codeBlock(`# Node.js verification\nconst expected = crypto.createHmac('sha256', secret).update(rawBody).digest('hex');\nconst ok = crypto.timingSafeEqual(Buffer.from(expected), Buffer.from(signature));`, { language: 'javascript' })}
        </div></section>
        <section class="card"><header class="card-head"><div><h3>Delivery policy</h3></div></header><div class="card-body">
          <div class="tile-link"><span>${ui.icon('clock', 15)} Timeout</span><span class="value">15 seconds</span></div>
          <div class="tile-link"><span>${ui.icon('lock', 15)} Redirects</span><span class="value">not followed</span></div>
          <div class="tile-link"><span>${ui.icon('refresh', 15)} Retries</span><span class="value">no automatic retry</span></div>
          <div class="tile-link"><span>${ui.icon('list', 15)} Log</span><span class="value">last 200 deliveries</span></div>
          <div class="note-strip" style="margin-top:11px">${ui.icon('info', 13)} <span>Return a 2xx response to mark a delivery as sent. Non-2xx responses and network failures are recorded for review.</span></div>
        </div></section>
      </div>
    </div>`;

  const load = async () => {
    try {
      const [hookPayload, deliveryPayload] = await Promise.all([
        ctx.api('/control/api/webhooks'),
        ctx.api('/control/api/webhooks/deliveries'),
      ]);
      if (!ctx.isCurrent()) return;
      webhooks = Array.isArray(hookPayload.webhooks) ? hookPayload.webhooks : [];
      events = Array.isArray(hookPayload.events) ? hookPayload.events : [];
      deliveries = Array.isArray(deliveryPayload.deliveries) ? deliveryPayload.deliveries : [];
      document.getElementById('webhook-summary').textContent = `${webhooks.length} endpoint${webhooks.length === 1 ? '' : 's'} · ${webhooks.filter((hook) => hook.enabled).length} enabled`;
      const list = document.getElementById('webhook-list');
      list.innerHTML = webhooks.length ? `<div class="table-wrap"><table class="data"><thead><tr><th>Name</th><th>URL</th><th>Events</th><th>State</th><th></th></tr></thead><tbody>${webhooks.map((hook) => `<tr><td><span class="cell-main">${esc(hook.name)}</span><span class="cell-sub">created ${esc(fmt.dateOnly(hook.createdAt))}</span></td><td><span class="mono break-all">${esc(hook.url)}</span></td><td><div class="chip-list">${hook.events.slice(0, 5).map((event) => `<span class="mono-chip">${esc(event)}</span>`).join('')}${hook.events.length > 5 ? `<span class="mono-chip">+${hook.events.length - 5}</span>` : ''}</div></td><td>${ui.statusPill(hook.enabled ? 'active' : 'disabled', hook.enabled ? 'Enabled' : 'Disabled')}</td><td class="actions">${canManage ? `<button class="icon-btn" type="button" data-webhook-test="${esc(hook.id)}" title="Send test" aria-label="Send test">${ui.icon('send', 14)}</button><button class="icon-btn" type="button" data-webhook-edit="${esc(hook.id)}" title="Edit webhook" aria-label="Edit webhook">${ui.icon('edit', 14)}</button><button class="icon-btn is-danger" type="button" data-webhook-delete="${esc(hook.id)}" title="Delete webhook" aria-label="Delete webhook">${ui.icon('trash', 14)}</button>` : ''}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No webhooks', 'Add an endpoint to receive signed control-plane events.', canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="webhook-create-empty">${ui.icon('plus', 13)} New webhook</button>` : '', 'webhook');
      document.getElementById('webhook-create-empty')?.addEventListener('click', () => openEditor(null));
      list.querySelectorAll('[data-webhook-test]').forEach((button) => button.addEventListener('click', async () => {
        try { await ctx.api(`/control/api/webhooks/${button.dataset.webhookTest}/test`, { method: 'POST' }); ui.toast('Test event queued.', 'good'); await load(); }
        catch (error) { ui.toast(errorText(error), 'critical', { title: 'Webhook test failed' }); }
      }));
      list.querySelectorAll('[data-webhook-edit]').forEach((button) => button.addEventListener('click', () => openEditor(webhooks.find((hook) => hook.id === button.dataset.webhookEdit))));
      list.querySelectorAll('[data-webhook-delete]').forEach((button) => button.addEventListener('click', async () => {
        const hook = webhooks.find((h) => h.id === button.dataset.webhookDelete);
        if (!hook) return;
        const ok = await ctx.confirm({ title: 'Delete webhook', message: `Delete ${hook.name}? Pending deliveries stop immediately and the secret is destroyed.`, confirmLabel: 'Delete webhook', tone: 'danger' });
        if (!ok) return;
        try { await ctx.api(`/control/api/webhooks/${hook.id}`, { method: 'DELETE' }); ui.toast('Webhook deleted.', 'good'); await load(); }
        catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not delete webhook' }); }
      }));
      const host = document.getElementById('webhook-deliveries');
      host.innerHTML = deliveries.length ? `<div class="table-wrap"><table class="data compact"><thead><tr><th>Status</th><th>Endpoint</th><th>Event</th><th>Code</th><th>Attempted</th></tr></thead><tbody>${deliveries.slice(0, 100).map((delivery) => `<tr><td>${ui.statusPill(delivery.status)}</td><td>${esc(delivery.name || delivery.webhookId)}</td><td class="mono">${esc(delivery.event)}</td><td class="num">${delivery.responseCode || '—'}</td><td>${esc(fmt.timeAgo(delivery.attemptedAt))}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No webhook deliveries', 'Test an endpoint to generate the first delivery.', '', 'activity');
    } catch (error) {
      document.getElementById('webhook-list').innerHTML = ui.inlineNotice('critical', 'Could not load webhooks', errorText(error));
    }
  };

  const openEditor = (hook) => {
    const editing = !!hook;
    const modal = ctx.modal({
      title: editing ? 'Edit webhook' : 'New webhook', size: 'large',
      body: `<form id="webhook-form"><div class="form-grid is-single">
        ${ui.field({ label: 'Name', name: 'name', value: hook?.name || '', required: true, placeholder: 'Incident bridge' })}
        ${ui.field({ label: 'Endpoint URL', name: 'url', type: 'url', value: hook?.url || '', required: true, placeholder: 'https://automation.example.com/alt8mail', help: 'Must be http(s). Redirects are not followed. Prefer HTTPS.' })}
        <div class="field"><label>Events</label><div class="check-grid">${events.map((event) => `<label class="check-field"><input type="checkbox" name="webhookEvents" value="${esc(event)}"${hook?.events?.includes(event) || (!hook && event === 'service.down') ? ' checked' : ''}><span class="mono">${esc(event)}</span></label>`).join('')}</div><small class="field-help">Select at least one event.</small></div>
        ${editing ? '' : ui.field({ label: 'Signing secret (optional)', name: 'secret', type: 'password', help: 'Leave blank to generate a 64-character secret. It is shown once.', autocomplete: 'new-password' })}
        ${editing ? ui.switchField({ label: 'Endpoint enabled', name: 'enabled', checked: !!hook.enabled, help: 'Disabled endpoints stay configured but receive nothing.' }) : ''}
      </div><div class="form-actions"><span class="form-note">HMAC covers the exact JSON body. Verify it before trusting the payload.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">${editing ? 'Save webhook' : 'Create webhook'}</button></div></div></form>`,
    });
    const form = modal.root.querySelector('#webhook-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const selected = ui.selectedValues(form, 'webhookEvents');
      if (!selected.length) { ui.toast('Choose at least one event.', 'critical'); return; }
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        if (editing) {
          await ctx.api(`/control/api/webhooks/${hook.id}`, { method: 'PATCH', body: { name: values.name, url: values.url, events: selected.join(','), enabled: values.enabled } });
          modal.close();
          ui.toast('Webhook updated.', 'good');
        } else {
          const result = await ctx.api('/control/api/webhooks', { method: 'POST', body: { name: values.name, url: values.url, events: selected.join(','), secret: values.secret || '' } });
          modal.close();
          ui.toast('Webhook created.', 'good');
          if (result.secret) secretModal(ctx, 'Webhook created', 'Signing secret', result.secret, result.message || 'Copy the signing secret now. It is not shown again.');
        }
        await load();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not save webhook' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  document.getElementById('webhook-create')?.addEventListener('click', () => openEditor(null));
  document.getElementById('webhook-refresh')?.addEventListener('click', load);
  await load();
}

/* =============================================================== notifications */

export async function renderNotifications(ctx) {
  const canManage = ctx.can('notifications.manage');
  let handles = [];
  let events = [];
  let deliveries = [];
  ctx.setTitle('Notifications', 'Security');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Security', title: 'Notifications',
    description: 'Personal alert handles for this operator account, plus the delivery log for control-plane events.',
    actions: `${canManage ? `<button class="btn btn-primary" type="button" id="notification-create">${ui.icon('plus', 15)} Add handle</button>` : ''}<button class="btn btn-secondary" type="button" id="notification-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    <div class="grid grid-main">
      <div class="stack">
        <section class="card"><header class="card-head"><div><h3>My notification handles</h3><p id="notification-summary">Loading handles…</p></div></header><div class="card-body" id="notification-list">${ui.loadingState('Loading notification handles…', 3)}</div></section>
        <section class="card"><header class="card-head"><div><h3>Delivery log</h3><p>Only your handles are shown</p></div></header><div class="card-body" id="notification-deliveries">${ui.loadingState('Loading deliveries…', 3)}</div></section>
      </div>
      <div class="stack">
        <section class="card"><header class="card-head"><div><h3>Event catalogue</h3><p>What can trigger an alert</p></div></header><div class="card-body"><div class="chip-list">${(handles.length ? events : []).map((event) => `<span class="mono-chip">${esc(event)}</span>`).join('') || '<span class="muted">Loading…</span>'}</div><div class="note-strip" style="margin-top:11px">${ui.icon('info', 13)} <span>Each operator manages their own Slack, Discord, Telegram, webhook or email handles. Handles are independent of global outbound webhooks.</span></div></div></section>
        <section class="card"><header class="card-head"><div><h3>Delivery behavior</h3></div></header><div class="card-body">
          <div class="tile-link"><span>${ui.icon('bell', 15)} Alert types</span><span class="value">security, service, SSL</span></div>
          <div class="tile-link"><span>${ui.icon('clock', 15)} Attempt timeout</span><span class="value">15 seconds</span></div>
          <div class="tile-link"><span>${ui.icon('mail', 15)} Email path</span><span class="value">local SMTP</span></div>
          <div class="tile-link"><span>${ui.icon('webhook', 15)} Webhook payload</span><span class="value">event + data JSON</span></div>
          <div class="notice tone-warning" style="margin-top:11px">${ui.icon('alert', 15)}<div><strong>Handle credentials are sealed</strong><p>Slack/Discord URLs, Telegram bot tokens and webhook URLs are encrypted at rest and never returned to the browser.</p></div></div>
        </div></section>
      </div>
    </div>`;

  const load = async () => {
    try {
      const [handlePayload, deliveryPayload] = await Promise.all([
        ctx.api('/control/api/notifications/handles'),
        ctx.api('/control/api/notifications/deliveries'),
      ]);
      if (!ctx.isCurrent()) return;
      handles = Array.isArray(handlePayload.handles) ? handlePayload.handles : [];
      events = Array.isArray(handlePayload.events) ? handlePayload.events : [];
      deliveries = Array.isArray(deliveryPayload.deliveries) ? deliveryPayload.deliveries : [];
      const enabled = handles.filter((handle) => handle.enabled).length;
      document.getElementById('notification-summary').textContent = `${handles.length} handle${handles.length === 1 ? '' : 's'} · ${enabled} enabled`;
      const list = document.getElementById('notification-list');
      list.innerHTML = handles.length ? `<div class="table-wrap"><table class="data"><thead><tr><th>Handle</th><th>Kind</th><th>Events</th><th>State</th><th></th></tr></thead><tbody>${handles.map((handle) => `<tr><td><span class="cell-main">${esc(handle.name)}</span><span class="cell-sub">created ${esc(fmt.dateOnly(handle.createdAt))}</span></td><td>${ui.badge(ui.humanTitle(handle.kind), handle.kind === 'email' ? 'info' : 'accent', handle.kind === 'email' ? 'mail' : handle.kind === 'telegram' ? 'send' : 'webhook')}</td><td><div class="chip-list">${handle.events.slice(0, 5).map((event) => `<span class="mono-chip">${esc(event)}</span>`).join('')}${handle.events.length > 5 ? `<span class="mono-chip">+${handle.events.length - 5}</span>` : ''}</div></td><td>${ui.statusPill(handle.enabled ? 'active' : 'disabled', handle.enabled ? 'Enabled' : 'Disabled')}</td><td class="actions"><button class="icon-btn" type="button" data-handle-test="${esc(handle.id)}" title="Send test" aria-label="Send test">${ui.icon('send', 14)}</button><button class="icon-btn" type="button" data-handle-edit="${esc(handle.id)}" title="Edit handle" aria-label="Edit handle">${ui.icon('edit', 14)}</button><button class="icon-btn is-danger" type="button" data-handle-delete="${esc(handle.id)}" title="Delete handle" aria-label="Delete handle">${ui.icon('trash', 14)}</button></td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No notification handles', 'Add a Slack, Discord, Telegram, webhook or email handle to receive control-plane alerts.', `<button class="btn btn-secondary btn-sm" type="button" id="notification-create-empty">${ui.icon('plus', 13)} Add handle</button>`, 'bell');
      document.getElementById('notification-create-empty')?.addEventListener('click', openEditor);
      list.querySelectorAll('[data-handle-test]').forEach((button) => button.addEventListener('click', async () => {
        try { await ctx.api(`/control/api/notifications/handles/${button.dataset.handleTest}/test`, { method: 'POST' }); ui.toast('Test notification sent.', 'good'); await load(); }
        catch (error) { ui.toast(errorText(error), 'critical', { title: 'Notification test failed' }); }
      }));
      list.querySelectorAll('[data-handle-edit]').forEach((button) => button.addEventListener('click', () => openEditor(handles.find((handle) => handle.id === button.dataset.handleEdit))));
      list.querySelectorAll('[data-handle-delete]').forEach((button) => button.addEventListener('click', async () => {
        const handle = handles.find((h) => h.id === button.dataset.handleDelete);
        if (!handle) return;
        const ok = await ctx.confirm({ title: 'Delete notification handle', message: `Delete ${handle.name}? Future events will not be delivered through it.`, confirmLabel: 'Delete handle', tone: 'danger' });
        if (!ok) return;
        try { await ctx.api(`/control/api/notifications/handles/${handle.id}`, { method: 'DELETE' }); ui.toast('Handle deleted.', 'good'); await load(); }
        catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not delete handle' }); }
      }));
      const host = document.getElementById('notification-deliveries');
      host.innerHTML = deliveries.length ? `<div class="table-wrap"><table class="data compact"><thead><tr><th>Status</th><th>Handle</th><th>Event</th><th>Detail</th><th>Attempted</th></tr></thead><tbody>${deliveries.slice(0, 100).map((delivery) => `<tr><td>${ui.statusPill(delivery.status)}</td><td>${esc(delivery.name || '—')}</td><td class="mono">${esc(delivery.event)}</td><td class="wrap-anywhere">${esc(ui.truncate(delivery.detail || '—', 70))}</td><td>${esc(fmt.timeAgo(delivery.attemptedAt))}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No deliveries yet', 'Test a handle to see the delivery outcome.', '', 'activity');
      const eventCatalogue = document.getElementById('notification-events');
      if (eventCatalogue) eventCatalogue.innerHTML = events.map((event) => `<span class="mono-chip">${esc(event)}</span>`).join('');
    } catch (error) {
      document.getElementById('notification-list').innerHTML = ui.inlineNotice('critical', 'Could not load notification handles', errorText(error));
    }
  };

  const openEditor = (handle) => {
    const editing = !!handle;
    const modal = ctx.modal({
      title: editing ? 'Edit notification handle' : 'Add notification handle', size: 'medium',
      body: `<form id="handle-form"><div class="form-grid is-single">
        ${ui.field({ label: 'Name', name: 'name', value: handle?.name || '', required: true, placeholder: 'On-call Slack' })}
        ${editing ? `<div class="note-strip">${ui.icon('info', 13)} <span>Destination credentials cannot be changed in place. Delete and create a new handle to change the target.</span></div>` : `<div class="field"><label>Kind</label><select class="input" name="kind" id="handle-kind">${[{ value: 'slack', label: 'Slack incoming webhook' }, { value: 'discord', label: 'Discord webhook' }, { value: 'telegram', label: 'Telegram bot' }, { value: 'webhook', label: 'Generic webhook' }, { value: 'email', label: 'Email' }].map((option) => `<option value="${option.value}">${option.label}</option>`).join('')}</select></div><div id="handle-config"></div>`}
        <div class="field"><label>Events</label><div class="check-grid">${events.map((event) => `<label class="check-field"><input type="checkbox" name="handleEvents" value="${esc(event)}"${handle?.events?.includes(event) || (!handle && ['service.down', 'service.up', 'ssl.failed', 'auth.lockout'].includes(event)) ? ' checked' : ''}><span class="mono">${esc(event)}</span></label>`).join('')}</div><small class="field-help">Select at least one event.</small></div>
        ${editing ? ui.switchField({ label: 'Handle enabled', name: 'enabled', checked: !!handle.enabled }) : ''}
      </div><div class="form-actions"><span class="form-note">Credentials are sealed with AES-GCM before storage.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">${editing ? 'Save handle' : 'Create handle'}</button></div></div></form>`,
    });
    const form = modal.root.querySelector('#handle-form');
    const kindSelect = form.querySelector('#handle-kind');
    const config = form.querySelector('#handle-config');
    const paintConfig = () => {
      if (!config || !kindSelect) return;
      const kind = kindSelect.value;
      config.innerHTML = `<div class="form-grid is-single" style="margin-top:13px">${kind === 'email'
        ? ui.field({ label: 'Email address', name: 'email', type: 'email', required: true, placeholder: 'oncall@example.com' })
        : kind === 'telegram'
          ? `${ui.field({ label: 'Bot token', name: 'botToken', type: 'password', required: true, help: 'From @BotFather. Stored sealed.' })}${ui.field({ label: 'Chat ID', name: 'chatId', required: true, placeholder: '-1001234567890' })}`
          : ui.field({ label: 'Webhook URL', name: 'webhookUrl', type: 'url', required: true, placeholder: kind === 'slack' ? 'https://hooks.slack.com/services/…' : kind === 'discord' ? 'https://discord.com/api/webhooks/…' : 'https://automation.example.com/hook', help: 'Must be an http(s) URL.' })}</div>`;
    };
    kindSelect?.addEventListener('change', paintConfig);
    paintConfig();
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const selected = ui.selectedValues(form, 'handleEvents');
      if (!values.name?.trim()) { ui.toast('Handle name is required.', 'critical'); return; }
      if (!selected.length) { ui.toast('Choose at least one event.', 'critical'); return; }
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        if (editing) {
          await ctx.api(`/control/api/notifications/handles/${handle.id}`, { method: 'PATCH', body: { name: values.name, events: selected.join(','), enabled: values.enabled } });
          modal.close();
          ui.toast('Notification handle updated.', 'good');
        } else {
          await ctx.api('/control/api/notifications/handles', { method: 'POST', body: {
            name: values.name, kind: values.kind, events: selected.join(','),
            webhookUrl: values.webhookUrl || '', email: values.email || '',
            botToken: values.botToken || '', chatId: values.chatId || '',
          } });
          modal.close();
          ui.toast('Notification handle created.', 'good');
        }
        await load();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not save notification handle' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  document.getElementById('notification-create')?.addEventListener('click', () => openEditor(null));
  document.getElementById('notification-refresh')?.addEventListener('click', load);
  await load();
}
