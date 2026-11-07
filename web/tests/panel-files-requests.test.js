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
    send(raw) { this.sent.push(typeof raw === "string" ? JSON.parse(raw) : raw); }
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

async function pickFile(name='upload.txt') {
  const input=document.querySelector('input[type=file]');
  Object.defineProperty(input,'files',{configurable:true,value:[new File(['abc'],name,{lastModified:1000})]});
  await act(async()=>input.dispatchEvent(new window.Event('change',{bubbles:true})));
}

it('settles an uploaded file, refreshes the directory and ignores late old-attempt errors', async()=>{
  const socket=sockets[0];
  await deliver(socket,reply(socket.sent[0],'existing.txt'));
  await pickFile();
  const start=socket.sent.at(-1);
  expect(start.op).toBe('upload_start');
  await deliver(socket,{op:'upload_ready',taskId:start.taskId,path:'/tmp/upload.txt',offset:0});
  expect(socket.sent.at(-1)).toBeInstanceOf(ArrayBuffer);
  await act(async()=>socket.onmessage({data:encodeFrame(0x21,start.taskId,3)}));
  expect(socket.sent.at(-1)).toMatchObject({op:'upload_end',taskId:start.taskId});
  await deliver(socket,{op:'transfer_end',taskId:start.taskId});
  expect(JSON.parse(localStorage.getItem('rdevFileTasks.one'))[0]).toMatchObject({status:'completed',done:3});
  expect(socket.sent.at(-1).op).toBe('list');
  await deliver(socket,{op:'transfer_error',taskId:start.taskId,message:'late old failure'});
  expect(JSON.parse(localStorage.getItem('rdevFileTasks.one'))[0].status).toBe('completed');
});

it('cancels the old upload wait and isolates acknowledgments after resume', async()=>{
  const socket=sockets[0];
  await pickFile();
  const first=socket.sent.at(-1);
  await deliver(socket,{op:'upload_ready',taskId:first.taskId,path:'/tmp/upload.txt',offset:0});
  await act(async()=>document.querySelector('[aria-label="files.pause upload.txt"]').click());
  expect(socket.sent.at(-1)).toMatchObject({op:'cancel',taskId:first.taskId});
  await act(async()=>document.querySelector('[aria-label="files.resume upload.txt"]').click());
  const second=socket.sent.at(-1);
  expect(second.taskId).not.toBe(first.taskId);
  await deliver(socket,{op:'upload_ready',taskId:second.taskId,path:'/tmp/upload.txt',offset:0});
  const sent=socket.sent.length;
  await act(async()=>socket.onmessage({data:encodeFrame(0x21,first.taskId,3)}));
  await deliver(socket,{op:'canceled',taskId:first.taskId});
  expect(socket.sent).toHaveLength(sent);
  expect(JSON.parse(localStorage.getItem('rdevFileTasks.one'))[0].status).toBe('running');
  await act(async()=>socket.onmessage({data:encodeFrame(0x21,second.taskId,3)}));
  expect(socket.sent.at(-1)).toMatchObject({op:'upload_end',taskId:second.taskId});
  expect(socket.sent.filter(x=>x.op==='upload_end')).toHaveLength(1);
});

it('resumes a running upload with a new attempt after socket reconnect', async()=>{
  const old=sockets[0];
  await pickFile();
  const first=old.sent.at(-1);
  await deliver(old,{op:'upload_ready',taskId:first.taskId,path:'/tmp/upload.txt',offset:0});
  await act(async()=>{ old.onclose(); vi.advanceTimersByTime(2000); });
  const current=sockets.at(-1);
  expect(current).not.toBe(old);
  await act(async()=>current.onopen());
  const next=current.sent.find(x=>x.op==='upload_start');
  expect(next.taskId).not.toBe(first.taskId);
  await deliver(current,{op:'upload_ready',taskId:next.taskId,path:'/tmp/upload.txt',offset:3});
  expect(current.sent.at(-1)).toMatchObject({op:'upload_end',taskId:next.taskId});
  await deliver(current,{op:'transfer_end',taskId:next.taskId});
  expect(JSON.parse(localStorage.getItem('rdevFileTasks.one'))[0].status).toBe('completed');
});

async function restoreUpload(status='running') {
  localStorage.setItem('rdevFileTasks.two',JSON.stringify([{id:'old-upload',dir:'upload',device:'two',name:'upload.txt',parentPath:'/original',size:3,done:1,status,created:1}]));
  await render('two');
  const socket=sockets.at(-1);
  await act(async()=>socket.onopen());
  await deliver(socket,reply(socket.sent.find(x=>x.op==='list'),'current.txt',{path:'/current'}));
  return socket;
}

it('restores uploads as requiring a file and does not match a new upload in another directory', async()=>{
  const socket=await restoreUpload();
  expect(document.body.textContent).toContain('files.reselect');
  expect(socket.sent.some(x=>x.op==='upload_start')).toBe(false);
  await pickFile();
  expect(socket.sent.at(-1)).toMatchObject({op:'upload_start',deviceId:'two',parentPath:'/current',name:'upload.txt'});
  const saved=JSON.parse(localStorage.getItem('rdevFileTasks.two'));
  expect(saved).toHaveLength(2);
  expect(saved.find(x=>x.id==='old-upload')).toMatchObject({status:'failed',parentPath:'/original'});
});

it('binds explicit reselect to its original task and rejects a mismatched file', async()=>{
  const socket=await restoreUpload('queued');
  await act(async()=>document.querySelector('[aria-label="files.resume upload.txt"]').click());
  await pickFile('wrong.txt');
  expect(document.body.textContent).toContain('files.reselectMismatch');
  expect(socket.sent.some(x=>x.op==='upload_start')).toBe(false);
  await act(async()=>document.querySelector('[aria-label="files.resume upload.txt"]').click());
  await pickFile();
  expect(socket.sent.at(-1)).toMatchObject({op:'upload_start',deviceId:'two',parentPath:'/original',name:'upload.txt'});
  const saved=JSON.parse(localStorage.getItem('rdevFileTasks.two'));
  expect(saved).toHaveLength(1);
  expect(saved[0]).toMatchObject({id:'old-upload',status:'queued'});
});
