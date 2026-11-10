import { expect, it } from 'vitest';
import { mergeBrowserRecords, parseBrowserRecords } from '../ui/components/agent/browser-history';
import { dragBrowserViewport, validBrowserViewport } from '../ui/components/browser-viewport';
const record = i => ({id:String(i),device:'device',action:'click',startedAt:i,durationMs:12,ok:true,thumbnail:'data:image/jpeg;base64,YQ=='});
it('deduplicates reconnects and bounds records and thumbnail memory',()=>{
 const records=Array.from({length:250},(_,i)=>record(i));
 const merged=mergeBrowserRecords(records,records.slice(240));
 expect(merged).toHaveLength(200);
 expect(merged[0].id).toBe('50');
 expect(merged.filter(r=>r.thumbnail)).toHaveLength(50);
 expect(records[50].thumbnail).toBeTruthy();
});
it('rejects malformed replay payloads and active image URLs',()=>{
 expect(parseBrowserRecords([{},null,{...record(1),startedAt:'now'}])).toEqual([]);
 expect(parseBrowserRecords([{...record(1),thumbnail:'https://other.test/pixel'}])[0].thumbnail).toBeUndefined();
 expect(parseBrowserRecords([{...record(1),thumbnail:'data:image/svg+xml;base64,YQ=='}])[0].thumbnail).toBeUndefined();
});
it('dragging clamps page dimensions and rejects invalid viewport inputs',()=>{
 expect(dragBrowserViewport({width:1280,height:800},100,-100)).toEqual({width:1380,height:700});
 expect(dragBrowserViewport({width:1280,height:800},9999,-9999)).toEqual({width:3840,height:240});
 expect(validBrowserViewport({width:390,height:844})).toBe(true);
 expect(validBrowserViewport({width:NaN,height:844})).toBe(false);
 expect(validBrowserViewport({width:390,height:9999})).toBe(false);
});
