(()=>{
 const selector=e=>{if(e===document.body)return 'body';if(e.id&&document.querySelectorAll('#'+CSS.escape(e.id)).length===1)return '#'+CSS.escape(e.id);const path=[];while(e&&e.nodeType===1&&e!==document.body){let i=1,p=e;while(p=p.previousElementSibling)if(p.tagName===e.tagName)i++;path.unshift(e.tagName.toLowerCase()+':nth-of-type('+i+')');e=e.parentElement}return 'body > '+path.join(' > ')};
 const label=e=>e.getAttribute('aria-label')||(e.getAttribute('aria-labelledby')||'').split(/\s+/).map(id=>document.getElementById(id)?.textContent||'').join(' ').trim()||[...(e.labels||[])].map(l=>l.textContent).join(' ').trim()||e.getAttribute('placeholder')||e.innerText||e.getAttribute('title')||'';
 const elements=[...document.querySelectorAll('a,button,input,textarea,select,[role="button"],[role="checkbox"],[role="combobox"],[role="tab"],[contenteditable="true"]')].filter(e=>e.getClientRects().length&&getComputedStyle(e).visibility!=='hidden').slice(0,150).map(e=>({
 selector:selector(e),role:e.getAttribute('role')||e.tagName.toLowerCase(),label:label(e).slice(0,200),type:e.getAttribute('type')||undefined,
 disabled:e.matches(':disabled')||e.getAttribute('aria-disabled')==='true',
 value:e.type==='password'?undefined:typeof e.value==='string'?e.value.slice(0,200):undefined,
 checked:typeof e.checked==='boolean'&&['checkbox','radio'].includes(e.type)?e.checked:undefined,
 options:e instanceof HTMLSelectElement?[...e.options].slice(0,30).map(o=>({value:o.value.slice(0,200),label:o.text.slice(0,200),selected:o.selected,disabled:o.disabled||!!o.parentElement.disabled})):undefined
 }));
 const snapshot={url:location.href.slice(0,2048),title:document.title.slice(0,200),width:innerWidth,height:innerHeight,text:(document.body?.innerText||'').slice(0,6000),elements};
 while(new TextEncoder().encode(JSON.stringify(snapshot)).length>10000){if(snapshot.elements.length)snapshot.elements.pop();else snapshot.text=snapshot.text.slice(0,Math.floor(snapshot.text.length/2));if(!snapshot.text&&!snapshot.elements.length)break}
 return JSON.stringify(snapshot);
})()
