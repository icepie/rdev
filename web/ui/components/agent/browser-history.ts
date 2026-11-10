export interface BrowserActionRecord {
  id: string; device: string; target?: string; action: string;
  startedAt: number; durationMs: number; ok: boolean; error?: string;
  thumbnail?: string; width?: number; height?: number;
}
export function parseBrowserRecords(value: unknown): BrowserActionRecord[] {
  if (!Array.isArray(value)) return [];
  return value.slice(-200).flatMap(v => {
    if (!v || typeof v !== 'object' || typeof v.id !== 'string' || typeof v.device !== 'string' || typeof v.action !== 'string' || !Number.isFinite(v.startedAt) || !Number.isFinite(v.durationMs)) return [];
    return [{ id:v.id, device:v.device, target:typeof v.target==='string'?v.target:undefined, action:v.action, startedAt:v.startedAt, durationMs:Math.max(0,v.durationMs), ok:v.ok===true,
      thumbnail:typeof v.thumbnail==='string' && v.thumbnail.length<=17000 && /^data:image\/jpeg;base64,[A-Za-z0-9+/]+=*$/.test(v.thumbnail)?v.thumbnail:undefined,
      width:Number.isFinite(v.width)?v.width:undefined, height:Number.isFinite(v.height)?v.height:undefined }];
  });
}
export function mergeBrowserRecords(current: BrowserActionRecord[], incoming: BrowserActionRecord[]): BrowserActionRecord[] {
  const byID = new Map(current.map(r => [r.id,r]));
  for (const row of incoming) byID.set(row.id,row);
  const rows = [...byID.values()].sort((a,b)=>a.startedAt-b.startedAt || a.id.localeCompare(b.id)).slice(-200);
  let thumbnails = 0;
  return rows.reverse().map(row => row.thumbnail && ++thumbnails>50 ? {...row,thumbnail:undefined} : row).reverse();
}
