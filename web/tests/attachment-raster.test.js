// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { needsAttachmentRaster, rasterizeAttachment } from '../ui/components/agent/attachment-raster';
let images, draw, encode;
beforeEach(() => {
 vi.useFakeTimers(); images=[]; draw=vi.fn();
 class FakeImage {
  naturalWidth=100; naturalHeight=80;
  constructor(){images.push(this)}
  removeAttribute=vi.fn();
 }
 vi.stubGlobal('Image',FakeImage);
 vi.spyOn(HTMLCanvasElement.prototype,'getContext').mockReturnValue({drawImage:draw});
 encode=vi.spyOn(HTMLCanvasElement.prototype,'toDataURL').mockReturnValue('data:image/png;base64,b2s=');
});
afterEach(()=>{vi.restoreAllMocks();vi.unstubAllGlobals();vi.useRealTimers()});
it('converts only formats outside the shared decoder set',()=>{
 for(const mime of ['image/svg+xml','image/avif','image/x-icon','image/vnd.microsoft.icon']) expect(needsAttachmentRaster(mime)).toBe(true);
 for(const mime of ['image/png','image/jpeg','image/webp','image/gif','image/bmp']) expect(needsAttachmentRaster(mime)).toBe(false);
});
it('rasterizes at original dimensions and releases loading resources',async()=>{
 const result=rasterizeAttachment('data:image/svg+xml;base64,eA==',new AbortController().signal);
 images[0].onload();
 expect(await result).toBe('data:image/png;base64,b2s=');
 expect(draw).toHaveBeenCalledWith(images[0],0,0,100,80);
 expect(images[0].removeAttribute).toHaveBeenCalledWith('src');
 expect(vi.getTimerCount()).toBe(0);
});
it('shrinks oversized PNG output until it fits the attachment limit',async()=>{
 encode.mockReturnValueOnce('data:image/png;base64,'+'x'.repeat(6*1024*1024));
 const result=rasterizeAttachment('data:image/avif;base64,eA==',new AbortController().signal);
 images[0].onload(); await result;
 expect(draw.mock.calls.map(call=>call.slice(3))).toEqual([[100,80],[50,40]]);
});
it.each(['abort','timeout','decode','dimensions','canvas'])('cleans up a %s failure',async failure=>{
 const controller=new AbortController();
 const result=rasterizeAttachment('data:image/x-icon;base64,eA==',controller.signal);
 const rejected=expect(result).rejects.toThrow('image conversion failed');
 if(failure==='abort') controller.abort();
 if(failure==='timeout') vi.advanceTimersByTime(10000);
 if(failure==='decode') images[0].onerror();
 if(failure==='dimensions'){images[0].naturalWidth=1000000;images[0].onload()}
 if(failure==='canvas'){encode.mockImplementation(()=>{throw new Error('canvas unavailable')});images[0].onload()}
 await rejected;
 expect(images[0].onload).toBeNull();expect(images[0].onerror).toBeNull();
 expect(vi.getTimerCount()).toBe(0);
});
