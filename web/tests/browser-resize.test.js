// @vitest-environment jsdom
import {expect,it,vi} from 'vitest';
import {resizeViewport,installBrowserResize} from '../../internal/server/browser_resize.js';
it('resizes only the selected edge in remote CSS pixels, including scaled previews',()=>{
 const start={width:1280,height:800,scaleX:.5,scaleY:.5};
 expect(resizeViewport({...start,centered:true},50,20,'x')).toEqual({width:1480,height:800});
 expect(resizeViewport(start,50,20,'x')).toEqual({width:1380,height:800});
 expect(resizeViewport(start,50,20,'y')).toEqual({width:1280,height:840});
 expect(resizeViewport(start,50,20,'both')).toEqual({width:1380,height:840});
 expect(resizeViewport(start,99999,-99999,'both')).toEqual({width:3840,height:240});
});
it('attaches slim edge grips to the page and cleans up without replacing the canvas',()=>{
 const surface=document.createElement('div');const canvas=document.createElement('canvas');surface.append(canvas);document.body.append(surface);
 canvas.getBoundingClientRect=()=>({left:20,top:40,right:660,bottom:440,width:640,height:400});
 const disconnect=vi.fn();vi.stubGlobal('ResizeObserver',class{observe(){}unobserve(){}disconnect=disconnect;});
 const notify=vi.fn();const resize=installBrowserResize({screencastView:{contentElement:surface}},notify);
 resize.set({width:1280,height:800});
 const grips=[...document.querySelectorAll('[role="separator"]')];expect(grips).toHaveLength(3);
 expect(grips[0].style.left).toBe('660px');expect(grips[1].style.top).toBe('440px');
 grips[0].dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowRight',bubbles:true}));
 expect(notify).toHaveBeenCalledWith({type:'rdev-browser-resize',width:1290,height:800});
 resize.dispose();expect(disconnect).toHaveBeenCalledOnce();expect(document.querySelector('[role="separator"]')).toBeNull();expect(surface.firstChild).toBe(canvas);
 surface.remove();vi.unstubAllGlobals();
});
