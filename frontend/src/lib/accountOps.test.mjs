import test from 'node:test'
import assert from 'node:assert/strict'
import { newQualityPlan, formatQualityAction, CANDY_PROMPT } from './accountOps.ts'
test('source defaults require selected models and groups, restore opt-in',()=>{const p=newQualityPlan();assert.equal(p.model,'');assert.equal(p.judge.model_id,'');assert.equal(p.action,'remove_groups');assert.equal(p.auto_restore,false);assert.equal(p.cron,'*/30 * * * *');assert.equal(p.expected_answer,'21');assert.equal(p.prompt,CANDY_PROMPT)})
test('restored group action is accurately labeled',()=>{assert.equal(formatQualityAction('restored','remove_groups'),'已恢复原分组');assert.equal(formatQualityAction('restored','disable_scheduling'),'已恢复调度')})
