// @vitest-environment jsdom
import { act, createElement, useRef } from 'react';
import { createRoot } from 'react-dom/client';
import { expect, it, vi } from 'vitest';
import { revealFileLine, useFileLineReveal } from '../ui/components/agent/file-line-reveal';

it('reveals renderer shadow rows once per target, waits for visibility, and leaves outer scroll alone', async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers();
  const outer = document.createElement('div');
  outer.scrollTop = 77;
  document.body.append(outer);
  const root = createRoot(outer);
  let onPostRender;
  function Harness(props) {
    const container = useRef(null);
    onPostRender = useFileLineReveal(container, props.range, props.enabled);
    return createElement('div',{ref:container,'data-pane':true},createElement('div',{'data-code-host':true}));
  }
  const props = {range:{start:100,end:102},enabled:true};
  const render = () => act(async () => root.render(createElement(Harness,props)));
  const tick = () => act(async () => vi.advanceTimersByTimeAsync(20));
  try {
    await render();
    const pane = outer.querySelector('[data-pane]'), host = pane.firstElementChild;
    Object.defineProperty(pane,'clientHeight',{configurable:true,value:400});
    Object.defineProperty(pane,'clientTop',{configurable:true,value:1});
    pane.getBoundingClientRect = () => ({top:100});
    const shadow = host.attachShadow({mode:'open'});
    for (const line of [100,200]) {
      const row = document.createElement('div');
      row.dataset.line = String(line);
      row.getBoundingClientRect = () => ({top:100+line*20-pane.scrollTop,height:20});
      shadow.append(row);
    }
    await act(async () => onPostRender(host,null,'mount'));
    await tick();
    expect(pane.scrollTop).toBe(1809);
    expect(outer.scrollTop).toBe(77);
    pane.scrollTop = 123;
    await act(async () => onPostRender(host,null,'update')); // syntax highlighting/theme update
    await tick();
    expect(pane.scrollTop).toBe(123);
    props.enabled = false;
    props.range = {start:200,end:200};
    await render();
    await act(async () => onPostRender(host,null,'update'));
    await tick();
    expect(pane.scrollTop).toBe(123);
    props.enabled = true;
    await render();
    await tick();
    expect(pane.scrollTop).toBe(3809);
    for (const line of [0,-1,1.5,NaN,500]) {
      expect(revealFileLine(pane,host,line)).toBe(false);
      expect(pane.scrollTop).toBe(3809);
    }
    const unrelated = document.createElement('div');
    document.body.append(unrelated);
    expect(revealFileLine(unrelated,host,100)).toBe(false);
    unrelated.remove();
    props.range = null;
    await render();
    props.range = {start:100,end:100};
    Object.defineProperty(pane,'clientHeight',{configurable:true,value:0});
    await render();
    await tick();
    expect(pane.scrollTop).toBe(3809);
    Object.defineProperty(pane,'clientHeight',{configurable:true,value:400});
    await act(async () => onPostRender(host,null,'update'));
    await tick();
    expect(pane.scrollTop).toBe(1809);
    props.range = {start:200,end:200};
    await render();
    expect(vi.getTimerCount()).toBe(1);
  } finally {
    await act(async () => root.unmount());
    expect(vi.getTimerCount()).toBe(0);
    outer.remove();
    vi.useRealTimers();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
