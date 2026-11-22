/* Webmail language availability and the system default locale. */
import * as ui from './ui.js?v=3.11.2';

const esc = ui.esc;

export async function renderLanguages(ctx) {
  const canManage = ctx.can('settings.write');
  ctx.setTitle('Languages', 'Workspace');
  let payload;
  try {
    payload = await ctx.api('/control/api/languages');
  } catch (error) {
    ctx.root.innerHTML = `${ui.pageHeader({ eyebrow: 'Workspace', title: 'Languages', description: 'Choose the languages available across webmail, the administration console and System Control.' })}
      ${ui.inlineNotice('critical', 'Could not load the language policy', error.message || String(error))}`;
    return;
  }
  const all = Array.isArray(payload.locales) ? payload.locales : [];
  const locales = all.filter((item) => item.status !== 'pseudo' && !item.hidden);
  const enabled = locales.filter((item) => item.enabled !== false);
  const defaultLocale = payload.default || 'en-US';
  const defaultOptions = enabled.map((item) => ({
    value: item.code,
    label: `${item.label} · ${item.code}`,
  }));
  if (!defaultOptions.some((option) => option.value === defaultLocale) && defaultLocale) {
    const current = locales.find((item) => item.code === defaultLocale);
    defaultOptions.unshift({ value: defaultLocale, label: current ? `${current.label} · ${current.code}` : defaultLocale });
  }
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Workspace',
    title: 'Languages',
    description: 'Control which shipped languages this Alt8Mail system offers. Changes apply to webmail, the administration console and System Control on their next request.',
    meta: `<span class="summary-pill">${ui.icon('globe', 13)} ${ui.num(enabled.length)} of ${ui.num(locales.length)} enabled</span>`,
  })}
  ${ui.inlineNotice('info', 'System-wide policy',
    'Disabling a language removes it from every language picker and rejects it if a stale client tries to save it. English (United States) always stays enabled as the fallback, and the current default language cannot be disabled until another default is chosen.',
    '')}
  ${ui.card(`<form id="language-policy-form">
      <div class="field" style="max-width:420px">
        <label for="language-default">Default system language</label>
        <select class="input" id="language-default" name="default"${canManage ? '' : ' disabled'}>
          ${defaultOptions.map((option) => `<option value="${esc(option.value)}"${option.value === defaultLocale ? ' selected' : ''}>${esc(option.label)}</option>`).join('')}
        </select>
        <small class="field-help">Used by webmail, the administration console and System Control unless the user or administrator has chosen their own language.</small>
      </div>
      <div class="grid" style="grid-template-columns:repeat(auto-fit,minmax(290px,1fr));gap:10px 18px;margin-top:16px">
        ${locales.map((item) => localeSwitch(item, canManage)).join('')}
      </div>
      <div class="form-actions" style="margin-top:20px">
        <button class="btn btn-secondary" type="button" id="languages-enable-all"${canManage ? '' : ' disabled'}>${ui.icon('rotate', 15)} Re-enable all</button>
        <div class="form-actions-right">
          <button class="btn btn-primary" type="submit"${canManage ? '' : ' disabled'}>${ui.icon('check', 15)} Save language policy</button>
        </div>
      </div>
    </form>`, {
    title: 'Language availability',
    subtitle: 'The list comes from the locale manifest shipped with this release. Adding a catalog in a future release automatically adds it here.',
  })}`;

  const form = document.getElementById('language-policy-form');
  if (!form) return;
  const defaultSelect = form.querySelector('[name="default"]');
  form.querySelectorAll('[data-locale-code]').forEach((input) => {
    input.addEventListener('change', () => {
      if (!input.checked && defaultSelect && input.getAttribute('data-locale-code') === defaultSelect.value) {
        defaultSelect.value = 'en-US';
        ui.toast('The disabled language was the default; the default moved to English (United States).', 'warning');
      }
    });
  });
  const save = async (disabled, defaultCode, button) => {
    if (button) {
      button.disabled = true;
      button.dataset.label = button.innerHTML;
      button.innerHTML = `${ui.icon('refresh', 15)} Saving…`;
    }
    try {
      await ctx.api('/control/api/languages', { method: 'PUT', body: { disabled, default: defaultCode } });
      ui.toast('Webmail language policy saved.', 'good');
      await renderLanguages(ctx);
    } catch (error) {
      if (button) {
        button.disabled = false;
        button.innerHTML = button.dataset.label || 'Save language policy';
      }
      ui.toast(error.message || 'Could not save the language policy.', 'critical', { title: 'Languages' });
    }
  };
  form.addEventListener('submit', (event) => {
    event.preventDefault();
    const disabled = Array.from(form.querySelectorAll('[data-locale-code]'))
      .filter((input) => !input.checked)
      .map((input) => input.getAttribute('data-locale-code'));
    const button = form.querySelector('button[type="submit"]');
    save(disabled, defaultSelect ? defaultSelect.value : defaultLocale, button);
  });
  document.getElementById('languages-enable-all')?.addEventListener('click', async () => {
    const confirmed = await ui.confirmDialog({
      title: 'Re-enable all languages?',
      message: 'Every shipped webmail language will be available to users again. The current default language is kept.',
      confirmLabel: 'Re-enable all',
      tone: 'primary',
    });
    if (confirmed) save([], defaultSelect ? defaultSelect.value : defaultLocale, document.getElementById('languages-enable-all'));
  });
}

function localeSwitch(item, canManage) {
  const required = item.code === 'en-US';
  const help = `${String(item.dir || 'ltr').toUpperCase()} · ${item.status || 'available'}${required ? ' · required fallback' : ''}`;
  return ui.switchField({
    label: `${item.label || item.code} · ${item.code}`,
    name: `locale-${item.code}`,
    checked: item.enabled !== false,
    disabled: !canManage || required,
    help,
    extra: `data-locale-code="${esc(item.code)}"`,
  });
}
