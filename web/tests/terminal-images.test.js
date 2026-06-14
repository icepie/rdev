import { deflateSync } from 'node:zlib';
import { JSDOM } from 'jsdom';
import { readFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

const root = resolve(import.meta.dirname, '../..');
const publicDir = resolve(root, 'web/public');
const ESC = '\x1b';
const ST = ESC + '\\';
const PNG_1X1 = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=';

async function setupTerminalImagesDom() {
  const dom = new JSDOM('<!doctype html><html><body><div id="term"><div class="xterm-rows"><div></div></div></div></body></html>', {
    url: 'https://rdev.test/',
    runScripts: 'outside-only',
    pretendToBeVisual: true
  });
  dom.window.DecompressionStream = globalThis.DecompressionStream;
  dom.window.Response = globalThis.Response;
  dom.window.Blob = globalThis.Blob;
  const source = await readFile(resolve(publicDir, 'terminal-images.js'), 'utf8');
  dom.window.eval(source);
  return dom;
}

function fakeTerm(window) {
  const writes = [];
  const element = window.document.getElementById('term');
  Object.defineProperty(element.querySelector('.xterm-rows > div'), 'getBoundingClientRect', {
    value: () => ({ width: 800, height: 20 })
  });
  return {
    cols: 80,
    element,
    buffer: { active: { cursorX: 2, cursorY: 3 } },
    write: text => writes.push(text),
    writes
  };
}

async function waitForImageSrc(img) {
  for (let i = 0; i < 30; i++) {
    if (img.src) return img.src;
    await new Promise(resolve => setTimeout(resolve, 10));
  }
  return img.src;
}

describe('RDevTerminalImages', () => {
  it('passes ordinary terminal output through unchanged', async () => {
    const dom = await setupTerminalImagesDom();
    const term = fakeTerm(dom.window);
    const writer = dom.window.RDevTerminalImages.create(term);

    writer.write(new TextEncoder().encode('hello'));
    writer.write(' world');

    expect(term.writes.join('')).toBe('hello world');
    expect(term.element.querySelector('.rdev-kitty-image-layer')).toBeNull();
  });

  it('renders Kitty inline image data at the terminal cursor', async () => {
    const dom = await setupTerminalImagesDom();
    const term = fakeTerm(dom.window);
    const writer = dom.window.RDevTerminalImages.create(term);

    writer.write(`before${ESC}_Ga=T,f=100,c=4,r=2;${PNG_1X1}${ST}after`);

    const img = term.element.querySelector('.rdev-kitty-image-layer img');
    expect(term.writes.join('')).toBe('before\n\nafter');
    expect(img).not.toBeNull();
    expect(await waitForImageSrc(img)).toBe(`data:image/png;base64,${PNG_1X1}`);
    expect(img.style.left).toBe('20px');
    expect(img.style.top).toBe('60px');
    expect(img.style.width).toBe('40px');
    expect(img.style.height).toBe('40px');
  });

  it('reassembles Kitty chunks and incomplete websocket frames', async () => {
    const dom = await setupTerminalImagesDom();
    const term = fakeTerm(dom.window);
    const writer = dom.window.RDevTerminalImages.create(term);
    const partA = PNG_1X1.slice(0, 24);
    const partB = PNG_1X1.slice(24);

    writer.write(`${ESC}_Ga=T,f=100,i=42,m=1;${partA}${ST}`);
    writer.write(`${ESC}_Gm=0;${partB}${ST}`);

    const img = term.element.querySelector('.rdev-kitty-image-layer img');
    expect(img).not.toBeNull();
    expect(await waitForImageSrc(img)).toBe(`data:image/png;base64,${PNG_1X1}`);
    expect(term.writes.join('')).toBe('');
  });

  it('leaves split escape prefixes pending until the next frame', async () => {
    const dom = await setupTerminalImagesDom();
    const term = fakeTerm(dom.window);
    const writer = dom.window.RDevTerminalImages.create(term);

    writer.write('text' + ESC);
    expect(term.writes.join('')).toBe('text');
    writer.write(`_Ga=T,f=101,s=9,v=7;${PNG_1X1}${ST}`);

    const img = term.element.querySelector('.rdev-kitty-image-layer img');
    expect(await waitForImageSrc(img)).toBe(`data:image/jpeg;base64,${PNG_1X1}`);
    expect(img.style.width).toBe('9px');
    expect(img.style.height).toBe('7px');
  });

  it('decodes zlib-compressed Kitty image payloads', async () => {
    const dom = await setupTerminalImagesDom();
    const term = fakeTerm(dom.window);
    const writer = dom.window.RDevTerminalImages.create(term);
    const compressed = deflateSync(Buffer.from(PNG_1X1, 'base64')).toString('base64');

    writer.write(`${ESC}_Ga=T,f=100,o=z,s=8,v=8;${compressed}${ST}`);
    const img = term.element.querySelector('.rdev-kitty-image-layer img');

    expect(img).not.toBeNull();
    expect(await waitForImageSrc(img)).toBe(`data:image/png;base64,${PNG_1X1}`);
  });

  it('reports unsupported Kitty path mode and oversized sequences', async () => {
    const dom = await setupTerminalImagesDom();
    const term = fakeTerm(dom.window);
    const writer = dom.window.RDevTerminalImages.create(term, { maxSequence: 8 });

    writer.write(`${ESC}_Gt=f;${PNG_1X1}${ST}`);
    writer.write(`${ESC}_Ga=T;this-is-too-long`);

    const output = term.writes.join('');
    expect(output).toContain('kitty image path mode is not supported yet');
    expect(output).toContain('kitty image sequence too large');
  });

  it('exposes parser helpers for automated protocol assertions', async () => {
    const dom = await setupTerminalImagesDom();
    const { RDevTerminalImages } = dom.window;

    expect(RDevTerminalImages.parseParams('a=T,f=102,c=3')).toEqual({ a: 'T', f: '102', c: '3' });
    expect(RDevTerminalImages.mimeFor({ f: '102' })).toBe('image/gif');
  });
});
