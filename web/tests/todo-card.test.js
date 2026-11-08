// @vitest-environment jsdom
import { act, createElement } from 'react';
import { createRoot } from 'react-dom/client';
import { expect, it } from 'vitest';
import { TodoCard } from '../ui/components/agent/todo-card';

it('distinguishes blocked and abandoned tasks without counting abandonment as completion', async () => {
 globalThis.IS_REACT_ACT_ENVIRONMENT = true;
 const host=document.createElement('div');document.body.append(host);
 const root=createRoot(host);
 const todos=[
  {content:'Skipped',status:'abandoned'},
  {content:'Done',status:'completed'},
  {content:'Waiting',status:'blocked',blocker:'Device offline'},
  {content:'Later',status:'pending'},
  {content:'Working',status:'in_progress'},
 ];
 const t=(key,args)=>key==='agent.todosDone'?`${args.done}/${args.total} completed`:{'agent.todoBlocked':'Blocked','agent.todoAbandoned':'Abandoned'}[key];
 try {
  await act(async()=>root.render(createElement(TodoCard,{todos,t})));
  const button=host.querySelector('button');
  expect(button.textContent).toContain('1/5 completed');
  await act(async()=>button.click());
  const rows=[...host.querySelectorAll('li')];
  expect(rows.map(row=>row.querySelector('span[title]').title)).toEqual(['Working','Waiting','Later','Skipped','Done']);
  expect(rows[1].textContent).toContain('Blocked');
  expect(rows[1].textContent).toContain('Device offline');
  expect(rows[3].textContent).toContain('Abandoned');
  expect(document.getElementById(button.getAttribute('aria-controls'))).toBe(host.querySelector('ul'));
  expect(host.querySelector('.animate-spin')).toBeNull();
  await act(async()=>button.click());
  expect(button.getAttribute('aria-expanded')).toBe('false');
  expect(host.querySelector('.disclosure-body').getAttribute('aria-hidden')).toBe('true');
 } finally {await act(async()=>root.unmount());host.remove();}
});
