import { expect, it, vi } from 'vitest';
import { attachDesktopMediaSource, desktopEncoderAcceleration, desktopEncoderRecoveryFormat, desktopEncoderStatus, desktopInputShouldDrop, desktopPanelPresets, desktopRuntimeEncoderStatus, desktopStreamRates } from '../ui/lib/desktop-media';
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

it('retries failed hardware streams with CPU H264 before falling back to MJPEG',()=>{
 expect(desktopEncoderRecoveryFormat('h264','nvenc')).toBe('h264');
 expect(desktopEncoderRecoveryFormat('h264','vaapi')).toBe('h264');
 expect(desktopEncoderRecoveryFormat('h264','mediafoundation')).toBe('h264');
 expect(desktopEncoderRecoveryFormat('h264','cpu')).toBe('mjpeg');
 expect(desktopEncoderRecoveryFormat('h264')).toBe('mjpeg');
 expect(desktopEncoderRecoveryFormat('mjpeg','jpeg')).toBeNull();
});

it('classifies only the active encoder, not the selection or API name', () => {
  for (const backend of ['h264_mf', 'mediafoundation', 'mediacodec-h264', 'videotoolbox', 'h264_videotoolbox', 'vendor-encoder']) {
    expect(desktopEncoderAcceleration(backend)).toBe('unknown');
  }
  for (const backend of ['cpu', 'jpeg', 'libx264']) expect(desktopEncoderAcceleration(backend)).toBe('software');
  for (const backend of ['NVENC', 'h264_nvenc', 'va-api', 'h264_vaapi/drm-prime']) expect(desktopEncoderAcceleration(backend)).toBe('hardware');
  expect(desktopEncoderAcceleration()).toBeNull();
  const t = key => key;
  expect(desktopEncoderStatus({encoderBackend: 'mediafoundation', encoderAcceleration: 'hardware'}, t)).toBe('desktop.hardwareAcceleration: mediafoundation');
  expect(desktopEncoderStatus({encoderBackend: 'c2.android.avc.encoder', encoderAcceleration: 'software'}, t)).toBe('desktop.softwareEncoding: c2.android.avc.encoder');
  expect(desktopEncoderStatus({encoderBackend: 'h264_mf'}, t)).toBe('desktop.encoderUnverified: h264_mf');
  expect(desktopEncoderStatus({encoderAcceleration: 'hardware'}, t)).toBe('');
  expect(desktopRuntimeEncoderStatus({inputBackend: 'uinput'}, t)).toBeUndefined();
  expect(desktopRuntimeEncoderStatus({encoderBackend: 'mediacodec-h264'}, t)).toBeUndefined();
  expect(desktopRuntimeEncoderStatus({encoderBackend: '等待视频流'}, t)).toBe('');
});

it('keeps manual rates and Agent presets distinct from auto defaults', () => {
  expect(desktopStreamRates('8', '50', true)).toEqual({fps: 30, quality: 75});
  expect(desktopStreamRates('8', '50')).toEqual({fps: 8, quality: 50});
  expect(desktopStreamRates('bad', '')).toEqual({fps: 30, quality: 75});
  expect(desktopStreamRates('60', '10')).toEqual({fps: 30, quality: 25});
  expect(desktopPanelPresets(null, null)).toEqual({fps: '30', quality: '75'});
  expect(desktopPanelPresets('20', '50')).toEqual({fps: '20', quality: '50'});
  expect(desktopPanelPresets('13', 'bad')).toEqual({fps: '30', quality: '75'});
});

it('drops only coalescable input while the desktop socket is congested',()=>{
 expect(desktopInputShouldDrop('mouse_move',600*1024)).toBe(true);
 expect(desktopInputShouldDrop('wheel',600*1024)).toBe(true);
 expect(desktopInputShouldDrop('key_down',600*1024)).toBe(false);
 expect(desktopInputShouldDrop('key_up',600*1024)).toBe(false);
 expect(desktopInputShouldDrop('mouse_down',600*1024)).toBe(false);
 expect(desktopInputShouldDrop('mouse_up',600*1024)).toBe(false);
 expect(desktopInputShouldDrop('wheel',128*1024)).toBe(false);
});
