(() => {
 const maxText=__ARG0__,maxElements=__ARG1__;
 // Elements of a same-origin iframe belong to that frame's window: read their style there.
 const styleOf=(el)=>(el.ownerDocument.defaultView||window).getComputedStyle(el);
 const shown=(el,r)=>{const s=styleOf(el);return s.visibility!=='hidden'&&s.display!=='none'&&r.width>0&&r.height>0};
 const name=(el)=>String(el.getAttribute('aria-label')||el.getAttribute('title')||el.getAttribute('alt')||el.innerText||el.textContent||el.getAttribute('placeholder')||'').replace(/\s+/g,' ').trim().slice(0,300);
 // File inputs are listed even when hidden (pages usually hide them behind a styled button; a click still opens the chooser), by existence only: never value or file names.
 const fileItem=(el,ref)=>({ref,tag:'input',role:el.getAttribute('role')||'button',name:String(el.getAttribute('aria-label')||el.getAttribute('title')||[...(el.labels||[])].map((label)=>label.innerText||label.textContent||'').join(' ')||'').replace(/\s+/g,' ').trim().slice(0,300),type:'file',disabled:Boolean(el.disabled)||el.getAttribute('aria-disabled')==='true',accept:String(el.getAttribute('accept')||''),multiple:Boolean(el.multiple),selectedCount:el.files?el.files.length:0});
 // Same option label, 50-option limit and 300-code-point cut as select_option in actions.js, which also accepts a cut label or value as an exact 300-code-point prefix.
 const maxOptions=50,maxOptionText=300;
 const optionLabel=(o)=>String(o.label||o.textContent||'').replace(/\s+/g,' ').trim();
 const cut=(text)=>{if(text.length<=maxOptionText)return text;let out='',n=0;for(const ch of text){if(n++===maxOptionText)break;out+=ch}return out};
 const optionItem=(o)=>{const value=String(o.value),label=optionLabel(o),item={value:cut(value)};if(item.value!==value)item.valueTruncated=true;item.label=cut(label);if(item.label!==label)item.labelTruncated=true;item.selected=Boolean(o.selected);item.disabled=Boolean(o.disabled);return item};
 const state=window.__mincodeAiRefs||(window.__mincodeAiRefs={snapshotId:'',refs:new Map()});
 const snapshotId=(crypto.randomUUID?crypto.randomUUID():String(Date.now())+Math.random());state.snapshotId=snapshotId;state.refs=new Map();
 // frames maps each same-origin iframe document to its iframe element, so inspect can translate boxes to the top viewport.
 state.frames=new Map();
 const selectors='a,button,input:not([type=password]),textarea,select,summary,[role],[contenteditable=true]';
 const scrolling=/^(auto|scroll|overlay)$/;
 const px=(value)=>parseFloat(value)||0;
 // A frame context: origin is the top-viewport position of the document's (0,0); clip is the visible part of its viewport in top coordinates; path is the frame path ('' for the top document); shadow marks open shadow trees; counter numbers the document's iframes; depth counts the iframes and shadow roots entered.
 const top={origin:{x:0,y:0},clip:{left:0,top:0,right:window.innerWidth,bottom:window.innerHeight},path:'',shadow:false,counter:{n:0},depth:0};
 // Iframes and shadow roots nest at most this deep; deeper ones are skipped and the snapshot is marked truncated.
 const maxDepth=32;let depthLimited=false;
 const inViewport=(r,ctx)=>{const left=ctx.origin.x+r.left,t=ctx.origin.y+r.top;return left<ctx.clip.right&&left+r.width>ctx.clip.left&&t<ctx.clip.bottom&&t+r.height>ctx.clip.top};
 // The iframe's content box, which is the child document's viewport, in top-viewport coordinates.
 const frameContext=(iframe,r,ctx,path)=>{const s=styleOf(iframe);const left=ctx.origin.x+r.left+px(s.borderLeftWidth)+px(s.paddingLeft),t=ctx.origin.y+r.top+px(s.borderTopWidth)+px(s.paddingTop),right=ctx.origin.x+r.right-px(s.borderRightWidth)-px(s.paddingRight),bottom=ctx.origin.y+r.bottom-px(s.borderBottomWidth)-px(s.paddingBottom);return {origin:{x:left,y:t},clip:{left:Math.max(left,ctx.clip.left),top:Math.max(t,ctx.clip.top),right:Math.min(right,ctx.clip.right),bottom:Math.min(bottom,ctx.clip.bottom)},path,shadow:false,counter:{n:0},depth:ctx.depth+1}};
 // scheme://host:port of the iframe's src only; empty for about:blank, srcdoc, data: and unparsable sources.
 const frameOrigin=(iframe)=>{if(iframe.hasAttribute('srcdoc'))return '';const src=String(iframe.src||'');if(!src)return '';try{const origin=new URL(src).origin;return origin==='null'?'':origin}catch{return ''}};
 // The page itself (html, body, scrolling element) scrolls through browser.scroll without containerRef.
 const scrollable=(el)=>{const doc=el.ownerDocument;if(el===doc.documentElement||el===doc.body||el===doc.scrollingElement)return false;const tall=el.scrollHeight>el.clientHeight,wide=el.scrollWidth>el.clientWidth;if(!tall&&!wide)return false;const s=styleOf(el);return (tall&&scrolling.test(s.overflowY))||(wide&&scrolling.test(s.overflowX))};
 const elements=[],texts=[String(document.body?.innerText||'')];let index=0,truncatedElements=false;
 // Lists el unless maxElements is reached (counted across all frames); build gets the new ref. frame and shadow locate it (file inputs too).
 const list=(el,ctx,build)=>{if(elements.length>=maxElements){truncatedElements=true;return}const ref='e'+(++index);state.refs.set(ref,el);const item=build(ref);if(ctx.path)item.frame=ctx.path;if(ctx.shadow)item.shadow=true;elements.push(item)};
 const visit=(el,ctx)=>{
  const tag=el.localName;
  if(tag==='iframe'||tag==='frame'){
   const position=ctx.counter.n++;const r=el.getBoundingClientRect();if(!shown(el,r))return;
   let doc=null;try{doc=el.contentDocument}catch{}
   // Unreadable means cross-origin (or sandboxed): listed, never entered.
   if(!doc){if(!truncatedElements)list(el,ctx,(ref)=>({ref,tag,crossOrigin:true,origin:frameOrigin(el),inViewport:inViewport(r,ctx)}));return}
   state.frames.set(doc,el);texts.push(String(doc.body?.innerText||''));
   walk(doc,frameContext(el,r,ctx,ctx.path?ctx.path+'.'+position:String(position)));return;
  }
  if(truncatedElements)return;
  const type=String(el.getAttribute('type')||'').toLowerCase();
  if(type==='password'||(tag==='input'&&type==='hidden'))return;
  const candidate=el.matches(selectors);
  if(candidate&&type==='file'){list(el,ctx,(ref)=>fileItem(el,ref));return}
  const scrolls=scrollable(el);
  if(!candidate&&!scrolls)return;
  const r=el.getBoundingClientRect();if(!shown(el,r))return;
  list(el,ctx,(ref)=>{
   const role=el.getAttribute('role')||(candidate?({A:'link',BUTTON:'button',INPUT:'textbox',TEXTAREA:'textbox',SELECT:'combobox',SUMMARY:'button'}[el.tagName]||''):'');
   const item={ref,tag:el.tagName.toLowerCase(),role,name:name(el)};
   if(candidate){item.type=type||undefined;item.disabled=Boolean(el.disabled)||el.getAttribute('aria-disabled')==='true';item.checked=typeof el.checked==='boolean'?el.checked:undefined}
   if(candidate&&el.tagName==='A'&&el.href){try{const u=new URL(el.href);u.username='';u.password='';u.search='';u.hash='';const href=u.toString();item.href=href.slice(0,300);if(href.length>300)item.hrefTruncated=true}catch{}}
   if(candidate&&el.tagName==='SELECT'){const options=Array.from(el.options||[]);item.options=options.slice(0,maxOptions).map(optionItem);if(options.length>maxOptions)item.optionsTruncated=true}
   if(scrolls)item.scrollable=true;
   item.inViewport=inViewport(r,ctx);
   return item;
  });
 };
 // Document order; an open shadow root's contents follow its host. Closed shadow roots are unreachable.
 const walk=(root,ctx)=>{if(ctx.depth>maxDepth){depthLimited=true;return}for(const el of root.querySelectorAll('*')){visit(el,ctx);if(el.shadowRoot)walk(el.shadowRoot,{...ctx,shadow:true,depth:ctx.depth+1})}};
 walk(document,top);
 const allText=texts.filter((t)=>t.trim()!=='').join('\n\n');
 const text=allText.replace(/\n{3,}/g,'\n\n').slice(0,maxText);
 // The title is cut to 300 code points like option labels (extract.js does the same).
 const rawTitle=String(document.title||''),title=cut(rawTitle);
 const result={snapshotId,url:location.href,title,readyState:document.readyState,text,elements,truncatedText:allText.length>maxText,truncatedElements:truncatedElements||depthLimited};
 if(title!==rawTitle)result.titleTruncated=true;
 return result;
})()
