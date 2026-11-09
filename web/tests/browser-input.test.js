import { JSDOM } from "jsdom";
import { expect, it, vi } from "vitest";
import { bindRemoteInput } from "../../internal/server/browser_input.js";

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
