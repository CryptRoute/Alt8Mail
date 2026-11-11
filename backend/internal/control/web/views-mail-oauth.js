/* Mail OAuth: server-wide Gmail and Microsoft sign-in applications. */
import * as ui from './ui.js?v=3.1.0';

const esc = ui.esc;

const PROVIDER_META = {
  gmail: {
    icon: 'mail',
    markClass: 'is-google',
    sourceLabel: 'Google Cloud Console',
    steps: [
      'Open <strong>Google Cloud Console → APIs &amp; Services → Credentials</strong> and choose or create the project that will own Alt8Mail sign-in.',
      'Configure the <strong>OAuth consent screen</strong>. Add the scopes <code>openid</code>, <code>email</code>, <code>profile</code> and <code>https://mail.google.com/</code>. While the app is in Testing, add each mailbox as a test user; publish the app when everyone should connect.',
      'Choose <strong>Create credentials → OAuth client ID</strong>, application type <strong>Web application</strong>.',
      'Under <strong>Authorized redirect URIs</strong>, paste the redirect URI from this page exactly.',
      'Copy the client ID and client secret into this page, save the draft, then apply it.',
    ],
    notes: [
      'Google may require OAuth verification before external users can grant the full <code>https://mail.google.com/</code> scope.',
      'Workspace administrators can restrict third-party app access; users may need approval in the Google Admin console.',
    ],
  },
  microsoft: {
    icon: 'mail-open',
    markClass: 'is-microsoft',
    sourceLabel: 'Microsoft Entra admin center',
    steps: [
      'Open <strong>Microsoft Entra admin center → App registrations → New registration</strong>.',
      'Choose the supported account types that match your users. This decides the tenant value: <code>common</code> for work and personal accounts, <code>organizations</code> for work only, <code>consumers</code> for personal only, or your directory ID / verified domain to restrict sign-in to one tenant.',
      'Under <strong>Authentication → Add a platform → Web</strong>, paste the redirect URI from this page exactly. Do not enable public client flows for this web application.',
      'Under <strong>Certificates &amp; secrets</strong>, create a client secret and copy its <strong>Value</strong> immediately. The Secret ID is not the secret.',
      'Under <strong>API permissions</strong>, add delegated <strong>Office 365 Exchange Online</strong> permissions <code>IMAP.AccessAsUser.All</code> and <code>SMTP.Send</code>, then grant admin consent for your tenant.',
      'Copy the application (client) ID, the secret value and the tenant into this page, save the draft, then apply it.',
    ],
    notes: [
      'Some Microsoft 365 tenants disable SMTP AUTH per mailbox; sending fails until the mailbox or tenant policy allows it.',
      'Application secrets expire. Add the replacement here before the old secret expires so refreshes and new connections keep working.',
    ],
  },
};

function secretSourceLabel(source) {
  if (source === 'stored') return 'stored securely in System Control';
  if (source === 'environment') return 'read from app.env';
  if (source === 'shared') return 'inherited from the shared OIDC application';
  return '';
}

function providerStatus(provider) {
  if (!provider.enabled) return { label: 'Disabled', tone: 'neutral', icon: 'circle-dot' };
  if (!provider.clientId || !provider.hasSecret) return { label: 'Incomplete', tone: 'critical', icon: 'alert' };
  return { label: 'Enabled', tone: 'good', icon: 'check-circle' };
}

function appliedSummary(provider) {
  const applied = provider.applied || {};
  if (applied.enabled && applied.clientId && applied.hasSecret) {
    return `Live for users · ${applied.secretSource === 'shared' ? 'shared OIDC credentials' : 'dedicated mail application'}`;
  }
  if (!applied.enabled && !applied.clientId && !applied.hasSecret) return 'No sign-in is offered to users';
  return 'Configuration is present but not live';
}

function providerCard(provider, canManage) {
  const meta = PROVIDER_META[provider.provider] || PROVIDER_META.gmail;
  const status = providerStatus(provider);
  const applied = provider.applied || {};
  const source = secretSourceLabel(provider.secretSource);
  const secretHelp = provider.hasSecret
    ? `A client secret is already ${source || 'configured'}. Leave this field blank to keep it; enter the new secret when you change the client ID.`
    : 'Paste the application client secret. It is encrypted before it is stored and is never shown again.';
  const tenantField = provider.provider === 'microsoft'
    ? `<div class="span-2">${ui.field({
        label: 'Directory (tenant)',
        name: `${provider.provider}Tenant`,
        value: provider.tenant || 'common',
        placeholder: 'common',
        disabled: !canManage,
        help: 'Use common, organizations, consumers, your directory (tenant) ID, or a verified domain name. This controls which Microsoft accounts can sign in.',
      })}</div>`
    : '';
  const steps = (meta.steps || []).map((step) => `<li>${step}</li>`).join('');
  const notes = (meta.notes || []).map((note) => `<li>${note}</li>`).join('');
  return `<section class="card oauth-provider-card">
    <header class="card-head">
      <div class="oauth-provider-title">
        <span class="oauth-app-mark ${esc(meta.markClass)}">${ui.icon(meta.icon, 18)}</span>
        <div><h3>${esc(provider.label)}</h3><p>${esc(provider.description || '')}</p></div>
      </div>
      <div class="oauth-provider-status">
        ${ui.badge(status.label, status.tone, status.icon)}
        ${provider.pending ? ui.badge('Draft not applied', 'warning', 'alert') : ''}
        ${applied.enabled && applied.hasSecret ? ui.badge('Live', 'good', 'check-circle') : ''}
      </div>
    </header>
    <div class="card-body">
      ${ui.switchField({
        label: `Offer ${provider.provider === 'microsoft' ? 'Microsoft' : 'Google'} sign-in for connected mailboxes`,
        name: `${provider.provider}Enabled`,
        checked: Boolean(provider.enabled),
        disabled: !canManage,
        help: `When this is off, the webmail hides ${provider.provider === 'microsoft' ? 'Outlook and Hotmail' : 'Gmail'} OAuth and refuses to start a sign-in request. Existing tokens keep syncing until they expire.`,
      })}
      ${provider.secretError ? `<div class="mt-3">${ui.inlineNotice('critical', 'Client secret cannot be read', provider.secretError)}</div>` : ''}
      <div class="form-grid mt-3">
        ${ui.field({
          label: 'Client ID',
          name: `${provider.provider}ClientId`,
          value: provider.clientId || '',
          placeholder: provider.provider === 'microsoft' ? 'Application (client) ID' : '1234567890-abc.apps.googleusercontent.com',
          required: true,
          disabled: !canManage,
          autocomplete: 'off',
          help: 'The public application identifier. It is safe to display here.',
        })}
        <div>${ui.field({
          label: 'Client secret',
          name: `${provider.provider}Secret`,
          type: 'password',
          value: '',
          placeholder: provider.hasSecret ? 'Stored — leave blank to keep it' : 'Paste the client secret',
          required: !provider.hasSecret,
          disabled: !canManage,
          autocomplete: 'new-password',
          help: secretHelp,
        })}</div>
        ${tenantField}
      </div>
      <div class="oauth-redirect">
        <div class="section-label">Authorized redirect URI</div>
        ${provider.redirectUri
          ? ui.codeBlock(provider.redirectUri, { language: 'redirect URI' })
          : ui.inlineNotice('warning', 'Webmail URL required', 'Set the webmail hostname under Web Net before enabling sign-in.')}
        <p class="form-note">Register this exact value in ${esc(meta.sourceLabel)}. It is built from the public webmail address and changes whenever that address changes.</p>
      </div>
      <div class="oauth-live">
        <span class="section-label">Currently live</span>
        <div class="tile-link"><span>${ui.icon('activity', 15)} Webmail behaviour</span><span class="value">${esc(appliedSummary(provider))}</span></div>
        <div class="tile-link"><span>${ui.icon('key', 15)} Applied client ID</span><span class="value mono">${esc(applied.clientId || 'Not set')}</span></div>
        ${provider.provider === 'microsoft' ? `<div class="tile-link"><span>${ui.icon('globe', 15)} Applied tenant</span><span class="value mono">${esc(applied.tenant || 'common')}</span></div>` : ''}
      </div>
      <details class="oauth-steps">
        <summary>${ui.icon('list', 14)} How to register this application</summary>
        <ol>${steps}</ol>
        <ul class="oauth-notes">${notes}</ul>
        <div class="oauth-links">
          <a class="btn btn-secondary btn-sm" href="${esc(provider.consoleUrl)}" target="_blank" rel="noopener noreferrer">${ui.icon('external', 13)} Open ${esc(meta.sourceLabel)}</a>
          <a class="btn btn-ghost btn-sm" href="${esc(provider.docsUrl)}" target="_blank" rel="noopener noreferrer">${ui.icon('file-code', 13)} OAuth documentation</a>
        </div>
      </details>
      <div class="oauth-scope-row">
        <span class="section-label">Requested scopes</span>
        <div class="oauth-scope-list">${(provider.scopes || []).map((scope) => `<code>${esc(scope)}</code>`).join('')}</div>
      </div>
    </div>
  </section>`;
}

function checksTable(checks) {
  const list = Array.isArray(checks) ? checks : [];
  if (!list.length) return ui.emptyState('No checks ran', 'Save a configuration and try again.', '', 'activity');
  return `<div class="table-wrap"><table class="data compact"><thead><tr><th>Provider</th><th>Result</th><th>Detail</th></tr></thead><tbody>${list.map((check) => `<tr>
    <td><span class="cell-main">${esc(check.label || check.provider)}</span></td>
    <td>${check.skipped ? ui.badge('Skipped', 'neutral', 'circle-dot') : ui.statusPill(check.ok ? 'active' : 'failed', check.ok ? 'Passed' : 'Needs attention')}</td>
    <td><span class="muted" style="font-size:10.5px;word-break:break-word">${esc(check.message || '')}</span></td>
  </tr>`).join('')}</tbody></table></div>`;
}

export async function renderMailOAuth(ctx) {
  const canManage = ctx.can('server.manage');
  ctx.setTitle('Mail OAuth', 'Mail');
  let data;
  try {
    data = await ctx.api('/control/api/mail-oauth');
  } catch (error) {
    ctx.root.innerHTML = `${ui.pageHeader({ eyebrow: 'Mail', title: 'Mail OAuth', description: 'Gmail and Microsoft sign-in applications for connected mailboxes.' })}${ui.inlineNotice('critical', 'Could not load mail OAuth', ctx.errorDetail(error))}`;
    return;
  }
  const providers = Array.isArray(data.providers) ? data.providers : [];
  const warnings = Array.isArray(data.warnings) ? data.warnings : [];
  const enabledCount = providers.filter((provider) => provider.enabled && provider.clientId && provider.hasSecret).length;
  const publicLabel = data.publicUrl ? data.publicUrl : 'Not configured';
  const actionButtons = canManage
    ? `<button class="btn btn-secondary" type="button" id="mail-oauth-test">${ui.icon('plug', 15)} Test connection</button>
       <button class="btn btn-secondary" type="button" id="mail-oauth-save">${ui.icon('check', 15)} Save draft</button>
       <button class="btn btn-primary" type="button" id="mail-oauth-apply"${data.appEnvReadable ? '' : ' disabled title="app.env is not readable"'}>${ui.icon('rotate', 15)} Apply to webmail</button>`
    : '';
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Mail',
    title: 'Mail OAuth',
    description: 'Register server-wide Google and Microsoft OAuth applications so people can connect Gmail and Outlook accounts to their multi-mailbox inbox. Credentials are written to the webmail environment and applied with a controlled service restart.',
    meta: `${ui.badge(`${enabledCount} of ${providers.length} providers ready`, enabledCount ? 'good' : 'neutral', enabledCount ? 'check-circle' : 'circle-dot')}
      ${data.pending ? ui.badge('Draft changes not applied', 'warning', 'alert') : ui.badge('No unapplied changes', 'good', 'check')}
      ${ui.badge(`Redirect base: ${data.publicUrlSource || 'not set'}`, data.publicUrl ? 'info' : 'warning', 'globe')}`,
    actions: `<button class="btn btn-secondary" type="button" id="mail-oauth-refresh">${ui.icon('refresh', 15)} Refresh</button>${actionButtons}`,
  })}
  ${warnings.length ? `<div class="notice tone-warning">${ui.icon('alert', 17)}<div><strong>Configuration notes</strong><ul class="oauth-warning-list">${warnings.map((warning) => `<li>${esc(warning)}</li>`).join('')}</ul></div></div>` : ''}
  ${!data.appEnvReadable ? ui.inlineNotice('critical', 'app.env is not readable', `System Control could not read ${data.appEnvPath || 'the webmail environment file'}. Applying credentials is disabled until CONTROL_APP_ENV points at the file the webmail services read.`) : ''}
  <div class="grid grid-main">
    <div class="stack">
      <form id="mail-oauth-form">
        ${providers.map((provider) => providerCard(provider, canManage)).join('')}
      </form>
      <section class="card">
        <header class="card-head"><div><h3>Connection test</h3><p>Saves the draft, then checks provider reachability and the redirect address.</p></div><button class="btn btn-secondary btn-sm" type="button" id="mail-oauth-test-inline">${ui.icon('plug', 13)} Run test</button></header>
        <div class="card-body" id="mail-oauth-checks">${ui.emptyState('Not tested yet', 'Run the test after saving the client ID and secret.', '', 'activity')}</div>
      </section>
    </div>
    <div class="stack">
      <section class="card">
        <header class="card-head"><div><h3>Deployment</h3><p>What the webmail services read at startup.</p></div>${ui.badge(data.appEnvReadable ? 'app.env ready' : 'app.env unreadable', data.appEnvReadable ? 'good' : 'critical', data.appEnvReadable ? 'check-circle' : 'alert')}</header>
        <div class="card-body">
          <div class="tile-link"><span>${ui.icon('globe', 15)} Public webmail URL</span><span class="value mono">${esc(publicLabel)}</span></div>
          <div class="tile-link"><span>${ui.icon('layers', 15)} Redirect source</span><span class="value">${esc(data.publicUrlSource || 'Not set')}</span></div>
          <div class="tile-link"><span>${ui.icon('file-code', 15)} Environment file</span><span class="value mono">${esc(data.appEnvPath || 'Not configured')}</span></div>
          <div class="tile-link"><span>${ui.icon('plug', 15)} Providers ready</span><span class="value">${enabledCount} of ${providers.length}</span></div>
          <div class="notice tone-info mt-3">${ui.icon('info', 15)}<div><strong>Applying restarts the webmail services</strong><p>Saving a draft does not affect users. Apply writes the Google and Microsoft credentials to app.env, restarts both application services and waits for their health checks. Open sign-in windows and mailbox sync pause for a few seconds.</p></div></div>
          <div class="notice tone-warning mt-3">${ui.icon('alert', 15)}<div><strong>Changing the webmail hostname breaks sign-in</strong><p>Registered redirect URIs are tied to the exact webmail origin. If you change it under Web Net, update the same callback URLs in the Google and Microsoft consoles before the next sign-in.</p></div></div>
          <div class="notice tone-info mt-3">${ui.icon('key', 15)}<div><strong>Changing the OAuth application affects existing accounts</strong><p>Refresh tokens belong to the client application that issued them. After replacing a client ID, existing Gmail or Outlook accounts may be prompted to reconnect. Rotating only the client secret keeps the accounts signed in.</p></div></div>
        </div>
      </section>
      <section class="card">
        <header class="card-head"><div><h3>How users connect</h3><p>The flow after both applications are applied.</p></div></header>
        <div class="card-body">
          <ol class="oauth-flow">
            <li><strong>Choose a provider</strong><span>The user opens Add mail account and selects Gmail or Outlook.</span></li>
            <li><strong>Continue with the provider</strong><span>Alt8Mail sends the user to Google or Microsoft with PKCE and the mail scopes.</span></li>
            <li><strong>Consent</strong><span>The provider asks for read access and send permission for that mailbox.</span></li>
            <li><strong>Verify and save</strong><span>Alt8Mail verifies IMAP with the new token, seals the tokens and starts syncing.</span></li>
          </ol>
        </div>
      </section>
      <section class="card">
        <header class="card-head"><div><h3>Before you apply</h3><p>Common reasons a sign-in is rejected.</p></div></header>
        <div class="card-body">
          <ul class="oauth-checks">
            <li>${ui.icon('check', 13)} The webmail URL is public HTTPS, not a bare IP or an internal hostname.</li>
            <li>${ui.icon('check', 13)} The redirect URI in the provider console matches this page exactly, including case and trailing path.</li>
            <li>${ui.icon('check', 13)} The OAuth app is published or the user is a test user.</li>
            <li>${ui.icon('check', 13)} The Microsoft app has delegated Exchange Online permissions with admin consent.</li>
            <li>${ui.icon('check', 13)} The secret has not expired.</li>
          </ul>
        </div>
      </section>
    </div>
  </div>`;

  const form = document.getElementById('mail-oauth-form');
  if (!form) return;
  const collect = () => {
    const values = ui.formValues(form);
    return {
      providers: providers.map((provider) => ({
        provider: provider.provider,
        enabled: Boolean(values[`${provider.provider}Enabled`]),
        clientId: String(values[`${provider.provider}ClientId`] || '').trim(),
        tenant: String(values[`${provider.provider}Tenant`] || '').trim(),
        clientSecret: String(values[`${provider.provider}Secret`] || ''),
      })),
    };
  };
  const save = async ({ quiet = false } = {}) => {
    const response = await ctx.api('/control/api/mail-oauth', { method: 'PUT', body: collect() });
    if (!quiet) {
      ui.toast('Mail OAuth draft saved. Apply it to restart the webmail services with these credentials.', 'good', { title: 'Draft saved' });
      await renderMailOAuth(ctx);
    }
    return response;
  };
  const withBusy = async (button, task) => {
    if (!button) return;
    button.classList.add('is-loading');
    button.disabled = true;
    try {
      await task();
    } catch (error) {
      ui.toast(ctx.errorDetail(error), 'critical', { title: 'Mail OAuth' });
    } finally {
      button.classList.remove('is-loading');
      button.disabled = false;
    }
  };
  const showChecks = (result) => {
    const host = document.getElementById('mail-oauth-checks');
    if (host) host.innerHTML = checksTable(result.checks);
    const tested = Number(result.tested) || 0;
    const title = !tested ? 'No enabled providers to test' : result.ok ? 'Provider checks passed' : 'Provider checks need attention';
    const headline = !tested ? 'Both providers are disabled' : result.ok ? 'Every enabled provider is reachable' : 'One or more providers need attention';
    const detail = !tested
      ? 'Enable a provider and add its client ID and secret, then run the test again.'
      : result.ok
        ? 'The redirect URI and OAuth metadata match the applied webmail address. Apply the draft if you have not already.'
        : 'Fix the rows marked Needs attention, then run the test again.';
    ctx.modal({
      title,
      size: 'large',
      body: `${ui.inlineNotice(!tested ? 'info' : result.ok ? 'good' : 'warning', headline, detail)}
        <div class="divider"></div>${checksTable(result.checks)}`,
      footer: `<button class="btn btn-secondary" type="button" data-modal-close>Close</button>`,
    });
  };
  const runTest = async (button) => withBusy(button, async () => {
    await save({ quiet: true });
    const result = await ctx.api('/control/api/mail-oauth/test', { method: 'POST' });
    showChecks(result);
    await renderMailOAuth(ctx).catch(() => {});
  });
  form.addEventListener('submit', (event) => {
    event.preventDefault();
    if (canManage) save().catch((error) => ui.toast(ctx.errorDetail(error), 'critical', { title: 'Save failed' }));
  });
  document.getElementById('mail-oauth-refresh')?.addEventListener('click', () => renderMailOAuth(ctx));
  document.getElementById('mail-oauth-save')?.addEventListener('click', (event) => withBusy(event.currentTarget, () => save()));
  document.getElementById('mail-oauth-test')?.addEventListener('click', (event) => runTest(event.currentTarget));
  document.getElementById('mail-oauth-test-inline')?.addEventListener('click', (event) => runTest(event.currentTarget));
  document.getElementById('mail-oauth-apply')?.addEventListener('click', (event) => withBusy(event.currentTarget, async () => {
    await save({ quiet: true });
    const confirmed = await ctx.confirm({
      title: 'Apply mail sign-in credentials',
      message: 'System Control writes the Gmail and Microsoft credentials to app.env, restarts the webmail and admin services, and waits for their health checks. Anyone signing in or syncing at that moment is briefly interrupted. Continue?',
      confirmLabel: 'Apply credentials',
      tone: 'danger',
      details: '<div class="notice tone-warning">' + ui.icon('alert', 16) + '<div><strong>The restart is required</strong><p>The Go services read OAuth credentials only at startup. The restart does not delete accounts, tokens or mail.</p></div></div>',
    });
    if (!confirmed) return;
    const result = await ctx.api('/control/api/mail-oauth/apply', { method: 'POST' });
    ui.toast(result.message || 'Sign-in credentials applied.', 'good', { title: 'Mail OAuth applied' });
    await renderMailOAuth(ctx);
  }));
}
