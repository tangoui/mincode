(() => {
 const format=__ARG0__,maxChars=__ARG1__,snapshotId=__ARG2__,ref=__ARG3__;
 const markdown=format!=='text';
 const limit=Math.max(1,Math.floor(Number(maxChars))||20000);
 // Same 300-code-point cut as the snapshot title.
 const cut=(text,max)=>{if(text.length<=max)return text;let out='',n=0;for(const ch of text){if(n++===max)break;out+=ch}return out};
 // A ref restricts extraction to that element: same lookup, messages and sensitive-input guard as inspect.js.
 let scope=null;
 if(ref!==''||snapshotId!==''){
  if(ref===''||snapshotId==='')throw new Error('extract_content needs both snapshotId and ref, or neither');
  const state=window.__mincodeAiRefs;if(!state||state.snapshotId!==snapshotId)throw new Error('stale snapshot; call browser.get_page_snapshot again');
  const el=state.refs.get(ref);if(!el||!el.isConnected||!el.ownerDocument.defaultView)throw new Error('element reference is stale');
  const type=String(el.getAttribute('type')||'').toLowerCase();if(type==='password'||type==='file')throw new Error('sensitive input interaction is blocked');
  scope=el;
 }
 // Never rendered: code, media, and every form control (form values are not page text).
 const always=new Set(['script','style','noscript','template','head','title','meta','link','base','svg','math','canvas','object','embed','audio','video','source','track','map','area','input','textarea','select','option','optgroup','datalist']);
 // Page chrome, dropped inside the content but kept when it is the ref itself.
 const noise=new Set(['nav','footer','aside','button']);
 const noiseRoles=new Set(['navigation','contentinfo','complementary']);
 const noiseSelector='nav,footer,aside,[role=navigation],[role=contentinfo],[role=complementary]';
 const blockTags=new Set(['address','article','blockquote','caption','center','dd','details','dialog','dir','div','dl','dt','fieldset','figcaption','figure','form','h1','h2','h3','h4','h5','h6','header','hgroup','hr','legend','li','main','menu','ol','p','pre','search','section','summary','table','tbody','td','tfoot','th','thead','tr','ul']);
 const inlineTags=new Set(['a','img','br','iframe','frame']);
 // Elements of a same-origin iframe belong to that frame's window: read their style there.
 const styles=new Map();
 const styleOf=(el)=>{if(styles.has(el))return styles.get(el);const win=el.ownerDocument&&el.ownerDocument.defaultView;let s=null;try{s=win?win.getComputedStyle(el):null}catch{}styles.set(el,s);return s};
 const collapse=(text)=>String(text||'').replace(/\s+/g,' ').trim();
 // Iframes and shadow roots nest at most this deep (as in snapshot.js), elements at most maxNesting; deeper content is skipped and the result marked truncated.
 const maxDepth=32,maxNesting=400,maxColumns=100;
 // Walking stops once twice the requested length of text has been produced.
 const budget=limit*2;let emitted=0,exhausted=false,limited=false;
 const count=(n)=>{emitted+=n;if(emitted>budget)exhausted=true};
 const shownCache=new Map();
 // el and its ancestors in its own tree are not hidden (hidden attribute or display:none).
 // Iterative, so a very deep page cannot overflow the stack: walk up to a known ancestor, then fill the cache downwards.
 const shownInTree=(el)=>{const path=[];let e=el,shown=true;for(;e&&e.nodeType===1;e=e.parentElement){if(shownCache.has(e)){shown=shownCache.get(e);break}path.push(e)}for(let i=path.length-1;i>=0;i--){const s=styleOf(path[i]);shown=shown&&!path[i].hidden&&!(s&&s.display==='none');shownCache.set(path[i],shown)}return shown};
 // The child context of el, or null when el and its subtree are skipped. root keeps a ref that is itself page chrome.
 const enter=(el,c,root=false)=>{
  const tag=el.localName;
  if(always.has(tag)||(tag==='dialog'&&!el.open))return null;
  if(!root&&(noise.has(tag)||noiseRoles.has(String(el.getAttribute('role')||'').toLowerCase())))return null;
  if(el.hidden)return null;
  const s=styleOf(el);if(s&&s.display==='none')return null;
  if(c.nest>=maxNesting){limited=true;return null}
  let depth=c.depth;if(el.shadowRoot){if(depth>=maxDepth){limited=true;return null}depth++}
  return {nest:c.nest+1,visible:s&&s.visibility?s.visibility!=='hidden'&&s.visibility!=='collapse':c.visible,pre:c.pre||tag==='pre',depth};
 };
 // The flat tree: an open shadow root replaces its host's children and a slot shows its assigned nodes; a closed <details> shows only its summary.
 const kids=(el)=>{
  if(el.shadowRoot)return el.shadowRoot.childNodes;
  if(el.localName==='slot'&&typeof el.assignedNodes==='function'){const assigned=el.assignedNodes();if(assigned.length)return assigned}
  if(el.localName==='details'&&!el.open)return Array.from(el.children).filter((child)=>child.localName==='summary').slice(0,1);
  return el.childNodes;
 };
 const isBlock=(el)=>{const tag=el.localName;if(inlineTags.has(tag))return false;if(blockTags.has(tag))return true;const s=styleOf(el);return Boolean(s&&/^(block|flex|grid|table|list-item|flow-root)/.test(s.display||''))};
 // Output: finished blocks, and the inline run of the paragraph being built.
 let blocks=[],run='';
 const para=(text)=>text.split('\n').map(collapse).filter(Boolean).join('\n');
 const flush=()=>{const p=para(run);if(p)blocks.push(p);run=''};
 const captureRaw=(fn)=>{const saved=[blocks,run];blocks=[];run='';fn();const got={blocks,run};[blocks,run]=saved;return got};
 const capture=(fn)=>captureRaw(()=>{fn();flush()}).blocks;
 // Links: absolute http(s) URL without credentials, query or fragment (as snapshot hrefs), cut to 300 characters.
 const linkHref=(el)=>{let u;try{u=new URL(el.href)}catch{return ''}if(u.protocol!=='http:'&&u.protocol!=='https:')return '';u.username='';u.password='';u.search='';u.hash='';const href=u.toString().replace(/[()]/g,(ch)=>ch==='('?'%28':'%29');return href.length>300?href.slice(0,300)+'…':href};
 // Wraps el's inline content with Markdown (keeping the surrounding spaces); block content inside becomes one wrapped paragraph.
 const wrapInline=(el,c,wrap)=>{
  const got=captureRaw(()=>visitChildren(el,c));
  if(!got.blocks.length){const t=collapse(got.run);if(!t){run+=got.run;return}run+=(/^\s/.test(got.run)?' ':'')+wrap(t)+(/\s$/.test(got.run)?' ':'');return}
  const t=collapse([...got.blocks.map((b)=>b.replace(/^#{1,6} /,'')),got.run].join(' '));flush();if(t)blocks.push(wrap(t));
 };
 const frames=[],seenFrames=new Set();
 const queueFrame=(el,c)=>{if(!c.visible)return;if(c.depth>=maxDepth){limited=true;return}let doc=null;try{doc=el.contentDocument}catch{}if(doc&&!seenFrames.has(doc)){seenFrames.add(doc);frames.push({doc,depth:c.depth+1})}};
 const visit=(node,c)=>{
  if(exhausted)return;
  if(node.nodeType===3){if(!c.visible)return;const t=c.pre?node.data:node.data.replace(/\s+/g,' ');if(/\S/.test(t))count(t.length);run+=t;return}
  if(node.nodeType!==1)return;
  const ec=enter(node,c);if(ec)element(node,ec);
 };
 const visitChildren=(el,c)=>{for(const child of Array.from(kids(el))){if(exhausted)return;visit(child,c)}};
 const list=(el,c)=>{
  const ordered=el.localName==='ol';const start=parseInt(el.getAttribute('start')||'',10);let n=ordered&&Number.isFinite(start)?start:1;
  const items=[];
  for(const child of Array.from(kids(el))){
   if(exhausted)break;
   if(child.nodeType!==1)continue;
   const ic=enter(child,c);if(!ic)continue;
   const li=child.localName==='li';
   const body=capture(()=>li?visitChildren(child,ic):element(child,ic)).join('\n');
   const marker=li&&markdown?(ordered?n+'. ':'- '):'';if(li)n++;
   const pad=marker?' '.repeat(marker.length):'  ';
   if(body)items.push(body.split('\n').map((line,i)=>i===0&&li?marker+line:(line?pad+line:'')).join('\n'));
  }
  return items.join('\n');
 };
 const table=(el,c)=>{
  const rows=Array.from(el.rows||[]);
  const role=String(el.getAttribute('role')||'').toLowerCase();
  // Layout tables (presentation role, or holding other tables) are read as blocks, cell by cell.
  if(role==='presentation'||role==='none'||el.querySelector('table')){for(const row of rows){const rc=enter(row,c);if(!rc)continue;for(const cell of Array.from(row.cells||[])){if(exhausted)return;const cc=enter(cell,rc);if(cc){flush();visitChildren(cell,cc);flush()}}}return}
  if(el.caption){const cc=enter(el.caption,c);if(cc){flush();visitChildren(el.caption,cc);flush()}}
  const grid=[];
  for(const row of rows){
   if(exhausted)break;
   const rc=enter(row,c);if(!rc)continue;
   const cells=[];
   for(const cell of Array.from(row.cells||[])){
    const cc=enter(cell,rc);if(!cc)continue;
    const text=collapse(capture(()=>visitChildren(cell,cc)).join(' '));
    cells.push(markdown?text.replace(/\|/g,'\\|'):text);
    for(let i=1;i<Math.min(Number(cell.colSpan)||1,maxColumns);i++)cells.push('');
   }
   if(cells.length>maxColumns){cells.length=maxColumns;limited=true}
   if(cells.some(Boolean)){grid.push(cells);count(cells.length*3)}
  }
  if(!grid.length)return;
  if(!markdown){blocks.push(grid.map((cells)=>cells.join('\t')).join('\n'));return}
  const width=grid.reduce((w,cells)=>Math.max(w,cells.length),0);
  const line=(cells)=>'| '+Array.from({length:width},(_,i)=>cells[i]??'').join(' | ')+' |';
  blocks.push([line(grid[0]),'| '+Array(width).fill('---').join(' | ')+' |',...grid.slice(1).map(line)].join('\n'));
 };
 const element=(el,c)=>{
  const tag=el.localName;
  if(tag==='br'){run+='\n';return}
  if(tag==='img'){const alt=c.visible?collapse(el.getAttribute('alt')):'';if(alt){count(alt.length);run+=alt}return}
  if(tag==='iframe'||tag==='frame'){queueFrame(el,c);return}
  if(/^h[1-6]$/.test(tag)){const text=collapse(capture(()=>visitChildren(el,c)).join(' '));flush();if(text)blocks.push((markdown?'#'.repeat(Number(tag[1]))+' ':'')+text);return}
  if(tag==='ul'||tag==='ol'||tag==='menu'||tag==='dir'){flush();const text=list(el,c);if(text)blocks.push(text);return}
  if(tag==='table'){flush();table(el,c);return}
  if(tag==='pre'){flush();const got=captureRaw(()=>visitChildren(el,c));const text=[...got.blocks,got.run].join('\n').replace(/\s+$/,'');if(!text.trim())return;let fence='```';while(text.includes(fence))fence+='`';blocks.push(markdown?fence+'\n'+text+'\n'+fence:text);return}
  if(tag==='blockquote'){flush();const text=capture(()=>visitChildren(el,c)).join('\n\n');if(text)blocks.push(markdown?text.split('\n').map((line)=>line?'> '+line:'>').join('\n'):text);return}
  if(tag==='hr'){flush();if(markdown)blocks.push('---');return}
  if(markdown&&!c.pre){
   if(tag==='a'){const href=linkHref(el);if(href){wrapInline(el,c,(t)=>'['+t.replace(/[[\]\\]/g,'\\$&')+']('+href+')');return}}
   if(tag==='strong'||tag==='b'){wrapInline(el,c,(t)=>'**'+t+'**');return}
   if(tag==='em'||tag==='i'){wrapInline(el,c,(t)=>'*'+t+'*');return}
   if(tag==='code'||tag==='kbd'||tag==='samp'){wrapInline(el,c,(t)=>t.includes('`')?'`` '+t+' ``':'`'+t+'`');return}
  }
  const block=isBlock(el);
  if(block)flush();
  visitChildren(el,c);
  if(block)flush();
 };
 // Main content: the first <main> or [role=main], else the outermost <article>s, else the block with the most non-link paragraph text.
 const densest=(body)=>{
  const scores=new Map();let best=body,bestScore=0;
  const add=(el,score)=>{if(!el||el.nodeType!==1)return;const total=(scores.get(el)||0)+score;scores.set(el,total);if(total>bestScore){bestScore=total;best=el}};
  for(const block of Array.from(body.querySelectorAll('p,pre,blockquote,li,td,dd,h1,h2,h3,h4,h5,h6'))){
   if(block.parentElement&&block.parentElement.closest('p,pre,blockquote,li,td,dd'))continue;
   if(block.closest(noiseSelector)||!shownInTree(block))continue;
   const length=collapse(block.textContent).length;if(length<20)continue;
   let links=0;for(const a of Array.from(block.querySelectorAll('a')))links+=collapse(a.textContent).length;
   const score=length*(1-Math.min(1,links/length));
   add(block.parentElement,score);add(block.parentElement&&block.parentElement.parentElement,score/2);
  }
  if(/^(tr|tbody|thead|tfoot)$/.test(best.localName)&&best.closest('table'))best=best.closest('table');
  return best;
 };
 const pickRoots=(doc)=>{
  const body=doc.body||doc.documentElement;if(!body)return [];
  const usable=(el)=>shownInTree(el)&&/\S/.test(el.textContent||'');
  const main=Array.from(doc.querySelectorAll('main,[role=main]')).find(usable);if(main)return [main];
  const articles=Array.from(doc.querySelectorAll('article')).filter((a)=>!(a.parentElement&&a.parentElement.closest('article'))&&usable(a));if(articles.length)return articles;
  return [densest(body)];
 };
 const renderRoot=(root,depth)=>{if(exhausted||!shownInTree(root))return;const c=enter(root,{nest:0,visible:true,pre:false,depth},true);if(!c)return;flush();element(root,c);flush()};
 for(const root of scope?[scope]:pickRoots(document))renderRoot(root,0);
 // Same-origin iframe documents follow the top document, in the order they were met.
 for(let i=0;i<frames.length&&!exhausted;i++)for(const root of pickRoots(frames[i].doc))renderRoot(root,frames[i].depth);
 let content=blocks.join('\n\n'),truncated=exhausted||limited;
 if(content.length>limit){let end=limit;const code=content.charCodeAt(end-1);if(code>=0xd800&&code<=0xdbff)end--;content=content.slice(0,end);truncated=true}
 const rawTitle=String(document.title||''),title=cut(rawTitle,300);
 const result={title,url:location.href,content,truncated};
 if(title!==rawTitle)result.titleTruncated=true;
 return result;
})()
