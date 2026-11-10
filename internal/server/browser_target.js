(()=>{
 const matches=document.querySelectorAll(%s);
 if(matches.length!==1)throw Error(matches.length?'Selector is ambiguous; take a fresh snapshot':'Element not found; take a fresh snapshot');
 const e=matches[0];
 if(e.matches(':disabled')||e.getAttribute('aria-disabled')==='true')throw Error('Element is disabled');
 e.scrollIntoView({block:'center',inline:'center',behavior:'instant'});
 const r=e.getBoundingClientRect();
 const left=Math.max(0,r.left),right=Math.min(innerWidth,r.right),top=Math.max(0,r.top),bottom=Math.min(innerHeight,r.bottom);
 if(right<=left||bottom<=top||getComputedStyle(e).visibility==='hidden')throw Error('Element is not visible');
 const x=(left+right)/2,y=(top+bottom)/2,hit=document.elementFromPoint(x,y);
 if(!hit||!(hit===e||e.contains(hit)))throw Error('Element is covered; dismiss the overlay or take a fresh snapshot');
 return JSON.stringify({x:Math.floor(x),y:Math.floor(y)});
})()
