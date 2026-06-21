import { JSDOM } from 'jsdom';
import { readFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { afterEach, describe, expect, it, vi } from 'vitest';

const root = resolve(import.meta.dirname, '../..');
const publicDir = resolve(root, 'web/public');

async function loadScript(window, name) {
  const source = await readFile(resolve(publicDir, name), 'utf8');
  window.eval(source);
}

async function setupDom(html = '<!doctype html><html><head></head><body></body></html>') {
  const dom = new JSDOM(html, {
    url: 'https://rdev.test/',
    runScripts: 'outside-only',
    pretendToBeVisual: true
  });
  Object.defineProperty(dom.window, 'matchMedia', {
    configurable: true,
    value: vi.fn(() => ({
      matches: false,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn()
    }))
  });
  await loadScript(dom.window, 'ui.js');
  await loadScript(dom.window, 'i18n.js');
  return dom;
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('RDevI18n', () => {
  it('keeps English and Chinese dictionaries aligned', async () => {
    const source = await readFile(resolve(publicDir, 'i18n.js'), 'utf8');
    const keyMatcher = /^\s+'([^']+)':/gm;
    const enBlock = source.slice(source.indexOf('en: {'), source.indexOf('zh: {'));
    const zhBlock = source.slice(source.indexOf('zh: {'), source.indexOf('\n    }\n  };', source.indexOf('zh: {')));
    const enKeys = [...enBlock.matchAll(keyMatcher)].map(match => match[1]).sort();
    const zhKeys = [...zhBlock.matchAll(keyMatcher)].map(match => match[1]).sort();

    expect(zhKeys).toEqual(enKeys);
  });

  it('applies translated text, placeholders, titles, and language changes', async () => {
    const dom = await setupDom(`
      <!doctype html><html><head></head><body>
        <button data-i18n="common.copy"></button>
        <input data-i18n-placeholder="files.path">
        <span data-i18n-title="theme.toggle"></span>
      </body></html>
    `);
    const { document, RDevI18n, localStorage } = dom.window;
    const langEvents = [];
    dom.window.addEventListener('rdev:langchange', event => langEvents.push(event.detail.lang));

    RDevI18n.setLang('zh');
    expect(document.querySelector('button').textContent).toBe('复制');
    expect(document.querySelector('input').placeholder).toBe('路径');
    expect(document.querySelector('span').title).toBe('主题');
    expect(document.documentElement.lang).toBe('zh-CN');
    expect(localStorage.getItem('rdevLang')).toBe('zh');

    RDevI18n.setLang('en');
    expect(document.querySelector('button').textContent).toBe('Copy');
    expect(document.querySelector('input').placeholder).toBe('Path');
    expect(document.documentElement.lang).toBe('en');
    expect(langEvents).toEqual(['zh', 'en']);
  });

  it('keeps built-in static image bridge in sync with the web source', async () => {
    const source = await readFile(resolve(root, 'web/public/terminal-images.js'), 'utf8');
    const embedded = await readFile(resolve(root, 'internal/server/static/terminal-images.js'), 'utf8');

    expect(embedded).toBe(source);
  });
});

describe('RDevUI', () => {
  it('renders SVG icons without emoji fallback text', async () => {
    const dom = await setupDom();
    const html = dom.window.RDevUI.icon('files', 'Files');

    expect(html).toContain('<svg');
    expect(html).toContain('icon-files');
    expect(html).toContain('<span>Files</span>');
    expect(html).not.toMatch(/[\u{1F300}-\u{1FAFF}]/u);
  });

  it('cycles and renders theme state from localStorage', async () => {
    const dom = await setupDom('<!doctype html><html><head></head><body><button data-theme-toggle></button></body></html>');
    const { document, localStorage, RDevUI } = dom.window;

    localStorage.setItem('rdevTheme', 'light');
    RDevUI.applyTheme();
    expect(document.documentElement.dataset.theme).toBe('light');
    expect(document.documentElement.dataset.themeChoice).toBe('light');
    expect(document.querySelector('[data-theme-toggle]').textContent).toContain('Light');

    document.querySelector('[data-theme-toggle]').click();
    expect(localStorage.getItem('rdevTheme')).toBe('dark');
    expect(document.documentElement.dataset.theme).toBe('dark');
  });

  it('implements custom select value changes and close behavior', async () => {
    const dom = await setupDom('<!doctype html><html><head></head><body><div id="select"></div><button id="outside"></button></body></html>');
    const { document, RDevUI } = dom.window;
    const el = document.getElementById('select');
    const changes = [];
    el.addEventListener('change', () => changes.push(el.value));

    RDevUI.setSelectOptions(el, [
      { value: 'a', label: 'Alpha' },
      { value: 'b', label: 'Beta' },
      { value: 'c', label: 'Gamma', disabled: true }
    ], 'a');

    expect(el.value).toBe('a');
    el.querySelector('.rdev-select-trigger').click();
    expect(el.classList.contains('open')).toBe(true);
    el.querySelector('[data-value="b"]').click();
    expect(el.value).toBe('b');
    expect(changes).toEqual(['b']);

    el.querySelector('.rdev-select-trigger').click();
    document.getElementById('outside').click();
    expect(el.classList.contains('open')).toBe(false);
  });

  it('uses custom modals instead of browser prompt/alert/confirm', async () => {
    const dom = await setupDom();
    const { document, RDevUI } = dom.window;
    const promptSpy = vi.spyOn(dom.window, 'prompt');
    const alertSpy = vi.spyOn(dom.window, 'alert');
    const confirmSpy = vi.spyOn(dom.window, 'confirm');

    const promptPromise = RDevUI.prompt({ title: 'Password', message: 'Enter password', type: 'password' });
    const input = document.querySelector('.rdev-modal-input');
    input.value = 'secret';
    document.querySelector('[data-modal-ok]').click();
    await expect(promptPromise).resolves.toBe('secret');

    const alertPromise = RDevUI.alert({ title: 'Error', message: 'Nope' });
    document.querySelector('[data-modal-ok]').click();
    await expect(alertPromise).resolves.toBeUndefined();

    expect(promptSpy).not.toHaveBeenCalled();
    expect(alertSpy).not.toHaveBeenCalled();
    expect(confirmSpy).not.toHaveBeenCalled();
  });

  it('stores device passwords per device and can forget them', async () => {
    const dom = await setupDom();
    const { RDevUI } = dom.window;

    RDevUI.rememberDevicePassword('device/a', 'one');
    RDevUI.rememberDevicePassword('device b', 'two');
    expect(RDevUI.getDevicePassword('device/a')).toBe('one');
    expect(RDevUI.getDevicePassword('device b')).toBe('two');

    RDevUI.forgetDevicePassword('device/a');
    expect(RDevUI.getDevicePassword('device/a')).toBe('');
    expect(RDevUI.getDevicePassword('device b')).toBe('two');
  });

  it('uses worker-backed sockets when available and native WebSocket fallback otherwise', async () => {
    const workerDom = await setupDom();
    const workerMessages = [];
    class FakeWorker {
      constructor(url) {
        this.url = url;
      }
      postMessage(message) {
        workerMessages.push(message);
      }
    }
    workerDom.window.Worker = FakeWorker;
    const workerSocket = workerDom.window.RDevUI.socket('wss://rdev.test/files');
    expect(workerMessages[0]).toMatchObject({ id: workerSocket.id, op: 'open', url: 'wss://rdev.test/files' });
    workerSocket.send('ping');
    expect(workerMessages[1]).toMatchObject({ id: workerSocket.id, op: 'send', data: 'ping' });

    const nativeDom = await setupDom();
    const opened = [];
    class FakeWebSocket {
      constructor(url) {
        this.url = url;
        opened.push(url);
      }
    }
    delete nativeDom.window.Worker;
    nativeDom.window.WebSocket = FakeWebSocket;
    const nativeSocket = nativeDom.window.RDevUI.socket('wss://rdev.test/terminal');
    expect(nativeSocket).toBeInstanceOf(FakeWebSocket);
    expect(opened).toEqual(['wss://rdev.test/terminal']);
  });
});

describe('terminal image wiring', () => {
  it('loads SIXEL, iTerm2, and Kitty image support on terminal pages', async () => {
    const terminal = await readFile(resolve(root, 'web/terminal.html'), 'utf8');
    const sessions = await readFile(resolve(root, 'web/sessions.html'), 'utf8');
    const staticTerminal = await readFile(resolve(root, 'internal/server/static/terminal.html'), 'utf8');
    const staticSessions = await readFile(resolve(root, 'internal/server/static/sessions.html'), 'utf8');

    for (const html of [terminal, sessions, staticTerminal, staticSessions]) {
      expect(html).toContain('/vendor/addon-image.js');
      expect(html).toContain('/terminal-images.js');
      expect(html).toContain('sixelSupport: true');
      expect(html).toContain('iipSupport: true');
      expect(html).toContain('RDevTerminalImages.create(term)');
      expect(html).toMatch(/imageWriter\.write\((?:evt\.data|data)\)|enqueueAttachOutput\(evt\.data\)/);
    }
  });
});
