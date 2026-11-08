// @vitest-environment jsdom
import { act, createElement } from 'react';
import { createRoot } from 'react-dom/client';
import { expect, it, vi } from 'vitest';
import { JobGuidance } from '../ui/components/agent/job-guidance';

it('correlates replies, retains drafts across targets and failures, and cleans up requests', async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers();
  const host = document.createElement('div');
  document.body.append(host);
  const root = createRoot(host);
  const callbacks = new Map();
  const sent = [];
  const props = {job:{id:'a',kind:'subagent',state:'running'}, threadId:'thread-a', ready:true,
    ops:{jobGuidanceReplies:{current:callbacks}}, sendRaw:payload => {sent.push(payload);return true;}, t:key=>key};
  const render = () => act(async () => root.render(createElement(JobGuidance,props)));
  const input = () => host.querySelector('textarea');
  const type = text => act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype,'value').set.call(input(),text);
    input().dispatchEvent(new Event('input',{bubbles:true}));
  });
  const submit = () => act(async () => host.querySelector('form').dispatchEvent(new Event('submit',{bubbles:true,cancelable:true})));
  const reply = (request,extra={}) => act(async () => callbacks.get(request.id)?.({op:'job_steered',id:request.id,thread:request.thread,jobId:request.jobId,...extra}));
  try {
    await render();
    await type('check logs');
    await submit();
    expect(sent[0]).toMatchObject({op:'steer_job',thread:'thread-a',jobId:'a',text:'check logs'});
    expect(host.querySelector('button').disabled).toBe(true);
    await submit();
    expect(sent).toHaveLength(1);
    // Continue editing while receipt is pending; acceptance must not erase it.
    await type('check logs and report');
    await reply(sent[0]);
    expect(input().value).toBe('check logs and report');
    expect(host.querySelector('[role="status"]').textContent).toBe('agent.jobs.guidanceAccepted');
    await submit();
    const a = sent.at(-1);
    props.job = {...props.job,id:'b'};
    props.threadId = 'thread-b';
    await render();
    expect(input().value).toBe('');
    await type('other task');
    await reply(a);
    expect(input().value).toBe('other task');
    await submit();
    await reply(sent.at(-1),{isError:true,message:'subagent finished'});
    expect(input().value).toBe('other task');
    expect(host.querySelector('[role="alert"]').textContent).toBe('subagent finished');
    await submit();
    await act(async () => vi.advanceTimersByTimeAsync(15000));
    expect(input().value).toBe('other task');
    expect(host.querySelector('[role="alert"]').textContent).toBe('agent.jobs.guidanceUnconfirmed');
    expect(callbacks.size).toBe(0);
    await submit();
    props.ready = false;
    await render();
    expect(callbacks.size).toBe(0);
    expect(input().value).toBe('other task');
    props.ready = true;
    await render();
    await submit();
    const wrong = sent.at(-1);
    await reply(wrong,{jobId:'a'});
    expect(input().value).toBe('other task');
    props.job = {...props.job,cancelRequested:true};
    await render();
    expect(input().value).toBe('other task');
    expect(host.querySelector('button').disabled).toBe(true);
    expect(host.textContent).toContain('agent.jobs.guidanceCanceling');
    const beforeStopping = sent.length;
    await submit();
    expect(sent).toHaveLength(beforeStopping);
    props.job = {...props.job,state:'completed',cancelRequested:false};
    await render();
    expect(input().value).toBe('other task');
    expect(host.querySelector('button').disabled).toBe(true);
    props.job = {id:'a',kind:'subagent',state:'running'};
    props.threadId = 'thread-a';
    await render();
    expect(input().value).toBe(''); // cleared by its own acknowledgement while hidden
    await type('界'.repeat(1366));
    const count = sent.length;
    await submit();
    expect(sent).toHaveLength(count);
    expect(host.querySelector('[role="alert"]').textContent).toBe('agent.jobs.guidanceTooLong');
    await type('last');
    props.sendRaw = () => false;
    await render();
    await submit();
    expect(input().value).toBe('last');
    expect(callbacks.size).toBe(0);
    props.sendRaw = payload => {sent.push(payload);return true;};
    await render();
    await submit();
    expect(callbacks.size).toBe(1);
  } finally {
    await act(async () => root.unmount());
    expect(callbacks.size).toBe(0);
    expect(vi.getTimerCount()).toBe(0);
    vi.useRealTimers();
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
