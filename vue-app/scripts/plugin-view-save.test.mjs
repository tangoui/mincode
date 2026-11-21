import test from 'node:test';
import assert from 'node:assert/strict';
import { registerPluginViewSave, flushPluginView } from '../src/services/plugin-view-save.ts';

test('sandbox transport acknowledges only after save succeeds and reports rejection',async()=>{
 const {readFile}=await import('node:fs/promises');const vm=await import('node:vm');
 const theme=(await readFile(new URL('../src/plugin-ui/theme-tokens.js',import.meta.url),'utf8')).replaceAll('export function','function');
 const transport=(await readFile(new URL('../src/plugin-ui/transport.js',import.meta.url),'utf8')).replace(/^import .*theme-tokens.*;$/m,'').replace('export function','function');
 const listeners=new Map(),parent={postMessage(){}},sent=[];
 const context=vm.createContext({URLSearchParams,location:{hash:'#mincodeSession=s&nonce=n&generation=1'},parent,setTimeout,clearTimeout,addEventListener:(type,fn)=>listeners.set(type,fn),removeEventListener:type=>listeners.delete(type)});
 vm.runInContext(theme+transport,context);
 let finish;let fail=false;
 const api=context.createTransport({onReady(){},onError(){},applyContext(){},onSave:()=>fail?Promise.reject(Error('failed')):new Promise(resolve=>{finish=resolve})});
 const port={start(){},close(){},postMessage(value){sent.push(value)}};
 listeners.get('message')({source:parent,data:{type:'mincode.host.connect',protocolVersion:1,sessionId:'s',nonce:'n',generation:1,features:{viewSave:1}},ports:[port]});
 assert.equal(sent[0].type,'mincode.plugin.view.save-subscribe');
 port.onmessage({data:{type:'mincode.host.view.save-request',requestId:'one'}});
 await Promise.resolve();assert.equal(sent.length,1);finish();await new Promise(resolve=>setImmediate(resolve));
 assert.equal(sent[1].ok,true);assert.equal(sent[1].requestId,'one');
 fail=true;port.onmessage({data:{type:'mincode.host.view.save-request',requestId:'two'}});
 await new Promise(resolve=>setImmediate(resolve));assert.equal(sent[2].ok,false);api.close();
});

test('closing a view awaits its real save acknowledgement',async()=>{
 let complete;let saved=false;
 const remove=registerPluginViewSave('tab',()=>new Promise(resolve=>{complete=()=>{saved=true;resolve()}}));
 let closed=false;const closing=flushPluginView('tab').then(()=>{closed=true});
 await Promise.resolve();assert.equal(closed,false);assert.equal(saved,false);
 complete();await closing;assert.equal(closed,true);remove();
});
test('save failure vetoes closure; removing a frame unregisters it',async()=>{
 const remove=registerPluginViewSave('tab',async()=>{throw Error('save denied')});
 await assert.rejects(flushPluginView('tab'),/save denied/);remove();await flushPluginView('tab');
});
