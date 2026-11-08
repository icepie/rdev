import {expect,it} from 'vitest';
import {filePreviewURL} from '../ui/lib/file-preview-url';

it('accepts only local grant URLs with a safe optional entry filename',()=>{
 const base='/api/media/'+'a'.repeat(64);
 for(const value of [base,base+'/index.html',base+'/hello%20world.html',base+'/%E9%A1%B5%E9%9D%A2.html']) expect(filePreviewURL(value)).toBe(value);
 for(const value of [undefined,'',base+'/../index.html',base+'/%2e%2e',base+'/%2Fetc.html',base+'/%5cfile.html',base+'/%00.html',base+'/%broken',base+'/.hidden.html',base+'/?x',base+'/#hash',base+'/x.html?key=1','//evil.example'+base,'https://evil.example'+base,'javascript:alert(1)','/api/media/short/index.html',base+'/C%3Afile.html']) expect(filePreviewURL(value)).toBeUndefined();
});
