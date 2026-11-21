(() => {
 const snapshotId=__ARG0__,ref=__ARG1__;
 // Same ref lookup and sensitive-input guard as resolveElement/guardSensitive in actions.js.
 const state=window.__mincodeAiRefs;if(!state||state.snapshotId!==snapshotId)throw new Error('stale snapshot; call browser.get_page_snapshot again');
 const el=state.refs.get(ref);if(!el||!el.isConnected||!el.ownerDocument.defaultView)throw new Error('element reference is stale');
 const type=String(el.getAttribute('type')||'').toLowerCase();if(type==='password'||type==='file')throw new Error('sensitive input interaction is blocked');
 // rect is in top-viewport coordinates: add the content-box offset of each enclosing same-origin iframe, as snapshot.js does.
 const r=el.getBoundingClientRect();const px=(value)=>parseFloat(value)||0;
 const frames=state.frames instanceof Map?state.frames:new Map();let x=r.x,y=r.y;
 for(let doc=el.ownerDocument,frame=frames.get(doc);frame;doc=frame.ownerDocument,frame=frames.get(doc)){const fr=frame.getBoundingClientRect(),s=(frame.ownerDocument.defaultView||window).getComputedStyle(frame);x+=fr.left+px(s.borderLeftWidth)+px(s.paddingLeft);y+=fr.top+px(s.borderTopWidth)+px(s.paddingTop)}
 return {ref,tag:el.tagName.toLowerCase(),role:el.getAttribute('role')||'',name:String(el.getAttribute('aria-label')||el.getAttribute('title')||el.innerText||el.textContent||'').replace(/\s+/g,' ').trim().slice(0,1000),type:type||undefined,placeholder:el.getAttribute('placeholder')||'',disabled:Boolean(el.disabled)||el.getAttribute('aria-disabled')==='true',checked:typeof el.checked==='boolean'?el.checked:undefined,rect:{x:Math.round(x),y:Math.round(y),width:Math.round(r.width),height:Math.round(r.height)}};
})()
