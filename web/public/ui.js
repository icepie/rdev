(function () {
  const sprite = {
    github: '<path fill="currentColor" stroke="none" d="M12 .5A12 12 0 0 0 8.2 23.9c.6.1.8-.2.8-.6v-2.2c-3.3.7-4-1.4-4-1.4-.5-1.4-1.3-1.8-1.3-1.8-1.1-.7.1-.7.1-.7 1.2.1 1.9 1.2 1.9 1.2 1.1 1.9 2.9 1.3 3.6 1 .1-.8.4-1.3.8-1.6-2.6-.3-5.4-1.3-5.4-5.9 0-1.3.5-2.4 1.2-3.2-.1-.3-.5-1.5.1-3.2 0 0 1-.3 3.3 1.2a11.4 11.4 0 0 1 6 0c2.3-1.5 3.3-1.2 3.3-1.2.6 1.7.2 2.9.1 3.2.8.8 1.2 1.9 1.2 3.2 0 4.6-2.8 5.6-5.4 5.9.4.4.8 1.1.8 2.2v3.2c0 .4.2.7.8.6A12 12 0 0 0 12 .5Z"/>',
    terminal: '<path d="m4 17 6-6-6-6"/><path d="M12 19h8"/>',
    package: '<path d="m16.5 9.4-9-5.2"/><path d="M21 16V8a2 2 0 0 0-1-1.7l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.7l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z"/><path d="m3.3 7 8.7 5 8.7-5"/><path d="M12 22V12"/>',
    sessions: '<path d="M4 7h16"/><path d="M4 12h16"/><path d="M4 17h16"/><circle cx="7" cy="7" r="1"/><circle cx="7" cy="12" r="1"/><circle cx="7" cy="17" r="1"/>',
    rocket: '<path d="M4.5 16.5c-1.5 1.3-2 4-2 4s2.7-.5 4-2c.8-.8.8-2.1 0-2.9-.8-.8-2.1-.8-2.9 0Z"/><path d="M9 15 3 9s1.5-4.5 6-4.5c1.8 0 3.5.7 4.8 2L18 2s2 4.5-.5 8.5C16 13 13.5 14.5 9 15Z"/><path d="M9 15s.5 2.5 2 4c0 0 3.5-2.5 4-6"/>',
    lockOpen: '<rect width="18" height="11" x="3" y="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 9.9-1"/>',
    key: '<circle cx="7.5" cy="15.5" r="5.5"/><path d="m21 2-9.6 9.6"/><path d="m15.5 7.5 3 3L22 7l-3-3"/>',
    lock: '<rect width="18" height="11" x="3" y="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>',
    radio: '<path d="M4.9 19.1a10 10 0 0 1 0-14.2"/><path d="M7.8 16.2a6 6 0 0 1 0-8.4"/><circle cx="12" cy="12" r="2"/><path d="M16.2 7.8a6 6 0 0 1 0 8.4"/><path d="M19.1 4.9a10 10 0 0 1 0 14.2"/>',
    inbox: '<path d="M22 12h-6l-2 3h-4l-2-3H2"/><path d="m5.5 5.1-3.3 6A2 2 0 0 0 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6a2 2 0 0 0-.2-.9l-3.3-6A2 2 0 0 0 16.8 4H7.2a2 2 0 0 0-1.7 1.1Z"/>',
    folder: '<path d="M4 20h16a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7l-2-2H4a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2Z"/>',
    upload: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><path d="m17 8-5-5-5 5"/><path d="M12 3v12"/>',
    play: '<polygon points="5 3 19 12 5 21 5 3"/>',
    close: '<path d="M18 6 6 18"/><path d="m6 6 12 12"/>',
    sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2"/><path d="M12 20v2"/><path d="m4.93 4.93 1.41 1.41"/><path d="m17.66 17.66 1.41 1.41"/><path d="M2 12h2"/><path d="M20 12h2"/><path d="m6.34 17.66-1.41 1.41"/><path d="m19.07 4.93-1.41 1.41"/>',
    moon: '<path d="M12 3a6 6 0 0 0 9 7.4A9 9 0 1 1 12 3Z"/>',
    monitor: '<rect width="20" height="14" x="2" y="3" rx="2"/><path d="M8 21h8"/><path d="M12 17v4"/>'
  };

  function injectIconStyles() {
    if (document.getElementById('rdev-icon-styles')) return;
    if (!document.head) {
      document.addEventListener('DOMContentLoaded', injectIconStyles, { once: true });
      return;
    }
    const style = document.createElement('style');
    style.id = 'rdev-icon-styles';
    style.textContent = `
      :root[data-theme="dark"] {
        --rdev-code-bg: #05070d;
        --rdev-code-text: var(--text);
        --rdev-code-border: transparent;
        --rdev-hover-bg: rgba(255,255,255,0.03);
        --rdev-auth-overlay: rgba(15,17,23,0.92);
      }
      :root[data-theme="light"] {
        --rdev-code-bg: #f8fafc;
        --rdev-code-text: #334155;
        --rdev-code-border: #e2e8f0;
        --rdev-hover-bg: rgba(15,23,42,0.04);
        --rdev-auth-overlay: rgba(248,250,252,0.92);
      }
      .icon {
        width: 1em;
        height: 1em;
        display: inline-block;
        flex: 0 0 auto;
        vertical-align: -0.14em;
        color: currentColor;
        overflow: visible;
      }
      .icon-inline {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        min-width: 0;
        line-height: 1.2;
      }
      .icon-inline > [data-icon] {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        flex: 0 0 auto;
        width: 1rem;
        height: 1rem;
      }
      .icon-inline > [data-icon] > .icon {
        display: block;
      }
      #lang-slot {
        display: inline-flex;
        align-items: center;
        gap: 8px;
        flex-wrap: wrap;
        min-width: 0;
      }
      #lang-slot .lang-switch {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        min-height: 28px;
        margin: 0;
        line-height: 1;
        white-space: nowrap;
      }
      #lang-slot .lang-switch select {
        min-height: 28px;
      }
      .theme-toggle {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        min-height: 28px;
        border: 1px solid var(--border);
        background: var(--card);
        color: var(--muted);
        border-radius: 7px;
        padding: 4px 8px;
        cursor: pointer;
        font-size: 0.78rem;
        white-space: nowrap;
      }
      .theme-toggle:hover {
        color: var(--accent);
        border-color: var(--accent);
      }
      .cmd-code,
      .hint code {
        background: var(--rdev-code-bg) !important;
        color: var(--rdev-code-text) !important;
        border: 1px solid var(--rdev-code-border);
      }
      :root[data-theme="light"] .cmd-code,
      :root[data-theme="light"] .hint code {
        box-shadow: inset 0 0 0 1px rgba(226,232,240,0.6);
      }
      tbody tr:hover,
      .device-item:hover,
      .sess-item:hover,
      .tab-item:hover {
        background: var(--rdev-hover-bg) !important;
      }
      .auth-overlay {
        background: var(--rdev-auth-overlay) !important;
      }
    `;
    document.head.appendChild(style);
  }

  function icon(name, label) {
    const safeName = String(name || '').replace(/[^a-zA-Z0-9_-]/g, '');
    return '<svg class="icon icon-' + safeName + '" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' + (sprite[safeName] || '') + '</svg>' + (label ? '<span>' + label + '</span>' : '');
  }

  function currentTheme() {
    return localStorage.getItem('rdevTheme') || 'auto';
  }

  function effectiveTheme(theme) {
    if (theme === 'light' || theme === 'dark') return theme;
    return matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
  }

  function applyTheme(theme) {
    const selected = theme || currentTheme();
    document.documentElement.dataset.theme = effectiveTheme(selected);
    document.documentElement.dataset.themeChoice = selected;
    document.querySelectorAll('[data-theme-toggle]').forEach(btn => {
      btn.innerHTML = icon(effectiveTheme(selected) === 'dark' ? 'moon' : 'sun') + '<span>' + (window.t ? t('theme.' + selected) : selected) + '</span>';
      btn.title = window.t ? t('theme.toggle') : 'Theme';
    });
  }

  function nextTheme() {
    const order = ['auto', 'light', 'dark'];
    const theme = order[(order.indexOf(currentTheme()) + 1) % order.length];
    localStorage.setItem('rdevTheme', theme);
    applyTheme(theme);
  }

  function themeButton() {
    return '<button class="theme-toggle" data-theme-toggle type="button"></button>';
  }

  document.addEventListener('click', event => {
    const btn = event.target.closest && event.target.closest('[data-theme-toggle]');
    if (btn) nextTheme();
  });
  window.matchMedia('(prefers-color-scheme: light)').addEventListener('change', () => applyTheme());
  document.addEventListener('DOMContentLoaded', () => applyTheme());
  injectIconStyles();
  window.addEventListener('rdev:langchange', () => applyTheme());

  window.RDevUI = { icon, themeButton, applyTheme };
  window.icon = icon;
})();
