import assert from 'node:assert/strict';
import test from 'node:test';
import { buildPluginAiPromptRegistry, expandAiPromptTemplate } from '../src/domain/ai/plugin-prompts.ts';
import { buildAiPromptSuggestions } from '../src/domain/ai/tool-command.ts';
const builtin={id:'review',kind:'prompt',title:'Review',body:'Review {{selection}}'};
const custom={id:'custom-0123456789abcdef0123456789abcdef',kind:'prompt',title:'My review',description:'Custom',body:'Check {{selection}}'};
const contribution=runtime=>({pluginId:'acme',pluginName:'Acme',version:'0.1.0',source:'installed',runtime:'process',activeInWorkspace:true,point:{fingerprint:'f',entries:[builtin],issues:[],runtime}});

test('saved runtime templates join slash suggestions without activating a plugin',()=>{
 const item=contribution({revision:1,entries:[custom]});
 const registry=buildPluginAiPromptRegistry([item]);
 assert.equal(registry.prompts.length,2);
 const suggestions=buildAiPromptSuggestions('/',[],registry.prompts.map(p=>({key:p.key,name:p.title,description:p.description,source:p.pluginName})));
 assert.ok(suggestions.some(s=>s.promptKey==='acme.'+custom.id));
 assert.equal(expandAiPromptTemplate(registry.prompts[1].body,{selection:'code'}).text,'Check code');
 assert.equal(buildPluginAiPromptRegistry([{...item,activeInWorkspace:false}]).prompts.length,0);
 assert.equal(buildPluginAiPromptRegistry([contribution({revision:2,entries:[]})]).prompts.length,1);
});

test('runtime overlays cannot inject skills, override builtins or use unsafe variables',()=>{
 const registry=buildPluginAiPromptRegistry([contribution({revision:1,entries:[{...custom,kind:'skill'},builtin,{...custom,body:'{{secret}}'}]})]);
 assert.deepEqual(registry.prompts.map(p=>p.id),['review']);
 assert.equal(registry.issues.length,3);
 const invalid=buildPluginAiPromptRegistry([contribution({error:'Saved data invalid',entries:[]})]);
 assert.ok(invalid.issues.some(i=>i.message==='Saved data invalid'));
});
