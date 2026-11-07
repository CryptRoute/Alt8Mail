/* Office / OnlyOffice lifecycle and feature flag. */
import * as ui from './ui.js';

const esc = ui.esc;

export async function renderOffice(ctx) {
  const canManage = ctx.can('server.manage');
  ctx.setTitle('Office', 'Infrastructure');
  let payload;
  try {
    payload = await ctx.api('/control/api/office');
  } catch (error) {
    ctx.root.innerHTML = `${ui.pageHeader({ eyebrow: 'Infrastructure', title: 'Office', description: 'OnlyOffice Document Server lifecycle and the webmail Office feature flag.' })}
      ${ui.inlineNotice('critical', 'Could not load Office', error.message || String(error))}`;
    return;
  }
  const settings = payload.settings || {};
  const agent = payload.agent || {};
  const certificates = Array.isArray(payload.certificates) ? payload.certificates : [];
  const agentOnline = !payload.agentError;
  const installed = Boolean(agent.installed);
  const running = agent.running && agent.health === 'ok';
  const certOptions = [{ value: '', label: 'Install container without a gateway' }].concat(certificates.map((cert) => ({
    value: cert.name,
    label: `${cert.name} · ${cert.notAfter ? cert.notAfter.slice(0, 10) : 'no expiry'}${cert.selfSigned ? ' · self-signed' : ''}`,
  })));
  const statusTone = running ? 'good' : settings.enabled ? 'warning' : 'neutral';
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Infrastructure',
    title: 'Office',
    description: 'Keep the OnlyOffice Document Server on a separate Docker host or container, managed by a narrow lifecycle agent. Webmail only exposes Office when this control plane marks it healthy and enabled.',
    actions: `<button class="btn btn-secondary" type="button" id="office-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
    meta: `<span class="summary-pill">${ui.icon('plug', 13)} ${running ? 'Ready' : installed ? 'Installed' : agentOnline ? 'Not installed' : 'Agent offline'}</span>`,
  })}
  ${payload.agentError ? ui.inlineNotice('warning', 'Office agent is not reachable', payload.agentError, `<a class="btn btn-secondary btn-sm" href="#/services">${ui.icon('server', 13)} Open services</a>`) : ''}
  ${ui.inlineNotice('info', 'Separate compute is mandatory', 'OnlyOffice Document Server runs Node.js, PostgreSQL, RabbitMQ and Redis. Do not co-locate it on a 4 GB mail host. The agent listens on loopback and only manages its own compose project.', '')}
  <div class="office-grid">
    <section class="card">
      <header class="card-head"><div><h3>Service status</h3><p>Agent, container and gateway health</p></div>${ui.badge(running ? 'Running' : installed ? 'Stopped' : agentOnline ? 'Not installed' : 'Offline', statusTone, running ? 'check-circle' : 'alert')}</header>
      <div class="card-body">
        ${ui.kv([
          ['Agent', payload.agentError ? 'Unreachable' : 'Reachable'],
          ['Container', esc(agent.containerStatus || settings.containerStatus || 'not installed')],
          ['Version', esc(agent.version || settings.version || '—')],
          ['Image', `<span class="mono">${esc(agent.image || settings.image || payload.defaultImage || '—')}</span>`],
          ['JWT secret', agent.secretPresent ? ui.badge('Present', 'good', 'lock') : ui.badge('Missing', 'warning', 'lock')],
          ['Gateway', agent.gatewayConfigured ? ui.badge('Configured', 'good', 'check-circle') : ui.badge('Not configured', 'neutral', 'link')],
          ['Public URL', esc(settings.publicUrl || agent.publicUrl || '—')],
          ['Last health', esc(settings.lastHealthAt ? new Date(settings.lastHealthAt).toLocaleString() : '—')],
        ], 'is-2')}
        ${settings.lastError || agent.error ? `<p class="form-note office-error">${esc(settings.lastError || agent.error)}</p>` : ''}
        <div class="office-feature">
          ${ui.switchField({
            label: 'Enable Office for webmail',
            name: 'office-enabled',
            checked: Boolean(settings.enabled),
            disabled: !canManage || (!running && !settings.enabled),
            help: canManage
              ? running
                ? 'Webmail will show Office controls only after this flag is enabled.'
                : settings.enabled
                  ? 'Office is enabled, but the container is not healthy. Disable it here or restore the service.'
                  : 'Start a healthy OnlyOffice container before enabling webmail Office.'
              : 'You need server.manage permission to change this flag.',
            extra: 'data-office-enabled',
          })}
        </div>
      </div>
    </section>
    <section class="card">
      <header class="card-head"><div><h3>Install / reconfigure</h3><p>Pinned image, public URL and optional gateway</p></div></header>
      <div class="card-body">
        <div class="office-fields">
          ${ui.field({ label: 'Public Office URL', name: 'office-url', value: settings.publicUrl || '', placeholder: 'https://office.example.com', help: 'The browser and OnlyOffice callback origin. Use a dedicated HTTPS hostname.', required: true })}
          ${ui.field({ label: 'OnlyOffice image', name: 'office-image', value: settings.image || payload.defaultImage || '', placeholder: 'onlyoffice/documentserver:9.4.0.1', help: 'Pin a tested image tag before production.', required: true })}
          ${ui.field({ label: 'TLS certificate for gateway', name: 'office-cert', type: 'select', value: '', options: certOptions, help: 'Optional. The certificate must already exist in System Control TLS. The agent writes the nginx gateway and reloads nginx.' })}
        </div>
        <div class="form-actions">
          <span class="form-note">Pull and first start can take several minutes. The agent waits up to three minutes for the health endpoint.</span>
          <button class="btn btn-primary" type="button" id="office-install" ${canManage ? '' : 'disabled'}>${ui.icon('download', 15)} Install / reconfigure</button>
        </div>
      </div>
    </section>
  </div>
  <section class="card" style="margin-top:16px">
    <header class="card-head"><div><h3>Lifecycle</h3><p>Start, stop, restart, update and rotate the JWT secret</p></div></header>
    <div class="card-body">
      <div class="office-actions">
        <button class="btn btn-secondary" type="button" data-office-action="start" ${canManage && installed ? '' : 'disabled'}>${ui.icon('play', 14)} Start</button>
        <button class="btn btn-secondary" type="button" data-office-action="stop" ${canManage && installed ? '' : 'disabled'}>${ui.icon('pause', 14)} Stop</button>
        <button class="btn btn-secondary" type="button" data-office-action="restart" ${canManage && installed ? '' : 'disabled'}>${ui.icon('refresh', 14)} Restart</button>
        <button class="btn btn-secondary" type="button" data-office-action="update" ${canManage && installed ? '' : 'disabled'}>${ui.icon('download', 14)} Update image</button>
        <button class="btn btn-secondary" type="button" data-office-action="rotate-jwt" ${canManage && installed ? '' : 'disabled'}>${ui.icon('key', 14)} Rotate JWT secret</button>
        <button class="btn btn-secondary" type="button" data-office-action="gateway" ${canManage && installed ? '' : 'disabled'}>${ui.icon('link', 14)} Reconfigure gateway</button>
        <button class="btn btn-secondary" type="button" id="office-logs">${ui.icon('terminal', 14)} View logs</button>
      </div>
      <p class="form-note" style="margin-top:12px">The JWT secret is stored at <span class="mono">/etc/a8m/office-jwt-secret</span> with <span class="mono">0640 root:a8m</span>. Rotating it invalidates open editor sessions.</p>
    </div>
  </section>
  <section class="card" id="office-logs-card" style="margin-top:16px" hidden>
    <header class="card-head"><div><h3>Container logs</h3><p>Last 120 lines, JWT lines redacted</p></div><button class="btn btn-secondary btn-sm" type="button" id="office-logs-close">${ui.icon('x', 13)} Close</button></header>
    <div class="card-body"><pre class="code-block office-logs" id="office-logs-output">Loading…</pre></div>
  </section>`;

  const reload = () => renderOffice(ctx);
  document.getElementById('office-refresh')?.addEventListener('click', reload);
  document.querySelector('[data-office-enabled]')?.addEventListener('change', async (event) => {
    const input = event.currentTarget;
    const next = input.checked;
    input.disabled = true;
    try {
      await ctx.api('/control/api/office', { method: 'PATCH', body: { enabled: next } });
      ui.toast(next ? 'Office enabled for webmail.' : 'Office disabled for webmail.', next ? 'good' : 'warning');
      await reload();
    } catch (error) {
      input.checked = !next;
      input.disabled = false;
      ui.toast(error.message || 'Could not change the Office feature flag.', 'critical');
    }
  });

  function selectedCert() {
    const name = document.querySelector('[name="office-cert"]')?.value || '';
    return certificates.find((cert) => cert.name === name) || null;
  }
  async function install() {
    const button = document.getElementById('office-install');
    const publicUrl = document.querySelector('[name="office-url"]')?.value.trim() || '';
    const image = document.querySelector('[name="office-image"]')?.value.trim() || '';
    const cert = selectedCert();
    if (!publicUrl) { ui.toast('Enter the public Office URL.', 'warning'); return; }
    const label = button?.innerHTML;
    if (button) { button.disabled = true; button.innerHTML = 'Installing…'; }
    try {
      await ctx.api('/control/api/office/install', { method: 'POST', body: {
        publicUrl, image, tlsCert: cert?.certPath || '', tlsKey: cert?.keyPath || '',
      } });
      ui.toast('Office installed or reconfigured.', 'good');
      await reload();
    } catch (error) {
      ui.toast(error.message || 'Office installation failed.', 'critical', { title: 'Office install' });
      if (button) { button.disabled = false; button.innerHTML = label || 'Install / reconfigure'; }
    }
  }
  document.getElementById('office-install')?.addEventListener('click', install);

  document.querySelectorAll('[data-office-action]').forEach((button) => {
    button.addEventListener('click', async () => {
      const action = button.getAttribute('data-office-action');
      if (action === 'rotate-jwt' && !(await ctx.confirm({ title: 'Rotate the Office JWT secret?', message: 'Open editor sessions will be invalidated. Webmail will read the new secret from the shared file.', confirmLabel: 'Rotate secret', tone: 'danger' }))) return;
      let body = {};
      if (action === 'update') {
        body = { image: document.querySelector('[name="office-image"]')?.value.trim() || '' };
      } else if (action === 'gateway') {
        const publicUrl = document.querySelector('[name="office-url"]')?.value.trim() || '';
        if (!publicUrl) { ui.toast('Enter the public Office URL.', 'warning'); return; }
        const cert = selectedCert();
        if (!cert && !agent.gatewayConfigured) { ui.toast('Choose a TLS certificate to configure the gateway.', 'warning'); return; }
        body = { publicUrl, tlsCert: cert?.certPath || '', tlsKey: cert?.keyPath || '' };
      }
      button.disabled = true;
      try {
        await ctx.api(`/control/api/office/${action}`, { method: 'POST', body });
        ui.toast(`Office ${action.replace('-', ' ')} completed.`, 'good');
        await reload();
      } catch (error) {
        ui.toast(error.message || `Office ${action} failed.`, 'critical', { title: 'Office action' });
        button.disabled = false;
      }
    });
  });

  document.getElementById('office-logs')?.addEventListener('click', async () => {
    const card = document.getElementById('office-logs-card');
    const output = document.getElementById('office-logs-output');
    if (!card || !output) return;
    card.hidden = false;
    output.textContent = 'Loading…';
    try {
      const data = await ctx.api('/control/api/office/logs?lines=120');
      output.textContent = Array.isArray(data.lines) && data.lines.length ? data.lines.join('\n') : 'No log lines returned.';
    } catch (error) {
      output.textContent = error.message || 'Could not load Office logs.';
    }
  });
  document.getElementById('office-logs-close')?.addEventListener('click', () => {
    const card = document.getElementById('office-logs-card');
    if (card) card.hidden = true;
  });
}
