import test from "node:test";
import assert from "node:assert/strict";
import { normalizePluginPublishRecord, releaseNotesExcerpt, metadataChangeRows } from "../src/domain/plugins/publish-summary.ts";
const record={id:"pub",status:"pending_confirmation",step:"lookup",pluginId:"demo.plugin",pluginName:"Demo",version:"1.0.0",releaseDir:"/project/release",account:"dev@example.com",newPlugin:true,existingDraft:false,submit:true,releaseNotes:"notes",expiresAt:"2026-11-04T10:00:00Z",packages:[{os:"darwin",arch:"arm64",file:"mac.min",size:20,sha256:"a".repeat(64),state:"pending",percent:0}],metadataChanges:[]};
test("publish normalizes fixed public fields and refuses malformed records",()=>{
 assert.equal(normalizePluginPublishRecord({...record,status:"upload_now"}),null);
 assert.equal(normalizePluginPublishRecord({...record,packages:{}}),null);
 const parsed=normalizePluginPublishRecord({...record,token:"secret",remember:true,packages:[{...record.packages[0],token:"secret"}],result:{releaseId:1,reviewStatus:"pending",developerUrl:"javascript:alert(1)",uploaded:[],skipped:[],token:"secret"}});
 assert.ok(parsed);assert.equal(JSON.stringify(parsed).includes("secret"),false);assert.equal("remember" in parsed,false);assert.equal(parsed.result.developerUrl,"");
 assert.equal(normalizePluginPublishRecord({...record,packages:[{...record.packages[0],size:Infinity}]}),null);
 assert.equal(normalizePluginPublishRecord({...record,metadataChanges:[{field:"token",old:"secret",new:"secret"}]}),null);
});
test("release notes are bounded by Unicode characters and include ellipsis",()=>{assert.equal([...releaseNotesExcerpt("中".repeat(300),280)].length,281);assert.equal(releaseNotesExcerpt("short",280),"short")});
test("metadata changes display tags and bounded README",()=>{const rows=metadataChangeRows([{field:"tags",old:["a","b"],new:["c"]},{field:"readme",old:"中".repeat(250),new:"new"}]);assert.equal(rows[0].old,"a, b");assert.equal([...rows[1].old].length,201)});
test("preparing and early terminal records can have no staged packages; confirmation cannot",()=>{
 for(const status of ["preparing","failed","canceled","expired"]) assert.equal(normalizePluginPublishRecord({...record,status,packages:[]})?.status,status);
 for(const status of ["pending_confirmation","awaiting_login","running","succeeded"]) assert.equal(normalizePluginPublishRecord({...record,status,packages:[]}),null);
});
