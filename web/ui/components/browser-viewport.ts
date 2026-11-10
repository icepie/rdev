export interface BrowserViewport { width: number; height: number }
export function validBrowserViewport(value: unknown): value is BrowserViewport {
  const v = value as BrowserViewport | null;
  return !!v && Number.isInteger(v.width) && Number.isInteger(v.height) && v.width>=240 && v.width<=3840 && v.height>=240 && v.height<=2160;
}
export function dragBrowserViewport(start: BrowserViewport, dx: number, dy: number): BrowserViewport {
  return {width:Math.max(240,Math.min(3840,Math.round(start.width+dx))),height:Math.max(240,Math.min(2160,Math.round(start.height+dy)))};
}
export function viewportStorageKey(src: string): string {
  // Store only page identity, never the capability URL or credentials.
  const url = new URL(src, location.origin);
  const socket = url.searchParams.get('ws') || url.searchParams.get('wss') || '';
  return 'rdev:browser-viewport:' + socket.split('/').pop();
}
export function loadBrowserViewport(src: string): BrowserViewport {
  try {const value=JSON.parse(sessionStorage.getItem(viewportStorageKey(src))||'null');if(validBrowserViewport(value))return value;} catch { /* Storage may be disabled. */ }
  return {width:1280,height:800};
}
