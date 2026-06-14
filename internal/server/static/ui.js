(function () {
  const sprite = {
    github: '<path fill="currentColor" stroke="none" d="M12 .5A12 12 0 0 0 8.2 23.9c.6.1.8-.2.8-.6v-2.2c-3.3.7-4-1.4-4-1.4-.5-1.4-1.3-1.8-1.3-1.8-1.1-.7.1-.7.1-.7 1.2.1 1.9 1.2 1.9 1.2 1.1 1.9 2.9 1.3 3.6 1 .1-.8.4-1.3.8-1.6-2.6-.3-5.4-1.3-5.4-5.9 0-1.3.5-2.4 1.2-3.2-.1-.3-.5-1.5.1-3.2 0 0 1-.3 3.3 1.2a11.4 11.4 0 0 1 6 0c2.3-1.5 3.3-1.2 3.3-1.2.6 1.7.2 2.9.1 3.2.8.8 1.2 1.9 1.2 3.2 0 4.6-2.8 5.6-5.4 5.9.4.4.8 1.1.8 2.2v3.2c0 .4.2.7.8.6A12 12 0 0 0 12 .5Z"/>',
    terminal: '<path d="m4 17 6-6-6-6"/><path d="M12 19h8"/>',
    package: '<path d="m16.5 9.4-9-5.2"/><path d="M21 16V8a2 2 0 0 0-1-1.7l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.7l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z"/><path d="m3.3 7 8.7 5 8.7-5"/><path d="M12 22V12"/>',
    sessions: '<path d="M4 7h16"/><path d="M4 12h16"/><path d="M4 17h16"/><circle cx="7" cy="7" r="1"/><circle cx="7" cy="12" r="1"/><circle cx="7" cy="17" r="1"/>',
    files: '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z"/><path d="M14 2v6h6"/><path d="M9 15h6"/><path d="M9 18h6"/>',
    download: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><path d="M7 10l5 5 5-5"/><path d="M12 15V3"/>',
    refresh: '<path d="M21 12a9 9 0 0 1-15.5 6.2L3 16"/><path d="M3 21v-5h5"/><path d="M3 12A9 9 0 0 1 18.5 5.8L21 8"/><path d="M21 3v5h-5"/>',
    pause: '<rect x="6" y="4" width="4" height="16"/><rect x="14" y="4" width="4" height="16"/>',
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
    check: '<path d="M20 6 9 17l-5-5"/>',
    chevronDown: '<path d="m6 9 6 6 6-6"/>',
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
      .header-right,
      .toolbar,
      header {
        min-width: 0;
      }
      .header-right {
        justify-content: flex-end;
      }
      .header-btns,
      .nav {
        display: flex;
        align-items: center;
        justify-content: flex-end;
        gap: 8px;
        min-width: 0;
      }
      .header-btns {
        flex-wrap: nowrap;
      }
      .nav {
        flex-wrap: wrap;
      }
      .header-btns .btn,
      .nav a {
        min-height: 32px;
        display: inline-flex;
        align-items: center;
        justify-content: center;
        gap: 6px;
        white-space: nowrap;
        line-height: 1;
      }
      .toolbar > #lang-slot,
      header > #lang-slot {
        flex: 0 0 auto;
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
      .rdev-select {
        position: relative;
        min-width: 0;
        font-size: 0.84rem;
      }
      .rdev-select-trigger {
        width: 100%;
        min-height: 30px;
        display: inline-flex;
        align-items: center;
        justify-content: space-between;
        gap: 8px;
        border: 1px solid var(--border);
        background: var(--bg, var(--card));
        color: var(--text);
        border-radius: 7px;
        padding: 5px 8px;
        cursor: pointer;
        font: inherit;
        text-align: left;
      }
      .rdev-select-trigger:hover,
      .rdev-select.open .rdev-select-trigger {
        color: var(--accent);
        border-color: var(--accent);
      }
      .rdev-select-value {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
      }
      .rdev-select-value .icon {
        width: 0.95em;
        height: 0.95em;
      }
      .rdev-select-menu {
        position: absolute;
        z-index: 80;
        top: calc(100% + 5px);
        left: 0;
        min-width: 100%;
        max-width: min(360px, 92vw);
        max-height: 260px;
        overflow: auto;
        display: none;
        padding: 5px;
        border: 1px solid var(--border);
        border-radius: 8px;
        background: var(--card);
        color: var(--text);
        box-shadow: 0 18px 48px rgba(0,0,0,0.28);
      }
      .rdev-select.open .rdev-select-menu {
        display: grid;
        gap: 3px;
      }
      .rdev-select-option {
        width: 100%;
        min-height: 30px;
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 10px;
        border: 0;
        border-radius: 6px;
        background: transparent;
        color: var(--text);
        padding: 6px 8px;
        cursor: pointer;
        font: inherit;
        text-align: left;
      }
      .rdev-select-option:hover,
      .rdev-select-option.active {
        background: var(--rdev-hover-bg);
        color: var(--accent);
      }
      .rdev-select-option:disabled {
        opacity: 0.55;
        cursor: not-allowed;
      }
      .rdev-select-option-label {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
      }
      .rdev-select-check {
        width: 1em;
        height: 1em;
        opacity: 0;
      }
      .rdev-select-option.active .rdev-select-check {
        opacity: 1;
      }
      .rdev-modal-overlay {
        position: fixed;
        inset: 0;
        z-index: 200;
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 18px;
        background: rgba(5,7,13,0.56);
        backdrop-filter: blur(8px);
      }
      .rdev-modal {
        width: min(420px, 100%);
        border: 1px solid var(--border);
        border-radius: 10px;
        background: var(--card);
        color: var(--text);
        box-shadow: 0 22px 70px rgba(0,0,0,0.38);
        overflow: hidden;
      }
      .rdev-modal-head {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 12px;
        padding: 14px 16px 10px;
      }
      .rdev-modal-title {
        font-size: 1rem;
        font-weight: 650;
        color: var(--text);
      }
      .rdev-modal-close {
        width: 30px;
        height: 30px;
        border: 1px solid var(--border);
        border-radius: 7px;
        background: var(--bg);
        color: var(--muted);
        display: inline-flex;
        align-items: center;
        justify-content: center;
        cursor: pointer;
      }
      .rdev-modal-close:hover {
        color: var(--red);
        border-color: var(--red);
      }
      .rdev-modal-body {
        padding: 0 16px 14px;
      }
      .rdev-modal-message {
        color: var(--muted);
        font-size: 0.9rem;
        line-height: 1.55;
        margin-bottom: 12px;
      }
      .rdev-modal-input {
        width: 100%;
        border: 1px solid var(--border);
        border-radius: 8px;
        background: var(--bg);
        color: var(--text);
        padding: 10px 12px;
        outline: none;
        font-size: 0.95rem;
      }
      .rdev-modal-input:focus {
        border-color: var(--accent);
      }
      .rdev-modal-actions {
        display: flex;
        justify-content: flex-end;
        gap: 8px;
        padding: 12px 16px 16px;
        border-top: 1px solid var(--border);
      }
      .rdev-modal-btn {
        min-height: 34px;
        border: 1px solid var(--border);
        border-radius: 8px;
        background: var(--bg);
        color: var(--text);
        padding: 8px 12px;
        cursor: pointer;
        font-size: 0.88rem;
      }
      .rdev-modal-btn:hover {
        color: var(--accent);
        border-color: var(--accent);
      }
      .rdev-modal-btn.primary {
        background: var(--accent);
        border-color: var(--accent);
        color: var(--bg);
        font-weight: 650;
      }
      .rdev-modal-btn.primary:hover {
        color: var(--bg);
        opacity: 0.9;
      }
      :root[data-theme="light"] .rdev-modal-overlay {
        background: rgba(15,23,42,0.28);
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
      @media (max-width: 760px) {
        .header-right {
          justify-content: flex-start;
        }
        .header-btns,
        .nav {
          width: 100%;
          justify-content: flex-start;
          overflow-x: auto;
          flex-wrap: nowrap;
          -webkit-overflow-scrolling: touch;
          scrollbar-width: thin;
        }
        .header-btns .btn,
        .nav a {
          flex: 0 0 auto;
        }
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

  function closeOpenSelects(except) {
    document.querySelectorAll('.rdev-select.open').forEach(el => {
      if (el !== except) {
        el.classList.remove('open');
        el.setAttribute('aria-expanded', 'false');
      }
    });
  }

  function optionLabel(option) {
    return option && option.html ? option.html : esc(option && option.label !== undefined ? option.label : option && option.value || '');
  }

  function renderSelect(el) {
    const state = el._rdevSelect;
    if (!state) return;
    const current = state.options.find(opt => opt.value === state.value) || state.options[0] || { value: '', label: state.placeholder || '' };
    state.valueNode.innerHTML = optionLabel(current);
    state.menu.innerHTML = state.options.map(opt => {
      const active = opt.value === state.value ? ' active' : '';
      const disabled = opt.disabled ? ' disabled' : '';
      return '<button type="button" class="rdev-select-option' + active + '" role="option" data-value="' + attr(opt.value) + '"' + disabled + ' aria-selected="' + (opt.value === state.value ? 'true' : 'false') + '">' +
        '<span class="rdev-select-option-label">' + optionLabel(opt) + '</span><span class="rdev-select-check">' + icon('check') + '</span></button>';
    }).join('');
  }

  function setSelectValue(el, value, notify) {
    const state = el._rdevSelect;
    if (!state) return;
    const next = value == null ? '' : String(value);
    state.value = next;
    el.dataset.value = next;
    renderSelect(el);
    if (notify) el.dispatchEvent(new Event('change', { bubbles: true }));
  }

  function enhanceSelect(el, options) {
    if (!el) return null;
    if (el._rdevSelect) return el;
    const initial = options && options.value !== undefined ? options.value : (el.dataset.value || '');
    el.classList.add('rdev-select');
    el.setAttribute('role', 'combobox');
    el.setAttribute('aria-haspopup', 'listbox');
    el.setAttribute('aria-expanded', 'false');
    el.tabIndex = -1;
    el.innerHTML = '<button type="button" class="rdev-select-trigger"><span class="rdev-select-value"></span>' + icon('chevronDown') + '</button><div class="rdev-select-menu" role="listbox"></div>';
    el._rdevSelect = {
      value: String(initial || ''),
      options: options && options.options ? options.options.slice() : [],
      placeholder: options && options.placeholder || '',
      trigger: el.querySelector('.rdev-select-trigger'),
      valueNode: el.querySelector('.rdev-select-value'),
      menu: el.querySelector('.rdev-select-menu')
    };
    try {
      Object.defineProperty(el, 'value', {
        configurable: true,
        get() { return this._rdevSelect ? this._rdevSelect.value : ''; },
        set(v) { setSelectValue(this, v, false); }
      });
    } catch(e) {}
    el._rdevSelect.trigger.addEventListener('click', event => {
      event.stopPropagation();
      const open = !el.classList.contains('open');
      closeOpenSelects(open ? el : null);
      el.classList.toggle('open', open);
      el.setAttribute('aria-expanded', open ? 'true' : 'false');
    });
    el._rdevSelect.menu.addEventListener('click', event => {
      const item = event.target.closest && event.target.closest('.rdev-select-option');
      if (!item || item.disabled) return;
      setSelectValue(el, item.dataset.value, true);
      el.classList.remove('open');
      el.setAttribute('aria-expanded', 'false');
      el._rdevSelect.trigger.focus();
    });
    el.addEventListener('keydown', event => {
      if (event.key === 'Escape') {
        el.classList.remove('open');
        el.setAttribute('aria-expanded', 'false');
      }
      if (event.key === 'Enter' || event.key === ' ') {
        event.preventDefault();
        el._rdevSelect.trigger.click();
      }
    });
    renderSelect(el);
    return el;
  }

  function setSelectOptions(el, options, value) {
    if (!el) return;
    enhanceSelect(el);
    const state = el._rdevSelect;
    state.options = (options || []).map(opt => typeof opt === 'string' ? { value: opt, label: opt } : Object.assign({}, opt, { value: String(opt.value == null ? '' : opt.value) }));
    const requested = value !== undefined ? String(value || '') : state.value;
    const exists = state.options.some(opt => opt.value === requested);
    state.value = exists ? requested : (state.options[0] ? state.options[0].value : '');
    el.dataset.value = state.value;
    renderSelect(el);
  }

  function modal(kind, options) {
    options = options || {};
    return new Promise(resolve => {
      const overlay = document.createElement('div');
      overlay.className = 'rdev-modal-overlay';
      const title = options.title || (kind === 'prompt' ? (window.t ? t('common.password') : 'Input') : (window.t ? t('common.error') : 'Message'));
      const message = options.message || '';
      const cancelText = options.cancelText || (window.t ? t('common.cancel') : 'Cancel');
      const confirmText = options.confirmText || (window.t ? t('common.ok') : 'OK');
      overlay.innerHTML = '<div class="rdev-modal" role="dialog" aria-modal="true">' +
        '<div class="rdev-modal-head"><div class="rdev-modal-title">' + esc(title) + '</div><button type="button" class="rdev-modal-close" aria-label="Close">' + icon('close') + '</button></div>' +
        '<div class="rdev-modal-body">' + (message ? '<div class="rdev-modal-message">' + esc(message) + '</div>' : '') + (kind === 'prompt' ? '<input class="rdev-modal-input" type="' + esc(options.type || 'text') + '" placeholder="' + esc(options.placeholder || '') + '" autocomplete="off">' : '') + '</div>' +
        '<div class="rdev-modal-actions">' + (kind === 'prompt' ? '<button type="button" class="rdev-modal-btn" data-modal-cancel>' + esc(cancelText) + '</button>' : '') + '<button type="button" class="rdev-modal-btn primary" data-modal-ok>' + esc(confirmText) + '</button></div>' +
        '</div>';
      document.body.appendChild(overlay);
      const input = overlay.querySelector('.rdev-modal-input');
      const cleanup = value => {
        document.removeEventListener('keydown', onKey);
        overlay.remove();
        resolve(value);
      };
      const submit = () => cleanup(kind === 'prompt' ? (input ? input.value : '') : undefined);
      const cancel = () => cleanup(kind === 'prompt' ? null : undefined);
      const onKey = event => {
        if (event.key === 'Escape') cancel();
        if (event.key === 'Enter' && kind === 'prompt') submit();
      };
      overlay.querySelector('[data-modal-ok]').addEventListener('click', submit);
      overlay.querySelector('.rdev-modal-close').addEventListener('click', cancel);
      const cancelBtn = overlay.querySelector('[data-modal-cancel]');
      if (cancelBtn) cancelBtn.addEventListener('click', cancel);
      overlay.addEventListener('mousedown', event => { if (event.target === overlay) cancel(); });
      document.addEventListener('keydown', onKey);
      setTimeout(() => (input || overlay.querySelector('[data-modal-ok]')).focus(), 20);
    });
  }

  function prompt(options) {
    return modal('prompt', options);
  }

  function alert(options) {
    return modal('alert', options);
  }

  let workerBridge;
  let socketSeq = 0;
  const workerSockets = new Map();

  function ensureWorkerBridge() {
    if (!('Worker' in window)) return null;
    if (workerBridge) return workerBridge;
    try {
      workerBridge = new Worker('/ws-worker.js');
      workerBridge.onmessage = event => {
        const msg = event.data || {};
        const socket = workerSockets.get(msg.id);
        if (!socket) return;
        if (msg.op === 'open') {
          socket.readyState = WebSocket.OPEN;
          if (socket.onopen) socket.onopen(new Event('open'));
          socket.dispatchEvent(new Event('open'));
        } else if (msg.op === 'message') {
          const evt = new MessageEvent('message', { data: msg.data });
          if (socket.onmessage) socket.onmessage(evt);
          socket.dispatchEvent(evt);
        } else if (msg.op === 'close') {
          socket.readyState = WebSocket.CLOSED;
          workerSockets.delete(msg.id);
          const evt = new CloseEvent('close', { code: msg.code || 1000, reason: msg.reason || '', wasClean: !!msg.wasClean });
          if (socket.onclose) socket.onclose(evt);
          socket.dispatchEvent(evt);
        } else if (msg.op === 'error') {
          const evt = new Event('error');
          evt.message = msg.message || '';
          if (socket.onerror) socket.onerror(evt);
          socket.dispatchEvent(evt);
        }
      };
      workerBridge.onerror = () => {
        workerBridge = null;
      };
    } catch(e) {
      workerBridge = null;
    }
    return workerBridge;
  }

  function socket(url, options) {
    options = options || {};
    if (options.worker === false) return new WebSocket(url);
    const bridge = ensureWorkerBridge();
    if (!bridge) return new WebSocket(url);
    const id = ++socketSeq;
    const listeners = {};
    const api = {
      id,
      url,
      readyState: WebSocket.CONNECTING,
      binaryType: options.binaryType || 'arraybuffer',
      onopen: null,
      onmessage: null,
      onclose: null,
      onerror: null,
      send(data) {
        const transfer = data instanceof ArrayBuffer ? [data] : undefined;
        bridge.postMessage({ id, op: 'send', data }, transfer);
      },
      close(code, reason) {
        api.readyState = WebSocket.CLOSING;
        bridge.postMessage({ id, op: 'close', code, reason });
      },
      addEventListener(type, fn) {
        if (!listeners[type]) listeners[type] = new Set();
        listeners[type].add(fn);
      },
      removeEventListener(type, fn) {
        if (listeners[type]) listeners[type].delete(fn);
      },
      dispatchEvent(event) {
        const set = listeners[event.type];
        if (set) set.forEach(fn => fn.call(api, event));
      }
    };
    workerSockets.set(id, api);
    bridge.postMessage({ id, op: 'open', url, binaryType: api.binaryType });
    return api;
  }

  function esc(s) {
    const d = document.createElement('div');
    d.textContent = s == null ? '' : String(s);
    return d.innerHTML;
  }

  function attr(s) {
    return esc(s).replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  document.addEventListener('click', event => {
    const btn = event.target.closest && event.target.closest('[data-theme-toggle]');
    if (btn) nextTheme();
    if (!event.target.closest || !event.target.closest('.rdev-select')) closeOpenSelects();
  });
  window.matchMedia('(prefers-color-scheme: light)').addEventListener('change', () => applyTheme());
  document.addEventListener('DOMContentLoaded', () => applyTheme());
  injectIconStyles();
  window.addEventListener('rdev:langchange', () => applyTheme());

  window.RDevUI = { icon, themeButton, applyTheme, enhanceSelect, setSelectOptions, prompt, alert, socket };
  window.icon = icon;
})();
