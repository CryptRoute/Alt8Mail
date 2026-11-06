/* System-wide add-on catalog policy. */
import * as ui from './ui.js';

const esc = ui.esc;

export async function renderAddons(ctx) {
  const canManage = ctx.can('server.manage');
  ctx.setTitle('Add-ons', 'Infrastructure');
  let payload;
  try {
    payload = await ctx.api('/control/api/addons');
  } catch (error) {
    ctx.root.innerHTML = `${ui.pageHeader({ eyebrow: 'Infrastructure', title: 'Add-ons', description: 'Curated first-party connectors available to webmail tenants.' })}
      ${ui.inlineNotice('critical', 'Could not load the add-on catalog', error.message || String(error))}`;
    return;
  }
  const addons = Array.isArray(payload.addons) ? payload.addons : [];
  const enabled = Number(payload.enabled || 0);
  const total = Number(payload.count || addons.length);
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Infrastructure',
    title: 'Add-ons',
    description: 'Control which reviewed connectors this Alt8Mail system exposes. Workspace administrators still choose what to configure for their own organization, and users install from Webmail → Settings → Add-ons.',
    actions: `<button class="btn btn-secondary" type="button" id="addons-refresh">${ui.icon('refresh', 15)} Refresh</button>`,
    meta: `<span class="summary-pill">${ui.icon('plug', 13)} ${ui.num(enabled)} of ${ui.num(total)} enabled</span>`,
  })}
  ${ui.inlineNotice('info', 'System catalog policy', 'Disabling an add-on here removes it from every workspace. Existing connections and installs remain in the database but their actions stop immediately. This is a system-level switch, not a tenant permission.', '')}
  ${addons.length ? `<div class="grid" style="grid-template-columns:repeat(auto-fit,minmax(320px,1fr));gap:16px">${addons.map((addon) => addonCard(addon, canManage)).join('')}</div>` : ui.emptyState('No add-ons registered', 'The first-party catalog is seeded by the database migration. If this install predates it, update Alt8Mail, then refresh this page.', `<button class="btn btn-secondary" type="button" id="addons-refresh-empty">${ui.icon('refresh', 15)} Refresh</button>`, 'plug')}`;

  const reload = () => renderAddons(ctx);
  document.getElementById('addons-refresh')?.addEventListener('click', reload);
  document.getElementById('addons-refresh-empty')?.addEventListener('click', reload);
  ctx.root.querySelectorAll('[data-addon-enabled]').forEach((input) => {
    input.addEventListener('change', async () => {
      const addonID = input.getAttribute('data-addon-enabled');
      const next = input.checked;
      input.disabled = true;
      try {
        await ctx.api(`/control/api/addons/${encodeURIComponent(addonID)}`, {
          method: 'PATCH',
          body: { enabled: next },
        });
        ui.toast(`${addonLabel(addonID, addons)} ${next ? 'enabled' : 'disabled'} system-wide.`, next ? 'good' : 'warning');
        await reload();
      } catch (error) {
        input.checked = !next;
        input.disabled = false;
        ui.toast(error.message || 'Could not update the add-on policy.', 'critical', { title: 'Add-on policy' });
      }
    });
  });
}

function addonLogo(addon) {
  if (!addon.logoAsset) return `<span class="addon-control-logo is-fallback">${ui.icon('plug', 22)}</span>`;
  return `<span class="addon-control-logo"><img src="/assets/addon-logos/${encodeURIComponent(addon.logoAsset)}" alt="${esc(addon.name)} logo" loading="lazy" decoding="async"></span>`;
}

function providerLinks(addon) {
  const links = [
    ['Website', addon.websiteUrl],
    ['Support', addon.supportUrl],
    ['Docs', addon.docsUrl],
    ['Privacy', addon.privacyUrl],
  ].filter(([, url]) => url);
  if (!links.length) return '';
  return `<div class="addon-control-links">${links.map(([label, url]) =>
    `<a href="${esc(url)}" target="_blank" rel="noopener noreferrer">${esc(label)}</a>`
  ).join('')}</div>`;
}

function addonCard(addon, canManage) {
  const enabled = addon.enabled !== false;
  const scope = addon.connectionScope === 'tenant' ? 'Workspace connection' : 'Personal connection';
  const auth = addon.id === 'slack' ? 'OAuth / webhook' : addon.authType === 'oauth2' ? 'OAuth 2.0' : addon.authType === 'webhook' ? 'Webhook' : 'API key';
  return `<section class="card" data-addon-card="${esc(addon.id)}">
    <header class="card-head">
      <div class="addon-control-identity">${addonLogo(addon)}<div><h3>${esc(addon.name)}</h3><p>${esc(addon.vendor || 'Alt8Mail')} · ${esc(addon.category || 'Add-on')}</p></div></div>
      ${enabled ? ui.badge('Enabled', 'good', 'check-circle') : ui.badge('Disabled', 'neutral', 'x-circle')}
    </header>
    <div class="card-body">
      <p class="form-note addon-control-tagline">${esc(addon.tagline || addon.description || 'Reviewed first-party add-on.')}</p>
      ${providerLinks(addon)}
      <div style="margin-top:14px">${ui.kv([
        ['Scope', esc(scope)],
        ['Authentication', esc(auth)],
        ['Connector version', esc(addon.connectorVersion || '1.0')],
        ['Workspaces using it', ui.num(addon.workspaceCount || 0)],
        ['Active installs', ui.num(addon.installCount || 0)],
      ], 'is-2')}</div>
      <div style="margin-top:15px">${ui.switchField({
        label: 'Enable on this Alt8Mail system',
        name: `addon-enabled-${addon.id}`,
        checked: enabled,
        disabled: !canManage,
        help: canManage ? 'Disabled add-ons disappear from every webmail tenant.' : 'You need server.manage permission to change catalog policy.',
        extra: `data-addon-enabled="${esc(addon.id)}"`,
      })}</div>
    </div>
  </section>`;
}

function addonLabel(id, addons) {
  const addon = addons.find((item) => item.id === id);
  return addon?.name || id;
}
