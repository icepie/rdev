// @vitest-environment jsdom
import { act, createElement, useEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { expect, it, vi } from 'vitest';
vi.mock('../ui/components/icon', () => ({ Icon: () => null }));
import { FilesPaneLayout } from '../ui/components/files-pane-layout';

it('uses available width for file tabs and retains both pane instances and state through folding and resizing', async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  let width = 390, notify;
  const disconnect = vi.fn();
  vi.stubGlobal('ResizeObserver', class {
    constructor(callback) { notify = callback; }
    observe(element) { Object.defineProperty(element, 'clientWidth', { configurable: true, get: () => width }); notify(); }
    disconnect = disconnect;
  });
  const host = document.createElement('div'); document.body.append(host);
  const root = createRoot(host), mounts = vi.fn();
  function Pane({ id }) {
    const [count, setCount] = useState(0);
    useEffect(() => { mounts(id); }, []);
    return createElement('button', { onClick: () => setCount(count + 1) }, `pane-${id}:${count}`);
  }
  function App() {
    const [focusedPane, onFocus] = useState(0), [secondOpen, setOpen] = useState(true);
    return createElement(FilesPaneLayout, {
      focusedPane, onFocus, secondMounted: true, secondOpen,
      onToggleSecond: () => { setOpen(!secondOpen); onFocus(secondOpen ? 0 : 1); },
      dragMime: 'application/x-rdev-pane-file', label: 'Files', paneLabel: id => `Pane ${id + 1}`, toggleLabel: 'Toggle',
      renderPane: id => createElement(Pane, { id }),
    });
  }
  const resize = value => act(async () => { width = value; notify(); });
  const click = el => act(async () => el.click());
  try {
    await act(async () => root.render(createElement(App)));
    const panes = [...host.querySelectorAll('section')];
    expect(panes[0].hidden).toBe(false);
    expect(panes[1].hasAttribute('inert')).toBe(true);
    await click(panes[0].querySelector('button'));
    let tabs = host.querySelectorAll('[role=tab]');
    expect(document.getElementById(tabs[1].getAttribute('aria-controls'))).toBe(panes[1]);
    await act(async () => tabs[0].dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true })));
    expect(document.activeElement).toBe(tabs[1]);
    expect(panes[0].hidden).toBe(true);
    expect(panes[1].hidden).toBe(false);
    await click(panes[1].querySelector('button'));
    await resize(1100);
    expect(host.querySelector('[role=tablist]')).toBeNull();
    expect(panes.every(p => !p.hidden && !p.hasAttribute('inert'))).toBe(true);
    await resize(700);
    expect(panes[0].hidden).toBe(true);
    const toggle = [...host.querySelectorAll('button')].find(b => b.textContent === 'Toggle');
    await click(toggle);
    expect(panes[1].hidden).toBe(true);
    expect(panes[0].hidden).toBe(false);
    await click(toggle);
    expect(panes[1].hidden).toBe(false);
    expect(panes.map(p => p.textContent)).toEqual(['pane-0:1', 'pane-1:1']);
    const drag = new Event('dragenter', { bubbles: true });
    Object.defineProperty(drag, 'dataTransfer', { value: { types: ['application/x-rdev-pane-file'] } });
    await act(async () => host.querySelector('[role=tab]').dispatchEvent(drag));
    expect(panes[0].hidden).toBe(false);
    expect(panes[1].hidden).toBe(true);
    expect(mounts).toHaveBeenCalledTimes(2);
  } finally {
    await act(async () => root.unmount());
    expect(disconnect).toHaveBeenCalledOnce();
    host.remove(); vi.unstubAllGlobals(); delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
