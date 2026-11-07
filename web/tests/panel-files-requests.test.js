import { JSDOM } from 'jsdom';
import { act, createElement } from 'react';
import { createRoot } from 'react-dom/client';
import { beforeEach, afterEach, expect, it, vi } from 'vitest';
vi.mock('../ui/pages/Terminal', () => ({ TabAuthOverlay: () => null }));
vi.mock('../ui/components/icon', () => ({ Icon: () => null }));
vi.mock('../ui/lib/api', () => ({ wsURL: () => 'ws://fixture/files', getDevicePassword: () => '', rememberDevicePassword() {}, forgetDevicePassword() {} }));
import { PanelFiles } from '../ui/components/agent/panel-files';

let dom, root, sockets;
beforeEach(async () => {
  dom = new JSDOM('<main></main>', { url:'http://localhost', pretendToBeVisual:true });
  sockets = [];
  for (const [key, value] of Object.entries({window:dom.window,document:dom.window.document,localStorage:dom.window.localStorage,IS_REACT_ACT_ENVIRONMENT:true})) vi.stubGlobal(key,value);
  vi.stubGlobal('WebSocket', class {
    static OPEN = 1;
    readyState = 1;
    sent = [];
    constructor() { sockets.push(this); }
    send(raw) { this.sent.push(JSON.parse(raw)); }
    close() {}
  });
  root=createRoot(document.querySelector('main'));
  await render('one');
  await act(async () => sockets[0].onopen());
});
afterEach(async () => { await act(async()=>root.unmount()); dom.window.close(); vi.unstubAllGlobals(); });
const render = device => act(async()=>root.render(createElement(PanelFiles,{device,visible:false,t:key=>key,onOpenFile(){}})));
const deliver = (socket, message) => act(async()=>socket.onmessage({data:JSON.stringify(message)}));
const reply = (request, name, extra={}) => ({op:'list_result',requestId:request.requestId,path:'/tmp',entries:[{name,path:'/tmp/'+name,isDir:false,size:1}],total:1,offset:0,...extra});

it('keeps the latest directory after older success/error and duplicate replies', async()=>{
  const socket=sockets[0], old=socket.sent.find(x=>x.op==='list');
  await act(async()=>document.querySelector('[aria-label="files.refresh"]').click());
  const latest=socket.sent.at(-1);
  expect(latest.requestId).not.toBe(old.requestId);
  await deliver(socket,reply(latest,'current.txt'));
  await deliver(socket,reply(old,'stale.txt'));
  await deliver(socket,{op:'error',requestId:old.requestId,message:'old failure'});
  await deliver(socket,reply(latest,'duplicate.txt'));
  expect(document.body.textContent).toContain('current.txt');
  expect(document.body.textContent).not.toMatch(/stale.txt|duplicate.txt|old failure/);
});

it('rejects a legacy first page returned for a load-more request', async()=>{
  const socket=sockets[0];
  await deliver(socket,reply(socket.sent[0],'first.txt',{total:2,truncated:true}));
  const more=[...document.querySelectorAll('button')].find(e=>e.textContent==='files.loadMore');
  await act(async()=>more.click());
  const request=socket.sent.at(-1);
  expect(request.offset).toBe(1);
  await deliver(socket,reply(request,'first.txt',{total:2,truncated:true,offset:0}));
  expect(document.body.textContent).toContain('files.paginationFailed');
});

it('ignores old socket results after a device switch and malformed binary frames', async()=>{
  const old=sockets[0], request=old.sent[0];
  await render('two');
  const current=sockets.at(-1);
  await act(async()=>current.onopen());
  await deliver(current,reply(current.sent[0],'device-two.txt'));
  await deliver(old,reply(request,'device-one.txt'));
  await act(async()=>current.onmessage({data:new ArrayBuffer(1)}));
  expect(document.body.textContent).toContain('device-two.txt');
  expect(document.body.textContent).not.toContain('device-one.txt');
});
