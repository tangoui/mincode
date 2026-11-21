import test from 'node:test';
import assert from 'node:assert/strict';
import { createAssociationsAPI } from '../../packages/plugin-sdk/node/runtime.mjs';
import { createFakeHost } from '../../packages/plugin-sdk/node/testing.mjs';
test('association SDK only requests trusted settings and bounded authorized reads',async()=>{
 const seen=[];const api=createAssociationsAPI(async(method,p)=>{seen.push([method,p]);return {};});
 await api.list();await api.request('seed');await api.read('a'.repeat(32),0,65536);
 assert.deepEqual(seen.map(v=>v[0]),['host.associations.list','host.associations.request','host.associations.read']);
 assert.throws(()=>api.request('../bad'));assert.throws(()=>api.read('a'.repeat(32),-1));assert.throws(()=>api.read('a'.repeat(32),0,65537));
 const host=createFakeHost();await assert.rejects(()=>host.handle('host.associations.list',{}));
 const granted=createFakeHost({capabilities:['system.associations']});assert.deepEqual(await granted.handle('host.associations.list',{}),{handlers:[]});await assert.rejects(()=>granted.handle('host.associations.read',{requestId:'forged'}));
});
