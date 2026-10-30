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

  ctx.setTitle('Security reports', 'Security');
  ctx.root.innerHTML = `
    ${ui.pageHeader({
      eyebrow: 'Security',
      title: 'Security reports',
      description: 'Phishing, spam and malware reported by any mailbox. Pending and confirmed senders are blocked at the Postfix policy layer for every tenant.',
      actions: `<button class="btn btn-secondary" type="button" id="security-reports-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
    })}
    <div class="stat-grid" id="security-report-stats"></div>
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
            await ctx.api(`/control/api/security/reports/${encodeURIComponent(reportID)}/undo`, {
              method: 'POST', body: { confirm: 'UNDO' },
            });
            ui.toast('Threat confirmation undone; sender unblocked.', 'good');
            close();
            await load();
          } catch (error) {
            button.disabled = false;
            ui.toast(errorText(error), 'critical', { title: 'Could not undo the threat' });
          }
        });
        root.querySelectorAll('[data-report-review]').forEach((button) => {
          button.addEventListener('click', async () => {
            const action = button.getAttribute('data-report-review');
            const payload = action === 'reopen'
              ? { status: 'pending', blockScope: 'address', resolution: '' }
              : action === 'dismiss'
                ? { status: 'dismissed', blockScope: 'none', resolution: 'Dismissed by security review' }
                : { status: 'confirmed', blockScope: action === 'domain' ? 'domain' : 'address', resolution: action === 'domain' ? 'Confirmed; sender domain blocked' : 'Confirmed malicious sender' };
            button.disabled = true;
            try {
              await ctx.api(`/control/api/security/reports/${encodeURIComponent(reportID)}`, { method: 'PATCH', body: payload });
              ui.toast(action === 'dismiss' ? 'Report dismissed and block removed.' : action === 'reopen' ? 'Report reopened for review.' : 'Threat confirmed and block updated.', 'good');
              close();
              await load();
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

  ctx.listen(document.getElementById('security-reports-refresh'), 'click', load);
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
  await load();
}
