import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { expect, it } from 'vitest';
const snapshot = readFileSync('internal/server/browser_snapshot.js', 'utf8');
const target = readFileSync('internal/server/browser_target.js', 'utf8');
function page(html) {
  const { window: w } = new JSDOM(html, { runScripts: 'outside-only', url: 'https://example.test' });
  w.TextEncoder = TextEncoder;
  w.CSS = { escape: x => x };
  w.HTMLElement.prototype.getClientRects = () => [{}];
  w.HTMLElement.prototype.scrollIntoView = () => {};
  w.HTMLElement.prototype.getBoundingClientRect = () => ({left: -20, top: 10, right: 100, bottom: 40});
  return w;
}
it('snapshot provides form labels and options without exposing password values', () => {
  const w = page('<label for="city">城市</label><select id="city"><option value="ly">洛阳</option></select><input type="password" value="secret"><input type="checkbox" checked>');
  const result = JSON.parse(w.eval(snapshot));
  expect(result.elements[0]).toMatchObject({label: '城市', options: [{value: 'ly', label: '洛阳', selected: true}]});
  expect(JSON.stringify(result)).not.toContain('secret');
  expect(result.elements[2].checked).toBe(true);
  w.close();
});
it('targets the visible portion and refuses covered, disabled and ambiguous targets', () => {
  const w = page('<button id="go">Go</button><div id="overlay"></div><button>Other</button>');
  const go = w.document.querySelector('#go');
  const run = selector => w.eval(target.replace('%s', JSON.stringify(selector)));
  w.document.elementFromPoint = () => go;
  expect(JSON.parse(run('#go'))).toEqual({x: 50, y: 25});
  w.document.elementFromPoint = () => w.document.querySelector('#overlay');
  expect(() => run('#go')).toThrow('covered');
  go.disabled = true;
  expect(() => run('#go')).toThrow('disabled');
  expect(() => run('button')).toThrow('ambiguous');
  expect(() => run('#missing')).toThrow('not found');
  w.close();
});
