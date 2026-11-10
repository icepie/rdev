// Edge grips stay attached to the rendered page, not the outer panel. Deltas
// are converted from preview pixels to remote CSS pixels once per gesture.
export function resizeViewport(start, dx, dy, axis) {
  return {
    width: Math.max(240, Math.min(3840, Math.round(start.width + (axis === 'y' ? 0 : dx * (start.centered ? 2 : 1) / start.scaleX)))),
    height: Math.max(240, Math.min(2160, Math.round(start.height + (axis === 'x' ? 0 : dy / start.scaleY)))),
  };
}
export function installBrowserResize(app, notify) {
  let viewport = {width:1280,height:800};
  let gesture = null;
  let canvas;
  let frame;
  let pending;
  const chinese = new URLSearchParams(location.search).get('rdevLang') === 'zh';
  const overlay = document.createElement('div');
  overlay.style.cssText = 'position:fixed;inset:0;pointer-events:none;z-index:1000;overflow:hidden';
  const handles = ['x','y','both'].map(axis => {
    const handle = document.createElement('div');
    handle.tabIndex = 0;
    handle.setAttribute('role','separator');
    handle.setAttribute('aria-label', chinese
      ? (axis === 'x' ? '调整视口宽度' : axis === 'y' ? '调整视口高度' : '调整视口尺寸')
      : (axis === 'x' ? 'Resize viewport width' : axis === 'y' ? 'Resize viewport height' : 'Resize viewport'));
    handle.setAttribute('aria-orientation', axis === 'y' ? 'horizontal' : 'vertical');
    handle.setAttribute('aria-valuemin', '240');
    handle.setAttribute('aria-valuemax', axis === 'y' ? '2160' : '3840');
    handle.style.cssText = `position:absolute;pointer-events:auto;touch-action:none;cursor:${axis==='x'?'ew':axis==='y'?'ns':'nwse'}-resize;display:none;outline-offset:-2px`;
    const grip = document.createElement('div');
    grip.style.cssText = `position:absolute;inset:0;margin:auto;opacity:.6;border-color:var(--sys-color-on-surface-subtle,#888);border-style:solid;${axis==='x'?'width:3px;height:24px;border-width:0 1px':axis==='y'?'width:24px;height:3px;border-width:1px 0':'width:6px;height:6px;border-width:0 2px 2px 0'}`;
    handle.append(grip);
    handle.addEventListener('pointerdown',event=>{
      if(event.button!==0||!canvas)return;
      event.preventDefault();event.stopPropagation();
      const rect=canvas.getBoundingClientRect();
      if(!rect.width||!rect.height)return;
      gesture={...viewport,centered:true,x:event.clientX,y:event.clientY,axis,scaleX:rect.width/viewport.width,scaleY:rect.height/viewport.height};
      handle.setPointerCapture(event.pointerId);
    });
    const flush=()=>{if(pending){notify({type:'rdev-browser-resize',...pending});pending=null;}frame=undefined;};
    handle.addEventListener('pointermove',event=>{
      if(!gesture)return;
      event.preventDefault();event.stopPropagation();
      pending=resizeViewport(gesture,event.clientX-gesture.x,event.clientY-gesture.y,gesture.axis);
      if(!frame)frame=requestAnimationFrame(flush);
    });
    const finish=()=>{gesture=null;if(frame)cancelAnimationFrame(frame);flush();};
    handle.addEventListener('pointerup',event=>{finish();if(handle.hasPointerCapture(event.pointerId))handle.releasePointerCapture(event.pointerId);});
    handle.addEventListener('pointercancel',finish);
    handle.addEventListener('lostpointercapture',finish);
    handle.addEventListener('keydown',event=>{
      if(!['ArrowLeft','ArrowRight','ArrowUp','ArrowDown'].includes(event.key))return;
      event.preventDefault();event.stopPropagation();
      const step=event.shiftKey?100:10;
      notify({type:'rdev-browser-resize',...resizeViewport({...viewport,scaleX:1,scaleY:1},event.key==='ArrowLeft'?-step:event.key==='ArrowRight'?step:0,event.key==='ArrowUp'?-step:event.key==='ArrowDown'?step:0,axis)});
    });
    overlay.append(handle);
    return handle;
  });
  document.body.append(overlay);
  const findCanvas = root => {
    if(!root)return null;
    const direct=root.querySelector?.('canvas');if(direct)return direct;
    for(const element of root.querySelectorAll?.('*')??[]) {if(element.shadowRoot){const found=findCanvas(element.shadowRoot);if(found)return found;}}
    return null;
  };
  const position=()=>{
    handles.forEach((handle,i)=>{
      handle.setAttribute('aria-valuenow', String(i === 1 ? viewport.height : viewport.width));
      handle.setAttribute('aria-valuetext', i === 2 ? `${viewport.width} × ${viewport.height}` : String(i === 1 ? viewport.height : viewport.width));
    });
    const next=app.screencastView?.canvasElement??findCanvas(app.screencastView?.contentElement);
    if(next!==canvas){if(canvas)observer.unobserve(canvas);canvas=next;if(canvas)observer.observe(canvas);}
    const r=canvas?.getBoundingClientRect();
    for(const handle of handles)handle.style.display=r?.width&&r?.height?'block':'none';
    if(!r?.width||!r?.height)return;
    const right=Math.min(innerWidth-10,r.right),bottom=Math.min(innerHeight-10,r.bottom);
    const rects=[{left:right,top:r.top,width:10,height:r.height},{left:r.left,top:bottom,width:r.width,height:10},{left:right,top:bottom,width:12,height:12}];
    handles.forEach((handle,i)=>{for(const [key,value] of Object.entries(rects[i]))handle.style[key]=`${value}px`;});
  };
  const observer=new ResizeObserver(position);
  observer.observe(app.screencastView.contentElement);
  const mutations=new MutationObserver(position);
  mutations.observe(app.screencastView.contentElement,{childList:true,subtree:true});
  window.addEventListener('resize',position);
  document.addEventListener('scroll',position,true);
  position();
  return {
    set(value){viewport=value;position();},
    dispose(){observer.disconnect();mutations.disconnect();window.removeEventListener('resize',position);document.removeEventListener('scroll',position,true);if(frame)cancelAnimationFrame(frame);overlay.remove();},
  };
}
