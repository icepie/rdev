// @vitest-environment jsdom
import {act,createElement} from 'react';
import {createRoot} from 'react-dom/client';
import {expect,it,vi} from 'vitest';
import {JobCancel} from '../ui/components/agent/job-cancel';

it('deduplicates pending cancellation, validates receipts, and allows retry after failures', async () => {
 globalThis.IS_REACT_ACT_ENVIRONMENT = true;
 vi.useFakeTimers();
 const host=document.createElement('div');document.body.append(host);
 const root=createRoot(host), callbacks=new Map(), sent=[];
 let jobs=[{id:'a',label:'build',state:'running'},{id:'b',label:'other',state:'running'}];
 const props={job:jobs[0],threadId:'thread-a',ready:true,t:key=>key,
  ops:{jobActionReplies:{current:callbacks},setJobs:update=>{jobs=update(jobs);props.job=jobs[0];}},
  sendRaw:payload=>{sent.push(payload);return true;}};
 const render=()=>act(async()=>root.render(createElement(JobCancel,{...props,key:props.threadId})));
 const click=()=>act(async()=>host.querySelector('button').click());
 const reply=(extra={})=>act(async()=> {
  const request=sent.at(-1);
  callbacks.get(request.id)?.({op:'job_cancelled',id:request.id,jobId:request.jobId,thread:request.thread,value:true,...extra});
 });
 try {
  await render(); await click(); await click();
  expect(sent).toHaveLength(1);
  expect(sent[0]).toMatchObject({op:'cancel_job',thread:'thread-a',jobId:'a'});
  expect(sent[0].id).not.toBe('a');
  expect(host.querySelector('button').disabled).toBe(true);
  await reply({thread:'other-thread'});
  expect(jobs[0].cancelRequested).toBeUndefined();
  expect(host.querySelector('[role=alert]').textContent).toBe('agent.jobs.cancelUnconfirmed');
  await click();await reply({isError:true,message:'job missing'});
  expect(host.querySelector('[role=alert]').textContent).toBe('job missing');
  await click();
  const timedOut=callbacks.get(sent.at(-1).id);
  await act(async()=>vi.advanceTimersByTimeAsync(15000));
  expect(callbacks.size).toBe(0);
  expect(host.querySelector('button').disabled).toBe(false);
  await click();
  await act(async()=>timedOut({op:'job_cancelled',thread:'thread-a',jobId:'a',value:true}));
  expect(host.querySelector('button').disabled).toBe(true);
  expect(jobs[0].cancelRequested).toBeUndefined();
  props.ready=false;await render();
  expect(callbacks.size).toBe(0);
  props.ready=true;await render();
  props.sendRaw=()=>false;await render();await click();
  expect(callbacks.size).toBe(0);
  expect(host.querySelector('button').disabled).toBe(false);
  props.sendRaw=payload=>{sent.push(payload);return true;};await render();
  await click();await reply();await render();
  expect(jobs[0]).toMatchObject({state:'running',cancelRequested:true});
  expect(jobs[1].cancelRequested).toBeUndefined();
  expect(host.querySelector('button').disabled).toBe(true);
  jobs[0]={...jobs[0],cancelRequested:false};props.job=jobs[0];await render();await click();
  const stale=callbacks.get(sent.at(-1).id);
  props.threadId='thread-b';await render();
  expect(callbacks.size).toBe(0);
  await act(async()=>stale({op:'job_cancelled',thread:'thread-a',jobId:'a',value:true}));
  expect(jobs[0].cancelRequested).toBe(false);
 } finally {
  await act(async()=>root.unmount());
  expect(callbacks.size).toBe(0);expect(vi.getTimerCount()).toBe(0);
  vi.useRealTimers();host.remove();delete globalThis.IS_REACT_ACT_ENVIRONMENT;
 }
});
