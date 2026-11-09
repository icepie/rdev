import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import { expect, it, vi } from 'vitest';
const script=readFileSync(new URL('../../internal/server/browser_locale.js',import.meta.url),'utf8');
it.each(['zh','en-US','invalid'])('initializes only a supported DevTools locale: %s',locale=>{
 const setItem=vi.fn();
 vm.runInNewContext(script,{URLSearchParams,location:{search:`?rdevLang=${locale}`},localStorage:{setItem}});
 if(locale==='invalid')expect(setItem).not.toHaveBeenCalled();
 else expect(setItem).toHaveBeenCalledExactlyOnceWith('language',JSON.stringify(locale));
});
