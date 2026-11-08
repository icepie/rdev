// @vitest-environment jsdom
import {act,createElement} from 'react';
import {createRoot} from 'react-dom/client';
import {MemoryRouter} from 'react-router-dom';
import {expect,it,vi} from 'vitest';
const fixture=vi.hoisted(()=>({preview:null}));
vi.mock('../ui/lib/devices',()=>({useDevices:()=>({devices:[{id:'device',hasPassword:false}]})}));
vi.mock('../ui/lib/api',()=>({wsURL:()=> 'ws://localhost/files',deviceAuthBypassed:async()=>false,getDevicePassword:()=>'',rememberDevicePassword(){},forgetDevicePassword(){}}));
vi.mock('../ui/lib/i18n',()=>({useI18n:()=>({t:key=>key})}));
vi.mock('../ui/components/icon',()=>({Icon:()=>null}));
vi.mock('../ui/components/os-icon',()=>({OsIcon:()=>null}));
vi.mock('../ui/components/agent/file-preview',()=>({FilePreview:props=>{fixture.preview=props;return null;}}));
import {FilesBrowser} from '../ui/pages/Files';

it('ends pending previews on disconnect, isolates old replies, and preserves loaded content',async()=>{
 const sockets=[];
 class Socket {
  static OPEN=1;readyState=0;sent=[];
  constructor(){sockets.push(this);}
  send(raw){this.sent.push(JSON.parse(raw));}
  open(){this.readyState=1;this.onopen?.();}
  close(){this.readyState=3;this.onclose?.();}
  message(frame){this.onmessage?.({data:JSON.stringify(frame)});}
 }
 vi.stubGlobal('WebSocket',Socket);
 vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT',true);
 vi.useFakeTimers();localStorage.clear();
 const host=document.createElement('div');document.body.append(host);
 const root=createRoot(host);
 try {
  await act(async()=>root.render(createElement(MemoryRouter,{initialEntries:['/files?device=device']},createElement(FilesBrowser,{primary:true,leafId:0,panesRef:{current:new Map()}}))));
  const first=sockets[0];
  await act(async()=>first.open());
  const listing=first.sent.find(frame=>frame.op==='list');
  await act(async()=>first.message({op:'list_result',requestId:listing.requestId,path:'/',entries:[{name:'notes.txt',path:'/notes.txt',size:10}]}));
  await act(async()=>host.querySelector('button[title="notes.txt"]').click());
  const original=first.sent.find(frame=>frame.op==='read');
  expect(fixture.preview.state.status).toBe('loading');
  const lateClose=first.onclose;
  await act(async()=>first.close());
  expect(fixture.preview.state).toMatchObject({status:'error',message:'files.connectionLost'});
  await act(async()=>vi.advanceTimersByTimeAsync(2000));
  const second=sockets[1];
  await act(async()=>second.open());
  await act(async()=>fixture.preview.onRefresh());
  const retry=second.sent.find(frame=>frame.op==='read');
  expect(retry.requestId).not.toBe(original.requestId);
  const reply={op:'file_content',deviceId:'device',content:'old',requestId:original.requestId};
  await act(async()=>{first.message(reply);second.message(reply);lateClose();});
  expect(fixture.preview.state.status).toBe('loading');
  await act(async()=>second.message({...reply,requestId:retry.requestId,content:'current'}));
  expect(fixture.preview.state).toMatchObject({status:'ready',content:'current'});
  await act(async()=>second.close());
  expect(fixture.preview.state).toMatchObject({status:'ready',content:'current'});
 } finally {
  await act(async()=>root.unmount());host.remove();localStorage.clear();vi.useRealTimers();vi.unstubAllGlobals();
 }
});
