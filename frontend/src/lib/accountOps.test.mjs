import test from 'node:test'
import assert from 'node:assert/strict'
import { newQualityPlan, qualityBPSFromTemplate, formatQualityAction, CANDY_PROMPT } from './accountOps.ts'
test('source defaults require selected models and groups, restore opt-in',()=>{const p=newQualityPlan();assert.equal(p.model,'');assert.equal(p.judge.model_id,'');assert.equal(p.action,'remove_groups');assert.equal(p.auto_restore,false);assert.equal(p.cron,'*/30 * * * *');assert.equal(p.expected_answer,'21');assert.equal(p.prompt,CANDY_PROMPT)})
test('restored group action is accurately labeled',()=>{assert.equal(formatQualityAction('restored','remove_groups'),'已恢复原分组');assert.equal(formatQualityAction('restored','disable_scheduling'),'已恢复调度')})
test('new BPS rules inherit saved transport defaults while retaining explicit false',()=>{
 const template={auto_enable_on_degradation:true,all_models:false,models:['gpt-6-astra'],omit_unsupported_tools:true,ignore_encrypted_content:false,auto_disable_on_403:false,auto_recover_on_403:true,session_proxy:true,proxy_source:'ip_pool',cache_creation_as_input:true}
 const plan=newQualityPlan(template)
 assert.equal(plan.action,'enable_bps');assert.equal(plan.bps.failure_threshold,1)
 assert.equal(plan.bps.ignore_encrypted_content,false);assert.equal(plan.bps.auto_disable_on_403,false)
 assert.equal(plan.bps.omit_unsupported_tools,true);assert.equal(plan.bps.cache_creation_as_input,true)
 plan.bps.models.push('another');assert.deepEqual(template.models,['gpt-6-astra'])
 assert.equal(newQualityPlan({...template,auto_enable_on_degradation:false}).action,'remove_groups')
 assert.equal(qualityBPSFromTemplate(template).proxy_source,'ip_pool')
})
