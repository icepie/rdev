import { expect, it, vi } from 'vitest';
import { attachDesktopMediaSource } from '../ui/lib/desktop-media';
function fixture() {
 const buffer=new EventTarget();buffer.mode='segments';
 const source=new EventTarget();source.readyState='open';source.addSourceBuffer=vi.fn(()=>buffer);
 const calls={current:vi.fn(()=>true),supported:vi.fn(()=>true),ready:vi.fn(),update:vi.fn(),error:vi.fn()};
 const cleanup=attachDesktopMediaSource(source,calls);
 return {source,buffer,calls,cleanup};
}
it('starts queued data at sourceopen and continues on updateend',()=>{
 const {source,buffer,calls,cleanup}=fixture();
 source.dispatchEvent(new Event('sourceopen'));
 expect(calls.ready).toHaveBeenCalledWith(buffer);expect(buffer.mode).toBe('sequence');
 expect(calls.update).toHaveBeenCalledTimes(1);
 buffer.dispatchEvent(new Event('updateend'));expect(calls.update).toHaveBeenCalledTimes(2);
 source.dispatchEvent(new Event('sourceopen'));expect(source.addSourceBuffer).toHaveBeenCalledTimes(1);
 cleanup();buffer.dispatchEvent(new Event('updateend'));expect(calls.update).toHaveBeenCalledTimes(2);
});
it('ignores a late sourceopen after stream replacement even before cleanup',()=>{
 const {source,calls,cleanup}=fixture();calls.current.mockReturnValue(false);
 source.dispatchEvent(new Event('sourceopen'));
 expect(source.addSourceBuffer).not.toHaveBeenCalled();expect(calls.ready).not.toHaveBeenCalled();cleanup();
});
it('detaches unopened sources and ignores stale buffer events',()=>{
 const first=fixture();first.cleanup();first.source.dispatchEvent(new Event('sourceopen'));
 expect(first.source.addSourceBuffer).not.toHaveBeenCalled();
 const next=fixture();next.source.dispatchEvent(new Event('sourceopen'));next.calls.current.mockReturnValue(false);
 next.buffer.dispatchEvent(new Event('updateend'));expect(next.calls.update).toHaveBeenCalledTimes(1);next.cleanup();
});
it('reports unsupported media setup without uncaught event exceptions',()=>{
 const {source,calls,cleanup}=fixture();calls.supported.mockReturnValue(false);
 const failure=new Error('codec not supported');source.addSourceBuffer.mockImplementation(()=>{throw failure});
 source.dispatchEvent(new Event('sourceopen'));
 expect(source.addSourceBuffer).toHaveBeenCalledWith('video/mp4');expect(calls.error).toHaveBeenCalledWith(failure);
 expect(calls.update).not.toHaveBeenCalled();cleanup();
});
