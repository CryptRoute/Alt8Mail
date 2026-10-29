/* Domains, mailboxes, aliases and import views. */
import * as ui from './ui.js';

const esc = ui.esc;
const fmt = {
  dateTime: ui.dateTime, dateOnly: ui.dateOnly, timeAgo: ui.timeAgo,
  bytes: ui.bytes, num: ui.num, pct: ui.pct,
};

function errorText(error) {
  if (!error) return 'Unknown error';
  return error.message || String(error);
}

function id(prefix = 'id') {
  return `${prefix}-${(crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random()}`).toString().replace(/[^a-zA-Z0-9-]/g, '').slice(0, 18)}`;
}

function emailLink(email, href) {
  return `<a href="${ui.attr(href)}">${esc(email)}</a>`;
}

/* =============================================================== domains */

export async function renderDomains(ctx) {
  const canManage = ctx.can('mail.manage');
  let query = '';
  let lastData = null;
  ctx.setTitle('Domains & DNS', 'Mail');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Mail',
    title: 'Domains & DNS',
    description: 'Managed domains, DKIM signing material, DNS readiness and rolling outbound caps.',
    actions: `${canManage ? `<button class="btn btn-primary" type="button" id="domain-create">${ui.icon('plus', 15)} New domain</button>` : ''}<button class="btn btn-secondary" type="button" id="domains-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    <div class="grid grid-main">
      <section class="card">
        <header class="card-head"><div><h3>Managed domains</h3><p id="domains-summary">Loading domains…</p></div><div class="card-actions"><div class="search-box">${ui.icon('search', 14)}<input class="input" id="domain-search" placeholder="Search domains" aria-label="Search domains"></div></div></header>
        <div class="card-body" id="domains-body">${ui.loadingState('Loading domains…', 4)}</div>
      </section>
      <div class="stack">
        <section class="card">
          <header class="card-head"><div><h3>Domain readiness</h3><p>What the control plane expects</p></div></header>
          <div class="card-body">
            <div class="tile-link"><span>${ui.icon('lock', 15)} DKIM material</span><span class="value">per domain</span></div>
            <div class="tile-link"><span>${ui.icon('mail', 15)} Postmaster mailbox</span><span class="value">postmaster@</span></div>
            <div class="tile-link"><span>${ui.icon('shield', 15)} SPF · DKIM · DMARC</span><span class="value">DNS plan</span></div>
            <div class="tile-link"><span>${ui.icon('network', 15)} PTR</span><span class="value">provider-side</span></div>
            <div class="note-strip" style="margin-top:11px">${ui.icon('info', 13)} <span>Open a domain to generate its DNS plan, copy records, or inspect live answers. PTR records must be set at the network provider.</span></div>
          </div>
        </section>
        <section class="card">
          <header class="card-head"><div><h3>Domain lifecycle</h3><p>Safe, transactional changes</p></div></header>
          <div class="card-body">
            <div class="timeline">
              <div class="timeline-item"><span class="timeline-dot"></span><strong>Create and verify</strong><span>Add a tenant-scoped domain and generate DKIM.</span></div>
              <div class="timeline-item"><span class="timeline-dot"></span><strong>Publish DNS</strong><span>MX, A/AAAA, SPF, DKIM, DMARC and TLS-RPT.</span></div>
              <div class="timeline-item"><span class="timeline-dot"></span><strong>Provision mailboxes</strong><span>Postmaster and real Maildir accounts.</span></div>
              <div class="timeline-item"><span class="timeline-dot"></span><strong>Rename or delete</strong><span>Previews and typed confirmations protect destructive work.</span></div>
            </div>
          </div>
        </section>
      </div>
    </div>`;

  const load = async (q = query) => {
    query = q;
    const body = document.getElementById('domains-body');
    if (!body) return;
    try {
      const payload = await ctx.api(`/control/api/domains?q=${encodeURIComponent(q)}`);
      if (!ctx.isCurrent()) return;
      lastData = payload;
      const domains = Array.isArray(payload.domains) ? payload.domains : [];
      const dkimReady = domains.filter((d) => d.dkimReady).length;
      const capped = domains.filter((d) => d.capsEnabled).length;
      document.getElementById('domains-summary').textContent = `${domains.length} domain${domains.length === 1 ? '' : 's'} · ${dkimReady} with DKIM · ${capped} with caps`;
      body.innerHTML = domains.length ? `<div class="table-wrap is-bare"><table class="data"><thead><tr><th>Domain</th><th>State</th><th class="num">Mailboxes</th><th class="num">Aliases</th><th>DKIM</th><th>Outgoing caps</th><th>DMARC</th><th></th></tr></thead><tbody>
        ${domains.map((domain) => `<tr class="clickable-row" data-href="#/domains/${esc(domain.id)}">
          <td><span class="cell-main">${domain.primary ? `<span class="primary-star" title="Primary domain">★</span> ` : ''}${esc(domain.domain)}</span><span class="cell-sub">${esc(domain.tenant)}</span></td>
          <td>${ui.statusPill(domain.status, ui.humanTitle(domain.status))}</td>
          <td class="num">${ui.num(domain.mailboxes)}</td>
          <td class="num">${ui.num(domain.aliases)}</td>
          <td>${domain.dkimReady ? ui.badge('Ready', 'good', 'check-circle') : ui.badge('Missing', 'warning', 'alert')}</td>
          <td>${domain.capsEnabled ? `<span class="mono">${ui.num(domain.capHour)}h / ${ui.num(domain.capDay)}d / ${ui.num(domain.capMonth)}m</span>` : ui.badge('Not set', 'neutral')}</td>
          <td>${ui.badge(String(domain.dmarcPolicy || 'none').toUpperCase(), domain.dmarcPolicy === 'reject' ? 'good' : domain.dmarcPolicy === 'quarantine' ? 'warning' : 'neutral')}</td>
          <td class="actions"><a class="icon-btn" href="#/domains/${esc(domain.id)}" title="Open domain" aria-label="Open ${esc(domain.domain)}">${ui.icon('chevron-right', 16)}</a></td>
        </tr>`).join('')}</tbody></table></div>` : ui.emptyState('No domains yet', 'Add the first mail domain to begin issuing mailboxes and DNS plans.', canManage ? `<button class="btn btn-primary" type="button" id="domain-create-empty">${ui.icon('plus', 14)} Add domain</button>` : '', 'globe');
      const empty = document.getElementById('domain-create-empty');
      if (empty) empty.addEventListener('click', openCreateDomain);
      body.querySelectorAll('tr[data-href]').forEach((row) => row.addEventListener('click', (event) => {
        if (event.target.closest('a,button,input,select,label')) return;
        ctx.navigate(row.dataset.href.replace(/^#\//, ''));
      }));
    } catch (error) {
      body.innerHTML = ui.inlineNotice('critical', 'Could not load domains', errorText(error));
    }
  };

  const openCreateDomain = async () => {
    let tenants = [];
    try {
      const tenantPayload = await ctx.api('/control/api/tenants');
      tenants = Array.isArray(tenantPayload.tenants) ? tenantPayload.tenants : [];
    } catch (error) {
      ui.toast(errorText(error), 'critical', { title: 'Could not load tenants' });
      return;
    }
    const modal = ctx.modal({
      title: 'Create mail domain',
      subtitle: 'A tenant-scoped domain with DKIM material and an optional postmaster mailbox.',
      size: 'medium',
      body: `<form id="domain-create-form">
        <div class="form-grid">
          ${ui.field({ label: 'Domain name', name: 'domain', placeholder: 'example.com', required: true, help: 'Apex domain only. Do not include a subdomain unless it is the mail domain you intend to manage.', autocomplete: 'off' })}
          ${ui.field({ label: 'Tenant', name: 'tenantId', type: 'select', required: true, options: tenants.map((tenant) => ({ value: tenant.id, label: `${tenant.name} (${tenant.slug})` })) })}
          ${ui.field({ label: 'DKIM selector', name: 'selector', value: 'a8m', required: true, help: 'The selector appears as <selector>._domainkey in DNS. Lowercase letters, digits, dots and hyphens.' })}
          <div class="field"><label>Options</label><div class="option-list">${ui.switchField({ label: 'Make this the primary domain', name: 'isPrimary', help: 'Used for default mail-from addresses and the webmail identity.' })}${ui.switchField({ label: 'Create postmaster mailbox', name: 'createPostmaster', checked: true, help: 'Creates postmaster@<domain> with a generated password when a mailbox name is available.' })}</div></div>
        </div>
        <div class="form-actions"><span class="form-note">Creation writes to PostgreSQL, generates DKIM keys and triggers a Postfix reload.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button type="submit" class="btn btn-primary">Create domain</button></div></div>
      </form>`,
    });
    const form = modal.root.querySelector('#domain-create-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading');
      button.disabled = true;
      try {
        const result = await ctx.api('/control/api/domains', {
          method: 'POST',
          body: {
            domain: values.domain,
            tenantId: values.tenantId,
            selector: values.selector || 'a8m',
            isPrimary: values.isPrimary,
            createPostmaster: values.createPostmaster,
          },
        });
        modal.close();
        ui.toast(`Domain ${result.domain} created.`, 'good');
        if (result.warnings?.length) result.warnings.forEach((warning) => ui.toast(warning, 'warning', { title: 'Domain created with warnings' }));
        await load('');
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not create domain' });
        button.classList.remove('is-loading');
        button.disabled = false;
      }
    });
  };

  document.getElementById('domain-create')?.addEventListener('click', openCreateDomain);
  document.getElementById('domains-refresh')?.addEventListener('click', () => load(query));
  const search = document.getElementById('domain-search');
  let timer = null;
  search?.addEventListener('input', () => {
    clearTimeout(timer);
    timer = setTimeout(() => load(search.value.trim()), 220);
  });
  ctx.onCleanup(() => clearTimeout(timer));
  await load('');
}

export async function renderDomainDetail(ctx) {
  const domainId = ctx.params.id;
  const canManage = ctx.can('mail.manage');
  let detail;
  let dnsPlan = null;
  let dnsLoading = false;
  let activeTab = 'overview';
  try {
    detail = await ctx.api(`/control/api/domains/${domainId}`);
  } catch (error) {
    ctx.root.innerHTML = ui.errorState(errorText(error));
    return;
  }
  if (!ctx.isCurrent()) return;
  ctx.setTitle(detail.domain, 'Domains & DNS');
  const usage = detail.usage || {};
  const caps = detail.caps || {};
  const aliasCounts = detail.aliasCounts || {};
  const mailboxes = Array.isArray(detail.mailboxes) ? detail.mailboxes : [];
  const capPercent = (used, limit) => (Number(limit) > 0 ? Math.min(100, (Number(used) / Number(limit)) * 100) : 0);
  const statusTone = (status) => status === 'pass' ? 'good' : status === 'warn' ? 'warning' : status === 'provider' ? 'info' : 'critical';

  const dnsMarkup = () => {
    if (dnsLoading) return ui.loadingState('Querying public DNS…', 4);
    if (!dnsPlan) return `${ui.emptyState('DNS has not been checked yet', 'Run a live check to compare the expected records with the current public answers. Lookups use the resolver configured in Server & network.', `<button class="btn btn-primary btn-sm" type="button" id="dns-check">${ui.icon('search', 13)} Check live DNS</button>`, 'network')}`;
    return `<div class="notice tone-info mb-3">${ui.icon('info', 16)}<div><strong>Live answers checked ${esc(fmt.timeAgo(dnsPlan.checkedAt))}</strong><p>Mail host ${esc(dnsPlan.mailHost)} · IPv4 ${esc(dnsPlan.ipv4 || 'not detected')} · IPv6 ${esc(dnsPlan.ipv6 || 'not configured')} · selector ${esc(dnsPlan.selector)}</p></div></div>
      <div class="table-wrap"><table class="data"><thead><tr><th>Type</th><th>Name</th><th>Recommended value</th><th>Status</th><th></th></tr></thead><tbody>
      ${dnsPlan.plan.map((record) => `<tr><td><span class="badge tone-neutral">${esc(record.type)}</span></td><td class="mono">${esc(record.name)}</td><td><div class="dns-value">${esc(record.value)}</div><div class="dns-purpose">${esc(record.purpose)}</div>${record.actual ? `<div class="dns-actual">Live: ${esc(record.actual)}</div>` : ''}${record.note ? `<div class="notice tone-info" style="margin-top:6px">${ui.icon('info', 14)}<div><p>${esc(record.note)}</p></div></div>` : ''}</td><td>${ui.statusPill(record.status, record.status === 'provider' ? 'Provider action' : record.status === 'pass' ? 'Verified' : record.status === 'warn' ? 'Review' : 'Missing')}</td><td class="actions"><button class="icon-btn" type="button" data-copy="${ui.attr(record.value)}" title="Copy recommended value" aria-label="Copy value">${ui.icon('copy', 14)}</button></td></tr>`).join('')}</tbody></table></div>
      <div class="form-actions" style="border-top:0;padding-top:10px"><span class="form-note">Your DNS provider may use different names for the apex or mail host. PTR is always provider-side.</span><div class="form-actions-right"><button class="btn btn-secondary btn-sm" type="button" id="dns-export">${ui.icon('download', 13)} Export plan</button><a class="btn btn-secondary btn-sm" href="/control/api/domains/${esc(domainId)}/dns/export">${ui.icon('download', 13)} Download .txt</a></div></div>
      <details class="mt-3"><summary class="muted" style="cursor:pointer;font-size:11px">Show copy-ready zone lines</summary><div class="mt-2">${ui.codeBlock(dnsPlan.export, { language: 'dns zone' })}</div></details>`;
  };

  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Domains & DNS',
    title: detail.domain,
    description: `Tenant ${detail.tenantId} · added ${fmt.dateOnly(detail.createdAt)}`,
    meta: `${detail.primary ? ui.badge('Primary domain', 'warning', 'check-circle') : ui.badge('Secondary domain', 'neutral')} ${ui.statusPill(detail.status, ui.humanTitle(detail.status))} ${detail.dkimError ? ui.badge('DKIM error', 'critical', 'alert') : ''}`,
    actions: `<a class="btn btn-ghost" href="#/domains">${ui.icon('chevron-left', 15)} Domains</a>${canManage && !detail.primary ? `<button class="btn btn-secondary" type="button" id="domain-primary">${ui.icon('star', 14)} Set primary</button>` : ''}<button class="btn btn-secondary" type="button" id="domain-refresh">${ui.icon('refresh', 15)} Refresh</button>${canManage ? `<button class="btn btn-soft-danger" type="button" id="domain-delete">${ui.icon('trash', 14)} Delete</button>` : ''}`,
  })}
    <div class="tabs" id="domain-tabs">
      <button class="tab is-active" type="button" data-domain-tab="overview">${ui.icon('dashboard', 15)} Overview</button>
      <button class="tab" type="button" data-domain-tab="dns">${ui.icon('network', 15)} DNS plan</button>
      <button class="tab" type="button" data-domain-tab="sending">${ui.icon('send', 15)} Sending caps</button>
      <button class="tab" type="button" data-domain-tab="mailboxes">${ui.icon('mail', 15)} Mailboxes <span class="tab-count">${mailboxes.length}</span></button>
      ${canManage ? `<button class="tab" type="button" data-domain-tab="danger">${ui.icon('alert', 15)} Rename & delete</button>` : ''}
    </div>
    <div class="stack">
      <section data-domain-panel="overview">
        <div class="grid grid-main">
          <div class="stack">
            <section class="card">
              <header class="card-head"><div><h3>Domain overview</h3><p>Identity, readiness and current volume</p></div>${detail.primary ? ui.badge('Primary', 'warning', 'star') : ''}</header>
              <div class="card-body">
                ${ui.kv([
                  ['Domain', `<span class="mono">${esc(detail.domain)}</span>`],
                  ['Tenant', `<span class="mono">${esc(detail.tenantId)}</span>`],
                  ['State', ui.statusPill(detail.status, ui.humanTitle(detail.status))],
                  ['DMARC policy', ui.badge(String(detail.dmarcPolicy || 'none').toUpperCase(), detail.dmarcPolicy === 'reject' ? 'good' : detail.dmarcPolicy === 'quarantine' ? 'warning' : 'neutral')],
                  ['Created', esc(fmt.dateTime(detail.createdAt))],
                  ['Mailboxes', ui.num(mailboxes.length)],
                  ['Alias identities', ui.num(aliasCounts.identities || 0)],
                  ['Usage this hour', ui.num(usage.hour)],
                  ['Usage today', ui.num(usage.day)],
                  ['Usage this month', ui.num(usage.month)],
                ])}
                ${detail.notes ? `<div class="divider"></div><p class="form-note">${esc(detail.notes)}</p>` : ''}
              </div>
            </section>
            <section class="card">
              <header class="card-head"><div><h3>DKIM signing</h3><p>Selector ${esc(detail.dkimSelector || 'not set')}</p></div>${detail.dkimRecord ? ui.badge('Key ready', 'good', 'check-circle') : ui.badge('Key missing', 'critical', 'alert')}</header>
              <div class="card-body">
                ${detail.dkimError ? `<div class="notice tone-critical">${ui.icon('alert-circle', 16)}<div><strong>DKIM error</strong><p>${esc(detail.dkimError)}</p></div></div>` : ''}
                ${detail.dkimRecord ? ui.codeBlock(detail.dkimRecord, { language: 'TXT record' }) : `<div class="notice tone-warning">${ui.icon('alert', 16)}<div><strong>No DKIM record</strong><p>Generate a key before publishing DNS or sending mail for this domain.</p></div></div>`}
                <div class="form-actions" style="border-top:0;padding-top:10px"><span class="form-note">Publish this value at <span class="mono">${esc(detail.dkimSelector)}._domainkey.${esc(detail.domain)}</span>.</span><div class="form-actions-right">${canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="dkim-regenerate">${ui.icon('refresh', 13)} Regenerate key</button>` : ''}</div></div>
              </div>
            </section>
          </div>
          <div class="stack">
            <section class="card">
              <header class="card-head"><div><h3>Sending allowance</h3><p>Rolling caps shown against real usage</p></div><a class="btn btn-ghost btn-sm" href="#" data-domain-tab="sending">Edit ${ui.icon('chevron-right', 12)}</a></header>
              <div class="card-body">
                ${caps.enabled ? `<div class="meter-stack">
                  <div>${ui.meter({ label: 'This hour', value: capPercent(usage.hour, caps.hour + caps.burst), detail: `${ui.num(usage.hour)} used · cap ${ui.num(caps.hour + caps.burst)}` })}</div>
                  <div>${ui.meter({ label: 'Today', value: capPercent(usage.day, caps.day), detail: `${ui.num(usage.day)} used · cap ${ui.num(caps.day)}` })}</div>
                  <div>${ui.meter({ label: 'This month', value: capPercent(usage.month, caps.month), detail: `${ui.num(usage.month)} used · cap ${ui.num(caps.month)}` })}</div>
                </div>` : `<div class="notice tone-warning">${ui.icon('alert', 16)}<div><strong>Rolling caps are not enabled</strong><p>Mail from this domain is limited only by Postfix rate limits. Enable caps for burst protection.</p></div></div>`}
              </div>
            </section>
            <section class="card">
              <header class="card-head"><div><h3>DNS readiness</h3><p>Expected records for this domain</p></div><button class="btn btn-ghost btn-sm" type="button" data-domain-tab="dns">Open plan ${ui.icon('chevron-right', 12)}</button></header>
              <div class="card-body">
                <div class="tile-link"><span>${ui.icon('mail', 15)} MX</span><span class="value">${esc(detail.domain)}</span></div>
                <div class="tile-link"><span>${ui.icon('shield', 15)} SPF</span><span class="value">required</span></div>
                <div class="tile-link"><span>${ui.icon('lock', 15)} DKIM</span><span class="value">${detail.dkimRecord ? 'ready' : 'missing'}</span></div>
                <div class="tile-link"><span>${ui.icon('shield-check', 15)} DMARC</span><span class="value">${esc(detail.dmarcPolicy || 'none')}</span></div>
                <div class="tile-link"><span>${ui.icon('network', 15)} PTR</span><span class="value">provider-side</span></div>
              </div>
            </section>
          </div>
        </div>
      </section>
      <section data-domain-panel="dns" hidden>
        <div class="card"><header class="card-head"><div><h3>DNS plan &amp; live verification</h3><p>Compare the recommended record set with current public answers</p></div><div class="card-actions"><button class="btn btn-secondary btn-sm" type="button" id="dns-recheck">${ui.icon('refresh', 13)} Re-check</button></div></header><div class="card-body" id="dns-body">${dnsMarkup()}</div></div>
      </section>
      <section data-domain-panel="sending" hidden>
        <div class="grid grid-main">
          <section class="card">
            <header class="card-head"><div><h3>Rolling outgoing caps</h3><p>Enforced by the Postfix policy daemon inside PostgreSQL</p></div>${caps.enabled ? ui.badge('Enabled', 'good', 'shield-check') : ui.badge('Disabled', 'neutral')}</header>
            <div class="card-body">
              <form id="caps-form" class="form-grid">
                ${ui.switchField({ label: 'Enforce rolling caps', name: 'capsEnabled', checked: !!caps.enabled, help: 'Caps apply to authenticated and locally submitted mail from this domain, never to inbound external mail.' })}
                <div></div>
                ${ui.field({ label: 'Hourly cap', name: 'capHour', type: 'number', value: caps.hour || 0, min: 0, help: 'Maximum accepted recipients per rolling hour. 0 means no hourly limit.' })}
                ${ui.field({ label: 'Burst allowance', name: 'burst', type: 'number', value: caps.burst || 0, min: 0, help: 'Extra recipients added to the hourly cap for short spikes.' })}
                ${ui.field({ label: 'Daily cap', name: 'capDay', type: 'number', value: caps.day || 0, min: 0, help: 'Maximum accepted recipients in 24 rolling hours. 0 disables.' })}
                ${ui.field({ label: 'Monthly cap', name: 'capMonth', type: 'number', value: caps.month || 0, min: 0, help: 'Maximum accepted recipients in 30 rolling days. 0 disables.' })}
                <div class="span-2">${ui.field({ label: 'DMARC policy', name: 'dmarcPolicy', type: 'select', value: detail.dmarcPolicy || 'none', options: [
                  { value: 'none', label: 'none — monitor only' },
                  { value: 'quarantine', label: 'quarantine — send suspicious mail to spam' },
                  { value: 'reject', label: 'reject — refuse failing mail' },
                ], help: 'Changing DMARC requires a DNS update; the plan tab regenerates the record text.' })}</div>
                <div class="span-2">${ui.field({ label: 'Notes', name: 'notes', type: 'textarea', value: detail.notes || '', rows: 3, help: 'Visible to panel operators only.' })}</div>
                <div class="span-2 form-actions"><span class="form-note">Usage is counted from the mail event log, not estimated from DNS or queue state.</span><div class="form-actions-right"><button class="btn btn-primary" type="submit">${ui.icon('check', 14)} Save domain policy</button></div></div>
              </form>
            </div>
          </section>
          <section class="card">
            <header class="card-head"><div><h3>Current usage</h3><p>Rolling windows, accepted mail</p></div></header>
            <div class="card-body">
              <div class="meter-stack">
                <div>${ui.meter({ label: 'Hour', value: capPercent(usage.hour, caps.enabled ? Math.max(1, caps.hour + caps.burst) : 100), detail: `${ui.num(usage.hour)} accepted recipients` })}</div>
                <div>${ui.meter({ label: 'Day', value: capPercent(usage.day, caps.enabled ? Math.max(1, caps.day) : 100), detail: `${ui.num(usage.day)} accepted recipients` })}</div>
                <div>${ui.meter({ label: 'Month', value: capPercent(usage.month, caps.enabled ? Math.max(1, caps.month) : 100), detail: `${ui.num(usage.month)} accepted recipients` })}</div>
              </div>
              <div class="note-strip" style="margin-top:14px">${ui.icon('info', 13)} <span>When a cap is reached, Postfix replies with a temporary 4.7.1 deferral. The message remains queued at the sender and can retry later.</span></div>
            </div>
          </section>
        </div>
      </section>
      <section data-domain-panel="mailboxes" hidden>
        <div class="card"><header class="card-head"><div><h3>Mailboxes on ${esc(detail.domain)}</h3><p>Accounts attached to this domain</p></div><a class="btn btn-secondary btn-sm" href="#/mailboxes">All mailboxes ${ui.icon('arrow-up-right', 12)}</a></header><div class="card-body is-flush">${mailboxes.length ? `<div class="table-wrap is-bare"><table class="data"><thead><tr><th>Mailbox</th><th>Provisioning</th><th class="num">Used</th><th></th></tr></thead><tbody>${mailboxes.map((box) => `<tr><td><span class="cell-main">${esc(box.email)}</span><span class="cell-sub">${esc(box.localPart)}</span></td><td>${ui.statusPill(box.active ? 'active' : 'disabled', box.active ? 'Active' : 'Disabled')} ${ui.badge(ui.humanTitle(box.provisionStatus), box.provisionStatus === 'ready' ? 'good' : box.provisionStatus === 'error' ? 'critical' : 'warning')}</td><td class="num">${ui.bytes(box.usedBytes)}</td><td class="actions"><a class="icon-btn" href="#/mailboxes/${esc(box.id)}" title="Open mailbox" aria-label="Open mailbox">${ui.icon('chevron-right', 15)}</a></td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No mailboxes yet', 'Create a mailbox from the Mailboxes section.', `<a class="btn btn-secondary btn-sm" href="#/mailboxes">Open mailboxes</a>`, 'mail')}</div></div>
      </section>
      ${canManage ? `<section data-domain-panel="danger" hidden><div class="grid grid-main-even">
        <section class="card"><header class="card-head"><div><h3>Rename domain</h3><p>Rewrites every address and Maildir in one transaction</p></div></header><div class="card-body">
          <div class="notice tone-warning">${ui.icon('alert', 16)}<div><strong>This is a visible address change</strong><p>Mailboxes, users, aliases, groups, identities, forwarding targets and Maildirs are rewritten. DNS and mail clients must be updated.</p></div></div>
          <form id="rename-form" class="mt-3"><div class="form-grid is-single">${ui.field({ label: 'New domain name', name: 'newDomain', placeholder: 'new.example.com', required: true, help: `Current domain: ${detail.domain}` })}<div class="form-actions"><span class="form-note">${ui.num(mailboxes.length)} mailbox${mailboxes.length === 1 ? '' : 'es'} and their aliases will move.</span><div class="form-actions-right"><button class="btn btn-secondary" type="submit">Preview rename</button></div></div></div></form>
        </div></section>
        <section class="card is-danger"><header class="card-head"><div><h3>Delete domain</h3><p>Archive Maildirs, then remove all managed records</p></div></header><div class="card-body">
          <p class="form-note">Deleting removes users, mailboxes, aliases, groups, message copies and DKIM material. Maildirs are archived under <span class="mono">/var/backups/alt8mail/deleted-domains</span> when possible.</p>
          <div class="notice tone-critical mt-3">${ui.icon('alert-circle', 16)}<div><strong>Irreversible from the panel</strong><p>Restoring requires filesystem and database access, not an undo button.</p></div></div>
          <div class="form-actions"><span class="form-note">A typed confirmation is required.</span><div class="form-actions-right"><button class="btn btn-danger" type="button" id="delete-domain-action">${ui.icon('trash', 14)} Delete domain</button></div></div>
        </div></section>
      </div></section>` : ''}
    </div>`;

  const setTab = (name) => {
    activeTab = name;
    ctx.root.querySelectorAll('[data-domain-tab]').forEach((button) => {
      if (button.closest('#domain-tabs')) button.classList.toggle('is-active', button.dataset.domainTab === name);
    });
    ctx.root.querySelectorAll('[data-domain-panel]').forEach((panel) => { panel.hidden = panel.dataset.domainPanel !== name; });
    if (name === 'dns') {
      const dnsBody = document.getElementById('dns-body');
      if (dnsBody && !dnsPlan && !dnsLoading) {
        dnsBody.innerHTML = dnsMarkup();
        bindDns();
      }
    }
  };
  ctx.root.querySelectorAll('#domain-tabs [data-domain-tab]').forEach((button) => button.addEventListener('click', () => setTab(button.dataset.domainTab)));
  ctx.root.querySelectorAll('a[data-domain-tab],button[data-domain-tab]').forEach((button) => {
    if (button.closest('#domain-tabs')) return;
    button.addEventListener('click', (event) => { event.preventDefault(); setTab(button.dataset.domainTab); });
  });

  const bindDns = () => {
    document.getElementById('dns-check')?.addEventListener('click', runDns);
    document.getElementById('dns-recheck')?.addEventListener('click', runDns);
    document.getElementById('dns-export')?.addEventListener('click', () => {
      if (!dnsPlan) return;
      ui.copy(dnsPlan.export, 'DNS plan copied');
    });
  };

  const runDns = async () => {
    const body = document.getElementById('dns-body');
    if (!body) return;
    dnsLoading = true;
    body.innerHTML = dnsMarkup();
    try {
      const payload = await ctx.api(`/control/api/domains/${domainId}/dns`);
      dnsPlan = payload;
    } catch (error) {
      body.innerHTML = ui.inlineNotice('critical', 'DNS check failed', errorText(error)) + `<div class="mt-2"><button class="btn btn-secondary btn-sm" type="button" id="dns-retry">${ui.icon('refresh', 13)} Try again</button></div>`;
      body.querySelector('#dns-retry')?.addEventListener('click', runDns);
      dnsLoading = false;
      return;
    }
    dnsLoading = false;
    if (!ctx.isCurrent()) return;
    body.innerHTML = dnsMarkup();
    bindDns();
  };
  bindDns();

  const refresh = async () => {
    await renderDomainDetail(ctx);
  };

  document.getElementById('domain-refresh')?.addEventListener('click', refresh);
  document.getElementById('domain-primary')?.addEventListener('click', async () => {
    const ok = await ctx.confirm({ title: 'Set primary domain', message: `Make ${detail.domain} the primary mail domain? The previous primary becomes secondary.`, confirmLabel: 'Set primary', tone: 'primary' });
    if (!ok) return;
    try {
      await ctx.api(`/control/api/domains/${domainId}/primary`, { method: 'POST' });
      ui.toast(`${detail.domain} is now the primary domain.`, 'good');
      await refresh();
    } catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not set primary domain' }); }
  });

  document.getElementById('dkim-regenerate')?.addEventListener('click', () => {
    const modal = ctx.modal({
      title: 'Regenerate DKIM key', subtitle: 'The selector changes the DNS record name; the previous key remains on disk until replaced.',
      size: 'small',
      body: `<form id="dkim-form"><div class="form-grid is-single">${ui.field({ label: 'DKIM selector', name: 'selector', value: detail.dkimSelector || 'a8m', required: true, help: 'Lowercase letters, digits, dots and hyphens. The public TXT record is replaced.' })}<div class="form-actions"><span class="form-note">Rspamd is reloaded after the key is written.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">Generate key</button></div></div></div></form>`,
    });
    const form = modal.root.querySelector('#dkim-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        const result = await ctx.api(`/control/api/domains/${domainId}/dkim`, { method: 'POST', body: { selector: form.querySelector('[name="selector"]').value } });
        modal.close();
        ui.toast(`DKIM key generated with selector ${result.selector}.`, 'good');
        await refresh();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'DKIM generation failed' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  });

  document.getElementById('caps-form')?.addEventListener('submit', async (event) => {
    event.preventDefault();
    const form = event.currentTarget;
    const values = ui.formValues(form);
    const button = form.querySelector('button[type="submit"]');
    button.classList.add('is-loading'); button.disabled = true;
    try {
      await ctx.api(`/control/api/domains/${domainId}`, {
        method: 'PATCH',
        body: {
          capsEnabled: values.capsEnabled,
          capHour: Number(values.capHour || 0),
          capDay: Number(values.capDay || 0),
          capMonth: Number(values.capMonth || 0),
          burst: Number(values.burst || 0),
          dmarcPolicy: values.dmarcPolicy,
          notes: values.notes || '',
        },
      });
      ui.toast('Domain policy saved.', 'good');
      await refresh();
    } catch (error) {
      ui.toast(errorText(error), 'critical', { title: 'Could not save domain policy' });
      button.classList.remove('is-loading'); button.disabled = false;
    }
  });

  document.getElementById('rename-form')?.addEventListener('submit', async (event) => {
    event.preventDefault();
    const form = event.currentTarget;
    const newDomain = form.querySelector('[name="newDomain"]').value.trim().toLowerCase();
    if (!newDomain || !newDomain.includes('.')) { ui.toast('Enter a valid new domain name.', 'critical'); return; }
    if (newDomain === detail.domain) { ui.toast('The new domain is the same as the current domain.', 'warning'); return; }
    const modal = ctx.modal({
      title: 'Review domain rename', subtitle: 'Check the scope before rewriting live addresses.',
      size: 'medium',
      body: `<div class="notice tone-warning">${ui.icon('alert', 16)}<div><strong>${esc(detail.domain)} → ${esc(newDomain)}</strong><p>This transaction rewrites all child records and renames the Maildir domain directory. It cannot be undone from the panel.</p></div></div>
        <div class="confirm-details"><div class="kv-grid">${ui.kv([['Mailboxes', ui.num(mailboxes.length)], ['Alias identities', ui.num(aliasCounts.identities || 0)], ['Forwarders', ui.num(aliasCounts.forwarders || 0)], ['Current domain', `<span class="mono">${esc(detail.domain)}</span>`], ['New domain', `<span class="mono">${esc(newDomain)}</span>`]])}</div></div>
        <div class="field mt-3"><label for="rename-confirm">Type <strong class="mono">${esc(detail.domain)}</strong> to confirm</label><input class="input" id="rename-confirm" name="confirm" autocomplete="off"></div>`,
      footer: `<button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button type="button" class="btn btn-danger" id="rename-apply">Rename domain</button>`,
    });
    modal.root.querySelector('#rename-apply')?.addEventListener('click', async (event) => {
      const input = modal.root.querySelector('#rename-confirm');
      if (input.value.trim().toLowerCase() !== detail.domain) {
        input.classList.add('is-invalid');
        ui.toast('Type the current domain exactly to confirm.', 'critical');
        return;
      }
      const button = event.currentTarget;
      button.classList.add('is-loading'); button.disabled = true;
      try {
        await ctx.api(`/control/api/domains/${domainId}`, { method: 'PATCH', body: { domain: newDomain } });
        modal.close();
        ui.toast(`Domain renamed to ${newDomain}.`, 'good');
        await refresh();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Rename failed' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  });

  document.getElementById('delete-domain-action')?.addEventListener('click', async () => {
    let preview;
    try { preview = await ctx.api(`/control/api/domains/${domainId}/delete-preview`); }
    catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not build delete preview' }); return; }
    const modal = ctx.modal({
      title: 'Delete domain', subtitle: 'Review every object that will be removed or archived.', size: 'medium',
      body: `<div class="notice tone-critical">${ui.icon('alert-circle', 16)}<div><strong>${esc(preview.domain)}</strong><p>${esc(preview.warning)}</p></div></div>
        <div class="confirm-details"><div class="kv-grid">
          <div><dt>Mailboxes</dt><dd>${ui.num(preview.mailboxes)}</dd></div>
          <div><dt>Users</dt><dd>${ui.num(preview.users)}</dd></div>
          <div><dt>Aliases</dt><dd>${ui.num(preview.aliases)} + ${ui.num(preview.controlAliases)}</dd></div>
          <div><dt>Groups</dt><dd>${ui.num(preview.groups)}</dd></div>
          <div><dt>Messages</dt><dd>${ui.num(preview.messages)}</dd></div>
          <div><dt>Maildir size</dt><dd>${ui.bytes(preview.maildirBytes)}</dd></div>
        </div></div>
        <p class="form-note mt-3">Maildir: <span class="mono">${esc(preview.maildirPath)}</span><br>DKIM keys: ${ui.num(preview.dkimKeys?.length || 0)}</p>
        <div class="field mt-3"><label for="delete-domain-confirm">Type <strong class="mono">${esc(preview.domain)}</strong> to confirm</label><input class="input" id="delete-domain-confirm" autocomplete="off"></div>`,
      footer: `<button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button type="button" class="btn btn-danger" id="delete-domain-apply">Delete ${esc(preview.domain)}</button>`,
    });
    modal.root.querySelector('#delete-domain-apply')?.addEventListener('click', async (event) => {
      const input = modal.root.querySelector('#delete-domain-confirm');
      if (input.value.trim().toLowerCase() !== preview.domain) {
        input.classList.add('is-invalid');
        ui.toast('Type the exact domain to confirm deletion.', 'critical');
        return;
      }
      const button = event.currentTarget;
      button.classList.add('is-loading'); button.disabled = true;
      try {
        const result = await ctx.api(`/control/api/domains/${domainId}`, {
          method: 'DELETE', body: { confirmDomain: input.value.trim(), confirmToken: preview.confirmToken },
        });
        modal.close();
        ui.toast(result.archive ? `Domain deleted. Maildirs archived at ${result.archive}.` : 'Domain deleted.', 'good');
        ctx.navigate('domains');
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Delete failed' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  });
}

/* =============================================================== mailboxes */

export async function renderMailboxes(ctx) {
  const canManage = ctx.can('mail.manage');
  let query = '';
  let statusFilter = 'all';
  let sort = 'email';
  let dir = 'asc';
  let page = 1;
  let pageSize = 100;
  ctx.setTitle('Mailboxes', 'Mail');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Mail',
    title: 'Mailboxes',
    description: 'Real Dovecot accounts with quotas, provisioning state, password probes and history.',
    actions: `${canManage ? `<button class="btn btn-primary" type="button" id="mailbox-create">${ui.icon('plus', 15)} New mailbox</button>` : ''}<button class="btn btn-secondary" type="button" id="mailboxes-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    <div class="card">
      <header class="card-head"><div><h3>Accounts</h3><p id="mailboxes-summary">Loading mailboxes…</p></div><div class="card-actions mailbox-head-actions"><div class="search-box">${ui.icon('search', 14)}<input class="input" id="mailbox-search" placeholder="Search address or name" aria-label="Search mailboxes" autocomplete="off"></div></div></header>
      <div class="mailbox-toolbar" aria-label="Mailbox filters">
        <label class="mailbox-filter"><span>Status</span><select class="input" id="mailbox-status"><option value="all">All statuses</option><option value="enabled">Enabled</option><option value="suspended">Suspended / disabled</option></select></label>
        <label class="mailbox-filter"><span>Sort by</span><select class="input" id="mailbox-sort"><option value="email">Mailbox address</option><option value="name">Display name</option><option value="status">Status</option><option value="quota">Quota used</option><option value="used">Storage used</option><option value="lastLogin">Last sign-in</option><option value="created">Created</option></select></label>
        <button class="btn btn-secondary btn-sm mailbox-dir" type="button" id="mailbox-dir" title="Toggle sort direction" aria-label="Sort ascending">${ui.icon('sort', 14)} <span id="mailbox-dir-label">Ascending</span></button>
        <label class="mailbox-filter mailbox-page-size"><span>Rows</span><select class="input" id="mailbox-page-size"><option value="100">100</option><option value="150">150</option><option value="250">250</option><option value="500">500</option></select></label>
      </div>
      <div class="card-body" id="mailboxes-body">${ui.loadingState('Loading mailboxes…', 5)}</div>
      <div class="mailbox-pagination" id="mailbox-pagination"></div>
    </div>`;

  const load = async ({ resetPage = false } = {}) => {
    if (resetPage) page = 1;
    const body = document.getElementById('mailboxes-body');
    if (!body) return;
    try {
      const params = new URLSearchParams({ q: query, status: statusFilter, sort, dir, page: String(page), pageSize: String(pageSize) });
      const payload = await ctx.api(`/control/api/mailboxes?${params.toString()}`);
      if (!ctx.isCurrent()) return;
      page = Number(payload.page) || 1;
      const boxes = Array.isArray(payload.mailboxes) ? payload.mailboxes : [];
      const summary = payload.summary || {};
      const totalAccounts = Number(summary.total ?? payload.total ?? boxes.length);
      document.getElementById('mailboxes-summary').textContent = `${ui.num(totalAccounts)} account${totalAccounts === 1 ? '' : 's'} · ${ui.num(summary.enabled || 0)} enabled · ${ui.num(summary.suspended || 0)} suspended · ${ui.num(summary.nearQuota || 0)} near quota`;
      body.innerHTML = boxes.length ? `<div class="table-wrap is-bare"><table class="data"><thead><tr><th>Mailbox</th><th>Name</th><th>Status</th><th>Quota</th><th>Provisioning</th><th>Last sign-in</th><th></th></tr></thead><tbody>
        ${boxes.map((box) => {
          const percent = box.quotaBytes > 0 ? Math.min(100, (box.usedBytes / box.quotaBytes) * 100) : 0;
          const quotaTone = percent > 95 ? 'critical' : percent > 85 ? 'warning' : 'good';
          return `<tr class="clickable-row" data-href="#/mailboxes/${esc(box.id)}">
            <td><span class="cell-main">${esc(box.email)}</span><span class="cell-sub">${esc(box.domain)}</span></td>
            <td>${esc(box.name || '—')}</td>
            <td>${ui.statusPill(box.enabled ? 'active' : box.status === 'active' ? 'disabled' : box.status, box.enabled ? 'Enabled' : box.status === 'active' ? 'Disabled' : ui.humanTitle(box.status))}</td>
            <td><div style="min-width:120px">${ui.progress(percent, quotaTone)}<span class="cell-sub">${ui.bytes(box.usedBytes)} of ${ui.bytes(box.quotaBytes)}</span></div></td>
            <td>${ui.badge(ui.humanTitle(box.provisionStatus), box.provisionStatus === 'ready' ? 'good' : box.provisionStatus === 'error' ? 'critical' : 'warning', box.provisionStatus === 'ready' ? 'check-circle' : 'alert')}</td>
            <td>${box.lastLoginAt ? `<span title="${ui.attr(fmt.dateTime(box.lastLoginAt))}">${esc(fmt.timeAgo(box.lastLoginAt))}</span>` : '<span class="muted">Never</span>'}</td>
            <td class="actions"><a class="icon-btn" href="#/mailboxes/${esc(box.id)}" title="Open mailbox" aria-label="Open mailbox">${ui.icon('chevron-right', 16)}</a></td>
          </tr>`;
        }).join('')}</tbody></table></div>` : `<div class="empty-state"><div class="empty-icon">${ui.icon('mail', 22)}</div><h3>No mailboxes found</h3><p>${query ? 'No account matches that search.' : 'Create the first mailbox to begin receiving mail.'}</p>${canManage && !query ? `<button class="btn btn-primary btn-sm" type="button" id="mailbox-create-empty">${ui.icon('plus', 14)} New mailbox</button>` : ''}</div>`;
      document.getElementById('mailbox-create-empty')?.addEventListener('click', openCreateMailbox);
      body.querySelectorAll('tr[data-href]').forEach((row) => row.addEventListener('click', (event) => {
        if (event.target.closest('a,button,input,select,label')) return;
        ctx.navigate(row.dataset.href.replace(/^#\//, ''));
      }));
      renderPagination(payload);
    } catch (error) {
      body.innerHTML = ui.inlineNotice('critical', 'Could not load mailboxes', errorText(error));
      const pagination = document.getElementById('mailbox-pagination');
      if (pagination) pagination.innerHTML = '';
    }
  };

  const renderPagination = (payload) => {
    const host = document.getElementById('mailbox-pagination');
    if (!host) return;
    const total = Number(payload.total) || 0;
    const pages = Math.max(1, Number(payload.pages) || 1);
    const currentPage = Number(payload.page) || 1;
    const size = Number(payload.pageSize) || pageSize;
    const start = total ? (currentPage - 1) * size + 1 : 0;
    const end = Math.min(total, currentPage * size);
    if (!total) { host.innerHTML = ''; return; }
    const numbers = new Set([1, pages]);
    for (let value = Math.max(2, currentPage - 2); value <= Math.min(pages - 1, currentPage + 2); value += 1) numbers.add(value);
    const sorted = Array.from(numbers).sort((a, b) => a - b);
    let pageButtons = '';
    let previous = 0;
    for (const value of sorted) {
      if (previous && value - previous > 1) pageButtons += '<span class="page-gap" aria-hidden="true">…</span>';
      pageButtons += `<button class="page-btn${value === currentPage ? ' is-active' : ''}" type="button" data-mailbox-page="${value}" ${value === currentPage ? 'aria-current="page"' : ''} aria-label="Page ${value}">${value}</button>`;
      previous = value;
    }
    host.innerHTML = `<span class="pagination-summary">Showing <strong>${ui.num(start)}–${ui.num(end)}</strong> of <strong>${ui.num(total)}</strong></span><div class="pagination-controls"><button class="btn btn-ghost btn-sm" type="button" id="mailbox-prev" ${currentPage <= 1 ? 'disabled' : ''}>${ui.icon('chevron-left', 13)} Previous</button>${pageButtons}<button class="btn btn-ghost btn-sm" type="button" id="mailbox-next" ${currentPage >= pages ? 'disabled' : ''}>Next ${ui.icon('chevron-right', 13)}</button></div><span class="pagination-page">Page ${currentPage} of ${pages}</span>`;
    host.querySelectorAll('[data-mailbox-page]').forEach((button) => button.addEventListener('click', () => {
      page = Number(button.dataset.mailboxPage);
      load();
    }));
    document.getElementById('mailbox-prev')?.addEventListener('click', () => { if (page > 1) { page -= 1; load(); } });
    document.getElementById('mailbox-next')?.addEventListener('click', () => { if (page < pages) { page += 1; load(); } });
  };

  const openCreateMailbox = async () => {
    let domains = [];
    try {
      const payload = await ctx.api('/control/api/domains');
      domains = (payload.domains || []).filter((domain) => domain.status === 'manual' || domain.status === 'verified');
    } catch (error) {
      ui.toast(errorText(error), 'critical', { title: 'Could not load domains' });
      return;
    }
    if (!domains.length) { ui.toast('Create a managed domain before adding a mailbox.', 'warning'); return; }
    const modal = ctx.modal({
      title: 'Create mailbox', subtitle: 'The account is provisioned in Dovecot with a generated or supplied password.',
      size: 'medium',
      body: `<form id="mailbox-create-form">
        <div class="form-grid">
          ${ui.field({ label: 'Display name', name: 'name', placeholder: 'Ada Lovelace', required: true, autocomplete: 'off' })}
          ${ui.field({ label: 'Domain', name: 'domainId', type: 'select', required: true, options: domains.map((domain) => ({ value: domain.id, label: domain.domain })) })}
          ${ui.field({ label: 'Local part', name: 'localPart', placeholder: 'ada', required: true, help: 'The address becomes <local-part>@<domain>.' })}
          ${ui.field({ label: 'Quota (GB)', name: 'quotaGb', type: 'number', value: '5', min: '0.25', step: '0.25', help: 'Between 0.25 GB and 1 TB.' })}
          <div class="span-2">${ui.field({ label: 'Password (optional)', name: 'password', type: 'password', help: 'Leave blank to generate a temporary password. Minimum 12 characters if supplied.', autocomplete: 'new-password' })}</div>
          <div class="span-2">${ui.field({ label: 'Email the temporary password to', name: 'sendTo', type: 'email', placeholder: 'alternate@example.com', help: 'Optional alternate address. The password is never stored in clear text.' })}</div>
        </div>
        <div class="form-actions"><span class="form-note">Creation writes webmail, Dovecot and Maildir state. The account can sign in immediately.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button type="submit" class="btn btn-primary">Create mailbox</button></div></div>
      </form>`,
    });
    const form = modal.root.querySelector('#mailbox-create-form');
    form?.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        const quotaGb = Number(values.quotaGb || 5);
        if (!Number.isFinite(quotaGb) || quotaGb < 0.25 || quotaGb > 1024) { ui.toast('Quota must be between 0.25 GB and 1 TB.', 'critical'); throw new Error('quota'); }
        const result = await ctx.api('/control/api/mailboxes', {
          method: 'POST',
          body: {
            name: values.name, localPart: values.localPart, domainId: values.domainId,
            quotaBytes: Math.round(quotaGb * 1024 * 1024 * 1024),
            password: values.password || '', sendTo: values.sendTo || '',
          },
        });
        modal.close();
        ui.toast(`Mailbox ${result.email} created.`, 'good');
        if (result.temporaryPassword) showCredentialModal(ctx, 'Mailbox created', result.email, result.temporaryPassword, result.message, result.emailed);
        await load({ resetPage: true });
      } catch (error) {
        if (error.message !== 'quota') ui.toast(errorText(error), 'critical', { title: 'Could not create mailbox' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  document.getElementById('mailbox-create')?.addEventListener('click', openCreateMailbox);
  document.getElementById('mailboxes-refresh')?.addEventListener('click', () => load());
  const search = document.getElementById('mailbox-search');
  let timer = null;
  search?.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(() => { query = search.value.trim(); load({ resetPage: true }); }, 220); });
  ctx.onCleanup(() => clearTimeout(timer));
  document.getElementById('mailbox-status')?.addEventListener('change', (event) => { statusFilter = event.target.value; load({ resetPage: true }); });
  document.getElementById('mailbox-sort')?.addEventListener('change', (event) => {
    sort = event.target.value;
    dir = ['quota', 'used', 'lastLogin', 'created'].includes(sort) ? 'desc' : 'asc';
    syncDirection();
    load({ resetPage: true });
  });
  const syncDirection = () => {
    const label = document.getElementById('mailbox-dir-label');
    if (label) label.textContent = dir === 'asc' ? 'Ascending' : 'Descending';
    document.getElementById('mailbox-dir')?.setAttribute('aria-label', dir === 'asc' ? 'Sort ascending' : 'Sort descending');
  };
  document.getElementById('mailbox-dir')?.addEventListener('click', () => { dir = dir === 'asc' ? 'desc' : 'asc'; syncDirection(); load({ resetPage: true }); });
  document.getElementById('mailbox-page-size')?.addEventListener('change', (event) => { pageSize = Number(event.target.value) || 100; load({ resetPage: true }); });
  syncDirection();
  await load();
}

function showCredentialModal(ctx, title, email, password, message, emailed = false) {
  const modal = ctx.modal({
    title, subtitle: 'Copy this password now. The panel does not retain it in clear text.',
    size: 'small',
    body: `<div class="notice tone-good">${ui.icon('check-circle', 16)}<div><strong>${esc(email)}</strong><p>${esc(message || 'The account is ready.')}</p></div></div>
      <div class="field" style="margin-top:14px"><label>Account password</label><div class="input-group"><input class="input mono" readonly value="${ui.attr(password)}" id="credential-password"><button class="btn btn-secondary" type="button" data-copy="${ui.attr(password)}">${ui.icon('copy', 14)} Copy</button></div><small class="field-help">${emailed ? 'A copy was also sent to the alternate address.' : 'Store this in your password manager, then dismiss this window.'}</small></div>`,
    footer: `<button class="btn btn-primary" type="button" data-modal-close>I have saved it</button>`,
  });
  const input = modal.root.querySelector('#credential-password');
  input?.select();
}

export async function renderMailboxDetail(ctx) {
  const mailboxId = ctx.params.id;
  const canManage = ctx.can('mail.manage');
  let mailbox;
  try {
    const payload = await ctx.api(`/control/api/mailboxes/${mailboxId}`);
    mailbox = payload.mailbox;
  } catch (error) {
    ctx.root.innerHTML = ui.errorState(errorText(error));
    return;
  }
  if (!ctx.isCurrent()) return;
  ctx.setTitle(mailbox.email, 'Mailboxes');
  const quotaPercent = mailbox.quotaBytes > 0 ? Math.min(100, (mailbox.usedBytes / mailbox.quotaBytes) * 100) : 0;
  const quotaTone = quotaPercent > 95 ? 'critical' : quotaPercent > 85 ? 'warning' : 'good';
  const domainRoot = mailbox.maildir || '';
  let activeTab = 'overview';
  let historyLoaded = false;
  let sieveLoaded = false;
  let sieveState = null;

  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Mailbox',
    title: mailbox.email,
    description: `${mailbox.name || 'Unnamed account'} · ${mailbox.domain}`,
    meta: `${ui.statusPill(mailbox.status === 'active' && mailbox.active ? 'active' : mailbox.status === 'suspended' ? 'disabled' : mailbox.status, mailbox.status === 'active' && mailbox.active ? 'Enabled' : ui.humanTitle(mailbox.status))} ${ui.badge(ui.humanTitle(mailbox.provisionStatus), mailbox.provisionStatus === 'ready' ? 'good' : mailbox.provisionStatus === 'error' ? 'critical' : 'warning')} ${mailbox.lastLoginAt ? `<span>Last sign-in ${esc(fmt.timeAgo(mailbox.lastLoginAt))}</span>` : '<span>Never signed in</span>'}`,
    actions: `<a class="btn btn-ghost" href="#/mailboxes">${ui.icon('chevron-left', 15)} Mailboxes</a><button class="btn btn-secondary" type="button" id="mailbox-refresh">${ui.icon('refresh', 15)} Refresh</button>${canManage ? `<button class="btn btn-soft-danger" type="button" id="mailbox-delete">${ui.icon('trash', 14)} Delete mailbox</button>` : ''}`,
  })}
    <div class="tabs" id="mailbox-tabs">
      <button class="tab is-active" type="button" data-mailbox-tab="overview">${ui.icon('user', 15)} Overview</button>
      <button class="tab" type="button" data-mailbox-tab="password">${ui.icon('key', 15)} Password</button>
      <button class="tab" type="button" data-mailbox-tab="history">${ui.icon('clock', 15)} History</button>
      <button class="tab" type="button" data-mailbox-tab="sieve">${ui.icon('filter', 15)} Sieve rules</button>
      ${canManage ? `<button class="tab" type="button" data-mailbox-tab="danger">${ui.icon('alert', 15)} Delete</button>` : ''}
    </div>
    <div class="stack">
      <section data-mailbox-panel="overview">
        <div class="grid grid-main">
          <section class="card"><header class="card-head"><div><h3>Account settings</h3><p>Display name, quota and account state</p></div></header><div class="card-body">
            <form id="mailbox-settings-form" class="form-grid">
              ${ui.field({ label: 'Display name', name: 'name', value: mailbox.name || '', required: true })}
              ${ui.field({ label: 'Status', name: 'status', type: 'select', value: mailbox.status === 'suspended' ? 'suspended' : 'active', options: [{ value: 'active', label: 'Active' }, { value: 'suspended', label: 'Suspended' }], help: 'Suspending the account also disables the mailbox.' })}
              ${ui.field({ label: 'Quota (GB)', name: 'quotaGb', type: 'number', value: (mailbox.quotaBytes / (1024 ** 3)).toFixed(2), min: '0.25', step: '0.25', required: true, help: 'Minimum 256 MB, maximum 1 TB.' })}
              <div class="field"><label>Mailbox state</label><div class="note-strip" style="min-height:34px">${mailbox.active ? ui.statusPill('active', 'Mailbox enabled') : ui.statusPill('disabled', 'Mailbox disabled')}</div></div>
              <div class="span-2 form-actions"><span class="form-note">Passwords are managed from the Password tab with a Dovecot login probe.</span><div class="form-actions-right"><button class="btn btn-primary" type="submit" ${canManage ? '' : 'disabled'}>${ui.icon('check', 14)} Save settings</button></div></div>
            </form>
          </div></section>
          <div class="stack">
            <section class="card"><header class="card-head"><div><h3>Storage</h3><p>Quota and provisioning</p></div></header><div class="card-body">
              ${ui.meter({ label: 'Mailbox quota', value: quotaPercent, tone: quotaTone, detail: `${ui.bytes(mailbox.usedBytes)} used of ${ui.bytes(mailbox.quotaBytes)}` })}
              <div class="divider"></div>
              ${ui.kv([
                ['Provision status', ui.badge(ui.humanTitle(mailbox.provisionStatus), mailbox.provisionStatus === 'ready' ? 'good' : mailbox.provisionStatus === 'error' ? 'critical' : 'warning')],
                ['Maildir', `<span class="mono break-all">${esc(mailbox.maildir || '—')}</span>`],
                ['Created', esc(fmt.dateTime(mailbox.createdAt))],
                ['Last sign-in', esc(fmt.dateTime(mailbox.lastLoginAt))],
                ['User record', `<span class="mono">${esc(mailbox.userId)}</span>`],
              ])}
            </div></section>
            <section class="card"><header class="card-head"><div><h3>Quick paths</h3><p>Related management</p></div></header><div class="card-body">
              <div class="tile-link"><span>${ui.icon('globe', 15)} Domain</span><a class="value" href="#/domains/${esc(mailbox.domain)}">${esc(mailbox.domain)}</a></div>
              <div class="tile-link"><span>${ui.icon('filter', 15)} Sieve rules</span><button class="value link-btn" type="button" data-mailbox-tab="sieve">Open</button></div>
              <div class="tile-link"><span>${ui.icon('clock', 15)} Mail history</span><button class="value link-btn" type="button" data-mailbox-tab="history">Open</button></div>
            </div></section>
          </div>
        </div>
      </section>
      <section data-mailbox-panel="password" hidden>
        <div class="grid grid-main-even">
          <section class="card"><header class="card-head"><div><h3>Reset password</h3><p>Hash is verified against Dovecot before it is kept</p></div></header><div class="card-body">
            ${canManage ? `<form id="mailbox-password-form" class="form-grid is-single">
              ${ui.field({ label: 'New password (optional)', name: 'password', type: 'password', help: 'Leave blank to generate. Minimum 12 characters if supplied.', autocomplete: 'new-password' })}
              ${ui.field({ label: 'Send the password to', name: 'sendTo', type: 'email', placeholder: 'alternate@example.com', help: 'Optional alternate address. The mailbox itself receives no copy unless you enter it.' })}
              <div class="form-actions"><span class="form-note">The new hash is rolled back if <span class="mono">doveadm auth test</span> fails.</span><div class="form-actions-right"><button class="btn btn-primary" type="submit">${ui.icon('key', 14)} Reset and probe</button></div></div>
            </form>` : `<div class="notice tone-warning">${ui.icon('lock', 16)}<div><strong>Read-only role</strong><p>Your role cannot reset mailbox passwords.</p></div></div>`}
          </div></section>
          <section class="card"><header class="card-head"><div><h3>How reset works</h3></div></header><div class="card-body">
            <div class="timeline">
              <div class="timeline-item"><span class="timeline-dot"></span><strong>Generate or validate</strong><span>Passwords shorter than 12 characters are rejected.</span></div>
              <div class="timeline-item"><span class="timeline-dot"></span><strong>Hash for web and mail</strong><span>Argon2id for web sign-in, SHA-512-CRYPT for Dovecot.</span></div>
              <div class="timeline-item"><span class="timeline-dot"></span><strong>Probe Dovecot</strong><span><span class="mono">doveadm auth test</span> proves the new hash works.</span></div>
              <div class="timeline-item"><span class="timeline-dot"></span><strong>Optionally deliver</strong><span>The temporary password can go to an alternate address.</span></div>
            </div>
          </div></section>
        </div>
      </section>
      <section data-mailbox-panel="history" hidden><div class="card"><header class="card-head"><div><h3>Message history</h3><p>Retained event log for this address</p></div><button class="btn btn-secondary btn-sm" type="button" id="history-refresh">${ui.icon('refresh', 13)} Refresh</button></header><div class="card-body" id="history-body">${ui.loadingState('Loading history…', 4)}</div></div></section>
      <section data-mailbox-panel="sieve" hidden><div class="card"><header class="card-head"><div><h3>Managed Sieve rules</h3><p>Deployed as the <span class="mono">${esc('alt8mail-control')}</span> script only; custom scripts are never clobbered</p></div><div class="card-actions"><button class="btn btn-secondary btn-sm" type="button" id="sieve-preview">${ui.icon('eye', 13)} Preview script</button>${canManage ? `<button class="btn btn-primary btn-sm" type="button" id="sieve-save">${ui.icon('check', 13)} Deploy rules</button>` : ''}</div></header><div class="card-body" id="sieve-body">${ui.loadingState('Loading Sieve rules…', 4)}</div></div></section>
      ${canManage ? `<section data-mailbox-panel="danger" hidden><div class="card is-danger"><header class="card-head"><div><h3>Delete mailbox</h3><p>Archive the Maildir, then remove the account</p></div></header><div class="card-body">
        <div class="notice tone-critical">${ui.icon('alert-circle', 16)}<div><strong>This cannot be undone from the panel</strong><p>The Maildir directory is moved to the deleted-mailboxes archive when possible. The user record and all account state are removed.</p></div></div>
        <div class="form-actions"><span class="form-note">Type the full address to confirm.</span><div class="form-actions-right"><button class="btn btn-danger" type="button" id="delete-mailbox-action">${ui.icon('trash', 14)} Delete mailbox</button></div></div>
      </div></div></section>` : ''}
    </div>`;

  const setTab = (name) => {
    activeTab = name;
    ctx.root.querySelectorAll('#mailbox-tabs [data-mailbox-tab]').forEach((button) => button.classList.toggle('is-active', button.dataset.mailboxTab === name));
    ctx.root.querySelectorAll('[data-mailbox-panel]').forEach((panel) => { panel.hidden = panel.dataset.mailboxPanel !== name; });
    if (name === 'history' && !historyLoaded) loadHistory();
    if (name === 'sieve' && !sieveLoaded) loadSieve();
  };
  ctx.root.querySelectorAll('#mailbox-tabs [data-mailbox-tab]').forEach((button) => button.addEventListener('click', () => setTab(button.dataset.mailboxTab)));
  ctx.root.querySelectorAll('button[data-mailbox-tab]').forEach((button) => {
    if (button.closest('#mailbox-tabs')) return;
    button.addEventListener('click', () => setTab(button.dataset.mailboxTab));
  });

  document.getElementById('mailbox-settings-form')?.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (!canManage) return;
    const form = event.currentTarget;
    const values = ui.formValues(form);
    const quotaGb = Number(values.quotaGb);
    if (!Number.isFinite(quotaGb) || quotaGb < 0.25 || quotaGb > 1024) { ui.toast('Quota must be between 0.25 GB and 1 TB.', 'critical'); return; }
    const button = form.querySelector('button[type="submit"]');
    button.classList.add('is-loading'); button.disabled = true;
    try {
      await ctx.api(`/control/api/mailboxes/${mailboxId}`, { method: 'PATCH', body: { name: values.name, status: values.status, quotaBytes: Math.round(quotaGb * 1024 ** 3) } });
      ui.toast('Mailbox settings saved.', 'good');
      await renderMailboxDetail(ctx);
    } catch (error) {
      ui.toast(errorText(error), 'critical', { title: 'Could not save mailbox' });
      button.classList.remove('is-loading'); button.disabled = false;
    }
  });
  document.getElementById('mailbox-refresh')?.addEventListener('click', () => renderMailboxDetail(ctx));

  document.getElementById('mailbox-password-form')?.addEventListener('submit', async (event) => {
    event.preventDefault();
    const form = event.currentTarget;
    const values = ui.formValues(form);
    const ok = await ctx.confirm({ title: 'Reset mailbox password', message: `Set a new password for ${mailbox.email}? This immediately changes webmail and mail client credentials.`, confirmLabel: 'Reset password', tone: 'danger' });
    if (!ok) return;
    const button = form.querySelector('button[type="submit"]');
    button.classList.add('is-loading'); button.disabled = true;
    try {
      const result = await ctx.api(`/control/api/mailboxes/${mailboxId}/password`, { method: 'POST', body: { password: values.password || '', sendTo: values.sendTo || '' } });
      showCredentialModal(ctx, 'Password reset verified', mailbox.email, result.temporaryPassword, result.message, result.emailed);
      form.reset();
    } catch (error) {
      ui.toast(errorText(error), 'critical', { title: 'Password reset failed' });
    } finally {
      button.classList.remove('is-loading'); button.disabled = false;
    }
  });

  const loadHistory = async () => {
    const body = document.getElementById('history-body');
    if (!body) return;
    try {
      const payload = await ctx.api(`/control/api/mailboxes/${mailboxId}/history?limit=250`);
      if (!ctx.isCurrent()) return;
      historyLoaded = true;
      const rows = Array.isArray(payload.history) ? payload.history : [];
      body.innerHTML = rows.length ? `<div class="notice tone-info mb-3">${ui.icon('clock', 15)}<div><strong>Retention: ${ui.num(payload.retentionDays)} days</strong><p>Showing the latest ${ui.num(rows.length)} events involving this address.</p></div></div><div class="table-wrap"><table class="data"><thead><tr><th>Time</th><th>Direction</th><th>Status</th><th>Sender</th><th>Recipient</th><th>Subject</th><th class="num">Size</th><th>Detail</th></tr></thead><tbody>${rows.map((row) => `<tr><td class="nowrap-cell">${esc(fmt.dateTime(row.at))}</td><td>${ui.badge(ui.humanTitle(row.direction), row.direction === 'inbound' ? 'info' : row.direction === 'outbound' ? 'accent' : 'neutral')}</td><td>${ui.statusPill(row.status)}</td><td>${esc(row.sender || '—')}</td><td>${esc(row.recipient || '—')}</td><td>${esc(ui.truncate(row.subject || '—', 54))}</td><td class="num">${ui.bytes(row.sizeBytes)}</td><td><button class="icon-btn" type="button" data-history-detail="${row.id}" title="Event detail" aria-label="Event detail">${ui.icon('eye', 14)}</button></td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No message history', 'Events are recorded as mail flows through Postfix and Dovecot.', '', 'clock');
      body.querySelectorAll('[data-history-detail]').forEach((button) => button.addEventListener('click', () => {
        const row = rows.find((item) => String(item.id) === button.dataset.historyDetail);
        if (!row) return;
        ctx.modal({ title: 'Mail event detail', size: 'medium', body: `<div class="kv-grid is-single">${ui.kv([
          ['Time', esc(fmt.dateTime(row.at))], ['Direction', esc(row.direction)], ['Status', ui.statusPill(row.status)],
          ['Sender', `<span class="mono">${esc(row.sender)}</span>`], ['Recipient', `<span class="mono">${esc(row.recipient)}</span>`],
          ['Subject', esc(row.subject || '—')], ['Queue ID', `<span class="mono">${esc(row.queueId || '—')}</span>`],
          ['Size', ui.bytes(row.sizeBytes)], ['Delay', `${ui.num(row.delaySeconds)}s`],
        ])}</div>${row.detail ? `<div class="divider"></div>${ui.codeBlock(row.detail, { language: 'postfix event' })}` : ''}` });
      }));
    } catch (error) {
      body.innerHTML = ui.inlineNotice('critical', 'Could not load history', errorText(error));
    }
  };
  document.getElementById('history-refresh')?.addEventListener('click', loadHistory);

  /* ---- Sieve editor ---- */
  const newCondition = () => ({ field: 'subject', operator: 'contains', value: '' });
  const newRule = () => ({ id: id('rule'), name: 'New rule', enabled: true, match: 'all', conditions: [newCondition()], action: { type: 'move', value: 'INBOX' } });
  const actionOptions = [
    { value: 'move', label: 'Move to folder' }, { value: 'redirect', label: 'Redirect a copy' },
    { value: 'markRead', label: 'Mark as read' }, { value: 'star', label: 'Flag / star' },
    { value: 'discard', label: 'Discard' }, { value: 'keep', label: 'Keep in inbox' },
  ];
  const fieldOptions = [{ value: 'from', label: 'From' }, { value: 'to', label: 'To' }, { value: 'subject', label: 'Subject' }, { value: 'body', label: 'Body' }, { value: 'size', label: 'Size (KB)' }];
  const operatorOptions = (field) => field === 'size'
    ? [{ value: 'over', label: 'over' }, { value: 'under', label: 'under' }]
    : [{ value: 'contains', label: 'contains' }, { value: 'is', label: 'is' }, { value: 'matches', label: 'matches' }, { value: 'notcontains', label: 'does not contain' }];

  const renderSieveEditor = () => {
    const rules = sieveState?.rules || [];
    const host = document.getElementById('sieve-editor');
    if (!host) return;
    host.innerHTML = `<div class="row-gap">${rules.length ? rules.map((rule, index) => `<article class="card" style="box-shadow:none">
      <header class="card-head" style="padding:11px 13px 0"><div style="display:flex;align-items:center;gap:8px;min-width:0"><span class="badge tone-neutral">${index + 1}</span><input class="input" style="max-width:280px;height:29px;font-size:11.5px" data-rule="${index}" data-key="name" value="${ui.attr(rule.name)}" aria-label="Rule name"></div><div class="card-actions">${ui.switchField({ label: 'Enabled', name: `enabled-${index}`, checked: rule.enabled })}<button class="icon-btn is-danger" type="button" data-sieve-remove="${index}" title="Remove rule" aria-label="Remove rule">${ui.icon('trash', 14)}</button></div></header>
      <div class="card-body" style="padding:11px 13px 13px">
        <div class="filterbar" style="margin-bottom:10px"><span class="muted" style="font-size:10.5px">Match</span><select class="input" style="height:29px;font-size:11px;min-width:90px" data-rule="${index}" data-key="match"><option value="all"${rule.match === 'all' ? ' selected' : ''}>all conditions</option><option value="any"${rule.match === 'any' ? ' selected' : ''}>any condition</option></select><span class="muted" style="font-size:10.5px">then</span><select class="input" style="height:29px;font-size:11px;min-width:130px" data-rule="${index}" data-key="actionType">${actionOptions.map((opt) => `<option value="${opt.value}"${rule.action.type === opt.value ? ' selected' : ''}>${opt.label}</option>`).join('')}</select>${['move', 'redirect'].includes(rule.action.type) ? `<input class="input" style="height:29px;font-size:11px;min-width:150px" data-rule="${index}" data-key="actionValue" value="${ui.attr(rule.action.value)}" placeholder="${rule.action.type === 'move' ? 'Folder name' : 'address@example.com'}" aria-label="Action value">` : ''}</div>
        <div class="mini-list">${rule.conditions.map((condition, cIndex) => `<div class="inline-form" style="gap:6px"><select class="input" style="height:28px;font-size:10.5px;width:105px;flex:none" data-rule="${index}" data-cond="${cIndex}" data-key="field">${fieldOptions.map((opt) => `<option value="${opt.value}"${condition.field === opt.value ? ' selected' : ''}>${opt.label}</option>`).join('')}</select><select class="input" style="height:28px;font-size:10.5px;width:120px;flex:none" data-rule="${index}" data-cond="${cIndex}" data-key="operator">${operatorOptions(condition.field).map((opt) => `<option value="${opt.value}"${condition.operator === opt.value ? ' selected' : ''}>${opt.label}</option>`).join('')}</select><input class="input" style="height:28px;font-size:10.5px;flex:1;min-width:120px" data-rule="${index}" data-cond="${cIndex}" data-key="value" value="${ui.attr(condition.value)}" placeholder="${condition.field === 'size' ? 'KB' : 'Value'}" aria-label="Condition value"><button class="icon-btn is-danger" type="button" data-sieve-remove-cond="${index}" data-cond="${cIndex}" title="Remove condition" aria-label="Remove condition" ${rule.conditions.length === 1 ? 'disabled' : ''}>${ui.icon('x', 13)}</button></div>`).join('')}</div>
        <button class="btn btn-ghost btn-sm" type="button" data-sieve-add-cond="${index}" style="margin-top:7px">${ui.icon('plus', 12)} Add condition</button>
      </div>
    </article>`).join('') : ui.emptyState('No managed rules', 'Add a rule to route, flag, redirect or discard matching mail. Saving with no rules removes the managed Sieve script.', '', 'filter')}</div>
    <div class="form-actions"><span class="form-note">${rules.filter((r) => r.enabled).length} enabled rule${rules.filter((r) => r.enabled).length === 1 ? '' : 's'} · script name <span class="mono">alt8mail-control</span></span><div class="form-actions-right">${canManage ? `<button class="btn btn-secondary" type="button" data-sieve-add>${ui.icon('plus', 14)} Add rule</button>` : ''}</div></div>`;
  };

  const loadSieve = async () => {
    const body = document.getElementById('sieve-body');
    if (!body) return;
    try {
      const payload = await ctx.api(`/control/api/mailboxes/${mailboxId}/sieve`);
      if (!ctx.isCurrent()) return;
      sieveLoaded = true;
      sieveState = payload;
      body.innerHTML = `<div class="notice ${payload.managed ? 'tone-good' : 'tone-info'}">${ui.icon(payload.managed ? 'check-circle' : 'info', 16)}<div><strong>${payload.managed ? 'Managed Sieve script is active' : 'No managed script deployed'}</strong><p>${payload.managed ? 'The rule editor owns only the alt8mail-control script. Other scripts remain untouched.' : 'Custom scripts uploaded through ManageSieve are left exactly as they are. Saving rules here deploys the managed script.'}</p></div></div>
        ${payload.dovecotScripts ? `<div class="mt-2"><span class="form-note">Dovecot scripts: <span class="mono">${esc(payload.dovecotScripts)}</span></span></div>` : ''}
        <div id="sieve-editor" class="mt-3"></div>
        <details class="mt-3"><summary class="muted" style="cursor:pointer;font-size:11px">Rendered Sieve script</summary><div class="mt-2">${ui.codeBlock(payload.script || '# No managed script', { language: 'sieve' })}</div></details>`;
      renderSieveEditor();
    } catch (error) {
      body.innerHTML = ui.inlineNotice('critical', 'Could not load Sieve rules', errorText(error));
    }
  };

  ctx.root.querySelector('#sieve-body')?.addEventListener('input', (event) => {
    const target = event.target;
    const ruleIndex = Number(target.dataset.rule);
    if (!Number.isInteger(ruleIndex) || !sieveState?.rules?.[ruleIndex]) return;
    const rule = sieveState.rules[ruleIndex];
    const key = target.dataset.key;
    if (target.dataset.cond !== undefined) {
      const cond = rule.conditions[Number(target.dataset.cond)];
      if (!cond) return;
      cond[key] = target.value;
      if (key === 'field') {
        cond.field = target.value;
        cond.operator = operatorOptions(cond.field)[0].value;
        cond.value = '';
        renderSieveEditor();
      }
      return;
    }
    if (key === 'name') rule.name = target.value;
    if (key === 'match') rule.match = target.value;
    if (key === 'actionType') {
      rule.action.type = target.value;
      rule.action.value = ['move', 'redirect'].includes(target.value) ? (target.value === 'move' ? 'INBOX' : '') : '';
      renderSieveEditor();
    }
    if (key === 'actionValue') rule.action.value = target.value;
  });
  ctx.root.querySelector('#sieve-body')?.addEventListener('change', (event) => {
    const target = event.target;
    if (target.matches('input[type="checkbox"][name^="enabled-"]')) {
      const index = Number(target.name.split('-')[1]);
      if (sieveState?.rules?.[index]) sieveState.rules[index].enabled = target.checked;
    }
    if (target.dataset.key === 'operator' && target.dataset.cond !== undefined) {
      const ruleIndex = Number(target.dataset.rule);
      const cond = sieveState?.rules?.[ruleIndex]?.conditions?.[Number(target.dataset.cond)];
      if (cond) cond.operator = target.value;
    }
  });
  ctx.root.querySelector('#sieve-body')?.addEventListener('click', (event) => {
    const add = event.target.closest('[data-sieve-add]');
    if (add) { sieveState.rules.push(newRule()); renderSieveEditor(); return; }
    const remove = event.target.closest('[data-sieve-remove]');
    if (remove) { sieveState.rules.splice(Number(remove.dataset.sieveRemove), 1); renderSieveEditor(); return; }
    const addCond = event.target.closest('[data-sieve-add-cond]');
    if (addCond) { sieveState.rules[Number(addCond.dataset.sieveAddCond)].conditions.push(newCondition()); renderSieveEditor(); return; }
    const removeCond = event.target.closest('[data-sieve-remove-cond]');
    if (removeCond) {
      const rule = sieveState.rules[Number(removeCond.dataset.sieveRemoveCond)];
      if (rule.conditions.length > 1) rule.conditions.splice(Number(removeCond.dataset.cond), 1);
      renderSieveEditor();
    }
  });

  const validateSieve = () => {
    for (const [index, rule] of (sieveState?.rules || []).entries()) {
      if (!rule.name.trim()) return `Rule ${index + 1}: name is required.`;
      if (!rule.conditions.length) return `Rule ${index + 1}: add at least one condition.`;
      for (const condition of rule.conditions) {
        if (!String(condition.value).trim()) return `Rule ${index + 1}: a condition value is empty.`;
        if (condition.field === 'size' && !/^\d+$/.test(String(condition.value).trim())) return `Rule ${index + 1}: size must be whole kilobytes.`;
      }
      if (['move', 'redirect'].includes(rule.action.type) && !String(rule.action.value).trim()) return `Rule ${index + 1}: choose a folder or redirect address.`;
    }
    return '';
  };

  document.getElementById('sieve-preview')?.addEventListener('click', async () => {
    if (!sieveState) return;
    const error = validateSieve();
    if (error) { ui.toast(error, 'critical'); return; }
    try {
      const payload = await ctx.api(`/control/api/mailboxes/${mailboxId}/sieve/preview`, { method: 'POST', body: { rules: sieveState.rules } });
      ctx.modal({ title: 'Sieve preview', subtitle: `${payload.ruleCount} rule${payload.ruleCount === 1 ? '' : 's'} would be deployed as the managed script.`, size: 'large', body: ui.codeBlock(payload.script, { language: 'sieve', maxHeight: '56vh' }) });
    } catch (error) { ui.toast(errorText(error), 'critical', { title: 'Sieve preview failed' }); }
  });
  document.getElementById('sieve-save')?.addEventListener('click', async () => {
    if (!sieveState) return;
    const error = validateSieve();
    if (error) { ui.toast(error, 'critical'); return; }
    const ok = await ctx.confirm({ title: 'Deploy managed Sieve rules', message: `Deploy ${sieveState.rules.length} rule${sieveState.rules.length === 1 ? '' : 's'} to ${mailbox.email}? Only the alt8mail-control script is replaced.`, confirmLabel: 'Deploy rules', tone: 'primary' });
    if (!ok) return;
    try {
      await ctx.api(`/control/api/mailboxes/${mailboxId}/sieve`, { method: 'PUT', body: { rules: sieveState.rules } });
      ui.toast('Managed Sieve rules deployed.', 'good');
      sieveLoaded = false;
      await loadSieve();
    } catch (error) { ui.toast(errorText(error), 'critical', { title: 'Sieve deployment failed' }); }
  });

  document.getElementById('delete-mailbox-action')?.addEventListener('click', async () => {
    const ok = await ctx.confirm({ title: 'Delete mailbox', message: `Delete ${mailbox.email}? The Maildir is archived and the account is removed. This cannot be undone from the panel.`, confirmLabel: 'Delete mailbox', tone: 'danger', requireText: mailbox.email });
    if (!ok) return;
    try {
      const result = await ctx.api(`/control/api/mailboxes/${mailboxId}`, { method: 'DELETE', body: { confirmEmail: mailbox.email } });
      ui.toast(result.archive ? `Mailbox deleted. Archive: ${result.archive}` : 'Mailbox deleted.', 'good');
      ctx.navigate('mailboxes');
    } catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not delete mailbox' }); }
  });
  ctx.onCleanup(() => { activeTab = ''; });
}

/* =============================================================== aliases */

export async function renderAliases(ctx) {
  const canManage = ctx.can('mail.manage');
  let query = '';
  let filterDomain = '';
  let lastPayload = null;
  ctx.setTitle('Aliases & forwarding', 'Mail');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Mail',
    title: 'Aliases & forwarding',
    description: 'Forwarders, catch-all mailboxes and comma-separated distribution lists, merged into the Postfix alias lookup.',
    actions: `${canManage ? `<button class="btn btn-primary" type="button" id="alias-create">${ui.icon('plus', 15)} New alias</button>` : ''}<button class="btn btn-secondary" type="button" id="aliases-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    <div class="card">
      <header class="card-head"><div><h3>Aliases</h3><p id="aliases-summary">Loading aliases…</p></div><div class="card-actions"><div class="filterbar" style="margin-bottom:0"><div class="search-box">${ui.icon('search', 14)}<input class="input" id="alias-search" placeholder="Search address or target" aria-label="Search aliases"></div><select class="select" id="alias-domain-filter" aria-label="Filter by domain"><option value="">All domains</option></select></div></div></header>
      <div class="card-body" id="aliases-body">${ui.loadingState('Loading aliases…', 4)}</div>
    </div>
    <div class="notice tone-info mt-4">${ui.icon('info', 16)}<div><strong>Alias lookup is shared with Postfix</strong><p>Changes are rendered into <span class="mono">postfix_alias_lookup()</span>, which merges webmail identities, groups and panel-managed forwarders. Previews show the resulting diff before anything is written.</p></div></div>`;

  const domainOptions = async () => {
    try {
      const payload = await ctx.api('/control/api/domains');
      return Array.isArray(payload.domains) ? payload.domains : [];
    } catch { return []; }
  };

  const load = async () => {
    const body = document.getElementById('aliases-body');
    if (!body) return;
    try {
      const payload = await ctx.api(`/control/api/aliases?q=${encodeURIComponent(query)}&domain=${encodeURIComponent(filterDomain)}`);
      if (!ctx.isCurrent()) return;
      lastPayload = payload;
      const aliases = Array.isArray(payload.aliases) ? payload.aliases : [];
      const counts = { forward: 0, catch_all: 0, distribution: 0 };
      aliases.forEach((alias) => { counts[alias.kind] = (counts[alias.kind] || 0) + 1; });
      document.getElementById('aliases-summary').textContent = `${aliases.length} alias${aliases.length === 1 ? '' : 'es'} · ${counts.forward} forwarders · ${counts.catch_all} catch-alls · ${counts.distribution} lists`;
      body.innerHTML = aliases.length ? `<div class="table-wrap is-bare"><table class="data"><thead><tr><th>Address</th><th>Type</th><th>Targets</th><th>Description</th><th>State</th><th></th></tr></thead><tbody>
        ${aliases.map((alias) => `<tr>
          <td><span class="cell-main mono">${esc(alias.address)}</span><span class="cell-sub">${esc(alias.domain)}</span></td>
          <td>${ui.badge(alias.kind === 'catch_all' ? 'Catch-all' : alias.kind === 'distribution' ? 'Distribution' : 'Forward', alias.kind === 'catch_all' ? 'warning' : alias.kind === 'distribution' ? 'accent' : 'info', alias.kind === 'catch_all' ? 'globe' : alias.kind === 'distribution' ? 'users' : 'forward')}</td>
          <td><div class="chip-list">${alias.targets.map((target) => `<span class="mono-chip">${esc(target)}</span>`).join('')}</div></td>
          <td>${esc(alias.description || '—')}</td>
          <td>${ui.statusPill(alias.active ? 'active' : 'disabled', alias.active ? 'Active' : 'Disabled')}</td>
          <td class="actions"><button class="icon-btn" type="button" data-alias-preview="${esc(alias.id)}" title="Preview diff" aria-label="Preview alias diff">${ui.icon('eye', 15)}</button>${canManage ? `<button class="icon-btn" type="button" data-alias-edit="${esc(alias.id)}" title="Edit alias" aria-label="Edit alias">${ui.icon('edit', 15)}</button><button class="icon-btn is-danger" type="button" data-alias-delete="${esc(alias.id)}" title="Delete alias" aria-label="Delete alias">${ui.icon('trash', 15)}</button>` : ''}</td>
        </tr>`).join('')}</tbody></table></div>` : ui.emptyState('No aliases found', query || filterDomain ? 'No alias matches these filters.' : 'Create a forwarder, catch-all or distribution list.', canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="alias-create-empty">${ui.icon('plus', 13)} New alias</button>` : '', 'forward');
      document.getElementById('alias-create-empty')?.addEventListener('click', () => openAliasEditor(null));
      body.querySelectorAll('[data-alias-preview]').forEach((button) => button.addEventListener('click', () => {
        const alias = aliases.find((item) => item.id === button.dataset.aliasPreview);
        if (alias) previewAlias('delete', alias, { address: alias.address, kind: alias.kind, targets: alias.targets, description: alias.description, active: alias.active }, true);
      }));
      body.querySelectorAll('[data-alias-edit]').forEach((button) => button.addEventListener('click', () => {
        const alias = aliases.find((item) => item.id === button.dataset.aliasEdit);
        if (alias) openAliasEditor(alias);
      }));
      body.querySelectorAll('[data-alias-delete]').forEach((button) => button.addEventListener('click', () => {
        const alias = aliases.find((item) => item.id === button.dataset.aliasDelete);
        if (alias) deleteAlias(alias);
      }));
    } catch (error) {
      body.innerHTML = ui.inlineNotice('critical', 'Could not load aliases', errorText(error));
    }
  };

  const previewAlias = async (action, alias, input, thenApply = false) => {
    try {
      const payload = await ctx.api('/control/api/aliases/preview', { method: 'POST', body: { action, id: alias?.id || '', input } });
      const modal = ctx.modal({
        title: action === 'delete' ? 'Delete alias preview' : action === 'update' ? 'Update alias preview' : 'Create alias preview',
        subtitle: 'Diff of the managed alias map. No change has been written yet.',
        size: 'large',
        body: `<div class="notice tone-info">${ui.icon('info', 16)}<div><strong>${payload.diff.length} changed line${payload.diff.length === 1 ? '' : 's'}</strong><p>The Postfix lookup merges all alias sources; this shows only the panel-managed map.</p></div></div>
          <div class="mt-3">${ui.diffBlock(payload.diff, 'No visible change')}</div>
          <details class="mt-3"><summary class="muted" style="cursor:pointer;font-size:11px">Show resulting map and lookup query</summary><div class="mt-2">${ui.codeBlock(payload.after.join('\n'), { language: 'aliases' })}${ui.codeBlock(payload.postfixQuery, { language: 'SQL' })}</div></details>`,
        footer: `<button type="button" class="btn btn-ghost" data-modal-close>Close</button>${thenApply && canManage ? `<button type="button" class="btn btn-danger" id="alias-apply-delete">Delete alias</button>` : ''}`,
      });
      if (thenApply && canManage) {
        modal.root.querySelector('#alias-apply-delete')?.addEventListener('click', async () => {
          try {
            await ctx.api(`/control/api/aliases/${alias.id}`, { method: 'DELETE' });
            modal.close();
            ui.toast(`Alias ${alias.address} deleted.`, 'good');
            await load();
          } catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not delete alias' }); }
        });
      }
    } catch (error) { ui.toast(errorText(error), 'critical', { title: 'Alias preview failed' }); }
  };

  const openAliasEditor = async (alias) => {
    const domains = await domainOptions();
    if (!domains.length) { ui.toast('Create a managed domain before adding aliases.', 'warning'); return; }
    const editing = !!alias;
    const formId = id('alias-form');
    const modal = ctx.modal({
      title: editing ? 'Edit alias' : 'New alias',
      subtitle: 'Preview the Postfix diff before writing it.',
      size: 'medium',
      body: `<form id="${formId}">
        <div class="form-grid">
          ${ui.field({ label: 'Type', name: 'kind', type: 'select', value: alias?.kind || 'forward', options: [{ value: 'forward', label: 'Forward to address(es)' }, { value: 'catch_all', label: 'Catch-all for domain' }, { value: 'distribution', label: 'Distribution list' }], help: 'Distribution lists deliver to every target; forwarders use the first matching alias expansion.' })}
          <div id="alias-address-wrap">${ui.field({ label: 'Alias address', name: 'address', value: alias?.kind === 'catch_all' ? '' : alias?.address || '', placeholder: 'sales@example.com', required: true })}</div>
          <div class="span-2" id="alias-targets-wrap">${ui.field({ label: 'Targets', name: 'targets', type: 'textarea', value: (alias?.targets || []).join(', '), rows: 3, required: true, help: 'Comma-separated addresses. Up to 100. A distribution list delivers to all targets.' })}</div>
          <div class="span-2">${ui.field({ label: 'Description', name: 'description', value: alias?.description || '', help: 'Visible to operators only.' })}</div>
          <div class="span-2">${ui.switchField({ label: 'Alias is active', name: 'active', checked: alias ? !!alias.active : true, help: 'Inactive aliases stay in the database but are removed from the Postfix map.' })}</div>
        </div>
        <div class="form-actions"><span class="form-note">Address collisions with mailboxes and webmail identities are rejected.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button type="button" class="btn btn-secondary" id="alias-preview-btn">${ui.icon('eye', 14)} Preview diff</button><button type="submit" class="btn btn-primary">Save alias</button></div></div>
      </form>`,
    });
    const form = modal.root.querySelector(`#${formId}`);
    const kind = form.querySelector('[name="kind"]');
    const addressWrap = form.querySelector('#alias-address-wrap');
    const targetsWrap = form.querySelector('#alias-targets-wrap');
    const rebuildAddress = () => {
      if (kind.value === 'catch_all') {
        addressWrap.innerHTML = ui.field({ label: 'Catch-all domain', name: 'domain', type: 'select', value: alias?.domain || domains[0].domain, required: true, options: domains.map((d) => ({ value: d.domain, label: d.domain })), help: 'All unmatched local recipients at this domain will forward to the targets.' });
      } else {
        addressWrap.innerHTML = ui.field({ label: 'Alias address', name: 'address', value: alias?.kind === 'catch_all' ? '' : alias?.address || '', placeholder: kind.value === 'distribution' ? 'team@example.com' : 'sales@example.com', required: true });
      }
    };
    kind.addEventListener('change', rebuildAddress);
    const collectInput = () => {
      const values = ui.formValues(form);
      const address = kind.value === 'catch_all' ? `@${values.domain}` : values.address;
      return {
        address,
        kind: kind.value,
        target: values.targets || '',
        description: values.description || '',
        active: values.active !== false,
      };
    };
    document.getElementById('alias-preview-btn')?.addEventListener('click', async () => {
      const input = collectInput();
      if (!input.address || !input.target) { ui.toast('Address and at least one target are required.', 'critical'); return; }
      try {
        const payload = await ctx.api('/control/api/aliases/preview', { method: 'POST', body: { action: editing ? 'update' : 'create', id: alias?.id || '', input } });
        const preview = ctx.modal({
          title: 'Postfix alias diff', subtitle: 'Review before applying.', size: 'large',
          body: `<div class="notice tone-info">${ui.icon('info', 16)}<div><strong>${payload.diff.length} changed line${payload.diff.length === 1 ? '' : 's'}</strong><p>Only the control-managed alias map is shown.</p></div></div><div class="mt-3">${ui.diffBlock(payload.diff, 'No visible change')}</div>`,
          footer: `<button class="btn btn-ghost" type="button" data-modal-close>Close preview</button><button class="btn btn-primary" type="button" id="alias-apply-preview">Apply changes</button>`,
        });
        preview.root.querySelector('#alias-apply-preview')?.addEventListener('click', async () => {
          try {
            await saveAlias(input);
            preview.close();
            modal.close();
          } catch { /* surfaced by saveAlias */ }
        });
      } catch (error) { ui.toast(errorText(error), 'critical', { title: 'Preview failed' }); }
    });
    const saveAlias = async (input) => {
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        await ctx.api(editing ? `/control/api/aliases/${alias.id}` : '/control/api/aliases', { method: editing ? 'PATCH' : 'POST', body: input });
        ui.toast(editing ? 'Alias updated.' : 'Alias created.', 'good');
        await load();
        modal.close();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: editing ? 'Could not update alias' : 'Could not create alias' });
        throw error;
      } finally {
        button.classList.remove('is-loading'); button.disabled = false;
      }
    };
    form.addEventListener('submit', async (event) => {
      event.preventDefault();
      const input = collectInput();
      if (!input.address || !input.target) { ui.toast('Address and at least one target are required.', 'critical'); return; }
      try { await saveAlias(input); } catch { /* handled */ }
    });
  };

  const deleteAlias = async (alias) => {
    try {
      const payload = await ctx.api('/control/api/aliases/preview', { method: 'POST', body: { action: 'delete', id: alias.id, input: { address: alias.address, kind: alias.kind, target: alias.targets.join(','), description: alias.description, active: alias.active } } });
      const modal = ctx.modal({
        title: 'Delete alias', subtitle: alias.address, size: 'medium',
        body: `<div class="notice tone-warning">${ui.icon('alert', 16)}<div><strong>This removes the forwarding path</strong><p>Mail sent to this address will no longer be redirected by this alias.</p></div></div><div class="mt-3">${ui.diffBlock(payload.diff)}</div>`,
        footer: `<button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button type="button" class="btn btn-danger" id="alias-confirm-delete">Delete alias</button>`,
      });
      modal.root.querySelector('#alias-confirm-delete')?.addEventListener('click', async (event) => {
        const button = event.currentTarget;
        button.classList.add('is-loading'); button.disabled = true;
        try {
          await ctx.api(`/control/api/aliases/${alias.id}`, { method: 'DELETE' });
          modal.close();
          ui.toast(`Alias ${alias.address} deleted.`, 'good');
          await load();
        } catch (error) {
          ui.toast(errorText(error), 'critical', { title: 'Delete failed' });
          button.classList.remove('is-loading'); button.disabled = false;
        }
      });
    } catch (error) { ui.toast(errorText(error), 'critical', { title: 'Preview failed' }); }
  };

  document.getElementById('alias-create')?.addEventListener('click', () => openAliasEditor(null));
  document.getElementById('aliases-refresh')?.addEventListener('click', load);
  const search = document.getElementById('alias-search');
  let timer = null;
  search?.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(() => { query = search.value.trim(); load(); }, 220); });
  ctx.onCleanup(() => clearTimeout(timer));
  const domainFilter = document.getElementById('alias-domain-filter');
  const domains = await domainOptions();
  if (domainFilter) {
    domains.forEach((domain) => {
      const option = document.createElement('option');
      option.value = domain.domain;
      option.textContent = domain.domain;
      domainFilter.appendChild(option);
    });
    domainFilter.addEventListener('change', () => { filterDomain = domainFilter.value; load(); });
  }
  await load();
}

/* =============================================================== imports */

export async function renderImports(ctx) {
  const canManage = ctx.can('imports.manage');
  let jobs = [];
  let mailboxes = [];
  let timer = null;
  ctx.setTitle('Mail import', 'Mail');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Mail',
    title: 'Mail import',
    description: 'Migrate IMAP accounts, mbox archives or Maildirs. One background job runs at a time with folder, flag and read-state preservation.',
    actions: `${canManage ? `<button class="btn btn-primary" type="button" id="import-create">${ui.icon('plus', 15)} New import</button>` : ''}<button class="btn btn-secondary" type="button" id="imports-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
  })}
    <div class="grid grid-main">
      <section class="card"><header class="card-head"><div><h3>Import jobs</h3><p id="imports-summary">Loading jobs…</p></div></header><div class="card-body" id="imports-body">${ui.loadingState('Loading imports…', 4)}</div></section>
      <div class="stack">
        <section class="card"><header class="card-head"><div><h3>Import fidelity</h3><p>What is preserved</p></div></header><div class="card-body">
          <div class="tile-link"><span>${ui.icon('inbox', 15)} Folder hierarchy</span><span class="value">preserved</span></div>
          <div class="tile-link"><span>${ui.icon('check-circle', 15)} Read / unread</span><span class="value">preserved</span></div>
          <div class="tile-link"><span>${ui.icon('calendar', 15)} Sent dates</span><span class="value">preserved</span></div>
          <div class="tile-link"><span>${ui.icon('star', 15)} Flags and keywords</span><span class="value">preserved</span></div>
          <div class="tile-link"><span>${ui.icon('archive', 15)} Duplicate safety</span><span class="value">message copy</span></div>
          <div class="note-strip" style="margin-top:11px">${ui.icon('info', 13)} <span>Only one import job executes at a time. Jobs can be stopped, retried and removed without deleting the target mailbox.</span></div>
        </div></section>
        <section class="card"><header class="card-head"><div><h3>Source types</h3></div></header><div class="card-body">
          <div class="mini-list">
            <div class="mini-row"><span class="list-icon">${ui.icon('cloud', 14)}</span><div class="grow"><strong>IMAP</strong><span>Remote server download with SSL, STARTTLS or plain text</span></div></div>
            <div class="mini-row"><span class="list-icon">${ui.icon('file-plus', 14)}</span><div class="grow"><strong>mbox</strong><span>One archive file or a directory of mbox files</span></div></div>
            <div class="mini-row"><span class="list-icon">${ui.icon('archive', 14)}</span><div class="grow"><strong>Maildir</strong><span>Existing Maildir tree, including nested folders</span></div></div>
          </div>
        </div></section>
      </div>
    </div>`;

  const load = async () => {
    const body = document.getElementById('imports-body');
    if (!body) return;
    try {
      const payload = await ctx.api('/control/api/imports');
      if (!ctx.isCurrent()) return;
      jobs = Array.isArray(payload.jobs) ? payload.jobs : [];
      const active = jobs.filter((job) => job.status === 'running').length;
      const queued = jobs.filter((job) => job.status === 'queued').length;
      document.getElementById('imports-summary').textContent = `${jobs.length} job${jobs.length === 1 ? '' : 's'} · ${active} running · ${queued} queued`;
      body.innerHTML = jobs.length ? `<div class="table-wrap is-bare"><table class="data"><thead><tr><th>Target</th><th>Source</th><th>Status</th><th style="min-width:160px">Progress</th><th class="num">Imported</th><th>Created</th><th></th></tr></thead><tbody>
        ${jobs.map((job) => {
          const tone = job.status === 'completed' ? 'good' : job.status === 'failed' ? 'critical' : job.status === 'running' ? 'info' : job.status === 'stopped' ? 'warning' : 'neutral';
          return `<tr><td><span class="cell-main">${esc(job.targetMailbox)}</span><span class="cell-sub">${esc(job.kind)}</span></td>
            <td><span class="cell-sub" style="margin-top:0">${esc(sourceSummary(job))}</span></td>
            <td>${ui.statusPill(job.status)}</td>
            <td><div class="progress-row"><div class="progress tone-${tone}"><span style="width:${Math.max(0, Math.min(100, Number(job.progress) || 0))}%"></span></div><span class="progress-label">${ui.num(job.progress)}%</span></div></td>
            <td class="num">${ui.num(job.messagesImported)}</td>
            <td>${esc(fmt.timeAgo(job.createdAt))}</td>
            <td class="actions"><button class="icon-btn" type="button" data-import-detail="${esc(job.id)}" title="View log" aria-label="View import log">${ui.icon('eye', 15)}</button>${canManage && job.status === 'running' ? `<button class="icon-btn is-danger" type="button" data-import-stop="${esc(job.id)}" title="Stop import" aria-label="Stop import">${ui.icon('pause', 15)}</button>` : ''}${canManage && ['failed', 'stopped'].includes(job.status) ? `<button class="icon-btn" type="button" data-import-retry="${esc(job.id)}" title="Retry import" aria-label="Retry import">${ui.icon('refresh', 15)}</button>` : ''}${canManage && !['running'].includes(job.status) ? `<button class="icon-btn is-danger" type="button" data-import-remove="${esc(job.id)}" title="Remove job" aria-label="Remove job">${ui.icon('trash', 15)}</button>` : ''}</td></tr>`;
        }).join('')}</tbody></table></div>` : ui.emptyState('No import jobs', 'Create an import to bring existing mail into a managed mailbox.', canManage ? `<button class="btn btn-secondary btn-sm" type="button" id="import-create-empty">${ui.icon('plus', 13)} New import</button>` : '', 'upload');
      document.getElementById('import-create-empty')?.addEventListener('click', openCreateImport);
      body.querySelectorAll('[data-import-detail]').forEach((button) => button.addEventListener('click', () => showImportDetail(button.dataset.importDetail)));
      body.querySelectorAll('[data-import-stop]').forEach((button) => button.addEventListener('click', async () => {
        try { await ctx.api(`/control/api/imports/${button.dataset.importStop}/stop`, { method: 'POST' }); ui.toast('Stop requested.', 'warning'); await load(); }
        catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not stop import' }); }
      }));
      body.querySelectorAll('[data-import-retry]').forEach((button) => button.addEventListener('click', async () => {
        try { await ctx.api(`/control/api/imports/${button.dataset.importRetry}/retry`, { method: 'POST' }); ui.toast('Import re-queued.', 'good'); await load(); }
        catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not retry import' }); }
      }));
      body.querySelectorAll('[data-import-remove]').forEach((button) => button.addEventListener('click', async () => {
        const ok = await ctx.confirm({ title: 'Remove import job', message: 'Remove this job and its log? The target mailbox and already imported messages are not deleted.', confirmLabel: 'Remove job', tone: 'danger' });
        if (!ok) return;
        try { await ctx.api(`/control/api/imports/${button.dataset.importRemove}`, { method: 'DELETE' }); ui.toast('Import job removed.', 'good'); await load(); }
        catch (error) { ui.toast(errorText(error), 'critical', { title: 'Could not remove import' }); }
      }));
      if (jobs.some((job) => ['running', 'queued'].includes(job.status)) && !timer) {
        timer = setInterval(() => { if (ctx.isCurrent()) load(); }, 4000);
        ctx.onCleanup(() => { if (timer) { clearInterval(timer); timer = null; } });
      }
      if (!jobs.some((job) => ['running', 'queued'].includes(job.status)) && timer) {
        clearInterval(timer); timer = null;
      }
    } catch (error) {
      body.innerHTML = ui.inlineNotice('critical', 'Could not load import jobs', errorText(error));
    }
  };

  const loadMailboxes = async () => {
    if (mailboxes.length) return mailboxes;
    try {
      const payload = await ctx.api('/control/api/mailboxes?limit=1000');
      mailboxes = (payload.mailboxes || []).filter((box) => box.active);
    } catch { mailboxes = []; }
    return mailboxes;
  };

  const openCreateImport = async () => {
    const boxes = await loadMailboxes();
    if (!boxes.length) { ui.toast('Create an active mailbox before importing mail.', 'warning'); return; }
    const formId = id('import-form');
    const modal = ctx.modal({
      title: 'New mail import', subtitle: 'The job downloads or reads the source, then uses doveadm import to preserve Maildir metadata.',
      size: 'medium',
      body: `<form id="${formId}">
        <div class="filterbar" id="import-kind-tabs"><button type="button" class="btn btn-secondary is-active" data-kind="imap">${ui.icon('cloud', 14)} IMAP</button><button type="button" class="btn btn-ghost" data-kind="mbox">${ui.icon('file-plus', 14)} mbox</button><button type="button" class="btn btn-ghost" data-kind="maildir">${ui.icon('archive', 14)} Maildir</button></div>
        <div class="form-grid">
          <div class="span-2">${ui.field({ label: 'Target mailbox', name: 'targetMailbox', type: 'select', required: true, options: boxes.map((box) => ({ value: box.email, label: box.email })) })}</div>
          <div class="span-2" id="import-source-fields"></div>
        </div>
        <div class="notice tone-warning mt-3">${ui.icon('info', 16)}<div><strong>One job at a time</strong><p>Imports are queued and executed by the background worker. Large mailboxes can take a long time; progress and the log remain visible.</p></div></div>
        <div class="form-actions"><span class="form-note">Credentials are sealed with AES-GCM before being stored.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button type="submit" class="btn btn-primary">Queue import</button></div></div>
      </form>`,
    });
    const form = modal.root.querySelector(`#${formId}`);
    let kind = 'imap';
    const sourceHost = form.querySelector('#import-source-fields');
    const paintSource = () => {
      sourceHost.innerHTML = kind === 'imap' ? `<div class="form-grid" style="margin:0"><div class="span-2">${ui.field({ label: 'IMAP host', name: 'host', placeholder: 'imap.example.com', required: true })}</div><div>${ui.field({ label: 'Port', name: 'port', type: 'number', value: '993', min: '1', max: '65535', required: true })}</div><div>${ui.field({ label: 'Security', name: 'security', type: 'select', value: 'ssl', options: [{ value: 'ssl', label: 'SSL / TLS' }, { value: 'starttls', label: 'STARTTLS' }, { value: 'plain', label: 'Plain text' }] })}</div><div class="span-2">${ui.field({ label: 'Username', name: 'username', required: true, autocomplete: 'username' })}</div><div class="span-2">${ui.field({ label: 'Password', name: 'password', type: 'password', required: true, autocomplete: 'current-password' })}</div><div class="span-2">${ui.switchField({ label: 'Accept an invalid upstream certificate', name: 'insecure', help: 'Only for a known test system. Prefer fixing the upstream certificate.' })}</div></div>` : `<div class="form-grid" style="margin:0"><div class="span-2">${ui.field({ label: kind === 'mbox' ? 'mbox file or directory' : 'Maildir path', name: 'path', placeholder: kind === 'mbox' ? '/srv/import/archive.mbox' : '/srv/import/Maildir', required: true, help: kind === 'mbox' ? 'A directory is scanned for mbox files.' : 'Path must contain cur/ and new/ directories.' })}</div></div>`;
      form.querySelectorAll('[data-kind]').forEach((button) => {
        button.classList.toggle('btn-secondary', button.dataset.kind === kind);
        button.classList.toggle('is-active', button.dataset.kind === kind);
        button.classList.toggle('btn-ghost', button.dataset.kind !== kind);
      });
    };
    form.querySelectorAll('[data-kind]').forEach((button) => button.addEventListener('click', () => { kind = button.dataset.kind; paintSource(); }));
    paintSource();
    form.addEventListener('submit', async (event) => {
      event.preventDefault();
      const values = ui.formValues(form);
      const body = { kind, targetMailbox: values.targetMailbox };
      if (kind === 'imap') Object.assign(body, { host: values.host, port: Number(values.port || 993), username: values.username, password: values.password, security: values.security || 'ssl', insecure: !!values.insecure });
      else body.path = values.path;
      const button = form.querySelector('button[type="submit"]');
      button.classList.add('is-loading'); button.disabled = true;
      try {
        await ctx.api('/control/api/imports', { method: 'POST', body });
        modal.close();
        ui.toast('Import queued. Progress will appear here.', 'good');
        await load();
      } catch (error) {
        ui.toast(errorText(error), 'critical', { title: 'Could not queue import' });
        button.classList.remove('is-loading'); button.disabled = false;
      }
    });
  };

  const showImportDetail = async (jobId) => {
    const modal = ctx.modal({ title: 'Import job', size: 'large', body: ui.loadingState('Loading job…', 3), footer: `<button class="btn btn-secondary" type="button" data-modal-close>Close</button>` });
    try {
      const payload = await ctx.api(`/control/api/imports/${jobId}`);
      if (!modal.root.isConnected) return;
      const job = payload.job;
      const body = modal.root.querySelector('.modal-body');
      body.innerHTML = `<div class="diagnostic-summary">${ui.statusPill(job.status)}<span class="summary-pill">${ui.icon('upload', 12)} ${ui.num(job.messagesImported)} imported</span><span class="summary-pill">${ui.icon('activity', 12)} ${ui.num(job.progress)}%</span><span class="summary-pill">${ui.icon('clock', 12)} ${esc(fmt.timeAgo(job.createdAt))}</span></div>
        <div class="progress" style="margin-bottom:12px"><span style="width:${Math.max(0, Math.min(100, Number(job.progress) || 0))}%"></span></div>
        ${job.error ? `<div class="notice tone-critical">${ui.icon('alert-circle', 16)}<div><strong>Import error</strong><p class="wrap-anywhere">${esc(job.error)}</p></div></div>` : ''}
        <div class="kv-grid is-3" style="margin-top:12px">${ui.kv([
          ['Target', esc(job.targetMailbox)], ['Kind', esc(job.kind)], ['Progress', `${ui.num(job.progress)}%`],
          ['Imported', ui.num(job.messagesImported)], ['Started', esc(fmt.dateTime(job.startedAt))], ['Finished', esc(fmt.dateTime(job.finishedAt))],
        ])}</div>
        <h4 class="form-section-title" style="margin-top:16px">Job log</h4>${ui.codeBlock(job.log || 'No log lines yet.', { language: 'import log', maxHeight: '38vh' })}`;
      if (canManage) {
        const footer = modal.root.querySelector('.modal-footer');
        if (job.status === 'running' || job.status === 'queued') {
          const stop = document.createElement('button');
          stop.className = 'btn btn-soft-danger';
          stop.innerHTML = `${ui.icon('pause', 14)} Stop job`;
          stop.addEventListener('click', async () => { try { await ctx.api(`/control/api/imports/${job.id}/stop`, { method: 'POST' }); modal.close(); ui.toast('Stop requested.', 'warning'); await load(); } catch (error) { ui.toast(errorText(error), 'critical'); } });
          footer.prepend(stop);
        }
        if (['failed', 'stopped'].includes(job.status)) {
          const retry = document.createElement('button');
          retry.className = 'btn btn-secondary';
          retry.innerHTML = `${ui.icon('refresh', 14)} Retry`;
          retry.addEventListener('click', async () => { try { await ctx.api(`/control/api/imports/${job.id}/retry`, { method: 'POST' }); modal.close(); ui.toast('Import re-queued.', 'good'); await load(); } catch (error) { ui.toast(errorText(error), 'critical'); } });
          footer.prepend(retry);
        }
      }
    } catch (error) {
      modal.root.querySelector('.modal-body').innerHTML = ui.inlineNotice('critical', 'Could not load import', errorText(error));
    }
  };

  document.getElementById('import-create')?.addEventListener('click', openCreateImport);
  document.getElementById('imports-refresh')?.addEventListener('click', load);
  await load();
}

function sourceSummary(job) {
  const source = job.source || {};
  if (job.kind === 'imap') return `${source.host || 'IMAP'}:${source.port || ''} · ${source.username || ''} · ${source.security || ''}`;
  return source.path || 'local path';
}
