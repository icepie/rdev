import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { expect, it, vi } from 'vitest';

vi.mock('../ui/components/icon', () => ({ Icon: () => null }));
vi.mock('../ui/components/markdown', () => ({ Markdown: ({text}) => createElement('p', null, text) }));
vi.mock('../ui/components/agent/thinking-block', () => ({ ThinkingBlock: () => null }));
vi.mock('../ui/components/agent/message-action-bar', () => ({ MessageActionBar: () => createElement('div', {'data-actions': true}) }));
vi.mock('../ui/components/agent/tool-row', () => ({
  ToolRow: ({tool}) => createElement('div', {'data-tool': tool.name}),
  ToolBundleRow: () => createElement('div', {'data-bundle': true}),
}));
import { TurnView } from '../ui/components/agent/turn-view';

it('keeps an active tool phase expanded without a text cursor or duplicate ask row', () => {
  const turn = {
    kind: 'turn', key: 1, text: 'Checking files', reasoning: '', streaming: false, streamingReasoning: false,
    tools: [1,2,3].map(id => ({id:String(id),name:'read_file',done:true})).concat({id:'ask',name:'ask',done:false}),
  };
  const render = active => renderToStaticMarkup(createElement(TurnView, {
    turn, active, t:key=>key, copied:false, onCopy(){}, onAddToChat(){},
  }));
  const running = render(true);
  expect(running.match(/data-tool="read_file"/g)).toHaveLength(3);
  expect(running).not.toContain('data-tool="ask"');
  expect(running).not.toContain('data-bundle');
  expect(running).not.toContain('data-actions');
  expect(running).not.toContain('after:animate-pulse');
  const settled = render(false);
  expect(settled).toContain('data-bundle');
  expect(settled).toContain('data-tool="ask"');
  expect(settled).toContain('data-actions');
});
