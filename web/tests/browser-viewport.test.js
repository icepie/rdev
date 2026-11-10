import { expect, it } from 'vitest';
import { validBrowserViewport } from '../ui/components/browser-viewport';
it('validates viewport inputs',()=>{
 expect(validBrowserViewport({width:390,height:844})).toBe(true);
 expect(validBrowserViewport({width:NaN,height:844})).toBe(false);
 expect(validBrowserViewport({width:390,height:9999})).toBe(false);
});
