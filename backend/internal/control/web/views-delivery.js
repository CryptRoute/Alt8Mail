/* Deliverability, queue, relay/outbound routing and suppression views. */
import * as ui from './ui.js';

const esc = ui.esc;
const fmt = {
  dateTime: ui.dateTime, dateOnly: ui.dateOnly, timeAgo: ui.timeAgo,
  bytes: ui.bytes, num: ui.num, pct: ui.pct, duration: ui.duration,
};

function errorText(error) {
  return error?.message || String(error || 'Unknown error');
}

function id(prefix = 'id') {
  return `${prefix}-${(crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random()}`).toString().replace(/[^a-zA-Z0-9-]/g, '').slice(0, 18)}`;
}

function rateTone(rate, warn = 2, bad = 5) {
  const value = Number(rate) || 0;
  if (value >= bad) return 'critical';
  if (value >= warn) return 'warning';
  return 'good';
}

/* =============================================================== deliverability */

export async function renderDelivery(ctx) {
  const canManage = ctx.can('delivery.manage');
  let days = 30;
  let deliverability = null;
  let blocklists = null;
  let tests = [];
  ctx.setTitle('Deliverability', 'Deliverability');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Deliverability',
    title: 'Deliverability',
    description: 'Outbound outcomes, recipient-domain quality, DNSBL posture and end-to-end SMTP tests.',
    actions: `<div class="segmented" id="delivery-days"><button type="button" data-days="7">7 days</button><button type="button" data-days="30" class="is-active">30 days</button><button type="button" data-days="90">90 days</button></div>${canManage ? `<button class="btn btn-primary" type="button" id="smtp-test-new">${ui.icon('send', 14)} SMTP test</button>` : ''}<button class="btn btn-secondary" type="button" id="delivery-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    <div id="delivery-kpis">${ui.loadingState('Loading delivery outcomes…', 3)}</div>
    <div class="grid grid-main mt-4">
      <div class="stack">
        <section class="card"><header class="card-head"><div><h3>Outcome breakdown</h3><p>Bounce and deferral rate by recipient domain and sender</p></div></header><div class="card-body" id="delivery-breakdown">${ui.loadingState('Loading outcomes…', 4)}</div></section>
        <section class="card"><header class="card-head"><div><h3>SMTP delivery tests</h3><p>End-to-end transcript through the local submission path</p></div>${canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="smtp-test-new-2">${ui.icon('plus', 13)} New test</button>` : ''}</header><div class="card-body" id="smtp-tests">${ui.loadingState('Loading tests…', 3)}</div></section>
      </div>
      <div class="stack">
        <section class="card"><header class="card-head"><div><h3>DNS blocklists</h3><p>Outbound IP reputation</p></div><div class="card-actions">${canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="blocklist-check">${ui.icon('refresh', 13)} Check now</button>` : ''}</div></header><div class="card-body" id="blocklist-summary">${ui.loadingState('Loading DNSBL state…', 3)}</div></section>
        <section class="card"><header class="card-head"><div><h3>Delivery path</h3><p>What to inspect when mail fails</p></div></header><div class="card-body">
          <div class="tile-link"><span>${ui.icon('queue', 15)} Postfix queue</span><a class="value" href="#/queue">Open queue</a></div>
          <div class="tile-link"><span>${ui.icon('relay', 15)} Relay / smarthost</span><a class="value" href="#/relay">Configure</a></div>
          <div class="tile-link"><span>${ui.icon('shield-off', 15)} Suppressions</span><a class="value" href="#/suppressions">Manage</a></div>
          <div class="tile-link"><span>${ui.icon('list', 15)} Mail events</span><a class="value" href="#/logs?direction=outbound">Search logs</a></div>
          <div class="note-strip" style="margin-top:11px">${ui.icon('info', 13)} <span>Rates use accepted and failed mail events ingested from Postfix. Deferrals are temporary; bounces are permanent failures.</span></div>
        </div></section>
      </div>
    </div>`;

  const loadDeliverability = async () => {
    const kpis = document.getElementById('delivery-kpis');
    const breakdown = document.getElementById('delivery-breakdown');
    if (!kpis || !breakdown) return;
    try {
      deliverability = await ctx.api(`/control/api/delivery/deliverability?days=${days}`);
      if (!ctx.isCurrent()) return;
      const data = deliverability;
      kpis.innerHTML = `<div class="stat-grid">
        ${ui.statTile({ label: 'Sent', value: ui.num(data.sent), detail: `Accepted outbound · ${days} days`, iconName: 'send', tone: 'info' })}
        ${ui.statTile({ label: 'Bounced', value: ui.num(data.bounced), detail: `${ui.pct(data.bounceRate)} of outbound`, iconName: 'x-circle', tone: Number(data.bounced) ? 'critical' : 'good' })}
        ${ui.statTile({ label: 'Deferred', value: ui.num(data.deferred), detail: `${ui.pct(data.deferralRate)} of outbound`, iconName: 'clock', tone: Number(data.deferred) ? 'warning' : 'good' })}
        ${ui.statTile({ label: 'Bounce rate', value: ui.pct(data.bounceRate), detail: data.bounceRate > 5 ? 'High — investigate immediately' : 'Within normal range', iconName: 'activity', tone: rateTone(data.bounceRate) })}
        ${ui.statTile({ label: 'Deferral rate', value: ui.pct(data.deferralRate), detail: 'Temporary delivery failures', iconName: 'refresh', tone: rateTone(data.deferralRate, 5, 15) })}
      </div>`;
      const byDomain = Array.isArray(data.byDomain) ? data.byDomain : [];
      const bySender = Array.isArray(data.bySender) ? data.bySender : [];
      breakdown.innerHTML = `<div class="grid grid-main-even">
        <div><h4 class="form-section-title">By recipient domain</h4>${byDomain.length ? `<div class="table-wrap"><table class="data compact"><thead><tr><th>Domain</th><th class="num">Sent</th><th class="num">Bounced</th><th class="num">Deferred</th><th class="num">Bounce rate</th></tr></thead><tbody>${byDomain.map((row) => `<tr><td class="mono">${esc(row.domain)}</td><td class="num">${ui.num(row.sent)}</td><td class="num">${ui.num(row.bounced)}</td><td class="num">${ui.num(row.deferred)}</td><td class="num">${ui.badge(ui.pct(row.bounceRate), rateTone(row.bounceRate))}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No recipient data', 'Send or receive mail to populate delivery outcomes.', '', 'send')}</div>
        <div><h4 class="form-section-title">By sender</h4>${bySender.length ? `<div class="table-wrap"><table class="data compact"><thead><tr><th>Sender</th><th class="num">Sent</th><th class="num">Bounced</th><th class="num">Deferred</th><th class="num">Bounce rate</th></tr></thead><tbody>${bySender.map((row) => `<tr><td class="mono">${esc(row.sender)}</td><td class="num">${ui.num(row.sent)}</td><td class="num">${ui.num(row.bounced)}</td><td class="num">${ui.num(row.deferred)}</td><td class="num">${ui.badge(ui.pct(row.bounceRate), rateTone(row.bounceRate))}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No sender data', '', '', 'mail')}</div>
      </div>`;
    } catch (error) {
      kpis.innerHTML = ui.inlineNotice('critical', 'Could not load deliverability', errorText(error));
      breakdown.innerHTML = '';
    }
  };

  const loadBlocklists = async () => {
    const host = document.getElementById('blocklist-summary');
    if (!host) return;
    try {
      blocklists = await ctx.api('/control/api/delivery/blocklists');
      if (!ctx.isCurrent()) return;
      const results = Array.isArray(blocklists.results) ? blocklists.results : [];
      const listed = results.filter((row) => row.listed);
      const lastChecked = results.reduce((latest, row) => (!latest || row.checkedAt > latest ? row.checkedAt : latest), '');
      host.innerHTML = `<div class="stat-grid" style="grid-template-columns:repeat(3,minmax(0,1fr))">
          <div class="kv-box"><span>Listed</span><strong class="${listed.length ? 'text-critical' : 'text-good'}">${ui.num(listed.length)}</strong></div>
          <div class="kv-box"><span>IPs checked</span><strong>${ui.num(blocklists.ips?.length || 0)}</strong></div>
          <div class="kv-box"><span>Zones</span><strong>${ui.num(blocklists.zones?.length || 0)}</strong></div>
        </div>
        <p class="form-note" style="margin-top:9px">Last result ${lastChecked ? esc(fmt.timeAgo(lastChecked)) : 'never checked'}. ${listed.length ? '<strong class="text-critical">At least one outbound IP is listed.</strong>' : 'No current listing among checked zones.'}</p>
        ${listed.length ? `<div class="notice tone-critical" style="margin-top:9px">${ui.icon('alert-circle', 16)}<div><strong>${listed.length} active listing${listed.length === 1 ? '' : 's'}</strong><p>${listed.slice(0, 5).map((row) => `${esc(row.ip)} on ${esc(row.blocklist)}`).join(' · ')}</p></div></div>` : ''}
        ${results.length ? `<div class="table-wrap" style="margin-top:10px;max-height:280px"><table class="data compact"><thead><tr><th>IP</th><th>Blocklist</th><th>State</th><th>Checked</th></tr></thead><tbody>${results.map((row) => `<tr><td class="mono">${esc(row.ip)}</td><td>${esc(row.blocklist)}</td><td>${row.listed ? ui.badge('Listed', 'critical', 'alert-circle') : row.blocked ? ui.badge('Check blocked', 'warning', 'alert') : ui.badge('Clear', 'good', 'check-circle')}</td><td>${esc(fmt.timeAgo(row.checkedAt))}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No DNSBL results', 'Register an outbound IP and run a check.', '', 'shield')}`;
    } catch (error) {
      host.innerHTML = ui.inlineNotice('critical', 'Could not load DNSBL state', errorText(error));
    }
  };

  const loadTests = async () => {
    const host = document.getElementById('smtp-tests');
    if (!host) return;
    try {
      const payload = await ctx.api('/control/api/delivery/smtp-tests');
      if (!ctx.isCurrent()) return;
      tests = Array.isArray(payload.tests) ? payload.tests : [];
      host.innerHTML = tests.length ? `<div class="table-wrap"><table class="data compact"><thead><tr><th>Status</th><th>Recipient</th><th>Subject</th><th>Created</th><th></th></tr></thead><tbody>${tests.map((test) => `<tr><td>${ui.statusPill(test.status)}</td><td class="mono">${esc(test.recipient)}</td><td>${esc(ui.truncate(test.subject, 50))}</td><td>${esc(fmt.timeAgo(test.createdAt))}</td><td class="actions"><button class="icon-btn" type="button" data-smtp-detail="${esc(test.id)}" title="View transcript" aria-label="View transcript">${ui.icon('eye', 14)}</button></td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No SMTP tests', 'Send a test to verify the local submission, Postfix policy and delivery path.', canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="smtp-test-empty">${ui.icon('send', 13)} Send test</button>` : '', 'send');
      document.getElementById('smtp-test-empty')?.addEventListener('click', openSMTPTest);
      host.querySelectorAll('[data-smtp-detail]').forEach((button) => button.addEventListener('click', () => showSMTPTest(button.dataset.smtpDetail)));
    } catch (error) {
      host.innerHTML = ui.inlineNotice('critical', 'Could not load SMTP tests', errorText(error));
    }
  };

  const showSMTPTest = async (testId) => {
    const modal = ctx.modal({ title: 'SMTP test transcript', size: 'large', body: ui.loadingState('Loading transcript…', 3), footer: `<button class="btn btn-secondary" type="button" data-modal-close>Close</button>` });
    try {
      const payload = await ctx.api(`/control/api/delivery/smtp-tests/${testId}`);
      if (!modal.root.isConnected) return;
      modal.root.querySelector('.modal-body').innerHTML = `<div class="diagnostic-summary">${ui.statusPill(payload.status)}<span class="summary-pill">${ui.icon('mail', 12)} ${esc(payload.recipient)}</span><span class="summary-pill">${ui.icon('clock', 12)} ${esc(fmt.dateTime(payload.createdAt))}</span></div>${payload.error ? `<div class="notice tone-critical">${ui.icon('alert-circle', 16)}<div><strong>Test failed</strong><p class="wrap-anywhere">${esc(payload.error)}</p></div></div>` : ''}<div class="kv-grid" style="margin-top:10px">${ui.kv([['Recipient', esc(payload.recipient)], ['Subject', esc(payload.subject)], ['Status', ui.statusPill(payload.status)]])}</div><div class="divider"></div>${ui.codeBlock(payload.transcript || 'No transcript', { language: 'SMTP transcript', maxHeight: '46vh' })}`;
    } catch (error) {
      modal.root.querySelector('.modal-body').innerHTML = ui.inlineNotice('critical', 'Could not load transcript', errorText(error));
    }
  };

  const openSMTPTest = () => {
    const modal = ctx.modal({
      title: 'Send an SMTP delivery test', subtitle: 'This talks to the local Postfix listener and captures the full protocol transcript.',
      size: 'medium',
      body: `<form id="smtp-test-form"><div class="form-grid">
        ${ui.field({ label: 'Recipient', name: 'recipient', type: 'email', placeholder: 'test@example.com', required: true, help: 'The message is sent through the real local submission path.' })}
        ${ui.field({ label: 'From', name: 'from', type: 'email', placeholder: 'postmaster@your-domain', help: 'Leave blank to use the configured default mail-from address.' })}
        ${ui.field({ label: 'Subject', name: 'subject', value: 'Alt8Mail SMTP test', })}
        <div class="span-2">${ui.field({ label: 'Message body', name: 'body', type: 'textarea', rows: 4, value: 'This is a delivery test from Alt8Mail System Control.\r\n\r\nIf you received it, the local Postfix submission path is working.' })}</div>
      </div><div class="form-actions"><span class="form-note">The transcript includes server replies. No credentials are included.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">Send and capture</button></div></div></form>`,
    });
    const form = modal.root.querySelector('#smtp-test-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        const result = await ctx.api('/control/api/delivery/smtp-tests', { method: 'POST', body: { recipient: values.recipient, from: values.from, subject: values.subject, body: values.body } });
        modal.close();
        await loadTests();
        const resultModal = ctx.modal({
          title: result.ok ? 'SMTP test succeeded' : 'SMTP test failed', subtitle: `${result.recipient} · ${result.status}`, size: 'large',
          body: `${result.error ? `<div class="notice tone-critical">${ui.icon('alert-circle', 16)}<div><strong>${esc(result.error)}</strong></div></div>` : `<div class="notice tone-good">${ui.icon('check-circle', 16)}<div><strong>Message accepted by the local SMTP listener</strong><p>Delivery continues through the normal Postfix queue and transport path.</p></div></div>`}<div class="divider"></div>${ui.codeBlock(result.transcript || 'No transcript', { language: 'SMTP transcript', maxHeight: '46vh' })}`,
          footer: `<button class="btn btn-secondary" type="button" data-modal-close>Close</button>`,
        });
        resultModal.root.querySelector('.modal-body')?.scrollTo(0, 0);
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'SMTP test failed' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  document.querySelectorAll('#delivery-days button').forEach((button) => button.addEventListener('click', () => {
    document.querySelectorAll('#delivery-days button').forEach((b) => b.classList.toggle('is-active', b === button));
    days = Number(button.dataset.days);
    loadDeliverability();
  }));
  document.getElementById('delivery-refresh')?.addEventListener('click', async () => { await Promise.all([loadDeliverability(), loadBlocklists(), loadTests()]); });
  document.getElementById('smtp-test-new')?.addEventListener('click', openSMTPTest);
  document.getElementById('smtp-test-new-2')?.addEventListener('click', openSMTPTest);
  document.getElementById('blocklist-check')?.addEventListener('click', async (event) => {
    const button = event.currentTarget;
    button.classList.add('is-loading'); button.disabled = true;
    try {
      await ctx.api('/control/api/delivery/blocklists/check', { method: 'POST' });
      ui.toast('DNSBL checks completed.', 'good');
      await loadBlocklists();
    } catch (error) { ui.toast(errorText(error), 'critical', { title: 'DNSBL check failed' }); }
    finally { button.classList.remove('is-loading'); button.disabled = false; }
  });

  await Promise.all([loadDeliverability(), loadBlocklists(), loadTests()]);
}

/* =============================================================== queue */

export async function renderQueue(ctx) {
  const canManage = ctx.can('delivery.manage');
  let items = [];
  ctx.setTitle('Mail queue', 'Deliverability');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Deliverability', title: 'Mail queue',
    description: 'Inspect Postfix queue entries, trace the delay reason and flush or remove stuck mail.',
    actions: `<a class="btn btn-ghost" href="#/delivery">${ui.icon('chevron-left', 15)} Deliverability</a><button class="btn btn-secondary" type="button" id="queue-refresh">${ui.icon('refresh', 15)} Refresh</button>${canManage ? `<button class="btn btn-soft-danger" type="button" id="queue-clear">${ui.icon('trash', 14)} Delete all</button>` : ''}`,
  })}
    <div class="card"><header class="card-head"><div><h3>Postfix queue</h3><p id="queue-summary">Loading queue…</p></div></header><div class="card-body" id="queue-body">${ui.loadingState('Reading the Postfix queue…', 4)}</div></div>
    <div class="notice tone-info mt-4">${ui.icon('info', 16)}<div><strong>Queue actions are immediate</strong><p>Flush asks Postfix to retry a message now. Delete removes it from the queue and it will not be delivered. Recipient delay reasons come from the queue listing.</p></div></div>`;

  const load = async () => {
    const body = document.getElementById('queue-body');
    if (!body) return;
    try {
      const payload = await ctx.api('/control/api/delivery/queue');
      if (!ctx.isCurrent()) return;
      items = Array.isArray(payload.messages) ? payload.messages : [];
      document.getElementById('queue-summary').textContent = `${items.length} message${items.length === 1 ? '' : 's'} in queue · checked ${fmt.timeAgo(payload.checkedAt)}`;
      body.innerHTML = items.length ? `<div class="table-wrap is-bare"><table class="data"><thead><tr><th>Queue ID</th><th>Sender</th><th>Recipients</th><th class="num">Size</th><th>Arrived</th><th></th></tr></thead><tbody>
        ${items.map((item) => `<tr><td><span class="cell-main mono">${esc(item.queueId)}</span><span class="cell-sub">${esc(item.queueName || '')}</span></td><td class="mono">${esc(item.sender || '<>')}</td><td>${item.recipients?.length ? item.recipients.map((recipient) => `<div><span class="mono">${esc(recipient.address)}</span>${recipient.delay_reason ? `<span class="cell-sub">${esc(recipient.delay_reason)}</span>` : ''}</div>`).join('') : '—'}</td><td class="num">${ui.bytes(item.messageSize)}</td><td class="nowrap-cell">${esc(fmt.dateTime(item.arrivalText))}</td><td class="actions"><button class="icon-btn" type="button" data-queue-detail="${esc(item.queueId)}" title="Inspect message" aria-label="Inspect message">${ui.icon('eye', 15)}</button>${canManage ? `<button class="icon-btn" type="button" data-queue-flush="${esc(item.queueId)}" title="Flush now" aria-label="Flush now">${ui.icon('refresh', 15)}</button><button class="icon-btn is-danger" type="button" data-queue-delete="${esc(item.queueId)}" title="Delete message" aria-label="Delete message">${ui.icon('trash', 15)}</button>` : ''}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('The queue is clear', 'Postfix has no messages waiting for delivery.', `<a class="btn btn-secondary btn-sm" href="#/delivery">Back to deliverability</a>`, 'queue');
      bindRows();
    } catch (error) {
      body.innerHTML = ui.inlineNotice('critical', 'Could not read the queue', errorText(error));
    }
  };

  const showDetail = async (queueId) => {
    const modal = ctx.modal({ title: `Queue ${queueId}`, size: 'large', body: ui.loadingState('Loading queue details…', 3), footer: `<button class="btn btn-secondary" type="button" data-modal-close>Close</button>${canManage ? `<button class="btn btn-secondary" type="button" id="queue-detail-flush">${ui.icon('refresh', 14)} Flush</button><button class="btn btn-danger" type="button" id="queue-detail-delete">${ui.icon('trash', 14)} Delete</button>` : ''}` });
    try {
      const payload = await ctx.api(`/control/api/delivery/queue/${queueId}`);
      if (!modal.root.isConnected) return;
      const message = payload.message;
      modal.root.querySelector('.modal-body').innerHTML = `${message ? `<div class="kv-grid" style="margin-bottom:12px">${ui.kv([
        ['Queue ID', `<span class="mono">${esc(message.queueId)}</span>`], ['Sender', `<span class="mono">${esc(message.sender)}</span>`],
        ['Size', ui.bytes(message.messageSize)], ['Arrived', esc(fmt.dateTime(message.arrivalText))],
        ['Queue', esc(message.queueName)],
      ])}</div>${message.recipients?.length ? `<h4 class="form-section-title">Recipients</h4><div class="mini-list">${message.recipients.map((recipient) => `<div class="mini-row"><span class="list-icon">${ui.icon('mail', 13)}</span><div class="grow"><strong class="mono">${esc(recipient.address)}</strong><span>${esc(recipient.delay_reason || 'No delay reason reported')}</span></div></div>`).join('')}</div>` : ''}` : ''}${ui.codeBlock(payload.transcript || 'No queue transcript', { language: 'postcat', maxHeight: '46vh' })}`;
      modal.root.querySelector('#queue-detail-flush')?.addEventListener('click', async () => {
        try { await ctx.api(`/control/api/delivery/queue/${queueId}/flush`, { method: 'POST' }); ui.toast('Flush requested.', 'good'); modal.close(); await load(); }
        catch (error) { ui.toast(errorText(error), 'critical', { title: 'Flush failed' }); }
      });
      modal.root.querySelector('#queue-detail-delete')?.addEventListener('click', async () => {
        const ok = await ctx.confirm({ title: 'Delete queued message', message: `Remove ${queueId} from the queue? The message will not be delivered.`, confirmLabel: 'Delete message', tone: 'danger' });
        if (!ok) return;
        try { await ctx.api(`/control/api/delivery/queue/${queueId}`, { method: 'DELETE' }); ui.toast('Message deleted from queue.', 'good'); modal.close(); await load(); }
        catch (error) { ui.toast(errorText(error), 'critical', { title: 'Delete failed' }); }
      });
    } catch (error) {
      modal.root.querySelector('.modal-body').innerHTML = ui.inlineNotice('critical', 'Could not load queue message', errorText(error));
    }
  };

  const bindRows = () => {
    document.querySelectorAll('[data-queue-detail]').forEach((button) => button.addEventListener('click', () => showDetail(button.dataset.queueDetail)));
    document.querySelectorAll('[data-queue-flush]').forEach((button) => button.addEventListener('click', async () => {
      try { await ctx.api(`/control/api/delivery/queue/${button.dataset.queueFlush}/flush`, { method: 'POST' }); ui.toast('Flush requested.', 'good'); await load(); }
      catch (error) { ui.toast(errorText(error), 'critical', { title: 'Flush failed' }); }
    }));
    document.querySelectorAll('[data-queue-delete]').forEach((button) => button.addEventListener('click', async () => {
      const ok = await ctx.confirm({ title: 'Delete queued message', message: `Remove ${button.dataset.queueDelete} from the queue? The message will not be delivered.`, confirmLabel: 'Delete message', tone: 'danger' });
      if (!ok) return;
      try { await ctx.api(`/control/api/delivery/queue/${button.dataset.queueDelete}`, { method: 'DELETE' }); ui.toast('Message deleted from queue.', 'good'); await load(); }
      catch (error) { ui.toast(errorText(error), 'critical', { title: 'Delete failed' }); }
    }));
  };

  document.getElementById('queue-refresh')?.addEventListener('click', load);
  document.getElementById('queue-clear')?.addEventListener('click', async () => {
    const ok = await ctx.confirm({ title: 'Delete the entire queue', message: 'Every message currently queued by Postfix will be removed. This cannot be undone and queued mail will be lost.', confirmLabel: 'Delete all messages', tone: 'danger', requireText: 'DELETE ALL' });
    if (!ok) return;
    try { await ctx.api('/control/api/delivery/queue', { method: 'DELETE', body: { confirm: 'DELETE ALL' } }); ui.toast('Postfix queue cleared.', 'good'); await load(); }
    catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not clear queue' }); }
  });
  await load();
}

/* =============================================================== relay & outbound */

export async function renderRelay(ctx) {
  const canManage = ctx.can('delivery.manage');
  let relay = null;
  let ips = [];
  let routes = [];
  ctx.setTitle('Relay & outbound', 'Deliverability');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Deliverability', title: 'Relay & outbound',
    description: 'Optional smarthost, outbound source addresses and per-sender transport routing rendered into Postfix.',
    actions: `<a class="btn btn-ghost" href="#/delivery">${ui.icon('chevron-left', 15)} Deliverability</a><button class="btn btn-secondary" type="button" id="relay-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    <div class="grid grid-main">
      <div class="stack">
        <section class="card"><header class="card-head"><div><h3>Outbound relay / smarthost</h3><p>SMTP credentials are sealed; sasl_passwd is mode 0600</p></div><div class="card-actions"><button class="btn btn-secondary btn-sm" type="button" id="relay-test">${ui.icon('activity', 13)} Test relay</button></div></header><div class="card-body" id="relay-body">${ui.loadingState('Loading relay settings…', 3)}</div></section>
        <section class="card"><header class="card-head"><div><h3>Outbound IP registry</h3><p>Source addresses considered for HELO and transport routing</p></div><div class="card-actions">${canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="ip-discover">${ui.icon('search', 13)} Discover</button><button class="btn btn-primary btn-sm" type="button" id="ip-create">${ui.icon('plus', 13)} Add IP</button>` : ''}</div></header><div class="card-body" id="ip-body">${ui.loadingState('Loading outbound IPs…', 3)}</div></section>
        <section class="card"><header class="card-head"><div><h3>Sender routes</h3><p>Render <span class="mono">sender_transport</span> and a per-domain SMTP transport</p></div>${canManage ? `<button class="btn btn-primary btn-sm" type="button" id="route-create">${ui.icon('plus', 13)} Add route</button>` : ''}</header><div class="card-body" id="route-body">${ui.loadingState('Loading sender routes…', 3)}</div></section>
      </div>
      <div class="stack">
        <section class="card"><header class="card-head"><div><h3>How routing is applied</h3></div></header><div class="card-body">
          <div class="timeline">
            <div class="timeline-item"><span class="timeline-dot"></span><strong>Pick a source</strong><span>Each outbound IP has a mode, priority, interface and HELO name.</span></div>
            <div class="timeline-item"><span class="timeline-dot"></span><strong>Map a sender domain</strong><span>Routes create <span class="mono">smtp-*</span> transports in master.cf and a sender_transport lookup.</span></div>
            <div class="timeline-item"><span class="timeline-dot"></span><strong>Reload Postfix</strong><span>Changes are validated and applied through a reload.</span></div>
            <div class="timeline-item"><span class="timeline-dot"></span><strong>Verify with a test</strong><span>Use Deliverability → SMTP test to verify the outbound path.</span></div>
          </div>
          <div class="note-strip" style="margin-top:12px">${ui.icon('info', 13)} <span>Provider PTR records must match the HELO hostname. Update reverse DNS at the VPS/cloud provider after adding a source address.</span></div>
        </div></section>
        <section class="card"><header class="card-head"><div><h3>Relay safety</h3></div></header><div class="card-body">
          <div class="tile-link"><span>${ui.icon('lock', 15)} Credential storage</span><span class="value">AES-GCM sealed</span></div>
          <div class="tile-link"><span>${ui.icon('file-code', 15)} Sasl map</span><span class="value">0600 · postmap</span></div>
          <div class="tile-link"><span>${ui.icon('shield-check', 15)} TLS verification</span><span class="value">TLS 1.2+</span></div>
          <div class="tile-link"><span>${ui.icon('send', 15)} Test path</span><span class="value">STARTTLS / AUTH</span></div>
        </div></section>
      </div>
    </div>`;

  const loadRelay = async () => {
    const host = document.getElementById('relay-body');
    if (!host) return;
    try {
      relay = await ctx.api('/control/api/delivery/relay');
      if (!ctx.isCurrent()) return;
      host.innerHTML = `<form id="relay-form">
        <div class="form-grid">
          ${ui.switchField({ label: 'Use an outbound smarthost', name: 'enabled', checked: !!relay.enabled, help: 'All outbound mail is handed to this relay instead of direct-to-MX delivery.' })}
          <div></div>
          ${ui.field({ label: 'Relay host', name: 'host', value: relay.host || '', placeholder: 'smtp.provider.net', required: false })}
          ${ui.field({ label: 'Port', name: 'port', type: 'number', value: relay.port || 587, min: 1, max: 65535 })}
          ${ui.field({ label: 'Username', name: 'username', value: relay.username || '', autocomplete: 'username' })}
          ${ui.field({ label: 'Password', name: 'password', type: 'password', placeholder: relay.hasPassword ? 'Leave blank to keep the saved password' : 'Relay password', help: relay.hasPassword ? 'A password is already sealed. Leave blank to preserve it.' : 'Required when the relay advertises AUTH.', autocomplete: 'new-password' })}
          ${ui.field({ label: 'Security', name: 'security', type: 'select', value: relay.security || 'starttls', options: [{ value: 'starttls', label: 'STARTTLS' }, { value: 'tls', label: 'Implicit TLS' }, { value: 'none', label: 'No TLS (not recommended)' }] })}
          <div class="field"><label>Credential handling</label><div class="option-list">${ui.switchField({ label: 'Clear the saved password', name: 'clearPassword', checked: false, help: 'Only if you are removing relay authentication.' })}</div></div>
          <div class="span-2 form-actions"><span class="form-note">${relay.lastTestAt ? `Last test ${fmt.dateTime(relay.lastTestAt)} · ${esc(relay.lastTestStatus || 'unknown')}${relay.lastTestDetail ? ` · ${esc(relay.lastTestDetail)}` : ''}` : 'No relay connection test has been recorded.'}</span><div class="form-actions-right"><button type="submit" class="btn btn-primary" ${canManage ? '' : 'disabled'}>Save relay</button></div></div>
        </div>
      </form>
      <div id="relay-test-result"></div>`;
      document.getElementById('relay-form')?.addEventListener('submit', async (event) => {
        event.preventDefault();
        if (!canManage) return;
        const form = event.currentTarget;
        const values = ui.formValues(form);
        const button = form.querySelector('button[type="submit"]');
        button.classList.add('is-loading'); button.disabled = true;
        try {
          await ctx.api('/control/api/delivery/relay', {
            method: 'PUT',
            body: {
              enabled: values.enabled, host: values.host || '', port: Number(values.port || 587),
              username: values.username || '', password: values.password || '',
              clearPassword: !!values.clearPassword, security: values.security || 'starttls',
            },
          });
          ui.toast('Relay configuration saved.', 'good');
          await loadRelay();
        } catch (error) {
          ui.toast(errorText(error), 'critical', { title: 'Could not save relay' });
          button.classList.remove('is-loading'); button.disabled = false;
        }
      });
    } catch (error) {
      host.innerHTML = ui.inlineNotice('critical', 'Could not load relay settings', errorText(error));
    }
  };

  const loadIPs = async () => {
    const host = document.getElementById('ip-body');
    if (!host) return;
    try {
      const payload = await ctx.api('/control/api/delivery/outbound-ips');
      ips = Array.isArray(payload.ips) ? payload.ips : [];
      if (!ctx.isCurrent()) return;
      host.innerHTML = ips.length ? `<div class="table-wrap"><table class="data compact"><thead><tr><th>Address</th><th>Label</th><th>Mode</th><th class="num">Priority</th><th>HELO</th><th>State</th><th></th></tr></thead><tbody>${ips.map((ip) => `<tr><td class="mono">${esc(ip.address)}</td><td>${esc(ip.label || '—')}<span class="cell-sub">${esc(ip.interface || '')}</span></td><td>${ui.badge(ui.humanTitle(ip.mode), ip.mode === 'primary' ? 'good' : ip.mode === 'backup' ? 'info' : 'neutral')}</td><td class="num">${ui.num(ip.priority)}</td><td class="mono">${esc(ip.heloName || '—')}</td><td>${ui.statusPill(ip.active ? 'active' : 'disabled', ip.active ? 'Active' : 'Disabled')}</td><td class="actions">${canManage ? `<button class="icon-btn" type="button" data-ip-edit="${esc(ip.id)}" title="Edit IP" aria-label="Edit IP">${ui.icon('edit', 14)}</button><button class="icon-btn is-danger" type="button" data-ip-delete="${esc(ip.id)}" title="Remove IP" aria-label="Remove IP">${ui.icon('trash', 14)}</button>` : ''}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No outbound IPs registered', 'Discover public interface addresses or add one explicitly.', canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="ip-create-empty">${ui.icon('plus', 13)} Add IP</button>` : '', 'network');
      document.getElementById('ip-create-empty')?.addEventListener('click', () => openIPEditor(null));
      host.querySelectorAll('[data-ip-edit]').forEach((button) => button.addEventListener('click', () => openIPEditor(ips.find((ip) => ip.id === button.dataset.ipEdit))));
      host.querySelectorAll('[data-ip-delete]').forEach((button) => button.addEventListener('click', () => deleteIP(ips.find((ip) => ip.id === button.dataset.ipDelete))));
    } catch (error) { host.innerHTML = ui.inlineNotice('critical', 'Could not load outbound IPs', errorText(error)); }
  };

  const openIPEditor = (ip) => {
    const editing = !!ip;
    const modal = ctx.modal({
      title: editing ? 'Edit outbound IP' : 'Add outbound IP', size: 'medium',
      body: `<form id="ip-form"><div class="form-grid">
        ${ui.field({ label: 'IP address', name: 'address', value: ip?.address || '', placeholder: '203.0.113.10', required: !editing, help: editing ? 'The address is immutable.' : 'Public IPv4 or IPv6 address.', extra: editing ? 'disabled' : '' })}
        ${ui.field({ label: 'Label', name: 'label', value: ip?.label || '', placeholder: 'primary-transport' })}
        ${ui.field({ label: 'Mode', name: 'mode', type: 'select', value: ip?.mode || 'backup', options: [{ value: 'primary', label: 'Primary' }, { value: 'backup', label: 'Backup' }, { value: 'disabled', label: 'Disabled' }] })}
        ${ui.field({ label: 'Priority', name: 'priority', type: 'number', value: ip?.priority || 100, min: 1, help: 'Lower values are preferred.' })}
        ${ui.field({ label: 'Interface', name: 'interface', value: ip?.interface || '', placeholder: 'eth0', help: 'Used for source-address selection and discovery context.' })}
        ${ui.field({ label: 'HELO name', name: 'heloName', value: ip?.heloName || '', placeholder: 'mail.example.com', help: 'Must match the PTR record at your network provider.' })}
        <div class="span-2">${ui.field({ label: 'Notes', name: 'notes', type: 'textarea', value: ip?.notes || '', rows: 2 })}</div>
        <div class="span-2">${ui.switchField({ label: 'Active', name: 'active', checked: ip ? !!ip.active : true, help: 'Inactive addresses are left out of routing decisions.' })}</div>
      </div><div class="form-actions"><span class="form-note">Saving regenerates sender transports and reloads Postfix.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">Save IP</button></div></div></form>`,
    });
    const form = modal.root.querySelector('#ip-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        if (editing) {
          await ctx.api(`/control/api/delivery/outbound-ips/${ip.id}`, { method: 'PATCH', body: { label: values.label, mode: values.mode, priority: Number(values.priority), interfaceName: values.interface, heloName: values.heloName, active: values.active, notes: values.notes } });
        } else {
          await ctx.api('/control/api/delivery/outbound-ips', { method: 'POST', body: { address: values.address, label: values.label, mode: values.mode, priority: Number(values.priority), interface: values.interface, heloName: values.heloName, active: values.active, notes: values.notes } });
        }
        modal.close();
        ui.toast(editing ? 'Outbound IP updated.' : 'Outbound IP added.', 'good');
        await loadIPs();
        await loadRoutes();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not save outbound IP' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  const deleteIP = async (ip) => {
    if (!ip) return;
    const ok = await ctx.confirm({ title: 'Remove outbound IP', message: `Remove ${ip.address} from the outbound registry? Sender routes using it lose their source selection.`, confirmLabel: 'Remove IP', tone: 'danger' });
    if (!ok) return;
    try { await ctx.api(`/control/api/delivery/outbound-ips/${ip.id}`, { method: 'DELETE' }); ui.toast('Outbound IP removed.', 'good'); await loadIPs(); await loadRoutes(); }
    catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not remove IP' }); }
  };

  const loadRoutes = async () => {
    const host = document.getElementById('route-body');
    if (!host) return;
    try {
      const payload = await ctx.api('/control/api/delivery/sender-routes');
      routes = Array.isArray(payload.routes) ? payload.routes : [];
      if (!ctx.isCurrent()) return;
      host.innerHTML = routes.length ? `<div class="table-wrap"><table class="data compact"><thead><tr><th>Sender domain</th><th>Source IP</th><th>HELO</th><th>Transport</th><th>State</th><th></th></tr></thead><tbody>${routes.map((route) => `<tr><td class="mono">${esc(route.senderDomain)}</td><td class="mono">${esc(route.sourceAddress || 'default')}</td><td class="mono">${esc(route.heloName || '—')}</td><td class="mono">${esc(route.transportName || '—')}</td><td>${ui.statusPill(route.active ? 'active' : 'disabled', route.active ? 'Active' : 'Disabled')}</td><td class="actions">${canManage ? `<button class="icon-btn" type="button" data-route-edit="${esc(route.id)}" title="Edit route" aria-label="Edit route">${ui.icon('edit', 14)}</button><button class="icon-btn is-danger" type="button" data-route-delete="${esc(route.id)}" title="Delete route" aria-label="Delete route">${ui.icon('trash', 14)}</button>` : ''}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No sender routes', 'Mail from all domains uses the default outbound path.', canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="route-create-empty">${ui.icon('plus', 13)} Add route</button>` : '', 'relay');
      document.getElementById('route-create-empty')?.addEventListener('click', () => openRouteEditor(null));
      host.querySelectorAll('[data-route-edit]').forEach((button) => button.addEventListener('click', () => openRouteEditor(routes.find((route) => route.id === button.dataset.routeEdit))));
      host.querySelectorAll('[data-route-delete]').forEach((button) => button.addEventListener('click', () => deleteRoute(routes.find((route) => route.id === button.dataset.routeDelete))));
    } catch (error) { host.innerHTML = ui.inlineNotice('critical', 'Could not load sender routes', errorText(error)); }
  };

  const openRouteEditor = (route) => {
    const editing = !!route;
    const modal = ctx.modal({
      title: editing ? 'Edit sender route' : 'Add sender route', size: 'medium',
      body: `<form id="route-form"><div class="form-grid">
        ${ui.field({ label: 'Sender domain', name: 'senderDomain', value: route?.senderDomain || '', placeholder: 'example.com', required: !editing, help: editing ? 'The domain is immutable. Delete and recreate to change it.' : 'Mail with this envelope sender domain is routed through the selected transport.', extra: editing ? 'disabled' : '' })}
        ${ui.field({ label: 'Source IP', name: 'sourceIpId', type: 'select', value: route?.sourceIpId || '', options: [{ value: '', label: 'Default source' }, ...ips.map((ip) => ({ value: ip.id, label: `${ip.address}${ip.label ? ` — ${ip.label}` : ''}` }))] })}
        ${ui.field({ label: 'HELO name', name: 'heloName', value: route?.heloName || '', placeholder: 'mail.example.com' })}
        ${ui.field({ label: 'Transport name', name: 'transportName', value: route?.transportName || '', placeholder: 'smtp-example', help: 'Must begin with smtp- and use lowercase letters, digits and hyphens. Leave blank when not creating a custom transport.' })}
        <div class="span-2">${ui.switchField({ label: 'Active', name: 'active', checked: route ? !!route.active : true })}</div>
      </div><div class="form-actions"><span class="form-note">Saving regenerates <span class="mono">sender_transport</span> and <span class="mono">master.cf</span> and reloads Postfix.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">Save route</button></div></div></form>`,
    });
    const form = modal.root.querySelector('#route-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      const body = { sourceIpId: values.sourceIpId || '', heloName: values.heloName || '', transportName: values.transportName || '', active: values.active };
      try {
        if (editing) await ctx.api(`/control/api/delivery/sender-routes/${route.id}`, { method: 'PATCH', body });
        else await ctx.api('/control/api/delivery/sender-routes', { method: 'POST', body: { ...body, senderDomain: values.senderDomain } });
        modal.close();
        ui.toast(editing ? 'Sender route updated.' : 'Sender route added.', 'good');
        await loadRoutes();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not save sender route' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  const deleteRoute = async (route) => {
    if (!route) return;
    const ok = await ctx.confirm({ title: 'Delete sender route', message: `Remove the outbound route for ${route.senderDomain}? Mail returns to the default transport.`, confirmLabel: 'Delete route', tone: 'danger' });
    if (!ok) return;
    try { await ctx.api(`/control/api/delivery/sender-routes/${route.id}`, { method: 'DELETE' }); ui.toast('Sender route deleted.', 'good'); await loadRoutes(); }
    catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not delete route' }); }
  };

  document.getElementById('relay-refresh')?.addEventListener('click', async () => { await Promise.all([loadRelay(), loadIPs(), loadRoutes()]); });
  document.getElementById('relay-test')?.addEventListener('click', async (event) => {
    const button = event.currentTarget;
    button.classList.add('is-loading'); button.disabled = true;
    const resultHost = document.getElementById('relay-test-result');
    try {
      const result = await ctx.api('/control/api/delivery/relay/test', { method: 'POST' });
      if (resultHost) resultHost.innerHTML = ui.inlineNotice(result.ok ? 'good' : 'critical', result.ok ? 'Relay connection succeeded' : 'Relay connection failed', `${result.detail || ''}${result.error ? ` ${result.error}` : ''}`);
      ui.toast(result.ok ? 'Relay connection succeeded.' : 'Relay test failed.', result.ok ? 'good' : 'critical');
      await loadRelay();
    } catch (error) {
      if (resultHost) resultHost.innerHTML = ui.inlineNotice('critical', 'Relay test failed', errorText(error));
      ui.toast(errorText(error), 'critical', { title: 'Relay test failed' });
    } finally { button.classList.remove('is-loading'); button.disabled = false; }
  });
  document.getElementById('ip-discover')?.addEventListener('click', async (event) => {
    const button = event.currentTarget;
    button.classList.add('is-loading'); button.disabled = true;
    try {
      const result = await ctx.api('/control/api/delivery/outbound-ips/discover', { method: 'POST' });
      ui.toast(result.added?.length ? `${result.added.length} address${result.added.length === 1 ? '' : 'es'} added.` : 'No new public addresses found.', result.added?.length ? 'good' : 'info');
      await loadIPs();
      await loadRoutes();
    } catch (error) { ui.toast(errorText(error), 'critical', { title: 'Discovery failed' }); }
    finally { button.classList.remove('is-loading'); button.disabled = false; }
  });
  document.getElementById('ip-create')?.addEventListener('click', () => openIPEditor(null));
  document.getElementById('route-create')?.addEventListener('click', () => openRouteEditor(null));
  await Promise.all([loadRelay(), loadIPs(), loadRoutes()]);
}

/* =============================================================== suppressions */

export async function renderSuppressions(ctx) {
  const canManage = ctx.can('delivery.manage');
  let direction = '';
  let query = '';
  let rows = [];
  ctx.setTitle('Suppressions', 'Deliverability');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Deliverability', title: 'Suppressions',
    description: 'Outbound recipients that must never receive mail and inbound senders that are blocked at SMTP time.',
    actions: `${canManage ? `<button class="btn btn-primary" type="button" id="suppression-create">${ui.icon('plus', 15)} New suppression</button>` : ''}<button class="btn btn-secondary" type="button" id="suppressions-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    <div class="card">
      <header class="card-head"><div><h3>Suppression map</h3><p id="suppressions-summary">Loading suppressions…</p></div><div class="card-actions"><div class="filterbar" style="margin-bottom:0"><div class="search-box">${ui.icon('search', 14)}<input class="input" id="suppression-search" placeholder="Search pattern or reason" aria-label="Search suppressions"></div><select class="select" id="suppression-direction" aria-label="Filter by direction"><option value="">Both directions</option><option value="outbound">Outbound</option><option value="inbound">Inbound</option></select></div></div></header>
      <div class="card-body" id="suppressions-body">${ui.loadingState('Loading suppressions…', 4)}</div>
    </div>
    <div class="grid grid-main-even mt-4">
      <div class="card"><div class="card-body"><h3 style="font-size:12px">Outbound suppressions</h3><p class="form-note">Applied through a Postfix <span class="mono">smtpd_recipient_restrictions</span> check and regenerated hash map. Use them for complaints, hard bounces and legal exclusions.</p></div></div>
      <div class="card"><div class="card-body"><h3 style="font-size:12px">Inbound suppressions</h3><p class="form-note">Rejected during SMTP conversation, before content inspection or mailbox delivery. Useful for persistent abuse sources, but prefer Rspamd rules for broad patterns.</p></div></div>
    </div>`;

  const load = async () => {
    const body = document.getElementById('suppressions-body');
    if (!body) return;
    try {
      const payload = await ctx.api(`/control/api/delivery/suppressions?direction=${encodeURIComponent(direction)}&q=${encodeURIComponent(query)}`);
      if (!ctx.isCurrent()) return;
      rows = Array.isArray(payload.suppressions) ? payload.suppressions : [];
      const out = rows.filter((row) => row.direction === 'outbound').length;
      const inbound = rows.filter((row) => row.direction === 'inbound').length;
      const expired = rows.filter((row) => row.expired).length;
      document.getElementById('suppressions-summary').textContent = `${rows.length} suppressions · ${out} outbound · ${inbound} inbound · ${expired} expired`;
      body.innerHTML = rows.length ? `<div class="table-wrap is-bare"><table class="data"><thead><tr><th>Direction</th><th>Pattern</th><th>Type</th><th>Reason</th><th>Expires</th><th>State</th><th></th></tr></thead><tbody>${rows.map((row) => `<tr><td>${ui.badge(ui.humanTitle(row.direction), row.direction === 'outbound' ? 'accent' : 'info', row.direction === 'outbound' ? 'send' : 'inbox')}</td><td class="mono">${esc(row.pattern)}</td><td>${ui.badge(ui.humanTitle(row.kind), 'neutral')}</td><td>${esc(row.reason || '—')}</td><td>${row.expiresAt ? esc(fmt.dateOnly(row.expiresAt)) : '<span class="muted">Never</span>'}</td><td>${row.expired ? ui.statusPill('expired') : ui.statusPill('active', 'Active')}</td><td class="actions">${canManage ? `<button class="icon-btn" type="button" data-suppression-edit="${esc(row.id)}" title="Edit suppression" aria-label="Edit suppression">${ui.icon('edit', 14)}</button><button class="icon-btn is-danger" type="button" data-suppression-delete="${esc(row.id)}" title="Delete suppression" aria-label="Delete suppression">${ui.icon('trash', 14)}</button>` : ''}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No suppressions', query || direction ? 'No suppression matches these filters.' : 'The inbound and outbound suppression maps are empty.', canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="suppression-create-empty">${ui.icon('plus', 13)} New suppression</button>` : '', 'shield-off');
      document.getElementById('suppression-create-empty')?.addEventListener('click', () => openEditor(null));
      body.querySelectorAll('[data-suppression-edit]').forEach((button) => button.addEventListener('click', () => openEditor(rows.find((row) => row.id === button.dataset.suppressionEdit))));
      body.querySelectorAll('[data-suppression-delete]').forEach((button) => button.addEventListener('click', () => removeSuppression(rows.find((row) => row.id === button.dataset.suppressionDelete))));
    } catch (error) { body.innerHTML = ui.inlineNotice('critical', 'Could not load suppressions', errorText(error)); }
  };

  const openEditor = (row) => {
    const editing = !!row;
    const modal = ctx.modal({
      title: editing ? 'Edit suppression' : 'New suppression', size: 'medium',
      body: `<form id="suppression-form"><div class="form-grid">
        ${ui.field({ label: 'Direction', name: 'direction', type: 'select', value: row?.direction || 'outbound', options: [{ value: 'outbound', label: 'Outbound — prevent sending to this address' }, { value: 'inbound', label: 'Inbound — reject mail from this sender' }], help: editing ? 'Direction cannot be changed; delete and recreate the suppression.' : 'Direction determines which Postfix map is regenerated.', extra: editing ? 'disabled' : '' })}
        ${ui.field({ label: 'Type', name: 'kind', type: 'select', value: row?.kind || 'address', options: [{ value: 'address', label: 'Email address' }, { value: 'domain', label: 'Whole domain' }, { value: 'catch_all', label: 'Catch-all @domain' }] })}
        <div class="span-2">${ui.field({ label: 'Pattern', name: 'pattern', value: row?.pattern || '', placeholder: 'user@example.com or example.com', required: true, help: 'Address, domain, or @domain for a catch-all. Patterns are normalized to lowercase.' })}</div>
        <div class="span-2">${ui.field({ label: 'Reason', name: 'reason', value: row?.reason || '', placeholder: 'Hard bounce, complaint, legal hold…', help: 'Shown to operators and included in the SMTP rejection text.' })}</div>
        ${ui.field({ label: 'Expires on', name: 'expiresAt', type: 'date', value: row?.expiresAt ? String(row.expiresAt).slice(0, 10) : '', help: 'Optional. Leave blank for no expiry.' })}
      </div><div class="form-actions"><span class="form-note">Saving rewrites both suppression maps and reloads Postfix when they change.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">Save suppression</button></div></div></form>`,
    });
    const form = modal.root.querySelector('#suppression-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        if (editing) await ctx.api(`/control/api/delivery/suppressions/${row.id}`, { method: 'PATCH', body: { pattern: values.pattern, kind: values.kind, reason: values.reason || '', expiresAt: values.expiresAt || '' } });
        else await ctx.api('/control/api/delivery/suppressions', { method: 'POST', body: { direction: values.direction, pattern: values.pattern, kind: values.kind, reason: values.reason || '', expiresAt: values.expiresAt || '' } });
        modal.close();
        ui.toast(editing ? 'Suppression updated.' : 'Suppression created.', 'good');
        await load();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not save suppression' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  const removeSuppression = async (row) => {
    if (!row) return;
    const ok = await ctx.confirm({ title: 'Delete suppression', message: `Remove the ${row.direction} suppression for ${row.pattern}? Mail flow may resume immediately after Postfix reloads.`, confirmLabel: 'Delete suppression', tone: 'danger' });
    if (!ok) return;
    try { await ctx.api(`/control/api/delivery/suppressions/${row.id}`, { method: 'DELETE' }); ui.toast('Suppression deleted.', 'good'); await load(); }
    catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not delete suppression' }); }
  };

  document.getElementById('suppression-create')?.addEventListener('click', () => openEditor(null));
  document.getElementById('suppressions-refresh')?.addEventListener('click', load);
  const search = document.getElementById('suppression-search');
  let timer = null;
  search?.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(() => { query = search.value.trim(); load(); }, 220); });
  ctx.onCleanup(() => clearTimeout(timer));
  document.getElementById('suppression-direction')?.addEventListener('change', (event) => { direction = event.target.value; load(); });
  await load();
}
