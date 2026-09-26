import assert from 'node:assert/strict';
import test from 'node:test';
import {accountOpsEventKey,isBasispointsOpsEvent} from './accountOps.ts';
test('event identity keeps credential generations separate across pagination',()=>{
 const events=[{account_id:4,kind:'auto_403_disabled',credential_generation:1},{account_id:4,kind:'auto_403_disabled',credential_generation:2}];
 assert.notEqual(accountOpsEventKey(events[0]),accountOpsEventKey(events[1]));
 assert.equal(new Map([...events,...events].map(e=>[accountOpsEventKey(e),e])).size,2);
 assert.equal(accountOpsEventKey({account_id:4,kind:'quality_degraded'}),'4:quality_degraded:0');
});
test('only fixed BPS event kinds use Bark delivery labels',()=>{
 for(const kind of ['auto_403_disabled','image_capacity_rejected','image_cleanup_failed','recovery_suggested']) assert.equal(isBasispointsOpsEvent({kind}),true);
 for(const kind of ['quality_degraded','weekly_quota','unknown'])assert.equal(isBasispointsOpsEvent({kind}),false);
});
