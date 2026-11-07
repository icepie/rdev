import { JSDOM } from 'jsdom';
import { act, createElement, Profiler } from 'react';
import { createRoot } from 'react-dom/client';
import { beforeEach, afterEach, expect, it, vi } from 'vitest';
vi.mock('../ui/pages/Terminal', () => ({ TabAuthOverlay: () => null }));
vi.mock('../ui/components/icon', () => ({ Icon: () => null }));
vi.mock('../ui/lib/api', () => ({ wsURL: () => 'ws://fixture/files', getDevicePassword: () => '', rememberDevicePassword() {}, forgetDevicePassword() {} }));
import { encodeFrame } from '../ui/lib/file-transfer-frame';
import { PanelFiles } from '../ui/components/agent/panel-files';

let dom, root, sockets, commits;
beforeEach(async () => {
  dom = new JSDOM('<main></main>', { url:'http://localhost', pretendToBeVisual:true });
  sockets = [];
  commits = [];
  for (const [key, value] of Object.entries({window:dom.window,document:dom.window.document,localStorage:dom.window.localStorage,IS_REACT_ACT_ENVIRONMENT:true})) vi.stubGlobal(key,value);
  vi.stubGlobal('WebSocket', class {
    static OPEN = 1;
    readyState = 1;
    sent = [];
    constructor() { sockets.push(this); }
    send(raw) { this.sent.push(JSON.parse(raw)); }
    close() {}
  });
  vi.useFakeTimers();
  root=createRoot(document.querySelector('main'));
  await render('one');
  await act(async () => sockets[0].onopen());
});
afterEach(async () => { await act(async()=>root.unmount()); dom.window.close(); vi.unstubAllGlobals(); vi.useRealTimers(); });
const render = (device, visible = false) => act(async()=>root.render(createElement(Profiler,{id:'files',onRender:()=>commits.push(1)},createElement(PanelFiles,{device,visible,t:key=>key,onOpenFile(){}}))));
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

for (const visible of [false, true]) {
  it(`coalesces download progress with visible=${visible} and does not render idle ticks`, async()=>{
    await render('one', visible);
    const socket=sockets[0];
    await deliver(socket,reply(socket.sent[0],'large.bin'));
    await act(async()=>document.querySelector('[aria-label="files.download large.bin"]').click());
    const request=socket.sent.at(-1);
    await deliver(socket,{op:'download_start',taskId:request.taskId,size:1000});
    const writes=vi.spyOn(dom.window.Storage.prototype,'setItem');
    commits.length=0;
    await act(async()=>{
      for(let offset=0;offset<200;offset++) socket.onmessage({data:encodeFrame(0x22,request.taskId,offset,new Uint8Array([1]).buffer)});
    });
    expect(writes).not.toHaveBeenCalled();
    expect(commits).toHaveLength(0);
    await act(async()=>{ vi.advanceTimersByTime(500); });
    expect(writes).toHaveBeenCalledTimes(1);
    expect(commits.length).toBe(visible ? 1 : 0);
    const saved=JSON.parse(localStorage.getItem('rdevFileTasks.one'));
    expect(saved[0].done).toBe(200);
    await act(async()=>{ vi.advanceTimersByTime(1000); });
    expect(writes).toHaveBeenCalledTimes(1);
    expect(commits.length).toBe(visible ? 1 : 0);
    await deliver(socket,{op:'canceled',taskId:request.taskId});
    writes.mockClear(); commits.length=0;
    await act(async()=>{
      socket.onmessage({data:encodeFrame(0x22,request.taskId,200,new Uint8Array([2]).buffer)});
      vi.advanceTimersByTime(500);
    });
    expect(writes).not.toHaveBeenCalled();
    expect(commits).toHaveLength(0);
    writes.mockRestore();
  });
}

it('flushes unsampled progress to the original device when changing devices', async()=>{
  const socket=sockets[0];
  await deliver(socket,reply(socket.sent[0],'pending.bin'));
  await act(async()=>document.querySelector('[aria-label="files.download pending.bin"]').click());
  const request=socket.sent.at(-1);
  await deliver(socket,{op:'download_start',taskId:request.taskId,size:1000});
  await act(async()=>socket.onmessage({data:encodeFrame(0x22,request.taskId,0,new Uint8Array([1,2,3]).buffer)}));
  await render('two');
  expect(JSON.parse(localStorage.getItem('rdevFileTasks.one'))[0].done).toBe(3);
  expect(localStorage.getItem('rdevFileTasks.two')).toBeNull();
});
