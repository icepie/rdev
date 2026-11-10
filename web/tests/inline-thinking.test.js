import { expect, it } from 'vitest';
import { projectInlineThinking } from '../ui/components/agent/inline-thinking';
it('projects tagged reasoning for history and keeps the answer for copy',()=>{
 expect(projectInlineThinking('<think>Plan retry</think>\nAnswer',false)).toEqual({text:'\nAnswer',reasoning:'Plan retry',thinking:false});
 expect(projectInlineThinking('Before<think>One</think>After<think>Two</think>',false)).toEqual({text:'BeforeAfter',reasoning:'One\n\nTwo',thinking:false});
});
it('handles chunk boundaries, unclosed tags and final partial text without leaking delimiters',()=>{
 const source='<think>Plan retry</think>Answer';
 for(let i=1;i<=source.length;i++) {
  const projected=projectInlineThinking(source.slice(0,i),true);
  expect(projected.text).not.toContain('<');
  expect(projected.reasoning).not.toContain('</');
 }
 expect(projectInlineThinking('<think>Still working',true).thinking).toBe(true);
 expect(projectInlineThinking('<think>Interrupted',false)).toEqual({text:'',reasoning:'Interrupted',thinking:false});
 expect(projectInlineThinking('literal <thi',false).text).toBe('literal <thi');
});
it('preserves inline, fenced and escaped markup examples',()=>{
 for(const text of ['Use `<think>x</think>` here','```xml\n<think>x</think>\n```','~~~xml\n<think>x</think>\n~~~','Use \\<think> literally']) {
  expect(projectInlineThinking(text,false)).toEqual({text,reasoning:'',thinking:false});
 }
 expect(projectInlineThinking('```\n<think>example</think>\n```\n<think>real</think>Answer',false).reasoning).toBe('real');
});
