import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { externalCandidate, validExternalContributions } from '../src/domain/plugins/external-open.ts';
import { parsePluginUIOpenRequest } from '../src/domain/plugin-ui/protocol.ts';
const read = p => readFile(new URL(p, import.meta.url),'utf8');
test('external candidates and view requests reject malformed data',()=>{
 const c={pluginId:'download-manager',pluginName:'Downloader',handlerId:'magnet',type:'magnet',view:'main',fingerprint:'hash'};
 assert.deepEqual(externalCandidate(c),c); assert.equal(externalCandidate({...c,view:''}),undefined);
 assert.ok(parsePluginUIOpenRequest({source:'external',requestId:'a',entry:'magnet',params:{url:'magnet:?xt=abc'}}));
 assert.equal(parsePluginUIOpenRequest({source:'external',from:'forged',requestId:'a',entry:'magnet'}),undefined);
 assert.deepEqual(validExternalContributions([{pluginId:'download-manager',point:{entries:[{id:'magnet',type:'magnet',view:'main'},null,{}]}}]),['download-manager/magnet']);
});
test('association dialogs remain trusted, lazy and keep downloads behind confirmation',async()=>{
 const facade=await read('../src/services/plugin-associations.ts'),runtime=await read('../src/services/plugin-associations-runtime.ts');
 assert.match(facade,/import\("@\/services\/plugin-associations-runtime"\)/);
 assert.match(facade,/event\?\.rejected/);
 assert.match(runtime,/notifyExternalOpenRejected/);
 assert.match(runtime,/ElMessageBox.confirm/);assert.match(runtime,/const second = await resolve\(id\)/);
 assert.match(runtime,/v.fingerprint === c.fingerprint/);
 assert.match(runtime,/plugin_host_external_deliver/);assert.doesNotMatch(runtime,/executePluginCommand|shell_execute|window.open/);
 assert.match(runtime,/!first.remembered \|\|/);
});
test('OS association registration never claims default UserChoice',async()=>{
 const platform=await read('../../backend/associations/platform_windows.go');
 assert.doesNotMatch(platform,/SetStringValue\("Hash"|SetStringValue\("ProgId"/);
});
