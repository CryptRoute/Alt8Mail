/* Services, listeners, Postfix, logs, backups, cron, server, firewall, database. */
import * as ui from './ui.js';

const esc = ui.esc;
const fmt = {
  dateTime: ui.dateTime, dateOnly: ui.dateOnly, timeAgo: ui.timeAgo,
  bytes: ui.bytes, num: ui.num, pct: ui.pct, duration: ui.duration,
};

function errorText(error) { return error?.message || String(error || 'Unknown error'); }

function id(prefix = 'id') {
  return `${prefix}-${(crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random()}`).toString().replace(/[^a-zA-Z0-9-]/g, '').slice(0, 18)}`;
}

function statusOf(service) {
  if (service.active) return 'active';
  if (service.loadState === 'not-found') return 'disabled';
  return 'failed';
}

function statusLabel(service) {
  if (service.active) return 'Active';
  if (service.loadState === 'not-found') return 'Not installed';
  if (service.activeState === 'failed') return 'Failed';
  return ui.humanTitle(service.activeState || 'inactive');
}

/* =============================================================== services */

export async function renderServices(ctx) {
  const canManage = ctx.can('services.manage');
  let services = [];
  let checkedAt = '';
  ctx.setTitle('Services', 'Infrastructure');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Infrastructure', title: 'Services',
    description: 'Live systemd state for the mail stack, with guarded start, stop, restart, reload and boot-policy actions.',
    actions: `<button class="btn btn-secondary" type="button" id="services-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    <div id="services-summary">${ui.loadingState('Sampling systemd…', 3)}</div>
    <div class="card mt-4"><header class="card-head"><div><h3>Monitored units</h3><p id="services-checked">Checking systemd…</p></div></header><div class="card-body" id="services-list">${ui.loadingState('Sampling systemd…', 6)}</div></div>
    <div class="grid grid-main-even mt-4">
      <div class="card"><div class="card-body"><h3 style="font-size:12px">What the panel controls</h3><p class="form-note">Only core mail-stack units are manageable. The control service itself cannot be stopped or disabled from the panel. Every action is audited with the resulting unit state.</p></div></div>
      <div class="card"><div class="card-body"><h3 style="font-size:12px">After a change</h3><p class="form-note">The service monitor samples every 15 seconds and raises <span class="mono">service.down</span> / <span class="mono">service.up</span> notifications. Postfix configuration changes still require <span class="mono">postfix check</span>, which the config editor runs before reloading.</p></div></div>
    </div>`;

  const load = async (silent = false) => {
    const list = document.getElementById('services-list');
    const summary = document.getElementById('services-summary');
    if (!list) return;
    if (!silent) list.innerHTML = ui.loadingState('Sampling systemd…', 6);
    try {
      const payload = await ctx.api('/control/api/services?refresh=1');
      if (!ctx.isCurrent()) return;
      services = Array.isArray(payload.services) ? payload.services : [];
      checkedAt = payload.checkedAt;
      const active = services.filter((service) => service.active).length;
      const down = services.filter((service) => !service.active && service.loadState !== 'not-found');
      summary.innerHTML = `<div class="stat-grid">
        ${ui.statTile({ label: 'Active', value: `${active}/${services.length}`, detail: 'Monitored units', iconName: 'check-circle', tone: active === services.length ? 'good' : 'warning' })}
        ${ui.statTile({ label: 'Attention', value: ui.num(down.length), detail: down.length ? 'Inactive or failed units' : 'All installed units healthy', iconName: 'alert-circle', tone: down.length ? 'critical' : 'good' })}
        ${ui.statTile({ label: 'Boot policy', value: ui.num(services.filter((s) => s.enabledState === 'enabled').length), detail: 'Enabled at boot', iconName: 'shield-check' })}
        ${ui.statTile({ label: 'Checked', value: fmt.timeAgo(checkedAt), detail: 'Live systemctl show', iconName: 'refresh' })}
      </div>`;
      document.getElementById('services-checked').textContent = `${services.length} units · refreshed ${fmt.timeAgo(checkedAt)}`;
      list.innerHTML = `<div class="table-wrap is-bare"><table class="data"><thead><tr><th>Service</th><th>State</th><th>Boot</th><th>PID</th><th>Unit</th><th></th></tr></thead><tbody>${services.map((service) => {
        const tone = service.active ? 'good' : service.loadState === 'not-found' ? 'neutral' : 'critical';
        const canAct = canManage && service.unit !== 'alt8mail-control.service';
        return `<tr><td><span class="cell-main">${esc(service.name)}</span><span class="cell-sub">${esc(service.description)}</span></td><td>${ui.statusPill(statusOf(service), statusLabel(service))}<span class="cell-sub">${esc(service.subState || service.activeState || '')}</span></td><td>${ui.badge(ui.humanTitle(service.enabledState || 'unknown'), service.enabledState === 'enabled' ? 'good' : service.enabledState === 'disabled' ? 'neutral' : 'warning')}</td><td class="mono">${service.mainPid || '—'}</td><td class="mono">${esc(service.unit)}</td><td class="actions">${canAct ? `<div class="row-actions">${service.active ? `<button class="btn btn-ghost btn-sm" type="button" data-service-action="restart" data-unit="${esc(service.unit)}">${ui.icon('refresh', 12)} Restart</button><button class="btn btn-ghost btn-sm" type="button" data-service-action="stop" data-unit="${esc(service.unit)}">${ui.icon('pause', 12)} Stop</button>` : `<button class="btn btn-ghost btn-sm" type="button" data-service-action="start" data-unit="${esc(service.unit)}">${ui.icon('play', 12)} Start</button>`}<button class="btn btn-ghost btn-sm" type="button" data-service-action="${service.enabledState === 'enabled' ? 'disable' : 'enable'}" data-unit="${esc(service.unit)}">${ui.icon('power', 12)} ${service.enabledState === 'enabled' ? 'Disable' : 'Enable'}</button></div>` : ''}</td></tr>`;
      }).join('')}</tbody></table></div>`;
      bindActions();
    } catch (error) {
      list.innerHTML = ui.inlineNotice('critical', 'Could not load services', errorText(error));
      summary.innerHTML = '';
    }
  };

  const bindActions = () => {
    document.querySelectorAll('[data-service-action]').forEach((button) => button.addEventListener('click', async () => {
      const unit = button.dataset.unit;
      const action = button.dataset.serviceAction;
      if (['stop', 'disable'].includes(action)) {
        const ok = await ctx.confirm({
          title: `${ui.humanTitle(action)} ${unit}`,
          message: action === 'stop' ? `Stop ${unit}? Mail delivery or client access may be interrupted.` : `Disable ${unit} at boot? It will not start automatically after a reboot.`,
          confirmLabel: ui.humanTitle(action), tone: 'danger',
        });
        if (!ok) return;
      }
      button.classList.add('is-loading'); button.disabled = true;
      try {
        const result = await ctx.api(`/control/api/services/${encodeURIComponent(unit)}/${action}`, { method: 'POST' });
        ui.toast(`${unit}: ${action} completed.`, 'good');
        if (result.output) ui.toast(ui.truncate(result.output, 120), 'info');
        await load();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: `${action} failed` });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    }));
  };

  document.getElementById('services-refresh')?.addEventListener('click', () => load());
  let timer = null;
  ctx.listen(window, 'control:event', (event) => {
    if (event.detail?.event !== 'services.updated') return;
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => { timer = null; if (ctx.isCurrent()) load(true); }, 600);
  });
  ctx.onCleanup(() => { if (timer) clearTimeout(timer); });
  await load();
}

/* =============================================================== listeners */

export async function renderPorts(ctx) {
  const canManage = ctx.can('services.manage');
  let config;
  let listeners = [];
  let stack = [];
  ctx.setTitle('Mail listeners', 'Infrastructure');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Infrastructure', title: 'Mail listeners',
    description: 'Manage SMTP, submission, SMTPS, IMAP, IMAPS, POP3 and POP3S listener ports with a previewed Postfix and Dovecot diff.',
    actions: `<button class="btn btn-secondary" type="button" id="ports-refresh">${ui.icon('refresh', 15)} Refresh</button>${canManage ? `<button class="btn btn-secondary" type="button" id="ports-preview">${ui.icon('eye', 14)} Preview diff</button><button class="btn btn-primary" type="button" id="ports-save">${ui.icon('check', 14)} Save listeners</button>` : ''}`,
  })}
    <div class="grid grid-main">
      <div class="stack">
        <section class="card"><header class="card-head"><div><h3>Listener configuration</h3><p>All seven listeners are required. Ports must be unique.</p></div></header><div class="card-body" id="ports-body">${ui.loadingState('Reading listener configuration…', 4)}</div></section>
        <section class="card"><header class="card-head"><div><h3>Currently bound sockets</h3><p>Live view from <span class="mono">ss -ltnp</span></p></div></header><div class="card-body" id="stack-ports">${ui.loadingState('Reading listening sockets…', 3)}</div></section>
      </div>
      <div class="stack">
        <section class="card"><header class="card-head"><div><h3>Managed files</h3><p>Changes are written and validated together</p></div></header><div class="card-body" id="ports-paths">${ui.loadingState('Loading paths…', 2)}</div></section>
        <section class="card"><header class="card-head"><div><h3>Port safety</h3></div></header><div class="card-body">
          <div class="notice tone-warning">${ui.icon('alert', 16)}<div><strong>Changing public ports can disconnect clients</strong><p>Publish the new port in firewall rules before saving, then tell users to update their mail client settings.</p></div></div>
          <div class="timeline" style="margin-top:12px">
            <div class="timeline-item"><span class="timeline-dot"></span><strong>Validate</strong><span>Postfix and Dovecot parse the new configuration.</span></div>
            <div class="timeline-item"><span class="timeline-dot"></span><strong>Apply files</strong><span>master.cf and a Dovecot drop-in are atomically replaced.</span></div>
            <div class="timeline-item"><span class="timeline-dot"></span><strong>Reload</strong><span>Both daemons reload; a failure is reported immediately.</span></div>
          </div>
        </div></section>
      </div>
    </div>`;

  const load = async () => {
    try {
      const [portsPayload, stackPayload] = await Promise.all([
        ctx.api('/control/api/services/ports'),
        ctx.api('/control/api/services/stack-ports'),
      ]);
      if (!ctx.isCurrent()) return;
      config = portsPayload;
      listeners = Array.isArray(portsPayload.listeners) ? portsPayload.listeners.map((item) => ({ ...item })) : [];
      stack = Array.isArray(stackPayload.listeners) ? stackPayload.listeners : [];
      paint();
    } catch (error) {
      document.getElementById('ports-body').innerHTML = ui.inlineNotice('critical', 'Could not read listeners', errorText(error));
    }
  };

  const paint = () => {
    document.getElementById('ports-body').innerHTML = `<div class="table-wrap"><table class="data"><thead><tr><th>Protocol</th><th>Port</th><th>TLS mode</th><th>Require SASL</th></tr></thead><tbody>${listeners.map((listener, index) => `<tr><td><span class="cell-main">${esc(listener.protocol)}</span><span class="cell-sub">${protocolPurpose(listener.protocol)}</span></td><td><input class="input mono" type="number" min="1" max="65535" style="width:96px;height:30px" value="${listener.port}" data-port-index="${index}" ${canManage ? '' : 'disabled'} aria-label="${esc(listener.protocol)} port"></td><td><select class="input" style="width:130px;height:30px" data-tls-index="${index}" ${canManage ? '' : 'disabled'} aria-label="${esc(listener.protocol)} TLS mode">${tlsModesFor(listener.protocol, listener.tls).map((mode) => `<option value="${mode}"${listener.tls === mode ? ' selected' : ''}>${mode === 'starttls' ? 'STARTTLS' : mode === 'implicit' ? 'Implicit TLS' : 'No TLS'}</option>`).join('')}</select></td><td><label class="check-field" style="display:inline-flex"><input type="checkbox" data-sasl-index="${index}"${listener.saslRequired ? ' checked' : ''} ${canManage ? '' : 'disabled'}> <span>${listener.saslRequired ? 'Authentication required' : 'Optional'}</span></label></td></tr>`).join('')}</tbody></table></div><div class="form-actions"><span class="form-note">SMTPS, IMAPS and POP3S must use implicit TLS. Submission and client access should require SASL.</span>${canManage ? `<div class="form-actions-right"><button class="btn btn-primary" type="button" id="ports-save-2">Save listeners</button></div>` : ''}</div>`;
    document.getElementById('ports-paths').innerHTML = `<div class="kv-grid is-single">${ui.kv([
      ['Postfix master.cf', `<span class="mono break-all">${esc(config.masterCf || '')}</span>`],
      ['Dovecot drop-in', `<span class="mono break-all">${esc(config.dovecotPath || '')}</span>`],
    ])}<div class="note-strip" style="margin-top:10px">${ui.icon('info', 13)} <span>Both files are managed as a unit; the save is rolled back if Postfix or Dovecot rejects the result.</span></div></div>`;
    document.getElementById('stack-ports').innerHTML = stack.length ? `<div class="table-wrap"><table class="data compact"><thead><tr><th>Local address</th><th>Port</th><th>Process</th></tr></thead><tbody>${stack.map((row) => `<tr><td class="mono">${esc(row.local)}</td><td class="mono">${esc(row.port)}</td><td class="mono break-all">${esc(row.process || '—')}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No listening sockets returned', 'ss may be unavailable or the stack may be stopped.', '', 'plug');
    if (!canManage) return;
    document.querySelectorAll('[data-port-index]').forEach((input) => input.addEventListener('input', () => { listeners[Number(input.dataset.portIndex)].port = Number(input.value); }));
    document.querySelectorAll('[data-tls-index]').forEach((select) => select.addEventListener('change', () => { listeners[Number(select.dataset.tlsIndex)].tls = select.value; }));
    document.querySelectorAll('[data-sasl-index]').forEach((checkbox) => checkbox.addEventListener('change', () => {
      const listener = listeners[Number(checkbox.dataset.saslIndex)];
      listener.saslRequired = checkbox.checked;
      paint();
    }));
    document.getElementById('ports-save-2')?.addEventListener('click', save);
  };

  const validate = () => {
    const ports = new Set();
    for (const listener of listeners) {
      if (!Number.isInteger(listener.port) || listener.port < 1 || listener.port > 65535) return `${listener.protocol}: invalid port.`;
      if (ports.has(listener.port)) return `Port ${listener.port} is used twice.`;
      ports.add(listener.port);
      if (['smtps', 'imaps', 'pop3s'].includes(listener.protocol) && listener.tls !== 'implicit') return `${listener.protocol} must use implicit TLS.`;
    }
    return '';
  };

  const preview = async () => {
    const error = validate();
    if (error) { ui.toast(error, 'critical'); return; }
    try {
      const payload = await ctx.api('/control/api/services/ports/preview', { method: 'POST', body: { listeners } });
      ctx.modal({
        title: 'Listener change preview', subtitle: 'Review the exact file diffs before writing anything.', size: 'xlarge',
        body: `<div class="grid grid-main-even"><div><h4 class="form-section-title">master.cf</h4>${ui.diffBlock(payload.master.diff, 'No Postfix change')}<details class="mt-3"><summary class="muted" style="cursor:pointer;font-size:11px">Full resulting master.cf</summary><div class="mt-2">${ui.codeBlock(payload.master.after, { language: 'master.cf', maxHeight: '36vh' })}</div></details></div><div><h4 class="form-section-title">Dovecot ports</h4>${ui.diffBlock(payload.dovecot.diff, 'No Dovecot change')}<details class="mt-3"><summary class="muted" style="cursor:pointer;font-size:11px">Full resulting drop-in</summary><div class="mt-2">${ui.codeBlock(payload.dovecot.after, { language: 'dovecot conf', maxHeight: '36vh' })}</div></details></div></div>`,
        footer: `<button class="btn btn-ghost" type="button" data-modal-close>Close</button><button class="btn btn-primary" type="button" id="ports-apply-preview">Apply listeners</button>`,
        onMount: (el) => el.querySelector('#ports-apply-preview')?.addEventListener('click', save),
      });
    } catch (error) { ui.toast(errorText(error), 'critical', { title: 'Preview failed' }); }
  };

  const save = async () => {
    const error = validate();
    if (error) { ui.toast(error, 'critical'); return; }
    const ok = await ctx.confirm({ title: 'Apply listener ports', message: 'Rewrite Postfix master.cf and the Dovecot listener drop-in, validate both, then reload the daemons?', confirmLabel: 'Apply and reload', tone: 'primary' });
    if (!ok) return;
    try {
      await ctx.api('/control/api/services/ports', { method: 'PUT', body: { listeners } });
      ui.toast('Listener ports applied and daemons reloaded.', 'good');
      await load();
    } catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not apply listeners' }); }
  };

  document.getElementById('ports-preview')?.addEventListener('click', preview);
  document.getElementById('ports-save')?.addEventListener('click', save);
  document.getElementById('ports-refresh')?.addEventListener('click', load);
  await load();
}

function tlsModesFor(protocol, current) {
  let modes = ['starttls', 'none'];
  if (protocol === 'smtps' || protocol === 'imaps' || protocol === 'pop3s') modes = ['implicit'];
  if (protocol === 'submission') modes = ['starttls'];
  if (protocol === 'smtp') modes = ['starttls', 'none'];
  return modes.includes(current) ? modes : [...modes, current];
}

function protocolPurpose(protocol) {
  return {
    smtp: 'Inbound mail · MX',
    submission: 'Authenticated client submission',
    smtps: 'Submission over implicit TLS',
    imap: 'Mailbox access',
    imaps: 'Mailbox access over TLS',
    pop3: 'Legacy mail retrieval',
    pop3s: 'Legacy retrieval over TLS',
  }[protocol] || '';
}

/* =============================================================== postfix config */

export async function renderPostfix(ctx) {
  const canManage = ctx.can('services.manage');
  let file = 'main.cf';
  let content = '';
  let original = '';
  let hash = '';
  let path = '';
  ctx.setTitle('Postfix configuration', 'Infrastructure');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Infrastructure', title: 'Postfix configuration',
    description: 'Edit main.cf or master.cf with optimistic concurrency, postfix check rollback and an audit trail.',
    actions: `<button class="btn btn-secondary" type="button" id="postfix-reload">${ui.icon('refresh', 15)} Reload Postfix</button>${canManage ? `<button class="btn btn-soft-danger" type="button" id="postfix-restart">${ui.icon('rotate', 15)} Restart</button>` : ''}`,
  })}
    <div class="card"><header class="card-head"><div><h3>Managed configuration</h3><p id="postfix-path">Loading…</p></div><div class="card-actions"><div class="segmented" id="postfix-tabs"><button type="button" class="is-active" data-file="main.cf">main.cf</button><button type="button" data-file="master.cf">master.cf</button></div></div></header><div class="card-body" id="postfix-body">${ui.loadingState('Reading configuration…', 4)}</div></div>
    <div class="grid grid-main-even mt-4">
      <div class="card"><div class="card-body"><h3 style="font-size:12px">Safety behavior</h3><p class="form-note">Save writes a timestamped backup, then runs <span class="mono">postfix check</span>. If check fails, the previous file is restored automatically. A successful save ends with <span class="mono">postfix reload</span>. An optimistic hash prevents overwriting a file changed in another tab.</p></div></div>
      <div class="card"><div class="card-body"><h3 style="font-size:12px">Out-of-band edits</h3><p class="form-note">The control plane reconciles only its own managed blocks. Hand edits outside them are preserved where possible, but every config sync can reveal drift. Use <a href="#/sync">Config sync</a> after external changes.</p></div></div>
    </div>`;

  const loadFile = async (next) => {
    const body = document.getElementById('postfix-body');
    if (!body) return;
    if (document.getElementById('postfix-editor') && document.getElementById('postfix-editor').value !== original) {
      const discard = await ctx.confirm({ title: 'Discard unsaved edits?', message: 'Switching files will lose unsaved changes in the current editor.', confirmLabel: 'Discard changes', tone: 'danger' });
      if (!discard) return;
    }
    file = next;
    body.innerHTML = ui.loadingState(`Reading ${file}…`, 4);
    try {
      const payload = await ctx.api(`/control/api/services/config?file=${encodeURIComponent(file)}`);
      if (!ctx.isCurrent()) return;
      content = payload.content || '';
      original = content;
      hash = payload.sha256 || '';
      path = payload.path || '';
      document.getElementById('postfix-path').textContent = `${payload.path} · SHA-256 ${payload.sha256?.slice(0, 16)}…`;
      document.querySelectorAll('#postfix-tabs button').forEach((button) => button.classList.toggle('is-active', button.dataset.file === file));
      body.innerHTML = `<div class="flex between mb-2"><div class="badge-row">${ui.badge(file, 'info')}<span id="postfix-dirty" class="badge tone-warning" hidden>Unsaved changes</span></div><span class="muted" style="font-size:10.5px" id="postfix-stats">${ui.num(content.split('\n').length)} lines · ${ui.bytes(content.length)}</span></div>
        <textarea class="input mono" id="postfix-editor" spellcheck="false" style="min-height:430px;line-height:1.55;font-size:11.5px;tab-size:2" ${canManage ? '' : 'readonly'} aria-label="${esc(file)} content">${esc(content)}</textarea>
        <div class="form-actions"><span class="form-note">${canManage ? 'Saving creates a backup under the configured backup directory.' : 'Your role can view but not edit configuration.'}</span><div class="form-actions-right"><button class="btn btn-secondary" type="button" id="postfix-download">${ui.icon('download', 13)} Download current</button>${canManage ? `<button class="btn btn-primary" type="button" id="postfix-save">${ui.icon('check', 14)} Save and reload</button>` : ''}</div></div>`;
      const editor = document.getElementById('postfix-editor');
      editor.addEventListener('input', () => {
        const dirty = editor.value !== original;
        document.getElementById('postfix-dirty').hidden = !dirty;
        document.getElementById('postfix-stats').textContent = `${ui.num(editor.value.split('\n').length)} lines · ${ui.bytes(editor.value.length)}`;
      });
      editor.addEventListener('keydown', (event) => {
        if (event.key === 'Tab') {
          event.preventDefault();
          const start = editor.selectionStart;
          const end = editor.selectionEnd;
          editor.value = `${editor.value.slice(0, start)}  ${editor.value.slice(end)}`;
          editor.selectionStart = editor.selectionEnd = start + 2;
          editor.dispatchEvent(new Event('input'));
        }
      });
      document.getElementById('postfix-download')?.addEventListener('click', () => {
        const blob = new Blob([editor.value], { type: 'text/plain' });
        const anchor = document.createElement('a');
        anchor.href = URL.createObjectURL(blob);
        anchor.download = file;
        anchor.click();
        setTimeout(() => URL.revokeObjectURL(anchor.href), 2000);
      });
      document.getElementById('postfix-save')?.addEventListener('click', save);
    } catch (error) {
      body.innerHTML = ui.inlineNotice('critical', `Could not read ${file}`, errorText(error));
    }
  };

  const save = async () => {
    const editor = document.getElementById('postfix-editor');
    if (!editor) return;
    const ok = await ctx.confirm({ title: `Save ${file}`, message: 'Write this file, run postfix check, and reload Postfix if validation passes? A timestamped backup is created first.', confirmLabel: 'Save and apply', tone: 'primary' });
    if (!ok) return;
    try {
      const result = await ctx.api('/control/api/services/config', { method: 'PUT', body: { file, content: editor.value, expectedHash: hash } });
      original = editor.value;
      hash = result.sha256;
      document.getElementById('postfix-dirty').hidden = true;
      ui.toast(`Configuration saved${result.backup ? ` · backup ${result.backup}` : ''}.`, 'good');
      await loadFile(file);
    } catch (error) { ui.toast(errorText(error), 'critical', { title: 'Configuration save failed' }); }
  };

  document.querySelectorAll('#postfix-tabs button').forEach((button) => button.addEventListener('click', () => loadFile(button.dataset.file)));
  document.getElementById('postfix-reload')?.addEventListener('click', async () => {
    try { await ctx.api('/control/api/services/config/reload', { method: 'POST' }); ui.toast('Postfix reloaded.', 'good'); }
    catch (error) { ui.toast(errorText(error), 'critical', { title: 'Reload failed' }); }
  });
  document.getElementById('postfix-restart')?.addEventListener('click', async () => {
    const ok = await ctx.confirm({ title: 'Restart Postfix', message: 'Restart the Postfix master process? Queued mail resumes after the restart; connected SMTP sessions are dropped.', confirmLabel: 'Restart Postfix', tone: 'danger' });
    if (!ok) return;
    try { await ctx.api('/control/api/services/config/restart', { method: 'POST' }); ui.toast('Postfix restarted.', 'good'); }
    catch (error) { ui.toast(errorText(error), 'critical', { title: 'Restart failed' }); }
  });
  await loadFile('main.cf');
}

/* =============================================================== logs */

export async function renderLogs(ctx) {
  const canManage = ctx.can('settings.write');
  let page = 1;
  let limit = 100;
  let total = 0;
  let lastPayload = null;
  let live = false;
  let liveTimer = null;
  let filters = {
    q: ctx.query.q || '', source: ctx.query.source || '', status: ctx.query.status || '',
    direction: ctx.query.direction || '',
  };
  ctx.setTitle('Mail logs', 'Infrastructure');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Infrastructure', title: 'Mail logs',
    description: 'Mail events ingested from the Postfix log stream, searchable by envelope, queue ID, status and direction.',
    actions: `<button class="btn btn-secondary" type="button" id="logs-live"><span class="status-dot" id="logs-live-dot"></span> Live tail</button>${canManage ? `<button class="btn btn-secondary" type="button" id="logs-settings">${ui.icon('settings', 14)} Ingestion</button>` : ''}<button class="btn btn-secondary" type="button" id="logs-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    <div class="card">
      <header class="card-head"><div><h3>Mail event log</h3><p id="logs-summary">Loading events…</p></div><div class="card-actions">${canManage ? `<button class="btn btn-soft-danger btn-sm" type="button" id="logs-prune">${ui.icon('trash', 13)} Prune retention</button>` : ''}</div></header>
      <div class="card-body">
        <form id="logs-filter" class="filterbar">
          <div class="search-box">${ui.icon('search', 14)}<input class="input" name="q" value="${ui.attr(filters.q)}" placeholder="Sender, recipient, queue ID or raw text" aria-label="Search logs"></div>
          <select class="select" name="source" aria-label="Source"><option value="">All sources</option><option value="mail.log"${filters.source === 'mail.log' ? ' selected' : ''}>mail.log</option><option value="journald"${filters.source === 'journald' ? ' selected' : ''}>journald</option></select>
          <select class="select" name="status" aria-label="Status"><option value="">All statuses</option>${['sent', 'received', 'deferred', 'bounced', 'rejected', 'expired'].map((status) => `<option value="${status}"${filters.status === status ? ' selected' : ''}>${ui.humanTitle(status)}</option>`).join('')}</select>
          <select class="select" name="direction" aria-label="Direction"><option value="">All directions</option>${['inbound', 'outbound', 'internal', 'unknown'].map((direction) => `<option value="${direction}"${filters.direction === direction ? ' selected' : ''}>${ui.humanTitle(direction)}</option>`).join('')}</select>
          <button class="btn btn-secondary" type="submit">Apply</button><button class="btn btn-ghost" type="button" id="logs-reset">Reset</button>
        </form>
        <div id="logs-table">${ui.loadingState('Searching mail events…', 6)}</div>
      </div>
    </div>
    <div class="notice tone-info mt-4">${ui.icon('list', 16)}<div><strong>Ingestion</strong><p>Events are parsed from <span class="mono">/var/log/mail.log</span> or the systemd journal. The offset and inode are persisted so a restart does not re-read the file. Retention is ${ui.num(lastPayload?.retentionDays || 365)} days by default.</p></div></div>`;

  const load = async (silent = false) => {
    const host = document.getElementById('logs-table');
    if (!host) return;
    if (!silent) host.innerHTML = ui.loadingState('Searching mail events…', 6);
    const params = new URLSearchParams({ ...filters, page: String(page), limit: String(limit) });
    try {
      const payload = await ctx.api(`/control/api/logs?${params.toString()}`);
      if (!ctx.isCurrent()) return;
      lastPayload = payload;
      page = payload.page || page;
      total = payload.total || 0;
      const rows = Array.isArray(payload.events) ? payload.events : [];
      const pages = Math.max(1, Math.ceil(total / (payload.limit || limit)));
      document.getElementById('logs-summary').textContent = `${ui.num(total)} event${total === 1 ? '' : 's'} · retention ${ui.num(payload.retentionDays)} days · page ${page} of ${pages}`;
      host.innerHTML = rows.length ? `<div class="table-wrap"><table class="data"><thead><tr><th>Time</th><th>Direction</th><th>Status</th><th>Envelope</th><th>Subject</th><th class="num">Size</th><th class="num">Delay</th><th>Queue</th><th></th></tr></thead><tbody>${rows.map((row) => `<tr class="clickable-row" data-log-id="${row.id}"><td class="nowrap-cell"><span class="cell-main">${esc(fmt.timeAgo(row.at))}</span><span class="cell-sub" title="${ui.attr(fmt.dateTime(row.at))}">${esc(fmt.dateTime(row.at))}</span></td><td>${ui.badge(ui.humanTitle(row.direction), row.direction === 'inbound' ? 'info' : row.direction === 'outbound' ? 'accent' : 'neutral')}</td><td>${ui.statusPill(row.status)}</td><td><span class="cell-main mono">${esc(row.sender || '—')}</span><span class="cell-sub mono">→ ${esc(row.recipient || '—')}</span></td><td>${esc(ui.truncate(row.subject || '—', 48))}</td><td class="num">${ui.bytes(row.sizeBytes)}</td><td class="num">${ui.num(row.delaySeconds)}s</td><td class="mono">${esc(row.queueId || '—')}</td><td class="actions"><button class="icon-btn" type="button" data-log-detail="${row.id}" title="Event detail" aria-label="Event detail">${ui.icon('eye', 14)}</button></td></tr>`).join('')}</tbody></table></div><div class="table-footer"><span>${ui.num(total)} events · ${ui.num(limit)} per page</span><div class="pagination"><button class="page-btn" type="button" id="logs-prev" ${page <= 1 ? 'disabled' : ''}>${ui.icon('chevron-left', 13)}</button><span class="page-btn is-active">${page}</span><button class="page-btn" type="button" id="logs-next" ${page >= pages ? 'disabled' : ''}>${ui.icon('chevron-right', 13)}</button></div></div>` : ui.emptyState('No events match', 'Change the filters, or wait for new mail to flow through Postfix.', '', 'list');
      document.getElementById('logs-prev')?.addEventListener('click', () => { if (page > 1) { page -= 1; load(); } });
      document.getElementById('logs-next')?.addEventListener('click', () => { if (page < pages) { page += 1; load(); } });
      host.querySelectorAll('[data-log-detail]').forEach((button) => button.addEventListener('click', (event) => { event.stopPropagation(); showDetail(button.dataset.logDetail); }));
      host.querySelectorAll('tr[data-log-id]').forEach((row) => row.addEventListener('click', (event) => { if (!event.target.closest('button,a')) showDetail(row.dataset.logId); }));
    } catch (error) { host.innerHTML = ui.inlineNotice('critical', 'Could not search logs', errorText(error)); }
  };

  const showDetail = async (logId) => {
    const modal = ctx.modal({ title: 'Mail event', size: 'large', body: ui.loadingState('Loading event…', 2), footer: `<button class="btn btn-secondary" type="button" data-modal-close>Close</button>` });
    try {
      const row = await ctx.api(`/control/api/logs/${logId}`);
      if (!modal.root.isConnected) return;
      modal.root.querySelector('.modal-body').innerHTML = `<div class="diagnostic-summary">${ui.statusPill(row.status)}${ui.badge(ui.humanTitle(row.direction), row.direction === 'inbound' ? 'info' : row.direction === 'outbound' ? 'accent' : 'neutral')}<span class="summary-pill">${ui.icon('clock', 12)} ${esc(fmt.dateTime(row.at))}</span><span class="summary-pill">${ui.icon('server', 12)} ${esc(row.source)}</span></div>
        <div class="kv-grid is-3">${ui.kv([
          ['Sender', `<span class="mono break-all">${esc(row.sender || '—')}</span>`],
          ['Recipient', `<span class="mono break-all">${esc(row.recipient || '—')}</span>`],
          ['Domain', `<span class="mono">${esc(row.domain || '—')}</span>`],
          ['Queue ID', `<span class="mono">${esc(row.queueId || '—')}</span>`],
          ['Size', ui.bytes(row.sizeBytes)], ['Delay', `${ui.num(row.delaySeconds)} seconds`],
          ['Relay', `<span class="mono break-all">${esc(row.relay || '—')}</span>`], ['Subject', esc(row.subject || '—')], ['Direction', esc(row.direction)],
        ])}</div>
        <h4 class="form-section-title" style="margin-top:15px">Detail</h4><p class="form-note break-all">${esc(row.detail || 'No parsed detail.')}</p>
        <h4 class="form-section-title" style="margin-top:15px">Raw log line</h4>${ui.codeBlock(row.raw || 'No raw line stored', { language: 'postfix', maxHeight: '38vh' })}`;
    } catch (error) { modal.root.querySelector('.modal-body').innerHTML = ui.inlineNotice('critical', 'Could not load event', errorText(error)); }
  };

  const setLive = (enabled) => {
    live = enabled;
    const button = document.getElementById('logs-live');
    const dot = document.getElementById('logs-live-dot');
    if (button) button.classList.toggle('btn-primary', live);
    if (dot) dot.className = `status-dot ${live ? 'up' : ''}`;
    if (liveTimer) { clearInterval(liveTimer); liveTimer = null; }
    if (live) {
      liveTimer = setInterval(() => { if (ctx.isCurrent() && document.visibilityState === 'visible') load(true); }, 6000);
      ui.toast('Live tail is on. New events refresh automatically.', 'info');
      load(true);
    }
  };
  ctx.onCleanup(() => { if (liveTimer) clearInterval(liveTimer); });
  let eventTimer = null;
  ctx.listen(window, 'control:event', (event) => {
    if (!live || event.detail?.event !== 'mail.event') return;
    if (eventTimer) clearTimeout(eventTimer);
    eventTimer = setTimeout(() => { eventTimer = null; if (ctx.isCurrent()) load(true); }, 1200);
  });
  ctx.onCleanup(() => { if (eventTimer) clearTimeout(eventTimer); });

  document.getElementById('logs-live')?.addEventListener('click', () => setLive(!live));
  document.getElementById('logs-refresh')?.addEventListener('click', () => load());
  document.getElementById('logs-filter')?.addEventListener('submit', (event) => {
    event.preventDefault();
    const values = ui.formValues(event.currentTarget);
    filters = { q: values.q || '', source: values.source || '', status: values.status || '', direction: values.direction || '' };
    page = 1;
    load();
  });
  document.getElementById('logs-reset')?.addEventListener('click', () => {
    filters = { q: '', source: '', status: '', direction: '' };
    page = 1;
    document.getElementById('logs-filter')?.reset();
    load();
  });
  document.getElementById('logs-settings')?.addEventListener('click', async () => {
    const modal = ctx.modal({ title: 'Log ingestion settings', size: 'small', body: ui.loadingState('Loading settings…', 2) });
    try {
      const settings = await ctx.api('/control/api/logs/settings');
      modal.root.querySelector('.modal-body').innerHTML = `<form id="logs-settings-form"><div class="form-grid is-single">
        ${ui.field({ label: 'Ingestion source', name: 'source', type: 'select', value: settings.source, options: [{ value: 'mail.log', label: 'mail.log file' }, { value: 'journald', label: 'systemd journal' }], help: `Current file path: ${settings.mailLog}` })}
        ${ui.field({ label: 'Retention (days)', name: 'retentionDays', type: 'number', value: settings.retentionDays, min: 1, max: 3650, help: 'Events older than this are pruned on a schedule and by the manual prune action.' })}
      </div><div class="form-actions"><span class="form-note">Switching sources starts from the current watermark; already ingested events remain searchable.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">Save ingestion</button></div></div></form>`;
      const form = modal.root.querySelector('#logs-settings-form');
      form?.addEventListener('submit', async (event) => {
        event.preventDefault();
        const values = ui.formValues(form);
        try {
          await ctx.api('/control/api/logs/settings', { method: 'PUT', body: { source: values.source, retentionDays: Number(values.retentionDays) } });
          modal.close();
          ui.toast('Log ingestion settings saved.', 'good');
          await load();
        } catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not save settings' }); }
      });
    } catch (error) { modal.root.querySelector('.modal-body').innerHTML = ui.inlineNotice('critical', 'Could not load settings', errorText(error)); }
  });
  document.getElementById('logs-prune')?.addEventListener('click', async () => {
    if (!lastPayload) return;
    const ok = await ctx.confirm({ title: 'Prune mail events', message: `Delete events older than the configured ${lastPayload.retentionDays}-day retention? This cannot be undone.`, confirmLabel: 'Prune events', tone: 'danger' });
    if (!ok) return;
    try {
      const result = await ctx.api('/control/api/logs/prune', { method: 'POST' });
      ui.toast(`${ui.num(result.deleted)} event${result.deleted === 1 ? '' : 's'} pruned.`, 'good');
      await load();
    } catch (error) { ui.toast(errorText(error), 'critical', { title: 'Prune failed' }); }
  });
  await load();
}

/* =============================================================== backups */

export async function renderBackups(ctx) {
  const canManage = ctx.can('backups.manage');
  let backups = [];
  let contentOptions = {};
  let directory = '';
  let timer = null;
  ctx.setTitle('Backups', 'Infrastructure');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Infrastructure', title: 'Backups',
    description: 'Create verifiable .tar.gz archives of the database, mail configuration, DKIM keys, certificates and Maildirs under the managed backup directory.',
    actions: `${canManage ? `<button class="btn btn-primary" type="button" id="backup-create">${ui.icon('archive', 15)} Create backup</button>` : ''}<button class="btn btn-secondary" type="button" id="backup-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    <div class="card"><header class="card-head"><div><h3>Archives</h3><p id="backup-summary">Loading backups…</p></div></header><div class="card-body" id="backup-list">${ui.loadingState('Loading backups…', 4)}</div></div>
    <div class="grid grid-main-even mt-4">
      <div class="card"><div class="card-body"><h3 style="font-size:12px">Storage</h3><p class="form-note">Backups live in <span class="mono">${esc(directory || '/var/backups/alt8mail')}</span> with directory mode 0700. Download a copy off-host; a backup on the same server is not disaster recovery.</p></div></div>
      <div class="card"><div class="card-body"><h3 style="font-size:12px">Restore safety</h3><p class="form-note">Restore is selective and destructive for the selected components. The database uses <span class="mono">pg_restore --clean</span>; config trees are copied over managed paths. Test restores on a spare host before relying on them.</p></div></div>
    </div>`;

  const load = async (silent = false) => {
    const host = document.getElementById('backup-list');
    if (!host) return;
    if (!silent) host.innerHTML = ui.loadingState('Loading backups…', 4);
    try {
      const payload = await ctx.api('/control/api/backups');
      if (!ctx.isCurrent()) return;
      backups = Array.isArray(payload.backups) ? payload.backups : [];
      contentOptions = payload.contentOptions || {};
      directory = payload.directory || '';
      const running = backups.filter((backup) => backup.status === 'running').length;
      const done = backups.filter((backup) => backup.status === 'completed').length;
      const bytesTotal = backups.reduce((sum, backup) => sum + Number(backup.sizeBytes || 0), 0);
      document.getElementById('backup-summary').textContent = `${backups.length} archive${backups.length === 1 ? '' : 's'} · ${done} completed · ${running} running · ${ui.bytes(bytesTotal)} total`;
      host.innerHTML = backups.length ? `<div class="table-wrap is-bare"><table class="data"><thead><tr><th>Name</th><th>Contents</th><th class="num">Size</th><th>Status</th><th>Created</th><th></th></tr></thead><tbody>${backups.map((backup) => `<tr><td><span class="cell-main">${esc(backup.name)}</span><span class="cell-sub mono break-all">${esc(backup.path)}</span>${backup.detail ? `<span class="cell-sub text-critical">${esc(backup.detail)}</span>` : ''}</td><td><div class="chip-list">${(backup.contents || []).map((content) => `<span class="mono-chip">${esc(content)}</span>`).join('')}</div></td><td class="num">${ui.bytes(backup.sizeBytes)}</td><td>${ui.statusPill(backup.status)}</td><td>${esc(fmt.dateTime(backup.createdAt))}</td><td class="actions">${backup.status === 'completed' ? `<a class="icon-btn" href="/control/api/backups/${esc(backup.id)}/download" title="Download archive" aria-label="Download archive">${ui.icon('download', 15)}</a>${canManage ? `<button class="icon-btn" type="button" data-backup-restore="${esc(backup.id)}" title="Restore archive" aria-label="Restore archive">${ui.icon('rotate', 15)}</button>` : ''}` : ''}${canManage && backup.status !== 'running' ? `<button class="icon-btn is-danger" type="button" data-backup-delete="${esc(backup.id)}" title="Delete archive" aria-label="Delete archive">${ui.icon('trash', 15)}</button>` : ''}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No backups yet', 'Create the first archive before making infrastructure changes.', canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="backup-create-empty">${ui.icon('archive', 13)} Create backup</button>` : '', 'archive');
      document.getElementById('backup-create-empty')?.addEventListener('click', openCreate);
      host.querySelectorAll('[data-backup-delete]').forEach((button) => button.addEventListener('click', async () => {
        const backup = backups.find((item) => item.id === button.dataset.backupDelete);
        if (!backup) return;
        const ok = await ctx.confirm({ title: 'Delete backup archive', message: `Permanently delete ${backup.name} from disk? This cannot be undone.`, confirmLabel: 'Delete archive', tone: 'danger' });
        if (!ok) return;
        try { await ctx.api(`/control/api/backups/${backup.id}`, { method: 'DELETE' }); ui.toast('Backup deleted.', 'good'); await load(); }
        catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not delete backup' }); }
      }));
      host.querySelectorAll('[data-backup-restore]').forEach((button) => button.addEventListener('click', () => openRestore(backups.find((item) => item.id === button.dataset.backupRestore))));
      if (running && !timer) {
        timer = setInterval(() => { if (ctx.isCurrent()) load(true); }, 4000);
        ctx.onCleanup(() => { if (timer) { clearInterval(timer); timer = null; } });
      }
      if (!running && timer) { clearInterval(timer); timer = null; }
    } catch (error) { host.innerHTML = ui.inlineNotice('critical', 'Could not load backups', errorText(error)); }
  };

  const optionsMarkup = (selected) => Object.entries(contentOptions).map(([key, label]) => `<label class="check-field"><input type="checkbox" name="backupContents" value="${esc(key)}"${selected.includes(key) ? ' checked' : ''}><span>${esc(label)}<small class="mono">${esc(key)}</small></span></label>`).join('');

  const openCreate = () => {
    const defaults = ['panel_database', 'postfix', 'dovecot', 'dkim', 'letsencrypt'];
    const modal = ctx.modal({
      title: 'Create backup archive', size: 'medium',
      body: `<form id="backup-form"><div class="form-grid is-single">
        ${ui.field({ label: 'Archive name', name: 'name', placeholder: `alt8mail-backup-${new Date().toISOString().slice(0, 10)}`, help: 'Letters, digits, dots, hyphens and underscores. A timestamp suffix is not added automatically.' })}
        <div class="field"><label>Contents</label><div class="check-grid">${optionsMarkup(defaults)}</div><small class="field-help">Maildirs can be very large. Archive them only when you need message stores included.</small></div>
      </div><div class="notice tone-info mt-3">${ui.icon('info', 16)}<div><strong>Archives are created in the background</strong><p>Progress appears in the table. The archive is written atomically; interrupted runs are marked failed.</p></div></div><div class="form-actions"><span class="form-note">Database dump uses pg_dump custom format.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">Start backup</button></div></div></form>`,
    });
    const form = modal.root.querySelector('#backup-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const contents = ui.selectedValues(form, 'backupContents');
      if (!contents.length) { ui.toast('Select at least one content set.', 'critical'); return; }
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        await ctx.api('/control/api/backups', { method: 'POST', body: { name: values.name || '', contents } });
        modal.close();
        ui.toast('Backup started in the background.', 'good');
        await load();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not start backup' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  const openRestore = (backup) => {
    if (!backup) return;
    const modal = ctx.modal({
      title: 'Restore backup', subtitle: backup.name, size: 'medium',
      body: `<div class="notice tone-critical">${ui.icon('alert-circle', 16)}<div><strong>Restoring overwrites live state</strong><p>Selected components are replaced. This can disconnect users and destroy changes made after ${esc(fmt.dateTime(backup.createdAt))}.</p></div></div>
        <form id="restore-form" class="mt-3"><div class="field"><label>Components to restore</label><div class="check-grid">${optionsMarkup(backup.contents || [])}</div></div>
        <div class="field mt-3"><label for="restore-confirm">Type <strong class="mono">RESTORE</strong> to confirm</label><input class="input" id="restore-confirm" name="confirm" autocomplete="off"></div>
        <div class="form-actions"><span class="form-note">Postfix, Dovecot and Rspamd are reloaded afterward.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-danger" type="submit">Restore selected</button></div></div></form>`,
    });
    const form = modal.root.querySelector('#restore-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      if (values.confirm !== 'RESTORE') { ui.toast('Type RESTORE exactly to confirm.', 'critical'); return; }
      const contents = ui.selectedValues(form, 'backupContents');
      if (!contents.length) { ui.toast('Select at least one component.', 'critical'); return; }
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        const result = await ctx.api(`/control/api/backups/${backup.id}/restore`, { method: 'POST', body: { confirm: 'RESTORE', contents } });
        modal.close();
        ui.toast(`Restore completed: ${result.restored.join(', ')}.`, 'good');
        await load();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Restore failed' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  document.getElementById('backup-create')?.addEventListener('click', openCreate);
  document.getElementById('backup-refresh')?.addEventListener('click', () => load());
  await load();
}

/* =============================================================== cron */

export async function renderCron(ctx) {
  const canManage = ctx.can('cron.manage');
  let jobs = [];
  let cronFile = '';
  ctx.setTitle('Scheduled tasks', 'Infrastructure');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Infrastructure', title: 'Scheduled tasks',
    description: 'Cron jobs written to /etc/cron.d/alt8mail, executed through the control CLI with stored output, status and duration.',
    actions: `${canManage ? `<button class="btn btn-primary" type="button" id="cron-create">${ui.icon('plus', 15)} New task</button>` : ''}<button class="btn btn-secondary" type="button" id="cron-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    <div class="card"><header class="card-head"><div><h3>Scheduled tasks</h3><p id="cron-summary">Loading tasks…</p></div><span class="mono-chip" id="cron-file">${esc(cronFile)}</span></header><div class="card-body" id="cron-list">${ui.loadingState('Loading scheduled tasks…', 4)}</div></div>
    <div class="grid grid-main-even mt-4">
      <div class="card"><div class="card-body"><h3 style="font-size:12px">Execution model</h3><p class="form-note">The generated cron file invokes <span class="mono">alt8mail-control cron run &lt;id&gt;</span>. The control service reads the stored command, runs it through <span class="mono">/bin/sh</span> as the chosen run-as user, and records stdout, stderr, exit status and duration.</p></div></div>
      <div class="card"><div class="card-body"><h3 style="font-size:12px">Command trust</h3><p class="form-note">Scheduled commands run with the privileges of their run-as user. Restrict this permission to trusted administrators. All create, update, run and delete actions are audited.</p></div></div>
    </div>`;

  const load = async () => {
    const host = document.getElementById('cron-list');
    if (!host) return;
    try {
      const payload = await ctx.api('/control/api/cron');
      if (!ctx.isCurrent()) return;
      jobs = Array.isArray(payload.jobs) ? payload.jobs : [];
      cronFile = payload.cronFile || '';
      const enabled = jobs.filter((job) => job.enabled).length;
      document.getElementById('cron-summary').textContent = `${jobs.length} task${jobs.length === 1 ? '' : 's'} · ${enabled} enabled`;
      document.getElementById('cron-file').textContent = cronFile;
      host.innerHTML = jobs.length ? `<div class="table-wrap is-bare"><table class="data"><thead><tr><th>Task</th><th>Schedule</th><th>Run as</th><th>State</th><th>Last run</th><th></th></tr></thead><tbody>${jobs.map((job) => `<tr><td><span class="cell-main">${esc(job.name)}</span><span class="cell-sub mono break-all">${esc(ui.truncate(job.command, 90))}</span></td><td><span class="mono">${esc(job.schedule)}</span>${job.startDate || job.endDate ? `<span class="cell-sub">${esc(job.startDate || 'now')} → ${esc(job.endDate || 'no end')}</span>` : ''}</td><td>${ui.badge(job.runAs, job.runAs === 'root' ? 'critical' : 'neutral')}</td><td>${ui.statusPill(job.enabled ? 'active' : 'disabled', job.enabled ? 'Enabled' : 'Disabled')}</td><td>${job.lastRunAt ? `${ui.statusPill(job.lastStatus, ui.humanTitle(job.lastStatus))}<span class="cell-sub">${esc(fmt.timeAgo(job.lastRunAt))} · ${ui.num(job.lastDurationMs)} ms</span>` : '<span class="muted">Never run</span>'}</td><td class="actions">${canManage ? `<button class="icon-btn" type="button" data-cron-run="${esc(job.id)}" title="Run now" aria-label="Run now">${ui.icon('play', 14)}</button>` : ''}<button class="icon-btn" type="button" data-cron-runs="${esc(job.id)}" title="Execution log" aria-label="Execution log">${ui.icon('list', 14)}</button>${canManage ? `<button class="icon-btn" type="button" data-cron-edit="${esc(job.id)}" title="Edit task" aria-label="Edit task">${ui.icon('edit', 14)}</button><button class="icon-btn is-danger" type="button" data-cron-delete="${esc(job.id)}" title="Delete task" aria-label="Delete task">${ui.icon('trash', 14)}</button>` : ''}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No scheduled tasks', 'Create a job to run maintenance, reports or integrations on a cron schedule.', canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="cron-create-empty">${ui.icon('plus', 13)} New task</button>` : '', 'calendar');
      document.getElementById('cron-create-empty')?.addEventListener('click', () => openEditor(null));
      host.querySelectorAll('[data-cron-run]').forEach((button) => button.addEventListener('click', async () => {
        const job = jobs.find((item) => item.id === button.dataset.cronRun);
        if (!job) return;
        const ok = await ctx.confirm({ title: 'Run task now', message: `Run ${job.name} immediately as ${job.runAs}? The scheduled execution happens in parallel.`, confirmLabel: 'Run now', tone: 'primary' });
        if (!ok) return;
        try { await ctx.api(`/control/api/cron/${job.id}/run`, { method: 'POST' }); ui.toast('Task started.', 'good'); setTimeout(() => { if (ctx.isCurrent()) load(); }, 1200); }
        catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not run task' }); }
      }));
      host.querySelectorAll('[data-cron-runs]').forEach((button) => button.addEventListener('click', () => showRuns(button.dataset.cronRuns)));
      host.querySelectorAll('[data-cron-edit]').forEach((button) => button.addEventListener('click', () => openEditor(jobs.find((job) => job.id === button.dataset.cronEdit))));
      host.querySelectorAll('[data-cron-delete]').forEach((button) => button.addEventListener('click', async () => {
        const job = jobs.find((item) => item.id === button.dataset.cronDelete);
        if (!job) return;
        const ok = await ctx.confirm({ title: 'Delete scheduled task', message: `Delete ${job.name} from the cron file and database? Execution history is removed.`, confirmLabel: 'Delete task', tone: 'danger' });
        if (!ok) return;
        try { await ctx.api(`/control/api/cron/${job.id}`, { method: 'DELETE' }); ui.toast('Scheduled task deleted.', 'good'); await load(); }
        catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not delete task' }); }
      }));
    } catch (error) { host.innerHTML = ui.inlineNotice('critical', 'Could not load scheduled tasks', errorText(error)); }
  };

  const openEditor = (job) => {
    const editing = !!job;
    const modal = ctx.modal({
      title: editing ? 'Edit scheduled task' : 'New scheduled task', size: 'large',
      body: `<form id="cron-form"><div class="form-grid">
        ${ui.field({ label: 'Task name', name: 'name', value: job?.name || '', required: true, placeholder: 'Nightly mailbox report' })}
        ${ui.field({ label: 'Schedule (cron)', name: 'schedule', value: job?.schedule || '0 3 * * *', required: true, help: 'Five fields: minute hour day-of-month month day-of-week. Example: 0 3 * * * = daily 03:00.' })}
        <div class="span-2">${ui.field({ label: 'Command', name: 'command', type: 'textarea', rows: 3, value: job?.command || '', required: true, help: 'Executed by /bin/sh. Example: /opt/alt8mail/bin/alt8mail-control doctor' })}</div>
        ${ui.field({ label: 'Run as user', name: 'runAs', value: job?.runAs || 'root', required: true, help: 'Must exist on the host. Prefer the least-privileged account that can perform the task.' })}
        ${ui.field({ label: 'Start date', name: 'startDate', type: 'date', value: job?.startDate || '' })}
        ${ui.field({ label: 'End date', name: 'endDate', type: 'date', value: job?.endDate || '' })}
        <div class="span-2">${ui.field({ label: 'Standard input', name: 'stdin', type: 'textarea', value: job?.stdin || '', rows: 3, help: 'Optional text piped to the command on stdin. Do not put long-lived secrets here unless the database is appropriately protected.' })}</div>
        <div class="span-2">${ui.switchField({ label: 'Enabled', name: 'enabled', checked: job ? !!job.enabled : true, help: 'Disabled jobs stay stored but are omitted from /etc/cron.d/alt8mail.' })}</div>
      </div><div class="form-actions"><span class="form-note">The cron line invokes the stored job by ID; editing here updates the next run automatically.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">${editing ? 'Save task' : 'Create task'}</button></div></div></form>`,
    });
    const form = modal.root.querySelector('#cron-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        if (editing) await ctx.api(`/control/api/cron/${job.id}`, { method: 'PATCH', body: { name: values.name, schedule: values.schedule, command: values.command, runAs: values.runAs, stdin: values.stdin || '', startDate: values.startDate || '', endDate: values.endDate || '', enabled: values.enabled } });
        else await ctx.api('/control/api/cron', { method: 'POST', body: { name: values.name, schedule: values.schedule, command: values.command, runAs: values.runAs, stdin: values.stdin || '', startDate: values.startDate || '', endDate: values.endDate || '', enabled: values.enabled } });
        modal.close();
        ui.toast(editing ? 'Scheduled task updated.' : 'Scheduled task created.', 'good');
        await load();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not save scheduled task' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  const showRuns = async (jobId) => {
    const modal = ctx.modal({ title: 'Task execution history', size: 'large', body: ui.loadingState('Loading runs…', 3), footer: `<button class="btn btn-secondary" type="button" data-modal-close>Close</button>` });
    try {
      const payload = await ctx.api(`/control/api/cron/${jobId}/runs`);
      if (!modal.root.isConnected) return;
      const runs = Array.isArray(payload.runs) ? payload.runs : [];
      modal.root.querySelector('.modal-body').innerHTML = runs.length ? `<div class="table-wrap"><table class="data compact"><thead><tr><th>Status</th><th>Started</th><th>Finished</th><th class="num">Duration</th></tr></thead><tbody>${runs.map((run) => `<tr><td>${ui.statusPill(run.status)}</td><td>${esc(fmt.dateTime(run.startedAt))}</td><td>${esc(fmt.dateTime(run.finishedAt))}</td><td class="num">${ui.num(run.durationMs)} ms</td></tr><tr><td colspan="4"><details><summary class="muted" style="cursor:pointer;font-size:10.5px">Output</summary>${ui.codeBlock(run.output || 'No output', { language: 'stdout / stderr', maxHeight: '24vh' })}</details></td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No runs yet', 'Run the task now or wait for its schedule.', '', 'calendar');
    } catch (error) { modal.root.querySelector('.modal-body').innerHTML = ui.inlineNotice('critical', 'Could not load runs', errorText(error)); }
  };

  document.getElementById('cron-create')?.addEventListener('click', () => openEditor(null));
  document.getElementById('cron-refresh')?.addEventListener('click', load);
  await load();
}

/* =============================================================== server & network */

export async function renderServer(ctx) {
  const canManage = ctx.can('server.manage');
  let settings = {};
  let hostname = '';
  let network = {};
  let time = {};
  let managed = [];
  ctx.setTitle('Server & network', 'Infrastructure');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Infrastructure', title: 'Server & network',
    description: 'Host identity, DNS resolver, webmail URL, interfaces, IP addresses, time zone and NTP state.',
    actions: `<button class="btn btn-secondary" type="button" id="server-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    <div class="grid grid-main">
      <div class="stack">
        <section class="card"><header class="card-head"><div><h3>Mail identity and resolver</h3><p>Used by DNS plans, SMTP greetings, HELO and default sender addresses</p></div></header><div class="card-body" id="server-settings">${ui.loadingState('Loading server settings…', 4)}</div></section>
        <section class="card"><header class="card-head"><div><h3>Network interfaces</h3><p>Live view from <span class="mono">ip addr</span> and the managed address registry</p></div>${canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="address-add">${ui.icon('plus', 13)} Add address</button>` : ''}</header><div class="card-body" id="network-body">${ui.loadingState('Loading interfaces…', 4)}</div></section>
        <section class="card"><header class="card-head"><div><h3>Routes</h3><p>Kernel routing table</p></div></header><div class="card-body" id="routes-body">${ui.loadingState('Loading routes…', 3)}</div></section>
      </div>
      <div class="stack">
        <section class="card"><header class="card-head"><div><h3>Time and NTP</h3><p>Server clock, zone and synchronization</p></div></header><div class="card-body" id="time-body">${ui.loadingState('Loading time state…', 2)}</div></section>
        <section class="card"><header class="card-head"><div><h3>Connectivity test</h3><p>TCP reachability from this host</p></div></header><div class="card-body">
          <form id="connectivity-form" class="form-grid is-single">
            ${ui.field({ label: 'Host', name: 'host', placeholder: 'mail.example.com or 203.0.113.10', required: true })}
            ${ui.field({ label: 'Port', name: 'port', type: 'number', value: '25', min: 1, max: 65535, required: true })}
            ${ui.field({ label: 'Timeout (seconds)', name: 'timeout', type: 'number', value: '5', min: 1, max: 20 })}
            <div class="form-actions"><span class="form-note">Useful for MX, relay and upstream DNS checks.</span><div class="form-actions-right"><button class="btn btn-secondary" type="submit">Run test</button></div></div>
          </form>
          <div id="connectivity-result"></div>
        </div></section>
        <section class="card"><header class="card-head"><div><h3>Host details</h3></div></header><div class="card-body" id="host-details">${ui.loadingState('Loading…', 2)}</div></section>
      </div>
    </div>`;

  const paint = () => {
    const host = document.getElementById('server-settings');
    if (!host) return;
    const value = (key, fallback = '') => settings[key] ?? fallback;
    host.innerHTML = `<form id="server-settings-form"><div class="form-grid">
      ${ui.field({ label: 'Hostname', name: 'hostname', value: value('server.hostname', hostname || ''), required: true, help: `Kernel hostname: ${hostname}` })}
      ${ui.field({ label: 'Mail hostname', name: 'mailHostname', value: value('server.mail_hostname', ''), required: true, help: 'The MX target and HELO name. Must have matching A/AAAA and PTR.' })}
      ${ui.field({ label: 'Webmail URL', name: 'webmailUrl', type: 'url', value: value('server.webmail_url', ''), placeholder: 'https://mail.example.com', help: 'Used in notifications and DNS plans.' })}
      ${ui.field({ label: 'DNS resolver', name: 'dnsResolver', value: value('server.dns_resolver', ''), placeholder: '1.1.1.1:53', help: 'IP address with optional port. Used for DNS and DNSBL lookups.' })}
      ${ui.field({ label: 'Public IPv4', name: 'publicIpv4', value: value('server.public_ipv4', ''), placeholder: '203.0.113.10' })}
      ${ui.field({ label: 'Public IPv6', name: 'publicIpv6', value: value('server.public_ipv6', ''), placeholder: '2001:db8::10' })}
      ${ui.field({ label: 'Default mail-from', name: 'mailFrom', type: 'email', value: value('server.mail_from', ''), placeholder: 'postmaster@example.com', help: 'Envelope sender for panel notifications and tests.' })}
      <div class="field"><label>Apply hostname</label>${ui.switchField({ label: 'Run hostnamectl set-hostname', name: 'applyHostname', help: 'Updates the live kernel hostname. DNS and /etc/hosts changes remain your responsibility.' })}</div>
      <div class="span-2 form-actions"><span class="form-note">The panel DB is the source of truth for these settings; files are reconciled by config sync where applicable.</span><div class="form-actions-right"><button class="btn btn-primary" type="submit" ${canManage ? '' : 'disabled'}>Save server settings</button></div></div>
    </div></form>`;
    document.getElementById('server-settings-form')?.addEventListener('submit', async (event) => {
      event.preventDefault();
      if (!canManage) return;
      const values = ui.formValues(event.currentTarget);
      const button = event.currentTarget.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        await ctx.api('/control/api/server/settings', { method: 'PUT', body: {
          hostname: values.hostname, mailHostname: values.mailHostname, webmailUrl: values.webmailUrl,
          dnsResolver: values.dnsResolver, publicIpv4: values.publicIpv4, publicIpv6: values.publicIpv6,
          mailFrom: values.mailFrom, applyHostname: !!values.applyHostname,
        } });
        ui.toast('Server settings saved.', 'good');
        await renderServer(ctx);
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not save server settings' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  const paintNetwork = () => {
    const interfaces = Array.isArray(network.interfaces) ? network.interfaces : [];
    const host = document.getElementById('network-body');
    host.innerHTML = interfaces.length ? `<div class="table-wrap"><table class="data compact"><thead><tr><th>Interface</th><th>State</th><th>Addresses</th><th>MTU</th></tr></thead><tbody>${interfaces.map((iface) => `<tr><td><span class="cell-main mono">${esc(iface.ifname)}</span><span class="cell-sub mono">${esc(iface.address || '')}</span></td><td>${ui.statusPill(iface.operstate === 'UP' ? 'active' : 'disabled', ui.humanTitle(iface.operstate || 'unknown'))}</td><td>${(iface.addr_info || []).map((address) => `<div class="mono" style="font-size:10.5px">${esc(address.local)}/${ui.num(address.prefixlen)} <span class="muted">${esc(address.family)} ${esc(address.scope || '')}</span></div>`).join('') || '—'}</td><td class="num">${ui.num(iface.mtu)}</td></tr>`).join('')}</tbody></table></div>${managed.length ? `<h4 class="form-section-title" style="margin-top:16px">Panel-added addresses</h4><div class="table-wrap"><table class="data compact"><thead><tr><th>Interface</th><th>Address</th><th>Label</th><th></th></tr></thead><tbody>${managed.map((address) => `<tr><td class="mono">${esc(address.interface)}</td><td class="mono">${esc(address.address)}</td><td>${esc(address.label || '—')}</td><td class="actions">${canManage ? `<button class="icon-btn is-danger" type="button" data-address-delete="${esc(address.id)}" title="Remove address" aria-label="Remove address">${ui.icon('trash', 14)}</button>` : ''}</td></tr>`).join('')}</tbody></table></div>` : ''}` : ui.emptyState('No interfaces returned', 'ip may be unavailable on this host.', '', 'network');
    host.querySelectorAll('[data-address-delete]').forEach((button) => button.addEventListener('click', async () => {
      const address = managed.find((item) => item.id === button.dataset.addressDelete);
      if (!address) return;
      const ok = await ctx.confirm({ title: 'Remove IP address', message: `Remove ${address.address} from ${address.interface} at runtime? This does not change persistent network configuration.`, confirmLabel: 'Remove address', tone: 'danger' });
      if (!ok) return;
      try { await ctx.api(`/control/api/server/addresses/${address.id}`, { method: 'DELETE' }); ui.toast('Address removed.', 'good'); await renderServer(ctx); }
      catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not remove address' }); }
    }));
    const routes = Array.isArray(network.routes) ? network.routes : [];
    document.getElementById('routes-body').innerHTML = routes.length ? `<div class="table-wrap"><table class="data compact"><thead><tr><th>Destination</th><th>Gateway</th><th>Device</th><th>Protocol</th></tr></thead><tbody>${routes.map((route) => `<tr><td class="mono">${esc(route.dst || 'default')}</td><td class="mono">${esc(route.gateway || '—')}</td><td class="mono">${esc(route.dev || '—')}</td><td>${esc(route.protocol || '—')}</td></tr>`).join('')}</tbody></table></div>` : '<p class="muted">No routes returned.</p>';
  };

  const paintTime = () => {
    const host = document.getElementById('time-body');
    host.innerHTML = `<div class="diagnostic-summary">${ui.statusPill(time.synchronized === 'yes' ? 'active' : 'warning', time.synchronized === 'yes' ? 'Synchronized' : 'Not synchronized')}<span class="summary-pill">${ui.icon('clock', 12)} ${esc(time.now || '')}</span></div>
      <form id="time-form" class="form-grid is-single">
        ${ui.field({ label: 'Time zone', name: 'timezone', value: time.timezone || '', required: true, help: 'IANA zone, for example Europe/London or UTC.' })}
        <div class="note-strip">${ui.icon('info', 13)} <span>NTP: ${esc(time.ntp || 'unknown')} · synchronized: ${esc(time.synchronized || 'unknown')}</span></div>
        <div class="form-actions"><span class="form-note">Changing the time zone affects log timestamps and cron interpretation.</span><div class="form-actions-right"><button class="btn btn-secondary" type="button" id="ntp-sync" ${canManage ? '' : 'disabled'}>${ui.icon('refresh', 13)} Force NTP step</button><button class="btn btn-primary" type="submit" ${canManage ? '' : 'disabled'}>Save time zone</button></div></div>
      </form>`;
    document.getElementById('time-form')?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(event.currentTarget);
      try { await ctx.api('/control/api/server/time', { method: 'PUT', body: { timezone: values.timezone } }); ui.toast('Time zone updated.', 'good'); await renderServer(ctx); }
      catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not change time zone' }); }
    });
    document.getElementById('ntp-sync')?.addEventListener('click', async (event) => {
      const button = event.currentTarget;
      button.classList.add('is-loading'); button.disabled = true;
      try { const result = await ctx.api('/control/api/server/time/sync', { method: 'POST' }); ui.toast(result.output ? `NTP step: ${result.output}` : 'NTP step requested.', 'good'); await renderServer(ctx); }
      catch (error) { ui.toast(errorText(error), 'critical', { title: 'NTP sync failed' }); }
      finally { button.classList.remove('is-loading'); button.disabled = false; }
    });
  };

  const load = async () => {
    try {
      const [settingsPayload, networkPayload, timePayload] = await Promise.all([
        ctx.api('/control/api/server/settings'),
        ctx.api('/control/api/server/network'),
        ctx.api('/control/api/server/time'),
      ]);
      if (!ctx.isCurrent()) return;
      settings = settingsPayload.settings || {};
      hostname = settingsPayload.hostname || '';
      network = networkPayload || {};
      managed = Array.isArray(networkPayload.managedAddresses) ? networkPayload.managedAddresses : [];
      time = timePayload || {};
      paint();
      paintNetwork();
      paintTime();
      document.getElementById('host-details').innerHTML = `<div class="tile-link"><span>${ui.icon('server', 15)} Kernel hostname</span><span class="value mono">${esc(hostname)}</span></div><div class="tile-link"><span>${ui.icon('globe', 15)} Mail hostname</span><span class="value mono">${esc(settings['server.mail_hostname'] || 'not set')}</span></div><div class="tile-link"><span>${ui.icon('calendar', 15)} Time zone</span><span class="value mono">${esc(time.timezone || 'unknown')}</span></div><div class="tile-link"><span>${ui.icon('clock', 15)} NTP</span><span class="value">${esc(time.ntp || 'unknown')}</span></div>`;
    } catch (error) {
      document.getElementById('server-settings').innerHTML = ui.inlineNotice('critical', 'Could not load server settings', errorText(error));
    }
  };

  document.getElementById('address-add')?.addEventListener('click', () => {
    const modal = ctx.modal({
      title: 'Add IP address', size: 'medium',
      body: `<form id="address-form"><div class="form-grid">
        ${ui.field({ label: 'Interface', name: 'interface', placeholder: 'eth0', required: true, help: 'Must already exist.' })}
        ${ui.field({ label: 'IP address', name: 'address', placeholder: '203.0.113.20', required: true })}
        ${ui.field({ label: 'Prefix length', name: 'prefix', type: 'number', value: '32', min: 1, max: 128, help: '32 for IPv4 host, 128 for IPv6 host.' })}
        ${ui.field({ label: 'Label', name: 'label', placeholder: 'secondary outbound' })}
      </div><div class="notice tone-warning mt-3">${ui.icon('alert', 16)}<div><strong>Runtime only</strong><p>The address becomes active immediately but is not written to persistent network configuration. Add it to the host's netplan/networkd config if it must survive reboot.</p></div></div><div class="form-actions"><span class="form-note">The panel records the addition for inventory.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">Add address</button></div></div></form>`,
    });
    const form = modal.root.querySelector('#address-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      try {
        const result = await ctx.api('/control/api/server/addresses', { method: 'POST', body: { interface: values.interface, address: values.address, prefix: Number(values.prefix), label: values.label || '' } });
        modal.close();
        ui.toast(result.note || 'Address added.', 'good');
        await renderServer(ctx);
      } catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not add address' }); }
    });
  });
  document.getElementById('connectivity-form')?.addEventListener('submit', async (event) => {
    event.preventDefault();
    const values = ui.formValues(event.currentTarget);
    const host = document.getElementById('connectivity-result');
    host.innerHTML = ui.loadingState('Testing connection…', 1);
    try {
      const result = await ctx.api('/control/api/server/connectivity-test', { method: 'POST', body: { host: values.host, port: Number(values.port), timeoutSeconds: Number(values.timeout || 5) } });
      host.innerHTML = `<div class="notice ${result.ok ? 'tone-good' : 'tone-critical'}" style="margin-top:10px">${ui.icon(result.ok ? 'check-circle' : 'alert-circle', 16)}<div><strong>${result.ok ? 'Connection succeeded' : 'Connection failed'}</strong><p>${result.ok ? `${esc(result.host)}:${result.port} in ${ui.num(result.elapsedMs)} ms` : esc(result.error || '')}</p></div></div>`;
    } catch (error) { host.innerHTML = ui.inlineNotice('critical', 'Connectivity test failed', errorText(error)); }
  });
  document.getElementById('server-refresh')?.addEventListener('click', () => renderServer(ctx));
  await load();
}

/* =============================================================== firewall */

export async function renderFirewall(ctx) {
  const canManage = ctx.can('server.manage');
  let firewall = { backend: 'none', raw: '' };
  ctx.setTitle('Firewall', 'Infrastructure');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Infrastructure', title: 'Firewall',
    description: 'Inspect and manage the host firewall. The panel supports ufw rule management and firewalld port additions.',
    actions: `<button class="btn btn-secondary" type="button" id="firewall-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    <div class="grid grid-main">
      <section class="card"><header class="card-head"><div><h3>Firewall state</h3><p id="firewall-summary">Loading firewall…</p></div><div class="card-actions" id="firewall-state-action"></div></header><div class="card-body" id="firewall-body">${ui.loadingState('Reading firewall state…', 4)}</div></section>
      <div class="stack">
        <section class="card"><header class="card-head"><div><h3>Add rule</h3><p>Applies immediately</p></div></header><div class="card-body">
          <form id="firewall-form" class="form-grid is-single">
            ${ui.field({ label: 'Port', name: 'port', type: 'number', min: 1, max: 65535, required: true, placeholder: '587' })}
            ${ui.field({ label: 'Protocol', name: 'proto', type: 'select', value: 'tcp', options: [{ value: 'tcp', label: 'TCP' }, { value: 'udp', label: 'UDP' }] })}
            ${ui.field({ label: 'Source address', name: 'from', placeholder: '203.0.113.0 (optional)', help: 'Restrict the rule to a single source IP. Leave blank for any source.' })}
            ${ui.switchField({ label: 'Deny instead of allow', name: 'deny' })}
            <div class="form-actions"><span class="form-note">Open the port in your upstream provider firewall too.</span><div class="form-actions-right"><button class="btn btn-primary" type="submit" ${canManage ? '' : 'disabled'}>Add rule</button></div></div>
          </form>
        </div></section>
        <section class="card"><header class="card-head"><div><h3>Mail ports checklist</h3></div></header><div class="card-body">
          <div class="tile-link"><span>${ui.icon('mail', 15)} SMTP</span><span class="value mono">25/tcp</span></div>
          <div class="tile-link"><span>${ui.icon('send', 15)} Submission</span><span class="value mono">587/tcp</span></div>
          <div class="tile-link"><span>${ui.icon('lock', 15)} SMTPS</span><span class="value mono">465/tcp</span></div>
          <div class="tile-link"><span>${ui.icon('inbox', 15)} IMAP / IMAPS</span><span class="value mono">143, 993/tcp</span></div>
          <div class="tile-link"><span>${ui.icon('archive', 15)} POP3 / POP3S</span><span class="value mono">110, 995/tcp</span></div>
          <div class="tile-link"><span>${ui.icon('dashboard', 15)} Control</span><span class="value mono">8808/tcp</span></div>
        </div></section>
      </div>
    </div>`;

  const ufwActive = () => /Status:\s*active/i.test(firewall.raw || '');
  const parseUfw = () => {
    const rules = [];
    for (const line of String(firewall.raw || '').split('\n')) {
      const match = line.match(/^\[\s*(\d+)\]\s+(.+?)\s{2,}(ALLOW|DENY|REJECT|LIMIT)\s+(IN|OUT)\s+(.*)$/);
      if (match) rules.push({ number: Number(match[1]), to: match[2].trim(), action: match[3], direction: match[4], from: match[5].trim() });
      else {
        const fallback = line.match(/^\[\s*(\d+)\]\s+(.+)$/);
        if (fallback) rules.push({ number: Number(fallback[1]), to: fallback[2].trim(), action: '—', direction: '—', from: '—' });
      }
    }
    return rules;
  };

  const paint = () => {
    const rules = parseUfw();
    const summary = document.getElementById('firewall-summary');
    const body = document.getElementById('firewall-body');
    const stateAction = document.getElementById('firewall-state-action');
    summary.textContent = firewall.backend === 'ufw' ? `${ufwActive() ? 'Active' : 'Inactive'} · ${rules.length} numbered rule${rules.length === 1 ? '' : 's'}` : firewall.backend === 'firewalld' ? 'firewalld backend' : 'No supported firewall manager';
    stateAction.innerHTML = firewall.backend === 'ufw' && canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="firewall-toggle">${ui.icon('power', 13)} ${ufwActive() ? 'Disable' : 'Enable'} firewall</button>` : '';
    if (firewall.backend === 'ufw' && rules.length) {
      body.innerHTML = `<div class="table-wrap"><table class="data compact"><thead><tr><th class="num">#</th><th>To</th><th>Action</th><th>Direction</th><th>From</th><th></th></tr></thead><tbody>${rules.map((rule) => `<tr><td class="num mono">${rule.number}</td><td class="mono">${esc(rule.to)}</td><td>${ui.badge(rule.action, rule.action === 'ALLOW' ? 'good' : rule.action === 'DENY' || rule.action === 'REJECT' ? 'critical' : 'warning')}</td><td>${esc(rule.direction)}</td><td class="mono">${esc(rule.from)}</td><td class="actions">${canManage ? `<button class="icon-btn is-danger" type="button" data-firewall-delete="${rule.number}" title="Delete rule" aria-label="Delete rule">${ui.icon('trash', 14)}</button>` : ''}</td></tr>`).join('')}</tbody></table></div>`;
    } else {
      body.innerHTML = ui.codeBlock(firewall.raw || 'No firewall output', { language: firewall.backend === 'ufw' ? 'ufw status' : 'firewall', maxHeight: '52vh' });
    }
    document.getElementById('firewall-toggle')?.addEventListener('click', async () => {
      const enable = !ufwActive();
      const ok = await ctx.confirm({ title: `${enable ? 'Enable' : 'Disable'} firewall`, message: enable ? 'Enable ufw now using the existing rule set? Ensure the control port is allowed before disconnecting.' : 'Disable the host firewall? All ports become reachable according to the host network policy. This is high risk.', confirmLabel: `${enable ? 'Enable' : 'Disable'} firewall`, tone: 'danger' });
      if (!ok) return;
      try { await ctx.api('/control/api/server/firewall/state', { method: 'POST', body: { enabled: enable } }); ui.toast(`Firewall ${enable ? 'enabled' : 'disabled'}.`, enable ? 'good' : 'warning'); await load(); }
      catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not change firewall state' }); }
    });
    body.querySelectorAll('[data-firewall-delete]').forEach((button) => button.addEventListener('click', async () => {
      const ok = await ctx.confirm({ title: `Delete firewall rule #${button.dataset.firewallDelete}`, message: 'Remove this numbered ufw rule immediately?', confirmLabel: 'Delete rule', tone: 'danger' });
      if (!ok) return;
      try { await ctx.api(`/control/api/server/firewall/${button.dataset.firewallDelete}`, { method: 'DELETE' }); ui.toast('Firewall rule deleted.', 'good'); await load(); }
      catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not delete rule' }); }
    }));
  };

  const load = async () => {
    try {
      firewall = await ctx.api('/control/api/server/firewall');
      if (!ctx.isCurrent()) return;
      paint();
    } catch (error) {
      document.getElementById('firewall-body').innerHTML = ui.inlineNotice('critical', 'Could not read firewall state', errorText(error));
    }
  };

  document.getElementById('firewall-form')?.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (!canManage) return;
    const form = event.currentTarget;
    const values = ui.formValues(form);
    const button = form.querySelector('button[type="submit"]');
    button.classList.add('is-loading'); button.disabled = true;
    try {
      const result = await ctx.api('/control/api/server/firewall', { method: 'POST', body: { port: Number(values.port), proto: values.proto, from: values.from || '', deny: !!values.deny } });
      ui.toast(result.output || 'Firewall rule added.', 'good');
      form.reset();
      await load();
    } catch (error) {
      ui.toast(errorText(error), 'critical', { title: 'Could not add firewall rule' });
      button.classList.remove('is-loading'); button.disabled = false;
    }
  });
  document.getElementById('firewall-refresh')?.addEventListener('click', load);
  await load();
}

/* =============================================================== database */

export async function renderDatabase(ctx) {
  const canManage = ctx.can('database.manage');
  let status = null;
  let migrations = [];
  let timer = null;
  ctx.setTitle('Database', 'Infrastructure');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Infrastructure', title: 'Database',
    description: 'PostgreSQL health, connections, cache efficiency, provisioning, connection tests and background database copies.',
    actions: `${canManage ? `<button class="btn btn-secondary" type="button" id="db-provision">${ui.icon('plus', 14)} Provision role</button><button class="btn btn-soft-danger" type="button" id="db-restart">${ui.icon('rotate', 14)} Restart PostgreSQL</button>` : ''}<button class="btn btn-secondary" type="button" id="db-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    <div id="db-summary">${ui.loadingState('Reading PostgreSQL status…', 3)}</div>
    <div class="grid grid-main mt-4">
      <div class="stack">
        <section class="card"><header class="card-head"><div><h3>Databases</h3><p>Size and connection distribution</p></div></header><div class="card-body" id="db-databases">${ui.loadingState('Loading databases…', 3)}</div></section>
        <section class="card"><header class="card-head"><div><h3>Active queries</h3><p>Non-idle backends excluding this panel</p></div><button class="btn btn-secondary btn-sm" type="button" id="db-activity-refresh">${ui.icon('refresh', 12)} Refresh</button></header><div class="card-body" id="db-activity">${ui.loadingState('Loading activity…', 3)}</div></section>
        <section class="card"><header class="card-head"><div><h3>Background database copies</h3><p>pg_dump → pg_restore into another database</p></div></header><div class="card-body" id="db-migrations">${ui.loadingState('Loading copies…', 3)}</div></section>
      </div>
      <div class="stack">
        <section class="card"><header class="card-head"><div><h3>Connection test</h3><p>Verify credentials and TLS mode</p></div></header><div class="card-body">
          <form id="db-test-form" class="form-grid is-single">
            ${ui.field({ label: 'Host', name: 'host', value: '127.0.0.1', required: true })}
            ${ui.field({ label: 'Port', name: 'port', type: 'number', value: '5432', min: 1, max: 65535 })}
            ${ui.field({ label: 'Database', name: 'database', value: 'a8m', required: true })}
            ${ui.field({ label: 'User', name: 'user', value: 'a8m', required: true })}
            ${ui.field({ label: 'Password', name: 'password', type: 'password', autocomplete: 'current-password' })}
            ${ui.field({ label: 'SSL mode', name: 'sslmode', type: 'select', value: 'disable', options: [{ value: 'disable', label: 'disable' }, { value: 'require', label: 'require' }, { value: 'verify-full', label: 'verify-full' }] })}
            <div class="form-actions"><span class="form-note">The password is sent to this endpoint over TLS and not stored.</span><div class="form-actions-right"><button class="btn btn-secondary" type="submit">Test connection</button></div></div>
          </form>
          <div id="db-test-result"></div>
        </div></section>
        <section class="card"><header class="card-head"><div><h3>Start a database copy</h3><p>Copy the live a8m database into a target</p></div></header><div class="card-body">
          <form id="db-migrate-form" class="form-grid is-single">
            ${ui.field({ label: 'Target database', name: 'targetDatabase', placeholder: 'a8m_copy', required: true, help: 'The target must already exist and must not be a8m. Provision a role/database first if needed.' })}
            <div class="form-actions"><span class="form-note">Runs in the background with progress and a log.</span><div class="form-actions-right"><button class="btn btn-primary" type="submit" ${canManage ? '' : 'disabled'}>Start copy</button></div></div>
          </form>
        </div></section>
        <section class="card"><header class="card-head"><div><h3>Performance</h3><p>Cache and connection health</p></div></header><div class="card-body" id="db-performance">${ui.loadingState('Loading performance…', 2)}</div></section>
      </div>
    </div>`;

  const paint = () => {
    if (!status) return;
    const data = status;
    const connPercent = data.maxConnections ? (data.connections / data.maxConnections) * 100 : 0;
    document.getElementById('db-summary').innerHTML = `<div class="stat-grid">
      ${ui.statTile({ label: 'PostgreSQL', value: data.service === 'active' ? 'Online' : data.service || 'Unknown', detail: ui.truncate(data.version || '', 46), iconName: 'database', tone: data.service === 'active' ? 'good' : 'critical' })}
      ${ui.statTile({ label: 'Database size', value: ui.bytes(data.databaseSizeBytes), detail: 'a8m database', iconName: 'archive', tone: 'info' })}
      ${ui.statTile({ label: 'Connections', value: `${ui.num(data.connections)}/${ui.num(data.maxConnections)}`, detail: `${ui.pct(connPercent)} of max_connections`, iconName: 'users', tone: connPercent > 85 ? 'warning' : 'good' })}
      ${ui.statTile({ label: 'Cache hit', value: ui.pct(data.cacheHitPercent, 2), detail: 'Shared buffer hit ratio', iconName: 'activity', tone: data.cacheHitPercent >= 99 ? 'good' : data.cacheHitPercent >= 95 ? 'warning' : 'critical' })}
    </div>`;
    document.getElementById('db-performance').innerHTML = `${ui.meter({ label: 'Connection capacity', value: connPercent, detail: `${ui.num(data.connections)} of ${ui.num(data.maxConnections)} connections in use` })}<div style="height:14px"></div>${ui.meter({ label: 'Cache hit ratio', value: data.cacheHitPercent, tone: data.cacheHitPercent >= 99 ? 'good' : data.cacheHitPercent >= 95 ? 'warning' : 'critical', detail: `${ui.pct(data.cacheHitPercent, 2)} of block reads served from cache` })}<div class="divider"></div>${ui.kv([['Version', `<span class="mono break-all">${esc(ui.truncate(data.version || '—', 80))}</span>`], ['Service state', ui.statusPill(data.service || 'unknown')]])}`;
    document.getElementById('db-databases').innerHTML = Array.isArray(data.databases) && data.databases.length ? `<div class="table-wrap"><table class="data compact"><thead><tr><th>Database</th><th class="num">Size</th><th class="num">Connections</th></tr></thead><tbody>${data.databases.map((db) => `<tr><td class="mono">${esc(db.name)}</td><td class="num">${ui.bytes(db.sizeBytes)}</td><td class="num">${ui.num(db.connections)}</td></tr>`).join('')}</tbody></table></div>` : '<p class="muted">No databases returned.</p>';
    document.getElementById('db-activity').innerHTML = Array.isArray(data.activity) && data.activity.length ? `<div class="table-wrap"><table class="data compact"><thead><tr><th>PID</th><th>User / DB</th><th>State</th><th class="num">Seconds</th><th>Query</th></tr></thead><tbody>${data.activity.map((row) => `<tr><td class="mono">${ui.num(row.pid)}</td><td><span class="mono">${esc(row.user)}</span><span class="cell-sub">${esc(row.database)}</span></td><td>${esc(row.state)}</td><td class="num">${ui.num(row.seconds)}</td><td class="mono break-all">${esc(ui.truncate(row.query, 120))}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No active queries', 'All backends are idle or this panel is the only connection.', '', 'database');
  };

  const paintMigrations = () => {
    const host = document.getElementById('db-migrations');
    if (!host) return;
    host.innerHTML = migrations.length ? `<div class="table-wrap"><table class="data compact"><thead><tr><th>Target</th><th>Status</th><th style="min-width:130px">Progress</th><th>Created</th><th></th></tr></thead><tbody>${migrations.map((item) => `<tr><td class="mono">${esc(item.target)}</td><td>${ui.statusPill(item.status)}</td><td><div class="progress-row"><div class="progress tone-${item.status === 'failed' ? 'critical' : item.status === 'completed' ? 'good' : 'info'}"><span style="width:${Math.max(0, Math.min(100, Number(item.progress) || 0))}%"></span></div><span class="progress-label">${ui.num(item.progress)}%</span></div></td><td>${esc(fmt.timeAgo(item.createdAt))}</td><td class="actions"><button class="icon-btn" type="button" data-migration-log="${esc(item.id)}" title="View log" aria-label="View log">${ui.icon('eye', 14)}</button></td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No database copies', 'Start a copy when you need a consistent snapshot in another database.', '', 'database');
    host.querySelectorAll('[data-migration-log]').forEach((button) => button.addEventListener('click', () => {
      const item = migrations.find((row) => row.id === button.dataset.migrationLog);
      if (!item) return;
      ctx.modal({ title: `Database copy · ${item.target}`, size: 'large', body: `<div class="diagnostic-summary">${ui.statusPill(item.status)}<span class="summary-pill">${ui.num(item.progress)}%</span><span class="summary-pill">${ui.icon('clock', 12)} ${esc(fmt.timeAgo(item.createdAt))}</span></div>${ui.codeBlock(item.log || 'No log output yet.', { language: 'pg_dump / pg_restore', maxHeight: '52vh' })}` });
    }));
    if (migrations.some((item) => ['queued', 'running'].includes(item.status)) && !timer) {
      timer = setInterval(() => { if (ctx.isCurrent()) loadMigrations(true); }, 5000);
      ctx.onCleanup(() => { if (timer) { clearInterval(timer); timer = null; } });
    }
    if (!migrations.some((item) => ['queued', 'running'].includes(item.status)) && timer) {
      clearInterval(timer); timer = null;
    }
  };

  const loadStatus = async () => {
    try {
      status = await ctx.api('/control/api/database');
      if (!ctx.isCurrent()) return;
      paint();
    } catch (error) { document.getElementById('db-summary').innerHTML = ui.inlineNotice('critical', 'Could not load database status', errorText(error)); }
  };

  const loadMigrations = async (silent = false) => {
    try {
      const payload = await ctx.api('/control/api/database/migrate');
      if (!ctx.isCurrent()) return;
      migrations = Array.isArray(payload.migrations) ? payload.migrations : [];
      paintMigrations();
    } catch (error) {
      if (!silent) document.getElementById('db-migrations').innerHTML = ui.inlineNotice('critical', 'Could not load database copies', errorText(error));
    }
  };

  document.getElementById('db-provision')?.addEventListener('click', () => {
    const modal = ctx.modal({
      title: 'Provision PostgreSQL role and database', size: 'medium',
      body: `<form id="db-provision-form"><div class="form-grid is-single">
        ${ui.field({ label: 'Role name', name: 'role', placeholder: 'a8m_reporting', required: true, help: 'Letters, digits, underscores. Must not already exist.' })}
        ${ui.field({ label: 'Database name', name: 'database', placeholder: 'a8m_reporting', required: true, help: 'Created with the new role as owner.' })}
      </div><div class="notice tone-warning mt-3">${ui.icon('alert', 16)}<div><strong>The generated password is shown once</strong><p>It is not stored in the panel database. Copy it into the consuming application's secret store.</p></div></div><div class="form-actions"><span class="form-note">Runs as the local postgres superuser via runuser.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">Provision</button></div></div></form>`,
    });
    const form = modal.root.querySelector('#db-provision-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        const result = await ctx.api('/control/api/database/provision', { method: 'POST', body: { role: values.role, database: values.database } });
        modal.close();
        ui.toast(`Database ${result.database} provisioned.`, 'good');
        ctx.modal({ title: 'Database role created', size: 'small', body: `<div class="notice tone-warning">${ui.icon('key', 16)}<div><strong>Copy the password now</strong><p>${esc(result.message || '')}</p></div></div><div class="field" style="margin-top:14px"><label>Password for ${esc(result.role)}</label><div class="input-group"><input class="input mono" readonly value="${ui.attr(result.password)}" id="db-password"><button class="btn btn-secondary" type="button" data-copy="${ui.attr(result.password)}">${ui.icon('copy', 14)} Copy</button></div></div>`, footer: `<button class="btn btn-primary" type="button" data-modal-close>I have saved it</button>` });
        document.getElementById('db-password')?.select();
        await loadStatus();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Provisioning failed' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  });

  document.getElementById('db-restart')?.addEventListener('click', async () => {
    const ok = await ctx.confirm({ title: 'Restart PostgreSQL', message: 'Restart postgresql@15-main? Every webmail, control and mail authentication connection is interrupted while it restarts.', confirmLabel: 'Restart PostgreSQL', tone: 'danger', requireText: 'RESTART' });
    if (!ok) return;
    try { await ctx.api('/control/api/database/restart', { method: 'POST', body: { confirm: 'RESTART' } }); ui.toast('PostgreSQL restarted.', 'good'); await loadStatus(); }
    catch (error) { ui.toast(errorText(error), 'critical', { title: 'Restart failed' }); }
  });

  document.getElementById('db-test-form')?.addEventListener('submit', async (event) => {
    event.preventDefault();
    const form = event.currentTarget;
    const values = ui.formValues(form);
    const host = document.getElementById('db-test-result');
    host.innerHTML = ui.loadingState('Testing connection…', 1);
    try {
      const result = await ctx.api('/control/api/database/test', { method: 'POST', body: { host: values.host, port: Number(values.port), database: values.database, user: values.user, password: values.password || '', sslmode: values.sslmode } });
      host.innerHTML = result.ok ? `<div class="notice tone-good" style="margin-top:10px">${ui.icon('check-circle', 16)}<div><strong>Connected in ${ui.num(result.elapsedMs)} ms</strong><p class="mono break-all">${esc(ui.truncate(result.version || '', 110))}</p></div></div>` : `<div class="notice tone-critical" style="margin-top:10px">${ui.icon('alert-circle', 16)}<div><strong>Connection failed</strong><p class="wrap-anywhere">${esc(result.error || '')}</p></div></div>`;
    } catch (error) { host.innerHTML = ui.inlineNotice('critical', 'Connection test failed', errorText(error)); }
  });

  document.getElementById('db-migrate-form')?.addEventListener('submit', async (event) => {
    event.preventDefault();
    const values = ui.formValues(event.currentTarget);
    const button = event.currentTarget.querySelector('button[type="submit"]');
    button.classList.add('is-loading'); button.disabled = true;
    try {
      await ctx.api('/control/api/database/migrate', { method: 'POST', body: { targetDatabase: values.targetDatabase } });
      ui.toast('Database copy queued.', 'good');
      event.currentTarget.reset();
      await loadMigrations();
    } catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not start database copy' }); }
    finally { button.classList.remove('is-loading'); button.disabled = false; }
  });

  document.getElementById('db-refresh')?.addEventListener('click', async () => { await Promise.all([loadStatus(), loadMigrations()]); });
  document.getElementById('db-activity-refresh')?.addEventListener('click', loadStatus);
  await Promise.all([loadStatus(), loadMigrations()]);
}

/* =============================================================== software updates */

export async function renderUpdates(ctx) {
  const canManage = ctx.can('server.manage');
  let data = null;
  let pollTimer = null;
  let currentJob = '';
  ctx.setTitle('Software updates', 'Infrastructure');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Infrastructure',
    title: 'Software updates',
    description: 'Review operating-system package updates, apply selected packages and follow the command output live.',
    actions: `${canManage ? `<button class="btn btn-primary" type="button" id="updates-all">${ui.icon('download', 14)} Update all</button><button class="btn btn-secondary" type="button" id="updates-check">${ui.icon('refresh', 14)} Check for updates</button>` : ''}<button class="btn btn-secondary" type="button" id="updates-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    <div id="updates-kpis">${ui.loadingState('Reading the package index…', 3)}</div>
    <div class="grid grid-main mt-4">
      <section class="card">
        <header class="card-head"><div><h3>Available updates</h3><p id="updates-summary">Loading package list…</p></div><div class="card-actions">${canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="updates-selected" disabled>${ui.icon('download', 13)} Update selected</button>` : ''}</div></header>
        <div class="card-body" id="updates-body">${ui.loadingState('Loading packages…', 5)}</div>
      </section>
      <div class="stack">
        <section class="card" id="updates-terminal-card" hidden>
          <header class="terminal-card-head"><div class="terminal-dots" aria-hidden="true"><span></span><span></span><span></span></div><div class="terminal-card-title"><strong>apt-get</strong><span id="updates-terminal-sub">Waiting for output</span></div><span class="terminal-state" id="updates-terminal-state">queued</span></header>
          <div class="terminal-shell" id="updates-terminal-shell"><pre class="terminal-output" id="updates-terminal"></pre></div>
          <div class="terminal-card-foot"><span class="terminal-hint">${ui.icon('shield', 13)} Root-only operation · audited and notification-enabled</span><button class="btn btn-ghost btn-sm" type="button" id="updates-terminal-clear">Clear view</button></div>
        </section>
        <section class="card">
          <header class="card-head"><div><h3>Maintenance notes</h3><p>Before you update the mail stack</p></div></header>
          <div class="card-body">
            <div class="tile-link"><span>${ui.icon('server', 15)} Services</span><a class="value" href="#/services">Inspect units</a></div>
            <div class="tile-link"><span>${ui.icon('activity', 15)} Diagnostics</span><a class="value" href="#/diagnostics">Run checks</a></div>
            <div class="tile-link"><span>${ui.icon('archive', 15)} Backups</span><a class="value" href="#/backups">Open backups</a></div>
            <div class="note-strip" style="margin-top:11px">${ui.icon('info', 13)} <span>Package updates are applied with non-interactive dpkg configuration and no automatic service restart. Reboot when the indicator says the kernel or a core library requires it.</span></div>
          </div>
        </section>
      </div>
    </div>`;

  const setTerminalVisible = (visible) => {
    const card = document.getElementById('updates-terminal-card');
    if (card) card.hidden = !visible;
  };

  const load = async () => {
    const kpis = document.getElementById('updates-kpis');
    const body = document.getElementById('updates-body');
    try {
      data = await ctx.api('/control/api/infra/updates');
      if (!ctx.isCurrent()) return;
      const packages = Array.isArray(data.packages) ? data.packages : [];
      const security = Number(data.security) || 0;
      kpis.innerHTML = `<div class="stat-grid">
        ${ui.statTile({ label: 'Available', value: ui.num(packages.length), detail: 'Installed packages with a newer candidate', iconName: 'download', tone: packages.length ? 'info' : 'good' })}
        ${ui.statTile({ label: 'Security', value: ui.num(security), detail: security ? 'Security repository updates' : 'No security updates pending', iconName: 'shield-check', tone: security ? 'critical' : 'good' })}
        ${ui.statTile({ label: 'Reboot', value: data.rebootRequired ? 'Required' : 'Not required', detail: data.rebootRequired ? 'A package requested a reboot' : 'No reboot flag present', iconName: 'server', tone: data.rebootRequired ? 'warning' : 'good' })}
        ${ui.statTile({ label: 'Last checked', value: data.lastChecked ? fmt.timeAgo(data.lastChecked) : 'Not recorded', detail: 'Package index refresh', iconName: 'clock', tone: 'neutral' })}
      </div>`;
      document.getElementById('updates-summary').textContent = packages.length ? `${packages.length} package${packages.length === 1 ? '' : 's'} available · ${security} security · selected updates are applied one command at a time` : 'The installed package set is current.';
      const selectedButton = document.getElementById('updates-selected');
      const allButton = document.getElementById('updates-all');
      if (allButton) allButton.disabled = !packages.length;
      if (!packages.length) {
        body.innerHTML = ui.emptyState('Everything is up to date', 'No installed package has a newer candidate in the configured repositories.', canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="updates-check-empty">${ui.icon('refresh', 13)} Check again</button>` : '', 'check-circle');
        if (selectedButton) selectedButton.disabled = true;
        document.getElementById('updates-check-empty')?.addEventListener('click', checkForUpdates);
        return;
      }
      body.innerHTML = `<div class="table-wrap is-bare"><table class="data"><thead><tr><th class="check-col"><input type="checkbox" id="updates-select-all" aria-label="Select all packages"></th><th>Package</th><th>Installed</th><th>Available</th><th>Repository</th><th>Type</th></tr></thead><tbody>
        ${packages.map((pkg) => `<tr><td class="check-col"><input type="checkbox" name="update-package" value="${ui.attr(pkg.name)}" aria-label="Select ${ui.attr(pkg.name)}"></td><td><span class="cell-main mono">${esc(pkg.name)}</span><span class="cell-sub">${esc(pkg.arch || '')}</span></td><td class="mono">${esc(pkg.current || '—')}</td><td class="mono">${esc(pkg.candidate || '—')}</td><td><span class="mono">${esc(pkg.repository || '—')}</span></td><td>${pkg.security ? ui.badge('Security', 'critical', 'shield-alert') : ui.badge('Standard', 'neutral')}</td></tr>`).join('')}</tbody></table></div>`;
      const syncSelected = () => {
        if (!selectedButton) return;
        selectedButton.disabled = !Array.from(body.querySelectorAll('input[name="update-package"]:checked')).length;
      };
      body.querySelectorAll('input[name="update-package"]').forEach((input) => input.addEventListener('change', syncSelected));
      document.getElementById('updates-select-all')?.addEventListener('change', (event) => {
        body.querySelectorAll('input[name="update-package"]').forEach((input) => { input.checked = event.target.checked; });
        syncSelected();
      });
    } catch (error) {
      kpis.innerHTML = ui.inlineNotice('critical', 'Could not read package updates', errorText(error));
      body.innerHTML = '';
    }
  };

  const checkForUpdates = async () => {
    const button = document.getElementById('updates-check') || document.getElementById('updates-check-empty');
    if (button) { button.classList.add('is-loading'); button.disabled = true; }
    try {
      const result = await ctx.api('/control/api/infra/updates/check', { method: 'POST' });
      ui.toast(`Package index refreshed · ${result.count} update${result.count === 1 ? '' : 's'} available`, 'good');
      await load();
    } catch (error) {
      ui.toast(errorText(error), 'critical', { title: 'Could not check for updates' });
    } finally {
      if (button) { button.classList.remove('is-loading'); button.disabled = false; }
    }
  };

  const pollJob = async (jobID) => {
    if (!ctx.isCurrent()) return;
    try {
      const job = await ctx.api(`/control/api/infra/updates/jobs/${jobID}`);
      if (!ctx.isCurrent()) return;
      setTerminalVisible(true);
      const output = document.getElementById('updates-terminal');
      if (output) {
        output.textContent = job.output || 'Waiting for apt-get…';
        const shell = document.getElementById('updates-terminal-shell');
        if (shell) shell.scrollTop = shell.scrollHeight;
      }
      const state = document.getElementById('updates-terminal-state');
      if (state) {
        state.textContent = job.status;
        state.className = `terminal-state is-${job.status}`;
      }
      const sub = document.getElementById('updates-terminal-sub');
      if (sub) sub.textContent = job.finishedAt ? `Finished · exit ${job.exitCode} · ${ui.num(job.remaining)} remaining` : 'Running · streaming output';
      if (job.status === 'completed' || job.status === 'failed') {
        currentJob = '';
        if (job.status === 'completed') ui.toast('Package update completed.', 'good');
        else ui.toast(job.error || 'Package update failed.', 'critical', { title: 'Update failed' });
        await load();
        return;
      }
      pollTimer = setTimeout(() => pollJob(jobID), 1000);
    } catch (error) {
      ui.toast(errorText(error), 'critical', { title: 'Lost the update job' });
    }
  };

  const startUpgrade = async (packages, all) => {
    if (!canManage) return;
    const confirmed = await ctx.confirm({
      title: all ? 'Update all packages' : 'Update selected packages',
      message: all ? 'Run apt-get upgrade for every available package? Mail services stay running; a reboot may be required afterwards.' : `Update ${packages.length} package${packages.length === 1 ? '' : 's'}? The command output will appear in the terminal panel.`,
      confirmLabel: all ? 'Update everything' : 'Update selected',
      tone: all ? 'danger' : 'primary',
      requireText: all ? 'UPDATE' : undefined,
    });
    if (!confirmed) return;
    const output = document.getElementById('updates-terminal');
    if (output) output.textContent = 'Starting apt-get…';
    setTerminalVisible(true);
    const state = document.getElementById('updates-terminal-state');
    if (state) { state.textContent = 'queued'; state.className = 'terminal-state is-queued'; }
    try {
      const result = await ctx.api('/control/api/infra/updates/upgrade', { method: 'POST', body: { packages, all } });
      currentJob = result.jobId;
      ui.toast('Package update started.', 'info');
      pollJob(result.jobId);
    } catch (error) {
      ui.toast(errorText(error), 'critical', { title: 'Could not start the update' });
    }
  };

  document.getElementById('updates-check')?.addEventListener('click', checkForUpdates);
  document.getElementById('updates-refresh')?.addEventListener('click', load);
  document.getElementById('updates-selected')?.addEventListener('click', () => {
    const packages = Array.from(document.querySelectorAll('input[name="update-package"]:checked')).map((input) => input.value);
    if (packages.length) startUpgrade(packages, false);
  });
  document.getElementById('updates-all')?.addEventListener('click', () => startUpgrade([], true));
  document.getElementById('updates-terminal-clear')?.addEventListener('click', () => {
    const output = document.getElementById('updates-terminal');
    if (output) output.textContent = '';
  });
  ctx.onCleanup(() => { if (pollTimer) clearTimeout(pollTimer); });
  await load();
  if (currentJob) pollJob(currentJob);
}

/* =============================================================== terminal */

let terminalAssetPromise = null;
function loadTerminalAsset(src) {
  return new Promise((resolve, reject) => {
    const script = document.createElement('script');
    script.src = src;
    script.onload = resolve;
    script.onerror = () => reject(new Error(`Could not load ${src}`));
    document.head.appendChild(script);
  });
}
function ensureTerminalAssets() {
  if (window.Terminal && window.FitAddon) return Promise.resolve();
  if (!terminalAssetPromise) {
    terminalAssetPromise = Promise.all([
      loadTerminalAsset('/assets/vendor/xterm.js'),
      loadTerminalAsset('/assets/vendor/addon-fit.js'),
    ]);
  }
  return terminalAssetPromise;
}

export async function renderTerminal(ctx) {
  let info = {};
  try {
    info = await ctx.api('/control/api/infra/terminal/info');
  } catch (error) {
    ctx.root.innerHTML = `${ui.pageHeader({ eyebrow: 'Infrastructure', title: 'Terminal', description: 'A local SSH shell for system administration.' })}<div class="card"><div class="card-body">${ui.inlineNotice('critical', 'Could not inspect the SSH listener', errorText(error))}</div></div>`;
    return;
  }
  const port = info.port || null;
  const passwordAuth = Boolean(info.passwordAuthentication);
  const keyAuth = info.keyAuthentication !== false;
  ctx.setTitle('Terminal', 'Infrastructure');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Infrastructure',
    title: 'Terminal',
    description: 'An authenticated SSH shell on this host. The SSH listener port is detected from the live sshd configuration.',
    actions: `<span class="terminal-port-chip">${ui.icon('plug', 13)} ${port ? `sshd :${esc(String(port))}` : 'SSH port not detected'}</span><a class="btn btn-secondary" href="#/services">${ui.icon('server', 15)} Services</a>`,
  })}
    <div class="terminal-layout">
      <section class="card terminal-connect-card">
        <header class="card-head"><div><h3>Connect</h3><p>Local loopback SSH, no panel password forwarding</p></div><span class="terminal-state ${port ? 'is-connected' : 'is-failed'}">${port ? 'Detected' : 'Blocked'}</span></header>
        <div class="card-body">
          ${port ? '' : `<div class="notice tone-critical" style="margin-bottom:12px">${ui.icon('alert-circle', 16)}<div><strong>SSH port detection failed</strong><p>${esc(info.error || 'Verify sshd is running and its configuration is readable.')}</p></div></div>`}
          <form id="terminal-form">
            <div class="form-grid is-single">
              ${ui.field({ label: 'System account', name: 'username', value: 'root', required: true, autocomplete: 'username', help: 'A local Linux account with a shell, such as root or a named administrator.' })}
              ${ui.field({ label: 'Authentication', name: 'authType', type: 'select', value: keyAuth ? 'key' : 'password', options: [{ value: 'key', label: 'SSH private key' }, { value: 'password', label: 'System password' }], help: passwordAuth ? 'The server accepts both methods.' : 'The live sshd configuration advertises password authentication disabled; use an SSH key.' })}
              <div data-auth-panel="password" ${keyAuth ? 'hidden' : ''}>${ui.field({ label: 'System password', name: 'password', type: 'password', autocomplete: 'current-password', help: 'Sent only over the TLS-protected panel WebSocket, used once for the SSH handshake and never stored.' })}</div>
              <div data-auth-panel="key" ${keyAuth ? '' : 'hidden'}><div class="field"><label for="terminal-key">Private key (PEM or OpenSSH)</label><textarea class="input mono terminal-key-input" id="terminal-key" name="privateKey" rows="6" autocomplete="off" spellcheck="false" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"></textarea><small class="field-help">Paste an unencrypted or passphrase-protected key. It is not written to disk.</small></div></div>
              <div data-auth-panel="key" ${keyAuth ? '' : 'hidden'}>${ui.field({ label: 'Key passphrase (if required)', name: 'passphrase', type: 'password', autocomplete: 'off' })}</div>
            </div>
            <div class="terminal-transport-note">${ui.icon('shield-check', 14)} <span>Loopback SSH · ${port ? `port ${esc(String(port))}` : 'port not detected'} · host-key verification disabled only for 127.0.0.1</span></div>
            <div class="form-actions"><span class="form-note">${esc(info.hostname || '')} · ${passwordAuth ? 'password + key auth' : 'key auth'} advertised by sshd</span><div class="form-actions-right"><button class="btn btn-primary" type="submit" id="terminal-connect" ${port ? '' : 'disabled'}>${ui.icon('terminal', 14)} Open shell</button></div></div>
          </form>
        </div>
      </section>
      <section class="card terminal-window-card">
        <header class="terminal-card-head"><div class="terminal-dots" aria-hidden="true"><span></span><span></span><span></span></div><div class="terminal-card-title"><strong id="terminal-title">SSH terminal</strong><span id="terminal-subtitle">Not connected</span></div><span class="terminal-state is-idle" id="terminal-state">Idle</span><div class="terminal-card-actions"><button class="icon-btn" type="button" id="terminal-clear" title="Clear terminal" aria-label="Clear terminal">${ui.icon('refresh', 14)}</button><button class="icon-btn" type="button" id="terminal-disconnect" title="Disconnect" aria-label="Disconnect" disabled>${ui.icon('close', 14)}</button></div></header>
        <div class="terminal-canvas" id="terminal-canvas"><div class="terminal-boot" id="terminal-boot">${ui.loadingState('Preparing terminal…', 2)}</div></div>
        <div class="terminal-card-foot"><span class="terminal-hint">${ui.icon('info', 13)} <span id="terminal-hint">Connect to start an interactive shell.</span></span><span class="terminal-shortcut">Ctrl+C interrupt · Ctrl+D exit</span></div>
      </section>
    </div>`;

  const form = document.getElementById('terminal-form');
  const authSelect = form?.querySelector('[name="authType"]');
  const toggleAuthPanels = () => {
    const value = authSelect?.value || 'key';
    form?.querySelectorAll('[data-auth-panel]').forEach((panel) => { panel.hidden = panel.dataset.authPanel !== value; });
  };
  authSelect?.addEventListener('change', toggleAuthPanels);
  toggleAuthPanels();

  let term = null;
  let fitAddon = null;
  let socket = null;
  let connected = false;
  const setState = (value, label) => {
    const chip = document.getElementById('terminal-state');
    if (chip) { chip.textContent = label; chip.className = `terminal-state is-${value}`; }
  };
  const setButton = (disabled) => {
    const button = document.getElementById('terminal-connect');
    if (button) { button.disabled = disabled || !port; button.classList.toggle('is-loading', disabled && !connected); }
    const disconnect = document.getElementById('terminal-disconnect');
    if (disconnect) disconnect.disabled = !connected;
  };
  const writeNotice = (text, tone = 'info') => {
    if (!term) return;
    term.writeln(`\r\n\x1b[${tone === 'critical' ? '31' : tone === 'good' ? '32' : '36'}m${text}\x1b[0m`);
  };

  try {
    await ensureTerminalAssets();
    if (!ctx.isCurrent()) return;
    const boot = document.getElementById('terminal-boot');
    if (boot) boot.remove();
    const host = document.getElementById('terminal-canvas');
    term = new window.Terminal({
      cursorBlink: true,
      cursorStyle: 'bar',
      fontFamily: '"Cascadia Code", "SFMono-Regular", ui-monospace, Menlo, Consolas, monospace',
      fontSize: 12.5,
      lineHeight: 1.25,
      letterSpacing: 0,
      scrollback: 8000,
      allowProposedApi: false,
      theme: {
        background: '#08111c', foreground: '#d8e6f5', cursor: '#61aef7', cursorAccent: '#08111c',
        selectionBackground: 'rgba(97,174,247,.28)',
        black: '#0b1622', red: '#f27d82', green: '#57d6a0', yellow: '#e9c46a', blue: '#6cb6ff',
        magenta: '#c59cff', cyan: '#66d9d0', white: '#d8e6f5', brightBlack: '#60788f',
        brightRed: '#ff9aa0', brightGreen: '#7ce8b8', brightYellow: '#f8d98b', brightBlue: '#93ccff',
        brightMagenta: '#d8bcff', brightCyan: '#8cece5', brightWhite: '#ffffff',
      },
    });
    fitAddon = new window.FitAddon.FitAddon();
    term.loadAddon(fitAddon);
    term.open(host);
    fitAddon.fit();
    term.writeln('\x1b[36mAlt8Mail System Control · SSH terminal\x1b[0m');
    term.writeln('Sign in with a local system account. The session is audited; credentials are not stored.\r\n');
    term.onData((data) => {
      if (!connected || !socket || socket.readyState !== WebSocket.OPEN) return;
      socket.send(JSON.stringify({ type: 'input', data }));
    });
    term.onResize(({ rows, cols }) => {
      if (connected && socket && socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: 'resize', rows, cols }));
    });
    ctx.listen(window, 'resize', () => { try { fitAddon.fit(); } catch { /* container hidden */ } });
  } catch (error) {
    const host = document.getElementById('terminal-canvas');
    if (host) host.innerHTML = ui.inlineNotice('critical', 'Terminal renderer unavailable', errorText(error));
    setState('failed', 'Renderer error');
    return;
  }

  const disconnect = () => {
    connected = false;
    try { if (socket) socket.close(); } catch { /* ignore */ }
    socket = null;
    setButton(false);
    setState('idle', 'Disconnected');
    const subtitle = document.getElementById('terminal-subtitle');
    if (subtitle) subtitle.textContent = 'Not connected';
    const hint = document.getElementById('terminal-hint');
    if (hint) hint.textContent = 'Session closed. Connect again when needed.';
  };
  document.getElementById('terminal-disconnect')?.addEventListener('click', disconnect);
  document.getElementById('terminal-clear')?.addEventListener('click', () => { if (term) term.clear(); });

  form?.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (!port || !term) return;
    const values = ui.formValues(form);
    const authType = values.authType || 'key';
    if (authType === 'password' && !values.password) { ui.toast('Enter the system account password.', 'warning'); return; }
    if (authType === 'key' && !values.privateKey?.trim()) { ui.toast('Paste the SSH private key.', 'warning'); return; }
    setButton(true);
    setState('connecting', 'Connecting');
    const subtitle = document.getElementById('terminal-subtitle');
    if (subtitle) subtitle.textContent = `Authenticating ${values.username || 'system account'}…`;
    const hint = document.getElementById('terminal-hint');
    if (hint) hint.textContent = 'Establishing the loopback SSH session…';
    term.writeln(`\x1b[36mConnecting to 127.0.0.1:${port} as ${values.username}…\x1b[0m`);
    try {
      const result = await ctx.api('/control/api/infra/terminal/sessions', {
        method: 'POST',
        body: {
          username: values.username,
          authType,
          password: values.password || '',
          privateKey: values.privateKey || '',
          passphrase: values.passphrase || '',
          rows: term.rows,
          cols: term.cols,
        },
      });
      const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
      const wsUrl = `${scheme}://${location.host}/control/api/infra/terminal/ws?ticket=${encodeURIComponent(result.ticket)}`;
      socket = new WebSocket(wsUrl);
      socket.binaryType = 'arraybuffer';
      socket.onopen = () => {
        connected = true;
        setButton(false);
        setState('connected', 'Connected');
        if (subtitle) subtitle.textContent = `${result.username}@${info.hostname || 'host'} · port ${result.port}`;
        if (hint) hint.textContent = 'Interactive shell · changes are audited.';
        const passwordField = form.querySelector('[name="password"]');
        const keyField = form.querySelector('[name="privateKey"]');
        const passField = form.querySelector('[name="passphrase"]');
        if (passwordField) passwordField.value = '';
        if (keyField) keyField.value = '';
        if (passField) passField.value = '';
        term.focus();
        fitAddon.fit();
        socket.send(JSON.stringify({ type: 'resize', rows: term.rows, cols: term.cols }));
      };
      socket.onmessage = (message) => {
        if (!term) return;
        if (message.data instanceof ArrayBuffer) term.write(new Uint8Array(message.data));
        else term.write(String(message.data));
      };
      socket.onerror = () => { writeNotice('[terminal] WebSocket error', 'critical'); };
      socket.onclose = () => {
        if (connected) {
          connected = false;
          setButton(false);
          setState('idle', 'Closed');
          if (subtitle) subtitle.textContent = 'Session ended';
          if (hint) hint.textContent = 'The SSH session ended.';
          writeNotice('[terminal] connection closed', 'info');
        }
      };
    } catch (error) {
      setButton(false);
      setState('failed', 'Failed');
      if (hint) hint.textContent = 'Authentication failed.';
      term.writeln(`\x1b[31m${String(errorText(error)).replace(/\x1b/g, '')}\x1b[0m`);
      ui.toast(errorText(error), 'critical', { title: 'SSH connection failed' });
    }
  });

  ctx.onCleanup(() => {
    connected = false;
    try { if (socket) socket.close(); } catch { /* ignore */ }
    try { if (term) term.dispose(); } catch { /* ignore */ }
  });
}
