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
import { settledAskCardKeys } from '../ui/components/agent/transcript-rows';

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

it('replaces only linked answered cards once their settled tool row is visible', () => {
  const first = { kind:'turn', key:1, tools:[{ id:'reused', name:'ask', done:true }] };
  const second = { kind:'turn', key:2, tools:[{ id:'reused', name:'ask', done:false }] };
  const cards = [
    {kind:'ask',key:3,turnKey:1,toolId:'reused',answer:'one'},
    {kind:'ask',key:4,turnKey:2,toolId:'reused',answer:'two'},
    {kind:'ask',key:5,turnKey:1,toolId:'reused',answer:null},
    {kind:'ask',key:6,answer:'unlinked'},
  ];
  expect([...settledAskCardKeys([first,second,...cards],1)]).toEqual([]);
  expect([...settledAskCardKeys([first,second,...cards],2)]).toEqual([3]);
  expect([...settledAskCardKeys([first,{...second,tools:[{...second.tools[0],done:true}]},...cards],null)]).toEqual([3,4]);
});
