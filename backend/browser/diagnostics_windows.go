//go:build windows

package browser

// 此脚本仅记录网页自身的有界诊断，不创建宿主消息通道或注入 desktop bridge。
const browserWindowsDiagnostics = `(()=>{
if(window.__mincodeDiagnostics)return;
const state={console:[],network:[],errors:[]};
const cleanUrl=value=>{try{const u=new URL(String(value),location.href);u.username='';u.password='';u.search='';u.hash='';return u.toString()}catch{return String(value).slice(0,500)}};
const text=value=>{try{return(typeof value==='string'?value:JSON.stringify(value)).slice(0,1000)}catch{return String(value).slice(0,1000)}};
const push=(list,item)=>{list.push(item);if(list.length>200)list.splice(0,list.length-200)};
for(const level of ['log','info','warn','error','debug']){const original=console[level]?.bind(console);if(original)console[level]=(...args)=>{push(state.console,{timestamp:Date.now(),level,message:args.map(text).join(' ')});original(...args)}}
addEventListener('error',event=>push(state.errors,{timestamp:Date.now(),type:'error',message:text(event.message||event.error),source:cleanUrl(event.filename||location.href),line:event.lineno||0,column:event.colno||0}),true);
addEventListener('unhandledrejection',event=>push(state.errors,{timestamp:Date.now(),type:'unhandledrejection',message:text(event.reason)}));
const nativeFetch=window.fetch?.bind(window);if(nativeFetch)window.fetch=async(input,init)=>{const started=performance.now(),method=String(init?.method||input?.method||'GET').toUpperCase(),url=cleanUrl(input?.url||input);try{const response=await nativeFetch(input,init);push(state.network,{timestamp:Date.now(),kind:'fetch',method,url,status:response.status,ok:response.ok,durationMs:Math.round(performance.now()-started)});return response}catch(error){push(state.network,{timestamp:Date.now(),kind:'fetch',method,url,status:0,ok:false,durationMs:Math.round(performance.now()-started),error:text(error)});throw error}};
const XHR=window.XMLHttpRequest;if(XHR){const open=XHR.prototype.open,send=XHR.prototype.send;XHR.prototype.open=function(method,url,...rest){this.__mincodeRequest={method:String(method||'GET').toUpperCase(),url:cleanUrl(url)};return open.call(this,method,url,...rest)};XHR.prototype.send=function(...args){const started=performance.now();this.addEventListener('loadend',()=>push(state.network,{timestamp:Date.now(),kind:'xhr',method:this.__mincodeRequest?.method||'GET',url:this.__mincodeRequest?.url||'',status:this.status||0,ok:this.status>=200&&this.status<400,durationMs:Math.round(performance.now()-started)}),{once:true});return send.apply(this,args)}}
Object.defineProperty(window,'__mincodeDiagnostics',{value:state,enumerable:false,configurable:false,writable:false});
})();`
