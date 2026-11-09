import { JSDOM } from "jsdom";
import { expect, it, vi } from "vitest";
import { bindRemoteInput, installBrowserInput } from "../../internal/server/browser_input.js";

function setup() {
 const dom = new JSDOM('<textarea></textarea>');
 const host = dom.window.document.querySelector('textarea');
 const send = vi.fn(), key = vi.fn();
 const input = bindRemoteInput(host, { send, key });
 const emit = (name, data) => host.dispatchEvent(new dom.window.CompositionEvent(name, { bubbles:true, data }));
 return { dom, host, send, key, input, emit, close() { input.dispose(); dom.window.close(); } };
}
it('mirrors composition and commits Unicode once without forwarding candidate confirmation', () => {
 const f=setup();
 try {
  f.emit('compositionstart',''); f.emit('compositionupdate','ni'); f.emit('compositionupdate','你好🌏');
  f.host.dispatchEvent(new f.dom.window.KeyboardEvent('keydown',{key:'Enter',code:'Enter',keyCode:229,isComposing:true}));
  f.emit('compositionend','你好🌏');
  f.host.dispatchEvent(new f.dom.window.InputEvent('input',{data:'你好🌏',inputType:'insertFromComposition'}));
  f.host.dispatchEvent(new f.dom.window.KeyboardEvent('keyup',{key:'Enter',code:'Enter'}));
  expect(f.key).not.toHaveBeenCalled();
  expect(f.send.mock.calls).toEqual([
   ['imeSetComposition',{text:'ni',selectionStart:2,selectionEnd:2}],
   ['imeSetComposition',{text:'你好🌏',selectionStart:4,selectionEnd:4}],
   ['insertText',{text:'你好🌏'}],
  ]);
  expect(f.host.value).toBe('');
 } finally { f.close(); }
});
it('cancels composition and does not insert the unconfirmed candidate',()=>{
 const f=setup();try {
  f.emit('compositionstart','');f.emit('compositionupdate','候选');f.emit('compositionend','');
  expect(f.send).toHaveBeenLastCalledWith('imeSetComposition',{text:'',selectionStart:0,selectionEnd:0});
  expect(f.send.mock.calls.some(([method])=>method==='insertText')).toBe(false);
 } finally {f.close();}
});
it('finishes composition before a pointer focus change without a second commit on blur',()=>{
 const f=setup();try {
  f.emit('compositionstart','');f.emit('compositionupdate','完成');f.input.finish();
  f.emit('compositionend','完成');f.host.dispatchEvent(new f.dom.window.FocusEvent('blur'));
  expect(f.send.mock.calls.filter(([method])=>method==='insertText')).toEqual([['insertText',{text:'完成'}]]);
 } finally {f.close();}
});
it('keeps normal typing, navigation and mobile deletion separate from composition',()=>{
 const f=setup();try {
  f.host.dispatchEvent(new f.dom.window.KeyboardEvent('keydown',{key:'a',code:'KeyA'}));
  f.host.dispatchEvent(new f.dom.window.InputEvent('input',{data:'a',inputType:'insertText'}));
  f.host.dispatchEvent(new f.dom.window.KeyboardEvent('keyup',{key:'a',code:'KeyA'}));
  expect(f.send).toHaveBeenCalledExactlyOnceWith('insertText',{text:'a'});
  expect(f.key.mock.calls.map(([,type])=>type)).toEqual(['rawKeyDown','keyUp']);
  const altGraph=new f.dom.window.KeyboardEvent('keydown',{key:'@',code:'KeyQ',ctrlKey:true,altKey:true,cancelable:true});
  Object.defineProperty(altGraph,'getModifierState',{value:name=>name==='AltGraph'});
  f.host.dispatchEvent(altGraph);
  expect(altGraph.defaultPrevented).toBe(false);
  expect(f.key.mock.calls.at(-1)[1]).toBe('rawKeyDown');
  const backspace=new f.dom.window.InputEvent('beforeinput',{inputType:'deleteContentBackward',cancelable:true});
  f.host.dispatchEvent(backspace);
  expect(backspace.defaultPrevented).toBe(true);
  expect(f.key.mock.calls.slice(-2).map(([event,type])=>[event.key,type])).toEqual([['Backspace','keyDown'],['Backspace','keyUp']]);
 } finally {f.close();}
});
it('pastes Unicode from the local clipboard and suppresses remote clipboard shortcuts',()=>{
 const f=setup();try {
  f.host.dispatchEvent(new f.dom.window.KeyboardEvent('keydown',{key:'v',code:'KeyV',ctrlKey:true}));
  const paste=new f.dom.window.Event('paste',{bubbles:true,cancelable:true});
  Object.defineProperty(paste,'clipboardData',{value:{getData:()=> '粘贴\n🌏'}});
  f.host.dispatchEvent(paste);
  f.host.dispatchEvent(new f.dom.window.KeyboardEvent('keyup',{key:'v',code:'KeyV',ctrlKey:true}));
  expect(f.key).not.toHaveBeenCalled();
  expect(paste.defaultPrevented).toBe(true);
  expect(f.send).toHaveBeenCalledExactlyOnceWith('insertText',{text:'粘贴\n🌏'});
 } finally {f.close();}
});

it('does not send a synthetic origin release when handing canvas focus to the IME host', async () => {
 const dom = new JSDOM('<div id="surface"><canvas tabindex="0"></canvas></div>', { url: 'https://rdev.test' });
 const { document } = dom.window;
 for (const name of ['window', 'document', 'location', 'HTMLCanvasElement', 'innerWidth', 'innerHeight']) vi.stubGlobal(name, dom.window[name]);
 const canvas = document.querySelector('canvas');
 const emit = vi.fn();
 const model = { emitMouseEvent: emit };
 const app = { screencastView: { contentElement: document.querySelector('#surface'), inputModel: model } };
 const dispose = installBrowserInput(app, vi.fn());
 canvas.addEventListener('mousedown', event => { model.emitMouseEvent(event, 0, 1); canvas.focus(); });
 canvas.addEventListener('mouseup', event => model.emitMouseEvent(event, 0, 1));
 canvas.addEventListener('blur', () => model.emitMouseEvent(new dom.window.MouseEvent('mouseup'), 0, 1));
 try {
  canvas.dispatchEvent(new dom.window.MouseEvent('mousedown', { bubbles:true, clientX:120, clientY:80, detail:1 }));
  canvas.dispatchEvent(new dom.window.MouseEvent('mouseup', { bubbles:true, clientX:120, clientY:80, detail:1 }));
  await new Promise(resolve => setTimeout(resolve, 0));
  expect(document.activeElement.tagName).toBe('TEXTAREA');
  expect(emit.mock.calls.map(([event]) => [event.type, event.offsetX, event.offsetY])).toEqual([['mousedown',120,80],['mouseup',120,80]]);
  // A genuine subsequent release must still be forwarded.
  model.emitMouseEvent(new dom.window.MouseEvent('mouseup', {clientX:42,clientY:24}), 0, 1);
  await new Promise(resolve => setTimeout(resolve, 0));
  expect(emit).toHaveBeenCalledTimes(3);
 } finally { dispose(); vi.unstubAllGlobals(); dom.window.close(); }
});
