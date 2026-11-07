import { afterEach, expect, it, vi } from 'vitest';
import { observeTranscriptResize } from '../ui/components/agent/transcript-resize';

const observers = [];
function pane(top = 800) {
  vi.stubGlobal('ResizeObserver', class {
    constructor(callback) { this.callback = callback; this.targets = []; observers.push(this); }
    observe(target) { this.targets.push(target); }
    disconnect() { this.targets = []; }
  });
  const viewport = Object.assign(new EventTarget(), { scrollTop: top, scrollHeight: 1000, clientHeight: 200 });
  const content = {};
  const attachment = vi.fn();
  const cleanup = observeTranscriptResize(viewport, content, attachment);
  return { viewport, content, attachment, cleanup, resize: observers.at(-1).callback, observer: observers.at(-1) };
}
afterEach(() => { observers.length = 0; vi.unstubAllGlobals(); });

it('follows delayed image growth and viewport shrink without a stream frame', () => {
  const p = pane();
  expect(p.observer.targets).toEqual([p.viewport, p.content]);
  p.viewport.scrollHeight = 2200;
  p.resize();
  expect(p.viewport.scrollTop).toBe(2000);
  p.viewport.clientHeight = 100;
  p.resize();
  expect(p.viewport.scrollTop).toBe(2100);
  expect(p.attachment).toHaveBeenLastCalledWith(true);
});

it('keeps a reader detached, then resumes following after returning to the bottom', () => {
  const p = pane();
  p.viewport.scrollTop = 300;
  p.viewport.dispatchEvent(new Event('scroll'));
  p.viewport.scrollHeight = 2000;
  p.resize();
  expect(p.viewport.scrollTop).toBe(300);
  expect(p.attachment).toHaveBeenLastCalledWith(false);
  p.viewport.scrollTop = 1800;
  p.viewport.dispatchEvent(new Event('scroll'));
  p.viewport.scrollHeight = 2400;
  p.resize();
  expect(p.viewport.scrollTop).toBe(2200);
});

it('honors upward movement even when resize notification arrives before the scroll event', () => {
  const p = pane();
  p.viewport.scrollHeight = 2000;
  p.viewport.scrollTop = 400;
  p.resize();
  expect(p.viewport.scrollTop).toBe(400);
  expect(p.attachment).toHaveBeenLastCalledWith(false);
});

it('handles scroll events delivered before resize and clamping after collapse', () => {
  const p = pane();
  p.viewport.scrollHeight = 2000;
  p.viewport.dispatchEvent(new Event('scroll'));
  p.resize();
  expect(p.viewport.scrollTop).toBe(1800);
  p.viewport.scrollHeight = 600;
  p.viewport.scrollTop = 400; // browser clamp
  p.viewport.dispatchEvent(new Event('scroll'));
  p.resize();
  p.viewport.scrollHeight = 1200;
  p.resize();
  expect(p.viewport.scrollTop).toBe(1000);
});

it('isolates split panes and disconnects listeners and queued observer callbacks', () => {
  const left = pane();
  const right = pane(300);
  left.viewport.scrollHeight = right.viewport.scrollHeight = 2000;
  left.resize();
  right.resize();
  expect(left.viewport.scrollTop).toBe(1800);
  expect(right.viewport.scrollTop).toBe(300);
  left.cleanup();
  left.attachment.mockClear();
  left.viewport.scrollHeight = 3000;
  left.resize();
  left.viewport.dispatchEvent(new Event('scroll'));
  expect(left.viewport.scrollTop).toBe(1800);
  expect(left.attachment).not.toHaveBeenCalled();
  expect(left.observer.targets).toEqual([]);
});

it('ignores a hidden pane without losing its last visible attachment', () => {
  const p = pane();
  p.viewport.clientHeight = 0;
  p.viewport.scrollTop = 0;
  p.resize();
  p.viewport.clientHeight = 200;
  p.viewport.scrollTop = 800;
  p.viewport.scrollHeight = 2000;
  p.resize();
  expect(p.viewport.scrollTop).toBe(1800);
});
