import { expect, it } from 'vitest';
import { createFileTreeRPC } from '../ui/lib/file-tree-rpc';

it('keeps concurrent device directories isolated and combines pages', async () => {
  const sent = [];
  const rpc = createFileTreeRPC(message => { sent.push(message); return true; });
  const first = rpc.request('a', 'list', { path: '/work' });
  const second = rpc.request('b', 'list', { path: '/work' });
  const [a, b] = sent;
  rpc.accept({ ...a, op: 'list_result', deviceId: 'b', entries: [{ name: 'wrong' }] });
  rpc.accept({ ...b, op: 'list_result', entries: [{ name: 'b' }] });
  rpc.accept({ ...a, op: 'list_result', offset: 0, total: 2, truncated: true, entries: [{ name: 'one' }] });
  expect(sent[2]).toMatchObject({ deviceId: 'a', path: '/work', offset: 1 });
  rpc.accept({ ...a, op: 'list_result', offset: 1, total: 2, entries: [{ name: 'two' }] });
  expect(await first).toEqual([{ name: 'one' }, { name: 'two' }]);
  expect(await second).toEqual([{ name: 'b' }]);
  expect(rpc.accept({ ...a, op: 'list_result' })).toBe(false);
});

it('rejects pending mutations on disconnect and ignores late acknowledgements', async () => {
  const sent = [];
  const rpc = createFileTreeRPC(message => { sent.push(message); return true; });
  const operation = rpc.request('a', 'rename', { from: '/old', to: '/new' });
  const rejected = expect(operation).rejects.toThrow('Connection closed');
  rpc.cancel();
  await rejected;
  expect(rpc.accept({ ...sent[0], op: 'rename_result', success: true })).toBe(false);
});

it('requires successful mutation acknowledgements and does not queue offline writes', async () => {
  let message;
  const rpc = createFileTreeRPC(value => { message = value; return true; });
  const operation = rpc.request('a', 'delete', { path: '/file' });
  const rejected = expect(operation).rejects.toThrow('denied');
  rpc.accept({ ...message, op: 'delete_result', success: false, error: 'denied' });
  await rejected;
  const offline = createFileTreeRPC(() => false);
  await expect(offline.request('a', 'mkdir', { path: '/new' })).rejects.toThrow('not connected');
});

it('uses exclusive creation and preserves Windows separators for both file workspaces', async () => {
  const {fileTreeMutation}=await import('../ui/lib/file-tree-rpc');
  expect(fileTreeMutation('newFile',{path:'C:\\work',name:'work',isDir:true},'Foo')).toEqual({op:'create',args:{path:'C:\\work\\Foo'}});
  expect(fileTreeMutation('rename',{path:'/work/Foo',name:'Foo'},'foo')).toEqual({op:'rename',args:{path:'/work/Foo',from:'/work/Foo',to:'/work/foo'}});
});
