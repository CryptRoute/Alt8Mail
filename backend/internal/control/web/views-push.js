/* Browser push settings for webmail notifications. */
import * as ui from './ui.js';

const esc = ui.esc;

export async function renderWebPush(ctx) {
  const canManage = ctx.can('notifications.manage');
  ctx.setTitle('Web push', 'Security');
  let payload;
  try {
    payload = await ctx.api('/control/api/web-push');
  } catch (error) {
    ctx.root.innerHTML = `${ui.pageHeader({ eyebrow: 'Security', title: 'Web push', description: 'Browser push notifications for webmail users.' })}${ui.inlineNotice('critical', 'Could not load web push settings', error.message || String(error))}`;
    return;
  }
  const enabled = Boolean(payload.enabled);
  const hourOptions = Array.from({ length: 24 }, (_, hour) => ({
    value: String(hour), label: `${String(hour).padStart(2, '0')}:00`,
  }));
  const statusTone = enabled && payload.keyPresent ? 'good' : enabled ? 'warning' : 'neutral';
  const statusText = enabled ? (payload.keyPresent ? 'Push enabled' : 'Enabled · key file missing') : 'Push disabled';
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Security', title: 'Web push',
    description: 'Deliver new-mail notifications to desktop and mobile browsers through the standard Web Push protocol. The control plane owns the VAPID identity; webmail users opt in per browser.',
    actions: `<button class="btn btn-secondary" type="button" id="webpush-refresh">${ui.icon('refresh', 15)} Refresh</button>${canManage ? `<button class="btn btn-secondary" type="button" id="webpush-test"${!enabled || !payload.subscriptionCount ? ' disabled' : ''}>${ui.icon('send', 15)} Send test</button><button class="btn btn-secondary btn-soft-danger" type="button" id="webpush-rotate">${ui.icon('key', 15)} Rotate keys</button>` : ''}`,
  })}
  <div class="grid grid-main">
    <div class="stack">
      <section class="card">
        <header class="card-head"><div><h3>Notification service</h3><p>Organization-wide switch and notification content.</p></div><span class="badge">${ui.icon('bell', 14)} Web Push</span></header>
        <div class="card-body">
          <form id="webpush-form">
            <div class="stack">
              ${ui.switchField({ label: 'Enable browser push', name: 'enabled', checked: enabled, disabled: !canManage, help: 'When off, users keep in-app notifications but no push is sent. Turning it on resets the delivery cursor to now.' })}
              <div class="form-grid">
                ${ui.field({ label: 'VAPID subject', name: 'subject', value: payload.subject || '', disabled: !canManage, required: true, placeholder: 'mailto:postmaster@example.com', help: 'A mailto: address or https URL that identifies this push service to browser push gateways.' })}
                ${ui.field({ label: 'Test recipient', name: 'testEmail', type: 'email', placeholder: 'user@example.com', help: 'Only used by Send test. Leave blank to use your control-plane email.' })}
              </div>
              <div class="form-grid">
                ${ui.switchField({ label: 'Include sender', name: 'includeSender', checked: payload.includeSender !== false, disabled: !canManage, help: 'Show the sender name in the notification body.' })}
                ${ui.switchField({ label: 'Include subject', name: 'includeSubject', checked: payload.includeSubject !== false, disabled: !canManage, help: 'Show the message subject in the notification body.' })}
              </div>
              ${ui.switchField({ label: 'Quiet hours', name: 'quietHoursEnabled', checked: Boolean(payload.quietHoursEnabled), disabled: !canManage, help: 'Suppress browser push during the selected server-time window. Messages remain available in webmail.' })}
              <div class="form-grid">
                ${ui.field({ label: 'Quiet from', name: 'quietStart', type: 'select', value: String(payload.quietStart ?? 22), options: hourOptions, disabled: !canManage })}
                ${ui.field({ label: 'Quiet until', name: 'quietEnd', type: 'select', value: String(payload.quietEnd ?? 7), options: hourOptions, disabled: !canManage })}
              </div>
              <div class="form-actions"><span class="form-note">Users choose whether this browser receives push in Webmail → Settings → Notifications.</span>${canManage ? `<div class="form-actions-right"><button class="btn btn-primary" type="submit">Save push settings</button></div>` : ''}</div>
            </div>
          </form>
        </div>
      </section>
      ${payload.publicKey ? `<section class="card"><header class="card-head"><div><h3>VAPID public key</h3><p>This is the only key material ever returned to a browser.</p></div></header><div class="card-body">${ui.codeBlock(payload.publicKey, { language: 'VAPID public key' })}<p class="form-note mt-3">Rotating the key invalidates every browser subscription; users must enable browser notifications again.</p></div></section>` : ''}
    </div>
    <div class="stack">
      <section class="card"><header class="card-head"><div><h3>Service status</h3><p>Delivery identity and connected browsers.</p></div>${ui.badge(statusText, statusTone)}</header><div class="card-body">
        <div class="tile-link"><span>${ui.icon('key', 15)} VAPID private key</span><span class="value">${payload.keyPresent ? ui.badge('Present', 'good') : ui.badge('Missing', 'warning')}</span></div>
        <div class="tile-link"><span>${ui.icon('users', 15)} Subscribed browsers</span><span class="value">${ui.num(payload.subscriptionCount || 0)}</span></div>
        <div class="tile-link"><span>${ui.icon('mail', 15)} Users reached</span><span class="value">${ui.num(payload.subscribedUsers || 0)}</span></div>
        <div class="tile-link"><span>${ui.icon('clock', 15)} Last updated</span><span class="value">${esc(payload.updatedAt ? ui.dateTime(payload.updatedAt) : 'Never')}</span></div>
        <div class="tile-link"><span>${ui.icon('shield', 15)} Updated by</span><span class="value">${esc(payload.updatedBy || 'System')}</span></div>
        ${!payload.keyPresent && enabled ? `<div class="notice tone-warning mt-3">${ui.icon('alert', 16)}<div><strong>Private key missing</strong><p>Save the settings to regenerate the VAPID key pair. Existing browser subscriptions will need to be recreated.</p></div></div>` : ''}
      </div></section>
      <section class="card"><header class="card-head"><div><h3>How browser push works</h3></div></header><div class="card-body">
        <div class="timeline">
          <div class="timeline-item"><span class="timeline-dot"></span><strong>User opts in</strong><span>Desktop notifications are enabled in webmail settings and the browser grants permission.</span></div>
          <div class="timeline-item"><span class="timeline-dot"></span><strong>Subscription registered</strong><span>The browser's push endpoint is linked to that mailbox account.</span></div>
          <div class="timeline-item"><span class="timeline-dot"></span><strong>New mail arrives</strong><span>Alt8Mail signs a VAPID notification and the browser OS shows it, even when the tab is closed.</span></div>
        </div>
        <div class="note-strip mt-3">${ui.icon('info', 13)} <span>Web Push requires HTTPS. Notification permission is always controlled by the user and can be revoked in the browser at any time.</span></div>
      </div></section>
    </div>
  </div>`;

  document.getElementById('webpush-refresh')?.addEventListener('click', () => renderWebPush(ctx));
  document.getElementById('webpush-test')?.addEventListener('click', async (event) => {
    const button = event.currentTarget;
    button.disabled = true;
    try {
      const target = form?.querySelector('[name="testEmail"]')?.value.trim() || '';
      const result = await ctx.api('/control/api/web-push/test', { method: 'POST', body: { email: target } });
      ui.toast(`Test sent to ${result.email || target || 'your account'} · ${ui.num(result.sent)} browser${result.sent === 1 ? '' : 's'}${result.failed ? ` · ${ui.num(result.failed)} failed` : ''}.`, result.sent ? 'good' : 'warning');
    } catch (error) { ui.toast(error.message || 'Could not send test push.', 'critical', { title: 'Push test failed' }); }
    finally { button.disabled = false; }
  });
  document.getElementById('webpush-rotate')?.addEventListener('click', async () => {
    const ok = await ctx.confirm({ title: 'Rotate VAPID keys', message: 'Every current browser subscription will be removed and users must enable desktop notifications again. Continue?', confirmLabel: 'Rotate keys', tone: 'danger' });
    if (!ok) return;
    try {
      await ctx.api('/control/api/web-push/rotate', { method: 'POST' });
      ui.toast('VAPID keys rotated. Existing subscriptions were cleared.', 'good');
      await renderWebPush(ctx);
    } catch (error) { ui.toast(error.message || 'Could not rotate keys.', 'critical', { title: 'Key rotation failed' }); }
  });
  const form = document.getElementById('webpush-form');
  form?.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (!canManage) return;
    const values = ui.formValues(form);
    const button = form.querySelector('button[type="submit"]');
    button.disabled = true;
    try {
      await ctx.api('/control/api/web-push', { method: 'PUT', body: {
        enabled: Boolean(values.enabled), subject: values.subject || '',
        includeSender: Boolean(values.includeSender), includeSubject: Boolean(values.includeSubject),
        quietHoursEnabled: Boolean(values.quietHoursEnabled),
        quietStart: Number(values.quietStart), quietEnd: Number(values.quietEnd),
      } });
      ui.toast('Browser push settings saved.', 'good');
      await renderWebPush(ctx);
    } catch (error) { ui.toast(error.message || 'Could not save browser push settings.', 'critical', { title: 'Save failed' }); button.disabled = false; }
  });
}
