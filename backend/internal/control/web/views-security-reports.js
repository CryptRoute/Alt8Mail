/* Cross-tenant phishing, spam and malware reports. */
import * as ui from './ui.js';

const esc = ui.esc;
const fmt = {
  dateTime: ui.dateTime, dateOnly: ui.dateOnly, timeAgo: ui.timeAgo,
  num: ui.num,
};

const REASONS = {
  phishing: ['Phishing', 'critical'],
  spam: ['Spam', 'warning'],
  unwanted: ['Unwanted', 'warning'],
  malware: ['Malware', 'critical'],
  other: ['Other', 'neutral'],
};

function errorText(error) {
  return error?.message || String(error || 'Unknown error');
}

function reasonBadge(reason) {
  const [label, tone] = REASONS[reason] || [reason || 'Other', 'neutral'];
  return ui.badge(label, tone);
}

function statusBadge(status) {
  if (status === 'confirmed') return ui.badge('Blocked', 'critical', 'shield');
  if (status === 'dismissed') return ui.badge('Dismissed', 'good', 'check-circle');
  return ui.badge('Pending review', 'warning', 'clock');
}

export async function renderSecurityReports(ctx) {
  const canManage = ctx.can('security.manage');
  let reports = [];
  let summary = { counts: { pending: 0, confirmed: 0, dismissed: 0, blockedSenders: 0 }, recent: [] };
  let threatIntel = { enabled: false, active: { senders: 0, domains: 0, hosts: 0 } };
  let threatIntelError = '';

  ctx.setTitle('Security reports', 'Security');
  ctx.root.innerHTML = `
    ${ui.pageHeader({
      eyebrow: 'Security',
      title: 'Security reports',
      description: 'Phishing, spam and malware reported by any mailbox. Pending and confirmed senders are blocked at the Postfix policy layer for every tenant.',
      actions: `<button class="btn btn-secondary" type="button" id="security-reports-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
    })}
    <div class="stat-grid" id="security-report-stats"></div>
    <section class="card mt-4" id="threat-intel-card">
      <div class="card-body">${ui.loadingState('Loading threat intelligence settings…', 2)}</div>
    </section>
    <section class="card mt-4">
      <header class="card-head">
        <div><h3>Review queue</h3><p>Newest pending reports first · sender block applies immediately</p></div>
        <div class="card-actions">
          <input class="input" id="security-report-search" placeholder="Search sender, subject or user" style="width:240px" autocomplete="off">
          <select class="input" id="security-report-status" style="width:150px">
            <option value="">All statuses</option>
            <option value="pending" selected>Pending</option>
            <option value="confirmed">Confirmed</option>
            <option value="dismissed">Dismissed</option>
          </select>
        </div>
      </header>
      <div class="card-body" id="security-report-list">${ui.loadingState('Loading security reports…', 4)}</div>
    </section>`;

  const renderStats = () => {
    const counts = summary.counts || {};
    document.getElementById('security-report-stats').innerHTML = [
      ui.statTile({ label: 'Pending review', value: ui.num(counts.pending || 0), detail: 'Sender blocked while pending', iconName: 'clock', tone: Number(counts.pending) ? 'warning' : 'good' }),
      ui.statTile({ label: 'Confirmed threats', value: ui.num(counts.confirmed || 0), detail: 'Postfix-level block retained', iconName: 'shield', tone: Number(counts.confirmed) ? 'critical' : 'good' }),
      ui.statTile({ label: 'Dismissed', value: ui.num(counts.dismissed || 0), detail: 'Block removed after review', iconName: 'check-circle' }),
      ui.statTile({ label: 'Blocked senders', value: ui.num(counts.blockedSenders || 0), detail: 'Distinct addresses or domains', iconName: 'users', tone: Number(counts.blockedSenders) ? 'warning' : 'good' }),
    ].join('');
  };

  const renderThreatIntel = () => {
    const host = document.getElementById('threat-intel-card');
    if (!host) return;
    if (threatIntelError) {
      host.innerHTML = `<div class="card-body">${ui.inlineNotice('critical', 'Could not load threat intelligence settings', threatIntelError)}</div>`;
      return;
    }
    const active = threatIntel.active || {};
    host.innerHTML = `
      <header class="card-head">
        <div><h3>Threat intelligence</h3><p>Confirmed indicators can train Rspamd through Redis-backed multimaps.</p></div>
        <div class="card-actions">${ui.badge(threatIntel.enabled ? 'Sharing enabled' : 'Off', threatIntel.enabled ? 'good' : 'neutral')}</div>
      </header>
      <div class="card-body">
        ${ui.switchField({
          label: 'Share confirmed threat indicators with Rspamd',
          name: 'rspamdShare',
          checked: Boolean(threatIntel.enabled),
          disabled: !canManage,
          help: 'Publishes the confirmed sender, sender domain and reviewer-approved URL hosts system-wide. Pending reports stay at the Postfix policy layer only.',
        })}
        <div class="form-actions" style="margin-top:12px">
          <span class="form-note">${threatIntel.enabled ? 'Active indicators' : 'Confirmed indicators ready to share'}: ${ui.num(active.senders || 0)} sender${Number(active.senders) === 1 ? '' : 's'} · ${ui.num(active.domains || 0)} domain${Number(active.domains) === 1 ? '' : 's'} · ${ui.num(active.hosts || 0)} URL host${Number(active.hosts) === 1 ? '' : 's'}.</span>
          ${canManage ? '<div class="form-actions-right"><button class="btn btn-primary btn-sm" type="button" id="threat-intel-save">Save threat sharing</button></div>' : ''}
        </div>
      </div>`;
    host.querySelector('#threat-intel-save')?.addEventListener('click', async (event) => {
      const button = event.currentTarget;
      const enabled = Boolean(host.querySelector('[name="rspamdShare"]')?.checked);
      button.disabled = true;
      try {
        const result = await ctx.api('/control/api/security/threat-intel', { method: 'PUT', body: { enabled } });
        threatIntel = { enabled: Boolean(result.enabled), active: result.active || { senders: 0, domains: 0, hosts: 0 } };
        renderThreatIntel();
        ui.toast(enabled
          ? 'Confirmed threat indicators are now shared with Rspamd.'
          : 'Rspamd threat sharing is off and the Redis maps were cleared.', 'good');
      } catch (error) {
        button.disabled = false;
        ui.toast(errorText(error), 'critical', { title: 'Could not update threat sharing' });
      }
    });
  };

  const loadThreatIntel = async () => {
    try {
      const data = await ctx.api('/control/api/security/threat-intel');
      if (!ctx.isCurrent()) return;
      threatIntel = { enabled: Boolean(data.enabled), active: data.active || { senders: 0, domains: 0, hosts: 0 } };
      threatIntelError = '';
    } catch (error) {
      if (!ctx.isCurrent()) return;
      threatIntelError = errorText(error);
    }
    renderThreatIntel();
  };

  const renderList = () => {
    const host = document.getElementById('security-report-list');
    if (!host) return;
    if (!reports.length) {
      host.innerHTML = ui.emptyState('No matching reports', 'Reports from any mailbox land here. Pending reports already block the sender at the Postfix policy layer.', '', 'shield');
      return;
    }
    host.innerHTML = `<div class="table-wrap"><table class="data"><thead><tr>
      <th>Sender</th><th>Reason</th><th>Reported by</th><th>Subject</th><th>State</th><th>Received</th><th></th>
    </tr></thead><tbody>${reports.map((report) => `<tr data-security-report="${esc(report.id)}">
      <td><span class="cell-main">${esc(report.senderName || report.senderEmail)}</span><span class="cell-sub mono break-all">${esc(report.senderEmail)}</span></td>
      <td>${reasonBadge(report.reason)}</td>
      <td><span class="cell-main">${esc(report.userEmail)}</span><span class="cell-sub">${esc(report.tenant || 'Organization')}</span></td>
      <td><span class="cell-main">${esc(report.subject || '(no subject)')}</span><span class="cell-sub">${esc(ui.truncate(report.snippet || report.note || '', 90))}</span></td>
      <td>${statusBadge(report.status)}<span class="cell-sub">${report.blockScope === 'domain' ? 'Sender domain blocked' : report.blockScope === 'address' ? 'Sender address blocked' : 'No active block'}</span></td>
      <td><span>${esc(fmt.timeAgo(report.createdAt))}</span><span class="cell-sub">${esc(fmt.dateTime(report.createdAt))}</span></td>
      <td class="actions"><button class="btn btn-secondary btn-sm" type="button" data-security-review="${esc(report.id)}">${canManage && report.status === 'pending' ? 'Review' : 'Details'}</button></td>
    </tr>`).join('')}</tbody></table></div>`;
  };

  const load = async () => {
    const status = document.getElementById('security-report-status')?.value || '';
    const query = document.getElementById('security-report-search')?.value.trim() || '';
    try {
      const data = await ctx.api(`/control/api/security/reports?status=${encodeURIComponent(status)}&q=${encodeURIComponent(query)}`);
      if (!ctx.isCurrent()) return;
      reports = Array.isArray(data.reports) ? data.reports : [];
      summary = data.summary || summary;
      renderStats();
      renderList();
    } catch (error) {
      const host = document.getElementById('security-report-list');
      if (host) host.innerHTML = ui.inlineNotice('critical', 'Could not load security reports', errorText(error));
    }
  };

  const review = async (reportID) => {
    let detail;
    try {
      detail = await ctx.api(`/control/api/security/reports/${encodeURIComponent(reportID)}`);
    } catch (error) {
      ui.toast(errorText(error), 'critical');
      return;
    }
    const report = detail.report || {};
    const message = detail.message || {};
    const body = message.bodyText || message.snippet || report.snippet || 'No message body snapshot is available.';
    const urlHosts = Array.isArray(report.urlHosts) ? report.urlHosts : [];
    const hostEditable = report.status !== 'confirmed';
    const hostControls = urlHosts.length
      ? `<div class="option-list">${urlHosts.map((item) => `<label class="switch-field"><span class="switch"><input type="checkbox" data-report-url-host value="${ui.attr(item.host)}"${item.approved ? ' checked' : ''}${hostEditable ? '' : ' disabled'}><span class="switch-track"><span></span></span></span><span class="switch-copy"><strong>${esc(item.host)}</strong>${item.shared ? '<small>Shared destination · excluded by default</small>' : ''}</span></label>`).join('')}</div>`
      : '<p class="form-note">No HTTP or HTTPS hostnames were found in this message.</p>';
    const hostNote = !hostEditable
      ? 'This report is confirmed. Undo or dismiss it before changing URL-host approvals.'
      : report.threatSharingEnabled
        ? 'Checked hosts are published when you confirm the threat. Shared destinations are excluded unless you explicitly check them.'
        : 'Rspamd sharing is currently off. These selections are saved for when an administrator enables sharing.';
    const footer = canManage
      ? `<button class="btn btn-secondary" type="button" data-modal-close>Close</button>
         ${report.status !== 'pending' ? `<button class="btn btn-secondary" type="button" data-report-review="reopen">${ui.icon('refresh', 14)} Reopen</button>` : ''}
         ${report.status === 'confirmed' ? `<button class="btn btn-secondary" type="button" data-report-undo>${ui.icon('shield-off', 14)} Undo threat</button>` : ''}
         <button class="btn btn-secondary" type="button" data-report-review="dismiss">${ui.icon('check-circle', 14)} Dismiss &amp; unblock</button>
         <button class="btn btn-danger" type="button" data-report-review="domain">${ui.icon('shield', 14)} Block sender domain</button>
         <button class="btn btn-danger" type="button" data-report-review="confirm">${ui.icon('alert-circle', 14)} Confirm threat</button>`
      : `<button class="btn btn-secondary" type="button" data-modal-close>Close</button>`;
    const modal = ctx.modal({
      title: `Security report · ${report.senderEmail || 'unknown sender'}`,
      subtitle: `${report.reason || 'report'} · ${report.status || 'pending'} · ${report.tenant || 'organization'}`,
      size: 'large',
      body: `
        <div class="stat-grid">
          ${ui.statTile({ label: 'Sender', value: report.senderEmail || 'Unknown', detail: report.senderName || report.senderDomain || '', iconName: 'mail' })}
          ${ui.statTile({ label: 'Reported by', value: report.userEmail || 'Unknown', detail: report.tenant || '', iconName: 'user' })}
          ${ui.statTile({ label: 'Reason', value: (REASONS[report.reason] || [report.reason || 'Other'])[0], detail: 'User-submitted classification', iconName: 'alert' })}
          ${ui.statTile({ label: 'Postfix block', value: report.blockScope === 'domain' ? 'Domain' : report.blockScope === 'address' ? 'Address' : 'None', detail: report.status === 'dismissed' ? 'Dismissed by reviewer' : 'Enforced while pending or confirmed', iconName: 'shield' })}
        </div>
        <div class="form-grid mt-4">
          <div class="span-2"><span class="field-label">Subject</span><div class="read-box">${esc(report.subject || message.subject || '(no subject)')}</div></div>
          <div><span class="field-label">Reported</span><div class="read-box">${esc(fmt.dateTime(report.createdAt))}</div></div>
          <div><span class="field-label">Reviewed by</span><div class="read-box">${esc(report.reviewedBy || 'Pending review')}${report.reviewedAt ? ` · ${esc(fmt.dateTime(report.reviewedAt))}` : ''}</div></div>
          <div class="span-2"><span class="field-label">User note</span><div class="read-box">${esc(report.note || 'No note supplied.')}</div></div>
          <div class="span-2"><span class="field-label">Resolution</span><div class="read-box">${esc(report.resolution || 'Not reviewed yet.')}</div></div>
          <div class="span-2"><span class="field-label">URL hosts to publish to Rspamd</span>${hostControls}<p class="form-note">${esc(hostNote)}</p></div>
          <div class="span-2"><span class="field-label">Message snapshot</span><div class="read-box message-snapshot">${esc(ui.truncate(body, 5000))}</div></div>
        </div>`,
      footer,
      onMount: (root, close) => {
        root.querySelector('[data-report-undo]')?.addEventListener('click', async (event) => {
          const button = event.currentTarget;
          const confirmed = await ctx.confirm({
            title: 'Undo threat confirmation',
            message: `Remove the Postfix block for ${report.senderEmail || 'this sender'} and mark the report dismissed? This reverses the confirmation and unblocks the sender.`,
            confirmLabel: 'Undo and unblock',
            tone: 'danger',
            requireText: 'UNDO',
          });
          if (!confirmed) return;
          button.disabled = true;
          try {
            const result = await ctx.api(`/control/api/security/reports/${encodeURIComponent(reportID)}/undo`, {
              method: 'POST', body: { confirm: 'UNDO' },
            });
            if (result && result.warning) ui.toast(result.warning, 'warning', { title: 'Undo saved with a warning' });
            else ui.toast('Threat confirmation undone; sender unblocked.', 'good');
            close();
            await Promise.all([load(), loadThreatIntel()]);
          } catch (error) {
            button.disabled = false;
            ui.toast(errorText(error), 'critical', { title: 'Could not undo the threat' });
          }
        });
        root.querySelectorAll('[data-report-review]').forEach((button) => {
          button.addEventListener('click', async () => {
            const action = button.getAttribute('data-report-review');
            const selectedHosts = [...root.querySelectorAll('[data-report-url-host]:checked')].map((input) => input.value);
            const payload = action === 'reopen'
              ? { status: 'pending', blockScope: 'address', resolution: '' }
              : action === 'dismiss'
                ? { status: 'dismissed', blockScope: 'none', resolution: 'Dismissed by security review' }
                : { status: 'confirmed', blockScope: action === 'domain' ? 'domain' : 'address', resolution: action === 'domain' ? 'Confirmed; sender domain blocked' : 'Confirmed malicious sender', urlHosts: selectedHosts };
            button.disabled = true;
            try {
              const result = await ctx.api(`/control/api/security/reports/${encodeURIComponent(reportID)}`, { method: 'PATCH', body: payload });
              if (result && result.warning) ui.toast(result.warning, 'warning', { title: 'Review saved with a warning' });
              else ui.toast(action === 'dismiss' ? 'Report dismissed and block removed.' : action === 'reopen' ? 'Report reopened for review.' : 'Threat confirmed and block updated.', 'good');
              close();
              await Promise.all([load(), loadThreatIntel()]);
            } catch (error) {
              button.disabled = false;
              ui.toast(errorText(error), 'critical', { title: 'Could not review report' });
            }
          });
        });
      },
    });
    return modal;
  };

  ctx.listen(document.getElementById('security-reports-refresh'), 'click', () => Promise.all([load(), loadThreatIntel()]));
  ctx.listen(document.getElementById('security-report-status'), 'change', load);
  ctx.listen(document.getElementById('security-report-search'), 'input', () => {
    clearTimeout(ctx.state.securitySearchTimer);
    ctx.state.securitySearchTimer = setTimeout(load, 250);
  });
  ctx.listen(ctx.root, 'click', (event) => {
    const button = event.target.closest('[data-security-review]');
    if (button) {
      event.preventDefault();
      review(button.getAttribute('data-security-review'));
    }
  });

  renderStats();
  await Promise.all([load(), loadThreatIntel()]);
}
