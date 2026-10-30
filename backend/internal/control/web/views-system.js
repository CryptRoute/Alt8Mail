/* System Control — release updates, version history, host reboot tracking. */
import * as ui from './ui.js';

const esc = ui.esc;
const fmt = {
  dateTime: ui.dateTime,
  timeAgo: ui.timeAgo,
  num: ui.num,
  duration: ui.duration,
};

function errorText(error) {
  return error?.message || String(error || 'Unknown error');
}

function actionLabel(action) {
  return ui.humanTitle(String(action || '').replace(/^system\./, '').replace(/_/g, ' '));
}

function jobTone(status) {
  if (status === 'completed') return 'good';
  if (status === 'failed') return 'critical';
  return 'info';
}

function auditTone(action) {
  if (String(action).includes('failed') || String(action).includes('not_observed')) return 'critical';
  if (String(action).includes('completed') || String(action).includes('rebooted')) return 'good';
  if (String(action).includes('requested') || String(action).includes('started')) return 'warning';
  return 'info';
}

export async function renderSystem(ctx) {
  const canManage = ctx.can('server.manage');
  let overview = null;
  let updateJob = null;
  let activeJobId = '';
  let activeJobTimer = null;
  let rebootTimer = null;
  let rebootWatching = false;

  ctx.setTitle('System', 'Infrastructure');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Infrastructure',
    title: 'System',
    description: 'Release updates, version history, host reboot and the state of the machine that runs Alt8Mail.',
    actions: `${canManage ? `<button class="btn btn-secondary" type="button" id="system-check">${ui.icon('refresh', 14)} Check for update</button><button class="btn btn-primary" type="button" id="system-update" disabled>${ui.icon('download', 14)} Install update</button>` : ''}<button class="btn btn-secondary" type="button" id="system-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    <div id="system-kpis">${ui.loadingState('Reading system state…', 4)}</div>
    <div class="grid grid-main mt-4">
      <div class="stack">
        <section class="card">
          <header class="card-head"><div><h3>Alt8Mail release</h3><p>Tag-based updates run through the transactional updater with rollback.</p></div></header>
          <div class="card-body" id="system-release">${ui.loadingState('Reading release state…', 4)}</div>
        </section>
        <section class="card" id="system-console-card" hidden>
          <header class="terminal-card-head"><div class="terminal-dots" aria-hidden="true"><span></span><span></span><span></span></div><div class="terminal-card-title"><strong>Alt8Mail updater</strong><span id="system-console-sub">Waiting for output</span></div><span class="terminal-state" id="system-console-state">queued</span></header>
          <div class="terminal-shell" id="system-console-shell"><pre class="terminal-output" id="system-console-log"></pre></div>
          <div class="terminal-card-foot"><span class="terminal-hint">${ui.icon('shield', 13)} Backup · migrations · health check · rollback on failure</span><button class="btn btn-ghost btn-sm" type="button" id="system-console-clear">Clear view</button></div>
        </section>
        <section class="card">
          <header class="card-head"><div><h3>Release history</h3><p>Installed releases under <span class="mono">/opt/a8m/releases</span></p></div></header>
          <div class="card-body" id="system-releases">${ui.loadingState('Reading releases…', 3)}</div>
        </section>
        <section class="card">
          <header class="card-head"><div><h3>System activity</h3><p>Reboot, update and version audit trail</p></div><a class="btn btn-ghost btn-sm" href="#/access">${ui.icon('shield', 13)} Full audit</a></header>
          <div class="card-body" id="system-activity">${ui.loadingState('Reading audit trail…', 3)}</div>
        </section>
      </div>
      <div class="stack">
        <section class="card">
          <header class="card-head"><div><h3>Host power</h3><p>Restart the machine and watch it return</p></div></header>
          <div class="card-body" id="system-reboot">${ui.loadingState('Reading host state…', 3)}</div>
        </section>
        <section class="card">
          <header class="card-head"><div><h3>Operating system</h3><p>Package updates and reboot requirement</p></div></header>
          <div class="card-body" id="system-os">${ui.loadingState('Reading package state…', 2)}</div>
        </section>
        <section class="card">
          <header class="card-head"><div><h3>Safety notes</h3><p>Before you restart or update</p></div></header>
          <div class="card-body">
            <div class="tile-link"><span>${ui.icon('archive', 15)} Backups</span><a class="value" href="#/backups">Open backups</a></div>
            <div class="tile-link"><span>${ui.icon('server', 15)} Services</span><a class="value" href="#/services">Inspect units</a></div>
            <div class="tile-link"><span>${ui.icon('download', 15)} OS packages</span><a class="value" href="#/updates">Software updates</a></div>
            <div class="note-strip" style="margin-top:11px">${ui.icon('info', 13)} <span>Application updates restart the web, admin and control services. The updater takes a database and configuration backup first and rolls back if readiness checks fail. A host reboot interrupts webmail, IMAP, SMTP and control sessions.</span></div>
          </div>
        </section>
      </div>
    </div>`;

  const setConsoleState = (status, detail) => {
    const card = document.getElementById('system-console-card');
    const state = document.getElementById('system-console-state');
    const sub = document.getElementById('system-console-sub');
    if (card) card.hidden = false;
    if (state) {
      state.textContent = status || 'unknown';
      state.className = `terminal-state is-${status || 'unknown'}`;
    }
    if (sub && detail) sub.textContent = detail;
  };

  const paintConsole = (job) => {
    if (!job) return;
    updateJob = job;
    setConsoleState(job.status, job.status === 'running' || job.status === 'queued' ? 'Running outside the control service · live output' : job.status === 'completed' ? `Completed${job.finishedAt ? ` · ${fmt.timeAgo(job.finishedAt)}` : ''}` : 'Update failed');
    const log = document.getElementById('system-console-log');
    if (log) {
      log.textContent = job.log || 'Waiting for updater output…';
      const shell = document.getElementById('system-console-shell');
      if (shell) shell.scrollTop = shell.scrollHeight;
    }
  };

  const paint = () => {
    if (!overview) return;
    const active = (overview.updateJobs || []).find((job) => job.status === 'queued' || job.status === 'running');
    const pending = overview.pendingReboot;
    const latest = overview.latestVersion || '';
    const updateAvailable = Boolean(overview.updateAvailable);
    const updateButton = document.getElementById('system-update');
    const checkButton = document.getElementById('system-check');
    if (checkButton) checkButton.disabled = !canManage || Boolean(active);
    if (updateButton) {
      updateButton.disabled = !canManage || !updateAvailable || Boolean(active) || Boolean(pending);
      updateButton.innerHTML = `${ui.icon('download', 14)} ${latest ? `Install ${esc(latest.startsWith('v') ? latest : `v${latest}`)}` : 'Install update'}`;
      updateButton.hidden = !updateAvailable;
      updateButton.classList.remove('is-loading');
    }

    document.getElementById('system-kpis').innerHTML = `<div class="stat-grid">
      ${ui.statTile({ label: 'Installed release', value: `v${esc(overview.currentVersion || '—')}`, detail: 'Active release symlink', iconName: 'server', tone: 'info' })}
      ${ui.statTile({ label: 'Latest release', value: latest ? `v${esc(latest)}` : 'Not checked', detail: updateAvailable ? 'Update available' : overview.checkedAt ? `Checked ${fmt.timeAgo(overview.checkedAt)}` : 'Run a release check', iconName: 'download', tone: updateAvailable ? 'warning' : latest ? 'good' : 'neutral' })}
      ${ui.statTile({ label: 'Host uptime', value: fmt.duration(overview.uptimeSeconds), detail: `Booted ${fmt.dateTime(overview.bootedAt)}`, iconName: 'clock', tone: 'good' })}
      ${ui.statTile({ label: 'Reboot', value: pending ? 'Pending' : overview.rebootRequired ? 'Required' : 'Not required', detail: pending ? 'Requested from this panel' : overview.rebootRequired ? 'A package requested it' : 'Host running normally', iconName: 'power', tone: pending || overview.rebootRequired ? 'warning' : 'good' })}
    </div>`;

    const release = document.getElementById('system-release');
    if (release) {
      const last = overview.lastUpdate;
      let notice = '';
      if (active) {
        notice = `<div class="notice tone-info">${ui.icon('download', 16)}<div><strong>${esc(ui.humanTitle(active.status))} ${esc(active.targetVersion || '')}</strong><p>Started by ${esc(active.actorEmail || 'an operator')} · ${esc(fmt.timeAgo(active.requestedAt))}. The control service will restart during the update.</p></div></div>`;
      } else if (pending) {
        notice = `<div class="notice tone-warning">${ui.icon('power', 16)}<div><strong>Reboot pending</strong><p>The host reboot was requested by ${esc(pending.actorEmail || 'an operator')} at ${esc(fmt.dateTime(pending.requestedAt))}. Complete it before starting another update.</p></div></div>`;
      } else if (updateAvailable) {
        notice = `<div class="notice tone-warning">${ui.icon('download', 16)}<div><strong>Alt8Mail ${esc(latest)} is available</strong><p>The installed release is ${esc(overview.currentVersion)}. Installing takes a backup first and rolls back automatically if readiness checks fail.</p></div></div>`;
      } else if (latest) {
        notice = `<div class="notice tone-good">${ui.icon('check-circle', 16)}<div><strong>Release is current</strong><p>The latest checked tag is ${esc(latest)}. Use Check for update to refresh it.</p></div></div>`;
      } else {
        notice = `<div class="notice tone-info">${ui.icon('info', 16)}<div><strong>Release state has not been checked</strong><p>Use Check for update to compare the installed release with the configured Git repository.</p></div></div>`;
      }
      release.innerHTML = `${notice}
        <div style="margin-top:15px">${ui.kv([
            ['Current version', `<span class="mono">v${esc(overview.currentVersion || '—')}</span>`],
            ['Latest version', latest ? `<span class="mono">v${esc(latest)}</span>` : 'Not checked'],
            ['Channel', `<span class="mono">${esc(overview.channel || 'stable')}</span>`],
            ['Last checked', overview.checkedAt ? esc(fmt.dateTime(overview.checkedAt)) : 'Never'],
            ['Last update', last ? `${ui.statusPill(last.status)} ${esc(last.targetVersion || '')}` : 'No release update recorded'],
            ['Repository access', 'SSH with HTTPS fallback'],
          ], 'is-3')}</div>
        ${last ? `<div class="divider"></div><div class="form-note">Last update ${esc(fmt.timeAgo(last.finishedAt || last.requestedAt))} by ${esc(last.actorEmail || 'an operator')}${last.detail ? ` · ${esc(last.detail)}` : ''}</div>` : ''}`;
    }

    const releases = document.getElementById('system-releases');
    if (releases) {
      const rows = Array.isArray(overview.releases) ? overview.releases : [];
      releases.innerHTML = rows.length ? `<div class="table-wrap"><table class="data compact"><thead><tr><th>Version</th><th>Installed</th><th>Status</th><th>Path</th></tr></thead><tbody>${rows.map((release) => `<tr><td class="mono">v${esc(release.version)}</td><td>${esc(fmt.dateTime(release.installedAt))}</td><td>${release.current ? ui.badge('Current', 'good', 'check-circle') : ui.badge('Previous', 'neutral')}</td><td class="mono break-all">${esc(release.path)}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No release history', 'Release directories appear after the first installer or update.', '', 'archive');
    }

    const reboot = document.getElementById('system-reboot');
    if (reboot) {
      const last = overview.lastReboot;
      reboot.innerHTML = `<div class="diagnostic-summary">${ui.statusPill(pending ? 'warning' : overview.rebootRequired ? 'warning' : 'active', pending ? 'Reboot pending' : overview.rebootRequired ? 'Reboot required' : 'Host online')}<span class="summary-pill">${ui.icon('clock', 12)} ${fmt.duration(overview.uptimeSeconds)} uptime</span><span class="summary-pill">${ui.icon('server', 12)} ${esc(overview.hostname || 'host')}</span></div>
        <div style="margin-top:13px">${ui.kv([
            ['Booted', esc(fmt.dateTime(overview.bootedAt))],
            ['Kernel', `<span class="mono">${esc(overview.kernel || '—')}</span>`],
            ['Boot ID', `<span class="mono break-all">${esc(overview.bootId || '—')}</span>`],
            ['Mail hostname', `<span class="mono">${esc(overview.mailHostname || '—')}</span>`],
            ['Webmail URL', `<span class="mono break-all">${esc(overview.webmailUrl || '—')}</span>`],
            ['Last reboot', last ? `${esc(actionLabel(last.action))} · ${esc(fmt.timeAgo(last.createdAt))}` : 'Not recorded'],
          ], 'is-3')}</div>
        <div id="reboot-watch" class="notice tone-info" style="margin-top:15px" hidden></div>
        <div class="form-actions"><span class="form-note">A restart interrupts webmail and mail delivery briefly. The control panel will disconnect, then reconnect here.</span><div class="form-actions-right"><button class="btn btn-soft-danger" type="button" id="reboot-button" ${!canManage || active || pending ? 'disabled' : ''}>${ui.icon('power', 14)} Reboot server</button></div></div>`;
      document.getElementById('reboot-button')?.addEventListener('click', requestReboot);
    }

    const os = document.getElementById('system-os');
    if (os) {
      os.innerHTML = `<div class="diagnostic-summary">${ui.statusPill(overview.rebootRequired ? 'warning' : 'active', overview.rebootRequired ? 'Reboot required' : 'No reboot flag')}<span class="summary-pill">${ui.icon('download', 12)} ${ui.num(overview.osUpdates || 0)} package update${Number(overview.osUpdates) === 1 ? '' : 's'}</span></div>
        <p class="form-note" style="margin-top:12px">Operating-system packages are managed separately from Alt8Mail releases. Reboot when a kernel or core library update requires it.</p>
        <div class="tile-link"><span>${ui.icon('download', 15)} Package updates</span><a class="value" href="#/updates">Open software updates</a></div>`;
    }

    const activity = document.getElementById('system-activity');
    if (activity) {
      const events = Array.isArray(overview.activity) ? overview.activity : [];
      activity.innerHTML = events.length ? `<div class="list audit-list">${events.map((event) => `<div class="list-item"><span class="list-icon">${ui.icon(String(event.action).includes('reboot') ? 'power' : String(event.action).includes('update') ? 'download' : 'activity', 13)}</span><div class="list-copy"><strong>${esc(actionLabel(event.action))}</strong><span>${esc(event.actor || 'system')} · ${esc(event.target || '')} ${event.detail ? `· ${esc(ui.truncate(event.detail, 72))}` : ''}</span></div><span class="list-time">${esc(fmt.timeAgo(event.createdAt))}</span></div>`).join('')}</div>` : ui.emptyState('No system activity yet', 'Reboots, update checks and release updates will appear here.', '', 'activity');
    }

    if (active && active.id !== activeJobId) {
      activeJobId = active.id;
      paintConsole(active);
      watchUpdate(active.id);
    }
    if (!active && updateJob) paintConsole(updateJob);
    if (pending && !rebootWatching) watchReboot(pending.previousBootId, pending.requestedAt);
  };

  const load = async (silent = false) => {
    try {
      const data = await ctx.api('/control/api/system');
      if (!ctx.isCurrent()) return;
      overview = data;
      paint();
    } catch (error) {
      if (!silent) {
        const host = document.getElementById('system-kpis');
        if (host) host.innerHTML = ui.inlineNotice('critical', 'Could not read system state', errorText(error));
      }
    }
  };

  const checkForUpdate = async () => {
    const button = document.getElementById('system-check');
    if (button) { button.classList.add('is-loading'); button.disabled = true; }
    try {
      const result = await ctx.api('/control/api/system/update/check', { method: 'POST' });
      ui.toast(result.updateAvailable ? `Alt8Mail ${result.latestVersion} is available.` : `Alt8Mail is current at ${result.currentVersion}.`, result.updateAvailable ? 'warning' : 'good');
      await load();
    } catch (error) {
      ui.toast(errorText(error), 'critical', { title: 'Could not check for a release' });
    } finally {
      if (button) { button.classList.remove('is-loading'); button.disabled = false; }
    }
  };

  const watchUpdate = (jobID) => {
    if (!jobID) return;
    if (activeJobTimer) clearTimeout(activeJobTimer);
    const tick = async () => {
      if (!ctx.isCurrent()) return;
      try {
        const job = await ctx.api(`/control/api/system/update/${encodeURIComponent(jobID)}`);
        if (!ctx.isCurrent()) return;
        paintConsole(job);
        if (job.status === 'completed' || job.status === 'failed') {
          activeJobId = '';
          updateJob = job;
          if (job.status === 'completed') ui.toast(`Alt8Mail updated to ${job.targetVersion}.`, 'good');
          else ui.toast(job.detail || 'The update did not complete.', 'critical', { title: 'Release update failed' });
          await load(true);
          return;
        }
      } catch (error) {
        if (error?.name === 'AbortError') return;
        if (error?.code === 'UNAUTHENTICATED') {
          setConsoleState('incomplete', 'The control session expired before the update finished.');
          return;
        }
        if (error?.code === 'UPDATE_JOB') {
          ui.toast('The update job is no longer available after the service restart.', 'warning');
          await load(true);
          return;
        }
        setConsoleState('running', 'The control plane is restarting; reconnecting to the update log…');
      }
      activeJobTimer = setTimeout(tick, 2200);
    };
    tick();
  };

  const startUpdate = async () => {
    if (!canManage || !overview) return;
    let target = overview.latestVersion;
    if (!target) {
      await checkForUpdate();
      target = overview?.latestVersion;
    }
    if (!target) {
      ui.toast('Check for an available release first.', 'warning');
      return;
    }
    const confirmed = await ctx.confirm({
      title: `Install Alt8Mail ${target}?`,
      message: 'The updater will take a database and configuration backup, apply migrations, switch the release and restart the three application services. It rolls back automatically if readiness checks fail.',
      confirmLabel: `Install ${target}`,
      tone: 'danger',
      requireText: 'UPDATE',
      details: `<div class="notice tone-warning">${ui.icon('power', 16)}<div><strong>The control plane restarts</strong><p>This page will disconnect briefly and reconnect to follow the update log.</p></div></div>`,
    });
    if (!confirmed) return;
    const button = document.getElementById('system-update');
    if (button) { button.classList.add('is-loading'); button.disabled = true; }
    setConsoleState('queued', 'Starting the updater…');
    try {
      const result = await ctx.api('/control/api/system/update', { method: 'POST', body: { target, confirm: 'UPDATE' } });
      ui.toast(`Update to ${result.targetVersion} started.`, 'info');
      activeJobId = result.jobId;
      setConsoleState('queued', 'Waiting for updater output…');
      watchUpdate(result.jobId);
      await load(true);
    } catch (error) {
      ui.toast(errorText(error), 'critical', { title: 'Could not start the update' });
      if (button) { button.classList.remove('is-loading'); button.disabled = false; }
    }
  };

  const setRebootWatch = (tone, title, detail) => {
    const host = document.getElementById('reboot-watch');
    if (!host) return;
    host.hidden = false;
    host.className = `notice tone-${tone}`;
    host.innerHTML = `${ui.icon(tone === 'good' ? 'check-circle' : tone === 'critical' ? 'alert-circle' : 'power', 16)}<div><strong>${esc(title)}</strong><p>${esc(detail)}</p></div>`;
  };

  const watchReboot = (previousBootId, requestedAt) => {
    if (rebootWatching) return;
    rebootWatching = true;
    const started = Date.now();
    let sawOffline = false;
    const requested = requestedAt ? Date.parse(requestedAt) : Date.now();
    setRebootWatch('info', 'Reboot requested', 'Waiting for the host to shut down…');
    const tick = async () => {
      if (!ctx.isCurrent()) return;
      let online = false;
      try {
        const response = await fetch('/healthz', { cache: 'no-store' });
        online = response.ok;
      } catch (error) {
        online = false;
      }
      if (!online) {
        sawOffline = true;
        setRebootWatch('warning', 'Server is offline', `Reboot in progress. Waiting for the host to return${sawOffline ? '' : '…'}`);
      } else {
        let fresh = null;
        try {
          fresh = await ctx.api('/control/api/system');
        } catch (error) {
          if (error?.code === 'UNAUTHENTICATED') {
            setRebootWatch('warning', 'Server is back online', 'Sign in again to confirm the new boot and continue administration.');
            rebootWatching = false;
            return;
          }
        }
        if (fresh?.bootId && previousBootId && fresh.bootId !== previousBootId) {
          setRebootWatch('good', 'Server is back online', `New boot ${fresh.bootId.slice(0, 8)} detected. The control plane and mail services are responding.`);
          ui.toast('Server rebooted and is back online.', 'good');
          rebootWatching = false;
          if (rebootTimer) clearTimeout(rebootTimer);
          rebootTimer = null;
          await load(true);
          return;
        }
        if (fresh && !fresh.pendingReboot && fresh.lastReboot?.action === 'system.reboot_completed' && Date.parse(fresh.lastReboot.createdAt) >= requested) {
          setRebootWatch('good', 'Server is back online', 'The control plane recorded the completed reboot.');
          ui.toast('Server rebooted and is back online.', 'good');
          rebootWatching = false;
          if (rebootTimer) clearTimeout(rebootTimer);
          rebootTimer = null;
          await load(true);
          return;
        }
        if (sawOffline) {
          setRebootWatch('info', 'Server responding', 'The host is reachable. Waiting for the control plane to finish starting…');
        } else {
          setRebootWatch('info', 'Reboot scheduled', 'The host has not dropped the connection yet. Waiting for systemd to restart it…');
        }
      }
      if (Date.now() - started > 5 * 60 * 1000) {
        setRebootWatch('critical', 'Reboot not confirmed', 'The control plane did not observe the host return within five minutes. Check the host console and the audit log before trying again.');
        rebootWatching = false;
        if (rebootTimer) clearTimeout(rebootTimer);
        rebootTimer = null;
        return;
      }
      rebootTimer = setTimeout(tick, 2500);
    };
    tick();
  };

  const requestReboot = async () => {
    if (!canManage || !overview) return;
    const confirmed = await ctx.confirm({
      title: 'Reboot this server?',
      message: 'Webmail, IMAP, SMTP, customer admin and System Control will be unavailable while the host restarts. Mail already queued on the server remains in the queue.',
      confirmLabel: 'Reboot server',
      tone: 'danger',
      requireText: 'REBOOT',
      details: `<div class="notice tone-warning">${ui.icon('power', 16)}<div><strong>The control plane will disconnect</strong><p>Stay on this page. It will poll the host health endpoint and tell you when the server is back.</p></div></div>`,
    });
    if (!confirmed) return;
    try {
      const result = await ctx.api('/control/api/system/reboot', { method: 'POST', body: { confirm: 'REBOOT' } });
      ui.toast('Reboot scheduled. Waiting for the host to restart.', 'info');
      rebootWatching = false;
      watchReboot(result.previousBootId, result.requestedAt);
    } catch (error) {
      ui.toast(errorText(error), 'critical', { title: 'Could not reboot the server' });
    }
  };

  document.getElementById('system-refresh')?.addEventListener('click', () => load());
  document.getElementById('system-check')?.addEventListener('click', checkForUpdate);
  document.getElementById('system-update')?.addEventListener('click', startUpdate);
  document.getElementById('system-console-clear')?.addEventListener('click', () => {
    const log = document.getElementById('system-console-log');
    if (log) log.textContent = '';
  });
  ctx.onCleanup(() => {
    if (activeJobTimer) clearTimeout(activeJobTimer);
    if (rebootTimer) clearTimeout(rebootTimer);
  });
  await load();
}
