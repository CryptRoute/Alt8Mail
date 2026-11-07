/* Office / OnlyOffice lifecycle and feature flag. */
import * as ui from './ui.js?v=0.11.40-control';

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
  const host = payload.host || {};
  const certificates = Array.isArray(payload.certificates) ? payload.certificates : [];
  const agentOnline = !payload.agentError;
  const installed = Boolean(agent.installed);
  const running = agent.running && agent.health === 'ok';
  const certOptions = [{ value: 'auto', label: "Automatic Let's Encrypt (recommended)" }]
    .concat(settings.gatewayStatus === 'configured' ? [{ value: 'keep', label: 'Keep the current gateway certificate' }] : [])
    .concat(certificates.map((cert) => ({
      value: `cert:${cert.name}`,
      label: `${cert.name} · ${cert.notAfter ? cert.notAfter.slice(0, 10) : 'no expiry'}${cert.selfSigned ? ' · self-signed' : ''}`,
    })))
    .concat([{ value: 'none', label: 'Install without a public gateway' }]);
  const defaultTls = settings.gatewayStatus === 'configured' ? 'keep' : 'auto';
  let preflightReady = false;
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
          ['Host memory', settings.deploymentMode === 'remote' ? 'See remote preflight' : (host.memoryTotalBytes ? `${ui.bytes(host.memoryTotalBytes)} total · ${Number(host.memoryPercent || 0).toFixed(0)}% used` : 'Unavailable')],
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
          ${ui.field({ label: 'Deployment', name: 'office-deployment', type: 'select', value: settings.deploymentMode === 'remote' ? 'remote' : 'local', options: [{ value: 'local', label: 'Local Docker host' }, { value: 'remote', label: 'Remote server over SSH' }], help: 'Remote mode provisions and manages the Office host through an SSH connection from this Control Plane.' })}
          ${ui.field({ label: 'Public Office URL', name: 'office-url', value: settings.publicUrl || '', placeholder: 'https://office.example.com', help: 'The browser and OnlyOffice callback origin. Use a dedicated HTTPS hostname.', required: true })}
          ${ui.field({ label: 'OnlyOffice image', name: 'office-image', value: settings.image || payload.defaultImage || '', placeholder: 'onlyoffice/documentserver:9.4.0.1', help: 'Pin a tested image tag before production.', required: true })}
          ${ui.field({ label: 'TLS for gateway', name: 'office-cert', type: 'select', value: defaultTls, options: certOptions.map((option) => ({ ...option, disabled: settings.deploymentMode === 'remote' && option.value.startsWith('cert:') })), help: 'Automatic mode runs certbot through the Office agent using the HTTP-01 webroot challenge. DNS must already point here.' })}
          ${ui.field({ label: "Let's Encrypt account email", name: 'office-tls-email', type: 'email', value: payload.tlsEmail || '', placeholder: 'hostmaster@example.com', help: 'Required for automatic issuance and renewal notices.' })}
        </div>
        <div id="officeRemoteFields" hidden>
          <div class="office-fields">
            ${ui.field({ label: 'Remote host', name: 'office-remote-host', value: settings.remoteHost || '', placeholder: '203.0.113.10 or office-host.example.com', help: 'SSH endpoint for the remote Office host.' })}
            ${ui.field({ label: 'SSH port', name: 'office-remote-port', type: 'number', value: String(settings.remotePort || 22), help: 'Usually 22.' })}
            ${ui.field({ label: 'SSH user', name: 'office-remote-user', value: settings.remoteUser || 'root', placeholder: 'root', help: 'Root or an account with passwordless sudo.' })}
            ${ui.field({ label: 'SSH authentication', name: 'office-remote-auth', type: 'select', value: settings.remoteAuth || 'key', options: [{ value: 'key', label: 'Private key' }, { value: 'password', label: 'Password' }] })}
          </div>
          <label class="field"><span>SSH private key or password</span><textarea class="input" name="office-remote-secret" rows="4" placeholder="${settings.remoteHasSecret ? 'A credential is saved; leave blank to keep it' : 'Paste an OpenSSH private key or the SSH password'}" autocomplete="new-password"></textarea><small class="muted">Stored encrypted with AES-256-GCM and never returned by the API.</small></label>
          <div class="office-fields">
            ${ui.field({ label: 'Key passphrase', name: 'office-remote-passphrase', type: 'password', value: '', placeholder: settings.remoteHasSecret ? 'Leave blank to keep the saved passphrase' : 'Optional for encrypted private keys' })}
            ${ui.field({ label: 'SSH host key fingerprint', name: 'office-remote-hostkey', value: settings.remoteHostKey || '', placeholder: 'SHA256:…', help: 'Required. Run preflight once to discover it, then trust the fingerprint.' })}
            ${ui.field({ label: 'Remote public IPv4/IPv6', name: 'office-remote-publicip', value: settings.remotePublicIp || '', placeholder: 'Optional when DNS points at an address on the remote interface', help: 'Set this for NAT/cloud hosts whose public address is not assigned to the interface.' })}
          </div>
        </div>
        <div class="notice tone-info" id="officeDeploymentNote" style="margin-top:12px">${ui.icon('server', 16)}<div><strong>Deployment target</strong><p>Local mode uses the loopback agent on this server. Remote mode opens SSH, runs the same agent on the remote host, and keeps the agent API on remote loopback.</p></div></div>
        <div class="form-actions">
          <span class="form-note">Pull and first start can take several minutes. DNS and memory are checked before the agent is called. The agent waits up to three minutes for the health endpoint.</span>
          <button class="btn btn-primary" type="button" id="office-install" ${canManage ? '' : 'disabled'}>${ui.icon('download', 15)} Install / reconfigure</button>
        </div>
      </div>
    </section>
  </div>
  <section class="card" style="margin-top:16px">
    <header class="card-head"><div><h3>Install preflight</h3><p>DNS A/AAAA, host memory and TLS readiness</p></div><button class="btn btn-secondary btn-sm" type="button" id="office-preflight-run">${ui.icon('refresh', 13)} Run checks</button></header>
    <div class="card-body" id="office-preflight-body"><div class="empty-mini">Enter the public Office URL, then run the preflight.</div></div>
  </section>
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
  document.getElementById('office-preflight-run')?.addEventListener('click', () => runPreflight());
  const urlInput = document.querySelector('[name="office-url"]');
  let preflightTimer = null;
  urlInput?.addEventListener('input', () => {
    preflightReady = false;
    const installButton = document.getElementById('office-install');
    if (installButton) installButton.disabled = true;
    clearTimeout(preflightTimer);
    preflightTimer = setTimeout(() => { runPreflight({ quiet: true }); }, 650);
  });
  const installButton = document.getElementById('office-install');
  if (installButton) installButton.disabled = true;
  document.querySelector('[name="office-remote-host"]')?.addEventListener('input', () => {
    const hostKeyInput = document.querySelector('[name="office-remote-hostkey"]');
    if (hostKeyInput) hostKeyInput.value = '';
    setPreflightReady(false);
  });
  document.querySelector('[name="office-remote-hostkey"]')?.addEventListener('input', () => {
    setPreflightReady(false);
  });
  const deploymentSelect = document.querySelector('[name="office-deployment"]');
  deploymentSelect?.addEventListener('change', () => {
    syncDeploymentFields();
    setPreflightReady(false);
    const preflightBody = document.getElementById('office-preflight-body');
    if (preflightBody) preflightBody.innerHTML = '<div class="empty-mini">Run the preflight for this deployment target.</div>';
  });
  syncDeploymentFields();
  if (settings.publicUrl) runPreflight({ quiet: true });
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
    const name = (document.querySelector('[name="office-cert"]')?.value || '').replace(/^cert:/, '');
    return certificates.find((cert) => cert.name === name) || null;
  }
  function deploymentMode() {
    return document.querySelector('[name="office-deployment"]')?.value || 'local';
  }
  function remoteTargetValues() {
    const hostKey = document.querySelector('[name="office-remote-hostkey"]')?.value.trim() || '';
    return {
      host: document.querySelector('[name="office-remote-host"]')?.value.trim() || '',
      port: Number(document.querySelector('[name="office-remote-port"]')?.value || 22),
      user: document.querySelector('[name="office-remote-user"]')?.value.trim() || 'root',
      auth: document.querySelector('[name="office-remote-auth"]')?.value || 'key',
      secret: document.querySelector('[name="office-remote-secret"]')?.value || '',
      passphrase: document.querySelector('[name="office-remote-passphrase"]')?.value || '',
      hostKey,
      clearHostKey: !hostKey,
      publicIp: document.querySelector('[name="office-remote-publicip"]')?.value.trim() || '',
    };
  }
  function syncDeploymentFields() {
    const remote = deploymentMode() === 'remote';
    const fields = document.getElementById('officeRemoteFields');
    if (fields) fields.hidden = !remote;
    const certSelect = document.querySelector('[name="office-cert"]');
    if (certSelect) {
      Array.from(certSelect.options).forEach((option) => {
        if (option.value.startsWith('cert:')) option.disabled = remote;
      });
      const targetChanged = remote !== (settings.deploymentMode === 'remote');
      if (remote && certSelect.value.startsWith('cert:')) certSelect.value = 'auto';
      if (targetChanged && certSelect.value === 'keep') certSelect.value = 'auto';
    }
  }
  function preflightItem(title, ok, detail) {
    return `<div class="timeline-item"><span class="timeline-dot" style="background:${ok ? 'var(--status-good)' : 'var(--status-critical)'}"></span><div class="timeline-copy"><strong>${esc(title)}</strong><small>${esc(detail)}</small></div><span class="badge ${ok ? 'tone-good' : 'tone-critical'}">${ok ? 'Pass' : 'Blocked'}</span></div>`;
  }
  function setPreflightReady(ready) {
    preflightReady = Boolean(ready);
    const installButton = document.getElementById('office-install');
    if (installButton) installButton.disabled = !canManage || !preflightReady;
  }
  function renderPreflight(data) {
    const body = document.getElementById('office-preflight-body');
    if (!body) return;
    const expected = Array.isArray(data.expected) && data.expected.length ? data.expected.join(', ') : 'no known local address';
    const resolved = Array.isArray(data.resolved) && data.resolved.length ? data.resolved.join(', ') : 'none';
    const memory = data.memoryTotalBytes ? `${ui.bytes(data.memoryTotalBytes)} total · ${Number(data.memoryPercent || 0).toFixed(0)}% used` : 'unavailable';
    body.innerHTML = `<div class="timeline">${preflightItem('DNS record', data.dnsOk, `${data.dnsDetail || ''} · resolved ${resolved} · expected ${expected}`)}${preflightItem('Host memory', data.memoryOk, `${memory} · ${data.memoryDetail || ''}`)}</div><p class="form-note" style="margin-top:12px">${data.ready ? 'Preflight passed. Installation can proceed.' : 'Fix the blocked checks before installing. DNS changes can take a few minutes to propagate.'}</p>`;
    setPreflightReady(data.ready);
  }
  function renderRemotePreflight(data) {
    const body = document.getElementById('office-preflight-body');
    if (!body) return;
    if (data.hostKeyTrusted && data.hostKeyFingerprint) {
      const input = document.querySelector('[name="office-remote-hostkey"]');
      if (input) input.value = data.hostKeyFingerprint;
    }
    const expected = Array.isArray(data.expected) && data.expected.length ? data.expected.join(', ') : 'no remote address discovered';
    const resolved = Array.isArray(data.resolved) && data.resolved.length ? data.resolved.join(', ') : 'none';
    const memory = data.memoryTotalBytes ? `${ui.bytes(data.memoryTotalBytes)} total · ${Number(data.memoryPercent || 0).toFixed(0)}% used` : 'unavailable';
    const rows = [
      preflightItem('SSH host key', data.hostKeyTrusted, data.hostKeyTrusted ? `Pinned ${data.hostKeyFingerprint}` : `Review the fingerprint ${data.hostKeyFingerprint || '—'} before trusting it`),
      preflightItem('SSH privilege', data.sudo, data.sudo ? 'Root or passwordless sudo is available' : 'The SSH account needs root or passwordless sudo'),
      preflightItem('Host memory', data.memoryOk, `${memory}`),
      preflightItem('Docker Compose', data.docker && data.dockerCompose, data.docker ? (data.dockerCompose ? 'Docker Engine and Compose are present' : 'Docker is present but the Compose plugin is missing') : 'Docker Engine is missing'),
      preflightItem('nginx and certbot', data.nginx && data.certbot, `nginx ${data.nginx ? 'present' : 'missing'} · certbot ${data.certbot ? 'present' : 'missing'}`),
      preflightItem('systemd', data.systemd, data.systemd ? 'systemd is available' : 'systemd is missing'),
      preflightItem('Disk space', data.diskOk, data.diskFreeBytes ? `${ui.bytes(data.diskFreeBytes)} free on /` : 'Could not read free disk space'),
      preflightItem('DNS record', data.dnsOk, `${data.dnsDetail || ''} · resolved ${resolved} · expected ${expected}`),
    ];
    const trust = data.hostKeyFingerprint && !data.hostKeyTrusted
      ? `<button class="btn btn-secondary btn-sm" type="button" id="office-trust-hostkey" data-fingerprint="${esc(data.hostKeyFingerprint)}">${ui.icon('shield', 13)} Trust fingerprint</button>`
      : '';
    body.innerHTML = `<div class="timeline">${rows.join('')}</div>${data.blockers && data.blockers.length ? `<div class="notice tone-warning" style="margin-top:12px">${ui.icon('alert', 16)}<div><strong>Blockers</strong><p>${esc(data.blockers.join(' · '))}</p></div></div>` : ''}<div style="display:flex;align-items:center;gap:8px;margin-top:12px;flex-wrap:wrap"><span class="form-note">${data.ready ? 'Remote preflight passed. Installation can proceed.' : (data.detail || 'Fix the blocked checks before installing.')}</span>${trust}</div>`;
    document.getElementById('office-trust-hostkey')?.addEventListener('click', () => {
      const input = document.querySelector('[name="office-remote-hostkey"]');
      if (input) input.value = data.hostKeyFingerprint;
      runPreflight();
    });
    setPreflightReady(data.ready);
  }
  async function runPreflight({ quiet = false } = {}) {
    const publicUrl = document.querySelector('[name="office-url"]')?.value.trim() || '';
    const body = document.getElementById('office-preflight-body');
    if (!publicUrl) {
      setPreflightReady(false);
      if (body) body.innerHTML = '<div class="empty-mini">Enter the public Office URL, then run the preflight.</div>';
      if (!quiet) ui.toast('Enter the public Office URL first.', 'warning');
      return null;
    }
    const button = document.getElementById('office-preflight-run');
    if (button) button.disabled = true;
    try {
      let data;
      if (deploymentMode() === 'remote') {
        if (!canManage) {
          setPreflightReady(false);
          if (body) body.innerHTML = '<div class="empty-mini">You need server.manage permission to test remote SSH and install Office.</div>';
          return null;
        }
        const target = remoteTargetValues();
        if (!target.host) { ui.toast('Enter the remote host first.', 'warning'); setPreflightReady(false); return null; }
        data = await ctx.api('/control/api/office/remote/preflight', { method: 'POST', body: { publicUrl, ...target } });
        renderRemotePreflight(data);
      } else {
        data = await ctx.api('/control/api/office/preflight?publicUrl=' + encodeURIComponent(publicUrl));
        renderPreflight(data);
      }
      return data;
    } catch (error) {
      setPreflightReady(false);
      if (body) body.innerHTML = `<div class="notice tone-critical">${ui.icon('alert-circle', 16)}<div><strong>Preflight failed</strong><p>${esc(error.message || String(error))}</p></div></div>`;
      if (!quiet) ui.toast(error.message || 'Preflight failed.', 'critical');
      return null;
    } finally {
      if (button) button.disabled = false;
    }
  }
  async function install() {
    const button = document.getElementById('office-install');
    const publicUrl = document.querySelector('[name="office-url"]')?.value.trim() || '';
    const image = document.querySelector('[name="office-image"]')?.value.trim() || '';
    const mode = document.querySelector('[name="office-cert"]')?.value || 'auto';
    const tlsEmail = document.querySelector('[name="office-tls-email"]')?.value.trim() || '';
    const cert = selectedCert();
    const deployment = deploymentMode();
    if (!publicUrl) { ui.toast('Enter the public Office URL.', 'warning'); return; }
    if (mode === 'auto' && !tlsEmail) { ui.toast("Enter the Let's Encrypt account email for automatic TLS.", 'warning'); return; }
    if (mode.startsWith('cert:') && !cert) { ui.toast('Choose a valid TLS certificate.', 'warning'); return; }
    if (deployment === 'remote' && mode.startsWith('cert:')) { ui.toast('Existing certificates are local to this server. Use automatic Let’s Encrypt for a remote host.', 'warning'); return; }
    const preflight = await runPreflight({ quiet: true });
    if (!preflight || !preflight.ready) {
      ui.toast('Preflight must pass before Office can be installed.', 'critical');
      return;
    }
    const body = {
      publicUrl, image, tlsMode: mode.startsWith('cert:') ? 'existing' : mode,
      tlsEmail, tlsCert: cert?.certPath || '', tlsKey: cert?.keyPath || '',
      deploymentMode: deployment,
    };
    if (deployment === 'remote') body.remote = remoteTargetValues();
    const label = button?.innerHTML;
    if (button) { button.disabled = true; button.innerHTML = 'Installing…'; }
    try {
      await ctx.api('/control/api/office/install', { method: 'POST', body });
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
        const mode = document.querySelector('[name="office-cert"]')?.value || 'auto';
        const tlsEmail = document.querySelector('[name="office-tls-email"]')?.value.trim() || '';
        const cert = selectedCert();
        if (mode === 'auto' && !tlsEmail) { ui.toast("Enter the Let's Encrypt account email for automatic TLS.", 'warning'); return; }
        if (mode.startsWith('cert:') && !cert) { ui.toast('Choose a valid TLS certificate.', 'warning'); return; }
        if (mode === 'none') { ui.toast('Choose automatic or an existing certificate to configure a gateway.', 'warning'); return; }
        const preflight = await runPreflight({ quiet: true });
        if (!preflight || !preflight.dnsOk) { ui.toast('DNS must point at this server before the gateway can be reconfigured.', 'critical'); return; }
        body = {
          publicUrl, tlsMode: mode.startsWith('cert:') ? 'existing' : mode, tlsEmail,
          tlsCert: cert?.certPath || '', tlsKey: cert?.keyPath || '',
        };
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
