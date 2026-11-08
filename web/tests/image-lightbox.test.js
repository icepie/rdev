// @vitest-environment jsdom
import { act, createElement } from 'react';
import { createRoot } from 'react-dom/client';
import { expect, it } from 'vitest';
import { ImagePreview } from '../ui/components/agent/image-preview';

it('navigates a gallery, zooms and resets, then restores trigger focus without leaving global wheel interception', async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement('div');
  document.body.append(host);
  const root = createRoot(host);
  const gallery = [{ src: 'data:image/png;base64,YQ==', name: 'first' }, { src: 'data:image/png;base64,Yg==', name: 'second' }];
  const click = async element => act(async () => element.click());
  const key = async (name, modifiers = {}) => act(async () => document.dispatchEvent(new KeyboardEvent('keydown', { key: name, bubbles: true, cancelable: true, ...modifiers })));
  const dialog = () => document.querySelector('[role=dialog]');
  try {
    await act(async () => root.render(createElement(ImagePreview, { ...gallery[0], gallery })));
    const trigger = host.querySelector('button');
    trigger.focus();
    await click(trigger);
    expect(dialog().parentElement).toBe(document.body);
    expect(dialog().querySelector('img').alt).toBe('first');
    await key('ArrowRight', { ctrlKey: true });
    expect(dialog().querySelector('img').alt).toBe('first');
    await key('ArrowRight');
    expect(dialog().querySelector('img').alt).toBe('second');
    await key('ArrowRight');
    expect(dialog().querySelector('img').alt).toBe('first');
    const wheel = new WheelEvent('wheel', { deltaY: -100, cancelable: true });
    await act(async () => window.dispatchEvent(wheel));
    expect(wheel.defaultPrevented).toBe(true);
    expect(dialog().querySelector('img').style.transform).toContain('scale(1.22)');
    await click(dialog().querySelector('img'));
    expect(dialog()).not.toBeNull();
    await click(dialog().querySelector('[aria-label="agent.imageFit"]'));
    expect(dialog().querySelector('img').style.transform).toContain('scale(1)');
    await key('Escape');
    expect(dialog()).toBeNull();
    await act(async () => new Promise(resolve => setTimeout(resolve, 0))); // Modal focus restoration follows unmount.
    expect(document.activeElement).toBe(trigger);
    const after = new WheelEvent('wheel', { deltaY: -100, cancelable: true });
    window.dispatchEvent(after);
    expect(after.defaultPrevented).toBe(false);
    await click(trigger);
    await click(dialog().querySelector('img'));
    expect(dialog()).toBeNull();
    await act(async () => new Promise(resolve => setTimeout(resolve, 0))); // Modal focus restoration follows unmount.
    expect(document.activeElement).toBe(trigger);
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});

it('recovers decode errors without closing the gallery and isolates failures between images', async () => {
 globalThis.IS_REACT_ACT_ENVIRONMENT=true;
 const host=document.createElement('div');document.body.append(host);
 const root=createRoot(host);
 const gallery=[{src:'data:image/png;base64,YQ==',name:'first'},{src:'data:image/png;base64,Yg==',name:'second'}];
 const dialog=()=>document.querySelector('[role=dialog]');
 try {
  await act(async()=>root.render(createElement(ImagePreview,{...gallery[0],gallery})));
  await act(async()=>host.querySelector('button').click());
  const first=dialog().querySelector('img');
  await act(async()=>first.dispatchEvent(new Event('error')));
  expect(dialog().textContent).toContain('agent.imageLoadFailed');
  expect(first.hidden).toBe(true);
  const wheel=new WheelEvent('wheel',{deltaY:-100,cancelable:true});
  window.dispatchEvent(wheel);
  expect(wheel.defaultPrevented).toBe(false);
  const retry=[...dialog().querySelectorAll('button')].find(button=>button.textContent.includes('agent.imageRetry'));
  await act(async()=>retry.click());
  expect(dialog()).not.toBeNull();
  expect(dialog().querySelector('img')).not.toBe(first);
  expect(dialog().querySelector('img').hidden).toBe(false);
  await act(async()=>dialog().querySelector('img').dispatchEvent(new Event('error')));
  await act(async()=>document.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowRight',bubbles:true,cancelable:true})));
  expect(dialog().querySelector('img').alt).toBe('second');
  expect(dialog().textContent).not.toContain('agent.imageLoadFailed');
  expect(dialog().querySelector('img').hidden).toBe(false);
 } finally {
  await act(async()=>root.unmount());host.remove();delete globalThis.IS_REACT_ACT_ENVIRONMENT;
 }
});
