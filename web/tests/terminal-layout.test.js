// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { createTerminalLayout } from '../ui/lib/terminal-layout';
let callbacks, fit, repaint, invalidate, shown, layout;
beforeEach(()=>{
 callbacks=new Map();let id=0;
 vi.spyOn(window,'requestAnimationFrame').mockImplementation(fn=>{callbacks.set(++id,fn);return id});
 vi.spyOn(window,'cancelAnimationFrame').mockImplementation(id=>callbacks.delete(id));
 fit=vi.fn();repaint=vi.fn();invalidate=vi.fn();shown=true;
 layout=createTerminalLayout({visible:()=>shown,fit,repaint,invalidate});
});
afterEach(()=>{layout.dispose();vi.restoreAllMocks()});
const paint=()=>{for(const callback of [...callbacks.values()])callback()};
it('fits and repaints only once for a burst of divider and observer events',()=>{
 for(let i=0;i<100;i++)layout.request();
 expect(callbacks.size).toBe(1);expect(fit).not.toHaveBeenCalled();
 paint();expect(fit).toHaveBeenCalledTimes(1);expect(repaint).toHaveBeenCalledTimes(1);
 expect(invalidate).not.toHaveBeenCalled();expect(callbacks.size).toBe(0);
});
it('keeps a hidden theme invalidation until the pane becomes visible',()=>{
 shown=false;layout.request(true);layout.request();paint();
 expect(fit).not.toHaveBeenCalled();expect(invalidate).not.toHaveBeenCalled();
 shown=true;layout.request();paint();
 expect(invalidate).toHaveBeenCalledTimes(1);
 layout.request();paint();expect(invalidate).toHaveBeenCalledTimes(1);
});
it('synchronizes immediately for ready without leaving a duplicate scheduled fit',()=>{
 layout.request();layout.flush();paint();
 expect(fit).toHaveBeenCalledTimes(1);expect(callbacks.size).toBe(0);
});
it('cancels queued and late work after disposal',()=>{
 layout.request(true);const late=[...callbacks.values()][0];layout.dispose();
 late();layout.request();layout.flush();
 expect(callbacks.size).toBe(0);expect(fit).not.toHaveBeenCalled();expect(invalidate).not.toHaveBeenCalled();
});
